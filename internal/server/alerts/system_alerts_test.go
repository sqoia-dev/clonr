package alerts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// openTestDB opens a fresh on-disk SQLite DB in t.TempDir() and runs all
// migrations.  We deliberately avoid in-memory mode so multi-connection
// tests don't accidentally split across separate databases.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// fixedClock returns a clock function whose value advances under the
// caller's control via the returned set fn.
func fixedClock(start time.Time) (now func() time.Time, set func(time.Time)) {
	t := start
	return func() time.Time { return t }, func(nt time.Time) { t = nt }
}

func TestSystemAlerts_PushAndList(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	now, _ := fixedClock(time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC))
	s.now = now

	a, err := s.Push(context.Background(), PushArgs{
		Key:     "raid_degraded",
		Device:  "ctrl0/vd1",
		Level:   LevelWarn,
		Message: "VD1 degraded",
		TTL:     30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if a.Key != "raid_degraded" || a.Device != "ctrl0/vd1" {
		t.Errorf("got %+v, want raid_degraded/ctrl0/vd1", a)
	}
	if a.ExpiresAt == nil {
		t.Errorf("expected ExpiresAt set on Push")
	}

	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
}

func TestSystemAlerts_PushExpires(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	now, set := fixedClock(time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC))
	s.now = now

	if _, err := s.Push(context.Background(), PushArgs{
		Key:     "k",
		Level:   LevelInfo,
		Message: "transient",
		TTL:     30 * time.Second,
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Advance past the TTL.
	set(time.Date(2026, 5, 9, 12, 1, 0, 0, time.UTC))

	// List filters out expired rows even before sweep runs.
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("post-TTL List should be empty, got %d", len(list))
	}

	// SweepExpired stamps cleared_at.
	n, err := s.SweepExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Sweep cleared %d rows, want 1", n)
	}
	// Second sweep is a no-op.
	if n2, _ := s.SweepExpired(context.Background()); n2 != 0 {
		t.Errorf("Second sweep cleared %d rows, want 0", n2)
	}
}

func TestSystemAlerts_SetUnsetRoundtrip(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	now, _ := fixedClock(time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC))
	s.now = now
	ctx := context.Background()

	a, err := s.Set(ctx, SetArgs{
		Key:     "host_unreachable",
		Device:  "node-42",
		Level:   LevelCritical,
		Message: "ping fails",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if a.ExpiresAt != nil {
		t.Errorf("Set should not set ExpiresAt, got %v", a.ExpiresAt)
	}

	cleared, err := s.Unset(ctx, "host_unreachable", "node-42")
	if err != nil {
		t.Fatalf("Unset: %v", err)
	}
	if !cleared {
		t.Errorf("Unset returned false, want true")
	}
	// Second unset is a no-op.
	cleared, _ = s.Unset(ctx, "host_unreachable", "node-42")
	if cleared {
		t.Errorf("Second unset returned true, want false")
	}

	list, _ := s.List(ctx)
	if len(list) != 0 {
		t.Errorf("after Unset, List should be empty, got %d", len(list))
	}
}

func TestSystemAlerts_PushIdempotentSameKeyDevice(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	ctx := context.Background()

	// Push twice with the same (key, device) — should upsert, not duplicate.
	for i := 0; i < 5; i++ {
		if _, err := s.Push(ctx, PushArgs{
			Key: "k", Device: "d", Level: LevelInfo,
			Message: "msg", TTL: time.Hour,
		}); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Errorf("repeated Push: List len = %d, want 1", len(list))
	}
}

func TestSystemAlerts_DifferentDevicesAreSeparate(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	ctx := context.Background()

	for _, dev := range []string{"vd1", "vd2", "vd3"} {
		if _, err := s.Push(ctx, PushArgs{
			Key: "raid_degraded", Device: dev, Level: LevelWarn,
			Message: dev + " degraded", TTL: time.Hour,
		}); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.List(ctx)
	if len(list) != 3 {
		t.Errorf("3 devices: List len = %d, want 3", len(list))
	}
}

func TestSystemAlerts_InvalidLevel(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	_, err := s.Push(context.Background(), PushArgs{
		Key: "k", Level: Level("emergency"), Message: "msg", TTL: time.Hour,
	})
	if err == nil {
		t.Fatalf("Push with invalid level should error")
	}
	// Wrapped via fmt.Errorf, but errors.Is on sentinel must work.
	if !errors.Is(err, ErrInvalidLevel) {
		t.Errorf("error chain: want ErrInvalidLevel, got %v", err)
	}
}

func TestSystemAlerts_EmptyKey(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	if _, err := s.Push(context.Background(), PushArgs{
		Key: "", Level: LevelInfo, TTL: time.Hour,
	}); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("empty key: want ErrEmptyKey, got %v", err)
	}
	if _, err := s.Set(context.Background(), SetArgs{
		Key: "", Level: LevelInfo,
	}); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("Set empty key: want ErrEmptyKey, got %v", err)
	}
	if _, err := s.Unset(context.Background(), "", "x"); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("Unset empty key: want ErrEmptyKey, got %v", err)
	}
}

func TestSystemAlerts_SetExtendsThenUnset(t *testing.T) {
	// A Push followed by Set on the same (key, device) clears expires_at,
	// converting a transient alert into a durable one.
	d := openTestDB(t)
	s := NewStore(d)
	ctx := context.Background()

	if _, err := s.Push(ctx, PushArgs{
		Key: "k", Device: "d", Level: LevelInfo, Message: "transient", TTL: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Set(ctx, SetArgs{
		Key: "k", Device: "d", Level: LevelWarn, Message: "durable",
	})
	if err != nil {
		t.Fatalf("Set on existing: %v", err)
	}
	if a.ExpiresAt != nil {
		t.Errorf("Set should clear ExpiresAt, got %v", a.ExpiresAt)
	}
	if a.Level != LevelWarn || a.Message != "durable" {
		t.Errorf("Set didn't update fields: %+v", a)
	}

	// Now Unset.
	cleared, _ := s.Unset(ctx, "k", "d")
	if !cleared {
		t.Errorf("Unset after Set returned false")
	}
}

func TestSystemAlerts_FieldsRoundtrip(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	ctx := context.Background()

	fields := map[string]any{
		"controller": "ctrl0",
		"vd_index":   float64(1), // JSON unmarshals integers as float64
	}
	if _, err := s.Push(ctx, PushArgs{
		Key: "k", Level: LevelInfo, Message: "msg",
		Fields: fields, TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	got := list[0].Fields
	if got["controller"] != "ctrl0" || got["vd_index"] != 1.0 {
		t.Errorf("fields roundtrip: got %+v, want controller=ctrl0 vd_index=1", got)
	}
}
