package selector

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// ─── Fake DB ─────────────────────────────────────────────────────────────────

type fakeDB struct {
	nodes          []SelectorNode
	groups         map[string][]NodeID // group name → node IDs
	rackNodes      map[string][]NodeID // rack name → node IDs
	chassisNodes   map[string][]NodeID // enclosure label → node IDs
}

func (f *fakeDB) ListAllNodes(_ context.Context) ([]SelectorNode, error) {
	return f.nodes, nil
}

func (f *fakeDB) ListGroupMemberIDs(_ context.Context, name string) ([]NodeID, error) {
	ids, ok := f.groups[name]
	if !ok {
		return nil, nil
	}
	return ids, nil
}

func (f *fakeDB) ListNodeIDsByRackNames(_ context.Context, names []string) ([]NodeID, error) {
	seen := make(map[NodeID]struct{})
	var out []NodeID
	for _, name := range names {
		for _, id := range f.rackNodes[name] {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func (f *fakeDB) ListNodeIDsByEnclosureLabels(_ context.Context, labels []string) ([]NodeID, error) {
	seen := make(map[NodeID]struct{})
	var out []NodeID
	for _, label := range labels {
		for _, id := range f.chassisNodes[label] {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// makeDB is a helper that builds a fakeDB with nodes n01..n05 where n01..n03
// are active (deployed_verified) and n04..n05 are inactive.
// Rack assignments: rack-a → {n01, n02}, rack-b → {n03, n04}.
func makeDB() *fakeDB {
	return &fakeDB{
		nodes: []SelectorNode{
			{ID: "id-n01", Hostname: "n01", Active: true},
			{ID: "id-n02", Hostname: "n02", Active: true},
			{ID: "id-n03", Hostname: "n03", Active: true},
			{ID: "id-n04", Hostname: "n04", Active: false},
			{ID: "id-n05", Hostname: "n05", Active: false},
			{ID: "id-node01", Hostname: "node01", Active: true},
			{ID: "id-node02", Hostname: "node02", Active: false},
			{ID: "id-node03", Hostname: "node03", Active: true},
			{ID: "id-node04", Hostname: "node04", Active: false},
			{ID: "id-node08", Hostname: "node08", Active: true},
			{ID: "id-node12", Hostname: "node12", Active: true},
			{ID: "id-node13", Hostname: "node13", Active: false},
			{ID: "id-node14", Hostname: "node14", Active: true},
			{ID: "id-node15", Hostname: "node15", Active: true},
		},
		groups: map[string][]NodeID{
			"compute": {"id-n01", "id-n02", "id-n04"},
			"login":   {"id-n03"},
		},
		rackNodes: map[string][]NodeID{
			"rack-a": {"id-n01", "id-n02"},
			"rack-b": {"id-n03", "id-n04"},
		},
	}
}

func sorted(ids []NodeID) []NodeID {
	out := append([]NodeID(nil), ids...)
	sort.Strings(out)
	return out
}

// ─── Hostlist parser tests ────────────────────────────────────────────────────

func TestParseHostlist_Plain(t *testing.T) {
	got, err := ParseHostlist("node01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"node01"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHostlist_SimpleRange(t *testing.T) {
	got, err := ParseHostlist("node[01-04]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"node01", "node02", "node03", "node04"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHostlist_RangeWithSingleElement(t *testing.T) {
	got, err := ParseHostlist("node[03]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"node03"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHostlist_ComboRange(t *testing.T) {
	// node[01-04,08,12-15]
	got, err := ParseHostlist("node[01-04,08,12-15]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"node01", "node02", "node03", "node04",
		"node08",
		"node12", "node13", "node14", "node15",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHostlist_TopLevelComma(t *testing.T) {
	// n01,n[03-05]  — top-level comma separates two tokens
	got, err := ParseHostlist("n01,n[03-05]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"n01", "n03", "n04", "n05"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHostlist_NoPadding(t *testing.T) {
	// Width comes from the boundary strings; "1-3" → no padding
	got, err := ParseHostlist("node[1-3]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"node1", "node2", "node3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHostlist_Empty(t *testing.T) {
	_, err := ParseHostlist("")
	if err == nil {
		t.Fatal("expected error for empty hostlist, got nil")
	}
}

func TestParseHostlist_UnmatchedOpen(t *testing.T) {
	_, err := ParseHostlist("node[01-03")
	if err == nil {
		t.Fatal("expected error for unmatched '[', got nil")
	}
}

func TestParseHostlist_UnmatchedClose(t *testing.T) {
	_, err := ParseHostlist("node01-03]")
	if err == nil {
		t.Fatal("expected error for unmatched ']', got nil")
	}
}

func TestParseHostlist_ReversedRange(t *testing.T) {
	_, err := ParseHostlist("node[05-01]")
	if err == nil {
		t.Fatal("expected error for reversed range, got nil")
	}
}

func TestParseHostlist_Dedup(t *testing.T) {
	// Overlapping top-level tokens should deduplicate.
	got, err := ParseHostlist("node01,node[01-03]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"node01", "node02", "node03"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// ─── Resolve tests ────────────────────────────────────────────────────────────

func TestResolve_EmptySelector(t *testing.T) {
	db := makeDB()
	_, err := Resolve(context.Background(), db, SelectorSet{})
	if err == nil {
		t.Fatal("expected error for empty SelectorSet, got nil")
	}
}

func TestResolve_All(t *testing.T) {
	db := makeDB()
	got, err := Resolve(context.Background(), db, SelectorSet{All: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return all 14 nodes.
	if len(got) != 14 {
		t.Errorf("want 14 nodes, got %d: %v", len(got), got)
	}
}

func TestResolve_Active(t *testing.T) {
	db := makeDB()
	got, err := Resolve(context.Background(), db, SelectorSet{Active: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Active nodes: n01, n02, n03, node01, node03, node08, node12, node14, node15 = 9
	for _, id := range got {
		node := findByID(db.nodes, id)
		if node == nil || !node.Active {
			t.Errorf("inactive node %q included in active-only result", id)
		}
	}
}

func TestResolve_HostlistByName(t *testing.T) {
	db := makeDB()
	got, err := Resolve(context.Background(), db, SelectorSet{Nodes: "n01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []NodeID{"id-n01"}
	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolve_HostlistRange(t *testing.T) {
	db := makeDB()
	got, err := Resolve(context.Background(), db, SelectorSet{Nodes: "node[01-04,08,12-15]"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Includes inactive nodes too — hostlist does not filter by state.
	want := sorted([]NodeID{
		"id-node01", "id-node02", "id-node03", "id-node04",
		"id-node08", "id-node12", "id-node13", "id-node14", "id-node15",
	})
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v, want %v", sorted(got), want)
	}
}

func TestResolve_HostlistNotFound(t *testing.T) {
	db := makeDB()
	_, err := Resolve(context.Background(), db, SelectorSet{Nodes: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown hostname, got nil")
	}
}

func TestResolve_Group(t *testing.T) {
	db := makeDB()
	got, err := Resolve(context.Background(), db, SelectorSet{Group: "compute"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := sorted([]NodeID{"id-n01", "id-n02", "id-n04"})
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v, want %v", sorted(got), want)
	}
}

func TestResolve_RacksByName(t *testing.T) {
	db := makeDB()
	// rack-a contains n01 and n02.
	got, err := Resolve(context.Background(), db, SelectorSet{Racks: "rack-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := sorted([]NodeID{"id-n01", "id-n02"})
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v, want %v", sorted(got), want)
	}
}

func TestResolve_RacksMultiple(t *testing.T) {
	db := makeDB()
	// rack-a + rack-b → n01, n02, n03, n04 (union, deduped).
	got, err := Resolve(context.Background(), db, SelectorSet{Racks: "rack-a,rack-b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := sorted([]NodeID{"id-n01", "id-n02", "id-n03", "id-n04"})
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v, want %v", sorted(got), want)
	}
}

func TestResolve_RacksUnknownRackReturnsEmpty(t *testing.T) {
	db := makeDB()
	// Unknown rack name — the DB returns empty, not an error.
	got, err := Resolve(context.Background(), db, SelectorSet{Racks: "rack-unknown"})
	if err != nil {
		t.Fatalf("unknown rack should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unknown rack should return empty, got %v", got)
	}
}

func TestResolve_ChassisEmptyFallback(t *testing.T) {
	db := makeDB()
	got, err := Resolve(context.Background(), db, SelectorSet{Chassis: "chassis-a"})
	if err != nil {
		t.Fatalf("chassis selector should not error before #138 lands: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("chassis selector should return empty before #138 lands, got %v", got)
	}
}

func TestResolve_IgnoreStatus_WithActive(t *testing.T) {
	db := makeDB()
	// -a --ignore-status should return all nodes (state filter bypassed).
	got, err := Resolve(context.Background(), db, SelectorSet{Active: true, IgnoreStatus: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 14 {
		t.Errorf("ignore-status with -a should return all 14 nodes, got %d", len(got))
	}
}

func TestResolve_ActiveOnlyFilters(t *testing.T) {
	db := makeDB()
	// -a without --ignore-status should return only active nodes.
	got, err := Resolve(context.Background(), db, SelectorSet{Active: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range got {
		node := findByID(db.nodes, id)
		if !node.Active {
			t.Errorf("inactive node %q found in -a result", id)
		}
	}
}

func TestResolve_UnionMultipleSelectors(t *testing.T) {
	db := makeDB()
	// -n n01 combined with -g login should union results.
	got, err := Resolve(context.Background(), db, SelectorSet{
		Nodes: "n01",
		Group: "login",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := sorted([]NodeID{"id-n01", "id-n03"})
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v, want %v", sorted(got), want)
	}
}

func TestResolve_DedupAcrossSelectors(t *testing.T) {
	db := makeDB()
	// n01 appears in both hostlist and group "compute".
	got, err := Resolve(context.Background(), db, SelectorSet{
		Nodes: "n01",
		Group: "compute",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be {n01, n02, n04} — n01 not duplicated.
	want := sorted([]NodeID{"id-n01", "id-n02", "id-n04"})
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v, want %v", sorted(got), want)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func findByID(nodes []SelectorNode, id NodeID) *SelectorNode {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}
