// Package posixid provides a POSIX UID/GID allocator that:
//   - Maintains configurable allocation ranges via the posixid_config DB table.
//   - Enforces reserved ranges (system and distro-typical accounts).
//   - Checks collision against live LDAP entries AND the system_accounts/
//     system_groups tables before allocating or validating an ID.
//
// The allocator is intentionally stateless — it reads current state on every
// call so it is safe for concurrent use and never drifts out of sync with the
// directory.
package posixid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// IDKind distinguishes UID from GID for Validate and CheckCollision.
type IDKind string

const (
	KindUID IDKind = "uid"
	KindGID IDKind = "gid"
)

// ErrRangeExhausted is returned when all IDs in the allocation range are in use.
var ErrRangeExhausted = errors.New("posixid: allocation range exhausted")

// ErrReserved is returned when an ID falls within a reserved range.
var ErrReserved = errors.New("posixid: ID is in a reserved range")

// ErrOutOfRange is returned when an ID falls outside the configured allocation range.
var ErrOutOfRange = errors.New("posixid: ID is outside the configured allocation range")

// ErrCollision is returned when an ID is already in use.
var ErrCollision = errors.New("posixid: ID is already in use")

// IDRange is a [min, max] pair (inclusive on both ends).
type IDRange [2]int

// IDSource is the interface the allocator uses to query live state.
// Both the LDAP directory and the DB satisfy this interface via the Allocator
// constructor.
type IDSource interface {
	// ListLDAPUIDs returns the set of uidNumbers currently in the LDAP directory.
	ListLDAPUIDs(ctx context.Context) ([]int, error)
	// ListLDAPGIDs returns the set of gidNumbers currently in the LDAP directory.
	ListLDAPGIDs(ctx context.Context) ([]int, error)
	// ListSysUIDs returns the set of UIDs in the system_accounts table.
	ListSysUIDs(ctx context.Context) ([]int, error)
	// ListSysGIDs returns the set of GIDs in the system_groups table.
	ListSysGIDs(ctx context.Context) ([]int, error)
	// GetConfig returns the active allocation config.
	GetConfig(ctx context.Context) (Config, error)
}

// Config holds the allocator policy, read from posixid_config.
type Config struct {
	UIDMin            int
	UIDMax            int
	GIDMin            int
	GIDMax            int
	ReservedUIDRanges []IDRange
	ReservedGIDRanges []IDRange
}

// Allocator is the POSIX ID allocator.
type Allocator struct {
	src IDSource
}

// New creates an Allocator backed by the given IDSource.
func New(src IDSource) *Allocator {
	return &Allocator{src: src}
}

// AllocateUID returns the lowest available UID in the configured range that is
// not reserved and not already in use (LDAP or system_accounts).
func (a *Allocator) AllocateUID(ctx context.Context) (int, error) {
	cfg, err := a.src.GetConfig(ctx)
	if err != nil {
		return 0, fmt.Errorf("posixid: read config: %w", err)
	}

	ldapUIDs, err := a.src.ListLDAPUIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("posixid: list LDAP UIDs: %w", err)
	}
	sysUIDs, err := a.src.ListSysUIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("posixid: list sys UIDs: %w", err)
	}

	used := toSet(ldapUIDs, sysUIDs)
	return allocate(cfg.UIDMin, cfg.UIDMax, cfg.ReservedUIDRanges, used)
}

// AllocateGID returns the lowest available GID in the configured range that is
// not reserved and not already in use (LDAP or system_groups).
func (a *Allocator) AllocateGID(ctx context.Context) (int, error) {
	cfg, err := a.src.GetConfig(ctx)
	if err != nil {
		return 0, fmt.Errorf("posixid: read config: %w", err)
	}

	ldapGIDs, err := a.src.ListLDAPGIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("posixid: list LDAP GIDs: %w", err)
	}
	sysGIDs, err := a.src.ListSysGIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("posixid: list sys GIDs: %w", err)
	}

	used := toSet(ldapGIDs, sysGIDs)
	return allocate(cfg.GIDMin, cfg.GIDMax, cfg.ReservedGIDRanges, used)
}

// CheckCollision reports whether id is already in use by any LDAP entry or
// system account/group entry. Returns (true, nil) on collision, (false, nil)
// when free, or (false, err) if the directory or DB could not be queried.
func (a *Allocator) CheckCollision(ctx context.Context, id int, kind IDKind) (bool, error) {
	var allIDs []int
	var err error

	switch kind {
	case KindUID:
		ldap, ldapErr := a.src.ListLDAPUIDs(ctx)
		sys, sysErr := a.src.ListSysUIDs(ctx)
		if ldapErr != nil {
			return false, fmt.Errorf("posixid: check uid collision: list LDAP: %w", ldapErr)
		}
		if sysErr != nil {
			return false, fmt.Errorf("posixid: check uid collision: list sys: %w", sysErr)
		}
		allIDs = append(ldap, sys...)
	case KindGID:
		ldap, ldapErr := a.src.ListLDAPGIDs(ctx)
		sys, sysErr := a.src.ListSysGIDs(ctx)
		if ldapErr != nil {
			return false, fmt.Errorf("posixid: check gid collision: list LDAP: %w", ldapErr)
		}
		if sysErr != nil {
			return false, fmt.Errorf("posixid: check gid collision: list sys: %w", sysErr)
		}
		allIDs = append(ldap, sys...)
	default:
		return false, fmt.Errorf("posixid: unknown kind %q", kind)
	}

	_ = err
	for _, v := range allIDs {
		if v == id {
			return true, nil
		}
	}
	return false, nil
}

// Validate returns an error if id should be rejected:
//   - ErrOutOfRange if id is outside [min, max] for the given kind.
//   - ErrReserved if id falls within a reserved range.
//   - ErrCollision if id is already in use.
func (a *Allocator) Validate(ctx context.Context, id int, kind IDKind) error {
	cfg, err := a.src.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("posixid: read config: %w", err)
	}

	var rangeMin, rangeMax int
	var reserved []IDRange

	switch kind {
	case KindUID:
		rangeMin, rangeMax = cfg.UIDMin, cfg.UIDMax
		reserved = cfg.ReservedUIDRanges
	case KindGID:
		rangeMin, rangeMax = cfg.GIDMin, cfg.GIDMax
		reserved = cfg.ReservedGIDRanges
	default:
		return fmt.Errorf("posixid: unknown kind %q", kind)
	}

	if id < rangeMin || id > rangeMax {
		return fmt.Errorf("%w: %d is outside [%d, %d]", ErrOutOfRange, id, rangeMin, rangeMax)
	}

	for _, r := range reserved {
		if id >= r[0] && id <= r[1] {
			return fmt.Errorf("%w: %d falls in reserved range [%d, %d]", ErrReserved, id, r[0], r[1])
		}
	}

	collision, err := a.CheckCollision(ctx, id, kind)
	if err != nil {
		return fmt.Errorf("posixid: collision check: %w", err)
	}
	if collision {
		return fmt.Errorf("%w: %s %d is already in use", ErrCollision, kind, id)
	}

	return nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// allocate finds the lowest ID in [min, max] that is not reserved and not in used.
func allocate(min, max int, reserved []IDRange, used map[int]struct{}) (int, error) {
	for id := min; id <= max; id++ {
		if isReserved(id, reserved) {
			continue
		}
		if _, ok := used[id]; ok {
			continue
		}
		return id, nil
	}
	return 0, ErrRangeExhausted
}

func isReserved(id int, ranges []IDRange) bool {
	for _, r := range ranges {
		if id >= r[0] && id <= r[1] {
			return true
		}
	}
	return false
}

func toSet(slices ...[]int) map[int]struct{} {
	m := make(map[int]struct{})
	for _, s := range slices {
		for _, v := range s {
			m[v] = struct{}{}
		}
	}
	return m
}

// ParseRanges deserialises a JSON string like [[0,999],[1000,9999]] into
// a slice of IDRange. Returns an empty slice on empty input.
func ParseRanges(raw string) ([]IDRange, error) {
	if raw == "" {
		return nil, nil
	}
	var pairs [][2]int
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		return nil, fmt.Errorf("posixid: parse ranges %q: %w", raw, err)
	}
	out := make([]IDRange, len(pairs))
	for i, p := range pairs {
		out[i] = IDRange{p[0], p[1]}
	}
	return out, nil
}

// SortedIDs returns a sorted copy of ids. Used in tests for deterministic output.
func SortedIDs(ids []int) []int {
	cp := make([]int, len(ids))
	copy(cp, ids)
	sort.Ints(cp)
	return cp
}
