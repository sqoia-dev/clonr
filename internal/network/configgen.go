package network

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var switchTemplates embed.FS

// VLANConfig describes a VLAN to be created on the switch.
type VLANConfig struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PortConfig describes one or more ports and their VLAN/mode assignment.
type PortConfig struct {
	Name        string `json:"name"`        // e.g. "Ethernet1", "ge-0/0/1"
	Description string `json:"description,omitempty"`
	VLANID      int    `json:"vlan_id,omitempty"`
	Mode        string `json:"mode"`        // "access" or "trunk"
}

// LAGConfig describes a Link Aggregation Group.
type LAGConfig struct {
	ID          int      `json:"id"`
	Description string   `json:"description,omitempty"`
	MemberPorts []string `json:"member_ports"` // e.g. ["Ethernet49", "Ethernet50"]
	Mode        string   `json:"mode"`         // "lacp" or "static"
}

// SwitchConfigData is the template data struct passed to all vendor templates.
type SwitchConfigData struct {
	SwitchName  string       `json:"switch_name"`
	Role        string       `json:"role"`
	VLANs       []VLANConfig `json:"vlans"`
	ServerPorts []PortConfig `json:"server_ports"`
	UplinkPorts []PortConfig `json:"uplink_ports"`
	LAGs        []LAGConfig  `json:"lags"`
	MTU         int          `json:"mtu"`
	EnablePFC   bool         `json:"enable_pfc"`
	PFCPriority int          `json:"pfc_priority"`
}

// vendorTemplateFile maps lowercase vendor identifiers to embedded template filenames.
var vendorTemplateFile = map[string]string{
	"arista":    "templates/arista.cfg.tmpl",
	"dell":      "templates/dell.cfg.tmpl",
	"juniper":   "templates/juniper.cfg.tmpl",
	"cisco":     "templates/cisco.cfg.tmpl",
	"hpe-aruba": "templates/generic.cfg.tmpl",
	"mellanox":  "templates/generic.cfg.tmpl",
}

// RenderSwitchTemplate renders a vendor-specific switch configuration template
// using the provided SwitchConfigData. The vendor string selects the template
// (e.g. "arista", "dell", "juniper", "cisco"); unknown or empty vendors fall
// back to the generic template. This is the pure inner function — it has no DB
// dependency and is used directly by tests.
func RenderSwitchTemplate(vendor string, data SwitchConfigData) (string, error) {
	// Set sensible defaults.
	if data.MTU == 0 {
		data.MTU = 9000
	}
	if data.EnablePFC && data.PFCPriority == 0 {
		data.PFCPriority = 3
	}

	tmplFile, ok := vendorTemplateFile[strings.ToLower(vendor)]
	if !ok {
		tmplFile = "templates/generic.cfg.tmpl"
	}

	tmplContent, err := switchTemplates.ReadFile(tmplFile)
	if err != nil {
		return "", fmt.Errorf("network: render template %s: %w", tmplFile, err)
	}

	tmpl, err := template.New("switch").Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("network: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("network: execute template: %w", err)
	}
	return buf.String(), nil
}

// GenerateSwitchConfig renders a vendor-specific configuration for the given
// switch ID using the provided SwitchConfigData. Returns the rendered text.
// If the switch vendor is unknown or empty, the generic template is used.
func (m *Manager) GenerateSwitchConfig(ctx context.Context, switchID string, data SwitchConfigData) (string, error) {
	sw, err := m.db.NetworkGetSwitchByID(ctx, switchID)
	if err != nil {
		return "", fmt.Errorf("network: generate config: %w", err)
	}

	// Populate switch name from DB if caller left it empty.
	if data.SwitchName == "" {
		data.SwitchName = sw.Name
	}
	if data.Role == "" {
		data.Role = string(sw.Role)
	}

	out, err := RenderSwitchTemplate(sw.Vendor, data)
	if err != nil {
		return "", fmt.Errorf("network: generate config: %w", err)
	}
	return out, nil
}
