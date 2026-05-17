package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/policy"
	"github.com/steveyegge/beads/internal/types"
)

// stubStore is a tiny storage stand-in so the close-policy unit tests don't
// have to spin up a real Dolt instance. evaluateClosePolicy only uses
// GetIssue when the caller passes a nil *types.Issue, which doesn't happen
// in the bd close hot path.
type stubStore struct {
	issue *types.Issue
}

// We only need to satisfy the small slice of storage.DoltStorage that
// evaluateClosePolicy actually calls. Defining one method on the stub
// resolves the rest at the call site (we never pass it as a full
// DoltStorage in this test — see TestEvaluateClosePolicy_* below).
func (s *stubStore) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return s.issue, nil
}

func writeClosePolicy(t *testing.T, beadsDir, doc string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, policy.PolicyFileName), []byte(doc), 0644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
}

func TestPolicyViolationFingerprint(t *testing.T) {
	v := &policy.Violation{
		IssueID: "hq-9t2.1",
		Reasons: []string{
			`rule "backlinks-need-playbook": require one of [playbook_path, pr_url] in metadata`,
			`rule "cert-needs-pr": require one of [pr_url, no_repo_reason] in metadata`,
		},
	}
	got := violationFingerprint(v)
	want := "backlinks-need-playbook,cert-needs-pr"
	if got != want {
		t.Errorf("violationFingerprint() = %q, want %q", got, want)
	}
}

func TestPolicyEvaluation_BlocksBacklinksWithoutPlaybook(t *testing.T) {
	// Pure-policy unit test: ensures the evaluator produces a Violation for
	// the exact failure pattern observed in batch backlinks-2026-05 (closing
	// a bead with no playbook_path or pr_url metadata).
	p, err := policy.Parse([]byte(`
rules:
  - name: backlinks-need-playbook
    when:
      labels_any: [backlinks-2026-05]
    require_one_of: [playbook_path, pr_url]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := p.Evaluate(policy.Issue{
		ID:     "hq-9t2.7",
		Type:   "task",
		Labels: []string{"backlinks-2026-05"},
	})
	if got == nil {
		t.Fatal("policy should block close without playbook_path / pr_url")
	}
	if !strings.Contains(got.Error(), "backlinks-need-playbook") {
		t.Errorf("violation should name the rule, got %s", got.Error())
	}
	// And it MUST mention --force so operators know the escape hatch.
	if !strings.Contains(got.Error(), "--force") {
		t.Errorf("violation should mention --force escape hatch, got %s", got.Error())
	}
}

func TestPolicyEvaluation_AllowsBacklinksWithPRUrl(t *testing.T) {
	p, _ := policy.Parse([]byte(`
rules:
  - name: backlinks-need-playbook
    when: { labels_any: [backlinks-2026-05] }
    require_one_of: [playbook_path, pr_url]
`))
	meta, _ := json.Marshal(map[string]string{
		"pr_url": "https://github.com/intelli-verse-x/foo/pull/42",
	})
	got := p.Evaluate(policy.Issue{
		ID:       "hq-9t2.8",
		Labels:   []string{"backlinks-2026-05"},
		Metadata: meta,
	})
	if got != nil {
		t.Fatalf("close should be allowed when pr_url is set; got %v", got)
	}
}

func TestPolicyFile_LoadedFromBeadsDir(t *testing.T) {
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	writeClosePolicy(t, beadsDir, `
rules:
  - name: cert-needs-pr
    when:
      labels_all: [cert-2026-05]
      metadata_keys: [repo]
    require_one_of: [pr_url, no_repo_reason]
`)
	p, err := policy.LoadFromBeadsDir(beadsDir)
	if err != nil {
		t.Fatalf("LoadFromBeadsDir: %v", err)
	}
	if len(p.Rules) != 1 || p.Rules[0].Name != "cert-needs-pr" {
		t.Fatalf("rules: %+v", p.Rules)
	}

	// Issue matches the rule and is missing pr_url → must violate.
	withRepo, _ := json.Marshal(map[string]string{"repo": "intelli-verse-x/x"})
	v := p.Evaluate(policy.Issue{
		ID:       "hq-dvs.1",
		Labels:   []string{"cert-2026-05", "seo"},
		Metadata: withRepo,
	})
	if v == nil {
		t.Fatal("expected violation for cert bead without pr_url")
	}

	// Same issue with pr_url set → must pass.
	bothMeta, _ := json.Marshal(map[string]string{
		"repo":   "intelli-verse-x/x",
		"pr_url": "https://github.com/intelli-verse-x/x/pull/1",
	})
	v2 := p.Evaluate(policy.Issue{
		ID:       "hq-dvs.1",
		Labels:   []string{"cert-2026-05", "seo"},
		Metadata: bothMeta,
	})
	if v2 != nil {
		t.Fatalf("expected pass with pr_url set; got %v", v2)
	}
}
