// Package proxmox implements a power.Provider backed by the Proxmox VE REST API.
// It controls QEMU VMs via the PVE API and is the right backend for test labs
// where nodes are VMs with no real BMC.
//
// Authentication: username+password → PVEAuthCookie + CSRFPreventionToken,
// cached for ~2 hours to avoid re-authenticating on every operation.
//
// API token auth (tokenID + tokenSecret) is also supported; when both are
// present the token path is used and the cookie cache is bypassed entirely.
//
// Boot-order semantics: Proxmox persists VM config changes ONLY on stop+start.
// /status/reset (warm reset) does NOT commit pending boot config changes.
// Therefore SetNextBoot and SetPersistentBootOrder on a running VM perform an
// explicit stop → config-write → start sequence so the new order takes effect
// on the next boot. This is the Proxmox-specific implementation of the one-shot
// SetNextBoot semantic documented in internal/power/power.go.
//
// See docs/boot-architecture.md §10.
package proxmox

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sqoia-dev/clustr/internal/power"
)

// Provider implements power.Provider for a single Proxmox QEMU VM.
type Provider struct {
	apiURL      string // e.g. "https://192.168.1.223:8006"
	username    string // e.g. "root@pam"
	password    string
	tokenID     string // e.g. "root@pam!mytoken"
	tokenSecret string
	node        string // Proxmox node name, e.g. "pve"
	vmid        int    // VM ID, e.g. 202

	httpClient *http.Client

	mu           sync.Mutex
	ticket       string    // cached PVEAuthCookie value
	csrfToken    string    // cached CSRFPreventionToken value
	ticketExpiry time.Time // tickets expire after 2h; refresh at 1h50m
}

// New constructs a Proxmox Provider from a ProviderConfig.
//
// Required fields:
//   - "api_url"  — Proxmox API base URL, e.g. "https://192.168.1.223:8006"
//   - "node"     — Proxmox node name, e.g. "pve"
//   - "vmid"     — VM ID as a decimal string, e.g. "202"
//
// Auth fields (one pair required):
//   - "username" + "password"           — username/password auth
//   - "token_id" + "token_secret"       — API token auth (preferred)
//
// Optional:
//   - "insecure" — "true" to skip TLS certificate verification (self-signed)
func New(cfg power.ProviderConfig) (power.Provider, error) {
	apiURL := strings.TrimRight(cfg.Fields["api_url"], "/")
	if apiURL == "" {
		return nil, fmt.Errorf("proxmox provider: missing required field \"api_url\"")
	}
	node := cfg.Fields["node"]
	if node == "" {
		return nil, fmt.Errorf("proxmox provider: missing required field \"node\"")
	}
	vmidStr := cfg.Fields["vmid"]
	if vmidStr == "" {
		return nil, fmt.Errorf("proxmox provider: missing required field \"vmid\"")
	}
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil {
		return nil, fmt.Errorf("proxmox provider: invalid vmid %q: %w", vmidStr, err)
	}

	username := cfg.Fields["username"]
	password := cfg.Fields["password"]
	tokenID := cfg.Fields["token_id"]
	tokenSecret := cfg.Fields["token_secret"]

	if tokenID == "" && (username == "" || password == "") {
		return nil, fmt.Errorf("proxmox provider: must supply either (username+password) or (token_id+token_secret)")
	}

	insecure := cfg.Fields["insecure"] == "true"
	transport := http.DefaultTransport
	if insecure {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}

	return &Provider{
		apiURL:      apiURL,
		username:    username,
		password:    password,
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		node:        node,
		vmid:        vmid,
		httpClient:  &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}, nil
}

// Register registers the "proxmox" factory with the given registry.
func Register(r *power.Registry) {
	r.Register("proxmox", New)
}

// Name implements power.Provider.
func (p *Provider) Name() string { return "proxmox" }

// ─── power.Provider implementation ───────────────────────────────────────────

// Status returns the current power state of the VM.
func (p *Provider) Status(ctx context.Context) (power.PowerStatus, error) {
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/current", p.node, p.vmid)
	if err := p.get(ctx, path, &resp); err != nil {
		return power.PowerUnknown, fmt.Errorf("proxmox: status: %w", err)
	}
	switch resp.Data.Status {
	case "running":
		return power.PowerOn, nil
	case "stopped":
		return power.PowerOff, nil
	default:
		return power.PowerUnknown, nil
	}
}

// PowerOn starts the VM.
func (p *Provider) PowerOn(ctx context.Context) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/start", p.node, p.vmid)
	if err := p.post(ctx, path, nil, nil); err != nil {
		return fmt.Errorf("proxmox: power on: %w", err)
	}
	return nil
}

// PowerOff forcefully stops the VM (equivalent to pulling the plug).
func (p *Provider) PowerOff(ctx context.Context) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/stop", p.node, p.vmid)
	if err := p.post(ctx, path, nil, nil); err != nil {
		return fmt.Errorf("proxmox: power off: %w", err)
	}
	return nil
}

// PowerCycle hard-cycles the VM. If the VM is stopped it starts it; if it is
// running it issues a reset. Proxmox returns HTTP 500 when /status/reset is
// called on a stopped VM, so we check the current status first.
func (p *Provider) PowerCycle(ctx context.Context) error {
	status, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: power cycle: get status: %w", err)
	}
	switch status {
	case power.PowerOff:
		return p.PowerOn(ctx)
	case power.PowerOn:
		return p.Reset(ctx)
	default:
		return fmt.Errorf("proxmox: power cycle: unexpected vm status %q", status)
	}
}

// Reset issues a hard reset to the VM. The VM must already be running;
// callers that need to handle stopped VMs should use PowerCycle instead.
func (p *Provider) Reset(ctx context.Context) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/reset", p.node, p.vmid)
	if err := p.post(ctx, path, nil, nil); err != nil {
		return fmt.Errorf("proxmox: reset: %w", err)
	}
	return nil
}

// SetNextBoot sets the boot order so that dev is first on the next boot.
//
// Proxmox has no native one-shot boot-override concept. This implementation
// writes the persistent VM boot config and, if the VM is currently running,
// performs an explicit stop → config-write → start so the new order is
// committed and takes effect immediately. A subsequent PowerCycle (warm reset)
// will therefore boot into the new order rather than the stale running config.
//
// If the VM is already stopped, only the config write is performed; the caller's
// subsequent PowerOn/PowerCycle will boot into the new order.
//
// See docs/boot-architecture.md §10.
func (p *Provider) SetNextBoot(ctx context.Context, dev power.BootDevice) error {
	status, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: SetNextBoot: pre-check status: %w", err)
	}
	if err := p.setBootOrder(ctx, dev); err != nil {
		return err
	}
	if status == power.PowerOn {
		// Proxmox commits pending VM config only on stop+start, not on
		// /status/reset (warm reset). Stop the VM, then start it so the
		// new boot order is in the running config before we return.
		// See docs/boot-architecture.md §10.7.
		if err := p.PowerOff(ctx); err != nil {
			return fmt.Errorf("proxmox: SetNextBoot: stop to commit pending config: %w", err)
		}
		if err := p.waitForStatus(ctx, power.PowerOff, 60*time.Second); err != nil {
			return fmt.Errorf("proxmox: SetNextBoot: wait for stop: %w", err)
		}
		if err := p.PowerOn(ctx); err != nil {
			return fmt.Errorf("proxmox: SetNextBoot: start after config commit: %w", err)
		}
	}
	return nil
}

// SetPersistentBootOrder sets the persistent boot order for the VM.
//
// Writes the VM boot config and, if the VM is currently running, performs an
// explicit stop → config-write → start so the new order is committed. This is
// critical for the post-deploy flip-back to disk-first: Proxmox only commits
// pending VM config on a full stop+start, not on /status/reset (warm reset).
//
// See docs/boot-architecture.md §10.
func (p *Provider) SetPersistentBootOrder(ctx context.Context, order []power.BootDevice) error {
	if len(order) == 0 {
		return fmt.Errorf("proxmox: SetPersistentBootOrder: order must not be empty")
	}
	status, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: SetPersistentBootOrder: pre-check status: %w", err)
	}
	// Use the first device as the primary boot device.
	if err := p.setBootOrder(ctx, order[0]); err != nil {
		return err
	}
	if status == power.PowerOn {
		// Commit pending config via stop+start. See docs/boot-architecture.md §10.7.
		if err := p.PowerOff(ctx); err != nil {
			return fmt.Errorf("proxmox: SetPersistentBootOrder: stop to commit pending config: %w", err)
		}
		if err := p.waitForStatus(ctx, power.PowerOff, 60*time.Second); err != nil {
			return fmt.Errorf("proxmox: SetPersistentBootOrder: wait for stop: %w", err)
		}
		if err := p.PowerOn(ctx); err != nil {
			return fmt.Errorf("proxmox: SetPersistentBootOrder: start after config commit: %w", err)
		}
	}
	return nil
}

// waitForStatus polls the VM status every 500ms until it matches want or the
// timeout elapses. Returns an error if the status does not match within timeout.
func (p *Provider) waitForStatus(ctx context.Context, want power.PowerStatus, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		got, err := p.Status(ctx)
		if err != nil {
			return fmt.Errorf("proxmox: waitForStatus: poll: %w", err)
		}
		if got == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxmox: waitForStatus: timed out after %s waiting for status %q (last: %q)", timeout, want, got)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("proxmox: waitForStatus: context cancelled: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// setBootOrder updates the Proxmox VM config with the appropriate boot order
// string for the given primary BootDevice.
func (p *Provider) setBootOrder(ctx context.Context, dev power.BootDevice) error {
	var bootOrder string
	switch dev {
	case power.BootPXE:
		// net0 first so the VM PXE-boots; scsi0 as fallback.
		bootOrder = "order=net0;scsi0"
	case power.BootDisk:
		// scsi0 (primary disk) first; net0 as fallback.
		bootOrder = "order=scsi0;net0"
	case power.BootCD:
		bootOrder = "order=ide2;scsi0;net0"
	default:
		return fmt.Errorf("proxmox: unsupported boot device %q", dev)
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", p.node, p.vmid)
	body := map[string]string{"boot": bootOrder}
	if err := p.put(ctx, path, body, nil); err != nil {
		return fmt.Errorf("proxmox: set boot order: %w", err)
	}
	return nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

// get performs a GET against the Proxmox API and decodes the JSON response into out.
func (p *Provider) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	if err := p.setAuth(ctx, req); err != nil {
		return err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return p.decodeResponse(resp, out)
}

// post performs a POST against the Proxmox API.
// body may be nil (no request body) or a map[string]string (form-encoded).
func (p *Provider) post(ctx context.Context, path string, body map[string]string, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		form := url.Values{}
		for k, v := range body {
			form.Set(k, v)
		}
		reqBody = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/api2/json"+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err := p.setAuth(ctx, req); err != nil {
		return err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return p.decodeResponse(resp, out)
}

// put performs a PUT against the Proxmox API with a form-encoded body.
func (p *Provider) put(ctx context.Context, path string, body map[string]string, out interface{}) error {
	form := url.Values{}
	for k, v := range body {
		form.Set(k, v)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.apiURL+"/api2/json"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := p.setAuth(ctx, req); err != nil {
		return err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return p.decodeResponse(resp, out)
}

// setAuth attaches the appropriate credentials to req.
// API token auth is stateless; cookie/CSRF auth requires a cached ticket.
func (p *Provider) setAuth(ctx context.Context, req *http.Request) error {
	if p.tokenID != "" {
		req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", p.tokenID, p.tokenSecret))
		return nil
	}
	// Cookie-based auth — ensure we have a fresh ticket.
	ticket, csrf, err := p.ensureTicket(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: authenticate: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})
	req.Header.Set("CSRFPreventionToken", csrf)
	return nil
}

// ensureTicket returns the cached PVEAuthCookie and CSRFPreventionToken,
// re-authenticating if the ticket is absent or has less than 10 minutes left.
func (p *Provider) ensureTicket(ctx context.Context) (ticket, csrf string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ticket != "" && time.Now().Before(p.ticketExpiry.Add(-10*time.Minute)) {
		return p.ticket, p.csrfToken, nil
	}

	ticket, csrf, err = p.authenticate(ctx)
	if err != nil {
		return "", "", err
	}
	p.ticket = ticket
	p.csrfToken = csrf
	// Proxmox tickets are valid for 2 hours.
	p.ticketExpiry = time.Now().Add(2 * time.Hour)
	return ticket, csrf, nil
}

// authenticate performs POST /api2/json/access/ticket and returns the new
// PVEAuthCookie value and CSRFPreventionToken.
func (p *Provider) authenticate(ctx context.Context) (ticket, csrf string, err error) {
	form := url.Values{
		"username": {p.username},
		"password": {p.password},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.apiURL+"/api2/json/access/ticket",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("auth failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var authResp struct {
		Data struct {
			Ticket              string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", "", fmt.Errorf("decode auth response: %w", err)
	}
	if authResp.Data.Ticket == "" {
		return "", "", fmt.Errorf("auth response contained no ticket")
	}
	return authResp.Data.Ticket, authResp.Data.CSRFPreventionToken, nil
}

// decodeResponse checks the HTTP status and optionally decodes JSON into out.
// out may be nil when the caller doesn't need the response body.
func (p *Provider) decodeResponse(resp *http.Response, out interface{}) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Try to extract the Proxmox error message from the JSON envelope.
		var errResp struct {
			Errors map[string]string `json:"errors"`
		}
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &errResp) == nil && len(errResp.Errors) > 0 {
			parts := make([]string, 0, len(errResp.Errors))
			for k, v := range errResp.Errors {
				parts = append(parts, k+": "+v)
			}
			msg = strings.Join(parts, "; ")
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out == nil {
		// Drain body to allow connection reuse.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	// Buffer the body so we can give a useful error if JSON is malformed.
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if err := json.NewDecoder(bytes.NewReader(buf)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w (body: %.200s)", err, buf)
	}
	return nil
}
