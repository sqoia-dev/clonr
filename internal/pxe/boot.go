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
// Presents a 5-second boot menu. The default action "disk" uses `sanboot` instead
// of `exit` because SeaBIOS (Proxmox/QEMU VMs) restarts the PXE loop on exit rather
// than falling through to the next boot order entry, causing an infinite PXE loop.
// sanboot uses iPXE's INT 13h chainload to explicitly boot the first local disk (0x80).
//
// The "reimage" option re-chains to the boot endpoint with force_reimage=1, which the
// server uses to mark the node for reimage and serve the deploy initramfs on the next
// PXE request.
//
// Visual layout uses iPXE cpair colour pairs (blue/white header, dark body) and
// item --gap for non-selectable separator and info lines.
const diskBootBIOSTemplate = `#!ipxe

# --- menu ------------------------------------------------------------------
menu clonr -- Boot Manager (BIOS)
item --gap --
item --gap --                    c l o n r   B o o t   M a n a g e r
item --gap --
item --gap --                    Node : {{.Hostname}}
item --gap --                    MAC  : ${mac}
item --gap --
item --gap --               --------------------------------------------------
item --default disk --timeout 5000 disk        Boot from disk      [auto 5s]
item reimage                                   Reimage this node
item rescue                                    Rescue shell
item --gap --               --------------------------------------------------
choose --default disk --timeout 5000 target && goto ${target} || goto disk

:disk
sanboot --no-describe --drive 0x80 || exit

:reimage
chain {{.ServerURL}}/api/v1/boot/ipxe?mac=${mac}&force_reimage=1 || goto disk

:rescue
echo
echo  Rescue shell is not yet configured for this node.
echo  Contact your administrator or reimage to recover.
echo
sleep 10
goto disk
`

// diskBootUEFITemplate is the iPXE response for UEFI-firmware nodes in NodeStateDeployed.
//
// Presents a 5-second boot menu. The default "disk" action chain-loads grubx64.efi
// directly from the clonr server (served from the image's extracted EFI binary).
// This is more reliable than plain `exit` on OVMF, which depends on BootOrder being
// correctly set via efibootmgr — chain-loading works regardless of BootOrder state.
//
// Falls back to plain `exit` (returns to firmware) if the grub chain fails (e.g. for
// images where grub.efi was not extracted because the image is BIOS-type or was built
// before grub.efi extraction was added). The fallback `exit` restores the previous
// behavior so UEFI nodes with correct BootOrder still boot correctly.
//
// The "reimage" option re-chains to the boot endpoint with force_reimage=1.
//
// Visual layout uses iPXE cpair colour pairs (blue/white header, dark body) and
// item --gap for non-selectable separator and info lines.
const diskBootUEFITemplate = `#!ipxe

# --- menu ------------------------------------------------------------------
menu clonr -- Boot Manager (UEFI)
item --gap --
item --gap --                    c l o n r   B o o t   M a n a g e r
item --gap --
item --gap --                    Node : {{.Hostname}}
item --gap --                    MAC  : ${mac}
item --gap --
item --gap --               --------------------------------------------------
item --default disk --timeout 5000 disk        Boot from disk      [auto 5s]
item reimage                                   Reimage this node
item rescue                                    Rescue shell
item --gap --               --------------------------------------------------
choose --default disk --timeout 5000 target && goto ${target} || goto disk

:disk
chain {{.ServerURL}}/api/v1/boot/grub.efi?mac=${mac} || exit

:reimage
chain {{.ServerURL}}/api/v1/boot/ipxe?mac=${mac}&force_reimage=1 || goto disk

:rescue
echo
echo  Rescue shell is not yet configured for this node.
echo  Contact your administrator or reimage to recover.
echo
sleep 10
goto disk
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
	Hostname  string
	// ServerURL is the public URL of clonr-serverd (e.g. http://10.99.0.1:8080).
	// Used to build the grub.efi chain URL and the reimage re-chain URL.
	// The ${mac} variable in the template is expanded by iPXE at runtime.
	ServerURL string
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
//
// serverURL is the public URL of clonr-serverd (e.g. http://10.99.0.1:8080).
// It is embedded in the boot script for the grub.efi chain URL and the reimage
// re-chain URL. The ${mac} variable in the script is expanded by iPXE at runtime.
func GenerateDiskBootScript(hostname, firmware, serverURL string) ([]byte, error) {
	tmpl := diskBootUEFITmpl
	if strings.EqualFold(firmware, "bios") {
		tmpl = diskBootBIOSTmpl
	}
	data := diskBootScriptData{Hostname: hostname, ServerURL: serverURL}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render disk boot script: %w", err)
	}
	return buf.Bytes(), nil
}
