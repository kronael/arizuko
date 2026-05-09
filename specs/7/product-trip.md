---
status: planned
brand: may
---

# Product: trip planner (May)

_Named for Maia, who opened the warm months and the roads between them. Every journey begins with her._

Research, synthesize, and structure a trip into a usable plan.
Template at `ant/examples/trip/`.

## Value prop

The agent does the legible work of travel planning: researches
destinations, distills options, builds a day-by-day itinerary, and
delivers a clean document. The user states preferences once; the
agent recalls them on future trips.

## What it does

- Takes a brief ("10 days in Japan, mid-budget, food focus") and runs
  multi-step research via oracle: destination deep-dives, neighborhood
  comparison, logistics, seasonality, visa, budget breakdown
- Stores preferences and past trips in facts/ — subsequent trips are
  faster and better calibrated
- Produces a structured itinerary (markdown or PDF via send_file)
- Answers follow-up questions against the stored plan
- Optionally: monitors flight prices or entry requirements on a schedule

## Skills

| Skill           | Required | Notes                                |
| --------------- | -------- | ------------------------------------ |
| diary           | yes      |                                      |
| facts           | yes      | travel preferences, past trips       |
| recall-memories | yes      |                                      |
| web             | yes      | destination research, real-time info |
| oracle          | yes      | multi-step research, synthesis       |
| find            | yes      |                                      |

## Template folder

```
ant/examples/trip/
  SOUL.md           — methodical travel researcher; asks clarifying
                      questions before researching; cites sources
  CLAUDE.md         — produce itineraries in a fixed markdown schema;
                      store every trip in facts/trips/YYYY-<dest>.md;
                      always include visa, budget, packing section
  skills/           — diary, facts, recall-memories, web, oracle, find
  facts/
    preferences.md  — seed with placeholder user profile
```

## Channels

Telegram or WhatsApp (personal). slink for web access to the plan.
send_file for PDF delivery.

## Depends on

- oracle (CODEX_API_KEY or OPENAI_API_KEY) — critical; without it
  research is shallow
- davd (optional) — for shared itinerary editing

## Developer capabilities embedded

Generates booking scripts, scrapes price tables, produces HTML
itinerary pages if asked — standard bash + web skills, no separate
"developer product" needed.

## Web page pitch

An AI travel researcher that knows your preferences and builds
real itineraries — not lists of links, but a structured plan
grounded in actual research.
