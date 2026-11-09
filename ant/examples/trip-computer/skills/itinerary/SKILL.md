---
name: itinerary
description: >
  Plan trips and day itineraries from a verified place database: census
  of sights, restaurants, car parks and trails around the base, then day
  pages with timelines, map links and routes. USE for "plan a trip to X",
  "what to see near Y", "suggest trips for Friday", "build the day page",
  "lunch options", "which trail", "walk-around loop", Google Maps or
  mapy.com links the user shares. NOT for weather forecasts alone (use
  weather) or car buying (use find-car).
user-invocable: true
---

# Itinerary

A finished day page starts with decision cards for the available routes. Each card gives distance, time and suitability and opens its route. Verify named stops and marked paths in a browser. Record photos, ratings, review counts and recent reviews separately for each source; mark missing evidence as unknown.

After a stop, date, route, venue or timing changes, recheck the affected sources, route and page. Before delivery, both commands must exit 0:

```sh
python3 ~/.claude/skills/itinerary/scripts/trip_lint.py ~/public_html/<trip> --root ~/public_html
python3 ~/.claude/skills/itinerary/scripts/link_check.py ~/public_html/<trip>/day*.html
```

Complete the remaining checks in `guides/checks.md` before delivery.

One trip = `~/workspace/trips/<trip>/` (`db/` tables, `CLAUDE.md` with
decisions and vetoes) + `~/public_html/<trip>/` (pages). A place is on a
page only as a copy of a `db/` row that has a source. Page markup and
style are not here: `guides/web-deployment.md`.

## Process

| round | do | guide |
| --- | --- | --- |
| 0 Census | OSM skeleton → the user's links → mapy.com → Google Maps → TripAdvisor → official sites → tourist office → trail data → score → `places.html` | `guides/census.md` |
| 1 Ask | what `<trip>/CLAUDE.md` and `~/users/` do not answer: pace, interests, budget, food, child age and gear, car, dates, start time — one chat message, 3–4 questions | — |
| 2 Enrich | for the rows a day uses: hours ON the date, prices, ratings per platform, phone, address, place links per kind, car-park fields, weather (`/weather`), story via `/find` | `guides/census.md` § Row, `guides/mapy-links.md`, `guides/pages.md` § Story |
| 3 Build | trip type per stop, timeline, arrival block, variants, loops, meal items, routes via `mapyrc.py` | `guides/pages.md`, `guides/mapy-links.md` |
| 4 Check | `trip_lint.py` exit 0, browser link and route checks, venue fields, trail facts, live 200 | `guides/checks.md` |

Round 0 runs before the first suggestion. Rounds 2–4 repeat on every
rebuild. A change on the day (start time, closure, rain, a sleeping
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
  planned stop (`census.md` step 1). Cost: four sights within 8 km of
  the base missed until the user sent links; a town at 11.4 km (radius
  10) had no car-park row.
- Resolve every map link the user sends into a row before other work.
- Copy venue facts from the row; every venue carries address, phone,
  place links, website — or "not found" written out. Cost: cards with a
  name and a rating only.
- Take trail colour, length, surface and car-park fee from map data
  (`osm_trails.py`, mapy.com). Cost: trail colour and car-park fee
  published from a blog, wrong.
- Open Google Maps in the browser for every venue (consent cookies:
  `mapy-links.md` § Disambiguation); drop "permanently / temporarily
  closed"; match the entry to the row by phone or address — a
  guesthouse and its restaurant are two entries. Cost: two venues
  recommended were closed; a restaurant linked to its guesthouse.
- Name the platform a number came from. Cost: TripAdvisor
  snippets labelled Google — "how are you checking Google Maps if you
  can't reach it?".
- Precheck egress before research; on a refusal send the exact
  `network_allow` ask at once (`census.md` § Egress). Cost: mapy.com
  found outside egress after the page was live.
- Where the car stops: arrival block from the parking row, then two
  timed variants, drive-up and with a walk (`pages.md`). Cost:
  a castle with one 3-car spot at the gate, no plan B.
- A hiking POI = short loop + long loop as mapy.com hike routes; a sight
  with trails around it gets a walk-around loop (`pages.md` § Loops).
  Cost: the castle page had a point, not a loop.
- At least five census POIs per day page, each with its kind as the
  first words of its text. Cost: "three POIs is not a day".
- Every POI on a page gets a story block with reference links from
  `/find` — verified facts only (`pages.md` § Story).
- A place link opens the place's own entry, by kind (`mapy-links.md`):
  food, cafés, museums, shops → Google place first, then mapy entity;
  trails, rocks, viewpoints, car parks → mapy entity first, then OSM
  object; a GPS pin (`source=coor`) only when no entry exists. Links
  under every timeline item; roundtrip and walk routes from `mapyrc.py`
  in the top box. Cost: `?q=` pins opened on the wrong place;
  GPS pins showed "Position on the map" instead of the venue.
- Meals are timeline items at their time — Breakfast, Lunch, Coffee,
  Dinner — each a `<ul class="food-list">` of the best-rated venues AT
  that spot, open at that hour, caps Lunch 3 / Breakfast 2 / Coffee 2 /
  Dinner 2; one `<li>` per venue (`pages.md` § Meals). Cost: pizza next to a
  spring walk; three cafés, none in the park walked.
- Open-today proof in every meal line ("open to 18:00": Google hours ON
  the date, the website, or a call). No hours = unconfirmed, never first;
  a spa or mountain town Nov–Mar = seasonal closure until confirmed.
  Cost: a café with Google 4.8 and no hours, listed first —
  closed for the season on arrival.
- A rating = the platform that was opened + score; `unrated` only after
  Google Maps was opened and showed none. Cost: a café
  "unrated", Google had 4.5.
- State hours ON the trip date; prices exact; count, date and source
  stay in the row.
- Keep decisions and vetoes in `~/workspace/trips/<trip>/CLAUDE.md`, never
  under `~/public_html/` (that tree is public). Once the user decides, the
  day page is one plan. Cost: an options page for a day the user had
  already decided.
- Send the result in the chat; the page URL alone is not a delivery.

## NEVER

- Propose a place that is not a `db/` row with a source.
- Guess coordinates, hours, phones or prices.
- Write "park at X" without fee, capacity, walk time and fallback.
- Hand-write an `rc=` route string, a short link, or a `?q=` pin; build
  a mapy `source=osm&id=` from an OSM id (mapy's id is its own).
- Use mapy.com for car routing without the Google route beside it.
- Recommend a restaurant under 4.0 (café 4.5) except as a labelled
  fallback.
- Put a café or restaurant inside a sight's item or `.map-links` row;
  a link row holds one stop's pins and route only. Cost:
  "why is pizza next to the spring walk".
- Narrate the road when a route link exists. Cost: three
  pages of "east on road 8 through X" nobody read.
- Cite sources inline in plan text or add "(estimate)" tails; one
  "Facts checked <date>" line and the `.source-note` carry them
  (`pages.md` § Exposition). Cost: 4,400 chars stripped.
- Write a story from memory or copy a blog paragraph.
- Put planning notes ("why this route works"), host or tool names, or an
  agent's name on a user-facing page. Cost: a weather box
  listed five refused APIs; a source note named the agent's model.
- Deliver while `trip_lint.py` fails or a required browser check is unresolved.
- Treat "book in advance" as a reason to drop a place.
- Re-propose a vetoed or already visited place.

## Files

- `guides/census.md` — workflow, five tables, row fields, rating systems, egress
- `guides/pages.md` — what each timeline item carries: arrival, variants, loops, story, meals, exposition
- `guides/mapy-links.md` — place-link order per kind, disambiguation, pin and route URLs
- `guides/checks.md` — pre-delivery checklist: `page_check.py`, venues, trails, page
- `guides/bookings.md` — car rental, accommodation, tours
- `guides/web-deployment.md` — page skeleton, classes `page_check.py` reads
- `scripts/` (`~/.claude/skills/itinerary/scripts/`) — `osm_census.py`, `osm_trails.py`, `mapyrc.py`, `page_check.py` (`--help`)
