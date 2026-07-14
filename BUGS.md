# BUGS.md — open issues queue

> **Standing policy — every fix here adheres to WISDOM** (`~/.claude/CLAUDE.md`):
> minimality (smallest change at the root cause), orthogonality (one concern per
> fix, no parallel second path — amend the original), fail loud to the user on
> user-facing paths, retry only transient errors, fix causes not symptoms.
> Redesigns (new contract, changed cross-daemon control flow, auth-model or
> schema changes) stay recorded as proposals and ship only after user sign-off.

## S2 — Missing SECRETS_KEY permits plaintext secret writes despite the required-key contract (2026-07-14, open)

The configuration contract says `SECRETS_KEY` is required, with no plaintext
mode or `AUTH_SECRET` fallback, but routd only warns when the keyring is empty.
`store.Store.storeValue` then returns the plaintext unchanged, so REST/CLI
secret writes can persist readable values while the service remains healthy.
This also makes imports unsafe to proceed before validating the target key.

- **Severity:** high
- **Scope:** secrets encryption precondition / routd startup
- **Affected:** instances started without `SECRETS_KEY`
- **Source:** core/config.go:136-146 (required-key contract); routd/cmd/routd/main.go:61-69 (warn-and-continue); store/secrets.go:72-78 (plaintext fallback)
- **Status:** open
- **Fix:**

## Web docs claim URL↔kind binding on route tokens; shipped code accepts any token at either URL (2026-07-12, fixed, LOW)

**Location**: `template/web/pub/arizuko/reference/tokens.html` §"/chat/ and /hook/ — each URL bound to its JID prefix kind" (+ echoes in `concepts/tokens.html`, `reference/schema.html`, both howto ledes)

The docs say "a hook: token at /chat/… returns 404; a web: token at /hook/…
returns 404 — binding enforced at token lookup". The shipped contract is the
opposite: spec 5/W §"URL routing" locked "both URL prefixes accept ANY valid
route_token; kind is metadata, not a URL access gate", and
`webd/route_token.go` implements exactly that (lookup does not filter on JID
prefix). Also spec 5/W §"Where this runs" still describes the old filtered
lookup — internal spec inconsistency. Fix: align the four web pages + that
spec section to the any-token contract (docs-only).

- **Status:** fixed 2026-07-13 — all four pages + spec 5/W (lede, "Where this
  runs", Tests) state the any-token contract. Same pass trued
  `reference/tokens.html`'s endpoint table to the shipped handlers (no GET
  `/hook`, no hook SSE row; POST `/hook` → 204) and removed its residual
  headers-delivery claim (same drift the fixed howto/webhooks entry removed —
  ingest forwards body only). `legacy/` snapshot pages left as-is.

## System-turn / broadcast delivery targets a folder-jid or prefix-less room → "no channel for jid" → the broadcast is silently lost (2026-07-13, OPEN)

From the "chats stop responding" eval. System/auto turns (the auto-migrate
release broadcast; some proactive turns) call `deliver.Send(jid, …)` with `jid`
set to the **group folder** (`main`, `atlas/support`) or a **prefix-less/stale
room** (`user/137865889`) instead of the group's actual routed **channel** jid.
`chanDeliverer.resolve` (`routd/deliver.go:78`) maps a jid → adapter via
`lookupSource` (the adapter that delivered the jid's newest inbound); a folder or
a prefix-less room has no inbound source → nil channel → `no channel for jid …`
and the message is dropped. Live: marinade `user/137865889` during
`auto-migrate-atlas/martin` (release note never delivered); sloth `jid:main` on
restart. **Impact:** the migration/announcement broadcast silently fails to reach
users on affected groups; error-spam in logs (51 hits/2d). NOT a user chat dying
(distinct from the stale-session fix). **Fix direction (record-only):** system-turn
delivery should resolve the group's bound channel jid (the routes-table channel /
newest routed inbound), not the folder; or skip+debug-log when a folder has no
bound channel instead of ERROR-spamming a doomed `Send`. Needs the broadcast/send
target-resolution audited — design, not a one-liner.

- **Severity:** medium (announcements silently lost on some groups; log noise)
- **Status:** OPEN, record-only.

## rate_limit is the top failure signal (195/2d) — verify it's surfaced to the user, not swallowed (2026-07-13, OPEN, needs verification)

`rate_limit` was the single largest signal in the stop-responding eval (195 hits
in 2 days across instances). The stale-session-adjacent BUGS note records the
session/weekly-limit case as *"SDK threw after result (ignored)"* — i.e.
swallowed. IF the model-API rate-limit/throttle error is swallowed rather than
delivered, the user's chat **silently stops** with no explanation during
throttling (unavoidable externally, but the silence is fixable). **To verify:**
trace `ant/src/index.ts` result handling for the rate-limit / "hit your … limit"
subtype — is it delivered as a "rate limited, back shortly" chat message or
dropped? If dropped, surface a terse user-facing notice (one delivery, not a
retry loop). Distinct from the stale-session fix (that class is now closed).

- **Severity:** medium (silent stop during throttling; most-frequent signal)
- **Status:** OPEN — verify surfaced-vs-swallowed, then a small surface-the-notice fix.

## Resource identity (`Name`/`Table`) restated across the two `resreg.Resource` sites — residual drift the 5/16 single-source model retires (2026-07-13, sweep, record-only)

Swept the code for duplications the `5/16` one-owner + single-source model renders
useless. **Mostly already fixed:** Endpoints / RowType / MCP metadata ARE single-
sourced — every mounted handler imports `resreg/resources/<name>.go`'s
`XEndpoints`/`XMCPArgs`/`XMCPDoc` (audited all 11: **0 inline re-declarations**,
comments say "single source"). So the older `openapi_two_declarations` two-full-
declarations drift is largely closed.

**Residual duplication (retire under 5/16):** each cold-tier resource is
instantiated as `resreg.Resource{}` **twice** — the registry entry
(`resreg/resources/<name>.go` `resreg.Register(...)`, for OpenAPI + the standalone
`arizuko apply`/`export` CLI, no handler) and the mounted handler
(`<owner>/<name>_resource.go`, which adds Store/Handler/Gate). Both **restate
`Name`** (the wire identity, per CLAUDE.md "Name IS wire identity") and some
`Table`. Two sources for one identity string = a drift vector — exactly the class
that let proxyd's live route resource drift to `Name:"routes"` while its catalog
said `proxyd_routes` (fixed 2026-07-01). ~11 resources, Name-restated at:
- routd: `acl_resource.go:54`, `groups_resource.go:58`, `network_rules_resource.go:67`,
  `routes_resource.go:100`, `route_tokens_resource.go:78`, `scheduled_tasks_resource.go:89`,
  `secrets_resource.go:56`, `web_routes_resource.go:50`
- onbod: `gates_resource.go:49` (onboarding_gates), `invites_resource.go:40`
- proxyd: `resource.go` (proxyd_routes)
  (each with a registry twin in `resreg/resources/*.go`)

**Fix (per 5/16 "one owner, single-source declaration"):** the mounted handler
derives `Name`/`Table` from the single registry declaration (import the identity),
never restates it — one source of `{Name, Table, PKFields, RowType, Endpoints}`,
handler adds only `{Store, Handler, Gate/containFn}`. Cheap interim guard: a test
asserting `mounted.Name == registry.Name` per resource catches drift until then.

**Related duplications of the same concept, already logged** (distinct sites, retire
together with the 5/8+5/16 finalization): duplicate `cost_log` tables (routd vs
store — the no-duplication-rule violation, entry below); dashd direct-DB reads that
bypass the owner (`dashd reads SQLite directly`); `arizuko apply` opening the frozen
`messages.db` instead of owner DBs (5/8). All three "bypass or duplicate the
one-owner model."

- **Severity:** low (residual drift vector; the heavy duplications are already fixed/logged)
- **Status:** OPEN, record-only — retire under 5/16 one-owner + single-source.

## Cost attribution is folder-only — per-chat lost, per-user auth-only, plus a duplicate cost_log (2026-07-13, OPEN)

Question raised: is LLM cost attributed completely to individual chats + users? **No.**
- **Cost → folder: yes.** `recordTurnResult` writes `cost_log(folder, turn_id, model, …)` once per model (`routd/turns.go:684-689`; schema `routd/migrations/0001` + `0011-cost-log-user-sub.sql`).
- **Cost → chat: NO (derivable, unwired).** `cost_log` has no `chat_jid`. The turn's `chat_jid` is on `turn_context` (same `turn_id` PK) but nothing joins it — not `CostByTurn` (`routd/db.go:1104`), `/v1/cost` (`routd/reads_http.go:299`), or dashd. A folder with many chats collapses to one folder total. **Min fix:** add `chat_jid` to `cost_log`, set from `tc.ChatJID` at the one write-site (`turns.go:685-689`).
- **Cost → user: PARTIAL.** `userSub := callerSubOfMsg(tc.Trigger)` resolves only `google:`/`github:`/`local:` senders (`routd/budget.go:9-23`); native adapter senders (`telegram:user/…`, WhatsApp, Slack) → `""` → folder-only. Most chat traffic has no user dimension. **Decision needed:** widen `callerSubOfMsg` to key on the raw adapter sender, OR document per-user cost as auth-only by design (currently reads as a bug, not a decision).
- **Duplicate cost_log (no-duplication violation).** A second, structurally different `cost_log` exists in `store/` (`store/cost_log.go` + `store/migrations/0049-cost-log.sql`, cols `ts/cache_read/cache_write`, no `turn_id`) with its own `LogCost`/`GroupUsageBulk` (dashd usage reads this one). Two cost-recording paths that will drift — consolidate to one owner.
- **Reporting:** dashd usage + `GroupUsageBulk` slice by folder only (`dashd/usage_page.go`, `store/cost_log.go:119`); `/v1/cost` is per-turn only. No per-chat or per-user rollup surface exists.

- **Severity:** medium (billing/showback can't split by chat or by unauthenticated user)
- **Status:** OPEN — chat gap is a 1-column + 1-write-site fix; user gap needs a design call; the duplicate cost_log needs sign-off (schema consolidation).

## Codex mount assumes creds readable by the container uid — 0600 operator creds break codex-in-container (2026-07-12, unblocked live, DURABLE FIX OPEN)

`container/runner.go:582-590` RO-mounts `HOST_CODEX_DIR/{auth.json,config.toml}`
into the agent container, which runs as uid 1000 (`mivu`). But those files are
the operator's personal codex creds under `/home/onvos/.codex`, owned
`onvos:1001` mode `0600` → uid 1000 gets `Permission denied` on `config.toml`,
so the in-container `/oracle`/codex path dies (looks like "not logged in", is
actually a uid mismatch). Live on krons 2026-07-12.

- **Unblocked live (fragile):** `chmod o+r /home/onvos/.codex/{auth.json,config.toml}`
  (setfacl not installed → couldn't scope to just uid 1000). uid 1000 now reads
  them; next fresh container's codex works, no restart. **Two caveats:** (1)
  world-readable now exposes the operator's ChatGPT OAuth token to any local
  user — acceptable on a single-operator box, tighten with an ACL (`apt install
acl`; `setfacl -m u:1000:r …`) if the host gains other users; (2) if onvos's
  host codex REWRITES these files (refresh/config change via atomic rename), the
  `o+r` is lost and it breaks again — re-apply, or ship the durable fix.
- **Durable fix (code, needs sign-off):** don't RO-mount the 0600 originals;
  at spawn, COPY `auth.json`/`config.toml` into the per-group writable `.codex`
  dir (`runner.go:583`, already chowned to the run uid) so the container reads
  its own uid-1000 copy. Keeps operator creds untouched, survives host-side
  rewrites, no world-read. Redesign of the codex mount → record + sign-off.

## Onboarding recurs as a manual operator chore — new chat → route-miss → silence → hand-run `arizuko group add` (2026-07-12, OPEN, HIGH)

This keeps popping up. Live datapoint: Nikol (`telegram:user/8177590051`) sent
`/start` + a question at 08:01 2026-07-12 on krons, hit a route-miss, got NO
greeting and NO admission-queue row; the operator had to discover the JID out of
band (sqlite) and hand-run `arizuko group krons add … rhias/niki`. Same story as
adshaus and the earlier telegram groups. Two distinct defects compound it:

1. **`InsertOnboarding` never fires on a genuine route-miss** — **fixed
   2026-07-12**: root cause pinned — TWO drifted route-miss handlers; the
   queue-worker copy that ingest actually drives (`processGroupMessages`)
   had no onboarding and advanced the cursor first, so `pollOnce`'s
   onboarding branch was dead. Full walkthrough in the "Chat-initiated
   onboarding still dead" entry below. One shared `routeMiss` handler now
   serves both paths; an `InsertOnboarding` failure is `slog.Error` + a
   notice delivered to the chat.
2. **No self-serve JID discovery — `/chatid` dead exactly where it's needed** —
   **fixed 2026-07-12.** Correction to the original claim: `/chatid` DOES
   exist (`routd/steer.go handleCommand`, tested by `TestCmdChatID`) — but the
   steer layer only runs on chats that RESOLVE, so on an unrouted chat (the
   one audience that needs it) the command fell into the route-miss drop.
   Live: 3 dead `/chatid` attempts on unrouted `telegram:group/5567410596`,
   2026-07-09. Fix: `routeMiss` intercepts `/chatid` (same `lookupCommand`
   parser, same `ack` path — not a second dispatcher) and replies with the
   chat JID; the miss still queues onboarding. Test: `TestRouteMissChatID`.

**Test gap (operator ask 2026-07-12):** **closed 2026-07-12** —
`tests/onboarding_e2e_test.go TestOnboardingEndToEnd` drives the WHOLE path on
the bootFederation harness (real authd tokens, real ingest→queue→dispatch,
real runed over HTTP): unrouted inbound → route miss → onboarding row
`awaiting_message` in the onbod-owned store via routd's production OnbodClient
→ operator admits (approve + the `arizuko group add` group/route shape) → same
chat's next message routes → agent reply row lands. Verified failing on the
pre-fix queue path (WaitForRow timeout — no onboarding row), green after.

- **Severity:** high (every new user needs manual operator intervention today)
- **Scope:** routd/loop.go onboarding branch; gateway `/chatid`; e2e/anteval
- **Status:** fixed 9fcb0bce (onboarding) + 5f102e0d (/chatid) + 03b6949c
  (e2e) (2026-07-12). NOT yet deployed — ships with the next krons image
  build + restart.

## Steer layer (slash commands / sticky nav / @child) races the queue path — routed-chat slash commands dispatch agent turns (2026-07-12, OPEN, HIGH)

Found while root-causing the onboarding miss (same structural drift, distinct
concern — recorded, NOT fixed). The steer layer (`routd/steer.go` — `/ping`,
`/new`, `/stop`, `/root`, `/invite`, `/gate`, sticky `@group`/`#topic`, @child
delegation) runs ONLY in `pollOnce` (`loop.go l.steer`), but ingest enqueues
straight into the queue worker `processGroupMessages`, which dispatches a turn
WITHOUT consulting steer. So for a routed chat both race: the queue path
usually starts an agent turn on the raw command message within ms, and the
2s-tick `pollOnce` may ALSO steer it while that run is in flight (cursor not
yet advanced) → double-processing (steer ack + an agent reply to the command
text).

**Live evidence (krons):** `/root check rhias direct groups…`
(`telegram:user/1112184352/5771`, 2026-07-12 08:08:31) has a `turn_context`
row on the RAW message (state=done) and NO `root-…` reinjection row — i.e. the
queue path ran it as a plain non-elevated agent turn and the agent replied
ABOUT `/root` instead of `cmdRoot` elevating it. Operator `/root` (and every
routd-serviceable command) is therefore unreliable on the live split: whether
the fixed command response, an agent turn, or both happen is a race.

**Suggested direction (needs design sign-off — control-flow change):** the
same consolidation shape as `routeMiss`: steer must run on the path that
processes the batch (`processGroupMessages`, before `runTurn`), not in a
racing backstop. Care: steer consumes only the LATEST message (`last`) while
the queue path batches; and pollOnce's steer call must not remain as a second
racing site.

- **Severity:** high (operator /root silently not elevating; command double-
  processing burns turns)
- **Scope:** routd/loop.go pollOnce steer site; routd/dispatch.go
  processGroupMessages; routd/steer.go
- **Status:** OPEN — recorded per triage protocol; fix is a dispatch-ordering
  redesign, awaits sign-off.

## Admin cannot log in to the krons dashboard (2026-07-12, OPEN, HIGH — needs root-cause)

Reported: an admin cannot get into krons (`/dash`/`/auth`). Facts gathered:
`auth_users` in krons `auth.db` has **0 rows**; krons `.env` and
`docker-compose.yml` carry **no OAuth provider config** (no `GOOGLE_CLIENT_ID`/
`_SECRET`, no `*_ALLOWED_EMAILS`) — only `AUTH_SECRET` + `AUTH_BASE_URL`. So
either the login flow has no identity provider to authenticate against, or a
successful login isn't being recognized as operator (operator status is a grant
check — `proxyd/main.go:678-681`, and durable once the acl table has a row per
`main.go:236`). No `/auth` deny lines in journalctl (the 401 spam is the known
whapd re-pair loop, unrelated). **Suggested fix (root-cause first, do NOT hack):**
trace the `/auth/login` → callback → `requireAuth` path on krons: (a) is a
provider configured at all — if not, that's an operator config gap (document the
exact `.env` keys), not a code change; (b) if login succeeds but `/dash` 403s,
the first operator needs a bootstrap grant (`**` acl row) — confirm how the
first operator is meant to be seeded and whether that path is broken. Fail loud
on the real cause; if it's config, surface the missing keys rather than
inventing a fallback.

- **Severity:** high (operator locked out of their own instance)
- **Scope:** proxyd/main.go requireAuth + /auth flow; authd; krons `.env`
- **Status:** config-not-code (root-caused 2026-07-12). **Every server-side hop
  verified working on live krons; no code change.** Corrections to the facts
  above: krons `.env` DOES carry an OAuth provider — `GITHUB_CLIENT_ID` +
  `GITHUB_CLIENT_SECRET` (the original check greped only `GOOGLE_*`), and
  compose delivers them (`env/authd.env`). Verified live: `/auth/login` 200
  renders the GitHub button; `/auth/github` 307s to GitHub authorize with a
  valid client_id + PKCE state; GitHub shows the normal sign-in (no
  redirect_uri-mismatch banner → the app's callback URL is registered
  correctly); `/auth/github/callback` is mounted (403 `invalid state` on a
  bogus probe); proxyd has `AUTHD_URL` so the cookie→`/v1/refresh`→JWKS chain
  (`tryRefreshViaAuthd`) is wired; and the first-operator grant is ALREADY
  seeded: routd.db `acl_membership: github:kronael → role:operator` +
  `acl: role:operator | * | ** | allow` (migration-0053). What's actually true:
  **no login has EVER completed** — `auth_users` = 0 rows, `refresh_tokens` =
  0 rows, zero `/auth/github/callback` hits in 11 days of journal. The flow
  dies at the human step, not in the platform. **Exact operator actions:**
  (1) open `https://krons.fiu.wtf/dash/` → redirected to `/auth/login` →
  "Continue with GitHub" → sign in to GitHub as **kronael** (the seeded
  operator sub) → operator scope resolves and `/dash` opens. (2) If the
  admin's GitHub account is NOT `kronael`: log in once (creates the
  `auth_users` row), then seed the grant:
  `arizuko grant krons github:<login> '**'` (→ `role:operator` membership,
  cmd/arizuko/main.go:532). (3) If Google login is wanted (as on marinade):
  add `GOOGLE_CLIENT_ID` + `GOOGLE_CLIENT_SECRET` (and optionally
  `GOOGLE_ALLOWED_EMAILS`) to `/srv/data/arizuko_krons/.env`, then
  `sudo systemctl restart arizuko_krons`.

## GB1 — Redesign: eliminate the "green but broken" class at the cause (2026-07-11, mostly ✅ SHIPPED)

Cause fix for the silent-failure class. fable-reviewed + code-verified; signed
off and shipped L3→L4→L2→L1, each its own commit + test (all `go test ./routd/`
green). NOT deployed yet (ships next image build).

- **Severity:** medium (rare triggers, silent + user-facing when they fire)
- **Scope:** routd dispatch + delivery, container spawn, route write-path, lint
- **Status:** shipped (code); remaining follow-ups below

**Shipped:** L3 silent-turn detect+notice+metric (`d65e73c2`); L4a ghost-group
dispatch guard (`c9979e78`); L4b DeleteGroup route/engagement cascade (`836d3f88`
+ fix `a4c4a68d`); L2 send_file→422+no-row (`d347a0dc`); L1 golangci errcheck+nilerr
config + `lint-strict` (`3aa59fd3`).

**Remaining (open):**
1. **L4b route-write validation** — `routesHandler` add/set + CLI `route add` should
   `GroupExists(target)` before persist. Deferred: needs a shared target parser
   (observe/web/hook suffixes) + confirm no bootstrap adds route-before-group.
   L4a backstops it at dispatch meanwhile.
2. **L1 gate-flip** — wire `lint-strict` into `make lint`/CI once golangci-lint
   builds against the toolchain (x/tools mismatch here), then triage findings.
3. **ant `deliverTurn`** — submit_turn RPC failure should retry once then exit
   non-zero (`ant/src/index.ts:105`) → `OutcomeError` → existing notice path.
4. **Finish `01a61a07`** — home-not-writable should return an error from the runner
   (→ `OutcomeError` → user notice), not log-and-spawn-config-less.

Original plan detail (for the record):

Ship order **L3 → L4 → L2 → L1** (biggest cause-net first). Each = own commit + test.

**L3 — silent-turn reconciliation (KEEP, ~8 lines, highest value).** The clean
epilogue `dispatch.go:314-321` ALREADY computes `TurnResultRecorded(folder,turnID)`
(line 316) and runs synchronously at container exit (no async observer/ledger
needed). Add: `Outcome==OK && !TurnResultRecorded && !TurnHasBotReply` → `slog.Error`
+ deliver a chat notice via the existing `l.deliver.Send` (as `dispatch.go:297-303`)
+ a `silent_turns_total` counter. NO retry (a 0-result run isn't transient). Verified
no false positives: deliberate silence records turn_results (`ant/src/index.ts:328`
calls deliverTurn on every result event incl. think-only); steered turns exit before
the epilogue. Cause-agnostic — catches the three residues L2/L4 miss: 0-result query
(`index.ts:459`), swallowed submit_turn RPC (`index.ts:105`), and the config-less
home fix that still spawns (`01a61a07`).

**L4 — ghost-group guard, TWO commits.** (a) Runtime backstop: guard the HEAD of
`runTurn` (`dispatch.go:102`), mirroring `budgetGate` (`:132`) — `!GroupExists(folder)`
→ ERROR + `deliver.Send` notice + return `true` (consume batch, cursor advances, no
poison replay). One choke covers route/engagement/sticky ghosts. **NOT** in `resolve()`
— there a failed guard becomes a route-*miss* → silently drops (`loop.go:540`) AND fires
spurious `InsertOnboarding` (`:525`). (b) Write-time integrity (the real cause):
`routesHandler` add/set (`routes_resource.go` ~149/171) + CLI `route add`
(`cmd/arizuko/route.go:33`) must `GroupExists(target)` before persist; add a route/
engagement cascade to `DeleteGroup` (`db.go:254`, currently a bare DELETE). Legit flows
traced clean (@child, sticky, web:/hook:, observe, onboarding all pre-register or
pre-guard).

**L2 — `handleDocument` only (~5 lines), NOT a funnel.** The agent surface is already
loud: MCP `send_file`→`mcpAppendDoc`→`toolErr` (`mcp.go:265`,`ipc/ipc.go:1112`), social
REST returns 422 via `relay()` (`turns.go:480`). Only `handleDocument` (`turns.go:336`)
drifted: swallow→HTTP 200 + `pending` row. Fix: return 422 on `Document()` error AND
don't persist the pending row — else the text-only `maybeRetryOutbound` sweep
(`loop.go:373`, sends `m.Content` only) re-sends the caption WITHOUT the file (latent
bug). DROP the `recordDelivery` abstraction and DROP making `appendAndDeliver` non-2xx
(fights `maybeRetryOutbound`, causes double-sends).

**L1 — golangci-lint `errcheck` + `nilerr` only.** Into `make lint` + pre-commit (lint
is bare `go vet`, `Makefile:35`). DROP `errorlint` (unrelated to this class) and DROP
the ast-grep `if err==nil{}` rule (false-positive generator — the with-else form is
legit and used correctly, `turns.go:297`, `dispatch.go:278`). Triage ~15 sweep sites;
benign discards → `//nolint … // benign:`.

**Plus (fable-added cause fixes, cheap):** ant `deliverTurn` submit_turn failure →
retry once then exit non-zero → `outcomeFor`→OutcomeError → existing retry+notice path
(`index.ts:105`,`docker.go:276`). And finish `01a61a07`: home-not-writable → runner
returns error → OutcomeError → existing user notice, not log-and-spawn-config-less.

## Error-hygiene sweep: swallowed / mis-levelled errors in the hot path (2026-07-11, 2 FIXED, rest OPEN)

Sweep for the same class as the no-reply bug below (an error swallowed as
Warn/`_ =` while execution continues → silent degradation). Two shapes recur:
`if err == nil { use }` with the else branch missing, and `_ = err` on
bookkeeping writes that matter next turn.

**FIXED this pass** (both were missing error logs, no control-flow change):
- `routd/loop.go` `resolve()` — a transient `l.db.Routes()` read failure was
  indistinguishable from a clean route-miss, so the poll caller advanced the
  cursor and **dropped the inbound message** with zero log. Kept the
  fail-forward (retry risks a poison-message loop) but made the drop a loud
  `slog.Error`.
- `routd/turns.go:337` `handleDocument` — `s.deliver.Document` error had no
  `else`, so `send_file` failures returned HTTP 200 (row left pending) and were
  never logged, unlike the sibling `handleSend` (`turns.go:308` logs
  "deliver send failed"). Added the matching `slog.Error`.

**VERIFIED, OPEN** (real, left for prioritisation):
- `authd/main.go:108-112` — `core.LoadConfig()` failure → OAuth `/auth/*` not
  mounted, logged only `Warn`, server starts green (JWKS/health OK). Human login
  404s while everything reports healthy. **FIXED 2026-07-11** — elevated to
  `slog.Error` with the "login 404s while health green" symptom in the message.

**NOT A BUG** (verified benign, recorded so it isn't re-flagged):
- `container/runner.go:173` `ipcDir, _ := folders.IpcPath(in.Folder)` — the same
  folder is already resolved+validated via `buildMounts` (`runner.go:170`, which
  handles the error at `:560`) before line 173; `IpcPath` won't diverge for a
  folder that reached dispatch. Discard is a cosmetic asymmetry, not a live gap.
- `routd/prompt.go:29-39` (scout #2) — the store reads there don't surface an
  error at the call site (methods return zero-value/empty context); the one
  explicit `_ = UpdateObservedCursor` is **documented-benign** (comment L24-27:
  re-seeing observed messages is harmless, at-least-once by design).

**REPORTED by sweep, NOT yet independently verified** (read before fixing):
- `ipc/ipc.go:378-386` `writeJSON` — swallows `json.Marshal` error with bare
  `return`, no error frame → tool result vanishes, caller times out. (HIGH if
  confirmed.) **Verified + partially fixed 2026-07-13** — marshal failure now
  `slog.Error` (same log-add shape as the two FIXED items above; no
  control-flow change). Error-frame delivery stays open: writeJSON takes an
  opaque `v`, so a proper JSON-RPC error frame needs the request id threaded
  in — design, not a log-add.
- `ipc/ipc.go:348-361` — peer-cred/UID mismatch on MCP accept only `Warn`; gates
  every tool call for the group. (MED)
- `crackbox/pkg/admin/registry.go:84-107` `flushLocked` — `Warn`+return on
  disk-persist failure inside a void fn; handler still ACKs 200 → stale egress
  allowlist can reappear after restart. (MED)
- `routd/turns.go:670-676` `recordTurnResult` — discards `PutMessage`/`PutSession`
  errors (this path historically broke all replies post-split — verify first). (MED)
- `runed/manager.go:212-214`, `runed/docker.go:66` (steer silently no-ops),
  `ipc/ipc.go:340-345` (conn-limit reject, no error frame),
  `routd/dispatch.go:263,305,320` (terminal SetTurnState unlogged),
  `crackbox/pkg/host/host.go:130-132`, `routd/budget.go:32-43` (spend caps fail
  open on a DB hiccup). (MED/LOW)

Adapters (teled/discd/slakd/mastd/bskyd/reditd/emaid/chanlib) swept clean for
this class.

## Root-owned group home → agent runs config-less, silently never replies ("typing but no reply") (2026-07-11, ROOT CAUSE FIXED on krons, HARDENING OPEN)

Symptom (user-reported): the bot shows a typing indicator then never replies,
intermittently. Reproduced on krons folder `krons/content`: 4 turns (5729–5732)
dispatched + spawned, each ran ~33s, logged `[ant] Query done. Messages: 0,
results: 0` then `[ant] Input empty, exiting`, `code:0` — **no `turn_results`
row, no bot message**. rhias/nemo (healthy) replies normally.

**Root cause.** `groups/krons/content` was hand-created as **root:root** (Jul 7,
manual `mkdir`/host-CLI — the "never mkdir groups manually, use onbod/SetupGroup"
rule, `group_creation` memory). The container runs `user: 1000:1000`
(`compose/compose.go:694`); it can't `mkdir .claude` under the root-owned home, so
`cpDirImpl` (`container/runner.go:1146`) and `writeSettings`
(`container/runner.go:865`) both fail — **settings.json (which carries the
`mcpServers.arizuko` socket + outputStyle) is never written** → the SDK query
runs with no MCP tools and returns 0 messages → no reply. Note `krons/content` is
also a **ghost**: routed-to but NOT in `groups` (registered krons groups:
adshaus, happy, krons, krons/public/marble, mayai, rhias, rhias/content,
rhias/nemo). Fixed live: `chown -R 1000:1000 groups/krons/content`; uid-1000 can
now write `.claude`. Pending end-to-end confirm on next inbound.

**Hardening (OPEN).** Both write-failure sites swallow the error as `WARN` and
continue, producing a silently dead agent — violates CLAUDE.md "strict, not
magical / no silent fallbacks." Fix: when the group home is not writable (mkdir
`.claude` = permission denied, or settings.json write fails), fail LOUD — log
`ERROR` with the folder AND abort the spawn / deliver an error turn, rather than
running a config-less container. Also consider refusing to dispatch a folder with
no `groups` row (ghost-group guard) or auto-`chown` a fresh home to the run uid.

## Services hub: onbod + timed tiles are `Built=true` but their `/dash/` routes 404 (2026-07-10, OPEN)

`dashd/services.go:35-36` marks `onbod` and `timed` `Built=true` (Dash=
`/dash/onbod/`, `/dash/timed/`), so `handleServices` renders them as drill-in
links once the daemon's `/health` is reachable (`services.go:143`). But
`dashd/main.go` only mounts `GET /dash/routd/` and `GET /dash/runed/` — there is
**no `/dash/onbod/` or `/dash/timed/` handler**. Result: on a live instance the
onbod and timed service tiles link to a 404. The slice comment ("Set Built=true
once the per-daemon /dash/ route is shipped") documents the invariant that was
violated. Fix: either ship the two control-plane handlers, or set
`Built=false` on onbod+timed until they exist. Surfaced by the dashd-playwright
audit (`services.spec.ts:32`), 2026-07-10.

- **Status:** fixed 2026-07-11 — set `Built=false` on onbod+timed (restores the
  slice invariant: tiles show as probe-only status, no dead drill-in link).
  Shipping the two `/dash/` control-plane handlers stays a follow-up (flip back
  to `true` then).

## dashd-playwright: 4 drifted test assertions (harness maintenance, not dashd bugs) (2026-07-10, RESOLVED 2026-07-11 — d068eeaa)

The offline dashd playtest (`make play`) has 4 stale assertions that no longer
match verified-correct dashd behavior. Dashboard is sound; fix the tests:
**All 4 fixed in d068eeaa "[tests] dashd-playwright: fix 4 drifted assertions to
match current dashd" — verified this pass.**
- `davd.spec.ts:21` — asserts per-file `/dav/inbox/{MEMORY,CLAUDE}.md` links on
  `/dash/memory/`; they actually render on the group **settings** page
  (`groups_admin.go:306-308`). Point the test there.
- `groups.spec.ts:71` — clicks "delete group" without expanding the collapsed
  `<details>Danger zone` wrapper (`groups_admin.go:330`, added 2026-06-18) →
  button hidden → 30s timeout. Expand the `<summary>` first.
- `invites.spec.ts:44` — POSTs the raw token to `/revoke`; handler keys revoke on
  the short **ref** (`invites_test.go:175,182`) → 404. Revoke by ref.
- `read-only.spec.ts:34` — asserts "Active sessions" on `/dash/status/`; that row
  lives on the chat portal (`main.go:756`). Assert the group-count row instead.

## Stale chat-bound session → `error_during_execution` + silent context loss (2026-07-10, OPEN)

Symptom (reported as "agents dying after some minutes"): a long-lived group's
agent briefly errors mid-turn and appears to "forget" the thread. **Not a
crash** — 24/24 container exits over 12h were `code:0, timedOut:false`; no OOM
(containers have no mem limit, ~218MB usage), no query timeout.

**Root cause.** routd persists one session ID per chat (`sessions` table) and
resumes it next turn via `claude --resume <id>`. The container's SDK transcript
store (`groups/<folder>/.claude/projects/-home-node/*.jsonl`, bind-mounted) is
pruned by Claude Code's **native retention cleanup** (`.last-cleanup`, ~30-day).
When routd resumes a session whose transcript was pruned or never fully
persisted, the SDK returns *"No conversation found with session ID"* → the turn
result is `subtype=error_during_execution`.

Evidence (marinade `atlas`): `.last-cleanup` = `2026-07-09T19:31:48Z` matches an
`error_during_execution` at 19:31:41 to the minute; another at 2026-07-10
07:13:26 resumed `b6c2b287-…` which is absent from disk. On-disk transcripts
span 06-10 → 07-10 (retention window).

**Self-heals but lossy.** `ant/src/index.ts:335,440` catches the session error
and retries with a **fresh** session — the 07:13 case delivered `subtype=success`
~3 min later. Costs: (1) the failed attempt's `error_during_execution` is logged
as `Result #1` and may surface to the user before the retry; (2) the resumed
conversation context is discarded (agent "forgets" the thread); (3) wasted
latency + tokens.

**Candidate fixes (do NOT apply until asked):** (a) before `--resume`, stat the
transcript file for the stored session ID; if missing, start fresh silently
(never surface `error_during_execution` as a delivered result); (b) routd expires
/ nulls a session ID older than the CLI retention window; (c) raise or disable
Claude Code `cleanupPeriodDays` in the container settings so referenced
transcripts aren't pruned while `sessions` still points at them. (a)+(c) together
close both the error-flash and the context loss.

## Chat-initiated onboarding still dead after the compose fix — InsertOnboarding never fires (2026-07-09, OPEN)

A new group posts, hits a route miss, and **nothing happens** — no greeting, no
admission-queue row. The operator must discover the JID out-of-band (grep
routd.db `messages.chat_jid` / journalctl) and hand-run `arizuko group <inst> add
<jid> <folder>`. Witnessed on krons: `telegram:group/5567410596` posted repeatedly
05:34–06:11 2026-07-09, all stored, zero onboarding row created.

**Fix #1 (shipped, `4b4d868c`, deployed to krons):** the route-miss onboarding
insert lives in **routd** (`routd/loop.go:520`, gated by `l.onboardingEnabled` ←
`routd/cmd/routd/main.go:184` `envOr("ONBOARDING_ENABLED","false")`), but
`compose/compose.go` passed `ONBOARDING_ENABLED` only to **onbod**. Added it (+
`ONBOARDING_PLATFORMS`) to routd's env passthrough; rebuilt `arizuko:latest`
(the systemd `ExecStartPre` regenerates env from the image, so a host-side
regen alone reverts on restart — the image MUST be rebuilt); verified
`printenv ONBOARDING_ENABLED=true` inside the routd container.

**Still broken (residual bug).** With the flag confirmed live, a genuine
telegram-group route miss at 06:11:42 was processed (chat `agent_cursor`
advanced to 06:11:42) but produced NO onboarding row and NO error. All
preconditions are met and ruled out:
- `l.onboardingEnabled=true` (in-container printenv),
- `l.onbod != nil` (routd logged "service-token bootstrap via authd" at 06:09:11),
- `ONBOARDING_PLATFORMS` unset → `onboardingAllowed` true,
- no matching/catch-all route, `sticky_group`/`sticky_topic` empty (no engagement),
- `httpOnbod.do` returns an error on any non-2xx and `loop.go:531` logs
  `slog.Warn("insert onboarding", …)` — **no such warn exists**, so a swallowed
  403 is excluded. `InsertOnboarding` is simply never invoked for the miss.

So the `else if l.onboardingEnabled && l.onbod != nil` branch at `loop.go:520` is
not being entered even though both are true. Next step: add a trace log at the
top of the route-miss block (is `!r.ok` even reached, or does `resolve()` return
`ok=true`/`Observe!=""` for an unrouted telegram group?) and confirm the poll
actually walks `pollOnce` for this chat rather than the cursor being advanced at
ingest. Candidate causes: `resolve()` returns a non-miss for a bare group JID; or
`agent_cursor` is advanced before the onboarding branch runs (batching /
`last.Timestamp <= GetAgentCursor` skip at `loop.go:500`).

Note: `/chatid` is not an arizuko command (nothing echoes it) — JID discovery is
meant to BE the onboarding greeting + dashboard queue, exactly what this disables.

See also the onboarding redesign the operator wants (telegram groups = Slack
channels: new group → default staging folder, private-group interaction gated to
some users). That vision is being specced into `specs/5/` — the fix here should
land compatibly with it.

- **Status:** fixed 9fcb0bce (2026-07-12) — **root cause: two route-miss
  handlers, and the one that runs had no onboarding.** Ingest (`routd/server.go`
  `handleMessages`) calls `loop.Enqueue` directly, so the queue worker
  `processGroupMessages` (dispatch.go) consumes the miss within milliseconds —
  its miss branch was a drifted copy (observe + advance only, NO onboarding).
  By the next `pollOnce` tick (2s) the cursor was already current, so the
  `last.Timestamp <= GetAgentCursor` skip suppressed the ONLY onboarding site.
  Matches the live evidence exactly (cursor advanced, no row, no warn, no
  turn_context). Fix: one shared `Loop.routeMiss` (observe ingest → onboarding
  → advance) called from BOTH `pollOnce` and `processGroupMessages`;
  `InsertOnboarding` failure is now `slog.Error` + `onboardingFailedNotice`
  delivered to the chat (fail loud, fail-forward — cursor still advances, no
  poison replay). Tests: `TestQueuePathRouteMissInsertsOnboarding` (failed
  pre-fix), `TestRouteMissOnboardingFailureSurfaces`.

## 5/16 invites fold — agent-forwarder half deferred (2026-07-07, by design)

onbod's `/v1/invites` REST face is folded onto resreg (`154cd17f`, mirrors the
gates fold): create/list/revoke ride the shared handler + one tx + audit, and
invites is on onbod's `/openapi.json`. The AGENT invite tools (`invite_create`/
`invite_list`/`invite_revoke`, ipc/ipc.go registerRaw → GatedFns → routd/mcp.go
→ httpOnbod federation) are deliberately NOT folded yet — same precedent as the
route_tokens operator twin left hand-rolled where the shape doesn't fit resreg.

Blocker for a mechanical forwarder fold (Store nil → forward to onbod): the agent
face carries three things a single HTTP forward can't express —
- **caller-bound issuer** (`issued_by="agent:"+folder`, never a client arg),
- **accept_url synthesis** (routd computes `AcceptURLBase + /invite/<token>`),
- **revoke ownership as list-then-delete** (routd/mcp.go:141 lists THIS folder's
  invites and confirms the token before calling onbod's token-only DELETE — the
  comment: "an agent must not revoke another folder's invite even though the wire
  call would"). A naive forwarded DELETE `/v1/invites/{token}` REGRESSES this into
  a cross-folder revoke.

Safe fold needs onbod's DELETE to gain an `issued_by` scope (`DELETE ... WHERE
token=? AND issued_by_sub=?`) so the forwarder passes the caller-bound issuer and
onbod enforces ownership in one call; create needs the forwarder to inject issuer
+ synthesize accept_url. That's the "one-owner + federation" design step, not a
mechanical fold — left for a focused change on the live agent surface.

## 5/16 two-faces rollout — status (updated 2026-07-06)

All 5 cold-tier agent-MCP faces ride one resreg.Resource via the injected Gate
seam. REST faces folded onto their shared handlers: web_routes (27537500 +
9d649e59 list-all leak fix), acl (44c53cef, closed a cross-folder scope hole),
routes (3195f867), tasks (0b6ca53e). Open:

1. **routes/tasks REST bake the agent tier model in the handler — two failure modes
   (being fixed by the containment decouple).** These handlers run
   `auth.AuthorizeStructural(auth.Resolve(Caller.Folder), …)` — a predicate written for
   the agent tier identity — against a REST operator whose real authority is `ownsFolder`
   (own-or-descendant, tier-independent). The two diverge in BOTH directions:
   - **routes — over-restrictive (no leak).** A tier-1+ REST token managing its OWN
     folder gets 403 (tier-1 needs a STRICT descendant); a tier-2 REST token can't manage
     routes at all. Latent (existing tests use tier-0 folders where the tier cap is a no-op).
   - **tasks — LIVE CROSS-TENANT LEAK (security).** `tasksRESTGate` keys `ownsFolder(jwt,
     Caller.Folder)` where `Caller.Folder==jwt` for per-task ops → a no-op; the only per-task
     check is the handler's tier cap, which is LOOSER than ownsFolder: a **tier-0 REST
     operator can `DELETE /v1/tasks/{anyId}` and cancel ANOTHER tenant's task**; a tier-1
     operator can cancel/patch any same-world sibling's task (`isInWorld`). Uncovered
     (`TestRESTTaskScopedSelfService` uses tier-2, where exact-own ≈ ownsFolder). Present on
     main since the tasks fold (0b6ca53e).
   **RESOLVED 2026-07-06 (0d25b687).** Decoupled containment into a routd-internal
   per-face `containFn(caller,action,target)` — agent→tier `AuthorizeStructural`,
   REST→`ownsFolder` — dropping the baked tier check from the handler (resreg untouched;
   set_routes keeps its face-agnostic `routeTargetWithin` loop). TDD'd: the two tasks
   guards returned 200+deleted pre-fix (leak confirmed), 403 post-fix. Guards:
   `TestRESTTask{Tier0NoCrossTenant,Tier1NoWorldLeak,Tier2Descendant}`,
   `TestRESTRoute{Tier1OwnSubtree,Tier2OperatorOwnSubtree}`. Agent tier semantics
   (`TestRoutesMCP_*`/`TestScheduledTasksMCP_*`) unchanged.
1b. **scheduled_tasks REST (`/v1/tasks`) not OpenAPI-discoverable (minor, follow-on).**
   OpenAPI truthfulness shipped (7c14efd6 — docs emit real Endpoints, no phantom paths).
   But `scheduled_tasks`'s REST face is mounted at `/v1/tasks/{taskId}` (`mountTasks`
   override) with a DIFFERENT action set (list/get/patch/cancel) than its agent tools
   (schedule/pause/resume/cancel/list), so it can't share the resource's registered
   `ScheduledTasksEndpoints` (`/v1/scheduled_tasks`, agent-derivation) — and advertising
   the resource would emit the wrong paths. So it's correctly LEFT OUT of routd's
   `/openapi.json` list (`[routes, web_routes, acl]`). Truthfully advertising `/v1/tasks`
   needs a separate REST resource declaration for the tasks operator face — a follow-on,
   not a phantom.
2. **network_rules: already clean, out of scope.** Its `AuthorizeStructural` lives in the
   injected Gate (network_rules_resource.go:187), the handler is auth-agnostic, and it has
   NO REST twin — the model the decouple brings routes/tasks to. Adopt the same containFn
   only if/when a REST face lands.
3. **Tool-browser drift — RESOLVED 2026-07-11** (df9ebad3 single-sources facade-tool
   MCP metadata; d5023c60 `ipc.ListTools` renders grant-visible cold-tier facade tools
   for dashd; `facade_tools_test.go`). `dashd`'s schema browser now shows the migrated
   cold-tier facade tools. Verified this pass (`ipc/ipc.go:2263`).
4. **`container/runner.go` standalone ServeMCP (minor).** The non-split dev path
   (`!ExternalMCP`) gets no postBuild → no facade tools there. Production (split, routd
   hosts the socket) unaffected.
5. **surrogate refresh: bodyless-4xx nulls the row (5/15).** RESOLVED 2026-07-07 (e813efd5):
   `Engine.Refresh` now signals `ErrReconnect` ONLY on a definitive OAuth error body
   (`tr.Error` set); a bodyless/unparseable non-2xx is a transient error that keeps the
   credential. Test `TestRefresh_BodylessErrorIsNotReconnect`.
6. **flaky test: `TestRefreshRotationRaceSingleWinner` (authd/bugfix_test.go:131).** A
   refresh-rotation concurrency race test that intermittently fails under full-suite parallel
   load but passes 5/5 in isolation. Pre-existing, unrelated to the 5/16/5/15 work. Needs a
   sync point or serialization in the test harness. Low priority (not a product bug).


## oracle skill + examples tell operators to folder-scope CODEX_API_KEY, which the store rejects (2026-07-02, open)

Spec 5/14 put `CODEX_API_KEY`/`OPENAI_API_KEY` in `store.EnvProfileKeys`, so `validateScope`
(`store/secrets.go:128`) rejects them at `scope_kind='folder'` — model creds are user-only (BYOA)
or platform (host `.env`). But `ant/skills/oracle/SKILL.md` "Path B" and several
`ant/examples/*/PRODUCT.md` still show `[[secret]] key = "CODEX_API_KEY"` at **folder** scope.
Under 5/14 enforcement that write 400s.

**Design call needed** (do not silently change either side): (a) keep the model — codex/openai are
user-only like Anthropic — and fix the oracle docs/examples to user-scope or platform `.env`; or
(b) carve `CODEX_API_KEY`/`OPENAI_API_KEY` OUT of the folder-reject as a shared *team* capability
(a folder pays for codex for everyone), keeping only the Anthropic keys user-only. The platform
`.env` fallback for all four now works (`container/runner.go:readSecrets` fixed 2026-07-02); this
is purely about whether folder-scope is allowed. Fixing the docs also requires an ant image rebuild.

- **Severity:** medium (breaks documented operator flow)
- **Scope:** store/secrets.go:128 EnvProfileKeys; ant/skills/oracle/SKILL.md, ant/examples/*/PRODUCT.md
- **Source:** credential refine-review 2026-07-02
- **Status:** RESOLVED 2026-07-05 — user decided codex scopes like Claude: one global (platform
  `.env`) + BYOC (user-scoped), NEVER folder. Code already enforced this (store rejects folder;
  readSecrets carries all 4 keys). Only `oracle/SKILL.md` Path B claimed "folder secret" — fixed to
  env-profile. The `PRODUCT.md` examples were already correct (they use `[[env]]` = platform, not
  `[[secret]]` folder). NOTE: the SKILL.md fix reaches live agents only after an ant image rebuild.

## OpenAPI convention emits phantom/divergent paths for hand-rolled resources (2026-07-02, open)

`resreg/openapi.go:resourcePaths` synthesizes the 5/8 CRUD convention (`/v1/<name>`
GET/POST/GET-one/PATCH/DELETE) from `(RowType, PKFields)`, ignoring each resource's real
`Endpoints`. Truthful only for `proxyd_routes` (the one resource whose live RegisterREST
matches the convention). The others advertise paths their daemons don't serve or serve with
different shapes:

- `timed` advertises `/v1/scheduled_tasks` CRUD — serves none (only `/health`,`/dash`,`/openapi.json`).
- `onbod` advertises `/v1/onboarding_gates` CRUD — actually serves `/v1/onboarding`.
- `routd` advertises `routes`/`web_routes`/`acl` — hand-rolled `server.go` diverges on method/param
  (routes `{id}` not `{seq}`, has `PUT` + no `PATCH`; acl remove is body-`DELETE /v1/acl`; etc).

The `secrets` read-surface leak (convention emitted `GET /v1/secrets/{scope_kind}`) is FIXED by
dropping `secrets` from routd's OpenAPI list (2026-07-02). The rest is the 5/16 ipc→resreg
migration: when each becomes a real resreg resource served via RegisterREST with true `Endpoints`,
change `resourcePaths` to emit from `Endpoints` (empty → schema-only) and the drift closes for good.

- **Severity:** medium (public API doc misleads; not a live leak — phantom paths 404)
- **Scope:** resreg/openapi.go:resourcePaths; timed/split.go, onbod/main.go, routd/cmd/routd/main.go
- **Source:** resreg refine-review 2026-07-02
- **Status:** MOSTLY RESOLVED 2026-07-11 — `resreg/openapi.go` now emits each resource's
  real `Endpoints` (7c14efd6, single-sourced), so routd (routes/web_routes/acl) and onbod
  (gates → `/v1/gates`, 4bd09532) are truthful. **Only `timed` remains**: `timed/split.go:13`
  still lists `scheduled_tasks` in its OpenAPI while serving only health/openapi/dash. Fix is
  a decision (Q8): emit an empty resource list for timed, or give timed a real `/v1` face.

## proxyd_routes list handler returns {routes:[]} envelope; OpenAPI documents a bare array (2026-07-02, open)

`proxyd/resource.go:187` returns `map[string]any{"routes": out}` but `resreg/openapi.go` documents
list `200` as a bare `array<ProxydRoutes>`. The one resource whose paths are correct still serves a
body shape its own doc contradicts. Fix: return the bare `[]Route` (matches the engine convention;
no in-tree consumer indexes the `routes` key) or document the envelope. Deferred: needs a consumer
sweep (webd forwarder relays the body) to confirm the bare-array change is safe.

- **Severity:** low-medium (doc/handler drift on one resource)
- **Scope:** proxyd/resource.go:187
- **Source:** resreg refine-review 2026-07-02
- **Status:** open

## resreg.Caller.Name is write-only (2026-07-02, open)

`resreg/resreg.go:76` `Caller.Name` is set by both adapters (`proxyd/resource.go:366`,
`webd/routes_mcp.go:50`) but read by nobody — `buildEvent` uses `Sub` for both audit `Actor` and
`ActorSub`. Either drop the field + its two assignments, or use it as the human-readable audit
`Actor`. Left this round (the drop cascades through the two call sites' `name` computations; the
use-as-Actor changes audit semantics — neither is a pure cleanup).

- **Severity:** minor
- **Scope:** resreg/resreg.go:76
- **Source:** resreg refine-review 2026-07-02
- **Status:** fixed 2026-07-12 — dropped the field + both assignments (audit Actor
  stays Sub, unchanged); the proxyd `name` computation went with it, webd's `name`
  param remains live for the forwarded X-User-Name header

## ConnectorSecrets resolves folder scope only — user BYOA key never reaches MCP subprocess

**Severity**: medium
**Found**: 2026-06-26
**Location**: `routd/sibling_db.go:ConnectorSecrets` (calls `FolderSecrets(folder)`, not `FolderSecretsForUser`)

A user who sets `GITHUB_TOKEN` in their user-scoped secrets (via `/dash/me/secrets`)
expects it to be used when they invoke a GitHub MCP connector tool. It isn't.
`ConnectorSecrets` reads folder scope only — the user-scoped override is invisible
to the connector subprocess.

Fix requires:
1. `sibling_db.go:ConnectorSecrets(folder, callerSub string)` — add `callerSub` param,
   call `FolderSecretsForUser(folder, callerSub)` instead of `FolderSecrets(folder)`.
2. Thread `callerSub` (the turn's trigger sender) into `ipc/ipc.go:1027` where
   `db.ResolveConnectorSecrets(folder, ...)` is called.
3. `routd/mcp.go:569` pass `callerSub` alongside `folder` into the resolver.

Spec: `specs/5/14-credentials.md § ConnectorSecrets user-scope`.

- **Status:** fixed 0d244973 (2026-06-26) — ConnectorSecrets now takes callerSub, calls FolderSecretsForUser

## Slack threading ignored for in-thread triggers when thread_replies=false (2026-06-25, fixed)

`routd/turns.go:deliverTurn` suppressed `threadRoot` (no new thread) when
`thread_replies=false`, but still passed `tc.Topic` as `threadID` to the platform.
On Slack this means: user types inside an existing thread → topic != "" → bot reply
carries the thread_ts → stays in the thread → invisible in main channel. The
`thread_replies=false` setting only blocked NEW threads, not existing thread chains.
Fixed in 2b2e6062: `deliverTurn` now zeroes `threadID` too when threading is disabled.
All atlas groups on marinade set to `thread_replies=0` (DB update).

- **Status:** fixed 2b2e6062 (2026-06-25)

## Channel adapter silent no-op after auto-deregister (2026-06-25, fixed)

`chanlib/run.go` registered with routd once at startup. After 3 consecutive `/health`
503s (e.g. transient Telegram API Bad Gateway), routd auto-deregisters the adapter.
Adapter recovers and `connected.Store(true)` flips health back to 200, but it never
re-registers. In the split topology, JWT auth and channel registration are decoupled:
inbound messages still reach routd (JWT passes), but outbound delivery silently fails
(`deliverRow` drops the error). 3 bot replies stuck at `status=pending` on krons
today. Fixed in 3625947b: 3-minute re-register heartbeat. Also added error logging
in `deliverRow` (8e61521a) so failures are no longer silent.

- **Status:** fixed 3625947b + 8e61521a (2026-06-25)

## `arizuko network` CLI writes/reads messages.db, not routd.db (2026-06-21, fixed)

`cmd/arizuko/network.go:17` opens the DB via `store.Open(dir/store)`, which targets
`messages.db` (store/store.go:51). But in the split topology `network_rules` is owned by
routd and lives in `routd.db` (store has `OpenRoutd` for exactly this). So `arizuko network
<inst> allow|deny|list|resolve` operates on the wrong DB: writes land in a `network_rules`
table routd never reads, and `resolve` shows stale messages.db rows, not routd's live
allowlist. The operator thinks they edited the egress allowlist but the agent's crackbox
rules are unchanged.

Discovered while fixing sloth main/trading web access: `allow` reported success but the host
stayed denied because the rule went to messages.db. Worked around by inserting directly into
routd.db. The agent-facing MCP path (routd/mcp.go → s.db.AddNetworkRule) is correct — only the
CLI is wrong.

Fix: `cmdNetwork` should call `store.OpenRoutd(dataDir/store)` instead of `store.Open`.

- **Severity:** medium (silent — operator egress edits via CLI are no-ops; no error surfaced)
- **Scope:** cmd/arizuko/network.go
- **Affected:** `arizuko network allow|deny|list|resolve` on all split instances
- **Source:** cmd/arizuko/network.go:17 (`store.Open` → should be `store.OpenRoutd`)
- **Status:** fixed 2026-06-21 (2dfa5670 — store.Open → store.OpenRoutd)

## slakd message IDs are per-channel, not globally unique (2026-06-19, open)

`slakd/bot.go:521` uses `m.TS` (Slack timestamp) as the `InboundMsg.ID`. Slack `ts` is unique
within a channel but not across channels in the same workspace. Two channels posting at the same
microsecond get the same `ts` → `INSERT OR IGNORE` would silently drop the second. Same class of
bug as the teled cross-chat ID collision (fixed 2026-06-19). In practice extremely unlikely
(Slack backend is monotonic per-workspace, microsecond precision), but structurally unsound.

Fix: prefix with the channel JID, e.g. `jid + "/" + m.TS`. Same pattern as teled.
Reaction IDs (`r.Item.TS + ":r:" + r.Reaction`) need the same treatment.

- **Severity:** low (theoretical — Slack TS precision makes collision astronomically unlikely)
- **Scope:** slakd
- **Affected:** slakd/bot.go:521,567 — multi-channel Slack workspaces
- **Status:** fixed 2026-07-11 — message + reaction IDs prefixed with the channel
  `jid`, mirroring teled (`bot.go:291,209`). Raw-TS API targeting uses `TargetID`,
  not `InboundMsg.ID`, so prefixing is safe.

## dashd reads SQLite directly — violates spec 6/1 read-path contract (2026-06-16, open)

`dashd/main.go:106, 170-177, 182-191` opens `routd.db`, `onbod.db`, and `messages.db` directly.
Spec 6/1 §"Read-path: /v1 only, no direct DB" forbids this. Direct DB reads miss all live process
state (runed active runs, routd breaker/queue depth, adapter session health, timed next-tick) —
state that only exists in daemon memory. The cockpit spec was written to force /v1 reads precisely
because snapshots can't see that state.

- **Severity:** high
- **Scope:** dashd, cockpit spec 6/1
- **Affected:** all dashd pages (groups, status, invites, memory, tokens, activity)
- **Source:** dashd/main.go:106,170-191; specs/7/1-cockpit-hub.md
- **Status:** open
- **Fix:**

## No operator audit-log page (2026-06-16, open)

`audit.Emit` writes events (dashd/main.go:114-124) but no `/dash` handler reads them. Spec 6/5
covers authd identity events only; there is no operator-facing "who did what" view for dashd
actions (approve/deny onboarding, revoke token, etc.). Table stakes for multitenant control planes.

- **Severity:** medium
- **Scope:** dashd, authd
- **Affected:** operators — no audit trail in UI
- **Source:** dashd/main.go:114-124; grep: no audit handler in dashd
- **Status:** resolved — /dash/audit/ shipped
- **Fix:**

## No usage/analytics page (2026-06-16, open)

`GroupUsageBulk` (dashd/main.go:789-822) surfaces msgs, tokens/7d, $/7d, last-active per folder —
but only on the groups detail page, three clicks deep. There is no instance-wide usage summary:
no aggregate message volume, no agent response-rate trend, no total spend. Specs 6/1 and 6/2 have
zero occurrences of "usage", "throughput", or "metric". Not specced, not built.

- **Severity:** medium
- **Scope:** dashd specs 6/1, 6/2
- **Affected:** operators, business visibility
- **Source:** dashd/main.go:789-822; specs/7/2-dashd-hub.md
- **Status:** resolved — /dash/usage/ shipped
- **Fix:**

## dashd: routes + task-detail visible to any auth'd user (2026-06-17, open)

`handleRoutes` GET (`routes_admin.go:28`) gates on `requireUser` only — any auth'd caller can
enumerate the full route table including routes targeting other tenants' folders. `handleTaskDetail`
GET (`tasks_admin.go:21`) also gates on `requireUser` with no folder-scope check — any auth'd
caller who knows a task ID can read its full prompt and owner. Both list pages filter correctly
(`handleGroups`, `handleTasks`), but their detail/read views don't re-check scope.

- **Severity:** medium
- **Scope:** dashd/routes_admin.go:28, dashd/tasks_admin.go:21
- **Affected:** multitenant: non-operator users can see other tenants' routes/task details
- **Source:** bucket A refine review 2026-06-17
- **Status:** resolved d52b2c50 + b99138fb (2026-06-06) — stale entry, verified
  2026-07-13: task detail gates on the task's owner via `visible()`
  (tasks_admin.go), routes list filters each row by `visible(target folder)`
  (routes_admin.go:65)
- **Fix:** add `requireOperator` or `requireVisible(folder)` gate to those GET handlers

## dashd: adminDB() panics if routd.db open fails (2026-06-17, open)

`main.go:226-231` warns and continues if `routd.db` fails to open, leaving `d.dbRoutd = nil`.
`adminDB()` (`main.go:307`) returns `d.dbRoutd` unconditionally. Any request to `handleStatus`,
`handleGroups`, `handleTasks`, `writeActivityRows`, etc. then panics at the `.Query` call.

- **Severity:** medium
- **Scope:** dashd/main.go:226-231, main.go:307
- **Affected:** startup — routd.db missing/corrupt causes panic on first request
- **Source:** bucket A refine review 2026-06-17
- **Status:** resolved a51989cc (2026-07-05) — stale entry, verified 2026-07-13:
  audited all 92 `adminDB()` call sites; every one is nil-guarded directly or
  sits behind a fail-closed gate (`requireVisible`/`requireAdmin` 503 on nil,
  chat portal early-returns on empty folders). Startup keeps Warn+continue by
  design (nil → degraded 503s, comment main.go:223)
- **Fix:** make routd.db open failure fatal, or nil-check in adminDB() and return 503

## Activity page: no relative timestamps and no pagination (2026-06-16, open)

`writeActivityRows` emits raw ISO8601 nanosecond timestamps (dashd/main.go:688). No "N min ago"
rendering anywhere in dashd. Activity hard-caps at 50 rows with no "older" affordance. During an
incident you cannot scroll back past 50 events and must parse UTC strings by eye.

- **Severity:** low
- **Scope:** dashd/main.go activity handler
- **Affected:** operators during incident response
- **Source:** dashd/main.go:688, 628-639
- **Status:** partial — relative timestamps added (`relativeTS`, abbr tooltip still shows ISO full);
  pagination not yet implemented
- **Fix:** `relativeTS` function + writeActivityRows updated (2026-06-16)

---
## Codex audit 2026-06-18 — dashd full sweep

---

## [SEC] channels: pairAuth fails open + innerHTML XSS in pair-status polling (2026-06-18, open)

Two issues in `channels.go`. (1) `pairAuth` at line 33: if service-token minting fails, dashd
still sends the call to whapd without Authorization — auth bypass risk on a sensitive operator
action. (2) Line 91: JS polling renders `expires_at`/`since` from whapd JSON directly into
`innerHTML` — admin-page XSS sink if whapd or a proxy returns attacker-controlled strings.

- **Severity:** high
- **Scope:** dashd/channels.go:33, :91
- **Source:** codex audit 2026-06-18
- **Status:** resolved — pairAuth returns error + fail-closed; JS uses textContent/createTextNode (80bd29da)
- **Fix:** (1) fail closed when svc token unavailable; (2) use `textContent` or escape fields

## [SEC] routes: handleRouteUpdate authorizes new target, not existing row (2026-06-18, open)

`routes_admin.go:194`: PATCH only requires admin on the new target folder. A caller who knows a
route ID in another folder can PATCH it into a folder they own — changing match/seq/target on an
otherwise inaccessible rule.

- **Severity:** high
- **Scope:** dashd/routes_admin.go:194
- **Source:** codex audit 2026-06-18
- **Status:** resolved — loads existing row first, requires admin on old + new folder (80bd29da)
- **Fix:** load current row first; require admin on both old and new folder

## [SEC] invites: raw token exposed in revoke URL (2026-06-18, open)

`invites.go:56`: token is a path param in the revoke form action — leaks live bearer into request
logs, browser history, and proxy access logs.

- **Severity:** high
- **Scope:** dashd/invites.go:56
- **Source:** codex audit 2026-06-18
- **Status:** resolved a51989cc — stale entry, verified 2026-07-12: revoke URL
  carries `inviteRef` (sha256 of token); handler resolves ref→token server-side
- **Fix:** revoke by opaque row ID, not token

## [SEC] route_tokens: encodeJID/decodeJID not reversible; webhook label unvalidated (2026-06-18, open)

`route_tokens.go:131`: `decodeJID` maps every `--` back to `/`, so a label containing `--` revokes
the wrong JID. `route_tokens.go:43`: label is unvalidated — can contain `/` or control chars that
break JID namespace.

- **Severity:** high
- **Scope:** dashd/route_tokens.go:43,:131
- **Source:** codex audit 2026-06-18
- **Status:** resolved — label validated against `[a-zA-Z0-9._-]+` (80bd29da);
  encodeJID collision fixed a51989cc, verified 2026-07-13: `encodeJID` =
  `url.PathEscape` (reversible), `decodeJID` deleted (mux PathValue unescapes)
- **Fix:** use `url.PathEscape`; validate label against `[a-zA-Z0-9._-]+`

## [SEC] chat: raw bearer tokens visible to read-scoped dashboard users (2026-06-18, open)

`chat.go:152,262`: session list renders raw chat bearer tokens in Continue links. Any read-scoped
user who can see the portal sees a reusable write-capable token, bypassing the admin gate on token
minting.

- **Severity:** high
- **Scope:** dashd/chat.go:152,:262
- **Source:** codex audit 2026-06-18
- **Status:** resolved a51989cc (2026-07-05) — stale entry, verified 2026-07-13:
  continue links render only for folders the caller admins (`callerAdmins` map
  in the portal, `admin` gate on the group page; `folderSessionTokens` fetched
  only when admin) — read-scoped viewers see rows without tokens
- **Fix:** restrict session listing to admins, or hide the raw token from non-admin views

## [SEC] routd/runed: retry + kill endpoints missing CSRF protection (2026-06-18, open)

`routd_page.go:106`: POST /dash/routd/retry gated by `requireOperator` only — no same-origin
check. `runed_page.go:135`: POST /dash/runed/kill same issue, higher impact (terminates live runs).

- **Severity:** high
- **Scope:** dashd/routd_page.go:106, dashd/runed_page.go:135
- **Source:** codex audit 2026-06-18
- **Status:** resolved — `requireSameOrigin` added to both (1dc9b356)
- **Fix:** add `requireSameOrigin` to both write endpoints

## [SEC] grants: effect silently defaults to allow; nil adminDB in POST paths (2026-06-18, open)

`grants_admin.go:184`: invalid `effect` value silently creates an allow grant. `grants_admin.go:46`
guards GET but not POST — nil adminDB panics on add/revoke.

- **Severity:** high
- **Scope:** dashd/grants_admin.go:184,:46
- **Source:** codex audit 2026-06-18
- **Status:** resolved — invalid effect → 400; nil adminDB guard on add + revoke (91159586)
- **Fix:** reject invalid effect with 400; nil-guard adminDB in POST handlers

## tasks: new tasks have no next_run; cron not validated on create (2026-06-18, open)

`tasks_admin.go:216`: tasks inserted with `status='active'` but no `next_run` — timed only fires
rows with `next_run <= now`, so dashboard-created tasks never schedule. `tasks_admin.go:201`:
invalid cron/interval strings stored silently; timed logs warning and task stays dead.

- **Severity:** high
- **Scope:** dashd/tasks_admin.go:201,:216
- **Source:** codex audit 2026-06-18
- **Status:** resolved — `taskNextRun` computes next_run at create; invalid cron → 400 (91159586)
- **Fix:** compute next_run at create time; validate cron using same parser as timed

## tasks: state-machine transitions not enforced (2026-06-18, open)

`tasks_admin.go:145`: `resume` can revive cancelled/completed tasks without restoring `next_run`;
`pause`/`cancel` can clobber a firing task. Produces zombie active tasks that never fire.

- **Severity:** medium
- **Scope:** dashd/tasks_admin.go:145
- **Source:** codex audit 2026-06-18
- **Status:** resolved — SQL-level guards on pause/resume/cancel; next_run recomputed on resume (91159586)
- **Fix:** enforce valid transitions in SQL or code; recompute next_run on resume

## groups: delete parent leaves orphaned child rows; not atomic (2026-06-18, open)

`groups_admin.go:403`: deleting `corp` removes ACL/routes for the whole subtree and purges files,
but leaves `corp/eng` rows in `groups`. Orphaned child groups become invisible but persistent.
Also not atomic — if ACL cleanup fails after groups DELETE, stale rows resurface.

- **Severity:** medium
- **Scope:** dashd/groups_admin.go:403
- **Source:** codex audit 2026-06-18
- **Status:** resolved — delete wrapped in transaction; descendant group rows purged atomically (91159586)
- **Fix:** delete descendant groups rows in same transaction; forbid non-leaf delete or wrap all in tx

## groups: observe-window and max_children forms corrupt defaults on save (2026-06-18, open)

`groups_admin.go:246,:286`: GET maps stored -1/NULL to `0` for display; POST saves `0` back as a
real override. Opening settings and clicking save changes "use defaults" to "0 msgs/0 chars" (send
nothing) or "0 max_children" (spawning disabled).

- **Severity:** medium
- **Scope:** dashd/groups_admin.go:246,:286
- **Source:** codex audit 2026-06-18
- **Status:** resolved — -1/NULL renders blank; 0 clears to default via ClearGroupMaxChildren (91159586)
- **Fix:** render -1/NULL as blank; treat submitted 0 as clear-to-default in POST handler

## main: writeTaskRows and activity feed apply LIMIT before visibility filter (2026-06-18, open)

`main.go:730`: `writeTaskRows` fetches LIMIT 500 then filters in Go — scoped users miss tasks that
sort after row 500. `main.go:808`: activity feed fetches 1000 rows then filters — same class.

- **Severity:** medium
- **Scope:** dashd/main.go:730,:808
- **Source:** codex audit 2026-06-18
- **Status:** resolved a51989cc (2026-07-05) — stale entry, verified 2026-07-13:
  writeTaskRows pushes the owner filter into SQL (`ownerVisibleSQL`; glob grants
  fall back to a 5000 over-fetch). Activity deliberately over-fetches 1000 for
  non-operators (visibility needs Go-side `jidFolder` route resolution, not
  SQL-expressible); residual: scoped rows older than the newest 1000 stay hidden
- **Fix:** push visibility filter into SQL; or page until N visible rows found

## main: memory write/delete broken for nested group folders (2026-06-18, open)

`main.go:1073`: `parseMemoryPath` splits on first `/` after `/dash/memory/`. For
`corp/eng`, folder="corp" and rel="eng/MEMORY.md" — fails allowlist. Read page shows nested groups
correctly but mutations don't work.

- **Severity:** medium
- **Scope:** dashd/main.go:1073
- **Source:** codex audit 2026-06-18
- **Status:** resolved — parseMemoryPath scans split points right-to-left; nested folders work (91159586)
- **Fix:** route mutations through a wildcard `{folder...}` path param or parse from suffix

## audit: pagination cursor off by one (2026-06-18, open)

`audit_page.go:83`: `lastID` is set on the 51st lookahead row, then page is sliced to 50 and
`before=<lastID>` skips the actual 50th row on the next page.

- **Severity:** medium
- **Scope:** dashd/audit_page.go:83
- **Source:** codex audit 2026-06-18
- **Status:** resolved — cursor taken from last displayed row (29e759f7)
- **Fix:** capture cursor from row 50, not the lookahead row

## routd: retry clears errored on all messages, not just errored=1 (2026-06-18, open)

`routd_page.go:123`: `UPDATE messages SET errored=0 WHERE chat_jid=?` resets all rows, including
non-errored history.

- **Severity:** medium
- **Scope:** dashd/routd_page.go:123
- **Source:** codex audit 2026-06-18
- **Status:** resolved — `AND errored=1` added (1dc9b356)
- **Fix:** `UPDATE messages SET errored=0 WHERE chat_jid=? AND errored=1`

## services: davd probe path wrong; timeout classified as unknown (2026-06-18, open)

`services.go:54`: probe uses `GET /health` for all daemons, but davd's healthcheck is `GET /`.
A healthy davd always shows err. `services.go:60`: timeout mapped to `unknown` — should be `err`
(unknown is for DNS failures; timeout means deployed-but-down).

- **Severity:** medium
- **Scope:** dashd/services.go:54,:60
- **Source:** codex audit 2026-06-18
- **Status:** resolved — per-service Probe field; timeout → statusErr; DNS → statusUnknown (29e759f7)
- **Fix:** per-service probe path; classify timeout as err not unknown

## usage: 7-day query covers 8 days; GroupUsageBulk IN list will fail at scale (2026-06-18, open)

`usage_page.go:111`: `date('now', '-7 days')` includes the full day 7 days ago + today = 8 days.
`usage_page.go:38`: `GroupUsageBulk` builds `IN (...)` from all folder names — hits SQLite param
limit with many groups.

- **Severity:** medium
- **Scope:** dashd/usage_page.go:38,:111
- **Source:** codex audit 2026-06-18
- **Status:** partial — datetime fix applied (29e759f7); GroupUsageBulk IN-list still open
- **Fix:** use `datetime('now','-7 days')` for rolling window; rewrite bulk query with JOIN

## chat: loadChatSessions limits before filtering; nondeterministic continue links (2026-06-18, open)

`chat.go:90`: newest 200 rows fetched then filtered in Go — older sessions disappear once table
grows. `chat.go:361`: `folderSessionTokens` infers topic→token by time proximity; two sessions
without an early message produce nondeterministic continue links.

- **Severity:** medium
- **Scope:** dashd/chat.go:90,:361
- **Source:** codex audit 2026-06-18
- **Status:** partial — LIMIT-before-filter half resolved a51989cc (2026-07-05,
  verified 2026-07-13: `folder IN (...)` pushed into SQL before the LIMIT 200).
  Still open: `folderSessionTokens` topic→token linkage by time proximity
  (nondeterministic continue links) — needs persisted linkage, design change
- **Fix:** push filter into SQL; persist topic-session linkage explicitly

## runed: dead session_id variable in run query (2026-06-18, open)

`runed_page.go:52`: `COALESCE(session_id,'')` selected and scanned but never used — dead code in
the hot path.

- **Severity:** low
- **Scope:** dashd/runed_page.go:52
- **Source:** codex audit 2026-06-18
- **Status:** resolved — dropped from SELECT (1dc9b356)
- **Fix:** drop from SELECT and remove variable

## main: duplicate profile nav link + dead portal .Err field (2026-06-18, open)

`main.go:463`: `profile` appears in both `navLinks` and the identity badge — double aria-current
on `/dash/profile/`. `main.go:540`: portal template `.Err` field is always empty — dead branch.

- **Severity:** low
- **Scope:** dashd/main.go:463,:540
- **Source:** codex audit 2026-06-18
- **Status:** resolved — profile removed from navLinks; .Err branch removed (91159586)
- **Fix:** remove profile from navLinks (keep badge); remove .Err from portal template

## ext tools: InputSchema never populated — agents have no parameter schema (2026-06-26, open)

`ipc.ExtTool.InputSchema` is `json.RawMessage` and `ipc/ipc.go` registration gates on
`len(tool.InputSchema) > 0` to attach it. But `routd/ext.go:LoadExtProviders` never sets
it — the `extToolConfig` struct has no schema field. Every ext tool registers with no
MCP input schema. LLM agents infer args from tool name + description; schema-driven
callers (and accurate UI hint displays) see nothing.

- **Severity:** low (LLM agents guess correctly from description; functional gap for strict callers)
- **Scope:** `ipc/extcall.go:ExtTool`, `routd/ext.go:extToolConfig`, `routd/extproviders/*.toml`
- **Affected:** all REST-descriptor tools (cloudflare, porkbun, gandi, namecheap)
- **Source:** refine review 2026-06-26
- **Status:** RESOLVED 2026-07-11 — `extToolConfig` gained a `[[param]]` array
  (`routd/ext.go:42`), `extInputSchema` builds the MCP schema (`:63`), and it's
  populated on registration (`:157`), consumed at `ipc/ipc.go:990`. Verified this pass.

---

## Nav chrome: no identity/scope signal (2026-06-16, open)

The persistent nav (`dashNavFor`) shows no identity, no folder scope, no operator badge. The only
tell for operator status is whether `services`/`invites` links appear. For a console that gates
destructive mutations, the current identity and scope must be visible at all times, not one click
away (profile page). CTO audit criterion: auth trust signal always on screen.

- **Severity:** low
- **Scope:** dashd/main.go dashNavFor
- **Affected:** all dashd pages
- **Source:** dashd/main.go:362-376
- **Status:** resolved — name + ◆ operator badge added to nav as profile link (2026-06-16)
- **Fix:** `dashNavFor` identity badge using X-User-Name/X-User-Sub + operator flag

## `arizuko apply`/`plan`/`export`/`get` operate on the frozen messages.db post-split (2026-07-01, open)

The resreg YAML-manifest CLI (`cmd/arizuko/apply.go`) opens `store.Open(dataDir+"/store")`
= **messages.db** and drives `resreg.Apply`/`Plan`/`Export`/`ConfigVersion` against it. But
the config resources it manages (groups/acl/acl_membership/routes/web_routes/scheduled_tasks/
network_rules/proxyd_routes/onboarding_gates) all MOVED to routd.db/onbod.db in the split
cutover. So `apply` reads + writes a frozen twin: its CAS counter (`config_meta`) and every
resource DELETE+INSERT hit tables the live daemons no longer read. Effectively `arizuko apply`
is a no-op against production config. `config_meta` is entangled with this — it cannot move to
routd.db until the CLI is repointed at the owner DBs (a per-resource DB routing problem, since
resources now span routd.db + onbod.db). Deferred out of the messages.db-retirement slice.

The same CLI also contradicts 5/8's token exclusion: `resreg.Export` scans every
registered `RowType`, while `resreg/resources/invites.go` registers the raw invite
`Token` and does not set `SkipApplyRebuild`. Therefore today's frozen-DB export can
emit invite bearer tokens, and a naive owner-DB repoint would emit the live tokens.
The 5/8 finalization must exclude imperative token resources from export/apply,
not merely route them to the correct owner DB. `route_tokens` already skips apply
but is still exported as non-secret metadata; decide separately whether that
metadata belongs in a dump.

- **Severity:** medium
- **Scope:** cmd/arizuko resreg apply/plan/export/get, config_meta ownership
- **Affected:** all instances (YAML-manifest config management dead post-split)
- **Source:** cmd/arizuko/apply.go:45,94,128,204 (store.Open messages.db); resreg/engine.go:627-645 (exports every RowType); resreg/resources/invites.go:14-21,36-43 (raw token registered); store/migrations/0067-config-meta.sql
- **Status:** open

## dashd reads messages/chats/cost_log/task_run_logs from frozen messages.db (2026-07-01, open)

Several dashd read paths still query the pre-split messages.db handle (`d.db`) for tables that
moved to routd.db: `routd_page.go` reads `messages`/`chats` (errored + pending outbound counts,
and UPDATEs `messages SET errored=0`), `usage_page.go` reads `cost_log` via `GroupUsageBulk(d.db)`,
`chat.go` reads chat_sessions + messages. These render stale/empty because the live rows are in
routd.db now. Separate concern from the audit_log move (which this pass fixed by repointing
audit.Init + the audit reader to routd.db). These readers should be repointed to `d.adminDB()`
(routd.db) / `d.dbRuned` respectively; after that dashd could drop `store.Open(messages.db)`.

- **Severity:** medium
- **Scope:** dashd routd_page.go / usage_page.go / chat.go direct messages.db reads
- **Affected:** all instances (dashd status/usage/chat views show stale post-split data)
- **Source:** dashd/routd_page.go:50,64,126; dashd/usage_page.go:38; dashd/chat.go:138,369
- **Status:** PARTIAL (2026-07-05) — `routd_page.go` errored/pending counts + retry UPDATE
  repointed to routd.db (live). STILL OPEN: (1) `usage_page.go` GroupUsageBulk + volume — NOT
  a handle swap: cost_log has divergent schemas across messages.db (store 0049: `ts/cents/
  input_tok`) and routd.db (routd 0001: `recorded_at/cost_cents/input_tokens`); GroupUsageBulk's
  SQL matches the store schema, so it needs a query rewrite to read routd.db's live cost_log.
  (2) `chat.go` chat_sessions is dashd-owned in messages.db (no external writer) — moving it needs
  the mint-write path repointed too. Until both land, dashd still holds `store.Open(messages.db)`.

## Product PRODUCT.md manifests declare a non-existent `facts` skill (2026-07-09, OPEN)

9 of the `ant/examples/*/PRODUCT.md` manifests list `"facts"` in their `skills`
array (personal, reality, socials, support, creator, pm, slack-team, strategy,
trip), but there is no `ant/skills/facts/` — writing facts is done by `find` +
`recall-memories`. Either the seed path silently ignores unknown skill names
(then the manifest is misleading) or it errors/mis-seeds. Product doc pages
mirror the manifests, so the drift shows up in docs too.

- **Severity:** low
- **Scope:** `ant/examples/*/PRODUCT.md` skills field vs `ant/skills/`
- **Source:** grep — no `ant/skills/facts`; 9 PRODUCT.md declare it
- **Status:** OPEN — decide: rename to a real skill, add a `facts` skill, or drop from manifests
- **Found:** product-docs refine pass (fable), 2026-07-09

## Suffixed web chat token mints a JID that routes nowhere (2026-07-09, OPEN)

`arizuko token <inst> issue chat <folder> <suffix>` builds
`jid = "web:"+folder+"/"+suffix` (`cmd/arizuko/token.go:60`). A group's auto route
is `room=<folder>`, and direct-address resolution keys on the folder — a
`web:<folder>/<suffix>` JID appears to match neither, so a suffixed chat link may
be dead on arrival. The unsuffixed form (`web:<folder>`) works. If suffixed chat
tokens are meant to route (e.g. sub-surface chats), that's a routd gap; if not,
the CLI should reject the suffix for `chat`. Product setup pages were written to
avoid the suffix as a workaround.

- **Severity:** medium (silent dead link if an operator uses the documented suffix arg)
- **Scope:** `cmd/arizuko/token.go` chat JID + routd route/direct-address resolution
- **Source:** `cmd/arizuko/token.go:55-62`; route match `room=<folder>`
- **Status:** OPEN — needs a routing repro to confirm whether `web:X/sub` resolves
- **Found:** product-docs refine pass (fable), 2026-07-09

## howto/webhooks.html claims hook headers are delivered; ingest forwards body only (2026-07-09, OPEN)

`template/web/pub/arizuko/howto/webhooks.html` documents a `"headers": {…}`
envelope on hook ingest, but the route-token webhook path forwards the request
body only (per `webd/route_token.go` ~:268, cited by the refine agent — verify).
Product pages had the same claim removed; the howto page still carries it.

- **Severity:** low (doc-only; misleads webhook integrators)
- **Scope:** `template/web/pub/arizuko/howto/webhooks.html` vs `webd/route_token.go`
- **Status:** fixed 2026-07-12 — verified `webd/route_token.go:handleHookIngest`
  builds `InboundMsg{Content: body}` only (no headers field exists on the type);
  removed all four headers-delivery claims from the howto page, security bullet
  now states headers are dropped at ingest
- **Found:** product-docs refine pass (fable), 2026-07-09

## `arizuko token issue chat|webhook` writes route_tokens to frozen messages.db (2026-07-11, open)

Same class as the fixed `arizuko network` bug (2026-06-21, 2dfa5670): `cmd/arizuko/token.go`
`cmdToken` opens the DB via `store.Open(dir/store)` → `messages.db`, but post-split
`route_tokens` is owned by routd in `routd.db` (`routd/tokens.go` resolves against `d.db`).
CLI-issued chat/webhook tokens land in a table routd never reads — the printed `/chat/<token>`
or `/hook/<token>` URL silently never resolves (webd POST /v1/route_tokens/resolve → 404).
Verified live on krons: routd.db has 3 route_tokens, messages.db has 1 stale CLI-issued row.
Also breaks `token list` (reads stale rows) and the `issue` folder-existence check
(`GroupByFolder` on messages.db misses post-split groups, e.g. `eval`). The REST
(`POST /v1/route_tokens/chat`) and MCP (`issue_chat_link`) paths are correct — only the CLI
is wrong. Note `tokenIssueBearer` (added 2026-07-11) is unaffected: it reads auth.db +
validates the folder via `mustOpenACL` (routd.db).

Fix: route `issue chat|webhook`/`list`/`revoke` through `store.OpenRoutd` (mirror 2dfa5670).

- **Severity:** medium (silent — operator-issued capability URLs are dead; no error surfaced)
- **Scope:** cmd/arizuko/token.go
- **Affected:** `arizuko token issue chat|webhook|list|revoke` on all split instances
- **Source:** cmd/arizuko/token.go:27 (`store.Open` → should be `store.OpenRoutd` for route_tokens)
- **Status:** fixed 2026-07-12 — cmdToken opens routd.db via the existing
  `mustOpenACL` (one CLI open-path, no second `store.OpenRoutd` copy); guarded by
  `TestTokenLandsInRoutdDB` (issue+revoke land in routd.db, messages.db stays empty)
- **Fix:**

## `arizuko group add`/`create` writes the group skeleton to CWD, not the data dir (2026-07-11, open)

`core.LoadConfigFrom(dataDir)` loads the instance `.env` but resolves
`root = envOr("DATA_DIR", mustCwd())` (`core/config.go:157`). Instance `.env` files don't set
`DATA_DIR` (compose injects it per-container), so on the HOST the CLI's `cfg.GroupsDir`
becomes `<cwd>/groups` — `container.SetupGroup` writes the whole agent-home skeleton
(`.claude/`, skills, logs) into a stray `groups/<folder>` under whatever directory the
operator ran the command from. The DB rows still land correctly (routd.db), and routd/runed
re-provision the real `groups/<folder>` on first dispatch, which masks the bug. Observed
live 2026-07-11 creating the `eval` group on krons from a repo worktree: skeleton appeared
in `<worktree>/groups/eval` (root-owned), real dir appeared only when routd provisioned it.

Fix direction: host CLI paths should derive GroupsDir from the instance dir it was given
(`filepath.Join(dataDir, "groups")`), not from `DATA_DIR`-or-cwd. Cross-check other CLI
users of `LoadConfigFrom` (`cmdCreate`, `cmdPair`, `cmdStatus`) for the same cwd fallback.

- **Severity:** low (masked by daemon-side re-provisioning; litters cwd, confuses operators)
- **Scope:** cmd/arizuko/main.go + core/config.go LoadConfigFrom root resolution
- **Affected:** `arizuko create`, `arizuko group add` run on the host
- **Source:** core/config.go:157 (`envOr("DATA_DIR", mustCwd())`)
- **Status:** fixed 2026-07-12 — `LoadConfigFrom(dir)` defaults DATA_DIR to `dir`
  (explicit env/.env still wins), fixing all four callers at the one cause site
  (cross-checked: create/group add/status/secret all pass `mustInstanceDir`);
  daemons use `LoadConfig()` directly, unaffected. Tests:
  `TestLoadConfigFromDefaultsRootToDir` + `TestLoadConfigFromRespectsDataDirEnv`
- **Fix:**

## PROPOSAL — anteval `--mcp` parity face needs an inspect-read on a public MCP surface (2026-07-11, proposed)

Spec 5/9 gap (b): `anteval --mcp` expects an "inspect-compatible MCP-over-HTTP face" —
`HTTPTarget.McpMessages` just GETs `<mcp>/v1/messages/inspect`, i.e. a REST-shaped URL, not a
real MCP client. The platform DOES have MCP-over-HTTP (webd `POST /mcp` session-auth'd, and
`POST /chat/{token}/mcp` chat-token-auth'd, stateless streamable HTTP) but its tools are
`send_message`/`get_round` — get_round returns ASSISTANT frames for one turn, so the
`rest-mcp-parity` sentinel (harness-injected USER message identical via both faces) cannot be
read through it. Closing the gap honestly needs ONE of: (1) an inspect-read tool on the
chat-token MCP face (new public contract on webd, folder-bound like routd's REST inspect);
(2) a real streamable-HTTP MCP client in anteval driving send_message/get_round and a
reshaped parity case (assert the ROUND's frames match via REST + MCP instead of the raw
sentinel). Both change a public surface contract or the spec's case semantics → needs
sign-off before shipping. Until then `--mcp` stays unset and `rest-mcp-parity` fails loudly
("surface not configured") — honest, not silent; `mcp-roundtrip` is unaffected (agent-driven,
callback-checked).

- **Severity:** low (one non-smoke case ungated; documented honest gap)
- **Scope:** anteval/pkg/run/target.go McpMessages + webd chat MCP toolset + spec 5/9
- **Affected:** anteval `rest-mcp-parity` case
- **Source:** anteval/README.md "Two known gaps"; webd/chat_mcp.go toolset
- **Status:** proposed (redesign, needs sign-off)
- **Fix:**

## krons: `/hook/` proxyd route missing — webhook surface dead; route table not hot-reloaded (2026-07-11, open)

Found by anteval's `webhook-in` live case. `POST /hook/<token>` on krons falls through to the
public catch-all (302 → `/pub/krons/hook/<token>`) for EVERY token — the `proxyd_routes` table
(messages.db) had `/chat/` but no `/hook/` row. `compose.coreProxydRoutes` HAS declared
`{/hook/ → webd, public}` since e2d2b5df (2026-05-18), but proxyd seeds the table only when it
is EMPTY, so instances seeded before that date never gain new core routes — a seeded-once vs
evolving-defaults drift class (same class as MIGRATION_VERSION-style drift, no re-seed path).
Spec 5/W webhooks (`issue_webhook` → `POST /hook/<token>`) are silently dead on krons: the
agent mints a token, the POST 302s, no turn fires.

Second facet: the row was INSERTed live (2026-07-11) and a `/zzztest/` probe row confirmed the
running proxyd does NOT pick up proxyd_routes changes without a restart — despite
`proxyd/resource.go snapshot()` reading the DB per request (spec 5/8, d9796a62). Either the
deployed build predates the per-request read or something still caches; verify on next deploy
and re-run `anteval run ... --case webhook-in` (the `/hook/` row is already in place, so the
next proxyd restart activates it).

- **Severity:** medium (a documented public surface is dead on the flagship instance; silent)
- **Scope:** proxyd route seeding / krons data drift
- **Affected:** krons (and any instance seeded before 2026-05-18); spec 5/W `/hook` surface
- **Source:** anteval webhook-in live run 2026-07-11; proxyd request log `status:302 path:/hook/...`
- **Status:** open (row inserted on krons, awaits proxyd restart; re-seed/upsert path undesigned)
- **Fix:**

## Positioning claims "diff, fork, git revert" but nothing versions agent folders (2026-07-14, open)

specs/5/A (grand message, shipped) + specs/17/9 sell ownership as "files you diff,
fork, and `git revert` on your own host". Verified: the platform has NO `git init`
at group creation (onbod/container/cmd grep clean), no auto-commit; live homes show
.git in only 4/8 krons groups + marinade/atlas, and those repos have ZERO commits
(rhias: empty master, 86 dirty files) — agent/operator-initiated shells. So `git
revert` mostly has nothing to revert to. The true claims: plain files, tar = full
backup, git POSSIBLE (ant image ships git + commit skill).

- **Severity:** low (positioning honesty, no runtime impact)
- **Scope:** specs/5/A + 17/9 wording; landing/README ownership sections; the gap itself
- **Fix options:** (a) soften wording to possibility framing (landing sub already
  instructed, 2026-07-14); (b) make it TRUE: `git init` + initial commit at group
  create + a commit hook on /migrate + diary writes — small, but that IS
  specs/9/3-git-as-truth scope creep; decide there, not ad hoc.
- **Status:** open — wording fix in flight; the make-it-true decision belongs to 9/3

## Two hub.js in template — release stamps the one pages don't load (2026-07-14, open)

`template/web/pub/assets/hub.js` and `template/web/pub/arizuko/assets/hub.js` are
byte-identical twins except `ARIZUKO_VERSION`. The docs pages reference relative
`assets/hub.js` → the `arizuko/assets/` copy; the release runbook (root CLAUDE.md
"Tagging" step 3) bumps only the top-level copy. Live footer showed v0.51.0 for
seven releases (v0.51→v0.58) until caught at the v0.58.0 deploy. Same twin risk
for hub.css.

- **Severity:** low (cosmetic version stamp; but a textbook two-paths drift)
- **Scope:** template/web asset layout + release runbook
- **Fix options:** (a) collapse to ONE file (arizuko/assets is the loaded one;
  decide whether the top-level assets/ serves any non-arizuko hub page before
  deleting); (b) release script bumps both (band-aid, applied 2026-07-14 in
  CLAUDE.md wording).
- **Status:** open — versions synced at v0.58.0; collapse decision pending
