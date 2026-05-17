"""Cross-provider PR reviewer.

Invoked by .github/workflows/pr-cross-review.yml. Reads a PR diff and
metadata from disk, calls an OpenAI-compatible chat-completions endpoint
(LiteLLM gateway by default), and emits a JSON file with the review
decision and body. The shell step that follows in the workflow uses
`gh pr review` to post the result.

The shape of the prompt is intentionally narrow:
  - The system message instructs the reviewer to look for SEMANTIC bugs
    that a curl/grep/Lighthouse suite cannot catch (the failure mode
    APEX-Agents documented at 24% first-pass success).
  - The user message includes only PR title, the base/head branch names,
    and the truncated unified diff. No file reads, no test execution.
  - Decision is exactly one of: APPROVE | REQUEST_CHANGES | COMMENT.
    The reviewer is asked to emit a fenced JSON block we can parse
    deterministically — the rest of the body is for humans.

Failures fall back to a COMMENT review describing the error rather than
blocking the PR; the Refinery / native CI remain the hard gates.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request


SYSTEM_PROMPT = """\
You are a cross-provider adversarial code reviewer for an agentic dev stack
where the PR author is another LLM (Anthropic Claude). Your job is to find
semantic bugs the author and the deterministic test suite could miss:

- logic flips (early-return short-circuits, off-by-one, inverted conditionals)
- silent error swallowing
- contract drift between callers and callees
- security/cost regressions (auth bypass, unbounded loops, infinite retries)
- dropped guardrails (removed checks, removed audit calls, removed timeouts)
- mismatch between PR description and what the diff actually does

You DO NOT need to flag style, formatting, or test-coverage gaps unless they
indicate a real defect. Be terse; quote the line(s) you are objecting to.

End your review with exactly one fenced JSON block of the form:

```json
{"decision": "APPROVE"}
```

or

```json
{"decision": "REQUEST_CHANGES", "summary": "<one-line reason>"}
```

Use COMMENT (instead of APPROVE/REQUEST_CHANGES) only when the diff is so
small or so out-of-scope that an adversarial review is not meaningful.
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--diff", required=True)
    parser.add_argument("--title", required=True)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    base_url = os.environ.get("LITELLM_BASE_URL", "https://litellm.intelli-verse-x.ai").rstrip("/")
    api_key = os.environ.get("LITELLM_API_KEY", "")
    model = os.environ.get("CROSS_REVIEW_MODEL", "openai/o4-mini")

    try:
        with open(args.diff, "r", encoding="utf-8", errors="replace") as f:
            diff = f.read()
    except OSError as e:
        return _emit_failure(args.output, f"could not read diff file: {e}")

    user_prompt = (
        f"PR title: {args.title}\n"
        f"Base branch: {args.base}\n"
        f"Head branch: {args.head}\n"
        f"\n"
        f"Unified diff (truncated to 200 KB):\n"
        f"```diff\n{diff}\n```\n"
    )

    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": user_prompt},
        ],
        "temperature": 0.2,
    }

    if not api_key:
        return _emit_failure(args.output, "LITELLM_API_KEY is not set; skipping cross-review call")

    req = urllib.request.Request(
        f"{base_url}/v1/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace") if hasattr(e, "read") else ""
        return _emit_failure(args.output, f"LiteLLM HTTP {e.code}: {body[:500]}")
    except urllib.error.URLError as e:
        return _emit_failure(args.output, f"LiteLLM connection error: {e}")
    except Exception as e:  # noqa: BLE001 — anything else is also a "fail safe" path
        return _emit_failure(args.output, f"LiteLLM unexpected error: {e}")

    try:
        envelope = json.loads(raw)
        content = envelope["choices"][0]["message"]["content"]
    except (KeyError, IndexError, json.JSONDecodeError) as e:
        return _emit_failure(args.output, f"could not parse LiteLLM envelope: {e} — first 500 chars: {raw[:500]}")

    decision, summary = _extract_decision(content)
    body = _format_review_body(model, content, decision, summary)

    with open(args.output, "w", encoding="utf-8") as f:
        json.dump({"decision": decision, "body": body}, f)
    return 0


_FENCED_JSON = re.compile(r"```json\s*(\{.*?\})\s*```", re.DOTALL)


def _extract_decision(content: str) -> tuple[str, str]:
    """Pull the trailing ```json {...}``` block out of the reviewer's reply."""
    matches = _FENCED_JSON.findall(content)
    if not matches:
        return "COMMENT", ""
    try:
        parsed = json.loads(matches[-1])
    except json.JSONDecodeError:
        return "COMMENT", ""
    decision = str(parsed.get("decision", "")).strip().upper()
    summary = str(parsed.get("summary", "")).strip()
    if decision not in ("APPROVE", "REQUEST_CHANGES", "COMMENT"):
        decision = "COMMENT"
    return decision, summary


def _format_review_body(model: str, full: str, decision: str, summary: str) -> str:
    header = (
        f"### Cross-provider review (`{model}`)\n\n"
        f"**Decision:** `{decision}`"
    )
    if summary:
        header += f" — {summary}"
    return f"{header}\n\n---\n\n{full.strip()}\n"


def _emit_failure(output_path: str, message: str) -> int:
    body = (
        "### Cross-provider review — failure\n\n"
        f"Review could not be produced: {message}\n\n"
        "This is an adversarial reviewer only; the Refinery / native CI are still the hard gates."
    )
    with open(output_path, "w", encoding="utf-8") as f:
        json.dump({"decision": "COMMENT", "body": body}, f)
    # Exit 0 so the workflow still posts the failure comment.
    return 0


if __name__ == "__main__":
    sys.exit(main())
