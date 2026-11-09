# googd

googd is the Google Drive comments channel adapter, prefix `gdrive:`. It
checks its service-account credential, polls bound files and posts thread
replies. The protocol it speaks is [`chanlib/README.md`](../chanlib/README.md).

## What runs today

- **Registration.** googd registers `gdrive:` and advertises `send_text`, `edit` and `delete`
  only when the Drive probe succeeds before registration (`capsFor`, `prepareBot`). A
  missing or unparseable `GDRIVE_SA_KEY_JSON` is final until the operator
  fixes it. Drive and Sheets are probed every 30 s with separate API scopes. A Drive
  probe that recovers puts these verbs back. The Sheets probe enables the
  `sheets` passthrough prefix only while the API is on.
- **It never exits on a bad key**: a daemon that exits at boot restarts the
  whole compose project.
- **Connector passthrough.** `/google/drive/` and `/google/sheets/` forward
  requests using each API’s scope while its probe is on. `GOOGD_CONNECTOR_TOKEN`
  authenticates callers; an unset token refuses every request. The passthrough
  reaches every file the account can access, so connector tools and grants
  must limit access. It is not published through proxyd.
- **Thread replies.** `send_text` posts a reply to the thread named by its JID.
  `edit` updates that reply; `delete` removes it. Turn statuses edit on retry
  and delete on success, using the reply ID returned by `send_text`.
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

- Resolving, reopening and editing root comments are connector tools.
- Push wake-ups. The poll stays the correctness path: per Google's
  documentation as read on 2026-10-16, `files.watch` and Workspace Events
  deliver no comment content, only that something changed.

Configuration: `CHANNEL_NAME` (default `gdrive`), `GDRIVE_SA_KEY_JSON`,
`GDRIVE_IMPERSONATE`, `GDRIVE_API_URL`, `SHEETS_API_URL` (default
`https://sheets.googleapis.com`), `GOOGD_CONNECTOR_TOKEN`, `GDRIVE_POLL_INTERVAL` (default 30s,
minimum 10s), `ROUTER_URL`, `LISTEN_ADDR`, `LISTEN_URL`.
