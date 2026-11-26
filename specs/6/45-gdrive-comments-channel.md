---
status: shipped
---

# Keep the Drive reply channel; defer new document actions

The Drive channel carries human comment threads to an agent and posts the
agent's replies through googd.

**Shipped decision, not live acceptance.** Keep the implemented channel and
Sheets read tool. Do not add resolve, reopen, document editing or push delivery
under this decision. Those actions require a separately approved connector
contract. No successful call to a real Google file is claimed.

## The connector cannot constrain a thread action

`routd/ext.go:37` declares a REST tool's method, path and model-supplied
parameters. It declares no fixed per-tool JSON body. A resolve tool that accepts
an unrestricted `action` could also reopen a thread. BUGS `L910` records that
contract decision. `template/connectors/google.toml` declares only
`read_sheet_range`.

## Decision 3 — the thread reply, and changes to the agent's own replies

`googd/main.go` implements `replies.create`, `replies.update` and
`replies.delete`. Edit and delete accept a reply ID for the same thread.
`googd/main.go` advertises text, edit and delete after the Drive probe succeeds.
The channel never resolves or reopens a thread. Each such state change needs
its own granted connector tool. The shared progress renderer can use the
channel's existing edit and delete operations.

`googd/poll.go` reads bound files and advances a cursor after accepted delivery.
`googd/state.go` stores poll deadlines and cursors; restart reads the store.
`routd/gdrive_files_resource.go` owns binding management through resreg.
The service-account key is an infra credential. The authenticated passthrough
spends that key; routd gates each declared connector tool.
Lasting behavior and limits belong to [googd](../../googd/README.md).

## A broader connector costs authority and maintenance

Estimated implementation after approval: **1–3 engineering days**, excluding
Google provisioning and live validation. Either per-tool constant bodies
change every REST connector's declaration contract, or separate googd action
handlers add a second API surface. The change replaces manual thread actions
with delegated mutation authority. Neither cost is needed for replying.

Polling remains the only inbound delivery mechanism. No cache, second
credential source, message expiry or log expiry is introduced. Only the
existing 90-day `audit_log` policy permits log expiry.

## The owner must supply Google acceptance evidence

The owner blocker is **a service-account credential whose project enables the
Drive API, plus real files shared with that account as Commenter**. The last
record names Drive disabled in project `morha-427320`. Current project state
is unverified. This closeout does not probe the project or change its API settings.

These nine probes remain unmeasured:

1. Compare comment IDs across two files to determine their uniqueness scope.
2. Post a plain reply on a resolved comment without an `action` value.
3. Inspect a service account's displayed author name in the editor.
4. Share a consumer `@gmail.com` document with the service account.
5. Measure the quota units charged for one minute of `comments.list` polling.
6. Poll twice at the same `startModifiedTime` to test cursor inclusivity.
7. Compare an editor's `PostAuthor.user` with the signed-in account's OIDC subject.
8. Attempt a Workspace Events subscription on a My Drive file.
9. Watch a real file and measure whether a comment produces a `files.watch` notification.

The owner also needs a disposable Drive acceptance that receives a human
comment and posts the reply in that thread. No googd acceptance case exists in
`anteval/cases/anteval.toml`. The live `replies.update` and `replies.delete`
progress lifecycle needs the same credential and a real test thread; fake
endpoint coverage does not prove Google accepts those operations.

Probes 8 and 9 cannot enable push under this decision. Probe 7 cannot confer
sender authority: Drive display names remain unattested.

## Controlled tests establish only the local contract

`googd/poll_test.go` covers pagination, accepted delivery, refusal and unchanged
cursors after later-page failure. `googd/send_test.go` covers reply IDs,
operation refusals and `TestTurnStatusReplyLifecycle`.
`googd/passthrough_test.go` covers the connector boundary.
`go test ./googd -count=1 -short` passes against controlled Google endpoints.
These tests establish local behavior, not live sharing, quota or identity.
