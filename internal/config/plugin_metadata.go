package config

// plugin_metadata.go — Sprint 41 Day 1
//
// Validation helpers and registration-time checks for PluginMetadata.
// Called by Register before the plugin queue is created so misconfigured
// plugins are caught at server startup, not in production.
//
// Priority band convention (for authors choosing a priority value):
//
//	  0– 50  Foundation   — host identity that everything else depends on
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

const (
	// PriorityMin is the lowest valid priority. 0 is reserved for "must run
	// absolutely first ever" (e.g. baseline filesystem layout). Used by
	// tests to probe the boundary.
	PriorityMin = 0

	// PriorityMax is the highest valid priority. Registration rejects values
	// above this to catch typos (e.g. 10000 instead of 100) at startup.
	PriorityMax = 1000
)

// ValidatePluginMetadata checks the invariants that must hold for a
// PluginMetadata to be registered. Returns a non-nil error with a human-readable
// description of the first violation found.
//
// Callers: Register (panics on error), tests.
func ValidatePluginMetadata(name string, m PluginMetadata) error {
	if m.Priority < PriorityMin || m.Priority > PriorityMax {
		return &metadataError{
			plugin: name,
			field:  "Priority",
			msg:    "must be in range [0, 1000]",
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
// when zero. This implements the "zero value means default 100" contract
// described in §2.1 of the design doc without requiring every plugin author
// to write Priority: DefaultPriority explicitly.
//
// Note: Priority=0 is valid for plugins that explicitly need to be first.
// Those plugins must set Priority: PriorityMin (0) in their Metadata() method.
// The distinction between "I forgot to set it" and "I mean 0" is enforced by
// ValidatePluginMetadata — plugins with Priority=0 must document their intent
// in their Metadata() godoc.
func EffectivePriority(m PluginMetadata) int {
	if m.Priority == 0 {
		return DefaultPriority
	}
	return m.Priority
}
