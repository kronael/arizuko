# Migration 088 — typed JIDs

## What changed

Routing match patterns now use kind-discriminated JIDs. Each platform
puts a kind in the first path segment:

| platform   | shape                                  |
| ---------- | -------------------------------------- |
| telegram   | `telegram:user/<id>` `telegram:group/<id>` |
| discord    | `discord:<guild>/<channel>` `discord:dm/<channel>` `discord:user/<id>` |
| whatsapp   | `whatsapp:<id>@<server>` (unchanged)   |
| mastodon   | `mastodon:account/<id>` `mastodon:status/<id>` |
| reddit     | `reddit:user/<name>` `reddit:subreddit/<name>` `reddit:comment/<id>` `reddit:submission/<id>` |
| email      | `email:thread/<id>` `email:address/<addr>` |
| bluesky    | `bluesky:user/<percent-encoded-did>` `bluesky:post/<at_uri>` |
| linkedin   | `linkedin:user/<urn>` `linkedin:post/<urn>` |
| twitter    | `twitter:user/<id>` `twitter:tweet/<id>` `twitter:dm/<id>` (unchanged) |
| web        | `web:<folder>` (unchanged — folder-keyed identity layer) |

## Why

One URL = one resource. The legacy format collided different kinds on
the same prefix:

- `telegram:1234` was either a DM or a group, disambiguated by sign bit
- `reddit:t1_xyz` and `reddit:t2_<user>` shared the `reddit:` prefix
- `discord:<channel>` lost the guild context

Routing rules previously had to encode platform-specific guesswork
(sign-bit hack on telegram, `t1_/t2_/t3_` checks on reddit). Now
patterns like `telegram:group/*` or `discord:dm/*` match the kind
directly under uniform `path.Match` glob semantics.

## What you need to do

Rewrite stale routing rules that reference the legacy form:

```text
# Before
chat_jid=telegram:*       chat_jid=reddit:t1_*    chat_jid=discord:*

# After
chat_jid=telegram:*/*     chat_jid=reddit:comment/*    chat_jid=discord:*/*
```

The store migration `0038-typed-jids.sql` rewrites stored routing match
patterns automatically (`chat_jid=telegram:*` → `chat_jid=telegram:*/*`)
for the common prefix-only form. Any operator-edited globs that
referenced the legacy specifics (e.g. `chat_jid=reddit:t1_*`) need
manual review.

Discord legacy rows have no guild_id stored; the migration tags those
as `discord:_/<channel>` (placeholder). New inbound from the discd
adapter emits the real `discord:<guild>/<channel>` going forward.
