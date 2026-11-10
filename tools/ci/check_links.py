#!/usr/bin/env python3
"""Check relative links and anchors in the repository's Markdown files.

    python3 tools/ci/check_links.py

External links (http, https, mailto) are not fetched. A relative link must
point at an existing file or directory; a "#fragment" must match a heading of
the target Markdown file (GitHub's anchor rules). Exit status 1 on any broken
link.
"""

from __future__ import annotations

import re
import subprocess
import sys
import unicodedata
from pathlib import Path
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[2]
LINK = re.compile(r"(?<!!)\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)|^\[[^\]]+\]:\s*(\S+)", re.M)
HEADING = re.compile(r"^(#{1,6})\s+(.+?)\s*#*\s*$", re.M)
FENCE = re.compile(r"^(```|~~~).*?^\1", re.M | re.S)
SKIP_PREFIXES = ("http://", "https://", "mailto:", "tel:")


def markdown_files() -> list[Path]:
    out = subprocess.run(["git", "ls-files", "-co", "--exclude-standard", "*.md"], cwd=ROOT,
                         capture_output=True, text=True, check=True).stdout
    return [ROOT / p for p in out.splitlines() if "node_modules/" not in p and (ROOT / p).is_file()]


def slug(text: str) -> str:
    text = re.sub(r"<[^>]+>", "", text)
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)  # keep link text only
    text = text.replace("`", "").strip().lower()
    kept = []
    for ch in text:
        cat = unicodedata.category(ch)
        if ch in " -":
            kept.append("-" if ch == " " else ch)
        elif ch == "_" or cat[0] in ("L", "N") or cat == "Mn":
            kept.append(ch)
    return "".join(kept)


def anchors(path: Path, cache: dict[Path, set[str]]) -> set[str]:
    if path not in cache:
        text = FENCE.sub("", path.read_text(encoding="utf-8"))
        seen: dict[str, int] = {}
        found = set()
        for _, title in HEADING.findall(text):
            base = slug(title)
            n = seen.get(base, 0)
            found.add(base if n == 0 else f"{base}-{n}")
            seen[base] = n + 1
        found.update(re.findall(r'<a\s+(?:name|id)="([^"]+)"', text))
        cache[path] = found
    return cache[path]


def main() -> int:
    cache: dict[Path, set[str]] = {}
    problems = []
    for md in markdown_files():
        text = FENCE.sub("", md.read_text(encoding="utf-8"))
        for match in LINK.finditer(text):
            target = match.group(1) or match.group(2)
            if not target or target.startswith(SKIP_PREFIXES):
                continue
            path_part, _, fragment = target.partition("#")
            dest = md if not path_part else (md.parent / unquote(path_part)).resolve()
            rel = md.relative_to(ROOT)
            if not dest.exists():
                problems.append(f"{rel}: missing target {target}")
                continue
            if fragment and dest.suffix == ".md" and unquote(fragment).lower() not in anchors(dest, cache):
                problems.append(f"{rel}: missing anchor {target}")
    for problem in problems:
        print(problem)
    print(f"checked {len(markdown_files())} Markdown files, {len(problems)} broken links")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
