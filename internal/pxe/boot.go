package pxe

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// iPXE boot script template.
// The ${mac} variable is expanded by iPXE itself at runtime.
//
// Boot script serves both BIOS (undionly.kpxe / Arch:00000) and UEFI (ipxe.efi
// / Arch:00007) clients using the universal iPXE syntax:
//
//   kernel <url> <cmdline>     — loads and prepares the kernel
//   initrd <url>               — loads the initrd (universal form, works in both modes)
//   boot                       — hands off to the kernel
//
// The initrd=initramfs.img parameter in the cmdline is NOT needed with this form
// because iPXE automatically associates the loaded initrd with the kernel when
// there is exactly one initrd loaded. Adding initrd= in the cmdline while also
// using `initrd <url>` can cause confusion in some iPXE builds.
//
// The --name form (`initrd --name initramfs.img`) is required ONLY when multiple
// initrds are loaded and must be referenced by name in the cmdline — skip it here.
//
// clustr.token is a short-lived node-scoped API key minted at PXE-serve time.
// The initramfs init script parses it from /proc/cmdline and exports CLUSTR_TOKEN
// so that `clustr deploy --auto` can authenticate against the server.
//
// clustr.ssh=1 enables the dropbear SSH server inside the initramfs for live
// deploy inspection. clustr.ssh.pass is a random per-boot password logged by
// the server at INFO level so the operator can find it via journalctl.
const bootScriptTemplate = `#!ipxe
set server-url {{.ServerURL}}
kernel ${server-url}/api/v1/boot/vmlinuz initrd=initramfs.img clustr.server=${server-url} clustr.mac=${mac} clustr.token={{.Token}} clustr.ssh=1 clustr.ssh.pass={{.SSHPass}}{{if .MulticastParams}} {{.MulticastParams}}{{end}} console=ttyS0,115200n8 console=tty0 earlyprintk=vga panic=60
initrd --name initramfs.img ${server-url}/api/v1/boot/initramfs.img
boot
`

var bootTmpl = template.Must(template.New("boot").Parse(bootScriptTemplate))

// bootScriptData holds template vars for the iPXE boot script.
type bootScriptData struct {
	ServerURL       string
	Token           string // full clustr-node-<hex> token, embedded in kernel cmdline
	SSHPass         string // random per-boot password for dropbear SSH debug access
	MulticastParams string // optional "clustr.multicast=1 clustr.session_poll_url=..." params
}

// randomSSHPass generates a short random hex string for use as a per-boot
// dropbear password. 4 bytes → 8 hex chars: short enough to type, random
// enough for an ephemeral debug credential on a private provisioning network.
func randomSSHPass() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "clustrdev" // fallback if entropy source fails
	}
	return hex.EncodeToString(b)
}

// GenerateBootScript renders the iPXE boot script for the given server URL and
// node-scoped deploy token. The MAC is left as an iPXE variable (${mac}) so iPXE
// fills it at runtime.
//
// A random SSH debug password is generated per call and embedded in the kernel
// cmdline as clustr.ssh.pass=<value>. The password is returned as the second
// return value so callers can log it at INFO level — operators can then retrieve
// it via journalctl when they need to SSH into a deploying node.
//
// multicastParams is optional; when non-empty it is appended verbatim to the
// kernel cmdline (e.g. "clustr.multicast=1 clustr.session_poll_url=<url>").
func GenerateBootScript(serverURL, token, multicastParams string) (script []byte, sshPass string, err error) {
	sshPass = randomSSHPass()
	data := bootScriptData{
		ServerURL:       serverURL,
		Token:           token,
		SSHPass:         sshPass,
		MulticastParams: multicastParams,
	}
	var buf bytes.Buffer
	if execErr := bootTmpl.Execute(&buf, data); execErr != nil {
		return nil, "", fmt.Errorf("pxe/boot: render boot script: %w", execErr)
	}
	return buf.Bytes(), sshPass, nil
}

// diskBootBIOSTemplate is the iPXE response for BIOS-firmware nodes in NodeStateDeployed.
//
// Presents a 5-second boot menu. The default action "disk" uses `sanboot` instead
// of `exit` because SeaBIOS (Proxmox/QEMU VMs) restarts the PXE loop on exit rather
// than falling through to the next boot order entry, causing an infinite PXE loop.
// sanboot uses iPXE's INT 13h chainload to explicitly boot the first local disk (0x80).
//
// The "reimage" option re-chains to the boot endpoint with force_reimage=1, which the
// server uses to mark the node for reimage and serve the deploy initramfs on the next
// PXE request.
//
// Visual layout: branded header is emitted via echo BEFORE the menu command so that
// iPXE's console renderer (which honours whitespace) displays it centred. The menu
// command creates its own rendering context that strips leading spaces from item --gap
// text, so all decorative/info lines live outside the menu block.
const diskBootBIOSTemplate = `#!ipxe

# --- header (echo preserves whitespace; item --gap strips leading spaces) ---
echo
echo                    c l o n r   B o o t   M a n a g e r
echo                                                  {{.Version}}
echo
echo                    Node : {{.Hostname}}
echo                    MAC  : ${mac}
echo

# --- menu ------------------------------------------------------------------
menu Select boot option:
item --gap --
item --default disk --timeout 5000 disk   Boot from disk      [auto 5s]
item reimage                              Reimage this node{{if .MulticastEnabled}}
item reimage-fleet                        Reimage via multicast (fleet){{end}}{{range .ExtraEntries}}
item entry_{{.ID}}                        {{.Name}}{{end}}
item --gap --
choose --default disk --timeout 5000 target && goto ${target} || goto disk

:disk
sanboot --no-describe --drive 0x80 || exit

:reimage
chain {{.ServerURL}}/api/v1/boot/ipxe?mac=${mac}&force_reimage=1 || goto disk
{{if .MulticastEnabled}}
:reimage-fleet
chain {{.ServerURL}}/api/v1/boot/ipxe?mac=${mac}&force_reimage=1&multicast=1 || goto disk
{{end}}{{range .ExtraEntries}}
:entry_{{.ID}}
echo Booting {{.Name}}...
kernel {{.KernelURL}}{{if .Cmdline}} {{.Cmdline}}{{end}}
{{if .InitrdURL}}initrd {{.InitrdURL}}
{{end}}boot || goto disk
{{end}}`

// diskBootUEFITemplate is the iPXE response for UEFI-firmware nodes in NodeStateDeployed.
//
// Presents a 5-second boot menu. The default "disk" action issues `exit` to return
// control to the UEFI firmware. Firmware walks the Proxmox/BMC boot order:
// net0 (PXE) is retried — the server returns this same disk-boot script and iPXE
// exits again — then scsi0 is tried and UEFI removable-media auto-discovery loads
// \EFI\BOOT\BOOTX64.EFI from the ESP (written by grub2-install --removable --no-nvram).
// No custom NVRAM OS entry is created. Removable-media discovery works across reimages,
// NVRAM wipes, AC loss, and cold-aisle resets — it does not depend on PARTUUID.
//
// This is symmetric with the BIOS `sanboot` path: in both cases the server makes
// the routing decision in iPXE then hands off to the OS-installed bootloader.
// No server-side grub binary, no HTTP chain-boot of GRUB.
//
// ADR: post-deploy UEFI uses `exit` + removable-media discovery, not chain-boot or
// NVRAM entries — see docs/boot-architecture.md §8
//
// The "reimage" option re-chains to the boot endpoint with force_reimage=1.
//
// Visual layout: branded header is emitted via echo BEFORE the menu command so that
// iPXE's console renderer (which honours whitespace) displays it centred. The menu
// command creates its own rendering context that strips leading spaces from item --gap
// text, so all decorative/info lines live outside the menu block.
const diskBootUEFITemplate = `#!ipxe

# --- header (echo preserves whitespace; item --gap strips leading spaces) ---
echo
echo                    c l o n r   B o o t   M a n a g e r
echo                                                  {{.Version}}
echo
echo                    Node : {{.Hostname}}
echo                    MAC  : ${mac}
echo

# --- menu ------------------------------------------------------------------
menu Select boot option:
item --gap --
item --default disk --timeout 5000 disk   Boot from disk      [auto 5s]
item reimage                              Reimage this node{{if .MulticastEnabled}}
item reimage-fleet                        Reimage via multicast (fleet){{end}}{{range .ExtraEntries}}
item entry_{{.ID}}                        {{.Name}}{{end}}
item --gap --
choose --default disk --timeout 5000 target && goto ${target} || goto disk

:disk
exit

:reimage
chain {{.ServerURL}}/api/v1/boot/ipxe?mac=${mac}&force_reimage=1 || goto disk
{{if .MulticastEnabled}}
:reimage-fleet
chain {{.ServerURL}}/api/v1/boot/ipxe?mac=${mac}&force_reimage=1&multicast=1 || goto disk
{{end}}{{range .ExtraEntries}}
:entry_{{.ID}}
echo Booting {{.Name}}...
kernel {{.KernelURL}}{{if .Cmdline}} {{.Cmdline}}{{end}}
{{if .InitrdURL}}initrd {{.InitrdURL}}
{{end}}boot || goto disk
{{end}}`

// waitRetryTemplate is served to nodes in reimage_pending state that have no
// base_image_id assigned. The node sleeps 60 seconds in iPXE and retries,
// looping until the operator assigns an image and triggers a fresh PXE boot.
// Using iPXE's built-in sleep+goto avoids an immediate flood of PXE requests
// while the operator is still configuring the node.
const waitRetryTemplate = `#!ipxe
echo Node {{.Hostname}} is pending reimage but has no image assigned -- retrying in 60s
:retry
sleep 60
chain ${next-server}/api/v1/boot/ipxe?mac=${mac} || goto retry
`

var waitRetryTmpl = template.Must(template.New("wait-retry").Parse(waitRetryTemplate))

var diskBootBIOSTmpl = template.Must(template.New("diskboot-bios").Parse(diskBootBIOSTemplate))
var diskBootUEFITmpl = template.Must(template.New("diskboot-uefi").Parse(diskBootUEFITemplate))

// diskBootScriptData holds template vars for the disk boot script.
type diskBootScriptData struct {
	Hostname  string
	// ServerURL is the public URL of clustr-serverd (e.g. http://10.99.0.1:8080).
	// Used to build the reimage re-chain URL in the boot menu.
	// The ${mac} variable in the template is expanded by iPXE at runtime.
	ServerURL string
	// Version is the clustr server version string displayed in the boot menu.
	Version string
	// ExtraEntries are operator-defined boot entries from the boot_entries table.
	// Each enabled entry is appended to the menu and rendered as a label block.
	ExtraEntries []api.BootEntry
	// MulticastEnabled controls whether the "Reimage via multicast (fleet)" menu
	// item is shown. Set by GenerateDiskBootScript when multicast_config.enabled=true.
	MulticastEnabled bool

	// Sprint 34 BOOT-SETTINGS-MODAL: when PersistedEntry is non-nil the boot
	// menu defaults to the operator-pinned entry instead of the standard
	// disk-boot item.  PersistedKernelCmdline (if set) is appended to the
	// kernel cmdline of any chained entry.  Both are populated by the boot
	// handler from NodeConfig.NetbootMenuEntry / NodeConfig.KernelCmdline.
	PersistedEntry         *api.BootEntry
	PersistedKernelCmdline string
}

// GenerateWaitRetryScript returns an iPXE script for nodes in reimage_pending
// state that have no base_image_id assigned. The node sleeps 60 seconds and
// re-chains to the boot endpoint so the operator can assign an image without
// requiring a manual BMC power cycle.
func GenerateWaitRetryScript(hostname string) ([]byte, error) {
	data := diskBootScriptData{Hostname: hostname}
	var buf bytes.Buffer
	if err := waitRetryTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render wait-retry script: %w", err)
	}
	return buf.Bytes(), nil
}

// GenerateDiskBootScript returns an iPXE script that boots the node from local
// disk. Used for nodes in NodeStateDeployed and related states.
//
// firmware must be "bios" or "uefi" (case-insensitive). Any value other than
// "bios" is treated as UEFI (fail-safe: UEFI is the default for new images).
//
// serverURL is the public URL of clustr-serverd (e.g. http://10.99.0.1:8080).
// It is embedded in the boot script for the reimage re-chain URL.
// The ${mac} variable in the script is expanded by iPXE at runtime.
//
// extraEntries is the list of enabled boot_entries rows to append to the menu.
// Pass nil or an empty slice to render the standard two-item menu.
//
// multicastEnabled controls whether the "Reimage via multicast (fleet)" item
// appears in the menu. Pass true when multicast_config.enabled=true in the DB.
func GenerateDiskBootScript(hostname, firmware, serverURL, version string, extraEntries []api.BootEntry, multicastEnabled bool) ([]byte, error) {
	return GenerateDiskBootScriptWithSettings(hostname, firmware, serverURL, version,
		extraEntries, multicastEnabled, nil, "")
}

// GenerateDiskBootScriptWithSettings is GenerateDiskBootScript plus the Sprint 34
// BOOT-SETTINGS-MODAL fields:
//
//   - persistedEntry: when non-nil, the operator pinned this boot_entries row as
//     the default for the next PXE boot.  The script auto-selects that entry
//     instead of the normal disk-boot item.  A nil persistedEntry preserves the
//     v0.1.22 default-disk-boot behaviour.
//   - persistedKernelCmdline: when non-empty, appended verbatim to the kernel
//     cmdline of any chained entry.  Used for serial console pinning or
//     one-off debug flags.  Empty string means "no override".
//
// Both fields are validated upstream by the BOOT-SETTINGS-MODAL handler — the
// renderer assumes well-formed input here.
func GenerateDiskBootScriptWithSettings(
	hostname, firmware, serverURL, version string,
	extraEntries []api.BootEntry,
	multicastEnabled bool,
	persistedEntry *api.BootEntry,
	persistedKernelCmdline string,
) ([]byte, error) {
	tmpl := diskBootUEFITmpl
	if strings.EqualFold(firmware, "bios") {
		tmpl = diskBootBIOSTmpl
	}
	if extraEntries == nil {
		extraEntries = []api.BootEntry{}
	}
	data := diskBootScriptData{
		Hostname:               hostname,
		ServerURL:              serverURL,
		Version:                version,
		ExtraEntries:           extraEntries,
		MulticastEnabled:       multicastEnabled,
		PersistedEntry:         persistedEntry,
		PersistedKernelCmdline: persistedKernelCmdline,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render disk boot script: %w", err)
	}
	return buf.Bytes(), nil
}
