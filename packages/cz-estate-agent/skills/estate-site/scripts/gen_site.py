#!/usr/bin/env python3
"""estate-site generator: builds the group's property site from local data.

  gen_site.py [--config FILE] build [YYYY-MM-DD]   write index.html, estate.css, local.css
  gen_site.py [--config FILE] new PATH TITLE [--listing ID]   write a page skeleton

Config (JSON, default ~/estate/site.json) and the listing table are the
group's own files. This script, components/ and estate.css are package files.
"""
import argparse
import datetime
import glob
import html
import json
import os
import re
import shutil
import string
import sys

SKILL = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COMPONENTS = os.path.join(SKILL, "components")

DEFAULTS = {
    "title": "Nemovitosti",
    "lang": "cs",
    "intro": "",
    "db": "~/estate/db.md",
    "site": "~/public_html",
    "links": [],  # [{"href": "...", "name": "...", "note": "..."}] cards under "more"
    # zone colour = worst of drive time, area and price; a null limit is ignored
    "limits": {"minutes_green": 40, "minutes_yellow": 50, "area_green": 10000, "price_red": None, "page_bytes": 10000, "valuation_sections": 8},
    "labels": {
        "updated": "aktualizováno",
        "active": "aktivních",
        "valuations": "Ocenění",
        "priority": "★ Prioritní",
        "new": "🆕 Nové",
        "listings": "Aktivní nemovitosti",
        "more": "Další",
        "reports": "Archiv reportů",
        "seen": "spatřeno",
        "min": "min",
        "gone": "pryč",
        "alive": "aktivní",
        "valuation": "ocenění",
        "generated": "Generováno z",
    },
}
REMOVED = ("staženo", "prodáno", "prodano", "neaktivní", "neaktivni", "vyřazeno", "vyrazeno")
ZONES = {0: ("zel", "🟢"), 1: ("zlu", "🟡"), 2: ("cer", "🔴")}


def load_config(path):
    cfg = json.loads(json.dumps(DEFAULTS))
    path = os.path.expanduser(path)
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            local = json.load(f)
        if "criteria" in local and not isinstance(local["criteria"], dict):
            raise ValueError("criteria must be a JSON object")
        for key, val in local.items():
            if isinstance(cfg.get(key), dict):
                cfg[key].update(val)
            else:
                cfg[key] = val
    return cfg


def render(template, **fields):
    with open(os.path.join(COMPONENTS, template), encoding="utf-8") as f:
        return string.Template(f.read()).substitute(fields)


def esc(text):
    return html.escape(text or "", quote=True)


def digits(text):
    d = re.sub(r"\D", "", text or "")
    return int(d) if d else None


def parse_db(path):
    """Rows of `| ID | place | url | price | area m2 | min | first seen | verified | status |`."""
    rows = []
    with open(os.path.expanduser(path), encoding="utf-8") as f:
        for line in f:
            if not line.strip().startswith("|"):
                continue
            c = [x.strip() for x in line.split("|")]
            if len(c) < 11 or not re.match(r"^\d+$", c[1]):
                continue
            status = c[9]
            low = status.lower()
            rows.append(dict(id=c[1], place=c[2], url=c[3], price=c[4], area=c[5],
                             minutes=c[6], first=c[7], status=status,
                             active=not any(k in low for k in REMOVED),
                             fresh="nové" in low or "new" in low, top="★" in status))
    return rows


def zone_rank(row, limits):
    ranks = []
    m = digits(row["minutes"])
    if m is not None:
        ranks.append(0 if m < limits["minutes_green"] else 1 if m <= limits["minutes_yellow"] else 2)
    a = digits(row["area"])
    if a is not None and limits["area_green"]:
        ranks.append(0 if a >= limits["area_green"] else 2)
    p = digits(row["price"])
    if p is not None and limits["price_red"]:
        ranks.append(2 if p > limits["price_red"] else 0)
    return max(ranks) if ranks else 3


def seen_key(row):
    m = re.search(r"\d{4}-\d{2}-\d{2}", row["first"] or "")
    return m.group() if m else "0000-00-00"


def order(rows, limits):
    rows.sort(key=seen_key, reverse=True)  # newest first, then stable by zone
    rows.sort(key=lambda r: zone_rank(r, limits))
    return rows


def plain(markup):
    markup = re.sub(r"<(script|style)[^>]*>.*?</\1>", " ", markup, flags=re.S | re.I)
    return re.sub(r"\s+", " ", html.unescape(re.sub(r"<[^>]+>", " ", markup))).strip()


def listing_card(row, cfg, valuations):
    lab, lim = cfg["labels"], cfg["limits"]
    zone, emoji = ZONES.get(zone_rank(row, lim), ("", ""))
    meta = esc(row["area"] + " m²" if re.search(r"\d", row["area"]) else row["area"])
    if re.search(r"\d", row["minutes"]):
        meta += f" · {emoji} {esc(row['minutes'])} {lab['min']}"
    elif row["minutes"]:
        meta += f" · {esc(row['minutes'])}"
    meta += f" · ID {row['id']}"
    if re.search(r"\d", row["first"]):
        meta += f' · <span class="seen">{lab["seen"]} {esc(row["first"])}</span>'
    if row["id"] in valuations:
        meta += f' · <a href="{esc(valuations[row["id"]]["href"])}">→ {lab["valuation"]}</a>'
    badge = '<span class="badge top">★</span>' if row["top"] else ""
    # the card is a div: a nested link inside a wrapping link is invalid HTML
    return render("card.html", href=esc(row["url"]), attrs=' target="_blank" rel="noopener"',
                  name=esc(row["place"]), badge=badge, price=esc(row["price"]), zone=zone, meta=meta)


def page_date(path, markup):
    m = re.search(r'<div class="nav"[^>]*>(.*?)</div>', markup, re.S)
    nav = plain(m.group(1)) if m else ""
    d = re.search(r"(\d{1,2})\.(\d{1,2})\.(\d{4})", nav)
    if d:
        return f"{d.group(3)}-{int(d.group(2)):02d}-{int(d.group(1)):02d}"
    d = re.search(r"\d{4}-\d{2}-\d{2}", nav)
    return d.group() if d else datetime.date.fromtimestamp(os.path.getmtime(path)).isoformat()


def find_valuations(site):
    """Pages that declare <meta name="listing" content="ID">, one per ID (newest wins)."""
    found = {}
    for path in sorted(glob.glob(os.path.join(site, "**", "*.html"), recursive=True)):
        rel = os.path.relpath(path, site)
        if rel == "index.html":
            continue
        with open(path, encoding="utf-8", errors="replace") as f:
            markup = f.read()
        m = re.search(r'<meta\s+name="listing"\s+content="(\d+)"', markup)
        if not m:
            continue
        big = re.search(r'<(div|p)\s+class="big[^"]*"[^>]*>(.*?)</\1>', markup, re.S)
        verdict = plain(big.group(2))[:140] if big else ""
        entry = dict(href=rel, date=page_date(path, markup), verdict=verdict)
        if m.group(1) not in found or entry["date"] > found[m.group(1)]["date"]:
            found[m.group(1)] = entry
    return found


def write_page(path, root, cfg, title, body, meta=""):
    with open(path, "w", encoding="utf-8") as f:
        f.write(render("page.html", lang=cfg["lang"], meta=meta, title=esc(title), root=root, body=body))


def build(cfg, date):
    lab, lim = cfg["labels"], cfg["limits"]
    site = os.path.expanduser(cfg["site"])
    os.makedirs(site, exist_ok=True)
    shutil.copyfile(os.path.join(COMPONENTS, "estate.css"), os.path.join(site, "estate.css"))
    local = os.path.join(site, "local.css")
    if not os.path.exists(local):  # the group's own tuning; never overwritten
        with open(local, "w", encoding="utf-8") as f:
            f.write("/* Tune the look here: override the --variables of estate.css or any rule. */\n")

    rows = parse_db(cfg["db"])
    active = [r for r in rows if r["active"]]
    top = order([r for r in active if r["top"]], lim)
    fresh = order([r for r in active if r["fresh"] and not r["top"]], lim)
    rest = order([r for r in active if not r["fresh"] and not r["top"]], lim)
    valuations = find_valuations(site)
    reports = sorted(glob.glob(os.path.join(site, "reports", "*.md")), reverse=True)[:8]

    body = [f'<div class="nav">{lab["updated"]} {date} · {len(active)} {lab["active"]}</div>',
            f'<h1>{esc(cfg["title"])}</h1>']
    if cfg["intro"]:
        body.append(f'<p class="sub">{esc(cfg["intro"])}</p>')
    for head, group in ((lab["priority"], top), (f'{lab["new"]} ({len(fresh)})', fresh),
                        (f'{lab["listings"]} ({len(rest)})', rest)):
        if group:
            body += [f"<h2>{head}</h2>"] + [listing_card(r, cfg, valuations) for r in group]
    if cfg["links"]:
        body.append(f'<h2>{lab["more"]}</h2>')
        for link in cfg["links"]:
            note = f'<div class="meta">{esc(link.get("note", ""))}</div>' if link.get("note") else ""
            body.append(f'<a class="item" href="{esc(link["href"])}"><span class="name">{esc(link["name"])}</span>{note}</a>')
    if reports:
        body.append(f'<h2>{lab["reports"]}</h2>')
        for p in reports:
            stem = os.path.splitext(os.path.basename(p))[0]
            body.append(f'<a class="item" href="reports/{esc(os.path.basename(p))}"><span class="name">{esc(stem)} →</span></a>')
    body.append(f'<p class="foot">{lab["generated"]} <code>{esc(os.path.basename(cfg["db"]))}</code> · {date} · {len(active)} {lab["active"]}</p>')
    out = os.path.join(site, "index.html")
    write_page(out, "", cfg, cfg["title"], "\n".join(body))
    print(f"wrote {out}: {len(active)} active ({len(top)} top, {len(fresh)} new, {len(rest)} rest), "
          f"{len(valuations)} valuations, {len(reports)} reports; css: estate.css + local.css")


def new_page(cfg, rel, title, listing):
    site = os.path.expanduser(cfg["site"])
    path = os.path.join(site, rel if rel.endswith(".html") else rel + ".html")
    if os.path.exists(path):
        sys.exit(f"refusing to overwrite {path}")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    depth = os.path.relpath(path, site).count(os.sep)
    root = "../" * depth
    meta = f'<meta name="listing" content="{int(listing)}">\n' if listing else ""
    today = datetime.date.today()
    body = (f'<div class="nav"><a href="{root or "./"}">← {esc(cfg["title"])}</a> · {today.day}.{today.month}.{today.year}</div>\n'
            f'<h1>{esc(title)}</h1>\n<p class="sub"></p>\n')
    write_page(path, root, cfg, title, body, meta)
    print(f"wrote {path}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--config", default="~/estate/site.json")
    sub = ap.add_subparsers(dest="cmd")
    b = sub.add_parser("build")
    b.add_argument("date", nargs="?", default=datetime.date.today().isoformat())
    n = sub.add_parser("new")
    n.add_argument("path")
    n.add_argument("title")
    n.add_argument("--listing")
    args = ap.parse_args()
    cfg = load_config(args.config)
    if args.cmd == "new":
        new_page(cfg, args.path, args.title, args.listing)
    else:
        build(cfg, getattr(args, "date", datetime.date.today().isoformat()))


if __name__ == "__main__":
    main()
