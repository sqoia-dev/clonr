package bios

import "strings"

// Diff computes the change set needed to bring current → desired.
//
// This is a package-level helper that all Provider implementations may call
// from their Diff method.  It encapsulates the shared logic:
//
//   - Comparison is case-insensitive on setting names (Intel SYSCFG emits
//     mixed-case names that vary by motherboard SKU and firmware version).
//   - Settings present in desired but missing from current are included as
//     Changes with From="".
//   - Settings present in current but absent in desired are ignored (partial
//     override — clustr never reverts settings the profile doesn't mention).
//   - Settings whose current value already equals the desired value (case-
//     sensitive on values, case-insensitive on names) produce no Change entry.
func Diff(desired, current []Setting) ([]Change, error) {
	// Build a case-insensitive lookup of current settings.
	currentMap := make(map[string]string, len(current))
	for _, s := range current {
		currentMap[strings.ToLower(s.Name)] = s.Value
	}

	var changes []Change
	for _, d := range desired {
		key := strings.ToLower(d.Name)
		currentVal, exists := currentMap[key]
		if exists && currentVal == d.Value {
			// Already at desired value — no change needed.
			continue
		}
		changes = append(changes, Change{
			Setting: Setting{
				Name:  d.Name,
				Value: d.Value,
			},
			From: currentVal, // "" when setting is absent in current
			To:   d.Value,
		})
	}
	return changes, nil
}
