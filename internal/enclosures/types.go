// Package enclosures defines the canned enclosure type catalog for Sprint 31 (#231).
//
// THREAD-SAFETY: Catalog is a package-level read-only map initialised once at
// program startup. It is never written after init, so no mutex is required.
// Any future code that writes to a map derived from Catalog must add its own
// synchronisation.
package enclosures

// EnclosureType describes a canned multi-node chassis model.
// SlotCount and HeightU are authoritative — they are never stored per-instance
// so an operator cannot create a 5-slot chassis of a 4-slot type.
type EnclosureType struct {
	// ID is the stable key used in the database type_id column.
	ID string

	// DisplayName is the human-readable label shown in the type picker.
	DisplayName string

	// HeightU is how many rack units the chassis occupies.
	HeightU int

	// SlotCount is the number of node slots the chassis provides.
	SlotCount int

	// Orientation is the layout hint for the UI renderer.
	// Allowed values: "horizontal", "vertical", "grid_2x2", "grid_2x4".
	Orientation string

	// Description is free text for the type picker tooltip.
	Description string
}

// Catalog is the authoritative set of canned enclosure types shipped with v0.11.0.
// Operator-defined types are out of scope for this release.
// To add a new type: add a key here + ship a point release. Zero DB migration required.
var Catalog = map[string]EnclosureType{
	"halfwidth-1u-2slot": {
		ID:          "halfwidth-1u-2slot",
		DisplayName: "1U Half-Width (2 slots)",
		HeightU:     1,
		SlotCount:   2,
		Orientation: "horizontal",
		Description: "Half-width 1U chassis with 2 side-by-side node slots. Typical: Supermicro 1U half-width sleds.",
	},
	"twin-2u-2slot": {
		ID:          "twin-2u-2slot",
		DisplayName: "2U Twin (2 slots)",
		HeightU:     2,
		SlotCount:   2,
		Orientation: "horizontal",
		Description: "2U Twin chassis with 2 horizontal node slots. Typical: Supermicro TwinPro.",
	},
	"blade-2u-4slot": {
		ID:          "blade-2u-4slot",
		DisplayName: "2U Blade Chassis (4 slots)",
		HeightU:     2,
		SlotCount:   4,
		Orientation: "horizontal",
		Description: "2U blade chassis with 4 horizontal blade slots. Typical: HPE Synergy half-height blades, Dell FX2 quarter-width sleds.",
	},
	"quad-4u-4slot": {
		ID:          "quad-4u-4slot",
		DisplayName: "4U Quad Chassis (4 slots)",
		HeightU:     4,
		SlotCount:   4,
		Orientation: "grid_2x2",
		Description: "4U quad chassis with a 2x2 grid of node slots. Typical: 4U GPU shelf or compute-sled chassis.",
	},
}

// Get returns the EnclosureType for the given typeID and whether it was found.
func Get(typeID string) (EnclosureType, bool) {
	t, ok := Catalog[typeID]
	return t, ok
}
