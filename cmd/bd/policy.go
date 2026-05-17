// Package main: bd policy — view the close-policy configuration.
//
// Surfaces the rules in .beads/close-policy.yaml so operators can verify a
// policy is loaded before relying on it. `bd policy check <id>` does a
// dry-run of the gate against an existing bead so authors can iterate on
// rules without forcing a real close attempt.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/policy"
	"github.com/steveyegge/beads/internal/ui"
	"gopkg.in/yaml.v3"
)

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Inspect bd close-policy configuration",
	Long: `Inspect the close-policy.yaml that gates 'bd close' for the active store.

Policy lives in .beads/close-policy.yaml (next to config.yaml). When a rule
matches an issue's labels / type / metadata, 'bd close' refuses to close it
unless the rule's required metadata keys are present. --force on close
bypasses the gate and writes a 'policy_bypass' row to the audit log.

See: internal/policy/close_policy.go for the rule shape.`,
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var policyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the parsed close-policy",
	Long: `Print the close-policy rules currently loaded from .beads/close-policy.yaml.

Useful for verifying that a policy file is being picked up by the active
store. Use --json for machine-readable output.`,
	Run: runPolicyShow,
}

var policyCheckCmd = &cobra.Command{
	Use:   "check <issue-id>",
	Short: "Dry-run the close-policy gate against an existing issue",
	Long: `Evaluate the close-policy against an existing bead without closing it.

Exits non-zero if the gate would block 'bd close <id>'. Useful for CI checks
or for authors iterating on policy rules — surface a violation before the
agent runs into it.`,
	Args: cobra.ExactArgs(1),
	Run:  runPolicyCheck,
}

var (
	policyShowJSON bool
)

func init() {
	rootCmd.AddCommand(policyCmd)
	policyCmd.AddCommand(policyShowCmd)
	policyCmd.AddCommand(policyCheckCmd)
	policyShowCmd.Flags().BoolVar(&policyShowJSON, "json", false, "Emit machine-readable JSON")
}

func runPolicyShow(cmd *cobra.Command, args []string) {
	beadsDir := resolveCommandBeadsDir(dbPath)
	if beadsDir == "" {
		fmt.Fprintln(os.Stderr, "no active .beads directory; run 'bd init' or 'bd where' to diagnose")
		os.Exit(1)
	}
	p, err := policy.LoadFromBeadsDir(beadsDir)
	if err != nil {
		if errors.Is(err, policy.ErrNoPolicy) {
			fmt.Printf("%s no close-policy.yaml in %s\n", ui.RenderMuted("·"), beadsDir)
			return
		}
		fmt.Fprintf(os.Stderr, "loading policy: %v\n", err)
		os.Exit(1)
	}

	if policyShowJSON {
		data, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("%s loaded %d rule(s) from %s\n", ui.RenderPass("✓"), len(p.Rules), beadsDir)
	for _, r := range p.Rules {
		fmt.Printf("\n%s %s\n", ui.RenderAccent("●"), r.Name)
		printRuleCondition(r.When)
		if len(r.RequireOneOf) > 0 {
			fmt.Printf("    require one of: %s\n", strings.Join(r.RequireOneOf, ", "))
		}
		if len(r.RequireAll) > 0 {
			fmt.Printf("    require all of: %s\n", strings.Join(r.RequireAll, ", "))
		}
	}
}

func printRuleCondition(c policy.Condition) {
	if len(c.LabelsAll) > 0 {
		fmt.Printf("    when labels_all: %s\n", strings.Join(c.LabelsAll, ", "))
	}
	if len(c.LabelsAny) > 0 {
		fmt.Printf("    when labels_any: %s\n", strings.Join(c.LabelsAny, ", "))
	}
	if len(c.ExcludeLabels) > 0 {
		fmt.Printf("    excluding labels: %s\n", strings.Join(c.ExcludeLabels, ", "))
	}
	if len(c.IssueTypes) > 0 {
		fmt.Printf("    when issue_types: %s\n", strings.Join(c.IssueTypes, ", "))
	}
	if len(c.MetadataKeys) > 0 {
		fmt.Printf("    when metadata_keys present: %s\n", strings.Join(c.MetadataKeys, ", "))
	}
}

func runPolicyCheck(cmd *cobra.Command, args []string) {
	id := args[0]
	beadsDir := resolveCommandBeadsDir(dbPath)
	if beadsDir == "" {
		fmt.Fprintln(os.Stderr, "no active .beads directory")
		os.Exit(1)
	}
	p, err := policy.LoadFromBeadsDir(beadsDir)
	if err != nil {
		if errors.Is(err, policy.ErrNoPolicy) {
			fmt.Printf("%s no close-policy.yaml in %s — close would not be gated\n", ui.RenderMuted("·"), beadsDir)
			return
		}
		fmt.Fprintf(os.Stderr, "loading policy: %v\n", err)
		os.Exit(1)
	}
	issue, err := store.GetIssue(rootCtx, id)
	if err != nil || issue == nil {
		fmt.Fprintf(os.Stderr, "issue %s not found\n", id)
		os.Exit(1)
	}
	pi := policy.Issue{
		ID:       issue.ID,
		Type:     string(issue.IssueType),
		Labels:   append([]string(nil), issue.Labels...),
		Metadata: issue.Metadata,
	}
	v := p.Evaluate(pi)
	if v == nil {
		fmt.Printf("%s %s: would pass close-policy\n", ui.RenderPass("✓"), id)
		return
	}
	fmt.Printf("%s %s: %s\n", ui.RenderFail("✖"), id, v.Error())
	os.Exit(1)
}

// _ keeps yaml.v3 imported even when policy.Parse is the only consumer,
// so vet does not complain after future edits remove the direct yaml.go
// import from this file.
var _ = yaml.Marshal
