---
status: planned
---

# Gateway Self-Modification — open

How the root agent modifies the gateway itself. Beyond agent
self-extension (skills, MCP) — this is about modifying the
gateway codebase, adding actions, changing routing logic.

## Problem

The root agent runs Claude Code with full development capabilities.
It should be able to:

1. Add new gateway actions without manual deploys
2. Fix bugs in gateway code
3. Add new channel adapters or integrations
4. Extend routing/permission logic

But changes need to:

- Stay in version control
- Not conflict with upstream kanipi updates
- Be testable before deploy
- Not break running instances

## Current state

Root agent has `/workspace/self` mounted read-only. It can read
gateway source but cannot modify it. Changes require:

1. Human edits code in `/home/onvos/app/kanipi/`
2. `make build && make image`
3. Restart systemd service

No path for agent-initiated gateway changes.

## Open questions

### 1. Where do agent changes live?

Options:

a) **Fork model** — agent works on a fork, human merges

- Pro: clean git history, easy upstream sync
- Con: requires human in loop for every change

b) **Plugin directory** — `gateway-plugins/` loaded at runtime

- Pro: isolated from core, survives upstream updates
- Con: limited to plugin API surface

c) **Patch model** — agent generates patches, applied on build

- Pro: changes reviewable, can rebase on upstream
- Con: patches break when upstream changes

d) **Branch model** — agent commits to `agent-changes` branch

- Pro: full git history, can cherry-pick
- Con: merge conflicts accumulate

### 2. How to test changes safely?

Options:

a) **Staging instance** — deploy to test instance first
b) **Hot reload** — gateway watches for changes, reloads modules
c) **Shadow mode** — run modified code in parallel, compare output
d) **Unit tests only** — agent runs `make test` before proposing

### 3. What can the agent modify?

Scope options:

a) **Actions only** — new `src/actions/*.ts` files
b) **Actions + handlers** — also mime-handlers, channel adapters
c) **Full access** — any gateway code except auth/permissions
d) **Everything** — including security-critical code

### 4. How to sync with upstream?

When upstream kanipi updates:

a) **Manual rebase** — human resolves conflicts
b) **Auto-merge** — CI attempts merge, alerts on conflict
c) **Plugin isolation** — plugins unaffected by core updates
d) **Versioned plugins** — plugins declare compatible gateway version

### 5. Permission model for self-modification

Who can trigger gateway changes?

a) **Root only** — tier 0 exclusive
b) **Root + approval** — root proposes, human approves
c) **Any tier + approval** — lower tiers can propose via escalation

### 6. Rollback strategy

When a change breaks the gateway:

a) **Git revert** — agent or human reverts commit
b) **Canary deploy** — gradual rollout, auto-rollback on errors
c) **Immutable images** — keep previous image, switch back
d) **Feature flags** — disable broken code path without redeploy

## Strawman: plugin directory

Minimal viable approach:

```
/home/onvos/app/kanipi/
  plugins/
    actions/           # additional action handlers
    handlers/          # additional mime handlers
    channels/          # additional channel adapters
```

Gateway loads plugins at startup:

```typescript
// src/plugin-loader.ts
const pluginActions = await loadPluginActions('plugins/actions/');
actionRegistry.registerAll(pluginActions);
```

Root agent has `/workspace/self/plugins` mounted read-write.
Agent can add/modify plugin files. Gateway restart picks up changes.

Upstream updates don't touch `plugins/` — clean separation.

## Strawman: agent branch + CI

More powerful but complex:

1. Root agent has full repo access (rw mount of gateway source)
2. Agent commits to `agent/<instance>` branch
3. CI runs tests on push
4. If tests pass, CI builds image and deploys
5. Human reviews commits periodically, merges to main

Requires:

- CI pipeline for agent branches
- Automated test coverage
- Image tagging per branch
- Rollback automation

## Dependencies

- Permissions spec (v1m1) — tier 0 capabilities
- Testing spec (v1) — CI infrastructure
- Container model — mount configuration

## Out of scope

- Multi-instance coordination (agents syncing changes)
- Agent-to-agent code review
- Automated upstream PR creation
