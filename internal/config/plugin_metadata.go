package config

// plugin_metadata.go — Sprint 41 Day 1
//
// Validation helpers and registration-time checks for PluginMetadata.
// Called by Register before the plugin queue is created so misconfigured
// plugins are caught at server startup, not in production.
//
// Priority band convention (for authors choosing a priority value):
//
//	  1– 50  Foundation   — host identity that everything else depends on
//	                        (hostname, /etc/hosts, kernel sysctls).
//	 51–100  Middleware   — authentication and time-synchronisation daemons
//	                        (sssd, pam, chrony). Run after foundation so LDAP
//	                        lookup works when these services start.
//	101–150  Applications — cluster software config (slurm, ssh authorized_keys,
//	                        resource limits). Run after auth middleware is settled.
//	151–200  Post-apply   — service restarts, validation probes, anything that
//	                        must fire only after every other plugin has settled.
//	201–1000 Reserved     — hard upper bound; values above 200 are not used by
//	                        any shipped plugin; reserved for future hook types.
//
// Pick deliberately. "I don't care" → use DefaultPriority (100) and document
// why ordering is irrelevant for your plugin.
// To run first, use Priority=1 (not 0 — zero is the unset sentinel).

const (
	// PriorityMin is the lowest valid explicit priority (1-based).
	// Zero is reserved as the unset sentinel and is treated as DefaultPriority
	// (100) by EffectivePriority. To request "run first", use Priority=1.
	PriorityMin = 1

	// PriorityMax is the highest valid priority. Registration rejects values
	// above this to catch typos (e.g. 10000 instead of 100) at startup.
	PriorityMax = 1000
)

// ValidatePluginMetadata checks the invariants that must hold for a
// PluginMetadata to be registered. Returns a non-nil error with a human-readable
// description of the first violation found.
//
// Priority=0 is the unset sentinel and is accepted (EffectivePriority promotes
// it to DefaultPriority=100). Negative priorities and values above PriorityMax
// are rejected.
//
// Callers: Register (panics on error), tests.
func ValidatePluginMetadata(name string, m PluginMetadata) error {
	if m.Priority < 0 || m.Priority > PriorityMax {
		return &metadataError{
			plugin: name,
			field:  "Priority",
			msg:    "must be in range [0, 1000] (0 is the unset sentinel; use 1 for run-first)",
		}
	}
	if m.Dangerous && m.DangerReason == "" {
		return &metadataError{
			plugin: name,
			field:  "DangerReason",
			msg:    "must be non-empty when Dangerous is true",
		}
	}
	if !m.Dangerous && m.DangerReason != "" {
		return &metadataError{
			plugin: name,
			field:  "DangerReason",
			msg:    "must be empty when Dangerous is false",
		}
	}
	if m.Backup != nil {
		if len(m.Backup.Paths) == 0 {
			return &metadataError{
				plugin: name,
				field:  "Backup.Paths",
				msg:    "must contain at least one path when Backup is non-nil",
			}
		}
	}
	return nil
}

// metadataError is the structured error type returned by ValidatePluginMetadata.
type metadataError struct {
	plugin string
	field  string
	msg    string
}

func (e *metadataError) Error() string {
	return "config: plugin " + e.plugin + ": invalid metadata field " + e.field + ": " + e.msg
}

// EffectivePriority returns m.Priority when it is non-zero, or DefaultPriority
// (100) when zero. This implements the "zero value means default 100" contract:
// a plugin that returns PluginMetadata{} (zero value) behaves as if it declared
// Priority=100, without the author needing to write it explicitly.
//
// Zero is the unset sentinel. To run first, set Priority=PriorityMin (1).
func EffectivePriority(m PluginMetadata) int {
	if m.Priority == 0 {
		return DefaultPriority
	}
	return m.Priority
}

// ValidatePriority checks whether a raw integer is a permissible Priority value
// for external or user-supplied contexts (CLI flags, API payloads). It accepts 0
// (the unset sentinel) and any positive integer; it rejects negative values which
// are always a caller error.
//
// This function does NOT enforce PriorityMax — that is done by
// ValidatePluginMetadata at plugin registration time. ValidatePriority is the
// lightweight boundary check for inputs that arrive before plugin registration
// (e.g. API requests that accept a priority override, CLI flag parsing).
//
// Future callers: API handlers that accept a priority query param; CLI flag.
func ValidatePriority(p int) error {
	if p < 0 {
		return &metadataError{
			plugin: "unknown",
			field:  "Priority",
			msg:    "must be >= 0 (0 is the unset sentinel; use 1 for run-first)",
		}
	}
	return nil
}
