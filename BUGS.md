# BUGS.md — open issues queue

> **Standing policy — every fix here adheres to WISDOM** (`~/.claude/CLAUDE.md`):
> minimality (smallest change at the root cause), orthogonality (one concern per
> fix, no parallel second path — amend the original), fail loud to the user on
> user-facing paths, retry only transient errors, fix causes not symptoms.
> Redesigns (new contract, changed cross-daemon control flow, auth-model or
> schema changes) stay recorded as proposals and ship only after user sign-off.

> **Three markers, and the middle one is not a bug.** `## L<n>` is open.
> `## ✅ FIXED <date> L<n>` is closed. `## 📋 RECORD L<n>` is a finding kept so a
> later sweep does not re-derive it — a negative result, or a principle whose
> work items are their own entries. A record has nothing to fix and never will,
> so counting one as open overstates the queue.

## What needs you

Every open entry waits on a person, not on work. Grouped by what it waits FOR,
so the queue can be unblocked without reading 5,000 lines.
`TestOpenEntriesAreListedHere` (`tests/bugsindex`) fails when this list and the
open set disagree, so it cannot quietly rot.

It reads the HEADING, because that is what a reader sees and it is the only
marker every entry carries. Ten entries said open in the heading and carried no
`- **Status:** open` line, so a Status-only count answered 22 for a queue of
31 — this section shipped with those ten missing.

**A contract or design decision.** Each names the question in its own Status
line; none can be answered from the code.

- `L347` — may the cursor stay put when a steered turn does not run? Changes
  the consume contract (`L104`).
- `L317` — may `/stop` drop the backlog older than itself, and is that drop
  sender-scoped? No other command eats a row it did not author.
- `L273`, `L249`, `L319`, `L159`, `L202` — proposals, each costed in
  its entry.
- `L339`, `L167`, `L200` — proposals too. `L200` and `L133` were re-measured
  2026-09-21 and each came back narrower than filed.
- `L436` — measure whether a route target holding `..` resolves, then fix the
  gate in every resource that takes a folder.

- `L437` — emit `WHISPER_BASE_URL` into the turn and hand every guest an
  ungated host service, or drop the self-transcription claim from the two
  skills that make it.

- `L440` — may `/send` key its frame on the row id so a live frame and a
  replayed one agree? It changes `platform_id`, which other readers compare.

- `L439` — the first status frame has no topic because routd writes the row
  with no reply-to; the fix is in the send path, not inside webd.

- `L434` — may gitd hold the dedup claim on a 5xx and dead-letter visibly?
  It changes delivery semantics for every adapter.

- `L435` — store the target on the `github_sources` row and migrate, or accept
  that a revoke below the owner leaves its routes.

- `L133` — one question after that re-measure: `ext:` is a third namespace the
  proposal would break.
- `L152` — the record is written: the reconciler has no oracle, because arizuko
  keeps no durable list of principals that have presented themselves. Building
  one is a new hot-path write, so it is a decision.
- `L338` — the restart loop is stopped; where Slack lives is the open decision.
- `F41` — an open question, unanswered by design: the re-greet cooldown is a
  rate and not a cap, sharpened to a three-way choice with costs.
- `V2` — may `acl_membership` grow a LIST action? It publishes who holds
  authority over whom, on REST and on MCP.
- `L326` — the honest liveness fix needs a barrier nobody has signed off.
- `L392` — every daemon on the host network, no `ports:` mapping. The
  loopback binds and the ports above 49000 shipped with v0.70.0; what is
  left is the move itself, which re-addresses every daemon and needs the Go
  suite runnable on the host first.
- `L422` — thirteen daemons read env keys that generation never delivers, so
  a key set in `.env` takes its code default and every surface reads healthy.
  emaid's five auth keys are delivered; each remaining row needs a yes or no
  before the guard can ship.
- `L425` — emaid's XOAUTH2 refusal is pinned against a fake shaped to the code:
  treat Google's bare `NO` as permanent now, or measure a Gmail mailbox first?
- `L430` — a verdict handled at ingress skips message-id dedup and the
  transcript and holds the inbound request on the ack's send: store the row
  first, ack asynchronously, or state the shape in `specs/5/E-routd.md`?
- `L431` — `inject_message` lets an agent write `/approve` as any sender: mark
  injected rows so `handleCommand` skips them, or drop the `sender` parameter?

**A contract or design decision, measured since.**

- `L387` — the boot budget is MISSED: p50 1.54 s against the ≤ 1.0 s gate, over 60
  live turns. `6/41` makes that budget the gate that keeps `6/20`'s warm pool
  declined, so the pool question reopens. Yours.
- `L390` — `specs/5/12` promises a 4 s doubling retry backoff and the code has no
  wait at all, re-feeding on a 2 s tick. Which of the two is the design is yours;
  three attempts in six seconds against a failing turn is not cosmetic.

- `L404` — a BYOC row is dropped under a gateway and `6/23` says it is translated. The
  silence is fixed; whether a user's own credential should fund a gateway key is yours.
- `L398` — the suite's slowness is MEASURED and it is not a slow test: 783 routd tests,
  median 0.61 s, each creating 32 tables through the fixture every daemon shares. Making
  that per-binary instead of per-test is a change to `store.TestOwner`'s contract, so it
  is yours.

**The GitHub channel, opened 2026-10-16 when the path first ran end to end.**
Three siblings were fixed the same day (`L413`, `L415`, `L419`); these are the
ones no measurement can settle.

- `L417` — an App source seeds `github:*/*`, so an instruction injected into a
  comment body reaches any repository the token can see. Narrowing the seed
  breaks enrolment; refusing an unknown JID breaks the agent opening an issue.
  The tension is in the capability, not the code, and it is a security
  boundary.
- `L416` — engagement promotes an observe-only PR event to a turn for 30
  minutes, so a push or a label wakes the agent. Being asked is now mechanical
  (a review request or assignment to the bot, an @-mention: `verb=mention`, a
  turn at any time). What stays yours is what else may wake an engaged agent:
  a push that breaks CI probably yes, a label probably no, same event class.
- `L420` — the self-drop holds because one identity happens to serve both
  doors on sloth. Dropping a SET of known bot logins works only while every
  identity the agent can post as is enumerable, and a credential granted later
  is not.
- `L411` — nothing checks the reply channel can send before the turn is paid
  for. Cheap to answer for a dead channel, undecidable for one failing
  intermittently.
- `L418` — an edited or deleted comment carries the object id, so the row is a
  duplicate no-op and nothing logs it. Re-keying rows on the delivery id is
  prescribed in `6/27` and touches ingest for every event type.
- `L421` — `replyThreadRoot` returns "" whenever a turn has a topic, and routd
  fills `ThreadID` from the topic. Both are wrong; repairing either changes
  what teled, discd and slakd receive.

**Small fixes awaiting a go.** Each is bounded, and its entry names the fix.

- `L427` — gitd marks a delivery accepted before it knows the outcome.
- `L428` — the web design guard passes shapes it should refuse, and a site that
  loses fifteen sidebars.
- `L429` — four classes the pages use have no rule in `hub.css`.
- `L432` — codex and pi start fresh under the declared session id and
  `submit_turn` overwrites it.
- `L433` — nothing fails when an isolated turn's session is written back, or
  when ant stops passing the declared id.

**An operator action on live data.** The platform side is done in each.
(`L334` is closed and carries one too: the third party's `**` on sloth is not
revoked — see its entry.)

- `L318` — the 19 per-daemon store keys. The resource list is declared and
  checked; the ROLLOUT is the live-key rotation.
- `L406` — `gitd` holds no store key, so enabling it restart-loops the instance.
  gitd holds a key and runs on sloth; what is left is the refusal that should
  stop an enable without a key, and the mint is `L319`.
- `L409` — the `vumilan/morha` webhook posts to sloth's App path and gitd
  refuses it without the flagged bypass. Deliveries are accepted behind the
  bypass; repointing the webhook needs admin on `vumilan/morha`.
- `L327` — which Discord channels should answer. The inversion CHECK shipped
  (`/eval` 22); which rows to renumber is your call.
- `L308`, `L181`, `L131`, `L188`.
- `L379` — `anteval --smoke` scores 1/9 on the VM substrate and the substrate is
  not why: the harness's callback sink is a docker-network address a guest cannot
  reach. Three fixes are costed in the entry and choosing between them is yours —
  a name the proxy can dial, one tap rule, or the callback over MCP.
- `L364` — `route rm <id>` on sloth deletes the WRONG row: six ids are duplicated
  by the marinade merge. It already destroyed a live rule once. Re-minting sloth's
  route ids is yours; do not address a sloth rule by id until then.
- `L363` — sloth advertises five atlas Telegram routes and can serve one. The
  bot the others talk to is served by nothing, and its only token is in the
  staged marinade tree. Three options in the entry; one is a deploy.
- `L307` — seven Slack chats carry traffic and match no route, live on sloth.
  Which route rows to write is yours.
- `L63` — krons whapd wants a phone re-pair; the recipe is in the entry.
- `L359` — 11 G of interrupted git repacks, on a disk with 3.9 G free. The
  files are unreferenced and the deletion is live data, so it is yours.

**Proposals the guard could not see until 2026-10-08.** Each heading says PROPOSED or
"needs sign-off" and no literal `open`, so `TestOpenEntriesAreListedHere` passed over
all thirteen — for two months, and the queue under-reported by that much. The guard
reads those words now.

- `L145`, `L146` — the inbound path as one rewrite: a channel payload becomes
  (destination URI, actor), and a link's URI selects a destination within what its
  token authorizes. Two halves of one design.
- `L185`, `L186` — the CLI's runtime verbs need a principal and an audit surface;
  dashd's admin writes over routd's `/v1` need a forwarder trust routd does not grant.
- `L171` — one TypeScript channel module for whapd and twitd.
- `L151` — an adapter with a dead credential restart-loops and nothing pushes the fact
  anywhere a person sees.
- `L2` — a delegated child is never dispatched; a measured cause and an unsigned fix.
- `L75` — half (a), the dead retry, SHIPPED. What is left is (b): a re-fed batch resolves
  by its newest row, so a pending mention is demoted by ordinary chatter, and the fix has
  to outrank it without breaking `L56` and `L336`.
- `L13`, `L31`, `L41`, `L134` — a write-only `secret_use_log` with no read surface;
  four of six product payload kinds seed-once so a product never updates; compose
  fragments frozen once they differ by a byte; a `shell:env:` grant that cannot be
  conditioned on the channel.
- `M4` — `vited` in production: the crash class is fixed and the cost premise was
  REFUTED, so what is left is posture.

**Outside this repo.**

- `L40` — the edge must set `X-Forwarded-For` before `TRUSTED_PROXIES` may
  widen. Adding the gateway without that buys nothing.

**Blocked on another entry.**

- `L189` — blocked on `L137`. Do not take it alone.


**A spec whose status is a claim.**


- **GITHUB-PRODUCT-SETUP** (MED, config) — `ant/examples/github/PRODUCT.md`
  requires `GITHUB_APP_ID`, `GITHUB_PRIVATE_KEY`, and `GITHUB_WEBHOOK_SECRET`,
  but `gitd.loadConfig` reads none of them. The setup claims gitd is unbuilt,
  uses the unregistered `git:` JID scheme, and copies a connector to a directory
  that `LoadConnectors` does not read. CONFIRMED at HEAD `09eef243` on
  2026-10-09 by comparing the product with `gitd/main.go`,
  `cmd/arizuko/github.go`, and `template/connectors/github.toml`.
  No test — config. **Fix:** make the existing product declare the supported
  token and describe source registration, `github:` routes, and `connectors.toml`.
  Working-tree correction verified by product seed tests, web link tests, and
  a before/after manifest check; commit blocked by read-only `.git`.

- **GITHUB-TOPIC-REPOSITORY** (MED, design) — `GitHubJID.Topic` at
  `chanlib/github_jid.go:75` returns only kind and number; `gitd/event.go:298`
  copies that value to the message, and `store/sessions.go` keys sessions by
  folder and topic. Two repositories' PR 12 routed to one group therefore share
  `pr/12`. Source-traced at HEAD `09eef243` on 2026-10-09; no test — design.
  **Proposal:** include repository identity in GitHub topics, or route each
  repository to a distinct group. Needs sign-off because the topic contract and
  existing conversation keys change.

- **GITHUB-APP-CREDENTIAL-CONTRACT** (MED, design) — `gitd/bot.go` sends
  comments with a static user token. `routd/mcp.go` resolves connector credentials
  from user/folder secrets, with no App installation-token provider. The requested
  installable App therefore cannot act as its bot without a personal token.
  Source-traced at HEAD `09eef243` on 2026-10-09; no test — design.
  **Proposal:** one App per instance; one shared token client for gitd and the
  broker. Resolve the installation through GitHub and restrict each token to the
  repository in the trusted turn JID. An explicit `GITHUB_INSTALLATION_TOKEN`
  connector slot selects App credentials; `GITHUB_TOKEN` retains user semantics.
  App keys stay in daemon env; derived tokens stay outside agent environments
  and storage. Existing tool grants and holds remain the authority for calls.
  No relay, credential endpoint, installation table, or wider storage/runtime
  port. Needs sign-off for the new credential contract.

- **BUILD-DAEMON-FAILURE-MASKED** (MED, ops) — `Makefile:22` runs daemon
  submakes separated by semicolons. A later success hides an earlier failure.
  Measured 2026-10-09: `make build` returned zero after dashd failed with a
  read-only Go cache; the complete output is in
  `/tmp/arizuko-github-baseline-build.log`. No test — ops.
  **Proposal:** stop the build on the first failed submake. This is an adjacent
  build-gate repair; scope approval is pending.

## Design invariants — intentional, do not re-report

One line per ruling: what was reported, and why it is correct behaviour. These are
never deleted — they exist to stop the same finding coming back. The reasoning that
produced each one is in `.diary/`, dated; this list is the index, not the argument.

- **`L351` — the six `import.go` files (1139 lines, zero production callers) are the
  test-seeding API, not dead code.** No symbol is unused, all six say so in their own
  first lines, and `audit/import.go` records why it cannot go: `Emit` stamps `now` with
  a random id suffix, so a trail at instants a test chooses is a state `Emit` cannot
  build, and dashd orders several owners on `created_at`. Every call site asserts
  through a production reader (`SessionsByFamily`, `RecentSessions`, `SpawnByRunID`,
  `ClaimDelivery`, `audit.Query`); the `counts` checks beside them are vacuous-pass
  guards. A sweep that counts those lines as dead is counting the fixtures.
- **`L147` — `web:` JIDs exist only to satisfy a storage column.** The principle:
  the channel attests, the router resolves, the ACL authorizes. Records a design
  fault, not a work item.
- **`F77` — the skill-guard's per-line scan is evadable by splitting a payload.** The
  demonstrated case is fixed; the class is out of scope by `5/23`, which says the guard
  raises the cost of one move and is not a containment boundary.
- **`L15` — the 2026-08-11 dead-code sweep's rejected hits.** `deadcode ./...` gave 149
  hits, 56 under `-test`; nine survived verification and were filed separately. Kept so
  the next sweep does not re-report the same ~135.

## L440 — webd's live frame id and its replayed id differ, so an edit appends instead of replacing (2026-10-17, open)

FOUND 2026-10-17 while building `specs/6/46-turn-progress.md` step 4
(`eddce204`). A live `/send` frame carries webd's minted `bot-…` id, which
equals `platform_id`; the same row replayed through `TurnFrames` carries the
row's `out-…` id. `POST /edit` publishes the live id, so a browser that
loaded the status through replay has no `msg-<id>` to replace and appends a
second element. Measured by reading both id sites, not by driving a browser.

**What needs you:** `/send` could key the frame on its `idempotency_key`,
which is the row id, and the two would agree. That changes `platform_id`,
which other readers compare against, so it is a contract change rather than
a fix.

## L439 — webd's first status frame lands on a topicless hub key and no live subscriber sees it (2026-10-17, open)

FOUND 2026-10-17 while building `specs/6/46-turn-progress.md` step 4
(`eddce204`). `handleSend` takes its topic only from the `reply_to` message.
routd writes a status row with no reply-to and `chanreg` sends webd no
`thread_id`, so the first live frame publishes to hub key `jid/` and a
topic subscriber never matches. The first edit carries the row's topic and
shows as a new frame, so progress still appears, one update late.

The cause is in the send path rather than in webd alone, which is why this
is recorded instead of patched inside webd. Unconfirmed on a running
instance: the reasoning is from the code, and the browser behaviour has not
been watched.

## L437 — two shipped skills tell the agent to call `$WHISPER_BASE_URL`, and the guest never gets it (2026-10-17, open)

MEASURED 2026-10-17 while sweeping `specs/3/0-agent-capabilities.md`, which
claims "`WHISPER_BASE_URL` env var passed to container. Agent transcribes audio
it downloads (yt-dlp, podcast episodes) without going through gateway IPC."

The guest's whole env is `turnEnv` (`runed/render.go:102`), and it emits `TZ`,
`ARIZUKO_MODEL`, `ARIZUKO_EFFORT`, `ARIZUKO_QUERY_TIMEOUT_MS`,
`ARIZUKO_BACKEND`, `MODEL_GATEWAY_URL`, `MODEL_GATEWAY_KEY`,
`ARIZUKO_IS_ROOT`, `ARIZUKO_GH_WRITE` — and nothing else. `git grep WHISPER --
runed/ container/ ant/` returns no producer: the only hits are the two
consumers below. `WHISPER_BASE_URL` reaches routd
(`routd/cmd/routd/main.go:224`) and the daemon env lists
(`compose/compose.go:142,174`), which is the inbound enrichment path, not the
agent's.

Two consumers read it inside the guest and get an empty string:

- `ant/skills/acquire/SKILL.md:41` — `curl -F "file=@audio.ogg"
  "$WHISPER_BASE_URL/inference" | jq -r '.text'`
- `ant/skills/self/migrations/031-rich-container-tooling.md:15` — the same
  line, announced to every group as a capability

So the documented self-transcription path has never worked: the curl goes to
`/inference`, which resolves to nothing, and the agent reports a failed
download rather than a missing grant.

This is [[ant_env_delivery]] — the agent-side instance of the class `L422`
names and leaves unfiled on this side.

- **Severity:** medium — silent, and it is advertised to agents in a migration
  broadcast, so the failure reads as the audio being unreachable
- **Scope:** `runed/render.go` `turnEnv`, `ant/skills/acquire/SKILL.md`,
  `ant/skills/self/migrations/031-rich-container-tooling.md`
- **Status:** open — needs a decision, because the two honest fixes differ in
  what they grant. Emitting `WHISPER_BASE_URL` into `turnEnv` hands every turn
  a host service the egress allowlist does not gate (the URL is internal, so
  the turn's proxy is not in the path); dropping the claim from both skills
  keeps the surface as built and loses the capability. Either way the spec's
  claim goes.
- **Related:** `L422` (the daemon-side census), [[ant_env_delivery]]

## L436 — the github_sources target passes containment with `..` in it (2026-10-17, open)

`routd/github_sources_resource.go:69` gates the target with
`groupfolder.Contains(folder, target)` alone. `Contains` is a prefix test, so
`eng/../ops` is contained in `eng` and the register answers 201, seeding four
route rules whose target is the literal string `eng/../ops`.
`groupfolder.IsValidFolder` exists and refuses `..` (`groupfolder/folder.go:74`),
and this handler never calls it.

Measured while writing the gdrive handler, which now calls `IsValidFolder`
before `Contains` and answers 400. I did not change github.

What the stored rule then does is NOT measured. Whether a target holding `..`
ever resolves to a real folder decides whether this is a cross-folder reach or
only a dead rule that looks live on the dashboard. Measure before fixing.

- **Severity:** unknown until the resolution is measured — a cross-folder reach
  if the target resolves, cosmetic if it cannot
- **Scope:** `routd/github_sources_resource.go:69`; the same gate shape is in
  every resource that resolves a folder parameter, so the sweep is the fix
- **Measurement:** read the handler 2026-10-17; `Contains` is
  `strings.HasPrefix` plus a separator check, and no call site validates the
  folder first
- **Status:** open — one probe answers it: seed the rule and ask routd to route
  a message for that repository
- **Related:** `L435` (the same file, the revoke path), `L417`

## L435 — a github source bound below its owner keeps its routes after a revoke (2026-10-17, open)

Found while writing `store/gdrive_files.go` against `github_sources` as the
model. `AddGitHubSourceInto` renders the seeded rules from its `target`
parameter (`store/github_sources.go:177`), and `RemoveGitHubSourceInto`
renders them from `ownerFolder` (`store/github_sources.go:238`). The removal
deletes rules BY MATCH, so when the target differs from the owner the two
renderings disagree and the delete matches nothing.

The row holds no target, which is why the remove path has nothing better to
use. A revoke then reports success, drops the source item, and leaves four
route rules pointing at a folder nobody bound any more; a later inbound event
for that repository still routes.

`gdrive_files` does not inherit this: the item carries `target_folder` and the
remove path renders from the row.

- **Severity:** medium — a revoked binding keeps routing, and the CLI says it
  worked
- **Scope:** `store/github_sources.go` `AddGitHubSourceInto` /
  `RemoveGitHubSourceInto`
- **Measurement:** read both call sites 2026-10-17; the add takes `target`, the
  remove takes `ownerFolder`, and `deleteRoutesMatchingInto` matches on the
  rendered `match` string
- **Status:** open — the fix is to store the target on the row, as
  `gdrive_files` does, and migrate the existing items
- **Related:** `L417` (the github seed's reach), `specs/6/45-gdrive-comments-channel.md`

## L434 — gitd calls every 5xx not-delivered, and its own spec calls 5xx ambiguous (2026-10-17, open — needs sign-off)

Found while fixing `L426`, in the same function, and left alone there on
purpose. `gitd/bot.go` `call` wraps every `>=500` in `chanlib.ErrNotDelivered`,
and `chanlib/dedup.go` reads that sentinel as proof nothing was posted, so it
releases the claim and routd re-sends.

`specs/5/34-channel-protocol.md` puts a 5xx in the AMBIGUOUS class for the
opposite reason: a proxy 502 on a POST may already have created the comment
upstream. Both cannot be right.

This is the inverse risk of `L413`. There the fix stopped a duplicate comment
by refusing to classify a transport error; here the code still asserts
not-delivered on a response that says nothing about whether the write landed.
gitd is the adapter whose posts are public, so the cost of being wrong is a
double comment on someone's pull request.

- **Severity:** medium — one duplicate per 5xx that actually wrote, and
  nothing reports it
- **Scope:** `gitd/bot.go` `call`, against `specs/5/34-channel-protocol.md`
- **Status:** open — the honest shape is to leave the claim held and dead-letter
  visibly, which changes delivery semantics and needs your sign-off
- **Related:** `L413` is the same seam from the other side

## L422 — thirteen daemons read env keys generation never delivers, so setting them in .env does nothing (2026-10-16, open — needs sign-off)

MEASURED 2026-10-16 with a guard written for this entry and parked at
a session scratchpad draft, not committed (see below). It
collects every key each daemon reads through `MustEnv`/`EnvOr`/`Getenv`,
subtracts `daemonKeys`, `commonKeys` and the keys its own
`template/services/<d>.yml` sets, and reports the rest:

```
onbod  ROUTER_URL                 authd   LISTEN_ADDR
dashd  APP_SRC_DIR AUTH_BASE_URL DASH_ACCENT_COLOR DATA_DIR WHATSAPP_PHONE
routd  CONNECTORS_TOML CONNECTOR_CALL_TIMEOUT_MS
webd   PROXYD_URL WEBD_LISTEN WEBD_URL
proxyd PROXYD_LISTEN PUB_REDIRECT_URL TRUSTED_PROXIES VITE_ADDR
reditd CHANNEL_NAME REDDIT_POLL_INTERVAL REDDIT_USER_AGENT
linkd  CHANNEL_NAME LINKEDIN_API_BASE LINKEDIN_OAUTH_BASE
gitd   CHANNEL_NAME GITHUB_API_URL
teled discd slakd emaid  CHANNEL_NAME
```

The census counts `MustEnv`/`EnvOr`/`Getenv` only; `EnvInt`/`EnvDur` readers
are missed, so it is a lower bound — webd also reads `WEBHOOK_RATE_HOOK`,
`WEBHOOK_RATE_WEB`, `CHAT_ANON_DOS_RPM` (`webd/main.go:48-50`) and slakd
`SLAKD_STALE_SECONDS`, `SLAKD_WATCHDOG_SECONDS`, `SLAKD_STALE_FAIL_LIMIT`
(`slakd/main.go:106-108`).

`compose/compose.go:73-77` already names the class: "a var a daemon reads and
compose never emits is silently dead, and this one left `6/44`'s gate armed
over a counter nothing could scrape". The operator sets the key, generation
drops it, the daemon takes its code default, and every surface reports
healthy.

**The emaid instance of this is FIXED** in the same pass: its five auth keys
(`EMAIL_TRUSTED_AUTHSERV`, `EMAIL_TRUSTED_DOMAINS`, `EMAIL_STRICT_AUTH`,
`EMAIL_UNVERIFIED_SUBJECT_PREFIX`, `EMAIL_VERIFY_DKIM`) were all missing, so
every inbound stayed untrusted and `EMAIL_STRICT_AUTH` could never take
effect — the whole trust model unreachable.

**Not every row is a defect.** `CHANNEL_NAME` is likely delivered by a
mechanism the guard does not model, and a key that is only ever a code default
(`GITHUB_API_URL`, `LINKEDIN_API_BASE`) may be deliberate. Each row needs a
yes or no before the guard can ship green, which is why the guard is parked
rather than committed: a test landed with a thirteen-entry exception list
names its scope by hand and stops covering what moves
([[guard_names_its_scope]]).

- **Severity:** medium — silent, and it already cost `6/44` a dead gate and
  emaid its entire trust configuration
- **Scope:** `compose/compose.go` `daemonKeys`, thirteen daemons
- **Status:** open — the triage is yours; the guard ships once the real set is
  known
- **Related:** [[ant_env_delivery]] is the same class on the agent side

## L423 — a GitHub outage at gitd's boot withdraws `send_text` for the life of the process, and `/health` says ok (2026-10-16, ✅ FIXED 2026-10-17 — `46cb5578`)

`gitd/bot.go:65-67` returns the raw `http.Client.Do` error from `GET /user`, so a
DNS failure, a timeout or a refused connection at boot reaches `verifyToken`
(`gitd/main.go:73-83`) the same way a 401 does. It logs "github token refused",
calls `SetCaps(capsFor(""))`, and nothing calls `start` again: `send_text` stays
withdrawn until the process restarts. `bot.go:56` stores `connected=true` in a
defer that runs on failure too, and `chanlib/handler.go` `handleHealth` reads
only that flag, so `/health` answers 200 for an adapter that cannot post and
has no login to drop its own comments with.

The degraded mode is not the clean refusal the code claims. `gitd/main.go:13-16`
and `gitd/README.md` say a withdrawn `send_text` gives the agent "a clean
Unsupported hint". `chanreg/httpchan.go:150,188` return a plain
`fmt.Errorf("send_text not supported")` — only voice, post, like, delete, pin,
unpin, forward, quote and repost go through `chanlib.Unsupported` — and
`ipc/ipc.go:759` then records the row `pending` and tells the agent "queued: the
router re-sends this message — do NOT send it again". `L411` measured that
state on sloth: 94 cents and ten retries for one comment nobody received.

Before `1f52b893` the same transport error exited the daemon and compose
restarted it, which was the retry; `L419` removed the exit because it cycled the
whole project (`L406`).

- **Severity:** medium — one transient at boot is a silent outage of replies
  until someone restarts gitd
- **Scope:** `gitd/bot.go`, `gitd/main.go`
- **Status:** ✅ FIXED — option (b). `start` classifies its own refusal through
  `statusErr`, the one place gitd turns a status into an error, and
  `verifyToken` withdraws `send_text` on any failure but spawns `reverify`
  only when the failure is NOT `chanlib.ErrPermanent`. `reverify` re-asks
  `/user` every minute until it answers, restores the cap through `SetCaps`
  and returns; a refusal that arrives later ends it too.
  `gitd/main_test.go` `TestVerifyToken_UnreachableGitHubRestoresSendText`
  (a hijacked, closed connection, then a live `/user`) and
  `TestVerifyToken_RefusedTokenDegradesToIngress` (a 401 is probed once and
  never again).
- **Why not (a) or (c):** (c) exits, and a daemon that exits at boot cycles
  the whole compose project under systemd (`L406`) — the reason `L419` removed
  the exit in the first place. (a) leaves `send_text` advertised with the
  login unknown, and `gitd/server.go` drops gitd's own comments at ingest only
  when `Login()` is non-empty, so the agent's own reply re-enters as a human
  event on a public PR. `send_text` therefore stays off for exactly as long as
  the login is unknown.
- **`/health` stays 200 on purpose:** `handleHealth` reads `connected`, and
  `disconnected` deregisters the channel after three probes
  (`specs/5/34-channel-protocol.md`), which would take ingress down too. The
  daemon IS up and ingesting; what is degraded is one verb, and the
  registration — not the health probe — is what says so.

## L425 — emaid's XOAUTH2 refusal is pinned against a fake shaped to the code, not to Google's documented exchange (2026-10-16, open — needs a decision)

Google's XOAUTH2 page (developers.google.com/workspace/gmail/imap/xoauth2-protocol)
ends the IMAP failure with `A01 NO SASL authentication failed` — no bracketed
response code. `permanentAuthFailure` (`emaid/imap.go:119-135`) returns true
only for `AUTHENTICATIONFAILED` or `AUTHORIZATIONFAILED`, so the documented
refusal is retried through the backoff (`imap.go:162-168`), not stopped as
`emaid/xoauth2.go:19-21`, `emaid/README.md` §"Access token expiry" and
`TestIMAPAuthXOAUTH2RefusalIsPermanent` state. The test's fake server sends
`NO [AUTHENTICATIONFAILED]` (`emaid/xoauth2_test.go:107`), which is the shape
the code accepts, not the shape Google documents. go-imap writes the empty
error-challenge response as `=`; Google's page shows a bare line; the test
accepts either (`xoauth2_test.go:158-159`), so it passes whichever the client
sends.

SMTP: `emaid/server.go:183` returns `sendReply`'s plain error, so an expired
token's 535 is a retryable failure to routd — ten attempts, and the agent told
"queued" — where the README said "returns the server's 535 to the sender.
Nothing retries".

No instance runs emaid, so what Gmail actually answers is unmeasured; the
encoder itself matches Google's worked example byte for byte
(`TestXOAUTH2ResponseExact`, recomputed with `printf | base64`).

- **Severity:** low — a daemon no instance runs; medium the day one does
- **Scope:** `emaid/imap.go:119-135`, `emaid/server.go:183`, `emaid/xoauth2_test.go`
- **Options:** (a) treat a bare `NO` that follows an XOAUTH2 error challenge as
  permanent, and wrap the SMTP 535 in `chanlib.ErrPermanent`; (b) measure
  against a Gmail mailbox first and pin the fake to what it sends.
- **Default if nothing is decided:** the README states the code's behaviour
  (retried on a bare NO; the 535 retried by routd) — done in this pass.
- **Status:** open — needs a decision

## L426 — a failed thread-root lookup holds the dedup claim as if a comment may have been posted (2026-10-16, ✅ FIXED 2026-10-17 — `cfee9ad9`)

`gitd/bot.go:155-160` `threadRoot` resolves an inline reply's root through the
same `call` as the POST. `call` leaves a transport error unclassified on
purpose: a lost response to a POST may have created the comment, so releasing
the claim could post it twice (`L413`). A GET creates nothing, so the same
treatment on the lookup holds the claim for a reply that was never sent; routd
answers 502, `ClaimInflight` returns `ErrDeliveryUnresolved` on every sweep,
and the row dead-letters after about an hour (`routd/loop.go:364-373`:
30 s doubling to a 1 h cap, measured from the row's own timestamp) having
posted nothing.

- **Severity:** medium — a reply lost to one timeout on a read
- **Scope:** `gitd/bot.go`
- **Status:** ✅ FIXED — a transport error on a GET wraps
  `chanlib.ErrNotDelivered`; only the POST stays unclassified.
  `gitd/bot_test.go` `TestSend_ThreadLookupTimeoutReleasesTheClaim`.
- **Also fixed here, the 403 split `L414` named:** GitHub answers a primary or
  secondary rate limit with 403 as well as 429, and the documented signals are
  a `retry-after` header or `x-ratelimit-remaining: 0`
  (docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api,
  re-read 2026-10-16). `isRateLimited` reads exactly those two, and a 403
  carrying either joins the retryable class instead of dead-lettering.
  `TestCall_RateLimited403IsRetryable` pins all three 403s.

## L427 — gitd records a delivery as accepted before it knows the outcome (2026-10-16, open)

`gitd/server.go:86` `markDelivery` runs before `SendMessage` (`:91`), so
`lastInboundAt` and the dash's last-accepted-delivery advance for a webhook
answered 400 ("routd refused") or 503 (`L415`). The timestamp predates the
range (`989c440c:gitd/server.go:85`).

- **Severity:** low — an operator reads "delivering" on a source whose
  deliveries are refused
- **Scope:** `gitd/server.go:86`
- **Fix:** mark after the 202 branch. no test — ops
- **Status:** open

## L428 — the web design guard passes on a page that carries its own colour, and on a site that loses fifteen sidebars (2026-10-16, open)

`tests/webdocs/design_test.go` detects a `<style` tag and a colour inside a
quoted `style="…"` attribute. It passes on: a named colour (`style="color:
white"`); `background: url(…)`; an unquoted `style=color:red`; an SVG
`fill="#…"`/`stroke="#…"`; `<font color>`; a second `<link rel="stylesheet">`;
`<style/media=all>`; an entity-hidden `#` (`&#35;ff0000`); `oklch(`, `lab(`,
`light-dark(`. `TestWebDocsNavListsEverySection` skips a page with no
`<nav class="docs-nav"` and its floor is 100 against 115 sidebars, so fifteen
sidebars can vanish and the suite stays green (measured on a copy: 15 pass, 16
fail). `hub.css` itself is read by no test: a raw hex outside `:root`, a
gradient and a `border-left` accent — all in `template/web/CLAUDE.md`
§Forbidden — pass. None of these shapes is on the live site today (0 `style=`
attributes, 0 `<svg`, 120/120 pages link `hub.css`).

- **Severity:** low — coverage gap, nothing live
- **Scope:** `tests/webdocs/design_test.go`, `template/web/CLAUDE.md` §Forbidden
- **Fix:** floors at the measured counts less a small slack; named colours and
  the attribute shapes in `colourRe`; a hub.css check for a colour literal
  outside the token blocks. no test — the entry is about the test
- **Status:** open

## L429 — four classes the pages use are defined nowhere, so the elements render unstyled (2026-10-16, open)

`class="docs-updated"` on 114 pages, `group-header` on `reference/stats.html`,
`tbl` on `reference/packages.html` and `docs-main` on `components/gitd.html`
have no rule in `template/web/pub/arizuko/assets/hub.css`. All four predate the
conversion (`989c440c`). `docs-updated` is part of the shell in
`template/web/CLAUDE.md`, so it may be a deliberate unstyled hook; the other
three read as leftovers.

- **Severity:** low
- **Scope:** `template/web/pub/arizuko/assets/hub.css`, the four pages
- **Fix:** define or drop each; a test that every `class="…"` token resolves to
  a hub.css rule would hold it. no test — docs
- **Status:** open

## L430 — a verdict handled at ingress skips message-id dedup and the transcript, and the inbound request waits on the ack's send (2026-10-16, open — needs a decision)

`5c096b17` runs `/approve`, `/reject` and `/always` in `handleMessages`
(`routd/server.go:511`, `routd/steer.go:156`) before `PutMessage`, so the held
call is answered while the agent worker is busy (`L271`'s deadlock). Three
things moved with it. `PutMessage` is where a duplicate id is a no-op
(`store/messages.go:234`), so a redelivered verdict — the adapter's second
attempt (`chanlib/chanlib.go:268`) or the retention sweep — runs again and
acks "Failed: pending action … already resolved (by someone else)"; the state
stays right because the conditional write refuses, the operator reads a wrong
message. The verdict row is never stored, so it is absent from `find_messages`
and the transcript (the pending_actions row keeps reviewer, time and note). And
`l.ack` (`routd/steer.go:32`) calls `deliver.Send` synchronously inside the
inbound handler, so the adapter's 10 s client timeout to routd
(`chanlib/chanlib.go:98`) now covers an outbound send with a 30 s timeout of its
own (`chanreg/httpchan.go:118`). `TestApprovalIngressWhileChatIsBusy` uses a nil
deliverer and cannot see the ack or the timing. The set of people who may
answer is unchanged — `resolveHoldInto` authorizes on the held row's folder —
while the set of chats widens to unrouted ones.

- **Severity:** medium — a correct state with a misleading ack, and a request
  that can time out on the way to it
- **Scope:** `routd/server.go:507-513`, `routd/steer.go:150-170`
- **Options:** (a) `PutMessage` first, then the verdict, so the id dedups and
  the transcript keeps the row; (b) ack asynchronously; (c) keep the shape and
  state it in `specs/5/E-routd.md`.
- **Default if nothing is decided:** the shape stands as shipped.
- **Status:** open — needs a decision; no test — design

## L431 — `inject_message` lets an agent write a `/approve` as any sender, so a folder holding that grant can answer its own held call (2026-10-16, open — needs sign-off)

`ipc/ipc.go:1918` takes `sender` from the agent and `routd/mcp.go:113-116`
stores the row with it. A row `Sender: telegram:user/<admin>`, content
`/approve <id>`, reaches `handleCommand` at dispatch — the one path verdicts
still take there after `5c096b17` consumed chat verdicts at ingress — and
`resolveHoldInto` authorizes the forged sender against the held row's folder.
Measured on an overlay test by the review sub: `status="approved"
reviewed_by="telegram:user/operator"`. The same sender field authorizes
`/root`, `/gate` and `/invite`. No default grant carries `inject_message`
(`store/seed.go` seeds none); the grant is the only control. Predates the range.

- **Severity:** high where the grant is held, none where it is not — a
  hold is the gate that stops an agent acting alone, and this reopens it
- **Scope:** `routd/mcp.go:113-116`, `routd/steer.go` `handleCommand`
- **Options:** (a) mark injected rows (a `Sender` the agent cannot forge, e.g.
  the `agent:` principal, or an attribute) and have `handleCommand` skip them;
  (b) drop the `sender` parameter from `inject_message`.
- **Default if nothing is decided:** no change; do not grant `inject_message`
  to a folder that also holds admin-answerable holds.
- **Status:** open — needs sign-off; a test asserting the injected `/approve`
  leaves the hold `held` is the pin, written with the fix

## L432 — the declared session id holds for claude only; codex and pi start fresh under it and `submit_turn` overwrites it (2026-10-16, open)

`50fe8823` mints the topic's session id in routd before the run and ant's
claude path creates the SDK session under it (`ant/src/claude.ts:493`,
SDK 0.3.278 `sessionId`). codex runs `exec resume <id>` (`ant/src/codex.ts:41,80`)
and pi `--session <id>` (`ant/src/pi.ts:52`) on an id that names no thread, so
each reports a resume miss, starts fresh, and `recordTurnResult`
(`routd/turns.go:236`) overwrites the stored id with the harness's own. A
codex or pi turn killed before `submit_turn` still loses its thread, and the
session_log records routd's id, not the harness's. `50fe8823`'s message says
so; ARCHITECTURE.md's "the harness opens its session under that id" did not
(corrected in this pass). Two smaller facts from the same change: the mint
(`routd/dispatch.go:742`) is an unconditional upsert, so two dispatches that
read "" together each mint and the last write wins — narrow under the
per-folder queue; and a `PutSession` failure before the run surfaces as a WARN
plus a queue ERROR and a re-feed, nothing in the chat.

- **Severity:** low — continuity on codex and pi is what it was before the change
- **Scope:** `ant/src/codex.ts`, `ant/src/pi.ts`, `routd/dispatch.go:735-747`
- **Status:** open; no test — the harnesses are not run in the suite

## L433 — nothing fails when an isolated turn's session is written back to the chat, or when ant stops passing the declared id (2026-10-16, open)

`routd/turns.go:236` guards the write-back with `&& !isolated` (`L54`: a cron's
throwaway transcript handed to the chat). Removing the guard passes the whole
routd suite (`go test -overlay … ./routd/ -count=1 -short`, 495 s, ok); the
three `persistSessionBackstop` tests that covered the backstop half went with
the function in `50fe8823` and `routd/isolated_session_test.go` exercises
`dispatchRun` only. On the ant side, deleting `sessionId: session.sessionId`
(`ant/src/claude.ts:493`) fails no test (`bun test src/`, same result as the
unmutated copy).

- **Severity:** medium — the two halves of the session contract this release
  rests on have no guard
- **Scope:** `routd/turns_test.go` (a `recordTurnResult` case with an isolated
  trigger asserting `SessionID(folder, topic)` unchanged), `ant/src/claude.test.ts`
- **Status:** open — tests to write; this entry is the pin until they exist

## L413 — a GitHub timeout releases the dedup claim, so the retry posts the comment twice (2026-10-16, ✅ FIXED 2026-10-16 — `80b71cb6`)

`gitd/bot.go:126` wraps EVERY `http.Client.Do` error — including a 15-second
timeout fired after the request body went out — in `chanlib.ErrNotDelivered`.
`chanlib/dedup.go:90` reads that sentinel as proof nothing was posted and
releases the claim. routd sees 502, keeps the row `pending`, and the sweep
(`routd/loop.go:396`) re-sends the same key, which wins a fresh claim and
POSTs again.

teled and discd deliberately leave a transport error unclassified for exactly
this reason: `teled/bot_test.go:437` and `discd/bot.go:735` both say "a lost
response may have sent the message". gitd is the one adapter that asserts it.

- **Severity:** high — a duplicate comment on a public PR, from a timeout
- **Scope:** `gitd/bot.go:126`
- **Status:** ✅ FIXED — only a response GitHub sent is classified; a transport
  error returns bare, so the claim stays inflight and routd dead-letters.
  `gitd/bot_test.go` `TestSend_TransportFailureIsUnclassified` (a handler that
  outlives the client timeout).
- **Fix:** wrap only `>=500` and `429` in `ErrNotDelivered`; return a transport
  error bare so the claim stays inflight and routd dead-letters visibly.

## L414 — gitd's permanent refusal goes on the wire as 502, so it is retried ten times and the agent is told it is queued (2026-10-16, ✅ FIXED 2026-10-17 — `3bd0e193`)

`gitd/bot.go:147` returns `ErrPermanent` for 401, 403, 404, 410 and 422 — a
revoked token, a deleted or foreign review root. `chanlib/handler.go:490-520`
has branches for Unsupported and `ErrInvalidRequest` only, so `ErrPermanent`
falls through to 502, and `chanreg/httpchan.go:60` maps 502 to a transient
error.

Two consequences. `routd/loop.go:408` spends all 10 attempts on a doubling
backoff, about an hour (`routd/loop.go:364`: 30 s doubling to a 1 h cap,
measured from the row's own timestamp), before dead-lettering. On the MCP path
`ipc/ipc.go:750` records `pending` and tells the agent "queued: the router
re-sends this message — do NOT send it again" about a message that will never
send. gitd is the only adapter returning `ErrPermanent` from `Send`; the
others return `ErrInvalidRequest`, which maps to 400.

- **Severity:** high — the agent is told the opposite of what happened
- **Scope:** `chanlib/handler.go` writeBotResult
- **Status:** ✅ FIXED — `writeBotResult` answers 400 for `ErrPermanent` beside
  `ErrInvalidRequest`. `chanlib/handler_test.go`
  `TestHandlerSendPermanentRefusal`; `specs/5/34-channel-protocol.md` now
  names the sentinel and its status.
- **What it changes for the other adapters: nothing, measured.** gitd is the
  only producer — `git ls-files '*.go' | xargs grep -n ErrPermanent` returns,
  outside `chanlib`'s declaration and `chanreg`'s routd-side alias, only
  `gitd/bot.go` (and tests). bskyd, discd, emaid, linkd, reditd, slakd and
  teled return `ErrInvalidRequest`, `ErrNotDelivered` or `ErrUnsupported` and
  never reach the new branch; whapd answers routd over HTTP of its own and
  does not use `writeBotResult`. The whole adapter set was run green at
  `3bd0e193`.
- **The 403 split this entry required landed first**, in `cfee9ad9` (`L426`),
  so the 400 mapping cannot dead-letter a rate-limited reply.

## L415 — a webhook is answered 202 "retained" when the retention write itself failed (2026-10-16, ✅ FIXED 2026-10-16 — `27fc7ec2`)

`chanlib/chanlib.go:231` returns only routd's error; `chanlib/inbound.go:101`
logs "inbound retain failed; the message is lost" and swallows it.
`gitd/server.go:96` then answers GitHub 202 `{"retained": true}`. GitHub
records a success and never redelivers.

The trigger is one event, not two: routd and the adapter store ride the same
store, so extenddb being unavailable fails both at once. gitd is the one
adapter whose HTTP response IS the platform's ack, so a 5xx here would be
correct and would leave a redeliver button in GitHub's UI.

- **Severity:** high — the comment reaches no turn and every surface says fine
- **Scope:** `chanlib/inbound.go`, `gitd/server.go:96`
- **Status:** ✅ FIXED — `retainInbound` reports whether anything still holds
  the message and `SendMessage` wraps the cause in `chanlib.ErrInboundLost`
  when nothing does; gitd answers 503 on it. `chanlib/inbound_test.go`
  `TestInboundNeitherDeliveredNorRetainedIsReportedLost` and
  `gitd/server_test.go` `TestWebhook_LostWhenRoutdAndRetentionBothFail`.
- **Fix:** `retainInbound` returns its outcome; answer 5xx when neither routd
  nor the retention store took the message.

## L416 — while the agent is engaged on a PR, every observe-only event becomes a turn (2026-10-16, open — needs a decision)

`routd/loop.go:640` checks engagement BEFORE `rt.Mode == "observe"`, and
`DefaultEngagementTTL` is 30 minutes (`routd/server.go:129`). Any reply renews
it on `(chat, pr/<n>)`. Inside that window a `synchronize`, `labeled`,
`review_requested`, `closed` or empty-body approval dispatches a turn with
content like "kronael synchronize PR #278", as if a person had spoken.

Spec `6/27` names the engagement mechanism as intended, but it was written
before gitd gave the agent a 30-minute wake-up on every state change.

**Done 2026-10-16: being asked.** `gitd/event.go` `mapEvent` sets `verb=mention`
for a review requested from gitd's own login (`requested_reviewer.login`), for an
issue or PR assigned to it (`assignee.login`), and for an `@<login>` of the bot in
a comment, review, review comment, or PR or issue body (a longer login such as
`@<login>-helper` is not a mention). The same events aimed at another account,
and `synchronize`, `labeled`, `closed`, stay ambient. routd does not rewrite the
verb: `routd/server.go` only promotes to `mention`, and the seeded rows already
route `verb=mention` to the folder. The window above is unchanged and still
applies to every ambient event.

- **Severity:** medium — the agent comments on a push or a label, one turn each
- **Status:** open — which ambient events may promote inside the window is yours
  to decide; being asked no longer depends on it

## L417 — an App-kind source lets the agent send to any repository on GitHub (2026-10-16, open — needs sign-off)

`store/github_sources.go:142,146` seed `chat_jid=github:*/*/pr/*` and the
`issue/*` twin. `ipc/ipc.go:935` authorizes a `send` or `reply` JID through
`DefaultFolderForJID` (`store/groups.go:374`), which is plain route
resolution, so `github:victim/repo/issue/1` resolves to the folder and gitd
posts with the instance token. A PAT can comment on any public repository.

The realistic trigger is prompt injection: the comment body that starts the
turn is untrusted text from any GitHub user. The `*/*` scope is declared by
the SEED, not by the operator.

- **Severity:** high — an injected instruction reaches any repo the token sees
- **Scope:** `store/github_sources.go:142`, `ipc/ipc.go:935`
- **Status:** open — narrowing the seed changes the enrolment contract
- **Fix (not chosen):** seed App routes per installed repository, or refuse a
  JID no inbound has ever been stored for.

## L418 — an edited, deleted or dismissed comment is dropped as a duplicate and nothing says so (2026-10-16, open)

`gitd/event.go:296,309,325` give every action on a comment the OBJECT id, so
`issue_comment.edited` arrives as the same `gh-c<id>` as the create.
`store/messages.go:236` makes a duplicate id a no-op, `GuardedBatch` returns
`(false, nil)`, routd answers 200 and gitd answers 200. No row, no log line.

Even stored, the mapping carries "login edited a comment" without the new
body (`gitd/event.go:302`). A person who edits a comment to add an instruction gets
nothing, and a deleted comment stays in the agent's context. No test covers
these actions.

- **Severity:** medium — silent, and editing a comment is ordinary behaviour
- **Scope:** `gitd/event.go:296`
- **Status:** open
- **Fix:** the delivery id keys the row, the object id keys the thread — the
  shape `6/27`'s proposal section already prescribes.

## L419 — a token GitHub refuses at boot restart-loops the whole instance (2026-10-16, ✅ FIXED 2026-10-16 — `1f52b893`)

`gitd/main.go:54` returns the `GET /user` error and `chanlib/run.go:65`
exits 1. `L406` measured that a daemon exiting at boot cycles the whole
compose project under systemd. A revoked PAT, or GitHub being unreachable
during a restart, therefore takes down every daemon on the instance for a
credential that gates ONE verb.

`rc.Register` (`run.go:57`) already ran, so routd holds a `send_text`
registration for a dead adapter until the heartbeat expires.

- **Severity:** high — instance-wide outage from one optional credential
- **Scope:** `gitd/main.go:54`
- **Status:** ✅ FIXED — `verifyToken` logs at ERROR, keeps ingress, and
  withdraws `send_text` through `RouterClient.SetCaps`, which re-registers from
  the saved parameters the heartbeat refreshes from. `gitd/main_test.go`
  `TestVerifyToken_RefusedTokenDegradesToIngress` and
  `TestVerifyToken_GoodTokenKeepsRegistration`.
- **Fix:** degrade to ingress-only with `send_text` unadvertised, loud at
  ERROR. Same shape as `L151`.

## L420 — the agent's own comments re-enter as human events when the connector identity differs from gitd's (2026-10-16, open)

The only loop guard is `gitd/server.go:83-84`, `sender.login` equal to gitd's own
token login, plus the `type == "Bot"` check at `gitd/event.go:213`, which a PAT
user is not. A folder whose `GH_TOKEN` resolves to a DIFFERENT account than
gitd's `GITHUB_TOKEN` gets a turn for every comment the agent posts through
`gh` or the connector, and answering it posts again.

On sloth today all four secrets are one identity (`L410`), so no loop exists
there. The guard holds by operational coincidence, not by rule.

- **Severity:** high when it triggers — an unbounded self-reply loop that bills
- **Scope:** `gitd/server.go:83-84`
- **Status:** open
- **Fix:** drop on a SET of known bot logins (gitd's plus each folder's
  connector identity), or record outbound comment ids and drop their echo.

## L412 — an answer to an inline review comment lands in the conversation tab, not under the code line (2026-10-16, ✅ FIXED 2026-10-16 — `e60b890b`)

MEASURED on `vumilan/morha` PR 277, 2026-10-16. An inline review comment IS
ingested — `gitd/event.go` maps `pull_request_review_comment` — and the agent
answered it correctly:

```
17:00:14 inline review comment on BUGS.md:2358 (kronael)
17:01:09 dispatch run topic=pr/277 turn_id=gh-c4297154186
17:01:40 sloth-goth: "Yes — I see it, and it is an inline review comment,
         not an issue comment."
```

The answer was right and in the WRONG PLACE: the PR conversation tab, detached
from the code line that prompted it, while `/pulls/277/comments` showed only the
human's comment. A reviewer reading the diff never saw it. `gitd/bot.go` always
posted to `/repos/<o>/<r>/issues/<n>/comments`.

**The shape that shipped.** `SendRequest.ReplyTo` is the address, and gitd reads
it. Review comments carry their own id namespace, `gh-rc<id>`, inbound and
outbound: GitHub keeps review comments and issue comments in separate id spaces
behind separate endpoints, and a bare number cannot say which one it names. A
`gh-rc` reply target on a `pr` JID posts to `pulls/<n>/comments/<root>/replies`;
everything else keeps the issue-comments endpoint. The topic stays `pr/<n>` —
one session per pull request.

`Send` resolves the thread root with a GET before posting, which costs one extra
API call per inline reply. It is not avoidable: GitHub takes only a top-level
review comment as a reply target ("replies to replies are not supported"), while
the anchor routd hands back is `last.ID`, the comment that triggered the turn —
a reply as often as the root.

**TRIED AND REVERTED: encoding the thread root in the topic.** `17c48337` made
the topic `pr/<n>/review/<root>` and had `Send` parse the root back out of it.
A topic is a SESSION key — which conversation this belongs to — not an ADDRESS.
Overloading it cost two things at once:

- every inline thread became its own session, so one submitted review of N
  inline comments cost N+1 turns in N sessions that each opened blind;
- repairing that took a second commit, `c8755c15`, which taught
  `routd/dispatch.go` to fork a topic from its nearest `/`-prefix holding a
  session — giving `/` a global meaning in the topic namespace for EVERY
  adapter, to serve one channel's addressing need.

Both are reverted (`3420d45e`, `34533e4e`). Do not put an address in a session
key; the threading primitive already existed.

- **Severity:** medium — code review is the main use and the answer missed its
  target; nothing was lost, it was misplaced
- **Scope:** `gitd/event.go` mapEvent, `gitd/bot.go` Send
- **Affected:** any instance; the behaviour was in the code
- **Status:** ✅ FIXED — `gitd/bot_test.go`
  `TestSend_InlineReviewReplyTargetsThreadRoot` (root and mid-thread anchors)
  and `TestSend_OrdinaryCommentTargetsIssueComments`
- **Correction:** this entry first said the reply was DISCARDED. It was not. I
  checked `/pulls/277/comments` and never `/issues/277/comments`, and read the
  wrong place as nowhere — the same error shape as [[attempt_line_is_not_work]].

## L421 — routd never hands an adapter the inbound message's own thread identity (2026-10-16, open)

Found while fixing `L412`. Two sites, both real, both left alone because the
repair is a cross-adapter contract change:

- `routd/turns.go:98` `replyThreadRoot` returns `""` whenever the turn has a
  topic, so a topic-carrying channel never sees `ThreadRoot` at all.
- `deliverRow` and `mcp.go` `mcpDeliver` fill `SendRequest.ThreadID` from the
  turn's TOPIC, not from the thread the inbound message arrived on.

The second is not a bug for teled, discd or slakd: their topic IS the native
thread id (telegram `message_thread_id`, a discord thread channel, a slack
`thread_ts`), so topic and thread identity are the same string by construction.
gitd is the first channel where a conversation has a session key and a thread
address that are different things, which is what exposed the gap.

Changing either site changes what all four adapters receive, so it needs a
decision rather than a patch. `InboundMsg` has no thread field to carry the
answer today, so the options are: add one and fill `ThreadID` from it, or accept
that a channel whose thread address differs from its topic addresses replies
through `ReplyTo`, as gitd now does.

- **Severity:** low — nothing is broken today; it is a contract that only one
  channel has needed
- **Scope:** `routd/turns.go` replyThreadRoot, `routd/turns.go` deliverRow,
  `routd/mcp.go` mcpDeliver, `chanlib/handler.go` SendRequest
- **Status:** open — needs the owner; a redesign, per the standing policy

## L410 — gitd has no GitHub token on sloth, so every agent reply to a PR is written, paid for and then thrown away (2026-10-16, ✅ FIXED 2026-10-16 — the round trip is still unmeasured)

MEASURED on sloth, 2026-10-16. The GitHub delivery of `L409` ran its turn and
the reply never reached the pull request:

```
13:48:26 ERROR deliver send failed  jid=github:vumilan/morha/pr/278
         err="channel github: send_text not supported"
13:48:26 INFO  agent self-reported model usage  model=claude-opus-4-8
         input=16 output=1712 cost_cents=94
13:49:05 WARN retry outbound: delivery failed  attempts=1  max=10
         … retried to attempts=10
```

The agent wrote 1712 output tokens, the turn cost 94 cents, and the text was
discarded. `GITHUB_TOKEN` is ABSENT from `arizuko_gitd_sloth`'s environment —
`docker inspect` finds no such key at all, not an empty one.

`capsFor` (`gitd/main.go:18`) is `map[string]bool{"send_text": token != ""}`,
so with no token gitd registers the `github` channel advertising NO verb. The
channel registers successfully at boot and looks healthy; only a delivery
discovers it can say nothing.

**This corrects `L409`.** That entry says "every step of the path is now
proven". Ingress is proven. Egress has never worked on this instance and
cannot until the token exists.

- **Severity:** high — a turn runs, bills and produces nothing a user sees
- **Scope:** `env/gitd.env` on sloth; `gitd/main.go:18` for the advertisement
- **Affected:** sloth measured; any instance whose gitd has no token
**Three doors, and only one of them is gitd's.** `GITHUB_TOKEN` in gitd's own
environment is unset, so gitd is ingress-only and `reply` on a `github:` JID
returns an Unsupported hint (`gitd/README.md` §Reply). That is the door this
entry opened on.

The turn also logged `shell_env:["GH_TOKEN"]` and `gh_write=true`, and I read
that as the agent holding a working token in its shell. It does not. The
operator measured an empty shell — no `GITHUB_TOKEN`, no `GH_TOKEN`,
`ARIZUKO_GH_WRITE` empty — because the broker keeps the value OUTSIDE the
shell. `shell_env` names a grant; it does not prove a value resolved.

The third door is the folder secret the connector actually reads:
`{secret:GH_TOKEN}` resolved through the broker against the FOLDER
(`ipc/connector.go:162,187`), never the daemon env var. `sloth/discord` and
`sloth/morha` each hold `GH_TOKEN` and `GITHUB_TOKEN`, sealed seconds apart on
2026-10-15. A read of morha issues has been proven to work through the broker
from `sloth/discord`, while the pair on `sloth/morha` is believed to be
`sloth-goth`'s, which 404s on the repo. Which identity each pair resolves to
is UNMEASURED and is the next thing to measure.

So the agent took the `reply` verb when its granted door was `gh`. Whether
gitd should hold a token at all, or whether the agent's shell credential is
the only intended reply path, is undecided — `specs/6/27` does not say.

**FIXED 2026-10-16 15:13.** Measured, not assumed: all four folder secrets —
`sloth/discord` and `sloth/morha`, `GH_TOKEN` and `GITHUB_TOKEN` — resolve to
the SAME `ghp_` token, identity `sloth-goth`, and that identity now answers
`200` on `vumilan/morha` with `push triage pull`. The 404 that drove the
earlier invite hunt is gone, so no secret needed copying between folders.

The only gap was gitd's own door. `GITHUB_TOKEN` is set in
`/srv/data/arizuko_sloth/.env` (backup `.env.bak-20261016-gitd`), which
`compose/compose.go:237` copies into the generated `env/gitd.env`. After the
restart gitd logs `github token verified login=sloth-goth`, so `send_text` is
advertised and the self-drop has an identity to compare against.

A probe that reads these secrets must call `SetSecretKeys` first. Mine did not
on its first run, so `store.open()` returned the `v2:` ciphertext unchanged
and every token read as a dead 401 — see [[attempt_line_is_not_work]] for the
same shape of error.

**Still unmeasured:** a comment actually reaching the PR. No delivery has
arrived since the fix, and the pre-fix outbound row spent its 10 retries, so
it will not self-heal. The next inbound comment closes it.

- **Status:** ✅ FIXED — platform verified; the round trip awaits one delivery
- **Fix:** undecided, two doors. Either give gitd its own `GITHUB_TOKEN` so
  `reply` works for every folder, or state that the shell `GH_TOKEN` is the
  reply path and make `reply` on a `github:` JID say so to the agent instead
  of failing at delivery after the turn is paid for.

## L411 — a channel that advertises no send verb still takes the turn, so the cost is paid before anyone checks the reply can be delivered (2026-10-16, open — proposal, needs sign-off)

Separated from `L410`, which is the missing token. This is the platform half:
nothing between the inbound delivery and the guest boot asks whether the
channel that must carry the answer can carry one.

The capability is known at registration. `routd` logs `channel registered
name=github prefixes=["github:"]` with the verb map in hand, and gitd's own
`capsFor` already decides `send_text` from the token. The check happens
instead at `chanreg/httpchan.go:150`, after the agent has run.

On sloth that cost 94 cents and ten retries for one comment nobody received.
An instance with a dead channel credential pays that on every inbound message.

Two shapes, neither chosen:

1. Refuse the turn at dispatch when the reply channel advertises no
   `send_text`, and say so on the inbound path, loudly. Cheapest, and it
   turns an invisible drop into a visible refusal.
2. Let the turn run but mark the outbound undeliverable before the retries
   start, so the operator sees one error and not ten.

Shape 1 changes what `routd` does with an inbound message, so it is a contract
change and needs sign-off before anyone writes it.

- **Severity:** medium — it wastes a turn's cost and hides a dead credential
- **Scope:** `routd/dispatch.go`, `chanreg`
- **Affected:** every channel whose verbs depend on a credential
- **Status:** open — proposal, needs sign-off
- **Related:** `L410` is the live instance of it; `L151` is the same silence
  for an adapter whose credential is dead

## L409 — a plain repository webhook posted to an App source's path is refused, and no end-to-end GitHub ingress has ever been measured (2026-10-16, open — TEMPORARY BYPASS SHIPPED, the real fix is next week)

MEASURED on sloth. A comment was posted on `https://github.com/vumilan/morha/pull/278`
(`issuecomment-6215829896`). GitHub delivered it, proxyd passed it, and gitd
answered 400:

```
POST /github/app/sloth → 400
gitd: "github webhook: rejected" source=app/sloth event=issue_comment
      err="not an App delivery: no installation in payload"
```

The 400 and not a 401 is the material fact: the signature verified, so the
repository holds the `app/sloth` secret. `checkSource` then refused the body
because an App-kind source demands an `installation` (`gitd/event.go`).

Two sources exist for sloth (`arizuko get sloth github_sources`):

| source | path | kind |
| --- | --- | --- |
| `app/sloth` | `/github/app/sloth` | App — demands an installation |
| `repo/vumilan/morha` | `/github/repo/vumilan/morha` | repo — binds on owner/login |

The repository posts to the first. `last_inbound_at` is `0`: gitd has accepted
no delivery since it was enabled on 2026-10-14, so the whole ingress path —
proxyd route, secret unseal, JID, route match, turn — is unproven in production.

**The operator fix needs repository admin.** Changing the webhook URL to
`https://sloth.quantblue.tech/github/repo/vumilan/morha` costs nothing and
needs no code, but `kronael` has push and not admin on `vumilan/morha`.

**The bypass, shipped to unblock the end-to-end measurement.**
`GITHUB_APP_ACCEPTS_REPO=1` makes an App-kind source also accept a delivery
with no installation. The secret still has to verify, so the sender holds the
credential. What it gives up is the binding: an App source no longer proves the
delivery belongs to an installation the App owns, so any repository holding
that secret can speak on that path. A repo-kind source keeps its own binding —
this widens nothing but the App path, and only when the flag is set.

`TestMapEvent_AppAcceptsRepoWhenFlagged` pins both halves, refused with the
flag off and accepted with it on. The test fails when the guard is hard-coded
either way.

**The flag is set in `/srv/data/arizuko_sloth/.env`.**
`template/services/gitd.yml:19` passes `GITHUB_APP_ACCEPTS_REPO:
'${GITHUB_APP_ACCEPTS_REPO:-}'` to the container and defaults it to empty, so an
instance carries the bypass only when its own `.env` sets it. The undo is
removing it from `.env`.

- **Severity:** medium — the bypass narrows an authorization check on one
  instance behind an explicit flag; leaving it set past the test is the risk
- **Scope:** `gitd/event.go`, `template/services/gitd.yml`, `.env` on sloth
- **Affected:** sloth only
- **Status:** open — the bypass is LIVE on sloth and the path is PROVEN; the
  decision is which permanent shape wins, and the flag comes out when it lands

**MEASURED END TO END, 2026-10-16 13:44:55Z.** A second comment on the same PR
(`issuecomment-6216852106`) with the flag set:

```
POST /github/app/sloth → 200 (44ms)
routd: dispatch run folder=sloth/morha topic=pr/278 turn_id=gh-c6216852106
       chat_jid=github:vumilan/morha/pr/278 trigger=github:kronael
       egress=true gh_write=true shell_env=[GH_TOKEN]
```

This is the first GitHub delivery arizuko has accepted. Every INGRESS step is
proven: the proxyd route, the signature, the secret unseal, the JID, the topic,
the route match, the folder, and the capability resolution. The reply is NOT:
gitd holds no token, so the answer this turn wrote was discarded. See `L410`.

The same turn_id dispatched twice, 13:44:55 and 13:44:59, with no restart
between them — a clean observation for `L408`, whose own reproduction is
polluted.

The flag is set in `/srv/data/arizuko_sloth/.env` and reaches the container
through `template/services/gitd.yml`, which defaults it to empty. The generated
`env/gitd.env` is rewritten on EVERY restart, not only on an explicit
`generate`, so it could not hold the flag.
- **Fix (the two candidates, neither chosen):**
  1. The operator repoints the repository webhook at its repo-kind path and the
     flag is deleted. No code survives.
  2. The receiver stops making the path's KIND decide what a payload must
     carry, and resolves the source by what the payload names. That changes the
     containment contract `6/27` states, so it is a redesign and needs sign-off.

## L406 — enabling a store-reading daemon without a store identity restart-loops the whole instance (2026-10-14, open — the mint is an operator action)

MEASURED on sloth. `gitd` was enabled at 17:16 and no store identity was minted
for it, so it exits at boot with
`dynstore: endpoint "https://extenddb:18443" takes an access key and a secret`.
Its exit ends the `docker compose up` that systemd supervises, systemd restarts
the unit, and the instance cycles: `NRestarts=2`, two containers up out of
fifteen, `systemctl is-active` stuck on `activating`.

`env/gitd.env` carries `DYNAMODB_URL`, `AWS_REGION`, `STORE_CA` and
`SECRETS_KEY` but neither `AWS_ACCESS_KEY_ID` nor `AWS_SECRET_ACCESS_KEY`;
`dashd.env` carries all three. `gitd` IS listed in `compose.daemonKeys` and in
the set that reaches the store, so nothing is missing from the code —
`mergeStoreCredentials` reads a key out of the daemon's own env file and there
was none to read.

**The guard already names it, at the right moment and in the right words.**
`arizuko generate sloth` reports `store enrolment is partial: these daemons
reach the store and hold no key... missing="[cmd/arizuko gitd]" enrolled=18
need=16`. It does NOT exit 1 — `warnPartialEnrolment` only logs
(`compose/compose.go:1015`) and `cmdGenerate` dies solely on `Generate`'s own
error (`cmd/arizuko/main.go:162`). So the warning scrolls past and the enable
proceeds, and the fault reaches a live stack as an outage rather than a refusal.

(The "exits 1" above was my own misreading, and it reached README and INSTALL
before a docs pass caught it. Corrected 2026-10-15.)

Minting the identity is `L319`: arizuko has no enrolment subcommand, so every
key on this fleet was created by hand against the store server, and whoever
enabled `gitd` had no command to run.

- **Severity:** high — a package enable takes the whole instance down, and the
  cause reads as a store outage rather than a missing key
- **Scope:** `cmd/arizuko/packages.go`, `compose/compose.go`
- **Affected:** sloth, measured; any instance enabling a store-reading package
- **Status:** fixed for sloth 2026-10-15 — gitd holds a key and runs; the
  REFUSAL is still open, and is the platform half
- **Fix (proposal):** `packages enable` refuses a daemon that reaches the store
  and holds no key, naming the same thing `generate` already names. The check
  exists; it runs one step too late to prevent anything.

**Recovery, for the record:** `arizuko packages sloth remove gitd` then
`generate` then restart. All fifteen containers healthy. The morha GitHub tools
are unaffected — that connector is `transport = "rest"` straight to
`api.github.com`, and `gitd` is only the inbound receiver.

## ✅ FIXED 2026-10-16 L407 — a failed turn reports the LAST 200 bytes of the agent's output, so the sentence naming the cause is cut off mid-word (2026-10-14)

MEASURED on sloth, twice today. The error routd recorded and the operator read
begins mid-word:

```
Guest exited with code 78: imit · ask your admin to raise it at
claude.ai/admin-settings/usage · your weekly limit resets Oct 18, 11am (UTC)
```

`runed/qemu.go:483-486` keeps `tail[len(tail)-200:]`. An agent that fails prints
its reason FIRST and then whatever follows, so the 200 bytes kept are the end of
the message and the discarded bytes are the diagnosis. Here the lost text is
whatever named the limit and whose account holds it — the only part that tells an
operator what to do.

This cost real time today: with the first line gone I read "weekly limit" and
asserted a shared quota between instances. The two instances hold different
tokens (`sha256` prefixes `772560947cb9` and `c3ffbf8a54dc`), so that conclusion
was wrong, and the sentence that would have settled it was the part thrown away.

- **Severity:** medium — every failed turn reports a mangled cause, and the
  mangling points the reader at the wrong one
- **Scope:** `runed/qemu.go`
- **Affected:** every instance, every non-zero guest exit
- **Status:** fixed
- **Fix:** `failureReason` keeps the front under the same 200-byte bound and
  marks the cut with an ellipsis, so the reason arrives whole and a long log
  still cannot fill an audit row. `TestFailureReasonIsTheLastLineFront` fails on the
  old behaviour with `"ck frame\n..."` — the same mid-word cut, from the other
  end of the same output.

`068d3170` kept the front of the whole buffer and lost the reason entirely
(ant's startup lines come first); `3d4f0302` keeps the last line's first 200
bytes. `runed/qemu_test.go` pins ant's order.

## L408 — one turn is dispatched up to seven times, seconds apart (2026-10-14, NOT A DEFECT — re-measured twice; the repeats are Busy refusals)

MEASURED on sloth, 2026-10-14 18:28-18:29, after a restart on a current image.
Turn `1559996454418907247` on `sloth/discord`:

```
dispatch run             × 7   (18:28:59, 18:29:05, 18:29:14, …)
dispatch capabilities    × 7
agent self-reported use  × 2
distinct guest handles for sloth/discord: 4
```

One trigger, one turn id, seven dispatches within fifteen seconds, and four
guests actually spawned. Two reached the agent far enough to report usage, so at
least two did the same work and can both reply.

**It is not steering.** Steering reuses a turn id by design, and the log carries
no steer line at all for the folder in that window.

**It is not the deadline mismatch of the earlier class.** That one fires minutes
apart; these are seconds. `RUNED_RUN_TIMEOUT` is unset in both `routd` and
`runed` on sloth, so both sit on their code defaults.

Four other turn ids in the same window repeat 5-7 times each, including three
`auto-migrate` turns, so this is not specific to one chat.

The guard the code already names is `SetTurnState(turnID, "done")` on a steered
turn, whose own comment says "a re-fed poll / recoverPending re-spawns it and
duplicates the output" (`routd/dispatch.go:468-472`). Something on the ordinary
path leaves the turn re-dispatchable.

- **Severity:** high — duplicate replies in chat, and several VMs doing one turn's
  work, which is the "slow and broken" an operator sees
- **Scope:** `routd/dispatch.go`, the poll loop's claim on a running turn
- **Affected:** sloth measured; krons runs the same code
- **Status:** NOT A DEFECT — one turn ran once; see the correction below
- **Fix:** none needed. Two real findings it exposed are `L410` and `L411`.

**The measurement is mine and it is polluted.** The reaping line says "reaping a
guest left by a previous runed", and I had restarted sloth four times in the
thirty minutes I was measuring. Each restart kills runed mid-turn, orphans its
guest, and the poll loop re-feeds the unfinished turn — so most of those seven
dispatches are my own restarts, not a dispatch defect. What survives is ONE real
`turn deadline reached, stopping guest` on `sloth/discord` at 18:31:54, which is
not evidence of repeated dispatch.

Re-measure on an instance nobody is restarting before taking this further. The
related finding that IS real is the session write-back, fixed separately.

**RE-MEASURED CLEAN, 2026-10-16.** sloth last restarted at 13:43:50. Every
dispatch below falls after it, so no restart orphans a guest and no re-feed is
mine. Turn `gh-c6216852106`, the GitHub delivery of `L409`:

```
13:44:55  13:44:59  13:45:09  13:45:27  13:46:02  13:47:03  13:48:05
   +4s       +10s      +18s      +35s      +61s      +62s
```

Seven dispatches of one turn id. The gap doubles and then caps near 60
seconds, which is the shape of a retry loop, not of a repeated trigger. Two
auto-migrate turns in the same window repeat 9 and 8 times, so the defect is
not specific to the GitHub path.

**CORRECTED the same day. The seven dispatches are six refusals and one run.**
`sloth/morha` booted exactly ONE guest, at 13:48:05 on the seventh dispatch,
and produced exactly ONE `agent self-reported model usage` line. Dispatches 1
to 6 got `Busy` from runed and spawned nothing.

runed caps concurrent agent runs at 2 (`runed/manager.go:244`, `activeCount >=
m.maxRun`) and answers `Busy`. `routd/dispatch.go:458` returns on that outcome
without recording a run id, and `noteBusy` (`routd/dispatch.go:255`) doubles
the wait from a 4-second base to a 60-second cap. That is the 4, 10, 18, 35,
61, 62 sequence exactly. The auto-migrate burst that the 13:43:50 restart
queued held both slots until 13:45:58.

A `dispatch run` line therefore does NOT mean a turn ran. The entry counted
log lines and called it duplicate execution, twice: once on 2026-10-14
(retracted in `28157de5`) and once on 2026-10-16. The guest count is the
measurement that settles it.

## L398 — `make test`, the documented dev loop, cannot pass: two packages exceed Go's default 10-minute timeout (2026-10-07, open — the ten minutes is unexplained)

Measured 2026-10-07, a full `go test ./... -count=1 -short` on this host:

```
FAIL github.com/kronael/arizuko/routd  601.621s
FAIL github.com/kronael/arizuko/store  601.680s
panic: test timed out after 10m0s
	running tests:
		TestIssuedURLsCarrySchemeForBareWebHost (2s)
```

67 of 69 packages pass. The two that fail do not fail an assertion — they run out of
Go's **default** per-package timeout, which `make test` never overrides. The test
named in the panic took 2 s; it was simply the one holding the floor when the clock
expired, so the panic does not even name the slow test.

So `CLAUDE.md` documents `make test` as the fast dev loop and it reports FAIL on a
healthy tree, every time, for everyone. A gate that always fails is not a gate: it
teaches its readers to ignore it, which is the same end as a gate that cannot fail.

**RE-MEASURED with the new ceiling, and the cause is contention, not a ten-minute
package.** Run alone, `go test ./routd/ ./store/ -short -timeout 1800s` gives
**routd 451.7 s, store 247.1 s**, both green. Against the full suite the same two took
601 s each. Go's `-timeout` is a per-package WALL clock and `go test ./...` runs
packages concurrently up to `GOMAXPROCS` — two cores here — so each binary's clock
counts the time it spends descheduled, plus the `dyntest` store container every
store-touching package starts. routd needs 452 s of its own and died at 600 s of wall
clock it mostly did not get to use.

So the ceiling was genuinely too low for a two-core host, and the slowness is still
real: 452 s for one package under `-short` is the part worth explaining.

**MEASURED 2026-10-07, and there is no slow test — there are 783 of them.**
`go test ./routd/ -short -json` gives **783 tests, 534 s of named test time, median
0.61 s**. The top twelve are 66 s, 12 % of the total; the slowest single test is 13.8 s.
565 tests sit between 0.1 s and 1 s. So the cost is a per-test FIXED overhead of roughly
0.6 s, not a hot spot, and no amount of fixing one test touches it.

The overhead is the fixture. `memDBOrSkip` → `routd.OpenMem` → `store.TestOwner` →
`PrepareOwner` → `EnsureEntitySet` over `EntitiesByOwner[OwnerRoutd]`, which is **32
entities**, plus `audit.EnsureEntities`, plus `SeedRoutdFloor`'s ACL rows. **Every one of
the 783 tests creates 32 tables against the store server and drops them again.**

**The timeout is the symptom; the 32-table fixture is the defect.** `-short` is supposed to
skip the long tests, and two packages still spend ten minutes. One contributor is
known and recorded: `chanreg.Register` does an unbounded `net.LookupIP`, measured at 98
seconds inside routd's suite (`dns_lookup_in_register`). Nothing has measured where the
rest goes, and `store` has no such explanation at all.

- **Severity:** medium (no correctness impact; it removes the project's only fast gate)
- **Scope:** `Makefile` `test`; `routd` and `store` test suites
- **Affected:** every run of `make test`, and CI, which runs exactly it
- **Source:** the full suite run of 2026-10-07; both durations and the panic text
  measured, not inferred
- **Status:** partially fixed. `make test` now passes `-timeout 1800s`, so the gate can
  report a real result. **The ten minutes is still unexplained and stays open**: a
  ceiling against a hang is not a budget. The cause is now measured (above), so what
  remains is a DECISION, not an investigation: prepare the entity set ONCE per test
  binary and give each test a namespace over it, instead of 32 `CreateTable` calls per
  test. That changes the fixture contract every daemon's tests share — `store.TestOwner`
  is not routd's — so it needs sign-off rather than a drive-by. `store`'s 247 s is the
  same shape through the same helper.

## L392 — four host ports below 49000 are arizuko's, one of them a code default nobody set, and the asked-for shape (no mapping, host network, ports above 49000) is a redesign (2026-10-05, open — needs sign-off)

What occupies the host today, measured 2026-10-05 (`ss -tlnp`, `docker ps`):

| host port | bind | owner | how it got there |
|---|---|---|---|
| 8080 | 0.0.0.0 | `arizuko_routd_sloth` (docker-proxy pid 12164) | sloth's `.env` has no `API_PORT`; `seedSharedDefaults` fills `core.DefaultAPIPort` = 8080 and `routdService` publishes `API_PORT:8080` |
| 8081 | 0.0.0.0 | `arizuko_routd_krons` | `API_PORT=8081` |
| 8090, 8091 | 0.0.0.0 | `arizuko_dashd_sloth`, `arizuko_dashd_krons` | `DASH_PORT` |
| 18443, 18444 | 127.0.0.1 | `arizuko_extenddb_krons`, `_sloth` | `STORE_HOST_PORT` (`template/services/extenddb.yml`) |
| 49165, 49177 | 0.0.0.0 | `arizuko_proxyd_krons` | `WEB_PORT`, `WEB_PORT_ALIASES` |
| 49176 | 0.0.0.0 | `arizuko_proxyd_sloth` | `WEB_PORT` |

Nothing else of arizuko's touches a host port. kokoro's `8880/tcp`, ttsd's
`8880`, vited's `8080` and every Go daemon's `:8080` live inside the instance
bridge network; `docker ps` prints them because the images `EXPOSE` them, and
an exposed-but-unpublished port consumes nothing on the host. The eight
listeners above are the whole of what "below 49000" means here, and six of them
are `.env` values the operator can move today (see the interim step).

Neighbours on the host network, so the allocation below avoids them: 49169
(`go_from_rust.service`), 49179–49181 (reveries), 18445 (`extenddb.service`),
8178 (`whisper.service`).

**The asked-for shape.** No `ports:` mapping and no `EXPOSE` indirection: each
service listens directly on its own host port above 49000, `network_mode:
host`, internal services bound to 127.0.0.1, public entry points to 0.0.0.0.
That is a redesign under this file's standing policy — it replaces compose
service names as the addressing scheme (`http://routd:8080` in every fragment,
every proxyd route backend, `ROUTER_URL`/`AUTHD_URL`/`ONBOD_URL`/`VITE_ADDR`,
`healthBlock`'s `http://127.0.0.1:8080/health`, `DYNAMODB_URL=https://extenddb:18443`
in both systemd units' `generate` step) with declared per-instance host ports,
and it retires the `:8080`-everywhere invariant in root `CLAUDE.md`
§Conventions. Shipping it uninvited would spend that decision; the 2026-04-29
outage in root `CLAUDE.md` came from one derived network name.

**Allocation — one declaration.** One `.env` key per instance, `PORT_BASE`
(krons 49200, sloth 49300), and one offset table in `compose` keyed by daemon
name; a package's offset lives in its `<name>.yaml` manifest, so a suffixed
copy (`teled-rhias`) declares its own in the copied manifest, never derived
from position. Every port is `PORT_BASE + offset`:

| offset | daemon | krons | sloth | bind |
|---|---|---|---|---|
| 0 | authd | 49200 | 49300 | 127.0.0.1 |
| 1 | routd | 49201 | 49301 | 127.0.0.1 (replaces `API_PORT`; the host CLI dials it) |
| 2 | runed | 49202 | 49302 | 127.0.0.1 — the per-turn egress proxy (`RUNED_PROXY_PORT`, 3128) is on the tap side and is not a host port today |
| 3 | webd | 49203 | 49303 | 127.0.0.1 |
| 4 | dashd | 49204 | 49304 | 127.0.0.1 (replaces `DASH_PORT`; proxyd fronts `/dash/`) |
| 5 | vited | 49205 | 49305 | 127.0.0.1 (`VITE_PORT`) |
| 6 | timed | 49206 | 49306 | 127.0.0.1 |
| 7 | onbod | 49207 | 49307 | 127.0.0.1 |
| 8 | davd | 49208 | 49308 | 127.0.0.1 (replaces `DAV_PORT`) |
| 9 | extenddb | 49209 | 49309 | 127.0.0.1 (replaces `STORE_HOST_PORT`) |
| 10 | extenddb_db | 49210 | 49310 | 127.0.0.1 (`postgres -p`) |
| 11 | ttsd | 49211 | 49311 | 127.0.0.1 (`TTSD_ADDR`) |
| 12 | kokoro | 49212 | 49312 | 127.0.0.1 — **mapped, not moved**: the upstream image's `entrypoint.sh` ends in `exec uv run … uvicorn … --host 0.0.0.0 --port 8880`, so 8880 is its contract; it stays on the instance bridge with `ports: ['127.0.0.1:${PORT}:8880']` and ttsd dials the loopback port |
| 13 | teled | 49213 | 49313 | 127.0.0.1 |
| 14 | teled-rhias | 49214 | — | 127.0.0.1 |
| 15 | whapd | 49215 | 49315 | 127.0.0.1 |
| 16 | discd | 49216 | 49316 | 127.0.0.1 |
| 17 | slakd | 49217 | 49317 | 127.0.0.1 |
| 18 | litellm | 49218 | 49318 | 127.0.0.1 (`--port`) |
| 19 | litellm_db | 49219 | 49319 | 127.0.0.1 |
| 20–25 | gitd, linkd, emaid, reditd, bskyd, twitd | 49220–49225 | 49320–49325 | 127.0.0.1 |
| — | proxyd | 49165 + 49177 | 49176 | 0.0.0.0 — the public entry point; unchanged because the edge dials these |

**What the move costs**, each a file or a contract the table above does not
cover by itself:

- `WEB_PORT_ALIASES` is a second `ports:` line today; on the host network
  proxyd must listen on each alias itself (one `LISTEN_ADDR` becomes a list).
- `DYNAMODB_URL` names `extenddb` and the store's TLS certificate is issued for
  that name; dialling `https://127.0.0.1:49209` needs the certificate to carry
  the loopback SAN or the client to pin `ServerName`.
- Where the per-turn egress proxy binds once runed is on the host network
  needs measuring, not assuming: on the tap it stays per-guest, on the host it
  becomes one more listener per turn.
- Every fragment under `template/services/` drops its literal `LISTEN_ADDR`,
  `LISTEN_URL` and `ROUTER_URL` lines; the renderer writes them into the
  daemon's scoped `env/<daemon>.env`, which the fragments already load.
- `compose/compose_test.go` pins `'49165:8080'`, `/web:/web:ro` and the
  health lines; `tests/integration_features_smoke_test.go` checks the
  third-party services as Running; both follow the renderer.
- Docs: root `CLAUDE.md` §Conventions, `ARCHITECTURE.md` §"All Go daemons
  listen on :8080", `INSTALL.md`, `vited/README.md`, `ttsd/README.md`,
  `davd/README.md`, `template/env.example`.
- `L379` narrows: anteval's callback sink becomes a host address, and whether
  the tap rule set lets a guest reach it is that entry's remaining question.

**Interim step, no code, yours (operator data).** It clears every arizuko port
below 49000 with the mechanism that exists: in `/srv/data/arizuko_krons/.env`
set `API_PORT=49201`, `DASH_PORT=49204`, `STORE_HOST_PORT=49209`; in
`/srv/data/arizuko_sloth/.env` set `API_PORT=49301`, `DASH_PORT=49304`,
`STORE_HOST_PORT=49309`; then `sudo systemctl restart arizuko_sloth`, check
`ss -tlnp`, then `sudo systemctl restart arizuko_krons`. One restart per
instance is the unit's own `down → generate → up`; there is no narrower path
while one compose file carries the instance. The container-internal `:8080`
and `:8880` stay, and they cost the host nothing.

- **Severity:** low in behaviour (nothing is broken; sloth's 8080 is a default
  on a public interface and `routd` has its own auth), high in scope
- **Scope:** `compose/compose.go` (`seedSharedDefaults`, `routdService`,
  `dashdService`, `proxydService`, `davdService`, `vitedService`,
  `healthBlock`, `proxydRoutes`), `core/config.go` (`DefaultAPIPort`), every
  `template/services/*.yml`, both systemd units, the docs listed above
- **Source:** the vited idle-cost measurement of 2026-10-05 (`L391`), which
  enumerated the host's listeners
- **Status:** open — the host-network shape. What shipped 2026-10-05
  (`7183063c`): `routdService`, `dashdService` and `davdService` publish on
  `127.0.0.1` by construction, so the bind column of the allocation holds
  for every port that exists today, and `TestInternalPortsBindLoopback`
  pins it; v0.70.0's deploy sets `API_PORT`, `DASH_PORT` and
  `STORE_HOST_PORT` per the table (krons 49201/49204/49209, sloth
  49301/49304/49309), and `L394` makes the store move reach the host CLI.
  What does not ship: `network_mode: host` and the end of `ports:` mapping.
  It re-addresses every daemon (`http://routd:8080` in every fragment and
  route backend, `healthBlock`, the systemd units' `generate` step, the
  store certificate's SAN, proxyd's alias listen) and nothing of it can be
  tested on this host today — `go1.27rc2` is not installed, so `make lint`
  and `go test ./...` cannot run outside a container, and the host had
  391 MB free with swap full. A redesign of that reach ships with the test
  suite green, not with a vited fix.

## L390 — `specs/5/12` says a retry waits on a doubling backoff, and no such wait exists (2026-09-29)

`specs/5/12-turn-retry.md:37` says the re-feed happens "on the busy backoff (4s,
doubling to a 60s cap)".

`routd/dispatch.go:484-492` returns `retry=true` with **no delay and no `noteBusy`
call** — the batch stays un-advanced and re-feeds on the next poll tick, which
`routd/loop.go:184-185` defaults to 2s with nothing setting it otherwise. `noteBusy` is
called from one place, `routd/dispatch.go:141`, which is the STEER path, not the retry
path.

So a reader who sizes a retry budget from the spec is out by more than an order of
magnitude, and two operator pages repeated the spec's number until this sweep removed
it.

- **Severity:** low in behaviour, and it is the spec that is wrong about the code
- **Scope:** `specs/5/12-turn-retry.md` — and whichever of the two is the intended
  design
- **Source:** web-docs sweep, 2026-09-29; the absent call verified at both sites
- **Status:** open, and it needs a DECISION rather than an edit: either the retry
  should back off, in which case the code is missing `noteBusy` and this is a defect,
  or it should re-feed immediately, in which case the spec's sentence goes. A 2s
  re-feed loop against a failing turn is three attempts in six seconds, so the
  question is not cosmetic.

## L387 — the boot number `6/41` is gated on cannot be measured: the bench target cannot run on this host and production never records the ready frame (2026-09-29)

`6/41`'s first acceptance criterion is "`make bench-boot` on krons reports ≤ 1.0 s
p50 to `guest-init`'s first frame, and the number is in the changelog". Neither half
is possible today.

**The bench target cannot run here.** Measured 2026-09-29:

```
bench-boot: NOTHING WAS MEASURED. The test did not reach a booted guest:
  - egress is not executable (RUNED_EGRESS_BIN=egress): executable file not found in $PATH
  - RUNED_VIRTIOFSD=/usr/libexec/virtiofsd is not an executable file
```

Both binaries live in the **arizuko image**, not on the host, and `make bench-boot`
runs `go test` on the host. The target's own guard is honest — it refuses rather than
reporting a SKIP as a measurement — so this is a target written for an environment
that does not exist, not a silent pass.

**And production measures nothing either.** `ctlSession.ready` is written on the
guest's `{ready}` frame and **has no reader outside the boot test** — measured, a
tree-wide grep for `.ready` in `runed/` returns one line, the send. The channel is
buffered 1 with a `default:` skip, so in production the frame arrives and is
discarded. runed logs `booting guest` and, much later, `guest exited`; nothing between
them, so the p50 cannot be recovered from the journal either (checked against 24 h of
`arizuko_krons` logs: seven turns, no boot line).

- **Severity:** low in behaviour, high for the gate (a criterion nothing can answer)
- **Scope:** `Makefile` (`bench-boot`), `runed/vmctl.go`, `specs/6/41-qemu-substrate.md`
- **Affected:** `6/41`'s acceptance criterion 1
- **Source:** ran `make bench-boot DATA_DIR=/srv/data/arizuko_krons` 2026-09-29; the
  absent production reader measured by grep, the absent log line by journal
- **MEASURED 2026-10-07, and the gate is MISSED.** The number was recoverable all
  along, from a place neither half of this entry looked: `guest-init`'s probe line
  in each turn's `*.console.log` carries `ctl_up_uptime`, the guest's own
  `/proc/uptime` at the moment the control channel came up. Across the **60 most
  recent turns on both instances**: **p50 = 1.54 s**, min 1.11 s, max 7.14 s. The
  gate is ≤ 1.0 s p50, so it fails by half — and it fails generously, because this
  clock starts at KERNEL entry and excludes QEMU's own startup.

  `6/41` makes the boot budget the gate that keeps `6/20`'s warm pool declined, so
  by that spec's own rule the pool question REOPENS. That is a decision, not a
  defect, and it belongs to the user.
- **Status:** open — the measurement is no longer missing; the GATE is now known to
  fail, and `make bench-boot` still cannot run on this host. The user waived this gate for the flip ("flip regardless of the
  number"), so nothing is blocked on it; it is the MEASUREMENT that is missing, and a
  waived gate that also cannot be measured later is how a number stays unknown forever.
- **Fix, one line of production rather than a harness:** runed logs the boot split when
  the `{ready}` frame arrives — the delta from `booting guest`, plus the guest's own
  probe map. Then every turn on both instances measures the gate and the changelog
  number comes from production instead of a synthetic boot. `make bench-boot` then
  stops being the only path to the number, and its own fix is separate: run the test
  inside the arizuko image, which is where `virtiofsd` and `egress` are.
- **Also stale:** `bench-boot` still exports `RUNED_SUBSTRATE=qemu`, a variable
  `6/41` step 4 deleted.

## L379 — anteval's callback sink is unreachable from a guest, so eight of nine cases time out on a working substrate (2026-09-27, open)

`anteval --smoke` on sloth, on the VM substrate, quiet host, two run slots, a 3 h
bearer: **1/9**. The one pass is `rest-roundtrip` (20 s), and it is the only case
that needs NO callback — it checks that a message appeared in `web:eval`.

Every other case fails the same way: `no callback for <case>`, or an empty URL
(`Get "": unsupported protocol scheme ""`) because the value the callback would
have carried never arrived.

**The substrate is not the cause, and the proxy's own log says so.** From the same
run:

```
egress: allow connect src=10.97.0.69 host=api.anthropic.com
egress: allow connect src=10.97.0.69 host=http-intake.logs.us5.datadoghq.com
```

The guest reaches the model through its tap's proxy, 16 model-usage reports landed
in 12 minutes, and one hand-timed turn on the same instance was **43 s** inject to
reply. What the proxy log does NOT contain is a single connect to the sink.

**The sink is a docker-network address.** The gate runs it on the compose network's
gateway (`--sink http://$GW:9099`), which worked when the turn was a container ON
that network. A guest's only interface is one tap whose rule set accepts its own
proxy and drops everything else, so a direct dial to `192.168.160.1:9099` is
dropped below the guest's kernel — and routing it through the proxy would not help,
because the allowlist matches NAMES and the matcher skips IP entries
(`egress/address.go`).

- **Severity:** high for the eval, zero for the fleet (the substrate works; the
  harness cannot observe it)
- **Scope:** `anteval/pkg/run`, `.claude/skills/eval/gates.md`
- **Affected:** every case whose verdict needs an agent callback — 8 of 9 in smoke
- **Source:** measured on sloth 2026-09-27: 1/9, the proxy's allow-connect lines,
  and no sink connect in the same window
- **Status:** open
- **Fix — one of three, and the choice is a decision:**
  1. **A name the proxy can dial.** Give the sink a hostname the host resolves and
     allowlist it for the eval folder. Smallest change, and it keeps the callback on
     the one path a guest has.
  2. **A tap rule for the sink.** One explicit accept beside the proxy rule, for the
     eval folder only. Widens a guest's egress by one address, which is what the
     rule set exists to prevent.
  3. **Callback over MCP.** The guest already has one authenticated channel to the
     platform; a case's result could arrive as a tool call rather than an HTTP POST.
     Largest change, and the only one that needs no network reachability at all.

## L363 — sloth advertises five atlas Telegram routes and can serve one; the bot the other four talk to is served by nothing (2026-09-25, open — operator deploy + decision)

`L335` found this shape for Slack. It is also true for Telegram, and it survived
that entry's fix because only the Slack half was repaired.

marinade's store was consolidated into sloth around 2026-09-18 (`L335`) and the
`atlas` folders and routes came across. **The Telegram bot did not.** sloth's
`teled` serves `@sloth_fiu_bot` (id 8520760331); atlas's chats talk to
`@mnde_atlas_bot` (id 8122465866), whose only token lives in marinade's tree and
whose instance is `disabled` and `inactive (dead)` since 2026-09-21.

Measured 2026-09-25 by asking Telegram `getChat` with EACH bot's token, because
the route table alone cannot say whether a chat is reachable:

| sloth route (priority 30) | chat | who can see it |
| --- | --- | --- |
| `atlas` ← `room=user/1112184352` | `kronael`, private | **`@sloth_fiu_bot`** — works |
| `atlas/content` ← `room=group/5012430759` | "Marinade Content Group", basic group | **`@mnde_atlas_bot` only** |
| `atlas/martin` ← `room=user/137865889` | `Martin`, private | **`@mnde_atlas_bot` only** |
| `atlas/support` ← `room=group/1003805633088` | — | **neither bot** (every id encoding returns "chat not found") |
| `atlas` ← `room=user/1444744465` | — | **neither bot** |

`@mnde_atlas_bot` answers `getMe`, has **no webhook** (`url` is empty, so it
long-polls) and `pending_update_count` 0 — nothing polls it, so an update is
dropped rather than queued. So `atlas/content` and Martin's DM are ingress the
route table advertises and no process can deliver.

The store agrees and would have been read wrong on its own. Both group
partitions look alive: `telegram:group/1003805633088` carries a message from
2026-09-25T02:00:48. It is `sender = timed-isolated:task-…` running
`/compact-memories episodes day` — **cron writing into the partition**, not
inbound traffic. Filtering to `sender like 'telegram:%'` gives the real picture:
atlas/support's newest human message is **2026-06-05**, atlas/content's is
**2026-05-19**, `atlas/martin` has **none ever**, while the operator DM that does
work carries one from 2026-09-21, after the cutover. A "newest message" check
that skips the sender reports a healthy chat.

- **Severity:** medium (two live chats, one of them a named person's DM, reach a
  folder that is advertised for them and cannot receive; no error anywhere)
- **Scope:** `/srv/data/arizuko_sloth/services/`, sloth's `routd.routes`
- **Affected:** the five priority-30/31 `atlas*` Telegram routes in sloth's live
  route table; `@mnde_atlas_bot`
- **Source:** the marinade consolidation, 2026-09-18 (`L335`); found 2026-09-25
  while answering whether atlas is hooked into krons — it is not, and was never
  meant to be
- **Status:** DECIDED 2026-09-25 — option 3. The owner's words: "atlas as a bot on
  telegram is now dead for now", and atlas stays a coding assistant **on sloth**.
  So the bot is not restored and the four unreachable routes are dead by
  intention, not by accident.

  Two things follow, and neither is an edit I may make. The four rows still sit in
  sloth's live route table and still advertise ingress that cannot arrive, so the
  table lies until an operator removes them — one `arizuko route delete` per row,
  live data, the owner's. And **"for now" is why the token still matters**: the
  only copy of `@mnde_atlas_bot`'s token is in
  `/srv/data/tmp_removed_marinade_20260925`, so deleting that tree ends the option
  to revive it. Copy the token out first if "for now" might end.

  A new Telegram account for sloth is the owner's too — BotFather is not
  something this repo can reach.
- **Fix — one of three, and it is a decision:**
  1. **Serve the atlas bot on sloth**, the shape that fixed `L335`: a second
     Telegram adapter beside `teled`, exactly as krons already runs
     `teled-rhias` alongside `teled`. Its token is `TELEGRAM_BOT_TOKEN` (id
     8122465866) in marinade's `.env` and `env/teled.env`. **Those files are the
     only copy**, and they now sit in
     `/srv/data/tmp_removed_marinade_20260925/`, staged for deletion — deleting
     that tree without copying the token out first makes this fix impossible.
  2. **Add `@sloth_fiu_bot` to the two live chats** and drop the atlas bot. One
     operator action per chat, no deploy, and it changes which bot the people in
     "Marinade Content Group" and Martin talk to.
  3. **Delete the four unreachable routes.** The honest option if atlas's
     Telegram side is finished: two of them already name chats neither bot can
     see, so they are dead regardless of the decision above.

## L364 — sloth's six duplicated route ids need re-minting; the CODE half is fixed (2026-09-25, open — operator action on live data)

**This destroyed a live route while I was using it.** `arizuko route sloth rm 37`
printed `removed route 37`, left route 37 in place, and deleted
`main/main ← room=group/5069669817 verb=mention` instead — a live Telegram group's
trigger rule. Its `#observe` twin survived, so that group would have observed
every message and answered none. Restored as route 50 with the identical match,
target and seq; a before/after diff of all 38 rows confirms the table now differs
from its prior state by exactly the one row that was meant to go.

`store/routes.go` `routeByID` reads the whole partition and returns on the FIRST
`r.ID == id`:

```go
for _, it := range items {
    r, err := routeFrom(it)
    ...
    if r.ID == id {
        return it, r, true, nil
    }
}
```

`DeleteRouteInto` then deletes `it.Key`. Items arrive in sort-key order, which is
`<seq>\x1f<target>\x1f<match>`, so the row destroyed is whichever shares the id
and sorts EARLIEST — a low-`seq` rule beats the one the operator named. The CLI
reports success naming the requested id, so nothing tells the operator which row
actually went.

**The duplicate ids come from the marinade consolidation.** Measured 2026-09-25:

| duplicated id | the two rows sharing it |
| --- | --- |
| 33 | `libtest ← room=libtest` · `main/trading ← room=group/5125571890 verb=mention` |
| 34 | `main/trading#observe ← room=group/5125571890` · `atlas/support ← discord:*/* verb=mention` |
| 35 | `main/trading#observe ← hook:main/trading/deepdex` · `atlas/support#observe ← discord:*/*` |
| 38 | `main/main#observe ← room=group/5069669817` · `atlas/content ← room=group/5012430759 verb=mention` |
| 39 | `main/content ← room=group/5282578105 verb=mention` · `atlas/content#observe ← room=group/5012430759` |

Every pair is one `main/*` row against one `atlas/*` row — sloth's own table
against marinade's, each minted from its own `CounterRouteID`, merged into one
partition around 2026-09-18 (`L335`). Id 37 was a sixth pair and is now unique
only because the delete above destroyed its `main/main` half. **krons has zero
duplicates**, so this is the merge and not a defect in minting.

It is the same class as `L335`'s closing paragraph — "the platform ids also now
overlap … the same id will be minted twice in one partition" — which was recorded
for `messages` and not chased into `routes`. `ReplaceFolderRoutesInto`'s own
comment says why an id matters: it is "the handle `delete_route(id)` addresses",
and a live agent reads it from `list_routes`.

- **Severity:** high (silent destruction of an arbitrary routing rule on any
  `route rm` against sloth; the agent's `delete_route` tool takes the same id)
- **Scope:** `store/routes.go`, sloth's `routd.routes`
- **Affected:** `routeByID`, `DeleteRouteInto`, `RouteByID`, `GetRoute`,
  `arizuko route rm`, the `delete_route` MCP tool
- **Source:** measured 2026-09-25 by deleting route 37 on sloth and diffing all
  38 rows before and after
- **Status:** open
- **Fix — two halves, and only the first is code:**
  1. **`routeByID` must REFUSE an ambiguous id** rather than return the first
     match: scan every item, and if two carry the id, return an error naming both
     coordinates. A wrong deletion becomes a loud refusal. Needs a test that
     seeds two rows with one id and asserts the delete fails and BOTH rows
     survive.
  2. **Re-mint sloth's ids so they are unique** — live data, the owner's. Until
     then, address a rule on sloth by its `(seq, target, match)` coordinate and
     never by id.

**The code half is FIXED and this entry is now the operator half only, re-verified
2026-10-08.** `store.ErrRouteIDAmbiguous` (`store/routes.go:45`) and the scan at
`:273-289` refuse an id two rows carry and name both coordinates; `f1220013` made
`routd.DB.GetRoute` delegate to `store.RouteByID` instead of re-implementing a
first-match loop a package up, and `eb4c9d5a` stopped the mint handing out an id a rule
already holds. `routd/routes_rest_test.go:278` seeds two rows with one id and passes.

So `route rm <id>` no longer deletes an arbitrary row — it refuses with a 409. What is
left is live data: six ids on sloth that the marinade merge duplicated. Until they are
re-minted, address a sloth rule by `(seq, target, match)` and never by id.
## L359 — 11 G of interrupted git repacks sit in the data tree and nothing prunes them (2026-09-24, open)

The root filesystem is at 99%, 242 G used of 256 G, 3.9 G free.

Eleven `tmp_pack_*` files under `/srv/data/arizuko_krons` total 11.0 G, the
largest 1.7 G. No git process is running, so every one is the debris of a
repack or fetch that was killed part-way. Git writes these under
`objects/pack/` while it builds a pack and renames them on success; on a kill
they stay, and no later operation removes them, because git only prunes temp
files it can prove are its own and older than the grace period.

The cause is that these repacks are being interrupted. The box runs at load
7.64 on two cores, so a repack of a multi-gigabyte repository takes long
enough to meet a container restart, a timeout, or an OOM.

- **Severity:** high — the disk has 3.9 G left, and a full root stops every
  daemon that writes
- **Scope:** the data tree under `/srv/data/arizuko_krons`
- **Affected:** the `elizaos` and `openclaw` reference clones in the krons and
  rhias groups and in `groups/krons/workspace/agent-research-sources`
- **Source:** `find /srv/data -name 'tmp_pack_*'` and `df -h /`, 2026-09-24
- **Status:** the 11 G is reclaimed (2026-09-29, named file by file with the
  user's approval — a `find -delete` over a data tree is not a cleanup, it is a
  second outage). The PREVENTION is not built, so this entry stays open: nothing
  prunes `tmp_pack_*`, and the interrupted repack that writes them is still
  interrupted. Measured 2026-09-29: no `tmp_pack_*` under `/srv/data`.
  say-so, and the build cache with them: `find /srv/data -name 'tmp_pack_*'`
  answers 0 and the disk went 2.7 G free → 18 G (98% → 93%). The CAUSE is open:
  nothing stops the next repack from being interrupted, and the 26 G staged
  marinade tree is still the largest single reclaim (its credentials are copied
  out, see `L363`).
- **Fix:** the files are unreferenced and no git process holds them, so
  deleting them recovers 11 G immediately and loses nothing. Two further
  reclaims need no decision: `docker system prune` frees 21.97 G of the 32.93 G
  in 66 images, of which only 18 are active, and the build cache is 3.664 G
  with nothing active. Together that is roughly 37 G, taking the disk from 99%
  to about 84%.

  A third reclaim, measured 2026-09-24: `.claude/worktrees/` holds **3.1 G** in
  **25** agent checkouts, of which **23 have no uncommitted changes** and are
  therefore agent litter. That is close to the 3.5 G the volume has left. It
  needs a person only because `git worktree remove` is a wrapped recursive
  delete, which WISDOM bans outright — and because a checkout that is clean can
  still belong to a session that has not edited anything yet.

  The cause fix is to stop interrupting the repacks. Either give the clone a
  bounded history so a repack cannot take minutes, which `L360` argues for on
  its own grounds, or run the repack where a restart cannot reach it and let
  it finish. A janitor that deletes `tmp_pack_*` on a timer is the symptom
  patch: it keeps the disk from filling while leaving every repack still
  failing, so nothing is ever actually packed.

## L404 — a person's own model credential is silently discarded under a gateway, and spec `6/23` says it is translated (2026-10-08, open — the translation needs sign-off)

`6/23` §"Tier placement": *"Env-profile rows remain for the BYOC case, but they resolve
INTO a gateway virtual key rather than into container env."*

`runed/secrets.go` `spawnSecrets` deletes every `store.EnvProfileKeys` entry the moment
`MODEL_GATEWAY_URL` is set. Nothing builds the translation — `routd/gateway.go` `mint`
carries no provider credential, and a grep for a BYOC path into the gateway finds none.

So a person who set their own `ANTHROPIC_API_KEY` gets the operator's gateway key
instead, their turn bills the operator, and **no surface says so**. The delete was right
(`6/23` Fault 1: a credential in guest env is readable by the agent and revocable only
by rotating it for everyone). The silence was not.

- **Severity:** medium (a user's explicit choice is discarded; the billing moves)
- **Scope:** `runed/secrets.go`, `specs/6/23-llm-gateway.md` §"Tier placement"
- **Affected:** any folder with an env-profile row on an instance with a gateway
- **Source:** the phase 6 audit, 2026-10-08; the delete read at
  `runed/secrets.go:36-45`, the absent translation confirmed against
  `routd/gateway.go`
- **Status:** the SILENCE is fixed 2026-10-08 — `spawnSecrets` logs one WARN per spawn
  naming the dropped keys and the gateway, because discarding a choice without a word is
  the fault whatever the right end state is. **The translation stays open**: making a
  BYOC row mint a gateway key the user's own credential funds is a new credential path
  through the gateway's `/key/generate`, which is a contract change needing sign-off.
  Until then `6/23`'s own sentence is the thing that is wrong, and the operator note
  beside it should say the row is dropped.

## ✅ FIXED 2026-10-08 L355 — a bare spec number that resolves can still name the wrong document, and two sit in the authorization path (2026-09-22)

`specs/CLAUDE.md` states the hazard and says no test can catch it: "A bare
`5/5` keeps resolving after a renumber and silently names a different spec —
`5/5` was `uniform-mcp-rest` and is `worlds-agents-sessions` today. Neither
`tests/specrefs` nor `tests/webdocs` can flag that, by construction: both ask
whether the number resolves, never whether it names the same document."

MEASURED 2026-09-22. Of the 64 bare spec numbers cited in tracked `.go`, **26
have named more than one DOCUMENT** — not renames; `git log -M --diff-filter=A`
with rename detection on. The worst:

| number | documents it has named |
| --- | --- |
| `6/9` | acl-unified, crackbox-dashboard, crackbox-sandboxing, crackbox-standalone, positioning |
| `5/5` | agent-media-awareness, tenant-self-service, uniform-mcp-rest, worlds-agents-sessions |
| `5/6` | evangelist, extend-gateway-self, middleware-pipeline, proactive-interjection |
| `5/A` | auth-consolidated, ipc-mcp-proxy, orthogonal-components, primitives-framing |
| `5/33` | auth-landscape, paths-roles, proactive-interjection |

**Two are confirmed wrong, both in the authorization path.** `6/9` today is
`9-crackbox-sandboxing.md`, which holds ZERO occurrences of `acl`, `ACLRow` or
`Membership`:

- `auth/authorize.go:21` — "the room JID for channel-bot inbound (spec 6/9
  §Membership)"
- `core/types.go:265` — "ACLRow is one grant in the unified ACL (spec 6/9)"

Both describe `6/9-acl-unified`, which is `specs/5/32-acl-unified.md` today —
and that file carries the `## Membership — roles, JID claims, channels` section
at line 117. A reader following either pointer lands on a sandboxing spec.

`L354` widened `tests/specrefs` to read the code, and it passes all of these:
`6/9` resolves. Resolution was never the question.

- **Severity:** medium (no runtime defect; a reader of the auth model is sent
  to the wrong document, and the guard says green)
- **Scope:** the 26 reused numbers' citation sites in `.go`; `auth/authorize.go`,
  `core/types.go` are the confirmed two
- **Affected:** anyone following a spec pointer out of the code
- **Source:** `git log --all -M --pretty=format: --name-only --diff-filter=A --
  specs/<phase>/<id>-*.md` per cited number; the content check on
  `specs/6/9-crackbox-sandboxing.md` and `specs/5/32-acl-unified.md`
**THE COMMENT AUDIT IS DONE 2026-09-22** (`fc356ac1`): 417 lines across 204
files, comments only. 31 sites moved to a DIFFERENT document — the two auth
ones, six `5/5` sites that meant `uniform-mcp-rest` (now `5/17-openapi-mcp`),
seventeen that meant the ownership table (`5/16`, which `5/17:210` points to),
four `5/1`/`5/Y` sites — and four section names moved with them, because a §
the new document lacks is a fresh falsehood. Every reused number now cites a
filename stem.

**What is left is not comments.** 31 citations sit inside Go STRING literals —
`packages.go`'s refusal messages, `blend.go`'s advice text, `ipc.go` MCP tool
descriptions, an `emaid` startup warning. They are user-facing text, three are
pinned by test assertions (`blend_test.go:360`, `packages_test.go:603`), and
eight of them carry a reused number.

Separately found and NOT fixed: four sites cite a § that its (correct) document
no longer has — `routd/contract_test.go:23,66` cite `5/E §"Standalone-ready
acceptance"` and that section lives in `5/P:229`; `dashd/engagement_page.go:19`
and `ipc/ipc.go` cite numbered items in `5/G`, which has no numbered list now;
`obs/metrics.go:18` cites `5/O §"Histogram buckets"` where `5/O:53` says "code
is the reference — do not restate here".

- **Status:** open on the string literals and the stale sections. Changing a
  user-facing message is a code change with three test assertions on it, and a
  wrong § inside a right document is a second concern from this entry's.
- **Fix:** `fc356ac1` (the comment audit).

**RE-MEASURED 2026-10-08, and the two sites this entry singled out are NOT ambiguous.**
`cmd/arizuko/packages.go` cites `spec 5/5` and `spec 5/33`; both carry their phase, so
both resolve uniquely — `specs/5/5-worlds-agents-sessions.md` and
`specs/5/33-paths-roles.md` — and both resolve to the right CONTENT: `5/5` has an
`Open 2` about a `folder:` principal carrying no JWT claims, and `5/33` has
subset-of-held. The ambiguity this entry is about needs a BARE number with no phase, and
these are not that. An audit pass counted them as reused; they are qualified.

One real staleness was there and is fixed: the refusal cited "spec 5/5 Open 2" as though
the question were open. It was **settled 2026-08-15** (`5/5:235`), so a reader followed a
pointer to a decision expecting a question. The citation now names the section and the
date it was settled.

What stays open is the SHAPE, not these two: 35 spec citations live in Go string literals
across tracked non-test code, and nothing checks that any of them resolves.
`tests/specrefs` checks markdown links and `depends:` frontmatter — not strings in Go. A
citation a reader follows to nothing is the defect, and the fix is a check over the
literals, which is work nobody has claimed.

- **Status:** ✅ FIXED 2026-10-08. `TestEverySpecCitationInGoResolves`
  (`tests/specrefs`) reads the citations that live in Go — comments and error strings —
  and fails when one names a document that is not in `specs/<phase>/`. **62 distinct
  citations, all resolving.** Planting `spec 6/99` in `ipc/connector.go` fails it.

  That was the open shape: `tests/specrefs` walked markdown links and `depends:`
  frontmatter, so a citation inside a string literal — the kind a USER reads in an error
  message — could name anything. Nothing checked them.

  Two things the check taught, both recorded at the code:

  1. **My first pattern reported six dead citations and all six were the pattern's
     fault** — `6/19.`, `5/F-topic-lineage.`, `5/17-openapi-mcp.`: a loose id class
     swallows the sentence's full stop and the slug. It uses the class
     `tests/webdocs` already proved.
  2. **The check needs no exclusion list, and that is deliberate.** The one genuine
     non-resolver was an EXAMPLE inside a comment about citation shapes
     (`tests/webdocs/links_test.go`). A real-looking number in a comment IS a citation to
     every grep that scans for them, so the comment now uses `<phase>/<id>` placeholders.
     An exclusion list is the part that rots.

## ✅ FIXED 2026-10-08 L350 — spec `6/37` cites six artifacts that no longer exist, four of them the guards it rests on (2026-09-22)

`specs/6/37-store-model.md` is `status: partial` and its steps 1-2 name the
tests that hold the SQL→objstore move honest. Every path in the spec was
checked against HEAD on 2026-09-22; six do not resolve:

| named in `6/37` | at HEAD |
| --- | --- |
| `db_utils/opener_test.go` | the whole `db_utils/` package is gone |
| `db_utils/sql_layer_test.go` | gone with it |
| `store/migrations/` | gone |
| `store/migrations_frozen_test.go` | gone |
| `auditstore/` | gone |
| `audit/query.go` | gone (`audit/query_test.go` remains) |

It also says "`migrate-split` migrates FROM it and nothing else runs it". No
`migrate-*` subcommand exists; `773cb5c5` deleted the last on 2026-09-13.

**The INVARIANTS still hold — it is the guards that are missing.** No
production file imports `database/sql` (one match, `objstore/ordered.go`, is a
comment naming `database/sql.Register` as an analogy). The one live import is
`audit/audit_log_test.go`, for a `routeCount` helper nothing calls. So the move
finished, and `6/37` describes it with tests that were removed once it did.

That is the question this entry holds: a spec at `status: partial` whose guards
were deleted as complete reads as work outstanding AND as protected, and it is
neither. Either `6/37` moves to `shipped` with the dead references cut, or the
guards come back because the invariant still needs holding.

- **Severity:** low (no runtime defect; a spec that sends a reader to six files
  that are not there)
- **Scope:** `specs/6/37-store-model.md`
- **Affected:** nobody at runtime; the next person to read `6/37`
- **Source:** every `` `path` `` in `6/37` tested against HEAD 2026-09-22;
  `git ls-files | xargs grep -l database/sql`
- **Status:** open — needs a decision: shipped-and-cut, or the guards return.
  Memory `phase5_closed` is the precedent: re-verifying 50 "shipped" specs
  found 13 drifts, so a status is a claim, not a fact.
- **Fix:**

- **Status:** ✅ FIXED 2026-10-08, both halves.

  The PROSE half: `6/37`'s **State** paragraph claimed in present tense that "the seam
  is built: `db_utils.Open` selects the engine". It now says shipped and names what
  superseded it, and everything from `## Order` down is explicitly the record of the
  move. The spec already carried that disclaimer — but it sat BELOW the paragraph, so it
  covered the ordered steps and not the claim above them.

  The GUARD half, which is the one that mattered: this entry's own line was *"the
  invariant still holds; the tests that held it do not."* `db_utils/opener_test.go`
  failed the build on any opener but one, and `objstore/sqlstore`'s refusal covered the
  engine; both left with the SQL they guarded, and nothing stopped a second one
  arriving. `TestNoPackageOpensSQL` (`tests/components`) refuses an import of
  `database/sql` and of five named drivers. `database/sql/driver` stays permitted — the
  AWS SDK and `google/uuid` reach it transitively and neither opens anything.

  Planting `import "database/sql"` in `core/` fails it. My first probe went in
  `tmp/_sqlprobe/` and proved nothing, because the go tool ignores a directory whose
  name starts with `_` — the project's own convention, and it makes a probe invisible as
  readily as it makes scratch invisible.

## L347 — rows behind a steered command are never re-fed (2026-09-21, open)

`routd/dispatch.go` consumes the steered row and advances the cursor TO it, so
the rows behind it are below the cursor and are owed a turn. On the `derr` and
`busy` paths that turn does not run: the rows are logged at ERROR and nothing
re-feeds them. A user who asks a question and then sends `/status` loses the
question when runed refuses the turn.

The ERROR log is a symptom marker — it names the loss without ending it.

**The observability half ships 2026-09-22.** While this waits, the ERROR log is
the ONLY record that a person's question vanished, and it read `rows: 3,
first: m1` — one row of N and no person. It names every id and every distinct
sender now, so an operator can find what was dropped and whose it was. The
CONTENT stays out on purpose, and `TestTheLostRowsLogNamesWhoseTheyWere` pins
both halves of that.

That changes no behaviour and settles nothing: the rows are still not re-fed.

- **Severity:** medium (a user message silently dropped, on a path that
  already knows it happened)
- **Scope:** dispatch
- **Affected:** routd
- **Source:** `routd/dispatch.go` (the steer branch's `derr` and `busy` arms)
- **Status:** open. The cause fix is to leave the cursor where it was when the
  turn does not run, so the next poll re-reads the batch — which is a change to
  the consume contract (`L104`) and needs sign-off.

## L339 — a sourced fragment can interpolate any `.env` key, so the `L201` bound stops at the mount (2026-09-21, open — proposal, needs sign-off)

`docker compose` interpolates `${KEY}` in every included fragment from the
project `.env` — the whole file, not the compose-managed block. A package
from a source that may no longer bind `.env` (`L201`, `d1d857b9`) can still
write `environment: {K: '${SECRETS_KEY}'}` or `'${AUTHD_SERVICE_KEYS}'` and
receive the value at `up`; `TestLitellmFragmentReadsOnlyItsDeclaredKeys` pins
this for litellm alone, by hand. `compose/README.md` states the rule ("no
fragment may read the shared `.env`") with nothing mechanical behind it for
a sourced package.

**MEASURED 2026-09-22 against the shipped catalog, and the rule does not hold
there.** Of 14 `template/services/*.yml`, SIX reference a `.env` key their own
manifest does not declare:

| fragment | undeclared `${…}` |
| --- | --- |
| `litellm` | `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, the three `OTEL_EXPORTER_OTLP_*` |
| `discd` | `DISCORD_USER_TOKEN`, `RUNED_RUN_TIMEOUT` |
| `bskyd` | `BLUESKY_IDENTIFIER`, `BLUESKY_PASSWORD` |
| `teled` | `RUNED_RUN_TIMEOUT` |
| `reditd` | `REDDIT_SUBREDDITS` |
| `slakd` | `SLAKD_USERS_CACHE_TTL` |

**litellm's omission is deliberate and written down**, which is the sharp part:
its manifest says the model credential "is the instance's own need, not this
package's, and is not declared here". So the proposal's "declare what you read"
and the catalog's "do not declare what is not yours" are in direct tension, and
the decision has to answer which wins — or exempt first-party fragments, which
`compose/README.md`'s flat sentence ("no fragment may read the shared `.env`")
currently does not.

No guard shipped for this: a test of the proposed rule would fail on six
first-party fragments today, and a guard that has to be born red is a guard
nobody keeps.

- **Severity:** medium (the same supply-chain reach as `L201`, one docker
  feature over)
- **Scope:** package install + upgrade
- **Affected:** any sourced package
- **Source:** `compose.CheckSourcedFragment` reads no `${…}` reference;
  `writeManagedEnv` writes `APP`, `FLAVOR`, `DATA_DIR`, `COMPOSE_PROFILES`
- **Status:** proposed. A sourced fragment may reference only the keys its
  manifest `env` block declares plus the four managed keys; every other
  `${KEY}`, `${KEY:-x}`, `${KEY:?x}` or `$KEY` is refused at install and at
  upgrade, in the same check. This is `5/28`'s `env` section read strictly —
  "what the package's process needs from `.env`" becomes what it may read —
  so litellm shipped as a sourced package would have to declare
  `ANTHROPIC_API_KEY` as `required`, which is the contract's own answer.
  Sign-off because it changes what an author must write.
- **Fix:**

**The exfiltration half is FIXED 2026-10-08; the "undeclared key" half was never the
defect.** Measured both:

`CheckSourcedFragment` now refuses a sourced fragment that interpolates `SECRETS_KEY`,
`AUTH_SECRET`, `AUTHD_SERVICE_KEY`, `MODEL_GATEWAY_KEY` or `AWS_SECRET_ACCESS_KEY`, in
any of compose's spellings — bare, `:-default`, `:?message`, and the brace-less `$KEY`.
Each of those authorises the whole instance rather than one service, and `docker compose`
substitutes them from the shared `.env` at parse time with no complaint, so one line of
`environment:` read the master key from inside a package's own container. `env_file` was
the wide door and was already refused; this was the narrow one, narrow enough to look
like configuration.

The bundled catalog is exempt, which is what makes a denylist safe here: `litellm.yml`
interpolates `MODEL_GATEWAY_KEY` legitimately, because it IS the gateway.

**What is NOT a defect, re-measured:** eight first-party fragments reference keys absent
from `daemonKeys` — `BLUESKY_IDENTIFIER`/`_PASSWORD`, `DISCORD_USER_TOKEN`,
`STORE_DB_PASSWORD`, `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`,
`MODEL_GATEWAY_DB_PASSWORD`, `REDDIT_SUBREDDITS`. Each arrives by an explicit
`environment: KEY: '${KEY}'` line naming exactly one value, which is a second delivery
path and not a leak — unlike the `.env` fallback closed the same day, it hands over one
named key rather than the file. `APP`, `FLAVOR` and `DATA_DIR` are compose-managed
identity, not daemon env. A first pass counted all eleven as undeclared; eight are
deliberate and three were my grep's false positives.

## L334 — sloth grants instance-wide operator to a third party's Telegram account, and to none of the operator's (2026-09-21, fixed — the revoke still needs the owner)

On sloth, `role:operator` holds `* :: ** :: allow` and its Telegram member is:

```sql
-- sloth, routd.acl_membership
select replace(sk_s, chr(31), ' -> '), item_data->'added_by'->>'S'
  from "_ddb_78dd52a0-e302-47a2-8cbe-e4cd2e9c201d" where sk_s like 'telegram%';
-- telegram:user/1183985669 -> role:operator | cli:kron-all
```

`telegram:user/1183985669` is **Michal F** — 120 messages on the instance, most
recent 2026-09-11, 117 of them in `telegram:group/5125571890`, which routes to
`main/trading` (route ids 33/34). He is not the operator. He holds `**`: every
`/root` elevation, every ACL write, every folder.

The operator's own account, `telegram:user/1112184352` ("Kron Ael", 2 922
messages, the DM that runs `atlas`), holds **no** membership edge and **no**
`acl` row. That is `L333`'s denial.

`cli:kron-all` wrote three edges in the same second on 2026-07-19T08:56:14 —
`discord:user/811295670702047272`, `google:115672318187756389401` and this one.
The first two are the operator's. The Telegram id in that batch is not.

**The instance ran the controlled experiment itself.** Same command, same code,
one variable — the membership edge:

| when | surface | sender | `root-…` re-injection? |
| --- | --- | --- | --- |
| 2026-07-18 10:27, 2026-07-19 08:42 | Discord | `discord:user/811295670702047272` | no |
| **2026-07-19 08:44:11** | — | `cli:kron-discord` writes `discord:user/811295670702047272 -> role:operator` | — |
| 2026-07-26 20:01, 2026-09-20 17:06 | Discord | same sender | **yes** — `root-main/trading-1785096086061959777`, `…-1789924050142714754` |
| 2026-04-02 … 2026-09-21 (8 attempts) | Telegram | `telegram:user/1112184352` | never |

Across the whole live store there are 15 `/root <arg>` rows (telegram 8,
discord 4, slack 3) and exactly 2 re-injections, both Discord, both after the
grant landed:

```sql
-- sloth, routd.messages (table 10acfe05-…)
select split_part(pk,':',1), count(*) from "_ddb_<messages>"
  where item_data->'content'->>'S' like '/root %' group by 1;          -- 8 / 4 / 3
select split_part(sk_s,chr(31),2), pk from "_ddb_<messages>"
  where split_part(sk_s,chr(31),2) like 'root-%';                      -- 2 rows, discord
```

The same wrong row is on krons, where it was worked around rather than fixed:

```sql
-- krons, routd.acl_membership
-- telegram:user/1112184352 -> role:operator | hitl-experiment | 2026-08-25
-- telegram:user/1183985669 -> role:operator | cli:kron-all    | 2026-07-19
```

**This corrects `L152`**, which read the same id on krons as "inert … has sent
zero messages on the instance, ever". That was true of krons and is false of
sloth: here it is a live sender holding the widest grant in the tree. A grant
can name a principal that is real, present, and the wrong human — and no
surface distinguishes that from the intended one, which is `L152`'s point with
a sharper edge.

- **Severity:** high (a third party holds `**` on a live instance; the operator
  holds nothing on the surface he uses daily)
- **Scope:** operator data on sloth and krons — no code change
**VERIFIED 2026-09-21 23:5x, live store.** The operator's edge is written:

```
telegram:user/1112184352 -> role:operator | arizuko grant | 2026-09-21T11:12:00Z
```

Every operator edge on sloth, with the first `/root` that followed it:

| account | channel | edge written | `/root` after the edge | re-injection |
| --- | --- | --- | --- | --- |
| `discord:user/811295670702047272` | Discord | 2026-07-19 08:44 | 2026-09-21 20:25 | **yes** |
| `slack:user/U4PMARRT6` | Slack | 2026-07-16 15:33 | 2026-07-16 20:01 | **no** |
| `telegram:user/1112184352` | Telegram | 2026-09-21 11:12 | none yet | — |
| `telegram:user/1183985669` | Telegram | 2026-07-19 08:56 | none ever | — |

Three `root-…` rows exist on the instance and all three are Discord's, the most
recent 2026-09-21 20:28 — so `cmdRoot` itself is sound and the grant IS the
Telegram cause. The id lives in the SORT KEY and there is no `id` attribute, so
a query on `attrs->'id'` answers zero rows for a working instance.

**Slack is a SECOND cause, not this one.** `U4PMARRT6` held the edge four and a
half hours before its `/root`, and no re-injection followed. That sits with
`L335`: sloth served Slack through marinade's routes with no slakd store, so the
command never reached a live adapter. `L335` and `L338` are closed; the Slack
half is untested since.

**PROVEN LIVE 2026-09-21 23:55.** Both identities ran an elevated turn on the
running instance, in a throwaway `web:eval/rootproof` chat (token revoked after):

```
trigger telegram:user/1112184352 -> turn root-eval-1790034919875239910
  dispatch capabilities resolved  elevated=true
  container agent  Result #1: ok=true text=ROOTPROOF      container exited code=0

trigger slack:user/U4PMARRT6     -> turn root-eval-1790034943843725650
  dispatch capabilities resolved  elevated=true
  container agent  Result #1: ok=true text=SLACKPROOF
```

The real routd, the real store, the real grant, a real elevated container. No
test double anywhere in that chain.

- **Status:** fixed for the operator's own access — the additive edge
  is in the live store. The third party's `**` is NOT revoked: that is operator
  data and the owner's call. Neither channel has seen a `/root` since its fix,
  so both are unproven by a real attempt.

  ```sh
  # sloth: give the operator's own Telegram account the ** grant.
  # Writes acl_membership telegram:user/1112184352 -> role:operator.
  # Effect: /root, /status, /invite, /gate work from the atlas DM.
  arizuko group sloth grant telegram:user/1112184352 ** admin

  # sloth AND krons: drop the third party's ** grant, if unintended.
  # Removes acl_membership telegram:user/1183985669 -> role:operator.
  # Effect: ordinary chatting is untouched — a turn's authority comes from the
  # ROUTE axis (folder:main/trading -> role:member, auth/authorize.go), and the
  # sender axis only ADDS. He loses /root, add_acl and cross-folder reach.
  # Decide before removing — this IS a revocation.
  arizuko group sloth ungrant telegram:user/1183985669 ** admin
  arizuko group krons ungrant telegram:user/1183985669 ** admin
  ```

  Related, same table, not proposed: `telegram:user/1183985669 ->
  google:114015391913260915382` (`migration-0053`, 2026-05-06) is an identity
  link to a Google sub authd does not know — `authd.oauth_identities` holds one
  row, `google:115672318187756389401`. It confers `admin :: coach` on him.

## L338 — marinade restart-loops every 6 seconds: its store was consolidated away and its config still points at the old one (2026-09-21, LOOP STOPPED 2026-09-21; where Slack lives is the open decision)

`arizuko_marinade.service` is `activating (auto-restart)` and has been failing
continuously. **Measured 2026-09-21: 559 failed starts in one hour**, one every
~6 seconds, and each iteration runs a `docker compose down --remove-orphans`
plus a `docker run` of the generate image. That is a live resource burn on a
host already at 97% disk.

The cause, reproduced by running the unit's own ExecStartPre by hand:

```
$ sudo docker run --rm -v /srv/data/arizuko_marinade:/srv/data/arizuko_marinade \
    -v /var/run/docker.sock:/var/run/docker.sock arizuko:latest arizuko generate marinade
Failed: open routd's store: dynstore: DYNAMODB_URL names no endpoint
```

`/srv/data/arizuko_marinade/.env` carries NO `DYNAMODB_URL` line, and no
`arizuko_extenddb_*_marinade` container exists. marinade's store was
consolidated into sloth's ExtendDB on 2026-09-18 — sloth now holds marinade's
1,160 `slack:` rows and its `slack:user/U4PMARRT6 -> role:operator` edge — but
the instance's own config was never repointed or retired.

**This is why Slack is dead on both instances.** marinade owned the Slack
adapter and cannot boot; sloth inherited the routes and the operator grant but
not the adapter, so `slakd` runs nowhere. The two facts are one incident.

The same run also prints four `L41` FROZEN warnings (`discd`, `emaid`, `slakd`,
`teled` fragments differ from the catalog), which are not the failure but will
bite whoever repoints this instance.

- **Severity:** high (a continuous restart loop on a disk-pressured host; one
  channel dead across two instances)
- **Scope:** instance config vs the store consolidation
- **Affected:** `arizuko_marinade` (loop), Slack on marinade AND sloth
- **Source:** `systemctl status arizuko_marinade`; the generate run above;
  `journalctl -u arizuko_marinade --since '1 hour ago' | grep -c 'Failed to
  start'` = 559; `/srv/data/arizuko_marinade/.env`
- **Status:** fixed. marinade is stopped and its unit disabled
  (`systemctl is-enabled` → disabled, `is-active` → inactive, no containers,
  verified 2026-09-21). That recovered 5.8 GB and ended the loop. The config
  drift itself is NOT repaired, so re-enabling the instance without pointing
  `DYNAMODB_URL` at a live endpoint restarts the loop.

  Do NOT copy the bot token to sloth while marinade can still boot: two
  instances holding one Slack bot token contend for the same workspace
  connection.


**LOOP STOPPED 2026-09-21.** `sudo systemctl disable --now arizuko_marinade`.
Failures went from ~10/minute to **0 in 30 seconds**, and free disk recovered
from 9.2G to **15G** (97% → 95%) — the container churn was itself consuming
the space.

That was harm reduction, not a decision: the journal spans 2026-09-10 to
2026-09-21 and contains **1,574 failed starts and not one successful start**,
so the unit has run at no point in at least eleven days. Stopping it removed no
working function. Reversible with
`sudo systemctl enable --now arizuko_marinade` once the store question below
is answered — but it will resume looping until then.

**The decision that remains is where Slack lives**, and it is now unblocked:
with marinade stopped there is no contention for the bot token, so sloth can
take the adapter safely. sloth already holds the routes, the 1,160 `slack:`
rows and the `slack:user/U4PMARRT6 -> role:operator` edge. What it needs is the
`slakd` package, `SLACK_BOT_TOKEN`/`SLACK_SIGNING_SECRET` in its `.env`, and a
restart — and a restart of a non-krons instance is an explicit operator call
per the root `CLAUDE.md`.

## L329 — an agent that decides to stay silent narrates the decision, and the guard that catches it is whole-string only (2026-09-20, BOUNDARY FIXED 2026-09-20; the steering redesign is a proposal)

A desk agent woke on a message that did not address it, decided correctly,
and delivered the decision to the chat:

```
Not addressed to me — michal0093 is continuing the geopolitics discussion
with susheen_, no @mention or tag. Desk norm is reply only when tagged.
... Staying silent.
```

Two mechanisms let it through, and the second is the one in this repo.

`routd.recordTurnResult` delivers `req.Result` as the reply when the agent
called no reply tool (`routd/turns.go:252`). That is deliberate — the contract
is "text outside `<think>` is delivered", and it closes the 2026-06-08 krons
no-reply outage. So any prose the agent leaves in its final block is a reply.

`router.FormatOutbound` has a guard for exactly this case:
`silentRefusalRe` (`router/router.go:163`) maps a bare "staying silent" to the
empty string, and its own comment says it exists "when it has nothing to say
but didn't wrap silence in `<think>`". It is anchored `^...$`, so it matches
the whole string only. Measured:

| final block | delivered |
| --- | --- |
| `Staying silent.` | "" |
| `<think>reasoning</think>\nStaying silent.` | "" |
| `reasoning. ... Staying silent.` | the whole line |

The agent is told the rule three times in one section
(`ant/CLAUDE.md:73,77,88`), including a bolded ALWAYS, and still wrote its
reasoning as prose. Repeating the instruction is not the lever.

- **Severity:** medium (a user-visible leak of the agent's deliberation into
  a chat it decided not to join; it reads as the agent talking to itself)
- **Scope:** `router.FormatOutbound`; `ant/CLAUDE.md`
- **Affected:** every channel; any turn woken by engagement rather than a
  mention
- **Source:** `router/router.go:163-173`; `routd/turns.go:244-254`;
  `ant/CLAUDE.md:71-88`; measured 2026-09-20 by running `FormatOutbound` on
  the four shapes above
- **Status:** the leak is FIXED at the boundary; the steering redesign is a
  proposal
- **Fix:** `trailingRefusalRe` (`router/router.go`) drops the WHOLE block when
  it ends by announcing the decision not to speak — not just the last
  sentence, because an agent that closes with "staying silent" has said every
  word before it was deliberation. Its phrase list is a deliberate SUBSET of
  the whole-string guard's: anchored to the whole string, bare "silent" and
  "acknowledged" are unambiguous, and at the end of a sentence they are not
  ("the pager stayed silent." is a real answer). Tests
  `TestFormatOutboundDropsReasoningThatEndsInARefusal` and
  `TestFormatOutboundKeepsAnAnswerThatMerelyMentionsSilence`; the second is
  the one that stops this guard from eating replies.

  The boundary fix does not close the cause, which is the second candidate:
  `ant/CLAUDE.md` is 198 lines / 16.5 KB and its
  largest section is `Response Style` (2305 B, 14%), which is formatting, not
  conduct, and the silence rule sits in the second largest. The operator's own
  proposal goes further: require an explicit reply tool call, deliver nothing
  when a turn calls none, and nudge the agent instead — with the bulk of
  `ant/CLAUDE.md` replaced by situation-triggered skill injection. That is
  specced separately and needs sign-off. Its blocking unknown is in this
  entry: the fallback at `routd/turns.go:252` was silent, so nobody knew how
  often a turn calls no reply tool, and that number decides whether the
  inversion costs one extra round trip on rare turns or on most of them.

  **The measurement is now wired.** `arizuko_prose_fallback_turns_total`
  (`obs/metrics.go`) counts that branch per folder. It is a SEPARATE counter
  from `arizuko_silent_turns_total` on purpose: a prose-fallback turn answered,
  a silent turn did not, and folding them together would make the older name a
  lie and corrupt the series `obs/README.md` says to drive to zero. Spec
  `6/44` (`status: draft`) holds the design and names ≥2,000 turns as the
  sample.

  The design pass expects the gate to FAIL as proposed, and the reason is in
  this repo: `ant/CLAUDE.md:85` tells the agent "Only your FINAL text block is
  delivered … write the answer once, at the end" — an instruction to answer in
  prose rather than through a tool. Under that manual the fallback is the
  designed normal path, not an exception, so the inversion would re-run most
  turns. That contradicting line is what to fix before re-measuring. The same
  pass costed the nudge layer honestly: it retires 18 lines / 11.6% of
  `ant/CLAUDE.md`, not its bulk, and regex classification of the inbound is
  refused — routd already classifies the turn, so a second classifier would
  decide the same question from worse evidence.


## L327 — an `#observe` route sorting above its answering twin makes a chat deaf to a direct mention (2026-09-20, open — operator data; the check is done)

MEASURED on sloth 2026-09-20. The operator asked why the agent does not attend
to messages in Discord. The threads are fine — every threaded exchange answered
inside 30 s (19:32:06→19:33:13, 17:06:58→17:07:27, 09:12:51→09:12:59). The
MAIN channel is what is silent, which is the opposite of how it reads from
inside Discord.

`routd.routes` for guild `837270552959516682`:

```
seq 1  main#observe          platform=discord room=837270552959516682/1504474527638224936
seq 1  main/trading#observe  platform=discord room=837270552959516682/920916162416635944
seq 2  main/main             platform=discord room=837270552959516682/*  verb=mention
```

`ResolveRoute` (`router/router.go:357-368`) returns the FIRST match in
`(seq, id)` order, and an `#observe` route carries no verb — so it matches every
message, mentions included. At seq 1 it shadows the guild-wide mention route at
seq 2 completely. Channel `1504474527638224936` has no answering route below it
at all: last human 2026-09-19T09:08:48, bot **NONE**.

**This is the route pair inverted.** The correct shape puts the answering route
FIRST and the `#observe` twin one seq below — sloth's own Slack rows do it
right: `atlas/support verb=mention` at seq 10, `atlas/support#observe` at
seq 11. The Discord rows are the other way round.

**The chat that DOES answer is not a counter-example.** `920916162416635944`
replied at 19:30:07 to a message whose whole content was "sloth", and at
19:33:13 inside a thread the agent was already engaged in — so engagement, not
the route table, is what woke it.

- **Severity:** medium — a channel reads as configured-to-answer and answers nothing, and a direct mention does not override it
- **Scope:** sloth's `routd.routes` rows (operator data); `.claude/skills/eval/checks.md` (the missing check)
- **Affected:** sloth, measured; the shape is possible on any instance
- **Source:** sloth `routd.routes` and `routd.messages`, read 2026-09-20
- **Status:** open. Two halves:
  1. **Operator data** — add an answering `verb=mention` route for the two
     channels at a seq BELOW the observe rows, or renumber the observe rows
     above the guild mention route. NOT done: which channels should answer is
     the operator's call, and this entry does not guess it.
  2. **The check — DONE.** `/eval` check 22 already described the shape and the
     glob trap; what it lacked was a query that ANSWERS it instead of asking
     the reader to spot the pair in a sorted list. It now carries one, proven
     against sloth: it returns exactly the two inverted Discord rows and leaves
     the correctly-ordered Slack pairs alone.

     `router.Describe`'s `shadowed_by` cannot report this and should not be
     asked to. It tests a synthetic message against earlier routes and collapses
     a glob predicate to a filler token so it never over-claims a shadow — so an
     earlier EXACT room intercepting a later glob is its designed blind spot.
     Reporting the partial case needs a SEPARATE annotation ("narrowed_by"),
     which is a new field on a REST/MCP view: a proposal, not this fix.

## L326 — the smoke liveness probe measures the queue, not liveness (2026-09-20, PARTLY FIXED 2026-09-20; the barrier needs sign-off)

`rest-roundtrip` (`anteval/cases/anteval.toml:228-236`) is the ONLY `rest_reply` case
in the catalog and therefore the whole of what `anteval --smoke` — gate 5, the one gate
that reaches a live agent — knows about whether anyone answers. It carries
`max_wall_ms = 90000`.

That cap is a measured false-fail source, not a hypothetical one.
`.claude/skills/eval/gates.md:56-60`: the same build scored "5/9 during the storm and
8/9 after it drained, and every one of the three failures was the agent answering
correctly but past the harness's wall — `rest-roundtrip` replied 5 minutes after a 94s
cap." The runbook's fix is a human instruction to drain first, which makes the gate's
verdict depend on an operator remembering a precondition.

A liveness probe that reports the agent when the queue is deep is the same class of
lie this queue's L149 entry names from the other side: a gate that is green when it
should be red, now also red when it should be green.

- **Severity:** medium — gate 5 is the release gate, and a false red trains an operator to re-run it rather than read it
- **Scope:** `anteval/cases/anteval.toml`; `anteval/pkg/run/run.go` budget handling
- **Affected:** every tagged release
- **Source:** measured 2026-09-20 at `bfcd70f9`; the 5/9-vs-8/9 run is `gates.md`'s own 2026-09-19 measurement
- **Status:** open — the READABLE half shipped; the honest fix needs sign-off on a
  new machine-readable in-flight-depth surface before `runCase`'s deadline loop can
  stop its clock (see below — it is NOT `settle`, and raising the cap only trades run
  time for signal). `TestSmokeKeepsALivenessProbe`
  (`anteval/pkg/spec/catalog_test.go`) now pins that the probe keeps EXISTING; it says
  nothing about whether its budget is honest.

- **The READABLE half is fixed 2026-09-20.** The verdict now names the wall it
  waited for: `timeout after 1m30s: no callback` rather than `timeout: no
  callback`. That is the difference between the two readings an operator has to
  choose between — a dead agent, which blocks the release, and a queue that has
  not drained, which is a precondition.

  It is the same shape `requiresHint` already had and for the same reason: a red
  that does not say what it waited for is a red the reader cannot act on, and
  this entry's own severity is "trains an operator to re-run it rather than read
  it". Naming the budget does not make the gate honest; it makes its answer
  legible, which is the part that needed no contract change.

  PROVEN BY MUTATION: restore the bare `"timeout: "` and
  `TestTimeoutReasonNamesTheWallItWaitedFor` fails. A second test pins that the
  default wall prints as `1m30s`, so a case declaring none cannot report a
  number it did not wait on.
- **Still open, and it is the honest fix** — but re-measured 2026-09-21 at
  `f5851f94`, and the sentence above named the WRONG FUNCTION. There is a
  `settle` (`anteval/pkg/run/run.go:158`) and it is not this bug:

  - `settle` writes `r.Note` and nothing else (`run.go:99`), and `report.Note`
    is typed "a run-level warning that is NOT a case verdict"
    (`anteval/pkg/report/report.go:22-24`); `AllPassed`/`Summary` read only
    `Pass`. So `settle` cannot have produced 5/9-vs-8/9.
  - `settle` ALREADY prefers a completion signal to a wall: it polls
    `cfg.Target.Cost(turnID) > 0` — the model gateway metering the turn — and
    the 180s is the fail-safe for an instance whose gateway has no meter. Its
    comment traces to BUGS `L68`: a case starting behind a still-running turn.

  **A "make the settle barrier wait on queue depth" patch would edit `settle`,
  fix nothing, and weaken `L68`'s guard.** That is the shape this queue keeps
  re-filing: a prescribed fix that reopens what a later commit closed.

  The wall that actually decides pass/fail is the UNNAMED per-case deadline
  loop in `runCase` (`anteval/pkg/run/run.go:199-220`), gated on `budget(c)`.
  It carries this entry's own `L326` comment. That loop is the fix target.

- **Queue depth is not observable, so this is a new capability and not a
  config change.** MEASURED 2026-09-21: `queue.GroupQueue.ActiveCount()` exists
  but its only two non-test callers are `routd/steer.go:189` and `:262` — the
  `/ping` and `/status` CHAT commands, text for a human. `runed`'s
  `Manager.ActiveCount` has exactly one non-test caller, a shutdown log line
  (`runed/cmd/runed/main.go:211`). No REST route, no MCP tool, no metric. The
  harness's own `Target`/`check.Reader` interfaces (`run.go:23-33`,
  `check.go:36-39`) expose `Inject`/`Cost`/`Session`/`RestMessages`/
  `McpMessages` — nothing about in-flight work.

  So the honest fix is two changes, and the first is the one to sign off:
  1. Expose in-flight depth on a machine surface — `ActiveCount` is already the
     gauge `/ping` prints, so this is one resreg resource over an existing
     reader, not a new count. One declaration, two readers (the chat command
     and the harness).
  2. Make `runCase`'s deadline loop stop the clock while depth is non-zero, so
     the budget measures the agent rather than the queue.

  Until (1) exists, raising the cap is the only lever, and it trades run time
  for signal without making the verdict honest. The runbook's drain-first
  instruction (`.claude/skills/eval/gates.md`) stays load-bearing meanwhile.

## L319 — arizuko never provisions a store identity, so a shared store server has no enrolment path (2026-09-20, open)

**The shared server is now live, so this is the only thing between it and use.**
`extenddb 0.1.11` serves `0.0.0.0:18445` on hel1v5 against `dynamo` /
`dynamo_catalog` at `10.0.5.1`, and `extenddb verify` reports `HEALTHY` (spec
`6/43`, 2026-09-20). Nothing is pointed at it, because pointing an instance at
it means creating that instance's account and minting its key pair, and that is
exactly what no subcommand does.

MEASURED 2026-09-20. arizuko reads five env values from the store server and
calls nothing else: `DYNAMODB_URL`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `STORE_CA` (`objstore/dynstore/env.go:16-22`). A grep
over the tree for `create-user`, `create-access-key`, `put-user-policy` and
`EXTENDDB_ACCOUNT_ID` returns no Go hit; the only `EXTENDDB_*` strings are test
container flags in `dyntest.go`. The account and the admin password sit in
`.env` for a person to type into `extenddb manage` — persisted as
`STORE_ACCOUNT_ID` and `STORE_ADMIN_PASSWORD` (`INSTALL.md:188`), not under the
`EXTENDDB_*` names this entry first used; `EXTENDDB_PASSWORD` is only the
ephemeral variable the `manage` CLI reads at invocation.

`generate` PRESERVES and never mints: for each daemon it reads the key out of
`env/<daemon>.env` and carries it forward, skipping the daemon when either half
is absent (`compose/compose.go:882-901`). The comment says why — a daemon whose
key changed under it signs every request wrong.

`template/services/extenddb.yml` states the design outright: the operator runs
`extenddb init` ONCE, by hand, and that single run makes both databases, the
TLS certificate, the account and every daemon's key. It ends "an instance moves
once and a printed-once password has to reach `.env`, so that run belongs to a
person, not to a subcommand."

That reasoning holds for one server per instance. It does not hold for one
server serving the fleet, which needs a per-deployment step that does not exist:
`manage create-account`, then one `create-user` + `put-user-policy` +
`create-access-key` per daemon, against a server that already exists.

**RE-MEASURED 2026-09-21 at `f5851f94`. The "two further things, both blocking"
paragraph that stood here is STALE — both were answered the same day this entry
was filed, five hours later.** `63050bfb` added `INSTALL.md` §"A shared server
for several instances" (`INSTALL.md:291-390`), which documents the manual
procedure for exactly the two:

- **The SAN.** Step 3 runs `init` for the certificate alone with
  `--tls-san <host-ip> --tls-san <host-fqdn>` instead of the service name
  (`INSTALL.md:329`). The per-instance fragment's SAN really is the compose
  service name (`template/services/extenddb.yml:9`), but that is the
  per-instance cert; a shared server is brought up with reachable SANs and an
  enrolling instance takes THAT certificate as its `STORE_CA`. It is a
  data-movement step, not an unsolved problem.
- **The PostgreSQL role.** Step 1 creates a single `dynamo` role owning
  `dynamo` / `dynamo_catalog`, and no per-instance role exists in that model at
  all. `6/43` §4 point 4 already makes this normative — "The PostgreSQL role is
  **not** a tenancy boundary … A deployment never holds a PostgreSQL credential"
  — so this is a settled decision to implement, not an open question.

That section's own closing line already scopes this entry down: "Creating that
account and minting that key is still by hand through `extenddb manage`:
`arizuko` has no enrolment subcommand (BUGS `L319`)" (`INSTALL.md:388`). The doc
commit never touched `BUGS.md`, which is why the entry kept claiming blockers
that had a documented answer.

**So the remaining scope is exactly the automation, and NOTHING here is bounded
today.** What enrolment would concretely write, verified 2026-09-21:

- It would register in `cmd/arizuko/main.go`'s command table beside `generate`
  and `packages` — there is no `enrol`, confirmed against the full list (`run`,
  `generate`, `proxy-config`, `create`, `github`, `group`, `gate`, `invite`,
  `chat`, `send`, `talk`, `status`, `pair`, `network`, `route`, `packages`,
  `products`, `secret`, `user-secret`, `budget`, `token`, `apply`, `plan`,
  `get`, `export`, `archive`, `probe`). Not `apply --as-folder`, which
  retargets FOLDERS and is unrelated despite the name.
- It would run `manage create-account`, then per daemon `create-user` +
  `put-user-policy` + `create-access-key` (the `L318` recipe), and write the
  pair into `env/<daemon>.env` — the only file `generate` will carry forward.
- `DYNAMODB_URL`, `AWS_REGION` and `STORE_CA` already flow from `.env` into
  every daemon's file via `commonKeys` (`compose/compose.go:95-99`); only the
  key pair is per-daemon and unminted.

It needs a live server, an admin password, and `L318`'s policy shape settled, so
it stays a proposal.

**One adjacent BOUNDED candidate, found while re-measuring, NOT shipped here
because it is a second concern.** `generate`'s carry-forward loop skips a daemon
SILENTLY when either half of the key is absent:

```go
if keyID == "" || secret == "" {
        continue                        // compose/compose.go:894
}
```

**The visibility half SHIPPED 2026-09-22, and the contract question answered
itself.** `generate` now reports a half-finished enrolment by name, reading
`StoreReach` for who needs a key (`timed`, `ttsd` need none) rather than keeping
a second list. That is the `L45` class made visible: a generated value that
silently stops applying while the instance looks healthy.

The reason it was held back — "it must not fire on a fresh `arizuko create`" —
turned out to be a rule, not a decision. NONE enrolled is a fresh create and
stays silent. ALL enrolled is done and stays silent. Only the MIDDLE is a fault,
and that is exactly the state an interrupted enrolment leaves.
`TestPartialStoreEnrolmentIsLoudAndAFreshInstanceIsNot` pins all three, and
each of the two guard conditions fails it when mutated.

- **Severity:** medium — no instance is broken; a shared store server cannot be built without this
- **Scope:** `cmd/arizuko` (a new subcommand); `compose/compose.go:882-901` carry-forward; `INSTALL.md` §"A shared server for several instances"
- **Affected:** any second instance on one store server; the 10.0.5.1 consolidation
- **Source:** `objstore/dynstore/env.go:16-22`; `compose/compose.go:882-901`; `INSTALL.md:291-390`; `specs/6/43` §6 (which already carries `defects: [L319]`)
- **Status:** open — the AUTOMATION only; the visibility half is shipped (above). Needs sign-off on the shape: an `arizuko enrol <instance>` subcommand against a shared server, minting the account and the scoped keys `L318` specifies, in one loop. It writes the identity plane arizuko today leaves to a person. The manual path is documented and works; only the automation is missing.
- **Fix:**

## L318 — every daemon's store key carries `dynamodb:* on *`, so 19 per-daemon keys give attribution and not isolation (2026-09-20, open)

MEASURED on sloth 2026-09-20. The instance mints one IAM user and one access
key per daemon — 19 of them, each in its own scoped env file (`routd.env`,
`teled.env`, …) and none in `.env`, so a daemon reads only its own key. The
separation stops there. Every one of those users carries the same `tables`
policy:

```json
{"Version":"2012-10-17","Statement":[
 {"Action":"dynamodb:*","Effect":"Allow","Resource":"*"}]}
```

So `ttsd`'s key can read `routd.messages`, and `teled`'s can drop
`routd.audit_log`. The key answers "who wrote this row", never "may you". The
account boundary is real — `tables` is keyed `(account_id, table_name)` and a
request authenticates into one account — but inside an account there is no
boundary at all.

This costs little today, where every daemon is ours and the blast radius is one
instance. It costs more under a SHARED store server, because the account then
becomes the only isolation left: the per-instance PostgreSQL role, which today
is a superuser owning that instance's own server, no longer exists.

- **Severity:** medium — an adapter compromise reaches every owner's tables in that instance
- **Scope:** `extenddb init`'s per-daemon `put-user-policy`; `INSTALL.md` § The store server
- **Affected:** sloth is the drift, krons is the template. Re-measured by the
  operator 2026-09-20: sloth holds 19 `tables` policies with ONE distinct
  document, `{"Action":"dynamodb:*","Resource":"*"}`; krons holds 9 `owners`
  policies genuinely bounded to `routd.*`, `onbod.*`, `teled.*` and so on. The
  original "Affected: krons" was backwards. marinade unverified. (Live-instance
  figures are the operator's measurement, not this repo's.)
- **Source:** `iam_policies` on sloth, 38 rows: one `SelfServicePolicy` and one `tables` policy per user, the latter identical for all 19
**The owner map exists — this entry first claimed it did not.** `store/owners.go`
registers four owners (`authd`, `onbod`, `routd`, `runed`), `dynstore.Prefix`
puts the owner in front of every entity name, and `chanlib/dedup.go` gives each
adapter a `<service>` prefix of its own. sloth's catalog agrees: `routd.*` 33
tables, `authd.*` 7, `onbod.*` 6, `runed.*` 4, `discd.*` 2, `teled.*` 2. So a
policy CAN name what a daemon may reach; nothing had to be invented.

Measured from the call sites, not from the `OpenOwner` comment above them:

| daemon | owners it opens |
| --- | --- |
| `routd` / `runed` / `authd` | its own, only |
| `onbod` | `onbod`, `routd` |
| `dashd` | `onbod`, `routd` |
| `webd` / `proxyd` / `gitd` / `slakd` | `routd` |
| the operator CLI | `authd`, `onbod`, `routd` |
| every adapter | its own `<service>` dedup prefix |

`timed` opens none: it writes through the owner's HTTP API, which is the split
write-discipline working.

**The code half is DONE 2026-09-20; the rollout is not.** Nothing in the repo
generated, documented or checked these policies — the only mention was one
sentence in `INSTALL.md` § The store server, and it had already drifted: it gave
`dashd` a policy on `runed.*`, which dashd has never opened. A shape that lives
in prose beside no check is a shape that is already wrong.

Now declared and checked:

- `store/owner_reach.go` `StoreReach` declares daemon → owner prefixes, beside
  the owner registry that the prefix comes from.
- `store/owner_reach_test.go` `TestStoreReachMatchesTheCallSites` walks the
  module for the store doors and fails on either direction — a daemon that opens
  an owner the map omits, and a daemon declared for an owner it never opens. The
  measured map matched the declaration on the first run, which independently
  confirms the table this entry already carried.
- `TestInstallDocumentsTheReach` asserts `INSTALL.md` carries the same rows, and
  prints the rows to paste when it does not. One declaration, two readers: the
  test and the operator.
- `INSTALL.md` § The store server now carries the table, the bounded policy
  document, and the `create-user` / `put-user-policy` / `create-access-key`
  commands, replacing the sentence that described them.

**The two the rollout must not get wrong**, and the reason the table beats
"adapters get their own prefix": `gitd` and `slakd` open `routd` directly
(`gitd/main.go`, `slakd/main.go`) ON TOP of their own adapter prefix, so an
adapter-prefix-only policy leaves them authenticated and unable to read
anything. `timed` and `ttsd` need no key at all. A too-narrow policy does not
fail at deploy — it fails the first time the daemon touches the table, which is
why the rollout is one daemon at a time and watched.

Each assertion was mutation-checked: declaring `runed.*` for dashd, dropping
`routd.*` from gitd, removing slakd from the map, giving ttsd a key it does not
use, and drifting the INSTALL.md row each turn the matching test red.

**RE-VERIFIED 2026-09-21 at `f5851f94`.** The code half holds: `StoreReach`
(`store/owner_reach.go:32`) declares 18 daemons, and both tests pass —

```
=== RUN   TestStoreReachMatchesTheCallSites
--- PASS: TestStoreReachMatchesTheCallSites (0.09s)
=== RUN   TestInstallDocumentsTheReach
--- PASS: TestInstallDocumentsTheReach (0.00s)
```

The test binds in THREE directions, not two: a daemon that opens an owner the
map omits also fails (`owner_reach_test.go:173`), so `whapd`, `twitd` and
`kokoro` being absent from the map is verified — they open no store — rather
than forgotten.

**Nothing here is bounded as code.** No Go code builds or applies the policy
document; the JSON body exists only as literal text in `INSTALL.md`. So the
remaining work is an operator rollout, in full, and the commands belong here
rather than in a fix.

**The Status line below used to say "`gitd` first, which reads `routd` and
nothing else". That is WRONG, and it contradicts this entry's own body four
paragraphs up.** `gitd` opens TWO prefixes: `store.OpenOwner(store.OwnerRoutd)`
at `gitd/main.go:34` AND `chanlib.OpenAdapterStore("gitd")` at `gitd/main.go:45`
— which is exactly what `StoreReach` declares, `"gitd": {OwnerRoutd, "gitd"}`.
An operator who copied that sentence would write a policy one resource short,
and `gitd` would authenticate and then fail on its own dedup table — the
too-narrow failure this entry already warns does not surface at deploy.

**The rollout, per daemon** (`gitd` shown, and it is still the right one to go
first — two resources, no other daemon reads its tables). `STORE_ACCOUNT_ID` and
`STORE_ADMIN_PASSWORD` come from the instance `.env`:

```bash
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  create-user --account $STORE_ACCOUNT_ID --name gitd

sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  put-user-policy --account $STORE_ACCOUNT_ID --name gitd --policy-name tables \
  --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:*","Resource":["arn:aws:dynamodb:*:*:table/routd.*","arn:aws:dynamodb:*:*:table/gitd.*"]}]}'

sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  create-access-key --account $STORE_ACCOUNT_ID --name gitd
```

Write the printed pair into `env/gitd.env` as `AWS_ACCESS_KEY_ID` /
`AWS_SECRET_ACCESS_KEY` — the only place `generate` will read it back from
(`compose/compose.go:882-901`). Then restart that one daemon and watch it before
the next. The resource array for every other daemon is `StoreReach`'s row for it,
one `arn:aws:dynamodb:*:*:table/<prefix>.*` per entry.

**Headcount: the repo's design says 16 keys, not 19.** `StoreReach` has 18
entries, of which `timed` and `ttsd` are empty — 15 daemons plus the operator
CLI need one. The "19" in this entry's title and body is the operator's LIVE
measurement on sloth and is not derivable from this repo; do not use it as a
rollout headcount.

- **Status:** open — the ROLLOUT only; the resource list is declared and checked,
  so the work is not the design, and no code change is pending. It rewrites live
  IAM policies on a running fleet, so it ships one daemon at a time against krons
  — `gitd` first, whose policy carries BOTH `routd.*` and `gitd.*` — each proven
  before the next. `slakd` is the same two-prefix shape; `timed` and `ttsd` get
  no key at all.
- **Fix:**

## L317 — `/stop` no longer cancels a question typed just before it (2026-09-20, open — needs a decision; re-measured 2026-09-21 after `L336`)

Fallout of the `L104` fix, named rather than discovered late. `/stop` kills the
running container (`routd/steer.go` `cmdStop`) and has never meant "discard my
backlog". Before `L104` it cancelled a pending question anyway, because the
steer advanced the cursor past EVERY older row in the batch — the bug. Now
"do X" then `/stop` kills the current container and X falls through to a new
turn and runs.

The old behaviour was the right answer for `/stop` and the wrong answer for
every other command, which is why the fix went the way it did. `/stop` is the
one place where a user plainly wants the backlog dropped too.

**RE-MEASURED 2026-09-21 at `f5851f94`, after `L336` changed the selection.**
`steerRowIdx` now takes the OLDEST slash command in a batch rather than the
newest non-bot row, so the shape this entry describes had to be re-read rather
than assumed. Three batches driven through `processGroupMessages` with a
`RunStopper` runner:

| batch | `/stop` fires | turns dispatched | cursor lands on |
| --- | --- | --- | --- |
| `do X`, `/stop` | yes | 1 — `do X` | `/stop` |
| `/stop`, `do Y` | yes | 0 | `/stop` |
| `do X`, `/stop`, `do Y` | yes | 1 — `do X` only | `/stop` |

So the defect is unchanged in its own shape — the question typed BEFORE `/stop`
still runs — and `L336` made it reach FURTHER. Row 3 is the new part: before
`L336` the newest row (`do Y`) was the steer candidate, `/stop` was never
steered at all, and it reached the agent as plain text killing nothing. `/stop`
now fires reliably, and every older row still dispatches. The entry got worse,
not stale.

**The table is pinned in code 2026-09-22.**
`TestStopLeavesTheBacklogRunning` (`routd/steer_commands_test.go`) drives all
three batches through `processGroupMessages` with the `stopRunner`, and it
asserts the DEFECT on purpose. It passes at HEAD, so the table above survived
the `dispatch.go` changes of 2026-09-21 — the route/steer split especially —
and the decision below rests on measured behaviour rather than on a table
somebody typed once. Whichever way the contract is settled, that test is what
has to change, which is how the change announces itself.

**The fix as originally written would re-open `L104`.** "Advances the cursor
past the whole batch it steered from" was written when the steered row was the
NEWEST row, where "the whole batch" and "everything older" were the same set.
Under `L336` the steered row is the OLDEST command, so the whole batch now
includes rows NEWER than the `/stop` — row 3's `do Y`, typed deliberately after
the cancel. Discarding those is exactly the harm `L104` names, in the other
direction, and it contradicts `steerRowIdx`'s own comment: "rows after it stay
above the cursor for the next poll — nothing is skipped in either direction
(BUGS L104)".

The only defensible variant is therefore **drop what is OLDER than the `/stop`
row and nothing else** — skip the turn for `msgs[:steerIdx]` in
`routd/dispatch.go`, where the steer branch already truncates to exactly that
slice. That is a one-line change, not a redesign, once the contract is signed
off.

**What sign-off has to answer**, and it is not "should /stop be tidier":

1. **Whose rows does it drop?** `msgs[:steerIdx]` is not filtered by sender, and
   the non-web path fans out per sender (`groupBySender`, `routd/dispatch.go`).
   In a group chat one person's `/stop` would discard every other person's
   pending question. Either the drop is sender-scoped to the stopper or `/stop`
   becomes a command one member can use to silence the others.
2. **Is it still the same contract as every other command?** No command in
   `steerCommands` consumes a row it did not author. `/new` goes the other way:
   it RE-INJECTS its argument as a fresh message row (`cmdNew`,
   `routd/steer.go:440`). `/stop` dropping the backlog would be the only
   command that eats rows, which is why it wants sign-off and not a default.
3. **Does the user get told?** A silent drop of a question the user typed is the
   failure mode this queue keeps re-filing. The drop has to ack the count.

- **Severity:** low — one command, and the user can say so in words
- **Scope:** `routd/dispatch.go` steer branch (`msgs[:steerIdx]`); `routd/steer.go` `cmdStop`
- **Affected:** any chat where a person cancels by `/stop` right after asking; a group chat is where it would bite others
- **Source:** named in the `L102`/`L103`/`L104` fix, 2026-09-20; re-measured 2026-09-21 at `f5851f94` after `L336` (`f63957e4`)
- **Status:** open — needs a decision, and the decision is narrower than it was: the question is no longer "clear the backlog?" but "clear the OLDER rows, scoped to whose sender, and acked how?"
- **Fix:** if `/stop` should clear the backlog, it skips the turn for `msgs[:steerIdx]` — the rows older than the command, never the whole batch — and acks how many it dropped. Sender scope is the open half. Advancing past the whole batch is now WRONG: it would discard rows typed after the `/stop` and re-open `L104`.

## L315 — a catalog package's rows freeze at `add` while its files and env keep tracking the catalog, and `upgrade` reports "nothing to do" (2026-09-19, REPORT FIXED 2026-09-20; re-applying the rows stays a proposal)

Found while closing `L199`. A package taken from the bundled catalog is
recorded with `Source: "catalog"`, and its `<name>.yml`/`<name>.yaml` are
symlinks INTO the catalog, so their bytes change with every release. Two of
the three readers follow that: `compose.collectProxydRoutes` and
`compose.ProvisionPackageEnv` re-read the manifest on every `generate`
(`compose/compose.go:562,880`). The third does not — the rows were applied
once, by the `add` that installed them, and `reapplyPackage` returns on its
first line for a catalog source (`cmd/arizuko/packages.go`), so neither
`upgrade` nor `sync` re-reads the manifest at all.

So a release that adds a `scheduled_tasks`, `network_rules`, `acl` or
`web_routes` row to a shipped manifest delivers it to no instance that already
ran `add`, and the operator who asks is told:

```
litellm already at catalog — nothing to do
```

Nothing is lost today: the thirteen shipped manifests declare `proxyd_routes`
and `env` only (measured 2026-09-19 at `b5243bdb`). The day one declares a row
kind, this is `L199`'s harm at the release boundary instead of the `add` one.

- **Severity:** low today; medium the day a catalog manifest declares a row kind
- **Scope:** `packages upgrade`, `packages sync`, the catalog origin
- **Affected:** `cmd/arizuko/packages.go` `reapplyPackage`; every
  `Source: "catalog"` record
- **Source:** read 2026-09-19 at `b5243bdb`: `reapplyPackage`'s
  `if rec.Source == catalogOrigin` early return, against
  `compose/compose.go:562,880`
- **Status:** the report half is FIXED 2026-09-20; re-applying the rows is still
  a proposal, unsigned
- **Fixed (2026-09-20):** `reapplyPackage`'s catalog branch re-hashes the
  record's `<name>.yaml` through `dirtyAssets` and returns what moved, and both
  sinks — `upgrade` and `sync` — print it through ONE renderer (`driftReport`),
  naming the file and saying the rows it declares were applied at `add` and are
  re-applied by no verb. Only the `.yaml` is reported: the `.yml` fragment is
  read through the link at every `generate`, and a copy that fell behind is
  already `relinkCatalog`'s FROZEN warning, so reporting it here would be a
  second path. Spec `specs/5/28-packages.md` states the catalog case under
  `sync`. Test: `TestPackagesUpgradeReportsCatalogManifestDrift`, which fails on
  both sinks against the old branch.
- **REFUTED in the fix text above:** "compare … against `pkgManifestDigest` at
  install time" cannot be done from the record — `pkgManifestDigest` is computed
  at `installPackage` and handed to `pkgApply` as the audit row's correlation
  token (`cmd/arizuko/packages.go`); no field of `routd.InstalledPackage` carries
  it. The recorded `AssetHashes["file:<name>.yaml"]` is the same bytes hashed and
  is already what `dirtyAssets` compares, so the existing mechanism answers it
  with no new field and no second hash.
- **Still open (proposal, NOT shipped):** re-applying rows on `upgrade` is a
  redesign, not a symptom patch: `reapplyPackage` deliberately replaces only the
  file half, because overwriting the recorded row identities would strand the
  rows `remove` must later delete. Doing it properly means withdrawing the
  recorded rows and installing the new set in one transaction — a new contract
  for what `upgrade` means. Today the operator's route is `packages remove` +
  `packages add`, which the report names.

## L308 — `priv-grant` asks the eval folder to delegate a capability it holds without the grant option (2026-09-18, open)

MEASURED on krons 2026-09-18, `anteval --smoke` 8/9. `priv-grant` is the only
failure, and the agent is right — it is the FIXTURE that is wrong.

The case asks the agent to create a child group and "grant it web publishing".
The agent created the child, called `add_acl`, and reported the refusal verbatim:

```
cannot delegate folder:eval/etljzsc-priv-grant-g web:publish on
eval/etljzsc-priv-grant-g: not held WITH GRANT OPTION
```

The acl agrees. `folder:eval` holds `web:publish` on scope `eval` with
`grant_option = 0`, so by `5/33` it cannot delegate onward, and a grant on
`eval` does not reach `eval/sub` anyway. The agent then declined to publish from
its own slot as a workaround, on the grounds that the task names the child as
the publisher — which is the honest answer, not a failure.

The case's `requires` line names `mcp:register_group` and `mcp:add_acl`. Both are
held. The requirement it does NOT state is the one that decides the case:
`web:publish` on `eval/**` WITH GRANT OPTION.

**This is an authorization change, so it is the operator's call.** Either the
eval folder gets `web:publish` on `eval/**` with the grant option — which is what
makes the `subagents` dimension test what it means to test, provisioning AND
empowering a child — or the case drops the delegation half and stops claiming to
cover it. Do not "fix" it by widening what the agent may do silently.

- **Severity:** medium — one smoke case has never been able to pass, and it reads as an agent failure
- **Scope:** `anteval/cases/anteval.toml` `priv-grant`, the krons `eval` folder's acl
- **Affected:** every `anteval --smoke` run
- **Source:** anteval report 2026-09-18 10:05; `routd.acl` `folder:eval web:publish eval allow grant_option=0`
**THE DIAGNOSTIC HALF IS SHIPPED 2026-09-22.** `requires` gates nothing — it is
quoted back on failure so a reader can tell an agent that failed the task from
an agent that was never granted the tool (`anteval/pkg/spec`). The case declared
the two tools and not the grant that actually decides it, so the failure text
named everything except the cause. `web:publish on eval/** WITH GRANT OPTION` is
in the list now. This widens nothing: the case still fails until the operator
chooses, but it now fails saying why.

- **Status:** open — needs the operator to choose the grant or the case
- **Fix:**

## L307 — seven Slack chats carry traffic and match no route — now LIVE on sloth (2026-09-18, open)

`slack:T4PNSRSP7/channel/C4P090XU1` (6 msgs/30d) and
`.../C4PMARTD2` (2 msgs/30d) reach routd and resolve to nothing: marinade's
table routes only `C0B4JMQ8X89`, and there is no Slack wildcard the way there
is for Discord (`chat_jid=discord:*/*`). The messages are stored and dropped.

Either add the `chat_jid=slack:*/channel/* verb=mention` + `#observe` wildcard
pair marinade already has for Discord, or the operator routes each channel.
This is a data decision, not a code fix.

**Re-measured 2026-09-21, and this is now LIVE — my own change activated it.**
The entry said two channels on marinade. marinade is disabled (`L338`) and its
store was consolidated into sloth's, and today I brought `slakd` up on sloth,
so this traffic now arrives at a running adapter.

sloth's `routd.messages` holds eight Slack chats; exactly ONE has routes:

| chat | rows | routed |
| --- | --- | --- |
| `channel/C0B4JMQ8X89` | 335 | yes — `atlas/support` mention + `#observe` |
| `channel/C4PMARTD2` | 352 | **no** |
| `channel/C4P090XU1` | 252 | **no** |
| `channel/C0B835BA9M2` | 65 | **no** |
| `channel/C0B61QK4A9F` | 63 | **no** |
| `dm/D0B43KQG9E2` | 54 | **no** |
| `dm/D0BAKBXKS5C` | 18 | **no** |
| `channel/C0B63J2RBJ8` | 18 | **no** |

822 rows across seven chats route nowhere. While marinade was down this was
dormant; it is not any more — a message in any of those seven is accepted,
stored, and answered by nothing.

The entry's proposed `chat_jid=slack:*/channel/*` glob does NOT cover the two
DMs, which use `/dm/`. Any fix needs both shapes, or two rows.

This is operator data — route rows — so it is the owner's to write, and the
choice is per chat: which of the seven should answer, under what verb, and
into which folder. `C0B4JMQ8X89`'s pair is the shape to copy (`verb=mention`
at the lower seq, `#observe` above it — see eval check 22 for why that order
matters).

## L305 — two live threads in one Discord channel: the second one's mention is never answered (2026-09-18, GROUPING HALF FIXED 2026-09-21; the cursor proposal is unevidenced)

MEASURED on sloth, and it is the cause behind "sloth is not remaining active in
threads which he did not start".

A Discord thread is a TOPIC inside its parent channel's chat — `discd`
`chatAndTopic` maps the thread to `jid = parent, topic = thread id`, which is
right. The dispatch CURSOR is per chat. So two threads in one channel share one
cursor, and an advance made for one takes the other's messages with it.

The sequence, from `messages` on sloth
(`discord:837270552959516682/920916162416635944`):

```
05:53:09  mention  topic 1539271427620995114   -> turn 1546760285610577970 runs
05:53:42  mention  topic 1539223435656302704   -> NO TURN, EVER
05:55:19  status   topic 1539271427620995114   (the first turn answering)
05:55:59  message  topic 1539271427620995114
```

`turn_context` holds two turns for topic `1539223435656302704`, both on
2026-08-18. The 05:53:42 mention is `verb=mention`, `routed_to=main/trading` —
routed correctly, gated correctly, and dropped. The thread then sat silent for
three weeks while the channel's other threads were answered normally, which is
exactly what a user reads as "he stopped attending threads".

`routd/dispatch.go:135` is the site: the batch is grouped by topic and the topic
loop dispatches, then `l.advance(chatJID, last)` moves the ONE cursor to the
last message of the whole batch. A topic that returned busy parks the chat and
does not advance — that guard works. A topic that is simply not dispatched in
this pass is carried past.

The mention gating is NOT at fault and sloth's routes are right: `verb=mention`
to a real target with a `#observe` twin, which is the shape that makes a main
channel require a mention and a thread inherit it.

- **Severity:** high — a directly-addressed question gets silence, and the more
  active the channel the likelier it is
- **Scope:** `routd/loop.go` `advance`, `routd/dispatch.go` the topic loop,
  `store.SetAgentCursor`
- **Affected:** any chat carrying two live topics — Discord threads, Slack
  threads, forked web topics
- **Source:** the message and turn_context rows above, sloth 2026-09-08
- **Status:** proposal — needs sign-off
- **Fix shape:** the cursor is per (chat, topic), not per chat, so an advance
  can only ever move the topic it fed. That is the same shape
  `AdvanceObservedCursor(folder, topic, ts)` already has one line below — the
  observed watermark is ALREADY per-topic, and the dispatch cursor is the one
  that is not. Making them agree is the smallest honest change, and it is a
  schema-shaped decision, so it is recorded rather than shipped.

**Re-measured 2026-09-20: this entry names half the cause, and the fix it
proposes does not close the other half.** The cursor half is confirmed —
`routd/dispatch.go:180` writes one attribute on the chat row
(`store/chats.go:141`) while the observed watermark one line below is already
per topic. The half not written down: for every non-web chat the batch is
grouped by SENDER, not topic (`routd/dispatch.go:129`, `groupBySender` at
`:697`), and the turn takes the topic of the group's LAST row (`:140`). Two
threads from one person collapse into one turn in one thread, which reproduces
the sloth trace exactly. A per-(chat, topic) cursor alone therefore will not fix
this; the grouping key belongs in the proposal before sign-off.

- **The grouping half is FIXED 2026-09-21 (`1c263329`), and it is the half that
  reproduces the sloth trace.** `routd/dispatch.go` now keys the non-web batch
  on the sender AND the topic the turn will dispatch under, read from ONE
  declaration (`turnTopic`) that the grouping key and the loop body share. A
  group can no longer span two destinations and then silently take one of them.
  Reproduced first, before any edit: two mentions, one sender, topics
  `thread-a` and `thread-b`, one poll → **one** turn, `topic=thread-b`,
  carrying thread-a's mention inside its batch. That is the sloth trace
  verbatim.
- **PROVEN BY MUTATION, both directions.** Drop the topic term from the key and
  `TestDispatch_TwoThreadsOneSenderGetOneTurnEach` fails with one turn instead
  of two. Key on the raw `m.Topic` instead of the dispatch topic and both
  `TestDispatch_ARoutePinnedTopicStillTakesOneTurn` and
  `TestDispatch_AStickyTopicStillTakesOneTurn` fail with two turns — a chat
  whose route or sticky pins one topic must still take exactly one.
- **The cursor proposal is now UNEVIDENCED, and that is what changes the
  sign-off.** Its premise is "a topic that is simply not dispatched in this pass
  is carried past". After the grouping fix every topic holding a non-bot row in
  the batch IS dispatched in the pass, so no topic reaches `l.advance`
  undispatched. The three non-advancing exits (`derr`, `busy`, `steered`) re-feed
  the whole batch, and `PutTurnContext` answers `live=false` for a turn that
  already completed, so the re-feed replays nothing. Whoever signs off should
  first be shown a case the grouping fix leaves open.
- **And the cursor change is not the small one the entry implies.** Five of the
  cursor's readers are not batch reads. Two take `GetAgentCursor(chatJID)` as
  "when this CHAT was last active", which a per-topic cursor gives N answers to
  and no rule for choosing one: `sessionIdleExpired` (`routd/loop.go:845`),
  which resets a stale Claude session, and the `new_day` check in
  `emitSystemEvents` (`routd/prompt.go:240`). Two more key the busy backoff on
  the chat rather than the cursor (`busyDeferred`/`noteBusy`), so a refusal in
  one topic parks the other. The fifth is the steer contract: `L104` guarantees
  a steer consumes its own row and leaves the older rows their turn, and that
  guarantee is written in terms of ONE ordered cursor over the chat.
- **One question for the sign-off:** is there a case, after the grouping fix,
  where a topic's rows are carried past undispatched — and if not, does the
  per-(chat, topic) cursor still buy anything worth those five readers losing
  their definition?

## L273 — the approval floor makes 22 tools unholdable for every folder, not only worlds (2026-09-12, open — needs sign-off)

The allow-everything-with-approval default seeds skip rows on `role:member` so a
world's agent is not made to ask permission to `reply`. Deny-wins is absolute,
so those rows make each named tool **unholdable fleet-wide** — for every folder,
not only world-tier ones:

```
hold:mcp:reply          → holds=false
hold:mcp:delete         → holds=false
hold:mcp:schedule_task  → holds=true
```

An operator can still FORBID a reply outright by denying `mcp:reply`. They can no
longer gate one behind a person.

The floor is 22 tools, not the 17 first proposed. Measured against what a world
turn actually mounts — 42 tools, 25 held, 17 free — five reads the agent needs
every turn were being held: `get_work`, `get_thread`, `fetch_history`,
`find_messages`, `inspect_messages`. `invite_list` and `get_web_presence` stay
held, and every write stays held.

This is the price of "seed once on the role" instead of "22 rows per world". The
alternative costs a row per tool per world and drifts the moment a tool is added.
`TestWorld_HoldsOnlyWhatAPersonShouldSee` walks the mounted set and fails if a
new tool lands on the wrong side, which is what keeps the choice honest.

- **Severity:** medium (a capability an operator had is gone; nothing is exposed)
- **Scope:** `store/seed.go` `SeedRoutdFloor`, `alwaysMountedTools`
- **Affected:** every folder on every instance, once the floor is seeded
- **Source:** measured against a world turn's mounted tool set, 2026-09-12
- **Status:** open — needs sign-off
- **Fix:** if gating a reply behind a person must stay possible, the skip rows go
  per world instead of on `role:member`, and the guard test becomes the thing
  that stops them drifting.

## L249 — the archive export carries the config plane, not the runtime plane, so export/import cannot yet carry a daemon's port (2026-09-07, open — proposal, needs sign-off)

MEASURED 2026-09-07. Snapshots of all three live instances were taken with
`sqlite3 .backup`, migrated to routd 0046, then exported with
`arizuko archive export <inst> --quiesced`. All three exports succeed.

`routd.yaml` carries 13 resources — `acl`, `acl_membership`, `github_sources`,
`groups`, `installed_packages`, `network_rules`, `pending_actions`,
`proxyd_routes`, `route_tokens`, `routes`, `scheduled_tasks`, `secrets`,
`web_routes` — plus `routd.messages.jl` (24,013 rows on krons),
`routd.secrets.yaml` and `onbod.yaml`.

Ten routd tables hold rows and travel in no dump (krons counts):

| table               | rows    | plane   |
| ------------------- | ------- | ------- |
| `audit_log`         | 163,750 | history |
| `turn_results`      | 4,242   | history |
| `cost_log_archive`  | 3,632   | history |
| `turn_context`      | 2,954   | runtime |
| `task_run_logs`     | 1,451   | history |
| `chat_reply_state`  | 310     | runtime |
| `sessions`          | 248     | runtime |
| `chats`             | 136     | runtime |
| `system_messages`   | 2       | runtime |

`chats` is the one that decides the port: it holds `agent_cursor`. An import
that does not carry it re-feeds every consumed message, which is the failure
`0044`'s migration order already had to avoid. `sessions.observed_cursor` is
the same shape, and `chat_reply_state` decides whether a reply is still owed.

So the export/import lane `6/37` step 4 relies on covers the operator's
declared configuration and the message history, and stops exactly where the
runtime state begins. Either the four runtime tables gain resreg resources
(`chats`, `chat_reply_state`, `sessions`, `system_messages` — `6/37` already
names three of them plus `chat_proactive`, `group_watchers`, `pane_sessions`,
`user_profiles` as the resreg gap), or the port carries them by a second
mechanism, which is a second format for the same facts.

The history plane is a separate decision: `audit_log` has its own archive
design, and `cost_log_archive` is deliberately left behind by `6/39`.

**NARROWED 2026-09-22: the import half of the runtime plane is already
written.** The 2026-09-07 measurement was taken with `sqlite3 .backup` against
routd 0046 — the SQLite era — so it was re-checked at HEAD. The finding holds:
`archive export` walks resreg subsystems (`cmd/arizuko/archive.go`
`ExportSnapshot`) and carries no runtime table.

**And the import half is NOT already written**, which `L348` measured on
2026-09-22 before deleting it: `RoutdSnapshot`/`RoutdRow` modelled legacy SQL
rows, not the archive's resreg snapshot shape, so it could never have been this
entry's import side. Both halves are still to write. What remains of that
region — `store/coordinate.go` — gives an import side its coordinate builders
for every runtime entity, which is the part that would otherwise be rewritten.

- **Severity:** high (blocks `6/37` step 4 — the per-daemon export/import port)
- **Scope:** `resreg/resources/`, `cmd/arizuko/archive.go`, `specs/6/37`
- **Affected:** every daemon port after `dashd`; a cutover would reset cursors
- **Source:** measured `arizuko archive export` vs `routd.db` table census
- **Status:** open — proposal, needs sign-off
- **Fix:** resreg resources for the four runtime tables, so one lane carries them

## L166 — the MCP outbound row records neither the thread it rooted nor its reply target; the REST row does (2026-09-05, THREAD HALF FIXED 2026-09-20; the reply-target half is a separate question)

`deliverRow` (`routd/turns.go`) stamps `row.Topic = threadRoot` after a
thread-rooting reply "so threadHasBotMessage finds it". The MCP path does not:
`putOutboundRow` (`ipc/ipc.go:696`) stamps `activeTopic(db, folder)` — the
turn's topic, `''` for a parent-channel turn — and `recordOutbound` passes no
`replyToID`, so a delivered MCP reply never records what it replied to.
Measured on sloth: 4 of the 9 `mcp-` rows delivered into thread
`1545760882322182276` carry `topic=''`; only the REST-delivered `out-` rows say
where they landed. Consequences: `threadHasBotMessage` misses an MCP-rooted
thread; the sweep (L169 link 2) has no thread to retry into; "where did this
reply land" is unanswerable from the row; and the 7-day reply/no-reply census
is polluted. Shape: `mcpDeliver` returns the root it used (or
`GatedFns.SendReply` returns a result struct) and `putOutboundRow` stamps it
and `replyToID` on success — one row shape for both paths. Touches `ipc/ipc.go`
(contested) and `routd/mcp.go`.

- **Severity:** medium
- **Scope:** outbound row shape (MCP vs REST)
- **Affected:** every instance
- **Source:** `ipc/ipc.go:669,696`, `routd/turns.go` deliverRow, `routd/mcp.go:247`
- **Status:** proposal (needs sign-off)
- **Fix:**

**Re-measured 2026-09-20: the scope in this entry's title is wrong.** It calls
the fix "the ipc↔routd contract", which reads as a cross-daemon change needing
sign-off. `GatedFns.SendReply` is a plain Go func field (`ipc/ipc.go:35`) with
ONE production assignment (`routd/mcp.go:50`), and `mcpDeliver` already computes
`threadRoot` (`routd/mcp.go:261`) — it just does not return it. Widen the field
to return the root, thread it through `recordOutbound` /
`recordOutboundFailure` / `putOutboundRow` from the single reply site
(`ipc/ipc.go:1216-1221`), and prefer it over `activeTopic` when non-empty. Six
test doubles take the new signature. About 40-60 lines, no contract crosses a
process. L169's MCP half is the same change.

**THREAD HALF FIXED 2026-09-20 (`091b2e3f`).** `mcpDeliver` returns the root it
used and `putOutboundRow` stamps it, preferring the turn's own topic when it has
one — `deliverRow`'s policy, not a second one, so a reply inside an existing
thread is not re-keyed onto a root the platform handed back. Tests
`TestRecordOutboundReply_RecordsTheThreadItRooted` and
`TestRecordOutboundReply_TheTurnsTopicWinsOverTheRoot`; the second is what stops
a counter that always stamps the root from passing.

**The reply-target half is NOT shipped, and it is a real question rather than an
oversight.** `putOutboundRow` documents the current shape: an undelivered row
carries `ReplyToID` so the sweep re-sends against it, and a delivered row leaves
it empty because its own id belongs in `platform_id`. So `ReplyToID` currently
means "this still needs sending to X". Stamping it on a delivered row changes
what the field means for every consumer, including the reply-to-bot →
`verb=mention` promotion. Decide whether the row needs a separate `in_reply_to`
before doing it — do not widen `ReplyToID`.

## L167 — which thread a `reply(replyToId=…)` should root is undecided (2026-09-05, open — proposal; the doc half is done)

The `reply` description (`ipc/ipc.go:1186`) says "pass replyToId to target a
specific earlier message", and `ant/CLAUDE.md` said Discord "has no native
inline threads" and `reply` "shows a Replied to banner" — while discd has rooted
a thread on every reply since 2026-06-11. The operator asked the agent to
"answer them in the respective threads and as replies to the respective
messages"; the agent did what its manual said and every call was refused
(L165). With L165 the text now lands in the current thread without the quote,
and `ant/CLAUDE.md` § Discord threading states the mechanics as built. What the
human asked for — a thread rooted on EACH quoted message — is a policy choice
for `replyThreadRoot` (`routd/turns.go:232`, the single policy site): root on
`replyToId` when it names a parent-channel message other than the trigger.
Decide before changing; it changes what `reply` means for every turn.

- **Severity:** medium
- **Scope:** reply semantics / agent manual
- **Affected:** every Discord instance
- **Source:** `ipc/ipc.go:1186`, `ant/CLAUDE.md` § Discord threading,
  `routd/turns.go:232`
**RE-MEASURED 2026-09-22: the documentation half this entry was titled for is
CLOSED.** `ipc/ipc.go`'s `reply` description now states the built mechanics
rather than a promise Discord cannot keep — "pass it to target a specific
earlier message in the SAME thread. A target outside the current thread still
delivers — the text lands in the thread without the quote banner, because the
platform resolves a reference inside the destination channel only." That is the
sentence whose earlier version sent the agent into a wall of refusals, and
`ant/CLAUDE.md` § Discord threading was corrected in the same direction.

So nothing here is a doc defect any more, and the title said otherwise for two
weeks.

- **Status:** open — proposal, and ONLY the policy question is left: should
  `replyThreadRoot` (`routd/turns.go`, the single policy site) root a thread on
  `replyToId` when it names a parent-channel message other than the trigger?
  That is what the operator asked for, and it changes what `reply` means for
  every turn on every platform, so it is not a default anyone should pick
  silently.

## L171 — proposal: one TypeScript channel module for whapd and twitd (2026-09-05, proposal — needs sign-off)

Measured at `c0594f32`: `auth.ts` (126 lines) and `log.ts` (10) are
byte-identical between the two adapters; `dash.ts` differs in 51 of 154
lines, `client.ts` in 40 of 74; `server.ts` differs in 505 lines of 386-513 —
platform code, not a copy. The shared surface is ~300 lines, not the 1,236
spec 6/36 §2 counts. Removing it means a package both Dockerfiles build, a
tsconfig path in each adapter, and one owner for the routd-facing ES256
verifier in TypeScript — a new package boundary, so a redesign. 6/36 open
question 6 (share a TypeScript chanlib, or move twitd to Go) decides it.

- **Severity:** low
- **Scope:** whapd, twitd
- **Affected:** whapd, twitd, the `Makefile` image targets
- **Source:** `whapd/src/{auth,log,dash,client}.ts` against `twitd/src/`
- **Status:** proposed
- **Fix:**

## L159 — a scheduled task fires nightly into a Telegram chat teled already classified as permanently dead (2026-09-05, open — proposal; the filed trigger is UNSAFE, a narrower one is named 2026-09-21)

krons `task-1776079794467-4c33dcb2` (`/compact-memories episodes day`, owner
`mayai`, `0 2 * * *`, isolated, created 2026-04-13) targets
`telegram:group/5161994241`. Telegram answers `Bad Request: chat not found` to
every typing call (10 WARN per run at 02:00; 33 in the 7 days before the
deploy) and `permanent: status 400` to the one send that was attempted
(`deliver send failed`, 2026-09-01 04:05:25). The run still spawns a 43 s
container each night. marinade shows the same shape at 02:00:
`telegram:group/1003805633088` (`Forbidden: the group chat was deleted`) and
`telegram:group/5012430759` (`chat not found`). teled names the verdict
(`permanent`) and nothing consumes it: no chat, route or task is retired, and
the operator sees nothing.

- **Severity:** medium-low (one wasted container run per dead chat per night; a task that can never deliver)
- **Scope:** teled terminal errors → routd chats / scheduled_tasks lifecycle
- **Affected:** krons (`mayai`), marinade
- **Source:** journal krons 2026-09-05T02:00:26-02:05:42; `routd.db` `scheduled_tasks`
- **Verified 2026-09-05:** `task-1776079794467-4c33dcb2` is still present on
  krons with `chat_jid = telegram:group/5161994241`. And the verdict really has
  no consumer: `chanlib.ErrPermanent` (`chanlib/handler.go:149`) is read only by
  the OUTBOUND retry bound — `routd/loop.go:432` dead-letters the message row —
  and nothing anywhere reads it to touch `chats`, `routes` or `scheduled_tasks`.
  The chat stays live, the task stays armed, the container runs nightly.
- **UNSAFE TRIGGER, confirmed 2026-09-21 — do not sign off the trigger as
  filed.** "A permanent send error" cannot carry this decision. `teled`
  collapses the whole class: `classifySendErr` (`teled/bot.go:497`) maps EVERY
  `tgbotapi.Error` with code 400 or 403 to `chanlib.ErrInvalidRequest`,
  `chanlib/handler.go:539` writes it as HTTP 400, and
  `chanreg.classifyStatus` (`chanreg/httpchan.go:61`) turns any 4xx into
  `ErrPermanent`. So `Bad Request: chat not found` and `Bad Request: message to
  be replied not found` arrive at routd as the same verdict. Retiring a chat on
  that would pause a LIVE chat's scheduled tasks on one malformed `reply_to`.
  The classification itself is correct for its actual job — both really are
  permanent for that message — it is just too coarse to retire a chat with.
- **NARROWER TRIGGER, and it is structurally immune to that.** The typing call.
  `sendTyping` (`teled/bot.go:758`) issues `sendChatAction` with a chat id and
  an action and NOTHING else — no text, no `reply_to`, no parse mode, no
  length. A 400/403 there cannot be caused by a malformed message, because
  there is no message; it can only be about the chat. This entry's own evidence
  is already that signal (10 WARN per run, `chat not found`), and it repeats on
  its own: `TypingRefresher` retries every 5 s and keeps going on failure
  (`chanlib/typing.go:83`). N consecutive chat-scoped typing failures is the
  same shape `chanreg` already uses for channel health
  (`maxHealthFails = 3`), so the trigger needs no new counting mechanism —
  only a classification at the typing site, which today just logs WARN.
- **Two conditions on the consumer, whatever trigger is chosen.** It must be
  REVERSIBLE: `bot was blocked by the user` and `bot was kicked` are chat-scoped
  but undoable, so a successful inbound or send has to revive the chat, or a
  restored chat stays retired with its tasks paused. And the retirement must be
  visible to the operator at the moment it happens — this entry exists because
  a verdict with no consumer is invisible, and a consumer with no notice would
  repeat that from the other side.
- **Status:** open — proposal, needs sign-off. The trigger is now settled
  enough to sign: chat-scoped typing failure, repeated, not "a permanent send
  error". The CONSUMER is what still needs the decision — marking the chat dead
  in `chats`, pausing its scheduled tasks and showing it on the dashboard spans
  teled, routd's chat/task tables and dashd, so it is not shipped inline.
- **Fix:**

## L161 — every deploy logs `execution plane unreachable` at ERROR and ends a live turn with 137 (2026-09-05, log half FIXED 2026-09-05; the stop half is a proposal)

At each restart routd polls before runed listens: `dispatch run transport
failure` → `execution plane unreachable; group work re-feeds on next poll`
(krons 2026-09-03: 26 lines in the 20:44-20:45 restart, 6 at 21:24; sloth
2026-09-04 14:30: 4). The work is re-fed, so nothing is lost, but the line is
ERROR for an expected startup race — the class `L25` fixed for slakd
(`depends_on` with no `condition: service_healthy`). On the stop side sloth's
`main/trading` turn was 22 s in when runed stopped (14:30:05 → 14:30:14) and
ended `container exited with error code:137`; routd re-fed it after the restart
(answered 14:30:57), so that turn's model spend was paid twice.

- **Severity:** low (ERROR noise on every deploy; one duplicated turn per live group per deploy)
- **Scope:** compose start ordering; runed stop
- **Affected:** all instances
- **Source:** journal sloth 2026-09-04T14:30:13-23; `runed/cmd/runed/main.go:152-179`
- **Correction to this entry.** `L25` did NOT fix this class with
  `condition: service_healthy` — it shipped `chanlib.RouterClient.RetainInbound`
  (a durable `inbound_pending` table). `service_healthy` appears NOWHERE in the
  tree; the compose fragments all use the plain `depends_on: [routd]` list form.
  So there is no precedent to follow, and the ordering fix was rejected on its
  own merits: `routd` already declares `depends_on: [authd, runed]` and both
  carry `healthBlock` (`interval: 30s`, `start_period: 15s`), so gating routd on
  runed's health would delay the INGRESS by 15-45 s on every deploy and make
  routd unstartable whenever runed is unhealthy. routd is what adapters deliver
  into; keeping it down longer trades a log line for lost inbound.
- **Status (log half): fixed.** The defect is the LEVEL, not the retry: the work
  re-feeds, which is the correct handling of a transient remote failure. The log
  site could not tell a restart from an outage, so it shouted on both. The queue
  now tracks the outage itself (`depDownSince`/`depDownLoud`, queue-wide — the
  plane is not per-group): WARN inside `dependencyDownGrace`, ERROR exactly ONCE
  when the outage outlives it, WARN thereafter, INFO on recovery. The window
  cannot be a failure COUNT — 26 groups fail in the same poll, so a fleet
  crosses any count immediately.
- **Status (stop half): proposal, needs sign-off.** A live turn SIGKILLed at
  runed's stop (`exit 137`) is re-fed and its model spend paid twice. Draining
  it means runed's stop path waits on the in-flight spawn (or records the kill
  so the re-feed is attributed) — a change to the shutdown contract across
  runed and routd, so it is not shipped here.
- **Fix (log half):** `fix(queue): level the unreachable-plane log by outage, not by group`

## L155 — the skill-guard quarantines 74 of arizuko's own 544 shipped skill files, `migrate/SKILL.md` first; `agent_config_mod` fires `critical` on the STRING "CLAUDE.md" (2026-09-03, FIXED 2026-09-20 — 20 of 551 → 11, and no shipped `SKILL.md` blocks; the 11 are true positives, left blocked on purpose)

The krons agent reported it in chat before any audit did: "`/migrate` … 14
quarantined by the platform skill-guard (which I did not circumvent)", and a turn
later "76 stock source files". Reproduced against the shipped corpus by running
the guard's own `blockingVerdict` over `ant/skills`:

```
74 of 544 shipped .md files would be quarantined
  agent_config_mod        108 hits across 65 files
  reverse_shell             6 hits across 5 files
  env_exfil_fetch           3 hits across 1 files
  setuid_setgid             2 hits across 2 files
  dns_exfil / destructive_root_rm / curl_pipe_shell   1 each
```

**`agent_config_mod` is 88% of it, and it fires on a MENTION.** On
`skills/migrate/SKILL.md` — 16 critical findings, every one of them:

```json
{"patternId":"agent_config_mod","severity":"critical","category":"persistence",
 "line":5,"match":"CLAUDE.md",
 "description":"references agent config files (could persist malicious instructions across sessions)"}
```

The match is the literal string `CLAUDE.md`. Migrating `CLAUDE.md` is what the
migrate skill IS, so the skill that delivers the corpus is the first file the
guard eats. A scanner that cannot distinguish USE from MENTION will always fire
hardest on the documentation of the thing it guards.

**Blast radius: every group, every instance.** `bashSkillRescan`
(`ant/src/guards.ts:149`) sweeps `guardedRoots(home)` after any Bash call and
renames anything blocking to `.quarantined` — and a skill only runs if it is
discoverable at its own path. So `/migrate` writes 95 stock skills and the
post-hook removes 74 of them, silently, on every sync. The agent noticed; no
alert did.

**Shape of a fix (proposal — needs sign-off; this is a security control).** Two
changes, and the first is the cause:

1. **`agent_config_mod` must match a WRITE, not a name.** `> ~/.claude/CLAUDE.md`,
   a `Write` whose path is an agent config, `tee` into one — not the token
   appearing in prose. As written the pattern cannot be satisfied by any honest
   document about arizuko.
2. **The shipped corpus is not agent-authored.** `bashSkillRescan` exists for
   BUGS `J8` — the agent writing a persistent instruction under
   `bypassPermissions`. A file that arrived from the baked image
   (`/opt/arizuko`, `MaterializeAppSrc`) was never agent-written, so scanning it
   is a category error. Skipping files identical to their image copy is
   defence-in-depth for the residual false positives after (1).

Not shipped: narrowing a threat pattern is a security decision, and the six
non-`agent_config_mod` patterns need reading one at a time before anyone claims
they are false too — `reverse_shell` across 5 files may be honest documentation
or may be a real finding in our own corpus.

- **RE-MEASURED 2026-09-20 by running `blockingVerdict` over the whole corpus:**
  **75 of 551** skill files blocked, `agent_config_mod` **109 hits across 66
  files**, `migrate/SKILL.md` first with 16 findings. The entry's 74/544 and
  108/65 were right at the time.
- **The false-positive half is fixed 2026-09-20.** `agent_config_mod` matched
  the BARE string `CLAUDE.md`, so a skill that merely NAMED the file was a
  `critical`, and a critical is blocking. The threat is a skill that WRITES an
  agent config file, so the pattern now asks for a write: a redirect whose path
  follows with no space, a mutating command or tool naming the file on the same
  line, or a write-mode `open`. **66 flagged files → 11**, measured over the
  same 551.

  PROVEN BY MUTATION: put the bare filename back, or drop the severity from
  `critical`, and a test fails. The test carries both directions — eight write
  forms that must still be caught, and five prose mentions that must not be —
  so narrowing it further to nothing would fail too.
- **Still open, and it is the half the entry says needs its own sign-off:** the
  remaining **11** files are arizuko's OWN shipped skills that genuinely write
  agent config, because that is their job — `migrate/SKILL.md`, `wisdom/SKILL.md`,
  `self/extension.md`, `self/migration.md` and seven migration notes. The guard
  has no notion of provenance, so it cannot tell the image's corpus from a skill
  the agent wrote. That is a new mechanism.

  It matters more than its severity suggests: `migrate/SKILL.md` is the delivery
  path for every `ant/CLAUDE.md` change (see `L316`), so quarantining it stops
  the fleet taking new instructions at all.
- **The "11 genuinely write agent config" claim was wrong, and the corpus half
  is FIXED 2026-09-20 without a provenance mechanism. 20 of 551 → 11**, measured
  by running `blockingVerdict` over the tree before and after. Reading all 20
  one at a time (which this entry asked for and nobody had done) found nine
  firing on text that is not the threat the pattern names, in three patterns:
  - `agent_config_mod` kept three members that match a MENTION. `\bopen\(` is
    not a mutation — a bare `open()` is a READ, and the write-mode branch
    already carried `open(x,'w')`; it was the ONLY reason `migrate/SKILL.md`
    fired, so the file this entry leads with was never a true positive.
    `\bWrite\b|\bEdit\b` compile `/i`, so the English verb counted — that is all
    `wisdom/SKILL.md` ever did (`description: Write or edit SKILL.md,
    CLAUDE.md`); they now have to name a PATH, which the tool-call form does.
    And branch 1's `>` matched the closing bracket of the markdown placeholder
    `<group>/CLAUDE.md`, which is how `self/migration.md` and `128` fired; a
    redirect's `>` now has to follow whitespace or an fd digit.
  - `reverse_shell` matched bare `\bsocat\b` while its `nc`/`ncat` siblings ask
    for a listen flag. All five hits are `socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET`
    — arizuko's own documented MCP bridge, a local socket that cannot be a
    network shell. It now asks for a network address or an `EXEC`.
  - `dns_exfil` took a `$` anywhere on the line after the word `host`, so the
    English noun in `canonical host + public/private URLs … \`$WEB_HOST\`` was a
    critical. The `$` now has to begin an argument — the query name being built.

  PROVEN BY MUTATION, six of them, each reversing one narrowing and one
  (`socat` → matches nothing) in the opposite direction to show the attack
  cases are real: every one turns a test red. The regression net is a new test
  that walks the shipped tree and asserts **no `SKILL.md` blocks** — the sharp
  invariant, because `bashSkillRescan` renames a blocking file and a skill
  dispatches only while its SKILL.md sits at its own path, so a blocked entry
  point does not degrade that skill, it deletes it fleet-wide. Helper pages are
  not pinned, so a new migration note does not turn it red.
- **DELIBERATELY LEFT BLOCKED — the remaining 11 are 9 files, all true
  positives, and no provenance mechanism is needed for them.** Seven skills
  really do instruct a write to an agent config, which is their job:
  `self/extension.md` and migration notes `013`, `014`, `054`, `057`, `059`,
  `091`. `self/chat-link.md` really does put a route token in an outbound fetch
  URL. `software/docker.md` really does `curl … | sh`. None is an entry point,
  so none deletes a skill.

  Two patterns are blunt and were left alone on purpose, because narrowing
  either unblocks NOTHING and one of them would cost real coverage:
  `setuid_setgid` fires on puppeteer's `--disable-setuid-sandbox` (a flag that
  DISABLES a sandbox) in the two copies of `p5js/references/export-pipeline.md`,
  and separating that from a real `setuid` needs an enumeration, which is an
  allowlist by another name; `destructive_root_rm` reads `rm -rf /var/lib/…` as
  "from root" for want of an end anchor, but `software/docker.md` stays blocked
  on its genuine `curl | sh` either way, and anchoring it would let `rm -rf
  /usr` pass. Both are recorded here rather than half-fixed.

  Noticed in passing, NOT fixed (different concern): `p5js/references/` is
  shipped twice, once under `create/art/`.

## L152 — an `acl` grant can name a principal nobody will ever present, and no surface says so (2026-09-02, open — the separable half SHIPPED 2026-09-22; the reconciler needs sign-off)

`acl_membership` on krons grants `role:operator` to
`telegram:user/1183985669`. That id has sent **zero** messages on the instance,
ever, and appears in no identity link. It is inert: a second row grants the same
role to `telegram:user/1112184352`, the account that actually talks to the krons
bots, so nothing is broken today. The bug is that nothing anywhere would say if
the inert row were the ONLY one.

**Why nothing would catch it.** A grant is written against a principal STRING.
No surface checks the string corresponds to anything: not at write time (the
CLI, `add_acl` and dashd's form all accept any text), not at read time
(`Authorize` answering "no" for an unmatched principal is indistinguishable from
"no" for a real one), and not afterwards (nothing enumerates rows whose
principal has never appeared as a message sender or a linked identity). The
denial an operator would then see — "Permission denied: … requires an operator
grant (**)" — reads as policy working, not as a typo in a row.

It is the mirror of [`grant_action_needs_a_door`]: there, a real principal could
not reach a new ACTION; here, a real ACTION cannot reach its intended PRINCIPAL.
Both fail the same way — the grant exists, the door does not, and the denial
looks like policy.

**Shape of a fix (proposal — needs sign-off).** A read-only reconciler, not a
write-time check: write-time validation would refuse to pre-grant an identity
that has not spoken yet, which is legitimate and common (an invite names someone
before they arrive). Surfaced on the dashboard grants page and in the CLI grant
listing as a "never seen" marker beside the row. No new table, no new write
path, no evaluator change.

**THE ONE QUERY THIS ASKED FOR DOES NOT EXIST. Measured against the call paths
2026-09-20, and no oracle in the store can answer "never seen".** The marker
would be wrong on correct grants, which is worse than the silence it replaces:
this entry's own history is a false-absence conclusion, and a reconciler built
on any source below would manufacture them at fleet scale. What each candidate
actually answers:

| candidate | what it answers | why it is not "never seen" |
| --- | --- | --- |
| `messages.sender` | nothing, in one query | `sender` is an ATTRIBUTE (`store/messages.go:66`), not part of the key, and `messages` is partitioned per chat JID — every read in the file names a `Partition`. The "one query" is a sweep of every chat partition on the instance. |
| `message_activity.sender` | spoke in the last 30 days | This one IS a single query: one constant partition and `sender` is an attribute on it (`store/message_activity.go:46`, `:114`). But `ActivityRetention` is `30 * 24 * time.Hour` (`:59`) and the feed is swept to it, so a real person quiet for a month reads exactly like a typo. |
| `user_profiles` | created a world through onbod | `RecordPerson` has ONE caller — `onbod/main.go:911`, the last write of `writeWorld`. A platform sender who only ever chats in an existing group never gets an item, which `UserCap` states outright (`store/user_profiles.go:236`: "platform senders (telegram:user/…, anon:…) and service:routd never own one"). |
| `oauth_identities` | has linked an OAuth login | `OAuthIdentityForSub` (`authd/authstore/auth_users.go:33`) resolves only identities an account has LINKED. An unlinked `telegram:user/…` is genuinely unclaimed — which is precisely the `!claimed` branch `nonCanonicalWarning` already returns `""` on (`routd/acl_resource.go:224`). Warning there would fire on nearly every legitimate grant. |

Nor is the krons case separable by shape: `telegram:user/1183985669` and
`telegram:user/1112184352` are both well-formed, and the repo declares no
principal validator at all (no `ValidPrincipal`, no canonical-JID check on any
write surface), so no syntactic rule tells the typo from the real account.

**What a sound fix therefore needs, and why it is not an edit.** Arizuko keeps
no durable, enumerable record of the principals it has seen present themselves.
Answering this question at all means writing one — a new item per sender on the
message-append path, or an attribute moved onto an existing one — which is a new
write on the hot path and a new entity to sweep. That is a redesign, so it is
recorded here for sign-off rather than shipped (root `CLAUDE.md`, "Redesigns
need sign-off"). Until then the three write surfaces stay as they are, and
`nonCanonicalWarning`'s alias check remains the only real signal: it is sound
because it fires only when authd says the string IS an alias of another account.

**Three write surfaces, one guard — ALL THREE READ IT NOW (2026-09-22).** The
2026-09-20 measurement said `add_acl` alone; dashd was wired after it
(`dashd/grants_admin.go:246`), and the CLI is wired today. The CLI is mounted,
so it reads authd's store directly per the split write-discipline, through the
same `authstore.OAuthIdentityForSub` that authd's `GET /v1/identities/{sub}` is
a thin wrapper over — one answer, two readers, no second resolution rule.
Fail-open on all three: an unreachable authd does not refuse a grant.

`TestGrantAsksAboutThePrincipal` pins that the CLI ASKS, separately from the
renderer's own tests. A test that calls the renderer passes whether or not the
write path reaches it, which is exactly how this surface stayed unwired while
the other two did not.

That was the separable half this entry named as "sound work with no false
positives". What remains is the reconciler, and it is unchanged: arizuko keeps
no durable record of which principals have presented themselves, so "never
seen" has no oracle.

**How this entry was first written wrong.** The first version claimed krons'
Telegram operator had been unreachable since 2026-07-19. That came from a
`SELECT … LIMIT 30` on a 54-row table: the correct grant is row 31. Read the
whole table before concluding a row is absent — see `truncated_evidence`.

## L151 — proposal: an adapter whose credential is dead restart-loops forever and nothing PUSHES the fact to anyone; routd already owns the mechanism to say so (2026-09-01, proposal — needs sign-off; sharpened 2026-09-21)

Three of the fleet's adapters are dead right now on a bad credential, and every
one of them fails the root `CLAUDE.md` rule *"fail loud, fail to the user — a
logged-but-invisible failure is still silent"*:

| instance | adapter | error | visible where |
| --- | --- | --- | --- |
| sloth | `discd` | `websocket: close 4004: Authentication failed` | log only |
| marinade | `emaid` | `imap: NO [AUTHENTICATIONFAILED] Invalid credentials` | log only |
| krons | `whapd` | WhatsApp `401 session invalidated` | log only |

**The two halves of the silence.**

1. `chanlib/run.go:90-93` treats every `opts.Start` failure the same:
   `slog.Error` then `os.Exit(1)`. Docker's restart policy is the retry, so a
   **permanent** misconfiguration (wrong token) loops exactly like a transient
   network blip. That is the retry rule inverted — `CLAUDE.md` says retry only
   remote/network and DB-busy errors; misconfig throws immediately.
2. `chanreg/health.go:63-66` already detects it. After three failed pings (90
   seconds) routd deregisters the channel and logs
   `channel auto-deregistered`. From that moment outbound messages to that
   platform go nowhere and nothing says so — not the operator, not the agent
   that calls `send`.

**The proposal, and why it adds no branch.** `system_messages` is the mechanism
routd already uses to tell agents something changed out-of-band —
`checkMigrationVersion` (`routd/loop.go:503`) enqueues a row per folder and
`FlushSysMsgs` (`routd/prompt_db.go:15`) renders it into the next prompt as a
`<system>` line. Deregistration should enqueue one the same way. The call lands
**inside the `if fails >= maxHealthFails` block that already exists**, so the
change is one line at one site plus the plumbing to reach the DB — no new
control flow, no second notification path.

**The one design question that needs the operator's answer, and is why this is a
proposal not a patch:** `chanreg` is a library with no DB handle, and a dead
channel is not scoped to a folder. Which folders get told? Candidates: every
folder with a route through that channel (precise, needs a query); the operator
folder only (cheap, one row); or all folders (loud, noisy). Wiring `chanreg` to
routd's DB is a cross-package contract change either way.

**Not proposed:** an in-arizuko watchdog for a stopped instance. Both `sloth`
and `marinade` were stopped deliberately by a human on 2026-08-29 17:05 UTC and
stayed down two days — correct systemd behaviour for an `enabled` unit, and
process/host alerting is infra, not arizuko's concern.

- **Files:** `chanreg/health.go:63-66`, `chanlib/run.go:90-93`,
  `routd/loop.go:503`, `routd/prompt_db.go:15`
- **Evidence:** `journalctl -u arizuko_sloth` 2026-09-01 07:25 (discd 4004 loop),
  `arizuko_marinade` emaid IMAP AUTHENTICATIONFAILED, `arizuko_krons` whapd 401
- **Related:** `L149`/`L150` (the same class — a real failure with no path to a
  human), `adapter_service_name`, `whapd_pairing_recovery`

**2026-09-05 re-check after the 2026-09-04 deploy.** sloth `discd` is resolved:
the operator's credential fix went live with the 13:41 restart (`channel
registered name=discord` 14:29:32; a Discord turn answered at 14:30:57), zero
`4004` lines since. marinade `emaid` unchanged: 358 `imap idle error` in the 6 h
before, 1018 in the 17 h after (~60/h both). krons `whapd` unchanged: 336
`session invalidated` in the 6 h before, 973 in the 17 h after (~56/h both).

**RE-MEASURED 2026-09-21 — both mechanisms CONFIRMED, two claims REFUTED, and
the proposal now carries an ORDER it did not have.**

- **Both halves are still live, verbatim.** `chanlib/run.go:74` logs
  `adapter start failed` and exits 1, and every adapter fragment declares
  `restart: on-failure` (`template/services/*.yml`, `compose/compose.go:1111`),
  so docker is the retry. `chanreg/health.go:62-64` still deregisters after
  `maxHealthFails = 3`, and `EnqueueSysMsg` still has exactly two producers
  (`routd/prompt.go:244,251`, new_day / new_session) — nothing tells an agent a
  channel died.
- **REFUTED: "the only trace is one log line."** `Deregister` emits a durable
  `audit_log` row (`chanreg/chanreg.go:203`, `channel.deregister`). It is
  queryable, and eval check 23 already uses it: a `channel.deregister` count
  that tracks `channel.register` is an adapter flapping. The trace is a
  second, durable one — what it is not is a notification.
- **REFUTED: "nothing says so — not the operator, not the agent that calls
  `send`."** The agent gets a real error: every egress path returns
  `no channel for jid <jid>` (`routd/deliver.go:107` and 16 sibling verbs), and
  inbound from an unregistered adapter is rejected `jid_prefix_mismatch`
  (`routd/server.go:500`). The operator has `GET /v1/channels`
  (`routd/channels.go:135`) and eval check 23. **The real defect is narrower
  than filed and still worth fixing: nothing PUSHES the fact.** A dead channel
  is visible to whoever asks, and absence from a list is not an alert.
- **The credential inventory in the table above is now duplicated.** sloth
  `discd` is resolved; marinade `emaid` is `L62`; krons `whapd` is `L63`. That
  is the same three-entries-for-two-actions shape `O2` was closed for today.
  This entry keeps only the MECHANISM proposal; the operator actions live in
  `L62`/`L63`.
- **ORDER, and the trap in half 1.** Half 1 reads as "stop retrying a permanent
  misconfiguration", but `os.Exit(1)` under `restart: on-failure` is the only
  reason the fault is visible at all — `RestartCount` climbs and `docker ps`
  says `Restarting` (marinade, 1,574 restarts, 2026-09-21). An adapter that
  exits and stays down reads as `Exited`, and one that exits 0 looks
  deliberate. That is exactly the quieter failure eval check 23 exists for:
  `health: starting` with `RestartCount=0` went unnoticed for weeks on
  marinade. **So half 2 (the notification) must ship BEFORE half 1, or the fix
  trades a loud symptom for a silent one.** Half 1 alone is a regression.
- **The design question is unchanged and is the operator's:** `chanreg` is a
  library with no DB handle, and a dead channel is not folder-scoped. Which
  folders get told — every folder routing through that channel, the operator
  folder only, or all of them — is the sign-off this entry waits on.

## L149 — only the LAST assistant text block is delivered, so an answer written before a tool call is destroyed (2026-08-31, instruction half FIXED 2026-08-31; the mechanism half is GOVERNED BY spec 6/44, whose gate is armed)

The operator watched the krons agent answer him three times and receive
*"you did not reply though"*. The agent had answered. The answers were thrown
away by the delivery path, and the agent's own manual told it they were not.

**The mechanism.** `ant/src/claude.ts:373` reads the harness's `result` event:

```go
const text = 'result' in raw ? (raw as { result?: string }).result ?? undefined : undefined;
```

The Claude Code CLI's `result` field carries **the final assistant message only**.
`deliverTurn` passes that one string to `submit_turn`, and routd stores it as the
bot message. Every assistant text block written earlier in the run — before a
`Skill`, `Bash`, `Write`, `Read` or `Edit` call — never leaves the container.

**What `ant/CLAUDE.md` says instead.** Line 85:

> After a tool call, stay silent unless the user asked a question. No "Done.",
> "Sent.", "OK", "All set" — **text outside `<think>` is delivered**; the action
> is already visible.

That sentence is false as implemented. Line 131 then completes the trap:

> for tasks that chat-emit a status, close with a one-line confirmation, even
> just "done." Status without conclusion is a contract break.

So an agent that answers, then records the answer in its diary, then closes with
the required one-line confirmation, **ships the confirmation and destroys the
answer**. The two lines are individually reasonable and jointly lethal.

**The live case** — krons, session `32863f1a`, 2026-08-31 07:35 UTC. The operator
asked the agent to introspect. Transcript line 299 holds a 1523-character answer
("The two replies came from two different modes, and I only switched because you
pushed…"). Lines 300–321 are the diary tool calls. Line 329 is
`Done — reply sent, lesson banked in diary…` — 102 characters. Telegram message
`6318` is that 102-character line, and nothing else. Repeated four minutes later:
1092 characters at line 351, delivered as 96 characters at line 356 (message
`6322`).

The agent then defended itself from its transcript — *"my visible history holds
the full thread… Nothing was reset, nothing was hidden"* — which was true of what
it **wrote** and false of what the operator **received**. See `L150`.

**Measured blast radius**, krons only, top-level chat sessions (excludes
subagents and workflow runs): **34 destroyed answers across 16 sessions**.
Heuristic: an assistant text block of ≥250 characters, at least one tool call
after it, and a final text block under `max(200, len/3)`. Largest single loss:
2195 characters of research synthesis delivered as 355. Several of the surviving
summaries refer to the destroyed text — *"the synthesis above is the core of
it"* — pointing the reader at something the reader never got.

**Why nobody caught it.** The failure is invisible on every axis an operator
checks. `status` is `sent`, `errored` is `0`, `delivery_attempts` is `0`, and
Telegram returns a real `platform_id` for the truncated message. A healthy
delivery of the wrong bytes.

**Fix, two halves — they are separable and the first is not a redesign:**

1. **Instruction (minimal, ships inline).** ✅ **DONE 2026-08-31.**
   `ant/CLAUDE.md:85` now states the contract that exists — *only your FINAL text
   block is delivered; text before a tool call is discarded; make every tool call
   first and write the answer once, at the end* — and names the exact trap:
   never answer and then file that answer, because the closing line replaces it.
   Line 131 is reconciled: a turn that emitted a `<status>` closes with the
   ANSWER, not with "done", because that closing block is the only text delivered.
2. **Mechanism (proposal, needs sign-off).** Make the delivered text the
   concatenation of the run's assistant text blocks rather than the last one.
   This changes delivery semantics for every backend and every turn, so it is a
   contract change, not a patch. Note `claude.ts:404` already has a
   concatenating shape for a different frame; do not add a second path — amend
   the one extraction site.

Option 1 alone stops the loss without touching the wire. Option 2 makes the loss
impossible by construction. `<status>` is not a workaround: it is capped at ~100
characters and is a progress signal, not an answer.

- **Files:** `ant/src/claude.ts:373`, `ant/src/index.ts:89-107`,
  `ant/CLAUDE.md:85`, `ant/CLAUDE.md:131`
- **Evidence:** krons session `32863f1a-c7a0-4373-a789-a6ce3c05c404`, transcript
  lines 299/329 and 351/356; `routd.db` messages `6318`, `6322`, `6324`
- **Related:** `L150` (the agent cannot observe the loss), `submit_turn_result`

**Re-measured 2026-09-20.** The instruction half is CLOSED: `ant/CLAUDE.md:85`
now states the real rule, and `:124` reconciles it. The mechanism half stands,
and this entry's "amend the one extraction site" undercounts it: the SDK
documents `result` as the final assistant text
(`ant/node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts:5414`) and THREE
readers take it — `ant/src/claude.ts:404`, `ant/src/codex.ts:178`,
`ant/src/pi.ts:151`. Earlier blocks reach `streamStatuses`, which forwards
`<status>`-wrapped text and drops the rest. Concatenating them contradicts the
instruction shipped at `:85` and widens `L329`, so this needs sign-off rather
than an inline patch.

**RE-MEASURED 2026-09-21 — the defect is CONFIRMED, the reader count was
imprecise, and the proposed fix is SUPERSEDED. Do not ship option 2.**

- **Confirmed live on all three backends, by three different mechanisms.**
  Only `ant/src/claude.ts:404` reads the SDK's `result` field. `codex.ts:178`
  and `pi.ts:151` return their own `this.lastText`, which each OVERWRITES on
  every assistant message (`codex.ts:133`, `pi.ts:130`). "Three readers take
  `result`" reads as one shared site; it is three, so a concatenation fix is
  three separate changes, not an amendment to one extraction site.
- **Earlier blocks really are destroyed.** On every backend they reach
  `streamStatuses` (`ant/src/index.ts:174`), which forwards only
  `statusBlocks(text)` — the `<status>`-wrapped spans — and drops the rest.
- **SUPERSEDED: `specs/6/44-delivery-contract.md` (draft, unsigned) already
  owns this question, and its decision is the opposite of option 2.** 6/44
  rules that "a turn reaches a chat through a delivery tool call, or it reaches
  nothing" — the prose result stops being a delivery path at all. Under that
  contract there is no last-block-only defect to fix, because `reply` delivers
  the whole argument it is given and returns an id. 6/44 also names the three
  disagreeing declarations this entry straddles (`ipc/ipc.go:1194`,
  `ant/CLAUDE.md`, `routd/turns.go:252`) and deletes `appendAndDeliver` rather
  than adding to it.
- **Why concatenating would be a regression, not a fix.** Every intermediate
  block the agent wrote would ship — which is the deliberation-leak class
  `L329` was narrowed to stop — and `ant/CLAUDE.md` now instructs agents that
  pre-tool-call text is discarded, so they write intermediates freely on that
  promise. Changing delivery under a manual agents are written against breaks
  the contract from the other side.
- **The gate is armed; this entry is waiting on a number, not on a design.**
  6/44 §"The gate" holds the inversion until `arizuko_prose_fallback_turns_total`
  has krons data (≥ 2,000 turns, ≥ 5 folders, ≥ 7 days). That counter has
  SHIPPED — `obs/metrics.go:152`, wired at `routd/turns.go:256` — so the
  measurement is running. Read it before reopening the mechanism half.
- **Status:** mechanism half tracked by `specs/6/44`; this entry keeps only the
  measured blast radius and the instruction fix. No separate sign-off needed
  here, and no code change should land against option 2.

## L150 — the agent never sees what it delivered, so it cannot detect a truncated reply and confabulates a cause instead (2026-08-31, RETRY-OWNER HALF FIXED 2026-09-21; the per-turn receipt stays a proposal)

`routd/prompt_db.go:192,197,208` build the `<messages>` block with
`AND is_bot_message = 0`. The envelope therefore carries **inbound messages
only**. The agent's sole record of its own speech is its conversation
transcript — which holds what it *wrote*, never what *shipped*.

Under `L149` those two differ, and the agent has no way to learn it. Asked
*"where did you answer?"* the krons agent looked at its transcript, found the
answer, and produced three successive explanations for a delivery failure it
could not see: *"a delivery gap, not me going silent"*, then *"Same session the
whole time… I saw the context and the `[resolve]` steering"*, then a
timestamp-based theory about turns splitting 13 seconds apart. The last one was
mechanically correct about a different event and still not the cause. This is
confabulation forced by missing feedback, not by a weak model — the honest
answer was unreachable from inside the container.

Note the asymmetry that makes it worse: an explicit `mcp__arizuko__reply` call
**returns a message id**, so the agent can confirm that one. Turn-final text
delivery returns nothing. The reliable path is the one the manual tells the
agent not to use.

**Two candidate fixes, both proposals:**

1. **Receipt.** Have `submit_turn` return the stored message id and length, and
   have ant surface a mismatch to the agent. Smallest change; tells the agent
   the truth at the only moment it can still act.
2. **Echo.** Include the agent's own delivered messages in `<messages>` (drop
   `is_bot_message = 0`, or add a bounded `<delivered>` block). Larger prompt,
   but it makes "what did they actually receive" answerable on every turn.

Fix 1 is enough to end the confabulation. Fix 2 also repairs the agent's model
of the conversation, which currently has a one-sided hole in it.

**Second measurement from the same envelope, recorded not filed.** The turn at
07:36 carried 22,078 characters. `<episodes>` and `<knowledge>` account for
roughly 21,800 of them; the live user message is 92. The present tense is 0.4%
of the context the agent reasons over, and every turn ends with the standing
directive `[resolve] Invoke /resolve now` — which guarantees a tool call, which
under `L149` is what destroys the answer. The proportion is not itself a bug,
but it is the pressure that makes `L149` fire on the turns that matter most.

- **Files:** `store/message_folders.go:354`, `ant/src/index.ts:89-107`
- **Evidence:** krons session `32863f1a`, transcript lines 333 (envelope), 340,
  351, 367
- **Related:** `L149` (the loss this blindness hides); `L165` (the same
  blindness in the failure direction, below)

**Measured 2026-09-05 on sloth (`L165`).** `reply` returned `status 502`. The
row was already `pending` and owned by the sweep, which delivered it 52 s later.
The agent, told only "failed", called `reply` again with the same text six
seconds later, and the sweep delivered that copy too — two of each answer in the
channel (`mcp-1788608628001196602` / `mcp-1788608634966950969`, and the `yes.`
pair). The tool result must name who owns the retry: either the error reads
"queued; the sweep retries this, do not resend" (fix 1's receipt shape), or a
send the agent was told failed is not retried by the sweep (one owner). Today it
is both, and the agent cannot tell.

**RE-MEASURED 2026-09-21 — one claim REFUTED, the filed line numbers are dead,
and one half was bounded after all.**

- **REFUTED: "the agent's sole record of its own speech is its transcript."**
  `inspect_messages` (`ipc/ipc.go:2177`) and `get_thread` (`:2297`) read
  `MessagesBefore` / `MessagesByThread`, which apply no bot filter
  (`store/messages.go:541,559`), so they have always returned the agent's own
  rows. With `L184` shipped today those rows also carry `bot="true"`
  (`router/router.go:156`), so the agent can now both reach and identify what it
  delivered. What is missing is not ACCESS — it is automatic feedback at the
  moment the agent can still act. Fix 1 (receipt) is the half that survives;
  fix 2 (echo) is largely redundant on the read side now.
- **The filed line numbers are dead.** `routd/prompt_db.go` holds the
  system-message flush and topic lineage, and reads no messages. The injected
  `<messages>` window excludes bot rows at `store/message_folders.go:354`
  (`{Attr: attrFeedBot, Op: objstore.Absent}`), so the envelope half of this
  entry is still live — at a different site.
- **FIXED 2026-09-21 — the `L165` half, which needed no sign-off.**
  `recordOutboundFailure` (`ipc/ipc.go`) ALREADY decides who owns the retry: a
  transient failure lands `pending` and routd's sweep re-sends it, a permanent
  refusal lands `failed`. It told the log and the row and not the caller, and
  all three egress sites (`send`, `send_file`, `reply`) returned the bare
  platform error, so one word "failed" covered both outcomes. The status now
  rides the returned error — one declaration, two readers, no second classifier
  at the call site. `TestRecordOutboundFailure_TellsTheAgentWhoOwnsTheRetry`
  fails when the error is returned bare and when the two branches are collapsed,
  and holds the `chanlib.ErrPermanent` sentinel across the wrap.
- **Still a proposal:** the per-turn receipt (fix 1 proper — `submit_turn`
  returns the stored id and length so the agent can see a truncated turn-final
  delivery) and the `<messages>` echo (fix 2). Both change a contract spanning
  routd and ant.

## L146 — proposal: the inbound path is one rewrite — a channel payload becomes (destination URI, Authorization header, body); authorize on user + destination, never source (2026-08-30, proposal — needs sign-off)

Designed with the operator across this session. It supersedes `L145`'s framing:
that entry asked whether a LINK can select its destination; this asks the same
question of every inbound message, and answers it the same way.

**The shape.** A channel adapter receives a platform webhook or socket frame and
does exactly one thing: rewrite it into arizuko's internal call — a destination
URI, an `Authorization` header, and the body. Everything behind that boundary is
uniform, and the adapter holds no authority of its own.

    platform payload → routd resolves → (destination URI, Authorization, body)

**Authorization is user + destination. The source is not a principal.** A JID is
an address, unauthenticated by construction — anyone in a group chat can post —
so authorizing on it would give every group member the group's authority by
accident. The destination folder says what authority applies; the paired user
narrows it when one is known; the source only selects the destination.

**Three measurements say the user half is computed and then thrown away:**

1. `turnCallerSub(last.Sender)` (`routd/dispatch.go:684`) already turns a message
   sender into a caller sub — mapping `timed`/`system` triggers to
   `service:routd` and passing a real sender through. It is used for the BUDGET
   gate (`dispatch.go:261`) and nothing else.
2. Every authorization call hardcodes the destination as the principal:
   `db.Authorize("folder:"+folder, folder, …)` at `ipc/ipc.go:1092,1118`, and
   `routd/sibling_db.go:271` documents `sub` as "the canonical agent principal
   (folder:<folder>)". Destination twice, user never.
3. The mechanism intended to close this is GONE: `AssumeRole` and `mcp_tokens`
   return zero hits in the tree. They were recorded as an unwired shell earlier
   in this project's history and have since been deleted, so no per-user
   narrowing exists anywhere on the turn path.

So budget knows who you are and authorization does not, and the gap is one call
frame wide.

**Resolution is only for namespaces arizuko does not own — and this is ALREADY
enforced.** `ROUTING.md:29-38`: web JIDs bypass the route table entirely
(`web:<folder>` resolves through `GroupByFolder`), and `store.AddRoute` REJECTS a
`chat_jid=web:*` predicate at insert time so the mistake fails loudly. The
surfaces arizuko controls — web chat, `POST /mcp`, `arizuko talk` — state their
destination and are authorized by grant, which is what `L139` shipped.

So the change is confined to the FOREIGN edge: make platform inbound produce the
same `(destination, Authorization)` shape the owned surfaces already use.
Nothing behind that boundary moves.

**The unpaired sender is a foreign-edge policy, not a hole in the model.** Owned
surfaces already carry an authenticated user (proxyd-stamped or bearer). Platform
senders mostly do not: krons has **164 distinct senders** shaped
`telegram:user/1112184352` and `whatsapp:271317395882184@lid`, exactly **one**
pairing token, and **zero** `acl` principals that are not `folder:` or `role:`.
Switching `Authorize` to the sender sub with no fallback would deny 163 of 164
senders and stop the instance. Three options:

1. **Folder floor when unpaired** (recommended default) — nothing breaks; a
   paired user may be granted more or less than the floor. Buys attribution, not
   containment, until people pair.
2. **Seed a principal per sender**, inheriting the folder's role on first sight —
   real per-person containment and revocation, at the cost of an acl row per
   stranger who ever posts.
3. **Require pairing to act** — true user authority, breaks every group chat.
   Not viable for a chat product.

Recommended: 1 as the default, 2 as an opt-in per folder, so nothing breaks and
containment is available where it is wanted.

**The one real complication.** `ipc/ipc.go:934` already passes a `callerSub` that
is the SOCKET peer, not the message sender. Two notions of "who" live in one
file. They must be reconciled into one principal per turn, not stacked — stacking
is how a confused deputy gets built.

**What must NOT collapse into URI routing** (settled earlier in the same
discussion, recorded here so it is not retried):

- **The correspondent mapping.** `tg:12345` is an external identity arizuko does
  not own and cannot place in its tree. `ROUTING.md`'s route table, target
  expansion, modes, autoviv, the inline `@name`/`#topic` layer and per-chat
  sticky state (`routd/db.go:546`) mean a destination is COMPUTED per message,
  not looked up. routd is a resolver, not a map.
- **Transport vs authority.** `proxyd_routes` answers "which process"; `acl`
  answers "may you". Same path shape, different question. Merging them makes a
  URL prefix into a permission.
- **Verb rules with arguments.** No path expresses
  `hold:mcp:github_create_pull_request(repo=x/*)`.

**What collapses cleanly:** folder paths, `acl` scope globs and request URIs are
one hierarchy in three notations. That is the same repetition the `route_tokens`
row shows by storing a folder three times (inside `jid`, again in
`target_folder`, again in `owner_folder`).

**What it buys:** adapters become untrusted — they present a correspondent and
hold no authority; the credential is minted per turn for the destination the
route just computed, which is the audit row for free; and one internal calling
convention replaces per-surface plumbing.

**What it costs:** a per-turn minting path that does not exist today, reconciling
the two `callerSub` notions, and a decision about what authority a turn carries
when the sender is NOT paired to a user — the common case on every chat platform.
That last one is the crux and this entry does not answer it.

- **Severity:** n/a (design proposal)
- **Scope:** routd dispatch / ipc authz / channel adapters
- **Affected:** `routd/dispatch.go`, `ipc/ipc.go`, `routd/sibling_db.go`, all adapters, `specs/5/34`, `specs/6/26`
- **Status:** DEFERRED by the operator 2026-08-30 — "for us it's a JID router,
  that is ok for now". Nothing is to be built from this entry. It is a recorded
  design, not a work item; the JID router stands.
- **Why it is still worth having written:** the scope is now bounded (the nine
  channel adapters plus `/hook/`, nothing behind them), the enforcement that
  already exists is named (`ROUTING.md:29-38`), and the decision that gates it —
  what an unpaired sender authorizes as — is costed with real numbers instead of
  being rediscovered later.
- **Related:** `L145` (the link half), `channel_recast` (the 2026-08-01 direction this completes)

## L145 — proposal: let a link's URI SELECT the destination within what its token AUTHORIZES, so folder routing is HTTP path routing (2026-08-30, proposal — needs sign-off)

The operator's framing: *"the links should be able to do this, and in fact the
JIDs should — or rather could — route also using this way, just HTTP URI
routing."*

**Why it is coherent.** `L139`'s collapse (shipped `8b8158dc`) made the GRANT SET
the authority: a route token authenticates an anonymous principal, `send` is
gated by `auth.MatchGroups(grants, folder)`, and today that set has exactly one
element so there is nothing to choose. Allow the grant to be a glob and the URI
becomes how a caller picks within it. Nothing new is invented — `MatchGroups`
already matches `corp/eng/**` (`auth/acl.go:21-29`), and the URI already carries
two of the three levels: `/chat/{token}/{topic}/messages` and
`/chat/{token}/{id}/sse` (`webd/server.go:104,108`). This adds the folder.

Folders ARE paths — `corp/eng/sre/oncall` — so folder containment becomes URI
prefix containment, which an HTTP router does natively and proxyd already does
for `proxyd_routes`.

**Four things it must get right.**

1. **The URI SELECTS; the grant AUTHORIZES. Never the reverse.** The folder in the
   path is a request, checked against the token's grant set — it must never be
   parsed into authority. `target_folder` exists on the row precisely because a
   suffixed JID (`web:t/sfx`) cannot be parsed back into a folder, and this
   session already shipped two bugs of exactly that class
   (`identity_derived_not_read`). A URI segment is the least trustworthy input
   there is.
2. **A subtree link is a WIDER credential than a pinned one.** Possession is
   membership, so a link granted `corp/**` hands the whole subtree to anyone it
   is forwarded to. This must be opt-in at mint time and never the default; the
   one-element grant stays the shape a shared chat link gets.
3. **Route collision.** `/chat/{token}/{topic}/messages` and `/chat/{token}/{id}`
   already claim the segment after the token, and a folder path contains `/`.
   A folder-addressed form needs its own namespace segment and a trailing
   wildcard — e.g. `/chat/{token}/f/{folder...}` — decided explicitly rather than
   overloading the existing patterns.
4. **`MatchGroups` is glob, not prefix.** `atlas` does not cover `atlas/content`
   (pinned by `TestChatTokenMCP_SendSubfolderRefused`). A subtree grant must be
   written `atlas/**` deliberately; the ambiguity between the two is exactly what
   `L122` cost.

**What it buys:** one mint for a whole subtree instead of one token per chat, and
routing that reads the same in the URI, the JID and the ACL — three notations for
one hierarchy collapse to one.

**What it costs:** a second credential shape to reason about at mint time, and a
sharing hazard that does not exist today.

- **Severity:** n/a (design proposal)
- **Scope:** webd token surfaces / route_tokens mint
- **Affected:** `webd/server.go`, `webd/route_token.go`, `store/route_tokens.go`, `specs/5/W`
- **Status:** proposal (needs sign-off) — nothing changed
- **Fix:** add a folder-addressed token surface whose grant set may be a glob; keep the one-element pinned form as the default a shared link gets

## L120 — `/openapi.json` fabricated response schemas from a verb convention, so 22 mounted REST operations were documented wrongly (2026-08-26, LIE REMOVED 2026-09-21; typing the remaining 22 bodies is a proposal)

`resreg/openapi.go:283` `endpointOp` INVENTS the response — list → `array<RowType>`,
GET → `RowType`, POST/PUT → `RowType` — while `restHandler`
(`resreg/resreg.go:374-410`) writes the handler's return value verbatim, and
nothing constrains a `Handler` to return `RowType`. Verified per handler:

- `GET /v1/routes` returns `[]router.RouteView` (id/mode/fires_turn/explain/
  shadowed_by), documented as `RoutesRow`; `POST /v1/routes` returns `{"id":n}`.
- `GET /v1/route_tokens` returns `{"tokens":[…]}`; the `chat`/`hook` mints return
  `{"token","jid","url"}`.
- `GET /v1/pending_actions` returns `{"pending":[…]}`; approve/reject return
  `{"ok","id","status"}`.
- `GET /v1/tasks` and `/v1/tasks/{taskId}` return `core.Task`, which has **no
  json tags** — the wire carries Go names `ID`/`ChatJID`/`NextRun` while the doc
  says snake_case. `timed/dash.go:40-46` deliberately decodes the Go names.
- `GET /v1/groups` returns `[{"folder"}]` only, documented as the 7-field
  `GroupsRow`.
- onbod: `/v1/invites` → `{"invites":[…]}`, `/v1/onboarding` →
  `{"onboarding":[…]}`, `/v1/gates` → `{"gates":[…]}`, and every onbod mutation
  returns `{"ok":true}`.

Clean by contrast: dead_letters list, installed_packages list/get, web_routes
list, audit list, sessions list, signing_keys list, proxyd_routes list/get/create.

- **Cost:** REST + `/openapi.json` is arizuko's stated boundary for external
  non-MCP tools, and CLAUDE.md claims *"drift is structurally impossible"* — true
  for paths and for the schema SOURCE, false for responses. Any client generated
  from the doc mis-decodes 11 operations.
- **The dead-letters handler already names this class** in its own comment
  (`routd/dead_letters_resource.go:131-135`) as the reason IT projects.
  `scheduled_tasks` is the un-projected sibling.
- **The one in-tree consumer that trusted the doc is already broken** — `L100`'s
  second break. Every other in-tree consumer agrees with its handler, not the doc.
- **Cause:** one declaration drives the doc, a second unconstrained one drives
  the wire, with no test tying `Handler` output to `RowType`.
- **Fix (cross-cutting):** project every handler return through its Row (the
  dead_letters pattern), or emit per-action response schemas.

- **MEASURED + FIXED 2026-09-21 at `a388cad7`.** The real count is **22**, not
  11. Method: every non-`MCPOnly` endpoint of every catalog resource carrying a
  `RowType` (the only ones the emitter documents) read against its mounted
  handler's REST return — 53 documented operations over five daemons. 22 assert
  a schema the handler contradicts; ~10 more are DELETEs the emitter described as
  bare `"OK"` while the handler answers a body, which is a gap, not a lie, and is
  counted separately. The 22: `routes` list/add/set; `route_tokens`
  list/issue_chat/issue_hook; `pending_actions` list; `tasks` list/get;
  `groups` list; `acl` add; `secrets` create; `github_sources` register;
  `dead_letters` replay; onbod `onboarding` list/create/approve/reprompt,
  `invites` list/create, `gates` list/update.

- **The fix is that the emitter stopped guessing.** `Resource.Responses` maps an
  action to the Go type its handler returns; `endpointOp` emits a schema from
  that and from nothing else, and an action with no entry now documents `"OK"`
  with **no content schema**. So no operation is documented wrongly any more:
  22 lies became 0 lies plus 21 stated gaps, which a generated client handles
  and a wrong struct does not. `listResponse`/`itemResponse` — the two
  convention renderers — are deleted. `rowSchema` also learned to inline an
  untagged embedded struct, without which `router.RouteView` documents as one
  member named `Route` and loses every route field.

  Declared so far, one line each in the catalog file that already owns the Row:
  `routes` list (`[]router.RouteView`), `scheduled_tasks` list/get/patch
  (`core.Task` — the Go field names it really answers), `dead_letters` list,
  `installed_packages` list/get, `github_sources` list, `web_routes` list,
  `audit` list, `sessions` list, `signing_keys` list, `proxyd_routes`
  list/get/create/update, and `groups` list — that last one named the
  folder-only projection the handler had been building as an anonymous
  struct twice, so declaring it deleted the duplicate.

- **The guard**: `resregtest.AssertDocumentedResponse` serves a REAL request on
  the daemon's own mux and compares the body to the schema the production
  emitter publishes for that operation. Nothing else could: paths derive from
  the mux and request schemas from `RowType`, but `Handler` returns `any`, so
  only a served body proves the doc. `routd/openapi_response_test.go` runs it
  over `/v1/routes`, the two task reads and `/v1/groups`, and pins the count it
  actually checked, so dropping a declaration fails the test rather than
  silently returning to a bodyless doc.

- **Refuted from the 2026-08-26 triage**: `invites` DOES declare a `RowType`
  (`resreg/resources/invites.go`), and the gates resource is
  `resreg/resources/onboarding_gates.go` — it declares `/v1/gates` and a
  `RowType`. Neither is undocumented; both are misdocumented, in the same way as
  the other 20. Also, the claim that `routd/routes_resource.go` answers
  `{"routes"}` is the MCP branch only: the REST branch answers
  `[]router.RouteView`, and `/openapi.json` documents REST.

- **Still open, and a proposal because it is a redesign.** The remaining 21 handlers keep
  answering ad-hoc `map[string]any` literals — `{"ok":true}`, `{"id":n}`,
  `{"token","jid","url"}`, `{"pending":[…]}` — which have no Go type to name, so
  those operations document no body. Closing it means giving each a named struct
  and returning it. Two costs decide it and neither is mine to take: (a) the MCP
  face shares the handler, so any change to the VALUE (unwrapping
  `{"onboarding":[…]}` to a bare array, the way proxyd's list was already
  unwrapped) is an agent wire-contract break; keeping the value and only naming
  its type is safe but adds ~15 structs; (b) two of the real types —
  `ipc.RouteTokenInfo` and `routd.PendingAction` — are unreachable from
  `resreg/resources` (`ipc` imports `resreg`, and `routd` imports `resources`),
  so they must move or the declaration must. The REQUEST body is invented the
  same way and is a separate entry's worth of work: `POST /v1/routes` documents a
  `RoutesRow` body while the handler reads `{"route": "<json>"}`.

## L96 — an inbound reaction reaches the agent, but never as a distinguishable event (2026-08-25, RE-SCOPED 2026-09-21 — one question, no redesign)

Inbound reactions are wired end-to-end on the four chat adapters — teled asks
Telegram for them explicitly (`teled/bot.go:233` `allowed_updates` must NAME
`message_reaction` or none are sent), discd holds the reaction intents
(`discd/bot.go:63`), slakd handles `reaction_added` (`slakd/bot.go:444`), whapd
subscribes to `messages.reaction` (`whapd/src/main.ts:344`). Each builds a real
`InboundMsg`; `routd.handleMessages` stores it and enqueues it like any message.

What happens next is the defect. A reaction whose `ReplyTo` targets the bot's
own row is force-promoted to `verb=mention` (`routd/server.go:531`), but a
reaction on anything else stays `verb=like` and must satisfy the route's own
`verb=` rule (`router/router.go:328`) — so a group route pinned to
`verb=mention` route-misses it. Live evidence on krons: five reaction rows have
ever landed, all Telegram, and in all five the bot's next reply answers the
user's next TEXT message. No reply ever addresses the reaction.

So a user who reacts 👍 instead of typing gets silence, and the agent cannot
tell "acknowledged" from "ignored".

**Proposal, not a fix.** The choice is a product decision with real cost: making
every reaction trigger a turn spends a model call per emoji and invites loops on
busy groups. The candidate shapes are (a) reactions stay ambient but the agent's
prompt states that a reaction arrived and on which message, (b) a route-level
`verb=like` opt-in, or (c) promote only reactions on the bot's own messages —
which `:531` already half does, via `ReplyTo` rather than authorship. Whoever
signs off should also say whether a reaction should ever spend a turn on its own.

Also structural, and separate: `bskyd`, `reditd` and `linkd` can SEND a reaction
and can never receive one — no `InboundMsg` is ever built from a like or an
upvote in those three.

- **RE-MEASURED 2026-09-21 at `b3b973e2`: the 2026-09-20 note above is REFUTED
  on both of its claims, and the refutation removes all three options.** That
  note grepped for `reaction` in `routd/` and concluded from the silence that no
  delivery path exists. The mechanism is spelled `verb`, not `reaction`, so the
  grep could not see it.
  - `router.FormatMessages` emits `verb="like"` for any verb but `message`
    (`router/router.go:133`) above a `<reply-to sender=…>excerpt</reply-to>`
    header (`router/router.go:75-92`). A reaction that reaches a prompt already
    says what arrived and which message it was on.
    `router/router_test.go:422` pins it.
  - `verb` IS a route predicate (`router/router.go:329`), so `verb=like` is a
    routable match today. `router/router_test.go:269` pins it.
  Therefore, of the entry's three candidate shapes:
  - **(b) a route-level `verb=like` opt-in needs NO code.** It is one route row.
    Ruled out as work.
  - **(c) promote reactions on the bot's own messages is SHIPPED**, and not
    "half, via `ReplyTo`": `routd/server.go:578` resolves the parent through
    `MessageByID`, which matches on `platform_id` — the id a reaction actually
    carries — and then tests `parent.BotMsg`, which is authorship.
    `ThreadHasBotMessage` widens it to any thread the bot has spoken in
    (spec `5/L`). Every adapter sets `ReplyTo` on a reaction
    (`discd/bot.go:273`, `teled/bot.go:264`). Ruled out as work.
  - **(a) is already the shipped default on the canonical config.** A reaction
    that misses the `verb=mention` trigger row hits `5/B`'s `#observe`
    catch-all, so `routeMiss` ingests it with `is_observed=1` and it reaches the
    NEXT turn's `<observed>` window — under the rule "Observed messages are
    context, not requests. Do not reply to them." The live krons finding, "the
    bot's next reply answers the user's next TEXT message", is that rule
    working as specified, not a delivery failure. Ruled out as work.
  So the title is wrong: a reaction DOES reach the agent, by all three doors.
  What is left is narrower. Once promoted, `verb` is OVERWRITTEN to `mention`
  and `core.Message` carries no reaction field at all
  (`routd/routes_http.go:38` drops `apiv1.Message.Reaction`), so the agent reads
  a message whose entire body is `👍`. A reaction is never a distinguishable
  EVENT — only ambient text it is told to ignore, or a bare emoji under a
  `<reply-to>` header.
- **One question for the sign-off:** should a reaction be distinguishable in the
  prompt — a `reaction` attribute the mention promotion does not overwrite — or
  is a bare emoji under a `<reply-to>` header enough? Nothing here needs a
  redesign or a product decision about spending a turn per emoji: (b) is a
  config row and (c) already ships. The structural half of this entry —
  `bskyd`, `reditd` and `linkd` can SEND a reaction and never receive one —
  is untouched and still open.

## L92 — the HITL hold gate fails OPEN when the `acl` read fails (2026-08-24, proposal)

`auth.CheckHold` (`auth/authorize.go:392`) logs and returns `false` when
`policyRows` errors:

```go
slog.Error("check hold: acl read failed; holding NOTHING", ...)
return false
```

Nothing is held, and every `hold:mcp:<tool>` the operator configured stops
gating until the read recovers. The comment argues it matches "Authorize's
deny", but it does not: Authorize failing closed REFUSES the call, while
CheckHold failing "open" LETS IT RUN. The two branches of `5/19` disagree with
each other, too — a hold that cannot be RECORDED blocks the call anyway
(`routd.holdGate`, BUGS `J3`), precisely so a storage failure cannot silently
defeat the gate. A read failure defeats it silently.

Found by an oracle review of the four-verdict design (`specs/6/26` § Decision
5), not by an incident.

**Proposal, not a fix.** Reversing it means an acl read error blocks every
held-eligible tool call — an availability decision with a real blast radius,
and the kind of cross-cutting change `CLAUDE.md` says needs sign-off. The two
candidate shapes: return an error the ipc layer renders as a blocked call with
a chat notice (mirrors `J3`), or keep failing open but alert loudly. Whoever
signs off should also decide whether `5/19`'s two branches must agree.

## L86 — one Discord channel is three sessions, and a triggered message is never ambient for its siblings (2026-08-23, proposal)

Reported from sloth as "before observed worked well, now it's selective and
unusable" and, in the channel itself, `dont yiu see the history?`.

Measured on sloth's `#trading` channel (`main/trading`), 2026-08-23:

```
15:58  topic 1541055376747659294   user      15:59 bot replies in that topic
16:04  topic (root)                user      16:05 bot replies
18:13  topic 1540726266351722526   user      18:13 bot replies in THAT topic
18:13  topic 1541055376747659294   user      18:14 bot replies in the OTHER topic
18:14  topic 1540726266351722526   "dont yiu see the history?"
```

Three live sessions for one channel — two Discord threads plus the root. Each
`sessions` row carries its own `session_id` and its own `observed_cursor`, so a
turn in thread A cannot see a word said in thread B. In the Discord UI the human
sees one interleaved conversation and the desk answering the wrong thing.

The threads are real: `discd/bot.go:187-200` sets `topic` only when the channel
IS a thread and rewrites the JID to the parent. Discord names a thread after the
message it was started from, which is why 57 `sessions` rows on sloth have a
topic equal to a root message's id. That part is correct.

The gap is what crosses between them. `<observed>` carries `is_observed=1` rows
only (`specs/5/B`), and a message that TRIGGERED a turn is `is_observed=0` — so
sibling topics never become ambient for each other by construction. `5/F` made
the cursor per-`(folder, topic)` for a good reason (topic A used to eat topic B's
window); nothing was added to let a topic see its siblings' primary traffic.

`observe_group` already implements exactly this shape — "pick up source's
primary-delivered (`is_observed=0`) messages as ambient" — but only across
FOLDERS. The same question intra-folder has no answer.

Second finding — and it RETRACTS the first version of this entry. I claimed the
envelope's budget had become inverted (22KB of `<episodes>` against 349 bytes of
conversation) and read that as the regression. Measuring every turn envelope on
`main/trading` back to June says otherwise:

```
day         turns  avg_prefix  avg_conv  avg_msgs
2026-06-14    209        2016      1399       1.1   <- when it worked
2026-07-15     14       15747      1102       1.0
2026-08-08      8       22014      1239       1.0
2026-08-23     46       22474      1409       1.1   <- now
```

`avg_conv` and `avg_msgs` are FLAT since June. The `<messages>` block has always
carried ~1 message; the agent has always relied on SDK session resume for
history. So the envelope did not regress and the inverted ratio is not the cause.

What did move is `avg_prefix`: **2016 → 22474 bytes, 11x since June**, gradually,
with no step at any deploy. Worth its own entry, not this one.

The measured step change is topics: the bot replied in 1–2 topics per day for
months, then 12 on 2026-08-21 and 4 on 08-23. Since history lives in the SESSION
and not the envelope, splitting a channel into N sessions is exactly what costs
the history — which is what `L87` fixes on the fork axis.

**Proposal (needs sign-off — this changes the prompt-context contract):**

1. A folder's OTHER topics' primary messages become ambient for a turn, capped by
   the existing `observe_window_*` knobs and tagged so they are distinguishable
   from same-topic history. This is `observe_group`'s mechanism applied
   intra-folder, not a new one.
2. Cap `<episodes>` growth — 11x since June with no step is a slow crowd-out,
   not the reported break. Separate change, separate evidence.

Both are behaviour changes on every turn on every instance, so neither ships
inline. `5/B` and `5/F` are the specs to amend.

- **RE-MEASURED 2026-09-21 at `b3b973e2`: the defect is CONFIRMED and proposal 1
  is MIS-SCOPED in both directions.** `store.ObservedSince`
  (`store/message_folders.go:337-356`) reads the observer's own folder with
  `is_observed` **Present** and each watched folder with **Absent**. There is no
  intra-folder primary read, so the hole is real.
  - It is NARROWER than the entry implies. That read is partitioned by FOLDER,
    not by topic, so sibling topics ALREADY share every `is_observed=1` row —
    and on `5/B`'s canonical mention-only shape (a `verb=mention` trigger row
    above a `#observe` catch-all) that is most of the channel. The rows a
    sibling cannot see are exactly the ones that TRIGGERED a turn.
  - It is also WIDER than the entry implies, and this is the half proposal 1
    does not name: BOTH reads carry `{Attr: attrFeedBot, Op: objstore.Absent}`,
    so the ambient window excludes bot rows outright. Thread B never sees what
    the agent SAID in thread A. "dont yiu see the history?" is about a
    conversation, and half of a conversation is the agent's own turns.
  - The bot filter is LOAD-BEARING and its comment says so, so widening it is
    the part that needs the sign-off, not the `is_observed` term.
    `router/router.go`'s `bot="true"` comment names that same filter as the
    reason a bot row never reaches the injected history, and says that without a
    marker the agent "reads its own past words as another user's — the read-side
    twin of BUGS L150". `L184` (local-row half shipped 2026-09-21) supplies the
    marker, so an ambient window COULD now carry the agent's own words without
    the agent reading them as another user's. Before `L184` the same widening
    would have re-opened that twin.
  - `L305`'s grouping fix (`1c263329`) removes the other reported symptom — the
    desk answering thread A inside thread B — but not this one. The two are
    independent.
- **One question for the sign-off:** does the intra-folder ambient window carry
  the siblings' BOT rows as well as their triggering user rows — and if it does,
  is `observe_window_*` still the right budget once the window stops meaning
  "what nobody answered" and starts meaning "the channel"?

## L75 — a re-fed batch resolves by its newest row, so a pending mention is demoted by ordinary chatter (2026-08-21, open — the dead-retry half shipped; this half changes dispatch control flow)

Two faults with one root: a dispatch failure path advances the cursor past the
work it did not do.

**(a) The retry is a dead promise.** `routd/dispatch.go:449-460` logs
"scheduling retry 1/3", then returns `(false, false, false, "", nil)`. That shape
is indistinguishable from "batch handled", so the caller falls through to
`l.advance(chatJID, last)` at `:144`. The `time.AfterFunc` fires 10s later,
calls `ResetTurnForRetry` + `Enqueue`, and the re-poll reads `NewMessages` from
the **already-advanced** cursor — nothing to dispatch. The turn strands `running`
until `SweepExpiredRunning` (`db.go:964`) flips it to `expired`.

Measured: every retry the fleet logged in 7 days died this way.

```
marinade 2026-08-21 02:01:00 WARN "turn failed without reply, scheduling retry" retry=1 max=3  (x2)
turn_context: sched-atlas/general-mem-0@… → expired
              sched-atlas/search-mem-0@…  → expired
(no further log line for either turn_id, ever)
```

The `busy` branch two hundred lines earlier gets this right and says why: "do NOT
advance the cursor … the batch is re-fed … turn_context stays 'running' so the
re-dispatch is live" (`:126-135`).

**(b) A pending mention is demoted mid-failure.** During the 2026-08-22 disk-full
outage a user @-mentioned sloth three times over 1.5h. Each created a turn, each
spawn failed, the cursor correctly did not advance — and then ordinary chatter
arrived. `dispatch.go:51` resolves a re-fed batch by `newestUserMsg` alone, so
the newest non-mention row selected the `#observe` fallthrough route and the
WHOLE batch, mentions included, was stored `is_observed=1` and the cursor moved
past it. The three turns were swept to `expired`. No reply, no error notice; the
next bot contact was 21h later.

```
1540738584259207179 main/trading 2026-08-22T15:04:58 expired  "@sloth what do you think?"
1540739822694440980 main/trading 2026-08-22T15:09:53 expired  "@sloth ?"
1540759670833160263 main/trading 2026-08-22T16:28:45 expired  "@sloth Hi"
```

This is not outage-specific: any chat with the shipped mention-route +
observe-fallthrough pair loses pending mentions whenever a dispatch failure
overlaps ordinary chatter.

**Proposal (needs sign-off — both change dispatch control flow):**

- (a) the retry branch returns the `busy` shape (no advance, re-feed) and drops
  the `AfterFunc` + `ResetTurnForRetry` entirely: the poll re-feed already IS the
  retry, exactly as the transport-failure path works. One return value, but it is
  the dispatch lifecycle, so it is recorded here first.
- (b) route resolution must not demote a message that already owns a live
  `turn_context`. Either resolve per-message and split the batch by resolved
  route (it already splits by sender), or exclude rows with a live turn from the
  newest-message resolve.
- Independently of both: `SweepExpiredRunning` expiring a turn with no bot reply
  must mark its trigger errored and deliver `runFailureNotice`. Today the turn
  dies with no user-visible trace at all, which is the fail-loud rule broken on a
  user-facing path.

- **RE-MEASURED 2026-09-20 at `9d2f1009`: CONFIRMED LIVE, and the entry
  understates it.** The retry is not merely a dead promise — the cursor moves
  past the batch first, so there is nothing left for it to retry.

  `runTurn` returns `(hadOutput, steered, busy, observedMark, rerr)`
  (`routd/dispatch.go:257`). Its retry path returns
  `false, false, false, "", nil` (`:526`) — no output, not steered, **not
  busy**, no mark, **no error**. The caller reads exactly those five at
  `:132-167`: `derr` nil, `busy` false, `steered` false, `mark` empty — so it
  falls through to `l.advance(chatJID, last)` at `:168`.

  Ten seconds later `ResetTurnForRetry` runs. It sets `state=running` and clears
  `run_returned` (`store/turn_context.go:293-296`) and **does not rewind
  `agent_cursor`**. So `l.Enqueue` wakes `processGroupMessages`,
  `NewMessagesForChat` returns zero rows past the advanced cursor, and it
  returns `(false, nil)` at `:33`. The retry is a no-op by construction.

- **The signal it needs already exists, which is what makes this contained.**
  `busy=true` is precisely "do not advance the cursor, re-feed" — `runTurn`'s
  own doc comment says so at `:254-256`, and the caller honours it at `:144-158`.
  The retry path wants the same treatment and does not ask for it.

  It is NOT a drop-in, and that is the one design question: `busy` also calls
  `l.noteBusy` and starts the per-chat backoff, so reusing it would stack a
  second backoff on top of the `time.AfterFunc(retryBackoff, ...)` already
  scheduled. Either the retry drops its own timer and rides the busy backoff, or
  `runTurn` grows a fifth verdict that means "held, re-feed, no backoff". One
  return value either way.

- **(a) FIXED 2026-09-20, `c1bd9473`.** The retry branch returns
  `false, false, true, "", nil`, so the caller takes the hold-and-re-feed path
  it already honours, the cursor stays behind the batch, and the next poll
  re-dispatches the same turn id. The stacked-backoff question answers itself:
  the retry dropped its own timer, so there is nothing left to stack. Three
  attempts now cost 4s + 8s of busy backoff, which a fresh inbound row clears
  at once, against the 10s x 3 the timer promised and never delivered.

  `time.AfterFunc`, `retryBackoff`, `DB.ResetTurnForRetry` and
  `Store.ResetTurnForRetry` are deleted with it. The reset was the re-bind
  written twice: `OpenTurnContext` seeds `bound` with `state=running` and
  removes `run_returned` on every re-dispatch, under a `state != done`
  condition that `pending_retry` satisfies. Net 11 lines out of the shipped
  code.

  Crash recovery closes as a consequence, so it needed no second fix:
  `recoverPending` only `Enqueue`s the chat, and an un-advanced cursor is
  exactly what makes that enough.

  PROVEN BY MUTATION. `TestTurnRetry_SchedulesRetryOnError`
  (`routd/fixes_test.go`) now asserts the cursor is still empty after the failed
  attempt, then clears the busy wait and polls again for a second attempt.
  Restoring the `false` busy value fails it on the cursor
  (`cursor advanced to "2026-09-20T21:18:44.877074502Z\x1fr1"`); with that
  assert removed it fails on `attempts = 1, want 2`. Both halves red, both green
  restored, `go test ./routd/ ./store/ -count=1 -short` clean. Spec `5/12` now
  records the busy re-feed as the retry's backoff.

- **(b) and the sweep-notice item are untouched and still need the decision.**
  (b) is route resolution, not the dispatch lifecycle: choosing between a
  per-message resolve and excluding live-turn rows from the newest-message
  resolve changes which route a MIXED batch takes, on every chat. The
  `SweepExpiredRunning` item is a third concern again — a terminal state that
  owes the user a notice.

**RE-MEASURED 2026-10-08: half (a) is SHIPPED, both parts of it, and this entry never
said so.**

The proposal for (a) was "the retry branch returns the `busy` shape (no advance,
re-feed) and drops the `AfterFunc` + `ResetTurnForRetry` entirely". Both landed:
`routd/dispatch.go:489-491` returns `false, false, true` and its comment names this
entry — *"The re-feed IS the retry: busy=true holds the cursor on this batch. Advancing
first woke a poll with nothing left to retry (BUGS L75a)"* — and a grep for `AfterFunc`
and `ResetTurnForRetry` across non-test `routd/` returns nothing, so the parallel retry
path is gone rather than merely unused.

**(b) is STILL TRUE.** `routd/dispatch.go:39` still takes `newestUserIdx(msgs)` for the
route, and the comment above it explains why the route and the busy key share that row
(`L56`, `L336`) — which is correct for those faults and is exactly what demotes a
pending mention when newer chatter arrives in the same batch. The fix has to make a
pending mention outrank a newer non-mention row for ROUTE selection without breaking
either of those two, so it is a dispatch-lifecycle change and stays a proposal.

The heading is re-scoped so the entry stops reading as two open halves. An entry that
overstates its own remaining work is as misleading as one that understates it.

## L72 — anteval leaks a group per case and every release pays an LLM turn for each (2026-08-23, proposal)

`anteval` cases create child groups (`register_group` / `delegate_group`) and
nothing ever deletes them. 31 of krons' 43 groups are eval leftovers:

```
$ sqlite3 routd.db "SELECT COUNT(*), SUM(folder LIKE 'eval/%') FROM groups;"
43|31
```

They are not inert. `routd.checkMigrationVersion` spawns an auto-migrate turn
per group on every version bump, so each leaked folder bills a real LLM turn
forever:

```
$ sqlite3 routd.db "SELECT SUM(cost_cents), COUNT(DISTINCT turn_id) FROM cost_log
                    WHERE turn_id LIKE 'auto-migrate-eval/%';"
4538|113
```

**$45.38 across 113 turns**, for folders that exist only because the harness
never cleaned up — and it grows by two folders per smoke run. Each also leaves a
docker network behind (`docker network ls` shows 28 `arizuko_krons_eval-*`).

**Proposal (needs sign-off — anteval gains a write it does not have today):**
anteval deletes the child groups its run created, at end of run. `L71` makes the
delete correct; what is missing is a surface — the groups resource registers
`list` and `register` only (`routd/groups_resource.go:41`), so there is no REST
or MCP delete for anteval to call. Either register the delete action on that
resource (one resource, the containment gate it already has), or have anteval
shell out to `arizuko group rm` on the instance host.

Not in scope here: whether leaked folders should auto-migrate at all. They
should not, but the cause is the leak, not the migration hook.

- **RE-MEASURED 2026-09-20: the code half is CONFIRMED LIVE.** `grep` over
  `anteval/pkg/run/` and `anteval/cases/anteval.toml` for `delete_group`,
  `DeleteGroup`, `cleanup` or `teardown` returns **nothing**. No case deletes
  the group it made and the harness has no teardown, so the leak is exactly as
  filed. `ensurePristineRoot` resets the root SESSION, never the group tree —
  the same gap that let a built skill outlive its case (`L153`).
- The 31-of-43 count is instance data and was not re-read.

**Re-measured 2026-09-21 on the live store: the leak has GROWN, and it now
gates another entry.** krons holds **47 eval groups of 59 total**, up from the
31 of 43 this entry recorded. The SQLite evidence above is frozen; the query
that answers it today is the `routd.groups` entity:

```sql
select count(*) filter (where sk_s like 'eval/%'), count(*) from "_ddb_<groups-id>"
```

Every one of those folders still bills an auto-migrate turn on each
`MIGRATION_VERSION` bump, so the per-release cost scales with a number that
only goes up.

**This blocks `L188`, and the two must be sequenced.** `L188` is a backfill
ledger asking for roughly 12 new anteval cases against the 22 that exist. Each
case creates a child group nothing deletes, so closing `L188` first would raise
the leak by about half again and make every later release slower and dearer.
Fix the deletion here first; then the backfill costs what it should.

**Re-measured 2026-09-21 — every figure in this entry has grown, and the docker
half is now the larger one.**

| what | when filed | 2026-09-21 |
| --- | --- | --- |
| krons eval groups | 31 of 43 | **47 of 59** |
| leaked `eval-` docker networks | 28 | **52** |

52 of the host's 81 arizuko networks are leaked eval networks — nearly two in
three. Each was created for a case that finished, and nothing removes them.
That is a bigger footprint than the folders, and unlike the folders it costs
address space in the parent subnet the per-folder /24s are carved from, not
just LLM turns.

The groups query that answers this today (the SQLite one above is frozen):

```sql
select count(*) filter (where sk_s like 'eval/%'), count(*) from "_ddb_<groups-id>"
```

and `sudo docker network ls --format '{{.Name}}' | grep -c 'eval-'`.

**The network half may be separable from the sign-off.** The entry's blocker is
that anteval has no delete SURFACE for groups — a new destructive write. A
docker network has no such gate: it is host state the harness created, and
removing one it made needs no new arizuko permission. If the group delete waits
on a decision, the network cleanup need not.

This entry also gates `L188` (see that entry): backfilling ~12 anteval cases
against today's harness raises both numbers by about half again.

## ✅ FIXED 2026-10-08 L62 — emaid retries an IMAP authentication rejection forever (2026-08-21, re-aimed 2026-09-25)

Filed against `arizuko_emaid_marinade`, which was `Up (unhealthy)` from
2026-08-18 and logged once a minute:

```
{"level":"ERROR","msg":"imap idle error","err":"login: imap: NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)"}
```

**The operator half is moot: the marinade instance was removed 2026-09-25**, its
store consolidated into sloth (`L335`) and its tree staged at
`/srv/data/tmp_removed_marinade_20260925`. Nobody has an IMAP password to fix.

**The code defect outlives the instance and is unchanged.** Verified 2026-09-25:
`emaid/imap.go:117-124` logs the error, sleeps, doubles the backoff to
`maxRetryBackoff` and loops — with no test for a non-transient failure. WISDOM
says retry only transient errors, and an authentication rejection is a
configuration fault, not a network blip. `AUTHENTICATIONFAILED` must stop the
loop and surface once. Same cadence class as `L61`.

- **Severity:** low (no instance runs emaid today, so nothing is failing now; the
  next one to configure it wrong gets a silent minute-by-minute loop)
- **Scope:** `emaid/imap.go`
- **Source:** `emaid/imap.go:117-124`, read 2026-09-25
- **Status:** open — a code fix, and it needs a test that a non-transient
  rejection ends the loop.

- **Status:** ✅ FIXED 2026-10-08. `connect` wraps a rejected credential in
  `errAuthRejected` and `run` returns on it instead of looping. The classification reads
  the server's own RESPONSE CODE — RFC 5530's `AUTHENTICATIONFAILED` and
  `AUTHORIZATIONFAILED`, which exist for exactly this — rather than matching the prose,
  which differs per server.

  **What stays retryable is the point.** A bare `NO` with no code, `UNAVAILABLE`, and a
  plain dial error all keep the backoff: a server refusing logins during maintenance
  sends one of those, and ending the loop there would lose a mailbox that recovers on its
  own. The log names the account, the host and what to fix.

  `TestAuthRejectionEndsThePollLoop` covers both directions and the wrap `connect` adds;
  mutating the classification to a different code fails it with
  `permanentAuthFailure(imap: [AUTHENTICATIONFAILED]) = false, want true`.

## L63 — krons whapd 401-loops, needs a phone re-pair (2026-08-21, open)

`arizuko_whapd_krons` is `Restarting` continuously. Known cause: the WhatsApp
link is gone and the pairing must be re-established from the user's phone
(pairing-code path, not QR — the recipe is in the whapd pairing memory). Not a
code defect; recorded so the fleet's one permanently-red container has an entry
rather than being treated as normal.

- **Source:** `whapd/`, operator action required.

## Error-hygiene sweep: swallowed / mis-levelled errors in the hot path (2026-07-11, ONE item left, and it is design work)

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
  every tool call for the group. **FIXED 2026-08-15** — both the peer-cred
  read failure and the UID mismatch are now `slog.Error` with the "connection
  rejected, group gets no tool calls" symptom (log-add only, no control-flow
  change).
- `crackbox/pkg/admin/registry.go:84-107` `flushLocked` — `Warn`+return on
  disk-persist failure inside a void fn; handler still ACKs 200 → stale egress
  allowlist can reappear after restart. **FIXED 2026-08-15** — all five
  failure branches (marshal/tmp-create/tmp-write/tmp-close/rename) elevated to
  `slog.Error` naming the "egress allowlist change not persisted, will revert
  on restart" symptom.
- `routd/turns.go:670-676` `recordTurnResult` — discards `PutMessage`/`PutSession`
  errors (this path historically broke all replies post-split — verify first).
  **FIXED 2026-08-15** — verified safe (log-add only, the delivery itself is
  already done via `appendAndDeliver` before the discarded `PutMessage`, so
  logging doesn't touch the reply path that broke in the 2026-06-08 outage).
  `PutMessage`, `PutSession`, `PutCost` (per-model), and `SetTurnState("done")`
  now each log `slog.Error` naming their specific symptom (missing from
  history/audit, lost session continuity, missing cost attribution, turn
  stuck in-progress).
- `runed/docker.go:66` `steerInto` — **FIXED 2026-08-15**: `writeIPCInput`
  failure now logs `slog.Error`, distinguishing a real disk-write failure from
  the documented SIGUSR1 race the function's doc comment describes (both
  previously returned `false` silently).
- `routd/dispatch.go:263,305,320` (renumbered ~300/349/379 at HEAD) terminal
  `SetTurnState("done")` unlogged — **FIXED 2026-08-15**, all three call
  sites (steered, terminal-failure, clean-completion) now log `slog.Error`
  naming "turn may appear stuck in-progress" (or, for the steer site, "may be
  re-dispatched, duplicating output").
- **Still open, unverified:**
  - `runed/manager.go` — **re-audited 2026-09-21, not a gap.** The package has
    exactly ONE discard left, `_ = m.executor(sp.Kind).Kill(...)` (`:474`), and
    it is a deliberate best-effort kill: the DURABLE write on the next line,
    `m.db.KillSpawn`, IS checked and returned, and the comment between them
    says why (`KillSpawn` re-checks liveness, so a run that finished between
    the read and the kill keeps its terminal state). Nothing silent.
  - `ipc/ipc.go:340-345` (conn-limit reject, no error frame) — already logs
    `slog.Warn`, so not silent; the gap is a missing JSON-RPC error frame to
    the caller, which needs the request id threaded through `writeJSON` —
    design work, not a log-add.
  - `routd/budget.go` — **the policy question is SETTLED, and it went the
    other way: it fails CLOSED.** `budgetGate` returns its error, and
    `routd/dispatch.go:316-322` logs `slog.Error` ("model gateway did not
    answer the budget gate; no spawn") and returns `queue.ErrDependencyDown`,
    so no run is dispatched. A cap set on an instance with no gateway to
    enforce it is now its own `slog.Error` too (`budget.go:31`).
  - `crackbox/pkg/host/` — **subsystem gone, verified 2026-09-21.** The
    package does not exist; `crackbox/pkg/` holds only `run`. Nothing to make
    consistent.

Adapters (teled/discd/slakd/mastd/bskyd/reditd/emaid/chanlib) swept clean for
this class.

## Root-owned group home → agent runs config-less, silently never replies ("typing but no reply") (2026-07-11, ROOT CAUSE FIXED, HARDENING 2/3 DONE, spawn-abort residual OPEN)

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

**Hardening — two of three pieces now DONE, re-verified 2026-08-15:**

- **Fail loud on write failure — DONE** (`01a61a07`). `container/runner.go`
  `seedSettings`'s `os.WriteFile` failure now logs `slog.Error` naming the
  exact symptom: "group home not writable — agent will run config-less and
  not reply; chown the home to the run uid" (`runner.go:899`).
- **Ghost-group guard — DONE** (`c9979e78`, GB1 L4a). `routd/dispatch.go:147`
  `runTurn` now refuses to dispatch when `!l.db.GroupExists(folder)`: logs
  `slog.Error("dispatch refused: unregistered group", ...)`, sends
  `ghostGroupNotice` to the chat, and consumes the batch instead of spawning.
  This directly closes the reproduced `krons/content` scenario (a ghost,
  routed-to but not in `groups`) — that exact case can no longer reach a
  config-less spawn.
- **Abort spawn / deliver an error turn on write failure — still OPEN.**
  `seedSettings` (void fn) and `cpDirImpl` (still `slog.Warn`, not `Error`,
  for its own mkdir/readdir/lstat/read failures) log and `return`;
  `buildMounts`/`container.Run` still have no error return, so a
  **registered** group whose home is nonetheless mis-owned (permission drift
  unrelated to being a ghost) still spawns config-less silently past the
  loud log — an operator watching logs catches it, one polling only
  `turn_results` does not. Propagating a write failure into an aborted spawn
  + delivered error turn crosses the `container`→`runed`→`routd` call chain
  (`Input`/`RunResult` contract change) — a redesign, needs sign-off, not
  attempted here.

## F5 — `store.UserScopes` decides folder visibility without action or deny (2026-08-05, reader half FIXED; the read GATE is ONE signed sentence away — re-verified 2026-09-21, and krons now has a deny row)

`specs/5/32` makes `auth.Authorize` the one question the ACL answers.
`store.UserScopes` (`store/acl.go:200-228`) is a second reader:
`SELECT DISTINCT scope FROM acl WHERE effect='allow' AND principal IN (…)`.
It ignores the `action` column entirely, has no deny-wins precedence against a
scope that also carries an allow row on a different action, and is blind to
wildcard-principal rows that `auth.Authorize` honours.

Its callers split, and only one half is a defect:

- **Inert** — `dashd/authz.go` feeds the result into `auth.Caller.Extra` as
  principal aliases, and a full `auth.Authorize` downstream re-checks action,
  deny and wildcards. The blindness cannot escape here.
- **Live** — `dashd/authz.go` `visible()` / `requireVisible()` /
  `requireOperator()` and `onbod/dash.go:20-29` treat the derived set as the
  terminal 200/403 decision with no downstream `Authorize`. A deny row that
  `auth.Authorize` would honour does not hide a folder from the dashboard.

So the violation is real but confined to the read/visibility surface, not the
mutation surface. Routing visibility through `auth.Authorize` changes which
folders appear for existing users — a live authorization-behaviour change.

- **Severity:** medium (deny rows do not suppress dashboard visibility)
- **Scope:** store.UserScopes vs auth.Authorize
- **Affected:** all instances — dashd + onbod folder lists
- **Source:** store/acl.go:200-228; dashd/authz.go visible/requireVisible/requireOperator; onbod/dash.go:20-29
- **Status:** PARTIALLY FIXED. The second READER is gone: `UserScopes` moved to
  `auth.UserScopes` and now projects the same rows `Authorize` loads (expanded
  principals + wildcard-principal rows) instead of its own
  `SELECT DISTINCT scope … principal IN (…)`. That closes the wildcard-principal
  blindness outright — a `google:*` grant authorized fine yet produced an EMPTY
  `X-User-Groups`. Documented and tested as a LISTING that can never be a
  verdict, with the deny/action gaps pinned as tests rather than left implicit.
  Live impact nil: no instance has a wildcard-principal row or a deny row (all
  43 rows fleet-wide are `allow`).

  **STILL OPEN, still needs sign-off:** dashd's read gate remains a scope-list
  match rather than an `auth.Authorize` call, so a deny row still cannot
  suppress dashboard visibility. That last step is a policy decision, not a
  refactor — it must pick WHICH action dashboard read requires (`interact` is
  the natural floor), and a `[]string` header is structurally incapable of
  carrying deny, so the gate has to move off the header entirely. Not shipped
  inline. Note the original entry overstated onbod: `onbod/dash.go`
  `requireOperator` only tests for `**`, it does no per-folder visibility.
  **Re-verified 2026-08-15 at `bcb5bb83`; still exactly as described, still a
  redesign.** `store.UserScopes` is gone — `store/acl.go:196` is now only a
  pointer to `auth.UserScopes` — so the second READER really did close. The
  live half is unchanged: `dashd.callerScope` (`dashd/authz.go:44`) reads the
  `X-User-Groups` header, falls back to `auth.UserScopes(sub)`, and treats
  `slices.Contains(allowed, "**")` as operator; `visible()` then decides
  200/403 by `auth.MatchGroups` over that list. There is no `auth.Authorize`
  anywhere in dashd's read path. Not fixed here: closing it picks which action
  dashboard read requires and moves the gate off a `[]string` header that
  cannot represent deny — an auth-model change, which is precisely the class
  that ships only after sign-off.
- **Found:** specs/5 frontmatter audit; `5/32` demoted to `partial`, now shipped.

**Re-verified 2026-09-21 at `a388cad7` — unchanged, and still the same class of
decision.** The closed half stayed closed: `auth.UserScopes`
(`auth/authorize.go:239`) reads through `policyRows`, the single reader
`Authorize` uses, so the wildcard-principal blindness cannot return, and its doc
comment now states the limit as a rule — *"It is a LISTING, and it can never be
the decision. A `[]string` carries no action and cannot represent a deny row, so
anything gating access MUST call Authorize; a caller that treats this result as
a verdict has built a second, weaker evaluator."*

dashd is still that caller. `callerScope` (`dashd/authz.go:23`) takes
`auth.CallerGroups(r)` from the proxyd-signed `X-User-Groups` header, falls back
to `auth.UserScopes(sub)`, and returns `slices.Contains(allowed, auth.OperatorGroup)`
as operator. `visible()` (`dashd/authz.go:46`) then decides 200/403 with
`auth.MatchGroups(allowed, folder)`. `requireVisible` and `requireOperator` are
the only two read gates and both go through it. There is no `auth.Authorize` in
dashd's read path.

NOT fixed here, and the reason is the one the entry already gives: closing it
picks WHICH action a dashboard read requires and moves the gate off a `[]string`
header that structurally cannot carry a deny row. That is an auth-model change
on a live surface — it changes which folders existing users see — so it is a
sign-off, not a refactor. **The owner's decision is one sentence: is `interact`
the floor action for dashboard read, and may the visibility gate stop trusting
the header and call `auth.Authorize` per folder instead?**

**Re-verified 2026-09-21 at `e7f1bac3`. The call path is unchanged, and three
facts sharpen the decision that were not on the record.**

The premise holds exactly: `callerScope` (`dashd/authz.go:23`) →
`auth.CallerGroups(r)` → `visible()` → `auth.MatchGroups`. dashd's WRITE gate
does go through the evaluator — `adminOn` calls
`auth.Authorize(store, caller, "admin", scope, nil)` — so the split is read
without an action, write with one, in the same file.

**1. "All 43 rows fleet-wide are allow" is no longer true, and the binary is
wrong too.** krons' `routd.acl` holds 42 rows today: 40 `allow`, **1 `deny`**,
and **1 `async`** — a third effect the entry's allow/deny framing does not
mention. The deny row is
`folder:rhias/nemo | hold:mcp:like | rhias/nemo | chatJid=…,reaction=👀,targetId=3148`.

**Live impact is still nil, but no longer for the reason the entry gives.** It
is nil because two independent guards each stop that row reaching an
`interact` read: `actionCovers("hold:mcp:like", "interact")` is false (the
switch covers only `*`, `admin` and `mcp:*`), and `paramsMatch` fails a
param-bearing row against a `nil` params call (`auth/authorize.go:442`). The day
someone writes a deny row with a bare action and no params, moving read onto
`Authorize` changes which folders that user sees. Also worth knowing before
choosing: the evaluator treats every effect that is not literally `"deny"` as an
allow, so `async` lands in the allow branch.

**2. "Call `auth.Authorize` per folder" is not a drop-in, and the cost is
structural.** `Authorize` runs `expandPrincipals` plus `policyRows` — two store
queries, `ACLRowsFor` and `ACLWildcardRows` — on EVERY call, with no cache.
`countVisible` (`dashd/authz.go`) calls the gate once per row for groups,
errored chats and failed tasks, so a literal substitution turns one dashboard
render into 2+ store round trips per row against extenddb over HTTP. Closing
this needs a request-scoped evaluator — one policy read, many folders — which
`auth` does not expose today. That is why it is a redesign and not a rename.

**3. Do not touch `jidFolder` while doing it.** `visible()` is fed by
`d.jidFolder(jid)`, which resolves a `web:`/`hook:` JID through the folder its
mint RECORDED precisely because the JID grammar reads `hook:a/b/c` as folder
`a/b` and showed tenant `a`'s message to tenant `a/b` (BUGS `L4`). A rewrite
that routes visibility through `Authorize` must keep that resolver in front of
it; folding the folder derivation back into the gate reopens a closed
cross-tenant leak.

**Sharpened to ONE sentence.** The two halves are not separable — `Authorize`
takes an action argument, so the gate cannot move without naming one, and the
`[]string` header cannot carry an action, so naming one demotes the header. They
are one change with two parameters:

> **Should a dashboard READ become an authorization question — `Authorize(caller,
> "interact", folder)` with deny-wins — accepting that it needs a request-scoped
> evaluator (one policy read, many folders) and demotes `X-User-Groups` from the
> source of truth to a hint?**

Yes means building that evaluator and re-verifying every dashboard page against
the 42 live rows. No means `auth.UserScopes`' doc comment keeps naming a rule
dashd does not follow, and that gap should be written down where dashd reads it
rather than left as a latent contradiction.

## L50 — LoadConfigFrom leaks one instance's .env into the process (2026-08-18, SILENT HALF FIXED 2026-09-20; the prescribed cause fix is refuted)

`core.LoadConfigFrom` (`core/config.go:185`) does two things a loader should not:

- `godotenv.Load(dir/.env)` writes every var of THAT instance's `.env` into the
  process env, and nothing ever unsets them;
- `os.Setenv("DATA_DIR", dir)` fires only when `DATA_DIR` is empty, so the FIRST
  instance a process touches pins the data root for every later call.

`compose.envOr` (`compose/compose.go:1344`) falls back to `os.Getenv`, so the
leaked vars become silent fallbacks for a LATER `compose.Generate` — a route
gated on `ONBOARDING_ENABLED` can be emitted for an instance whose own `.env`
never mentions it.

Not currently reachable in production: one CLI command is one process, one
instance. It is reachable the moment anything loops over instances in-process,
and it is already reachable in the test binary — wiring `syncPackageSkills`
(`L47`) into `generate` made `TestGenerateSkipsGatedOffRoute` fail from a
neighbouring test's `.env`. Worked around there by pinning `DATA_DIR` and
`ONBOARDING_ENABLED` per test, which is a test fix, not the cause fix.

**That cause fix is REFUTED, re-measured 2026-09-20 at `b09de9ea`.** Both
mutations are deliberate and each one closed a named earlier bug, so a
non-mutating loader reopens both:

- the `.env` write is `LoadInstanceEnv` (`core/config.go:193`), and its own
  doc says every host CLI door calls it because `DYNAMODB_URL` decides whether
  a store is the server or a file — a subcommand that skipped it wrote to the
  file while every daemon read the server (`L294`);
- the `DATA_DIR` default is `LoadConfigFrom`'s (`core/config.go:176`), and
  without it the root fell back to the operator's cwd, so `arizuko create` and
  `group add` wrote the `groups/` skeleton into whatever directory the command
  ran from (2026-07-11).

What survives is the hazard, not the prescription: the process env is global
and one-way, so the FIRST instance a process touches owns every var. No
production path loops instances — grepping every one of the 15
`LoadConfigFrom` call sites finds no loop — so this is latent, and nothing
stated the constraint or would notice it breaking.

- **Severity:** low while latent; the failure mode when it wakes is one
  instance generating compose against another's env, which looks healthy
- **Scope:** `core.LoadInstanceEnv`
- **Affected:** any future in-process instance loop; the test binary today
- **Source:** `core/config.go:171-208`; 15 `LoadConfigFrom` call sites, none
  in a loop; measured 2026-09-20
- **Status:** the silent half is FIXED; the non-mutating loader is refused as
  a fix, not deferred
- **Fix:** `LoadInstanceEnv` remembers the first directory and WARNS when a
  later call names a different one, saying that the first instance still owns
  the env and `DATA_DIR`. Tests
  `TestLoadInstanceEnvWarnsOnASecondInstanceInOneProcess` and
  `TestLoadInstanceEnvIsSilentForTheSameInstanceTwice` — the second one
  matters as much, because one instance loaded twice is every normal CLI
  command and a warning that fires there is noise nobody reads.


## L42 — krons WhatsApp is down: the paired session is invalidated (2026-08-16, BLOCKED on the operator's phone)

    whapd  error  session invalidated, delete auth dir and re-pair  code=401

whapd restart-loops on it, so krons' primary channel delivers nothing. This is
not a code fault and nothing in the repo fixes it — WhatsApp expired the paired
session and only a re-pair restores it.

It was masked until tonight: whapd was crash-looping earlier on `L41`'s missing
mount and never reached the login, so the 401 is the fault underneath. It is
referenced in passing elsewhere in this file as "the known whapd re-pair loop"
and has never had an entry, which is why it has stayed unaddressed.

Use the pairing-code path, not QR (recipe in the operator's notes): whapd
`--pair <phone>`. The QR path is the one that does not work here.

- **Severity:** high — the primary channel is dead
- **Scope:** operational, krons
- **Status:** blocked — needs the operator's phone, nothing else
- **Fix:**

## L41 — an instance's compose fragments freeze forever once they differ by a byte (2026-08-16, LOUD NOW and the warning NAMES THE ABSENT LINES 2026-09-21; auto-repair still needs sign-off)

Found when tonight's `L32` whapd fix crash-looped krons' primary channel:

    Error: EACCES: permission denied, mkdir '/srv/data/store/whapd/outbound-dedup'

`template/services/whapd.yml` carries the mount the ledger needs. The instance
copy at `/srv/data/arizuko_krons/services/whapd.yml`, dated 2026-07-28, does
not. `env/` regenerates on every restart; `services/` does not.

`R1` already fixed this class: `compose.RelinkCatalog` replaces an installed
fragment with a symlink at the catalog, so a template edit reaches every
instance. **It only converts a BYTE-IDENTICAL copy.** krons' fragments were
written by an older generator (`# Converted from services/whapd.toml by
'arizuko generate'` — a header today's template does not have), so every one is
classified as an operator hand-edit and skipped, silently, on every generate
since. The R1 fix reaches only instances that did not need it.

Three live faults on krons traced to this one cause:

- **whapd crash-looped** — the `L32` dedup mount never arrived. WhatsApp, the
  primary channel, was down. FIXED live 2026-08-16 by adding the mount and
  `WHAPD_STATE_DIR` to the instance fragment (additively — the instance copy
  carries a `CHANNEL_SECRET` the template does not, so no overwrite).
- **`F53` is this bug** — teled's `${DATA_DIR}/store/teled` mount and `DATA_DIR`
  are in the template and absent live, which is exactly why the getUpdates
  offset resets on every recreate. `T1`'s `RUNED_RUN_TIMEOUT` is missing too.
- **ttsd and kokoro read the whole instance `.env`** — the template scopes them
  to `env/ttsd.env` / `env/kokoro.env` precisely so they do not. kokoro is a
  third-party image, and it currently receives `SECRETS_KEY`, `AUTH_SECRET`,
  `CLAUDE_CODE_OAUTH_TOKEN` and every bot token.

**SHIPPED**: `RelinkCatalog` now returns the diverged set and `generate` prints
a per-fragment warning naming the frozen file. That is the loud half — it would
have surfaced all three above at the first restart after the template changed.

**PROPOSAL, needs sign-off.** The check cannot tell "the operator edited this"
from "we wrote this two versions ago", and it must, or the warning is the only
thing that ever happens. Our own output is positively identifiable by its
generated header, so the byte-compare can consult it: a fragment carrying the
`# Converted from services/<x>.toml by 'arizuko generate'` marker is ours and
may be relinked; anything else stays an operator edit and is only reported.
Shipping that auto-repairs every instance created before the catalog moved —
which is also why it is not shipping without a decision.

- **Severity:** high — it silently withholds every adapter mount and env var
  added since an instance was created, and it caused a live channel outage
- **Scope:** compose fragment installation
- **Affected:** every instance whose `services/*.yml` predate the current
  catalog (krons: whapd, teled, ttsd, kokoro)
- **Source:** `compose/fragments.go` `RelinkCatalog` (byte-identical gate);
  `cmd/arizuko/packages.go` `relinkCatalog`
- **Status:** warning shipped and now names the absent lines; auto-repair proposed
- **Fix:** `compose.Diverged` + `missingLines` (`compose/fragments.go`), printed
  by `relinkCatalog` (`cmd/arizuko/packages.go`)

**✅ VERIFIED FIRING 2026-09-21 at `e7f1bac3`, and the report was half a report.**
The premise holds on live data: sloth has three frozen fragments — `discd.yml`,
`extenddb.yml` and `teled.yml` are real files while `slakd.yml`/`slakd.yaml` are
symlinks, so the classifier is working and three fragments are skipped on every
generate.

The warning fired and named the file. It did not say what differed, and
"differs from the catalog" is not something an operator can act on — it names a
file and hands back the diffing job that produced this entry in the first place.
It also pointed at `template/services/<x>.yml`, a repo-relative path, when
`relinkCatalog` already holds the resolved host catalog dir.

**The warning now names the absent lines.** `RelinkCatalog` returns
`[]Diverged{Name, Missing}` instead of names alone; `Missing` is the catalog's
own lines, verbatim, that the installed copy does not carry. Run against sloth's
real fragments:

    services: services/discd.yml differs from the catalog and is FROZEN
      1 catalog line(s) absent from it:
        RUNED_RUN_TIMEOUT: '${RUNED_RUN_TIMEOUT:-20m}'
    services: services/extenddb.yml differs from the catalog and is FROZEN
      1 catalog line(s) absent from it:
        - '127.0.0.1:${STORE_HOST_PORT:-18443}:18443'
    services: services/teled.yml differs from the catalog and is FROZEN
      4 catalog line(s) absent from it:
        volumes:
        - '${DATA_DIR}/store/teled:/srv/app/home/store/teled'
        DATA_DIR: '/srv/app/home/store/teled'
        RUNED_RUN_TIMEOUT: '${RUNED_RUN_TIMEOUT:-20m}'

teled's four are this entry's own `F53` and `T1`, still absent on sloth today.
**discd and extenddb are new live findings**: discd has no typing backstop, and
sloth's extenddb publishes no host port. Both are operator work, not platform.

**The noise had to go or the report would train people to skip it.** sloth's
frozen `teled.yml` was written by the older generator, which quotes with `"`
where the catalog quotes with `'` — a byte-level diff calls nearly every line
changed. The comparison runs on a normalized key (quotes stripped, whitespace
collapsed, comments and blank lines dropped) while the report prints the
catalog's verbatim line, so teled reports 4 lines rather than 12 and every one
is a real absence. `compose/fragments_test.go` pins both halves: the restyled
copy reports nothing, the copy missing a mount names it.

**The auto-repair is untouched and still needs sign-off.** Nothing here converts
a fragment; the byte-identical gate is exactly as it was. This only makes the
report actionable while the decision waits.

## L40 — live krons cannot tell two anonymous visitors apart (2026-08-16, open — needs an infra decision)

Found by anteval `chat-entrypoint`, which failed `429, want 200` on a freshly
minted chat token. The code half is FIXED in `3e19ea3e` (the shield keyed on
`r.RemoteAddr` instead of the existing `clientIP` helper). The deployment half
is not, and no code change can close it.

`proxyd` on krons logs exactly two source addresses over six hours:
`127.0.0.1` (239) and `10.0.5.1` (18). `10.0.5.1` is the host's own default
gateway (`enp0s2 10.0.5.2/30`), and `krons.fiu.wtf` resolves to `65.21.89.43`,
which is not an address on this host. Every external visitor therefore reaches
proxyd as one address. `TRUSTED_PROXIES` is unset, so `fixForwardedFor`
correctly discards any inbound `X-Forwarded-For` and stamps the peer.

Three mechanisms degrade together, silently until now:

- the anon chat DoS shield (`CHAT_ANON_DOS_RPM`, default 10/min) is one bucket
  for the whole world — one reader locks out the next, which is the 429
  anteval hit;
- the auth-denied log names the hop, not the scanner — the exact regression
  `clientIP`'s comment says was already fixed once;
- `webd.anonSender` hashes that address, so every anonymous chat visitor shares
  one `anon:<hex>` identity.

**ANSWERED 2026-08-16 by the deployed warning.** The edge DOES forward a
client address:

    WARN untrusted edge proxy: X-Forwarded-For discarded, client identity is
    the hop  peer=10.0.5.1  fix="add 10.0.5.1 to TRUSTED_PROXIES"

So the real client IP arrives at proxyd on every request and is thrown away.
One env var recovers it.

**Do not set `TRUSTED_PROXIES` before one test.** proxyd reads the LEFT-most
hop, so trusting an edge that appends to a client-supplied header lets any
caller forge their own source, bypass the shield and poison the audit log.
Decide it empirically: set `TRUSTED_PROXIES=10.0.5.1` on krons, restart, then
compare two requests — one plain, one carrying `X-Forwarded-For: 1.2.3.4`.

- both log the same real client address → the edge overwrites; keep the setting
- the second logs `1.2.3.4` → the edge appends; revert, and fix the edge to
  send `$remote_addr` rather than `$proxy_add_x_forwarded_for`

This is a live-host security posture change on infrastructure we do not own
here, so it is the operator's call, not a code fix.

Secondary, worth measuring once clients are distinguishable: `/chat/` has six
routes and a page load spends root + config + history + POST, with a CORS
preflight per POST for an embedded widget. A 10/min budget is then ~3 messages
a minute for one visitor. Possibly the intended shield strength; currently
untestable, because the budget is not per-visitor.

- **Severity:** high — anonymous chat is the public entry surface
- **Scope:** krons deployment (all instances behind the same edge)
- **Affected:** every anonymous `/chat/` and `/hook/` caller
- **Source:** `proxyd/main.go` `fixForwardedFor`; `TRUSTED_PROXIES` unset in
  `/srv/data/arizuko_krons/.env`
- **RE-MEASURED 2026-09-20, and the obvious lever is the WRONG one.** The
  tempting fix is to add the gateway to `TRUSTED_PROXIES` — it is unset on
  krons, so the `127.0.0.1/32` default applies and `10.0.5.1` is not in it.
  **That would change nothing, and it would widen a trust boundary for no
  benefit.**
- **Because no `X-Forwarded-For` arrives at all.** proxyd already reports this
  case: `untrustedEdgeOnce` (`proxyd/main.go:119-126`) warns once per peer with
  "untrusted edge proxy: X-Forwarded-For discarded, client identity is the hop"
  and names the exact CIDR to add. It fires only when a peer SENDS an XFF that
  is not trusted. **It has not fired on krons in 24 hours.** No header is being
  discarded, so there is none to trust.
- **So the lever is at the edge, outside this repo.** Whatever terminates
  `krons.fiu.wtf` / `65.21.89.43` forwards to the host without the client
  address. Until that edge sets `X-Forwarded-For`, every external visitor
  reaches proxyd as the same hop and `webd`'s `anonSender`
  (`webd/route_token.go:33-42`) derives one identity for all of them — which is
  correct behaviour on the input it gets.
- **The code half stays fixed** (`3e19ea3e`), and `webd/anonshield.go:23-31`
  reads the leftmost XFF token, so the moment the edge sets one this resolves
  with no code change.
- **Status:** open — infra, and now a specific one: set `X-Forwarded-For` at the
  edge, THEN add that edge to `TRUSTED_PROXIES`. Doing the second without the
  first is a widening that buys nothing.

## L31 — a product never really updates: four of six payload kinds are seed-once (2026-08-15, CONFIRMED at HEAD 2026-09-21 with three corrections; still a proposal — needs sign-off, and it is NOT `L315`)

`arizuko packages upgrade` / `sync` re-apply exactly two payload kinds: skills
(and `blendSkills` REFUSES any skill whose hash moved, so a locally edited one
is skipped) and `settings.json` keys not already present. `PERSONA.md`, the
`CLAUDE.md` region, `facts/` and `tasks.toml` go through `blendSeedOnce`, which
writes only when nothing is there yet and never rewrites
(`container/blend.go` `seedOnce`).

Consequence: a product author cannot fix anything they shipped. A typo in
`PERSONA.md`, a wrong fact, a bad default task — every group that already
installed the product keeps it forever, and the author has no channel to
correct it. `5/28` calls itself a package manager; on four of six payload kinds
it is a one-time copy.

- **Severity:** medium — no failure, but the headline capability does not work
- **Scope:** `5/28` composition, the blend table's "On upstream update" column
- **Affected:** every installed product; nothing on the fleet has been upgraded
  in place because there is no meaningful upgrade
- **Source:** `container/blend.go` `blendSeedOnce` vs `blendSkills`;
  `specs/5/28-packages.md` §Composition table
- **Status:** proposal, operator-stated 2026-08-15. **A product is overwritten
  on upgrade, like any software package; customization lives in the local
  layer, not in the package's files.**

  The local layer already exists and is already managed: the agent's memory and
  `facts/`, the operator's `~/CLAUDE.md` overlay, `.disabled` to drop a skill.
  The contract becomes two clean surfaces — product files are the product's,
  local state is the group's — instead of one file with two owners and a
  dirty-hash arbitration between them.

  `CLAUDE.md` is the precedent, not an exception: marked regions already give a
  product ownership of its own region while text outside it is never rewritten
  (`<!-- arizuko:package:<name> BEGIN/END -->`). The proposal extends that
  contract to the other payloads rather than inventing one.

  **What it dissolves.** Per-product `migrations/` (`L?`, deferred then refused
  2026-08-15) exists to transform state you cannot overwrite. If files are
  replaced, the only state left to migrate is the agent's own memory, which the
  agent curates. The deferred runner stops being owed.

  **What it costs, and the one question to settle.** Everything edited in place
  today needs a home in the local layer. `PERSONA.md` is the sharp case:
  operators do tune a persona by editing the file, and under this rule that edit
  is overwritten. Either a persona tweak becomes an overlay, or `PERSONA.md`
  keeps a carve-out — that choice is the whole decision, and it should be made
  before the table is rewritten. `tasks.toml` needs the same answer (an
  operator-added task must not vanish on upgrade).

  On sign-off this is a `5/28` spec edit first — the blend table's two right-hand
  columns — then `container/blend.go` moves payloads from `blendSeedOnce` to the
  managed path, keeping dirty-detection only where a carve-out survives.
- **Fix:**

- **RE-MEASURED 2026-09-21 at `17459f6a`: the defect is CONFIRMED and three
  details in the entry are wrong.**

  Seed-once at HEAD, by code and not by comment: `blendSeedOnce`
  (`container/blend.go:395`) writes `PERSONA.md`, `Dockerfile.ant` and every
  union/verbatim payload (`facts/`, `tasks.toml`) through `seedOnce`, which
  writes only when nothing is there yet; `blendClaude` skips an existing region
  outright (`container/blend.go:471`, `if present[name] { continue }`). The two
  managed kinds are `blendSkills` and `blendSettings`. The headline count holds.

  1. **`packages upgrade` cannot reach a product at all** — it reads
     `rdb.InstalledPackage(routd.InstanceWide, name)`
     (`cmd/arizuko/packages.go:1029`), so a group-scoped product record is never
     found and the verb answers "is not installed". `reapplyPackage` is the
     compose-fragment plane (`svcDir`, `*.yml`) and never calls the blend. The
     verbs that DO run the blend are `packages sync` — which loops every
     non-instance-wide folder through `applyProductMix`
     (`cmd/arizuko/packages.go:1083`) — and `products apply <folder>`.
  2. **The `CLAUDE.md` region is not in `blendSeedOnce`**; it is `blendClaude`'s
     own skip. Same outcome, different function, and it matters because the
     region is the precedent the proposal builds on.
  3. **`Dockerfile.ant` is a fifth seed-once payload** the entry does not name
     (`image_extension`). A product cannot fix its own image extension either.

- **The cost estimate is too high, and that opens a third option the entry does
  not list.** "Moves payloads to the managed path" reads as new machinery; it is
  not. `blendSkills` already does per-file dirty-detection end to end: `isDirty`
  against the recorded `AssetHashes`, report as `Dirty`, skip the write, and
  carry the recorded hash forward so the next apply does not call the edit clean
  and clobber it (`container/blend.go:344-372`). `dropStale` already withdraws
  what a product stops shipping. Moving a payload is a classification change,
  not a new mechanism.

  So `PERSONA.md` does not have to be either overwritten or carved out. The
  third answer is **managed with dirty-detection**: an untouched persona takes
  the author's typo fix, an operator-tuned one is reported and skipped exactly
  like a locally edited skill today. That is the same contract the operator
  already lives with on skills, so it adds no concept.

  **The question for the owner is one sentence: does an operator's edit to
  `PERSONA.md` / `tasks.toml` block the author's update (managed + dirty-skip,
  the skills contract) or lose to it (overwrite, customization moves to the
  local layer)?** Dirty-skip costs an author whose fix silently does not land on
  the tuned groups — the group must be reconciled by hand. Overwrite costs an
  operator whose tuning disappears on the next `packages sync`, with `sync`
  being the verb an operator runs for unrelated reasons.

- **NOT the same question as `L315`, and not blocked by it.** `L315` defers
  re-applying the **rows** a catalog manifest declares (`scheduled_tasks`,
  `network_rules`, `acl`, `web_routes`) because overwriting recorded row
  identities strands the rows `remove` must later delete — it needs a withdraw-
  and-reinstall transaction that does not exist. This entry is the **file**
  plane, a different verb (`packages sync`/`products apply`, not
  `packages upgrade`) and a different mechanism (`BlendProducts`), and the
  withdrawal problem that blocks `L315` is already solved here by `dropStale`
  (`container/blend.go:289-300`, BUGS `L34`). Answering one does not answer the
  other; they should stay two entries.

## L13 — `secret_use_log` is write-only: no read surface on any face (2026-08-11, proposed 2026-08-15 — RE-MEASURED 2026-09-21: the ENTITY has no reader, the FACT has two. Do NOT build the proposal below)

The table (routd/migrations/0008-secrets.sql) is written on every resolved
secret (`routd/mcp.go:503`, `store/secret_use_log.go:38`) and read by nothing:
no resreg resource, no REST/MCP face, no dashd page, no CLI verb — only the
side `audit_log` row each write also emits is reachable. This repeats the exact
class BUGS `F29`/`F35` closed for the four `audit_log` owners ("audit rows
reachable only with sqlite3"), and it outlived `F68` (2026-08-09), which fixed
this table's writer fields without giving it a reader. Cost: the per-key
forensic detail (key, tool, scope_kind) the writer was just fixed to record
answers "who used which secret" only to an operator with sqlite3 on the host.

- **Severity:** low
- **Scope:** secret-use forensic log read surface
- **Affected:** routd, store
- **Source:** `store/secret_use_log.go:38`, `routd/migrations/0008-secrets.sql`
- **Status:** PROPOSED, not built — the premise is CONFIRMED at 28d20228
  (readers are four test files and nothing else; no `Table:` declaration, no
  REST handler, no dashd page, no CLI verb), but the fix is a new wire contract,
  which this pass does not ship unreviewed.
  Why it is not a mechanical registration: in this codebase a resource's name IS
  its wire identity, and `5/17` derives the MCP tool from the same declaration
  that serves REST. Registering `secret_use_log` therefore publishes a new
  `/v1/secret_use_log` path AND a new agent-visible tool in one move, and the
  question "may an AGENT read which secrets were used, by whom, with which
  tool?" is a security decision the operator owns, not a detail of the wiring.
  Recommended shape, for approval:
  - Owner routd (it owns the table, routd migration `0008`), read-only: `list`
    with folder containment, no create/update/delete — the writer stays
    `store/secret_use_log.go`, the single one.
  - RowType mirroring the columns already written (`key`, `scope`,
    `scope_kind`, `tool`, `caller_sub`, `spawn_id`, `ts`), so the doc and the
    handler read one struct.
  - Gate: operator scope on REST. On the agent MCP face, either omit it
    entirely (operator-only forensics, the safer default and my
    recommendation) or bind it to the caller's own folder — never a plain
    `mcp:` grant with the default target resolver.
  - `F35`'s onbod `audit_log` resource is the precedent to copy for the
    read-only shape.
- **Evidence:** table writer/reader sweep over all migrations
  (`INSERT INTO|UPDATE` vs `FROM <table>` vs `Table:` resreg decls) →
  `secret_use_log writers=1 readers=0 resreg=0`. `grep -rn secret_use_log`
  → writer paths + migrate-split copy + docs only.
- **RE-MEASURED at 17459f6a 2026-09-21. The defect is half real and the
  prescribed fix is now the WRONG one — building it would manufacture the
  duplication root `CLAUDE.md` forbids.**

  **The entity still has no reader.** Confirmed, and the source says so itself:
  `store/secret_use_log.go:15` — "NO READ DECIDED IT, because there is no
  reader: nothing in the tree calls this file for anything but the write, on
  any instance." No resreg declaration, no handler, no dashd page, no CLI verb.

  **But the FACT is fully readable, on both faces, and has been.** `LogSecretUse`
  writes the item and an `audit_log` row in ONE batch
  (`store/secret_use_log.go:141-183`), and that row is a COMPLETE projection of
  `SecretUseRow` — all nine fields, nothing dropped:

  | `SecretUseRow` | where it lands in the `audit_log` row |
  | --- | --- |
  | `TS` | the row id / `created_at` |
  | `CallerSub` | `actor` + `actor_sub` |
  | `Key`, `Scope` | `resource` = `secrets/<scope>/<folder>/<key>` |
  | `Scope` | `scope` |
  | `Folder` | `folder` |
  | `Tool`, `SpawnID` | `params_summary` |
  | `Status` | `outcome` + `error_msg` |
  | `LatencyMS` | `duration_ms` |

  `audit_log` IS a registered resreg resource (`resreg/resources/audit.go`),
  mounted by routd (`routd/audit_resource.go`), which is the daemon that owns
  this table. So "who used which secret, from where, with which tool" is already
  answerable two ways, neither needing a shell on the host:

  - REST `GET /v1/audit?category=secret` behind the `audit:read` scope;
  - MCP `query_audit` with `category=secret`, bound to the caller's own folder.

  `action=secret.read` is written by exactly one site in the tree
  (`store/secret_use_log.go:154`), so it isolates secret USES from
  `secret.set` / `secret.delete` cleanly.

  **Therefore the entry's own security question is already answered, and not in
  the direction it assumed.** It asks the operator to decide "may an AGENT read
  which secrets were used, by whom, with which tool?" — an agent holding
  `mcp:query_audit` can read exactly that today, folder-bound. Registering
  `secret_use_log` would not open a new door; it would cut a second door into
  the same room, publishing `/v1/secret_use_log` and a second agent tool over a
  fact `/v1/audit` already serves. That is a second FORMAT for one declaration,
  the rule in root `CLAUDE.md` §"One declaration, many readers", and
  `resreg/resources/audit.go` is emphatic that an append-only log gets one
  read-only face, not several.

  **The recorded direction is the opposite one: retire the entity.**
  `audit/PLAN.md:319-322` already says so — "`secret_use_log` — replaced by
  `secret.read` rows in `audit_log` once the audit table lands (… consolidates
  in round 3 if hot-path volume permits)". The audit table landed. The item is
  the leftover half of a consolidation that stopped at the write.

  **What it costs to leave alone: almost nothing.** The entity is EMPTY on the
  whole fleet — `store/secret_use_log.go:28` records zero rows on all three live
  snapshots at 2026-09-07, because the broker writes only when a container tool
  resolves a secret and the fleet has one secret. A reader would page nothing.

  **Status: the proposal above is WITHDRAWN, not pending.** Do not build it. The
  live question is whether to finish `audit/PLAN.md`'s round 3 and delete the
  entity, leaving `audit_log` as the single record. That is a spec-level
  deletion touching `specs/5/13` §Audit, which mandates the row, so it is a
  sign-off rather than a refinement — but a much smaller and better-shaped one
  than the new wire contract this entry originally asked for. Until it happens
  the duplicate write is harmless: one empty item collection, written in the
  same batch, costing one item per secret read.

## L2 — a delegated child is never dispatched: its only message is a bot row (2026-08-08, CAUSE ISOLATED 2026-08-15; SILENT HALF FIXED 2026-09-20, the fix needs sign-off, REPRODUCED 2026-08-16 on v0.64.0)

Same live run, `child-delegate`. Unlike L1 this one is NOT root-caused, and it
is recorded that way on purpose.

The v0.64.0 anteval run reproduces it unchanged (`no callback for
etjty2l-child-delegate`). The harness prints "case needs `mcp:register_group`,
`mcp:delegate_group`" on any timeout; that hint is generic and wrong here — the
logs below show both calls succeeding.

What is verified from the logs: the parent registered the child
(`"group registered" jid=web:eval/etjgjnv-child-delegate folder=eval/etjgjnv-child-delegate`)
and delegated the task (`"send" folder=eval jid=web:eval/etjgjnv-child-delegate
text="Task: run this exact command and report the result back here"`). So
registration and delegation both work. The child never fired the callback.

**CAUSE ISOLATED 2026-08-15 by the call path.** No probe was needed and no
capability is involved. Agent-to-agent delegation over `send` cannot wake the
target folder, by construction:

1. The parent's `send` reaches `ipc.internalSend` → `gated.SendMessage` →
   `routd.mcpDeliver`, which delivers, and `ipc.putOutboundRow`, which persists
   the row. That writer stamps EVERY agent-authored row `FromMe: true,
   BotMsg: true` (`ipc/ipc.go` putOutboundRow) — there is no separate shape for
   "addressed to another folder".
2. The poll DOES see it: `DB.NewMessages` filters on timestamp only, so the
   child chat is picked up and `resolve` succeeds (the child folder was
   registered), and it is enqueued.
3. `processGroupMessages` then drops it (`routd/dispatch.go:52-62`):

       // Strip bot rows from the trigger batch (don't feed the agent its own
       // output) but keep them in the rendered context.
       for _, m := range msgs { if !m.BotMsg { trigger = append(trigger, m) } }
       if len(trigger) == 0 { l.advance(chatJID, last); return false, nil }

   The child's chat holds nothing BUT bot rows, so `trigger` is empty. The
   cursor advances and the function returns `(false, nil)` — the "silent no-op"
   that is deliberately NOT a failure, so it does not even trip the breaker.
   No container is ever spawned for the child.

Both recorded candidates are answered:
- The `egress` ACL vs `network_rules` question is MOOT. No container starts, so
  no egress decision is ever reached. The two mechanisms never got to disagree.
- "the child may never have been dispatched at all" is CORRECT, and now
  explained: it was enqueued and then dropped at the bot-row strip.

- **Severity:** medium — agent-to-agent delegation silently does nothing.
- **Scope:** message model — what may trigger a turn
- **Affected:** routd (dispatch), ipc (outbound writer)
- **Status:** NOT FIXED — the fix is a redesign and needs sign-off.

**PROPOSAL, for the user, not built.** The bot-row strip is correct and must
stay: it is what stops an agent re-triggering itself on its own output. The
missing distinction is that a row authored by folder A and addressed to folder
B is B's INBOUND, not B's own output. A fix has to say so explicitly, and that
is a new contract on the message model plus a loop-safety rule:

- Make the trigger test `m.BotMsg && m.Sender != <this folder>` rather than
  `m.BotMsg` alone, so a row a DIFFERENT folder wrote can trigger. `Sender` is
  already the author folder at this writer, so no new column is needed.
- Bound it, or two agents ping-pong forever: a hop budget carried on the row, a
  depth cap on the delegation chain, or a rule that only a registered CHILD
  folder may be woken by its parent. This is the part that decides the shape,
  and it is a product call, not a code call.
- Decide whether the woken turn is credentialed as the child (socket folder) —
  it is, since the socket is per-folder — and whether the parent needs an ACL
  action to wake another folder at all.

Until then, delegation should either be refused loudly at the `send` gate when
the target folder is not the caller's own chat, or documented as not supported.
Silently storing a row nobody will ever act on is the worst of the three.

- **The DROP HAS MOVED since this was filed, and it is still silent.** It is no
  longer the bot-row strip: `processGroupMessages` now calls `newestUserIdx`
  first and returns at `steerIdx < 0` — "An all-bot batch routes nothing"
  (`routd/dispatch.go`). Same outcome, one screen earlier. The mechanism this
  entry isolated is unchanged: every agent-authored row is stamped `BotMsg`, so
  a delegated child's chat holds nothing else.
- **The silence is fixed 2026-09-20.** An all-bot batch is normally a folder's
  OWN output, correctly ignored. It is not when another folder wrote it, and
  that case now says so, naming the chat, the author and the row count. The
  distinction is `groupfolder.JidFolder(chatJID)` against the newest row's
  `Sender`: a delegation carries the PARENT's folder, a self-reply carries the
  chat's own.
- **PROVEN BY MUTATION both ways:** drop the warning and the delegation test
  fails; drop the `Sender != author` term and the twin fails, because a folder's
  own output must stay quiet — a notice on every turn a folder takes would be
  worse than silence.
- **Still NOT FIXED, and it is the redesign:** the bot-row strip is correct and
  must stay, because it is what stops an agent re-triggering itself. Making a
  delegation wake its target needs a row shape that is neither "a person spoke"
  nor "my own output" — a message-model change, which is the sign-off this
  entry has been waiting for since August.
- **The notice shipped 2026-09-20 read the chat's folder out of the JID, and on
  two JID shapes that made it lie. FIXED 2026-09-21 (`2abe141f`).** It called
  `groupfolder.JidFolder(chatJID)`, but the grammar is ambiguous by design —
  `web:t/sfx` reads as folder `t/sfx` while its mint recorded `t`, and
  `hook:a/b/c` reads as `a/b`. So on every route-token chat and every `hook:`
  chat the comparison was `t/sfx != t`, and the folder's OWN reply logged
  "delegation woke nothing" once per reply, forever.
  `store.RouteTokenTargetByJID` is "the single reader every folder-from-JID path
  goes through" (its own comment) and `routd/loop.go` `directFolder` is the call
  site that already uses it; the notice now uses it too. The 2026-09-20 mutation
  proof could not catch this because both its tests use a plain `web:<folder>`
  JID, where the grammar happens to be right. Same bug class as `L4`/`L1`, in
  the code written to report `L2`.
  - PROVEN BY MUTATION: with the grammar reader,
    `TestDispatch_ARouteTokenChatsOwnReplyIsNotADelegation` fails with
    `chat_folder=acme/tok wrote_by=acme`; the two 2026-09-20 tests keep passing
    either way, so a real cross-folder delegation still warns and a plain
    folder chat still stays quiet.
- **The warning is NOT enough to act on, and that is the bounded half of the
  sign-off.** A `slog.Warn` in routd reaches an operator reading the census. It
  does not reach the agent that called `send`, which still gets a success and
  then waits for a callback that cannot come, and it does not reach the person
  who asked for the delegation. `CLAUDE.md` §Fail loud requires an error on a
  user-facing path to surface to the caller, not only to a log — and the entry
  already named this option ("refused loudly at the `send` gate"). It is one
  question, not a redesign: **should `send` refuse when the target chat's folder
  is not the caller's own, given that posting a notification into another
  folder's channel is a legitimate use of the same call?** The message-model
  change (a row that is neither "a person spoke" nor "my own output", plus the
  hop budget) stays the separate, larger sign-off; nothing here touches it.

## F75 — three skill trees are triplicated and one pair has already drifted (2026-08-08, partly fixed 2026-09-21 — live drift closed, de-triplication still a redesign)

`ant/skills/p5js/` also exists under `ant/skills/create/art/.../p5js/` (same for
excalidraw and manim-video). A `diff` shows the top-level
`p5js/scripts/export-frames.js` carries a chromium `executablePath` fix the
`create/`-tree copy lacks — so the duplicate is not merely redundant, it is
already stale. Deciding which is canonical for skill dispatch is the fix.

**Verified 2026-08-15, premise still holds.** All three pairs are real and
each nested copy carries a `README.md` where the top-level carries `SKILL.md`
(dispatchable skill vs. reference resource read by the `create` router):
`ant/skills/p5js` ↔ `ant/skills/create/art/p5js`,
`ant/skills/excalidraw` ↔ `ant/skills/create/diagram/excalidraw`,
`ant/skills/manim-video` ↔ `ant/skills/create/video/manim`. The `p5js` pair's
`scripts/export-frames.js` still differs — the top-level tree has the
chromium `executablePath` fix, the `create/`-tree copy does not.
NOT fixed here: syncing the stale script would patch the symptom (this
instance of drift) while leaving the duplication mechanism that produced it
in place — the same class root `CLAUDE.md` calls out under "No duplication —
amend the original." The actual fix is the canonicalization decision the
entry already names (which tree is the single source, and whether the other
becomes a symlink/reference rather than a second copy), which is a
cross-cutting structural change to skill dispatch and wants sign-off.

**Re-verified 2026-09-21 at `a388cad7` — all three pairs stand, the drift is
still the same four lines, and one thing the earlier verifications left implicit
is worth stating, because it is what makes the drift bite.** Both trees are
LIVE and documented as such: `ant/skills/create/CLAUDE.md` lists the nested
copies as the create router's own subtree (`art/p5js.md` + `art/p5js/`,
`diagram/excalidraw.md` + `diagram/excalidraw/`, `video/manim.md` +
`video/manim/`), and `ant/skills/create/SKILL.md:30,32,26` dispatches into them.
Nothing outside those trees references the nested paths, so neither copy is dead
and neither can simply be deleted.

The drift is not cosmetic. The top-level `ant/skills/p5js/scripts/export-frames.js`
carries four lines the `create/` copy lacks:

```js
// Use the image's apt-installed chromium so we don't download
// puppeteer's bundled build (needs egress, often absent in-container).
executablePath:
  process.env.PUPPETEER_EXECUTABLE_PATH || '/usr/bin/chromium',
```

So the copy an agent reaches through `/create` is the one that tries to download
a chromium the container's egress allowlist will not let through. That is the
live cost, and it is the argument for deciding rather than deferring again.

**Live drift fixed 2026-09-21; de-triplication still a redesign.** The four
lines are carried into `ant/skills/create/art/p5js/scripts/export-frames.js`,
so both copies pin the image's chromium and the `/create` export path no
longer attempts a download. The egress half of the claim checks out: the base
allowlist every folder inherits is `{anthropic.com, api.anthropic.com}`
(`store/seed.go:86`) and `egress.New` allows "exactly allow minus deny", so
puppeteer's download host was refused by default deny, not merely unlisted.
`ant/Dockerfile:25` apt-installs the chromium the fix points at.

Sharpened while fixing:

- Only the `p5js` pair ever drifted in content. The `excalidraw` and `manim`
  pairs differ solely by the dispatch marker — `SKILL.md` top-level vs
  `README.md` nested — which is the intended difference, not drift.
- The mechanism is a re-vendor, not an edit slip. The top-level fix landed in
  `ec15fefb` as a local repair; `9769d1ec` later vendored the create router
  fresh from `kronael/tools`, reintroducing the unfixed upstream copy. Any
  future `sync-tools-skills` run splits the pair again the same way.
- `ant/src/skill-tree-parity.test.ts` now holds all three pairs byte-equal
  (bar the marker) and names the chromium line specifically, so a re-vendor
  fails the suite instead of shipping silently. That guard makes the redesign
  safe to defer; it does not replace it.

**Still the owner's one-sentence decision:** which tree is canonical for each
pair, and does the other become a reference rather than a second copy? What
reads each copy today: the top-level tree is reached by skill dispatch through
its `SKILL.md`; the nested tree is reached only by `create/SKILL.md:26,30,32`
via the `<mode>/<slug>.md` router page. Nothing else references either. The
one-declaration shape, for when the question is answered: keep the top-level
tree as the single source, and reduce each nested copy to its
`<mode>/<slug>.md` router page alone, its body pointing at the top-level tree
instead of a sibling subtree. That removes three duplicate subtrees and leaves
one reader per declaration. It wants sign-off because it changes how `create`
resolves subtree-relative paths, an invariant `ant/skills/create/CLAUDE.md`
states explicitly.

**Ships with the next release** — editing `ant/` rides a `MIGRATION_VERSION`
bump plus an agent image rebuild. Neither is done here, deliberately.

## F41 — the re-greet cooldown is a rate, not a cap (2026-08-06, open question — NOT a spec defect; sharpened to a three-way choice with costs 2026-09-21, UNANSWERED by design)

Shipped with the `5/31` fold and named in the spec rather than hidden. A chat
that (a) keeps messaging, (b) still routes nowhere, and (c) never redeems its
pairing link is offered a fresh one every `store.PairingTTL` (10 min) — 144/day
in the worst case. Before the fold it was greeted exactly once, ever, which was
the permanent lockout `P1b` blocker 2 existed to remove; the trade is a live
link the user can always get for a message the user cannot switch off.

Two bounds already hold and are what make this a rate rather than a flood: the
re-arm sits in `store.InsertOnboarding`, which only routd's route-MISS branch
calls, so a since-routed chat and a silent chat are BOTH never greeted again
(this is what stopped the timer-only design from re-greeting three live krons
group chats every ten minutes — see `5/31`).

Open question, not a defect with an obvious fix: should the Nth greeting back
off? A `prompt_count` column and a widening interval is the obvious shape, and
it is a new column plus a policy constant for a case nobody has hit yet. Left
as a rate until an instance reports it.

**Reclassified 2026-08-11 — this never made `5/31` defected.** A spec is
`defected` when an open bug CONTRADICTS its guarantee. `5/31` names the rate in
its own text and shipped it deliberately, so the spec is accurate and the code
matches it; what is open is a future policy choice, which is a backlog item.
`5/31` returns to `shipped` and keeps this entry as the recorded question.

**Re-verified 2026-09-21 at `a388cad7` — the mechanism survived the DynamoDB
move intact, and the question is still unanswered because nothing has asked
it.** `store.InsertOnboarding` (`store/onboarding.go:138`) is now two conditional
writes rather than SQL, but it re-arms on exactly the two bounds the entry
credits: a create-if-absent (`Cond: ItemAbsent`), and on refusal an update that
removes `prompted_at` only when `status = awaiting_message` AND
`prompted_at < now - PairingTTL` (`store/onboarding.go:169`). The caller is still
routd's route-MISS branch, so a routed chat and a silent chat are both never
re-greeted. There is no `prompt_count` column anywhere in the tree.

Still an open POLICY question, not a defect: should the Nth greeting back off?
**The owner's decision is one sentence: leave the re-greet a flat rate, or add a
`prompt_count` attribute and widen the interval after N.** Two months on, no
instance has reported the 144/day worst case, which is the evidence the entry
said to wait for.

**RE-VERIFIED 2026-09-21 at `17459f6a` (`a388cad7` is its ancestor) — mechanism
unchanged, `prompt_count` still absent from the whole tree. Two bounds the entry
does not credit, and a THIRD option cheaper than the one it names.**

- **The mechanism is three conditional writes and NO read, which is the thing a
  back-off would spend.** Absence of `prompted_at` IS "due a greeting":
  `InsertOnboarding` re-arms by REMOVING the stamp once it is older than
  `PairingTTL` (`store/onboarding.go:163-171`), `UnpromptedJIDs` lists rows
  where it is Absent (`:560`), and `ClaimOnboardingPrompt` sets it under
  `Absent` — "the condition IS the claim: two overlapping poll ticks cannot both
  win one jid" (`:651-668`). No path reads the row to decide.
- **Two more bounds.** routd's route-miss call is gated by
  `onboardingAllowed(chatJID, l.onboardingPlatforms)`, and a Discord GUILD
  channel additionally needs `Verb == "mention"` (`routd/loop.go:696-699`) — so
  the 144/day worst case needs 144 explicit @mentions there, not 144 messages.
  `InsertOnboarding` has a second caller the entry does not name, the REST
  `create` on `/v1/onboarding` (`onbod/onboarding_resource.go:79`); it is
  operator/agent-invoked and drives no automatic rate.

- **Three options, not two.** The entry's title already says "a rate, not a
  cap", and the cap is the cheap one it does not cost out:

  1. **Flat rate (as is).** No code, no attribute. Worst case 144 greetings/day
     to one chat that keeps talking, never routes and never redeems; the user
     cannot switch it off. The failure it prevents — the permanent lockout
     `P1b` blocker 2 removed — is PROVEN; the flood it permits is still
     hypothetical after ~7 weeks.
  2. **Hard cap after N.** One `prompt_count` attribute, incremented at claim
     time (the `incrementEntityVia` primitive already exists), plus ONE more
     term on the re-arm condition: `prompt_count < N`. A comparison against a
     literal, so it stays a single conditional write with no read and the
     race-freedom above survives intact. Costs the user who hits N a silent
     dead end — the entry's original lockout, deferred rather than removed.
  3. **Widening interval after N.** Same attribute, but the re-arm condition
     becomes `prompted_at < now - interval(count)`, and the comparison value is
     computed by the CALLER. `InsertOnboarding` does not know `count`, so this
     one buys a read-modify-write where there is now a single conditional
     write. That is the real price, and it is invisible from "add a column".

  **So the sentence to answer is: flat rate, hard cap at N, or widening
  interval — knowing the cap costs one condition term and the widening costs the
  lock-free write.** Unanswered here on purpose: it is a policy choice, and
  nothing has yet asked it.

## M4 — `vited` runs the Vite DEV server in production (2026-07-16, crash class fixed 2026-07-18; the COST premise is REFUTED and the entry is re-scoped to posture 2026-09-21 — needs sign-off)

**Crash class fixed (2026-07-18):** the dev server's dep-optimization cache
defaulted into the bind-mounted `/web` root, where host uid ownership drifted to
root and `vite` (USER node) hit `EACCES: unlink …/.vite/deps/_metadata.json` on a
config change → crash-loop → `/pub` 502 (marinade + sloth, twice). Fixed by moving
`cacheDir` out of the mount to a container-local path (`ant/vite.config.js`) — the
cache is now container-owned + ephemeral, so it can't recur. Immediate incident
patched by chowning the stale `/web/**/.vite` caches back to the container uid.

**Residual (still open):** vited still runs `vite dev` (poll-watch + transform)
rather than serving the static `/pub`+`/priv` tree.

**Re-measured 2026-09-21 at `e7f1bac3`. The dev server is still there; the
~1-core cost is not.** `ant/Dockerfile.vite:15` runs bare `vite`, so the premise
that vited runs the dev server HOLDS. The cost that justified acting on it does
not. Cumulative CPU over each container's whole life, which is a fairer number
than an instantaneous sample:

- `arizuko_vited_krons` — 2212s CPU over 3h55m uptime = **0.157 cores**
- `arizuko_vited_sloth` — 13.9s CPU over 1h02m uptime = **0.004 cores**

Both containers run the image built 2026-09-18, which carries the scoped
watcher (`unwatched` is in `/etc/vite.config.js` inside the running container),
so this measures the state the entry calls "~1 core even with the scoped
watcher". It is 6x lower than that on krons and ~250x lower on a clean instance.
krons is the outlier because of the 16k-file tree an agent published under
`pub/`, which is the watcher's input, not the dev server's floor.

**`vite preview` is not an option, and the entry should stop naming it.**
`preview` serves a BUILT `dist/`, and `template/web/package.json` has one script,
`dev`. There is no build, and there cannot usefully be one: `/web` is a
read-only bind mount whose contents agents in other containers write at runtime
(that is why the watcher polls at all). A build artifact is stale the moment an
agent publishes a page. That leaves "a plain file server" as the only real
end-state.

**Which is not bounded.** The two plugins that make the site work,
`pubFallback` and `trailingSlash`, hang off `configureServer` — a dev-server
hook. A static server has to re-implement the `/pub` rewrite, the directory 301,
the trailing-slash index resolution and the `fs.deny` list for `.env`/`*.db`/
`.git`, and then two healthchecks change (`ant/Dockerfile.vite`'s `/` probe and
`compose/compose.go:1350`'s `/@vite/client` probe) plus `vited/README.md`,
`template/web/pub/arizuko/components/vited.html`, `ARCHITECTURE.md` and
`SECURITY.md`. Nothing in the tree tests that image, `make vite-image` needs
docker on a 95%-full host, and this exact component has already taken `/pub` to
a 502 crash-loop twice. An unverifiable rewrite of the operator web origin to
recover 0.15 of a core on one instance is a bad trade.

**What survives is a posture argument, and it is measurable.** Every public page
krons serves carries an injected dev client:

    $ docker exec arizuko_vited_krons wget -qO- http://127.0.0.1:8080/pub/arizuko/ | grep -o @vite/client
    @vite/client
    $ docker exec arizuko_vited_krons wget -S -qO- http://127.0.0.1:8080/@vite/client
    HTTP/1.1 200 OK
    Content-Type: text/javascript

So the public operator site ships Vite's HMR client to every visitor and answers
for it, and the healthcheck depends on that answer. That is a different claim
from the one this entry was filed on, and it is the one worth deciding.

- **Severity:** low (was medium on the cost claim, which no longer holds)
- **Status:** the cost premise is refuted; the posture question is open
- **The owner's decision is one sentence:** is shipping Vite's dev client and
  its HMR endpoint on the public `/pub` site acceptable, given that removing it
  means hand-writing the `/pub` rewrite, directory 301 and deny-list that
  `configureServer` currently provides, on a component with no test and two
  prior `/pub` outages?

## Codex mount assumes creds readable by the container uid — 0600 operator creds break codex-in-container (2026-07-12, unblocked live; SILENT HALF FIXED 2026-09-21, the copy is still a proposal)

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
  Still unsigned, and it widens exposure in a way the entry does not price: a
  copy chowned to the run uid is WRITABLE by the agent and outlives the
  container in the group folder, where the RO mount is neither.

- **RE-MEASURED at `fdb59418` 2026-09-21. The mount is there and the failure
  mode is real.** `container/runner.go` mounts the group's `.codex` and then
  RO-overmounts each `CodexROMounts` entry from `cfg.HostCodexDir`; the
  overmount carries the HOST file's owner and mode, and the agent's uid is 1000
  unless runed runs as some third uid. Nothing between the operator's `chmod`
  wearing off and the agent answering "not logged in" said a word.

- **The silence is fixed 2026-09-21. The permission is NOT, and must not be.**
  `unreadableCodexCreds` (`container/runtime.go`) names each shared credential
  the agent's uid certainly cannot open, and `buildMounts` logs it at ERROR
  with the path, the uid and the symptom the operator will otherwise chase.
  Two options were refused, each for its own reason:
  - **Repairing the mode** would have arizuko `chmod` an operator's own
    `~/.codex`. `CLAUDE.md`: operator data fixes belong to the operator, the
    platform stays mechanical.
  - **Refusing the spawn** would break every turn that never runs codex. codex
    is one optional backend; a hard gate on its credential is the wrong blast
    radius, so the log is the whole surfacing and the mount is unchanged — the
    test asserts the credential still reaches the mount list.
  Only the CERTAIN case is named: a file the uid owns and can read, or one with
  the world-read bit, is reachable; a group-readable one may be reachable
  through a supplementary group runed cannot enumerate, so it is not accused.
  A wrong accusation trains the operator to skip the line that matters.

- **Also collapsed: `containerRunUID`.** `buildArgs` decided the agent's
  in-container uid with a bare `uid != 1000` beside the `containerUID`
  constant; the readability check needs the same answer, so the rule is one
  function with two readers rather than a second copy that drifts.

- **PROVEN BY MUTATION:** drop the log site and
  `TestBuildMountsSaysWhenCodexCredsAreUnreachable` fails on both credentials
  and on the missing symptom; the 0640 case in
  `TestUnreadableCodexCredsNamesOnlyTheCertainCase` fails the moment the check
  widens to accuse a group-readable file.

## RECORD — 5/16 invites fold: the agent invite tools stay hand-authored (2026-07-07, re-verified 2026-09-21 — not a defect)

**This is a record of a deferred fold, not a bug.** It sat in the open queue
reading like a defect; nothing here is broken, and the duplication it names is
priced and guarded. Re-verified line by line at `9229a97b`.

onbod's `/v1/invites` REST face rides resreg (`154cd17f`). The three AGENT tools
(`invite_create`/`invite_list`/`invite_revoke`) do not: they are hand-written
closures in `routd/mcp.go:130-179` forwarding to onbod over HTTP. All three
reasons stand, unchanged:

- **caller-bound issuer** — `ipc/ipc.go:2062` passes `"agent:"+folder`; never a
  client argument.
- **accept_url synthesis** — `ipc/ipc.go:2080` builds
  `<AcceptURLBase>/invite/<token>`.
- **revoke ownership as list-then-confirm** — `routd/mcp.go:160-179` lists THIS
  folder's invites and refuses a ref it did not issue, because onbod's DELETE
  takes a ref and nothing else (`onbod/invites_resource.go:90-103`, "ref IS the
  coordinate"). A naive forwarded DELETE regresses that into a cross-folder
  revoke.

The gate is live, not aspirational: neutering the `!found` refusal in
`routd/mcp.go` fails `TestBuildGatedFns_RevokeInvite_OwnershipGate`
("RevokeInvite(cross-folder) should be rejected"), measured 2026-09-21. All
three cases in `routd/invite_mcp_test.go` pass.

**Why this is not the `5/17` fold CLAUDE.md asks for.** `resources.InvitesRow`
declares no `x-mcp-*` action, so there is no derived MCP face for invites to
drift from — the hand-authored tools are the only agent face, not a second one
beside a generated twin. And `onbod/invites_resource.go` says it in its own
header: "onbod has no agent socket, so this is REST-only — there is no MCP twin
to share the handler with." routd cannot mount onbod's handler in-process. So
folding is not a resreg registration; it is a change to onbod's public DELETE
contract plus a rewrite of routd's closures into forwarders.

**The decision, whenever 5/16 next moves:** give onbod's DELETE an `issued_by`
argument so ownership is enforced in one call and the agent face becomes a
forwarder that injects the issuer and synthesizes `accept_url` — or leave the
agent invite tools hand-authored for good. Either is defensible; neither is
urgent. Kept here as the written reason the duplication exists, so the next
audit does not re-derive it.

- **Severity:** none (no live defect; duplication is guarded and tested)
- **Scope:** `routd/mcp.go`, `ipc/ipc.go`, onbod's invites resource
- **Source:** routd/mcp.go:130-179; ipc/ipc.go:2062,2080;
  onbod/invites_resource.go:8,90-103; routd/invite_mcp_test.go
- **Status:** record — deferred by design, no sign-off pending
- **Fix:** none needed

## dashd's cross-owner reads — two survive spec 6/28's read rule (2026-06-16, one of two fixed 2026-09-22)

**The conflict this entry tracked is resolved, and not by code.** It read: the
renamed spec (`7/1` → `specs/6/28-cockpit-index.md`) forbids the direct DB read,
the code and root `CLAUDE.md` require it, someone must pick. `6/28` is
`status: shipped` and now states both halves itself. Its §"Read-path: `/v1`
only, no direct DB" carves the `secrets` exception on a named test — "would the
owner serve this if asked", and routd's secrets resource refuses a read face on
purpose (`routd/secrets_resource.go`) — and its §"WRITES are governed by
CLAUDE.md" quotes §"Split write-discipline" verbatim. Nothing is left to sign
off.

**The SQLite half is subsystem-gone.** `database/sql` is imported by exactly one
file in the tree (`audit/audit_log_test.go`) and no SQLite driver is linked at
all. `dbRoutd`, `dbOnbod` and `dbRuned` do not exist: dashd holds an
`objstore.Store` over routd's owner partition and reads it through
`dashd/dashstore` (`6/37` step 1). The `runed.db` read the entry named is gone
outright — `openSiblingOwner` is called once, for `store.OwnerOnbod`
(`dashd/main.go:263`).

**Most of the pages it named already moved.** Routes, tasks and groups render
from routd's `/v1` over the service bearer (`routes_admin.go:379`,
`tasks_admin.go:310,330`, `groups_admin.go:451`); their surviving `dashstore`
calls are the write path and its pre-write lookups, which CLAUDE.md governs.
`/dash/audit/` fans out to each owner's `/v1/audit`, and `route_tokens.go:37`
cites the rule in its own comment.

**What survives: two cross-owner READS.** Neither is the spec's `secrets`
exception, and neither needs a decision — `6/28` already prescribes the remedy.

1. `dashd/usage_page.go:148` → `dashstore.MessageActivityByFolder`, reading
   routd's `message_folder_counts` entity and each folder's feed head. The cost
   half of the same page already goes through `GET /v1/cost/summary`
   (`usage_page.go:264`); the message half has no endpoint, so this is 6/28's
   "failing that, add a minimal daemon-owned read endpoint".
2. `dashd/invites.go:34` → `onbodStore().ListInvites("")`. The page's WRITES are
   CLAUDE.md's FS-mounted discipline; the LIST is not, and onbod already serves
   `GET /v1/invites` on resreg (`onbod/invites_resource.go`), so this one is a
   swap, not an addition.

Severity drops from high to low, scope from "all dashd pages" to these two.

- **Severity:** low
- **Scope:** `dashd/usage_page.go`, `dashd/invites.go`
- **Affected:** `/dash/usage/`, `/dash/invites/`
- **Source:** dashd/usage_page.go:148; dashd/invites.go:34;
  specs/6/28-cockpit-index.md:33-71; dashd/main.go:263
- **Status:** item 2 FIXED 2026-09-22, item 1 open.

  **Item 2 (invites) — done.** `dashd/invites.go` reads
  `GET /v1/invites` over the service bearer, the way the audit page already
  reads onbod. The page's WRITES stay on the FS-mounted discipline. onbod's
  `inviteJSON` is in its own package main, so nothing binds the two shapes and
  a renamed field would decode as a blank with no error — the page would render
  empty refs and report nothing. `TestInviteRowMatchesOnbodsWireShape` reads
  onbod's declaration with `go/parser` and demands the same tag set; renaming
  one tag fails it. `stubOnbod` in the test file serves the same shape, because
  onbod's real resource cannot be mounted from another main.

  **Item 1 (usage page) — open.** It needs routd to grow the read first:
  `dashstore.MessageActivityByFolder` has no `/v1` twin, so this is 6/28's
  "failing that, add a minimal daemon-owned read endpoint" — a new endpoint on
  routd, which is an addition to a public surface rather than a swap.

## Adversarial review of the 5/16+surrogate features — the surviving findings (2026-07-30, re-adjudicated 2026-09-21)

Fixed inline at filing time: F3, F4 (`cc11eace`), F8 (`48c7e5ce`), F9
(`25e0d91c`), F16 (`1f66d77f`).

Closed since, with evidence:

- **F1 (role:operator grant_option seeding) — dead.** `cmd/arizuko/migrate_split.go`
  does not exist and no numbered SQL migration does either, so the copy-then-UPDATE
  race cannot occur. One write site remains: `store/seed.go` seeds `role:operator`
  `(*, **)` WITH GRANT OPTION when the ACL entity is empty (`RoleOperator`,
  `store/seed.go:39-42`). The item's own caveat is false too — `auth.Delegate`
  is wired, called at `routd/acl_resource.go:182`.
- **F7 (`DELETE /v1/route_tokens/{jid}` 404s on a slash-bearing jid) — FIXED
  2026-09-21 (`debe58f4`).** It was live: a hook jid is `hook:<folder>/<label>`, so
  every token `POST /v1/route_tokens/hook` mints was unrevokable over REST. The
  single-segment wildcard is now `{jid...}`, the catch-all `proxyd_routes` and
  `github_sources` already use. Guard:
  `TestFeature_RouteTokens/http-revoke-slash-bearing-hook-jid`.
- **F10, first half (misconfig laundered as transient) — FIXED 2026-09-21
  (`416c4cb2`).** `resolve()` now returns `surrogate.ErrNotConfigured` for
  "unknown provider" / "no client credentials", before any network call, and
  `refreshOAuth` branches on it and logs at ERROR. The stale token is still
  kept: nulling the row would force every user of that provider to reconnect
  over one operator mistake, which is the decision F9 recorded for
  `invalid_client`. Guards: `TestResolve_MisconfigurationIsNotTransient`,
  `TestBroker_MisconfiguredProviderIsErrorNotTransient`. Note the honest limit —
  the entry asked for a permanent fault to "stop being retried forever", and no
  retry loop exists to stop: the attempt is once per row per tool call and never
  reaches the network. What was wrong was the CLASSIFICATION, and that is what
  changed.
- **F11, second half — FIXED 2026-09-21 (`fcd203c3`); the filed wording was
  wrong.** `Provider` carries no secret field, so no TOML "inherits the
  built-in's client secret" — the pairing is by NAME, in the environment
  (`SURROGATE_<NAME>_CLIENT_ID/_SECRET`), while a datadir `<name>.toml` replaces
  the embedded struct whole. `loadRegistry` now reports which built-ins an
  operator file MOVED (`auth_url`/`token_url` changed) and `NewEngine` warns for
  those carrying creds. A replacement that leaves the endpoints alone stays
  silent — a warning nobody can act on trains operators to skip the one that
  matters. Guard: `TestRegistry_RedirectedCredentialedBuiltinWarns` (4 cases).
- **F13 (unvalidated `secret_key` collisions) — FIXED 2026-09-21
  (`8d493784`).** `loadRegistry` now rejects two providers writing one
  `secret_key`, naming both. **The deployment risk was taken deliberately**: a
  colliding registry fails to load, which today means routd and dashd exit
  (F11's first half, below, still unresolved). Reasons on the record — it is the
  same class as the existing required-field error and `specs/5/15` already rules
  a half-defined provider a hard boot error; dropping one of the pair would pick
  a winner arbitrarily and leave a provider whose Connect button writes into
  another's row; and the blast radius is a datadir that ALREADY corrupts
  credentials silently, since the embedded pair cannot collide
  (`TestRegistry_EmbeddedSecretKeysUnique` pins that, but only since
  `c2aa8901`: the check first sat after the operator overlay, behind two early
  returns that most deployments take, so it ran nowhere without a datadir TOML.
  The overlay moved into `overlayOperator` and `loadRegistry` has one tail).
  Guard: `TestRegistry_DuplicateSecretKeyErrors`.
- **F14 (a de-registered provider leaves a live, unlistable, unrevocable token)
  — FIXED 2026-09-21 (`9ef99cb1`).** `listConnections` lists the union of
  registry + stored providers inside itself (no third reader), and
  `handleMeConnectionDelete` resolves the stored row it was already reading for
  the revoke and deletes by THAT row's key — the registry lookup is gone, not
  made conditional. Two behaviours fell out, both wanted: a stored-row read
  failure now 500s instead of being swallowed by an `err == nil` guard, and
  disconnecting a provider with no OAuth row is a 404 that no longer deletes a
  hand-pasted PAT sitting at that provider's `secret_key` (the old path did —
  proven by reverting). Guards:
  `TestConnections_DeregisteredProviderStaysListableAndDeletable`,
  `TestConnections_DeleteWithoutRowKeepsPastedPAT`. Still true and correct: the
  upstream revoke for a de-registered provider warns, because there is no
  endpoint left to call — the local row is gone but the token may live on at the
  provider.
- **F10, second half ("near-expiry token dropped, not stale-used") — wrong
  premise.** `routd/sibling_db.go` `connectorSecrets` pre-populates `out[k]` from
  the stored value BEFORE calling `refreshOAuth`, and the transient-failure branch
  `continue`s without deleting it. The stale value is served, not dropped.

Still live:

- **F5 (REST faces open when `verify == nil`) — by design, no action.**
  `routd/groups_http.go:25` and ~12 sibling gates return nil when `s.verify` is
  nil (local dev). Inert in production because `compose/compose.go` always writes
  `AUTHD_URL` — an operational guarantee, not a code-enforced one. Kept as a
  standing decision record; changing it is an auth-model change.
- **F6 (`groupsRESTAuthz` scope mismatch) — real, REDESIGN, and this entry's own
  fix pointer was wrong.** `routd/groups_http.go:23-30` gates on `resource:verb`
  scopes, but a human JWT's scopes come from `auth.UserScopes`
  (`auth/authorize.go:239`), which returns raw ACL-row folder globs (`acme/eng`,
  `**`). `hasAnyScope` (`routd/server.go:371-379`) matches neither, so a human
  session token can never pass; only a service/CLI token can. `210b8012` fixed a
  different, narrower half (it added the `ownsFolder` subtree filter at
  `routd/groups_resource.go:67`) and never touched the gate. The entry said
  "resolve at 5/33" — wrong: 5/33 governs ACL rows and shipped without touching
  this. The model to reconcile against is 5/1 / `auth/scope.go`, and the fix spans
  authd's mint side and routd's gate side.
- **F11, first half (`os.Exit(1)` on a malformed operator TOML) — real, but the
  failure mode needs a decision.** `routd/cmd/routd/main.go` and `dashd/main.go`
  exit if `surrogate.NewEngine(dataDir)` errors, so one bad optional-feature file
  takes down all routing — even though downstream code already handles
  `surrogate == nil`. Log-and-continue is one line per call site, but it trades a
  loud crash for a silently-absent feature, which is the fail-loud rule cutting
  both ways. Sign-off, not a mechanical fix.
- **F12 (`Provider.AllowedDomain` parsed, never enforced) — real, REDESIGN.**
  `AllowedDomain` is populated in both shipped TOMLs and read nowhere in
  `engine.go`; it is the only occurrence of that identifier in the Go tree. Egress
  is actually enforced by a different system (`routd/network_rules_resource.go` +
  `egress/match`, folder-scoped host allowlists). Giving the field teeth means a
  new contract joining provider config to that egress point. Until then it is a
  false guarantee in a config file — deleting the field is the other honest option.
  **Said out loud 2026-09-22.** The decision waits, but an operator reading
  `providers/*.toml` should not be relying on the line meanwhile. The struct
  field and both shipped TOMLs now state that nothing reads it and that egress
  is bounded by the folder's host allowlist. No behaviour changed and neither
  option is pre-empted; the config just stopped implying a bound it does not
  place.

- **Severity:** medium (F6 blocks a documented REST face for humans; F12 is a
  false security guarantee)
- **Scope:** `routd/groups_http.go`, `auth/surrogate/registry.go` (`AllowedDomain`)
- **Status:** open — the four left for the owner are F5 (a standing decision, no
  action), F6 and F12 (both redesigns needing sign-off), and F11's first half
  (`os.Exit(1)` on a malformed operator TOML — a decision, not a mechanical fix,
  and F13 now rides on it). The four bounded ones are fixed.
- **Fix:** F10a `416c4cb2`, F11b `fcd203c3`, F13 `8d493784`+`c2aa8901`, F14
  `9ef99cb1`, F7
  `debe58f4` — all 2026-09-21; F1 and F10b closed as dead / wrong premise

## ✅ FIXED 2026-10-08 S1b — the helm chart deploys the deleted `gated` daemon (2026-08-05)

Split out of S1, whose fix ("wire `AUTHD_SERVICE_KEY` instead of the dead
`CHANNEL_SECRET`") cannot be applied to `deploy/helm` as a config change. The
chart is pre-split and cannot deploy arizuko at all:

- `templates/gated.yaml` runs `command: ["gated"]`. `gated` was deleted in
  v0.50.0 (`45e4ab0d` + `24d57a3e`); it is not in the Makefile's `DAEMONS` and
  there is no `gated/` directory.
- `_helpers.tpl:89-97` points every adapter's `ROUTER_URL` at
  `<release>-gated:8080`, so the whole chart's routing target does not exist.
- There is no `authd`, `routd`, or `runed` template, and **zero** occurrences of
  `AUTHD_URL` / `AUTHD_SERVICE_KEY` / `AUTHD_SERVICE_NAME` anywhere in the chart.

That last point is why this is not S1's one-line swap. Wiring
`AUTHD_SERVICE_KEY` into `adapters.yaml` would inject a credential with no
`authd` to exchange it at and no `routd` to present it to — every adapter would
still `os.Exit(1)` on the required-env check (`chanlib/run.go:51-55`), but the
chart would now *look* current. That is worse than the honest breakage.

- **Severity:** medium (k8s install path is dead; no runtime effect on the three
  compose instances)
- **Scope:** deploy/helm
- **Affected:** anyone installing via helm
- **Source:** deploy/helm/arizuko/templates/gated.yaml:57;
  deploy/helm/arizuko/templates/_helpers.tpl:89-97;
  deploy/helm/arizuko/templates/dashd.yaml:56;
  deploy/helm/arizuko/templates/adapters.yaml:64-65
- **Status:** PROPOSED — redesign, needs sign-off. Do not half-wire it.

- **RE-MEASURED at 17459f6a 2026-09-21. The defect is real and BIGGER than
  filed, two of the entry's own claims are stale, and the one user-facing half
  is now fixed.**

  **Fixed in this pass (bounded):** `howto/index.html:66` was the only thing in
  the repo that sent a reader to this chart — "For Kubernetes, see the Helm
  chart under `deploy/helm/arizuko/`". It now points at `kubernetes.html`, the
  Kubernetes howto that sits beside it. Nothing else references the chart; its
  own `README.md` is the sole remaining mention. The dead install path is no
  longer advertised, and the chart's templates were NOT touched — half-wiring
  them is still refused.

  Its `README.md`, the one remaining mention, now opens by stating that the
  chart does not deploy arizuko and pointing at the Kubernetes howto, so a
  reader who reaches it directly is not told a dead path is a supported one.
  That is the opposite of half-wiring: the chart still does not work, and now
  says so instead of describing a `gated` monolith as current.

  **REFUTED — the `CHANNEL_SECRET` half already shipped.** `46c4e69e`
  ("fix(helm): drop CHANNEL_SECRET and PROXYD_HMAC_SECRET from the chart")
  removed it. `grep -rn "CHANNEL_SECRET\|channelSecret" deploy/helm/` answers
  nothing, so this entry's cited `values.yaml:119` and `secret.yaml:13` and its
  prescription to "drop `secrets.channelSecret`" all name something that is
  already gone.

  **REFUTED — the page and the chart do NOT have to move together.** This entry
  scoped in `template/web/pub/arizuko/howto/kubernetes.html` as "left at
  `CHANNEL_SECRET` on purpose". It carries zero occurrences of `CHANNEL_SECRET`,
  and it is CURRENT: it names `routd` 35 times, `runed` 30, `authd` 6 and
  `extenddb` 5. Its single `gated` hit is `<data>/ipc/<folder>/gated.sock`,
  which is still the live socket filename (`groupfolder/folder.go:189`) and
  therefore correct. The page documents hand-written manifests, not this chart —
  its only `helm` lines install third-party `external-secrets`. The page moved
  with the split and with the store; the chart is the orphan that did not.

  **Bigger than filed — the storage model changed too, and the chart missed
  that as well.** Beyond the three missing core daemons:
  - **The whole persistence layer is absent.** Every owner table now lives on
    the `extenddb` store server reached over TLS (`template/services/extenddb.yml`,
    spec 6/42) — "no daemon opens a file". The chart has no `extenddb` or
    `extenddb_db` object and none of the env that addresses them. It instead
    provisions an RWO PVC whose stated job is "a live SQLite WAL"
    (`gated.yaml:17`, `values.yaml:32,71`) that nothing opens any more.
  - `dashd.yaml:56` sets `DB_PATH=/srv/app/home/store/routd/routd.db`. Those
    files are frozen leftovers that answer a lie.
  - `SECRETS_KEY` is absent from `configmap.yaml` and `secret.yaml`; routd
    refuses to start without it.
  - Four adapters shipped since are missing from `values.yaml`: `gitd`, `ttsd`,
    `twitd`, `whapd`.
  - `Chart.yaml` `appVersion: '0.49.0'` against a shipped `v0.69.0`.

  **NOT stale, checked and kept:** `davd` and `vited` are both still live
  (`compose/compose.go:688` and `compose/compose.go:1339`), so their templates
  are not a second `gated`. A word-boundary grep says otherwise and is wrong —
  the live symbol is `vitedService`.

  **Root cause, and why it will recur: the chart is unowned and ungated.** Five
  commits in its whole history, the last substantive one the starter commit
  `388f73b4`. `helm` is not installed on the build host and `.github/workflows/ci.yml`
  never renders the chart, so no gate could have caught any of the above — it
  drifted through the daemon split and the DynamoDB move in silence, and any
  rewrite drifts again by the next release unless a render gate lands with it.

  **Recommendation: delete `deploy/helm/`.** The rewrite is not a chart edit but
  a new chart — three core deployments, a store server with TLS and a minted
  credential per daemon, and an agent-spawn model the chart's own README already
  files as unsolved ("a k8s-native Job/Pod runner is future work"). The reason to
  carry that cost was that it is the documented k8s path, and it is not:
  `kubernetes.html` is, and it is current and maintained. Deleting the chart
  costs a k8s user nothing and removes a surface that has never once been
  rendered in CI. Rewriting it is defensible ONLY together with a `helm template`
  gate in `ci.yml` and an owner. Either way this is the owner's call, not a
  refinement — which is why it stays PROPOSED.

**RE-MEASURED 2026-10-08, and I filed it a second time as `L399` before finding this
entry.** The duplicate is folded in here; the counts it added:
`templates/gated.yaml` is **113 lines**, and `gated` has **15 more references** across
`values.yaml`, `_helpers.tpl`, `configmap.yaml`, `webd.yaml`, `timed.yaml`, `NOTES.txt`,
`Chart.yaml` and `README.md`. `NOTES.txt` tells the operator to run
`kubectl rollout status deploy/<release>-gated`. The crackbox half of the chart is gone
as of `a01f4d7b`; this half is the larger one and is unchanged.

**Why I missed this entry:** it has no `L`-number, so
`TestOpenEntriesAreListedHere` cannot see it and it has never appeared in "What needs
you". Ten entries are in that state. The guard now checks for them (same commit), which
is the actual fix — a queue item invisible to the guard gets re-filed by the next
reader, as it just was.

- **Status:** ✅ FIXED 2026-10-08 in `a7241d06`. `gated.yaml` and the shared all-tokens
  `secret.yaml` are deleted; `routd`, `authd` and `runed` are new, written against
  `compose/compose.go`'s own service builders. `runed` holds `/dev/kvm`,
  `/dev/vhost-vsock` and `/dev/net/tun` as `CharDevice` hostPaths with
  `NET_ADMIN`+`SYS_ADMIN` and AppArmor `Unconfined`, and takes a `nodeSelector` because
  it pins to nodes that have them. One Secret per daemon replaces the shared one, which
  is what compose's per-daemon env files already are. The volume is the file tree, not
  the SQLite WAL that has not existed since v0.68.0.

  Every must-set value is `required` with a sentence naming the key and why, so a bare
  `helm lint` fails on purpose and `ci/lint-values.yaml` is the fixture that renders.

- **And the gate, which is the half that keeps it fixed.** This entry's own re-measurement
  said a rewrite without a gate drifts again, and it was right: the chart rendered nothing
  CI read, so it sat two phases behind for two months. `make lint-helm` runs `helm lint`
  and `helm template` — neither needs a cluster — and asserts the two facts that matter:
  the render names no `gated`, and it carries routd, runed and authd. It is in
  `.github/workflows/ci.yml`. Mutating routd's component label fails it.

- **VERIFIED vs NOT.** Verified: lint clean, 11 Deployments, zero `gated`, each `required`
  triggered by hand. **Not verified: that it deploys.** No cluster exists on this host and
  the README says so. Three things the port found and did not fix, each named in the
  README: proxyd's route rows name `http://<daemon>:8080` while the chart's Services are
  `<release>-<daemon>`; a hostPath device may still be refused by the device cgroup without
  `privileged`; adapters get no volume for their offset files.

## V2 — the cockpit split SHIPPED and the duplication is closed; one item is live: dashd has no authority-provenance view (2026-08-04, DUPLICATION CLOSED 2026-09-21)

**Re-measured 2026-09-21 at `f5851f94`. The direction was taken, not
abandoned.** `6/28`, `6/29` and `6/30` are all `status: shipped`, and eight
daemons serve their own `/dash/<daemon>/` (`routd/dash.go`, `runed/dash.go`,
`authd/dash.go`, `proxyd/dash.go`, `onbod/dash.go`, `timed/dash.go`,
`webd/dash.go`, `ttsd/dash.go`), with davd's abstention recorded as the ninth
outcome rather than a ninth gap. So the premise this entry was blocked on —
"no daemon but onbod and timed has a `/dash/` route at all" — is no longer
true, and every row of the table below has an answer:

| this entry's row | outcome at HEAD |
| --- | --- |
| `chat.go` + test | deleted 2026-08-15, as recorded below |
| route-token mint form | deleted 2026-08-15, as recorded below |
| `runed_page.go` runs + kill | **moved** — the file is gone; `runed/dash.go` serves the run table and kill |
| errored-retry | **moved** — `routd/dash.go` serves retry + replay |
| routes, tokens list/revoke, tasks, invites, usage, audit, packages, groups | **RETAINED BY DECISION** — `6/29` §"Retained cross-cutting pages" names `route_tokens`, `routes_admin`, `grants_admin`, `usage_page`, `groups_admin` and `tasks_admin` as dashd's, because they span daemons or are operator-global |

The per-page complaints are answered too, and by the code rather than the
spec's intention: `usage_page.go` reads routd's store, not the frozen
`messages.db`; `audit_page.go` fans out to each owner's `GET /v1/audit` and
merges, instead of reading routd's `audit_log` and calling it global; the tool
browser (`dashd/tools_admin.go`, 51 lines) renders `ipc.ListTools` — the same
path the runtime uses, which is one renderer and not a second copy; and
`/dash/` is now a tile grid of PAGES that links to `/dash/services/` as one
tile among eleven, rather than repeating it.

**The one thing still live is the item this entry itself called "the one real
gap", and it is no longer blocked by anything.** `6/29`'s retained-page table
promises "grants / ACL (rows, **edges, roles, principal-effective**)".
`dashd/grants_admin.go` renders `acl` rows and consumes `acl_membership` as
autocomplete only — the same two sites this entry named in August. Measured
across the tree: `store.MemberEdges` and `store.ParentEdges` exist and are
called by `onbod/main.go` (the person's own linked-accounts list) and by tests.
No dashd handler calls either. So an operator still cannot answer "why does
this Telegram account carry Alice's authority?", and the shipped spec says they
can.

- **Severity:** medium — an operator cannot audit delegated authority, and a
  shipped spec advertises the view
- **Scope:** `dashd` grants page
- **Affected:** `dashd/grants_admin.go`; `specs/6/29-dashd-hub.md`
  §"Retained cross-cutting pages"
**MEASURED 2026-09-22: "not a redesign" was wrong, and the blocker is
specific.** The store reads do exist. What does not is a way for dashd to make
them, because `6/28` puts dashd's grants page on routd's `/v1` — reading
`store.MemberEdges` from dashd would re-introduce exactly the cross-owner read
just removed from `dashd/invites.go`.

routd's `acl_membership` resource declares **one action, delete**, and its
header says so on purpose: "It gains exactly ONE action — delete — scoped to
`added_by='pairing'` so it cannot reach role membership." A provenance view
needs a LIST, and adding one to that resource publishes "who holds authority
over whom" on REST and, per `5/17`, on MCP as well.

- **Status:** open — needs sign-off on the read, not on the view. Three
  questions the delete action did not have to answer:
  1. **Who may list whose edges?** Delete proves the caller IS an endpoint of
     the edge. A list has no such natural bound: an operator listing a folder's
     edges is one thing, a folder-scoped caller listing another folder's is a
     disclosure.
  2. **Does the agent get it too?** `5/17` derives the MCP face from the REST
     one. `unpair` is deliberately agent-reachable; a `list_membership` tells
     every granted agent who else holds authority.
  3. **Which direction?** `MemberEdges(parent)` and `ParentEdges(child)` answer
     different questions and leak differently.

  Once the read is signed off the view itself IS small — the page exists and
  the unpair action exists.

### The August review, kept for the record

Codex review of the dashd surface against the post-phase-5 end state. Almost
nothing is *dead* — the identity deletions left only textual rot. The problem is
**duplication**: dashd hosts control planes that `specs/6/30` moves to each
daemon's own `/dash/<daemon>/`.

| target | LOC | why |
| ------ | --- | ---- |
| ~~`chat.go` + test~~ **DONE** | ~1,019 (really 477 + 504 test) | duplicates webd's `/me/chats/new`; **records the RAW reusable route bearer** (`chat.go:25,38,450`) |
| routes / route_tokens / errored-retry — **mint form DONE, rest blocked** | ~867 | duplicates routd's planned `/dash/routd/`. The mint form (`route_tokens.go:103`) should not survive anywhere — cockpit may list/revoke, minting belongs to webd and agent tools |
| `runed_page.go` | 526 → **really 289** | reads `runed.db.spawns` directly and proxies kill; both are runed's |
| tasks (`main.go:771`, `tasks_admin.go`) | ~505 | duplicates timed's dashboard, including a SECOND cron parser and next-run computation |
| invites + whapd pairing | ~782 | belong on `/dash/onbod/` and `/dash/whapd/` |
| usage / audit / packages | ~842 | usage reads the frozen `messages.db` (`usage_page.go:38`); audit reads only routd's `audit_log` while claiming to be global; packages duplicates the CLI |

Plus in-page duplicates: the `/dash/` tile portal repeats `/dash/services/`;
`/dash/status/` repeats counts and calls them health while the hub probes real
health; groups repeats usage and route rows; the 53-line tool browser duplicates
MCP `tools/list`.

**`chat.go` is deletable NOW, ahead of the rest.** `chat_sessions` does not exist
on krons, sloth or marinade — the portal has never been used, so there is no data
to preserve and no capability to lose. Deleting it also removes a plaintext
bearer store of exactly the class Y2/I1 just closed for invites.

**Everything else is SEQUENCED, not free.** Deleting dashd's routes page before
routd serves `/dash/routd/` loses the capability. Order: build the per-daemon
page, then delete dashd's copy. That ordering is the whole proposal.

**The one real gap** is not an edge list — it is an **authority-provenance view**.
`grants_admin.go:52` renders only `acl` rows and consumes `acl_membership` merely
as autocomplete (`:92`), so an operator cannot answer "why does this Telegram
account carry Alice's authority?". The view needs child → parent, `added_by`,
role chains and the resulting effective grants, wired to the existing unpair
action.

Explicitly NOT to add: an outstanding-pairings page (a 10-minute single-use token
offers an operator no action) and an archive page (`arizuko archive` is a CLI
operation).

- **Severity:** medium (duplication + one unused plaintext bearer store)
- **Fix:** `f98f2f33` — chat portal + `chat_sessions` drop (routd `0036`) +
  route-token mint form. The rest stays — see below.

### Done 2026-08-15 — the two that were not sequenced

**`chat.go` deleted** (477 lines + 504 of test, not the ~1,019 counted here —
the count was of both files). Replacement verified live, not assumed: webd
registers `GET`+`POST /me/chats/new` (`webd/server.go:130-131`) and proxyd
routes `/me/` to it with `Auth: "user"` (`compose/compose.go`). `chat_sessions`
— the RAW reusable bearer store at `chat.go:428` — is dropped by
`routd/migrations/0036-drop-chat-sessions.sql`; 0029's own header records zero
rows fleet-wide, so there was nothing to preserve. dashd's `chat` tile and
both folder-link sites (`runed_page.go`, `routd_page.go`) now point at
`/me/chats`, and the dead `.conv-*` CSS is out of `theme/theme.go`.

**Route-token mint form deleted** (`route_tokens.go`, the `POST
/dash/tokens/{folder}/` route with it). This one needed no replacement: routd's
`route_tokens` resource already serves both faces —
`POST /v1/route_tokens/{chat,hook}` for operators and `issue_chat_link` /
`issue_webhook` / `issue_pairing_link` for agents
(`resreg/resources/route_tokens.go:38-54`) — plus `arizuko token` for the CLI.
List + revoke stay. `6/30` §webd already said it: "There is no token minting
anywhere in the cockpit."

### Was BLOCKED on `6/30` — resolved 2026-09-21, two moved and one retained

The August measurement, and what each row became once `6/30` shipped. It was
verified against each daemon's route registration rather than the spec's
intention, which is why the table survived being wrong about the direction:
at the time, no daemon but onbod and timed had a `/dash/` route at all
(`compose/compose.go:367-370`), and dashd answered `/dash/routd/` and
`/dash/runed/` from the catch-all.

- **runs + kill: MOVED.** `runed/dash.go` serves the run table and kill;
  `dashd/runed_page.go` is deleted.
- **errored-retry: MOVED.** `routd/dash.go` serves retry + replay. `6/30`
  §routd records why retry and "reset breaker" are one verb, not two.
- **routes, tokens list/revoke, tasks: RETAINED.** `6/29` keeps them in dashd
  as cross-cutting pages, so there is nothing to delete. `/dash/timed/` ships
  deliberately control-less — routd owns the task rows and timed owns the fire
  loop, so timed's page reads through routd's `GET /v1/tasks` and dashd stays
  the place a task is created, paused or cancelled.

| dashd surface                                        | LOC (verified)              | what `6/30` must ship first                                                                                                       |
| ----------------------------------------------------- | --------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| routes + tokens list/revoke + errored-retry           | 304 + 145 + 173 (`routd_page.go`) | routd's `/dash/routd/` — routd serves no `/dash/` namespace and compose has no route to it                                    |
| `runed_page.go` runs + kill                           | **289**, not the 526 claimed | runed's `/dash/runed/` AND `GET /v1/runs?state=`. runed's mux is 8 routes and none lists runs (`runed/server.go:39-46`); there is no `ListSpawns` in the tree, so dashd's direct `spawns` read is the only run list |
| tasks list/detail/create/pause/resume/cancel          | 323 (`tasks_admin.go`) + `main.go` list | timed's task + task-detail pages. `/dash/timed/` is ONE handler, the overview (`timed/split.go:42`), with no controls        |

Deleting any of these today removes the capability outright. Order stands:
build the per-daemon page, then delete dashd's copy.

### The cron duplication was real and is FIXED — one renderer, `core.NextRun`

Three copies computed a next run in August, and the drift was live: routd's
`parseTaskSchedule` computed in the process's local time, dashd's `taskNextRun`
in `TZ`.

`core/cron.go` `NextRun(cronVal, tz)` is now the one place a cron value becomes
an instant, and every writer of `next_run` reads it — the seeder that gives a
new group's tasks their first run, timed's re-arm after each fire, and both
surfaces that take a cron from a human. dashd's `taskNextRun` is six lines over
`core.NextRun(cronVal, core.Timezone())`; routd's `parseTaskSchedule` calls the
same pair. The timezone drift is gone by construction: one function, one
`Timezone()`.

What remains is not a drift and is spec-recorded rather than loose. `6/30`
states it: "What still differs is what those two surfaces ACCEPT, not what they
compute: routd reads an RFC3339 timestamp as a one-shot, dashd rejects that
same value with 400." Confirmed in code — routd parses RFC3339 ahead of
`core.NextRun` and stores an empty cron so timed completes the task after one
firing, which is a value space `core.NextRun` deliberately does not carry.
Whether dashd should grow the one-shot is a product question for its owner, not
a defect in the renderer.

## L131 — the model credential is readable from the agent's shell on every harness; the boundary is the gateway, and no live instance runs one (2026-08-28, partial — claude scrub FIXED, gateway withholding SHIPPED 2026-09-05 and deployed nowhere, startup warning FIXED 2026-09-21, pi filter REFUSED; the closing action is the operator's)

**Boundary statement.** A harness-level env filter is defence against an
accidental echo, not a boundary. Every process in the agent container runs as
one uid (`container/runner.go`, `--user 1000:1000`), nothing sets `hidepid`,
and `/proc/<pid>/environ` is readable across siblings at equal uid. Measured
2026-09-05 and re-measured 2026-09-21 on `arizuko-ant:latest`: a sentinel
planted in one process's env, read from a sibling —

    sibling pid=8 self=1 uid=1000
    sentinel lines readable from sibling environ: 1
    pid 1 environ readable: 21 vars
    hidepid mounts: 0

So `cat /proc/1/environ` shows the credential on claude (`bash-scrub`), codex
(`shell_environment_policy`) and pi (`{...process.env}` verbatim) alike. The
one fix that holds is upstream of every harness: with `MODEL_GATEWAY_URL` set,
`container.spawnSecrets` deletes every `store.EnvProfileKeys` entry, so no
model credential enters the container and `/proc` holds nothing (spec 6/23).

**The fleet runs without it (measured 2026-09-21).** No live instance has a
gateway, none runs pi, and each authenticates with a subscription token:

    $ for i in krons sloth marinade; do sudo grep -hE '^(MODEL_GATEWAY_URL|ARIZUKO_BACKEND)=' /srv/data/arizuko_$i/.env; done
    (no output)
    $ for i in krons sloth marinade; do sudo grep -hoE '^(ANTHROPIC_API_KEY|CLAUDE_CODE_OAUTH_TOKEN|ANTHROPIC_OAUTH_TOKEN|OPENAI_API_KEY|CODEX_API_KEY)=' /srv/data/arizuko_$i/.env; done
    CLAUDE_CODE_OAUTH_TOKEN=     (each of the three)

The gateway fix is in effect on zero instances. Every live agent container
holds the operator's `CLAUDE_CODE_OAUTH_TOKEN` in its process env, readable
from the agent's shell and from `/proc`; on a multi-folder instance that is
every tenant able to read the operator's own token. This is the live state of
the whole fleet, not an edge case.

**Shipped.**

- claude scrub (`ant/src/guards.ts` `SECRET_ENV_VARS`, `bashScrub`): the
  three ANTHROPIC/CLAUDE names are unset in front of every Bash command.
  `OPENAI_API_KEY` / `CODEX_API_KEY` are deliberately not — the agent's own
  `/oracle` skill shells out to `codex` and probes exactly those two.
  `container/secret_scrub_test.go` pins the partition against
  `store.EnvProfileKeys` across the language boundary. Echo defence only.
- gateway withholding (`container.spawnSecrets`, 2026-09-05): the boundary,
  wherever a gateway is configured.
- startup warning (2026-09-21, `b7d3b253`): `container.SpawnHoldsModelCredential`
  — no gateway and an anchor set — and runed logs one WARN at start beside the
  substrate line: `no model gateway: the operator model credential enters every
  agent container and is readable from the agent's shell and /proc; set
  MODEL_GATEWAY_URL (the litellm package, spec 6/23) to withhold it`. Unset is
  a supported deployment (6/23: fine when operator and users are the same
  people), so a warning, not a refusal. `TestSpawnHoldsModelCredential` pins
  both halves; both mutants killed.

**Refused: a pi shell-env filter.** Any one reason suffices. (1) Not a
boundary — see above. (2) pi is the backend on no instance, so it defends
nothing today. (3) The only mechanism that works is unverified. Deleting the
keys from `process.env` inside the extension is DISPROVED on pi 0.51.6: pi
reads the credential per request (`agent-loop.js:150` →
`AuthStorage.getApiKey`, whose step 4 is the env var), and `ExtensionContext`
exposes no auth handle to supply it another way. What would work is writing
the credential to `getAgentDir()/auth.json` (pi's step 2) and spawning pi from
`ant/src/pi.ts` with `turn.env` minus the keys; two unknowns only a live pi
settles — the `auth.json` entry shape for an OAuth credential, and whether
pi's refresh rewrites that file. A credential-handling change verified only by
reading does not ship. With a gateway the question is moot: pi's env then
holds `MODEL_GATEWAY_KEY`, a per-turn virtual key. codex needs no filter of
ours: `shell_environment_policy` default excludes `*KEY*`, `*SECRET*`,
`*TOKEN*` (codex-cli 0.147.0 binary strings) cover all five names — echo
defence, same caveat.

**OPEN — the closing action is the operator's, and it is a billing decision.**
The `litellm` package closes the live exposure by construction, but the three
instances hold `CLAUDE_CODE_OAUTH_TOKEN`, a Claude subscription token, and
Anthropic's terms (code.claude.com/docs/en/legal-and-compliance, read
2026-09-21) say developers "may not collect, store, or intermediate Claude.ai
credentials or session tokens" and may not "route requests through Free, Pro,
or Max plan credentials on behalf of their users"; an API key "in a
development environment, secrets manager, or machine image" billed to the key
owner is the permitted shape. So the choice is: gateway + API-key billing
(exposure closed), or keep the subscription token in every container and
accept the posture the WARN names. Neither is a code change. The entry closes
when each instance sets `MODEL_GATEWAY_URL`, or the operator records that the
posture is accepted.

History, so it is not filed wrong a third time: the first filing claimed codex
was exposed (it filters its shell env); the second treated the harness filters
as the gate (the `/proc` measurement refuted that); candidate 1 for pi was
disproved on a live pi.

- **Severity:** high — live on all three instances, and the credential is the
  operator's subscription token
- **Scope:** container env / model credential
- **Affected:** krons, sloth, marinade — every agent container
- **Source:** `container/runner.go` (`readSecrets`, `spawnSecrets`,
  `SpawnHoldsModelCredential`), `runed/cmd/runed/main.go` (the WARN),
  `ant/src/guards.ts`, `ant/src/index.ts` (env merge), `ant/src/pi.ts:200`,
  `store/secrets.go` (`EnvProfileKeys`), pi `dist/utils/shell.js:105`
- **Status:** open — operational, operator decision
- **Fix:** `b7d3b253` (startup warning); claude scrub and gateway
  withholding shipped earlier

## L134 — a `shell:env:` grant cannot be conditioned on the channel, and writing the predicate that looks like it can silently revokes the key everywhere (2026-08-29, SILENT HALF FIXED 2026-09-20; RE-MEASURED 2026-09-21 — the same trap is LIVE and FAIL-OPEN in the exempted `hold:` namespace, pinned by test; ONE question, needs sign-off)

An operator asked for "only atlas **in telegram** may use the gh key". The
`shell:env:<KEY>` row has a `predicate` column and `namespacedAllows` evaluates
it (`auth/authorize.go:426`), so `predicate=platform=telegram` LOOKS like the
answer. It is a trap.

`ShellEnvSecrets` (`routd/sibling_db.go:150`) builds the caller as:

```go
caller := auth.Caller{Principal: "folder:" + folder}
```

**No `Claims`.** And `ShellEnvKeys` (`auth/authorize.go:463`) passes `nil` for
params. `predicateMatches` (`auth/authorize.go:298`) returns `true` only for an
EMPTY predicate; any non-empty predicate against nil claims returns false at
`:309`/`:317`. So a `platform=telegram` predicate does not narrow the grant to
Telegram — it makes the row match nothing, on every channel, and the agent
loses `gh` silently. Nothing logs a denial, because nothing was denied: the key
simply never appears in the shell.

- **Why the information is not there:** the resolution happens once per dispatch
  (`routd/dispatch.go:753`), keyed on the FOLDER. The trigger's platform is a
  property of the chat JID that routed the turn, not of the folder, and it is
  not carried into the capability call.
- **What IS expressible today, and is how the operator's intent was met:** the
  grant's scope is an exact folder (`atlas` does not cover `atlas/support` —
  measured, `auth.MatchGroups(["atlas"], "atlas/support") == false`). So routing
  a channel at a CHILD folder denies it the key by construction. Removing
  atlas's non-support Slack routes left Slack reaching only `atlas/support`,
  which the `atlas`-scoped grant does not cover.
- **The gap that structure does not close:** any channel routed at the bare
  granted folder still gets the key — the routing table is what decides, not the
  grant.

  **Resolved on marinade 2026-08-29 by routing, per the operator's decision:**
  Slack's non-support routes were removed and Discord's two routes re-targeted
  from `atlas` to `atlas/support` (new ids 34, 35). Only Telegram routes 2 and 6
  (`room=user/1112184352`, `room=user/1444744465`) now reach the bare `atlas`
  folder, so only Telegram-triggered turns hold the token. Checked twice: the
  scope does not cover the child folder, and the sole wildcard row
  (`role:operator | * | **`) does not confer a `shell:env:` action, because
  `namespacedAllows` accepts only the exact action or `shell:env:*`.

  That is a per-instance ROUTING arrangement, not an expressible grant. It holds
  only while nobody points another channel at `atlas`, and nothing enforces
  that — which is why the fix below stays open.
- **Minimal fix (proposal):** stamp the trigger's platform as a claim on the
  caller at `routd/dispatch.go:753` — `Claims: map[string]string{"platform":
  …}` — so `predicate=platform=telegram` means what it reads. It is small and
  local, but it changes what a grant row can express on a credential path, so it
  is an auth-model change and needs sign-off before it ships.
- **Second, independent hazard worth fixing either way:** a predicate that can
  never match should not be silently unsatisfiable on this path. Either reject
  an unknown predicate key when the row is written, or log at ERROR when a
  `shell:env:` row is skipped for a predicate the caller carries no claims for.
  Today the failure is indistinguishable from "no grant".

- **The SILENT half is fixed 2026-09-20 — the trap is now a loud refusal.**
  `aclPut` (`store/acl.go`) refuses a `shell:env:` row carrying any predicate,
  and the message says what it would have done and what to write instead: "a
  predicate on %q can never match — the shell-secret resolver is keyed on the
  folder and carries no claims, so %q would revoke the key on every channel
  rather than narrow it; scope the grant to an exact folder instead".
- **It sits beside the effect check, at the same choke point and for the same
  stated reason:** every door into the acl entity renders through `aclPut`, and
  "a guard in one face is a guard the next face forgets" (`L272`'s wording).
- **Scoped to the namespace whose resolver carries no claims**, not to
  predicates in general. `CheckHold` passes `caller.Claims`, so a `hold:`
  predicate is evaluated rather than dead and stays writable.
  PROVEN BY MUTATION both ways: drop the namespace term and the `hold:` test
  fails (the guard took a working capability away); remove the guard and the
  `shell:env:` test fails (the silent revocation is back). A third test pins
  that a `shell:env:` grant with NO predicate — the shape that works — is still
  accepted.
- **`ShellEnvPrefix` moved to `store` and `auth` aliases it**, matching
  `HoldPrefix` exactly: the acl WRITE validates rows in this namespace and the
  evaluator reads them, so one declaration serves both and neither can drift
  into a prefix the other does not recognise.
- **Still open, and it is the operator's original ask:** expressing "only atlas
  IN TELEGRAM may use the gh key". The trigger's platform is a property of the
  chat JID that routed the turn, and the resolution happens once per dispatch
  keyed on the FOLDER (`routd/dispatch.go:899`), so the information is not there
  to condition on. Carrying it is a capability-call contract change.

- **RE-MEASURED 2026-09-21 at `b3b973e2`: the same trap is live in the `hold:`
  namespace, which the write-guard deliberately exempts — and there it fails
  OPEN.** The exemption's stated ground is false as a statement about the call
  graph. `store/acl_test.go` justified it with "CheckHold passes the caller's
  claims, so a hold: predicate is evaluated rather than dead". `auth.CheckHold`
  does forward `caller.Claims` (`auth/authorize.go:605`) — and nothing fills
  them. The only production caller is `routd/sibling_db.go:301`,
  `auth.Caller{Principal: sub}`, reached from `routd/pending_actions.go:479`
  `holdGate`. The only two sites that populate `auth.Caller.Claims`
  (`resreg/resreg.go:755` `authCaller`, fed by `routd/server.go:407`) reach
  `Authorize` and never `CheckHold`. Grep of every `.Claims` site confirms it.
  - **Direction matters, and it is the reverse of the `shell:env:` case.** A
    never-matching `shell:env:` row withholds a KEY — the agent loses `gh`.
    A never-matching `hold:` row withholds the GATE: the tool call the operator
    wrote the rule to suspend proceeds unheld, and nothing logs a denial because
    nothing was denied. Same silence, opposite side of the fence.
  - **PINNED BY TEST, mutation-proven both ways**
    (`auth.TestCheckHold_PredicateIsInertOnTheLiveCaller`): the predicated rule
    does not hold a claimless caller; the identical row WITHOUT a predicate
    does, so the predicate is the cause; and the same row DOES hold once the
    caller carries `platform=telegram`, so the evaluator is sound and the gap is
    the caller. Mutation 1 — drop `predicateMatches` from `namespacedAllows` —
    turns the first assertion red. Mutation 2 — pass `nil` claims from
    `CheckHold` — turns the third red.
  - **Not fixed here on purpose.** Both remedies are policy: extend the
    `aclPut` refusal to `hold:` (the row becomes unwritable), or stamp the
    trigger's platform as a claim (the row becomes live in BOTH namespaces).
    They are the same decision, and it is the one below.

- **THE ONE QUESTION:** should a turn carry its trigger's platform as a caller
  claim — making `predicate=platform=telegram` mean what it reads on `hold:`
  AND `shell:env:` rows — or is a capability conditioned only on FOLDER, with
  the acl write refusing a predicate in both namespaces?
  - Answer "carry it" → stamp `Claims` at `routd/dispatch.go:899` and
    `routd/pending_actions.go:479`, drop the `aclPut` refusal. Auth-model
    change; needs the folder/channel confinement argued, because a claim the
    channel supplies is a claim the channel can choose.
  - Answer "folder only" → extend `aclPut`'s refusal to `HoldPrefix`, and the
    `hold:` fail-open closes at the write. Smaller, and it removes a row shape
    no evaluator reads.

## L133 — `actionCovers`'s `*` covers rule namespaces too, so every namespaced rule needs its own evaluator (2026-08-29, open — RE-MEASURED 2026-09-21: `ext:` is a third namespace the proposal would break, check 2 half REFUTED; ONE question, needs sign-off)

`auth.Authorize` is documented as the SOLE runtime authorization evaluator
(`5/33`). It is not, and the reason is one line.

`actionCovers` returns true for ANY requested action when `rowAction == "*"`.
An operator holds `(*, **)`, so under the lattice an operator matches
`hold:mcp:delete` and `shell:env:GITHUB_TOKEN` as readily as `mcp:send`. For a
GRANT that is right — the operator may do anything. For a RULE it is backwards:
it would suspend every tool call the operator makes (spec `5/19` Hazard 2) and
hand every folder secret to every operator turn.

So each rule namespace opts out with its own reader. `CheckHold` did it first;
`ShellEnvKeys` repeated it 2026-08-29 and the two now share `namespacedAllows`
(`98a637a1`). That shared body is the workaround, not the fix: the cost is one
more hand-rolled evaluator per namespace forever, and every one of them is a
place the scope/predicate/params filter can drift from `Authorize`.

**Proposal — invert the default for rule namespaces.** Teach `actionCovers`
that `hold:` and `shell:env:` are RULE namespaces, where only an exact action
or the namespace's own `<ns>*` matches, and no broader row covers them. Then
`Authorize` is correct for every question, `namespacedAllows` deletes, and
`CheckHold`/`ShellEnvKeys` become thin wrappers that supply the prefix.

Two things to check before shipping, neither settled here:

1. **A rule namespace must be declared, not sniffed.** Deriving "is this a rule?"
   from a `:` in the action name would catch `mcp:send` and `web:publish`, which
   are grants. It needs an explicit set — one declaration, and `grantableAction`
   in `cmd/arizuko/main.go` is a second reader of it (`L133` is exactly the shape
   `feedback_grant_action_needs_a_door` warns about).
2. **`EffectiveActions` and `UserScopes` also walk rows** and would need the same
   treatment, or a rule row would show up in a capability listing as though it
   were a grant.

This is a change to the core evaluator's contract, so it is recorded here and
NOT shipped. The current state is correct and tested — the mutation that makes
`namespacedAllows` read the lattice turns 6 tests red across both namespaces.
This entry is about the shape, not a live defect.

- **Source:** `auth/authorize.go` `actionCovers` (`rowAction == "*"`),
  `namespacedAllows`, `CheckHold`, `ShellEnvKeys`; `specs/5/33-paths-roles.md`
  ("SOLE runtime authorization evaluator"), `specs/5/19` Hazard 2.

- **RE-MEASURED 2026-09-20: the root cause is unchanged and the consequence was
  PREVENTED.** The entry's title is the part that no longer holds.
- **The one line is still there.** `auth/authorize.go:390-393` — `actionCovers`
  returns true for any requested action when `rowAction == "*"`. Under the
  lattice an operator's `(*, **)` still matches `hold:mcp:delete` and
  `shell:env:GITHUB_TOKEN`.
- **But "every namespaced rule needs its own evaluator" did not happen.** There
  are TWO namespaces, `hold:` and `shell:env:`, and they share ONE body.
  `namespacedAllows` (`auth/authorize.go:603-613`) states both the reason and
  the guard against the drift this entry predicts: "The action is matched
  EXACTLY, never through `actionCovers`. That is the whole reason this body
  exists apart from `Authorize` … Both namespaces need the same refusal, so they
  share one body rather than two loops that drift."
- **So the debt is bounded and written down rather than accumulating.** What
  remains is the design question the entry really asks: `5/33` calls
  `auth.Authorize` the SOLE runtime evaluator and there are two, because the
  lattice cannot express a rule — only a grant. Resolving that is an auth-model
  change, which is why this stays a proposal.
- **The hazard it cites is real and already guarded:** an operator's `*` reading
  as a rule would hold every tool call the operator makes (`5/19` Hazard 2) and
  hand every folder secret to every operator turn. Exact matching is what stops
  both.

- **RE-MEASURED 2026-09-21 at `b3b973e2`. The one line is unchanged
  (`auth/authorize.go:390-393`). Three things moved under it.**

- **(1) There is now a THIRD colon-namespace, and it is a GRANT — which makes
  the entry's own check 1 sharper and more dangerous than it records.**
  `auth.ExtScopePrefix` (`auth/authorize.go:655`) opens `ext:`, and an ext
  tool's `scope` string is passed as the ACTION to `Authorize`
  (`ipc/ipc.go:1187`, `db.Authorize(folder, tool.Scope, nil)`; the field is
  `Authorize func(folder, action string, …)`, `ipc/ipc.go:274`). So `ext:` is
  evaluated through `actionCovers` ON PURPOSE. Its comment says why, and it
  cites a bug ID: "an action in this namespace has to be grantable or the only
  rows that reach an ext tool are a blanket `*` and an operator elevation, both
  of which confer everything (BUGS `L230`)".
  - Check 1 names `mcp:send` and `web:publish` as the false positives a sniffed
    "has a `:` → it is a rule" test would catch. It misses `ext:`, which is
    shaped EXACTLY like `shell:env:` — multi-segment, colon-delimited,
    open-ended, allowlisted by the same `grantableAction` prefix switch
    (`cmd/arizuko/main.go:600-605`). An implementation that sniffs would take
    `*` away from every ext tool and leave them reachable only by an exact
    `ext:<provider>:<area>:<verb>` row. That is a functional regression on the
    namespace whose comment cites `L230`, not a security one — but it is the
    landmine in this proposal, and it is the one the entry does not name.
  - **The declaration has no home yet.** `HoldPrefix` and `ShellEnvPrefix` are
    declared in `store` (`store/acl.go:141,148`); `ExtScopePrefix` is declared
    in `auth`. `grantableAction` already enumerates all four open-ended prefixes
    in ONE place, so the SET exists — what does not exist is the rule/grant bit,
    and `auth` cannot read it from `cmd/arizuko`. If this ships, the bit belongs
    beside the prefixes in `store`, which already holds two of the three and
    owns the acl row.

- **(2) Check 2 is half REFUTED.** `EffectiveActions` cannot surface a rule row
  as a grant. Its lattice branch answers only `strings.HasPrefix(action, "mcp:")`,
  its exact branch keys `held[r.Action]` on the FULL action string, and the only
  caller queries it as `held("mcp:"+name)` (`routd/agent_gate.go:73`). A
  `hold:mcp:delete` row lands at `held["hold:mcp:delete"]` and is never asked
  for. `UserScopes` is the half that STANDS: it filters on effect and scope and
  reads no action at all (`auth/authorize.go:239-262`), so a `hold:` or
  `shell:env:` row's SCOPE does enter the `X-User-Groups` header proxyd stamps
  and authd's login-time snapshot, as though it were a grant scope. Its own
  comment already says a listing "can never be the decision", so this is a
  listing defect, not a gate defect.

- **(3) The lattice is now refused in THREE bodies, not two.** `identityCovers`
  (`auth/authorize.go:~150`) is a second exact-match matcher inside `Authorize`
  itself, for the identity axis, and its comment gives `namespacedAllows`'
  reason verbatim — `5/19` Hazard 2. So the shape the entry describes has
  spread INSIDE the evaluator it wants to fix, which strengthens the argument
  that the lattice cannot express "exact match" and weakens "one more
  hand-rolled evaluator per namespace forever" — nobody added a third namespace
  evaluator; `ext:` deliberately took the lattice instead.

- **THE ONE QUESTION:** is the rule/grant split a declared property of an action
  namespace that `actionCovers` reads — accepting that `ext:` must be declared a
  GRANT or it silently loses operator `*` — or does `Authorize` stay a
  grant-only evaluator, with `5/33`'s "SOLE runtime authorization evaluator"
  corrected to say it decides grants while rules are read exactly?
  - Answer "declared split" → the bit lands in `store` beside the prefixes,
    `namespacedAllows` deletes, `UserScopes` gains an action filter. Core
    evaluator contract change.
  - Answer "correct the spec" → nothing in the code moves, `5/33` gets one
    sentence, and this entry closes as a RECORD. `namespacedAllows`' comment
    already carries the whole argument.

## L137 — `sandbox/` and `crackbox/` are ~2400 lines no arizuko daemon runs (2026-08-30, DECIDED 2026-09-23 — option C, and `crackbox/` left the repo)

The operator's framing: *"i guess eventually crackbox changed to qemubox for
users and here it's useless then."* This entry records the measured basis for
that decision and does not take it.

**Measured at `b0b299f9`** (`go list -deps` over the whole module, plus
`go list -f '{{.ImportPath}} {{.Deps}}' ./...` for the inbound direction):

| dir         | non-test lines | inbound edges from arizuko                              | outbound edges into arizuko |
| ----------- | -------------- | ------------------------------------------------------- | --------------------------- |
| `egress/`   | 682            | `container` → `egress`; `routd`, `store` → `egress/match` | **none**                    |
| `sandbox/`  | 1342           | **none**                                                 | `egress/match` only         |
| `crackbox/` | 414            | **none**                                                 | `egress`, `sandbox`         |

`sandbox/`'s only consumer anywhere in the tree is
`crackbox/pkg/run/run.go:175,180` (`sandbox.New` / `h.Spawn`) — the local dev
command. `runed` and `container/` contain zero KVM/qemu references; runed
spawns Docker (`runed/docker.go:46` `NewDockerRuntime`). So `6/9`'s
"shipped but unwired" is confirmed, and 1342 lines of KVM library are carried
by every clone, build, vet and test run of arizuko without a production
caller.

The severance cost is therefore **exactly one package**: `egress/match` (144
lines of glob matching), which `sandbox/` imports and which `routd` and
`store` also import. Nothing else crosses. `tests/components/imports_test.go`
already machine-checks the outbound half of this boundary (it passes), so the
graph above is an enforced invariant, not a snapshot.

**RE-MEASURED 2026-09-22: the cost is TWO packages, not one.** The size holds —
`sandbox/` is 1356 non-test lines against the 1342 filed, and `egress/match` is
138 against the 144. What changed is the table's `sandbox/` row, which says its
only outbound edge is `egress/match`. `sandbox/sandbox.go` now imports
`substrate` as well, to declare its `substrate.Profile`.

`substrate` cannot travel with the leaving repo: `container/egress.go` and four
`runed/` files import it, so it stays in arizuko exactly as `egress/match`
does. Option B's severance cost is therefore `egress/match` (138) plus
`substrate` (65) — 203 lines across two packages to extract, vendor or copy.

The guard did not catch the new edge because it never checked for it: its own
header says "sandbox reuses egress's allowlist matcher and declares a
substrate.Profile", and `substrate` is inside its `shippable` set
(`tests/components/imports_test.go:28`). It enforces that `substrate` imports
nothing arizuko-internal, not what imports `substrate`. So the table above was
a snapshot after all, on the one row that decides option B's price.

Options, and what each costs:

- **A — keep as is.** Cost: 1756 lines (`sandbox` + `crackbox`) with no
  production caller stay in the module; `6/16`'s ship narrative keeps pointing
  at `crackbox` as the thing that ships (see `L135`). Gain: zero work, and
  `crackbox run --kvm` stays available to arizuko developers.
- **B — `sandbox/` + `crackbox/` leave as "qemubox"; `egress/` stays.** This
  is the operator's reading. Cost: `egress/match` must become a dependency the
  new repo can consume (extract it, vendor it, or copy 144 lines); arizuko devs
  lose `crackbox run` unless they clone the second repo. Gain: arizuko sheds
  1756 lines it never executes, and the remaining boundary is honest — arizuko
  keeps the part it runs.
- **C — wire `sandbox/` into runed as a second backend (`6/9`'s original
  intent).** Cost: real engineering, not packaging — `container/` carries 11
  internal deps and is Docker-shaped, and `6/20` already declined the warm-pool
  lifecycle that neighbours this. Gain: the KVM isolation becomes a product
  property instead of a library.
- **D — delete `sandbox/`.** Cost: discards shipped, tested work and kills
  `6/9`'s decision record. Not recommended; `B` preserves the option `D`
  destroys.

`A` and `B` are both defensible and `C` is a separate project. The reason to
decide rather than drift: while it is undecided, `6/16` keeps advertising a
ship gate for a component whose shape `6/24` already changed, and `6/9` stays
`defected` describing a library with no home.

**DECIDED 2026-09-23 — option C, plus the `crackbox/` half of option B.** The
maintainer's words: *"keep only the minimal in here and directly in runed. not
as a publishable code."* and *"remove crackbox or perhaps like park it with
qemubox or so in tools and remove from this repo."*

- `crackbox/` left the repo on 2026-09-23. The four files are parked at
  `/home/onvos/app/tools/crackbox/` beside `qemubox`, with a `PARKED.md` that
  says they need a `go.mod` and the two vendored packages to build. `Makefile`
  `COMPONENTS` drops it and `tests/components/imports_test.go` drops both the
  `TestNoDaemonImportsCrackbox` guard and the `crackbox` entry.
- `sandbox/` stops being a shippable component in the same commit: the
  `shippable` set is `egress`, `substrate`, `mcpfw`. `qemubox` is the
  standalone KVM tool, so arizuko publishes no Go library for it.
- `sandbox/`'s code folds into `runed/` as unexported code, which is
  `specs/6/41-qemu-substrate.md` step 1 of §"Order". Until that step runs,
  `sandbox/` sits in the tree with no consumer at all. That is the measured
  state, not a regression.
- `egress/match` and `substrate` stay in arizuko, so the 203-line severance
  cost option B carried is not paid.

- **Severity:** low (no runtime defect; this is packaging and narrative)
- **Scope:** module layout / component boundary
- **Affected:** `sandbox/`, `crackbox/`, `egress/`, `specs/6/9`, `specs/6/16`,
  `specs/6/24`
- **Source:** import graph measured 2026-08-30 (commands above);
  `crackbox/pkg/run/run.go:175,180`; `runed/docker.go:46`;
  `tests/components/imports_test.go`
- **Status:** decided. `crackbox/` is out of the repo; the `sandbox/` half
  waits on `6/41` step 1.
- **Fix:**

- [x] ✅ FIXED 2026-09-05 — `add_acl` and `POST /v1/acl` never write `grant_option` (`routd/acl_resource.go:290` vs `store/acl.go:16`), so re-delegation is reachable only through `resreg.Apply` — the live grant surfaces cannot express it. `aclArgsRow` reads the argument, `grantACLTx` writes the column and `list_acl` reports it; the Gate needed no change, because `auth.Delegate`'s `grantableRows` already only holds rows the caller has WITH the option. `fix(routd): write grant_option on the live acl grant surfaces`
- [x] ✅ FIXED 2026-09-05 — `webd` registers with chanreg once and sends no heartbeat (`webd/main.go:127`), so three failed probes deregister the `web` channel until the daemon restarts. The heartbeat `chanlib.Run` ran inline is now `RouterClient.Heartbeat`, replaying the registration `Register` already saves; `chanlib.Run` and webd both start it. `fix(chanlib): one heartbeat every registering daemon can run`

- **RE-MEASURED 2026-09-20: still live, and the number moved the wrong way.**
  `sandbox/` is **2029** lines of Go now, against the 1342 this entry counted.
- **The "nothing runs it" claim needs one correction.** No arizuko daemon
  imports it, which is the entry's point and holds — but it is not unreferenced:
  `crackbox/pkg/run/run.go:26` imports `github.com/kronael/arizuko/sandbox`, and
  crackbox is a shippable sibling with its own `cmd` and `Makefile`. So a delete
  is not a delete of dead code; it is a decision about whether crackbox keeps
  its isolation path.
- **That is why this stays a proposal.** `specs/6/24` cut the per-spawn egress
  proxy down to `egress/` and explicitly declined the heavier sandbox shape, so
  the question is whether the sibling still needs what arizuko decided not to
  use — and that is the maintainer's call about crackbox, not a cleanup.

## L181 — operator data left behind by L178 and L179 (2026-09-05, open — operator task; RE-MEASURED 2026-09-21, now exact commands)

Nothing mechanical fixes stored data. Two sets to clean by hand. **Measured
again on the live hosts at 2026-09-21; the numbers below replace every earlier
count in this entry, and three of its claims are refuted.** marinade is
disabled (no containers; its data dir remains), so only **krons** and **sloth**
are in scope, and krons is CLEAN on both items.

### Item 1 — per-user files under a name no reader looks up

`router.senderToUserFileID` (`router/router.go:233`) builds the name the agent
reads: the platform short code from `platformShort` (`dc`, `sl`, `tg`, `wa`,
`em`, `web`), a hyphen, then the BARE id with the `user/` kind segment dropped.
So `discord:user/811…` is `dc-811….md`. A file under any other name is invisible
to `UserContextXml`, which is why these four are stranded rather than merely
misnamed.

All four are on **sloth**; krons has none:

```
$ sudo find /srv/data/arizuko_krons/groups -path '*/users/*' \
    \( -iname '*-user-*' -o -path '*-user/*' \)     # → nothing
```

| path under `/srv/data/arizuko_sloth/groups/` | bytes | mtime | correct name | twin? |
| --- | --- | --- | --- | --- |
| `main/trading/users/dc-user-657972559354265610.md` | 2686 | 08-24 21:36 | `dc-657972559354265610.md` | no — clean move |
| `main/main/users/dc-user-811295670702047272.md` | 1117 | 06-12 06:32 | `dc-811295670702047272.md` | no — clean move |
| `atlas/search/users/sl-user/U4PMARRT6.md` | 833 | 06-12 07:16 | `sl-U4PMARRT6.md` | no — clean move |
| `main/trading/users/dc-user-811295670702047272.md` | 2643 | 09-04 13:50 | `dc-811295670702047272.md` | **YES, 1917 B, 09-18** — MERGE |

**The three clean moves.** `-n` refuses to clobber, so a twin appearing between
reading this and running it stops the command instead of overwriting:

```bash
cd /srv/data/arizuko_sloth/groups
sudo -u mivu mv -n main/trading/users/dc-user-657972559354265610.md \
                   main/trading/users/dc-657972559354265610.md
sudo -u mivu mv -n main/main/users/dc-user-811295670702047272.md \
                   main/main/users/dc-811295670702047272.md
sudo -u mivu mv -n atlas/search/users/sl-user/U4PMARRT6.md \
                   atlas/search/users/sl-U4PMARRT6.md
sudo -u mivu rmdir atlas/search/users/sl-user      # non-recursive: fails if not empty
# verify: every users/ file now starts with a known short code
sudo find . -path '*/users/*' \( -iname '*-user-*' -o -path '*-user/*' \)
```

`atlas/search` is LIVE on sloth (activity 2026-09-21 03:09), so that third move
is the one with a reader waiting.

**The merge is a HUMAN read, not a command.** Both files are the same person
(`name: kronael`) and neither is a subset of the other — the older wrong-named
file carries operator knowledge the current one lost:

- only in `dc-user-811295670702047272.md` (09-04): the DEAD-token exclusion list
  (MNGO, DRIFT, ZEX/ZETA, "confirmed dead by live data 2026-06-15"), the
  candles-repo working style (never patch repo code from chat; log to `BUGS.md`;
  2025-2026 data is a LOCKED HOLDOUT), the clickable-bare-URL preference, and
  `first_seen: 2026-05-23`;
- only in `dc-811295670702047272.md` (09-18): the current thread-only reply norm
  and the recent-entry log.

So `mv` either way destroys real content. Open both, fold the four items above
into the 09-18 file, then delete the wrong-named one:

```bash
cd /srv/data/arizuko_sloth/groups/main/trading/users
sudo -u mivu "${EDITOR:-vi}" dc-811295670702047272.md dc-user-811295670702047272.md
# only after the fold is saved:
sudo rm dc-user-811295670702047272.md
```

`main/main/users/dc-user-811295670702047272.md` (1117 B) is an OLDER, shorter
copy of the same person in a different group and has no twin there, so it moves
cleanly above — it is a different group's file, not a third copy to reconcile.

### Item 2 — the rows `fetch_history` wrote into the inbound log

**krons: 0. sloth: 62.** The filed 634/947/28 cannot be reproduced and the
krons number was wrong when it was filed.

The damage is defined by the code that made it: before `216196c1`,
`fetchPlatformHistory` called `PutMessage` with
`core.Message{ID, ChatJID, Sender, Content, Timestamp, ReplyToID}` — so a
damaged row has the platform's own message id as its sort suffix and carries
NONE of `sender_name`, `source`, `chat_name`, `is_bot_message`.

The absent-attribute half of that test is NOT sufficient on its own, which is
what inflated every earlier count: `timed-isolated:` crons, `system` migrate
broadcasts, `user:anteval` harness traffic and HITL-approval synthetics all
share it and are all still written today. On krons the loose predicate answers
4229, of which **620 postdate the fix** — proof it matches live traffic. The
platform-native message id is what separates them.

Set `TBL` per instance, then count (read-only):

```bash
INSTANCE=sloth      # or krons
TBL=$(sudo docker exec -e PGUSER=arizuko_${INSTANCE} arizuko_extenddb_db_${INSTANCE} \
  psql -d extenddb_catalog -At -c \
  "select table_id from tables where table_name='routd.messages';")
sudo docker exec -e PGUSER=arizuko_${INSTANCE} arizuko_extenddb_db_${INSTANCE} \
  psql -d extenddb -At -F'|' -c "
  select pk, count(*), min(substr(sk_s,1,19)), max(substr(sk_s,1,19))
  from \"_ddb_${TBL}\"
  where not item_data ? 'sender_name' and not item_data ? 'source'
    and not item_data ? 'chat_name'   and not item_data ? 'is_bot_message'
    and substr(sk_s,32) ~ '^[0-9]+\$' and pk ~ '^[a-z]+:'
  group by pk order by 2 desc;"
```

The sort key is a 30-char instant, a `\x1f` separator, then the id — so the id
starts at **32**, not 31. Off by one and the count silently reads 0.

sloth answers 62 rows over four Discord chats, newest `2026-09-05T11:44:41`:

| chat | rows | oldest | newest |
| --- | --- | --- | --- |
| `discord:837270552959516682/920916162416635944` | 52 | 2026-05-29 | 2026-09-05 |
| `discord:837270552959516682/1388139201173655634` | 4 | 2026-07-28 | 2026-07-28 |
| `discord:837270552959516682/1514908015936143460` | 4 | 2026-06-12 | 2026-06-12 |
| `discord:837270552959516682/1505268596622753872` | 2 | 2026-05-18 | 2026-05-19 |

Two facts confirm the set is the right one: it contains
`1545761586571116545`, the single row `216196c1`'s message names, and **zero**
rows postdate the fix, so the producer is stopped and the set cannot grow.

**Recommendation: leave the 62 in place.** The producer is dead, the newest row
is outside the 7-day census, and what remains is that four Discord chats'
`inspect_messages` / `search_messages` return 62 rows of the bot's own history
as user inbound. Deleting them means raw `DELETE` against `_ddb_<uuid>` on a
live store — there is no CLI verb and no resreg face for it — which is a larger
risk than the symptom. If the owner still wants them gone, take a store backup
first, run the SELECT above and read every row, then re-run it as `DELETE` with
the identical WHERE clause, one `pk` at a time.

- **Severity:** low
- **Scope:** instance data
- **Affected:** sloth only (krons clean on both items; marinade disabled)
- **Source:** `router/router.go:233` (the name a reader looks up); `216196c1`
  (the write that stopped); live measurement 2026-09-21
- **Status:** open (operator) — four file operations and one judgement call on
  the rows
- **REFUTED from the earlier text:**
  - **krons is not affected.** It has no stranded user file and 0 damaged rows.
    Its filed 947 is close to the 946 `local:` chat rows whose sender is
    `system`/`anon` — synthetic CLI traffic the loose predicate swept up, not
    `fetch_history` output.
  - **`sl-user/U4PMARRT6.md` is on SLOTH, not marinade.** This entry filed it
    under marinade. marinade holds its own inert copy; sloth's is the one in a
    live group.
  - **`main/main/users/dc-user-811295670702047272.md` has no twin**, so the
    collision this entry warns about applies to `main/trading` alone — one
    merge, not two.

## L184 — `fetch_history` cannot tell the agent which rows are its own (2026-09-05, LOCAL-ROW HALF FIXED 2026-09-21; the platform-fetched half stays the chanlib proposal)

With L178 fixed the rows are rendered from the adapter's response, whose
`InboundMsg` has no own-author flag, so the bot's posts render as
`sender="discord:user/<bot id>"`. The agent reads its own words as another
user's — the twin of L150 on the read side. Shape: the adapter, which knows
its user id (`session.State.User.ID`), marks its rows `from_me` in the
`HistoryResponse` and the renderer emits the folder name as sender. A chanlib
wire change; decide before adding the field.

- **Severity:** low
- **Scope:** chanlib HistoryResponse, adapters' FetchHistory
- **Affected:** every adapter with the fetch_history cap
- **Source:** `discd/bot.go` FetchHistory, `routd/mcp.go` fetchPlatformHistory
- **Status:** proposal (needs sign-off)
- **Fix:**

- **RE-MEASURED 2026-09-20: CONFIRMED LIVE, exactly as filed.**
  `chanlib.InboundMsg` (`chanlib/chanlib.go:49-57`) carries `Sender`,
  `SenderName` and `ReplyToSender` and **no own-author field** — no `FromMe`,
  no `BotMsg`. `HistoryResponse` (`chanlib/handler.go:280-284`) is a slice of
  exactly that type, so the flag has nowhere to ride even if an adapter set it.
- **And routd drops the question rather than answering it.**
  `routd/mcp.go:399-404` maps each history row into a `core.Message` carrying
  `ID`, `ChatJID`, `Sender`, `Name`, `Content`, `Timestamp` and `ReplyToID` —
  `BotMsg` is never set, so every row the adapter returns renders as a user row,
  the agent's own posts included.
- **No contained fix exists**, which is why this stays a proposal: routd cannot
  infer own-authorship from what it receives. The adapter is the only party that
  knows its own platform user id (`session.State.User.ID` on Discord), so the
  flag has to be added to the wire type and set by each of the ten adapters — a
  `chanlib` contract change, the same reach `5/34` governs.

- **SCOPE CORRECTION 2026-09-21 — the entry named one tool and the defect had
  three, and two of them needed no wire change at all.** `fetch_history` is not
  the only agent surface that returns bot rows. `inspect_messages` (`ipc/ipc.go:2177`)
  and `get_thread` (`ipc/ipc.go:2297`) read `MessagesBefore` / `MessagesByThread`,
  which apply no `is_bot_message` filter (`store/messages.go:541,559`), and
  `fetch_history` falls back to the same unfiltered read when the adapter is
  unreachable (`routd/mcp.go:392`). On every one of those rows the store HAS the
  answer: `ipc/ipc.go:792` writes the agent's own reply `BotMsg: true`,
  `store/messages.go:206` persists it and `:238` reads it back. Only the
  renderer lost it — `router.FormatMessages` emitted twelve attributes and not
  that one.

  `inspect_messages`'s own tool description promises rows "including
  outbound/bot rows", so the tool documented a distinction its output could not
  express.

- **FIXED 2026-09-21 (local-row half), at the one renderer.** `FormatMessages`
  now emits `bot="true"` when `BotMsg` is set, beside the existing
  `errored="true"`. One renderer serves all four message sinks — the three tools
  and the turn prompt — so the fix lands once. The injected history is unchanged:
  it filters bot rows out (`is_bot_message` Absent, `store/messages.go:1106,1150,1244,1299`),
  which is spec `1/N`'s rule, so the marker only ever appears on rows the agent
  explicitly asked for.

  `BotMsg`, NOT `FromMe`: an imported archive sets `FromMe` for the linked
  ACCOUNT's own messages — the operator's, not the agent's
  (`store/messages_archive.go:411`). A test pins that.

  Tests: `TestFormatMessagesMarksBotRows` and
  `TestFormatMessagesFromMeIsNotOwnAuthorship`. Mutation-proven three ways —
  dropping the marker, emitting it unconditionally, and reading `FromMe` instead
  of `BotMsg` each fail.

  `ant/CLAUDE.md` tells the agent to read the attribute rather than infer
  authorship by matching `sender` against its folder name — the inference the
  doc previously implied, which is right only for rows this instance wrote and
  silently wrong for every platform-fetched one (the
  `identity_derived_not_read` class). Editing `ant/` rides a release: NOT bumped
  here, it ships with the next `MIGRATION_VERSION`.

- **Still open, and it is the original entry unchanged:** a row fetched FROM THE
  PLATFORM still carries no own-author flag, because `chanlib.InboundMsg` has no
  field for one and `routd/mcp.go:409` has nothing to map. That half is the
  chanlib wire change above and still needs sign-off. The fix here does not
  pre-empt it — it adds the renderer the wire flag would feed once it exists.

## L185 — proposal: `arizuko` runtime verbs as HTTP clients need a CLI principal and an audit-surface decision; refused as a mechanical cut (2026-09-05, proposal — RE-VERIFIED 2026-09-21: all four facts hold, `resreg/engine.go` is a third `cli`-surface producer; ONE question, needs sign-off)

Spec 6/36 S4 moves the nine `cmd/arizuko` writers (`grant`, `route`,
`network`, `secret`, `token`, `send`, `packages`, `apply`, `skills`) from
`store` onto routd's `/v1`, after `talk.go`'s shape. Measured at `f86e84b2`,
four facts make it a redesign, not a cut:

1. **Credential.** Only routd publishes a host port (`compose/compose.go`
   `routdService`, `API_PORT:8080`); authd publishes none, so the CLI cannot
   exchange `AUTHD_SERVICE_KEY` for a service token. The one host-side bearer
   is `mintBearer` (`cmd/arizuko/token.go`), which reads authd's ACTIVE
   PRIVATE KEY from `auth.db`. The trust root stays the filesystem; the cut
   moves the FS access from a `routd.db` write to an `auth.db` key read.
2. **Authorization.** routd's REST gates (`aclRESTGate`, `routesRESTGate`)
   authorize by bearer scope + JWT folder, not by `acl` rows, so a root
   bearer with `acl:write` CAN make the bootstrap grant over HTTP — the
   principal question does not block. What changes: every runtime verb needs
   routd up. `arizuko grant` with routd stopped fails where today it writes.
3. **Audit surface.** `store.AsCLI` records a CLI mutation as `cli:<osuser>`
   on surface `cli`; its own comment calls recording it as REST "a lie the
   audit trail cannot detect". Over `/v1` routd records `user:<bearer sub>`
   on surface `rest`. Every CLI mutation's audit row changes shape.
4. **`send`.** `operatorInject` (`cmd/arizuko/send.go`) is the second
   `messages` appender 6/36 §2 names; routd's poll (`routd/loop.go`
   `pollOnce` → `NewMessages`) does pick the row up, so the gap is the
   skipped ingest enrichment, not delivery. `POST /v1/messages` acks
   `{ok,id}` with no turn id, and `--wait`/`--stream` poll
   `BotRepliesSince` on the DB; none of the five `GET /v1/messages/*` reads
   is a bot-reply cursor for one chat, so an HTTP `send --wait` needs a new
   read endpoint.

Line count: the runtime-mutation bodies in those files total ~750 lines and
each `store` call becomes an HTTP call of the same size plus bearer minting.
The "~1k lines removed" is a move, not a removal.

Decision needed: (a) the CLI stays an FS-root operator (as is; `resreg.Apply`
remains the ungated second engine `6/36` §"Premises checked against the tree" names), or (b) it becomes a REST
client under a `user:cli` principal, retiring audit surface `cli` and adding
a `/v1` reply read for `send`.

- **Severity:** low
- **Scope:** cmd/arizuko write verbs
- **Affected:** operators of `arizuko grant|route|network|secret|token|send`
- **Source:** `cmd/arizuko/{main,route,network,secret,token,send}.go`;
  `compose/compose.go` routdService; `store/store.go` AsCLI;
  `routd/acl_resource.go` aclRESTGate
- **Status:** proposal (needs sign-off)
- **Fix:**

- **RE-VERIFIED 2026-09-21 at `b3b973e2`. All four facts hold; spec `6/36` still
  carries S4 (`specs/6/36-minimal-projection.md:339,391`). One cost is missing.**
  1. **Credential — holds, and is tighter than "only routd publishes a host
     port".** `ports` is set as a literal at exactly one site,
     `compose/compose.go:1170` (routd's `API_PORT:8080`). proxyd and davd build
     theirs from env for the web and file surfaces; authd publishes none. So
     there is no host-reachable authd to exchange `AUTHD_SERVICE_KEY` against.
     `mintBearer` (`cmd/arizuko/token.go:163-191`) still opens the authd store
     directly and signs with the ACTIVE key. The trust root is the filesystem.
  2. **Authorization — holds.** `aclRESTGate` (`routd/acl_resource.go:71-85`)
     gates on the `acl:write` bearer scope then `ownsFolder` against the JWT
     folder. No acl-row read, so a root bearer can make the bootstrap grant over
     HTTP and the principal question does not block. `scope "**"` needs the
     empty-folder root token.
  3. **Audit surface — holds, and reaches further than the entry says.**
     `store.AsCLI` sets `SurfaceCLI` and `auditIdentity` (`store/store.go:60-67`)
     is the one renderer of `cli:<osuser>` versus `user:<sub>`; the comment
     calling a REST recording "a lie the audit trail cannot detect" is intact.
     **But `audit.SurfaceCLI` has a THIRD producer the entry does not name:**
     `resreg/engine.go:805` `applyEvent` hardcodes it on every `config.apply`
     row. Its only production caller is `cmd/arizuko/apply.go`, so nothing lies
     today — and "retiring audit surface `cli`" therefore edits the resreg
     ENGINE, not only the ~750 lines of `cmd/arizuko`. That is a second
     subsystem inside option (b)'s blast radius.
  4. **`send` — holds.** `BotRepliesSince` (`store/messages.go:611`) still has
     no REST reader: `cmd/arizuko/send.go:197` and one store test are the only
     callers. An HTTP `send --wait` still needs a new endpoint.

- **THE ONE QUESTION:** is the `arizuko` binary an operator standing ON the host
  — filesystem as the root of trust, audited as `cli:<osuser>` — or a client OF
  the deployment, holding a bearer like every other caller?
  - Answer "on the host" → (a). Nothing moves, and `6/36` S4 is recorded as
    declined with the four facts above as the cost of doing it. `resreg.Apply`
    stays the ungated second engine by the same decision.
  - Answer "a client" → (b). Needs, in order: a reply-read endpoint for `send`,
    an audit-surface decision that includes `resreg/engine.go:805`, and the
    acceptance that every runtime verb now fails when routd is down.

## L186 — proposal: dashd's admin writes over routd's `/v1` need a forwarder trust routd does not have; refused as a cut (2026-09-05, proposal — RE-VERIFIED 2026-09-21: the absence already cost `L152` a dashd→routd import edge; ONE question, needs sign-off)

Spec 6/36 S1 says `webd/routes_mcp.go` proves the audit-actor bridge
(`X-User-*` forwarded under a service bearer). Measured at `f86e84b2`: that
bridge exists only at proxyd — `proxyd/resource.go` `trustedForwarders =
[webd, dashd]` and `channelTrusted`. routd's verifier
(`routd/cmd/routd/main.go` `keysetVerifier.Verify`) is `auth.VerifyHTTP`
alone: it returns the bearer's own sub and never reads `X-User-*`, and
`auth.ProxydTransit` pins `service:proxyd`. dashd's `bearerCall`
(`dashd/engagement_page.go`) sends no `X-User-*` to routd on purpose:
"routd and authd authorize the BEARER".

S1 therefore has two shapes, both redesigns: (a) routd grows a forwarder
allowlist like proxyd's and honors `X-User-*` under `service:dashd` — a new
trust edge on the daemon that owns `acl`; or (b) dashd writes under its own
bearer and the audit actor becomes the daemon — the regression
`store.AsUser` exists to prevent. Also absent: `POST /v1/groups`
(`resreg/resources/groups.go`: register is MCPOnly, "no daemon serves POST
/v1/groups"). dashd's create is one transaction over `groups`,
`SeedMemberRoleTx`, the creator's `admin` row and the audit row, plus
`container.SetupGroup` on the groups mount; 6/36 S11 needs the same handler.

Shipped instead, the safe half S1 lists "on the way": dashd's route update
and delete write through the store's audited transaction as the operator
(`AsUser(sub).UpdateRoute` / `DeleteRoute`); the raw `DELETE` + `audit.Emit`
outside the transaction as `rest:dashd`, and the audit-free
`UpdateRouteRow`/`DeleteRouteRow` twins, are gone.

Decision needed: shape (a) or (b), and whether `groups.create` lands on
routd's `/v1` first — it is the precondition for both S1 and S11.

- **Severity:** medium
- **Scope:** dashd ↔ routd identity on `/v1` writes
- **Affected:** dashd, routd
- **Source:** `proxyd/resource.go` trustedForwarders;
  `routd/cmd/routd/main.go` keysetVerifier; `dashd/engagement_page.go`
  bearerCall; `resreg/resources/groups.go` GroupsAgentEndpoints
- **Status:** proposal (needs sign-off)
- **Fix:**

- **RE-VERIFIED 2026-09-21 at `b3b973e2`. Every premise holds.**
  `trustedForwarders = {service:webd, service:dashd}` exists at proxyd only
  (`proxyd/resource.go:343`), and `channelTrusted` pins the bearer's sub so a
  valid token reaching proxyd directly cannot forge `X-User-*`. routd's verifier
  is `auth.VerifyHTTP` alone (`routd/cmd/routd/main.go:337-343`): it returns the
  bearer's own sub and reads no `X-User-*`. `auth.ProxydTransit`
  (`auth/middleware.go:33-39`) pins `service:proxyd`. dashd's `bearerCall`
  (`dashd/engagement_page.go:186`) still sends no `X-User-*` to routd on
  purpose. `POST /v1/groups` is still unserved
  (`resreg/resources/groups.go:30-38`).

- **NEW, and it is the strongest evidence this entry has: the absence has
  already been PAID FOR, on `acl` — the very table routd owns.** dashd's grants
  surface is SPLIT down the middle. It READS over routd's `/v1/acl`
  (`dashd/grants_admin.go:327`) and WRITES straight to the store
  (`:235` `AddACLRow`, `:309` `AsUser(sub).RemoveACLRow`).
  - When `L152` was fixed (2026-09-21), routd's alias warning could not reach
    the operator on the web surface, because dashd never makes the HTTP call
    that would carry it back. The fix exported the renderer —
    `routd.NonCanonicalWarning` (`routd/acl_resource.go:215-219`, "the one
    renderer of that warning, for every surface") — and dashd now calls it
    through a **Go import edge into the routd package**
    (`dashd/grants_admin.go:248`). Its own comment names the cause: "this form
    writes the store directly, so the operator using the web surface was the one
    person not told (BUGS `L152`)".
  - **So the missing forwarder trust is not a future cost.** It has already
    converted one HTTP dependency into a tighter compile-time one. Every further
    routd-side rule about an acl write — not just a warning — lands the same
    way: exported from routd and linked into dashd, or silently absent on the
    web surface. That is the drift `L152` paid for once and will pay for again.

- **THE ONE QUESTION:** may routd trust an allowlisted sibling's forwarded
  `X-User-*` — keeping the audit actor the human who clicked — or does routd
  stay bearer-only, making the daemon the actor on every admin write?
  - Answer "trust the forwarder" → shape (a): routd grows proxyd's allowlist
    under `service:dashd`, a new trust edge on the daemon that owns `acl`.
    `groups.create` on `/v1` is then the first handler to build, because S1 and
    S11 both need it.
  - Answer "bearer only" → shape (b), and the consequence is written down
    rather than discovered: `store.AsUser` stops being reachable from dashd's
    admin writes, so `6/36` S1 is declined and dashd keeps writing the store
    directly — with the `L152` import edge as the accepted pattern, not an
    accident.

## L188 — sections that left `ant/CLAUDE.md` under 6/34 with no anteval case behind them (2026-09-05, open — backfill list)

6/34 proposed "no case, no move"; the maintainer moved first and accepted a
window in which a dropped rule is undetectable. This is the ledger of that
window: each row is a rule that now loads only when the agent chooses its
owner, and whose observance nothing measures. A row closes when
`anteval/cases/anteval.toml` gains a case that exercises it.

| Moved section (owner now)                                                                     | Case today                                       |
| --------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| Greetings (`hello` frontmatter)                                                               | none                                             |
| Model economy — breadth to `/sonnet`, `/haiku` (their frontmatter)                            | none                                             |
| Persona layers (`persona/SKILL.md`, the `<persona>` block's own `/persona` pointer)          | none                                             |
| `inspect_*` / `find_messages` over `sqlite3` (`self/mcp.md`)                                  | none                                             |
| Session-opening ritual (`self/SKILL.md` §Session recovery)                                   | none                                             |
| `register_group` absent = ask, the CLAUSE only (`self/mcp.md`)                                | partial — `world-create-refusal-honest`, `child-delegate` |
| Additional mounts, operator SQL (`EXTENDING.md` §Adding a host mount)                         | none (operator procedure, not agent behaviour)   |
| CLAUDE.md ownership, `~/CLAUDE.md` operator-only (`self/migration.md`)                        | none                                             |
| Web routing table + login/dashboard/portal pointers (`self/web-routing.md`)                   | partial — `web-route`                            |
| Gateway commands (`self/commands.md`)                                                         | none                                             |
| `@<unknown>` prefix (`self/chat-routing.md`)                                                  | none                                             |
| Runtimes — `uv --python 3.14`, `bun`, never bare `pip` (`self/runtimes.md`)                   | none                                             |
| Attachment transcript preference (`self/messages.md`)                                         | none (`read-attachment-honesty` covers the resident no-tag rule only) |
| Social actions — `platform_id=` as `targetId`, edit windows (`like`/`delete`/`edit`/pin descriptions) | none                                     |
| Slack `like` scope error, Reddit unsupported (`channels/SKILL.md`)                            | none                                             |
| Slack / Discord threading (`slack-thread.md`, `slack-channel.md`, `discord-channel.md`)       | none                                             |
| Reaction register mirroring, DM anti-patterns (`like` description)                            | none                                             |
| Telegram group reactions (`telegram-group.md`)                                                | none                                             |

Covered by an existing case and therefore not listed: Network egress
(`egress-diagnose`, `egress-dns-distinguish`, `self-grant-noop`),
Publishing (`priv-401`, `priv-deny`, `priv-grant`, `web-route`,
`spawn-publish-reach`), Agent home / storage (`state-persist`, `priv-401`),
Local paths table (`link-is-public-url`), the map (`self-skill`).

- **Severity:** low (measurement gap, no observed regression)
- **Scope:** anteval cases
- **Affected:** every backend, every group
- **Source:** `specs/6/34-resident-prompt.md:276` §"The failure mode, and the three guards" (`status: shipped`); the move
  commit `docs(ant): ant/CLAUDE.md carries only what the rule of 6/34 keeps resident`
- **RE-MEASURED 2026-09-20 and the ledger was wrong about two of its own rows.**
  A ledger that names a rule as unguarded when the rule never left sends the
  next person to write a case for something that already loads on every turn.
  - **`send_file` caption = TL;DR — row removed.** The rule is still in the
    resident file, twice: `ant/CLAUDE.md:186` ("the `caption` carries the
    TL;DR") and `:196` ("a 2-4 sentence distillation"). It never moved.
  - **`register_group` — row narrowed.** `ant/CLAUDE.md:41` still says
    "Creating a group is always explicit … nothing auto-creates one". Only the
    "absent = ask" clause moved, so the row now names that clause alone.
  - The `Source:` line cited §"The failure mode"; the section is §"The failure
    mode, and the three guards" at `specs/6/34-resident-prompt.md:276`, and that
    spec is `status: shipped`.

  `anteval/cases/anteval.toml` holds 22 cases and has not changed since
  `9579fd32`, so every remaining "Case today" column re-measures identically.
  All 19 owner paths resolve and all 11 "already covered" ids exist.
- **Status:** open — 17 rows remain, and each is a case to write, not an edit.

**Sequencing, measured 2026-09-21: `L72` comes first.** Each row here closes by
adding an anteval case, and `anteval/cases/anteval.toml` holds 22 today. `L72`
records that every case creates a child group nothing deletes — re-measured,
krons now carries 47 eval groups of 59, and each one bills an auto-migrate turn
on every `MIGRATION_VERSION` bump, forever.

So backfilling these twelve rows against today's anteval raises a live leak by
about half again, and every release after pays for it. The ledger is right that
the rules are unmeasured; the cost of measuring them is the thing to fix first.

One row needs no case at all and should be struck rather than backfilled:
"Additional mounts, operator SQL" is an operator procedure, not agent
behaviour, and the table already says so.

## L189 — `sandbox/` writes no netfilter rule for a host-name allowlist entry (2026-09-05, SILENT HALF FIXED 2026-09-20; the rule is ANSWERED by `6/41`, pending that spec's step 1)

Measured at `7c631ed3`: `sandbox/internal/netfilter.go` `RestoreNetworkRules`
and `CleanupNetworkRules` walk `meta.AllowList` and act only where
`match.LooksLikeIP(target)` is true; the other branch is the comment
"domains are handled by egred proxy — no iptables rules needed". `6/24`
removed that proxy and the registration, and nothing on the VM path resolves
a name. `sandbox/README.md` said the matcher is shared with the proxy "so a
rule means the same thing on both substrates"; `specs/6/9` line 70 says
"host allowlist enforced by netfilter". Both overstate. The declaration
`sandbox.Profile.Egress = address` (shipped with `6/25` §3) now states the
measured level, and the README paragraph is corrected to match it.

What a name entry does at the packet level depends on the `cbx-<H4>`
chain's default policy, which is not in the tree (`libexec/crackbox-tap`
writes only NAT rules) — so whether a name is denied or is allow-all is
unmeasured. Decision needed, not taken here because the substrate is unwired
and each option is a design choice: (a) resolve names at spawn and write
address rules (TTL churn, rebinding); (b) a proxy inside the VM network;
(c) declare address-only in `6/9` and have `Spawn` reject a name in
`VMConfig.AllowList` — the strict option, the same rule `6/25` 1b applied to
the proxy ("a rule that never matches reads as protection and is not").

- **Severity:** low (nothing in arizuko runs `sandbox/` yet)
- **Scope:** `sandbox/internal/netfilter.go`
- **Affected:** a caller that passes host names in `VMConfig.AllowList`
- **Source:** `sandbox/internal/netfilter.go` RestoreNetworkRules; `egress/match.LooksLikeIP`; `sandbox/sandbox.go` Profile
- **Status:** open — the design answer is settled and the code change waits on
  `specs/6/41-qemu-substrate.md` step 1. `L137` chose option C, so the KVM
  lifecycle becomes runed's own code, and `6/41` §"Egress by name, sealed
  below the kernel" reaches option (c) by construction rather than by a
  reject: the tap rule set is static (accept guest to the host-side proxy,
  drop everything else), and every allowlist entry — name and address alike —
  goes to the proxy, which already matches both. A name never reaches
  netfilter, so no rule that "never matches" is left to read as protection.
- **Fix:** lands with `6/41` step 1; no separate change.

- **The silence is fixed 2026-09-20, and the stale comment with it.**
  `RestoreNetworkRules` said "domains are handled by egred proxy — no iptables
  rules needed". `egred` has not existed in the code for months; the only
  survivors are `CHANGELOG.md` and `USELESS.md`, which record what was true when
  written. A host-name entry now logs that NOTHING enforces it, naming the entry
  and the VM.
- **What the comment should have said, and now does:** iptables matches
  addresses, not names, and there is no proxy on the VM path to resolve one.
  `6/24` cut per-folder egress to one forward proxy per SPAWN — arizuko's path,
  not this one. So an operator who allowlists `api.github.com` here has
  allowlisted nothing.
- **PROVEN BY MUTATION:** make it silent again and the first test fails; drop
  the no-IP early return and the second fails, because a VM with no address yet
  must claim nothing either way.
- **Still open, and it is `L137`'s decision, not this entry's. RE-MEASURED
  2026-09-21 at `fdb59418`.** `L137` is still `proposal — needs sign-off` with
  nothing chosen among its four options, and three of the four make the rule
  wasted work: under **B** (`sandbox/` + `crackbox/` leave as qemubox) and
  **D** (delete) the code is not arizuko's to harden, and under **A** (keep as
  is) the package stays unwired, so a rule enforces nothing any arizuko path
  reaches. Only **C** — wire `sandbox/` into runed as a second backend — gives
  the rule a caller, and `L137` prices **C** as "real engineering, not
  packaging … a separate project".

  So the honest verdict is: **do not write the rule, and do not spend the ~10
  lines on (c) either.** (c) is cheap but it is a behaviour change to a shipped
  public API (`Spawn` starts rejecting a `VMConfig.AllowList` a caller accepts
  today), which is its own sign-off, spent on a package three of four options
  move or leave. Writing either one buys a decision `L137` has not taken.

  Re-open this the moment `L137` is signed **C**. Until then the log is the
  whole of what is owed, and it is shipped.

- **The `specs/6/9` half is discharged, without taking this decision.** That
  line is out of scope now: `6/9` points at `sandbox.Profile.Egress` instead of
  restating a granularity (`L190`, 2026-09-21), so it asserts nothing about
  host names and needs no second edit whichever option lands — adopting (a) or
  (b) moves the declaration to `EgressDomain` and the prose follows for free.

- **Stale in the neighbour, noted not fixed:** `L137`'s closing paragraph says
  "`6/9` stays `defected`". `6/9`'s frontmatter is `status: shipped` with no
  `defects:` key at `fdb59418`.

## L200 — first-party wiring lives in five Go tables no manifest can express, so a third-party package cannot be an adapter (2026-09-05, open — proposal, needs sign-off; RE-MEASURED 2026-09-21: two hooks dissolved, one table missed, the XOR refuted)

The contract's test is a package this repo does not contain. Measured
against it, a package outside the tree can declare routes, ten row kinds and
its env needs (`L199`, the `env` block) — and still cannot become a channel
adapter without patching Go, because four facts have no manifest key:

| fact | where it lives today | what a package would need |
| --- | --- | --- |
| the service principal (`service:<name>` key + `AUTHD_SERVICE_NAME`) | `compose.adapterDaemons` | a way to say "wire me a service key" |
| which `.env` keys reach its scoped `env/<name>.env` | `compose.daemonKeys` (+ `foreignImages`) | the `env` block, read as the file's key list |
| its routes reserved as core ground | `compose.coreProxydRoutes` | first-party daemons declaring theirs in `<daemon>.yaml` |
| a dashd hub tile | `dashd/services.go` core set + `compose.ReservedRoutePaths` | derived from the route it declares |

Adding `gitd` touched five core files for exactly these (`c0594f32`:
`cmd/arizuko/github.go`, `compose/compose.go`,
`resreg/resources/github_sources.go`, `store/github_sources.go`,
`store/migrations/0088`), against `5/27`'s "adding an adapter touches no Go".
`baseDaemons` (`packages.go`) is a sixth copy of "what is core".

What a package outside the maps actually hits:

- Outside `adapterDaemons`: no `AUTHD_SERVICE_KEY` is provisioned, so `POST
  /v1/channels/register` answers 401 under a Verifier (`routd/channels.go`,
  `channelAuth`). The adapter never registers and nothing says why.
- Outside `daemonKeys`: `writeEnvFiles` writes no `env/<daemon>.env`, and the
  two ways to reach one fail differently. Every adapter fragment in
  `template/services/` hardcodes `env_file: ['../env/<name>.env']`, so a
  package that ships its own fragment fails `compose up` — compose's
  `env_file` `required` defaults to true and the file was never written. A
  daemon that takes the generated stanza instead gets `envFileFor`, whose
  `envFileName` falls back to the shared `.env` (`compose/compose.go:283-293`)
  — every instance secret, to a package from outside the tree. Neither path
  says the daemon is not in the map.
- The same class, smaller: `hasService(services, "ttsd")`
  (`compose/compose.go:788`) sets `TTS_ENABLED`/`TTS_BASE_URL` on runed by
  name, and `/dash/ttsd/` (`compose/compose.go:369`) is a core route where
  every adapter's dashboard route is a manifest row.

Nothing in the residue is imperative, so no install script is needed: every
item is a declaration the manifest lacks a key for. A `postinst` would only re-express these as code
that runs with the operator's authority, which is the second path this file's
policy forbids.

- **Severity:** medium (first-party components are not packages yet; the
  contract holds for a sidecar like litellm and fails for an adapter). The
  `envFileName` fallback raises it: the failure mode is not "no env" but "the
  shared `.env`".
- **Scope:** compose, dashd, packages
- **Affected:** any adapter shipped outside this repo; every first-party
  adapter's own wiring
- **Source:** `compose/compose.go` `adapterDaemons` (250-254), `daemonKeys`
  (98-238), `foreignImages`, `coreProxydRoutes`, `hasService` (788), the core
  route set (369); `dashd/services.go:57-67`; `routd/channels.go` `channelAuth`;
  `cmd/arizuko/packages.go` `baseDaemons`; `git show --stat c0594f32`; measured
  2026-09-05 at `02d0fbc1`
- **Status:** proposed. Dissolve the tables into per-daemon `<daemon>.yaml`:
  the `env` block becomes the scoped env file's key list (a daemon is in
  `daemonKeys` XOR declares `env`, both refuses); one manifest flag names an
  adapter needing a service principal; each core daemon's routes move to its
  own manifest and the reserved set derives from the catalog. Then
  `TestFragmentEnvFilesAreGenerated` asserts against the catalog, not the
  map. Spec: `7/25` §"What the contract needs"; whether the declaration is a
  resreg row (`5/8`'s shape) or a compose-side key in `<name>.yaml` is the
  decision to sign. Redesign — sign-off before code.
- **Fix:** `f8b82a90` (the `/dash/ttsd/` row); the rest is the redesign below

**Re-measured at HEAD 2026-09-21.** The tables above were checked one by
one; two of the facts are gone and one table was missed.

- `hasService(services, "ttsd")` does not exist. `166a0223` (2026-09-05,
  the day this entry was filed) replaced it with `ttsd.yaml` declaring
  `TTS_ENABLED` and `TTS_BASE_URL` as `env` defaults — the manifest key this
  entry asks for, already shipped for one hook. `7/25`'s table row and
  ttsd's "three name switches" are stale by that commit.
- `/dash/ttsd/` left `coreProxydRoutes` at `f8b82a90`: `ttsd.yaml` declares
  it in `proxyd_routes` like every adapter, gated by `TTS_ENABLED`, owner
  `package:ttsd`. The core row also routed `/dash/ttsd/` at a `ttsd:8080`
  that did not run whenever an operator pointed `TTS_BASE_URL` at an
  external backend. `coreProxydRoutes` now holds core daemons only.
- The `envFileName` fallback to the shared `.env` — what raised this
  entry's severity — is on no live path. Every stanza `writeSvc` renders is
  in `daemonKeys`; davd and vited declare no `env_file` at all; a package's
  fragment is included verbatim and never takes the generated stanza; and
  since `d1d857b9` (`L201`) a sourced fragment may not declare `env_file`,
  refused at install naming this entry, so "installs, then the whole
  instance fails at `up`" is now one refused install. The fallback is dead
  code and should become a refusal in the redesign, not a wider map.
- A table this entry missed: `authd/http.go` `serviceGrants`. A
  `service:<name>` principal outside it mints a token with no scope, and its
  own comment says what follows: "a missing entry means empty scope and every
  inbound 401/403s". `compose.adapterDaemons` carries "Keep in sync with
  authd serviceGrants" — two tables, one fact ("I post inbound as
  `service:<name>`"), kept in step by a comment.
- The file count. `c0594f32` touched 51 files; the ADAPTER-WIRING subset is
  `compose/compose.go` (+5: a `daemonKeys` row and an `adapterDaemons`
  row), `authd/http.go` (+1: the grants row), `Dockerfile` (+1) and
  `Makefile`. The other four files named above — `cmd/arizuko/github.go`,
  `resreg/resources/github_sources.go`, `store/github_sources.go`,
  `store/migrations/0088` — are gitd's `github_sources` feature: a core
  resource because gitd's delivery sources are routd's rows, which is the
  "mount of another owner's database" coupling `7/25` measures separately,
  not adapter wiring. The honest cost of "become an adapter" today is two
  compose rows, one authd row and an image — `7/25`'s "one map edit from
  passing", except it is three tables.
- `dashd/services.go` now lists all nine adapters as tiles; the fact holds
  (a third-party `/dash/<name>/` route works and the hub does not show it)
  over a wider table.
- **The `daemonKeys` XOR `env` refusal is refuted by ttsd**, which is in
  `daemonKeys` (an empty entry, so it gets a scoped file instead of `.env`)
  AND declares `env`. They are two declarations: `env` provisions `.env`
  keys (`generate`/`default`/`required`, spec `5/28`); `daemonKeys` says
  which `.env` keys reach the daemon's scoped file. Making the `env` block
  mean both changes its meaning for the three manifests that carry it.

**What remains, sharpened.** Three facts, five tables:

| fact                                  | tables today                                   | one-declaration shape                                                                                                                                                                                                                                                                                                                                                                                                                       |
| ------------------------------------- | ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| "I post inbound as `service:<name>`"  | `compose.adapterDaemons`, `authd.serviceGrants` | a manifest key (`service: {scopes: [messages:write]}`); generate wires the key AND emits the scopes beside `AUTHD_SERVICE_KEYS` in authd's env, so authd becomes a reader of generate's output — the second reader at a different time and shape the CLAUDE.md rule allows — and `serviceGrants` keeps only authd's own row                                                                                                                  |
| "these `.env` keys reach my env file" | `compose.daemonKeys`, `compose.foreignImages`  | a manifest key naming the keys (`env_file: [KEYS…]`), distinct from `env`, which provisions; `foreignImages` is the same key with `commonKeys` off. The redesign MUST keep two special cases or reopen closed bugs: runed's `store.EnvProfileKeys` (`M1`, the `/login` outage) and runed's NO `SECRETS_KEY` (`X3`) — a per-daemon list that a comment no longer guards is exactly how M1 happened. A daemon outside the declaration is refused, never handed `.env` |
| "show a hub tile"                     | `dashd/services.go`                            | derived from the `/dash/<name>/` rows in `proxyd_routes` (spec `6/28`: route entry IS registration), with the static list kept for core daemons only                                                                                                                                                                                                                                                                                       |

`baseDaemons` (`cmd/arizuko/packages.go`) is the sixth copy. **The deferral is
right and was re-checked 2026-09-22**: compose writes each core service by its
own call, not from a list, so "export the list from `compose`" means
restructuring `Generate`, not moving a variable.

**What did not need the redesign is the CHECK, and it shipped.** This copy
drifts QUIETLY — `packages add` warns "unsatisfied dependency" for a
`depends_on` outside the set, so a core daemon missing here makes the CLI warn
about a dependency that is always present, and an operator learns to ignore the
warning. `TestBaseDaemonsAreServicesComposeEmits` generates a compose file and
fails on any name `baseDaemons` claims that `Generate` does not emit; all ten
match today. The decision the owner signs: the declaration is
a compose-side manifest key like `env` (read at generate, before any DB
exists — the only reader that can wire a key and write a file), not a
resreg row (`packages add` applies no rows, `L199`, and generate runs where
no store is open). `env` already set that precedent; the sign-off is to
extend it to two more keys.

**One line of this is DONE, 2026-10-08.** `envFileName`'s fallback to the instance's
shared `.env` is gone: a name with no `daemonKeys` entry now gets "" instead of the file
that holds `SECRETS_KEY`, every channel token and the model credential. `Generate`
refuses a rendered compose file in which any service points `env_file` at `.env`
(`checkNoServiceReadsTheSharedEnv`), and the check reads the RENDERED yaml, so a stanza
written by a path it does not know about is still caught.

Measured honestly: **nothing took that path.** Restoring the fallback changes no rendered
service, because every name reaching `envFileFor` has an entry — `davd` and `vited` never
call it, and `vited` reads nothing at all. So this removed a dangerous DEFAULT, it did not
close a live leak. The rest of `L200` — the manifest keys that would delete the seven
tables — still adds code and still needs the design.

## L202 — a linked package runs the host checkout's compose fragment while generate reads the image's manifest, so one package can come from two versions (2026-09-05, open — needs sign-off; RE-VERIFIED 2026-09-21 at HEAD, no bounded half)

`packages add` links `services/<name>.yml` at `HOST_APP_DIR/template/services/`
(`compose.HostCatalogDir`); docker `include`s it on the host, so the live
container runs whatever the checkout holds — an uncommitted edit ships on the
next restart with no build and no tag, the class `M1` closed for `ant/`. The
`L198` fix reads a linked `.yaml` through `compose.CatalogDir`, which is the
baked `/opt/arizuko` copy where the deploy unit runs generate. The two halves
of one package therefore come from the checkout and the release respectively,
and only agree while the checkout is at the deployed tag.

Re-verified at HEAD 2026-09-21 against the live unit. `arizuko_krons.service`
runs generate as `docker run --rm -v /srv/data/arizuko_krons:/srv/data/arizuko_krons
-v /var/run/docker.sock:/var/run/docker.sock arizuko:latest arizuko generate
krons` — the data dir and the socket, never `/home/onvos/app/arizuko`. Inside
that container `CatalogDir` finds no `HOST_APP_DIR/template/services` and
answers the baked `/opt/arizuko/template/services`, so `manifestPath` reads
the release's `teled.yaml`; on the host, `docker compose up` follows
`services/teled.yml → /home/onvos/app/arizuko/template/services/teled.yml`,
the checkout's. Ten of krons' twelve fragments are such links; sloth's slakd
pair was linked today.

**There is no bounded half.** Detecting the split is a byte comparison of
the link target against the baked copy, and the process that reads the
manifest is the one process that cannot read the target — the checkout is
not mounted. The comparison exists only once the checkout is visible to
generate, and making it visible IS shape 2 or 3 below. Two weaker signals
were considered and rejected: a per-generate line saying the link is
unverifiable fires on every deploy for every linked package while nothing is
wrong (`L41`'s FROZEN line fires only while a divergence exists); and
`app-src/template/services/`, the release copy runed materialises on the
host, would let `packages add` compare at LINK time — but it exists only
after runed ran, it is a staging dir for agents, and add-time says nothing
about the checkout at the next deploy.

Three shapes, one decision:

1. **A release copy in the data dir.** Generate exports the baked
   `template/services/` to `<dataDir>/catalog/` on every run — the
   `app-src/` precedent, same image, same data dir. `packages add` and
   `relinkCatalog` link there (`HostCatalogDir` becomes
   `HOST_DATA_DIR/catalog`, host-resolvable by construction) and
   `manifestPath` reads the same dir, so both halves are the image's and
   "a release is an image" holds; `APP_SRC_DEV=1` can keep the checkout for
   the dev loop the way `appSrc` does. Cost: relink re-points every existing
   link under `HOST_APP_DIR` (mechanical — the target is compared against the
   export), and the first `add` on a fresh instance needs one generate
   before it, or falls back to a copy as it does today without `HOST_APP_DIR`.
2. **The checkout is the release.** Mount `HOST_APP_DIR` read-only into the
   generate container; `CatalogDir` then answers the checkout for both
   halves. Cost: every instance runs whatever the checkout holds, uncommitted
   edits included — the `M1` class this repo closed for `ant/`.
3. **A compare-only mount.** Mount the checkout at a path `CatalogDir` never
   consults; generate compares each link target with the baked copy and
   refuses on a byte difference. The split is detected and what runs is
   unchanged; cost: every deploy while the checkout is off the tag fails at
   ExecStartPre, which is shape 2's coupling turned into an outage.

- **Severity:** medium
- **Scope:** package delivery
- **Affected:** every catalog-linked package on every instance
- **Source:** `compose.HostCatalogDir`, `compose.CatalogDir`,
  `compose.manifestPath`; `/etc/systemd/system/arizuko_krons.service`
  ExecStartPre (2026-09-21)
- **Status:** open. Shape 1 keeps "a release is an image" and is the one
  the `app-src/` mechanism already argues for; sign-off decides.
- **Fix:**


- **ANT-ONE-SHOT-TOOL-SKIP** (MED, correctness) — the live Sloth one-sentence TLDR
  completes without `disengage`, despite the completion rule at `ant/CLAUDE.md:79`
  appearing in the resident manual. Measured at source `4c0b81d4`: private web
  turn `msg-1791987194289335014`, topic `one-shot-20261014-1414`, completes at
  14:39:25 UTC; its reply state has no `disengaged_at`. Transcript
  `40ecf6a7-a8aa-4e85-9e21-351b5e268b04.jsonl` has no tool call. The web
  message carries no mention verb; explicit-mention behavior remains unmeasured.
  **Fix:** measure instruction/tool exposure and completion behavior, then improve
  the existing declaration so an unambiguous completed task calls the stop tool.
  Do not add a second lifecycle policy or unconditional platform stop hook.
