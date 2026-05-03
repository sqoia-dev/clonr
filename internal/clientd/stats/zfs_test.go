package stats

import (
	"context"
	"os"
	"testing"
)

func TestParseZpoolStatus_Degraded(t *testing.T) {
	data, err := os.ReadFile("testdata/zpool-status-degraded")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	pools := parseZpoolStatus(data)
	if len(pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(pools))
	}

	// Find pools by name.
	poolsByName := make(map[string]zpoolInfo)
	for _, p := range pools {
		poolsByName[p.name] = p
	}

	// "tank" is DEGRADED.
	tank, ok := poolsByName["tank"]
	if !ok {
		t.Fatal("pool 'tank' not found")
	}
	if tank.state != "DEGRADED" {
		t.Errorf("tank state: want DEGRADED, got %q", tank.state)
	}
	// Aggregate errors from the pool row: READ=0 WRITE=0 CKSUM=0 (pool-level row).
	if tank.errRead != 0 {
		t.Errorf("tank errRead: want 0, got %d", tank.errRead)
	}
	if tank.errWrite != 0 {
		t.Errorf("tank errWrite: want 0, got %d", tank.errWrite)
	}
	if tank.errCksum != 0 {
		t.Errorf("tank errCksum: want 0, got %d", tank.errCksum)
	}
	if tank.scrubPct >= 0 {
		t.Errorf("tank scrubPct: want -1 (no scrub in progress), got %v", tank.scrubPct)
	}

	// "backup" is ONLINE.
	backup, ok := poolsByName["backup"]
	if !ok {
		t.Fatal("pool 'backup' not found")
	}
	if backup.state != "ONLINE" {
		t.Errorf("backup state: want ONLINE, got %q", backup.state)
	}
	if backup.scrubPct >= 0 {
		t.Errorf("backup scrubPct: want -1, got %v", backup.scrubPct)
	}
}

func TestZpoolStateVal(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"ONLINE", 0},
		{"DEGRADED", 1},
		{"FAULTED", 2},
		{"OFFLINE", 3},
		{"UNAVAIL", 4},
		{"REMOVED", 5},
		{"UNKNOWN", -1},
		{"", -1},
	}
	for _, c := range cases {
		if got := zpoolStateVal(c.in); got != c.want {
			t.Errorf("zpoolStateVal(%q): want %v got %v", c.in, c.want, got)
		}
	}
}

func TestZFSPlugin_AbsentBinary(t *testing.T) {
	if _, err := findBinary("zpool"); err == nil {
		t.Skip("zpool present in PATH; skipping absent-binary test")
	}

	p := NewZFSPlugin()
	samples := p.Collect(context.Background())
	if len(samples) != 0 {
		t.Errorf("expected 0 samples when binary absent, got %d", len(samples))
	}
}

func TestParseZpoolList(t *testing.T) {
	// Tab-separated: name size alloc free ckpoint expandsz frag cap dedup health altroot
	raw := []byte("tank\t10737418240\t5368709120\t5368709120\t-\t-\t0%\t50\t1.00x\tDEGRADED\t-\n" +
		"backup\t2147483648\t107374182\t2040109466\t-\t-\t0%\t5\t1.00x\tONLINE\t-\n")
	caps := parseZpoolList(raw)
	if v, ok := caps["tank"]; !ok || v != 50.0 {
		t.Errorf("tank capacity: want 50, got %v (ok=%v)", v, ok)
	}
	if v, ok := caps["backup"]; !ok || v != 5.0 {
		t.Errorf("backup capacity: want 5, got %v (ok=%v)", v, ok)
	}
}

func TestParseZpoolStatus_ScrubInProgress(t *testing.T) {
	// Synthetic fixture: pool with scrub in progress.
	raw := []byte(`  pool: data
 state: ONLINE
  scan: scrub in progress since Thu May  1 11:00:00 2026
        2.00G scanned out of 10.0G at 512M/s, 0h0m24s to go
        512M repaired, 25.00% done
config:

	NAME        STATE     READ WRITE CKSUM
	data        ONLINE       0     0     0
	  sda       ONLINE       0     0     0

errors: No known data errors
`)
	pools := parseZpoolStatus(raw)
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(pools))
	}
	if pools[0].scrubPct != 25.0 {
		t.Errorf("scrubPct: want 25.0, got %v", pools[0].scrubPct)
	}
}
