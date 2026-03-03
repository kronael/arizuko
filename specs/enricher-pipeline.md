# Enricher Pipeline

A composable, pre-dispatch enrichment stage that runs on every inbound message
before the container is spawned. Enrichers may read message content, resolve
attachments, fetch external data, or annotate context. All matching enrichers
run; the message is not dispatched until all complete.

---

## Model

```
InboundMessage                    (raw, from channel)
      │
      ▼
  [Enricher Pipeline]
      │   ┌─────────────────────┐
      │   │ matches(msg)?       │  — predicate, called for every enricher
      │   │ enrich(msg, ctx)    │  — all matching enrichers run in parallel
      │   └─────────────────────┘
      │
      ▼
EnrichedMessage                   (prompt annotated, attachments resolved)
      │
      ▼
  ContainerInput (stdin JSON)
      │
      ▼
  Container (Claude agent)
```

Enrichers are not limited to attachments. Any enricher may inspect or
transform any property of the message — text, sender, group, timestamp,
channel, or attachments.

---

## Interfaces

```typescript
// The normalized inbound message passed to every enricher.
// Populated by channel adapters before the pipeline runs.
interface InboundMessage {
  // Core
  id: string;
  chatJid: string;
  sender: string;
  senderName: string;
  text: string; // raw text content
  timestamp: string;
  channel: 'telegram' | 'whatsapp' | 'discord';

  // Group context
  groupFolder: string;
  isMain: boolean;

  // Optional raw attachment references (not yet downloaded)
  attachments?: RawAttachment[];

  // Optional channel-native extras
  replyToText?: string; // quoted message text, if any
  replyToSender?: string;
  threadId?: string; // discord thread / telegram topic
  mediaGroupId?: string; // telegram album id
}

interface RawAttachment {
  type: 'image' | 'voice' | 'audio' | 'video' | 'document' | 'sticker';
  mimeType?: string;
  filename?: string;
  sizeBytes?: number;
  durationSeconds?: number;
  source: TelegramSource | WhatsAppSource | DiscordSource;
}

type TelegramSource = { kind: 'telegram'; fileId: string };
type WhatsAppSource = { kind: 'whatsapp'; message: WAMessage };
type DiscordSource = { kind: 'discord'; url: string };

// An attachment after an enricher has processed it.
interface EnrichedAttachment {
  type: RawAttachment['type'];
  localPath: string; // relative to groupDir (mounted at /workspace/media/)
  filename: string;
  mimeType?: string;
  sizeBytes: number;
  // Pre-computed enrichments
  transcription?: string; // voice, audio, video audio track
  annotation?: string; // free-form, injected into [attachments] block
}

// Shared context available to all enrichers.
interface EnrichContext {
  groupDir: string; // absolute host path to groups/<folder>/
  mediaDir: string; // groupDir/media/ — write downloads here
  config: EnricherConfig;
  channel: Channel; // for reply() if needed (e.g. "transcribing...")
}

// The enriched message — what the pipeline produces.
// Enrichers accumulate into this; later enrichers see prior results.
interface EnrichedMessage extends InboundMessage {
  enrichedAttachments?: EnrichedAttachment[];
  contextAnnotations: ContextAnnotation[]; // ordered, injected before prompt
}

// A named block injected above the user's message in ContainerInput.prompt.
interface ContextAnnotation {
  label: string; // e.g. "voice transcription", "reply context"
  content: string;
  order: number; // lower = closer to top of annotations
}

// The enricher contract.
interface MessageEnricher {
  name: string;
  // Return true if this enricher should run for this message.
  // Called synchronously; must be cheap (no I/O).
  matches(msg: InboundMessage): boolean;
  // Mutate or extend msg. Returns the (possibly updated) message.
  // Called in parallel with all other matching enrichers.
  enrich(msg: EnrichedMessage, ctx: EnrichContext): Promise<EnrichedMessage>;
}
```

---

## Pipeline execution

```typescript
async function runEnrichers(
  msg: InboundMessage,
  enrichers: MessageEnricher[],
  ctx: EnrichContext,
): Promise<EnrichedMessage> {
  const enriched: EnrichedMessage = {
    ...msg,
    contextAnnotations: [],
  };

  const matching = enrichers.filter((e) => e.matches(msg));
  if (matching.length === 0) return enriched;

  // All matching enrichers run in parallel.
  // Each receives a snapshot of the current state; results are merged.
  const results = await Promise.allSettled(
    matching.map((e) => e.enrich({ ...enriched }, ctx)),
  );

  for (const r of results) {
    if (r.status === 'fulfilled') merge(enriched, r.value);
    else logger.warn('enricher.failed', { error: r.reason });
  }

  enriched.contextAnnotations.sort((a, b) => a.order - b.order);
  return enriched;
}
```

Enrichers are **fire-and-collect**: all run, failures are logged and skipped,
the message is dispatched with whatever enrichments succeeded. A failed
enricher never blocks dispatch.

---

## Built-in enrichers

### 1. VoiceTranscriber

```
matches:  attachment.type === 'voice' || attachment.type === 'audio'
          || mimeType.startsWith('audio/')
enriches: downloads bytes → saves to media/voice/<id>.ogg
          → transcribes via local Whisper server (OpenAI-compatible API)
          → sets enrichedAttachment.transcription
          → adds ContextAnnotation { label: 'voice', content: transcript, order: 10 }
config:   VOICE_TRANSCRIPTION_ENABLED, WHISPER_BASE_URL, WHISPER_MODEL
```

### 2. VideoAudioTranscriber

```
matches:  attachment.type === 'video' || mimeType.startsWith('video/')
enriches: downloads → saves to media/video/<id>.mp4
          → extracts audio track (ffmpeg: -vn -acodec copy)
          → transcribes extracted audio via Whisper
          → sets enrichedAttachment.transcription
          → adds ContextAnnotation { label: 'video audio', order: 11 }
config:   VIDEO_TRANSCRIPTION_ENABLED, WHISPER_BASE_URL, WHISPER_MODEL
requires: ffmpeg in PATH
```

### 3. GenericFileSaver

```
matches:  any attachment not matched by a more specific enricher
          (images, stickers, documents, anything else)
enriches: downloads → saves to media/files/<filename>
          → sets localPath, sizeBytes, mimeType
          → adds ContextAnnotation { label: 'file', order: 30 }
config:   MEDIA_ENABLED
```

---

## Prompt assembly

After the pipeline, `buildPrompt(enriched)` assembles `ContainerInput.prompt`.
Each enricher that produces output injects an XML fragment into the prompt.
The user's raw text follows after all enricher blocks.

```xml
<attachment index="0" type="voice" path="/workspace/media/20260303/abc123-0.ogg">
  <transcript>i think the liability clause needs to be revised, especially section 4b</transcript>
</attachment>

<attachment index="1" type="video" path="/workspace/media/20260303/abc123-1.mp4">
  <transcript>here is the screen recording showing the bug reproduction steps</transcript>
</attachment>

<attachment index="2" type="image" path="/workspace/media/20260303/abc123-2.jpg">
  <description>file saved, no further enrichment</description>
</attachment>

hey check this out and let me know what you think
```

Enrichers that produce no text (GenericFileSaver) still emit a minimal
`<attachment>` tag so the agent knows the file exists and where to find it.
Transcript and description content is also written to the `-whisper.txt` /
`-<enricher>.txt` sidecar for persistence.

---

## Config

```bash
# .env
MEDIA_ENABLED=true
MEDIA_MAX_FILE_BYTES=20971520        # 20 MB hard limit per file

VOICE_TRANSCRIPTION_ENABLED=true
WHISPER_BASE_URL=http://localhost:8080   # local whisper.cpp or faster-whisper-server
WHISPER_MODEL=turbo                  # base / small / medium / large-v3 / turbo

VIDEO_TRANSCRIPTION_ENABLED=false    # requires ffmpeg
```

---

## File layout

```
src/
  enricher-pipeline.ts     — InboundMessage, EnrichedMessage, runEnrichers()
  enrichers/
    voice.ts               — VoiceTranscriber
    video.ts               — VideoAudioTranscriber
    generic.ts             — GenericFileSaver (images, docs, everything else)
  channels/
    telegram.ts            — extractAttachments(), populate InboundMessage
    whatsapp.ts            — extractAttachments()
    discord.ts             — extractAttachments()
  container-runner.ts      — mount /workspace/media, receive EnrichedMessage
```

---

## Container mount

`/workspace/media` is added as a read-write mount per group:

```
groups/<folder>/media/  →  /workspace/media/
  <YYYYMMDD>/
    <msg-id>-<idx>.<ext>            — raw download
    <msg-id>-<idx>-<enricher>.txt   — enricher sidecar (one per enricher)
```

Date-bucketed to keep daily volume manageable. Within each day's dir, flat.
Naming encodes message, attachment index, and enricher — no coordination needed.

Examples:

```
20260303/
  abc123-0.ogg
  abc123-0-whisper.txt
  abc123-1.jpg
  def456-0.mp4
  def456-0-whisper.txt
```

`localPath` in `EnrichedAttachment` points to the raw file
(`media/<YYYYMMDD>/<msg-id>-<idx>.<ext>`), relative to `groupDir`.
Agent path: `/workspace/media/<YYYYMMDD>/`.

---

## Extension

Custom enrichers can be added by:

1. Implementing `MessageEnricher` in `src/enrichers/<name>.ts`
2. Adding config flag `<NAME>_ENABLED` to `config.ts`
3. Registering in `buildEnrichers()` in `enricher-pipeline.ts`

No changes to channels or container-runner required. The enricher declares
its own `matches()` predicate — it decides what it processes.

Future: enrichers loaded from `container/skills/enrichers/` (agent-defined
enrichers, seeded into gateway at startup). Same skill-seeding mechanism.
