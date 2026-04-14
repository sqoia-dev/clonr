package pxe

import (
	"bytes"
	"fmt"
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

// diskBootScriptTemplate is the iPXE response returned for nodes in NodeStateDeployed.
//
// iPXE's "exit" command surrenders control back to the BIOS/UEFI firmware, which
// then falls through to the next entry in the boot order — local disk. This
// requires PXE to be first in the BIOS boot order (set once during rack/stack)
// and is the canonical PXE-server-as-source-of-truth pattern (xCAT, Warewulf,
// Cobbler all work this way).
//
// The hostname comment is templated in so operators can confirm the correct node
// is receiving the disk-boot response in packet captures or iPXE serial output.
const diskBootScriptTemplate = `#!ipxe
echo Node {{.Hostname}} is deployed -- booting from local disk
exit
`

var diskBootTmpl = template.Must(template.New("diskboot").Parse(diskBootScriptTemplate))

// diskBootScriptData holds template vars for the disk boot script.
type diskBootScriptData struct {
	Hostname string
}

// GenerateDiskBootScript returns an iPXE script that hands control back to
// the BIOS/UEFI boot order (local disk). Used for nodes in NodeStateDeployed.
func GenerateDiskBootScript(hostname string) ([]byte, error) {
	data := diskBootScriptData{Hostname: hostname}
	var buf bytes.Buffer
	if err := diskBootTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pxe/boot: render disk boot script: %w", err)
	}
	return buf.Bytes(), nil
}
