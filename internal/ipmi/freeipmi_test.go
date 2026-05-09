package ipmi

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// mockRunner records invocations and returns canned output, so unit tests
// can verify argv composition + parser behaviour without invoking real
// binaries.
type mockRunner struct {
	last []string
	out  string
	err  error
}

func (m *mockRunner) Run(_ context.Context, argv ...string) (string, error) {
	m.last = argv
	return m.out, m.err
}

// ─── PowerArgv composition ────────────────────────────────────────────────────

func TestPowerArgv_Remote(t *testing.T) {
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "admin", Password: "secret"}
	tests := []struct {
		name   string
		action FreeIPMIAction
		flag   string
	}{
		{"status", FreeIPMIPowerStatus, "--stat"},
		{"on", FreeIPMIPowerOn, "--on"},
		{"off", FreeIPMIPowerOff, "--off"},
		{"cycle", FreeIPMIPowerCycle, "--cycle"},
		{"reset", FreeIPMIPowerReset, "--reset"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := PowerArgv(c, tc.action)
			if err != nil {
				t.Fatalf("PowerArgv: %v", err)
			}
			if argv[0] != "ipmi-power" {
				t.Errorf("argv[0] = %q, want ipmi-power", argv[0])
			}
			joined := strings.Join(argv, " ")
			for _, want := range []string{"-h 10.0.0.5", "-u admin", "--driver-type=LAN_2_0", tc.flag} {
				if !strings.Contains(joined, want) {
					t.Errorf("argv missing %q: got %s", want, joined)
				}
			}
			// CODEX-FIX-1-FOLLOWUP: the BMC password must NEVER appear on
			// the argv composed by PowerArgv.  /proc/<pid>/cmdline is
			// world-readable while ipmi-power runs, so any -p <password>
			// substring would be observable to local users.  The password
			// flows through a 0600 temp file passed via --password-file at
			// exec time (see runWithPassword); the public *Argv builder is
			// kept password-free so it remains safe to log / inspect.
			assertNoPasswordInArgv(t, argv, "secret")
		})
	}
}

func TestPowerArgv_Local(t *testing.T) {
	c := &FreeIPMIClient{}
	argv, err := PowerArgv(c, FreeIPMIPowerStatus)
	if err != nil {
		t.Fatalf("PowerArgv: %v", err)
	}
	// In local/in-band mode, none of the remote-flag tokens should appear as
	// standalone argv entries. Substring matching on the joined string is too
	// loose because "ipmi-power" itself contains "-p".
	for _, tok := range argv {
		switch tok {
		case "-h", "-u", "-p":
			t.Errorf("local argv must not contain remote flag %q: %v", tok, argv)
		}
		if strings.HasPrefix(tok, "--driver-type") {
			t.Errorf("local argv must not contain %q: %v", tok, argv)
		}
	}
}

func TestPowerArgv_Invalid(t *testing.T) {
	c := &FreeIPMIClient{Host: "10.0.0.5"}
	if _, err := PowerArgv(c, FreeIPMIAction("bogus")); err == nil {
		t.Fatal("expected error for invalid action")
	}
}

// ─── SELArgv composition ──────────────────────────────────────────────────────

func TestSELArgv_List(t *testing.T) {
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "sekret-sel"}
	argv, err := SELArgv(c, FreeIPMISELList)
	if err != nil {
		t.Fatalf("SELArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"ipmi-sel", "-h 10.0.0.5", "--no-header-output",
		"--comma-separated-output", "--output-event-state",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: got %s", want, joined)
		}
	}
	// CODEX-FIX-1-FOLLOWUP: SELArgv must not embed the password either.
	assertNoPasswordInArgv(t, argv, "sekret-sel")
}

func TestSELArgv_Clear(t *testing.T) {
	c := &FreeIPMIClient{Host: "10.0.0.5"}
	argv, err := SELArgv(c, FreeIPMISELClear)
	if err != nil {
		t.Fatalf("SELArgv: %v", err)
	}
	if !strings.Contains(strings.Join(argv, " "), "--clear") {
		t.Errorf("clear argv missing --clear: %v", argv)
	}
}

func TestSELArgv_Invalid(t *testing.T) {
	c := &FreeIPMIClient{}
	if _, err := SELArgv(c, FreeIPMISELOp("nope")); err == nil {
		t.Fatal("expected error for invalid op")
	}
}

// ─── SensorsArgv composition ──────────────────────────────────────────────────

func TestSensorsArgv(t *testing.T) {
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "sekret-sensors"}
	argv := SensorsArgv(c)
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"ipmi-sensors", "-h 10.0.0.5",
		"--no-header-output", "--comma-separated-output", "--output-sensor-state",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: got %s", want, joined)
		}
	}
	// CODEX-FIX-1-FOLLOWUP: SensorsArgv must not embed the password either.
	assertNoPasswordInArgv(t, argv, "sekret-sensors")
}

// ─── Power: end-to-end with mock runner ───────────────────────────────────────

func TestPower_StatusOK(t *testing.T) {
	mr := &mockRunner{out: "10.0.0.5: on\n"}
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "admin", Password: "topSecretBMC", Runner: mr}
	got, err := c.Power(context.Background(), FreeIPMIPowerStatus)
	if err != nil {
		t.Fatalf("Power: %v", err)
	}
	if got != "10.0.0.5: on" {
		t.Errorf("got %q, want %q", got, "10.0.0.5: on")
	}
	if !strings.Contains(strings.Join(mr.last, " "), "--stat") {
		t.Errorf("runner argv missing --stat: %v", mr.last)
	}
	// CODEX-FIX-1-FOLLOWUP: even at the runner layer (the actual argv
	// that would be exec'd) the password value must not appear; only
	// --password-file=<path> may.
	assertNoPasswordInArgv(t, mr.last, "topSecretBMC")
	assertPasswordFileFlag(t, mr.last)
}

func TestPower_RunnerError(t *testing.T) {
	mr := &mockRunner{err: errors.New("BMC unreachable")}
	c := &FreeIPMIClient{Host: "10.0.0.5", Runner: mr}
	if _, err := c.Power(context.Background(), FreeIPMIPowerOn); err == nil {
		t.Fatal("expected error from mock runner")
	}
}

// ─── SEL: end-to-end with mock runner ─────────────────────────────────────────

func TestSEL_List(t *testing.T) {
	mr := &mockRunner{out: "" +
		"1,Apr-14-2024,12:34:56,Temperature,Threshold,Warning,Upper Non-Critical going high\n" +
		"2,Apr-14-2024,12:35:01,CPU0_VRHOT,Discrete,Critical,State Asserted\n"}
	c := &FreeIPMIClient{Host: "10.0.0.5", Runner: mr}
	entries, err := c.SEL(context.Background(), FreeIPMISELList)
	if err != nil {
		t.Fatalf("SEL: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Severity != SELSeverityWarn {
		t.Errorf("entry[0] severity = %q, want %q", entries[0].Severity, SELSeverityWarn)
	}
	if entries[1].Severity != SELSeverityCritical {
		t.Errorf("entry[1] severity = %q, want %q", entries[1].Severity, SELSeverityCritical)
	}
	if entries[0].Sensor != "Temperature" {
		t.Errorf("entry[0] sensor = %q, want Temperature", entries[0].Sensor)
	}
}

func TestSEL_Clear(t *testing.T) {
	mr := &mockRunner{out: ""}
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "selSecret999", Runner: mr}
	entries, err := c.SEL(context.Background(), FreeIPMISELClear)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if entries != nil {
		t.Errorf("clear should return nil entries, got %v", entries)
	}
	if !strings.Contains(strings.Join(mr.last, " "), "--clear") {
		t.Errorf("argv missing --clear: %v", mr.last)
	}
	// CODEX-FIX-1-FOLLOWUP: ensure the SEL runner argv carries the
	// password file flag, not the password itself.
	assertNoPasswordInArgv(t, mr.last, "selSecret999")
	assertPasswordFileFlag(t, mr.last)
}

// ─── Sensors: end-to-end with mock runner ─────────────────────────────────────

func TestSensors_Parse(t *testing.T) {
	mr := &mockRunner{out: "" +
		"1,CPU0_Temp,Temperature,Nominal,42.000,degrees C,'OK'\n" +
		"2,Fan1_RPM,Fan,Warning,200.000,RPM,'Lower Non-Critical'\n" +
		"3,VRM1,Voltage,Critical,1.800,Volts,'Upper Critical going high'\n" +
		"4,DIMM_Ambient,Temperature,N/A,N/A,N/A,'No Reading'\n"}
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "sensorPwHidden", Runner: mr}
	got, err := c.Sensors(context.Background())
	if err != nil {
		t.Fatalf("Sensors: %v", err)
	}
	// CODEX-FIX-1-FOLLOWUP: assert the runner argv carries the password
	// file flag, not the password itself.
	assertNoPasswordInArgv(t, mr.last, "sensorPwHidden")
	assertPasswordFileFlag(t, mr.last)
	if len(got) != 4 {
		t.Fatalf("got %d sensors, want 4", len(got))
	}
	want := []Sensor{
		{Name: "CPU0_Temp", Value: "42.000", Units: "degrees C", Status: "ok"},
		{Name: "Fan1_RPM", Value: "200.000", Units: "RPM", Status: "warning"},
		{Name: "VRM1", Value: "1.800", Units: "Volts", Status: "critical"},
		{Name: "DIMM_Ambient", Value: "N/A", Units: "N/A", Status: "ns"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sensors mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

// ─── Runner abstraction ───────────────────────────────────────────────────────

func TestDefaultRunner_EmptyArgv(t *testing.T) {
	r := defaultFreeIPMIRunner{}
	if _, err := r.Run(context.Background(), []string{}...); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

// ─── BMC password out-of-band channel (CODEX-FIX-1-FOLLOWUP) ──────────────────

// assertNoPasswordInArgv fails the test if the literal password value, the
// `-p` token, or any `--password=` / `--password ` form appears in argv.
// The `--password-file=<path>` form is allowed because the path itself
// is not the secret.  This mirrors cmd/clustr-privhelper/ipmi_test.go's
// TestCommonIPMIArgs_Remote_NoPasswordOnArgv.
func assertNoPasswordInArgv(t *testing.T, argv []string, password string) {
	t.Helper()
	for _, tok := range argv {
		// Exact-token match for `-p` — substring match would falsely fire
		// on "--password-file=..." which contains "-p".
		if tok == "-p" || tok == "--password" {
			t.Fatalf("password flag leaked via argv token %q in %v", tok, argv)
		}
		if password != "" && strings.Contains(tok, password) {
			t.Fatalf("password value %q leaked via argv token %q in %v", password, tok, argv)
		}
		if strings.HasPrefix(tok, "--password=") {
			t.Fatalf("argv must use --password-file=, not --password=: token %q in %v", tok, argv)
		}
	}
}

// assertPasswordFileFlag fails the test when no --password-file=<path>
// token is present in argv.  When the password file is present the file
// must exist on disk with mode 0600 — the worker process or stat tooling
// might otherwise leak the password to other local users.
func assertPasswordFileFlag(t *testing.T, argv []string) {
	t.Helper()
	var pwPath string
	for _, tok := range argv {
		if strings.HasPrefix(tok, "--password-file=") {
			pwPath = strings.TrimPrefix(tok, "--password-file=")
			break
		}
	}
	if pwPath == "" {
		t.Fatalf("argv missing --password-file=<path>: %v", argv)
	}
	// The temp file is defer-cleaned at the end of runWithPassword, so by
	// the time the test inspects mr.last the file may already be gone.
	// Stat only when the file still exists; otherwise treat the cleanup as
	// the expected behaviour.
	info, err := os.Stat(pwPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("stat password file %q: %v", pwPath, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("password file %q mode = %o, want 0600", pwPath, mode)
	}
}

// TestWritePasswordFile_Roundtrip verifies the package-private
// writePasswordFile helper produces a 0600 file containing the password,
// and that empty input yields an empty path.  Mirrors the privhelper
// variant.
func TestWritePasswordFile_Roundtrip(t *testing.T) {
	path, err := writePasswordFile("s3cret")
	if err != nil {
		t.Fatalf("writePasswordFile: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path for non-empty password")
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "s3cret" {
		t.Errorf("contents = %q, want %q", got, "s3cret")
	}

	emptyPath, err := writePasswordFile("")
	if err != nil {
		t.Fatalf("writePasswordFile(empty): %v", err)
	}
	if emptyPath != "" {
		t.Errorf("empty password should yield empty path, got %q", emptyPath)
	}
}

// TestRunWithPassword_AppendsFlagAndCleansUp asserts that runWithPassword
// (a) appends --password-file=<path> to the runner argv when a password
// is present, (b) does NOT append it when the password is empty (in-band
// mode), and (c) removes the temp file after the runner returns — even
// when the runner returns an error (the failure path is the dangerous
// one because a leaked file would persist with a known-readable mode for
// any concurrent local user).
func TestRunWithPassword_AppendsFlagAndCleansUp(t *testing.T) {
	t.Run("with password", func(t *testing.T) {
		mr := &mockRunner{out: "ok"}
		c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "leakCheck", Runner: mr}
		out, err := c.runWithPassword(context.Background(), []string{"ipmi-power", "-h", "10.0.0.5", "--stat"})
		if err != nil {
			t.Fatalf("runWithPassword: %v", err)
		}
		if out != "ok" {
			t.Errorf("got %q, want ok", out)
		}
		// The runner saw --password-file=<path>, never the literal pw.
		assertNoPasswordInArgv(t, mr.last, "leakCheck")
		var pwPath string
		for _, tok := range mr.last {
			if strings.HasPrefix(tok, "--password-file=") {
				pwPath = strings.TrimPrefix(tok, "--password-file=")
			}
		}
		if pwPath == "" {
			t.Fatalf("expected --password-file token in %v", mr.last)
		}
		// File is removed after runWithPassword returns.
		if _, err := os.Stat(pwPath); !os.IsNotExist(err) {
			t.Errorf("password file %q should be removed after run; stat err = %v", pwPath, err)
		}
	})

	t.Run("error path still cleans up", func(t *testing.T) {
		mr := &mockRunner{err: errors.New("BMC unreachable")}
		c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "leakOnError", Runner: mr}
		_, err := c.runWithPassword(context.Background(), []string{"ipmi-power", "--stat"})
		if err == nil {
			t.Fatal("expected error from mock runner")
		}
		var pwPath string
		for _, tok := range mr.last {
			if strings.HasPrefix(tok, "--password-file=") {
				pwPath = strings.TrimPrefix(tok, "--password-file=")
			}
		}
		if pwPath == "" {
			t.Fatalf("expected --password-file token even on error path: %v", mr.last)
		}
		if _, err := os.Stat(pwPath); !os.IsNotExist(err) {
			t.Errorf("password file %q must be cleaned up on runner error", pwPath)
		}
	})

	t.Run("empty password skips flag", func(t *testing.T) {
		mr := &mockRunner{out: ""}
		c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "", Runner: mr}
		_, err := c.runWithPassword(context.Background(), []string{"ipmi-power", "--stat"})
		if err != nil {
			t.Fatalf("runWithPassword: %v", err)
		}
		for _, tok := range mr.last {
			if strings.HasPrefix(tok, "--password-file=") {
				t.Fatalf("empty password must not produce --password-file token: %v", mr.last)
			}
		}
	})
}

// TestCommonArgs_NoPasswordEverEmitted asserts the package-private
// commonArgs builder does not include the password under any condition,
// including the historical -p form.  This is the structural invariant
// that backs CODEX-FIX-1-FOLLOWUP.
func TestCommonArgs_NoPasswordEverEmitted(t *testing.T) {
	cases := []*FreeIPMIClient{
		{Host: "10.0.0.5"},
		{Host: "10.0.0.5", Username: "admin"},
		{Host: "10.0.0.5", Username: "admin", Password: "supersecret-bmc"},
		{Host: "10.0.0.5", Password: "no-username-but-pw-set"},
	}
	for i, c := range cases {
		args := c.commonArgs()
		assertNoPasswordInArgv(t, args, c.Password)
		joined := strings.Join(args, " ")
		_ = i
		if c.Host != "" && !strings.Contains(joined, "-h "+c.Host) {
			t.Errorf("case %d: argv missing -h %s: %v", i, c.Host, args)
		}
	}
}
