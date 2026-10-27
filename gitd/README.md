# gitd — GitHub webhook ingress

A GitHub repository's events reach the agent as ordinary messages. gitd is a
channel adapter like `slakd`: it receives signed webhook deliveries, maps each
to `chanlib.InboundMsg` rows and POSTs them to routd. Everything the agent does
BACK to the repo — merge, label, review, close — goes through the GitHub MCP
connector (`template/connectors/github.toml`), not through channel verbs.

## Delivery sources

A **source** is one webhook secret, registered against a folder. Three kinds,
one receiver — GitHub sends the same signed POST for all of them:

| kind   | name             | paste the URL + secret into                                   |
| ------ | ---------------- | ------------------------------------------------------------- |
| `repo` | `<owner>/<repo>` | the repo's Settings → Webhooks                                |
| `org`  | `<owner>`        | the org's Settings → Webhooks (every repo the org owns)       |
| `app`  | `<slug>`         | the GitHub App's Webhook settings (every installation's repo) |

```bash
arizuko github <inst> add app arizuko-krons eng      # prints url + secret ONCE
arizuko github <inst> list                           # secrets never print again
arizuko github <inst> rm app/arizuko-krons eng       # 404s the URL, drops the routes
```

The same three actions are `/v1/github_sources` on routd (REST) and the
agent's `register_github_source` / `list_github_sources` /
`revoke_github_source` tools. The secret is sealed under `SECRETS_KEY` in
routd's `github_sources`, which gitd reads through `store.OpenOwner` — the
endpoint when `DYNAMODB_URL` names one — and unseals per delivery. The path `/github/<kind>/<name>`
selects the secret — the payload never chooses its own verifier — and a
repo/org source is bound to its repo/org: a payload naming another repo is
refused with 400 even when the signature is valid.

## JID grammar

```
github:<owner>                    account/org-level event (no repository)
github:<owner>/<repo>             repo-level event (push, release, …)
github:<owner>/<repo>/pr/<n>      one pull request   topic=pr/<n>
github:<owner>/<repo>/issue/<n>   one issue          topic=issue/<n>
```

Owner and repo ride in the JID (`chanlib.ParseGitHubJID` /
`FormatGitHubJID`), so one App secret serving fifty repos yields fifty JIDs
and the ordinary route table maps them. The sender is `github:<login>` — the
same principal authd mints for a GitHub sign-in, so one person's dashboard
login and PR comment are one identity.

## Observe vs trigger

`add` seeds mention routes and `<folder>#observe` fallbacks. The bot's login
comes from `GITHUB_TOKEN`. Assign the bot or request its review on a PR to
receive its activity as mentions. A submitted review also qualifies.

| Event                                                                             | Default result |
| --------------------------------------------------------------------------------- | -------------- |
| PR body, comment or review explicitly mentions `@<login>`                         | Turn           |
| Inline PR comment directly replies to a stored bot comment                        | Turn           |
| Human comment, inline comment or review with a body on a bot-authored PR or issue | `mention`      |
| PR assignment or individual review request targets the bot                        | Turn           |
| PR activity where the bot is assigned, requested or has submitted a review        | Turn           |
| Completed check, suite or workflow names such a PR                                | Turn           |
| Other plain comment or discussion                                                 | Observe        |
| Assignment or review request targets someone else on an unrelated PR              | Observe        |
| Other issue-only, repository-only, queued or in-progress event                    | Observe        |

Human conversation on a PR or issue that the bot authored maps to `mention`.
The bot's own comments drop, and its retained metadata never becomes a mention.
A person's `message` or `mention` keeps a PR or issue engaged after a reply, as
on every channel: a human comment on an engaged PR starts a turn without a new
mention. A `check_run`, `workflow_run` or review-state row that is not promoted
to `mention` does not ride engagement. It lands on the `#observe` fallback.
To let CI continue an open
conversation, add one route above the fallback, for example
`chat_jid=github:o/r/pr/* verb=check_run` to `sloth/morha#engaged`
([`ROUTING.md`](../ROUTING.md)). gitd carries no flag for this.
Repo rows sit at seq 10/100, org at 20/200, app at
30/300, so a narrower registration wins. Operators can edit the route rows.
For an existing source, remove any `verb=message` trigger rows and issue
mention trigger rows. Keep its PR mention route and observation fallbacks.

Subscribe to `issue_comment`, `pull_request`, `pull_request_review`,
`pull_request_review_comment` and `pull_request_review_thread` for PR conversations
and state. For CI, add `check_run`, `check_suite` and `workflow_run` events.
gitd reads the PR's assignees, requested reviewers and submitted review history
through the authenticated GitHub API. The token needs Pull requests read access.
Unassociated, queued and in-progress CI events perform no API lookup. A CI
delivery allows at most ten distinct PRs. Each delivery has ten seconds for
lookups. Failed or incomplete lookups return 503 before forwarding any row;
use GitHub's redeliver button after recovery. Associated PR rows use stable
delivery-and-PR IDs, so routd deduplicates a replay.
Fork checks with empty PR associations remain observations.

Created comments keep their replyable object IDs. Edits, deletions and review
dismissals use delivery IDs, so their notifications survive ingestion.
Those notifications do not rewrite or remove the original stored text. gitd
sets `ReplyTo` only on a created review comment. An edit or deletion can still
become a mention when the bot has responsibility for the PR.

## Location header

Every message gitd maps opens with one header line, so the agent reads where a
message sits without an API call. One renderer in `event.go` builds it, and the CI
fan-out in `ci.go` uses it too:

```
[github kind=pull_request_review_comment id=gh-rc5678901234 thread=gh-rc5678901200 review=gh-r6789012345 path=gitd/verify.go lines=10-12 side=RIGHT url=https://github.com/kronael/arizuko/pull/12#discussion_r5678901234]
```

`kind` is GitHub's event name. `id` is the message id, which is the value `reply`
takes as `replyToId`. A review comment also carries `thread`, `review`, `path`,
`lines` (`outdated=true` when GitHub reports no current line) and `side`, and the last 20
lines of its diff hunk follow in a fenced `diff` block. A review carries `review`
and `state`. An event leaves out a key it does not have.

## Typing

With a token, a running turn shows an 👀 reaction on the comment that started it
(`typing` is advertised with `send_text`); it comes off when the turn ends, at the
`chanlib.DefaultTypingMaxTTL` backstop. The token needs
Issues write (conversation comments) and Pull requests write (inline review
comments). A refused reaction is logged at warn and never fails the turn. The mark is
`chanlib.AckMark`, recorded in the adapter store: a crash or a graceful shutdown
leaves the 👀 and its record, because the turn may still be running. The turn's
own `Typing(off)` removes it, or the next boot when the record is older than the
backstop.

## Reply

`send_text` and `send_file` are the verbs, with `typing`, advertised only when
`GITHUB_TOKEN` is set. gitd advertises no `edit`, so routd posts no progress
comment on GitHub and the `👀` mark is the whole progress display.
`send_text` posts the agent's reply as a comment on the PR or issue the JID
names and returns the id the same comment carries inbound.
`send_file` posts a text file as a fenced block in a comment (GitHub has no API that
attaches a file to one); a binary or over-cap file is refused before any request, naming
the `github` connector, which can commit it to a branch.

The `reply` tool on a GitHub chat requires `replyToId`, the `id` of the message
the agent answers; `ipc/ipc.go` refuses a call without it and sends nothing.
`ReplyTo` decides which comment stream. A `gh-rc<id>` names an inline review
comment, so the reply goes under that code line: gitd resolves the comment that
opened the thread (GitHub accepts only that one) and posts to
`pulls/<n>/comments/<root>/replies`, returning `gh-rc<id>`. Anything else is the
issue-comments endpoint, returning `gh-c<id>`. The topic stays `pr/<n>` either
way — it names the session, never the address. The token's own
comments are dropped at ingest (`sender.login` == the token's login), as is
other bot comments. Typed PR state and CI events from automation remain eligible.
Without a token gitd is ingress-only: a `reply` on a
`github:` JID fails with a plain `channel github: send_text not supported`
(`chanreg/httpchan.go`), and on the MCP path `ipc/ipc.go` records the row
pending and tells the agent it is queued.

A `GET /user` that fails at boot lands in the same place: gitd logs it at
ERROR, withdraws `send_text` from the registration that already ran, and keeps
serving ingress — exiting would cycle every daemon on the instance (BUGS
`L406`). What follows depends on the answer. GitHub REFUSING the token is
permanent, so nothing asks again and a new credential is the only repair.
GitHub not answering — a dropped connection, a 5xx, a rate limit — is not, so
gitd re-asks `/user` every minute and puts `send_text` back the moment it
answers; it stays withdrawn in between because the login that drops gitd's own
comments at ingest is still unknown. `/health` reads 200 throughout: the daemon
is up and ingesting, and a `disconnected` 503 would have routd deregister the
channel after three probes and take ingress with it.

## Env

| var                       | default                  | note                                                                                   |
| ------------------------- | ------------------------ | -------------------------------------------------------------------------------------- |
| `ROUTER_URL`              | required                 |                                                                                        |
| `SECRETS_KEY`             | required                 | the keyring routd seals `github_sources` with                                          |
| `GITHUB_TOKEN`            | unset                    | enables `send_text`, `send_file`, `typing`                                             |
| `GITHUB_API_URL`          | `https://api.github.com` |                                                                                        |
| `LISTEN_ADDR`             | `:8080`                  |                                                                                        |
| `CHANNEL_NAME`            | `github`                 | the channel name gitd registers under                                                  |
| `LISTEN_URL`              | `http://gitd:8080`       | the URL gitd registers with routd                                                      |
| `GITHUB_APP_ACCEPTS_REPO` | unset                    | `1` lets an App source accept a repository delivery with no installation (BUGS `L409`) |

## Delivery response

GitHub's "Recent deliveries" tab shows gitd's answer: `200 {"ok":true,"id",
"jid","verb"}` delivered, `202 {"retained":true}` routd was down and the row
waits in gitd's own tables for re-delivery, `200 {"dropped":"bot"|"self"}` ignored,
`401` bad signature, `404` unknown source, `400` payload outside the source or
refused by routd, `503` CI lookup failed or neither routd nor retention took the message —
GitHub's redeliver button is then the recovery path. `GET /dash/gitd/` — mounted by `chanlib.NewAdapterMux` from
the one renderer in `chanlib/dash.go` — lists the last accepted
delivery per source.

## Coverage

`verify_test.go` (HMAC incl. forged/missing/tampered), `event_test.go`
(fixtures in `testdata/`: push, issues, pull_request opened/merged,
issue_comment on a PR and on an issue of a second repo, review comment reply,
ping, bot; two repos on one secret; observe-vs-trigger through
`router.ResolveRouteTarget`; the location header of every fixture), `server_test.go` (end to end against a fake
routd: signature, unknown source, containment, bot/self drops, retention when
routd is down, 503 when the retention write fails too, no retention on a
permanent refusal), `bot_test.go` (comment POST against a fake GitHub API), `caps_test.go` (the
advertised verbs match the implemented ones).
