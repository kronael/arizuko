#!/usr/bin/env python3
"""Find neighbouring pasture candidates from Czech open data.

Requires GPS, cadastral territory, all subject parcels and an output base path.
"""
import argparse
import datetime as dt
import io
import json
import math
import os
import re
import sys
import time
import xml.etree.ElementTree as ET

import requests
from shapely.geometry import shape, Point
from shapely.ops import unary_union
import shapely

RUIAN = "https://ags.cuzk.gov.cz/arcgis/rest/services/RUIAN/MapServer/{}/query"
KN_WMS = "https://services.cuzk.cz/wms/wms.asp"
LPIS_WMS = "https://mze.gov.cz/public/app/wms/plpis.fcgi"
SPU = "https://spu.gov.cz/nabidky/"
UA = {"User-Agent": "estate-pasture-research/1.0"}

DRUH = {2: "orná", 3: "chmelnice", 4: "vinice", 5: "zahrada", 6: "sad", 7: "TTP", 8: "TTP",
        10: "les", 11: "voda", 13: "zastavěná", 14: "ostatní"}
VYUZITI = {1: "skleník", 2: "školka", 3: "plantáž dřevin", 4: "les jiný", 5: "les s budovou",
           6: "rybník", 7: "koryto toku", 8: "koryto umělé", 9: "nádrž přírodní", 10: "nádrž umělá",
           11: "zamokřená", 12: "společný dvůr", 13: "zbořeniště", 14: "dráha", 15: "dálnice",
           16: "silnice", 17: "ost. komunikace", 18: "ost. dopravní", 19: "zeleň", 20: "sportoviště",
           21: "pohřebiště", 22: "kulturní", 23: "manipulační", 24: "dobývací prostor", 25: "skládka",
           26: "jiná plocha", 27: "neplodná", 28: "vodní s budovou", 29: "FVE", 30: "mez, stráň"}
# ostatní plocha (14) — které způsoby využití jde reálně spásat
OSTATNI_GRAZEABLE = {None, 19, 26, 27, 30}
OWNER_LAYERS = [("stát", "char_vl_stat"), ("kraj", "char_vl_kraje"),
                ("obec", "char_vl_obec"), ("soukromý/podílový", "char_vl_soukr_podil")]


def log(*a):
    print(*a, file=sys.stderr, flush=True)


def get(url, params=None, tries=3, timeout=90, **kw):
    last = None
    for i in range(tries):
        try:
            r = requests.get(url, params=params, timeout=timeout, headers=UA, **kw)
            if r.status_code == 200:
                return r
            last = f"HTTP {r.status_code}"
        except Exception as e:  # noqa
            last = str(e)
        time.sleep(2 * (i + 1))
    raise RuntimeError(f"GET {url} failed: {last}")


def post_json(path, **data):
    r = requests.post(SPU + path, data=data, timeout=60,
                      headers={**UA, "X-Requested-With": "XMLHttpRequest"})
    r.raise_for_status()
    return r.json()


# ---------- geometry helpers (local metric projection) ----------
def make_proj(lat0, lon0):
    kx = 111320.0 * math.cos(math.radians(lat0))
    ky = 110574.0

    def f(coords):  # numpy array (N,2) lon/lat -> metres
        out = coords.copy()
        out[:, 0] = (coords[:, 0] - lon0) * kx
        out[:, 1] = (coords[:, 1] - lat0) * ky
        return out
    return f


def parse_label(label):
    """Split a building or land parcel label into type and number."""
    s = label.strip().replace(" ", "")
    m = re.match(r"^(st\.?|st)(\d+(?:/\d+)?)$", s, re.I)
    if m:
        return 1, m.group(2)
    return 2, s


# ---------- RUIAN ----------
def ruian_parcels(lat, lon, radius_m):
    dlat = radius_m / 110574.0
    dlon = radius_m / (111320.0 * math.cos(math.radians(lat)))
    env = {"xmin": lon - dlon, "ymin": lat - dlat, "xmax": lon + dlon, "ymax": lat + dlat,
           "spatialReference": {"wkid": 4326}}
    feats, offset = [], 0
    while True:
        p = {"f": "geojson", "returnGeometry": "true", "inSR": "4326", "outSR": "4326",
             "outFields": "id,cisloparcely,kmenovecislo,poddelenicisla,vymeraparcely,druhpozemkukod,"
                          "zpusobyvyuzitipozemku,katastralniuzemi,druhcislovanikod",
             "geometry": json.dumps(env), "geometryType": "esriGeometryEnvelope",
             "spatialRel": "esriSpatialRelIntersects", "resultRecordCount": 2000,
             "resultOffset": offset}
        d = get(RUIAN.format(5), p).json()
        if "error" in d:
            raise RuntimeError(d["error"])
        feats += d.get("features", [])
        if not d.get("exceededTransferLimit") or not d.get("features"):
            break
        offset += len(d["features"])
    out = []
    for f in feats:
        pr = f["properties"]
        g = shape(f["geometry"])
        if not g.is_valid:
            g = g.buffer(0)
        out.append({"id": pr["id"], "label": pr["cisloparcely"], "typ": pr["druhcislovanikod"],
                    "ku": pr["katastralniuzemi"], "vymera": pr["vymeraparcely"] or 0,
                    "druh_kod": pr["druhpozemkukod"], "vyuziti_kod": pr["zpusobyvyuzitipozemku"],
                    "geom": g})
    return out


def ruian_lookup(layer, where, fields):
    d = get(RUIAN.format(layer), {"f": "json", "where": where, "outFields": fields,
                                  "returnGeometry": "false"}).json()
    f = d.get("features") or []
    return f[0]["attributes"] if f else {}


_KU_NAMES = {}


def ku_name(code):
    if code not in _KU_NAMES:
        _KU_NAMES[code] = (ruian_lookup(7, f"kod={code}", "kod,nazev") or {}).get("nazev") or str(code)
    return _KU_NAMES[code]


def fmt(n):
    return f"{n:,.0f}".replace(",", " ")


def ku_context(ku_code):
    ku = ruian_lookup(7, f"kod={ku_code}", "kod,nazev,obec")
    obec = ruian_lookup(12, f"kod={ku.get('obec')}", "kod,nazev,okres") if ku else {}
    okres = ruian_lookup(15, f"kod={obec.get('okres')}", "kod,nazev") if obec else {}
    return {"ku_kod": ku_code, "ku_nazev": ku.get("nazev"), "obec": obec.get("nazev"),
            "obec_kod": obec.get("kod"), "okres": okres.get("nazev")}


# ---------- druh klasifikace ----------
def classify(p):
    dk, vk = p["druh_kod"], p["vyuziti_kod"]
    if dk in (7, 8):
        return "★ pastvina (TTP)", True
    if dk == 2:
        return "★ orná → zatravnit", True
    if dk == 6:
        return "sad (podmíněně — pastva pod stromy)", True
    if dk == 14 and vk in OSTATNI_GRAZEABLE:
        return f"ostatní/{VYUZITI.get(vk, '?')} (podmíněně)", True
    if dk == 5:
        return "zahrada (podmíněně, obvykle oploceno)", False
    if dk == 10:
        return "les (ne)", False
    if dk == 11:
        return "voda (ne)", False
    if dk == 13:
        return "zastavěná (ne)", False
    if dk == 14:
        return f"ostatní/{VYUZITI.get(vk, '?')} (ne)", False
    return f"{DRUH.get(dk, dk)} (ne)", False


# ---------- owner type via ČÚZK KN WMS char_vl_* ----------
def owner_type_map(bbox, max_px=2000):
    """bbox=(lat_min,lon_min,lat_max,lon_max). Returns dict layer->PIL image (RGBA)."""
    from PIL import Image
    lat_min, lon_min, lat_max, lon_max = bbox
    w_m = (lon_max - lon_min) * 111320 * math.cos(math.radians((lat_min + lat_max) / 2))
    h_m = (lat_max - lat_min) * 110574
    W = min(max_px, int(w_m))
    H = max(1, int(W * h_m / w_m))
    imgs = {}
    for name, layer in OWNER_LAYERS:
        r = get(KN_WMS, {"service": "WMS", "version": "1.3.0", "request": "GetMap", "layers": layer,
                         "styles": "", "crs": "EPSG:4326",
                         "bbox": f"{lat_min},{lon_min},{lat_max},{lon_max}",
                         "width": W, "height": H, "format": "image/png", "transparent": "true"})
        if not r.headers.get("content-type", "").startswith("image"):
            raise RuntimeError(f"KN WMS {layer}: {r.text[:200]}")
        imgs[name] = Image.open(io.BytesIO(r.content)).convert("RGBA")
    return imgs, (W, H)


def sample_owner(imgs, size, bbox, geom):
    """Majority vote of non-transparent pixels over a few interior points."""
    lat_min, lon_min, lat_max, lon_max = bbox
    W, H = size
    pts = [geom.representative_point()]
    c = geom.centroid
    if geom.contains(c):
        pts.append(c)
    inner = geom.buffer(-0.00003)  # ~3 m
    if not inner.is_empty:
        pts.append(inner.representative_point())
        try:
            for part in getattr(inner, "geoms", [inner]):
                pts.append(part.representative_point())
        except Exception:
            pass
    votes = {}
    for pt in pts:
        px = int((pt.x - lon_min) / (lon_max - lon_min) * W)
        py = int((lat_max - pt.y) / (lat_max - lat_min) * H)
        if not (0 <= px < W and 0 <= py < H):
            continue
        for name, im in imgs.items():
            if im.getpixel((px, py))[3] > 0:
                votes[name] = votes.get(name, 0) + 1
    if not votes:
        return "?"
    best = max(votes.values())
    names = [n for n, v in votes.items() if v == best]
    return "/".join(names) if len(names) > 1 else names[0]


# ---------- LPIS ----------
def lpis_user(lat, lon, d=0.0002):
    r = get(LPIS_WMS, {"service": "WMS", "version": "1.3.0", "request": "GetFeatureInfo",
                       "layers": "LPIS_FB4_DEFAULT", "styles": "", "query_layers": "LPIS_FB4_DEFAULT",
                       "crs": "EPSG:4326", "bbox": f"{lat-d},{lon-d},{lat+d},{lon+d}",
                       "width": 101, "height": 101, "i": 50, "j": 50,
                       "info_format": "application/vnd.ogc.gml", "feature_count": 2}, timeout=60)
    try:
        root = ET.fromstring(r.content)
    except ET.ParseError:
        return None
    feat = None
    for el in root.iter():
        if el.tag.endswith("LPIS_FB4_DEFAULT_feature"):
            feat = el
            break
    if feat is None:
        return None
    g = lambda t: (feat.findtext(t) or "").strip()
    return {"uzivatel": g("uzivatel"), "ico": g("ICO"), "dpb": f"{g('CTVEREC')}/{g('zkracenyKod')}",
            "vymera_ha": g("vymera"), "stav": g("stav"), "od": g("platnyOd"), "rezim": g("rezimEZ_PO")}


# ---------- SPÚ ----------
def spu_offers(okres_name, ku_name):
    """Returns {typ: {"termin":..., "datum":..., "parcely":[...]}} for pacht (N) + prodej/převod (pozemky)."""
    out = {}
    for typ, label in (("N", "pacht/pronájem (SPÚ typ N)"), ("pozemky", "prodej/převod oprávněným (typ pozemky)")):
        try:
            terms = post_json("getDatum", typ_nabidky=typ)
        except Exception as e:  # noqa
            out[typ] = {"label": label, "error": str(e)}
            continue
        if not terms:
            out[typ] = {"label": label, "termin": None, "parcely": []}
            continue
        term = next((t for t in terms if str(t.get("akt")) == "1"), terms[0])
        tid = term["id_termin"]
        rec = {"label": label, "termin": tid, "datum": term.get("datum"), "aktualni": term.get("akt"),
               "parcely": [], "ku_v_nabidce": False}
        try:
            okresy = [o["NabidkyOkres"] for o in post_json("getOkresy", typ_nabidky=typ, dat1=tid)]
            rec["okres_v_nabidce"] = okres_name in okresy
            if okres_name in okresy:
                kus = [k["NabidkyNazevKU"] for k in post_json("getKu", okres=okres_name, id_termin=tid)]
                rec["ku_v_nabidce"] = ku_name in kus
                rec["ku_v_okrese"] = kus
                if ku_name in kus:
                    rec["parcely"] = post_json("getNabidky", okres=okres_name, id_terminu=tid, ku=ku_name)
        except Exception as e:  # noqa
            rec["error"] = str(e)
        out[typ] = rec
    return out


def spu_parcel_no(s):
    m = re.match(r"^\s*(?:KN|PK|ZE)?\s*([0-9]+(?:/[0-9]+)?)", s or "")
    return m.group(1) if m else None


# ---------- main ----------
def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--id", required=True, help="listing identifier")
    ap.add_argument("--gps", required=True, help="lat,lon")
    ap.add_argument("--ku", type=int, required=True, help="cadastral territory code")
    ap.add_argument("--subject", required=True, help="comma-separated subject parcels")
    ap.add_argument("--radius", type=int, default=700, help="poloměr stažení parcel [m]")
    ap.add_argument("--rings", type=int, default=2, help="kolik prstenců sousedů (1=přímo u hranice)")
    ap.add_argument("--near", type=float, default=25.0, help="parcely do N m od bloku (přes cestu) = prstenec 1~")
    ap.add_argument("--min-area", type=int, default=300, help="ignoruj kandidáty pod N m²")
    ap.add_argument("--no-owner", action="store_true")
    ap.add_argument("--no-lpis", action="store_true")
    ap.add_argument("--no-spu", action="store_true")
    ap.add_argument("--out", required=True, help="output base path")
    a = ap.parse_args()

    lat, lon = (float(x) for x in a.gps.split(","))
    base = os.path.expanduser(a.out)
    os.makedirs(os.path.dirname(os.path.abspath(base)), exist_ok=True)

    log(f"[1/6] RUIAN parcely r={a.radius} m …")
    parcels = ruian_parcels(lat, lon, a.radius)
    log(f"      {len(parcels)} parcel")
    proj = make_proj(lat, lon)
    for p in parcels:
        p["m"] = shapely.transform(p["geom"], proj)

    want = {parse_label(s) for s in a.subject.split(",") if s.strip()}
    if not want:
        sys.exit("--subject must name at least one parcel")
    subj = [p for p in parcels if p["ku"] == a.ku and (p["typ"], p["label"]) in want]
    missing = want - {(p["typ"], p["label"]) for p in subj}
    if missing:
        sys.exit(f"subject parcels not found: {sorted(missing)}")
    subj_ids = {p["id"] for p in subj}
    block = unary_union([p["m"] for p in subj])
    ctx = ku_context(a.ku)
    log(f"      subjekt {len(subj)} parcel, {sum(p['vymera'] for p in subj):.0f} m², "
        f"k.ú. {ctx['ku_nazev']} {a.ku}, obec {ctx['obec']}, okres {ctx['okres']}")

    # rings
    log(f"[2/6] sousedé (prstence ≤{a.rings}, near {a.near} m) …")
    cand, seen = [], set(subj_ids)
    frontier = block
    for ring in range(1, a.rings + 1):
        ring_members = []
        for p in parcels:
            if p["id"] in seen:
                continue
            dist = p["m"].distance(frontier)
            if dist > a.near:
                continue
            contact = 0.0
            if dist < 0.5:
                contact = p["m"].boundary.intersection(frontier.boundary).length
                if contact < 0.5:  # vertices do not coincide exactly -> tolerant match
                    contact = p["m"].boundary.buffer(0.3).intersection(frontier.boundary).length
            cls, grazeable = classify(p)
            p.update({"ring": f"{ring}" if dist < 0.5 else f"{ring}~", "dist": round(dist, 1),
                      "contact": round(contact, 0), "cls": cls, "grazeable": grazeable})
            ring_members.append(p)
            seen.add(p["id"])
        cand += ring_members
        nxt = [p["m"] for p in ring_members if p["grazeable"] and p["vymera"] >= a.min_area]
        if not nxt:
            break
        frontier = unary_union(nxt)
    log(f"      {len(cand)} sousedních parcel, z toho spásatelných "
        f"{sum(1 for p in cand if p['grazeable'])}")

    # owner type
    if not a.no_owner:
        log("[3/6] typ vlastníka (ČÚZK KN WMS char_vl_*) …")
        pad = 0.0015
        xs = [p["geom"].bounds for p in cand + subj]
        bbox = (min(b[1] for b in xs) - pad, min(b[0] for b in xs) - pad,
                max(b[3] for b in xs) + pad, max(b[2] for b in xs) + pad)
        imgs, size = owner_type_map(bbox)
        for p in cand + subj:
            p["owner"] = sample_owner(imgs, size, bbox, p["geom"])
    else:
        for p in cand + subj:
            p["owner"] = "-"

    # LPIS
    if not a.no_lpis:
        targets = [p for p in cand if p["grazeable"] and p["vymera"] >= a.min_area]
        log(f"[4/6] LPIS uživatel pro {len(targets)} kandidátů …")
        for p in targets:
            rp = p["geom"].representative_point()
            try:
                p["lpis"] = lpis_user(rp.y, rp.x)
            except Exception as e:  # noqa
                p["lpis"] = {"error": str(e)}
            time.sleep(0.3)

    # SPÚ
    spu = {}
    if not a.no_spu:
        log(f"[5/6] SPÚ nabídky okres {ctx['okres']} / k.ú. {ctx['ku_nazev']} …")
        spu = spu_offers(ctx["okres"], ctx["ku_nazev"])
        offered = {}
        for typ, rec in spu.items():
            for n in rec.get("parcely", []):
                no = spu_parcel_no(n.get("NabidkyParcela"))
                if no:
                    offered.setdefault(no, []).append(typ)
        for p in cand:
            p["spu_nabidka"] = ",".join(offered.get(p["label"], [])) if p["ku"] == a.ku else ""

    # route verdict per parcel
    def route(p):
        o = p.get("owner", "?")
        lp = p.get("lpis") or {}
        farmed = bool(lp.get("uzivatel"))
        if o.startswith("stát"):
            r = "PACHT od SPÚ (žádost; ověř na LV příslušnost hospodařit = SPÚ) / koupě jen přes nabídku SPÚ"
        elif o.startswith("obec"):
            r = "PACHT od obce (rada/zastupitelstvo) / koupě záměr obce"
        elif o.startswith("kraj"):
            r = "pacht od kraje (nepravděpodobné)"
        elif o.startswith("soukrom"):
            r = "Ověř vlastníka a dostupnost"
        else:
            r = "vlastník ? — ověřit na LV"
        if p.get("spu_nabidka"):
            r = "★ V NABÍDCE SPÚ (" + p["spu_nabidka"] + ") — " + r
        if farmed:
            r += f"; LPIS uvádí uživatele {lp['uzivatel']}; ověř jeho práva"
        return r

    for p in cand:
        p["route"] = route(p)

    # totals
    def tot(flt):
        return sum(p["vymera"] for p in cand if flt(p))
    g1 = tot(lambda p: p["grazeable"] and p["ring"].startswith("1"))
    g_all = tot(lambda p: p["grazeable"])
    by_owner = {}
    for p in cand:
        if p["grazeable"]:
            by_owner[p["owner"]] = by_owner.get(p["owner"], 0) + p["vymera"]
    farmed_area = tot(lambda p: p["grazeable"] and (p.get("lpis") or {}).get("uzivatel"))
    subj_area = sum(p["vymera"] for p in subj)
    subj_graz = sum(p["vymera"] for p in subj if classify(p)[1])

    # ---------- write ----------
    log("[6/6] zápis …")
    rows = sorted(cand, key=lambda p: (p["ring"][0], not p["grazeable"], -p["vymera"]))

    def label(p):
        return ("st." if p["typ"] == 1 else "") + p["label"] + ("" if p["ku"] == a.ku else f" (k.ú. {ku_name(p['ku'])})")

    def lpis_str(p):
        lp = p.get("lpis") or {}
        if lp.get("uzivatel"):
            return f"{lp['uzivatel']} (DPB {lp['dpb']}, {lp['vymera_ha'].strip()} ha)"
        return "unknown"

    md = []
    md.append(f"# ID{a.id} — rozšíření pastvin + pacht (kandidáti), {dt.date.today().isoformat()}")
    md.append(f"k.ú. {ctx['ku_nazev']} {a.ku}, obec {ctx['obec']}, okres {ctx['okres']}; GPS {lat},{lon}. "
              f"Subjekt {len(subj)} parcel = {fmt(subj_area)} m² (z toho spásatelné {fmt(subj_graz)} m²).")
    md.append("Zdroje: ČÚZK RUIAN, ČÚZK KN WMS, LPIS a SPÚ. Dostupnost a vlastnictví ověř samostatně.")
    md.append("")
    md.append(f"## TOP kandidáti (spásatelné ≥ {a.min_area} m², prstenec 1 = u hranice, 1~ = přes cestu do "
              f"{a.near:.0f} m, 2 = za nimi)")
    md.append("| parcela | prst. | kontakt m | m² | druh | vlastník typ | LPIS uživatel | SPÚ nab. | cesta |")
    md.append("|---|---|---|---|---|---|---|---|---|")
    for p in rows:
        if not p["grazeable"] or p["vymera"] < a.min_area:
            continue
        md.append(f"| {label(p)} | {p['ring']} | {p['contact']:.0f} | {fmt(p['vymera'])} | {p['cls']} | {p['owner']} | "
                  f"{lpis_str(p)} | {p.get('spu_nabidka', '')} | {p['route']} |")
    md.append("")
    md.append("## Všechny sousední parcely (prstenec 1 celý vč. bariér; prstenec 2 jen spásatelné ≥ "
              f"{a.min_area} m²)")
    md.append("| parcela | prst. | kontakt m | m² | druh (pastva) | vlastník typ | LPIS uživatel (DPB) | SPÚ nab. |")
    md.append("|---|---|---|---|---|---|---|---|")
    for p in rows:
        if not p["ring"].startswith("1") and (not p["grazeable"] or p["vymera"] < a.min_area):
            continue
        md.append(f"| {label(p)} | {p['ring']} | {p['contact']:.0f} | {fmt(p['vymera'])} | {p['cls']} | {p['owner']} | "
                  f"{lpis_str(p)} | {p.get('spu_nabidka', '')} |")
    md.append("")
    md.append("## Součty (spásatelné = TTP + orná + sad + ostatní/neplodná,zeleň,jiná,mez)")
    md.append(f"- Prstenec 1 (přímo u hranice / do {a.near:.0f} m): **{g1/10000:.2f} ha**")
    md.append(f"- Všechny prstence ≤{a.rings}: **{g_all/10000:.2f} ha**")
    md.append("- Podle typu vlastníka: " + "; ".join(f"{k} {v/10000:.2f} ha" for k, v in sorted(by_owner.items(), key=lambda x: -x[1])))
    by_owner1 = {}
    for p in cand:
        if p["grazeable"] and p["ring"].startswith("1"):
            by_owner1[p["owner"]] = by_owner1.get(p["owner"], 0) + p["vymera"]
    md.append("- Prstenec 1 podle vlastníka: " + "; ".join(f"{k} {v/10000:.2f} ha" for k, v in sorted(by_owner1.items(), key=lambda x: -x[1])))
    md.append(f"- LPIS uvádí uživatele pro nejméně {farmed_area/10000:.2f} ha; chybějící výsledky jsou neznámé.")
    md.append("")
    md.append("## SPÚ nabídky v k.ú. (živě)")
    for typ, rec in spu.items():
        if "error" in rec:
            md.append(f"- {rec['label']}: CHYBA {rec['error']}")
            continue
        md.append(f"- {rec['label']}: termín {rec.get('datum')} (id {rec.get('termin')}, aktuální={rec.get('aktualni')}); "
                  f"okres v nabídce: {rec.get('okres_v_nabidce')}; k.ú. {ctx['ku_nazev']} v nabídce: {rec.get('ku_v_nabidce')}; "
                  f"parcel v k.ú.: {len(rec.get('parcely', []))}")
        for n in rec.get("parcely", []):
            md.append(f"  - {n.get('NabidkyParcela')} — {n.get('NabidkyVymera')} m², {n.get('NabidkyDruh')} "
                      f"{('/ ' + n['NabidkyVyuziti']) if n.get('NabidkyVyuziti') else ''}; "
                      f"cena {n.get('NabidkyCena') or '-'}; nájem: {n.get('NabidkyNajem') or '-'}; stav {n.get('NabidkaStav') or '-'}")
    md.append("")
    md.append("## Verdikt (doplní executor)")
    md.append("- Ověř územní plán, přístup a dostupnost každé parcely.")
    with open(base + ".md", "w", encoding="utf-8") as f:
        f.write("\n".join(md) + "\n")

    def strip(p):
        q = {k: v for k, v in p.items() if k not in ("geom", "m")}
        q["centroid"] = [round(p["geom"].representative_point().y, 6), round(p["geom"].representative_point().x, 6)]
        q["vdp_url"] = f"https://vdp.cuzk.cz/vdp/ruian/parcely/{p['id']}"
        return q
    with open(base + ".json", "w", encoding="utf-8") as f:
        json.dump({"id": a.id, "gps": [lat, lon], "ctx": ctx, "subject": [strip(p) for p in subj],
                   "candidates": [strip(p) for p in cand], "spu": spu,
                   "totals": {"ring1_grazeable_m2": g1, "all_grazeable_m2": g_all, "by_owner_m2": by_owner,
                              "ring1_by_owner_m2": by_owner1,
                              "farmed_m2": farmed_area, "subject_m2": subj_area, "subject_grazeable_m2": subj_graz},
                   "generated": dt.datetime.now(dt.timezone.utc).isoformat()}, f, ensure_ascii=False, indent=1)
    print("\n".join(md))
    log(f"→ {base}.md / .json")


if __name__ == "__main__":
    main()
