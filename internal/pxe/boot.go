package pxe

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// iPXE boot script template.
// The ${mac} variable is expanded by iPXE itself at runtime.
//
// Boot script serves both BIOS (undionly.kpxe / Arch:00000) and UEFI (ipxe.efi
// / Arch:00007) clients using the universal iPXE syntax:
//
//   kernel <url> <cmdline>     — loads and prepares the kernel
//   initrd <url>               — loads the initrd (universal form, works in both modes)
//   boot                       — hands off to the kernel
//
// The initrd=initramfs.img parameter in the cmdline is NOT needed with this form
// because iPXE automatically associates the loaded initrd with the kernel when
// there is exactly one initrd loaded. Adding initrd= in the cmdline while also
// using `initrd <url>` can cause confusion in some iPXE builds.
//
// The --name form (`initrd --name initramfs.img`) is required ONLY when multiple
// initrds are loaded and must be referenced by name in the cmdline — skip it here.
//
// clonr.token is a short-lived node-scoped API key minted at PXE-serve time.
// The initramfs init script parses it from /proc/cmdline and exports CLONR_TOKEN
// so that `clonr deploy --auto` can authenticate against the server.
const bootScriptTemplate = `#!ipxe
set server-url {{.ServerURL}}
kernel ${server-url}/api/v1/boot/vmlinuz initrd=initramfs.img clonr.server=${server-url} clonr.mac=${mac} clonr.token={{.Token}} console=ttyS0,115200n8 console=tty0 earlyprintk=vga panic=60
initrd --name initramfs.img ${server-url}/api/v1/boot/initramfs.img
boot
`

var bootTmpl = template.Must(template.New("boot").Parse(bootScriptTemplate))

// bootScriptData holds template vars for the iPXE boot script.
type bootScriptData struct {
	ServerURL string
	Token     string // full clonr-node-<hex> token, embedded in kernel cmdline
}

// GenerateBootScript renders the iPXE boot script for the given server URL and
// node-scoped deploy token. The MAC is left as an iPXE variable (${mac}) so iPXE
// fills it at runtime.
func GenerateBootScript(serverURL, token string) ([]byte, error) {
	data := bootScriptData{ServerURL: serverURL, Token: token}
	var buf bytes.Buffer
	if err := bootTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render boot script: %w", err)
	}
	return buf.Bytes(), nil
}

// diskBootBIOSTemplate is the iPXE response for BIOS-firmware nodes in NodeStateDeployed.
//
// We use `sanboot --no-describe --drive 0x80` instead of `exit` because SeaBIOS
// (used by Proxmox/QEMU VMs) restarts the PXE loop on exit rather than falling
// through to the next boot order entry, causing an infinite PXE loop. sanboot
// uses iPXE's built-in INT 13h chainload to explicitly boot the first local disk
// (0x80), bypassing firmware boot-order handling entirely. This works on both
// SeaBIOS VMs and real BIOS hardware — same pattern used by xCAT and Warewulf.
// Diagnosed by Gilfoyle; VM207 was stuck in the loop before this fix.
//
// The hostname comment is templated in so operators can confirm the correct node
// is receiving the disk-boot response in packet captures or iPXE serial output.
const diskBootBIOSTemplate = `#!ipxe
echo Node {{.Hostname}} is deployed (BIOS) -- booting from local disk via sanboot
sanboot --no-describe --drive 0x80
`

// diskBootUEFITemplate is the iPXE response for UEFI-firmware nodes in NodeStateDeployed.
//
// On UEFI nodes, `sanboot --drive 0x80` uses INT 13h — a BIOS concept not
// available under OVMF/EDK2. On OVMF, sanboot with no SAN device silently fails
// or returns to the firmware picker, so the node never boots from disk.
//
// `exit` returns control from iPXE to the UEFI firmware, which follows the NVRAM
// boot order (set by grub2-install --removable + efibootmgr during finalization)
// and finds grubx64.efi on the ESP. This is the correct pattern for UEFI HTTP boot.
const diskBootUEFITemplate = `#!ipxe
echo Node {{.Hostname}} is deployed (UEFI) -- returning to UEFI firmware boot order
exit
`

// waitRetryTemplate is served to nodes in reimage_pending state that have no
// base_image_id assigned. The node sleeps 60 seconds in iPXE and retries,
// looping until the operator assigns an image and triggers a fresh PXE boot.
// Using iPXE's built-in sleep+goto avoids an immediate flood of PXE requests
// while the operator is still configuring the node.
const waitRetryTemplate = `#!ipxe
echo Node {{.Hostname}} is pending reimage but has no image assigned -- retrying in 60s
:retry
sleep 60
chain ${next-server}/api/v1/boot/ipxe?mac=${mac} || goto retry
`

var waitRetryTmpl = template.Must(template.New("wait-retry").Parse(waitRetryTemplate))

var diskBootBIOSTmpl = template.Must(template.New("diskboot-bios").Parse(diskBootBIOSTemplate))
var diskBootUEFITmpl = template.Must(template.New("diskboot-uefi").Parse(diskBootUEFITemplate))

// diskBootScriptData holds template vars for the disk boot script.
type diskBootScriptData struct {
	Hostname string
}

// GenerateWaitRetryScript returns an iPXE script for nodes in reimage_pending
// state that have no base_image_id assigned. The node sleeps 60 seconds and
// re-chains to the boot endpoint so the operator can assign an image without
// requiring a manual BMC power cycle.
func GenerateWaitRetryScript(hostname string) ([]byte, error) {
	data := diskBootScriptData{Hostname: hostname}
	var buf bytes.Buffer
	if err := waitRetryTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render wait-retry script: %w", err)
	}
	return buf.Bytes(), nil
}

// GenerateDiskBootScript returns an iPXE script that boots the node from local
// disk. Used for nodes in NodeStateDeployed and related states.
//
// firmware must be "bios" or "uefi" (case-insensitive). Any value other than
// "bios" is treated as UEFI (fail-safe: UEFI is the default for new images).
func GenerateDiskBootScript(hostname, firmware string) ([]byte, error) {
	tmpl := diskBootUEFITmpl
	if strings.EqualFold(firmware, "bios") {
		tmpl = diskBootBIOSTmpl
	}
	data := diskBootScriptData{Hostname: hostname}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render disk boot script: %w", err)
	}
	return buf.Bytes(), nil
}
