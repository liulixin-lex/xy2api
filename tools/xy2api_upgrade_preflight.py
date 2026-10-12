#!/usr/bin/env python3
"""Validate that a Sub2API deployment can be cut over without changing data identity."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path


LEGACY_VOLUME_RE = re.compile(r"(?:^|[_-])sub2api(?:[_-](?:data|app|postgres|redis))?(?:$|[_-])", re.IGNORECASE)
REQUIRED_SECRETS = ("POSTGRES_PASSWORD", "JWT_SECRET", "TOTP_ENCRYPTION_KEY")


@dataclass(frozen=True)
class Finding:
    level: str
    code: str
    message: str


def parse_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        key, value = line.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        values[key.strip()] = value
    return values


def docker_volumes() -> list[str]:
    result = subprocess.run(
        ["docker", "volume", "ls", "--format", "{{.Name}}"],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if result.returncode != 0:
        return []
    return sorted(line.strip() for line in result.stdout.splitlines() if line.strip())


def legacy_paths(root: Path) -> list[str]:
    candidates = (
        root / "etc" / "sub2api",
        root / "opt" / "sub2api",
        root / "var" / "lib" / "sub2api",
        root / "etc" / "systemd" / "system" / "sub2api.service",
    )
    return [str(path) for path in candidates if path.exists()]


def compose_uses_legacy_bind_mount(compose_file: Path | None) -> bool:
    if compose_file is None or not compose_file.is_file():
        return False
    text = compose_file.read_text(encoding="utf-8")
    return "/var/lib/sub2api/production-app:/app/data" in text or "/var/lib/sub2api:/app/data" in text


def evaluate(
    env: dict[str, str],
    volumes: list[str],
    root: Path,
    compose_file: Path | None,
    expected_postgres_db: str,
    expected_redis_db: str,
    expected_dashboard_prefix: str,
) -> list[Finding]:
    findings: list[Finding] = []

    postgres_db = env.get("POSTGRES_DB", "").strip()
    if postgres_db != expected_postgres_db:
        findings.append(Finding("error", "postgres-db-changed", f"POSTGRES_DB must remain {expected_postgres_db!r}; found {postgres_db or '<unset>'!r}."))

    redis_db = env.get("REDIS_DB", "").strip()
    if redis_db != expected_redis_db:
        findings.append(Finding("error", "redis-db-changed", f"REDIS_DB must remain {expected_redis_db!r}; found {redis_db or '<unset>'!r}."))

    dashboard_prefix = env.get("DASHBOARD_CACHE_KEY_PREFIX", "").strip()
    if dashboard_prefix != expected_dashboard_prefix:
        findings.append(Finding("warning", "dashboard-prefix-unverified", f"Set DASHBOARD_CACHE_KEY_PREFIX={expected_dashboard_prefix} or preserve the same value in config.yaml during the first cutover."))

    for key in REQUIRED_SECRETS:
        if not env.get(key, "").strip():
            findings.append(Finding("error", "missing-stable-secret", f"{key} is empty; reuse the existing value before cutover."))

    app_volume = env.get("APP_DATA_VOLUME_NAME", "").strip()
    legacy_candidates = [name for name in volumes if LEGACY_VOLUME_RE.search(name)]
    bind_mount = compose_uses_legacy_bind_mount(compose_file)
    if bind_mount:
        findings.append(Finding("info", "legacy-bind-mount", "Compose keeps the legacy /var/lib/sub2api application data bind mount."))
    elif not app_volume or app_volume == "xy2api_data":
        if len(legacy_candidates) == 1:
            findings.append(Finding("error", "legacy-volume-not-selected", f"Set APP_DATA_VOLUME_NAME={legacy_candidates[0]} before cutover."))
        elif len(legacy_candidates) > 1:
            findings.append(Finding("error", "legacy-volume-ambiguous", f"Multiple legacy volumes were found: {legacy_candidates}. Select the verified application data volume explicitly."))
        else:
            findings.append(Finding("error", "legacy-volume-unresolved", "APP_DATA_VOLUME_NAME still points to the fresh-install default and no unique legacy volume was found."))
    elif app_volume not in volumes and volumes:
        findings.append(Finding("error", "app-volume-missing", f"Configured APP_DATA_VOLUME_NAME={app_volume} is not present in docker volume ls."))

    paths = legacy_paths(root)
    if paths:
        findings.append(Finding("info", "legacy-installation-detected", f"Legacy installation paths detected: {paths}. Keep them unchanged until XY2API validation succeeds."))

    if not findings:
        findings.append(Finding("info", "ready", "No identity-changing upgrade issue was detected."))
    return findings


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--compose-file", type=Path)
    parser.add_argument("--root", type=Path, default=Path("/"))
    parser.add_argument("--skip-docker", action="store_true")
    parser.add_argument("--expected-postgres-db", default="sub2api")
    parser.add_argument("--expected-redis-db", default="0")
    parser.add_argument("--expected-dashboard-prefix", default="sub2api:")
    parser.add_argument("--json", action="store_true", dest="json_output")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    if not args.env_file.is_file():
        print(f"ERROR: env file not found: {args.env_file}", file=sys.stderr)
        return 2

    findings = evaluate(
        parse_env(args.env_file),
        [] if args.skip_docker else docker_volumes(),
        args.root,
        args.compose_file,
        args.expected_postgres_db,
        args.expected_redis_db,
        args.expected_dashboard_prefix,
    )
    if args.json_output:
        print(json.dumps([asdict(item) for item in findings], indent=2, ensure_ascii=False))
    else:
        for item in findings:
            print(f"{item.level.upper()}: [{item.code}] {item.message}")
    return 2 if any(item.level == "error" for item in findings) else 0


if __name__ == "__main__":
    raise SystemExit(main())
