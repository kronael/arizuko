---
status: draft
---

# 6-voice-synthesis — TTS output from agent

**Status**: planned (open questions)

Voice transcription (Whisper, MEDIA_ENABLED) already handles input.
This spec covers the output direction: agent text → spoken voice message
delivered back to the user via the channel.

---

## Motivation

Channels that support audio (Telegram, WhatsApp, Discord) can receive
voice notes or audio files. Users who send voice messages expect voice
replies. An agent that only replies in text breaks the conversational
medium.

---

## What it looks like

1. User sends a voice note on Telegram
2. Whisper transcribes it → text in agent prompt
3. Agent responds with text
4. Gateway detects "this conversation started as voice" or "agent asked for voice"
5. TTS converts agent text to audio
6. Adapter sends audio file back to the user (Telegram voice note / WhatsApp audio)

---

## Components

### TTS service

External process (like Whisper for input). Accepts text, returns audio file.

Options:

| Service          | Type  | Quality | Cost        | Self-hosted |
| ---------------- | ----- | ------- | ----------- | ----------- |
| OpenAI TTS       | API   | high    | paid        | no          |
| ElevenLabs       | API   | highest | paid/limits | no          |
| Kokoro-82M       | local | good    | free        | yes         |
| Piper            | local | ok      | free        | yes         |
| Coqui TTS        | local | ok      | free        | yes         |
| edge-tts (Azure) | API   | high    | free quota  | no          |

For self-hosted: Kokoro is the current best quality/size tradeoff.
For managed: OpenAI TTS (`tts-1`, `tts-1-hd`) integrates trivially.

### ttsd daemon

Thin HTTP wrapper around TTS backend, same pattern as Whisper:

```
POST /synthesize
Content-Type: application/json
{"text": "...", "voice": "alloy", "format": "ogg_opus"}

→ audio/ogg binary
```

`VOICE_SYNTHESIS_BASE_URL` env var, similar to `WHISPER_BASE_URL`.

### Gateway integration

Router or gateway converts agent text reply to audio before sending.
Trigger options (open question below):

- Per-route flag: `voice_reply: true` in routes table
- Per-session: if input was voice → reply in voice
- Agent-controlled: `send_voice` MCP tool

### Adapter support

| Adapter | Voice output mechanism             | Format     |
| ------- | ---------------------------------- | ---------- |
| teled   | `sendVoice` (Telegram API)         | OGG Opus   |
| whapd   | `sendAudio` (WhatsApp)             | OGG Opus   |
| discd   | `sendFile` or voice channel stream | MP3 or OGG |
| emaid   | MIME attachment                    | MP3        |

Telegram voice notes require OGG/Opus specifically. MP3 works as audio
file (not voice note). Whisper outputs to the right format for Telegram.

---

## Caching

TTS is expensive (latency + cost). Cache synthesized audio by content hash:

```
groups/<folder>/media/tts/<sha256>.ogg
```

Same text + same voice = serve from cache. TTL: indefinite (content-addressed).

---

## Design: separate `send_voice` MCP tool (resolved)

Research across Telegram, Discord, WhatsApp, Email APIs confirms the
**separate method per capability** pattern — each platform uses distinct
calls for text, voice, and files. Arizuko's `Send()` / `SendFile()`
split already matches this model. Adding `send_voice` as a third MCP
tool (like `send_file`) is the natural fit.

**Rejected alternative**: "unified message with optional voice field"
(WhatsApp/email style). This would require changing `Channel.Send`,
all adapter signatures, and `OutboundEntry` — too much churn for no gain.
Telegram and Discord don't model voice this way either.

**Consequence**: no changes to core types, Channel interface, or adapters.
Voice is just another file (OGG audio) delivered through existing `SendFile`
infrastructure. The gateway synthesizes and caches; adapters just receive a file.

### Implementation sketch (~150 LOC)

```go
// ipc/ipc.go — add send_voice tool (like send_file)
srv.AddTool("send_voice",
    mcp.WithString("chatJid", mcp.Required()),
    mcp.WithString("text", mcp.Required()),
    mcp.WithString("voice"),  // optional: overrides TTS_VOICE
)

// gateway/gateway.go — synthesize + send
func (g *Gateway) sendVoice(jid, text, voice string) error {
    audioPath, err := g.synthesize(text, voice)  // calls ttsd, caches
    if err != nil { return err }
    return g.sendDocument(jid, audioPath, "voice.ogg")
}

func (g *Gateway) synthesize(text, voice string) (string, error) {
    hash := sha256sum(text + voice)
    cachePath := filepath.Join(g.cfg.DataDir, "tts", hash+".ogg")
    if exists(cachePath) { return cachePath, nil }
    audio, err := callTTSD(g.cfg.TTSBaseURL, text, voice)
    os.WriteFile(cachePath, audio, 0644)
    return cachePath, nil
}
```

### Adapter changes

**None required.** Audio is sent via existing `sendDocument` → `SendFile`
path. Telegram's `sendFile` already dispatches `.ogg` as audio (see
`teled/bot.go:219`). Discord sends as file attachment. WhatsApp `sendAudio`
can be triggered if whapd gets a `/send-voice` endpoint (optional upgrade).

## Open questions

### Q1: Voice selection

Single global voice (configurable) vs per-group/per-persona voice?

- **Single voice**: simplest. `TTS_VOICE=alloy` env var. Implement first.
- Per-persona: SOUL.md includes `voice: nova` frontmatter. Agent reads it
  and passes to `send_voice(text, voice="nova")`. Elegant — no schema change.
- Per-group: routes table `voice_id` column. Most complex.

### Q2: Long messages

TTS APIs have limits (OpenAI: 4096 chars). Agent responses can be long.

- Agent is responsible for keeping voice replies short (CLAUDE.md instruction)
- Fallback: if text > N chars, gateway sends as text instead

### Q3: WhatsApp voice note vs audio file

WhatsApp distinguishes voice notes (`{ audio, ptt: true }`) from music
(`{ audio, ptt: false }`). Should `/send-voice` endpoint in whapd set
`ptt: true`? Probably yes for conversational voice replies.

### Q4: TTS cache scope

Cache key = hash(text + voice). Where does the cache live?

- Option A: `<data_dir>/tts/<hash>.ogg` — global cache, shared across groups
- Option B: `<group_dir>/media/tts/<hash>.ogg` — per-group cache

Option A is simpler and avoids duplicate synthesis. Option B follows the
per-group media pattern already used for whisper transcripts.

---

## Minimal implementation path

1. `ttsd` wrapper around Kokoro or OpenAI TTS
2. `send_voice(text)` MCP tool in ipc/ (like `send_file`)
3. Adapter support: teled `sendVoice`, whapd audio
4. CLAUDE.md instruction: "use send_voice when user sent a voice message"
5. Cache layer: content-hash in media/tts/

No schema changes. No per-route config. Agent decides when to use voice.
This is the smallest path to working voice replies.

---

## Compose integration

```bash
TTS_ENABLED=true          # enable ttsd
TTS_BASE_URL=http://ttsd:8098
TTS_VOICE=alloy           # or kokoro voice name
TTS_MODEL=tts-1           # openai: tts-1 | tts-1-hd
```

`ttsd` added to compose when `TTS_ENABLED=true`, similar to `davd`.
Profile: available in `standard` and above (not minimal/web).
