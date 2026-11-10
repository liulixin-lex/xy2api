#!/usr/bin/env python3
"""Print the CHANGELOG.md section used as the notes of a GitHub release.

    python3 tools/ci/changelog_section.py 0.6.0        # section "## [0.6.0] - <date>"
    python3 tools/ci/changelog_section.py 0.6.0-rc.1   # the "## [Unreleased]" section

A final release needs its own non-empty section. A release candidate uses the
Unreleased section, which must not be empty either. Exit status 1 otherwise.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

CHANGELOG = Path(__file__).resolve().parents[2] / "CHANGELOG.md"
VERSION = re.compile(r"^\d+\.\d+\.\d+(-rc\.\d+)?$")
HEADING = re.compile(r"^## \[([^\]]+)\]")


def section(text: str, name: str) -> str | None:
    lines: list[str] = []
    inside = False
    for line in text.splitlines():
        heading = HEADING.match(line)
        if heading:
            if inside:
                break
            inside = heading.group(1) == name
            continue
        if inside:
            if line.startswith("[") and "]: " in line:  # link reference definitions at the end
                break
            lines.append(line)
    if not inside and not lines:
        return None
    return "\n".join(lines).strip()


def notes(text: str, version: str) -> str:
    if not VERSION.match(version):
        raise ValueError(f"not a release version: {version}")
    if "-rc." in version:
        body = section(text, "Unreleased")
        if not body:
            raise ValueError("the Unreleased section is empty; describe the release candidate there")
        base = version.split("-rc.")[0]
        return f"{base} 的候选版本。\n\n{body}"
    body = section(text, version)
    if not body:
        raise ValueError(f"CHANGELOG.md has no non-empty section for [{version}]")
    return body


def main(argv: list[str]) -> int:
    if len(argv) != 1:
        print(__doc__, file=sys.stderr)
        return 2
    try:
        print(notes(CHANGELOG.read_text(encoding="utf-8"), argv[0]))
    except ValueError as err:
        print(f"error: {err}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
