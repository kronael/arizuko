# Checks — before every delivery (Round 4)

Run all of it after every change, not only the first build.

## Page check script (exit 0 or no delivery)

```bash
python3 ~/.claude/skills/itinerary/scripts/page_check.py ~/public_html/<trip>/day*.html
```
Fails on: title ≠ h1, `<div>` imbalance, `<nav>` count ≠ 1, a `?q=`
anywhere, an empty `href`, a `mapy.cz` link, a phone outside a `tel:`
link, `checked-line` count ≠ 1, a missing `source-note`, a missing meal
item (Breakfast / Lunch / Coffee / Dinner as `HH:MM — <Meal>`), a Food
heading outside the timeline, and a food `<li>` that is over 10 words
before the first `·`, has no `Google x.x` / `unrated`, no `tel:` link
(or `phone: not found`), no Google link, no mapy link, or a
`source=coor` pin. Warns when `Story.` boxes < stops, `if full` < drive
items, or a food line shows no open-today proof (a time) — fix or
explain each warning in the delivery.

## Venues and car parks

Every restaurant, café, museum, attraction and car park on the page is
a `db/` row with address and postcode, phone, website or listing when
one exists, place links per kind (`mapy-links.md`) — or `not found`
written out after two sources tried. Public copies: `diff -q ~/workspace/trips/<trip>/db/*.md
~/public_html/<trip>/db/` is empty.

## Trails

Before a trail, car park or walking distance goes on a page:
```bash
python3 ~/.claude/skills/itinerary/scripts/osm_trails.py LAT1 LON1 LAT2 LON2
```
Lists every marked route in the box between the two points (`[BOTH]` =
passes both) with colour (`osmc:symbol`), length of the member ways in
the box, surface mix, and car parks (ways and nodes) with `fee=` /
`charge=` / `capacity=`. Write colour, length and relation id from that
output; climb from the mapy.com route profile; when mapy.com is
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
