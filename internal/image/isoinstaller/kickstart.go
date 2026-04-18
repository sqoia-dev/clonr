package isoinstaller

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"text/template"
)

// AutoInstallConfig holds the rendered automated-install configuration files
// for a given distro. Different distros need different files in different places.
type AutoInstallConfig struct {
	// Format identifies which installer automation format was generated.
	Format AutoInstallFormat

	// KickstartContent is the rendered kickstart / preseed / autoinstall file.
	// For RHEL-family this is the ks.cfg content.
	// For Ubuntu this is the user-data content (cloud-init autoinstall).
	// For Debian this is the preseed.cfg content.
	KickstartContent string

	// MetaDataContent is the cloud-init meta-data content (Ubuntu only).
	// Empty for non-Ubuntu distros.
	MetaDataContent string

	// ISOLabel is the volume label that causes Anaconda / the installer to
	// auto-discover the kickstart drive. OEMDRV for RHEL, CIDATA for Ubuntu.
	ISOLabel string
}

// templateData holds the variables injected into each install config template.
// targetDisk is the block device name exposed to the install VM. The install
// VM attaches the target disk via virtio-blk (-drive if=virtio,...), which the
// kernel enumerates as vda — NOT sda. All installer templates must use this
// name; hardcoding sda causes Anaconda/preseed/AutoYaST to abort with
// "disk does not exist".
const targetDisk = "vda"

type templateData struct {
	// RootPasswordHash is a SHA-512 crypt(3) hash of the root password.
	RootPasswordHash string
	// DiskSizeGB is the target disk size, used in preseed size hints.
	DiskSizeGB int
	// TargetDisk is the block device name for the install target (e.g. "vda").
	TargetDisk string
	// DefaultUser is the optional non-root username to create in the image.
	// Empty string means no user directive is emitted.
	DefaultUser string
	// DefaultPasswordHash is the SHA-512 crypt hash used for both the root
	// account and the DefaultUser account when a password was supplied.
	DefaultPasswordHash string
}

const defaultRootPasswordHash = "$6$rounds=4096$clonr$oJJBrlGPtKS6kxQe7yLm.lXX/XKNEDXkJxhXbXONnR5Rb2FIWKijYcpg/0E1n3W6B9Ik8n3Zd7gH8kO35i3o1"

// hashPassword hashes a plaintext password with SHA-512 crypt using
// openssl passwd -6, which is available on all modern Linux systems.
// It is safe to call with an empty password — returns an empty string
// so callers can distinguish "no password supplied" from a hash.
func hashPassword(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	out, err := exec.Command("openssl", "passwd", "-6", plaintext).Output()
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GenerateAutoInstallConfig produces the automated-install configuration for
// the given distro and build options. Returns an AutoInstallConfig with the
// rendered file content(s) ready to be written to a seed ISO.
func GenerateAutoInstallConfig(distro Distro, opts BuildOptions, customKickstart string) (*AutoInstallConfig, error) {
	// Hash the caller-supplied password (if any) once, reuse across all templates.
	// Never log opts.DefaultPassword — it is plaintext.
	pwHash := defaultRootPasswordHash
	if opts.DefaultPassword != "" {
		h, err := hashPassword(opts.DefaultPassword)
		if err != nil {
			return nil, fmt.Errorf("generate config: %w", err)
		}
		pwHash = h
	}

	data := templateData{
		RootPasswordHash:    pwHash,
		DiskSizeGB:          opts.DiskSizeGB,
		TargetDisk:          targetDisk,
		DefaultUser:         opts.DefaultUsername,
		DefaultPasswordHash: pwHash,
	}

	switch distro.Format() {
	case FormatKickstart:
		return generateKickstart(distro, data, opts, customKickstart)
	case FormatAutoInstall:
		return generateUbuntuAutoInstall(data, opts)
	case FormatPreseed:
		return generateDebianPreseed(data)
	case FormatAutoYaST:
		return generateAutoYaST(data)
	case FormatAnswers:
		return generateAlpineAnswers(data)
	default:
		return generateKickstart(distro, data, opts, customKickstart)
	}
}

// ── RHEL family (Rocky / Alma / CentOS / RHEL) ───────────────────────────────

// ksTemplateData is injected into the RHEL kickstart template.
type ksTemplateData struct {
	templateData
	Distro         string
	RoleIDs        []string
	Packages       []string
	Services       []string
	InstallUpdates bool
	NeedsNVIDIA    bool
	NeedsLustre    bool
	NeedsBeeGFS    bool
	// HasDefaultUser is true when a non-root user should be created.
	HasDefaultUser bool
	// Firmware is the firmware mode: "uefi" or "bios".
	// Controls whether an ESP (vfat /boot/efi) or a biosboot partition is
	// included in the kickstart partition directives.
	Firmware string
}

// joinStrings is the template func for joining slices — kept here so the
// "strings" import is actually referenced and the compiler is happy.
func joinStrings(sep string, elems []string) string { return strings.Join(elems, sep) }

var kickstartTemplate = template.Must(template.New("ks").Funcs(template.FuncMap{
	"join": func(elems []string, sep string) string { return strings.Join(elems, sep) },
	"eq":   func(a, b string) bool { return a == b },
}).Parse(`# clonr auto-generated kickstart
# Distro:   {{.Distro}}
# Roles:    {{join .RoleIDs ", "}}
# Firmware: {{.Firmware}}
# This kickstart produces a minimal, identity-scrubbed base image suitable
# for capture as a clonr BaseImage. It is NOT intended as a production kickstart.
cdrom
lang en_US.UTF-8
keyboard us
timezone UTC --utc
rootpw --iscrypted {{.RootPasswordHash}}
{{- if .HasDefaultUser}}
user --name={{.DefaultUser}} --password={{.DefaultPasswordHash}} --iscrypted --groups=wheel
{{- end}}
selinux --permissive
firewall --disabled
network --bootproto=dhcp --device=link --activate
skipx
firstboot --disabled

zerombr
clearpart --all --initlabel --disklabel=gpt
bootloader --location=mbr --boot-drive={{.TargetDisk}} --append="console=ttyS0,115200"
{{- if eq .Firmware "uefi"}}
part /boot/efi --fstype=vfat --size=512  --ondisk={{.TargetDisk}} --label=esp --fsoptions="umask=0077,shortname=winnt"
{{- else}}
part biosboot  --fstype=biosboot --size=1    --ondisk={{.TargetDisk}} --label=biosboot
{{- end}}
part /boot     --fstype=xfs     --size=1024 --ondisk={{.TargetDisk}} --label=boot
part /         --fstype=xfs     --size=1    --grow      --ondisk={{.TargetDisk}} --label=root

%packages --ignoremissing
@^minimal-environment
openssh-server
# Boot packages — both firmware modes so a single image works for BIOS and UEFI.
# Content-only images (ADR-0009) must ship with all boot dependencies; runtime
# package installation at deploy time is forbidden (no DNS in deploy initramfs).
grub2-pc
grub2-pc-modules
grub2-efi-x64
grub2-efi-x64-modules
shim-x64
efibootmgr
{{- range .Packages}}
{{.}}
{{- end}}
%end

%post --log=/root/ks-post.log
# ── Base services ──────────────────────────────────────────────────────────
systemctl enable sshd
{{range .Services -}}
systemctl enable {{.}}
{{end -}}
{{- if .InstallUpdates}}
# ── Install OS updates ─────────────────────────────────────────────────────
dnf update -y
{{end -}}
{{- if .NeedsNVIDIA}}
# ── NVIDIA / CUDA (gpu-compute role) ──────────────────────────────────────
# Adds the NVIDIA CUDA repo for RHEL 9 / Rocky 9 / Alma 9.
dnf config-manager --add-repo https://developer.download.nvidia.com/compute/cuda/repos/rhel9/x86_64/cuda-rhel9.repo
dnf install -y kernel-devel-$(uname -r) kernel-headers-$(uname -r) || true
dnf install -y cuda-drivers cuda-toolkit nvidia-driver-NVML || true
systemctl enable nvidia-persistenced || true
{{end -}}
{{- if .NeedsLustre}}
# ── Lustre client (storage role) ──────────────────────────────────────────
dnf config-manager --add-repo https://downloads.whamcloud.com/public/lustre/latest-release/el9.4/client/
dnf install -y lustre-client --nogpgcheck || true
{{end -}}
{{- if .NeedsBeeGFS}}
# ── BeeGFS (storage role) ─────────────────────────────────────────────────
wget -q https://www.beegfs.io/release/beegfs_7.4/dists/beegfs-rhel9.repo -O /etc/yum.repos.d/beegfs-rhel9.repo || true
dnf install -y beegfs-storage beegfs-meta --nogpgcheck || true
{{end}}
# ── Enable SSH password authentication ────────────────────────────────────
# RHEL 10 / Rocky 10 changed the OpenSSH compiled-in default to
# PasswordAuthentication no. Write a drop-in so clonr users can log in
# with the default clonr/clonr credentials after deployment.
mkdir -p /etc/ssh/sshd_config.d
echo "PasswordAuthentication yes" > /etc/ssh/sshd_config.d/60-clonr-password-auth.conf
chmod 600 /etc/ssh/sshd_config.d/60-clonr-password-auth.conf
# ── Strip node identity — regenerated on first boot by clonr finalize ──────
rm -f /etc/machine-id
touch /etc/machine-id
rm -f /etc/ssh/ssh_host_*
rm -f /etc/NetworkManager/system-connections/*
%end

reboot --eject
`))

func generateKickstart(distro Distro, data templateData, opts BuildOptions, customKickstart string) (*AutoInstallConfig, error) {
	if customKickstart != "" {
		return &AutoInstallConfig{
			Format:           FormatKickstart,
			KickstartContent: customKickstart,
			ISOLabel:         "OEMDRV",
		}, nil
	}

	packages, services := MergeRoles(opts.RoleIDs, distro)

	roleIDs := opts.RoleIDs
	if len(roleIDs) == 0 {
		roleIDs = []string{"(none — minimal install)"}
	}

	firmware := opts.Firmware
	if firmware == "" {
		firmware = "uefi" // safe default when not specified
	} else if firmware != "bios" && firmware != "uefi" {
		return nil, fmt.Errorf("isoinstaller: unknown firmware %q -- accepted values are bios and uefi", firmware)
	}

	d := ksTemplateData{
		templateData:   data,
		Distro:         distro.FamilyName(),
		RoleIDs:        roleIDs,
		Packages:       packages,
		Services:       services,
		InstallUpdates: opts.InstallUpdates,
		NeedsNVIDIA:    hasRole(opts.RoleIDs, "gpu-compute"),
		NeedsLustre:    hasRole(opts.RoleIDs, "storage"),
		NeedsBeeGFS:    hasRole(opts.RoleIDs, "storage"),
		HasDefaultUser: opts.DefaultUsername != "",
		Firmware:       firmware,
	}

	var buf bytes.Buffer
	if err := kickstartTemplate.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("render kickstart: %w", err)
	}
	return &AutoInstallConfig{
		Format:           FormatKickstart,
		KickstartContent: buf.String(),
		ISOLabel:         "OEMDRV",
	}, nil
}

// ── Ubuntu (autoinstall / cloud-init) ────────────────────────────────────────

// ubuntuTemplateData extends templateData with role-specific fields for Ubuntu.
type ubuntuTemplateData struct {
	templateData
	Packages       []string
	Services       []string
	InstallUpdates bool
}

var ubuntuUserDataTemplate = template.Must(template.New("ubuntu-ud").Parse(`#cloud-config
autoinstall:
  version: 1
  identity:
    hostname: generic
    username: root
    password: "{{.RootPasswordHash}}"
  locale: en_US.UTF-8
  keyboard:
    layout: us
  network:
    network:
      version: 2
      ethernets:
        any:
          match:
            name: "en*"
          dhcp4: true
  storage:
    layout:
      name: direct
      sizing-policy: all
  packages:
    - openssh-server
{{- range .Packages}}
    - {{.}}
{{- end}}
  late-commands:
    - curtin in-target -- systemctl enable ssh
{{- range .Services}}
    - curtin in-target -- systemctl enable {{.}}
{{- end}}
{{- if .InstallUpdates}}
    - curtin in-target -- apt-get upgrade -y
{{- end}}
    - curtin in-target -- rm -f /etc/machine-id
    - curtin in-target -- touch /etc/machine-id
    - curtin in-target -- rm -f /etc/ssh/ssh_host_*
    - curtin in-target -- rm -f /etc/NetworkManager/system-connections/*
  shutdown: reboot
`))

func generateUbuntuAutoInstall(data templateData, opts BuildOptions) (*AutoInstallConfig, error) {
	packages, services := MergeRoles(opts.RoleIDs, DistroUbuntu)

	ud := ubuntuTemplateData{
		templateData:   data,
		Packages:       packages,
		Services:       services,
		InstallUpdates: opts.InstallUpdates,
	}

	var udBuf bytes.Buffer
	if err := ubuntuUserDataTemplate.Execute(&udBuf, ud); err != nil {
		return nil, fmt.Errorf("render ubuntu user-data: %w", err)
	}
	return &AutoInstallConfig{
		Format:           FormatAutoInstall,
		KickstartContent: udBuf.String(),
		MetaDataContent:  "instance-id: clonr-build\nlocal-hostname: generic\n",
		ISOLabel:         "CIDATA",
	}, nil
}

// ── Debian (preseed) ─────────────────────────────────────────────────────────

var debianPreseedTemplate = template.Must(template.New("debian-preseed").Parse(`# clonr auto-generated Debian preseed
d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/get_hostname string generic
d-i netcfg/get_domain string localdomain
d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i mirror/http/proxy string
d-i passwd/root-password-crypted password {{.RootPasswordHash}}
d-i passwd/make-user boolean false
d-i clock-setup/utc boolean true
d-i time/zone string UTC
d-i partman-auto/disk string /dev/{{.TargetDisk}}
d-i partman-auto/method string regular
d-i partman-auto/choose_recipe select atomic
d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i base-system/install-recommends boolean false
d-i apt-setup/non-free boolean false
d-i apt-setup/contrib boolean false
tasksel tasksel/first multiselect standard
d-i pkgsel/include string openssh-server
d-i pkgsel/upgrade select none
d-i grub-installer/only_debian boolean true
d-i grub-installer/with_other_os boolean true
d-i grub-installer/bootdev string /dev/{{.TargetDisk}}
d-i finish-install/reboot_in_progress note
d-i preseed/late_command string \
  in-target systemctl enable ssh; \
  in-target rm -f /etc/machine-id; \
  in-target touch /etc/machine-id; \
  in-target rm -f /etc/ssh/ssh_host_*
`))

func generateDebianPreseed(data templateData) (*AutoInstallConfig, error) {
	var buf bytes.Buffer
	if err := debianPreseedTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render debian preseed: %w", err)
	}
	return &AutoInstallConfig{
		Format:           FormatPreseed,
		KickstartContent: buf.String(),
		ISOLabel:         "OEMDRV",
	}, nil
}

// ── SUSE / openSUSE (AutoYaST) ───────────────────────────────────────────────

var autoYaSTTemplate = template.Must(template.New("autoyast").Parse(`<?xml version="1.0"?>
<!DOCTYPE profile>
<profile xmlns="http://www.suse.com/1.0/yast2ns" xmlns:config="http://www.suse.com/1.0/configns">
  <general>
    <mode><confirm config:type="boolean">false</confirm></mode>
  </general>
  <language><language>en_US</language><languages/></language>
  <keyboard><keymap>english-us</keymap></keyboard>
  <timezone><hwclock>UTC</hwclock><timezone>UTC</timezone></timezone>
  <networking>
    <keep_install_network config:type="boolean">true</keep_install_network>
    <interfaces config:type="list">
      <interface>
        <bootproto>dhcp</bootproto>
        <name>eth0</name>
        <startmode>auto</startmode>
      </interface>
    </interfaces>
  </networking>
  <partitioning config:type="list">
    <drive>
      <device>/dev/{{.TargetDisk}}</device>
      <disklabel>gpt</disklabel>
      <initialize config:type="boolean">true</initialize>
      <use>all</use>
      <partitions config:type="list">
        <partition>
          <partition_type>primary</partition_type>
          <filesystem config:type="symbol">btrfs</filesystem>
          <mount>/</mount>
          <size>max</size>
        </partition>
      </partitions>
    </drive>
  </partitioning>
  <software>
    <packages config:type="list">
      <package>openssh</package>
    </packages>
    <patterns config:type="list">
      <pattern>base</pattern>
    </patterns>
  </software>
  <users config:type="list">
    <user>
      <username>root</username>
      <user_password>{{.RootPasswordHash}}</user_password>
      <encrypted config:type="boolean">true</encrypted>
    </user>
  </users>
  <scripts>
    <post-scripts config:type="list">
      <script>
        <filename>clonr-scrub.sh</filename>
        <interpreter>shell</interpreter>
        <source><![CDATA[
#!/bin/bash
systemctl enable sshd
rm -f /etc/machine-id && touch /etc/machine-id
rm -f /etc/ssh/ssh_host_*
]]></source>
      </script>
    </post-scripts>
  </scripts>
</profile>
`))

func generateAutoYaST(data templateData) (*AutoInstallConfig, error) {
	var buf bytes.Buffer
	if err := autoYaSTTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render autoyast: %w", err)
	}
	return &AutoInstallConfig{
		Format:           FormatAutoYaST,
		KickstartContent: buf.String(),
		ISOLabel:         "OEMDRV",
	}, nil
}

// ── Alpine (answers file) ─────────────────────────────────────────────────────

var alpineAnswersTemplate = template.Must(template.New("alpine").Parse(`KEYMAPOPTS="us us"
HOSTNAMEOPTS="-n generic"
INTERFACESOPTS="auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
"
DNSOPTS="-d localdomain 8.8.8.8"
TIMEZONEOPTS="-z UTC"
PROXYOPTS="none"
APKREPOSOPTS="-1"
SSHDOPTS="-c openssh"
NTPOPTS="-c chrony"
DISKOPTS="-m sys /dev/{{.TargetDisk}}"
`))

func generateAlpineAnswers(data templateData) (*AutoInstallConfig, error) {
	var buf bytes.Buffer
	if err := alpineAnswersTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render alpine answers: %w", err)
	}
	return &AutoInstallConfig{
		Format:           FormatAnswers,
		KickstartContent: buf.String(),
		ISOLabel:         "OEMDRV",
	}, nil
}

// ensure joinStrings is used to satisfy the compiler — the template FuncMap
// closure captures strings.Join directly, so this just avoids the import warning.
var _ = joinStrings
