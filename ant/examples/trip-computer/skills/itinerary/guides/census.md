# Census — the trip database (Round 0)

The census is a research workflow, not a script. A script produces the
skeleton (step 1); every other step is work in the browser and the
search engine, as a person would do it. The result is the database the
whole trip is planned from. Rule set and costs: `../SKILL.md`.

## Result

`~/workspace/trips/<trip>/db/` — five tables, one row per place — and
`~/public_html/<trip>/places.html` published from them (sights by
distance band, then the full list; links to the tables).

| file | rows | score |
| --- | --- | --- |
| `sights.md` | rocks, viewpoints, peaks, ruins, castles, museums, parks, waterfalls, caves | POI |
| `food.md` | restaurants, cafés, bakeries, bars | Food |
| `parking.md` | car parks that serve a sight or a town | Parking (cost first) |
| `trails.md` | marked routes and loops between rows of `sights.md` | Trail |
| `kids.md` | playgrounds, toilets, indoor fallbacks (pool, museum, play room) | Kids |

## Row

Every row carries its place links in the order of `guides/mapy-links.md`
(by kind: food → Google place then mapy entity; trails and car parks →
mapy entity then OSM; GPS pin last) and its `link_kind`.

Every row: name · km from base · coordinates · place links
(`mapy-links.md`) · address with postcode · phone · website · hours for
the trip dates · price · one source per fact · `checked <date>` · score
with reasons · one-line verdict. An empty mandatory field reads
`not found`, never blank.

- `sights.md` (Round 2 adds these to the skeleton) `kind` (`hiking` /
  `town` / `indoor` / `roadside`),
  `story` (3–6 verified sentences: origin, history, what matters — no
  curiosities) and `refs` (the links), both from `/find <poi>` →
  `~/facts/<poi>.md`. A `hiking` row
  links two `trails.md` rows: `short loop` (≤ 1 h 30 at the group's pace) and
  `long loop` (half a day); `none` is valid for the long loop when the
  terrain only allows a small rounding.
- `parking.md` adds `serves` (sight or street), walk to it in metres and
  minutes from Google or mapy walking directions (never straight-line),
  `surface`, `capacity` as a count with source or `≈n (OSM outline / 25 m²)`.
- `trails.md` adds colour, OSM relation id, measured length, climb,
  surface mix, stroller yes / partly (to where) / no, and the mapy.com
  foot-route URL from `mapyrc.py`.

## Steps

1. **Skeleton from OpenStreetMap.**
   `python3 ~/.claude/skills/itinerary/scripts/osm_census.py --lat … --lon … --radius 10 --out …/db`
   Named objects with coordinates, OSM address, phone, hours, fee,
   capacity, trail colour and length — candidates, nothing verified.
   The radius covers every planned stop: a stop outside it gets
   `--also LAT,LON` (a 2 km circle; `km` stays from the base) and steps
   3 and 4 around itself before its page. A car park row carries
   `near:` = the nearest named sight within 600 m — the `serves`
   candidate.
2. **The user's own links.** Resolve every map link the user sent
   (`curl -sIL` a `maps.app.goo.gl` link, read the place from the final
   URL). Each becomes a row marked `user pick` before any other work.
3. **mapy.com.** Open each sight and trail on
   `https://mapy.com/en/turisticka` (`agent-browser`): confirm trail
   colour and marking, read mapy's rating and description, take the
   route-planner distance for walks and drives (`mapyrc.py` builds the
   URL). Write `mapy ✓` or the discrepancy in the sources column.
4. **Google Maps.** Open each sight, restaurant, café and car park:
   rating, review count, hours for the exact trip dates, phone,
   "permanently / temporarily closed", price level, car-park photos
   (surface, barrier, pay machine). Match the entry to the row by phone
   or address (a guesthouse and its restaurant are two entries); cookies
   and the limited view: `mapy-links.md` § Disambiguation.
5. **TripAdvisor.** Every restaurant and paid attraction: rating, count,
   rank in town, last three review dates, mentions of children (high
   chair, play corner, pram space). Restaurant Guru and Yelp when
   TripAdvisor has fewer than 30 reviews. A refused host (§ Egress) =
   the step is skipped and the row says so; never a number from a
   search snippet labelled as the platform.
6. **Official websites.** Every paid or timed place (museum, mine,
   fortress, castle, pump room, park): price list, season table, booking
   rules, phone, parking fee. Quote the page and its date. Blogs and
   listings fill gaps only, labelled `blog`.
7. **Tourist office / town site.** Events on the trip dates, closures,
   parking zones and fees.
8. **Trails on the map data.** Every walk:
   `python3 ~/.claude/skills/itinerary/scripts/osm_trails.py LAT1 LON1 LAT2 LON2` → colour,
   relation id, length of the member ways in the box, surface mix, car
   parks (ways and nodes) with fee / charge / capacity. Climb: the
   elevation profile of the mapy.com route (OSM trails carry no `ele`).
   Loops: `pages.md` § Loops.
9. **Score every row** with its table's system, write the verdict, mark
   `checked <date>`.
10. **Publish `places.html`** and keep the tables as the only source
    for day pages: a day page copies its venues from rows.

## Rating systems

**POI (sights), 1–5 each, verdict from the sum**
- Draw: what you see; Google / mapy / TripAdvisor ratings and counts
  quoted. 5 = landmark, 1 = a sign by the road.
- Child fit: 5 = stroller all the way, 3 = carrier, 1 = steps / exposure.
- Effort: door-to-door incl. parking; 5 = under 30 min, 1 = half a day.
- Season fit: open and sensible on the dates and in that weather;
  indoor = 5 in rain.
- Cost: 5 = free, 1 = the top price band for the region.
Verdict: "Sun morning, from the house, carrier" / "skip: closed Nov".

**Food (restaurants, cafés)**
- Ratings: Google x.x (n) · TripAdvisor x.x (n, rank) · mapy x.x; flag
  < 30 reviews or a 0.5 gap between platforms. Listed from 4.0 average
  (cafés 4.5); below only as a labelled fallback.
- Open-today proof: hours ON the trip date from Google, the website or
  a call, quoted with source; none = `unconfirmed` (spa or mountain
  town Nov–Mar: assume seasonal closure). Open at the meal time.
- Child fit: high chair, play corner, pram space, kids menu — from
  reviews or a call; `unknown` if neither.
- Price band $ / $$ / $$$ with a quoted main-course price when found.
- Route fit: metres from the day's route; a detour over 5 min noted.
Verdict: "lunch Fri 13:00, pizza for the child" / "fallback only".

**Parking (car parks) — cost first**
- Cost: exact fee per hour / day or `free`, with source and date; winter
  changes (barrier down, booth closed) noted.
- Capacity and fill risk on the trip date (weekend, season).
- Walk to the sight it serves: metres, minutes, climb.
- Surface and access: asphalt / gravel / mud; height bar; disabled bay.
- Facilities: toilet, lights, pay method (card / coins / app).
Verdict: "park here, 8.00/day, card" / "free, 7 places, arrive before 10".

**Trail (walks)**
- Marking: colour and OSM relation id; mapy confirmation.
- Length one way (OSM-measured) and climb.
- Surface shares: asphalt / gravel / path / rock / boardwalk.
- Stroller: yes / partly (to where) / no.
- Time at child pace (3 km/h + 15 min per 100 m climb) and the safe
  turnaround point.
Verdict: "carrier, 1 h 30 out and back" / "stroller to the first lake".

**Kids (playgrounds, toilets, indoor fallbacks)**
- Open in the season, fenced, age range, toilet within 200 m, indoor
  option within 10 min drive for rain.

## Egress

Hosts the census needs: `api.openstreetmap.org`,
`nominatim.openstreetmap.org`, `mapy.com`, `*.mapy.com`, `mapy.cz`,
`*.mapy.cz`, `www.google.com`, `maps.google.com`, `maps.app.goo.gl`,
`goo.gl`, `maps.gstatic.com`, `www.gstatic.com`,
`lh3.googleusercontent.com`, `consent.google.com`,
`www.tripadvisor.com`, `<lang>.wikipedia.org` (stories), `api.met.no` and
`api.open-meteo.com` (Round 2 weather), and the official sites and town
sites of the places (town parking fees live there; a town site may
refuse). This is the only egress list in the skill.

Precheck before step 3 and again before Round 2:
```bash
for h in api.openstreetmap.org nominatim.openstreetmap.org mapy.com www.google.com maps.app.goo.gl www.tripadvisor.com en.wikipedia.org api.met.no api.open-meteo.com; do
  printf '%s ' "$h"; curl -s -o /dev/null -w '%{http_code}\n' "https://$h/"
done
```
`403` on every path or `000` = the host is not on the allowlist. Send
the user the exact ask in the same message, then continue with OSM and
label the page "corroborated on OSM, not Mapy":
`/root network_allow('<folder>', '<host>')` (folder from
`$ARIZUKO_GROUP_FOLDER`).

## Photos and reviews

Record each platform's rating, review count, observation date and recent reviews separately. Inspect photos for practical details such as access and seating. Mark missing counts or photos as unknown.
