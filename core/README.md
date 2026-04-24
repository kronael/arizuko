# core

Types, config loader, `Channel` interface.

## Purpose

Zero-dependency package defining the shared vocabulary: `Config`,
`Message`, `Group`, `GroupConfig`, `Route`, `Task`, and the `Channel`
interface that adapters satisfy. Every daemon imports `core`; `core`
imports nothing else arizuko-internal.

## Public API

- `LoadConfig() (*Config, error)` — reads `.env` from cwd + env vars
- `LoadConfigFrom(dir string)` — explicit dir
- `Config` — all tunables and flags (see CLAUDE.md for the full list)
- `Message`, `Group`, `GroupConfig`, `Mount`, `Route`, `Task`, `SessionRecord`
- `Channel` — `Connect`, `Send`, `SendFile`, `Owns`, `Typing`, `Disconnect`
- `HistoryFetcher`, `Socializer` — optional extensions
- `JidRoom(jid)`, `JidPlatform(jid)` — JID parsing
- `GenSlinkToken()`, `MsgID(prefix)`, `SanitizeInstance(name)`

## Dependencies

None (stdlib only).

## Files

- `config.go` — env parsing, defaults
- `types.go` — shared types, `Channel` interface

## Related docs

- `ARCHITECTURE.md` (Key Types)
