// operator_bios_test.go — unit tests for HandleBiosApplyRequest (Sprint 26).
package clientd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// ─── HandleBiosApplyRequest tests ─────────────────────────────────────────────

// captureSend returns a send function and a pointer to the last sent ClientMessage.
func captureSend() (func(ClientMessage) error, *ClientMessage) {
	var last ClientMessage
	fn := func(m ClientMessage) error {
		last = m
		return nil
	}
	return fn, &last
}

func TestHandleBiosApplyRequest_UnknownVendor(t *testing.T) {
	send, got := captureSend()
	HandleBiosApplyRequest(context.Background(), BiosApplyRequestPayload{
		RefMsgID:     "ref-1",
		Vendor:       "amiga",
		SettingsJSON: `{"key":"value"}`,
		ProfileID:    "profile-1",
	}, send)

	if got.Type != "bios_apply_result" {
		t.Fatalf("msg type = %q, want bios_apply_result", got.Type)
	}
	var payload BiosApplyResultPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.OK {
		t.Error("expected OK=false for unknown vendor")
	}
	if payload.Error == "" {
		t.Error("expected non-empty error for unknown vendor")
	}
	if payload.RefMsgID != "ref-1" {
		t.Errorf("RefMsgID = %q, want ref-1", payload.RefMsgID)
	}
}

func TestHandleBiosApplyRequest_EmptySettingsJSON(t *testing.T) {
	send, got := captureSend()
	HandleBiosApplyRequest(context.Background(), BiosApplyRequestPayload{
		RefMsgID:     "ref-2",
		Vendor:       "intel",
		SettingsJSON: "",
		ProfileID:    "profile-2",
	}, send)

	var payload BiosApplyResultPayload
	_ = json.Unmarshal(got.Payload, &payload)
	if payload.OK {
		t.Error("expected OK=false for empty settings_json")
	}
	if payload.Error == "" {
		t.Error("expected non-empty error for empty settings_json")
	}
}

func TestHandleBiosApplyRequest_PrivhelperSuccess(t *testing.T) {
	// Redirect staging dir to a temp directory so tests don't need /var/lib/clustr.
	orig := biosStagingDir
	biosStagingDir = t.TempDir() + "/"
	defer func() { biosStagingDir = orig }()

	// Stub biosApplyViaPrivhelper to succeed without spawning a real process.
	origApply := biosApplyViaPrivhelper
	defer func() { biosApplyViaPrivhelper = origApply }()

	var capturedVendor, capturedPath string
	biosApplyViaPrivhelper = func(ctx context.Context, vendor, profilePath string) error {
		capturedVendor = vendor
		capturedPath = profilePath
		return nil
	}

	send, got := captureSend()
	HandleBiosApplyRequest(context.Background(), BiosApplyRequestPayload{
		RefMsgID:     "ref-3",
		Vendor:       "intel",
		SettingsJSON: `{"Intel(R) Hyper-Threading Technology":"Disable"}`,
		ProfileID:    "profile-3",
	}, send)

	var payload BiosApplyResultPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.OK {
		t.Errorf("expected OK=true, got error: %s", payload.Error)
	}
	if payload.AppliedCount != 1 {
		t.Errorf("AppliedCount = %d, want 1", payload.AppliedCount)
	}
	if payload.ProfileID != "profile-3" {
		t.Errorf("ProfileID = %q, want profile-3", payload.ProfileID)
	}
	if capturedVendor != "intel" {
		t.Errorf("vendor passed to privhelper = %q, want intel", capturedVendor)
	}
	// Path must be under the bios staging dir and end in .json.
	if len(capturedPath) == 0 {
		t.Error("expected non-empty staging path")
	}
}

func TestHandleBiosApplyRequest_PrivhelperError(t *testing.T) {
	origDir := biosStagingDir
	biosStagingDir = t.TempDir() + "/"
	defer func() { biosStagingDir = origDir }()

	orig := biosApplyViaPrivhelper
	defer func() { biosApplyViaPrivhelper = orig }()

	biosApplyViaPrivhelper = func(ctx context.Context, vendor, profilePath string) error {
		return errors.New("privhelper: bios-apply intel: exit status 1; output: syscfg not found")
	}

	send, got := captureSend()
	HandleBiosApplyRequest(context.Background(), BiosApplyRequestPayload{
		RefMsgID:     "ref-4",
		Vendor:       "intel",
		SettingsJSON: `{"HT":"Enable"}`,
		ProfileID:    "profile-4",
	}, send)

	var payload BiosApplyResultPayload
	_ = json.Unmarshal(got.Payload, &payload)
	if payload.OK {
		t.Error("expected OK=false when privhelper returns error")
	}
	if payload.Error == "" {
		t.Error("expected non-empty error when privhelper fails")
	}
}

func TestHandleBiosApplyRequest_RefMsgIDEcho(t *testing.T) {
	origDir := biosStagingDir
	biosStagingDir = t.TempDir() + "/"
	defer func() { biosStagingDir = origDir }()

	orig := biosApplyViaPrivhelper
	defer func() { biosApplyViaPrivhelper = orig }()
	biosApplyViaPrivhelper = func(_ context.Context, _, _ string) error { return nil }

	send, got := captureSend()
	HandleBiosApplyRequest(context.Background(), BiosApplyRequestPayload{
		RefMsgID:     "echo-ref-99",
		Vendor:       "intel",
		SettingsJSON: `{"k":"v"}`,
		ProfileID:    "p1",
	}, send)

	var payload BiosApplyResultPayload
	_ = json.Unmarshal(got.Payload, &payload)
	if payload.RefMsgID != "echo-ref-99" {
		t.Errorf("RefMsgID = %q, want echo-ref-99", payload.RefMsgID)
	}
}

// TestPrivhelperBiosApplyCmd_ArgvShape verifies that privhelperBiosApplyCmd
// constructs the correct argv for the bios-apply verb.
func TestPrivhelperBiosApplyCmd_ArgvShape(t *testing.T) {
	cmd := privhelperBiosApplyCmd(context.Background(), "intel", "/var/lib/clustr/bios-staging/test-id.json")
	args := cmd.Args
	// args[0] is the binary path; args[1..] are the arguments.
	if len(args) != 4 {
		t.Fatalf("argv len = %d, want 4 (binary + bios-apply + vendor + path): %v", len(args), args)
	}
	if args[1] != "bios-apply" {
		t.Errorf("args[1] = %q, want bios-apply", args[1])
	}
	if args[2] != "intel" {
		t.Errorf("args[2] = %q, want intel", args[2])
	}
	if args[3] != "/var/lib/clustr/bios-staging/test-id.json" {
		t.Errorf("args[3] = %q, want staging path", args[3])
	}
}

// TestCountSettingsJSON verifies the setting count helper.
func TestCountSettingsJSON(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`{}`, 0},
		{`{"a":"1"}`, 1},
		{`{"a":"1","b":"2","c":"3"}`, 3},
		{`not-json`, 0},
		{``, 0},
	}
	for _, tc := range cases {
		got := countSettingsJSON(tc.in)
		if got != tc.want {
			t.Errorf("countSettingsJSON(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
