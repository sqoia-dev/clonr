package posixid_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sqoia-dev/clustr/internal/posixid"
)

// ─── stub IDSource ────────────────────────────────────────────────────────────

type stubSource struct {
	cfg      posixid.Config
	ldapUIDs []int
	ldapGIDs []int
	sysUIDs  []int
	sysGIDs  []int
}

func (s *stubSource) ListLDAPUIDs(_ context.Context) ([]int, error) { return s.ldapUIDs, nil }
func (s *stubSource) ListLDAPGIDs(_ context.Context) ([]int, error) { return s.ldapGIDs, nil }
func (s *stubSource) ListSysUIDs(_ context.Context) ([]int, error)  { return s.sysUIDs, nil }
func (s *stubSource) ListSysGIDs(_ context.Context) ([]int, error)  { return s.sysGIDs, nil }
func (s *stubSource) GetConfig(_ context.Context) (posixid.Config, error) {
	return s.cfg, nil
}

func defaultCfg() posixid.Config {
	return posixid.Config{
		UIDMin: 10000,
		UIDMax: 60000,
		GIDMin: 10000,
		GIDMax: 60000,
		ReservedUIDRanges: []posixid.IDRange{{0, 999}, {1000, 9999}},
		ReservedGIDRanges: []posixid.IDRange{{0, 999}, {1000, 9999}},
	}
}

// ─── AllocateUID tests ────────────────────────────────────────────────────────

func TestAllocateUID_ReturnsLowestFreeInRange(t *testing.T) {
	src := &stubSource{cfg: defaultCfg()}
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 10000 {
		t.Fatalf("expected 10000, got %d", uid)
	}
}

func TestAllocateUID_SkipsUsedLDAPEntries(t *testing.T) {
	src := &stubSource{
		cfg:      defaultCfg(),
		ldapUIDs: []int{10000, 10001, 10002},
	}
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 10003 {
		t.Fatalf("expected 10003, got %d", uid)
	}
}

func TestAllocateUID_SkipsUsedSysAccountEntries(t *testing.T) {
	src := &stubSource{
		cfg:     defaultCfg(),
		sysUIDs: []int{10000},
	}
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 10001 {
		t.Fatalf("expected 10001, got %d", uid)
	}
}

func TestAllocateUID_ReturnsErrRangeExhausted(t *testing.T) {
	// Narrow range with all IDs taken.
	cfg := posixid.Config{
		UIDMin:            10000,
		UIDMax:            10002,
		GIDMin:            10000,
		GIDMax:            60000,
		ReservedUIDRanges: nil,
	}
	src := &stubSource{
		cfg:      cfg,
		ldapUIDs: []int{10000, 10001, 10002},
	}
	a := posixid.New(src)
	_, err := a.AllocateUID(context.Background())
	if !errors.Is(err, posixid.ErrRangeExhausted) {
		t.Fatalf("expected ErrRangeExhausted, got %v", err)
	}
}

// ─── AllocateGID tests ────────────────────────────────────────────────────────

func TestAllocateGID_ReturnsLowestFreeInRange(t *testing.T) {
	src := &stubSource{cfg: defaultCfg()}
	a := posixid.New(src)
	gid, err := a.AllocateGID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != 10000 {
		t.Fatalf("expected 10000, got %d", gid)
	}
}

func TestAllocateGID_SkipsBothLDAPAndSysGIDs(t *testing.T) {
	src := &stubSource{
		cfg:      defaultCfg(),
		ldapGIDs: []int{10000},
		sysGIDs:  []int{10001},
	}
	a := posixid.New(src)
	gid, err := a.AllocateGID(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != 10002 {
		t.Fatalf("expected 10002, got %d", gid)
	}
}

// ─── CheckCollision tests ─────────────────────────────────────────────────────

func TestCheckCollision_CollisionWithLDAPUID(t *testing.T) {
	src := &stubSource{
		cfg:      defaultCfg(),
		ldapUIDs: []int{12345},
	}
	a := posixid.New(src)
	collision, err := a.CheckCollision(context.Background(), 12345, posixid.KindUID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !collision {
		t.Fatal("expected collision=true")
	}
}

func TestCheckCollision_CollisionWithSysAccount(t *testing.T) {
	src := &stubSource{
		cfg:     defaultCfg(),
		sysUIDs: []int{55000},
	}
	a := posixid.New(src)
	collision, err := a.CheckCollision(context.Background(), 55000, posixid.KindUID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !collision {
		t.Fatal("expected collision=true")
	}
}

func TestCheckCollision_NoCollision(t *testing.T) {
	src := &stubSource{
		cfg:      defaultCfg(),
		ldapUIDs: []int{10000},
		sysUIDs:  []int{10001},
	}
	a := posixid.New(src)
	collision, err := a.CheckCollision(context.Background(), 10002, posixid.KindUID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision {
		t.Fatal("expected collision=false")
	}
}

func TestCheckCollision_GIDCollisionWithLDAP(t *testing.T) {
	src := &stubSource{
		cfg:      defaultCfg(),
		ldapGIDs: []int{20000},
	}
	a := posixid.New(src)
	collision, err := a.CheckCollision(context.Background(), 20000, posixid.KindGID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !collision {
		t.Fatal("expected collision=true")
	}
}

// ─── Validate tests ───────────────────────────────────────────────────────────

func TestValidate_InRangeNotReservedNotColliding(t *testing.T) {
	src := &stubSource{cfg: defaultCfg()}
	a := posixid.New(src)
	if err := a.Validate(context.Background(), 15000, posixid.KindUID); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_OutOfRange(t *testing.T) {
	src := &stubSource{cfg: defaultCfg()}
	a := posixid.New(src)
	err := a.Validate(context.Background(), 9999, posixid.KindUID)
	if !errors.Is(err, posixid.ErrOutOfRange) {
		t.Fatalf("expected ErrOutOfRange, got %v", err)
	}
}

func TestValidate_OutOfRangeAboveMax(t *testing.T) {
	src := &stubSource{cfg: defaultCfg()}
	a := posixid.New(src)
	err := a.Validate(context.Background(), 60001, posixid.KindUID)
	if !errors.Is(err, posixid.ErrOutOfRange) {
		t.Fatalf("expected ErrOutOfRange, got %v", err)
	}
}

func TestValidate_Reserved(t *testing.T) {
	src := &stubSource{cfg: defaultCfg()}
	a := posixid.New(src)
	// 500 is in the [0,999] reserved range
	err := a.Validate(context.Background(), 500, posixid.KindUID)
	if !errors.Is(err, posixid.ErrOutOfRange) && !errors.Is(err, posixid.ErrReserved) {
		t.Fatalf("expected ErrOutOfRange or ErrReserved, got %v", err)
	}
}

func TestValidate_CollidingWithLDAP(t *testing.T) {
	src := &stubSource{
		cfg:      defaultCfg(),
		ldapUIDs: []int{10500},
	}
	a := posixid.New(src)
	err := a.Validate(context.Background(), 10500, posixid.KindUID)
	if !errors.Is(err, posixid.ErrCollision) {
		t.Fatalf("expected ErrCollision, got %v", err)
	}
}

func TestValidate_CollidingWithSysAccount(t *testing.T) {
	src := &stubSource{
		cfg:     defaultCfg(),
		sysUIDs: []int{11000},
	}
	a := posixid.New(src)
	err := a.Validate(context.Background(), 11000, posixid.KindUID)
	if !errors.Is(err, posixid.ErrCollision) {
		t.Fatalf("expected ErrCollision, got %v", err)
	}
}

// ─── ParseRanges tests ────────────────────────────────────────────────────────

func TestParseRanges_Valid(t *testing.T) {
	ranges, err := posixid.ParseRanges(`[[0,999],[1000,9999]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ranges) != 2 {
		t.Fatalf("expected 2 ranges, got %d", len(ranges))
	}
	if ranges[0] != (posixid.IDRange{0, 999}) {
		t.Fatalf("first range wrong: %v", ranges[0])
	}
	if ranges[1] != (posixid.IDRange{1000, 9999}) {
		t.Fatalf("second range wrong: %v", ranges[1])
	}
}

func TestParseRanges_Empty(t *testing.T) {
	ranges, err := posixid.ParseRanges("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ranges) != 0 {
		t.Fatalf("expected empty, got %d", len(ranges))
	}
}

func TestParseRanges_Invalid(t *testing.T) {
	_, err := posixid.ParseRanges(`not-json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
