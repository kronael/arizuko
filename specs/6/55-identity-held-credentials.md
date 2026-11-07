---
status: draft
---

# A user's model subscription pays for that user's turn

An identity-held model credential pays for the turn its owner initiates.
Tool accounts and tool authority belong to [identity-held tools](56-identity-held-tools.md).

## A sender must resolve to the account that owns the credential

The owner authorized phase 7 design: "all for phase 6 is now being completed
lets start with phase 7". This draft proposes contracts for approval. It does
not authorize implementation, deployment, or changes to live credentials.

The owner's scope is binding: users authenticate, their channel senders join
one OAuth account, and their model subscription powers the turns they initiate.
The owner also said:

> normally the agent should use its own logins for tools, and acting on
> behalf of users should be explicitly granted anyway, so that is a
> different spec than the oauth for users' subscription to tokens.

A guest in a shared group therefore pays with the guest's credential. The
folder owner, the last speaker in observed context, and the tool's account
cannot become the payer. Connecting a model grants no tool authority.

Source baseline: `e1ddadfbc51a3042e12b9c54b799a599a6ba8513`. The relevant
contracts differ from the filing in these places:

- `routd/dispatch.go:427` stores the batch's last `Sender` as `TurnContext.Trigger`.
  `turnCallerSub` at `routd/dispatch.go:782` passes ordinary senders unchanged
  and maps timed/system triggers to `service:routd`. A trigger identifies a
  source, not necessarily a human. `webd/api.go:109` can emit `operator` without
  a user subject. `webd/route_token.go:300` makes a hook's source label its sender.
- `store/pairing.go:153` writes a channel-sender edge in `acl_membership`,
  marked `added_by=pairing`. It does not populate authd's OAuth alias table.
  `authd/authstore/auth_users.go:26` resolves linked OAuth logins through
  `oauth_identities` to `auth_users`. Both relations are needed.
  GitHub is directly joinable: `gitd/event.go:364` and `auth/oauth.go:292`
  both use the login name. `discord:user/<id>` still needs pairing to the
  OAuth spelling `discord:<id>`; stripping `user/` would invent an association.
- The direct credential path exists. `routd/dispatch.go:858` calls
  `DB.EnvProfileSecrets` with the raw sender. `routd/sibling_db.go:97` reads
  that exact user's rows and returns an empty map on a store failure.
  It neither resolves the account nor refreshes OAuth model credentials.
- `runed/secrets.go:38` merges operator model keys with caller keys per key.
  Different keys can therefore coexist across owners. A configured gateway
  deletes every env-profile key and leaves the gateway credential active.
  `routd/gateway.go:84` attributes a virtual key to `user_id`; it does not bind
  that key to the user's upstream token. Attribution does not select billing.
- `store.EnvProfileKeys` at `store/secrets.go:248` includes
  `ANTHROPIC_OAUTH_TOKEN` as well as the Claude and API-key names.
  It has no Codex subscription-cache or OpenRouter key entry.
  `runed/mounts.go:174` lends operator Codex login files under a `codex` grant.
  An OpenAI API key and a ChatGPT subscription login are different credentials.
- `auth/surrogate/registry.go:22` embeds the provider files; only GitHub and
  Google files exist.
  `auth/surrogate/engine.go:129` requires both a client ID and secret.
  `dashd/me_connections.go:235` writes the signed-in user's canonical subject.
  Model enrollment and model refresh are not supplied by those tool providers.
  `routd/surrogate_refresh.go:27` refreshes connector credentials only.
- The existing separation is incomplete: `routd/sibling_db.go:203` already
  overlays a raw caller's tool secrets, and `routd/mcp.go:594` refreshes them.
  Thus the filing's account of explicit surrogate grants is not proof that
  every use of a personal tool login needs separate consent. This draft must
  not feed its canonical model subject into that path. 6/56 owns that review.
- Pi refuses execution at `ant/src/backend.ts:116` because it cannot deliver
  through MCP. Vendor quota behavior is not its only blocker.
  The filing's dated gateway probes do not establish current provider support.
- Ant selects the adapter at `ant/src/index.ts:482` through
  `ant/src/backend.ts:112`. Credential selection follows whichever harness
  selection path applies. [Per-group harness](53-per-group-harness.md)
  is not a prerequisite; coordinate only its host-login mount condition.
  A future identity consumer reads the join below;
  it must not create another principal map.

[Surrogate OAuth](../5/15-surrogate-oauth.md) supplies OAuth storage and the
exchange engine. [Credentials](../5/14-credentials.md) owns credential tiers.
[Pairing](../5/31-identity-pairing.md) owns consent and its edges.
[Unified ACL](../5/32-acl-unified.md) and
[paths and roles](../5/33-paths-roles.md) own authorization.
[External MCP](../5/13-ext-mcp.md) owns tool credential delivery.
Their frontmatter does not prove the missing model path exists.

## One join feeds the existing credential transport

```text
channel sender ── pairing edge ──┐
OAuth login ────────────────────┤
                               ▼
                    ResolveInitiator (new) ◄── authd identity read (changed)
                               │ canonical sub (new)
                               ▼
                  turn_context.model_user_sub (new)
                               │ credential read (changed)
                               ▼
                   secrets, scope_kind=user
                               │ RunRequest.Secrets (changed)
                               ▼
                    runed renderer (changed)
                               │ stdin secrets; payer env (new)
                               ▼
                  ant harness selection
                               │ selected model credential (new)
                               ▼
                     Claude / Codex / OpenRouter

operator anchors ── human merge (removed)
model gateway ── personal turn: visible refusal (new)
raw trigger ── tool broker and ACL
```

The proposed first delivery uses the direct env-profile path. A personal
turn carries no operator model credential. Gateway instances refuse personal
turns until their existing gateway supports a verified upstream credential
binding. They must not bypass the gateway or create another model proxy.
This release boundary requires owner approval in question 1.

### Module map

Paths marked new are proposed files. Names below are proposed public contracts
unless the source excerpts show them already. Test jobs state their required
assertions in the acceptance section.

```yaml
- path: authd/authstore/auth_users.go (changed)
  job: distinguish an absent OAuth identity from a failed identity read
  names: [OAuthIdentityForSub]
  never: read routd membership or credentials
- path: authd/http.go (changed)
  job: preserve lookup failure as HTTP 503 on the existing identity endpoint
  names: ['GET /v1/identities/{sub}']
  never: answer identity null for a store error
- path: authd/oauth.go (changed)
  job: stop login when canonical identity resolution fails
  names: [OAuthIdentityForSub]
  never: create a new account after a failed lookup
- path: cmd/arizuko/grant_canonical.go (changed)
  job: propagate the checked identity reader's error to its existing caller
  names: [OAuthIdentityForSub]
  never: duplicate canonicalization
- path: routd/identity.go (changed)
  job: join an exact sender through consented pairing and authd
  names:
    [
      'Server.ResolveInitiator(trigger) (sub string, err error)',
      ErrInitiatorUnclaimed,
      ErrInitiatorConflict,
    ]
  never: traverse roles as ownership or convert sender spellings
- path: routd/sibling_db.go (changed)
  job: read only the canonical user's model credentials with errors
  names: ['DB.EnvProfileSecrets(sub) (map[string]string, error)']
  never: read folder model keys or return empty on read failure
- path: routd/dispatch.go (changed)
  job: isolate batches by sender and bind the payer before credential reads
  names: [Loop, RunRequest.ModelUserSub, dispatchRun]
  never: change turnCallerSub or the tool principal to the model payer
- path: store/turn_context.go (changed)
  job: persist a nonsecret payer snapshot with the existing turn binding
  names: [TurnContext.ModelUserSub, OpenTurnContext]
  never: persist a token or overwrite a bound payer on retry
- path: routd/db.go (changed)
  job: carry the payer through the existing turn-context facade
  names: [PutTurnContext, GetTurnContext]
  never: maintain another identity table
- path: store/secrets.go (changed)
  job: declare model keys and share native-cache validation and sealing across writes
  names: [EnvProfileKeys, SetSecret, PutSecretRow]
  never: store a native refresh token or accept model keys at folder scope
- path: auth/surrogate/engine.go (changed)
  job: support the OpenRouter PKCE exchange beside the existing confidential flow
  names: [Engine.AuthorizeURL, Engine.Exchange, Engine.HasCreds]
  never: require a fictitious OpenRouter client secret or change GitHub and Google wire forms
- path: auth/surrogate/providers/openrouter.toml (new)
  job: declare OpenRouter endpoints and the OPENROUTER_API_KEY sink once
  names: [secret_key]
  never: carry a live credential
- path: dashd/me_connections.go (changed)
  job: connect OpenRouter and describe model billing separately from tool accounts
  names: ['/dash/me/connections']
  never: let the request choose another user's scope_id
- path: dashd/me_env.go (changed)
  job: import native OAuth results and show missing credentials as refusal
  names: ['/dash/me/env']
  never: label an absent personal key as an active operator fallback
- path: runed/api/v1/types.go (changed)
  job: carry the canonical payer beside the existing secret map
  names: [RunRequest.ModelUserSub, RunRequest.TriggerSender]
  never: interpret the harness document or widen runed.Turn
- path: runed/server.go (changed)
  job: validate payer and trigger against the authenticated caller before rendering
  names: ['POST /v1/runs']
  never: treat a missing payer or a scope alone as internal billing authority
- path: runed/secrets.go (changed)
  job: select personal-only or operator-only model credentials in one renderer
  names: [spawnSecrets]
  never: mix model credentials across owners
- path: runed/render.go (changed)
  job: emit ARIZUKO_MODEL_USER_SUB and refuse personal gateway execution
  names: [Turn.Env, Turn.Stdin]
  never: put a real model credential in process arguments
- path: runed/mounts.go (changed)
  job: withhold operator Codex login files from a personal turn
  names: [buildMounts]
  never: let a personal turn borrow HOST_CODEX_DIR
- path: ant/src/index.ts (changed)
  job: validate personal model authentication inside the delivering error boundary
  names: [runTurn]
  never: start an adapter after missing or ambiguous personal authentication
- path: ant/src/claude.ts (changed)
  job: make the selected authentication and endpoint authoritative across SDK configuration
  names: [claudeHarness]
  never: send a Claude subscription token to OpenRouter
- path: ant/src/codex.ts (changed)
  job: stage the user's native login and classify known auth and quota failures as terminal
  names: [codexHarness]
  never: persist personal auth.json in the shared group home
- path: ant/src/guards.ts (changed)
  job: scrub the added model keys and their SDK auth alias before shell commands
  names: [SECRET_ENV_VARS, bashScrub]
  never: claim that shell scrubbing prevents guest access
- path: container/secret_scrub_test.go (changed)
  job: pin the added canonical model keys and the derived bearer alias
  names:
    [
      TestEveryEnvProfileKeyIsScrubbedOrExplicitlyAgentDriven,
      TestScrubListNamesOnlyEnvProfileKeys,
    ]
  never: turn a runtime alias into a second stored credential
- path: authd/authstore/authstore_test.go (changed)
  job: test the checked identity reader and distinguish failed reads
  names: [OAuthIdentityForSub]
  never: treat an unavailable store as an absent account
- path: tests/dynamo/env_test.go (changed)
  job: consume the checked identity read in existing integration coverage
  names: [OAuthIdentityForSub]
  never: ignore its error result
- path: tests/dynamo/store_test.go (changed)
  job: retain identity integration coverage with the checked reader
  names: [OAuthIdentityForSub]
  never: infer success from the presence boolean alone
- path: authd/identity_test.go (changed)
  job: prove missing and failed identity reads have different HTTP outcomes
  names: [TestIdentityEndpoint_Resolves]
  never: infer read reliability from a successful lookup alone
- path: routd/identity_test.go (changed)
  job: exercise the complete principal join and its refusal cases
  names: [TestHTTPIdentity_Resolves]
  never: treat a role edge as a credential owner
- path: routd/model_credentials_test.go (new)
  job: prove payer binding, checked reads, acquisition ordering and tool-account isolation
  names: [TestModelCredentialDispatch]
  never: verify only the resolver in isolation
- path: store/secrets_test.go (changed)
  job: prove both write operations reject invalid native caches and strip refresh tokens
  names: [SetSecret, PutSecretRow]
  never: rely on dashboard validation
- path: runed/server_test.go (changed)
  job: prove omitted payers and inconsistent triggers refuse before rendering
  names: ['POST /v1/runs']
  never: accept an internal-looking label from an untrusted caller
- path: runed/secrets_test.go (changed)
  job: prove personal turns contain no operator model keys
  names: [TestSpawnSecrets_Personal]
  never: inspect only one key when testing credential ownership
- path: runed/render_test.go (changed)
  job: verify payer env and gateway refusal through the renderer
  names: [TestRenderTurn_Personal]
  never: accept a virtual key as evidence of personal upstream billing
- path: runed/mounts_test.go (changed)
  job: prove personal turns receive no host Codex login
  names: [TestPersonalCodexMounts]
  never: require 6/53 or lend host authentication to a personal turn
- path: runed/env_contract_test.go (changed)
  job: pin the Go producer and ant consumer of the payer env
  names: [ARIZUKO_MODEL_USER_SUB]
  never: exempt the new key from the source scanner
- path: ant/src/claude.test.ts (changed)
  job: observe actual SDK requests with competing shared-home settings and helpers
  names: [claudeHarness]
  never: test selection without observing the outgoing credential
- path: ant/src/codex.test.ts (changed)
  job: observe isolated auth, resume and terminal revoked-token, expiry and quota failures
  names: [codexHarness]
  never: leave a credential in a persistent rollout directory
- path: ant/src/index.test.ts (changed)
  job: prove credential validation submits a visible terminal error before adapter startup
  names: [runTurn]
  never: infer delivery from an adapter unit test
- path: dashd/me_env_test.go (changed)
  job: prove native imports validate and remain private to the signed-in subject
  names: ['/dash/me/env']
  never: store a native refresh token
- path: routd/secrets_test.go (changed)
  job: prove REST and derived MCP writes apply the native-cache storage contract
  names: ['POST /v1/secrets']
  never: accept API-key mode or persist imported refresh tokens
- path: cmd/arizuko/secret_test.go (changed)
  job: prove CLI imports apply the same native-cache storage contract
  names: [runSecretSet]
  never: bypass validation through SetSecret
- path: auth/surrogate/engine_test.go (changed)
  job: pin both OAuth protocols and existing provider behavior
  names: [Engine]
  never: call a live provider in unit tests
- path: dashd/me_connections_test.go (changed)
  job: prove OpenRouter consent binds to the signed-in account
  names: ['/dash/me/connections']
  never: expose returned tokens in HTML or JSON reads
```

Harness transport and removal of the `codex` grant are outside this change.
The runtime port stays `Run` and `Kill`; the existing env, stdin and mounts
carry all new data. No objstore method or managed entity is added. Existing secrets,
connections and turn resources retain their handlers and authorization gates.

## The join reads identity without granting authority

```text
trigger ── exact OAuth lookup (changed) ──► canonical account
   │                                               ▲
   └── direct added_by=pairing edge (new reader) ────┘
role/manual membership ── ownership inference (removed)
```

Current identity client, `routd/identity.go:21-23`:

```go
type IdentityResolver interface {
    Resolve(sub string) (ipc.Identity, []string, bool, error)
}
```

Proposed additional reader on `Server`; the HTTP client remains the same:

```go
func (s *Server) ResolveInitiator(trigger string) (string, error)
```

An ordinary sender resolves by exact value. Read its direct pairing edges
with `Store.ParentEdges`. Ignore role and manual membership edges. Resolve
the sender and any pairing parent through the existing authd identity client.
A pairing parent must be an existing canonical account, not an arbitrary ACL
principal. Accept one account only. A direct OAuth result and a pairing result
must agree. Missing identity, stale parent, multiple pairing parents, or
conflicting results produce a named refusal. A failed membership or authd read
produces a dependency error. Use `ErrInitiatorUnclaimed` and
`ErrInitiatorConflict` for permanent identity refusals. Keep dependency
errors distinct so dispatch can preserve the batch. Do not cache results
across turns.

Do not use `Server.resolveIdentity` at `routd/server.go:191`; that advisory
wrapper collapses errors. authd's storage reader also collapses failures at
`authd/authstore/auth_users.go:32-43`. Its existing signature is:

```go
func (s *Store) OAuthIdentityForSub(sub string) (userID, name, createdAt string, subs []string, ok bool)
```

Proposed signature:

```go
func (s *Store) OAuthIdentityForSub(sub string) (userID, name, createdAt string, subs []string, ok bool, err error)
```

Only absent rows mean `ok=false, err=nil`. The endpoint returns 503 on a
failed store read. Its existing bearer verification and `identity:read` scope
remain. No sender spelling, display name, email match, folder ancestry, or
transitive ACL expansion establishes ownership. JWT subjects use the existing
`auth.BareSub` boundary once; stored sender principals pass unchanged.

`ResolveInitiator` is the one join available to 6/56 and any other identity consumer.
No consumer may read `oauth_identities` directly from routd or invent
another membership table. The join returns ownership, not an authorization
verdict. ACL evaluation continues through `auth.Authorize` and the turn gate.

## Every personal run binds one payer

```text
inbound rows ── sender and topic batches (changed) ──► exact trigger
                                                        │ join (new)
                                                        ▼
                                      turn context with payer (changed)
                                                        │ same payer on retry (new)
                                                        ▼
                                             RunRequest (changed)
```

Current batching exception, `routd/dispatch.go:128-135`:

```go
var groups [][]core.Message
if web {
    groups = groupByTopic(trigger)
} else {
    groups = consecutiveRuns(trigger, func(m core.Message) string {
        return m.Sender + "\x00" + turnTopic(m)
    })
}
```

Proposed batching for every surface:

```go
groups := consecutiveRuns(trigger, func(m core.Message) string {
    return m.Sender + "\x00" + turnTopic(m)
})
```

The batch's last triggering row remains the initiating row. Ambient and bot
rows never select the payer. Two senders in one web topic get separate turns.
A forwarded row with an attested human sender uses that sender.
`ReturnTo` selects reply delivery only. The delegation writer at
`routd/steer.go:656` emits `store.DelegateSender`, not the original human.
That synthetic turn uses operator credentials; it cannot borrow the parent's
subscription without a separate delegation contract.
Elevation changes authorization, not the payer.

Current model read, `routd/dispatch.go:825` and `routd/dispatch.go:858`:

```go
caller := types.UserSub(turnCallerSub(trigger))
secrets := l.db.EnvProfileSecrets(string(caller))
```

Proposed resolution in `runTurn`, before the budget gate at
`routd/dispatch.go:389`:

```go
modelSub, err := l.srv.ResolveInitiator(last.Sender)
// Classify failures and reject a configured gateway before any model work.
// Bind modelSub through PutTurnContext before reading credentials.
```

Proposed personal read in `dispatchRun`; `modelSub` is its new argument from
that binding, not another identity lookup:

```go
caller := types.UserSub(turnCallerSub(trigger)) // remains the tool caller
secrets, err := l.db.EnvProfileSecrets(modelSub)
// Return read failures; pass the bound modelSub as RunRequest.ModelUserSub.
```

The internal-turn branch skips the personal reader. Move the turn binding
before credential resolution. Keep gateway budget and mint operations only
on the internal-turn branch. runed validates caller, trigger and payer before
rendering, then repeats the gateway refusal. At `runed/server.go:105`, retain
the verified bearer subject. An empty payer requires `service:routd` and a declared internal
`TriggerSender`. A missing verifier is not trusted. Reject an absent trigger,
a human trigger without a payer, or an internal trigger with a payer.
A `runs:run` scope or request `CallerSub` alone cannot authorize operator billing.

`model_user_sub` is the bare canonical account subject. Add it to the existing
`TurnContext` and `RunRequest`; it is nonsecret attribution, not another
principal declaration. Bind it with `OpenTurnContext` before spawn. A retry
must resolve the same account and guard the persisted value against change.
An unpair, account change, or failed revalidation refuses the retry. An
existing personal turn with no bound payer also refuses; do not guess a payer
for a process admitted without this contract. Read fresh credentials for
each new process. Never store a token in the turn row.

Reject anonymous web and hook sources before classifying internal triggers.
Operator model credentials apply only to `core.IsScheduledSender` and the
existing `store.SyntheticSenders` declarations at `store/messages.go:379`.
Do not reuse `isSystemTrigger`'s broad prefix match as billing consent. These
internal turns carry an empty `ModelUserSub` and cannot read personal model
rows. A timer or agent delegation does not inherit a human's plan.
An unclaimed sender, anonymous chat link, hook source label, or
unauthenticated `operator` sender
gets a visible request to authenticate and pair. A source label is not a
human account, even if it resembles a username.

This is a deliberate restriction on anonymous and hook-triggered execution.
Do not infer a human from a hook token's destination or owner. Support for a
new authenticated ingress must establish its sender at ingress, not in this
credential resolver.

runed's active-folder admission already returns `Busy` at
`runed/manager.go:207-209`. Keep that rule: a later sender waits for a separate
process. Do not add cross-user steering. Any queued inner-loop input that
cannot prove the original initiating sender must wait for a fresh run.
Topic transcripts retain their existing group scope; payer isolation does
not make the conversation private from other group members.

## Personal model credentials replace the operator set as a whole

```text
personal payer (new) ──► canonical user's model keys ──► ant selection (changed)
operator turn ─────────► operator model keys ─────────► ant selection
shell grants ──────────► existing tool/shell path
```

Current merge, `runed/secrets.go:38-41`:

```go
func spawnSecrets(cfg *core.Config, override map[string]string) map[string]string {
    s := mergeSecrets(readSecrets(), override)
    if cfg.ModelGatewayURL == "" {
        return s
    }
```

Proposed signature and selection:

```go
func spawnSecrets(cfg *core.Config, req runedv1.RunRequest) (map[string]string, error) {
    if req.ModelUserSub != "" {
        if cfg.ModelGatewayURL != "" {
            return nil, errors.New("personal model billing is unavailable through this gateway")
        }
        return maps.Clone(req.Secrets), nil
    }
    // Admission already requires service:routd and a declared internal trigger.
    // Keep the existing operator/gateway rendering for that admitted turn.
}
```

routd includes only that account's env-profile model rows in the personal
model set. Existing explicitly granted shell keys retain their existing path.
runed must not add host model env or `HOST_CODEX_DIR` to a personal request.
At `runed/mounts.go:174`, require an admitted internal turn before lending
host login files. Combine this condition with 6/53's mount rule if it lands.
runed knows no backend names.

The renderer always emits `ARIZUKO_MODEL_USER_SUB` as the request's
nonsecret payer, with an empty value for an internal turn.
Ant validates credentials after the existing harness selection, inside the
delivering error boundary. Per-run authentication and endpoints are authoritative
across inherited env, native caches, and every loaded settings source.
Claude's project, local, user and managed settings must not restore competing
authentication, `apiKeyHelper`, or endpoint overrides after SDK env construction.
The [settings reference](https://code.claude.com/docs/en/settings-reference#env)
defines settings env and credential helpers. `runed/settings.go:25` preserves
settings env. `ant/src/claude.ts:555` enables project and user sources.
Use per-run configuration to exclude or neutralize those competing fields
without writing the personal credential into persistent settings. If the SDK
cannot enforce this precedence, refuse before a model call.
An empty or incompatible personal credential produces a terminal error submission.
No API-key fallback may consume paid credits when a subscription is missing.
Direct Codex and OpenRouter require explicit rules for their vendor hosts
through the existing network resource. A connection or harness selection
grants no egress. The seed at `store/seed.go:20` covers Anthropic hosts only.

Model secrets remain env-profile credentials under 5/14. The real credential
crosses the trusted routd-to-runed hop and reaches the guest through the
existing stdin secret map. It is readable by the guest's user. This design
does not promise confidentiality between group members. `runed/mounts.go:56`
mounts a shared, writable group home. A prior turn can leave code that captures
a later user's token. `runed/secrets.go:65-71` states that guest code can read
credentials. Separate processes and env scrubbing do not close that boundary.

Gateway virtual keys remain operator credentials for system turns.
`user_id` metadata alone never satisfies personal billing. Do not mint a key,
claim a personal run, or report a successful personal connection as usable
through a gateway that lacks upstream credential selection. Existing gateway
budget gates and accounting continue for operator turns. Direct personal
turns have no LiteLLM spend record or enforced gateway cap. They use the existing unmetered response at `routd/reads_http.go:295`.
They must not show zero spend as a measured bill. A gateway or cost cap never justifies charging
the operator after personal authentication fails.

## Enrollment uses one secret store and the provider's real protocol

```text
native vendor OAuth ── imported result (new) ─┐
OpenRouter OAuth ── surrogate engine (changed)┤
                                            ▼
                            secrets user row (changed)
                                            │ selected value (new)
                                            ▼
                               existing ant adapters
```

The first release uses native OAuth enrollment for Claude and Codex, with
import through `/dash/me/env`. It does not offer an invented Claude.ai or
ChatGPT OAuth client. The user completes the vendor's own login and imports
the resulting credential privately. Import is not a new account association;
`requireUser` fixes the destination to the authenticated canonical subject.
Native enrollment versus an embedded login is question 2.

Provider facts checked on 2026-11-07. `ant/Dockerfile:173` installs Codex
without a version pin. Record the actual image digest, CLI and SDK versions
with the provider activation evidence. Do not reuse the filing's probe
versions as the release contract.

- [Anthropic's credential rules](https://code.claude.com/docs/en/legal-and-compliance)
  distinguish hosted native login from collecting Claude.ai tokens in another
  application. The token-collection design needs a permitted vendor arrangement
  before Claude enrollment is enabled. A successful HTTP probe grants no such
  permission. Keep Claude connection unavailable with an explicit explanation
  when that prerequisite is absent. Do not substitute an API key or forge a client.
- [Official Codex authentication](https://learn.chatgpt.com/docs/auth) distinguishes
  ChatGPT subscription login from API billing and documents copying the native
  auth cache to a headless host. It does not document a general arizuko OAuth
  registration. Use the native cache's format with the release image's CLI.
- [OpenRouter PKCE](https://openrouter.ai/docs/guides/overview/auth/oauth) returns
  a user-owned API key charged to OpenRouter credits. It is not proof of a
  Claude or ChatGPT subscription. Its redirect uses `callback_url`; its exchange
  posts JSON to `https://openrouter.ai/api/v1/auth/keys` and returns `key`.
- [OpenRouter's SDK integration](https://openrouter.ai/docs/guides/community/anthropic-agent-sdk)
  and [Claude Code integration](https://openrouter.ai/docs/guides/coding-agents/claude-code-integration)
  describe an Anthropic-compatible endpoint. Verify the release SDK against
  that endpoint before enabling this connection for execution. Documentation is not
  a live billing measurement.

Extend `EnvProfileKeys` once with `CODEX_AUTH_JSON` and `OPENROUTER_API_KEY`.
The former contains a native ChatGPT auth cache, not an arizuko wrapper. Reject
API-key mode, empty access tokens, and malformed native tokens on import.
`store/secrets.go` owns one shared write preparation operation used by
`SetSecret` and `PutSecretRow` before sealing. It validates `CODEX_AUTH_JSON`
and removes its refresh token. Dashboard, REST, derived MCP and CLI writes
all use that operation; dashboard validation alone is insufficient.
Keep the existing secret-size limit unless a measured native cache exceeds it.
Do not add a model credential table or a provider association table.

| Selected adapter | Personal credential       | Required behavior                                                                                                                                   |
| ---------------- | ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Claude           | `CLAUDE_CODE_OAUTH_TOKEN` | Use the user's native subscription token only, when vendor permission permits enrollment.                                                           |
| Claude           | `OPENROUTER_API_KEY`      | Use `ANTHROPIC_BASE_URL=https://openrouter.ai/api` and `ANTHROPIC_AUTH_TOKEN` derived from that key; clear Claude subscription and API credentials. |
| Codex            | `CODEX_AUTH_JSON`         | Present a native ChatGPT login to Codex; never reinterpret `OPENAI_API_KEY` or `CODEX_API_KEY` as subscription access.                              |
| Pi               | Any                       | Retain the existing explicit MCP refusal. No quota or identity workaround.                                                                          |

Claude accepts exactly one of its two compatible credentials. If both are
connected, refuse with instructions to disconnect one. Codex ignores Claude
and OpenRouter credentials. Ant removes unselected model values before adapter
invocation. No new provider selector, backend, model catalogue or Go harness
schema is needed. Model and effort follow the existing harness configuration.
A model that the selected provider rejects fails visibly. The platform never
rewrites the model.

Codex uses a fresh directory under the guest's temporary filesystem as
`CODEX_HOME`, with auth mode 0600. Retain the existing group rollout storage
through a link to the persistent rollouts directory. Stage the existing
Codex hooks there too. Never read the group's saved `auth.json` or operator
`config.toml` as personal authentication. Remove the private directory in a
`finally` path. Guest termination removes it after a crash.
Validate stored native caches and strip refresh tokens before guest staging.
Ant checks the native access-token expiry and refuses expired access on spawn. An opaque Claude token
with no expiry metadata must rely on a visible vendor rejection.
Native tokens require manual reconnection after expiry. The guest cannot
rotate a refresh token.

The engine selects OpenRouter's wire behavior using its existing provider name
from `auth/surrogate/registry.go:146`, passed to `Engine.AuthorizeURL` and
`Engine.Exchange` at `auth/surrogate/engine.go:139` and `auth/surrogate/engine.go:170`.
The provider file owns the documented URLs and `secret_key=OPENROUTER_API_KEY`.
It requires S256 PKCE and state, not `SURROGATE_OPENROUTER_CLIENT_SECRET`.
Preserve the existing GitHub and Google protocol. OpenRouter supplies no refresh
credential in the documented exchange; store an empty refresh and zero expiry.
Do not claim automatic renewal or provider-side revocation without a documented
endpoint. The existing local disconnect still removes the user's credential.

Current shared connection write, `dashd/me_connections.go:235`:

```go
ss.PutOAuthSecret(sub, p.SecretKey, tok.Access, provider, tok.Refresh, tok.ExpiresAt, tok.Scope)
```

Current connection state, `dashd/me_connections.go:185`:

```go
state := auth.SignState(d.stateSecret, auth.StateIntent{})
```

Proposed state on the same cookie path:

```go
state := auth.SignState(d.stateSecret, auth.StateIntent{
    Intent: "surrogate:" + provider,
    LinkFrom: sub,
})
```

The callback compares both signed fields with its provider and authenticated
subject. Reuse `VerifyState`, `WriteStateCookie` and `ConsumePKCE`.
Keep the shared secret sink for OpenRouter. Bind the start, provider, state, PKCE verifier
and callback to the same authenticated subject. Reject a callback after an
account switch, missing verifier, failed exchange, or changed provider. Do not
return values in connection lists. The env import form uses the existing
write-only secrets operation and user boundary; a successful import creates
no grant and no connection to another account.

## Credential acquisition fixes the attempt's snapshot

```text
credential acquisition (changed) ──► immutable attempt snapshot (new) ──► process start
disconnect ── delete row ──► subsequent acquisition refuses (new)
```

`DB.EnvProfileSecrets` gains an error result. A model read or decrypt failure
cannot become an empty set. A successful credential acquisition fixes the
attempt's snapshot. Disconnect blocks acquisitions after deletion, but an
already admitted attempt may still start. Checked reads must observe a completed
delete. Retries acquire fresh credentials. A guarantee tied to actual process
start requires additional coordination and is outside this contract.

No first-release model credential supports refresh. Keep checked reads and
explicit reconnection; connector refresh remains outside this change.
Local deletion cannot recall a copied bearer or stop an active guest's spending.
The vendor may reject the token sooner. This delivery boundary is question 1.

| Failure                                                         | Visible result                                       | Queue behavior                                                               |
| --------------------------------------------------------------- | ---------------------------------------------------- | ---------------------------------------------------------------------------- |
| Unpaired, anonymous, conflicting or stale identity              | Authenticate/pair or repair the association          | Consume once; do not spawn or trip the folder breaker.                       |
| authd, membership, secret read or decrypt outage                | Credential service unavailable                       | Preserve the batch through `queue.ErrDependencyDown`; no model call.         |
| Missing, expired, ambiguous or incompatible personal credential | Connect or reconnect the named model credential      | One terminal refusal; no operator fallback or retry loop.                    |
| Personal request with configured model gateway                  | Personal billing unavailable through this gateway    | One refusal before budget/mint/spawn.                                        |
| Vendor permission or adapter support absent                     | Provider unavailable, with the required prerequisite | No enrollment presented as usable and no model request.                      |
| Known vendor authentication or quota failure during execution   | Delivered terminal adapter error                     | Codex sets the existing result `terminal` field; no retry or account switch. |
| Disconnect after credential acquisition                         | Connection removed; admitted attempt keeps snapshot  | Subsequent acquisitions refuse; the admitted attempt may still start.        |

Logs and the turn row may contain the trigger, canonical payer, turn ID and
credential key name. They must never contain access tokens, refresh values,
auth-cache JSON or a reversible credential reference. Existing per-daemon
audit paths record credential mutations; do not add a billing audit store.

## Acceptance proves the outgoing account and the refusal paths

These are implementation gates, not claims that this draft implements them.
Each behavior test must fail when its named boundary is removed.

- Identity tests seed authd OAuth aliases and real pairing edges separately.
  Telegram and Discord senders paired to one canonical account read that
  account's row. Direct GitHub and authenticated web subjects reach the same
  join. Role edges, manual memberships, a stale parent, conflicting mappings,
  and two pairing parents cannot select a credential. An authd store outage
  returns 503, while an absent identity returns the existing null result.
- Dispatch tests send two users through the same web topic and group, then
  capture real HTTP `RunRequest` bodies. Each request contains only its payer's
  model keys. Include a guest, forwarded message, `/root`, retry after unpair,
  and timed/system/delegation triggers. Anonymous links and hook source labels
  refuse, including a hook label equal to a synthetic sender.
  Removing the web sender split or replacing `modelSub` with raw `caller`
  must fail the tests. Prove existing tool-secret calls still receive the raw
  caller and never the new canonical payer.
- Secret tests inject read and decrypt failures; no model request follows.
  With barriers, acquire a credential, delete it, then start the admitted
  attempt; its snapshot remains valid. Delete before acquisition and require
  refusal, including retries. A native import cannot rotate a refresh token
  inside the guest.
- Renderer tests start with populated operator Claude, OpenAI and Codex keys,
  host Codex login files, a personal model set and granted shell credentials.
  Assert all operator model material is absent from a personal turn's env,
  stdin and mounts. Admission tests omit the payer on a human request and
  mismatch payer/trigger fields. Internal-looking labels from untrusted callers,
  and a missing verifier, refuse before rendering. Test trusted internal turns
  separately. A gateway personal request must fail before `/key/generate`;
  its `user_id` cannot make the test pass.
  The existing Go/TS env scanner must detect a missing payer env emission.
  Extend the existing secret-scrub partition test for both new keys and the
  SDK-derived `ANTHROPIC_AUTH_TOKEN` alias without adding that alias to storage.
- Adapter tests observe requests with fake credentials, rather than asserting
  selection helpers alone. Claude and OpenRouter must hit the correct endpoint
  with the selected user's token through the actual release SDK. Populate
  shared-home user, project and local settings with competing credentials,
  `apiKeyHelper` and endpoint overrides; cover managed settings too.
  Assert the helper never runs and no competing endpoint receives a request.
  Competing connected credentials refuse. Codex exec and resume must use the
  private native cache, retain rollouts and hooks, and leave
  no personal cache in the group home. Missing credentials submit a visible
  error and terminal exit before SDK or CLI invocation. Codex's known auth and
  quota errors set `terminal` on both failed events and CLI-exit results,
  including revoked tokens and mid-run expiry; dispatch must not retry them.
  `ant/src/codex.ts:151` and `ant/src/codex.ts:172` currently omit that field.
  Pi retains its refusal.
- Enrollment tests exercise the existing connections and secrets surfaces.
  Verify OpenRouter's JSON exchange, S256, subject-bound state, account-switch
  rejection, local disconnect and token-free lists. Native imports reject an
  API-key Codex cache, malformed tokens and empty access tokens through dashboard,
  REST, derived MCP and CLI writes. Inspect stored values to prove every door
  strips refresh tokens; stage pre-existing invalid rows and require refusal.
  Folder-scoped model keys remain invalid. GitHub and Google tests must pass.

A disposable smoke runs two accounts against distinct controlled upstreams on
one instance. Capture which account each upstream receives, then verify each
reply reaches its originating chat. Repeat with one account disconnected,
a shared topic, an operator-only system turn and gateway refusal. Provider
activation also needs a permitted real-account enrollment and one measured
request showing the intended quota or credit account. Old rate-limit headers,
a fake upstream, and gateway attribution alone do not prove subscription billing.
No live-instance anteval run belongs to this change.

Verification commands for implementation:

```sh
go test ./authd/... ./auth/surrogate ./store ./routd ./runed/... ./dashd ./cmd/arizuko ./container ./tests/dynamo -count=1 -short
make -C ant test
make build
make lint
make test
speclint specs
```

Store tests require the DynamoDB endpoint described in `tests/README.md`.
`make lint` owns the doc gates:

```sh
go test ./tests/webdocs ./tests/specrefs ./tests/bugsindex -count=1
```

Draft verification runs the full spec corpus and those doc gates. Implementation
must also satisfy `specs/CLAUDE.md`: dashboard controls, repository and web docs,
image rebuild and truthful cost displays. No implementation status follows
from a passing Markdown or spec-reference check.

## Open questions

1. Approve direct delivery to a guest with a shared, writable group home, or
   extend the existing gateway so the guest never holds the token?
   `runed/mounts.go:56` persists group state; `runed/secrets.go:65` permits guest
   code to read credentials. A prior turn's code can capture a later user's
   token. Direct delivery also requires gateway refusal and unmetered execution.
   Disconnect blocks later acquisitions, but an admitted attempt may still start;
   local deletion cannot recall a copied bearer or stop its spending.
   **Recommend extending the existing gateway** to retain host-held credentials
   and the requested proxy security and auditing. **Caveat:** personal execution
   must wait for verified upstream credential binding and revocation behavior.
2. Approve native credential import with manual reconnection for Claude and
   Codex, plus embedded OpenRouter PKCE, or require embedded native enrollment?
   `auth/surrogate/engine.go:129` provides no public native-client login.
   **Recommend native import and OpenRouter PKCE** because these use the vendors'
   existing enrollment paths. **Caveat:** native imports need manual reconnection
   after expiry. Provider permission remains an external prerequisite; owner
   approval cannot authorize collection. Provider scope and separation from tool
   authority are already settled.
3. Approve operator-paid timers, system turns and delegation, with anonymous
   and hook execution refused? `routd/steer.go:656` emits `store.DelegateSender`;
   `webd/route_token.go:300` accepts a hook source label, not a human payer.
   **Recommend this funding and ingress policy** because internal automation
   has no initiating human subscription. **Caveat:** the operator pays internal
   usage, and anonymous links and hooks cannot start model execution.
