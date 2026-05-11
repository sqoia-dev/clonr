package config_test

// plugin_test.go — Sprint 41 Day 1 / Day 4
//
// Asserts that:
//   1. The PluginMetadata zero value has the expected defaults.
//   2. Each of the four converted Sprint 36 plugins returns the §2.2 priorities.
//   3. All four plugins return Dangerous=false on Day 1 (except SSSD, which is
//      Dangerous=true from Day 3).
//   4. hostname/hosts/limits return Backup=nil; SSSD returns a populated
//      BackupSpec from Day 4.
//   5. ValidatePluginMetadata accepts valid metadata and rejects invalid cases.
//   6. EffectivePriority returns DefaultPriority for zero-value Priority.

import (
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/config/plugins"
)

// TestPluginMetadata_ZeroValue verifies that the zero value of PluginMetadata
// has the expected field values. Zero-value plugins compile and are safe to use
// in the observer; they get EffectivePriority=DefaultPriority.
func TestPluginMetadata_ZeroValue(t *testing.T) {
	var m config.PluginMetadata

	if m.Priority != 0 {
		t.Errorf("PluginMetadata{}.Priority = %d, want 0", m.Priority)
	}
	if m.Dangerous {
		t.Error("PluginMetadata{}.Dangerous = true, want false")
	}
	if m.DangerReason != "" {
		t.Errorf("PluginMetadata{}.DangerReason = %q, want empty", m.DangerReason)
	}
	if m.Backup != nil {
		t.Errorf("PluginMetadata{}.Backup = %v, want nil", m.Backup)
	}
}

// TestEffectivePriority_ZeroValueBecomesDefault verifies that a zero Priority
// is promoted to DefaultPriority by EffectivePriority.
func TestEffectivePriority_ZeroValueBecomesDefault(t *testing.T) {
	m := config.PluginMetadata{Priority: 0}
	got := config.EffectivePriority(m)
	if got != config.DefaultPriority {
		t.Errorf("EffectivePriority(zero) = %d, want %d", got, config.DefaultPriority)
	}
}

// TestEffectivePriority_ExplicitValuePreserved verifies that a non-zero Priority
// is returned unchanged.
func TestEffectivePriority_ExplicitValuePreserved(t *testing.T) {
	for _, p := range []int{1, 20, 50, 100, 150, 1000} {
		m := config.PluginMetadata{Priority: p}
		got := config.EffectivePriority(m)
		if got != p {
			t.Errorf("EffectivePriority(%d) = %d, want %d", p, got, p)
		}
	}
}

// TestValidatePluginMetadata_Valid verifies that well-formed metadata is accepted.
func TestValidatePluginMetadata_Valid(t *testing.T) {
	cases := []struct {
		name string
		m    config.PluginMetadata
	}{
		{"zero value (unset sentinel)", config.PluginMetadata{}},
		{"explicit priority 1 (PriorityMin, run-first)", config.PluginMetadata{Priority: 1}},
		{"explicit priority 20", config.PluginMetadata{Priority: 20}},
		{"explicit priority 1000 (max)", config.PluginMetadata{Priority: 1000}},
		{"dangerous with reason", config.PluginMetadata{
			Priority:     80,
			Dangerous:    true,
			DangerReason: "breaks login for all LDAP users",
		}},
		{"with backup", config.PluginMetadata{
			Priority: 20,
			Backup:   &config.BackupSpec{Paths: []string{"/etc/hostname"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := config.ValidatePluginMetadata("test-plugin", tc.m); err != nil {
				t.Errorf("ValidatePluginMetadata(%q): unexpected error: %v", tc.name, err)
			}
		})
	}
}

// TestValidatePluginMetadata_Invalid verifies that invalid metadata is rejected.
func TestValidatePluginMetadata_Invalid(t *testing.T) {
	cases := []struct {
		name string
		m    config.PluginMetadata
	}{
		// Priority=0 is the unset sentinel (valid); negative values are rejected.
		{"priority negative", config.PluginMetadata{Priority: -1}},
		{"priority too high", config.PluginMetadata{Priority: 1001}},
		{"dangerous without reason", config.PluginMetadata{
			Priority:  80,
			Dangerous: true,
			// DangerReason missing — must be rejected
		}},
		{"danger reason without dangerous flag", config.PluginMetadata{
			Priority:     80,
			Dangerous:    false,
			DangerReason: "this should not be set",
		}},
		{"backup with empty paths", config.PluginMetadata{
			Priority: 20,
			Backup:   &config.BackupSpec{Paths: nil},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := config.ValidatePluginMetadata("test-plugin", tc.m); err == nil {
				t.Errorf("ValidatePluginMetadata(%q): expected error, got nil", tc.name)
			}
		})
	}
}

// TestConvertedPlugins_Metadata verifies all four Sprint 36 plugins declare the
// §2.2 priorities. SSSD is Dangerous=true from Day 3 and has a populated
// BackupSpec from Day 4; all others remain Dangerous=false with Backup=nil.
func TestConvertedPlugins_Metadata(t *testing.T) {
	cases := []struct {
		name          string
		plugin        config.Plugin
		wantPriority  int
		wantDangerous bool
		wantBackupNil bool
	}{
		{
			name:          "hostname",
			plugin:        plugins.HostnamePlugin{},
			wantPriority:  20,
			wantDangerous: false,
			wantBackupNil: true,
		},
		{
			name:          "hosts",
			plugin:        plugins.HostsPlugin{},
			wantPriority:  30,
			wantDangerous: false,
			wantBackupNil: true,
		},
		{
			// Sprint 41 Day 3: SSSD is now Dangerous=true (gate live).
			// Sprint 41 Day 4: SSSD now has BackupSpec wired (wantBackupNil=false).
			// See internal/config/plugins/sssd.go Metadata() and
			// docs/design/sprint-41-auth-safety.md §2.2 and §5.
			name:          "sssd",
			plugin:        plugins.SSSDPlugin{},
			wantPriority:  80,
			wantDangerous: true,
			wantBackupNil: false,
		},
		{
			name:          "limits",
			plugin:        plugins.LimitsPlugin{},
			wantPriority:  110,
			wantDangerous: false,
			wantBackupNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.plugin.Metadata()

			if m.Priority != tc.wantPriority {
				t.Errorf("plugin %q: Priority = %d, want %d", tc.name, m.Priority, tc.wantPriority)
			}
			if m.Dangerous != tc.wantDangerous {
				t.Errorf("plugin %q: Dangerous = %v, want %v", tc.name, m.Dangerous, tc.wantDangerous)
			}
			if tc.wantBackupNil && m.Backup != nil {
				t.Errorf("plugin %q: Backup = %v, want nil", tc.name, m.Backup)
			}
			if !tc.wantBackupNil && m.Backup == nil {
				t.Errorf("plugin %q: Backup = nil, want non-nil BackupSpec (Day 4)", tc.name)
			}

			// For SSSD specifically: verify the backup paths include the key dirs.
			if tc.name == "sssd" && m.Backup != nil {
				if len(m.Backup.Paths) == 0 {
					t.Error("sssd BackupSpec.Paths must not be empty")
				}
				if m.Backup.RetainN <= 0 {
					t.Errorf("sssd BackupSpec.RetainN = %d, want > 0", m.Backup.RetainN)
				}
			}

			// Confirm metadata passes validation (would panic at Register otherwise).
			if err := config.ValidatePluginMetadata(tc.name, m); err != nil {
				t.Errorf("plugin %q: ValidatePluginMetadata: %v", tc.name, err)
			}
		})
	}
}

// TestConvertedPlugins_PriorityOrder confirms the §2.2 ordering:
// hostname (20) < hosts (30) < sssd (80) < limits (110).
func TestConvertedPlugins_PriorityOrder(t *testing.T) {
	hostname := plugins.HostnamePlugin{}.Metadata().Priority
	hosts := plugins.HostsPlugin{}.Metadata().Priority
	sssd := plugins.SSSDPlugin{}.Metadata().Priority
	limits := plugins.LimitsPlugin{}.Metadata().Priority

	if !(hostname < hosts) {
		t.Errorf("hostname priority (%d) must be < hosts priority (%d)", hostname, hosts)
	}
	if !(hosts < sssd) {
		t.Errorf("hosts priority (%d) must be < sssd priority (%d)", hosts, sssd)
	}
	if !(sssd < limits) {
		t.Errorf("sssd priority (%d) must be < limits priority (%d)", sssd, limits)
	}
}
