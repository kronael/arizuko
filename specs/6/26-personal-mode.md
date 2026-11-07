---
status: shipped
shipped: 2026-09-03
depends:
  [
    ../5/19-hitl-firewall,
    ../5/13-ext-mcp,
    ../5/31-identity-pairing,
    ../5/32-acl-unified,
    ../5/35-hermes-integration,
    ../5/A-primitives-framing,
    27-git-channel,
  ]
---

# specs/6/26 — personal mode: one human, one terminal, one approval per call

> A single operator wants their agent (`atlas`) without a chat platform: talk
> to it from a terminal, let it act on their GitHub, and answer each risky call
> with **yes / no / always** — in the channel or in a browser.
>
> Almost all of it ships as the platform. This spec records what that is.

## Why not run Hermes directly

Because the harness is the cheap half. [`5/35`](../5/35-hermes-integration.md)
measured it: Hermes's adapter interface and `chanlib.BotHandler` are the same
verb set differently spelled, and 26,102 of its 39,005 gateway lines are
platform adapters. What a harness does not carry is what a single human needs
the day after the first turn works — persistence with search
([`5/C`](../5/C-message-mcp.md)), an audit row per state transition written in
the mutation's own transaction ([`5/I`](../5/I-tool-call-logging.md)), a grant
checked on every tool call ([`5/32`](../5/32-acl-unified.md)), credentials
brokered on the host so they never enter the agent's environment
([`5/13`](../5/13-ext-mcp.md)), per-folder egress, and the hold gate
([`5/19`](../5/19-hitl-firewall.md)).

Personal mode is **not a smaller arizuko**. It is the same platform with one
folder and one human, and what you pay for is the approval gate and the
credential broker, not the chat.

## Decision 1 — personal mode is a topology, not a build flag

[`5/A`](../5/A-primitives-framing.md) decision 6 (user-locked 2026-08-09)
settles this: the smallest real deployment is `routd` + `dashd` + `onbod` +
`timed` with **no channel adapter at all**, and what crosses the boundary
crosses as REST or MCP. Personal mode adds no flag, no profile, no second code
path — it is that topology with one group folder. Slack is not removed; it is
simply not started.

## Decision 2 — the terminal is an MCP client, not a new daemon

`POST /chat/{token}/mcp` already serves `list_groups`, `send` and
`get_history` over streamable HTTP (`webd/mcp.go`) — the SAME tools
`POST /mcp` serves, authorized by a `route_tokens` bearer that pins the
destination set to ONE folder ([`5/W`](../5/W-webhook-routes.md), BUGS
L139). Any MCP client — Claude
Code on the laptop included — attaches to the bot with a URL and a token, from
anywhere, with no operator shell and no DB access.

Two host-local paths stay beside it: `arizuko send -i <inst> <folder> "<msg>"
--stream` (operator-direct, writes the inbound straight to the DB) and
`arizuko chat -i <inst>` (an interactive Claude Code on the ROOT MCP socket — an
operator tool, not a conversation with the persona).

**`arizuko talk`**: a REPL that is a thin client of the MCP surface and
nothing else. It holds no DB handle and knows no folder; it sends, then
streams the SSE. The third consumer of one surface, never a fourth path into
the queue.

### The `talk` interface

Command-line first. The common case takes no arguments:

```
arizuko talk                    # REPL
arizuko talk --prompt "what broke?"   # one turn: send, print, exit
arizuko talk --prompt                 # same, prompt from stdin
arizuko talk @work --prompt "…"       # a named entry instead of the default
arizuko talk -g eng/sre               # a folder other than the resolved default
arizuko talk --thread bugfix          # a different thread, same folder
```

**`--prompt` is one iteration of the REPL loop, not a second code path.** Same send,
same read, same printer — otherwise the one-shot and interactive forms drift,
which is what "never a fourth path into the queue" forbids.

**Authenticate the caller; let the caller choose the destination.** `talk` holds
an authd-minted ES256 bearer and speaks `POST /mcp` — the surface that already
offers `list_groups`, `send` (gated by `auth.MatchGroups(grants, folder)`) and
`get_history`. It does NOT carry a route token: a capability token pins one JID
and carries no identity, so every destination would need its own token and
nothing could enumerate them. Authenticating the human and authorizing the
folder is the mechanism arizuko already has everywhere else; a second one for
the CLI is the duplication this spec exists to avoid. See BUGS `L139` for the
wider collapse this implies.

`proxyd.tryAuth` accepts `Authorization: Bearer <ES256>`, verifies against
authd's JWKs and stamps `X-User-*`; `webd.identified` then requires that stamp
plus `auth.ProxydTransit`. So `talk` points at the PUBLIC proxyd URL, never at
webd directly, and the token is minted by an existing command —
`arizuko token -i <inst> issue bearer <folder> --scope`.

**Config — minimal; the common case has no section header.**

```toml
# ~/.config/arizuko/talk.toml
url   = "https://krons.fiu.wtf"
token = "…"            # authd ES256 bearer, not a route token

[work]
url   = "…"
token = "…"
```

Precedence: flag > `@name` > top-level > `ARIZUKO_CHAT_TOKEN` > an error that
prints the file to write. Refused unless mode is `0600` — it holds a bearer, the
reason `psql` refuses a group-readable `.pgpass`.

**Folder resolution, in order:** `-g` > a `group` key on the entry > if
`list_groups` returns exactly one, that one, silently > otherwise an error
listing what the caller can reach. That last rule is what makes "one chat per
user needs no arguments" true without hardcoding a default.

**The thread default is the fixed literal `talk`** — no last-used cache, so there
is never a "which thread am I in?".

The bearer is minted by the operator and pasted once; there is no device flow.

## Decision 3 — GitHub arrives as a connector, not as a channel

Two shapes exist and they are different products:

- **Connector.** The connector is GitHub's HOSTED MCP endpoint
  (`https://api.githubcopilot.com/mcp/`): an endpoint needs no runtime, and
  routd's image has no node and holds no docker socket by design.
  `template/connectors/github.toml` declares it;
  [`5/13`](../5/13-ext-mcp.md) shape 2 discovers its tools per folder with that
  folder's credential, narrows the folder's secrets to the keys the connector
  declared, gates each call on `mcp:<tool>`, renders the token into the
  Authorization header, and scrubs secret values from the result.
  `auth/surrogate/providers/github.toml` supplies the user-consented token
  ([`5/15`](../5/15-surrogate-oauth.md)) into the same `GITHUB_TOKEN` slot, so
  the agent acts as the human rather than as a shared PAT. GitHub as a **tool
  the agent calls**.
- **`gitd` ([`6/27`](27-git-channel.md)).** GitHub as a **channel that calls
  the agent** — each repo a folder, each PR and issue a thread. The right
  design for a different want: an agent that watches a repo, not a human who
  asks their agent to open a PR.

Personal mode takes the connector.

### The host `.env` is the wrong door, and it fails silently

An operator reaching for "let atlas use `gh`" puts `GH_TOKEN` in the instance
`.env`. That reaches **nothing**: `readSecrets` (`container/runner.go`) iterates
`store.EnvProfileKeys` and passes only those five model credentials into a
container. Every other name in `.env` is an infra credential that stays on the
host by construction (`5/14` tier 3). Measured on marinade 2026-08-28 —
`GH_TOKEN` and `BK_TOKEN` are both present in `.env` and neither reaches the
agent. Nothing logs a warning, which is what makes it worth writing down.

A capability credential lives in the `secrets` table, folder- or user-scoped,
and reaches a connector subprocess through the broker per call
(`ipc.CallConnectorTool`), which is also what puts a `secret_use_log` row under
every use. `template/connectors/github.toml` and `buildkite.toml` carry the
wiring; the token goes in with `arizuko secret set` or `/dash/me/secrets`.

### The CLI route: a narrowed grant (v0.66.0)

A `shell:env:<KEY>` acl row names ONE secret that may enter the container's
shell env, the broker resolves it per spawn, and no row means nothing crosses.
Model credentials are excluded even under `shell:env:*`, and the action is
exact-match, so an operator's `**` row does not confer it (BUGS `L134` for the
channel-scoping limit, `grant_action_needs_a_door` for why `grantableAction`
must accept the namespace before any row in it can be written).

**Three bounds, which are why the grant is per-key and why the PAT's own scope
is the real lock:**

- **It cannot be gated.** `gh pr review --approve` is a shell string. arizuko's
  gate is `CheckHold` at the one `tools/call` interception; a Bash command is
  one tool call carrying opaque text, so "block PR approval" degrades to
  matching substrings, which `${A}${B}` defeats. An operator who asks for both
  a shell token and a block on approvals has asked for two things that cannot
  both hold.
- **The connector route gives the block for free.** Each remote tool is its own
  broker tool name, so `hold:mcp:github_<tool>` suspends it for human approval
  (`5/19`) and a deny row refuses it outright. This is why Decision 3 picks the
  connector: not because a CLI is inelegant, but because per-call authority is
  only expressible when the call has a name.
- **It widens the shell blast radius.** Anything in container env is readable
  from the agent's shell on pi and, unless scrubbed, on claude (BUGS `L131`).

**The real enforcement is the token's own scope.** An `acl` row refuses a tool;
only the credential bounds what that tool may do once called. A fine-grained PAT
without `pull_requests: write`, or a Buildkite read-only token, is the bound
that holds even if every arizuko gate is wrong. Every agent action attributes
to the consenting human — GitHub renders a bot badge only for an App
installation, never for a PAT or a user OAuth token — so "cannot approve PRs"
rests on the token's scope; an `acl` deny row is the second lock, not the
first.

## Decision 4 — HITL is the approval gate

A human in the middle of a TOOL CALL, shipped 2026-08-07
([`5/19`](../5/19-hitl-firewall.md)). A `hold:mcp:<tool>(param=glob)` acl row
makes `CheckHold` suspend the call at the one `tools/call` interception in
`ipc.serveConn`, write a `pending_actions` row and notify the chat. The
original agent re-issues on approval, in its own turn, container, session and
grants; release is one-shot and keyed on the canonical-JSON hash of the args,
so edited args are held again.

So "granular approvals" is already granular — per tool, per argument glob, per
folder. Do not build a second gate.

## Decision 5 — three verdicts, and none of them is a memory

The operator answers a held call the way Claude Code asks: **yes · no ·
always**. Two are one-shot rows, one also writes an `acl` row, and both gates
re-read `acl` on every call ([`5/32`](../5/32-acl-unified.md) § "No caching,
deliberately"). The agent therefore stores nothing and remembers nothing — it
learns the standing answer from the OUTCOME of its next call.

| Verdict                | The call in hand                       | Also writes                                                 | Next identical call    |
| ---------------------- | -------------------------------------- | ----------------------------------------------------------- | ---------------------- |
| **yes** (`/approve`)   | `approved` (one-shot, args-hash keyed) | —                                                           | held again             |
| **no** (`/reject`)     | `rejected`                             | —                                                           | held again             |
| **always** (`/always`) | `approved`, exactly as `yes`           | skip-approval: `hold:mcp:<tool>(<params>)`, `effect='deny'` | runs inline, no notice |

`always` uses chat `/always <id> [note]` or `standing=true` on
`POST /v1/pending_actions/{id}/approve`. A tool must declare keys for the
standing rule. The held call must supply a scalar value for each key.
The approvals page offers the third button only when the rule is valid.

**`always` is `yes` plus a rule — never a third verdict on the row.**
`pending_actions` keeps its states and `resolvePendingAction` keeps
rejecting unknown verdicts (`TestResolvePendingAction_RejectsUnknownVerdict`);
the standing answer lives in `acl`, where every other standing answer already
lives. The verdict and its acl row commit in the SAME transaction as the
resolution message, as `routd.resolveHoldInto` commits the verdict
and its trigger together — otherwise a crash leaves a call approved with no
standing rule, or a rule with no approved call.

**Name it "skip approval", never "deny the tool".** The row denies the `hold:`
action, not the tool — `deny` is the right effect because `Authorize` treats
every other value as ALLOW (`authorize.go:73`), so inventing a third effect
would be the `5/19` Hazard 1 trap in new clothes. But an operator reading
"deny" next to a button that lets a call through will misread it. The wire
keeps `deny`; every surface says **skip approval**.

### The mechanics behind `always`

1. **`CheckHold` uses `Policy.Allows`.** Matching local rows decide first.
   The defaults in `auth/defaults.go` decide only when no local row matches.
   Deny wins within the selected layer. A matching deny means skip approval.
   A local hold can therefore override a default skip. Removing a local
   skip exposes the remaining policy. `actionCovers` accepts an exact hold
   action or its namespace wildcard; other actions do not cover holds.
2. **`CheckGrant` checks the row that the ACL handler writes.** The handler
   builds the mutation from `aclArgsRow` and `store.GrantDefaults`.
   Added allows and removals outside `hold:` require covering authority with
   the grant option. Hold mutations require admin authority without that
   option. Adding a non-hold deny narrows access. Scope, params and predicate
   restrictions apply through `auth/grant.go`. The write proves the policy
   version from the check. This check also applies to elevated turns.
   Revocation addresses the exact params string in the row's key.
   `auth.ValidateParams` checks the argument matcher before an add.
3. **The generic tool gate cannot match a shaped deny.** The hold path
   passes arguments (`stringArgs(args)`), but the authz path does not:
   `authorizeCall(name, nil)` (`ipc/ipc.go`) hands `Authorize` a nil params
   map. `paramsMatch` requires each named key to be present. A deny shaped
   `mcp:<tool>(repo=x/*)` therefore matches nothing at this gate.
   `always` uses the held call's arguments and can match a shaped row.
   `routd/hold_shaped_never_test.go` pins the REASON, not just the rule: the
   same shaped deny is inert under nil params and decisive once arguments
   travel, and the hold path is shown carrying them. Removing the restriction
   therefore requires removing the reason. Carrying arguments into the authz
   path is its own change, with every tool in its blast radius.

Undoing `always` is removing the skip-approval row: it writes ONE row, on the
`hold:` action, and removing that row is the whole undo — there is no second
row whose provenance is in doubt. The two surfaces say which row: chat answers
`/always` with the `remove_acl` call that undoes it, and the approvals page
points at the group's grants table. The `always` writer records
`granted_by = "always:<reviewer>"`, which is a trace, NOT provenance —
nothing reads it, and it does not survive the row being re-created by hand.
`acl` rows carry no verdict identity, so two identical denies are
indistinguishable.

### The predicate is part of the verdict

`/always` pins only the tool's declared keys. For `like`, the standing rule
pins `reaction` and leaves the target message unconstrained:

```text
principal: folder:solo
action: hold:mcp:like
scope: solo
params: reaction=👀
effect: deny
```

`pinnedSpec` requires a scalar value for each declared key. `auth.ParamsSpec`
refuses values with commas, stars or surrounding spaces. An operator can write
a broader rule through `add_acl` after its grant check.

`routd.withMatcher` derives `ask`, `matcher`, `rule_text` and `matcher_error`.
The chat notice, REST row, approvals page and writer read those fields.
The writer uses `p.Matcher` without rebuilding the rule.

## Decision 6 — approval delivery: one decision, three sinks

`5/19` owns the decision and both resolution paths (`/approve <id>` in chat,
`POST /v1/pending_actions/{id}/approve|reject`, both funnelling through
`routd.resolveHoldInto` in one transaction). The DELIVERY the operator sees has
three sinks.

**In a browser (2026-08-24).** The held notice carries
`<WEB_HOST>/dash/approvals/?id=<id>`, and the page moves that row to the top;
an id that already has a verdict, or none at all, is reported rather than
silently absent. With `WEB_HOST` unset the notice carries the chat commands and
no broken URL. The link names WHICH decision; the page's existing operator gate
names WHO — the notice lands in the chat the agent shares with its USERS, so a
bearer there would hand the verdict to everyone who can read the chat
(`5/35`: a URL which travels never carries verb authority).

**In the channel (2026-09-02/03).** `SendRequest.Options
[]Option{label,data}` rides `POST /send`, and `chanreg.HTTPChannel.SendOptionsCtx`
is the one fold: an adapter without the `buttons` cap never receives an options
field, it receives the answers written into the text. teled, discd and slakd draw
them natively and map a press to an ordinary `InboundMsg` whose Content is the
option's data, so press and typed command converge on the one `/approve` parser
before any turn — a verdict never burns a model call. Detail in
[`5/19`](../5/19-hitl-firewall.md) §"Buttons".

**Reactions.** A REACTION is the answer surface every platform already has,
and every adapter already delivers one as an ordinary inbound. A green/red
reaction on a held notice is rewritten into its verdict at ingest (`5/19`
§"Reactions"), which covers whapd — the one adapter with no send path for
buttons — with no new message kind. 👍/👎 are excluded on purpose: an ordinary
social reaction is not an approval.

Native render is worth having on three of four adapters, and the fourth is why
the text fallback is not a placeholder (measured 2026-08-24):

| Adapter   | Library                      | Native buttons                            | Cost                                                                                                                                                                                                                                                                                                            |
| --------- | ---------------------------- | ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **teled** | telegram-bot-api v6.5.0      | inline keyboard; `CallbackQuery` in types | lowest — `callback_data` caps at 64 B, and `NewPendingID` is already 8 chars for this reason; must call `answerCallbackQuery` or the client spins                                                                                                                                                               |
| **discd** | discordgo v0.29.0 (vendored) | `components.go` + `interactions.go`       | low — interactions arrive as `INTERACTION_CREATE` on the gateway it already holds; ack in 3 s. **Only on the bot path**: `discd/bot.go:47` also identifies as a user account, and interactions are an application feature                                                                                       |
| **slakd** | hand-rolled HTTP             | Block Kit out via `chat.postMessage`      | highest — clicks do NOT come back through the Events API. They POST to a separate Interactivity Request URL, form-encoded with a JSON `payload`, needing 200 within 3 s: a second handler beside `POST /slack/events` (`slakd/server.go:41`), a proxyd route, and the Interactivity toggle in the app dashboard |
| **whapd** | Baileys 6.7.21               | **none**                                  | the sendable union (`Types/Message.d.ts:141`) has no `buttons` option, only `buttonReply` and `poll`; the proto still parses `buttonsMessage` inbound, so there is a receive path and no send path. Upstream removed it; Meta deprecates Web-MD buttons for the Cloud API. Reactions carry the verdict (above)  |

Three verdicts fit every one of these: 3 ≤ Discord's 5 buttons per row and
Slack's 25 per actions block, and a verdict plus an 8-char id is far under
Telegram's 64 B.

## Decision 7 — the reviewer needs admin on the folder

`resolveHoldInto` asks `CheckGrant` for the reviewer's `admin` authority on the
held call's folder. A standing answer also checks its hold mutation.
The write proves the returned policy version.
The agent socket exposes no `pending_actions` resource.

## A failed policy read blocks the call

`CheckHold` returns policy read errors. `holdGate` blocks the call and notifies
the chat. A failure to record a pending action also blocks the call.
`routd/hold_read_failure_test.go` checks ACL and membership read failures.

## Residual hole this does not close

Claude Code natively reads `.mcp.json` from the session cwd — the agent's own
home — and the SDK query passes neither `strictMcpConfig` nor an empty
`settingSources`, so a server the agent writes there is never seen by the
socket and no `hold:` rule can suspend its tools (BUGS `J14`). Every guarantee
above is bounded by that.

## Acceptance

Shipped 2026-08-24 (verified, tests in the shipping commit):

- `hold:mcp:<tool>(param=glob)` fires → `pending_actions` row lands `held` and
  the notice carries the `/approve <id>` text AND `/dash/approvals/?id=<id>`.
- The link opens the page with that row first; a resolved or unknown id is
  reported, not silently absent; `WEB_HOST` unset yields no broken URL.

Shipped 2026-08-29/30 — `always` (`auth/checkhold_test.go`,
`auth/params_test.go`, `routd/acl_params_test.go`, `routd/hold_always_test.go`,
`routd/hold_shaped_never_test.go`, `dashd/approvals_page_test.go`):

- A `hold:mcp:<tool>` row plus a same-action row with `effect='deny'` and a
  narrowing: the matching shape runs inline, every other shape is still held,
  a broad `hold:mcp:*` does not resurrect the exception, insert order does not
  matter within one layer, an inherited local skip beats a directly-held hold, and
  `TestCheckHold_OperatorStarDoesNotHoldEverything` stays green.
- `add_acl` writes a row carrying params and `remove_acl` drops that same row
  by naming the narrowing exactly; `auth.CheckGrant`'s containment check runs on the
  SHAPED row, so a caller who may grant `mcp:t(repo=mine/*)` cannot write
  `mcp:t`; a narrowed row at scope `**` is an ordinary row and never operator
  membership; a spec the evaluator would read differently from how it is stored
  is refused.
- `always` writes exactly the matcher the notice displayed — the same
  `withMatcher` output the notice, the REST row and the page all read — and
  refuses outright when no exact rule can be stated, leaving the call held
  rather than approved without its rule.
- The notice and the approvals page both state the two widenings in words and
  name the arguments no rule can constrain; an unnarrowed rule announces that
  every call to the tool will run.
- No surface offers a shaped deny while `authorizeCall` passes nil params,
  and the test pins the REASON — the same shaped deny is inert under nil params
  and decisive once arguments travel — so removing the restriction requires
  removing the reason.

Shipped 2026-08-30 — `arizuko talk` (`cmd/arizuko/talk.go`,
`talk_config.go`, tests in `talk_test.go` + `talk_config_test.go`):

- `arizuko group -i <inst> grant <sub> '<folder>/**'` +
  `arizuko token -i <inst> issue bearer <folder> --sub user:<sub>` →
  `arizuko talk` holds a conversation with the persona from a second machine.
  The binary imports no `store`, opens no sqlite file and takes no instance
  argument: it POSTs `/mcp` and reads `/me/chats/<folder>/<thread>/sse`, both
  through the PUBLIC proxyd URL. The grant is a folder subtree, not operator
  scope — `**` is never required.
- `--prompt` is ONE ITERATION of the same loop the REPL runs: one `promptSource`
  feeds one `turn`, and a test asserts the one-shot form sends exactly one
  message through that same path.
- The thread default is the fixed literal `talk`; `--thread` names another and the
  name reaches BOTH the send and the stream (a thread that reached only one
  would split the conversation in half).
- Config `~/.config/arizuko/talk.toml` is refused unless its mode is `0600`;
  precedence is flag > `@name` > top-level > `ARIZUKO_CHAT_*` > an error that
  prints the file to write and the two commands that fill it.

## Ties

`5/19` HITL (the gate) · `5/32` (the `acl` row the verdicts write) ·
`5/13`+`5/14`+`5/15` (the credential path) · `5/31` (capability links are token
kinds) · `5/35` (why not Hermes) · `5/A` decision 6 (the topology) · `6/27`
(the other GitHub) · BUGS `J14`.
