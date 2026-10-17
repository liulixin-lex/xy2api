#!/usr/bin/env python3
"""Check that the repository's documentation keeps up with its code.

Rules live in tools/docs_consistency.json. The checks:

- links: relative links in tracked Markdown resolve to tracked files;
- docs-index: docs/README.md lists every file under docs/ and every
  OpenSpec change;
- docs-whitelist: no tracked file under docs/ is ignored by .gitignore, so the
  whitelist stays complete for the next new document;
- pins: version pins (Go, golangci-lint) agree with their source of truth;
- contracts: names the code defines (metrics, log events, settings) appear in
  the document that is meant to list them;
- retired: identifiers of removed features appear only where history is kept;
- private: host details (non-public hosts and addresses, server and
  workstation paths) stay out of the public repository;
- memory: docs/PROJECT_MEMORY.md stays readable (size cap, log structure).

Standard library only. Exit status 1 when a check finds a problem.

    python3 tools/check_docs_consistency.py [--root DIR] [--config FILE]
"""

from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys
import urllib.parse
from pathlib import Path

DEFAULT_CONFIG = "tools/docs_consistency.json"
BINARY_SUFFIXES = {
    ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".svg", ".gz", ".tgz", ".zip",
    ".woff", ".woff2", ".ttf", ".otf", ".pdf", ".exe", ".dll", ".so", ".dylib",
}
SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:")
INLINE_LINK = re.compile(r"!?\[(?:[^\[\]]|\[[^\]]*\])*\]\(\s*(<[^>]*>|[^)\s]+)(?:\s+(?:\"[^\"]*\"|'[^']*'))?\s*\)")
REFERENCE_LINK = re.compile(r"^\s{0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)", re.M)
HTML_ATTR = re.compile(r"<(?:img|a)\b[^>]*?\s(?:src|href)=\"([^\"]+)\"", re.I)
FENCE = re.compile(r"^\s{0,3}(`{3,}|~{3,})")


class Repo:
    """Tracked files of a git work tree."""

    def __init__(self, root: Path):
        self.root = root
        out = subprocess.run(
            ["git", "ls-files", "-z"], cwd=root, check=True, stdout=subprocess.PIPE
        ).stdout.decode("utf-8")
        self.files = sorted(p for p in out.split("\0") if p and (root / p).is_file())
        self.file_set = set(self.files)
        self.dirs = set()
        for path in self.files:
            parts = path.split("/")
            for i in range(1, len(parts)):
                self.dirs.add("/".join(parts[:i]))

    def read(self, path: str) -> str:
        return (self.root / path).read_text(encoding="utf-8", errors="replace")

    def exists(self, path: str) -> bool:
        path = path.rstrip("/")
        return path in self.file_set or path in self.dirs

    def ignored(self, paths: list[str]) -> list[str]:
        if not paths:
            return []
        proc = subprocess.run(
            ["git", "check-ignore", "--no-index", "--stdin", "-z"],
            cwd=self.root,
            input="\0".join(paths).encode("utf-8"),
            stdout=subprocess.PIPE,
        )
        if proc.returncode not in (0, 1):
            raise RuntimeError("git check-ignore failed")
        return [p for p in proc.stdout.decode("utf-8").split("\0") if p]

    def glob(self, pattern: str) -> list[str]:
        return [p for p in self.files if match(p, pattern)]


def match(path: str, pattern: str) -> bool:
    """fnmatch where `**/` may also match nothing."""
    if fnmatch.fnmatchcase(path, pattern):
        return True
    return "**/" in pattern and fnmatch.fnmatchcase(path, pattern.replace("**/", ""))


def strip_code(text: str) -> str:
    """Blank out fenced code blocks and inline code, keeping line numbers."""
    lines = text.split("\n")
    out = []
    fence = None
    for line in lines:
        m = FENCE.match(line)
        if fence is None and m:
            fence = m.group(1)[0] * 3
            out.append("")
            continue
        if fence is not None:
            if line.strip().startswith(fence):
                fence = None
            out.append("")
            continue
        out.append(re.sub(r"(`+)[^`]*?\1", "", line))
    return "\n".join(out)


def link_targets(text: str) -> list[tuple[int, str]]:
    """Relative link targets of a Markdown text with their line numbers."""
    text = strip_code(text)
    found = []
    for regex in (INLINE_LINK, REFERENCE_LINK, HTML_ATTR):
        for m in regex.finditer(text):
            target = m.group(1).strip()
            if target.startswith("<") and target.endswith(">"):
                target = target[1:-1].strip()
            if not target or target.startswith("#") or SCHEME.match(target) or target.startswith("//"):
                continue
            found.append((text.count("\n", 0, m.start()) + 1, target))
    return found


def resolve(source: str, target: str) -> str:
    path = urllib.parse.unquote(target.split("#", 1)[0].split("?", 1)[0])
    if not path:
        return ""
    if path.startswith("/"):
        return os.path.normpath(path.lstrip("/"))
    return os.path.normpath(os.path.join(os.path.dirname(source), path))


def check_links(repo: Repo, cfg: dict) -> list[str]:
    ignore = cfg.get("links", {}).get("ignore_files", {})
    problems = []
    for path in repo.files:
        if not path.endswith(".md") or path in ignore:
            continue
        for line, target in link_targets(repo.read(path)):
            resolved = resolve(path, target)
            if resolved and not repo.exists(resolved):
                problems.append(f"{path}:{line}: link `{target}` does not resolve to a tracked file")
    return problems


def check_docs_index(repo: Repo, cfg: dict) -> list[str]:
    conf = cfg.get("docs_index")
    if not conf:
        return []
    index = conf["path"]
    if index not in repo.file_set:
        return [f"{index}: missing (the documentation index)"]
    listed = {resolve(index, target).rstrip("/") for _, target in link_targets(repo.read(index))}
    problems = []
    docs_root = conf.get("docs_root", "docs")
    required = set()
    for path in repo.files:
        if not path.startswith(docs_root + "/") or path == index:
            continue
        rest = path[len(docs_root) + 1:]
        required.add(docs_root + "/" + rest.split("/", 1)[0])
    changes_root = conf.get("openspec_changes")
    if changes_root:
        for path in repo.files:
            if path.startswith(changes_root + "/"):
                name = path[len(changes_root) + 1:].split("/", 1)[0]
                if name != "archive" and (repo.root / changes_root / name).is_dir():
                    required.add(changes_root + "/" + name)
    for entry in sorted(required - listed):
        problems.append(f"{index}: does not list {entry}")
    return problems


def check_docs_whitelist(repo: Repo, cfg: dict) -> list[str]:
    roots = cfg.get("docs_whitelist", {}).get("roots", [])
    tracked = [p for p in repo.files if any(p.startswith(r.rstrip("/") + "/") for r in roots)]
    return [
        f"{path}: tracked but ignored by .gitignore; add a `!{path}` whitelist entry"
        for path in repo.ignored(tracked)
    ]


def check_pins(repo: Repo, cfg: dict) -> list[str]:
    problems = []
    for pin in cfg.get("pins", []):
        name = pin["name"]
        src = pin["source"]
        if src["path"] not in repo.file_set:
            problems.append(f"{src['path']}: missing (source of the {name} pin)")
            continue
        m = re.search(src["pattern"], repo.read(src["path"]), re.M)
        if not m:
            problems.append(f"{src['path']}: no {name} version found")
            continue
        want = m.group(1)
        for copy in pin.get("copies", []):
            path = copy["path"]
            if path not in repo.file_set:
                problems.append(f"{path}: missing (expected a {name} {want} pin)")
                continue
            found = re.findall(copy["pattern"], repo.read(path), re.M)
            if len(found) < copy.get("min_count", 1):
                problems.append(f"{path}: expected {copy.get('min_count', 1)} {name} pin(s) matching /{copy['pattern']}/, found {len(found)}")
            for got in found:
                if got != want:
                    problems.append(f"{path}: {name} {got} differs from {want} in {src['path']}")
    return problems


def contract_names(repo: Repo, contract: dict) -> set[str]:
    sources = []
    for pattern in contract["sources"]:
        sources.extend(repo.glob(pattern))
    excluded = contract.get("exclude", [])
    names = set()
    for path in sources:
        if any(match(path, ex) for ex in excluded):
            continue
        names.update(re.findall(contract["pattern"], repo.read(path)))
    skip = contract.get("skip")
    if skip:
        names = {n for n in names if not re.search(skip, n)}
    return names


def check_contracts(repo: Repo, cfg: dict) -> list[str]:
    problems = []
    for contract in cfg.get("contracts", []):
        doc = contract["doc"]
        if doc not in repo.file_set:
            problems.append(f"{doc}: missing (lists every {contract['item']})")
            continue
        text = repo.read(doc)
        names = contract_names(repo, contract)
        if not names:
            problems.append(f"no {contract['item']} found in {', '.join(contract['sources'])}; update the contract")
        for name in sorted(names):
            if contract.get("match", "code") == "code":
                found = re.search(r"`(?:[A-Za-z0-9_.]*\.)?" + re.escape(name) + r"`", text)
            else:
                found = re.search(r"(?<![A-Za-z0-9_])" + re.escape(name) + r"(?![A-Za-z0-9_])", text)
            if not found:
                problems.append(f"{doc}: does not mention {contract['item']} `{name}`")
    return problems


def forbidden_terms(repo: Repo, cfg: dict, key: str, what: str) -> list[str]:
    """Report matches of the `key` rules outside their allowed paths.

    A rule may limit the files it looks at with `paths` (globs)."""
    problems = []
    rules = [(re.compile(r["pattern"]), r) for r in cfg.get(key, [])]
    if not rules:
        return problems
    for path in repo.files:
        if Path(path).suffix.lower() in BINARY_SUFFIXES or path == cfg.get("_config_path"):
            continue
        text = None
        for regex, rule in rules:
            if rule.get("paths") and not any(match(path, scope) for scope in rule["paths"]):
                continue
            if any(match(path, allowed) for allowed in rule.get("allowed", [])):
                continue
            if text is None:
                raw = (repo.root / path).read_bytes()
                if b"\0" in raw[:8192]:
                    break
                text = raw.decode("utf-8", errors="replace")
            for m in regex.finditer(text):
                line = text.count("\n", 0, m.start()) + 1
                problems.append(f"{path}:{line}: `{m.group(0)}` {what} ({rule['reason']})")
    return problems


def check_retired(repo: Repo, cfg: dict) -> list[str]:
    return forbidden_terms(repo, cfg, "retired", "is retired")


def check_private(repo: Repo, cfg: dict) -> list[str]:
    return forbidden_terms(repo, cfg, "private", "is a private host detail")


def check_memory(repo: Repo, cfg: dict) -> list[str]:
    conf = cfg.get("memory")
    if not conf:
        return []
    path = conf["path"]
    if path not in repo.file_set:
        return [f"{path}: missing"]
    problems = []
    size = (repo.root / path).stat().st_size
    if size > conf["max_bytes"]:
        problems.append(
            f"{path}: {size // 1024} KiB exceeds {conf['max_bytes'] // 1024} KiB; move finished "
            f"entries and older log entries to {conf['archive']} (see its maintenance rules)"
        )
    log_heading = conf["log_heading"]
    lines = strip_code(repo.read(path)).split("\n")
    log_at = [i for i, line in enumerate(lines) if line.strip() == log_heading]
    if len(log_at) != 1:
        problems.append(f"{path}: expected exactly one `{log_heading}` heading, found {len(log_at)}")
    else:
        for i in range(log_at[0] + 1, len(lines)):
            if re.match(r"^#{1,2} ", lines[i]):
                problems.append(f"{path}:{i + 1}: `{lines[i].strip()}` inside the operation log; log entries use `###`")
    return problems


CHECKS = {
    "links": check_links,
    "docs-index": check_docs_index,
    "docs-whitelist": check_docs_whitelist,
    "pins": check_pins,
    "contracts": check_contracts,
    "retired": check_retired,
    "private": check_private,
    "memory": check_memory,
}


def run(root: Path, config: Path, only: list[str] | None = None) -> list[str]:
    cfg = json.loads(config.read_text(encoding="utf-8"))
    try:
        cfg["_config_path"] = str(config.resolve().relative_to(root.resolve()))
    except ValueError:
        cfg["_config_path"] = ""
    repo = Repo(root)
    problems = []
    for name, check in CHECKS.items():
        if only and name not in only:
            continue
        problems.extend(f"[{name}] {p}" for p in check(repo, cfg))
    return problems


def main(argv: list[str] | None = None) -> int:
    here = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n", 1)[0])
    parser.add_argument("--root", type=Path, default=here, help="repository root")
    parser.add_argument("--config", type=Path, help=f"rules (default: <root>/{DEFAULT_CONFIG})")
    parser.add_argument("--check", action="append", choices=sorted(CHECKS), help="run only these checks")
    args = parser.parse_args(argv)
    config = args.config or args.root / DEFAULT_CONFIG
    problems = run(args.root, config, args.check)
    for problem in problems:
        print(problem)
    if problems:
        print(f"{len(problems)} documentation consistency problem(s); see {DEFAULT_CONFIG} and AGENTS.md", file=sys.stderr)
        return 1
    print("documentation consistency: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
