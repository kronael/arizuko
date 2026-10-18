---
status: partial
defects: [GITHUB-TOPIC-REPOSITORY]
depends:
  [
    ../5/34-channel-protocol,
    ../5/13-ext-mcp,
    ../5/19-hitl-firewall,
    26-personal-mode,
    ../5/Q-unified-routing,
    ../5/31-identity-pairing,
    ../5/35-hermes-integration,
    ../5/28-packages,
    ../5/F-topic-lineage,
  ]
---

# specs/6/27 — gitd: GitHub channel and repository tools

Repository events reach groups through ordinary routes. gitd receives signed webhooks
and posts replies as comments. Repository state changes use the GitHub MCP connector.
App webhook ingress exists; App replies, installation-token tooling, and complete
conversation isolation do not. Automatic PR workspaces and group archival are unbuilt.
The proposals below need owner approval before implementation, including their public
contracts and retention policy. This partial status describes the existing adapter;
it does not approve the proposed lifecycle or claim a deployment.

## Channel messages and repository tools

A comment is a message in the conversation. A merge, label, or review changes repository
state. `gitd/bot.go` implements `send_text`, `send_file`, `edit` and the typing indicator below;
`gitd/main.go:capsFor` advertises all four when `GITHUB_TOKEN` is set. The token's user posts the comment and the returned identity
is `gh-c<id>`, or `gh-rc<id>` for an inline review comment, matching inbound comment IDs.
Without that token, gitd is ingress-only.
Other channel verbs return Unsupported.

> The channel creates, edits and reacts to ITS OWN posts in the thread it was
> addressed in. Every change to another party's state — resolve, reopen,
> merge, label, a new thread, the document body — is the connector's, under
> its own grant and hold.

`Edit` replaces the text of one of gitd's own comments, which is how routd keeps one
progress comment per turn (`specs/6/46-turn-progress.md` §"What this changes in 6/27 and 6/45").
The id prefix picks the endpoint, as it does for the reaction: `gh-c<id>` is
`PATCH /repos/{owner}/{repo}/issues/comments/{id}`, `gh-rc<id>` is
`PATCH /repos/{owner}/{repo}/pulls/comments/{id}`, each with the body `{"body"}` and
answered `200` (docs.github.com/en/rest/issues/comments,
docs.github.com/en/rest/pulls/comments). Any other id (a bare number, a review `gh-r<id>`,
an empty id) is `ErrInvalidRequest` before any call. The id grammar cannot say who wrote a
comment, and a token with write access may edit anyone's, so gitd reads the comment first
and refuses unless its `user.login` is the login `GET /user` returned at start. The refusal is
`ErrPermanent`, and no `PATCH` is sent. A transport error stays unclassified, as for `send_text`.

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

Current `GitHubJID.Topic` returns `pr/<n>` or `issue/<n>`, with an empty topic at
repository/account level. `store/sessions.go` keys conversations by folder and topic.
Two repositories' PR 12 routed to one group therefore share a conversation.

`mapEvent` marks human content (a comment, a new or reopened issue, a new, reopened or ready PR)
`mention` when it @-mentions the bot's login OR when the conversation's assignee set
(`issue.assignees`, `pull_request.assignees`) holds that login, and `message` otherwise.
A review requested from the bot and an assignment to it are mentions on their own event;
`requested_teams` is not read, and `issue_comment` carries no `requested_reviewers`, so
assignment is the only claim that reads the same on every event family. Other events retain
their GitHub event verb. `store.GitHubSourceRoutes` seeds mention triggers; `message` and
everything else go to `#observe`. Being asked is what wakes the agent: a human comment on a
conversation nobody assigned to it is context, not work. Without `GITHUB_TOKEN`, or until
`GET /user` answers, the login is empty, nothing can be a mention, and gitd observes
everything; gitd logs that at startup.
Narrow repo routes precede org
routes and the App default. Operators change that split through the existing route table.
Route globs use `path.Match`; `*` does not cross `/`.
Live engagement can trigger later events on that thread before the observe route applies
([`Q-unified-routing.md`](../5/Q-unified-routing.md)).

An inline review comment is a thread, and `SendRequest.ReplyTo` is how gitd addresses it:
the topic stays `pr/<n>`, one session per pull request. Review comments carry the `gh-rc<id>`
namespace inbound and outbound because GitHub keeps them in a different id space behind a
different endpoint, and a bare number cannot say which. `gitd/bot.go` reads that prefix: it
resolves the comment that opened the thread — GitHub takes only that one as a reply target —
and posts to `pulls/<n>/comments/<root>/replies`. Every other reply uses the issue-comments
endpoint. A missing or foreign thread is reported, never redirected to the conversation tab.
`SendRequest.ThreadID` carries the turn's topic, which is a session key and not an address,
so gitd does not read it.

## Repository workspace proposal — unapproved

A repository has one explicitly bound group, such as `sloth/morha`. Each open PR has
one child, `sloth/morha/pr-<number>`, with its own checkout, files, grants and sessions.
Review discussions share that PR's checkout through named topics. Issues remain topics
in the repository group; branches, commits, labels and reviews do not create groups.
The repository group remains after its PRs close. Archived PR groups leave the active
group list; operators can still open their retained history through an archive view.

### Binding and provisioning

The proposed cold-tier `github_repositories` resource belongs to routd beside
`github_sources`. One row binds a positive GitHub repository ID to a source key, current
`owner/repo`, explicit group folder, and `pr_product` source. `repo_product` supplies
the seed when the repository group does not exist. Existing groups require explicit
binding; a matching folder name does not authorize adoption. A repository ID and a
repository folder each have one binding per instance. Source registration alone does
not enable provisioning, and an App installation does not enroll every repository.
Enabling automation requires verified repository access through the shared GitHub client.
The client accepts explicitly configured broker-held PAT or App installation credentials.
Credential choice does not change source verification, group ownership or lifecycle rules.
Signed webhook ingress itself remains independent of that API credential.

Both product references use the existing product source and `PRODUCT.md` format from
[`28-packages.md`](../5/28-packages.md). A prototype is an explicitly selected product
directory in that format. The binding pins its revision or local content snapshot.
Changing that pin affects new children; an existing child keeps its recorded product
until an explicit product apply.
`container.SetupGroup` and `container.SeedProduct` own single-product materialization;
`installed_packages` records the source, revision and managed assets. An ordered mix
remains declared through `products.toml` and applied through `container.ComposeGroup`;
the binding itself names one seed product. There is no second prototype schema,
parent-directory search, or live copy of a parent's workspace.
Persona, skills and settings are materialized in the child itself. Secrets, sessions,
memory and grants are not copied from the parent. Grants use the existing ACL manifest
and evaluator, within the registering operator's authority. Explicit subtree ACL rows
still apply; creating a child infers no additional grant.

Creation extends one core group provisioner used by CLI, REST, derived MCP and adapters.
The provisioner owns guarded `store.GroupCreateWrite`, product materialization and route
rendering. It accepts an explicit product and opaque origin reference; it contains no
GitHub repository, PR or webhook parser. gitd supplies the validated binding and PR intent.
`routd/mcp.go:registerGroup` alone is insufficient: it upserts a row, adds a route and
git-inits the directory, without product seeding. A pre-existing child or directory with
a different recorded owner is a conflict. Redelivery resumes only the same owned
creation. Partial filesystem work stays in `provisioning`, with a visible error and
no dispatch; recovery never calls the destructive subtree deletion as rollback.
gitd holds a PR's dispatchable messages until its owned child is ready, before ordinary
route resolution. A closed PR first seen without a child gets an archived metadata
record and history, without cloning code.

The binding's `max_active_prs` defaults to eight. Provisioning and stopping groups
count toward that limit. Admission is guarded at binding scope, so concurrent PR opens
cannot exceed it. Capacity exhaustion leaves the PR visibly pending until
capacity or configuration changes; it never routes the PR to the parent. Existing
per-folder budget checks apply to each child. Parent budgets do not imply child caps.
Drafts are open PRs. Opening, reopening or marking a PR ready provisions its workspace
without starting an agent turn.

The source and workspace binding are declarations; the ordinary route table selects
delivery. Extend `store.GitHubSourceRoutes`' renderer for exact repository and PR routes
ahead of source defaults. Generated rows have recorded ownership. Conflicting manual
routes produce a visible binding error; provisioning never overwrites an operator row.
The managed defaults dispatch only an explicit configured bot mention or a direct reply to a
recorded bot comment. All other comments remain context. A bot's earlier participation
does not turn a human review thread into an implicit mention. The existing zero-TTL
setting and one-shot disengagement apply independently of workspace lifetime.
General PR comments have no native reply pointer, so they require an explicit mention.
An inline reply qualifies directly only when its recorded root is a bot comment.

The repository ID is the identity; names are display and transport coordinates.
A same-owner rename updates GitHub JIDs and generated routes without moving folders.
For a managed source, that update requires the bound repository ID to match; the
source's registered URL remains stable. A transfer, source mismatch or replacement
repository at the same name suspends the
binding until explicit reauthorization. The binding's folder is never inferred from
the checkout path, branch name, PR title or sender.

### Conversations and replies

Transport JIDs retain the existing grammar. Topics use the repository ID, which also
keeps an ordinary route carrying several repositories safe:

| GitHub conversation                                 | Topic                                                                                      |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| PR description, general comments and review summary | `github:repo/<id>/pr/<n>`                                                                  |
| One inline review discussion                        | `github:repo/<id>/pr/<n>` (shared; the thread is addressed by `ReplyTo`, never by a topic) |
| Issue description and comments                      | `github:repo/<id>/issue/<n>`                                                               |

These are topic names, not new JID forms. The session key remains `(folder, topic)`.
A review reply uses `comment.in_reply_to_id`; a root uses its own ID. gitd validates
that the root belongs to the same repository and PR. It resolves missing root data
through GitHub before dispatch. `pull_request_review_thread` resolution events map to
the same root; a filename, line number, review ID or resolution state is never the key.
Labels can show file and line without changing topic identity. Resolution retains the
topic and does not close the PR group. A review with several inline comments adds one
`gh-rc<id>` reply target per comment inside the one PR topic.

The first explicit trigger hydrates the PR description, current head and that topic's
paginated history through the existing message store and prompt renderer. Imported
comments are context, never pending work. Later webhooks update that stored context;
turns do not scrape the full PR again. A hundred comments therefore remain one shared
checkout with one PR topic, not a hundred agent tasks or subgroups.

`send_text` reads `SendRequest.ReplyTo`; the topic names the session, never
the address. General PR/issue replies use
the issue-comments endpoint. Inline replies use the review-comment reply endpoint
with the root ID; GitHub accepts only a top-level review comment as the reply target.
An unknown, deleted or foreign root fails visibly, without posting to the general PR.
This is channel reply behavior; creating reviews, resolving threads and merging remain
connector actions under their existing grants and holds.
See GitHub's [comment types](https://docs.github.com/en/rest/guides/working-with-comments)
and [review reply contract](https://docs.github.com/en/rest/pulls/comments#create-a-reply-for-a-review-comment).

Issue-comment message IDs retain `gh-c<id>`; review comments use `gh-rc<id>` on both
inbound and outbound paths. Reviews retain `gh-r<id>`. Delivery IDs remain distinct
from object IDs, so edit/delete events cannot disappear as duplicates of creation.
Edits and deletions update the retained conversation without starting another turn.
GitHub object identity and accepted delivery identity are separate. The existing
`store/messages.go:PutMessage` keeps the first message ID and cannot apply these updates.
The shared conversation store and renderer require guarded object revisions and deletion
tombstones. Older edits or a delayed create cannot overwrite a newer revision or
resurrect a tombstone. Ambiguous order requires an authoritative object read. A confirmed
object deletion, rather than an access failure, records the tombstone.

Hydration records the triggering delivery separately from imported context. Importing
the same comment first cannot deduplicate away its one authorized trigger. Imported
history never changes dispatch cursors or creates pending tasks.
Existing mixed sessions and ambiguous comment references stay historical; cutover
starts new qualified topics and never guesses how to split their transcripts.

Topic lineage uses the existing session renderer: a pull request is one topic and
one session, and every reply resumes it. Each PR has at most one live writer through
the existing folder queue. Another topic waits instead of steering its text into the active topic.
`runed/manager.go:admit` currently steers by folder alone. The shared admission path
requires matching topics before steering; a different topic returns the existing busy
result and stays queued. This is a core topic rule, not a gitd-specific queue.
Separate PR groups can run concurrently.

### Adapter intent and core ownership

gitd verifies deliveries, reconciles GitHub state and requests group transitions.
routd owns group registration, dispatch admission, sessions, products, routes and host
workspace files. runed owns execution and termination. gitd has no group filesystem
mount and performs no direct writes to another daemon's tables.

Extend `groups` with recorded origin, lifecycle state, generation and generated-asset
ownership. The origin is an opaque `(adapter, binding, object)` reference. gitd binds that object
to immutable `(repository ID, PR number)`; the core does not interpret GitHub identifiers.
Core transitions are provision, suspend, archive, resume and generated-asset reap.
Any adapter can request these transitions within its enrolled scope; only gitd maps
GitHub state to them.
The generation fences work admitted before a transition through the existing turn/run
association in routd; it adds no field to the runtime port's value. This state has one
owner and renderer; there is no second GitHub workspace table mirroring groups and sessions.
Ordinary groups retain their current behavior unless explicitly enrolled.

The groups resource exposes registration, suspend, archive, resume and reap through one handler
on MCP and REST. The new repository binding has the same two surfaces, CLI management
and dashboard view/control. resreg registration binds authn and authz to the concrete
source, repository folder and child. An adapter service credential can submit lifecycle
intent only for its configured binding. A webhook actor receives no management grant.
Neither folder depth nor a product name confers authority. Changes use each owner's
audit and `objstore.Query`/`GuardedBatch`; `runed.Runtime` stays `Run`/`Kill`.

| Confirmed condition                                                           | Group behavior                                                                                    |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Open PR, including reopened                                                   | Materialize or resume the same child; `provisioning` becomes `active` only after readiness.       |
| Head SHA changes                                                              | Fence and stop the current generation before updating the checkout; preserve topic history.       |
| Closed PR, merged or unmerged                                                 | `stopping` becomes `archived` after execution stops; retain the outcome and `closed_at`.          |
| Source disabled, access revoked, repository removed or installation suspended | Suspend dispatch and credentials; preserve files and history without starting a cleanup deadline. |
| GitHub state unavailable or ambiguous                                         | Report the error; do not create, resume, update or reap from an unverified assumption.            |

Archive first commits the admission fence. Every ingress, timer, delegation, retry,
held action, tool call and outbound delivery checks that fence and its generation.
Queued work and pending approvals from the old generation become terminal with a
visible reason. They never replay on reopen. The core then stops the live run through
runed and verifies termination before changing files. An already accepted external
request can complete; its result remains recorded. Archival cannot undo that request.
A termination failure leaves `stopping`, with cleanup blocked.
`runed/manager.go:Kill` currently discards the executor error and marks the run killed.
An HTTP success or terminal spawn row therefore does not prove termination. The shared
stop/hold path must preserve termination failures and retain its exclusion until the
execution and mounts are confirmed gone. This strengthens that existing path without
adding a runtime-port method.

The filesystem exclusion covers every overlapping writable mount, including ancestors
and explicit shared mounts. `runed/mounts.go` mounts a group directory with its children,
so a repository or world turn can still write a PR checkout. The core blocks new
overlapping writers and drains existing ones before checkout update or reap. A PR-only
run slot is insufficient. Unrelated nonoverlapping groups remain runnable.
Suspension uses the same fence and termination barrier. Only archival starts the
declared cleanup deadline.

The exact PR route remains bound while archived. New comments remain readable history
and do not dispatch, fall through to the parent, or reactivate the workspace through
an engagement lease. Resume requires a confirmed open PR and current GitHub access
before it enables a new generation. A merged PR cannot reopen. A local retry after an
operator repairs a provisioning error does not change GitHub state.

### Checkout and retention

Each PR owns an independent detached checkout under its generated `repo/` directory.
This avoids shared writable Git metadata between groups. The host fetches the bound
base repository's PR head ref and verifies the resulting SHA against GitHub's current
PR head. It does not use the synthetic merge ref or a branch name as identity.
[GitHub exposes PR head refs](https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/checking-out-pull-requests-locally)
for this purpose, including fork PRs. Checkout credentials stay in the host broker;
the App token has repository-limited Contents read permission. The guest receives
neither a token-bearing remote URL nor authority to the author's fork.

Fetching and materialization execute no repository hooks, submodules, package scripts
or product commands on the host. Repository code runs inside the existing turn sandbox.
A head update never resets, stashes or overwrites local edits. Modified files, untracked
or ignored files, stashes, or commits/refs outside the recorded fetched state suspend
the update for operator resolution. Reopening preserves the same rule.

The binding declares `generated_retention_days`, proposed default seven, and a retain
flag. This policy governs generated files only; zero days still requires every archive
and cleanup safety check.
The proposed default retains group rows, ACL ownership, topics, transcripts, messages,
audit, product files and user artifacts without an automatic expiry. After the declared retention interval following
successful archival, the core can reap only the recorded generated checkout and temporary
assets. A retain flag disables that reaping. Dirty or unknown assets stay, with the
exact reason shown. Downloaded source is disposable; retained SHAs do not promise that
GitHub keeps old commits available forever.
The deadline is set once per closed generation; duplicate deliveries do not extend it.

Reaping requires a fresh closed-state check, the same archived generation, no live
run or mount, and an exact generated-asset ownership match. It never walks a parent
subtree, follows symlinks or removes a manually created child. Reopen and reap serialize
on the same group generation. A reopen after cleanup recreates the checkout while
retaining conversation history. Partial cleanup resumes from the same recorded assets.
An API failure, ownership mismatch or failed deletion is visible and preserves the
remaining data. Full group deletion remains a separate explicit operator action.

`store.DeleteGroup` is unsuitable for PR closure: `store/folder_scope.go` purges scoped
sessions, grants, tasks and other data. `dashd/groups_admin.go` also removes the group
directory. `/stop` is insufficient too: `routd/steer_commands_test.go` explicitly tests
that its queued backlog remains runnable. Archive and generated-asset reaping therefore
require a core lifecycle contract; calling either existing action from gitd is not it.

### Delivery and reconciliation

Subscribe to PR, issue-comment, review, review-comment and review-thread events.
App lifecycle events also cover installation access and repository membership.
A successful public PR read is not proof of an App installation grant. Reconciliation
checks the installation identity, suspension status, effective permissions and paginated
repository membership separately. A narrowed installation event can omit removal
entries, so an empty list is not proof of continued membership. PAT mode checks its
configured authenticated identity and effective repository authority. Access failures
suspend enrolled groups without starting a data-cleanup deadline.
Signed lifecycle events are processed even when the sender is a bot or the App itself;
loop suppression applies only to conversational dispatch. Unknown event actions do
not acquire lifecycle meaning. The current blanket Bot filter cannot serve this design.

The existing adapter retention mechanism owns a durable verified envelope containing
source/version, delivery ID, payload hash and the normalized event facts. It retains
the work until both lifecycle reconciliation and message acceptance succeed. A 2xx
means that durable acceptance succeeded; persistence failure returns non-2xx. Redelivery
of the same source/delivery is idempotent, and a different payload under that key is
rejected. The current message-only retained shape omits repository ID and lifecycle
facts and is insufficient. There is one retained delivery path, not a second daemon
or queue for cleanup. Replay rechecks the source and binding version before any effect;
revoking a source also fences its retained work.

Webhooks wake reconciliation; they do not overwrite state in arrival order. gitd reads
the current PR state/head through the shared GitHub client before a state-changing request.
One worker per binding/PR and guarded generations serialize local effects. Reconcile
also runs at startup and every five minutes, with pagination and GitHub rate-limit
handling. Missing from a list is not proof of closure; confirm each known active PR.
Periodic reconciliation covers active, provisioning, stopping and cleanup-eligible
children. Archived children outside that set require a reopen event or explicit
reconciliation; retained history does not require polling every old PR forever.
This repairs missed lifecycle events for the monitored set. It does not prove comment completeness: failed
comment deliveries require GitHub redelivery. Dashboard reconciliation status and
delivery health remain distinct.

GitHub documents [out-of-order delivery](https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/troubleshooting-webhooks),
[the ten-second response deadline and stable redelivery ID](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks),
and [the absence of automatic redelivery](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries).
[Argo CD's PR generator](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Pull-Request/)
likewise combines webhook wakeups with reconciliation against open PRs. The applicable
pattern is current-state reconciliation; its application deletion policy does not
define this system's conversation retention.

## App completion proposal — unapproved

One App belongs to one instance. Personal accounts and organizations install that App
on selected repositories. Its webhook uses the existing `app/<slug>` source directly.
Installation supplies GitHub authority; source registration supplies verification and
routing; ACL grants supply tool access; holds supply approval. These remain separate.

The proposal adds one shared App client for gitd replies and routd's existing broker.
The App ID and RSA private key are operator credentials in the host configuration.
Neither the key nor an installation token enters the agent environment or tool output.
The client resolves the repository's installation through GitHub and mints a token for
exactly that repository. Tokens remain on the host and refresh before their expiry.
The proposal adds no installation table, credential endpoint, relay, channel mutation
verbs, or extension to the storage/execution ports.

The connector declares a reserved derived `GITHUB_INSTALLATION_TOKEN` slot in the existing
connector shape. `routd/mcp.go` binds discovery and call credentials to immutable
`turnMCP.chatJID`, never to agent arguments. `GITHUB_TOKEN` keeps its user/PAT meaning.
The derived slot has no user-secret overlay or PAT fallback. Non-GitHub and account-only
turns have no implicit App repository. Repository token bounds protect private data and
writes; public reads follow GitHub's public access rules.

GitHub's [host integration guide](https://github.com/github/github-mcp-server/blob/main/docs/host-integration.md)
accepts valid access tokens; its [remote policy documentation](https://github.com/github/github-mcp-server/blob/main/docs/policies-and-governance.md)
explicitly lists installation tokens. The existing hosted HTTP transport remains the
proposed path. Real discovery and repository calls still require credentialed acceptance.

The repository/topic contract above supplies the immutable identity for managed
workspaces. The broker still binds credentials to the trusted turn context, never an
agent-selected repository or topic string. Revocation fences new calls and invalidates
cached tokens before another dispatch. Neither the token contract nor the workspace
and topic contract is implemented.

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
A token GitHub refuses at startup is not fatal: gitd withdraws `send_text` through
`RouterClient.SetCaps` and serves ingress only, because exiting cycles the whole
compose project. Without that login the self-drop is inactive.

GitHub logins are mutable, so login-based principals can transfer after a rename.
App JWTs, token refresh, App bot replies, and App-token discovery are absent.
`github_sources` has REST/MCP and CLI management but no dashboard view and control.
Source deletion does not uninstall an App or revoke its tool grants.
Revocation, repository removal, suspension, and uninstall need checks with cached tokens.

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

The workspace proposal additionally requires these observable results:

- Duplicate opens and a crash at every creation boundary leave one owned child,
  one product seed and one set of routes. An existing foreign directory is untouched.
- Same-number PRs from two repositories have separate groups and sessions. Replies
  to two inline discussions stay in their respective GitHub threads, including when
  a reply arrives before its root. A deleted root cannot redirect a reply.
- Bot merges, delayed opens, close-before-open delivery, force-push, lost close events,
  restart and reopen during cleanup converge to GitHub's current state. Old turns,
  approvals, callbacks, queued messages and engagement cannot reactivate an archive.
- Termination failure and any overlapping writable mount block filesystem changes.
  Cross-topic steering is refused. Dirty, ignored, untracked, manually
  owned, symlinked and mounted data survive reaping. Clean generated data expires only
  after the stated delay; history survives both reaping and reopening.
- Source removal, installation suspension, repository transfer, quota exhaustion,
  API rate limits and revoked credentials produce visible scoped failures, including
  removed access to a public repository. Out-of-order edits and deletes converge without
  resurrecting comments; hydration preserves exactly one authorized trigger. Sibling
  groups without a matching grant cannot inspect or mutate the binding, checkout or
  history through REST/MCP. Lifecycle-only deliveries and ordinary comments start no
  model turn; explicit mentions and direct bot replies remain the engagement boundary.

These require fault-injection and concurrency tests at the owner boundaries, plus a
credentialed disposable-repository exercise of real thread replies and lifecycle events.
This spec revision executes no creation, cancellation, cleanup or live GitHub write.
