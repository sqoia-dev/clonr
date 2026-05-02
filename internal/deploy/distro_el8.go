package deploy

// el8Driver targets Rocky Linux 8, RHEL 8, AlmaLinux 8, and CentOS 8 Stream.
// Network management and bootloader behavior are identical to el9; the
// implementation delegates entirely to elBase.
//
// Known el8 differences from el9 (informational; not reflected in this driver
// because they affect the image build, not the deploy-side file writes):
//   - dnf module streams differ (Python 3.6/3.8 default vs 3.9/3.11 in el9).
//   - systemd 239 vs 252 — cloud-init cloud.cfg.d format is the same.
//   - NetworkManager 1.32 vs 1.42 — keyfile format is backwards-compatible.
type el8Driver struct{ elBase }

func init() { RegisterDriver(&el8Driver{elBase{major: 8}}) }

func (d *el8Driver) Distro() Distro { return Distro{Family: "el", Major: 8} }
