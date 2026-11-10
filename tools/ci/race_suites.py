#!/usr/bin/env python3
"""Run the backend test suites that need the race detector or real services.

Every suite runs ``go test -race -json`` and fails when a test fails, is
skipped, or when the selection matches no test. A missing PostgreSQL or Redis
therefore cannot quietly turn a suite into a no-op.

Usage (from the repository root):

    python3 tools/ci/race_suites.py            # all suites
    python3 tools/ci/race_suites.py resilience # selected suites

Suites that need a service read its address from an environment variable; CI
sets them from the service containers in ``.github/workflows/ci.yml``.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

BACKEND = Path(__file__).resolve().parents[2] / "backend"

TEST_FUNC = re.compile(r"^func (Test\w+)\(t \*testing\.T\)", re.M)
BUILD_TAG = re.compile(r"^//go:build (.+)$", re.M)


@dataclass
class Suite:
    name: str
    packages: list[str]
    run: str = ""
    tags: str = "unit"
    env: list[str] = field(default_factory=list)
    flags: list[str] = field(default_factory=list)
    timeout: str = "10m"
    # Package -> glob of test files whose tests form the selection (instead of ``run``).
    files: dict[str, str] = field(default_factory=dict)


SUITES = [
    # Every test in the gateway resilience test files.
    Suite(
        name="resilience",
        packages=[],
        files={
            "internal/resilience": "*_test.go",
            "internal/service": "*resilience*_test.go",
            "internal/handler": "resilience_*_test.go",
            "internal/handler/admin": "resilience_*_test.go",
            "internal/server/middleware": "resilience_*_test.go",
        },
        timeout="15m",
    ),
    Suite(
        name="gateway-reliability",
        packages=["internal/service", "internal/handler", "internal/handler/admin"],
        run=r"^(TestGPT61Sol(NativeHTTP|HTTPValidation|WS)|TestNonstreamReadErrorSafety$|TestRequestBodyReplaySafety$"
        r"|TestOpenAIQualityRoutingInertWithoutOptIn$|TestGatewayBufferedReadFailureAuthenticatedFailover$"
        r"|TestRetiredSchedulingRoutesAnswerGone$|TestShareScheduling|TestOpenAIShareScheduling|TestShareAttribution)",
        flags=["-p", "1", "-parallel", "1"],
    ),
    Suite(
        name="openai-cache",
        packages=[
            "internal/pkg/apicompat",
            "internal/service",
            "internal/service/openai_ws_v2",
            "internal/repository",
            "internal/handler",
        ],
        run=r"^(TestOpenAICache|TestCacheControls|TestOpenAIUsageCanonical|TestOpenAIUsageConversion"
        r"|TestOpenAISelectAccountWithLoadAwareness_StickyCapacity)",
        env=["SCHEDULING_TEST_REDIS_ADDR"],
        flags=["-p", "1"],
    ),
    Suite(
        name="stripe-hosted",
        packages=["internal/service"],
        run=r"^TestStripeHostedDatabase",
        env=["STRIPE_HOSTED_TEST_DSN"],
        flags=["-p", "1", "-parallel", "1"],
        timeout="5m",
    ),
    Suite(
        name="mcp-postgres",
        packages=["internal/mcp"],
        run=r"^(TestCore|TestOAuthPostgres|TestReader|TestRuntime)",
        tags="integration",
        env=["MCP_READER_TEST_DSN"],
        flags=["-p", "2"],
        timeout="8m",
    ),
]


def collect_tests(package: str, pattern: str, tags: str) -> list[str]:
    names: set[str] = set()
    for path in sorted((BACKEND / package).glob(pattern)):
        source = path.read_text(encoding="utf-8")
        tag = BUILD_TAG.search(source)
        if tag and tag.group(1).strip() != tags:
            continue
        names.update(TEST_FUNC.findall(source))
    return sorted(names)


def go_test(suite: Suite, packages: list[str], run: str) -> list[dict]:
    cmd = ["go", "test", "-race", f"-tags={suite.tags}", "-count=1", f"-timeout={suite.timeout}", "-json"]
    cmd += suite.flags + ["-run", run] + ["./" + p for p in packages]
    proc = subprocess.run(cmd, cwd=BACKEND, capture_output=True, text=True, check=False)
    events = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
    if proc.returncode != 0 and not any(e.get("Action") == "fail" for e in events):
        raise RuntimeError(f"go test exited {proc.returncode}: {proc.stderr.strip()[-2000:]}")
    return events


def run_suite(suite: Suite) -> list[str]:
    missing_env = [name for name in suite.env if not os.environ.get(name)]
    if missing_env:
        return [f"{suite.name}: environment variable(s) not set: {', '.join(missing_env)}"]

    selections: list[tuple[list[str], str, set[str]]] = []
    if suite.files:
        for package, pattern in suite.files.items():
            names = collect_tests(package, pattern, suite.tags)
            if not names:
                return [f"{suite.name}: no tests found in {package}/{pattern}"]
            selections.append(([package], "^(" + "|".join(names) + ")$", set(names)))
    else:
        selections.append((suite.packages, suite.run, set()))

    problems: list[str] = []
    passed_total = 0
    for packages, run, required in selections:
        try:
            events = go_test(suite, packages, run)
        except RuntimeError as err:
            problems.append(f"{suite.name}: {err}")
            continue
        top_level_passed = {e["Test"] for e in events if e.get("Action") == "pass" and e.get("Test") and "/" not in e["Test"]}
        for e in events:
            if e.get("Action") in ("fail", "skip") and e.get("Test"):
                problems.append(f"{suite.name}: {e['Package']} {e['Test']} {e['Action']}")
        for name in sorted(required - top_level_passed):
            problems.append(f"{suite.name}: {name} did not pass")
        if not top_level_passed:
            problems.append(f"{suite.name}: no test matched {run} in {', '.join(packages)}")
        passed_total += len(top_level_passed)

    if not problems:
        print(f"{suite.name}: {passed_total} tests passed under -race, 0 skipped", flush=True)
    return problems


def main(argv: list[str]) -> int:
    wanted = set(argv)
    unknown = wanted - {s.name for s in SUITES}
    if unknown:
        print(f"unknown suite(s): {', '.join(sorted(unknown))}", file=sys.stderr)
        return 2
    problems: list[str] = []
    for suite in SUITES:
        if not wanted or suite.name in wanted:
            problems += run_suite(suite)
    for problem in problems:
        print("FAIL " + problem, flush=True)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
