package main

// console_cmd_test.go — unit tests for #128 CLI console helpers.

import (
	"strings"
	"testing"
)

// ─── Escape sequence state machine ───────────────────────────────────────────

func TestEscapeStateMachine_DefaultSequence(t *testing.T) {
	// Session starts as if we are after a newline (escSeenNewline).
	state := escSeenNewline
	escSeq := "~."

	// '~' after newline → seenTilde; no disconnect yet.
	newState, disconnect := advanceEscapeState(state, '~', escSeq)
	if disconnect {
		t.Fatal("should not disconnect on '~' alone")
	}
	if newState != escSeenTilde {
		t.Errorf("expected escSeenTilde, got %v", newState)
	}

	// '.' after tilde → disconnect.
	_, disconnect = advanceEscapeState(newState, '.', escSeq)
	if !disconnect {
		t.Fatal("expected disconnect on '~.'")
	}
}

func TestEscapeStateMachine_NotTriggeredMidLine(t *testing.T) {
	// '~.' in the middle of a line (no preceding newline) should NOT disconnect.
	state := escIdle
	escSeq := "~."

	newState, disconnect := advanceEscapeState(state, '~', escSeq)
	if disconnect {
		t.Fatal("should not disconnect on '~' without preceding newline")
	}
	// No preceding newline: state stays idle.
	if newState != escIdle {
		t.Errorf("expected escIdle after '~' from idle, got %v", newState)
	}

	// Even a '.' now should not disconnect.
	_, disconnect = advanceEscapeState(newState, '.', escSeq)
	if disconnect {
		t.Fatal("should not disconnect: '~' was not preceded by newline")
	}
}

func TestEscapeStateMachine_NewlineSetsState(t *testing.T) {
	state := escIdle
	escSeq := "~."

	newState, disconnect := advanceEscapeState(state, '\r', escSeq)
	if disconnect {
		t.Fatal("newline should not disconnect")
	}
	if newState != escSeenNewline {
		t.Errorf("expected escSeenNewline after \\r, got %v", newState)
	}
}

func TestEscapeStateMachine_ResetAfterNonEscape(t *testing.T) {
	// After newline, typing a non-~ character resets to idle.
	state := escSeenNewline
	escSeq := "~."

	newState, disconnect := advanceEscapeState(state, 'a', escSeq)
	if disconnect {
		t.Fatal("should not disconnect on regular char after newline")
	}
	if newState != escIdle {
		t.Errorf("expected escIdle after 'a', got %v", newState)
	}
}

func TestEscapeStateMachine_TildeResetByOtherChar(t *testing.T) {
	// After ~, typing something other than '.' resets to idle.
	state := escSeenTilde
	escSeq := "~."

	newState, disconnect := advanceEscapeState(state, 'x', escSeq)
	if disconnect {
		t.Fatal("should not disconnect on '~x'")
	}
	if newState != escIdle {
		t.Errorf("expected escIdle after '~x', got %v", newState)
	}
}

func TestEscapeStateMachine_NewlineAfterTilde(t *testing.T) {
	// After ~, a newline should reset to escSeenNewline (the newline itself
	// starts the new line context).
	state := escSeenTilde
	escSeq := "~."

	newState, disconnect := advanceEscapeState(state, '\n', escSeq)
	if disconnect {
		t.Fatal("should not disconnect on newline after ~")
	}
	if newState != escSeenNewline {
		t.Errorf("expected escSeenNewline after newline, got %v", newState)
	}
}

// ─── WebSocket URL builder ────────────────────────────────────────────────────

func TestBuildConsoleWSURL_HTTP(t *testing.T) {
	wsURL, err := buildConsoleWSURL("http://localhost:8080", "abc123", "ipmi-sol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(wsURL, "ws://") {
		t.Errorf("expected ws:// scheme, got: %s", wsURL)
	}
	if !strings.Contains(wsURL, "/api/v1/console/abc123") {
		t.Errorf("expected console path in URL, got: %s", wsURL)
	}
	if !strings.Contains(wsURL, "mode=ipmi-sol") {
		t.Errorf("expected mode=ipmi-sol in URL, got: %s", wsURL)
	}
}

func TestBuildConsoleWSURL_HTTPS(t *testing.T) {
	wsURL, err := buildConsoleWSURL("https://clustr.example.com", "node01", "ssh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(wsURL, "wss://") {
		t.Errorf("expected wss:// scheme, got: %s", wsURL)
	}
}

func TestBuildConsoleWSURL_NoMode(t *testing.T) {
	wsURL, err := buildConsoleWSURL("http://localhost:8080", "node01", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(wsURL, "mode=") {
		t.Errorf("expected no mode query param in URL, got: %s", wsURL)
	}
}

// ─── Multi-target rejection ───────────────────────────────────────────────────

// TestConsoleCmd_RequiresExactlyOneNode verifies that the console command
// rejects invocations without -n (cobra enforces MarkFlagRequired) and that
// the error message is clear.
func TestConsoleCmd_RequiresExactlyOneNode(t *testing.T) {
	cmd := newConsoleCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	// Execute without -n flag.
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when -n is not provided")
	}
	// Cobra should report the flag as required.
	// The error may come from MarkFlagRequired or our own validation.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "node") && !strings.Contains(errMsg, "required") {
		t.Errorf("expected error about required node flag, got: %s", errMsg)
	}
}

// TestConsoleCmd_MutuallyExclusiveModes verifies that --ipmi-sol and --ssh
// together produce a clean error before any network call is made.
func TestConsoleCmd_MutuallyExclusiveModes(t *testing.T) {
	cmd := newConsoleCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"-n", "node01", "--ipmi-sol", "--ssh"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --ipmi-sol and --ssh are given")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got: %s", err.Error())
	}
}
