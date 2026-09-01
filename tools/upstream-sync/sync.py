#!/usr/bin/env python3
"""Prepare, report, normalize, and audit Sub2API tag synchronizations."""

from __future__ import annotations

import argparse
import fnmatch
import hashlib
import json
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
POLICY_PATH = Path(__file__).with_name("policy.json")
PROVENANCE_PATH = ROOT / "UPSTREAM_BASE.json"
MIGRATION_MANIFEST_PATH = ROOT / "backend" / "migrations" / "checksums.json"
PRODUCT_VERSION_PATH = ROOT / "backend" / "cmd" / "server" / "VERSION"
COMPAT_VERSION_PATH = ROOT / "backend" / "cmd" / "server" / "SUB2API_COMPAT_VERSION"


class SyncError(RuntimeError):
    pass


def run_git(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode != 0:
        raise SyncError(result.stderr.strip() or result.stdout.strip())
    return result


def run_git_bytes(*args: str, check: bool = True) -> subprocess.CompletedProcess[bytes]:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode != 0:
        message = result.stderr.decode("utf-8", errors="replace").strip()
        if not message:
            message = result.stdout.decode("utf-8", errors="replace").strip()
        raise SyncError(message)
    return result


def git_output(*args: str) -> str:
    return run_git(*args).stdout.strip()


def load_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SyncError(f"cannot read {path.relative_to(ROOT)}: {exc}") from exc


def write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def load_policy() -> dict:
    policy = load_json(POLICY_PATH)
    if policy.get("schema_version") != 1:
        raise SyncError("unsupported upstream sync policy schema")
    return policy


def matches_any(path: str, patterns: list[str]) -> bool:
    return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)


def classify_owner(path: str, policy: dict) -> str | None:
    categories = policy.get("categories", {})
    priority = policy.get("ownership_priority", list(categories))
    for category in priority:
        if matches_any(path, categories.get(category, [])):
            return category
    return None


def changed_files(base: str, head: str) -> list[str]:
    output = git_output("diff", "--name-only", f"{base}..{head}", "--")
    return sorted(line for line in output.splitlines() if line)


def commit_records(revision_range: str) -> list[dict[str, str]]:
    output = git_output("log", "--reverse", "--format=%H%x09%s", revision_range, "--")
    records: list[dict[str, str]] = []
    for line in output.splitlines():
        if not line:
            continue
        commit, _, subject = line.partition("\t")
        records.append({"commit": commit, "subject": subject})
    return records


def classify_impacts(paths: list[str]) -> dict[str, list[str]]:
    rules = {
        "migrations": ["backend/migrations/*.sql"],
        "api": [
            "backend/internal/handler/**",
            "backend/internal/server/routes/**",
            "backend/pkg/pluginapi/**",
            "frontend/src/api/**",
        ],
        "config": [
            "backend/internal/config/**",
            "deploy/.env.example",
            "deploy/config.example.yaml",
            "deploy/docker-compose*.yml",
        ],
        "dependencies": [
            "backend/go.mod",
            "backend/go.sum",
            "frontend/package.json",
            "frontend/*lock*",
            "Dockerfile*",
            "deploy/Dockerfile",
        ],
        "generated": [
            "backend/ent/**",
            "backend/cmd/server/wire_gen.go",
            "backend/pkg/pluginapi/**/*.pb.go",
        ],
    }
    return {
        name: [path for path in paths if matches_any(path, patterns)]
        for name, patterns in rules.items()
    }


def build_report(base: str, upstream: str, fork: str, policy: dict) -> dict:
    upstream_files = changed_files(base, upstream)
    fork_files = changed_files(base, fork)
    overlap = sorted(set(upstream_files) & set(fork_files))
    ownership = {category: [] for category in policy["ownership_priority"]}
    ownership["unclassified"] = []
    for path in fork_files:
        owner = classify_owner(path, policy)
        ownership[owner or "unclassified"].append(path)
    all_paths = sorted(set(upstream_files) | set(fork_files))
    return {
        "schema_version": 1,
        "base": base,
        "upstream": upstream,
        "fork": fork,
        "upstream_commit_count": int(git_output("rev-list", "--count", f"{base}..{upstream}")),
        "fork_commit_count": int(git_output("rev-list", "--count", f"{base}..{fork}")),
        "upstream_commits": commit_records(f"{base}..{upstream}"),
        "upstream_changed_files": upstream_files,
        "fork_changed_files": fork_files,
        "overlap_files": overlap,
        "three_way_matrix": [
            {
                "path": path,
                "upstream_changed": path in upstream_files,
                "fork_changed": path in fork_files,
                "overlap": path in overlap,
                "fork_owner": classify_owner(path, policy) if path in fork_files else "upstream",
            }
            for path in all_paths
        ],
        "fork_ownership": ownership,
        "upstream_impact": classify_impacts(upstream_files),
        "fork_impact": classify_impacts(fork_files),
        "upstream_migrations": [path for path in upstream_files if path.startswith("backend/migrations/") and path.endswith(".sql")],
    }


def command_report(args: argparse.Namespace) -> int:
    policy = load_policy()
    fork = git_output("rev-parse", args.fork)
    upstream = git_output("rev-parse", args.upstream)
    base = git_output("merge-base", fork, upstream)
    report = build_report(base, upstream, fork, policy)
    write_json(Path(args.output).resolve(), report)
    print(json.dumps(report, indent=2, ensure_ascii=False))
    return 0


def normalize_module_bytes(raw: bytes, source: bytes, target: bytes) -> bytes:
    return raw.replace(source, target)


def normalize_module_paths(base: str, upstream: str, policy: dict) -> list[str]:
    rewrite = policy["module_rewrite"]
    source = rewrite["from"]
    target = rewrite["to"]
    extensions = set(rewrite["extensions"])
    updated: list[str] = []
    for relative in changed_files(base, upstream):
        path = ROOT / relative
        if path.suffix not in extensions or not path.is_file():
            continue
        raw = path.read_bytes()
        normalized = normalize_module_bytes(raw, source.encode(), target.encode())
        if normalized != raw:
            path.write_bytes(normalized)
            updated.append(relative)
    return updated


def command_normalize(args: argparse.Namespace) -> int:
    policy = load_policy()
    updated = normalize_module_paths(args.base, args.upstream, policy)
    print(json.dumps({"updated": updated}, indent=2, ensure_ascii=False))
    return 0


def migration_checksum(path: Path) -> str:
    content = path.read_bytes().decode("utf-8").strip()
    return hashlib.sha256(content.encode("utf-8")).hexdigest()


def audit_migrations(errors: list[str]) -> None:
    manifest = load_json(MIGRATION_MANIFEST_PATH)
    if manifest.get("schema_version") != 1:
        errors.append("migration checksum manifest has an unsupported schema")
    if manifest.get("algorithm") != "sha256(strings.TrimSpace(utf8))":
        errors.append("migration checksum manifest uses an unexpected algorithm")
    expected = manifest.get("migrations", {})
    files = sorted((ROOT / "backend" / "migrations").glob("*.sql"))
    names = {path.name for path in files}
    if names != set(expected):
        missing = sorted(names - set(expected))
        stale = sorted(set(expected) - names)
        errors.append(f"migration checksum manifest mismatch; missing={missing}, stale={stale}")
    for path in files:
        want = expected.get(path.name)
        if want and migration_checksum(path) != want:
            errors.append(f"published migration changed: backend/migrations/{path.name}")


def tracked_files() -> list[str]:
    return [line for line in git_output("ls-files").splitlines() if line]


def audit_policy(policy: dict, errors: list[str]) -> None:
    expected_categories = {"upstream", "xy_owned", "compat_invariant", "manual_merge", "generated"}
    actual_categories = set(policy.get("categories", {}))
    if actual_categories != expected_categories:
        errors.append(
            "ownership categories must be exactly "
            f"{sorted(expected_categories)}; got {sorted(actual_categories)}"
        )

    ownership_priority = policy.get("ownership_priority", [])
    if len(ownership_priority) != len(set(ownership_priority)) or set(ownership_priority) != expected_categories:
        errors.append(
            "ownership_priority must list every ownership category exactly once; "
            f"got {ownership_priority}"
        )

    patch_ids = set(policy.get("custom_patches", []))
    test_bindings = policy.get("custom_patch_tests", [])
    bound_ids = {binding.get("id", "") for binding in test_bindings}
    if bound_ids != patch_ids:
        errors.append(
            "custom patch test bindings mismatch; "
            f"missing={sorted(patch_ids - bound_ids)}, unknown={sorted(bound_ids - patch_ids)}"
        )
    for binding in test_bindings:
        path = ROOT / binding.get("path", "")
        literal = binding.get("literal", "")
        if not path.is_file() or not literal or literal not in path.read_text(encoding="utf-8", errors="replace"):
            errors.append(
                f"custom patch test binding is missing: {binding.get('id')} -> "
                f"{binding.get('path')}::{literal}"
            )

    for rule in policy.get("required_literals", []):
        path = ROOT / rule["path"]
        if not path.is_file() or rule["value"] not in path.read_text(encoding="utf-8"):
            errors.append(f"required compatibility literal missing: {rule['path']} -> {rule['value']}")

    for relative in policy.get("required_absent_paths", []):
        if (ROOT / relative).exists():
            errors.append(f"fork-owned removal was reintroduced: {relative}")

    forbidden = policy.get("forbidden_module_paths", [])
    for relative in tracked_files():
        if not relative.startswith("backend/") or Path(relative).suffix not in {".go", ".proto"}:
            continue
        path = ROOT / relative
        if not path.is_file():
            continue
        content = path.read_text(encoding="utf-8", errors="replace")
        for value in forbidden:
            if value in content:
                errors.append(f"upstream Go module path remains in {relative}: {value}")


def classify_fork_differences(upstream: str, policy: dict) -> list[str]:
    rewrite = policy["module_rewrite"]
    source = rewrite["from"].encode("utf-8")
    target = rewrite["to"].encode("utf-8")
    rewrite_extensions = set(rewrite["extensions"])
    unclassified: list[str] = []

    for relative in changed_files(upstream, "HEAD"):
        if relative in {"UPSTREAM_BASE.json"} or relative.startswith("tools/upstream-sync/") or relative.startswith("docs/upstream-sync/"):
            continue
        if classify_owner(relative, policy) is not None:
            continue
        current_path = ROOT / relative
        upstream_blob = run_git_bytes("show", f"{upstream}:{relative}", check=False)
        if current_path.is_file() and upstream_blob.returncode == 0:
            current = current_path.read_bytes()
            if current_path.suffix in rewrite_extensions:
                current = normalize_module_bytes(current, target, source)
            if current == upstream_blob.stdout:
                continue
        unclassified.append(relative)
    return unclassified


def verify_tag_signature(tag_ref: str) -> str:
    tag_type = git_output("cat-file", "-t", tag_ref)
    if tag_type != "tag":
        raise SyncError(f"{tag_ref} must be an annotated tag, got {tag_type}")

    tag_body = run_git_bytes("cat-file", "tag", tag_ref).stdout
    signature_markers = (b"BEGIN PGP SIGNATURE", b"BEGIN SSH SIGNATURE")
    if not any(marker in tag_body for marker in signature_markers):
        return "unsigned"

    verification = run_git("verify-tag", "--raw", tag_ref, check=False)
    if verification.returncode != 0:
        message = verification.stderr.strip() or verification.stdout.strip()
        raise SyncError(f"tag signature verification failed for {tag_ref}: {message}")
    return "verified"


def path_inside_repo(path: str) -> tuple[Path, str]:
    resolved = Path(path).resolve()
    try:
        relative = resolved.relative_to(ROOT).as_posix()
    except ValueError as exc:
        raise SyncError(f"path must stay inside the repository: {resolved}") from exc
    return resolved, relative


def audit_provenance(policy: dict, errors: list[str]) -> None:
    provenance = load_json(PROVENANCE_PATH)
    if provenance.get("schema_version") != 1:
        errors.append("UPSTREAM_BASE.json has an unsupported schema")
        return

    upstream_commit = provenance.get("upstream_commit", "")
    if not upstream_commit:
        errors.append("UPSTREAM_BASE.json does not record upstream_commit")
    elif run_git("merge-base", "--is-ancestor", upstream_commit, "HEAD", check=False).returncode != 0:
        errors.append(f"recorded upstream commit is not an ancestor of HEAD: {upstream_commit}")

    tag_ref = provenance.get("upstream_tag_ref", "")
    tag_object = provenance.get("upstream_tag_object", "")
    if not tag_ref or not tag_object:
        errors.append("UPSTREAM_BASE.json does not record the upstream tag ref and object")
    else:
        local_tag = run_git("rev-parse", tag_ref, check=False)
        if local_tag.returncode != 0:
            errors.append(f"recorded upstream tag is unavailable locally: {tag_ref}")
        elif local_tag.stdout.strip() != tag_object:
            errors.append(f"recorded upstream tag object changed: {tag_ref}")
        tagged_commit = run_git("rev-parse", f"{tag_ref}^{{commit}}", check=False)
        if tagged_commit.returncode == 0 and upstream_commit and tagged_commit.stdout.strip() != upstream_commit:
            errors.append(f"recorded upstream tag no longer targets {upstream_commit}")

    product_version = PRODUCT_VERSION_PATH.read_text(encoding="utf-8").strip()
    compat_version = COMPAT_VERSION_PATH.read_text(encoding="utf-8").strip()
    if provenance.get("xy2api_version") != product_version:
        errors.append("UPSTREAM_BASE.json xy2api_version does not match VERSION")
    if provenance.get("sub2api_compat_version") != compat_version:
        errors.append("UPSTREAM_BASE.json sub2api_compat_version does not match SUB2API_COMPAT_VERSION")

    pending = [item.get("path", "") for item in provenance.get("manual_resolutions", []) if item.get("status") != "resolved"]
    if pending:
        errors.append(f"manual merge resolutions are still pending: {pending}")
    if provenance.get("status") != "resolved":
        errors.append(f"upstream sync provenance status is not resolved: {provenance.get('status')}")

    expected_patches = set(policy.get("custom_patches", []))
    recorded_patches = set(provenance.get("custom_patches", []))
    if expected_patches != recorded_patches:
        errors.append(f"custom patch inventory mismatch; missing={sorted(expected_patches - recorded_patches)}, unknown={sorted(recorded_patches - expected_patches)}")

    if upstream_commit:
        unclassified = classify_fork_differences(upstream_commit, policy)
        if unclassified:
            errors.append(f"unclassified fork differences remain: {unclassified}")


def command_audit(_args: argparse.Namespace) -> int:
    policy = load_policy()
    errors: list[str] = []
    status = git_output("status", "--porcelain")
    if status:
        errors.append("working tree is not clean")

    conflict_marker = run_git("grep", "-n", "-e", "^<<<<<<< ", "-e", "^>>>>>>> ", "--", check=False)
    if conflict_marker.returncode == 0:
        errors.append("merge conflict markers remain:\n" + conflict_marker.stdout.strip())

    audit_migrations(errors)
    audit_policy(policy, errors)
    audit_provenance(policy, errors)

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("upstream sync audit passed")
    return 0


def command_prepare(args: argparse.Namespace) -> int:
    if git_output("status", "--porcelain"):
        raise SyncError("prepare requires a clean working tree")

    policy = load_policy()
    tag = args.tag if args.tag.startswith("v") else f"v{args.tag}"
    tag_ref = f"sub2api/{tag}"
    fork = git_output("rev-parse", "HEAD")
    signature_status = verify_tag_signature(tag_ref)
    upstream = git_output("rev-parse", f"{tag_ref}^{{commit}}")
    tag_object = git_output("rev-parse", tag_ref)
    base = git_output("merge-base", fork, upstream)
    if args.expected_commit and upstream != args.expected_commit:
        raise SyncError(f"tag target changed: expected {args.expected_commit}, got {upstream}")
    if args.expected_base and base != args.expected_base:
        raise SyncError(f"merge base changed: expected {args.expected_base}, got {base}")
    report = build_report(base, upstream, fork, policy)
    report_path, report_relative = path_inside_repo(args.report)

    merge = run_git("merge", "--no-ff", "--no-commit", upstream, check=False)
    conflicts = [line for line in git_output("diff", "--name-only", "--diff-filter=U").splitlines() if line]
    allowed = policy["categories"]["manual_merge"]
    unexpected = [path for path in conflicts if not matches_any(path, allowed)]
    report["merge_conflicts"] = conflicts
    report["unexpected_conflicts"] = unexpected
    report["upstream_tag"] = tag
    report["upstream_tag_ref"] = tag_ref
    report["upstream_tag_object"] = tag_object
    report["upstream_tag_signature"] = signature_status
    write_json(report_path, report)
    if unexpected:
        run_git("merge", "--abort", check=False)
        raise SyncError(f"unexpected conflicts blocked the sync: {unexpected}")
    if merge.returncode != 0 and not conflicts:
        run_git("merge", "--abort", check=False)
        raise SyncError(merge.stderr.strip() or "upstream merge failed")

    for path in conflicts:
        run_git("checkout", "--ours", "--", path)
        run_git("add", "--", path)
    run_git("commit", "-m", f"merge: sub2api {tag}")

    normalized = normalize_module_paths(base, upstream, policy)
    if normalized:
        run_git("add", "--", *normalized)
        run_git("commit", "-m", f"sync: normalize xy2api module paths for sub2api {tag}")

    provenance = {
        "schema_version": 1,
        "upstream_repo": policy["upstream_repo"],
        "upstream_tag": tag,
        "upstream_tag_ref": tag_ref,
        "upstream_tag_object": tag_object,
        "upstream_tag_signature": signature_status,
        "upstream_commit": upstream,
        "merge_base": base,
        "fork_start": fork,
        "xy2api_version": PRODUCT_VERSION_PATH.read_text(encoding="utf-8").strip(),
        "sub2api_compat_version": COMPAT_VERSION_PATH.read_text(encoding="utf-8").strip(),
        "status": "pending" if conflicts else "review",
        "manual_resolutions": [
            {"path": path, "status": "pending", "resolution": "kept XY2API side until human review"}
            for path in conflicts
        ],
        "custom_patches": policy["custom_patches"],
    }
    write_json(PROVENANCE_PATH, provenance)
    run_git("add", "UPSTREAM_BASE.json", report_relative)
    run_git("commit", "-m", f"docs: record sub2api {tag} sync provenance")
    print(json.dumps({"tag": tag, "upstream": upstream, "conflicts": conflicts, "normalized": normalized}, indent=2, ensure_ascii=False))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    report = subparsers.add_parser("report")
    report.add_argument("--fork", default="HEAD")
    report.add_argument("--upstream", required=True)
    report.add_argument("--output", required=True)
    report.set_defaults(func=command_report)

    normalize = subparsers.add_parser("normalize")
    normalize.add_argument("--base", required=True)
    normalize.add_argument("--upstream", required=True)
    normalize.set_defaults(func=command_normalize)

    audit = subparsers.add_parser("audit")
    audit.set_defaults(func=command_audit)

    prepare = subparsers.add_parser("prepare")
    prepare.add_argument("--tag", required=True)
    prepare.add_argument("--report", required=True)
    prepare.add_argument("--expected-commit")
    prepare.add_argument("--expected-base")
    prepare.set_defaults(func=command_prepare)
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        return args.func(args)
    except SyncError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
