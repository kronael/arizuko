#!/usr/bin/env python3
"""Pre-delivery check of trip day pages (itinerary skill, Round 4).
Usage: page_check.py ~/public_html/<trip>/day*.html   (exit 1 when a page fails)
Fails: title != h1, <div> balance, one <nav>, '?q=' anywhere, empty href,
mapy.cz / short links, plain phone outside a tel: link, checked-line count != 1,
no source-note, a missing meal item (Breakfast/Lunch/Coffee/Dinner as
'HH:MM — <Meal>'), a Food heading outside the timeline, and every food <li>:
<= 10 words before the first '·', a 'Google x.x' or 'unrated' token, a tel:
link or 'phone: not found', a Google link, a mapy link, no source=coor pin.
Warns: Story count < stops, 'if full' < drive items, food line without a
time (open-today proof)."""
import re, sys, html

MAX_WORDS = 10
MEALS = ('Breakfast', 'Lunch', 'Coffee', 'Dinner')
PHONE = re.compile(r'\+\d{2}[\s\d]{8,14}\d')
RATING = re.compile(r'\bGoogle \d\.\d\b|\bunrated\b')
TIME = re.compile(r'\d{1,2}:\d{2}')
TAG = re.compile(r'<[^>]+>')

def head(li):
    return TAG.sub('', li).split('·')[0]

def words_before_dot(li):
    h = head(li).replace('—', ' ').replace('–', ' ')
    return len([w for w in h.split() if w.strip('-,.;:()')])

def check(path):
    s = open(path, encoding='utf-8').read()
    errs, warn = [], []
    t = re.search(r'<title>(.*?)</title>', s, re.S); h = re.search(r'<h1>(.*?)</h1>', s, re.S)
    if not t or not h or html.unescape(t.group(1).strip()) != html.unescape(h.group(1).strip()):
        errs.append('title != h1')
    o, c = len(re.findall(r'<div\b', s)), s.count('</div>')
    if o != c: errs.append(f'div balance {o}/{c}')
    if len(re.findall(r'<nav\b', s)) != 1: errs.append('nav count != 1')
    if '?q=' in s: errs.append(f"?q= links: {s.count('?q=')}")
    empty = s.count('href=""')
    if empty: errs.append(f'empty href: {empty}')
    if re.search(r'https?://(en\.)?mapy\.cz', s): errs.append('mapy.cz link')
    n = s.count('class="checked-line"')
    if n != 1: errs.append(f'checked-line count {n} != 1')
    if 'source-note' not in s: errs.append('no source-note')
    for meal in MEALS:
        if not re.search(r'class="timeline-date">\d{1,2}:\d{2} — ' + meal + r'\b', s):
            errs.append(f'missing meal item: {meal}')
    for m in re.findall(r'<h[23]>\s*(Food|Breakfast|Lunch|Coffee|Dinner)', s):
        errs.append(f'meal heading outside the timeline: {m}')
    for ul in re.findall(r'<ul class="food-list">(.*?)</ul>', s, re.S):
        for item in re.findall(r'<li>(.*?)</li>', ul, re.S):
            name = head(item).strip()[:50]
            n = words_before_dot(item)
            if n > MAX_WORDS: errs.append(f'food li {n} words: {name}')
            if not RATING.search(TAG.sub('', item)): errs.append(f'food li without Google x.x / unrated: {name}')
            if 'href="tel:' not in item and 'phone: not found' not in item: errs.append(f'food li without tel link: {name}')
            if not re.search(r'href="https://(www\.)?google\.com/maps/', item): errs.append(f'food li without google link: {name}')
            if 'href="https://mapy.com/' not in item: errs.append(f'food li without mapy link: {name}')
            if 'source=coor' in item: errs.append(f'food li with a GPS pin: {name}')
            if not TIME.search(head(item)): warn.append(f'no open-today time: {name[:30]}')
    stripped = re.sub(r'<a [^>]*href="tel:[^"]*"[^>]*>.*?</a>', '', s, flags=re.S)
    for m in PHONE.findall(stripped): errs.append(f'phone not a tel: link: {m.strip()}')
    stops = len(re.findall(r'class="timeline-date">\d{1,2}:\d{2} — (?!Breakfast|Lunch|Coffee|Dinner|Leave|Drive|Back|Home|Walk down)', s))
    stories = s.count('Story.')
    drives = len(re.findall(r'class="timeline-date">\d{1,2}:\d{2} — (Leave|Drive|Park)', s))
    iffull = len(re.findall(r'if full', s, re.I))
    if stories < stops: warn.append(f'stories {stories} < stops {stops}')
    if iffull < drives: warn.append(f"'if full' {iffull} < drive items {drives}")
    return errs, warn

if len(sys.argv) < 2 or sys.argv[1] in ('-h', '--help'):
    sys.exit(__doc__.strip())
bad = 0
for p in sys.argv[1:]:
    errs, warn = check(p)
    print(f'{p}: {"FAIL" if errs else "ok"}' + (f' (warn: {"; ".join(warn)})' if warn else ''))
    for e in errs: print('  -', e)
    bad |= bool(errs)
sys.exit(1 if bad else 0)
