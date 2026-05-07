// state_ldap_gate_test.go — pin the v0.1.15 LDAP readiness gate in
// NodeConfig.State(): a node that has booted (DeployVerifiedBootedAt set)
// but reports LDAPReady=false must surface as deployed_ldap_failed, not
// deployed_verified.
//
// Regression context: pre-v0.1.15, the verify-boot handler accepted any
// payload — including sssd_status=not_installed, pam_sss_present=false —
// and unconditionally transitioned to deployed_verified. Gilfoyle found
// this on freshly-imaged vm201/vm202 in v0.1.13. State() now gates on the
// recorded ldap_ready value so the UI surfaces the broken-LDAP failure.
package api

import (
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func TestState_DeployedVerified_WhenLDAPReadyTrue(t *testing.T) {
	now := time.Now()
	cfg := NodeConfig{
		DeployVerifiedBootedAt: &now,
		LDAPReady:              boolPtr(true),
		LDAPReadyDetail:        "sssd connected, pam_sss.so present",
	}
	if got := cfg.State(); got != NodeStateDeployedVerified {
		t.Errorf("State() = %q, want %q (LDAPReady=true → fully verified)", got, NodeStateDeployedVerified)
	}
}

func TestState_DeployedVerified_WhenLDAPReadyNil(t *testing.T) {
	// LDAPReady==nil means the node either hasn't probed sssd yet (older
	// client) or LDAP is not configured cluster-wide. Legacy semantics:
	// preserve deployed_verified to avoid breaking installs that don't use
	// the LDAP module at all.
	now := time.Now()
	cfg := NodeConfig{
		DeployVerifiedBootedAt: &now,
		LDAPReady:              nil,
	}
	if got := cfg.State(); got != NodeStateDeployedVerified {
		t.Errorf("State() = %q, want %q (LDAPReady=nil preserves legacy behavior)", got, NodeStateDeployedVerified)
	}
}

func TestState_DeployedLDAPFailed_WhenLDAPReadyFalse(t *testing.T) {
	// The fix: a node booted (DeployVerifiedBootedAt set) but reported
	// sssd not active. Must NOT be deployed_verified. v0.1.15.
	now := time.Now()
	cfg := NodeConfig{
		DeployVerifiedBootedAt: &now,
		LDAPReady:              boolPtr(false),
		LDAPReadyDetail:        "sssd not_installed; pam_sss.so missing",
	}
	if got := cfg.State(); got != NodeStateDeployedLDAPFailed {
		t.Errorf("State() = %q, want %q (LDAPReady=false → deployed_ldap_failed, not deployed_verified)", got, NodeStateDeployedLDAPFailed)
	}
}

func TestState_LDAPReadyFalse_ButNotYetBooted_StillReturnsLowerStates(t *testing.T) {
	// Edge case: ldap_ready=false but DeployVerifiedBootedAt is nil. The
	// LDAP gate must only apply once verify-boot has been recorded; otherwise
	// states for nodes mid-deploy would skip ahead to deployed_ldap_failed.
	now := time.Now()
	cfg := NodeConfig{
		DeployCompletedPrebootAt: &now,
		DeployVerifiedBootedAt:   nil,
		LDAPReady:                boolPtr(false),
	}
	if got := cfg.State(); got != NodeStateDeployedPreboot {
		t.Errorf("State() = %q, want %q (LDAP gate only applies after verify-boot)", got, NodeStateDeployedPreboot)
	}
}

func TestState_ReimagePending_TakesPriorityOverLDAPGate(t *testing.T) {
	// ReimagePending must always win — even if the previous deploy left
	// LDAPReady=false, a new reimage should not be masked by the LDAP state.
	now := time.Now()
	cfg := NodeConfig{
		DeployVerifiedBootedAt: &now,
		LDAPReady:              boolPtr(false),
		ReimagePending:         true,
	}
	if got := cfg.State(); got != NodeStateReimagePending {
		t.Errorf("State() = %q, want %q (reimage_pending dominates)", got, NodeStateReimagePending)
	}
}
