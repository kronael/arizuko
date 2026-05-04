---
status: planned
---

# Products — curated agent templates

A product is a curated ant folder shipped as a starter template:
persona + skills + behavior bundled together. `arizuko create <name>
--product <product>` copies the chosen template into the new group's
folder.

Folder shape is defined by [ant](../8/b-ant-standalone.md); this spec
covers only the arizuko-side concerns: catalog, selection flow, schema.

## Catalog

`ant/examples/<name>/`. Initial set:

`assistant` (default), `developer`, `researcher`, `writer`,
`trader`, `ops`, `support`, `companion`.

Each is an ant folder — `SOUL.md`, `CLAUDE.md`, `skills/`, optional
`tasks.toml`, optional `facts/`. Plus `PRODUCT.md` frontmatter
declaring `name`, `tagline`, `skills` whitelist (seeded folder gets
only those skills, not the full curated set).

## Selection

```bash
arizuko create mybot --product developer
```

`cmdCreate` reads `ant/examples/<name>/PRODUCT.md`, copies files into
the new group folder, seeds only listed skills. Onboarding: state
`awaiting_product` between name and approval; product stored on
`onboarding` row and used by `spawnWorld`. `.product` marker in group
root carries product name for hello skill and dashd listing.

## Schema

```sql
ALTER TABLE groups ADD COLUMN product TEXT DEFAULT 'assistant';
```

## Open

- Third-party products: `/srv/data/.../products/` per-instance vs
  global catalog — defer
- `--product` accepting a URL / git repo — defer
