---
status: planned
---

# Plugin layer for MCP-tool capabilities

Ergonomic, multi-tenant install of MCP-server-backed capabilities
(image generation, video, transcription variants, web scraping, …)
without hand-editing `settings.json` and `.env` per-folder.

## Why

arizuko already loads extra MCP servers from `~/.claude/settings.json`
via `ant/src/index.ts:loadAgentMcpServers()`, alongside the native
gated socket. So image-gen, video, and other capability servers
_technically work today_: drop a `mcpServers` block in settings,
restart, the agent gets the tools.

The friction is multi-tenant operator workflow:

- Every operator hand-rolls JSON in settings and re-discovers what
  each plugin needs (env vars, paths, command line).
- Per-tool grants are missing — arizuko grants match server names,
  but a plugin typically exposes 4–6 tools (`generate`, `upscale`,
  `remove_bg`, `erase`) and operators want different tiers per tool.
- API keys belong per-folder (per-customer billing isolation), but
  there's no automatic routing of `groups/<folder>/SECRETS.toml`
  into the spawned plugin server's env.
- No catalog. No "which plugins are installed on this instance, who
  has them enabled" view.

## What

Three artifacts:

### 1. `plugin.toml` manifest

One file per plugin describing everything needed to install + run it:

```toml
name = "image-flux"
description = "Image generation via FLUX (replicate)"
version = "1.0.0"

[server]
command = "npx"
args = ["@arizuko/plugin-image-flux"]
# OR: container = "ghcr.io/arizuko/plugin-image-flux:1.0.0"

[secrets]
# routed from groups/<folder>/SECRETS.toml at spawn time
required = ["FLUX_API_KEY"]
optional = ["FLUX_MODEL"]

[tools]
# per-tool grants — operator can grant a subset
exposed = ["generate", "upscale", "remove_bg"]
default_tier = 2  # tiers ≥2 get the tools by default; ≥1 can opt in
```

### 2. `arizuko plugin` CLI

```
arizuko plugin add image-flux <instance>      install + register
arizuko plugin list <instance>                 what's installed
arizuko plugin enable image-flux <folder>      grant per folder
arizuko plugin disable image-flux <folder>     ungrant
```

`add` reads the manifest, validates required env, prompts for
secrets (which land in the _operator's_ `~/.arizuko/instance.toml`
or get routed at enable-time to the right `SECRETS.toml`), wires
the MCP server config into the instance's settings.

### 3. `/dash/plugins/` page

dashd lists installed plugins, shows a per-folder × per-tool enable
matrix, lets operator paste API keys into a form that writes the
correct `SECRETS.toml`. Browse + enable + key entry without ssh.

## Phase plan

- **Phase 1** (week): manifest format + `arizuko plugin add/list/enable`
  CLI + one reference plugin (`image-flux`). Folder-scoped secrets
  routing from `groups/<folder>/SECRETS.toml` into MCP server env.
  No dashboard UI yet.
- **Phase 2** (month): dashd `/plugins/` catalog + per-folder enable
  matrix + key entry form. Operator stops needing CLI for routine
  enable/disable.
- **Phase 3** (later): per-tool grants (today: per-server only); plugin
  marketplace concept (`arizuko plugin search`); signed manifests;
  version pinning.

## Acceptance (Phase 1)

- One reference plugin (`image-flux`) installs in <60s on a fresh
  instance via `arizuko plugin add`
- API key entered once; image gen works in any enabled folder
  without further per-folder configuration
- Per-folder enable/disable changes take effect on next agent spawn
  (no service restart needed)
- A new operator can install + enable + use the plugin without
  reading source

## Open

- **Server transport**: subprocess (npm/uvx) vs container (docker run).
  Subprocess is faster + simpler; container gives isolation + sandbox.
  Probably support both; default subprocess for trust-host plugins,
  container for third-party.
- **Tool naming collision**: two plugins both expose `generate` →
  prefix with plugin name (`image-flux.generate`)? Or namespace at
  manifest level? Telegram/Discord precedent: prefix.
- **Built-ins vs plugins**: `voice` (TTS) and `oracle` are today
  hard-wired into gateway/ant. Should they migrate to plugin form for
  uniformity? Probably yes long-term, but a plugin-as-built-in shim
  during transition.
- **Cost tracking**: per-folder spend caps for paid plugins (Replicate,
  OpenAI). Hooks into specs/8/4-rate-limits.md.

## References

- Pattern survey (vanilla Claude Code, 2026):
  - Pixa MCP — image + video + upscale + bg-remove
  - claude-image-gen (Gemini Imagen via Skills/MCP)
  - claude-code-generate-images-mcp (Azure DALL-E + Flux 1.1 Pro)
- arizuko-side wiring already exists: `ant/src/index.ts:58
loadAgentMcpServers()` reads settings.json `mcpServers`.
