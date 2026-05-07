---
name: typed-jids
description: Reference for the typed JID format — `<platform>:<kind>/<id>` — and route predicates (`room=`, `chat_jid=`).
when_to_use: >
  Consult when writing routing rules, calling MCP tools that take a `chatJid`,
  or rewriting legacy bare-id forms (`telegram:1234`) to the current shape.
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
| mastodon   | `mastodon:account/<id>`, `mastodon:status/<id>`            |
| reddit     | `reddit:user/<name>`, `reddit:subreddit/<name>`, `reddit:comment/<id>`, `reddit:submission/<id>` |
| email      | `email:thread/<id>`, `email:address/<addr>`                |
| bluesky    | `bluesky:user/<percent-encoded-did>`, `bluesky:post/<at_uri>` |
| linkedin   | `linkedin:user/<urn>`, `linkedin:post/<urn>`               |
| twitter    | `twitter:user/<id>`, `twitter:tweet/<id>`, `twitter:dm/<id>` |
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
chat_jid=reddit:t1_*      →       chat_jid=reddit:comment/*    (manual)
```

Operator-edited globs that referenced legacy specifics (the
`reddit:t1_/t2_/t3_` prefix hack, the telegram sign-bit DM/group split)
need manual review — the migration only catches the prefix-only case.

## Why the format changed

One URL = one resource. Legacy collided different kinds on the same
prefix: `telegram:1234` was either a DM or a group disambiguated by
sign bit; `reddit:t1_xyz` and `reddit:t2_<user>` shared `reddit:`;
`discord:<channel>` lost guild context. Typed JIDs encode the
discriminator structurally so glob rules don't need platform-specific
guesswork.

Code: `core/jid.go`. Migration: `ant/skills/self/migrations/088-typed-jids.md`.
