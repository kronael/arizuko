---
status: draft
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

# specs/6/27 — gitd: a Git host as an ingress channel

> A repo's events reach the agent as messages on JIDs the route table already
> understands. gitd is **ingress only**; everything the agent does BACK to
> the repo goes through the GitHub MCP connector — `gh` and its relatives —
> not through channel verbs. GitHub first; GitLab and Gitea are the same
> shape behind the same `git:` prefix.

## Decision 1 — ingress and egress are two different mechanisms

An earlier draft of this file got this wrong by putting
`review`/`label`/`close`/`merge` in the chanlib verb registry.

- **Ingress is a channel.** gitd receives webhooks, maps each event to a
  `chanlib.InboundMsg` and POSTs it to routd. That is the whole daemon.
- **Egress is tooling.** Opening a PR, merging, labelling, closing,
  requesting review — repo STATE changes — arrive as MCP tools from the
  GitHub connector ([`5/13`](../5/13-ext-mcp.md) shape 2,
  `template/connectors/github.toml`), gated by `mcp:<tool>` grants, brokered
  credentials, audit rows, and — the reason this matters — holdable by
  `hold:mcp:<tool>` ([`5/19`](../5/19-hitl-firewall.md)).

**The test that draws the line: does the act produce a MESSAGE in the thread
the agent is speaking in, or does it change the repo?** A comment is a
message. A merge is not, even though it carries text. A review is a state
change with a comment attached, so it is tooling.

Putting merge in the channel would put it outside the hold gate, because the
gate sits on the MCP socket. Keeping it in the connector is what lets
`hold:mcp:github_merge_pull_request` exist at all — and merge is precisely
the verb `6/26`'s four verdicts were designed for.

## Decision 2 — gitd implements `send_text`, and nothing else

Verbs are capability-gated (`chanreg.Entry.HasCap`): gitd advertises ONE of
`chanlib.BotHandler`'s fourteen, and every other verb returns
`chanlib.Unsupported`. `send_text` posts a comment on the issue or PR the JID
names, and it is not optional — routd delivers the agent's `reply` through the
registered channel, so without it an agent triggered by a PR comment has
nowhere to answer. That one API call is the whole of the messaging gitd
reimplements.

An earlier draft advertised `edit`, `delete` and `like` as well. They are cut.
The GitHub connector already reaches those endpoints under `mcp:` grants and
the hold gate, and a second path to one act is the duplication this repo
forbids. The cost is uniformity — the agent's generic `like` works on Slack and
Telegram and returns `Unsupported` on a `git:` JID — and we pay it until
somebody needs the reaction badly enough to say so.

> **Prerequisite (BUGS `L94`).** `Like` and `Post` are the only two verbs in
> `chanreg/httpchan.go` with no `HasCap` guard, so today they reach the adapter
> whatever it advertises. gitd is the first adapter that will not implement
> them, so the agent would read a 404 transport error where it should read
> "this platform does not do that". Add the guards before gitd ships.

## Decision 3 — the JID grammar IS the routing model

Host-neutral, with `github.com` elided as the default:

```
git:<owner>/<repo>                 repo-level events (push, release, CI, stars)
git:<owner>/<repo>/pr/<n>          one pull request
git:<owner>/<repo>/issue/<n>       one issue
git:gitlab.com/<owner>/<repo>/...  a non-default host
```

Three properties fall out of the existing route table
([`5/Q`](../5/Q-unified-routing.md), `ROUTING.md`) with no new machinery:

1. **Every repo can be its own route.** `chat_jid=git:kronael/arizuko/**` →
   one folder. A second repo routes to a second folder, and the repos never
   share a queue.
2. **Every PR can be its own route.** Routes are seq-ordered globs, so a
   repo-wide rule at seq 100 gives the default and a rule at seq 10 pulls one
   hot PR into a different folder. A PR is routable WITHOUT being routed by
   default.
3. **A PR is its own thread only because gitd stamps a topic.** A distinct
   JID buys a distinct chat cursor, sticky state, engagement record and reply
   anchor — but NOT a distinct session. `sessions` is keyed
   `(group_folder, topic)` (`routd/db.go:1003`, `:1082`) and dispatch groups a
   batch by `Message.Topic` (`routd/dispatch.go:114`), so two PRs routed to one
   folder with an empty topic SHARE one session and one lineage. gitd therefore
   sets `InboundMsg.Topic` on every event — `pr/<n>`, `issue/<n>`, empty at
   repo level — and the isolation follows from the field that already drives
   it. One field set at ingest; no new machinery.

Per-PR sticky routing then needs no special case: `reply_to_id` is the GitHub
comment id, and the existing reply rules take over.

## Decision 4 — events are ordinary messages; the route selects observe vs trigger

A push, a CI result, a label change, a release are not anybody talking — but
that is no Git specialty; every channel has non-message events, and they all
ride the one inbound shape: an `InboundMsg` whose `verb` names the event,
with a rendered one-line body (`chanlib/chanlib.go:51` — reactions already
ride `Verb: "like"`; `5/34` deliberately defines no generic event endpoint).
Whether an event fires a turn is the ROUTE's choice, not gitd's: a
`<folder>#observe` target ingests with `is_observed=1` and no turn
(`routd/loop.go:614`, `ROUTING.md:165`).

gitd's contribution is only the two things a route can key on: which JID an
event lands on, and which verb it carries. Item events (comments, reviews,
labels, merges) land on that issue or PR's JID; repo-scoped events (push, CI
runs, releases) land on the repo JID. **`verb` is a first-class route
predicate** — `router.routeMatches` evaluates it like any other field, and
`RouteMatchesIgnoreVerb` exists precisely because it is one; `arizuko` already
ships `room=<x> verb=mention` rows (`cmd/arizuko/main.go:468`). So the operator
selects observe-versus-trigger along BOTH axes without touching gitd:

```
seq  match                                              target
10   chat_jid=git:kronael/arizuko/pr/* verb=reply       eng/arizuko
10   chat_jid=git:kronael/arizuko/pr/* verb=message     eng/arizuko
20   chat_jid=git:kronael/arizuko/pr/* verb=ci_status   eng/arizuko
100  chat_jid=git:kronael/arizuko/pr/*                  eng/arizuko#observe
100  chat_jid=git:kronael/arizuko/issue/*               eng/arizuko#observe
100  chat_jid=git:kronael/arizuko                       eng/arizuko#observe
```

Read it as: a human comment or a new PR triggers, a CI result on a PR triggers,
and everything else — pushes, labels, releases, reactions — is context. Drop
the seq-20 row and CI results become context too. That one-row edit is the
whole knob.

> **There is no subtree glob; write one catch-all row per level.** Route
> patterns go through `path.Match`, where `*` never crosses `/` — `**` is two
> of those and matches ONE slash-free segment, never a subtree. Measured:
> `git:kronael/arizuko/**` matches `git:kronael/arizuko`, `.../pr/12` and
> `.../issue/3` — **none of them**. A `**` row is not a wide rule, and it fails
> silently as a route miss. The grammar has exactly three levels, so three rows
> cover it. (`auth`'s scope matcher DOES implement recursive `**`
> (`auth/acl.go:60`); the route matcher does not, and that difference is the
> trap.)

**A busy repo must never be a turn generator**: fifty pushes an hour cost zero
turns and still surface in the next turn's `<observed>` block. The failure mode
is a container per push, and the route table is where the operator prevents it,
per repo.

> **Engagement outranks `#observe`, by design.** `resolve` consults the live
> engagement record BEFORE the route's mode (`routd/loop.go:709`), so once the
> agent has answered on a PR's JID+topic, later events there — a CI result, a
> label — DO start turns for the engagement TTL, route notwithstanding. That is
> the behaviour we want (the agent stays in the conversation it joined) and it
> is bounded by JID+topic: repo-level pushes land on the repo JID and are never
> pulled in. Operators must know it, because "observe" reads as absolute and is
> not.

## Decision 5 — GitHub App, and the identity rules that follow

**App over PAT.** A PAT leaks the granting user's identity into every audit
trail; an App appears as `<app>[bot]` and scopes per repo. The App's
short-lived installation token is what the connector uses for state changes.
PAT stays a fallback for an operator wiring webhooks by hand.

Two identity rules gitd inherits and must not break
([`5/35`](../5/35-hermes-integration.md) § Known gaps):

- gitd owns the `git:` scheme and MUST NOT assert a sender outside it.
  `Entry.OwnsScheme` enforces this; a compromised adapter can still claim any
  GitHub user, which is why a GitHub identity holds no grants until an
  `acl_membership` edge is paired ([`5/31`](../5/31-identity-pairing.md)).
- The folder is READ from the route resolution, never parsed out of the JID.
  Deriving a folder from a JID is a recorded bug class with four instances and
  two cross-tenant writes (BUGS `L4`).

## Honest gaps

- **Bot loops.** The agent's own comment fires `issue_comment.created`. gitd
  drops it at ingest on `sender.type == "Bot"` plus the App slug — ingest is
  the only place that can. (An earlier draft added "rate-limit with the
  engagement TTL": the TTL decides ROUTING, not rate (`routd/db.go:640`), and
  engagement makes loops MORE likely, not less — see Decision 4.)
- **Webhook delivery loss.** GitHub retries 8× then drops. Defence is a
  periodic reconcile poll that diffs known issue state against the API. Behind
  NAT with no public endpoint, polling is the only ingest — strictly worse, but
  it unblocks development.
- **`.github/workflows/` stays read-only** unless a grant explicitly opens it.
  An agent that can edit its own CI can edit its own gate.

## Architecture

A standard `5/34` adapter, packaged per `5/7`/`5/8`: a
`template/services/gitd.yml` fragment plus a `gitd.yaml` manifest whose
`proxyd_routes` block exposes the inbound webhook path. gitd-specific work
is the webhook HMAC verification; per-repo persistence is ordinary folder
state (`facts/`, `diary/`, `users/` keyed to GitHub handles, a repo-specific
`PERSONA.md`).

## Acceptance

- A comment on a PR reaches the agent as a `reply` on
  `git:<owner>/<repo>/pr/<n>`, and the agent's answer lands as a comment on
  that PR.
- Two PRs on one repo hold two independent conversations: each event carries
  `topic=pr/<n>`, the two topics resolve to two different `sessions` rows, and
  neither transcript contains the other's text.
- A push to a repo routed `#observe` creates NO turn and IS visible in the
  next turn's `<observed>` block.
- A route row at a lower seq pulls one PR into a different folder, and the
  repo's other PRs still route to the default.
- `github_merge_pull_request` is a connector tool, not a channel verb, and
  `hold:mcp:github_merge_pull_request` suspends it for approval.
- gitd advertises exactly `send_text`; every other verb returns `Unsupported`
  without a network call — `like` and `post` included, which is BUGS `L94`.

## Out of scope

GitLab / Gitea daemons (same shape, later) · line-anchored review comments
(top-level only in v1) · org-level events · cross-repo coordination (each repo
is its own folder; coordinate by mentioning a sibling).
