---
status: draft
---

# specs/7/61 — smaller, orthogonal components

A phase-7 task to remove integration code and give each remaining concern one owner.
The task covers ant, upstream login, and the core component boundaries.

## The owner asks for less machinery

Owner, verbatim:

> it would be mostly ideal if we could give arizuko integration in the environment. but i agree today its best if its ts.

> still you should try to make it smaller have this as a task for 7/ … where we want to also try to replace the oauth layer if we can and simplify everything we have into more orthogonal components

The owner later moved the language question out of this spec: "it makes not much
sense to have ant in typescript after all … have the rewrite for 8/ not 7/". A Go
ant that drives the `claude`, `codex` and `pi` CLIs is phase 8. This task tests
which responsibilities the environment or an upstream component can take over,
whatever language ant is written in.

All source measurements below use detached commit
`9be919983c1f2c60772631cc2eab5b4f67b6039e`, measured 2026-11-08.
Counts include blank lines and comments. Production counts exclude test filenames.
The draft authorizes no implementation, deployment, or live data change.

## Each cut keeps the product contract

Candidate ownership after the cuts:

```text
┌────────────────────────────┐
│ environment (changed)      │
│ trusted MCP and hooks      │
└──────────────┬─────────────┘
               │ configuration (changed)
┌──────────────▼─────────────┐
│ harness                    │
└──────────────┬─────────────┘
               │ events
┌──────────────▼─────────────┐
│ ant (changed)              │
│ turn and policy            │
└──────────────┬─────────────┘
               │ gated.sock
┌──────────────▼─────────────┐
│ routd (changed)            │
│ one address resolver       │
│ one placement renderer     │
└──────────────┬─────────────┘
               │ delivery
┌──────────────▼─────────────┐
│ channel adapter            │
└────────────────────────────┘

Dex ── upstream login ──► authd token issuer
native provider callbacks (removed only after owner approval)
```

The environment integrations are candidates, not claims about a supported standard.
Dex remains optional under [6/22-proxyd-cut](../6/22-proxyd-cut.md)
§"Step 4 — Dex as an optional upstream broker (measured 2026-09-05)".
Core process composition belongs to [7/26-single-world-deployment](26-single-world-deployment.md).
The cuts here must preserve ownership when components share a process.

```yaml
- path: specs/7/61-smaller-orthogonal-core.md (new)
  job: record the task, measured boundaries, candidate deletions, and owner decisions
  names: [ant, authd, routd]
  never: approve a runtime contract or prescribe unmeasured replacement code
- path: specs/7/index.md (changed)
  job: expose the draft task
  names: [status]
  never: claim implementation completion
```

## 1. Ant can lose translations when the environment supplies them

### Measured current state

`ant/src/` contains **3,759 production lines in 20 TypeScript files** and
**3,478 test lines in 22 files**. The three harness files total 1,262 lines:
`claude.ts` 628, `codex.ts` 343, and `pi.ts` 291.
These files also implement credentials, continuity, and event reduction.
Their full size is not a deletion estimate.

`ant/src/mcp-servers.ts:30` declares one MCP server. It starts `socat` against
`/run/ipc/gated.sock`. All arizuko tools pass through routd's grant, hold, and
audit checks. Third-party tools arrive through that same gate.
The harness translations are:

| Concern            | Current translation                                                                                                                                           | Candidate cut                                                                                                           |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| MCP configuration  | Claude SDK `mcpServers` and `strictMcpConfig`, `ant/src/claude.ts:559`; Codex `mcp_servers.*` argv, `ant/src/codex.ts:58`; pi `mcp.json`, `ant/src/pi.ts:199` | Remove these renderers if each pinned harness consumes one trusted environment declaration.                             |
| Tool policies      | One `bindings` table, `ant/src/policy.ts:67`; Claude SDK hooks plus native settings hooks; Codex `hooks.json` through `policy-cli.js`; pi `pi-extension.ts`   | Remove format and event translations when a shared hook contract preserves the required effects. Keep the policies.     |
| Status and results | Each harness reduces its events to `Frame`, `ant/src/backend.ts:44`; one status extractor, `ant/src/index.ts:157`                                             | Keep reduction until the environment also supplies equivalent lifecycle events. MCP discovery alone removes none of it. |
| Turn submission    | `ant/src/mcp.ts` sends `submit_status` and `submit_turn`; `ant/src/index.ts:410` handles submission nudges and continuity                                     | Keep completion acknowledgement, usage, failures, and the nudge continuation. Final prose is not message delivery.      |

`mcp-servers.ts` is 45 lines. `policy.ts` is 178 lines, `policy-cli.ts` 40,
and `pi-extension.ts` 156. These are whole-file counts, not additive savings.
Pi's extension also guards native writes and configures model providers.
Claude's native settings are `ant/.claude/settings.json`; policy rows become
SDK callbacks through `claudeHooks` at `ant/src/claude.ts:283`.
Codex renders and writes hooks at `ant/src/codex.ts:296` and `:315`.

### Candidate cut and first measurement

Use a standard environment declaration for MCP discovery and a shared hook
contract when the pinned harnesses demonstrate both. Do not invent a fourth
configuration format and keep three translators behind it.
If the environment owns the trusted socket declaration, `injectMcpEnv` can also
disappear. The socket, grant checks, holds, and audit records must stay.

First record each pinned harness's behavior with the same socket and policy fixtures.
Measure tool discovery, tool names, eager availability, and attempts to add an
agent-written MCP server. The environment must preserve the exclusive gated socket.
Do not copy secret environment values into pi's MCP configuration.

For hooks, measure pre-call denial, input rewrite, post-call context injection,
Stop continuation, edit/write coverage, and background task completion.
`CODEX_HONOURS` currently excludes rewrite at `ant/src/codex.ts:283`.
Pi excludes Stop rows at `ant/src/pi-extension.ts:85`.
A shared spelling does not prove shared semantics.

Record removable function ranges, replacement lines, startup time, hook subprocess
count, and the same successful and refused calls through each installed harness.
Keep `policy.ts` as the declaration. Keep credentials, skills, prompt delivery,
steering, cancellation, deadlines, continuity, event reduction, and `submit_turn`
until a separately measured replacement supplies each responsibility.

## 2. An upstream broker can replace only the provider dance

### Measured current state

The decision in 6/22 remains: Dex is an optional upstream broker.
Its historical classification measured about 345 replaceable lines, not the whole auth layer.
No new broker runtime experiment accompanies this draft.

Today's physical Go line counts are:

| Scope                     | Non-test files / lines | Test files / lines |
| ------------------------- | ---------------------- | ------------------ |
| `auth/`, direct children  | 14 / 2,331             | 24 / 4,144         |
| `auth/`, recursive        | 17 / 2,824             | 25 / 4,536         |
| `authd/`, direct children | 11 / 1,947             | 20 / 4,604         |
| `authd/`, recursive       | 16 / 2,962             | 21 / 5,136         |

The recursive `auth/` count includes `surrogate/` and the 33-line `authtest/`
support file. Surrogate credential exchange is a separate concern from user login.
The recursive `authd/` count includes its owned `authstore/`.

The six files measured by 6/22 total **1,388 lines today**:
`auth/oauth.go` 416, `authd/oauth.go` 429, `auth/es256.go` 224,
`auth/jwks.go` 196, `authd/sessions_resource.go` 79, and
`authd/signing_keys_resource.go` 44. `authd/dex.go` remains **126 lines**.
The four evaluator files total **1,072 lines**: `auth/authorize.go` 684,
`auth/acl.go` 107, `auth/delegate.go` 199, and `auth/scope.go` 82.

A narrower, reproducible candidate count covers ten native function bodies:

| Function locations                                         | Functions                                                       | Lines |
| ---------------------------------------------------------- | --------------------------------------------------------------- | ----- |
| `auth/oauth.go:69-122`, `:137-150`, `:232-356`             | Six `Exchange*` / `Fetch*` functions and `checkGitHubOrgMember` | 189   |
| `authd/oauth.go:202-270`, excluding separating blank lines | `googleCallback`, `githubCallback`, `discordCallback`           | 67    |
| Total                                                      | Ten function bodies                                             | 256   |

The 256-line count differs in scope from 6/22's historical 345-line classification.
It includes callback prologues, dispatch calls, and admission checks that a replacement
must retain or supply. It excludes imports, route registration, buttons, and authorize URLs.
Neither number is a net deletion promise.

### Candidate cut and cost

If new evidence justifies a mandatory broker, delete the native exchange/userinfo
functions, callback wrappers, and provider-specific login wiring.
Use the existing Dex callback and the existing `dispatch` path.
The 126-line Dex client already exists, so do not subtract it as newly added code.
Measure the actual patch after preserving the shared logic and adding required admission checks.

Keep signed CSRF state, PKCE cookies, safe returns, identity linking, scope snapshots,
and session issuance. Keep the ES256 mint and rotation, JWKS, refresh families,
downscope, service-token exchange, and the grant evaluator.
Relevant boundaries are `authd/oauth.go:279`, `authd/oauth.go:305`,
`authd/server.go:185`, `authd/server.go:209`, and `authd/http.go:164`.
`auth.PostForm` also serves Dex and surrogate credentials. Deleting native login
does not delete that shared transport or `auth/surrogate/`.

The cost is a required broker process, its supported persistent storage, connector
configuration, secrets, callback registration, upgrades, and an additional login dependency.
6/22's measured connector limits remain evidence for its decision, not fresh vendor claims.
Before reconsideration, pin and test the proposed Dex version and storage topology.
Measure idle memory, login latency, broker outage behavior, and the net repository deletion.

Prove Google verified-email/glob admission, GitHub organization admission and login-based
principals, Discord snowflake principals, account linking, and the same arizuko claims.
The native checks are `authd/oauth.go:219` and `:245`.
The current Dex claims reader at `authd/dex.go:107` does not read email verification.
Do not remove an admission rule merely to make the deletion larger.
Without new evidence and an owner decision, retain the optional broker.

## 3. Core components need one owner per concern

### Measured current state

[ARCHITECTURE.md](../../ARCHITECTURE.md) is the component map.
The following inventory names source components, not a live instance's enabled services.
Go entrypoints come from `rg -l '^func main\(' --glob '*.go'`.

| Daemon or service         | Responsibility                                                                            |
| ------------------------- | ----------------------------------------------------------------------------------------- |
| `routd`                   | Message append, routes, prompt assembly, dispatch, outbound lifecycle, per-turn MCP host. |
| `runed`                   | Turn admission, isolated guest lifecycle, mounts, egress process, execution records.      |
| `authd`                   | Arizuko signing authority, OAuth login, sessions, signing keys.                           |
| `proxyd`                  | HTTP entry, proxy routes, identity headers, folder web and WebDAV access checks.          |
| `webd`                    | Web chat, route-token chat/webhooks, SSE.                                                 |
| `dashd`                   | Operator cockpit and management pages.                                                    |
| `onbod`                   | Onboarding gates, invitations, redemption.                                                |
| `timed`                   | Poll due tasks, enqueue task messages, request rescheduling through routd.                |
| `teled`, `discd`, `slakd` | Telegram, Discord, Slack adapters.                                                        |
| `emaid`, `whapd`          | Email and WhatsApp adapters; `whapd` uses TypeScript.                                     |
| `gitd`, `googd`           | GitHub events/replies and Google document comment threads.                                |
| `ttsd`, `sidecar`         | Speech synthesis and the whisper-cpp transcription image.                                 |
| `davd`, `vited`           | External WebDAV binary and Vite static origin; neither has a Go daemon entrypoint.        |

| Library or supporting component                                   | Responsibility                                                                                       |
| ----------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `types`, `core`                                                   | Shared IDs, configuration, row and message types.                                                    |
| `objstore`                                                        | `Query` and `GuardedBatch` storage port and Dynamo-compatible implementation.                        |
| `store`, `authd/authstore`, `runed/runstore`, `chanlib/chanstore` | Owning entities and persistence operations.                                                          |
| `audit`, `obs`                                                    | Durable audit records and optional telemetry.                                                        |
| `auth`                                                            | Offline verification, middleware, grant evaluation, delegation, shared OAuth primitives.             |
| `resreg`                                                          | One management handler exposed through REST and MCP, with injected authorization.                    |
| `chanlib`, `chanreg`                                              | Adapter protocol/reliability and registered-channel delivery.                                        |
| `router`, `queue`, `proactive`                                    | Pure route/prompt functions, folder scheduling, silence-mode configuration.                          |
| `ipc`                                                             | In-process agent MCP tools and calls into routd.                                                     |
| `container`, `diary`                                              | Group/package asset setup and memory summary reads.                                                  |
| `groupfolder`, `mountsec`                                         | Folder containment and mount path checks.                                                            |
| `egress`, `substrate`, `mcpfw`                                    | Standalone egress proxy, substrate capability declaration, MCP filter. Arizuko does not run `mcpfw`. |
| `proxycfg`, `compose`, `theme`                                    | Caddy configuration reader, deployment generation, shared page CSS.                                  |
| `kernel`, `ant`, `anteval`, `cmd/arizuko`                         | Guest kernel/init, harness and skills, capability acceptance runner, operator CLI.                   |

### Candidate cuts, in value order

1. **Resolve a conversation address once.** `discd/bot.go:299` maps a thread to
   parent JID and topic. `routd/turns.go:57` reconstructs the thread JID for
   delivery accounting. `ipc/ipc.go:775` records outbound placement.
   `routd/mcp.go:514` and `routd/reads.go:50` decide read reach from JIDs.
   L716 measured false delivery nudges on five of six turns despite visible replies.
   Candidate: one core resolver consumes adapter-supplied parent/topic facts.
   Reads, stored delivery, and completion accounting consume that same address.
   Do not guess a parent from a thread snowflake or add a second routing table.
   Proof: replay parent-plus-topic and thread-JID inputs against the same stored
   conversation and obtain the same rows, delivery result, and authorization target.
   An unrelated chat and folder must remain refused.
   L761 also reports three refused history reads. Its own-chat exception failure
   remains unmeasured in the entry. Trace that failure before changing authorization.
   Normalization and route authorization require separate changes and evidence.

2. **Render delivery placement once.** `routd/placement.go:21` supplies turn
   placement. Cross-chat `replyToId` placement also lives in `routd/mcp.go:289`.
   Text, documents, and voice enter the helper with different flags at
   `routd/mcp.go:284`, `:322`, and `:341`.
   L719 measured a file outside the thread opened for its turn.
   L720 measured five recovery replies in the channel root.
   Candidate: one placement function resolves explicit destination, named reply
   message, topic, and chat preference for every answering medium and progress notice.
   Transport-specific senders consume its result.
   Keep the explicit `replyToId` rule. The owner rejected a stored handoff origin
   because one recovery request can answer several conversations.
   Proof: identical placement fixtures for text, file, voice, progress, and retry;
   preserve an explicit foreign destination and root placement for ordinary sends.
   The patch must remove the second rule implementation, not just wrap both.

3. **Separate proxy transport from arizuko access checks.** `proxyd/main.go:99`
   and `:275` build reverse proxies. `proxycfg/caddy.go` renders the same stored
   routes for Caddy. `/auth/forward` already exists at `proxyd/main.go:330`.
   Current totals: proxyd has 1,960 production lines and 4,438 test lines;
   proxycfg has 121 production lines and 167 test lines.
   These totals include access checks that must stay.
   Candidate: exercise the 6/22 boundary with a fronting proxy, then delete only
   the transport implementation whose behavior that deployment proves equivalent.
   Preserve folder scoping, WebDAV write enforcement, transit proof, redirects,
   route order, optional authentication, and uncached authorization.
   Proof: run the same HTTP cases through each composition and compare responses,
   identity headers, access refusals, websocket behavior, latency, and idle memory.
   Record exact deleted functions and required configuration before any cutover.

Sources for the incident measurements: `.diary/20261108.md` entries L716, L719,
and L720; `BUGS.md` entry L761. These records describe distinct failures.
The draft does not claim one refactor fixes all four.

Keep authn, authorization, resource handling, storage, and execution as separate
concerns. Keep one resource handler for REST and MCP.
Do not widen `objstore` or `runed.Runtime` to make consolidation convenient.
Keep `egress`, `substrate`, and `mcpfw` free of arizuko-internal imports.
Process count alone does not measure orthogonality.

## Open questions

1. **Should environment integration require upstream support before ant changes?**
   Recommendation: yes. The three MCP renderers and hook translations above are
   today's concrete targets. A local standard without harness adoption adds machinery.
   Measure installed harness behavior first; the language rewrite is phase 8.
2. **Should Dex become required for a small callback deletion?**
   Recommendation: no on present evidence. The candidate function bodies total
   256 lines, and the native callbacks enforce admission rules absent from the
   current Dex claims reader. Reconsider only after the parity and operating-cost
   measurements above support a deletion. The 6/22 optional decision stands meanwhile.
3. **Should conversation normalization be the first core cut?**
   Recommendation: yes, followed by placement as a separate change.
   L716 and the source sites show repeated address knowledge.
   Require a measured explanation of L761 before approving any read-policy change.
4. **Should a fronting-proxy trial precede the single-process composition?**
   Recommendation: keep it separate from 7/26. Existing `proxycfg` and
   `/auth/forward` make the transport boundary independently testable.
   The owner must approve an actual proxy cutover after the comparison.

## Contract

Reproduce whole-file counts from the recorded commit:

```bash
rg --files ant/src -g '*.ts' -g '!*.test.ts' | xargs wc -l
rg --files ant/src -g '*.test.ts' | xargs wc -l
rg --files auth -g '*.go' -g '!*_test.go' | xargs wc -l
rg --files authd -g '*.go' -g '!*_test.go' | xargs wc -l
wc -l auth/oauth.go authd/oauth.go auth/es256.go auth/jwks.go \
  authd/sessions_resource.go authd/signing_keys_resource.go authd/dex.go
```

The native-body count sums each function from its `func` line through its closing
brace, excluding blank separators between functions. It uses the ranges above.
Before implementation, remeasure changed sources and record the actual deletion
against each candidate's retained behavior and replacement cost.
Mutation checks must break each claimed equivalence when the shared resolver,
placement rule, gate, or required hook effect is bypassed.

For this draft, `go test ./tests/specrefs/ -count=1` must exit zero.
Prettier and pre-commit must pass on the spec and phase index.
Only those two files belong to the draft commit.
