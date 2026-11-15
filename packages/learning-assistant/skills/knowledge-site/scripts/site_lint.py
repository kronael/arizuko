#!/usr/bin/env python3
"""site_lint — static linter for a published knowledge site.

Usage: site_lint.py SITE --ns NS [--www DIR [--vhost FOLDER]] [--rule NAME ...]     exit 1 on any finding

SITE holds the pages to check. NS is the site's page-metadata namespace
(`<meta name="NS:type">`). Root-relative links (`/pub/x/y.html`, `/x/y.html`)
resolve under --www, the directory every published tree is served from
(default /var/lib/www); a leading `pub/` segment is optional there. A relative
link must stay inside --www, or inside SITE when --www is not given.
--vhost names the site folder under --www that a mapped vhost serves: the server
sends a root-relative path outside /pub/ to that folder and a /pub/ path found
nowhere else to the same folder, and the linter resolves the same way.

A rule is one function `check(site)` that yields Finding(page, message);
register it with @rule("name"). Every finding is an error.
"""

import argparse
import datetime
import os
import re
import stat
import sys
from collections import Counter
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlparse

DEFAULT_WWW = '/var/lib/www'
NOT_STATIC = {'priv', 'dash', 'chat', 'api', 'x', 'auth'}
TAILWIND = 'cdn.tailwindcss.com'
CODE_LANGUAGE = 'language-'
VOID = {'area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'param', 'source', 'track', 'wbr'}
OPTIONAL_END = {
    'html', 'head', 'body', 'p', 'li', 'dt', 'dd', 'tr', 'td', 'th', 'thead', 'tbody', 'tfoot',
    'option', 'optgroup', 'colgroup', 'caption', 'rt', 'rp',
}
RAW_TEXT = {'textarea', 'title'}
PAGE_SUFFIXES = ('.html', '.htm')
LINK_ATTR = {
    'a': 'href',
    'link': 'href',
    'script': 'src',
    'img': 'src',
    'source': 'src',
    'iframe': 'src',
    'video': 'src',
    'audio': 'src',
}


def is_regular(path: Path) -> bool:
    try:
        return stat.S_ISREG(os.stat(path).st_mode)
    except (OSError, ValueError):
        return False


def is_directory(path: Path) -> bool:
    try:
        return stat.S_ISDIR(os.stat(path).st_mode)
    except (OSError, ValueError):
        return False


@dataclass
class Finding:
    page: str
    message: str


@dataclass
class Link:
    line: int
    url: str
    kind: str


@dataclass
class ClassUse:
    line: int
    name: str


@dataclass
class Page:
    name: str
    path: Path
    ids: set[str] = field(default_factory=set)
    links: list[Link] = field(default_factory=list)
    classes: list[ClassUse] = field(default_factory=list)
    inline_css: str = ''
    inline_js: str = ''
    title: str = ''
    h1: str = ''
    has_h1: bool = False
    refresh: str | None = None
    refresh_line: int = 0
    metas: dict[str, str] = field(default_factory=dict)
    imbalance: str = ''


class _Parser(HTMLParser):
    def __init__(self, page: Page):
        super().__init__(convert_charrefs=True)
        self.page = page
        self.stack: list[tuple[str, int]] = []
        self.raw = ''
        self.in_title = False
        self.title_done = False
        self.h1_depth = 0
        self.open: Counter[str] = Counter()

    def fail(self, message: str):
        if not self.page.imbalance:
            self.page.imbalance = message

    def collect(self, tag: str, attrs: list[tuple[str, str | None]]):
        a = {k: v or '' for k, v in attrs}
        page = self.page
        line = self.getpos()[0]
        if a.get('id'):
            page.ids.add(a['id'])
        if tag == 'a' and a.get('name'):
            page.ids.add(a['name'])
        for name in a.get('class', '').split():
            page.classes.append(ClassUse(line, name))
        key = LINK_ATTR.get(tag)
        if key and key in dict(attrs):
            kind = tag
            if tag == 'link' and 'stylesheet' in a.get('rel', '').lower().split():
                kind = 'stylesheet'
            page.links.append(Link(line, a[key], kind))
        if tag == 'meta' and a.get('name') and 'content' in a:
            page.metas.setdefault(a['name'], a['content'])
        if tag == 'meta' and a.get('http-equiv', '').lower() == 'refresh':
            found = re.search(r'url\s*=\s*[\'"]?([^\'";]+)', a.get('content', ''), re.IGNORECASE)
            if found:
                page.refresh = found[1].strip()
                page.refresh_line = line
        if tag in ('script', 'style'):
            self.raw = tag
        if tag == 'title' and not self.title_done:
            self.in_title = True
        if tag == 'h1':
            page.has_h1 = True
            self.h1_depth += 1

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]):
        self.collect(tag, attrs)
        if tag not in VOID:
            self.stack.append((tag, self.getpos()[0]))
            self.open[tag] += 1
        if tag in RAW_TEXT:
            self.set_cdata_mode(tag)

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, str | None]]):
        self.collect(tag, attrs)
        if tag in ('script', 'style'):
            self.raw = ''

    def handle_data(self, data: str):
        if self.raw == 'style':
            self.page.inline_css += data
        elif self.raw == 'script':
            self.page.inline_js += data + '\n'
        if self.in_title:
            self.page.title += data
        if self.h1_depth:
            self.page.h1 += data

    def handle_endtag(self, tag: str):
        if tag in VOID:
            return
        if tag in ('script', 'style'):
            self.raw = ''
        if tag == 'title' and self.in_title:
            self.in_title = False
            self.title_done = True
        if tag == 'h1' and self.h1_depth:
            self.h1_depth -= 1
        if not self.open[tag]:
            if tag != 'p':
                self.fail(f'line {self.getpos()[0]}: unexpected </{tag}>')
            return
        while self.stack:
            top, line = self.stack.pop()
            self.open[top] -= 1
            if top == tag:
                return
            if top not in OPTIONAL_END:
                self.fail(f'line {line}: unclosed <{top}>')


def parse_page(name: str, path: Path) -> Page:
    page = Page(name, path)
    parser = _Parser(page)
    parser.feed(read_text(path))
    parser.close()
    for tag, line in parser.stack:
        if tag not in OPTIONAL_END:
            parser.fail(f'line {line}: unclosed <{tag}>')
    return page


@dataclass
class Resolution:
    path: Path | None = None
    frag: str = ''
    error: str = ''


class Site:
    def __init__(self, scope: Path, www: Path, ns: str, fence: Path | None = None, vhost: str = ''):
        self.root = scope
        self.www = www
        self.fence = fence or scope
        self.vhost = vhost
        self.ns = ns
        self.cache: dict[Path, Page] = {}
        self.pages: dict[str, Page] = {}
        for dirpath, _, files in os.walk(scope):
            for f in sorted(files):
                path = Path(dirpath) / f
                if f.lower().endswith(PAGE_SUFFIXES) and is_regular(path):
                    self.pages[path.relative_to(scope).as_posix()] = self.parse_at(path)

    def parse_at(self, path: Path) -> Page:
        if path not in self.cache:
            name = path.relative_to(self.root).as_posix() if path.is_relative_to(self.root) else str(path)
            self.cache[path] = parse_page(name, path)
        return self.cache[path]

    def resolve(self, page: Page, link: Link) -> Resolution:
        url = link.url.strip()
        if not url:
            return Resolution(error='empty URL')
        try:
            u = urlparse(url)
        except ValueError:
            return Resolution(error=f'malformed URL {url}')
        if u.scheme or u.netloc:
            return Resolution()
        frag = unquote(u.fragment)
        path = unquote(u.path)
        if not path:
            return Resolution(page.path, frag)
        if path.startswith('/'):
            rel = path.lstrip('/')
            if rel.split('/')[0] in NOT_STATIC:
                return Resolution()
            if not self.www.is_dir():
                return Resolution(error=f'{url} is root-relative and --www {self.www} is not a directory')
            if self.vhost and rel.startswith('pub/'):
                candidates = [(rel[4:], self.www), (f'{self.vhost}/{rel[4:]}', self.www / self.vhost)]
            elif self.vhost:
                candidates = [(f'{self.vhost}/{rel}', self.www / self.vhost)]
            else:
                candidates = [(rel, self.www)]
                if rel.startswith('pub/'):
                    candidates.append((rel[4:], self.www))
            base = self.www
            where = '--www'
        else:
            candidates = [(path, self.fence)]
            base = page.path.parent
            where = '--www' if self.fence != self.root else 'the site'
        for rel, fence in candidates:
            target = Path(os.path.normpath(base / rel))
            if not target.is_relative_to(fence):
                return Resolution(error=f'{url} leaves {where}')
            if path.endswith('/') or is_directory(target):
                target = target / 'index.html'
            if is_regular(target):
                return Resolution(target, frag)
        kind = 'page' if Path(path.rstrip('/')).suffix.lower() in ('', *PAGE_SUFFIXES) else 'file'
        return Resolution(error=f'{url} → no such {kind}')

    def local_file(self, page: Page, link: Link) -> Path | None:
        res = self.resolve(page, link)
        return res.path if res.path and not res.error else None


Check = Callable[[Site], Iterator[Finding]]
RULES: dict[str, Check] = {}


def rule(name: str) -> Callable[[Check], Check]:
    def deco(fn: Check) -> Check:
        if name in RULES:
            raise ValueError(f'duplicate rule {name}')
        RULES[name] = fn
        return fn

    return deco


def is_stub(page: Page) -> bool:
    return page.refresh is not None


@rule('html-balance')
def check_html_balance(site: Site) -> Iterator[Finding]:
    for page in site.pages.values():
        if page.imbalance:
            yield Finding(page.name, page.imbalance)


@rule('title-h1')
def check_title_h1(site: Site) -> Iterator[Finding]:
    for page in site.pages.values():
        if is_stub(page):
            continue
        if not page.title.strip():
            yield Finding(page.name, 'missing or empty <title>')
        if not page.has_h1:
            yield Finding(page.name, 'missing <h1>')


@rule('links')
def check_links(site: Site) -> Iterator[Finding]:
    for page in site.pages.values():
        for link in page.links:
            res = site.resolve(page, link)
            if res.error:
                yield Finding(page.name, f'line {link.line}: {res.error}')
            elif (
                res.path
                and res.frag not in ('', 'top')
                and res.path.suffix.lower() in PAGE_SUFFIXES
                and res.frag not in site.parse_at(res.path).ids
            ):
                yield Finding(page.name, f'line {link.line}: {link.url} → no id "{res.frag}"')


CSS_CLASS = re.compile(r'\.((?:[a-zA-Z_]|\\.)(?:[\w-]|\\.)*)')
CSS_ATTR_CLASS = re.compile(r'\[\s*class\s*[~|^$*]?=\s*["\']?([^"\'\]\s]+)')
JS_STRING = re.compile(r'\'([^\'\\\n]*)\'|"([^"\\\n]*)"|`([^`]*)`')


def css_classes(css: str) -> set[str]:
    css = re.sub(r'/\*.*?\*/', '', css, flags=re.DOTALL)
    escaped = {re.sub(r'\\(.)', r'\1', name) for name in CSS_CLASS.findall(css)}
    return escaped | set(CSS_ATTR_CLASS.findall(css))


def js_tokens(js: str) -> set[str]:
    tokens: set[str] = set()
    for m in JS_STRING.finditer(js):
        for s in m.groups():
            if s:
                tokens.update(s.split())
        if m[3]:
            tokens |= js_tokens(m[3])
    return tokens


def is_remote(url: str) -> bool:
    u = urlparse(url.strip())
    return bool(u.scheme or u.netloc)


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding='utf-8', errors='replace')
    except OSError:
        return ''


@rule('css-class')
def check_css_class(site: Site) -> Iterator[Finding]:
    for page in site.pages.values():
        if any(TAILWIND in link.url or (link.kind == 'stylesheet' and is_remote(link.url)) for link in page.links):
            continue
        defined = css_classes(page.inline_css) | js_tokens(page.inline_js)
        unresolved = False
        for link in page.links:
            if link.kind not in ('stylesheet', 'script') or is_remote(link.url):
                continue
            path = site.local_file(page, link)
            if not path:
                unresolved = True
            else:
                if link.kind == 'stylesheet':
                    defined |= css_classes(read_text(path))
                else:
                    defined |= js_tokens(read_text(path))
        if unresolved:
            continue
        seen: set[str] = set()
        for use in page.classes:
            if use.name not in defined and use.name not in seen and not use.name.startswith(CODE_LANGUAGE):
                seen.add(use.name)
                yield Finding(page.name, f'line {use.line}: undefined class {use.name}')


META_TYPE = re.compile(r'[a-z][a-z-]*')


@rule('page-meta')
def check_page_meta(site: Site) -> Iterator[Finding]:
    for page in site.pages.values():
        if is_stub(page):
            continue
        kind = page.metas.get(f'{site.ns}:type')
        if kind is None or not META_TYPE.fullmatch(kind):
            yield Finding(page.name, f'{site.ns}:type missing or not [a-z][a-z-]*: {kind!r}')
        date = page.metas.get(f'{site.ns}:date')
        try:
            if date is None or not re.fullmatch(r'\d{4}-\d{2}-\d{2}', date):
                raise ValueError(date)
            datetime.date.fromisoformat(date)
        except ValueError:
            yield Finding(page.name, f'{site.ns}:date missing or not a valid YYYY-MM-DD: {date!r}')


def is_redirect_loop(site: Site, page: Page, target: Path) -> bool:
    seen = {page.path}
    while target not in seen:
        seen.add(target)
        stub = site.parse_at(target)
        if not is_stub(stub):
            return False
        res = site.resolve(stub, Link(stub.refresh_line, stub.refresh or '', 'refresh'))
        if not res.path:
            return False
        target = res.path
    return target == page.path


@rule('redirect-stub')
def check_redirect_stub(site: Site) -> Iterator[Finding]:
    for page in site.pages.values():
        if not is_stub(page):
            continue
        res = site.resolve(page, Link(page.refresh_line, page.refresh or '', 'refresh'))
        if res.path and is_redirect_loop(site, page, res.path):
            yield Finding(page.name, f'line {page.refresh_line}: refresh target {page.refresh} leads back to this page')
        elif res.error:
            yield Finding(page.name, f'line {page.refresh_line}: refresh target: {res.error}')
        elif not res.path:
            yield Finding(page.name, f'line {page.refresh_line}: refresh target {page.refresh} is not a local page')
        elif not any(
            link.kind == 'a' and site.resolve(page, link).path == res.path for link in page.links
        ):
            yield Finding(page.name, f'line {page.refresh_line}: body does not link the refresh target {page.refresh}')


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or '').splitlines()[0])
    ap.add_argument('site')
    ap.add_argument('--ns', required=True)
    ap.add_argument('--www')
    ap.add_argument('--vhost', default='')
    ap.add_argument('--rule', action='append', default=[])
    a = ap.parse_args(argv)
    unknown = set(a.rule) - set(RULES)
    if unknown:
        ap.error(f'unknown rule(s): {", ".join(sorted(unknown))}; known: {", ".join(RULES)}')
    if not os.path.isdir(a.site):
        ap.error(f'not a directory: {a.site}')
    root = Path(a.site).resolve()
    www = Path(a.www or DEFAULT_WWW).resolve()
    if a.vhost and (not a.www or '/' in a.vhost or not is_directory(www / a.vhost)):
        ap.error(f'--vhost {a.vhost} must be a folder directly under --www')
    site = Site(root, www, a.ns, www if a.www else root, a.vhost)
    if not site.pages:
        ap.error('site has no HTML pages')
    names = a.rule or list(RULES)
    found = [(f.page, name, f.message) for name in names for f in RULES[name](site)]
    for page, name, message in sorted(found):
        print(f'ERROR {page} {name} {message}')
    counts = ' '.join(f'{n}={sum(1 for _, r, _ in found if r == n)}' for n in names)
    print(f'-- {len(names)} rules, {len(site.pages)} pages, {len(found)} errors: {counts}')
    return 1 if found else 0


if __name__ == '__main__':
    sys.exit(main())
