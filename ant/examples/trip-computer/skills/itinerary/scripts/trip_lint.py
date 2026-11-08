#!/usr/bin/env python3
"""trip_lint — minimal static linter for a static site (default: ~/public_html/<trip>).

Usage: trip_lint.py DIR [--rule NAME ...] [--quiet]     exit 1 on any ERROR

A rule is one function `check(site)` that yields (level, page, message);
register it with @rule("name"). Pages are parsed once; links are resolved
once into Link(page, line, url, host, path, target, frag, text).
"""
import argparse, html, math, os, re, sys  # noqa: F401
from dataclasses import dataclass
from html.parser import HTMLParser
from urllib.parse import urlparse, parse_qs, unquote

RULES = {}
def rule(name):
    def deco(fn):
        if name in RULES: raise ValueError(f'duplicate rule {name}')
        RULES[name] = fn; return fn
    return deco

@dataclass
class Link:
    page: str; line: int; url: str; host: str; path: str; target: str; frag: str; text: str; in_nav: bool = False

class _Parser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.tags, self.ids, self.links, self._a, self._t = [], set(), [], None, []
        self.heads, self._h, self.nav_lines = {}, None, []
    def handle_starttag(self, tag, attrs):
        a = dict(attrs); self.tags.append((tag, a, self.getpos()[0]))
        if a.get('id'): self.ids.add(a['id'])
        if tag == 'link' and a.get('href'): self.links.append((a['href'], self.getpos()[0], '', False))
        if tag == 'script' and a.get('src'): self.links.append((a['src'], self.getpos()[0], '', False))
        if tag == 'a' and a.get('href'): self._a, self._t = (a['href'], self.getpos()[0]), []
        if tag in ('title', 'h1') and tag not in self.heads: self._h, self.heads[tag] = tag, ''
        if tag == 'nav': self._nav = True
    def handle_data(self, d):
        if self._a: self._t.append(d)
        if self._h: self.heads[self._h] += d
    def handle_endtag(self, tag):
        if tag == 'a' and self._a:
            self.links.append((*self._a, ' '.join(''.join(self._t).split()), getattr(self, '_nav', False))); self._a = None
        if tag == self._h: self._h = None
        if tag == 'nav': self._nav = False

class Page:
    def __init__(self, root, rel):
        self.name = rel; self.src = open(os.path.join(root, rel), encoding='utf-8').read()
        p = _Parser(); p.feed(self.src); p.close()
        self.tags, self.ids, self.raw_links = p.tags, p.ids, p.links
        self.heads = {k: ' '.join(v.split()) for k, v in p.heads.items()}
    def has_class(self, c): return any(c in (a.get('class') or '').split() for _, a, _ in self.tags)

class Site:
    def __init__(self, root):
        self.root = os.path.abspath(root)
        self.pages = {}
        for dp, _, fs in os.walk(self.root):
            for f in fs:
                if f.endswith('.html'):
                    rel = os.path.relpath(os.path.join(dp, f), self.root); self.pages[rel] = Page(self.root, rel)
        self.links = [self._resolve(p, *l) for p in self.pages.values() for l in p.raw_links]
    def _resolve(self, page, url, line, text, in_nav=False):
        try: u = urlparse(url); host = (u.hostname or '').lower()
        except ValueError: return Link(page.name, line, url, 'invalid', '', '', '', text)
        target = ''
        if not u.scheme and not u.netloc and u.path:
            base = '' if u.path.startswith('/') else os.path.dirname(page.name)
            target = os.path.normpath(os.path.join(base, unquote(u.path).lstrip('/')))
            if url.endswith('/') or os.path.isdir(os.path.join(self.root, target)): target = os.path.join(target, 'index.html')
        elif not u.scheme and not u.netloc and u.fragment: target = page.name
        return Link(page.name, line, url, host, u.path, target, unquote(u.fragment), text, in_nav)

def host_is(link, *domains): return any(link.host == d or link.host.endswith('.' + d) for d in domains)
DAY = re.compile(r'(^|/)day\d+(-[\w-]+)?\.html$')

# ---------------------------------------------------------------- rules
@rule('title-h1')
def _(site):
    for p in site.pages.values():
        t, h = p.heads.get('title'), p.heads.get('h1')
        if not t or not h: yield 'ERROR', p.name, 'missing <title> or <h1>'
        elif t != h: yield 'ERROR', p.name, f'title "{t}" ≠ h1 "{h}"'

@rule('internal-links')
def _(site):
    for l in site.links:
        if l.host == 'invalid': yield 'ERROR', l.page, f'line {l.line}: malformed URL {l.url}'; continue
        if not l.target: continue
        if l.target.split(os.sep)[0] == '..':
            yield 'ERROR', l.page, f'line {l.line}: link leaves site root: {l.url}'; continue
        if l.target.endswith('.html') and l.target not in site.pages:
            yield 'ERROR', l.page, f'line {l.line}: {l.url} → no such page'
        elif not l.target.endswith('.html') and not os.path.exists(os.path.join(site.root, l.target)):
            yield 'ERROR', l.page, f'line {l.line}: {l.url} → no such file'
        elif l.frag and l.target in site.pages and l.frag not in site.pages[l.target].ids:
            yield 'ERROR', l.page, f'line {l.line}: {l.url} → no id "{l.frag}"'

@rule('nav')
def _(site):
    days = sorted(n for n in site.pages if DAY.search(n))
    labels = {}
    for n in days:
        p = site.pages[n]; navs = [ln for t, a, ln in p.tags if t == 'nav']
        if len(navs) != 1: yield 'ERROR', n, f'{len(navs)} <nav> elements'; continue
        seen = {n}
        for l in site.links:
            if l.page == n and l.in_nav and l.target in site.pages:
                seen.add(l.target); labels.setdefault(l.target, set()).add(l.text)
        for other in days:
            if other not in seen: yield 'ERROR', n, f'nav has no link to {other}'
    for target, labs in labels.items():
        if len(labs) > 1: yield 'ERROR', target, f'nav label differs between pages: {sorted(labs)}'

@rule('index-coverage')
def _(site):
    for idx in [n for n in site.pages if os.path.basename(n) == 'index.html']:
        d = os.path.dirname(idx)
        carded = {l.target for l in site.links if l.page == idx}
        for n in site.pages:
            if DAY.search(n) and os.path.dirname(n) == d and n not in carded:
                yield 'ERROR', idx, f'{n} has no card'

def _num(q, k):
    try: v = float(q.get(k, [''])[0]); return v if math.isfinite(v) else None
    except ValueError: return None

@rule('mapy-links')
def _(site):
    import mapyrc
    for l in site.links:
        if not host_is(l, 'mapy.com', 'mapy.cz'): continue
        if host_is(l, 'mapy.cz'): yield 'ERROR', l.page, f'line {l.line}: use mapy.com'; continue
        q = parse_qs(urlparse(l.url).query, keep_blank_values=True); where = f'line {l.line}'
        if 'q' in q: yield 'ERROR', l.page, f'{where}: ?q= opens a search, not the place'; continue
        if _num(q, 'x') is None or _num(q, 'y') is None: yield 'ERROR', l.page, f'{where}: missing or bad x/y'
        if 'planovani-trasy' in l.url:
            rs, ri = q.get('rs', []), q.get('ri', [])
            try: n = len(mapyrc.decode(q.get('rc', [''])[0]))
            except Exception as e: yield 'ERROR', l.page, f'{where}: rc= does not decode ({e})'; continue
            if not (n == len(rs) == len(ri)): yield 'ERROR', l.page, f'{where}: {n} points, {len(rs)} rs, {len(ri)} ri'
            if any(source != 'coor' and not ident for source, ident in zip(rs, ri)): yield 'ERROR', l.page, f'{where}: named route point has no id'
            if sum(r == 'coor' for r in rs) > 1: yield 'WARN', l.page, f'{where}: route has unnamed (coor) stops'
        elif 'source' not in q: continue                                   # plain map view at x/y
        elif q.get('source', [''])[0] not in ('osm', 'base', 'firm', 'pubt', 'coor'):
            yield 'ERROR', l.page, f'{where}: unknown source= "{q.get("source", [""])[0]}"'
        elif q.get('source') == ['coor']:
            yield 'WARN', l.page, f'{where}: GPS pin, not a place ("{l.text}")'

@rule('google-links')
def _(site):
    for l in site.links:
        if not host_is(l, 'google.com') or not l.path.startswith('/maps'): continue
        if l.path.startswith('/maps/search/'): yield 'WARN', l.page, f'line {l.line}: search link, not a place ("{l.text}")'

@rule('tel-links')
def _(site):
    for p in site.pages.values():
        body = re.sub(r'<a\b[^>]*href=["\']tel:[^>]*>.*?</a>', '', p.src, flags=re.S)
        for m in re.finditer(r'\+\d{2}(?:[  ]?\d){8,12}', re.sub(r'<[^>]+>', ' ', body)):
            yield 'ERROR', p.name, f'phone not a tel: link: {m.group(0)}'

@rule('meal-lines')
def _(site):
    for p in site.pages.values():
        for ul in re.findall(r'<ul\b[^>]*class=["\'][^"\']*\bfood-list\b[^"\']*["\'][^>]*>(.*?)</ul>', p.src, re.S):
            for li in re.findall(r'<li\b[^>]*>(.*?)</li>', ul, re.S):
                t = ' '.join(html.unescape(re.sub(r'<[^>]+>', '', li)).split()); head = t.split('·')[0]
                hrefs = re.findall(r'href=["\']([^"\']+)', li)
                if len(head.split()) > 10: yield 'ERROR', p.name, f'meal line {len(head.split())} words: {head[:40]}'
                if not re.search(r'·\s*(?:Google\s+)?[1-5]\.\d\b|\bunrated\b', t): yield 'ERROR', p.name, f'meal line without rating: {head[:40]}'
                if not any(h.startswith('tel:') for h in hrefs) and 'phone: not found' not in t: yield 'ERROR', p.name, f'meal line without tel: link: {head[:40]}'
                if not any('google.com/maps/' in h for h in hrefs): yield 'ERROR', p.name, f'meal line without Google link: {head[:40]}'
                if not any('mapy.com/' in h for h in hrefs): yield 'ERROR', p.name, f'meal line without mapy link: {head[:40]}'
                if any('source=coor' in h for h in hrefs): yield 'ERROR', p.name, f'meal line uses GPS pin: {head[:40]}'

@rule('day-blocks')
def _(site):
    for n, p in site.pages.items():
        if not DAY.search(n): continue
        for c in ('checked-line', 'route-overview', 'map-links', 'timeline', 'source-note'):
            if not p.has_class(c): yield 'ERROR', n, f'missing .{c}'
        for t, a, ln in p.tags:
            if t == 'iframe' and a.get('loading') == 'lazy': yield 'ERROR', n, f'line {ln}: lazy iframe renders blank on phones'
        if len(re.findall(r'<div\b', p.src)) != p.src.count('</div>'): yield 'ERROR', n, 'unbalanced div elements'
        if sum(1 for _, a, _ in p.tags if 'checked-line' in (a.get('class') or '').split()) != 1: yield 'ERROR', n, 'expected one checked-line'
        if any(t == 'a' and a.get('href') == '' for t, a, _ in p.tags): yield 'ERROR', n, 'empty href'
        if re.search(r'<h[23]>\s*(Food|Breakfast|Lunch|Coffee|Dinner)\b', p.src): yield 'ERROR', n, 'meal heading outside timeline'
        for meal in ('Breakfast', 'Lunch', 'Coffee', 'Dinner'):
            if not re.search(r'class=["\'][^"\']*\btimeline-date\b[^"\']*["\'][^>]*>\s*\d{1,2}:\d{2}\s*[—–-]\s*' + meal + r'\b', p.src):
                yield 'ERROR', n, f'missing meal item: {meal}'

# ---------------------------------------------------------------- runner
def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument('dir'); ap.add_argument('--rule', action='append', default=[])
    ap.add_argument('--quiet', action='store_true', help='errors only')
    a = ap.parse_args(argv)
    unknown = set(a.rule) - set(RULES)
    if unknown: ap.error(f'unknown rule(s): {", ".join(sorted(unknown))}; known: {", ".join(RULES)}')
    if not os.path.isdir(a.dir): ap.error(f'not a directory: {a.dir}')
    site = Site(a.dir); out = []
    if not site.pages: ap.error("site has no HTML pages")
    for name in a.rule or RULES:
        out += [(lvl, page, name, msg) for lvl, page, msg in RULES[name](site)]
    for lvl, page, name, msg in sorted(out, key=lambda f: (f[1], f[0], f[2])):
        if not (a.quiet and lvl != 'ERROR'): print(f'{lvl:5} {page:20} {name:15} {msg}')
    e = sum(f[0] == 'ERROR' for f in out)
    print(f'-- {len(a.rule or RULES)} rules, {len(site.pages)} pages, {e} errors, {len(out) - e} warnings')
    return 1 if e else 0

if __name__ == '__main__': sys.exit(main())
