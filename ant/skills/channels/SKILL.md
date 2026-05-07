---
name: channels
description: Adapter-aware file dispatch reference — how `send_file` resolves to platform-native attachments per channel.
when_to_use: >
  Consult before sending media, or when an attachment lands as a generic
  document instead of inline.
---

# Channels

## File dispatch matrix

| Adapter   | Routing                                                                   |
| --------- | ------------------------------------------------------------------------- |
| telegram  | extension-based: `.png/.jpg/.jpeg/.webp` → photo; `.mp4/.mov/.webm` → video; `.gif` → animation; `.mp3/.m4a/.flac/.ogg` → audio (music — voice notes use `send_voice`); other → document |
| discord   | any extension; mime set per extension (`image/png`, `video/mp4`, `audio/mpeg`, …) → rich-media bubble. PDFs and unknown types attach as plain files |
| whatsapp  | platform-native dispatch (Baileys infers type)                            |
| twitter   | photo / video media types via platform lib                                |
| bluesky   | image-only — `.jpg/.jpeg/.png/.webp/.gif` upload as `app.bsky.embed.images`; non-image returns "Bluesky's PDS embed surface only accepts image blobs" → host elsewhere, post the URL via `send` |
| email     | MIME multipart attachment                                                 |
| mastodon, reddit, linkedin | no file surface (`chanlib.NoFileSender`) — `send_file` errors. Host the file on `/pub/` and link via `send` |

## Voice vs file

`send_voice` is its own pipeline (TTS → adapter PTT). Telegram's `.ogg`
audio routing in `send_file` produces a *music* attachment, not a voice
note. Use `send_voice` when you want PTT semantics.

## Capability discovery

When unsure whether a platform supports a verb, just call the tool:
adapters return `chanlib.ErrUnsupported` (HTTP 501) for verbs they
don't implement. The MCP layer maps this to a structured response —
catch it and degrade gracefully (`send_file` fail → host + `send` URL).

## Routing patterns

Channel scope is encoded in the JID — see `/typed-jids` for the format.
Routes can target one platform (`chat_jid=discord:*/*`), one kind
(`chat_jid=telegram:group/*`), or one chat (`chat_jid=telegram:user/<id>`).
