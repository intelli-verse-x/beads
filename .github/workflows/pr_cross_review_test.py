"""Unit tests for the cross-provider review decision parser.

Run locally with:

    python3 -m unittest .github/workflows/pr_cross_review_test.py

Kept intentionally lightweight (stdlib only) so the test can run inside
GitHub Actions without a Python dependency install step.
"""
from __future__ import annotations

import importlib.util
import os
import sys
import tempfile
import unittest


def _load_module():
    here = os.path.dirname(os.path.abspath(__file__))
    spec = importlib.util.spec_from_file_location(
        "pr_cross_review", os.path.join(here, "pr_cross_review.py")
    )
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


pcr = _load_module()


class ExtractDecisionTests(unittest.TestCase):
    def test_approve_block(self):
        content = "Looks fine.\n\n```json\n{\"decision\": \"APPROVE\"}\n```\n"
        decision, summary = pcr._extract_decision(content)
        self.assertEqual(decision, "APPROVE")
        self.assertEqual(summary, "")

    def test_request_changes_with_summary(self):
        content = (
            "I see an off-by-one.\n\n"
            "```json\n"
            "{\"decision\": \"REQUEST_CHANGES\", \"summary\": \"off-by-one in loop bound\"}\n"
            "```\n"
        )
        decision, summary = pcr._extract_decision(content)
        self.assertEqual(decision, "REQUEST_CHANGES")
        self.assertEqual(summary, "off-by-one in loop bound")

    def test_unknown_decision_falls_back_to_comment(self):
        content = "```json\n{\"decision\": \"approve_with_nits\"}\n```"
        decision, summary = pcr._extract_decision(content)
        self.assertEqual(decision, "COMMENT")
        self.assertEqual(summary, "")

    def test_no_json_block_falls_back_to_comment(self):
        content = "Looks fine, no formal verdict."
        decision, _ = pcr._extract_decision(content)
        self.assertEqual(decision, "COMMENT")

    def test_invalid_json_falls_back_to_comment(self):
        content = "```json\n{not json}\n```"
        decision, _ = pcr._extract_decision(content)
        self.assertEqual(decision, "COMMENT")

    def test_uses_last_json_block(self):
        # Reviewer may emit several speculative blocks; we must take the last one
        # as the authoritative verdict.
        content = (
            "Draft thought:\n```json\n{\"decision\": \"APPROVE\"}\n```\n"
            "Actually:\n```json\n{\"decision\": \"REQUEST_CHANGES\"}\n```\n"
        )
        decision, _ = pcr._extract_decision(content)
        self.assertEqual(decision, "REQUEST_CHANGES")


class FormatReviewBodyTests(unittest.TestCase):
    def test_header_includes_model_and_decision(self):
        body = pcr._format_review_body("openai/o4-mini", "Body content.", "APPROVE", "")
        self.assertIn("openai/o4-mini", body)
        self.assertIn("APPROVE", body)
        self.assertIn("Body content.", body)

    def test_header_includes_summary_when_provided(self):
        body = pcr._format_review_body("openai/o4-mini", "x", "REQUEST_CHANGES", "off-by-one")
        self.assertIn("off-by-one", body)


class EmitFailureTests(unittest.TestCase):
    def test_emits_comment_review_with_message(self):
        with tempfile.NamedTemporaryFile(mode="w", delete=False, suffix=".json") as f:
            path = f.name
        try:
            rc = pcr._emit_failure(path, "LiteLLM HTTP 502: backend down")
            self.assertEqual(rc, 0)
            with open(path) as f:
                payload = f.read()
            self.assertIn("COMMENT", payload)
            self.assertIn("502", payload)
        finally:
            os.unlink(path)


if __name__ == "__main__":
    unittest.main()
