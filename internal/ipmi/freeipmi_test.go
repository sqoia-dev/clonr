package ipmi

import (
	"context"
	"errors"
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
			for _, want := range []string{"-h 10.0.0.5", "-u admin", "-p secret", "--driver-type=LAN_2_0", tc.flag} {
				if !strings.Contains(joined, want) {
					t.Errorf("argv missing %q: got %s", want, joined)
				}
			}
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
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "p"}
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
	c := &FreeIPMIClient{Host: "10.0.0.5", Username: "u", Password: "p"}
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
}

// ─── Power: end-to-end with mock runner ───────────────────────────────────────

func TestPower_StatusOK(t *testing.T) {
	mr := &mockRunner{out: "10.0.0.5: on\n"}
	c := &FreeIPMIClient{Host: "10.0.0.5", Runner: mr}
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
	c := &FreeIPMIClient{Host: "10.0.0.5", Runner: mr}
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
}

// ─── Sensors: end-to-end with mock runner ─────────────────────────────────────

func TestSensors_Parse(t *testing.T) {
	mr := &mockRunner{out: "" +
		"1,CPU0_Temp,Temperature,Nominal,42.000,degrees C,'OK'\n" +
		"2,Fan1_RPM,Fan,Warning,200.000,RPM,'Lower Non-Critical'\n" +
		"3,VRM1,Voltage,Critical,1.800,Volts,'Upper Critical going high'\n" +
		"4,DIMM_Ambient,Temperature,N/A,N/A,N/A,'No Reading'\n"}
	c := &FreeIPMIClient{Host: "10.0.0.5", Runner: mr}
	got, err := c.Sensors(context.Background())
	if err != nil {
		t.Fatalf("Sensors: %v", err)
	}
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
