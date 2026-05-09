package hostlist

import (
	"reflect"
	"strings"
	"testing"
)

// TestExpand_Plain — a plain hostname with no brackets is returned verbatim.
func TestExpand_Plain(t *testing.T) {
	got, err := Expand("node01")
	if err != nil {
		t.Fatalf("Expand(node01): unexpected error %v", err)
	}
	if !reflect.DeepEqual(got, []string{"node01"}) {
		t.Errorf("Expand(node01) = %v, want [node01]", got)
	}
}

// TestExpand_SingleElementBracket — node[01] is the same as node01.
func TestExpand_SingleElementBracket(t *testing.T) {
	got, err := Expand("node[01]")
	if err != nil {
		t.Fatalf("Expand(node[01]): unexpected error %v", err)
	}
	if !reflect.DeepEqual(got, []string{"node01"}) {
		t.Errorf("Expand(node[01]) = %v, want [node01]", got)
	}
}

// TestExpand_ContiguousRange — node[01-12] expands to 12 zero-padded names.
func TestExpand_ContiguousRange(t *testing.T) {
	got, err := Expand("node[01-12]")
	if err != nil {
		t.Fatalf("Expand(node[01-12]): unexpected error %v", err)
	}
	if len(got) != 12 {
		t.Fatalf("len = %d, want 12", len(got))
	}
	want := []string{"node01", "node02", "node03", "node04", "node05", "node06",
		"node07", "node08", "node09", "node10", "node11", "node12"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand(node[01-12]) = %v, want %v", got, want)
	}
}

// TestExpand_LargeRange — node[001-128] respects the 3-digit zero-pad.
func TestExpand_LargeRange(t *testing.T) {
	got, err := Expand("node[001-128]")
	if err != nil {
		t.Fatalf("Expand(node[001-128]): unexpected error %v", err)
	}
	if len(got) != 128 {
		t.Fatalf("len = %d, want 128", len(got))
	}
	if got[0] != "node001" {
		t.Errorf("first = %q, want node001", got[0])
	}
	if got[127] != "node128" {
		t.Errorf("last = %q, want node128", got[127])
	}
}

// TestExpand_MixedRangeAndComma — node[01-12,20-25] = 12 + 6 = 18 names.
func TestExpand_MixedRangeAndComma(t *testing.T) {
	got, err := Expand("node[01-12,20-25]")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	if len(got) != 18 {
		t.Fatalf("len = %d, want 18 (12 + 6)", len(got))
	}
	if got[0] != "node01" || got[11] != "node12" || got[12] != "node20" || got[17] != "node25" {
		t.Errorf("boundary names wrong: got %v", []string{got[0], got[11], got[12], got[17]})
	}
}

// TestExpand_OutOfOrder — order is preserved as written by the operator.
func TestExpand_OutOfOrder(t *testing.T) {
	got, err := Expand("node[03,01,02]")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	want := []string{"node03", "node01", "node02"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand(node[03,01,02]) = %v, want %v", got, want)
	}
}

// TestExpand_TopLevelComma — commas outside brackets join independent tokens.
func TestExpand_TopLevelComma(t *testing.T) {
	got, err := Expand("n01,n[03-05],n08")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	want := []string{"n01", "n03", "n04", "n05", "n08"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExpand_MultiBracket — rack[1-3]-node[01-12] yields 36 cross-product names.
func TestExpand_MultiBracket(t *testing.T) {
	got, err := Expand("rack[1-3]-node[01-12]")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	if len(got) != 36 {
		t.Fatalf("len = %d, want 36 (3 × 12)", len(got))
	}
	// First three should be rack1-node01..rack1-node03 (cross-product is
	// outer-loop-first / inner-loop-second, matching written order).
	if got[0] != "rack1-node01" {
		t.Errorf("got[0] = %q, want rack1-node01", got[0])
	}
	if got[11] != "rack1-node12" {
		t.Errorf("got[11] = %q, want rack1-node12", got[11])
	}
	if got[12] != "rack2-node01" {
		t.Errorf("got[12] = %q, want rack2-node01", got[12])
	}
	if got[35] != "rack3-node12" {
		t.Errorf("got[35] = %q, want rack3-node12", got[35])
	}
}

// TestExpand_DedupeAcrossTokens — same name twice = one name in output.
func TestExpand_DedupeAcrossTokens(t *testing.T) {
	got, err := Expand("n01,n[01-02],n02")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	want := []string{"n01", "n02"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (dedupe preserves first occurrence)", got, want)
	}
}

// TestExpand_NoZeroPad — node[1-3] without leading zeros still works.
func TestExpand_NoZeroPad(t *testing.T) {
	got, err := Expand("node[1-3]")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	want := []string{"node1", "node2", "node3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExpand_Whitespace — leading/trailing whitespace inside top-level commas
// is tolerated; whitespace inside the bracket payload is also tolerated.
func TestExpand_Whitespace(t *testing.T) {
	got, err := Expand(" node[01-02] , other03 ")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	want := []string{"node01", "node02", "other03"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExpand_ErrorCases covers every malformed-input path that must NOT
// silently degrade.
func TestExpand_ErrorCases(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantSub string
	}{
		{"empty", "", "empty pattern"},
		{"whitespace only", "    ", "empty pattern"},
		{"unmatched open", "node[01-03", "unmatched '['"},
		{"unmatched close", "node01-03]", "unmatched ']'"},
		{"empty bracket", "node[]", "empty bracket group"},
		{"reversed range", "node[05-01]", "range end 1 < start 5"},
		{"non-numeric", "node[a-c]", "invalid range"},
		{"trailing dash", "node[01-]", "malformed range element"},
		{"leading dash", "node[-05]", "malformed range element"},
		{"bare dash", "node[-]", "malformed range element"},
		{"double comma in bracket", "node[01,,03]", "empty range element"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Expand(tc.pattern)
			if err == nil {
				t.Fatalf("Expand(%q): expected error, got nil", tc.pattern)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Expand(%q): error = %q; want substring %q", tc.pattern, err.Error(), tc.wantSub)
			}
		})
	}
}

// TestCompress_Empty — empty input → empty string.
func TestCompress_Empty(t *testing.T) {
	if got := Compress(nil); got != "" {
		t.Errorf("Compress(nil) = %q, want empty", got)
	}
	if got := Compress([]string{}); got != "" {
		t.Errorf("Compress([]) = %q, want empty", got)
	}
}

// TestCompress_Single — a single name is returned verbatim.
func TestCompress_Single(t *testing.T) {
	if got := Compress([]string{"node01"}); got != "node01" {
		t.Errorf("Compress([node01]) = %q, want node01", got)
	}
}

// TestCompress_ContiguousRun — 12 sequential names → node[01-12].
func TestCompress_ContiguousRun(t *testing.T) {
	names := []string{"node01", "node02", "node03", "node04", "node05", "node06",
		"node07", "node08", "node09", "node10", "node11", "node12"}
	got := Compress(names)
	want := "node[01-12]"
	if got != want {
		t.Errorf("Compress: got %q, want %q", got, want)
	}
}

// TestCompress_GappedRun — gaps coalesce into multiple ranges.
func TestCompress_GappedRun(t *testing.T) {
	names := []string{"n01", "n02", "n03", "n08", "n12", "n13", "n14"}
	got := Compress(names)
	want := "n[01-03,08,12-14]"
	if got != want {
		t.Errorf("Compress: got %q, want %q", got, want)
	}
}

// TestCompress_RoundTrip — Compress(Expand(x)) yields a re-Expandable string
// equivalent to x as a set.
func TestCompress_RoundTrip(t *testing.T) {
	patterns := []string{
		"node[01-12]",
		"node[01-04,08,12-15]",
		"compute[001-128]",
		"single",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			expanded, err := Expand(p)
			if err != nil {
				t.Fatalf("Expand(%q): %v", p, err)
			}
			compressed := Compress(expanded)
			reExpanded, err := Expand(compressed)
			if err != nil {
				t.Fatalf("re-Expand(%q): %v", compressed, err)
			}
			if !reflect.DeepEqual(expanded, reExpanded) {
				t.Errorf("round-trip diverged: %q → %v → %q → %v",
					p, expanded, compressed, reExpanded)
			}
		})
	}
}

// TestCompress_MixedPrefix — names with different prefixes bucket separately.
func TestCompress_MixedPrefix(t *testing.T) {
	names := []string{"compute01", "compute02", "compute03", "gpu01", "gpu02"}
	got := Compress(names)
	want := "compute[01-03],gpu[01-02]"
	if got != want {
		t.Errorf("Compress: got %q, want %q", got, want)
	}
}

// TestCompress_LiteralFallback — names with no trailing digits pass through.
func TestCompress_LiteralFallback(t *testing.T) {
	names := []string{"login-host", "node01", "node02", "controller"}
	got := Compress(names)
	if got != "node[01-02],login-host,controller" {
		t.Errorf("Compress: got %q", got)
	}
}

// TestExpand_TripleBracket — a 3-bracket cross product still works.
func TestExpand_TripleBracket(t *testing.T) {
	got, err := Expand("dc[1-2]-rack[1-2]-n[01-02]")
	if err != nil {
		t.Fatalf("Expand: unexpected error %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8 (2×2×2)", len(got))
	}
	if got[0] != "dc1-rack1-n01" || got[7] != "dc2-rack2-n02" {
		t.Errorf("boundary names wrong: got %v", []string{got[0], got[7]})
	}
}
