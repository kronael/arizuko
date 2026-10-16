# gitd — GitHub webhook ingress

A GitHub repository's events reach the agent as ordinary messages. gitd is a
channel adapter like `slakd`: it receives signed webhook deliveries, maps each
to one `chanlib.InboundMsg` and POSTs it to routd. Everything the agent does
BACK to the repo — merge, label, review, close — goes through the GitHub MCP
connector (`template/connectors/github.toml`), not through channel verbs.
Spec: [`specs/6/27-git-channel.md`](../specs/6/27-git-channel.md).

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

`add` seeds route rows: human content — a comment, a new PR or issue
(`verb=message`, or `verb=mention` once the agent has spoken in the thread) —
triggers a turn; every other event (push, merge, label, CI) lands as
`<folder>#observe` context. Repo rows sit at seq 10/100, org at 20/200, app at
30/300, so a narrower registration wins. Edit the rows (`arizuko routes`,
`/dash/routd/`) to change the split; gitd never decides it.

## Reply

`send_text` is the only verb, advertised only when `GITHUB_TOKEN` is set: it
posts the agent's reply as a comment on the PR or issue the JID names and
returns the id the same comment carries inbound.

`ReplyTo` decides which comment stream. A `gh-rc<id>` names an inline review
comment, so the reply goes under that code line: gitd resolves the comment that
opened the thread (GitHub accepts only that one) and posts to
`pulls/<n>/comments/<root>/replies`, returning `gh-rc<id>`. Anything else is the
issue-comments endpoint, returning `gh-c<id>`. The topic stays `pr/<n>` either
way — it names the session, never the address. The token's own
comments are dropped at ingest (`sender.login` == the token's login), as is
any `sender.type == "Bot"`. Without a token gitd is ingress-only and `reply`
on a `github:` JID returns an Unsupported hint. A token GitHub REFUSES at boot
lands in the same place: gitd logs the refusal at ERROR, withdraws `send_text`
from the registration that already ran, and keeps serving ingress — exiting
would cycle every daemon on the instance (BUGS `L406`).

## Env

| var              | default                  | note                                          |
| ---------------- | ------------------------ | --------------------------------------------- |
| `ROUTER_URL`     | required                 |                                               |
| `SECRETS_KEY`    | required                 | the keyring routd seals `github_sources` with |
| `GITHUB_TOKEN`   | unset                    | enables `send_text`                           |
| `GITHUB_API_URL` | `https://api.github.com` |                                               |
| `LISTEN_ADDR`    | `:8080`                  |                                               |

## Delivery response

GitHub's "Recent deliveries" tab shows gitd's answer: `200 {"ok":true,"id",
"jid","verb"}` delivered, `202 {"retained":true}` routd was down and the row
waits in gitd's own tables for re-delivery, `200 {"dropped":"bot"|"self"}` ignored,
`401` bad signature, `404` unknown source, `400` payload outside the source or
refused by routd, `503` neither routd nor the retention store took the message —
GitHub's redeliver button is then the recovery path. `GET /dash/gitd/` — mounted by `chanlib.NewAdapterMux` from
the one renderer in `chanlib/dash.go` (spec 6/31) — lists the last accepted
delivery per source.

## Coverage

`verify_test.go` (HMAC incl. forged/missing/tampered), `event_test.go`
(fixtures in `testdata/`: push, issues, pull_request opened/merged,
issue_comment on a PR and on an issue of a second repo, review comment reply,
ping, bot; two repos on one secret; observe-vs-trigger through
`router.ResolveRouteTarget`), `server_test.go` (end to end against a fake
routd: signature, unknown source, containment, bot/self drops, retention when
routd is down, 503 when the retention write fails too, no retention on a
permanent refusal), `bot_test.go` (comment POST against a fake GitHub API).
