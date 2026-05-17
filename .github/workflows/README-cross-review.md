# Cross-provider PR review

This directory ships a reusable adversarial review workflow that runs on
every non-draft PR and posts a verdict from a **different LLM provider**
than the one that wrote the PR.

## Why

Every reviewer in this stack — auditor, seo/geo/content/qa specialists,
Refinery, Mayor, Deacon, Witness — currently runs on Anthropic Claude. A
reviewer that runs on the same provider as the author cannot catch
"confidently wrong code that passes tests"; APEX-Agents (2026) measured
24 % first-pass success for single-provider agentic workflows. OpenAI's
`codex-plugin-cc` (April 2026) was the productisation of that observation.

This workflow is the minimum viable version of that idea, wired
end-to-end:

| Step | Actor |
|------|-------|
| PR opened by Claude polecat/crew | Anthropic |
| Refinery gate (existing) | Anthropic |
| `pr-cross-review.yml` (this workflow) | **OpenAI o-series via LiteLLM** |

The action does not block merge by itself; it posts a `REQUEST_CHANGES`
or `APPROVE` review that operators and the merge gate can react to.

## Configuration

| Name | Type | Default | Meaning |
|------|------|---------|---------|
| `LITELLM_API_KEY` | secret | _required_ | Bearer token for the LiteLLM gateway. |
| `LITELLM_BASE_URL` | var | `https://litellm.intelli-verse-x.ai` | LiteLLM endpoint. |
| `CROSS_REVIEW_MODEL` | var | `openai/o4-mini` | LiteLLM model alias to call. |

The recommended deployment is **org-level secrets** so every repo in
`intelli-verse-x` inherits the gateway credentials without re-pasting.

## Failure mode

If LiteLLM is unreachable or the response is unparseable, the workflow
posts a `COMMENT` review with the error message instead of failing the
PR. The Refinery and native CI remain the hard gates; this reviewer is
strictly advisory.

## Test

```
python3 -m unittest .github.workflows.pr_cross_review_test
```
