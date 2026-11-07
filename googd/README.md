# googd

googd is the Google Drive comments channel adapter, prefix `gdrive:`. It
checks its service-account credential, polls bound files and posts thread
replies. The protocol it speaks is [`chanlib/README.md`](../chanlib/README.md).

## What runs today

- **Registration.** googd registers `gdrive:` and advertises `send_text`
  only when the Drive probe succeeds before registration (`capsFor`, `prepareBot`). A
  missing or unparseable `GDRIVE_SA_KEY_JSON` is final until the operator
  fixes it. Drive and Sheets are probed every 30 s with separate API scopes. A Drive
  probe that recovers puts `send_text` back. The Sheets probe records state
  but does not advertise a capability.
- **It never exits on a bad key**: a daemon that exits at boot restarts the
  whole compose project.
- **Thread replies.** `send_text` posts a reply to the thread named by its JID.
- **Inbound.** The poll delivers comment threads from bound files to routd.
  It stores each file cursor only after routd accepts the page.
- **Dashboard.** A **Service account** section shows the credential state and
  the address to share a document with. API rows show probe state, Google’s
  reason and the enablement URL. Bound-file rows show the name, last poll,
  status and cursor.
- **The binding is on routd.** `gdrive_files` pairs a Drive file id with a
  target folder (`routd/gdrive_files_resource.go`, `arizuko gdrive`), and
  `DeleteGroup` sweeps it with the folder.

The bundled `googd` package starts the adapter and exposes its dashboard.

## Not built

- Resolving, reopening and editing a comment are connector tools, not channel verbs.
- Push wake-ups. The poll stays the correctness path: per Google's
  documentation as read on 2026-10-16, `files.watch` and Workspace Events
  deliver no comment content, only that something changed.

Configuration: `CHANNEL_NAME` (default `gdrive`), `GDRIVE_SA_KEY_JSON`,
`GDRIVE_IMPERSONATE`, `GDRIVE_API_URL`, `GDRIVE_POLL_INTERVAL` (default 30s,
minimum 10s), `ROUTER_URL`, `LISTEN_ADDR`, `LISTEN_URL`.
