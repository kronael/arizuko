---
status: planned
---

# Products — Agent Personality Templates

A product is a curated agent configuration: persona + skills + behavior.
Users pick a product when creating a group, instead of getting all
35 skills + generic SOUL.md. Products also ship as distributable
instance blueprints (git-repo form, like Helm charts).

## Layout

```
ant/products/<name>/
  PRODUCT.md          # name, tagline, skill list, capabilities
  SOUL.md             # persona
  HELLO.md            # greeting template
  SYSTEM.md           # optional system prompt override
  facts/              # optional seed knowledge
  tasks.toml          # optional scheduled tasks
```

PRODUCT.md frontmatter: `name`, `tagline`, `skills:` list of stock
skill names, `capabilities:` list (voice, web, media). Core skills
(self, migrate, dispatch, reload, diary, facts, recall-memories,
recall-messages, compact-memories, users, hello, info, howto,
acquire, agent-browser) are always seeded.

## Catalog

assistant (default, all skills), developer, researcher, writer,
trader, ops, support, companion.

## Selection

```bash
arizuko create mybot --product developer
```

`cmdCreate` reads `ant/products/<name>/PRODUCT.md`, copies files,
seeds only listed skills. Onboarding: add state `awaiting_product`
between name and approval; product stored on `onboarding` row and
used by `spawnWorld`. Prototype `.product` marker lets child groups
inherit.

## Schema

```sql
ALTER TABLE groups ADD COLUMN product TEXT DEFAULT 'assistant';
```

`.product` marker file in group root carries product name for hello
skill and dashd listing.

## Seeding contract

```go
func SeedGroupDir(cfg core.Config, folder, product string)
```

Reads `ant/products/<product>/`, seeds core skills + product skills,
copies SOUL.md / HELLO.md / SYSTEM.md / facts/ / tasks.toml if
present, writes `.product` marker.

## Instance blueprints

Products also distribute as full-instance git repos:

```
arizuko-<product>/
├── .env.example        # config template, tokens as placeholders
├── PRODUCT.md
├── SOUL.md
├── groups/root/        # CLAUDE.md, facts/, tasks.toml
└── README.md
```

CLI:

```bash
arizuko create <name> --from <repo-url-or-path>
arizuko update <name> --from <repo-url>   # merge groups/, keep .env
```

Clone → `/srv/data/arizuko_<name>/` → copy `.env.example` → `.env` →
copy groups/ → generate systemd unit → register groups.

## Not in scope

Plugin composition (products are atomic), per-skill versioning,
runtime product switching (recreate group), marketplace.
