# Checks — before every delivery (Round 4)

Run `python3 ~/.claude/skills/itinerary/scripts/trip_lint.py ~/public_html/<trip> --root ~/public_html` for the whole trip; `--root` lets links to shared assets and sibling trips resolve. It checks HTML tag balance, local pages and assets, empty links, stylesheet classes, navigation, page structure, Mapy and OSM URLs, map link text, phone links, the meals the timeline reaches, meal ratings and fallback labels, opening hours against the meal time, and meal selection caps. Inline styles are permitted for minimal layout. The `page_check.py` entry point delegates to this same checker.

Run `python3 ~/.claude/skills/itinerary/scripts/link_check.py <pages>` to inspect map links in the browser: after an edit, the pages the change touched; on a first build or a full audit, every page of the trip, `places.html` included. It opens a link shared by several pages once and fails on `mapy.cz` and short links. A browser error or timeout is a failed check. Inspect rendered routes and a 390 px page separately; static checks cannot verify route geometry or source truth.

At 390×844, read a screenshot top to bottom: the first choice card is fully visible (`pages.md` § First screen). Measure link heights with one DOM query over `document.querySelectorAll('a')`, minus only the inline exception of `web-deployment.md` § Links; report the selector with the result. NEVER say "all links" after measuring a narrower selector.

When a stop, date, route or venue changes, recheck its hours, route, affected links and pages. For independent judgment use core `oracle` with Codex model `gpt-6-astra` and reasoning effort `high`.

Run all of it after every change, not only the first build. After a page or directory moves, lint every trip that links to it, archived pages included.

## Venues and car parks

Every restaurant, café, museum, attraction and car park on the page is
a `db/` row with address and postcode, phone, website or listing when
one exists, place links per kind (`mapy-links.md`) — or `not found`
written out after two sources tried. Compare the page facts with the private rows.
Keep database files, decisions and personal contacts outside `~/public_html/`.

## Trails

Before a trail, car park or walking distance goes on a page:
```bash
python3 ~/.claude/skills/itinerary/scripts/osm_trails.py LAT1 LON1 LAT2 LON2
```
Lists every marked route in the box between the two points (`[BOTH]` =
some member way passes within 300 m of each point; it does not prove one
connected walk) with colour (`osmc:symbol`), length of the member ways in
the box, surface mix, and car parks (ways and nodes) with `fee=` /
`charge=` / `capacity=`. Write colour and relation id from that output;
the walk's length and climb from the rendered mapy.com route, never the
member-way length; when mapy.com is
reachable, confirm the marking there and cite both. A source that
disagrees with the map is named as disagreeing in the row.

## Page

```bash
cd ~/public_html/<trip>
for f in *.html; do
  t=$(grep -m1 '<title>' "$f" | sed 's/.*<title>\(.*\)<\/title>.*/\1/')
  h=$(grep -m1 '<h1>' "$f" | sed 's/.*<h1>\(.*\)<\/h1>.*/\1/')
  [ "$t" = "$h" ] || echo "title/h1 differ: $f"
  echo "$f nav=$(grep -c '<nav' "$f") div=$(grep -o '<div' "$f" | wc -l)/$(grep -o '</div>' "$f" | wc -l)"
  curl -s -o /dev/null -w "$f %{http_code}\n" "https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/<trip>/$f"
done
```
- 200 on every page; one `<nav>`; open and close `<div>` counts equal.
- Stylesheet and script paths, skeleton, block order: `web-deployment.md`.
- Index card header = "Day N — Weekday, Mon D" (`web-deployment.md`); its text
  names the page's stops in order.
- Timeline times do not overlap; Plan A ↔ Plan B cross-references hold;
  transport facts agree across mentions.
- One `checked-line` under the subtitle; sources once in `.source-note`;
  no inline citations in the plan text (`pages.md` § Exposition).
