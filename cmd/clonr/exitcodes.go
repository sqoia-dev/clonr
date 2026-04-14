package main

import (
	"errors"
	"fmt"
)

// ExitCode is the typed exit status for the clonr deploy agent.
// Each value maps to a distinct failure class so operators can triage
// incidents without reading logs.
type ExitCode int

const (
	ExitSuccess    ExitCode = 0
	ExitGeneric    ExitCode = 1  // reserved generic failure
	ExitConfig     ExitCode = 2  // missing/invalid CLONR_SERVER, CLONR_MAC, CLONR_TOKEN
	ExitAuth       ExitCode = 3  // 401/403 on any server call
	ExitImageFetch ExitCode = 4  // GetImage / image metadata failed
	ExitDownload   ExitCode = 5  // blob stream failed, throughput watchdog tripped
	ExitPartition  ExitCode = 6  // sgdisk / partprobe / loopdev failed
	ExitFormat     ExitCode = 7  // mkfs.* failures, EBUSY, etc.
	ExitExtract    ExitCode = 8  // tar/rsync during rootfs extract
	ExitFinalize   ExitCode = 9  // fstab, machine-id, systemd-nspawn finalize
	ExitBootloader ExitCode = 10 // grub2-install, efibootmgr
	ExitCallback   ExitCode = 11 // deploy-complete / deploy-failed callback
	ExitNetwork    ExitCode = 12 // DHCP, DNS, TCP to server
	ExitHardware   ExitCode = 13 // disk/NIC enumeration failed, no matching disk
	ExitUnknown    ExitCode = 64 // catch-all for unclassified errors
	ExitPanic      ExitCode = 99 // panic recovery
)

// exitCodeNames maps each ExitCode to its short symbolic name.
// Used in structured log output and the deploy-failed payload.
var exitCodeNames = map[ExitCode]string{
	ExitSuccess:    "success",
	ExitGeneric:    "generic",
	ExitConfig:     "config",
	ExitAuth:       "auth",
	ExitImageFetch: "image_fetch",
	ExitDownload:   "download",
	ExitPartition:  "partition",
	ExitFormat:     "format",
	ExitExtract:    "extract",
	ExitFinalize:   "finalize",
	ExitBootloader: "bootloader",
	ExitCallback:   "callback",
	ExitNetwork:    "network",
	ExitHardware:   "hardware",
	ExitUnknown:    "unknown",
	ExitPanic:      "panic",
}

// Name returns the symbolic name for the exit code (e.g. "extract").
func (c ExitCode) Name() string {
	if name, ok := exitCodeNames[c]; ok {
		return name
	}
	return "unknown"
}

// DeployError is a typed error that carries a classified exit code,
// the deployment phase in which the failure occurred, and the underlying cause.
type DeployError struct {
	Code  ExitCode
	Phase string
	Cause error
}

// Error implements the error interface.
// Format: "deploy failed at <phase>: <cause>"
func (e *DeployError) Error() string {
	return fmt.Sprintf("deploy failed at %s: %v", e.Phase, e.Cause)
}

// Unwrap lets errors.As / errors.Is traverse the chain.
func (e *DeployError) Unwrap() error {
	return e.Cause
}

// Wrap constructs a DeployError with the given code, phase, and underlying error.
func Wrap(code ExitCode, phase string, err error) *DeployError {
	return &DeployError{Code: code, Phase: phase, Cause: err}
}

// IsDeployError reports whether err (or any error in its chain) is a *DeployError.
func IsDeployError(err error) bool {
	var de *DeployError
	return errors.As(err, &de)
}
