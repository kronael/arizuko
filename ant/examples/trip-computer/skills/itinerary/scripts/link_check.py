#!/usr/bin/env python3
"""Open every map link of a trip page in the browser and fail on broken ones.
Usage: link_check.py PAGE.html [PAGE2.html ...]   (exit 1 on any FAIL)
Checks: mapy place links open a named entry (not "Position on the map" /
"search results"); mapy route links render a route with km and every
waypoint named; openstreetmap.org links
open an object whose name matches the link text when the text is a name.
An "Access denied", consent or error page fails on every host.
Google links are opened with consent cookies: a place must open its card
named as in the URL or the link text, a
directions link must show a travel time, a search link is a WARN."""
import argparse, re, sys, html, subprocess, time, unicodedata, urllib.parse
from html.parser import HTMLParser

class Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.links, self.href, self.text = [], None, []
    def handle_starttag(self, tag, attrs):
        if tag == 'a': self.href, self.text = dict(attrs).get('href'), []
    def handle_data(self, data):
        if self.href is not None: self.text.append(data)
    def handle_endtag(self, tag):
        if tag == 'a' and self.href is not None:
            self.links.append((self.href, ''.join(self.text)))
            self.href = None

BLOCKED = re.compile(r'access denied|forbidden|captcha|consent|error', re.I)

def words(s):
    plain = ''.join(c for c in unicodedata.normalize('NFKD', s) if not unicodedata.combining(c))
    return {w.lower() for w in re.findall(r'\w{4,}', plain)}

def ab(*a):
    return subprocess.run(['agent-browser',*a],capture_output=True,text=True,timeout=60,check=True).stdout.strip()
def snap(): return ab('snapshot')
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pages", nargs="+")
    args = parser.parse_args()
    fails=0
    checked=0
    GOOGLE_READY=[False]
    unsupported=0
    seen=set()
    try:
        for page in args.pages:
            parsed=Links(); parsed.feed(open(page, encoding='utf-8').read())
            for u,t in parsed.links:
                u=html.unescape(u); t=re.sub('<[^>]+>','',t).strip()
                if re.search(r'mapy\.cz|goo\.gl', u):
                    unsupported+=1; fails+=1; print(f'{page}\tFAIL unsupported map link, use mapy.com\t{t[:30]}\t{u[:90]}'); continue
                if u in seen or not re.search(r'mapy\.com|openstreetmap\.org|google\.com/maps', u): continue
                seen.add(u); checked+=1; res='ok'
                if 'google.com/maps' in u:
                    if not re.search(r'/maps/(place|dir|search)/', u): res='FAIL bad google url'
                    else:
                        if not GOOGLE_READY[0]:
                            ab('open','https://www.google.com/robots.txt'); time.sleep(1)
                            ab('eval','document.cookie="CONSENT=YES+cb.20210328-17-p0.en+FX+417; domain=.google.com; path=/; max-age=31536000"; document.cookie="SOCS=CAESHAgBEhJnd3NfMjAyMzA4MTAtMF9SQzIaAmVuIAEaBgiA_LyaBg; domain=.google.com; path=/; max-age=31536000";1')
                            GOOGLE_READY[0]=True
                        ab('open',u+('&' if '?' in u else '?')+'hl=en'); time.sleep(6)
                        title=(ab('get','title').splitlines() or [''])[0]; sn=snap()
                        if 'consent' in title.lower() or 'error' in title.lower() or not title: res=f'FAIL google did not open: {title}'
                        elif '/maps/dir/' in u:
                            m=re.search(r'(\d+ ?(?:h|hr|hod)[^"]{0,8}\d* ?min|\d+ ?min)', sn)
                            res=f'ok {m.group(0)}' if m else f'FAIL google route not found: {title}'
                        elif '/maps/place/' in u:
                            expected=words(urllib.parse.unquote_plus(u.split('/maps/place/')[1].split('/')[0]))|words(t)
                            if title.strip() in ('Google Maps','') or title.startswith('Results'): res=f'FAIL google place not opened: {title}'
                            elif expected and not words(title)&expected: res=f'FAIL google place differs: {title}'
                            else: res='ok '+title.split(' - ')[0].split(' – ')[0]
                        else:
                            res=('WARN google search page, not a place: '+title) if not re.search(r'heading "[^"]+" \[level=1', sn) or 'Results' in sn[:3000] else 'ok '+title
                else:
                    ab('open',u); time.sleep(5); title=ab('get','title').splitlines()[0] if ab('get','title') else ''
                    if 'planovani-trasy' in u:
                        sn=snap(); km=re.search(r'([0-9.,]+ ?k?m) • ([0-9:]+ ?h|[0-9]+ ?min)', sn)
                        raw=re.findall(r'heading "(\d+\.\d+N, \d+\.\d+E)" \[level=4', sn); raw=raw[1:-1] if len(raw)>=2 and raw[0]==raw[-1] else raw
                        if not km: res='FAIL route not rendered'
                        elif raw: res=f'WARN {len(raw)} unnamed waypoint(s) ({km.group(0)})'
                        else: res=f'ok {km.group(0)}'
                    elif 'mapy.com' in u:
                        if 'Position on the map' in title: res='WARN gps pin, not a place'
                        elif 'search results' in title or not title or BLOCKED.search(title): res=f'FAIL no place: {title}'
                        elif words(t) and not words(title)&words(t): res=f'WARN mapy name differs: {title}'
                        else: res='ok '+title.split(' • ')[0]
                    else:
                        if 'not found' in title.lower() or not title or BLOCKED.search(title): res=f'FAIL osm object missing: {title}'
                        elif t and len(t.split())<=4 and not any(w.lower() in title.lower() for w in re.findall(r'\w{4,}',t)): res=f'WARN osm name differs: {title}'
                        else: res='ok '+title[:50]
                if res.startswith('FAIL'): fails+=1
                print(f'{page}\t{res}\t{t[:30]}\t{u[:90]}')
    finally:
        if checked: ab('close')
    print(f'-- {checked} checked, {fails} failed, {unsupported} unsupported')
    if not checked: print('FAIL: no supported map links', file=sys.stderr)
    return 1 if fails or not checked else 0

if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, subprocess.SubprocessError) as error:
        print(f'FAIL browser: {error}', file=sys.stderr)
        sys.exit(1)
