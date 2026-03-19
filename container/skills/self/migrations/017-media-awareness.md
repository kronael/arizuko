# Migration 017: Media Awareness

Agents now understand how to read files that users send.

## What changed

Added "Receiving files from users" section to CLAUDE.md:

- PDFs/documents: use `Read(path)` on the attached file path
- Images: use `Read(path)` — Claude vision reads them natively
- Voice: already transcribed, text appears inline

## Action

When you see `[media attached: /path/to/file (mime-type)]` in a message,
use the Read tool on that path. Claude can read PDFs, images, and
documents directly.

NEVER say "I can't read this document" without first trying to Read it.
