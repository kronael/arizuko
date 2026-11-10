#!/usr/bin/env python3
"""Corroborate trail facts from OpenStreetMap (works without mapy.com egress).
Usage: osm_trails.py LAT1 LON1 LAT2 LON2 [pad_deg=0.006]
Lists hiking/bicycle route relations in the bbox around the two points with
colour (osmc:symbol), length of their member ways returned for the bbox
(whole ways, so a way crossing the edge counts fully), surface/highway mix,
whether they pass within 300 m of each point, plus car parks (ways AND nodes)
with fee/capacity/charge tags. Climb is NOT here: read it from the mapy.com
route elevation profile. Source: https://api.openstreetmap.org/api/0.6/map
(limit: 50 000 nodes and 0.25 deg^2 per call; HTTP 400 = too big, use closer points or a smaller pad)."""
import sys, math, urllib.request, urllib.error, xml.etree.ElementTree as ET
from typing import NoReturn

def usage(msg='') -> NoReturn:
    print(__doc__)
    if msg: print(f'\nerror: {msg}')
    sys.exit(2)

args = sys.argv[1:]
if len(args) < 4 or args[0] in ('-h', '--help'): usage()
try:
    lat1, lon1, lat2, lon2 = map(float, args[:4]); pad = float(args[4]) if len(args) > 4 else 0.006
except ValueError as err: usage(str(err))
if not (-90 <= lat1 <= 90 and -90 <= lat2 <= 90 and -180 <= lon1 <= 180 and -180 <= lon2 <= 180): usage('LAT LON order: latitude first')
bbox = f"{min(lon1,lon2)-pad:.6f},{min(lat1,lat2)-pad:.6f},{max(lon1,lon2)+pad:.6f},{max(lat1,lat2)+pad:.6f}"
area = (abs(lon1-lon2)+2*pad)*(abs(lat1-lat2)+2*pad)
if area > 0.25: usage(f'bbox area {area:.3f} deg^2 exceeds the OSM limit of 0.25')
req = urllib.request.Request(f"https://api.openstreetmap.org/api/0.6/map?bbox={bbox}", headers={"User-Agent": "arizuko-trip-planner/1.0"})
try:
    root = ET.fromstring(urllib.request.urlopen(req, timeout=120).read())
except urllib.error.HTTPError as err:
    sys.exit(f'OSM map call failed: HTTP {err.code} for bbox={bbox} ({err.read()[:160].decode(errors="replace")!r}); 400 = over 50 000 nodes, reduce pad or distance')
except urllib.error.URLError as err:
    sys.exit(f'OSM map call failed: {err.reason}')
nodes = {n.get('id'): (float(n.attrib['lat']), float(n.attrib['lon'])) for n in root.findall('node')}
ways = {w.get('id'): w for w in root.findall('way')}
tags = lambda e: {t.get('k'): t.get('v') for t in e.findall('tag')}
def hav(a, b):
    p1 = math.radians(a[0])
    p2 = math.radians(b[0])
    dl = math.radians(b[1]-a[1])
    h = math.sin((p2-p1)/2)**2 + math.cos(p1)*math.cos(p2)*math.sin(dl/2)**2
    return 2*6371000*math.asin(math.sqrt(h))
def wnodes(w): return [nodes[n.get('ref')] for n in ways[w].findall('nd') if n.get('ref') in nodes]
def wlen(w): nd = wnodes(w); return sum(hav(nd[i], nd[i+1]) for i in range(len(nd)-1))
def near(w, p, r=300): return any(hav(n, p) < r for n in wnodes(w))
print(f"bbox={bbox}  (OSM map call, {len(nodes)} nodes, {len(ways)} ways; lengths = member ways returned for the bbox, whole ways)")
if not nodes: print("warning: 0 nodes — check the LAT LON order (latitude first) and the pad")
for rel in root.findall('relation'):
    tg = tags(rel)
    if tg.get('type') != 'route' or tg.get('route') not in ('hiking', 'foot', 'bicycle', 'mtb'): continue
    wids = [m.get('ref') for m in rel.findall('member') if m.get('type') == 'way' and m.get('ref') in ways]
    if not wids: continue
    L = sum(wlen(w) for w in wids); surf = {}; hw = {}
    for w in wids:
        t = tags(ways[w]); surf[t.get('surface', '?')] = surf.get(t.get('surface', '?'), 0) + wlen(w); hw[t.get('highway', '?')] = hw.get(t.get('highway', '?'), 0) + wlen(w)
    a = any(near(w, (lat1, lon1)) for w in wids); b = any(near(w, (lat2, lon2)) for w in wids)
    flag = 'BOTH' if a and b else ('A' if a else ('B' if b else '-'))
    print(f"[{flag}] rel {rel.get('id')} {tg.get('route')} colour={tg.get('colour')} osmc={tg.get('osmc:symbol')} name={tg.get('name')} len_in_bbox={L/1000:.2f}km highway={ {k: round(v) for k, v in hw.items()} } surface={ {k: round(v) for k, v in surf.items()} }  https://www.openstreetmap.org/relation/{rel.get('id')}")
def park(kind, oid, t, ctr):
    d = min(hav(ctr, (lat1, lon1)), hav(ctr, (lat2, lon2))) if ctr else None
    print(f"parking {kind} {oid} name={t.get('name')} fee={t.get('fee')} charge={t.get('charge')} access={t.get('access')} capacity={t.get('capacity')} surface={t.get('surface')} at={ctr} nearest_point={round(d) if d is not None else None}m  https://www.openstreetmap.org/{kind}/{oid}")
for w, el in ways.items():
    t = tags(el)
    if t.get('amenity') == 'parking':
        c = wnodes(w); park('way', w, t, (sum(p[0] for p in c)/len(c), sum(p[1] for p in c)/len(c)) if c else None)
for n in root.findall('node'):
    t = tags(n)
    if t.get('amenity') == 'parking': park('node', n.get('id'), t, (float(n.attrib['lat']), float(n.attrib['lon'])))
