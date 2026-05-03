// Package supermicro implements the bios.Provider interface for Supermicro
// servers using the Supermicro Update Manager (sum) utility.
//
// # Operator supply chain
//
// Supermicro's sum is distributed under the Supermicro EULA, which prohibits
// redistribution as part of a third-party product.  clustr does NOT bundle the
// binary.  The operator must:
//
//  1. Download Supermicro Update Manager (sum) from Supermicro's support site.
//     https://www.supermicro.com/en/solutions/management-software/bmc-ipmi → sum
//  2. Extract the archive and locate the `sum` binary for Linux.
//  3. Drop the binary at /var/lib/clustr/vendor-bios/supermicro/sum on the
//     clustr server node.
//  4. Ensure the binary is executable (chmod 0755).
//
// # Supported sum versions
//
// sum 2.x and sum 3.x are supported.  sum 2.x emits INI-like .cfg files;
// sum 3.x emits XML.  The parser detects the format automatically by examining
// the first non-blank line of the config file: an XML declaration or '<'
// prefix triggers XML parsing; anything else triggers INI parsing.
//
// sum 1.x (legacy text format) is NOT supported.
//
// # Binary invocations
//
//   - ReadCurrent:  sum -c GetCurrentBiosCfg --file <tmp-path>
//                   then parse <tmp-path> (INI or XML)
//   - Apply:        sum -c ChangeBiosCfg --file <staging-path>
//                   where <staging-path> is a INI-format file written by Apply
//
// # Privhelper path
//
// For the post-boot drift-check path, Apply writes a staging .cfg file under
// /var/lib/clustr/bios-staging/ and invokes privhelper bios-apply supermicro.
// The privhelper builds the sum -c ChangeBiosCfg --file <path> argv internally.
// Inside initramfs, Apply exec's sum directly (already root, no helper).
package supermicro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sqoia-dev/clustr/internal/bios"
)

// binaryPath is the operator-supplied location for the Supermicro sum binary.
const binaryPath = "/var/lib/clustr/vendor-bios/supermicro/sum"

// supermicroProvider implements bios.Provider for Supermicro via sum.
type supermicroProvider struct {
	// binPath allows test injection of a fake binary path.
	binPath string
}

func init() {
	bios.Register(&supermicroProvider{binPath: binaryPath})
}

// Vendor returns "supermicro".
func (p *supermicroProvider) Vendor() string { return "supermicro" }

// binaryAvailable returns true when the operator-supplied binary exists and is
// executable.
func (p *supermicroProvider) binaryAvailable() bool {
	info, err := os.Stat(p.binPath)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && (info.Mode().Perm()&0o111 != 0)
}

// ReadCurrent executes `sum -c GetCurrentBiosCfg --file <tmp>` and parses the
// resulting config file.  Detects sum 2.x INI-like format vs sum 3.x XML
// automatically.  Returns bios.ErrBinaryMissing when the binary is absent.
func (p *supermicroProvider) ReadCurrent(ctx context.Context) ([]bios.Setting, error) {
	if !p.binaryAvailable() {
		return nil, fmt.Errorf("%w: expected at %s", bios.ErrBinaryMissing, p.binPath)
	}

	// sum writes the config to a file; use a temp file to capture it.
	tmpFile, err := os.CreateTemp("", "clustr-sum-cfg-*.cfg")
	if err != nil {
		return nil, fmt.Errorf("supermicro bios: create temp config file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	cmd := exec.CommandContext(ctx, p.binPath, "-c", "GetCurrentBiosCfg", "--file", tmpPath) //#nosec G204 -- binPath is a fixed operator-supplied path; tmpPath is our own temp file
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("supermicro bios: sum GetCurrentBiosCfg failed (exit %v): %s",
			err, strings.TrimSpace(stderr.String()))
	}

	data, err := os.ReadFile(tmpPath) //#nosec G304 -- tmpPath is our own temp file
	if err != nil {
		return nil, fmt.Errorf("supermicro bios: read config file: %w", err)
	}

	return parseSumConfig(string(data))
}

// parseSumConfig detects whether data is XML (sum 3.x) or INI-like (sum 2.x)
// and dispatches to the appropriate parser.
func parseSumConfig(data string) ([]bios.Setting, error) {
	trimmed := strings.TrimSpace(data)
	if strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<") {
		return parseSumXML(data)
	}
	return parseSumINI(data), nil
}

// ─── INI parser (sum 2.x) ────────────────────────────────────────────────────
//
// sum 2.x emits sections like:
//
//	[Advanced]
//	  Hyper-threading=Enabled
//	  Intel Virtualization Technology=Enabled
//
//	[Boot]
//	  Boot Mode Select=Legacy
//
// Setting names are stored as "<Section>.<Key>" to match the sum attribute
// path format used in SupportedSettings.  Section headers without a section
// name, blank lines, and comment lines (starting with '#' or ';') are skipped.

func parseSumINI(data string) []bios.Setting {
	var settings []bios.Setting
	var currentSection string

	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if key == "" {
			continue
		}

		// Prefix key with section for a namespaced attribute path.
		name := key
		if currentSection != "" {
			name = currentSection + "." + key
		}
		settings = append(settings, bios.Setting{Name: name, Value: value})
	}
	return settings
}

// ─── XML parser (sum 3.x) ────────────────────────────────────────────────────
//
// sum 3.x emits XML like:
//
//	<?xml version="1.0" encoding="UTF-8"?>
//	<BiosCfg>
//	  <Menu name="Advanced">
//	    <Setting name="Hyper-threading" selectedOption="Enabled" />
//	    <Menu name="CPU Configuration">
//	      <Setting name="Intel Virtualization Technology" selectedOption="Enabled" />
//	    </Menu>
//	  </Menu>
//	  <Menu name="Boot">
//	    <Setting name="Boot Mode Select" selectedOption="Legacy" />
//	  </Menu>
//	</BiosCfg>
//
// Nested menus produce names like "Advanced.CPU Configuration.Intel Virtualization Technology".

func parseSumXML(data string) ([]bios.Setting, error) {
	dec := xml.NewDecoder(strings.NewReader(data))

	var settings []bios.Setting
	// sectionStack tracks the hierarchy of Menu/@name elements.
	var sectionStack []string

	for {
		tok, err := dec.Token()
		if err != nil {
			// io.EOF is expected at end of document.
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Menu":
				name := xmlAttr(t, "name")
				sectionStack = append(sectionStack, name)
			case "Setting":
				name := xmlAttr(t, "name")
				value := xmlAttr(t, "selectedOption")
				if name == "" {
					continue
				}
				// Build the full attribute path: "Section[.SubSection]*.Name"
				parts := append(sectionStack, name) //nolint:gocritic // intentional new slice
				fullName := strings.Join(parts, ".")
				settings = append(settings, bios.Setting{Name: fullName, Value: value})
			}
		case xml.EndElement:
			if t.Name.Local == "Menu" && len(sectionStack) > 0 {
				sectionStack = sectionStack[:len(sectionStack)-1]
			}
		}
	}

	return settings, nil
}

// xmlAttr returns the value of an XML attribute named key from a StartElement.
func xmlAttr(t xml.StartElement, key string) string {
	for _, a := range t.Attr {
		if a.Name.Local == key {
			return a.Value
		}
	}
	return ""
}

// Diff delegates to the shared bios.Diff helper.
func (p *supermicroProvider) Diff(desired, current []bios.Setting) ([]bios.Change, error) {
	return bios.Diff(desired, current)
}

// Apply writes a sum 2.x-compatible INI config file containing the desired
// changes and invokes `sum -c ChangeBiosCfg --file <staging-path>`.
//
// For the post-boot path, callers MUST route through privhelper (bios-apply
// verb).  This direct path is used only inside initramfs (already root, no
// privhelper present).
//
// Returns bios.ErrBinaryMissing when the binary is absent.
func (p *supermicroProvider) Apply(ctx context.Context, changes []bios.Change) ([]bios.Change, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	if !p.binaryAvailable() {
		return nil, fmt.Errorf("%w: expected at %s", bios.ErrBinaryMissing, p.binPath)
	}

	stagingDir := "/var/lib/clustr/bios-staging"
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("supermicro bios: create staging dir: %w", err)
	}

	f, err := os.CreateTemp(stagingDir, "bios-profile-*.cfg")
	if err != nil {
		return nil, fmt.Errorf("supermicro bios: create staging file: %w", err)
	}
	stagingPath := f.Name()
	defer os.Remove(stagingPath)

	if werr := writeSumINI(f, changes); werr != nil {
		f.Close()
		return nil, fmt.Errorf("supermicro bios: write staging file: %w", werr)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("supermicro bios: close staging file: %w", err)
	}

	cmd := exec.CommandContext(ctx, p.binPath, "-c", "ChangeBiosCfg", "--file", stagingPath) //#nosec G204 -- binPath is operator-supplied fixed path; stagingPath is our own temp file
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("supermicro bios: sum ChangeBiosCfg failed (exit %v): %s\n%s",
			err, strings.TrimSpace(stderr.String()), strings.TrimSpace(stdout.String()))
	}

	return changes, nil
}

// writeSumINI writes changes to w in sum 2.x INI-compatible format.
// Keys that include a "Section." prefix are grouped under their section header.
// Keys without a prefix are written under [General].
func writeSumINI(w *os.File, changes []bios.Change) error {
	// Group changes by section (the part before the first '.').
	type entry struct{ key, value string }
	sections := make(map[string][]entry)
	var sectionOrder []string
	seen := make(map[string]bool)

	for _, c := range changes {
		section := "General"
		key := c.Name
		if idx := strings.Index(c.Name, "."); idx >= 0 {
			section = c.Name[:idx]
			key = c.Name[idx+1:]
		}
		if !seen[section] {
			sectionOrder = append(sectionOrder, section)
			seen[section] = true
		}
		sections[section] = append(sections[section], entry{key, c.To})
	}

	bw := bufio.NewWriter(w)
	for _, sec := range sectionOrder {
		if _, err := fmt.Fprintf(bw, "[%s]\n", sec); err != nil {
			return err
		}
		for _, e := range sections[sec] {
			if _, err := fmt.Fprintf(bw, "%s=%s\n", e.key, e.value); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(bw); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// SupportedSettings returns a curated set of commonly-edited Supermicro BIOS
// setting names in the "<Section>.<Key>" path format used by this provider.
// Operators may submit keys outside this list; the provider passes them through
// without hard rejection — this list is used only for validation warnings.
//
// Returns nil when the binary is absent (validation is then skipped with a
// warning; failures surface at deploy time).
func (p *supermicroProvider) SupportedSettings(_ context.Context) ([]string, error) {
	if !p.binaryAvailable() {
		return nil, nil
	}
	return []string{
		"Advanced.Hyper-threading",
		"Advanced.Intel Virtualization Technology",
		"Advanced.SR-IOV Support",
		"Advanced.NUMA Nodes Per Socket",
		"Boot.Boot Mode Select",
	}, nil
}

// NewWithBinaryPath constructs a supermicroProvider with a custom binary path.
// Used in tests to inject a fake sum binary.
func NewWithBinaryPath(path string) bios.Provider {
	return &supermicroProvider{binPath: path}
}
