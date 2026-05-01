# bugs.md

Bugs / follow-ups discovered during voice + media-dispatch audit (2026-05-01).

## SendFile gaps (platforms with native media but no implementation)

- **mastd**: `NoFileSender` returns `errSendFile`. Mastodon has the v2
  media API (`POST /api/v2/media` + attach via `media_ids` on toot).
  Wire it to dispatch `.jpg/.png/.gif/.webp` → image, `.mp4/.webm`
  → video, `.mp3/.ogg` → audio. Documents/PDFs aren't natively
  supported — return Unsupported pointing the agent at a URL in toot
  text.
- **reditd**: `NoFileSender`. Reddit's image upload is a 3-step flow
  (websocket lease → S3 PUT → submit with `kind:image`). Spec out
  before implementing; for now `Unsupported(send_file, ...)` would be
  more honest than the generic errSendFile.
- **linkd**: `NoFileSender`. LinkedIn UGC media upload is a
  `/assets registerUpload` + binary PUT flow. Linkedin's
  `Post` for posts already returns Unsupported with a hint; SendFile
  inherits the generic errSendFile — convert to a structured Unsupported.
- **emaid**: explicitly returns `Unsupported(send_file, "MIME
  attachments not implemented; inline the content in send body.")`.
  Email is the universal medium for attachments; this is a future
  enhancement, not a wrong-dispatch bug.

## Other

- `chanlib.NoFileSender.SendFile` returns plain `errSendFile` (not a
  structured `*UnsupportedError`). Adapters embedding `NoFileSender`
  surface "send-file not supported" to the agent without the
  tool/platform/hint envelope. Convert `errSendFile` to
  `chanlib.Unsupported("send_file", "?", "...")` and let each
  embedder pin the platform name. Same for `NoVoiceSender` if we
  want richer hints than the bare ErrUnsupported.
