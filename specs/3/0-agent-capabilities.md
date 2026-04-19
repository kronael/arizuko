---
status: shipped
---

# Agent Capabilities

Agent containers ship multimedia acquisition, compilers, and browser
automation. Enough for download/transcribe/compile/install loops.

## Container tooling

Baseline: node, bun, chromium, agent-browser, curl, git, claude-code.

Added: ffmpeg, yt-dlp, python3, jq, build-essential, go, rust (cargo), wget.

## Gateway to agent data flow

| Media   | Gateway processing  | What agent sees                |
| ------- | ------------------- | ------------------------------ |
| Voice   | whisper transcribes | `[voice/auto→en: text]`        |
| Video   | ffmpeg → whisper    | `[video audio: text]`          |
| Image   | passed through      | attachment path (vision reads) |
| PDF/doc | passed through      | attachment path (Read tool)    |

## Agent to whisper (direct)

`WHISPER_BASE_URL` env var passed to container. Agent transcribes audio
it downloads (yt-dlp, podcast episodes) without going through gateway IPC.

## Skill: acquire

`ant/skills/acquire/SKILL.md` — multimedia acquisition strategy:
prefer transcripts, screenshot key moments, metadata first, non-obvious
search services (DeepWiki, Marginalia).

## Open

- Container image size impact (go+rust+chromium+ffmpeg is heavy).
