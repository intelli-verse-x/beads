// Package policy implements bd close-time policy gates.
//
// Motivation. Closing a bead is the single moment when downstream consumers
// (Refinery, Witness, Mayor, follow-up agents) trust that the issue is done
// AND that the deliverable is attached. Today nothing enforces the latter:
// in real Gas Town batches we have observed
//
//   - 17 backlinks beads closed, only 2 with playbook_path metadata, 2 with pr_url
//   - 78 cert beads in flight, only 3 with pr_url at close time
//
// i.e. the convention is documented but agents skip the metadata step
// silently. A coordinator that reads bd to plan next work then routes
// downstream off stale state.
//
// This package adds a fail-closed gate: when a bead matches one of the rules
// declared in `.beads/close-policy.yaml`, `bd close` refuses to close it
// unless the required metadata is present. `--force` bypasses the gate (with
// an audit trail) for break-the-glass scenarios.
//
// The policy file shape is intentionally minimal:
//
//	# .beads/close-policy.yaml
//	rules:
//	  - name: backlinks-need-playbook
//	    when:
//	      labels_any: [backlinks-2026-05]
//	    require_one_of: [playbook_path, pr_url]
//	  - name: cert-needs-pr
//	    when:
//	      labels_all: [cert-2026-05]
//	      metadata_keys: [repo]              # only when issue.metadata.repo is present
//	    require_one_of: [pr_url, no_repo_reason]
//
// All conditions in `when` are ANDed; if all conditions match the rule
// applies. `require_one_of` lists top-level keys in `issue.metadata` that
// must each evaluate to a non-null JSON value (string, number, object,
// array, true). Empty strings, `null`, and missing keys fail the check.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Policy is the parsed close-policy.yaml document.
type Policy struct {
	Rules []Rule `yaml:"rules"`
}

// Rule binds a match condition to a required-metadata assertion.
type Rule struct {
	Name         string    `yaml:"name"`
	When         Condition `yaml:"when"`
	RequireOneOf []string  `yaml:"require_one_of"`
	RequireAll   []string  `yaml:"require_all,omitempty"`
}

// Condition describes which issues a rule applies to. All fields are
// ANDed together; an empty Condition matches every issue.
type Condition struct {
	// LabelsAll: issue must carry every label in the list.
	LabelsAll []string `yaml:"labels_all,omitempty"`
	// LabelsAny: issue must carry at least one label in the list.
	LabelsAny []string `yaml:"labels_any,omitempty"`
	// ExcludeLabels: issue must not carry any label in the list.
	ExcludeLabels []string `yaml:"exclude_labels,omitempty"`
	// IssueTypes: issue.type must be in this list (e.g., "task", "epic").
	IssueTypes []string `yaml:"issue_types,omitempty"`
	// MetadataKeys: issue.metadata must contain every key in the list,
	// each resolving to a non-null JSON value. Useful to scope a rule to
	// "only beads that already declared a repo" etc.
	MetadataKeys []string `yaml:"metadata_keys,omitempty"`
}

// Issue is the subset of bead state the policy evaluator inspects. The bd
// close path passes a *types.Issue through a shim in evaluator_shim.go to
// keep this package free of beads-internal dependencies for testing.
type Issue struct {
	ID       string
	Type     string
	Labels   []string
	Metadata json.RawMessage
}

// PolicyFileName is the conventional path under .beads/.
const PolicyFileName = "close-policy.yaml"

// ErrNoPolicy means the policy file does not exist; callers should treat
// this as "no rules" and proceed without gating.
var ErrNoPolicy = errors.New("no close policy file")

// LoadFromBeadsDir reads the policy file from the given .beads directory.
// Returns (nil, ErrNoPolicy) when the file does not exist; an empty policy
// (no rules) returns a non-nil Policy with len(rules)==0.
func LoadFromBeadsDir(beadsDir string) (*Policy, error) {
	if beadsDir == "" {
		return nil, ErrNoPolicy
	}
	path := filepath.Join(beadsDir, PolicyFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoPolicy
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes a policy YAML document.
func Parse(data []byte) (*Policy, error) {
	if len(data) == 0 {
		return &Policy{}, nil
	}
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing close policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks rule shape. Returns a descriptive error pointing at the
// offending rule index so policy authors can fix typos without guessing.
func (p *Policy) Validate() error {
	if p == nil {
		return nil
	}
	for i, r := range p.Rules {
		if len(r.RequireOneOf) == 0 && len(r.RequireAll) == 0 {
			return fmt.Errorf("rule[%d] %q: must declare require_one_of or require_all", i, r.Name)
		}
	}
	return nil
}

// Violation describes why a close is blocked. The Reasons slice is suitable
// for emitting one human-readable line per failing rule.
type Violation struct {
	IssueID string
	Reasons []string
}

func (v *Violation) Error() string {
	if v == nil || len(v.Reasons) == 0 {
		return fmt.Sprintf("close-policy violation on %s", v.IssueID)
	}
	return fmt.Sprintf(
		"close-policy violation on %s:\n  - %s\n(use --force to override; the audit log records the bypass)",
		v.IssueID,
		strings.Join(v.Reasons, "\n  - "),
	)
}

// Evaluate runs every rule whose Condition matches `issue` and returns a
// Violation if any required-metadata assertion fails. Returns nil when the
// issue satisfies every applicable rule (or no rule applies).
func (p *Policy) Evaluate(issue Issue) *Violation {
	if p == nil || len(p.Rules) == 0 {
		return nil
	}
	meta := parseMetadata(issue.Metadata)
	var reasons []string
	for _, r := range p.Rules {
		if !ruleMatches(r.When, issue, meta) {
			continue
		}
		if !satisfiesRequireOneOf(r.RequireOneOf, meta) {
			reasons = append(reasons,
				fmt.Sprintf("rule %q: require one of [%s] in metadata", r.Name, strings.Join(r.RequireOneOf, ", ")))
		}
		if !satisfiesRequireAll(r.RequireAll, meta) {
			reasons = append(reasons,
				fmt.Sprintf("rule %q: require all of [%s] in metadata", r.Name, strings.Join(r.RequireAll, ", ")))
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	return &Violation{IssueID: issue.ID, Reasons: reasons}
}

func ruleMatches(c Condition, issue Issue, meta map[string]json.RawMessage) bool {
	labelSet := stringSet(issue.Labels)

	if len(c.LabelsAll) > 0 {
		for _, lbl := range c.LabelsAll {
			if _, ok := labelSet[lbl]; !ok {
				return false
			}
		}
	}
	if len(c.LabelsAny) > 0 {
		found := false
		for _, lbl := range c.LabelsAny {
			if _, ok := labelSet[lbl]; ok {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(c.ExcludeLabels) > 0 {
		for _, lbl := range c.ExcludeLabels {
			if _, ok := labelSet[lbl]; ok {
				return false
			}
		}
	}
	if len(c.IssueTypes) > 0 {
		match := false
		for _, t := range c.IssueTypes {
			if strings.EqualFold(t, issue.Type) {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	if len(c.MetadataKeys) > 0 {
		for _, k := range c.MetadataKeys {
			if !metadataKeyPresent(meta, k) {
				return false
			}
		}
	}
	return true
}

func satisfiesRequireOneOf(keys []string, meta map[string]json.RawMessage) bool {
	if len(keys) == 0 {
		return true
	}
	for _, k := range keys {
		if metadataKeyPresent(meta, k) {
			return true
		}
	}
	return false
}

func satisfiesRequireAll(keys []string, meta map[string]json.RawMessage) bool {
	if len(keys) == 0 {
		return true
	}
	for _, k := range keys {
		if !metadataKeyPresent(meta, k) {
			return false
		}
	}
	return true
}

// metadataKeyPresent returns true if meta has a top-level key with a
// non-null, non-empty-string value. Empty strings count as "not provided"
// because that is the failure mode we see in the wild: agents stamp
// `pr_url: ""` rather than dropping the key entirely.
func metadataKeyPresent(meta map[string]json.RawMessage, key string) bool {
	raw, ok := meta[key]
	if !ok {
		return false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	// Treat the JSON empty string ("" -> `""`) as missing.
	if trimmed == `""` {
		return false
	}
	return true
}

func parseMetadata(raw json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func stringSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}
