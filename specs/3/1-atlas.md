---
status: shipped
---

## <!-- trimmed 2026-03-15: status markers removed, rich facts only -->

## status: shipped

# Atlas: What We Actually Need

## Facts

YAML markdown knowledge files in `groups/root/facts/`. Schema: `path`,
`category`, `topic`, `verified_at`, `header` (dense summary). 85+ files.

`/facts` skill handles retrieval (explore subagent scans headers),
research (web + codebase search, write/update files), and verification
(cross-check + stamp `verified_at`). Age gate: 14 days triggers refresh.

## Persona / Gatekeeper

CLAUDE.md + character.json + group trigger mode.

## Sandboxed Support (product pattern)

```
atlas/               tier 1: world admin
  atlas/support      tier 2: research backend (rw facts/)
    atlas/support/web  tier 3: user-facing (ro, escalate-only)
```

Worker escalates to parent when facts insufficient. Product
configuration, not gateway code (uses specs/2/5-permissions.md).

## Deferred (v2)

- Semantic similarity search (embeddings)
- Automatic injection into every prompt
- Background researcher cron
