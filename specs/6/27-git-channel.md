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
    ../5/F-topic-lineage,
  ]
---

# specs/6/27 — gitd: GitHub channel and repository tools

Repository events reach groups through ordinary routes. gitd receives signed webhooks
and posts replies as comments. Repository state changes use the GitHub MCP connector.
App webhook ingress and repository-qualified route topics have implementation tests.
`routd/topic_fanout_test.go` proves separate sessions for two repositories' PR 12.
Installation-token access through the broker binds to the trusted turn repository.
The App completion has never run on a credentialed disposable repository: BUGS `L907` holds
that acceptance, and BUGS `L409` holds the temporary `GITHUB_APP_ACCEPTS_REPO` bypass.

## Channel messages and repository tools

A comment is a message in the conversation. A merge, label, or review changes repository
state. `gitd/bot.go` implements `send_text`, `send_file` and the typing indicator below;
`gitd/main.go:capsFor` advertises those three plus `edit` and `delete` when a PAT or App ID is set. The selected identity posts the comment and the returned identity
is `gh-c<id>`, or `gh-rc<id>` for an inline review comment, matching inbound comment IDs.
`Edit` and `Delete` address the same IDs with the same identity; GitHub refuses a change to
another account's comment, and a delete of an absent comment counts as done.
Without either credential, gitd is ingress-only.
Other channel verbs return Unsupported.

> The channel creates, edits and reacts to ITS OWN posts in the thread it was
> addressed in. Every change to another party's state — resolve, reopen,
> merge, label, a new thread, the document body — is the connector's, under
> its own grant and hold.

gitd advertises `edit` and `delete`, so routd keeps one turn status comment on GitHub:
it posts the comment, edits it with progress, retries and the final cause, and deletes it
when the turn succeeds (`specs/6/46-turn-progress.md` §"The sink's declaration"). The `👀`
mark below shows that the turn started.

`SendFile` posts a text file as a fenced block in a comment under the same rules as
`send_text`, because GitHub has no API that attaches a file to a comment. A binary file is
`Unsupported`, and one that makes a comment over 65536 characters is `ErrInvalidRequest`, both
before any call and both naming the `github` connector as the way to commit the file instead.

State changes use `template/connectors/github.toml` through the hosted HTTP broker
in `ipc/connector.go`. Each tool uses its `mcp:github_<tool>` grant and the socket's
`hold:mcp:<tool>` policy. A held merge re-enters the ordinary authorization path after
approval. A second channel path for repository mutations would bypass that boundary.
Credentials resolve on the host and results scrub their values
([`13-ext-mcp.md`](../5/13-ext-mcp.md), [`19-hitl-firewall.md`](../5/19-hitl-firewall.md)).

## Typing indicator

While a turn runs, gitd shows an 👀 (`eyes`) reaction on the comment that started it
and removes it when the turn ends, not when the first reply lands. chanreg sends
`/typing` only to an adapter that advertises `typing`, so gitd advertises it with
`send_text`. The mark is `chanlib.AckMark`: which comment it stands on, how it
persists, the backstop and the boot sweep are specified once in
`specs/6/46-turn-progress.md` §"The mark, implemented once". gitd supplies only the
two GitHub calls, in `gitd/typing.go`:

- Conversation comment (`gh-c`): `POST /repos/{o}/{r}/issues/comments/{id}/reactions`,
  then `DELETE` on that path plus `/{reaction_id}`.
- Inline review comment (`gh-rc`): `POST /repos/{o}/{r}/pulls/comments/{id}/reactions`,
  then `DELETE` on that path plus `/{reaction_id}`.

Create answers 201, or 200 when the reaction already stands. Delete answers 204, and
404 (the reaction or its comment already gone) counts as removed. The token needs
Issues write for the first row and Pull requests write for the second
(docs.github.com/en/rest/reactions/reactions). A trigger with no reactable comment (a
review, an issue or PR body) gets no mark, and never the 👀 of an older comment. A
refusal from GitHub is logged at warn with GitHub's reason and goes no further: the
port returns nothing, the reply is the product, and the indicator is decoration.

## Sources and identity

Repository, organization, and App hooks share one receiver, signature verifier, and
event mapper: `gitd/server.go`, `gitd/verify.go`, and `gitd/event.go`.
The `/github/<kind>/<name>` path selects the registered signing secret before mapping.
The payload never selects its verifier. `checkSource` binds a repo source to its full
repository name and an org source to its owner. An App delivery requires an installation
object, except for ping; the current check does not validate a positive installation ID.
`GITHUB_APP_ACCEPTS_REPO=1` lets an App source also accept a repository delivery with no
installation; the secret still has to verify. It is the temporary bypass BUGS `L409`
records.

`store/github_sources.go` owns sealed source secrets and the route renderer.
gitd reads sealed secrets from routd's store through `store.OpenOwner`.
Provisioning uses the existing `github_sources` resreg resource in
`routd/github_sources_resource.go` and `resreg/resources/github_sources.go`.
The CLI is `cmd/arizuko/github.go`. Registration returns the URL and raw secret once;
list and audit output omit the secret. Revocation removes the source and matching seeded
routes. The deleted webhook URL returns 404.

The sender is `github:<sender.login>`, matching GitHub sign-in identity in `authd/oauth.go`.
That agreement lets paired grants apply to both surfaces
([`31-identity-pairing.md`](../5/31-identity-pairing.md)). The registered channel owns the
`github:` scheme. Route resolution selects the group; gitd does not derive a folder.
The webhook URL selects a signing source, not a folder supplied by the payload.
Registration seeds that source's routes; an explicit repository route can override
an App's broad default without changing the webhook URL. An App URL requires an App
delivery unless `GITHUB_APP_ACCEPTS_REPO=1`; a repository webhook uses a registered
`repo/<owner>/<repo>` source.

## Conversations and routing

`chanlib/github_jid.go` owns these JIDs:

```text
github:<owner>
github:<owner>/<repo>
github:<owner>/<repo>/pr/<n>
github:<owner>/<repo>/issue/<n>
```

Topic selection follows [`34-channel-protocol.md`](../5/34-channel-protocol.md)
§"Route targets". gitd leaves `Topic` empty.
`store.GitHubSourceRoutes` seeds `<folder>#{jid}` for mention triggers and
`<folder>?observe#{jid}` for fallbacks at every chat level. The same targets
apply to existing GitHub routes at upgrade when their target has no fragment;
explicit topic pins retain their meaning.

`store/sessions.go` keys conversations by folder and topic. The full JID
separates two repositories' PR 12 even when both route to one group.
Each full JID selects its own session. The routing migration does not move
or merge session transcripts under another topic.

`mapEvent` renders an explicit ask as `mention`, whatever the event kind: a bot
@-mention in text a person wrote (a created or edited comment, an opened or edited
PR or issue body, a submitted review, a discussion or discussion comment, a commit
comment, a project status update, a commit message in a push, the title, summary or
text of a completed check or workflow run), a PR or issue assignment to its login,
or a PR review request to it. routd promotes an inline reply to a stored bot comment.
gitd sets `ReplyTo` only on a `created` review comment.
Human comments, inline review comments and submitted reviews with a body on a
PR or issue authored by the bot also render `mention`.
A turn starts on an explicit ask only. Being assignee, requested reviewer or a past
reviewer of a PR is not an ask: later pushes, label changes, closures, review states
and CI results that name no one stay observations. Team review requests do not match an
individual login. The bot's own text never asks. A completed check that names the bot
and carries PR associations asks on each PR chat, not on the repository row beside them.
Events with no issue or PR (discussion, commit comment, push, status update, a check
with no PR) ask on the repository chat `github:<owner>/<repo>`, which has no comment
thread, so the agent answers through the GitHub connector.
`store.GitHubSourceRoutes` seeds, for each chat level of the source (`pr/*`,
`issue/*`, the repository and, for an org or App source, the owner), a mention trigger
and an observe fallback.
Engagement follows the rule of `ROUTING.md` §"A target query sets the mode": a person's
`message` or `mention` keeps a conversation engaged, and only those two verbs ride
engagement, on GitHub as on every channel. A human comment on an engaged PR or issue
chat starts a turn without a new mention. An automation row that is not promoted
to `mention` rides no engagement and lands on the seeded `?observe#{jid}` fallback.
An operator extends engagement to such rows with one route above the fallback, for
example `chat_jid=github:o/r/pr/* verb=check_run` to `sloth/morha?engaged#{jid}`: that row
dispatches while the chat and topic are engaged and is observed otherwise. gitd carries
no flag for this; the route table decides.
Bot comments drop; typed PR state and CI events from automation remain eligible
and keep their event verb.

Completed `check_run`, `check_suite` and `workflow_run` deliveries use their
`pull_requests` associations. gitd adds one observation row per associated PR, with
an ID containing the delivery and PR number. Repository-only, queued and
in-progress events add none. gitd calls no GitHub API to classify an event. At most
ten distinct PRs fan out per delivery; excess associations return a visible 400
before dispatch. This reader creates no watch table or repository binding.

Comment creation retains its replyable object ID. Edit, delete and review-dismissal
notifications use delivery IDs and current action timestamps, so redelivery deduplicates
without suppressing a later action. Notifications retain history; they do not rewrite
or remove the original comment row. Without a resolved bot login, gitd cannot
classify mentions.
Narrow repo routes precede org
routes and the App default. Operators change that split through the existing route table.
Route globs use `path.Match`; `*` does not cross `/`.

An inline review comment is a thread, and `SendRequest.ReplyTo` is how gitd addresses it:
the topic is the PR JID, one session per pull request. Review comments carry the `gh-rc<id>`
namespace inbound and outbound because GitHub keeps them in a different id space behind a
different endpoint, and a bare number cannot say which. `gitd/bot.go` reads that prefix: it
resolves the comment that opened the thread — GitHub takes only that one as a reply target —
and posts to `pulls/<n>/comments/<root>/replies`. Every other reply uses the issue-comments
endpoint. A missing or foreign thread is reported, never redirected to the conversation tab.
`SendRequest.ThreadID` carries the turn's topic, which is a session key and not an address,
so gitd does not read it.

## Location header

Every GitHub message that gitd maps opens with one header line, so the agent reads
where a message sits without a second API call. One renderer in `gitd/event.go` builds
it once per mapped message, after the message id is final, and the CI fan-out in
`gitd/ci.go` uses the same renderer. Built in `76040b8a`; the `reply` rule below in
`279cc523`.

````text
[github kind=pull_request_review_comment id=gh-rc5678901234 thread=gh-rc5678901200 review=gh-r6789012345 path=gitd/verify.go lines=10-12 side=RIGHT url=https://github.com/kronael/arizuko/pull/12#discussion_r5678901234]
```diff
<the last 20 lines of diff_hunk>
```

<body>
````

| Key      | Value                                                                                                                        |
| -------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `kind`   | GitHub's event name for the object, with no gitd vocabulary                                                                  |
| `id`     | the message id of this row (`gh-c<id>`, `gh-rc<id>`, `gh-r<id>`); it is the value `replyToId` takes                          |
| `thread` | review comments only: `gh-rc<in_reply_to_id>`, or the comment's own id for a root                                            |
| `review` | review comments and reviews: the `gh-r<id>` of the review                                                                    |
| `state`  | reviews only: GitHub's review state                                                                                          |
| `path`   | review comments only: the file path                                                                                          |
| `lines`  | review comments only: `<start_line>-<line>`, or `<line>` for one line; `original_*` with `outdated=true` when `line` is null |
| `side`   | review comments only: `LEFT` or `RIGHT`                                                                                      |
| `url`    | `html_url` of the object                                                                                                     |

An event without a key leaves it out. The fenced hunk follows for a review comment,
including a comment whose body is an empty `suggestion` block. The CI fan-out header
carries the per-PR row id of the delivery.

routd reads a command past the header and the hunk: slash commands, sticky
`#topic`/`@group` navigation and `@child` delegation all match the text the person
typed (`typedText` in `routd/steer.go`). A delegated or re-filed message keeps the
header, so the turn that takes it can name the comment it answers.

`reply` on a `github:<owner>/<repo>/pr/<n>` or `github:<owner>/<repo>/issue/<n>` chat
requires `replyToId`: the `id` of the message the agent answers. The `reply` handler in
`ipc/ipc.go` refuses a call without it, sends nothing and writes no outbound row. The
error text is:

> reply on a GitHub chat needs replyToId: the id of the message you answer (gh-rc<id>
> answers in that code thread, gh-c<id> in the conversation)

No other channel needs the argument; they keep the fallback to the topic's last reply.
Only the `reply` handler applies that fallback to a send. routd's own notices
send without a reply target, so the rule cannot live in gitd.

## App completion

Both credential modes are required. The operator chooses per source: a `repo/` or `org/` source replies and calls tools
with the PAT in `GITHUB_TOKEN`; an `app/<slug>` source uses App installation
tokens for both. No setting outside the source registration selects between them.
For a reply, gitd checks registered sources for the repository in order: an exact
`repo/<owner>/<repo>`, then `org/<owner>`, then an `app/<slug>`. The first two
use the PAT; the App uses its repository installation or refuses. With no source,
gitd refuses the reply.

One App belongs to one instance. Personal accounts and organizations install that App
on selected repositories. Its webhook uses the existing `app/<slug>` source directly.
Installation supplies GitHub authority; source registration supplies verification and
routing; ACL grants supply tool access; holds supply approval. These remain separate.

`githubapp` supplies one shared App client for gitd replies and routd's existing broker.
The App ID and RSA private key are operator credentials in the instance `.env`,
named `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY` (base64-encoded PEM), respectively.
Neither the key nor an installation token enters the agent environment or tool output.
The client resolves the repository's installation through GitHub and mints a token for
exactly that repository. Tokens remain on the host and refresh before their expiry.
The implementation adds no installation table, credential endpoint, relay, channel mutation
verbs, or extension to the storage/execution ports.

The connector declares a reserved derived `GITHUB_INSTALLATION_TOKEN` slot in the existing
connector shape. `routd/mcp.go` binds discovery and call credentials to immutable
`turnMCP.chatJID`, never to agent arguments. `GITHUB_TOKEN` keeps its user/PAT meaning.
The derived slot has no user-secret overlay or PAT fallback. Non-GitHub and account-only
turns have no implicit App repository. Repository token bounds protect private data and
writes; public reads follow GitHub's public access rules.

GitHub's [host integration guide](https://github.com/github/github-mcp-server/blob/main/docs/host-integration.md)
accepts valid access tokens; its [remote policy documentation](https://github.com/github/github-mcp-server/blob/main/docs/policies-and-governance.md)
explicitly lists installation tokens. The existing hosted HTTP transport carries
discovery and calls. Real discovery and repository calls still require credentialed acceptance.

The broker binds credentials to the trusted turn context, never an agent-selected
repository or topic string. Revocation fences new calls and invalidates cached tokens
before another dispatch. The broker token contract is implemented in `routd/mcp.go` and
`ipc/connector.go`. `githubapp.ResolveSource` supplies the shared source precedence for gitd
and routd.

## Delivery and gaps

`chanlib.RetainInbound` attempts to retain transient routd failures in gitd's adapter
store; `gitd/server.go` answers 202. When the retention write fails too — routd and
the adapter store ride the same store, so one outage fails both — `SendMessage`
reports `chanlib.ErrInboundLost` and the answer is 503, which keeps GitHub's own
redelivery available as the recovery path.
Permanent refusals return an error. A gitd outage still requires GitHub
redelivery. Stable comment/delivery IDs give routd's message store its replay boundary;
GitHub's HMAC does not attest a timestamp. `mapEvent` drops Bot senders, and token-backed
startup learns the token user's login so `handleWebhook` also drops that user's comments.
A PAT GitHub refuses at startup is not fatal: gitd withdraws `send_text` through
`RouterClient.SetCaps` and serves ingress only, because exiting cycles the whole
compose project, unless an App ID also enables replies. Without that login the self-drop is inactive.

GitHub logins are mutable, so login-based principals can transfer after a rename.
App-token discovery uses the trusted turn repository and refuses missing App authority.
`github_sources` has REST/MCP, CLI and dashboard view/control through routd's `/v1/github_sources`.
Source deletion does not uninstall an App or revoke its tool grants.
gitd replies and routd's App connector discovery/calls re-read sources before resolving credentials.
A missing source refuses access and evicts the repository's cached App token.
`githubapp.Client.Token` checks the repository installation before cache reuse; lookup and token-request failures,
or a suspended installation, evict the cached token and refuse access without a PAT fallback.
Verified installation deletion/suspension and repository removal webhooks invalidate gitd's affected tokens.

## Acceptance boundary

Existing tests cover event fixtures and route resolution (`gitd/event_test.go`), signed
delivery and retention against a fake routd (`gitd/server_test.go`), and comment API shape
against a fake GitHub (`gitd/bot_test.go`). They do not prove a complete live App conversation.

Complete App delivery requires distinct same-number repository sessions, App bot replies
without loops, repository-limited discovery/calls, renewal, and visible lifecycle refusal.
It also requires grant/hold regression checks, source dashboard controls, matching setup
docs, and real hosted MCP acceptance. A credential-free skip proves none of those live
claims. Live delivery remains gated by owner-created credentials, authorized actions,
and the post-deploy checks including `anteval --smoke`.
