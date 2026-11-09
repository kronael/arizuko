# Map links — place entries, pins and routes

## Place links — preference order

A link opens the place's own entry, never a bare coordinate when an
entry exists:
1. **mapy.com entity** — open `?q=<name>&x=&y=&z=17` in the browser; if
   mapy redirects to `source=osm&id=NNN` (or `source=firm&id=`) with a
   matching title within 200 m of the row, use that entity URL
   (`source=<src>&id=<id>&x=&y=&z=17`).
2. **OpenStreetMap object** — `https://www.openstreetmap.org/<node|way>/<id>`
   from the census tile or Nominatim, within 200 m.
3. **Google Maps place** — the `/maps/place/.../data=...` URL from a
   title-matched Maps search.
4. **GPS pin** — the `source=coor` form (§ GPS pin), only when no entry
   exists.

By kind (rule per kind): restaurants, cafés, bakeries,
museums and shops link to **Google Maps first** (reviews, hours, photos),
then a mapy entity with a rating; never an OSM object. Trails, rocks,
viewpoints, peaks, car parks and trailheads link to **mapy first**, then
OSM, then Google. Meal lines carry both: `· google · mapy`. Record the
kind in the row (`link_kind`). Cost: every link was a GPS
pin, so the reader saw "Position on the map" instead of the place, its
photos, hours and phone.

## Disambiguation

- Match the entry to the row by phone or address, never by name alone:
  a guesthouse and its restaurant are two Google entries (a restaurant
  and its guesthouse).
- Check the title and the distance (≤ 200 m from the row's coordinates)
  before taking an entity URL.
- mapy `source=osm&id=NNN` is mapy's own id, not the OSM node/way id
  ; take it from the browser address bar only.
- Open Google Maps and complete its consent flow in the browser. If consent
  or static hosts are refused, request access using `census.md` § Egress.
  If the limited view hides review counts or hours for the trip date,
  record those fields as unverified; verify them from another live source.

## GPS pin (last resort)

```
https://mapy.com/en/turisticka?source=coor&id=LON%2CLAT&x=LON&y=LAT&z=17
```

- `source=coor&id=LON,LAT` pins the exact coordinates ("Position on the
  map", Directions button). Lon first, comma encoded as `%2C`;
  `x`/`y`/`z` equal the pin; 4+ decimals. The link text carries the name.
- `?q=<name>&x=&y=` is NOT a pin: mapy searches the name and jumps to
  its own hit, overriding x/y (a "parking" link lands on the sight;
  an unmatched name lands anywhere).
- Google for a place with no entry:
  `https://www.google.com/maps/search/?api=1&query=LAT,LON`.

Coordinates: open the place on mapy.com in `agent-browser`, click the
exact result, read `x=`/`y=` from the address bar; fallback
`curl "https://nominatim.openstreetmap.org/search?format=json&limit=1&q=Name+Town"`
(`lon` → x, `lat` → y). Never from memory.

## Routes

Route-planner URL, generated — never typed:
```bash
python3 ~/.claude/skills/itinerary/scripts/mapyrc.py routes.json
# routes.json: {"fri_car": {"mode": "car", "points": [[LAT, LON], [LAT, LON], …]},
#               "fri_walk": {"mode": "hike", "points": [[LAT, LON], …]}}
# → one line per route: name, mode, max_err, URL (rc= encoder, port of jamhed/mapy coords.ts)
```
- Modes (`mrp={"c":…}`, browser-checked): `car` 111; `foot`
  131 = mapy "Fast" (road-first); `hike` 132 = "Outdoor" (marked
  trails) — loops and trail walks use `hike`; `bike` 121. Open the URL
  and read mapy's length and time onto the page.
- Every car route also as Google: the `/maps/dir/LAT,LON/…` URL that
  `mapyrc.py` prints for the same points — never a `dir/` by place name
  (Google geocodes a name the way mapy's `?q=` does).
- Top `.map-links` box: `🔁 Roundtrip` (mapy + Google), `🥾 Walk` /
  `Loop` routes, `📍 Waypoints`. Under each timeline item: the place
  link and the drive or walk segment that reaches it.

## What breaks

- `?q=Name` with or without coordinates (search, not a pin)
- a GPS pin where an entity exists; a venue linked to its guesthouse
- `source=osm&id=` built from an OSM id
- `mapy.cz`, `en.mapy.cz` domains; `mapy.cz/s/XXX` short links
- swapped `x=LAT&y=LON`; a hand-written `rc=` string
- a mapy car route without the Google route beside it
- a trail walk or loop in `foot` (131): the "Fast" profile may leave
  the marked trail; use `hike` (132)

## Route verification

Match each named place to its own map entry before building the route. Open the finished route in a browser and check that its path, named stops and distances agree with the page. Static URL checks do not verify the rendered route.
