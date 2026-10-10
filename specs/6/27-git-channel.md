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
  ]
---

# specs/6/27 — gitd: GitHub channel and repository tools

Repository events reach groups through ordinary routes. gitd receives signed webhooks
and posts replies as comments. Repository state changes use the GitHub MCP connector.
App webhook ingress exists; App replies, installation-token tooling, and complete
conversation isolation do not. The App completion contract below needs owner approval.

## Channel messages and repository tools

A comment is a message in the conversation. A merge, label, or review changes repository
state. `gitd/bot.go` implements only `send_text`; `gitd/main.go:capsFor` advertises it
when `GITHUB_TOKEN` is set. The token's user posts the comment and the returned identity
is `gh-c<id>`, matching inbound comment IDs. Without that token, gitd is ingress-only.
Other channel verbs return Unsupported.

State changes use `template/connectors/github.toml` through the hosted HTTP broker
in `ipc/connector.go`. Each tool uses its `mcp:github_<tool>` grant and the socket's
`hold:mcp:<tool>` policy. A held merge re-enters the ordinary authorization path after
approval. A second channel path for repository mutations would bypass that boundary.
Credentials resolve on the host and results scrub their values
([`13-ext-mcp.md`](../5/13-ext-mcp.md), [`19-hitl-firewall.md`](../5/19-hitl-firewall.md)).

## Sources and identity

Repository, organization, and App hooks share one receiver, signature verifier, and
event mapper: `gitd/server.go`, `gitd/verify.go`, and `gitd/event.go`.
The `/github/<kind>/<name>` path selects the registered signing secret before mapping.
The payload never selects its verifier. `checkSource` binds a repo source to its full
repository name and an org source to its owner. An App delivery requires an installation
object, except for ping; the current check does not validate a positive installation ID.

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

`mapEvent` marks human comments, new/reopened issues, and new/reopened/ready PRs as messages.
Other events retain their GitHub event verb. `store.GitHubSourceRoutes` seeds message
and mention triggers; other events go to `#observe`. Narrow repo routes precede org
routes and the App default. Operators change that split through the existing route table.
Route globs use `path.Match`; `*` does not cross `/`.
Live engagement can trigger later events on that thread before the observe route applies
([`Q-unified-routing.md`](../5/Q-unified-routing.md)). Review replies retain `gh-c<id>` reply pointers.

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

The topic proposal uses the full PR/issue JID as the topic. Existing mixed sessions cannot
be split safely. The proposal does not rewrite live history or widen the global session key.
Neither the token contract nor the topic change is implemented.

## Delivery and gaps

`chanlib.RetainInbound` retains transient routd failures in gitd's adapter store and
answers 202. Permanent refusals return an error. A gitd outage still requires GitHub
redelivery. Stable comment/delivery IDs give routd's message store its replay boundary;
GitHub's HMAC does not attest a timestamp. `mapEvent` drops Bot senders, and token-backed
startup learns the token user's login so `handleWebhook` also drops that user's comments.

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
