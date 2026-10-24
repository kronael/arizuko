---
name: typed-jids
description: >
  Typed JID format reference — `<platform>:<rest>` — and route
  predicates (`room=`, `chat_jid=`). USE for "what's a JID", "add a
  routing rule", "fix this chat_jid", route predicates, `chatJid`
  parameter, legacy bare-id rewrites (`telegram:1234`). NOT for chat
  history reads (use recall-messages).
user-invocable: true
---

# Typed JIDs

Wire form: `<platform>:<rest>` (RFC 3986 opaque-path URI). Each adapter
defines the segments of `<rest>`.

`core.JidPlatform(jid)` returns `<platform>`; `core.JidRoom(jid)`
returns `<rest>`. Glob match uses `path.Match` semantics — `*` does
not cross `/`.

## Type matrix

| Platform   | Shape                                                      |
| ---------- | ---------------------------------------------------------- |
| telegram   | DM `telegram:<bot_id>/user/<user_id>`, group `telegram:group/<id>`, sender `telegram:user/<user_id>` |
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
chat_jid=telegram:*/user/*   # all Telegram DMs
room=<bot_id>/user/<user_id> # one Telegram DM, platform implied by context
```

## Telegram DM addresses

The numeric bot id comes from `getMe`. Each bot has a separate DM chat
with the same user. Senders and pairing identities use
`telegram:user/<user_id>` across bot accounts.

ALWAYS use the chat JID from the inbound message for replies and chat tools.
Use `platform=telegram` to match all Telegram chats. The route glob
`telegram:*/*` matches groups but cannot match DMs with three path segments.

The operator runs `arizuko telegram <instance> rekey-dms` to migrate stored
DM chats. See `cmd/arizuko/README.md` for the offline procedure.
