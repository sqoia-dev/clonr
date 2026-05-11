package auth

// rbac_test.go — Sprint 41 Day 1 P2 fix
//
// Regression tests for the Allow function's wildcard-query guard.
//
// Contract:
//   - Allow must never grant access when the caller passes a wildcard query
//     verb (any verb containing "*"). Wildcards are only valid on the stored-
//     permission side; a wildcard query is always a caller bug.
//   - Stored wildcard permissions (e.g. "node.*") must still grant concrete
//     query verbs (e.g. "node.reimage") — that is the intended namespace-wildcard
//     matching behaviour and must not be broken by the guard.

import "testing"

// TestAllow_RejectsWildcardQuery_NodeStar verifies that Allow returns false
// when the query verb is "node.*", even though Permissions["node.*"]=true.
// Without the guard, r.Permissions["node.*"] would evaluate to true and grant
// access — a silent over-grant when the caller accidentally passes a wildcard.
func TestAllow_RejectsWildcardQuery_NodeStar(t *testing.T) {
	r := &Resolution{
		Permissions: map[string]bool{
			"node.*": true,
		},
	}
	if Allow(r, "node.*") {
		t.Error("Allow(r, \"node.*\") = true; want false — wildcard query verbs must never grant")
	}
}

// TestAllow_RejectsWildcardQuery_StarOnly verifies that Allow returns false
// when the query verb is the bare wildcard "*".
func TestAllow_RejectsWildcardQuery_StarOnly(t *testing.T) {
	r := &Resolution{
		IsAdmin:     false,
		Permissions: map[string]bool{"*": true},
	}
	// IsAdmin is false here so we don't short-circuit via the admin flag;
	// we want to confirm the wildcard guard fires before the permissions lookup.
	if Allow(r, "*") {
		t.Error("Allow(r, \"*\") = true; want false — wildcard query verbs must never grant")
	}
}

// TestAllow_AllowsExactMatchEvenWhenWildcardStored verifies that a stored
// namespace wildcard permission ("node.*") still grants a concrete verb
// ("node.reimage"). The wildcard guard only blocks wildcard *queries*; stored
// wildcards continue to match concrete verbs via the namespace-wildcard check.
func TestAllow_AllowsExactMatchEvenWhenWildcardStored(t *testing.T) {
	r := &Resolution{
		Permissions: map[string]bool{
			"node.*": true,
		},
	}
	if !Allow(r, "node.reimage") {
		t.Error("Allow(r, \"node.reimage\") = false; want true — stored \"node.*\" must match concrete verb")
	}
}
