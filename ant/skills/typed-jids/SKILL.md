---
name: typed-jids
description: >
  Typed JID format reference — `<platform>:<kind>/<id>` — and route
  predicates (`room=`, `chat_jid=`). USE for "what's a JID", "add a
  routing rule", "fix this chat_jid", route predicates, `chatJid`
  parameter, legacy bare-id rewrites (`telegram:1234`). NOT for chat
  history reads (use recall-messages).
user-invocable: true
---

# Typed JIDs

Wire form: `<platform>:<rest>` (RFC 3986 opaque-path URI). The first
segment of `<rest>` is the **kind discriminator**; per-kind structure
is the adapter's contract.

`core.JidPlatform(jid)` returns `<platform>`; `core.JidRoom(jid)`
returns `<rest>`. Glob match uses `path.Match` semantics — `*` does
not cross `/`.

## Type matrix

| Platform   | Shape                                                      |
| ---------- | ---------------------------------------------------------- |
| telegram   | `telegram:user/<id>`, `telegram:group/<id>`                |
| discord    | `discord:<guild>/<channel>`, `discord:dm/<channel>`, `discord:user/<id>` |
| whatsapp   | `whatsapp:<id>@<server>` (unchanged — no kind segment)     |
| email      | `email:thread/<id>`, `email:address/<addr>`                |
| slack      | `slack:<workspace>/channel/<id>`, `slack:<workspace>/dm/<id>`, `slack:<workspace>/group/<id>` |
| web        | `web:<folder>` (folder-keyed identity layer; no kind segment) |

## Route predicates

Routing rules are `key=glob` pairs joined with whitespace. Two predicates
target a JID:

- `room=<rest>` — match against `JidRoom(jid)` (the part after the
  scheme). Used by the gateway when it already knows the platform.
- `chat_jid=<full>` — match against the full canonical string. Used
  when the rule needs to discriminate by platform too.

Other keys: `platform=<scheme>`, `sender=<jid>`, `verb=<send|reply|…>`.

```text
chat_jid=telegram:group/*    # all telegram groups
chat_jid=discord:dm/*        # all discord DMs (any guild)
chat_jid=*:user/*            # any DM-style chat across platforms
room=user/<id>               # one user, platform implied by context
```

## Migration: legacy → typed

Legacy routes used bare ids; the store migration `0038-typed-jids.sql`
rewrites the common prefix-only forms automatically:

```text
# Before                          # After
chat_jid=telegram:*       →       chat_jid=telegram:*/*
chat_jid=discord:*        →       chat_jid=discord:*/*
```

Operator-edited globs that referenced legacy specifics (the telegram
sign-bit DM/group split) need manual review — the migration only catches the prefix-only case.

## Why the format changed

One URL = one resource. Legacy collided different kinds on the same
prefix: `telegram:1234` was either a DM or a group disambiguated by
sign bit;
`discord:<channel>` lost guild context. Typed JIDs encode the
discriminator structurally so glob rules don't need platform-specific
guesswork.

Code: `core/jid.go`. Migration: `ant/skills/self/migrations/088-typed-jids.md`.
