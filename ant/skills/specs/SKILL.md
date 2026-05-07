---
name: specs
description: Write and manage arizuko specs in specs/.
when_to_use: Use when creating or updating design specs, architecture docs, or status tracking.
user-invocable: true
---

# Specs

Design references in `specs/`. Master index: `specs/index.md`.

## Frontmatter

```yaml
---
status: shipped|partial|spec|planned|draft
---
```

Lifecycle: `draft` → `spec` → `partial` → `shipped`.
`reference` for analysis docs that don't ship.

## File naming

`<phase>/<base58>-<topic>.md` (base58 = 0-9, A-H, J-N, P-Z, a-k, m-z) for
stable sort. Update `specs/index.md` when adding a spec.

## Specs contain

- **Problem** — why this exists, what was wrong before
- **Approach** — design decisions, tradeoffs, why this way
- **Code pointers** — where the code lives and why

## Specs don't contain

- Step-by-step implementation details (read the code)
- Code snippets that duplicate the codebase
- Completed checklists or TODO items

## After shipping

1. Update frontmatter to `status: shipped`
2. Trim implementation details; keep problem, design, why
3. Keep code pointers (where + why), drop how
