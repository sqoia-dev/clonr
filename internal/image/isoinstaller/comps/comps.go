// Package comps parses RHEL-family comps XML to extract environment groups.
// It is intentionally dependency-free (encoding/xml only) so it can be unit
// tested without a real ISO.
package comps

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// EnvironmentGroup describes one installable environment group from an ISO's
// comps XML. The ID maps directly to the kickstart @^<id> syntax.
type EnvironmentGroup struct {
	// ID is the comps <id> value, e.g. "minimal-environment".
	ID string `json:"id"`
	// Name is the human-readable <_name> string, e.g. "Minimal Install".
	Name string `json:"name"`
	// Description is the <_description> text. May be empty.
	Description string `json:"description,omitempty"`
	// DisplayOrder is the <display_order> integer. Used for UI sort ordering.
	// Defaults to 0 when the element is absent.
	DisplayOrder int `json:"display_order,omitempty"`
	// IsDefault is true for the entry whose ID is "minimal-environment",
	// falling back to the entry with the lowest DisplayOrder when
	// "minimal-environment" is absent.
	IsDefault bool `json:"is_default"`
}

// xmlComps is the top-level document structure used by encoding/xml.
type xmlComps struct {
	Environments []xmlEnvironment `xml:"environment"`
}

type xmlEnvironment struct {
	ID           string `xml:"id"`
	Name         string `xml:"_name"`
	Description  string `xml:"_description"`
	DisplayOrder int    `xml:"display_order"`
}

// ParseCompsXML parses raw comps XML bytes and returns the environment groups
// sorted by DisplayOrder ascending (lowest number first). When the XML
// contains no <environment> elements, an empty (non-nil) slice is returned —
// the caller treats this the same as no-comps for UI purposes.
func ParseCompsXML(data []byte) ([]EnvironmentGroup, error) {
	var doc xmlComps
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("comps: parse XML: %w", err)
	}

	groups := make([]EnvironmentGroup, 0, len(doc.Environments))
	for _, e := range doc.Environments {
		if e.ID == "" {
			continue
		}
		groups = append(groups, EnvironmentGroup{
			ID:           e.ID,
			Name:         e.Name,
			Description:  e.Description,
			DisplayOrder: e.DisplayOrder,
		})
	}

	// Sort by DisplayOrder ascending; stable so equal-order entries preserve
	// document order.
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].DisplayOrder < groups[j].DisplayOrder
	})

	// Mark the default: prefer "minimal-environment" by ID match; fall back to
	// the entry with the lowest DisplayOrder (first after sort).
	setDefault(groups)
	return groups, nil
}

// setDefault marks the IsDefault field on the appropriate entry. It mutates
// the slice in place; groups must already be sorted by DisplayOrder.
func setDefault(groups []EnvironmentGroup) {
	if len(groups) == 0 {
		return
	}
	for i := range groups {
		if strings.Contains(groups[i].ID, "minimal") {
			groups[i].IsDefault = true
			return
		}
	}
	// No "minimal" entry — use the first (lowest display_order).
	groups[0].IsDefault = true
}

// ProbeComps extracts and parses the comps XML from an ISO image using 7z.
// It uses 7z -so to pipe file contents directly to stdout without requiring
// a scratch directory.
//
// Returns (nil, nil) when:
//   - repodata/repomd.xml is absent from the ISO (non-RHEL ISOs, minimal ISOs)
//   - repomd.xml has no group or group_gz data entry
//
// Returns a non-nil error only for unexpected I/O or tool failures.
func ProbeComps(isoPath string) ([]EnvironmentGroup, error) {
	// Step 1: extract repodata/repomd.xml
	repomdBytes, err := extractBytesFromISO(isoPath, "repodata/repomd.xml")
	if err != nil || len(repomdBytes) == 0 {
		// No repomd.xml — ISO has no repodata (Ubuntu, minimal ISOs, etc.)
		return nil, nil //nolint:nilerr
	}

	// Step 2: parse repomd.xml to find the comps file location
	compsPath, isGzipped, err := findCompsInRepomd(repomdBytes)
	if err != nil || compsPath == "" {
		// No group data entry — ISO doesn't ship comps
		return nil, nil //nolint:nilerr
	}

	// Step 3: extract the comps file
	compsBytes, err := extractBytesFromISO(isoPath, compsPath)
	if err != nil || len(compsBytes) == 0 {
		return nil, nil //nolint:nilerr
	}

	// Step 4: decompress if needed
	xmlBytes := compsBytes
	if isGzipped {
		xmlBytes, err = gunzipBytes(compsBytes)
		if err != nil {
			return nil, fmt.Errorf("comps: decompress %s: %w", compsPath, err)
		}
	}

	// Step 5: parse the comps XML
	return ParseCompsXML(xmlBytes)
}

// extractBytesFromISO uses 7z to extract a single file from an ISO and
// returns its content as a byte slice. Returns (nil, nil) when the file is
// not found inside the ISO (7z exits non-zero or produces no output).
func extractBytesFromISO(isoPath, isoFilePath string) ([]byte, error) {
	path7z, err := exec.LookPath("7z")
	if err != nil {
		return nil, fmt.Errorf("comps: 7z not found — install p7zip-full: %w", err)
	}
	out, err := exec.Command(path7z, "e", "-so", isoPath, isoFilePath).Output()
	if err != nil || len(out) == 0 {
		// File not present in ISO — treat as soft not-found.
		return nil, nil //nolint:nilerr
	}
	return out, nil
}

// repomdData is the minimal repomd.xml structure we need.
type repomdData struct {
	Data []repomdEntry `xml:"data"`
}

type repomdEntry struct {
	Type     string `xml:"type,attr"`
	Location struct {
		Href string `xml:"href,attr"`
	} `xml:"location"`
}

// findCompsInRepomd parses repomd.xml bytes and returns the location path of
// the comps file and whether it is gzip-compressed. Returns ("", false, nil)
// when there is no group data entry.
func findCompsInRepomd(data []byte) (path string, gzipped bool, err error) {
	var doc repomdData
	if err := xml.Unmarshal(data, &doc); err != nil {
		return "", false, fmt.Errorf("comps: parse repomd.xml: %w", err)
	}
	for _, entry := range doc.Data {
		switch entry.Type {
		case "group_gz":
			href := entry.Location.Href
			// Normalize: strip leading slash if present
			href = strings.TrimPrefix(href, "/")
			// Strip "repodata/" prefix since we access from ISO root
			return href, true, nil
		case "group":
			href := entry.Location.Href
			href = strings.TrimPrefix(href, "/")
			return href, false, nil
		}
	}
	return "", false, nil
}

// gunzipBytes decompresses gzip data.
func gunzipBytes(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
