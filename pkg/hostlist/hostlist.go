// Package hostlist implements the pdsh / clustershell / pyhostlist range-syntax
// parser used across clustr's CLI, API, and UI layers (Sprint 34 HOSTLIST).
//
// The expression grammar handled by Expand:
//
//	plain                       node01                          → ["node01"]
//	contiguous range            node[01-04]                     → ["node01", "node02", "node03", "node04"]
//	mixed list/range            node[01-04,08,12-15]            → ["node01"…"node04","node08","node12"…"node15"]
//	out-of-order range          node[03,01,02]                  → ["node03","node01","node02"] (preserved)
//	zero-pad widening           node[001-128]                   → ["node001"…"node128"]
//	top-level commas            n01,n[03-05],n08                → ["n01","n03","n04","n05","n08"]
//	multi-bracket cross-product rack[1-3]-node[01-12]            → ["rack1-node01" … "rack3-node12"] (3 × 12 = 36)
//
// The parser is strict about brackets: malformed input (unmatched, empty,
// non-numeric) returns a clear error rather than silently degrading.
//
// Compress is the inverse: given a list of hostnames, produces the most
// compact bracket form possible. It is intentionally conservative — it never
// reorders ranges (preserving operator intent) and falls back to a comma list
// when no compression is possible. Round-tripping Compress(Expand(x)) yields a
// canonical form that may differ from x in spacing or range coalescence but is
// equivalent.
//
// The package has no external dependencies; the existing in-tree
// internal/selector/selector.go ParseHostlist predates this package and is
// kept for back-compat. Multi-bracket support is the breaking extension over
// the legacy parser, so we move forward in this package rather than mutate
// the older one.
package hostlist

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Expand parses pattern and returns the expanded hostname list.
// Hostnames are returned in the natural left-to-right order produced by the
// pattern; duplicates are dropped (first occurrence wins).
//
// Returns ([], nil) is impossible — an empty/whitespace pattern returns an
// error. The minimum legal pattern is a single literal hostname.
//
// Multi-bracket patterns produce the cross-product of every bracket group, in
// order: rack[1-2]-node[01-03] yields 6 names (rack1-node01, rack1-node02,
// rack1-node03, rack2-node01, rack2-node02, rack2-node03).
func Expand(pattern string) ([]string, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("hostlist: empty pattern")
	}

	tokens, err := splitTopLevel(pattern)
	if err != nil {
		return nil, err
	}

	var out []string
	seen := make(map[string]struct{})
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		expanded, expErr := expandToken(tok)
		if expErr != nil {
			return nil, expErr
		}
		for _, h := range expanded {
			if _, ok := seen[h]; !ok {
				seen[h] = struct{}{}
				out = append(out, h)
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("hostlist: empty pattern after parsing %q", pattern)
	}
	return out, nil
}

// splitTopLevel splits expr by commas that are NOT inside [...] brackets.
// Returns an error on any unbalanced bracket so callers fail fast.
func splitTopLevel(expr string) ([]string, error) {
	var tokens []string
	depth := 0
	start := 0
	for i, ch := range expr {
		switch ch {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return nil, fmt.Errorf("hostlist: unmatched ']' in %q", expr)
			}
			depth--
		case ',':
			if depth == 0 {
				tokens = append(tokens, expr[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("hostlist: unmatched '[' in %q", expr)
	}
	tokens = append(tokens, expr[start:])
	return tokens, nil
}

// expandToken expands a single comma-free token. The token may contain zero,
// one, or multiple bracket groups; multiple groups produce the cross-product.
func expandToken(tok string) ([]string, error) {
	openIdx := strings.IndexByte(tok, '[')
	if openIdx < 0 {
		// No brackets — plain hostname.  Empty token can occur from a
		// trailing/leading bracket close in a recursive call; treat as a
		// single empty string so the cross-product loop in the caller still
		// produces well-formed output.
		if tok == "" {
			return []string{""}, nil
		}
		return []string{tok}, nil
	}
	closeIdx := strings.IndexByte(tok[openIdx:], ']')
	if closeIdx < 0 {
		return nil, fmt.Errorf("hostlist: unmatched '[' in %q", tok)
	}
	closeIdx += openIdx // absolute index

	prefix := tok[:openIdx]
	inner := tok[openIdx+1 : closeIdx]
	suffix := tok[closeIdx+1:]

	// Recurse on the suffix to handle additional bracket groups (cross-product).
	suffixExpansions, err := expandToken(suffix)
	if err != nil {
		return nil, err
	}

	innerNames, err := expandRange(inner, tok)
	if err != nil {
		return nil, err
	}

	// Cross-product: every (innerName, suffixExpansion) pair.
	out := make([]string, 0, len(innerNames)*len(suffixExpansions))
	for _, n := range innerNames {
		for _, s := range suffixExpansions {
			out = append(out, prefix+n+s)
		}
	}
	return out, nil
}

// expandRange parses the inner content of a bracket group (the substring
// between '[' and ']') and returns its expansion in the order specified.
//
// fullToken is the surrounding token (used for error context).
func expandRange(inner, fullToken string) ([]string, error) {
	if inner == "" {
		return nil, fmt.Errorf("hostlist: empty bracket group in %q", fullToken)
	}

	parts := strings.Split(inner, ",")
	var names []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("hostlist: empty range element in %q", fullToken)
		}
		// A single dash inside the part marks a range (start-end).  We
		// deliberately do NOT support negative numbers in hostlists.
		if dash := strings.Index(part, "-"); dash > 0 && dash < len(part)-1 {
			startStr := part[:dash]
			endStr := part[dash+1:]

			startVal, err := strconv.Atoi(startStr)
			if err != nil {
				return nil, fmt.Errorf("hostlist: invalid range start %q in %q", startStr, fullToken)
			}
			endVal, err := strconv.Atoi(endStr)
			if err != nil {
				return nil, fmt.Errorf("hostlist: invalid range end %q in %q", endStr, fullToken)
			}
			if endVal < startVal {
				return nil, fmt.Errorf("hostlist: range end %d < start %d in %q", endVal, startVal, fullToken)
			}
			width := len(startStr)
			if len(endStr) > width {
				width = len(endStr)
			}
			for i := startVal; i <= endVal; i++ {
				names = append(names, fmt.Sprintf("%0*d", width, i))
			}
		} else if part == "-" || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return nil, fmt.Errorf("hostlist: malformed range element %q in %q", part, fullToken)
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("hostlist: invalid range element %q in %q", part, fullToken)
			}
			names = append(names, fmt.Sprintf("%0*d", len(part), n))
		}
	}
	return names, nil
}

// ─── Compress ────────────────────────────────────────────────────────────────

// Compress is the inverse of Expand for the single-bracket case: given a
// list of hostnames, returns the most compact pdsh-style expression that
// expands back to the same set (modulo ordering / zero-pad consistency).
//
// Rules:
//   - Empty input → "" (caller can treat as "no nodes").
//   - Single name → returned verbatim.
//   - Multi-bracket cross-product compression is NOT attempted; the function
//     groups by common prefix+pad, coalesces contiguous numeric ranges, and
//     joins with commas.
//   - Names that don't share a prefix/pad template fall through unchanged
//     and are joined with the rest.
//
// This mirrors the UX we want in the web table ("12 nodes (compute[01-12])")
// without committing to a perfect inverse — operators care that the output
// is short and obviously equivalent, not byte-identical to the original.
func Compress(names []string) string {
	if len(names) == 0 {
		return ""
	}
	// Deduplicate while preserving first-occurrence order.
	seen := make(map[string]struct{}, len(names))
	uniq := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		uniq = append(uniq, n)
	}

	type bucketKey struct {
		prefix string
		width  int
	}
	type bucket struct {
		nums []int
	}

	buckets := make(map[bucketKey]*bucket)
	keyOrder := make([]bucketKey, 0)
	literals := make([]string, 0)

	for _, n := range uniq {
		prefix, numStr, ok := splitTrailingDigits(n)
		if !ok {
			literals = append(literals, n)
			continue
		}
		v, err := strconv.Atoi(numStr)
		if err != nil {
			literals = append(literals, n)
			continue
		}
		k := bucketKey{prefix: prefix, width: len(numStr)}
		b, exists := buckets[k]
		if !exists {
			b = &bucket{}
			buckets[k] = b
			keyOrder = append(keyOrder, k)
		}
		b.nums = append(b.nums, v)
	}

	// Render each bucket: contiguous runs become "a-b", isolated values stay
	// as "n". We sort the numbers so range detection is deterministic, and
	// dedupe within the bucket too.
	rendered := make([]string, 0, len(keyOrder))
	for _, k := range keyOrder {
		b := buckets[k]
		sort.Ints(b.nums)
		nums := dedupInts(b.nums)

		if len(nums) == 1 {
			rendered = append(rendered, fmt.Sprintf("%s%0*d", k.prefix, k.width, nums[0]))
			continue
		}

		groups := coalesceRuns(nums)
		parts := make([]string, 0, len(groups))
		for _, g := range groups {
			if g.lo == g.hi {
				parts = append(parts, fmt.Sprintf("%0*d", k.width, g.lo))
			} else {
				parts = append(parts, fmt.Sprintf("%0*d-%0*d", k.width, g.lo, k.width, g.hi))
			}
		}
		if len(parts) == 1 && !strings.Contains(parts[0], "-") {
			rendered = append(rendered, k.prefix+parts[0])
		} else {
			rendered = append(rendered, fmt.Sprintf("%s[%s]", k.prefix, strings.Join(parts, ",")))
		}
	}

	all := append(rendered, literals...)
	return strings.Join(all, ",")
}

// splitTrailingDigits returns (prefix, digits, ok) where digits is the
// trailing all-digit suffix of name (preserving zero-pad). ok is false if the
// name has no trailing digits.
func splitTrailingDigits(name string) (string, string, bool) {
	i := len(name)
	for i > 0 && isDigit(name[i-1]) {
		i--
	}
	if i == len(name) {
		return name, "", false
	}
	return name[:i], name[i:], true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func dedupInts(nums []int) []int {
	if len(nums) <= 1 {
		return nums
	}
	out := nums[:1]
	for _, n := range nums[1:] {
		if n != out[len(out)-1] {
			out = append(out, n)
		}
	}
	return out
}

type intRun struct{ lo, hi int }

// coalesceRuns groups consecutive integers (n, n+1, n+2, …) into runs.
// Input must be sorted ascending.
func coalesceRuns(nums []int) []intRun {
	if len(nums) == 0 {
		return nil
	}
	runs := []intRun{{lo: nums[0], hi: nums[0]}}
	for _, n := range nums[1:] {
		last := &runs[len(runs)-1]
		if n == last.hi+1 {
			last.hi = n
		} else {
			runs = append(runs, intRun{lo: n, hi: n})
		}
	}
	return runs
}
