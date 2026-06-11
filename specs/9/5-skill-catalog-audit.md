---
status: draft
source: aeon catalog inspection (2026-05-22); reference_aeon.md
---

# 9/5 — Skill catalog audit (Aeon 121 vs arizuko 56)

Aeon ships 121 pre-built skills. arizuko ships 56 stock + per-folder
custom. Many overlap, many don't. This spec is the audit methodology —
how we decide which Aeon skills are worth importing, which we already
cover better, and which we reject as bad fits.

No new code lands from this spec. The output is a worklist of
"import these N skills" entries, each of which becomes its own follow-up
implementation task.

## Why an audit, not a blind clone

Three reasons not to bulk-import:

1. **Arizuko skills are conversational and memory-aware.** Aeon's are
   single-shot batch. A skill written for Aeon often assumes "fetch
   fresh data, do thing, commit, exit" — arizuko skills should reach
   into per-folder facts, react to channel signals, and accumulate
   diary entries.
2. **Aeon's 121 includes many narrow integrations** (specific crypto
   tokens, specific feeds) that don't fit a multi-tenant
   conversational platform.
3. **Quality varies.** Open-source skill catalogs accumulate skills
   that were once useful for one user. We import the patterns, not
   the inventory.

## Audit methodology

For each Aeon skill `<name>`:

1. **Categorise**:
   - `infrastructure` — wraps a tool/API (e.g. `/github-pr`)
   - `analytical` — reads sources + summarises (e.g. `/crypto-trends`)
   - `monitoring` — periodic check + alert (e.g. `/twitter-watch`)
   - `creative` — generates content (e.g. `/tweet-thread`)
   - `meta` — operates on the agent itself (e.g. `/self-check`)

2. **Check overlap** against arizuko's existing 56:
   - **Direct overlap**: arizuko already has this. Skip; consider
     improvements to ours from Aeon's prompt.
   - **Partial overlap**: arizuko has the primitive (e.g. webhook
     posting) but no opinionated skill. Candidate for import as an
     opinionated wrapper.
   - **No overlap**: net-new capability. Highest import priority IF
     it fits arizuko's model.

3. **Check fit**:
   - Does it want **multi-turn memory**? Good fit; arizuko strength.
   - Does it want **channel realtime**? Good fit; we have adapters.
   - Does it require **GHA-style cron-only**? Bad fit unless trivial.
   - Does it require **a specific API key** the operator may not
     have? OK if the key is conditional (skip with helpful message)
     vs hard-required (skip the skill).

4. **Estimate effort**: lines of SKILL.md + new MCP tool? Skills
   that need only existing tools land trivially. Skills that need
   new tools cost a daemon + handler too.

5. **Decision**: import / improve-ours / reject / defer.

## Worked examples (5-10 to motivate the survey)

### `/crypto-price-watch` — IMPORT

**Aeon**: monitors token prices, fires on threshold cross.
**Fit**: arizuko has channel realtime + memory; perfect for an
"alert me when ETH crosses 5k" personal-assistant flow.
**Effort**: medium. Needs a price-fetch tool (exists via davd + web
fetch); needs the threshold-and-debounce logic which is novel.
**Decision**: import as `/watch-price` with cooldown via `6/2`'s
table.

### `/github-pr-summary` — IMPORT (HIGH PRIORITY)

**Aeon**: reads a PR diff + comments, writes a summary.
**Fit**: synergy with `specs/16/1-git-channel.md` (git-as-channel).
The PR thread is a folder; the summary skill is a folder operation.
**Effort**: small once `7/1` lands; medium otherwise.
**Decision**: import + cross-ref into 7/1. Maps onto `git:` channel
verbs cleanly.

### `/twitter-watch-handle` — IMPORT (CONDITIONAL)

**Aeon**: monitors a specific handle's tweets, alerts on keyword.
**Fit**: arizuko has `twitd`. Skill becomes a per-folder
configurable watcher.
**Effort**: small (logic in SKILL.md, calls existing twitd verbs).
**Decision**: import once `twitd` rate-limit story is solid (Aeon
runs into Twitter limits constantly).

### `/regression-blame` — REJECT (for agents)

**Aeon**: git-blame since last green CI to surface likely culprit
for a regression.
**Fit**: developer tool, not an agent skill. Useful for the arizuko
_codebase_ (operator-side debugging) but doesn't belong in
`ant/skills/`.
**Decision**: skip in `ant/skills/`; consider as a `tools/` script.

### `/cron-self-check` — SUPERSEDED

**Aeon**: agent runs a periodic "am I working?" check, commits a
heartbeat.
**Fit**: arizuko has `skill_health` via `6/1`. The Aeon pattern is a
weaker version of what `6/1` ships.
**Decision**: skip; `6/1` is the better answer.

### `/news-digest` — IMPORT (LOW PRIORITY)

**Aeon**: fetches N news sources, summarises, posts.
**Fit**: arizuko's per-folder personal-assistant story (spec
`7/product-personal-assistant.md`) wants this.
**Decision**: import as `/digest-news` for the personal-assistant
template. Low priority because personal-assistant is post-MVP.

### `/discord-mod-reply` — DEFER

**Aeon**: opinionated moderation reply template.
**Fit**: arizuko has `discd`; persona-driven moderation is already
better-served by per-folder PERSONA + grants.
**Decision**: defer — a moderation skill belongs in a "moderation
product template" (future spec slot), not as a generic stock skill.

### `/secret-prefetch` — REJECT

**Aeon**: prefetches API tokens at workflow start, exposes via env.
**Fit**: arizuko has `specs/7/Y-secret-broker.md`. Aeon's pattern is
a workaround for GHA's secret-injection model that we don't share.
**Decision**: skip; broker is the better answer.

## Output of the audit

A worklist file at `tmp/aeon-skill-audit.md` (not in specs/ — too
mechanical; tracks per-skill decisions). Format:

```
| Aeon skill          | Category       | Decision   | Notes                                  |
| ------------------- | -------------- | ---------- | -------------------------------------- |
| /crypto-price-watch | monitoring     | import     | high-pri; needs 6/2 cooldown           |
| /github-pr-summary  | infrastructure | import     | high-pri; cross-ref 7/1                |
| /cron-self-check    | meta           | superseded | 6/1 ships the better version           |
| /regression-blame   | meta           | reject     | dev tool not agent skill               |
| ... (121 total)     |                |            |                                        |
```

Each `import` row becomes a follow-up task: write the SKILL.md, add
to `ant/skills/`, ship under the standard skill migration lifecycle
(`specs/4/P-personas.md ## Versioning`).

## Honest gaps

- **121 is a lot.** Triaging all of them is itself ~2 days of work.
  Realistic v1 of this audit: triage the top ~40 by Aeon's own
  documentation frequency (the skills Aeon highlights in its README
  and examples).
- **Aeon's skills may rot.** Importing copies the prompt today; if
  Aeon improves a skill, we don't auto-pull. Treat imports as
  inspirations, not living dependencies.
- **License check**: confirm Aeon's license permits the prompt
  re-use we plan. Likely permissive (MIT/Apache) but verify before
  importing prose verbatim.
- **Skill bloat risk**: 56 → 100+ stock skills makes `/help` harder
  to scan. Group imports under a `aeon-derived/` subdirectory to
  preserve attribution + allow operator opt-out.

## Out of scope

- Actually authoring the imported skills. Each import is a follow-up
  task.
- A reverse audit (arizuko skills Aeon should adopt). Their problem,
  not ours.
- Wholesale repackaging of Aeon's distribution. We import patterns.

## Decisions

- The audit is selective, not exhaustive.
- Output is a worklist at `tmp/aeon-skill-audit.md`, not a spec
  per skill.
- Each `import` row becomes a follow-up task under the standard
  skill migration lifecycle.
- Imported skills group under `ant/skills/aeon-derived/` for
  attribution + opt-out.
- License check is a hard gate before any prompt copy-paste.
- Triage the top ~40 first; the long tail waits for demonstrated
  demand.
