package alerts

import (
	"testing"
)

func TestRuleValidate(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{
			name: "valid minimal rule",
			rule: Rule{
				Name:   "disk-percent",
				Plugin: "disks",
				Sensor: "used_pct",
				Threshold: Threshold{Op: ">=", Value: 90},
				Severity: "warn",
			},
			wantErr: false,
		},
		{
			name:    "missing name",
			rule:    Rule{Plugin: "disks", Sensor: "used_pct", Threshold: Threshold{Op: ">=", Value: 90}, Severity: "warn"},
			wantErr: true,
		},
		{
			name:    "missing plugin",
			rule:    Rule{Name: "x", Sensor: "used_pct", Threshold: Threshold{Op: ">=", Value: 90}, Severity: "warn"},
			wantErr: true,
		},
		{
			name:    "missing sensor",
			rule:    Rule{Name: "x", Plugin: "disks", Threshold: Threshold{Op: ">=", Value: 90}, Severity: "warn"},
			wantErr: true,
		},
		{
			name:    "unknown op",
			rule:    Rule{Name: "x", Plugin: "disks", Sensor: "s", Threshold: Threshold{Op: "~=", Value: 90}, Severity: "warn"},
			wantErr: true,
		},
		{
			name:    "unknown severity",
			rule:    Rule{Name: "x", Plugin: "disks", Sensor: "s", Threshold: Threshold{Op: ">=", Value: 90}, Severity: "emergency"},
			wantErr: true,
		},
		{
			name: "invalid label regex",
			rule: Rule{
				Name:      "x",
				Plugin:    "disks",
				Sensor:    "s",
				Threshold: Threshold{Op: ">=", Value: 90},
				Severity:  "warn",
				Labels:    map[string]string{"mount": "[invalid("},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestThresholdEvaluate(t *testing.T) {
	cases := []struct {
		op    Op
		tv    float64
		v     float64
		want  bool
	}{
		{OpGt, 90, 91, true},
		{OpGt, 90, 90, false},
		{OpGte, 90, 90, true},
		{OpGte, 90, 89, false},
		{OpLt, 90, 89, true},
		{OpLt, 90, 90, false},
		{OpLte, 90, 90, true},
		{OpLte, 90, 91, false},
		{OpEq, 0, 0, true},
		{OpEq, 0, 1, false},
		{OpNeq, 0, 1, true},
		{OpNeq, 0, 0, false},
	}
	for _, c := range cases {
		th := Threshold{Op: c.op, Value: c.tv}
		got := th.Evaluate(c.v)
		if got != c.want {
			t.Errorf("Threshold{%s %v}.Evaluate(%v) = %v, want %v", c.op, c.tv, c.v, got, c.want)
		}
	}
}

func TestRuleMatchesLabels(t *testing.T) {
	r := &Rule{
		Name:   "disk-percent",
		Plugin: "disks",
		Sensor: "used_pct",
		Threshold: Threshold{Op: ">=", Value: 90},
		Severity: "warn",
		Labels: map[string]string{"mount": "/var/.*"},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if !r.MatchesLabels(map[string]string{"mount": "/var/lib/clustr"}) {
		t.Error("expected /var/lib/clustr to match /var/.*")
	}
	if r.MatchesLabels(map[string]string{"mount": "/boot"}) {
		t.Error("expected /boot NOT to match /var/.*")
	}
	if r.MatchesLabels(map[string]string{}) {
		t.Error("expected empty labels NOT to match (rule requires 'mount' key)")
	}

	// Rule with no labels should match everything.
	r2 := &Rule{
		Name:   "no-labels",
		Plugin: "disks",
		Sensor: "used_pct",
		Threshold: Threshold{Op: ">=", Value: 90},
		Severity: "warn",
	}
	if err := r2.Validate(); err != nil {
		t.Fatalf("Validate r2: %v", err)
	}
	if !r2.MatchesLabels(map[string]string{"mount": "/anything"}) {
		t.Error("rule with no labels should match all label sets")
	}
	if !r2.MatchesLabels(nil) {
		t.Error("rule with no labels should match nil labels")
	}
}
