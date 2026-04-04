---
status: spec
---

# Products — Agent Personality Templates

A product is a curated agent configuration: persona + skills + behavior.
Users pick a product when creating a group. The agent feels like something
specific rather than a generic assistant.

## Problem

Every new group gets all 35 skills and the same generic SOUL.md. Users
don't know what the agent is for. The hello message lists everything.
There's no identity, no focus, no personality. Users need to feel like
they're talking to something purposeful.

## What's a Product

A directory in `ant/products/<name>/` containing:

```
ant/products/developer/
  PRODUCT.md          # name, tagline, skill list
  SOUL.md             # persona (voice, tone, identity)
  HELLO.md            # greeting template (replaces hello skill default)
  SYSTEM.md           # optional system prompt override
  facts/              # optional seed knowledge
  tasks.toml          # optional scheduled tasks
```

### PRODUCT.md

```yaml
---
name: Developer
tagline: Writes code, reviews PRs, knows your stack.
skills:
  - bash
  - cli
  - commit
  - go
  - python
  - rust
  - typescript
  - sql
  - testing
  - service
  - ops
  - infra
  - web
  - refine
  - ship
  - specs
capabilities:
  - voice
  - web
  - media
---
```

`skills` lists which stock skills to seed. Core skills (self, migrate,
dispatch, reload, diary, facts, recall-memories, recall-messages,
compact-memories, users, hello, info) are always included — they're
infrastructure, not product-specific.

`capabilities` controls `.gateway-caps` flags.

### SOUL.md

Product-specific persona. Copied to group on creation. Defines voice,
greeting style, behavior boundaries.

### HELLO.md

Overrides the default hello skill feature list. Products show only
what's relevant. A researcher doesn't mention code review. A trader
doesn't mention web deployment.

### SYSTEM.md

Optional. Replaces Claude Code's default system prompt. Used by
research-style products that need strict behavior rules (knowledge-first,
no code modification, cite sources).

## Product Catalog

### 1. assistant (default)

The general-purpose agent. All skills, all capabilities. For users
who want an everything-agent. This is what ships today.

```
Tagline: Your personal AI assistant. Research, code, create, automate.
Skills: all stock skills
Persona: Calm, capable, direct. No fluff. Gets to work.
```

### 2. developer

Code-focused agent. Writes, reviews, refactors, deploys. Knows your
stack. Opinionated about quality.

```
Tagline: Writes code, reviews PRs, knows your stack.
Skills: bash, cli, commit, go, python, rust, typescript, sql,
        testing, service, ops, infra, web, refine, ship, specs
Persona: Technical, precise, slightly opinionated. Prefers boring
         code. Flags complexity. Suggests simpler alternatives.
```

### 3. researcher

Knowledge-first agent. Builds fact bases, cites sources, never guesses.
The Atlas pattern generalized.

```
Tagline: Researches deeply, cites sources, never guesses.
Skills: research, facts, recall-memories, web, data
System: Knowledge-first rule (scan facts before answering,
        research when uncertain, cite file:line)
Persona: Methodical, evidence-driven. Says "I don't know" when
         uncertain. Emits status updates during long research.
```

### 4. writer

Content creation agent. Tweets, blog posts, documentation, editorial.
Has voice and style opinions.

```
Tagline: Writes content with voice. Tweets, posts, docs.
Skills: tweet, research, web, wisdom
Persona: Dense, narrative, no marketing fluff. Edits aggressively.
         Cuts filler words. Values information density.
```

### 5. trader

Market analysis and trading tools. The REDACTED pattern.

```
Tagline: Market analysis, position tracking, trade execution.
Skills: trader, data, research, web
Persona: Quantitative, calm under pressure. Reports numbers,
         not narratives. Flags risk clearly.
```

### 6. ops

Infrastructure and operations. Deploys, monitors, debugs production.

```
Tagline: Deploys, monitors, debugs production systems.
Skills: bash, cli, ops, infra, commit, service, go, python
Persona: Cautious, methodical. Confirms before destructive ops.
         Thinks in terms of blast radius and rollback.
```

### 7. support

Customer-facing Q&A agent. Friendly, helpful, knows the product.
For teams that want a support bot in their community channels.

```
Tagline: Answers questions, helps users, knows the product.
Skills: research, facts, recall-memories, web
System: Knowledge-first (like researcher but conversational)
Persona: Friendly but not bubbly. Admits gaps. Escalates when
         stuck. Uses simple language, avoids jargon.
```

### 8. companion

Personal companion. Casual, remembers everything, daily check-ins.
For users who want a persistent friend, not a tool.

```
Tagline: Remembers you. Checks in. Helps with life.
Skills: research, web, data
Tasks: daily check-in (morning greeting + weather + calendar)
Persona: Warm, personal, uses your name. Remembers past
         conversations. Asks follow-up questions. Not a yes-man —
         pushes back gently when you're wrong.
```

## Selection

### CLI

```bash
arizuko create mybot --product developer
arizuko create research-lab --product researcher
arizuko create mybot                          # defaults to assistant
```

`cmdCreate` reads `ant/products/<name>/PRODUCT.md`, copies product
files into the group folder, seeds only listed skills.

### Onboarding

When `ONBOARDING_ENABLED=true` and a new user arrives:

```
State machine addition:
  awaiting_name → awaiting_product → pending → approved

"Pick a product:"
1. Assistant — does everything
2. Developer — writes and reviews code
3. Researcher — deep research, cites sources
4. Writer — content with voice
5. Trader — markets and positions
6. Ops — infrastructure and deploys
7. Support — answers questions
8. Companion — personal assistant

(or type a number)
```

Product choice stored in `onboarding` table: `product TEXT DEFAULT 'assistant'`.
On approval, `spawnWorld` uses the product config for seeding.

### Prototype inheritance

Prototypes can reference a product:

```
groups/root/prototype/
  .product              # contains "support" (product name)
  SOUL.md               # can override product SOUL.md
```

When spawning from prototype:

1. Read `.product` if exists
2. Load product config (skills, SYSTEM.md, HELLO.md)
3. Override with prototype-specific files (SOUL.md, CLAUDE.md)
4. Seed the child

## Implementation

### Seeding changes

`container.SeedGroupDir(cfg, folder)` currently seeds ALL skills.
Change to:

```go
func SeedGroupDir(cfg core.Config, folder string, product string) {
    p := loadProduct(cfg.AppDir, product) // reads ant/products/<name>/

    // Core skills always seeded
    for _, s := range coreSkills {
        seedSkill(cfg, folder, s)
    }

    // Product skills
    for _, s := range p.Skills {
        seedSkill(cfg, folder, s)
    }

    // Product files
    if p.SoulMD != "" { copyIfNotExists(p.SoulMD, groupDir+"/SOUL.md") }
    if p.SystemMD != "" { copyIfNotExists(p.SystemMD, groupDir+"/SYSTEM.md") }
    if p.HelloMD != "" { copyIfNotExists(p.HelloMD, groupDir+"/.claude/skills/hello/HELLO.md") }
    if p.Facts != nil { seedFacts(p.Facts, groupDir+"/facts/") }
    if p.Tasks != nil { seedTasks(p.Tasks, folder) }

    // Write product marker
    writeFile(groupDir+"/.product", product)
}
```

### Core skills (always seeded)

```
self, migrate, dispatch, reload, diary, facts,
recall-memories, recall-messages, compact-memories,
users, hello, info, howto, acquire, agent-browser
```

These are system infrastructure — every product needs them.

### Product marker

`.product` file in group root contains the product name. Used by:

- Hello skill (reads HELLO.md from product if exists)
- Status reporting (dashd shows product per group)
- Future: product upgrades (re-seed on image update)

### Hello skill integration

Hello skill already reads SOUL.md for persona. Add:

```
1. Read SOUL.md for persona
2. Read ~/.claude/skills/hello/HELLO.md if exists (product override)
3. If HELLO.md exists, use its feature list instead of default
4. Fall back to current full feature list
```

### Store changes

```sql
ALTER TABLE groups ADD COLUMN product TEXT DEFAULT 'assistant';
```

Exposed via `get_routes` / group listing in dashd.

### Migration

Existing groups get `product = 'assistant'` (backward compatible).
No behavior change for existing deployments.

## File layout

```
ant/
  products/
    assistant/
      PRODUCT.md
      SOUL.md              # current default SOUL.md
    developer/
      PRODUCT.md
      SOUL.md
      HELLO.md
    researcher/
      PRODUCT.md
      SOUL.md
      HELLO.md
      SYSTEM.md            # knowledge-first system prompt
    writer/
      PRODUCT.md
      SOUL.md
      HELLO.md
    trader/
      PRODUCT.md
      SOUL.md
      HELLO.md
    ops/
      PRODUCT.md
      SOUL.md
      HELLO.md
    support/
      PRODUCT.md
      SOUL.md
      HELLO.md
      SYSTEM.md
    companion/
      PRODUCT.md
      SOUL.md
      HELLO.md
      tasks.toml           # daily check-in
  skills/                  # stock skills (unchanged)
  CLAUDE.md                # shared agent instructions
  SOUL.md                  # fallback persona (= assistant product)
```

## What This Replaces

- `container/worlds/` concept from Z-versioning-personas.md → `ant/products/`
- Manual SOUL.md setup → product selection
- All-skills-everywhere → curated skill sets per product

## What This Does NOT Do

- Plugin composition (deferred — products are atomic, not composable)
- Per-skill versioning (still global MIGRATION_VERSION)
- Marketplace or user-contributed products (future)
- Runtime product switching (recreate group to change product)

## Implementation Order

1. Create `ant/products/` directory structure with all 8 products
2. Write SOUL.md + HELLO.md for each product
3. Write SYSTEM.md for researcher and support
4. Modify `SeedGroupDir` to accept product parameter
5. Add `--product` flag to `arizuko create`
6. Add product column to `groups`
7. Wire onboarding product selection
8. Update dashd to show product per group
9. Write tasks.toml for companion (daily check-in)
