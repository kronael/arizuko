#!/usr/bin/env python3
"""Inspect elevation and nearby mapped features for one or more LAT,LON points."""
import sys, json, math, time, urllib.request, urllib.parse
AZ=16; RADII=[300,700,1200]
def off(lat,lon,dn,de):
    return lat+dn/111320.0, lon+de/(111320.0*math.cos(math.radians(lat)))
def elev_batch(pts):
    loc="|".join(f"{a:.6f},{b:.6f}" for a,b in pts)
    u="https://api.opentopodata.org/v1/eudem25m?locations="+urllib.parse.quote(loc)
    for k in range(3):
        try: return [r["elevation"] for r in json.load(urllib.request.urlopen(u,timeout=40))["results"]]
        except Exception: time.sleep(2)
    return [None]*len(pts)
HDR={"User-Agent":"estate-terrain/1.0"}
def overpass(la,lo,rad=2000):
    q=(f'[out:json][timeout:50];('
       f'way["landuse"="industrial"](around:{rad},{la},{lo});'
       f'way["landuse"="quarry"](around:{rad},{la},{lo});way["man_made"="mineshaft"](around:{rad},{la},{lo});'
       f'way["highway"~"motorway|trunk"](around:{rad},{la},{lo});'
       f'way["railway"="rail"](around:{rad},{la},{lo}););out center tags;')
    for k in range(3):
        try:
            r=json.load(urllib.request.urlopen(urllib.request.Request(
              "https://overpass-api.de/api/interpreter",data=("data="+urllib.parse.quote(q)).encode(),headers=HDR),timeout=60)); break
        except Exception as e:
            if k==2: return {"err":str(e)[:30]}
            time.sleep(4)
    res={"industrial":[],"quarry":[],"road":[],"rail":[]}
    for e in r.get("elements",[]):
        c=e.get("center") or {"lat":e.get("lat"),"lon":e.get("lon")}
        if not c.get("lat"): continue
        d=math.hypot((c["lat"]-la)*111320,(c["lon"]-lo)*111320*math.cos(math.radians(la)))
        t=e.get("tags",{})
        if t.get("landuse")=="industrial": res["industrial"].append((round(d),t.get("name","")))
        if t.get("landuse")=="quarry" or t.get("man_made")=="mineshaft": res["quarry"].append((round(d),t.get("name","")))
        if t.get("highway") in("motorway","trunk"): res["road"].append(round(d))
        if t.get("railway")=="rail": res["rail"].append(round(d))
    return res
def analyze(la,lo):
    pts=[(la,lo)]; meta=[]
    for r in RADII:
        for i in range(AZ):
            a=2*math.pi*i/AZ; pts.append(off(la,lo,r*math.cos(a),r*math.sin(a))); meta.append((r,i))
    el=elev_batch(pts)
    c=el[0]
    if c is None or len(el) != len(pts) or not any(v is not None for v in el[1:]):
        return {"error": "elevation unknown", "ov": overpass(la,lo)}
    ring=[(meta[j],el[j+1]) for j in range(len(meta)) if el[j+1] is not None]
    vals=[v for _,v in ring]
    relief=c-(sum(vals)/len(vals))
    lower=sum(1 for v in vals if v < c-2)/len(vals)          # openness (výhled)
    vdrop=c-min(vals)                                         # max viditelný pokles
    # sklon: největší převýšení přes nejbližší prstenec (300m) na průměr délky
    r300=[v for (rr,_),v in ring if rr==300]
    slope=(max(r300)-min(r300))/600*100 if r300 else None
    ov=overpass(la,lo)
    return dict(elev=round(c),relief=round(relief,1),open=round(lower*100),vdrop=round(vdrop),slope=round(slope,1) if slope is not None else None,ov=ov)
if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit("provide at least one LAT,LON point")
    for arg in sys.argv[1:]:
        la, lo = [float(x) for x in arg.split(",")]
        print(json.dumps({"point": arg, **analyze(la, lo)}, ensure_ascii=False))
