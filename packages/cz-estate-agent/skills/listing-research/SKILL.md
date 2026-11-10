---
name: listing-research
description: Research and track property listings against the group's criteria. NOT for valuations (use property-valuation) or page rendering (use estate-site).
---

# Listing research

Read `~/estate/site.json` and use its `criteria` as the group's search brief. Ask for missing criteria before applying a filter. Criteria may describe location, budget, buildings, pasture, access or other needs; do not assume a particular buyer or country.

Set `criteria` to a JSON object, for example `{"use":"stables","pasture":"adjacent","minimum_area_m2":20000}`. Treat its fields as the group's requirements, not as colour thresholds.

Search and verify sources using [sources.md](guides/sources.md). Record source links, dates, unknowns and the reason for each selection in the group's research. Deduplicate listings by property and retain the sources. Update `~/estate/db.md` with verified status and use estate-site to build the index. Do not create valuations unless requested.

For terrain evidence run `python3 ~/.claude/skills/listing-research/scripts/terrain_view.py LAT,LON`. Missing elevation or map results mean unknown; confirm terrain and surroundings independently.
