---
status: shipped
---

# Atlas

## Facts

YAML markdown knowledge files in `groups/root/facts/`. Schema: `path`,
`category`, `topic`, `verified_at`, `header` (dense summary). 85+ files.

`/find` skill handles retrieval (explore subagent scans headers),
research (web + codebase search, write/update files), and verification
(cross-check + stamp `verified_at`). Age gate: 14 days triggers refresh.

## Persona / gatekeeper

CLAUDE.md + character.json + group trigger mode.

## Sandboxed support (product pattern)

```
atlas/                 world admin
  atlas/support        research backend (rw facts/)
    atlas/support/web  user-facing (ro, escalate-only)
```

The user-facing leaf holds no write access and no outbound authority
beyond its own chat; when the facts are insufficient it escalates to the
parent, which owns the corpus. A public surface that could research
would also be a public surface that could be steered into writing.

This is product configuration over the platform primitives, not a
platform feature — the depth-derived tiers it originally leaned on are
gone ([`5-tool-authorization.md`](5-tool-authorization.md)); the same
shape is now expressed as explicit role/ACL grants
([`../5/33-paths-roles.md`](../5/33-paths-roles.md)).

## Deferred (v2)

- Semantic similarity search (embeddings)
- Automatic injection into every prompt
- Background researcher cron
