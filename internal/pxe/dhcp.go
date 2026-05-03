// Package pxe provides a built-in DHCP/TFTP/iPXE server for clustr-serverd.
// It handles PXE boot requests from bare-metal nodes on the provisioning network,
// assigns IPs, and chainloads into iPXE.
package pxe

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/rs/zerolog/log"
)

// leaseEntry holds an IP lease for a client.
type leaseEntry struct {
	IP        net.IP
	ExpiresAt time.Time
}

// NOTE: DHCP leases are stored in memory only and are lost on server restart.
// This is an intentional tradeoff: clustr targets isolated provisioning networks
// where nodes PXE-boot on demand. A node that re-appears after a restart will
// simply acquire a new lease from the pool. Persistent lease storage (e.g.
// a SQLite file) is not implemented and is not required for this use case.

// DHCPServer is a lightweight DHCP server that only responds to PXE clients.
type DHCPServer struct {
	iface      string
	serverIP   net.IP
	rangeStart net.IP
	rangeEnd   net.IP
	subnetCIDR int    // prefix length for DHCP option 1 (subnet mask), e.g. 24
	leaseDur   time.Duration
	httpPort   string // port of the clustr-serverd HTTP API (for iPXE chainload URL)

	mu     sync.Mutex
	leases map[string]leaseEntry // keyed by MAC string
	pool   []net.IP              // pre-built list of IPs in range

	server *server4.Server

	// OnSwitchDiscovered is called after a lease is issued to a client whose
	// Option 60 vendor class identifies it as a known switch vendor.
	// Arguments: mac (hardware address string), vendor (lowercase identifier),
	// ip (the leased IP string). May be nil — if so, detection is skipped.
	OnSwitchDiscovered func(mac, vendor, ip string)

	// ResolveReservedIP returns the static IP assigned to a registered node by
	// its MAC address. When non-nil and the lookup returns a non-nil IP, the
	// DHCP server serves that IP instead of allocating from the pool. This
	// implements DHCP reservations for nodes that have a static IP configured
	// in their InterfaceConfig. May be nil — if so, reservation lookup is skipped.
	ResolveReservedIP func(mac string) net.IP

	// IsIPReservedByOtherMAC reports whether the given IP is already configured
	// as a static address on a different node (i.e. a node with a different MAC).
	// Used during pool scan to prevent assigning a pool IP that is reserved for
	// another registered node. Fail-open: if nil or returns false, allocation
	// proceeds normally. May be nil — if so, the check is skipped.
	IsIPReservedByOtherMAC func(ip string, mac string) bool
}

// detectSwitch inspects a DHCP Option 60 vendor class string and returns
// (true, vendor) when the fingerprint matches a known switch vendor.
// Matching is case-insensitive on the known prefix/substring patterns.
func detectSwitch(vendorClass string) (bool, string) {
	vc := strings.ToLower(vendorClass)
	switch {
	case strings.Contains(vc, "arista"):
		return true, "arista"
	case strings.Contains(vc, "dell emc") || strings.Contains(vc, "dell force10") || strings.Contains(vc, "dell"):
		return true, "dell"
	case strings.Contains(vc, "juniper"):
		return true, "juniper"
	case strings.Contains(vc, "cisco"):
		return true, "cisco"
	case strings.Contains(vc, "mellanox"):
		return true, "mellanox"
	case strings.Contains(vc, "aruba") || strings.Contains(vc, "arubaap"):
		return true, "hpe-aruba"
	}
	return false, ""
}

// newDHCPServer creates a DHCPServer from config. It does not start listening.
// subnetCIDR is the prefix length advertised via DHCP option 1; must be 1–30.
func newDHCPServer(iface string, serverIP net.IP, ipRange string, httpPort string, subnetCIDR int) (*DHCPServer, error) {
	start, end, err := parseIPRange(ipRange)
	if err != nil {
		return nil, fmt.Errorf("pxe/dhcp: parse ip range: %w", err)
	}

	pool := buildPool(start, end)
	if len(pool) == 0 {
		return nil, fmt.Errorf("pxe/dhcp: ip range %s produced no addresses", ipRange)
	}

	if subnetCIDR < 1 || subnetCIDR > 30 {
		return nil, fmt.Errorf("pxe/dhcp: SubnetCIDR %d out of range [1, 30]", subnetCIDR)
	}

	return &DHCPServer{
		iface:      iface,
		serverIP:   serverIP,
		rangeStart: start,
		rangeEnd:   end,
		subnetCIDR: subnetCIDR,
		leaseDur:   24 * time.Hour,
		httpPort:   httpPort,
		leases:     make(map[string]leaseEntry),
		pool:       pool,
	}, nil
}

// Start begins listening for DHCP requests on the configured interface.
func (d *DHCPServer) Start(ctx context.Context) error {
	handler := func(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
		d.handleDHCP(conn, peer, req)
	}

	srv, err := server4.NewServer(d.iface, nil, handler)
	if err != nil {
		return fmt.Errorf("pxe/dhcp: create server on %s: %w", d.iface, err)
	}
	d.server = srv

	log.Warn().Msg("DHCP leases are ephemeral — all lease state will be lost on server restart")
	log.Info().Str("interface", d.iface).Str("server_ip", d.serverIP.String()).
		Str("range", fmt.Sprintf("%s-%s", d.rangeStart, d.rangeEnd)).
		Msg("DHCP server listening")

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	if err := srv.Serve(); err != nil {
		// Serve returns on Close -- only treat as error if context is not done.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("pxe/dhcp: serve: %w", err)
	}
	return nil
}

// handleDHCP is the per-packet handler called by the server4.Server.
func (d *DHCPServer) handleDHCP(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
	vendorClass := req.ClassIdentifier()
	userClass := string(req.Options.Get(dhcpv4.OptionUserClassInformation))

	isPXEClient := strings.HasPrefix(vendorClass, "PXEClient")
	isIPXE := strings.Contains(userClass, "iPXE")
	// isNonPXE covers deployed-OS clients that request DHCP for network
	// configuration after first boot. The clustr provisioning network is also
	// the OS management network, so we serve all DHCP clients — not just PXE.
	isNonPXE := !isPXEClient && !isIPXE

	mac := req.ClientHWAddr.String()
	log.Debug().Str("mac", mac).Str("type", req.MessageType().String()).
		Bool("ipxe", isIPXE).Bool("non_pxe", isNonPXE).Msg("DHCP request")

	switch req.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		d.handleDiscover(conn, peer, req, isIPXE)
	case dhcpv4.MessageTypeRequest:
		d.handleRequest(conn, peer, req, isIPXE)
	}
}

func (d *DHCPServer) handleDiscover(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4, isIPXE bool) {
	mac := req.ClientHWAddr.String()
	vendorClass := req.ClassIdentifier()
	userClass := string(req.Options.Get(dhcpv4.OptionUserClassInformation))

	ip := d.acquireOrAssignIP(mac)
	if ip == nil {
		log.Warn().Str("mac", mac).Msg("DHCP pool exhausted")
		return
	}

	resp, err := dhcpv4.NewReplyFromRequest(req)
	if err != nil {
		log.Error().Err(err).Msg("DHCP: build reply")
		return
	}
	resp.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeOffer))
	d.populateBootOptions(resp, req, ip, isIPXE)

	bootFile := bootFilename(req, isIPXE, d.serverIP, d.httpPort)
	log.Info().
		Str("mac", mac).
		Str("vendor_class", vendorClass).
		Str("user_class", userClass).
		Str("assigned_ip", ip.String()).
		Str("boot_filename", bootFile).
		Msg("DHCP DISCOVER")

	if _, err := conn.WriteTo(resp.ToBytes(), peer); err != nil {
		log.Error().Err(err).Msg("DHCP: send offer")
	}
}

func (d *DHCPServer) handleRequest(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4, isIPXE bool) {
	// If the Request carries a ServerIdentifier option that names a different
	// DHCP server, silently ignore it. This prevents duplicate ACKs in
	// environments where multiple DHCP servers are present on the same segment.
	if sid := req.ServerIdentifier(); sid != nil && !sid.Equal(d.serverIP) {
		log.Debug().
			Str("mac", req.ClientHWAddr.String()).
			Str("requested_server", sid.String()).
			Str("our_server", d.serverIP.String()).
			Msg("DHCP REQUEST for different server — ignoring")
		return
	}

	ip := d.acquireOrAssignIP(req.ClientHWAddr.String())
	if ip == nil {
		log.Warn().Str("mac", req.ClientHWAddr.String()).Msg("DHCP pool exhausted on request")
		return
	}

	resp, err := dhcpv4.NewReplyFromRequest(req)
	if err != nil {
		log.Error().Err(err).Msg("DHCP: build reply")
		return
	}
	resp.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeAck))
	d.populateBootOptions(resp, req, ip, isIPXE)

	mac := req.ClientHWAddr.String()
	log.Info().Str("mac", mac).Str("ip", ip.String()).Msg("DHCP ACK")

	if _, err := conn.WriteTo(resp.ToBytes(), peer); err != nil {
		log.Error().Err(err).Msg("DHCP: send ack")
		return
	}

	// After successfully ACK-ing, check if this is a switch and fire the callback.
	if d.OnSwitchDiscovered != nil {
		vendorClass := req.ClassIdentifier()
		if isSwitch, vendor := detectSwitch(vendorClass); isSwitch {
			log.Info().Str("mac", mac).Str("vendor", vendor).Str("ip", ip.String()).
				Msg("DHCP: switch vendor fingerprint detected — notifying network manager")
			go d.OnSwitchDiscovered(mac, vendor, ip.String())
		}
	}
}

// populateBootOptions fills in yiaddr, next-server, boot-file, and lease time.
func (d *DHCPServer) populateBootOptions(resp *dhcpv4.DHCPv4, req *dhcpv4.DHCPv4, ip net.IP, isIPXE bool) {
	resp.YourIPAddr = ip
	resp.ServerIPAddr = d.serverIP

	// Subnet mask — derived from configured SubnetCIDR (default /24).
	resp.UpdateOption(dhcpv4.OptSubnetMask(net.CIDRMask(d.subnetCIDR, 32)))
	resp.UpdateOption(dhcpv4.OptRouter(d.serverIP))
	resp.UpdateOption(dhcpv4.OptIPAddressLeaseTime(d.leaseDur))
	resp.UpdateOption(dhcpv4.OptServerIdentifier(d.serverIP))

	// Advertise the clustr server as the DNS resolver for PXE clients.
	//
	// Without this option the PXE initramfs has no /etc/resolv.conf (udhcpc
	// does not write one when the DHCP response carries no DNS option), so any
	// outbound name lookup during the deploy finalize phase — notably the dnf
	// call in installSlurmInChroot — fails with "Couldn't resolve host name".
	//
	// The clustr server already runs dnsmasq (or the host's resolver) and has
	// outbound internet access, so pointing PXE clients at the server IP gives
	// them a working resolver for the duration of the deploy.
	resp.UpdateOption(dhcpv4.OptDNS(d.serverIP))

	// Next-server (siaddr) always points to self.
	resp.ServerIPAddr = d.serverIP

	bootFile := bootFilename(req, isIPXE, d.serverIP, d.httpPort)
	if bootFile != "" {
		resp.BootFileName = bootFile
	}
}

// bootFilename selects the appropriate boot file based on client architecture.
// Arch type is carried in DHCP option 93 (ClientSystemArchitectureType).
// If the client is already running iPXE, return the HTTP URL to the boot script.
//
// The URL includes ?mac=${mac} so that iPXE expands its own ${mac} variable
// when fetching the script. The boot handler uses this MAC to look up node state
// and return either the full deploy script or an exit (boot-from-disk) response,
// making the PXE server the source of truth for boot routing.
func bootFilename(req *dhcpv4.DHCPv4, isIPXE bool, serverIP net.IP, httpPort string) string {
	if isIPXE {
		// Already chainloaded into iPXE -- give it the boot script URL.
		// ${mac} is an iPXE variable; iPXE expands it before fetching the URL.
		return fmt.Sprintf("http://%s:%s/api/v1/boot/ipxe?mac=${mac}", serverIP, httpPort)
	}

	// Read option 93 -- client system architecture.
	// Values defined by RFC 4578 and IANA PXE Client Architecture Types:
	//   0x0000 = BIOS x86
	//   0x0006 = EFI IA32
	//   0x0007 = EFI x86-64 (standard)
	//   0x0009 = EFI x86-64 (alternate, used by some firmware)
	//   0x000a = EFI ARM 32-bit
	//   0x000b = EFI ARM 64-bit
	//   0x0010 = UEFI HTTP boot x86-64 (HTTPClient, e.g. OVMF with HTTP Boot enabled)
	archOpt := req.Options.Get(dhcpv4.OptionClientSystemArchitectureType)
	if len(archOpt) >= 2 {
		archType := uint16(archOpt[0])<<8 | uint16(archOpt[1])
		switch archType {
		case 16: // UEFI HTTP boot x86-64 (HTTPClient:Arch:00016)
			// For HTTP boot clients, the boot file must be a full HTTP URL, not a
			// bare filename. OVMF with HTTP Boot enabled treats Option 67 as a URL
			// when the vendor class is "HTTPClient". Sending just "ipxe.efi" causes
			// the firmware to attempt a TFTP transfer (or silently fail) because it
			// looks like a TFTP path, not an HTTP URL. The full URL causes OVMF to
			// fetch ipxe.efi over HTTP, which then chainloads into iPXE.
			return fmt.Sprintf("http://%s:%s/api/v1/boot/ipxe.efi", serverIP, httpPort)
		case 6, 7, 9: // EFI IA32 / EFI x86-64 (standard TFTP-based PXE)
			// Standard UEFI PXE clients use TFTP. Return the bare filename;
			// the TFTP server serves it from the tftpboot directory.
			return "ipxe.efi"
		case 10, 11: // EFI ARM 32-bit / EFI ARM 64-bit
			return "ipxe.efi"
		}
	}
	// Default: BIOS (arch type 0 or unset).
	return "undionly.kpxe"
}

// RecentLeases returns MAC addresses that received a DHCP lease within the last
// window duration. Used by GetActiveJobs to detect nodes that may be mid-PXE
// boot: any MAC with a lease issued in the last 30 seconds is considered
// "pxe_in_flight" — coarse but unblocking for the restart-safety check.
func (d *DHCPServer) RecentLeases(window time.Duration) []string {
	cutoff := time.Now().Add(-window)
	d.mu.Lock()
	defer d.mu.Unlock()
	var macs []string
	for mac, lease := range d.leases {
		// A lease whose expiry is still far in the future was issued recently
		// (ExpiresAt ≈ now + leaseDur). We detect "recently issued" by checking
		// whether (ExpiresAt - leaseDur) > cutoff, i.e. the lease was granted
		// after the cutoff moment.
		issuedAt := lease.ExpiresAt.Add(-d.leaseDur)
		if issuedAt.After(cutoff) {
			macs = append(macs, mac)
		}
	}
	return macs
}

// GetLeaseIP returns the IP currently leased to the given MAC, or nil if none.
// The MAC is matched case-insensitively.
func (d *DHCPServer) GetLeaseIP(mac string) net.IP {
	d.mu.Lock()
	defer d.mu.Unlock()
	mac = strings.ToLower(mac)
	if lease, ok := d.leases[mac]; ok && lease.ExpiresAt.After(time.Now()) {
		return lease.IP
	}
	return nil
}

// SubnetCIDR returns the prefix length configured for this DHCP server (e.g. 24).
func (d *DHCPServer) SubnetCIDR() int {
	return d.subnetCIDR
}

// acquireOrAssignIP finds an existing lease or assigns a new IP from the pool.
// If ResolveReservedIP is set and returns a non-nil IP for this MAC, that
// reserved IP is served instead of a pool address (DHCP reservation).
func (d *DHCPServer) acquireOrAssignIP(mac string) net.IP {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// Check existing (non-expired) lease first — covers both reserved and pool IPs.
	if lease, ok := d.leases[mac]; ok && lease.ExpiresAt.After(now) {
		return lease.IP
	}

	// Check for a static IP reservation before falling back to the pool.
	// ResolveReservedIP is called outside the lock window for the DB query, but
	// the lock is held here for the lease-map write — that is safe because the
	// callback must not call back into DHCPServer.
	if d.ResolveReservedIP != nil {
		// Release lock briefly while calling the potentially-blocking DB lookup.
		d.mu.Unlock()
		reservedIP := d.ResolveReservedIP(mac)
		d.mu.Lock()

		if reservedIP != nil {
			// Re-check the lease map in case another goroutine wrote one while
			// we had the lock released.
			if lease, ok := d.leases[mac]; ok && lease.ExpiresAt.After(now) {
				return lease.IP
			}
			d.leases[mac] = leaseEntry{
				IP:        cloneIP(reservedIP.To4()),
				ExpiresAt: now.Add(d.leaseDur),
			}
			log.Info().Str("mac", mac).Str("ip", reservedIP.String()).
				Msg("DHCP: serving reserved static IP for registered node")
			return d.leases[mac].IP
		}
	}

	// No reservation — collect IPs currently in use by non-expired leases.
	inUse := make(map[string]bool, len(d.leases))
	for _, l := range d.leases {
		if l.ExpiresAt.After(now) {
			inUse[l.IP.String()] = true
		}
	}

	// Pick first free IP from pool.
	for _, ip := range d.pool {
		ipStr := ip.String()
		if inUse[ipStr] {
			continue
		}

		// If the DB-backed callback is wired, check whether this pool IP is
		// already reserved for a different registered node. The callback may
		// block on a DB query, so we release the mutex around the call.
		if d.IsIPReservedByOtherMAC != nil {
			d.mu.Unlock()
			reservedElsewhere := d.IsIPReservedByOtherMAC(ipStr, mac)
			d.mu.Lock()

			// Re-check inUse after re-acquiring the lock in case another
			// goroutine assigned this IP while the lock was released.
			if inUse[ipStr] {
				continue
			}
			if reservedElsewhere {
				continue
			}
		}

		d.leases[mac] = leaseEntry{
			IP:        ip,
			ExpiresAt: now.Add(d.leaseDur),
		}
		log.Info().Str("mac", mac).Str("ip", ipStr).
			Msg("DHCP: serving pool IP for unregistered or unconfigured node")
		return ip
	}
	return nil
}

// parseIPRange parses a "start-end" IP range string.
func parseIPRange(r string) (net.IP, net.IP, error) {
	parts := strings.SplitN(r, "-", 2)
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("expected format start-end, got %q", r)
	}
	start := net.ParseIP(strings.TrimSpace(parts[0])).To4()
	end := net.ParseIP(strings.TrimSpace(parts[1])).To4()
	if start == nil || end == nil {
		return nil, nil, fmt.Errorf("invalid IP addresses in range %q", r)
	}
	if ipGreaterThan(start, end) {
		return nil, nil, fmt.Errorf("DHCP range start (%s) must be less than or equal to end (%s)", start, end)
	}
	return start, end, nil
}

// buildPool constructs a flat list of IPs from start to end (inclusive).
func buildPool(start, end net.IP) []net.IP {
	var pool []net.IP
	cur := cloneIP(start)
	for !ipGreaterThan(cur, end) {
		pool = append(pool, cloneIP(cur))
		incrementIP(cur)
	}
	return pool
}

func cloneIP(ip net.IP) net.IP {
	c := make(net.IP, len(ip))
	copy(c, ip)
	return c
}

func incrementIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func ipGreaterThan(a, b net.IP) bool {
	for i := range a {
		if a[i] > b[i] {
			return true
		}
		if a[i] < b[i] {
			return false
		}
	}
	return false
}
