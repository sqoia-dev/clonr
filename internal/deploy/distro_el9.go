package deploy

// el9Driver targets Rocky Linux 9, RHEL 9, AlmaLinux 9, and CentOS 9 Stream.
// This is the primary production target for clustr v1.0.
// All deploy-side file writes delegate to elBase.
type el9Driver struct{ elBase }

func init() { RegisterDriver(&el9Driver{elBase{major: 9}}) }

func (d *el9Driver) Distro() Distro { return Distro{Family: "el", Major: 9} }
