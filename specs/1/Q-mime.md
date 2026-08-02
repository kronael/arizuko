---
status: shipped
---

# Media enrichment

Inbound attachments are downloaded, transcribed, and folded into the
message before the turn runs. Code: `routd/enrich.go`
(`Loop.enrichAttachments`).

## Shape as shipped

One serial pass over `msg.Attachments`, not the pluggable parallel
`Enricher` pipeline with ordered `ContextAnnotation` values this spec
originally proposed. Voice and video are the only two transforms, both
Whisper; a registry and an ordering field would have been machinery for
a list of two. Add the abstraction when a third transform arrives with a
genuinely different ordering need.

Enrichment **rewrites `msg.Content`** with `<attachment>` blocks and
persists the rewrite via `EnrichMessage`, so a later turn reading the
message as observed context sees the transcript too. Annotating only the
in-flight prompt would have made the same message read differently
depending on which turn looked at it.

Failures log WARN and skip that one attachment; the turn proceeds. An
attachment with neither a URL nor inline data is logged loudly rather
than dropped silently — that shape means the adapter built the
attachment without a reachable file ref (teled with `LISTEN_URL` unset),
which is a misconfiguration worth seeing.

## Prompt shape

```xml
<attachment index="0" type="voice" path="/home/node/media/...">
  <transcript>...</transcript>
</attachment>

hey check this out
```

## Download auth

The adapter's `/files` endpoint is gated by `chanlib.Auth`, so routd
presents its `service:routd` ES256 bearer on the download. Unset
`AUTHD_URL` (local dev only) means the fetch goes out unauthenticated.

## Layout and language selection

Files land in `groups/<folder>/media/<YYYYMMDD>/`. Per-group
transcription languages come from the agent-writable
`.whisper-language` file — see
[`H-introspection.md`](H-introspection.md).

Config: `MEDIA_ENABLED`, `MEDIA_MAX_FILE_BYTES`,
`VOICE_TRANSCRIPTION_ENABLED`, `VIDEO_TRANSCRIPTION_ENABLED`
(needs ffmpeg), `WHISPER_BASE_URL`, `WHISPER_MODEL`.
