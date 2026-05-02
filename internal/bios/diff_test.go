package bios

import (
	"testing"
)

func TestDiff(t *testing.T) {
	tests := []struct {
		name    string
		desired []Setting
		current []Setting
		want    []Change
	}{
		{
			name:    "empty desired produces no changes",
			desired: nil,
			current: []Setting{{Name: "HT", Value: "Enable"}},
			want:    nil,
		},
		{
			name:    "empty current includes all desired as new settings",
			desired: []Setting{{Name: "HT", Value: "Disable"}},
			current: nil,
			want: []Change{
				{Setting: Setting{Name: "HT", Value: "Disable"}, From: "", To: "Disable"},
			},
		},
		{
			name: "identical values produce no change",
			desired: []Setting{
				{Name: "Intel(R) Hyper-Threading Technology", Value: "Enable"},
			},
			current: []Setting{
				{Name: "Intel(R) Hyper-Threading Technology", Value: "Enable"},
			},
			want: nil,
		},
		{
			name: "different values produce change",
			desired: []Setting{
				{Name: "Intel(R) Hyper-Threading Technology", Value: "Disable"},
			},
			current: []Setting{
				{Name: "Intel(R) Hyper-Threading Technology", Value: "Enable"},
			},
			want: []Change{
				{
					Setting: Setting{Name: "Intel(R) Hyper-Threading Technology", Value: "Disable"},
					From:    "Enable",
					To:      "Disable",
				},
			},
		},
		{
			name: "case-insensitive name matching",
			desired: []Setting{
				{Name: "intel(r) hyper-threading technology", Value: "Disable"},
			},
			current: []Setting{
				{Name: "Intel(R) Hyper-Threading Technology", Value: "Enable"},
			},
			want: []Change{
				{
					Setting: Setting{Name: "intel(r) hyper-threading technology", Value: "Disable"},
					From:    "Enable",
					To:      "Disable",
				},
			},
		},
		{
			name: "setting in current but not desired is ignored (partial override)",
			desired: []Setting{
				{Name: "HT", Value: "Disable"},
			},
			current: []Setting{
				{Name: "HT", Value: "Enable"},
				{Name: "Turbo", Value: "Enable"},
			},
			want: []Change{
				{Setting: Setting{Name: "HT", Value: "Disable"}, From: "Enable", To: "Disable"},
			},
		},
		{
			name: "multiple settings mixed changes",
			desired: []Setting{
				{Name: "HT", Value: "Disable"},           // change
				{Name: "Power", Value: "OS Controls EPB"}, // same
				{Name: "NewSetting", Value: "On"},          // new
			},
			current: []Setting{
				{Name: "HT", Value: "Enable"},
				{Name: "Power", Value: "OS Controls EPB"},
				{Name: "OtherSetting", Value: "Off"},
			},
			want: []Change{
				{Setting: Setting{Name: "HT", Value: "Disable"}, From: "Enable", To: "Disable"},
				{Setting: Setting{Name: "NewSetting", Value: "On"}, From: "", To: "On"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Diff(tc.desired, tc.current)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len(changes) = %d, want %d\ngot:  %+v\nwant: %+v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				g, w := got[i], tc.want[i]
				if g.Name != w.Name || g.Value != w.Value || g.From != w.From || g.To != w.To {
					t.Errorf("change[%d]:\n  got  %+v\n  want %+v", i, g, w)
				}
			}
		})
	}
}
