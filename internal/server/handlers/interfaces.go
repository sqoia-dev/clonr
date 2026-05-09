// Package handlers — interfaces.go implements the typed multi-NIC editor
// endpoints (Sprint 44 MULTI-NIC-EDITOR).
//
// Routes:
//
//	GET  /api/v1/nodes/{id}/interfaces  — returns the typed interface list
//	PUT  /api/v1/nodes/{id}/interfaces  — admin: replaces (default) or merges
//	                                      the typed interface list
//
// The legacy on-disk schema (pkg/api/types.go InterfaceConfig + IBInterfaceConfig +
// BMCNodeConfig) has three flat shapes for the same conceptual idea: "a network
// interface on this node".  The cockpit-parity UI wants a single typed list with
// one kind-tagged row per interface.  This handler is the API-boundary adapter
// that flattens those three storage shapes into the typed list and back, so the
// UI never has to know the storage stores them apart.
//
// Storage mapping (NodeConfig field ↔ TypedInterface kind):
//
//	cfg.Interfaces (ethernet/loopback) ↔ kind=ethernet
//	cfg.IBConfig                       ↔ kind=fabric
//	cfg.BMC                            ↔ kind=ipmi (singleton — one BMC per node)
//
// On PUT, the handler validates each entry and writes back into the same three
// storage shapes.  Because BMC is a singleton, at most one ipmi-kind row is
// accepted; subsequent rows return a validation error.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Wire types ───────────────────────────────────────────────────────────────

// TypedInterface is the kind-tagged row returned by GET /nodes/{id}/interfaces.
//
// The shape mirrors the React component (web/src/components/InterfaceList.tsx)
// so the UI can round-trip without translation.  Only fields appropriate for
// the chosen kind should be set on the wire; the handler ignores foreign
// fields silently rather than rejecting the request.
type TypedInterface struct {
	Kind string `json:"kind"` // "ethernet" | "fabric" | "ipmi"
	Name string `json:"name"`

	// ethernet-only
	MAC              string `json:"mac,omitempty"`
	IsDefaultGateway bool   `json:"is_default_gateway,omitempty"`
	VLAN             string `json:"vlan,omitempty"`

	// fabric-only — InfiniBand GUID, e.g. "0001:0002:0003:0004"
	GUID string `json:"guid,omitempty"`
	Port string `json:"port,omitempty"`

	// ipmi-only
	Channel string `json:"channel,omitempty"`
	User    string `json:"user,omitempty"`
	Pass    string `json:"pass,omitempty"` // write-only; never returned in GET

	// shared optional fields
	IP string `json:"ip,omitempty"` // ethernet/fabric/ipmi IP — CIDR or plain
}

// InterfacesResponse is the GET response body.
type InterfacesResponse struct {
	NodeID     string           `json:"node_id"`
	Interfaces []TypedInterface `json:"interfaces"`
}

// InterfacesRequest is the PUT request body.
//
// Mode controls how the list is applied:
//
//	"replace"  — (default) overwrites all three storage shapes with the
//	             posted list. Interfaces of kinds not present in the body
//	             are cleared. Use to fully describe the node.
//	"merge"    — per-kind merge: ethernet rows replace cfg.Interfaces
//	             only when at least one ethernet row is provided; same for
//	             fabric and ipmi. Empty kinds preserve existing storage.
//	             Use to update one slice without touching the others.
type InterfacesRequest struct {
	Mode       string           `json:"mode,omitempty"`
	Interfaces []TypedInterface `json:"interfaces"`
}

// ─── Handler ──────────────────────────────────────────────────────────────────

// InterfacesDB is the minimal DB interface required.  Decoupled from *db.DB
// so we can unit-test against a fake.
type InterfacesDB interface {
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
	UpdateNodeConfig(ctx context.Context, cfg api.NodeConfig) error
}

// InterfacesHandler serves /api/v1/nodes/{id}/interfaces.
type InterfacesHandler struct {
	DB InterfacesDB
}

// NewInterfacesHandler wires the handler against the real *db.DB.
func NewInterfacesHandler(d *db.DB) *InterfacesHandler {
	return &InterfacesHandler{DB: d}
}

// Get handles GET /api/v1/nodes/{id}/interfaces.
func (h *InterfacesHandler) Get(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, InterfacesResponse{
		NodeID:     nodeID,
		Interfaces: TypedInterfacesFromNodeConfig(cfg),
	})
}

// Put handles PUT /api/v1/nodes/{id}/interfaces.
//
// Admin scope required (route registration enforces this).
func (h *InterfacesHandler) Put(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")

	var req InterfacesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	// Validate every row before mutating storage. The validator returns a
	// per-index field-keyed error map; we surface the first error to the
	// caller for status-code purposes but include the whole map in the body
	// so the UI can render per-field error chips.
	if errs := ValidateTypedInterfaces(req.Interfaces); len(errs) > 0 {
		// Custom envelope so the UI can render per-field error chips off
		// "fields" while shell-callers see a one-line "error" summary.
		writeJSON(w, http.StatusBadRequest, struct {
			Error  string            `json:"error"`
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		}{
			Error:  firstValidationMessage(errs),
			Code:   "validation_error",
			Fields: errs,
		})
		return
	}

	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "merge" {
		writeValidationError(w, fmt.Sprintf("invalid mode %q; valid: replace, merge", mode))
		return
	}

	if err := ApplyTypedInterfaces(&cfg, req.Interfaces, mode); err != nil {
		writeValidationError(w, err.Error())
		return
	}

	if err := h.DB.UpdateNodeConfig(r.Context(), cfg); err != nil {
		writeError(w, err)
		return
	}

	// Re-read so the response reflects exactly what's persisted (and any
	// server-side normalisation, e.g. lowercased MACs).
	cfg, err = h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, InterfacesResponse{
		NodeID:     nodeID,
		Interfaces: TypedInterfacesFromNodeConfig(cfg),
	})
}

// ─── Storage <-> wire translation ─────────────────────────────────────────────

// TypedInterfacesFromNodeConfig flattens cfg.Interfaces / cfg.IBConfig / cfg.BMC
// into the typed wire list.  Order: ethernet rows first (in cfg.Interfaces
// order), then fabric rows (cfg.IBConfig order), then the singleton ipmi row
// if cfg.BMC is set.  Pass field on ipmi rows is never emitted.
func TypedInterfacesFromNodeConfig(cfg api.NodeConfig) []TypedInterface {
	out := make([]TypedInterface, 0, len(cfg.Interfaces)+len(cfg.IBConfig)+1)

	for _, iface := range cfg.Interfaces {
		row := TypedInterface{
			Kind: "ethernet",
			Name: iface.Name,
			MAC:  iface.MACAddress,
			IP:   iface.IPAddress,
		}
		// "default" gateway flag isn't stored on InterfaceConfig — derive from
		// presence of a gateway. Best-effort: callers that care should set it
		// explicitly via PUT.
		if iface.Gateway != "" {
			row.IsDefaultGateway = true
		}
		out = append(out, row)
	}

	for _, ib := range cfg.IBConfig {
		out = append(out, TypedInterface{
			Kind: "fabric",
			Name: ib.DeviceName,
			IP:   ib.IPAddress,
			// pkeys[0] doubles as a low-fidelity GUID surrogate when the
			// underlying schema doesn't carry a real GUID — the new wire shape
			// reserves a dedicated GUID slot, but we have nothing to populate
			// it with from on-disk data. Leave empty; PUT can set it.
		})
	}

	if cfg.BMC != nil && cfg.BMC.IPAddress != "" {
		out = append(out, TypedInterface{
			Kind:    "ipmi",
			Name:    "ipmi0",
			IP:      cfg.BMC.IPAddress,
			User:    cfg.BMC.Username,
			Channel: "1",
		})
	}

	return out
}

// ApplyTypedInterfaces writes the typed list back into cfg's storage fields.
//
// In "replace" mode all three storage shapes are overwritten (kinds with no
// rows are cleared).  In "merge" mode each kind is touched only if at least
// one row of that kind appears in the input.
//
// Returns an error only on structural problems (e.g. multiple ipmi rows).
// Per-field validation must be done by the caller before invoking.
//
// Password preservation: GET /interfaces never returns the BMC password
// (Pass is write-only).  When the UI POSTs an edited interface list back
// without re-typing the password, the ipmi row's Pass field is empty.
// Treating empty as "clear the password" silently nukes the stored
// credential — a regression Codex caught on PR #14.  Mirroring the BMC
// patch handler's pattern, an empty Pass preserves the existing value.
func ApplyTypedInterfaces(cfg *api.NodeConfig, rows []TypedInterface, mode string) error {
	// Snapshot the existing BMC password (if any) up-front so we can
	// preserve it when the request omits Pass.  Without this snapshot the
	// "replace" branch below cfg.BMC = ipmi before we read existing.Password.
	var existingBMCPass string
	if cfg.BMC != nil {
		existingBMCPass = cfg.BMC.Password
	}

	var (
		eth    []api.InterfaceConfig
		fab    []api.IBInterfaceConfig
		ipmi   *api.BMCNodeConfig
		ipmiCt int
	)

	for _, row := range rows {
		switch row.Kind {
		case "ethernet":
			ic := api.InterfaceConfig{
				Name:       row.Name,
				MACAddress: strings.ToLower(strings.TrimSpace(row.MAC)),
				IPAddress:  row.IP,
			}
			eth = append(eth, ic)
		case "fabric":
			ib := api.IBInterfaceConfig{
				DeviceName: row.Name,
				IPAddress:  row.IP,
			}
			// Carry GUID via PKeys[0] for now (PKeys is the closest existing
			// freeform string slot in IBInterfaceConfig). When the schema
			// gains a dedicated GUID column we'll move this off PKeys.
			if row.GUID != "" {
				ib.PKeys = []string{row.GUID}
			}
			fab = append(fab, ib)
		case "ipmi":
			ipmiCt++
			if ipmiCt > 1 {
				return fmt.Errorf("at most one ipmi-kind interface allowed (BMC is a singleton)")
			}
			pass := row.Pass
			if pass == "" {
				// Preserve existing password — UI never reads it back so an
				// empty Pass on PUT means "leave as-is", not "clear".
				pass = existingBMCPass
			}
			ipmi = &api.BMCNodeConfig{
				IPAddress: row.IP,
				Username:  row.User,
				Password:  pass,
			}
		}
	}

	switch strings.ToLower(mode) {
	case "merge":
		// Replace per-kind only if rows of that kind were provided.
		anyEth, anyFab, anyIPMI := false, false, false
		for _, row := range rows {
			switch row.Kind {
			case "ethernet":
				anyEth = true
			case "fabric":
				anyFab = true
			case "ipmi":
				anyIPMI = true
			}
		}
		if anyEth {
			cfg.Interfaces = eth
		}
		if anyFab {
			cfg.IBConfig = fab
		}
		if anyIPMI {
			cfg.BMC = ipmi
		}
	default: // "replace"
		cfg.Interfaces = eth
		cfg.IBConfig = fab
		cfg.BMC = ipmi
	}

	// Sync cfg.PrimaryMAC if the caller has not set it and we have at least
	// one ethernet row.  This keeps the on-disk PrimaryMAC consistent with
	// the typed list without surprising callers who set it explicitly.
	if cfg.PrimaryMAC == "" && len(eth) > 0 && eth[0].MACAddress != "" {
		cfg.PrimaryMAC = eth[0].MACAddress
	}
	return nil
}

// ─── Validation ───────────────────────────────────────────────────────────────

var (
	macRe  = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)
	guidRe = regexp.MustCompile(`^([0-9a-fA-F]{4}:){3}[0-9a-fA-F]{4}$`)
	ipv4Re = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}(\/\d+)?$`)
)

// ValidateTypedInterfaces walks rows and returns a map of "<index>.<field>" →
// human-readable error message.  Empty map means accepted.
//
// Mirrors web/src/components/InterfaceList.tsx::validateInterfaces so server
// rejects exactly what the client form catches, eliminating round-trip surprises.
func ValidateTypedInterfaces(rows []TypedInterface) map[string]string {
	errs := map[string]string{}
	for i, row := range rows {
		key := func(field string) string { return fmt.Sprintf("%d.%s", i, field) }

		if strings.TrimSpace(row.Name) == "" {
			errs[key("name")] = "Interface name required"
		}

		switch row.Kind {
		case "ethernet":
			mac := strings.TrimSpace(row.MAC)
			if mac == "" {
				errs[key("mac")] = "MAC address required"
			} else if !macRe.MatchString(mac) {
				errs[key("mac")] = "Invalid MAC (e.g. bc:24:11:36:e9:2f)"
			}
			if row.IP != "" && !ipv4Re.MatchString(strings.TrimSpace(row.IP)) {
				errs[key("ip")] = "Invalid IPv4 address or CIDR"
			}
			if row.VLAN != "" {
				if v, err := strconv.Atoi(row.VLAN); err != nil || v < 1 || v > 4094 {
					errs[key("vlan")] = "VLAN must be 1–4094"
				}
			}
		case "fabric":
			guid := strings.TrimSpace(row.GUID)
			if guid == "" {
				errs[key("guid")] = "IB GUID required"
			} else if !guidRe.MatchString(guid) {
				errs[key("guid")] = "Invalid GUID (e.g. 0001:0002:0003:0004)"
			}
			if row.IP != "" && !ipv4Re.MatchString(strings.TrimSpace(row.IP)) {
				errs[key("ip")] = "Invalid IPv4 address or CIDR"
			}
			if row.Port != "" {
				if v, err := strconv.Atoi(row.Port); err != nil || v < 0 || v > 255 {
					errs[key("port")] = "Port must be 0–255"
				}
			}
		case "ipmi":
			ipmi := strings.TrimSpace(row.IP)
			if ipmi == "" {
				errs[key("ip")] = "IPMI IP required"
			} else if !ipv4Re.MatchString(ipmi) {
				errs[key("ip")] = "Invalid IPv4 address or CIDR"
			}
			if row.Channel == "" {
				errs[key("channel")] = "Channel must be 0–15"
			} else if v, err := strconv.Atoi(row.Channel); err != nil || v < 0 || v > 15 {
				errs[key("channel")] = "Channel must be 0–15"
			}
			if strings.TrimSpace(row.User) == "" {
				errs[key("user")] = "IPMI user required"
			}
		case "":
			errs[key("kind")] = "kind is required"
		default:
			errs[key("kind")] = fmt.Sprintf("unknown kind %q (valid: ethernet, fabric, ipmi)", row.Kind)
		}
	}
	return errs
}

// firstValidationMessage returns a deterministic summary line. Sorting by key
// keeps test output stable.
func firstValidationMessage(errs map[string]string) string {
	if len(errs) == 0 {
		return "validation failed"
	}
	keys := make([]string, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	// sort by index-then-field; Go map iteration is random.
	min := keys[0]
	for _, k := range keys[1:] {
		if k < min {
			min = k
		}
	}
	return fmt.Sprintf("%s: %s", min, errs[min])
}
