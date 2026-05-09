package external

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPingArgv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		host    string
		want    []string
		wantErr bool
	}{
		{
			name: "ipv4",
			host: "10.0.0.1",
			want: []string{"ping", "-c", "1", "-W", "2", "-n", "10.0.0.1"},
		},
		{
			name: "hostname",
			host: "node01.lab.example.com",
			want: []string{"ping", "-c", "1", "-W", "2", "-n", "node01.lab.example.com"},
		},
		{
			name: "ipv6",
			host: "fe80::1%eth0",
			want: []string{"ping", "-c", "1", "-W", "2", "-n", "fe80::1%eth0"},
		},
		{
			name:    "empty",
			host:    "",
			wantErr: true,
		},
		{
			name:    "shell injection attempt",
			host:    "10.0.0.1; rm -rf /",
			wantErr: true,
		},
		{
			name:    "newline injection",
			host:    "10.0.0.1\nfoo",
			wantErr: true,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := PingArgv(c.host)
			if c.wantErr {
				if err == nil {
					t.Fatalf("PingArgv(%q): expected error, got argv=%v", c.host, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PingArgv(%q): unexpected error: %v", c.host, err)
			}
			if !equalArgv(got, c.want) {
				t.Fatalf("PingArgv(%q): got %v, want %v", c.host, got, c.want)
			}
		})
	}
}

func TestIPMIMCArgv(t *testing.T) {
	t.Parallel()
	got, err := IPMIMCArgv("192.168.1.50", "ADMIN", "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"ipmi-sensors",
		"-h", "192.168.1.50",
		"--driver-type=LAN_2_0",
		"--session-timeout=2000",
		"--retransmission-timeout=500",
		"--no-output",
		"-u", "ADMIN",
		"-p", "secret",
	}
	if !equalArgv(got, want) {
		t.Fatalf("IPMIMCArgv: got %v, want %v", got, want)
	}

	// No-creds case (anon BMC auth) still produces a valid argv.
	gotAnon, err := IPMIMCArgv("192.168.1.51", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has := containsArgv(gotAnon, "-u"); has {
		t.Fatalf("IPMIMCArgv anon: argv should not include -u: %v", gotAnon)
	}
	if has := containsArgv(gotAnon, "-p"); has {
		t.Fatalf("IPMIMCArgv anon: argv should not include -p: %v", gotAnon)
	}

	// Empty addr → error.
	if _, err := IPMIMCArgv("", "u", "p"); err == nil {
		t.Fatal("IPMIMCArgv(\"\"): expected error")
	}

	// Injection-shaped addr → error.
	if _, err := IPMIMCArgv("10.0.0.1; cat /etc/passwd", "u", "p"); err == nil {
		t.Fatal("IPMIMCArgv with injection: expected error")
	}
}

func TestSSHBannerMatch(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"SSH-2.0-OpenSSH_9.6\r\n":     true,
		"SSH-2.0-Server\n":            true,
		"SSH-1.99-OldServer\r\n":      true,
		"SSH-1.5-Ancient\n":           false,
		"HTTP/1.1 200 OK\r\n":         false,
		"":                            false,
		"   ":                         false,
		"SSH-2.0-":                    true, // bare SSH-2.0- is technically valid
		"SSH-2.0-banner-with-spaces ": true,
		"  SSH-2.0-leading-space\n":   true, // we trim before match
	}
	for line, want := range cases {
		got := matchSSHBanner(line)
		if got != want {
			t.Errorf("matchSSHBanner(%q) = %v, want %v", line, got, want)
		}
	}
}

// fakeRunner is the test injection point. RunFn lets each test
// describe its own behaviour without building a struct dance.
type fakeRunner struct {
	RunFn func(ctx context.Context, argv ...string) (string, error)
	Calls [][]string
}

func (f *fakeRunner) Run(ctx context.Context, argv ...string) (string, error) {
	f.Calls = append(f.Calls, argv)
	if f.RunFn != nil {
		return f.RunFn(ctx, argv...)
	}
	return "", nil
}

func TestProberRunAllOnce_PingFailureDoesNotShortCircuit(t *testing.T) {
	t.Parallel()
	// runner: ping → error, ipmi-sensors → success.
	r := &fakeRunner{
		RunFn: func(ctx context.Context, argv ...string) (string, error) {
			if len(argv) == 0 {
				return "", errors.New("empty argv")
			}
			switch argv[0] {
			case "ping":
				return "", errors.New("100% packet loss")
			case "ipmi-sensors":
				return "ok", nil
			}
			return "", errors.New("unexpected binary: " + argv[0])
		},
	}
	p := NewProber(r)
	// SkipSSH so the test doesn't actually dial the network.
	p.SkipSSH = true
	p.PingTimeout = 50 * time.Millisecond
	p.IPMITimeout = 50 * time.Millisecond
	res := p.RunAllOnce(context.Background(), ProbeTargets{
		HostIP:  "10.0.0.1",
		BMCAddr: "192.168.1.50",
		BMCUser: "admin",
		BMCPass: "secret",
	})
	if res.Ping != false {
		t.Fatalf("Ping: got %v, want false", res.Ping)
	}
	if res.IPMIMC != true {
		t.Fatalf("IPMIMC: got %v, want true", res.IPMIMC)
	}
	if res.CheckedAt.IsZero() {
		t.Fatal("CheckedAt: should be set even on partial failures")
	}
	// Both probes must have been attempted.
	if len(r.Calls) != 2 {
		t.Fatalf("runner called %d times, want 2 (ping + ipmi-sensors): %v", len(r.Calls), r.Calls)
	}
}

func TestProberRunAllOnce_EmptyTargetsSkipsCleanly(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	p := NewProber(r)
	p.SkipSSH = true
	res := p.RunAllOnce(context.Background(), ProbeTargets{})
	if res.Ping != false || res.SSH != false || res.IPMIMC != false {
		t.Fatalf("empty targets should yield all-false: %+v", res)
	}
	if len(r.Calls) != 0 {
		t.Fatalf("runner called %d times, want 0: %v", len(r.Calls), r.Calls)
	}
}

// equalArgv compares two argv slices.
func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containsArgv reports whether token appears anywhere in argv.
func containsArgv(argv []string, token string) bool {
	for _, a := range argv {
		if strings.EqualFold(a, token) {
			return true
		}
	}
	return false
}
