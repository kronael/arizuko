---
name: itinerary
description: Plan and revise trips and day itineraries from a verified place database. NOT for standalone forecasts (use weather) or car purchases (use find-car).
when_to_use: 'plan a trip to X, what to see near Y, suggest trips for Friday, build the day page, rebuild the day, start later, the place is closed, child asleep, lunch options, where to park, which trail, walk-around loop, day route, a Google Maps or mapy.com link the user sends, výlet, kam na výlet, trasa, parkování, oběd'
user-invocable: true
---

# Itinerary

A finished day page starts with decision cards for the available routes while the route is undecided; once the user decides, it starts with the chosen route and its fallback. Each card gives distance, time and suitability and opens its route. Verify named stops and marked paths in a browser. Record photos, ratings, review counts and recent reviews separately for each source; mark missing evidence as unknown.

After a stop, date, route, venue or timing changes, recheck the affected sources, route and page. Before delivery, both commands must exit 0:

```sh
python3 ~/.claude/skills/itinerary/scripts/trip_lint.py ~/public_html/<trip> --root ~/public_html
python3 ~/.claude/skills/itinerary/scripts/link_check.py <the changed pages>
```

Complete the remaining checks in `guides/checks.md` before delivery.

One trip = `~/workspace/trips/<trip>/` (`db/` tables, `CLAUDE.md` with
decisions and vetoes) + `~/public_html/<trip>/` (pages). A place is on a
page only as a copy of a `db/` row that has a source. Page markup and
style are not here: `guides/web-deployment.md`.

## Process

| round | do | guide |
| --- | --- | --- |
| 0 Ask | before any browser or network work: what `<trip>/CLAUDE.md`, `~/facts/preferences.md` and `~/users/` do not answer: destination, dates, pace, interests, budget, food, child age and gear, car, start time — one chat message, 3–4 questions; none when the brief is complete | — |
| 1 Census | the user's links → OSM skeleton → mapy.com → Google Maps → TripAdvisor → official sites → tourist office → trail data → score → `places.html` | `guides/census.md` |
| 2 Enrich | for the rows a day uses: hours ON the date, prices, ratings per platform, phone, address, place links per kind, car-park fields, weather (`/weather`), story via `/find` | `guides/census.md` § Row, `guides/mapy-links.md`, `guides/pages.md` § Story |
| 3 Build | trip type per stop, timeline, arrival block, variants, loops, meal items, routes via `mapyrc.py` | `guides/pages.md`, `guides/mapy-links.md` |
| 4 Check | `trip_lint.py` exit 0, browser link and route checks, venue fields, trail facts, live 200 | `guides/checks.md` |

Round 1 runs once per new trip or base, before the first suggestion;
a small edit does not rerun it. Rounds 2–4 repeat on every rebuild. A change on the day (start time, closure, rain, a sleeping
child, a new base): `guides/pages.md` § On the day. Bookings (cars,
rooms, tours): `guides/bookings.md`.

## Trip types — pick one per stop; the page shows what the type needs

| type | what the page must contain |
| --- | --- |
| drive-up stop | arrival block (`guides/pages.md` § Arrival by car), minutes on site |
| stop + short walk | lower car park, marked path up, mapy route distance, climb, minutes |
| walk-around loop | circular foot route from the car park around the sight on mapy.com (`mapyrc.py`, mode hike): length, child-pace time, stroller yes/no |
| town stroll | arrival block for the start car park, the loop on foot with sights in order, Coffee item, toilets |
| day loop by car | roundtrip route (mapy + Google), stops in road order, fuel, toilets |
| indoor / rain day | hours on the date, tickets, booking, a second indoor option |
| on-the-way stop | minutes added to the drive, arrival block, exit back onto the route |

`kind` in `sights.md` picks the type: hiking → walk-around loop or stop +
short walk; town → town stroll; indoor → indoor / rain day; roadside →
drive-up or on-the-way. The type and the minutes open the stop's text
("Walk-around loop, 1 h 20.") so the group sees it.

## ALWAYS

- Run the census before the first suggestion; the radius covers every
  planned stop (`census.md` step 2). Cost: four sights within 8 km of
  the base missed until the user sent links; a town at 11.4 km (radius
  10) had no car-park row.
- Resolve every map link the user sends into a row before other work.
- Copy venue facts from the row; every venue carries address, phone,
  place links, website — or "not found" written out. Cost: cards with a
  name and a rating only.
- Take trail colour, length, surface and car-park fee from map data
  (`osm_trails.py`, mapy.com). Cost: trail colour and car-park fee
  published from a blog, wrong.
- Open Google Maps in the browser for every venue; drop "permanently /
  temporarily closed"; match the entry to the row by phone or address
  (matching and consent cookies: `mapy-links.md` § Disambiguation). Cost: two venues
  recommended were closed; a restaurant linked to its guesthouse.
- Name the platform a number came from. Cost: TripAdvisor
  snippets labelled Google — "how are you checking Google Maps if you
  can't reach it?".
- Precheck egress before research; on a refusal send the exact
  `network_allow` ask at once (`census.md` § Egress). Cost: mapy.com
  found outside egress after the page was live.
- Where the car stops: write the arrival block from the parking row —
  fee, capacity, walk time, fallback and the research's doubt — then two
  timed variants, drive-up and with a walk (`pages.md` § Arrival by
  car); NEVER a bare "park at X". Cost: a castle with one 3-car spot at
  the gate, no plan B.
- A hiking POI = short loop + long loop as mapy.com hike routes; a sight
  with trails around it gets a walk-around loop (`pages.md` § Loops).
  Cost: the castle page had a point, not a loop.
- At least five census POIs per full day page as candidates, each with
  its kind as the first words of its text; the timeline schedules only
  the stops that fit the day's hours. Cost: "three POIs is not a day".
- Every POI on a page gets a story block with reference links from
  `/find` — verified facts only (`pages.md` § Story).
- A place link opens the place's own entry, by kind (`mapy-links.md`):
  food, cafés, museums, shops → Google place first, then mapy entity;
  trails, rocks, viewpoints, car parks → mapy entity first, then OSM
  object; a GPS pin (`source=coor`) only when no entry exists. Links
  under every timeline item; roundtrip and walk routes from `mapyrc.py`
  in the top box. Cost: `?q=` pins opened on the wrong place;
  GPS pins showed "Position on the map" instead of the venue.
- Meals are timeline items at their time: the best-rated venues AT that
  spot, open at that hour, with open-today proof and the rating
  arithmetic in every line (`pages.md` § Meals). Cost: pizza next to a
  spring walk; a café with Google 4.8 and no hours, listed first —
  closed for the season on arrival; a café "unrated", Google had 4.5.
- State hours ON the trip date; prices exact; count, date and source
  stay in the row.
- Keep `db/` tables, personal contacts, and decisions and vetoes (in
  `<trip>/CLAUDE.md`) under `~/workspace/trips/<trip>/`, never under
  `~/public_html/` (that tree is public). Once the user decides, the
  day page is one plan. Cost: an options page for a day the user had
  already decided.
- Send the result in the chat; the page URL alone is not a delivery.

## NEVER

- Propose a place that is not a `db/` row with a source — ALWAYS add
  the sourced `db/` row first, then propose the place.
- Guess coordinates, hours, phones or prices — ALWAYS copy them from a
  source, or write "not found".
- Hand-write an `rc=` route string, a short link, or a `?q=` pin, or
  build a mapy `source=osm&id=` from an OSM id — ALWAYS build routes
  with `mapyrc.py` and link a place by mapy's own entity
  (`mapy-links.md`).
- Use mapy.com for car routing alone — ALWAYS put the Google route
  beside every mapy car route.
- Recommend a restaurant under 4.0 (café 4.5) — ALWAYS pick at or above
  the floor; label anything lower `fallback`.
- Put a café or restaurant inside a sight's item or `.map-links` row —
  ALWAYS give every meal its own timeline item (`pages.md` § Timeline).
  Cost: "why is pizza next to the spring walk".
- Narrate the road when a route link exists — ALWAYS give the route
  link, the distance and the drive time. Cost: three pages of "east on
  road 8 through X" nobody read.
- Cite sources inline in plan text or add "(estimate)" tails — ALWAYS
  carry them in the one "Facts checked <date>" line and the
  `.source-note` (`pages.md` § Exposition). Cost: 4,400 chars stripped.
- Write a story from memory or copy a blog paragraph — ALWAYS write it
  from `/find` results, with reference links.
- Put planning notes ("why this route works"), host or tool names, or an
  agent's name on a user-facing page — ALWAYS keep planning notes in
  `<trip>/CLAUDE.md`. Cost: a weather box listed five refused APIs; a
  source note named the agent's model.
- Deliver while `trip_lint.py` fails or a required browser check is
  unresolved — ALWAYS run both commands to exit 0 and resolve every
  browser check first.
- Treat "book in advance" as a reason to drop a place — ALWAYS keep the
  place and state the booking step.
- Re-propose a vetoed or already visited place — ALWAYS read the
  decisions and vetoes in `<trip>/CLAUDE.md` before proposing.

## Files

- `guides/census.md` — workflow, five tables, row fields, rating systems, egress
- `guides/pages.md` — what each timeline item carries: arrival, variants, loops, story, meals, exposition
- `guides/mapy-links.md` — place-link order per kind, disambiguation, pin and route URLs
- `guides/checks.md` — pre-delivery checklist: `trip_lint.py`, `link_check.py`, venues, trails, page
- `guides/bookings.md` — car rental, accommodation, tours
- `guides/web-deployment.md` — page skeleton, links, the classes `trip_lint.py` reads
- `scripts/` (`~/.claude/skills/itinerary/scripts/`) — `osm_census.py`, `osm_trails.py`, `mapyrc.py`, `trip_lint.py`, `link_check.py`; each prints its usage with `--help`
