---
status: planned
---

# Products — curated agent templates

A product is a curated ant folder shipped as a starter template:
persona + skills + behavior bundled together. arizuko's
`arizuko create <name> --product <product>` copies the chosen
template into the new group's folder.

Folder shape is defined by [ant](../5/b-memory-skills-standalone.md);
this spec covers only the arizuko-side concerns: catalog, selection
flow, schema column.

## Catalog

`ant/examples/<name>/` (once ant ships, see
[5/b](../5/b-memory-skills-standalone.md)). Initial set:

`assistant` (default), `developer`, `researcher`, `writer`,
`trader`, `ops`, `support`, `companion`.

Each is just an ant folder — `SOUL.md`, `CLAUDE.md`, `skills/`,
optional `tasks.toml`, optional `facts/`. Plus `PRODUCT.md`
frontmatter declaring `name`, `tagline`, `skills` whitelist (so the
seeded folder gets only those skills, not the full curated set).

## Selection

```bash
arizuko create mybot --product developer
```

`cmdCreate` reads `ant/examples/<name>/PRODUCT.md`, copies files
into the new group folder, seeds only listed skills. Onboarding:
state `awaiting_product` between name and approval; product stored
on `onboarding` row and used by `spawnWorld`. Prototype `.product`
marker lets child groups inherit.

## Schema

```sql
ALTER TABLE groups ADD COLUMN product TEXT DEFAULT 'assistant';
```

`.product` marker file in group root carries product name for hello
skill and dashd listing.

## Out of scope

- The folder shape itself (lives in ant)
- Hot-reload of edited products (operator restarts container)
- User-authored products outside `ant/examples/` (future)

## Open

- Where do third-party products live? (`/srv/data/.../products/`?
  Per-instance vs global?) — defer
- Should `--product` accept a URL / git repo? — defer
