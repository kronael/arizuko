# Plugins (v2) — open

Enableable plugin system for kanipi instances, similar to Eliza's
character-level plugin config.

## Current state (v1)

All features compiled into the binary. Telegram is the only channel.
WhatsApp code ships but is disabled via TELEGRAM_ONLY=true.

## Goal

Per-instance plugin enablement via TOML config:

```toml
[plugins]
enabled = ["telegram", "voice-transcription", "web-search"]
```

## Plugin types

### Channels

- telegram (current, always-on)
- whatsapp (nanoclaw upstream, dormant)
- slack (nanoclaw /add-slack skill)
- gmail (nanoclaw /add-gmail skill)

### Features

- voice-transcription (whisper API for voice notes)
- agent-swarm (multi-bot telegram, /add-telegram-swarm)
- web-search (tool available to agent)
- browser (playwright in container)

## Implementation approach

Each plugin = directory under `plugins/` with:

- `index.ts` exporting `register(app: KanipiApp): void`
- Plugin receives app context (channels array, config, etc.)
- Channels implement the existing `Channel` interface
- Features register tools or middleware

Dynamic import based on TOML `plugins.enabled` list.
No plugin = no import = no dependency cost.

## Migration path

1. Extract telegram channel into `plugins/telegram/`
2. Add plugin loader to main()
3. Move remaining nanoclaw skills to plugin format
4. Per-instance TOML controls what loads
