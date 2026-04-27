// roles.go — Slurm node role constants and role-to-resource mappings.
// A node may have multiple roles (e.g. controller + login on a small cluster).
package slurm

const (
	RoleController = "controller"
	RoleCompute    = "compute"
	// RoleWorker is a deprecated alias for RoleCompute accepted on all read paths.
	// The canonical value is "compute"; "worker" was the original API role string
	// before the rename. Scheduled for removal post-v1.0 once all DB rows and
	// API callers have been migrated. Do not emit "worker" in new code — use
	// RoleCompute instead.
	RoleWorker = "worker"
	RoleLogin  = "login"
	RoleDBD    = "dbd"
	RoleNone   = "none"
)

// IsComputeRole returns true when the role string represents a compute (slurmd)
// role, accepting both the canonical "compute" value and the deprecated "worker"
// alias. Use this instead of a raw hasRole check anywhere the two strings must
// be treated equivalently.
func IsComputeRole(role string) bool {
	return role == RoleCompute || role == RoleWorker
}

// FilesForRoles returns which config files should be deployed to a node given
// its roles. Files are deduplicated; order is stable.
func FilesForRoles(roles []string) []string {
	set := make(map[string]struct{})
	for _, role := range roles {
		switch role {
		case RoleController:
			for _, f := range []string{"slurm.conf", "cgroup.conf", "topology.conf", "plugstack.conf", "slurmdbd.conf"} {
				set[f] = struct{}{}
			}
		case RoleCompute, RoleWorker: // RoleWorker is deprecated alias
			for _, f := range []string{"slurm.conf", "gres.conf", "cgroup.conf", "plugstack.conf"} {
				set[f] = struct{}{}
			}
		case RoleLogin:
			set["slurm.conf"] = struct{}{}
		case RoleDBD:
			for _, f := range []string{"slurm.conf", "slurmdbd.conf"} {
				set[f] = struct{}{}
			}
		}
	}
	// Return in a stable canonical order matching defaultManagedFiles + slurmdbd.conf.
	canonical := []string{"slurm.conf", "gres.conf", "cgroup.conf", "topology.conf", "plugstack.conf", "slurmdbd.conf"}
	var result []string
	for _, f := range canonical {
		if _, ok := set[f]; ok {
			result = append(result, f)
		}
	}
	return result
}

// ServicesForRoles returns which systemd services should be enabled for a node
// given its roles. Services are deduplicated; munge is always included when any
// Slurm role is present (all Slurm daemons require munge).
func ServicesForRoles(roles []string) []string {
	if len(roles) == 0 {
		return nil
	}
	set := make(map[string]struct{})
	hasSlurmRole := false
	for _, role := range roles {
		switch role {
		case RoleController:
			set["slurmctld.service"] = struct{}{}
			hasSlurmRole = true
		case RoleCompute, RoleWorker: // RoleWorker is deprecated alias
			set["slurmd.service"] = struct{}{}
			hasSlurmRole = true
		case RoleDBD:
			set["slurmdbd.service"] = struct{}{}
			hasSlurmRole = true
		case RoleLogin:
			hasSlurmRole = true
		}
	}
	if hasSlurmRole {
		set["munge.service"] = struct{}{}
	}
	canonical := []string{"munge.service", "slurmctld.service", "slurmd.service", "slurmdbd.service"}
	var result []string
	for _, s := range canonical {
		if _, ok := set[s]; ok {
			result = append(result, s)
		}
	}
	return result
}

// ScriptTypesForRoles returns which Slurm script types are relevant for a node
// given its roles. Script types match the Slurm configuration parameter names.
func ScriptTypesForRoles(roles []string) []string {
	set := make(map[string]struct{})
	for _, role := range roles {
		switch role {
		case RoleController:
			for _, s := range []string{"PrologSlurmctld", "EpilogSlurmctld"} {
				set[s] = struct{}{}
			}
		case RoleCompute, RoleWorker: // RoleWorker is deprecated alias
			for _, s := range []string{"Prolog", "Epilog", "TaskProlog", "TaskEpilog", "HealthCheckProgram", "RebootProgram"} {
				set[s] = struct{}{}
			}
		}
	}
	// Stable order.
	canonical := []string{
		"Prolog", "Epilog", "TaskProlog", "TaskEpilog",
		"PrologSlurmctld", "EpilogSlurmctld",
		"HealthCheckProgram", "RebootProgram",
	}
	var result []string
	for _, s := range canonical {
		if _, ok := set[s]; ok {
			result = append(result, s)
		}
	}
	return result
}

// hasRole reports whether roles contains the given role string.
func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}
