# 089 — send_voice MCP tool + media-dispatch audit

Two related shipments:

## (a) `send_voice` is a new MCP tool

`send_voice(chatJid, text, voice?)` synthesizes `text` via the configured
TTS backend and delivers it as a platform-native voice message:

- **Telegram** → push-to-talk (sendVoice, NewVoice — distinct from
  the music-attachment NewAudio used by send_file's audio branch)
- **WhatsApp** → push-to-talk (`audio + ptt:true` via Baileys)
- **Discord** → audio attachment with `audio/ogg` ContentType (Discord
  renders an inline player; no native PTT)
- **Mastodon, Bluesky, Reddit, LinkedIn, Email, X**: not supported.
  Returns Unsupported with a hint pointing at plain `send`.

`voice` resolution: explicit arg > `voice:` field in `~/SOUL.md`
frontmatter > `TTS_VOICE` env default. Text capped at 5000 chars.

When to call: the user's last message was voice, or the persona is
voice-first. Don't follow `send_voice` with a `send` — the voice
message stands alone (no caption text).

When NOT to call: music or file delivery (use `send_file`); plain
chat (use `send`); platforms that return Unsupported (fall back to
`send`).

## (b) SendFile media-type dispatch audited

Every adapter's `send_file` now routes to the right platform-native
API by extension:

- **Telegram**: `.png/.jpg/.jpeg/.webp` → photo; `.mp4/.mov/.webm`
  → video; `.gif` → animation; `.mp3/.m4a/.flac/.ogg` → audio
  (music); other → document.
- **Discord**: ContentType is set by extension so the rich-media
  bubble fires (image preview, video player, audio player) even
  when upstream gave an ambiguous filename.
- **Bluesky**: image extensions go to the embed upload; non-images
  return Unsupported (PDS embed surface only accepts image blobs —
  link in post text is the workaround).
- **WhatsApp**: already MIME-dispatches via Baileys.
- **X (Twitter)**: already routes via the lib's photo/video media types.
- **Mastodon, Reddit, LinkedIn, Email**: still `NoFileSender`. These
  platforms have native media APIs but the wiring isn't done yet —
  not regressed, tracked in `bugs.md`.

Operator impact: none (default behavior). Set `TTS_ENABLED=true` and
point `TTS_BASE_URL` at a Kokoro-FastAPI (bundled `arizuko-ttsd`
default) or any OpenAI-compatible TTS server to enable voice output.
