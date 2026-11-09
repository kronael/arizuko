#!/usr/bin/env python3
"""trip_lint — minimal static linter for a static site (default: ~/public_html/<trip>).

Usage: trip_lint.py DIR [--root PUBLIC_DIR] [--rule NAME ...] [--quiet]     exit 1 on any ERROR

DIR holds the pages to check. Local links resolve inside --root (default DIR),
the public directory DIR is published under, e.g. ~/public_html.

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
    page: str; line: int; url: str; host: str; path: str; target: str; frag: str; text: str; in_nav: bool = False; cls: str = ''

VOID = set('area base br col embed hr img input link meta param source track wbr'.split())

class _Parser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.tags, self.ids, self.links, self._a, self._t = [], set(), [], None, []
        self.heads, self._h, self.nav_lines = {}, None, []
        self.stack, self.balance = [], []
    def handle_starttag(self, tag, attrs):
        a = dict(attrs); self.tags.append((tag, a, self.getpos()[0]))
        if tag not in VOID: self.stack.append((tag, self.getpos()[0]))
        if a.get('id'): self.ids.add(a['id'])
        if tag == 'link' and a.get('href'): self.links.append((a['href'], self.getpos()[0], '', False))
        if tag in ('script', 'img') and a.get('src'): self.links.append((a['src'], self.getpos()[0], '', False))
        if tag == 'a' and 'href' in a: self._a, self._t = (a['href'] or '', self.getpos()[0], a.get('class') or ''), []
        if tag in ('title', 'h1') and tag not in self.heads: self._h, self.heads[tag] = tag, ''
        if tag == 'nav': self._nav = True
    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)
        if tag not in VOID: self.handle_endtag(tag)
    def handle_data(self, d):
        if self._a: self._t.append(d)
        if self._h: self.heads[self._h] += d
    def handle_endtag(self, tag):
        if tag in VOID: self.balance.append(f'line {self.getpos()[0]}: unexpected </{tag}>')
        elif not self.stack or self.stack[-1][0] != tag:
            self.balance.append(f'line {self.getpos()[0]}: unexpected </{tag}>')
        else: self.stack.pop()
        if tag == 'a' and self._a:
            href, line, cls = self._a
            self.links.append((href, line, ' '.join(''.join(self._t).split()), getattr(self, '_nav', False), cls)); self._a = None
        if tag == self._h: self._h = None
        if tag == 'nav': self._nav = False

class Page:
    def __init__(self, root, rel):
        self.name = rel; self.src = open(os.path.join(root, rel), encoding='utf-8').read()
        p = _Parser(); p.feed(self.src); p.close()
        self.tags, self.ids, self.raw_links = p.tags, p.ids, p.links
        self.balance = p.balance + [f'line {line}: unclosed <{tag}>' for tag, line in p.stack]
        self.heads = {k: ' '.join(v.split()) for k, v in p.heads.items()}
    def has_class(self, c): return any(c in (a.get('class') or '').split() for _, a, _ in self.tags)

class Site:
    def __init__(self, scope, root):
        self.root = os.path.abspath(root)
        self.pages = {}
        for dp, _, fs in os.walk(os.path.abspath(scope)):
            for f in fs:
                if f.endswith('.html'):
                    rel = os.path.relpath(os.path.join(dp, f), self.root); self.pages[rel] = Page(self.root, rel)
        self.links = [self._resolve(p, *l) for p in self.pages.values() for l in p.raw_links]
    def _resolve(self, page, url, line, text, in_nav=False, cls=''):
        try: u = urlparse(url); host = (u.hostname or '').lower()
        except ValueError: return Link(page.name, line, url, 'invalid', '', '', '', text)
        target = ''
        if not u.scheme and not u.netloc and u.path:
            base = '' if u.path.startswith('/') else os.path.dirname(page.name)
            target = os.path.normpath(os.path.join(base, unquote(u.path).lstrip('/')))
            if url.endswith('/') or os.path.isdir(os.path.join(self.root, target)): target = os.path.join(target, 'index.html')
        elif not u.scheme and not u.netloc and u.fragment: target = page.name
        return Link(page.name, line, url, host, u.path, target, unquote(u.fragment), text, in_nav, cls)

def host_is(link, *domains): return any(link.host == d or link.host.endswith('.' + d) for d in domains)
DAY = re.compile(r'(^|/)day\d+(-[\w-]+)?\.html$')
MEALS = {'breakfast': 'breakfast', 'snídaně': 'breakfast', 'lunch': 'lunch', 'oběd': 'lunch',
         'coffee': 'coffee', 'káva': 'coffee', 'dinner': 'dinner', 'večeře': 'dinner'}
MEAL_HOURS = {'breakfast': (0, 600), 'lunch': (690, 840), 'coffee': (840, 1020), 'dinner': (1080, 1440)}

def _text(s): return ' '.join(html.unescape(re.sub(r'<[^>]+>', '', s)).split())
def _minutes(m): return int(m[1]) * 60 + int(m[2])

def timeline(page):
    heads = list(re.finditer(r'<[^>]+class=["\'][^"\']*\btimeline-date\b[^"\']*["\'][^>]*>(.*?)</[^>]+>', page.src, re.S | re.I))
    items = [(None, None, page.src[:heads[0].start()] if heads else page.src)]
    for h, end in zip(heads, [h.start() for h in heads[1:]] + [len(page.src)]):
        title = _text(h.group(1)); at = re.match(r'(\d{1,2}):(\d{2})', title)
        meal = next((MEALS[w] for w in re.findall(r'\w+', title.lower()) if w in MEALS), None)
        items.append((_minutes(at) if at else None, meal, page.src[h.end():end]))
    return items

def venues(body):
    lists = re.findall(r'<ul\b[^>]*class=["\'][^"\']*\bfood-list\b[^"\']*["\'][^>]*>(.*?)</ul>', body, re.S | re.I)
    return [li for ul in lists for li in re.findall(r'<li\b[^>]*>(.*?)</li>', ul, re.S | re.I)]

# ---------------------------------------------------------------- rules
@rule('html-balance')
def _(site):
    for p in site.pages.values():
        for error in p.balance: yield 'ERROR', p.name, error

@rule('css-class')
def _(site):
    for p in site.pages.values():
        paths = [os.path.join(site.root, l.target) for l in site.links
            if l.page == p.name and l.target.endswith('.css') and not l.target.startswith('..')]
        css = ''
        for path in paths:
            if os.path.isfile(path):
                with open(path, encoding='utf-8') as f: css += f.read()
        defined = set(re.findall(r'\.([a-zA-Z_][\w-]*)', re.sub(r'/\*.*?\*/', '', css, flags=re.S)))
        used = [(line, name) for _, attrs, line in p.tags for name in (attrs.get('class') or '').split()]
        if used and not css:
            yield 'ERROR', p.name, f'classes used but no linked stylesheet found: {", ".join(sorted({n for _, n in used})[:5])}'; continue
        for line, name in used:
            if name not in defined: yield 'ERROR', p.name, f'line {line}: undefined class {name}'

@rule('empty-href')
def _(site):
    for p in site.pages.values():
        for tag, attrs, line in p.tags:
            if 'href' in attrs and not (attrs['href'] or '').strip():
                yield 'ERROR', p.name, f'line {line}: empty href'

@rule('osm-links')
def _(site):
    for l in site.links:
        if host_is(l, 'openstreetmap.org') and l.path != '/' and not re.fullmatch(r'/(?:node|way|relation)/[1-9][0-9]*/?', l.path):
            yield 'ERROR', l.page, f'line {l.line}: invalid OSM object URL {l.url}'

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
        if l.path.startswith('/'):
            yield 'ERROR', l.page, f'line {l.line}: root-relative link {l.url}; link relative to the page'; continue
        if l.target.split(os.sep)[0] == '..':
            yield 'ERROR', l.page, f'line {l.line}: link leaves site root: {l.url}'; continue
        if l.target.endswith('.html') and not os.path.isfile(os.path.join(site.root, l.target)):
            yield 'ERROR', l.page, f'line {l.line}: {l.url} → no such page'
        elif not l.target.endswith('.html') and not os.path.exists(os.path.join(site.root, l.target)):
            yield 'ERROR', l.page, f'line {l.line}: {l.url} → no such file'
        elif l.frag and l.target in site.pages and l.frag not in site.pages[l.target].ids:
            yield 'ERROR', l.page, f'line {l.line}: {l.url} → no id "{l.frag}"'

@rule('nav')
def _(site):
    days = sorted(n for n in site.pages if re.search(r'(^|/)day\d+\.html$', n))
    labels = {}
    for n in days:
        p = site.pages[n]; navs = [ln for t, a, ln in p.tags if t == 'nav']
        if len(navs) != 1: yield 'ERROR', n, f'{len(navs)} <nav> elements'; continue
        seen = {n}
        for l in site.links:
            if l.page == n and l.in_nav and l.target in site.pages:
                seen.add(l.target); labels.setdefault(l.target, set()).add(l.text)
        for other in days:
            if os.path.dirname(other) == os.path.dirname(n) and other not in seen: yield 'ERROR', n, f'nav has no link to {other}'
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
        x, y = _num(q, 'x'), _num(q, 'y')
        if x is None or y is None or not (-180 <= x <= 180 and -90 <= y <= 90): yield 'ERROR', l.page, f'{where}: missing or bad x/y'
        if 'planovani-trasy' in l.url:
            rs, ri = q.get('rs', []), q.get('ri', [])
            try: n = len(mapyrc.decode(q.get('rc', [''])[0]))
            except Exception as e: yield 'ERROR', l.page, f'{where}: rc= does not decode ({e})'; continue
            if n < 2: yield 'ERROR', l.page, f'{where}: route has {n} points'
            elif not (n == len(rs) == len(ri)): yield 'ERROR', l.page, f'{where}: {n} points, {len(rs)} rs, {len(ri)} ri'
            if any(source != 'coor' and not ident for source, ident in zip(rs, ri)): yield 'ERROR', l.page, f'{where}: named route point has no id'
            if sum(r == 'coor' for r in rs) > 1: yield 'WARN', l.page, f'{where}: route has unnamed (coor) stops'
        elif 'source' not in q: continue
        elif q.get('source', [''])[0] not in ('osm', 'base', 'firm', 'pubt', 'coor', 'stre', 'traf'):
            yield 'ERROR', l.page, f'{where}: unknown source= "{q.get("source", [""])[0]}"'
        elif not q.get('id', [''])[0]:
            yield 'ERROR', l.page, f'{where}: source= without id='
        elif q.get('source') == ['coor']:
            yield 'WARN', l.page, f'{where}: GPS pin, not a place ("{l.text}")'

@rule('google-links')
def _(site):
    for l in site.links:
        if not host_is(l, 'google.com') or not l.path.startswith('/maps'): continue
        if l.path.startswith('/maps/search/'): yield 'WARN', l.page, f'line {l.line}: search link, not a place ("{l.text}")'

@rule('link-text')
def _(site):
    for l in site.links:
        is_map = host_is(l, 'mapy.com', 'openstreetmap.org') or (host_is(l, 'google.com') and l.path.startswith('/maps'))
        if is_map and 'alt' not in l.cls.split() and re.fullmatch(r'\W*(?:google|mapy|osm|maps?)\W*', l.text, re.I):
            yield 'ERROR', l.page, f'line {l.line}: link text "{l.text}" names the map, not the place'

@rule('tel-links')
def _(site):
    for p in site.pages.values():
        body = re.sub(r'<a\b[^>]*href=["\']tel:[^>]*>.*?</a>', '', p.src, flags=re.S)
        for m in re.finditer(r'\+\d{2}(?:[  ]?\d){8,12}', re.sub(r'<[^>]+>', ' ', body)):
            yield 'ERROR', p.name, f'phone not a tel: link: {m.group(0)}'

@rule('meal-lines')
def _(site):
    for p in site.pages.values():
        for at, meal, body in timeline(p):
            for li in venues(body):
                t = _text(li); head = t.split('·')[0]
                hrefs = re.findall(r'href=["\']([^"\']+)', li)
                if len(head.split()) > 10: yield 'ERROR', p.name, f'meal line {len(head.split())} words: {head[:40]}'
                rating = re.search(r'·\s*(?:Google|TripAdvisor|mapy)\s+([1-5]\.\d)\b', t, re.I); floor = 4.5 if meal == 'coffee' else 4.0
                if not rating and not re.search(r'\bunrated\b', t, re.I): yield 'ERROR', p.name, f'meal line without rating: {head[:40]}'
                elif rating and float(rating[1]) < floor and 'fallback' not in head.lower(): yield 'ERROR', p.name, f'meal line rated {rating[1]}, below {floor} and not labelled fallback: {head[:40]}'
                if not re.search(r'\b(?:open|from|until|to)\b[^·]*\b\d{1,2}:\d{2}\b', head, re.I): yield 'ERROR', p.name, f'meal line without opening time: {head[:40]}'
                opens = re.search(r'\bfrom (\d{1,2}):(\d{2})', head, re.I); closes = re.search(r'\b(?:to|until) (\d{1,2}):(\d{2})', head, re.I)
                if at is not None and opens and _minutes(opens) > at: yield 'ERROR', p.name, f'meal line opens {opens[1]}:{opens[2]}, after the meal at {at // 60:02}:{at % 60:02}: {head[:40]}'
                if at is not None and closes and _minutes(closes) <= at: yield 'ERROR', p.name, f'meal line closes {closes[1]}:{closes[2]}, before the meal at {at // 60:02}:{at % 60:02}: {head[:40]}'
                if not any(h.startswith('tel:') for h in hrefs) and 'phone: not found' not in t: yield 'ERROR', p.name, f'meal line without tel: link: {head[:40]}'
                if not any('google.com/maps/' in h for h in hrefs): yield 'ERROR', p.name, f'meal line without Google link: {head[:40]}'
                if not any('mapy.com/' in h for h in hrefs): yield 'ERROR', p.name, f'meal line without mapy link: {head[:40]}'
                if any('source=coor' in h for h in hrefs): yield 'ERROR', p.name, f'meal line uses GPS pin: {head[:40]}'

@rule('meal-cap')
def _(site):
    for p in site.pages.values():
        for _, meal, body in timeline(p):
            count = len(venues(body)); cap = 3 if meal == 'lunch' else 2
            if meal and count > cap: yield 'ERROR', p.name, f'{meal} has {count} selections; maximum {cap}'

@rule('day-blocks')
def _(site):
    for n, p in site.pages.items():
        if not DAY.search(n): continue
        for c in ('checked-line', 'route-overview', 'map-links', 'timeline', 'source-note'):
            if not p.has_class(c): yield 'ERROR', n, f'missing .{c}'
        for t, a, ln in p.tags:
            if t == 'iframe' and a.get('loading') == 'lazy': yield 'ERROR', n, f'line {ln}: lazy iframe renders blank on phones'
        if sum(1 for _, a, _ in p.tags if 'checked-line' in (a.get('class') or '').split()) != 1: yield 'ERROR', n, 'expected one checked-line'
        if re.search(r'<h[23]>\s*(Food|Breakfast|Lunch|Coffee|Dinner)\b', p.src): yield 'ERROR', n, 'meal heading outside timeline'
        items = timeline(p)[1:]
        times = [at for at, _, _ in items if at is not None]
        present = {meal for _, meal, _ in items}
        for meal, (start, end) in MEAL_HOURS.items():
            if times and start <= max(times) and min(times) < end and meal not in present: yield 'ERROR', n, f'missing meal item: {meal}'
        for _, meal, body in items:
            if meal and not venues(body) and 'self-catered' not in _text(body).lower(): yield 'ERROR', n, f'{meal} has no venue and is not self-catered'

# ---------------------------------------------------------------- runner
def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument('dir'); ap.add_argument('--root'); ap.add_argument('--rule', action='append', default=[])
    ap.add_argument('--quiet', action='store_true', help='errors only')
    a = ap.parse_args(argv)
    unknown = set(a.rule) - set(RULES)
    if unknown: ap.error(f'unknown rule(s): {", ".join(sorted(unknown))}; known: {", ".join(RULES)}')
    if not os.path.isdir(a.dir): ap.error(f'not a directory: {a.dir}')
    root = a.root or a.dir
    if os.path.relpath(os.path.abspath(a.dir), os.path.abspath(root)).split(os.sep)[0] == '..': ap.error(f'{a.dir} is not inside --root {root}')
    site = Site(a.dir, root); out = []
    if not site.pages: ap.error("site has no HTML pages")
    for name in a.rule or RULES:
        out += [(lvl, page, name, msg) for lvl, page, msg in RULES[name](site)]
    for lvl, page, name, msg in sorted(out, key=lambda f: (f[1], f[0], f[2])):
        if not (a.quiet and lvl != 'ERROR'): print(f'{lvl:5} {page:20} {name:15} {msg}')
    e = sum(f[0] == 'ERROR' for f in out)
    print(f'-- {len(a.rule or RULES)} rules, {len(site.pages)} pages, {e} errors, {len(out) - e} warnings')
    return 1 if e else 0

if __name__ == '__main__': sys.exit(main())
