# bugs.md

Open-issues queue. Resolved entries are moved to `.diary/` — see e.g.
`.diary/20260525.md` for the most recent cleanup. New finds: record
date + scope + severity + suspected fix-path; don't auto-fix during
general audits (CLAUDE.md bug-triage protocol). Workflow: `/bugs` skill.

## FIXED 2026-06-01 — gated wiped all secrets on every startup (was HIGH live data-loss)

Fixed with the M7 encryption-at-rest impl: removed the `PurgeUnencryptedSecrets`
DELETE (replaced by an idempotent encrypt-in-place migrate), dropped the
`AUTH_SECRET` fallback so the purge no longer ran with an always-set key. Secrets
are now plaintext by default, AES-256-GCM when `SECRETS_KEY` is set. Tests in
`store/secrets_test.go`. Original report below for the record.

Found during the docs persona-audit (CTO lens), confirmed against code.

`SetSecret` (store/secrets.go:63-69) stores the value as **raw plaintext, no
`v1:` prefix** (encryption was reverted in migration 0047 — "v1 stores
plaintext"). But `PurgeUnencryptedSecrets` (secrets.go:181-185) runs
`DELETE FROM secrets WHERE value NOT LIKE 'v1:%'`, and gated calls it on EVERY
startup: `gated/main.go:54-64` sets `encKey = SecretsKey || AuthSecret`
(AUTH_SECRET is always set) → `SetSecretKey` → `PurgeUnencryptedSecrets`. Since
no row carries a `v1:` prefix, **every plaintext secret is deleted on each gated
restart.** Any folder/user secret an operator sets via `arizuko secret` or
`/dash/me/secrets` is gone on the next restart.

Live check 2026-06-01: krons/marinade/sloth all have 0 rows in `secrets` —
consistent with the wipe (can't distinguish from "feature unused", since the
broker that consumes secrets isn't wired yet). Not in the split path (routd/runed
never call the purge). The purge is leftover scaffolding from the reverted
encryption design (it was meant to drop old plaintext AFTER encrypting).

Fix (design choice — flagged, not auto-fixed): either drop the
`SetSecretKey`+`PurgeUnencryptedSecrets` block from `gated/main.go` (plaintext is
the v1 model — nothing to purge), OR re-introduce the `v1:` prefix on
`SetSecret` writes so the purge only removes genuinely-old rows. Docs corrected
this session (security/index.html + concepts/secrets.html claimed AES-256-GCM
"shipped"; now state plaintext/deferred).

## atlas-on-Slack thread/self-reply analysis (2026-06-02)

User: atlas doesn't always "attend" to threads, and replies to its own messages
(and forwards of them). Traced the mechanism end to end.

### Self-reply (the concrete bug) — own-filter is adapter-only + fragile
The ONLY guard against atlas processing its own Slack message is slakd's
`BotUserID` check (`slakd/bot.go:357-362`), where `BotUserID()` is set from
`auth.test` (`bot.go:202-206`). marinade's Slack token is **user-mode +
rotating** (`.env`: "will need refresh after ~12h"). When it rotates and
`auth.test` fails, `BotUserID()` goes stale/empty → with an empty id the filter
`m.User == ""` no longer matches a real own-message (which carries the actual
user id) → atlas's own message passes through. There is NO defense-in-depth: the
gateway/api promotion has no `is_bot`/own-sender guard, and the **5/L promotion**
(`api/api.go:303`, `IsBotMessageByID` matches `id` OR `platform_id`,
`store/messages.go:575`) ACTIVELY re-promotes that own message to `verb=mention`
(its thread_ts resolves to atlas's own root) → atlas replies to itself. Symptom
is intermittent — correlates with token-rotation windows.

Data check (2026-06-02): ZERO inbound rows carry atlas's own user id
(U0B47GYRMLZ) — the identity own-filter works in steady state, AND botUserID is
set once at start() and never cleared, so it doesn't actually go empty mid-run.
So the active self-reply isn't reproduced in the recorded window; the exposure is
the edges (a wrong botUserID at start, an unusual own-message shape, the echo
arriving author-less).

FIXED (slakd dedup-own-TS): `Send` marks each posted TS (`markSent`); inbound
drops any TS we sent (`isOwnEcho`), pruned in the health probe. Independent of
identity, so a stale/wrong botUserID or token-mode quirk can't make an own post a
self-reply. Test `TestInbound_OwnEchoSkippedByTS`. (Did NOT fail-closed on empty
botUserID — that would drop real user messages during a token gap; the TS dedup
is the targeted guard.) NOT fixed: a human FORWARD of atlas's content is a
genuine human message (new TS, human author) — suppressing it is ambiguous
intent, not clearly fixable.

### Forwards of own messages — same class
A human forwarding/quoting atlas's message is authored by the human (is_bot=false,
not caught by the own-filter), but if it carries atlas's message id as the
reply/thread reference, `IsBotMessageByID(ReplyTo)` matches → promoted → atlas
re-responds to its own content. Same defense needed: don't promote when the
referenced/echoed content is the bot's own AND the new message adds no question,
or suppress reply-to-bot promotion for forward/share subtypes.

### Thread attendance — mostly by design, one candidate gap
The promotion works for threads ROOTED at atlas's own messages (IsBotMessageByID
matches platform_id). Threads rooted at a HUMAN message → atlas attends only via
explicit `@mention` or active engagement (5/G). So "doesn't always attend" is
largely by-design: atlas auto-attends threads it started, not arbitrary ones. To
attend all thread replies in a channel, add a route, not a code fix. CANDIDATE
real gap: if atlas's message was delivered via the gateway turn-result path
(not the `reply` tool — happens when the reply tool is authz-blocked, see the
atlas/support chat-send-forbidden entry below) and that path doesn't store
platform_id+BotMsg, then thread replies to it never promote → silently missed.
Verify the turn-result delivery records platform_id with is_bot=1.

## atlas-on-Slack live trace (2026-06-01) — 2 HIGH bugs

Traced marinade/atlas Slack behavior on the user's request. Two real bugs.

### FIXED 2026-06-03 (commit 2ba0f5e1) — mention-only sub-folder agents can't reply (send authz resolved to parent)

Fixed exactly as the analysis below prescribed: added `router.RouteMatchesIgnoreVerb`
+ `store.JIDRoutableToFolder` (verb-agnostic "is `id.Folder` or a descendant a route
target for `jid` under ANY verb?"), and `authorizeJID` falls back to it when the
default `DefaultFolderForJID` resolution denies. Routing/observe untouched — the
route-seq workaround was REJECTED because it collapses the parent's observe stream
(the operator wants sub-folders to keep observing). Fixes ALL mention-only
sub-folders at once (atlas/support, atlas/content, atlas/search). Tests: authorizeJID
mention-only allow + sibling-deny regression; RouteMatchesIgnoreVerb; JIDRoutableToFolder.

### HIGH — mention-only sub-folder agents can't reply (send authz resolves to parent)

`atlas/support` is routed mention-only on `slack:T4PNSRSP7/channel/C0B4JMQ8X89`
(route id 18: `verb=mention → atlas/support`, seq 10) with a wildcard observe
catch-all (route id 14: `slack:*/channel/* → atlas#observe`, seq 100) and a
specific observe (route id 17: `…C0B4… → atlas/support#observe`, seq **110**).

When `atlas/support`'s agent calls `reply`/`send`, `authorizeJID`
(`ipc/ipc.go:603`) authorizes via `DefaultFolderForJID` ONLY. That fn
(`store/groups.go:188`) resolves with a synthetic `Verb:"message"` — so the
`verb=mention` route is skipped and the wildcard (seq 100) beats the specific
observe (seq 110) → `DefaultFolderForJID(C0B4) = "atlas"` (the PARENT).
`AuthorizeStructural(atlas/support, …, target=atlas)` then denies (child not in
parent's subtree) → `forbidden: chat … belongs to folder atlas, not in subtree
of atlas/support`. Observed live 2026-06-01 13:43:20 — the agent computed the
answer, couldn't deliver via `reply`, saved it to `~/tmp/pending-reply-*.md`,
and self-filed `chat-send-forbidden`. (The gateway's turn-RESULT auto-delivery
still gets the final text out, so messages DO appear — masking the tool failure;
inconsistent.)

Fix — NOT trivial (initial idea was wrong): `JIDRoutedToFolder` does NOT help —
it also calls `DefaultFolderForJID` (`store/groups.go:188`), so
`JIDRoutedToFolder(C0B4,"atlas/support")` is false (default resolves to the
parent `atlas`). A correct fix needs a NEW verb-agnostic resolver: "is
`id.Folder` (or an ancestor) a target of ANY route matching `jid`, ignoring the
verb condition?" — `RouteMatches` checks verb, so it needs a verb-skipping
variant + an authz-broadening + tests. Design it; don't rush. Operator
workaround (per channel, isolated, semantically OK — support-channel ambient →
support agent): lower the specific observe route's seq below the wildcard's
(e.g. route 17 `…C0B4…→atlas/support#observe` 110 → <100). NOTE: other
mention-only subfolders (atlas/content on C0B63J2RBJ8, etc.) have the same gap;
the code fix is the general solution, route-seq surgery is per-channel.

### FIXED 2026-06-03 (commit 2ba0f5e1) — reply-tool sends break thread re-attendance (empty platform_id)

`recordOutbound` (`ipc/ipc.go`) stored the sent message's own platform id in
`reply_to_id` and left `platform_id` empty, violating the contract documented at
`store/messages.go` IsBotMessageByID (matches `id OR platform_id`, never
`reply_to_id`). So a human reply to a reply-tool (`mcp-*`) message never matched →
the reply-to-bot → `verb=mention` promotion (spec 6/J, `api/api.go:304`) missed →
atlas silently dropped threads it had answered via the `reply` tool. Live evidence:
100% of `mcp-*` rows have empty platform_id (atlas 442/442, atlas/support 67/67).
The gateway turn-result (`out-*`) path was already correct via MarkMessageDelivered.
Fixed: store the sent TS in `platform_id`. Test: recordOutbound platform_id +
PutMessage→IsBotMessageByID promotability. (The earlier eval mistook this for "100%
expected" — it is the bug, not a baseline.)

## atlas-on-Slack full-group eval (2026-06-03) — triage queue (NOT yet fixed)

Swept all atlas groups on marinade. The two platform CODE bugs above are fixed +
deployed. The rest are agent-behavior / operator-data / external — logged here for
the operator to prioritise, NOT auto-fixed (triage protocol):

- **Slack mrkdwn dialect** (atlas, atlas/support): agent emits `**bold**`
  (CommonMark) not `*bold*`; `[t](url)` not `<url|t>`. Agent output-style concern
  (slakd does not server-side convert like teled). Recurring since 2026-05-26.
- **`/resolve` + `/recall-memories` skip on session start / context loss** (atlas,
  atlas/content): subagents jump to execution; resolve doesn't semantic-search on
  lost context. Skill-discipline (ant/skills), not platform.
- **eyes 👀 reaction not cleared after processing** (atlas, 2026-06-03): slakd
  reaction lifecycle. Minor, non-blocking. Verify before fixing.
- **web access method wrong in settings.json** (atlas, 2026-06-03): operator data
  (the group's own settings), not platform code.
- **Telegram 4096-char overflow / no split** (atlas/content): agent message-length
  awareness. Agent-side.
- **historical Slack/web 502s** (2026-05-15/20/25): not in last 48h; transient infra.
- **SAM delegation priority inverted**: EXTERNAL (Marinade off-chain bot), not arizuko.
- 11 `failed` rows = stale `atlas/tom` (removed 2026-05-25) → telegram. Not actionable.

### FIXED 2026-06-01 — cpDirOverwrite can't overwrite root-owned output-style files (regression from 9a2a181a)

The v0.49.0 `seedOutputStyles` change `cpDirFresh`→`cpDirOverwrite` (commit
9a2a181a) truncate-opened each dest file. Output-style files left **root:root**
by a 2026-05-21 sudo op (150 across marinade/krons/sloth) in a uid-1000 dir →
gated (uid 1000) `cpDir: write failed … permission denied` on EVERY spawn → those
styles stayed frozen, the exact staleness the fix was meant to cure.

FIXED (code, **5fb685b9**): `cpDirImpl` now `os.Remove(dst)` before WriteFile
(the dir is uid-1000-owned, so unlink succeeds even on root-owned files) +
regression test feeding a 0444 dst. Operator remedy APPLIED 2026-06-01: chowned
all 150 root-owned files (marinade 51, krons 47, sloth 52) to 1000:1000, so live
seeding works on the next spawn without waiting for the code redeploy.

### LOW / noted
- 1 errored slack turn (2026-06-01 07:22 mention) — container exited code 0 at
  ~9.9 min, `timedOut:false`; cause unclear, single occurrence.
- Not bugs: engagement override `C4P090XU1 → atlas` (by design, spec 5/G); docs
  asset cache served a stale `hub.js` to one curl (on-disk correct).

## 10-sub bug hunt (2026-06-01) — Workflow wf_e4e8d372-820 + auth re-run

10 read-only finders across code+docs; high-severity findings adversarially
verified. 34 findings + the auth bucket re-run (its finder crashed mid-workflow).
LIVE/dormant findings below are FINDER-confidence only (just the 3 HIGH were
refute-checked) — VERIFY before fixing; some overlap older entries.

### FIXED this session
- **dashd memory write/delete had NO auth gate (HIGH security)** — `handleMemoryWrite`/
  `handleMemoryDelete` (dashd/main.go:797,839) wrote/deleted any tenant's
  MEMORY.md/PERSONA.md/CLAUDE.md with no `requireAdmin` and no CSRF guard; any
  logged-in user could cross-tenant overwrite/delete (verified — tests passed with
  no X-User-Sub). FIXED: `requireAdmin(folder)` on both + regression test (unauth → 401).
- **migrate release-broadcast wrong param (HIGH)** — ant/skills/migrate/SKILL.md:279
  `send jid:=` but the `send` tool requires `chatJid` (ipc.go:869) → every release
  announce silently failed. FIXED: `jid:=`→`chatJid:=`.

### Confirmed-real LIVE, not fixed (finder-confidence high — triage)
- **store MatchWebRoute LIKE-injection** (store/web_routes.go:138) — agent-set
  path_prefix `_`/`%` act as SQL LIKE wildcards → over-broad web-route match. Escape/exact.
- **slakd reaction sentiment always "like"** (slakd/bot.go:455) — Slack emoji
  short-names never match the like/dislike classifier; all reactions read as like.
- **dashd-created scheduled tasks never fire** (dashd/tasks_admin.go:206) — next_run
  left NULL; timed's claim query excludes NULL → never runs.
- **linkd fetch_history broken for posts** (linkd/client.go:706) — `linkedin:post/`
  prefix not stripped before the API call.
- **gateway always-true `!out.HadOutput` guard** (gateway.go:1354) — deletes the
  session on errored turns (dead-guard).
- **mastd favourite/reblog omit ReplyTo** (mastd/client.go:203) — breaks
  reply-to-bot→mention promotion. **reditd no self-author filter** (reditd/client.go:380)
  — bot's own posts re-ingested (loop risk).
- **webd /api/groups + /x/groups + dashd read handlers leak all folder names**
  (webd/api.go:13, dashd/groups_admin.go:190) unfiltered by caller grant (overlaps a
  2026-05-28 webd entry).

### Dormant split (cutover-only, no live impact)
- **authd refresh consumes the token BEFORE the grants snapshot** (authd/server.go:301
  vs 315) — a transient grants-backend blip during refresh marks the token spent with
  no successor → next use = reuse → family revoked → user force-logged-out. Fix:
  snapshot grants before `markRefreshUsed`. (medium; auth re-finder.)
- routd handleEngagementSet/handleCost write caller folder with no ownsFolder check
  (reads_http.go:165,207); del_web_route tier-0 deletes zero rows (tokens.go:124).
- authd loadServiceSecrets keyed by secret → dup secret silently drops a principal
  (main.go:141); OAuth within-account silent rebind (store.go:120) (low).
- runed session_log never swept (db.go:243); egress networks never removed
  (container/network.go:97); handleRunStatus maps all errors→404 (server.go:97) (low).

### Low (live, latent/cosmetic)
- gateway TTS `.tmp` shared-path race (tts.go:94); voice-len counts bytes not chars
  (tts.go:139). chanreg FetchHistory raw JID in URL unencoded (httpchan.go:258).
- ipc issue_chat_link tier desc drift (ipc.go:2163); inject_message tier-1 cross-world
  (ipc.go:1605). whapd inbound reaction no fromMe guard (main.ts:340); onbod admit
  ticker stalls if poll <1s (onbod/main.go:145); timed bad-cron hot-loops (main.go:194).
- docs: ARCHITECTURE.md:229 self-contradiction on MCP-socket host; README:82 stale
  `tg:` JID prefix; README:182 upstream attribution mismatch.

### Refuted (adversarial verify)
- `$ARIZUKO_MCP_SOCKET` "never set" → REFUTED: set via ant/Dockerfile:169 ENV.

## RESOLVED 2026-05-29 (this session — detail in `.diary/20260528.md`/`20260529.md`)

These entries below are now FIXED; superseding their stale open-entries:
store `ObservedSince` arg-order (`fc417d49`); auth pane/pin/inspect_tasks
over-deny (`f90b1618`); container `resetIdle` race + egress nil-guard
(`f87f4035`); webd SSE `round_done` turn-filter (`a80153b4`); `timed`
non-UTC `next_run` (`5f879893`); `teled` dup-send (`dbc43fc6`); `reditd`
cursor-loss (`57076082`); `linkd` 401-empty-body (`2bfd67ba`) + dedup-
before-deliver (`bcdb0ce7`); `twitd` jid-prefix (`48012efc`); `compose`
proxyd depends_on (`b3f0dc46`); `audit` webhook flush (`8708e3ee`);
gateway turn-state race (`8b1bb420`); queue `activeCount` race
(`edfc2894`) + steer-starvation (`3638728c`); store `FlushSysMsgs`
swallow (`4f208cdd`); onbod daily-cap bypass (`149616e8`); chanlib
`RouterClient.token` race (`b7d3b32d`); dashd nested-folder routing +
ipc `set_web_route` path-claim + cmd/apply version-print + auth
`get_routes`→`list_routes` (`e8a87278`); STORE_DIR + typed-JID doc drift
+ 6 broken spec links (`90ed5675`); proxyd no-op proxy cache
(`29853f08`). Residuals (audit idle-tail flush; grants tier-1/2
pin/pane default; tier-formula collision) remain open below.

## RESOLVED 2026-05-30 (bug-fix wave — research+fix subs, each with tests)

- **authd auth surface** (cutover step 1, additive): `/v1/tokens`,
  `/v1/service-token` (Authorization header), `/auth/*` OAuth ported into
  authd, `auth/service.go` TokenSource bootstrap (`fea768e4`). Live HS256
  path untouched; the flip stays USER-GATED.
- **reditd delete-cap** omission (`82d96380`) — advertise `delete` cap.
- **twitd snowflake lexical compare** (`82d96380`) — BigInt cursor compare.
- **emaid FetchHistory empty Verb** (`82d96380`) — set trust Verb from the
  same classifier as the live poller.
- **bskyd AT-URI** (`6b216907`) — inbound ID is now the full `at://` URI so
  the agent's default reply/like/delete resolve; `Send` returns the post URI.
- **ipc feed verbs skip recordOutbound** (`6d1b9446`) — `quote`/`repost` now
  record (own-feed content); `forward` stays an unrecorded relay by design.
- **ipc list_acl tier guard** (`6d1b9446`) — registered for tier ≤1 to match
  `AuthorizeStructural` enforcement.
- **linkd seen-before-deliver** — was ALREADY fixed (`bcdb0ce7`); the
  sweep-section entry below was a double-log. No action.
- **ipc post/delete "Tier 0-2" descriptions** — confirmed ACCURATE (describe
  the default grant matrix, not a hard tier gate; tier-3 defaults exclude
  post/delete). Not stale, no enforcement gap. No change.

New find (open, low): **`invite_create` desc/enforcement mismatch** — desc
(`ipc/ipc.go` ~2087) says "Tier 0-2 only" but `AuthorizeStructural` denies
`tier >= 2` (tier 0-1 only). Same shape as the fixed list_acl case; doc-fix
follow-up.

## RESOLVED 2026-06-01 (verified already-fixed during /fin bug triage)

Triage of the offered "clean fix batch" found 9 of 10 entries + the onbod
daily-cap were ALREADY fixed in prior fix-waves but never groomed from this
queue. Verified against current code; these supersede their stale open entries
below:

- **`invite_create` desc** (the open find just above) — FIXED 2026-06-01: desc
  "Tier 0-2 only" → "Tier 0-1 only" (`ipc/ipc.go`), matching `AuthorizeStructural`
  (`auth/policy.go:126` denies tier ≥ 2).
- **proxyd `snapshot()` err-swallow** — FIXED: `proxyd/resource.go:66-70` logs
  `slog.Error` + returns nil,nil (visible outage, not silent 404).
- **gateway `transcribeOnce` body leak** — FIXED: `defer resp.Body.Close()`
  (gateway.go:1863) precedes the non-200 early return.
- **gateway `downloadFile` silent truncate** — FIXED: `LimitReader(…, maxBytes+1)`
  trips `n > maxBytes` → error + `os.Remove(dest)` (gateway.go:1813-1820).
- **teled empty-photo panic** — FIXED: `case len(msg.Photo) > 0` guard
  (teled/bot.go:711) before the `[len-1]` index.
- **teled `/health` lies** — FIXED: `connected` is `atomic.Bool`, set false on
  poll failure (teled/bot.go:36,153).
- **teled `b.cancel` race** — NOT A RACE: the field has no writer (only the read
  at bot.go:231); vestigial dead field. Minor dead-code cleanup if ever touched.
- **teled cap-map drift** — already correct: quote/repost/dislike/fetch_history
  intentionally NOT advertised (teled/main.go:11 comment); `unpin` is gated on the
  `"pin"` cap (chanreg/httpchan.go:335) which teled advertises + implements.
- **chanlib `RouterClient.token` race** — FIXED: token read/written under `regMu`
  via a getter (chanlib/chanlib.go:85-93).
- **reditd missing `delete` cap** — FIXED: `"delete": true` present (reditd/main.go:19).
- **dashd nested-folder routing** — FIXED: settings/delete/POST use `{folder...}`
  trailing wildcard (dashd/main.go:167,205,206). (Residual: `{folder}/grants` and
  `{folder}/tools` still single-segment — nested-folder grants/tools pages, not in
  the original 2026-05-16 report; separate latent item.)
- **onbod cross-day daily-cap bypass** — FIXED: migration 0071 added `admitted_at`;
  `admitFromQueue` counts approvals by `admitted_at` day-range (onbod/main.go:693-694),
  admission stamps it (main.go:626,719).

Also resolved this session: eval skill drift (chats.errored → messages.errored per
migration 0030; `PRAGMA user_version` → `migrations` table) fixed in
`.claude/skills/eval/SKILL.md`.

## routd has NO production Deliverer + NO channel-registration surface (2026-05-30, CUTOVER FLIP-BLOCKER, high)

Found during the gated→routd compose cutover flip (compose-gen wiring task).
routd cannot deliver outbound to adapters and adapters cannot register:

- `routd/cmd/routd/main.go:88` wires `routd.NewServer(db, loop, nil, ...)` —
  the `Deliverer` is `nil` in production. The only `Deliverer` implementations
  in the tree are test fakes (`parity_test.go`, `auth_test.go`, etc.). So
  `POST /v1/turns/{id}/send`, `/v1/outbound`, the outbound-retry loop, and the
  run-error failure-notice all hit the `s.deliver == nil` → `502 no_channel`
  path. The agent's replies never reach the platform.
- routd's mux (`routd/server.go` `Handler`) has NO `/v1/channels/register`,
  `/v1/channels/deregister`, or `GET /v1/channels`. Adapters register via
  `chanlib.RegisterChannel` → `POST /v1/channels/register` against `ROUTER_URL`
  (`chanlib/chanlib.go:103`); against routd that 404s, so no adapter is ever
  known to the deliverer. `arizuko status` also reads `GET /v1/channels`
  (`cmd/arizuko/main.go:811`) → would 404.

In the gated topology this is `gated/main.go`'s `apiSrv` + `chanreg` (the
`OnRegister`/`ChannelLookup` wiring at `gated/main.go:79-93`). The routd build
never ported channel registration + the jid-prefix→adapter delivery resolution
the comment on `Deliverer` (`routd/server.go:18` "Production resolves the
adapter by jid prefix") promises. This is a routd parity gap, NOT fixable by
compose wiring — the FLIP re-points `ROUTER_URL` to `routd:8080` correctly, but
routd needs a production `Deliverer` (chanreg-backed) + the `/v1/channels/*`
endpoints wired in `routd/cmd/routd/main.go` before the flip can carry live
traffic. Suspected fix: port `chanreg.New` + `apiSrv.OnRegister/ChannelLookup`
+ the HTTP `/v1/channels/*` handlers into routd, and construct the real
`Deliverer` (jid-prefix → registered HTTPChannel) instead of `nil`.

## runed StoreFns federation — deferred tools (2026-05-30, GAP 1.1 closure)

GAP 1.1 wired the agent's read/manage MCP tool surface in the split:
`runed/runtimes.go` now builds `container.Input.StoreFns` (federated to
routd / runed-local / from-RunSpec). The StoreFns BELOW are intentionally
left nil — their post-split backend daemon/tables do not exist yet, so a
federation would be a forward to a 404. The owning tool no-ops (nil-guard
returns "not configured"), which is correct until the backend lands:

- **Tasks** — `CreateTask`, `GetTask`, `UpdateTaskStatus`, `DeleteTask`,
  `ListTasks`, `TaskRunLogs`. `timed` has NO `/v1/tasks` HTTP surface
  (only `/health` + `/openapi.json`) and still reads `scheduled_tasks`
  from the legacy `messages.db`. Tools dark in the split: `schedule_task`,
  `list_tasks`, `pause_task`, `resume_task`, `cancel_task`, task-run-logs.
  Fix path: `timed` owns `scheduled_tasks`/`task_run_logs` + a `/v1/tasks`
  CRUD surface; then add a runed StoreFns federating to it (spec P-runed
  federation table: "list_tasks/pause_task/scheduled-task ops → timed").
  This is a scheduler-surface concern, not cutover plumbing.
- **ACL** — `ListACL` (the `list_acl` tool). No `acl`/`acl_membership`
  tables in any split daemon yet (routd's openapi CLAIMS ownership but the
  tables/handlers are unported). Authz itself is NOT broken: the MCP gate
  already verifies the brokered token's scope+folder before any tool runs,
  so `Authorize` is intentionally NOT federated (token scope is the live
  authz source). `ListACL` is read-only introspection; defer until acl
  tables land in routd.
- **GetIdentityForSub** — the `identity` tool. No `identities` table in
  the split. The verified Subject (sub/folder/scope) is available at the
  gate, but a general sub→identity lookup needs the table. Defer.
- **LogIPCAudit** — no audit table/owner in the split daemons
  (`audit_log` lived in gated's store). audit is observability, not
  cutover-critical; the `obs`/OTLP path still captures slog events. Defer
  until an audit owner is decided.

WIRED now (grounded in real routd/runed APIs): message reads
(`MessagesBefore`/`MessagesByThread`/`FindMessages` → new routd
`/v1/messages/{inspect,thread,find}`), routes CRUD
(`ListRoutes`/`SetRoutes`/`AddRoute`/`DeleteRoute`/`GetRoute` → existing
`/v1/routes`), web routes (`ListWebRoutes`/`SetWebRoute`/`DelWebRoute`/
`WebRouteOwner` → existing `/v1/web_routes` + new owner query), routing
resolution + engagement (`DefaultFolderForJID`/`JIDRoutedToFolder`/
`EngagedFolder`/`SetEngagement`/`GetLastReplyID`/`ErroredChats` → new
routd `/v1/routing/*` + `/v1/engagement`), cost (`LogExternalCost` →
routd `/v1/cost`), and runed-local (`RecentSessions`/`GetSession` →
runed.db) / from-RunSpec (`CurrentTriggerSender`/`CurrentTopic`).
`PutMessage`/`SetLastReply`/`BumpEngagement` stay nil — routd owns the
write-side already inside `/v1/turns/*`; they are not agent-callable.

## container/ipc bug-hunt sweep (2026-05-28)

Read-only correctness pass over `container/` + `ipc/`. Items below are UNSURE-whether-bug (logged, not fixed per triage protocol). Verified-not-bugs at the end.

- [ipc] ipc.go:1108-1116 (regSocial) + 1150/1167/1180 — `forward`, `quote`, `repost` (idOut=true, create new platform content with an id) do NOT call `recordOutbound`, so no `messages` row, no `SetLastReply`, no engagement bump. `post` (line 1065) and `send`/`reply`/`send_voice` all DO record. Likely intentional (feed amplification ≠ conversation reply) but the asymmetry vs `post` looks like an oversight. If feed actions should appear in the store/threading, route them through recordOutbound.
- [ipc] ipc.go:1024 (post desc) + 1133 (delete desc) — descriptions say "Tier 0-2 only" but `regSocial`/`post` apply NO tier gate; `authorizeOutbound` (auth/policy.go:128) only checks subtree, not tier. A tier-3 agent with the grant and JID ownership can call `post`/`delete`. Either the desc is stale or a tier check is missing. Same desc-vs-enforcement gap for `list_acl` ("Tier 0-2 only" at ipc.go:2045 but AuthorizeStructural denies tier 2). Safe direction (deny) for list_acl; post/delete is the permissive direction — verify intent.
- [container] egress.go:42,98 — `EgressConfig.active()` checks AdminURL/NetworkPrefix/CrackboxContainer but NOT `AllowlistFn`; `registerEgress` calls `cfg.AllowlistFn(id)` unconditionally (line 98). Current sole caller (gateway.go:1290) always sets it to `store.ResolveAllowlist`, so unreachable today, but a future active-config with nil AllowlistFn panics. Latent.
- [container] runner.go:257,267,347 — `resetIdle` func var is read by the stderr goroutine (line 267) and reassigned by the main goroutine (line 347) with no synchronization → data race on the func value (would trip `-race`). Pre-existing. Early resets (before line 347) are also silently dropped, but harmless (idle timer not created until line 344). `timer.Reset` itself is concurrency-safe.

Verified NOT bugs (do not re-flag): `get_thread` oldest=`msgs[len-1]` (ipc.go:2411) is correct — `MessagesByThread` returns DESC (newest first), while `inspect_messages`/`fetch_history` use `msgs[0]` because `MessagesBefore` reverses to ASC (store/messages.go:646). Connector `nil` secrets at ipc.go:868 is intentional ("broker removed", connector_test.go:151). `find_messages` `hits[:0]` in-place compaction is the safe Go idiom. `hp()` prefix check (runner.go:658) can't hit the sibling-prefix escape because all callers pass `ProjectRoot + "/" + subdir`. `inject_message` passing a jid as TargetFolder (ipc.go:1601) is harmless — that AuthorizeStructural case ignores TargetFolder.

## Typed-JID docs/spec drift (2026-05-28, low)

`core/jid.go` (typed `JID`/`ChatJID`/`UserJID` structs + `ParseJID`/
`MatchJID`) was deleted in a `[scrub]` pass — shipped per
`specs/5/S-jid-format.md` but never adopted; production uses string
`core.JidPlatform`/`core.JidRoom` (types.go) and `path.Match` directly,
and `specs/5/U-genericization.md` (partial) moves `ChatJID` toward a
string alias. Stale references remain and now describe code that no
longer exists:
- `template/web/pub/concepts/jid.html:45` — claims "`core.ChatJID` and
  `core.UserJID` are different types... the compiler refuses [a swap]".
  Was already false (prod funcs take `string`); now also dangling.
- `template/web/pub/reference/jid.html` — deep-links to `core/jid.go`
  line numbers (#L28/#L73/#L252) for `ParseJID`/`validateKind`/`MatchJID`.
- `specs/5/index.md:59` + `specs/5/S-jid-format.md` (status:shipped) —
  describe typed structs as the implementation.
- `specs/6/00-finalise-plan.md:82` lists `core/jid.go` in a file set.
Fix path: rewrite the two web-doc pages to describe the string JID +
`JidPlatform`/`JidRoom` reality, reconcile S-jid-format with the
genericization direction, drop the file-list entry. Operator-facing
docs + spec reconciliation = separate concern from the code scrub.

## STORE_DIR env-var doc drift (2026-05-28, low)

`template/web/pub/components/gated.html` documents a `STORE_DIR` env
var ("gated opens `$STORE_DIR/messages.db`") that gated never reads.
gated derives the store dir from `DATA_DIR`
(`core/config.go:164` → `filepath.Join(root, "store")`). Fix path:
update the doc to reference `DATA_DIR`, or drop the `STORE_DIR` mention.

## SendFile gaps (platforms with native media but no implementation)

- **mastd**: `NoFileSender` returns `errSendFile`. Mastodon has the v2
  media API (`POST /api/v2/media` + attach via `media_ids` on toot).
  Wire it to dispatch `.jpg/.png/.gif/.webp` → image, `.mp4/.webm`
  → video, `.mp3/.ogg` → audio. Documents/PDFs aren't natively
  supported — return Unsupported pointing the agent at a URL in toot
  text.
- **reditd**: `NoFileSender`. Reddit's image upload is a 3-step flow
  (websocket lease → S3 PUT → submit with `kind:image`). Spec out
  before implementing; for now `Unsupported(send_file, ...)` would be
  more honest than the generic errSendFile.
- **linkd**: `NoFileSender`. LinkedIn UGC media upload is a
  `/assets registerUpload` + binary PUT flow. Linkedin's
  `Post` for posts already returns Unsupported with a hint; SendFile
  inherits the generic errSendFile — convert to a structured Unsupported.
- **emaid**: explicitly returns `Unsupported(send_file, "MIME
  attachments not implemented; inline the content in send body.")`.
  Email is the universal medium for attachments; this is a future
  enhancement, not a wrong-dispatch bug.

## Adapter cap maps — drift

- 2026-05-28 (reditd, low): `reditd/client.go` implements a working
  `Delete()` (own posts/comments via Reddit API), but `reditd/main.go`'s
  `Caps` map omits `"delete": true`. `chanreg.HTTPChannel.Delete` gates
  on `HasCap("delete")`, so the `delete` MCP verb returns
  `UnsupportedError` for reddit despite the impl existing. Fix: add
  `"delete": true` to reditd's cap map + a `delete` round-trip test.
  Spec 5/Z coverage table marks reditd `delete ✗` to match shipped
  behavior. Behavior change → not a refine-pass edit.

- 2026-05-28 (grants, low): pin verbs (`pin_message`/`unpin_message`/
  `unpin_all`) are absent from `grants/grants.go` `platformActions`, so
  only tier-0 (`*`) holds them by default; tier-1/2 need an explicit
  grant. If pin should be a tier-1/2 default (like send/reply), add the
  three verbs to `platformActions`. Product decision, not a bug —
  flagged by the 5/Z refine pass; 5/Z spec documents the shipped behavior.

## reditd/emaid/twitd/linkd bug-hunt sweep (2026-05-28)

- 2026-05-28 (twitd, low): `twitd/src/main.ts:106-107` compares Twitter
  snowflake IDs with string `<=` / `>` (`id <= state.mentions`,
  `id > next.mentions`). Lexical comparison only matches numeric ordering
  while all IDs are the same length (currently 19 digits). If X ever issues
  shorter/longer IDs in the same batch, the cursor would skip or re-fetch.
  Not currently triggering — snowflakes are fixed-width. Latent.
- 2026-05-28 (emaid, low): `emaid/server.go:fetchMsgToInbound` (FetchHistory
  path) leaves `Verb` empty, whereas the live poller sets it via
  `VerbForState(auth.State)` (imap.go:359). History items therefore lack the
  trusted/untrusted verb signal. Likely acceptable (Verb is informational on
  replayed history; classifyRaw would require re-fetching raw bytes), but a
  divergence from the "one renderer" intent the file comments claim.
- 2026-05-28 (linkd, low): `linkd/client.go:deliverComment` marks
  `seen[key]=true` (line 437) BEFORE `router.SendMessage` (line 479). If the
  router POST fails transiently, the comment is already deduped and the next
  poll skips it — inbound silently lost. Contrast reditd, which advances its
  cursor only after successful delivery, and emaid, which marks IMAP \Seen
  only after delivery. Reorder so the seen-mark happens after SendMessage
  succeeds (keeping the own-comment/empty-text skips before the mark). Note
  `TestDeliverComment` asserts seen-after-call with an up router, so the test
  would still pass; add a router-failure case alongside the reorder.

- 2026-05-28 (twitd, FIXED): `twitd/src/client.ts:36` registered
  `jid_prefixes: ['x:']` while every inbound/outbound JID uses `twitter:`
  (main.ts inbound `chat_jid:'twitter:home'`, server.ts /health
  `['twitter:']`, verbs.ts `parseJid` strips `^twitter:`). The router's
  inbound handler rejects messages where `!entry.Owns(chat_jid)`
  (api/api.go:257), so EVERY mention twitd delivered would 400 with "jid
  prefix mismatch"; outbound prefix-fallback routing (`ForJID`) also never
  matched. Fixed to `['twitter:']`. All 42 twitd bun tests pass.

## Host-tool capabilities — drift

- 2026-05-03: `ant/skills/ship/SKILL.md` promises a `ship` CLI that
  isn't installed in `ant/Dockerfile`. Either install it (uv-based
  Python tool, see `/home/onvos/.local/share/uv/tools/ship/`) and
  decide auth-state mount strategy (sibling of `HOST_CODEX_DIR`
  pattern), or rewrite the skill as an in-session planning recipe.
  Skill currently has a missing-tool fallback so it doesn't crash,
  but the value prop only lands once the binary ships.

## Aggregated user-reported issues — 2026-05-03

Consolidated from per-group `issues.md` files across krons, sloth, marinade.

### From krons/mayai

- 2026-04-12: Sync `itinerary` skill to global (`~/.claude/skills/`) so all groups share lessons learned (mapy.com deep links, multi-platform review aggregation, Google Maps for route calculations, "no late/early" timing language).
- 2026-04-19: Session continuity broken — agent doesn't remember context from previous sessions. Diary entries often empty ("session ended: error"), `get_history` tool missing, episodes compressed but session detail lost. Fix: always write diary on session end, read 2-3 recent session files before answering, expose `get_history`, add CLAUDE.md "Session Continuity" section.
- 2026-04-19 (whatsapp): Agent should answer trivial in-context questions (e.g. opening hours of a museum already discussed) without re-asking — but only after current workflow finishes.
- 2026-04-25 10:29 UTC (whatsapp): Selective message loss — Kron's reply "nejsem platce dph. jde mi o naklady" sent to a WhatsApp group between 09:49 and 10:29 never reached agent session. Gateway routing / selective-loss bug. Platform scope.
- 2026-04-25 12:47 UTC (whatsapp): Agent sends unnecessary confirmations ("Odesláno", "Done", "Sent") after every action. Message itself is proof of delivery. Suppress meta-commentary.
- 2026-04-25 13:05 UTC (whatsapp): Research file links use direct filesystem paths (`tmp/car-buying-guide.md`, etc.) instead of authenticated WebDAV URLs. Need helper to generate WebDAV URLs with auth so user can access research output remotely.
- 2026-04-25 13:12 UTC (whatsapp): Agent leaks "No response requested." meta-commentary into chat. Filter or prevent this internal-reasoning phrase from being delivered.
- 2026-04-25 13:27 UTC (whatsapp): During long Task-agent runs, user wants interim `<status>` updates so they know agent is working (may have been false-alarm typing-indicator timing — verify).
- 2026-04-25 14:04 UTC: Codify web design anti-patterns into a doc / `/web` skill rules section. Anti-patterns: `border-left: 4px solid` callouts, generic purple gradients reused across unrelated content types, emoji in every heading, generic `box-shadow: 0 2px 8px rgba(0,0,0,0.1)`, inconsistent spacing scales.
- 2026-04-25 14:08 UTC: Agent generates AI-slop color gradients despite contextual instructions. Add palette-research workflow (Coolors, Adobe Color, real industry sites) before designing; reduce gradient overuse to 1-2 key elements; document chosen palette at top of CSS.
- 2026-04-25 14:18 UTC: Hard rule — NEVER combine `border-radius` with `border-top/border-left` colored accents (Bootstrap/AI-template tell). Add to web skill SKILL.md, remove from existing pages, redesign callouts with background colors or sharp corners.
- 2026-04-25 14:19 UTC: Build a `web-style` skill that researches 3-5 real sites in a target domain via browser automation, extracts cohesive design system (colors, type, spacing, borders, shadows), generates CSS variables, and integrates with `/web` skill.
- 2026-04-25 14:23 UTC: Agent misreads urgency signals — interpreted "add this is unacceptable" as "add to issues" instead of "fix it now." Update decision logic so urgency words ("unacceptable", "wrong", "broken") override "add this" interpretation; acknowledge before spawning Task agent and summarize after.
- 2026-04-25 14:29 UTC: When spawning Task subagents, tell user BEFORE ("Working on X..."), use `<status>` tags during, summarize AFTER. Users have zero visibility into background work otherwise.

### From krons/krons

- 2026-04-24 (migration): Telegram adapter returned 502 on `send_message` during v0.29.4 announcement broadcast — all 4 telegram groups (nemo, content, happy, mayai) failed; local krons group succeeded. Platform scope, whapd/adapters layer. [STALE?] (likely fixed since v0.29.4)
- 2026-04-25 (telegram): Custom domain links without trailing slash break routing (e.g. `lore.krons.cx` vs `lore.krons.cx/`). Fix: `proxyd` should 301-redirect domain root → domain/, OR document the requirement. Affects all custom domain mappings.
- 2026-04-28 (telegram): Long research workflows (`/hub`, `/find`, custom research) should emit periodic status updates every ~10 steps. Currently no skill enforces this. Update skill guidelines / CLAUDE.md pattern.

### From krons/rhias/nemo

- 2026-04-13 (telegram): `recall-messages` skill returns XML to a file misleadingly named `~/tmp/messages.json`. Explore agent has to parse XML to find `reply_to` chains. Fix: `get_history` should return JSON, or skill should follow `reply_to` recursively, or gateway should inject referenced messages directly.
- 2026-04-18 (telegram): Agent demonstrates capabilities in `/hello` follow-up but doesn't validate results — Python script failed `FileNotFoundError` on `test_data.csv`, agent reported success anyway. Capability demos should produce complete working examples (with test data) and validate each step.
- 2026-04-18 (telegram): `/hello` advertises howto URL without checking it exists; `/howto` skill jumped to "pick a style" without checking if a howto page already exists. Add `/hello` precheck (`curl -I` the URL or omit), and `/howto` step-0 state-check.
- 2026-04-18 (telegram): Verified-capability audit for `/hello` advertising. Working: research, code (Python/uv 3.14), files-read, scheduling MCP tools, memory (diary/facts/users/episodes), 44 skills, 24 MCP tools, agent-browser, git via `/commit`, multi-language runtimes, media (yt-dlp/whisper/ffmpeg via `/acquire`). Failed/incomplete: claimed `/pub/rhias/nemo/test/` deployment but 404, data-viz (matplotlib/plotly) not tested, file-delivery via `send_file` not demonstrated. Missing from `/hello` but available: `/katastr`, `/nemo`, `/ericeira`, `/itinerary`, `/tweet`, `/hub`, `/git-repo`, `/refine`, `/specs`, `/trader`, `/infra`, `.whisper-language` config.
- 2026-05-01 (internal): `agent-browser` blocked by crackbox proxy returning 403 on HTTPS CONNECT. Direct `no_proxy="*"` times out (no direct egress). Blocks NEMO scanning of sreality.cz, bazos.cz, reality.idnes.cz. Fix options: allow CONNECT through crackbox proxy, alternate proxy with HTTPS tunneling, allowlist specific domains for direct egress, or fall back to WebFetch/curl.

### From sloth/main

- Browser automation — undetected mode. Bot-detection sites (Propdash, others) return empty 39-byte HTML to headless Chrome; curl gets full 6973-byte HTML. Stealth flags + custom UA don't help; `undetected-chromedriver` can't install in externally-managed env. Request: agent-browser `--undetected` flag (patch out webdriver/CDP markers), or pre-installed undetected-chromedriver image, or session-state import (cookies/localStorage from real browser).
- Web skill — auto-add back navigation links to parent pages on landing pages (`/pub/learning/` had no link home). Detect parent path, use consistent zinc-500/zinc-300 styling matching policy pages.

### From marinade/atlas

- 2026-04-24 08:08: Agent should infer meaning from user shortcuts/typos/incomplete words ("10pcr penet" = "10% penetration", "issues and dont be concrete add this there too" = "add to issues.md"). Only ask for clarification when truly ambiguous.
- 2026-04-24 07:58: Project-specific CLAUDE.md standards (e.g. "Killer Hub: SOL not USD") should live in the nested working directory where work happens, not just at group root. Agent should walk CLAUDE.md hierarchy from cwd up.
- 2026-04-24 07:53: Web skill should detect and preserve `<!-- LLM: ... -->` comment blocks (calculated facts, source-of-truth data) when editing pages, instead of regenerating from scratch.
- 2026-04-14 08:30: Telegram message-delivery gap — Kron sent "stake-o-matic" message between 08:11:46 and 08:28:40 that never reached agent context. Investigate Telegram → gateway → agent routing. (reported by krons + marinade — same pattern as krons/mayai 2026-04-25 selective loss)
- 2026-04-16 14:25: Resolve workflow leaked into user-facing output ("## 1. Classify", "## 4. Act" rendered as headers). Resolve skill is `user-invocable: false` and "Do not mention this skill to the user" — agent must follow it silently. Marked as resolved by reporter.
- 2026-04-16 14:26: Verify the consolidation pipeline from group `issues.md` → repo `bugs.md` actually runs and is documented. (This task is the answer.)
- 2026-04-16 16:57: `/diary` skill leaks "Recorded to diary" confirmations to user. Wrap status in `<think>` or omit entirely when invoked automatically by other skills.
- 2026-04-16 19:59 / 2026-04-16 20:45: Slink delivery report and follow-up correction. Reporter initially blamed routing; on investigation, slink resolves correctly via `web:` prefix in `gateway.resolveGroup` (gateway/gateway.go:1195) before route rules. Real fix shipped: `Accept: application/json` content negotiation + optional `?wait=<sec>` blocking param in webd/slink, plus deletion of bogus `{seq:10, match:"verb=slink", target:"atlas"}` route. **Status: closed by reporter.** [STALE? — keep for traceability, do not action]
- 2026-04-17 19:33: Repeated OOM (exit 137) during 300-question generation task — 8 crashes on 2026-04-17 (11:29, 13:41, 14:28, 14:53, 15:48, 17:32, 18:00). Container memory limits or leak during large-history processing / subagent spawning. Need: handle large context without OOM, or checkpoint progress, or fail with explicit error instead of silent crash.
- 2026-04-20 08:18: Episode compaction summaries too verbose. Target: day = 1-3 sentences, week = 1-2 paragraphs max. Update `compact-memories` skill.
- 2026-04-21 15:08: Agent gave generic "bullshit" suggestions for "what to improve on the killer hub?" instead of recovering shipped-state context from diary 20260420 + episodes 2026-W17 etc. Resolve skill should better surface recent shipping context; recognize "what to improve on X" as continuation when X was recently shipped; auto state-recovery at session start.
- 2026-04-24 08:05: User-steering messages mid-execution should be answered with `send_message` MCP tool (explicit routing to user), not `send_reply`. May need gateway support to identify steering context.
- 2026-04-23 07:20: Agent asks "should I do X?" when user already directed X and data is available. Pattern: over-cautious, treats implied directives as optional. CLAUDE.md updated: "Never stop until done. Only ask when blocked." Consider gateway nudge to auto-resume incomplete prior-session work.
- 2026-04-24 18:14: `/hello` greeting should include group folder name (`atlas`) and connector JID (`telegram:1112184352`), not just `$ARIZUKO_GROUP_NAME` (`admin`). Read from message context or routes table.
- 2026-04-24 18:22: Agent ignored XML `reply_to="3554"` attribute and answered as if user's question was about the most recent topic. Rule: ALWAYS check `reply_to` FIRST; structured XML metadata beats recency. Consider gateway injection of referenced-message content inline.
- 2026-04-26 20:32: Agent batched 15+ discrete tasks into one end-of-work message instead of sending interim status after each completion. (reported by atlas + krons — see krons/krons 2026-04-28 long-research progress-update issue, same pattern.) Send `<status>` for quick updates, `send_message` for task completions during multi-task work (>3 tasks).

### From marinade/atlas/support

- 2026-04-25 (telegram): Atlas (parent agent) didn't use `inspect_messages` to read preceding thread context before asking "what topic?" on a continuation question (thread 1454 about commission penalties). Agents should check recent message context before requesting clarification on continuation requests.
- 2026-04-25 (telegram, thread 1459): Atlas reasoning error on validator commission mechanics — falsely claimed protected event fees are "transfer-neutral" (validator gains commission from on-chain rewards but pays from BOND — different pools). Conflated bid penalties with protected events. Failed to invoke `/resolve` on a >5-min-gap continuation. User requested: "force resolve skill on every new question; >5 min delay = new task," and "record why you decided and answered like this."
- 2026-04-26 (telegram): Agent skipped `/resolve` invocation on first message of session ("Back to the commission increase questions...") — went straight to `/recall-memories`. CLAUDE.md says "Every prompt carries a [resolve] nudge. Invoke /resolve BEFORE doing anything else." Also skipped on second message. CLAUDE.md updated to strengthen rule (treat followups >5min as new tasks, always invoke `/resolve` even for topic continuations).
- 2026-04-26 (telegram): Sync local skill/config changes back to canonical arizuko deployment — (1) `resolve/SKILL.md` (>5min = new task), (2) `CLAUDE.md` (resolve-on-followups). Source: `/workspace/self/ant/` or deployment config.
- 2026-04-26 (telegram): Review and consolidate Knowledge Pipeline / fact-verification protocol duplication across system prompt, CLAUDE.md, and `/facts` skill. Decide canonical location for each protocol piece; whether to copy full Knowledge Pipeline to CLAUDE.md; whether to auto-load skills when directories are touched.
- 2026-04-26 (telegram): Auto-load `/facts` skill when agent reads/lists/searches `facts/` directory. Verify whether gateway feature (auto-dispatch by path) or explicit resolve/dispatch logic is needed. Added reminder to `facts/CLAUDE.md`.

## 2026-05-13 — architecture-doc audit (vited + diagrams)

- **Normalize `vited` as first-class sourceless core daemon**: create top-level `vited/` directory mirroring `davd/` shape — move `ant/Dockerfile.vite` to `vited/Dockerfile`, write `vited/README.md` (purpose: web origin for `/pub/*` + auth-gated default; mount `<dataDir>/web:/web`; healthcheck `/@vite/client`), update `Makefile:74-77` `vite-image` target, add row to `README.md` daemon table between `davd` and `teled`. Cost: 1 file move, 1 README, 2 Makefile lines, 1 table row. No code change. (Oracle action #2; deferred because it's a structural rename across docs/Makefile.) See `tmp/vited-audit.md`.
- **Replace hand-maintained inventory tables with generated views**: adapter list rendered 3× by hand and drifts on every adapter ship (slakd ship missed `ROUTING.md:13-23` JID prefix table + `EXTENDING.md:109-122` verb matrix). Same for `README.md` daemon table vs CLAUDE.md core list vs `compose/compose.go` emission. Option A: generate from `template/services/*.toml` + `chanlib` cap declarations at doc-render time. Option B: drop hand tables, link to `template/services/` and per-adapter README. Option C: add a `make doc-check` target that lints for drift. Oracle action #3 (highest long-term payoff, moderate effort).
- **Rename `vited`**: `vited` encodes implementation (Vite). Role is "web origin" — `webd-static` or `weborigind` or `pubd` would be clearer. Defer — name churn cost is meaningful, and the bigger fix (give it a proper directory) is independent.
- **Crackbox network-topology diagram missing from SECURITY.md**: trust-zone box at `SECURITY.md:79-98` doesn't show that with `CRACKBOX_ADMIN_API` set, agents attach to a separate `internal: true` network with crackbox as the only egress hop. Three-tier filter (HTTP CONNECT 403 / DNS NXDOMAIN / no-route) is described in prose at `SECURITY.md:103-143` but no diagram. Add a 20-line ASCII when next touching SECURITY.md.

## 2026-05-13 — reference-manual audit findings

- **Stale tool name in `tier1FixedActions`** (ipc/): registered list contains `get_routes` but the actual registered tool name is `list_routes`. Tier-1 callers asking for the routes-list tool by canonical name miss the allowlist entry. Single-string fix; verify against `ipc/inspect.go` and the routing-tool registrations.
- **`concepts/grants.html` tier formula** — superseded by deeper bug below (two tier formulas collide). Page itself documents one tier model while agent runtime uses another. Fix is at the code level, not the doc level.

## Tier-formula collision (surfaced 2026-05-25)

Two competing formulas compute "tier" for the same folder:

- `auth/identity.go:19` — `Tier = min(strings.Count(folder, "/"), 3)`.
  Bare `atlas` (no slash) → tier 0. Used by `auth.Authorize`,
  `auth/policy.go` (Structural and other authz checks).
- `container/runner.go:70` — `strings.Count(folder, "/") + 1` (when
  not root). Bare `atlas` → tier 1. Used to set `ARIZUKO_TIER` env
  var injected into agent containers. Agent reads this and follows
  the tier-1 web-publishing recipe in `~/.claude/CLAUDE.md`.

So `atlas` is simultaneously tier 0 (per auth) and tier 1 (per agent
env). They encode different concepts under the same word —
"privilege class" vs "nesting depth." Authz is looser than the
agent thinks; the agent applies a recipe (Case B: subdomain vhost)
that authz wouldn't enforce.

Concrete symptom: atlas/strengths failure traced 2026-05-25 — agent
saw `tier=1` env, applied subdomain recipe, subdomain didn't exist,
files went to wrong path. v0.45.10 patched the recipe to be
defensive; the formula split itself remains.

Fix: pick one canonical formula, replace the other call sites,
align all docs. Likely the `runner.go` form (privilege class) is
the canonical one for both env injection AND authz — `min(...,3)`
collapses tiers 3+ into "no surface" which is a different concern.
Worth a dedicated spec.

## dashd nested-folder path routing (2026-05-16, found via playwright)

- **dashd admin endpoints can't address nested folders.**
  `mux.HandleFunc("GET /dash/groups/{folder}/settings", ...)` — Go's
  `{folder}` path-value matches a single segment. `/dash/groups/solo/inbox/settings`
  falls through to the list handler and silently returns the wrong page
  with status 200. Affects: settings, delete, settings POST. Fix:
  use `{folder...}` (trailing wildcard) and trim the suffix.
  Production groups like `solo/inbox` or `corp/eng/sre/oncall` are
  unreachable by these endpoints today. Caught by
  `tests/dashd-playwright/groups.spec.ts` (working around via flat seed).

## Episode compaction audit (2026-05-25, all instances)

Three-instance audit (krons + marinade + sloth) found systemic
silent-success in compact-memories cron skill. Code/skill fixes
shipped in v0.45.9 (commit `ff2c0eb`) — see `.diary/20260525.md`.
Operator-data items below remain open.

- **L — Schedule key naming inconsistency**. `<folder>-mem-N` vs
  `task-<ts>-<hash>` IDs. Two creation paths. Not addressed in v0.45.9;
  cosmetic only.

### Operator-data (left as zero-output per user)

- **D — JID identity drift**. `Coach` (capital) vs `coach` folder on
  sloth; `Strengths` non-canonical on marinade; `local:main` orphaned
  after sub-group split on sloth. Per user direction (2026-05-25):
  these tasks produce zero output but are harmless — DO NOT delete.

### Operator-data (still to fix)

- **I — Missing schedules**. sloth `main/content` is an active group
  (938KB jsonl, 14 diary days) with zero compact-memories scheduled
  tasks. Action: provision the 5 cron tasks (episodes day/week/month,
  diary week/month). Investigate whether onbod/SetupGroup auto-provisions
  these for new groups; if so, why did `main/content` miss them.

### Backfills queued (post-v0.45.9 deploy)

For each (instance, group, period) tuple where the audit found a
sources-present-but-output-missing case, queue a one-off invocation
using the new override arg. Target list assembled from audit reports;
execution sequenced after v0.45.9 image deploy.

## Tier-2 web publishing recipe breaks when WEB_PREFIX is unset (2026-05-25)

Found via atlas/strengths confusion eval: when a tier-2 group runs on an
instance where `WEB_PREFIX` is unset (single-vhost deployment, no
`<prefix>.<host>` subdomain configured), the Case-C tier-2 recipe in
`ant/CLAUDE.md ## How to publish a web page` emits broken URLs:
`https://.fab.krons.cx/...` and writes files to wrong paths
(`/workspace/web/<groupname>/<app>/`).

The case check should fall through to Case A (root `pub/`) when:
- `WEB_PREFIX` is empty OR unset
- `/workspace/web/pub/` exists

Today the agent reasons through tier from depth and picks the matching
Case directly, ignoring the env-var sanity check. Worth tightening the
case-selection logic in the platform CLAUDE.md so a single-vhost
deployment without a subdomain configured doesn't dispatch agents to
the tier-2 recipe.

Operator-data fix landed for atlas/strengths
(`/srv/data/arizuko_marinade/groups/atlas/strengths/CLAUDE.md` —
explicit override disabling the tier-2 recipe). Platform fix queued
for triage.

## FIXED 2026-05-28 — Explicit MCP `reply` posts to channel root inside a thread

The `reply` MCP fallback at `ipc/ipc.go` read `GetLastReplyID(jid, "")`
— hardcoded empty topic — while the gateway seeds the last-reply under
`(jid, thread_ts)`. Fix: gateway exposes the active turn's topic via
`StoreFns.CurrentTopic` (mirrors `CurrentTriggerSender`; set/cleared by
`setCurrentTurn`/`clearCurrentTurn` in both `processSenderBatch` and
`processWebTopics`). The reply fallback + `recordOutbound` now key
`GetLastReplyID`/`SetLastReply`/`BumpEngagement` under
`activeTopic(db, folder)`. `send` is intentionally top-level (fresh
message) — unchanged. Test: `TestServeMCP_Reply_ThreadsViaActiveTopic`.

## whapd_krons session-401 restart loop (2026-05-25, ops)

`arizuko_whapd_krons` is in a restart loop every ~60s. Pattern: WHATSAPP
session token invalidated (`code=401`, `"session invalidated, delete
auth dir and re-pair"`), container exits, systemd restarts, repeat.
Memory note `reference_whapd_pairing` from 2026-05-21: pairing code
`LMDTPW8J` was issued to user's phone (+420735544891); user hasn't
completed the pairing flow on the device side. **Status: waiting on
user phone action; not a platform bug.** Side effect: `arizuko_gated_krons`
emits WARN every ~30s about the failed channel health check (DNS
miss → 503 disconnected). No traffic impact (whatsapp routes won't fire
anyway with no session). To clear: complete pairing on the phone via
LMDTPW8J, OR remove whapd from `template/services/whapd.toml` for the
krons compose if WhatsApp on krons is no longer wanted.

## 900s container timeout — design limit (2026-05-25)

900s is the Claude Code SDK abortion cap; user-visible as `errored=1`
on long parallel-research turns. Not a bug — see
`ARCHITECTURE.md ## Long-running tasks — the 900s container timeout`
for mitigation hints (split tasks across turns, checkpoint to
`~/facts/`, fewer parallel subagents).

## Compaction — sloth has zero compact-log entries post-v0.45.9 (2026-05-25)

v0.45.9 introduced `.compact-log.jl` artifacts under
`<group>/episodes/` and `<group>/diary/`. After redeploy:

- krons: 9 WRITE + 1 SKIP_NO_SOURCES (8 diary-month + 1 month written)
- marinade: 2 WRITE + 9 SKIP_NO_SOURCES (3 diary-month written)
- **sloth: 0 entries — no compaction has executed since v0.45.9 deploy**

Likely root cause: sloth's scheduled_tasks point at non-canonical
chat_jids (`Coach` capital vs lowercase `coach` folder; `local:main`
after sub-group split; `Strengths` shape). The cron prompts dispatch
but the agent container for those JIDs never spawns — no agent =
no compact-log. Per prior user direction (bugs.md "Operator-data —
left as zero-output"): **do not delete** these tasks. But the
secondary effect is sloth gets no compaction at all. Tension between
"don't delete" and "system should be perfect." Worth a re-decision:
either route sloth's cron tasks to a valid agent (e.g. rename `Coach`
chat_jid to `coach`, migrate `local:main` to a sub-group, provision
proper task for `main/content`), or accept sloth runs without
compaction indefinitely.

## Diary/month — partial coverage after v0.45.9 (2026-05-25)

April 2026 diary-month rollups produced for 7 groups across krons +
marinade. Still missing for: `krons/krons` (root), `atlas/tom`,
`atlas/content`, all sloth groups. Likely a mix of:

1. Groups where the May-1 04:00 UTC diary-month cron hasn't fired
   yet on a post-v0.45.9 image. Will fix itself on June 1.
2. Groups where the cron task is misconfigured (sloth's JID drift).
3. Groups where source data (`diary/week/*.md`) doesn't exist for
   the period — legitimate SKIP_NO_SOURCES.

Backfill mechanism exists per v0.45.9 (`/compact-memories diary
month 2026-04` override arg). Not auto-queued. Operator can run
manually per affected group.

## marinade emaid IMAP auth failure — 8 days continuous (2026-05-25)

`arizuko_emaid_marinade` has been logging `imap: NO [AUTHENTICATIONFAILED]
Invalid credentials (Failure)` against `imap.gmail.com:993` every ~60s
since **2026-05-17 18:13 UTC** (8 days, 11,463 error rows). Account:
`im.atlas.the.alpha@gmail.com`. Most likely cause: Gmail app-specific
password was revoked or 2FA changed.

Effect: marinade's email channel is dead (no inbound, no outbound),
emaid container is healthy but performing zero useful work. Burns CPU
on the auth-retry loop.

To clear: operator regenerates a Gmail app password for the account,
updates `EMAIL_PASSWORD` in `/srv/data/arizuko_marinade/.env`, runs
`sudo systemctl restart arizuko_marinade`. Alternatively, if email on
marinade is no longer wanted, remove emaid from the marinade compose
profile (or set `EMAIL_ACCOUNT=` empty so compose skips it).

Not present on krons or sloth (0 IMAP errors). marinade-only.

## Telegram blocked-bot is a permanent fail, not transient (platform follow-up from atlas/tom removal)

teled correctly emits `Forbidden: bot was blocked by the user` as a
403 error, but gateway treats it as transient and retries indefinitely
until `outboundMaxAge`. A 403/blocked is permanent (only the user can
unblock from the Telegram client). Surfaced 2026-05-25 when atlas/tom
crons were deleted (see `.diary/20260525.md`). Patch sketch:

- `teled/bot.go::Send` — classify "bot was blocked by the user" /
  "user is deactivated" as a typed permanent-fail error
- `gateway/gateway.go::dispatchOutbound` — on permanent-fail, set
  status='failed' immediately, skip retry queue
- Optional: mark the chat as `blocked` (new `chats.blocked` column);
  skip future outbound until next inbound from that user
  (auto-unblocks)
- Optional: pause scheduled_tasks targeting a `blocked` chat; resume
  on first inbound

Low priority — atlas/tom was the only affected JID across all three
instances. Will become more valuable if other recipients block bots.

## Aggregated user-reported issues — 2026-05-25 (post-2026-05-03 sweep)

Three parallel audits ran across krons / marinade / sloth groups + per-group
`issues.md` + per-group `.diary/` entries dated after 2026-05-03. Debounced
against the 2026-05-03 aggregation above. 27 new issues, 11 stale revivals,
10 cross-cutting patterns.

### Cross-cutting platform patterns (highest leverage)

#### CC1 — Cross-folder / tier-0 broadcast still blocked

Every migration cycle from v0.34 (2026-05-14) through v0.45.10 (2026-05-25)
logs the same workaround: MCP `send` enforces folder scope even for tier-0
root, so a migrate skill running in mayai can't broadcast version
announcements to krons/rhias/happy. The migrate skill papers over with
per-group fires; the underlying `auth/policy.go` has no tier-0 broadcast
escape hatch.

- **Severity:** medium (recurring; single most-repeated user friction)
- **Scope:** platform — `auth/policy.go` + new tier-0 broadcast tool needed
- **Affected:** krons/mayai, recurring across all releases since v0.34
- **Sources:** `/srv/data/arizuko_krons/groups/mayai/issues.md:5`; diary
  blasts in rhias 2026-05-19, rhias/nemo 2026-05-17
- **Status:** open. Root `bugs.md` 2026-05-03 entry marked `[STALE? — likely
  fixed since v0.29.4]` — audit confirms NOT fixed. Re-open.

#### CC2 — crackbox network-connect race on container restart

Across krons (2026-05-19, ~600 recovery entries across 4 group diaries) and
marinade (2026-05-16, 18, 19 — 100+ retries each), the pattern is the same:
crackbox container disappears (restart, redeploy, OOM), agent containers
hit `connection refused` on `egress register: crackbox register: Post
http://crackbox:3129/v1/register`, retry-loop without backoff and without
self-healing the docker-network attach. The "identity is configured, never
derived" CLAUDE.md note hints the fix landed for one class; the
network-attach side still has hard dependency on a container that can vanish.

- **Severity:** high (took down ingestion across 4 krons groups for ~50min on 2026-05-19)
- **Scope:** platform — crackbox lifecycle + container network-attach race
- **Affected:** krons (rhias + sibling groups), marinade (atlas)
- **Sources:** `/srv/data/arizuko_krons/groups/rhias/issues.md:1-14`;
  `/srv/data/arizuko_krons/groups/{krons,rhias,happy,mayai}/diary/20260519.md`;
  marinade atlas diaries
- **Status:** open. Probably interacts with the proxyd-redeploy side-effect
  memory note — restart sequencing also part of the fix.

#### CC3 — `/web` skill drift on multi-tier deployments

Tier-2 groups (`rhias/nemo`, `atlas/strengths`) hit the recipe wrong: 404s,
wrong paths, missing `/workspace/web/` mounts. Tier formula collision +
`WEB_PREFIX` empty + per-page custom CLAUDE.md not respected. v0.45.10
shipped a defensive recipe but the underlying mount-gap + case-selection
logic isn't bullet-proof.

- **Severity:** medium-high (persistent operator friction; high "fuck the style" factor)
- **Scope:** platform (mount strategy) + skill (`/web` per-tier docs)
- **Affected:** krons/rhias/nemo (mount missing), marinade/atlas/strengths
  (case-selection wrong), krons (custom CLAUDE.md overrides not honored)
- **Sources:** `/srv/data/arizuko_krons/groups/rhias/nemo/issues.md:5-7`;
  root bugs.md tier-2 web entry (2026-05-25)
- **Status:** in-progress. Defensive recipe landed in v0.45.10; mount + case
  selection logic still pending.

#### CC4 — Adapter 502 cluster (Slack + Discord)

Three Slack 502s on marinade (May 15 send / May 20 like / May 25 /pub/) +
two Discord 502s on sloth (May 14, two within 46min on same channel).
Could be coincidence with upstream APIs, but the time-cluster on marinade
suggests proxyd or vited had a marinade-instance-wide event. No
root-cause investigation has happened.

- **Severity:** medium (sporadic but unconfirmed root cause)
- **Scope:** adapter (slakd + discd) or proxyd/vited
- **Affected:** marinade/atlas, sloth/main
- **Sources:** `/srv/data/arizuko_marinade/groups/atlas/issues.md:1-96`;
  `/srv/data/arizuko_sloth/groups/main/issues.md:3-4`
- **Status:** open. Worth a single investigation rather than three independent fixes.

#### CC5 — Subagent context drift

Task-delegated subagents repeatedly ignore parent-context standards:
visual language file (`~/killer-visual-language.md`), parent CLAUDE.md
hierarchy, project-specific styling. Operator quote: "fuck the style so
much", "horrendous". Partial fix in `~/.claude/skills/hub/SKILL.md`
mandating reference-read; structural fix (shared stylesheet or per-subdir
CLAUDE.md with visual language inline) outstanding.

- **Severity:** medium (recurring per-build regression risk)
- **Scope:** platform (subagent context inheritance) + skill (hub/web)
- **Affected:** marinade/atlas (NEAR validators hub, strengths form)
- **Sources:** `/srv/data/arizuko_marinade/groups/atlas/issues.md:98-133`
- **Status:** in-progress

#### CC6 — 900s container timeout on long parallel research

Hit on 4 dates in May (atlas 2026-05-07, 19, 25; krons 2026-05-25;
support 2026-05-19). Same pattern as 2026-04-17 OOM (atlas, 8 crashes in
one day). Hard cap in Claude Code SDK; user-visible as "Container exited
with code 1" reply.

- **Severity:** medium (intentional design limit; user-visible as failure)
- **Scope:** docs (ARCHITECTURE.md / EXTENDING.md) — mitigation hints not
  written yet despite earlier note
- **Affected:** krons + marinade atlas/atlas-support
- **Sources:** root bugs.md `Krons agent 900s timeout`; new krons diary
  + marinade atlas + atlas-support diaries
- **Status:** logged as design limit; ARCHITECTURE.md note still missing.

#### CC7 — Agent over-cautious asking + chat-routing discipline

Two manifestations of the same agent-discipline class. **Over-asking**:
"should I commit and create skill?" when the user already directed it
(krons 2026-05-17 — same as 2026-04-23 atlas). **Routing override**: agent
inserts `chatJid=` in send/reply when the platform already handles
routing, lands reply in wrong channel (sloth 2026-05-16); separately,
agent ignores `thread=` attr on inbound and doesn't switch chat_jid
(sloth 2026-05-23).

- **Severity:** medium (cumulative friction)
- **Scope:** skill / CLAUDE.md guidance — `/resolve`, `/issues` template
- **Affected:** krons/krons, sloth/main
- **Sources:** `/srv/data/arizuko_krons/groups/krons/issues.md:5`;
  `/srv/data/arizuko_sloth/groups/main/issues.md:7,10`
- **Status:** open. CLAUDE.md tightening shipped previously but not enough.

#### CC8 — Egress isolation hits user features

Two sloth cases: `agent-browser` blocked on bot-detection sites (in root
bugs.md 2026-05-03 already); crypto data APIs (CoinMarketCap, CoinGecko,
CoinPaprika) all blocked from agent container (sloth main/main
2026-05-25). Same root cause: crackbox egress allowlist doesn't include
"research APIs" or "stealth browser endpoints".

- **Severity:** high (blocks core trading + research workflows on sloth)
- **Scope:** platform (crackbox egress) — allowlist or trusted-egress channel
- **Affected:** sloth/main, sloth/main/main
- **Sources:** root bugs.md 2026-05-03 agent-browser line;
  `/srv/data/arizuko_sloth/groups/main/main/issues.md:1`
- **Status:** open

#### CC9 — Research-correctness on volatile data

Sloth's research-style workflows surfaced two distinct correctness gaps:
**oracle verbosity** in chat context (full long-form analysis when a
brief summary was wanted — May 16) and **date-confusion in BTC pricing**
(cited May-15 article as May-17 context, actual price ~3% off — May 17,
caught by user). Both came from research/oracle calls; both warrant a
shared "research output discipline" (brief-summary + artifact via
send_file; source-date verification before quoting).

- **Severity:** medium (oracle UX) + high (correctness on financial claims)
- **Scope:** skill (oracle + research) — output discipline
- **Affected:** sloth/main
- **Sources:** `/srv/data/arizuko_sloth/groups/main/issues.md:6,8`
- **Status:** open

#### CC10 — Per-group `issues.md` not being groomed

Already-resolved entries linger across instances. Marinade atlas
2026-05-21/22 strengths-form routing items marked **fixed** in the file
but never deleted; atlas/support 2026-05-13 recall-memories also marked
Fixed but still present. The 2026-04-16 atlas issue ("verify the
consolidation pipeline runs and is documented") is essentially the meta-
bug describing this very process — and it's still manual.

- **Severity:** low (operational)
- **Scope:** ops / skill (groom per-group issues.md after consolidation)
- **Affected:** all instances
- **Status:** open

### Per-instance unique items not covered above

#### krons

- **2026-05-21** Oracle skill leaks cost line ("cost: $0.11") into user chat.
  Skill / `/oracle` output post-processing. Low severity. (`krons/krons/issues.md:7`)
- **2026-05-24** Feature request: `/feed` (changelog feed) skill — diary →
  feed.json → /pub/index.html re-render. Low. (`krons/krons/issues.md:9`)
- **2026-05-24** Feature request: install best-in-class frontend / wiki
  design-system skills. Low. (`krons/krons/issues.md:8`)
- **2026-05-14** Migration broadcast can't cross folder boundaries —
  already covered under CC1.

#### marinade

- **2026-05-24** Agent sends bare URLs as plain text; user wants explicit
  `[text](url)` markdown link formatting. Low.
  (`marinade/atlas/issues.md:56-73`)
- **2026-05-06** PSR-dashboard GUIDE.md bond formula references external
  coefficient names without values. Operator-owned external project doc,
  not arizuko platform. Logged here because it sits in the per-group
  queue. (`marinade/atlas/support/issues.md:6-32`)

#### sloth

- **2026-05-23** News-backtest skill manual workflow shipped; real-time
  path open (bot not in paid/private Telegram channels Zoomer, aggrnews,
  Solid Intel, Layergg, PhoenixNews). Low (feature).
  (`sloth/main/issues.md:9`)
- **main/trading 0 compaction** — already covered by root bugs.md
  "Compaction — sloth has zero compact-log entries post-v0.45.9".

### Stale revivals worth re-checking

- **Selective Telegram message loss** (atlas 2026-04-25, krons/mayai
  2026-04-25 same date) — never confirmed fix; no recurrence in May logs
  either. Spot-check still warranted.
- **/resolve skipping on continuations** (atlas-support 2026-04-25/26) —
  CLAUDE.md tightened, no audit confirms behavior change.
- **2026-04-25 sloth/main items** (`routing-task-to-wrong-session`,
  `social-actions-pin-messages`, `topic-reset-on-inactivity`,
  `topic-visibility-and-replies`) — partially addressed by topic MCP
  tools (`fork_topic` etc. shipped v0.40.0); verify operator-facing
  resolution.
- **OOM during large tasks** (atlas 2026-04-17, 8 crashes one day) — no
  recurrence in May, but 900s timeout (CC6) is the same long-task class
  resurfacing. Worth unifying.
- **Migration-skill diary noise** — krons mayai 2026-05-17 has duplicate
  "13:51" diary entries from auto-migrate. Migrate skill may be
  double-writing.
- **Sloth/main pre-2026-05-03 items in root bugs.md** (`agent-browser
  undetected mode`, `web auto-add back-nav`) — no fix shipped; the
  2026-05-25 crypto-API-blocked entry is plausibly the same class.

### Totals

- Instances audited: 3
- Per-group `issues.md` files scanned: 10 (krons 4, marinade 3, sloth 3)
- Per-group `.diary/` scanned: ~45 files across the three instances
- **New unresolved issues since 2026-05-03: 27**
- **Cross-cutting platform patterns: 10**
- **Stale items worth re-checking: 11**
- Group-level `bugs.md` files: 0 (all aggregation rolls up to root)

### What to fix first (suggested priority)

1. **CC2 crackbox lifecycle** (high; recurring; took down 4 groups for ~50min on 2026-05-19)
2. **CC8 egress isolation** (high; blocks core sloth trading workflow today)
3. **Sloth date-confusion on financial claims** (high; correctness)
4. **CC1 cross-folder broadcast** (medium; recurring; needs new tier-0 tool)
5. **CC3 /web tier drift** (medium-high; v0.45.10 softened, structural fix outstanding)
6. **CC4 adapter 502 cluster** (medium; needs root-cause investigation, not per-incident fix)
7. **CC5 subagent context drift** (medium; partial fix in hub skill, structural pending)

Everything else is medium-low and can ride the next refine cycle.

## LOCAL.md canonical-skills path drift (2026-05-25)

Surfaced during the oracle cost-leak fix (`.diary/20260525.md`):
LOCAL.md still points at
`~/app/tools/assistants/claude-template/global/skills/`, which
doesn't exist on this host — the actual canonical is
`~/app/tools/skills/oracle/`. LOCAL.md needs a refresh.

## proxyd: in-memory route cache violates no-cache discipline (2026-05-27)

**Scope**: `proxyd/resource.go` (`routesResource`, `RWMutex`),
`proxyd/main.go` (`loadInitialRoutes`, per-route `httputil.ReverseProxy`
instances).

**Problem**: proxyd caches `proxyd_routes` rows in memory and only
updates the cache via its own resreg handler. A direct DB write (e.g.
from `arizuko apply`) bypasses the handler and leaves the cache stale.
Architectural rule: all daemons share one SQLite file on one host;
no in-memory config cache is needed or correct. DB reads are
microseconds — cheaper than cache invalidation complexity.

**Fix path**: remove `routesResource` mutex + snapshot pattern. Read
`proxyd_routes` from DB on each request (one indexed query by path).
Keep `httputil.ReverseProxy` instances in a `sync.Map` keyed by
backend URL only (not by route config) — backend URLs are stable;
route rows are not. That preserves connection-pool reuse without
coupling proxy lifetime to route config.

## Agent asks "which group are you?" instead of reading configured identity (2026-05-27)

**Scope**: `ant/` agent prompt / skill system — group identity at runtime.

**Observed**: agent replied "Can't access /workspace/web… which group
are you?" — asking the user for its own group identity rather than
reading it from configuration.

**Problem**: violates CLAUDE.md rule "identity is configured, never
derived." The agent's group folder, instance name, and workspace path
are injected at container start via env vars / PERSONA.md. The agent
must read those, never ask the user and never reverse-engineer from
filesystem paths.

**Fix path**: audit `ant/` system prompt and skill CLAUDE.md to
confirm that `ARIZUKO_GROUP`, `ARIZUKO_INSTANCE`, and the group
folder are explicitly available to the agent at turn start. If any
of those vars are missing from the container env or not surfaced in
the prompt, add them to the compose template and system prompt. The
agent should surface its identity in the first line of any confused
response ("I am atlas on krons, I cannot access X") — never ask.

## Week-N code audit — 5/36 + 5/C + 5/Z + ant SDK (2026-05-28)

Read-only correctness sweep over this week's `[5/36]`/`[5/C]`/`[5/Z]`/`[ant]`/`[fks]`/`[fix]` commits. Ranked high→low.

- 2026-05-28 (resreg/engine groups, medium): `GroupsRow` BeforeInsert comment (`resreg/resources/groups.go:53-54`) claims `open` defaults to 1, but no code sets it — the dangling `if r.ContainerConfig == ""` block is about ContainerConfig, not open. A hand-authored manifest omitting `open` inserts Go zero `open=0`, silently closing every group's sibling visibility (DB column DEFAULT is 1). Export→apply round-trips are safe (`yaml:"open"` has no omitempty so the value is always emitted), but hand-written YAML or any future omit path closes groups. Fix: add `if open not explicitly set → 1` — but Go zero-value can't distinguish "absent" from "0"; model `Open` as `*int` or `*bool` in the row, or drop the misleading comment and document open=0-on-omit as intended.
- 2026-05-28 (proxyd, medium): `snapshot()` swallows the DB error from `AllProxydRoutes()` (`proxyd/resource.go:75-81`, `if err == nil`). On any transient DB error every request silently 404s with no log — invisible outage. Fix: log the error (slog) and/or surface 502 instead of an empty route table.
- 2026-05-28 (proxyd, low/perf): each matched request runs the route snapshot twice — `s.routes()` (main.go:546/538) then `s.proxies()` inside `dispatchRoute` (main.go:568) — and each `snapshot()` rebuilds a fresh `httputil.ReverseProxy` for EVERY route (`proxyFor`→`sameProxy` always returns false, resource.go:132-135). So 2× full `AllProxydRoutes` scan + 2× rebuild-all-proxies per request, and the "connection cache" never reuses connections (transport rebuilt every hit). Functionally correct, but defeats pooling on the hottest path. Fix: snapshot once per request; key proxies by backend URL and actually reuse (the already-logged 2026-05-27 entry sketches this).
- 2026-05-28 (resreg/secrets, low): `resreg.Apply` silently ignores a `secrets:` block in the manifest — the `SkipApplyRebuild` resources `continue` before reading `manifestRows[name]` (`engine.go:471`). An operator declaring a secret triple in YAML expecting creation gets no row and no error. Matches spec's "drafted" note; low. Fix: reject non-empty manifest rows for SkipApplyRebuild resources, or implement triple-insert.
- 2026-05-28 (cmd/apply, low): `cmdApply` success line prints `version -> newVer` using the *manifest* version as the "from" value (`cmd/arizuko/apply.go:65`); after `--force` against a drifted DB this misreports the prior version. Cosmetic. Fix: print the DB's pre-apply version (already returned as `current` in the mismatch path; thread it through on success too).

Verified NOT bugs (do not re-flag): find_messages tier-0 ACL bypass is correct — tier 0 is highest privilege (root), `identity.Tier > 0` filtering matches `inspect_messages` (ipc.go:2232) and spec 5/C "tier-0 bypasses ACL"; `filtered := hits[:0]` in-place compaction is the safe Go idiom. FTS5 triggers (0070) use the standard external-content delete-then-insert pattern; `query` is bound, no injection. SDK 0.3.153 `alwaysLoad` is a real McpStdioServerConfig field (sdk.d.ts:1136) matching the code comment; `ant` typechecks clean (`bunx tsc --noEmit` exit 0). `GroupsRow` covers all 10 live `groups` columns (the dropped `slink_token` from mig 0059 is correctly absent — no apply data-loss). proxyd_routes `preserve_headers`/`gated_by` are NOT NULL DEFAULT in schema (mig 0050) so the non-COALESCE shadow-column scan can't hit NULL. 0068/0069 table rebuilds drop no indexes that aren't recreated (0069 recreates `route_tokens_jid`; web_routes had only a PK).

Counts: 0 high, 3 medium, 2 low. (FK-DSN medium-high resolved — fix `7113c490`.)

## auth/grants trust-boundary sweep (2026-05-28)

- 2026-05-28 (grants, medium): `grants.platformActions` (grants/grants.go:148-152) omits `pin_message`/`unpin_message`/`unpin_all`/`pane_set_prompts`/`pane_set_title`, so `DeriveRules` never emits grant rules for them at tier 1/2. The structural over-deny was fixed (added to `authorizeOutbound` in auth/policy.go so tier-0 and explicit ACL grants now work), but a tier-1/2 agent still cannot pin/set-pane because `grantslib.CheckAction(rules, "pin_message", ...)` finds no matching derived rule. Adding the verbs to `platformActions` widens the default tier-1/2 grant surface (a policy decision) — left for operator to confirm intent. Same gap applies to `pane_set_*` which are Slack-only assistant-pane verbs (spec 6/D). Decide whether pin/pane belong in the per-platform default grant set.

## dashd/onbod bug-hunt sweep (2026-05-28)

- 2026-05-28 (onbod, high): `admitFromQueue` (onbod/main.go:662-731) enforces the per-gate `limit_per_day` by counting `status='approved'` rows whose **`queued_at`** falls in today's date range, but admission sets `status='approved'` without touching `queued_at`. So any backlog queued on a prior day and admitted today never counts toward today's quota: each ~60s tick recomputes today's admitted-count as 0, `remaining = limit_per_day` again, and the whole backlog drains in minutes — the daily rate limit is effectively unbounded for any cross-day backlog. Lets too much through. Root fix needs an admission timestamp (e.g. add `admitted_at TEXT` to `onboarding`, set it on approve, count on it) — that's a store/migrations schema change owned by gated, outside the dashd/onbod bucket and larger than a minimal single-package diff, so logged not fixed. Tests `TestAdmitFromQueue`/`TestAdmitFromQueueRespectsDaily` only exercise same-day queued_at and so pass despite the bug.
- 2026-05-28 (onbod, low): admit cadence gate `admitCount*int(cfg.pollInterval.Seconds()) >= 60` (onbod/main.go:132) truncates `pollInterval.Seconds()` to int. A sub-second `ONBOARD_POLL_INTERVAL` (e.g. `500ms`) makes the multiplier 0, so the `>=60` condition is never true and `admitFromQueue` only runs once at startup — the queue never drains. Unrealistic infra config (poll intervals are seconds-scale), so low. Fix path: compare elapsed wall-time, or use a second ticker for admission.

Verified NOT bugs (do not re-flag): `handleTokenLanding` ignoring `claimOnboarding`'s return then calling `linkJID` (main.go:327-328) is safe — `linkJID` re-guards against rebinding a JID already owned by a different parent (main.go:573-580). `inv.Token[:8]` in invites.go:54 can't panic — `GenHexToken`/invite tokens are always 64 chars; `handleInviteRevoke` already uses `min(8,len)`. Scope-widening guard in grants_admin.go:137 correctly blocks `**`/parent escalation; a `..`-bearing scope can't normalize through `auth.matchPattern` (literal `path.Match` per segment) so it matches no real folder. `handleGroupSettingsSave` not nil-checking `d.dbRW` is unreachable in prod (`dbRW==db`, non-nil); the nil case is read-only test wiring only.

## webd/vited bug-hunt sweep (2026-05-28)

vited has no Go source (sourceless Vite wrapper; see tmp/vited-audit.md), so this sweep is webd-only. FIXED in-tree: `handleTurnSSE` round_done not filtered by turn_id (turn.go) — see commit/diff; regression test in webd/turn_test.go. Remaining (logged, not fixed):

- 2026-05-28 (webd, low): `/api/groups` (`api.go:13` `groupList`) and `/x/groups` (`partials.go:13`) return EVERY group's folder unfiltered by the caller's grants, while the parallel surfaces (`list_groups` MCP tool mcp.go:53, `/me/x/chats`/`/me/x/folders` me.go) filter via `userAllowedFolder`. Both routes sit behind `requireUser` only (server.go:120-125), so any authenticated non-operator can enumerate all folder names. Inconsistent ACL / folder-name info leak. Uncertain whether these back operator-only surfaces gated at a higher proxyd auth tier (the `/` groups page + `/panel`) — if so, intended; if not, `groupList` should filter by `userGroups(r)` like the other reaches. Confirm intended tier before fixing.
- 2026-05-28 (webd, low): `waitForAssistant` (route_token.go:414) returns the first `role=="assistant"` frame off the topic channel WITHOUT filtering by turn_id. On a topic with two concurrent in-flight turns, the JSON `wait` path in `handleChatTokenPost` (route_token.go:208-214) and the `get_round` wait in chat_mcp.go can surface another turn's reply as this turn's. Rare (one in-flight wait per call; widget uses a fresh topic per page load) — the per-turn `/sse` path is now turn_id-filtered, but this blocking-read helper is not. Fix path: thread turnID into waitForAssistant and skip non-matching frames.
- 2026-05-28 (webd, low): `handleSlinkRedirect` (server.go:231) maps bare `GET /slink/<token>` (no trailing slash) → `/chat/<token>` (no trailing slash), but the chat root route is `GET /chat/{token}/{$}` (server.go:76) which requires the trailing slash, so a bare legacy slink URL 301s into a 404. Legacy back-compat path; uncertain whether bare `/slink/<token>` was ever a published URL (the widget always used `/slink/<token>/`). Fix path: append "/" when tail has no "/" before redirecting.

## whapd/mastd/bskyd adapter bug-hunt sweep (2026-05-28)

Read-only correctness pass over `whapd/` (TS), `mastd/`, `bskyd/`. No clean single-package fix surfaced; all genuine issues are cross-cutting (ID-format/protocol) or ambiguous-intent — logged not fixed per triage protocol. Build/vet/test green at sweep time (mastd+bskyd `go test -short` ok; whapd `bun test` 117 pass).

- 2026-05-28 (bskyd, medium): inbound message ID format vs outbound reply-target format are incompatible, so an agent's *default* `reply` to an inbound Bluesky mention/reply fails. `handleNotification` sets `ID = uriToKey(n.URI)` (bare rkey, no slashes — client.go:295/748). The gateway seeds `SetLastReply(jid, topic, last.ID, ...)` (gateway.go:878) with that rkey; the MCP `reply` fallback resolves `GetLastReplyID` and passes the rkey as `req.ReplyTo` (ipc.go:926). `bskyClient.Send` then calls `strongRef(req.ReplyTo)`→`getPostCID`→`splitATURI(rkey)` (client.go:386/579), which needs a full `at://repo/collection/rkey` and returns "invalid at:// uri" on a bare rkey. `Like`/`Delete` have the same dependency on a full AT-URI as `TargetID`. Compounding: `Send` returns `""` (client.go:395) so an agent never learns its own post's URI to later edit/delete it (whereas `Post`/`Quote`/`Repost` return the URI). Reconstruction inside `Send` can't work — the parent's repo is the *original author's* DID, not `bc.getSession().DID`. The only correct fix is to store the full AT-URI as the inbound `ID` (and keep `uriToKey` only for the dedup/log surface), which changes the gateway-visible ID format and touches dedup — cross-cutting, beyond a minimal bskyd-only diff. Tests pass because `TestSend_WithReply`/`TestBotHandler_Like`/`Delete` all feed a full `at://…` URI, never the rkey the runtime actually delivers.

- 2026-05-28 (whapd, low): `/health` returns **503 on stale** (server.ts:158-161: connected but no inbound for >5min → status="stale", code=503), but the canonical Go path `chanlib.handleHealth` keeps **200 on stale** (handler.go:546-557 only sets 503 in the `!isConnected()` branch). A connected whapd that's simply quiet for 5min will be marked unhealthy by anything polling the HTTP code, diverging from every Go adapter. Could be intentional given whapd's session fragility (a quiet-but-connected socket can mask a dead WA session), so left for the user to decide. Fix path if unifying: drop `code=503` for the stale branch in server.ts, matching chanlib (stale reported in body, HTTP 200).

- 2026-05-28 (whapd, low): the `messages.reaction` inbound handler (main.ts:340-356) has no own-echo / `fromMe` loop guard, unlike the `messages.upsert` handler which skips `msg.key.fromMe` and `isOwnEcho(...)` (main.ts:362-365). `buildReactionPayload`/`ReactionEvent` (inbound.ts:149-156) don't model `fromMe` at all. If Baileys re-emits the bot's own emoji reactions (e.g. those sent via `/like`) as inbound `messages.reaction` events, they round-trip back to the gateway as an engagement signal. Uncertain whether Baileys echoes self-reactions (platform-dependent, can't verify offline). Fix path if confirmed: thread the target/reactor `fromMe` flag into `ReactionEvent` and skip self-reactions, mirroring the upsert guard.

Verified NOT bugs (do not re-flag): mastd `stream` backoff (client.go:57-77) resets to 1s only after a successful `streamOnce` and uses exponential `min(backoff*2, 60s)` on error — no busy-loop; `streamOnce` returns nil only on `ctx.Done()`. mastd streaming `streaming.Store(true)` is set only after `StreamingWSUser` succeeds and deferred-false on return, so `/health` correctly reports disconnected during reconnect. bskyd `xrpc` 401 path refreshes-then-retries exactly once (no recursion/loop) and clears `authed` only when both refresh and create fail. bskyd `fetchNotifications` walks oldest→newest and `updateSeen`s per-item so unread items beyond the 25-window aren't bulk-dropped. whapd reconnect (main.ts:303-321) increments `reconnectAttempts`, caps at 10 then exits, resets to 0 only on `open` — correct exponential backoff. whapd reaction `react` key `fromMe:false` (server.ts:305) is correct (target message is the user's); `/delete` `fromMe:true` (server.ts:319) is correct (deleting own message). bskyd `FetchHistory` 5-page cap + `Before` client-side filter is bounded. mastd reblog `Content=string(n.Status.ID)` is an intentional repost-signal payload, not a strip-omission.

- 2026-05-28 (audit, low): web/system webhook batches have no force-flush without a poll. `proxyd` constructs `audit.New(...)` and calls `EmitWeb` but never `StartPoll`; `EmitWeb` now flushes on-write via the 200-event/5s heuristic (matching `emitMessage`), so memory is bounded under sustained traffic and delivery lands within 5s while traffic continues. Residual: a fully-idle tail (<200 events, then no further web events) sits in the batch until the next `EmitWeb`. A periodic force-flush belongs in proxyd's run loop (out of audit bucket); not fixing here.

## store/chanlib bug-hunt sweep — error-handling + boundary lens (2026-05-28)

Read every non-test, non-migration file in `store/` + `chanlib/`. No genuine
error-handling/boundary bug found to fix. The recently-fixed `ObservedSince`
arg-order bug (commit fc417d49) has NO remaining siblings: every conditional
arg-builder I checked aligns placeholders with args correctly — `FindMessages`
(messages.go:706, 11 args/11 placeholders), `NewMessages` (messages.go:124),
`GroupUsageBulk` (cost_log.go:119), `ResolveAllowlist` (network.go:92),
`FolderSecretsResolved` (secrets.go:207). All `IN (?,...)` builders guard the
empty-slice case. All error-returning row iterators check `rows.Err()`; the
`[]T`-returning ones use the codebase's consistent skip-on-error pattern.
`ConsumeInvite` RETURNING/ErrNoRows path is correct and fully covered.
`receiveUpload` filename sanitization (handler.go:296) is sound on the Linux
target. `DoWithRetry` body-rewind + 429/5xx exhaustion logic is correct.
Logged (out of this lens — concurrency, not error/boundary):

- 2026-05-28 (chanlib, medium): `RouterClient.token` is read unlocked in
  `SendMessage` (chanlib.go:152) and `Deregister` (chanlib.go:129) while
  `reregister`→`Register` writes it under `regMu` (chanlib.go:105-110). The
  `regMu` mutex was added to guard the saved re-register params (regName/URL/
  prefixes/caps) but the `token` field — the value those writes most need to
  publish — is left outside the lock on the read side. Adapters call
  `SendMessage` from multiple goroutines (emaid/imap.go:361, slakd/bot.go:370
  & :415, bskyd/client.go:290), so a concurrent 401-triggered re-register
  racing an in-flight `SendMessage` is a real data race on `token`. Matches
  the class of race the recent commits (8b1bb420, edfc2894) were fixing
  elsewhere. Fix path: read `token` under `regMu` in `SendMessage`/`Deregister`
  (or make it atomic.Value). Left unfixed: outside the error-handling/boundary
  lens of this sweep, and a synchronization change wants its own focused diff.

## Pre-existing broken spec links — RESOLVED 2026-05-30 (fresh whole-specs link scan = 0 broken; fixed during the restructuring)

<details><summary>stale entries (kept for traceability)</summary>

Surfaced by a whole-tree markdown link scan during the 5/ finalize; all
predate this session's restructure (the 5/4-delete + 5/A→11/A move added
zero broken links). Fix = repoint to the correct target:
- `specs/5/36-yaml-manifests.md` → `4-data-ingestion-curation-eventing.md` and `2-data-model.md` — these are phase-7 specs (`../7/4-…`, `../7/2-…`) linked relatively as if same-dir.
- `specs/5/5-uniform-mcp-rest.md` → `../11/11-crackbox-secrets.md` (×2) — no such file in specs/11 (renamed/removed); find the real secrets-broker spec (6/Y?).
- `specs/7/index.md` → `5-yaml-manifests.md` (×2) — the yaml-manifests spec is `../5/36-yaml-manifests.md`.
</details>

## resreg (5/36) spec-vs-impl gaps (found 2026-05-29 shipping plan/get; address in finalize)

The engine + 10 resources + apply/export + plan/get + /openapi.json ship and
test green, but these diverge from the rewritten 5/36 vision — none block the
engine, all are finalize/refine-stage work:
- **Scoped apply not wired.** `resreg.Apply` does wholesale `DeleteAll` per
  resource, not the spec's `DELETE … WHERE folder IN (<manifest scope>)`.
  `DeleteScope` exists + is unit-tested but unused → a *partial* manifest
  apply would delete out-of-scope rows (smoke showed seeded rows as removals).
  Depends on resolving the nested-layout question below.
- **Nested manifest layout deferred.** Parser/emitter are flat resource-keyed;
  the spec's group-folder-nested layout is underspecified+self-contradictory
  (the `acl` example writes `scope:` explicitly — nesting does NOT inject scope
  — yet §"Secrets in YAML" says folder is inferred from nesting). Sub correctly
  did not guess. Resolve the nesting→row scope rule before wiring scoped apply.
- **config_meta migration 0067** uses a bare `version` column, not the spec's
  `id INTEGER PRIMARY KEY CHECK(id=1)` singleton guard. Functionally fine
  (single INSERT); minor divergence.
- Not wired: post-commit `SetupGroup` + `arizuko repair` (§new-group fs prep);
  group-removal active-state clearing in the apply tx; multi-file `manifest/`
  composition + PK-collision detection.

## Per-component bug-hunt (2026-05-29, pre-cutover, opus audit vs specs)

Each shipped daemon audited vs its spec. ALL are additive/not-yet-live (zero
current blast radius) — but these MUST be cleared before the HMAC→ES256
cutover (#46) wires them into the live path. Fix queue for the fix-each-daemon
stage (#48). Detailed repros in the audit transcripts.

### authd / auth (vs 5/1) — iss/exp ✓ FIXED (2138cedc)

- [blocker] `arz/folder` claim mis-serialized: `es256.go:39` `Extra`→nested
  `extra.folder`; routd/runed read `Extra["arz/folder"]` (never present) →
  folder-scope authz SILENTLY ABSENT. `MintForSubject`/`MintNarrower` take no
  folder arg. Fix: marshal `Extra` as top-level `arz/<key>` + parse back;
  thread folder through mint.
- [should-fix] `Refresh` doesn't re-snapshot grants (`server.go:164`) — freezes
  scope to issuance (spec: re-run snapshot on refresh).
- [should-fix] DB defaults to `messages.db` not `auth.db` (`main.go:37`).
- [should-fix] `/v1/service-token` takes secret in body not `Authorization`
  header; plaintext not SHA-256; not constant-time (`http.go:70`).
- [should-fix] service grants hard-coded in Go (`http.go:14`) not seeded from
  a `service_keys` table / per-daemon TOML (violates compose-no-edit).
- [should-fix, CUTOVER] missing `/v1` surface: `POST /v1/tokens` (downscope),
  `/v1/keys/rotate`, `DELETE …/accounts/{provider}`, the `/auth/*` OAuth flow.
  The cutover builds these.
- [should-fix] no hourly GC (oauth_state/refresh/keys), no scheduled rotation
  (`AUTHD_KEY_ROTATION_DAYS`), no encryption-at-rest (`store.go:197` raw PKCS8).
- [nit] kid uses uuid not sortable `<unix>-<hex>` (`server.go:70`); window math
  uses `maxAccessTTL` not `max(accessTTL, jwksCacheTTL)`.
- ✓ clean (opus, sequential): scope math (`*:*` reject + bounding), key
  lifecycle (sequential), SQLi, UNIQUE constraints.

**Codex oracle deeper pass (2026-05-29) — found 4 blockers the opus pass
missed (mostly concurrency + the remote verify path):**

- [blocker] remote-path alg-confusion — ✓ FIXED (08b3cadd): the RemoteKeySet
  path called `VerifySignature` directly (no alg check); a poisoned/MITM JWKS
  with an `oct` key + HS256 token would verify. Now pre-pins ES256.
- [blocker] emergency-revoke doesn't propagate (`jwks.go:50,85`): RemoteKeySet
  caches keys + refreshes only on kid-miss, not max-age → a revoked key keeps
  verifying from a verifier's cache until restart/kid-miss. Breaks the
  "kill every token now" lever. Fix: verifier max-age refresh, OR formally
  rely on the short access TTL (≤15m) as the revocation horizon + document it.
- [blocker] retired-key forgery window (`server.go:60`, `jwks.go:96`): verifiers
  don't check `token.iat < key.retired_at`; a STOLEN old key mints valid tokens
  for the entire overlap window (rotation preserves signing authority for the
  thief, not just overlap for preexisting tokens).
- [blocker] refresh rotation RACEABLE (`server.go:145`, `store.go:157`):
  concurrent redeem of one refresh token → both `lookupRefresh` before either
  writes `used_at` → two live successor chains; family-reuse never fires. Fix:
  atomic compare-and-set (`UPDATE … WHERE used_at IS NULL`, check rows-affected).
- [should-fix] audience fail-open (`scope.go:25`): service tokens `aud=""` +
  verifiers never call `MatchesAudience` → cross-service token replay where
  scopes overlap (confused deputy). Bind + check aud per daemon at cutover.
- [should-fix] rotation not atomic + no "exactly one active key" DB constraint
  (`server.go:69`, migration `0001:11`) → concurrent `Rotate()` → multiple
  active signers (split authority at the trust root).
- [should-fix] unbounded request bodies on `/v1/service-token` + `/v1/refresh`
  (`http.go:70,95`, no `http.MaxBytesReader`) → memory/CPU DoS.
- [nit] `go.mod` `go 1.25.5` is rejected by older toolchains (codex couldn't run
  the test suite; our local toolchain is fine) — confirm CI's Go ≥ 1.25.

### routd (vs 5/E + 5/33)

- [blocker] early `submit_turn` 409s legit trailing callbacks (`turns.go:333`):
  collapses "submit_turn arrived" vs "run returned" into `done`; a `send` after
  `submit_turn` before the run-response returns gets `409 turn_done`. Need a
  separate run-live marker.
- [should-fix] run-response clobbers `submit_turn`'s session_id (`loop.go:331`)
  — should prefer submit_turn's.
- [should-fix] idempotency finish not atomic with message append (`turns.go:74`)
  → crash between leaves a permanent `409 in_flight`.
- [should-fix] `POST /v1/messages` ignores `X-Idempotency-Key`, no
  `ambiguous_idempotency` (`server.go:110`).
- [should-fix] reaction topic-inheritance missing at ingress (`routes_http.go:15`)
  — reactions to threads route to the main topic.
- [should-fix] no `outboundRetryLoop` — `pending` rows never re-dispatched/failed
  (`turns.go:153`); no `expired` sweep — stale `running` turns re-fed forever
  (`db.go:402`); `SweepIdempotency` never invoked (`tokens.go:173`).
- [nit] `outcome:error` treated like ok (no mark-errored/failure-notice);
  `BreakerOpen` ignored; done-guard absent on document/like/edit/delete/pin/unpin;
  timestamp-only cursor (same-second drop hazard).
- ✓ clean: proactive chain + FireProactive atomicity + engagement-skip +
  malformed-config + the routd↔runed field match.

### runed (vs 5/P) — execution engine has real gaps

- [blocker] per-folder serialization + `MAX_CONCURRENT` cap GONE
  (`manager.go:75`): two concurrent runs for one idle folder → two containers in
  one workspace (steer-check + register are separate locked sections); no global
  cap.
- [blocker] circuit breaker: wrong trip point (4th call trips; the tripping run
  never runs) + no reset-on-inbound → stays open forever (`manager.go:131`,
  `endRun:181`).
- [blocker] production steer path DEAD — `SetSteer` only called by tests
  (`manager.go:195`); prod always falls through to a fresh spawn → the
  steer-into-running-container contract is unreachable.
- [should-fix] `intersect` broadens scope on empty/disjoint input
  (`manager.go:213`) — fail-open; should fail closed (empty→empty).
- [should-fix] endpoint auth does no scope/folder check (`server.go:43`) — any
  valid token can read any folder's sessions / kill any run.
- [should-fix] `DELETE /v1/runs/{id}` doesn't stop the container (`server.go:92`)
  — only flips DB; `Runtime` has no `Kill`.
- [should-fix] `dockerRuntime` never populates ExitCode/MessageCount
  (`runtimes.go:66`).
- [should-fix] graceful shutdown cancels in-flight runs (`cmd/runed/main.go:111`)
  — spec wants detach + `RUNED_SHUTDOWN_GRACE`.
- [nit] `RunStatus.Outcome` serializes `""` not `null`; read-tool federation
  wrong-path/verb in `federate.go` default case.
- ✓ clean: federation verb→path map, broker isolation (fails closed), DB schema,
  token delivery (never env var), MCP socket served in prod.

### resreg (vs 5/36) — NEW (beyond the already-logged gaps above)

- [blocker] strict-parse NOT implemented (`engine.go:696,290`) — unknown resource
  keys AND unknown row fields silently accepted; an operator typo passes
  validation and the intended config silently doesn't apply. Add
  `Decoder.KnownFields(true)` + reject unknown top-level keys.
- [blocker] OpenAPI per-daemon ownership wrong: `timed` passes `[]string{}` → 0
  paths; routd/runed pass `nil` → all 10 (advertise foreign resources). Live
  `gated` is correct; routd/runed/timed not yet deployed. (`timed/main.go:80`,
  `routd/cmd/routd/main.go:79`, `runed/cmd/runed/main.go:98`)
- [should-fix] `plan` misreports secrets (SkipApplyRebuild) as removals
  (`engine.go:597`) — plan lies vs apply.
- [should-fix] `Diff` false-positive update on server-stamped timestamps
  (`engine.go:562`) — hand-written manifests always churn.
- [should-fix] `scheduled_tasks` scope `Field:"Owner"` (not folder); `secrets`
  `Field:"ScopeID"` (polymorphic) — latent wrong-scope once scoped-delete wired.
- [should-fix] no `audit_log` summary row per apply (`engine.go:412`).
- [nit] single-file apply only (no `manifest/` dir composition + PK-collision).

## Flaky test (2026-05-30, found during fix-wave verification)

- `gateway/integration_test.go:291 TestContainerNameIncludesInstance` — PASSES
  isolated (`go test -run … -count=1`, full + `-short`), but intermittently
  FAILS under the concurrent full `go test ./gateway/` run. Pre-existing (not
  caused by the authd/routd fix-wave; gateway untouched). Shared-state / timing
  flake among the gateway integration tests. Address in the test-completeness
  stage (#49).

## Codebase-wide oracle bug-hunt (2026-05-30, all new+old .go) — TRIAGE QUEUE

### gateway (core) — correctness
- gateway.go:1817 transcribeOnce leaks resp.Body on non-200 (defer after status check)
- gateway.go:788 groupBySender batches across whole slice, not consecutive runs → reorders causality
- gateway.go:924 processWebTopics regroups whole backlog by topic → interleaved topics reordered
- gateway.go:1763 downloadFile treats LimitReader exhaustion as success → silent truncate of oversized attachments
- gateway.go:133/220 SetAudit dropped when Run rebuilds g.gatedFns wholesale (audit wiring lost)
- gateway.go:1456 sendDocument bypasses SEND_DISABLED_CHANNELS (unlike other senders)
- gateway.go:141/659/738 engagement precedence split across pollOnce + resolveOrEngaged (state-dependent routing)
- gateway.go:2168 observeWindow walks all routes, later overrides earlier (contradicts 'first match' comment)
- spawn.go:49/53 + gateway.go:1923 group create ignores AddRoute error after PutGroup → orphaned groups
### gateway — minimality/orthogonality
- tts.go:388 validateVoiceText dead (tested, unused) — wire into sendVoice (also fixes empty/too-long → timeout)
- commands.go:806 + gateway.go:260 invite creation duplicated (command vs MCP) → policy drift
### routd — correctness/scoping (beyond the 7 already fixed)
- routd/db.go:210-214 scan error skipped (row dropped) in core feed → silent message loss/cursor corruption
- routd/routes_http.go:61-77 list APIs use exact-folder not subtree (inconsistent with ownsFolder)
- routd/routes_http.go:80-103 full route-replace under own_group = destructive global op w/ broken scope
- routd/routes_http.go:207-226 + tokens.go:90 route-token op missing folder-ownership check
- routd/proactive.go:316 + db.go:671 proactive reason only in sync.Map, not persisted → replay incomplete
- routd/loop.go:288 + db.go:300 cursor 'min floor' vs 'max' precedence mismatch → unnecessary rescans
### teled (adapters) — correctness
- teled/bot.go:143,227 b.cancel write/read unsynchronized → data race + missed cancel
- teled/bot.go:373,648 msg.Photo!=nil insufficient; empty non-nil slice panics → check len>0
- teled/bot.go:50,140 connected set true once, never cleared → /health lies after auth/poll failure
- teled/bot.go:591 + main.go:24 fetch_history advertised but always returns unsupported → suppresses DB fallback
- teled/main.go:30 Caps drift: quote/repost/dislike advertised but Unsupported; unpin impl'd but not advertised
- teled/bot.go:339 HTML-send fallback drops ReplyToMessageID; bot.go:373 SendFile ignores replyTo
### teled — minimality (refine targets)
- teled/bot.go envelope dup (194/239), msg-id parse dup (312..), thread-parse dup, markdown renderer in adapter

Status: 7 shipped-code findings FIXED (e7d52712/890cb24e/6b2d9843). Above = old-code, triage queue for the fix+refine wave.

## migrate announce skipped for groups with pre-`/opt/arizuko/ant` skills (2026-05-30, low, mostly self-healing)

On the v0.47.0 deploy (152→153), `krons/rhias` reported `Migration v152 → v153
blocked. /workspace/self/ unavailable`. Root cause: rhias's per-group migrate
skill predates the path change to `/opt/arizuko/ant/` and still references the
dead `/workspace/self/` mount. The CURRENT shipped skill
(`ant/skills/self/migration.md`) correctly uses `/opt/arizuko/ant/`, and the
gated-side 3-way merge still advanced rhias's `.claude/skills/self/` to 153 — so
the group self-heals next cycle. The only loss: rhias (and any similarly-stale
group) **missed the v0.47.0 announce broadcast** this once. Other krons groups
(mayai/happy/krons) announced fine. Hardening option (not required): make the
migrate skill's announce step independent of the file-copy step so a stale-path
failure can't skip the broadcast — future migrations are unaffected since the
correct skill is shipped. Manual remedy for rhias: re-trigger `/migrate` once
the new skill is active. Scope: ant migrate skill / per-group skill staleness.

## post-cutover verifier findings — deferred (2026-05-30, 4 adversarial sonnet verifiers)

The cutover code (daemon parity + spec + dual-verify + compose-wire) was
adversarially re-verified; all security-critical + parity fixes confirmed SOLID.
7 actionable regressions/bugs were FIXED (365328d4/a99dc3cc/485a89d6/5543c65f/
2b2d5486). These remain DEFERRED:

- **FLIP-BLOCKER (auth, MED, latent):** `auth/middleware.go stampES256Identity`
  is incomplete on the ES256-DIRECT path — it stamps the `user:`-prefixed sub
  (breaks onbod `github:`/`google:` gate matching → onboarding stalls) and only
  `arz/folder` as `X-User-Groups` (operator `**` grant → `[]` → locked out of
  `requireFolder` routes). LATENT: during the soak proxyd still injects
  `X-User-Sig`, so webd/onbod take the HMAC path; this bites only at the FLIP
  when proxyd stops injecting `X-User-Groups`. RESOLVE BEFORE FLIP: keep proxyd
  the grant-injector (verify ES256 → inject `X-User-Groups` from DB grants), OR
  have `stampES256Identity` do the DB grants lookup + stamp the bare sub.
- **LOW (auth):** middleware comment says bare-sub but stamps prefixed (comment-
  only); `/v1/service-token` response omits `expires_at` (fallback works);
  retired-key forgery check is a no-op on RemoteKeySet (all backend verifiers) —
  documented tradeoff, short access-TTL is the defence; `handleTokens` issuer-
  mint doesn't validate/normalize the `user:` sub prefix (grants strip via
  bareSub, minted sub just violates the contract).
- **LOW (runed):** `Kill` doesn't `EndSession` for non-isolated killed runs;
  `armRunTTL` can have an in-flight Kill after Run returns (idempotent, not a
  leak); `handleRunKill`/`handleRunStatus` don't fold the token folder (docker-
  network-internal, threat-model-acceptable); `staticBroker` test jti PK
  collision swallowed in multi-run tests (test-infra).
- **LOW (gateway/adapters):** `tts.synthesize` uses `context.Background()` (not
  cancelled on shutdown); `like`/`post` cap-drift is invisible to
  `CapImplReport` (chanreg never gates them — informational only); mastd reply
  Topic = immediate parent, not thread root (depth-1 threads, design limit);
  emaid `sendReply` has a redundant rootMsgID fallback (dead when called from
  server.go, not a bug).

None are soak blockers. The auth FLIP-BLOCKER must be resolved before the prod
flip removes proxyd's `X-User-Groups` injection.

## cross-section test findings (2026-05-30)

- **routd ingress not folder-gated (verify intent, low/med):** `POST /v1/messages`
  (`routd/server.go:172`) is scope-gated (`messages:write`) but `authed` discards
  the token's `arz/folder` claim — a `messages:write` token bound to folder A can
  ingest a message that routes to folder B. Folder-binding is enforced only on the
  turn-callback surface (`authzTurn`, turns.go:138) + route-CRUD. Plausibly
  by-design (routing is route-table-driven; the callback is where folder-scope
  matters), but if ingress should be folder-scoped it's MISSING. Surfaced by
  `routd/authd_integration_test.go` (1fd68528).
- **authd `TestRefreshRotationRaceSingleWinner` flaky (test-quality, low):**
  intermittently fails under parallel `go test ./...` load ("successor must be
  revoked after a concurrent-reuse family kill"), passes 5/5 in isolation + on
  rerun. The TEST's own concurrency timing is racy, not the refresh code. Fix the
  test's synchronization (deterministic ordering) in the refine pass so it stops
  reddening the integration checkpoint.

## routd extended-verb delivery gap (2026-05-30, flip-completeness, med)

routd's `/v1/turns/{id}/*` callback surface (server.go:93-102) covers reply,
send, document, history, like, edit, delete, pin, unpin, result — but MISSING
`send_voice`, `post`, `quote`, `repost`, `forward`, `dislike`, `typing` (gated's
ipc/ipc.go exposes all of them). routd's `Deliverer` likewise lacks SendVoice/
Post/Quote/Repost/Forward/Dislike/Typing. At the cutover flip, an agent calling
any of these → 404 on routd → voice replies, social-feed amplification (post/
quote/repost for twitter/mastodon/reddit/bluesky), forward, typing indicators,
and dislike-reactions all break. CLOSE before a feature-complete flip: port the
verbs (add /v1/turns handlers + Deliverer methods, from ipc/ipc.go + the gated
Channel/Socializer egress). Core conversation (text reply/send/document/react)
works without it. Found scoping the channel-plane (ff8b3ca4).

## routd in-process MCP — deferred tools (Phase 2, 2026-05-31, cutover-gap, med)

Phase 2 stood up routd's in-process agent MCP socket (`routd/mcp.go`:
`buildGatedFns`/`buildStoreFns`/`ServeTurnMCP`), wiring every tool whose state
routd owns (reply/send/document, social verbs, group controls, route tokens,
submit_turn, routes/messages/find/errored/web-routes/engagement/cost reads).
These tools are LEFT NIL because their backing capability lives in another
plane; the next phases must wire them where they belong:

- **SendVoice** — needs the TTS pipeline (`TTS_BASE_URL` / `ttsd`); a separate
  capability, not a routd Deliverer verb. Wire when the voice egress moves.
- **CreateInvite** — no invites table in routd; invites are onbod/authd domain.
- **SpawnGroup / SetupGroup / FetchPlatformHistory / EnqueueMessageCheck** —
  need the container runner / onbod / platform adapters; runed/onbod domain.
  (InjectMessage is wired: the routd loop polls new rows by timestamp, so no
  explicit EnqueueMessageCheck is required for it.)
- **Audit** (GatedFns.Audit) — the audit sink is not plumbed into routd yet.
- **StoreFns tasks** (CreateTask/GetTask/UpdateTaskStatus/DeleteTask/ListTasks/
  TaskRunLogs) — tasks live in `timed`; federate there.
- **StoreFns ACL/identity/audit** (ListACL/Authorize/GetIdentityForSub/
  LogIPCAudit) — operator ACL overlay + identity + ipc_audit move to `authd`.
  Consequence: `ServeTurnMCP` passes `callerSub=""` (full operator access) and
  `Authorize` is nil — the row-based grants gate is DEFERRED to authd. Tier-
  default grant rules (grants.DeriveRules) DO apply.
- **StoreFns sessions** (RecentSessions/GetSession) — session_log is runed's.
- **GroupsDir / WebDir** — left empty on the in-process GatedFns; the dispatch
  wire-in (next phase) supplies them so the file-path tools (send_file media,
  vhosts) resolve. ServeTurnMCP is a capability and is NOT yet called from the
  loop, so this is harmless until then.

## /v1/turns HTTP surface — agent-unused post-flip (minimization candidate)

After the routd-owns-MCP-socket flip, the agent reaches its write tools via
routd's in-process MCP socket (ipc.ServeMCP in routd/mcp.go), NOT the HTTP
`/v1/turns/{turn_id}/*` callbacks. Those handlers (routd/turns.go) + the
idempotency ledger (IdemClaim/AppendAndFinish/idempotency_keys) + the
routd/api/v1 turn-callback client are now exercised only by tests, not by any
production caller (runed's Federator is deleted). They remain as the bearer-gated
REST face over the shared appendAndDeliver/recordTurnResult (still live in-process).
KEPT for now (cheap, tested, REST-uniform). Future minimization: delete the HTTP
surface + idem ledger + convert the ~6 test files (auth_test, deliver_test,
authd_integration_test, parity_test, contract_test, fixes_test) to drive the
in-process MCP path instead. Not done now — test-conversion churn, no functional gain.

## Eval skill drift (2026-05-31, LOW — eval skill, not split code)

`/eval` (.claude/skills/eval/SKILL.md) check #4 queries `chats.errored` — column
no longer exists (schema changed; errored tracking moved). Check #10 reads
`PRAGMA user_version` but db_utils.Migrate tracks applied migrations in its own
table (user_version stays 0), so the "DB behind" check is a false-negative.
Fix: update check #4 to the current errored mechanism + check #10 to the
migrations table. Tangential to the split; do during a docs/skill pass.

## Eval baseline 2026-05-31 11:5x — krons gated v0.48 HEALTHY
service active; channels registering; no container/lifecycle errors; idle.
ONLY recurring error: whatsapp auto-deregister (503 disconnected) ~every 6min =
the known whapd pairing issue (memory whapd_pairing_recovery; awaiting user re-pair).
Not a code bug. Split daemons not deployed (gated default). Baseline = PASS.

# ============================================================
# SPLIT BUGHUNT 2026-05-31 (4 sonnet subs, post-flip)
# ============================================================

## Bucket 4 — cross-cutting + dead/backcompat (sub a751cf2d)

### Bugs
- **A3-1 HIGH (crackbox deployments only)**: `runed/docker.go` builds `container.Input`
  with a ZERO `Egress` field — `EgressConfig.active()` is false → crackbox network
  isolation SILENTLY DISABLED on the split; agent containers get unrestricted
  internet when isolation is expected. Gated builds it from `cfg.*Egress*`
  (gateway.go:1309-1317); runed never does. Full fix also needs `AllowlistFn`,
  which derives from grants — runed has no grant DB (→ federate to authd/routd or
  pass via RunRequest). NEEDS DESIGN. Gated path unaffected.
- **A2-3 LOW**: `runed/server.go` handleRun checks `runs:run` scope but not the
  token's arz/folder claim vs requested folder (already logged earlier). Low —
  broker only mints agent-scoped tokens.
- **A3-2 COSMETIC**: `container/runner.go:647` `NO_PROXY=...,gated,...` — stale for
  split (daemon is routd); `gated` DNS won't resolve in split agent containers.
  Only matters when crackbox active. Change gated→routd. REMOVE-NOW.
- A2-1/A2-2 SUSPECTED-LOW: service-token boot timing + static fallback; mitigated
  by compose depends_on/healthcheck + always-set AUTHD_SERVICE_KEY.

### Dead code
- `runed/api/v1/types.go:73-87` SpawnRequest/SpawnResponse — never mounted, split-only
  dead. REMOVE-NOW.
- `routd/api/v1/client.go` Client struct + all methods — production caller (runed
  Federator) deleted; test-only now. REMOVE-POST-SOAK (removing now breaks tests).
  Wire TYPES (ReplyRequest/TurnResult/…) must stay.
- Stale federation comments: routd/server.go:116, routd/reads_http.go:13-14,
  routd/turns.go:20 ("runed federates …"). REMOVE-NOW (comment cleanup).
- KEEP: ROUTD/RUNED_SERVICE_TOKEN fallbacks (local-dev load-bearing); /v1/turns
  surface (REST face, post-soak); container.Input.GatedFns/StoreFns (gated uses).

### Confirmed CORRECT
socket cross-container share (both mount dataDir→/srv/app/home, uid 1000:1000,
/run/ipc/gated.sock matches); depends_on ordering; service-key provisioning;
ES256-only verify in split (no dual-verify wrinkle).

## Bucket 3 — runed execution plane (sub a38300c4) + my verification

- **R-CRIT-1 outcomeFor always Silent (CONFIRMED, I verified)**: clean run →
  Output{Status:"success"} but HadOutput never set + Result="" (stdout io.Discard).
  outcomeFor's `!HadOutput && Result==""` branch fires → EVERY clean split run =
  OutcomeSilent. routd dispatch.go:174 hadOutput=(Outcome!=Silent)=false →
  queue.go:298/303 consecutiveFailures++ → breaker opens after 3 turns on EVERY
  folder. BREAKS THE SOAK. FIX: outcomeFor `case Status=="success": OutcomeOK`
  (output goes out-of-band via the socket now; runed can't see it). TOP PRIORITY.
- **R-HIGH-2 Model+ContainerConfig+Isolated not forwarded (CONFIRMED, I verified)**:
  routd/dispatch.go dispatchRun builds RunRequest WITHOUT Model/ContainerConfig/
  Isolated. Per-group model + container_config + timed-isolation all dead. Needs a
  routd.DB GroupConfig(folder) reader + PutGroup to write container_config +
  dispatchRun to set the 3 fields (Isolated = HasPrefix(trigger,"timed-isolated:")).
- **R-HIGH-3 RegisterSteer before container starts (CONFIRMED-plausible)**: steer is a
  SIGUSR1 no-op during the docker-run startup window; fallback (queue→fresh spawn)
  is correct, so it's wasted-container inefficiency not a correctness break. MED really.
- **R-MED-6 Grants not on container.Input (CONFIRMED)**: runed docker.go never sets
  Input.Grants → buildMounts share_mount check always false → /var/lib/share never
  mounted. routd derives grants (ServeTurnMCP) but they don't reach runed. Needs
  grants in RunRequest OR runed re-derives. share_mount is niche.
- **R-CRIT-1b token undelivered → RECLASSIFIED DEAD (my analysis)**: the sub called
  "brokered JWS never reaches agent" CRITICAL. But post-flip the agent uses the
  in-process socket (SO_PEERCRED), NOT a bearer for tool calls (gated's socket was
  also uid-gated). The token was for the deleted federation (agent→runed→routd HTTP).
  So the broker machinery is VESTIGIAL post-flip, not a critical break — nothing the
  agent does needs it. → remove the per-spawn broker post-soak (verify no agent use).
- DEAD: runed/db.go RecentSessionRecords, ActiveSpawnForFolder, AppendLog/Logs (no
  callers; spawn_logs streaming endpoint never implemented); SpawnRequest/Response;
  routd PutGroup never writes container_config column.

## Bucket 2 — routd agent-tool plane (sub a74d7fea) + my verification

- **A-HIGH-2 jid injection in mcpIssueRouteToken (CONFIRMED)**: REST handlers validate
  jid_suffix/source_label with segRe=`^[\w-]+$`; mcpIssueRouteToken appends raw →
  agent can inject `/`/`..` into the stored JID. FIX: share segRe, validate.
- **A-CRIT-1 nil-deref on mutation closures → DEBUNKED (I verified)**: premise was
  "Deliverer nil in production" citing a STALE pre-flip bugs.md entry. main.go:86/104/
  109 wires the real chanDeliverer + SetChannelRegistry. NOT nil. Downgrade to LOW
  defensive nit (add nil-guards anyway, cheap). The "FLIP-BLOCKER Deliverer-nil +
  channel-surface-absent" entries from 2026-05-30 are ALREADY FIXED in main.go.
- **A-HIGH-3 handleDocument no engagement bump → GATED-PARITY (my analysis)**: gated's
  send_file via internalSend→recordOutbound with platformID="" ALSO skips the bump.
  Not a split regression. Note only.
- **A-MED-4 cache tokens dropped → BY-DESIGN (my analysis)**: cost_log has no cache
  columns in gated either; CacheRead/Write fold into CostCents upstream. Not a bug.
- **A-MED-6 delegated-turn engagement on delivery-jid → GATED-PARITY**: gated's MCP
  recordOutbound also bumped the delivery jid. Not a split regression. Note.
- A-MED-5 invite_create desc "Tier 0-2" vs enforce 0-1 — doc fix (already logged).
- A-LOW-7 disengage via time.Until(time.Time{}) fragile — works, low.

## Bucket 1 — routd dispatch/socket plane (sub a0c7589a)

- **D-HIGH-1 RoundDone not wired (CONFIRMED-plausible)**: main.go LoopConfig never sets
  RoundDone → publishRoundDone is a nil-guard no-op → web-chat SSE clients hang
  waiting for round_done. FIX: wire RoundDoneFn in main.go (resolve web: channel +
  PostRoundDone). VERIFY main.go.
- **D-HIGH-2/6 steered turn left state=running (CONFIRMED-plausible)**: on Steered=true,
  runTurn returns without SetTurnState(done)/SetRunReturned + cursor not advanced →
  recoverPending re-dispatches on restart (duplicate output) + ChatHasRunningTurn
  blocks proactive for runTimeout. FIX: on steered, SetTurnState(done) + advance.
- **D-MED-3 UpdateObservedCursor no-op on new topic (CONFIRMED-plausible)**: cursor
  UPDATE runs before PutSession creates the sessions row → turns 1-2 both see full
  observed window (double-consume). FIX: upsert the cursor OR ensure the row exists.
- D-LOW-4 NewMessages no is_bot filter → bot rows poll through, !hadOutput→breaker++
  (related to R-CRIT-1; same queue-counts-silent-as-failure root).
- D-LOW-5 turnLocks sync.Map never cleaned → unbounded growth. FIX: Delete on done.

## Triage for the fix pass (confirmed regressions, not gated-parity)
P0 outcomeFor (R-CRIT-1) → breaker storm. P1: dispatchRun Model/Config/Isolated
(R-HIGH-2), RoundDone (D-HIGH-1), steered-turn-state (D-HIGH-2), jid validation
(A-HIGH-2). P2: observed-cursor (D-MED-3), turnLocks cleanup (D-LOW-5), queue
silent!=failure (D-LOW-4, overlaps P0). P3 design-needed: Egress/crackbox (A3-1),
Grants-on-Input (R-MED-6). DEAD-now: SpawnRequest/Response, RecentSessionRecords/
ActiveSpawnForFolder/AppendLog/Logs, stale federation comments, NO_PROXY gated→routd.
GATED-PARITY (note, don't touch): doc-engagement, delegated-engagement, cache-tokens.

## P3 SOAK-BLOCKER (krons, crackbox-on): egress isolation + grants not wired to runed

krons has CRACKBOX_ADMIN_API set → egress isolation ACTIVE. But runed/docker.go
builds container.Input with NO Egress + NO Grants. registerEgress (container/
egress.go) no-ops unless EgressConfig.active() (needs AdminURL+NetworkPrefix+
CrackboxContainer+AllowlistFn). So a split spawn is NEVER attached to the crackbox
network → DEFAULT docker network → OPEN INTERNET. Security regression on the split.
Also: Input.Grants empty → buildMounts share_mount always false (/var/lib/share
never mounted).

Root: per-folder authz data (egress allowlist via store.ResolveAllowlist→network_rules
table store/migrations/0037; grant rules via grants.DeriveRules+ACL) lives in
gated.db / authd. runed has neither. routd derives grants (ServeTurnMCP) + the
resreg catalog already lists network_rules as a routd resource, but routd.db has NO
network_rules migration yet.

DECISION NEEDED (ownership): where does network_rules live in the split?
  (A) routd owns it (per resreg catalog): add a network_rules migration + a
      ResolveAllowlist to routd.DB; routd resolves the allowlist + the grant rules
      at dispatch and passes BOTH in RunRequest (EgressAllowlist []string + Grants
      []string); runed builds EgressConfig (static fields from its cfg + AllowlistFn
      returning the passed list) + sets Input.Grants. Clean — routd already owns the
      authz knowledge; runed stays a pure executor fed everything it needs.
  (B) runed owns it (per "runed is the only daemon wired to crackbox"): network_rules
      migration in runed.db + ResolveAllowlist on runed.DB + the `arizuko network`
      CLI retargets runed.db. runed owns egress end-to-end; grants still need to cross
      (authd) for share_mount.
RECOMMEND (A): routd is the authz/conversation plane; runed stays pure. One
RunRequest grows EgressAllowlist+Grants; runed wires them into container.Input.
Until done: a krons split soak must run with crackbox OFF, or this lands first.
NOT a quick fix — own task. The non-crackbox correctness is otherwise soak-ready
(all confirmed breaker/dispatch/socket regressions fixed this session).

## routd disabled() not applied to social verbs (split-vs-gated parity, LOW)
routd/deliver.go disabled() (SEND_DISABLED_CHANNELS) gates Send/Document/Post only.
gated's canSendToJID also gates Forward/Quote/Repost/React/Edit/Pin/Voice
(gateway.go:1520-1559 + tts.go:22). So on the split, SEND_DISABLED_CHANNELS=discord
suppresses send/reply/document/post but NOT a discord forward/quote/like/edit/pin.
Fix: apply d.disabled(jid) (or targetJID for Forward) at the top of the other
chanDeliverer verbs too. Also re-verify the stale bugs.md:1101 sendDocument note
(gated sendDocument DOES check canSendToJID at gateway.go:1478 now — likely fixed).

## specs/5 audit (2026-06-03) — 4-opus-sub review (status + docs + bugs)

Reviewed all 35 specs/5 in 4 buckets. Findings are FINDER-confidence (cited
file:line, not all independently re-verified) — VERIFY before fixing, per triage
protocol. The `network_rules`/egress surface was excluded (impl in flight).

### LIVE (deployed gated path) — verify + fix candidates
- **MEDIUM send_file thread-reattendance** — `ipc/ipc.go:580` internalSend calls
  `recordOutbound(...,platformID="",...)` for files (SendDocument returns no id),
  so file-message bot rows have empty platform_id → reply-to-bot promotion misses
  (same class as 2ba0f5e1, still live for files). Needs SendDocument to return the
  platform id.
- **MEDIUM webhook no rate limit** — spec 5/W §"Rate limits" mandates a per-token
  in-memory bucket (prefix-based ceiling); webd has none (`webd/route_token.go`
  only caps body size). Public bearer `/hook/` + `/chat/` unthrottled.
- **HIGH (secrets) connector tools get nil secrets** — `ipc/ipc.go:896`
  CallConnectorTool(...,nil); `ipc.injectSecretsAdapter` named in
  `container/runner.go:250` does NOT exist; `store.FolderSecretsResolved` has no
  production caller → folder/user secrets never injected into connector/MCP calls.
  (spec 5/32 Phase C + 9/11.)
- **MEDIUM user-scope secrets write-only** — `store.ScopeUser` secrets set via
  `cmd/arizuko/secret.go` but never read/overlaid at resolution (spec 5/32 folder∪user).
- **MEDIUM (unconfirmed) proxyd /pub→/priv traversal** — `proxyd/main.go:547,587`
  lack the `..`/`%2e%2e`/`%2f` reject the vhost branch has (`:503-505`); sibling
  web/pub|web/priv dirs → possible auth-gate bypass. No test. Needs runtime check.
- **MEDIUM proxyd routes MCP unmounted** — `proxyd/resource.go:305-334` declares
  routes.* MCP tools but `resreg.RegisterMCP` is called nowhere; proxyd has no MCP
  socket. REST `/v1/routes` works, MCP half doesn't (uniform-MCP-REST violation).
- **MEDIUM tier-0 /var/lib/www RO vs spec V rw** — `container/runner.go:592` hardcodes
  RO for all tiers; spec V mandates tier-0 rw to stage content for any group.
- **HIGH (feature) O-otlp turn correlation dead** — `obs.WithTurn`/`InjectTraceparent`/
  `ExtractTraceparent` have zero prod callers; deterministic per-turn TraceID never
  happens. Frontmatter `shipped` overclaims (→ partial).
- **MEDIUM I-tool-call-logging Layer B unstructured** — `container/runner.go:300` logs
  `[ant] [tool]` JSON as raw `line=`, never lifts to `tool=`/`surface=`/`duration_ms=`;
  `journalctl|grep tool=Bash` acceptance fails. `ant/src/tool-log.ts:78` sets
  turn_id=sessionId (wrong). Frontmatter `shipped` overclaims.

### DORMANT (split, CUTOVER_SPLIT) — bite at cutover
- routd `GET /v1/turns/{id}/thread` unmounted (`routd/server.go`); get_thread routes to
  `/v1/messages/thread` instead — REST twin path drift.
- routd agent outbound no pending/retry: in-process recordOutbound writes Status=sent
  only on success, drops on transient delivery fail, omits turn_id — diverges from REST
  twin appendAndDeliver (two-renderers-drift).
- routd `/openapi.json` advertises groups/acl/secrets/network_rules CRUD it doesn't serve.
- **HIGH-dormant** `auth/middleware.go:25` ES256 stamps prefixed `user:<sub>` into
  X-User-Sub; downstream (dashd me_secrets.go:25,54; audit) expects bare sub →
  user-secret + audit corruption on ES256 soak path (only when AUTHD_URL set). Must strip prefix.
- `authd/server.go:235` Downscope skips TTL cap when parent already expired (latent).

### DOCS gaps / drift
- ENGAGEMENT_TTL: `concepts/engagement.html:62` + `reference/env.html:457` say 10m;
  code `core/config.go:203` is 20m. (Spec 5/G self-contradicts; docs copied wrong value.) EASY.
- No `components/routd.html` / `components/runed.html`; components/index lists gated 5×,
  split 0×. ARCHITECTURE.md:205 vs :229 self-contradict (routd vs runed hosts MCP).
- mention-promotion (5/L) + proactive-interjection (5/33) undocumented in web docs.
- arizuko-client.js still primary-targets deprecated `/slink/` (works via 301).

### Spec frontmatter accuracy (flag to owner, not code bugs)
- `32-tenant-self-service` marked shipped but Phase C (secrets resolution) incomplete,
  Phase D diverged (chats.kind→is_group). `D-docs-refs-redesign` draft but shipped.
  `K-ant-backend-codex` draft but shipped. `33-proactive` draft but shipped-in-routd.
  `I`/`O` shipped but partial. `1-auth-standalone` accurately partial (schema/endpoints
  diverge materially: no encryption-at-rest, /v1/keys/rotate, unlink, account-linking).
- Stale `specs/6/...` citations: `store/migrations/0054` (→5/B), `api/api.go:297` (→5/L),
  `0034-secrets` cites 7/35.

## CEO/CTO critique + live docs audit (2026-06-04) — NOT yet fixed

Adversarial CEO + CTO assessment (.ship/critique-{ceo,cto}-20260604.md), live
browser pass over krons /pub/arizuko/, codex second-opinion on the new
primitive framing (specs/5/A). New finds below; ENGAGEMENT_TTL drift already
logged above (DOCS gaps).

### Security (HIGH — defense-in-depth)
- **dashd trusts unsigned `X-User-Sub`** (`dashd/me_secrets.go:25`, `dashd/profile.go:24`):
  no `RequireSigned` anywhere in dashd; relies entirely on proxyd being the sole
  ingress. webd DOES verify (`RequireSignedOrBearer`, webd/server.go) — asymmetry.
  One published dashd port / SSRF from a co-resident container / compose slip →
  operator takeover via `curl -H "X-User-Sub: github:operator"`, nothing to forge.
  Fix-path: wrap dashd mux in `auth.RequireSigned(PROXYD_HMAC_SECRET)` like webd.
  By-design today, but a real defense-in-depth gap per CLAUDE.md trust-boundary rule.

### Docs vs code (HIGH — docs describe a protection/route the code doesn't)
- `concepts/auth.html:89,91,94` names **`CHANNEL_SECRET`** for proxyd's identity-sig;
  code uses **`PROXYD_HMAC_SECRET`** (proxyd/main.go:53,798). Wrong var — an operator
  matching CHANNEL_SECRET across daemons configures the wrong secret.
- `concepts/auth.html:91` claims dashd does `auth.RequireSigned(CHANNEL_SECRET)` +
  HMAC recompute. It does NOT (see security finding). Doc describes a guard that
  isn't there.
- `concepts/routing.html:62` teaches `chat_jid=web:acme → solo/chat`; code REJECTS
  web: route predicates at insert (`store/routes.go:48,101` ErrWebJIDRouted).

### Docs vs code (MED/LOW)
- `reference/mcp.html:84,97` send/reply guidance BACKWARDS vs code: reply is the
  default (`ipc/ipc.go:928`), send is the exception (`ipc/ipc.go:900`).
- `reference/schema.html:441,447` + `products/slack-team/index.html:169` say secrets
  "plaintext at rest"; encrypted AES-256-GCM now (`store/secrets.go:18`). Stale.
- `get_routes` phantom MCP tool in `concepts/routing.html:95`, `concepts/autoviv.html:100`,
  `reference/grants.html:135,151` — retired; only list/set/add/delete_routes exist.
- `reference/mcp.html` + `reference/env.html` source anchors (`ipc/ipc.go#L…`,
  `core/config.go#L182`) ~+400 lines stale — generate them, don't hand-maintain.
- No `components/vited.html` despite vited named core in CLAUDE.md.

### CEO credibility / live staleness (deploy-the-docs)
- Live landing footer `v0.48.0` (`index.html:369`) vs CHANGELOG `v0.49.0`; live
  `/changelog/` omits v0.49.0 entirely. (Internal links fine: 77/77 resolve 200.)
- `/products/` ships 2 cards (reality, slack-team); CLI offers 9 templates
  (`cmd/arizuko/main.go:193`); landing promises "and more." Thin catalog vs pitch.
- README "Direction" + landing forward surfaces present `CUTOVER_SPLIT`/git-as-truth
  (unshipped: compose/compose.go:31 default-off; specs/7/3 drafting) in present
  tense without a "planned" marker.
- No named buyer / no business posture stated anywhere (CEO kill-shot: operability
  gap × no SaaS = narrow market). Positioning rewrite in critique-ceo memo.

## SPLIT (CUTOVER_SPLIT) deploy validation — empirical, 2026-06-04

User: "the split should already be live." Stood up a throwaway split instance on
krons (fresh DBs, torn down after). FINDING: the split does NOT survive first boot.
This is the never-done end-to-end validation. The code BUILDS (go build ./routd/...
./runed/... ./authd/... = exit 0) and the compose TOPOLOGY generates correctly
(authd+routd+runed, no gated), but runtime fails:

- **authd/routd/runed SQLITE_CANTOPEN (code 14)** opening auth.db/routd.db/runed.db.
  Split daemons run `user 1000:1000` (compose.go:681 authdService etc.) against the
  freshly-created root-owned data dir → can't create their DBs. The gated monolith
  works in the same mount, so the gap is split-specific DB-dir creation/ownership in
  `arizuko create` / compose (ensure the split daemons' DB path is 1000-writable).
- **routd host-port collision** — routdService (compose.go:702) publishes
  `API_PORT:8080`; fresh instance defaults API_PORT=8080, already held on the shared
  host (arizuko_gated_sloth). Multi-instance hosts need distinct API_PORT per instance.
- **No data migration messages.db -> {auth,routd,runed}.db** (confirmed: no split
  daemon opens messages.db — only comments noting they're separate, runed/README.md:27,
  authd/main.go:40). Flipping CUTOVER_SPLIT on an EXISTING instance = fresh empty DBs =
  total loss of history/groups/routes/grants. NO rollback shim (spec U "no backward compat").

To make the split live: (1) fix split DB-dir ownership in create/compose; (2) fix
per-instance port handling; (3) build the messages.db->split migrator; (4) validate
boot+turn end-to-end on a fresh instance; (5) backup + cutover (krons first). Steps 3
+ the cutover are the real program; the split-flip is a one-way production-topology
decision. E-routd/P-runed status stays partial until this lands.
