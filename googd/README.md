# googd

googd is the Google Drive comments channel adapter, prefix `gdrive:`. Today it
registers, checks its service-account credential and shows that on its
dashboard. It reads no comments and posts no replies yet. The protocol it
speaks is [`chanlib/README.md`](../chanlib/README.md).

## What runs today

- **Registration.** googd registers `gdrive:` and advertises `send_text`
  only while Google accepts its key (`capsFor`, `verifyCredential`). A
  missing or unparseable `GDRIVE_SA_KEY_JSON` is final until the operator
  fixes it. A key Google refused, or a call it never answered, is asked again
  every 30 s, and the first good answer puts `send_text` back.
- **It never exits on a bad key**: a daemon that exits at boot restarts the
  whole compose project.
- **`Send` has no reply path.** It answers 400 "googd cannot post a reply",
  even while `send_text` is advertised.
- **No inbound.** No poll loop runs, so `/health` never turns `stale`.
- **Dashboard.** A **Service account** section shows the credential state and
  the address to share a document with.
- **The binding is on routd.** `gdrive_files` pairs a Drive file id with a
  target folder (`routd/gdrive_files_resource.go`, `arizuko gdrive`), and
  `DeleteGroup` sweeps it with the folder.

googd has no `template/services/` fragment, so `arizuko packages add` cannot
install it, and it has no components page.

## Not built

- The comment poll and the thread reply, the one channel verb the adapter
  is meant to have. Resolving, reopening and editing a comment are connector
  tools, not channel verbs.
- Push wake-ups. The poll stays the correctness path: per Google's
  documentation as read on 2026-10-16, `files.watch` and Workspace Events
  deliver no comment content, only that something changed.

Configuration: `CHANNEL_NAME` (default `gdrive`), `GDRIVE_SA_KEY_JSON`,
`GDRIVE_IMPERSONATE`, `GDRIVE_API_URL`, `ROUTER_URL`, `LISTEN_ADDR`, `LISTEN_URL`.
