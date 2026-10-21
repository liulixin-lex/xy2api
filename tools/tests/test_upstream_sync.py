import hashlib
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "tools" / "upstream-sync" / "sync.py"
SPEC = importlib.util.spec_from_file_location("xy2api_upstream_sync", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"cannot load {MODULE_PATH}")
SYNC = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = SYNC
SPEC.loader.exec_module(SYNC)


class UpstreamSyncTests(unittest.TestCase):
    def test_load_json_rejects_duplicate_keys(self):
        # A duplicated key silently drops the first value: a required
        # literal written twice in one object was never checked.
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "policy.json"
            path.write_text('{"items": [\n  {"path": "a", "value": "x",\n   "path": "b", "value": "y"}\n]}\n', encoding="utf-8")
            with self.assertRaises(SYNC.SyncError) as raised:
                SYNC.load_json(path)
            self.assertIn('duplicate key "path"', str(raised.exception))
            self.assertIn("line 2", str(raised.exception))
            path.write_text('{"items": [{"path": "a", "value": "x"}, {"path": "b", "value": "y"}]}', encoding="utf-8")
            self.assertEqual(SYNC.load_json(path)["items"][1]["path"], "b")

    def test_policy_has_no_duplicate_keys(self):
        policy = SYNC.load_policy()
        literals = {(item["path"], item["value"]) for item in policy["required_literals"]}
        self.assertIn(("frontend/src/components/common/GroupSelector.vue", 'data-test="group-exclusive-tag"'), literals)

    def test_module_normalization_is_byte_exact(self):
        source = b"github.com/Wei-Shaw/sub2api"
        target = b"github.com/liulixin-lex/xy2api"
        raw = b"import \"github.com/Wei-Shaw/sub2api/internal/config\"\r\n\x00"

        normalized = SYNC.normalize_module_bytes(raw, source, target)

        self.assertEqual(
            normalized,
            b"import \"github.com/liulixin-lex/xy2api/internal/config\"\r\n\x00",
        )

    def test_ownership_patterns_cover_nested_generated_files(self):
        self.assertTrue(SYNC.matches_any("backend/ent/user.go", ["backend/ent/**"]))
        self.assertFalse(SYNC.matches_any("backend/internal/service/user.go", ["backend/ent/**"]))

    def test_ownership_priority_keeps_compat_and_manual_paths_specific(self):
        policy = {
            "ownership_priority": ["compat_invariant", "manual_merge", "xy_owned"],
            "categories": {
                "compat_invariant": ["backend/pkg/pluginapi/**"],
                "manual_merge": ["backend/internal/config/config.go"],
                "xy_owned": ["backend/**"],
            },
        }

        self.assertEqual(
            SYNC.classify_owner("backend/pkg/pluginapi/v1/runtime.go", policy),
            "compat_invariant",
        )
        self.assertEqual(
            SYNC.classify_owner("backend/internal/config/config.go", policy),
            "manual_merge",
        )
        self.assertEqual(
            SYNC.classify_owner("backend/internal/service/user.go", policy),
            "xy_owned",
        )

    def test_v020_conflicts_are_explicit_manual_merges(self):
        manual = SYNC.load_policy()["categories"]["manual_merge"]
        for path in [
            "README_JA.md",
            "backend/cmd/server/VERSION",
            "backend/internal/service/bedrock_request.go",
            "backend/internal/service/gateway_request.go",
            "backend/internal/service/openai_oauth_passthrough_test.go",
        ]:
            with self.subTest(path=path):
                self.assertTrue(SYNC.matches_any(path, manual))

    def test_v021_conflicts_are_explicit_manual_merges(self):
        manual = SYNC.load_policy()["categories"]["manual_merge"]
        for path in [
            "backend/internal/handler/gemini_v1beta_handler_test.go",
            "backend/internal/pkg/claude/constants.go",
            "backend/internal/service/identity_service.go",
            "backend/internal/service/openai_ws_fallback_test.go",
            "backend/internal/service/ops_upstream_context.go",
        ]:
            with self.subTest(path=path):
                self.assertTrue(SYNC.matches_any(path, manual))

    def test_v023_conflicts_are_explicit_manual_merges(self):
        manual = SYNC.load_policy()["categories"]["manual_merge"]
        for path in [
            "backend/cmd/server/VERSION",
            "backend/ent/group.go",
            "backend/ent/runtime/runtime.go",
            "backend/internal/handler/admin/account_handler.go",
            "backend/internal/handler/admin/grok_import_probe.go",
            "backend/internal/handler/admin/group_handler.go",
            "backend/internal/handler/gateway_models_test.go",
            "backend/internal/handler/gemini_v1beta_handler_test.go",
            "backend/internal/handler/openai_gateway_handler.go",
            "backend/internal/repository/group_repo_integration_test.go",
            "backend/internal/server/routes/composite_platform_test.go",
            "backend/internal/server/routes/gateway.go",
            "backend/internal/service/admin_account.go",
            "backend/internal/service/admin_group.go",
            "backend/internal/service/admin_service.go",
            "backend/internal/service/admin_service_duplicate_account_test.go",
            "backend/internal/service/admin_service_group_test.go",
            "backend/internal/service/api_key_auth_cache_impl.go",
            "backend/internal/service/openai_gateway_ollama_cloud_max_tokens_test.go",
            "frontend/src/components/account/EditAccountModal.vue",
            "frontend/src/views/admin/GroupsView.vue",
        ]:
            with self.subTest(path=path):
                self.assertTrue(SYNC.matches_any(path, manual))

    def test_v024_conflicts_are_explicit_manual_merges(self):
        manual = SYNC.load_policy()["categories"]["manual_merge"]
        for path in [
            "README.md",
            "README_CN.md",
            "README_JA.md",
            "backend/cmd/server/VERSION",
            "backend/internal/service/ops_settings.go",
            "backend/internal/service/plugin_package.go",
            "deploy/tests/apple-container-test.sh",
        ]:
            with self.subTest(path=path):
                self.assertTrue(SYNC.matches_any(path, manual))

    def test_v024_sponsor_assets_remain_excluded(self):
        absent = SYNC.load_policy()["required_absent_paths"]
        self.assertIn("assets/partners/logos/cola-proxy.jpg", absent)
        self.assertIn("assets/partners/logos/ppdog.png", absent)

    def test_v025_conflicts_are_explicit_manual_merges(self):
        manual = SYNC.load_policy()["categories"]["manual_merge"]
        for path in [
            "README.md",
            "README_CN.md",
            "README_JA.md",
            "backend/cmd/server/VERSION",
            "backend/cmd/server/wire_gen.go",
            "backend/internal/repository/user_platform_quota_repo.go",
            "backend/internal/repository/user_platform_quota_repo_integration_test.go",
            "backend/internal/repository/user_platform_quota_upsert_test.go",
            "backend/internal/service/openai_gateway_request_body.go",
            "backend/internal/service/openai_responses_ingress_compat.go",
            "backend/internal/service/openai_ws_session_preemption_test.go",
            "backend/internal/setup/setup_test.go",
            "deploy/APPLE_CONTAINER.md",
            "deploy/apple-container.sh",
            "deploy/tests/apple-container-test.sh",
            "frontend/src/views/__tests__/KeyUsageView.spec.ts",
        ]:
            with self.subTest(path=path):
                self.assertTrue(SYNC.matches_any(path, manual))

    def test_migration_checksum_uses_trim_space_rule(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "001.sql"
            path.write_bytes(b"  SELECT 1;\r\n")

            self.assertEqual(
                SYNC.migration_checksum(path),
                hashlib.sha256(b"SELECT 1;").hexdigest(),
            )

    def test_scheduler_hunks_cancel_import_moves_only(self):
        upstream = b"""import (
\t"context"
\t"github.com/gin-gonic/gin"
\t"github.com/liulixin-lex/xy2api/internal/config"
)

func pick() {
\tcheckA()
\tcheckB()
}
"""
        current = b"""import (
\t"context"
\t"github.com/liulixin-lex/xy2api/internal/config"
\t"github.com/gin-gonic/gin"
)

func pick() {
\tcheckB()
\tcheckA()

\thook()
}
"""
        hunks = SYNC.scheduler_hunks(upstream, current)
        removed = [line for hunk in hunks for line in hunk["removed"]]
        added = [line for hunk in hunks for line in hunk["added"]]

        self.assertNotIn('"github.com/gin-gonic/gin"', removed + added)
        self.assertEqual(len(removed), 1, "a reordered statement still counts")
        self.assertIn(removed[0], ("checkA()", "checkB()"))
        self.assertEqual(sorted(added), sorted([removed[0], "hook()"]))

    def test_scheduler_diff_allowlist_matches_the_fork(self):
        errors: list[str] = []
        SYNC.audit_scheduler_diff(SYNC.load_policy(), errors)
        self.assertEqual(errors, [])

    def test_scheduler_diff_allowlist_tracks_the_recorded_upstream(self):
        allowlist = SYNC.load_json(SYNC.SCHEDULER_DIFF_PATH)
        provenance = SYNC.load_json(SYNC.PROVENANCE_PATH)
        self.assertEqual(allowlist["upstream_commit"], provenance["upstream_commit"])
        self.assertEqual(
            sorted(allowlist["files"]),
            sorted(
                f"backend/internal/service/{name}"
                for name in (
                    "gateway_scheduling.go",
                    "openai_account_scheduler.go",
                    "openai_gateway_scheduling.go",
                    "gemini_messages_compat_service.go",
                )
            ),
        )
        for path, blocks in allowlist["files"].items():
            for block in blocks:
                with self.subTest(path=path, block=block.get("block")):
                    self.assertTrue(block.get("block") and block.get("note"))
                    self.assertNotEqual(block["block"], "unreviewed")


if __name__ == "__main__":
    unittest.main()
