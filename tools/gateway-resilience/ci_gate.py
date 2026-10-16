#!/usr/bin/env python3
"""CI gate for the gateway resilience tests (tasks 13.1).

Collects every Test function of the resilience test files, runs them with
-race under the unit tag and fails when any of them fails, is skipped or
does not run. New tests in those files join the gate on their own; the
repository integration tests run in the integration job.

Usage (from backend/):
    python3 ../tools/gateway-resilience/ci_gate.py
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

# Package directory -> file pattern of its resilience tests.
PACKAGES = {
    "internal/resilience": "*_test.go",
    "internal/service": "*resilience*_test.go",
    "internal/handler": "resilience_*_test.go",
    "internal/handler/admin": "resilience_*_test.go",
    "internal/server/middleware": "resilience_*_test.go",
}

TEST_FUNC = re.compile(r"^func (Test\w+)\(t \*testing\.T\)", re.M)
BUILD_TAG = re.compile(r"^//go:build (.+)$", re.M)


def collect(pkg: str, pattern: str) -> set[str]:
    names: set[str] = set()
    for path in sorted(Path(pkg).glob(pattern)):
        src = path.read_text(encoding="utf-8")
        tag = BUILD_TAG.search(src)
        if tag and tag.group(1).strip() != "unit":
            continue
        names.update(TEST_FUNC.findall(src))
    return names


def run(pkg: str, names: set[str]) -> list[str]:
    regex = "^(" + "|".join(sorted(names)) + ")$"
    proc = subprocess.run(
        ["go", "test", "-race", "-tags=unit", "-count=1", "-timeout=15m", "-json", "-run", regex, "./" + pkg],
        capture_output=True, text=True, check=False,
    )
    events = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
    passed = {e["Test"] for e in events if e.get("Action") == "pass" and e.get("Test") and "/" not in e["Test"]}
    problems = [f"{pkg}: {e.get('Test') or 'package'} {e['Action']}" for e in events if e.get("Action") in ("fail", "skip")]
    problems += [f"{pkg}: {name} did not pass" for name in sorted(names - passed)]
    if proc.returncode != 0 and not problems:
        problems.append(f"{pkg}: go test exited {proc.returncode}: {proc.stderr.strip()[-2000:]}")
    if not problems:
        print(f"{pkg}: all {len(names)} resilience tests PASS under -race, 0 SKIP", flush=True)
    return problems


def main() -> int:
    problems: list[str] = []
    for pkg, pattern in PACKAGES.items():
        names = collect(pkg, pattern)
        if not names:
            problems.append(f"{pkg}: no resilience tests found ({pattern})")
            continue
        problems += run(pkg, names)
    for problem in problems:
        print("FAIL " + problem, flush=True)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
