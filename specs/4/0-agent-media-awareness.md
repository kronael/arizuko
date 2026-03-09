# Agent Media Awareness

**Status**: open

Agents don't know how to handle media attachments. When users send
documents, images, or voice messages, agents see placeholder text
but don't know how to access the actual content.

## Problem

Observed in journalctl:

```
Agent output: Nemůžu ho přímo zobrazit - mohl bys ho popsat
Agent output: Stále vidím jen `[document]` - nemůžu zobrazit obsah
```

The agent receives:

```
[media attached: /workspace/group/media/20260307/msgId/0.pdf (application/pdf)]
```

But doesn't realize it can `Read` that path. Claude can read PDFs,
images, and documents natively — the agent just needs to be told.

## Root cause

CLAUDE.md lacks media handling instructions. The nanoclaw refs have
clear "What You Can Do" sections that explain capabilities. Kanipi
agents don't have this.

## Solution

Add media awareness to the agent CLAUDE.md template.

### Media attachment format

When users send media, the gateway:

1. Downloads the file to `media/YYYYMMDD/<msgId>/`
2. Appends to message: `[media attached: <path> (<mime-type>)]`

For voice/audio (when transcription enabled):

1. Whisper transcribes the audio
2. Appends: `[voice/auto→cs: transcribed text here]`

### What agents should know

Add to `container/CLAUDE.md`:

```markdown
## Media Handling

When users send files, you'll see:
`[media attached: /workspace/group/media/.../file.pdf (application/pdf)]`

**Read it with the Read tool** — Claude reads PDFs, images, and
documents natively. The path is absolute and accessible.

| Media type   | What you see                       | What to do                   |
| ------------ | ---------------------------------- | ---------------------------- |
| PDF/document | `[media attached: path]`           | `Read(path)`                 |
| Image        | `[media attached: path]`           | `Read(path)` — Claude vision |
| Voice        | `[voice/auto→lang: text]`          | Text already transcribed     |
| Video        | `[video: path] [transcript: text]` | Transcript injected          |

NEVER say "I can't read this" or "I can't display this" without
first trying to Read the attached file path.
```

### Howto skill update

Update `container/skills/howto/SKILL.md` Level 1 to mention:

- "Send me documents — I'll read and analyze them"
- "Send images — I can see and describe them"
- "Send voice messages — I'll transcribe them"

### Agent self-check

When an agent sees `[media attached: ...]` or `[document]`, it should:

1. Extract the path from the attachment line
2. Use Read tool to access the file
3. Process the content
4. Respond based on file contents

If no path is visible (just `[document]` with no path), the gateway
media processing may be disabled. Check `MEDIA_ENABLED=true` in .env.

## Implementation

1. Update `container/CLAUDE.md` with media handling section
2. Update `container/skills/howto/SKILL.md` level 1
3. Sync to existing agents via migration

### Migration

Add to `container/skills/self/migrations/017-media-awareness.md`:

```markdown
# Migration 017: Media Awareness

Added media handling instructions to CLAUDE.md.

## Changes

Agents now know to:

- Read PDF/document attachments using Read tool
- Read image attachments (Claude vision)
- Understand voice transcription format

## Action required

Read the updated CLAUDE.md media handling section.
When you see `[media attached: path]`, use `Read(path)`.
```

## Open

- Should agent-browser PDF generation be mentioned here too?
- Video handling: ffmpeg in container vs gateway-only?
- Large files: add size warning? (Claude has token limits)
