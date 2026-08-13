package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	p, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if len(p.Rules) != 0 {
		t.Errorf("empty document should yield zero rules, got %d", len(p.Rules))
	}
}

func TestParse_InvalidRule(t *testing.T) {
	doc := `
rules:
  - name: bad-rule
    when:
      labels_any: [foo]
`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("rule without require_one_of/require_all must fail Validate()")
	}
}

func TestLoadFromBeadsDir_Missing(t *testing.T) {
	tmp := t.TempDir()
	_, err := LoadFromBeadsDir(tmp)
	if !errors.Is(err, ErrNoPolicy) {
		t.Fatalf("missing policy should return ErrNoPolicy, got %v", err)
	}
}

func TestLoadFromBeadsDir_Present(t *testing.T) {
	tmp := t.TempDir()
	doc := `
rules:
  - name: backlinks-need-playbook
    when:
      labels_any: [backlinks-2026-05]
    require_one_of: [playbook_path, pr_url]
`
	if err := os.WriteFile(filepath.Join(tmp, PolicyFileName), []byte(doc), 0644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	p, err := LoadFromBeadsDir(tmp)
	if err != nil {
		t.Fatalf("LoadFromBeadsDir: %v", err)
	}
	if len(p.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(p.Rules))
	}
	if p.Rules[0].Name != "backlinks-need-playbook" {
		t.Errorf("rule name mismatch: %q", p.Rules[0].Name)
	}
}

func backlinksPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := Parse([]byte(`
rules:
  - name: backlinks-need-playbook
    when:
      labels_any: [backlinks-2026-05]
    require_one_of: [playbook_path, pr_url]
  - name: cert-needs-pr
    when:
      labels_all: [cert-2026-05]
      metadata_keys: [repo]
    require_one_of: [pr_url, no_repo_reason]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestEvaluate_BacklinksMissingMetadata(t *testing.T) {
	p := backlinksPolicy(t)
	issue := Issue{
		ID:     "hq-9t2.1",
		Type:   "task",
		Labels: []string{"backlinks-2026-05"},
	}
	v := p.Evaluate(issue)
	if v == nil {
		t.Fatal("missing playbook_path / pr_url should violate the rule")
	}
	if v.IssueID != "hq-9t2.1" {
		t.Errorf("violation IssueID: %s", v.IssueID)
	}
	if len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "backlinks-need-playbook") {
		t.Errorf("expected reason naming the rule; got %v", v.Reasons)
	}
}

func TestEvaluate_BacklinksWithPlaybookPath(t *testing.T) {
	p := backlinksPolicy(t)
	meta, _ := json.Marshal(map[string]string{"playbook_path": "/gt/audits/.../foo.md"})
	issue := Issue{
		ID:       "hq-9t2.2",
		Labels:   []string{"backlinks-2026-05"},
		Metadata: meta,
	}
	if v := p.Evaluate(issue); v != nil {
		t.Fatalf("playbook_path should satisfy the rule; violation: %v", v)
	}
}

func TestEvaluate_EmptyStringTreatedAsMissing(t *testing.T) {
	p := backlinksPolicy(t)
	meta, _ := json.Marshal(map[string]string{"pr_url": ""})
	issue := Issue{
		ID:       "hq-9t2.3",
		Labels:   []string{"backlinks-2026-05"},
		Metadata: meta,
	}
	v := p.Evaluate(issue)
	if v == nil {
		t.Fatal("empty-string metadata value should not satisfy the rule")
	}
}

func TestEvaluate_CertNeedsPRWhenRepoPresent(t *testing.T) {
	p := backlinksPolicy(t)
	withRepo, _ := json.Marshal(map[string]string{"repo": "intelli-verse-x/foo"})
	issue := Issue{
		ID:       "hq-dvs.1",
		Type:     "task",
		Labels:   []string{"cert-2026-05", "seo"},
		Metadata: withRepo,
	}
	v := p.Evaluate(issue)
	if v == nil {
		t.Fatal("cert bead with repo but no pr_url must violate")
	}
}

func TestEvaluate_CertSkippedWhenRepoMissing(t *testing.T) {
	p := backlinksPolicy(t)
	// No metadata at all -> rule condition (metadata_keys: [repo]) does not match -> rule is skipped.
	issue := Issue{
		ID:     "hq-dvs.2",
		Type:   "task",
		Labels: []string{"cert-2026-05", "seo"},
	}
	if v := p.Evaluate(issue); v != nil {
		t.Fatalf("rule should not apply when metadata.repo is absent; got violation %v", v)
	}
}

func TestEvaluate_NoRulesNoViolation(t *testing.T) {
	p := &Policy{}
	if v := p.Evaluate(Issue{ID: "x"}); v != nil {
		t.Fatalf("empty policy should never block close; got %v", v)
	}
}

func TestEvaluate_ExcludeLabel(t *testing.T) {
	p, err := Parse([]byte(`
rules:
  - name: skip-experimental
    when:
      labels_any: [backlinks-2026-05]
      exclude_labels: [experimental]
    require_one_of: [playbook_path]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	issue := Issue{
		ID:     "hq-9t2.4",
		Labels: []string{"backlinks-2026-05", "experimental"},
	}
	if v := p.Evaluate(issue); v != nil {
		t.Fatalf("exclude_labels should skip the rule; got %v", v)
	}
}

func TestEvaluate_RequireAll(t *testing.T) {
	p, err := Parse([]byte(`
rules:
  - name: both
    when: { labels_any: [cert] }
    require_all: [pr_url, qa_report]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	meta, _ := json.Marshal(map[string]string{"pr_url": "https://example/1"})
	issue := Issue{ID: "x", Labels: []string{"cert"}, Metadata: meta}
	if v := p.Evaluate(issue); v == nil {
		t.Fatal("require_all should fail when only one key present")
	}
	full, _ := json.Marshal(map[string]string{"pr_url": "https://example/1", "qa_report": "/q/r"})
	issue.Metadata = full
	if v := p.Evaluate(issue); v != nil {
		t.Fatalf("require_all should pass with both keys; got %v", v)
	}
}

func TestEvaluate_IssueTypeMatch(t *testing.T) {
	p, err := Parse([]byte(`
rules:
  - name: tasks-only
    when:
      issue_types: [task]
    require_one_of: [pr_url]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v := p.Evaluate(Issue{ID: "e", Type: "epic"}); v != nil {
		t.Fatalf("epic should be skipped by issue_types filter; got %v", v)
	}
	if v := p.Evaluate(Issue{ID: "t", Type: "task"}); v == nil {
		t.Fatal("task should require pr_url")
	}
}
