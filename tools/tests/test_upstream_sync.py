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

    def test_migration_checksum_uses_trim_space_rule(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "001.sql"
            path.write_bytes(b"  SELECT 1;\r\n")

            self.assertEqual(
                SYNC.migration_checksum(path),
                hashlib.sha256(b"SELECT 1;").hexdigest(),
            )


if __name__ == "__main__":
    unittest.main()
