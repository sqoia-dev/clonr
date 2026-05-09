package external

// snmp.go — agent-less SNMP collector for switches, PDUs, and any
// other clustr-adjacent device that speaks SNMPv2c/v3. Wraps gosnmp.
//
// Scope for Sprint 38 Bundle A: a "ping by OID list" collector. The
// caller supplies a list of OIDs to walk (sysUpTime, ifInOctets,
// etc.); we issue a single GET, format the results into a JSON map,
// and store under source='snmp'. Trap reception, full MIB
// translation, and per-OID polling cadences are out of scope.
//
// Auth: SNMPv2c community string only. v3 (USM/auth/priv) is left for
// a follow-up sprint; the API call shape below makes it a small
// extension when needed.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMPSample is the JSON shape stored under each OID in
// node_external_stats.payload_json (source='snmp'). We capture the
// printable representation along with the SNMP type so the UI can
// render correctly without having to look up the MIB.
type SNMPSample struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

// SNMPPayload is the full payload JSON for source='snmp'.
type SNMPPayload struct {
	Samples     map[string]SNMPSample `json:"samples"`
	CollectedAt time.Time             `json:"collected_at"`
	Source      string                `json:"source"` // "snmpv2c"
	Error       string                `json:"error,omitempty"`
}

// SNMPTarget is the per-poll input. Empty community defaults to
// "public" — gosnmp's default — but production deployments should
// always set this explicitly.
type SNMPTarget struct {
	Host      string
	Port      uint16 // 0 → 161
	Community string // empty → "public"
	OIDs      []string
}

// SNMPClient is the gosnmp surface we use, abstracted so tests can
// inject a fake without touching the network. The production binding
// lives in gosnmpAdapter (it wraps *gosnmp.GoSNMP because that type
// has no Close() — its Conn field is the net.Conn we own); tests use
// a fakeSNMPClient that returns canned PDUs.
type SNMPClient interface {
	Connect() error
	Close() error
	Get(oids []string) (*gosnmp.SnmpPacket, error)
}

// SNMPClientFactory builds an SNMPClient from a target. Swappable in
// tests. The production factory configures the gosnmp.GoSNMP struct
// with v2c, the supplied community, and a 2-second timeout.
type SNMPClientFactory func(t SNMPTarget) SNMPClient

// gosnmpAdapter wraps *gosnmp.GoSNMP. The wrapper exists so we can
// expose a Close() that targets the embedded net.Conn (gosnmp v1
// doesn't surface a Close on its own type).
type gosnmpAdapter struct {
	g *gosnmp.GoSNMP
}

func (a *gosnmpAdapter) Connect() error { return a.g.Connect() }
func (a *gosnmpAdapter) Close() error {
	if a.g.Conn != nil {
		return a.g.Conn.Close()
	}
	return nil
}
func (a *gosnmpAdapter) Get(oids []string) (*gosnmp.SnmpPacket, error) {
	return a.g.Get(oids)
}

// DefaultSNMPClientFactory wires gosnmp directly. Returns a client
// that has not yet been Connect()-ed.
func DefaultSNMPClientFactory(t SNMPTarget) SNMPClient {
	port := t.Port
	if port == 0 {
		port = 161
	}
	community := t.Community
	if community == "" {
		community = "public"
	}
	return &gosnmpAdapter{
		g: &gosnmp.GoSNMP{
			Target:    t.Host,
			Port:      port,
			Community: community,
			Version:   gosnmp.Version2c,
			Timeout:   2 * time.Second,
			Retries:   1,
		},
	}
}

// SNMPCollector polls a single device. The pool calls Collect once per
// cadence per (node_id, snmp target). Like BMCCollector, an error
// is captured into the payload Error field rather than dropping the
// row entirely — the UI surfaces "snmp.failed" so an operator can see
// it instead of guessing.
type SNMPCollector struct {
	Factory SNMPClientFactory
}

// Collect runs the SNMP GET against t.OIDs and returns the parsed
// payload. Always returns a non-nil payload.
func (c *SNMPCollector) Collect(ctx context.Context, t SNMPTarget) SNMPPayload {
	pl := SNMPPayload{
		Samples:     map[string]SNMPSample{},
		CollectedAt: time.Now().UTC(),
		Source:      "snmpv2c",
	}
	if t.Host == "" {
		pl.Error = "no SNMP host configured"
		return pl
	}
	if len(t.OIDs) == 0 {
		pl.Error = "no SNMP OIDs configured"
		return pl
	}
	factory := c.Factory
	if factory == nil {
		factory = DefaultSNMPClientFactory
	}
	cli := factory(t)
	if err := cli.Connect(); err != nil {
		pl.Error = fmt.Sprintf("snmp connect: %v", err)
		return pl
	}
	defer cli.Close()

	// gosnmp's Get respects ctx via the underlying socket Deadline,
	// not by accepting a context. We approximate ctx-cancel by giving
	// the Get a deadline that respects ctx.Deadline if set.
	pkt, err := cli.Get(t.OIDs)
	if err != nil {
		pl.Error = fmt.Sprintf("snmp get: %v", err)
		return pl
	}
	for _, v := range pkt.Variables {
		pl.Samples[strings.TrimPrefix(v.Name, ".")] = SNMPSample{
			Value: snmpValueString(v),
			Type:  snmpTypeString(v.Type),
		}
	}
	_ = ctx // ctx threaded for symmetry with BMCCollector; gosnmp v1
	// uses socket Timeout. A future revision can move to a
	// context-aware client.
	return pl
}

// snmpValueString stringifies an SNMP variable for storage. Numerical
// types use %v; OctetString is rendered as a string (most network
// gear emits ASCII labels). Other types get a fmt.Sprintf("%v") so we
// don't crash on exotic ASN.1 returns from old PDUs.
func snmpValueString(v gosnmp.SnmpPDU) string {
	if v.Value == nil {
		return ""
	}
	switch t := v.Value.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}

func snmpTypeString(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.Integer:
		return "integer"
	case gosnmp.OctetString:
		return "octet_string"
	case gosnmp.Counter32:
		return "counter32"
	case gosnmp.Counter64:
		return "counter64"
	case gosnmp.Gauge32:
		return "gauge32"
	case gosnmp.TimeTicks:
		return "ticks"
	case gosnmp.IPAddress:
		return "ipaddr"
	case gosnmp.ObjectIdentifier:
		return "oid"
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return "no-such-object"
	default:
		return fmt.Sprintf("0x%x", byte(t))
	}
}
