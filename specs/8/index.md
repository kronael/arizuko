---
status: active
---

# specs/8 — self-healing (Aeon mechanism incorporation)

A deliberate import of the three load-bearing mechanisms from
[Aeon](https://github.com/aaronjmars/aeon) — a GitHub-Actions-based
agent framework with 121 skills and a "self-healing" reputation. Aeon
runs on cron in GHA; arizuko is a channel-native multi-tenant
platform. The architecture does not port. The mechanisms do.

Source analysis (memory): `reference_aeon.md`. Of the 9 mechanisms
catalogued, three are imports for this phase, three are deferred to
later phases, three are skipped because arizuko has a better answer.

## Why this phase exists

Aeon's "self-healing" reduces to a small loop:

1. **LLM-as-quality-gate** — Haiku scores Opus output after every run
   (1-5 + flags + reasoning, JSON).
2. **State-evaluator** — periodic check on a rolling score history
   fires a reactive trigger when conditions match.
3. **Repair playbooks** — opinionated skills the agent invokes on the
   reactive trigger.

Combined with a cooldown table, that loop turns a noisy failure mode
into a tracked, throttled, agent-driven repair. arizuko doesn't have
this loop today. It should — once, mechanically, riding existing
primitives.

## Design principle (carried from CLAUDE.md)

**Minimality and orthogonality.** Don't introduce new primitives where
the messaging primitive carries the seam.

- State-evaluator is a message producer, like `timed`. No new trigger
  type in the gateway, no new MCP tool, no new daemon shape.
- Repair playbooks are skills, not infrastructure. They drop into
  `ant/skills/` and are invoked via normal dispatch when the agent
  handles `verb=state-event`.
- Self-eval is a Haiku sub-query at container exit, writing a row to
  `skill_health`. Same shape as the existing `specs/11/8-self-eval`
  predecessor — but with a stronger judge and a persistent surface.

The seam is the SQLite schema (`skill_health`, `cooldowns`) plus the
existing message bus. Nothing else.

## Specs in this phase

| Spec                                                             | Status | Hook                                                               |
| ---------------------------------------------------------------- | ------ | ------------------------------------------------------------------ |
| [1-self-eval-haiku.md](1-self-eval-haiku.md)                     | draft  | Haiku scores Opus output; writes `skill_health` rows per run.      |
| [2-state-evaluator.md](2-state-evaluator.md)                     | draft  | Message producer fires `verb=state-event` on conditions; cooldown. |
| [3-repair-playbooks.md](3-repair-playbooks.md)                   | draft  | Opinionated skills for typed failure recovery (api, rate, schema). |
| [4-positioning-componentized.md](4-positioning-componentized.md) | draft  | Capture componentized-daemon vs monolithic-GHA as the real diff.   |
| [5-skill-catalog-audit.md](5-skill-catalog-audit.md)             | draft  | Map Aeon's 121 skills against arizuko's 56; prioritise imports.    |

## Cross-spec dependencies

- **`specs/4/P-personas.md`** — skills lifecycle, versioning, migration
  hook. Repair playbooks ship under the same lifecycle (skill file in
  `ant/skills/`, MIGRATION_VERSION bump, auto-broadcast on tag).
- **`specs/11/8-self-eval-skill.md`** — the predecessor self-eval
  spec, where the judge was the same model via in-container
  `query()`. Superseded by `8/1` — Aeon's Haiku-judges-Opus is the
  stronger design (cheaper judge, less reflexive). The 11/8 spec
  retains the trigger-shape research; 8/1 supersedes the model
  choice and the data shape.
- **`specs/11/6-workflows.md`** — workflowd. `8/4` proposes dropping
  this in favour of "any automation is a folder" — see that spec for
  the case.
- **`specs/6/Y-secret-broker.md`** — arizuko's answer to Aeon's
  prefetch-outside-sandbox pattern. Not imported; called out in
  `8/4` as "we already do this, but better".

## Out of scope for this phase

- **Regression-hunter / git-blame** (Aeon mechanism #5). Useful for
  the arizuko _codebase_ (developer tool), not for agents.
- **Cluster-first triage** (mechanism #6). Defer until `skill_health`
  data accumulates and signature-grouping has signal to chew on.
- **24h repair cooldown** (mechanism #9). Folded into `8/2` as the
  cooldowns table; not a separate spec.
- **Pull-based Telegram scheduler** (mechanism #8). Skip; we have
  instant webhooks.
- **Git-as-database** (mechanism #1). Skip; SQLite + per-folder
  files is more flexible.
