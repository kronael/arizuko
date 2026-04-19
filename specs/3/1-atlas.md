---
status: shipped
---

# Atlas

## Facts

YAML markdown knowledge files in `groups/root/facts/`. Schema: `path`,
`category`, `topic`, `verified_at`, `header` (dense summary). 85+ files.

`/facts` skill handles retrieval (explore subagent scans headers),
research (web + codebase search, write/update files), and verification
(cross-check + stamp `verified_at`). Age gate: 14 days triggers refresh.

## Persona / gatekeeper

CLAUDE.md + character.json + group trigger mode.

## Sandboxed support (product pattern)

```
atlas/               tier 1: world admin
  atlas/support      tier 2: research backend (rw facts/)
    atlas/support/web  tier 3: user-facing (ro, escalate-only)
```

Worker escalates to parent when facts insufficient. Product config,
uses `5-permissions.md`.

## Deferred (v2)

- Semantic similarity search (embeddings)
- Automatic injection into every prompt
- Background researcher cron
