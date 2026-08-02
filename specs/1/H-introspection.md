---
status: shipped
---

# Introspection

Agent discovers gateway features via a manifest; configures
behavior via well-known files. Written by `container/`.

## .gateway-caps (TOML, read-only)

Gateway writes `/home/node/.gateway-caps` on each container spawn.

Sections: `[voice]` (`enabled`, `model`), `[video]` (`enabled`),
`[media]` (`enabled`, `max_size_mb`), `[web]` (`enabled`, `host` when
`WEB_HOST` is set). Emitted by `writeGatewayCaps` in
`container/runner.go` — read it there for the exact keys.

Agent treats it as advisory; missing file = assume defaults. It is
declarative capability discovery rather than trial-and-error: an agent
that knows voice is off does not waste a turn attempting transcription.

## .whisper-language (agent-writable)

Path: `/home/node/.whisper-language`. One ISO-639-1 code per line.
Empty/absent = auto-detect only. Gateway adds one transcription
pass per code. Auto-detect pass always included.
