#!/usr/bin/env python3
"""Read-only environment and repository preflight for XY2API upstream syncs."""

from __future__ import annotations

import argparse
import json
import platform
import shutil
import subprocess
import sys
from pathlib import Path


REQUIRED_PATHS = (
    "tools/upstream-sync/sync.py",
    "tools/upstream-sync/policy.json",
    "UPSTREAM_BASE.json",
    "backend/cmd/server/VERSION",
    "backend/cmd/server/SUB2API_COMPAT_VERSION",
    ".github/workflows/upstream-sync.yml",
    ".github/workflows/release.yml",
)


def run(root: Path, *args: str) -> tuple[int, str, str]:
    result = subprocess.run(
        args,
        cwd=root,
        check=False,
        text=True,
        encoding="utf-8",
        errors="replace",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return result.returncode, result.stdout.strip(), result.stderr.strip()


def git(root: Path, *args: str) -> str:
    code, stdout, stderr = run(root, "git", *args)
    return stdout if code == 0 else f"ERROR: {stderr or stdout}"


def find_root(start: Path) -> Path | None:
    resolved = start.resolve()
    for candidate in (resolved, *resolved.parents):
        if (candidate / ".git").exists() and (candidate / "tools/upstream-sync/sync.py").is_file():
            return candidate
    return None


def first_line(value: str) -> str:
    return value.splitlines()[0] if value else ""


def tool_version(root: Path, name: str, *version_args: str) -> dict[str, object]:
    executable = shutil.which(name)
    if not executable:
        return {"available": False, "version": ""}
    code, stdout, stderr = run(root, executable, *version_args)
    return {
        "available": code == 0,
        "version": first_line(stdout or stderr),
        "path": executable,
    }


def read_text(root: Path, relative: str) -> str:
    return (root / relative).read_text(encoding="utf-8").strip()


def read_json(root: Path, relative: str) -> dict:
    return json.loads((root / relative).read_text(encoding="utf-8"))


def build_report(root: Path, run_audit: bool) -> dict[str, object]:
    missing = [relative for relative in REQUIRED_PATHS if not (root / relative).exists()]
    status = git(root, "status", "--porcelain")
    branch = git(root, "branch", "--show-current")
    head = git(root, "rev-parse", "HEAD")
    origin_main = git(root, "rev-parse", "--verify", "origin/main")
    upstream_fetch = git(root, "remote", "get-url", "upstream")
    upstream_push = git(root, "remote", "get-url", "--push", "upstream")

    provenance: dict[str, object] = {}
    provenance_error = ""
    try:
        provenance = read_json(root, "UPSTREAM_BASE.json")
    except (OSError, json.JSONDecodeError) as exc:
        provenance_error = str(exc)

    product_path = root / "backend/cmd/server/VERSION"
    compat_path = root / "backend/cmd/server/SUB2API_COMPAT_VERSION"
    versions = {
        "product": read_text(root, "backend/cmd/server/VERSION") if product_path.is_file() else "",
        "sub2api_compat": (
            read_text(root, "backend/cmd/server/SUB2API_COMPAT_VERSION") if compat_path.is_file() else ""
        ),
    }

    recorded_tag_ref = str(provenance.get("upstream_tag_ref", ""))
    recorded_tag_object = str(provenance.get("upstream_tag_object", ""))
    local_tag_type = git(root, "cat-file", "-t", recorded_tag_ref) if recorded_tag_ref else ""
    local_tag_object = git(root, "rev-parse", recorded_tag_ref) if recorded_tag_ref else ""

    tools = {
        "git": tool_version(root, "git", "--version"),
        "python": {"available": True, "version": platform.python_version(), "path": sys.executable},
        "gh": tool_version(root, "gh", "--version"),
        "docker": tool_version(root, "docker", "--version"),
        "go": tool_version(root, "go", "version"),
        "node": tool_version(root, "node", "--version"),
        "pnpm": tool_version(root, "pnpm", "--version"),
    }

    warnings: list[str] = []
    if missing:
        warnings.append(f"missing required paths: {missing}")
    if status:
        warnings.append("working tree is not clean")
    if branch != "main":
        warnings.append(f"current branch is {branch or '<detached>'}, not main")
    if upstream_fetch.startswith("ERROR:"):
        warnings.append("upstream fetch remote is unavailable")
    elif "Wei-Shaw/sub2api" not in upstream_fetch:
        warnings.append(f"unexpected upstream fetch URL: {upstream_fetch}")
    if upstream_push != "DISABLED":
        warnings.append(f"upstream push URL is not disabled: {upstream_push}")
    if provenance_error:
        warnings.append(f"cannot read UPSTREAM_BASE.json: {provenance_error}")
    elif provenance.get("status") != "resolved":
        warnings.append(f"previous sync provenance is not resolved: {provenance.get('status')}")
    if provenance and provenance.get("xy2api_version") != versions["product"]:
        warnings.append("VERSION does not match UPSTREAM_BASE.json")
    if provenance and provenance.get("sub2api_compat_version") != versions["sub2api_compat"]:
        warnings.append("SUB2API_COMPAT_VERSION does not match UPSTREAM_BASE.json")
    if recorded_tag_ref:
        if local_tag_type != "tag":
            warnings.append(f"recorded upstream tag is not an available annotated tag: {recorded_tag_ref}")
        elif local_tag_object != recorded_tag_object:
            warnings.append(
                f"recorded upstream tag object mismatch: expected {recorded_tag_object}, got {local_tag_object}"
            )
    if head.startswith("ERROR:") or origin_main.startswith("ERROR:") or head != origin_main:
        warnings.append("local HEAD and origin/main are not confirmed equal")

    audit: dict[str, object] = {"requested": run_audit, "passed": None, "output": ""}
    if run_audit:
        code, stdout, stderr = run(root, sys.executable, "tools/upstream-sync/sync.py", "audit")
        audit = {"requested": True, "passed": code == 0, "output": stdout or stderr}
        if code != 0:
            warnings.append("upstream sync audit failed")

    upstream_ok = not upstream_fetch.startswith("ERROR:") and "Wei-Shaw/sub2api" in upstream_fetch
    provenance_ok = (
        not provenance_error
        and provenance.get("status") == "resolved"
        and provenance.get("xy2api_version") == versions["product"]
        and provenance.get("sub2api_compat_version") == versions["sub2api_compat"]
        and bool(recorded_tag_ref)
        and local_tag_type == "tag"
        and local_tag_object == recorded_tag_object
    )
    ready = (
        not missing
        and not status
        and branch == "main"
        and upstream_ok
        and upstream_push == "DISABLED"
        and head == origin_main
        and provenance_ok
    )
    if run_audit:
        ready = ready and audit["passed"] is True

    return {
        "schema_version": 1,
        "repo_root": str(root),
        "platform": {"system": platform.system(), "release": platform.release()},
        "git": {
            "branch": branch,
            "head": head,
            "origin_main": origin_main,
            "clean": not bool(status),
            "upstream_fetch": upstream_fetch,
            "upstream_push": upstream_push,
        },
        "versions": versions,
        "last_sync": {
            "tag": provenance.get("upstream_tag", ""),
            "tag_ref": recorded_tag_ref,
            "tag_object": provenance.get("upstream_tag_object", ""),
            "local_tag_type": local_tag_type,
            "local_tag_object": local_tag_object,
            "commit": provenance.get("upstream_commit", ""),
            "status": provenance.get("status", ""),
            "sync_pr": provenance.get("sync_pr", ""),
        },
        "tools": tools,
        "audit": audit,
        "ready_for_prepare": ready,
        "warnings": warnings,
    }


def print_human(report: dict[str, object]) -> None:
    git_info = report["git"]
    versions = report["versions"]
    last_sync = report["last_sync"]
    print(f"repo: {report['repo_root']}")
    print(f"branch/head: {git_info['branch']} @ {git_info['head']}")
    print(f"clean: {git_info['clean']}  origin/main aligned: {git_info['head'] == git_info['origin_main']}")
    print(f"versions: XY2API {versions['product']} / Sub2API compat {versions['sub2api_compat']}")
    print(f"last sync: {last_sync['tag']} @ {last_sync['commit']} ({last_sync['status']})")
    available = [name for name, item in report["tools"].items() if item.get("available")]
    missing = [name for name, item in report["tools"].items() if not item.get("available")]
    print(f"tools available: {', '.join(available)}")
    print(f"tools missing: {', '.join(missing) if missing else 'none'}")
    if report["audit"]["requested"]:
        print(f"audit passed: {report['audit']['passed']}")
        if report["audit"]["output"]:
            print(report["audit"]["output"])
    print(f"ready_for_prepare: {report['ready_for_prepare']}")
    for warning in report["warnings"]:
        print(f"warning: {warning}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path.cwd(), help="repository path or a child path")
    parser.add_argument("--audit", action="store_true", help="also run the repository upstream-sync audit")
    parser.add_argument("--json", action="store_true", help="print machine-readable JSON")
    parser.add_argument("--strict", action="store_true", help="return non-zero unless ready_for_prepare is true")
    args = parser.parse_args()

    root = find_root(args.repo)
    if root is None:
        print("ERROR: cannot locate an XY2API repository from the supplied path", file=sys.stderr)
        return 2

    report = build_report(root, args.audit)
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print_human(report)
    return 1 if args.strict and not report["ready_for_prepare"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
