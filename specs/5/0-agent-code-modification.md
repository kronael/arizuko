---
status: planned
---

# Agent Code Modification (Staging Area)

## Problem

Root agent (tier 0) can see gateway source via /workspace/self/
(ro mount). It should be able to propose code changes without
direct write access to the running gateway.

## Architecture

Agent writes proposed changes to a staging directory. Gateway
applies them on restart (or via explicit CLI command). Never
live-patched.

```
/workspace/self/          ro — agent reads current source
/workspace/staging/       rw — agent writes proposed changes
```

On restart or `kanipi apply-staging <instance>`:

1. Diff staging/ against self/
2. Show changes for review (or auto-apply if configured)
3. Apply to gateway source
4. Clear staging/
5. Rebuild if needed

## Scope

Only root (tier 0) gets staging access. World and agent tiers
cannot propose code changes.

## Open Questions

1. **Auto-apply vs review** — should staging changes require
   manual approval? Or trust root agent? Probably configurable.

2. **What can be staged** — full source? Or only specific
   files (config, skills, CLAUDE.md)? Restricting to non-core
   files is safer.

3. **Conflict resolution** — what if gateway source changed
   since agent read it? Git-style merge? Reject and re-read?

4. **Rebuild trigger** — if TypeScript source changes, need
   `make build`. Who triggers this? The apply command?

5. **Rollback** — if applied changes break the gateway,
   how to revert? Keep backup of previous state?

6. **Audit trail** — log what was changed, when, by which
   agent session. Important for multi-agent setups.
