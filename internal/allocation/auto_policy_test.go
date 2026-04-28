package allocation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSlugify verifies the slugify helper.
func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"HPC Research Group 2026", "hpc-research-group-2026"},
		{"  leading spaces  ", "leading-spaces"},
		{"", "project"},
		{"A", "a"},
		{strings.Repeat("a", 60), strings.Repeat("a", 48)},
	}
	for _, tc := range cases {
		got := slugify(tc.in)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRenderTemplate verifies the partition name template helper.
func TestRenderTemplate(t *testing.T) {
	got := renderTemplate("{{.ProjectSlug}}-compute", map[string]string{"ProjectSlug": "my-lab"})
	if got != "my-lab-compute" {
		t.Errorf("renderTemplate: got %q want my-lab-compute", got)
	}

	got2 := renderTemplate("shared-compute", map[string]string{})
	if got2 != "shared-compute" {
		t.Errorf("renderTemplate (literal): got %q want shared-compute", got2)
	}
}

// TestParseStateView_UndoAvailable verifies undo availability within 24h.
func TestParseStateView_UndoAvailable(t *testing.T) {
	state := policyState{
		Version:            "1",
		NodeGroupID:        "ng-001",
		NodeGroupName:      "mylab-compute",
		SlurmPartitionName: "mylab-compute",
		PIUserID:           "pi-001",
		CreatedAt:          time.Now().Add(-1 * time.Hour), // 1 hour ago
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	sv, err := ParseStateView(string(stateJSON), nil)
	if err != nil {
		t.Fatalf("ParseStateView: %v", err)
	}
	if !sv.UndoAvailable {
		t.Error("undo should be available within 24h")
	}
	if sv.HoursRemaining < 22 || sv.HoursRemaining > 24 {
		t.Errorf("hours remaining: got %.2f, want ~23", sv.HoursRemaining)
	}
}

// TestParseStateView_WindowClosed verifies undo is blocked when finalized.
func TestParseStateView_WindowClosed(t *testing.T) {
	state := policyState{
		Version:     "1",
		NodeGroupID: "ng-002",
		NodeGroupName: "old-compute",
		CreatedAt:   time.Now().Add(-25 * time.Hour), // 25 hours ago
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	fin := time.Now().Add(-1 * time.Hour)
	sv, err := ParseStateView(string(stateJSON), &fin)
	if err != nil {
		t.Fatalf("ParseStateView: %v", err)
	}
	if sv.UndoAvailable {
		t.Error("undo should NOT be available when finalized")
	}
	if sv.HoursRemaining != 0 {
		t.Errorf("hours remaining should be 0, got %.2f", sv.HoursRemaining)
	}
}

// TestParseStateView_WindowExpiredButNotFinalized checks elapsed-window detection
// even without a finalized_at timestamp (race window before finalizer runs).
func TestParseStateView_WindowExpiredButNotFinalized(t *testing.T) {
	state := policyState{
		Version:     "1",
		NodeGroupID: "ng-003",
		NodeGroupName: "old-compute-2",
		CreatedAt:   time.Now().Add(-26 * time.Hour),
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// finalizedAt is nil (finalizer hasn't run yet), but window expired.
	sv, err := ParseStateView(string(stateJSON), nil)
	if err != nil {
		t.Fatalf("ParseStateView: %v", err)
	}
	// HoursRemaining should be 0 (clamped by max()).
	if sv.HoursRemaining != 0 {
		t.Errorf("hours remaining should be 0 for expired window, got %.2f", sv.HoursRemaining)
	}
	// UndoAvailable should be false (remaining <= 0).
	if sv.UndoAvailable {
		t.Error("undo should not be available when window time has elapsed")
	}
}
