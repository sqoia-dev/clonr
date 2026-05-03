package stats

import (
	"testing"
)

func TestParseProcNetDev(t *testing.T) {
	stats, err := parseProcNetDev("testdata/proc_net_dev")
	if err != nil {
		t.Fatalf("parseProcNetDev: %v", err)
	}

	eth0, ok := stats["eth0"]
	if !ok {
		t.Fatal("expected eth0 in net/dev")
	}
	if eth0.rxBytes != 987654321 {
		t.Errorf("eth0.rxBytes: want 987654321 got %d", eth0.rxBytes)
	}
	if eth0.rxErrors != 2 {
		t.Errorf("eth0.rxErrors: want 2 got %d", eth0.rxErrors)
	}
	if eth0.txBytes != 543210987 {
		t.Errorf("eth0.txBytes: want 543210987 got %d", eth0.txBytes)
	}

	lo, ok := stats["lo"]
	if !ok {
		t.Fatal("expected lo in net/dev")
	}
	if lo.rxBytes != 123456789 {
		t.Errorf("lo.rxBytes: want 123456789 got %d", lo.rxBytes)
	}
}
