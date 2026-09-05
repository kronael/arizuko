---
status: draft
depends: [23-agent-habitat]
---

# specs/7/24 — skill search

Discovery is the unpriced half of the skill mechanism. A skill that is
never found does nothing, and nothing reports that it was never found.

## Measured, 2026-09-05

arizuko ships **95 skills** under `ant/skills/`; their descriptions alone
are **6,491 bytes**. The engineer's own set in `kronael/tools` is 84.
Both grow monotonically — a skill is added far more often than removed.

Three backends discover them three different ways:

| Backend | Mechanism                                                                                                  |
| ------- | ---------------------------------------------------------------------------------------------------------- |
| claude  | the harness's own skill surface                                                                            |
| codex   | `renderSkillsIndex` (`ant/src/skills-scan.ts:68`) renders every name, description and path into the prompt |
| pi      | explicit `--skill <dir>` arguments, discovery off                                                          |

For codex the entire index is resident on every turn, whether one skill
is relevant or none.

## The failure that has no signal

`solve` routes on frontmatter keywords. A skill whose description omits
the words a request uses is unreachable, and the agent's transcript
records a task done the long way rather than a lookup that missed.

That shape is already the platform's most expensive class: a consumer
that quietly finds nothing ends the error and starts the silence. Search
without a miss signal reproduces it at a new layer.

## What a search facility must answer

1. **What is indexed** — a description is written for a human skimming a
   list. A retrieval index needs the phrases a requester actually types,
   which are not the same words.
2. **What a query is** — the request text, a classification of it, or a
   term the model chooses. Each has a different failure mode, and the
   third puts the model in the retrieval loop it is trying to escape.
3. **What happens on a miss** — the load-bearing question. A miss must be
   observable, or the mechanism is unfalsifiable: no one can tell a task
   with no matching skill from a skill the search failed to surface.
4. **What stays resident** — search only pays if the full index stops
   being resident. If both remain, cost rises.

## Bounds

- Adding search while the index stays in the prompt is a second reader of
  one declaration and buys nothing.
- Ranking that the model performs is not search; it is the current
  mechanism with extra steps.
- A miss counter is the minimum viable version and may be the whole
  answer: measure how often a request matches nothing before building
  retrieval for a problem that may not exist at this size.

## Open questions

- Does the harness offer a native skill-search or lazy-listing facility?
  If it does, adopt it rather than building one.
- Is 95 skills past the point where a flat index stops working, or is
  that assumed? No measurement here shows a discovery failure; the case
  rests on the cost of the resident index and the absence of a miss
  signal, not on an observed miss.

## Ties

`23-agent-habitat` (the resident budget this competes for) · `6/34` (what
an always-loaded file may carry).
