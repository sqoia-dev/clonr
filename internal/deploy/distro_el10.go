package deploy

// el10Driver targets Rocky Linux 10, RHEL 10, and AlmaLinux 10.
// Deploy-side file writes are identical to el9; the elBase implementation
// covers NetworkManager keyfiles and GRUB2 for all EL versions.
//
// Known el10 differences from el9 (informational; these affect image build,
// not the deploy-side writes handled here):
//   - systemd 256 (vs 252 in el9) — journal config unchanged for deploy writes.
//   - dnf5 replaces dnf4 as the primary package manager — affects installKernelInChroot
//     callers in finalize.go; that code is distro-aware via its own heuristics.
//   - NetworkManager 1.46 — keyfile format remains backwards-compatible with el9.
type el10Driver struct{ elBase }

func init() { RegisterDriver(&el10Driver{elBase{major: 10}}) }

func (d *el10Driver) Distro() Distro { return Distro{Family: "el", Major: 10} }
