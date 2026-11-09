#!/usr/bin/env python3
"""Mapy.com route-planner coordinate string (rc=) encoder/decoder.
Port of jamhed/mapy src/coords.ts (same algorithm as SMap.Coords).
Usage: mapyrc.py routes.json
  routes.json: {"name": {"mode": "car|foot|hike|bike", "points": [[LAT, LON], [LAT, LON, SOURCE, ID], ...]}, ...}
  SOURCE, ID: the mapy entity of a named point (source= and id= of its mapy.com URL);
  a point without them is a coordinate (rs=coor), e.g. a private base.
  prints one line per route: name, mode, max_err, mapy.com URL, Google Maps dir URL (same points).
Modes (mapy mrp "c", browser-checked): car 111 = fastest, foot 131 = "Fast" (shortest on foot),
hike 132 = "Outdoor" (prefers marked trails; use it for loops and trail walks), bike 121."""
import sys, json, urllib.parse
A = '0ABCD2EFGH4IJKLMN6OPQRST8UVWXYZ-1abcd3efgh5ijklmn7opqrst9uvwxyz.'

def _ser(delta, orig):
    if -1024 <= delta < 1024:
        v = delta + 1024
        return A[v >> 6] + A[v & 63]
    if -32768 <= delta < 32768:
        v = 131072 | (delta + 32768)
        return A[(v >> 12) & 63] + A[(v >> 6) & 63] + A[v & 63]
    v = 805306368 | (orig & 268435455)
    return ''.join(A[(v >> s) & 63] for s in (24, 18, 12, 6, 0))

def encode(points):  # points: [(lat, lon), ...]
    ox = oy = 0; out = ''
    for lat, lon in points:
        x = min(round((lon + 180) * (1 << 28) / 360), (1 << 28) - 1)
        y = min(round((lat + 90) * (1 << 28) / 180), (1 << 28) - 1)
        out += _ser(x - ox, x) + _ser(y - oy, y); ox, oy = x, y
    return out

def decode(s):
    FIVE = (1 + 2) << 4; THREE = 1 << 5
    arr = list(s.strip())[::-1]; res = []; c = [0, 0]; i = 0
    def num(n):
        r = 0
        while n:
            ch = arr.pop(); k = A.find(ch)
            if k == -1: continue
            r = (r << 6) + k; n -= 1
        return r
    while arr:
        n = num(1)
        if n & FIVE == FIVE:
            n -= FIVE; n = ((n & 15) << 24) + num(4); c[i] = n
        elif n & THREE == THREE:
            n = ((n & 15) << 12) + num(2); n -= 1 << 15; c[i] += n
        else:
            n = ((n & 31) << 6) + num(1); n -= 1 << 10; c[i] += n
        if i:
            res.append((c[1] * 180 / (1 << 28) - 90, c[0] * 360 / (1 << 28) - 180))
        i = (i + 1) % 2
    if i:
        raise ValueError('rc= ends inside a point')
    return res

MODES = {'car': 111, 'foot': 131, 'hike': 132, 'bike': 121}

def gurl(points):  # Google Maps directions through the same coordinates
    return 'https://www.google.com/maps/dir/' + '/'.join(f'{lat:.6f},{lon:.6f}' for lat, lon in points)

def url(points, mode, places):
    rc = encode(points); n = len(points)
    q = 'planovani-trasy&rc=' + rc + ''.join(f'&rs={s}' for s, _ in places) + ''.join(f'&ri={i}' for _, i in places)
    crit = MODES[mode]
    q += '&mrp=' + urllib.parse.quote(json.dumps({'c': crit}, separators=(',', ':')))
    lat = sum(p[0] for p in points) / n; lon = sum(p[1] for p in points) / n
    return f'https://mapy.com/en/turisticka?{q}&x={lon:.6f}&y={lat:.6f}&z=13'

if __name__ == '__main__':
    if len(sys.argv) != 2 or sys.argv[1] in ('-h', '--help'):
        sys.exit(__doc__.strip())
    routes = json.load(open(sys.argv[1]))
    for name, r in routes.items():
        if any(len(p) not in (2, 4) for p in r['points']): sys.exit(f'{name}: a point is [LAT, LON] or [LAT, LON, SOURCE, ID]')
        pts = [(float(p[0]), float(p[1])) for p in r['points']]
        places = [(p[2], str(p[3])) if len(p) == 4 else ('coor', '') for p in r['points']]
        if r.get('mode') not in MODES: sys.exit(f'{name}: mode must be one of {sorted(MODES)}, got {r.get("mode")!r}')
        if len(pts) < 2: sys.exit(f'{name}: at least two points')
        if any(not (-90 <= lat <= 90 and -180 <= lon <= 180) for lat, lon in pts): sys.exit(f'{name}: points are [LAT, LON]')
        u = url(pts, r['mode'], places)
        back = decode(encode(pts))
        err = max(max(abs(a - b[0]), abs(c - b[1])) for (a, c), b in zip(pts, back))
        print(f'{name}\t{r["mode"]}\tmax_err={err:.6f}\t{u}\t{gurl(pts)}')
