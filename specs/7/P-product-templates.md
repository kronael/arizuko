---
status: planned
---

# Product templates

Ship curated ant folders as deployable starting points. `arizuko create
<instance> --product <name>` seeds the new group from `ant/examples/<name>/`.

## What exists already

`container.SetupGroup(cfg, folder, prototype)` already copies a prototype
directory into the group dir via `CopyDirNoSymlinks`. The `--product` flag
is wiring that to a named path in `ant/examples/`.

## Template structure

```
ant/examples/<name>/
  PRODUCT.md        — manifest (see below)
  SOUL.md           — persona
  CLAUDE.md         — runbook (behavior, memory layout, formats)
  facts/            — seed files (knowledge base, preferences, task board)
  tasks.toml        — scheduled tasks (optional)
```

Skills are not bundled in the template — they are seeded by `seedSkills`
from `ant/skills/` on every `SetupGroup` call. `PRODUCT.md` declares which
skills the operator must enable (via grants or env vars), not which files to
copy.

## PRODUCT.md

```toml
name    = "atlas"
tagline = "Customer support agent — answers from your knowledge base."
skills  = ["diary", "facts", "recall-memories", "users", "web"]

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = true
hint     = "BotFather token for the support channel"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional — enables web search via oracle"
```

`skills` is informational only (shown in `arizuko create` output as a
checklist). Env hints are printed after create so the operator knows what
to set in `.env`.

## CLI change

```
arizuko create <instance> --product <name>
```

`cmdCreate` resolves `<name>` to `<HostAppDir>/ant/examples/<name>/`,
reads `PRODUCT.md`, calls `SetupGroup(cfg, "main", protoPath)`, then
prints the env checklist. Unknown product → fatal with list of known names.

Default (no flag): existing behaviour (blank group, no prototype).

## Deployment flow for an operator

```bash
arizuko create myatlas --product atlas
# Output:
#   created instance myatlas at /srv/data/arizuko_myatlas
#
#   Required env — set in /srv/data/arizuko_myatlas/.env:
#     TELEGRAM_BOT_TOKEN   (required) BotFather token for the support channel
#
#   Skills enabled: diary, facts, recall-memories, users, web
#
#   Next: populate groups/main/facts/ with your knowledge base, then:

arizuko run myatlas
```

## Initial product set

| Name     | Brand      | facts/ seed                      |
| -------- | ---------- | -------------------------------- |
| personal | fiu        | preferences.md (placeholder)     |
| support  | atlas      | kb/ (empty, operator fills)      |
| trip     | may        | preferences.md (placeholder)     |
| strategy | prometheus | domain.md, watchlist.md          |
| pm       | sloth      | tasks.md (empty board), team.md  |
| reality  | rhias      | threads/ (empty, operator seeds) |
| creator  | inari      | style.md (placeholder)           |
| socials  | phosphene  | channels.md, voice.md            |

## Open

- Third-party templates (URL / git repo as `--product`) — defer
- Per-instance custom template dir — defer
- `arizuko product list` to enumerate known templates — easy add-on
