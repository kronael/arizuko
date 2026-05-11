---
status: planned
---

# Template distillation — harvest live wisdom back to `ant/examples/`

Inverse of `/migrate`. Knowledge flows one way today: template → live
group. Refinements accrued in a deployed group never make it back to
`ant/examples/<product>/`; each new operator starts from a stale
template and re-learns the same lessons.

## Why

Atlas (`groups/atlas/`) has accrued wisdom the support template
doesn't carry: a `facts/CLAUDE.md` index of 51 topics with confidence
scores, a canonical source-path convention
(`refs/ds-sam-pipeline/auctions/<epoch>.<slot>/outputs/results.json`),
CLAUDE.md drift in "When to respond" and tool-ack sections. None
lands in `ant/examples/support/`, so a fresh deploy reproduces the
discovery sequence — the five-correction `bidTooLowPenalty.coef`
exchange that motivated `/support` (specs/7/2) repeats per operator.

## What

`/distill-product <product>` — root-only, manual trigger, gated by
operator review. Delegates the eventual 3-way reconciliation to
`/migrate`; this skill only thickens the upstream side. Five phases:

1. **Harvest** — for each live group running `<product>`: skills with
   `managed: local` or local edits (diff vs `ant/skills/`), CLAUDE.md
   sections in group not in template, `facts/CLAUDE.md` topic index
   (filenames + headers, not bodies), `SOUL.md` diff, `sources.md`
   schema. Excluded: `users/` (per-sender memory reconstructs the
   person even after strip); diary bodies (keep date layout + frontmatter,
   prose → `<entry>`).
2. **Strip** — deterministic regex pass (table below). Output:
   rewritten file + `strip-report.jl` (every substitution) +
   `flagged.jl` (unrecognized high-entropy tokens) for phase 3.
3. **Generalize** — operator-driven: keep noun-phrase shape
   (`<entity>-<facet>-<resource>`), strip only proper nouns. E.g.
   leave `validator-bid-penalty`; rewrite `marinade-sam-auctions` →
   `<domain>-<pipeline>-<artefact>`.
4. **Land** — emit review bundle in `.ship/distill-<product>-<date>/`
   (operator workspace, ephemeral, gitignored): per-file diff +
   `strip-report.jl` + `flagged.jl`. Operator commits accepted output
   to `ant/examples/<product>/`.
5. **Re-migrate** — operator runs `/migrate` on the donor group
   against the freshened template. **Invariant: no-op for the donor**
   — we just absorbed its drift. Any conflict means strip/generalize
   regressed; flag for operator review, never auto-resolve.

Skill `description:` — `Harvest accumulated wisdom from a live group back to ant/examples/<product>/. Root-only manual trigger; produces a reviewable diff bundle. Use when a deployed group has accrued skills, CLAUDE.md drift, or facts/ structure worth porting upstream.`

## Stripping rules

| Drop (regex / pattern)                             | Keep (allowlist)                                     |
| -------------------------------------------------- | ---------------------------------------------------- |
| Base58 pubkeys `[1-9A-HJ-NP-Za-km-z]{32,44}`       | Entity _types_ (validator, account, epoch)           |
| Hex addresses `0x[0-9a-f]{40,}`, JIDs `\d+@s\.`    | Reference-source conventions (`refs/<proj>/...`)     |
| Operator paths `/srv/data/arizuko_\w+`             | Skill paths (`ant/skills/<name>/SKILL.md`)           |
| Host/instance names (`krons`, `solo`, `$WEB_HOST`) | Adapter + tier vocabulary                            |
| Assistant/persona names (`Atlas`, `Rhias`)         | `<assistant>` / `<persona>` role placeholders        |
| `groups/<folder>/` example paths                   | The shape `groups/<group>/facts/<topic>.md`          |
| Channel titles, group invite IDs                   | Channel + grant terminology                          |
| Secrets, tokens, API keys (error on retained)      | Env-var _names_ (`OPENAI_API_KEY`, `CHANNEL_SECRET`) |
| SOUL/PERSONA per-deploy quirks (specific in-jokes) | SOUL section structure (Voice, Vocabulary, Refuse)   |
| Diary entry bodies                                 | Diary date layout + section headings                 |

Derived-identity rule (CLAUDE.md "identity is configured, never
derived"): hostnames, container names, network names, instance
flavors leak operator config — strip to `<host>` / `<instance>` /
`<persona>` even in prose.

## Acceptance

Each criterion is a runnable check:

- `diff -u` of distilled `ant/examples/support/facts/CLAUDE.md` shows
  category structure + confidence column header, zero Marinade bodies.
- `grep -rE '[1-9A-HJ-NP-Za-km-z]{32,44}' ant/examples/support/` empty
  (no retained pubkeys).
- `grep -rwE '(krons|solo|Atlas|Rhias|arizuko_\w+)' ant/examples/support/`
  empty.
- `test -f ant/examples/support/facts/sources.md` and contains
  `refs/<canonical-source>/<entity>.<key>/outputs/<artefact>.json`
  (shape kept, values stripped).
- Donor-group `/migrate` dry-run reports zero conflicts on harvested
  files.
- Fresh `support` deploy: `/find` writes into a pre-shaped `facts/`
  with category hooks present.

## Open

- **Multi-donor** — single-donor v1; revisit when a second instance
  of the same product is worth comparing.
- **Cadence** — manual v1; auto-prompt after N turns is premature
  until strip rules are battle-tested.
