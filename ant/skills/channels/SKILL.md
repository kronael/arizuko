---
name: channels
description: >
  Per-platform `send_file` routing matrix — telegram/discord/whatsapp/
  email/etc. USE before sending media, "how do I send
  this image on X", "why did my attachment land as a doc", "what
  platforms support video", capability discovery for `send_file`. NOT
  for voice/PTT (use voice skill / `send_voice`).
user-invocable: true
---

# Channels

## File dispatch matrix

| Adapter   | Routing                                                                   |
| --------- | ------------------------------------------------------------------------- |
| telegram  | extension-based: `.png/.jpg/.jpeg/.webp` → photo; `.mp4/.mov/.webm` → video; `.gif` → animation; `.mp3/.m4a/.flac/.ogg` → audio (music — voice notes use `send_voice`); other → document |
| discord   | any extension; mime set per extension (`image/png`, `video/mp4`, `audio/mpeg`, …) → rich-media bubble. PDFs and unknown types attach as plain files |
| whatsapp  | platform-native dispatch (Baileys infers type)                            |
| bluesky   | image-only — `.jpg/.jpeg/.png/.webp/.gif` upload as `app.bsky.embed.images`; non-image returns "Bluesky's PDS embed surface only accepts image blobs" → host elsewhere, post the URL via `send` |
| email     | MIME multipart attachment                                                 |

## Voice vs file

`send_voice` is its own pipeline (TTS → adapter PTT). Telegram's `.ogg`
audio routing in `send_file` produces a *music* attachment, not a voice
note. Use `send_voice` when you want PTT semantics.

## Capability discovery

When unsure whether a platform supports a verb, just call the tool:
adapters return `chanlib.ErrUnsupported` (HTTP 501) for verbs they
don't implement. The MCP layer maps this to a structured response —
catch it and degrade gracefully (`send_file` fail → host + `send` URL).

Some adapters return `ErrUnsupported` for `like` — do not
retry. A Slack `like` that errors usually means the bot token lacks the
`reactions:write` scope: log it with `/issues`, do NOT loop-retry, do NOT
tell the user the channel is broken.

## Routing patterns

Channel scope is encoded in the JID — see `/typed-jids` for the format.
Routes can target one platform (`chat_jid=discord:*/*`), one kind
(`chat_jid=telegram:group/*`), or one chat (`chat_jid=telegram:user/<id>`).
