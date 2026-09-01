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
