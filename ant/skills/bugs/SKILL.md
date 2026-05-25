---
name: bugs
description: >
  Record open issues in `bugs.md` at project root. NOT for fixing — fixes happen on
  explicit request. After resolution, move a one-line reference to the diary.
when_to_use: "bug found", "log this bug", "open issues", debugging-but-not-fixing-now, audit-record-only, "what's broken", "what's the queue"
---

# Bugs

`bugs.md` at the project root is the **open-issues queue**. Complementary to
`.diary/` which is the **resolution log**, and to `TODO.md` which is the
forward-looking backlog (features, refactors).

## When to record

- During an audit (`/audit`, episode compaction sweep, security review,
  reference-manual sweep) — record everything you find, fix nothing
- Mid-task when a real bug surfaces but the user hasn't authorized fixing
- After user-reported issues that didn't get an immediate fix
- After cross-group consolidation (per-group `issues.md` → root `bugs.md`)

**Do NOT proactively fix** bugs found during a general check. Record, move
on, let the user prioritise — per project CLAUDE.md "Bug Triage Protocol".

## When NOT to record

- User is currently driving a fix — just fix
- Trivial / one-shot enough that a code comment is enough
- It's a feature request — goes in `TODO.md` or a new spec, not `bugs.md`
- Already an open entry covering the same root cause — append context to the
  existing entry instead of duplicating

## Entry format

H2 heading per entry. Date in parens; optional status if resolved-in-place.

```markdown
## <one-line title> (<YYYY-MM-DD>[, fixed | open | partial])

<paragraph: what's broken, observed impact, suspected root cause>

- **Severity:** high | medium | low
- **Scope:** platform | adapter | skill | group-config | docs | ops
- **Affected:** <instance/group(s)>
- **Source:** <file:line OR journal time OR issues.md location>
- **Status:** open | in-progress | resolved-not-yet-removed
- **Fix:** <commit SHA if fixed, else blank>
```

Resolved entries get a `**Fixed**` line (or a ✅ FIXED prefix on the title
line) carrying the commit SHA, before removal.

## When to remove

After ALL three:

1. The bug is fixed in code OR closed as won't-fix
2. A one-line reference is in `.diary/YYYYMMDD.md` under a "bugs.md cleanup"
   section (see `/diary` skill)
3. The fix is deployed to all affected instances (or marked as not-deployed)

Invoke this skill with `prune` to sweep ✅-marked entries.

## Aggregation from per-group `issues.md`

Per-group `issues.md` files
(`/srv/data/arizuko_<instance>/groups/<g>/issues.md`) accumulate user
reports. Periodically (weekly, or after a series of reports):

1. Spawn one read-only sub per instance to enumerate post-date entries
2. Synthesize into a single root `bugs.md` section grouped by cross-cutting
   pattern, debounced against prior aggregations
3. Note in each per-group file: "Consolidated to root `bugs.md` <date>"
4. Per-group `issues.md` stays as scratch — operator wipes after merge

Aggregation cadence: weekly, OR triggered by `/audit`, OR triggered by
this skill with `aggregate`.

## Cross-cutting patterns

Patterns spanning multiple groups/instances live at H4 under an H3
"### Cross-cutting platform patterns" inside an H2
"## Aggregated user-reported issues — <date>". Label them CC1, CC2, … so
they can be referenced by spec PRs and commit messages.

## Grooming cadence

Two motions:

- **Mark in place** — when a bug ships, prepend ✅ FIXED `<date>` to the
  title and add the commit SHA. Keep the entry; the next prune removes it.
- **Prune to diary** — periodically (per release, per refine pass) sweep
  ✅-marked entries; for each, write a one-line `.diary/YYYYMMDD.md` entry
  citing the bug title + commit SHA, then delete from `bugs.md`.

## Pointers

- `.diary/` — resolution log
- `TODO.md` — forward-looking backlog (features, refactors)
- `bugs.md` — open-issues queue (this skill)
- `specs/` — design surface (specs may reference bugs by anchor)
