package auth

// permissions.go — Sprint 41 Day 3
//
// Permission verb constants. All handler code that calls requirePermission or
// auth.Allow must reference one of these constants — no raw strings at call sites.
// This file is the closed enum of shipped permission verbs (see design doc §9.1).
//
// Adding a new verb: add a constant here, document the semantic, and update the
// built-in role seeds in 113_roles_and_assignments.sql if the verb should be
// granted by default to any built-in role.
//
// Wildcards are only valid on the grant side (stored in roles.permissions_json).
// Callers of auth.Allow must always supply an exact verb from this list.

const (
	// VerbNodeRead is required to read node state (GET /nodes, GET /nodes/{id}, etc.).
	VerbNodeRead = "node.read"

	// VerbNodeWrite is required to update node configuration.
	VerbNodeWrite = "node.write"

	// VerbNodeReimage is required to initiate a node reimage.
	VerbNodeReimage = "node.reimage"

	// VerbConfigDangerousPush is required to stage and confirm a dangerous
	// config push (POST /api/v1/config/dangerous-push and
	// POST /api/v1/config/dangerous-push/{id}/confirm).
	//
	// By default only the built-in admin role holds this verb ("*" wildcard).
	// Operators who need to perform SSSD or other dangerous plugin pushes must
	// receive an explicit role assignment that grants this verb.
	//
	// See docs/design/sprint-41-auth-safety.md §3.6 and §4.2.
	VerbConfigDangerousPush = "config.dangerous_push"
)
