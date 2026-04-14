package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCode_Names(t *testing.T) {
	cases := []struct {
		code ExitCode
		name string
	}{
		{ExitSuccess, "success"},
		{ExitGeneric, "generic"},
		{ExitConfig, "config"},
		{ExitAuth, "auth"},
		{ExitImageFetch, "image_fetch"},
		{ExitDownload, "download"},
		{ExitPartition, "partition"},
		{ExitFormat, "format"},
		{ExitExtract, "extract"},
		{ExitFinalize, "finalize"},
		{ExitBootloader, "bootloader"},
		{ExitCallback, "callback"},
		{ExitNetwork, "network"},
		{ExitHardware, "hardware"},
		{ExitUnknown, "unknown"},
		{ExitPanic, "panic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.code.Name(); got != tc.name {
				t.Errorf("code %d: Name() = %q, want %q", tc.code, got, tc.name)
			}
		})
	}
}

func TestDeployError_Format(t *testing.T) {
	cause := errors.New("no space left on device")
	de := Wrap(ExitExtract, "extract", cause)

	want := "deploy failed at extract: no space left on device"
	if de.Error() != want {
		t.Errorf("Error() = %q, want %q", de.Error(), want)
	}
}

func TestDeployError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("original error")
	de := Wrap(ExitFinalize, "finalize", inner)

	if !errors.Is(de, inner) {
		t.Error("errors.Is should find the inner error via Unwrap")
	}
}

func TestIsDeployError(t *testing.T) {
	plain := errors.New("plain error")
	if IsDeployError(plain) {
		t.Error("plain error should not be a DeployError")
	}

	wrapped := Wrap(ExitDownload, "download", plain)
	if !IsDeployError(wrapped) {
		t.Error("Wrap result should be a DeployError")
	}

	// Wrapped again — errors.As traverses the chain.
	outer := fmt.Errorf("outer: %w", wrapped)
	if !IsDeployError(outer) {
		t.Error("DeployError wrapped in fmt.Errorf should still be found by IsDeployError")
	}
}

func TestWrap_Classification(t *testing.T) {
	// Verify each failure branch emits the expected code.
	cases := []struct {
		code  ExitCode
		phase string
	}{
		{ExitConfig, "config"},
		{ExitAuth, "auth"},
		{ExitImageFetch, "image_fetch"},
		{ExitDownload, "download"},
		{ExitPartition, "partition"},
		{ExitFormat, "format"},
		{ExitExtract, "extract"},
		{ExitFinalize, "finalize"},
		{ExitBootloader, "bootloader"},
		{ExitCallback, "callback"},
		{ExitNetwork, "network"},
		{ExitHardware, "hardware"},
		{ExitPanic, "panic"},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			de := Wrap(tc.code, tc.phase, errors.New("cause"))
			var got *DeployError
			if !errors.As(de, &got) {
				t.Fatal("errors.As failed")
			}
			if got.Code != tc.code {
				t.Errorf("Code: got %d want %d", got.Code, tc.code)
			}
			if got.Phase != tc.phase {
				t.Errorf("Phase: got %s want %s", got.Phase, tc.phase)
			}
		})
	}
}
