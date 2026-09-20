# BUGS.md — open issues queue

> **Standing policy — every fix here adheres to WISDOM** (`~/.claude/CLAUDE.md`):
> minimality (smallest change at the root cause), orthogonality (one concern per
> fix, no parallel second path — amend the original), fail loud to the user on
> user-facing paths, retry only transient errors, fix causes not symptoms.
> Redesigns (new contract, changed cross-daemon control flow, auth-model or
> schema changes) stay recorded as proposals and ship only after user sign-off.

## L323 — a folder delete does its work and then reports failure, and one comment still says it rolls back (2026-09-20, open)

Split out of `L246`, whose own premise turned out to be false.

`purgeFolder` (`store/folder_scope.go:287-343`) sweeps every scope and deletes
matching items with `TryWriteEntities` per chunk. It is not a transaction and it
does not roll back — but the comment at `store/folder_scope.go:275` still says
it does, which is the kind of claim a reader acts on.

`DeleteGroup` (`store/groups.go:351-353`) then reads `removed == 0` and skips
the audit row. Both callers read that as the whole operation failing:
`cmd/arizuko/main.go:475` dies with "no group at X", and
`dashd/groups_admin.go:416` answers 404. So an operator deleting a folder whose
`groups` row is already gone gets an error AFTER the orphan rows were
successfully swept, is told nothing was done, and has no audit row to check.

- **Severity:** low — the data outcome is correct; the report is not, and it invites a second attempt that also "fails"
- **Scope:** `store/folder_scope.go`, `store/groups.go`, the two callers
- **Affected:** every instance; reachable whenever a purge runs against a missing `groups` row
- **Source:** measured 2026-09-20 at `ed81ed1f`
- **Status:** open — the comment fix is mechanical; what the callers should
  REPORT after a successful sweep with no group row is a small contract
  question, so it waits.

## ✅ FIXED 2026-09-20 L322 — webd advertises a browser SDK at `/assets/`, and no proxyd route carries that prefix (2026-09-20)

Split out of `L117`, where it was recorded as the adjacent finding.

`handleChatTokenConfig` returns `"sdk": "/assets/arizuko-client.js"`
(`webd/route_token.go:109`) and webd mounts `GET /assets/{path...}`
(`webd/server.go:79`). `coreProxydRoutes` has no `/assets/` row, and neither do
the three live `proxyd_routes` tables, so the request never leaves proxyd. On
krons it survives by falling through to `vhostRedirect` →
`/pub/krons/assets/arizuko-client.js`, which happens to hold a matching copy —
an accident of one world's pub slot, not a route.

The two facts live in two packages, which is why the advertisement outlived the
route: webd says where the SDK is, and compose decides whether that path reaches
webd.

- **Severity:** medium — the documented cross-origin widget cannot load its own SDK except on the one instance whose pub slot happens to hold a copy
- **Scope:** `compose/compose.go` (`coreProxydRoutes`)
- **Affected:** every instance
- **Status:** fixed 2026-09-20.
- **Fix:** one row, `{Path: "/assets/", Backend: "http://webd:8080", Auth: "public"}`.
  Public because a widget on a third-party page carries no session.
  `ReservedRoutePaths` derives from the same table, so the prefix is reserved
  against a package claim with no second list to keep.

  The binding is now a test rather than a convention:
  `TestAdvertisedSDKPathHasAProxydRoute` reads the `sdk` path out of webd's own
  `/config` response and requires a core route to cover it. PROVEN BY MUTATION:
  with the row removed the test reports the advertised path is unreachable.

  Docs: `ARCHITECTURE.md` path model and the `ROUTING.md` fixed-prefix table
  both carry `/assets/`.
- **Reaches a running instance only through `arizuko generate`** — `proxyd_routes` is store-backed, so the row lands on a live fleet at the next apply, not at the next image (`L45` is this class).

## ✅ FIXED 2026-09-20 L321 — webd's mock router announces a message before it persists it, so `?wait=` misses the reply about one run in ten (2026-09-20)

MEASURED. `go test ./... -count=1 -short` on 2026-09-20 returned 72 `ok` and one
`FAIL`: `webd` / `TestChatTokenPost_WaitSuffixedToken`. A rerun of the same pair
failed the OTHER test, `TestChatTokenPost_WaitHookToken`. Both report
"wait= missed the assistant frame (hub keys disagree)", which names a cause that
was not the cause.

The order inside `newMockRouterWithStore`'s `POST /v1/messages` handler is the
fault. It appended to `m.messages` first and wrote the row to the store second.
`assertWaitSeesReply` polls `mr.sent()` and plays routd's `/send` callback as
soon as the length reaches 1 — at which point the row may not be readable yet.
`handleSend` resolves the reply's hub topic with
`stRoutd.TopicByMessageID(reply_to, chat_jid)`, which returns `""` on a miss, so
the reply published on key `<jid>/` while the waiter sat on `<jid>/tw`. The
`?wait=` then burned its whole 5s budget and reported a key disagreement.

Real routd appends the message before it calls back, so no product path has this
window. The store went over the network at v0.68.0, which is what widened the
gap from nanoseconds to a visible fraction of a run.

- **Severity:** medium — the failure is intermittent and names the wrong cause, so a real hub-key regression would read as this flake and be dismissed
- **Scope:** `webd/server_test.go` (test harness only)
- **Affected:** `TestChatTokenPost_WaitSuffixedToken`, `TestChatTokenPost_WaitHookToken`
- **Source:** `./tmp/test-fin.log` (suite run 2026-09-20 15:28); reproduced with `go test ./webd/ -count=5 -run TestChatTokenPost_Wait`
- **Status:** fixed 2026-09-20.
- **Fix:** the mock persists the row BEFORE it appends to `m.messages`, so
  `sent()` is a promise the row is readable — the same order routd guarantees.
  The comment states that contract, because the two statements look independent.

  PROVEN BY MUTATION: with the old order, `-count=20` over the two tests gives
  **2 failures in 40 runs**; with the fix, **0 in 40**. The full `webd` package
  is `ok` at 40.5s.

## ✅ FIXED 2026-09-20 L320 — a chat that takes human messages and answers none is invisible to every operator surface (2026-09-20)

MEASURED on krons 2026-09-20. `telegram:group/5229089286` routes to
`rhias/nemo`. It carried **zero** bot replies from 2026-09-17 09:00 onward while
two human messages waited — 10:24 and 14:41 on 2026-09-20. Nothing reported it.

Every check reads green, and each is right on its own terms:

- `chats.errored` is clear, because no turn failed — none ran.
- The cursor is at the newest message, because `#observe` advances it.
- The circuit breaker is closed, because it counts failed runs.
- The error census is empty, because nothing logged an error.
- `/eval` check 4 compares `agent_cursor` against the newest user message and
  passes for the same reason: the cursor is not behind.

The cause was a route the operator declared — a `verb=mention` rule with an
`#observe` twin — so the platform behaved exactly as configured. That is what
makes it a monitoring gap rather than a routing bug: a folder can be configured
deaf, and no surface says a chat with live human traffic stopped answering.

Same shape as `L175`, from the other end: there a model outage was delivered as
a reply and booked as success; here a configuration makes the reply never
happen. Both are silent, and both need a check that asks about OUTCOMES rather
than errors.

- **Severity:** medium — a tenant can go unanswered for days and every health surface says the fleet is well
- **Scope:** `.claude/skills/eval/checks.md`; the census; any future alerting
- **Affected:** every instance; found on krons, and the shape exists wherever a route pair is used
- **Source:** krons `routd.messages` on `telegram:group/5229089286`, 2026-09-17 to 2026-09-20; routes `id=60` (`verb=mention`, seq 0) and `id=61` (`#observe`, seq 1)
- **Status:** fixed 2026-09-20.
- **Fix:** `/eval` check 21, "Unanswered chats (humans outran the replies)". It
  groups `routd.messages` per chat and compares the newest human message against
  the newest bot message over a 7-day window, so a chat that takes messages and
  answers none is named even when nothing errored. Check 4 stays as it is: it
  asks whether the cursor MOVED, and the whole point here is that the cursor
  moves fine.

  The check also says how to read its own output, because a NULL reply is
  correct for a quiet folder and for the `#observe` half of a route pair. It
  prints the chat's rules so the reader can tell "configured to observe" from
  "should have answered", and names the `sender=` rule that answers one person
  without opening the chat to everyone.

  VERIFIED against krons: the query returns the rhias group answered (human
  14:41, reply 14:47) and flags two `eval/*-child-delegate` chats that hold a
  human message and no reply.

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
container flags in `dyntest.go`. `EXTENDDB_ACCOUNT_ID` and `EXTENDDB_PASSWORD`
sit in `.env` for a person to type into `extenddb manage`.

`generate` PRESERVES and never mints: for each daemon it reads the key out of
`env/<daemon>.env` and carries it forward, skipping the daemon when either half
is absent (`compose/compose.go:862-872`). The comment says why — a daemon whose
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

Two further things the by-hand run hides, both blocking:

- **The certificate SAN is the compose service name `extenddb`**, which resolves
  only inside ONE instance's network. A shared server needs a SAN every instance
  can reach, and each instance's `STORE_CA` must carry that certificate.
- **The per-instance PostgreSQL role is a superuser of its own server**
  (`POSTGRES_USER: ${APP}_${FLAVOR}`, and the fragment says "the role IS the
  instance"). On a shared server that role must not exist, so the account becomes
  the only boundary and `L318` stops being cosmetic.

- **Severity:** medium — no instance is broken; a shared store server cannot be built without this
- **Scope:** `cmd/arizuko`, `compose/compose.go`, `template/services/extenddb.yml`, `INSTALL.md` § The store server
- **Affected:** any second instance on one store server; the 10.0.5.1 consolidation
- **Source:** `objstore/dynstore/env.go:16-22`; `compose/compose.go:855-872`; `template/services/extenddb.yml` header
- **Status:** open — proposal, needs sign-off on the shape: an `arizuko enrol <instance>` subcommand against a shared server, minting the account and the 19 scoped keys `L318` specifies, in one loop. It writes the identity plane arizuko today leaves to a person.
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
- **Affected:** krons, sloth, marinade (the same by-hand recipe)
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

- **Status:** open — the resource list is derivable from the table above, so the
  work is the rollout, not the design. It rewrites 19 live IAM policies on a
  running fleet, so it ships one daemon at a time against krons — `gitd` first,
  which reads `routd` and nothing else — each proven before the next.
- **Fix:**

## L317 — `/stop` no longer cancels a question typed just before it (2026-09-20, open — needs a decision)

Fallout of the `L104` fix, named rather than discovered late. `/stop` kills the
running container (`routd/steer.go` `cmdStop`) and has never meant "discard my
backlog". Before `L104` it cancelled a pending question anyway, because the
steer advanced the cursor past EVERY older row in the batch — the bug. Now
"do X" then `/stop` kills the current container and X falls through to a new
turn and runs.

The old behaviour was the right answer for `/stop` and the wrong answer for
every other command, which is why the fix went the way it did. `/stop` is the
one place where a user plainly wants the backlog dropped too.

- **Severity:** low — one command, and the user can say so in words
- **Scope:** `routd/steer.go` `cmdStop`, `routd/dispatch.go` steer branch
- **Affected:** any chat where a person cancels by `/stop` right after asking
- **Source:** named in the `L102`/`L103`/`L104` fix, 2026-09-20
- **Status:** open — needs a decision
- **Fix:** if `/stop` should clear the backlog, it advances the cursor past the whole batch it steered from, and says how many rows it dropped. That is a different contract from every other command, so it wants sign-off rather than a quiet default.

## L316 — a late follow-up ping makes the agent restate an answer the user can already see (2026-09-20, open)

MEASURED on sloth 2026-09-20, the `main/trading` Discord thread. Two mentions
landed 16 s apart: "ask codex astra why what you said is wrong" at 07:25:43 and
a bare "sloth" at 07:25:59. The second arrived 23 s into the first turn, so the
queue serialized it into a SECOND turn once the first finished — one container
each, `...134084` exiting 07:26:53 and `...222462` exiting 07:28:03.

Delivery was correct: the two replies differ in content, length (1534 vs 450
bytes) and `platform_id`, so nothing was sent twice and no dedup contract was
broken. The defect is judgement. The second reply opens "here." and recaps the
critique it had just posted, and its own text says "I ran the adversarial pass
myself just above" — the agent KNEW it had answered and answered again.

The resident prompt produced this. `ant/CLAUDE.md` § Solve said a short or
ambiguous message "earns `fetch_history` first, never silence", and § Reactions
makes a reaction the floor for an addressed message. Neither said what to do
when the history shows the question is already answered, so the agent took the
only instruction it had and wrote the answer a second time.

A bare name arriving mid-turn is a late-delivered follow-up, not a second
question. The rule now says so: react, or send one line that moves things
forward, and never restate an answer the user can already see.

- **Severity:** low — no data loss; the agent reads as broken to the person in the chat
- **Scope:** `ant/CLAUDE.md` § Solve
- **Affected:** every chat where a user pings again before a turn lands
- **Source:** sloth `routd.messages` on `discord:837270552959516682/920916162416635944`, 2026-09-20 07:25:43–07:27:40; `runed` container exits 07:26:53 and 07:28:03
- **Status:** fixed
- **Fix:** § Solve now names the already-answered case and its answer, beside the
  `fetch_history` rule that sent the agent looking. Behaviour only — no code
  path changed, and the rule reaches a live agent on the next `MIGRATION_VERSION`
  bump plus ant image rebuild.

## L315 — a catalog package's rows freeze at `add` while its files and env keep tracking the catalog, and `upgrade` reports "nothing to do" (2026-09-19, open — proposal, needs sign-off)

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
- **Status:** open — proposal
- **Fix (proposal, NOT shipped):** re-applying rows on `upgrade` is a redesign,
  not a symptom patch: `reapplyPackage` deliberately replaces only the file
  half, because overwriting the recorded row identities would strand the rows
  `remove` must later delete. Doing it properly means withdrawing the recorded
  rows and installing the new set in one transaction — a new contract for what
  `upgrade` means. The cheap half, which needs no sign-off if the owner wants
  it alone: have `upgrade`/`sync` compare the catalog manifest's current digest
  against `pkgManifestDigest` at install time and say so, rather than print
  "nothing to do".

## ✅ FIXED 2026-09-19 L314 — one catch-all `/pub/` redirect row disables publishing for every folder but `atlas` on sloth (2026-09-19)

MEASURED on sloth 2026-09-19 by `anteval --smoke`, which scored 7/9; the
`link-is-public-url` failure is this. Every public page a folder publishes is
unreachable:

```
/pub/eval/   302 -> /pub/atlas/eval/   404
/pub/main/   302 -> /pub/atlas/main/   404
/pub/coach/  302 -> /pub/atlas/coach/  404
/pub/atlas/  200
```

`routd.web_routes` holds two rows, both from 2026-06-02: `/pub/atlas/` with
`access=public`, and `/pub/` with `access=redirect` and
`redirect_to=/pub/atlas/`. The second is a vanity redirect for an
atlas-branded instance, and it covers every folder's publish prefix, so a page
written correctly to `~/public_html/` is bounced to a path that does not
exist. krons does the same branding with NARROW rows — `/pub/agents/` →
`/pub/krons/agents/`, one per page — and its `/pub/<folder>/` therefore
answers 200.

The agent behaved correctly throughout: it published to `~/public_html/`,
verified the URL, found the 302, chased it to the 404, reported the documented
public URL through the callback and named the redirect as the cause. It spent
its whole 150 s wall doing so, so the callback landed past the harness's
deadline and the case reads as a capability failure. The instance is broken,
not the agent.

Three rows in the same class deserve the same look: nothing warns that a
`redirect` row on a prefix shadows every folder underneath it, and
`set_web_route` is an agent-grantable tool, so a folder can do this to an
instance with one call.

**Fix (instance data, operator's call):** drop the `/pub/` redirect row and
express the atlas vanity as narrow per-page rows the way krons does, or move
it to `/`. **Fix (code, needs sign-off):** `set_web_route` refuses a
`redirect` row whose prefix is an ancestor of a live publishing folder's path,
and says which folder it would shadow.

- **Severity:** high — the publish surface is dead for 4 of 5 folders on a live instance
- **Scope:** sloth `routd.web_routes`; `set_web_route` validation
- **Affected:** sloth (all folders but `atlas`); any instance that sets a prefix redirect
- **Source:** `curl -sI https://sloth.quantblue.tech/pub/eval/` → 302 `/pub/atlas/eval/` → 404; `routd.web_routes` rows `/pub/` and `/pub/atlas/`, created 2026-06-02; anteval report 2026-09-19 15:47
- **Status:** fixed (code); the sloth row was dropped by hand
- **Fix:** `set_web_route` refuses a `redirect` whose `path` is a prefix of any
  group's `/pub/<folder>/`, and the refusal (409) names those folders. The guard
  sits in `webRoutesHandler`'s create branch beside the self-slot constraint it
  completes, so the agent MCP tool and the REST twin get it from the one shared
  handler; `shadowedFolders` asks proxyd's own question — a raw string prefix over
  `/pub/<folder>/`, not a segment walk — and refuses the write outright when the
  group list cannot be read, because a nil answer would pass every prefix. A
  folder still governs exactly its own root, and a prefix that covers no group
  (`/pub/agents/` → `/pub/krons/agents/`) is still a legal claim.
  `TestWebRoutesMCP_RedirectShadowsAnotherSlot` and
  `TestRESTWebRouteRedirectShadowRefused` drive the sloth row on both faces and
  assert the shadowed folders are named. Spec `5/V` §"One URL, one backing
  store" point 5 states the rule; the agent-facing `set_web_route` doc says it
  before the call.

## ✅ FIXED 2026-09-19 L313 — the repo's `create-eval` skill never loads: the global one of the same name shadows it (2026-09-19)

MEASURED 2026-09-19. `.claude/skills/create-eval/SKILL.md` is tracked in this
repo (`2840d3cd`), and the skill listing this session carries the OTHER one —
`~/.claude/skills/create-eval/SKILL.md`, whose description says "service-eval"
and which writes `.claude/skills/service-eval/SKILL.md`. Two skills answer to
one name and the global copy wins, so every edit to the repo copy changes
nothing.

They have also drifted. The repo copy has no `when_to_use` and no NOT-for
clause, and it routes findings to `.diary/`, which contradicts this project's
own Bug Triage Protocol (bugs go to `BUGS.md`; the diary is the resolution
log). The global copy carries the regression-check rule, the
threshold-from-invariants rule and the prove-it-can-fail rule that the repo
copy never learned.

The repo copy is the one that produced `.claude/skills/eval/`, so deleting it
loses nothing that is still needed; the global skill generates a project eval
skill in any repo. Recorded rather than deleted because removing a tracked
file is the user's call.

- **Severity:** low — a dead file, but an edit to it reads as a change and is not one
- **Scope:** `.claude/skills/create-eval/SKILL.md`
- **Affected:** anyone who tries to improve this repo's eval generator
- **Source:** the session skill listing 2026-09-19 (global description shown); `git log -1 -- .claude/skills/create-eval/SKILL.md` → `2840d3cd`
- **Status:** fixed
- **Fix:** `.claude/skills/create-eval/SKILL.md` is deleted. The global
  `~/.claude/skills/create-eval/` is the one that loads and the one that
  carries the regression-check, threshold-from-invariants and
  prove-it-can-fail rules; it generates a project eval skill in any repo, so
  the repo needed no copy of its own. The lesson this eval produced went to
  the live skill instead: keep a generated `SKILL.md` under 200 lines and put
  the checks and the gates in siblings, because this project's own grew to
  678 lines in one file.

## ✅ FIXED 2026-09-19 L312 — creating a group fires all five seeded memory tasks at once, and the root session they leave aborts the next `anteval` run (2026-09-19)

MEASURED on sloth 2026-09-19. `arizuko group sloth add web:eval eval` ran at
15:02:37. Between 15:03:55 and 15:06:06 the folder ran **five turns** —
`ti-eval-mem-0` … `ti-eval-mem-4`, one every ~35 s — although the rows those
tasks wrote say `next_run` = `2026-09-20T02:00Z`, `2026-09-21T03:00Z` and
`2026-10-01T04:00Z`. A brand-new group pays five LLM turns before anybody
sends it a message.

The cause is one line: `store/tasks.go:535` seeds every default task with
`NextRun: &now`, so `timed` reads five overdue tasks on its next sweep and
fires them, then computes the real next run. `timed/main.go:32`
(`computeNextRun`) already turns a cron expression into the next instant, so
the first `NextRun` has a correct value available at seed time; the seeder
does not read it.

The second cost is the one that blocks work. Each of those turns runs on the
ROOT topic and persists a root session, and `anteval`'s per-case baseline
refuses to inject against one (`specs/5/9` § Session isolation). The first
`anteval --smoke` on sloth aborted with `root session
a6ee16b5-216f-4a32-94b5-2453caa86066 survived /new for 1m30s` — the storm was
still running. So the documented setup order (create the eval folder, then
run the gate) cannot work on any instance.

**Fix (one concern, amends the original):** give `SeedDefaultTasks` the first
`NextRun` from the task's own cron instead of `now`. `computeNextRun` lives in
`timed` and `store` must not import a daemon, so the helper moves to a place
both read — one declaration, many readers.

- **Severity:** medium — five wasted turns per new group, and the first eval run after a group is created always aborts
- **Scope:** `store/tasks.go:525-554` (`SeedDefaultTasks`), `timed/main.go:32`
- **Affected:** every new group on every instance; `anteval` on a fresh eval folder
- **Source:** sloth `groups/eval/logs/container-2026-09-19T15-0{3,4,5,6}-*.log`; `routd.scheduled_tasks` rows `eval-mem-0..4`; `store/tasks.go:535`
- **Status:** fixed (code); live rows keep the `now` they were seeded with until they next fire
- **Fix:** `SeedDefaultTasks` writes each task's first `next_run` from the task's
  OWN cron, so a new group is not already due. The cron-to-next-instant answer
  moved to `core.NextRun` (`core/cron.go`) — one declaration, and every writer of
  `next_run` reads it: the seeder, timed's `computeNextRun`, dashd's
  `taskNextRun` and routd's `parseTaskSchedule`, which each held a copy of the
  same parser. `core.Timezone()` is the one resolution of `TZ` beside it, so the
  first run and every later run are read in the same zone. `store` gained no
  second mechanism and no flag: the value that was wrong is the value that
  changed. `TestSeedDefaultTasks_FirstRunFollowsItsCron` asserts each seeded
  `next_run` matches the wall-clock fields its expression names and that
  `DueTasks` claims NOTHING from a group that was just created — the reader that
  fired the five. The cron tests moved with the declaration
  (`core/cron_test.go`). Spec `6/30` §"One cron parser" replaces its
  three-copies paragraph.

## ✅ FIXED 2026-09-19 L311 — a second instance's `env/operator.env` points the operator CLI at a name only containers resolve, and it is world-readable (2026-09-19)

MEASURED on sloth 2026-09-19. Every `arizuko <cmd> sloth` died with
`open routd's store: … dial tcp: lookup extenddb on 127.0.0.53:53: server
misbehaving`. `env/operator.env` carried `DYNAMODB_URL=https://extenddb:18443`
— the compose service name, which resolves inside the instance network and
nowhere else. The operator runs on the host. krons's copy carries
`https://127.0.0.1:18443` and a comment that says exactly this, so the file is
hand-written and the fix travelled with the person, not with the code.

The port makes it worse. `template/services/extenddb.yml:34` publishes
`127.0.0.1:18443:18443` as a literal, so the second instance on a host
collides with the first. sloth's fragment was hand-edited to
`127.0.0.1:18444:18443`, and a hand-edited fragment is frozen from the
catalog from then on (`compose/fragments.go`) — so sloth never takes another
store change. The host address is not derivable from `.env` either: nothing
records which port this instance published.

The same file was mode 0644 holding `AWS_SECRET_ACCESS_KEY` for the store
account that reaches every owner's tables. krons's is 0600. Any host user
could read sloth's.

Both instance files are corrected in place (sloth now
`https://127.0.0.1:18444`, mode 0600). The code cause is untouched: nothing
writes `operator.env`, and the published port is a literal.

**Fix (needs sign-off — it adds a generated file):** `arizuko generate` writes
`env/operator.env` from the same facts it writes every other env file, with
the host address built from the port it published, at mode 0600. The
published port then belongs in the manifest as a per-instance `env` key with a
`generate`d default, the way every other colliding value already does.

- **Severity:** high — the whole operator CLI is dead on a second instance, and a store credential is world-readable
- **Scope:** `template/services/extenddb.yml:34`, `template/services/extenddb.yaml`, `compose/` (no writer for `env/operator.env`)
- **Affected:** every instance after the first on a host; sloth, marinade
- **Source:** `/srv/data/arizuko_sloth/env/operator.env` (2026-09-18 19:47, mode 0644); `sudo docker port arizuko_extenddb_sloth` → `127.0.0.1:18444`
- **Status:** fixed; both live files were also corrected by hand
- **Fix:** `arizuko generate` now writes `env/operator.env`
  (`compose.writeOperatorEnv`), pointing `DYNAMODB_URL` at the loopback address
  of the port THIS instance publishes, at mode 0600. The credential lines are
  minted by a by-hand `extenddb init` and cannot be generated, so the file is
  never rewritten wholesale: one URL line is edited and every other byte,
  comments included, is kept, and the line is touched ONLY when it is absent or
  names the in-network service — an endpoint the operator chose is theirs and is
  left alone. The published port left the fragment for `STORE_HOST_PORT` in the
  manifest's `env` block, so a second instance takes its own port from `.env`
  instead of an edit that freezes the fragment. Verified against copies of the
  two live files: both come out byte-identical and at 0600.
  `TestOperatorEnvKeepsAnOperatorAddressedFile` and
  `TestOperatorEnvReplacesTheInNetworkName` pin those two cases.

  **Residual, by design:** `STORE_HOST_PORT` is a `default:` key, and a
  `default` fills the run's env without landing in `.env`, so a second instance
  still collides on 18443 until its operator sets the key. INSTALL.md § The
  store server now says so at the step where it matters. Making the port mint
  itself per instance is a larger change to how `default` works and is not in
  this fix.

## ✅ FIXED 2026-09-19 L310 — `dyntest` leaks one store container per interrupted test binary; eleven were alive for 32 hours (2026-09-19)

MEASURED on the dev host 2026-09-19: eleven `arizuko-dynstore-<pid>`
containers, image `ghcr.io/extenddb/extenddb-dev:0.1.11`, up 31–32 hours. Each
holds a published loopback port and a SQLite file.

`Shutdown()` (`objstore/dynstore/dyntest/dyntest.go:109`) removes the
container this process started, and `Main` calls it after `m.Run()` returns.
Nothing calls it when the binary does not return: `go test -timeout` kills the
process, a panic in `TestMain`, and Ctrl-C all skip it. `start()` reaps only
`arizuko-dynstore-<own pid>` (`dyntest.go:189-190`), and a pid does not repeat
soon, so every orphan survives every later run.

The comment above `Shutdown` already records the same symptom — "seven were
found running a day after their suite finished". The fix that followed made
the clean exit tidy and left the interrupted one alone, so the count grew.

**Fix (one concern, amends the original):** start the container with a label
that carries the creating pid, and reap every labelled container whose pid is
gone in `start()` — the line that already runs `docker rm -f <name>`. The
orphan then cannot outlive the next run, whatever killed its creator.

- **Severity:** low — no production path, but the host accumulates containers and ports until someone notices
- **Scope:** `objstore/dynstore/dyntest/dyntest.go:107-116` (`Shutdown`), `:189-190` (`start`)
- **Affected:** every developer host that runs the store test suites
- **Source:** `docker ps --filter name=arizuko-dynstore-` 2026-09-19, 11 rows, 31–32 h uptime
- **Status:** fixed; the eleven measured orphans carried no label, so the reap could not take them — removed by hand 2026-09-19, each pid checked dead first
- **Fix:** the container is started with a label naming the pid that started it
  (`arizuko-dynstore-pid`), and `start()` removes every labelled container whose
  process the kernel no longer knows before it starts its own — the line that
  already removed this pid's container by name. `Shutdown` stays the tidy path
  for a process that returns; the reap is what covers one that does not, so an
  orphan cannot outlive the next run whatever killed its creator. Liveness is
  signal zero: a sibling test binary under `go test ./...` holds a live pid and
  is spared, and a permission error counts as running. The name-scoped
  `docker rm -f` stays for the one case the reap declines — a recycled pid that
  is this process. `TestOrphansTakesTheDeadPidOnly` pins the choice against a
  real exited pid and a real live one, `TestOrphansKeepsARowItCannotRead` the
  row it cannot parse, and `TestReapRemovesADeadPidContainer` drives the whole
  reap through docker, which is the half that proves the format string.
  Measured end to end 2026-09-19: a suite killed with SIGKILL left
  `arizuko-dynstore-1825964` up, and the next run removed it.

## L309 — the docs mount is gated on `web:publish`, so 49 of 55 folders ship a skill that names a path they do not have (2026-09-19, open — proposal, needs sign-off)

MEASURED on krons 2026-09-19. Every agent carries the `arizuko` skill, whose
whole job is to answer "how does arizuko do X" from
`/var/lib/www/arizuko/`. `container/runner.go:641` mounts that tree only
`if in.WebPublish`, and exactly **6 of 55 folders** hold `web:publish`
(`fiu`, `adshaus`, `eval`, `happy`, `krons`, `rhias/nemo`). For the other 49 the
skill names a directory that is not there, and the agent has no other copy of the
documentation.

The gate reads as deliberate but is not: the mount was added so a publishing
folder could see what it published, and reading the docs is a second, unrelated
purpose that inherited the condition. `/var/lib/www` is `<web-dir>/pub`, which
proxyd already serves to the public at `/pub/`, so mounting it read-only
discloses nothing a browser cannot fetch anonymously.

**The change is two lines** — drop `in.WebPublish` from the `/var/lib/www`
mount and leave it on the `~/public_html` read-write mount below, which
separates "may read the public web root" from "may publish" and is the
orthogonal split. It is recorded rather than shipped because it changes what
every turn container mounts, and a sandbox contract change takes sign-off
(root `CLAUDE.md` § System-change discipline).

- **Severity:** medium — the documentation an agent is told to consult is absent for 89% of folders
- **Scope:** `container/runner.go:641`, `ant/skills/arizuko/SKILL.md`
- **Affected:** every folder without `web:publish`, every instance
- **Source:** `container/runner.go:641`; `routd.acl` holds 6 `web:publish` rows against 55 groups
- **Status:** open — proposal, needs sign-off
- **Fix:**

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
- **Status:** open — needs the operator to choose the grant or the case
- **Fix:**

## ✅ FIXED 2026-09-19 L306 — `is_group` reads 0 on live multi-party chats (2026-09-18)

MEASURED on all three instances. Every one of these carries a multi-party JID
and a live message stream, and `chats.is_group` is 0:

```
marinade  telegram:group/1003805633088   37 msgs/30d, 11 senders
marinade  telegram:group/5012430759      37 msgs/30d,  8 senders
sloth     telegram:group/5125571890      46 msgs/30d
sloth     telegram:group/5282578105
sloth     discord:837270552959516682/{1512842813874639048,1514875125785296916,
          1514907285451968533,1514908015936143460}
```

The JID kind and the flag come from ONE expression at the producer —
`teled/bot.go` `chatJIDFromID(id)` returns `telegram:group/...` exactly when
`id < 0`, and the same `id < 0` sets `IsGroup`. They cannot disagree at write
time, so a row where they disagree was written by a path that does not set the
field, and `routd/server.go:598` `SetChatIsGroup` then stores false as an
absent attribute — indistinguishable from never written.

Consumer: `routd/turns.go:107` `ThreadReplies(tc.Folder, s.db.ChatIsGroup(jid))`
picks the reply shape from it, so a group is treated as a direct chat.

The producer is `timed`. `fireSplit` (`timed/split.go`) posts one
`apiv1.Message` per due task carrying id, chat_jid, sender, content and
timestamp — and no classification, because a scheduler posts INTO a chat it
does not observe. routd read that silence as the assertion "DM" and cleared the
flag on every fire, so each listed chat is one a scheduled task fires into and
whose last event was that fire. `anteval`'s `Inject` is the same shape.

**Fix:** `apiv1.Message.IsGroup` is a `*bool` and routd's ingress writes
`chats.is_group` only for a caller that STATES one. A classification is what
the adapter OBSERVES; a poster that observes nothing now says nothing, and a
bare bool can only lie for it. `chanlib.InboundMsg.IsGroup` drops `omitempty`
so an adapter's false still reaches the wire — that answer is what takes a
wrong `true` back off a chat routd once saw misclassified, and without it the
third state would be decorative. The ingress no longer needs the flag write to
open the chat item: `PutMessage` on the line above already does
(`store/messages.go` §"ONE WRITE OPENS THE CHAT AND FLAGS IT").

`timed` is deliberately NOT taught to read the JID grammar. That would be a
second declaration of a fact every adapter already makes, and it answers
Discord wrong: a group DM is multi-party and its JID is `discord:dm/<channel>`.

`TestCronFireLeavesTheChatClassification` drives the exact `fireSplit` payload
through the ingress and fails on the old behaviour;
`TestCronFireOpensAnUnclassifiedChat` pins the open that no longer rides the
flag write; `TestInboundCarriesTheClassificationEvenWhenFalse` pins the wire.

The listed rows repair themselves: an adapter states the classification on
every inbound, so each chat is re-flagged by its next message once the build
ships. Nothing is written to a live row by hand.

## L307 — two Slack channels on marinade carry traffic and match no route (2026-09-18, open)

`slack:T4PNSRSP7/channel/C4P090XU1` (6 msgs/30d) and
`.../C4PMARTD2` (2 msgs/30d) reach routd and resolve to nothing: marinade's
table routes only `C0B4JMQ8X89`, and there is no Slack wildcard the way there
is for Discord (`chat_jid=discord:*/*`). The messages are stored and dropped.

Either add the `chat_jid=slack:*/channel/* verb=mention` + `#observe` wildcard
pair marinade already has for Discord, or the operator routes each channel.
This is a data decision, not a code fix.

## L305 — two live threads in one Discord channel: the second one's mention is never answered (2026-09-18, open — proposal, needs sign-off)

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

## ✅ FIXED 2026-09-16 L302 — folder-scope verdicts named tables no owner declares any more (2026-09-14)

Nineteen `folderScopeKeep` entries named entities no owner declares: the five
`messages_fts*` shadow tables, `migrations` and `sqlite_sequence` went with the
SQL driver, and `cli_audit`, `ipc_audit`, `cost_log`, `cost_log_archive`,
`chat_sessions`, `auth_sessions`, `identities`, `identity_claims`,
`identity_codes`, `idempotency_keys`, `router_state` and `config_meta` were
already retired. The list called itself total and was total over a set that
included ghosts, so a reader could not tell a deliberate cross-owner verdict
from a leftover.

- **Status:** fixed in v0.68.0
- **Fix:** the nineteen go. `auth_users` (authd's) and `session_log` (runed's)
  stay and say why — the purge is one batch on routd's tables and reaches no
  other owner, so "no" is the answer a reader needs stated.
  `TestFolderScope_ClassifiesNothingUndeclared` is the lock on the direction the
  completeness test did not cover.

## ✅ FIXED 2026-09-14 L300 — `arizuko create` needed a store server that cannot exist yet (2026-09-14)

MEASURED on this tree. `create` ended by opening routd to seed group `main`, its
ACL floor and its default tasks. With SQLite gone that open dials
`DYNAMODB_URL`, and on a NEW instance there is no endpoint to dial:
`INSTALL.md` brings the store server up AFTER `create`, and it has to, because
`arizuko packages <name> add extenddb` and `arizuko generate` both need the
instance directory `create` makes. So a fresh install failed at the first
command, and the order could not simply be swapped.

An existing instance was never affected — krons already holds both the endpoint
and the `main` row.

- **Severity:** high — blocked every new instance on v0.68.0; no effect on a
  running one
- **Scope:** `cmd/arizuko/main.go` (`cmdCreate`), `routd/db.go` (`Open`),
  `store/seed.go`, `compose/compose.go`, `INSTALL.md` § Create instance
- **Affected:** a fresh `arizuko create` on v0.68.0
- **Source:** `cmd/arizuko/main.go` `mustOpenRoutd` → `routd.Open` →
  `store.Handle` → `dynstore.FromEnv`, which errors when `DYNAMODB_URL` is unset
- **Status:** fixed — signed off 2026-09-14
- **Fix:** the two concerns are split by owner. `create` makes the instance's
  FILES — directory, `.env`, compose, the group's own tree — and DECLARES the
  operator's `--product` choice as `ARIZUKO_DEFAULT_PRODUCT`. Every ROW is its
  owner daemon's: `store.SeedDefaultGroup` writes the `main` row and its default
  tasks into an empty `groups`, beside the ACL and egress floors routd already
  seeds at boot. One writer of those rows instead of two, and one declaration of
  the product with compose and routd as its readers.

## ✅ FIXED 2026-09-16 L304 — `chats.errored` was set and never cleared, so it reported a past minute as a present state (2026-09-16)

MEASURED on krons: two chats carried `errored=1` from a rate limit at 02:02, and
a full sweep — every chat's newest non-bot message against its `agent_cursor` —
found ZERO chats with an unanswered user message. The flag had one writer and no
clearer, so the count was monotonic for the life of an instance.

- **Status:** fixed in v0.68.1
- **Fix:** `SetAgentCursor` clears the flag in the same write. The advance past
  the failed batch is what `MarkChatErrored`'s own comment names as the
  starvation guard, so it is also the evidence the chat recovered.

## ✅ FIXED 2026-09-16 L303 — whapd EXITED on an invalidated WhatsApp session, so the surface an operator re-pairs through went first (2026-09-16)

MEASURED on krons: `arizuko_whapd_krons` had restarted **2,820 times**, and the
last hour of the journal held exactly three distinct errors, all this one cause —
56 x `session invalidated`, 121 x `channel health failed ... lookup whapd: server
misbehaving`, 10 x `channel auto-deregistered`.

A server-side logout is not a crash and it is not transient, so there was nothing
to retry; and `/v1/pair/status` and `/v1/pair/start` live in that process, so the
exit took away the one surface that fixes it.

- **Status:** fixed in v0.68.1
- **Fix:** the branch marks the bot unauthenticated, sets `suspended` to stop the
  reconnect loop, and RETURNS. `/health` answers 503 disconnected and
  `/v1/pair/status` answers `unauthenticated`, which is where an operator looks.
  Nothing restarts and nothing floods.
- **Still operator work:** the session itself needs the pairing-code path with
  the user's phone; no code change replaces it.

## ✅ FIXED 2026-09-14 L301 — a `*sql.Tx` parameter was threaded through nineteen functions and could only ever be nil (2026-09-14)

`store.TryWriteEntitiesInTx` returned an error the moment its `tx` was not nil,
because the one driver left opens none. Every caller passed nil, and the
parameter survived through eight `store` files plus `resreg/engine.go`, along
with the `database/sql` import that only existed to name the type.

- **Status:** fixed in v0.68.0
- **Fix:** the parameter goes, and with it the four write doors that existed only
  to choose between a transaction and no transaction — `WriteEntities` and
  `TryWriteEntities` are the pair. `objStore(tx)` and `reads(tx)` went too: both
  already ignored the argument. resreg's column-shaped reader followed, since
  `Querier`, `filterSQL`, `whereFilter`, `orderBy` and `scanTargets` had no
  caller once the entity path became the only path.
- **Measured after:** `go list -deps ./...` reaches `database/sql` from nothing;
  what remains is `database/sql/driver` and `database/sql/internal`, reached by
  `google/uuid` for its `driver.Valuer`. `modernc.org/sqlite` is out of `go.mod`.

## ✅ FIXED 2026-09-14 L299 — the agent was told to search for deferred tools, the runtime has no such tool, and from one refusal it reported the platform as broken (2026-09-14)

MEASURED, and the first reading of this entry was wrong. A subagent in krons' 09:23 turn
reported *"Web tools are hard-blocked in this environment — both WebFetch and WebSearch
return 'user doesn't want to take this action' on every call"*. Probed live: a turn that
spawns one subagent with one `WebSearch` gets eight links back, and the parent's search
works too. The turn transcript holds exactly ONE refusal, and it is not a web tool —
it is `ToolSearch`.

`ToolSearch` does not exist in this runtime. The SDK's tool list carries `WebSearch`,
`WebFetch`, `Agent`, `Bash` and the rest, and no tool search; `injectMcpEnv` declares ONE
server (`arizuko`) with `alwaysLoad: true`, so nothing is deferred at all. Two of
arizuko's own files told the agent otherwise: `ant/CLAUDE.md` § Skills and tools said
third-party tools "are deferred behind the **Tool Search Tool**" and instructed it to
"Search for it", and `ant/README.md` said connectors load from `~/.claude/settings.json`
— a path `injectMcpEnv` deliberately does not read (BUGS J1, F76).

So the agent obeyed a false instruction, met the generic refusal a missing tool returns,
and generalised it into a claim about the platform, in a chat a person reads.

- **Severity:** high — the instruction manufactured the confusion, and the output is a
  confident false statement about the product
- **Scope:** `ant/CLAUDE.md`, `ant/README.md`
- **Affected:** every agent on every instance; measured on krons
- **Source:** `journalctl` 2026-09-14 09:23:53–09:24:24; transcript
  `8f44dea3-….jsonl` (one refusal, on `ToolSearch`); the live probe above
- **Status:** fixed — the turn's tool list is complete and the agent is told so
- **Fix:** `ant/CLAUDE.md` now states there is no tool search, that a capability it
  cannot see is an unmet grant to ask for, and that one refused call proves nothing about
  a capability — report the call and the text verbatim, never a platform claim.
  `ant/README.md` describes the one server that actually serves connectors.

## ✅ FIXED 2026-09-14 L298 — a running turn's own rows cleared its busy backoff, so one cron fire re-dispatched itself fifteen times (2026-09-14)

MEASURED on krons' 03:00 cron window (`journalctl`, 02:55–06:00): 22 tasks fired, 22
containers spawned, 22 turns completed — and **102 `dispatch run` lines**, up to 15 for a
single turn id (`sched-rhias-mem-1@2026-09-14T03:00:00Z` 15, `sched-krons/public/marble-mem-1`
13, `sched-task-1776079795417-0adb00d2` 12). Each attempt pays for a capability resolve,
an MCP socket and a prompt build, which is the cost `L55` recorded.

The backoff that exists to stop this was keyed on the chat's NEWEST ROW
(`processGroupMessages`, `noteBusy(chatJID, last.ID)`), and its comment called a bot row
"new activity worth resetting on". While a turn runs it writes its own rows — progress
notes, replies — so the turn cleared the backoff that was holding it back, over and over.
A cron turn is the loud case because it runs for minutes and talks while it works.

- **Severity:** high — wasted work at every poll, and a cron that reads as hanging
- **Scope:** `routd/dispatch.go`
- **Affected:** krons, measured; every instance with a talkative long turn
- **Source:** 102 dispatches for 22 spawns, 2026-09-14 03:00 window
- **Status:** fixed — the key is the newest USER row, which the running turn cannot move
- **Fix:** `routd/dispatch.go`; a person's next message still clears the backoff at once,
  and `TestABotRowDoesNotClearTheBusyBackoff` pins the other half

## ✅ FIXED 2026-09-13 L295 — every `mustOpenRoutd` CLI open starts a full network reindex and then cancels it, logging a false "build the ranked message index" ERROR on every restart and every admin write (2026-09-13)

MEASURED on krons across two restarts (11:20 and 12:06): `journalctl` shows exactly one
`ERROR build the ranked message index err="..."` per restart, from the `arizuko generate
krons` `ExecStartPre` process (PID traced in the log: `chown store dir` warning and
`proxyd routes: N written` — `syncProxydRoutes`'s own stdout — share the PID with the
index error, seconds apart).

`syncProxydRoutes` (`cmd/arizuko/routes.go:27`) and ten other CLI call sites
(`cmd/arizuko/packages.go:277` `mustOpenRoutd`, used by `budget.go`, `skills.go`,
`main.go`, `products.go`, `routes.go` twice, `packages.go` four times) open a full
`*routd.DB` for a lightweight administrative read or write. On a served instance
`routd.OpenAt` → `openServed` → `OpenOver` (`routd/db.go:997`) unconditionally calls
`startSearch()`, which walks every chat partition over the network to rebuild the BM25
index (`routd/search.go:43`). The CLI command finishes in milliseconds and its
`defer rdb.Close()` calls `d.searchStop()` (`routd/db.go:214`), cancelling the walk
before it gets anywhere — `msgsearch.Index.Build` records that cancellation as
`ix.failed` and `slog.Error`s it (`routd/search.go:52`).

This is a false alarm for the running daemon — verified live: `GET /v1/messages/find`
answered `200` with a real ranked hit off the routd container's own separately-built
index, which finishes because nothing closes it early. But `msgsearch.Index.failed` is
sticky (`routd/msgsearch/msgsearch.go:308`, no reset on retry), so any CLI-owned `*Index`
that fails this way answers every future search with `context canceled` for the rest of
that process's life — harmless only because the CLI process exits right after. The
recurring cost is real: a guaranteed ERROR-severity log line on every single
`arizuko generate` (an `ExecStartPre` on every `systemctl restart`) and on every other
`mustOpenRoutd` call, forever, on a served instance — exactly the kind of noise an
error-census misreads as a live index failure, and a wasted full-corpus network read
that the CLI command never needed.

**Fixed 2026-09-13.** `StartSearch` is exported and routd's own main calls it; an opener that only reads or writes rows never starts the walk, and a process with no index refuses a search rather than answering it empty.

- **Severity:** medium — no functional break (the daemon's own index builds fine), but
  a guaranteed false-positive ERROR on every restart and administrative write, and a
  discarded full-corpus network read each time
- **Scope:** `cmd/arizuko/packages.go` `mustOpenRoutd`; `routd/db.go` `OpenOver`;
  `routd/search.go` `startSearch`
- **Affected:** every served instance; krons confirmed, both restarts today
- **Source:** `routd/search.go:43-58`; `routd/db.go:997-999`, `:210-218`;
  `routd/msgsearch/msgsearch.go:153-163`, `:306-309`; journal PID trace at 11:20:27 and
  12:07:20
- **Status:** fixed — needs a decision on the fix shape
- **Fix (proposal):** a CLI-facing open that never starts the search walk (the ten
  `mustOpenRoutd` sites write routes/packages/budgets/skills, none reads search), so the
  daemon is the only opener that pays for and needs the index. Needs sign-off: whether
  that is a new `routd.OpenOverNoSearch`/flag, or lazily starting the walk on first
  `Search()` call instead of at every open.

## ✅ FIXED 2026-09-13 L296 — extenddb's PostgreSQL healthcheck probes a database that does not exist, flooding one FATAL every ~10s forever (2026-09-13)

MEASURED on krons: 284 occurrences of `FATAL: database "arizuko_krons" does not exist`
in the ~50 minutes since cutover, continuous across both restarts, roughly one every 10
seconds — the healthcheck's own interval.

`template/services/extenddb.yml`'s `extenddb_db` healthcheck runs
`pg_isready -U ${APP}_${FLAVOR}` with no `-d`. libpq defaults `PGDATABASE` to the
username when unset, so `pg_isready` asks for a database literally named
`arizuko_krons` — but `POSTGRES_DB` is explicitly `postgres` two lines above, so that
database has never existed. `pg_isready` still reports the server as accepting
connections (the container shows `healthy` throughout), so nothing is functionally
broken; the cost is pure log noise, at `FATAL` severity, forever, on every instance
running this fragment — the single largest line item in this census by count, and the
kind of flood that trains a log-based alert to ignore `FATAL` from this container.

**Fixed 2026-09-13.** The healthcheck names `postgres`, which is the database the server creates.

- **Severity:** low — cosmetic/noise only; healthcheck still passes and reports true status
- **Scope:** `template/services/extenddb.yml` `extenddb_db.healthcheck`
- **Affected:** every served instance (krons confirmed; same fragment ships to all)
- **Source:** `template/services/extenddb.yml` `pg_isready -U ${APP}_${FLAVOR}`;
  284 occurrences 11:20:35–12:09:19 in `journalctl -u arizuko_krons`
- **Status:** fixed
- **Fix:** `aac2f358` — add `-d postgres` (or a dedicated healthcheck database) to the `pg_isready`
  invocation.

## ✅ FIXED 2026-09-13 L297 — `CreateTable` is re-declared for every owned entity on every daemon boot, and ExtendDB's Postgres backend logs a raw unique-constraint ERROR for each one that already exists (2026-09-13)

MEASURED on krons: 118 occurrences of
`ERROR: duplicate key value violates unique constraint "tables_pkey"` across two
restarts (~59 per restart), all clustered in the first ~10 seconds after `extenddb_db`
comes up, one per already-existing table.

`store.PrepareOwner` → `EnsureEntitySet` (`store/served.go:33`) calls `CreateTable` for
every entity an owner declares, on every boot, and treats "already exists" as success at
the Go layer (`store/served.go`'s own comment: "asking twice is a no-op"). ExtendDB
implements that idempotency by attempting the catalog insert and catching the conflict,
which means PostgreSQL itself logs a real `ERROR`-severity line for each of routd's,
onbod's and authd's already-existing tables on every single restart — a second daemon
never touches another's tables here (each owner creates only its own), so this is not
the `L119`/federation class, just an idempotent path implemented as attempt-and-catch
rather than check-first or `ON CONFLICT DO NOTHING`. Same shape as `L296`: harmless,
but it floods `ERROR` severity on a cadence (every restart, forever) that makes
error-census-by-log-level a bad tool for this stack until it's fixed.

**Fixed 2026-09-13.** `CreateEntity` asks with `DescribeTable` and creates only what is missing.

- **Severity:** low — no functional effect measured; log-noise only, but at `ERROR`
  severity where `L296` is `FATAL`, so together they dominate any severity-based census
- **Scope:** `store/served.go` `EnsureEntitySet`/`PrepareOwner`; ExtendDB's `CreateTable`
  handling
- **Affected:** every served instance, every restart
- **Source:** `store/served.go:29-42`; 118 occurrences 11:20:28–12:07:32 in
  `journalctl -u arizuko_krons`
- **Status:** fixed — needs a decision on which side owns the fix (arizuko calls
  `DescribeTable` before `CreateTable` instead of relying on the conflict, or the fix is
  requested upstream in ExtendDB to answer `ResourceInUseException` without a Postgres
  ERROR)
- **Fix:** `aac2f358` —

## ✅ FIXED 2026-09-13 L292 — an open with nothing to change was refused by the endpoint, so every agent reply failed on a served instance (2026-09-13)

MEASURED through the conformance suite: a `Merge` naming no attribute, no initial
value and no removal rendered an empty `UpdateExpression`, and the server answered
`ValidationException: ExpressionAttributeNames must not be empty`. `store.PutMessage`
sends exactly that write per message — a chat that carries no real inbound is opened
and nothing more — and it rides in the batch that writes the message, so the refusal
took the reply with it. `isRealInbound` is false for a bot row, which made every
agent reply the failing case.

The SQL driver has always implemented it (`INSERT … ON CONFLICT DO NOTHING`), so the
two drivers disagreed and no conformance body asked.

- **Severity:** critical — no agent reply lands on a served instance
- **Scope:** `objstore/dynstore/batch.go`, `objstore/conformance/writes.go`
- **Affected:** any instance on the store server; found before krons moved
- **Source:** `objstore/dynstore/batch.go` `writeAction`, the `Merge` arm
- **Status:** fixed — the expression takes away a name the model never writes
- **Fix:** `72e61f2b`; pinned by `AnOpenWithNothingToChangeCreatesTheItem` on both
  drivers, green against ExtendDB and against DynamoDB Local

## ✅ FIXED 2026-09-13 L294 — the CLI wrote to the file while every daemon read the server (2026-09-13)

MEASURED on krons minutes after its flip: `arizuko send krons eval "…"` printed
`injected: cli-… → web:eval`, `routd.db`'s mtime moved, and the message never appeared
in any daemon's read — `GET /v1/messages/find` and `/v1/messages/inspect` both answered
from the server, where the row was not. `arizuko route add` landed in the same place, so
a route the operator could list did not exist for proxyd.

The cause is a door that branched on the PROCESS environment while only some subcommands
put the instance's environment there: `store.OpenOwner` reads `DYNAMODB_URL` through
`store.Served()`, `core.LoadConfigFrom` loads `.env`, and `send`, `route` and their
siblings resolved the data directory without ever calling it. So the branch silently took
the file path on a served instance — a split-brain writer, the exact shape
`identity_derived_not_read` warns about, with the environment standing in for the record.

- **Severity:** critical — an operator write is accepted, reported as done and never seen
- **Scope:** `cmd/arizuko/main.go` `instanceDir`, `core/config.go`
- **Affected:** krons, measured; every served instance
- **Source:** `routd.db` mtime 11:28:55 against a fleet started 11:20
- **Status:** fixed — resolving an instance's directory loads that instance's environment
- **Fix:** `core.LoadInstanceEnv` is the one declaration and `instanceDir` its second
  reader, so every subcommand that names an instance sees the same store the daemons do

## L293 — the store server answers 500 under a full host disk, and says nothing about the disk (2026-09-13, open)

MEASURED on krons' own server (ExtendDB 0.1.11 over PostgreSQL 17) during the copy: a
`Query` of `routd.audit_log` and then a `TransactWriteItems` into it both failed with
`InternalServerError` and a RequestID, so the server answered rather than the connection
breaking. The host root filesystem was at **100%, 320K free** at that moment
(`/srv` 103G, `/home` 86G, `/var` 43G), which is the state PostgreSQL cannot write in.

The server's answer names nothing an operator can act on: not the disk, not the write,
not the database. A copy or an operator page therefore reads "the server is broken"
when the true fact is "the host is full".

- **Severity:** high — the failure is indistinguishable from a server defect, and it
  reached a migration mid-run
- **Scope:** the store server's error surface; `objstore/dynstore` error mapping
- **Affected:** krons, measured
- **Source:** RequestIDs `0588dbe2-47b5-4751-aa1d-9ed92b1abcfd` (read),
  `ca8107bf-03fb-4914-b23e-e8c6cad6a5c6` (write)
- **Status:** open — the disk is an infrastructure concern, the opaque 5xx is ours
- **Fix:** two parts. The driver maps a 5xx into an error that says the server refused
  and names the operation, so a caller stops reporting it as a client fault; and the
  read of a constant-partition entity (`audit_log`, 182,314 items in ONE partition) is
  re-measured on a host with room, because this run cannot tell a size limit from a
  full disk. `specs/6/42` §12 items 5 and 6 own that measurement.

## ✅ FIXED 2026-09-18 L290 — `routd`'s held-world test races its own recorder, so one run in five reports the owner was never asked (2026-09-13)

MEASURED by a subagent running the whole suite: `routd/hold_world_inversion_test.go:70`
failed once with "the owner was not asked: []", and `go test ./routd/ -race -count=30
-run TestWorld_HoldsWhatTheOwnerShouldSeeAndRunsTheRest` reports `WARNING: DATA RACE`
between the main goroutine reading `dl.sends` at line 69 and the gate goroutine writing
it at line 63. `awaitHeldID` waits for the DB row, which can land before the chat send
is recorded, so the read happens while the write is in flight.

It is the TEST that races, not the daemon — but it fails a green suite about one run in
five, and `make test` runs without `-race`, so the race itself is invisible there.

- **Severity:** medium — a flaky failure in the suite that gates every merge
- **Scope:** `routd/hold_world_inversion_test.go`
- **Affected:** every run; the failure is timing-dependent
- **Source:** `routd/hold_world_inversion_test.go:63`, `:69`
- **Status:** fixed in 5a1ffc96
- **Fix:** the CAUSE was not the read. `recDeliverer.SendOptions` stamped the
  options onto the last recorded send OUTSIDE the lock, and the hold notice is
  delivered through that method — so every reader raced that one write. The
  stamp takes the lock, with `defer`: the index panics on an empty slice, and a
  panic between Lock and Unlock holds the mutex for good, which turns one failing
  test into a package timeout.

  It also collapsed five copies of the same `for range 500 { … 2ms }` poll onto
  one `awaitCond`. One second held for a single test on an idle host and missed
  under `-race`; the budget is 15s now, and `TestHoldE2E_RejectPressKeepsTheCallUnrun`
  was failing on exactly that. VERIFIED: the whole hold suite, `-race -count=10`,
  green in 412s; `go vet ./routd/` clean.

## ✅ FIXED 2026-09-14 L291 — `database/sql` survived in dashd to carry three optional strings (2026-09-13)

`dashd/tasks_admin.go` declared `cron`, `nextRun` and `contextMode` as
`sql.NullString` to render a form, and nothing read the `Valid` half — so it was
an optional string with an extra field, and it was the only reason `database/sql`
reached dashd at all.

- **Status:** fixed in v0.68.0
- **Fix:** plain strings plus the empty test the template already makes, since
  absent and empty are the same thing on a form.

## ✅ FIXED 2026-09-12 L284 — `/eval` queries the pre-migration schema, so the debug loop goes blind the moment an instance migrates (2026-09-12)

REPRODUCED on a migrated copy of krons' `routd.db`:

```
sqlite> SELECT c.jid, c.agent_cursor FROM chats c LIMIT 1;
Error: in prepare, no such column: c.jid
```

`.claude/skills/eval/SKILL.md` reads columns the entity does not have, at lines
96-106 (check 4, routing cursors), 117 and 124 (the stuck-chat drill-down and the
errored-message clear), 170 and 179 (check 7, the task scheduler), and 408 and
447 (checks 17 and 19, skill seeding and consistency). Six of the twenty checks
stop working, and two of them are the ones a person runs when an instance looks
stuck.

This matters more than its severity suggests: `/eval` is the instrument for
finding out whether a migration worked, and it breaks on exactly the databases
it would be pointed at. An operator would read the errors as a broken instance
rather than a broken query.

- **Severity:** high — the migration verification tool does not survive the migration
- **Scope:** `.claude/skills/eval/SKILL.md`
- **Affected:** any migrated instance
- **Status:** fixed
- **Fix:** every check reads the entity shape — the jid is the `sort`, a message's
  instant is the first 30 bytes of its `sort`, and a flag is `"1"` or absent, so
  there is no stored zero to compare against.

**It also fixed a latent miscount nobody had seen.** Check 7's window compared
`run_at` (`2026-09-12T02:01:01Z`) against `datetime('now','-24 hours')`
(`2026-09-11 16:34:24`). `T` is 0x54 and a space is 0x20, so rows from earlier
days compared as greater: the check reported **25** runs in 24 hours where there
were **11**, measured on krons. A scheduler that had stopped would have read as
healthy. The window now compares one layout against itself. Same class as `L97`.

VERIFIED against a migrated krons copy and the unmigrated original: chats 136,
active tasks 61, groups 53, errored messages 12 — identical on both sides; runs
in 24 h is 11 on both once each query compares one layout.

## L283 — the agent turn container runs unhardened while its own egress sidecar is locked down (2026-09-12, open — needs sign-off)

MEASURED at `container/runner.go:678-700`: the `docker run` that executes an
agent turn carries `--rm`, `--name` and `--shm-size=1g`, and nothing else. No
`--cap-drop`, no `--security-opt`, no `--read-only`, no `--pids-limit`, no
`--memory`. The egress sidecar beside it carries `--cap-drop ALL --read-only`
(`container/egress.go:249-250`).

The hardening sits on the process that proxies traffic and not on the process
that runs whatever the model produced. `SECURITY.md:49` names "a malicious agent
in group A" in the threat model, and today that agent holds the default Docker
capability set on a shared kernel.

`runed` also mounts `/var/run/docker.sock` (`compose/compose.go:1004-1009`), so
the daemon that spawns turns holds root-equivalent access to the host.

- **Severity:** high — the isolation boundary is weaker than the threat model states
- **Scope:** `container/runner.go`, `compose/compose.go`
- **Affected:** krons, marinade, sloth — every turn on every instance
- **Status:** open — the flag set is a new contract for every spawn, so it needs
  sign-off before it ships
- **Fix:** the turn spawn declares the same restrictions the sidecar already
  declares, from ONE place both read, so a new spawn site cannot be born
  unhardened. `--runtime=runsc` is the separate, larger question: it is one
  `daemon.json` line and changes no arizuko mechanism.

## ✅ FIXED 2026-09-12 L285 — the agent's own reply records the chat as its folder, so half the folder feed is unreadable (2026-09-12)

MEASURED on live krons: **2,710 of 5,605** message rows carrying `routed_to` hold
a chat jid rather than a folder, and **42 of the 95** `message_folders`
partitions are jids. That half of the index is written on every agent reply and
read by nothing.

Two writers build the same row. `routd/turns.go:118` writes `RoutedTo: tc.Folder`.
`ipc/ipc.go` wrote `RoutedTo: jid`, with the turn's `folder` already in scope and
already used one line away.

`routed_to` names the folder the turn ran in, and the folder feed is partitioned
by it, so a jid there makes the reply invisible to every folder-scoped read:
`find_messages` cannot see it, the per-folder counts understate it, and
`resolveTarget` refuses a jid as a delegation target and returns early, skipping
the sticky-group and route-table fallbacks.

- **Severity:** high — 48% of a live index is unreadable, on every instance
- **Scope:** `ipc/ipc.go`
- **Affected:** krons measured; marinade and sloth run the same code
- **Status:** fixed
- **Fix:** one token. `TestRecordOutbound_RoutesToTheFolderNotTheChat` pins it and
  fails on the old value. The existing rows are a separate decision: a backfill
  would rewrite 2,710 items and their feed entries, and nothing reads them today.

## ✅ FIXED 2026-09-12 L287 — deleting a folder leaves every child folder's feed partition behind (2026-09-12)

REPRODUCED with groups `acme` and `acme/support`, one feed item in each:
partition `acme` emptied, partition `acme/support` kept its item.

`purgeFolder` reads the subtree off `groups`, and `groups` is the FIRST entry in
`entityScopePurge` — so by the time `purgeFolderPartitions` asked which folders
the subtree owns, the rows that name them were already deleted and the read
answered nothing. The doc comment claimed the opposite: "The read runs BEFORE the
purge above deletes those rows."

The next tenant of that path inherits the previous tenant's ambient window, since
`ObservedSince` reads that partition. Same class as `L66` and `L67`.

- **Severity:** high — state leaks between tenants of the same path
- **Scope:** `store/folder_scope.go`
- **Affected:** every instance
- **Status:** fixed
- **Fix:** the folder list is read once, before the purge loop, and handed to the
  partition purge. `TestMessageFolders_ChildPartitionPurgedOnGroupDelete` pins it
  and fails on the old order.

## ✅ FIXED 2026-09-12 L288 — `resreg` opened a transaction per mutating call, and it was the last one (2026-09-12)

`resreg.go` began a transaction for every mutating call, handed it to the handler
as `x.Tx`, and committed it with the audit row `emitAudit` wrote. That was the
same guarantee `store`'s writers had already given up in favour of one guarded
batch, and it was the only place left in arizuko that opened a transaction for a
mutation.

The difference was the contract. A `store` writer builds its writes and hands
them over, so the batch is assembled in one place. A resreg `Handler` PERFORMED
its writes and returned a rendered row, so `invoke` never saw them and could not
put them in a batch with the audit row.

- **Severity:** high — the last structural blocker for the storage port
- **Scope:** `resreg/resreg.go`, and every handler that read `x.Tx`
- **Affected:** routd, onbod, proxyd
- **Status:** fixed
- **Fix:** `Execution.Writes` carries a `store.WriteSet` where `x.Tx` was. Each
  `…Tx` store method became a `…Into` twin that APPENDS its write, and `invoke`
  sends the whole set plus the audit row as one `GuardedBatch`. A write whose
  refusal is an answer goes in through `WriteSet.AddGuarded` with the error that
  refusal means, because the handler has returned by the time the verdict exists;
  a set holds at most one, since a batch reports one bit.

  Three writes changed SHAPE to fit one batch, each because it named one item
  twice or refused where a refusal had no meaning: the gate upsert folds its
  limit and its flag into one Merge, the web-route upsert is a Merge rather than
  a create-then-update pair, and the network-rule allow is a Merge rather than a
  create-if-absent whose refusal was discarded.

  Two write sets are sized by the CALLER — `set_routes` and a jid's token revoke
  — and both go in WHOLE rather than in chunks: past `objstore.MaxBatchActions`
  the batch is refused naming the count, which an operator can act on, where a
  sweep would leave a half-replaced routing table or live bearers under an audit
  row saying they were revoked. The largest live instance holds 58 route rules
  and 43 route tokens, so neither bound is a live limit.

  Exported `store` functions naming a `*sql.Tx`: **33 → 5**
  (`WriteEntitiesInTx`, `TryWriteEntitiesInTx`, `QueuedSystemMessagesTx`,
  `DrainSystemMessagesTx`, and their two callers' doors). `Store.Obj()` still
  cannot return the interface — see `L289`.

## ✅ FIXED 2026-09-14 L289 — `Obj()` returned the SQL driver, not the interface (2026-09-12)

`Store.Obj()`, `routd.DB.Obj()` and `runed.DB.Obj()` all answer
`objstore.Store` now. The six call sites the entry measured were held by the
transaction machinery, and the transaction went with the SQL driver: there is
one driver, it opens no transaction, and `GuardedBatch` is the whole write.

- **Status:** fixed — `objstore/sqlstore` and `db_utils` deleted in v0.68.0
- **Left over:** the `*sql.Tx` PARAMETER those doors carried is still threaded
  through nine files and can only ever be nil. Recorded as `L301`.

## ✅ FIXED 2026-09-12 L286 — five writers needed an unbounded atomic delete, which the object store cannot give (2026-09-12)

Twenty-two writers in `store/` opened a transaction so their audit row would
commit with their mutation. Seventeen are now one guarded batch, which gives the
same all-or-nothing in a single call. Five cannot follow, and the reason is the
same for four of them: they write a number of rows the caller decides.

`applyChunks` (`store/entities.go:378`) already sends one batch per 99 actions.
Inside a caller-owned transaction those chunks commit together; without one they
do not. A batch holds at most `objstore.MaxBatchActions`, so the object store has
no way to make an unbounded delete atomic.

- `DeleteGroup` (`store/groups.go:362`) — purges every entity in
  `entityScopePurge` plus each per-folder partition, and reads on the transaction.
- `RevokeRouteToken` (`store/route_tokens.go:362`) — one row per mint of the jid.
- `RemoveGitHubSource` (`store/github_sources.go:339`) — reads the whole routes
  partition, then deletes through `applyChunks`.
- `SyncProxydRoutes` (`store/proxyd_routes.go:293`) — one write per shipped route,
  26 on krons today and unbounded in principle.
- `AddGitHubSource` (`store/github_sources.go:328`) — different reason: its seven
  or eight seeded rules go through a create-if-absent that tolerates refusal by
  reading the existing id back. In one batch a single pre-existing rule refuses
  the whole registration.

This is not academic for `DeleteGroup`. A half-purged folder is observable today:
the subtree read runs inside the same transaction as the deletes that precede it,
so it returns nothing and every CHILD folder's `message_folders` partition
survives. The next tenant of that path inherits the previous tenant's ambient
window. Reproduced on `store.OpenMem` with groups `a` and `a/b`: partition `a/b`
kept one item.

- **Severity:** high — it blocks the last five writers, and one of them already
  leaks state between tenants
- **Scope:** `store/groups.go`, `store/route_tokens.go`, `store/github_sources.go`,
  `store/proxyd_routes.go`, `store/entities.go`
- **Affected:** every instance
- **Status:** fixed
- **Fix:** ordering, and no new marker. Each sweep deletes the row that makes the
  subject REACHABLE first — `groups` for a folder, the source row for a GitHub
  registration — so a failure part-way leaves rows nothing can find and the next
  run collects them. `AddGitHubSource` seeds its rules with a create-if-absent
  that tolerates one already there, so a partial registration completes on a
  re-run rather than refusing whole.

**The premise was wrong and the redesign was not needed.** These writers never
required atomicity; they required an order. All twenty-two audited writers are
now batch-shaped or sweeps, `runAudited` is deleted, and `store` opens no
transaction of its own. The child-partition leak is `L287`, fixed separately.

## ✅ CLOSED 2026-09-13 L276 — re-running `migrate-hot` after routd has taken writes rolls every cursor back, and the refusal comes too late to stop it (2026-09-12)

REPRODUCED on a migrated copy of sloth's `routd.db`. The move derives each chat's
cursor from `messages_v1`, which is frozen at the moment of the first run, so a
message routd appended afterwards does not exist for the derivation:

```
cursor set to the message that arrived after the move
$ arizuko migrate-hot --db ./routd.db
Failed: 19299 messages read back, 19298 were written    (exit 1)
cursor on disk: 2026-04-16T22:10:16.000000000Z\x1f3126  (the pre-migration value)
```

The tool refuses LOUDLY and the cursor is already overwritten, because the count
check is a post-hoc read rather than a precondition. routd then boots on the
rolled-back cursor and re-feeds every message consumed since the migration.

An operator re-runs a migration exactly when something looks wrong, so this is
the likely second command, not an unlikely one.

**The tool is gone.** Migration is hand-driven (maintainer, 2026-09-13): the
`migrate-*` subcommands are deleted and the binary carries no migration, so
there is nothing left to re-run. An operator who moves an instance watches it,
and the rows an instance already holds stay in its SQLite files, which the
retired binary still reads.

- **Severity:** critical — blocks an unattended migration; re-feeds a whole chat
- **Scope:** `cmd/arizuko/migrate_hot.go`
- **Affected:** every instance, reproduced on a sloth copy
- **Status:** closed — the subcommand no longer exists
- **Fix:** `aae3c0cb` (`specs/6/42` §7)

## ✅ CLOSED 2026-09-13 L277 — a killed migration leaves a database that boots, holding part of the history and no cursors (2026-09-12)

Reported by the migration rehearsal, on copies: with every other tool complete
and `migrate-hot` killed part-way, routd started normally holding **292 of 19,300
messages and 0 of 53 chats with an `agent_cursor`**. Nothing marks the database
incomplete, and an empty cursor reads as "feed from the start"
(`store/chats.go:126`).

Re-running recovers completely — rows and their feeds commit together, and a kill
leaves 0 rows missing a feed. The defect is that a half-moved database is
indistinguishable from a finished one.

**The tool is gone**, so the half-moved database it describes cannot be
produced by anything shipped. A hand-run copy is watched, and its verification
is the operator's step rather than a contract the daemon enforces.

- **Severity:** high — a silent partial migration serves partial history
- **Scope:** `cmd/arizuko/migrate_hot.go`, `store/import_hot.go`
- **Affected:** every instance
- **Status:** closed — the subcommand no longer exists
- **Fix:** `aae3c0cb` (`specs/6/42` §7)

## ✅ CLOSED 2026-09-13 L278 — the migration verifies 7 of routd's 13 entities and none of the four derived message feeds (2026-09-12)

`verifyRoutd` reads back `groups`, `acl`, `routes`, `route_tokens`,
`network_rules`, `proxyd_routes` and `web_routes`. It never reads
`acl_membership`, `scheduled_tasks`, `secrets`, `pending_actions`,
`installed_packages` or `github_sources` — and `secrets` is a credential table.
`verifyHot` checks all eleven hot entities and the cursors, but none of
`message_folders`, `message_folder_counts`, `message_activity` or
`message_delivery`.

That gap is why L275 went unnoticed: the delivery items were written, so a count
check passed, while an attribute was missing from every one of them.

**The tool is gone.** The rule it was going to learn survives in the copy the
operator runs: verification iterates the DECLARED entity list, per table the
row count and the `Query` count agree, and every `chats` cursor reads back at
the same position (`specs/6/42` §7).

- **Severity:** medium — the tool reports success over what it did not read
- **Scope:** `cmd/arizuko/migrate_routd.go`, `cmd/arizuko/migrate_hot.go`
- **Affected:** every instance
- **Status:** closed — the subcommand no longer exists
- **Fix:** `aae3c0cb` (`specs/6/42` §7)

## ✅ FIXED 2026-09-12 L279 — `migrate-chanlib` and `migrate-emaid` exit 1 when the database does not exist, so a scripted migration stops before the audit step (2026-09-12)

MEASURED: `migrate-chanlib` exits 1 against an absent database. The rehearsal saw
it on marinade and sloth, and `migrate-emaid` on all three, because no instance
has an `emaid.db`. Under `set -e` the run aborts before `migrate-audit`, which is
the step carrying 417,714 rows fleet-wide.

`migrate-audit` already treats an absent owner as nothing to do. The two disagree
about the same situation.

- **Severity:** medium — a correct scripted run stops half-way
- **Scope:** `cmd/arizuko/migrate_chanlib.go`, `cmd/arizuko/migrate_emaid.go`
- **Affected:** krons, marinade, sloth — measured
- **Status:** fixed
- **Fix:** "this instance has no such database" exits 0 with a line saying so,
  which is what `migrate-audit` already does. A path the operator NAMED with
  `--db` still fails, because that one is a mistake rather than an absence.
  VERIFIED: both tools now exit 0 on all three instances, and an explicit
  missing path still exits 1.

## ✅ CLOSED 2026-09-13 L280 — `migrate-split --dry-run` writes three databases, puts `onbod.db` where no daemon reads it, carries no auth, and then cannot finish (2026-09-12)

Reported by the migration rehearsal, measured on copies. A `--dry-run` printed
"source row counts only, no writes" and then created `store/onbod.db` (flat —
daemons read `store/onbod/onbod.db`), `store/routd/routd.db` migrated to chain 47
and seeded with 35 acl rows, and `store/runed/runed.db`. It then failed with
`onbod.db: no such column: ref`, because `bootstrapOnbod` is gated behind
`!dryRun` and the read that needs it is not. The only mention of auth in the file
is a header comment claiming it populates `auth.db`; there is no auth
destination.

Not a blocker for krons, marinade or sloth — all three are already split and
`migrate-split` must not run on them. It is fatal for a genuinely pre-split
instance.

**The tool is gone**, and no live instance needed it: krons, marinade and sloth
are all split. A pre-split instance now moves by hand, with the retired binary
reading the monolith it was built to read.

- **Severity:** high for a pre-split instance, none for the current fleet
- **Scope:** `cmd/arizuko/migrate_split.go`
- **Affected:** any instance still on `messages.db`
- **Status:** closed — the subcommand no longer exists
- **Fix:** `aae3c0cb` (`specs/6/42` §7)

## ✅ CLOSED 2026-09-13 L281 — 9,216 `cost_log` rows are carried by no migration tool, and `0046` names a table that exists nowhere (2026-09-12)

MEASURED on all three live instances: `cost_log_archive` does not exist, and
`cost_log` holds 3,715 rows on krons, 2,783 on marinade and 2,718 on sloth.
Migration `0046`'s text says "The three files that carry those rows carry them
already, under the name `cost_log_archive`". That is false for the live fleet:
only `migrate-split` ever writes that name, and it never ran on these three.

The rows survive as a plain SQL table, so nothing is lost today — but no tool
owns them and no entity holds them, so they are outside the port.

**Decided: the cost log is dropped.** No Go code reads or writes `cost_log` —
the gateway is the ledger (`specs/6/39`) — so the rows are history, not state.
The copy onto the store server does not carry them, and they stay readable in
the SQLite file the instance leaves behind.

- **Severity:** medium — 9,216 rows outside the object model, and a spec claim
  that does not match any live instance
- **Scope:** `routd/migrations/0046-cost-log-archive.sql`
- **Affected:** krons, marinade, sloth — measured
- **Status:** closed — the rows are history and the copy skips them
- **Fix:** `7972f092` (`specs/6/42` §7)

## ✅ FIXED 2026-09-18 L282 — three adapter containers have no Docker mount, so their databases die on every recreate (2026-09-12, code fixed; two instances carry a frozen fragment)

MEASURED with `docker inspect -f '{{len .Mounts}}'`: `arizuko_teled_marinade`,
`arizuko_discd_marinade` and `arizuko_teled_sloth` report **0** mounts.
`arizuko_teled_krons` reports 1. Their `store/teled/teled.db` lives in the
container's writable layer, so the adapter's dedup state is lost whenever the
container is recreated — which a deploy does.

Independent of the storage port. It is why `migrate-chanlib` finds nothing for
those instances.

- **Severity:** medium — silent state loss on every deploy, two instances
- **Scope:** `template/services/teled.yml`, `template/services/discd.yml`, compose generation
- **Affected:** marinade (teled, discd), sloth (teled) — measured
- **Status:** the template is fixed and proven; the two instances are not
- **Re-measured 2026-09-18:** every fragment that names `DATA_DIR` (`teled`,
  `slakd`, `bskyd`, `linkd`, `reditd`) declares its bind mount, and
  `arizuko_teled_krons` reports 1 mount —
  `/srv/data/arizuko_krons/store/teled -> /srv/app/home/store/teled`, owned
  `mivu:mivu`. `discd` needs none: it names no `DATA_DIR` and writes nothing to
  disk, so this entry was wrong to count it.
- **What is left is a deploy, not a code change.** `arizuko_teled_marinade`,
  `arizuko_discd_marinade` and `arizuko_teled_sloth` still report 0 mounts
  because `services/teled.yml` freezes once its bytes differ from the template,
  and both instances hold the pre-deletion tag. Do NOT hand-patch a frozen
  fragment on its own: neither instance has a `store/teled` directory, so docker
  would make it root-owned and the non-root daemon could not write it. Make the
  directory first, owned the way krons owns it, then recreate the adapter.
- **Fix:** the adapter fragment declares its owner directory as a bind mount, the
  way krons already has one.

## ✅ FIXED 2026-09-12 L275 — the move and the archive drop `delivery_attempts`, so every restored dead letter claims never to have been tried (2026-09-12)

MEASURED on a copy of sloth's live `routd.db`: the legacy `messages` table
carries a `delivery_attempts` column, 14 rows hold a non-zero count, and after
`migrate-hot` the `message_delivery` items carried one attribute, `chat_jid`.
`store.ArchiveMessageRow` had no field for it either, and its own doc comment
claims to be "the FULL shape of one archived message: every attribute the item
carries" — so a restore from the archive lost it as well.

The count is what `DeadLetters` reports to the operator. Fleet-wide the
dead-letter set is 922 rows on sloth, 395 on krons, 81 on marinade, with attempt
counts up to 10.

Nothing is re-sent: `PendingOutbound` reads the `pending` partition, the move
puts every undelivered row in `failed`, and all three instances hold ZERO pending
rows. The live path was always right — `DeadLetter` writes the count. Only the
two bulk paths dropped it.

- **Severity:** medium — operator-visible on 1,398 rows, no message is re-sent
- **Scope:** `store/import_hot.go`, `store/messages_archive.go`, `store/message_delivery.go`
- **Affected:** krons, marinade, sloth — measured
- **Status:** fixed
- **Fix:** `deliveryWrite` takes the count, which is the ONE renderer both bulk
  paths and the live append already share, so neither can carry a delivery item
  without it. `ArchiveMessageRow` gained the field and the export reads it back.
  VERIFIED on a fresh sloth copy: 14 legacy rows with a count, 14 items carrying
  it, values intact. `TestMessagesRoundTripEveryColumn` now drives a row to the
  dead-letter set through real retries and reads the count back; it fails on the
  unfixed export.

## ✅ FIXED 2026-09-12 L274 — routd cannot open a migrated instance: the chain still ran an UPDATE against a column the move removed (2026-09-12)

REPRODUCED on a copy of sloth's live `routd.db` (routd version 40; krons is 43,
marinade 40 — all three below 44, so `0044` is pending on every instance):

```
$ arizuko migrate-hot --db ./routd.db
  verified messages 19298 ... verified chats 53 cursors, same position
$ routd.OpenAt("./routd.db")
OPEN FAILED: 0044-fixed-width-timestamps.sql: no such column: timestamp
```

`migrate-hot` renames `messages` to `messages_v1` and the entity takes the name,
so the three-column table is what the pending `UPDATE messages SET timestamp =
...` meets. Every instance would have failed to start after its own migration,
and recovery needs a backup — there is no rollback.

The statement had nothing left to do. `migrate-hot` renders every instant through
the writer that enforces the width, and it verifies the result by the SET of rows
each cursor leaves unfed rather than by bytes. MEASURED on two copies of the same
sloth database, one padded by the SQL first and one not — 7,592 rows of
difference at the input — the `messages` keys and the `chats` items came out
byte-identical, all 19,298 of them.

- **Severity:** critical — no instance starts after its own migration
- **Scope:** `routd/migrations/0044-fixed-width-timestamps.sql`, `routd/db.go`
- **Affected:** krons, marinade, sloth — every instance, measured
- **Status:** fixed
- **Fix:** the chain entry holds no statement, like its neighbours whose tables
  became entities. `NormalizeFixedWidthTimestamps` went with it: nothing called
  it, and it executed the same file. Both orders — move-then-open and
  open-then-move — now reach `OPEN OK` on a live sloth copy.

## ✅ FIXED 2026-09-10 L266 — `arizuko apply` of an UNMODIFIED export destroys every route token and every github source (2026-09-08)

REPRODUCED 2026-09-08 on a copy of marinade's routd.db, read-only against the
snapshot, with the binary from before any of today's work:

```
before: groups=7 route_tokens=2 web_routes=2 github_sources=0
DELETE FROM groups;          -- what Apply does to rebuild the resource
after:  route_tokens=0 web_routes=0 github_sources=0
```

`resreg.Apply` rebuilds a resource with `DeleteAll` then `InsertAll`.
`groups` is rebuilt that way. `route_tokens.owner_folder`,
`web_routes.folder` and `github_sources.owner_folder` all carry
`ON DELETE CASCADE` to `groups(folder)`, and `db_utils.FileDSN` sets
`foreign_keys(on)`, so the delete takes them with it.

`web_routes` survives because the manifest re-inserts it. **`route_tokens` and
`github_sources` are `SkipApplyRebuild`** (`resreg/resources/route_tokens.go:128`,
`github_sources.go:73`), so nothing puts them back. They are gone.

Both are live ingress credentials: a route token is the bearer in
`/chat/<token>/` and `/hook/<token>`, and a github source holds the webhook
secret gitd unseals. Applying a config manifest an operator just exported —
changing nothing — revokes every one of them silently, and the apply reports
success.

This is the same cascade `store/folder_scope.go` records in three `keepReason`
verdicts, which is how `DeleteGroup` gets away with issuing no statement for
them (`L253`). Deleting ONE group cascading to that group's tokens is correct.
Deleting every group to rebuild the table is not a tenant delete, and the
cascade cannot tell the two apart.

- **Severity:** high (silent credential loss on a routine operator action)
- **Scope:** `resreg/engine.go` `Apply`, `resreg/resources/{route_tokens,github_sources}.go`
- **Affected:** every instance; measured on marinade, 2 tokens
- **Source:** reproduced against a snapshot copy, 2026-09-08
- **Status:** fixed 2026-09-10 by the port, not by a redesign

Fixed as a side effect of the objstore move, and verified rather than assumed.
An entity has no foreign key, so the cascade has no mechanism left:
`grep 'REFERENCES groups' routd/migrations/*.sql` returns nothing, and a
migrated marinade copy carries no table whose DDL mentions it.

Rehearsed end to end on a copy: `migrate-routd`, `migrate-hot`,
`migrate-onbod`, `migrate-audit`, then `arizuko export` and `arizuko apply` of
the unmodified export. `route_tokens` stayed at 2 and `web_routes` at 2 across
every attempt, including two that failed for other reasons. The redesign this
entry asked for is not needed: the cascade is gone.
- **Fix:** a rebuild is not a tenant delete. `Apply` must not rebuild a table
  whose rows other resources hang off — either `groups` stops being
  delete-all/insert-all and becomes a diffed upsert, or the rebuild runs with the
  cascade suppressed and the dependent rows re-attached. The second is the
  smaller change and the first is the right one.

## ✅ FIXED 2026-09-12 L271 — the world's own owner cannot approve anything in their world (2026-09-12)

`cmdResolveHold` (`routd/steer.go:574`) gates `/approve`, `/reject` and
`/always` on `IsOperator(sender)` — `role:operator`, or `admin` on `**`. A world
creator gets `admin <world>/**` (`onbod/main.go:884`), and `matchPattern`
(`auth/acl.go:63`) does not read `main/**` as covering `**`.

So the person who owns a world cannot answer a hold in it. On sloth the discord
user holds exactly that row. The dashboard is no escape: it gates on the stamped
groups carrying `**`, which is the same test by another reader.

This is the mechanism behind "the trading group cannot set up crons". Granting
the tools is only half — with nobody able to approve, a held call waits until it
expires.

- **Severity:** high (the approval feature has no reachable approver per world)
- **Scope:** `routd/steer.go:574`, `dashd/approvals_page.go`, `auth/dashauth.go`
- **Affected:** every world; measured on sloth
- **Source:** `IsOperator` vs the `admin <world>/**` row onbod writes
- **Status:** open
Fixed 2026-09-12. The reproduction was written first and failed as filed —
`status = "held", want approved; the ack was "Permission denied: approving a
held call requires an operator grant (**)"`. The gate now sits in
`resolveHoldTx` and keys on the held row's folder, so chat and REST run ONE
gate; `auth.Authorize` is the evaluator, so a deny row still wins unconfined.
dashd forwards the proxyd-verified sub as the reviewer and routd accepts only
folders that sub holds `admin` on.

- **Fix:** the gate moves into `resolveHoldTx` and keys on the HELD row's folder
  — `Authorize(reviewer, "admin", p.GroupFolder, nil)`. One gate for chat and
  REST, replacing two readers of "operator". A deny row stays unconfined, so an
  operator refusal still wins.

## ✅ FIXED 2026-09-12 L272 — `add_acl` writes any `effect` string, and `Authorize` reads every non-`deny` value as allow (2026-09-12)

`routd/acl_resource.go` takes `Effect: argString(args, "effect")` verbatim and
only substitutes `allow` when it is empty. `auth.EffectAllowed`
(`auth/authorize.go:578`) is the validator, and it has exactly ONE caller —
`dashd/grants_admin.go:208`. `Authorize` (`auth/authorize.go:99`) treats a row as
allow unless the effect is literally `deny`.

So a row written through the agent or REST face with a mistyped or invented
effect becomes a GRANT. `effect: "denny"` is an allow. A `hold:` row written with
a wrong effect stops holding.

Today the blast radius is small because few callers hold `acl.write`. It becomes
a privilege escalation the moment world-tier folders hold `mcp:*`, because
`add_acl` is then one of the tools they hold — an agent could widen its own
grants by writing a row whose effect nobody validates.

This is "one declaration, many readers" failing: the validator exists, and two of
the three writers do not call it.

- **Severity:** medium now, high under the allow-everything-with-approval default
- **Scope:** `routd/acl_resource.go`, `auth/authorize.go`, `dashd/grants_admin.go`
- **Affected:** every instance
- **Source:** `grep EffectAllowed` returns one non-test caller, 2026-09-12
- **Status:** open
Fixed 2026-09-12, with the escalation proved first: *"ESCALATION: the row
spelled `denny` authorizes `read` on root/team"*. Validation now sits in
`store`'s `aclPut`, which every door into the acl entity renders through —
`PutACLRow`, `PutACLRowTx`, `ACLGrantWrite`, `AddACLRow` and the seed. `store`
declares the vocabulary once and `auth` reads it.

- **Fix:** validation moves to where the row is WRITTEN — `store`'s ACL writer —
  so no face can bypass it, and `EffectAllowed` stops being something a caller
  may forget.

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

## ✅ FIXED 2026-09-18 L270 — nine adapter dashboards trust `X-User-*` without verifying proxyd stamped it (2026-09-12)

`auth.RequireOperator` (`auth/dashauth.go:66`) is the one correct gate, and its
own comment names the attack: reading `X-User-*` without verifying proxyd's
`service:proxyd` transit proof "lets anyone who can reach the port send
`X-User-Groups: [\"**\"]` and receive the operator page". Seven daemons use it;
`dashd` verifies `ProxydTransit` directly (`dashd/main.go:501`).

Nine surfaces do not. `chanlib/dash.go:66` gates on `auth.IsOperator(r)` alone,
and it sits inside the shared adapter handler, so **eight adapters inherit it**
— the "one renderer, no per-adapter edit" shape working against us.
`ttsd/dash.go:36` is the ninth.

**It is documented, not forgotten.** `chanlib/dash.go:32` says the adapter
"holds no keyset, so it cannot use the nil-KeySet form the core daemons use",
and `dashEnforced()` — `AUTHD_URL` being set — is the substitute. So the gap is
a missing capability, not a missed call.

**And it is bounded, measured on sloth 2026-09-12:** the adapters publish NO
host port. `docker ps` shows only `proxyd` (49176), `dashd` (8090) and `routd`
(8080) reachable from the host, and all three verify the proof. Reaching an
adapter dashboard means already being inside the compose network.

So this is defence-in-depth that was never built, not an open door. It still
matters: the network is the only thing holding it, one published port would end
that, and the rule this project keeps is that a stamped header is worthless
without the proof beside it.

- **Severity:** medium (bounded by the network today; high the day a port is published)
- **Scope:** `chanlib/dash.go`, `ttsd/dash.go`, `auth/dashauth.go`
- **Affected:** 8 channel adapters plus ttsd, every instance
- **Source:** `grep RequireOperator` vs `chanlib/dash.go:66`; `docker ps` on sloth
- **Status:** fixed in 59cad96e
- **Fix:** both call `auth.RequireOperator` now, the gate every core daemon
  already uses. The keyset was never a missing capability: `chanlib` already
  built one to pin routd's service token, because it is one authd answering both
  questions. It is exported as `chanlib.AuthdKeys` and INJECTED, the way
  `authGate` already takes one — a `sync.Once` built from the process env cannot
  be swapped, which is why the old test set `AUTHD_URL` and still passed against
  an open gate. `dashEnforced` is gone, and `ttsd` reads the same keyset.
  `auth/authtest` holds the fixture; onbod had a copy of it and timed had it
  inline, so there is one now. Each gate states the case: `["**"]` with no proof
  beside it is 403.
- **Fix:** give the adapter the keyset it says it lacks — it already reads
  `AUTHD_URL` to decide whether to enforce, which is the same fact
  `auth.NewKeySet` needs — then call `RequireOperator` like everyone else. One
  renderer, one gate, and the `dashEnforced` substitute goes.

## ✅ FIXED 2026-09-18 L269 — nothing in production created a `user_profiles` item (2026-09-11)

Every one of `CreateUserProfile`'s call sites was a `_test.go` file, so onbod's
`writeWorld` ended by RENAMING a profile that was never there — an Update whose
refusal had to be tolerated for the admission to work at all. A person admitted
today stayed nameless on every operator surface that labels people.

- **Status:** fixed in v0.68.2
- **Fix:** `store.RecordPerson` is the door the admission path takes: it creates
  the profile when the instance has never seen the sub, and renames an existing
  one when the person comes back. `CreateUserProfile`'s refusal is
  `ErrProfileExists`, a sentinel, because RecordPerson decides on it and
  deciding on an error's text breaks when the text is reworded.
- **Test:** `store.TestRecordPerson_CreatesThenRenames`.
## ✅ FIXED 2026-09-11 L268 — a guarded write's `committed` verdict is discarded at 15 call sites, so `PUT /v1/gates/<gate>` answers 200 and writes nothing (2026-09-11)

`objstore.GuardedBatch` answers `(committed bool, err error)`, and `committed=false`
with a nil error is the NORMAL outcome of a condition that did not hold — an
`Update` on an item that does not exist, a claim someone else won. It is not an
error and it never will be; that is the design.

So a caller that writes `if _, err := …; err != nil` reports success for a write
that did not happen. Measured 2026-09-11: **15 of 59 `WriteEntitiesInTx` call
sites in `store/` discard the bool.**

Reproduced on a live onbod running against PostgreSQL:
`PUT /v1/gates/github:org=acme` carrying only `{"enabled":true}`, against a gate
that does not exist, answers `HTTP 200 {"ok":true}` and writes zero rows.
`store/onboarding.go:401` `EnableGateTx` is the site. The operator sees success,
the gate stays absent, and nothing logs.

This is engine-independent — the write is unguarded at
`objstore/sqlstore/batch.go:145` — and it is the exact shape WISDOM's "fail loud,
fail to the user" exists to stop: a logged-but-invisible failure is still silent,
and here not even a log line is produced.

Not every discard is a defect. A delete asking twice is legitimately
unconditional, and some writes are create-if-absent where a refusal means the row
is already right. Each of the 15 needs its own verdict; what is wrong is that the
type lets a caller ignore the answer by accident and 15 did.

- **Severity:** high (a user-facing write reports success and does nothing)
- **Scope:** `store/onboarding.go:401` and 14 others; `store/scopes.go`'s writers
- **Affected:** every instance; onbod's gate surface measured
- **Source:** reproduced on onbod against PostgreSQL 17.11, 2026-09-11
- **Status:** fixed 2026-09-11

Fixed by making it a BUILD error. `store`'s three writers return `error` and no
verdict, so `if _, err := …` no longer compiles — every dropped verdict became a
compile failure, and so does the sixteenth. A refusal is `store.ErrRefused`,
naming the batch. A caller that legitimately expects a refusal calls the `Try…`
door, where the NAME is the record, and `store/scopes_test.go` walks the module
refusing an unexplained `_` on it. `errcheck` could never have caught this: a
bool is not an error.

**My count of 15 was low — it is 54 in `store/` alone**, because the entry
counted `WriteEntitiesInTx` and `WriteEntities` carries 27 more, plus 4 in
`routd/`. Each got its own verdict: 18 unconditional (a delete asking twice),
21 guarded where a refusal IS a failure, 15 deliberate where a refusal is the
answer and the code now says what it means.

The reproduction was written first and watched fail at `42bfc883`, at the HTTP
level because the 200 is what made it a defect — and the audit line flipped from
`outcome=ok` to `outcome=error` with it. One mutation turns two bodies red, one
of which names the file and line.

One assertion had encoded the bug: `store.TestTaskUpdateNonexistent` asserted
that patching a task nobody created does NOT error. It now asserts `ErrRefused`.

- **Fix:** the writers stop returning a bool a caller can drop. Either the helper
  takes what the caller expects and turns a mismatch into an error, or each site
  is audited and the unconditional ones say so in the code. The first makes the
  bug unexpressible; the second only fixes today's 15.

## ✅ FIXED 2026-09-10 L267 — a failed `arizuko apply` holds its scope forever, and no operator command releases it (2026-09-10)

REPRODUCED 2026-09-10 on a migrated copy of marinade. An `apply` that fails
part-way leaves the subsystem's version marker CLAIMED, which is by design —
`objstore/replace.go` says a half-written scope must not read as settled. Every
later apply then refuses:

```
Failed: pre-image routd: read resreg_scopes/routd consistently: another replace
holds the scope since 2026-09-10T09:48:39.745116010Z, so its rows are
part-replaced
```

`Replace`'s own doc says recovery is roll-forward. **Nothing implements
roll-forward.** `--force` does not clear it — `objstore/replace.go:49` notes
that Apply's force is an empty `wantVersion`, which defeats the version check
and not the holder check. `grep` finds no takeover, release or resume path
anywhere.

So one failed apply bricks `arizuko apply` for that subsystem permanently, and
the only escape is editing `resreg_scopes` by hand — which is exactly the
operator-reaching-into-the-database this whole layer exists to remove.

The failure that produced it is ordinary: I applied before running
`migrate-audit`, so the audit write hit a column table. Any half-finished
migration, any interrupted apply, any crash reaches the same state.

- **Severity:** high (one failure disables the config lane, no recovery)
- **Scope:** `objstore/replace.go`, `objstore/marker.go`, `resreg/engine.go` `Apply`, `cmd/arizuko/apply.go`
- **Affected:** every instance, the first time an apply fails
- **Source:** reproduced on a migrated marinade copy, 2026-09-10
- **Status:** fixed 2026-09-10

Fixed, and the shape is the interesting part: **a claim is not a lock and there
is no release.** Releasing a claim over a part-written scope is the exact silent
corruption the claim exists to prevent, so recovery is not "release" but
"finish".

The marker gained an `intent` beside `holder`, and two values are needed because
they answer two questions: the fence needs a value unique per ATTEMPT, so a
recovering replace does not pass the dead one's check, while the roll-forward
needs a value stable across attempts, because that stability IS the proof. A
replace whose intent matches the held one takes the claim over. Re-running the
same manifest, with no flags, rolls it forward.

The takeover is safe without proving anything about the dead process: a replace
makes a scope EQUAL a document, and the delete set is derived from the
manifest's own scopes and re-read live, so it lands where a clean run would
whether the dead apply stopped at row 1 or row 400. If the "dead" replace is
actually alive it gets `ErrClaimLost` and writes nothing.

**Liveness is deliberately not inferred.** A slow replace on another host and a
dead one look identical, and a sweeper acting on a timeout takes the scope from
a replace still writing it. `since` stays what an operator reads, never a
predicate the code evaluates. The other escape is an assertion, not an
inference: `arizuko apply <file> --take-over`, which settles the scope and
accepts that rows outside this document stay part-written. The refusal names
both intents and both escapes.

Reproduction written FIRST and watched fail at HEAD, with the real trigger (the
audit entity absent, so the last write dies after every row write lands). Two
mutations, both red on SQLite and Postgres: releasing a claim it cannot prove is
its own turns a 60-of-150-row scope SETTLED, and dropping the takeover's CAS
lets two recoveries hold one scope. Interleaves forced, never raced.

Rehearsed end to end on a marinade copy: apply fails, a DIFFERENT manifest is
refused naming both intents, then the same manifest with no flags exits 0 and
the second apply is a fixed point.
- **Fix:** the holder is recorded, so an apply of the same manifest can take its
  own claim over and roll forward. Failing that, an explicit operator command to
  release a claim it can prove is abandoned. A claim with no release is a lock,
  and `6/40` chose a marker precisely so it would not be one.

## ✅ FIXED 2026-09-18 L265 — two registrations declare fewer columns than their table has, and `routes` already loses two of them on every apply (2026-09-08)

MEASURED 2026-09-08, and my first count was wrong — I read a truncated grep and
reported 12. `pending_actions` declares **16 of 18**; only `mode` and
`notice_id` are missing, and routd writes both
(`routd/pending_actions.go:176`, `:215`).

**`routes` is the one that is already broken.** It declares 5 of 8 — `seq`,
`match`, `target`, `observe_window_messages`, `observe_window_chars` — and
omits `id`, `added_by` and `added_via`. It is NOT `SkipApplyRebuild`, so
`Apply` runs `DeleteAll` then `InsertAll` over five columns, and `added_by` and
`added_via` take their DDL defaults on every apply. The route attribution an
operator can see in the dashboard is erased by a config apply today, on the
column path, with no entity anywhere near it.

Three more resources declare fewer columns and are safe, because all three are
`SkipApplyRebuild` and the omitted columns hold sealed material:
`github_sources.secret`, `route_tokens.kind`/`token_hash`, and `secrets`'
five.

Today the column path writes only the columns it declares, so an `apply` leaves
the other six alone. That is why nobody noticed. The moment the resource becomes
an objstore entity it stops being survivable: an entity `Put` replaces the whole
attribute set, so an apply would erase a held action's mode, its notice, its
result and its expiry.

The registration is the contract for what an operator's manifest carries, so an
incomplete `RowType` is also an incomplete export: a held action round-tripped
through `export` and `apply` loses six fields today at the manifest level, even
though the row keeps them.

- **Status:** fixed in 3c6c6727
- **Re-measured 2026-09-18:** both declarations are complete today. `RoutesRow`
  names all eight columns and its own comment cites this entry; `PendingActionsRow`
  names all thirteen fields `routd.PendingAction` stores, and `mode` is gone —
  the three that remain (`Matcher`, `Unmatchable`, `MatcherError`) are DERIVED
  from `Args` by one renderer and were never stored. `routd/resource_columns_test.go`
  does not exist; it went with the column path.
- **What was missing was the guard.** `TestApply_RoundTrip_Routes` counted rows
  and nothing else, so the next omission would land exactly the way `added_by`
  and `added_via` did. It now applies a route carrying both and asserts they —
  and the `id` that `delete_route` addresses — survive the round trip. That is
  the shape `TestApply_RoundTrip_ProxydRoutes_PreservesRedirectTo` already used
  for the same class.
- **Severity:** high for `routes` (live attribution loss), medium for `pending_actions`
- **Scope:** `resreg/resources/routes.go`, `resreg/resources/pending_actions.go`
- **Affected:** every instance; `routes` holds 58 rows on krons
- **Source:** `routd/resource_columns_test.go` compares every `db:` tag against
  the live schema, and it is the check that should have existed all along
- **Status:** open
- **Fix:** the `RowType` declares every column, or the registration says which
  columns are deliberately outside the manifest and the entity writer preserves
  them. A test comparing `RowType`'s `db:` tags against the live schema would
  have caught this and should exist for every resource.

## ✅ FIXED 2026-09-18 L263 — `discd`'s typing-off test was flaky (2026-09-07)

Same cause as `L168`: a typing loop that outlived `Stop` sent one more time,
which the off-path test saw as a call it asserts never happens.

- **Status:** fixed in v0.68.2 with `L168` — `Stop` waits for its loops.
## ✅ FIXED 2026-09-08 L264 — `ReadConsistent` sees only the writers that remember to carry `Bump`, and one that forgets is silent (2026-09-07)

`objstore.ReadConsistent` detects a concurrent write by reading a scope's
version marker before and after its queries. The marker moves only because a
writer put `objstore.Bump(scope)` into its own batch. So the guarantee is a
DISCIPLINE, not an engine property: a bounded writer that forgets the extra
action is invisible, and the export it raced reads as consistent.

This is the shape of a missing audit row — the failure is silence, and it shows
up as data that disagrees rather than as an error. `store/audit_helpers.go`
solved the same problem by funnelling every audited mutation through one
`runAudited`, so no caller could forget.

The conformance suite asserts the limit honestly (a write without a `Bump` is
invisible, and the body says so), which is the right first move. It is not a
fix.

**No longer latent, and it cost a guarantee** (2026-09-07). `onbod`'s three
resources are now entity-backed, so `arizuko archive export` reads them through
`objstore.ReadConsistent`. The column path it replaced opened a read
transaction, which EXCLUDED a concurrent write. `ReadConsistent` sees only
writers that carry `Bump`, and `grep objstore.Bump` over non-test code outside
`objstore/` returns one comment and no writer. So an admission landing during an
export is invisible today, and the export reports itself consistent.

That is strictly weaker than what shipped yesterday, on an operator-facing
surface, and it is the first real cost of the port rather than a deferred one.

The fix is blocked on an import cycle: `store` cannot import `resreg`. The
shape that works is the one `resreg` already uses for the subsystem key — the
scope's constants live in `store`, `resreg` reads them the way it reads
`store.OwnerOnbod`, and `store`'s writers add the `Bump` action without
importing `resreg` at all.

- **Severity:** high (a shipped guarantee is weaker than it was)
- **Scope:** `objstore/marker.go`, `objstore/consistent.go`, `store/invites.go`, `store/onboarding.go`, `resreg/archive.go`
- **Affected:** `arizuko archive export` and `export` on onbod, today
- **Source:** `grep objstore.Bump` over non-test code, 2026-09-07
- **Status:** fixed 2026-09-08

Fixed by enforcement rather than by three call sites. `store/scopes.go` holds
`writeEntities`, `writeEntitiesInTx` and `incrementEntity`, and they are the ONLY
places in `store` that reach `GuardedBatch` — each derives the bump from the
entities its batch touches, so a writer cannot leave it out. `EnsureEntities`
creates the marker, so no opener can write into a scope that has none.

The scope declaration lives in `store/scopes.go` because `store` cannot import
`resreg` (`resreg/resreg.go:40` imports store); `resreg` reads
`store.OwnerScope` at its three call sites, the same way it already reads
`store.OwnerOnbod`.

The test was written FIRST and watched fail against unmodified HEAD — "the
consistent read reported 1 invites, want 2: a mint committed between the two
marker reads and the read did not run again" — then passed on both engines with
the interleave forced by `conformance.SlipAt` rather than raced. Four mutations
name it, including one that puts a raw `GuardedBatch` back into a writer:
`onboarding.go:697:18 calls GuardedBatch directly; it must call writeEntities`.

The scoped set is a declaration, not "every entity": `pane_sessions` and
`secret_use_log` are projected by no resource, and bumping routd for a secret
use would spend one of `ReadConsistent`'s attempts on every tool call.
- **Fix:** a scope's writes go through one helper that adds the `Bump`, the way
  `runAudited` owns the audit row, so forgetting is not expressible. Adopt it
  with the first real caller rather than before one exists.

## ✅ FIXED 2026-09-19 L260 — `route_tokens.PKFields` is not unique in live data, so `resreg.Diff` collapses 15 of 43 tokens (2026-09-07)

MEASURED on krons 2026-09-07: `route_tokens` holds 43 rows over 27 distinct
`jid` values, and `web:eval` alone carries 16. `PKFields` was `{"JID"}`, which
`resreg.Diff` treats as the manifest identity and `EmitRows` sorts by.

**The sort-key half was already safe.** `EntitySortKey` is a declaration of its
own (spec 6/40 §1) and `route_tokens` names `TokenHash` there, so no key was
ever derived from `PKFields`. Only the manifest identity was wrong, which is
why the harm stayed bounded: `SkipApplyRebuild` keeps `apply` off the table,
and `packages` refuses a `SkipApplyRebuild` resource outright, so `MergeRows` /
`DropRows` / `PKs` never reach these rows either.

**Where the rows were actually lost.** `engine.go` `byPK` is last-writer-wins
on a duplicate PK, so `Diff` indexed 16 credentials under `web:eval|` and named
one. `EmitRows` drops nothing — what it lost was the ORDER: `sortByPK` is a
STABLE sort, so tied rows kept whatever order the scan handed them, and that
order is the export's bytes, which are the subsystem checksum. A reversed copy
of the same four rows emitted a different document (proven in the test below).

**What the proposed fix alone would have broken.** `TokenHash` is `yaml:"-"`,
so no manifest ever carries it. Moving `PKFields` to it makes the LIVE index
right and the MANIFEST side degenerate — every parsed row reads back as the
empty identity — so `arizuko plan` against a full export would have printed
`route_tokens: 1 set` where it printed `27`, against a true 43. `invites` has
had that shape all along for the same reason. So the plan line's count had to
stop being a PK count; it is now the live row count.

Checked and NOT readers of this resource's `PKFields`: REST path params (parsed
out of `Endpoint.Path`), the MCP tool args (explicit `MCPArgs`), the
`/openapi.json` schema, and the archive — which round-trips route tokens
through `store.ArchiveRouteTokenRow`, a separate document that already carries
`token_hash`.

- **Severity:** medium now, high at the move
- **Scope:** `resreg/resources/route_tokens.go`, `resreg/entity.go` `initEntity`,
  `resreg/engine.go` `Diff`, `cmd/arizuko/apply.go` `printPlan`
- **Affected:** krons measured; any instance with more than one token per jid
- **Source:** `SELECT count(*), count(DISTINCT jid) FROM route_tokens`
- **Status:** fixed; no schema migration and no row was rewritten — the change
  is the manifest identity, not the stored data. `SkipApplyRebuild` is untouched.
- **Fix:** `PKFields` is `{"TokenHash"}`, the coordinate the items are already
  keyed on, which is exactly how `invites` declares its `ref`. `initEntity`
  now REFUSES at registration any entity-backed resource whose `PKFields` does
  not name every `EntitySortKey` field: the coordinate is unique by
  construction, so a PK covering it is unique too, and that is the checkable
  stand-in for the uniqueness nothing can check. `route_tokens` was the only
  resource in the catalog that failed it. `ResourceDelta.Live` carries the
  scanned row count and `printPlan`'s informational line reports it, so the
  number means rows held rather than identities parsed.
  `TestRouteTokens_ManyTokensOnOneJIDStayDistinct` seeds three tokens on one
  jid plus one on another and pins both halves — `Diff` names 4 of 4 (it named
  2) and `EmitRows` of a reversed copy is byte-identical (it was not);
  `TestEntity_RegisterRefusesAMisdeclaredCoordinate` gains the narrow-PK case;
  `TestPrintPlan_SkipApplyRebuildCountsLiveRows` pins 43 live against 1 parsed
  identity. Commit SHA: (pending).
- **Know before deploying:** the routd checksum MOVES, because `EmitRows` now
  orders these rows by `token_hash` instead of by `jid`. Nothing persists a
  checksum — it is recomputed from live rows at read time — so the only effect
  is that an export taken before this change fails the CAS check of an `apply`
  run after it, until it is re-exported. Loud, and one command to clear.

## ✅ FIXED 2026-09-20 L261 — every route on krons sits at `seq = 0`, so dispatch order is decided by an autoincrement tie-break (2026-09-07)

MEASURED on krons 2026-09-07: `routes` holds 58 rows and `count(DISTINCT seq)`
is 1 — every row has `seq = 0`. `ORDER BY seq, id` therefore resolves entirely
on `id`, which is `INTEGER PRIMARY KEY AUTOINCREMENT`.

`6/37` rule 4 says never order by a generated id, because the id is an artefact
of insert order rather than a fact anyone declared. Routing order is exactly the
kind of decision an operator should be able to state and read back. Today it is
whatever order the rows happened to be inserted in, and re-importing an archive
in a different order silently reorders dispatch.

`delete_route(id)` is a live MCP wire contract, so the id must survive as a
value; the point is that it must stop being the ORDER.

- **Severity:** medium (latent reorder on any re-import)
- **Scope:** `store/routes.go`, `resreg/resources/routes.go`, `cmd/arizuko` route verbs
- **Affected:** krons measured, 58 rows
- **Source:** `SELECT count(*), count(DISTINCT seq) FROM routes`
- **Status:** open
- **Fix:** `seq` carries a declared order — the route verbs set it — and the
  entity sorts on a padded `seq` so string order is numeric order


- **Status:** closed on re-check 2026-09-20 — the premise is false, and shipping
  the proposed fix would CONTRADICT a written decision.
- **Re-measured:** `id` is no longer an autoincrement. `store/routes.go:22-27`
  mints it through `store.Counters` compare-and-swap, because an entity has no
  AUTOINCREMENT and two concurrent `add_route` calls reading `max(id)+1` would
  hand one handle to two rules. The padded-seq half is shipped: `padSeq` and
  `routeSeqWidth = 10` (`store/routes.go:62-105`), with `db:"seq,pad"` at
  `resreg/resources/routes.go:39`. An operator can already declare order with
  `arizuko route --seq` (`cmd/arizuko/route.go:62-63`). A re-import preserves
  `id` and carries the counter past it (`store/import_routd.go:301-348`), so the
  entry's concrete risk — re-importing in a different order reordering dispatch
  — does not happen.
- **The tie-break is deliberate.** `store/routes.go:16-21` states why: every
  live instance carries almost every rule at seq 0 (58 of 58 on krons), so
  sorting by the coordinate would re-rank those 58 rules alphabetically and
  change which one answers a chat. Taken during the entity migration
  (`1628b3bd`) to avoid exactly that re-rank.

## ✅ FIXED 2026-09-07 L262 — the storage-layer guard uppercases a line before matching, so prose reads as SQL (2026-09-07)

`db_utils/sql_layer_test.go:164` does `strings.ToUpper` before testing a string
literal against the SQL verb list. So an MCP tool description beginning "Update
fields on an existing proxyd route." matches `UPDATE `.

One instance in the live tree — `resreg/resources/proxyd_routes.go:42` — and it
is enough to block `resreg` from joining `sqlLayers`. The guard is meant to be
exact: it already parses with `go/parser` precisely so a comment quoting a query
is not mistaken for one, and then throws that precision away on the case fold.

- **Severity:** low (a false positive, never a false negative)
- **Scope:** `db_utils/sql_layer_test.go`
- **Affected:** any package whose prose starts with a SQL verb
- **Source:** `resreg/resources/proxyd_routes.go:42`, measured 2026-09-07
- **Status:** open
- **Fix:** match the verb case-sensitively. Every statement in the tree is
  written in upper case, so the fold buys nothing and costs this.

Fixed 2026-09-07. Verified both ways rather than asserted: a lowercase
`select kid from signing_keys` pasted into an authd handler is now MISSED, and
the upper-case form is caught — so the change is a real narrowing and the
comment says so. The narrowing is safe because it was measured: every SQL-verb
string literal in the tree outside `third_party/` and `refs/` is upper case, and
every lower-case hit is an error message or a test fixture.

## ✅ FIXED 2026-09-18 L259 — `resreg` had one row reader and it built column SQL (2026-09-07)

Fixed: the column-shaped reader is deleted, and with it `Querier`, `filterSQL`,
`whereFilter`, `orderBy` and `scanTargets`. Every resource an owner subsystem
declares is entity-shaped, and `resreg/resources/shape_test.go` is the lock.

- **Status:** fixed in v0.68.0
## L257 — 39 session rows on sloth carry a `started_at` that runs backwards against their id sequence (2026-09-07, open)

MEASURED 2026-09-07 while rehearsing `arizuko migrate-runed` against copies of
all three live snapshots. On sloth, 39 `session_log` rows in one folder on one
day carry a `started_at` one to three seconds EARLIER than the row before them
in the id sequence. krons and marinade have none.

`session_log`'s identity becomes a time key on the move, so the id order and
the clock order must agree, and here they do not. The migration is right and the
first verifier was wrong: every reader of this history renders `started_at`, so
the instant is the order a reader means. The tool now compares against the key's
order and reports what moved — 118 sessions change position across the fleet.

What is not explained is why the clock ran backwards for one folder on one day.
`StartSession` stamps `started_at` from the daemon's own clock, so either the
host's clock stepped, or rows were written out of order by something that does
not stamp at insert time.

- **Severity:** low (history only; no cursor reads `session_log`)
- **Scope:** `runed/runstore/session_log.go`, `cmd/arizuko/migrate_runed.go`
- **Affected:** sloth, measured; 39 rows
- **Source:** the migration's own order check, 2026-09-07
- **Status:** open
- **Fix:** none needed for the port. Worth knowing whether the host clock stepped,
  because anything else stamped from the same clock moved with it.

## ✅ FIXED 2026-09-18 L258 — `dashd` opened `runed.db` and nothing read the handle (2026-09-07)

Fixed: `dash.dbRuned` is gone, and so is `runed.db`. dashd reads runed over its
HTTP face and holds no handle on its store.

- **Status:** fixed in v0.68.0
## L253 — three tables survive a folder delete on the strength of an `ON DELETE CASCADE` a document store does not have (2026-09-07, open — proposal, needs sign-off)

`store/folder_scope.go` is a TOTAL classification of every table into purge or
keep, with a completeness test in both `store` and `routd`. Three tables sit in
`folderScopeKeep` and the reason given IS the mechanism:

```
"web_routes":     "FK groups(folder) ON DELETE CASCADE"
"route_tokens":   "FK groups(folder) ON DELETE CASCADE; a token naming a gone target is refused at webd ingress (BUGS L5)"
"github_sources": "FK groups(folder) ON DELETE CASCADE"
```

`store.DeleteGroup` therefore issues no statement for them: SQLite's foreign
keys do the work, and `db_utils.FileDSN` turns `foreign_keys` on so they fire.

A key-value engine has no foreign keys and no cascade. The moment `groups`
becomes an entity behind `objstore`, those three tables stop being cleaned by
anything — and the completeness test still passes, because the tables are still
CLASSIFIED. The guard checks that every table has a verdict, not that the
verdict's mechanism exists.

`route_tokens` is the one that matters: it is a live ingress credential for
`/chat/<token>/` and `/hook/<token>`. Its keep-reason names a second guard —
webd refuses a token whose target folder is gone — so the credential does not
become a live door. It becomes an orphan row that no delete removes, forever,
on a surface whose whole point is that deleting a folder removes the tenant.

- **Severity:** high (a folder delete stops being complete the moment routd moves)
- **Scope:** `store/folder_scope.go`, `store/groups.go` `DeleteGroup`, `specs/6/40` §3
- **Affected:** every instance, once `routd` moves onto the object store
- **Source:** `store/folder_scope.go:123-125`; `db_utils.FileDSN` sets `foreign_keys(on)`
- **Status:** open — proposal, needs sign-off
- **Fix:** the classification records the MECHANISM, not just the verdict, and the
  completeness test refuses a keep-reason naming a cascade once the owning table
  is an entity. Then those three move to `folderScopePurge` with the rest.

## L254 — "the point of no return arrives once per daemon" is false for anything that reaches through `store` (2026-09-07, open — proposal, needs sign-off)

`6/37`'s order says each daemon moves on its own and reverts on its own.
Measured 2026-09-07: **18 packages import `store`** — auth, authd, cmd/arizuko,
compose, container, dashd, dashd/dashstore, gitd, onbod, proxycfg, proxyd,
resreg, routd, runed, slakd, webd and two test packages — and **18 packages
name `routd.db`**. `store` runs 150 to 178 statements depending on how they are
counted, `resreg` 18, `audit` 2.

So a daemon does not move alone. It moves when the shared package it reads
through moves, and the shared package cannot move until every consumer is ready.
`6/37` step 6 puts `store` LAST, which schedules the eighteen-consumer change as
the final step — the one big bang the per-daemon order exists to avoid.

The order that actually reverts per daemon is the opposite: the shared packages
move FIRST, behind an interface each consumer already calls, so a consumer's own
move is then local. That is a different plan, not a reordering of this one, and
it is the decision this entry asks for.

- **Severity:** high (the order's central property does not hold)
- **Scope:** `specs/6/37-store-model.md` §Order, `store/`, `resreg/`, `audit/`
- **Affected:** every unit after `authd`
- **Source:** measured importer census, 2026-09-07
- **Status:** open — proposal, needs sign-off
- **Fix:** move `store`, `resreg` and `audit` first, or accept that the port is
  one cutover for eighteen packages and plan the rehearsal accordingly

## ✅ FIXED 2026-09-07 L255 — `objstore`'s package doc and README say three protocols are "written once over the two primitives", and none of them exists (2026-09-07)

`6/40` answers its own three hardest shapes — the versioned replace, the
reachability guard, and claim-and-return — by making them PROTOCOLS: package
functions written once over `Query` and `GuardedBatch`, shared by every driver.
`objstore`'s documentation repeats that.

`grep '^func ' objstore/*.go` returns exactly one function, `ValidateBatch`.

Nothing is wrong with the design; the documentation states a thing that has not
been built as though it had been, which is the shape that makes a reader stop
looking. The three shapes are also the ones every later daemon needs:
`resreg.Apply` is the only versioned-replace caller, the membership walk has
four copies, and `routd/pending_actions.go:421` already has the claim-and-return
form in SQL.

- **Severity:** medium (documentation asserts unbuilt code)
- **Scope:** `objstore/objstore.go` package doc, `objstore/README.md`
- **Affected:** anyone reading the package to find the protocols
- **Source:** `grep '^func ' objstore/*.go` → one result, 2026-09-07
- **Status:** open
- **Fix:** write the three, or state plainly that they are the next thing and
  which caller each one is derived from

Fixed 2026-09-07. `objstore/marker.go` carries the item all three share — a
scope with a `version` every writer moves and a `holder` present only while a
replace runs. `replace.go` is the versioned replace, `consistent.go` the read
twin, `reach.go` the reachability guard. Sixteen mutations turn a conformance
body red on both engines, and two of them found real gaps rather than noise: the
claim's `holder Absent` conjunct was dead because every claim moves `version` in
the same write, and the finish CAS was unprotected because the fence test's body
returned an error before reaching it. Both conjuncts deleted, both bodies
rewritten.

The fourth, claim-and-return, is deliberately NOT written: its caller
(`routd/pending_actions.go:421`) has not moved, and the package doc now names it
as absent rather than claiming it. No adoption happened — `acl_membership` is
raw SQL at eight sites in `onbod/main.go` alone, and `resreg.Apply` still takes
a `*sql.DB` (`L259`).

## L256 — the membership cycle check runs inside its own insert's transaction, and `Check` cannot express the graph frontier it needs (2026-09-07, open — proposal, needs sign-off)

`store/membership.go:36`, `:151`, `:199`, `routd/acl_resource.go:361` and
`resreg/resources/membership.go:88` all run the same recursive walk — four
copies of one query — and the insert path runs it INSIDE the transaction that
adds the edge, so a cycle cannot be created.

`objstore.Check` binds an ITEM: this key, this condition. A cycle check is a
condition on a graph frontier, which is not an item. Two concurrent calls that
each close the cycle from opposite ends would both walk a graph without the
other's edge, both pass, and both commit.

Today SQLite's single writer hides this: the second transaction waits and walks
the graph the first one left. Postgres at READ COMMITTED does not, and neither
does a key-value engine.

`specs/15/h` measured the graph on all three live instances: 58 edges, exactly
two parents, and no parent is ever itself a child. So there is no cycle to close
today and this is latent, not live. It stops being latent the moment a folder is
made a member of something.

- **Severity:** medium (latent; the graph has no depth on any live instance)
- **Scope:** `store/membership.go`, `routd/acl_resource.go`, `resreg/resources/membership.go`
- **Affected:** any instance whose membership graph gains a second level
- **Source:** four copies of the recursive walk; `objstore/batch.go` `Check`
- **Status:** open — proposal, needs sign-off
- **Fix:** a version attribute on the graph that every edge insert conditions on,
  so two concurrent inserts serialise. That is `6/40`'s reachability protocol,
  and `L255` is why it does not exist yet.

## ✅ FIXED 2026-09-07 L252 — three folder-containment filters build the LIKE wildcard in Go, so `L122`'s "all six fixed" grep could not see them (2026-09-07)

MEASURED 2026-09-07. `L122` closed on 2026-08-29 by adopting
`groupfolder.SQLSubtree` at six sites, and verified itself with
`grep "LIKE ?||'/%'"` over the tree. That grep matches the wildcard built in
SQL. Three sites build it in Go and pass it as a bind value, so the grep
returned clean over code that still uses `LIKE` for containment:

| site | what it scopes | what widens |
| ---- | -------------- | ----------- |
| `routd/dead_letters_db.go:27` | `routed_to` on a grant-scoped read | a grant on `acme` also returns `acme_x`'s dead letters, and `ACME/...` |
| `dashd/dashstore/pages.go:91` | the dashboard's routes-into-folder panel | a sibling `a_b`/`axb` pair, and `Coach`/`coach` |
| `store/groups.go:307` | `SiblingFolders`, the open-sibling list | nothing — see below |

`dead_letters_db.go:27` carries the comment "Subtree, not exact: a grant on
`acme` covers `acme/support`", which names the intent exactly and then spells it
with the wrong predicate. `isValidFolder` (`groupfolder/folder.go:59`) rejects
only `..`, backslash, NUL, empty segments, over-128-byte segments and the
reserved names, so `%` and `_` are both legal in a folder name, and SQLite
`LIKE` folds ASCII case — a live instance has carried a `Coach`/`coach` pair.

The dashd one predates the storage move: `dashd/main.go:1204` held the same
predicate before `359da435`, and the extraction carried it across unchanged.

The lesson is the verification, not the three sites. A fix that proves itself
with a grep proves only what the grep can express. `SQLSubtree` adoption has a
mechanical test — mutate the helper back to `LIKE` and every adopter goes red —
and these three sites do not participate in it because they never call it.

- **Severity:** high (`dead_letters_db.go` is a grant-scoped cross-folder read)
- **Scope:** `routd/dead_letters_db.go`, `dashd/dashstore/pages.go`, `store/groups.go`
- **Affected:** any instance with a folder name differing only by case or holding `_`/`%`
- **Source:** `grep '+ "/%"'` over non-test Go, 2026-09-07
- **Status:** open
- **Fix:** the same adoption `L122` made — `groupfolder.SQLSubtree(col, "/")` plus
  `SubtreeArgs`. Then extend `store/subtree_filter_test.go`'s mutation test to
  cover all three, so the next site cannot hide from the check.

Fixed 2026-09-07 (`5f5fdd7a`). All three call `SQLSubtree`/`SubtreeArgs`.
`SiblingFolders` also excludes the parent by name, because `SQLSubtree` matches
the parent itself and `LIKE parent||'/%'` did not.

**Two of the three were live; `SiblingFolders` was not.** Measured by mutating
`SQLSubtree` back to `LIKE` and re-running: `dead_letters`, the dashd routes
panel, the FTS folder scope, the errored-chat list and `ListRoutes` all go red.
`SiblingFolders` stays green, because `store/groups.go:326` already re-checked
every returned row with `groupfolder.ParentOf(f) != parent` — an exact,
case-sensitive Go comparison that rejects exactly what the widened predicate
returned. The SQL widened and the Go filter caught it, so no caller ever saw a
stranger's folder.

That makes its test unfalsifiable by the mutation, so it is named for what it
can prove — `TestSiblingFoldersKeepOnlyExactChildren` — rather than claiming a
predicate guarantee it does not carry. Adopting `SQLSubtree` there is still
right: one predicate, one declaration, and the Go filter stops being the only
thing standing between a widened scope and the caller.

## ✅ FIXED 2026-09-18 L251 — the storage-layer guard read string literals (2026-09-07)

Moot: `db_utils/sql_layer_test.go` is deleted, and the guard it was is now one
line — `go list -deps ./... | grep database/sql` is empty, and it sees a handle
passed across a package boundary, which the string guard could not
(spec `6/42` §9).

- **Status:** moot in v0.68.0
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

- **Severity:** high (blocks `6/37` step 4 — the per-daemon export/import port)
- **Scope:** `resreg/resources/`, `cmd/arizuko/archive.go`, `specs/6/37`
- **Affected:** every daemon port after `dashd`; a cutover would reset cursors
- **Source:** measured `arizuko archive export` vs `routd.db` table census
- **Status:** open — proposal, needs sign-off
- **Fix:** resreg resources for the four runtime tables, so one lane carries them

## ✅ FIXED 2026-09-12 L250 — migration 0044 pads RFC3339 timestamps and silently leaves any other shape unpadded (2026-09-07)

MEASURED on a migrated krons snapshot: after `routd/0044`, two `messages` rows
still carry a 19-character timestamp, `'2026-04-02 12:38:47'` — SQLite's
`datetime()` shape, no `T` and no zone. Their ids are `test-trigger-1775133527`
and `debug-1775135388`, so a person inserted them by hand in April 2026.

`core.TSLayout` is fixed width precisely so string order is time order. A space
(0x20) sorts before `T` (0x54), so these two rows sort ahead of every real
message of that date.

**It was not harmless, and I said it was** (2026-09-09). Those two rows are what
`DeriveAgentCursors` met when `chats` became an entity: `MAX(timestamp)` returned
the 19-byte value, the ordered-width declaration refused it, and the operation
aborted — leaving every alphabetically LATER chat with no cursor at all, which is
the precise state deriving cursors exists to prevent.

- **Severity:** medium — two rows on one instance, and they aborted a fleet operation
- **Scope:** `store/import_hot.go`, `store/messages_archive.go`
- **Affected:** krons only, measured
- **Status:** fixed
- **Fix:** the silence is gone because the pad is gone. `store.ReadMessageInstant`
  is the one parser for a row and for a cursor; it reads the naive datetime as
  UTC, which is the zone the instance has always written, and it REFUSES any
  other shape by name. VERIFIED on a sloth copy with one row set to
  `not-an-instant`: the move aborts with `message "255" in chat
  "telegram:group/5069669817"` and sets nothing aside, so no half-moved database
  exists to recover from.
## ✅ FIXED 2026-09-18 L240 — the egress proxy and its spawn drew addresses independently (2026-09-06)

Two independent draws over ~250 usable hosts collided about once in 250, and the
collision was silent: docker gave both containers the same address and the
folder's egress stayed broken until the next turn re-rolled.

- **Status:** fixed in v0.68.2
- **Fix:** the cause fix this entry named. `pickPair` draws ONCE and derives the
  proxy as the spawn's neighbour, wrapped inside the subnet's usable range, so
  sharing an address is impossible rather than improbable. The retry-until-they-
  differ it replaces was the symptom patch. `crackbox/pkg/run` carried the same
  two-draw shape and takes the same fix.
- **Test:** `container.TestPickPair_NeverSharesAnAddress` — 2,000 draws, each
  pair distinct, in range and off the reserved ends.
## ✅ FIXED 2026-09-05 L165 — an unknown `MCPArg.Type` silently declares a string property, so the argument never arrives (2026-09-05)

`mcpArgOption` (`resreg/resreg.go:805-824`) switches on `"number"`, `"bool"`,
`"array"` and falls through its `default` to `mcp.WithString` for everything
else. A Type nobody typed correctly is therefore not an error: the tool schema
advertises a string, the caller sends a JSON bool, and the argument is absent
from `Args` — the handler writes its zero value and answers `{"ok":true}`.
Found live while fixing `grant_option`: declared `Type: "boolean"`, the
`add_acl` write was a no-op with no error on any surface, and the only symptom
was a stored `0`. Two other resources spell it `"bool"`
(`pending_actions.go:73`, `proxyd_routes.go:60,69`), so the correct spelling
existed and the wrong one still registered cleanly. This is the silent-fallback
class root `CLAUDE.md` forbids ("Strict, not magical").

- **Severity:** medium (a mis-typed argument is silently dropped on the agent's
  own management surface; the write reports success)
- **Scope:** resreg MCP tool derivation
- **Affected:** every resreg resource declaring `MCPArgs`
- **Source:** `resreg/resreg.go:805-824`; measured 2026-09-05 against `add_acl`
- **Status:** fixed. `Resource.checkMCPArgTypes` runs from `initMeta`, the
  existing panic-on-misconfiguration path, so an unknown Type is a build
  failure — every `Register` call is `init()`-time.
- **Fix:** `fix(resreg): reject an unknown MCPArg.Type at registration`
## L169 — a Discord `reply` quoting a message outside its thread was refused, labelled 502, retried ten times and re-posted top-level; the agent re-sent it too (2026-09-05, partial)

Measured on sloth, channel `920916162416635944`, 11:39–11:47 UTC: 14 `mcp
deliver failed` and 22 sweep retries, every one `channel discord send: status
502`; ten rows still `pending` at `delivery_attempts=6`. Every refused row
carries `reply_to_id` = a parent-channel message AND a thread destination
(`topic` = a thread, or `ThreadRoot` = the trigger); every success in the same
minutes had no reference or a same-thread one. A 22-hour-old thread with fresh
human activity (`1545429028956012584`) and a 4-minute-old one
(`1545760882322182276`) refused the same pair, so archive state and message age
are not the variable; the cross-channel reference is. Discord resolves a
`message_reference` inside the destination channel only and answers 400 code
50035 "Unknown message". The journal cannot show that text: the body was dropped
(below).

The chain, each link measured:

1. `discd.classifySendErr` wrapped the Discord 4xx as a plain error → chanlib
   502 → `chanreg.classifyStatus` "transient", body dropped → the routd sweep
   retried it ten times and the agent read "status 502".
2. `maybeRetryOutbound` (`routd/loop.go:428`) re-sends with `m.Topic` only, no
   `threadRoot`. A row from a parent-channel turn has `topic=''`, so the sweep
   posted it in the parent channel — the four `sent` rows with `reply_to_id`
   set and `topic=''`, delivered 11:44:40 and 11:45:12 by platform-id
   snowflake, 52 s and 78 s after the tool call. That is "answers in the main
   channel instead of the thread": the first reply of the turn rooted the
   thread, the next two fell out of it.
3. The tool told the agent "502"; the agent called `reply` again with the same
   text; the sweep delivered both — the repeats. Recorded under `L150`.

Fixed here: (1) discd sends the reference with `fail_if_not_exists=false`, so
the text posts in the thread without the quote; a Discord 4xx maps to
`chanlib.ErrInvalidRequest` (400 → dead-letter, reason in the text); chanreg
carries the adapter's `{"error":…}` body up. Over the seven days before, the
discd inline fall-through (`thread start failed`) fired 0 times and the sweep
dead-lettered 0 rows; all 14 failures are this class.

**Open — proposal.** Link 2 remains for a genuinely transient failure on a
thread-rooting reply: the pending row does not carry the thread it was meant to
root, so the sweep cannot put it back. Shape: `deliverRow` (`routd/turns.go`)
stamps `row.Topic = threadRoot` on the pending row too, and `mcpDeliver` returns
the root so `putOutboundRow` (`ipc/ipc.go`) stamps it — then the sweep's
`ThreadID` is the thread. A thread the first reply never created answers 404,
which now dead-letters visibly instead of posting in the wrong place. Same hole
on Slack (`slakd/bot.go:1211` takes `ThreadID` or `ThreadRoot`; the sweep sends
neither for a `topic=''` row).

- **Severity:** high
- **Scope:** discd send, chanreg error transport, routd outbound sweep
- **Affected:** sloth; every Discord instance
- **Source:** `journalctl -u arizuko_sloth` 2026-09-05T11:39:58Z–11:47:25Z;
  `messages` rows `mcp-1788608398191977579` … `mcp-1788608845928580763`;
  `discd/bot.go:416,719`, `chanreg/httpchan.go:206`, `routd/loop.go:428`
- **Status:** partial — links 1 and 3's Discord case fixed; link 2 is a proposal
- **Fix:** (this commit) for link 1

## L166 — the MCP outbound row records neither the thread it rooted nor its reply target; the REST row does (2026-09-05, open — proposal)

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

## L167 — `reply(replyToId=<parent-channel message>)` from inside a thread: the tool doc promised a quote Discord cannot make, and which thread it should go to is undecided (2026-09-05, open — proposal)

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
- **Status:** proposal (needs sign-off)
- **Fix:**

## ✅ FIXED 2026-09-18 L168 — `TestTyping_DebouncesAndSends` failed the race detector 2 runs in 3 (2026-09-05)

`TypingRefresher.Stop` closed each loop's stop channel and returned. Closing
only ASKS: a loop already inside `send` kept calling the platform after Stop
returned. In the test that goroutine read the package-level
`discordgo.Endpoint*` var while the next cleanup wrote it; in production it is
an outbound call outliving the shutdown that was supposed to end it.

- **Status:** fixed in v0.68.2
- **Fix:** the refresher counts its live loops and `Stop` WAITS for them, so Stop
  means stopped for every adapter rather than only for this test.
- **Measured after:** `go test ./discd/ -race -run TestTyping` three runs, three
  passes, where the entry records FAIL, FAIL, ok.
## ✅ FIXED 2026-09-20 L170 — `routd.DB` and `store.Store` declare 66 method names twice with different SQL; routd's `AllGroups` hands its own callers `Open=false` and no observe window (2026-09-05)

Measured at `c0594f32` with a receiver-normalised diff of every method name
both types declare: 69 shared, 3 textually identical (`Close`,
`JIDRoutedToFolder`, `TopicLineage`), 66 different. Two differences change
behaviour. routd's `PutGroup` (`routd/db.go:115`) merges `config.$.model` with
`json_set` and writes no audit row; `store.PutGroup` (`store/groups.go:21`)
replaces `container_config` and audits `group.upsert`. routd's `AllGroups`
(`routd/db.go:170`) reads four columns, so every routd caller —
`groups_resource.go:60` (the agent's `groups` list), `dispatch.go:343` (the
`available_groups` snapshot the container reads), `steer.go:163`,
`loop.go:517`, `dash.go:57` — sees `Open=false` and a zero observe window
where `store.AllGroups` (`store/groups.go:148`) reads both. `store.New(d.db)`
also carries neither routd's secret keyring nor its audit actor, so forwarding
the secret and audited writers changes what they seal and who they record.
Spec 6/36 S3 ("`routd.DB` forwards every shared name to `store`") is not a
mechanical change: each of the 66 is a decision on which body survives.

- **Severity:** medium (two writers with different semantics over one file; the agent's groups snapshot lacks `open`)
- **Scope:** routd data layer vs `store`
- **Affected:** routd; dashd, webd, proxyd and the CLI as the `store` side
- **Source:** `routd/db.go:115,170`; `store/groups.go:21,148`
- **Status:** fixed. Closed on re-check 2026-09-20 — `d783394e`, "refactor: SQL
  leaves the dependency graph", removed the second body.
- **Fix:** RE-MEASURED 2026-09-20. Both behavioural differences are gone, and
  the premise the entry rests on — two bodies with different SQL — no longer
  describes the code.

  - `routd/db.go:146` `PutGroup` is `return d.store().PutGroupRow(g)`. One
    writer, one audit decision.
  - `routd/db.go:173` `AllGroups` is `return d.store().AllGroups()`, which
    decodes through `groupFrom` (`store/groups.go:112`) — the same decoder
    `GroupByFolder` uses. It sets `Open` (absent means OPEN) and both observe-
    window fields (unset is -1). Every routd caller listed above now reads them,
    and `WriteGroupsSnapshot` serialises whole `core.Group` values, so the
    agent's `available_groups.json` carries `open`.
  - `json_set` appears once in the whole tree, inside a comment in
    `store/groups.go:462`. No statement uses it.

  `routd/db.go` declares 90 methods; ~78 are one-line forwards to `store()` and
  about twelve do routd-specific work. That layer is a wrapper-type question
  (`~/.claude/skills/go` ¶ "Wrapper types vs functions"), not two writers over
  one file, so it is not a bug. Collapsing it is a cross-package refactor and
  needs sign-off before it ships — spec `6/36` S3 still holds the question.

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

## ✅ FIXED 2026-09-18 L172 — `InboundAttachment.Size` was set by four adapters and read by nobody (2026-09-05)

`routd/api/v1.Attachment` has no `Size`, so every adapter that filled it handed
the value to a JSON encoder that dropped it.

- **Status:** fixed in v0.68.2
- **Fix:** the field goes, and with it the writes in bskyd, discd, slakd and
  teled and the three tests that asserted a value nothing carried.
- **Also:** `chanlib.UserAgent` was frozen at `arizuko/0.54.0` through fourteen
  releases — the one place arizuko names its version to a third party, and it
  was false. It carries no version now: a second copy of the number is a second
  thing to keep in step with the one the docs ship.
## ✅ FIXED 2026-09-05 L173 — `idempotency_keys` has no writer; the hourly sweep, its test and the table outlive the ledger (2026-09-05)

The REST turn face was the ledger's only writer; with it gone (`fa00076d`)
`SweepIdempotency` (`routd/tokens.go`) deletes from an always-empty table
every hour, `TestSweepIdempotencyDropsExpired` seeds it by raw SQL, and
`routd/migrations/0001-initial-schema.sql` still creates it. The cut is a
migration dropping the table, the `SweepIdempotency` call in
`routd/loop.go:498` and the test. Deferred 2026-09-05 because `loop.go` was
in another edit set that day.

- **Severity:** low
- **Scope:** routd schema
- **Affected:** routd
- **Source:** `routd/tokens.go` `SweepIdempotency`; `routd/loop.go:498`
- **Status:** fixed — migration 0042 drops the table (0043 takes `pane_sessions.last_status_at`, the same writer-less shape, found by a column census); zero rows on krons, marinade and sloth
- **Fix:** dde8683f
## L174 — gateway-measured spend has no path into `cost_log`: the litellm package exports OTLP spans and nothing arizuko runs reads them (2026-09-05, open — proposal, needs sign-off)

`cost_log` has two writers, both in routd: `recordTurnResult` books
`req.Models` from the agent's `submit_turn` (`routd/turns.go:703`), and
`LogExternalCost` behind `POST /v1/cost` (`cost:write`,
`routd/reads_http.go:215`) plus the `log_external_cost` MCP tool. dashd reads
the one summary face, `GET /v1/cost/summary`. With `MODEL_GATEWAY_URL` set the
gateway sees every model request and none of that reaches either writer, so
the cap still enforces against the spender's own report — 6/23's Fault 2,
unchanged by 6/23 shipping. `tests/e2e_turn_test.go` exhibits the gap end to
end: a turn whose model call fails books no row and the folder's spend view
does not move, although the endpoint served the request.

Measured 2026-09-05 against `ghcr.io/berriai/litellm:main-latest` (1.101.0)
with the package's no-Postgres config and a local upstream + HTTP sink: the
proxy forwards `/v1/messages` upstream with the injected credential (the
upstream saw `x-api-key`); `success_callback: ["generic"]` with
`GENERIC_LOGGER_ENDPOINT` starts (`Initialized Success Callbacks -
['generic']`) and delivered nothing to the sink after a `/v1/messages` and a
`/chat/completions` request and 20 s of waiting (cause not determined); the
JSON access line for a request carries the status and no usage;
`/spend/logs` needs Postgres. So today the ONLY per-request usage export is the
OTLP span, and arizuko has no receiver — by design (`CLAUDE.md`: "audit_log
stays SQLite-canonical; OTLP is observability only").

Two gaps, in order:

1. **Attribution.** The spec's shape — a per-spawn virtual key the gateway
   meters by — needs Postgres in litellm. Without it every request carries
   the master key and the only per-request identity is what the container
   sends (a header: `ANTHROPIC_CUSTOM_HEADERS` for claude, `http_headers` on
   the codex provider, the pi provider's headers), set by the metered party.
   The AMOUNT would be gateway-measured; the FOLDER would still be
   self-reported.
2. **Transport.** The gateway → routd carrier does not exist. Candidates: (i)
   a litellm custom callback (one python file mounted through `configs:` in
   `template/services/litellm.yml`) that POSTs `apiv1.CostRequest` to the
   existing `POST /v1/cost` with a `cost:write` service bearer — amends the
   original write path, no second table, no second reader; (ii) an OTLP
   receiver in arizuko — a second accounting substrate, declined above.

Proposal, needing sign-off because it changes the `/v1/cost` contract and
the trust model: (i) for transport; for attribution, `POST /v1/cost` accepts an
optional `turn_id` the callback forwards from a request header the harness
sets, and routd takes the FOLDER from its own turn context
(`GetTurnContext`), never from the header — the container can then mislabel
only onto a live turn id it can name, not onto an arbitrary folder. The
agent's `req.Models` degrades to corroboration and a disagreement between the
two rows for one turn is logged, as 6/23 already states. Touches
`routd/reads_http.go` (handleCost), `routd/api/v1/types.go`,
`template/services/litellm.yml` (callback file), and the three harness
renderers under `ant/src/` (one header each). Until then the operator note
stands: the gateway is a credential boundary, not an accounting one.

- **Severity:** medium (the cap enforces a number the metered party supplies)
- **Scope:** model gateway → routd accounting
- **Affected:** any instance with `MODEL_GATEWAY_URL` set (none today)
- **Source:** `template/services/litellm.yml`; `routd/reads_http.go:215`; `routd/dispatch.go:261`; litellm probe 2026-09-05 12:30
- **Status:** fixed — the transport is neither of the two candidates. The
  package ships litellm's Postgres, so routd mints a key per turn, and the
  carrier is the run it already brackets: `recordGatewayCost` reads
  `/spend/logs` for that key at the turn boundary and `putTurnCost` writes the
  row. No callback file, no header the metered party sets, no second
  accounting substrate — attribution is the key itself, which routd minted.
- **Fix:** 2f6441ed, c8854f7d

## ✅ FIXED 2026-09-20 L175 — ant delivers a model API failure as a successful reply and books no usage for it (2026-09-05)

Measured with claude-cli 2.1.261 (host and the `arizuko-ant` image alike):
an HTTP 400 from the model endpoint yields a result of `subtype:"success",
is_error:true, result:"API Error: 400 …", modelUsage:{}`.
`claudeResultStatus` (`ant/src/claude.ts:329-335`) reads only the subtype and
the "Not logged in" text, so ant submits `status: success` with the error
line as the result and no `models`; routd's `recordTurnResult` then delivers
"API Error: 400 fake says no" to the chat as the agent's answer, writes no
cost row, and the run counts as clean for the breaker. The same shape
follows an exhausted 529 retry. `tests/e2e_turn_test.go` step 3 exhibits it
(the assertion documents the behaviour, it does not endorse it).

**Measured again live, 2026-09-20 on sloth, and the severity is wrong.** The
account behind sloth's `CLAUDE_CODE_OAUTH_TOKEN` hit its seven-day limit with
the org spend cap reached, so every turn came back
`apiErrorStatus: 429, quotaLimits.status: "rejected",
overageDisabledReason: "org_spend_cap_reached"`. The container exited 0, ant
submitted the rejection text as the turn result, and routd delivered
"You've hit your individual spend limit …" to the Discord chat as the agent's
answer. `chats.errored` stayed clear, no cost row was written, the breaker
stayed closed, and every eval check reads green. A fleet-wide outage is
invisible to the operator surfaces — that is the real cost, not the one line
the user reads.

The status field ant submits is write-only today: `store.RecordTurnResult`
persists it and NOTHING reads it back (`grep` over `routd/`, `store/` finds
one writer, zero readers). `routd`'s failure path keys on runed's
`out.Outcome`, which comes from the container exit code. So fixing
`claudeResultStatus` alone changes nothing — the second half is required.

Retry shape matters here: a seven-day quota rejection is NOT transient
(`~/.claude/CLAUDE.md`, retry only transient errors). Its reset was 11 hours
out, so routd's `maxTurnRetry` path must not re-spawn it — this is the
`out.Terminal` shape, which names the cause and never retries.

- **Severity:** high (a fleet-wide model outage reads green on every operator surface)
- **Scope:** ant claude harness → routd turn result
- **Affected:** every claude-backend instance
- **Source:** `ant/src/claude.ts:329-335`, `:388-397`; `routd/turns.go:679`; `store/turn_results.go:91`
- **Status:** fixed 2026-09-20.
- **Fix:** the proposal's second half is NOT needed, and looking for it was the
  mistake. routd already has the failure path this turn belongs on —
  `dispatch.go:509`, keyed on `out.Outcome == OutcomeError`, which retries a
  non-terminal failure, marks `chats.errored`, marks the trigger's messages
  errored, delivers a failure notice (naming the cause when `out.Terminal`),
  closes the turn and feeds the breaker. Nothing was missing there. What was
  missing is that a model API failure never REACHED it, because the container
  exited 0. So the fix moves the failure onto the existing serialization point
  rather than building a second one, and **routd is unchanged**.

  Three edits, all in the container's own half:

  1. `claudeResultStatus` takes the SDK's `is_error` and lets it win over the
     subtype (`ant/src/claude.ts`). That is the verdict the SDK already
     publishes and the one the old reading ignored.
  2. A failed turn's text rides `error`, never `result` (`ant/src/index.ts`).
     routd delivers a non-empty `result` to the chat as the agent's reply, so
     this is what stops "API Error: 400 …" being the answer. The shape matches
     the one the no-result-frame case already used.
  3. The container exits non-zero on a failed turn: `1` for a retryable one,
     `78` for a terminal one. `78` is sysexits.h `EX_CONFIG`, and
     `container.terminalExit` accepts it beside Docker's 125/126/127 — whose
     comment already reads "the exit code proves a configuration fault", which
     an exhausted quota is.

  Terminal is decided from a signal the harness already emits, not from
  matching the error text: `ClaudeTurn` records a `rate_limit_event` whose
  `rate_limit_info.status` is `rejected`, and a result that errors after one is
  terminal. An informational rate-limit event leaves the failure retryable.

  PROVEN BY MUTATION, four ways:
  - drop the `is_error` check → 3 ant tests fail;
  - put a failed turn's text back into `result` → 1 fails;
  - never set `terminal` → 1 fails;
  - drop `78` from `terminalExit` → `TestRun_ExitCodes/exit_code_78` fails.

  ant: 227 pass, 0 fail, `tsc --noEmit` clean.

  **Reaches live agents only after an `arizuko-ant` image rebuild** — the
  change is in the container, so a restart alone does not carry it.

## ✅ FIXED 2026-09-18 L176 — `GET /v1/messages/volume` compared RFC3339 to `datetime('now')` (2026-09-05)

Fixed: `handleMessageVolume` calls `db.DailyVolume(ctx, volumeDays)`. There is
no SQL date function in the path, and the window is counted in Go.

- **Status:** fixed in v0.68.0
## L164 — anteval's cost column is empty for 6 of 9 passing cases and `fresh-context` stalls 3 min on a turn that never ran (2026-09-05, open)

Smoke on krons 2026-09-05 07:46-07:55, 9/9 pass. routd recorded 24 `cost_log`
rows — opus + haiku for every one of the 12 turns the run dispatched — so the
platform side is complete. The harness still printed `tokens=0` for six cases
(e.g. `chat-entrypoint`, whose turn `in-d0097d8645d31f85` has rows at 07:46:50)
and, for `fresh-context`, waited on `in-1b05efa7fa40f3d3` for 3m0s ("never
recorded a cost … the next case may start behind it"): that case sends `/new`
and its prompt back to back (07:47:33.818 / .854) and routd folded both into
one turn, `cmd-new-1788594453854141148`, whose row landed at 07:47:53. The id
the harness waited on never ran. The stall is ~30 % of a 9.5-minute run and the
per-case token figure cannot be trusted.

- **Severity:** low (harness only: +3 min per smoke run; token column under-reports)
- **Scope:** anteval cost read / turn-id derivation
- **Affected:** anteval
- **Source:** anteval report 2026-09-05 07:55; `routd.db` `cost_log` 07:45-07:56
- **Confirmed still live 2026-09-18:** ALL NINE cases reported `0 tokens` and
  every one carried "turn in-<id> never recorded a cost within 3m0s". It is no
  longer 6 of 9 — the cost column is empty for the whole run, so the report's
  header reads "8/9 passed (0 tokens)".
- **Status:** open. Fix shape: read the turn id routd actually ran for the injected message instead of deriving `in-<msgid>`, and read cost after the turn's `round_done`, not on a fixed wait.
- **Fix:**

## ✅ FIXED 2026-09-05 L156 — runed's `warnChown` reports docker's root-owned `auth.json` mount stub in every group: 50 false EPERM warnings per restart (2026-09-05)

`runed` start runs `SeedCodexDirs` → `warnChown(<group>/.codex)`
(`container/runtime.go:26-39`, `runner.go:1110-1117`). Each group's
`.codex/auth.json` on the host is a 0-byte root-owned file: docker materialized
it as the target of the RO bind `HostCodexDir/auth.json → ~/.codex/auth.json`
(`runner.go:616`) on the group's first spawn, inside the bind-mounted home.
runed is uid 1000, so `lchown` on it is EPERM, and the WARN fires once per
group per restart — krons 50 per restart (548 in the 24 h before the
2026-09-04 deploy across 11 restarts; 50 at 14:28:18-44), marinade 6, sloth 4.
The agent never writes that path (it is RO), so every line is a false alarm.
The warning exists to catch a REAL root-owned path (its comment cites the
`~/public_html` that survived unnoticed from May to August); a real one would
now be the 51st identical line. Present since at least 2026-08-27 19:21 (first
in the 30-day journal).

- **Severity:** low (noise that defeats the warning's own purpose)
- **Scope:** runed startup / codex pre-seed
- **Affected:** krons, marinade, sloth
- **Source:** `container/runtime.go:26-39`, `container/runner.go:616`, `:1110-1117`; journal krons 2026-09-04T14:28:18
- **Verified:** `auth.json` AND `config.toml` are both 0-byte `root:root` under
  every seeded group on krons (`adshaus`, `eval`, …), in a `.codex/` otherwise
  owned by uid 1000 — so the daemon, not the agent, made them.
- **Status:** fixed. `container.SeedCodexMountTargets` creates the RO overmount
  targets as the running uid before the docker daemon has to, and replaces a
  zero-length target it does not own (runed owns the directory, so it may
  unlink what it may not chown). Called from `SeedCodexDirs` and from
  `buildMounts`, which now builds the RO mount list from the same
  `CodexROMounts` declaration.
- **Fix:** `fix(container): own the codex RO mount targets before docker does`

## ✅ FIXED 2026-09-20 L157 — `SeedCodexDirs` derives "group" from a `.claude/` marker on disk, so a root-owned project dir is seeded and fails at every restart (2026-09-05, open)

`SeedCodexDirs` walks `groups/` and treats any directory holding `.claude/` as
a group (`runtime.go:19-24`: "runed has no groups table to enumerate, so it
walks the tree"). On sloth `groups/main/trading/candles/downloader/` is a
root-owned project directory with its own `.claude/`; it is not a group
(`groups` holds only `main/trading`), so every restart logs `seed codex dir …
mkdir …/.codex: permission denied` (sloth 10, marinade 18 in the 24 h before the
deploy; 1-2 per restart). Identity is derived from a filesystem marker — the
class root `CLAUDE.md` forbids — and the walk descends every project tree under
every group home. Harmless today because the mkdir fails; a writable project
dir with a `.claude/` marker gets a platform-seeded `.codex/`.

- **Severity:** low
- **Scope:** runed startup
- **Affected:** sloth, marinade
- **Source:** `container/runtime.go:14-39`; journal sloth 2026-09-04T14:30:23
- **Verified 2026-09-05:** `groups/main/trading/candles/downloader/` on sloth is
  `drwxr-xr-x root root` and holds a root-owned `.claude/`; `routd.db.groups`
  holds `coach, main, main/code, main/content, main/main, main/reading,
  main/trading` and nothing below `main/trading`. The walk seeds a directory
  that is not a group, and cannot.
- **Verified, and it sharpens the proposal:** the race the walk exists to close
  has no window in runed. `buildMounts` calls `os.MkdirAll(groupCodex)`
  synchronously, in-process, before runed execs `docker run`
  (`container/runner.go:611`), and a group's spawns are serialised by its run
  slot. Docker never sees a missing bind source that runed is about to create.
  The comment's cited precedent is `gateway.seedCodexDirs` — a topology that no
  longer exists.
- **Status:** open — proposal, needs sign-off. Preferred shape: DELETE the walk;
  `buildMounts` already creates the directory and (since `L156`) its mount
  targets, per group, at the moment the group actually spawns. That removes the
  identity-derived-from-a-`.claude`-marker violation instead of narrowing it.
  Alternative if the pre-seed must stay: read the group list from routd's
  `/v1/groups`. Not shipped: deleting a startup mechanism is a control-flow
  change.
- **Fix:**

- **Status:** fixed 2026-09-20.
- **Fix:** `SeedCodexDirs` is deleted, with its one call site in
  `runed/cmd/runed/main.go`. Nothing replaces it, because the spawn path
  already does the work: `buildMounts` (`container/runner.go`) calls
  `os.MkdirAll(groupCodex)` and `SeedCodexMountTargets` in-process, before
  runed execs `docker run`, and a group's spawns are serialised by its run
  slot. There was no window for docker to materialise the bind source.

  The deletion also removes the class violation. The walk decided what a group
  IS from a `.claude/` directory on disk, which is identity derived from a
  filesystem marker — the thing root `CLAUDE.md` opens by forbidding. runed
  now never needs to answer that question, so there is nothing left to derive.

  The four tests that exercised the walk are gone. The one that matters now
  asserts the property the deletion depends on:
  `TestBuildMountsOwnsTheGroupCodexDirBeforeDockerSeesIt` checks `.codex/` does
  NOT exist first, so it cannot pass vacuously, then that `buildMounts` creates
  it and seeds both overmount targets. The `SeedCodexMountTargets` tests stay
  and now drive that function directly.

  PROVEN BY MUTATION: remove the `os.MkdirAll(groupCodex)` from `buildMounts`
  and the new test fails — so if the spawn path ever stops owning the
  directory, deleting the walk stops being safe and the suite says so.

## ✅ FIXED 2026-09-05 L158 — a Discord mention of the assistant is parsed as a child-group `@prefix` (2026-09-05)

discd rewrites the bot's own mention `<@botid>` to `@<assistant name>`
(`discd/bot.go:763-768`). routd's prefix layer then reads `@sloth …` as a
delegation to the child group `<folder>/sloth`, finds none, warns `@prefix:
child group not found child=main/trading/sloth` and falls through
(`routd/steer.go:449-458`). One message (`@sloth only on mentions`, sloth
2026-09-04 14:30:05) produced 7 warnings, one per poll until the restart let it
through; the turn then answered normally. Harmless today. Latent: a child group
named after the assistant (`main/sloth`) would turn every plain mention into a
silent delegation.

- **Severity:** low
- **Scope:** discd inbound → routd steer
- **Affected:** sloth (any Discord group)
- **Source:** `discd/bot.go:763-768`, `routd/steer.go:449-458`
- **Status:** fixed at the producer. `replaceMentions` drops the self-mention
  (`dropMention` takes one adjacent space with it, so no doubled or leading
  space is left) and keeps rewriting other users' mentions. A message that is
  nothing but the mention falls back to the bare assistant name: routd 400s on
  empty content (`routd/server.go:501`), so a bare ping must keep text.
- **Fix:** `fix(discd): drop the bot's own mention instead of rendering it @name`

## L159 — a scheduled task fires nightly into a Telegram chat teled already classified as permanently dead (2026-09-05, open)

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
- **Status:** open — proposal, needs sign-off: on a permanent send or typing error (chat not found, chat deleted, bot kicked) mark the chat dead in `chats`, pause its scheduled tasks, and show it on the dashboard; the operator deletes or re-targets. No retry, no silent skip. This is a new lifecycle spanning teled, routd's chat/task tables and the dashboard, so it is not shipped inline.
- **Fix:**

## ✅ FIXED 2026-09-05 L160 — routd's poll loop resolves a chat after `db.Close()`: a "message dropped" ERROR at shutdown (2026-09-05)

sloth 2026-09-04 13:41:20.999 `routd stopping`; 22 ms later `resolve: route
table read failed; message dropped as route-miss
jid=discord:…/1327767461285855437 err="sql: database is closed"`.
`routd/cmd/routd/main.go` runs `go loop.Run(ctx)` (line 280), on SIGTERM calls
`cancel()` (284) and returns without waiting for the loop; the deferred
`db.Close()` (line 52) runs while the poll goroutine is mid-`resolve`, so
`Routes()` fails and `loop.go:694` fails forward — cursor advanced, message
dropped. This time nothing real was lost (that chat's newest row is from
2026-05-15), but the drop is possible by construction at every restart. 1
occurrence in 7 days.

- **Severity:** low today (an inbound message in the poll at shutdown would be lost)
- **Scope:** routd shutdown ordering
- **Affected:** all instances
- **Source:** `routd/cmd/routd/main.go:52,280-287`; `routd/loop.go:682-696`
- **Status:** fixed. `Loop.Run` closes an internal `done`; `Loop.Wait(timeout)`
  blocks on it and main calls it after `httpd.Shutdown`, before the deferred
  `db.Close()`. A timeout exits WITHOUT closing — an unclosed SQLite WAL is
  recovered on the next open, a dropped inbound message is not.
- **Fix:** `fix(routd): wait for the poll loop before closing the DB`

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

## ✅ FIXED 2026-09-05 L162 — slakd logs `slack inbound quiet; token healthy, not restarting` every minute, forever (2026-09-05)

marinade: 1019 lines in the 17 h after the deploy, 1390 in the 24 h before;
`stale_seconds` reached 61500 (17 h). The watchdog (`slakd/bot.go:291-313`)
re-warns on every tick once `age > staleSec` while `auth.test` passes. A quiet
workspace is a state, not an event; this is the cadence defect `L61` fixed in
onbod — an unactionable condition at WARN once a minute trains the reader to
filter the adapter that would carry a real one.

- **Severity:** low (noise)
- **Scope:** slakd watchdog
- **Affected:** marinade
- **Source:** `slakd/bot.go:291-313`
- **Status:** fixed. The watchdog carries a `quiet` state: it warns on the edge
  into quiet and logs `slack inbound resumed` on the edge out. An auth.test
  failure clears the state, so a recovery back to quiet says so once.
  `stale_seconds` still rides `/health` every tick.
- **Fix:** `fix(slakd): warn on the edge into a quiet stream, not every tick`

## L163 — an adapter restart empties its in-memory `/files/<id>` cache, so attachments re-fed across the restart 404 and the agent gets the text only (2026-09-05, open)

sloth 2026-09-04 13:43:13-19, right after the 13:41 restart: 12 × `enrich:
download failed url=http://discd:8080/files/<id> err="HTTP 404"`.
`chanlib.URLCache` (`chanlib/urlcache.go`) is an in-memory LRU mapping the
12-hex id to the platform URL; discd's restart drops it, routd's enrich WARNs
and `continue`s (`routd/enrich.go:110`), so the turn runs on a message that
names an attachment it never receives. Silent to the user.

- **Severity:** low-medium (silent attachment loss around every adapter restart)
- **Scope:** chanlib file proxy / routd enrich
- **Affected:** every adapter on `FileProxyHandler` (discd measured)
- **Source:** `chanlib/urlcache.go`, `chanlib/handler.go:678-690`, `routd/enrich.go:96-112`
- **Verified 2026-09-05, and it is wider than a restart.** `URLCache` is a
  bounded LRU (`DefaultURLCacheSize = 4096`), so an id is also lost by
  EVICTION, not only by restart — a busy adapter drops the oldest mapping while
  running. The id is `sha256(url)[:12]`, derived from the URL it stands for,
  which is what makes the third option below possible.
- **Status:** open — proposal, needs sign-off. Three shapes, cheapest last:
  (1) persist the id→url map in the adapter's own SQLite (the emaid precedent);
  (2) enrich delivers a visible placeholder so the loss is not silent
  (`routd/enrich.go:110` currently WARNs and `continue`s — the agent is told
  nothing); (3) make the id a signed capability token that CARRIES the URL, so
  the proxy holds no state and neither a restart nor an eviction can lose one.
  (3) is the only one that makes the bad state impossible by construction, but
  it changes the `/files/<id>` contract for every adapter on `FileProxyHandler`.
- **Fix:**

## L153 — anteval `webhook-in`: the ingress worked and the agent answered the PREVIOUS case; a skill that hardcodes a nonce outlives the case that built it (2026-09-03, eval finding)

`anteval --smoke` on krons after the 2026-09-03 deploy: **7/9**. Both failures
are agent reasoning, not platform — every surface under them worked. Recorded so
the next run can tell a regression from these two.

**`webhook-in`.** The platform half is flawless: proxyd logged
`POST /hook/<token> 204`, the payload became a `hook:eval/etksvtw/webhook-in`
message, and a turn fired on it. Then the agent replied *"Callback fired.
`HTTP 204` — **self-skill** proof registered"* — the answer to the PREVIOUS case.

The cause is the case before it. `self-skill` asks the agent to write a skill
under `~/.claude/skills/` whose body is `curl -s {sink}/cb/{nonce}` — with that
run's `self-skill` nonce baked in. The skill persists in the group's home. So
when the webhook turn said "run the callback", the agent invoked the skill it
already had, which curled the wrong nonce. anteval saw no callback for
`webhook-in` and failed it at its 150s cap.

Two things this means. The suite is ORDER-COUPLED where it claims cases are
independent: `self-skill` leaves a booby-trapped tool in the group that answers
a later case's instruction plausibly and wrongly. And it is a real agent lesson
— a stored skill that hardcodes a one-shot token is indistinguishable from a
correct tool until the token is stale.

**`priv-grant`** timed out at its 180s cap with
`GET probe: unsupported protocol scheme ""` — the checker's fallback when
`{cb.url}` never arrived. The child group `eval/etksvtw-priv-grant-g` WAS
created, so the agent got through `register_group` + `add_acl`; the child's
publish, or the parent's POST-back of the URL, did not finish in the wall. The
near-identical `priv-401` (publish + POST the URL) passed in 42s, so the
delegation half is what costs. Not diagnosed further.

The three grants anteval's hint names (`mcp:register_group`, `mcp:add_acl`,
`mcp:issue_webhook`) are all held by `folder:eval` — checked, so neither failure
is the missing-grant case that hint warns about.

**Shape of a fix (proposal — needs sign-off).** For the order-coupling: give
`self-skill` a teardown, or have the harness reset `~/.claude/skills/` between
cases the way it already resets the root session. The cheaper half is to make
the case's prompt build a skill that takes the nonce as an ARGUMENT rather than
baking it in, which also removes the stale-token lesson from the group. Either
is a change to the catalog's independence contract, so it waits for sign-off.

## ✅ FIXED 2026-09-03 L154 — `6/28` states its own read-path rule two incompatible ways, which is why `L142` reads as a violation and the per-daemon dashboards read as compliant (2026-09-03, phase-6 status audit)

**FIXED 2026-09-03 — `:223` wins, `:37` is rewritten to match.** The rule is
"no dashboard reads ANOTHER daemon's SQLite file". A daemon's own dashboard
reading its own DB is the topology: it owns the file, migrates it, and is its
only writer, so routing its own page through its own `/v1` would be a second
reader of one table for no gain.

Two consequences, both now unambiguous. `6/30`'s per-daemon dashboards are
COMPLIANT and were never defected. And `L142` keeps its whole force: dashd
reading `routd.db` is exactly the case the surviving sentence forbids, so `6/29`
stays defected until those reads go through routd's `/v1`.

Auditing every phase-6 spec's `status:` against the code. `6/28` is the
architecture `6/29`–`6/32` all inherit "and do not restate", and it says the rule
twice, differently:

- `:37` — "**No dashboard — hub or per-daemon — reads a SQLite DB directly.**"
- `:223` — "no dashboard reads **another daemon's** SQLite file."

Those are not the same rule. Under `:37` a daemon dashboard reading its OWN db is
a violation; under `:223` it is the intended design. `L142` quotes `:37` and is
correct against it; the per-daemon dashboards `6/30` ships are correct against
`:223`. Nothing can be both, so "is `6/30` defected?" has no answer today — which
is the actual reason this audit could not settle three specs' status.

Measured while checking: `chanlib/dash.go` — the ONE renderer behind all nine
adapter dashboards — executes no SQL at all (no `sql.`, `db.`, `Query`, `store.`).
So `6/31` and `6/32` satisfy BOTH readings and their `shipped` is sound either
way. The ambiguity only bites `6/29` (dashd, where `L142` measured the direct
reads) and `6/30`.

**Shape of a fix (proposal — needs sign-off).** Pick one sentence and delete the
other. `:223` is the one the code was built to and the one the split
write-discipline already implies — a daemon owning its DB and serving its own
page from it is the topology, not a leak. If `:37` is the intended rule instead,
then `6/30` is defected too and dashd's rewrite is larger than `L142` scopes. The
choice decides two specs' status, so it is the operator's.

## L155 — the skill-guard quarantines 74 of arizuko's own 544 shipped skill files, `migrate/SKILL.md` first; `agent_config_mod` fires `critical` on the STRING "CLAUDE.md" (2026-09-03, live on krons)

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

## L152 — an `acl` grant can name a principal nobody will ever present, and no surface says so (2026-09-02)

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
before they arrive). One query — principals in `acl`/`acl_membership` that are
JID-shaped and appear in no `messages.sender` and no linked identity — surfaced
on the dashboard grants page and in the CLI grant listing as a "never seen"
marker beside the row. No new table, no new write path, no evaluator change.

**How this entry was first written wrong.** The first version claimed krons'
Telegram operator had been unreachable since 2026-07-19. That came from a
`SELECT … LIMIT 30` on a 54-row table: the correct grant is row 31. Read the
whole table before concluding a row is absent — see `truncated_evidence`.

## L151 — proposal: an adapter whose credential is dead restart-loops forever and the only trace is one log line; routd already owns the mechanism to say so (2026-09-01, proposal — needs sign-off)

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
Both still log-only.

## L149 — only the LAST assistant text block is delivered, so an answer written before a tool call is destroyed; `ant/CLAUDE.md:85` tells the agent the opposite (2026-08-31, instruction half FIXED 2026-08-31; the mechanism half is a proposal)

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

## L150 — the agent never sees what it delivered, so it cannot detect a truncated reply and confabulates a cause instead (2026-08-31, open — proposal, needs sign-off)

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

- **Files:** `routd/prompt_db.go:192,197,208`, `ant/src/index.ts:89-107`
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

## L148 — 22 ad-hoc JID⇄folder conversions in 16 files; collapse them to three named seams so `"web:"` appears twice in the tree (2026-08-30, open — minimal, no schema change)

The operator's requirement: *"remove special treatment of JIDs — that should all
only be specced in routing, or not there at all."* This is the minimal way to
match that to the current design. It changes no schema, no queue, no routing, and
no behaviour; it removes duplication.

**Measured.** `"web:"`/`"hook:"` prefix construction or parsing appears at 22
sites in 16 files: `webd/me.go` ×3, `webd/mcp.go` ×2, `store/route_tokens.go` ×2,
`store/messages.go` ×2, `routd/server.go` ×2, `routd/dispatch.go` ×2,
`cmd/arizuko/token.go` ×2, and one each in `webd/ratelimit.go`,
`webd/channel.go`, `webd/api.go`, `store/routes.go`, `routd/route_tokens_resource.go`,
`ipc/ipc.go`, `groupfolder/folder.go`, `dashd/authz.go`, `cmd/arizuko/send.go`.

**Only `store/routes.go` is legitimately routing.** Every other site is doing one
of two things, ad hoc:

- **folder → JID**: `"web:" + folder` (`store/messages.go:285,314`, …)
- **JID → folder**: `strings.CutPrefix(jid, "web:")` (`ipc/ipc.go:849`,
  `dashd/authz.go:123`, `groupfolder/folder.go:111`, …)

**The three seams every site must use instead:**

1. **Constructing a web JID → `groupfolder.WebJID(folder)`** (net-new, ~3 lines).
   The only place the literal `"web:"` is written.
2. **Holding a token row → `row.Folder()`**, which READS `target_folder`
   (`store/route_tokens.go`). Never parse a suffixed `web:t/sfx` or a `hook:` JID
   — `target_folder` exists precisely because that string cannot be parsed back,
   and `L139`'s implementation caught a live instance of exactly that bug.
3. **Holding a bare unsuffixed web JID and no row → `groupfolder.JidFolder(jid)`**,
   which already exists and is already documented as prefix-grammar-only.

After this, `"web:"` appears twice in the tree — once in `WebJID`, once in
`JidFolder` — and every other file deals in folders. That is as close to "JIDs
only in routing" as the current schema allows, because the remaining JID is not a
routing address at all: it is the queue's serialization key (`Enqueue(chatJID)`,
`busyWait sync.Map // chatJID → busyState`, `publishRoundDone(chatJID, turnID)`).

**Why this is the right minimal step, not a half-measure.** Removing `web:`
entirely means re-keying `messages.chat_jid` and the queue onto `(folder, topic)`
— a schema change touching every consumer. After this consolidation that becomes
a change at THREE seams instead of 22, and the bug class it causes
(`identity_derived_not_read`: three traced failures, including the SSE
`round_done` key bug which landed twice) is fenced meanwhile.

**Ordering note.** `groupfolder.JidFolder` is itself named in
`identity_derived_not_read` as a smell — parsing rather than reading. It is SOUND
for unsuffixed web JIDs (1:1 with the folder, `ROUTING.md:29-38`) and UNSOUND for
suffixed and `hook:` ones. Seam 2 exists to take those; seam 3 must be documented
as the unsuffixed-web case only.

- **Severity:** low (no runtime defect today; it is the substrate of a class that
  has produced three)
- **Scope:** JID⇄folder conversion, repo-wide
- **Affected:** the 16 files above
- **Status:** open — minimal, mechanical, no schema change
- **Fix:** add `groupfolder.WebJID`, route all 22 sites through the three seams,
  add a test that fails if `"web:"` appears as a literal outside `groupfolder/`
- **Related:** `L147` (the principle), `L146` (deferred), `identity_derived_not_read`

## L147 — principle: the channel attests, the router resolves, the ACL authorizes — and `web:` JIDs exist only to satisfy a storage column (2026-08-30, principle — records a design fault, no work item)

The operator's formulation, at the end of the session that produced `L139`,
`L145` and `L146`: *"web should not have JIDs, that's the fault in the design"*,
*"web should have direct links with an auth attached"*, *"who authorizes and the
routing are two things separate"*, and finally *"channel created a trusted
authority, the auth authorizes later"*.

**Three roles, three owners, none deriving another:**

| role | owner | why it must be there |
| --- | --- | --- |
| **attest** | the channel adapter | it is the ONLY component holding the platform secret — slakd verifies `X-Slack-Signature` (`slakd/server.go:103-105`), teled its token. Nothing downstream can re-check it. |
| **resolve** | routd | correspondent → destination, computed per message from the route table, target expansion, modes, autoviv, the `@name`/`#topic` layer and sticky state (`routd/db.go:546`). A resolver, not a map. |
| **authorize** | the `acl` evaluator | may this authority reach this destination, with these arguments. The only place a grant is read. |

The adapter authenticates and does not authorize. The acl authorizes and cannot
authenticate. They are separated by a hop, and that separation is the design —
not an artefact of it.

**There are TWO attesters, and web is not a special case.** proxyd is the owned
surfaces' attester: `proxyd.tryAuth` (`proxyd/main.go:810`) verifies our own ES256
bearer against authd's JWKs and stamps `X-User-*`, which is structurally the same
job slakd does with `X-Slack-Signature`. Ours, theirs — same role, different
secret holder.

| path | attester | holds | attests |
| --- | --- | --- | --- |
| foreign | the channel adapter | the platform's signing secret | a correspondent |
| owned | proxyd | authd's JWKs / our OAuth | a person |

**And that exposes the real asymmetry: both paths attest, but only the owned
path's attestation SURVIVES to the authorization.** Web carries `X-User-Sub`
through to `auth.MatchGroups`. The foreign path attests a correspondent and then
discards it — `db.Authorize("folder:"+folder, folder, …)` (`ipc/ipc.go:1092,1118`)
— so the folder becomes the principal and the attested sender survives only as a
payload field, converted to a sub once for the budget gate
(`routd/dispatch.go:261`) and never for authz.

So the foreign path **attests and discards**; the owned path **attests and
carries**. It is not a missing mechanism, it is a dropped one — which is why
`L146` measured the gap as one call frame wide.

**Internally it is the credential arizuko already has, minted at the edge.** No
new credential type is implied. Adapters ALREADY authenticate to authd and hold a
bearer (`chanlib.SetServiceToken`, spec `5/1`); it is exchanged as the DAEMON
principal — `AUTHD_SERVICE_NAME`, e.g. `teled` — so today it says "I am the
adapter". The delta is one claim: "I am slakd, ACTING FOR
`telegram:user/1112184352`". Same mint, same JWKs, same `X-User-*` stamp
downstream.

That is the whole edge conversion, and both edges already run it:

    platform signature → authd credential      (adapter, foreign)
    OAuth bearer       → authd credential      (proxyd, owned)

Two edges, one internal credential, nothing new behind either. Which is why this
is a claims change on a credential path that already exists, not a new auth
system.

**The fault this names.** Today routing address and authority are FUSED in the
JID: it is both where a message goes and, through the token pin, what may be
reached. That fusion is why `route_tokens` stores a folder three times (inside
`jid`, again in `target_folder`, again in `owner_folder`), and why `L145` and
`L146` kept circling one knot.

**`web:` is the clearest instance, and it carries no information at all.**
`web:<folder>` is 1:1 with the group, bypasses the route table entirely, and
`store.AddRoute` REJECTS a `chat_jid=web:*` predicate at insert time
(`ROUTING.md:29-38`). It exists because `messages.chat_jid` is the storage key,
so every message needs a JID and web got a synthetic one. **The prefix is
ceremony to satisfy a column.**

**It is the direct cause of a measured bug class — 16 sites in 8 files build or
parse `"web:"+folder`** (`webd/me.go` ×3, `webd/mcp.go` ×2, `store/messages.go`
×2, `cmd/arizuko/token.go` ×2, `webd/channel.go`, `webd/api.go`,
`routd/server.go`, `routd/route_tokens_resource.go`). Three failures traced to it:

- `target_folder` had to be ADDED to the row because `web:t/sfx` cannot be parsed
  back into a folder;
- `L139`'s implementation caught `send` deriving `jid := "web:" + g.Folder`, which
  would have written to and read the WRONG chat for any suffixed or `hook:` token;
- the SSE `round_done` key bug, which landed twice — keyed on the chat JID where
  the subscriber keyed on the target folder.

All one shape: a folder encoded into a string and parsed back out
(`identity_derived_not_read`).

**What follows, if this is ever acted on:** for an owned surface there is no
correspondent to resolve — only a folder to address — so the URI names the
destination, the header carries the authority, and `web:` has nothing left to do.
For a foreign surface the adapter attests, routd resolves, and the acl authorizes,
each reading its own input.

**No work item.** `L146` is DEFERRED — the JID router stands. This entry exists so
the fault is named rather than rediscovered, and so a future change to
`route_tokens`, `messages.chat_jid` or the adapter boundary starts from the
principle instead of the symptom.

**Update 2026-08-30 — the "attests and DISCARDS" half is closed.** The turn now
authorizes on two axes: `auth.Caller.Route` carries the destination folder and
`auth.Caller.Principal` carries the attested sender, and `auth.Authorize` unions
them with the sender's half confined to the route's subtree and to an exact
action (spec `5/33` §"Two axes"). The foreign path still attests a JID rather
than an account — the sender string is matched as it arrives, and mapping a
correspondent to a paired account is `L146`'s inbound rewrite, which stays
deferred. So the fault this entry names is now the STORAGE-KEY half (`web:` as
ceremony for a column, `L148`'s 22 conversion sites), not the authorization half.

- **Severity:** n/a (recorded principle + a named design fault)
- **Scope:** routing / auth model / storage key
- **Affected:** `store/messages.go`, `store/route_tokens.go`, `webd/*`, `ROUTING.md`, `specs/5/W`, `specs/5/34`
- **Status:** recorded — no work item; the auth-axis half shipped 2026-08-30,
  `L146`'s inbound rewrite stays deferred, `L145` open as a proposal
- **Related:** `L145`, `L146`, `L148`, `L122`, `identity_derived_not_read`

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

## ✅ FIXED 2026-09-05 L144 — `6/30`'s crackbox section prescribes reviving the shared-daemon shape `6/24` retired; two shipped specs contradict each other (2026-08-30)

`6/30`'s table row for crackbox is honest — `Built? no`, citing `6/24` as the
reason. But its "Required `/v1` work" column and its "crackbox — orthogonality
forces a different gate, and a different port" prose then prescribe exactly the
architecture `6/24` killed: a persistent admin listener on `:3129`, bearer-
forwarded auth, `Route.InjectAuthorization`, and an `EGRESS_CRACKBOX`-gated
compose fragment.

`6/24` (also `status: shipped`) retired crackbox's daemon mode, admin API,
registry and long-lived container. The code agrees: `crackbox/` is 414 lines in
two files and `cmd/crackbox/main.go:9-10`'s own banner reads "no daemon mode, no
admin API and no registry: one proxy per sandbox, allowlist fixed at start".
Repo-wide there is no `InjectAuthorization`, no `template/services/crackbox.yaml`;
`:3129` and `CRACKBOX_ADMIN_SECRET` survive only in changelog and helm artifacts.

The shape cannot return as described — there is no long-lived container to attach
a fixed admin port to. One shipped spec's future-work section describes an
architecture another shipped spec explicitly deleted, so a reader following `6/30`
would rebuild the leak `6/24` removed.

- **Severity:** low (nothing built; the risk is a future reader acting on it)
- **Scope:** specs
- **Affected:** `specs/6/30`, `specs/6/24`
- **Status:** fixed 2026-09-05 by `942474b7`, "docs(specs): specs state what is,
  not what was declined or never built". Closed on re-check 2026-09-20.
- **Fix:** the crackbox section is gone from `6/30`. VERIFIED: `grep -i crackbox
  specs/6/30-per-daemon-dashboards.md` returns nothing, and `3129`,
  `InjectAuthorization` and `CRACKBOX_ADMIN_SECRET` appear in no spec at all.
  `6/24` stands alone as the governing text.

## ✅ FIXED 2026-09-20 L143 — `6/30`'s runed section describes a build state two releases stale, and its acceptance criterion cites an endpoint that was never mounted (2026-08-30)

**Stale build state.** `6/30`'s top table says runed is "overview only" with the
runs list "unbuilt", and its Migration-state table says `/dash/runed/` runs+kill
is "still blocked" on runed's mux having no run list route. The code ships both:
`runed/dash.go` implements a run list with `?state=` filtering and inline kill,
wired at `runed/server.go:61-62`, covered by
`TestDashboardAndItsControlAreServedByRuned` and `TestDashKillStopsTheRun`. A run
DETAIL page genuinely is not built, so that half holds.

**Unmet acceptance.** The criterion "a folder-scoped token calling `GET /v1/runs`
sees only its subtree" cites an endpoint that does not exist: `runed/server.go:51`
mounts `POST /v1/runs`, never a GET list, and `/v1/capacity` and
`Manager.Snapshot()` do not exist either. The containment DISCIPLINE is real and
tested — `runed/authz_containment_test.go` holds `TestRunControlFolderBound`,
`TestRunControlRootUnrestricted` and `TestRunSpawnFolderBound` — so the guarantee
is sound; the acceptance line names the wrong surface for it.

(An audit first reported that test file as fictional. It exists; verified before
filing. The endpoint half is what is real.)

**Dead FS mount.** `dashd/main.go:277-279,310` still opens and holds a `runed.db`
handle, with a comment claiming "the `/dash/runed/` runs view reads them here
directly". That view moved to runed — `dashd/main.go:537` says so itself — so
dashd keeps an FS mount on another daemon's DB for no live purpose. Not a
security hole (proxyd routes `/dash/runed/` to runed ahead of dashd's catch-all),
but it is the coupling `L142` is about, kept alive by a stale comment.

- **Severity:** low (documentation + one dead handle)
- **Scope:** specs / dashd
- **Affected:** `specs/6/30`, `dashd/main.go`
- **Status:** open
- **Fix:** mark runed's list built and detail unbuilt; point the acceptance line at
  the containment tests that exist, or mount the `GET /v1/runs` it names; drop
  `dbRuned` and its comment

**PARTIAL 2026-09-04 — the group list, four pages.** `SELECT folder FROM groups
ORDER BY folder LIMIT 500` was copy-pasted into the groups page, the memory
page, the usage page and the proactive page: four projections of one table, each
free to drift into a WHERE the owner would not serve. They now share one reader,
`dash.foldersViaRoutd`, which asks routd's `GET /v1/groups` — measured live, it
answers `[{"folder":…}]`.

Mutation-checked: a reader that returns nothing turns 15 tests red, so the pages
genuinely depend on it rather than on a fallback.

No fallback to the DB was added, deliberately. A read that silently drops back
to the file on a routd hiccup is the second path this rule exists to remove, and
it would make the defect invisible instead of fixed — the pages now render a
loud "could not reach routd" banner instead.

**Me-secrets: CLOSED 2026-09-04 by amending the rule, not the code.** `6/28`
now carries an exception for a read the owner REFUSES to serve, and states the
burden: a named, deliberate refusal in the owner's code, not an endpoint nobody
got round to writing. `/dash/me/{secrets,env}` reading `secrets` directly is
correct rather than tolerated.

The original finding, kept because it is the exception's whole justification:
Me-secrets/env reads `secrets`, and routd's secrets resource is deliberately
write-only — `routd/secrets_resource.go:43`: "Write-only: no read twin, no agent
MCP tool." Satisfying `6/28`'s rule there means creating a REST **and MCP**
surface that LISTS secrets, which is strictly worse than the direct read it
would replace. So `6/28` needs a recorded exception for owner-refused reads, not
code: the rule assumes the owner will serve everything a dashboard shows, and
here the owner refuses on purpose.

**Tasks: DONE 2026-09-04, verified live.** `service:dashd` now holds
`tasks:read` and `messages:read`, and `GET /v1/tasks` answers 200 from the
krons container — the same probe that answered 403 and forced the revert.

The ceiling test was updated rather than deleted: the two scopes are listed with
why, and the exact-length check still bites, so a third addition fails until
someone justifies it the same way. Both are READ-ONLY and both replace a direct
`routd.db` read the pages already performed, so dashd's reach NARROWS — a file
handle becomes two endpoints its owner gates.

**Status, Activity and Usage's message reads still need a HANDLER, not a scope.**
`messages:read` is now held, but routd serves no aggregate: its message reads are
`/v1/messages/{find,inspect,thread}` and its session surface is
`/v1/topic_sessions` (one folder's session id, not per-folder counts). Each page
needs a small REST-only handler on routd — the `handleUserScopes` shape, plain
`mux.HandleFunc` + `s.authed`, no resreg resource and therefore no agent MCP
tool. That is the remaining work, and it is bounded.

**Superseded — the four-page consolidation:** Tasks, Status,
Activity and Usage's message reads all fail at the same place: `service:dashd`'s
scope set. routd gates its reads with
`scopeRead = {chats:read:own_group, chats:read, messages:read}`
(`routd/turns.go:34`) and its task list with `tasks:read`; dashd's token
(`authd/http.go:103`) holds none of them. Measured live on krons:
`GET /v1/groups 200`, `GET /v1/tasks 403`.

`authd/service_dashd_test.go:57` pins that set exactly — "every scope beyond
these needs the sign-off `routes:write` got" — so the ceiling is deliberate and
one sign-off unblocks all four. **The decision: add `tasks:read` and
`messages:read` to `service:dashd`, both read-only.** Neither confers new
visibility: all four pages already display this data by reading `routd.db`
directly. It replaces four file handles with doors their owner controls.

**Per-page detail:** Status (`sessions`), Activity and Usage's
two `FROM messages` reads, Grants' `user_profiles` + `acl_membership` datalists (the scope/groups
datalist IS converted; `/v1/acl_membership` is DELETE-only, with no list face),
Profile (`user_profiles`), and Me secrets/env (`secrets`). **Tasks: written, deployed, REVERTED the same day — blocked on ONE scope that
the repo gates.** Two of my readings of this page were wrong, in opposite
directions, and the sequence is the useful part.

First I recorded it as blocked because routd's list takes one `?folder=` while
the page needs N glob patterns. That premise was false: `resreg`'s list carries
no `LIMIT`, and `tasksTarget` (`routd/tasks_http.go:37`) reads dashd's empty
folder claim as list-all. So I converted it, tested it, and shipped it.

Then a live probe against krons answered `GET /v1/tasks 403`. `service:dashd`'s
grants (`authd/http.go:103`) carry `sessions:read` but not `tasks:read`, so the
page rendered "could not reach routd" on all three instances. Reverted.

**The decision this needs: grant `service:dashd` the read-only scope
`tasks:read`.** `authd/service_dashd_test.go:57` refuses it by design — it pins
dashd's scope set exactly and says "every scope beyond these needs the sign-off
`routes:write` got". The scope confers nothing new: the page already showed
every task by reading `routd.db` directly, so it replaces a file handle with a
door the owner controls. No `tasks:write` — the cockpit mutates nothing.

Also established and worth keeping: the REST path is `/v1/tasks`, NOT
`/v1/<name>`, because timed calls it across the container boundary (BUGS `F21`).
A conversion that assumes the resource name is the path gets a 404, which is how
this one started.


- **Status:** closed 2026-09-20. The prose halves were already fixed: `6/30`'s
  runed row now reads "overview (capacity + breaker + run table)" with kill and
  stop, and neither `GET /v1/runs` nor `/v1/capacity` nor `Snapshot()` is cited
  anywhere in the spec. Re-measured against the tree today, and the code half of
  the entry holds exactly: `runed/server.go` mounts `POST /v1/runs`,
  `GET /v1/runs/{run_id}` and `DELETE /v1/runs/{run_id}` — there is still no GET
  list — while `runed/dash.go` does serve the run list with `state=` filtering.
- **Fix:** one stale citation was left and is corrected here. The row pointed at
  `runed/server.go:38-43` for "kill run, stop folder's run"; those lines are
  `NewServer` today. The controls are at `:61-70`.
## ✅ FIXED 2026-09-04 L142 — `6/28`'s core rule "no dashboard reads a SQLite DB directly" is false: at least 6 of 9 retained dashd pages still read `routd.db` for their displayed content (2026-08-30, fixed 2026-09-04)

**FIXED 2026-09-04.** Every display read now goes through the owner, verified
against the live krons container rather than a stub — `GET /v1/groups`,
`/v1/tasks`, `/v1/sessions/counts`, `/v1/messages/volume`,
`/v1/messages/activity`, `/v1/principals`, all 200.

Four new reads are plain handlers on routd, never resreg resources, and that is
the design rather than a shortcut: resreg gives REST and MCP from one
declaration, so registering them would hand every agent a tool for cross-folder
message previews and the list of every principal. The cockpit needs them; an
agent does not.

Each NARROWS what a caller may ask rather than proxying SQL — routd truncates
the activity preview to 80 chars, clamps `limit`, applies `is_bot_message=0` to
the histogram, and `/v1/principals` projects sub+name only. `service:dashd`
gained `tasks:read` and `messages:read`, both read-only, both replacing a direct
`routd.db` read; dashd's reach NARROWS, from a file handle to six gated
endpoints. authd's ceiling test keeps its teeth — the additions are listed with
their justification and the exact-length check still fails a third.

Me-secrets stays a direct read, and `6/28` now says why: a read the owner
deliberately refuses to serve is an exception to the rule, not a violation of it.

**What this cost, recorded because the tests never caught it:** the tasks page
shipped green through the whole suite and answered 403 in production, because
the stub answered where live routd refused. It took a container-side probe to
find, a revert to unbreak, and a scope to fix. Every endpoint above was
re-probed live before this entry was closed.

Found auditing every phase-6 spec that claims `status: shipped`, the same method
that found 13 drifts when phase 5 closed.

**The promise.** `6/28` §Read-path: "Every datum a dashboard shows flows through
the owning daemon's `/v1` surface. No dashboard — hub or per-daemon — reads a
SQLite DB directly." `6/29` marks reads "DONE 2026-08-30" and lists as
Acceptance: "No dashd handler opens a SQLite DB or reads a group's FS directly."

**The code.** Direct SQL against `routd.db` (`d.adminDB()` / `d.dbRoutd`) is the
PRIMARY content of these pages, not a pre-write authz check:

| page | read | site |
| --- | --- | --- |
| Groups | `SELECT folder FROM groups ORDER BY folder LIMIT 500` | `dashd/main.go:1193` |
| Status | `SELECT group_folder, COUNT(*) FROM sessions` | `dashd/main.go:856` |
| Tasks (list) | `SELECT … FROM scheduled_tasks` | `dashd/main.go:953` |
| Activity | `SELECT … FROM messages` | `dashd/main.go:1067` |
| Memory | `SELECT folder FROM groups` + `os.ReadDir` of the group FS | `dashd/main.go:1492-1521` |
| Usage | `SELECT folder FROM groups`, two `FROM messages` | `dashd/usage_page.go:89,198,223` |
| Grants | `user_profiles`, `acl_membership`, `groups` for the datalists | `dashd/grants_admin.go:116-149` |
| Profile | `SELECT name FROM user_profiles` | `dashd/profile.go:36` |
| Me secrets/env | `secrets` via `secretsDB()` | `dashd/me_secrets.go:63`, `me_env.go:51` |

`usage_page` and `grants_admin` are two of the pages `6/29` names BY NAME as
"READS DONE". `grants_admin.go:59-62` carries a comment admitting the gap — "the
three datalist reads below still go to the DB … See 6/29 for what closing them
needs" — so the code says open work on the same line the spec says closed.

**NOT counted here** (defensible, and the distinction matters): `routes_admin.go`
`SELECT target FROM routes WHERE id = ?` and `groups_admin.go`
`SELECT COUNT(*) FROM groups WHERE folder = ?` are existence/authz checks inside
a write transaction on a table dashd is FS-mounted on — that is the sanctioned
direct-WRITE path from CLAUDE.md §"Split write-discipline", not a display read.
`route_tokens.go` and `routes_admin.go`'s GET listing are genuinely migrated.

**Why it matters beyond tidiness:** the rule exists so a dashboard cannot show a
view of another daemon's data that the daemon itself would not serve — no schema
coupling, no bypassed authz on the owner's side. Nine pages currently can.

- **Severity:** medium (architectural rule stated as shipped, not enforced)
- **Scope:** dashd cross-cutting pages
- **Affected:** `dashd/main.go`, `usage_page.go`, `grants_admin.go`, `profile.go`, `me_secrets.go`, `me_env.go`, `specs/6/28`, `specs/6/29`
- **Status:** open — `6/28` and `6/29` carry `defected`
- **Fix:** migrate each read to routd's `/v1`, or amend the specs to state the real rule (which may be "no CROSS-daemon read", given dashd is FS-mounted on routd.db)

## ✅ FIXED 2026-09-04 L141 — dashd, the hub the cockpit specs are about, is the one dashboard NOT on the shared operator gate, and still carries the `**` shortcut `6/28` says was replaced (2026-08-30, fixed 2026-09-04)

**FIXED 2026-09-04.** Three copies became one, each verified before deleting:
`dashd.callerGroups` was byte-for-byte `auth.CallerGroups` (same header, same
unmarshal, same nil-on-empty) and is gone; the literal `"**"` is now
`auth.OperatorGroup`; `main.go`'s own `slices.Contains(callerGroups(r), "**")`
is `auth.IsOperator(r)`. The operator and per-folder refusals render
`auth.Forbidden`'s themed banner instead of `http.Error` bare text.

`callerScope`'s DB fallback stays: when `X-User-Groups` is absent it reads
`auth.UserScopes(sub)`, which the shared `IsOperator` has no equivalent for, so
delegating the whole decision would have LOST a path rather than unified one.
The shared readers are used where they are the same rule, and only there.

`dashd/operator_gate_test.go` pins both halves; both mutation-checked (bare-text
refusal, wrong operator constant), both red.

Still true, and not this entry's claim: `ttsd/dash.go:36-38` inlines
`auth.IsOperator` + `auth.Forbidden` rather than `RequireOperator`, documented in
code as deliberate because ttsd holds no `KeySet`.

**The promise.** `6/28` Build state: `auth/dashauth.go` is "the one operator
decision every daemon dashboard applies", replacing three drifted copies — one of
which it names as "dashd carried the `**` shortcut inside `callerScope`". It also
requires that "a deep-page 403 renders the theme error banner (not plain text)".

**The code.** `grep -rn 'auth.RequireOperator|auth.RequireWrite|auth.Forbidden'
dashd/*.go` returns **zero**. dashd gates through its own `d.requireOperator`
(`dashd/authz.go:99-108`) → `d.callerScope` (`:40-59`), and
`slices.Contains(allowed, "**")` is still there at `dashd/authz.go:55` — the exact
shortcut the spec says was removed. Its refusal is
`http.Error(w, "forbidden", 403)`: bare text, not the themed banner.

Ten call sites across eight files, including `handleServices` — the hub itself.

The seven daemons the spec lists DO use the shared gate (`timed/dash.go:50`,
`onbod/dash.go:23`, `webd/dash.go:45`, `proxyd/dash.go:35`, `authd/dash.go:31`,
`runed/dash.go:29`, `routd/dash.go:86`), so the claim is true of everything
except the daemon the specs exist to describe.

Separately, `ttsd/dash.go:36-38` inlines `auth.IsOperator` + `auth.Forbidden`
rather than calling `RequireOperator`. That one is documented in code as
deliberate (ttsd holds no `KeySet`), so it is a fourth pattern rather than a
drifted copy — lower severity, but it means "the one gate" is not literally true.

- **Severity:** medium (auth uniformity claimed and not held; no known bypass — `callerScope` still requires an operator scope)
- **Scope:** dashd authz
- **Affected:** `dashd/authz.go`, 8 dashd page files, `specs/6/28`
- **Status:** open — `6/28` carries `defected`
- **Fix:** dashd adopts `auth.RequireOperator`/`RequireWrite`, deleting `callerScope`'s `**` branch; or `6/28` states dashd's folder-scoped gate as a second deliberate exception alongside ttsd's

## ✅ FIXED L140 — three shipped adapter specs still count ten channel adapters; `mastd` was deleted 17 days earlier (2026-08-30, closed 2026-09-20)

`mastd/` was removed in `b72e80f9` ("feat(channels)!: drop Mastodon support",
2026-08-13) — the directory, `template/services/mastd.yml`, the `go-mastodon`
dependency and every reference in root docs, ant skills and web docs. Nine
adapters remain: whapd, teled, slakd, bskyd, reditd, linkd, discd, emaid, twitd.

`6/31` says "the ten channel adapters" (`:9`) and "All ten adapters now serve
`/dash/<adapter>/`" (`:242`). `6/32` is TITLED "the ten channel adapters", repeats
the count four times, and carries `mastd` rows in its controls and acceptance
tables — acceptance criteria for an adapter that does not exist. `6/33` lists
`mastd` as one of five social adapters (`:6`).

`6/31` and `6/32` were last edited 2026-08-30 — seventeen days AFTER the removal —
and `6/32`'s own "Build state (2026-08-30)" asserts all ten serve a dashboard. So
this is not stale prose overtaken by a later change; it is a shipped-status claim
written after the fact with the count never corrected.

Everything else in the three specs verified CLEAN: one `chanlib/dash.go` renderer
across all seven Go adapters with no per-adapter code, the three-state health
model single-sourcing its threshold with `/health`, the capability strip probed
via `isStubVerb`, the four `DashSectioner` implementers exact, and
`tests/socialboundary` passing all three subtests.

- **Severity:** low (documentation; the mechanism holds for all nine)
- **Status:** closed 2026-09-20 — ALREADY FIXED, by `64dcc899` ("the gitd
  headcount") and `23119576` ("phase 6 states the present, and the adapter
  counts follow the tree"). Re-measured today: `mastd` appears **zero** times in
  `6/31`, `6/32` and `6/33`. "Ten channel adapters" is true again for a
  different ten — `mastd` left and `gitd` arrived — and the fragments in
  `template/services/` list exactly those ten. `6/33`'s social/chat split is
  right: bskyd, reditd, twitd, linkd + teled, discd, slakd, whapd, emaid.
- **Scope:** specs
- **Affected:** `specs/6/31`, `specs/6/32`, `specs/6/33`
- **Status:** open
- **Fix:** ten → nine throughout; drop every `mastd` row and the `6/33:6` entry
- **Also worth a line:** twitd's `STALE_SECONDS` and whapd's equal their `/health`
  threshold today, but only a COMMENT says they must — no test enforces it. The Go
  side cannot drift this way; the TS side can.

## ✅ FIXED 2026-08-30 L139 — collapse the two MCP surfaces into one; a route token authenticates an ANONYMOUS principal whose destination set has one element (2026-08-30)

Found designing `arizuko talk` (`6/26` Gap 1). The operator's framing, in order:
*"better to let the client choose his destination and only authenticate him
through a secret or oauth"*, then *"this would collapse the system and make it
simpler"*, then the one that makes it work: *"anon tokens — it can be auth for
anon, default same way, just no varying destination. No change."*

**Today there are two MCP surfaces over one queue, with two vocabularies:**

| surface | auth | tools |
| --- | --- | --- |
| `POST /mcp` (`webd/mcp.go`) | identity — proxyd stamps `X-User-*`, folder gated by `auth.MatchGroups` | `list_groups`, `send`, `get_history` |
| `POST /chat/{token}/mcp` (`webd/chat_mcp.go`) | capability — the token IS one JID, no identity | `send_message`, `get_round`, `get_round_status` |

`send`/`send_message` and `get_history`/`get_round` do the same job for the same
queue. A client author must know which door they came through before they can
name a tool. That is "one renderer, many sinks" inverted.

**The unification, in one sentence:** every caller authenticates, and the only
thing that differs is the SIZE OF THE DESTINATION SET. A signed-in human gets N
folders and picks one; a route token gets exactly one and cannot vary it. Same
tools, same flow, no second vocabulary — `list_groups` returning a single row IS
the pinned JID, said in the language the surface already speaks.

**It fits the code as written.** `buildWebMCP(sub, name, grants)` takes the
principal and the grant list as plain arguments — `handleMCP` happens to read
them from proxyd's headers, but the builder does not care where they came from.
A token path calls the same builder with `sub = "anon:<jid>"` and
`grants = []string{row.Folder()}`. `lookupRouteToken` already resolves the row
and already refuses a token naming a deleted folder. Nothing new is invented;
one call site is added and one tool set is deleted.

**What CANNOT be removed, and why the instruction "remove all tokens" must not be
read literally.** The token is the CREDENTIAL for a caller that has no identity,
and there are two real ones:

- an external human handed a `/chat/<token>/` link, who has no authd account;
- a third-party service POSTing `hook:<folder>/<source>`, which has no account
  either and cannot perform a login.

`route_tokens` also carries per-link `context` (`5/W`) with no identity twin, and
`target_folder`, which exists because a suffixed JID cannot be parsed back into a
folder. Deleting the table would delete the anonymous product, not simplify it.

**So the proposal is: delete the second SURFACE and the second VOCABULARY, keep
the token as one of two ways to become a principal.** Concretely:

1. `webd/chat_mcp.go`'s three tools go; `/chat/{token}/mcp` routes to the same
   `buildWebMCP` with a one-element grant set.
2. `send`, `list_groups`, `get_history` become the only names.
3. The non-MCP `/chat/<token>/` browser surfaces (`POST /chat/{token}`, the SSE
   stream, the history read) are UNAFFECTED — they are a web page's transport,
   not an agent's tool vocabulary.

**Blast radius to measure before shipping:** every existing `/chat/<token>/mcp`
client breaks on the tool rename. The web chat UI is the known one; `6/26`'s
`arizuko talk` is being built against `/mcp` already, so it is not affected.
A deprecation window or an alias may be needed — that is part of the decision.

**Why it surfaced now:** `talk` was specified against the capability surface and
rewritten to the identity one the same day, because the identity path already
worked end to end and nothing had to be built. If the newest client needed no
new mechanism, the second vocabulary earns its keep only for the anonymous case
— and the anonymous case turns out not to need it either.

- **Severity:** low (no runtime defect; surface duplication)
- **Scope:** webd MCP surfaces / client tool contract
- **Affected:** `webd/mcp.go`, `webd/chat_mcp.go`, `specs/5/W`, `specs/6/26`,
  `specs/5/J`, `SECURITY.md`, `webd/README.md`, the token reference page
- **Status:** fixed
- **Fix:** one `buildWebMCP`, two identity sources, grant set of N or 1;
  `webd/chat_mcp.go` deleted

Fixed as signed off. `handleChatTokenMCP` moved to `webd/mcp.go` and calls the
same `buildWebMCP` with `sub = "anon:chat-<shorthash(token)>"` and
`grants = []string{row.Folder()}`. `webd/chat_mcp.go` is deleted whole — its
three tools and the four helpers only they used. `list_groups`, `send` and
`get_history` are the only names on either door.

Two facts the grant list alone could not carry, so `buildWebMCP` takes a
`*chatPin`:

- **The destination JID is READ, not derived.** `send` built `"web:"+folder` and
  `get_history` read `MessagesByTopic`, which is `MessagesByThread("web:"+folder)`.
  A token's JID can be `web:t/sfx` or `hook:a/b`, which no folder rebuilds — so a
  pinned caller would have written to, and read, the wrong chat. Both tools now
  take the JID off the row. This is the
  [identity_derived_not_read] class, caught before it shipped.
- **The link `context` (5/W) rides `send`.** It reaches the prompt via
  `routd/prompt.go:99`; the inline injector in `send` had no field for it.

`send` therefore funnels through `injectRouteMessage`, the injector the POST door
already used, instead of growing a second one that also carries `link_context`
(no-duplication rule). Its log is now `webd inbound`, with `token_hash` only when
there is a token.

**Gate:** unchanged — `tokenSurface` (CORS + anon DoS shield). Not `identified`,
which demands an `X-User-Sub` an anonymous visitor cannot have; not `transited`,
which guards the long-lived SSE subscription. Containment is the grant set being
built from the ROW: `auth.MatchGroups` is segment-wise, so the token reaches its
folder and NOT its subfolders — pinned by test.

**Blast radius, corrected.** No JS client calls these tools (`webd/static/` is
`style.css`; the widget is POST + SSE). The measurement missed one real client:
`tests/integration_features_smoke_test.go` drove `send_message` + `get_round`
(`wait=true`). The collapsed surface has NO blocking round read, so that test now
makes one blocking `POST /chat/<token>?wait=120` — still one call, no poll loop,
server-side deadline — against a surface this change does not touch. Its
`chatMCPCall` helper is deleted.

## L138 — the CLI cannot revoke the acl row `/always` writes, and cannot type one (2026-08-30, open)

`always` (spec `6/26` Decision 5, shipped 2026-08-30) writes a skip-approval
row: `hold:mcp:<tool>` at `effect='deny'` carrying the call's argument
narrowing. Three surfaces can now address a shaped row — `add_acl`/`remove_acl`
on the agent + REST face, and `/dash/groups/<f>/grants` (its revoke form has
carried `params` all along). `arizuko grant`/`ungrant` cannot:

- `runUngrant` (`cmd/arizuko/main.go`) hardcodes `Action: "admin"`,
  `Effect: "allow"` and no `params`, so it addresses exactly one row shape. An
  operator on the host cannot undo an `always`.
- `grantableAction` accepts `mcp:`, `shell:env:` and `<resource>.<verb>` but
  not `hold:`, so the same CLI cannot type a hold rule OR its exception either
  — the `grant_action_needs_a_door` shape, where a new action namespace is
  unreachable until this allowlist learns it.

Not a hole: the dashboard and `remove_acl` both work, and `/always` answers with
the exact `remove_acl` call that undoes what it wrote. It is a gap in the
operator's host-side escape hatch, which is the surface that matters when the
dashboard is the thing that is broken.

- **Owed:** `arizuko grant/ungrant` take `--action`/`--effect`/`--params` and
  `grantableAction` learns `hold:`. Additive; no behaviour changes for the
  existing call shapes. Worth pairing with a `grants` listing that prints
  `params`, which `runGrants` also omits today.

## ✅ FIXED 2026-09-06 L97 — `/approve` on a held call is a no-op: the re-issue trigger sorts BEHIND the cursor that skips it (2026-08-26)

**FIXED 2026-09-06 in `55cf3086`, `936a4fcb`, `56d41b37`, `5734a9c6`.**
`core.TSLayout` is the one write layout for every TEXT timestamp column:
nine fraction digits, always, forced to UTC, so lexicographic order over the
column IS chronological order. Thirty-five writer sites across `routd/`,
`store/`, `chanlib/` and `dashd/` render through `core.TS`; five of them had
no `.UTC()` at all, which broke width as well as order. Readers are unchanged
— `time.Parse(time.RFC3339Nano, ...)` reads a fixed-width nine-digit
fraction, measured rather than assumed.

Nine render sites stay on `time.RFC3339Nano` on purpose: they build a JSON
response field or an audit event and are never sorted. `pending_actions` and
`invites` stay on plain `time.RFC3339`, which has no fraction and is already
fixed width on BOTH the write and the compare — changing one side would have
created the defect there.

`routd/migrations/0044-fixed-width-timestamps.sql` pads `messages.timestamp`,
`chats.agent_cursor` and `sessions.observed_cursor`, GLOB-scoped so a re-run
matches nothing. Rehearsed against copies of all three live databases: krons
6560/28/9 rows, marinade 5072/25/26, sloth 7248/34/34, no column left mixed,
a second run byte-identical, 986 ms on the largest. An independent pass with
real `time.Parse` confirmed 0 instants changed, 0 rows newly visible and 0
newly hidden.

**The ordering inside the migration is load-bearing and measured.** Padding a
cursor makes it shorter than the unpadded rows of its own second, so
normalising cursors BEFORE rows re-feeds consumed messages — 28 on krons, 27
on marinade, 33 on sloth. Rows first measured no flip. `db_utils` runs the
whole file in one `BEGIN EXCLUSIVE`, so no reader observes the partial state
either way; the file states the order for anyone running the statements by
hand.

**The migration recovers nothing already lost.** Both stranded `hitl-` rows
on krons sit behind cursors that advanced long ago. Its job is to make the
code fix work on a deployed instance, which it must: a chat holding a legacy
whole-second cursor still hides a new fractional trigger. `L102` (clock skew)
and `L103` (exact equality) survive a fixed-width format untouched.

**A fixture is a writer too.** Eleven test files seeded a compared column with
raw SQL and `time.RFC3339Nano`, which collides with the padded cursor the code
now writes: the stripped form of an instant whose nanoseconds end in zero sorts
AFTER the padded form of that same instant, so the row is never excluded.
`TestBuildAgentPrompt_DoesNotConsumeTheObservedWindow` lost about one run in
ten on it. Fixed in `2f90e308` — every fixture renders through `core.TS`.

HITL approval never completes in chat. The operator types `/approve <id>`, gets
`approved <id> (like) — the agent will re-issue it`, and **nothing happens**.
The held call is never re-issued.

`resolveHoldTx` (`routd/pending_actions.go:270`) writes the re-issue trigger
with `resolutionMessage`, whose timestamp is `time.Now().UTC()` — nanosecond
precision. Every message timestamp goes through one renderer,
`routd/db.go:262`, and that renderer is `time.RFC3339Nano`. **RFC3339Nano
strips trailing zeros**, so it is not fixed-width: a whole-second time renders
`2026-08-26T19:06:41Z`, a nanosecond time `2026-08-26T19:06:41.882458214Z`.

The dispatch scan compares that column as a string. `.` is 0x2E, `Z` is 0x5A,
so the fractional form sorts BEFORE the bare form in the same second.

**CORRECTION 2026-08-26.** This entry first named `store/groups.go:184`
(`PendingChatJIDs`) as the comparison. That function has **zero callers** —
`grep -rn "PendingChatJIDs" --include=*.go .` returns only its definition. It is
dead code, and a fix or test scoped to it would change nothing. The live
comparisons are `routd/db.go:309` (`NewMessages`, the `pollOnce` feed),
`routd/db.go:321` (`MessagesSince`, the `processGroupMessages` feed) and
`routd/loop.go:588` (the pollOnce skip gate, which formats with
`time.RFC3339Nano` and compares `<=`). The observed failure and the marinade
verification stand unchanged; only the cited site was wrong. The trigger is invisible to the scan that must pick it up, and
stays invisible forever — the cursor only moves forward.

The collision is the normal case, not a race. `/approve` advances the cursor
to its own timestamp; adapters store platform timestamps truncated to the
second (Slack ts `1784213589.114039` lands as `2026-07-16T14:53:09Z`); the
resolution message is written in the same handler, so it is always in that
same second, and always carries a fraction.

- **Cost:** every chat-driven HITL approval on every platform. The REST face
  (`POST /v1/pending_actions/{id}/approve`) runs the same `resolveHoldTx`, so
  it is broken the same way.
- **Verified 2026-08-26 on marinade**, `atlas` folder, Slack `#random`, with a
  real `hold:mcp:like` rule. The hold, the notice, `/approve`, the operator
  gate and the release all work; only this link is broken:

  ```
  sqlite> SELECT m.timestamp, c.agent_cursor,
          (m.timestamp > COALESCE(c.agent_cursor,'')) AS visible
          FROM messages m LEFT JOIN chats c ON m.chat_jid=c.jid
          WHERE m.id='hitl-c5a53a20-1787771201882452082';
  2026-08-26T19:06:41.882458214Z|2026-08-26T19:06:41Z|0
  ```

  With the trigger delivered by hand instead, `ConsumeApprovedAction` released
  the call and the 👍 landed on the real Slack message — so the defect is this
  one comparison, not the HITL design.

- **Cause:** a column used as a sort key is written with a format whose string
  order is not its time order. One renderer, but the wrong renderer.
- **Proposed fix (cross-cutting → sign-off):** make `routd/db.go:262` write a
  fixed-width layout (`2006-01-02T15:04:05.000000000Z07:00`) so string order is
  time order, plus a migration normalising existing `messages.timestamp` rows.
  Every other comparison on this column (`routd/db.go:309`, `:321`, `:435`,
  `:938`, `routd/loop.go:588`) becomes correct by construction. Same audit is
  owed to the other RFC3339Nano sort keys — `store/cost_log.go:33`,
  `store/turns.go:19`, `store/pairing.go:82`.
- **This fix is NOT sufficient on its own.** `L102` (clock skew between the
  platform and host domains) and `L103` (equal timestamps) both survive a
  fixed-width format. Decide all three together.
- **Rejected:** nudging the resolution timestamp forward a second, or comparing
  with `datetime()` at each call site — both leave the non-monotonic format in
  place for the next reader.
- **Not this bug:** the held notice on marinade carries no `/dash/approvals/`
  deep link. `approvalURL` landed in `d82ed823` at 18:41 on 2026-08-24; the
  instance image was built 12:51 the same day. Stale image, not a defect.

## L98 — HITL's operator surface has no end-to-end test on either face (2026-08-26, open)

`L97` — chat `/approve` never re-issues the held call — survived because
nothing exercises the approval path end to end.

- **Chat face:** `routd/pending_actions_test.go` covers the hold, the acl rules,
  the operator gate and `ConsumeApprovedAction` in isolation. No test writes a
  resolution message and then asserts the dispatch scan picks it up, which is
  exactly the link `L97` breaks.
- **REST/dashboard face:** `dashd/approvals_page.go` renders the review queue
  and a `POST /dash/approvals/{id}/resolve` button. `tests/dashd-playwright/tests/`
  holds 13 specs — auth, davd, grants, groups, invites, profile, read-only,
  routes, secrets, services, tasks, tokens — and **none for approvals**. The
  one operator surface that gates destructive agent actions is the one page with
  no browser coverage.

Both faces funnel through `resolveHoldTx`, so both carry `L97` identically.

- **Owed:** one integration test that holds a call, resolves it, and asserts the
  agent re-issues — at the dispatch level, not the unit level.

**Dashboard half CLOSED 2026-08-26.** `tests/dashd-playwright` now covers every
`/dash/` surface dashd mounts, `approvals.spec.ts` included: 113 passing, up
from 44 passing and 10 failing. Fixing the suite is what exposed the rest of
this entry — the harness seeded `store/onbod.db` while dashd reads
`store/onbod/onbod.db`, hand-copied an invites schema that had drifted to a
`ref` primary key, and never set `SECRETS_KEY`, so nine failures read as
product bugs. Four more tests asserted surfaces deleted months earlier (the
tokens mint form, the profile wording).

Two gaps stay open, both harness-side:

- **The transit guard is never exercised.** `AUTHD_URL` builds dashd's key set,
  and a non-nil key set turns `guard` on — every request would need a real
  `service:proxyd` ES256 bearer. The suite therefore tests dashd's own authz
  and not proxyd's transit proof, and `/dash/authd/` is covered on its
  unreachable path only. Closing it means minting a transit bearer in
  `global-setup.ts`.
- **`dashNavFor` derives "operator" twice.** It reads `X-User-Groups` directly
  while `callerScope` falls back to an ACL read, so a caller who is an operator
  by grant but arrives without the header gets the pages and not the links to
  them. proxyd always stamps the header in production, so this bites tests and
  any non-proxyd caller, not the live dashboard. The fix — `dashNavFor` calling
  `d.callerScope` — needs `d` threaded through `pageShell`, which every handler
  calls, so it is a proposal, not an inline fix.
- **Also surfaced by the same audit:** `acl_membership` has no create surface
  outside the CLI. `dashd/grants_admin.go:52` renders `acl` rows and only
  consumes `acl_membership`, so an operator cannot add a principal to
  `role:operator` from the dashboard.

## L99 — `/invite/` and `/onboard` are `public` routes whose handlers key on identity, so onboarding is an infinite redirect (2026-08-26, proposal — needs sign-off)

Nobody can redeem an invite or create a world through the web. The link 303s to
`/auth/login`, OAuth returns instantly because the provider session is live, and
the return lands back on the same route with no identity — a loop the browser
ends at its hop limit.

`compose/compose.go:404-406` declares all three routes `Auth: "public"`.
proxyd's public arm (`proxyd/main.go:642-644`) is `rp.ServeHTTP(w, r)` and
nothing else: no `tryAuth`, no `setUserHeaders`, no transit. `stripClientHeaders`
(`proxyd/main.go:90-97`) has already deleted `X-User-Sub`/`-Name`/`-Groups` at
`:466`, so the backend sees an anonymous request by construction. onbod then
reads `X-User-Sub`, finds `""`, and bounces:
`onbod/main.go:1196-1205` for `/invite/{token}`, `:639-643` for `/onboard`.

`optional` would not fix it either — that arm only runs `tryAuth`, which reads a
Bearer header, and a browser navigation carries a cookie.

- **Verified live on krons 2026-08-26**, and the cookie's Max-Age is the
  discriminator:

  ```
  $ curl -sSi https://krons.fiu.wtf/invite/testtoken123
  HTTP/1.1 303 See Other
  Location: /auth/login
  Set-Cookie: auth_return=/invite/testtoken123; ... Max-Age=86400 ...
  ```

  `86400` is onbod's cookie (`onbod/main.go:1200`); proxyd's gate writes `600`
  (`proxyd/main.go:918`). So the request reached onbod **through the public
  arm, unidentified** — it was never gated.

- **Live blast radius, all three instances:** `invite_redemptions` is 0
  everywhere (krons 0, sloth 0, marinade 0) though sloth minted 4 invites. No
  `acl` row has ever carried `granted_by='invite'` (`onbod/main.go:1257`) or
  `granted_by='onbod'` (`:976`) on any instance. `L45` (fixed 2026-08-17) kept
  `/invite/` out of the route table until nine days ago and explains the older
  half of that zero, not the current state.
- **Cause:** the wrong auth mode on a surface that needs a caller.
  `/pair/` — the same shape, the same requirement — got it right at
  `compose/compose.go:389` with `Auth: "user"` and the comment "so an anonymous
  visitor is bounced through OAuth with the pairing URL as the return path".
- **Proposed fix:** move the three rows to `Auth: "user"`. onbod's
  `userSub == ""` branches then become dead code instead of the only reachable
  branch. Local in code, but it changes a live route table and needs
  `arizuko generate` on every instance — hence sign-off, not an inline fix.
- **Not verified:** the authenticated case. No session was available to drive
  it. A live browser that reaches `/onboard` and renders would disprove this.

## ✅ FIXED 2026-09-20 L100 — proxyd and webd authorize under a `user:`-prefixed principal, so `/dash/proxyd/` and webd's `routes.*` tools are 403 for everyone (2026-08-26)

The JWT `sub` is always prefixed — `authd/oauth.go:329` mints
`claimSub := "user:" + sub`, `authd/server.go:361` the same on refresh — and
`proxyd/main.go:757` stamps `X-User-Sub` with the raw claim. Two readers then
carry it into the ACL unchanged:

- `proxyd/resource.go` `callerFromHTTP` — `sub := r.Header.Get("X-User-Sub")`,
  no strip, into `resreg.Caller{Sub: sub}`.
- `webd/routes_mcp.go:48` — `resreg.Caller{Sub: sub}`, same.

`acl.principal` and `acl_membership.child` store BARE subs, so the prefixed
principal expands to itself alone (`store/membership.go:181`), matches no row
(`store/acl.go:176`) and no wildcard row (`:232`), and `resreg`'s default gate
403s. Every `proxyd_routes` action is denied for every operator, and webd's five
`routes.*` MCP tools fail the same way.

`auth.BareSub` exists for exactly this and its doc already names the failure:
*"A prefixed subject compared against stored rows matches nothing and silently
grants nothing, which is exactly how identity pairing shipped inert (BUGS.md
V1)."* Five call sites strip; these two do not.

- **Evidence (live krons routd.db):** ancestors of the prefixed sub → none;
  ancestors of the bare sub → `role:operator`. ACL rows for the prefixed
  principal set → 0. Wildcard-principal rows → 0 on all three instances.
- **Masked by its own test.** `proxyd/resource_test.go:52-57` seeds
  `Principal: "**"`, and `auth/authorize.go:231` `matchPrincipal` returns true
  for pattern `**` against anything — so the test never exercises the principal.
  It also signs a bare-shaped `op@example`.
- **The operator still reaches the page**: `requireOperator`
  (`dashd/authz.go:103`) reads `X-User-Groups`, which proxyd computed through
  `groupsForSub` (`proxyd/main.go:803`) — and that one DOES strip. The gate that
  fails is downstream.
- **Fix:** `auth.BareSub(...)` at both sites — local. The cross-cutting half is
  that a per-daemon strip is now a seven-copy predicate; the durable fix is
  stripping once where the header is read.
- **Second, independent break on the same page:** `proxyd/resource.go:194`
  returns a BARE ARRAY for list while `dashd/proxyd_page.go:136-139` unmarshals
  into `struct { Routes []proxydRoute `json:"routes"` }`. Even with authz fixed
  the page renders "proxyd returned something dashd could not read".

**Authz half FIXED 2026-08-29.** Both sites call `auth.BareSub` —
`callerFromHTTP` (`proxyd/resource.go`) on the stamped header, and webd's
`callerFor` (`webd/routes_mcp.go`) on the session sub. `proxyd/caller_sub_test.go`
seeds a NAMED acl principal instead of the `**` row that masked this, and
asserts a `user:`-prefixed header reaches the Caller bare and that
`auth.Authorize` then admits it; it fails with the strip removed.

**Still open:** the response-shape break above (bare array vs `{"routes":…}`),
a separate defect on the same page, and the cross-cutting half — a per-daemon
strip is now a seven-copy predicate, and the durable fix is stripping once
where the header is read.

- **Status:** closed on re-check 2026-09-20 — both halves hold.
- **Re-measured:** the response-shape half is settled, not open.
  `proxyd/resource.go:181-188` returns a BARE array, its own comment names the
  convention, and the one in-tree consumer decodes a bare array
  (`dashd/services.go:166-169`). `grep 'json:\"routes\"'` returns **0 hits**
  repo-wide, so no envelope survives to disagree with. `dashd/proxyd_page.go`
  is gone — proxyd serves its own page from its snapshot (`proxyd/dash.go:40`,
  `ba63d55a`). The authz half stays fixed at `proxyd/resource.go:374` and
  `webd/routes_mcp.go:46`.
- **Not a defect:** the "seven-copy strip" note is a design preference about
  `BareSub`, measured at six production sites today. It is not part of this
  entry's failure.

## ✅ FIXED 2026-08-29 L101 — webd runs a second folder-containment predicate that disagrees with `auth.MatchGroups` in both directions (2026-08-26)

`webd/server.go:178` decides folder access with
`f == "**" || groupfolder.Contains(f, folder)`. `groupfolder.Contains`
(`groupfolder/folder.go:24`) is a positional prefix walk —
`child == parent || strings.HasPrefix(child, parent+"/")` — with no glob
semantics. The one evaluator is `auth.MatchGroups` (`auth/acl.go:21`), whose own
doc says *"No caller may bolt a prefix walk beside it — that reintroduces
depth-derived authority under another name."* dashd deleted its copy
(`dashd/authz.go:64-78`); webd's survived, gating 12 call sites —
`requireFolder` (`/panel/`, `/api/groups/*`, `/x/groups/*`), nine `webd/me.go`
handlers, and `webd/mcp.go:44,63` (`send`, `get_history`).

It diverges both ways:

- **Wider.** A grant of scope `main` — the exact-folder shape dashd's grant form
  writes — lets that user read and post in `main/code` through `/me/chats/`,
  `/panel/` and the `send` MCP tool, while `/dash/groups/main/code` 403s them.
- **Narrower.** A grant of scope `main/**` — the shape
  `onbod/main.go:977` `createWorldTx` writes for every world creator — matches
  NOTHING in webd, not the subtree and not the folder itself, because
  `"atlas" != "atlas/**"` and `"atlas"` has no `"atlas/**/"` prefix. The
  creator's own portal 403s them on their own world.

- **Live blast radius: currently nil, one folder away.** Every human principal
  with a grant is either a `role:operator` member (scope `**`, both predicates
  agree) or holds an exact-folder grant on a folder with no children. The moment
  a subfolder exists under sloth's `coach`, that user reads it in webd and is
  refused it in dashd. The narrowing direction fires on the first world created
  through onboarding — which `L99` currently prevents.
- **Cause:** a second containment predicate, not a wrong one.
- **Fix (local):** `auth.MatchGroups(groups, folder)` in `webd/server.go:178`,
  delete the helper. `groupfolder.Contains` stays correct where filesystem
  nesting is the reason (`auth.MatchSlot`, `/priv`, `/dav`); it is wrong as a
  grant test.

Fixed as proposed: `userAllowedFolder` is deleted and all 12 call sites
(`webd/server.go` `requireFolder`, nine in `webd/me.go`, two in `webd/mcp.go`)
call `auth.MatchGroups` directly, so webd has no containment predicate of its
own. `TestRequireFolderMatchesGrantGlobs` (`webd/server_test.go`) replaces the
old helper test and drives the real middleware: it pins both divergences —
`atlas` no longer reaches `atlas/content`, and `atlas/**` now reaches both
`atlas` and its subtree. Both rows fail against the deleted predicate.

## ✅ FIXED 2026-09-20 L102 — every steer re-injection is written BEHIND the cursor it must outrun, so `/root <cmd>`, `/new <text>` and `#topic <text>` silently never run (2026-08-26, proposal — needs sign-off)

An operator types `/root allow.it` and nothing happens. No ack, no elevated
turn, and the `pendingElevation` entry leaks.

`cmdRoot` (`routd/steer.go:222-250`) re-injects the argument as a NEW message
row stamped `time.Now().UTC()` — the HOST clock, nanoseconds. Its `l.Enqueue` is
discarded because the group is already active (`queue/queue.go:104-108` logs
"will drain after" and adds nothing to `waitingGroups`). Back at
`routd/dispatch.go:80`, `advance(chatJID, steerMsg)` moves the cursor to the
`/root` message's PLATFORM timestamp, which every adapter truncates to whole
seconds (`routd/server.go:513`; `teled/bot.go:346`, `discd/bot.go:240`,
`slakd/bot.go:539`). The only remaining pickup is the poll
(`routd/loop.go:588` → `routd/db.go:309`/`:321`), all string comparisons.

Two independent conditions strand the re-injection forever:

1. **Same second, fractional vs whole** — the `L97` mechanism, at three
   previously unrecorded PRODUCER sites.
2. **Platform clock ahead of host clock** — the cursor is the platform's second,
   the re-injection is host `time.Now()`. Any positive skew puts the
   re-injection numerically behind. No format subtlety needed, and **a
   fixed-width format does not fix this half.**

Same shape at `cmdNew`'s followup (`routd/steer.go:389-396`),
`handlePrefixLayer`'s `#topic` re-append (`:460-469`), and the agent's
`inject_message` tool (`routd/mcp.go:114-124`, which relies on the poll and
calls no Enqueue at all).

- **Verified live on sloth:**

  ```
  1531028709274943749                   /root allow.it  2026-07-26T20:01:29Z
  root-main/trading-1785096086061959777 allow.it        2026-07-26T20:01:26.061964536Z
  ```

  The re-injection is stamped **3 seconds behind** the cursor. `turn_context`
  and `messages` for that turn id: 0 rows each. The elevated turn never ran.
  Also on sloth, the pure fractional case: `cmd-new-1781452449117768949`
  ("@sloth analyze latest movements of xmr.") at `15:54:09.117778307Z` against
  cursor-second `15:54:09Z`, no turn. 3 of 4 re-injections since June dropped.
- **Cost:** every `/root` with an argument, every `/new <text>`, every
  `#topic <text>`, on every platform. The user sees "Session cleared. Processing
  your message…" and then silence.
- **Cause:** two clock domains — platform seconds and host nanoseconds — written
  into one column used as a strict-order cursor key.
- **Proposed fix (cross-cutting → sign-off):** a fixed-width format closes only
  the fractional half. The root fix makes re-injections visible independent of
  timestamp order — cursor on `rowid`, or stamping a re-injection from the
  consumed message's own timestamp. Decide with `L97` and `L103`.

## ✅ FIXED 2026-09-20 L103 — a message sharing its platform second with a batch tail is dropped forever; one Telegram upload lost 11 of 12 documents (2026-08-26, proposal — needs sign-off)

Adapters truncate platform timestamps to seconds (`routd/server.go:513`), so
equal timestamps within one chat are routine — Telegram albums, Slack bursts,
several scheduled tasks firing in one second.

`processGroupMessages` reads the batch (`routd/dispatch.go:27-28`), runs the
turn (seconds to minutes), then advances the cursor to `last.Timestamp`
(`:144`). Any row that arrived AFTER the batch read carrying the SAME second is
then `timestamp = cursor`: the strictly-greater feed (`routd/db.go:321`) never
returns it, the poll skip gate (`routd/loop.go:588`) skips the chat, and its
arrival-time Enqueue was discarded while the group was active. Nothing else
reads it. **The window is the whole duration of the previous turn**, not
milliseconds.

- **Verified live on krons.** Chat `telegram:user/1112184352`, all twelve rows
  at `2026-06-10T11:01:53Z`:

  ```
  5137 [Document: Detail porovnání produktů.pdf]      ← turn_context row exists
  5138 … 5148  (11 more [Document: …] rows)           ← no turn, any of them
  ```

  And the user-visible half, from the bot itself two minutes later:
  *"Tenhle PDF taky nedorazil — žádný přiložený soubor"* ("This PDF didn't
  arrive either — no attachment"). The agent told the user it received nothing,
  because it had.
- **Also hits the scheduled fan-out.** `sched-*-mem-N` rows sharing one second
  show the first with a `turn_context` and the rest with none, on all three
  instances (e.g. krons `2026-06-08T03:00:49Z`, four chats). Those
  `/compact-memories` runs silently never executed.
- **Fleet-wide upper bound** (rows sharing a chat+timestamp with an earlier row
  that got a turn, themselves turn-less, non-bot, non-observed): krons 86,
  marinade 62, sloth 43. An upper bound — a mid-batch row legitimately carries
  no turn id — but the exemplars above are individually airtight.
- **Cause:** the cursor's granularity equals the timestamp's granularity and the
  feed is strictly greater-than. The design assumes strictly monotonic per-chat
  timestamps, which second-truncated ingest cannot provide.
  **`L97`'s fixed-width format does not fix this — equal stays equal.**
- **Proposed fix (cross-cutting → sign-off):** a strictly monotonic sort key
  (rowid-based cursor), or `>=` plus an id-set dedup.

## ✅ FIXED 2026-09-20 L104 — a steer consumes the whole batch, discarding older unprocessed user messages (2026-08-26, open)

`routd/dispatch.go:51` picks `steerMsg = newestUserMsg(msgs)`; `:79-81` then
calls `advance(chatJID, steerMsg)` unconditionally, moving the cursor past
**every older row in the batch** — including ordinary user messages that were
never dispatched, never steered and never observed.

`L56` (fixed 2026-08-20) reasoned only about rows NEWER than the steered one
("anything newer has not been consumed", `routd/dispatch.go:71-78`). Nothing
handles older ones. `routd/steer_overtaken_test.go` pins "a later command
supersedes an earlier one" for two COMMANDS and never covers a plain message
followed by a command.

- **Failure:** the user sends "please check X", waits, gets impatient and types
  `/status` (or `/ping`, `/new`, a bare `#topic`, an `@group` sticky pin — all
  steer). Both land in one batch; the command is consumed and acked; "please
  check X" is skipped forever with no trace. An in-flight turn stretches the
  batching window to minutes (`L103`'s mechanism), so this ordering is common,
  not rare.
- **Evidence:** the mechanism is three lines of unconditional control flow and
  needs no live proof. Live candidate pairs exist on all three instances (e.g.
  marinade `5206` "who mights?" at 11:51:16 then `/root can you move a…` at
  11:51:33, no turn), but for a historical pair the older row's consumption
  cannot be excluded — rated suggestive, not proven.
- **Fix (local):** after a steer, advance only if no older un-dispatched
  non-bot rows remain — or re-run the loop over the remainder first.

## ✅ FIXED 2026-09-20 L105 — `fetch_history` caches platform rows as user-authored, and the twin poisons every `id=? OR platform_id=?` lookup (2026-08-26)

`fetchPlatformHistory` (`routd/mcp.go:390-399`, behind the agent's
`fetch_history` tool) inserts each fetched row as
`core.Message{ID, ChatJID, Sender, Content, Timestamp, ReplyToID}` — **no
`BotMsg`, no `FromMe`, no `Topic`**. The wire format cannot supply them:
`chanlib.InboundMsg` (`chanlib/chanlib.go:43`) has no bot/from-me field at all.

So a fetched copy of the bot's OWN platform message lands as a user row whose
`id` is the raw platform id, while the real outbound row carries that same value
in `platform_id`. `ReplyTargetIsBot` (`routd/db.go:1306-1312`) and `TopicByID`
(`:447-451`) are both `WHERE id=? OR platform_id=?` with `QueryRow`/`LIMIT 1`
and **no ORDER BY** — they match two rows with contradictory answers and return
whichever the query plan scans first.

- **Verified live on sloth** — the twin exists:

  ```
  out-1780064467139190875 | is_bot=1 | platform_id=1509924562530468010 | **Institutional wave + ETF inflows.** …
  1509924562530468010     | is_bot=0 | platform_id=              ''    | **Institutional wave + ETF inflows.** …
  ```

  Dozens more sloth rows in `discord:.../920916162416635944` hold the bot's own
  output ("⏳ researching…") stored as non-bot user rows.
- **Failure (a):** a user replies to a bot message on a `verb=mention`-routed
  channel; ingestion's mention promotion (`routd/server.go:533-537`) consults
  `ReplyTargetIsBot`, gets the twin's `is_bot_message=0`, does not promote, and
  the reply route-misses. The user answered the bot and got silence.
- **Failure (b):** a reply or reaction in a platform thread inherits the twin's
  empty `Topic` (`routd/server.go:523-525`) and lands in the main topic — wrong
  session, wrong thread anchor.
- **Cause:** the history cache writes rows in a shape the read paths treat as
  authoritative identity. Cross-cutting — wire format, insert site, and two
  ambiguous lookups.
- **Minimal containment:** never `PutMessage` a fetched row that collides with
  an existing `platform_id`, or mark cached rows so the OR-lookups prefer the
  authored one.
- **Confidence:** high on the mechanism, medium on frequency — the outcome is
  nondeterministic on query-plan order.


- **Status:** closed on re-check 2026-09-20 — the writer is gone.
- **Re-measured:** `routd/mcp.go:394-405` decodes the `HistoryResponse` into
  `out.Messages` and returns it; there is no `PutMessage` on the path, so no
  twin row is written. `ba3f8615` (2026-09-05) removed it, and every remaining
  `PutMessage` caller in routd mints a fresh id. `ReplyTargetIsBot` returns
  **0 hits** repo-wide — `routd/server.go:577` reads `parent.BotMsg` off a full
  row instead.
- **Left, and it is operator data not code:** the first-match-wins scan
  survives (`store/messages.go:725-730`), latent with no twin writer. Twin rows
  an instance already holds would need a data cleanup.

## L106 — the agent's `reply`/`send` sends one idempotency key and the retry sweep sends a different one, so the whole dedup contract is off for every MCP message (2026-08-26, proposal — needs sign-off; the fix crosses the ipc↔routd contract)

`routd/mcp.go:259` delivers with a throwaway key:

```go
pid, err := s.deliver.Send(jid, text, replyTo, topic, threadRoot, "mcp-"+randHex(8))
```

On failure the row is persisted with a DIFFERENT id — `ipc/ipc.go:736`
`core.MsgID("mcp")` = `mcp-<unix nanos>` — and the retry sweep re-sends under
that row id (`routd/loop.go:433`, `m.ID`). The adapter has never seen it, so
`claimDelivery` (`chanlib/dedup.go:144`) opens a fresh claim and posts again.

The contract is written at `routd/server.go:29-33`: *"idempotencyKey (the bot
row's stable message_id) so the adapter dedups platform-side."* The turn-result
path honours it — `routd/turns.go:321` passes `row.ID`, and
`routd/deliver_test.go:101` pins exactly that. The MCP tool path does not, and
has no equivalent test.

- **Verified live on krons.** The adapter ledger and routd disagree on the key
  for the same two deliveries:

  ```
  teled-rhias outbound_dedup:  mcp-6048982074c344ff | sent | platform_id 3069
                               mcp-c0ea103a81c6549f | sent | platform_id 3071
  routd messages:              mcp-1787314291520821980 | platform_id 3069
                               mcp-1787334941510537296 | platform_id 3071
  ```

  Every `out-*` key in the same ledger matches its routd row id exactly; only
  the `mcp-*` ones diverge. And the sweep does fire on these rows —
  `mcp- | failed | delivery_attempts=10 | 3 rows`.
- **Failure:** the adapter posts, the HTTP response to routd is lost (30s client
  timeout, container killed mid-response, reset). The adapter's ledger says
  `sent`; routd writes a `pending` row under a new id and re-sends it. **The
  user gets the message twice.** Second half: when the first attempt genuinely
  failed and left the claim `inflight`, the fresh key means
  `ErrDeliveryUnresolved` never fires, so the ambiguous case gets a blind resend
  instead of the designed visible dead-letter (`chanlib/dedup.go:58-63`, `D5`,
  `L32`, spec `5/34`).
- **Worse on teled:** `teled/bot.go:395` chunks at 4096 chars and
  `classifySendErr` (`:441`) deliberately holds the claim once a chunk has
  landed so routd dead-letters instead of re-posting. A mismatched key makes
  that protection inert and re-posts every delivered chunk.
- **Live blast radius:** 665 `mcp-` bot rows on krons, every one delivered
  outside the dedup contract. No confirmed duplicate yet — no `mcp-` row is
  `sent` with `delivery_attempts>0`, so the sweep has not yet re-delivered one
  to a real adapter.
- **Cause:** `mcpDeliver` delivers BEFORE the row exists, so it has no stable id
  and mints a throwaway. The turn-result path builds the row first.
- **Fix (local):** mint the row id in `mcpDeliver` and hand the same string to
  both `deliver.Send` and `putOutboundRow`.

**Correction 2026-08-29 — the fix is NOT local.** `mcpDeliver`
(`routd/mcp.go`) delivers through the `Deliverer`, and the row is minted a
package away in `ipc`'s `recordOutbound` (`core.MsgID("mcp")`), which
`mcpDeliver` deliberately must not do — its own comment says persisting there
would double-store the row. One id therefore has to cross the `ipc`↔`routd`
seam: either `GatedFns.SendMessage` takes the id as a parameter, or it returns
the id it used alongside the platform id. Both change a shared contract, so
this is a proposal awaiting sign-off, not a one-line change. The defect and the
live evidence stand exactly as written above.

## ✅ FIXED 2026-09-20 L107 — the adapter-name pin is bound to the docker container IP, so a legitimate restart is rejected; 156 rejections on krons in 30 days (2026-08-26)

`chanreg/chanreg.go:122-131` pins a registered adapter name to BOTH its service
principal and `old.OriginIP != originIP`. `originIP` is
`routd/channels.go:151-157` — `r.RemoteAddr`'s host, i.e. the adapter
container's docker-network IP, which docker reassigns on recreate. The service
principal in the same comparison is constant across restarts, so the IP is the
only field that can mismatch. `routd/channels.go:100-106` maps that to **409**,
the only error in `RegisterWithOrigin` that produces one.

The stale entry clears only after 3 consecutive `/health` failures at 30s
(`chanreg/health.go:55-70`) — up to 90 seconds.

- **Verified live on krons:** `journalctl --since "30 days ago" | grep -c "status 409"` → **156**.
  The mechanism is visible in the interleaving:

  ```
  02:01:10 routd | channel health failed  channel=whatsapp fails=3   <- deregistered
  02:01:17 whapd | registered with router                            <- fresh claim, new IP
  02:02:09 routd | channel health failed  channel=whatsapp fails=2   <- only 2, entry survives
  02:02:22 whapd | router registration failed  status 409
  ```

  Registration succeeds exactly when the health loop reached 3 first and 409s
  when it reached 2 — consistent only with the IP changing every restart.
- **Go adapters (teled/discd/slakd/emaid/bskyd/reditd/linkd) crash-loop on it.**
  `chanlib/run.go:64-67` treats any registration failure as fatal `os.Exit(1)`,
  before `net.Listen` and before `opts.Start`. The loop breaks only once routd's
  health loop has accumulated 3 failures. Outbound is dead for that window and
  inbound is dropped entirely. Not observed live — `grep "router registration
  failed" | grep -v whapd` → 0 in 30 days; Go adapters restart rarely.
- **whapd does not self-heal.** `whapd/src/main.ts:463-475` starts the HTTP
  server and fires `registerWithRetry()` without awaiting. The new container
  answers `/health` 200 on the same URL, so routd calls `ResetHealth` and
  **never deregisters the stale entry**, while `registerWithRetry` 409s forever.
  Outbound keeps working, but the registry's `Capabilities` and `JIDPrefixes`
  stay frozen at pre-restart values until routd itself restarts — so a release
  adding a whapd capability silently does not take effect.
- **Cause:** identity derived from a runtime-assigned docker IP — the exact
  failure `CLAUDE.md` opens with. The authenticated service principal already in
  that comparison is the stable half.
- **Fix (local):** drop `OriginIP` from the comparison, keep the principal pin.
  A second, independent defect: a 409 is not a config fault, so
  `chanlib/run.go`'s `os.Exit(1)` on it is wrong — that half is cross-cutting.

- **Status:** fixed 2026-09-20.
- **Fix:** the origin pin drops its address term and keeps its credential one.
  `chanreg/chanreg.go` compared BOTH the presented secret's hash and
  `old.OriginIP` against the caller; the hash is the credential and the address
  is not. An adapter's address is its docker container IP, which changes on
  every legitimate restart, so the adapter's own return was refused — and
  `chanlib/run.go:64` exits 1 on a failed `Register`, before `opts.Start` and
  before it listens, so the adapter then crash-looped.

  The security property is unchanged: an attacker who holds the secret is
  already inside, and one who does not is stopped by the hash whatever address
  they call from. `OriginIP` stays on the entry, recorded and never compared,
  and the comment says which of the two it is so the term is not re-added.

  PROVEN BY MUTATION, both halves:
  - put the address term back → `TestReRegisterSurvivesANewContainerIP` fails;
  - make the hash compare never refuse → `TestReRegisterRefusesADifferentSecret`
    fails, at the same address and at a new one.

  The 156-rejection count is instance data and was not re-measured today.

## ✅ FIXED 2026-08-29 L108 — bskyd, reditd and linkd retry non-idempotent CREATE calls, so a lost response double-posts (2026-08-26)

`chanlib/retry.go:26-34` defines two policies and its own doc names the rule,
citing `D5`: `RetryRateLimitOnly` exists for *"a create call the platform gives
no idempotency key for … a 5xx or a dropped connection may have created the
thing already, so resending posts it twice."*

slakd applies it (`slakd/bot.go:530-533` lists `/chat.postMessage` and
`/files.completeUploadExternal`; `:543-546` selects per path). It is the **only**
`RetryRateLimitOnly` caller in the repo. Three adapters route creates through
`DoWithRetry` (= `RetryTransient`), which replays the body via `req.GetBody`:

- **bskyd** `client.go:371-384` `createRecord` — the shared envelope for
  `Send`/`Post`/`Like`/`Quote`/`Repost`/`SendFile` — POSTs
  `com.atproto.repo.createRecord` with **no client-supplied `rkey`**, via
  `xrpc` → `DoWithRetry` (`:562`, `:736`). Two identical feed posts.
- **reditd** `client.go:461-479` (`/api/comment`, `/api/submit`) and `:501-513`
  (`/api/submit`) via `do` → `doWithRetry` → `DoWithRetry` (`:179`).
- **linkd** `client.go:533-540` `postShare`/`ugcPost` (`POST /v2/ugcPosts`) and
  `postComment` via `do` → `DoWithRetry` (`:285`, and again at `:302` on the
  401-refresh path).

`chanlib/dedup.go`'s `deliverOnce` cannot help — it claims the key BEFORE the
platform call, and this retry happens INSIDE it.

- **Live blast radius: none today.** None of bskyd/reditd/linkd is deployed on
  krons, marinade or sloth. A shipped-code defect, not a live one.
- **Cause:** the policy was applied at the one site where the bug was found
  (slakd, `D5`) rather than at the class, and `DoWithRetry`'s default is the
  unsafe one, so every new adapter inherits it.
- **Fix:** a `nonIdempotent` path set per adapter (local), or invert the default
  so a create must opt into full retry (cross-cutting).
- **Unverified:** whether Bluesky/Reddit/LinkedIn dedup server-side. Confirmed
  that `createRecord` carries no client `rkey` (`bskyd/client.go:372-376` sends
  only `repo`/`collection`/`record`).

Fixed 2026-08-29 with the LOCAL option — a per-adapter non-idempotent set,
exactly slakd's shape, so the four adapters now read alike:

- `bskyd`: `bskyNonIdempotent` on the XRPC nsid — `com.atproto.repo.createRecord`
  only. `uploadBlob` is content-addressed by CID, so it keeps the full policy.
- `reditd`: `redditNonIdempotent` on the path — `/api/comment`, `/api/submit`.
- `linkd`: `linkedInNonIdempotent(path)` — `/v2/ugcPosts` and any
  `.../comments`. A like is addressed by actor+object, so re-sending is a no-op
  and it keeps the full policy.

Tests: each adapter answers 500 from a test server and asserts one request for
a create and three for a read or an id-addressed mutation. Removing an entry
from any of the three sets turns its test red.

**Inverting `DoWithRetry`'s default stays open** — the entry's cross-cutting
option. It is the stronger fix, because a new adapter still inherits the unsafe
default today, but it changes a shared contract and needs sign-off.

## ✅ FIXED 2026-08-29 L109 — the egress proxy follows plain-HTTP redirects and never re-checks the allowlist, so one allowlisted redirector reaches any host (2026-08-26)

`egress/proxy.go:241` runs `p.hostAllowed(host)` exactly once, on the first
request line, then `:264` calls `p.httpClient().Do(out)`. `httpClient()`
(`egress/proxy.go:133`) is `&http.Client{Timeout: 60s, Transport: p.transport}`
with **no `CheckRedirect`**, so Go's default follows up to 10 hops. Each hop
dials through `p.dialAllowed` (`egress/address.go:97`), which runs only the
ADDRESS-class check — loopback, link-local, IMDS, optional private. It never
calls `hostAllowed`. The name policy runs once; the redirect chain is free.

- **Reproduced independently against the binary built from HEAD**
  (`go build ./egress/cmd/egress`, `--allow localhost --allow-loopback
  --client 127.0.0.1`, a redirector on `:18081` returning
  `302 Location: http://127.0.0.1.nip.io:18082/exfil?secret=TOPSECRET`, a sink
  on `:18082`; `127.0.0.1.nip.io` resolves to loopback and is NOT allowlisted):

  ```
  control: curl -x proxy http://127.0.0.1.nip.io:18082/exfil  -> HTTP=403
  test:    curl -sL -x proxy http://localhost:18081/go
           EXFILTRATED
           HTTP=200 final=http://localhost:18081/go
  sink:    SINK-HIT host=127.0.0.1.nip.io:18082 path=/exfil?secret=TOPSECRET
  proxy:   {"msg":"deny http","host":"127.0.0.1.nip.io"}
           {"msg":"allow http","host":"localhost","status":200}
  ```

  `final=http://localhost:18081/go` proves the CLIENT never saw the 302 — the
  proxy followed it. The proxy log has no line for the host it actually reached,
  so the audit trail records only the allowed first hop.
- **HTTPS is not affected:** `handleConnect` (`egress/proxy.go:192`) splices raw
  TCP, so a TLS redirect is re-issued by the client as a fresh `CONNECT` and
  does hit `hostAllowed`.
- **Live blast radius:** egress is armed on all three instances. krons journal
  over 3 days shows constrained per-spawn proxies for `krons/content` (8 rules),
  `rhias/niki` (4), `krons/public/marble` (7), `rhias/content` (3), `mayai` (2),
  and sloth's `main/reading`, `main/content`, `main/code` (14 each). Every one is
  exploitable today. The spawn network is `--internal`, so plain-HTTP traffic has
  no route except this proxy — it goes THROUGH this path, not around it.
- **Cause:** a forward proxy must not be an HTTP client that resolves redirects
  on the caller's behalf; following one moves the destination decision out of
  the request line the enforcement point reads.
- **Fix (local):** `CheckRedirect: func(*http.Request, []*http.Request) error {
  return http.ErrUseLastResponse }` on `httpClient()`, returning the 3xx to the
  client so it re-requests and the new host goes through `hostAllowed`.
  Re-checking per hop would also work but keeps the wrong shape.
- No test in `egress/proxy_test.go`, `egress/test/e2e_test.go` or
  `container/egress_test.go` mentions redirects.

Fixed as the entry proposed: `httpClient` (`egress/proxy.go:133`) now carries
`CheckRedirect` returning `http.ErrUseLastResponse`, so the 3xx goes back to the
client and the client re-issues the hop through `hostAllowed`. Test
`TestRedirectIsNotFollowed` (`egress/proxy_test.go`) stands an allowlisted
redirector in front of a sink that is not on the list, and asserts the sink is
never reached, the body never arrives, and the re-issued hop answers 403. The
test fails with the `CheckRedirect` line removed.

## L110 — `network_allow` refuses `*` and then accepts `com`, which is the same allow-all spelled differently; live on sloth for 2.5 months (2026-08-26, proposal — needs sign-off)

`routd/network_rules_resource.go:97` refuses a bare `*` — *"opening all egress
is an operator decision, not an agent grant"* — then hands the value to
`match.ValidEntry` (`egress/match/match.go:94`), which accepts anything matching
`domainRegex`, **single labels included** (deliberately, so `localhost` is a
legal rule). At match time `match.Host` (`:64-68`) is
`host == entry || strings.HasSuffix(host, "."+entry)`, so the stored rule `com`
matches every `*.com` host. The repo's own test pins it as intended semantics:
`match_test.go` — `{"single label matches its suffix", []string{"com"},
"github.com", true}`.

- **Live on sloth, written by the agent itself:**

  ```
  main/trading | com  | folder:main | 2026-06-12T20:02:44Z
  main/trading | org  | folder:main | 2026-06-12T20:02:45Z
  main/trading | net  | folder:main | 2026-06-12T20:02:45Z
  main/trading | io   | folder:main | 2026-06-12T20:02:46Z
  main/trading | ai   | folder:main | 2026-06-12T20:02:46Z
  main/trading | co   | folder:main | 2026-06-12T20:02:47Z
  main/trading | news | folder:main | 2026-06-12T20:02:47Z
  main/trading | xyz  | folder:main | 2026-06-12T20:02:48Z
  ```

  `created_by='folder:main'` is the in-container agent principal
  (`routd/mcp.go:605`, `callerSub := turnPrincipal(t.folder)`); the operator's
  own rows on the same folder are stamped `cli`. Eight rows in four seconds is a
  loop, not a human.
- **The operator-visible signal lies.** sloth's journal over 3 days shows 20
  spawns starting with `"rules":28,"allow_all":false` for that folder. It reads
  "constrained"; it is not.
- **Cause:** `ValidEntry` is the grammar boundary and answers "can this rule ever
  match?" — never "does this rule match everything?". The bare-`*` check is a
  symptom patch on one spelling.
- **Proposed fix (cross-cutting → sign-off):** a public-suffix or label-count
  floor in `match.ValidEntry`, or a dedicated agent-write predicate, so every
  write path — CLI, agent tool, manifest apply — refuses a rule whose match set
  is a whole suffix the writer does not own. It changes the shared grammar and
  would reject existing rows. `localhost` must stay legal, which is what makes
  the naive "require a dot" rule wrong.

## ✅ FIXED 2026-08-29 L111 — the steer callback is deleted by folder without checking which run owns it, so a live run silently loses steering (2026-08-26)

The write side is guarded: `runed/manager.go:439` `SetSteer(folder, runID, …)`
returns early at `:442` unless `active.RunID == runID`, pinned by
`runed/queue_concurrency_test.go:304` `TestSetSteerIgnoresStaleRunID`. The
delete side is not:

- `runed/manager.go:431` — `endRun(folder, runID, …)` takes `runID` and then
  calls `delete(m.steerFns, folder)`, never using it.
- `runed/manager.go:471` — `Kill` calls `delete(m.steerFns, sp.Folder)`
  **unconditionally**, outside the `if live` block at `:462`.

Two reachable failures:

- **Back-to-back turns.** `Manager.spawn` writes `EndSpawn` at `:378` (the row
  goes terminal, so `ActiveSpawnForFolder` reports the folder FREE) and only
  calls `endRun` at `:388`. A concurrent `POST /v1/runs` for the same folder can
  admit and reach `dockerRuntime.Run`, whose FIRST statement is
  `spec.RegisterSteer` (`runed/docker.go:149-152`). Run B's `SetSteer` lands in
  that window and passes its own guard; run A's `endRun` then deletes it. Run B
  is live with no steer callback for its whole life, so every message takes
  `admit`'s `steer != nil` false branch (`manager.go:234`) → `Busy` →
  `routd/dispatch.go:424` leaves the cursor un-advanced and re-feeds → `Busy`
  again, until B exits. Near the 20-minute `RunTTL` that is a 20-minute delay
  for follow-ups that should have been written into the live container in under
  a second.
- **A stale `run_id`.** `DELETE /v1/runs/{run_id}` (`runed/server.go:246`) on an
  already-terminal run kills nothing, returns 200, and still wipes that folder's
  steer callback — which by then belongs to a different, live run.
  `cmd/arizuko/archive.go:232` `ReleaseHold` is exactly this shape: a hold whose
  `RunTTL` expired is terminal, and releasing it disarms whatever agent run has
  since claimed the folder.
- **Cause:** `steerFns` is keyed by folder but its lifetime belongs to a run.
- **Fix (local), symmetric with `SetSteer`:** store `{runID, fn}` (or add
  `clearSteer(folder, runID)`) and delete only on a matching `runID`.
- **Confidence:** medium-high on the mechanism, medium on how often the first
  shape fires — it needs a message inside a window of a few DB writes. Read end
  to end; not reproduced live (firing a `DELETE` at a production runed was out
  of scope).

Fixed 2026-08-29 by removing the question instead of answering it three times.
The entry's own cause line is the fix: `steerFns` is keyed by folder but its
lifetime belongs to a run — so it is now keyed by `run_id`.

That deletes the guard at every site rather than adding one:

- `SetSteer(runID, fn)` drops both the `folder` parameter and the
  `GetActiveSpawn` read + two-condition guard. Wiring a callback for a finished
  run stores an entry nothing can reach, because `admit` looks up
  `active.RunID` — the run_id it just read from the DB.
- `endRun` and `Kill` delete by the run they were given. Neither can touch a
  successor's entry.
- Net: one `if` (two conditions) and one DB round-trip per steer wire REMOVED,
  and the correctness stops depending on three call sites remembering.

The DB's active-spawn row is now the only liveness authority; the map is a plain
`run_id → closure` lookup and no longer a second, weaker record of which run is
live.

Tests (`runed/queue_concurrency_test.go`): `TestEndRunDoesNotDisarmTheSuccessor`
and `TestKillingATerminalRunDoesNotDisarmTheLiveOne` reproduce the entry's two
failures; `TestEndRunClearsItsOwnSteer` pins that the delete still works for its
owner, so the map cannot grow a closure per run.
`TestSetSteerIgnoresStaleRunID` is rewritten as
`TestStaleSteerRegistrationIsUnreachable`: the property it protected is
unchanged, only the mechanism moved, so it now asserts the property through
`admit`. All three fail against the folder-keyed shape.

## ✅ FIXED 2026-08-29 L112 — ant scrubs 2 of the 5 model credentials from the agent's shell (2026-08-26)

`ant/src/guards.ts:27` — `SECRET_ENV_VARS = ['ANTHROPIC_API_KEY',
'CLAUDE_CODE_OAUTH_TOKEN']`. The `bash-scrub` policy (`ant/src/policy.ts:85`)
prefixes every Bash command with an `unset` of exactly those two.

The canonical set is `store/secrets.go:115` `EnvProfileKeys`, which holds
**five**: those two plus `ANTHROPIC_OAUTH_TOKEN`, `OPENAI_API_KEY` and
`CODEX_API_KEY`. All five reach the agent — `container/runner.go:769`
`readSecrets()` iterates `EnvProfileKeys`, ships them over stdin, and
`ant/src/index.ts:395-397` folds them into `turn.env`, handed to the harness at
`ant/src/claude.ts:482`, `ant/src/codex.ts:205`, `ant/src/pi.ts:188` — i.e. into
the CLI subprocess whose children the Bash tool is. Three of the five are
readable with `echo $ANTHROPIC_OAUTH_TOKEN`, which is exactly what `bashScrub`
exists to prevent.

- **Live exposure: zero, measured.** The top-level `.env` and generated
  `env/runed.env` on krons, marinade and sloth each set only
  `CLAUDE_CODE_OAUTH_TOKEN` — and that one IS scrubbed. The `secrets` table
  holds one row fleet-wide (`marinade / folder / atlas / GITHUB_TOKEN`), not an
  env-profile key.
- **It arms the moment anyone sets `OPENAI_API_KEY`, `CODEX_API_KEY` or
  `ANTHROPIC_OAUTH_TOKEN`** — and `ANTHROPIC_OAUTH_TOKEN` was added to
  `EnvProfileKeys` FOR the pi backend, so a pi deployment arms it by design.
- **Cause:** two lists for one fact, in two languages.
- **Fix (local):** derive the TS list from the same five names — better, have
  the container hand ant the key set it injected, so there is one declaration
  and two readers.


**Closed 2026-08-29 on re-verification.** `ant/src/guards.ts` `SECRET_ENV_VARS`
now lists the three ANTHROPIC/CLAUDE keys and records why `OPENAI_API_KEY` and
`CODEX_API_KEY` are deliberately NOT unset — the `/oracle` skill shells out to
`codex`, which needs them, and `container/secret_scrub_test.go` pins that
partition. The exposure this entry described is tracked where it is still real:
`L131`'s pi half.

## ✅ FIXED 2026-08-29 L113 — `container` is red on `main`: an 11th product shipped without its spec count or its catalog pages (2026-08-26)

`go test ./container/... -count=1 -short` fails at HEAD, and has nothing to do
with the execution plane:

```
--- FAIL: TestProductCorpusPayloadKinds
    product_seed_test.go:296: corpus size moved: 11 products, spec 5/28 says 10
--- FAIL: TestProductCatalogMatchesCorpus
    product_seed_test.go:378: product "github" ships but has no catalog index.html
    product_seed_test.go:378: product "github" ships but has no catalog setup.html
```

A `github` product was added to the corpus without bumping the count in
`specs/5/28` or adding `template/web/pub/.../github/{index,setup}.html`, which
`template/web/CLAUDE.md` §"Adding a new product" requires.

- **Cost:** the package is red on `main`, so both tests are now noise and cannot
  catch the next drift — a broken window on the one check that keeps
  `products list` and the public site in agreement.
- **Fix (local):** either ship the two catalog pages and bump the spec count, or
  drop `github` from the corpus. Two failing assertions, one decision.


**Closed 2026-08-29 on re-verification.** `go test ./container/ -run
'TestProductCorpusPayloadKinds|TestProductCatalogMatchesCorpus'` is green;
commit `1a6e480d` shipped the github catalog pages.

## ✅ FIXED 2026-09-20 L114 — WebDAV `MOVE`/`COPY` authorize the source path and never the `Destination` header, so a folder-scoped user writes into any other tenant (2026-08-26; cross-tenant write)

`/dav/` is a `user` route with `StripPrefix` (`compose/compose.go:403`).
`davRoute` (`proxyd/main.go:667`) computes `rest` from `r.URL.Path` at `:698`
and applies BOTH guards to that one string — `auth.MatchSlot(gs, rest)` at
`:702` and `davAllow(r.Method, rest)` at `:709`. `davAllow`'s signature is
`func davAllow(method, rest string) bool`: **no third argument, no reader of
`r.Header`.** `stripClientHeaders` (`proxyd/main.go:90-97`) deletes eight `X-*`
headers; `Destination` is not among them, and `buildRouteProxy` leaves headers
alone. The backend is `dufs --allow-all` over a read-write bind of
`<data>/groups` — it enforces nothing.

WebDAV has two write targets. Only one is modelled.

- **Failure:** a caller holding a grant on `coach` sends
  `MOVE /dav/coach/notes.md` with `Destination: /main/trading/CLAUDE.md`.
  `rest` is `coach/notes.md`, so `MatchSlot` passes and `davAllow` passes.
  proxyd forwards; dufs writes into another tenant's folder — which is
  bind-mounted into that tenant's agent container. The same shape with
  `Destination: /coach/.env` overwrites a file the write block exists to
  protect.
- **Executed against the exact image the fleet runs** (`arizuko-davd:latest`,
  throwaway container, scratch tree; live data untouched):

  ```
  MOVE /tenantA/notes.md   Destination: /tenantB/logs/pwned.md      -> 204, file moved
  COPY /tenantA/a.md       Destination: http://…/tenantB/logs/…     -> 204, file copied
  MOVE /proj/harmless.txt  Destination: /proj/.env                  -> 204, .env overwritten
  ```

  `docker inspect arizuko_davd_krons` confirms `["--port","8080","--allow-all","/data"]`
  over an `rw` bind of `/srv/data/arizuko_krons/groups`, and `OPTIONS /` advertises
  `COPY,MOVE`.
- **Live blast radius — exploitable on sloth today.** `/dav/` is routed there
  (`proxyd_routes`: `/dav/|http://davd:8080|user|1`). `google:114015391913260915382`
  holds `admin` on scope `coach` and has **no** `acl_membership` row, so it is a
  real non-operator. Reachable targets: `main`, `main/code`, `main/content`,
  `main/main`, `main/reading`, `main/trading`. krons and marinade currently have
  only operator principals, so there it is latent.
- **Cause:** the guard authorizes the REQUEST LINE for a protocol whose write
  target is a HEADER. Not a missing entry in a blocklist — one of WebDAV's two
  write targets was never modelled.
- **Proposed fix (contract change → sign-off):** `davRoute` parses and
  prefix-strips `Destination`, rejects one outside `/dav/`, and re-runs
  `MatchSlot` + `davAllow` against it; `davAllow` takes the resolved
  destination.
- **Not executed:** a `MOVE` through the live proxyd (needs an OAuth session and
  would write live data). Disproof would be proxyd or nginx stripping
  `Destination` on `/dav/*` — neither does.
- **Fix:** `davRoute` resolves the `Destination` header for `COPY` and `MOVE`
  (`davDestMethods`; measured against the shipped image, those are the only two
  verbs davd reads it for, and the match is case-sensitive there), applies the
  SAME two guards to the result that the request line already gets —
  `auth.MatchSlot` then `davAllow` — and rewrites the header to the path it
  authorized before forwarding. The rewrite is what makes the guard answer:
  davd resolves the header proxyd wrote, never the caller's spelling, so a
  normalization that diverged from the backend cannot become a bypass.
  `davDest` mirrors dufs 0.46 step for step — hyper's URI parse, which drops
  scheme and authority and reads `//main/x.md` as a PATH where `net/url` reads
  an authority and would authorize a folder the write never touches; ONE
  percent-decode BEFORE the split, so `%2F` is a separator and `%2e%2e` a
  parent hop; then Rust `Path::components` semantics, where repeated separators
  collapse, `.` drops everywhere but the front and `..` refuses. A destination
  outside `/dav/`, unparseable, or carrying a `..` is 400; one that resolves
  inside `/dav/` but outside the caller's slot, or onto `.env` / `*.pem` /
  `.git` / `logs/`, is 403. `davDestHeader` strips `/dav` the way
  `buildRouteProxy` strips it off the request line, so header and request line
  name one tree — without that a conforming client's `MOVE` wrote to
  `<data>/groups/dav/`, which the same throwaway container reproduces.
- **Evidence:** a 37-case differential against `arizuko-davd:latest` (dufs
  0.46.0, throwaway container, scratch tree) agrees with `davDest` on every raw
  header, and every header `davDestHeader` renders lands the file on exactly
  the authorized path. `TestProxydDavDestinationOutOfSlotRefused` drives the
  path-only, absolute-URI, foreign-authority, percent-encoded, `//`,
  dot-segment and encoded-dot-segment forms for MOVE and COPY;
  `TestProxydDavDestinationInSlotRewritten` proves an in-slot move still
  succeeds and reaches the backend rewritten; `TestDavDest` and
  `TestDavDestHeaderRoundTrip` hold the resolution table and the round trip.
  With the guard removed every refusal case answers 200. SHA (pending).

## ✅ FIXED 2026-08-29 L115 — `davAllow`'s `logs/` read-only rule only covers top-level groups (2026-08-26)

`proxyd/main.go:734-737`:

```go
parts := strings.Split(rest, "/")
if len(parts) >= 2 && parts[1] == "logs" {
    return false
}
```

`davAllow("DELETE", "main/trading/logs/agent.log")` → `parts[1]` is `"trading"`
→ **true**. `MatchSlot(["main"], …)` passes because it walks every prefix
(`auth/acl.go:46-58`). So a user scoped to `main` deletes or overwrites
`main/trading/logs/*`, while the byte-identical action on `main/logs/*` is a
403.

The sensitive-path block eight lines below tests **every** segment
(`for _, p := range parts`) — the author knew the difference.

- **Live directories exist** — sloth: `main/code/logs`, `main/main/logs`,
  `main/reading/logs`, `main/content/logs`, `main/trading/logs` all writable;
  only `main/logs` and `coach/logs` protected. krons is the same shape
  (`rhias/nemo/logs`, `rhias/niki/logs`, `rhias/content/logs`,
  `krons/content/logs`, every `eval/<child>/logs`).
- **Every case in `TestDavAllow` (`proxyd/main_test.go:1339-1370`) uses a
  single-segment group `g`.** The multi-segment case is absent from all 15 rows.
- **Cause:** `parts[1]` assumes the group boundary is at index 1 — the exact
  assumption `proxyd/main.go:696-701` already records as wrong and fixed for
  `MatchSlot` (*"Cutting at the first segment 403'd a multi-segment folder on
  its own files"*). The fix landed next door and not here.
- **Fix (local):** test for a `logs` segment at the group boundary — any
  segment, matching how `.env`/`.pem`/`.git` are already tested.

Fixed as proposed: `davAllow` (`proxyd/main.go`) now scans every segment after
the first for `logs`, the same shape the sensitive-path block beside it already
used. The first segment is skipped so a top-level group named `logs` stays a
group. `TestDavAllow` gains the multi-segment rows the old table missed
(`main/trading/logs/agent.log`, `a/b/c/logs/x`) plus the read and the
top-level-group rows; they fail against the old `parts[1]` test.

## L116 — `/panel/<folder>`'s live SSE stream can never connect: it asks for a route token it does not have (2026-08-26, open)

`webd/pages.go:83` emits `sse-connect="/chat/stream?group=%s&topic="` and
`:104` rebuilds it on topic switch. Neither carries `token`. The endpoint
`handleRouteTokenStream` (`webd/route_token.go:423`) requires `topic`
(400 when empty, `:424-428`) and then resolves `token` (`:433`);
`store.LookupRouteToken` short-circuits on the empty string
(`store/route_tokens.go:197-199`) → 404. The `group=` parameter is deliberately
ignored — `webd/route_token.go:412-413` says so.

- **Verified live on krons**, byte-for-byte the two URLs `pages.go` emits:

  ```
  GET /chat/stream?group=krons&topic=     -> 400
  GET /chat/stream?group=krons&topic=t1   -> 404
  ```

- **Failure:** an operator opens `/panel/krons`, sends a message, the agent
  replies and is delivered — and the panel shows nothing. The thread only
  refills on the `hx-get /x/groups/<folder>/messages` that fires on load or
  topic switch, so the operator sees their own message and then silence until
  they reload.
- **Blast radius: every instance.** The URL is a compiled-in format string with
  no token slot, so no deployment can differ.
- **Cause:** `specs/6/22` step 1 moved token resolution into
  `handleRouteTokenStream` and made the token mandatory. The `/chat/<token>/`
  widget was updated (`webd/route_token.go:667` passes `token=`); `/panel` was
  not, and kept the `group=` parameter the same commit turned into dead input.
- **Fix:** `/panel` has no route token, so pointing it at the anonymous token
  surface is the wrong shape. It needs its own folder-scoped stream, the way
  `/me` already has one (`webd/me.go:328` `handleMeSSE`) — a redesign, not a
  one-line edit.
- **Secondary, lower confidence, same page:** `sse-swap:message="beforeend"`
  (`pages.go:84`) splices the hub's payload into a div the server otherwise
  fills with rendered HTML (`partials.go:68`), and the hub only ever publishes
  JSON (`webd/hub.go:66-68`). A connected reply would land as raw JSON text.
  Not driven through htmx; flagged, not claimed.

## ✅ FIXED 2026-09-20 L117 — the published chat SDK cannot send: `POST /slink/<token>` answers 301, which downgrades the request to GET, and `/slink/` carries no CORS (2026-08-26)

`webd/assets/arizuko-client.js` uses the `/slink/` prefix at five sites
(`:109` send, `:127` status, `:147` snapshot, `:166` stream, `:222` config).
`webd/server.go:122-125` registers the four `/slink/` patterns on the BARE mux —
not through `tokenSurface` — so `chatCORS` (`webd/route_token.go:496-508`) never
wraps them, and `handleSlinkRedirect` (`webd/server.go:292-299`) answers **301**.
Per RFC 9110 and the Fetch spec a 301 on POST is re-issued as GET, so
`POST /chat/{token}` is never reached.

- **Verified live on krons:**

  ```
  POST /slink/BOGUS                       -> 301  Location: /chat/BOGUS
                                             (no Access-Control-* headers)
  POST /chat/BOGUS  (Origin: acme)        -> 404  Access-Control-Allow-Origin: *
                                                  Access-Control-Allow-Methods: GET, POST, OPTIONS
  ```

  Following the chain shows the method change: `POST /slink/…` → 301 →
  `GET /chat/…` → 307 → `GET /chat/…/` → 404. With `--post301`, which preserves
  POST, curl stops after one hop — confirming the extra hop is the downgrade,
  not the server.
- **The surface is advertised:** `handleChatTokenConfig` returns
  `"sdk": "/assets/arizuko-client.js"` (`webd/route_token.go:118`), and the live
  doc page `/pub/arizuko/howto/chat-sdk.html` ships a copy-paste cross-origin
  sample. Cross-origin the CORS failure fires before the downgrade matters.
- **Cause:** the `/slink/ → /chat/` cutover changed the URL, left the SDK on the
  old prefix, and chose a status that is not method-preserving.
- **Two local fixes:** point the SDK at the `endpoints` map `/config` already
  returns (fetched into `this.endpoints` at `arizuko-client.js:86`, then never
  used), and make the compatibility shim `308` and CORS-wrapped for POST.
- **Status:** fixed 2026-09-20.
- **Fix:** both local fixes, one concern each.

  The SDK builds all five request paths from one `SURFACE = '/chat/'` constant,
  so it reaches the mux directly and no hop sits on the critical path of a send.
  The `endpoints` map `/config` returns is NOT the source: it holds three of the
  five paths, and `connect()` must fetch `/config` before it can read the map at
  all, so a client that used it would still spell two paths itself. One constant
  is the single spelling; the map stays as the server's own advertisement.

  The shim answers **308**, which RFC 9110 §15.4.2 defines as method-preserving,
  and it is registered through `tokenSurface`, so it carries the same CORS
  headers and anon shield as its target. A browser can now both read and follow
  it.

  PROVEN BY MUTATION, three assertions in `webd/slink_shim_test.go`:
  - `308 → 301`: the status assertion fails AND the end-to-end send fails, because
    Go's client re-issues the POST as GET exactly as a browser does.
  - `tokenSurface → mux.HandleFunc`: the `Access-Control-Allow-Origin` assertion
    fails.
  - `SURFACE = '/slink/'`: the SDK-source assertion fails, and so does the
    copies-are-identical test.

  `ARCHITECTURE.md:154` and `SECURITY.md:96` now say 308.
- **Left open, filed as `L322`:** `/assets/` is absent from `coreProxydRoutes`
  (`compose/compose.go:371-407`) and from all three live `proxyd_routes` tables,
  so webd's `GET /assets/{path...}` is unreachable through proxyd everywhere. On
  krons the request survives only by falling through to `vhostRedirect` →
  `/pub/krons/assets/arizuko-client.js`, which happens to hold a matching copy.
  An accident of one world's pub slot, not a route.

## L118 — `arizuko apply` destroys every route token and the whole task-run history through FK cascades, on an UNCHANGED round-trip (2026-08-26, proposal — needs sign-off; data loss)

`resreg.Apply` rebuilds each mentioned resource: `DeleteScope`
(`resreg/engine.go:316`, `DELETE FROM groups WHERE folder=?`) or `DeleteAll`
(`:333`, `DELETE FROM scheduled_tasks`), then `InsertAll`. The handle is
`store.OpenRoutd` → `openOwned` (`store/store.go:153`), whose DSN carries
`_pragma=foreign_keys(on)`. The live schema cascades:

```
route_tokens.owner_folder TEXT REFERENCES groups(folder)      ON DELETE CASCADE
web_routes.folder         TEXT REFERENCES groups(folder)      ON DELETE CASCADE
task_run_logs.task_id     TEXT REFERENCES scheduled_tasks(id) ON DELETE CASCADE
```

So the groups rebuild wipes `route_tokens` — a table declared
`SkipApplyRebuild: true` (`resreg/resources/route_tokens.go:134`) and therefore
never re-inserted — and the scheduled_tasks rebuild wipes `task_run_logs`, which
is not a resource at all. This contradicts Apply's own contract at
`resreg/engine.go:844-847`: *"SkipApplyRebuild resources … are never
wiped/rebuilt"* and *"a resource the manifest omits entirely is not touched"*.

- **Reproduced on a `.backup` copy of krons' live routd.db** (the live file was
  never written):

  ```
  PRAGMA foreign_keys=ON;
  tokens_before  = 37      task_run_logs_before = 1244
  DELETE FROM groups WHERE folder='krons';   -> tokens_after   = 35   (2 destroyed)
  DELETE FROM scheduled_tasks;               -> runlogs_after  = 10   (1234 destroyed)
  ```

  The 10 survivors are pre-existing orphans with no matching task — the `F38`
  class. An independent full harness (fresh `routd.Create`, then the exact
  `cmdExport` → `cmdApply` sequence) reached the same result on an UNCHANGED
  manifest: `route_tokens 1 → 0`, `task_run_logs 1 → 0`; and a groups-only
  manifest additionally destroyed the folder's `web_routes`.
- **Reachable from three documented surfaces:** `arizuko apply <manifest>` (the
  5/8 operator workflow), `arizuko packages install/upgrade/remove` (same
  `applyDocs`, `cmd/arizuko/packages.go:497-526` — so any package declaring a
  `scheduled_tasks` row, the capability `L44` exists to provide, wipes the
  instance's whole run history on install), and `archive apply` onto a live
  instance.
- **Live blast radius: zero so far, and one command away.** No `config.apply`
  audit row exists on any of the three instances. krons currently holds 37 route
  tokens and 1244 run logs that a single `arizuko apply` would destroy while
  reporting success.
- **Cause:** the rebuild pattern assumes a resource's DELETE is local to its
  table; the schema's cascades make it non-local.
- **Proposed fix (redesign → sign-off):** diff-based apply, or an FK-aware
  rebuild, or dropping the cascades in favour of the explicit `DeleteGroup`
  cascade.
- **Side observation:** the same FK defeats `L71`'s recorded decision that
  "route_tokens is deliberately NOT cascaded" — `store.DeleteGroup`
  (`store/groups.go:117`) deletes the groups row on an FK-on handle, so
  self-owned tokens cascade anyway.

## ✅ FIXED 2026-08-29 L119 — three of the four group-creation surfaces never bind `folder:<f> → role:member`, so a world born there holds zero messaging verbs (2026-08-26)

The binding exists in exactly one place: `routd/db.go:124` `PutGroup` →
`routd/seed_grants.go:16` `assignDefaultRole` → `store/membership.go:113`. A
repo-wide grep for writers of `role:member` returns only that path and
`cmd/arizuko/migrate_split.go:383`. The other three creation surfaces bypass it:

- **CLI** `arizuko group <inst> add` — `cmd/arizuko/main.go:457` calls
  `store.PutGroupRow` (group row only); no membership write anywhere in the
  `case "add"` block.
- **dashd** group create — `dashd/groups_admin.go:125-171` does a raw
  `INSERT INTO groups` plus an `acl` admin row for the CREATING OPERATOR's sub,
  in one tx. No `acl_membership` row for the folder principal.
- **onbod** web onboarding — `onbod/main.go:966-981` `createWorldTx` writes
  `user_profiles` + `groups` + the creator's `admin` acl row. No membership row.

`auth.Authorize` (`auth/authorize.go:42,95`) resolves a folder's rights by
expanding `folder:<f>` through `acl_membership`. A folder with no `role:member`
edge matches zero allow rows, so `routd/mcp.go:571` advertises zero messaging
tools and `routd/dispatch.go:786` denies `mcp:reply`/`send`/`send_file`. The
agent still answers in prose via `submit_turn` — which is exactly the symptom
`routd/migrations/0030-backfill-role-member.sql` describes, and its comment
*"New groups are unaffected: PutGroup → assignDefaultRole"* is true only for the
routd path.

- **Live: zero damaged rows.** On all three instances,
  `SELECT folder FROM groups WHERE 'folder:'||folder NOT IN (SELECT child FROM
  acl_membership WHERE parent='role:member')` returns nothing — the three broken
  surfaces simply have not been used since 0030 (onbod's is additionally blocked
  by `L99`).
- **The trap fires on** the next dashd group create, the next CLI `group add`,
  and on the first web onboarding the moment `L99` is fixed — that world comes
  up mute.
- **Cause:** the invariant "a group row implies the member edge" lives in one of
  four writers instead of in the shared group insert.
- **Fix:** move the membership write into the shared store-level insert (one
  renderer), or add `putMembershipTx` to each creating tx — dashd and onbod
  already own one; `PutGroupRow` would need one.
- **Related class:** the derived-vs-stored migration trap that cost the fleet
  9 hours (0023 → 0030).

Fixed 2026-08-29 with the one-renderer half of the proposal: `store` now owns
both the name and the write — `store.RoleMember` and `store.SeedMemberRoleTx`,
the single writer of the create-time edge, on a caller-owned transaction.

- `store.PutGroupRow` opens a tx and writes the row AND the edge together, so
  the CLI's `group add` carries the invariant.
- `dashd/groups_admin.go` and `onbod/main.go` `createWorldTx` each call
  `SeedMemberRoleTx` inside the transaction they already held, beside their
  admin-acl row.
- `routd.RoleMember` is now `= store.RoleMember`, so the string exists once.

Tests: `store/member_seed_test.go` pins that `PutGroupRow` writes exactly one
edge and that a re-put neither fails nor duplicates, and that
`SeedMemberRoleTx` is idempotent on a caller tx. Emptying the helper turns both
red.

## L120 — `/openapi.json` fabricates response schemas from a verb convention, so at least 11 mounted REST operations are documented wrongly (2026-08-26, open)

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

## ✅ FIXED 2026-08-25 L95 — a social mutation verb left no trace anywhere (2026-08-25)

`regSocial` (`ipc/ipc.go:1368`) writes a `messages` row only when the verb sets
`record`, and the field is deliberately nil for the mutation verbs — the
comment at `:1362` is right that `like`/`delete`/`edit` mutate existing content
rather than authoring new content, so a 👍 must not appear in the transcript as
something the agent said.

But those verbs did not emit an audit row either. `emitSys` had 12 call sites
(`ipc/ipc.go:1757`…), every one a management tool; no social verb called it. So
`like`, `dislike`, `delete`, `edit` and `forward` produced no `messages` row and
no `audit_log` row: the agent could delete or edit a message and nothing
anywhere recorded that it happened. Confirmed on krons — `audit_log` held zero
rows for any of them.

Found by a verification pass on reaction delivery, not by an incident.

Fixed at the one `regSocial` site so every social verb is covered by a single
call rather than a per-verb second path. `auditParams` caps each value at 200
chars so a long `content` cannot bloat every row. Tests:
`ipc/social_audit_test.go` pins the ok and error outcomes and the cap.

## L96 — an inbound reaction reaches the DB but never reliably reaches the agent (2026-08-25, proposal)

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

## ✅ FIXED 2026-08-25 L94 — `Like` and `Post` skip the capability guard, so an unsupported verb 404s instead of returning Unsupported (2026-08-25)

Every verb in `chanreg/httpchan.go` short-circuits on `HasCap` before it
touches the network — `delete` (`:382`), `pin` (`:390`), `edit` (`:446`),
`send_voice` (`:210`), eleven in all. Two do not: `Post` (`:362`) and
`Like` (`:376`) post to the adapter unconditionally.

`chanreg/verb_test.go` exists to pin exactly this rule, and its comment names
the consequence: "a regression (dropping the HasCap guard) would surface as a
confusing adapter-side 404 instead of a clean 'platform doesn't support this'
hint to the agent." Its `checks` map covers delete/dislike/edit/pin/unpin plus
forward/quote — and omits the two verbs that are actually unguarded.

Today every registered adapter implements both, so nothing fails. It bites the
first adapter that does not: `specs/6/27`'s gitd advertises `send_text` alone,
so the agent's `like` on a `git:` JID reaches gitd, 404s, and the agent reads a
transport error where it should read "this platform does not do that".

Fix: add the two guards, and add `like` and `post` to the test's `checks` map —
the omission is why the gap survived. **Blocks nothing today; blocks gitd.**

Fixed: both guards added (`chanreg/httpchan.go`), and `like`/`post` added to
`verb_test.go`'s `checks` map. Three existing tests declared no capabilities and
so stopped reaching the adapter once the guards landed —
`TestHTTPChannelPost501JSON`, `TestHTTPChannelPost501Plain` and the
like-on-missing-target case now advertise the cap, which is what they were
always testing. `Post501Plain` had been passing for the wrong reason: the guard
returns `Unsupported` too, masking the 501 decode the test exists to check.

## ✅ FIXED 2026-08-29 L93 — add_acl validates an `allow` row and then writes the requested `deny`, bypassing Delegate's deny ban (2026-08-25)

`auth.Delegate` refuses a deny row outright (`auth/delegate.go:51`):

```go
if r.Effect == "deny" {
    return fmt.Errorf("cannot delegate a deny row (%s %s on %s)", ...)
}
```

But the gate that calls it builds the candidate row with the effect
**hardcoded** (`routd/acl_resource.go:219`):

```go
want := core.ACLRow{..., Effect: "allow"}
if err := auth.Delegate(...); err != nil { ... }
```

while the write takes the effect from the caller's arguments —
`argString(x.Args, "effect")` (`acl_resource.go:161`) straight into
`grantACLTx`, which INSERTs it (`:305`). So a non-root caller asking for a
`deny` row is validated as if it asked for an `allow` row, and the ban never
sees it.

Bounded, not harmless: the caller must already pass
`authorize(callerSub, target, "mcp:add_acl", nil)` on the target scope, so it
cannot write outside scopes it administers. Inside them it can write denies it
was never delegated, and `Authorize` is deny-wins — a deny is a
capability-removal primitive, so this is an un-delegated power to disable, and
it can name a principal other than itself.

Found by an oracle review of `specs/6/26`'s writer requirement, not by an
incident. No live instance is known to have exercised it.

**Fix is one line — pass the requested effect into `want` — but it is an auth
change and it BLOCKS `6/26` gap 2.2**, which needs this same writer to start
carrying `params` as well. Do both together, with a test that a non-root
caller's `effect=deny` is refused, and one that `params` reaches `Delegate`'s
subset check unshaved.

Fixed 2026-08-29, the effect half only: the Gate now builds `want` with the
caller's own `effect` (`routd/acl_resource.go`), defaulting to `allow` exactly
as `grantACLTx` does, so the candidate row IS the row the write produces.
`TestACLMCP_NonRootCannotWriteADenyRow` grants a folder `read` on `world/**`
WITH the grant option, shows the `allow` call still passes, and pins that the
same call with `effect=deny` is refused and writes nothing; it fails with the
hardcoded `"allow"` restored.

**The `params` half closed 2026-08-30.** `grantACLTx`/`revokeACLTx` take the
`core.ACLRow` and the Gate builds its candidate with the same
`aclArgsRow`+`aclDefaults` pair the write uses, so the row `Delegate` judges is
the row stored on BOTH axes. `TestACLMCP_DelegateJudgesTheShapedRow` pins the
new half: a caller holding only `read(repo=mine/*)` WITH the grant option may
write that same narrowed row and may not write the unnarrowed `read` —
`auth.paramsCover` reads an empty spec as no constraint, so shaving `params`
off the candidate is the same class of bypass this entry records for `effect`.
Both directions fail with the shave restored.

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

## ✅ FIXED 2026-08-24 L91 — dashd's env page omits ANTHROPIC_OAUTH_TOKEN, and its own test says so (2026-08-24)

`TestMeEnv_HTMLPage` (`dashd/me_env_test.go:420`) iterates `store.EnvProfileKeys`
and asserts every key renders on `GET /dash/me/env`. It fails:

```
me_env_test.go:420: html page omits ANTHROPIC_OAUTH_TOKEN
```

`9e55216e` ("ANTHROPIC_OAUTH_TOKEN forwards to the container (pi's credential
name)") added the key to `store.EnvProfileKeys` without the dashd page that
lists them. A user running the pi backend therefore has no operator surface to
set the one credential that backend needs — the same class as
`reference_runed_model_credentials`, where an omitted canonical key cost the
fleet-wide `/login` outage.

**Fixed 2026-08-24.** The page held a SECOND literal of the same facts
(`dashd/me_env.go` `envProfileKeyList`), so adding a key to the map could never
reach it. `envProfileKeyList` is now DERIVED from `store.EnvProfileKeys`, sorted
for a stable display order — one declaration, two readers, and a key added to the
map appears on the page by construction. `TestMeEnv_HTMLPage` is the guard and
passes.

## ✅ FIXED 2026-08-24 L90 — a stale test, not a fork bug: the assertion outlived the rule it asserted (2026-08-24)

`TestEnsureTopicWithFork_CopiedSessionKeepsForkCursor`
(`routd/session_fork_test.go:161`) fails on `main`:

```
observed cursor cleared even though the child inherited the parent transcript
```

**The premise above was wrong** — read the call path before believing it. Nothing
clears the cursor. `L87` (fixed 2026-08-23, `64aae191`) deliberately replaced
"a fork stamps observed_cursor=now" with "the child inherits the PARENT's
cursor", because stamping now discarded the channel's ambient backlog at the
moment a thread was created. The test asserted the pre-L87 rule
(`ObservedCursor != ""`) and its fixture gave the parent no cursor, so it
failed from `64aae191` onward while the code was correct.

**Fixed 2026-08-24** by updating the test, NOT the code — "fix" here would have
reintroduced `L87`. `TestEnsureTopicWithFork_CopiedSessionKeepsParentCursor`
now seeds a parent cursor and asserts the child inherits that exact value, and
a second case pins that a cursorless parent hands down nothing. A test that
outlives its rule reads as a regression in the code it guards.

The assertion had a TWIN in the integration suite —
`TestFeature_ForkContinuity/fork-with-parent-session-resumes-a-restamped-copy`
(`tests/feat_continuity_test.go`), red since `64aae191` for the same reason and
fixed the same way. One rule change, two suites: grep every assertion of a rule
before believing one file carries it.

## ✅ FIXED 2026-08-29 L89 — release announcements stopped 2026-07-15: nobody opted in, and the CHANGELOG is not in the image (2026-08-24)

The last release line to reach a real chat is `v0.51.0` on 2026-06-11. Thirteen
releases have shipped since. `/migrate` itself is healthy — every group but the
stragglers below is at v203, and each posts a migration report — but step (e),
the announce, has produced nothing for ten weeks. Two independent causes, either
one sufficient.

**1. No destination.** `v0.59.0` (2026-07-15, `b3bdccac`) made announcing
opt-in: step (e) sends only to routes whose target carries the `#announce`
fragment, and "a group with no `#announce` route announces nowhere". The change
shipped without tagging anything, and nothing has been tagged since:

```
krons     52 routes, 0 #announce
sloth     16 routes, 0 #announce (4 #observe)
marinade  21 routes, 0 #announce (6 #observe)
```

Opt-in with an empty opt-in set is off. The `#observe` counts show operators do
use fragments, so the mechanism works — the release just never asked for one.

**2. Nothing to announce.** Step (e) reads
`/opt/arizuko/CHANGELOG.md`. That path is right; the file is not there:

```
$ docker run --rm arizuko-ant:latest head -1 /opt/arizuko/CHANGELOG.md
head: cannot open '/opt/arizuko/CHANGELOG.md': No such file or directory
```

The agent's `/opt/arizuko` is `MaterializeAppSrc`'s copy of runed's own
`/opt/arizuko` (`container/runtime.go:58`), and the `Dockerfile` populated that
with `ant/skills`, `ant/CLAUDE.md`, `ant/output-styles` and
`template/services` — no `CHANGELOG.md`. Before the `M1` fix the agent got a
bind of `HOST_APP_DIR`, the repo root, which does carry it; closing M1's
dev-tree hole silently closed the announce path with it. Live agents said so
plainly, and it read as success: *"No CHANGELOG present, so announce is a no-op
(nothing to announce)"* — `krons eval/etk57vv-priv-grant-g`, 2026-08-22.

**3. And the destination it would build is not a JID.** Step (e) read the
route's `match` and took `room=<r>` as the address to `send` to. `room` is not
an address: `router.msgField` defines `chat_jid` as the whole JID and
`platform`+`room` as its two halves, so `room=group/5069669817` reaches no
adapter and `platform=discord room=<guild>/<chan>` loses the `discord:` half.
Only a match written `chat_jid=<jid>` ever produced a working destination —
one route on the fleet, on marinade. Cause 3 was invisible behind cause 2.

**4. And it recorded the non-delivery as delivered.** With no destination the
`for` loop had nothing to iterate, so control fell through to
`echo "$latest" > ~/.announced-version`. Every guard on the fleet reads
`v0.63.0` — the version at which the step last "succeeded" — and two of them
hold the malformed value `## v0.63.0 — 2026-07-31`. This is `A2` again in a new
place: `A2` moved the guard write after the sends, but an empty destination list
walks past the sends without failing. The same `test -n "$jids"` that surfaces
cause 3 closes this: zero destinations is now an error, so the guard stays put.

Fixed here (v204):

- `Dockerfile` copies `/src/CHANGELOG.md` to `/opt/arizuko/CHANGELOG.md`, next
  to the other four, because that directory is the agent's whole view of the
  release.
- `ant/skills/migrate/SKILL.md` builds the destination as `<platform>:<room>`,
  uses `chat_jid=` verbatim when present, and **fails** — leaving the guard
  unadvanced — when a tagged route names no platform. An announce route that
  resolves to nothing is now an error, not a quiet skip.
- One route per instance tagged `#announce` (below). `Announce` is read only by
  `router.Describe` for the `inspect_routing` annotation, so the fragment
  changes nothing about inbound matching.

Related, found in the same sweep: eleven groups are frozen below v203 —
`sloth main/1000` v126, `main/coach` v126, `coach/1000` v126, `main` v170;
`krons 1000` v170, `mayai/1000` v162, `krons/1000` v162, `share` v165,
`krons/groups/mayai` v0; `marinade share` v134, `atlas/research` v129. Every
`1000` folder is stale on both instances, which is a pattern, not eleven
accidents. Untriaged.


**Closed 2026-08-29 on re-verification.** The root `Dockerfile` copies
`CHANGELOG.md` to `/opt/arizuko`, and `ant/skills/migrate/SKILL.md` builds the
destination JIDs and fails loud on an empty destination list rather than
advancing the guard.

## ✅ FIXED 2026-08-29 L69 — every cost row on the fleet is attributed to nobody: 7188 rows, 0 user subs (2026-08-22)

`cost_log` already carries the three dimensions spend must be sliced by: `folder`
(the group), `model`, and `user_sub` (who triggered the turn). Two of the three
work. The third is empty in every row that has ever been written.

```
krons    2711 rows, 0 with a user_sub
sloth    2209 rows, 0
marinade 2268 rows, 0
```

The cause is one list. `routd/budget.go:12`:

```go
var authSubPrefixes = []string{"google:", "github:", "local:"}
```

`callerSubOfMsg` returns the trigger sender only when it carries one of those
prefixes, and `recordTurnResult` writes that return value into `cost_log.user_sub`.
No sender on any instance has ever carried one. The senders that actually spend
are platform ones — krons' busiest human is `telegram:user/1112184352` with 779
turns — plus `anon:<hex>` from the web chat. Every one of them writes `''`.

The list is not wrong for what it was written for. Its comment says platform
senders are "not bound to a `user_profiles` row, so they carry no per-user cap",
and that is true. But ONE function serves two concerns:

- **attribution** — record who caused this spend. Wants every human sender.
- **cap lookup** — does this sub have a configured daily limit. Needs a profile row.

Restricting attribution to satisfy the cap lookup is what emptied the column.
`SpendTodayUser` and `idx_cost_log_user_day` exist and answer nothing;
`arizuko budget show user <sub>` returns 0 for every sub on the fleet.

A second defect sits under the same code. `store.UserCap` returns
`sql.ErrNoRows` when the sub has no `user_profiles` row, so the moment
attribution starts working `budgetGate` logs a warning per turn for every
platform user. "No profile" means uncapped — it is the documented default, not
an error.

**Fix**: split the two concerns. Attribution writes the trigger sender verbatim
for every sender that is a person (excluding `system`, `timed`, `timed-isolated:`,
`delegate` and `operator`, which are the platform triggering itself); the cap
lookup keeps requiring a profile row and treats its absence as uncapped.

Not a redesign: no new contract, no changed control flow, no schema change. The
column, the index and both queries already exist for exactly this.

Ties: `specs/6/23` (a gateway makes the NUMBER trustworthy; this makes the row
attributable — orthogonal halves of the same question).


**Closed 2026-08-29 on re-verification.** `turnCallerSub`
(`routd/dispatch.go`) now passes every non-platform sender through verbatim,
and `store.UserCap` returns `(0, nil)` on `sql.ErrNoRows` instead of erroring.
Both sites carry the L69 reference.

## ✅ FIXED 2026-08-23 L83 — webd read failures render as success: the SSE says the round finished (2026-08-23)

`store.GetTurnResult` maps only `sql.ErrNoRows` to `"pending"`; a real error
returns the zero `TurnInfo`, whose `Status` is `""`. The SSE handler discarded the
error and tested `Status != "pending"`:

```go
if info, _ := s.stRoutd.GetTurnResult(folder, turnID); info.Status != "pending" {
    …writeSSE("event: round_done" …)   // "" != "pending" → the round "finished"
    return
}
```

So a transient sqlite `busy`/`locked` under WAL load ends the browser's round
with an empty status and closes the stream. The sibling JSON handlers
(`handleTurnSnapshot`, `handleTurnStatus`) return 500 on the identical query.

Four more reads on the same page discarded their error and rendered a blank page
at HTTP 200 — `webd/me.go` (thread x2, topic list) and `webd/partials.go`
(topics, thread) — while their JSON twins in `webd/api.go` return 500. The SSE
replay query (`TurnFrames`) discarded its error with no log at all, so a client
resuming from `Last-Event-Id` silently skipped frames.

**Fixed**: the SSE ends the round with `status:"error"` and an ERROR log (headers
are already sent, so the truth rides the status field the client already reads);
the replay failure logs; the five HTML reads return 500 like their JSON twins.

Covered by a test pinning the zero-value collision that made it possible — a
failed read is neither an error the caller sees nor `"pending"`. The handler's
own emission is NOT covered end to end; the SSE fixture needs an authorized turn
plus a hub topic, and that is worth adding.

Remaining item RESOLVED BY DELETION 2026-08-30 (`L139`): `collectRoundFrames`
swallowed its error and returned `nil` frames, so `get_round` answered with a
successful empty snapshot and `wait=true` blocked the caller for the full
5-minute cap. `get_round` and its helper no longer exist — the collapsed MCP
surface has no blocking round read, and the `?wait=` POST it replaced them with
already ends the round on error.

## ✅ FIXED 2026-08-23 L81 — an operator's edit to a package-owned proxyd route succeeds, then silently reverts (2026-08-23)

Two predicates answered one question and disagreed about the third owner value.

```go
store/proxyd_routes.go  (SyncProxydRoutes keep loop)  if owners[r.Path] == RouteOwnerOperator   // allowlist
proxyd/resource.go      (PATCH guard)                 if routes[idx].Owner == RouteOwnerArizuko // denylist
```

`package:<name>` (introduced by `L46`) is neither, so a PATCH to `/slakd/` or
`/teled/` returned 200 and the next `arizuko generate` overwrote it — with no
log, no 409, nothing. That is precisely the "config stops applying and looks
healthy" failure the guard was written to make loud (`L45`), reappearing through
the owner the guard never learned about.

**Fixed**: `store.RouteSurvivesGenerate(owner)` is the one predicate — exactly
`operator` survives — and both the keep loop and the PATCH guard call it. The 409
now names the actual owner instead of hardcoding "arizuko".

## ✅ FIXED 2026-08-23 L82 — a folder cannot manage its own `#observe` route (2026-08-23)

A route target legally carries a fragment (`main/trading#observe`), and the
containment predicates compared the RAW string:

```go
func routeTargetWithin(target, owner string) bool { return groupfolder.Contains(owner, target) }
```

`Contains("main/trading", "main/trading#observe")` is false, so `set_routes`,
`add_route` and `DELETE /v1/routes` 403'd on the caller's OWN row — while the
resource's own delete SQL (`target LIKE ?||'#%'`), `core.ParseRouteTarget` and the
tool description all treat that row as belonging to `main/trading`. A
list→edit→set round-trip therefore either refused the caller's existing observe
route or dropped it, and it could not be re-added: the channel flips from
"archived as observed context" to a route miss, and messages stop being stored.

Live rows in this shape: sloth 4, marinade 6, krons 0. Sub-folder targets
(`atlas/general#observe`) were unaffected, which is why it hid.

**Fixed**: both `routeTargetWithin` and `routeTargetOwned` compare
`core.ParseRouteTarget(target).Folder`. A target PREFIX
(`folder:`/`daemon:`/`builtin:`) is still not a folder — only the fragment comes
off — and the test pins that.

## ✅ FIXED 2026-08-23 L79 — `remove_acl` reported a grant revoked while the grant stayed live (2026-08-23)

The agent/REST revoke never checked what it removed:

```go
// routd/acl_resource.go, before
_, err := tx.ExecContext(ctx,
  `DELETE FROM acl WHERE principal=? AND action=? AND scope=? AND effect=? AND params='' AND predicate=''`, …)
return err                       // …and the handler returned apiv1.OK{OK:true} unconditionally
```

Two things compound. The DELETE pins `params` and `predicate` to `''`, so a row
an operator created WITH a narrowing — `dashd/grants_admin.go:160` offers exactly
that (`jid=telegram:*`), and package-installed grants carry a predicate — cannot
be matched. And with no rows-affected check the call answered `{"ok":true}`
regardless. `list` DOES return `params`/`predicate`, so a caller could see a row
it could not revoke and be told the revoke worked.

The store twin gets it right (`store/acl.go:84-90`, full PK), as do
`onbod/invites_resource.go:104` and `routd/membership_resource.go:79`, which both
check rows-affected.

**Fixed**: `revokeACLTx` returns the row count and the handler refuses `404` when
it is zero, naming why — a revoke is the one operation where "did nothing" must
never read as success. The refusal text points the operator at where a narrowed
grant can be revoked.

NOT changed, deliberately: this face still addresses only un-narrowed rows.
`grantACLTx` writes `params='', predicate=''` too, so grant and revoke stay
symmetric; widening both to carry a narrowing is an additive contract change,
recorded as `L80`, not smuggled into a fail-loud fix.

Live rows affected: zero — no `params`-bearing acl row exists on any of the three
instances. The defect is the shape, on an auth surface.

## L84 — dashd's "Delete group" is a fourth delete renderer L71 missed, and it strands tasks (2026-08-23, proposal)

`L71` unified three group-delete paths into `store.DeleteGroup`. It missed the
one an operator is most likely to use: the dashboard button
(`dashd/groups_admin.go:437-464`), which opens its own transaction and cascades a
DIFFERENT set. The two operator-reachable paths now strand disjoint things:

| cascade | `store.DeleteGroup` (CLI `group rm`) | dashd handler |
| --- | --- | --- |
| child groups | no | **yes** |
| acl | no | **yes** |
| routes | yes | yes |
| `chat_reply_state` | yes | no |
| `scheduled_tasks` | **yes** | **no** |
| `sessions`, `network_rules`, `acl_membership`, `installed_packages` | no | no |

So a dashboard delete leaves the group's `<folder>-mem-N` rows `active`, timed
fires them forever at a folder that no longer resolves, and onbod reads each
unroutable jid as a new account to onboard — the exact `L66`→`L67` chain, still
reachable. Tasks at risk per dashboard-delete: krons 61, sloth 37, marinade 36
active rows across 12/7/7 task-owning folders.

The handler's own comment is now false: "store.DeleteGroup can't stand in: it
opens its own tx and touches only groups" (`:428`) — untrue since `ae559c7a`
earlier the same day.

**Proposal (needs sign-off — this changes dashd's control flow and widens
store.DeleteGroup's contract):** dashd delegates to the unified delete for each
folder in the subtree, and `store.DeleteGroup` absorbs the subtree + acl cascade
dashd has, so ONE renderer owns the whole thing. The four tables neither path
touches (`sessions`, `network_rules`, `acl_membership`, `installed_packages`)
should be decided in the same pass rather than left to a fifth caller.

Note `L78` already fixed the shared half — all these cascades now use
`groupfolder.SQLSubtree` instead of `LIKE`.

## ✅ CLOSED 2026-09-18 L85 — the smoke is 6/9 after the SDK jump L73 exposed; the three failures are all child-spawn callbacks (2026-08-23)

`L73` fixed the ant image so it ships the SDK it installs. The practical effect
is that the fleet moved from `@anthropic-ai/claude-agent-sdk` **0.3.153 to
0.3.237** in one step — 84 patch versions — because the host copy had been
winning silently for an unknown length of time.

The last smoke on 0.3.153 was 9/9 (11:57). The first full smoke on 0.3.237 is
6/9:

```
child-delegate  FAIL  240s  1765 tok  no callback
priv-grant      FAIL  180s  6112 tok  GET probe: unsupported protocol scheme ""
webhook-in      FAIL  150s  1913 tok  no callback
(the other six pass, including two that DO receive sink callbacks)
```

What is ruled out: delegation itself works — both child groups were created and
both child turns reached `done`. The sink is reachable — `fresh-context` and
`self-skill` received callbacks in the same run. Grants are intact. The eval
folder's egress rule is `*`.

What is NOT ruled out, and is the obvious suspect: the SDK jump. `priv-grant`
returning the literal string `probe` where a URL belongs reads like changed agent
behaviour, not a Go defect.

**Re-measured 2026-09-18 on krons (v0.68.2 image `802cc669`): 8/9.** The SDK
premise is disproven. `child-delegate` and `webhook-in` both PASS — the callback
arrives — so two of the three were transient, not the SDK. `priv-grant` still
fails and its cause is now known and unrelated: the eval folder cannot delegate
`web:publish` because it does not hold it WITH GRANT OPTION. That is `L308`, a
fixture gap, and it is where the remaining work lives.

An earlier run in the same session scored 1/9 and was NOT this: the harness was
pointed at a docker gateway that a `systemctl restart` had re-allocated
(192.168.160.1 → 192.168.192.1). That is fixed — anteval now refuses a run whose
`--sink` it cannot reach.

**Next test, not yet run**: build the ant image with the SDK pinned to 0.3.153,
re-run the three cases. If they pass, the jump is the cause and the decision is
whether `@latest` stays in the Dockerfile — it is deliberate and documented
there, so that is the operator's call, not a fix to make silently.

Until then the fleet runs an SDK version no smoke has passed at 9/9.

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

## ✅ FIXED 2026-08-23 L87 — a forked topic inherits the parent's transcript but not its ambient backlog (2026-08-23)

`EnsureTopicLineage` stamped the child's `observed_cursor = now`:

```sql
INSERT OR IGNORE INTO sessions(..., forked_at, observed_cursor) VALUES(?,?,?,?, now, now)
```

The fork's other half copies the parent's Claude transcript (`L54`, 2026-08-20),
and the stamp is only correct while that transcript carries the rest. It does
not. A transcript holds the turns that RAN in the parent, and a busy channel's
traffic is `is_observed=1` rows that never ran one — on sloth's `#trading`, 20 of
37 messages in 24h. So a new Discord thread opened blind to everything said in
the channel before it existed.

Measured: thread `1540726266351722526` forked at 18:13:05 with
`observed_cursor = 18:13:05`, while the channel conversation it needed ran
15:58–18:13. Its parent (the root topic) sat at cursor 15:10:44.

**Fixed**: the child inherits the PARENT's cursor, falling back to empty when the
parent has no row — the normal cold path, where `ObservedSince` reads the whole
trailing window already capped by `observe_window_messages` / `_chars`.
Re-showing a message the parent saw is benign; the standing rule is "observed
messages are context, not requests", which the cursor's at-least-once advance
already depends on.

Test fails on the old behaviour (verified by mutation: child cursor came back as
`now`, not the parent's).

## ✅ FIXED 2026-08-23 L88 — `/resolve` recalls files, never the channel; the agent has no instruction to read it (2026-08-23)

Asked whether the agent researches and recalls before starting a topic. It runs
the triage — `resolve` fires 90 times across `main/trading`'s transcripts, so the
`[resolve]` nudge works. What it recalls is the problem.

`resolve` step 2 "Recall" reads `~/diary`, `~/facts`, `~/users` — files the agent
wrote. Nothing in it reads the CHAT. Measured tool use over the folder's entire
transcript history:

```
compact-memories 157 · resolve 90 · migrate 44 · market-context 35
inspect_messages  33 · fetch_history 12 · recall-memories 1
```

`fetch_history` — whose own description is "reconstruct context before replying,
especially on first contact or after a reset_session" — ran **12 times ever**.
`recall-memories` ran **once**.

Two causes, both prompt-level:

1. `resolve`'s recall step never names a channel-history tool, and step 1 tells a
   continuation to skip recall entirely.
2. `ant/CLAUDE.md` did name a tool for this — the WRONG one. It said "call
   `inspect_messages` for recent history", while `inspect_messages`'s own
   description says "Not for conversation context before replying
   (fetch_history)". The instruction and the tool contract disagreed.

**Fixed**: `resolve` step 2 now calls `fetch_history` on the chat FIRST, before
the file recall, with the reason stated — a thread forked off a busy channel
inherits the transcript of turns that RAN there, which is not what people
actually said. `ant/CLAUDE.md` now names `fetch_history` and says why
`inspect_messages` is not it.

Pairs with `L87` (the mechanical half: a fork now inherits the parent's observed
backlog instead of stamping `now`). `L87` gives the turn the ambient window;
this makes the agent go and read the conversation when it starts fresh.

Threading itself is NOT at fault and was checked: every bot reply on
`#trading` since 2026-08-01 carries its trigger's topic, and the only cross-topic
rows are the expected root→new-thread creations.

## L80 — the agent/REST acl face cannot express a narrowed grant (2026-08-23, proposal)

`grantACLTx` hardcodes `params='', predicate=''` on INSERT and `revokeACLTx`
matches the same, so the resreg acl resource can neither create nor address a
narrowed grant. Only `dashd`'s form and the CLI can, through the store writer.
`list` returns `params`/`predicate`, so the face shows what it cannot manage.

Since `L79` the mismatch is at least loud (a revoke that matches nothing 404s).

**Proposal (additive contract, needs sign-off):** `add_acl`/`remove_acl` and the
REST twins accept optional `params` and `predicate`, defaulting to `''` so
existing callers are unaffected, and both writers bind them. That makes the
resreg face the one renderer for the acl table instead of a lossy subset of it.

## ✅ FIXED 2026-08-23 L78 — group-delete cascades used SQL `LIKE`, so deleting one folder deleted another's rows (2026-08-23)

Five DELETE statements expressed folder containment as `col = ? OR col LIKE ?||'/%'`.
SQL `LIKE` is the wrong predicate twice over: it is case-INSENSITIVE for ASCII,
and `_` and `%` are wildcards. Measured on sqlite 3:

```
$ DELETE FROM routes WHERE target='a_b' OR target LIKE 'a_b'||'#%';
  → also deleted axb#observe        (`_` matched `x`)
$ DELETE FROM routes WHERE target='coach' OR target LIKE 'coach'||'#%';
  → also deleted Coach#observe      (LIKE folds case)
```

The case half is not hypothetical: sloth carried a `Coach`/`coach` pair for
3.5 months (`L66`). The codebase already knew — `routd/db.go:753` avoids LIKE in
`ListEngaged` for exactly this reason — but the five cascades did not.

`register_group` made the metachar half reachable: it was the ONLY create path
that skipped `IsValidFolder` (the CLI, dashd, invites and archive import all call
it), so an agent could create `acme/a_b`. Worse, `registerGroup` git-inits
`filepath.Join(groupsDir, folder)` and `Join` resolves `..` — a mutation check
confirms `world/../../etc` registered with `outcome=ok`, because the authz glob
`world/**` matches the raw string and containment never parses it.

**Fixed.** `groupfolder.SQLSubtree` / `SubtreeArgs` is the SQL twin of
`groupfolder.Contains` — an exact `substr` comparison with no metacharacters and
no case folding — and all five sites use it (`store/groups.go`,
`routd/routes_resource.go`, three in `dashd/groups_admin.go`). No destructive
`LIKE` remains: `grep "DELETE FROM" | grep LIKE` is empty. `register_group`
validates the folder. Both are covered by tests that fail on the un-fixed code,
the SQL one against a real sqlite.

Live blast radius today: zero rows — no metachar or case-variant folder exists on
any of the three instances.

## ✅ FIXED 2026-08-23 L74 — 14 recurring tasks are `active` with no next run, dead since June (2026-08-23)

```
$ sqlite3 routd.db "SELECT COUNT(*) FROM scheduled_tasks
                    WHERE status='active' AND COALESCE(cron,'')<>'' AND COALESCE(next_run,'')='';"
sloth 9 · marinade 5 · krons 0
```

The due claim is `WHERE status='active' AND datetime(next_run) <= datetime(?)`
(`store/tasks.go:138`). `datetime('')` is NULL, and `NULL <= …` is NULL, so such
a row is never claimed again while still reporting itself as scheduled. Nothing
recomputes `next_run`, so the state has no exit.

`main/content-mem-0..4` were created 2026-05-25 and have **zero** rows in
`task_run_logs` — that group's memory compaction has never run. sloth `main`'s
daily and weekly compactions last fired 2026-06-09/15; marinade's whole atlas
daily set last fired 2026-06-16.

Creation is not the cause: `store.SeedDefaultTasks` writes `next_run = now`
(`store/tasks.go:269`), and every stranded cron here parses fine. The rows date
to the June split cutover.

**Fixed, two halves.** Data: the 14 rows were backed up
(`scratchpad/stranded-tasks-backup.sql`) and given their real next occurrence;
0 were due on write. Producer: `timed/split.go` could still reach the same state
— `computeNextRun` returns `""` on a cron parse failure (`timed/main.go:43`) and
the old code wrote `('', 'active')` for it. A bad cron is a misconfiguration, not
a transient fault, so it now sets `status='error'` and logs ERROR instead of
leaving a row that lies about being scheduled.

## L75 — a dispatch failure loses the trigger silently: the retry never retries, and pending mentions get demoted to "observed" (2026-08-23, proposal)

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

## ✅ FIXED 2026-08-23 L76 — a message no one can receive is stamped `sent` (2026-08-23)

`routd/turns.go` `deliverRow` skips delivery for a JID with no `:` prefix and
sets `row.Status = MessageStatusSent`. No channel owns a prefixless JID, so the
row records a delivery that never happened.

```
sloth  mcp-1786912578405198742  chat=coach                  status=failed attempts=10
       "/migrate v199→v201 done — but a new guard is breaking skills. Needs operator."
krons  mcp-1787239846679434451  chat=krons/public/marble     same shape
later migrate reports to `coach` (08-20/21): status=sent, attempts=0 — delivered nowhere
```

The skip itself is deliberate and documented (it stops breaker spam). Stamping
`sent` is the defect: at least two operator alerts are provably lost, one of them
reporting a skills-breaking guard. The agent-side twin fails loud
(`no channel for jid coach`); only the `submit_turn` result path swallows.

**Fixed**: the row lands `failed`, not `sent`, and the skip logs WARN instead of
DEBUG. No new status was invented — `failed` IS the dead-letter state and
`delivery_attempts=0` already means "refused permanently", so the content becomes
visible on the dead_letters surface the operator already has. Nothing re-sends
it: `PendingOutbound` selects `status='pending'` only, so the breaker the skip
was written to protect is untouched.

The real fix is proactive-operator delivery via `role:operator` routing, which
`ARCHITECTURE.md` already lists as unshipped; that is a redesign and not
proposed here.

## ✅ FIXED 2026-09-20 L77 — shipped skills tell the agent to use subagent types the container does not have (2026-08-23)

`ant/skills/sonnet/SKILL.md:8` instructs `ALWAYS use subagent_type: "sonnet"`,
and `opus/`, `haiku/`, `fable/` say the same for their tiers. The agent container
has no such types:

```
sloth transcripts, 7d: 3x  Agent type 'sonnet' not found.
  Available agents: claude, Explore, general-purpose, Plan, statusline-setup
```

There is no `ant/agents/` directory, no `.claude/agents/` in group homes, and
`ant/src/claude.ts` passes no `agents` map to the SDK — only
`DEFAULT_MODEL='claude-opus-5'`. The skills were synced from the host toolkit
where those definitions exist; the definitions were not ported.

Every tier dispatch therefore runs at the parent's model and effort instead of
the cheaper one the skill promises — a cost leak, unmeasured.

**It was ten types, not four.** Measured 2026-09-20 over the shipped corpus:
`distill`, `fable`, `haiku`, `improve`, `learn`, `opus`, `readme`, `sonnet`,
`visual` are dispatched by eleven SKILL.md files, and `refine` is named by the
host set beside them.

- **Status:** fixed 2026-09-20.
- **Fix:** `ant/src/agents.ts` declares them and `claude.ts` passes the map as
  the SDK's `agents` option, so the effort pin the skills promise is real. Two
  shapes, and the split is the point:
  - a TIER agent (`fable`, `opus`, `sonnet`, `haiku`) is only a model and an
    effort, because effort is INHERITED when absent — which is exactly what
    `model: "haiku"` alone fails to pin;
  - a ROLE agent (`distill`, `improve`, `learn`, `readme`, `refine`, `visual`)
    names its skill through `AgentDefinition.skills` instead of restating it.
    Copying the skill body here would be a second declaration of the same text.

  `ant/src/agents.test.ts` reads the shipped corpus and fails when a skill
  dispatches to a type nothing declares, so the drift cannot return silently. It
  also asserts the corpus really dispatches, so the check cannot pass vacuously.
  VERIFIED by mutation: dropping `sonnet` turns it red. Full ant suite 223 pass,
  0 fail; `tsc --noEmit` clean.

## ✅ FIXED 2026-08-23 L71 — the operator's only group delete cascades nothing and writes no audit row (2026-08-23)

Three functions delete a group, and the one an operator can actually run is the
weakest:

| path | reachable by | cascades | audit |
| --- | --- | --- | --- |
| `store.DeleteGroupRow` | `arizuko group rm` | nothing | no |
| `store.DeleteGroup` | nothing (tests only) | nothing | yes |
| `routd.DB.DeleteGroup` | `registerGroup` rollback | routes, engagement, tasks | no |

So the L66 cascade shipped on 2026-08-22 lives on a path no operator can invoke,
and `arizuko group rm <folder>` leaves exactly the wreckage L66 was about:
routes aimed at a folder that no longer resolves, `chat_reply_state` pinned to
it, and `scheduled_tasks` timed keeps firing forever (which then reaches onbod
and mints a pairing token for an unroutable jid — `L67`).

There is no foreign key to fall back on. Neither `routes` nor `scheduled_tasks`
declares one, and the live DBs run with `foreign_keys=0`:

```
$ sudo sqlite3 .../krons/store/routd/routd.db "PRAGMA foreign_keys;"
0
```

Live damage, measured 2026-08-23:

```
instance   orphan routes   orphan engagement rows
krons      0               0
sloth      0               0
marinade   1               4
```

**Fixed**: one renderer. `store.DeleteGroup` now does the whole cascade in ONE
transaction (a partial cascade IS the orphan state) and stamps the audit row
through `auditIdentity()`, so a CLI delete is attributed to the operator instead
of to "system". `routd.DB.DeleteGroup` delegates to it; `DeleteGroupRow` is
deleted; `arizuko group rm` calls the one function.

`route_tokens` is deliberately NOT cascaded — a token for a deleted target is
refused at webd's ingress (`L5`).

`TestRegisterGroupRollsBackOnAddRouteError` forced its failure by dropping the
`routes` table, which then aborted the now-transactional rollback and left the
orphan it guards. It fails the INSERT with a trigger instead; the invariant is
unchanged.

## ✅ FIXED 2026-08-23 L73 — every agent image shipped the host's stale Anthropic SDK, not the `@latest` it installed (2026-08-23)

`ant/Dockerfile` installs the SDK deliberately and says why:

```dockerfile
# Anthropic SDK auto-updates to latest on every image build; overrides
# the version pinned in package-lock.json. ...
RUN npm install @anthropic-ai/claude-agent-sdk@latest
COPY . ./
```

`ant/` is its own build context (`ant/Makefile`: `docker build -t arizuko-ant .`),
so the repo-root `.dockerignore` never applied to it and there was no
`ant/.dockerignore`. `COPY . ./` therefore copied the host's 521MB
`ant/node_modules` straight over `/app/node_modules` — including the SDK the
line above had just fetched.

Measured before and after adding `ant/.dockerignore`:

```
host ant/node_modules  → 0.3.153
image, before          → 0.3.153   (the host's copy won)
image, after           → 0.3.237   (what @latest actually resolves to)
```

84 patch versions behind, on every agent turn the fleet has run. Two live turns
on krons completed on 0.3.237 after the rebuild, so the jump is not itself a
regression.

**Fixed**: `ant/.dockerignore` excluding `node_modules`, `.next`, `dist`. The
repo-root `.dockerignore` also gained `.claude/` and `.refs/` — 6.2GB of
subagent worktrees and reference checkouts that `COPY . .` was transferring on
every build (2.8GB context) and that busted the layer cache whenever a session
wrote a file.

Related, NOT fixed: 37 stale subagent worktrees under `.claude/worktrees` (6.2GB)
that nothing cleans up. Removal is recursive and some may hold uncommitted work —
operator's call.

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

## L70 — spend is recorded on three dimensions and readable on none of them (2026-08-22, proposal)

With `L69` fixed, `cost_log` carries folder, model and user_sub on every row.
Nothing can read them together.

`GET /v1/cost` answers exactly one question — one turn, summed across models:

```
GET /v1/cost?turn_id=<id> → {turn_id, folder, input_tokens, output_tokens, cost_cents}
```

The aggregate readers are `SpendTodayFolder` and `SpendTodayUser`, and their only
callers outside `budgetGate` are `arizuko budget show`, which reads the DB file
directly. So per-group and per-user spend has:

- no REST surface and no MCP tool — an agent cannot answer "what has this group
  spent", and neither can an external tool;
- no window but today — the UTC-day sum the gate needs, nothing else;
- no breakdown — never per model, never per user within a folder, never per day.

That fails `CLAUDE.md`'s standing rule directly: **every cold-tier management
entity is a resreg resource, no exceptions.** `cost_log` is one, and it is
reachable only through a CLI that opens the file.

**Proposal (needs sign-off — this is a new contract):** register a `spend`
resreg resource on routd, read-only, per `5/17` (REST + derived MCP, the injected
gate binding `folder` to the caller's folder exactly as `/v1/cost` already does):

```
GET /v1/spend?folder=&user_sub=&model=&since=&until=&group_by=folder|user_sub|model|day
    → [{folder, user_sub, model, day, input_tokens, output_tokens, cost_cents}]
```

One resource, one handler, the containment check the other cold-tier resources
already use. `idx_cost_log_folder_day` and `idx_cost_log_user_day` already index
both hot paths.

Explicitly NOT in this proposal: whether the numbers can be trusted. That is
`specs/6/23` — cost is still self-reported by the spender, and a gateway is what
makes it authoritative. The two are orthogonal: `L69` made the row attributable,
this makes it readable, `6/23` makes it true.

## ✅ FIXED 2026-08-22 L68 — anteval starves its own tail: one overrunning case times out every case after it (2026-08-22)

anteval runs cases sequentially into ONE folder, and arizuko correctly runs one
turn per folder at a time. `Drive` calls `ensurePristineRoot` before each case,
which sends `/new` and polls until it is consumed — a session reset, NOT a wait
for the folder to be idle. Nothing waits for the previous case's TURN to end.

So a case whose agent keeps working past its own `max_wall_ms` leaves that turn
running, and every later case queues behind it and times out having never
started. The signature is a run whose failures are a contiguous ALPHABETICAL
tail, each reporting `0 tokens`.

Measured on krons 2026-08-22 (nonce `etk5t9p`): `priv-grant` spent 9265 tokens —
by far the run's most — and anteval abandoned it at its 180s budget while the
agent kept going. The `eval` folder then had NO dispatch between 07:14:39 and
07:22:21, 7m42s. `rest-roundtrip` (90s), `self-skill` (120s) and `webhook-in`
(150s) all expired inside that window with 0 tokens each.

The inverse correlation across four runs is the tell: when the heavy case ends
early the tail passes (smoke3: 8/9, only `child-delegate` failing), and when it
runs long the same four fail (smoke2, smoke4: 5/9). The product is behaving
correctly in both.

This has now cost two investigations in one day — the same four FAILs were read
first as an ipc regression, then as an expired token (which was a REAL second
cause, fixed by the startup check in `a17bccec`).

Proposed fix (harness contract change, needs sign-off): before each case, wait
for the folder to hold no in-flight turn, with its own bounded cap and a loud
error when the cap is hit. A case that cannot get a quiet folder must say so
rather than report the next case's starvation as that case's failure.

- **Source:** `anteval/pkg/run/run.go` (`Drive`, `ensurePristineRoot`).

**FIXED 2026-08-22** (`10969e54`): `Drive` waits for a case's own turn to record
its cost before injecting the next case. A turn that never records one does not
hang the suite — the wait is bounded and the result carries a NOTE that the next
case may have started behind it.

**Verified 2026-08-22**: krons ran 9/9 in a single 12-minute smoke, the first
clean run. One case (`fresh-context`) spent the full 3-minute fallback and said
so — its injected message id never became a turn id, because routd batches a
folder's pending messages and the turn takes the LAST id in the batch. The
bounded wait plus the loud NOTE is exactly what that case is for; the real
signal (does this folder have a live turn) has no read surface, recorded in
`specs/6/23`.

## ✅ FIXED 2026-08-22 L66 — a removed group strands its 5 seeded tasks; they fire forever and route nowhere (2026-08-22)

`SeedDefaultTasks` writes 5 `<folder>-mem-N` rows per group, but
`scheduled_tasks.owner` carries NO foreign key to `groups.folder`, so removing
or renaming a group leaves its tasks behind. They stay `active`, `timed` keeps
firing them on cron, and every firing targets a folder that does not exist.

Two live instances carry the shape:

| instance | orphan tasks | real group |
|----------|--------------|------------|
| sloth    | `Coach-mem-0..4` (`owner='Coach'`) | `coach` exists; `Coach` does not |
| marinade | `libtest-mem-0..4` | no `libtest` group at all |

sloth's pair is a CASE mismatch: `groups` holds exactly one row, `coach`, and
zero `Coach` (checked with `=`, not `LIKE` — sqlite's `LIKE` is
case-insensitive and hides this). Nothing in the folder path lowercases, so the
two names were written by two different creation events.

`turn_results` dates it exactly: folder `Coach` holds ONE turn ever
(2026-05-05T21:29Z, its creation day) and folder `coach` holds 123, the most
recent 2026-08-21. The group ran once under the capitalised name, was recreated
lowercase, and the 5 tasks stayed pointed at the name nothing answers to.

The damage runs both ways:

- The orphans never execute — `Coach-mem-0` has fired daily since
  2026-05-05 and has never once run.
- The REAL group gets nothing: `coach` has no `-mem-` tasks, so its memories
  have never compacted.

Cause fix (needs sign-off, it is a schema change): a foreign key
`owner REFERENCES groups(folder) ON DELETE CASCADE`, matching the FK
`task_run_logs` already has on `scheduled_tasks`. That makes the orphan
impossible instead of detecting it later. Operator cleanup of the 10 existing
rows is separate and is the operator's call.

- **Source:** `store/tasks.go:261` (`SeedDefaultTasks`), `scheduled_tasks` schema.

**FIXED 2026-08-22** (`9b68f8e0`): `DeleteGroup` now cascades `scheduled_tasks`,
following the route/engagement pattern already there rather than adding a
foreign key — that cascade is deliberately non-fatal so registerGroup's rollback
can always remove the group row. Live rows cleared: sloth `Coach-mem-0..4`,
marinade `libtest-mem-0..4` (backed up first). The groups that had lost their
tasks were re-seeded with real next-cron times, not `now`, so nothing fired on
insert: sloth `coach`, marinade `atlas/strengths`, krons `krons/content` and
`rhias`.

## ✅ FIXED 2026-08-22 L67 — onbod mails a pairing link to an unroutable task JID, then logs success over the failure (2026-08-22)

Downstream of `L66`, and a fail-loud violation in its own right.

When `timed` fires an orphan task, its JID (`Coach`) matches no route. onbod's
`promptUnprompted` reads it as a brand-new account to onboard, mints a pairing
token and POSTs a pairing link at it. routd answers `502`. On sloth this has
run every night; the log pair is always:

```
WARN "send reply non-2xx" jid=Coach status=502
INFO "sent pairing link"  jid=Coach
```

`sendReply` returns nothing, so the caller cannot tell a `502` from a delivery
and logs the success line unconditionally — the failure is one line above the
claim that it worked. Worse, the row is CLAIMED before the send
(`prompted_at` is stamped first, deliberately, so two ticks cannot both send),
so a failed send is never retried: the pairing link is minted, lost, and the
row now reads as prompted.

That claim-first order is correct for the race it defends against; the defect
is that `sendReply` swallows its own outcome. It should return an error, and a
failed send should release the claim it took.

Neither the firing nor the 502 writes an `audit_log` row: sloth's audit surface
records exactly ONE non-ok outcome in 7 days (a discord like 502) while this
fails every night. The audit log reads clean through a repeating user-facing
failure.

- **Source:** `onbod/main.go:1421` (`sendReply`), `onbod/main.go:482`
  (`promptUnprompted`).

**FIXED 2026-08-22** (`54994318`): `sendReply` returns its outcome and the
pairing tick logs one error instead of a success line under the failure. The
claim still survives a failed send ON PURPOSE — releasing it would re-mint a
pairing token every tick for a permanently unroutable jid, and WISDOM retries
only transient errors. The two other call sites (refusal, queue notice) now
report a user who was refused or queued in silence.

## L62 — marinade's emaid IMAP login fails, unhealthy for 3 days (2026-08-21, open)

`arizuko_emaid_marinade` has been `Up (unhealthy)` since 2026-08-18. Every IMAP
IDLE cycle logs once a minute:

```
{"level":"ERROR","msg":"imap idle error","err":"login: imap: NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)"}
```

The daemon retries a NON-transient error forever — WISDOM says retry only
transient failures, and an authentication rejection is a configuration fault,
not a network blip. Two separate items:

- **Operator:** the stored IMAP password is wrong or revoked. Email ingest on
  marinade has been dead for three days and nothing surfaced it to a user.
- **Code:** `AUTHENTICATIONFAILED` should stop the loop and surface once, not
  re-log every 60s. Same cadence class as `L61`.

- **Source:** `emaid/`, marinade instance `.env`.

## L63 — krons whapd 401-loops, needs a phone re-pair (2026-08-21, open)

`arizuko_whapd_krons` is `Restarting` continuously. Known cause: the WhatsApp
link is gone and the pairing must be re-established from the user's phone
(pairing-code path, not QR — the recipe is in the whapd pairing memory). Not a
code defect; recorded so the fleet's one permanently-red container has an entry
rather than being treated as normal.

- **Source:** `whapd/`, operator action required.

## Error-hygiene sweep: swallowed / mis-levelled errors in the hot path (2026-07-11, 8 FIXED, rest OPEN)

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
  - `runed/manager.go` (steer-related discards near the admit/spawn path,
    line numbers drifted from the original 212-214 citation — not
    independently re-audited this pass).
  - `ipc/ipc.go:340-345` (conn-limit reject, no error frame) — already logs
    `slog.Warn`, so not silent; the gap is a missing JSON-RPC error frame to
    the caller, which needs the request id threaded through `writeJSON` —
    design work, not a log-add.
  - `routd/budget.go:32-43` spend caps fail open on a DB hiccup — already
    logs `slog.Warn` on `FolderCap`/`UserCap` read failure, so not silent
    either; the open question is whether failing OPEN (allow the turn) is the
    right policy when the cap check itself errors, vs. failing closed — a
    policy call, not a log-add.
  - `crackbox/pkg/host/host.go:130-132` — egress-proxy unregister failure on
    VM stop logs to stderr via `fmt.Fprintf`, not `slog` — inconsistent
    structured-logging but not silent. Low priority, left open.

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

## F5 — `store.UserScopes` decides folder visibility without action or deny (2026-08-05, PARTIALLY FIXED 2026-08-05)

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

## F77 — skill-guard's per-line scan is evadable by splitting a payload (2026-08-08, PARTIALLY FIXED 2026-08-08)

The demonstrated case is fixed: shell line-continuations are joined before
scanning, so `curl \` + newline + payload no longer scans `safe` while the
one-line form scans `dangerous`. Reproduced by codex and independently before
the fix; regression test in `ant/src/skillguard.test.ts`.

The CLASS remains open. A payload assembled from string concatenation, built in
a variable, or split across a here-doc still passes a regex table that reads one
line at a time. Closing it means AST or dataflow analysis, which
`specs/5/23` explicitly puts out of scope.

Not a containment boundary — Docker, the crackbox egress allowlist and the gated
MCP socket are, and `SECURITY.md` states this. Recorded so the residual is not
mistaken for coverage.

**Re-verified 2026-08-15 at `bcb5bb83`: accurate as written, no action.** The
demonstrated fix is present and is the one described — `logicalLines`
(`ant/src/skillguard.ts:46`) joins a line ending in `\` with the next before
scanning. The residual class is genuinely out of scope: closing it needs AST or
dataflow analysis, which `specs/5/23` excludes by decision, so there is no
contained fix to make and nothing to close. Left open deliberately as a
recorded residual, which is what this entry is for.

## ✅ FIXED 2026-08-22 L65 — `delegate_group` and `escalate_group` were dead in the split routd (2026-08-21)

Caught by anteval `child-delegate`, which is exactly what that suite is for.

Evidence from krons routd.db + journald for run `etk57vv` (2026-08-21T23:29Z,
eval turn `82bc4d61`):

- `register_group` succeeded at 23:29:52 — the child folder
  `eval/etk57vv-child-delegate` exists.
- the agent called `delegate_group` at 23:29:56 — the tool-log PRE line is
  there, and **no POST line**.
- `ipc/ipc.go:1957`'s `"delegating to child"` INFO never fired; no child
  message row, no child dispatch, no error surfaced at transcript-visible level.
- the agent then fell back to a plain `send` to
  `web:eval/etk57vv-child-delegate` (23:30:17). That writes a bot message, which
  **wakes no turn**, so the child never ran and the callback never came.

Reproduced 2026-08-21T23:52Z (`etk58og`): still FAIL, 150s cap, same shape.

**Triage candidates** (not yet distinguished): the granted-row authz on the tool
(scope glob vs the target folder), or the handler erroring before its first log
statement. The tool log's `args_summary` is empty for this call, which hides the
arguments and is its own small gap.

**A wrong theory, recorded so it is not retried:** I proposed that the child had
no `egress` capability because 5/33 says a path carries zero authorization. That
is wrong for egress — egress is NOT an `acl` row. It lives in `network_rules`,
and `routd/network.go` `ResolveAllowlist` walks the full ancestry at dispatch,
so a child DOES inherit its parent's allowlist. Amending the case to grant the
child egress did not fix it (the reproduction above ran with that prompt).

**ROOT CAUSE, found 2026-08-22 — two faults stacked, and my first two theories
were both wrong:**

1. **The tool was not in the agent's list at all.** `folder:eval` held
   `mcp:delegate_group` scoped `eval/**`, which covers the subtree BELOW eval,
   not `eval` itself. The agent's own transcript said it: *"gated socket shows
   no register_group/delegate_group/routing tools, just the 22-tool member
   floor."* Granting scope `eval` made the tool appear.
2. **Then it failed on its own wiring guard:** `delegate_group not configured`.
   Both `escalate_group` and `delegate_group` guarded on
   `gated.EnqueueMessageCheck != nil`. routd NEVER sets that field — its loop
   polls new messages by timestamp, which `routd/mcp.go:121` states outright for
   `InjectMessage`. So both tools were registered, advertised to the agent, and
   returned "not configured" on every call since the split.

**FIXED**: the guard now requires only `db.PutMessage`, and the enqueue is
called only when present — it is a latency optimisation, not a dependency.
Test `TestDelegateGroupWorksWithoutEnqueueHook` drives the real tool over the
socket with `GatedFns{}` (nil enqueue), exactly as routd constructs it.

- **Source:** `ipc/ipc.go` `delegate_group` + `escalate_group`;
  `routd/mcp.go:121`; `anteval/cases/anteval.toml` `child-delegate`.

## ✅ FIXED 2026-08-21 L64 — isolated crons re-enter the chat through the ambient window (2026-08-21)

`L54` closed the SESSION path: an isolated scheduled run gets its own one-off
container and never resumes or persists the chat's session, so a folder's crons
stop writing themselves into the conversation. Their rows still land
`routed_to = <folder>` with `is_observed = 1`, and `ObservedSince` filters by
folder with no sender exclusion — so the ambient window served every cron prompt
back to **every topic in that folder** on its next turn. The same hole, through a
second door.

Measured on sloth `main/trading` (2026-08-21): the `<observed>` block of a live
Discord-thread turn was dominated by 8-a-day `timed-isolated:` "Silent cron: …
stay silent" prompts. 113 observed entries across recent transcripts.

**FIXED**: `core.IsolatedSenderPrefix` / `core.IsIsolatedSender` declare the
marker once; `routd/prompt_db.go`'s ambient query excludes those senders from
both UNION arms, and `routd/dispatch.go`'s three literal prefix tests now read
the same declaration. Test `TestObservedSinceExcludesIsolatedCronRuns` asserts
the cron row qualifies on every OTHER predicate first, so it cannot pass
vacuously.

**NOT fixed, and not a bug**: the folder-scoped ambient window itself. `5/F`
records that decision — the cursor is per-`(folder, topic)` precisely so every
topic sees the folder's ambient traffic once. A forked topic also inherits its
parent's transcript by design (that is what `L54` restored). Two sloth session
files legitimately carry root turns plus one thread's turns.

- **Source:** `routd/prompt_db.go` `ObservedSince`; `core/types.go`; `specs/5/F`.

## ✅ FIXED 2026-08-21 L61 — stranded onboarding rows re-log at ERROR forever (2026-08-21)

**FIXED** by `warnedStranded` (a `sync.Map` in `onbod/main.go`): each
`(jid, status)` pair is reported once per process, and a row whose status
CHANGES warns again because that is a new fact. Confirmed on TWO instances, not
one — `telegram:user/1112184352` on sloth and `telegram:user/1612831875` on
marinade, 6 ERROR lines a minute each. The rows themselves still need an
operator to set or delete them; arizuko will not guess a status for a row of
unknown provenance. Test: `TestStrandedRowWarnsOncePerProcess`.

sloth carries an `onboarding` row for `telegram:user/1112184352` with
`status='pending'`, which is not in `knownStatuses`
(`awaiting_message`/`token_used`/`queued`/`approved`). `warnStrandedRows`
(`onbod/main.go:387`) scans the whole table on a timer and logs one ERROR per
unknown row per scan — 6 in a 60-second window after a restart.

The warning is deliberate and correct: its comment says arizuko will not pick a
status for a row of unknown provenance, because that is the operator's call,
and "silence is what let them sit unnoticed". The defect is the CADENCE, not
the decision. An operator-actionable data condition that cannot change without
human action re-logs at ERROR indefinitely, which trains the reader to filter
ERROR on the one daemon where onboarding failures surface.

- **Not a code-path bug**: no turn, invite or grant is affected. The row simply
  never advances, which is exactly what the log says.
- **Fix shape**: log each stranded jid once per process (a seen-set), or once
  per status transition, and keep the count on the existing health/metrics
  surface. Loud once beats loud forever — the same distinction `L45` draws
  between a config silently ignored and one whose drop is named.
- **Operator fix for the live row**: decide what that jid should be and set it,
  or delete the row if the onboarding attempt is abandoned. arizuko will not
  guess it.
- **Source:** `onbod/main.go:387-408`; live on sloth 2026-08-21.

## ✅ FIXED 2026-08-21 L60 — `ARIZUKO_IS_ROOT` never reached ant: the `/var/lib/share` skip was dead (2026-08-20)

`ant/src/claude.ts` (`extraDirs`, moved 2026-08-20 from `index.ts:251`) reads
`process.env.ARIZUKO_IS_ROOT` to keep an elevated `/root` turn from adding
`/var/lib/share` to `additionalDirectories`. But `seedSettings` writes
`ARIZUKO_IS_ROOT` only into the settings.json **env block**
(`container/runner.go:838`), which reaches the SDK subprocess — never ant's own
process.env; no `-e ARIZUKO_IS_ROOT` exists in `buildArgs`. So the check is
always false and every elevated turn gets `/var/lib/share` as an extra dir
anyway. Same class as `ARIZUKO_MODEL`/`RUNED_RUN_TIMEOUT`/`ARIZUKO_BACKEND`
([ant_env_delivery]): a var ant reads that nothing emits is silently dead.

- **Impact**: cosmetic-to-low — root already mounts `/var/lib/groups` RW; the
  extra read-only context dir is redundant, not an escalation. The agent-side
  `$ARIZUKO_IS_ROOT` doc claim ("echo $ARIZUKO_IS_ROOT shows privilege") holds
  only inside the SDK's shells, not for ant.
- **Fix shape**: either emit `-e ARIZUKO_IS_ROOT=` in `buildArgs` (one line,
  symptom-level) or fold it into the three-layer config render
  (`specs/6/7` § "ant owns the capabilities", next increment) where ant renders
  the settings and therefore knows the flag. The render is the cause fix.
- **Source:** `container/runner.go:694-728` (the `-e` list), `:838`;
  `ant/src/claude.ts` `extraDirs`.
- **Fixed 2026-08-21**: `buildArgs` takes `elevated` and always emits
  `-e ARIZUKO_IS_ROOT=` (`"1"` or empty), so the value is present-and-false
  rather than absent-and-ambiguous — the shape that made the first two
  instances of this class hard to spot. Pinned by
  `TestBuildArgs_EmitsIsRootBothWays`. The cause fix (ant renders the settings
  itself and therefore knows the flag) still belongs to `specs/6/7`'s next
  increment; this makes the documented behaviour true today.

## ✅ FIXED 2026-08-21 L59 — the `5/23` skill-guard cannot gate what codex loads (2026-08-20)

`codex` discovers skills natively from `$HOME/.claude/skills`. In the container
the group dir IS `$HOME` (`container/runner.go:552` mounts it at `/home/node`),
so codex loads the group's skills whether the connector wants it to or not.
Measured on codex-cli 0.147.0 (2026-08-20): a skill written to
`cwd/.claude/skills` was NOT loaded, every `$HOME/.claude/skills` entry WAS,
and `--ignore-user-config` did not change it. codex exposes no flag to restrict
or disable that discovery.

`ant/src/codex.ts` runs `scanSkills` and renders `scan.clean` into a prompt
index. That index is duplicate work — codex already read those files — and the
guard's verdict is advisory: a skill the scan EXCLUDES is still loaded by codex,
it is merely absent from the index the connector appends.

- **The agent writes `$HOME`.** A SKILL.md the agent writes itself is loaded on
  the next turn. That is the self-grant class `strictMcpConfig` closes for
  claude and `--ignore-user-config` / `--no-extensions` close for codex config
  and pi extensions. Skills are the hole left open.
- **CORRECTION 2026-08-20, same day: codex HAS a gate — I missed it.** The
  entry above reasoned "no flag to restrict discovery, therefore no gate".
  codex ships a full hook system: `$CODEX_HOME/hooks.json` declares
  `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `PreCompact` and `Stop`,
  using the SAME schema as Claude Code's settings hooks (`matcher` plus
  `{type:"command", command, timeout}`), gated by hook trust
  (`--dangerously-bypass-hook-trust` for vetted automation). So the `5/23`
  guard can be a WRITE-TIME gate for codex exactly as it is for claude — a
  `PreToolUse` matcher on the write tools — rather than the advisory load-time
  scan. The absence of a `--skill` flag was never the relevant question.
  Same error shape as [[our-flag-their-limit]] filed an hour earlier: I
  concluded a capability was missing without looking for the mechanism that
  provides it.
- **The real fix is one hook set, rendered per harness.** ant declares the guard
  (and the mid-turn steering drain) ONCE and renders it into each harness's
  native config: claude's settings/SDK callbacks, codex's `hooks.json`, pi's
  extensions. One declaration, many readers — not three guards. That subsumes
  options (a)/(b)/(c) above and is tracked with the seam inversion in
  `specs/6/7` § "ant owns the capabilities" (next increment; the seam itself
  shipped 2026-08-20).
- **FIXED 2026-08-21** by that increment. `ant/src/policy.ts` holds each
  capability as a harness-neutral `(ToolEvent) => Verdict` and one `BINDINGS`
  table; three renderers read it — claude SDK callbacks, a generated
  `$CODEX_HOME/hooks.json` whose commands run `policy-cli.js`, and a pi
  extension loaded by `-e`. Two measurements changed the shape and both are in
  the spec: codex writes with **`apply_patch`**, not `Write`/`Edit`, so the
  write-time matcher never fires there — `skill-rescan` therefore binds to every
  tool and is the gate on codex; and codex **swallows a hook's stderr**, so the
  CLI spools its log lines to a file the harness drains. Verified end to end
  against codex 0.147.0: a turn told to write a SKILL.md carrying `curl … | bash`
  left `…/skills/evil/SKILL.md.quarantined` behind. The guard is a mechanism on
  codex now, not advice.
- **Source:** `ant/src/codex.ts` `CodexSession.events`; `ant/src/skills-scan.ts`;
  `specs/5/23`; the matrix note in `specs/6/7-environment-contracts.md`.

## ✅ FIXED 2026-08-21 L58 — a re-fed turn lost its observed window: the cursor advanced before the container spawned (2026-08-20)

`routd/prompt.go:38` advances the per-topic observed cursor inside
`buildAgentPrompt`, immediately after `ObservedSince` reads the rows and
**before** anything spawns. Nothing rolls it back. The prompt is built at
`dispatch.go:283`; every failure after that point discards the rendered prompt
and re-feeds the batch on the next 2s poll (`dispatch.go:117` transport
failure, `:126` the growing backoff, `:211` runed rejecting admission at
capacity). The re-fed turn calls `buildAgentPrompt` again — and now
`ObservedSince` returns nothing, because the first attempt already consumed the
watermark.

The trigger messages survive (their cursor advances separately, on success).
The **ambient observed context does not**: it is read once and lost for that
topic forever.

- **The comment states the opposite of the code.** `prompt.go:20-25` says
  "at-least-once — the cursor advance is not transactional with the turn; on
  crash recovery a topic may re-see an observed message, which is benign".
  Read-then-advance-then-spawn is at-MOST-once, and the failure is a silent
  loss, not a benign duplicate. A comment is a claim, not evidence.
- **`L55` made this constant.** The starved instance produced 4,704 dispatch
  runs against 18 spawns. Nearly every one of those was a re-feed, and every
  re-feed burned whatever observed rows had arrived since the last one.
  Candidate second cause for the sloth "context is bad or even missing"
  reports — `L54` was the fork half; this is the ambient half. Not yet proven
  against a live transcript.
- **Backend-independent, and worse without a transcript.** For `claude` an
  attempt that reached the model leaves the rows in the resumed transcript, so
  the loss is partial. `pi` runs `--no-session` (`ant/src/pi.ts`), so layer 1
  is always empty and nothing covers it.
- **Source:** `routd/prompt.go:27-39`; re-feed paths in `routd/dispatch.go`.
- **Fixed 2026-08-21**: `buildAgentPrompt` is now pure with respect to the
  cursor — it RETURNS the watermark and writes nothing. `processGroupMessages`
  collects the per-topic marks and applies them after `l.advance(chatJID, last)`,
  the one place a dispatch already commits its progress. That is the
  `serialization_point` move: the second bookkeeping point was removed rather
  than guarded. Every early return above that line (transport failure, busy
  backoff, steer ack) now leaves the window intact for the re-fed turn, which
  makes the read genuinely at-least-once as the comment always claimed.
  A failed `UpdateObservedCursor` is logged, not swallowed. Pinned by
  `TestBuildAgentPrompt_DoesNotConsumeTheObservedWindow`, which renders twice
  and requires the same window both times.

## ✅ FIXED 2026-08-29 L57 — the resreg routes face re-implements the route INSERT without the web-JID guard (2026-08-20)

`store.AddRoute`/`SetRoutes`/`UpdateRoute` all refuse a `chat_jid=web:*`
match (`ErrWebJIDRouted`, store/routes.go:48,150,170) because web JIDs are
direct-addressed and the route table never applies to them. The agent/REST
face on the routes resreg resource does NOT: `addRouteTx` and `setRoutesTx`
(routd/routes_resource.go) are their own INSERTs on resreg's tx and carry no
`matchesWebJID` check, so an agent's `add_route` accepts a `chat_jid=web:*`
rule that `resolve()` will never consult (direct address precedes the table).
The rule lands, `inspect_routing` annotates it as if it governed, and the
agent believes a web chat is redirected or observe-silenced when nothing
changed — the exact silent-message-loss confusion the skill doc closes with
"`chat_jid=web:*` is rejected" (ant/skills/self/chat-routing.md), which is now
true on only one of the two write paths. Classic one-renderer violation: two
writers of `routes`, one guarded. Found while defusing the anteval
`route-observe-silent` landmine.

- **Severity:** medium
- **Scope:** routd routes resource / store write-discipline
- **Affected:** agent `add_route`/`set_routes`, REST `/v1/routes` add/set
- **Source:** routd/routes_resource.go `addRouteTx`/`setRoutesTx` vs store/routes.go:48
- **Status:** FIXED 2026-08-29

Fixed by giving both writers the SAME predicate rather than a second copy:
`matchesWebJID` is now exported as `store.MatchesWebJID`, and `addRouteTx` and
`setRoutesTx`'s per-route loop refuse `store.ErrWebJIDRouted` before the INSERT.
Both handlers map that one error to 400 — a caller's bad rule, not a server
fault. `TestRoutesMCP_WebJIDRuleIsRefusedOnBothWriters` drives `add_route` and
`set_routes` over the agent socket and asserts nothing lands; it fails with
either guard removed.

## ✅ FIXED 2026-08-20 L56 — a bot reply swallows the user's slash command

**First diagnosis was WRONG and is kept here as the correction.** `web:eval`'s
`agent_cursor` read `2026-08-15T21:32:31Z` while its neighbours were current, and
that looked like a permanently stuck chat. It was not stuck — it was lagging one
long batch. Once the last anteval case finished, the cursor advanced to
`12:16:58Z` on its own. Correct measurement, wrong interpretation.

The real fault is narrower and much more general. `processGroupMessages` steered
the batch's **newest** row:

```go
last := msgs[len(msgs)-1]
…
if l.steer(chatJID, last, folder) { … }
```

A slash command is consumed by `steer` and nowhere else, and `handleCommand`
refuses a bot row (`msg.BotMsg` → false). So whenever the agent answers between
the command and the next poll, `steer` inspects the reply, refuses it, and the
command falls through into the turn as plain text. Nothing logs.

Measured on krons, `web:eval`, both attempts identical in shape:

| time     | row                        |
| -------- | -------------------------- |
| 11:58:42 | user `/new`                |
| 12:04:20 | **bot** "Page is published…" |
| 12:08:29 | user `/new`                |
| 12:12:20 | **bot** "⏳ publishing…"     |

Neither `/new` was consumed — no `"Session cleared."` ack was ever delivered, and
`sessions(eval,"")` moved from `948b6564` to `675edf03` because a turn replaced
it rather than `cmdNew` clearing it. anteval's baseline guard then aborted,
correctly.

This is not an anteval quirk: the same hole loses a user's `/stop`, `/root`,
`/gate` or a sticky `#topic` in any chat busy enough to answer first.

- **Severity:** medium (silent; a user's command is executed as chat text)
- **Source:** routd/dispatch.go — `steer(chatJID, last, folder)` on the batch's
  newest row
- **Fix:** steer the newest NON-bot row (`newestUserMsg`), and advance past that
  row only — anything newer has not been consumed.
- **Tests:** `routd.TestNewestUserMsg_{SkipsBotRowsAfterACommand,TakesTheLatestUserRow,AllBotBatchSteersNothing}`

## ✅ FIXED 2026-08-20 L55 — a restart starves the instance: 4,704 dispatches produced 18 spawns (2026-08-20)

Restarting `arizuko_krons` fires `routd.checkMigrationVersion` for every folder
below `MIGRATION_VERSION`, queueing an `auto-migrate-<folder>` turn each. krons
runs `MAX_CONCURRENT_CONTAINERS=2`, so the queue drains two at a time while the
dispatch loop re-feeds every waiting turn on its 2s poll (`L51`).

Measured over 15 minutes after the 11:42Z restart:

| signal                | count |
| --------------------- | ----- |
| `dispatch run`        | 4,704 |
| `mcp server listening`| 4,704 |
| `spawning container`  | **18** |

A 260:1 ratio, and the re-feed is not free: every cycle resolves capabilities and
**starts an MCP server**. In one 60s window four turns — `auto-migrate-mayai`,
`-krons`, `-fiu`, `-eval/etjty2l-child-delegate` — were each dispatched 30 times,
once per poll.

Two things it broke, both diagnosed rather than assumed:

- **anteval scored 0/5 with 0 tokens on every case.** The turns dispatched
  (`topic=etk2gpt-*`, `trigger=user:anteval`) but never got one of the two slots
  inside a case's 120-180s timeout. Not an agent failure and not a deploy defect.
- **The `L54` per-case root reset could not win.** `auto-migrate-eval` runs on
  `folder=eval, topic=""` and re-persists a root session; anteval's new baseline
  guard correctly aborted rather than score inherited history — the guard worked,
  the instance was simply not quiet.

Also visible and separate: `session: resetting on idle expiry` logged 270 times
in the window, once per re-fed dispatch for the same topic, because the reset
decision is re-evaluated every poll on a chat whose cursor is weeks old.

- **Severity:** medium (self-clearing, but it starves real user turns for minutes
  after every restart and burns model budget on migrate turns nobody asked for)
- **Source:** routd `checkMigrationVersion`; the 2s re-feed poll (`L51`);
  `MAX_CONCURRENT_CONTAINERS=2` in `env/runed.env`
- **Status:** resolved-not-yet-removed. Of the shapes costed (backoff / stagger
  the migrate wave / operator-gated auto-migrate / the `L51` push path), the
  per-chat backoff shipped: it removes the busy-wait for EVERY starvation source
  (migrate waves, task bursts, a slow folder), not just the restart one, and is
  contained in routd — no new cross-daemon flow, no changed delivery semantics
  (cursor and turn_context handling untouched; at-least-once holds).
- **Fix:** `routd/dispatch.go` — a batch runed refuses as Busy retries on a
  per-chat exponential backoff (4s base, ×2 per consecutive refusal of the same
  batch, 60s cap) instead of every 2s poll; `busyDeferred` skips the re-feed
  BEFORE the expensive setup (capability resolve, MCP socket, prompt build). A
  new inbound message clears the backoff (fresh activity and slash commands are
  never delayed); any cursor advance clears it too. The 15-minute krons window
  drops from ~450 dispatch cycles per starved chat to ~19.
- **Tests:** `routd.TestBusyOutcomeRetriesWithoutAdvanceOrBreaker` (a busy
  result must not produce another dispatch on the very next poll; the same turn
  completes after the window); `routd.TestBusyBackoffGrowsAndNewMessageBypasses`.
- **Deploy:** routd only — rebuild + `systemctl restart`. The `session:
resetting on idle expiry` re-log and the `L51` push path stay open as `L51`.
- **Do NOT** read an anteval score taken while a migrate wave is draining.

## ✅ FIXED 2026-08-20 L54 — every forked topic answered its first turn with no context (2026-06-01)

Reported from the sloth desk 2026-08-19 19:45Z, verbatim: *"Vidí nějak tu diskusi,
nebo vždy začíná znovu bez kontextu?"* — "Does it see the discussion at all, or does
it always start over without context?" It did start over. A user replying inside a
Discord thread opens a new topic, and the first turn on that topic carried no
transcript and no observed window: the bare @mention and nothing else.

Three faults on one call path, each hiding the next:

1. `core.NewSessionID()` minted `sess-<unixnano>`. `claude --resume` takes a UUID
   and nothing else, so `ant/src/index.ts` `resolveResumeSession` refused the id
   and started fresh.
2. `copyParentSession` had already copied the parent transcript to
   `sess-<nano>.jsonl` — 934 KB on the live case — a file the agent could never
   open. The fork's whole purpose, silently void.
3. `EnsureTopicLineage` stamps `observed_cursor = now` so the child reads only
   post-fork context. Correct when it inherits the transcript; with (1) and (2)
   it removed the last source of context the child had.

Dead since `dcf29e03` (2026-06-01), which added the UUID guard to stop `--resume`
burning a turn on the retry. That fixed the symptom and left the cause: routd kept
minting an id the agent would always reject. The guard then logged the loss at
INFO and answered anyway — a user-facing failure that never surfaced.

Live proof (sloth, 2026-08-19): 19:40:02 topic `""` resumes `a2fb6376` with the
full 10-message observed window; 19:44:58 topic `1539720616280727582` forks;
19:45:01 `session id sess-1787168698153112071 is not a resumable UUID (arizuko
placeholder) — starting fresh`; 19:45:02 new session `dcac785c`, prompt = one
`<message>`, no `<observed>`. The complaint lands at 19:45:17.

- **Severity:** high (fleet-wide, user-visible; krons 26 hits/7d, sloth 4/3d)
- **Source:** core/types.go:282; routd/dispatch.go:411-455; routd/mcp.go:97;
  container/runner.go:1298; ant/src/index.ts:21-42
- **Fix:** `NewSessionID` returns a UUIDv4, so a minted id is resumable by
  construction and ant's guard goes back to being a safety net for a pruned
  transcript. `CopySession` restamps `"sessionId":"<src>"` → `"<dst>"` on every
  line so the child transcript names the child. `CopySession` reports whether it
  copied, and `ensureTopicWithFork` clears the child's `observed_cursor` when it
  did not — no fork can start with zero context on both axes.
- **Tests:** `core.TestNewSessionIDIsResumable` pins the id against ant's own
  regexp; `container.TestCopySession_RestampsSessionID`;
  `routd.TestEnsureTopicWithFork_{NoParentSessionKeepsObservedWindow,CopiedSessionKeepsForkCursor}`.
- **Deploy:** routd + the agent image both carry half of this. Rebuild and restart
  both. Topic rows already holding a `sess-<nano>` id keep starting fresh once
  each — the next turn stores the real UUID, so no migration is needed.

### The other half: an isolated task resumed the chat it was isolated from

The same report also covers *"context is bad"*, and that is a second defect found
in the same transcript. `specs/4/8` §"Implemented beyond base spec" defines
`context_mode`: `"group"` resumes the session, `"isolated"` means **no
`--resume`**. `routd/dispatch.go` passed `SessionID: l.db.SessionID(folder,
topic)` unconditionally and set `Isolated` only as a separate flag, which
suppressed the `session_log` row and the container reuse but never the resume.

So every isolated cron opened the chat's transcript, ran, and appended its whole
tool-heavy turn to it. sloth's `main/trading` transcript holds 13 turns: **8
crons** (`/migrate`, `/compact-memories` ×6, the MacroHype monitor) against **4
human ones**, at 1 MB. Claude Code compacts a transcript that size, and it was
compacting a conversation that was mostly cron.

All 10 of sloth's active tasks already declare `context_mode = 'isolated'`. The
operator asked for this and the code did not do it.

The write-back is the same defect mirrored, and closing only the read half makes
it worse: runed mints a throwaway session for a spawn that arrives with none, and
both `dispatchRun`'s backstop and `recordTurnResult` then installed that id as the
topic's session. Before the fix an isolated cron already replaced the chat's
session with whatever it ended in; after a read-only fix it would replace it with
a transcript holding nothing but the cron. Both writers now skip an isolated turn.

- **Source:** routd/dispatch.go:637,384 (was); routd/turns.go:708 (was);
  specs/4/8-scheduler-service.md:39-41
- **Fix:** `dispatchRun` resolves `isolated` once and sends `SessionID: ""`.
  The backstop moved into `persistSessionBackstop`, which skips an isolated
  trigger, and `recordTurnResult` applies the same rule on its own path.
- **Tests:** `routd.TestDispatchRun_{IsolatedTaskGetsNoSession,GroupTaskResumesSession}`
  assert on the `RunRequest` routd builds, not on the call site;
  `routd.TestPersistSessionBackstop_{IsolatedDoesNotOverwriteSession,ChatTurnPersistsSession,RecordedTurnWins}`
  cover the write-back.
- **Deploy:** routd only. Existing chat transcripts keep the cron turns already
  written into them; only new runs stop adding to them.

### ✅ FIXED 2026-08-20 Known consequence: the fork fix turned anteval's case isolation OFF

`5067cfdf` (2026-08-08) fixed cross-case contamination in anteval by giving each
case its own topic: eight smoke cases had shared one conversation, so case 1
answered "I can't, that tool is root-only" and cases 2-8 inherited it — the run
scored 4/8 off history rather than off the live tool list.

That fix worked only because a new topic started blind, which is the defect above.
`specs/5/F:48` is explicit that the first turn in any non-main topic forks the
folder's main, so with the fork repaired every anteval case now inherits the eval
chat's root transcript. On krons `sessions(eval, "")` holds `56952d01` at 167 KB
of prior eval runs. The contamination `5067cfdf` removed comes back on the next
run, and anteval scores the history again.

**Fix (2026-08-20):** `5/F` unchanged; anteval establishes a pristine baseline
itself. Before EVERY case the driver reads `GET /v1/topic_sessions` for the
root topic (`--folder`, now required) and, when a session exists, sends a bare
`/new` (steer-consumed: deletes the root sessions row, spawns no turn) and
holds case injection until the session is observably gone — per-case because a
group-context scheduled follow-up (state-persist) fires topic-less on the ROOT
topic and re-persists a session mid-run. A root session that will not clear
aborts the run loudly. Verified while fixing: with main sessionless the fork
copies nothing and clears the case topic's observed cursor, and the ambient
window cannot carry one case into another — `ObservedSince` takes only
`is_observed=1` rows and `PutMessage` hard-codes injected rows to 0 — so there
is no second hole. Tests:
`anteval/pkg/run.TestDrive{ResetsDirtyRootBeforeCases,PristineRootSendsNoNew,AbortsWhenRootWontClear,ResetsRootDirtiedMidRun}`,
`TestHTTPTargetSession`. Spec: `5/9` §"Session isolation".

Litter this also quantifies: `groups/eval/.claude/projects/-home-node/` holds **38
orphaned `sess-*.jsonl` copies, 7.9 MB**, one per forked topic — every one a
transcript routd copied and the agent then refused to open. The fix stops new ones
accruing; the existing 38 are dead files.

- **Severity:** medium (no live agent affected; it silently invalidates the one
  tool that certifies the agent)
- **Source:** routd/dispatch.go:173-177; specs/5/F-topic-lineage.md:48;
  anteval/pkg/run/run.go (topic per case, `5067cfdf`)
- **Status:** OPEN, needs a decision. anteval must isolate explicitly instead of
  relying on a broken fork. Cheapest option with no contract change: clear the eval
  chat's root session before a run, so main holds no transcript and each case forks
  from nothing. The alternative — a topic that declares "do not inherit" — amends
  `5/F` and needs sign-off.
- **Do NOT** run anteval and trust the score until this is settled.

## ✅ FIXED 2026-09-20 L53 — the secret broker writes `surface: "crackbox"` on every secret read (2026-08-18)

`store/secret_use_log.go:57` stamps `audit.SurfaceCrackbox` on the `audit_log`
row it emits per secret read, and `audit/log.go:34` defines that constant as the
string `"crackbox"`.

The secret never went near crackbox. `LogSecretUse` is called by the MCP broker
middleware — the resolver `ipc/ipc.go:247` calls the audit seam — so the surface
is `mcp`. The label was wrong before `6/24` and is now worse: it names a
component production does not run at all, so an operator filtering
`surface = 'crackbox'` in `audit_log` finds secret reads and nothing else,
while filtering `surface = 'mcp'` misses them.

**Severity: low, but it corrupts the one query an audit table exists for.**
Nothing breaks; the answer is just wrong.

**Why this is not an inline fix.** `surface` is a closed enum whose values are
written into `audit_log` rows and kept. Changing the constant makes historical
rows and new rows disagree about the same event, and the spec that defines the
closed set is `specs/5/I`. Either the write changes and old rows stay (a query
must then union two values), or a migration rewrites them. That is a data
decision, so it is recorded here for sign-off rather than shipped.

Recommended: change the write to `audit.SurfaceMCP`, delete `SurfaceCrackbox`
from the enum, and migrate existing rows — the string appears on exactly one
write path, so the rewrite is `UPDATE audit_log SET surface='mcp' WHERE
surface='crackbox'` with no ambiguity about which rows are meant.

- **Status:** fixed 2026-09-20.
- **Fix:** `store/secret_use_log.go` stamps `audit.SurfaceMCP`. There is
  exactly ONE production writer — routd's MCP broker middleware
  `ResolveConnectorSecrets` (`routd/mcp.go:530-548`) — so `mcp` is not a
  guess about the surface, it is the surface. `crackbox` named a sandbox that
  is not on this path, so a query by surface found none of these rows.

  RE-MEASURED 2026-09-20: `SurfaceCrackbox` now has exactly one use left in the
  tree, its own declaration at `audit/event.go:27`.

  PROVEN BY MUTATION: put `SurfaceCrackbox` back and
  `TestSecretUseLogStampsTheMCPSurface` fails. The test reads the row back
  through `audit.Query` rather than asserting the constant, and fails loudly if
  no `secret.read` row exists at all, so it cannot pass vacuously.
- **Left to the operator:** rows already written carry `crackbox`. Rewriting
  them is a data decision, not a code one.

## ✅ FIXED 2026-08-29 L52 — `arizuko token revoke <jid>` cannot revoke a suffixed chat token (2026-08-18)

`cmd/arizuko/token.go:222` derives the owner folder with
`groupfolder.JidFolder(jid)` when the operator does not pass one, and
`groupfolder/folder.go:110` implements the `web:` case as
`strings.CutPrefix(jid, "web:")` — the whole remainder is returned as the
folder.

But `token issue chat <folder> [<suffix>]` mints `web:<folder>/<suffix>`. So for
every token that has a suffix the derived folder is `<folder>/<suffix>`, which
is not a folder at all, `RevokeRouteToken` matches no row, and the command dies
with **"token not found or wrong owner"** — a message that names the wrong
cause. Measured live on krons while verifying the `6/24` deploy: revoking
`web:eval/egress624` failed, and the same command with `eval` passed explicitly
succeeded on the same row.

The `hook:` branch immediately below it already strips the last path segment
(`folder.go:114`). The `web:` branch does not, and that asymmetry is the bug.

**Severity: medium, operator-facing.** Every suffixed chat token is
unrevokable by its documented command unless the operator happens to know to
pass the owner folder as a second argument. A token that cannot be revoked is a
standing credential.

**This is the [identity_derived_not_read] class**, and it is a leftover of the
fix that already closed the same class elsewhere: migration `200` added
`target_folder` and states "nothing parses it back out of the JID any more".
The mint stopped parsing; `revoke` did not. The owner folder is a column on the
`route_tokens` row (`owner_folder`), and the command parses it out of the JID
instead of reading it. The cause fix reads the row — `RevokeRouteToken`
already looks the token up, so the folder need not be supplied at all when the
caller is the operator CLI. Do NOT "fix" `JidFolder` to strip a segment: `web:`
JIDs for folder-level chats have no suffix, so stripping would break the
common case and merely move the asymmetry.

Not fixed: separate concern from the `6/24` egress split it was found during.

Fixed 2026-08-29 with the cause fix the entry names: `store.RouteTokenOwner`
reads `owner_folder` off the row, and `tokenRevoke` (`cmd/arizuko/token.go`)
calls it when the operator gives no folder. An explicit second argument still
wins, so a higher folder revoking on behalf of a descendant is unchanged, and a
JID with no token now says so instead of blaming the owner.
`groupfolder.JidFolder` is untouched, as the entry insists.
`TestRouteTokenOwner_ReadsTheColumnForASuffixedJID` (`store/route_tokens_test.go`)
covers `web:eval` and `web:eval/egress624` — the live failure — revokes both
with the owner it reports, and asserts `JidFolder` still returns the whole
remainder, so a later "fix" there is caught.

## L51 — every turn waits ~1s for a poll tick that could be a channel send (2026-08-18)

`routd/loop.go:325` drives dispatch from `time.NewTicker(l.pollEvery)`, and
`pollEvery` defaults to **2 seconds** (`routd/pending_actions_http.go:12`
documents it). There is no push path — grepping `loop.go` for a channel, a
notify or a wake finds nothing.

So an inbound message lands in `messages`, and then nothing happens for up to 2s
(≈1s average) until the next tick reads it. That latency is paid on **every
turn**, before the container is even spawned, and it is invisible: no log, no
metric, nothing that says "we sat here".

For scale: the per-spawn proxy readiness poll costs ~10ms and I declined a
push-callback for it as not worth the machinery. This is ~100x that, and it
already has the machinery.

**A channel, not a webhook.** routd's ingest handler and its dispatch loop are
the same binary — the handler that writes the row can signal the loop directly.
An HTTP callback would add a listener, a retry policy and a delivery guarantee
to cross a process boundary that does not exist. Webhook is cross-process push;
this is in-process push.

Shape: a buffered `chan struct{}` of size 1, non-blocking send after the message
row commits, and the loop selects on it alongside the ticker. The ticker STAYS —
it is the self-healing path for anything that writes a row without signalling
(a migration, a direct DB write, a future daemon), which is exactly the property
that makes polling forgiving. Push for latency, poll for correctness.

Other tickers checked and deliberately NOT included: `runed` hourly
housekeeping, `timed`'s 60s scheduler tick (inherently time-driven),
`webd/hub.go` SSE keepalive, `webd/turn.go` 15s status. None of them sit on the
per-turn hot path.

Not yet measured end to end — before building, instrument one turn to confirm
the tick is really the dominant wait rather than container spawn. If spawn is
300ms and the tick is 1000ms, this is the biggest single latency item in the
system.


## L50 — LoadConfigFrom leaks one instance's .env into the process (2026-08-18, found while fixing L47)

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

Cause fix (redesign, needs sign-off): a non-mutating loader — read the `.env`
into a map like compose already does, resolve the Config from it, and let a
caller that genuinely wants process env opt in. Touches every `LoadConfigFrom`
caller, so it is a contract change, not an inline patch.

Recorded, not fixed (Bug Triage Protocol).


## ✅ FIXED 2026-09-06 L49 — proposal: an LLM gateway, so the container never holds the real model key (2026-08-18, operator's idea)

Shipped as `specs/6/23-llm-gateway.md` (the gateway holds the credential; one
virtual key per turn, bounded by the run ceiling and deleted at the end) and
`specs/6/39-gateway-owns-spend.md` (the gateway is the ledger: the folder is a
team, its cap is that team's one-day budget enforced on the request, the caller
rides the key as `user_id`, and arizuko stores no spend). Per-key metering is a
read, as this entry asked.

The codex-on-ChatGPT-OAuth gap below stands: it is a host mount with no token to
swap and no base URL to redirect, so no gateway can meter it.

**Today the agent container holds the real provider key.** `ANTHROPIC_API_KEY`
is in `store.EnvProfileKeys` (`store/secrets.go:115`), which `5/14` defines as
the one table-backed credential class that reaches container env by design. The
agent can read its own environment. There is also **no base-URL override
anywhere in the tree** — no `ANTHROPIC_BASE_URL`, no seam to point model traffic
elsewhere.

**A gateway is not a proxy, and that is the point.** Egress proxying cannot
meter model calls without MITM, which `L48` argues we should drop entirely. A
gateway avoids interception: the client is CONFIGURED to call it
(`ANTHROPIC_BASE_URL`), so the gateway IS the endpoint. No TLS termination, no
CA in the container, nothing spliced.

The shape:

- arizuko mints a **per-spawn virtual key** with a budget and a TTL, and emits
  it plus the base URL into the container.
- The gateway holds the real provider keys. The container never sees one.
- **Revocation is deleting the virtual key** — immediate, and it needs no
  cooperation from a running turn.
- **Metering is per-key by construction**: tokens, cost and limits land against
  the spawn, so per-folder and per-user cost attribution is a read, not an
  estimate.

**OAuth is a hard requirement, not a preference.** `store.EnvProfileKeys`
already carries `CLAUDE_CODE_OAUTH_TOKEN` beside the API keys, and subscription
billing is flat-rate where API keys bill per token — at arizuko's turn volume
the API-key path is impractically costly. A gateway that only speaks API keys is
disqualified.

Candidates (verified 2026-08-18):

- **Bifrost** (Maxim AI) — Apache-2.0, single Go binary, self-hosted.
  Purpose-built for Claude Code: `ANTHROPIC_BASE_URL` points the CLI at it,
  `ANTHROPIC_AUTH_TOKEN` carries the virtual key (sent as `Authorization:
  Bearer` automatically). Four-tier budget hierarchy, per-request token counts,
  per-virtual-key cost attribution, 20+ providers with failover, ~11µs overhead.
  Documented to detect an Anthropic MAX account instead of API-key auth and work
  with it out of the box. Matches arizuko's stack and the minimal requirement.
- **LiteLLM** — MIT, 100+ providers, the reference virtual-key/budget model.
  Handles subscription auth by FORWARDING the client's OAuth header upstream
  (`forward_client_headers_to_llm_api`). Python + Postgres; heavy for a solo
  instance.

**THE ONE THING TO VERIFY BEFORE COMMITTING — where the OAuth credential
lives.** It decides whether this proposal is a security change or only a
visibility one:

- Credential **at the gateway**, spawn gets a revocable virtual key → this
  entry's actual goal: isolation AND metering, subscription billing preserved.
- Credential **forwarded from the client** → the container still holds the real
  token. Metering and cost attribution, but the security half evaporates.

LiteLLM's documented mechanism is explicitly the second. Bifrost's phrasing
suggests the first, but "detects if you're using a MAX account" comes from a
vendor summary, not its docs. Read Bifrost's own documentation on MAX-account
configuration before building anything.

**Coverage is bounded, and the gap is the worst-isolated path.** A gateway can
front a provider only where BOTH hooks exist: a base-URL override and a token in
env. arizuko's two subscription routes differ exactly there:

- **Claude Code** — `CLAUDE_CODE_OAUTH_TOKEN` in env + `ANTHROPIC_BASE_URL`.
  Both hooks present; a gateway fronts it.
- **Codex on ChatGPT-OAuth** — `ant/skills/oracle/SKILL.md:39` documents it as a
  HOST MOUNT: `HOST_CODEX_DIR` bind-mounted to `/home/node/.codex`, "no env vars
  needed". No token to swap, no base-URL redirect. A gateway cannot front it.

So metering would cover the Anthropic subscription and every API-key provider,
and MISS codex-on-ChatGPT-OAuth. Its meterable alternative
(`CODEX_API_KEY`/`OPENAI_API_KEY`) is the per-token billing this entry exists to
avoid.

Worth noting the host-mount path is also the weakest isolation arizuko has — a
bind-mounted `~/.codex` gives the container the operator's whole ChatGPT
session, not a scoped token — and it is the one case a gateway structurally
cannot improve, because nothing in the request path can be intercepted or
replaced. That is its own problem, not this entry's.

**NOT iron-proxy.** That is an egress MITM proxy for injecting credentials into
arbitrary HTTP — a different mechanism at a different point in the path. It
meters nothing.

Wiring cost is small: emit `ANTHROPIC_BASE_URL` and the virtual key as `-e` in
`buildArgs`. Note the trap — anything not emitted there is silently dead in the
container (three prior instances: `ARIZUKO_MODEL`, `RUNED_RUN_TIMEOUT`,
`ARIZUKO_BACKEND`).

Ties: `5/14` (credential tiers — this moves the model key OUT of the env tier),
`L48` (the egress proxy stays MITM-free because metering does not live there),
`X2` (the same "container must not hold the raw secret" argument, other half).


## L48 — proposal: one proxy per spawn, instead of one registry keyed by source IP (2026-08-18, operator's idea, needs sign-off)

`6/8` §"Security invariants" makes egress safety depend on an ordering the
consumer must honour:

> Consumers MUST register before the spawned container starts outbound traffic,
> and MUST unregister on exit. The "register before traffic" ordering is the
> consumer's invariant, not egred's — egred just enforces "no entry → deny".

Late registration fails closed, which is fine. **Late UNregistration does not.**
Docker reuses source IPs; if runed dies between a container exiting and
`/v1/unregister`, the stale `(source_ip, id, allowlist)` entry survives and the
next tenant on that IP inherits the previous folder's allowlist. A cross-folder
egress leak held off by a convention, which root `CLAUDE.md` says to replace
with a design that makes the bad state impossible.

**The proposal: give each spawn its own proxy instance**, configured with that
folder's allowlist at spawn time and dying with the turn. No registry, no IP
keying, no ordering obligation, nothing to reuse. The allowlist is static for
the proxy's whole life.

What it buys beyond the leak:

1. **An off-the-shelf proxy becomes usable unmodified.** The one thing that
   ruled out Squid and tinyproxy was needing dynamic per-source rules without a
   restart; a per-spawn proxy never needs a rule change at all. Envoy's control
   plane stops being necessary too.
2. **~890 lines of arizuko-specific code stop existing** — `pkg/admin` (572),
   `pkg/match` (144), `pkg/client` (175) are all in service of the registry.
3. **The failure domain shrinks to one turn** instead of every container.

Costs, honestly:

- One more process per turn. Note `6/20` already DECLINED warm pools, so
  per-turn ephemeral is the accepted shape and the spawn machinery exists.
- The proxy must be listening before the agent's first egress — an ordering
  problem, but a local one inside a single spawn rather than a distributed one
  across a shared registry.
- Network plumbing per pair, unless the proxy runs as a sidecar in the agent's
  own netns, which avoids most of it.

**"Static config precludes the MITM/header-injection work" — checked, it does
not.** Two reasons:

- **Nothing dynamic exists to preclude.** `runed/docker.go:127` builds the
  EgressConfig "from runed's own config + that static per-folder list", once, at
  spawn. There is no re-register and no mid-run update anywhere. The registry's
  dynamism serves MULTIPLEXING one proxy across many containers — not live rule
  changes. Per-spawn removes the multiplexing, which is all it was doing.
- **Per-spawn is BETTER for header injection, not worse.** Injection needs
  this tenant's credential; a shared proxy must map source IP → whose secret,
  the same fragile keying that causes the leak above. One proxy per spawn has
  exactly one tenant and no mapping. TLS interception needs a CA trusted inside
  the container — per-spawn that CA can be minted for one turn, instead of a
  fleet-wide MITM CA whose leak compromises every container ever spawned.

The credential and the lifetime become the same scope, which is what makes the
injection half safe rather than merely possible.

**Shape, measured (2026-08-18).** Running one per turn means it must be tiny,
and it nearly is already. `pkg/proxy` is 638 non-test lines — `peek.go` 279 (SNI
extraction, the hard part), `proxy.go` 164 (CONNECT + HTTP splice),
`transparent.go` 106 and `origdst_*.go` 89 (transparent mode). The registry
coupling is one constructor parameter, `func New(allow *admin.Registry)`; for a
single-tenant proxy that becomes a plain allowlist.

Drop transparent mode — a sidecar does not need it, the container gets
`HTTP_PROXY` pointing at its own proxy, so nothing is intercepted — and the
whole thing is **peek + splice + match, roughly 450 lines.**

So: SHRINK rather than adopt. tinyproxy is smaller in C, but not smaller
operationally once a config renderer and a second build language are counted,
and it does nothing the 450 lines cannot. Go suits the per-turn model —
millisecond start, a few MB resident.

It also improves the "ship one component to a stranger" story (`6/16`): a
450-line single-tenant proxy with a static allowlist can be read in an afternoon
and trusted. A 2,400-line system with a registry, an admin API and a source-IP
trust model cannot.

Not considered in `6/8`, `6/9` or `6/20`. Worth costing before any further work
on the registry, because it would delete the registry rather than improve it.


## L47 — a package's skills never reach a group that already existed (2026-08-17, FIXED)

`seedSkills` (`container/runner.go:1125`) copies a package's `skills/<n>/` into a
group's `~/.claude/skills/`. It has exactly one non-test caller:
`container.SetupGroup` (`runner.go:1076`). `SetupGroup` is reached only from
group/instance CREATION — `cmdCreate` (`cmd/arizuko/main.go:335`), `cmdGroup`
(`:448`), `onbod/main.go:869`, `dashd/groups_admin.go:114` — and from
archive-apply. **`cmdGenerate` is not among them.**

So a package's skill lands in a group only if the package was already installed
when that group was created. Verified by walking the call graph, not inferred.

`packages install` / `upgrade` / `sync` all print "run `arizuko generate
<instance>` to apply" (`cmd/arizuko/packages.go:521,588,622,678`). The operator
does exactly that, sees success, and no group that already existed ever receives
the skill — on first install or on any later revision.

Doubly closed: `cpDirFresh` (`runner.go:1271`) skips files that already exist,
so even if `seedSkills` did run again it could not UPDATE a skill, only create
absent ones.

**The spec says the opposite.** `specs/5/28-packages.md:203` lists `skills/<n>/`
as `managed: sync`, and `packages.go:323`'s own comment says "seedSkills layers
into every group". Code, spec and comment disagree, and the code loses.

**No reconcile exists** — checked before claiming. Stock skills have two: the
`MIGRATION_VERSION` comparison in `prepareInput` annotates the prompt "[pending
migration]" every turn, and `/migrate` does a real 3-way merge against
`/opt/arizuko/ant/`. Neither covers packages: `/opt/arizuko/ant/` is the baked
image, not the host `<datadir>/skills/` tree, and that tree is never
bind-mounted into any container, so the agent-side merge cannot see it.

**Severity: high, and structurally worse than `L41`/`L45`.** Same shape — an
operator-facing "apply this" command that silently does not — but `L41` had a
partial reconcile on a later generate and `L45` at least reached new deploys.
Here there is no path at all, ever, for a group that predates the install. A
package shipping a skill fix, a corrected instruction, or new capability wording
freezes at whatever content existed when each group was created.

Untested as well as unnoticed: `TestSeedSkillsPackageLayer`
(`container/container_test.go:938`) calls `seedSkills` directly and proves only
the first copy. The update-into-an-existing-group case has no test.

**SIGNED OFF 2026-08-17, fixed 2026-08-18.** `generate` reseeds package skills
into every existing group (`cmd/arizuko/skills.go:syncPackageSkills`, the
delivery half that `syncProxydRoutes` is for routes), and the package layer is
extracted out of `seedSkills` into `container.SeedPackageSkills` so both callers
run ONE implementation. `MIGRATION_VERSION` was not used: that signal announces
staleness to the agent and asks it to merge, which is right for `ours` content
and wrong here — nothing needs merging when the operator owns no copy.

The overwrite boundary, verified before choosing it rather than assumed:
`<datadir>/skills/<n>/` is written ONLY by `packages install` and deleted only by
`remove` — no operator authoring path, no `.merge-base` entry, so `/migrate`
never touches it and the package owns the directory NAME. That makes it
upstream-managed (`5/28` `managed: sync`) and `cpDirOverwrite` correct for it.
`cpDirFresh` is unchanged and still guards the stock `ours` tree beside it.

Two guards the overwrite needed, neither in the original entry:

- **`.disabled` is honoured**, as it is for stock skills. dashd's per-group skill
  toggle already writes the marker for package skills and nothing acted on it;
  under skip-existing that was merely ineffective, under overwrite it would have
  made disabling impossible.
- **a package skill whose name a shipped skill holds is skipped, loudly.** The
  package layer copies into the same `.claude/skills/` tree, so overwrite alone
  would let a package edit a shipped skill — a shadowing hole skip-existing had
  been hiding.

`TestSeedSkillsPackageLayer` was also asserting on a placement the real caller
never produces (a free-standing `claudeDir`, not the group's own); corrected.


## L46 — a package may grant itself operator authority and claim core routes (2026-08-17, FIXED)

`arizuko packages install <git-url>` applies a package's declared grants and
routes with **no bound on what it may ask for**.

**Grants.** `applyPackageGrants` (`cmd/arizuko/packages.go:210`) validates only
`checkGrantPredicate(row)` — the predicate SYNTAX — then stamps
`GrantedBy = "package:<name>"` and writes the row. Scope and action are never
checked. A package shipping

    [{"principal":"folder:evil","action":"*","scope":"**","effect":"allow"}]

gets instance-wide authority over every folder, because `auth/authorize.go:231`
matches `**` against everything and operator-ness in arizuko is emergent from
exactly that row (there is no operator role to check against).

**Routes.** `applyPackageRoutes` applies any path. A package declaring `/dash/`
or `/mcp` re-points the operator console or the agent socket surface at its own
backend. `collectProxydRoutes` will happily emit it.

**Owner.** After `L45`, `store.ProxydRoute` has an `owner` field and the package
decoder is lenient, so a package can stamp `operator` and opt out of ever being
rewritten by generate. (Flagged by the L45 implementation, deliberately left.)

**Severity.** Not remotely exploitable — it needs the operator to run `install`.
This is supply chain, `curl | bash` class. What makes it a bug rather than a
known property is that packages are DOCUMENTED as a bounded, reversible unit
("remove reverses exactly what the record names"), and the install summary
prints the grants it applies as if they were routine. Nothing signals that
installing a package is equivalent to handing over the instance.

**The rule that is missing** (operator, 2026-08-17): some ground is reserved for
core arizuko and core packages. A package's authority must be bounded by what
the operator granted THE PACKAGE, never by what the package asks for.

**SIGNED OFF 2026-08-17, fixed 2026-08-18.** All three bounds land in
`parsePkgManifests` — the one place a package's rows enter the instance — before
the first byte is written, so a refusal leaves no partial state (the `L44`
guarantee). Spec `5/28` §"A package's authority is what the operator handed it".

1. **Reserved route paths.** `compose.ReservedRoutePaths` DERIVES the set from
   `coreProxydRoutes` (no second list to drift) and `ReservedRouteClaim` applies
   proxyd's own matching rule, so `/dash/mine/` is refused as well as `/dash/` —
   longest-prefix wins, so a sub-path front-doors the console. Gates are ignored:
   `/onboard` is core's whether ONBOARDING_ENABLED is on or not.
2. **Bounded grant scope.** `install` takes `--folder <scope>` (repeatable). `**`
   and `*` are refused whatever the operator named; every other scope must be a
   subset of the bound, decided by `auth.MatchGroups` — the one containment
   matcher — applied to witness paths, since both sides are patterns. No
   `--folder` is a bound of NOTHING, so a package declaring a grant is refused
   with the flag it needs.
3. **A third owner value.** `store.RouteOwnerPackage(name)` → `package:<name>`,
   stamped by install and by generate; a manifest declaring `owner` is refused.
   `package:*` is rewritten by `SyncProxydRoutes` like an arizuko row (generate
   re-derives it from the package's fragment) but keeps its attribution — only
   the operator's own row stays hands-off.

Two corrections to this entry, found while implementing:

- `applyPackageGrants` / `applyPackageRoutes` no longer exist — `L44` replaced
  them with `parsePkgManifests` + the apply engine. The substance held.
- `/v1/` is NOT in `coreProxydRoutes`, so the derived reserved set does not cover
  it, nor the prefixes proxyd hand-wires before `MatchRoute` (`/pub/`, `/auth/`,
  `/health`, `/`). A package route there is dead rather than a hijack — proxyd
  dispatches those first — so it is a truthfulness gap, not an authority one.
  Closing it needs those prefixes declared somewhere derivable; a hardcoded
  second list in `compose` is the wrong answer. Not fixed here.


## L45 — proxyd routes shipped in a release never reach an existing instance (2026-08-17, FIXED)

`loadInitialRoutes` (`proxyd/main.go:248`) is seed-once: if `proxyd_routes` has
any row, `PROXYD_ROUTES_JSON` is ignored. There is no reconcile — the env var is
read at that one call site and nowhere else. So every route arizuko has added to
`coreProxydRoutes` since an instance first booted is absent from it forever.

**Live on krons.** 12 rows in `proxyd_routes`; `coreProxydRoutes` ships 18.
Missing: `/dash/onbod/`, `/dash/timed/`, `/invite/`, `/me`, `/me/`, `/pair/`,
`/panel/`.

Confirmed with a control rather than inferred. An unrouted path falls through to
the static site:

    /definitely-not-a-route-xyz/  302 → /pub/krons/definitely-not-a-route-xyz/
    /invite/                      302 → /pub/krons/invite/      (same = unrouted)
    /panel/                       302 → /pub/krons/panel/       (same = unrouted)
    /slink/                       404                            (routed; backend answers)

So the invite surface, device pairing, `/me`, the panel and the federated
onbod/timed dashboards are silently absent on the deploy target. Same class as
`L41`: a config path that stops applying and looks healthy.

**The design intent is legitimate** — the comment says "operator mutations are
durable across restarts", and clobbering an operator's runtime route on every
redeploy would be worse. The fault is the mechanism, not the goal.

**The rule it breaks already exists.** Root `CLAUDE.md`: "Business features
(gates, grants, onboarding) are DB-backed with CLI + chat command for
management. Infra (ports, timeouts, images, paths) stays as env vars in `.env`."
Routes are business config — operators mutate them at runtime — so they were
never env-var material. The operator states the general form: **the DB is
authoritative and the only way to persist; env is for purely static config.**

**SIGNED OFF 2026-08-17.** Arizuko- and package-owned routes are authoritative
and overwrite on regenerate — an operator does NOT get to edit them in place,
and must be told so. A separate override mechanism is required so an operator
can still win deliberately; editing the row is not that mechanism.

Proposal: `arizuko generate` writes core routes as rows,
upserting by path so an operator's own routes and edits survive, and
`PROXYD_ROUTES_JSON` is deleted as a config source. One authority, no seed-once
window. Ties to `L44`, which removes the parallel `*-routes.json` format feeding
the same env var.

**FIXED 2026-08-17.** `arizuko generate` writes the route table into
`proxyd_routes` (`store.SyncProxydRoutes`, called from
`cmd/arizuko/routes.go`); `compose.Generate` returns the routes instead of
emitting them, and `PROXYD_ROUTES_JSON` is gone from both compose and proxyd's
boot path — `loadInitialRoutes` reads rows only. A new `owner` column carries
the authority: `arizuko` rows are rewritten by every generate, `operator` rows
are never touched at any path. The override is ownership, not editing — PATCH on
an arizuko-owned row answers 409 naming the way through (DELETE, then POST,
which stamps `operator`). Migration `routd/0038` (mirrored in `store/0087`)
backfills existing rows to `arizuko`, so the shipped paths get reclaimed while a
hand-added route at a path arizuko does not ship (krons `/arizuko`) is never
read. Residual, recorded deliberately: generate never deletes, so a route that
stops being shipped, or whose `gated_by` is later turned off, stays until the
operator removes it.

**This is also the unlock for `6/16`'s recorded direction** — "shrink toward
off-the-shelf and publish the grant-scoped mount contract", where proxyd is
called "the commodity half — Caddy/Traefik plus Authelia's job, and the weakest
position in the repo". That decomposition needs routes to be rows first. With
them: the routing TABLE is data, the PROXY is off-the-shelf with its config
rendered from those rows (one more reader, per the `one declaration, many
readers` rule), and what stays arizuko's is the forward-auth endpoint the proxy
calls per request — `auth.MatchSlot` folder scoping, which is the one thing
`6/16` names irreplaceable. Three orthogonal pieces instead of one 1,591-line
daemon doing all three.


## L44 — packages declare in two bespoke formats what the manifest already carries (2026-08-17, FIXED)

`5/8`'s manifest carries "ACL, routes, secrets metadata, scheduled tasks, proxyd
routes, web routes, network rules" and `apply` installs it. Packages re-declare
two of those in their own formats, each hand-parsed with its own types:

    <name>-grants.json   → applyPackageGrants  (cmd/arizuko/packages.go:195)
    <name>-routes.json   → applyPackageRoutes  (cmd/arizuko/packages.go:266)
                         → collectProxydRoutes (compose/compose.go:434)

Root `CLAUDE.md`: "If it exists, fix/extend the ORIGINAL; NEVER add a parallel
second path — two paths drift."

**Three costs, all live:**

1. **A package cannot declare what the manifest supports** — no scheduled task,
   network rule, web route or secrets metadata, though `apply` understands all
   four.
2. **Install is not transactional.** `applyPackageGrants` calls `die()` inside
   its loop, so grant 3 of 5 failing leaves 1 and 2 applied and the process
   gone. `apply` already has per-subsystem transactions with **pre-image
   rollback** (`5/8`) — packages hand-roll a worse version beside it.
3. **Routes land in two places** — a generate-time env var via the JSON sidecar,
   and `proxyd_routes` rows via hot-apply.

**Proposal (needs sign-off — redesign, not patch).** A package ships two files,
both in formats that already exist:

    <name>.yml     docker's compose fragment (not our format; unchanged)
    <name>.yaml    the 5/8 manifest — grants, routes, tasks, network rules

Delete both JSON formats and their parsers. `apply` installs the rows and brings
transactional install plus rollback with it. Compose generation reads routes
from the manifest instead of the sidecar — the routes still become one proxyd
env var at generate time (`collectProxydRoutes` documents why they must), so
this is one declaration with two readers, which is the project's own
one-renderer-many-sinks rule rather than a second path.

`installed_packages` is unchanged: it already records owned rows (`Manifest`)
and content hashes (`AssetHashes`), which is the dpkg half arizuko got right.

**SIGNED OFF 2026-08-17.** Hard cutover: existing installs are rewritten and
adapted. No dual-read, no backwards compatibility anywhere.

**FIXED 2026-08-17.** A package ships `<name>.yml` (docker's, unchanged) and
`<name>.yaml` (a `5/8` manifest). Both JSON formats and both parsers are gone,
along with `applyPackageGrants` / `applyPackageRoutes`.

- **Install goes through the `apply` path.** `installPkgRows` merges the
  package's rows onto the subsystem's live projection
  (`resreg.Resource.MergeRows`, new) and hands the whole document to the same
  `applyDocs` `arizuko apply` uses — one transaction per owner DB, pre-image
  rollback, in-tx checksum CAS. The manifest is parsed and validated before the
  first byte is written, and a refusal after that unwinds the fragment files, so
  an install is whole or nothing. `remove` is the mirror: `DropRows` of exactly
  the PKs the record names, so an operator's own row at another key survives.
- **The record is uniform.** `installed_packages.manifest` keys on the resreg
  resource name (`acl`, `proxyd_routes`, `scheduled_tasks`, …) with the PK
  strings of the rows the package owns; `compose_fragment` and `skill` are the
  only non-resource keys. The old `grant` / `proxyd_route` keys are gone — a
  record still carrying them makes `remove` refuse loudly and name the reason
  rather than under-remove.
- **Compose reads the same file.** `collectProxydRoutes` →
  `compose.ManifestRoutes` parses `services/<name>.yaml` with
  `resreg.ParseYAML`, so generate and install share one parser and one
  declaration. `template/services/slakd-routes.json` became `slakd.yaml`.
- **Two knock-on findings.** (1) `resources.ACLRow` omitted `grant_option`, and
  acl has no folder scope, so ANY apply mentioning acl rebuilt the table and
  reset every holder's WITH GRANT OPTION to 0 — a pre-existing `arizuko apply`
  hole this change would have put on the package path. The column is in the row
  type now. (2) The per-grant / per-route audit rows (`Q5`, `F2`) collapse into
  the one `config.apply` row apply emits; `resreg.ApplyOpts.Extra` (new) carries
  the package name and the PK of every row added or withdrawn, so the row still
  names what it handed out. Whole rows do not fit — `params_summary` caps at 512
  bytes.
- **Not done, deliberately:** `L46`. The merge is a single choke point, so
  bounding what a package may ask for is one hook at
  `cmd/arizuko/packages.go:parsePkgManifests`, and `ManifestRoutes` already
  drops the `owner` column on the compose side.

## L43 — 44 stale spec references in the live tree (2026-08-16, PARTLY FIXED 2026-09-20; the `6/7` family remains)

A reference-graph check over tracked `.md`/`.go` (excluding `CHANGELOG.md`, which
is history and correct as written) found **44 distinct spec IDs that no longer
resolve**. They sit in the documents a reader trusts most:

    ROUTING.md          6/F, 6/J, 10/17
    SECURITY.md         6/J, 9/14, 10/17
    ARCHITECTURE.md     5/H, 7/D, 7/F
    README.md           8/Z, 9/3
    BUGS.md             6/2, 8/3, 7/00 (this file)
    core/types.go       5/U, 6/F        store/messages.go   6/F, 6/J
    ipc/README.md       7/7, 7/Y, 8/Y   resreg/README.md    8/F

Not all are errors: `8/Z` in `README.md` is a deliberate forward reference to a
spec phase 8 named and never wrote. The rest are
residue from earlier renumbers — the lettered phase-6 IDs (`6/B`, `6/D`, `6/E`,
`6/F`, `6/J`, `6/R`, `6/Y`) are the largest family.

**Why this blocks the renumber, and why it is the real finding.** A dangling
reference is detectable: follow it, get nothing, notice. But the proposed re-cut
moves the phase-7 cockpit specs INTO phase 6's number space, and stale `6/2`
references already exist in `BUGS.md`, `proxyd/resource_test.go` and migration
`119`. Renumbering `7/2-dashd-hub` to `6/2` makes every one of those resolve —
to a spec about something else entirely. Dangling pointers become confidently
wrong pointers, and the class stops being detectable at all.

So the order is fixed: repair the graph first, renumber second. Repairing is
cheap and independently worth doing; renumbering onto it is then safe.

**PARTLY FIXED 2026-08-27.**

- **The 12 pointers a reader trusts most were retargeted in place** — a live
  document must name the live spec. `ARCHITECTURE.md` and
  `ant/skills/oracle/SKILL.md` dropped `5/H` (the oracle ships as code, which is
  why the spec was deleted); `core/types.go` + `types/identity.go` moved to
  `5/A`; `slakd/README.md` to `8/D`, where the pane spec shipped after the
  2026-07-10 phase shift; `ipc/README.md` to `5/16` and `5/13`. Four more —
  `README.md`, `resreg/README.md`, `audit/PLAN.md` — had a correct link TARGET
  under stale link TEXT, so a reader who clicked arrived while a reader who read
  was misled.
- **`tests/specrefs` gates the link half**: every markdown link under `specs/`
  must resolve. Mutation-tested; it missed its first mutation because the regex
  did not match the `(6/24-name.md)` form, and was fixed before shipping.
- **A retired-ID table was built and REJECTED by the operator** (100 rows from
  git history: what each retired ID was, its lifespan, its successor). It made
  every stale ID resolve, at the cost of a graveyard file the corpus has to
  carry. Decision: delete it. The renumber went ahead without it.

**What stays open.** Roughly 70 spec IDs across the tree still resolve to
nothing, and nothing checks them. Most sit in history that is correct as
written (`CHANGELOG.md`, `.diary/`, migrations) and must not be rewritten. The
live-tree remainder is small and can be deleted ref-by-ref when someone is in
the file anyway. The renumber rule that came out of this is recorded in
`specs/index.md`: **a retired number is burnt** — `specs/6/1` has been five
different specs, `specs/7/2` six, and reusing one is what turns a dangling
pointer into a confidently wrong one.


- **2026-09-20, five families repointed — 36 sites.** Each destination was read
  and its title matched against what the citing site claims, not guessed:
  - `6/F` → `5/F` (topic lineage: forks + per-topic observed cursor), 11 sites
  - `6/J` → `5/L` (routd-side reply-to-bot → `verb=mention` promotion), 9
  - `10/17` → `11/17` (emaid sender authentication + quarantine), 8
  - `7/F` → `5/F`, 4 · `7/D` → `8/D` (Slack agent pane), 3
  - `8/15` → `11/15` in `dashd/channels.go`, whose own line calls itself the
    operator-only re-pair surface — which is `11/15`'s title.

  `BUGS.md`, `.diary/`, `CHANGELOG.md` and `ant/skills/self/migrations/` were
  NOT touched: those record what was true when written.

- **The guard gap is closed, and it was real.** `tests/webdocs`'s `specIDRe`
  required whitespace after "spec", so the `Spec:&nbsp;7/F` colon form slipped
  past it — on `reference/mcp.html`, the one page a public reader hits, five
  times. The separator class now accepts a colon. PROVEN BY MUTATION: put one
  site back to `7/F` and the test names the file and the citation.

- **A stale warning removed, because acting on it would have caused a wrong
  rewrite.** `specs/11/index.md` warned that `5/34` in code meant cost caps.
  RE-MEASURED: all eleven `5/34` sites are about dedup, outbound resolution and
  route targets — exactly what `specs/5/34 — Channel adapter protocol` holds.
  The citations are correct and the note was not. It also named `8/16` where
  the code says `8/15`.

- **Left: the `6/7` family, 36 sites and the largest.** It is NOT a scripted
  rewrite: `6/7` was retired into `6/1`, `5/9`, `5/16` and `6/16`, so each site
  needs the one that holds its claim. That is judgement per hunk, which is what
  the `blind_id_rewrite` rule exists for.

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

## L41 — an instance's compose fragments freeze forever once they differ by a byte (2026-08-16, LOUD NOW, auto-repair needs sign-off)

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
- **Status:** warning shipped; auto-repair proposed
- **Fix:**

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
- **Status:** open — needs the edge's `X-Forwarded-For` behaviour confirmed
  before anything is trusted
- **Fix:**

## L39 — whapd acks WhatsApp before routd accepts, the TS half of L25 (2026-08-15, open)

Found while fixing `L25`, which named a sibling entry `L20` for this — `L20`
does not exist in this file, and the in-memory queue it described
(`whapd/src/queue.ts`) was deleted before that pass. The defect itself is real
and still open.

`whapd/src/main.ts` calls `sock.readMessages([msg.key])` — the WhatsApp read
receipt — BEFORE `await rc.sendMessage(payload)`, and the `catch` around the
send only logs `deliver failed`. Baileys will not hand the message over again,
so a routd that is restarting loses it, exactly as slakd did.

`L25` shipped the durable answer as `chanlib.RouterClient.RetainInbound` (a
per-adapter `inbound_pending` table plus a sweep), wired into slakd and discd.
whapd is TypeScript and cannot use it. The fix is the same shape in TS and
should share whatever durable store `L32` picks for the outbound ledger — one
adapter-local database, two tables, not two bespoke stores.

- **Severity:** high — same class as `L25`, on the adapter krons runs as its
  primary channel
- **Scope:** whapd inbound durability
- **Affected:** every WhatsApp message arriving while routd is restarting
- **Source:** `whapd/src/main.ts` (`readMessages` then `rc.sendMessage` in a
  log-only `catch`); contrast `chanlib/inbound.go`
- **Status:** open — do it together with `L32`, which has to choose the same
  durable store (node:22 has no stable `node:sqlite`).
- **Fix:**

## ✅ FIXED 2026-08-15 — L32 — whapd ignores `idempotency_key`: the one adapter still outside the D5 dedup contract (2026-08-15)

Every Go adapter claims/settles routd's `idempotency_key` in a durable
per-adapter ledger (`chanlib/dedup.go`), so a sweep resend of a delivered row
posts nothing. whapd (TS, node:22) drops the field on the floor: a routd
resend after a lost `/send` response double-posts on WhatsApp — exactly the
D5 class, one adapter left. Spec `5/34` now names whapd as outside the
contract. Fix shape: a durable claim/settle ledger in whapd's own state dir
(node:22 has no stable `node:sqlite`, so file-per-key with `O_CREAT|O_EXCL`
as the atomic claim, or a runtime bump), wired into `/send` before
`sock.sendMessage`, plus a Baileys error taxonomy call — which of its errors
prove nothing was sent (release the claim, per `chanlib.ErrNotDelivered`)
vs ambiguous (hold). ~100 lines TS + tests; the taxonomy decision is why
this needs a look rather than a mechanical port.

- **Severity:** medium
- **Scope:** outbound delivery / channel protocol
- **Affected:** whapd (krons runs it as the primary channel)
- **Source:** whapd/src/server.ts `/send` (no idempotency_key read)
- **Status:** FIXED — `whapd/src/dedup.ts` claims the key before the Baileys
  call and settles it after, wired into BOTH `/send` and `/send-file` (the
  entry only named `/send`; routd sends the key on both, `chanreg/httpchan.go`
  writes the `idempotency_key` multipart field). Every adapter is now inside
  the D5 contract and spec `5/34` says so.

  **Store: a directory of files, one per key** — the entry's first option, and
  it needs no runtime bump. node:22 has no stable `node:sqlite`, and the only
  operation that must be atomic here is the CLAIM, which `open(path, 'wx')`
  (`O_CREAT|O_EXCL`) already is at the filesystem level. Keys are
  `encodeURIComponent`-encoded: a routd message id carries `/` and `:`, and a
  raw key as a filename would escape the ledger directory. It lives under
  `WHAPD_STATE_DIR`, and `template/services/whapd.yml` now mounts it — an
  unmounted ledger forgets every claim on recreate, which is the same as
  having none.

  **The Baileys error taxonomy decision — the answer is ORDERING, not error
  parsing.** Baileys exposes no signal that separates "WhatsApp refused it"
  from "the ack was lost", so whapd never guesses: it HOLDS the claim on any
  error after the platform call and answers `409` on the next attempt, which
  routd dead-letters for an operator rather than double-posting.

  What stops that from turning every plain transient into a guaranteed
  dead-letter — the failure `chanlib.ErrNotDelivered` exists to prevent — is
  the CLAIM ORDER, not a classifier: `requireSock` refuses a send when the
  socket is not open, BEFORE any claim exists. So a routd restart or a dropped
  link answers 502 with nothing recorded and the sweep retries freely. That is
  the whole class of proven-non-delivery whapd can actually observe, and it is
  handled where it is observable.
- **Fix:** 44677153

## ✅ FIXED 2026-08-15 — L33 — a one-shot task's retry mints a fresh message id, so a lost response fires it twice (2026-08-15)

`timed.fireSplit` enqueues each due task with `id = core.MsgID("sched-"+t.ID)`
— a `UnixNano` suffix, fresh per attempt. When the `POST /v1/messages`
response is lost but routd recorded the row, timed's error path reschedules a
one-shot (`cron=""`) to fire again in 1 minute with a NEW id, and routd's
id-PK dedup never matches: the task fires twice. Cron tasks are unaffected
(the reschedule targets the next natural slot, which should fire anyway).
The exact fix needs a stable per-fire identity that survives the reschedule —
the claimed `next_run` carried through `/v1/tasks/due` and preserved on the
error-path reschedule, or a fire_id column; a bare `sched-<taskID>` key would
instead silently dedup a legitimately re-armed one-shot.

- **Severity:** low
- **Scope:** scheduled tasks / ingress idempotency
- **Affected:** timed → routd
- **Source:** timed/split.go:184 (`core.MsgID`), core/types.go:275
- **Status:** FIXED — the claimed `next_run` is the fire identity, as this entry
  proposed; no `fire_id` column was needed.

  `GET /v1/tasks/due` now returns `next_run` (RFC3339 UTC, unchanged by the
  claim) and `timed.fireID` mints `sched-<taskID>@<next_run>` instead of
  `core.MsgID`. Every attempt at one fire carries one id, so routd's
  `INSERT OR IGNORE` on `messages.id` collapses them.

  The error path is what makes that hold: a one-shot is re-armed to the
  next_run it was CLAIMED at, not a fresh `now+1m`. That value IS the identity,
  so the next tick re-claims the same fire with the same id. The 60s fire-loop
  cadence preserves the old one-minute retry exactly — the backoff was never
  the `now+1m` value, it was the tick. Cron tasks keep taking their next
  natural slot: the missed fire and the next one are different fires.

  A task with no `next_run` falls back to the wall-clock id. A stable-LOOKING
  id that is not actually per-fire would dedup two legitimate fires into one,
  and a silent drop is worse than a duplicate.
- **Fix:** a56845c7

## ✅ FIXED 2026-08-15 L38 — the product catalog and the product corpus are two different sets (2026-08-15)

`ant/examples/` ships 11 products; `template/web/pub/arizuko/products/` documents
9. They are not the same 9.

- **Shipped, undocumented (6):** `creator`, `personal`, `pm`, `socials`,
  `strategy`, `support`. An operator running `arizuko products <inst> list` sees
  names that appear nowhere on the site.
- **Documented, unshipped (4):** `company-brain`, `discord-team`, `marble`,
  `supermarket`. Pages describe products with no tree behind them. `supermarket`
  at least says so ("No template ships for this one yet"); the other three read
  like something you could install.

Only 5 exist on both sides. `template/web/CLAUDE.md` §"Adding a new product"
already prescribes the two-page + grid-row ritual, so the process exists and was
not followed — this is drift, not a missing convention.

- **Severity:** low — nothing breaks, but the catalog is the first thing a new
  operator reads and roughly half of it is wrong in one direction or the other
- **Scope:** product catalog docs vs `ant/examples/`
- **Affected:** anyone choosing a product from the site
- **Source:** `ls ant/examples` vs `ls template/web/pub/arizuko/products`
- **Status:** FIXED — pages written, not products dropped. Every shipped tree is
  something an operator can seed today; deleting six working templates to make a
  docs assertion pass would be the wrong direction.
- **Fix:** six page pairs written (`support`, `personal`, `pm`, `creator`,
  `socials`, `strategy`) from each product's `PRODUCT.md` + persona + facts, each
  with an intro and a setup page; the six moved from the "Templates without a
  page" list into the grid, and that section is gone. The four documented-only
  products keep their pages and now carry a "This is a shape, not a template"
  note at the top, so the page no longer reads like something you can install.
  The `nav-children` list is identical across all 31 catalog pages. Landing-page
  counts corrected ("all eight" → all fifteen).
  `TestProductCatalogMatchesCorpus` is the gate, directional by design: a shipped
  product MUST have both pages (a name `products list` prints with no page is a
  dead end), a documented product with no tree MUST carry the shape note. Both
  halves checked to fail. `template/web/CLAUDE.md` §"Adding a new product" names
  the test and the nav step. Also dropped the stale `L37` warning from
  `products/helpdesk/setup.html` — the two seeders now agree. SHA 52254c51.

## ✅ FIXED 2026-08-15 L37 — a product's skill is dead when seeded by `--product`, live when blended (2026-08-15)

The two seeders disagree about the one payload kind that relocates, so a product
shipping `skills/<name>/` works one way and silently does not the other.
Reproduced on a throwaway instance:

| Path | Where the skill lands | Agent loads it? |
| ---- | --------------------- | --------------- |
| `arizuko products <inst> apply <folder>` (blend) | `.claude/skills/triage/SKILL.md` | yes |
| `arizuko group add … --product helpdesk` (verbatim `SetupGroup`) | `skills/triage/SKILL.md` | **no** |

`container.blend.go` relocates a product's `skills/<n>/` under `skillsRel`
(`.claude/skills`) because that is where the agent looks; `seedSkills` then
layers the stock tree in beside it. The verbatim path copies the product tree as
it stands — by design, and locked byte-for-byte by `TestProductSeedIsVerbatim` —
so the directory stays at the group root, where nothing reads it.

Consequence: `arizuko create <name> --product <p>` and `group add --product <p>`
— the commands the whole product catalog tells operators to run — seed a skill
into a dead location. No corpus product shipped `skills/` until 2026-08-15, which
is why this never surfaced: the divergence existed the whole time with nothing to
expose it.

Found by running the command I had just written into
`products/helpdesk/setup.html`, which claimed both paths "end with the same files
in the same places". They do not.

- **Severity:** medium — a shipped payload kind silently reaches no consumer on
  the more prominent of the two paths
- **Scope:** `5/28` composition vs `5/21` product seeding
- **Affected:** any product shipping `skills/`; today only the `helpdesk`
  reference product
- **Source:** `container/blend.go` (`skillsRel` relocation) vs
  `container/runner.go` `SetupGroup` → `chanlib.CopyDirNoSymlinks`
- **Status:** FIXED — both seeders now share one placement rule.
- **Fix:** `container.productDest` is the single rule mapping a product-relative
  path to its group destination; `blendSkills` and the new
  `container.SeedProduct` both call it, and `SetupGroup` uses `SeedProduct`
  instead of a plain `chanlib.CopyDirNoSymlinks` (same symlink discipline, same
  bytes, one placement). Verbatim now means byte-identical, not
  layout-identical. `TestProductSeedIsVerbatim` grew the relocation exemption
  and also asserts the file is NOT left at the group root — one placement rule,
  not two — and `TestBothSeedersPlaceAlike` pins the two seeders to the same
  destination set, checked to fail against the old copy. Spec `5/28` blend table
  + §single-product updated. SHA adcbc223.

## ✅ FIXED 2026-08-15 L36 — `packages install` applies grants without saying so (2026-08-15)

Installing a package that ships `<name>-grants.json` prints:

```
installed helpbot@local (2 file(s), 1 route(s)) — run `arizuko generate lab` to apply
```

The grant IS applied and IS attributed (`acl.granted_by = 'package:helpbot'`,
verified in the lab), and `Q5` already made the audit trail per-grant for exactly
this reason: "a bundle row would say 'installed ttsd' and hide that the install
handed somebody admin on a folder". The audit row is right; the operator's
terminal is not. Files and routes are counted, grants are silent — the one asset
kind that changes who can do what is the one the summary omits.

- **Severity:** low — no wrong state, but the operator is not told about the
  change most worth telling them about
- **Scope:** `cmd/arizuko/packages.go` install reporting
- **Affected:** any package shipping `*-grants.json`
- **Source:** the install summary line in `cmd/arizuko/packages.go`; contrast
  `applyPackageGrants`, which returns each applied row for the record
- **Status:** FIXED.
- **Fix:** the summary counts grants beside files and routes, then prints one
  line per grant naming effect, principal, action and scope (plus params and
  `WITH GRANT OPTION` when set) — `describeGrant` in `cmd/arizuko/packages.go`.
  `applyPackageGrants` returns `[]core.ACLRow` instead of pre-marshalled JSON so
  the caller can both record and report from one value; the record's bytes are
  unchanged, so `remove` still reverses exactly. Files and routes are inert
  until `generate`, a grant is live the moment it lands — that is why it is
  named and not counted. `TestPackageInstallSummaryNamesEachGrant` +
  a lab assertion on the printed line. SHA 9bc40ad4.

## ✅ FIXED 2026-08-15 L35 — `tasks.toml` is a product payload nothing reads (2026-08-15)

`specs/5/21:56` says a product ships "`tasks.toml` (seed scheduled tasks)", and
`5/28`'s blend table gives it a union rule. **No code reads it.** The only
mention of the filename in any `.go` or `.ts` file is `container/blend.go:83`,
the classify rule that decides where to COPY it. There is no parser, no schema
type, no `seedTasks`, nothing.

Scheduled tasks are the `scheduled_tasks` resreg resource
(`resreg/resources/scheduled_tasks.go`) — DB rows created through the MCP tool,
the REST face, or the CLI. A file has never been an input to them.

So a product author following `5/21` writes a `tasks.toml`, `arizuko products
apply` reports success, the file lands in the group home, and no task is ever
scheduled. Found while building a reference product for the docs: I wrote one,
then went looking for the schema to document and there was none.

This is the third instance of one class, and the other two were both closed by
refusing the payload: `mcpServers` in `settings.json` (`J15`) and a product's
`migrations/` (`5/28`, refused 2026-08-15). The rule those set is that a payload
which applies cleanly and reaches no consumer is an authoring error, not an
asset.

- **Severity:** medium — silent no-op on an operator-facing path, and a spec
  actively instructs people to use it
- **Scope:** `5/28` composition, `5/21` product manifest
- **Affected:** any product author following `5/21`; no corpus product ships one
  today (asserted by `container/product_seed_test.go`)
- **Source:** `container/blend.go:83`; `specs/5/21-products.md:56`;
  `specs/5/28-packages.md:206`
- **Status:** FIXED — **REFUSED**, the same exit as `mcpServers` and
  `migrations/`. Three reasons, in order of weight. (1) *It is not the product's
  to own*: a scheduled task is a `scheduled_tasks` row with a real owning
  handler on all three faces, so a file input would be a fourth writer of that
  table whose only caller is the seeder — the one-renderer rule forbids it.
  (2) *The apply could not be reversed or re-applied*: `Manifest` maps asset
  kinds to identities and `AssetHashes` to file content, and a seeded ROW is
  neither, so `products remove` could not withdraw it and upgrade could not
  re-apply it — that is `L31`, still unsigned, and a new payload would be born
  into it already broken. (3) *The intent keeps a route that works*: a product's
  daily-digest task stays expressible through its `CLAUDE.md` region telling the
  agent to schedule it, the same escape hatch `migrations/` took. Building it
  would have made `5/21`'s sentence true at the cost of a fourth writer and a
  known-broken lifecycle; deleting the sentence makes it true for free.
- **Fix:** `classify` maps `tasks.toml` to a new `payTasks`, and BOTH refused
  payloads now render through ONE `refusedPayloads` map — the old
  `migrationsRefusal` is gone rather than joined by a twin, so a refusal is a
  row and the two seeders cannot drift on what is refused. Every refusal must
  name the route that works, asserted by `TestRefusedPayloadsNameARoute`.
  `TestBlendRefusesProductTasks` + `TestCheckProductAssetsRefusesWhatTheBlendDoes`
  cover blend and verbatim seeder; the product lab probes it beside the
  `migrations/` and `mcpServers` refusals. Specs `5/21` (sentence replaced with
  the refusal + the working route) and `5/28` (blend-table row + a
  §"Seeding a product's `tasks.toml`" decision record). SHA 84df9e82.

## ✅ FIXED 2026-08-15 L34 — handing an asset between two products in one mix DELETES it (2026-08-15)

Reproduced end to end in the product lab, from a clean group:

1. Mix is `[pa]`; `pa` ships `skills/shared/SKILL.md`. Apply → the file is at
   `.claude/skills/shared/SKILL.md`. Correct.
2. `pa` drops the skill, `pb` ships the **identical** file, mix becomes
   `[pa, pb]`. Apply → **the file is gone from the group.**
3. Apply again → `pb` writes it back.

So the asset vanishes for the whole window between two applies, and nothing
re-applies routinely. Worse, the apply that deleted it reported `pb unchanged`
— the operator gets no signal at all, and `installed_packages` records `pb` as
owning a file that is not on disk.

Cause: `BlendProducts` runs `blendSkills` first (pb's content is byte-identical,
so `writeIfDifferent` correctly writes nothing), then calls `dropStale` **per
product with only that product's own hashes**
(`container/blend.go`, `dropStale(groupDir, prior[mix[i].Name], out[i].AssetHashes)`).
`pa`'s prior hashes contain the path, its current hashes do not, and the on-disk
bytes still match `pa`'s recorded hash — because pb ships the same bytes. So
`dropStale` classifies it as "clean and no longer shipped" and removes it.
`dropStale` cannot see that another product in the SAME mix now owns the path.

The same shape produces a false warning when the contents differ: applying the
two-product mix in the lab printed "`.claude/skills/triage/SKILL.md` is no
longer shipped but was edited locally — left in place" when nobody had edited
anything; `oncall` had simply taken ownership from `helpdesk`.

- **Severity:** medium — silent asset loss on a supported operation (changing
  which product in a mix provides an asset), self-healing only on the next apply.
- **Scope:** `5/28` composition, `container/blend.go` `dropStale`
- **Affected:** any group whose `products.toml` mix moves an asset between
  providers; no live instance runs a multi-product mix yet
- **Source:** `container/blend.go` `BlendProducts` → `dropStale`; reproduced
  2026-08-15 with `arizuko products <inst> apply`
- **Status:** FIXED.
- **Fix:** `BlendProducts` computes the union of every product's `AssetHashes`
  once and passes that one `shipped` set to every product's `dropStale`, so a
  path any current provider ships is still shipped. The false "edited locally"
  warning goes with it — ownership handoff stops looking like a local edit.
  `TestBlendHandoffKeepsTheAsset` covers it (checked to fail against the
  per-product hashes) and asserts no warning; the product lab's L34 probe is now
  a pass/fail assertion instead of a known-bug note. Spec `5/28` states the
  union rule. SHA 4d40f601.

## L31 — a product never really updates: four of six payload kinds are seed-once (2026-08-15, proposal — needs sign-off)

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

## ✅ FIXED 2026-08-15 L30 — the SSE hub key is an unwritten string convention both ends re-derive (2026-08-15)

`webd/turn.go` was fixed on 2026-08-15 to subscribe on the real chat JID rather
than the route token's `target_folder` (spec `5/J`). The fix is correct and
tested, but it treats one symptom of a shared cause: **nothing owns the hub
key.** Each end reconstructs it with string surgery, and the two must agree by
convention alone.

Four remaining sites, all verified at `8ef52f47`:

- `routd/turns.go:716` — publishes on `strings.TrimPrefix(tc.ChatJID, "web:")`.
  For a `hook:` chat JID the prefix does not match, so the key stays
  `hook:a/b` while every webd subscriber builds a `web:`-derived key.
- `webd/channel.go:59` — `handleRoundDone` rebuilds `jid := "web:" + req.Folder`
  and looks the topic up by it. A `hook:` chat gets `"web:hook:a/b"`, so
  `TopicByMessageID` returns `""` and the event publishes under an empty topic.
- `webd/route_token.go:195,382` — `handleChatTokenPost` / `injectRouteMessage`
  subscribe and publish on `row.Folder()` (the token's target), while the
  assistant frame arrives through `handleSend` keyed on the chat JID. A
  suffixed token's `wait=` POST never sees its own reply.
- `webd/route_token.go:419,435` — `handleRouteTokenStream` builds
  `"web:" + folder` from the CLIENT-SUPPLIED `group` param and subscribes to
  it, so a suffixed chat is unreachable and the param decides the key.

- **Severity:** medium — each site is a hang, not a leak, but the last one lets
  a request parameter choose a subscription key.
- **Scope:** webd SSE delivery
- **Affected:** every route token minted with a `jid_suffix`, and every `hook:`
  chat
- **Source:** `routd/turns.go:716`; `webd/channel.go:59`;
  `webd/route_token.go:195,382,419,435`; contrast the fixed `webd/turn.go:125-140`
- **Status:** fixed by redesign (signed off): the derivation is ELIMINATED, not
  centralized — the hub key IS the chat JID verbatim (`webd/hub.go`), and the
  routd→webd wire (`/v1/round_done`, `/send`) carries `chat_jid` untouched.
  Every publisher/subscriber passes the JID it already holds (token row, turn
  context, wire field); `/chat/stream` now binds to the token record, so the
  client `group=` param no longer picks a key. A helper both ends must
  remember to call was rejected — a convention with an owner is still a
  convention. Same class as [`L4`](#l4); see the identity-derived-not-read
  rule in `CLAUDE.md`.
- **Fix:** 30a0bf3a

## L27 — five channel platforms ship no output-style file, so their agents format blind (2026-08-15, open)

`specs/5/Y` says WhatsApp, Reddit, Bluesky, Twitter and LinkedIn "keep their
single platform file". None of those files exists. `ant/output-styles/` ships
`slack{,-dm,-channel,-thread,-pane}`, `telegram{,-dm,-group}`,
`discord{,-dm,-channel}`, `email.md`, `web.md` and `README.md` — nothing else.
(Mastodon is correctly absent: `mastd` was dropped 2026-08-15.)

The picker does no host-side existence check by design (`5/Y` §"Name selection
and file loading are separate concerns"), so it names `whatsapp` for a whapd
turn, Claude Code's `readOutputStyle` finds no such file, and the agent answers
with Claude Code's stock formatting — markdown a platform may not render, no
length envelope, no per-surface shape.

- **Severity:** medium — every reply on five live adapters is unstyled; whapd
  and reditd both run today.
- **Scope:** agent output formatting
- **Affected:** whapd, reditd, bskyd, linkd, and the Twitter surface
- **Source:** `ant/output-styles/` (14 files, none of the five);
  `specs/5/Y-output-styles-per-surface.md:33-36`
- **Status:** open. Two halves, and only the first is mechanical: the spec
  sentence is false and must say those platforms ship no file today. Whether to
  AUTHOR the five files is a content decision — each one is a real
  length/markup envelope per platform, not a template — so it wants the user's
  call before anyone writes prose into the agent's mouth.
- **Fix:**

## ✅ FIXED 2026-08-15 — L25 — slakd acks Slack before routd accepts, so a deploy silently drops messages (2026-08-15)

`slakd.handleEvents` answers `200 OK` to Slack BEFORE calling routd
(`slakd/bot.go:405-406`, comment: "ack first; Slack retries on non-2xx, which we
don't want for handled events"). If the routd call then fails, `dispatch` logs
`deliver failed` and returns (`slakd/bot.go:546-548`). Slack has its 2xx, so it
never redelivers. The message is gone, with one ERROR line as the only trace.

`slakd.yml:23` declares `depends_on: [routd]` with no `condition:
service_healthy`, so the documented deploy — `systemctl restart
arizuko_<instance>` — restarts both and opens exactly this window on every
release.

`teled` does the opposite and is correct: `teled/bot.go:212-224` advances its
getUpdates offset ONLY when `delivered` is true, so Telegram redelivers on the
next poll. The two adapters disagree on whether an inbound is durable.

- **Severity:** high — silent user-visible data loss on the documented deploy
  path, on the adapter krons and marinade both run.
- **Scope:** slakd inbound durability
- **Affected:** every Slack message arriving while routd is restarting or down
- **Source:** `slakd/bot.go:405-406,546-548`; `template/services/slakd.yml:23`;
  contrast `teled/bot.go:212-224`
- **Status:** FIXED — the redesign shipped as ONE mechanism in `chanlib`, not a
  slakd-local patch. `RouterClient.RetainInbound(ctx, db)` writes a failed
  inbound to the adapter's own `inbound_pending` table
  (`chanlib/inbound.go`, `chanlib/migrations/0002-inbound-pending.sql`) and a
  15s sweep re-delivers it with a 30s→15m backoff. The ack is untouched: Slack
  still gets its 200 inside 3s.

  Wired into the two adapters that CONSUME their platform event on arrival —
  `slakd` (Slack's webhook ack) and `discd` (the gateway hands each event over
  once). Deliberately NOT wired into `teled`, `bskyd`, `reditd`, `linkd` or
  `emaid`: each already advances its cursor / seen-pointer / `\Seen` flag only
  after routd answers 200, so the platform IS their queue and a second queue
  beside a working one is a second thing to drift.

  Retention covers UNAVAILABILITY, not refusal: a 400/403/413/422 means routd
  read the payload and rejected it, so the row is dropped with an ERROR line
  rather than replayed forever (`permanentIngress`, keyed on the new
  `chanlib.StatusError` rather than on matching error text). The retained row
  carries the adapter's stable message id — routd's `messages` PK — so a
  re-delivery of a row routd already took is an `INSERT OR IGNORE` no-op.
  `GET /health` reports `pending_inbound` while the backlog is non-empty, so
  the failure surfaces where an operator already looks.

  **Correction to this entry's own sibling note.** `L20` does not exist in this
  file — the reference is dangling, and `whapd/src/queue.ts` (the in-memory
  queue it described) was deleted before this pass. whapd's inbound path DOES
  still have the shape (`whapd/src/main.ts` calls `sock.readMessages` to ack,
  then only logs a failed `rc.sendMessage`), but it is TypeScript and outside
  this Go mechanism; filed fresh as `L39` rather than fixed here.
- **Fix:** 054daefd

## ✅ FIXED 2026-08-29 L26 — `make test-race`, the documented pre-tag gate, cannot finish (2026-08-15)

`routd` and `store` both hit `panic: test timed out after 10m0s` under `-race`,
so the target never completes and never gates anything. CI does not run it
either. 0 races were reported in the packages that did finish, so this is a
timeout, not a detected race — but a gate that cannot run is not a gate, and
the release procedure names it.

- **Severity:** medium — no race coverage on the two most concurrent packages
- **Scope:** release verification
- **Affected:** every tag; `routd` and `store` in particular
- **Source:** `make test-race`; reproduced 2026-08-15 during the CTO audit
- **Status:** FIXED 2026-08-29 — the first option, a longer `-timeout`.
- **Fix:** `-timeout 30m` on the `test-race` recipe (`Makefile:74`). go's
  per-package default is 10m and two packages sit just past it, so the target
  died on a panic instead of finishing.

**Measured 2026-08-29 on this tree, every package in the target, 0 races:**

    runed              15.6s   (+ api/v1 1.1s, cmd/runed 1.5s)
    timed               1.2s
    routd             611.7s   (+ cmd/routd 1.0s)   <- panicked at 10m before
    store             856.3s                        <- panicked at 10m before
    authd              18.5s
                    ~25 min total

**What was NOT run: one end-to-end `make test-race` invocation.** Two attempts
were killed by the environment mid-`routd`, both after `runed` and `timed` had
already passed — a killed background command, not a test failure. Every package
the target names passes individually under the new timeout, and the two that
panicked are the two that now finish, so the defect this entry describes is
gone. Run the whole target once at the next tag to confirm the sum.

## L24 — a fresh instance ships with egress isolation OFF, while the docs describe it as the model (2026-08-15, docs half FIXED; the default is still off)

**RE-MEASURED 2026-09-20 and the switch has been renamed since this was
filed.** It is `EGRESS_IMAGE`, not `CRACKBOX_ADMIN_API`: `core/config.go:242`
→ `Config.EgressImage`, and `compose/compose.go:777` gates the egress service
and the per-folder networks on it being non-empty. `container/runner.go:749`
attaches no proxy network when it is empty. The finding itself is unchanged —
`arizuko create` writes six keys and not this one (`cmd/arizuko/main.go:314`),
so a fresh instance's agent containers reach the internet unfiltered, silently.

`SECURITY.md:249` and `ARCHITECTURE.md:23,88,761` describe egress isolation as
the deployment model without saying it is opt-in and off by default. A reader
who follows `INSTALL.md` gets the opposite of what the security page describes,
with no warning at any step.

**Correcting the finding that surfaced this.** The 2026-08-15 CEO memo
(`.ship/critique-ceo-20260815.md`) reads `container/runner.go:184-191` — the
`egress` grant appending `"*"`, which `crackbox/pkg/match/match.go:49` honours
as allow-all — as egress default-deny being "negated by its own grant". That
half is NOT a defect: it is the documented behaviour of the `egress` grant per
spec `5/33`, and the comment at the call site says so. The grant is how an
operator deliberately opens a folder. The real gap is the one above — the
control is off unless an operator knows to switch it on.

- **Severity:** high — a security control the docs present as the model does not
  run on a default install, and nothing says so.
- **Scope:** instance seeding + the security docs
- **Affected:** every instance created from `arizuko create`; readers of
  SECURITY.md and ARCHITECTURE.md
- **Source:** `core/config.go:265`; `compose/compose.go:493,621`;
  `template/env.example` (no entry); `SECURITY.md:249`
- **Status:** proposed — needs a decision, two parts:
  1. **Docs (no sign-off needed).** State plainly in `SECURITY.md`,
     `ARCHITECTURE.md` and `INSTALL.md` that egress isolation is opt-in, name
     the variable, and say what an instance without it allows.
  2. **Default (needs sign-off).** Whether `arizuko create` should seed
     `CRACKBOX_ADMIN_API` so a fresh instance is filtered by default. That
     changes what a new install runs and requires the crackbox image to be
     present, so it is a deployment-behaviour change, not an inline fix.
- **Fix:**

**Re-verified 2026-08-29.** The mechanism this entry named is gone: there is no
`CRACKBOX_ADMIN_API` in `core/config.go` or `compose/compose.go`, and
`runed/cmd/runed/main.go` REFUSES TO START if it is still set. The switch is now
`EGRESS_IMAGE` (`core/config.go`, default `""`).

The **docs half is closed** — `SECURITY.md` and `ARCHITECTURE.md` both phrase
isolation conditionally ("When `EGRESS_IMAGE` is set…"), so nothing describes
as the model something a fresh instance does not have.

**Still open:** `template/env.example` carries no `EGRESS_IMAGE` and no
`cmd/arizuko` path writes one, so a fresh instance is still unfiltered by
default. Turning that on by default is a deployment-behaviour decision and
still needs sign-off.

- **Discoverable half fixed 2026-09-20.** `template/env.example` now carries a
  commented `EGRESS_IMAGE=` that says in as many words what an empty value
  means: agent containers reach the internet unfiltered. An operator reading the
  file they are told to copy now sees the switch and its default.
- **Still open, and it needs the sign-off the entry asks for:** flipping the
  default so a fresh instance is isolated unless the operator opts out. Both
  live instances set `EGRESS_IMAGE`, so no running instance is exposed today.

## L13 — `secret_use_log` is write-only: no read surface on any face (2026-08-11, proposed 2026-08-15 — needs sign-off)

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

## L15 — dead-code sweep 2026-08-11: the hits deliberately REJECTED (record, not a bug)

Not a defect. This is the audit's negative result, kept so the next sweep does not
re-report the same ~135 hits. `deadcode ./...` gave 149 hits, 56 under `-test`;
nine survived verification and are filed above as `L6`-`L14`.

- **Status:** record only — nothing to fix

Production run: 149 hits. With `-test`: 56. Buckets and verdicts:

- **crackbox/** (~67 prod hits; 48 survive `-test`: `pkg/host` + `pkg/host/internal`,
  `pkg/admin.NewAPI`, `pkg/dns Server.LocalAddr`, `pkg/match.LooksLikeDomain`) —
  REJECTED: crackbox is a shipped standalone component with external users
  (`specs/6/8`; brief's false-positive #2). Reachability from arizuko's mains is
  the wrong root set for a public library. Not adjudicated here: whether some
  `pkg/host/internal` helpers are dead even relative to crackbox's public API —
  out of scope for an arizuko audit, flag to a crackbox-scoped pass if wanted.
- **tests/testutils** (28 hits: FakeChannel/FakePlatform/NewInstance/etc.) —
  REJECTED: shared test library; the 8 that survive `-test`
  (Name/Connect/Disconnect/Forward/Quote/Repost/Dislike/Edit) are interface-
  satisfaction stubs, live through the Channel interface contract.
- **resreg/resregtest** (8 hits) + `resreg/engine.go:1311 reset` ("Test-only." by
  its own comment) — REJECTED: test harness by design.
- **routd/db.go:87, runed/db.go:61, store/store.go:178 `OpenMem`** — REJECTED:
  conventional in-memory constructors for tests, colocated with the real Open.
- **runed/runtimes.go FakeRuntime.Run/Kill** — REJECTED: fake in a prod file so
  other packages' tests can import it; standard pattern.
- **queue SetSignalContainerForTest / SetActiveForTest** — REJECTED: named test
  seams (the *unnamed* dead queue surface is filed above).
- **proxyd/resource.go:114 installManualWeb** — REJECTED: self-labeled
  "test-only entry point" in its own doc comment.
- **chanlib isStubVerb + CapImplReport** — REJECTED: consumed by nine adapter
  test suites (`emaid/bskyd/slakd/discd/mastd/reditd/linkd/teled/caps_test.go`,
  `chanlib/extra_test.go`) — the drift-guard is doing exactly its intended job
  at test time.
- **chanlib NoFileSender.SendFile** — REJECTED (with note): unused mixin, but
  its siblings `NoVoiceSender` (5 adapters) and `NoPinSupport` (2) are embedded;
  it is the deliberate mixin family's spare slot for the next file-less adapter.
  4 lines; deleting buys nothing.
- **core/types.go RouteTarget.String** — REJECTED: fmt.Stringer convention;
  absence of any `%v` path over a RouteTarget value is not provable by grep, and
  a wrong deletion breaks log formatting silently.
- **groupfolder/folder.go:34 IsTopLevel** — REJECTED: documented shape predicate
  of the leaf library (deliberately renamed from IsRoot with a semantics note),
  test-covered; 1 line.
- **auth/surrogate/engine.go:83 NewEngineWith** — REJECTED: own comment says
  "tests point a…" — a named test seam.
- **auth/jwks.go:50 NewKeySet** — REJECTED as dead code (test convenience), but
  its stale "used in-process by authd" comment is filed in the wrapper-atrophy
  entry above.
- **chanlib/handler.go:252 NoFileSender / core RouteTarget / audit loadCursor+cursor.get/set/save** —
  cursor family NOT rejected: filed inside the audit-messages entry (same
  deleted chain).
- **auth/jwt.go:29 mintJWT** — NOT rejected: filed as the HS256 entry.
- **routd/tokens.go (5), routd/db.go (4), routd/sibling_db.go (2),
  routd/pending_actions.go (2), routd/loop.go StopQueue, queue
  RegisterProcess/StopProcess/SetHasPendingFn, runed/manager.go intersect,
  obs (3), audit emitMessage** — NOT rejected: filed above.

# Not determined (UNVERIFIED)

- ~~Whether any live instance sets `AUDIT_WEBHOOK_URL_MESSAGES`~~ — MOOT
  2026-08-15: the knob and its producer-less stream are deleted (`L11`). No
  setting of it ever produced an event.
- ~~`authd/http.go:40-43`'s claim that service:runed's grants entry gates a
  per-turn downscope~~ — RESOLVED 2026-08-15 while fixing `L8`: the claim was
  FALSE. runed calls `auth.ServiceToken` nowhere and mints nothing, so the
  grant gated no call. Grant, comment and dead plumbing are deleted.
- Whether the 2s poll fully substitutes for the queue's unwired
  same-group-restart (`hasPending`) in production load — behavior inference
  from code, not observed live.

## L2 — a delegated child is never dispatched: its only message is a bot row (2026-08-08, CAUSE ISOLATED 2026-08-15, fix needs sign-off, REPRODUCED 2026-08-16 on v0.64.0)

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

## J16 — `ant/CLAUDE.md` tells the agent connector tools are deferred; they are eager (2026-08-09, proposed)

"Third-party connector tools (Slack, GitHub, …) do NOT [load eagerly] — they're
deferred behind the **Tool Search Tool**." Connectors are `ExtTools` on the
arizuko server, and that server is `alwaysLoad: true`, so they ride the eager
prefix. `ant/src/mcp-servers.ts` said as much in its own comment before `J1`
touched it. The agent is told to search for tools already in its list, which
costs turns and invites "capability missing" conclusions when a search misses.

Pre-existing, not caused by `J1` — but `J1` removed the last server that COULD
be deferred, so no deferred MCP server exists at all now.

- **Severity:** low
- **Scope:** agent instructions vs MCP assembly
- **Affected:** ant
- **Source:** ant/CLAUDE.md:104
- **Status:** proposed
- **Fix:** either drop the paragraph, or split the routd-side MCP server so
  connector tools really are deferred (`5/17` names the split as the cost).
  Editing `ant/CLAUDE.md` fires the release path — migration file,
  `MIGRATION_VERSION` bump, agent image rebuild — so it rides a release, not a
  doc commit.

## F75 — three skill trees are triplicated and one pair has already drifted (2026-08-08, open — verified 2026-08-15, still a redesign)

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

## ✅ FIXED 2026-08-16 — F53 — live krons teled has no state mount, so its offset resets every recreate (2026-08-07)

Closed by `L41`, which this entry had already diagnosed a week earlier ("worth
a fleet-wide fragment diff … relink only replaces fragments that are still
identical catalog copies") and which then went unacted-on until tonight's whapd
outage forced it. The generalisation, the shipped warning and the auto-repair
proposal are all in `L41`.

Live on krons 2026-08-16: the current template is installed, so the mount and
`DATA_DIR` are present, and the fragment is now a symlink to the catalog rather
than a copy that can fall behind again.

    /srv/data/arizuko_krons/store/teled -> /srv/app/home/store/teled
    DATA_DIR=/srv/app/home/store/teled
    RUNED_RUN_TIMEOUT=20m

The same install applied `T1`'s typing timeout, which had shipped to the
template and never reached the instance, and moved ttsd/kokoro onto their
scoped env files — kokoro, a third-party image, went from five instance
secrets in its environment to none.

**Note:** `teled-rhias.yml` is an operator variant and is deliberately never
relinked, so it needed the same mount by hand. Added live 2026-08-16 —
`store/teled-rhias` plus `DATA_DIR` and `RUNED_RUN_TIMEOUT`. A variant carries
this cost permanently: no catalog change will ever reach it.

Deployed `/srv/data/arizuko_krons/services/teled.yml` carries neither a state
mount nor `DATA_DIR`; the repo's `template/services/teled.yml` has both. teled's
default dataDir is `/srv/app/home` (`teled/main.go:69`), unmounted for teled, so
`teled-offset-telegram` lives in the container's ephemeral layer and is lost on
every recreate.

The deployed fragment is stale against the repo. Worth a fleet-wide fragment
diff, not just a teled fix — `arizuko generate`'s relink only replaces fragments
that are still identical catalog copies.

- **Source:** deployed `services/teled.yml` vs `template/services/teled.yml`; `teled/main.go:69,78`
- **Status:** open

## F41 — the re-greet cooldown is a rate, not a cap (2026-08-06, open question — NOT a spec defect)

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

## ✅ FIXED 2026-08-15 — F46 — `runed`, `routd` and `authd` all serve `GET /v1/sessions`, for different tables (2026-08-06 — CORRECTED 2026-08-07: three daemons, not two)

**Correction (2026-08-07, during `F33`).** This is THREE daemons, not two.
`routd/server.go:286` also mounts `GET /v1/sessions` (hand-rolled,
`s.handleSessionGet`, reads `core.SessionRecord`). The original entry missed it.
The count matters: it was the reason `F33`'s signed-off "derive the advertised
set by probing paths" approach was rejected and rebuilt as derivation by mount
IDENTITY. Probing would have made BOTH routd's and runed's `/openapi.json`
publish authd's `SessionsRow` schema.

Found while picking a foreign-resource anchor for `runed`'s new doc-vs-mux
guard: `sessions` was unusable as one, because `runed` really does serve that
path. `runed/server.go:44` mounts `GET /v1/sessions` (hand-rolled, reads
`session_log` — agent spawn sessions); `routd/server.go:286` mounts it over
`core.SessionRecord`; `authd/http.go` mounts the resreg `sessions` resource at
the same path (refresh-token families, `SessionsRow`). Three daemons, one wire
path, three unrelated tables.

Since `F33` the doc no longer lies about this — only authd's RegisterREST mount
is documented, and `resreg/openapi_mount_test.go` pins that a hand-rolled mount
at a resource's path documents nothing. The wire-identity collision itself is
what the rename below closed.

It was never reachable as a live fault — the daemons listen on separate
containers, and both squatters were hand-rolled rather than resreg
registrations, so `TestResourceEndpoints_SingleSource` saw nothing. It was the
exact shape the `Name` rule exists to prevent — "a resource's name IS its wire
identity, globally unique" (root `CLAUDE.md`), the rule `proxyd_routes` violated
in 2026-07 — and it would have bitten the moment either surface was federated
behind one host, or `runed`'s spawn sessions became a resreg resource (which
`5/16`'s adoption program would do).

- **Severity:** low today, medium on federation (an aggregator or generated
  client keying on `/v1/sessions` gets whichever daemon it was pointed at)
- **Scope:** runed/server.go, routd/server.go, resreg/resregtest, authd sessions resource
- **Source:** runed/server.go `mux.HandleFunc("GET /v1/sessions", …)`;
  routd/server.go `mux.HandleFunc("GET /v1/sessions", s.handleSessionGet)`;
  authd/http.go `s.mountSessions(m)` → `/v1/sessions`
- **Fix:** ✅ 2026-08-15. `authd` keeps `sessions` — it is the registered resreg
  resource and the one with a REST twin, so it holds the wire identity. `runed`'s
  two reads became `/v1/spawn_sessions` and `/v1/spawn_sessions/recent` (matching
  its `spawns`/`session_log` vocabulary); `routd`'s became `/v1/topic_sessions`
  (its `sessions` table is the `(group_folder, topic)` → Claude-session-id map).
  Not `runs` for `runed`, though `5/5` names that for the tier: `/v1/runs` and
  `/v1/runs/{run_id}` already serve the `spawns` table, and one name over two
  tables is the defect being closed.
- **Cross-daemon path change, and why it needed no sign-off:** the ONE federated
  caller is routd's `SessionResolver` → `runed/api/v1.Client.RecentSessions`
  (`routd/session.go`), both sides in the same binary set and the same
  `docker compose up`. Its contract is already advisory — any transport error or
  non-2xx returns nil and the prompt renders "no prior session" — so a
  mixed-version window degrades exactly like an unreachable runed, never a
  failed turn. `runed`'s plain `/v1/spawn_sessions` had NO caller in the tree at
  all. `authd`'s `/v1/sessions` did not move, so dashd's authd control plane is
  untouched.
- **Cause fix, not just this instance:** `resregtest.AssertServesNoneOf(t,
  daemon, foreign, mux)` took a hand-written foreign list, and none of the six
  daemons' lists named `sessions` — which is how this survived three audits. It
  is now `AssertServesNoForeignResource(t, daemon, mux)` and DERIVES the foreign
  set as the complement of what the mux owns (every registered resource minus
  the ones `RegisterREST` mounted there), so a list can no longer omit the name
  that matters. Falsified against the pre-fix tree: restoring routd's
  `GET /v1/sessions` fails `TestRoutdMux_ServesNoForeignResource` with the
  collision message.

## F37 — three dispatch tests fail under whole-suite load, so the suite cannot gate a release (2026-08-06, open)

Found while verifying `F36`. Two packages failed on two separate whole-suite
runs and passed every isolated re-run:

- `tests/TestFeature_MessageRouting/inbound-to-dispatch` — `bot rows = 0, want
1`, with `ERROR silent turn: clean run delivered nothing` in the log. It
  polls `dispatchedFolder` on a 5s deadline and then asserts the bot row
  immediately; the dispatch was seen, the reply row was not yet written.
  Passed 6 of 6 isolated, and `go test ./tests/ -count=3` clean.
- `slakd` — one whole-suite failure, no `--- FAIL` line captured at all (only
  the package-level `FAIL`), then 20 of 20 subtests PASS isolated and
  `-count=3` clean.

None touches `authd`, refresh rotation, signing keys or sessions; the same
tree ran `53 ok / 0 fail` on three other whole-suite runs, including one
immediately before and one immediately after these.

**Third instance, same package, new test name (2026-08-06, base `9c944875`).**
`tests/TestSplitFederation_InboundToTurnRoundTrip` failed one whole-suite run
(`52 ok / 1 fail`), then passed isolated (0.098s), at `-count=3` on the package,
and on a whole-suite re-run that came back `53 ok / 0 fail`. So the flake is not
specific to `inbound-to-dispatch`: it is the `tests/` package's poll-then-assert
shape, and the fix below should be applied to every such site in the package,
not just the one named test. Disk was 89% throughout, not 100%, which weakens
the disk-pressure confound for THIS instance without eliminating it.

**A second, unrelated failure mode seen in the same session — do not confuse it
with this bug.** One whole-suite run returned `0 ok / 61 FAIL`, every package
`[setup failed]` with `package net is not in std
(/home/onvos/sdk/go1.27rc2/src/net)`. The directories all exist; the next run
was clean. That is a transient toolchain/cache race under concurrent agents, not
a dispatch flake and not `no space left on device` — it presents as total,
uniform failure across every package, whereas this bug is one or two packages.

**A confound that must not be dropped from the record.** The host was at 100%
disk during the first failure — a later run in the same session aborted
outright with `no space left on device` across ~40 packages — and three agents
were running suites concurrently. So "flaky under load" is supported, but
"flaky under *load alone*" is not: disk pressure is an equally good explanation
and was demonstrably present. Do not close this by re-running on an idle box;
that reproduces the pass, not the failure.

This is the same shape as `F36`'s own filing — a test whose outcome depends on
scheduling is reporting something, and the honest first step is to make the
failure reproducible rather than to re-run until green. The difference is that
`F36`'s invariant was over committed rows and could be forced with a barrier;
these two are waiting on asynchronous work with no barrier to hold.

- **Severity:** medium (a suite that flakes under load cannot gate a release,
  and the two flakes were on different packages on different runs)
- **Scope:** the `tests/` package's poll-then-assert sites (at least
  `feat_routing_test.go` and `TestSplitFederation_InboundToTurnRoundTrip`),
  `slakd` package tests
- **Affected:** CI and any developer running the full suite on a busy machine
- **Source:** tests/feat_routing_test.go:66-75 (poll-then-assert); TestSplitFederation_InboundToTurnRoundTrip; slakd whole-package FAIL with no subtest line
- **Status:** open
- **Fix:** give `inbound-to-dispatch` the same deadline treatment for the bot
  row that it already gives the dispatch — poll `countBotRows` rather than
  reading it once the instant dispatch is observed. `slakd` needs a captured
  failure first: run it under `-count=N` with the log kept, because a
  package-level `FAIL` with no `--- FAIL` line is usually a panic or timeout in
  a goroutine, not an assertion.

## F34 — "who ended this engagement" has two answers in two tables (2026-08-06, PROPOSED — needs sign-off, DISPROVED)

Found while shipping `5/G`'s disengage control, which fixed the REST half only.

A `(jid, topic)` window can be ended early by two callers, and they now record it
in two different places:

- **REST** — `POST /v1/engagement` writes an `audit_log` row inside the write's
  own transaction (`routd DB.SetEngagementAudited`, `engagement.clear`). This is
  what `/dash/engagement/`'s button uses.
- **MCP** — the agent's `disengage` tool writes an `ipc_audit` row plus an
  `audit-system.jl` line, through the generic wrapper every `granted` tool passes
  (`ipc/ipc.go:909`). No `audit_log` row, and not in the write's transaction —
  `db.SetEngagement` is a bare Exec.

So an operator asking "who stopped the bot talking in this chat" must query
`audit_log` AND `ipc_audit` and merge two schemas, and only one of the two
answers is transactional. `/dash/audit/` reads `audit_log`, so the agent's own
disengages are invisible there.

This is the same shape as root `CLAUDE.md`'s "one renderer, many sinks": two
paths into one question drift. Note the older claim that both writers were
already audited in routd's transaction — repeated in `F31`, `5/G`, the
`serviceGrants` comment and `dashd/README.md` — was true of neither before today
and is true of one now; those four sites are corrected.

The fix is not local. `ipc_audit` predates `audit_log` and ~45 hand-rolled ipc
tools feed it (`store/ipc_audit.go` already says new callers should prefer
`audit.EmitInTx`), so moving `disengage` alone would make engagement the one tool
that reports differently from its neighbours — trading a split between SURFACES
for a split between TOOLS. The real question is whether the `granted` wrapper
should emit `audit_log` for every state-changing tool, which is a cross-cutting
contract change over every ipc tool and therefore a sign-off, not an inline fix.

- **Severity:** low (both writes ARE recorded; the cost is that answering one
  question takes two queries, and `/dash/audit/` shows only half)
- **Scope:** `ipc` audit contract vs `audit_log` (specs 5/I, 5/G, 5/16)
- **Affected:** all instances
- **Source:** ipc/ipc.go:873-925,1571-1578; routd/db.go SetEngagement /
  SetEngagementAudited; store/ipc_audit.go:16; dashd audit page reads audit_log
- **Status:** PROPOSED — the narrow fix (disengage only) is worse than the gap;
  the cause fix is the `granted` wrapper, which needs sign-off
- **Blocks:** nothing. `5/G` is shipped — its DoD asks that the dashboard
  mutation be audited, and it is.

- **DISPROVED 2026-08-06.** `store.LogIPCAudit` already writes `audit_log`
  via `audit.EmitDB` (`surface=mcp`, `action=mcp.tool.invoke`); only the
  function NAME is legacy. Live krons: 50 `mcp/set_web_route` + 7
  `mcp/delegate_group` rows, and the `ipc_audit` table no longer exists there.
  The premise came from a stale field comment at `ipc/ipc.go:251` saying
  "persists one ipc_audit row" — a claim, not evidence. Comment corrected.
  No code move needed; `disengage` is audited like its ~45 neighbours.

## F38 — `task_run_logs` holds orphan rows on all three live instances (2026-08-06, open)

Found while proving migration `0031` caused no FK damage: `PRAGMA
foreign_key_check` on a `.backup` copy of each live `routd.db` reports violations
on `task_run_logs` and only `task_run_logs` — krons 10, sloth 32, marinade 66.
Identical counts before and after `0031`, so this is pre-existing and unrelated
to the re-key; recorded here rather than fixed (record-don't-fix).

The rows point at a parent that no longer exists (the FK was added by a later
migration than the data, or a task delete did not cascade). Not currently
harmful: routd opens `routd.db` with `_pragma=foreign_keys(on)`, and SQLite does
not re-validate existing rows — only new writes are checked. It becomes harmful
the moment any migration rebuilds `task_run_logs` or its parent the way `0031`
rebuilt `installed_packages`: a rebuild under FK enforcement can fail outright or
silently drop the orphans.

The count growing with instance age (10 / 32 / 66) suggests an ongoing producer,
not a one-off backfill.

- **Severity:** low today, latent trap for the next `task_run_logs` migration
- **Scope:** routd.db `task_run_logs`
- **Affected:** krons (10), sloth (32), marinade (66)
- **Source:** `PRAGMA foreign_key_check` on read-only `.backup` copies, 2026-08-06
- **Status:** open — needs the producer identified before deciding delete-orphans
  vs relax-the-constraint. Do NOT rebuild `task_run_logs` until then.

## ✅ FIXED 2026-08-15 C9 — queue replay opens circuit breakers before runed is ready (2026-07-30)

On every full-stack restart, routd begins replaying pending group work before
runed listens on `:8080`. The three immediate `connection refused` attempts
consume each group's breaker budget, so healthy queued work remains blocked
after runed becomes ready. Live krons reproduced this fleet-wide at
07:49:05–07:49:08; restarting only routd after runed was healthy cleared it.

- **Severity:** high
- **Scope:** routd queue replay readiness / runed client retry
- **Affected:** every instance with pending work during restart
- **Source:** krons journal 2026-07-30 07:49:05–07:49:08; reproduced live on a
  krons deploy 2026-08-15 19:54:48 (`connect: connection refused` to
  `runed:8080`, four groups' breakers opened, then self-recovered)
- **Fix:** `49e967e5` — the breaker no longer counts a dependency outage.
  `queue.ErrDependencyDown` marks a failure the group did not cause;
  `queue.runForGroup` leaves the streak untouched for it (neither incremented
  nor reset — nothing ran, so there is nothing to blame or credit) and logs it
  loud as an operator condition. `routd.runedUnreachable` decides, tagging only
  a failed CONNECT: `syscall.ECONNREFUSED` (nothing listening on :8080 yet) and
  `*net.DNSError` (compose has not published the name yet). A
  `*runedv1.APIError` is excluded explicitly — runed took the request and
  refused it, which IS the folder's business — and a mid-run deadline still
  counts, because that run started.

Chose the breaker-accounting fix over startup ordering: runed can restart at any
time, not only at boot, so an ordering gate would fix the boot case and leave
the general one. No new mechanism — this joins the exemption `dispatch.go`
already applies for `busy` ("do NOT return an err, which would trip the queue
breaker"), and expresses the one rule the breaker has: only a failure
attributable to processing THIS group counts.

**Correction to the original entry.** "Healthy queued work remains blocked
after runed becomes ready" is WRONG, and the fix should not be read as
unblocking anything. `consecutiveFailures` is never a gate — the only non-test
read is in `EnqueueMessageCheck`, which RESETS it — so routd's breaker was
log-only and the next poll always re-fed the batch. runed's own
`circuit_breaker` table could not have been touched either: runed was not
running to increment it. The real defect was a false alarm on a signal
`CLAUDE.md` lists as a post-deploy RED FLAG ("circuit breaker open"), pointing
an operator at four healthy groups instead of at the one daemon still starting.
The severity stands for that reason; the blocked-work claim does not.

- **Verified:** `queue/dependency_down_test.go` — a dependency-down return
  leaves the streak at 0 across threshold+2 runs, does not reset a streak real
  failures built, and real failures still count once the plane recovers.
  `routd/runed_unreachable_test.go` — table over the classifier, using a real
  dial to a closed port rather than a hand-built error, plus the API-error,
  deadline and decode-failure negatives.

## ✅ FIXED 2026-08-15 — T1 — stalled typing indicator on timed-out/errored turns (2026-07-26)

Symptom (krons, telegram:user/1112184352, 2026-07-26): the agent shows "typing…"
for ~20 min then goes silent with no reply. Recurring; the user recognises it as
"the typing indication again".

Root cause (config + missed clear, one concern — the turn-end typing-clear is
unreliable and the backstop can't cover for it):
- Container RunTTL = 20 min; in-container query timeout = RunTTL−30s = 19.5 min
  (`runed/docker.go:147` `queryTimeoutMs`).
- Typing backstop `chanlib/typing.go:14` `DefaultTypingMaxTTL = 20*time.Minute`
  — **equal to RunTTL**, so it never fires before the run is already SIGKILLed.
- Event-driven clear is skipped on the failing path: `dispatch.go:270` clears
  typing only for `!out.Steered`. Follow-up messages that arrive mid-run get
  **steered** into the live container and hand typing-clear to `submit_turn` —
  which never fires when the turn hits the query-timeout wall and its
  graceful-summary fallback itself errors (observed: `Query timeout reached` →
  `Not logged in · Please run /login`, no `submit_turn`, no content). Typing
  refreshes every 5s for the full 20 min, then stops only at the backstop ==
  the container's own death.

Evidence: 4 query-timeouts + 3 `container exited with error` in 12h; last real
bot reply 10:03 while user msgs at 16:11/16:12/16:13 went unanswered under a
live-but-doomed 19.5-min browser-automation turn.

Re-verified 2026-08-15, unchanged: `DefaultTypingMaxTTL` (`chanlib/typing.go`)
is still `20*time.Minute`, `RUNED_RUN_TIMEOUT` (`runed/cmd/runed/main.go`)
still defaults to the same `20*time.Minute`, and `dispatch.go`'s typing-clear
still gates on `!out.Steered` with no run-end reconcile for an orphaned
steered turn (`turns.go`'s `appendAndDeliver` is still the only other clear
site). Not contained — routd turn-lifecycle change across dispatch.go +
turns.go + the chanlib backstop, not one call path — so left unbuilt per the
redesign sign-off gate.

Fix (redesign — needs sign-off; routd turn-lifecycle + chanlib backstop):
1. **Clear typing on every terminal turn outcome, including steered.** The
   steered branch (`dispatch.go` `out.Steered`) marks the turn done but leaves
   typing owned by a `submit_turn` that may never arrive. When a steered turn's
   parent run ends without delivering the steered turn's reply, that turn's
   typing must be cleared by the run-end reconcile, not orphaned.
2. **Make the backstop a real safety net: strictly below RunTTL** (e.g. derive
   it as a fraction of RunTTL, or plumb RunTTL into the refresher), so a stalled
   turn stops "typing" well before the 20-min wall regardless of which clear
   path was missed. A 20-min backstop equal to the 20-min run is not a backstop.
3. **Secondary — fail loud:** the query-timeout graceful-summary path erroring
   with "Not logged in" means the timeout fallback delivers nothing. That resume
   call losing auth is its own bug; the user-facing timeout must still surface a
   notice (dispatch.go:339 `runFailureNotice` covers the non-steered parent, but
   confirm it fires for the steered-follow-up case).

**FIXED 2026-08-15 — points 1 and 2 shipped; point 3 was CONFIRMED and split
out.**

Point 1, and it is the cause rather than the symptom: `turn_context.run_id`
ALREADY records which run each turn was steered into (`SetTurnRunID`, set from
the steer ack's parent `RunID`), so the run-end clear reads that instead of
keeping a second ledger of indicator owners. `DB.TypingJIDsForRun` resolves each
turn's indicator chat the same way dispatch picked it — `return_to` when set,
else `chat_jid` — and dispatch's single `Typing(typingJID, false)` became a loop
over `typingOwnedBy(typingJID, out.RunID)`. This is why the reported case ran
the full 20 minutes: the parent's own clear fires on the PARENT's chat, and
routd steers by FOLDER, so a follow-up from a second chat in the same folder was
never covered by it.

Point 2: `DefaultTypingMaxTTL` is now `90%` of `RUNED_RUN_TIMEOUT`
(`chanlib.typingMaxTTL`, floored at 1m) instead of a flat `20m` that equalled
the wall. It reads the env var runed and routd already read, and
`template/services/{teled,discd}.yml` now pass it, so an instance with a wider
wall widens the backstop instead of losing the indicator early. A test asserts
the derivation stays strictly below the wall for every plausible value.

Point 3 CONFIRMED OPEN and filed as `T2` rather than folded in: the steered
branch returns before the `Outcome == OutcomeError` block, so a steered turn in
another chat gets no failure notice at all. That is a delivery concern, not a
typing concern, and merging it here would make one fix span two.

**Fix:** a4f2152d

## T2 — a steered turn gets no failure notice when its run dies (2026-08-15, open)

Split out of `T1` point 3, confirmed at this tree while fixing `T1` points 1
and 2.

`dispatch.go`'s steered branch marks the turn done and returns
(`return true, true, false, nil`) BEFORE the `out.Outcome == OutcomeError`
block that sends `runFailureNotice`. The parent run's own error branch does
send a notice — to the PARENT's chat. routd steers by FOLDER, so a follow-up
from a second chat in the same folder had its message consumed into the
container and gets nothing at all when that run dies at the timeout wall.

`T1` closed the half that lies (the indicator now stops). This is the half that
is silent.

- **Severity:** medium — the user's message was accepted and produced neither a
  reply nor an error
- **Scope:** turn failure delivery
- **Affected:** routd dispatch, any folder with two chats routed to it
- **Source:** `routd/dispatch.go` (steered early return vs the
  `OutcomeError` notice block)
- **Status:** open — the decision is much smaller than this entry assumed.

**Update 2026-08-29 — the fan-out reader already exists, and its twin already
uses it.** `T1`'s fix built `DB.TypingJIDsForRun(runID)` (`routd/db.go:852`),
which returns every chat recorded against a run:

```sql
SELECT DISTINCT COALESCE(NULLIF(return_to,''), chat_jid)
FROM turn_context WHERE run_id=?
```

Its own doc comment names this exact situation — *"steering is by FOLDER, so
the steered turn's chat is not always the running turn's chat"*. `dispatch.go:613`
calls it to STOP the typing indicator for all of them; the failure notice eight
lines earlier still sends to `chatJID` alone (`:460`). So the two halves of "who
was affected by this run" disagree, and the half that goes quiet is the half
that tells the user something.

The link is already persisted: `SetTurnRunID` (`routd/dispatch.go:402`) records
the ABSORBING run's id on the steered turn before the steered early-return, so
nothing new needs storing.

**This answers the notice-storm worry.** The set is not "every steered chat" —
it is the turns recorded against the ONE run that failed, which is precisely who
was affected. No per-run cap is needed, and `TurnHasBotReply` is not needed as a
gate either: it already guards whether this block runs at all.

- **Remaining decision, and it is only this:** the notice is sent with the
  parent turn's `topic`. Sending the parent's topic to a DIFFERENT chat can land
  the notice in the wrong thread there. Either resolve each chat's own topic
  from its turn row, or send the fan-out notices untopiced. Not chosen here.
- **Fix shape (one loop, one fallback):** send `notice` to
  `TypingJIDsForRun(out.RunID)`, falling back to `chatJID` when that is empty
  (`out.RunID` unset), with the idempotency key extended per destination so the
  adapter does not dedup the fan-out into one delivery.

## X2 — the aws-devops product's credential model died with X1 (2026-08-09, DECIDED 2026-08-20: retire)

`specs/17/product-aws-devops.md` sells one headline: each engineer's `AWS_*`
keys reach `aws`/`boto3` **inside the container**, so CloudTrail attributes the
call to that person. `X1` removed the mechanism — `AWS_ACCESS_KEY_ID` is a
capability credential, so it is now broker-only and container env never carries
it. Six claims in that spec and the whole flow diagram assert the removed
behavior.

This is not dead paper. `arizuko create --product aws-devops` is a shipped flag
(`cmd/arizuko/main.go:221`) that seeds `ant/examples/aws-devops/`, whose
`CLAUDE.md:9-12` tells the agent its env holds the triggering user's AWS keys.
After `X1` those vars are always empty, so the product fails silently rather
than loudly — worse than a crash.

- **Severity:** medium (no live instance runs it; blast radius is the next
  operator who passes `--product aws-devops`)
- **Source:** specs/17/product-aws-devops.md:30,34,55,110,144,175;
  ant/examples/aws-devops/CLAUDE.md:9-12; ant/examples/aws-devops/PRODUCT.md:8
- **Status:** proposed — spec marked `defected`, code untouched, awaiting the
  design decision. NOT worked around.
- **Options (sign-off):** (a) host-side SigV4 connector — the AWS call becomes a
  `5/13` shape 1/2 tool, the credential stays on the host, per-operator
  attribution survives, and `registerWithSecrets` finally gets its first
  consumer; (b) an explicit, narrow, signed-off exception letting one declared
  key prefix into spawn env, which reopens exactly the hole `X1` closed and
  should be refused unless (a) is proven impossible; (c) retire the product.
  Until one is chosen, `ant/examples/aws-devops/` must not ship.
- **DECISION 2026-08-20 — option (c), retire the product.** The egress-credential
  spec is deleted and every tier of it declined: arizuko serves credentials through the MCP
  broker or not at all, and an opaque in-container client is out of scope by
  decision. That removes (a) as an option and (b) was always refusable. Verified
  the same day: `--product aws-devops` is still a shipped flag
  (`cmd/arizuko/main.go:231`) and `ant/examples/aws-devops/` still exists, but
  **zero groups on krons, sloth or marinade use it** — so retiring costs nothing
  live. REMAINING WORK: delete the flag value + the example dir, and the six
  claims in `specs/17/product-aws-devops.md` that assert the removed behavior.
- **Superseded proposal note (2026-08-11):** the egress-credential spec (deleted 2026-08-20)
  (rewritten) is the concrete mechanism for (a): iron-proxy's `aws_auth`
  transform re-signs SigV4 host-side from the same credential store —
  configuration, not new crypto code, and `boto3` works unmodified in the
  container. Decision between (a)/(b)/(c) remains the user's.

## ✅ FIXED 2026-08-15 X3 — runed gets `SECRETS_KEY` and never reads it (2026-08-09)

`compose/compose.go:178` ships `SECRETS_KEY` to runed. Nothing in `runed/` or
`container/` reads it: routd decrypts and sends plaintext in
`RunRequest.Secrets`, and runed has no store. So the decryption key for every
secret in the fleet sits in the env of a daemon that has no use for it, next to
the daemon that spawns agent containers. Found while closing `X1`.

- **Severity:** low (runed is a trusted daemon on the compose network; this
  widens the blast radius of a runed compromise, it does not create one)
- **Source:** compose/compose.go:178; no reader in runed/ or container/
- **Fix:** `c7722184` — `SECRETS_KEY` removed from `daemonKeys["runed"]` in
  `compose/compose.go`. routd and dashd keep it (they seal/unseal rows); runed
  gets already-decrypted credentials in `RunRequest.Secrets` and opens no
  secrets table.

**Why this regeneration is NOT the v0.62 `/login` outage (`M1`).** That outage
happened because the model credentials (`ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, …) vanished from a regenerated `env/runed.env`. Those
keys do not come from `daemonKeys["runed"]` at all — `writeEnvFiles` appends
`store.EnvProfileKeys` for runed separately (`compose/compose.go`), so editing
the runed key list cannot drop them. `TestModelCredentialsScopedToRuned` pins
that, and it still passes.

**OPERATOR ACTION ON DEPLOY.** This changes generated output, so it only takes
effect where the compose env is regenerated:

1. Regenerate, then diff `env/runed.env` before restarting. The ONLY expected
   change is the `SECRETS_KEY=` line disappearing. If any `ANTHROPIC_API_KEY` /
   `CLAUDE_CODE_OAUTH_TOKEN` / `OPENAI_API_KEY` / `CODEX_API_KEY` line also
   disappeared, STOP — that is `M1` recurring, not this change.
2. Leave `SECRETS_KEY` in the instance's top-level `.env`. It is still required:
   routd refuses to start without it, and `env/routd.env` + `env/dashd.env`
   still carry it. Removing it from `.env` crash-loops the instance
   (`deploy_secrets_key_landmine`).
3. `sudo systemctl restart arizuko_<instance>` and confirm a real turn replies
   end-to-end — runed injecting model credentials is the path this touches.

- **Verified:** `compose.TestSplitScopesSecretsKey` now asserts routd + dashd
  carry the key and runed does not; `TestModelCredentialsScopedToRuned` (the M1
  guard) unchanged and green.

## M1 — `mcpc` socat-connect form 502s since mcpc 0.3.0 (2026-07-16, open)

The documented ad-hoc MCP-call form for agents — `mcpc connect "socat
UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s` (`ant/skills/self/mcp.md:128`,
`ant/skills/mcp/SKILL.md:24`, `ant/skills/migrate/SKILL.md:277`) — fails on the
in-container `mcpc` 0.3.0 with `Failed to connect to MCP server: Proxy response
(502) !== 200 when HTTP Tunneling`. The socket is healthy (routd logs `"mcp
server listening"`, `srw-rw---- /run/ipc/gated.sock`); it's mcpc's HTTP-tunnel
wrapper regressing on the socat form, NOT routd. Known since a June diary note.

Impact: an agent that must reach a NON-native MCP tool via mcpc gets a hard 502
and (marinade atlas, 2026-07-16) narrated it to a user as a routd outage +
falsely tied it to an unrelated transient Slack-send 502. For the route-token
class specifically mcpc is now never the answer: minting is root-only by grant
(tier-0 `*` set), a genuine `/root` turn gets every socket tool natively
(worker-side steering + elevated row-ACL gate, 2026-07-16), and list/revoke
ride the tier-1/2 defaults — a non-elevated agent that can't see a mint tool
lacks the grant, and mcpc wouldn't change that. Any genuinely-non-native tool
still hits this.

Fix (deferred, operator): pin/patch in-container `mcpc` to a version whose
`connect "socat …"` form works, OR switch the documented form + the three
skills to a working direct-`UNIX-CONNECT` form — verify against a live socket
before touching the skill docs.

**Premise re-verified 2026-08-15 at `bcb5bb83`: still live, still deferred, and
the deferral now has a second reason.** All three documented sites still carry
the failing form verbatim (`ant/skills/self/mcp.md:134`,
`ant/skills/mcp/SKILL.md:24`, `ant/skills/migrate/SKILL.md:277`), plus a fourth
in the frozen migration record `ant/skills/self/migrations/052-mcpc-cli.md:7`,
which is history and must not be rewritten. `ant/Dockerfile:171` installs
`@apify/mcpc` UNPINNED, so the version an image gets is whatever npm serves at
build time — the regression can move under the fleet without a code change, and
pinning is itself the first half of the fix.

Not fixed here, for the reason the entry already gives and one more: the fix is
unverifiable without a live socket (this worktree has none, and a form that
merely LOOKS right is what put the failing one in three skills), and
`ant/skills/` is release machinery — a change there needs a migration file, a
`MIGRATION_VERSION` bump and an ant image rebuild to reach any agent. Shipping
an unverified form into all three skills would replace a known-broken
instruction with an unknown one.

## M4 — `vited` runs the Vite DEV server in production (2026-07-16, partly fixed 2026-07-18)

**Crash class fixed (2026-07-18):** the dev server's dep-optimization cache
defaulted into the bind-mounted `/web` root, where host uid ownership drifted to
root and `vite` (USER node) hit `EACCES: unlink …/.vite/deps/_metadata.json` on a
config change → crash-loop → `/pub` 502 (marinade + sloth, twice). Fixed by moving
`cacheDir` out of the mount to a container-local path (`ant/vite.config.js`) — the
cache is now container-owned + ephemeral, so it can't recur. Immediate incident
patched by chowning the stale `/web/**/.vite` caches back to the container uid.

**Residual (still open):** vited still runs `vite dev` (poll-watch + transform)
rather than serving the static `/pub`+`/priv` tree — a steady ~1-core cost even
with the scoped watcher. The clean end-state is a static serve (`vite preview` or
a plain file server) keeping the pub-fallback/trailing-slash middleware; deferred,
needs sign-off.

## R2 — Reserving `#announce` silently steals an existing route topic (2026-07-15, proposed)

Before v0.59, every fragment other than `#observe` was a valid pinned topic, so
`folder#announce` meant topic `announce`. The new parser silently reinterprets
that same persisted value as announcement metadata and clears `Topic`; inbound
messages now run in the root topic. No migration detects or rewrites existing
collisions. Parser-produced values otherwise round-trip correctly, and the
`Mode > Announce > Topic` `String` precedence is safe because parsing never sets
more than one field.

- **Severity:** medium
- **Scope:** route-target backward compatibility
- **Affected:** any pre-v0.59 route whose pinned topic is literally `announce`
- **Source:** core/types.go:94-135; routd/loop.go:630-646
- **Status:** proposed
- **Fix:**

## ✅ FIXED 2026-08-15 — L23 — a dead-lettered message is countable but not readable (2026-08-11)

Premise re-verified at this tree: `dashd/routd_page.go` still only shows a
`dead-lettered` COUNT; grepped `resreg/` for any dead-letter resource —
none exists. Still true: an operator can tell messages were lost but not
which ones, to whom, or with what content, and nothing can replay them.

Not contained — this is a new resreg `Resource` (REST + MCP, per the
CLAUDE.md "every cold-tier management entity is a resreg resource" rule),
not a one-file fix, so it needs the redesign sign-off this pass doesn't
have authority to give. Recommended shape, unchanged from the original
report: `messages WHERE status='failed' AND is_bot_message=1` is already the
durable, identifiable row set — read-only surface, no new state.

L22 (this pass) added the one piece this needs that didn't exist yet:
`delivery_attempts` is now readable (`core.Message.DeliveryAttempts` via
`msgReadCols`/`scanMessages`), where before it was write-only. The
resource itself — registration, REST handler, MCP annotation, a replay
action — is still undone.

- **Severity:** medium — the recovery path the dead-letter state exists for does not exist.
- **Scope:** dead-letter visibility
- **Affected:** dashd, routd, resreg
- **Source:** `dashd/routd_page.go`
- **Status:** FIXED — shipped as the resreg `dead_letters` resource, in the
  shape this entry recommended: `messages WHERE status='failed' AND
  is_bot_message=1` is the row set, and NO new state was added.

  `GET /v1/dead_letters` + `POST /v1/dead_letters/{id}/replay`
  (`resreg/resources/dead_letters.go` for the catalog, `routd/
  dead_letters_resource.go` for the mounted handler), with the agent twins
  `list_dead_letters` / `replay_dead_letter` on the MCP socket. One handler,
  two injected gates, containment on `routed_to` by subtree — and the replay
  re-reads the row to bind the id to the caller's folder before mutating, so
  an id alone cannot reach another tenant's message.

  **Replay adds no second delivery path.** It flips the row to `pending` with
  a zeroed attempt count, which is exactly what `PendingOutbound` selects, so
  routd's existing outbound sweep sends it. It reports whether a row actually
  moved: a row already replayed answers 409 rather than claiming a queueing
  it did not do.

  Three decisions worth keeping:

  - **`DB: ""` in the registration**, like `audit`. A dead letter is EVIDENCE
    that a message was lost, so `Export`/`Plan`/`Apply` must never round-trip
    it — `arizuko apply` would be able to rewrite the record. It is also a
    VIEW of `messages`, which no manifest owns.
  - **Its own `dead_letters:read`/`dead_letters:write` scope pair**, not
    `messages:*`. `messages:write` is "speak as a channel" (it authorizes
    POST /v1/messages), and authd's `service:dashd` ceiling explicitly denies
    that; requeueing one already-composed row is a strictly smaller authority.
    dashd's ceiling went 9 → 11 with the case stated at the grant.
  - **The handler projects to `DeadLetterRow`.** `core.Message` carries no
    json tags, so returning it raw shipped Go field names in a body whose
    `/openapi.json` schema is reflected off `DeadLetterRow` — the two
    declarations disagreed on every field name. Caught by the REST test.

  `/dash/routd/` now renders the rows (chat, the lost text, refused-vs-retries
  -exhausted, when) with a replay button, where it rendered a bare count. It
  reads over HTTP and writes over HTTP even though the count beside it reads
  routd.db directly: a page whose read and write disagree about the source of
  truth is how the two drift.
- **Fix:** 752185f0

## ✅ FIXED 2026-08-15 L16 — legacy plaintext secret rows are never sealed; the migrator has no caller (2026-08-11)

`store.EncryptPlaintextSecrets` exists to seal pre-encryption rows in place, and
it has **zero non-test callers** — it is wired to no startup path, so it has
never run on any instance.

This matters more since `S2`: requiring the key stops NEW plaintext writes, but
every row dashd already wrote in plaintext stays that way on disk. `S2` closed
the leak; it did not clean up what leaked. A reader still decrypts those rows
correctly (`open` falls back for un-prefixed values), so nothing is broken —
they are simply unencrypted at rest, which is exactly what `5/14` promises they
are not.

Wiring a write-migration into boot is a behavior change on a live table, so it
is recorded rather than done.

- **Severity:** medium — a real at-rest exposure, bounded to rows written
  before the fix
- **Scope:** secrets at-rest migration
- **Affected:** store, routd
- **Source:** `store/secrets.go` `EncryptPlaintextSecrets`
- **Status:** open — needs sign-off on where the migration runs.

**Premise re-verified 2026-08-15 at `bcb5bb83`, and it holds exactly.**
`EncryptPlaintextSecrets` has two callers and both are in
`store/secrets_test.go`; no daemon boot path, no CLI verb, no migration
references it. It is correct code (idempotent, `WHERE value NOT LIKE 'v2:%'`,
re-seals in one transaction, no-ops without a key) with nowhere to run.

**PROPOSED SHAPE — an explicit operator verb, NOT a boot hook.** Recommending
`arizuko secret seal-legacy <instance>` (a `--dry-run` counting rows first)
over wiring the call into routd's boot after `SetSecretKeys`, for two reasons
this file already paid for:

- The function it REPLACED, `PurgeUnencryptedSecrets`, was a boot-time mutation
  of this same table, and it wiped every secret on each startup because writes
  never wrote the prefix it keyed on (recorded in its own doc comment). A
  second boot-time in-place rewrite of `secrets.value` is the same shape as the
  outage.
- `derived_vs_stored_migration` is the standing lesson: a data migration whose
  rehearsal matches 0 rows is a red flag, not an all-clear. An operator verb
  reports the count before and after; a boot hook runs silently on three
  instances at once with the daemon's exit code as its only signal.

Blast radius if wrong is total — this rewrites the column every credential
lives in — so it does not ship inline, and it wants a rehearsal against a
read-only `.backup` copy of each instance before the real run.

- **Fix:** `33d4d277` — the proposed shape, built as proposed. `arizuko secret <inst>
  seal-legacy [--dry-run]` (`cmd/arizuko/secret.go` `runSealLegacy`) is the
  caller — an explicit operator verb, NOT a boot hook, for the two reasons
  recorded above. `store.CountPlaintextSecrets` is new and backs the dry run;
  `EncryptPlaintextSecrets` now returns the count it sealed, and the verb prints
  before/after and FAILS if any row is still plaintext after the run. Reachable
  from `user-secret` too — it shares `cmdScopedSecret`, and the table is one
  table, so the verb seals every scope either way.

**Reachability checked before writing the caller, per the brief.** The migrator
was live code with no caller, not dead code: `EncryptPlaintextSecrets` compiled,
had two passing tests, and the only thing missing was an entry point. That is
the opposite of `L17`'s `LookupSecret`, which was a dead READER (deleted rather
than wired). A sealer with no caller leaves data exposed; a reader with no
caller leaves nothing.

**Also changed: no key is now an error, not a silent success.** It returned
`nil` on an empty keyring, so a caller could not tell "sealed nothing, there was
nothing to seal" from "sealed nothing, there was no key" — the exact ambiguity
that let this sit unrun. It returns `store.ErrNoSecretsKey` (the existing error,
not a second one). The CLI cannot reach that branch: `openStoreWithKey` already
calls `core.RequireSecretKeyring`.

**Doc drift this exposed.** Three docs asserted the mechanism was already wired,
which is part of why nobody noticed it was not: `SECURITY.md` claimed "legacy
plaintext rows migrated in place on boot"; `store/README.md` still documented
`PurgeUnencryptedSecrets` — the function DELETED in this area's last fix — as
"called on startup ... removes rows without `v1:` prefix". Both corrected;
`cmd/arizuko/README.md` gained the verb.

- **Operator note:** rehearse `--dry-run` against a read-only `.backup` copy per
  instance before the real run, and read the count. A rehearsal matching 0 rows
  is a RED FLAG, not an all-clear (`derived_vs_stored_migration`).
- **Verified:** `cmd/arizuko/seal_legacy_test.go` — seals plaintext rows without
  changing what any reader sees (including the already-sealed row, which must
  not be re-encrypted), `--dry-run` reports the count and changes nothing, a
  clean table is a no-op, and a keyless store refuses. `store/secrets_test.go`
  updated for the count return, still asserting idempotence (second run seals
  0).

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

**v0.59 confirmation:** the new opt-in path still has this exact prefix-loss.
Normal CLI-created routes use `room=` plus `core.JidRoom(jid)`, so the migrate
script strips `room=` and passes values such as `T/channel/C` or `group/42` to
`send`. Adapter ownership requires the original `slack:`/`telegram:`/`discord:`
prefix, and routd returns `no channel for jid`. A normal `room=` route tagged
`#announce` therefore cannot announce. Source: `cmd/arizuko/main.go:403-425`,
`ant/skills/migrate/SKILL.md:280-295`, `chanreg/chanreg.go:41-47`,
`routd/deliver.go:112-120`.

**2026-09-05 evidence.** sloth: 22 lines in 7 days, newest 2026-09-04 14:30:35 —
the `/migrate` broadcast is delegated to `main/main`, whose reply targets jid
`main` (a folder) and is dead-lettered (`out-c804cc4821ac8d0f`). krons: 7 lines
2026-09-03 20:47-20:52 for the anteval `eval/etksvtw-*` chats, same shape.

- **Severity:** medium (announcements silently lost on some groups; log noise)
- **Status:** OPEN, record-only.

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

## OPERATIONAL 2026-08-15 — Admin cannot log in to the krons dashboard (2026-07-12, HIGH, config-not-code)

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

  **2026-08-15 re-check:** OPERATIONAL — a live-instance login attempt, not
  a repo-verifiable claim. Operator: run `sudo sqlite3
  /srv/data/arizuko_krons/store/authd/auth.db "select sub, created_at from
  auth_users;"` to confirm whether a login has since completed; if still 0
  rows, follow the three numbered actions above. No further code change.

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

## dashd reads SQLite directly — spec 7/1 (formerly 6/1) says it shouldn't; code and CLAUDE.md say it should (2026-06-16, PROPOSED — spec/code conflict, needs sign-off, verified 2026-08-15)

`dashd/main.go` opens `routd.db` (`dbRoutd`), `onbod.db` (`dbOnbod`) and `runed.db`
(`dbRuned`, via `openSiblingOwner`) directly and queries them from most page handlers
(groups, tasks, routes, activity, secrets, usage). The renamed spec (`6/1` →
`specs/6/28-cockpit-index.md`, still `status: draft`) still says, verbatim: "Every
datum a dashboard shows or mutates flows through the owning daemon's `/v1` surface.
No dashboard — hub or per-daemon — reads a SQLite DB directly. This retires dashd's
current direct reads of routd.db/onbod.db/messages.db."

That is the OPPOSITE of root `CLAUDE.md`'s current "### Split write-discipline":
"FS-mounted daemons (dashd, onbod, CLI) write owned tables directly to the owner's
DB" — and the code comments agree with CLAUDE.md, not the spec: `dashd/main.go:385`
"dbRoutd *sql.DB // routd.db handle — dashd's ONLY message/config store", `:417`
"dashd IS FS-mounted on onbod.db (dbOnbod, for the invites page)". `openSiblingOwner`
(`dashd/main.go:451`) is a THIRD documented pattern: dashd also opens `runed.db`
read-only as a "sibling owner" for dashboard views it doesn't own.

The one page that WAS moved off direct DB reads is `/dash/audit/`
(`dashd/audit_page.go`), whose header comment explicitly frames the old direct-read
pattern as "the recorded defect class that dashd/route_tokens.go already is" and
fans out to each owner's `/v1/audit` instead. So the codebase is not uniform: one
page adopted the spec's contract, most did not, and CLAUDE.md documents the
NON-adoption as the shipped decision.

- **Severity:** high
- **Scope:** dashd, spec 7/1 (draft, never shipped) vs. root CLAUDE.md (shipped)
- **Affected:** all dashd pages (groups, status, invites, memory, tokens, activity, tasks, usage)
- **Source:** dashd/main.go:385-451; dashd/audit_page.go:3-16; specs/6/28-cockpit-index.md:33-61;
  root CLAUDE.md "### Split write-discipline"
- **Status:** PROPOSED, not fixed — this is a spec-vs-implementation conflict, not a
  contained bug: resolving it either means (a) implementing the full `/v1`-only
  read-path for every dashd page the way `/dash/audit/` already does (cross-daemon
  control-flow change, new `/v1` read endpoints per `specs/6/30`/`6/32`), or (b)
  marking spec 7/1's "no direct DB" section declined/superseded and updating it to
  document the FS-mounted direct-read pattern CLAUDE.md and the code already ship.
  Either direction needs a decision, not a docs-only close. NOT fixed this pass.
- **Fix:** needs sign-off — see Status.

## Codex audit 2026-06-18 — dashd full sweep

---

## chat: loadChatSessions limits before filtering; nondeterministic continue links (2026-06-18, PARTIAL — folderSessionTokens half PROPOSED, needs sign-off, verified still open 2026-08-15)

`chat.go:90`: newest 200 rows fetched then filtered in Go — older sessions disappear once table
grows. `chat.go:361`: `folderSessionTokens` infers topic→token by time proximity; two sessions
without an early message produce nondeterministic continue links.

- **Severity:** medium
- **Scope:** dashd/chat.go:90,:361
- **Source:** codex audit 2026-06-18
- **Status:** partial — LIMIT-before-filter half resolved a51989cc (2026-07-05,
  verified 2026-07-13: `folder IN (...)` pushed into SQL before the LIMIT 200).
  `folderSessionTokens` half re-verified 2026-08-15, still open and NOT a contained
  fix: for each `chat_sessions` row (newest created_at first) it takes the first
  `messages` row with `timestamp >= created_at AND topic != ''` as that session's
  topic, first claim wins (`dashd/chat.go:359-391`). Two sessions from the same
  folder created close together can each have a topic-bearing message fall inside
  the other's `>= created_at` window, so the session→topic mapping can attach a
  continue link to the wrong session — a heuristic on message timing, not an
  explicit link. A real fix needs an explicit session↔topic (or session↔turn)
  column persisted at session-creation/first-message time — a schema change to an
  existing table, not a new call path — so per CLAUDE.md redesign discipline this
  is reported, not fixed, pending sign-off on where that column lives
  (`chat_sessions.topic` set once at first message, vs. `messages.session_id`).
- **Fix:** push filter into SQL (done); persist topic-session linkage explicitly (proposed, open)

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
- **2026-08-15 re-check:** two separate facets, still both true. (a) OPERATIONAL,
  live-krons-only — verify with `sudo curl -s -o /dev/null -w '%{http_code}\n'
  https://krons.fiu.wtf/hook/<a-real-token>` (should NOT 302 to `/pub/...`) and
  `sudo sqlite3 /srv/data/arizuko_krons/store/proxyd/proxyd_routes.db "select
  path from proxyd_routes where path like '/hook%';"`. (b) code-level design
  gap CONFIRMED still present: `proxyd/main.go:loadInitialRoutes` (`:238-273`)
  still seeds `PROXYD_ROUTES_JSON` into `proxyd_routes` ONLY when the table has
  zero rows ("Persistence wins when proxyd_routes has rows" — spec 6/2
  Phase-3), so any pre-existing instance never gains a newly-added
  `coreProxydRoutes` entry without a manual row insert. A durable fix is an
  upsert-missing-core-routes-on-boot path — new boot behavior, cross-cutting
  (every instance's route table), so this stays a PROPOSAL needing sign-off,
  not a same-session fix.

## ✅ FIXED 2026-08-15 P3 — Proxyd dispatch combines routes from two database snapshots (2026-07-30)

Route matching reads one snapshot, then proxy selection rereads the table and
rebuilds proxies. A concurrent route mutation can pair old authentication policy
with a new backend, return a false 404, and doubles database/proxy construction
work on every routed request.

- **Severity:** high
- **Scope:** proxyd request dispatch
- **Affected:** requests concurrent with `proxyd_routes` mutation
- **Source:** proxyd/main.go:124-136,625-626,654-660; proxyd/resource.go:60-91
- **Fix:** `0dc7dce8` — one snapshot per dispatch. `s.routes()`/`s.proxies()` each called
  `rr.snapshot()` — two reads of `proxyd_routes` and two full proxy-map builds
  per routed request. Replaced by `s.routeSnapshot()`, taken once in the
  catch-all handler and passed into `dispatchRoute(rt, proxies, …)`; `s.routes()`
  survives only for the callers that need the table and no backend (startup log,
  `/health` count). Halves the per-request DB reads and proxy construction as a
  side effect.

The false 404 is now impossible-by-construction rather than merely unlikely:
`snapshot()` builds a proxy for every non-redirect route in the same read, and
`dispatchRoute` returns early on redirects, so `proxies[rt.Path]` cannot be nil
for a route matched from that snapshot. That branch answered a bare 404 —
blaming the caller for a proxyd bug — and now logs loud and answers 500.

- **Verified:** `proxyd/dispatch_snapshot_test.go`.
  `TestSnapshotCarriesAProxyForEveryRoute` is the deterministic structural
  guard. `TestConcurrentRouteMutationNeverSplitsAcrossSnapshots` deletes and
  re-adds THE MATCHED route under 8 concurrent readers and requires both a
  match and a miss to occur (else the fixture is vacuous), then asserts no
  matched request ever lost its backend. Mutation-checked: restoring the second
  `routeSnapshot()` call inside the loop reproduces it — "4 of 58 matched
  requests found no backend". Full `go test ./proxyd/ -race` green (424s).

## ✅ CLOSED-STALE 2026-08-15 K1 — Security-sensitive identifier generators discard entropy failures (2026-07-30)

JWT JTIs, OAuth-state nonces, signing-key IDs, run IDs, and session UUIDs are
returned after unchecked `crypto/rand.Read`. Entropy failure therefore emits
predictable or duplicate identifiers instead of aborting issuance or execution.
PKCE and refresh-token generators already return these errors correctly.

- **Severity:** medium
- **Scope:** authentication and execution identifier generation
- **Affected:** JWT lineage, OAuth state uniqueness, key rotation, run/session identity
- **Source:** auth/es256.go:140-145; auth/oauth.go:160-164;
  authd/server.go:95-101; runed/manager.go:364-376
- **Fix:** NONE — no code changed. The premise does not hold on this toolchain.

**`crypto/rand.Read` cannot return a non-nil error.** Since Go 1.24 it is
documented "It never returns an error, and always fills b entirely. Read calls
io.ReadFull on Reader and CRASHES THE PROGRAM IRRECOVERABLY if an error is
returned." `go.mod` declares `go 1.27`, so no supported build of this module
gets the old behaviour.

Verified empirically rather than from the doc comment, on `go1.27rc2`: a probe
swapping in a always-failing `rand.Reader` does not return an error to the
caller — it dies with `fatal error: crypto/rand: failed to read random data (see
https://go.dev/issue/66821)`, unrecoverable, from `crypto/rand.fatal`.

So the failure mode the entry describes — "entropy failure therefore emits
predictable or duplicate identifiers instead of aborting" — has no reachable
path. Every listed site is ALREADY maximally fail-loud: the process dies before
a weak identifier can exist. All eight discarding sites (`auth/es256.go:152`,
`auth/oauth.go:162`, `authd/server.go:101`, `routd/server.go:772`,
`runed/manager.go:505,512`, `crackbox/pkg/run/run.go:168`) import
`crypto/rand`, not `math/rand` — checked, not assumed.

The entry's closing line has it backwards: the PKCE and refresh-token generators
it praises for "returning these errors correctly" are the ones carrying
UNREACHABLE branches. That is harmless and conventional, so it is left alone —
deleting dead `if err != nil` across seven files would be churn with no
behaviour change.

**Checked for a real instance of the class while here, and found none.** The two
`math/rand` users are not identifier generators in the security sense:
`chanlib/retry.go:121` is backoff jitter, and `runed/docker.go:244` is a
uniqueness suffix on a temp filename inside one folder's own private IPC dir. If
either had minted a token or nonce it WOULD have been this bug for real, since
`math/rand` genuinely is predictable.

**Re-file only if** `go.mod`'s floor ever drops below `go 1.24`, or a
security-sensitive identifier starts coming from `math/rand`.

## ✅ FIXED 2026-08-15 A4 — ACL read errors are indistinguishable from valid empty policy (2026-07-30)

Query failures intentionally return no rows and therefore deny authorization,
but the API cannot distinguish that failure from an empty policy. User-scope and
list endpoints consequently return successful empty results. Worse, individual
scan failures are skipped, producing a partial policy that can omit a deny row
while retaining allows.

- **Severity:** high
- **Scope:** ACL storage and authorization evaluation
- **Affected:** agent authorization, login scope snapshots, ACL management surfaces
- **Source:** store/acl.go:176-310; auth/authorize.go:59-99; routd/server.go:506-514
- **Fix:** `9076253a` — the four `store` ACL readers (`ACLRowsFor`, `ACLWildcardRows`,
  `ListACL`, `ListACLByScope`) return `([]core.ACLRow, error)`. Authorization
  still fails CLOSED everywhere — the error return changes who can SEE the
  failure, never the direction it fails in.

Two distinct defects, both closed:

1. **A read error read as an empty policy.** A query error returned a bare `nil`
   with no log at all. Now an error, so `GET /v1/users/{sub}/scopes` answers 500
   instead of `404 no_grants` (authd snapshots that at login — it would have
   minted a scopeless session indistinguishable from a legitimately
   unprivileged one), `list_acl` answers 500 instead of `{"acl":[]}`, dashd's
   grants page shows an error banner instead of an empty table, and `arizuko
   grants` errors instead of printing "no grants".
2. **A partial policy could ESCALATE.** Per-row scan failures were SKIPPED
   (`if err == nil { append }`). Dropping a DENY row leaves the allow rows
   above it intact, so the caller is granted something an operator explicitly
   forbade — the more dangerous half, and the reason a partial read is now
   never returned at all: one bad row fails the whole read.

Consolidated rather than repeated: `store.scanACLRows` is the single place a
policy read materialises, and `auth.policyRows` is the single exact+wildcard read
all four evaluators (`Authorize`, `EffectiveActions`, `UserScopes`, `CheckHold`)
go through — previously four hand-copied pairs of calls that could each drift on
what a failed read means.

Fail-closed directions, all logged loud: `Authorize` DENIES; `EffectiveActions`
advertises NO tools; `CheckHold` holds nothing; `auth.Delegate` REFUSES (it is
the precondition on a write handing out authority — if what the granter holds
cannot be read, nothing is provably a subset of it); proxyd stamps no
`X-User-Groups`; dashd treats the caller as having no access.

- **Verified:** `store/acl_partial_test.go` corrupts ONE row's `grant_option` so
  only that row fails to scan, and requires an error with no rows — the
  dropped-deny escalation. `store/failclosed_test.go` rewritten: it used to
  ASSERT the old contract (empty on a broken table) and now requires an error
  from all four readers. `auth/userscopes_test.go` gained
  broken-table cases for `UserScopes` (error, not empty), `Authorize` +
  `EffectiveActions` (still deny/hide) and `Delegate` (refuses), each with a
  vacuity guard proving the fixture allowed before the table was dropped.
  `routd/acl_read_error_test.go` pins the endpoint 500; mutation-checked by
  disabling the error branch, which reproduces the exact `404 no_grants`.

## Adversarial review of the 5/16+surrogate features — deferred findings (2026-07-30)

Bug-hunt on the newly-shipped features. Fixed inline: F3 (Delegate glob-widening,
cc11eace), F4 (Delegate deny/wildcard, cc11eace), F8 (tier-cap on operator REST
mint, 48c7e5ce), F9 (surrogate over-aggressive credential null, 25e0d91c), F16
(OpenAPI omitted route_tokens, 1f66d77f). Remaining, deferred:

- **F1 (fix before wiring Delegate — phase 3).** `role:operator` grant_option=1 is
  set by an UPDATE in routd/0021 + store/0075, but on a split-migrated instance
  `cmd/arizuko/migrate_split.go` copies `acl` (omitting grant_option) at :344
  AFTER routd.Open runs 0021 at :300 → the root ends up grant_option=0, so
  `auth.Delegate`'s root can delegate nothing once wired. Fresh-install seeding of
  role:operator into routd.db also needs grant_option=1. Latent today (Delegate
  unwired). Fix: seed grant_option on the operator row idempotently at the point
  Delegate is wired, after any copy.
- **F6 (verify before relying on /v1/groups for humans).** `groupsRESTAuthz` wants
  a `routes:read`/`acl:read` resource:verb scope, but human JWTs carry folder-glob
  scopes (`handleUserScopes`) that `auth/scope.go` rejects → the read twin may be
  reachable only by service/CLI tokens (folder="" → unfiltered list-all), and the
  subtree filter (`ownsFolder`) inverts for folder=""/"**". Entangled with the
  tier/scope claim model that `5/33` rewrites — resolve there.
- **F7 (pre-existing).** `DELETE /v1/route_tokens/{jid}` matches one path segment;
  hook JIDs (`hook:acme/github`) and nested `web:` JIDs contain `/` → 404. Fix:
  `{jid...}` catch-all (verify resreg path-param binding) or a query param.
- **F5 (by-design, pre-existing).** Both new REST faces (+the pre-existing acl one)
  are open when `verify==nil` (unset AUTHD_URL = local-dev open). Inert in prod
  (AUTHD_URL always set). Changing the open-when-unverified model needs its own
  sign-off.
- **Surrogate robustness (F10–F14, medium).** F10: misconfig (unknown provider / no
  creds) laundered as transient + near-expiry token dropped not stale-used. F11: one
  malformed operator TOML `os.Exit(1)`s routd (routing outage from an optional
  feature) + a shadowing datadir filename silently inherits the built-in's client
  secret. F12: `Provider.AllowedDomain` parsed, never enforced (false egress
  guarantee). F13: unvalidated `secret_key` collisions overwrite/clobber rows. F14:
  a provider removed from the registry leaves a live, unlistable, unrevocable token.

## 5/33 grant-surface flip — attempted, reverted (2026-07-30, adversary-caught)

The role-based grant flip (`600fc408`) shipped broken; reverted (`0f8d956f`). An
adversarial review (fable sub; codex dead this session) found + reproduced:

- **BUG1 (critical):** the "differential" test drove `SeedFolderGrants` — dead code
  the shipped `deriveFolderGrants` never called. "Proven safe by the differential"
  was FALSE. Lesson: an equivalence test must diff the ACTUAL production function.
- **BUG2 (critical):** `deriveFolderGrants` blind-rebound `folder:<path>` → its
  DEPTH role on every call → restriction-by-role impossible (only widening). The
  "decoupled from location" claim was false. Fix: assign the role once at
  create/invite, never re-derive per read.
- **BUG3 (high, live):** `grants.CheckAction` is last-match-wins; `store.ACLRowsFor`
  has no `ORDER BY`; the `acl_by_principal_action` index returns `folder:` before
  `role:`, so an operator DENY sorted before a role ALLOW and was masked. Confirmed
  repro. Live unmitigated consumer: `container/runner.go:553` `share_mount` RO gate
  (fed from `deriveFolderGrants`, no `db.Authorize` pairing). Fix in the correct
  flip: render denies LAST, or add `ORDER BY effect` / deny-wins to the render.
- **BUG4 (medium):** `SeedTierRoles` is INSERT-OR-IGNORE-only — a tightened tier
  bundle never revokes; stale role rows survive a redeploy. Now moot (unwired).
- **BUG5 (medium, pre-existing):** `dashd/tools_admin.go:36` renders the tool list
  from `grants.DeriveRules` directly — a second sink that drifts from the socket's
  `deriveFolderGrants` (overlay/roles). Violates one-renderer-many-sinks. Predates
  the flip; fold both onto one renderer when the flip lands.

Status: BUG1-3 un-shipped by the revert; deriveFolderGrants back to DeriveRules-base
+ expanded overlay (deny appended last = correct precedence). BUG4 moot until the
flip; BUG5 pre-existing. The correct flip must satisfy all five + the tests the
review named (differential over the REAL path; operator-deny-vs-role-verb; restrict-
via-role; multi-platform dedup; dashd/socket parity; reseed-staleness).

## CEO audit — release-readiness doc gaps (2026-07-31)

Release-ready per the CEO audit after these (docs/positioning, not code):

- **Connector/ext-mcp capability is buried (biggest pitch-vs-capability gap).** The
  ext-mcp connector injection (5/13) — arizuko's actual differentiator — has no
  landing-page or concepts-page presence, only `reference/mcp.html:830-871` + one
  line in `security/index.html:44`. Fix: write `concepts/ext-mcp.html` (connectors)
  + link from landing; add `howto/add-connector.html` mirroring
  `howto/oauth-provider.html`. Half-day.
- **Tier-language doc-debt (NOT stale yet — tracked for the 5/33 rename pass).** ~27
  web pages + root docs say "tier"; today they're still factually accurate (roles ==
  the tier bundles), so NOT release-blocking. But when 5/33 phase (b)
  (AuthorizeStructural→scope) + the tier-scalar deletion land, every "tier"-language
  file must update in one coordinated pass. Files to sweep: `security/index.html`,
  `concepts/*`, `reference/mcp.html`+`grants.html`, root `SECURITY.md`/`ARCHITECTURE.md`/
  `ROUTING.md`, ant self-skills reading `ARIZUKO_TIER`. Owner: the 5/33 rename pass.
- **README/landing polish:** SDK-comparison pitch (`README.md:88-102`) should move onto
  `index.html` (done: README "What's planned" 5/17-shipped staleness fixed 2026-07-31).

## CTO audit — 5/33 partial cutover (2026-07-31)

CRITICAL C1+C2 FIXED by reverting step (d) `edd42181` (commit `3a8b8291`): `egress`/`web:publish`
off `tierOf` onto grants was NOT equivalence-preserving — `container.tierOf` (count+1) and
`auth.Resolve().Tier` (min(count,3), floored to 1) diverge below top level, so 18 live sub-groups
would gain unconstrained egress and `/root` turns silently lost web mounts (`CheckAction(["*"],
"web:publish")` is false — `matchGlob` stops `*` at `:`). Real fix (deferred, needs sign-off):
a reviewed backfill migration writing explicit per-folder `egress`/`web:publish` grants that
reproduce the `tierOf` predicate EXACTLY, then delete `tierOf` — the capability was never
derivable from the tier scalar. H1 (test fixtures) + H2/H3 (atomic seed + loud assign) FIXED.

Open (MEDIUM, deferred — none release-blocking; fail-closed or latent):

- **M3 — folderGrantsFromACLOnly drops ACL row Scope.** `routd/seed_grants.go` renders a
  scoped operator DENY (`mcp:send` on `acme/eng/secret`) as a GLOBAL `!send` for the folder.
  Fail-closed (over-denies), but wrong. The flip made the overlay the whole bundle so it bites
  more. Fix: thread scope into the rule render / evaluate scope in CheckAction.
- **M4 — `/root` gets a READ-ONLY share mount (inverted).** `container/runner.go:554`
  `CheckAction(["*"], "share_mount", {readonly:"true"})` matches (rule `*` has no params) so
  elevated gets RO while tier-1 gets RW. Pre-existing `*`-glob-vs-params gap (same as C2).
- **M5 — no un-assign path.** `store.RemoveMembershipBare` has no caller; `remove_acl` scope `**`
  only deletes `role:operator` edges. The operator-rebind rationale isn't reachable via any tool.
- **M6 — role rows are scope `**`.** Bypasses authorize.go's "tier defaults apply only at own
  folder" guard. Harmless today (every db.Authorize passes scope==socket folder) but the `**`
  scope, not the guard, bounds delegation once `AuthorizeStructural` is deleted in step (b).
- **M1/BUG5 — dashd tools display off DeriveRules.** `dashd/tools_admin.go:36`. The revert
  neutralized the "made-it-live" divergence (egress/web back on tierOf); still a second sink to
  fold when step (e) lands. **M2 self-resolved by the revert** — `SECURITY.md:210` "tier ≤ 1
  egress" is accurate again.

## P2 — three identity-link mechanisms; authd owns two of them (2026-08-02, decided)

"One human, many logins" is implemented three times. Operator decision
2026-08-02: **collapse to one, exposed by authd in the system's canonical way**
(a resreg resource — REST + MCP from one handler, per root CLAUDE.md), with
proxyd and dashd consuming that surface rather than reading a table.

| mechanism | DB | writer | read surface | live rows |
|---|---|---|---|---|
| `identities` + `identity_claims` | auth.db | `store/identities.go:33` | **yes** — `GET /v1/identities/{sub}` (`authd/http.go:384`), consumed by routd's `inspect_identity` | 0 everywhere |
| `auth_users(user_id)` + `oauth_identities` | auth.db | `authd/store.go:122` (the live OAuth login path) | **none** | 1 (marinade) |
| `auth_users.linked_to_sub` | **routd.db** | none (`LinkSubToCanonical` has no non-test caller) | read by `proxyd/main.go:885`, `dashd/profile.go:81` | 0 everywhere |

So authd holds one model that is exposed but never populated, and one that is
populated but never exposed; routd holds a third that is neither. The two
readers in row 3 therefore always get the empty answer — proxyd canonicalises
to the input sub unchanged, dashd renders an empty linked-accounts list.

**Trap worth knowing**: `auth_users` exists in BOTH routd.db and auth.db with
DIFFERENT schemas — `(sub, username, hash, name, created_at, linked_to_sub)`
versus `(user_id, name, created_at)`. Same name, two databases, two meanings.

### Target

1. `oauth_identities` (+ `auth_users(user_id)`) becomes the single model: it has
   the live writer and a first-class person entity, so no sub is "elected"
   canonical and losing one provider account cannot orphan the rest.
   `UNIQUE(provider, provider_sub)` already prevents one login mapping to two
   people.
2. Re-point `GET /v1/identities/{sub}` at it and register it as a resreg
   resource so the MCP twin is derived rather than hand-rolled. The endpoint
   already exists as the seed; today it reads the empty model.
3. proxyd and dashd consume that surface. Note the accepted cost: this puts an
   HTTP hop on proxyd's per-request cookie path, so authd being unreachable
   degrades session canonicalisation — decide the failure mode (fail closed, or
   fall through to the raw sub) as part of the work.
4. Delete `identities`, `identity_claims`, `store/identities.go`,
   `auth_users.linked_to_sub`, `CanonicalSub`, `LinkSubToCanonical`,
   `SubsLinkedTo`, and `dashd`'s `linkedSubs` — additive DROP migrations in
   both schemas.

Deleting before step 2 lands would remove `inspect_identity`'s backing store,
so the order matters.

- **Severity:** medium (account linking non-functional on two surfaces)
- **Source:** `store/identities.go`; `authd/store.go:122`; `authd/http.go:384`;
  `store/auth.go:60,73`; `proxyd/main.go:885`; `dashd/profile.go:81`
### Step 1 SHIPPED (90e46d62)

`GET /v1/identities/{sub}` now reads `auth_users`+`oauth_identities`. The
endpoint had been answering `{identity:null,subs:[]}` for every sub, so routd's
`inspect_identity` reported everyone as unclaimed.

### Step 2 is NOT a deletion — correction to this entry

The old model has two more live surfaces, found while attempting the delete:

- **an operator CLI** — `arizuko identity list/create/link/unlink`
  (`cmd/arizuko/main.go:815-870`), the only way to bind a platform sub to a
  person by hand;
- **`inspect_identity`** (`ipc/inspect.go:112`) reads
  `GetIdentityForSub` straight from the DB, NOT through authd's endpoint — so
  re-pointing the endpoint did not re-point the tool.

So the two models are not redundant in the way this entry first claimed. They
differ by WRITER: `identities`/`identity_claims` is manual and
operator-driven and accepts any sub (`tg:42`); `oauth_identities` is written
only by the OAuth login path (`upsertOAuthUser`) and has no manual entry point.
Deleting the first would remove the operator's ability to link a platform
identity by hand — which is the pairing capability `5/31` is about.

**Revised step 2**: give `oauth_identities` a manual writer, migrate the CLI and
`inspect_identity` onto it, THEN delete `identities`, `identity_claims`,
`store/identities.go`, `linked_to_sub` + accessors, and dashd's `linkedSubs`.
`provider`/`provider_sub` already holds a platform sub fine — the endpoint test
seeds `tg:42` that way.

### Step 2 SHIPPED as a deletion (2026-08-04, fcd845cb + 54125cbd)

The revision above was itself wrong on one point: `inspect_identity` had
already moved. `routd/identity.go` resolves through authd's
`GET /v1/identities/{sub}` (`b6aabfd2`), which step 1 re-pointed at
`auth_users` + `oauth_identities` — so the tool needed no work.

That left `arizuko identity link|unlink` as the only writer, filling a table no
reader ever looked at, which is not a capability to preserve. And the manual
bind it nominally offered is what `5/31` pairing now does with consent and an
inverse. So step 2 landed as a straight delete, no manual writer added:
`store/identities.go`, the CLI subcommand, `linked_to_sub` + `CanonicalSub` +
`LinkSubToCanonical` + `LinkedSubs` + `AuthUserBySub`, dashd's `linkedSubs`,
and migrate-split's copy of the two tables. Dropped by authd `0006` and routd
`0027`.

**Step 3 is what remains** and is an addition, not a deletion: dashd's
"Linked accounts" section was removed rather than left rendering an
always-empty table, so showing a real list means dashd consuming authd's
`GET /v1/identities/{sub}` — including the failure-mode decision this entry
already flagged. proxyd no longer reads `CanonicalSub` at all.

- **Status:** steps 1 + 2 shipped; step 3 (dashd consumes authd's endpoint) open

## P3b — rejected minimization phases, recorded so they are not retried (2026-08-01, closed)

Two phases from the 2026-08-01 minimization plan were rejected on review and
should NOT be reattempted as written:

- **Fold `onboarding.token` into `invites`** — forbidden by `specs/5/31`: an
  invite carries a SCOPE and is issued by a granter; a pairing nonce carries a
  JID and proves chat control. Merging them yields a table whose redemption
  semantics depend on which columns are NULL, with a bug class where a pairing
  token grants. If they ever share storage it must be via an explicit `kind`
  column, never NULL-inference.
- **Drop `onboarding.status` as derivable from its timestamps** — it is not
  derivable, and the fail-loud work (`c6bfe2b0`) added a refusal outcome that
  makes it less so.

- **Status:** closed — recorded to prevent a repeat, no action wanted

## S1b — the helm chart deploys the deleted `gated` daemon (2026-08-05, PROPOSED — redesign, needs sign-off)

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
- **Scope:** deploy/helm, and `template/web/pub/arizuko/howto/kubernetes.html`
  which documents it (left at `CHANNEL_SECRET` on purpose — the page and the
  chart must move together)
- **Affected:** anyone installing via helm
- **Source:** deploy/helm/arizuko/templates/gated.yaml:57;
  deploy/helm/arizuko/templates/_helpers.tpl:89-97;
  deploy/helm/arizuko/values.yaml:119; deploy/helm/arizuko/templates/secret.yaml:13;
  deploy/helm/arizuko/templates/adapters.yaml:68-75
- **Status:** PROPOSED — redesign, needs sign-off. The real fix is a split-topology
  chart (authd/routd/runed deployments + services, repoint `routerService`, add
  the `AUTHD_*` env, drop `gated.yaml` and `secrets.channelSecret`), or deleting
  the chart if helm is not a supported install path. Do not half-wire it.

## O2 — two adapters down on platform credentials, not code (2026-08-02, open)

Fleet eval + redeploy of all three instances (2026-08-02 15:17–15:20). All units
active, all daemons healthy, and the agent path is verified end-to-end on krons —
a real bot row landed in `routd.db` at `2026-08-02T15:17:49Z`
(`telegram:user/1112184352`), 35s after restart. Two adapters are nonetheless
dead, both on expired platform credentials, both pre-existing and unaffected by
the deploy:

- **krons `whapd`** — `Restarting`, ~65-70s cadence, 30+ restarts observed.
  Logs: `"session invalidated, delete auth dir and re-pair","code":401`. routd
  auto-deregisters the `whatsapp` channel every ~3 failures. Needs the
  `--pair <phone>` pairing-code re-auth, not a restart. A pairing code was
  issued 2026-05-21 and never redeemed, so this has been down ~10 weeks.
- **marinade `emaid`** — `health: starting` indefinitely, `RestartCount=0` (it
  does not crash-loop, so it never gets flagged as failing). Logs:
  `imap idle error … [AUTHENTICATIONFAILED] Invalid credentials` every ~60s;
  routd deregisters the `email` channel every ~3 min. Needs the correct IMAP
  password in secrets.

Both are operator credential fixes, not code fixes — recorded so they stop being
rediscovered by each fleet check. The second one is also a monitoring gap worth
noting: an adapter stuck in `health: starting` forever looks less alarming in
`docker ps` than a crash loop, while being equally down.

- **Severity:** medium (two channels dead; no data loss, no security exposure)
- **Scope:** operator credentials
- **Affected:** `arizuko_whapd_krons`, `arizuko_emaid_marinade`
- **Fix:** re-pair whapd per `reference_whapd_pairing`; set marinade's IMAP
  password. Neither requires a deploy.

## A1 — the split is deployment-shaped only: auth data lives in the router's DB (2026-08-02, proposal)

The daemons look like microservices and are coupled through a shared filesystem.
Three verified facts:

**1. Every daemon mounts every DB.** `compose/compose.go:812`, in the *generic*
`writeSvc` emitter, writes `- <dataDir>:/srv/app/home` for every service with no
per-daemon condition. Verified on live krons: `arizuko_timed_krons` has the
instance data dir mounted `rw`. So CLAUDE.md's "non-mounted daemons (slakd,
timed) write via the owner's HTTP API" describes a boundary that does not exist —
the split write-discipline is a convention, honored voluntarily in code.

Honoring it: `timed`, `davd`, `teled` open no DB at all. Not honoring it:
`proxyd` (routd.db), `webd` (routd.db), `dashd` (routd.db + onbod.db +
runed.db), and `slakd` (`slakd/main.go:44` opens routd.db) — which CLAUDE.md
names as an HTTP writer. `timed` owns no DB because it chose the wire, not
because it lacks access.

**messages.db is no longer in that list (2026-08-05, `e5e75bd5` + `3c2b7ad7`).**
routd's two boot-time legacy copiers are deleted and dashd is repointed, so no
daemon opens the monolith — it was the one file legitimately shared by several
daemons and therefore the stated blocker on per-owner `store/` subdirectories.
That blocker is lifted; the remaining ones are enumerated in Q1 (three CLI verbs
still open it) and the cross-DB reads catalogued above (onbod→routd.db,
dashd→3 DBs), which a per-owner mount would have to keep reachable. The
messages.db FILE stays on all three instances — it holds pre-split history, and
disposing of it is an operator decision, separate from retiring the code path.

**2. Authorization lives in the router.** `acl`, `acl_membership`,
`auth_sessions`, `auth_users` are in `routd.db`. authd owns `auth.db` and holds
`identities`, `oauth_identities`, `refresh_tokens`, `signing_keys`,
`identity_claims`. So the daemon whose entire job is auth does not hold the
authorization rows, and `auth.Authorize` — the sole evaluator — reads them out of
the router's file.

**3. `auth_users` is duplicated across two owner DBs.** It exists in BOTH
`auth.db` and `routd.db`, and five packages write "it": `onbod/main.go`,
`proxyd/main.go`, `authd/http.go`+`store.go`, `dashd/profile.go`,
`routd/budget.go` — each hitting whichever DB its handle opened. Both are empty
on krons today, so this is a latent split-brain, not live divergence. Nothing
prevents divergence the moment both get rows.

This is the root cause of Y1's confusion and of proxyd reading another daemon's
tables: a daemon needing an auth answer must go where the auth rows happen to
sit, which is routd.

**Proposal.** Move `acl`, `acl_membership`, `auth_sessions`, and the single
canonical `auth_users` into `auth.db` under authd. proxyd then answers identity
by offline-verifying authd's ES256 token — the mechanism every other daemon
already uses (`5/1`) — instead of opening routd.db. The one thing forcing a DB
read is the opaque **session cookie**; have authd's login set a JWT-bearing
cookie and proxyd's identity reads disappear entirely. Afterwards, narrow the
mount per daemon at `compose/compose.go:812`, which turns the write-discipline
from a convention into an enforced boundary.

- **Severity:** high (architectural; latent split-brain on `auth_users`)
- **Scope:** cross-daemon schema ownership + container mounts
- **Affected:** `compose/compose.go:812`, `proxyd/main.go:869-890`,
  `slakd/main.go:44`, `store/auth.go`, `authd/store.go`, CLAUDE.md's
  write-discipline paragraph, `specs/5/16` owner-DB map
- **Fix:** needs sign-off — cross-daemon schema move + migration per instance.
  Do not start piecemeal; `5/8` and `5/16` should be rewritten against the
  outcome, not before it.

### The `auth_users` collision decomposes — there is nothing to merge (2026-08-02)

The two tables are not drifted copies of one table; they model different
things and were wrongly given the same name.

`auth.db.auth_users` — `user_id` PK, `name`, `created_at`. The OAuth identity
record, FK'd from `oauth_identities`.

`routd.db.auth_users` — `sub`, `username`, `hash`, `name`, `created_at`,
`linked_to_sub`, `cost_cap_cents_per_day`. Checked column by column:

- **`hash` is dead; `username` is NOT.** CORRECTED 2026-08-02 — the original
  claim here ("username + hash are dead") was wrong, produced by running the
  evidence grep through `| head -6`, getting six test-file hits, and concluding
  from a truncated list. `onbod/main.go:784` writes `UPDATE auth_users SET
  username = ?` inside `createWorldTx`, and `:593` reads it on every
  `GET /onboard` — a failed scan renders "User not found." and returns, so
  dropping the column breaks onboarding for every user rather than degrading a
  dashboard. `hash` is genuinely dead: no `SELECT` anywhere, written only by
  `store/auth.go:29,96` to satisfy `NOT NULL`. Delete `hash`; keep `username`
  and carry it into `user_profiles`.
- **`cost_cap_cents_per_day` is live but is not identity** —
  `store/cost_log.go:87,98` read/write it as a per-user spend cap (the same
  column also exists on `groups`). It belongs on a budget table keyed by sub,
  not on an identity table.
- **`sub`/`name`/`created_at`/`linked_to_sub`** are identity, duplicating
  authd's row.

So the resolution is not a merge: strip the dead credential columns, move the
budget column to its own table, and the remainder consolidates into authd's
`auth_users` — reconciling `sub` with authd's `user_id`. The name collision
disappears instead of being resolved.

**Timing.** CORRECTED — "both tables hold zero rows" was checked on krons ONLY
and generalised. Real counts (read-only):

| instance | `routd.db` `auth_sessions` | `routd.db` `auth_users` | `auth.db` `auth_users` |
| -------- | -------------------------- | ----------------------- | ---------------------- |
| krons    | 0                          | 0                       | 0                      |
| sloth    | 5                          | 3                       | 0                      |
| marinade | 3                          | 2                       | 1                      |

sloth's rows are real Google identities; marinade has `local:admin` plus one
Google row. `cost_cap_cents_per_day` is 0 everywhere. So the migration must
carry data on two of three instances.

### `auth_sessions` is not writer-less either (CORRECTED 2026-08-02)

`routd/db.go:110` `copyLegacyProxydTables` `INSERT OR IGNORE`s into
`auth_sessions` from the legacy `messages.db` on **every routd boot** — that is
production code writing the table, contradicting the claim that nothing writes
it. What does hold: nothing MINTS a session post-split (`CreateAuthSession` has
only test callers), every row is backfill (counts match `messages.db` exactly),
and all rows are long expired — newest `expires_at` is 2026-06-05 — so the
cookie branch still cannot match in production.

**Correct order, which the drop-first plan had backwards:** remove
`auth_sessions` from `copyLegacyProxydTables` first, so the backfill stops and
the rows go inert; then drop the table in a later migration once every instance
reads zero. `specs/5/16`'s own migration gate already forbids dropping while an
instance is non-zero.

Note what that writer implies more broadly: routd still opens the frozen
`messages.db` at every boot to copy legacy rows forward. That is the real
blocker for retiring `messages.db`, and it is bigger than any single table.

## ✅ FIXED 2026-08-15 (CLOSED-STALE — fixed in 32b180da, entry never closed) — E1 — five adapters keep state in unmounted paths; it dies on every recreate (2026-08-03)

Found by the mount audit. `teled`, `bskyd`, `reditd`, `linkd` and `emaid` each
persist state under a data-dir path, and their compose fragments declare
**`volumes:` zero times** — verified on `template/services/{teled,bskyd,reditd,linkd,emaid}.yml`,
and `sudo docker inspect arizuko_teled_krons` returns an EMPTY mount list. The
state lives inside the container layer and is destroyed on every recreate.

| daemon | state | code |
| ------ | ----- | ---- |
| teled  | Telegram update offset | `teled/bot.go:60` (`loadOffset`), written `:66-101` |
| bskyd  | `bluesky-session.json` | `bskyd/client.go:80`, written `:92,95` |
| reditd | `cursors.json` | `reditd/client.go:78`, written `:88,90` |
| linkd  | `linkd-state-<name>.json` | `linkd/client.go:95,122`, written `:143,161` |
| emaid  | `emaid.db` (a whole SQLite DB) | `emaid/store.go:22`, created `:19-38` |

**teled is the user-visible one.** `loadOffset` is
`data, _ := os.ReadFile(b.cfg.StateFile)` — the error is swallowed, so a missing
file silently returns offset 0. Telegram treats `getUpdates` offset 0 as "send
everything still queued" and retains ~24h, so a recreate can re-deliver up to a
day of updates and the agent answers them all again. Silent by construction: no
log line marks the reset.

`emaid` is the structural oddity — it opens a SQLite database at a path nothing
mounts, so it silently starts empty each time.

Note `reditd` and `emaid` are not even given `DATA_DIR`, so their paths resolve
relative to the container working dir.

Correcting a related claim recorded earlier in **A1**: the universal
`<dataDir>:/srv/app/home` rw mount comes from `compose/compose.go:811` in
`writeSvc`, which emits ONLY the eight core daemons (authd, routd, runed, timed,
onbod, dashd, proxyd, webd). The 10 channel adapters plus davd/ttsd/vited are
NOT emitted there — they are standalone fragments included verbatim
(`compose.go:384`), each declaring its own volumes or none. So "one line hands
every daemon every database" is true of the core eight only, and narrowing the
adapters is a per-fragment edit, not one code change.

- **Severity:** high for teled (duplicate agent replies after any recreate);
  medium for the rest (re-auth / re-scan / lost dedup state)
- **Scope:** compose fragment volumes for channel adapters
- **Affected:** `template/services/{teled,bskyd,reditd,linkd,emaid}.yml`,
  `teled/bot.go:60`
- **Fix:** 32b180da — give each fragment the narrow rw mount it needs
  (`whapd.yml:9` and `twitd.yml:9` already show the pattern — a single subpath,
  not the data dir), set `DATA_DIR` where absent, and make `loadOffset` fail
  loud instead of swallowing the read error.

**CLOSED-STALE 2026-08-15. Every claim in this entry is false at HEAD: the fix
landed in `32b180da` and the entry was simply never closed.** Re-verified point
by point:

- `grep -c 'volumes:'` on
  `template/services/{teled,bskyd,reditd,linkd,emaid}.yml` returns **1 for
  each**, where the entry says "declares `volumes:` zero times". Each is the
  narrow subpath the fix line prescribed, e.g.
  `${DATA_DIR}/store/teled:/srv/app/home/store/teled`.
- All five fragments now set `DATA_DIR` explicitly, including the two the entry
  singled out as not even having it (`reditd`, `emaid`), and all five daemons
  carry a matching code default (`chanlib.EnvOr("DATA_DIR", …)`).
- `loadOffset` no longer swallows the read error. It separates the three cases
  (absent file → genuine first run, offset 0; unreadable → error; unparseable →
  error), and `teled/main.go` returns that error out of `Start` with the
  comment saying why, so `chanlib.Run` exits rather than polling from offset 0
  and re-answering Telegram's ~24h backlog.

The LIVE half is a different entry and stays open: deployed instances include
their OWN `/srv/data/arizuko_<inst>/services/*.yml`, not `template/`, so krons
still runs the pre-fix fragment. That is `F53` (and `R1` finding 1) — fragment
relink on deployed instances, not this repo's templates.

## R1 — codex review of the 2026-08-03 fix batch: six gaps (2026-08-03, 5 of 6 closed 2026-08-15; 1 open on live-state only)

Adversarial review of `50af5f50..e37a084b` (the Y2/M1/E1/D6/D7 fixes). Findings
verified before logging. Ordered by consequence.

**1. CRITICAL — the adapter-mount fix is inert on every live instance.**
`compose/compose.go:383-402` includes each instance's OWN
`/srv/data/arizuko_<inst>/services/*.yml`, not `template/services/`. Confirmed
on krons: that directory holds installed copies ("Converted from
services/teled.toml by `arizuko generate`"), including a second adapter
`teled-rhias.yml`. Editing the template fixes NEW installs only —
krons/sloth/marinade still have no adapter state mounts, so teled still replays
~24h of Telegram updates on recreate. `cmd/arizuko/packages.go:347-375` copies
templates only on `packages add`; there is no update path for installed
fragments. The installed copies are also still emitting the retired
`CHANNEL_SECRET`.

**2. teled's swallow moved from read to write.** The startup read now fails
loud through `chanlib/run.go:90-94`, but `teled/bot.go:92-94` only warns when
the SAVE fails, advances the in-memory offset, and continues. A full or
read-only mount looks healthy until restart, then replays from the last
offset that actually persisted.

**3. The dissolved-tier class is incomplete.** `resreg/resources/route_tokens.go:86`
still tells `issue_webhook` "Tier rules match issue_chat_link", and — worse —
the agent's own global instructions assert tier ceilings at `ant/CLAUDE.md:28`
and `:165-166`. Those reach the model on every turn.

**4. The invite-bearer fix is YAML-only.** `yaml:"-"` does stop `export`
(`resreg/engine.go:359-375`), but `json:"token"` remains, so the live token is
still returned by `GET /v1/invites` (`onbod/admin.go:53-72`), rendered in full
by dashd (`dashd/invites.go:72-84`), printed by the CLI
(`cmd/arizuko/main.go:732-743`), and published in OpenAPI
(`resreg/openapi.go:122-143`). Y2 fixed the manifest path, not credential
exposure generally.

**5. `SkipApplyRebuild` now yields false advice.** `cmd/arizuko/apply.go:165-169`
assumes every skipped resource is a secret and prints "set via `arizuko secret
set`" — nonsense for invites.

**6. The vacuous-target class remains.** `webd/Makefile:10-11` still runs
`-run E2E` with no zero-match guard; only the root Makefile was hardened.

Minimality notes (not defects): `shouldLink` is a one-line wrapper with a
dedicated 21-line test the renderer test already covers; commit `32b180da`
bundles three narrowings with the persistence fix and `e37a084b` bundles dashd
with Make.

- **Severity:** high (1 leaves the reported bug live on all three instances)
- **Fix:** 1 needs an installed-fragment update path — a real `arizuko` command,
  not a manual edit, since it must run on every instance and survive the next
  `generate`. 2-6 are small and independent.
- **Status:** 2026-08-15 — each of the six re-verified SEPARATELY at `bcb5bb83`.
  Five are closed, one (1) is closed in code and unverifiable here. Verdicts:

  1. **Closed in code; live state unverified.** The mechanism claim still holds —
     `compose/compose.go:764` writes `./services/<name>.yml`, i.e. the
     instance's own dir, never `template/services/`. But the update path R1
     demanded now exists as a real command: `arizuko packages upgrade` (one
     package) and `arizuko packages sync` (all) both re-fetch the recorded
     source and rewrite the installed fragment through ONE updater,
     `reapplyPackage` (`cmd/arizuko/packages.go:371`), which refuses to
     overwrite a locally edited asset instead of clobbering it. `template/
     services/teled.yml` carries the `${DATA_DIR}/store/teled` state mount and
     no `CHANNEL_SECRET`. What cannot be checked from this worktree: whether
     krons/sloth/marinade have actually RUN it, and whether hand-made variants
     like `teled-rhias.yml` carry an `installed_packages` record for `sync` to
     reach. Both are live-host questions.
  2. **Closed.** teled no longer swallows the save. `maxSaveFailStreak = 3`
     consecutive `saveOffset` failures now call `fatalExit` (`teled/bot.go`),
     so a full or read-only mount crashes the process instead of looking
     healthy until restart. A single failure is still logged and tolerated,
     which is correct — a transient write hiccup must not crash-loop the poll
     goroutine.
  3. **Closed.** Both halves. `resreg/resources/*.go` contains no tier language
     at all (the `route_tokens.go:86` sentence is gone), and `ant/CLAUDE.md`
     now states the opposite of what R1 found: lines 4, 35 and 173 each say
     "5/33: no tiers" explicitly. Repo-wide, nothing outside this queue's own
     history says "Tier 0-2 only" or "Tier 0-1 only". See `M1` for the last
     residue of this class, fixed in the same pass.
  4. **Closed by a schema change, not by a serializer tag.** `json:"token"` is
     gone because the FIELD is gone: migration
     `store/migrations/0077-invites-hash-at-rest` made `ref` (=
     `hex(sha256(token))`) the primary key, so the raw bearer is never
     persisted and `InvitesRow` has no token field to leak by omission. dashd
     renders `ref`. The only surviving raw token is
     `toInviteCreatedJSON`'s mint-time response (`onbod/admin.go`), which is the
     one moment the caller must receive it.
  5. **FIXED 2026-08-15** — see the commit closing it. It was real: FIVE
     resources carry `SkipApplyRebuild` (secrets, route_tokens, invites,
     onboarding, installed_packages), and `printPlan` printed the secrets
     command for all five.
  6. **Closed.** `webd/Makefile` `test-e2e` captures the output and `exit 1`s on
     "no tests to run", with a comment naming the root Makefile's guard as its
     mirror. A selector matching zero tests can no longer pass as green.

## V2 — dashd duplicates control planes the cockpit specs move away — CHAT PORTAL + MINT FORM DELETED 2026-08-15; the rest BLOCKED on `6/30` (2026-08-04, partial)

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

### BLOCKED on `6/30` — do not delete these yet

Verified against each daemon's route registration, not the spec's intention.
No daemon but onbod and timed has a `/dash/` route at all
(`compose/compose.go:367-370`); dashd answers `/dash/routd/` and `/dash/runed/`
from the catch-all.

| dashd surface                                        | LOC (verified)              | what `6/30` must ship first                                                                                                       |
| ----------------------------------------------------- | --------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| routes + tokens list/revoke + errored-retry           | 304 + 145 + 173 (`routd_page.go`) | routd's `/dash/routd/` — routd serves no `/dash/` namespace and compose has no route to it                                    |
| `runed_page.go` runs + kill                           | **289**, not the 526 claimed | runed's `/dash/runed/` AND `GET /v1/runs?state=`. runed's mux is 8 routes and none lists runs (`runed/server.go:39-46`); there is no `ListSpawns` in the tree, so dashd's direct `spawns` read is the only run list |
| tasks list/detail/create/pause/resume/cancel          | 323 (`tasks_admin.go`) + `main.go` list | timed's task + task-detail pages. `/dash/timed/` is ONE handler, the overview (`timed/split.go:42`), with no controls        |

Deleting any of these today removes the capability outright. Order stands:
build the per-daemon page, then delete dashd's copy.

### The cron duplication is real and ALREADY DRIFTED

Canonical is **timed's** (`timed/main.go:33-57`) — `6/30` §timed: "timed owns
the fire runtime … and all cron/interval computation". There are three copies,
not two, and the drift is live: routd's `parseTaskSchedule`
(`scheduled_tasks_resource.go:285`) accepts an RFC3339 one-shot and computes in
the process's local time; dashd's `taskNextRun` (`tasks_admin.go:20,36`)
rejects that same expression with 400 and computes in `TZ`. So a schedule the
agent accepts, the dashboard refuses. dashd's copy dies with its tasks page
(blocked above); routd's needs to converge on timed's semantics when timed's
pages land — filed as the follow-up, not fixed here, because it is a second
concern.

## ✅ FIXED 2026-08-15 N2 — an operator can grant to a non-canonical alias, silently (2026-08-04)

Residual risk from N1 (`beecf595`), verified as having **zero live instances**
today but no guard against acquiring one.

authd now resolves a linked OAuth login to the account's canonical sub
(`auth_users.user_id`) at mint. Grants are keyed on provider subs. So if Alice's
canonical is `google:X` and she links `github:Y`, an operator who grants to
`github:Y` creates a row that **can never match**: Alice's JWT always carries
`google:X`, and `expandPrincipals` compares exact strings.

Same shape for pairing: an `acl_membership.parent` naming a non-canonical alias
is unreachable — and unreachable to unpair too, since the contain seam compares
the caller's canonical sub against the stored parent.

Verified fleet-wide: `oauth_identities` is EMPTY on krons and sloth, and
marinade's single row is its own canonical (`user_id` = `provider:provider_sub`).
sloth's two granted principals (`google:114015391913260915382`,
`discord:user/811295670702047272`) appear in no link table, so each resolves to
itself. **Nothing is stranded today**, and nothing can be until someone links a
second OAuth identity — which no one has.

- **Severity:** low now, medium once anyone links a second login
- **Fix:** reject (or loudly warn on) a grant or pairing whose principal is a
  known non-canonical `oauth_identities.provider_sub`. authd already answers
  this — `oauthIdentityForSub` returns the owning account — so the check is a
  lookup, not new state. Do NOT silently rewrite the principal: moving a grant
  without the operator asking is the failure mode N1 chose an immutable
  canonical to avoid.
- **Status:** open — PROPOSAL, needs sign-off. Premise re-verified 2026-08-15
  at `bcb5bb83` and it holds.

**"The check is a lookup, not new state" understates it: the lookup crosses a
daemon.** `oauth_identities` lives in **auth.db** and `oauthIdentityForSub` is
unexported in package `main` of **authd** (`authd/store.go:116`). The grant and
pairing writes are **routd**, against **routd.db** (`routd/acl_resource.go`
`add_acl`). routd holds no handle on auth.db — `routd/sibling_db.go:13` states
identity is federated over HTTP, never opened — so the check is a cross-daemon
call, not a local lookup.

The transport exists: `GET /v1/identities/{sub}` returns the canonical identity
plus every linked sub, and routd already calls it via `httpIdentity.Resolve`
(`routd/identity.go:47`). Canonicality is then `identity.id != principal`.

**Why it still needs sign-off rather than being contained.** `Resolve` is
deliberately advisory: EVERY failure — transport, auth, non-200, decode —
returns the unclaimed shape, never an error, because its only caller today is
an inspect tool. Making `add_acl` REFUSE on its answer puts a hard dependency
on authd into a write path that has none, and the fail-open/fail-closed choice
is the whole decision:

- fail-CLOSED (authd down → no grants) turns an authd blip into a control-plane
  outage, over a risk with zero live instances.
- fail-OPEN (authd down → grant proceeds) is a guard that stops working exactly
  when identity data is unavailable, which is a guarantee worth less than it
  reads.

Recommending fail-OPEN with a LOUD warn on both the unreachable case and the
non-canonical case, on the grounds that N2 is a data-hygiene guard and not a
containment boundary — but that is the user's call, and either way `Resolve`
must grow a distinguishable "could not answer" result instead of folding it
into "unclaimed". Do not ship this inline.

- **Fix:** `22e16615` — built as recommended: fail-OPEN, both cases warned.
  `Server.nonCanonicalWarning` (`routd/acl_resource.go`) runs on the `add` case
  of the ONE shared `aclHandler`, so the agent's `add_acl` and the operator's
  `POST /v1/acl` get it from the same line. `IdentityResolver.Resolve` grew the
  distinguishable fourth result the entry required: `(identity, subs, claimed,
  error)`, where a 404 or a null identity is a real "unclaimed" (`false, nil`)
  and a token/transport/other-non-200/decode failure is an error. It never
  rewrites the principal — moving a grant the operator did not ask to move is
  the failure mode N1 chose an immutable canonical to avoid.

**The warning rides on the RESPONSE, not only the log.** A fail-open guard whose
only output is a log line is a guard nobody sees, and `add_acl` is a user-facing
path. `add` now returns `aclAddResult{ok, warning omitempty}` — the wire shape is
byte-identical to the old `{"ok":true}` when there is nothing to say, and when
there is, the caller is told in the same breath the grant lands. Both cases
warn: a known alias names the canonical sub to re-grant to, and an unreachable
authd says plainly that it could NOT verify, rather than implying the principal
checked out.

`inspect_identity` keeps its advisory behaviour (an error still renders as
unclaimed) but now logs instead of swallowing — that tool cannot distinguish
"authd is down" from "nobody claims this sub" in its output either way.

**Not covered, and deliberately:** the pairing half. `acl_membership.parent`
naming an alias has the same shape, but it is a different write path and a
different concern; folding it in here would be two fixes pretending to be one.
Still open as recorded above.

- **Verified:** `routd/acl_canonical_test.go` — an alias warns and names the
  canonical; canonical, unclaimed, `role:` principals and a nil resolver are all
  silent; an unreachable authd warns that it could not verify; and the grant
  still lands on the principal AS ASKED, with nothing written to the canonical.
  `routd/identity_test.go` gained the 404-is-unclaimed, 5xx-is-an-error and
  dead-authd-is-an-error cases that the old advisory `Resolve` could not express.

## Z1 — all three live instances run a routd/onbod image ~5 days stale; a chunk of shipped migrations is not live (2026-08-04, open)

Found verifying `83cccc78` (the 5/8 CAS/repoint) against read-only `.backup`
copies of the three live instance DBs (never touched the originals). Every
instance's `migrations` table stops at `routd` version **19** and `onbod`
version **2**, dated 2026-07-12 and 2026-06-06 respectively — but the
codebase's `routd/migrations/` goes to **0027** and `onbod/migrations/` to
**0003**, with 0021 (2026-07-30), 0023/0025/0026/0027 (2026-08-01 through
2026-08-04) and onbod 0003 (2026-08-03) all added AFTER the running image.
`sudo docker inspect arizuko_routd_krons` shows image `30c7bd65…`, tagged
`arizuko:latest`, **created 2026-07-30 07:42**; `sudo systemctl status
arizuko_krons` shows the container restarted 11h ago (2026-08-04 10:39) —
so the SERVICE restarted recently, but on the SAME stale image, and no new
migration ran because the binary inside it doesn't contain them. Identical
version numbers (19 / 2) on krons, sloth, AND marinade — one shared stale
image, not a per-instance drift.

Concrete consequence for code shipped this week: `route_tokens.kind`
(migration 0026, 5/31 pairing-vs-route distinction) and onbod's invites
hash-at-rest `ref` PK (migration 0003) do not exist on any live routd.db/
onbod.db yet — `resreg.Export`/`Checksum` fail outright on those two
resources against live data today (`no such column: kind` / `no such column:
ref`), confirmed by the same verification run. This is NOT a bug in
`83cccc78`'s `route_tokens`/`invites` RowFilter changes — the code is correct
against the schema the codebase defines and against a freshly-migrated DB
(`routd.OpenMem()`, which the CI-equivalent tests use) — it is a **deployment
gap**: this week's migrations, and everything else shipped since the 2026-07-30
image build, are not protecting any live user.

- **Severity:** high (a growing list of shipped fixes — pairing grants,
  role-member 4R, user-profiles, drop-linked-to-sub, invites hash-at-rest —
  is not live on any instance)
- **Scope:** deployment / image freshness, all three instances
- **Affected:** krons, sloth, marinade (`routd`, `onbod`)
- **Source:** `.backup` copies verified 2026-08-04 21:4x UTC; live `migrations`
  table + `docker inspect`/`systemctl status` (read-only checks)
- **Fix:** rebuild + redeploy (`sudo make images` then `sudo systemctl restart
  arizuko_<instance>` per instance) — an operator action, not attempted here
  per "never touch a live instance." Once redeployed, re-verify `route_tokens`/
  `invites` export against real data (this session's synthetic-schema tests
  already cover the post-migration shape; only the live-data path was blocked).

## ✅ FIXED 2026-08-15 Z5 — `network_rules`' seed migration uses `CURRENT_TIMESTAMP`, making two fresh instances' checksums permanently unequal (2026-08-04)

Found chasing a flaky archive test (a cross-agent report of ONE failure led to
a 50-run `-count` sweep, not dismissed as a fluke). `routd/migrations/0005-
network-rules.sql` seeds two default rows (`anthropic.com`, `api.anthropic.
com`) with `created_at = CURRENT_TIMESTAMP` — SQLite's own wall-clock `now()`,
evaluated at MIGRATION-RUN time, second resolution. `network_rules` is a
registered `resreg` resource (`resreg/resources/network_rules.go`) whose row
content — `created_at` included — is part of `resreg.Export`/`Checksum`'s
projection for every `routd.db`, unconditionally (not just for resources a
given manifest mentions). Two independently-bootstrapped `routd.Open` calls
(the exact shape `cmd/arizuko/apply_test.go`'s `openInstance` helper uses
everywhere) therefore get DIFFERENT `network_rules` seed timestamps whenever
the two migration runs cross a wall-clock second — same-second runs
coincidentally match, which is why this was never caught before: every
existing cross-instance test (`TestCLI_ExportApply_RealFiles` and this
session's own archive tests) already uses `--force` for its apply, which
skips the checksum comparison entirely and never exercises this path.

`CreatedAt` is declared `StampedFields: []string{"CreatedAt"}` on this
resource, so `Diff`/`plan`'s comparison already correctly ignores it — this
is NOT a live bug in ordinary `apply`/`plan` output today. It only bites the
content-hash CAS `Checksum` computes (spec 5/8's gate for whether `apply`
requires `--force`), which hashes the raw `Export` projection with no
StampedFields exclusion. `routd/migrations/0022-seed-operator-grant-
option.sql` seeds a comparable default row (`role:operator`'s base ACL grant)
and gets this right — a fixed literal `'2026-07-30T00:00:00Z'`, not
`CURRENT_TIMESTAMP` — so the inconsistency is between two migrations solving
the same "seed a default row" problem, not a property of seeding itself.

- **Severity:** low (no live-instance impact — a real instance's
  `network_rules` seed timestamp never changes after first boot, so its OWN
  checksum stays stable across repeated exports; this only bites a scenario
  comparing checksums ACROSS two independently-bootstrapped instances without
  `--force`, which no shipped code path does today) — but a real round-trip-
  honesty gap in the same class as Z2, and a landmine for the next test or
  tool that tries a no-force cross-instance config compare
- **Scope:** `routd/migrations/0005-network-rules.sql`; the general question
  of whether `Checksum` should exclude `StampedFields` the way `Diff` already
  does (Z2's `payloadEqual` fix, if it lands, is the natural place to also
  fix this — both are "the hash is more sensitive than the comparison the
  rest of the engine already treats as canonical")
- **Affected:** any code comparing two independently-migrated `routd.db`
  checksums without `--force`; `cmd/arizuko/archive_test.go`'s
  `TestArchive_Apply_SkipsNonEmptyFolderUnlessForce` worked around it by
  testing `tarGroups`/`extractGroups` directly instead of the full checksum-
  gated `applyArchive` pipeline
- **Source:** `routd/migrations/0005-network-rules.sql`,
  `resreg/resources/network_rules.go:73`, verified 2026-08-04 by a 50-run
  `-count` sweep with the migration file moved aside vs restored
- **Fix:** `493b280e` — `routd/migrations/0037-network-rules-seed-timestamp.sql` normalises
  the two system-seeded rows to the fixed literal `2026-08-04T00:00:00Z`,
  matching 0022's pattern. Scoped by exact PK plus `created_by='system'`, so an
  operator who re-added either target keeps their own stamp. 0005 is left
  untouched (already applied everywhere); a fresh install runs 0005 then 0037
  and lands on the same bytes as an existing instance.

**The `Checksum`/`StampedFields` question is DECIDED: no.** `Checksum` keeps
hashing the exact `EmitYAML(Export(...))` bytes. Excluding `StampedFields` there
would break the property the CAS rests on — that the hash is the hash of the
document the operator holds — and would leave two renderers where 5/8 requires
one. `Diff` ignores stamped fields because it answers a different question (did
INTENT change); the hash answers "is the content still what I read". A
non-deterministic seed is cured by making the seed deterministic, not by making
the hash blind. Z2's gap stands on its own and is NOT closed by this.

Also surfaced by the fix: `CURRENT_TIMESTAMP` was writing `2026-08-15 20:16:08`,
SQLite's space-separated form — every other stamp in the projection is RFC3339,
so the seed was off-format as well as non-deterministic. The literal is RFC3339.

- **Verified:** `cmd/arizuko/seed_determinism_test.go` — two instances
  bootstrapped >1s apart must agree on `resreg.Checksum`, plus a direct assert
  on the literal so a future `CURRENT_TIMESTAMP` re-seed cannot pass by landing
  inside one second. Mutation-checked: both tests FAIL with 0037 moved aside
  (`a = sha256:e2c741d7…` vs `b = sha256:de29e07a…`) and pass with it restored.

## ✅ FIXED 2026-09-20 L121 — adapter registration blocks on an unbounded `net.LookupIP`, so a slow resolver stalls a routd handler for the platform's whole DNS timeout (2026-08-27)

`chanreg.validateAdapterURL` (`chanreg/chanreg.go:319`) resolves the adapter's
hostname with the package-level `net.LookupIP`, which takes no `context.Context`
and no deadline:

```go
ips, err := net.LookupIP(host)
if err != nil || len(ips) == 0 {
	return nil        // unresolvable is allowed on purpose
}
```

`Register` → `RegisterWithOrigin` → `validateAdapterURL` is on routd's
`POST /v1/channels` path, so the handler goroutine is held for however long the
resolver takes. The answer is then discarded whenever the lookup fails, which is
the common case for a compose service name resolved from outside its network.

- **Measured**, this tree, `chanreg`:

  ```
  hostname register took 7.771698649s     // http://teled:8080
  ip register took       12.965µs         // http://10.1.2.3:8080
  ```

  That is one lookup against a resolver that cannot answer. `glibc`'s default is
  5 s × 2 attempts per `nameserver` line; a `resolv.conf` with three
  nameservers gives ~30 s per registration.
- **Cost:** an adapter restarting into a routd whose DNS is degraded re-registers
  slowly, and every concurrent registration holds a goroutine for the same
  window. Nothing crashes; the control plane just stops answering for a while,
  which reads as "routd is hung" rather than "DNS is slow".
- **It also cost the test suite.** `chanreg` took **151 s** in `make test`
  — documented as "fast unit tests (<5s)" — because 23 registrations each waited
  out the resolver. Fixed 2026-08-27 by registering private IP literals in
  `chanreg_test.go`, which skip the lookup: **151 s → 7.7 s**. The remaining
  7.7 s is `ssrf_test.go`'s one deliberate `docker_service_name` case. That is a
  test fix, not a fix for this entry — the production path is unchanged.
- **Source:** `chanreg/chanreg.go:319`; callers `chanreg/chanreg.go:108,112`.
- **Status:** fixed 2026-09-20.
- **Fix:** `validateAdapterURL` resolves through `net.DefaultResolver.LookupIP`
  under a 2 s `adapterDNSTimeout`. The caller's context was not threaded:
  `Register`/`RegisterWithOrigin` take none, and adding one changes a public
  signature and every caller for no extra guarantee — the bound is the whole
  benefit. The branch already allows an unresolvable name, so a timeout reaches
  the same verdict and no verdict changes.
  `TestAdapterDNSLookupCarriesABound` pins that the resolver is handed a
  deadline, through the `lookupHostIPs` seam, so the check cannot silently go
  unbounded again. Measured: `go test ./chanreg/` 7.7 s → 2.1 s.

## ✅ FIXED 2026-08-29 L122 — six containment filters still hand-write `LIKE ?||'/%'`, so a folder named `%` reads every tenant's messages and a `Coach`/`coach` pair leaks both ways (2026-08-27)

`groupfolder.SQLSubtree` / `SubtreeArgs` exist precisely because SQL `LIKE` is
the wrong predicate for folder containment — it is case-INSENSITIVE for ASCII,
and `_` and `%` are wildcards. The cascades adopted it (BUGS `L66`). **Six read
paths never did:**

| site | what it scopes |
| --- | --- |
| `routd/reads.go:114` | the agent's `find_messages` — message CONTENT |
| `store/messages.go:766` | the same FTS search on the store side |
| `audit/query.go:110` | the audit log |
| `store/inspect.go:23` | errored chats |
| `store/routes.go:221` | the route table |
| `dashd/grants_admin.go:135` | the folder picker |

- **Measured**, `audit.Query` against rows in `coach`, `Coach/private`,
  `a_b/one`, `aXb/two`, `x/y`:

  ```
  filter "coach" -> [Coach/private coach]        # LIKE is case-insensitive
  filter "a_b"   -> [aXb/two a_b/one]            # _ is a single-char wildcard
  filter "%"     -> [x/y aXb/two a_b/one Coach/private]   # every folder with a /
  filter "_"     -> [x/y]
  ```

- **`isValidFolder` permits all three.** `groupfolder/folder.go:68` rejects only
  `..`, `\`, NUL, empty segments, >128-char segments and reserved names. There is
  no character allowlist, so `%`, `_` and `Coach` are all legal folder names, and
  the caller's own folder is what binds the placeholder.
- **The case half is not hypothetical.** `groupfolder/folder.go:196` records that
  a live instance HAS carried a `Coach`/`coach` pair; that is what motivated
  `SQLSubtree` in the first place. Those two folders read each other's messages
  today through `find_messages`.
- **Cost:** a tenant placed at a folder named `%` reads every other tenant's
  message content, audit trail, routes and errored chats. A tenant whose folder
  differs from another only in case, or by one character where theirs has `_`,
  reads that neighbour. Nothing is logged as a denial, because nothing was
  denied.
- **Fix (local, mechanical):** replace each of the six with
  `groupfolder.SQLSubtree(col, "/")` + `groupfolder.SubtreeArgs(folder, "/")`.
  `store/routes.go` needs the `#` separator form as well as `/`. The helper is
  exact, case-sensitive and metacharacter-free, and it is already the one
  implementation the cascades share — this is adoption, not new code.
- **Second, independent fix:** give `isValidFolder` a character allowlist. A
  folder name is a path segment, a network-name component and a SQL scope; `%`
  is none of those. That does not replace the first fix (the case half survives
  an allowlist), and it is a behaviour change for any existing folder, so it
  needs sign-off separately.
- **Source:** the six rows above; helper at `groupfolder/folder.go:191-211`;
  validator at `groupfolder/folder.go:68`.
- **Test:** `audit/query_test.go` `TestQueryFolderFilterIsNotWidenedByLikeMetacharacters`
  is written and skipped on this entry. Un-skip it with the fix.

Fixed 2026-08-29, the adoption half. All six sites now call
`groupfolder.SQLSubtree`/`SubtreeArgs`; `store/routes.go` uses both the `#` and
the `/` form, and `dashd/grants_admin.go` skips the folder itself in the loop
because the picker already offers it as the selected option. `grep "LIKE ?||'/%'"`
over the tree now returns only the helper's own doc comment.

Tests: the audit test is un-skipped and gains the `%` and `Coach`/`coach` rows;
`store/subtree_filter_test.go` covers the message search, the errored-chat list
and the route table on the same three metacharacter cases, and asserts the
folder's OWN subtree still reads. Mutating `SQLSubtree` itself back to a `LIKE`
turns all four tests red, which is what proves the six sites share one
predicate now.

**The `isValidFolder` allowlist stays open** — it is the second, independent
fix named above and a behaviour change for existing folders, so it still needs
sign-off. The case half survives an allowlist either way.

## ✅ FIXED 2026-08-29 L123 — an agent that stringifies a numeric tool argument has it silently DROPPED, while the REST twin of the same call accepts it (2026-08-27)

`resreg.Args.Int` documents the contract: MCP decodes numbers to `float64`,
REST query params arrive as strings off `url.Values`, and "both must land on
the same number or a filter silently ignores itself on one face"
(`resreg/resreg.go:133-137`). `Int` honours it — it parses a string. The MCP
DECODER in front of it does not.

`numArg` (`resreg/resreg.go:833`) accepts `float64`, `int` and `int64` and
returns `ok=false` for anything else, and `decodeMCPArgs`
(`resreg/resreg.go:760`) only writes the key when `ok`. So the argument never
reaches `Args` at all, and the handler reads its own default.

- **Measured**, this tree:

  ```
  MCP  {"limit":"50"}    -> present=false Int=0
  REST ?limit=50         -> present=true  Int=50
  MCP  {"dry_run":"true"} -> true          # GetBool DOES coerce the string
  ```

  The same logical call gives 0 on one face and 50 on the other, and MCP is
  internally inconsistent: `bool` coerces a stringified value, `number` drops it.
- **This is the exact failure mode the tool-schema literature describes.**
  Stringifying a numeric argument is one of the common ways a model gets a tool
  call slightly wrong. Nothing tells it: no error, no warning — the tool runs
  with a different limit, offset or count than the agent asked for.
- **Cost:** silent wrong-value on the agent's whole cold-tier surface. A dropped
  `limit` reads as "no limit given", which for several handlers means the
  package default and for others means 0.
- **Source:** `resreg/resreg.go:760` `decodeMCPArgs`, `:833` `numArg`;
  contract at `:133`.
- **Two local fixes, pick one:**
  1. **Coerce, matching REST and matching `GetBool`'s existing behaviour**: add
     a `case string:` to `numArg` that `strconv.ParseFloat`s and reports `ok`
     only on success. Three lines; makes the two faces agree.
  2. **Reject loudly**: return an argument error naming the parameter and the
     type it needed, so the agent can retry. More correct, more churn — every
     caller of `decodeMCPArgs` has to carry an error.

  Silently dropping is the one option that must not stay.
- **Test:** `resreg/args_test.go` `TestDecodeMCPArgsMatchesRESTOnAStringifiedNumber`
  is written and skipped on this entry. Un-skip it with fix 1, or rewrite it to
  assert the error with fix 2.

Fixed with option 1: `numArg` (`resreg/resreg.go`) gains a `case string:` that
`strconv.ParseFloat`s and reports not-ok on a value that is not a number, so
the two faces land on one number and `number` now behaves like `bool`, which
already coerced. `TestDecodeMCPArgsMatchesRESTOnAStringifiedNumber` is
un-skipped and fails with the case removed.

## ✅ FIXED 2026-08-29 L124 — reditd wraps chanlib's 429 retry in a second one, so one logical call makes NINE requests at a rate limiter (2026-08-27)

`chanlib.DoWithPolicy` (`chanlib/retry.go:47`) already retries 429: three
attempts, honouring `Retry-After` "if present and <= 30s". `reditd.doWithRetry`
(`reditd/client.go:176`) calls it inside its OWN three-iteration 429 loop, with
its own `Retry-After` parse clamped at `maxRetryAfter = 5 * time.Minute`
(`reditd/client.go:174`).

The two layers multiply:

```
HTTP requests=9  elapsed=8.76s  err="rate limited after 3 retries"
```

measured in this tree against an endpoint answering `429` + `Retry-After: 1`.

- **Cost:** nine requests at a rate limiter for one poll is how a Reddit app key
  gets suspended — the limiter's whole purpose is to make the client back off,
  and this backs off nine times harder in requests. The outer clamp also
  contradicts the inner one: chanlib caps the honoured `Retry-After` at 30s,
  reditd's outer loop at 5 minutes, so a `Retry-After: 120` is ignored by the
  inner layer and slept by the outer, three times — up to 15 minutes past any
  `/health` staleness tolerance (`pollStaleAfter` is 15 minutes, so the adapter
  can flip to 503 while it is merely waiting).
- **Not a duplicate of the retry rule.** The repo's policy is "retry ONLY
  transient errors"; 429 IS transient, and both layers are individually
  correct. The bug is that there are two.
- **Source:** `reditd/client.go:176-212`; inner policy `chanlib/retry.go:37-90`.
- **Fix (local):** drop reditd's 429 branch and let `chanlib.DoWithPolicy` own
  it — the outer loop then keeps only the 401-refresh-once retry, which chanlib
  does not do and which is the real reason this wrapper exists. If reditd needs
  a longer honoured `Retry-After` than chanlib's 30s, raise it in ONE place
  (a policy field) rather than by stacking a second loop.
- **The correct pattern already ships, in `slakd`.** `slakd/bot.go:1697-1707`
  calls `chanlib.DoWithPolicy` and then does NOT retry: a 429 that outlived the
  policy's budget becomes a typed `chanlib.ErrNotDelivered` so the sweep can
  retry it. One retry layer, one owner. That is the shape reditd should take.
- **Siblings checked 2026-08-27:** `slakd` correct (above); `discd` classifies
  discordgo's own rate-limit errors and does not add an HTTP retry loop;
  `bskyd`, `teled`, `twitd`, `whapd`, `linkd` and `emaid` carry no 429 retry
  branch of their own. reditd is the only one stacking.
- **Test:** `reditd/client_state_test.go`
  `TestDoWithRetryDoesNotStackTwoRetryLayers` is written and skipped on this
  entry. Un-skip it with the fix.

Fixed as proposed, taking slakd's shape: `doWithRetry` (`reditd/client.go`) now
calls `chanlib.DoWithRetry` once, keeps ONLY the 401-refresh-once retry that is
the real reason the wrapper exists, and turns a 429 that outlived the policy's
budget into `chanlib.ErrNotDelivered` — a documented refusal the dedup sweep may
retry — instead of sleeping and re-sending. reditd's contradictory 5-minute
`maxRetryAfter` clamp is gone; chanlib's 30-second cap is the only one.
`TestDoWithRetryDoesNotStackTwoRetryLayers` is un-skipped and caps one logical
call at 3 requests.

## ✅ FIXED 2026-09-03 L125 — `6/9` is `status: shipped` and teaches an egress mechanism that `6/24` deleted, including the registration whose leak was the reason to delete it (2026-08-27, status half FIXED 2026-08-29, rewrite FIXED 2026-09-03)

**FIXED 2026-09-03.** The spec now describes what exists, each claim checked
against the code rather than the old text: the package is `sandbox/` (`crackbox/pkg/`
holds only `run`), `VMConfig` is the real five fields with `AllowList` and no
`EgressProxy`, and the egress section states netfilter-on-the-tap and says in
words WHY there is no registration — an allowlist held by a registration
outlives a crashed consumer, which is the leak `6/24` deleted. The acceptance
criterion that asked for `crackbox proxy serve` + register/unregister is
replaced by one that asks for the tap rules as the evidence. `status: defected`
lifted; the wiring gap (no daemon imports it) is unchanged and still stated,
because it is true.

`specs/6/9-crackbox-sandboxing.md` still describes the KVM sandbox as it stood
before 2026-08-18. Measured at `7d81cd78`:

- **The import path in it does not exist.** The spec names
  `crackbox/pkg/host/` **12 times** (`:10`, `:17`, `:28`, `:33`, …). `ls
crackbox/pkg/` returns `run` and nothing else — the library moved to
  `sandbox/` when `6/24` shipped.
- **The struct in it does not exist.** `:70` documents
  `EgressProxy string   // proxy admin URL; empty = no proxy`. The real
  `sandbox.VMConfig` (`sandbox/sandbox.go:39-45`) has five fields and no
  `EgressProxy`; enforcement is netfilter rules on the VM's own tap device.
- **The flow in it is the leak `6/24` was written to kill.** `:91-100`:
  "`Spawn` reads the `EgressProxy` field … POSTs `/v1/register {ip, id,
allowlist}`. `Stop` POSTs `/v1/unregister`." `6/24` §"It removes a real leak"
  deleted exactly that: a consumer-held unregister invariant means a crashed
  consumer leaves an allowlisted IP registered forever.
- **Severity:** medium, and it is a security-doc fault rather than a typo. The
  spec reads `shipped`, so an engineer wiring the KVM backend into `runed` — the
  one task the spec exists for — starts by looking for an admin API that was
  deleted, and the obvious way to make the spec true again is to reintroduce the
  registration. `sandbox/README.md:41-49` already carries the correct model.
- **Source:** `specs/6/9-crackbox-sandboxing.md:10,17,28,33,70,91-100,133`;
  `sandbox/sandbox.go:39-45`; `sandbox/README.md:41-49`; `6/24` §"It removes a
  real leak".
- **Fix:** rewrite `6/9`'s body against `sandbox/` and the netfilter model, or
  demote its status. Which one is the operator's call: the LIBRARY did ship, so
  `shipped` is true of the thing and false of the description.
- **Also stale in the same file, separately:** `:130` claims "`pkg/host/` has
  zero arizuko-internal imports (component test, per `6/16`)". No such test
  existed. Written 2026-08-27 as `tests/components`; the invariant did hold.

**Half done 2026-08-29.** `6/9` now carries `status: defected` + `defects:
[L125]`, the recorded convention for a spec whose text an open bug contradicts.
That removes the specific harm named above — the spec no longer reads `shipped`
to an engineer wiring the KVM backend, so the "make the spec true again" path
no longer points at reintroducing the deleted registration.

**Still open, and deliberately not decided here:** whether to rewrite the body
against `sandbox/` + the netfilter model or to demote the spec. The entry says
that is the operator's call, and it stays that way.

## ✅ FIXED 2026-08-27 L126 — `6/7`'s conformance matrix says pi's skill-guard is a load-time scan; the code says write-time and blocks, so the matrix understates two of three backends' defences (2026-08-27)

`specs/6/7-environment-contracts.md:749` — the `skill-guard (5/23)` row reads
`write-time hook | load-time scan | load-time scan` for claude / codex / pi.
The prose above it at `:721-722` is blunter: "**pi** … No hook mechanism."

Both are false at `7d81cd78`:

- `ant/src/pi.ts:205` declares `guard: 'write-time'`, with the comment "the
  extension's tool_call handler blocks a dangerous write before it lands".
- `ant/src/pi-extension.ts:87-90` registers a real pre-execution hook:
  `pi.on('tool_call', …)` returning `{ block: true, reason }` on a `deny`
  verdict, over the shared `bindings()` table — the same mechanism class as
  claude's `PreToolUse`, not a scan.
- `PI_HONOURS` (`ant/src/pi-extension.ts:16`) is `['deny','inject']`, so the
  skill-guard row is honoured rather than dropped.

The codex cell is wrong in the other direction — "load-time scan" is not what
protects it. codex's `hooks.json` wires the `PreToolUse` skill-guard row, but it
structurally never fires because codex writes with `apply_patch`, not
`Write`/`Edit` (`ant/src/policy.ts:85-88`). What actually catches a codex skill
write is the no-matcher `skill-rescan` `PostToolUse` row (`ant/src/policy.ts:87`,
`ant/src/guards.ts:83-137`) — a continuous quarantine sweep after every tool
call, not a one-shot scan at session start.

- **Cost:** the matrix is what a reader consults to decide whether a backend is
  safe to run unattended. It currently says pi has no hook at all, when pi
  refuses a dangerous write before it lands; and it says codex is gated at load
  time, when a codex-authored skill is quarantined AFTER the write. Someone
  debugging why a codex skill was quarantined instead of blocked, or refusing to
  run pi unattended, reasons from a false model in both directions.
- **Root cause:** a same-document reconciliation miss. The matrix and the
  "Skills across backends" prose were written in `819efd9e` (2026-08-20). The
  one-policy-table mechanism shipped the next day in `52cef0aa` (2026-08-21) and
  correctly updated a *different* section of the same file (the renderer list
  around `:407`), leaving two sections of one spec disagreeing with each other.
- **Source:** `specs/6/7-environment-contracts.md:721-722,749` vs
  `ant/src/pi.ts:205`, `ant/src/pi-extension.ts:16,87-90`,
  `ant/src/policy.ts:81,85-88`, `ant/src/guards.ts:83-137`.
- **FIXED 2026-08-27.** Both cells and the prose now say what the code does: pi
  reads `write-time hook (pi-extension.ts)`, codex reads `post-write quarantine
  sweep`, and the "Skills across backends" section states that codex's
  `PreToolUse` row never fires because it writes with `apply_patch` — the
  `skill-rescan` `PostToolUse` row is what protects it. The load-time scan is
  described as what it is: the backstop on both, not the gate.

## ✅ FIXED 2026-08-29 L127 — six of nine daemons install their SIGTERM handler AFTER they start serving, so a signal in that window kills them with no graceful shutdown (2026-08-27)

Measured at `7d81cd78` by comparing the first `net.Listen`/`Serve` line with the
first `signal.Notify` line in every `main.go`:

    LATE  authd/main.go              listen@116  notify@123
    LATE  egress/cmd/egress/main.go  listen@71   notify@89     <- FIXED 2026-08-27
    LATE  onbod/main.go              listen@156  notify@162
    LATE  routd/cmd/routd/main.go    listen@272  notify@280
    LATE  runed/cmd/runed/main.go    listen@148  notify@155
    LATE  ttsd/main.go               listen@53   notify@60
    ok    dashd/main.go              notify@330  listen@337
    ok    proxyd/main.go             notify@1012 listen@1021
    ok    webd/main.go               notify@88   listen@136

Between the listener accepting and `signal.Notify` returning, a `SIGTERM` takes
Go's **default disposition** — the process dies immediately and the
`srv.Shutdown(ctx)` written directly below never runs.

- **This is not theoretical; it broke the suite.** `egress/cmd/egress`'s
  `main()` test polls `/health`, then signals. On 2026-08-27 it produced
  `signal: terminated` and `FAIL` mid-run, having already logged `egress up` and
  a correct `deny http` — the signal landed in the window. Fixed for egress by
  moving the two `signal.Notify` lines above `net.Listen`; `go test -count=20`
  then passes.
- **Severity:** low-to-medium and narrow. The window is sub-millisecond and only
  a signal arriving inside it is lost, so the realistic exposure is a fast
  restart loop or a `systemctl restart` racing a just-started unit. What it
  costs when hit: routd and runed drop in-flight work instead of draining, and
  the deploy path in `CLAUDE.md` is `systemctl restart arizuko_<instance>`.
- **The fix is not a redesign.** `dashd`, `proxyd` and `webd` already register
  first; the fix is moving two lines in each of the remaining five to match the
  pattern already in the tree. No new mechanism, no contract change.
- **Source:** the table above; the correct precedent at `dashd/main.go:330`,
  `proxyd/main.go:1012`, `webd/main.go:88`.
- **Fix:** move `stop := make(chan os.Signal, 1)` + `signal.Notify(...)` above
  the first `net.Listen` in `authd`, `onbod`, `routd`, `runed` and `ttsd`.
  Not done here — found during a general check (Bug Triage Protocol); only the
  egress half shipped, because its own test was red.

Fixed 2026-08-29: the two lines moved above the listener in all five remaining
daemons, matching the shape dashd, proxyd and webd already had. The ordering is
a source fact, so `tests/shutdown` reads it from the source the same way the
defect was measured: `TestSignalHandlerIsArmedBeforeTheListener` compares the
first `signal.Notify`/`NotifyContext` line with the first listener line in each
of the nine `main.go` files, and it covers any daemon added later. Moving one
back turns the test red.

## ✅ FIXED 2026-08-27 L128 — `ARIZUKO_BACKEND` never reached runed, so the documented per-backend certification procedure was a silent no-op (2026-08-27)

`specs/6/7` §"Conformance: `anteval`, per backend" documents the procedure:

    ARIZUKO_BACKEND=codex  # instance .env; restart; then, manually:
    anteval run --cases ./anteval/cases ...

**It does nothing.** Measured on krons while running that exact procedure:

- runed's compose service loads **only** `env/runed.env` (`env_file:` in the
  generated `docker-compose.yml`), never the instance `.env`.
- `ARIZUKO_BACKEND` was **not in runed's key allowlist** (`compose/compose.go`
  `daemonKeys["runed"]`), so `arizuko generate` never copied it across.
- Set in `/srv/data/arizuko_krons/.env`, restarted, and `env/runed.env` came
  back with 22 keys and no `ARIZUKO_BACKEND`. runed kept spawning claude.

runed is the ONLY reader: `core/config.go:213` resolves it into
`cfg.AgentBackend`, and `buildArgs` re-emits it as container env for ant's
`selectHarness` (`ant/src/index.ts:425`). A test already asserted the SECOND
hop (`container/extra_test.go:131` — "ARIZUKO_BACKEND died precisely because
buildArgs never emitted it"), so the same class had been fixed one hop
downstream and left broken one hop up.

- **Cost:** codex and pi could not be certified at all, on any instance. That
  is the direct reason `6/7`'s matrix says only claude has a live anteval
  history, and why two of its conformance rows were written from recorded
  fixtures instead of a live run.
- **This is the `ant_env_delivery` class again** — a var not emitted is
  silently dead, and the failure looks exactly like a healthy instance.
- **FIXED**: `ARIZUKO_BACKEND` added to `daemonKeys["runed"]`, with a test that
  fails when it is removed.
- **Found beside it, not a code fault:** the deployed `arizuko:latest` predated
  `ANTHROPIC_OAUTH_TOKEN` joining `store.EnvProfileKeys`, so pi had no
  credential on an instance that holds one under the other name
  (`store/secrets.go:118` states this exactly). The image rebuild that shipped
  the fix also closed that. `Z1` class: the running image is behind the tree.

## ✅ FIXED 2026-08-27 L129 — switching `ARIZUKO_BACKEND` does not switch the model, and routd's default is a hardcoded claude id, so every codex turn 400s (2026-08-27)

Found running the per-backend certification `specs/6/7` documents, immediately
after `L128` made the switch take effect at all.

`ARIZUKO_BACKEND=codex` selects the harness. The MODEL is independent:
`routd/cmd/routd/main.go:191` is `envOr("ARIZUKO_DEFAULT_MODEL",
"claude-opus-4-8")`, and `codexArgs` passes `turn.model` verbatim as `-m`. So a
correctly-switched instance sends a claude model id to codex, and every turn
comes back:

    Result #1: ok=false text={"type":"error","status":400,"error":
    {"type":"invalid_request_error","message":"The 'claude-opus-4-8' model is
    not supported when using Codex with a ChatGPT account."}}

Measured live on krons: 100% of turns failed this way until
`ARIZUKO_DEFAULT_MODEL=gpt-5.6-sol` was set. With it set, the same instance
scored **7/9** on the codex smoke.

- **Unsetting the var does not help** — the fallback is the hardcoded claude id,
  so there is no configuration that leaves the model to the harness's own
  default. `6/7`'s seam says `model?: string` and "absent = the harness's own
  default"; routd can never express absent.
- **Severity:** medium. It is not a data fault and it is loud, but it makes the
  documented procedure produce a 100%-failing instance, and the failure is
  attributed to the backend rather than to the config. The smoke's cost is
  failure-dominated (`anteval/README.md`), so it also burns a full wall cap per
  case while proving nothing.
- **FIXED 2026-08-27 with option 1, let absent mean absent.** The fallback is
  now `""`; an unset `ARIZUKO_DEFAULT_MODEL` reaches ant as falsy and each CLI
  picks its own default, which is what the seam already promised
  (`ant/src/backend.ts:29`, "backend-native id, verbatim"). Option 2 (a
  backend→model map) was declined: it puts vendor model ids in routd to restore
  a property the contract already had.
- **Live impact: none.** All three instances — krons, marinade, sloth — set
  `ARIZUKO_DEFAULT_MODEL=claude-opus-4-8` explicitly, so nothing changes for
  them; the change only makes "unset" mean something.
- **The guard is a SOURCE assertion**, because the config is a struct literal
  inside `main()` with nothing to call. The first version of the test called
  `envOr` with an empty fallback and passed just as happily with the vendor id
  restored at the call site — it caught neither its own mutation nor the bug.
- **Source:** `routd/cmd/routd/main.go:191`; `ant/src/codex.ts:65`;
  `specs/6/7` §"The harness seam" (`model?: string`, backend-native, verbatim).
- **Docs:** `6/7`'s certification procedure names only `ARIZUKO_BACKEND`. It
  now also names the model; the spec text was corrected in the same pass.

## L130 — a codex or pi agent never receives arizuko's operating manual, so it fails exactly the tasks that need platform knowledge (2026-08-27, open — proposal, needs sign-off)

Diagnosis of the two failures in the first live codex certification (7/9,
krons 2026-08-27). Both share one signature — `unsupported protocol scheme ""`,
a callback URL the agent never produced — and one cause.

**What each backend receives as instructions:**

| | claude | codex | pi |
| --- | --- | --- | --- |
| `~/.claude/CLAUDE.md` (33,511 bytes, the operating manual) | yes — Claude Code reads it natively | **no** | **no** |
| `AGENTS.md` (codex's own convention) | n/a | **does not exist anywhere in the repo** | n/a |
| `systemPrompt` | `SYSTEM.md` + output style | same | same |
| skills | native | prompt index | `--skill` dirs |

`buildSystemPrompt` (`ant/src/index.ts:49`) is `ci.systemMd` + the output style,
and `systemMd` is the group's optional `SYSTEM.md`
(`container/runner.go:531`) — **not** `CLAUDE.md`. claude gets the manual only
because Claude Code discovers it by itself. Nothing puts it in front of codex or
pi.

**Measured on the eval folder during the run:** `SYSTEM.md` ABSENT,
`AGENTS.md` ABSENT, `.claude/CLAUDE.md` present at 33,511 bytes and mentioning
`private_html` **7 times**. So the file containing the exact recipe the failing
case needed sat unread in the group home while the agent guessed.

- **`priv-401`** asks for a JWT-gated page. The `~/private_html` → `/priv/<folder>/`
  mapping is arizuko-specific and lives only in that manual. `web/priv/eval/`
  holds an artifact from every previous claude run and **nothing** from the codex
  run, while `web/pub/eval/` holds the codex nonce — so the agent published
  public pages happily and never found the private slot.
- **`priv-grant`** burned 671,679 tokens over 180s and never called back — the
  same shape: no manual, so no recipe for the grant + publish chain.
- **The pass/fail split is exactly this line.** codex passed every case whose
  task is self-describing from the prompt (`chat-entrypoint`, `child-delegate`,
  `fresh-context`, `link-is-public-url`, `rest-roundtrip`, `self-skill`,
  `webhook-in`) and failed both that require platform knowledge.
- **Contract impact:** `6/7` states the agent contract as what the environment
  guarantees an occupant. Two of three occupants never receive the document that
  defines how to operate it. That makes the conformance matrix read as backend
  capability when it is partly instruction delivery.

**PROPOSAL — needs sign-off, because it is a contract change with a real cost.**
`codexPrompt`/`runPi` can prepend the manual the same way `renderSkillsIndex`
already prepends the skills index — one call site each, no new mechanism. The
cost is ~33KB of input per turn per backend, which is why this is a decision and
not an inline fix: it is cheap against codex's cached input and not free. The
alternative, writing `AGENTS.md` into the group home for codex's own discovery,
keeps it out of the prompt but adds a second copy of one declaration — the
duplication rule says amend the original instead.

- **Source:** `ant/src/index.ts:49`, `container/runner.go:531`,
  `ant/src/codex.ts:84` (`codexPrompt`), `ant/src/pi.ts` (`runPi`);
  the measurement above.

## L131 — pi's bash tool inherits the whole process env, so every model credential is readable from the agent's shell (2026-08-28, claude half FIXED; pi half open — candidate 1 DISPROVED 2026-09-03, replacement mechanism identified, not shipped)

Found auditing the policy layer across all three harnesses. **This entry was
filed wrong the first time and is corrected here**: the original claimed codex
was exposed too, reasoning "the key is in the process env, therefore the shell
sees it" without checking whether each CLI filters its own shell env. codex
does. pi does not.

**CORRECTED 2026-09-05: the harness filter is not a boundary, and the table
below only ever described accidental leakage.** Every process in the agent
container runs as the same uid (`container/runner.go:718`, `--user 1000:1000`),
nothing sets `hidepid`, and `/proc/<pid>/environ` is readable across siblings at
equal uid. Measured: a sentinel planted in one process was read from another
inside `arizuko-ant:latest`. So the agent's shell reads the harness's own env
whatever that harness strips from the child it spawns — `cat /proc/1/environ`
defeats codex's `shell_environment_policy` and claude's `bash-scrub` alike.
All three occupants are exposed; pi is only the one that does not try.

The fix is upstream of every harness and it SHIPPED 2026-09-05: with
`cfg.ModelGatewayURL` set, `container.spawnSecrets` deletes every
`store.EnvProfileKeys` entry from the merged secret map, so no model credential
enters the container and `/proc` holds nothing to read. Scrubbing stays as
defence against an accidental echo. Without a gateway configured the exposure
is unchanged, so 6/7's defect stands for that case.

**The delivery chain is common to all three.** `readSecrets`
(`container/runner.go:765`) iterates `store.EnvProfileKeys` and passes every one
set in the host `.env` through; they ride the stdin payload as
`containerInput.secrets`, and `index.ts:377` merges them into the `env` handed
verbatim to `turn.env`. So all five are in every harness's process env. What
differs is what each harness's SHELL TOOL inherits:

| harness | shell env | verdict |
| --- | --- | --- |
| claude | full, minus what `bash-scrub` unsets | **was 2 of 5 scrubbed — FIXED** |
| codex | filtered by codex itself | **not exposed** |
| pi | `{...process.env}` verbatim | **all 5 exposed — open** |

- **codex is clean, for a reason worth writing down.** `shell_environment_policy`
  is a real `ConfigToml` key, and its default excludes are the literals `*KEY*`,
  `*SECRET*`, `*TOKEN*` (read from the codex-cli 0.147.0 binary alongside the
  `core` inherit set `SHELL`/`TMPDIR`/`TMP`/`LC_ALL`/`LOGNAME`/`LC_CTYPE`, and
  corroborated by the existence of an `ignore_default_excludes` field). All five
  EnvProfileKeys match one of those globs. Basis: static read of the binary, not
  an executed probe.
- **pi is exposed.** `getShellEnv()` (`utils/shell.js:105`) returns
  `{...process.env, PATH: updatedPath}` with no filtering, and `bash.js` passes
  it to every spawn. `bash-scrub` cannot cover it: the row returns `rewrite`,
  `PI_HONOURS` is `['deny','inject']`, so `runnableBy` drops it.

**FIXED (claude half).** `SECRET_ENV_VARS` held 2 of the 5 and nothing said
whether the other 3 were an omission or a decision. It is now a stated
partition: the three ANTHROPIC/CLAUDE credentials are scrubbed (nothing under
`ant/skills` reads them), and `OPENAI_API_KEY`/`CODEX_API_KEY` are deliberately
NOT — the agent's own `/oracle` skill shells out to `codex` and probes exactly
those two, so scrubbing them breaks a shipped skill.
`container/secret_scrub_test.go` pins the partition against
`store.EnvProfileKeys` across the language boundary, so a key added to the
canonical set fails the build rather than landing on the wrong side by
omission. Both directions mutation-tested.

**Candidate 1 is DISPROVED (2026-09-03, measured on pi 0.51.6).** Deleting the
keys from `process.env` inside the extension breaks pi's authentication, because
pi does not read the credential once at startup — it reads it **per request**:
`agent-loop.js:150` calls `config.getApiKey(provider)` on every turn, and
`AuthStorage.getApiKey` (`core/auth-storage.js:223`) walks a five-step priority
list whose step 4 is the environment variable. Delete the variable and the
second model call of the turn has no credential.

The two escapes pi offers cannot be reached from where we stand.
`setRuntimeApiKey` (step 1) and `setFallbackResolver` (step 5) are public on
`AuthStorage`, but `ExtensionContext` (`core/extensions/types.d.ts:169`) exposes
`ui`, `sessionManager`, `modelRegistry`, `model`, `abort`, `shutdown`,
`compact` — and no auth handle. An extension cannot supply the credential by
another route, so it cannot take it out of the env.

**The mechanism that does work is upstream of the extension: never put the
credential in pi's env.** `ant/src/pi.ts:200` spawns pi with `env: turn.env`,
the whole set. pi's step 2 reads `auth.json` at `getAgentDir()/auth.json`, and
arizuko owns the container. Write the credential there and spawn with
`turn.env` minus `SECRET_ENV_VARS`: `getShellEnv()` then has nothing to leak —
by construction, not by a delete that races a re-read — and auth resolves one
step earlier than it does today.

Not shipped, and deliberately. Two unknowns remain that only a live pi can
settle: the `auth.json` entry shape for an OAUTH credential (`{type:"oauth",
expires, ...}`) versus an api key, and whether pi's refresh path rewrites that
file under a container's read-only assumptions. **pi is not the backend on any
instance** — krons, sloth and marinade all run the claude default with
`ARIZUKO_BACKEND` unset — so this leaks nothing today and there is no reason to
ship a credential change verified only by reading. It becomes urgent the day a
folder is switched to pi.

**OPEN (pi half) — proposal, needs sign-off.** The tension is real: the
credential MUST be in pi's own env (that is how pi authenticates) and must NOT
reach the shell pi spawns later. pi offers no `shell_environment_policy`
equivalent and its hook cannot rewrite a tool's input. Candidates:

1. `delete process.env.<key>` inside `pi-extension.ts` after pi initialises its
   provider — the extension is in-process, so children spawned later never see
   them. Changes re-auth behaviour if pi re-reads the env on a provider retry.
2. A `deny` row matching a bash command that references the names — weak
   (trivially evaded by `${A}${B}` concatenation) and it fights the agent.
3. Teach the seam a `scrub-env` verdict pi CAN honour, rendered as (1).

(1) is the smallest, and it is still a control-flow change to credential
handling on a live backend, so it is recorded rather than shipped.

- **Source:** `ant/src/guards.ts` (`SECRET_ENV_VARS`, `bashScrub`),
  `store/secrets.go:115`, `container/runner.go:765`, `ant/src/index.ts:377`,
  `ant/src/pi-extension.ts` (`PI_HONOURS`),
  pi `dist/utils/shell.js:105` + `dist/core/tools/bash.js`,
  codex-cli 0.147.0 binary strings.

## L134 — a `shell:env:` grant cannot be conditioned on the channel, and writing the predicate that looks like it can silently revokes the key everywhere (2026-08-29, open — proposal, needs sign-off)

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

## L133 — `actionCovers`'s `*` covers rule namespaces too, so every namespaced rule needs its own evaluator (2026-08-29, open — proposal, needs sign-off)

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

## ✅ FIXED 2026-08-30 L132 — the GitHub connector `6/26` calls "shipped, use this" cannot run: all three documented commands are absent from routd's image (2026-08-28, fixed)

`6/26` Decision 3 picks the MCP connector over the `gh` CLI and labels it
"(shipped, use this)". The mechanism is shipped; a runnable connector is not.

**The connector subprocess runs inside routd's container** — routd hosts the
agent MCP socket in-process, so `ipc.CallConnectorTool` spawns there.
`template/connectors/github.toml` offers three commands and the runtime image
(`Dockerfile`: `alpine:3.20` + `sqlite-libs ca-certificates docker-cli wget` +
the Go binaries) can run none of them:

| documented command | why it fails |
| --- | --- |
| `npx -y @modelcontextprotocol/server-github` | no node, no npx in the image |
| `github-mcp-server stdio` | "install separately" — not installed anywhere |
| `docker run … ghcr.io/github/github-mcp-server` | routd has NO docker socket |

Measured on marinade 2026-08-28: `docker exec arizuko_routd_marinade command -v
node npx github-mcp-server` exits 127 for all three, and routd's only mount is
`/srv/data/arizuko_marinade -> /srv/app/home`. The docker-socket option is not
an oversight to fix — runed is deliberately the only daemon wired to
`docker.sock`, so handing routd the socket to reach a connector would undo that
boundary for a tool call.

**Corroborating: no instance has ever configured one.** `connectors.toml` is
absent on krons, marinade and sloth, so the connector path has no production
exercise at all. The credential half is already in place — marinade's `secrets`
table holds `folder|atlas|GITHUB_TOKEN` — which is what makes the gap visible:
the token is correctly homed and there is nothing that can spend it.

(The operator ALSO put `GH_TOKEN` and `BK_TOKEN` in marinade's host `.env`,
where they reach no container at all. That is the separate trap `6/26` now
documents; the secrets-table row is the one that counts.)

**Fix — ship a connector runtime, and prefer the Go one.**
`github/github-mcp-server` is a single static Go binary, which matches an image
whose entire payload is already static Go binaries: one more `COPY --from=build`
costs a few MB and no new language runtime. Adding node/npx to the shared
runtime image would bloat every daemon (proxyd, teled, onbod … all run this
image) to serve one connector. Verify the upstream's license and build before
adopting.

Until then `6/26` Decision 3 is aspirational and should not be quoted as a
working path — hence `status: defected`.

- **Source:** `Dockerfile:14-16`, `template/connectors/github.toml`,
  `ipc/connector.go` (`DiscoverConnectorTools` spawns in-process),
  `compose/compose.go` `routdService` ("NO egress proxy, NO docker socket"),
  live probes above.

**Update 2026-08-29 — GitHub is reachable now, by the other door.** `gh` is
installed in the agent image (`ant/Dockerfile:97-102`) and never had a
credential, so the shell-credential tier (`5/13` §Shell credentials) gives the
same capability without the connector: `arizuko group <inst> grant
'folder:atlas' 'atlas' 'shell:env:GITHUB_TOKEN'` puts the existing
`folder|atlas|GITHUB_TOKEN` row in the agent's env, and `ant/src/guards.ts`
`ghWriteGate` refuses the writes it must not make. The shell tier hands the
agent a spendable token; the connector does not, which is why this stayed open
after that shipped.

**RESOLVED 2026-08-30 — and the fix above is NOT what shipped. No
`github-mcp-server` binary is in any image.** Operator decision: the connector
transport moved from a stdio subprocess to HTTP, and the stdio path was deleted
rather than kept as a fallback. Nothing spawns a subprocess any more, so there
is no runtime to install and the whole premise of this entry is gone.

The proposed fix was measured and rejected on its own terms before the switch:

- `github/github-mcp-server` is MIT and does build with our toolchain
  (`go install …@v1.11.0` under `CGO_ENABLED=0`, verified).
- But it is **30.5 MB** (21.3 MB stripped), not "a few MB", against a 537 MB
  runtime image every daemon shares — proxyd, teled and onbod would all carry it
  to serve one connector.
- And it would still not have worked: `DiscoverConnectorTools` spawned with
  `cmd.Env = []string{}`, and `github-mcp-server stdio` **exits 1 before any MCP
  handshake** with "authentication required: set GITHUB_PERSONAL_ACCESS_TOKEN"
  when the environment is empty. Verified by driving the real binary through
  arizuko's own discovery path: `n=0 err=connector "github" initialize: read:
  EOF`. Shipping the binary would have shipped a connector that still discovered
  nothing.

What shipped instead: `[[mcp_connector]]` takes a `url` + a `headers` map, and
the broker renders the same `{secret:KEY}` placeholders into request headers
rather than a process environment. Everything around it is unchanged — the
broker still resolves the folder's secrets narrowed to the connector's declared
keys, still writes the `secret_use_log` row, still scrubs secret values out of
the result. Both shipped connectors now point at hosted endpoints that exist and
answer: `https://api.githubcopilot.com/mcp/` and `https://mcp.buildkite.com/mcp`.

Two consequences worth keeping visible:

- **Discovery is per folder, not per boot.** A remote MCP server authenticates
  `tools/list` (GitHub's answers `401`), and the credential is folder-scoped —
  routd's boot has no folder, so it has no token. `LoadConnectors` therefore
  dials nothing, and each turn's socket build discovers with that folder's
  credential through the same audited broker seam a call uses, recorded as
  `<connector>_tools/list`. A connector that cannot be reached registers no
  tools and is logged at error; it never fails the turn.
- **routd crosses no new egress boundary.** "NO egress proxy" in
  `compose/compose.go` describes the per-spawn AGENT proxy runed owns, not a
  daemon network restriction. routd already makes outbound HTTPS on this exact
  path — surrogate OAuth refreshes tokens against `github.com` at connector-call
  time (`routd/surrogate_refresh.go`). Confirmed live:
  `docker exec arizuko_routd_krons wget -qO- https://api.githubcopilot.com/…`
  returns the protected-resource metadata.

MCP's OAuth 2.1 is now REACHABLE (the spec says an stdio server "SHOULD NOT"
follow it and takes credentials from the environment instead) but is NOT wired:
the connector authenticates with the broker-resolved static token. What OAuth
would still need — RFC 9728/8414 discovery, RFC 7591 dynamic client
registration, and a consent flow with no user present at routd — is recorded in
`5/13` §"OAuth is the next step, not this one".

- **Severity:** medium
- **Scope:** connectors / ipc
- **Affected:** routd, ipc, template/connectors
- **Status:** resolved-not-yet-removed
- **Fix:** see the commit closing this entry

## L135 — `6/16` says the shared registry and the DNS filter are "both LIVE and enforcing"; `6/24` deleted both three days later (2026-08-30, open)

`specs/6/16-daemon-standalone-matrix.md:178-180` is the paragraph that clears
crackbox's ship gate, and its parenthesis names three mechanisms that do not
exist at `b0b299f9`:

> a forward proxy with per-source allowlists and a DNS filter (both LIVE and
> enforcing — `container/egress.go` registers every spawn's IP with its admin
> API) plus the unwired KVM library above.

- **"per-source allowlists"** — the `(source_ip, id, allowlist)` registry was
  `crackbox/pkg/admin` (572 lines), deleted in `79ce6690` (2026-08-18).
- **"a DNS filter … LIVE and enforcing"** — `crackbox/pkg/dns` (753 lines),
  deleted in the same commit, and it was never enforcing anything: at
  `45f3e2b1` it was reachable only from the crackbox binary's own
  `CRACKBOX_DNS_ADDR`/`CRACKBOX_DNS_UPSTREAM` flags. No daemon imported it, no
  `template/services/` fragment started it.
- **"`container/egress.go` registers every spawn's IP with its admin API"** —
  the current `container/egress.go:129` `startEgressProxy` starts a private
  proxy container per spawn with `--client <spawnIP>` and `--allow <list>`,
  and `defer eg.Stop()` (`container/runner.go:204`) tears it down. There is no
  register call, no admin API and no secret; `crackbox/pkg/client` went with
  the registry.

Two more sites in the same file describe the pre-cut shape: the component
table at `:131` (`crackbox | Default-deny egress per source id + KVM sandbox |
CLI + HTTP | Ships now`) and the package table at `:160` (`crackbox | 0 |
ships now | egress proxy + KVM sandbox`). `6/24` reversed exactly that:
`crackbox/` is now a LOCAL DEV command and the shipping pieces are `egress/`
and `sandbox/`. `:171` still points at `crackbox's pkg/host`, a path deleted
with the rest.

This is the same class as `L125` and arguably worse: `L125` is a spec
describing a deleted mechanism, this one asserts a deleted mechanism is
**currently enforcing a security property**. `6/16` carries `status: draft`,
which mitigates it — a draft is not a promise — but it is also the file the
index points at for "what ships", and the sentence is written as a measurement
("both LIVE and enforcing"), not a plan.

- **Severity:** medium
- **Scope:** specs / security documentation
- **Affected:** `specs/6/16-daemon-standalone-matrix.md`
- **Source:** `specs/6/16-daemon-standalone-matrix.md:131,160,171,178-180`;
  `container/egress.go:129`; `container/runner.go:204`; deletion commit
  `79ce6690` (2026-08-18)
- **Status:** open
- **Fix:**

## L136 — the `--internal` network does not seal UDP/53, and the two docs that explain the DNS filter's absence give a reason that does not cover DNS (2026-08-30, open)

`SECURITY.md:103` states the egress guarantee as "Default-deny; the spawn's
network has no route out", and `:~300` as "Non-cooperating clients fail
closed: the internal Docker network". **Measured 2026-08-30, both are true of
TCP and false of DNS.** On this docker host, a container on a
`docker network create --internal` network with no proxy environment:

```
$ getent hosts example.com        → 2606:4700:10::ac42:93f3, exit 0
$ curl -m 8 http://example.com    → http_code=000, exit 28 (timeout)
```

`/etc/resolv.conf` in that container is `nameserver 127.0.0.11` — docker's
embedded resolver, which forwards upstream from the HOST namespace, so it is
not subject to the container's missing default route. TCP is sealed by
topology exactly as documented; UDP/53 is not. An agent can therefore emit
arbitrary query names to a resolver it does not control, regardless of the
folder's `network_rules` allowlist — the classic low-bandwidth DNS side
channel, and precisely what a NXDOMAIN filter on UDP/53 filters.

**This is not a regression from `6/24`.** `pkg/dns` was never wired into any
daemon or compose fragment (see `L135`), so the channel was open before the
cut on exactly the same terms. What `6/24` changed is the explanation. Two
live docs now justify having no DNS filter with a claim that does not reach
DNS:

- `SECURITY.md:345-349` — "a single-tenant proxy already sees every
  destination in the CONNECT line — a rule expressed twice is a rule that can
  disagree with itself."
- `specs/6/24-crackbox-lightest.md` said the same until it was corrected in
  this pass.

A DNS query never appears in a CONNECT line, so the proxy cannot see it and
the two rules cannot disagree — they do not overlap. The reasoning would
license removing a control the proxy does not in fact subsume.

Scoping it honestly: this does **not** breach the boundary `SECURITY.md:49`
names ("a malicious agent in group A trying to reach group B's data, files, or
network") — folder isolation is per-network and holds. It defeats the weaker,
separately stated property at `:332`, "the egress allowlist restricts _where_
the agent can reach", for anything an attacker can encode in a hostname.

- **Severity:** medium
- **Scope:** egress / threat model
- **Affected:** `SECURITY.md`, all instances with `EGRESS_IMAGE` set (krons,
  sloth, marinade)
- **Source:** `SECURITY.md:103,300,332,345-349`; `container/network.go:159`
  (`--internal`); measured docker probe 2026-08-30 (network created and removed)
- **Status:** open — needs a decision, not just a doc edit: either correct the
  docs to scope the guarantee to TCP and accept the channel, or restore a
  UDP/53 filter and wire it (it never was)
- **Fix:**

## L137 — proposal: `sandbox/` is 1342 lines nothing in arizuko runs; the import graph says it can leave cleanly, and the cost of leaving is one 144-line package (2026-08-30, proposal — needs sign-off)

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

- **Severity:** low (no runtime defect; this is packaging and narrative)
- **Scope:** module layout / component boundary
- **Affected:** `sandbox/`, `crackbox/`, `egress/`, `specs/6/9`, `specs/6/16`,
  `specs/6/24`
- **Source:** import graph measured 2026-08-30 (commands above);
  `crackbox/pkg/run/run.go:175,180`; `runed/docker.go:46`;
  `tests/components/imports_test.go`
- **Status:** proposal (needs sign-off) — no split performed, nothing deleted
- **Fix:**

- [x] ✅ FIXED 2026-09-05 — `add_acl` and `POST /v1/acl` never write `grant_option` (`routd/acl_resource.go:290` vs `store/acl.go:16`), so re-delegation is reachable only through `resreg.Apply` — the live grant surfaces cannot express it. `aclArgsRow` reads the argument, `grantACLTx` writes the column and `list_acl` reports it; the Gate needed no change, because `auth.Delegate`'s `grantableRows` already only holds rows the caller has WITH the option. `fix(routd): write grant_option on the live acl grant surfaces`
- [x] ✅ FIXED 2026-09-05 — `webd` registers with chanreg once and sends no heartbeat (`webd/main.go:127`), so three failed probes deregister the `web` channel until the daemon restarts. The heartbeat `chanlib.Run` ran inline is now `RouterClient.Heartbeat`, replaying the registration `Register` already saves; `chanlib.Run` and webd both start it. `fix(chanlib): one heartbeat every registering daemon can run`

## ✅ FIXED 2026-09-05 L177 — every `<reply-to>` rendered `sender="unknown"` with no body; routd never read the excerpt columns back (2026-09-05)

`msgReadCols` (`routd/reads.go`) omitted `reply_to_text` and `reply_to_sender`,
so the 80 excerpts teled stored never reached a prompt, and no other adapter
sends one. Measured in the live transcripts: sloth `main/trading` 374 of 374
real `<reply-to>` lines `sender="unknown"`, krons 2146 of 2146; the only
`sender="bot"` lines are the manual quoting itself. `ant/CLAUDE.md` promised
an excerpt "when the parent is out of the session window" — a rule nothing
implemented, in the monolith either (`api/api.go:331` copied the adapter's
field and stopped). On sloth 1966 of 2069 replies name a parent routd already
holds. The agent's own episodes log filed this on 8/21 as "gateway never fills
`<reply-to>` parent even when in local db".

- **Severity:** medium (the user's load-bearing intent — which message they
  answered — arrived as an opaque id)
- **Scope:** routd message read path + ingest
- **Affected:** every instance, every adapter but teled
- **Source:** `routd/reads.go` msgReadCols, `routd/server.go` handleMessages;
  transcripts 2026-09-05
- **Status:** fixed. The read path scans both columns; ingest fills sender +
  a 200-char excerpt from the stored parent through one `MessageByID` lookup
  that also serves topic inheritance and reply-to-bot promotion.
  `ant/CLAUDE.md` § How messages arrive states the mechanics as built.
- **Fix:** 25f57b15

## ✅ FIXED 2026-09-05 L178 — `fetch_history` wrote platform rows into `messages` as user inbound; 1609 dead or misattributed rows fleet-wide (2026-09-05)

`fetchPlatformHistory` (`routd/mcp.go`) `PutMessage`d every fetched row with
no `BotMsg`, no name, no verb, no chat name. Two faces, both measured on
sloth: (1) the bot's own posts came back under its own Discord user id
(`discord:user/1325081371307278484`, `sender_name` "sloth_re_tardus") as
`is_bot_message=0` — 630 rows marked `is_observed=1`, i.e. the agent read its
own words in `<observed>` as a stranger's; (2) a row inserted after the chat
cursor had passed its platform timestamp is invisible to the poll loop
forever — `1545761586571116545` (2026-09-05 11:44:41Z, the sweep-delivered
copy of the agent's own reply from L169) has `routed_to=''`,
`is_observed=0`, no `turn_context`, no journal line, and is absent from every
transcript of the folder while `inspect_messages` answers it. Cache-shaped
rows (no sender_name, no source, no chat_name): sloth 634, krons 947,
marinade 28.

- **Severity:** high (a second writer of the inbound log with a different row
  shape; the agent's own speech re-enters as a user's)
- **Scope:** routd fetch_history
- **Affected:** sloth, krons, marinade
- **Source:** `routd/mcp.go` fetchPlatformHistory; `routd.db` on the three
  instances 2026-09-05
- **Status:** fixed — rows are rendered, never stored; the `cache` fallback
  reads what routd ingested itself. The rows already written stay (below,
  L181).
- **Fix:** 216196c1

## ✅ FIXED 2026-09-05 L179 — after the typed-JID migration the `<user>` block never found a per-user memory file (2026-09-05)

`senderToUserFileID` kept everything after the scheme, so migration 0042
turned `discord:user/789` into file id `dc-user/789`: the lookup path gained
a directory level and every pre-migration file (`dc-<id>.md`, `tg-<id>.md`,
`sl-<id>.md` on sloth, krons, marinade) stopped resolving. Live: sloth
`main/trading` transcripts inject `<user id="dc-user/162597936323231753" />`
with no `memory=` while `users/dc-162597936323231753.md` exists and was
edited by the agent the same day. Agents then wrote new files by hand under
three spellings. The same function expands a `{sender}` route target, which
became a two-level folder.

- **Severity:** medium (per-user memory silently unreachable since 0042)
- **Scope:** router sender → file id
- **Affected:** every instance, every typed-JID platform
- **Source:** `router/router.go` senderToUserFileID; `users/` listings on the
  three instances 2026-09-05
- **Status:** fixed; the misnamed files are operator data (L181)
- **Fix:** e7b2675e

## ✅ FIXED 2026-09-05 L180 — `<observed>` carried `source=` equal to `chat_id=` on every line (2026-09-05)

3508 of 3508 observed lines in the krons `krons` and sloth `main/trading`
transcripts carried both attributes with the same value. Removed.

- **Severity:** low
- **Scope:** router prompt renderer
- **Affected:** every instance
- **Source:** `router/router.go` FormatMessages
- **Status:** fixed
- **Fix:** 98cb94b7

## L181 — operator data left behind by L178 and L179 (2026-09-05, open — operator task)

Nothing mechanical fixes stored data. Two sets to clean by hand:

1. Per-user files written under the wrong name while L179 was live — rename
   to `<short>-<bare id>.md`: sloth `groups/main/trading/users/dc-user-657972559354265610.md`,
   `groups/main/trading/users/dc-user-811295670702047272.md`,
   `groups/main/main/users/dc-user-811295670702047272.md`; marinade
   `groups/atlas/search/users/sl-user/U4PMARRT6.md` (a subdirectory).
2. The platform rows L178 wrote: `is_bot_message=0 AND sender_name IS NULL
   AND source IS NULL AND chat_name IS NULL` on a `<platform>:` chat — 634 on
   sloth, 947 on krons, 28 on marinade. They answer `inspect_messages`,
   `search_messages` and the 7-day census as user inbound. Deleting them is a
   data fix on three live DBs; sign off before running it.

- **Severity:** low
- **Scope:** instance data
- **Affected:** sloth, krons, marinade
- **Source:** `find … -path '*/users/*.md'` and the `messages` query above,
  2026-09-05
- **Status:** open (operator)
- **Fix:**

## L182 — the sloth host clock is 18 s behind Discord, so every inbound `time=` is in the future of `now:` (2026-09-05, open — infra)

`date -u` on hel1v5 read 12:35:56Z while `https://discord.com` answered
`Date: 12:36:14 GMT`. Every Discord inbound is stamped from the snowflake, so
the transcript shows `now: 12:10:52Z` above `time="12:11:10Z"` on the same
turn; `ago=` reads "now" for a negative duration, and `MessagesBefore(before="")`
(= now) hides the newest 18 s. Not a code defect: NTP on the host.

- **Severity:** low
- **Scope:** sloth host
- **Affected:** sloth
- **Source:** transcript `827e9e0e` 12:11:08Z; `date -u` vs Discord `Date`
  header 2026-09-05 12:36
- **Status:** open (infra)
- **Fix:**

## L183 — a same-second inbound pair split across two turns loses the second message; measured exposure, no loss observed (2026-09-05, proposal)

Adapters stamp whole seconds (`m.Timestamp.Unix()`; 5844 of sloth's inbound
rows have no fraction) and the chat cursor is the trigger's timestamp string
compared with `timestamp > ?`. When message B lands in the same second as A
after A's batch was read, A's turn advances the cursor to that second and B
is never `> cursor` — and a fractional B (`…05.3Z`) sorts BEFORE a
whole-second A (`…05Z`) in the string compare. 133 same-second inbound pairs
exist on sloth. Checked against the transcripts for `#trading` since 08-29:
every non-cache inbound row reached a transcript, so the exposure has not
fired in that window. Shape of the fix: the cursor becomes the row's `rowid`
(routd is the sole writer, the table is append-only), with the timestamp kept
for `new_day` and idle expiry — a cursor redesign, not a patch.

- **Severity:** medium (silent loss of a user message, rare)
- **Scope:** routd dispatch cursor
- **Affected:** every instance
- **Source:** `routd/dispatch.go` processGroupMessages, `routd/db.go`
  MessagesSince; `messages` on sloth 2026-09-05
- **Status:** proposal (needs sign-off)
- **Fix:**

## L184 — `fetch_history` cannot tell the agent which rows are its own (2026-09-05, proposal)

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

## L185 — proposal: `arizuko` runtime verbs as HTTP clients need a CLI principal and an audit-surface decision; refused as a mechanical cut (2026-09-05, proposal — needs sign-off)

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

## L186 — proposal: dashd's admin writes over routd's `/v1` need a forwarder trust routd does not have; refused as a cut (2026-09-05, proposal — needs sign-off)

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

## ✅ FIXED 2026-09-05 L187 — `ARIZUKO_LOCAL_SUB` has no reader (2026-09-05)

Its only reader was the in-runner `ipc.ServeMCP` branch in
`container/runner.go`, which the split never took (runed spawned with the
socket owned by routd) and which is now deleted. `ipc.ServeMCP` still takes
`callerSub`; routd passes the turn's identity, not this env var. CHANGELOG
v0.42.0 and `ant/skills/self/migrations/136-v0.42.0-local-auth-cli-audit.md`
document the variable as a grants-check switch for local sessions; it does
nothing. Either document it as retired or wire it where `arizuko chat` opens
the root socket.

- **Severity:** low
- **Scope:** local-dev MCP identity
- **Affected:** operators who set it
- **Source:** `ipc/ipc.go` ServeMCP callerSub; `container/runner.go`
- **Status:** fixed — retired: the last code mention (`ipc/ipc.go`) is gone; the v0.42.0 CHANGELOG entry and migration 136 stay as history
- **Fix:** 277b1dbf
## ✅ FIXED 2026-09-05 L185 — closes L130: the resident manual reaches codex and pi (2026-09-05)

L130 measured a codex agent on the eval folder with `~/.claude/CLAUDE.md`
present at 33,511 bytes and unread: only Claude Code discovers that file, so
two of three harnesses ran with none of the operating manual and failed
exactly the cases needing platform knowledge (`priv-401`, `priv-grant`).

- **Severity:** high (two harnesses ran without the agent contract)
- **Scope:** ant chassis, harness profiles
- **Affected:** every group on `ARIZUKO_BACKEND=codex|pi`
- **Source:** `ant/src/index.ts` `buildSystemPrompt`, `ant/src/backend.ts`
  `HarnessProfile.manual`
- **Status:** fixed. One declaration, one reader per harness: the seeded
  `~/.claude/CLAUDE.md` (the operator-mergeable copy Claude Code reads
  natively) is the only file. `HarnessProfile` gains `manual: 'native' |
  'rendered'`; claude declares `native` and is never handed the file twice,
  codex and pi declare `rendered` and the chassis reads that same file into
  the system prompt between `SYSTEM.md` and `PERSONA.md`. No `AGENTS.md`, no
  per-harness copy. The per-turn cost is the resident file's size, which is
  why `6/34` trims it in the same change.
- **Fix:** `fix(ant): the resident manual reaches codex and pi`

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
| `register_group` is explicit, absent = ask (`self/mcp.md`)                                    | partial — `world-create-refusal-honest`, `child-delegate` |
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
| `send_file` caption = TL;DR (`send_file` description)                                         | none                                             |

Covered by an existing case and therefore not listed: Network egress
(`egress-diagnose`, `egress-dns-distinguish`, `self-grant-noop`),
Publishing (`priv-401`, `priv-deny`, `priv-grant`, `web-route`,
`spawn-publish-reach`), Agent home / storage (`state-persist`, `priv-401`),
Local paths table (`link-is-public-url`), the map (`self-skill`).

- **Severity:** low (measurement gap, no observed regression)
- **Scope:** anteval cases
- **Affected:** every backend, every group
- **Source:** `specs/6/34-resident-prompt.md` §"The failure mode"; the move
  commit `docs(ant): ant/CLAUDE.md carries only what the rule of 6/34 keeps resident`
- **Status:** open — each row wants a case before the ledger is done
- **Fix:**

## L189 — `sandbox/` writes no netfilter rule for a host-name allowlist entry, and the comment that explains why names a proxy `6/24` deleted (2026-09-05, open — proposal, needs sign-off)

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

- **Severity:** low (nothing in arizuko runs `sandbox/`; `crackbox run --kvm` is local dev)
- **Scope:** `sandbox/internal/netfilter.go`; `specs/6/9` line 70
- **Affected:** `crackbox run --kvm` users who pass host names
- **Source:** `sandbox/internal/netfilter.go` RestoreNetworkRules; `egress/match.LooksLikeIP`; `sandbox/sandbox.go` Profile
- **Status:** open — proposal, needs sign-off
- **Fix:** pick (a), (b) or (c); (c) is ~10 lines in `sandbox.Spawn` plus one spec line

## L190 — proposal: `6/9`, `6/16` and `6/25` §Order restate substrate facts that `substrate.Profile` now declares; make the prose a pointer (2026-09-05, open — docs, needs the spec owner)

`6/25` §3 says the matrix in `6/16` "becomes the doc rendering of a value,
not a second source of truth". The value shipped 2026-09-05:
`substrate.Profile` (an enum-and-bool struct; `TestProfileCarriesNoPolicy`
refuses any other field shape, `TestSubstrateIsALeaf` refuses any arizuko
import), `container.Profile` (docker, mounted_home, no sessions, domain
egress), `sandbox.Profile` (kvm, resident_disk, sessions, address egress),
and `EgressConfig.Effective()` as the one verdict on whether egress isolation
runs — runed logs it at start, refuses to start on a half configuration, and
shows it on `/dash/runed/`.

The prose that still restates those facts, and could not be edited by the
work that shipped the value (`specs/` was out of its write scope):
`specs/6/9` "Architecture" tree and line 70; `specs/6/16` "The four layers"
item 3 and the `dockbox` row of the package axis; `specs/6/25` §Order,
which still reads "1 is adopted" and should record 3 as adopted. Choice
recorded: the code is the only statement. Each paragraph should become "see
`substrate.Profile` (`container.Profile`, `sandbox.Profile`)" with no
restated value, so the next substrate difference is a diff to one struct.

- **Severity:** low (docs drift, no runtime effect)
- **Scope:** `specs/6/9`, `specs/6/16`, `specs/6/25`
- **Affected:** readers of the specs
- **Source:** `substrate/substrate.go`; `container/egress.go` Profile, Effective; `sandbox/sandbox.go` Profile
- **Status:** open — docs
- **Fix:** replace each restated value with the pointer; one edit per spec

## ✅ FIXED 2026-09-06 L191 — the forward-auth transit proof is bounded by the bearer TTL, not by the request (2026-09-05)

**RESOLVED 2026-09-06 in `08693410`: the TTL-bounded property is the design,
because the stronger one is not purchasable.** Two measurements settle it, and
BOTH of this entry's own assumptions were wrong.

**The mint does not ride an existing round trip.** `/auth/forward` makes zero
network calls: `auth.VerifyHTTP` checks the caller's token against a
`RemoteKeySet` built once at boot, `attachTransit` serves the cached bearer,
and `groupsForSub` is a local SQLite read on the FS-mounted `routd.db` (the
`5/16` hot-path exception). The "one subrequest per request" step 3 refuses to
cache is the PROXY's subrequest to arizuko, not an arizuko-to-authd hop. So a
per-request mint ADDS a call to every proxied request and makes authd a
synchronous dependency of the web path, where the cached bearer carries proxyd
through an authd restart today. Benchmarked on loopback with a real ES256
mint: 0.20-0.35 ms against 0.12-0.20 ms for the local verify — roughly double
per decision, and a container hop in production.

**And it would not deliver the property.** `POST /v1/service-token` takes no
TTL argument and mints at `accessTTL`, 15 minutes (`authd/main.go`). A
per-request mint yields a fresh `jti` and the SAME window. `auth.ProxydTransit`
verifies offline and consults no `jti` list, so freshness is invisible to a
backend. Even a short-TTL mint floors at 30 seconds, because `tokenClockSkew`
(`auth/jwks.go:25`) still accepts an expired token inside it. "No token beyond
one request" therefore needs a replay cache in every backend — the per-request
lookup `5/16` rules out.

Two corrections to this entry as written: its Scope named `specs/6/25` §2,
which is about a substrate declaring what it can do and has nothing on the
transit proof; and its Status named `POST /v1/tokens`, which is the downscope
path needing a parent token, not the service mint.

`proxyd/forward_auth.go` copies `Authorization` from `attachTransit`
(`proxyd/main.go:773`), which calls `auth.TokenSource.Token` — the bearer
`auth/service.go` exchanges once and re-exchanges one minute before its
15-minute expiry (`authd/main.go:25`). Every forward-auth response inside one
window carries the same `service:proxyd` bearer. The property the spec stated
— no token an external proxy holds can impersonate proxyd beyond the one
request — does not hold: a proxy host that reads one response can present
that bearer to any backend for the rest of the window. The weaker property
holds: nothing long-lived sits in the proxy's config, and the exposure is
bounded by the TTL. `proxyd/forward_auth_test.go` asserts the proof's
presence, not its freshness; a test asserting a distinct `jti` per response
fails today. `specs/6/22` now states the shipped property; the open choice is
whether to build the stronger one.

- **Severity:** low — no off-the-shelf proxy is cut over; proxyd is the only reader of `/auth/forward` today
- **Scope:** proxyd forward-auth; `specs/6/22` step 3; `specs/6/25` §2
- **Affected:** any deployment that cuts over to Caddy/Traefik `forward_auth`
- **Source:** `proxyd/main.go:773` `attachTransit`; `auth/service.go:59` `Token`
- **Status:** open — proposal: either mint per request through `POST /v1/tokens` (one authd round trip per request, the tax step 3 already accepts for authorization) and pin freshness with a per-response `jti` test, or accept the TTL-bounded property as the design and drop the per-request wording for good
- **Fix:**

## ✅ FIXED 2026-09-19 L192 — `idempotency_keys` has no writer: the table exists, a sweep deletes from it, nothing inserts (2026-09-05)

`routd/migrations/0001-initial-schema.sql:164` creates `idempotency_keys`
with an expiry index; the only statement that names it in Go is the sweep
`DELETE FROM idempotency_keys WHERE expires_at < ?` (`routd/tokens.go:92`).
No `INSERT` exists anywhere in tracked code, and the table holds 0 rows on
krons. Outbound idempotency lives in `chanlib/dedup.go` (`outbound_dedup`),
so the routd table is a leftover, and the sweep is work against an empty
table on every tick. `6/38` cited a `messages + idempotency_keys`
transaction; the 2026-09-05 census found none.

- **Severity:** low (no behaviour; dead DDL and a no-op sweep)
- **Scope:** `routd/migrations`, `routd/tokens.go`
- **Affected:** readers of the schema; the sweep loop
- **Source:** `grep -rn idempotency_keys --include='*.go' --include='*.sql'`; `SELECT COUNT(*)` on krons `routd.db`
- **Status:** fixed — carried away by the DynamoDB move, verified 2026-09-19
- **Fix:** no work: the v0.68.0 store move deleted the table with the rest of
  `routd/migrations/` and the sweep with it. Verified 2026-09-19 — `grep -rn
  idempotency_keys` over `*.go` and `*.sql` matches nothing, and
  `routd/migrations/` does not exist. a migration that drops the table, and delete the sweep — or an insert at the ledger site, if one was meant to exist

## L193 — `7/22` and `specs/index.md` argue from the "27 of 43" census and the storage seam that `6/38` declined (2026-09-05, open — docs, spec owner)

`specs/7/22-rust-track.md` §"Rust buys nothing for the cloud" says the cloud
track "proceeds through the storage seam" and repeats "~27 of 43". `6/38`
now records 26 of 47 and declines the seam: `database/sql` is the seam and
the engines that keep the transactions are off the shelf. `7/22`'s
conclusion — Rust buys nothing for the cloud because the constraint is the
transaction — still holds, but its premise names a component that will not
be built. `specs/index.md` lines 23 and 58 still call phase 7 "the storage
seam".

- **Severity:** low (docs drift, no runtime effect)
- **Scope:** `specs/7/22`, `specs/index.md`
- **Affected:** readers of the phase-7 specs
- **Source:** `grep -n '27 of 43\|storage seam' specs/7/22-rust-track.md specs/index.md`
- **Status:** open — docs
- **Fix:** one paragraph in `7/22` pointing at the `6/38` census; two phrases in `specs/index.md`
## L195 — `store/migrations` is a second declaration of routd's schema that only tests run, and it has drifted on 7 shared tables (2026-09-05, open — proposal, needs sign-off)

Measured at `23b629e0` by migrating both chains into memory and diffing
`pragma_table_info`: `store/migrations/` (88 files, 1855 lines) builds 45
tables; `routd/migrations/` builds 35. Production never runs the store chain
— `store.Migrate`/`store.Open`/`store.OpenMem` have no caller outside
`_test.go` files except `tests/dashd-playwright/seed/main.go` — yet 28 test
files across `auth`, `cmd`, `proxyd`, `resreg`, `routd`, `slakd`, `tests`
and `webd` open it, and `testutils.NewInstance` hands it to
`onbod/integration_test.go`, `webd/integration_test.go`,
`proxyd/integration_test.go` and `tests/onboarding_e2e_test.go`.

The two chains disagree today on every column of `cost_log` (store
`ts,cents,input_tok,output_tok,cache_read,cache_write` vs routd
`recorded_at,cost_cents,input_tokens,output_tokens,turn_id`), on
`system_messages` (store `attrs,created_at,event,group_id,origin` vs routd
`created,folder,kind,source`), and routd-only columns on `messages`
(`delivery_attempts,link_context`), `pending_actions` (`mode,notice_id`),
`secrets` (`expires_at,provider,refresh_val,scope_list`) and `chats`
(`errored`); `user_profiles.linked_to_sub` exists only in store. The store
chain still grows in step with routd's (`0082`/`0083` mirror routd
`0031`/`0032`), so every routd migration is written twice. `testutils.go`
names the failure this buys: "a store-schema fixture lets a genuine
column-name regression pass". `webd/integration_test.go` and
`tests/onboarding_e2e_test.go` read `messages` through the store schema,
which lacks two production columns.

The cut is not mechanical: `store` cannot import `routd` (routd imports
store), so the store package's own unit tests need a schema from somewhere.
Options: (a) move the store-only tables' helpers to their owners and have
`store`'s tests open `routd/migrations` through a test-only embed; (b) keep
one chain under `store/` and have `routd.Create` apply it (undoes the 5/16
per-daemon ownership); (c) freeze the store chain at `0083` and forbid
additions, moving the four `NewInstance` callers to `NewRoutdInstance`.
(c) is the smallest and stops the bleeding; (a) is the one-declaration fix.

- **Severity:** medium (tests certify a schema production does not run)
- **Scope:** `store/migrations/`, `tests/testutils`, four integration tests
- **Affected:** every daemon test on `NewInstance`; the drift is silent
- **Source:** `store/store.go` Migrate/Open/OpenMem; `tests/testutils/testutils.go` NewInstance
- **Status:** open — proposal, needs sign-off
- **Fix:** (c) first; then (a) as its own spec under `6/38`
## L194 — `routd/cmd/routd/main.go` re-declares five defaults `core.LoadConfig` also declares, and one pair already differs (2026-09-05, open — proposal, needs sign-off)

routd builds its `LoopConfig` from `os.Getenv` directly and never calls
`core.LoadConfig`. After the dead-field cut (`core.Config` lost 19 fields no
production code read) eight env vars still carry a default in both places:
`MAX_CONCURRENT_CONTAINERS` (5 / 5 — the pair BUGS F71 already paid for),
`ASSISTANT_NAME` (`Andy` / `Andy`), `MEDIA_ENABLED`, `MEDIA_MAX_FILE_BYTES`,
`WHISPER_MODEL`, `VOICE_TRANSCRIPTION_ENABLED`,
`VIDEO_TRANSCRIPTION_ENABLED` (runed writes them into the agent's media
TOML through `core.Config`; routd reads them for inbound enrichment), and
`APP_SRC_DIR`, where the fallbacks differ: core falls to `HOST_APP_DIR` then
`execDir()`, routd to `HOST_APP_DIR` then `""`. Compose always sets
`HOST_APP_DIR`, so the difference is latent on an instance and live in a
bare `routd` run.

Two readers of one env var are allowed; two declarations of its default
are the F71 shape. The fix is one of: routd calls `core.LoadConfig` and
copies the fields it needs into `LoopConfig` (a control-flow change in
`main`, so it needs the owner's sign-off), or the defaults become named
constants in `core` that both `LoadConfig` and routd's `main` read.

- **Severity:** low
- **Scope:** `routd/cmd/routd/main.go`, `core/config.go`
- **Affected:** routd, runed (media defaults), operators who set none of the vars
- **Source:** `routd/cmd/routd/main.go` NewLoop call; `core/config.go` LoadConfig
- **Status:** open — proposal, needs sign-off
- **Fix:** constants in `core` (~15 lines) or `core.LoadConfig` in routd's main

## L196 — routd's operator dashboard renders to anyone who sets `X-User-Groups`, on a host-published port (2026-09-05, open — needs sign-off)

MEASURED on live krons, read-only GET, no token:

```
curl -o /dev/null -w '%{http_code}'                       http://127.0.0.1:8081/dash/routd/  -> 403
curl -H 'X-User-Groups: ["**"]' -H 'X-User-Sub: audit'    http://127.0.0.1:8081/dash/routd/  -> 200
```

The 200 is the full operator page (`<title>arizuko — routd</title>`). The port
is reachable off-host: `docker port arizuko_routd_krons` is `0.0.0.0:8081`
(`compose/compose.go` emits `'$API_PORT:8080'`, short syntax binds all
interfaces), `ss -ltnp` confirms `0.0.0.0:8081`, and `iptables -S INPUT` is
`-P INPUT ACCEPT` with only a fail2ban jump on 22.

`6/28` §Auth defines two gates: gate 1 verifies the `service:proxyd` ES256
bearer with `auth.ProxydTransit`, and only a daemon that passed gate 1 may
trust the stamped `X-User-*`. Gate 2 ships; gate 1 does not. `auth.ProxydTransit`
has four non-test call sites (`auth/middleware.go`, `dashd/main.go`,
`onbod/main.go`, `webd/server.go`), none of them a `dash.go`. `routd/dash.go`
calls `auth.RequireOperator` → `auth/dashauth.go` `IsOperator`, which reads the
raw header. `auth/dashauth.go`'s own header comment says a caller "MUST verify
that before calling anything here" — seven daemon dashboards do not.

Not probed, because it would be a write: `RequireWrite` adds a `sameOrigin`
check, so whether the page's POST controls (retry, replay, drop, route CRUD,
token revoke) are equally reachable is REASONED, not measured. The read alone
exposes routes, tokens, queue and errored traffic for every tenant.

- **Severity:** high
- **Scope:** `auth/dashauth.go`, `routd/dash.go`, `runed/dash.go`, `authd/dash.go`,
  `timed/dash.go`, `webd/dash.go`, `onbod/dash.go`, `proxyd/dash.go`,
  `ttsd/dash.go`, `chanlib/dash.go`
- **Affected:** krons (measured), marinade and sloth (same image, same compose
  shape — not probed)
- **Source:** `6/28` §Auth gate 1 vs `auth/dashauth.go` `IsOperator`
- **Status:** open — needs sign-off; the fix is a contract change (gate 1 inside
  `RequireOperator`, or every dashboard mount wrapped), which is a redesign
- **Fix:** fold `auth.ProxydTransit` into `RequireOperator`/`IsOperator` so the
  one gate is one gate, and add the negative test (`X-User-Groups` with no
  transit proof → 403) that no dashboard has today

## L197 — six phase-6 specs read `shipped` in frontmatter and `draft`/`defected` in the index, and five of those flips touched no code (2026-09-05, open — docs, spec owner)

MEASURED. Frontmatter vs `specs/6/index.md`, whole directory:

| spec | frontmatter | index row |
| --- | --- | --- |
| `6/12` | shipped | draft |
| `6/23` | shipped | draft |
| `6/28` | shipped | defected |
| `6/29` | shipped | defected |
| `6/34` | shipped | draft |
| `6/35` | shipped | draft |
| `6/36` | shipped | draft |

Every disagreement runs the same direction. Four commits set them, all
2026-09-05, and `git show --stat` says what each changed: `b32262e5` (6/23,
6/22) two frontmatter lines, no code; `d6f17262` (6/12) one frontmatter line,
no code; `b71ec2c2` (6/35, 6/25); `23b629e0` (6/34, 6/36) two frontmatter
lines, no code. None touched `specs/6/index.md`.

Three bodies contradict their own frontmatter in a sentence:
`6/12:13` "Draft / future. No consumer wired yet."; `6/32:15` "Nothing here is
built."; `6/34:14-15` "A proposal, not a decision … nothing moves until this
spec is `planned`."

`tests/specrefs/refs_test.go` checks that a `depends:` entry resolves to a
file. Nothing checks that a status is true, that a body agrees with its own
frontmatter, or that the index agrees with either — so no test can fail on
this, which is why `L154`'s audit and this one found the same class again.

- **Severity:** medium
- **Scope:** `specs/6/*.md` frontmatter, `specs/6/index.md`, `tests/specrefs/`
- **Affected:** anyone reading `specs/6/` to learn what the platform does
- **Source:** `git show --stat b32262e5 d6f17262 b71ec2c2 23b629e0`
- **Status:** open — docs, needs the spec owner
- **Fix:** reconcile each row against the code, then extend `tests/specrefs` with
  the two mechanical checks it can make — frontmatter status equals the index
  row, and a `shipped` spec contains no "not built"/"no consumer"/"nothing
  moves" sentence

## ✅ FIXED 2026-09-06 L198 — two group deletes, each purging a different half (2026-09-05)

**FIXED 2026-09-06 in `2d93482d`, and the scope was FOUR gaps, not two.** Both
paths also left `acl_membership`, `secrets`, `network_rules`, `sessions`,
`group_watchers`, `pending_actions`, `installed_packages` and
`chats.sticky_group`.

Two of those are worse than the `acl` row this entry names. `acl_membership.child`
holds `folder:<path>` — the route-axis principal `auth.Authorize` expands
through `FolderPrincipal` and `Ancestors` — so an edge into a privileged parent
survived and re-conferred on the re-created folder. And folder-scoped `secrets`
survived: marinade holds `folder|atlas|GITHUB_TOKEN` today, so deleting and
re-creating `atlas` hands the next tenant the previous tenant's GitHub PAT.
Verified against the live row.

`store/folder_scope.go` is now the one declaration — 15 scopes rendered through
`groupfolder.SQLSubtree`, run in one transaction, called by `store`, `dashd`,
the CLI and routd's rollback. dashd's own list is gone; its stated reason for
having one ("DeleteGroup opens its own tx and touches only groups") was wrong
on both halves, and the real constraint — needing to know whether a row existed
for its 404 — is met by returning a count.

**The classification is TOTAL over the schema.** `folderScopeKeep` gives every
other table a reason and `folderScopeUnpurged` names what the delete cannot
reach, backed by a completeness test in both `store` and `routd`. It earned
that immediately by catching `invite_redemptions`, which the author had missed.

**The escalation is latent, not exploited.** Zero `acl` orphans on krons,
marinade and sloth. marinade does carry one orphan route, four engagement rows
and two sessions for `libtest` and `atlas/tom` — `atlas/tom` being a CHILD, which
the exact-match `groups` delete could never reach.

Also corrected: `store/groups.go`'s comment claimed the live databases run with
`foreign_keys=0`. `db_utils.FileDSN` sets `_pragma=foreign_keys(on)` and every
daemon opens through it, so `web_routes`, `route_tokens.owner_folder` and
`github_sources` DO cascade — confirmed empirically. The false claim had
narrowed the purge.

The generation (`6/37` redesign 1) is NOT shipped and is the recorded next
step. The purge is atomic today only because `groups`, `acl`, `acl_membership`
and `secrets` share routd.db; `6/37`'s federated split ends that, and the two
`folderScopeUnpurged` entries are already out of reach.

MEASURED. `store/groups.go:112-135` `DeleteGroup` — the delete behind
`arizuko group rm` (`cmd/arizuko/main.go:495`), routd's rollback
(`routd/db.go:147`) and `registerGroup`'s rollback (`routd/mcp.go:217`) —
deletes `groups` (exact), `routes` (`#`-subtree), `chat_reply_state` and
`scheduled_tasks`. `dashd/groups_admin.go:447-491` — the operator's delete —
deletes `groups` (exact and `/`-subtree), `acl` (`/`-subtree), `routes` (`#`-
and `/`-subtree) and the audit row. Neither touches what the other does, and
each path's comment describes the other wrongly: `routd/db.go:143-146` calls
store's "the ONE delete … shared with the operator's `arizuko group rm`";
`dashd/groups_admin.go:444-445` says store's "touches only groups".

Consequence. `arizuko group rm acme` leaves every `acl` row scoped under
`acme/`: the re-created folder inherits the last tenant's admin, the
escalation dashd's own comment names (`:440-443`). The operator's delete
leaves the folder's `scheduled_tasks`, so timed keeps firing into an
unroutable folder — the `L66`/`L67` shape — and its `chat_reply_state`.

- **Severity:** high (privilege escalation on the CLI and rollback paths; the `L66` recurrence on the operator path)
- **Scope:** `store/groups.go`, `dashd/groups_admin.go`
- **Affected:** every instance, both delete paths
- **Source:** `sed -n 112,135p store/groups.go; sed -n 440,491p dashd/groups_admin.go`
- **Status:** open — proposal, needs sign-off: one delete in `store.DeleteGroup` carrying both halves (`groups` exact and `/`-subtree, `acl` `/`-subtree, `routes` both separators, `chat_reply_state`, `scheduled_tasks`, the audit row through `runAudited`), and dashd calling it. `specs/6/37` redesign 1 makes the cascade non-security-bearing by construction.
- **Fix:**

`specs/6/40-store-surface.md:53-67` now collapses this cascade and
`resreg.Apply` into one "versioned replace". The code does not: `DeleteGroup`
uses four table-specific predicates and no version, while `Apply` uses exact
resource scopes plus a subsystem-wide checksum compare-and-swap
(`resreg/engine.go:301-316,786-879`).

## ✅ FIXED 2026-09-19 L199 — `packages add` links a manifest whose non-route keys nothing reads (2026-09-05)

`arizuko packages <inst> add <name>` symlinks or copies the catalog's
`<name>.yml` and `<name>.yaml` into `services/` and applies no row —
`parsePkgManifests` and `installPkgRows` run only under `install`
(`cmd/arizuko/packages.go`, `case "add"` vs `case "install"`). The only
reader of a linked manifest is `generate`, through `compose.ManifestRoutes`,
which takes `manifest[proxyd_routes]` and `continue`s past every other key
(`compose/compose.go:513-516`). So a catalog manifest that declares a
`scheduled_tasks`, `network_rules`, `acl` or `web_routes` row parses cleanly
(`resreg.ParseYAML` accepts any registered resource), links cleanly, prints
"run `arizuko generate` to apply", and delivers nothing. The ten shipped
manifests declare only `proxyd_routes`, so nothing is lost today; the path is
armed for the first manifest that declares more. Same class as `L44` (rows a
package could not express) and `J15` (a payload that applies and reaches no
consumer).

Re-measured 2026-09-19 at `b5243bdb`, two of the three premises no longer
hold. `case "add"` calls `installPackage`, the one install path, so it parses
the manifest and applies its rows through `installPkgRows` exactly as
`install` does — `TestPackageInstallsTaskAndNetworkRule` and
`TestPackagesAddProvisionsEnvAndAppliesRows` both drive a non-route key and
pass. Thirteen manifests ship, not ten, and they declare `proxyd_routes` and
`env` only.

What survived is the same harm one turn further on: the install summary was a
hand-picked list of kinds beside the apply that writes whatever the manifest
engine understands. Measured output for a package declaring a cron and an
egress rule, both live in routd the moment the command returned:

```
installed tnpkg@local (2 file(s), 0 route(s), 0 grant(s)) — run `arizuko generate tn` to apply
```

Two untruths in one line: the counts name neither row kind, and the tail tells
the operator the effect is still ahead of them. And one refusal sat on the far
side of the first write — `audit`, `dead_letters`, `sessions` and
`signing_keys` are registered resources with no owner DB, so
`resreg.ParseManifest` accepts them, the fragment files get written, and
`pkgSubsystemStores` then `die`s and leaves them in `services/` for the next
`generate` to pick up.

- **Severity:** medium (an operator reads a live scheduled task or egress rule
  as nothing at all)
- **Scope:** package install, the `add` and `install` verbs
- **Affected:** `cmd/arizuko/packages.go` `installPackage`, `parsePkgManifests`
- **Source:** measured 2026-09-19 at `b5243bdb` by running
  `go test ./cmd/arizuko/ -run TestPackageInstallsTaskAndNetworkRule -v`
- **Status:** fixed
- **Fix:** the report is DERIVED from the map `installPkgRows` returns, so
  every resource the apply wrote is named with its row count and marked
  `applied now`, and only the file count keeps the "run `arizuko generate`"
  tail; grants stay named on top (`L36`). And the no-owner refusal moved next
  to the `SkipApplyRebuild` one inside `parsePkgManifests`, naming the key and
  the file, so it lands before a byte is written; `pkgSubsystemServed` spells
  the served set once for the parser and the store opener. Tests:
  `TestPackageAddSummaryNamesEveryAppliedResource`,
  `TestInstallRefusesAResourceNoOwnerAppliesBeforeWriting`. SHA:

## L200 — proposal: a package cannot declare that it is a channel adapter or which env keys it reads; two name maps in `compose.go` decide both, so no outsider adapter passes the package contract (2026-09-05, proposal — needs sign-off)

`7/25`'s acceptance test: can someone outside the repo ship a package that
hooks in without patching arizuko. The wire contract passes it — `5/34` is
HTTP in any language, and `whapd`/`twitd` prove it from TypeScript — and the
deployment contract fails it at two names:

- `compose.adapterDaemons` (`compose/compose.go:250-254`) is the set
  `wireServiceKey` provisions a `service:<daemon>` principal for. An adapter
  outside it gets no `AUTHD_SERVICE_KEY`, and `POST /v1/channels/register`
  answers 401 under a Verifier (`routd/channels.go`, `channelAuth`).
- `compose.daemonKeys` (`compose/compose.go:98-238`) is the per-daemon env
  allowlist `writeEnvFiles` writes `env/<daemon>.env` from. A daemon outside
  it gets no file; a fragment that names one fails `compose up` (compose's
  `env_file` `required` defaults to true), and `envFileName`'s fallback hands
  it the shared `.env` with every instance secret.

The same class, smaller: `hasService(services, "ttsd")`
(`compose/compose.go:788`) sets `TTS_ENABLED`/`TTS_BASE_URL` on runed by name;
`/dash/ttsd/` is a core route (`compose/compose.go:369`) where every adapter's
dashboard route is a manifest row; `dashd/services.go:57-67` lists the hub
tiles by name. Adding `gitd` touched `cmd/arizuko/github.go`,
`compose/compose.go`, `resreg/resources/github_sources.go`,
`store/github_sources.go` and `store/migrations/0088` in one commit
(`c0594f32`), against `5/27`'s "adding an adapter touches no Go".

- **Severity:** medium (the package contract is unmet for the whole channel
  layer; every shipped adapter needs a `compose.go` edit to exist)
- **Scope:** compose generation; the package manifest
- **Affected:** every adapter package; any outsider adapter; `ttsd`
- **Source:** `compose/compose.go:98-238,250-254,369,788`;
  `dashd/services.go:57-67`; `routd/channels.go` `channelAuth`;
  `git show --stat c0594f32`; measured 2026-09-05 at `02d0fbc1`
- **Status:** proposal — needs sign-off (a manifest key is a new contract)
- **Fix:** the manifest declares what the maps decide — that the package is a
  channel adapter (a principal) and the env keys it reads — and `generate`
  reads the declaration instead of a map keyed by name; the hub lists what
  the route table serves. Spec: `7/25` §"What the contract needs". Whether
  the declaration is a resreg row (`5/8`'s shape) or a compose-side key in
  `<name>.yaml` is the decision to sign.

## ✅ FIXED 2026-09-05 L203 — the manifest half of every shipped package never reached a live instance (2026-09-05)

MEASURED across the fleet: krons, sloth and marinade hold zero
`services/*.yaml` sidecars and zero `owner = package:*` rows in
`proxyd_routes`. krons has `/dash/ttsd/` (a core route) and nothing for
teled or whapd, although both ship a `6/31` dashboard route. Two mechanical
causes, both in the delivery path, neither in the manifests:

1. `compose.RelinkCatalog` `continue`d on an already-linked base fragment
   before the sidecar was ever considered, and `relinkSidecarIfIdentical`
   returned on an absent instance copy. krons's four fragments were linked on
   2026-08-16, before any sidecar existed, so no later generate could deliver
   one.
2. The deploy unit runs `arizuko generate` in a container that mounts
   `/srv/data/arizuko_<inst>` and the docker socket only
   (`/etc/systemd/system/arizuko_krons.service` ExecStartPre). A sidecar
   linked at `HOST_APP_DIR/template/services/` is a dangling symlink there,
   and `ManifestRoutes` read a dangling link as "no manifest" — so even a
   linked sidecar delivered nothing on a deploy.

- **Severity:** high (every package's declared routes and, now, env needs
  were silently absent on every deployed instance)
- **Scope:** compose relink + generate-time manifest read
- **Affected:** all three instances; every catalog package with a `.yaml`
- **Source:** `sudo ls -la /srv/data/arizuko_*/services/`; `SELECT owner FROM
  proxyd_routes` on each `routd.db`; the unit's ExecStartPre mounts
- **Status:** fixed. `relinkSidecar` runs for a linked base and creates the
  absent sidecar; `manifestPath` reads a linked sidecar from
  `compose.CatalogDir` (the checkout when visible, else the copy baked into
  the image). Tests `TestRelinkCatalogLinksTheMissingSidecarOfALinkedBase`,
  `TestLinkedManifestIsReadFromTheCatalog`. On krons's next deploy this links
  `teled.yaml`, `whapd.yaml`, `ttsd.yaml` and writes `/dash/teled/` and
  `/dash/whapd/` (`auth: user`, gated by each adapter's token).
- **Fix:** `a8e1a34a`

## ✅ FIXED 2026-09-05 L199 — `packages add` applied nothing, so two install paths read one manifest differently (2026-09-05)

`install <source>` parsed every resource of `<name>.yaml` and applied it
through the apply engine with an `installed_packages` record; `add <name>` —
the verb every shipped package uses — linked two files and applied nothing,
and `generate` read exactly one key (`proxyd_routes`) from the sidecar. A
catalog package therefore could not declare a grant, a task, a network rule
or a web route, whatever `5/28` says it may, which is why all ten shipped
manifests carry only `proxyd_routes`. Two verbs, two readers, one format —
the optionality root `CLAUDE.md` forbids.

- **Severity:** medium
- **Scope:** `cmd/arizuko/packages.go`
- **Affected:** every catalog package
- **Source:** `cmdPackages` `add` vs `install`; `compose.ManifestRoutes`
- **Status:** fixed. `installPackage` is the one path: `add` and `install`
  differ only in where the files come from (catalog, linked; source, copied)
  and what the record says. A catalog record carries `Source: catalog`, so
  `upgrade`/`sync` leave it alone (its files follow the catalog) and `remove`
  withdraws the rows it names. `add` takes `--folder` for the same grant
  bound `install` does. Test `TestPackagesAddProvisionsEnvAndAppliesRows`.
  Still unread by any verb: `upgrade` re-applies no rows for any package
  (`5/28` records this gap); the new env section is provisioned by both.
- **Fix:** `0e9b5691`

## L200 — first-party wiring lives in five Go tables no manifest can express, so a third-party package cannot be an adapter (2026-09-05, open — proposal, needs sign-off)

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

Adding `gitd` touched five core files for exactly these. `baseDaemons`
(`packages.go`) is a sixth copy of "what is core". Nothing in the residue is
imperative, so no install script is needed: every item is a declaration the
manifest lacks a key for. A `postinst` would only re-express these as code
that runs with the operator's authority, which is the second path this file's
policy forbids.

- **Severity:** medium (first-party components are not packages yet; the
  contract holds for a sidecar like litellm and fails for an adapter)
- **Scope:** compose, dashd, packages
- **Affected:** any adapter shipped outside this repo; every first-party
  adapter's own wiring
- **Source:** `compose/compose.go` `adapterDaemons`, `daemonKeys`,
  `foreignImages`, `coreProxydRoutes`; `dashd/services.go`; `cmd/arizuko/packages.go`
  `baseDaemons`
- **Status:** proposed. Dissolve the tables into per-daemon `<daemon>.yaml`:
  the `env` block becomes the scoped env file's key list (a daemon is in
  `daemonKeys` XOR declares `env`, both refuses); one manifest flag names an
  adapter needing a service principal; each core daemon's routes move to its
  own manifest and the reserved set derives from the catalog. Then
  `TestFragmentEnvFilesAreGenerated` asserts against the catalog, not the
  map. Redesign — sign-off before code.
- **Fix:**

## L201 — nothing mechanical stops a sourced package from mounting a core owner's store or the docker socket (2026-09-05, open — proposal, needs sign-off)

Contract rule 1: a package brings its own dependencies and arizuko's store is
untouched. `TestNoFragmentMountsTheWholeDataDir` and
`TestStatefulFragmentsMountTheirStateDir` enforce it for the shipped catalog
only; `packages install <source>` accepts any `volumes:` line, so a package
may bind `${DATA_DIR}/store/routd`, `${DATA_DIR}/.env`, `${DATA_DIR}/ipc` or
`/var/run/docker.sock` and install cleanly. Routes have the analogue already
(`compose.ReservedRouteClaim`, refused at install and again at generate).

- **Severity:** medium (supply-chain: an installed package reads every
  owner DB and every secret)
- **Scope:** package install + generate
- **Affected:** any sourced package
- **Source:** `readFragmentAssets`/`installPackage` never read `volumes:`;
  `warnUnsatisfiedDeps` shows the regex-over-fragment precedent
- **Status:** proposed. Refuse at install and at generate a fragment whose
  bind source is an owner store dir, `.env`, `ipc/`, or the docker socket —
  the same two-reader shape as `ReservedRouteClaim`. A new refusal on an
  operator path; sign-off first.
- **Fix:**

## L202 — a linked package runs the host checkout's compose fragment while generate reads the image's manifest, so one package can come from two versions (2026-09-05, open — needs sign-off)

`packages add` links `services/<name>.yml` at `HOST_APP_DIR/template/services/`
(`compose.HostCatalogDir`); docker `include`s it on the host, so the live
container runs whatever the checkout holds — an uncommitted edit ships on the
next restart with no build and no tag, the class `M1` closed for `ant/`. The
`L198` fix reads a linked `.yaml` through `compose.CatalogDir`, which is the
baked `/opt/arizuko` copy where the deploy unit runs generate. The two halves
of one package therefore come from the checkout and the release respectively,
and only agree while the checkout is at the deployed tag.

- **Severity:** medium
- **Scope:** package delivery
- **Affected:** every catalog-linked package on every instance
- **Source:** `compose.HostCatalogDir`, `compose.CatalogDir`, the deploy
  unit's mounts
- **Status:** open. Two honest shapes: link at a release copy generate
  exports into the data dir (`<dataDir>/catalog/`, written from
  `/opt/arizuko` on every generate, so both halves are the image's), or mount
  the checkout into the generate container and accept that a checkout is the
  release. The first keeps "a release is an image"; sign-off decides.
- **Fix:**

## ✅ FIXED 2026-09-18 L204 — `chats.errored` was set and never cleared (2026-09-05)

The same finding as `L304`, recorded twice. A clean turn clears the flag now,
beside the circuit-breaker clear (`routd/dispatch.go`) — not the cursor advance,
which the failure path takes too.

- **Status:** fixed in v0.68.1, and this entry is `L304`'s duplicate.
## ✅ FIXED 2026-09-18 L205 — a route added through proxyd's dashboard was stamped with no owner (2026-09-05)

`store.PutProxydRoute` defaults an empty owner to `RouteOwnerArizuko` — "the
release derived this" — and `arizuko generate` deletes every arizuko-owned row
it did not derive, so an operator's own route vanished on the next deploy.

- **Status:** fixed in v0.68.2
- **Fix:** `handleDashRouteCreate` passes `Owner: store.RouteOwnerOperator`
  explicitly, which is what the entry named. The default is not relied on.
- **Test:** `proxyd.TestDashRouteIsStampedOperator` reads the row back.
## ✅ FIXED 2026-09-20 L206 — a group created from the dashboard grants its creator an exact-folder scope, so they cannot administer their own child folders (2026-09-05)

MEASURED. Two writers hand-roll the creator's admin grant and they disagree on
the scope:

- `onbod/main.go:936` grants `folder+"/**"`
- `dashd/groups_admin.go:150` grants `folder` (exact)

`auth.matchPattern` (`auth/acl.go:63`) does not let a bare `X` cover `X/sub` —
verified by probe: `matchPattern("eng","eng/sre")` is false, and
`matchPattern("eng/**","eng")` is true, so onbod's form covers both the folder
and its subtree while dashd's covers only the folder itself.

So the same group has two different authority outcomes depending on which
surface created it. A group made through web onboarding lets its creator
administer child folders; the same group made from `/dash/groups/` does not,
and the failure appears later, at the first `requireAdmin` on a child.

Both writers also bypass `store.AddACLRow`/`PutACLRow` (`store/acl.go:34`,
`:133`), so neither emits an `acl.add` audit row — a grant lands with no record
of who granted it.

- **Severity:** medium — a silent authority difference between two creation
  paths, discovered only when an admin action is refused
- **Scope:** dashd, onbod, store
- **Affected:** every group created through `/dash/groups/`
- **Source:** `dashd/groups_admin.go:150` vs `onbod/main.go:936`;
  `auth/acl.go:63`
- **Status:** open
- **Fix:** one `store` helper both callers use, taking a tx (both writes are
  inside one), granting `folder+"/**"`, and emitting the audit row. The scope
  question is settled by `5/33`: the creator holds the folder and its subtree.
  Amend the original; do not add a third writer.

- **Status:** fixed 2026-09-20.
- **Fix:** one renderer. `store.AdminGrantWrite(principal, folder, grantedBy)`
  makes the grant a folder's creator gets, and both creators call it —
  `dashd/dashstore/groups.go` and `onbod/main.go`. There is no second shape left
  to drift.

  The scope is `folder + "/**"`, which is onbod's shape and not dashd's.
  `matchSegments` (`auth/acl.go:76-105`) walks the glob segment by segment and
  ends on `len(in) == 0`, so a bare `eng` covers `eng` and REFUSES `eng/sre`;
  `**` with nothing after it answers true whatever input is left, so `eng/**`
  covers the folder AND its whole subtree. A dashboard-created group therefore
  gave its creator 403 on every child folder, at `dashd/authz.go:189`.

  The reason for the shape is now pinned where the matcher lives:
  `auth.TestSubtreeScopeCoversTheFolderAndItsChildren` asserts both directions,
  including that a bare folder does NOT cover a child — so the test fails if the
  premise ever stops being true rather than passing vacuously.

  PROVEN BY MUTATION: put `Scope: folder` back and both
  `TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder` and
  `TestAdminGrantWriteCoversTheFolderAndItsSubtree` fail.

  The existing dashd test pinned the bare scope, so it changes here too, and it
  gained the assertion that names the symptom: the creator can administer a
  CHILD of the folder they just made.
- **Left open:** onbod's `writeWorld` still emits no audit row for the grant
  (`onbod/main.go`), which is the entry's second half and a separate concern.

## ✅ FIXED 2026-09-18 L207 — authd stamped `issued_at` in local time (2026-09-05)

Fixed at the site the entry named: `authstore.now()` is
`time.Now().UTC().Format(time.RFC3339)`.

The same class survived one place more — `store/user_profiles.go` stamped
`created_at` with `time.Now().Format`, no `.UTC()`. It goes through `core.TS`
now, which is fixed-width and UTC, so two hosts agree (BUGS `L97`).

- **Status:** fixed in v0.68.2
## L208 — `store/` is a second, dead implementation of five tables that `routd` now owns: ~44 statements with no production caller, and the dead copy is the drifted one (2026-09-05, open — proposal, needs sign-off)

MEASURED with gopls reference resolution, not grep. Grep cannot decide this
question: `routd.DB` and `store.Store` share 20+ method names, so `s.X()` and
`d.X()` are indistinguishable textually. Every claim below was resolved by
symbol.

During the daemon split, `routd` grew its own SQL for tables `store` already
served, and nobody deleted store's. About **44 statements across ~990 lines,
17% of the package**, now have zero production callers. Each is shadowed by a
live twin in `routd/` or `proxyd/`.

The sharpest case, verified: `store.Store.FindMessages`
(`store/messages.go:727`) has **16 references and all 16 are `_test.go`**. It
is byte-identical FTS5 to `routd/reads.go:89` — the same
`snippet(messages_fts,0,'«','»','…',32)`, the same `bm25()`, the same
`m.rowid = f.rowid`. So the single hardest statement in the repo to port to
Postgres exists twice and one copy is dead.

Also confirmed with zero production callers: `MessagesAll`, `PatchTask` (3
UPDATE statements), `RemoveTask`, `PendingChatJIDs`, `WebRouteOwner`.

**Where two copies exist, the dead one is the drifted one.** Four measured:

- `store/web_routes.go:28 SetWebRoute` sets `created_at = excluded.created_at`
  on conflict — it overwrites the original. `routd`'s live `putWebRouteTx`
  deliberately keeps it.
- `store/groups.go:15 CountErroredChats` counts `SELECT COUNT(DISTINCT
  chat_jid) FROM messages WHERE errored=1`; the live `routd/db.go:467` counts
  `SELECT COUNT(*) FROM chats WHERE errored=1`. Different table, same name.
  (The live one is `L204`'s bug; the dead one is the correct derivation.)
- `store/proxyd_routes.go` writes DELETE-then-INSERT; `proxyd` writes
  INSERT + a separate UPDATE.
- **`cost_log` is two incompatible tables with one name.**
  `store/migrations/0049` declares `(ts, folder, user_sub, model, input_tok,
  cache_read, cache_write, output_tok, cents)`; `routd/migrations/0001`
  declares `(folder, turn_id, model, input_tokens, output_tokens, cost_cents,
  recorded_at)` with a PK. `store/cost_log.go`'s statements name store's
  columns, which exist only in the frozen pre-split `messages.db` that
  `store/owners.go:6` says no daemon opens. That SQL cannot run anywhere real.

One reduction inverts: the LIVE `routd.MarkMessagesObserved` is a loop of
single-row UPDATEs, while the DEAD `store/messages.go:372` already does the
correct single `... WHERE id IN (...)`. Port the good one before deleting it.

- **Severity:** medium — no live fault, but every one of these is ported,
  reviewed and maintained twice, and a reader cannot tell which copy runs
- **Scope:** store, and the routd/proxyd twins
- **Affected:** the whole Postgres move; ~35 statements and one of the two
  FTS5 queries disappear before it starts
- **Source:** `store/messages.go`, `tasks.go`, `web_routes.go`,
  `groups.go` (the `chats` accessors), `proxyd_routes.go`, `cost_log.go`
- **Status:** signed off; `web_routes.go`, `cost_log.go`, `tasks.go`,
  the `groups.go` `chats` block and `proxyd_routes.go` are done. What remains
  of `messages.go` is not a deletion — see below.
- **Fix:** delete in this order, smallest blast radius first: `web_routes.go`
  (5 statements), `cost_log.go` (2), the free `tasks.go` deletes (4), the
  `groups.go` `chats` block (9), `proxyd_routes.go` (1), then `messages.go`.

### `messages.go` is not a dead file — `webd` reads it (measured 2026-09-06)

The open question was whether the call sites keeping `store.PutMessage` alive
test `store` or use it as a fixture. gopls answers it: **87 references, and
exactly one is production** — `cmd/arizuko/send.go:176`. The other 86 are
`_test.go`.

But the file stays, because `webd` is a live `store` consumer. `webd/main.go:66`
opens routd.db with `store.OpenRoutd`, `webd/server.go:32` holds the
`*store.Store`, and webd's production code calls six of this file's readers:
`Topics`, `MessagesByTopic`, `MessagesByThread`, `MessagesSinceTopic`,
`TopicByMessageID`, `MessageTimestampByID`. `dashd`, `onbod`, `proxyd`, `slakd`,
`gitd` and the CLI hold one too. `store/` is the shared schema library those
daemons read through; only the methods `routd` re-implemented are dead.

Six of them were, and are gone: `MarkMessageStatus`, `MarkMessageDelivered`,
`PendingOutboundOlderThan` (with `scanMessageWithStatus`), `LatestSource`,
`IsBotMessageByID`, `MessagesAll`. Each had a live twin in `routd/db.go` or no
caller at all.

`FindMessages` is the one left, and its tests have moved (2026-09-06).
`routd/messages_fts_test.go` now covers the LIVE query — index-on-insert, the
FTS5 operators, diacritic folding, each filter arm, the `a_b`/`%`/`Coach`
subtree widenings (`L122`), the delete trigger, a malformed query surfacing as
an error, and ranking at 1000 rows. A same-arity mutation of the subtree
predicate turns the widening test red on all three cases.

Deleting store's copy of those tests lost nothing: `store.OpenRoutd` does NOT
run store's migrations — routd owns routd.db's schema — so `store/`'s own
`messages_fts` exists only under `store.OpenMem`, in no live database.

The method itself stays, and the blocker has a name:
`tests/integration_mcp_test.go` builds the whole `ipc.StoreFns` MCP harness
from one `*store.Store`, including `FindMessages`. Moving it to `routd.DB`
needs either a `routd.New(*sql.DB)` constructor — which routd deliberately does
not have, since `Open` refuses to manufacture a database — or the rest of that
harness (invites, watchers, tasks) on routd too. That is the test-scaffold
migration, and it is the last thing standing between the tree and one FTS5
query instead of two.

## ✅ FIXED 2026-09-20 L209 — a bare TLD is a valid egress allow entry, so one agent-written rule opens every host under it (2026-09-06)

MEASURED, on the live sloth instance. `main/trading` holds eight allow rows
that are bare TLDs: `com`, `org`, `net`, `io`, `ai`, `co`, `news`, `xyz`. All
eight carry `created_by = 'folder:main'` and timestamps inside six seconds on
2026-06-12 — the agent wrote them itself through `network_allow`.

`match.Host` matches a suffix: `strings.HasSuffix(host, "."+entry)`. So the
row `com` allows every `.com` host. A probe against the real package confirms
it — `Host(["org","com","io"], "api.telegram.org")` is `true`, and so is
`evil.org`. Eight rows put the commercial internet inside one folder's
allowlist while the operator's rule list still reads as an allowlist.

`match.ValidEntry` refuses a scheme, credentials, an IP, a port, a path, a
`*` and an inner wildcard — every shape that *cannot* match. It does not
refuse the one shape that matches far too much. `domainRegex` accepts a single
label, so `com` is a well-formed host name and passes.

The folder-scope check is not the containment here either: `network_allow` is
granted per folder, and a folder that may add `api.coingecko.com` may add
`com`. The grant says which folder writes rules, never which hosts a rule may
cover.

**FIXED 2026-09-06, and the first cause was wrong.** Refusing a dotless entry
in `ValidEntry` breaks legitimate hosts: `localhost` is a real host name with
no dot, and so is every container service name on the compose network. The
`egress/test` suite proved it immediately — two e2e cases allow `localhost`.

The fault is not the entry, it is the match. `Host` applied the subdomain arm
to every entry, so a single label was read as a parent domain. A single label
now matches EXACTLY and never as a suffix: `localhost` still means the host
`localhost`, and `com` means a host literally named `com`, which is harmless.
No list of TLDs, no public-suffix dependency, and no legal entry refused.

`egress/match/match_test.go` carried `{"single label matches its suffix",
["com"], "github.com", true}` — a test asserting the hole was correct
behaviour. It is replaced by the three cases that pin the rule, and dropping
the `strings.Contains(entry, ".")` guard turns two of them red.

The eight sloth rows stay in the table and stop granting the TLD at the next
spawn after the deploy. They are not deleted: each is data the folder wrote,
and `api.coingecko.com`-style rows beside them keep working, so nothing the
agent explicitly asked for is withdrawn.

- **Severity:** high — an agent-reachable path to near-total egress
- **Scope:** `egress/match`
- **Affected:** sloth `main/trading` — the only folder holding such rows,
  fleet-wide
- **Status:** fixed in `egress/match/match.go`; reaches an instance on the next
  image rebuild and restart


- **Status:** closed on re-check 2026-09-20 — the entry's own FIXED note holds.
- **Re-measured:** `egress/match/match.go:53-58` — an entry matches exactly, and
  the suffix arm runs only when `strings.Contains(entry, ".")`, so a bare TLD
  opens nothing under it. `egress/match` is the ONE matcher: `crackbox/pkg/match`
  is gone, and its five importers are `egress/proxy.go:21`, `store/network.go:36`,
  `sandbox/internal/netfilter.go:8`, `routd/network_rules_resource.go:29` and
  `sandbox/sandbox.go`.

## ✅ FIXED 2026-09-06 L210 — 26 prose spec IDs across the corpus name a spec that does not exist, and no check sees that form (2026-09-06)

**FIXED 2026-09-06 in `8ad728e8`, `ad3d8104`, `e0ca2f43`, `fde7b5fd`.** All 26
resolve. Eight were repointed at the spec that received the claim — `6/10` to
`6/24` (`6e3c1ce5` deletes `6/8` and `6/10` and names `6/24` the only crackbox
record), `7/7` to `6/30` (which carries onbod's admission queue at line 27),
`5/20` to `5/28`, `4/1` to `5/34`, and the two `7/Y` comments in
`cmd/arizuko/secret.go` and `store/secret_use_log.go` to `5/13`. The rest name
a mechanism deleted with its spec, so the sentence states the fact and carries
no pointer.

`tests/specrefs.TestEveryBacktickSpecIDResolves` is the guard: it resolves
`` `N/M` `` and `` `specs/N/M` `` against `specs/N/M-*.md`, reports 775 IDs,
and ignores a phase directory that does not exist because a phase is planned
before it is written. Proved by mutation — a `` `6/99` `` inserted into `6/20`
turns it red naming that file.

**`6/7` half FIXED 2026-09-06 in `8ad728e8`, and the count was low.** Every
live `6/7` citation now names what actually holds its claim: `6/1` states the
two contracts, `5/9` is the agent conformance basis, `5/16` gives each daemon
the database it owns and migrates, and `6/16` is the ship-in-parts map.
`BUGS.md` and `CHANGELOG.md` keep theirs — they record what was true when
written.

**The real scope is larger than this entry measured.** A guard that resolves
the prose form (`` `N/M` `` and `` `specs/N/M` ``) against `specs/<N>/<M>-*.md`
reports 803 IDs in the corpus and 26 dangling, in phases 1, 4, 5, 6, 7, 8, 9
and 14 — not 23 pointing at one retired number. Three were phase 6 and went
with the `6/7` sweep; 23 remain, and each needs its successor found rather
than guessed.

There is still no `specs/RETIRED.md`; commit `7d81cd78` dropped that table and
kept the link check, so a retired number resolves to nothing by design. The
guard's rule matches that decision: a phase directory that does not exist is
fine, because a phase is planned before it is written, but a phase that exists
and holds no file for the cited ID is a reader sent nowhere.

MEASURED. `git log --diff-filter=D -- 'specs/6/7-*'` names commit `fd1a4c3b`,
"docs(specs): retire 6/7; conformance is measured, and its dependents say so
directly". The file is gone. The pointers are not:

`grep -rn '`+"`6/7`"+`' --include=*.md` returns 23 hits across `BUGS.md`,
`ant/README.md`, `specs/index.md`, `specs/5/9-agent-capability-eval.md`,
`specs/6/1-adoption-interop.md`, `specs/6/16-daemon-standalone-matrix.md`,
`specs/6/34-resident-prompt.md` and `specs/6/38-storage-seam.md`. Three of
those are phase-6 specs marked `shipped`, and each cites `6/7` as the
authority for the environment or component contract — `specs/6/index.md` says
"`7` owns the contracts" of a spec that does not exist.

`tests/specrefs` does not catch it: `refs_test.go:46` matches markdown links
(`](path.md)`) and `depends:` entries, and every one of these 23 is a backtick
ID in prose. So the build is green while eight files cite a retired number.

That is the `L43` failure exactly — a pointer that reads as authoritative and
resolves to nothing — with the extra twist that the retirement was deliberate
and the sweep was not done.

There is no `specs/RETIRED.md` in the tree, so a reader who follows `6/7` has
nowhere to land and no record that the number is burnt.

- **Severity:** medium — no runtime effect; every reader of three shipped specs
  is sent to a spec that does not exist
- **Scope:** `specs/`, `BUGS.md`, `ant/README.md`, `tests/specrefs`
- **Fix:** (a) rewrite the 23 citations to name what actually holds each claim
  — the retirement commit says the dependents already state it directly, so the
  target text exists; (b) extend `tests/specrefs` to resolve backtick IDs
  (`` `N/M` ``) as well as links, which is what would have caught this the day
  `6/7` was retired; (c) decide whether a retired-number table comes back, and
  if so make (b) resolve against it

## ✅ FIXED 2026-09-06 L220 — the public operator docs cite specs that do not exist (2026-09-06)

**FIXED 2026-09-06 in `ba3c4104`, `0a3febf2`, `3266b8bf`, `b77f8753`, and MY
COUNT WAS LOW.** Sixteen was the `.md` path form only. The site cites specs in
three shapes — a `.md` path, a bare `specs/N/M`, and `spec N/M` in prose — plus
a glob (`specs/7/D-*`). Sweeping all four found **27 dead references across 31
files**, nine of them in the prose form and three of those on live pages. 46
distinct spec paths remain and every one resolves.

Most resolved to a renumber or a move with a commit to cite: `10/17`→`11/17`,
`7/D`→`8/D`, `5/5-uniform-mcp-rest`→`5/16`, both crackbox specs→`6/24`, and
`7/Y-secret-broker`→`5/13` following the same repoint `e0ca2f43` had already
made in the Go comments.

Six had no successor and the pointer was dropped rather than invented — the
important one being `7/H-per-daemon-secrets` at eleven sites, where
`specs/8/index.md` records that the env var it documents NEVER existed.

**Three false claims fell out of the repoint and were corrected, not recited:**
workflow enforcement is a shipped costed decline (`6/18`), not "on the
roadmap"; the standalone `ant` CLI is dropped, not "planned"; and secrets are
AES-256-GCM at rest under `SECRETS_KEY` (`store/secrets.go:77` refuses a
plaintext write) where a page said "Plaintext at rest".

`tests/webdocs` now resolves both forms over `.html` and `.js` — 239 paths and
84 prose IDs, with floors so the check cannot silently stop matching. It
deliberately differs from `tests/specrefs` on one point: specrefs tolerates a
phase directory that does not exist because a phase is planned before it is
written, and the site may not, because a published page hands its reader a 404.
Five mutations, one per citation form, each red.

MEASURED. `template/web/pub/arizuko/` names 56 distinct `specs/<phase>/<id>-<slug>.md`
paths. Sixteen resolve to no file:

`1/W-slink` · `4/26-prototypes` · `5/4-openapi-discoverable` ·
`5/5-uniform-mcp-rest` · `5/X-session-workflows` · `6/F-group-observe` ·
`7/D-slack-agent-pane` · `7/H-per-daemon-secrets` · `7/Y-secret-broker` ·
`10/8-self-eval-skill` · `10/17-emaid-auth` · `10/19-cost-caps` ·
`11/2-declarative-composition` · `11/12-crackbox-sandboxing` ·
`11/15-crackbox-dns-filter` · `12/b-ant-standalone`.

`reference/env.html` alone carries ten references to `specs/7/H-per-daemon-secrets.md`.
Several are written as live github.com links, so a reader on
`krons.fiu.wtf/pub/arizuko/` follows them to a 404.

`tests/specrefs` cannot see these: it checks markdown under `specs/`, and these
are HTML in another tree with its own deploy procedure
(`template/web/CLAUDE.md`).

- **Severity:** medium — no runtime effect, but this is the operator-facing
  public surface, and a 404 from a published page is worse than a dead pointer
  inside the corpus
- **Scope:** `template/web/pub/arizuko/`, `tests/webdocs`
- **Fix:** repoint each to the spec that received its claim the way `L210` did,
  and extend the check that already guards this tree (`tests/webdocs`) to
  resolve `specs/…md` paths, so the deploy cannot ship a dead one again

## ✅ FIXED 2026-09-06 L221 — `specs/index.md` lists git-as-truth as live phase-9 work; `specs/9/index.md` says it is rejected (2026-09-06)

**FIXED 2026-09-06.** Both sites now say what `specs/9/index.md` decided: the
one remaining phase-9 action is the data model, and git as truth is declined
because `5/8` makes the SQLite database authoritative and YAML a transport
dump — committing an `export` to git is fine, a continuously-synced working
tree is not the model. The narrative paragraph also stopped recounting which
action moved where and states where each one lives now.

MEASURED. `specs/index.md:44` reads "the remaining phase-9 actions are the data
model and git-as-truth", and its phase table at `:63` describes `9/` as
"platform program — data model, git-as-truth".

`specs/9/index.md:36` says the opposite of the second half: `3-git-as-truth.md`
is "the rejected approach above", built on "the gateway is the only git writer"
— and `gated`/gateway was deleted at v0.50.0. No git-writing code was ever
written. `:37` records that the ingestion doc premised on it self-disclaimed.

So the master index sends a reader to look for work that the phase's own index
settled as declined. No pointer dangles, so `L210`'s guard cannot see it.

- **Severity:** low — a reader of the top-level index believes an approach is
  open that the phase rejected
- **Scope:** `specs/index.md`
- **Fix:** state the one remaining phase-9 action, and say git-as-truth is
  declined with the reason `9/` already gives

## ✅ FIXED 2026-09-06 L211 — `6/23` promised a per-spawn virtual key and gateway-authoritative cost; neither shipped (2026-09-06)

VERIFIED independently after an audit raised it.

**Fault 1, the per-spawn key, did not ship.** The spec's mechanism is a virtual
key minted per spawn with a budget and a TTL, so revocation is scoped to one
turn. `template/services/litellm.yaml:9` declares `MODEL_GATEWAY_KEY:
generate: true` — minted ONCE per instance, into `.env` — and
`container/runner.go:691` passes that same static `cfg.ModelGatewayKey` to
every spawn forever. No mint, no TTL, no per-key budget exists anywhere. The
container no longer holds the operator's Anthropic credential, which is real
and is `spawnSecrets` (`container/runner.go:765`) working as designed. What it
holds instead is one shared non-expiring master key to the gateway.

**Fault 2, gateway-authoritative cost, did not ship.** `routd/turns.go:224`
still calls `PutCost(...)` in a loop over `req.Models` — the agent's own
`submit_turn` field. Nothing reads LiteLLM's `/spend/logs`. Cost attribution
is exactly as self-reported as before.

**The spec still argues for the wrong vendor.** §"Adopt Bifrost"
(`specs/6/23-llm-gateway.md:78`) calls LiteLLM "Python + Postgres — heavy" and
disqualifying. `grep -ri bifrost` over the code and compose returns nothing;
`template/services/litellm.yml` is what ships. The reversal was correct and
measured — LiteLLM injects the OAuth credential and Bifrost cannot — but the
spec never recorded it, so the document argues against what runs.

- **Severity:** medium — one shared gateway key is better than one shared
  Anthropic key, but it is not what the spec promises, and spend is still
  attacker-declared
- **Scope:** `container/runner.go`, `routd/turns.go`, `specs/6/23`
- **Status:** fixed — both halves built and the spec rewritten. The blocker
  both shared was litellm's Postgres, which `6/37` and `6/38` had already
  decided; the package now ships it as `litellm_db`. routd mints the per-turn
  key (`routd/gateway.go`), bounds it by the folder's remaining daily budget
  and the run ceiling, and deletes it; `putTurnCost` (`routd/turns.go`) is the
  one cost_log writer and takes the gateway's measurement, with the agent's
  `submit_turn` figure demoted to a log line. `6/23` states the LiteLLM
  decision, present tense, with the measured reason Bifrost is rejected.
- **Fix:** 2f6441ed, c8854f7d, a67a94e1

## ✅ FIXED 2026-09-06 L212 — `mcpfw` is complete, tested, and wired to nothing (2026-09-06)

**FIXED 2026-09-06 by restatement, and the entry's CONCLUSION was wrong.** The
measurement stands — five files, all under `mcpfw/`, no `check`, no `/v1/*`, no
`pkg/client`, no unix or stdio transport, no fan-in. What is wrong is "the
unmet need is fully open". It is closed, by the gate arizuko already has.

An agent never dials a third-party MCP server. `injectMcpEnv`
(`ant/src/mcp-servers.ts:37`) hands the turn exactly one entry, the arizuko
socket, and each harness pins that shut: claude with `strictMcpConfig: true`,
codex with `--ignore-user-config`, pi with `tools: 'none'`. A connector's tools
are LOCAL tools — routd registers each as `<connector>_<tool>` through
`granted()` and dials the upstream itself INSIDE the handler
(`ipc/connector.go:138`), after the check.

That check is `auth.Authorize` on the `acl` rows, at announce
(`ipc/ipc.go:1107`) and again at call time (`:989` → `authorizeCall`). It is
deny-wins and no-match-denies, which IS `6/12`'s model, and it carries `5/33`'s
two axes, which a `Rule` — `{tool, action}` over a name glob, no target —
cannot express. So wiring `mcpfw` in would be a second gate on one
`tools/call`, which this repo forbids, and making it the only gate would trade
containment for name matching.

`6/12` now describes the binary that ships and says arizuko is not its
consumer — the "ships alone" category `6/16` already files it under, and what
`7/22` and `7/25` already said. `ipc/connector_gate_test.go` holds the gate:
proved by mutation, neutering the call-time check lets a refused call reach
upstream and the request counter rises from 4 to 8.

MEASURED. `grep -rl mcpfw --include=*.go` returns five files and every one is
under `mcpfw/`. No daemon imports it, none pushes a ruleset, and no agent's
third-party MCP traffic passes through it. The unmet need `6/12` was written to
close — third-party MCP tool calls reaching an agent ungated — is fully open.

The engine itself is real: deny-wins and default-deny in
`mcpfw/pkg/rule/rule.go`, and `mcpfw/pkg/firewall/firewall.go` refuses before
touching upstream, both tested. What is missing is everything that would let
something use it: `mcpfw/cmd/mcpfw/main.go` registers `serve` only — no
`check` subcommand — and the mux serves `POST /` alone, so the spec's
`PUT/GET /v1/rules`, `GET /v1/log`, `GET /health`, `pkg/client`, the unix and
stdio transports and the multi-upstream fan-in do not exist.

- **Severity:** medium — no false guarantee in code, but `6/12` reads as a
  shipped control and gates zero traffic
- **Scope:** `mcpfw/`, `specs/6/12`
- **Fix:** either wire routd's connector path through it, or mark `6/12` as a
  library that ships alone with no arizuko consumer yet — `6/16`'s own category

## ✅ FIXED 2026-09-06 L213 — two connectors may share a name and both register, with no error and no log (2026-09-06)

**FIXED 2026-09-06 in `4acc973f`.** `LoadExtProviders` now walks ordered
sources and indexes by `LocalName` — the agent's wire identity, so it also
catches the prefix collision (`foo_bar` + `baz` against `foo` + `bar_baz`)
that a connector-level check misses. A later source replaces an earlier entry
and logs the shadow at WARN with both sources named; the same name twice
inside one source is an error, since no ordering justifies it. The embedded
`extproviders/` set counts as ONE source: it ships as a unit and its order is
an accident of filenames.

**The entry's scope was one file short.** `routd/cmd/routd/main.go:255`
downgraded a load failure to `slog.Warn` and served on, and the loader returns
`nil` on error — so one rejected block silently removed EVERY ext tool. It now
fails the boot, the same terms `LoadConnectors` already applies to the same
file twenty lines above.

MEASURED at `routd/ext.go:114`. `LoadExtProviders` reads the embedded
`extproviders/` set, appends the operator's `connectors.toml`, and emits one
`ipc.ExtTool` per tool with `LocalName = p.Name + "_" + t.Name`. There is no
name check between the two sources and none within either.

So an operator who names a connector after a built-in gets two tools with an
identical `LocalName` handed to the agent. Which one a call reaches is
whichever the consumer's map or slice order lands on.

`6/19` names the opposite behaviour as load-bearing and credits it to Centaur:
"sources are ordered … later shadows earlier BY NAME, every shadow logged
loud." Neither half exists — no shadowing, no log line. `routd/ext_test.go`
has no duplicate-name case.

- **Severity:** medium — an operator can silently displace or duplicate a
  built-in tool, and the failure is undefined rather than loud
- **Scope:** `routd/ext.go`, `specs/6/19`
- **Fix:** index by `LocalName` while loading; a later source replaces an
  earlier one and logs the shadow at WARN; a duplicate within one source is an
  error, since no order can justify it

## ✅ FIXED 2026-09-06 L214 — `6/26` reads as a shipped approval gate while its primary verdict is a known no-op (2026-09-06, docs)

**FIXED 2026-09-06.** `L97` is closed, so the bound is gone and the spec no
longer carries it. The one-shot half of every verdict re-issues, and
`routd/pending_actions_reissue_test.go` drives that path end to end.

VERIFIED. `specs/6/26-personal-mode.md` carries `status: shipped` and its
Decisions 4 and 5 say "Acceptance: Shipped 2026-08-24/29/30". Its headline
scenario is answering each risky call yes / no / always.

`L97` says the plain yes is a no-op: the re-issue trigger's timestamp sorts
behind the dispatch cursor that skips it, so the held call is never re-issued.
That entry is still `proposal — needs sign-off`, and the cause is unchanged
today — `routd/db.go:110` is `func nowTS() string { return
time.Now().UTC().Format(time.RFC3339Nano) }`, and RFC3339Nano is not
fixed-width, so a whole-second stamp sorts ahead of a fractional one.

`always` is unaffected: it skips the approval path rather than re-issuing
through it. So the one verdict a personal-mode user reaches for most is the
one that does nothing.

The spec does name the bound, but as a closing footnote rather than in the
status line or either decision body. A reader who stops at "Acceptance:
Shipped" believes the loop works end to end.

- **Severity:** medium (docs) — `L97` carries the runtime severity
- **Scope:** `specs/6/26`
- **Fix:** `status: defected` with `defects: [L97]`, and the bound stated in
  Decision 4 rather than at the end — the convention `defected_spec_status`
  already exists for exactly this

## ✅ FIXED 2026-09-06 L215 — two `6/25` citations name the wrong file and a stale line count (2026-09-06, docs)

**FIXED 2026-09-06 in `64dcc899`, and the spec had a third staleness.** The
citation is `ant/src/claude.ts` (`tests/feat_continuity_test.go:42` reads it),
and qm's `mock-harness.ts` is 840 lines at `main`, dated at the read so the
next drift is visible.

The read for this fix found what the entry missed: decisions 1 and 2 are
adopted, and section 1 still described the gap as current — `egress/proxy.go`
matching the name then dialling `r.Host`, at two line numbers that no longer
resolve. `egress/address.go:67` `resolveDest` resolves once and refuses a host
when ANY answer is blocked. Both sections now state the shipped code, and the
spent `## Order` ranking is a two-line `## State` (`bfb3b8fa`).

MEASURED. `specs/6/25-qm-adoptions.md` says `UUID_RE` is extracted from
`ant/src/index.ts`; it lives in `ant/src/claude.ts`, which is what the test
itself reads — only the prose is wrong. The same spec cites qm's
`mock-harness.ts` at 802 lines; qm's `main` is 840 today, drift since the
2026-08-20 read.

Every mechanism the spec claims is real: `egress/address.go` resolve-then-dial
with the IMDSv2 IPv6 case, `match.Allowed` checking deny before allow,
`substrate.Effective` with `runed/dash.go`'s `substrateLine`, and all three
contract tests.

- **Severity:** low — no deploy impact; a reader following the citation lands
  on the wrong file
- **Scope:** `specs/6/25`
- **Fix:** correct the filename; date the qm line count or drop it

## ✅ FIXED 2026-09-06 L216 — `6/33` documents two inbound verbs no adapter emits, and its enforcement test excludes the one adapter that could break it (2026-09-06)

**Test half FIXED 2026-09-06.** `tests/socialboundary` globs each adapter's
`src/*.ts` beside its `*.go`, and `twitd` joins the scanned set, so the
boundary the spec cites now covers all four. Proved by mutation: a `trending`
comment appended to `twitd/src/poll.ts` turns the scan red and naming twitd,
which also proves the glob reaches the file rather than reading an empty set.

**Doc half FIXED 2026-09-06 in `64dcc899`.** Re-measured before editing: the
four adapters write `message` (`bskyd/client.go:289`, `reditd/client.go:353`,
`twitd/src/poll.ts:56`), `reply`, `comment` (`linkd/client.go:468`) and `post`
(`reditd/client.go:365`), and `like` appears only in the three outbound
capability maps and reditd's vote call. `6/33` gains a §"Inbound verbs" table
naming those four with their sites, states that `mention` is routd's central
promotion (`routd/server.go:577-580`, and the verdict rewrite at `:587-589`
which the entry missed), and tells the operator to key a route on
`message`/`reply`/`comment`/`post` because `mention` and `like` match nothing.
The example payload carries `Verb: "message"`, and the count reads three of
four.

**The test half is untouched and stays open**: `tests/socialboundary` still
scans Go only, so twitd is outside the boundary `6/33` claims. That is a `.go`
change.

VERIFIED. `specs/6/33-social-adapter-model.md:44` says social adapters deliver
"reactions/likes on the bot's posts (as `verb=like`)", and its example payload
at :86 carries `Verb: "mention"`.

Neither reaches routd. `grep '"like"'` over the four social adapters returns
only outbound capability maps (`bskyd/main.go:16`, `linkd/main.go:19`,
`reditd/main.go:19`) and reditd's own vote call (`reditd/client.go:531`) —
never an inbound verb. `bskyd/client.go:228` asks the notifications API for
`reasons: "mention,reply"`, so a like is not even fetched. The literal string
`"mention"` is never written as an inbound verb by any of the four; their
inbound verbs are `message`, `reply`, `comment` and `post`.

routd's only `verb=mention` write is the central promotion at
`routd/server.go:576`, which fires on a reply inside a thread the bot is
already in. A first-contact @mention — the spec's own example — is not
promoted, so an operator route keyed on `verb=mention` silently never matches
it.

The boundary guard has a hole of its own. `tests/socialboundary/boundary_test.go:27`
is `var socialAdapters = []string{"bskyd", "reditd", "linkd"}` and its comment
excludes twitd because twitd ships TypeScript, not Go. The spec cites this test
as proof "a SECOND adapter cannot cross it quietly". twitd can: nothing scans
`twitd/src/` for the observer markers.

Cosmetic on top: :115 says "four of five social adapters are thin"; the tree
holds four.

- **Severity:** medium — an operator writes a route that never matches and
  gets no error, and one adapter is outside the boundary the spec claims
- **Scope:** `specs/6/33`, `tests/socialboundary`
- **Fix:** (a) state the verbs the adapters actually emit, or emit the ones
  documented; (b) extend the boundary scan to `.ts` so twitd is covered — the
  markers are plain strings and the language does not matter

## ✅ FIXED 2026-09-06 L217 — `6/34` marks three root `CLAUDE.md` sections "move" and all three are still resident, into skills that do not exist (2026-09-06)

**FIXED 2026-09-06 in `b2c79dd5`, resolved as keep, and the entry's premise
was wrong.** The two destinations are addresses nothing reads. A project skill
whose name collides with a global one loses, and `~/.claude/skills/refine/`
and `~/.claude/skills/release/` both exist — so `.claude/skills/refine/` and
`.claude/skills/release/` would be shadowed and reach no reader.

Both global skills already read this file: the release skill's step 0 reads
the project `CLAUDE.md` and its step at `:77` reads the `## Announcing`
section by name; the refine skill reads the live project WISDOM. So the root
file is the DECLARATION and the global skill is the READER, which is the rule
`6/34` exists to protect. Moving the shape to `CHANGELOG.md` would have
created the second format that rule forbids.

The three rows now say keep under a fourth residency rule ("A workflow skill
reads it from here"), `CHANGELOG.md`'s head comment is a pointer instead of a
second copy of four rules, and
`tests/resident/resident_test.go` pins the three sections so the row cannot
drift again.

VERIFIED. The spec's per-section table is its plan. Three rows say move:
"Refine scope" → `.claude/skills/refine/SKILL.md`, "Tagging a new version" →
`.claude/skills/release/`, "Announcing" → `CHANGELOG.md`'s head.

All three are still in the resident file — `CLAUDE.md:167`, `:245`, `:256` —
and `ls .claude/skills/` returns `create-eval` and `eval` only. Neither
destination exists.

So the file the spec audited carries about 15% of its own text past the verdict
it recorded, and the "Announcing" block is now declared in two places, which is
the "one declaration, many readers" violation `6/34` exists to remove.

The `ant/CLAUDE.md` half of the same spec is executed correctly — 388 lines to
196, and each move or trim verdict spot-checked lands as written.

- **Severity:** low — no runtime effect; the resident budget the spec sets is
  overspent and the plan reads as done
- **Scope:** `CLAUDE.md`, `specs/6/34`
- **Fix:** create the two skills and cut the sections, or change the three rows
  to "keep" with the reason. Either is fine; the table claiming one thing while
  the file does another is not.

## ✅ FIXED 2026-09-06 L222 — a raw tool schema drops the WHOLE `tools/list`, so the turn loses every tool (2026-09-06)

**FIXED 2026-09-06 in `7419a01d`, and this entry UNDERSTATED it: the defect was
live on the fleet, not latent.** I wrote that no `acl` row grants an ext tool so
nothing arms it. The announce filter is not the only door. `turnAuthorize`
(`routd/agent_gate.go:38`) returns `func(...) bool { return true }` for an
elevated turn, because the operator holds `*` WITH GRANT OPTION — so `/root`
passes every check, the four embedded providers under `routd/extproviders/`
register, and the marshal refuses. **Every `/root` turn on every instance
received no tools at all** and timed out, which reads as an agent ignoring the
operator. Measured on a live socket: with the defect, `read: i/o timeout` and
the log line naming `cloudflare_dns_create`; with the fix, 53 tools listed.

The arming condition in this entry was also wrong. The ext announce filter reads
`tool.Scope` (`ext:cloudflare:dns:read`), not `mcp:<name>`, and per
`auth/authorize.go:390` only a `*` row covers that shape — `admin` and `mcp:*`
do not. So the doors are `*` and elevation, never an ordinary grant.

The fix is one clearing at `ipc/ipc.go`'s single registration funnel, following
what `mcp-go`'s own `WithInputSchema` does: clear `InputSchema.Type` when a raw
schema is present. `NewToolWithRawSchema` was rejected on the library's
documented warning that such a tool is incompatible with the `ToolOption`s both
call sites pass.

A per-tool marshal guard was considered and refused on reachability: the input
conflict is now impossible at the one funnel, no `RawOutputSchema` exists
anywhere in the tree, and both raw-schema producers are `json.Marshal` of a Go
value, so the bytes are valid by construction. The guard would trade a loud
total failure for a quiet dropped tool, which is worse under fail-loud.

Two tests hold it, each through a real producer rather than a hand-built
fixture: a connector tool over `fakeMCP`, and the real embedded cloudflare
provider through `LoadExtProviders`. One mutation proves both — invert the
guard to `if tool.RawInputSchema == nil` and each goes red naming its tool.

VERIFIED against the vendored library and the call path, not a report.

`mcp.NewTool` sets `InputSchema.Type = "object"`
(`mcp-go@v0.45.0/mcp/tools.go:788`). `WithRawInputSchema` sets `RawInputSchema`
and does NOT clear that field (`:897`). `Tool.MarshalJSON` refuses when both
are set (`:619`): "has both InputSchema and RawInputSchema set".

`ipc/ipc.go` builds every connector tool (`:1110`) and every REST ext tool
(`:1137`) as `mcp.NewTool(name, WithRawInputSchema(...))` through `granted()`
→ `srv.AddTool(mcp.NewTool(name, all...))` at `:964`. So each of those tools
carries both.

**The blast radius is the whole response, not the tool.** `tools/list`
marshals as ONE document, so a single refusing tool makes `writeJSON` fail and
routd drops the reply. The agent receives no tool list at all and times out.

**It does not fire on the fleet today, and the reason is an accident.** The
four embedded providers (`routd/extproviders/`: cloudflare, gandi, namecheap,
porkbun) all build a non-empty schema from their path placeholders
(`routd/ext.go:197`), so they all carry the conflict — but the announce filter
at `ipc/ipc.go:1107` skips a tool the folder has no grant for, and no live
`acl` row on krons, marinade or sloth grants `mcp:*` or a `mcp:<connector>_*`
action. Every ext tool is filtered out before it can be marshalled.

So the trap is armed and waiting for the first operator who grants an ext tool
— the very act of enabling a connector is what breaks that folder's entire
tool surface.

- **Severity:** high — latent, and it fires on the first grant of the feature
  it guards, taking down every unrelated tool with it
- **Scope:** `ipc/ipc.go`
- **Fix:** clear `InputSchema.Type` beside `WithRawInputSchema`, the way
  `mcp-go`'s own `WithInputSchema[T]` does, or build those tools with
  `mcp.NewToolWithRawSchema`. Then a test that marshals a `tools/list`
  containing one raw-schema tool, which is the assertion nothing makes today.

## ✅ FIXED 2026-09-06 L223 — a 10.7 MB compiled binary is tracked in git (2026-09-06)

**FIXED 2026-09-06 in `db098c3d`.** `git rm --cached mcpfw/mcpfw`, and
`.gitignore` now covers both build outputs a `make -C <dir> build` leaves —
`mcpfw/mcpfw` and `gitd/gitd`, the second of which was untracked and one
`git add` away from the same fate. The objects already in history stay; that is
the cost of noticing late.

MEASURED. `git ls-files -s mcpfw/mcpfw` returns a tracked mode-100755 blob and
the file is 10,752,842 bytes of ELF. It is `make -C mcpfw build`'s output, and
`.gitignore` does not cover it. A scan of every tracked executable finds it is
the only one, so the rule the repo follows everywhere else was missed once.

Every rebuild rewrites it, so each one adds another ~10 MB object to the
history, which no `git rm` recovers.

- **Severity:** low — no runtime effect; it costs clone time and repository
  size, and it grows on every build
- **Scope:** `mcpfw/`, `.gitignore`
- **Fix:** `git rm --cached mcpfw/mcpfw` and add the build output to
  `.gitignore`. The objects already in history stay; that is the cost of
  noticing late.

## ✅ FIXED 2026-09-06 L224 — the Caddyfile renderer copied three of the four forward-auth headers, so every fronting-proxy caller is anonymous (2026-09-06)

FOUND and fixed while auditing `6/22`. `/auth/forward` answers four headers;
`proxycfg` rendered `copy_headers` with three, dropping `X-User-Name`.
Rendered against krons' live table: 26 rows, 26 routes, 21 of them carrying
the auth hop and every one short that header.

It fails silently. Every request still returns 200, and the backends that read
the header just see nothing: `webd/api.go:98` records the sender name on a
posted message, `webd/pair.go:61`, and `dashd/main.go:657` labels the nav. So a
deployment fronted by Caddy would attribute every message to a nameless user
and nothing would report an error.

Two declarations of one set, which is the failure root `CLAUDE.md` names. Fixed
by `auth.TransitHeaders` — one declaration, both the endpoint and the renderer
read it. Proved by mutation twice: restoring the shipped renderer turns the new
`proxycfg` test red naming the missing header, and slicing the set to two in
`proxyd/forward_auth.go` turns the endpoint test red.

- **Severity when live:** would have been high — silent, total loss of caller
  attribution behind a fronting proxy
- **Reached no instance:** proxyd is the running proxy; the renderer prints and
  installs nothing

## ✅ FIXED 2026-09-06 L225 — `proxyd/README.md` says proxyd accepts HS256 bearers; `tryAuth` is ES256-only (2026-09-06)

**FIXED 2026-09-06 in `db098c3d`, and the README was not the only place.**
Eight sites still described the removed HS256 path, and two of them stated
something FALSE about the code that runs: `proxyd/main.go:114` and `:940`
both said proxyd is "HS256-only, exactly as today" when `AUTHD_URL` is unset.
It is not — `tryAuth` returns nil on a nil key set, so it authenticates nobody
and every route needing an identity falls through to login.

`proxyd/README.md` also documented `AUTH_SECRET` as the "HS256 JWT verify
secret". It verifies no bearer; it keys the OAuth CSRF state, which is why
`requireAuth` denies when it is unset. Both README lines and the six comments
now state what runs, with no account of what was removed.

The regression guard in `proxyd/proxy_internals_test.go` stays: it asserts an
HS256 bearer signed with `AUTH_SECRET` is refused even when the ES256 key set
is absent, which is the `L6` forgery branch staying shut.

MEASURED during the `6/22` audit. `proxyd/README.md:18` states proxyd
authenticates `Bearer <jwt>` "(HS256 or ES256)". The HS256 branch was removed
under `L6`; `tryAuth` verifies ES256 against the JWKS key set and nothing else.

- **Severity:** low — a reader configuring a client tries an algorithm proxyd
  refuses, and the 401 does not say why
- **Scope:** `proxyd/README.md`
- **Fix:** say ES256, and name the key set it verifies against

## ✅ FIXED 2026-09-06 L226 — `db_utils`'s concurrency test fails under `-race`, and `make test` cannot see it (2026-09-06)

**FIXED 2026-09-06 in `43a7afcc`.** The fixture's hold is bounded from above by
`busy_timeout(5000)`: a losing opener waits that long and then reports
`SQLITE_BUSY`, so a hold near the timeout makes a correctness test into a
timing test. The recursive CTE drops from 400,000 rows to 40,000, about 106 ms,
which leaves headroom for the race detector's slowdown. Measured here:
`-race` fails in 8.3 s before, passes in 3.6 s after; 0.21 s without `-race`.

**One thing this does NOT establish, and it is worth its own entry.** The test
asserts the outcome — three rows, three recorded versions — and nothing asserts
that the race actually happened. The fixture's own comment says the point of
the hold is that every other opener reads `MAX(version)=0` before the winner
commits, and that "without the hold the goroutines serialise and the recheck is
never exercised". So a hold that becomes too short passes silently, which is
the failure the 10x reduction makes newly plausible. `proxyd`'s snapshot race
test already carries the guard this one lacks: it fails the fixture outright
when both outcomes did not occur.

REPRODUCED on the tree WITHOUT the change that surfaced it, so it is
pre-existing, not introduced. `go test ./db_utils/ -race -count=1 -run
TestMigrateAppliesEachFileOnceUnderConcurrentOpeners` fails: five of six
openers report `create migrations table: database is locked (5) (SQLITE_BUSY)`.

The fixture `db_utils/testdata/slow/0001-slow.sql` burns time with a 400k-row
recursive CTE. Race instrumentation slows modernc.org/sqlite enough that the
burn exceeds the `busy_timeout(5000)` every opener waits on, so the losers
report a lock rather than queueing.

`make test` runs no `-race`, so the suite is green and the failure is
invisible. That is the part worth fixing: a concurrency test whose only
failure mode hides from the command everyone runs.

- **Severity:** low — the test is wrong, not the code; but a race detector
  nobody runs on a concurrency test is a gap in the guarantee
- **Scope:** `db_utils/testdata/slow/`, the `test` target
- **Fix:** bound the fixture's burn by TIME rather than by iterations, so
  instrumentation cannot push it past the timeout. Then decide whether
  `make test-all` should carry `-race` for this package, since a concurrency
  guarantee proved without it is proved weakly.

## ✅ FIXED 2026-09-06 L229 — the migration concurrency test cannot tell a passing race from one that never happened (2026-09-06)

**FIXED 2026-09-06 in `b65a22ea`, and MY OWN CONFIRMATION ABOVE WAS WRONG.** I
wrote that the race never happens. It happens in roughly 40% of runs — measured
at 17 vacuous in 30, later 10 in 30, once `migrate` reported outcomes directly.

The error was a weak mutation. `cur >= ver` → `cur > ver` only turns a yield
into an error on the exact `cur == ver` tie; the common yield is `cur > ver`
strictly, because the winner applied 0001, 0002 and 0003 while the loser waited
on 0001, and the mutated code still skips those. So the mutation under-reports,
and I read its silence as absence. Run 30 times it went red twice.

The stated cause holds and is one step earlier than "the outer skip":
`createVersionTable` takes the SAME critical section the migrations do, so an
opener queued behind a rival's `CREATE TABLE` only reads the version after that
rival has also committed a migration, then skips every file. Creating the table
before the start line took the vacuous rate from 17/30 to 0/40.

`Migrate` keeps its signature. The body is unexported `migrate(...)
(migrateReport, error)` and `Migrate` delegates and discards — one
implementation, no second path. The report has `applied` and `yielded`, and a
file already recorded when the call read the version is in NEITHER, which is
the distinction that makes the guard faithful. The signature stayed because
there is no `arizuko migrate` to print a report: `cmd/arizuko/main.go` registers
`migrate-split` only, so exporting the type would add API nothing calls.

Stability after the fix: 40/40 green, 12/12 under `-race`, 15/15 at load 10.9.

MEASURED while closing `L226`. `TestMigrateAppliesEachFileOnceUnderConcurrentOpeners`
starts six openers on one barrier and then asserts only the end state: three
rows in `widgets`, three recorded versions. Nothing asserts that more than one
opener ever read `MAX(version)=0`, which is the whole point — the fixture's
comment says so itself.

The recheck-and-rollback path it exists to cover is therefore exercised only as
long as the fixture's hold outlasts the goroutines' start-up. `L226` cut that
hold by 10x for a good reason, so the margin is now unmeasured rather than
generous.

`proxyd/dispatch_snapshot_test.go` already solves this for the same shape of
test: it counts both outcomes and calls `t.Fatalf("fixture is vacuous: …")`
when either is zero, so a race that stops racing fails loudly instead of
passing.

- **Severity:** low — the code is right; the test's guarantee is weaker than it
  reads, and it degrades silently
- **Scope:** `db_utils/migrate_engine_test.go`
- **Fix:** count the openers that observed version 0 and fail the fixture when
  fewer than two did, the way the proxyd test counts its two outcomes

**CONFIRMED 2026-09-06, and it is worse than "the margin is unmeasured": the
race has never happened in this test, at either fixture size.** Measured by
mutation. The recheck in `db_utils/db_utils.go` is `if cur >= ver { skip }`;
changing it to `if cur > ver` makes a waiting opener fall through to the
`ver != cur+1` gap check and return an error, so the test MUST go red if any
opener ever waited on the lock. It stays green — at the 40,000-row fixture
(0.056s) and at the original 400,000 (0.892s) alike.

The cause is the outer skip, not the hold. Each opener re-reads `max` at the
top of `Migrate`, before touching the lock, so one that arrives after the first
has committed skips every file at `if ver <= max` and never reaches the
recheck. On a loaded two-CPU box the six goroutines do not overlap: opener 0
finishes all three files before opener 1 is scheduled, and the total runtime is
one burn rather than six.

So `L226`'s size change is innocent — it weakened nothing that was working.
Two consequences follow. The recheck-and-skip branch has NO coverage on
SQLite at all (the Postgres side is covered, because `pg_advisory_xact_lock`
was proved by its own mutation). And a guard has to count what happens INSIDE
`Migrate`: a first attempt read the version in the goroutine just before
calling `Migrate`, which counts openers that then re-read a finished state and
skip, so it reports a race that did not occur. It was measured, found
unfaithful, and not shipped.

- **Revised fix:** have `Migrate` report whether it applied or skipped each
  file, and make the test require at least one of each. That is also something
  `arizuko migrate` could print, which the silent path today cannot.

## ✅ FIXED 2026-09-06 L227 — the one-opener guard stops at the test boundary, so the suite cannot be pointed at another engine (2026-09-06)

**FIXED 2026-09-06 in `a5ba8193` and `496d2f94`. All 39 sites converted, ZERO
exempted.** Two `sql.Open("sqlite"` remain in the tree: the seam itself, and
the guard's own string literal.

The seam is `db_utils.NewTestDB(name) (dsn, release, err)`, which returns a DSN
and links no `testing` — that matters, because `store.OpenMem`, `routd.OpenMem`
and `runed.OpenMem` are PRODUCTION files, and a decision living in a
`testing`-importing package would link `testing` into every daemon binary.
`db_utils/dbtest.Open(t)` is the ergonomic wrapper for the 22 sites that just
want an empty database; the other 17 read a file the code under test wrote, so
their DSN is dictated by the artifact and they call `Open(FileDSN(path))`.

The guard now scans `_test.go` and its only exemption is `db_utils`'s own
directory — the named-file list is gone, and `db_utils/dbtest` is guarded like
everything else. Proved by mutation: a direct open added to
`routd/packages_store_test.go` fails the build naming the file and line.

Pointing the suite at Postgres now fails on `AUTOINCREMENT` and `blob`, which
is exactly the `L228` boundary — every test reaches the engine and fails on
schema rather than on "there is no way to ask". Zero test databases are left
behind.

Two behaviour deltas worth knowing: several `":memory:"` sites ran with FOREIGN
KEYS OFF and now run production's pragma set, and `onbod`'s
`SetMaxOpenConns(1)` workaround for `:memory:` per-connection semantics is gone
because the shared-cache memdb makes it unnecessary.

MEASURED, and confirmed independently: `git grep -c 'sql.Open("sqlite"' --
'*_test.go'` sums to **40 sites across 26 files**. `db_utils/opener_test.go`
exempts `_test.go` by design, so every one of them bypasses the seam. On top of
that `store.OpenMem`, `routd.OpenMem` and `runed.OpenMem` each build their own
in-memory SQLite DSN.

`6/37` step 6 asks that the same suite pass against both engines.
`db_utils`'s own suite now does, once per engine. The rest of the repo cannot
even be asked the question: there is no single place to point it at another
engine, because forty test files each open SQLite directly.

This is independent of the schema work (`L228`). A Postgres-compatible schema
would still leave the suite unable to run against it.

- **Severity:** medium — it blocks the proof `6/37` step 6 defines, and every
  new test file adds another site
- **Scope:** `db_utils/opener_test.go`, the three `OpenMem` builders, 26 test
  files
- **Fix:** give the three `OpenMem` builders one engine-aware source, then
  extend the guard past the test boundary so a new direct `sql.Open` is caught
  the day it is written — the same move that made the production side one seam

## ✅ FIXED 2026-09-18 L232 — the concurrent `createVersionTable` race had no test (2026-09-06)

Moot: `createVersionTable` was `db_utils`, which is deleted. `go list -deps`
reaches `database/sql` from nothing.

- **Status:** moot in v0.68.0
## ✅ FIXED 2026-09-06 L233 — the per-turn gateway key broke the end-to-end turn, and `make test` cannot see it (2026-09-06)

**FIXED 2026-09-06 in `960772ef`. Three distinct causes, each real, found by
running it rather than reading it.**

1. **The mint was never wired in the harness.** routd reads the gateway pair
   from `routd.LoopConfig`; the container reads it from `core.Config`. The e2e
   set only the second, so `container.Run`'s guard fired correctly on a mint
   that never happened. Production wires both from `MODEL_GATEWAY_URL` and
   `MODEL_GATEWAY_KEY` in `routd/cmd/routd/main.go`, and refuses to boot on a
   URL without a key — so this is a harness gap, not a live trap.
2. **Two routes to one gateway.** The container reaches the fake across the
   docker bridge and routd reaches it on loopback. In a deployment both sides
   sit inside the compose network and the value is one.
3. **Cost is now asynchronous, and `6/23` shipped that without saying so.**
   routd polls `/spend/logs` after the run returns, because LiteLLM writes
   usage on a batch timer. The test read `cost_log` straight after the reply
   arrived; the write landed a second later against a closed database. **The
   reply landing is no longer the cost landing** — worth knowing for any
   operator surface that shows a turn's cost.

The fake now serves `/key/generate`, `/spend/logs` and `/key/delete`, derives
spend from the tokens it actually served, and takes its price from the scripted
reply so the assertion and the fake read ONE declaration. The test asserts the
container presented a MINTED key, not the instance master key — proof the mint
happens at all, which nothing checked end to end before.

Runtime went 241s failing to 10.7s passing.

**The process half stays open, and it is the part that matters.** `make test`
is `-short` and skips the e2e, so this shipped through every merge with 64
packages reported green — by two agents and by me. Same shape as `L226`. Decide
whether `make test` carries the e2e, or whether shipping a spec's mechanism
must run `make test-e2e` before merge.

REPRODUCED on the merged tree, then fixed in the same pass — recorded because
the way it shipped matters more than the fix.

`TestE2ETurn_CostAccounting` fails: `run outcome error … err="model gateway
configured but this run carries no virtual key; routd mints one per turn (spec
6/23)"`, then a four-minute `WaitForRow` timeout. The turn never runs.

Cause: `6/23` made routd mint a key per turn against `MODEL_GATEWAY_URL`, and
the e2e's `testutils.FakeLLM` serves `POST /v1/messages` and 404s everything
else. So the mint fails and `container.Run` refuses the spawn — correctly, by
its own new guard. The unit side was covered (`routd/gateway_test.go` has a
fake gateway); the e2e fake was not updated with it.

**`make test` is `-short`, which skips the e2e, so the suite reported 64
packages ok through every merge today.** That is the same shape as `L226`: a
real failure sitting behind the flag nobody runs by default. Two agents and I
each ran the short suite and called the tree green.

- **Severity:** medium — no instance is affected (none sets
  `MODEL_GATEWAY_URL`), but the gate that would have caught it is not in the
  loop anyone runs
- **Scope:** `tests/testutils/fakellm.go`, `tests/e2e_turn_test.go`, the
  `test`/`test-e2e` split
- **Fix:** the fake now serves the gateway admin surface — `/key/generate`,
  `/spend/logs`, `/key/delete` — and derives spend from the tokens it actually
  served, so the e2e proves the per-turn mint end to end and asserts the
  container presented the MINTED key rather than the instance master key. The
  open half is process: decide whether `make test` should carry the e2e, or
  whether shipping a spec's mechanism must run `make test-e2e` before merge.

## ✅ FIXED 2026-09-19 L234 — `template/web/pub/arizuko/legacy/` is a second publicly-reachable site documenting deleted machinery (2026-09-06)

MEASURED while fixing `L220`. The `legacy/` tree is a whole second copy of the
operator site, linked from `changelog/index.html:183`, so a reader reaches it.
It documents machinery that no longer exists: the `gated` daemon (deleted at
v0.50.0), 35 mentions of the retired `CHANNEL_SECRET`, tier-0/tier-1
authorization (tiers are gone per `5/33`), and a cost gate "in `gateway/`".

It carried 19 of the 31 files the `L220` sweep had to edit, so it is not inert
— it is a maintenance tax that pays for nothing, and it hands readers a picture
of a system that is not this one.

- **Severity:** medium — public, and wrong in ways a reader cannot detect
- **Scope:** `template/web/pub/arizuko/legacy/`
- **Fix:** delete it, or unlink it and mark it an archive with a date at the
  top of every page. Deleting a published tree is a decision, not a fix —
  hence this entry rather than a commit.

## ✅ FIXED 2026-09-18 L235 — `arizuko-client.js` shipped in copies that had diverged (2026-09-06)

The served copy (`webd/assets/`) and the documented copy
(`template/web/pub/arizuko/assets/`) differed by one line: the served one cited
`specs/1/W-slink.md`, a spec that does not exist. Nothing said they had drifted.

- **Status:** fixed 2026-09-19
- **Fix:** the 75 files are archived to
  `/srv/data/arizuko_krons/legacy-docs-20260919.tar.gz` and removed from the
  served tree one named path at a time. `legacy/components/gated.html` answers
  404; every current section still answers 200, and the live tree now matches
  `template/web/pub/arizuko/` file for file (121 each, nothing live-only).
- **Why it mattered more than a stale page.** The agent's `arizuko` skill browses
  `/var/lib/www/arizuko/`, which is this same tree mounted read-only. An agent
  asked "how does arizuko work" could read `legacy/components/gated.html` and
  `legacy/components/mastd.html` and answer with two daemons that were deleted in
  June. Nothing in the repo regenerated it: it existed on krons and nowhere else,
  and no current page linked to it.
- **Fix:** the served copy takes the live citation, `specs/5/W-webhook-routes.md`,
  so the two are byte-identical again.
- **Lock:** `webd.TestBrowserSDKCopiesAreIdentical` compares their digests, so a
  change to either fails until both carry it. The third copy the entry named,
  under `legacy/`, is not in the source tree at all.
## ✅ FIXED 2026-09-18 L236 — repo links on the public site pointed at Go files that do not exist (2026-09-06)

Eleven links across two pages pointed into `gateway/gateway.go`,
`gateway/commands.go` and `routd/seed_grants.go`. The gateway daemon was deleted
in the split, and grant seeding moved into `store/seed.go`.

- **Status:** fixed in v0.68.2
- **Fix:** each points at the file that owns the behaviour now — topics at
  `routd/mcp.go`, the grant floor at `store/seed.go` — and the `#L` anchors go
  with them: a line number is a claim that rots on the next edit, and every one
  of these was already wrong.
- **Measured after:** all 62 repo links on the site resolve to a file that
  exists.
## L237 — a spec renumber that lands on a live number cannot be caught by any check (2026-09-06, open)

OBSERVED while fixing `L220`. `legacy/reference/mcp.html` said "Spec 5/5
(uniform MCP/REST registry)". `5/5` today is `worlds-agents-sessions`. The
citation resolves, so neither `tests/specrefs` nor the new `tests/webdocs`
guard can flag it — the number is live, it just names a different spec.

Only the filename form is self-checking: `5/5-uniform-mcp-rest.md` fails to
resolve the moment the file is gone, while a bare `5/5` silently retargets.

- **Severity:** low — one known instance, but it is undetectable by
  construction, which is what makes it worth a rule
- **Scope:** the citation convention
- **Fix:** a house rule that a spec is cited by filename wherever a link is
  possible, and the bare `N/M` form is reserved for prose that names a spec the
  sentence also describes. Then the existing guards cover the rest.

## ✅ FIXED 2026-09-18 L238 — 94 test sites discarded `OpenMem`'s error (2026-09-06)

`s, _ := OpenMem()` then a method call is a nil dereference when the open fails,
and the open is a NETWORK call now — an endpoint that is down reports as a panic
rather than a failure. The sites also registered no cleanup, so each one left its
namespace's tables on the endpoint.

- **Status:** fixed in v0.68.2
- **Fix:** every site takes the package's own fatal helper — `store.openMem(t)`
  and `routd.memDB(t)` — which reports the error and drops the namespace at
  cleanup. Both already existed; 94 sites across 15 files bypassed them.
## ✅ FIXED 2026-09-18 L239 — `OpenMem` opened Postgres, so its name was wrong (2026-09-06)

Moot in its stated form: nothing opens Postgres directly. `OpenMem` reserves a
private table namespace on the endpoint the test process dials, which is what
"a fresh private store for one test" means.

- **Status:** moot in v0.68.0
## ✅ FIXED 2026-09-18 L243 — `system_messages` survived a group delete (2026-09-06)

Fixed: `SystemMessages` is in `entityScopePurge` with its own folder extractor
(`store/folder_scope.go`), so the subtree delete takes it. The two-schema
disagreement the entry names is moot — there is one schema.

- **Status:** fixed
- **Lock:** `TestFolderScope_ClassifiesEveryDeclaredEntity` fails if an entity
  joins an owner's declaration without a verdict.
## L244 — `invites` and `invite_redemptions` are folder-scoped in another database, so no purge reaches them (2026-09-06, open)

FOUND while closing `L198`. Both live in onbod.db and carry `target_glob`,
which names a folder subtree. A redemption that outlives its folder authorizes
`handleCreateWorld` under the re-created path, admitting someone the new tenant
never invited.

One transaction on routd.db cannot reach a second database, so this is not
fixable by extending the purge. It is the first concrete case for `6/37`
redesign 1: a generation makes a stale row unreachable without needing the two
databases to commit together.

- **Severity:** medium — admission to a re-created folder by a stale invite
- **Scope:** `onbod/`, `specs/6/37` redesign 1
- **Fix:** the generation, or an onbod-side sweep keyed on the folder's absence

## ✅ FIXED 2026-09-18 L245 — routd's `registerGroup` rollback deleted a subtree it did not create (2026-09-06)

`PutGroup` upserts, so a failed route insert on a folder that ALREADY existed
rolled back rows the call never created — and the purge is a subtree delete, so
it took the folder's children with it.

- **Status:** fixed in v0.68.2
- **Fix:** `registerGroup` asks whether the folder existed BEFORE it upserts, and
  rolls back only when it did not. That is the entry's own stated fix.
- **Test:** `routd.TestRegisterGroup_RollsBackOnlyWhatItCreated` — a pre-existing
  folder and its child both survive a failed route insert.
## ✅ FIXED 2026-09-20 L246 — orphan rows from the old delete cannot be cleaned through the new one (2026-09-06)

MEASURED. marinade carries one orphan route, four engagement rows and two
sessions for `libtest` and `atlas/tom`, left by the delete path `L198` fixed.
`DeleteGroup` no-ops when no `groups` row matches, so the fixed purge cannot
reach them: the row that would trigger it is exactly the row that is gone.

`atlas/tom` is a child folder, which the exact-match `groups` delete could
never have reached in the first place.

- **Severity:** low — stale rows, no authorization among them (zero `acl`
  orphans fleet-wide)
- **Scope:** `store/`, `cmd/arizuko`
- **Fix:** a `purge <folder>` verb that runs the scope without requiring the
  `groups` row, or `6/37` redesign 1's generation, which makes an orphan
  unreachable rather than absent


- **Status:** closed on re-check 2026-09-20 — the premise is false now.
- **Re-measured:** `purgeFolder` (`store/folder_scope.go:287-343`) returns
  `errNoSuchGroup` only when `folder == ""`. It then sweeps every scope and
  deletes matching items with `TryWriteEntities` per chunk. `DeleteGroup`
  (`store/groups.go:351-353`) reads `removed == 0` only AFTER that sweep ran, so
  it skips the audit row — never the delete. The gate went in `ce5ccaa0`,
  "deleting a folder is a sweep, not a transaction".
- **Two residuals, filed as `L323` rather than left here:** the comment at
  `store/folder_scope.go:275` still says the purge rolls back, and both callers
  report failure after a successful sweep (`cmd/arizuko/main.go:475` dies with
  "no group at X"; `dashd/groups_admin.go:416` answers 404), so the cleanup
  happens and nothing tells the operator.

## ✅ FIXED 2026-09-20 L247 — `handleGroupCreate` has no test, and it is the security-bearing transaction (2026-09-06)

MEASURED while giving `dashd` a storage layer. `dashstore.GroupExists` and
`dashstore.CreateGroupWithAdmin` sit at **0% coverage**, and
`handleGroupCreate` has no test at all — no reference to it or to
`POST /dash/groups/new` anywhere in `dashd/*_test.go` or `tests/`.

That call writes the group row, the member edge, the admin grant and the audit
row in one transaction. `specs/6/38`'s census names exactly this shape as
security-bearing: the escalation it prevents is a folder whose creator does not
get the admin grant, or gets one the audit does not record. It is also the
transaction `L198` had to reason about from the other end.

The rest of the new package is at 76.2%, so this is a hole rather than a
pattern.

- **Severity:** medium — untested, not known broken; the risk is that a future
  change to the three-write sequence has nothing to catch it
- **Scope:** `dashd/`, `dashd/dashstore`
- **Status:** fixed 2026-09-20.
- **Fix:** `dashd/group_create_test.go` drives `POST /dash/groups/new` through
  the mux. `TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder` asserts the
  group row, the creator's `admin` grant and the `group.create` audit row;
  `TestGroupCreatePage_RefusesAFolderTheCallerCannotAdminister` pins that a
  caller without admin on the target gets no row written.

  **The first assertion was a false green and the mutation caught it.** It read
  the verdict from `auth.Authorize`, but `splitAdminDash` seeds the creator
  `**`, so the evaluator answers yes whether or not the transaction wrote
  anything — scoping the grant to `folder + "-MUTANT"` passed. It now reads the
  acl ROW back through `ListACL` and keeps the `Authorize` call beside it, so
  the test says both "the row was written" and "the evaluator honours it".
  VERIFIED by mutation: a wrong grant scope and a renamed audit action each
  turn it red, and both revert green.

## ✅ FIXED 2026-09-20 L248 — four dashd list reads now surface a mid-iteration failure that was silently dropped (2026-09-06)

RECORDED as a change, not a bug, because it is a real behaviour delta shipped
inside a refactor and it deserves to be visible.

`InstalledPackages`, `ProactiveCooldowns`, `RoutesIntoFolder` and the secret-key
read now return `rows.Err()`, and their handlers banner on it. Before, a driver
failure PART WAY THROUGH iteration was dropped and the page rendered a
truncated table with no trace — the operator saw a short list and no error. The
open-error path is unchanged.

This aligns with "fail loud, fail to the user", so the delta is in the right
direction. No test covers it either way.

- **Severity:** low — strictly more honest than what it replaces
- **Scope:** `dashd/dashstore`
- **Fix:** a test that injects a mid-iteration failure and asserts the banner,
  which would also pin the four call sites against a silent regression


- **Status:** closed on re-check 2026-09-20 — the mechanism is gone.
- **Re-measured:** `grep -rn 'rows.Err()'` over the tree (excluding the
  untracked `.migrate*` copies) returns **0 hits**. The four functions survive
  and read through `objstore.Query` (`dashd/dashstore/pages.go:30-51`, `:96-97`,
  `:121-145`), which returns a complete slice or an error — there is no
  mid-iteration failure left to drop, so the entry's stated fix is
  unimplementable as written. The SQLite→DynamoDB move took it (`9698de97`).
  The error-surfacing behaviour it praised survives in another shape
  (`dashd/proactive_page.go:140-145`).

## ✅ FIXED 2026-09-18 L228 — 30 migration files were SQLite-only (2026-09-06)

Moot: `store/migrations/` and `routd/migrations/` are deleted. An entity has no
DDL past `CreateTable`, so there is no chain to port.

- **Status:** moot in v0.68.0
## ✅ FIXED 2026-09-06 L230 — no CLI can grant a single ext tool, because `grantableAction` refuses the scope shape the filter reads (2026-09-06)

**FIXED 2026-09-06 in `b7ad983a`.** `auth.ExtScopePrefix` declares the
namespace once, beside `ShellEnvPrefix`, and `grantableAction` accepts it. The
unknown-action message names the shape too — the mutation that proved the test
surfaced that the message is user-facing and was about to be wrong.

`cmd/arizuko/grant_action_test.go` now records this class three times over:
`egress` after migration 0023, `shell:env:<KEY>`, and this. Each has the same
shape — an evaluator learns an action and nothing can write a row for it — and
each asserts the same second half, that the default `admin` grant does NOT
reach it, because the lattice stops at `mcp:*`.

Proved by mutation: break the prefix match and the test goes red with
`unknown action "ext:cloudflare:dns:read"`.

MEASURED while fixing `L222`. The ext-tool announce filter checks
`tool.Scope`, which the embedded providers write as
`ext:<provider>:<area>:<verb>` — for example `ext:cloudflare:dns:read`.
`grantableAction` (`cmd/arizuko/main.go:596`) accepts an action only with an
`mcp:` or `shell:env:` prefix, or a `.`, so it rejects every one of those
scopes.

The consequence: an operator cannot open ONE ext tool to a folder. The only
rows that reach it are a blanket `*` and an operator `/root` elevation, both of
which confer everything. A test that needed the row had to write it directly
rather than through `arizuko grant`.

This is the `grant_action_needs_a_door` class exactly — the same shape that
cost the 0023 egress outage, where a new action existed in the evaluator and
nothing could write a row for it.

- **Severity:** medium — a shipped capability has no least-privilege path, so
  using it at all means granting everything
- **Scope:** `cmd/arizuko/main.go`, the ext announce filter
- **Fix:** teach `grantableAction` the `ext:` prefix. Then check the same
  question for every other scope namespace a filter reads, since this is the
  second instance of one bug class.

## ✅ FIXED 2026-09-06 L231 — `writeJSON` drops an unmarshalable response with no error frame, so the caller waits out its timeout (2026-09-06)

**FIXED 2026-09-06 in `73b26588`.** `writeJSON` takes the request id as a first
parameter, bound once per request as `reply := func(v any) { writeJSON(head.ID,
v) }`, so the two submit handlers learn nothing about the failure path.
`rpcEncodeFailure` assembles the frame as BYTES rather than marshalling a
struct, and the only external part — the id — is re-marshalled alone with a
`null` fallback, so the replacement cannot fail the way the original did. The
cause stays in the ERROR log and out of the frame: it names a Go type the agent
cannot act on, and putting it in the frame means marshalling a second value at
the one place that must not marshal.

Reproduced end to end rather than by reading: a tool returning
`StructuredContent: make(chan int)` made the client wait out its deadline.
Proved by four mutations, including one that pins the frame's wording, because
the socket test would go vacuous if the MCP library ever emitted its own
`-32603` — the same shape of vacuity `L229` is about.

This also closes the 2026-07-13 sweep bullet that reads "Error-frame delivery
stays open: `writeJSON` takes an opaque `v`, so a proper JSON-RPC error frame
needs the request id threaded in — design, not a log-add."

MEASURED while fixing `L222`. `ipc/ipc.go` logs at ERROR with the folder and
the error, then returns without writing anything. The agent is a JSON-RPC
client: with no response and no error frame it blocks until its read deadline —
10.01 seconds, measured.

`L222` removed the only reachable way to produce an unmarshalable response, so
nothing triggers this today. It stays a reporting gap: any future response that
cannot marshal costs the turn a silent timeout rather than an error the agent
can act on or report.

- **Severity:** low — unreachable now, and the log line is loud; what is
  missing is delivery to the caller
- **Scope:** `ipc/ipc.go`
- **Fix:** write a JSON-RPC error frame when marshalling the result fails, so
  the failure reaches the agent instead of only the journal

## ✅ FIXED 2026-09-06 L219 — restoring an archive re-introduces the mixed-width timestamps `L97` removed (2026-09-06)

**FIXED 2026-09-06 in `3c932b5c` and `706d1b95`. The open policy question
resolves to REFUSE THE ROW, never the restore**, and the reason is structural
rather than a preference.

`applyArchive` is not atomic: it walks a tar, commits config, then secrets,
then messages in batches of 500, so an error returns with everything before it
already written. "Refuse the restore" cannot mean "roll back" — that option
does not exist in this code. Validating up front is against the design too:
the messages entry is a deliberately unbounded streaming tar entry, batched so
a long transfer never holds the write lock.

And a whole-archive refusal would make krons's own archive unrestorable.
Measured on copies of all three live databases: krons holds exactly two rows
migration `0044` cannot parse, `test-trigger-1775133527` and
`debug-1775135388`, both space-separator debug rows that `0044` skips by
design; marinade and sloth hold none. Refusing the archive would abort after
config and secrets had committed, over two dead rows. Per-row refusal protects
the invariant identically — neither writes the bad value — and the import is
idempotent, so the operator corrects the row and re-runs while everything
already landed is a no-op. The refused rows are named in the apply report.

**A sibling carried the same defect.** `cmd/arizuko/migrate_split.go` runs the
migrations, `0044` among them, and THEN bulk-copies `messages.timestamp`,
`chats.agent_cursor` and `sessions.observed_cursor` verbatim into a database
where `0044` is already recorded and will never revisit them. Fixed with
`routd.NormalizeFixedWidthTimestamps`, which executes the embedded `0044`
file — one SQL text with two callers, not a second copy of the padding.

**Negative finding worth as much as the fix.** The archive's other imported
timestamps are NOT siblings. `onboarding`, `invites`, `route_tokens` and
`pending_actions` write and compare plain `time.RFC3339`, which is
fraction-free and therefore already fixed width. Re-rendering those through
`core.TS` would ADD a fraction and create this defect where it does not exist —
the trap `L97` names. The fix touches `messages.timestamp` only.

MEASURED. `resreg/archive.go:101` carries `Timestamp string` verbatim out of an
archive and writes it into `messages.timestamp` at `:206`. Nothing re-renders
it. `DeriveAgentCursors` (`:268`) then derives a cursor with a string
`MAX(timestamp)` and compares `excluded.agent_cursor > COALESCE(...)`.

So an archive written before migration `0044` restores rows in the stripped
`time.RFC3339Nano` shape into a column the rest of the system now guarantees is
fixed width, and the whole `L97` class returns for those chats: a nanosecond
row lands behind a whole-second cursor and is skipped for good.

- **Severity:** medium — it needs an archive restore to fire, but the failure
  is silent and permanent for the chats it touches
- **Scope:** `resreg/archive.go`
- **Fix (proposal):** parse each archived timestamp and re-render it through
  `core.TS` on the way in. What to do with one that will not parse is the part
  that needs sign-off: refusing the restore is loud and correct by this repo's
  rules, but it takes a working recovery path away from an operator holding a
  hand-edited archive.

## ✅ FIXED 2026-09-06 L218 — `6/18` says "four tables per queue"; the migration it cites creates five, and a sixth in partitioned mode (2026-09-06, docs)

**FIXED 2026-09-06 in `64dcc899`, re-counted from the fetched file.**
`0007_absurd_workflows.sql` at `20f3021` creates `t_`, `r_`, `c_`, `e_`, `w_`
at `:150-230` and `i_` at `:232-240`, inside `if v_storage_mode =
'partitioned'`. The spec names all five with their prefixes, says `e_<queue>`
is the row `emit_event` first-writes, and records that an unpartitioned queue
carries `idempotency_key` as a unique column on `t_<queue>` instead of a
sixth table. The cited range moves `:151-230` → `:150-230` so it starts at the
statement rather than inside it, and the reopen trigger says five tables.
The decline is untouched.

VERIFIED against the source, not the summary. Fetched
`services/api-rs/crates/centaur-session-sqlx/migrations/0007_absurd_workflows.sql`
from `paradigmxyz/centaur` at the exact commit `6/18` cites,
`20f3021728a826c0b2dc44177a213fbad1e45202`.

The per-queue generator emits six `create table` statements with the prefixes
`t_`, `r_`, `c_`, `e_`, `w_` and `i_`. The spec counts four — tasks, runs,
checkpoints, waits — and omits `e_<queue>`, the events store
(`event_name text primary key`). `i_<queue>` (idempotency) is the sixth and
appears in partitioned mode.

The spec's own "External events" bullet describes `emit_event` as
"first-write-wins the event row", so the row it forgot to count is the one its
prose depends on.

This does not touch the decline itself, which stands: nothing in arizuko
implements a workflow engine, verified by grep for checkpoint, lease and
claim-expiry machinery.

- **Severity:** low — a wrong count in a document whose value is precise
  measurement
- **Scope:** `specs/6/18`
- **Fix:** five tables, six under partitioning; name `e_<queue>`

## L241 — the `6/40` audit intent log loses successful deletes and races active or later writers (2026-09-06, proposed)

The pending-audit back-reference proves only that one row currently names the
audit id. A successful delete leaves no row, a partial multi-row write leaves
enough evidence to look complete, and a later update or delete erases the
marker before reconciliation. A concurrent sweep can also observe no marker,
delete the pending audit, and then let the active writer commit its data.
`runAudited` includes all these shapes, and builds some audit fields only after
the mutation.

- **Severity:** high
- **Scope:** `specs/6/40-store-surface.md` audited-write protocol
- **Affected:** any future non-transactional store implementation
- **Source:** `specs/6/40-store-surface.md:74-97`; `store/audit_helpers.go:10-28`; `store/groups.go:106-133`; `store/proxyd_routes.go:127-162`
- **Status:** proposed — redesign needs sign-off
- **Fix:**

## L242 — the `6/40` three-operation surface omits graph and multi-row invariants (2026-09-06, proposed)

`AddMembership` reads the full ancestor closure and inserts the edge under the
same transaction. No path-prefix, single-row conditional write, or scoped
replace expresses that test. Invite redemption and `/always` also condition
one row and atomically write other rows, while `DueTasks` conditionally updates
and returns an unbounded due set.

- **Severity:** high
- **Scope:** `specs/6/40-store-surface.md` operation census
- **Affected:** membership, invite redemption, held-call resolution, scheduler
- **Source:** `store/membership.go:18-84`; `store/invites.go:265-337`; `routd/pending_actions.go:356-407`; `store/tasks.go:77-97`
- **Status:** proposed — redesign needs sign-off
- **Fix:**
