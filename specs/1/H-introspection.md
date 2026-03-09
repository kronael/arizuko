# v1/introspection — Agent capability introspection and configuration

## Problem

The agent has no way to discover what gateway features are available or how
to configure them. Capabilities like voice transcription languages, idle
timeout, or media handling are opaque. Users ask "can you configure X" and
the agent guesses.

## Approach

Two separate concerns:

1. **Introspection** — agent reads a gateway-written manifest at startup
2. **Configuration** — agent writes well-known files that the gateway reads

These are already partially implemented (`.whisper-language` exists). This
spec formalises both.

## Gateway capabilities manifest

Gateway writes `/workspace/group/.gateway-caps` on each container spawn.
TOML format. Agent reads it to know what's enabled and configurable.

```toml
[voice]
enabled = true
model = "large-v3"
# languages currently configured for this group
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

File is read-only from the agent's perspective (gateway rewrites it each
spawn). Agent uses it to give accurate answers about capabilities.

## Agent-writable configuration files

Files the agent creates/edits in `/workspace/group/`:

| File                | Type        | Effect                                       |
| ------------------- | ----------- | -------------------------------------------- |
| `.whisper-language` | text, lines | ISO-639-1 codes; forced transcription passes |

### `.whisper-language`

One ISO-639-1 code per line. Empty file or absent = auto-detect only.
Gateway adds one transcription pass per code, labelled `[voice/cs: ...]`.
Auto-detect pass always included, labelled `[voice/auto→cs: ...]`.

```
cs
ru
```

## Self-skill documentation

`container/skills/self/SKILL.md` lists all configuration files under
"Group configuration files". When a new config file is added:

1. Add row to the table in `SKILL.md`
2. Add it to the `.gateway-caps` manifest schema
3. Bump `MIGRATION_VERSION` if existing sessions need updating

## Gateway-caps implementation notes

- Written in `container-runner.ts` before `runContainerAgent`, alongside
  the existing tasks/groups snapshots
- Derive from live config constants — no separate config read
- Agent treats it as advisory; missing file = assume defaults

## What is NOT in scope here

- Agent modifying gateway `.env` or systemd config — operator-only
- Cross-group capability differences — future work
- Dynamic capability changes without restart — future work
