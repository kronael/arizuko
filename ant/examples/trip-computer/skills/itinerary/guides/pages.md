# Pages — what each timeline item carries (Round 3)

Markup, class names and block order: `web-deployment.md` (and the
site's own `design.md` when it has one); copy an existing day page. This guide says what goes INTO
the blocks. Every fact is a copy of a `db/` row (`census.md`).

## Timeline

- Chronological, top to bottom; the timeline is the master index.
  Never "see options below" or separate sections.
- Drive item: distance, time, then the arrival block (below) when the
  car stops. Tolls exact or "None".
- Stop item: name, map link; the text opens with the trip type and the
  minutes ("Drive-up stop, 20 min." / "Walk-around loop, 1 h 20."), then
  entry price, the two variants (below), the story box (below), per-item
  `.map-links` last.
- Meal item: § Meals below. A sight's `.map-links` row holds that stop's
  pins and route only; food links live only in meal items.
- Durations: photo stop 5–10 min · viewpoint + coffee 20–40 · church
  15–45 · lunch 1–1.5 h · museum 1–2 h.
- Times add up: each item starts no earlier than the previous item's
  start + its duration + the transfer between them, and inside the
  place's opening hours on the date. A 45-min stop at 13:45 is not
  followed by lunch at 13:50. Two items that exclude each other are one
  choice item, never two entries of the chronology.
- The `.route-overview`, the decision cards and the items read the same
  selected schedule: one distance and one time per leg everywhere.
- Order: group by area, uphill in the morning, lunch at the view,
  closure days noted (Mondays), scenic over shortest when time allows.
- Optional blocks after the last fixed stop and before the drive home,
  marked "Optional", with the cut-off time in the `.highlight-box`
  (daylight, closing); Dinner closes the timeline.
- Facts, not judgements: "crowds arrive 11:00", not "go early".
- `.route-overview` cost line: per person; parking per car with the fee
  from the arrival block; entries exact; meals as a range.

## Arrival by car

Every item where the car stops ends with the arrival block. No field
may be missing; no `db/parking.md` row → no stop (census first).
The block tells the car where to stop only when the row verifies legal
parking at a named place and a fallback. When the research left it
open (no car park found, a roadside pull-off not seen at street level),
the block says `parking unconfirmed` and what is unknown, and the plan
never sends the car to that shoulder, bus stop or verge as if verified.

```
12:10 — Drive to <Town>, park at the town hall
  9 km, 13 min
  park: <Car park name>, <Street> 3 — 3.00/h, first 20 min free,
  >90 places, asphalt, 210 m / 3 min on foot to the spa house —
  if full: <Street> 2, 350 m / 5 min
```

- Car park: the row's name and address; mapy entity link and the Google drive
  link to the car park, not to the sight.
- Fee: exact, or `free`, with the trip day and hour covered; "~" for an
  uncertain figure. The source stays in the row.
- Capacity: a count, or `≈n` from the OSM outline.
- Surface and stroller: asphalt / gravel / mud; steps or kerbs.
- Walk: metres and minutes from walking directions, never straight-line.
- Fallback: the next row when full; a free row if one exists.

## Stop variants

Every stop that involves parking gets both variants, each timed
end-to-end so the day can be re-planned on the spot:

```
13:30 — <Castle>, drive-up (20 min)
  park: 3-car spot at the gate (free) — if full, variant B
13:30 — <Castle>, with the walk (55 min)
  park: <Street>, town (free); green trail 0.8 km, +100 m, 25 min up,
  15 min down at the group's pace; stroller no, carrier yes
```

- The `park:` line of each variant is the arrival block above.
- Walk variant: marked path, mapy route distance, climb, minutes at 3 km/h
  + 15 min per 100 m, stroller yes/no, turnaround point.

## Loops and hiking POIs

- A sight with trails around it is published with a walk-around loop:
  1. start and end = the parking row;
  2. waypoints on the marked trails that circle the sight (relation ids
     from `osm_trails.py`, viewpoints from `db/sights.md`);
  3. `python3 ~/.claude/skills/itinerary/scripts/mapyrc.py routes.json` (mode `hike` =
     mapy "Outdoor", follows marked trails; `foot` is the road-first
     "Fast" profile) → URL; open it and read mapy's length and time;
  4. `🥾 Loop:` line in the top `.map-links` with length, mapy time,
     group-pace time, stroller yes/no, turnaround; the same link under
     the stop's item.
- A `hiking` row is never a bare stop: "<name> — short loop x km /
  y min" and "long loop x km / y h", both route links, and the car park
  row that starts them (`census.md` § Row).
- The day's census POIs (count: `SKILL.md`) are listed with their kind
  on or near the route; the group picks on the day.

## Story

Right under the stop's `.timeline-text`, an `.info-box`:
```html
<div class="info-box"><strong>Story.</strong> Built 1853–1880 for
the founding family … <a href="https://en.wikipedia.org/wiki/…">Wikipedia</a> ·
<a href="https://…">official site</a></div>
```
3–6 sentences: origin, history, why it matters. Verified facts only; no
curiosities, anecdotes or colour. Text and links are the `story` and
`refs` fields of the row, produced by `/find <poi>`. A story without
links is removed before delivery.

## No road prose

When the item has a route link, the text is:
`<distance>, <time>. <arrival facts: car park, fee, toilets, if full>.`
Never narrate roads, turns or villages passed — the map carries the
path. The same for walks: colour, length, surface, turnaround point,
not "go left after the bridge".

## Exposition

- The plan says what to do, where, when, how much. No inline citations,
  no "(estimate)" tails, no "one listing says", no "by the OSM outline",
  no host, tool or agent names; an uncertain number is written with "~"
  or "about".
- Citations leave the plan text; uncertainty that changes what the
  traveller does stays beside its stop: `parking unconfirmed`, `hours
  unknown`, `fee unknown`.
- Under the subtitle, one line:
  `<p class="checked-line">Facts checked <date>; sources at the end of the page.</p>`
  When a fact the plan depends on is still unverified, the line says
  so: `Facts checked <date>, except parking at <stop>; sources at the end of the page.`
- All sources once, in the `.source-note` at the end; the `db/` row
  keeps the per-fact source for the next edit.
- Two readers: the travellers on a phone in the car (plan text) and the next
  agent (`db/` rows). Never mix the two on the page.

## Meals — timeline items at their time

- The meals the day's timeline reaches, where they happen: `Breakfast`
  before leaving when the day starts before 10:00, `Lunch` at the lunch
  stop when it spans 11:30–14:00, `Coffee` right after lunch or at the
  café stop when it spans 14:00–17:00, `Dinner` after the return when it
  runs to 18:00. A part-day page (a return drive 11:30–17:30) carries
  only the meals inside its hours. The heading names the meal in the
  page's language ("Late lunch", "Oběd"). Never a separate Food
  section, never a café inside a sight's item (a café inside a walk
  item, pizza next to a spring walk).
- A meal with no venue to pick says `self-catered` in its text; never an
  invented venue to fill the slot.
- Each meal item = `timeline-date`, optional one-line text (walk from
  the car park, cheap fallback), then `<ul class="food-list">` from
  `db/food.md` (bakeries → Breakfast, cafés → Coffee, restaurants on the
  route → Lunch, near home → Dinner). Never empty when the census has rows.
- One `<li>` per place, at most 10 words before the first `·`:
  `<a href="…/maps/place/…">Name</a> — what it is, open to 18:00 · mapy 4.8 · 123 reviews · 6 recent · <a href="tel:+00…">+00 …</a> · <a class="alt" href="…source=osm&id=…">mapy</a>`
  The venue name links Google; the `class="alt"` chip links mapy
  (`mapy-links.md` § By kind); phone missing after
  the Google check → `phone: not found`. Address, hours and price stay
  in the row.
- Rating, reviews and recent are the row's derived fields
  (`census.md` § Rating arithmetic): the higher score with its platform,
  the summed count, the 2-month count. A missing count is named:
  `mapy 4.8 · 77 mapy reviews, Google count unknown`. `unrated` only
  after Google Maps was opened and showed no rating (a café "unrated";
  Google had 4.5).
- Select, do not list: the best-rated venues AT the spot where the meal
  happens (the park café for a park walk, the square for a town lunch),
  sorted by rating; caps Lunch 3, Breakfast 2, Coffee 2, Dinner 2. A
  venue under 4.0 (Coffee under 4.5) carries `fallback` in its line.
  Breakfast and Dinner follow the day's route, not the previous page
  (three pages carried the same four lines). Cost: three
  cafés, none in the park walked.
- Open-today proof in EVERY line ("open to 18:00", "from 7:00"):
  Google's hours for the trip date, the venue website / Facebook, or a
  call. A card with a rating but no hours is unconfirmed: never first,
  and in a spa or mountain town assume seasonal closure Nov–Mar until
  confirmed. Open at the meal time: a café from 10:30 is not Breakfast
  at 8:45. Cost: a café with Google 4.8, no hours shown,
  listed first — closed for the season on arrival.
- The Google entry matches the row's phone or address, not the name
  alone: a guesthouse and its restaurant are two entries (a restaurant
  and its guesthouse). Details: `mapy-links.md` § Disambiguation.
- A spa town, promenade or town stroll ALWAYS has a Coffee item.

## On the day — the group changes the plan

Edit the page in place; Rounds 2–4 run on the changed items only.

- Start time moves ("rebuild Fri start at 11:30"): keep every duration,
  shift every time, recompute the cut-off line, drop the optional blocks
  that now end after sunset. A shifted item that now falls outside its
  place's hours takes the next feasible row, or the reply names the
  conflict; the same holds when a variant or duration changes.
- A place is closed on the date (call, Google, official site): the next
  row of the same table by rating, with its car park; the swap goes in
  `<trip>/CLAUDE.md`; the closed row keeps `closed <date>`.
- Rain: one "Rain plan" timeline item near the end from the day's
  `indoor` sights or `kids.md` fallbacks — hours on the date, tickets.
- Child asleep in the car: the drive-up variant of the next stop; the
  walk variant waits. Both times are already on the item.
- The base moves: Round 1 around the new base (`--also` for the old
  stops), `km` recomputed; the pages keep their stops.
- A forecast host is down: `/weather` § Freshness decides what the
  weather box shows; never the hosts tried.

## First screen

Place the day decision first. Show route choices as cards with distance, time and suitability, and link each card to its verified route. Keep rating and review counts tied to their named source. On a 390×844 phone screen the first choice card is fully visible: above it stand only the back link, the one-line day nav, the title, one subtitle line and the checked line; introductory prose follows the cards.
