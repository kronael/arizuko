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
arizuko github -i <inst> add app arizuko-krons eng      # prints url + secret ONCE
arizuko github -i <inst> list                           # secrets never print again
arizuko github -i <inst> rm app/arizuko-krons eng       # 404s the URL, drops the routes
```

The same three actions are `/v1/github_sources` on routd (REST) and the
agent's `register_github_source` / `list_github_sources` /
`revoke_github_source` tools. The secret is sealed under `SECRETS_KEY` in
routd's `github_sources`, which gitd reads through `store.OpenOwner` — the
endpoint when `DYNAMODB_URL` names one — and unseals per delivery. The path `/github/<kind>/<name>`
selects the secret — the payload never chooses its own verifier — and a
repo/org source is bound to its repo/org: a payload naming another repo is
refused with 400 even when the signature is valid.

## A GitHub App connects selected repositories

Use one GitHub App per instance. An installation gives the App access to selected
repositories. A source supplies webhook verification and routing to a group.

1. In GitHub account or organization settings, open **Developer settings → GitHub Apps → New GitHub App**.
   Enter an App name and your instance's public HTTPS URL as the homepage.
   Create the App. Record its numeric **App ID**.
   The slug is the name in `https://github.com/apps/<slug>`.
   A private App installs only on its owner's account. Other accounts require a public App.
2. Under **Private keys**, select **Generate a private key**.
   Keep the downloaded PEM file private. Encode the file as one base64 line:

   ```bash
   base64 -w0 /path/to/private-key.pem
   ```

   Set these values in the instance's `.env`:

   ```dotenv
   GITHUB_APP_ID=<numeric-app-id>
   GITHUB_APP_PRIVATE_KEY=<base64-pem>
   ```

   These are host credentials. Do not put the private key in a group or an agent secret.

3. Register the App source for the group that receives its events:

   ```bash
   arizuko github -i <inst> add app <slug> <folder>
   ```

   Copy the returned URL and one-time secret into the App's **Webhooks** settings.
   The URL is `https://<instance-host>/github/app/<slug>`. Select **Active**.

4. In **Permissions & events**, set repository permissions:
   - **Metadata: Read-only**, which GitHub includes automatically.
   - **Issues: Read and write** for issue comments and reactions.
   - **Pull requests: Read and write** for pull request comments, reviews and reactions.
   - **Contents: Read-only** if repository tools need file access. Write tools need **Read and write**.
   - **Checks: Read-only** and **Actions: Read-only** if you need CI events.

   Subscribe to **Issues**, **Issue comment**, **Pull request**, **Pull request review**,
   **Pull request review comment** and **Pull request review thread**.
   For CI, also select **Check run**, **Check suite** and **Workflow run**.
   Save the changes. Add other permissions only when the selected tools or events need them.

5. Open **Install App**, select an account, and choose **Only select repositories**.
   Select the repositories and install the App. An organization owner may need to approve the installation.
6. Add the connector block from `template/connectors/github.toml` to the instance's `connectors.toml` to enable repository tools.
   Keep `GITHUB_INSTALLATION_TOKEN` in `secrets` and the `Authorization` header.
   routd derives this token from the trusted turn repository. Do not set the slot in `.env` or user secrets.
   Tool access still requires `mcp:github_<tool>` grants. Holds still require approval.
   Restart the instance with `sudo systemctl restart arizuko_<inst>` to load the configuration.

In the App's **Recent deliveries**, check that a ping receives HTTP 200.
Check **GitHub sources** at `/dash/github_sources/` to list, add or delete sources.
The dashboard lists source types and names, never secrets. When you add a source there, the next page shows its webhook URL and secret once, as the CLI does.

For each reply, gitd selects the most specific registered source: `repo/<owner>/<repo>`,
then `org/<owner>`, then `app/<slug>`. Repository and organization sources use `GITHUB_TOKEN`, a personal access token.
An App source uses an installation token for that repository. Missing App access refuses the reply without a PAT fallback.
The connector's `GITHUB_INSTALLATION_TOKEN` slot also requires an App source for the trusted repository.
The private key and installation tokens stay on the host.

To stop deliveries and remove the source's seeded routes, delete the source in the dashboard or run:

```bash
arizuko github -i <inst> rm app/<slug> <folder>
```

The webhook URL then returns 404. Source deletion does not uninstall the App or remove tool grants.
To remove GitHub access, uninstall the App under the account's **Settings → Applications → Installed GitHub Apps**.
You can also remove individual repositories from the installation.
gitd replies and App connector calls refuse access when the source or installation is missing.

GitHub setup details: [App registration](https://docs.github.com/en/apps/creating-github-apps/writing-code-for-a-github-app/building-a-github-app-that-responds-to-webhook-events),
[permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app),
[installation and removal](https://docs.github.com/en/apps/using-github-apps/reviewing-and-modifying-installed-github-apps).

## JID grammar

```
github:<owner>                    account/org-level event (no repository)
github:<owner>/<repo>             repo-level event (push, release, …)
github:<owner>/<repo>/pr/<n>      one pull request
github:<owner>/<repo>/issue/<n>   one issue
```

Owner and repo ride in the JID (`chanlib.ParseGitHubJID` /
`FormatGitHubJID`), so one App secret serving fifty repos yields fifty JIDs
and the ordinary route table maps them. The sender is `github:<login>` — the
same principal authd mints for a GitHub sign-in, so one person's dashboard
login and PR comment are one identity.

## Observe vs trigger

`add` seeds a `<folder>#{jid}` mention trigger and a
`<folder>?observe#{jid}` fallback for every chat level of the source: `pr/*`,
`issue/*`, the repo and, for an org or App source, the owner. The fragment gives each chat its full JID as the topic.
The bot's login comes from the configured App, or from `GITHUB_TOKEN` when no App is configured. A turn starts only on an
explicit ask: an @mention of the bot written by a person anywhere (PR, issue,
discussion, commit comment, project status update, commit message, CI output),
a review request or assignment to the bot login, a reply to a bot comment, or a
human comment or review on a PR or issue the bot authored.
Assignment, review requests and past reviews do not turn later PR activity into
turns.

| Event                                                                                            | Default result |
| ------------------------------------------------------------------------------------------------ | -------------- |
| PR or issue body, comment, review, discussion, commit comment or commit message names `@<login>` | Turn           |
| Completed check, suite or workflow whose title, summary, text or commit message names `@<login>` | Turn           |
| Inline PR comment directly replies to a stored bot comment                                       | Turn           |
| Human comment, inline comment or review with a body on a bot-authored PR or issue                | `mention`      |
| PR or issue assignment, or individual PR review request, targets the bot                         | Turn           |
| Push, label, close, review state or edit on a PR the bot is assigned to or reviews               | Observe        |
| Completed check, suite or workflow that does not name the bot                                    | Observe        |
| Other plain comment or discussion                                                                | Observe        |
| Assignment or review request targets someone else                                                | Observe        |
| Other issue-only, repository-only, queued or in-progress event                                   | Observe        |

A mention is the only text rule. gitd reads the text the delivery itself wrote
(a created or edited comment, an opened or edited body, a submitted review, a
completed check) and ignores the object it carries, so a label event does not
re-read the PR body. The bot's own text never asks. A completed check that names
the bot and is associated with PRs asks on each PR chat and not on the repository
row. A discussion, a commit comment, a push, a project status update or a check
with no PR asks on the repository chat (`github:<owner>/<repo>`), which has no
comment thread: gitd's `send_text` refuses it, so the agent answers through the
GitHub connector.

Human conversation on a PR or issue that the bot authored maps to `mention`.
The bot's own comments drop, and its retained metadata never becomes a mention.
A person's `message` or `mention` keeps a PR or issue engaged after a reply, as
on every channel: a human comment on an engaged PR starts a turn without a new
mention. A `check_run`, `workflow_run` or review-state row is never a `mention`,
so it does not ride engagement. It lands on the `?observe` fallback.
To let CI continue an open
conversation, add one route above the fallback, for example
`chat_jid=github:o/r/pr/* verb=check_run` to `team/reviewer?engaged#{jid}`
([`ROUTING.md`](../ROUTING.md)). gitd carries no flag for this.
Repo rows sit at seq 10/100, org at 20/200, app at
30/300, so a narrower registration wins. Operators can edit the route rows.
A source registered before issue and repository mention rows existed has only
the `pr/*` one. For a `repo/o/r` source on folder `eng` the operator adds the
others, same seq as the existing mention row (`10`; `20` for an org, `30` for an
App, with the owner row last):

```
arizuko route -i demo add 'chat_jid=github:o/r/issue/* verb=mention' 'eng#{jid}' --seq 10
arizuko route -i demo add 'chat_jid=github:o/r verb=mention' 'eng#{jid}' --seq 10
```

Subscribe to `issue_comment`, `issues`, `pull_request`, `pull_request_review`,
`pull_request_review_comment` and `pull_request_review_thread` for PR and issue
conversations and state; add `discussion`, `discussion_comment`, `commit_comment`
and `push` where a mention there must start a turn. For CI, add `check_run`, `check_suite` and `workflow_run` events.
A completed CI delivery adds one observation row per associated PR, next to the
repository row. gitd makes no GitHub API call to classify any event. A CI
delivery allows at most ten distinct PRs; more are refused with 400. Associated PR
rows use stable delivery-and-PR IDs, so routd deduplicates a replay.
Fork checks with empty PR associations remain observations.

Created comments keep their replyable object IDs. Edits, deletions and review
dismissals use delivery IDs, so their notifications survive ingestion.
Those notifications do not rewrite or remove the original stored text. gitd
sets `ReplyTo` only on a created review comment. An edit becomes a mention only
when its new text names the bot or it is a human edit on a bot-authored PR or issue.

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

`send_text`, `send_file`, `typing`, `edit` and `delete` are advertised only when
`GITHUB_TOKEN` or `GITHUB_APP_ID` is set. Turn statuses edit the posted comment
on retry and delete it on success. Both issue comments and inline review replies
use their returned comment IDs.
The typing mark follows the same source and credential rule as replies.
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
issue-comments endpoint, returning `gh-c<id>`. The topic stays
`github:<owner>/<repo>/pr/<n>` either way — it names the session, never the address. The token's own
comments are dropped at ingest (`sender.login` == the token's login), as is
other bot comments. Typed PR state and CI events from automation remain eligible.
Without either credential gitd is ingress-only: a `reply` on a
`github:` JID fails with a plain `channel github: send_text not supported`
(`chanreg/httpchan.go`), and on the MCP path `ipc/ipc.go` records the row
pending and tells the agent it is queued.

A `GET /user` that fails at boot makes gitd log an error and keep serving ingress.
Without an App ID, gitd withdraws `send_text` from the registration. Exiting would
cycle every daemon on the instance (BUGS
`L406`). What follows depends on the answer. GitHub REFUSING the token is
permanent, so nothing asks again and a new credential is the only repair.
GitHub not answering — a dropped connection, a 5xx, a rate limit — is not, so
gitd re-asks `/user` every minute and puts `send_text` back the moment it
answers; without an App ID it stays withdrawn in between because the login that drops gitd's own
comments at ingest is still unknown. `/health` reads 200 throughout: the daemon
is up and ingesting, and a `disconnected` 503 would have routd deregister the
channel after three probes and take ingress with it.

## Env

| var                       | default                  | note                                                                                   |
| ------------------------- | ------------------------ | -------------------------------------------------------------------------------------- |
| `ROUTER_URL`              | required                 |                                                                                        |
| `SECRETS_KEY`             | required                 | the keyring routd seals `github_sources` with                                          |
| `GITHUB_TOKEN`            | unset                    | enables `send_text`, `send_file`, `typing`                                             |
| `GITHUB_APP_ID`           | unset                    | GitHub App ID for App source replies                                                   |
| `GITHUB_APP_PRIVATE_KEY`  | unset                    | base64-encoded GitHub App RSA private key in PEM format                                |
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
refused by routd, `503` neither routd nor retention took the message —
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
