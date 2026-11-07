# googd

googd is the Google Drive comments channel adapter, prefix `gdrive:`. Today it
registers, checks its service-account credential and posts thread replies.
The comment mapper exists, but no poll loop delivers comments yet. The protocol it
speaks is [`chanlib/README.md`](../chanlib/README.md).

## What runs today

- **Registration.** googd registers `gdrive:` and advertises `send_text`
  only when the Drive probe succeeds before registration (`capsFor`, `prepareBot`). A
  missing or unparseable `GDRIVE_SA_KEY_JSON` is final until the operator
  fixes it. A key Google refused, or a call it never answered, is asked again
  every 30 s, and the first good answer puts `send_text` back.
- **It never exits on a bad key**: a daemon that exits at boot restarts the
  whole compose project.
- **Thread replies.** `send_text` posts a reply to the thread named by its JID.
- **No inbound.** No poll loop runs, so `/health` never turns `stale`.
- **Dashboard.** A **Service account** section shows the credential state and
  the address to share a document with.
- **The binding is on routd.** `gdrive_files` pairs a Drive file id with a
  target folder (`routd/gdrive_files_resource.go`, `arizuko gdrive`), and
  `DeleteGroup` sweeps it with the folder.

googd has no `template/services/` package, so `arizuko package add` cannot install it
from the bundled catalog. It has no components page.

## Not built

- The comment poll. Resolving, reopening and editing a comment are connector
  tools, not channel verbs.
- Push wake-ups. The poll stays the correctness path: per Google's
  documentation as read on 2026-10-16, `files.watch` and Workspace Events
  deliver no comment content, only that something changed.

Configuration: `CHANNEL_NAME` (default `gdrive`), `GDRIVE_SA_KEY_JSON`,
`GDRIVE_IMPERSONATE`, `GDRIVE_API_URL`, `ROUTER_URL`, `LISTEN_ADDR`, `LISTEN_URL`.
