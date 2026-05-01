package isoinstaller

// roles_test.go — Sprint 15 #99 anti-regression tests.
// These tests assert that LDAP-client roles always include sssd-ldap so a
// future refactor cannot silently drop the package and re-introduce the
// "sssd backend crashes on start" failure mode from the 2026-04-29 incident.

import (
	"testing"
)

// TestRoles_LDAPNodeRolesContainSSSDLDAP asserts that every role intended for
// LDAP-joined nodes includes "sssd-ldap" in its package list for all EL distros.
// Roles that genuinely do not need LDAP auth (e.g. storage) are excluded.
func TestRoles_LDAPNodeRolesContainSSSDLDAP(t *testing.T) {
	// These role IDs are the LDAP-joined roles — they must have sssd-ldap.
	ldapRoles := []string{"head-node", "compute", "gpu-compute"}

	// Distros that use the "sssd-ldap" RPM name (EL-family).
	checkDistros := []Distro{DistroRocky, DistroAlmaLinux}

	for _, roleID := range ldapRoles {
		var role *Role
		for i := range HPCRoles {
			if HPCRoles[i].ID == roleID {
				role = &HPCRoles[i]
				break
			}
		}
		if role == nil {
			t.Errorf("role %q not found in HPCRoles — was it renamed or removed?", roleID)
			continue
		}

		for _, distro := range checkDistros {
			pkgs, ok := role.Packages[distro]
			if !ok {
				t.Errorf("role %q: no package list for distro %q", roleID, distro)
				continue
			}
			hasSSSDLDAP := false
			hasSSSD := false
			for _, pkg := range pkgs {
				if pkg == "sssd-ldap" {
					hasSSSDLDAP = true
				}
				if pkg == "sssd" {
					hasSSSD = true
				}
			}
			if !hasSSSDLDAP {
				t.Errorf("role %q distro %q: missing sssd-ldap package — SSSD backend will crash on start without it (Sprint 15 #99)", roleID, distro)
			}
			if !hasSSSD {
				t.Errorf("role %q distro %q: missing sssd package — LDAP authentication will not work", roleID, distro)
			}
		}
	}
}

// TestRoles_LDAPNodeRolesEnableSSSD asserts that LDAP-joined node roles enable
// sssd.service so it starts on first boot.
func TestRoles_LDAPNodeRolesEnableSSSD(t *testing.T) {
	ldapRoles := []string{"compute", "gpu-compute"}
	checkDistros := []Distro{DistroRocky, DistroAlmaLinux}

	for _, roleID := range ldapRoles {
		var role *Role
		for i := range HPCRoles {
			if HPCRoles[i].ID == roleID {
				role = &HPCRoles[i]
				break
			}
		}
		if role == nil {
			t.Errorf("role %q not found in HPCRoles", roleID)
			continue
		}

		for _, distro := range checkDistros {
			svcs, ok := role.Services[distro]
			if !ok {
				t.Errorf("role %q: no service list for distro %q", roleID, distro)
				continue
			}
			hasSSSD := false
			for _, svc := range svcs {
				if svc == "sssd" {
					hasSSSD = true
					break
				}
			}
			if !hasSSSD {
				t.Errorf("role %q distro %q: sssd service not enabled — LDAP auth will not start at boot", roleID, distro)
			}
		}
	}
}
