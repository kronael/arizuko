---
status: shipped
depends:
  [
    ../5/34-channel-protocol,
    ../5/13-ext-mcp,
    ../5/19-hitl-firewall,
    26-personal-mode,
    ../5/Q-unified-routing,
    ../5/31-identity-pairing,
    ../5/35-hermes-integration,
  ]
---

# specs/6/27 — gitd: GitHub as an ingress channel

> A repository's events reach the agent as messages on JIDs the route table
> already understands. gitd is **ingress only**; everything the agent does
> BACK to the repo goes through the GitHub MCP connector — not through channel
> verbs. Shipped 2026-09-05: `gitd/`, the `github_sources` registry, the
> `arizuko github` CLI.

## Decision 1 — ingress and egress are two different mechanisms

- **Ingress is a channel.** gitd receives webhooks, maps each event to a
  `chanlib.InboundMsg` and POSTs it to routd. That is the whole daemon.
- **Egress is tooling.** Opening a PR, merging, labelling, closing, requesting
  review — repo STATE changes — arrive as MCP tools from the GitHub connector
  ([`5/13`](../5/13-ext-mcp.md) shape 2, `template/connectors/github.toml`),
  gated by `mcp:<tool>` grants, brokered credentials, audit rows, and — the
  reason this matters — holdable by `hold:mcp:<tool>`
  ([`5/19`](../5/19-hitl-firewall.md)).

**The test that draws the line: does the act produce a MESSAGE in the thread
the agent is speaking in, or does it change the repo?** A comment is a
message. A merge is not, even though it carries text. A review is a state
change with a comment attached, so it is tooling.

Putting merge in the channel would put it outside the hold gate, because the
gate sits on the MCP socket. Keeping it in the connector is what lets
`hold:mcp:github_merge_pull_request` exist at all.

## Decision 2 — gitd implements `send_text`, and nothing else

Verbs are capability-gated (`chanreg.Entry.HasCap`): gitd advertises ONE of
`chanlib.BotHandler`'s fourteen, and every other verb returns
`chanlib.Unsupported`. `send_text` posts a comment on the issue or PR the JID
names (GitHub keeps both on `/repos/{o}/{r}/issues/{n}/comments`) and returns
`gh-c<id>`, the id the same comment carries inbound, so a later `reply_to`
resolves to the agent's own row.

`send_text` is advertised **only when `GITHUB_TOKEN` is set** (`gitd/main.go`
`capsFor`). Without it gitd is ingress-only and the agent's `reply` on a
`github:` JID gets a clean Unsupported hint instead of a failed call. The token
is a PAT or a fine-grained token; gitd holds no App private key (Decision 5).

`edit`, `delete` and `like` are not implemented: the connector already
reaches those endpoints under `mcp:` grants and the hold gate, and a second
path to one act is the duplication this repo forbids.

## Decision 3 — the JID grammar IS the routing model

The scheme is **`github:`**, not a host-neutral `git:`. Two measured
constraints force it: routd's ingress guard (`routd/server.go` handleMessages)
rejects a sender whose scheme no registered JID prefix shares
(`chanreg.Entry.OwnsScheme`), and authd mints a GitHub sign-in as
`github:<login>` (`authd/oauth.go` dispatch with `auth.FetchGitHubUser`).
Decision 5 requires the webhook sender to BE that principal, so the JID scheme
must be `github:`. Scheme = OAuth provider name, so the sender/JID agreement
holds by construction.

```
github:<owner>                    account/org-level event (no repository)
github:<owner>/<repo>             repo-level event (push, release, CI, …)
github:<owner>/<repo>/pr/<n>      one pull request     topic=pr/<n>
github:<owner>/<repo>/issue/<n>   one issue            topic=issue/<n>
```

`chanlib.ParseGitHubJID` / `FormatGitHubJID` own the grammar (the
`SlackJID` precedent); nothing hand-splits the string.

**The installation identity lives in the JID, never in a mapping table.** An
org hook or an App installation delivers fifty repos on one secret; each
delivery names its repo in `repository.full_name`, so each yields its own
JID and the ordinary route table maps it. A repo created later under the same
installation simply starts producing a new JID — nothing is provisioned for it.
Verified: `gitd/event_test.go` `TestMapEvent_TwoReposOneSecret` and
`gitd/server_test.go` `TestWebhook_TwoReposOneSecret` drive
`issue_comment_created.json` (kronael/arizuko PR 12) and
`issue_comment_created_crackbox.json` (kronael/crackbox issue 7) through one
`app/` source and get `github:kronael/arizuko/pr/12` and
`github:kronael/crackbox/issue/7`; a later `repo/kronael/crackbox` registration
to a second folder pulls only the crackbox JID there.

Three properties fall out of the existing route table
([`5/Q`](../5/Q-unified-routing.md), `ROUTING.md`) with no new machinery:

1. **Every repo can be its own route.** `chat_jid=github:kronael/arizuko` →
   one folder; a second repo routes to a second folder, and they never share
   a queue.
2. **Every PR can be its own route.** Routes are seq-ordered globs, so a
   repo-wide rule at seq 100 gives the default and a rule at seq 10 pulls one
   hot PR into a different folder.
3. **A PR is its own thread only because gitd stamps a topic.** `sessions` is
   keyed `(group_folder, topic)` and dispatch groups a batch by
   `Message.Topic`, so two PRs routed to one folder with an empty topic would
   SHARE one session. gitd sets `Topic` from the JID (`GitHubJID.Topic`) on
   every event — `pr/<n>`, `issue/<n>`, empty at repo and owner level.

`reply_to` is `gh-c<in_reply_to_id>` on a review-comment reply, and the
existing reply rules take over.

## Decision 4 — events are ordinary messages; the route selects observe vs trigger

Every event rides the one inbound shape: an `InboundMsg` whose `verb` names
the event and whose body is a rendered one-liner. gitd stamps
(`gitd/event.go` `mapEvent`):

| event                                   | verb                              | JID level          |
| --------------------------------------- | --------------------------------- | ------------------ |
| `issue_comment` created                 | `message`                         | pr/issue           |
| `pull_request_review_comment` created   | `message` (+ `reply_to`)          | pr                 |
| `pull_request_review` submitted w/ body | `message`                         | pr                 |
| `issues` opened/reopened                | `message`                         | issue              |
| `pull_request` opened/reopened/ready    | `message`                         | pr                 |
| everything else                         | the GitHub event name (`push`, …) | repo/owner or item |

Whether an event fires a turn is the ROUTE's choice, not gitd's: a
`<folder>#observe` target ingests with `is_observed=1` and no turn. `verb` is a
first-class route predicate, so the operator selects observe-versus-trigger
along both axes. Registration (Decision 5) seeds this split
(`store.GitHubSourceRoutes`, the one renderer):

```
seq  match                                                 target
10   chat_jid=github:<o>/<r>/pr/*    verb=message          <folder>
10   chat_jid=github:<o>/<r>/pr/*    verb=mention          <folder>
10   chat_jid=github:<o>/<r>/issue/* verb=message          <folder>
10   chat_jid=github:<o>/<r>/issue/* verb=mention          <folder>
100  chat_jid=github:<o>/<r>/pr/*                          <folder>#observe
100  chat_jid=github:<o>/<r>/issue/*                       <folder>#observe
100  chat_jid=github:<o>/<r>                               <folder>#observe
```

An `org/<o>` source seeds the same rows on `github:<o>/*` at seq 20/200 plus
`github:<o>` observe; an `app/<slug>` source on `github:*/*` at 30/300 plus
`github:*`. Narrower registrations win by seq. `verb=mention` is what routd
promotes a human message to once it replies to a bot row or lands in a thread
the bot spoke in, so the agent keeps a conversation it joined. Drop a row and
that event class becomes context; that one-row edit is the whole knob.
Verified: `TestMapEvent_ObserveVersusTrigger` resolves the seeded rows through
`router.ResolveRouteTarget` — push, merge and ping → `#observe`; a comment, a
new PR, a mention → trigger.

> **There is no subtree glob; write one catch-all row per level.** Route
> patterns go through `path.Match`, where `*` never crosses `/`. The grammar
> has four levels, so the renderer writes a row per level.

**A busy repo must never be a turn generator**: fifty pushes an hour cost zero
turns and still surface in the next turn's `<observed>` block.

> **Engagement outranks `#observe`, by design.** `resolve` consults the live
> engagement record BEFORE the route's mode, so once the agent has answered on
> a PR's JID+topic, later events there DO start turns for the engagement TTL.
> Bounded by JID+topic: repo-level pushes land on the repo JID and are never
> pulled in.

## Decision 5 — delivery sources, and the identity rules that follow

GitHub delivers the same HMAC-signed POST (`X-Hub-Signature-256`,
`X-GitHub-Event`, `X-GitHub-Delivery`) from three places, so gitd has **one
receiver, one verification, one mapping** and a registry of _delivery
sources_:

| kind   | covers                                             | operator pastes URL + secret into |
| ------ | -------------------------------------------------- | --------------------------------- |
| `repo` | one repository                                     | repo Settings → Webhooks          |
| `org`  | every repo the org owns (org-owner rights)         | org Settings → Webhooks           |
| `app`  | every repo of every installation, incl. later ones | the GitHub App's webhook settings |

A personal account cannot carry a webhook, so covering all of its repos is the
App case. An App is registered once per arizuko instance (its webhook URL is
the instance's host), then installed on any account or org.

**gitd needs only the webhook secret.** The App's private key authenticates
API calls as the App; this adapter is ingress-only and the connector holds its
own token, so no second credential enters gitd. Consequence: with
`GITHUB_TOKEN` set, the agent's comment is posted by that token's user, not as
`<app>[bot]`.

**The path selects the secret; the payload never does.** The URL is
`/github/<kind>/<name>` (`repo/<owner>/<repo>`, `org/<owner>`, `app/<slug>`).
`github_sources` (routd.db, routd migration 0041) holds one row per source:
the key, the secret sealed under `SECRETS_KEY` exactly like a `secrets` value,
the owning folder. gitd mounts `store/routd/` and unseals per delivery; it
never writes. A `repo`/`org` source is then bound to its scope
(`gitd/event.go` `checkSource`): a valid signature over a payload naming
another repo is a 400, so a collaborator who knows repo X's secret cannot
address repo Y's folder. An `app` source must carry `installation` (its ping
precedes any install and is exempt).

**Provisioning is one resreg resource** (`github_sources`, CLAUDE.md "every
cold-tier management entity"): `POST/GET/DELETE /v1/github_sources` on routd,
the agent tools `register_github_source` / `list_github_sources` /
`revoke_github_source` (default-deny like `issue_webhook`), and
`arizuko github <inst> add|list|rm`. Register mints the secret, seals it,
inserts the row and the route rows of Decision 4 in one transaction and returns
`{source, url, secret}` once; the secret is in no list, log line or audit row.
Revoke deletes the row and the seeded routes; the URL 404s immediately.

Two identity rules gitd inherits:

- **The sender is `github:<sender.login>`** — the principal authd mints for a
  GitHub sign-in, so one person's dashboard login and PR comment are one
  identity and grants apply to both. gitd registers the `github:` prefix and
  so owns that scheme (`Entry.OwnsScheme`); a compromised adapter can still
  claim any GitHub user, which is why a GitHub identity holds no grants until
  an `acl_membership` edge is paired ([`5/31`](../5/31-identity-pairing.md)).
- **The folder is READ from route resolution, never parsed out of the JID**
  (BUGS `L4`). gitd never sees a folder.

## Honest gaps

- **GitHub logins are mutable.** A user rename silently transfers
  `github:<old-login>` — and every grant on it — to whoever claims the old
  name; the numeric `sender.id` is stable but is not what authd mints. gitd
  matches authd for one identity rather than inventing a second shape.
- **Bot loops.** The agent's own comment fires `issue_comment.created`. gitd
  drops `sender.type == "Bot"` and any sender equal to `GITHUB_TOKEN`'s login
  (learned from `GET /user` at start) at ingest — the only place that can.
- **Webhook delivery loss.** GitHub does not retry a failed delivery on its
  own; the operator redelivers from the "Recent deliveries" tab. gitd covers
  the routd side: a delivery routd cannot take is retained in `gitd.db`
  (`chanlib.RetainInbound`) and answered `202`, re-delivered by the sweep. A
  gitd outage is the operator's redeliver.
- **Replay.** GitHub signs no timestamp. A replayed delivery re-posts a
  message whose id (`gh-c<comment id>`, `gh-d<delivery guid>`) routd already
  holds, so it is an `INSERT OR IGNORE` no-op — the id, not the signature, is
  the replay defence.
- **`.github/workflows/` stays read-only** unless a grant explicitly opens it.
  An agent that can edit its own CI can edit its own gate.

## Architecture

A standard `5/34` adapter, packaged per `5/7`/`5/8`: `template/services/gitd.yml`

- `gitd.yaml` (`/github/` public with the three GitHub headers preserved,
  `/dash/gitd/` for operators). `gitd/` — `verify.go` (HMAC), `event.go`
  (payload → `InboundMsg`), `server.go` (`POST /github/{source...}`), `bot.go`
  (`send_text`, dash section). `store/github_sources.go` — the registry, its
  sealing and the route renderer. `resreg/resources/github_sources.go` +
  `routd/github_sources_resource.go` — the REST/MCP faces.
  `cmd/arizuko/github.go` — the CLI.

## Acceptance

- A comment on a PR reaches the agent as `verb=message` on
  `github:<owner>/<repo>/pr/<n>` with `topic=pr/<n>` — **fixture-verified**
  (`TestMapEvent_Fixtures`, `TestWebhook_PushDelivered`). The agent's answer
  lands as a comment on that PR — **API-shape verified against a fake GitHub**
  (`TestSend_PostsComment`).
- Two PRs on one repo hold two independent conversations: each event carries
  its own topic — **fixture-verified**; the two `sessions` rows are routd
  behaviour.
- A push to a repo routed `#observe` creates NO turn — **verified through
  `router.ResolveRouteTarget`** on the seeded rows.
- A route row at a lower seq pulls one PR into a different folder — route
  table property, verified for the repo-over-app case
  (`TestMapEvent_TwoReposOneSecret`).
- Two repos on one secret produce two JIDs and route independently —
  **verified** (`TestWebhook_TwoReposOneSecret`).
- A forged or missing signature is refused with 401 and nothing reaches routd
  — **verified** (`TestVerifySignature`, `TestWebhook_RejectsBadSignature`).
- `github_merge_pull_request` is a connector tool, not a channel verb, and
  `hold:mcp:github_merge_pull_request` suspends it — unchanged from `5/19`.
- gitd advertises exactly `send_text`; every other verb returns `Unsupported`
  without a network call — **verified** (`TestCapImplConsistency`).
