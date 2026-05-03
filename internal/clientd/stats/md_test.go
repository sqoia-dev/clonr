package stats

import (
	"testing"
)

func TestParseMdstat(t *testing.T) {
	arrays, err := parseMdstat("testdata/proc_mdstat")
	if err != nil {
		t.Fatalf("parseMdstat: %v", err)
	}

	if len(arrays) != 2 {
		t.Fatalf("expected 2 arrays got %d", len(arrays))
	}

	// Find md127 (clean, all 3 disks present)
	var md127, md0 *mdArray
	for i := range arrays {
		switch arrays[i].name {
		case "md127":
			md127 = &arrays[i]
		case "md0":
			md0 = &arrays[i]
		}
	}

	if md127 == nil {
		t.Fatal("md127 not found")
	}
	if md127.missingDisks != 0 {
		t.Errorf("md127.missingDisks: want 0 got %d", md127.missingDisks)
	}

	if md0 == nil {
		t.Fatal("md0 not found")
	}
	// md0 has one (F) disk
	if md0.missingDisks != 1 {
		t.Errorf("md0.missingDisks: want 1 got %d", md0.missingDisks)
	}
	// md0 is in recovery
	if md0.state != "recovering" {
		t.Errorf("md0.state: want recovering got %q", md0.state)
	}
	if md0.recoveryPct < 0 || md0.recoveryPct > 1 {
		t.Errorf("md0.recoveryPct: want ~0.5 got %v", md0.recoveryPct)
	}
}
