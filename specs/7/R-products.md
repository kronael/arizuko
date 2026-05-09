---
status: active
---

# Products — curated agent templates

A product is a curated template for an ant (agent). It bundles a persona,
skills, and seed files so an operator can spin up a configured agent with one
command instead of building from scratch.

## What a product is

Products live in `ant/examples/<name>/`. Each product folder is a prototype
for the group workspace that gets seeded into the new instance.

Required file:

```
PRODUCT.md     TOML manifest: name, brand, tagline, skills list, env hints
```

Optional files (copied verbatim into the group's data dir):

```
SOUL.md        Agent persona — name, description, voice, scope, do/don't list
CLAUDE.md      Operator runbook — memory conventions, escalation rules, KB structure
facts/         Seed fact files (style guides, reference data, KB stubs)
tasks.toml     Seed scheduled tasks
```

`SOUL.md` is the agent's identity. `CLAUDE.md` is the per-group runbook that
the agent reads every session. Both are optional but most products ship both.

Skills are not stored in the product folder. `skills` in `PRODUCT.md` is a
whitelist that controls which global skills (`ant/skills/`) get seeded into
the new group. The full skill set lives in the image; products select a subset.

## How to install a product

```bash
arizuko create mybot --product support
```

`cmdCreate` in `cmd/arizuko/main.go`:

1. Locates `ant/examples/<name>/PRODUCT.md` and parses it.
2. Creates the instance data dir and seeds `.env` with `ASSISTANT_NAME`,
   `CONTAINER_IMAGE`, `API_PORT`, `CHANNEL_SECRET`.
3. Creates the `main` group row in the DB with `product=<name>`.
4. Calls `container.SetupGroup(cfg, "main", productDir)` which:
   - Creates `groups/main/` and `groups/main/logs/`
   - Copies all files from `ant/examples/<name>/` into the group dir
     (SOUL.md, CLAUDE.md, facts/, tasks.toml — whatever is present)
   - Seeds `.claude/skills/` from `ant/skills/` (all skills from the image)
   - Seeds `.claude/CLAUDE.md` from `ant/CLAUDE.md` (the global ant runbook)
   - Chowns the group dir to UID 1000 so the container can write to it
5. Prints env hints from `PRODUCT.md [[env]]` blocks so the operator
   knows which keys to set in `.env` before running.

After creating:

```bash
# Edit .env to add required tokens (printed by create)
arizuko run mybot
```

## PRODUCT.md format

TOML file at the root of each product folder:

```toml
name    = "support"
brand   = "atlas"
tagline = "Embedded support agent — answers from your knowledge base, escalates when stuck."
skills  = ["diary", "facts", "recall-memories", "users", "issues", "web"]

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = true
hint     = "BotFather token — primary channel + escalation target"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional — enables web search fallback"
```

`skills` is informational today — `SetupGroup` seeds all global skills. It
exists to document which skills the product actively uses and for future
skill-gating. `[[env]]` blocks are printed by `arizuko create` as setup
instructions; they do not automatically validate the env file.

## Current catalog

Products in `ant/examples/` (shipped):

| Name     | Brand      | Tagline                                                |
| -------- | ---------- | ------------------------------------------------------ |
| personal | fiu        | Personal assistant with persistent memory              |
| support  | atlas      | KB-backed support agent, escalates to human when stuck |
| trip     | may        | Multi-step travel research → structured itinerary      |
| strategy | prometheus | Domain tracker; weekly synthesis → team briefing       |
| pm       | sloth      | Team task board + weekly digest                        |
| reality  | rhias      | Ongoing life-context thread holder                     |
| creator  | inari      | Content pipeline — draft, refine, publish on approval  |
| socials  | phosphene  | Multi-platform distribution, schedule + engagement     |

Public pages at `/pub/products/<name>/` when the web layer is running.

## Creating a new product

1. Create `ant/examples/<name>/`:
   - Write `PRODUCT.md` with `name`, `brand`, `tagline`, `skills`, and any
     `[[env]]` blocks the operator needs to fill in.
   - Write `SOUL.md` with frontmatter (`name`, `description`) and persona
     sections (voice, what it does, what it never does).
   - Write `CLAUDE.md` with the per-group runbook (KB lookup order, memory
     conventions, escalation rules, response style, scope).
   - Add `facts/` seed files if the product needs pre-populated knowledge.
   - Add `tasks.toml` if the product has scheduled tasks.

2. Test locally:

   ```bash
   arizuko create testbot --product <name>
   arizuko run testbot
   ```

3. Add a spec file `specs/7/product-<name>.md` documenting skills, channels,
   dependencies, and the web page pitch.

4. Add the product to the catalog table in `specs/6/index.md`.

No code changes are required — `cmdCreate` discovers products by scanning
`ant/examples/` at runtime.

## Open

- Skill whitelisting: `skills` list in PRODUCT.md is not enforced yet; all
  global skills are seeded regardless.
- Third-party products: per-instance product dirs — defer.
- `--product` accepting a URL or git repo — defer.
