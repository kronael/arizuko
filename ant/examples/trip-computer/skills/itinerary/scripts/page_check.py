#!/usr/bin/env python3
"""Run the trip checker for the sites containing the supplied pages."""
import argparse
from pathlib import Path
import trip_lint


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('pages', nargs='+')
    args = parser.parse_args()
    pages = [Path(p) for p in args.pages]
    if any(not p.is_file() or p.suffix.lower() != '.html' for p in pages):
        parser.error('each page must be an existing HTML file')
    return max(trip_lint.main([str(parent)]) for parent in sorted({p.resolve().parent for p in pages}))


if __name__ == '__main__':
    raise SystemExit(main())
