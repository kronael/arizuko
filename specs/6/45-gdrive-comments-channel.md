---
status: draft
depends:
  [
    27-git-channel,
    31-adapter-contract,
    33-social-adapter-model,
    ../5/34-channel-protocol,
    ../5/13-ext-mcp,
    ../5/14-credentials,
    ../5/Q-unified-routing,
  ]
---

# specs/6/45 — gdrid: Google Drive comment threads as a channel

A Google Docs, Sheets or Slides file bound to a folder delivers each of its
comment threads to that folder's agent as a topic, and the agent answers inside
the thread — through one adapter, `gdrid`, that polls the Drive comments API
with a service-account credential and posts its replies with the same
credential.

`status: draft`: the owner's framing — a document is a group, a comment thread
is a topic — holds only in part (§"The owner's framing, tested"), the credential
is an operator decision (§"Decision 2"), and nine facts the design rests on are
marked unverified until a probe against a real file measures them
(§"Unverified"). Nothing here is built.

## Problem

A person who works in a document wants to ask the agent in the margin and read
the answer in the same thread. arizuko has that shape for GitHub: gitd maps a
signed webhook to one `chanlib.InboundMsg` whose JID names the repository and
the thread (`gitd/event.go:174`), and `send_text` posts the comment back
(`gitd/bot.go:98`). Google gives no webhook for a comment, the author of a
comment arrives without an email or a stable id, and the credential that can
read and reply on a file must itself be shared on that file. This spec decides
the ingress, the credential, the binding, the JID grammar and the verb set so a
Go developer builds the adapter from gitd's shape without a further question.

## What Google offers — verified 2026-10-16

Every row was read on Google's own page on the date above. "Unverified" marks
what no page states.

| Mechanism                                   | What the documentation says                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Source                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Drive push, `files.watch` / `changes.watch` | The notification body is empty ("`Content-Length: 0`"); the headers carry `X-Goog-Resource-State` ∈ `sync, add, remove, update, trash, untrash, change` and `X-Goog-Changed` ∈ `content, parents, children, permissions` (one table adds `properties`); no comment event is listed; expiration defaults to 3600 s, maximum 86400 s for `files` and 604800 s for `changes`; "there's no automatic way to renew a notification channel" — a new `watch` call replaces it; the address "must use HTTPS" with a valid certificate                                                                                                                                                                                                                                                                                                                                                                                                           | [push](https://developers.google.com/workspace/drive/api/guides/push)                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `comments.list`                             | `GET /drive/v3/files/{fileId}/comments`; `startModifiedTime` (RFC 3339, the minimum comment `modifiedTime`), `includeDeleted`, `pageSize` default 20 maximum 100, `pageToken`; "The `fields` parameter must be set"; scopes `drive`, `drive.file`, `drive.readonly`, `drive.meet.readonly`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | [list](https://developers.google.com/workspace/drive/api/reference/rest/v3/comments/list)                                                                                                                                                                                                                                                                                                                                                                                                               |
| Comment resource                            | `modifiedTime` is the last modification of the comment "or any of its replies"; `replies[]` in chronological order; `resolved` — "resolved by one of its replies"; `anchor` — "A region of the document represented as a JSON string", opaque for editor files; `author` — "email address and permission ID won't be populated"; `deleted` — no content; `content` plain text, `htmlContent` output only; `quotedFileContent{mimeType,value}`; `mentionedEmailAddresses[]`, `assigneeEmailAddress`                                                                                                                                                                                                                                                                                                                                                                                                                                      | [comments](https://developers.google.com/workspace/drive/api/reference/rest/v3/comments)                                                                                                                                                                                                                                                                                                                                                                                                                |
| Reply resource, `replies.create`            | `POST …/comments/{commentId}/replies`; `action` ∈ `resolve`, `reopen`; `content` "required by the `create` method if no `action` value is specified"; scopes `drive`, `drive.file`; "A comment can only be resolved by posting a reply to a comment"                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | [replies](https://developers.google.com/workspace/drive/api/reference/rest/v3/replies), [create](https://developers.google.com/workspace/drive/api/reference/rest/v3/replies/create), [guide](https://developers.google.com/workspace/drive/api/guides/manage-comments)                                                                                                                                                                                                                                 |
| Anchors                                     | "Anchors are immutable, and their position relative to the content of a document cannot be guaranteed between revisions"; editor files carry "editor-specific internal anchor data" the Drive API "treats as opaque"; editors treat a Drive-created anchored comment as unanchored                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | [guide](https://developers.google.com/workspace/drive/api/guides/manage-comments)                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `about.get`, User                           | `GET /drive/v3/about?fields=user` returns "The authenticated user" as a User: `displayName`, `emailAddress`, `permissionId`, `me` ("Whether this user is the requesting user"), `photoLink`; `fields` required                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | [about](https://developers.google.com/workspace/drive/api/reference/rest/v3/about), [User](https://developers.google.com/workspace/drive/api/reference/rest/v3/User)                                                                                                                                                                                                                                                                                                                                    |
| Quota                                       | 1,000,000 units per minute per project; 325,000 per minute per user per project; "List items, such as `files.list`" 100 units, reads 5, edits 50; 400,000,000 units per day is the billing threshold; `403 User rate limit exceeded`, `429`; truncated exponential backoff                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | [limits](https://developers.google.com/workspace/drive/api/guides/limits)                                                                                                                                                                                                                                                                                                                                                                                                                               |
| Scopes                                      | `drive`, `drive.readonly`, `drive.activity*` are **restricted** (verification; "If you store restricted scope data on servers … you must go through a security assessment"); `drive.file` is non-sensitive and covers files the user opens with the app or shares through the Picker                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | [scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth)                                                                                                                                                                                                                                                                                                                                                                                                                    |
| Workspace Events API, Drive                 | Event types `google.workspace.drive.comment.v3.{created,edited,resolved,reopened,deleted}` and `reply.v3.{created,edited,deleted}` (plus file, permission, approval events); endpoint is a Pub/Sub topic published by `drive-api-event-push@system.gserviceaccount.com`; payload without resource data is `{"comment":{"id","file_id"}}` / `{"reply":{"id","comment_id","file_id"}}`; maximum lifetime 7 days without resource data, 4 hours with, renewed by `patch` of `ttl`; lifecycle reminders 12 h and 1 h before expiry; "Subscriptions are only supported for files and folders inside shared drives"; the authorizing user "must have permission on the file"; release notes: "(Developer Preview): The Google Workspace Events API now supports subscriptions to Google Drive" (2025-07-07), no GA entry; preview terms: features "may not be included in public applications prior to the General Availability announcement" | [events-drive](https://developers.google.com/workspace/events/guides/events-drive), [subscriptions](https://developers.google.com/workspace/events/reference/rest/v1/subscriptions), [create](https://developers.google.com/workspace/events/guides/create-subscription), [renew](https://developers.google.com/workspace/events/guides/update-subscription), [release notes](https://developers.google.com/workspace/events/release-notes), [preview](https://developers.google.com/workspace/preview) |
| Drive Activity API v2                       | `activity.query` filters `time > …` and `detail.action_detail_case:COMMENT`; the Comment detail carries `post.subtype` ∈ `ADDED, DELETED, REPLY_ADDED, REPLY_DELETED, RESOLVED, REOPENED`, assignment and suggestion subtypes, `mentionedUsers[]`; **no comment id and no text**; scopes `drive.activity(.readonly)`, restricted                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | [query](https://developers.google.com/workspace/drive/activity/v2/reference/rest/v2/activity/query), [comment](https://developers.google.com/workspace/drive/activity/v2/reference/rest/v2/activity/comment)                                                                                                                                                                                                                                                                                            |
| Docs / Sheets / Slides APIs                 | `documents.get` with `commentsViewMode=COMMENTS_VIEW_MODE_INCLUDED` (requires `suggestionsViewMode=SUGGESTIONS_INLINE`) returns `comments[]` as `CommentThread{commentId, anchorId, headPost, replies[], status OPEN/RESOLVED, plainTextQuote}`, `Post{postId, content, author, createTime, deleted, assigneeEmail}`, `PostAuthor{displayName, me, anonymous, user "users/{user}"}` and `commentAnchors` → `ranges[]{segmentId, startIndex, endIndex, tabId}`; `batchUpdate` requests `insertComment`, `addCommentReply`, `updateCommentPost`, `deleteComment`, `deleteCommentReply` exist in all three (Docs 53, Sheets 74, Slides 49 request types)                                                                                                                                                                                                                                                                                   | [documents](https://developers.google.com/workspace/docs/api/reference/rest/v1/documents), [docs requests](https://developers.google.com/workspace/docs/api/reference/rest/v1/documents/request), [spreadsheets](https://developers.google.com/workspace/sheets/api/reference/rest/v4/spreadsheets), [slides requests](https://developers.google.com/workspace/slides/api/reference/rest/v1/presentations/request)                                                                                      |
| Service accounts                            | "an account that belongs to your application instead of to an individual end user"; "No end user is involved"; scopes ride in the JWT `scope` claim; domain-wide delegation impersonates through the `sub` claim and is granted by a super administrator per scope — "the app has access to the data belonging to all of your users"; "Service accounts don't have storage quota in Drive, so ownership transfers to service accounts will fail"; exempt from the 100-refresh-token limit                                                                                                                                                                                                                                                                                                                                                                                                                                               | [service-account](https://developers.google.com/identity/protocols/oauth2/service-account), [DWD](https://knowledge.workspace.google.com/admin/apps/control-api-access-with-domain-wide-delegation), [transfer](https://developers.google.com/workspace/drive/api/guides/transfer-file)                                                                                                                                                                                                                 |
| User OAuth refresh tokens                   | an External project in `Testing` status "is issued a refresh token expiring in 7 days"; "a limit of 100 refresh tokens per Google Account per OAuth 2.0 client ID", the oldest "invalidates … without warning"                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | [oauth2](https://developers.google.com/identity/protocols/oauth2)                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| Pub/Sub                                     | message retention default 7 days, maximum 31; acknowledgement deadline 10–600 s; unacknowledged messages past 24 h cost extra; inactive subscriptions expire after 31 days                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | [properties](https://docs.cloud.google.com/pubsub/docs/subscription-properties)                                                                                                                                                                                                                                                                                                                                                                                                                         |
| Google's hosted MCP                         | Drive MCP server (Developer Preview): 8 tools, none for comments; Docs MCP server (Developer Preview): `read_doc`, `update_doc`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | [drive mcp](https://developers.google.com/workspace/drive/api/reference/mcp), [docs mcp](https://developers.google.com/workspace/docs/api/reference/mcp)                                                                                                                                                                                                                                                                                                                                                |
| Third-party evidence                        | Pipedream's `google_drive-new-or-modified-comments` 1.1.0 "(Instant)" is a Drive `change` push notification followed by `comments.list` on the selected file with a per-file `modifiedTime` high-water mark; Apps Script has no comment trigger (open, edit, change, form submit, time-driven)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | [pipedream](https://raw.githubusercontent.com/PipedreamHQ/pipedream/master/components/google_drive/sources/new-or-modified-comments/new-or-modified-comments.mjs), [triggers](https://developers.google.com/apps-script/guides/triggers)                                                                                                                                                                                                                                                                |

Whether a comment makes `files.watch` fire an `update` or `change` notification
is **unverified**: Google's page lists no comment event, and Pipedream's source
relies on it in practice.

## Decision 1 — polling, bounded; no push

The adapter polls `comments.list` per bound file. The two push shapes are
declined:

- **Workspace Events** is the only comment-level push and it is Developer
  Preview ("may not be included in public applications"), delivers to a Pub/Sub
  topic (a Cloud project, a topic, a puller and a second credential), covers
  "files and folders inside shared drives" only, needs a 7-day subscription per
  file renewed by the same OAuth client, and its payload carries ids only — the
  fetch is still `comments.list`.
- **`files.watch`** carries an empty body and no comment reason, lives one day,
  and needs a public HTTPS endpoint with a valid certificate. It can at best
  wake the same list call sooner, and it adds two failure modes (an expired
  channel, a refused endpoint) for latency this surface does not need.
- **Drive Activity** is a second restricted scope and still a poll, and it
  returns no comment id, so the list call follows anyway.

### The poll (proposed)

One request per bound file per interval:

```
GET /drive/v3/files/{fileId}/comments
    ?startModifiedTime=<cursor>&pageSize=100&pageToken=…
    &fields=nextPageToken,comments(id,createdTime,modifiedTime,resolved,deleted,
      author(displayName,me),content,quotedFileContent(value),
      mentionedEmailAddresses,assigneeEmailAddress,
      replies(id,createdTime,modifiedTime,deleted,action,author(displayName,me),
        content,mentionedEmailAddresses,assigneeEmailAddress))
```

The response carries whole threads, replies included, so there is no second
fetch. A reply bumps its head comment's `modifiedTime`, so the
`startModifiedTime` filter returns the thread. The adapter delivers every post
it has not delivered yet — routd's message key is the dedup: "A DUPLICATE ID
IS A NO-OP" (`store/messages.go:234`) — and advances the file's cursor to the
largest `modifiedTime` on the page only after routd accepted every post on
it. That is the cursor discipline teled, reditd and emaid follow: "the platform
IS their queue — they must NOT wire" `RetainInbound` (`chanlib/inbound.go:7`).
A routd outage costs nothing but delay.

| Knob                                                          | Value                                                                | Why                                                          |
| ------------------------------------------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------ |
| `GDRIVE_POLL_INTERVAL`                                        | default `30s`, floor `10s`                                           | one list call is 100 units; see the arithmetic below         |
| page size                                                     | 100                                                                  | the API maximum                                              |
| per-file backoff on `403 userRateLimitExceeded` / `429` / 5xx | 30 s doubling to 15 min                                              | the same bounds `chanlib/inbound.go` uses for a routd outage |
| a `403` or `404` on one file                                  | that file's status on `/dash/gdrid/`, polling continues for the rest | a forgotten share is the operator's, not the daemon's        |

Quota arithmetic at 30 s: one file costs 2 × 100 = 200 units per minute. The
per-user cap (325,000 per minute, one service account is one user) allows
1,625 files; the project cap allows 5,000; the 400,000,000 units per day
billing threshold allows 1,388 files polling around the clock at no charge.
Latency is at most one interval plus the sweep.

## Decision 2 — the credential is a service account, shared per document

`gdrid` authenticates as a service account with the scope
`https://www.googleapis.com/auth/drive`. The operator shares each document
with the account's address as Commenter; that share is the precondition of a
binding and is Google's own authorization. The key is an infra credential
(`specs/5/14-credentials.md` §"3. Infra / operator credentials"): host `.env`,
read at boot, never in the `secrets` table.

| Point                      | Service account (chosen)                                                                                     | Per-user OAuth (`auth/surrogate/providers/google.toml`)                                                                      |
| -------------------------- | ------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------- |
| Who the reply appears as   | the service account (with `GDRIVE_IMPERSONATE`, the impersonated Workspace user)                             | the human who consented                                                                                                      |
| Consent, publishing status | none; "No end user is involved"                                                                              | an External project in `Testing` loses the refresh token after 7 days; publishing with a restricted scope means verification |
| Scope that works headless  | `drive` (restricted) — `drive.readonly` cannot reply, `drive.file` sees only files opened through the Picker | the same                                                                                                                     |
| Lifetime                   | the key, until rotated                                                                                       | 100 tokens per account per client, oldest dropped silently                                                                   |
| Where it lives             | host `.env` (infra tier)                                                                                     | `secrets` row (capability tier) — the right place for the CONNECTOR's token, not the daemon's                                |

Identity at boot: `about.get?fields=user` yields the account's `emailAddress`
and `permissionId`; the email decides `verb=mention`
(§"Wire shapes") and shows on the dashboard. Self-drop needs no lookup at all:
every post carries `author.me`, Google's own answer to "did I write this". gitd
has to learn its login from `/user` and skips the check without a token
(`gitd/bot.go:54`, `gitd/server.go:83`) — the gap BUGS `L410` found. Here
there is no credential-less mode: without the key `Start` returns an error
and the process exits, so a healthy daemon that can read but not answer cannot
exist.

`GDRIVE_IMPERSONATE=<user@domain>` (domain-wide delegation) is optional and
reaches only a Workspace domain whose super administrator granted the account
the `drive` scope; the replies then appear as that user, and the account
reads everything that user reads — the owner's call per instance, not the
default. Unverified: how the editors render a service account's display name
(§"Unverified").

## Decision 3 — one verb, the thread reply

`capsFor` advertises `send_text` alone, and `Send` is `replies.create` on the
thread the JID names, with `fields=id`. A send to a file-level JID returns
`ErrInvalidRequest`, as gitd's does for a repository JID (`gitd/bot.go:104`).
The adapter never sets `action`, never calls `comments.create`, never edits a
document. Resolving or reopening a thread, opening a new (anchored) thread
through `insertComment`, and every edit are state changes and go through an MCP
connector under per-tool grants and holds — `specs/5/13-ext-mcp.md` §"Shape 2 —
MCP HTTP connector (shipped)", `specs/5/19-hitl-firewall.md` — exactly the
line `specs/6/27-git-channel.md` §"Channel messages and repository tools"
holds for GitHub.

Why the line holds here too: a thread reply is the one action whose authority
is "the agent was addressed in this thread". A resolve closes a human's
question; a new thread interrupts a document; an edit changes the artefact.
Each deserves its own `acl` row and, where the operator wants it, a `hold:`.
A second channel verb would bypass that boundary and would have to be
re-implemented in every adapter that grows one.

The connector is declared in `connectors.toml` like
`template/connectors/github.toml`. Google's hosted Docs and Drive MCP servers
are Developer Preview and carry no comment tool today; any MCP server over the
`batchUpdate` comment requests fits. The surrogate `GOOGLE_TOKEN` the dashboard
writes fills its slot — its scope list is `drive.readonly` today
(`auth/surrogate/providers/google.toml:11`), so a connector that edits needs a
wider scope there; that change belongs to the connector, not to this spec.

Reply bodies are plain text: `content` "is used for setting the content" and
`htmlContent` is output only, so markdown lands as its source characters. The
output style for the `gdrive` surface is plain prose
(`specs/5/Y-output-styles-per-surface.md`).

## Decision 4 — binding: `gdrive_files` on routd

A binding is a row on routd that pairs a Drive file id with a target folder and
seeds the routes. It mirrors `github_sources` in every part the surface rules
of root `CLAUDE.md` demand: a resreg resource with REST and derived MCP faces
(`resreg/resources/github_sources.go:27`), target containment
(`routd/github_sources_resource.go:69`), a route renderer
(`store/github_sources.go:129`), and a CLI (`cmd/arizuko/github.go`). It holds
no secret, so unlike gitd the adapter needs no `SECRETS_KEY`.

### Proposed resource

| Item          | Value                                                                                                                                                                                                                              |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| resreg `Name` | `gdrive_files` — REST `/v1/gdrive_files`, MCP prefix                                                                                                                                                                               |
| Actions       | `register` (`POST /v1/gdrive_files`, 201), `list` (`GET`), `revoke` (`DELETE /v1/gdrive_files/{file_id}`)                                                                                                                          |
| MCP names     | `register_gdrive_file`, `list_gdrive_files`, `revoke_gdrive_file`                                                                                                                                                                  |
| Row           | `file_id`, `owner_folder`, `target_folder`, `created_at`                                                                                                                                                                           |
| Uniqueness    | one binding per file per instance; a second `register` answers 409                                                                                                                                                                 |
| Who may bind  | REST: `target_folder` equal to or under the caller's folder (`groupfolder.Contains`, as `routd/github_sources_resource.go:69`); MCP: `db.AuthorizeTurn` on `mcp:register_gdrive_file`, so an agent binds only into its own subtree |
| Identity      | the file id; the title is display, read by `files.get?fields=name,mimeType` (5 units) for `ChatName` and the dashboard                                                                                                             |
| CLI           | `arizuko gdrive <inst> add <file_id> <folder>` / `list` / `rm <file_id>`, the shape of `cmd/arizuko/github.go`                                                                                                                     |

### Seeded routes (proposed)

The renderer follows `store.GitHubSourceRoutes`: human posts trigger, the
rest is context, and the operator edits the rows.

| Seq | Match                                       | Target             |
| --- | ------------------------------------------- | ------------------ |
| 10  | `chat_jid=gdrive:<fileId>/c/* verb=message` | `<folder>`         |
| 10  | `chat_jid=gdrive:<fileId>/c/* verb=mention` | `<folder>`         |
| 100 | `chat_jid=gdrive:<fileId>/c/*`              | `<folder>#observe` |
| 100 | `chat_jid=gdrive:<fileId>`                  | `<folder>#observe` |

The default is gitd's: every human post on a bound file boots a guest. A
document full of human-to-human threads wants the first row deleted, so only
a mention or a reply to the agent triggers; once the agent has answered in a
thread, engagement keeps that thread flowing to the folder without a route
(`ROUTING.md:170`).

The adapter reads the binding set from routd's store each sweep
(`store.OpenOwner(store.OwnerRoutd)`, as `gitd/main.go:34`), polls exactly
those files, and keeps the per-file cursor and last status in its own prefix
(`chanlib.OpenAdapterStore("gdrid")`, `chanlib/dedup.go:31`). A revoke drops
the routes; the adapter stops polling the file at the next sweep. A share the
operator forgot shows as that file's `403` on the dashboard.

## The owner's framing, tested

| Framing                                      | Verdict                              | Where it breaks                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| -------------------------------------------- | ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| "A document is a group"                      | partly                               | A document is a JID bound to a folder by routes, the way a repository is: "Route resolution selects the group; gitd does not derive a folder" (`specs/6/27-git-channel.md` §"Sources and identity"). One folder may hold many documents; a document never creates a folder — the per-PR child group of `specs/6/27-git-channel.md` is unapproved and this spec proposes no document child. The roster is Google's sharing list: whoever may comment may address the agent, and the sender arrives as a display name only, so no grant can hang on a commenter. |
| "A comment thread is a topic"                | holds, with limits                   | Threads are flat: a Reply has no parent field, so "reply to a reply" does not exist. The agent can answer only in a thread a human opened; a thread the agent opens is a connector action (`insertComment`), and a Drive-created anchored comment is "treated as unanchored" by the editors. Anchors drift: the topic keeps its id while the text it points at moves or disappears between revisions.                                                                                                                                                          |
| "A person comments; the agent answers there" | holds                                | at poll latency, for a file the account is shared on; every human post triggers under the default routes until the operator narrows to mentions                                                                                                                                                                                                                                                                                                                                                                                                                |
| Unresolved vs resolved                       | a flag, not a state the adapter owns | `resolved` is set by a reply with `action=resolve`. The adapter delivers that reply as `verb=resolve` to `#observe` and never changes the flag; reopening is the connector's `action=reopen`. Whether Drive accepts a plain reply on a resolved thread is unverified; a refusal surfaces as `ErrPermanent` on the delivery.                                                                                                                                                                                                                                    |
| Document edits                               | not messages                         | The adapter never looks at edits. The body reaches the agent through the connector at turn time, and the anchored text rides in the message as `quotedFileContent`. No comment-versus-edit signal is needed because nothing subscribes to edits.                                                                                                                                                                                                                                                                                                               |
| Identity                                     | display only                         | The Drive API withholds the author's email and permission id by design. The editor APIs give `PostAuthor.user` as `users/{id}`; whether that id equals the OIDC `sub` authd stores as `google:<id>` (`authd/server.go:307`) is unverified. Until measured, a `gdrive:` sender carries no grant and a route row with a `sender=` predicate on a `gdrive:` JID is a mistake.                                                                                                                                                                                     |

## Wire shapes (proposed)

### JID grammar

```
gdrive:<fileId>                    the file         topic ""            observe only
gdrive:<fileId>/c/<commentId>      one thread       topic c/<commentId>
```

A new file in `chanlib/` beside `chanlib/github_jid.go`, with the same
parse/format/`Topic()` trio (`chanlib/github_jid.go:75`). `fileId` and
`commentId` are opaque; the parser accepts `[A-Za-z0-9_-]+` and refuses an
empty segment or a `/` inside one. The charset beyond observed ids is
unverified; a stricter alphabet is a one-line change.

### InboundMsg

| Field                   | Value                                                                                                                                                                                      |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `ID`                    | `gd-c<fileId>.<commentId>` for a head comment, `gd-r<fileId>.<commentId>.<replyId>` for a reply. The file id rides along because the uniqueness scope of a Drive comment id is unverified. |
| `ChatJID`               | `gdrive:<fileId>/c/<commentId>`                                                                                                                                                            |
| `Topic`                 | `c/<commentId>` — the session key `(folder, topic)` gives two threads two sessions                                                                                                         |
| `Sender` / `SenderName` | `gdrive:<displayName>` / `displayName` — display only, unattested                                                                                                                          |
| `Content`               | head: `quotedFileContent.value` as a quoted first paragraph, then `content`; reply: `content`                                                                                              |
| `Timestamp`             | `createdTime`                                                                                                                                                                              |
| `ReplyTo`               | the id of the preceding post in the thread (the head for the first reply). routd's `directReply` then marks a reply that follows the agent's own as `mention` (`routd/server.go:486`).     |
| `Verb`                  | the table below                                                                                                                                                                            |
| `IsGroup`               | `true` — a document has many readers                                                                                                                                                       |
| `ChatName`              | the file name from `files.get`, else the file id                                                                                                                                           |
| `Source`                | `CHANNEL_NAME` (`gdrive`)                                                                                                                                                                  |

### Verbs

| Post                                                     | Verb                                                                                                           |
| -------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| head comment or reply with content, `author.me == false` | `message`; `mention` when the account's email is in `mentionedEmailAddresses` or equals `assigneeEmailAddress` |
| reply with `action=reopen` and content                   | `message` / `mention` by the same rule                                                                         |
| reply with `action=resolve`                              | `resolve`                                                                                                      |
| reply with `action=reopen` and no content                | `reopen`                                                                                                       |
| any post with `author.me == true`                        | dropped (`self`): the agent's own replies, including the `gd-r…` id `send_text` returned                       |
| `deleted == true`                                        | not delivered — there is no content                                                                            |
| an edit of a delivered post                              | not re-delivered: routd keeps the first row (`store/messages.go:234`)                                          |

### Outbound

`send_text` on `gdrive:<fileId>/c/<commentId>` is
`POST /drive/v3/files/{fileId}/comments/{commentId}/replies?fields=id` with
`{"content": <text>}`. A 2xx with an id returns `gd-r<fileId>.<commentId>.<id>`
— the id the same reply carries inbound, so the self-drop also matches by id.
`429` and 5xx map to `chanlib.ErrNotDelivered` (routd re-sends the pending
row); any other 4xx to `chanlib.ErrPermanent`; a file-level JID to
`chanlib.ErrInvalidRequest`. The mux dedups re-sends on `IdempotencyKey`
(`chanlib/handler.go:31`), as for every adapter.

## Sheets and Slides

Free. The Drive comments API is per file and type-blind: `comments.list` and
`replies.create` take a file id, and the adapter reads `mimeType` only for the
dashboard. What differs is outside the adapter: the anchor is opaque for all
three editors; `quotedFileContent` for a Sheets cell or a Slides element is
unverified; and the editor-specific comment requests (`insertComment` and
siblings exist in Docs, Sheets and Slides alike) are the connector's. A PDF or
an image shared with the account works the same way, and is where Google says
anchored comments render reliably.

## Env (proposed)

| Var                     | Default                      | Note                                                               |
| ----------------------- | ---------------------------- | ------------------------------------------------------------------ |
| `ROUTER_URL`            | required                     |                                                                    |
| `GDRIVE_SA_EMAIL`       | required                     | the service account's `client_email`; the daemon exits without it  |
| `GDRIVE_SA_PRIVATE_KEY` | required                     | the key's `private_key` PEM, `\n`-escaped; signs the JWT assertion |
| `GDRIVE_IMPERSONATE`    | unset                        | domain-wide delegation subject                                     |
| `GDRIVE_POLL_INTERVAL`  | `30s`                        | floor `10s`                                                        |
| `GDRIVE_API_URL`        | `https://www.googleapis.com` | tests point it at a fake                                           |
| `CHANNEL_NAME`          | `gdrive`                     |                                                                    |
| `LISTEN_ADDR`           | `:8080`                      |                                                                    |

`compose/compose.go:233` lists which host `.env` keys each daemon's generated
env file receives; the `gdrid` entry names `AUTHD_URL`, `AUTHD_SERVICE_KEY`,
`GDRIVE_SA_EMAIL`, `GDRIVE_SA_PRIVATE_KEY`, `GDRIVE_IMPERSONATE`. The compose
fragment has the shape of `template/services/gitd.yml` without `SECRETS_KEY`.

## Dashboard

`/dash/gdrid/` is the shared renderer (`chanlib.NewAdapterMux`,
`chanlib/handler.go:290`; `specs/6/31-adapter-contract.md` §"One renderer in
chanlib") plus one delta section: a row per bound file — file id, name, last
poll, last status (`ok`, `403`, `404`, `429`), cursor. The `403` row is the
operator's "share it" prompt; the account's email sits in the overview so they
know whom to share with.

## Cost

- Code: gitd is 679 non-test lines at `c8c687eb` (`wc -l gitd/*.go`); gdrid
  drops HMAC verification and the webhook event zoo and adds a poll loop and a
  service-account JWT signer. Estimate 500–700 lines, plus ~200 for the
  resource and CLI. An estimate, not a measurement.
- Google: a Cloud project, a service account, the Drive API enabled; no charge
  below the quota threshold (§"Decision 1").
- Operator: share each document with the account; one `arizuko gdrive add`.
- Latency: one poll interval.
- Security: the restricted `drive` scope on one account, so every file shared
  with it is readable by the daemon; the key is in the host `.env`.

## Unverified

Each line names the probe that settles it; none blocks writing the adapter,
and the design is chosen so that each answer changes at most one line.

| Claim                                                                                     | Probe                                           | If false                                                        |
| ----------------------------------------------------------------------------------------- | ----------------------------------------------- | --------------------------------------------------------------- |
| a Drive comment id is unique only within its file                                         | compare ids across two files                    | nothing: the message id carries the file id                     |
| `replies.create` without `action` succeeds on a resolved comment                          | reply on a resolved test comment                | the delivery fails `ErrPermanent`; reopen stays the connector's |
| the editors render a service account's `displayName` acceptably                           | post one reply, look                            | set `GDRIVE_IMPERSONATE` on a Workspace domain                  |
| a consumer `@gmail.com` document can be shared with a service account's address           | share one                                       | Workspace domains only; say so in INSTALL                       |
| `comments.list` is a 100-unit list-class call                                             | read the quota page after one minute of polling | the interval floor moves                                        |
| `startModifiedTime` is inclusive                                                          | poll twice with the same cursor                 | nothing: id dedup                                               |
| `PostAuthor.user` (`users/{id}`) equals the OIDC `sub` authd stores                       | sign in with a test account, compare            | the sender stays display-only                                   |
| Workspace Events' "inside shared drives" limit applies to a file subscription on My Drive | subscribe to one My Drive file                  | the wake-up (§"Not built") stays declined even at GA            |
| a comment fires a `files.watch` notification                                              | watch a file, comment                           | irrelevant to the poll design                                   |

## Not built

| Shape                                             | Why not now                                                                                                     | Build when                                                                                       |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Workspace Events wake-up                          | Developer Preview, Pub/Sub, shared drives only, 7-day renewals, ids-only payload                                | GA, My Drive files covered; the poll stays the correctness path and events only shorten the wait |
| `files.watch` push                                | empty body, no comment reason, 1-day channels, public HTTPS endpoint                                            | never for this surface                                                                           |
| per-user OAuth as the daemon credential           | replies as a human, 7-day test tokens, verification                                                             | never; the surrogate token is the connector's                                                    |
| document edits as messages                        | the body is context the connector reads at turn time                                                            | a case that needs edit events as context names itself                                            |
| a child folder per document                       | the per-PR workspace of `specs/6/27-git-channel.md` is itself unapproved; a document has no checkout to isolate | a document that needs its own files, grants or budget                                            |
| a second channel verb (resolve, new thread, edit) | each is a state change with its own grant and hold                                                              | never; that is what the connector is for                                                         |
| Drive Activity API                                | restricted scope, still a poll, no comment id                                                                   | never                                                                                            |
