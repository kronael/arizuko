#!/usr/bin/env python3
"""Stamp the <time> in each doc page's .docs-footer with its last-commit date.

Pre-commit entry. Reads `git log -1 --format=%aI -- <file>` (the date of the
last commit that touched the file) and writes it into the
`<time class="docs-updated" datetime="YYYY-MM-DD">…</time>` element.

Idempotent: re-stamping an unchanged file rewrites the same bytes, so
pre-commit sees no diff. If git has no date for a file (untracked, or git
unavailable), the existing stamp is left untouched — never blanked.

Files are passed by pre-commit (the `files:` filter scopes us to
template/web/pub/**.html). Run standalone with explicit paths to hand-stamp.
"""
import re
import subprocess
import sys

STAMP = re.compile(
    r'(<time class="docs-updated" datetime=")[^"]*(">)[^<]*(</time>)'
)


def git_date(path):
    try:
        out = subprocess.run(
            ["git", "log", "-1", "--format=%aI", "--", path],
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError:
        return None
    iso = out.stdout.strip()
    return iso.split("T", 1)[0] if iso else None


def stamp(path):
    date = git_date(path)
    if not date:
        return False  # no git date — leave the page's existing stamp alone
    with open(path, encoding="utf-8") as f:
        src = f.read()
    new = STAMP.sub(lambda m: m.group(1) + date + m.group(2) + date + m.group(3), src)
    if new == src:
        return False
    with open(path, "w", encoding="utf-8") as f:
        f.write(new)
    return True


def main(argv):
    changed = False
    for path in argv:
        if not path.endswith(".html"):
            continue
        if stamp(path):
            changed = True
            print("stamped", path)
    return 1 if changed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
