// Package alerts implements the alert rule engine for clustr (#133).
//
// Rules are defined as YAML files under /etc/clustr/rules.d/*.yml.
// The engine evaluates all loaded rules on a 60s tick against the node_stats
// time-series and routes firing/resolved transitions through the existing
// webhook dispatcher and SMTP notifier.
//
// Rule file format:
//
//	name: disk-percent
//	description: Disk usage above threshold
//	plugin: disks
//	sensor: used_pct
//	labels:
//	  mount: ".*"          # optional regex; omit to match all label combos
//	threshold:
//	  op: ">="             # >, >=, <, <=, ==, !=
//	  value: 90
//	duration: 300s         # must hold for this long before firing
//	severity: warn         # info | warn | critical
//	notify:
//	  webhook: true
//	  email: ["ops@example.com"]
package alerts

import (
	"fmt"
	"regexp"
	"time"
)

// Severity levels.
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityCritical = "critical"
)

// Op is a threshold comparison operator.
type Op string

const (
	OpGt  Op = ">"
	OpGte Op = ">="
	OpLt  Op = "<"
	OpLte Op = "<="
	OpEq  Op = "=="
	OpNeq Op = "!="
)

// validOps is the set of recognised operator strings.
var validOps = map[Op]struct{}{
	OpGt: {}, OpGte: {}, OpLt: {}, OpLte: {}, OpEq: {}, OpNeq: {},
}

// Threshold is the comparison applied to each sensor value.
type Threshold struct {
	Op    Op      `yaml:"op"`
	Value float64 `yaml:"value"`
}

// Notify controls which channels receive the alert notification.
type Notify struct {
	Webhook bool     `yaml:"webhook"`
	Email   []string `yaml:"email,omitempty"`
}

// Rule is a parsed alert rule loaded from a YAML file.
type Rule struct {
	// Name is the stable identifier used in alert state keying.  Must be unique
	// across all rule files; loaded files that collide on Name are skipped.
	Name string `yaml:"name"`

	// Description is a human-readable summary shown in notifications.
	Description string `yaml:"description,omitempty"`

	// Plugin is the stats plugin identifier, e.g. "disks", "infiniband".
	Plugin string `yaml:"plugin"`

	// Sensor is the specific measurement within the plugin, e.g. "used_pct".
	Sensor string `yaml:"sensor"`

	// Labels is an optional map of regex patterns to match against each sample's
	// labels.  An empty map (or nil) matches all samples.
	Labels map[string]string `yaml:"labels,omitempty"`

	// Threshold holds the comparison applied to each sample value.
	Threshold Threshold `yaml:"threshold"`

	// Duration is the minimum window over which the threshold must hold
	// continuously before the alert fires.
	Duration time.Duration `yaml:"duration"`

	// Severity classifies the alert: info, warn, or critical.
	Severity string `yaml:"severity"`

	// Notify configures the delivery channels for this rule.
	Notify Notify `yaml:"notify"`

	// compiledLabels holds the compiled regex for each Labels entry.
	// Populated by Validate().
	compiledLabels map[string]*regexp.Regexp

	// sourceFile is the path from which this rule was loaded (informational).
	sourceFile string
}

// Validate checks the rule for logical consistency and compiles label regexes.
// Returns a non-nil error for any misconfiguration.  Callers should skip rules
// that fail validation rather than crashing.
func (r *Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rule: name is required")
	}
	if r.Plugin == "" {
		return fmt.Errorf("rule %q: plugin is required", r.Name)
	}
	if r.Sensor == "" {
		return fmt.Errorf("rule %q: sensor is required", r.Name)
	}
	if _, ok := validOps[r.Threshold.Op]; !ok {
		return fmt.Errorf("rule %q: unknown threshold op %q (allowed: >, >=, <, <=, ==, !=)", r.Name, r.Threshold.Op)
	}
	switch r.Severity {
	case SeverityInfo, SeverityWarn, SeverityCritical:
	default:
		return fmt.Errorf("rule %q: unknown severity %q (allowed: info, warn, critical)", r.Name, r.Severity)
	}
	if r.Duration < 0 {
		return fmt.Errorf("rule %q: duration must be >= 0", r.Name)
	}

	// Compile label regexes.
	r.compiledLabels = make(map[string]*regexp.Regexp, len(r.Labels))
	for k, pattern := range r.Labels {
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return fmt.Errorf("rule %q: label %q: invalid regex %q: %w", r.Name, k, pattern, err)
		}
		r.compiledLabels[k] = re
	}
	return nil
}

// Evaluate returns true when value satisfies the threshold condition.
func (t Threshold) Evaluate(value float64) bool {
	switch t.Op {
	case OpGt:
		return value > t.Value
	case OpGte:
		return value >= t.Value
	case OpLt:
		return value < t.Value
	case OpLte:
		return value <= t.Value
	case OpEq:
		return value == t.Value
	case OpNeq:
		return value != t.Value
	}
	return false
}

// MatchesLabels returns true when labels satisfies the rule's label patterns.
// An empty compiledLabels map matches all label sets.
// Each pattern in compiledLabels must be satisfied by the corresponding key in
// the sample labels.  Extra keys in labels are ignored (subset match).
func (r *Rule) MatchesLabels(labels map[string]string) bool {
	for k, re := range r.compiledLabels {
		v, ok := labels[k]
		if !ok {
			// The rule requires this key; the sample doesn't have it.
			return false
		}
		if !re.MatchString(v) {
			return false
		}
	}
	return true
}
