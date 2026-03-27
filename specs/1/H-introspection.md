---
status: draft
---

<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Introspection

Agent discovers gateway features via a manifest; configures
behavior via well-known files.

## .gateway-caps (TOML, read-only)

Gateway writes `/home/node/.gateway-caps` on each container spawn.

```toml
[voice]
enabled = true
model = "large-v3"
languages = ["cs", "ru"]

[video]
enabled = true

[media]
enabled = true
max_size_mb = 50

[web]
enabled = true
host = "REDACTED.REDACTED"
```

Agent treats it as advisory; missing file = assume defaults.

## .whisper-language (agent-writable)

Path: `/home/node/.whisper-language`. One ISO-639-1 code per line.
Empty/absent = auto-detect only. Gateway adds one transcription
pass per code. Auto-detect pass always included.
