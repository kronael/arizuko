---
status: draft
depends: [5/5-uniform-mcp-rest]
---

# specs/6/A — hierarchical skills + self-skill

## Why

Today `ant/skills/` is a flat directory. `resolve` enumerates every
SKILL.md frontmatter on every turn to find matches. Skill catalog
size = per-turn cost in tokens + classifier work.

This becomes a hardening problem at scale: a tier-0 instance with
50+ skills enumerates 50+ descriptions on every prompt. The cost is
linear in catalog size; growth is bounded by enumeration cost, not
by useful capability.

## What this is

A nested skill layout where the catalog navigates like a directory
tree, not a flat list. `resolve` descends only the relevant subtree.

```
ant/skills/
  SKILL.md                ← the "self" skill (always-loaded index)
    frontmatter:
      name: self
      description: |
        Discover available skills. Lists categories with one-line
        descriptions. Descend by reading the SKILL.md in the named
        subdirectory.
    body:
      categories:
        - data/      — CSV/JSON/parquet wrangling
        - social/    — Slack/Discord/Telegram operations
        - ops/       — diary, facts, users, recall
        - meta/      — migrate, persona, hello, self
        - oracle/    — second-model consultation

  data/
    SKILL.md              ← category description (mid-level)
    csv/
      SKILL.md            ← leaf skill (full body)
    json/
      SKILL.md
    ...

  social/
    SKILL.md
    slack/SKILL.md
    discord/SKILL.md
    ...
```

## How `resolve` changes

Today: enumerate all `~/.claude/skills/*/SKILL.md` frontmatters.

Proposed:

1. Read `~/.claude/skills/SKILL.md` body (the index, always ≤200 tokens).
2. Haiku-classifier (or rule-match) selects a category.
3. Read `~/.claude/skills/<category>/SKILL.md` body.
4. Repeat descent until a leaf skill matches.
5. Load the leaf's body as instruction context.

Per-turn token cost becomes O(depth × avg-description-size) instead
of O(catalog-size × avg-description-size).

## Self-skill contract

`ant/skills/SKILL.md` is the always-loaded root. Frontmatter:

```yaml
name: self
description: Skill catalog index — read this body to discover capabilities
user-invocable: true # /self is a valid command
```

Body sections:

- **Categories** — one-line per subdirectory + description.
- **Primitive tools** — listing of MCP tools that are always available
  (send, reply, like, post, inspect\_\*, send_file, send_voice).
- **Continuation rule** — descend on relevance; if no category matches,
  fall back to enumerating leaves (degraded mode, equivalent to today).

## Migration

Existing flat skills are grouped by domain heuristic:

| Category | Skills moved in                                      |
| -------- | ---------------------------------------------------- |
| data/    | (currently none, future expansion)                   |
| social/  | (channel-specific reactions, threading, attachments) |
| ops/     | diary, facts, users, recall-memories, find, issues   |
| meta/    | migrate, persona, hello, self, wisdom                |
| oracle/  | oracle (codex/openai), find-llm                      |

The `/migrate` skill handles the directory restructuring for existing
groups. After migration, `~/.claude/skills/` matches the new layout.

## What stays flat

Custom user-written skills in `~/.claude/skills/<name>/` (without a
category prefix) remain enumerable. The catalog falls back to flat
discovery for any skill that isn't under a recognized category.
Operators don't lose the ability to add ad-hoc skills.

## Tools side: deferred disclosure

Skills are the _knowledge_ half of progressive disclosure. **Tools**
are the other half, and they have the same context-pollution problem
for the same reason — measured against the Anthropic API:

- Tool defs ride the **request prefix on every turn** (stateless
  Messages API). 1000 tool defs = 1000 sent every turn; prompt caching
  makes re-sending cheap (~10% on hit) but does NOT reduce
  context-window usage or attention dilution.
- **Mutating the `tools` array nukes the cache** from `tools` onward
  (system prompt + messages re-billed). So "enable/disable tools per
  turn" is the most expensive option.

The fix is Anthropic's native **Tool Search Tool**: tools marked
`defer_loading: true` leave the eager `tools` array; the model sees
only the Tool Search Tool + non-deferred tools, searches when it needs
a capability, and matching schemas expand as **message-stream tool
results** (append-only, cache-friendly) callable as native typed
tools. Measured: 85% token reduction; Opus 4.5 selection 79.5%→88.1%.

**The split for arizuko:**

- **Eager** (loaded every turn): core messaging + read — `send`,
  `reply`, `send_file`, `inspect_*` — plus Claude Code built-ins and
  `ToolSearch` itself.
- **Deferred** (`defer_loading: true`, found via search): connector
  tools (mounted via `ipc/connector.go`), rarely-used management tools
  (routes/web/tokens/group lifecycle).

### Skills vs tools — division of labor

The Tool Search Tool and the skill hierarchy are complementary, not
competing — they disclose different content:

- **Tool Search** discloses **tools** — discrete typed callable
  functions (`slack.chat_postMessage`). Native MCP, one call.
- **Skills** (`resolve` + this spec) disclose **knowledge + workflows**
  — how-tos, multi-step recipes, the rules around a tool set.

A connector's _tools_ are deferred MCP tools found via search; its
_usage guidance_ is a skill found via resolve. The skill may point at
tools; the tools don't need the skill to be callable.

### Enablement (shipped)

The SDK knob to defer MCP tools is **per-MCP-server `alwaysLoad?:
boolean`** (`@anthropic-ai/claude-agent-sdk` 0.3.153). `alwaysLoad:
true` keeps a server's tools eager; omit it and the server's tools
defer behind the Tool Search Tool. Wired in `ant/src/mcp-servers.ts`:
the `arizuko` server (core messaging) is `alwaysLoad: true`;
third-party connector servers default to deferred.

Limitation: `alwaysLoad` is per-server, and gated serves core +
management + `connectors.toml` tools through one `arizuko` server — so
gated's management tools ride eagerly with core. Deferring those needs
a gated-side server split (`arizuko-core` + `arizuko-mgmt`), Go-side,
only if the management surface grows enough to matter. Third-party
connectors (the Slack-200-tools case) already defer correctly.

**No-MCP-server case:** for an external service that publishes a REST
API but no MCP server, auto-generate deferred MCP tools from its
OpenAPI spec — `openapi2mcp` (Go library) + a curation/scope-annotation
layer. Belongs in the future `mcpfw` orthogonal component (see
[`../11/A-orthogonal-components.md`](../11/A-orthogonal-components.md)).
Research: `.ship/research-openapi-mcp.md`. For services that DO ship an
MCP server (most), mount it via `ipc/connector.go` — built.

## Acceptance

- `resolve` per-turn token cost drops from O(N) to O(log N) where N
  is skill count (in practice, O(depth) where depth ≤ 3).
- The `/self` command produces the same catalog as today's enumeration.
- Existing user skills (uncategorized) still discovered and dispatched.
- E2E test: 50-skill catalog, three-level deep; verify resolve picks
  the right leaf without enumerating the whole tree.

## Non-goals

- Not replacing `resolve`'s Haiku-classifier — same dispatcher, fewer
  candidates per call.
- Not changing skill body format. SKILL.md still markdown + frontmatter.
- Not introducing skill dependencies or DAGs. Skills stay independent.
- Not auto-categorization. Operators place skills in the right
  directory; the index lists what they're given.

## Open questions

1. **Depth cap.** Three levels (self → category → leaf) feels right.
   Should leaves be allowed to have sub-leaves? Lean: no, depth=3.
2. **Cross-category skills.** A skill relevant to multiple categories
   (e.g. `find` is ops AND meta) — symlink, primary-location, or
   listed in multiple SKILL.md indexes? Lean: symlink + primary.
3. **Category churn.** As new skills appear, who decides which
   category they belong to? Operator at authoring time. Skill scaffolding
   (`/wisdom create`) prompts for category.

## Pointers

- `ant/skills/resolve/SKILL.md` — current `resolve` implementation.
- `ant/skills/wisdom/SKILL.md` — skill-authoring entry point.
- `container/runner.go:SetupGroup` — seeds skills into new groups.
