package deploy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeBMC records every Run() invocation and returns canned output for
// `lan print <ch>` while accepting any `lan set` / `user set` no-op.
type fakeBMC struct {
	lanPrintOut string
	invocations [][]string
}

func (f *fakeBMC) Run(_ context.Context, args ...string) (string, error) {
	f.invocations = append(f.invocations, append([]string{}, args...))
	if len(args) >= 2 && args[0] == "lan" && args[1] == "print" {
		return f.lanPrintOut, nil
	}
	return "", nil
}

// setOnly returns the subset of invocations matching `lan set` / `user set`.
func (f *fakeBMC) setOnly() [][]string {
	var out [][]string
	for _, a := range f.invocations {
		if len(a) >= 2 && a[1] == "set" {
			out = append(out, a)
		}
	}
	return out
}

// ─── parseLanPrint ────────────────────────────────────────────────────────────

func TestParseLanPrint_Standard(t *testing.T) {
	out := `Set in Progress         : Set Complete
Auth Type Support       : MD5 PASSWORD
IP Address Source       : Static Address
IP Address              : 10.99.0.5
Subnet Mask             : 255.255.255.0
MAC Address             : aa:bb:cc:dd:ee:ff
Default Gateway IP      : 10.99.0.1
802.1q VLAN ID          : Disabled`
	got := parseLanPrint(out)
	want := bmcCurrentState{
		IPSource:  "static",
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	if got != want {
		t.Errorf("parseLanPrint mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParseLanPrint_DHCP(t *testing.T) {
	out := `IP Address Source       : DHCP Address
IP Address              : 10.99.0.50`
	got := parseLanPrint(out)
	if got.IPSource != "dhcp" {
		t.Errorf("expected dhcp, got %q", got.IPSource)
	}
}

// ─── planBMCDiff ──────────────────────────────────────────────────────────────

func TestPlanBMCDiff_AllDiffer(t *testing.T) {
	current := bmcCurrentState{
		IPSource:  "dhcp",
		IPAddress: "0.0.0.0",
		Netmask:   "0.0.0.0",
		Gateway:   "0.0.0.0",
	}
	desired := &api.BMCNodeConfig{
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	steps := planBMCDiff(current, desired)
	if len(steps) != 4 {
		t.Fatalf("expected 4 steps, got %d: %v", len(steps), steps)
	}
	if steps[0][3] != "ipsrc" {
		t.Errorf("first step should set ipsrc, got %v", steps[0])
	}
}

func TestPlanBMCDiff_NoDiff(t *testing.T) {
	current := bmcCurrentState{
		IPSource:  "static",
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	desired := &api.BMCNodeConfig{
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	steps := planBMCDiff(current, desired)
	if len(steps) != 0 {
		t.Errorf("idempotent re-apply must be zero writes, got %v", steps)
	}
}

func TestPlanBMCDiff_OnlyIPDiffers(t *testing.T) {
	current := bmcCurrentState{
		IPSource:  "static",
		IPAddress: "10.99.0.4",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	desired := &api.BMCNodeConfig{
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	steps := planBMCDiff(current, desired)
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d: %v", len(steps), steps)
	}
	if steps[0][3] != "ipaddr" {
		t.Errorf("expected ipaddr step, got %v", steps[0])
	}
}

// ─── End-to-end: applyBMCConfigWithRunner idempotency ────────────────────────

func TestApplyBMCConfigToHardware_FirstApply_Writes(t *testing.T) {
	fake := &fakeBMC{
		lanPrintOut: `IP Address Source : DHCP Address
IP Address        : 0.0.0.0
Subnet Mask       : 0.0.0.0
Default Gateway IP: 0.0.0.0`,
	}
	cfg := &api.BMCNodeConfig{
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	if err := applyBMCConfigWithRunner(context.Background(), cfg, fake.Run); err != nil {
		t.Fatalf("apply: %v", err)
	}
	writes := fake.setOnly()
	if len(writes) == 0 {
		t.Fatal("expected at least one write on first apply")
	}
}

func TestApplyBMCConfigToHardware_SecondApply_NoWrites(t *testing.T) {
	fake := &fakeBMC{
		lanPrintOut: `IP Address Source : Static Address
IP Address        : 10.99.0.5
Subnet Mask       : 255.255.255.0
Default Gateway IP: 10.99.0.1`,
	}
	cfg := &api.BMCNodeConfig{
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	if err := applyBMCConfigWithRunner(context.Background(), cfg, fake.Run); err != nil {
		t.Fatalf("apply: %v", err)
	}
	writes := fake.setOnly()
	if len(writes) != 0 {
		t.Errorf("idempotent re-apply must produce zero writes, got %d: %v", len(writes), writes)
	}
	reads := 0
	for _, a := range fake.invocations {
		if len(a) >= 2 && a[0] == "lan" && a[1] == "print" {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("expected exactly 1 read, got %d", reads)
	}
}

func TestApplyBMCConfigToHardware_NilCfg_NoOp(t *testing.T) {
	fake := &fakeBMC{}
	if err := applyBMCConfigWithRunner(context.Background(), nil, fake.Run); err != nil {
		t.Fatalf("nil cfg should be no-op: %v", err)
	}
	if len(fake.invocations) != 0 {
		t.Errorf("nil cfg must not invoke runner")
	}
}

func TestApplyBMCConfigToHardware_StableSequence(t *testing.T) {
	fake := &fakeBMC{
		lanPrintOut: `IP Address Source : DHCP Address
IP Address        : 0.0.0.0
Subnet Mask       : 0.0.0.0
Default Gateway IP: 0.0.0.0`,
	}
	cfg := &api.BMCNodeConfig{
		IPAddress: "10.99.0.5",
		Netmask:   "255.255.255.0",
		Gateway:   "10.99.0.1",
	}
	_ = applyBMCConfigWithRunner(context.Background(), cfg, fake.Run)
	got := fake.setOnly()
	want := [][]string{
		{"lan", "set", "1", "ipsrc", "static"},
		{"lan", "set", "1", "ipaddr", "10.99.0.5"},
		{"lan", "set", "1", "netmask", "255.255.255.0"},
		{"lan", "set", "1", "defgw", "ipaddr", "10.99.0.1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv sequence mismatch\n got: %v\nwant: %v", got, want)
	}
}

// ─── Smoke: failed read is propagated as error ────────────────────────────────

type failingBMC struct{ msg string }

func (f failingBMC) Run(_ context.Context, _ ...string) (string, error) {
	return "", &runErr{msg: f.msg}
}

type runErr struct{ msg string }

func (e *runErr) Error() string { return e.msg }

func TestApplyBMCConfigToHardware_ReadFails(t *testing.T) {
	cfg := &api.BMCNodeConfig{IPAddress: "10.99.0.5", Netmask: "255.255.255.0", Gateway: "10.99.0.1"}
	err := applyBMCConfigWithRunner(context.Background(), cfg, failingBMC{msg: "kcs unreachable"}.Run)
	if err == nil {
		t.Fatal("expected error when read fails")
	}
	if !strings.Contains(err.Error(), "kcs unreachable") {
		t.Errorf("error should wrap inner error: %v", err)
	}
}
