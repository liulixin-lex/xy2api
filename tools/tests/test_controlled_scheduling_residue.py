import re
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]

# Controlled scheduling was removed in favour of share scheduling. Its tables
# stay in published migrations until the export-then-drop follow-up (D7), but
# no code may read them or reintroduce the old entry points again.
RESIDUE = re.compile(
    r"internal/scheduling\b|ControlledScheduling|controlledScheduling|Sub2APISchedulingEnabled|scheduling_system_mode"
)
SOURCE_SUFFIXES = {".go", ".ts", ".vue", ".sql", ".yml", ".yaml"}


def tracked_sources() -> list[str]:
    output = subprocess.run(
        ["git", "ls-files", "backend", "frontend/src", "deploy"],
        cwd=ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
    ).stdout
    return [
        path
        for path in output.splitlines()
        if Path(path).suffix in SOURCE_SUFFIXES and not path.startswith("backend/migrations/")
    ]


class ControlledSchedulingResidueTests(unittest.TestCase):
    def test_no_code_references_controlled_scheduling(self):
        hits = []
        for relative in tracked_sources():
            path = ROOT / relative
            if not path.is_file():
                continue
            for number, line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
                if RESIDUE.search(line):
                    hits.append(f"{relative}:{number}: {line.strip()}")
        self.assertEqual(hits, [])

    def test_controlled_package_stays_deleted(self):
        self.assertFalse((ROOT / "backend" / "internal" / "scheduling").exists())


if __name__ == "__main__":
    unittest.main()
