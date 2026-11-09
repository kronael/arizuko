#!/usr/bin/env python3
"""OSM census around a base point: sights, food, parking, kids, trails.
Usage: osm_census.py --lat 50.0875 --lon 14.4208 --radius 10 --out DIR [--cache DIR] [--also LAT,LON[,LAT,LON...] --also-radius 2]
Writes census.json + sights.md food.md parking.md kids.md trails.md (sorted by distance, mapy links).
A table that exists keeps its rows; a rerun appends only the places whose OSM id is not in it yet.
--also adds extra census circles (planned stops outside the radius, e.g. a town at 11.4 km) to the same tables.
Tiles the bbox in 0.055° x 0.04° pieces (api.openstreetmap.org map call, 50k-node limit); a tile that
answers HTTP 400 is split in four and retried. A cached tile older than 7 days is fetched again. Trail km_in_area sums each member way returned for the area once,
whole ways included — relation coverage, not the length of a walk."""
import argparse, json, math, os, re, subprocess, time, urllib.parse, xml.etree.ElementTree as ET
from collections import defaultdict
UA='arizuko-trip-planner/1.0 (osm_census)'
CACHE_DAYS=7
def hav(a,b):
    R=6371; p1,p2=math.radians(a[0]),math.radians(b[0]); dl=math.radians(b[1]-a[1])
    return 2*R*math.asin(math.sqrt(math.sin((p2-p1)/2)**2+math.cos(p1)*math.cos(p2)*math.sin(dl/2)**2))
class TileTooBig(Exception): pass
def fetch(bb,cache):
    f=os.path.join(cache,bb.replace(',','_')+'.xml')
    if not (os.path.exists(f) and os.path.getsize(f)>1000 and time.time()-os.path.getmtime(f)<CACHE_DAYS*86400 and open(f,errors='replace').read(200).lstrip().startswith('<?xml')):
        tmp=f+'.part'
        r=subprocess.run(['curl','-sS','--connect-timeout','20','--max-time','180','-A',UA,'-o',tmp,'-w','%{http_code}',f'https://api.openstreetmap.org/api/0.6/map?bbox={bb}'],capture_output=True,text=True)
        code=r.stdout.strip(); body=open(tmp,errors='replace').read(160) if os.path.exists(tmp) else ''
        if code=='400' and 'node' in body.lower(): os.remove(tmp); raise TileTooBig(bb)
        if code!='200':
            if os.path.exists(tmp): os.remove(tmp)
            raise SystemExit(f'tile {bb} -> HTTP {code} {body!r} {r.stderr.strip()}')
        os.replace(tmp,f)
    return f
def tiles_for(lat,lon,radius):
    dlat=radius/111.0; dlon=radius/(111.0*math.cos(math.radians(lat)))
    lon0,lon1,lat0,lat1=lon-dlon,lon+dlon,lat-dlat,lat+dlat
    out=[]; x=lon0
    while x<lon1:
        y=lat0
        while y<lat1:
            out.append((x,y,min(x+0.055,lon1),min(y+0.04,lat1))); y+=0.04
        x+=0.055
    return out
def split(t):
    x0,y0,x1,y1=t; xm=(x0+x1)/2; ym=(y0+y1)/2
    return [(x0,y0,xm,ym),(xm,y0,x1,ym),(x0,ym,xm,y1),(xm,ym,x1,y1)]
def bbstr(t): return ','.join(f'{v:.4f}' for v in t)
CATS={
 'sights':[('tourism',{'attraction','viewpoint','museum','artwork','zoo','theme_park','picnic_site','gallery','aquarium'}),('natural',{'peak','rock','cliff','waterfall','cave_entrance','stone','arch','spring','water'}),('waterway',{'waterfall'}),('historic',{'castle','ruins','monument','fort','tower','chapel','church','memorial','archaeological_site','manor','city_gate','citywalls','mine'}),('leisure',{'park','nature_reserve','garden'}),('man_made',{'tower','observatory','adit','mineshaft','lighthouse'}),('amenity',{'place_of_worship'})],
 'food':[('amenity',{'restaurant','cafe','fast_food','bar','pub','ice_cream','food_court','biergarten'}),('shop',{'bakery','confectionery','pastry'})],
 'parking':[('amenity',{'parking'})],
 'kids':[('leisure',{'playground','water_park','swimming_pool','sports_centre'}),('amenity',{'toilets'})],
}
def main():
    ap=argparse.ArgumentParser(); ap.add_argument('--lat',type=float,required=True); ap.add_argument('--lon',type=float,required=True)
    ap.add_argument('--radius',type=float,default=10); ap.add_argument('--out',required=True); ap.add_argument('--cache',default=None); ap.add_argument('--base-name',default='base'); ap.add_argument('--also',default='',help='LAT,LON[,LAT,LON...] extra circles'); ap.add_argument('--also-radius',type=float,default=2.0)
    a=ap.parse_args()
    nums=[float(v) for v in a.also.replace(';',',').split(',') if v.strip()]
    if len(nums)%2: raise SystemExit('--also needs LAT,LON pairs')
    centres=[(a.lat,a.lon,a.radius)]+[(nums[i],nums[i+1],a.also_radius) for i in range(0,len(nums),2)]
    if not all(-90<=la<=90 and -180<=lo<=180 and r>0 for la,lo,r in centres): raise SystemExit('each circle needs LAT -90..90, LON -180..180 and a radius over 0 km')
    os.makedirs(a.out,exist_ok=True); cache=a.cache or os.path.join(a.out,'tiles'); os.makedirs(cache,exist_ok=True)
    todo=[t for c in centres for t in tiles_for(*c)]; tiles=[]
    base=(a.lat,a.lon); seen=set(); rows=defaultdict(list); trails={}; counted=set()
    def inside(p): return any(hav((la,lo),p)<=r for la,lo,r in centres)
    while todo:
        tl=todo.pop(0); bb=bbstr(tl)
        try: f=fetch(bb,cache)
        except TileTooBig:
            if (tl[2]-tl[0])<0.004: raise SystemExit(f'tile {bb} still over the node limit at 0.004°')
            todo=split(tl)+todo; continue
        tiles.append(bb)
        t=ET.parse(f).getroot()
        nodes={n.get('id'):(float(n.get('lat')),float(n.get('lon'))) for n in t.findall('node')}
        ways={w.get('id'):w for w in t.findall('way')}
        def tags(e): return {x.get('k'):x.get('v') for x in e.findall('tag')}
        def pos(e):
            if e.tag=='node': return float(e.get('lat')),float(e.get('lon'))
            if e.tag=='relation': return relpos(e)
            pts=[nodes[n.get('ref')] for n in e.findall('nd') if n.get('ref') in nodes]
            return (sum(p[0] for p in pts)/len(pts), sum(p[1] for p in pts)/len(pts)) if pts else None
        def relpos(rel):
            pts=[]
            for m in rel.findall('member'):
                if m.get('type')=='way' and m.get('ref') in ways: pts+=[nodes[n.get('ref')] for n in ways[m.get('ref')].findall('nd') if n.get('ref') in nodes]
                elif m.get('type')=='node' and m.get('ref') in nodes: pts.append(nodes[m.get('ref')])
            return (sum(p[0] for p in pts)/len(pts), sum(p[1] for p in pts)/len(pts)) if pts else None
        for e in list(t.findall('node'))+list(t.findall('way'))+[r for r in t.findall('relation') if r.find("tag[@k='type']") is not None and r.find("tag[@k='type']").get('v')=='multipolygon']:
            tg=tags(e)
            if not tg: continue
            for cat,rules in CATS.items():
                hit=next((f'{k}={tg[k]}' for k,vals in rules if tg.get(k) in vals),None)
                if not hit: continue
                if cat!='parking' and cat!='kids' and not tg.get('name'): continue
                p=pos(e)
                if not p: continue
                d=hav(base,p)
                if not inside(p): continue
                key=(cat,tg.get('name') or '',round(p[0],4),round(p[1],4))
                if key in seen: continue
                seen.add(key)
                rows[cat].append({'name':tg.get('name') or '(unnamed)','type':hit,'lat':round(p[0],5),'lon':round(p[1],5),'km':round(d,1),'ele':tg.get('ele'),'addr':(' '.join(x for x in [tg.get('addr:street') or tg.get('addr:place'),tg.get('addr:housenumber'),tg.get('addr:postcode'),tg.get('addr:city')] if x) if (tg.get('addr:street') or tg.get('addr:place')) else None),'phone':tg.get('phone') or tg.get('contact:phone'),'web':tg.get('website') or tg.get('contact:website'),'hours':tg.get('opening_hours'),'cuisine':tg.get('cuisine'),'fee':tg.get('fee'),'charge':tg.get('charge'),'maxstay':tg.get('maxstay'),'operator':tg.get('operator'),'capacity':tg.get('capacity'),'access':tg.get('access'),'surface':tg.get('surface'),'wiki':tg.get('wikipedia'),'osm':f'{e.tag}/{e.get("id")}'})
        for rel in t.findall('relation'):
            tg=tags(rel)
            if tg.get('route') not in ('hiking','foot'): continue
            rid=rel.get('id'); L=0.0
            for m in rel.findall('member'):
                if m.get('type')=='way' and m.get('ref') in ways and (rid,m.get('ref')) not in counted:
                    counted.add((rid,m.get('ref')))
                    pts=[nodes[n.get('ref')] for n in ways[m.get('ref')].findall('nd') if n.get('ref') in nodes]
                    L+=sum(hav(pts[i],pts[i+1]) for i in range(len(pts)-1))
            r=trails.setdefault(rid,{'osm':f'relation/{rid}','name':tg.get('name'),'colour':tg.get('colour'),'osmc':tg.get('osmc:symbol'),'km_in_area':0.0})
            r['km_in_area']=round(r['km_in_area']+L,2)
    for cat in rows: rows[cat].sort(key=lambda r:r['km'])
    named=[r for r in rows['sights'] if r['name']!='(unnamed)']
    for r in rows['parking']:
        best=min(((hav((r['lat'],r['lon']),(x['lat'],x['lon'])),x['name']) for x in named),default=None)
        r['near']=f"{best[1]} {round(best[0]*1000)} m" if best and best[0]<=0.6 else None
    out={'base':{'name':a.base_name,'lat':a.lat,'lon':a.lon,'radius_km':a.radius},'tiles':tiles,'counts':{k:len(v) for k,v in rows.items()},**rows,'trails':sorted(trails.values(),key=lambda r:-r['km_in_area'])}
    json.dump(out,open(os.path.join(a.out,'census.json'),'w'),ensure_ascii=False,indent=1)
    def mapy(r): return f'https://mapy.com/en/turisticka?source=coor&id={r["lon"]}%2C{r["lat"]}&x={r["lon"]}&y={r["lat"]}&z=17'  # exact pin; ?q= searches the name and jumps elsewhere
    def cell(v): return ' '.join(str(v or '').split()).replace('|','\\|')
    def append(name,header,lines):
        path=os.path.join(a.out,name); old=open(path,encoding='utf-8').read() if os.path.exists(path) else ''
        with open(path,'a',encoding='utf-8') as f:
            if not old: f.write(header)
            elif not old.endswith('\n'): f.write('\n')
            for osm,line in lines:
                if not re.search(rf'\b{re.escape(osm)}\b',old): f.write(line)
    for cat,title in [('sights','Sights'),('food','Restaurants, cafés, bakeries'),('parking','Car parks'),('kids','Playgrounds, toilets, pools')]:
        lines=[]
        for r in rows[cat]:
            notes=' '.join(x for x in [r['cuisine'] and 'cuisine:'+r['cuisine'], r['fee'] and 'fee:'+r['fee'], r.get('charge') and 'charge:'+r['charge'], r.get('maxstay') and 'maxstay:'+r['maxstay'], r['capacity'] and 'cap:'+r['capacity'], r['access'] and 'access:'+r['access'], r.get('surface') and 'surface:'+r['surface'], r.get('operator') and 'operator:'+r['operator'], r.get('near') and 'near:'+r['near'], r['ele'] and r['ele']+' m', r['web'] or ''] if x)
            lines.append((r['osm'],f"| {r['km']} | {cell(r['name'])} | {cell(r['type'])} | {cell(r['addr'])} | {cell(r['phone'])} | {cell(r['hours'])} | {cell(notes)} | [mapy]({mapy(r)}) | {r['osm']} |\n"))
        append(f'{cat}.md',f'# {title} within {a.radius} km of {a.base_name} ({a.lat},{a.lon}) — OSM census\n\n| km | name | type | address | phone | hours | notes | map | osm |\n|---|---|---|---|---|---|---|---|---|\n',lines)
    append('trails.md','# Marked hiking routes crossing the area — OSM census\n\n| km in area | colour | osmc | name | osm |\n|---|---|---|---|---|\n',
        [(r['osm'],f"| {r['km_in_area']} | {cell(r['colour'])} | {cell(r['osmc'])} | {cell(r['name'])} | https://www.openstreetmap.org/{r['osm']} |\n") for r in out['trails']])
    print(json.dumps(out['counts']), 'trails', len(out['trails']), '->', a.out)
if __name__=='__main__': main()
