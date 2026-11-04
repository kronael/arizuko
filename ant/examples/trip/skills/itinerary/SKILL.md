---
name: itinerary
description: Travel planning and destination research for one region at a time. Use for trip questions ("what to see in X", "plan trip to Y"), destination highlights, itineraries, routes, maps, restaurants, cafes, attractions, day schedules, sightseeing planning.
---

# Itinerary Planning

A new region needs fresh research: sources, schedules, prices and coordinates from
another trip do not carry over. Save research in `~/tmp/`, never reuse it blind.

## 3-Round Process

**Round 1: User Questioning (ALWAYS first)**
- Ask preferences: pace, interests, budget (<€15 default), dietary needs
- Gather themes: areas heard about, must-visit spots
- Confirm constraints: mobility, time, accommodation location
- Send the questions as a normal chat message (never AskUserQuestion)

**Round 2: Deep Research (MANDATORY before planning)**

**Weather:** Use `/weather` skill — NEVER skip
- Cross-check 2 official sources (the national met institute vs yr.no)
- Report: temps, rain timing, wind, sea conditions

**Restaurants (3-5 per meal):**
- ALWAYS aggregate reviews: Google Maps, TripAdvisor, Restaurant Guru, Yelp
- Report ALL ratings: "Google 4.3/5 (500) | TripAdvisor 4.5/5 (#12 of 150)"
- ONLY recommend 4.0+ averaged (cafes: 4.5+)
- Flag <100 reviews or conflicting ratings (>0.5 difference)
- Research via agent-browser, save to `~/tmp/<dest>-restaurants-database.md`

**Cafes:**
- Build 50-100+ database with multi-platform ratings
- Pick 3 best ON route (NEVER detours)
- Save to `~/tmp/<dest>-cafes-<area>.md`

**Attractions:**
- Hours (check seasonal variations: summer vs winter)
- **ALWAYS state which schedule applies TODAY** (check current date vs seasons)
- Closure days, renovations (verify current year)
- Entry prices (exact, NOT estimates)
- Report: "Open today: HH:00-HH:00 (summer schedule)" NOT just "9-18 winter, 9-20 summer"

**Transport:**
- Google Maps for time estimates
- Exact pricing via websites: "€1.55" NOT "~€2"
- Tolls status: "None (free roads)" OR "€4.20 total (A8)"

**Map Links:**
- See `guides/mapy-links.md` for mapy.com format
- MANDATORY: `&x=&y=&z=` coordinates for mobile app
- Use agent-browser to extract coords, NEVER guess

**Round 3: Build & Deploy**
- Create day pages with timeline, maps, food, transport
- Deploy to `~/public_html/<destination>/`
- Run consistency checks (see `guides/web-deployment.md`)

## Timeline Structure

**REQUIREMENTS:**
- ALL content flows chronologically (top-to-bottom)
- Restaurants/cafes/attractions inline or `<details>` in timeline
- Accordion: expanding one `<details>` collapses others in same timeline-item
- NEVER "see options below" or separate sections

**Drive/Transfer:**
- Distance, time, route link (Google Maps)
- Tolls status (exact amount or "None")

**Attractions:**
- Name + map link, entry price, time needed
- Collapsible details: what to see, hours, tips

**Meals:**
- Walking directions to area
- 3-6 restaurant options as `<details>`
- Light outline style: `border: 1px solid var(--border)`
- Include: price range, dishes, multi-platform ratings, map link

**Timing:**
- Photo stop: 5-10 min
- Viewpoint + coffee: 20-40 min
- Church: 15-45 min
- Lunch/Dinner: 1-1.5h
- Museum: 1-2h

**Communication:**
- NEVER "late/early" — state facts: "Crowds arrive 11am", "Best light morning"

## Route Optimization

ALWAYS:
- Group by area (minimize backtracking)
- Uphill morning, downhill after lunch
- Lunch at scenic viewpoint
- Dinner near accommodation/transit
- Note closure days (Mondays common)
- Choose scenic over shortest

## Route Overview Box

ALWAYS at top of day pages:
```html
<div class="route-overview">
  <strong>📍 Route:</strong> Start → Stop1 → Stop2 → End<br>
  <strong>⏱️ Total time:</strong> Full day (9:00-21:00) or Half day (14:00-19:00)<br>
  <strong>🚗 Distance:</strong> ~XX km round trip<br>
  <strong>🛣️ Tolls:</strong> None (free roads) OR €X.XX total (A8 motorway)<br>
  <strong>💰 Total cost per person:</strong> €XX-XX (parking + entries + meals + transport)
</div>
```

**Cost breakdown:**
- Per person (not per group)
- Itemize: parking + entries + meals + transport
- Ranges for meals (€35-55), exact for fixed costs (€1.50)

## Car Rentals

ALWAYS search: rentalcars.com, kayak.com, momondo.com via agent-browser
- Compare local vs international
- Extract: price/day, total, excess, insurance, mileage, fuel policy
- Check Google Maps ratings + Trustpilot
- NEVER recommend <3.5/5 without warning
- Present 3-5 options: budget, mid-range, best value (4.0+ rating)

Format: `Company | Model | Price/day | Excess | Rating | Notes`

## Accommodation

Search: Booking.com, Airbnb, Hostelworld
- Filter: walkable (max 20min), 8.0+/4.5+, per budget
- Present 3-5 options with location advantage

Format: `Name | Type | Price/night | Rating | Location | Why`

## Activities/Tours

Search: GetYourGuide, Viator, provider websites
- Compare: group vs private, skip-line vs regular
- Extract: price, duration, included, cancellation, rating

Format: `Activity | Provider | Price | Duration | Included | Rating`

## Web Deployment

Deploy to `~/public_html/<destination>/`

**Pre-delivery Checklist (MANDATORY):**
```bash
# 1. Every restaurant/cafe/attraction has map link
grep -L 'mapy.com\|google.com/maps' *.html | grep -E 'day|plan'

# 2. Every venue has price info
grep -L 'Price:\|€' *.html | grep -E 'day|plan'

# 3. Title = H1 consistency
for f in *.html; do
  title=$(grep -m1 '<title>' "$f" | sed 's/.*<title>\(.*\)<\/title>.*/\1/')
  h1=$(grep -m1 '<h1>' "$f" | sed 's/.*<h1>\(.*\)<\/h1>.*/\1/')
  [ "$title" != "$h1" ] && echo "❌ $f"
done
```

**NEVER deliver without:**
- ✅ Map link on EVERY restaurant, cafe, brewery, attraction
- ✅ Price info on EVERY venue (exact or range)
- ✅ Address on venues where helpful
- ✅ Hours if relevant (cafes, museums)
- ✅ Title = H1 on every page

See `guides/web-deployment.md` for:
- HTML template
- Design rules
- Cross-reference validation

See `guides/mapy-links.md` for:
- Map link format
- Coordinate extraction

## NEVER

- Skip user questioning
- Estimate pricing (ALWAYS exact)
- Use WebSearch for weather
- Choose shortest over scenic
- Skip 3+ restaurant research per meal
- Guess coordinates/hours
- Use mapy.cz short links or search-only URLs
- Use mapy.cz for car/transit routing (use Google Maps)
- Manually construct map URLs
- Put "Why This Route Works" on user-facing pages (planning artifact only)
- Treat "book in advance" as blocker
- Recommend <4.0 avg restaurant rating

## Files

Save research to `~/tmp/`:
- `<dest>-transport-research.md`
- `<dest>-cafes-<area>.md`
- `<dest>-restaurants-database.md`

Supporting guides:
- `guides/mapy-links.md` — Map link construction
- `guides/web-deployment.md` — HTML templates, consistency checks
