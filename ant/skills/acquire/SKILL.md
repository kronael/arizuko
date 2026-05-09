---
name: acquire
description: Download video/audio (yt-dlp), transcribe (whisper), scrape web pages.
when_to_use: >
  Use when gathering source material for research or analysis — "download
  this video", "transcribe this", "scrape that page".
---

# Acquire

## Video (yt-dlp + ffmpeg)

```bash
# download video
yt-dlp -o '~/tmp/%(title)s.%(ext)s' '<url>'

# audio only (smaller, faster)
yt-dlp -x --audio-format mp3 -o '~/tmp/%(title)s.%(ext)s' '<url>'

# subtitles/transcript (no download)
yt-dlp --write-subs --write-auto-subs --sub-lang en --skip-download \
  -o '~/tmp/%(title)s' '<url>'

# metadata
yt-dlp --dump-json '<url>' | jq '{title, duration, description}'

# key frames
ffmpeg -i ~/tmp/video.mp4 -vf "fps=1/30" ~/tmp/frame_%03d.jpg
```

YouTube, Twitter/X, Reddit, TikTok, Vimeo, 1000+ more sites.
`yt-dlp --list-extractors` to check.

## Audio transcription

```bash
curl -s -F "file=@$HOME/tmp/audio.mp3" -F "model=turbo" \
  "$WHISPER_BASE_URL/inference" | jq -r '.text'
```

Split long audio first:

```bash
ffmpeg -i ~/tmp/long.mp3 -f segment -segment_time 600 \
  -c copy ~/tmp/chunk_%03d.mp3
```

## Images

Claude reads images natively via Read tool:

```bash
curl -o ~/tmp/img.jpg '<url>'
```

## Web content

- Static: `curl -s '<url>'`
- JS-rendered / auth-required: `agent-browser` skill

## Rules

- ALWAYS prefer transcripts over raw media
- ALWAYS save intermediate files to `~/tmp/` (sendable via `send_file`)
- ALWAYS try `yt-dlp --dump-json` first — description/comments often suffice
- NEVER download full video when subtitles exist
