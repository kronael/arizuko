#!/usr/bin/env python3
"""catalogue — write the by-type, by-date archive page of a published site.

Usage: catalogue.py SITE --ns NS --types T1,T2,... --title TEXT [--out FILE]

Lists every page that carries `<meta name="NS:type">`, grouped by type in the
order of --types, newest first (`NS:date`). Redirect stubs and unstamped pages
are skipped. A type missing from --types exits 1 and names the pages. The page
goes to SITE/archive/index.html unless --out says otherwise; the output file
itself is never listed. Needs jinja2 (`uv run --with jinja2 catalogue.py ...`).
"""

import argparse
import datetime
import os
import sys
from dataclasses import dataclass
from html.parser import HTMLParser
from pathlib import Path

from jinja2 import Environment, FileSystemLoader, select_autoescape

COMPONENTS = Path(__file__).resolve().parents[1] / 'components'
DEFAULT_OUT = 'archive/index.html'


@dataclass
class Entry:
    path: str
    type: str
    date: str
    title: str


class _Done(Exception):
    pass


class _Head(HTMLParser):
    def __init__(self, ns: str):
        super().__init__()
        self.ns = ns
        self.type: str | None = None
        self.date = ''
        self.title = ''
        self.refresh = False
        self.in_title = False

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]):
        a = {k: v or '' for k, v in attrs}
        if tag == 'body':
            raise _Done
        if tag == 'title':
            self.in_title = True
        if tag != 'meta':
            return
        if a.get('http-equiv', '').lower() == 'refresh':
            self.refresh = True
        if a.get('name') == f'{self.ns}:type':
            self.type = a.get('content', '').strip()
        elif a.get('name') == f'{self.ns}:date':
            self.date = a.get('content', '').strip()

    def handle_endtag(self, tag: str):
        if tag == 'title':
            self.in_title = False

    def handle_data(self, data: str):
        if self.in_title:
            self.title += data


def read_entry(path: Path, rel: str, ns: str) -> Entry | None:
    parser = _Head(ns)
    try:
        parser.feed(path.read_text(encoding='utf-8', errors='replace'))
    except _Done:
        pass
    if parser.type is None or parser.refresh:
        return None
    return Entry(rel, parser.type, parser.date, ' '.join(parser.title.split()) or rel)


def collect(site: Path, ns: str, out: Path) -> list[Entry]:
    entries: list[Entry] = []
    for dirpath, _, files in os.walk(site):
        for f in files:
            path = Path(dirpath) / f
            if not f.endswith('.html') or path == out:
                continue
            entry = read_entry(path, path.relative_to(site).as_posix(), ns)
            if entry:
                entries.append(entry)
    return entries


def page_url(rel: str, base: Path, site: Path) -> str:
    target = os.path.relpath(site / rel, base)
    return target.removesuffix('index.html') if rel.endswith('index.html') else target


def render(site: Path, out: Path, entries: list[Entry], types: list[str], ns: str, title: str) -> str:
    base = out.parent
    sections = []
    for t in types:
        rows = sorted((e for e in entries if e.type == t), key=lambda e: (e.date, e.path), reverse=True)
        if rows:
            sections.append({
                'type': t,
                'label': t.capitalize(),
                'entries': [{'date': e.date, 'title': e.title, 'url': page_url(e.path, base, site)} for e in rows],
            })
    env = Environment(loader=FileSystemLoader(COMPONENTS), autoescape=select_autoescape(['html', 'j2']))
    return env.get_template('catalogue.html.j2').render(
        ns=ns,
        title=title,
        generated=datetime.datetime.now(datetime.UTC).date().isoformat(),
        total=len(entries),
        sections=sections,
        css=os.path.relpath(site / 'assets/hub.css', base),
        js=os.path.relpath(site / 'assets/hub.js', base),
        home=os.path.relpath(site, base) + '/',
    )


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description='Write the by-type archive page of a published site.')
    ap.add_argument('site', type=Path)
    ap.add_argument('--ns', required=True, help='page metadata namespace')
    ap.add_argument('--types', required=True, help='comma-separated type order')
    ap.add_argument('--title', required=True)
    ap.add_argument('--out', type=Path, help=f'output file (default SITE/{DEFAULT_OUT})')
    args = ap.parse_args(argv)
    site: Path = args.site.resolve()
    if not site.is_dir():
        print(f'catalogue: no such directory {site}', file=sys.stderr)
        return 1
    out = (args.out or site / DEFAULT_OUT).resolve()
    types = [t.strip() for t in args.types.split(',') if t.strip()]
    entries = collect(site, args.ns, out)
    unknown = [e for e in entries if e.type not in types]
    if unknown:
        for e in unknown:
            print(f'catalogue: {e.path}: type {e.type!r} is not in --types', file=sys.stderr)
        return 1
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(render(site, out, entries, types, args.ns, args.title), encoding='utf-8')
    print(f'catalogue: {len(entries)} pages -> {out}')
    return 0


if __name__ == '__main__':
    sys.exit(main())
