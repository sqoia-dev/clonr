package posixid_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sqoia-dev/clustr/internal/posixid"
)

// ─── stub IDSource ────────────────────────────────────────────────────────────

type stubSource struct {
	cfgs     map[posixid.Role]posixid.Config
	ldapUIDs []int
	ldapGIDs []int
	sysUIDs  []int
	sysGIDs  []int
}

func (s *stubSource) ListLDAPUIDs(_ context.Context) ([]int, error) { return s.ldapUIDs, nil }
func (s *stubSource) ListLDAPGIDs(_ context.Context) ([]int, error) { return s.ldapGIDs, nil }
func (s *stubSource) ListSysUIDs(_ context.Context) ([]int, error)  { return s.sysUIDs, nil }
func (s *stubSource) ListSysGIDs(_ context.Context) ([]int, error)  { return s.sysGIDs, nil }
func (s *stubSource) GetConfig(_ context.Context, role posixid.Role) (posixid.Config, error) {
	if cfg, ok := s.cfgs[role]; ok {
		return cfg, nil
	}
	return posixid.Config{}, errors.New("no config for role " + string(role))
}

func ldapUserCfg() posixid.Config {
	return posixid.Config{
		UIDMin:            10000,
		UIDMax:            60000,
		GIDMin:            10000,
		GIDMax:            60000,
		ReservedUIDRanges: []posixid.IDRange{{0, 9999}},
		ReservedGIDRanges: []posixid.IDRange{{0, 9999}},
	}
}

func systemAccountCfg() posixid.Config {
	return posixid.Config{
		UIDMin:            200,
		UIDMax:            999,
		GIDMin:            200,
		GIDMax:            999,
		ReservedUIDRanges: []posixid.IDRange{{0, 199}},
		ReservedGIDRanges: []posixid.IDRange{{0, 199}},
	}
}

func bothRoleSource() *stubSource {
	return &stubSource{
		cfgs: map[posixid.Role]posixid.Config{
			posixid.RoleLDAPUser:      ldapUserCfg(),
			posixid.RoleSystemAccount: systemAccountCfg(),
		},
	}
}

// ─── Role-split allocation tests ─────────────────────────────────────────────

// TestAllocateUID_RoleSystemAccount verifies that auto-allocated system-account
// UIDs are < 1000 (in the 200-999 range).
func TestAllocateUID_RoleSystemAccount(t *testing.T) {
	src := bothRoleSource()
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background(), posixid.RoleSystemAccount)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid < 200 || uid >= 1000 {
		t.Fatalf("expected system account UID in [200,999], got %d", uid)
	}
}

// TestAllocateUID_RoleLDAPUser verifies that auto-allocated LDAP-user UIDs
// are >= 10000.
func TestAllocateUID_RoleLDAPUser(t *testing.T) {
	src := bothRoleSource()
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid < 10000 {
		t.Fatalf("expected LDAP user UID >= 10000, got %d", uid)
	}
}

// TestAllocateUID_RoleMismatch verifies that allocating with a role whose range
// doesn't overlap the other role's range produces distinct, non-colliding results.
func TestAllocateUID_RoleRangesDontOverlap(t *testing.T) {
	src := bothRoleSource()
	a := posixid.New(src)

	sysUID, err := a.AllocateUID(context.Background(), posixid.RoleSystemAccount)
	if err != nil {
		t.Fatalf("system account alloc: %v", err)
	}
	ldapUID, err := a.AllocateUID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("ldap user alloc: %v", err)
	}

	if sysUID >= 1000 {
		t.Errorf("system account UID %d >= 1000 (leaked into LDAP range)", sysUID)
	}
	if ldapUID < 10000 {
		t.Errorf("ldap user UID %d < 10000 (fell into system range)", ldapUID)
	}
}

// TestAllocateUID_RoleSystemAccount_ReservedSkipped verifies that IDs in the
// system account reserved range (0-199) are never allocated.
func TestAllocateUID_RoleSystemAccount_ReservedSkipped(t *testing.T) {
	// Fill 200-299 as used; next should be 300.
	used := make([]int, 100)
	for i := range used {
		used[i] = 200 + i
	}
	src := &stubSource{
		cfgs:    map[posixid.Role]posixid.Config{posixid.RoleSystemAccount: systemAccountCfg()},
		sysUIDs: used,
	}
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background(), posixid.RoleSystemAccount)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 300 {
		t.Fatalf("expected 300, got %d", uid)
	}
}

// TestAllocateUID_RoleSystemAccount_Exhausted verifies ErrRangeExhausted when
// the system account range is fully used.
func TestAllocateUID_RoleSystemAccount_Exhausted(t *testing.T) {
	used := make([]int, 800) // 200-999 = 800 IDs
	for i := range used {
		used[i] = 200 + i
	}
	src := &stubSource{
		cfgs:    map[posixid.Role]posixid.Config{posixid.RoleSystemAccount: systemAccountCfg()},
		sysUIDs: used,
	}
	a := posixid.New(src)
	_, err := a.AllocateUID(context.Background(), posixid.RoleSystemAccount)
	if !errors.Is(err, posixid.ErrRangeExhausted) {
		t.Fatalf("expected ErrRangeExhausted, got %v", err)
	}
}

// TestValidate_RoleLDAPUser_OutOfRange verifies that a system-range UID is
// rejected when validated as an LDAP user UID.
func TestValidate_RoleLDAPUser_OutOfRange(t *testing.T) {
	src := bothRoleSource()
	a := posixid.New(src)
	err := a.Validate(context.Background(), 500, posixid.KindUID, posixid.RoleLDAPUser)
	if !errors.Is(err, posixid.ErrOutOfRange) && !errors.Is(err, posixid.ErrReserved) {
		t.Fatalf("expected ErrOutOfRange or ErrReserved for UID 500 in ldap_user role, got %v", err)
	}
}

// TestValidate_RoleSystemAccount_OutOfRange verifies that an LDAP-range UID is
// rejected when validated as a system account UID.
func TestValidate_RoleSystemAccount_OutOfRange(t *testing.T) {
	src := bothRoleSource()
	a := posixid.New(src)
	err := a.Validate(context.Background(), 10000, posixid.KindUID, posixid.RoleSystemAccount)
	if !errors.Is(err, posixid.ErrOutOfRange) {
		t.Fatalf("expected ErrOutOfRange for UID 10000 in system_account role, got %v", err)
	}
}

// ─── Existing tests — preserved and updated to pass role ─────────────────────

func defaultCfg() posixid.Config {
	return ldapUserCfg() // existing tests used ldap-user-like range
}

func singleRoleSource(cfg posixid.Config) *stubSource {
	return &stubSource{
		cfgs: map[posixid.Role]posixid.Config{
			posixid.RoleLDAPUser: cfg,
		},
	}
}

func TestAllocateUID_ReturnsLowestFreeInRange(t *testing.T) {
	src := singleRoleSource(defaultCfg())
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 10000 {
		t.Fatalf("expected 10000, got %d", uid)
	}
}

func TestAllocateUID_SkipsUsedLDAPEntries(t *testing.T) {
	src := &stubSource{
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
		ldapUIDs: []int{10000, 10001, 10002},
	}
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 10003 {
		t.Fatalf("expected 10003, got %d", uid)
	}
}

func TestAllocateUID_SkipsUsedSysAccountEntries(t *testing.T) {
	src := &stubSource{
		cfgs:    map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
		sysUIDs: []int{10000},
	}
	a := posixid.New(src)
	uid, err := a.AllocateUID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != 10001 {
		t.Fatalf("expected 10001, got %d", uid)
	}
}

func TestAllocateUID_ReturnsErrRangeExhausted(t *testing.T) {
	cfg := posixid.Config{
		UIDMin:            10000,
		UIDMax:            10002,
		GIDMin:            10000,
		GIDMax:            60000,
		ReservedUIDRanges: nil,
	}
	src := &stubSource{
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: cfg},
		ldapUIDs: []int{10000, 10001, 10002},
	}
	a := posixid.New(src)
	_, err := a.AllocateUID(context.Background(), posixid.RoleLDAPUser)
	if !errors.Is(err, posixid.ErrRangeExhausted) {
		t.Fatalf("expected ErrRangeExhausted, got %v", err)
	}
}

func TestAllocateGID_ReturnsLowestFreeInRange(t *testing.T) {
	src := singleRoleSource(defaultCfg())
	a := posixid.New(src)
	gid, err := a.AllocateGID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != 10000 {
		t.Fatalf("expected 10000, got %d", gid)
	}
}

func TestAllocateGID_SkipsBothLDAPAndSysGIDs(t *testing.T) {
	src := &stubSource{
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
		ldapGIDs: []int{10000},
		sysGIDs:  []int{10001},
	}
	a := posixid.New(src)
	gid, err := a.AllocateGID(context.Background(), posixid.RoleLDAPUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != 10002 {
		t.Fatalf("expected 10002, got %d", gid)
	}
}

func TestCheckCollision_CollisionWithLDAPUID(t *testing.T) {
	src := &stubSource{
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
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
		cfgs:    map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
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
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
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
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
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

func TestValidate_InRangeNotReservedNotColliding(t *testing.T) {
	src := singleRoleSource(defaultCfg())
	a := posixid.New(src)
	if err := a.Validate(context.Background(), 15000, posixid.KindUID, posixid.RoleLDAPUser); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_OutOfRange(t *testing.T) {
	src := singleRoleSource(defaultCfg())
	a := posixid.New(src)
	err := a.Validate(context.Background(), 9999, posixid.KindUID, posixid.RoleLDAPUser)
	if !errors.Is(err, posixid.ErrOutOfRange) && !errors.Is(err, posixid.ErrReserved) {
		t.Fatalf("expected ErrOutOfRange or ErrReserved, got %v", err)
	}
}

func TestValidate_OutOfRangeAboveMax(t *testing.T) {
	src := singleRoleSource(defaultCfg())
	a := posixid.New(src)
	err := a.Validate(context.Background(), 60001, posixid.KindUID, posixid.RoleLDAPUser)
	if !errors.Is(err, posixid.ErrOutOfRange) {
		t.Fatalf("expected ErrOutOfRange, got %v", err)
	}
}

func TestValidate_Reserved(t *testing.T) {
	src := singleRoleSource(defaultCfg())
	a := posixid.New(src)
	// 500 is in the [0,9999] reserved range of the ldap_user config
	err := a.Validate(context.Background(), 500, posixid.KindUID, posixid.RoleLDAPUser)
	if !errors.Is(err, posixid.ErrOutOfRange) && !errors.Is(err, posixid.ErrReserved) {
		t.Fatalf("expected ErrOutOfRange or ErrReserved, got %v", err)
	}
}

func TestValidate_CollidingWithLDAP(t *testing.T) {
	src := &stubSource{
		cfgs:     map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
		ldapUIDs: []int{10500},
	}
	a := posixid.New(src)
	err := a.Validate(context.Background(), 10500, posixid.KindUID, posixid.RoleLDAPUser)
	if !errors.Is(err, posixid.ErrCollision) {
		t.Fatalf("expected ErrCollision, got %v", err)
	}
}

func TestValidate_CollidingWithSysAccount(t *testing.T) {
	src := &stubSource{
		cfgs:    map[posixid.Role]posixid.Config{posixid.RoleLDAPUser: defaultCfg()},
		sysUIDs: []int{11000},
	}
	a := posixid.New(src)
	err := a.Validate(context.Background(), 11000, posixid.KindUID, posixid.RoleLDAPUser)
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
