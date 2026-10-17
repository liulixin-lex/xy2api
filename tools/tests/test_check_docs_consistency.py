import contextlib
import importlib.util
import io
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).resolve().parents[1] / "check_docs_consistency.py"
SPEC = importlib.util.spec_from_file_location("check_docs_consistency", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class DocsConsistencyTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        subprocess.run(["git", "init", "-q"], cwd=self.root, check=True)
        self.config = {}

    def tearDown(self):
        self._tmp.cleanup()

    def write(self, path, text, track=True):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")
        if track:
            subprocess.run(["git", "add", "-f", path], cwd=self.root, check=True)

    def problems(self, *checks):
        config = self.root / "rules.json"
        config.write_text(json.dumps(self.config), encoding="utf-8")
        return MODULE.run(self.root, config, list(checks) or None)

    def test_relative_links_must_reach_tracked_files(self):
        self.write("docs/A.md", "see [b](B.md), [dir](sub/), [up](../README.md#top), [web](https://example.com), [anchor](#x)\n")
        self.write("docs/B.md", "b\n")
        self.write("docs/sub/C.md", "c\n")
        self.write("README.md", "![logo](assets/logo.svg) <img src=\"assets/missing.svg\">\n")
        self.write("assets/logo.svg", "<svg/>\n")
        self.write("docs/D.md", "[gone](Gone.md)\n```\n[in code](Nope.md)\n```\nand `[inline](Nope.md)`\n")
        self.write("docs/Untracked.md", "u\n", track=False)
        self.write("docs/E.md", "[untracked](Untracked.md)\n")
        problems = self.problems("links")
        self.assertEqual(
            problems,
            [
                "[links] README.md:1: link `assets/missing.svg` does not resolve to a tracked file",
                "[links] docs/D.md:1: link `Gone.md` does not resolve to a tracked file",
                "[links] docs/E.md:1: link `Untracked.md` does not resolve to a tracked file",
            ],
        )

    def test_ignored_files_skip_the_link_check(self):
        self.write("frontend/README.md", "[x](/src/x.md)\n")
        self.config = {"links": {"ignore_files": {"frontend/README.md": "upstream"}}}
        self.assertEqual(self.problems("links"), [])

    def test_index_lists_every_doc_and_change(self):
        self.config = {"docs_index": {"path": "docs/README.md", "docs_root": "docs", "openspec_changes": "openspec/changes"}}
        self.write("docs/README.md", "[a](A.md) [legal](legal/) [c1](../openspec/changes/c1/)\n")
        self.write("docs/A.md", "a\n")
        self.write("docs/B.md", "b\n")
        self.write("docs/legal/x.md", "x\n")
        self.write("docs/shots/y.png", "y\n")
        self.write("openspec/changes/c1/tasks.md", "t\n")
        self.write("openspec/changes/c2/tasks.md", "t\n")
        self.assertEqual(
            self.problems("docs-index"),
            [
                "[docs-index] docs/README.md: does not list docs/B.md",
                "[docs-index] docs/README.md: does not list docs/shots",
                "[docs-index] docs/README.md: does not list openspec/changes/c2",
            ],
        )

    def test_tracked_docs_must_not_be_ignored(self):
        self.config = {"docs_whitelist": {"roots": ["docs"]}}
        self.write(".gitignore", "docs/*\n!docs/A.md\n")
        self.write("docs/A.md", "a\n")
        self.write("docs/B.md", "b\n")
        self.assertEqual(
            self.problems("docs-whitelist"),
            ["[docs-whitelist] docs/B.md: tracked but ignored by .gitignore; add a `!docs/B.md` whitelist entry"],
        )

    def test_pins_follow_their_source(self):
        self.config = {
            "pins": [
                {
                    "name": "Go",
                    "source": {"path": "go.mod", "pattern": "^go (\\d+\\.\\d+\\.\\d+)$"},
                    "copies": [
                        {"path": "ci.yml", "pattern": "go(\\d+\\.\\d+\\.\\d+)", "min_count": 2},
                        {"path": "Dockerfile", "pattern": "golang:(\\d+\\.\\d+\\.\\d+)"},
                    ],
                }
            ]
        }
        self.write("go.mod", "module x\n\ngo 1.27.2\n")
        self.write("ci.yml", "grep go1.27.2\n")
        self.write("Dockerfile", "FROM golang:1.27.0-alpine\n")
        self.assertEqual(
            self.problems("pins"),
            [
                "[pins] ci.yml: expected 2 Go pin(s) matching /go(\\d+\\.\\d+\\.\\d+)/, found 1",
                "[pins] Dockerfile: Go 1.27.0 differs from 1.27.2 in go.mod",
            ],
        )

    def test_contracts_require_every_name_in_the_document(self):
        self.config = {
            "contracts": [
                {
                    "item": "log event",
                    "sources": ["src/**/*.go"],
                    "exclude": ["*_test.go"],
                    "pattern": "\\.(?:Warn|Info)\\(\"resilience\\.([a-z_]+)\"",
                    "skip": "_failed$",
                    "doc": "docs/G.md",
                },
                {
                    "item": "setting",
                    "sources": ["src/settings.go"],
                    "pattern": "json:\"([a-z_]+)\"",
                    "match": "word",
                    "doc": "docs/D.md",
                },
            ]
        }
        self.write("src/a/x.go", 'slog.Warn("resilience.balance_paused")\nslog.Info("resilience.web_search_replay")\nslog.Warn("resilience.store_failed")\n')
        self.write("src/a/x_test.go", 'slog.Warn("resilience.only_in_tests")\n')
        self.write("src/settings.go", 'A int `json:"pause_seconds"`\nB int `json:"edges"`\n')
        self.write("docs/G.md", "- `resilience.balance_paused`\n- web_search_replay without code marks\n")
        self.write("docs/D.md", '{"pause_seconds": 300}\n')
        self.assertEqual(
            self.problems("contracts"),
            [
                "[contracts] docs/G.md: does not mention log event `web_search_replay`",
                "[contracts] docs/D.md: does not mention setting `edges`",
            ],
        )

    def test_retired_terms_only_where_history_is_kept(self):
        self.config = {
            "retired": [
                {"pattern": "old_event|old_key(?![a-z_])", "reason": "removed", "allowed": ["docs/HISTORY.md", "tests/*_test.go"]}
            ]
        }
        self.write("docs/HISTORY.md", "renamed old_event\n")
        self.write("tests/a_test.go", '"old_key"\n')
        self.write("src/b.go", "old_key_pause is a different name\n")
        self.write("docs/design.md", "ok\nlogs `resilience.old_event`\n")
        self.write("img.png", "old_event\n")
        self.assertEqual(
            self.problems("retired"),
            ["[retired] docs/design.md:2: `old_event` is retired (removed)"],
        )

    def test_private_host_details_stay_out(self):
        public_ip = (
            "(?<![\\d.])(?!(?:127|10|0)\\.|192\\.168\\.|192\\.0\\.2\\.)"
            "(?:(?:25[0-5]|2[0-4]\\d|1?\\d?\\d)\\.){3}(?:25[0-5]|2[0-4]\\d|1?\\d?\\d)(?![\\d.])"
        )
        site, service, root = (MODULE.digest(v) for v in ("secret-site.example", "old-svc", "wsb"))
        self.config = {
            "private": [
                {"tokens_sha256": [site, service], "reason": "private"},
                {"pattern": "(?<![A-Za-z0-9_.:/~-])/([A-Za-z0-9._-]+)/", "group_sha256": [root], "reason": "private"},
                {"pattern": public_ip, "paths": ["docs/**"], "reason": "address"},
            ]
        }
        self.write("docs/a.md", "证据在/wsb/artifacts/x，站点 https://test.secret-site.example/ 与 old-svc_default\n"
                   "https://host/wsb/ok、`<工作机 A>/artifacts`、/v1/responses、OLD-SVC、old-svcs\n")
        self.write("docs/b.md", "examples 192.0.2.10, 127.0.0.1:8080, 10.1.2.3, version 1.27.2\nreal 8.8.4.4\n")
        self.write("src/ip_test.go", "ip := \"8.8.4.4\"\n")
        hidden = "is a private host detail (private)"
        self.assertEqual(
            self.problems("private"),
            [
                f"[private] docs/a.md:1: `sha256 {site[:12]}` {hidden}",
                f"[private] docs/a.md:1: `sha256 {service[:12]}` {hidden}",
                f"[private] docs/a.md:2: `sha256 {service[:12]}` {hidden}",
                f"[private] docs/a.md:1: `sha256 {root[:12]}` {hidden}",
                "[private] docs/b.md:2: `8.8.4.4` is a private host detail (address)",
            ],
        )
        self.assertNotIn("secret-site", "\n".join(self.problems("private")))

    def test_token_pieces_are_whole_word_runs(self):
        self.assertEqual(
            list(MODULE.token_pieces("A.b-C_d", 1)),
            ["a", "a.b", "a.b-c", "a.b-c_d", "b", "b-c", "b-c_d", "c", "c_d", "d"],
        )

    def test_memory_size_and_log_structure(self):
        self.config = {"memory": {"path": "docs/M.md", "archive": "docs/ARCHIVE.md", "max_bytes": 200, "log_heading": "## 操作日志"}}
        self.write("docs/M.md", "# M\n\n## 操作日志\n\n### 2026-10-17 — a\n\n## stray heading\n\n```\n## in code\n```\n" + "x" * 200 + "\n")
        problems = self.problems("memory")
        self.assertEqual(len(problems), 2)
        self.assertIn("exceeds 0 KiB; move finished entries and older log entries to docs/ARCHIVE.md", problems[0])
        self.assertEqual(problems[1], "[memory] docs/M.md:7: `## stray heading` inside the operation log; log entries use `###`")

    def test_main_reports_ok(self):
        self.write("README.md", "ok\n")
        config = self.root / "rules.json"
        config.write_text("{}", encoding="utf-8")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(MODULE.main(["--root", str(self.root), "--config", str(config)]), 0)
        self.assertEqual(out.getvalue(), "documentation consistency: ok\n")


if __name__ == "__main__":
    unittest.main()
