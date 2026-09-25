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
- `L273`, `L249`, `L319`, `L159`, `L202`, `L283` — proposals, each costed in
  its entry.
- `L339`, `L167`, `L200` — proposals too. `L200` and `L133` were re-measured
  2026-09-21 and each came back narrower than filed.
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
- `L360` — is one reference clone shared across groups, or copied per group?
  Sharing couples them to one revision; copying costs about 10 G today and
  serves a third-party `.git` tree from the public webroot.
- `L358` — the helm chart renders a `crackbox:latest` proxy wired to `gated`, a
  daemon deleted at v0.50.0. Deleting it is four files; whether the chart
  carries an egress story at all is a deploy decision, because k8s has no
  per-folder docker network for the per-spawn proxy to attach to.

**An operator action on live data.** The platform side is done in each.
(`L334` is closed and carries one too: the third party's `**` on sloth is not
revoked — see its entry.)

- `L318` — the 19 per-daemon store keys. The resource list is declared and
  checked; the ROLLOUT is the live-key rotation.
- `L327` — which Discord channels should answer. The inversion CHECK shipped
  (`/eval` 22); which rows to renumber is your call.
- `L308`, `L181`, `L131`, `L188`.
- `L364` — `route rm <id>` on sloth deletes the WRONG row: six ids are duplicated
  by the marinade merge. It already destroyed a live rule once. Re-minting sloth's
  route ids is yours; do not address a sloth rule by id until then.
- `L363` — sloth advertises five atlas Telegram routes and can serve one. The
  bot the others talk to is served by nothing, and its only token is in the
  staged marinade tree. Three options in the entry; one is a deploy.
- `L307` — seven Slack chats carry traffic and match no route, live on sloth.
  Which route rows to write is yours.
- `L62` — emaid retries an IMAP auth rejection forever; the marinade half is
  moot (instance removed 2026-09-25), the code defect is not.
- `L63` — krons whapd wants a phone re-pair; the recipe is in the entry.
- `L359` — 11 G of interrupted git repacks, on a disk with 3.9 G free. The
  files are unreferenced and the deletion is live data, so it is yours.

**Outside this repo.**

- `L40` — the edge must set `X-Forwarded-For` before `TRUSTED_PROXIES` may
  widen. Adding the gateway without that buys nothing.
- `L136` — the `--internal` network does not seal UDP/53; the decision is what
  is outstanding, the docs are already true.

**Blocked on another entry.**

- `L189` — blocked on `L137`. Do not take it alone.

**The gate itself.**

- `L357` — `.golangci.yml` is v1, golangci-lint is v2, and it refuses to load.
  Pin the action to v1, or migrate and face 81 findings.

**A pointer that resolves and lies.**


- `L355` — the comment audit shipped; what is left is 31 citations inside Go
  string literals (three pinned by tests) and four stale § names.

**Dead code with a price.**

- `L351` — six `import.go` files, 1120 lines, no production caller. They are
  the test-seeding API wearing a deleted tool's documentation; one decision
  covers all six.

**A spec whose status is a claim.**

- `L350` — `6/37` cites six deleted artifacts, four of them its own guards. The
  invariant still holds; the tests that held it do not.

## ✅ FIXED 2026-09-21 L333 — an operator-only slash command refuses without naming the principal it tested, and the refusal leaves no trace

sloth's operator typed `/root <request>` in the Telegram DM on 2026-09-17 and
2026-09-21. This entry is the 2026-09-21 one, where routd DID intercept the
command: it consumed the row (`routd.chats.agent_cursor` advanced to
`…/4181`), dispatched no turn, wrote no `root-…` re-injection, and answered:

```
Permission denied: /root requires an operator grant (**).
```

The sentence is true and useless. It does not say WHICH principal was tested,
and the operator cannot see it: a chat identity is a platform id the person
never types, and `IsOperator` reads it verbatim — `auth.Authorize` resolves
nothing on the way in (`auth/authorize.go:13-16`, "There is no resolve step
here"). So a grant row written against a different platform id reads exactly
like the platform refusing everybody, and the operator concluded `/root` was
broken. The agent published the opposite diagnosis into the chat:
*"`$ARIZUKO_IS_ROOT` is empty … it reached me as plain text (not intercepted as
an operator command)"* (`telegram:user/1112184352`, 2026-09-18T20:41:17Z). That
sentence is TRUE of the 2026-09-17 attempt and false of this one — the two
attempts failed in two different ways, and the other one is `L336`. Nothing the
operator could see distinguished them.

**And nothing on the instance recorded the refusal.** An ack goes straight to
the platform through the Deliverer (`routd/deliver.go:86`) and is never
appended, so there is no `messages` row. `IsOperator` writes no `audit_log`
row. Nothing logged. The only surviving evidence of the 2026-09-21 attempt was
teled's own idempotency record:

```sql
-- sloth, teled.outbound_dedup
select pk, item_data->'chat_jid'->>'S', item_data->'created_at'->>'S'
  from "_ddb_163d7d83-363a-42c1-9e4c-095e3106c8ee" where pk like 'ack-%';
-- ack--1789973515767194979 | telegram:user/1112184352 | 2026-09-21T06:51:55Z
```

Four commands shared the gate and each spelled its own refusal —
`/status`, `/invite` and `/gate` said only `Permission denied: operator only.`

- **Severity:** high (the operator's escalation path. 8 Telegram `/root`
  attempts on this workload since 2026-04-02 and not one succeeded; the denial
  never said why, and the agent published the wrong cause into the chat)
- **Scope:** `routd/steer.go` — `cmdRoot`, `cmdStatus`, `cmdInvite`, `cmdGate`
- **Source:** sloth `routd.messages` (`telegram:user/1112184352`,
  2026-09-21T06:52:28Z, id `…/4181`), `routd.chats.agent_cursor` advanced to
  that id, `teled.outbound_dedup` row `ack--1789973515767194979` → Telegram
  message 4182, and NO `dispatch run` for 4181 in `journalctl -u arizuko_sloth`
- **Status:** FIXED
- **Fix:** one renderer, `Loop.denyNonOperator(chatJID, sender, cmd)`, used by
  all four. It names the principal (`… requires an operator grant (**);
  "telegram:user/1112184352" has none.`) and logs the denial at WARN, which is
  the operator-side half — a steer refusal now leaves a trace somewhere.
  Test: `TestOperatorDenialNamesTheSender` (`routd/steer_commands_test.go`) —
  fails on all four commands without the fix. It does NOT change who may run
  `/root`; the wrong-principal data is `L334`. (`4f78901e`)

## ✅ FIXED 2026-09-21 L340 — `ASSISTANT_NAME` has two declared defaults with two different values, and the test that should catch it is a tautology (2026-09-21, open)

`core/config.go:37` declares `DefaultAssistantName = "Andy"` under a comment
that forbids precisely this:

> TWO READERS OF ONE ENV VAR ARE FINE; two declarations of its DEFAULT are not
> — they drift silently and the drift only shows on the instance that hits the
> fallback (BUGS `L194`, the shape `F71` already paid for).

`compose/compose.go:767` seeds `"ASSISTANT_NAME": "arizuko"` when the instance
`.env` omits it, and `ASSISTANT_NAME` is in `commonKeys` (`:80`), so that value
is written into every daemon's scoped env file. **The daemon therefore reads
`arizuko`. `Andy` surfaces only where compose never wrote the var** — a bare
binary outside compose, which is the case `L194` was closed to make impossible.

**The test named for this cannot detect it.** `core/config_test.go`
`TestTheSharedDefaultsAreNamedOnce` asserts `DefaultAssistantName != "Andy"`,
`DefaultWhisperModel != "turbo"`, `DefaultMediaMaxBytes != 20*1024*1024` — each
constant against its own literal, in the same package. The constant IS the
declaration, so the comparison holds by construction. It is green right now
while the drift is live.

- **Severity:** low in effect (a name), high as a class — this is `L194`'s
  exact shape, reappearing under the comment that names it
- **Scope:** the shared defaults
- **Affected:** every instance generated by compose
- **Source:** `core/config.go:30-40`; `compose/compose.go:80,767`;
  `core/config_test.go` `TestTheSharedDefaultsAreNamedOnce`
- **Status:** fixed
- **Fix:** compose reads `core.DefaultAssistantName` instead of its own
  literal, so one declaration has two readers. Then rewrite the test to assert
  what it claims to: that the value compose seeds EQUALS the constant. A test
  comparing a constant to its own literal pins nothing, and should not be left
  standing as evidence — it is why this survived.
  Check the other two the same way before assuming only this one drifted.

**FIXED 2026-09-21, and the resolution went the way that changes nothing.**
`ASSISTANT_NAME` is in `commonKeys`, so compose's seeded value is what every
daemon has always read — `arizuko`. Making compose read the constant while the
constant said `Andy` would have renamed the assistant on every new instance.
So the CONSTANT takes the value the fleet already uses, and compose reads it:
one declaration, two readers, zero behaviour change.

`seedSharedDefaults` (`compose/compose.go`) is extracted from `Generate` so the
invariant is testable at all — it was inline, and `Generate` needs a whole
instance layout to drive.

- **Status:** fixed
- **Fix:** `core.DefaultAssistantName` is `arizuko`; `compose` reads it.
  `TestSeededAssistantNameIsTheSharedDefault` pins the two sides equal, proven
  by mutation with a DIFFERING literal — mutating both sides together passes,
  which is the trap that made the old test worthless.
  `TestTheSharedDefaultsAreNamedOnce` no longer compares a constant to its own
  literal; it asserts what the declaration is for, that `EnvOr` on an unset var
  resolves to the shared default.

## L368 — the mount table carries HOST paths and virtiofsd runs inside runed, so every VM turn fails to stage its tree (2026-09-25, open)

**Measured on krons, 2026-09-25, on the first turn after the flip:**

```
run outcome error folder=krons/content turn_id=cli-1790375856053172002
  err="staging tree: mount source /srv/data/arizuko_krons/groups/krons/content
       (guest /home/node): stat …: no such file or directory" retry_count=3
```

The path exists. It does not exist FOR RUNED:

```
$ sudo ls -d /srv/data/arizuko_krons/groups/krons/content        # host: exists
$ docker exec arizuko_runed_krons ls -d /srv/data/arizuko_krons/…  # No such file
$ docker exec arizuko_runed_krons ls -d /srv/app/home/groups/krons/content  # exists
```

Every turn fails, on every folder, three retries each, and the fleet reports
healthy: runed answers `/health`, the substrate line says `backend=kvm`, and the
only symptom is a WARN per turn.

**`runed/mounts.go` `hp()` translates every mount source to the path the HOST
sees** — `HOST_DATA_DIR` over `ProjectRoot`, eleven call sites. That was right
for the Docker substrate and only for it: runed handed the path to the docker
DAEMON, a process on the host, which resolved it there. virtiofsd is not on the
host. runed starts it as its own child, inside its own mount namespace, so the
only path that can work is the one runed itself sees. The translation is now
exactly backwards.

Its doc comment says so, read after the fact: "maps a path as runed sees it to
the path the substrate's host sees, for the deployments where runed itself runs
in a container." The substrate's host WAS a different machine-eye-view; now it
is runed.

- **Severity:** high (no turn runs on the flipped instance; the fleet looks
  healthy while every turn fails)
- **Scope:** `runed/mounts.go`, `core/config.go`
- **Affected:** every mount of every turn under the VM substrate; `hp`,
  `Config.HostProjectRoot`, `HOST_DATA_DIR` as runed reads it
- **Source:** measured on krons 2026-09-25 (above) after the 6/41 step 4 flip;
  `runed/mounts.go:214-225`
- **Status:** open
- **Fix:** `hp()` goes, with its eleven call sites, so `Mount.Host` is the path
  runed sees. The renderer's test asserted the TRANSLATED path in eleven lines,
  which is why nothing caught this: it pinned the docker-sibling behaviour, and
  it now pins the substrate's. `HOST_DATA_DIR` stays a compose fact — compose
  writes bind sources the docker daemon resolves, and a package fragment
  interpolates it — but runed stops reading it. `Config.HostProjectRoot` keeps
  one reader, proxyd's audit path, which is a separate question.

## ✅ FIXED 2026-09-25 L367 — the QEMU substrate starts `virtiofsd` with a flag it does not have, so every VM turn would fail its share (2026-09-25)

`runed/qemu.go` `virtiofsdArgs` renders the clamp as `--translate-uid=` and
`--gid` likewise. The daemon the image installs takes neither. Measured
2026-09-25 against the exact package `Dockerfile` installs
(`alpine:3.20`, `apk add virtiofsd`, 1.10.1):

```
$ virtiofsd --help | grep -c translate
0
$ virtiofsd --socket-path=/tmp/x --shared-dir=/tmp --translate-uid=map:0:1000:1
error: unexpected argument '--translate-uid' found
```

The flags are `--uid-map` and `--gid-map`, and each takes
`:namespace_uid:host_uid:count:` — the help's own example is
`:0:100000:65536:`. The value the code carries, `map:0:1000:1`, is a second
spelling of the same guess.

**`6/41` §"The peer credential" already recorded the correction and the code did
not take it**: "The flags are `--uid-map` and `--gid-map`, not
`--translate-uid`. That name was a guess and it was wrong: 1.10.1's own `--help`
has no `translate` of any kind." So the spec, written 2026-09-24, and the
substrate that shipped the same day disagree, and the spec is the one that
measured.

**The precondition cannot catch it, and it looks like it can.** `checkClamp`
greps the daemon's `--help` for `clampFlag`, and `clampFlag` IS `--uid-map`
(`runed/qemuhost.go:74`). So the check passes on a binary that can clamp, and the
spawn then hands that same binary a flag it rejects. One constant names the flag
the check looks for; a literal in `virtiofsdArgs` names the flag the daemon gets.
Two declarations, and only one of them was corrected.

**The test pins the defect.** `TestVirtiofsdArgsClampOnlyTheTurnShare` asserts
`--translate-uid=map:0:1000:1`, so it derives its expectation from the code under
test rather than from the tool the code drives — green, and worth nothing. The
same class as [[test_mirrors_its_subject]].

The failure mode is not subtle: `virtiofsd` exits at once, the turn's share never
appears, and the guest cannot mount `turn`. It would have hit on the first turn
after the flip and on no test before it, because no test runs the real daemon.

- **Severity:** high (no VM turn can run; found on the flip's own path, before
  the flip)
- **Scope:** `runed/qemu.go`, `runed/qemuhost.go`, `runed/qemu_test.go`
- **Affected:** `virtiofsdArgs`, `checkClamp`, `RUNED_VIRTIOFSD_UID_MAP`,
  `RUNED_VIRTIOFSD_GID_MAP`, every turn under `RUNED_SUBSTRATE=qemu`
- **Source:** measured 2026-09-25 in `alpine:3.20` with the packaged 1.10.1;
  `specs/6/41-qemu-substrate.md` §"The peer credential"
- **Status:** ✅ FIXED 2026-09-25
- **Fix:** one declaration of each flag — `virtiofsdArgs` renders `clampFlag`
  and its gid twin rather than a literal, so the string the check requires IS the
  string the daemon gets. The map VALUE is the operator's, so its SHAPE is
  checked at start (`:N:M:C:`), which is where a wrong spelling belongs: the
  alternative is a daemon that exits on turn one. The test asserts the measured
  help text, and it fails if either flag goes back to a literal.

## ✅ FIXED 2026-09-25 L365 — `add_route` mints an id a restored rule already holds, and `L364`'s refusal then makes BOTH rules undeletable (2026-09-25)

`L364` fixed the READ: an ambiguous route id refuses instead of destroying an
arbitrary rule. It said the duplicates came from the marinade merge and that
`AddRoute` cannot produce one. **The second half of that sentence is false.**
Measured 2026-09-25 with a probe test on an empty store:

```go
s.PutRouteRow(core.Route{ID: 1, ...})   // what a restore states
s.PutRouteRow(core.Route{ID: 2, ...})
id, _ := s.AddRoute(core.Route{...})    // id == 1
```

`AddRoute` minted **1**, an id a rule already held. So the collision `L364`
blamed on an operator's item-level copy is reachable through the ordinary API,
and the rule that lands is unaddressable from the moment it is written: with
`L364` in place `routeByID` refuses the id, so `route rm`, `delete_route` and
`update_route` now refuse BOTH rules. The table can only be repaired by an
item-level write.

**Two mints, one counter, and only one of them reads it.** `store.putRoute`
mints from `store.Counters` (`CounterRouteID`), the compare-and-swap that makes
two concurrent adds one winner. The apply path mints differently:
`resreg/entity.go` `mintNumbers` numbers every unset id "one past the LARGEST
the set already states" and writes the items directly. Nothing in `resreg`
touches `Counters` — `grep -rn 'Counters' --include=*.go .` names only
`store/entities.go`, `store/scopes.go`, `store/routes.go`'s comment and the
objstore conformance suite. So every writer that STATES an id leaves the counter
where it was:

- `arizuko apply <manifest>` and `arizuko archive apply` — a restore states the
  ids it exported, and `arizuko packages install` applies its rows through the
  same path (`cmd/arizuko/README.md`).
- `store.PutRouteRow` with a non-zero `ID` (`routd.DB.AddRoute`, the
  dashd-playwright seed, `cmd/arizuko/main.go`'s setup rows).
- `ensureCounters` seeds a NEW counter at zero and leaves an existing one alone,
  so a restore onto a fresh instance starts the counter at 0 under a table whose
  ids run to N.

**Bumping the counter in each writer would not have stopped the incident.** The
marinade merge was an item-level copy and went through no writer at all. The
invariant that holds for both doors is on the MINT: a minted id is not one a
rule already holds.

- **Severity:** high (a restored instance's first `add_route` writes an
  unaddressable rule, and takes a second rule down with it)
- **Scope:** `store/routes.go`
- **Affected:** `putRoute`, `AddRoute`, `PutRouteRow`, `add_route`, every
  instance created by `arizuko apply` / `archive apply` / `packages install`
- **Source:** measured 2026-09-25 by a probe test on an empty store (above);
  `resreg/entity.go:518-552`; `store/entities.go:200-239`
- **Status:** ✅ FIXED 2026-09-25
- **Fix:** the mint skips an id the table already holds. `putRoute` reads the
  partition once and draws from `CounterRouteID` until the number is free, so
  the counter stays the arbiter between two concurrent adds and the table is
  what says which numbers are taken. The test states two ids a restore carries
  and asserts the next `AddRoute` collides with neither. It does NOT re-mint
  sloth's existing duplicates: that is live data and stays `L364` half two.

## ✅ FIXED 2026-09-25 L366 — three readers answer "which rule does this id name", and `L364`'s refusal reaches neither the agent's surface nor the operator's web UI (2026-09-25)

`L364` put the refusal in `store.routeByID`. Two of the three by-id readers do
not go through it, so the guarantee stops at `dashd` and the CLI.

**`routd.DB.GetRoute` is a second reader, and it first-matches.**
`routd/reads.go:86-97` reads `RouteTable()` and returns the first `r.ID == id` —
the exact loop `L364` removed from the store, re-implemented one package up. Its
own comment says why it exists: `store.GetRoute` collapses "absent" and
"unreadable" into one `false`. It is the reader behind `GET /v1/routes/{id}`
(`routd/routes_http.go:112`) and behind the agent's `delete_route` (
`routd/routes_resource.go:180`), where the route it returns is what `contain`
binds the caller's folder to. On sloth's six duplicated ids it authorizes
against one row while the store then refuses to delete either — and before
`L364` it authorized against `main/*` and deleted `atlas/*`.

**`store.GetRoute` drops the error, so the operator's own UI reports the
refusal as a 404.** `onbod/main.go:1467` is its only caller:
`route, ok := db.GetRoute(id)` → `http.Error(w, "route not found", 404)`. An
operator told "route not found" about a rule that is still firing goes and
creates a second one — the cost `dashstore.RouteTarget`'s comment already spells
out, which is why dashd reads through `RouteByID` instead.

**And the delete still reports the handle, not the row.**
`cmd/arizuko/route.go:117` prints `removed route %d` from the id the operator
typed. That sentence is what made `L364` survive the command that caused it:
`route rm 37` printed `removed route 37` while 37 was still in the table. The
row that went is in hand — `DeleteRoute` reads it before it deletes — and no
surface says it.

- **Severity:** high (the agent MCP surface resolves an ambiguous id to an
  arbitrary rule for its authz check; the operator UI reports a live rule as
  absent)
- **Scope:** `routd/reads.go`, `routd/routes_resource.go`,
  `routd/routes_http.go`, `store/routes.go`, `onbod/main.go`,
  `cmd/arizuko/route.go`
- **Affected:** `GET /v1/routes/{id}`, `delete_route`, `update_route`, onbod's
  `delete_route` form action, `arizuko route rm`
- **Source:** read 2026-09-25 while sweeping `L364`'s class;
  `routd/reads.go:86`, `onbod/main.go:1467`, `cmd/arizuko/route.go:117`
- **Status:** ✅ FIXED 2026-09-25
- **Fix — one reader, and it names what it did:**
  1. `routd.DB.GetRoute` delegates to `store.RouteByID` and maps `ok=false` to
     `ErrNoRoute`. The duplicate scan goes.
  2. An ambiguous id is a sentinel (`store.ErrRouteIDAmbiguous`) so a surface
     can answer **409** with the two coordinates. `store_error` on a 500 is not
     a loud refusal; it is the silence `L364` was filed against.
  3. `store.GetRoute` goes. `onbod` reads `RouteByID` and splits 404 from 500,
     the shape `dashstore.RouteTarget` already uses.
  4. `arizuko route rm` prints the coordinate it removed, so the operator can
     check the tool's word against the table.

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

## L364 — `route rm <id>` deletes the FIRST row carrying that id, and the merge left six ids duplicated on sloth (2026-09-25, open)

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

## ✅ FIXED 2026-09-24 L362 — one guest write into the share root keeps the VM record forever, and five of them exhaust the CID space (2026-09-24, open)

`reapVM` (`runed/vmrecord.go`) removes a turn's record only when NOTHING in the
reap reported an error, and the staging tree's removal is one of those reports.
`turnTree.teardown` ends `return t.remove()`, and `remove` treats **any non-empty
regular file it did not create** as foreign and returns `"tree kept"`. That error
joins `failed`, so `reapVM` returns `"record kept"` and the record file survives
— every reap and every disk-scan replay hits the same file and fails the same way.

The record is what marks a CID used. `liveCIDs` builds its set from the records on
disk and nothing else, and `allocateCID` searches only
`[3, 3+MAX_CONCURRENT_CONTAINERS)`. So a kept record costs a CID permanently, and
on a default instance **five** of them leave `allocateCID` with nothing to return:
`"no free guest CID: 5 live guests at MAX_CONCURRENT_CONTAINERS=5"` on every
spawn, with no live guest at all.

Measured by reading the chain 2026-09-24, each link confirmed in the source:
`vmtree.go` `remove` → `teardown`'s `return t.remove()` → `reapVM`'s `failed` →
the `os.Remove` of the record that never runs → `liveCIDs` → `allocateCID`.

**Step 0's own probe triggers it.** `guest-init`'s `mountBase` writes
`/turn/from-guest.txt` (17 bytes) to prove the uid clamp, and `/turn` IS the
staging tree root, so the acceptance test's own guest bricks the instance it ran
on. That is how the bug was found, but it is not the bug: a guest holds a WRITABLE
export by design, so any guest — buggy or hostile — can do the same on purpose.

The guard itself is right and must stay. A recursive delete over a live bind
deletes the group's own files, so refusing to remove a tree that holds something
unexpected is correct. What is wrong is the CONSEQUENCE: the record's job is to
name the processes a dead runed must reap, and every one of them was reaped.

- **Severity:** high (a reachable, permanent denial of every spawn on the
  instance; no runtime defect today because `RUNED_SUBSTRATE` defaults to
  `docker`)
- **Scope:** `runed/vmrecord.go`, `runed/vmtree.go`
- **Affected:** `reapVM`, `turnTree.remove`, `liveCIDs`, `allocateCID`,
  `kernel/guest-init/main.go` `mountBase`
- **Source:** the step-1 `guest-init` build, 2026-09-24 — found by the subagent
  writing the production half, which was told to keep the probe path and asked
  what the probe write lands on
- **Status:** fixed
- **Fix:** `reapVM` separates "every process is reaped" from "the tree is gone". A
  tree `remove` refuses is logged at ERROR with its path and the action a person
  takes; it no longer joins `failed`, so the record goes and the CID comes back.
  The tree is an operator action on live data, the same split `make rootfs-stale`
  takes. `TestReapReleasesTheCIDWhenOnlyTheTreeIsStuck` holds a 17-byte file — the
  size of the probe's own write — in the tree and asserts three things: the record
  is gone, `liveCIDs` no longer marks the CID, and **the guest's file is still
  there**. Reverting the one line fails it.

  The test then found a second half. `listVMRecords` called a dir with NO record
  file `broken`, the same as a CORRUPT one, so a released turn reported itself on
  every scan forever and would have buried the corrupt case that does need
  reading. A missing record is now skipped and an unparseable one still reports —
  `TestListVMRecordsReportsAnUnreadableDir` writes `{not json`, so the property
  the disk scan exists for is still guarded.

## ✅ FIXED 2026-09-24 L361 — the section-citation guard reads only citations that spell "spec", so 145 bare `§"…"` pointers are unchecked (2026-09-24, open)

`tests/specrefs` `TestEverySectionCitationResolves` matches
`spec[s]? <phase>/<id> § ?"<section>"` (`specrefs_test.go:206`). A spec citing a
section of ITSELF writes the short form the corpus actually prefers — a bare
`§"The host budget"`, with no number in front of it — and that form the regex
never sees. `specs/CLAUDE.md` describes the guard as reading "the quoted form",
which reads as covering both.

Measured 2026-09-24:

- **145** bare `§"…"` pointers exist across the documents. **One is dead**:
  `EXTENDING.md:452` cites `specs/5/13-ext-mcp.md` §"Connector declaration", and
  5/13 has no such heading — the shipped shape is §"Shape 2 — MCP HTTP connector
  (shipped)".
- The guard is green on a broken one. Rewriting one live citation in
  `specs/6/41-qemu-substrate.md` to `§"The host budgetZZ"` — a section no file
  has — left `go test ./tests/specrefs/ -count=1` reporting `ok`.
- `sourceFiles` already walks `.md`, so the file set is right and only the
  pattern is narrow. The numbered form is checked in the same loop and works:
  mutating the heading `runed/vmrecord.go` cites makes the guard name that file.

This is the shape of an unprovable gate, and a section heading is exactly what a
refine pass renames — `6/24`'s own §"Two parts" became §"What is left" in this
pass, and no test could have told me whether anything cited it. Renaming
`6/41` §"The host budget" takes **10** pointers down in silence.

**Two traps in writing the fix, both worth the lines.** The bare form does NOT
mean the containing file: §"Identity header trust" is `SECURITY.md`'s and
§"Split write-discipline" is the root `CLAUDE.md`'s, so a guard that resolves a
bare pointer against its own file reports 28 false deaths. And a first cut that
searched the raw document PASSED ITS OWN MUTATION, because a pointer is a quoted
copy of the heading and therefore matches itself — the pointers have to come out
of the haystack before the search.

- **Severity:** low (one dead pointer, in a doc; the guard cannot report the
  next one)
- **Scope:** `tests/specrefs`
- **Affected:** `tests/specrefs/specrefs_test.go:206` `sectionRe`,
  `specs/CLAUDE.md` §"Quote the section you cite"
- **Source:** the refine pass of 2026-09-24, proving the gate rather than
  trusting its green run (`feedback_gate_cannot_fail`)
- **Status:** fixed
- **Fix:** `TestEveryBareSectionCitationResolves` reads the bare form and
  resolves it against every `.md` and `.html` document with the pointers removed
  from the haystack first. `.html` is in the set because `5/9` cites an operator
  page's `<h2>`, and `unwrap` learned the `>` blockquote prefix, which two
  citations carried inside the quotes. It checks 145 pointers and fails on an
  empty set. Proven twice by mutation: one broken citation names its file, and
  renaming `6/41` §"The host budget" reports all 10 pointers to it.
  `EXTENDING.md`'s dead pointer now names §"Shape 2 — MCP HTTP connector
  (shipped)".

## L360 — one agent reference clone is copied per group and served from the public webroot (2026-09-24, open)

`elizaos` exists three times under `/srv/data/arizuko_krons`, at 5.4 G, 5.2 G
and 5.2 G: once in `groups/krons/workspace/agent-research-sources/`, and once
each under `web/pub/krons/agents/refs/` and `web/pub/rhias/agents/refs/`.
`openclaw` is duplicated the same way.

They are copies, not independent clones. The orphaned pack files of `L359`
carry identical names across them — `tmp_pack_78joXn` appears in both the
krons and the rhias tree, and a second clone of the same upstream would not
produce that name. So a whole `.git` directory was copied per group, garbage
included.

Two problems, and only the first is size. The clones live under `web/pub`,
which is the public webroot, so a 2.1 G pack file and the full history of a
third-party repository are served to anyone who asks. Nothing needs the
`.git` directory to be reachable for the checked-out files to be.

- **Severity:** medium — roughly 10 G recoverable, and a `.git` tree is public
- **Scope:** agent reference material, per-group layout
- **Affected:** every group that pins a reference repository; krons and rhias
  hold the two measured copies
- **Source:** `du` over `/srv/data/arizuko_krons`, 2026-09-24; identical
  `tmp_pack_*` names across the krons and rhias trees
- **Status:** open — whether references are shared or copied is a design
  decision, because sharing one clone couples the groups to one revision
- **Fix:** three shapes. Share one clone per upstream outside any group and
  give each group a checkout of the revision it pins, which is the smallest
  change and the one that keeps groups independent at the revision. Or keep
  per-group copies and clone with `--depth 1`, which loses the ability to pin
  an old revision. Either way the `.git` directory stops being served: the
  webroot needs the working tree, never the object store.

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
- **Status:** PARTLY FIXED 2026-09-25 — the 11 files are deleted, on the owner's
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

## L358 — the helm chart still deploys a shared crackbox proxy wired to `gated`, a daemon deleted at v0.50.0 (2026-09-23, open)

`deploy/helm/arizuko/templates/crackbox.yaml` renders a `Deployment` for a
`crackbox:latest` image and its own comment says what it is for: *"You must set
the `CRACKBOX_*` env on gated (via config/secrets) to make gated route through
it"* (lines 7-8). `gated` was deleted at v0.50.0, so the instruction names a
daemon that does not exist and the Deployment has nothing to serve.

Three more facts, measured 2026-09-23:

- No Makefile target builds a `crackbox:latest` image. `COMPONENTS` is `egress
  anteval`, so `make build` cannot produce it and `helm install` with
  `crackbox.enabled=true` pulls an image that is not there.
- The shared proxy this chart deploys is the shape `6/24` replaced with one
  forward proxy per spawn. `runed/cmd/runed/main.go:82` REFUSES TO START when
  `CRACKBOX_ADMIN_API` is set without `EGRESS_IMAGE`, so the compose path
  actively rejects what this chart configures.
- `activeProfiles` (`compose/compose.go:602`) never emits a `crackbox` profile,
  and `compose/compose_test.go:544` asserts no `crackbox:` service is written.
  The compose half is clean; only helm carries the relic.

The chart defaults to `enabled: false`, so nothing is broken today. It is a
documented deployment path that cannot work, which is worse than an absent one:
an operator reading `deploy/helm/arizuko/README.md:40` finds a supported
"egress-isolation proxy (advanced; parity only)" row.

- **Severity:** low (no runtime defect; `enabled: false` by default)
- **Scope:** kubernetes deploy artifacts
- **Affected:** `deploy/helm/arizuko/templates/crackbox.yaml`,
  `deploy/helm/arizuko/values.yaml:190-192`,
  `deploy/helm/arizuko/README.md:40`, `deploy/helm/arizuko/templates/NOTES.txt`
- **Source:** the crackbox removal sweep of 2026-09-23 (`L137`)
- **Status:** open. The fix is a deletion across four files, and whether the
  helm chart carries an egress story at all is a deploy decision, not a code
  one — k8s has no per-folder docker network for the per-spawn proxy to attach
  to, which the chart's own comment already says.
- **Fix:**

## L357 — `.golangci.yml` is a v1 config and `golangci-lint` is v2, so the one gate CI enforces cannot load it (2026-09-22, open)

`.golangci.yml`'s own header calls itself "the one gate CI enforces". It has no
`version:` key, which makes it a v1 config. golangci-lint **2.13.2** — what
`version: latest` resolves to today — refuses it outright:

```
Error: can't load config: unsupported version of the configuration: ""
```

`make lint` exits 3 on that, so the errcheck/nilerr half of the gate does not
run. `.github/workflows/ci.yml:39-45` installs the linter with
`golangci/golangci-lint-action@v6` and `version: latest`, precisely so "its
half of that gate is real in CI rather than a skipped notice".

**Measured locally 2026-09-22**, by installing the linter this box was missing
(`static_analysis_unusable` recorded its absence; `staticcheck` and
`govulncheck` still cannot read this toolchain, but golangci-lint 2.13.2 builds
against go1.27rc2 and runs).

**What it reports once it loads**, with the config migrated: 81 issues —
errcheck 47, staticcheck 20, nilerr 7, unused 4, ineffassign 3.

**CORRECTION 2026-09-22: I first wrote that this is v2's wider default set, and
it is not.** v1 and v2 enable essentially the same defaults; `gosimple` merged
into `staticcheck`, and that is the whole difference. Nor do the migrator's
exclusion presets account for it: running the same config WITHOUT them changes
errcheck from 47 to 50, not from 47 to 5.

So a working v1 gate would have reported roughly these 81 too. Either CI's lint
step has been failing, or these findings are newer than its last green run, or
`action@v6` resolves `latest` to a version where this config still loads and
the gate has been reporting them all along. **Which of those is true cannot be
checked from this box**, and it decides how urgent this entry is: a gate that
silently stopped failing is worse than one that cannot load, because the second
announces itself.

**The seven `nilerr` hits are the class this config exists for** and most are
sanctioned. `webd/mcp.go:193` returns an MCP tool-error inside the result,
which is the protocol convention. `compose/fragments.go:161` and
`container/runner.go:1265` carry comments saying absence is not an error.
`compose/compose.go:346` treats an unparseable URL as the operator's own
endpoint, which the comment above it states. `mcpfw/pkg/firewall/firewall.go:109`
passes a reply that is not a tools list through unfiltered, which is what a
transparent proxy does.

`container/runner.go:1218` is the one worth reading: `chownR`'s doc says it
records the first failure "rather than discarding it", and the WalkDir callback
returns nil on a per-entry error, so a directory it cannot read is discarded
while `Lchown` errors are kept.

**`golangci-lint migrate` still needs reading before it is kept.** On this
config it dropped the header explaining why the gate exists and dropped
`errcheck` from the explicit enable list — both worth restoring by hand. It
also ADDED four exclusion presets (`comments`, `common-false-positives`,
`legacy`, `std-error-handling`). I first read those as weakening the gate; they
are v1's own default exclusions made explicit, and measured they move errcheck
by three. So the presets are defensible and the dropped header is not.

- **Severity:** medium (the gate that guards the swallow class does not load;
  whether CI is red today depends on what `action@v6` resolves `latest` to,
  which cannot be checked from this box)
- **Scope:** `.golangci.yml`, `.github/workflows/ci.yml`
- **Affected:** every lint run
- **Source:** `golangci-lint --version` → 2.13.2; `make lint` → exit 3;
  `golangci-lint migrate` then `make lint` → 81 issues
**TWELVE OF THE 81 EXAMINED 2026-09-22, and three were real defects.** Each is
fixed on its own terms; the linter was run with `--no-config` so the repo's
config stayed untouched.

- `nilerr` → **`chownR` answered success over a tree it never touched**
  (`d5b18dce`). `F62` made its `Lchown` errors propagate; the WALK error was
  still discarded, so a directory runed cannot read skipped that whole subtree
  with no chown attempted and nothing reported — `F62`'s exact silence, one
  level above the fix that names it.
- `unused` → **`routd` declared a `cost:write` scope nothing reads**
  (`bffe31c7`). `4fee18cf` moved the write path to the gateway under
  `6/39-gateway-owns-spend`; the scope and its rationale stayed.
- `ineffassign` → **`runed/docker.go` bound `StartSpan`'s context and dropped
  it** (`ce5a7b93`). `runner.Run` takes none, so `container_spawn` parents
  nothing. Two others in the same run were NOT defects: one redundant `err =
  nil`, and one `src = nil` that is load-bearing — it drops the last reference
  so the GC below can take the corpus before the heap is read, so it took a
  `nolint` with that reason rather than a deletion.

**The one substantive `staticcheck` finding is a proposal, not a fix.**
`proxyd/main.go:282,287` use `httputil.ReverseProxy.Director`, deprecated since
Go 1.26 in favour of `Rewrite`. It is NOT a security hole: `stripClientHeaders`
is the first statement of `(s *server) route`, proxyd's single ingress, and it
deletes `X-User-Sub/Name/Groups/Sig` before anything else runs. But `Rewrite`
builds the outbound request differently — `SetXForwarded` is explicit there —
and proxyd is the sole `X-User-*` stamper, so changing it is changed control
flow on the security boundary. The other 19 are style (De Morgan, `Fprintf`,
merged declarations, capitalised error strings).

Unexamined: 47 `errcheck` and two unused test helpers.

**THE MIGRATION IS PREPARED AND TESTED**, so the decision is "apply this", not
"work out the migration". Exact file, verified in place:

```yaml
version: "2"
linters:
  enable: [errcheck, nilerr]
  settings:
    errcheck: {check-blank: false, check-type-assertions: false}
  exclusions:
    generated: lax
    presets: [comments, common-false-positives, legacy, std-error-handling]
    rules:
      - {linters: [errcheck], path: _test\.go}
    paths: [third_party$, builtin$, examples$]
```

plus the existing header, kept verbatim with two sentences added saying why
`version` and the presets are there. It reports **70 issues** in the tree
today: errcheck 44, nilerr 6, staticcheck 20.

70, not 81, and the gap is exactly the eleven this session fixed — 4 `unused`,
3 `ineffassign`, 3 `errcheck`, 1 `nilerr`. That is the check that the config is
equivalent to the migrator's rather than quietly looser.

The remaining 70 are the cost of taking this route, and none is a swallow: the
6 `nilerr` are the sanctioned ones read above, the 44 `errcheck` are Close /
Write / Remove, and the 20 `staticcheck` are style plus the `proxyd` `Director`
deprecation.

- **Status:** open — two ways out and they cost differently. Pin the action to
  a v1 release and the gate keeps working with no code change. Or migrate the
  config, restore the header and the explicit `errcheck`, drop the presets the
  migrator invented, and work through what v2's wider default set reports. The
  evidence now favours migrating: three genuine defects out of the twelve
  findings read. Choosing is yours; the v1 config is untouched.
- **Fix:** `d5b18dce`, `bffe31c7`, `ce5a7b93` are the defects it found. The
  config itself is undecided.

## L355 — a bare spec number that resolves can still name the wrong document, and two sit in the authorization path (2026-09-22, open)

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

## L351 — six `import.go` files, 1120 lines, zero production callers: the migration importers are now the test-seeding API (2026-09-22, open)

`L348` deleted the routd and hot-tier importers. `deadcode ./...` with every
main package as root then named `store/import.go`, and `Store.Import` turned
out to have exactly ONE reference — its own declaration. That half is gone
(`b6fa1938`, 169 lines: `Snapshot`, `Counts`, `PaneRow`, `UseRow`,
`importWrites`, `LegacyUseID`, `normalizeTS`).

**The onbod half that remains is the same class, and stops short for one
reason.** `gopls` gives `Store.ImportOnbod` seven references; six are tests and
the seventh is its declaration. So it is dead in production and load-bearing in
tests, exactly as `L348`'s region was.

It differs from `L348` in a way that decides the work:

| | `L348`'s region | this one |
| --- | --- | --- |
| production callers | none | none |
| used as a test SEED | 5 files | 3 — `cmd/arizuko/archive_onboarding_test.go:27`, `cmd/arizuko/archive_test.go:111`, `store/invite_redemptions_test.go:131` |
| tests OF the importer | none | 3 in `store/import_test.go` |
| covered by `SeedItem` | yes, `keyFields` already held the coordinates | **no** |

`SeedItem`'s `keyFields` carries routd's thirteen entities and the hot tier's
eleven. `onbodWrites` addresses four the table does not name: `Onboarding`,
`OnboardingGates`, `Invites`, `InviteRedemptions`. So the `L348` move does not
transfer for free — it costs four coordinate builders wired into `keyFields`
first, and then deleting three tests whose subject is the importer itself.

**AND IT IS NOT ONE FILE — IT IS SIX.** Every reference below is from
`gopls go_symbol_references`, not grep: six packages declare a symbol named
`Import` and a type named `Snapshot`, so a bare-name grep answers about the
wrong package. Measured 2026-09-22:

| importer | lines | references other than its declaration |
| --- | --- | --- |
| `store/import.go` `Store.ImportOnbod` | 149 | 6, all tests |
| `audit/import.go` `Import` | 128 | 8, all tests (6 packages seed audit rows through it) |
| `authd/authstore/import.go` `Store.Import` | 235 | 5, all tests |
| `runed/runstore/import.go` `Store.Import` | 293 | 3, all tests |
| `chanlib/chanstore/import.go` `Store.Import` | 176 | 2, all tests |
| `emaid/emaistore/import.go` `Store.Import` | 139 | 1, all tests |

1120 lines, and not one production caller among them. Each header describes a
two-half design — "the source half belongs to the tool that runs it" — where
the tool is gone: `773cb5c5` deleted the last `migrate-*` subcommand on
2026-09-13.

**They survive because they are the only writer that can place a row at a
chosen instant**, which is exactly what `L348` found for the routd pair. So
this is not six dead regions; it is one undeclared seeding API that every store
package grew independently, still wearing a migration tool's documentation.

**`deadcode` alone does not find this** and the reason is worth keeping: it
under-reports METHODS. It flagged `store.Store.Import` and missed
`authstore.Store.Import`, which gopls shows has only test callers. The census
above is gopls per symbol; `deadcode ./...` was only the way in.

- **Severity:** low (no runtime defect; 1120 lines with no production caller)
- **Scope:** the six `import.go` files above and the tests that seed through them
- **Affected:** nothing at runtime
- **Source:** `deadcode ./...`; `gopls go_symbol_references` on `Store.Import`
  and `Store.ImportOnbod` — grep is unreliable here, the package has
  same-named neighbours (`Snapshot`, `UseRow`, `importWrites` all exist in
  other packages)
**OPTION (b) IS SHIPPED 2026-09-22** (`a12413d5`, `6ee11aa2`). All six headers
now name the live writers they work around and say that nothing in production
calls them. A tree-wide sweep then found twelve more references to the deleted
tool, in eight files, and they are gone too — including two enumerations
(`invitePut`, `onboardingPut`) that listed the writers going through them. A
list of callers is what rots, so they state the invariant instead: there is ONE
renderer. Comments only, proven both by `go/printer` strip-and-diff and by
`git diff -U0`.

- **Status:** open on (a) alone. Either each store grows a `SeedItem` of its own
  and the importers go — the `L348` move, six times, each costing that store's
  coordinate builders — or they stay as the seeding API they now admit to
  being. Nothing is foreclosed either way.
- **Fix:**

## L350 — spec `6/37` cites six artifacts that no longer exist, four of them the guards it rests on (2026-09-22, open)

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

## ✅ FIXED 2026-09-22 L349 — `make integration` was red, four defects deep (2026-09-22)

`make integration` fails. `tests/TestE2ETurn_CostAccounting`'s subtest
"model failure moves the meter only by what it served" times out after 4
minutes waiting for the turn to close (`tests/e2e_turn_test.go:429`), so the
whole target exits non-zero after ~250s.

**NOT a regression from the 2026-09-21/22 campaign.** Reproduced identically at
`b0998e91`, the commit before it, in a detached worktree. It has been failing
for at least that long.

**Nothing would have caught it.** The `integration` target's own comment says
"CI runs: `make test && make integration`". CI runs `make lint`, `make test`
and `make test-e2e` — `.github/workflows/ci.yml` lines 45, 48, 65 — and never
`make integration`. So the target is documented as a gate, is not wired as one,
and is red. That is the `gate_cannot_fail` shape with the gate simply absent
rather than swallowing.

The subtest sets the folder cap to 0 and expects the turn to run: `capCents > 0
&& SpentCents >= capCents` (`routd/budget.go:87`) means 0 is unlimited, so the
budget gate is NOT the refusal.

**MEASURED 2026-09-22 with a 550s budget.** The turn DOES run. The model answers
400, ant reports `ok=false text="API Error: 400 fake says no"`, and the
container exits 1. routd reads the exit code as a failed turn, retries three
times, and ends with `run outcome error`. No bot row is ever written, so
`countBotRows >= 2` never holds and the wait times out.

So the defect is not the harness: **a model error the agent captured as its
result is dropped instead of delivered.** The test asserts the opposite — that
`API Error: 400` reaches the chat — which is the "fail loud, fail to the user"
rule. A container exit code is being read as a transport failure when the turn
in fact produced a reply.

**The target's own timeout hides which test failed, and it is the F37 signature
the Makefile warns about ten lines above.** `integration` runs
`go test ./tests/... -count=1 -timeout 120s`, and this test needs ~250s, so the
package is killed at the budget before the test can report. What an operator
sees is a bare `FAIL github.com/kronael/arizuko/tests 124.493s` with NO
`--- FAIL:` line — exactly the shape `GOFLAGS`' comment says BUGS F37 hunted for
six weeks. Reading the real assertion needs `-timeout 300s` by hand.

So there are two defects, and the second one hid the first: the turn does not
close, AND `integration`'s per-package budget is shorter than the test it runs.

`make test-turns` runs the same test with `-timeout 600s` and a vacuous-pass
guard, so it would show the real failure. It is not in CI either.

**The retry rule and a pinned test disagree, and that is the decision here
(measured 2026-09-22).** `ClaudeTurn` marks a failure `terminal` only when the
ACCOUNT was rejected (`claude.ts`, BUGS L175). A model `API Error: 400` is
therefore retryable, so routd spawns the container three more times to receive
the same 400, and only then delivers a notice.

Root `CLAUDE.md` says: "Retry ONLY transient errors — remote/network calls and
DB busy/locked. Everything else … throws immediately: no retry." A 4xx other
than 429 is the caller's own request — a bad model name, a malformed body, a
dead credential — and re-sending it cannot change the answer. By that rule the
current behaviour is wrong and costs three container spawns per occurrence.

`claude.test.ts` asserts `r.terminal` is UNDEFINED for exactly this input, so
the behaviour is pinned. **But `git blame` says the pin is incidental.** Commit
`55980815` (2026-09-20) INTRODUCED `terminal` and scoped it to `this.rejected`,
and its subject was "a model failure ends the run instead of answering as the
agent" — the bug was ant submitting `status:success` so routd delivered "API
Error: 400 …" as the reply. Whether a 400 should RETRY was never the question
being answered; retryable is the default it inherited, and the assertion marks
"not the rejected case" rather than "400 must retry".

I implemented the change — a `permanentAPIError` helper reading the SDK's
`API Error: <code>` line, 4xx-except-429 → terminal — and reverted it, because
it changes retry behaviour on a live path.

**My own classification was also too broad**, which is why this wants a decision
rather than a quick flip: 408 Request Timeout and 425 Too Early ARE transient
and belong with 429. The permanent set is 400/401/403/404/422 and their
neighbours, not "4xx except 429".

**And it would not be enough on its own.** A failure notice goes out through
`chanDeliverer.send`, which calls the channel directly and writes NO message
row — so the user sees the notice and `messages` has no record of it. The test
asserts a MESSAGE containing "API Error: 400", which needs both halves: the
cause named (terminal) and the notice recorded.

- **Severity:** medium (a documented gate that no one runs and that does not
  pass; the failing case covers cost accounting for a refused model call)
- **Scope:** `tests/e2e_turn_test.go`, `Makefile` `integration` (the 120s
  budget) and `test-turns`, `.github/workflows/ci.yml`
- **Affected:** nothing at runtime — the failure is in the harness or in what
  it asserts, and which is unmeasured
- **Source:** `make integration` at HEAD and at `b0998e91`; CI workflow
- **Status:** FIXED. `make integration` is green. Four defects, each hiding
  the next:

  1. **The target's budget was shorter than its test.** `-timeout 120s` against
     a ~250s test, so the package died before reporting — the bare
     `FAIL <pkg> 124s` F37 signature. Now 600s.
  2. **A refused request was retried.** `API Error: 400` was classified
     retryable, so routd spawned three more containers for the same refusal.
     400/401/403/404/405/410/422 are terminal now; 408, 425, 429 and every 5xx
     stay retryable. The assertion that pinned 400 as retryable was incidental
     — `git blame` shows `55980815` introduced `terminal` for the quota case
     and its subject was that ant answered as the agent.
  3. **The test waited for a bot row that never comes.** A notice is delivered
     and never appended, which the subtest above it already asserts. It waits
     on the delivery now.
  4. **`recordTurnResult` dropped `req.Error`.** The agent submits its own
     account — "API Error: 400 fake says no" — and routd showed the user the
     container's stderr tail: "ake says no", truncated mid-word with ant's log
     lines. `RecordTurnResult` stores it, `TurnFailure` reads it back, and the
     terminal notice prefers it. Same class as the drop that broke every reply
     for two days.

  `TestE2ETurn_CostAccounting`: 250s timeout → 17s green.

  **Still open and NOT part of this fix:** CI does not run `make integration`.
  The target's comment no longer claims it does. Wiring it is a cost call —
  ~250s and docker — and belongs to the owner.

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

## ✅ FIXED 2026-09-21 L335 — sloth serves marinade's Slack routes with no Slack adapter, credential or compose fragment (2026-09-21, open — operator data + deploy)

marinade's store was consolidated into sloth's ExtendDB around 2026-09-18:
sloth's own frozen SQLite (`store/routd/routd.db`, 2026-09-18 19:20) holds
**0** `slack:` messages and no `slack:user/U4PMARRT6` edge, marinade's
(2026-09-18 20:22) holds 1 160 and the edge, and sloth's LIVE store now holds
both — together with the `atlas` folders and the DM that answers the operator
today. marinade's service went down 2026-09-18T20:20:48 and has not come back.

The routes and the Slack operator grant came across. The adapter did not:

| carried into sloth | not carried |
| --- | --- |
| `atlas/support` ← `slack:T4PNSRSP7/channel/C0B4JMQ8X89 verb=mention` (seq 10) | `services/slakd.yml` — sloth's `docker-compose.yml:2-5` includes only discd, extenddb, teled |
| its `#observe` twin (seq 11) | `SLACK_BOT_TOKEN` / `SLACK_SIGNING_SECRET` — absent from `/srv/data/arizuko_sloth/.env`, present in marinade's (lines 43-44) |
| `slack:user/U4PMARRT6 -> role:operator` | any slakd process — `journalctl -u arizuko_sloth` since 2026-08-23: **0** occurrences of `slakd`; routd registers `telegram`, `discord`, `web` and nothing else |

So a Slack message cannot reach routd at all, and `/root` there fails before
any gate — it is not `L333` and not `L334`. `env/slakd.env` exists and is
regenerated on every `arizuko apply`, which is the `instance_fragments_freeze`
shape: `env/` regenerates, `services/` froze at first write, and the env file
alone looks like the adapter is configured.

The Slack path was never exercised after it could have worked: all three Slack
`/root` attempts (2026-05-18 ×2, 2026-07-16 13:03) predate
`slack:user/U4PMARRT6 -> role:operator`, written the same afternoon at
2026-07-16T15:33:40 by `cli:ondra-grant`. So the grant is in place and untested,
and now unreachable. Evidence table in `L334`.

**The merge also carried sloth's cursor for a partition that is now
marinade's.** Both instances had a `telegram:user/1112184352` chat — different
bots, the same person. sloth's own 19 rows end 2026-04-16; marinade's 3 007 end
2026-09-18T13:23:42. They are now one partition, and the `chats` row that came
across is sloth's:

```
sloth frozen store/routd/routd.db  chats[telegram:user/1112184352]
  agent_cursor = 2026-04-16T22:10:16.000000000Z | 3126
marinade frozen store/routd/routd.db  chats[telegram:user/1112184352]
  (no row)
```

Twenty minutes after the cutover sloth re-fed rows marinade had already
answered — `2026-09-18T20:40:11Z dispatch run turn_id=telegram:user/1112184352/5628`
replays a message from the previous afternoon. That replay is what delivered
the operator's `/root` to the agent as plain text (`L336`). The platform ids
also now overlap: marinade's history reaches `…/5639` while sloth's teled is
issuing `…/4179`, `…/4181` today, so the same id will be minted twice in one
partition — the shape `L105` already cost on `id=? OR platform_id=?` lookups.
Recorded, not fixed: reconciling a merged cursor is not an edit.

- **Severity:** medium (a whole ingress surface is advertised by the route
  table and cannot deliver; the operator reads it as `/root` being broken)
- **Scope:** `/srv/data/arizuko_sloth/.env`, `services/slakd.yml`, `routd.routes`
- **Status:** fixed, NOT applied. Deploy + operator data are the owner's.
- **Fix — one of two, and it is a decision:**
  1. **Restore Slack on sloth**: copy `SLACK_BOT_TOKEN` / `SLACK_SIGNING_SECRET`
     from `/srv/data/arizuko_marinade/.env` into `/srv/data/arizuko_sloth/.env`,
     then install the fragment — `arizuko packages sloth add slakd`, which links
     `template/services/slakd.yml` into `services/`, provisions its `env` block
     and re-applies. `readFragments` derives the `include:` list from that
     directory (`compose/compose.go:658`), so nothing else has to be edited.
     Then `sudo systemctl restart arizuko_sloth`. `slack:user/U4PMARRT6 ->
     role:operator` is already in place, so `/root` from Slack works on arrival.
  2. **Retire it**: delete the two Slack route rows (ids 17, 18) so the table
     stops advertising an ingress nothing serves. `L307` (marinade's two
     unrouted Slack channels) is moot either way — that instance is stopped.

**FIXED 2026-09-21: Slack has one home, and it is sloth.** All three gaps in
the table above are closed, verified on the live instance:

- `services/slakd.yml` installed from the catalog — NOT copied from marinade,
  whose frozen copy was an older generated form missing the `store/slakd`
  volume and the entrypoint.
- `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` copied into sloth's top-level
  `.env` (original backed up as `.env.bak-preslack`).
- `store/slakd/` created `mivu:mivu`, and sloth redeployed.

`arizuko_slakd_sloth` is `Up (healthy)`, `slack connected bot_user_id=…
team_id=T4PNSRSP7` — the same workspace the carried-over routes name — and
routd registered the `slack:` prefix.

**The move was only safe because marinade was stopped first** (`L338`). Two
instances holding one bot token contend for the same workspace connection, and
marinade still has the credentials in its own `.env`. Re-enabling it without
removing them re-opens that.

**It also activated `L307`.** Those routes were dormant while no adapter
served them; seven Slack chats on sloth carry 822 rows and match no route, so
messages there now reach a live adapter and are answered by nothing. That is
operator route data — see `L307`.

- **Status:** fixed
- **Fix:** operator deploy; no code change

## ✅ FIXED 2026-09-21 L336 — a slash command that is not the newest row of its batch is never steered, and `/root` then reaches the agent as plain text (2026-09-21)

`steer` inspects exactly ONE row per batch:

```go
steerIdx := newestUserIdx(msgs)          // routd/dispatch.go:43
…
if l.steer(chatJID, steerMsg, folder) {  // routd/dispatch.go:92
```

`newestUserIdx` (`routd/dispatch.go:202`) returns the newest **non-bot** row by
position; it asks nothing about content. So when anything non-bot lands after a
command before the next poll — a second human message, a `timed`/`sched-task`
cron row, another person in a group chat — the command is not the row that gets
steered. It is not dropped either: it stays in `trigger`, and the agent receives
it verbatim.

Measured, two rows in one batch (`/root mint the token`, then `and also look at
this`, operator granted):

```
runs=1  turn=m2  elevated=false  batch-has-/root=true
ack[0] "⚠️ I couldn't produce a reply to that — please re-send."
```

One unelevated turn, the literal `/root …` inside its message batch, no
elevation, no denial, no re-injection.

**This is live.** sloth dispatched the operator's 2026-09-17 `/root give this
folder the capability to approve pull requests` as an ordinary turn 31 hours
later:

```
2026-09-18T20:40:11Z  dispatch run  folder=atlas  turn_id=telegram:user/1112184352/5628
                      chat_jid=telegram:user/1112184352  elevated=false
```

`turn_id` is the tail of a TRIGGER group (`bl.ID` in `runTurn`'s caller), so
that row was the thing the turn was run for, not ambient context — and the
container transcript carries it verbatim:

```
groups/atlas/.claude/projects/-home-node/c1b0a600-97d2-4132-b6f5-33b2ad45f2c6.jsonl
  prompt ts 2026-09-18T20:40:16.914Z
  <messages> … <message id="telegram:user/1112184352/5628" … >/root give this
  folder the capability to approve pull requests</message> </messages>
```

The agent then answered the operator in the chat — *"`$ARIZUKO_IS_ROOT` is empty
— the `/root` grant didn't land. It reached me as plain text (not intercepted as
an operator command)"* — which is correct for THIS attempt and wrong for the
2026-09-21 one, where the steer did fire and refused it (`L333`). Two different
failures of one command, four days apart, and the owner read them as one.

Honest gap: I did not reconstruct which row WAS the newest in that batch — the
chat had just been handed from marinade to sloth on a stale cursor (`L335`),
and routd logs no steer decision. The code property above is what makes any
such batch behave this way, and the probe reproduces it in isolation.

**It is the same bug twice already.** `BUGS L56` moved the steer from "newest
row" to "newest USER row" after a bot reply landing first swallowed a command;
`caf38bfb` moved steering into `processGroupMessages` after a Slack `/root mint`
raced the queue (marinade atlas, 2026-07-16). Both fixed WHICH single row is
steered. Neither made it more than one.

- **Severity:** high (the operator's only escalation path, lost silently to
  ordinary chat timing; a `/root` reaching a model as plain text is also the
  agent being handed an instruction the platform never authorised)
- **Scope:** `routd/dispatch.go` `processGroupMessages` — the steer/consume/advance contract
- **Status:** proposal, NOT shipped — needs sign-off (root `CLAUDE.md`,
  "Redesigns need sign-off")
- **Why it is a redesign, not an edit:** steering every command in a batch
  changes what the cursor may advance over and what survives into the turn. The
  function already carries three recorded scars on exactly that contract — "A
  STEER CONSUMES ITS OWN ROW AND NO OTHER" (`L104`), the `steerConsumed`
  re-feed guarantee, and the busy-backoff key. Two commands in one batch, a
  command with ordinary rows on both sides, and a `/root` whose re-injection
  must land AHEAD of the cursor (`L102`) each need an answer before it ships.
- **Shape to sign off on:** walk the batch once, oldest→newest, steering every
  row a handler claims and keeping the rest in `trigger`; the cursor advances to
  the newest steered row only when nothing unsteered is older than it, otherwise
  the turn takes the remainder as it does today. Alternative, smaller and worse:
  steer only the newest row as now, but REFUSE to dispatch a batch containing an
  unsteered leading `/`-command and tell the chat to re-send it — loud, but it
  makes ordinary chat timing reject the operator's message.

**FIXED 2026-09-21, and it is NOT the redesign this entry assumed.** The
"consume/advance contract" is what made it look like one, and the contract is
what makes the fix safe — it turns entirely on WHICH command you pick.

`steerRowIdx` (`routd/dispatch.go`) offers `steer` the **oldest** slash command
in the batch, falling back to the newest non-bot row when there is none.
`advance` sets the cursor to the steered row's message key, so rows BEHIND it
still get the turn they are owed and rows AFTER it stay above the cursor for
the next poll. Nothing is skipped in either direction. Picking the NEWEST
command is the variant that breaks it — that advances past an older command and
loses it, which is `L104` seen from the other side.

The second thing that made it look hard was that detection and execution were
fused. `lookupCommand` is pure, so `IsSteerCommand` (`routd/steer.go`) now
answers "is this row a command?" without running it, and the eleven-command set
is declared ONCE in `steerCommands` with `handleCommand`'s switch guarding on
it — a case added there without a key stops being a command rather than
silently becoming unfindable.

- **Status:** fixed
- **Fix:** tests `TestSteerRowIdx_FindsACommandThatIsNotTheNewestRow`,
  `TestSteerRowIdx_TakesTheOldestCommandOfTwo`,
  `TestSteerRowIdx_NoCommandKeepsTheNewestUserRow` and
  `TestIsSteerCommand_RefusesBotAndCronRows`. The third is the regression that
  matters — without it a change here re-routes every ordinary turn. Mutation:
  collapsing `steerRowIdx` to `newestUserIdx` fails the first two.
- **Known gap:** these pin the FUNCTION. No test drives dispatch's call site,
  so reverting `steerIdx := steerRowIdx(msgs)` alone is caught by nothing —
  found because that exact mutation passed. Closing it needs the loop harness.

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

## ✅ FIXED 2026-09-21 L337 — sloth's teled has no state directory, so the next inbound Telegram message kills it (2026-09-21, open — found while diagnosing `L333`)

`teled` writes its Telegram poll offset to `STATE_FILE`, and the directory is
gone on sloth:

```
$ sudo ls /srv/data/arizuko_sloth/store/
authd  extenddb  extenddb_db  messages.db  onbod  routd  routd.db  runed  timed.db
$ sudo ls /srv/data/arizuko_krons/store/
authd  extenddb  extenddb_db  messages.db  onbod  routd  runed  teled  teled-rhias  whapd  …
```

krons has `store/teled`; sloth does not, and nothing in the code creates it —
`saveOffset` opens `StateFile + ".tmp"` directly (`teled/bot.go:110`). So every
save fails:

```
2026-09-21T06:50:34Z WARN offset save failed  err="open /srv/app/home/store/teled/teled-offset-telegram.tmp: no such file or directory" consecutive=1
2026-09-21T06:51:55Z WARN offset save failed  …                                                                                    consecutive=2
```

`saveOffset` fires once per received update batch and the streak only resets on
a success, so **the third inbound Telegram message exits the process**
(`maxSaveFailStreak = 3`, `teled/bot.go:92,115` — `fatalExit(1)`, deliberately,
"so the mount problem is visible"). The counter stands at 2 right now. The
escalation is working as designed; what is broken is the directory under it.

On restart the offset file is still absent, so teled resumes from no offset and
Telegram re-delivers its backlog — duplicate inbound on the operator's own DM,
which is the chat `L333` is about.

The two failures are the operator's own two messages of 2026-09-21 06:50/06:51.
There has been no other inbound Telegram traffic since the container started
(2026-09-20T07:36:48Z, `RestartCount=0`), which is the only reason it is still
up.

- **Severity:** high (armed; the next Telegram message to sloth takes the
  adapter down, and the restart re-ingests the backlog)
- **Scope:** `/srv/data/arizuko_sloth/store/teled/` (missing), `teled/bot.go`
- **Source:** `journalctl -u arizuko_sloth | grep 'offset save failed'` —
  2 hits, both 2026-09-21; `sudo ls /srv/data/arizuko_sloth/store/`
- **Status:** fixed, NOT applied — a live instance's filesystem is the owner's
- **Fix, two halves:**
  1. **Now, on the box:** `sudo -u mivu mkdir -p /srv/data/arizuko_sloth/store/teled`
     (owner `mivu`, matching the sibling `store/routd`). No restart needed —
     the next save succeeds and resets the streak.
  2. **So it cannot recur:** `saveOffset` should `os.MkdirAll(filepath.Dir(
     b.cfg.StateFile), 0o755)` before the atomic write, or the daemon should
     make its state dir at startup and fail loudly there instead of three
     messages later. A daemon that names a path it will not create is relying
     on an installer step; sloth's `store/` was reorganised for ExtendDB on
     2026-09-18 and the directory did not come back. Not shipped here: it is a
     different concern from `L333`, and it wants its own test.

**FIXED 2026-09-21.** `/srv/data/arizuko_sloth/store/teled` created
`mivu:mivu 755`, matching krons. The offset save has somewhere to land, so the
counter stops climbing toward `maxSaveFailStreak` and the next inbound message
no longer takes the adapter down. `arizuko_teled_sloth` is `Up (healthy)`.

This was armed, not theoretical: the streak was at 2 of 3 when it was found.

- **Status:** fixed (operator data; no code change)
- **Fix:** the directory

## ✅ FIXED 2026-09-21 L331 — the `/onboard/` route row points at a 404 (2026-09-21, open)

`compose.coreProxydRoutes` declares both spellings
(`compose/compose.go:537-538`):

```go
{Path: "/onboard",  Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
{Path: "/onboard/", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
```

onbod's mux registers only the exact path — `GET /onboard` and `POST /onboard`
(`onbod/main.go:189,192`). Go's `ServeMux` treats a pattern WITHOUT a trailing
slash as an exact match, so it does not serve `/onboard/`. proxyd forwards the
request faithfully and onbod answers 404.

So a user who types the trailing slash, or follows a link that normalises to
it, gets a 404 on the one page whose whole job is to onboard them. The route
row makes it look handled.

Found while fixing `L99`; it is a separate concern, so it is a separate entry.

- **Severity:** low (one spelling of one path; the bare form works)
- **Scope:** the core route table vs onbod's mux
- **Affected:** every instance with `ONBOARDING_ENABLED`
- **Source:** `compose/compose.go:537-538`; `onbod/main.go:189,192`; Go
  `net/http.ServeMux` exact-match semantics for a pattern with no trailing
  slash
- **Status:** fixed
- **Fix:** decide which spelling is canonical and make ONE of them true —
  either drop the `/onboard/` row, or register the subtree in onbod. Do not do
  both: two live spellings of one page is the drift `L45` already paid for.
  Whichever way, a test should assert the declared route table and the mux
  agree, because nothing checks that today — which is how this survived.


**FIXED 2026-09-21, and this entry had the mechanism wrong.** It read the two
rows as two SPELLINGS of one page. They are not: `proxyd.MatchRoute` treats a
trailing-slash path as a PREFIX and a bare path as exact
(`proxyd/routes.go:38`), so `/onboard/` was a SUBTREE route — and onbod serves
no path under `/onboard`, only the exact `GET` and `POST`
(`onbod/main.go:189,192`).

So the row forwarded every `/onboard/...` to a 404 while the route table
advertised the subtree as served. Dropped it; the bare row stays and is the one
spelling every link uses (`onbod/main.go:774,786`).

That also answers the entry's "decide which spelling is canonical": there was
never a second spelling to choose, only a subtree nothing served.

- **Status:** fixed
- **Fix:** the row is gone and `TestNoOnboardSubtreeRouteWithoutASubtreeHandler`
  pins BOTH halves — the exact row must stay, the subtree row must not return.
  The entry asked for a test that the route table and the mux agree; this is
  that test for this pair.

## ✅ FIXED 2026-09-21 L330 — 160 files are gofmt-dirty and nothing gates formatting (2026-09-20)

`gofmt -l .` (excluding `third_party/` and the untracked `.migrate*` copies)
names **160 files**. Nothing catches it: `make lint` is `go vet ./...`, which
does not check formatting, and `.pre-commit-config.yaml` runs
trailing-whitespace, end-of-file, yaml, json, merge-conflict, private-key,
prettier and two typecheck hooks — no `gofmt`, no `gofumpt`, no
`golangci-lint`.

So the drift is silent and one-way: every edit to an already-dirty file keeps
it dirty, and a clean file goes dirty the first time a signature change
misaligns a struct literal. Two of the 160 were made dirty by this session's
own `L166` commit and were only noticed because a subagent happened to run
`gofmt -l`.

The cost is not aesthetic. A dirty file makes every later diff noisier than the
change it carries, and it hides a real reformat inside an unrelated commit.

- **Severity:** low (nothing breaks; every diff is dirtier than it needs to be)
- **Scope:** the lint gate
- **Affected:** 160 files across `container/`, `emaid/`, `ipc/` and others
- **Source:** `gofmt -l .` 2026-09-20; `Makefile:36` `lint:`;
  `.pre-commit-config.yaml` hook list
- **Status:** fixed
- **Fix:** add the gate FIRST, then sweep — a sweep without a gate drifts
  straight back. `golangci-lint` is already the `lint-strict` dependency
  (`Makefile:45`), and its `gofmt` linter is the gate that costs no new tool.
  The sweep is then one mechanical commit, kept separate from any behaviour
  change so neither hides in the other.


**The count in this entry's own title is WRONG, and I wrote it.** 160 came from
`gofmt -l .` without excluding `refs/`, an UNTRACKED vendored checkout, and
`third_party/`. Measured against tracked files only, the real number was
**three**: `container/env_contract_test.go`, `container/output_style_test.go`,
`emaid/imap.go`. The gate was missing; the damage was not widespread.

**FIXED 2026-09-21, and the lint surface is now ONE target.** `make lint` runs
gofmt, then `go vet ./...`, then golangci-lint, failing cheapest first. CI runs
exactly that target and installs golangci-lint, so its half of the gate is real
there rather than a skipped notice.

`lint-strict` is gone. It existed because golangci-lint could not build against
this repo's toolchain in 2026-07; keeping a second target meant the
errcheck/nilerr gate CI never ran. It now runs inside `lint`, and is skipped
with a printed notice when the binary is absent — so a machine without it still
gets gofmt and vet instead of a blocked commit.

The per-daemon `make -C <d> lint` loop went with it: every one of those twenty
targets is `go vet ./...`, and this is a SINGLE-module repo, so the top-level
vet already covered them. Twenty invocations of a subset of work already done.

- **Status:** fixed
- **Fix:** the three files formatted; `Makefile` `lint` consolidated;
  `.github/workflows/ci.yml` installs golangci-lint. Gate proven by mutation —
  appending badly-formatted Go to `emaid/imap.go` makes `make lint` exit 1 and
  name the file.

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

## ✅ FIXED 2026-09-20 L309 — the docs mount is gated on `web:publish`, so 53 of 59 folders ship a skill that names a path they do not have (2026-09-19, FIXED 2026-09-20)

MEASURED on krons 2026-09-19, re-measured 2026-09-20. Every agent carries the
`arizuko` skill, whose whole job is to answer "how does arizuko do X" from
`/var/lib/www/arizuko/`. `container/runner.go` mounted that tree only
`if in.WebPublish`, and exactly **6 folders hold `web:publish`** (`fiu`,
`adshaus`, `eval`, `happy`, `krons`, `rhias/nemo`) against **59 groups**. For
the other 53 the skill named a directory that is not there, and the agent has
no other copy: the image stages only `ant/` + `template/services` +
`CHANGELOG.md` under `/opt/arizuko` (`Dockerfile:40-48`), never
`template/web/`.

The gate was not deliberate: the mount was added so a publishing folder could
see what it published, and reading the docs is a second purpose that inherited
the condition.

**The entry's original safety argument was REFUTED, and the fix it prescribed
was not shipped.** It argued that `/var/lib/www` is `<web-dir>/pub`, which
proxyd serves publicly at `/pub/`, so an ungated read-only whole-tree mount
"discloses nothing a browser cannot fetch anonymously". That is false.
`proxyd/main.go:518` takes the longest-prefix `web_routes` match and switches
on `access` — `public|auth|deny|redirect` (`resreg/resources/web_routes.go:41`)
— and `routd/web_routes_resource.go:87` accepts `auth` and `deny` on any prefix
the caller's own slot covers. A folder CAN therefore gate part of its own
`/pub/` subtree at the edge, and a filesystem mount bypasses that gate. Dropping
`in.WebPublish` from the whole-tree mount would have handed all 59 folders a
read path around every such row. On krons today the only `auth` row is
`/priv/eval/eti0hsk-priv-401.html`, under `<web>/priv/` — a different host
directory that is not in the mount — so this was mechanism risk, not a live
leak, which is exactly why it would have shipped unnoticed.

**Shipped instead: split by subtree, not by dropping the grant.** A folder with
`web:publish` keeps the whole-tree RO mount unchanged. A folder without it gets
`<web>/pub/arizuko` → `/var/lib/www/arizuko` read-only and nothing else — no
whole-tree view, no writable slot. `<web>/pub/arizuko/` is not a group's
content: it is the platform doc site `arizuko create` copies from
`template/web/pub/arizuko/`, and it is what the skill every folder ships reads.

- **Severity:** medium — the documentation an agent is told to consult was absent for 90% of folders
- **Scope:** `container/runner.go` `buildMounts`; spec `specs/5/V-web-vhosts.md`
- **Affected:** every folder without `web:publish`, every instance
- **Source:** `routd.acl` holds 6 `web:publish` rows against 59 `routd.groups` rows (krons, 2026-09-20)
- **Status:** fixed — `container/runner.go` splits the mount by subtree; `TestBuildMounts_DocsSubtreeWithoutWebPublish` pins both directions (docs mounted RO without the grant; no whole-tree mount and no writable slot without it)
- **Residual:** the doc subtree itself is still a filesystem read around any
  future `auth`/`deny` row over `/pub/arizuko/...`. Closing that would need a
  longest-prefix route matcher in routd — proxyd owns the only one
  (`proxyd.matchWebRoute`), so a second copy is the drift this repo bans. Not
  worth it for a subtree whose only purpose is to be read by every agent, but
  it is the reason `arizuko` should never become a group folder name.

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

**The gap is written into the threat model 2026-09-22.** `SECURITY.md` was
honest about WHERE the boundary is — "the mount set and the network namespace"
— and said nothing about the turn holding docker's default capability set on a
shared kernel. A reader of the threat model is exactly who needs that, so the
section now states it and points here. No behaviour changed and no decision is
pre-empted; the claim just stopped being stronger than the code.

- **Severity:** high — the isolation boundary is weaker than the threat model states
- **Scope:** `container/runner.go`, `compose/compose.go`
- **Affected:** krons, marinade, sloth — every turn on every instance
- **Status:** open — the flag set is a new contract for every spawn, so it needs
  sign-off before it ships
- **Fix:** the turn spawn declares the same restrictions the sidecar already
  declares, from ONE place both read, so a new spawn site cannot be born
  unhardened. `--runtime=runsc` is the separate, larger question: it is one
  `daemon.json` line and changes no arizuko mechanism.

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

## ✅ CLOSED-STALE 2026-09-22 L254 — "the point of no return arrives once per daemon" is false for anything that reaches through `store` (2026-09-07)

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
**CLOSED-STALE 2026-09-22: the port this entry asked to RE-ORDER has finished.**
The measurement was sound and the question is gone, because the work it
sequenced is done. Measured at HEAD:

- `database/sql` is imported by exactly ONE file in the tree, `audit/audit_log_test.go`.
- `db_utils/` does not exist, so the storage-layer rule and its guard
  (`sql_layer_test.go`, `sqlLayers`) — steps 1 of the order — have no referent.
- `store/migrations/` does not exist, so step 2's frozen legacy schema is gone.
- No SQL runs in `store`. The four remaining `SELECT`/`sql.` hits in the package
  were COMMENTS, two of them describing the `*sql.DB` era as current; corrected
  in the same pass.

Whatever order was actually taken, eighteen consumers reached no-SQL and the
fleet is green on it (`dynamo_transition`, v0.68.0). Nothing is left to sign
off. `specs/6/37` still says `status: partial`, which is a separate question
about that spec's own bookkeeping, not about this decision.

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

## ✅ FIXED 2026-09-21 L163 — an adapter restart empties its in-memory `/files/<id>` cache, so attachments re-fed across the restart 404 and the agent gets the text only (2026-09-05, FIXED 2026-09-21 — the mapping is a `file_urls` entity in the adapter's own store, so a restart and an eviction both resolve)

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
- **Status:** fixed — proposal, needs sign-off. Three shapes, cheapest last:
  (1) persist the id→url map in the adapter's own SQLite (the emaid precedent);
  (2) enrich delivers a visible placeholder so the loss is not silent
  (`routd/enrich.go:110` currently WARNs and `continue`s — the agent is told
  nothing); (3) make the id a signed capability token that CARRIES the URL, so
  the proxy holds no state and neither a restart nor an eviction can lose one.
  (3) is the only one that makes the bad state impossible by construction, but
  it changes the `/files/<id>` contract for every adapter on `FileProxyHandler`.
- **Fix:**

- **The silence is fixed 2026-09-20 — option (2), which the standing rules
  already required.** "An error on a user-facing path MUST surface to the user,
  not just be logged" (`~/.claude/CLAUDE.md`), and a dropped attachment wrote
  NOTHING into the turn: the agent ran on a message naming a file it never
  received, and neither it nor the user could see anything was missing.

  `routd/enrich.go` now renders `<attachment filename=… mime=… unavailable=…/>`
  at every failure site — all FOUR of them, not just the download: no file
  reference at all (teled with `LISTEN_URL` unset), a base64 body that does not
  decode, a write that fails, and the fetch that fails. One renderer beside the
  one that renders the success case, because two shapes for "here is what the
  sender attached" is how the two drift.

  The notice carries NO `path=`, deliberately: offering a path to a file that is
  not there trades a silent loss for a failed read.

  PROVEN BY MUTATION: silence the download failure again, or let the notice
  carry a path — each turns a test red. A third test pins the success path, so
  the fix cannot be satisfied by marking everything unavailable.
- **The cause is fixed 2026-09-21 — option (1), persist the mapping.** The
  adapter rewrites an attachment's URL to `/files/<id>` before the envelope
  reaches routd, so the platform URL is held nowhere else: the envelope carries
  the proxy URL and so does the message routd stores. The map was therefore a
  state of record living in memory, which `durable_local_db` forbids.

  `chanstore` gains a third entity, `file_urls` (`chanlib/chanstore/file_url.go`),
  keyed partition=sort=id for `outbound_dedup`'s reason: both reads address one
  id handed to them by an HTTP path and neither filters. chanlib SHIPS the
  entity; the TABLE belongs to each adapter daemon, so no library owns a
  database.

  `chanlib.URLCache` IS KEPT and given that backing rather than replaced — five
  packages name the type. The LRU is now a cache in front of the record: `Put`
  writes through, `Get` falls through to the store on a miss and warms memory.
  That is one mechanism for both halves of the loss, because a restart and an
  eviction are the same thing to the turn that needed the file.

  `FileProxyOpts.Resolve` gains ctx and an error, so "the store would not
  answer" reports 502 instead of the 404 that would tell enrich the sender
  attached nothing. `s.files.Get` stays a method value at all three sites.

  NOTHING SWEEPS `file_urls`, deliberately: an expiry is a deliberate loss of
  the mapping, which is this bug. The cost is one item per inbound attachment,
  strictly fewer than `outbound_dedup`'s one per outbound delivery, which this
  entity set already accepts unswept.

  PROVEN BY MUTATION: stop `Get` falling through to the store, or stop `Put`
  recording, and `TestURLCacheResolvesAfterARestart` — a SECOND cache built over
  the same store, which is what a restart is — goes red along with the eviction
  and warm-up bodies, while the memory-tier bodies stay green. Dropping
  `FileURL` from `chanstore.Entities` reddens all five `file_url_test.go`
  bodies, since an undeclared entity is a table no deployment creates.

  REFUTED while fixing: whapd does not reference `URLCache` at all — the
  referencers are slakd, discd and reditd (plus the type's own tests). teled
  stays unaffected: `teled/server.go` rebuilds the URL from the request path and
  holds no cache.

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

## 📋 RECORD L147 — principle: the channel attests, the router resolves, the ACL authorizes — and `web:` JIDs exist only to satisfy a storage column (2026-08-30, principle — records a design fault, no work item)

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

## ✅ FIXED 2026-09-21 L99 — `/invite/` and `/onboard` are `public` routes whose handlers key on identity, so onboarding is an infinite redirect (2026-08-26, FIXED 2026-09-21)

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

- **RE-MEASURED 2026-09-20 at `6cef0e48`: CONFIRMED LIVE, unchanged.** All
  three routes still declare `Auth: "public"` — `compose/compose.go:531,532,533`.
  The line numbers moved; the declaration did not. Still correctly a proposal:
  the fix changes either the auth on three routes or the identity model of the
  handlers behind them.

- **REPRODUCED then FIXED 2026-09-21.** `onbod/onboarding_gate_test.go` stands
  onbod's real mux behind the gate its routes really declare, read from
  `compose.CoreRouteAuth`, and walks it with a cookie jar and a login that
  returns at once. Before the fix `/invite/<tok>` ended in
  `stopped after 10 redirects` — the browser hop limit, from code, not a curl.

  Both halves moved, because either one alone leaves the loop reachable:

  - the three rows are `Auth: "user"`, so proxyd's `requireAuth` trades the
    session cookie for an identity. `optional` was checked and rejected for the
    reason the entry gives: its arm only calls `tryAuth`, which reads a Bearer.
  - onbod answers **401** where it bounced. `handleOnboardPost` already did;
    the two GETs now match it. proxyd's `requireAuth` is the only writer of the
    `auth_return` cookie (spec `5/31`), and onbod's second one disagreed with
    it on lifetime — 86400 against 600, which is what the live capture caught.
    With the bounce gone the loop cannot re-form when the transit proof stops
    verifying and `stripUnsignedGuard` strips a signed-in caller's sub.

  **Two details of the entry are wrong.** `/onboard` does not loop: onbod wrote
  no `auth_return` there, so authd's `consumeReturn` had nothing and dropped the
  caller on `/`. Same outcome, different mechanism — only `/invite/` loops.
  And the `userSub == ""` branches do not become dead code: `stripUnsignedGuard`
  still strips an unprovable sub, which is why they answer rather than vanish.

  Unverified as before: no live instance was driven. `arizuko generate` on each
  instance is still required for the route change to reach proxyd.

## ✅ FIXED 2026-09-21 L106 — the agent's `reply`/`send` sends one idempotency key and the retry sweep sends a different one, so the whole dedup contract is off for every MCP message (2026-08-26, FIXED 2026-09-21; the "crosses the ipc↔routd contract" scope claim is REFUTED)

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

- **RE-MEASURED 2026-09-20 at `6cef0e48`: CONFIRMED LIVE, and it is TWO sites
  not one.** `routd/mcp.go:264` still delivers with `"mcp-"+randHex(8)`, and
  `:285` does the same for `Document`. A throwaway key on each, so the retry
  sweep's own key can never match either. Still correctly a proposal: the fix
  crosses the ipc↔routd contract.

- **FIXED 2026-09-21 at `f13f3428`, and the scope claim it carried for 23 days
  is WRONG.** The 2026-08-29 correction called this a shared-contract change
  needing sign-off. `GatedFns.SendMessage`/`SendReply`/`SendDocument` are plain
  Go func fields in one module with ONE production assignment each
  (`routd/mcp.go` `buildGatedFns`) and ONE production call site each
  (`ipc/ipc.go`). Widening them is compile-checked, not a wire change: the
  adapter's `idempotency_key` field, `chanlib.deliverOnce` and every adapter
  are untouched. Same shape as `L166`'s false contract claim.
- **The direction the correction proposed was also the wrong one.** It offered
  "the id crosses as a parameter OR as a return". The Deliverer's own doc says
  the key IS *"the bot row's stable message_id"*, and ipc writes that row — so
  ipc owns the mint and routd is the reader. `mcpDeliver`/`mcpAppendDoc` take
  the id and forward it; both `"mcp-"+randHex(8)` sites are gone. Returning it
  from routd would have kept two mints, because a nil Deliverer still persists
  a row and would need its own fallback.
- **Shipped:** one `core.MsgID("mcp")` per outbound attempt, minted in the
  `reply` handler and in `internalSend` (one per file), handed to the send AND
  to `putOutboundRow`. A failed send now records the row under the very id it
  presented, so the sweep's `m.ID` re-opens the claim the first attempt made.
  The verbs whose Deliverer takes no key — voice, post, the social verbs — pass
  `""` and the row mints its own id; there is nothing for them to match.
  `routd/mcp_integration_test.go` `TestMCPReplyRetryReusesIdempotencyKey`
  drives a failing `reply` over the real socket and sweeps the row it leaves:
  both the row id and the retry's key must equal the first send's key. Against
  the old code it prints `row id="mcp-1789982117543437938" but the send claimed
  "mcp-be1f5085bb961060"` — the entry's live krons evidence, reproduced.

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

## L75 — a dispatch failure loses the trigger silently: the retry never retries, and pending mentions get demoted to "observed" (2026-08-23, (a) FIXED 2026-09-20; (b) needs sign-off)

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

## L62 — emaid retries an IMAP authentication rejection forever (2026-08-21, re-aimed 2026-09-25, open)

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

## 📋 RECORD F77 — skill-guard's per-line scan is evadable by splitting a payload (2026-08-08, RECORD 2026-09-21 — the demonstrated case is fixed, the class is out of scope by `5/23`)

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

**Re-verified 2026-09-21 at `a388cad7`: accurate as written, no action, and no
change since.** `logicalLines` (`ant/src/skillguard.ts`) still joins a line
ending in `\` with the next before the regex table runs, and
`ant/src/skillguard.test.ts` still carries the regression case. The residual
class — a payload assembled by string concatenation, held in a variable, or
split across a here-doc — still passes, and closing it still needs AST or
dataflow analysis that `specs/5/23` excludes by decision.

This entry wants no verdict and no sign-off. It is a recorded residual, kept so
the gap is not mistaken for coverage; `SECURITY.md` names the actual containment
boundaries (Docker, the crackbox egress allowlist, the gated MCP socket). It
should be re-read, not re-triaged, and it costs the queue a line rather than a
decision.

**Typed RECORD 2026-09-21 at `f5851f94`, which is the only thing that was
missing.** Both halves re-measured a third time and unchanged: `logicalLines`
(`ant/src/skillguard.ts`) joins a line ending in `\` before the regex table
runs, and `ant/src/skillguard.test.ts` carries three cases over it — the
`curl \`-split payload scanning `dangerous`, the finding pointing at the FIRST
physical line, and a Windows path that ends in a backslash and must NOT join.
The exclusion is the spec's own text, not an inference: `specs/5/23` is
`status: shipped` and says both halves itself — "split across string
concatenation or assembled from a variable still passes", and "Deliberately NOT
in scope: … AST scanning".

So the residual is spec-sanctioned and the title was the whole problem:
`PARTIALLY FIXED` reads as an open defect and drew a re-triage in August, in
September, and again now. It is typed `RECORD` so the next pass re-reads it
instead. Nothing here is a work item.

- **Severity:** low — raises cost, never a containment boundary
- **Scope:** `ant/` skill-guard PreToolUse hook
- **Source:** `ant/src/skillguard.ts` `logicalLines`; `specs/5/23-skill-guard.md`
- **Status:** record — the class is excluded by a shipped spec; do not re-triage
- **Fix:** none wanted

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


## ✅ FIXED 2026-09-20 L43 — 44 stale spec references in the live tree (2026-08-16, PARTLY FIXED 2026-09-21; 2 sites remain, genuinely homeless)

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

- **2026-09-20, the `6/7` family measured: 29 sites, not 36, and every one of
  them a code comment.** Tracked files minus `BUGS.md`, `CHANGELOG.md`,
  `.diary/` and `ant/skills/self/migrations/`: 26 in `.go`/`.ts`, 3 in
  `anteval/cases/anteval.toml`, and **zero in `specs/` or any root UPPERCASE
  doc**. No scripted rewrite was possible, and not only because the content
  split: `specs/6/7-*.md` has been six different specs across the renumbers
  (`self-learning`, `platform-api`, `ant-portability`, `onbod-dashboard`,
  `orthogonal-components`, `environment-contracts`), so the citations do not
  even share a subject.

  Five repointed (`8c83e657`), each to the spec whose CURRENT text carries the
  claim the citing line makes:

  | target | sites | the claim                                                                                    |
  | ------ | ----- | -------------------------------------------------------------------------------------------- |
  | `6/23` | 2     | anything `buildArgs` omits is dead inside the container — `6/23` names `ARIZUKO_BACKEND` in it |
  | `6/16` | 1     | the harness seam owns the backend selector (§ "The four layers")                               |
  | `5/P`  | 1     | the layered `.codex` mount (§ "The container model")                                           |
  | none   | 1     | `tests/specrefs/refs_test.go:130` cited the retired number as its own rationale; it dates the incident instead |

  Two of those three destinations sit OUTSIDE the four the split was expected to
  land in (`6/1`, `5/9`, `5/16`, `6/16`). The claim moved with the code, not
  with the spec text — which is the reason to read each site rather than trust
  the retirement commit's own account of where its content went.

- **Left: 24 sites, all in directories with other work in flight** — `ant/src/`
  (14), `cmd/arizuko/` (5), `anteval/cases/anteval.toml` (3), `onbod/` (2).
  Read and mapped, not applied:
  - `ant/src/` — the harness seam, the renderers and the selector are `6/16`;
    `skills-scan.ts`/`skills-scan.test.ts` are `5/23` (the skill-guard table
    they reuse at the second hook point).
  - `cmd/arizuko/probe.go` + `main.go` — the component contract is `6/16`
    § "The component contract"; "capabilities federate, infrastructure
    conforms" has no current home and needs a sentence written, not a pointer
    moved.
  - `onbod/dash.go:67` cites `6/7` for "one renderer per resource", which is
    root `CLAUDE.md` § "One renderer, many sinks" and was never in `6/7`.
    `onbod/main.go:203` is the proxyd transit proof, `5/16`.
  - `anteval/cases/anteval.toml:402` cites § "Context — three layers". **No
    current spec states the three-layer context model at all** — that is a hole
    to fill, not a citation to repoint.

**2026-09-21, the 24 sites applied — 22 fixed, 2 left.** Each destination was
read before writing, not assumed from the plan above:

- `ant/src/` (14) → `6/16` for the harness seam / occupant / renderer files
  (`backend.ts`, `backend.test.ts`, `claude.ts`, `codex.ts`, `codex.test.ts`,
  `pi.ts`, `pi-extension.ts`, `policy.ts`) — confirmed: `6/16` § "The four
  layers" names `ant/src/backend.ts` as owning "the seam types and the
  selector" and states "a second backend — pi, codex — is a sibling of
  `ant`", which is exactly what these files claim of themselves.
  `claude.ts:549`'s section name changed too: `6/16` has no section titled
  "The harness seam" (that phrase is prose, not a heading) — repointed to
  the actual heading, "The four layers". `skills-scan.ts` +
  `skills-scan.test.ts` → `5/23` instead: they extend the SAME skill-guard
  scanner to a second hook point, not the harness-seam architecture: the
  file already said so ("the 5/23 skill-guard hook"). Dropped their `§
  "Skills across backends"` — no section by that name exists in `5/23` or
  anywhere else; inventing one would trade a dead ID for a dead anchor.
- `cmd/arizuko/main.go` + `probe.go` (5) → `6/16` for the 4 "component
  contract" sites (confirmed: `6/16` has `### The component contract —
  arizuko owns domain, the component owns mechanism`). `probe.go:22`'s
  `"capabilities federate, infrastructure conforms"` is untouched — that
  exact phrase appears nowhere in the current corpus; re-verified, still a
  hole.
- `onbod/dash.go:67` → repointed to `CLAUDE.md § "One renderer, many
  sinks"` (not a spec ID — this was never a spec citation, `6/7` was wrong
  about its own kind, not just its number). `onbod/main.go:203` → `5/16`:
  confirmed `5/16`'s "hot-path exception" section documents the
  `X-User-Groups` transit-trust chain (`setUserHeaders` /
  `auth.UserScopes`) that `stripUnsigned` consumes.
- `anteval/cases/anteval.toml` (3): lines 379 and 400 (the portability claim
  and the "Backend contract" section header) → `6/16`, same seam. Line 402
  (`§ "Context — three layers"`) untouched — re-verified, still no current
  spec states a three-layer context model.

**Left, deliberately: 2 sites**, both genuinely homeless rather than
mis-numbered — writing a spec ID for either would be a guess:
`cmd/arizuko/probe.go:22` and `anteval/cases/anteval.toml:402`. Each names a
real design point (`5/13`'s capability-federates boundary; the three-layer
context model) that no current spec states in those words. Fixing these
means writing the missing sentence into an existing spec (or a new one),
not moving a pointer — out of scope for a reference-graph fix.

Measured before: 24 sites (`git grep '6/7'` over tracked files minus
`BUGS.md`/`CHANGELOG.md`/`.diary/`/`ant/skills/self/migrations/`). Measured
after: 2. `go test ./tests/specrefs/... -count=1 -short` stays green — none
of these 24 were in its scope (`TestEveryBacktickSpecIDResolves` only scans
`.md`; these were all plain-prose `(spec N/N)`, no backticks, in `.go`/
`.ts`/`.toml`), so the fix did not add regression coverage. That gap is
still open: a plain-prose, non-backtick citation in code can go stale again
with nothing to catch it.

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

## ✅ FIXED 2026-09-21 L27 — five channel platforms ship no output-style file, so their agents format blind (2026-08-15)

The picker does no host-side existence check by design (`5/Y` §"Name selection
and file loading are separate concerns"), so it named `whatsapp` for a whapd
turn, `ant/src/index.ts::readOutputStyle` swallowed the ENOENT in a bare
`catch`, and the agent answered with Claude Code's stock formatting — nothing
logged.

**Wrote four files, refused the fifth.** Each of the four earns its lines from
a send path that makes stock markdown wrong, read before writing:

- `whatsapp.md` — `whapd/src/server.ts:31` `mdToWa` rewrites `**bold**`→`*bold*`
  and `~~strike~~`→`~strike~` and NOTHING else, so `#` headers, `[text](url)`
  and single-backtick code reach the phone literal. Same class as teled's
  `mdToHTML`: the file tells the agent to emit markdown, not WhatsApp markup.
- `bluesky.md` — `bskyd/client.go:396` posts `req.Content` verbatim as the
  post's `text` and builds no richtext facets. 300 graphemes, enforced by the
  PDS REJECTING the record; the adapter neither trims nor threads, so an
  over-length reply delivers nothing at all.
- `linkedin.md` — `linkd/client.go:520` posts verbatim into
  `shareCommentary.text` / comment `message.text`, both plain text, 3000 chars.
- `twitter.md` — `twitd/src/verbs.ts` posts verbatim; 280 chars, no truncation
  and no threading in the adapter.

**Reddit deliberately gets no file.** `reditd/client.go:469` posts the agent's
text verbatim into a field Reddit renders as real markdown, so stock formatting
is already correct there; a file would add a surface to maintain and change
nothing. Its self-post title is hardcoded `"arizuko"` (`client.go:523`), so the
agent shapes nothing there either.

**Scope correction: the count was five, the reality is six.** `gitd` registers
`github:` (`gitd/main.go:28`) and `gitd/bot.go:98` posts `req.Content` verbatim
as an issue/PR comment — a sixth channel the picker names and no file serves.
It is the same call as Reddit: GitHub renders GFM, so stock output is right and
no file is wanted. Both are now recorded as deliberate, not missing.

- **Severity:** medium — every reply on four live adapters was unstyled.
- **Scope:** agent output formatting
- **Affected:** whapd, bskyd, linkd, twitd (reditd + gitd: no file by decision)
- **Source:** `ant/output-styles/` (18 files); `container/output_style_test.go`
- **Status:** resolved-not-yet-deployed. Editing `ant/` rides a release — this
  needs a `MIGRATION_VERSION` bump and an agent image rebuild to reach live
  agents. NOT bumped here; it ships with the next release.
- **Fix:** `fe13ded2`. `TestPickOutputStyle_ShippedStyleExists` binds every
  selected name to a shipped file and records the two deliberate stock
  fallbacks, so a later addition has to declare itself. Proved by mutation:
  renaming `whatsapp.md`→`whapd.md` fails the test, which is the real failure
  mode — the channel is the JID scheme, never the daemon name.

## ✅ FIXED 2026-09-25 L24 — a fresh instance ships with egress isolation OFF, while the docs describe it as the model (2026-08-15, closed by construction 2026-09-25)

**CLOSED BY THE SUBSTRATE, not by a signed default.** The question this entry
held open — should isolation default on — stopped existing when spec `6/41` step 4
deleted the Docker turn path. A turn is a KVM guest whose only interface is one
tap, the tap's rule set accepts that guest's own proxy and drops everything else,
and runed refuses to start when the proxy binary is missing. There is no
`EGRESS_IMAGE` to leave empty: `arizuko create` and `template/env.example` write
none, and the four `EGRESS_*` settings are gone from `core.Config` and from the
generated env. `TestCreateEnvCarriesNoEgressSwitch` inverts the old guard, so a
re-introduced switch fails it.

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

- **RE-MEASURED 2026-09-21 at `17459f6a` — the discoverability fix landed in a
  file the documented install path never reads, so it reached nobody.**
  `arizuko create` writes a SIX-KEY string literal and never opens
  `template/env.example` (`cmd/arizuko/main.go:314`, no branch reads it). So the
  2026-09-20 warning comment helps only an operator who copies `env.example` by
  hand, while `INSTALL.md` tells them to run `arizuko create`. On that route the
  generated `.env` said nothing about egress at all.

  Two docs asserted the opposite and were simply false: `template/README.md`
  ("seeds a new instance from `env.example`") and `cmd/arizuko/README.md`
  ("seed data dir from `template/env.example`").

- **FIXED 2026-09-21 — the no-sign-off half, done properly this time.**
  `create`'s generated `.env` now carries the commented `EGRESS_IMAGE=` block
  naming the switch and saying that empty lets agent containers reach the
  internet UNFILTERED. **Zero behaviour change**: `core/config.go:283` is
  `EnvOr("EGRESS_IMAGE", "")` and `compose/compose.go:801` gates on non-empty,
  so an explicit empty is exactly what absent already meant. Both README lines
  now state what `create` actually writes.

  Test: `TestCreateEnvNamesEgressIsolation` (`cmd/arizuko/create_env_test.go`).
  Mutation-proven two ways — removing the block fails it, and SEEDING AN IMAGE
  also fails it. That second assertion is deliberate: it is a tripwire so the
  unsigned default cannot be flipped as a "docs fix" by a later pass.

- **The default is now the ONLY thing left, and three facts price it.**
  1. **Every live instance already runs isolated.** `krons`, `marinade` AND
     `sloth` all set `EGRESS_IMAGE=arizuko-egress:latest` (read 2026-09-21 —
     the entry said "both", there are three). The default is the only thing
     disagreeing with universal operator practice.
  2. **The image is not an extra step.** The entry's stated blocker — "requires
     the crackbox image to be present" — names the wrong image and overstates
     it: `make images` builds `arizuko-egress` unconditionally
     (`Makefile:166`), and `INSTALL.md` tells every operator to run it.
  3. **Seeding the one key is enough, and its failure mode is loud.** Two
     checks that could have made this a multi-key change do not:
     `compose.Generate` DERIVES the other two inputs whenever `EGRESS_IMAGE`
     is non-empty — `EGRESS_NETWORK_PREFIX` from the app/flavor and
     `EGRESS_UPLINK_NETWORK` as `<project>_default`
     (`compose/compose.go:801-812`) — so `EgressConfig.Effective`'s
     missing-input error (`container/egress.go:127`) cannot fire from a seeded
     default, and runed starts normally. And if the IMAGE itself is absent,
     `container/runner.go:229` returns `Status: "error"` for the spawn:
     "starting the agent anyway would put an unfiltered container on the
     network, the exact inversion of the property egress exists for." So a
     host that skipped `make images` gets a per-turn error naming the cause —
     never a silent unfiltered fallback.

  **The owner's decision is one sentence: should `arizuko create` seed
  `EGRESS_IMAGE=arizuko-egress:latest`, accepting that on a host that skipped
  `make images` every turn then errors instead of answering unfiltered?** Yes
  costs a new operator a fleet that answers nothing until they build one image,
  with the error naming it. No keeps first-run frictionless and keeps shipping
  a default that all three live instances already override by hand.

- **REFUTED while measuring this — two claims that read as blockers are not.**
  The entry's "requires the crackbox image to be present" names the wrong
  image (`arizuko-egress`, which `Makefile:166` builds in `make images`), and
  `runed/docker.go:152`'s comment about refusing to start describes a
  MISSING-INPUT config, not a missing docker image; compose derives those
  inputs, so that refusal is unreachable from this flip.

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

## 📋 RECORD L15 — dead-code sweep 2026-08-11: the hits deliberately REJECTED (record, not a bug)

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

## ✅ FIXED 2026-09-20 J16 — `ant/CLAUDE.md` tells the agent connector tools are deferred; they are eager (2026-08-09)

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

**ALREADY FIXED, verified 2026-09-20.** `dc2bcfbe` ("the turn's tool list is
complete, and there is no tool search") removed the claim, and did better than
this entry's "drop the paragraph": `ant/CLAUDE.md:102` now states the true
mechanism AND what to do about it — "There is no tool search in this runtime …
A capability you cannot see is an unmet grant — give the user the exact ask;
never hunt for a tool that would produce it." So the agent is no longer sent
searching for tools already in its list.

The mechanism the entry named is unchanged and still correct:
`ant/src/mcp-servers.ts` sets `alwaysLoad: true`, so every tool on the arizuko
server rides the eager prefix. No release action is outstanding — the edit
shipped with its own commit.

- **Status:** fixed
- **Fix:** `dc2bcfbe`

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

## ✅ FIXED 2026-09-21 F37 — three dispatch tests fail under whole-suite load, so the suite cannot gate a release (2026-08-06, `tests/` half FIXED 2026-09-12 at `881bfbf0`; the slakd half CAPTURED 2026-09-21 — it is a TIMEOUT, not a flake)

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
- **Status:** the `tests/` half is FIXED; the slakd half is still open and still
  needs a captured failure.
- **Fix:** give `inbound-to-dispatch` the same deadline treatment for the bot
  row that it already gives the dispatch — poll `countBotRows` rather than
  reading it once the instant dispatch is observed. `slakd` needs a captured
  failure first: run it under `-count=N` with the log kept, because a
  package-level `FAIL` with no `--- FAIL` line is usually a panic or timeout in
  a goroutine, not an assertion.

**✅ The `tests/` half shipped 2026-09-12 in `881bfbf0`, and it was applied to
the whole package, which is what this entry asked for.** The prescribed fix —
wait on the bot row instead of reading it the instant dispatch is seen — is
`testutils.WaitForBotRow` (`tests/testutils/testutils.go:277`), a deadline poll
over `MessagesSince` for a `BotMsg` row. Both named tests now go through it
before they assert, on a 5s deadline:

- `tests/feat_routing_test.go:58` — `inbound-to-dispatch`, with the race written
  down beside it: *"The fake agent records the dispatch before it posts its
  reply, so the bot row is the signal the turn closed; waiting on the dispatch
  alone raced the reply under a loaded package run."*
- `tests/split_federation_test.go:439` — `TestSplitFederation_InboundToTurnRoundTrip`.

It reaches six more poll-then-assert sites in the package
(`feat_continuity_test.go:129,184`, `onboarding_e2e_test.go:114`,
`feat_secrets_test.go:209`, `e2e_turn_test.go:320`), so the shape, not the one
named test, is covered.

**The slakd half is unchanged and is NOT closed by this.** It never got its
captured failure, and the entry's own instruction still stands: do not close it
by re-running on an idle box.

Two things narrow it since filing, both measured at `a388cad7`:

- **The DNS confound does not apply here.** `chanreg.Register`'s unbounded
  `net.LookupIP` costs ~7.8s per hostname and did wreck `chanreg` and `routd`
  timings, and slakd's tests do use compose service names
  (`http://slakd:8080`, `http://gated:8080`). But slakd never registers through
  that path — `ListenURL` is read only at `slakd/bot.go:858` to build a
  `/files/<id>` URL string. No resolver, so this is not slakd's cause.
- **slakd is instead uniformly slow, which is the profile that fits a
  package-level timeout.** `go test ./slakd/ -count=1 -short` took 37.7s then
  23.9s on two consecutive idle runs — a 1.6x spread with nothing else changing.
  No single test dominates (slowest: `TestOutbound_RateLimitRetry` 1.41s,
  `TestPostFormDoesNotRetryACreateCallOn500` 1.23s, then a long tail of ~98
  tests at 0.1-0.6s). Nearly every one carries a real timer, so the package
  total scales with host load rather than with any one assertion — which is why
  the failure presented with no `--- FAIL` line and why an idle re-run cannot
  reproduce it.

So the remaining work is exactly what the entry says: capture it. The disk-
pressure confound was never eliminated and the host is at 97% again today.

**✅ CAPTURED 2026-09-21 at `e7f1bac3`, and the answer is that slakd was never a
flake. It is go's own per-package timeout, and there is no race to find.**

Reproduced on demand — the signature this entry has carried unexplained since
August is what `go test` prints when it runs out of budget:

    $ go test ./slakd/ -count=1 -short -timeout 20s
    panic: test timed out after 20s
        running tests:
            TestWatchdog_ReprobesOnStale (0s)
    <2703 lines of goroutine dump>
    FAIL    github.com/kronael/arizuko/slakd    21.339s

Zero `--- FAIL:` lines, a goroutine dump, and a bare package-level `FAIL`. That
is exactly what the August run recorded, and it is a timeout, not an assertion.
The same thing happened to `go test ./routd/` under load on 2026-09-21.

Three facts close the alternatives:

- **The budget was never chosen.** `make test` ran `go test ./... -count=1
  -short` with no `-timeout`, so every package inherited go's silent 10m. 22 of
  the suite's invocations were in that state — the root line and all 21
  per-daemon and per-component sub-makes, each of which runs `go test ./...
  -count=1` bare. slakd is one of them, and `make test` runs it twice.
- **The other producer of a bare `FAIL` would have said so.** A `dyntest` setup
  failure is the only other load-sensitive path to a package-level FAIL with no
  subtest line, and `dyntest.Main` prints `dyntest: <err>` to stderr before
  `os.Exit(1)` (`objstore/dynstore/dyntest/dyntest.go:127`). Nothing like that
  was in the record.
- **The load arithmetic works.** slakd measures 24.8s idle at HEAD and the entry
  already recorded a 1.6x spread with nothing changing. The host has TWO cores
  (`dyntest.go` says so, and `nproc` agrees), `dyntest` starts a container per
  test binary, and the failures happened with three agents running suites
  concurrently at 100% disk. Reaching 600s needs 24x; the headroom was never
  there.

**This entry's own diagnosis was the cost.** It reasoned from "passes isolated,
fails under load" to a race and asked for a captured failure to find it. The
premise was sound and the conclusion was wrong: passing isolated and failing
under load is also, and more simply, what a fixed time budget does. `make
test-race` had already hit the identical wall and written it down (BUGS `L26`,
"two packages exceed go's 10m per-package default … the gate the release
procedure names could never run") — but it set a budget only for itself, so the
lesson never reached the target that needed it.

**Fixed by stating the budget once**: `export GOFLAGS += -timeout=20m` in the
root Makefile. `+=` so a caller's own GOFLAGS survives rather than being
replaced, and an inline `-timeout` still wins because go's command line beats
GOFLAGS — `integration` (120s), `test-race` (30m), `test-e2e` (300s) and
`test-turns` (600s) keep the budgets they name. Verified: `GOFLAGS=-timeout=1s
go test ./slakd/ -short -timeout 300s` passes in 24.4s, so an explicit budget is
not overridden. `tests/buildgate/timeout_test.go` reads the Makefiles and fails
when a `go test` the gate runs would fall back to the default; deleting the
export makes it name all 22.

**The disk confound is unaffected and stays true.** Disk pressure and CPU
contention are both ways to run out of a fixed budget, and this fix does not
distinguish them — it only stops either one presenting as a mystery FAIL. A
package that now exceeds 20m is a real hang and should be read as one.

- **Fix:** Makefile `export GOFLAGS += -timeout=20m` +
  `tests/buildgate/timeout_test.go`

## ✅ CLOSED 2026-09-21 F34 — "who ended this engagement" has two answers in two tables (2026-08-06, DISPROVED — there is one table, and the correction is now pinned in the code)

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

**CLOSED 2026-09-21 at 17459f6a. Re-verified from the call path, not from the
2026-08-06 note: the premise is dead and the correction it prompted has held.**

Both writers land in `audit_log`, so the operator's question takes one query:

- REST — `routd/engagement_http.go:112` → `DB.SetEngagementAudited`
  (`routd/db.go:523`), which rides the window's own write:
  `audit.WriteFor(ev)` then `BumpEngagementWith`. One batch, all-or-nothing.
- MCP — `granted("disengage", …)` (`ipc/ipc.go:1706`) → `emitSys`
  (`ipc/ipc.go:1108`) → `db.LogIPCAudit` → `audit.EmitTo`
  (`store/ipc_audit.go`), `category=agent`, `action=mcp.tool.invoke`,
  `surface=mcp`. An `audit_log` row, not an `ipc_audit` one.

`ipc_audit` is written by nothing: `grep -rn "ipc_audit" --include=*.go`
returns this one file plus test doubles, and the name is all that is legacy.
`/dash/audit/` reads `audit_log`, so it shows the agent's disengages too.

The cause fix this entry asked sign-off for — "should the `granted` wrapper
emit `audit_log` for every state-changing tool" — is **already the shipped
behaviour**, and not for `disengage` alone: `emitSys` is the wrapper, so all
~45 granted tools report the same way. There is nothing to sign off.

The correction is pinned where the wrong claim lived: `ipc/ipc.go:296-300`
now names `audit.EmitTo` as the destination and cites this bug ID, so the
stale field comment that produced the premise cannot re-seed it.

**Two things found while closing, neither reopening this entry:**

- **The transactionality difference in the original report is REAL and is the
  recorded design, not a gap.** REST rides the mutation's batch; the MCP path
  cannot, because an MCP tool's mutation is whatever the tool did and there is
  no batch to join. `emitSys` also discards the error (`_ = db.LogIPCAudit(…)`),
  so a failed audit write is silent. That is one concern, not this one — this
  entry was "two tables", and there is one. `store/ipc_audit.go` states the
  constraint at the function, corrected in this commit.
- **`audit.EmitInTx` no longer exists** (`audit/audit_log.go` has `WriteFor`
  and `EmitTo`), yet eleven sites still name it: `store/ipc_audit.go:16`,
  `audit/PLAN.md:517,546,549`, `specs/5/I:8`, `specs/5/35:73`, `specs/5/G:185`,
  `routd/social_audit.go:44`, `runed/audit.go:44`, six routd `*_resource_test.go`
  comments, and `CHANGELOG.md:1781`. Only ONE instructed a future caller to use
  it — `store/ipc_audit.go:16` — and that one is fixed here. The rest are
  descriptive ("EmitTo, not EmitInTx, because …"), historical (CHANGELOG), or a
  spec's record of what shipped; rewriting 1 of 11 would leave the tree
  inconsistent, and rewriting all 11 is a rename sweep across specs and tests
  that belongs to its own pass. Recorded, not swept.

## ✅ CLOSED 2026-09-21 F38 — `task_run_logs` holds orphan rows on all three live instances (2026-08-06, ✅ CLOSED 2026-09-21 — the constraint that made it a trap is gone, and the orphan is now the recorded design)

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
- **Status:** CLOSED 2026-09-21.

**Every load-bearing word of this entry names something that no longer exists.**
The whole entry is a SQLite statement: `PRAGMA foreign_key_check`, a FK added by
a later migration than its data, `routd.db` opened with
`_pragma=foreign_keys(on)`, and "a rebuild under FK enforcement". The
DynamoDB move (v0.68.0, 2026-09-16) took all four. There is no
`routd/migrations/` or `store/migrations/` directory, no `PRAGMA` anywhere in
the Go tree, and `database/sql` is reached only by `audit/audit_log_test.go`
and `objstore/ordered.go`. The trap the entry warns about — "the moment any
migration rebuilds `task_run_logs`" — has no mechanism left to fire.

**The concern did not die with the subsystem; it was decided.** `task_run_logs`
is an objstore entity (`store/task_run_logs.go`), and the question the entry
raised is answered twice in the tree, in writing, as intent rather than damage:

- `store/task_run_logs.go:30-35` — "A RUN LOG OUTLIVES THE TASK IT NAMES.
  `scheduled_tasks` is an entity and an entity has no column a REFERENCES clause
  can name, so removing a task removes the task and nothing else", and it names
  the same three counts this entry measured: "The three live instances already
  carry 10, 32 and 66 logs whose task is gone."
- `store/folder_scope.go:200` — a group delete keeps `task_run_logs` on purpose:
  `keep("history; a run names a task id the next tenant never mints")`.

So the producer the entry asked to identify is an ordinary task delete, and the
delete-orphans-vs-relax-the-constraint choice was made in favour of neither:
there is no constraint, and the rows are history that is meant to outlive the
task. Nothing to fix.

## ✅ FIXED 2026-09-21 T2 — a steered turn gets no failure notice when its run dies (2026-08-15)

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
- **Status:** fixed — the failure notice fans out over the same reader the
  typing clear uses.

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

**Fixed 2026-09-21.** The `OutcomeError` block sends over
`chatsOwnedBy(chatJID, out.RunID)` — the helper `T1` built, which already
carries the fall back to the primary when the run reported no id. It was named
`typingOwnedBy`; a second consumer that is not typing makes that name a lie, so
it is now `chatsOwnedBy` and its comment says both readers must agree or the
half that goes quiet is the half that tells the user something. The notice
renderer is untouched — same `runFailureNotice` / `retryExhaustedNotice` /
`terminalFailureNotice` selection, one destination loop around it.

The topic decision went to **untopiced for the fan-out**: a thread id is local
to its own chat, so the parent's topic means nothing in chat B and could land
the notice in an unrelated thread there. Only the primary keeps its topic, so
the already-correct path is byte-identical. The idempotency key gained a `-<jid>`
suffix on every destination, primary included, or the adapter dedups the
fan-out into one delivery.

Test `TestRunErrorNotifiesEveryChatTheRunOwned` (`routd/fixes_test.go`) steers a
second chat into the live run from inside the runner, then asserts the notice
TEXT reached that chat and that the two keys differ. Without the loop it fails
with `chat slack:T/B/U got ""`.

- **Left alone deliberately:** `MarkChatErrored`/`MarkMessagesErrored` still
  name only the primary chat. Those drive the breaker and the error census, not
  delivery — a different concern, and widening them changes breaker semantics.

## ✅ FIXED 2026-09-20 X2 — the aws-devops product's credential model died with X1 (2026-08-09)

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

**CLOSED 2026-09-20: the decision was carried out.** `242b7d10` ("retire the
aws-devops product — its credential model is gone") removed all three things
this entry names. Verified at HEAD: `specs/17/product-aws-devops.md` does not
exist, `ant/examples/aws-devops/` does not exist, and `aws-devops` appears
nowhere in `cmd/arizuko/`, so the `--product aws-devops` flag is gone with it.
Nothing can seed the product that would fail silently.

- **Status:** fixed
- **Fix:** `242b7d10`

## ✅ FIXED 2026-09-21 M1 — `mcpc` socat-connect form 502s since mcpc 0.3.0 (2026-07-16, ✅ FIXED 2026-09-21 in the repo; delivery needs a `MIGRATION_VERSION` bump)

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

**✅ FIXED 2026-09-21 — the form was measured against a real socket, and the
client's grammar, not its HTTP tunnel, is the cause.** The deferral's stated
blocker was "unverifiable without a live socket", so one was built: a socat
`UNIX-LISTEN` in front of a minimal newline-delimited JSON-RPC MCP server, and
`@apify/mcpc` installed at the version npm serves today.

The 502 is gone because the version moved — exactly the drift the unpinned
`ant/Dockerfile:171` install was flagged for. npm now serves **0.6.0**, and on
it the documented form fails differently and more plainly:

```
$ mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
Error: Cannot specify @session name when connecting all servers from a config file.
$ mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -"
Error: Config file not found: …/socat UNIX-CONNECT:…/gated.sock -
```

`mcpc help connect` states the whole grammar: a server argument is a remote HTTP
server or a `<config-file>[:<entry>]` pair. **There is no ad-hoc command form.**
A stdio server is reachable only as a config entry — "Config entries spawn the
command on connect". So the form in the skills was never a tunnel regression to
wait out; it is a shape the client does not parse, and no version will parse it.

The working form, connected and `tools-list`ed against that socket:

```bash
cat > "$HOME/gated.mcp.json" <<JSON
{"mcpServers":{"gated":{"command":"socat","args":["UNIX-CONNECT:$ARIZUKO_MCP_SOCKET","-"]}}}
JSON
mcpc connect "$HOME/gated.mcp.json:gated" @s
```

→ `MCP: version 2024-11-05 / stdio (stateful)`, the server info and the tool
list. `mcpc @s close` (the `trap` the skills already use) also works as written
— only `connect` was wrong.

Shipped into the three live sites: `ant/skills/self/mcp.md`,
`ant/skills/mcp/SKILL.md`, `ant/skills/migrate/SKILL.md`.
`ant/skills/self/migrations/052-mcpc-cli.md` is frozen history and is untouched.
Pinned by `ipc/skill_mcpc_form_test.go`, which fails on any shipped skill whose
`mcpc connect` argument carries a space — the one property that separates a
command from a URL or a config path — so the form cannot rot back in silently.

- **Blast radius this closes, and it is wider than "ad-hoc scripts":** the whole
  release-announce step in `ant/skills/migrate/SKILL.md` opens with this connect
  and every later line runs through `@s`, so the `#announce` broadcast could not
  run at all.
- **Still the operator's call:** the repo is correct, the fleet is not. `ant/` is
  baked into the image, so delivery needs a `MIGRATION_VERSION` bump plus an
  ant-image rebuild and a restart — a release, not a bug fix, and a bump also
  queues one auto-migrate turn per folder.
- **Pinning `@apify/mcpc` is still undone** and is the other half the entry named:
  `ant/Dockerfile:171` installs it unpinned, which is how 0.3.0's 502 became
  0.6.0's parse error with no commit in between.
- **Found next door, NOT fixed here (record-don't-fix):** two lines below the
  edit, `migrate/SKILL.md` pipes `mcpc @s tools-call inspect_routing` into `jq`
  with no `--json`. Measured on 0.6.0, a plain `tools-call` prints a human
  banner (`✓ Tool … executed successfully`), and only `--json` emits JSON, so
  that `jq` cannot parse what it is given. Separate defect, separate entry.

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

## ✅ FIXED 2026-09-21 R2 — Reserving `#announce` silently steals an existing route topic (2026-07-15, ✅ CLOSED 2026-09-21 — the migration half is impossible, the silence half is fixed)

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
- **Status:** CLOSED 2026-09-21. The entry's `Fix:` field was left blank for two
  months, and that blank was the honest answer.

**The premise holds and the parser is unchanged** (`core.ParseRouteTarget`,
`core/types.go:138-154`): `case "announce": rt.Announce = true`, `Topic` stays
empty, and `String()` renders it back as `#announce`.

**The migration the entry asks for cannot be written.** `core.Route` is
`{ID, Seq, Match, Target, ObserveWindow*}` (`core/types.go:107-114`) — no
created-at, and the DynamoDB move did not add one. The string `folder#announce`
is therefore identical whether it was written before v0.59 as a pinned topic or
after it as an announce tag. Nothing in the row distinguishes them, so
"detect and rewrite existing collisions" has no predicate to run. The window is
also shut: v0.59 is two months and one whole store migration back, and no
instance reported a topic that stopped pinning.

**What was live is the word "silently", and that half is fixed.** The reserved
vocabulary existed only in the parser. `ROUTING.md`'s fragment table — the
operator-facing list — carried `_(none)_` and `#observe` and stopped, so an
operator had no way to learn that `announce` was taken, nor even that any other
fragment pins a topic. The table now names `#announce` and `#<topic>` and states
that `observe` and `announce` are reserved words a topic cannot use.
`core/route_fragment_doc_test.go` reads the `case` labels out of
`ParseRouteTarget`'s own source and fails when ROUTING.md does not name one, so
the next reserved fragment cannot reach the parser without reaching the operator
— which is the mechanism that produced this entry.

## ✅ CLOSED 2026-09-21 System-turn / broadcast delivery targets a folder-jid or prefix-less room → "no channel for jid" (2026-07-13, CLOSED 2026-09-21)

CLOSED — all three premises fixed by dated commits, and BOTH prescriptions the
entry proposed are now regressions. Verified at `96cab2b6`.

- **The ERROR-spam / breaker storm** (`5f7a2f3a`, 2026-07-15). `deliverRow`
  (`routd/turns.go:168`) refuses a jid with no `:` before the Send, so a doomed
  `Send` no longer trips the per-folder breaker on a MIGRATION_VERSION bump.
- **"Silently lost"** (`ed9d9e9f`, 2026-08-23, `L76`). The refused row lands
  `failed` — the dead-letter state — so the content stays visible on the
  `dead_letters` surface. Nothing is lost; it is parked where an operator reads
  it. `/v1/outbound` does the same AND returns `502 delivery_failed` to the
  caller (`routd/server.go:648-662`).
- **The v0.59 prefix loss** (`8e19a746`, 2026-08-24, `L89`). The announce step
  builds `<platform>:<room>` from the route match, and a `room=`-only match
  aborts the step loudly rather than announcing nowhere
  (`ant/skills/migrate/SKILL.md:268-317`). The `~/.announced-version` guard
  advances only after every send succeeds, so the step retries.

**The 2026-09-05 evidence in this entry recorded the fix working, not the bug.**
`WARN`, not `ERROR` — the guard's own line; "is dead-lettered (`out-c804…`)" is
`L76`'s chosen outcome. The krons `eval/etksvtw-*` lines are anteval chats,
which have no platform channel by design: anteval asserts on stored rows via
`/v1/messages/inspect`, so a dead-lettered reply is still the eval's evidence.

**Do not apply either prescribed fix.**

- "Skip + debug-log instead of ERROR-spamming" would re-stamp the row `sent` or
  drop it — that is exactly `L76`, which cost two lost operator alerts, one of
  them reporting a skills-breaking guard.
- "Resolve the group's bound channel jid in routd" would be a SECOND announce
  resolver beside the migrate skill's `inspect_routing` + `#announce` read. One
  renderer, many sinks: the skill owns destination resolution.

`routd/loop.go:588` still writes the auto-migrate prompt with `ChatJID: folder`.
That is the internal address of a turn with no chat, not a defect: the turn's
announcement goes out through the skill's own `send` calls to real JIDs, and the
leftover prose dead-letters by design.

Pinned by `routd/contract_test.go` `TestDeliverRow_SkipsBareFolderJid`, which
now asserts BOTH shapes the entry named — the bare folder and the prefix-less
room `user/137865889`.

- **Severity:** none (closed)
- **Status:** CLOSED — no code change; test widened to the second shape.

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

## ✅ FIXED 2026-09-21 OPERATIONAL — Admin cannot log in to the krons dashboard (2026-07-12)

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

**CLOSED 2026-09-21: both halves this entry asked about are in place on krons.**
Measured against the live instance, not inferred:

- *(a) is a provider configured?* Yes. `GITHUB_CLIENT_ID` and
  `GITHUB_CLIENT_SECRET` are set in the top-level `/srv/data/arizuko_krons/.env`
  and reach authd through its scoped `env/authd.env`, with
  `AUTH_BASE_URL=https://krons.fiu.wtf`. The entry's "no OAuth provider config"
  was true when written and is not now.
- *(b) does the first operator hold a grant?* Yes.
  `role:operator :: * :: ** :: allow` exists in `routd.acl`, and
  `routd.acl_membership` edges `github:kronael -> role:operator` — the GitHub
  identity the configured provider issues. So a successful GitHub login lands on
  an operator.

`auth_users` having 0 rows is no longer the signal it was: the SQLite
`auth.db` this entry read is frozen, and identity lives in authd's
`oauth_identities` entity now.

One nearby fact worth keeping, since it burned a measurement here: reading
these keys with `grep -o '^KEY='` prints the key and HIDES the value, so a
configured key reads as empty. Match the whole line.

- **Status:** fixed (operator config, since applied)
- **Fix:** none in code

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

## ✅ CLOSED 2026-09-21 5/16 two-faces rollout — status (2026-07-06)

A rollout snapshot; the rollout finished. Each of its six items re-verified at
`96cab2b6`.

1. **routes/tasks REST bake the agent tier model** — the entry already recorded
   this RESOLVED at `0d25b687` (per-face `containFn`).
1b. **`/v1/tasks` not OpenAPI-discoverable** — fixed, and the mechanism the item
   reasoned about is gone. `ScheduledTasksEndpoints`
   (`resreg/resources/scheduled_tasks.go:54`) declares the REST paths `/v1/tasks`
   and `/v1/tasks/{taskId}` beside three `MCPOnly` agent actions, and
   `resreg.MountedResources` (`resreg/openapi.go:100`) derives the advertised set
   from the daemon's own mux — so the hand-curated `[routes, web_routes, acl]`
   list no longer exists to leave anything out of.
2. **network_rules out of scope** — still true, still no work item: all three
   actions are `MCPOnly` (`resreg/resources/network_rules.go:28`), i.e. no REST
   twin, so the `containFn` adoption stays conditional on a face that has not
   landed.
3. **Tool-browser drift** — recorded RESOLVED; confirmed. `dashd/tools_admin.go:34`
   renders from `auth.EffectiveActions`, the same view the agent socket gets at
   `routd/agent_gate.go:72` (`2ef91f65`).
4. **`container/runner.go` standalone ServeMCP** — subsystem gone. Neither
   `ExternalMCP` nor any `ServeMCP` call survives in `container/`; routd hosts the
   socket on every path (`routd/mcp.go` `ServeTurnMCP`).
5. **surrogate bodyless-4xx** — the entry already recorded this RESOLVED at
   `e813efd5`.
6. **flaky `TestRefreshRotationRaceSingleWinner`** — superseded, and the
   supersession is written down. `authd/f36_test.go` states in its own header that
   this test "passes on an idle machine, so it can never be the proof that a fix
   landed", and adds two tests that FORCE the interleave through a production seam
   (`Authd.grants`). Measured 2026-09-21: `go test ./authd/ -count=10 -run
   TestRefreshRotationRaceSingleWinner` passes, and `go test ./authd/ -count=1
   -race` passes in 27s.

- **Status:** closed — nothing left to track
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

## ✅ CLOSED-STALE 2026-09-21 chat: loadChatSessions limits before filtering; nondeterministic continue links (2026-06-18)

Subsystem gone. `dashd/chat.go` was deleted at `f98f2f33` (2026-08-15, "V2 delete
the chat portal and the route-token mint form"); `loadChatSessions` and
`folderSessionTokens` match nothing in the tree. The proposed fix — an explicit
session-to-topic column persisted at first message — has no reader left to serve.

- **Status:** closed. This was the last surviving finding of the 2026-06-18 codex
  dashd sweep; the other 19 were pruned at `57b14af8` + `0d376586` (2026-08-15),
  which left that sweep's heading standing over nothing.
- **Fix:** none needed

## anteval `--mcp` has no MCP client — bounded, anteval-side (2026-07-11, fixed 2026-09-22)

**The sign-off this entry asked for is not needed: the public surface it
wanted already exists.** It offered two ways to close `5/9` gap (b) and said
both "change a public surface contract or the spec's case semantics". Option
(1) — an inspect-read on the chat-token MCP face — is SHIPPED. `webd/mcp.go:171`
registers `get_history(folder, topic, limit≤100)` in `buildWebMCP`, the one
builder both doors call, so it answers on `POST /mcp` AND on
`POST /chat/{token}/mcp` (`webd/server.go:103,128`). It is folder-bound the way
the entry asked — the grant set comes from the route-token ROW, never a request
param — and it returns BOTH roles (`messageRole`, `webd/server.go:296`:
non-bot → `"user"`), which is exactly what a harness-injected USER sentinel
needs.

**Two of the entry's facts were already stale when it was filed or went stale
after.** The tools are not `send_message`/`get_round` — those names exist
nowhere in the tree; the surface is `list_groups` / `send` / `get_history`. And
the blocker it rested on ("get_round returns ASSISTANT frames for one turn, so
the sentinel cannot be read through it") therefore has no referent.

**What is actually missing is one thing, inside anteval.**
`HTTPTarget.McpMessages` (`anteval/pkg/run/target.go:107`) hands `t.MCPURL` to
`messages()`, which GETs `<base>/v1/messages/inspect?jid=` — a REST shape. So
`--mcp` can only ever be pointed at a second REST URL, and pointing it at
webd's MCP face is not a misconfiguration the operator can fix. The work:

- a streamable-HTTP MCP client calling `get_history`. `mark3labs/mcp-go
  v0.45.0` is already a direct dependency and ships
  `client.NewStreamableHttpClient`; webd mounts the server side stateless.
- the argument shape. `McpMessages` takes a chat jid; `get_history` takes
  `folder` + `topic`, both required. anteval has `--folder` and runs each case
  in its own topic, so both are in hand — but it is a signature change, not a
  URL swap.
- `role` → `check.Msg.FromBot`, the mapping `RestMessages` does from
  `is_bot_message`.
- restoring a `rest-mcp-parity` case. The `parity_sentinel` checker is still in
  the vocabulary (`anteval/pkg/check/check.go:76`, `spec.go:52`) and still
  tested; only the case was deleted 2026-08-21, so nothing is affected TODAY.

No public contract changes, no spec amendment, no case semantics rewritten.
`5/9` and `anteval/README.md` both said the face lacks an inspect-read; both
corrected in the same pass.

- **Severity:** low (a flag that cannot be configured; no case depends on it)
- **Scope:** `anteval/pkg/run/target.go`, `anteval/cases/anteval.toml`
- **Affected:** nothing live — the case that used it is gone
- **Source:** anteval/pkg/run/target.go:83-107; webd/mcp.go:171;
  webd/server.go:103,128,296
- **Status:** fixed. `anteval/pkg/run/mcpread.go` speaks streamable HTTP
  (`mark3labs/mcp-go`, already a direct dependency) and calls `get_history`.
  The two argument shapes the entry flagged are settled: the topic is the run's
  NONCE, which `run.go` already injects under, carried to the checker on
  `check.Ctx.Topic`; the folder comes from `groupfolder.JidFolder`, so a
  suffixed jid names no folder and is REFUSED rather than read as another
  group's history (`L139`'s bug, not repeated). `role` maps to
  `check.Msg.FromBot`. The `rest-mcp-parity` case is back in
  `anteval/cases/anteval.toml`.

  Two silent failure modes are pinned by test and proven by mutation: inverting
  the role mapping, and failing to open the result envelope — webd answers
  `NewToolResultText`, so the payload is a JSON STRING inside the first content
  and is decoded twice. Either mistake reports an empty or mis-attributed
  history, which in a parity check reads as "MCP does not have it".
  `anteval/README.md`'s known-gap paragraph is replaced by what it does now.

## ✅ FIXED 2026-09-21 krons: `/hook/` proxyd route missing — webhook surface dead; route table not hot-reloaded (2026-07-11)

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
- **Status:** fixed (row inserted on krons, awaits proxyd restart; re-seed/upsert path undesigned)
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

**Both facets are closed in code, verified 2026-09-21.**

*Seeded-once drift.* Core routes are no longer seeded into an empty table and
then frozen. `collectProxydRoutes` (`compose/compose.go:565`) rebuilds the whole
core set from `coreProxydRoutes` on EVERY generate, and `store.SyncProxydRoutes`
rewrites the arizuko-owned side each run — its own test says so: "rewritten
every run, because generate re-derives it from the package's own manifest
fragment, exactly as it re-derives a core route from the release table". So an
instance seeded before a route was added gains it on its next generate.
`/hook/` is row `compose/compose.go:508`.

*Hot reload.* `proxyd`'s `snapshot()` (`proxyd/resource.go:57`) queries the
route table per call and holds no cache across requests, so a row written live
is served without a restart.

What is NOT settled by this entry is whether a given live instance is running a
build new enough to contain either — that is a deploy question, answered by the
eval gates, not a defect. `anteval --case webhook-in` is still the check.

- **Status:** fixed in code; live verification belongs to the deploy gates
- **Fix:** `collectProxydRoutes` + `SyncProxydRoutes`; `proxyd/resource.go`

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

## ✅ CLOSED 2026-09-21 5/33 grant-surface flip — attempted, reverted (2026-07-30)

A record of what the CORRECT flip had to satisfy. The flip landed; all five are
satisfied, and three of them describe functions that no longer exist. Verified at
`96cab2b6`.

- **BUG1 (the differential drove dead code)** — a lesson, never a code defect, and
  moot besides: `auth.Authorize` is now the one evaluator, so there is no second
  representation to run a differential against.
- **BUG2 (`deriveFolderGrants` rebound the role per read)** — the function is
  gone. The role is bound once at create: `store.SeedMemberRoleTx`, called from
  `store.SeedRoutdFloor` (`store/seed.go:102`, `routd/db.go:83`).
- **BUG3 (deny masked by index sort order) — the one flagged LIVE; fixed, and by
  construction rather than by an ORDER BY.** `auth.Authorize`
  (`auth/authorize.go:87-118`) scans ALL matching rows, accumulates `denied`, and
  returns false if any deny matched — so row order cannot change the answer.
  Guard: `TestAuthorize_DenyWinsSameTriple` (`auth/authorize_test.go:62`). The
  named live consumer is gone too: the `share_mount` gate is now
  `routd/dispatch.go:884`, which calls `l.db.Authorize` directly.
- **BUG4 (`SeedTierRoles` insert-or-ignore)** — gone. `SeedRoutdFloor` seeds only
  when the ACL entity is empty; no periodic re-seed of a changing bundle exists to
  go stale.
- **BUG5 (dashd renders the tool list from a second renderer)** — fixed at
  `2ef91f65`. `dashd/tools_admin.go:34` calls `auth.EffectiveActions`, the same
  renderer `routd/agent_gate.go:72` uses for the agent socket. One renderer.

- **Status:** closed — the flip landed and satisfies all five
- **Fix:** `6f980437`, `2ef91f65`, `e10cfcc2`

## CEO audit — the connector capability has no concepts or howto page (2026-07-31, fixed 2026-09-22)

The one surviving item of the 2026-07-31 CEO audit. Re-verified 2026-09-21: the
ext-mcp connector injection (spec `5/13`) — the differentiator the pitch leans on
— still has no landing-page or concepts-page presence. `concepts/ext-mcp.html` and
`howto/add-connector.html` do not exist, `concepts/index.html`'s 27-entry
curriculum has no connector entry, and `template/web/pub/arizuko/index.html`
contains the word "connector" zero times. The capability is documented only in
`reference/mcp.html` (the `[[connector]]` TOML descriptor, credential injection,
grant gating), one sentence at `security/index.html:46`, and one at
`components/egress.html:109`.

**FIXED 2026-09-22.** `concepts/ext-mcp.html` states what a connector is, the
two transports (`mcp` discovers its catalogue and reads headers only; `rest`
declares its tools and may use all three sinks), the `{secret:KEY}` sinks and
the three things that happen to the value per call, the `ext:<service>:<op>`
grant gate, and three limits worth knowing — chiefly that the call leaves from
`routd`, so the folder's egress allowlist does not bound it.
`howto/add-connector.html` walks one REST connector end to end and ends on what
to check when it does not work. Both are wired into every concepts and howto
nav and into both index pages, and the landing page carries a connector bullet.
`tests/webdocs` passes, and it does catch a dangling link — proven by breaking
one. NOT deployed: the `cp` workflow in `template/web/CLAUDE.md` is the
operator's to run.

The other two items are closed:

- **Tier-language doc-debt — resolved.** The item was explicitly conditional ("NOT
  stale yet — tracked for the 5/33 rename pass"). The trigger fired
  (`AuthorizeStructural` has no definition in the tree; `container/runner.go:928`
  documents `ARIZUKO_TIER` as dropped per 5/33 decision 10) and the coordinated
  sweep it called for landed on 2026-08-05 in `7b3c41fe` (web) and `1bc1a967`
  (root docs). Swept again 2026-09-21: no web page, root UPPERCASE doc or `ant/`
  skill claims tiers gate anything today. The remaining "tier" hits are
  cold-tier/hot-tier (MCP surface categories), `strategy/index.html`'s Tier A-D
  roadmap buckets, third-party API tiers (Slack, Twitter), a CSS class, and dated
  changelog and migration-summary history.
- **README SDK-comparison pitch — moot.** `README.md:88-102` no longer holds a
  pitch of that shape; it is a "when to use / when not to" fit filter. There is
  nothing left to move onto the landing page.

- **Severity:** low (positioning, not code)
- **Scope:** `template/web/pub/arizuko/concepts/`, `howto/`, `index.html`

## ✅ CLOSED 2026-09-21 CTO audit — 5/33 partial cutover (2026-07-31)

A snapshot of a cutover that has since completed. C1+C2 were already fixed by the
revert (`3a8b8291`). The deferred real fix and all five MEDIUMs are resolved;
verified at `96cab2b6`.

- **The deferred `tierOf` backfill** — done, and better than filed. `6f980437`
  (2026-08-01, "capability grants as typed booleans") DELETED `tierOf` outright:
  `egress`, `web:publish` and the share mount are typed booleans routd resolves
  via `auth.Authorize` (`routd/dispatch.go:884-887`). The only survivor is
  `worldOf()` for `WEB_PREFIX`, a vhost coordinate, not authz
  (`container/runner.go:928`).
- **M3 (`folderGrantsFromACLOnly` drops ACL row Scope)** — subsystem gone. The
  function does not exist; `auth.Authorize` matches `r.Scope` against the real
  target before it branches on allow/deny (`auth/authorize.go:89`), so a scoped
  deny stays scoped.
- **M4 (`/root` gets a READ-ONLY share mount, inverted)** — fixed.
  `routd/dispatch.go:884` reads `shareRO := !elevated && l.db.Authorize(...)`, so
  an elevated turn short-circuits to a WRITABLE share before `Authorize` runs.
- **M5 (no un-assign path)** — fixed. `DELETE /v1/acl` at operator shape removes
  the membership edge (`routd/acl_resource.go` `revokeACLInto` →
  `RemoveMembershipInto(principal, "role:operator")`), guarded by
  `TestACLRESTOperatorRootOnly`.
- **M6 (`**`-scoped role rows bound delegation once `AuthorizeStructural` dies)** —
  the trigger fired and the replacement is in place. Delegation is bounded by
  `auth.Delegate` (subset-of-held + `grant_option`, `auth/delegate.go`) plus the
  Gate's own containment; writing a `**` scope itself requires root.
- **M1/BUG5 (dashd tools display off a second renderer)** — fixed at `2ef91f65`;
  see the 5/33 flip entry.

- **Status:** closed — nothing left to track
- **Fix:** `6f980437`, `2ef91f65`, `3a8b8291`

## ✅ FIXED 2026-09-21 P2 — dashd promised "linked accounts" and could not show them (2026-08-02)

Steps 1 and 2 of the identity-link collapse shipped (`90e46d62`; `fcd845cb` +
`54125cbd`): `oauth_identities` + `auth_users` is the single model,
`GET /v1/identities/{sub}` reads it, `inspect_identity` resolves through that
endpoint, and `identities`/`identity_claims`/`linked_to_sub`/`CanonicalSub`/
`LinkSubToCanonical`/`LinkedSubs` and dashd's `linkedSubs` are deleted. proxyd
does not read `CanonicalSub` at all. Re-verified 2026-09-21: none of those symbols
exist in the tree.

**Step 3 is what remains** — dashd consuming that endpoint — and it is an
addition, not a deletion. Today `dashd/main.go:740` advertises the profile tile as
"linked accounts" and `dashd/profile.go` shows none: it renders the caller's own
sub, then builds the "Add a provider" list from
`prefixes := map[string]bool{providerPrefix(sub): true}` — the CALLER'S OWN sub
prefix and nothing else. With four supported providers and at most one prefix
skipped, the page always offers at least three "Link X" buttons, so its
"All known providers already linked." branch is unreachable and a user who has
already linked GitHub is still invited to link GitHub.

**The decision, in one sentence:** dashd calls authd's
`GET /v1/identities/{sub}` for the profile page — which needs `identity:read`
added to `service:dashd` in `authd/http.go`'s `serviceGrants` (only `service:routd`
holds it today) and a choice of failure mode when authd is unreachable (render
nothing, or render the raw sub behind a banner) — or the tile stops saying "linked
accounts" and this drops from the queue.

- **Severity:** low (a page that under-promised would be fine; this one
  over-promises)
- **Scope:** `dashd/profile.go`, `dashd/main.go:740`
- **Status:** ✅ fixed — the promise is gone, the page is unchanged in what it shows
- **Fix:** steps 1-2 shipped at `90e46d62`, `fcd845cb`, `54125cbd`; the promise
  dropped 2026-09-21

- **Taken: the honest absence, which is the branch this entry offered
  ("the tile stops saying 'linked accounts' and this drops from the queue").**
  The portal tile reads `account and API keys`, which is both sections the page
  actually renders, and the unreachable `All known providers already linked.`
  branch is deleted along with the one-entry `prefixes` map that made it look
  reachable. `handleProfile` says in a comment what it knows (the caller's own
  sub) and what it does not (which providers resolve to the same account,
  authd's `GET /v1/identities/{sub}`), so the gap is written down where the
  next reader hits it.

- **NOT taken, and it needs no entry of its own to stay refusable.** dashd
  reading `GET /v1/identities/{sub}` is still a new cross-daemon read plus
  `identity:read` on `service:dashd` in `authd/http.go` serviceGrants (only
  `service:routd` holds it), plus a failure mode for an unreachable authd.
  That is a redesign and wants sign-off. Nothing in the tree now claims it
  happened, which is the whole point of closing this the other way.

- **PROVEN BY MUTATION:** put `linked accounts` back in the tile and
  `TestPortalProfileTilePromisesOnlyWhatProfileShows` fails; make the provider
  filter skip every provider and `TestProfile_ProviderListIsNeverEmpty` reports
  0 buttons for all five subs — which is the arithmetic that made the deleted
  branch unreachable, now pinned, so a filter that CAN empty the list has to
  supply the empty state it removes.

## ✅ CLOSED 2026-09-21 P3b — rejected minimization phases, recorded so they are not retried (2026-08-01, closed)

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

**Re-measured 2026-09-21 at `f5851f94`. Both rejections hold. The first one's
POINTER is dead and its REASONING was vindicated — corrected, not
re-litigated.**

There is no `onboarding.token` any more. `store/onboarding.go` declares the
admission item's whole attribute set — `status`, `user_sub`, `gate`, `created`,
`prompted_at`, `queued_at`, `admitted_at` — with the JID as the sort key and no
token among them. The pairing nonce moved to `route_tokens` under
`kind='pair'` (`store/pairing.go`, spec `5/31`), a 10-minute single-use
credential that commits in the same batch as the `acl_membership` edge it
authorizes.

So the nonce went to a THIRD home rather than into `invites`, which is the
rejection's own reasoning arriving by a route it did not name: an invite is
still a granter's scope and a pairing is still a proof of chat control, and
they still do not share a table. What the record must not do now is send a
reader hunting a column that is gone. The rule it states survives the move
intact — if a pairing credential and an invite ever share storage it is via an
explicit `kind`, never NULL-inference — and `route_tokens` is already built
that way, which is why it could hold the nonce at all.

The second rejection needs no correction. `attrOnbStatus` is still a stored
attribute and still carries four values, `refused` among them
(`store/onboarding.go`) — the fail-loud refusal outcome that made the status
less derivable from timestamps than it already was.

- **Status:** closed — recorded to prevent a repeat, no action wanted. Still
  useful: `onboarding` and `invites` are separate entities today
  (`store/entities.go`), so the merge this records the rejection of is still a
  thing somebody could propose.

## S1b — the helm chart deploys the deleted `gated` daemon (2026-08-05, PROPOSED — redesign, needs sign-off; RE-MEASURED 2026-09-21)

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

## ✅ CLOSED 2026-09-21 O2 — two adapters down on platform credentials, not code (2026-08-02)

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

**CLOSED 2026-09-21: both adapters already have their own entries, and the one
thing this entry added that they did not is now covered.**

The two credential faults are `L63` (krons whapd, needs the `--pair` re-auth)
and `L62` (marinade emaid, needs the IMAP password). Three entries for two
operator actions is how a queue reads bigger than it is; those two stay, this
one goes.

What O2 contributed beyond them is the MONITORING gap, and it was real:
`RestartCount=0` with status `health: starting` is equally down and much
quieter than a crash loop, so an adapter failing its healthcheck on a
credential looks like one that is still coming up — for weeks. Verified
2026-09-21 that no eval check mentioned `health: starting` or `unhealthy`.

Now check 23 in `.claude/skills/eval/checks.md`, with the routd-side
cross-check beside it: a `channel.deregister` count that tracks
`channel.register` is an adapter flapping, not an adapter working.

- **Status:** closed; the credential work lives in `L62` and `L63`
- **Fix:** eval check 23

## ✅ CLOSED 2026-09-21 A1 — the split is deployment-shaped only: auth data lives in the router's DB (2026-08-02, proposal — all three facts resolved, migrated or ADJUDICATED; the proposal was rejected by name in `5/16`)

**Closed 2026-09-21 at `f5851f94`. Do not apply the proposal below.** It was
put to `specs/5/16` as option A and REJECTED THERE BY NAME, with a cost; the
vocabulary its three facts are written in ("mounts every DB", "the router's
DB") no longer describes the system; and the two halves that were genuinely
wrong were fixed on the day this entry was filed. Taking each fact in turn,
because each closes for a different reason and only one of them closes as
"fixed".

**Fact 1 — "every daemon mounts every DB" — the DBs are gone, and both halves
of what replaced them are declared and checked.** `store.OwnerDBPath` no longer
exists. `store.OpenOwner(owner)` is `dynstore.FromEnv(dynstore.Prefix(owner))`
(`store/served.go`): there is one store server, the owner is a prefix in front
of every entity name, and no daemon opens a file another daemon owns because
there are no files. The concern did not dissolve in the move, it SPLIT, and
both halves landed:

- The filesystem half is `svcDef.dataSubdirs` → `dataMounts`
  (`compose/compose.go`), whose comment cites this entry: with no subdirs a
  daemon gets the whole instance tree, naming subdirs mounts only those
  children. dashd and onbod are narrowed — dashd to `groups` + `surrogate`,
  because the whole-tree mount handed it `.env` and the live per-turn agent MCP
  sockets. That is the "narrow the mount per daemon" the proposal asked for,
  built and shipped as a `5/16` step.
- The isolation half is `store/owner_reach.go` `StoreReach`, which declares
  daemon → owner prefixes, and `TestStoreReachMatchesTheCallSites`, which walks
  the module for the store doors and fails in BOTH directions. The residual —
  the 19 live IAM policies still carrying `dynamodb:* on *` — is `L318`, open,
  with the rollout as its only remaining work.

So fact 1 is tracked, in current vocabulary, in a newer entry. Keeping it here
too is two entries on one root cause.

**And its supporting claim about slakd is REFUTED.** This entry read
`slakd/main.go` opening routd as a write-discipline violation, since CLAUDE.md
names slakd an HTTP writer. It conflated opening with writing. `slakd/bot.go`
declares `paneStore` as "the READ subset of `*store.Store` slakd needs for pane
sessions", and the pane WRITE goes through routd (`SetPaneContext`) —
"routd routes the write; slakd never opens messages.db for pane writes".
CLAUDE.md's sentence is about writes and it is correct as written.

**Fact 2 — "authorization lives in the router" — still TRUE, and it is the
signed decision, not the defect.** `acl` and `acl_membership` are `OwnerRoutd`
(`store/scopes.go`) and proxyd reads them per request. `specs/5/16`
§"The auth decision" put exactly this entry's proposal as option A — it cites
`BUGS.md A1` and quotes its sentence — and rejected it: `acl`/`acl_membership`
are queried in-process, per-call, by the two highest-QPS authorization paths in
the system (`resreg`'s default `Gate` on every routd-mounted REST mutation, and
`db.Authorize` on every agent tool invocation of every turn), and moving the
rows to authd forces both onto a cross-daemon round trip. Option B — routd
stays the policy decision point — was chosen and shipped. `5/16` §"Not done in
this phase" states the terms for reopening: "Revisit only if a consumer emerges
whose need authd can serve more cheaply than routd's in-process path." No such
consumer has emerged; the one this entry named has gone the other way.

**Fact 3 — "`auth_users` is duplicated across two owner DBs" — RESOLVED,
exactly as this entry's own decomposition prescribed.** `auth_users` exists in
one place, authd's (`authd/authstore/auth_users.go`). The routd-side table was
renamed to `user_profiles` carrying precisely the two columns the decomposition
said to keep — `username` (the correction that caught the truncated-grep error)
and `cost_cap_cents_per_day` — at `store/user_profiles.go`. `hash` is deleted.
The latent split-brain cannot happen: there is one table.

**`auth_sessions` is gone outright, and with it the thing the proposal turned
on.** Zero references in the tree — no table, no `CreateAuthSession`, no
`copyLegacyProxydTables`. The proposal's hinge was "the one thing forcing a DB
read is the opaque session cookie; have authd's login set a JWT-bearing cookie
and proxyd's identity reads disappear entirely." That is what proxyd does now:
identity is an ES256 access JWT, with a `refresh_token` cookie exchanged at
authd's `POST /v1/refresh` (`proxyd/main.go`). Proxyd's remaining routd read is
not identity — it is `groupsForSub` → `auth.UserScopes`, the per-folder grant
patterns it stamps as `X-User-Groups`, which no token claim can carry and which
`5/16` recorded as the hot-path exception. The proposal conflates the two; at
HEAD they are visibly different reads.

Nothing below is actionable. The two sub-sections that follow are the
corrections this entry made to itself, both fully overtaken — the `auth_users`
decomposition is done, and `auth_sessions` is deleted rather than ordered.

- **Status:** closed — proposal rejected by `specs/5/16` with a stated cost;
  facts 1 and 3 resolved; fact 1's residual lives in `L318`
- **Fix:** none. `auth_sessions` drop and `auth_users` → `user_profiles` landed
  as `5/16` steps; the per-daemon mount narrowing is `compose/compose.go`
  `dataMounts`, which cites this ID.

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
- **`cost_cap_cents_per_day` is live but is not identity** — the column is a
  per-user spend cap and the same column also exists on `groups`. It belongs
  on a budget table keyed by sub, not on an identity table.

  *(The `store/cost_log.go:87,98` citation this bullet carried is dead:
  `cost_log` and its file were deleted when spec `6/39` made the gateway the
  ledger. Re-measured 2026-09-21, the column now lives at
  `store/user_profiles.go:61` and `store/groups.go:55`, read by
  `cmd/arizuko/budget.go`. The observation stands; only the pointer rotted.)*
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

## ✅ CLOSED 2026-09-21 R1 — codex review of the 2026-08-03 fix batch: six gaps (2026-08-03)

All six were re-verified SEPARATELY at `bcb5bb83` on 2026-08-15 and five closed
there; the sixth was "closed in code, live state unverifiable from a worktree".
Spot-checked again at `96cab2b6`:

- **5** — `cmd/arizuko/apply.go` `printPlan` names no command on the
  `SkipApplyRebuild` line, and its own comment cites this finding ("BUGS R1-5").
- **6** — `webd/Makefile` `test-e2e` captures the output and `exit 1`s on "no
  tests to run", with a comment naming the root Makefile's guard as its mirror.
  (Confirmed useful: a vacuous `-run` selector bit this pass too.)

**Item 1's residual is now two live entries, not this one.** The update path R1
demanded exists (`arizuko packages upgrade` / `sync`, both through one
`reapplyPackage`, `cmd/arizuko/packages.go:877`), and `template/services/teled.yml`
carries the `${DATA_DIR}/store/teled` state mount with no `CHANNEL_SECRET`. What
R1 could not check — whether an instance's installed fragment actually receives
that — is exactly the subject of `L41` ("compose fragments freeze forever once
they differ by a byte", which says in its own text that "`R1` already fixed this
class" and names the residue) and `L315` (`reapplyPackage` early-returns for a
catalog source). Both are open, newer and sharper.

- **Status:** closed — superseded by `L41` + `L315` for the live half
- **Fix:** see the 2026-08-15 verdicts

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

## ✅ CLOSED-STALE 2026-09-20 Z1 — all three live instances run a routd/onbod image ~5 days stale; a chunk of shipped migrations is not live (2026-08-04)

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

**CLOSED-STALE 2026-09-20: the subsystem this entry measures no longer
exists.** The defect was numbered SQL migrations shipping in the tree but not
in the running image — `routd` at version 19 against `routd/migrations/0027`.
The DynamoDB move (v0.68.0, 2026-09-16) removed that whole mechanism. Verified
at HEAD: `routd/migrations/`, `onbod/migrations/` and `store/migrations/` do
not exist, and no Go code reads a `migrations` table.

A stale image is still possible and still worth catching, but it is a
different failure now — no numbered migration can be "not live", because there
are none. The live surface for that question is the eval gates, not this
entry.

- **Status:** closed, subsystem gone
- **Fix:** none needed

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

## L136 — the `--internal` network does not seal UDP/53, and the two docs that explain the DNS filter's absence give a reason that does not cover DNS (2026-08-30, open — RE-MEASURED 2026-09-21: channel reproduced, but the TCP-vs-UDP framing is REFUTED; ONE question)

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
- **Status:** open — the DECISION is what is outstanding; the docs are true as
  of 2026-09-20. Either scope the guarantee to TCP and accept the channel, or
  add a UDP/53 filter and wire it into `container.Run` (nothing ever wired one).
- **Docs corrected 2026-09-20 (`ec3a933d`).** Five `SECURITY.md` claims carry a
  protocol now — the Network-egress table row (`:110`), the § intro (`:317`),
  the fail-closed bullet (`:329`) and the allowlist caveat (`:362`) all say TCP,
  and § "No DNS filter" states the 2026-08-30 measurement, why the proxy cannot
  subsume the control (a DNS query never reaches a CONNECT line), the residual
  channel and its cost, and both options WITHOUT taking one. The same reason had
  spread to `template/web/pub/arizuko/security/index.html` §4 and
  `components/egress.html`; both carry the corrected one.
  `research/index.html:118` listed DNS filtering as a live defence layer and now
  names the per-spawn egress proxy. `specs/6/24-crackbox-lightest.md:118-124`
  was already correct and is the source all of them were written from.
- **Fix:** docs `ec3a933d`; the decision itself is unmade

- **RE-MEASURED 2026-09-21 on this host at `b3b973e2`. The channel is still
  open, and the MECHANISM is not what either the entry or `SECURITY.md` says.**
  Probe: `docker network create --internal … --subnet 10.253.77.0/24`, one
  container on it from a local image, network removed after.
  - `/etc/resolv.conf` → `nameserver 127.0.0.11` (unchanged).
  - `getent hosts example.com` → `2606:4700:10::ac42:93f3`, exit 0 — the SAME
    address the 2026-08-30 measurement recorded. Reproduced exactly.
  - **`ip route` shows `default via 10.253.77.1 dev eth0`.** So "the spawn's
    network has no default route" (`SECURITY.md:110`) is false as literally
    read. The route is there; something else does the sealing.
  - **What actually seals it, read from the host:**
    `iptables -S DOCKER-ISOLATION-STAGE-1` carries, for the probe's bridge,
    `! -d 10.253.77.0/24 -i br-… -j DROP` and its `-s`/`-o` mirror.
- **That REFUTES the TCP-versus-UDP framing this entry and `SECURITY.md` now
  both use.** The DROP rule carries no `-p`, so it covers every FORWARDED
  protocol — a spawn's UDP to an arbitrary external address is dropped exactly
  as its TCP is. Nothing "sits outside it" by protocol. What escapes is one
  address: `127.0.0.11` is docker's embedded resolver, served inside the
  container's own netns and forwarded upstream from the HOST namespace, so its
  query never crosses this bridge and this rule never sees it.
- **Why that changes the decision rather than just the wording.** "Add a UDP/53
  filter and wire it into `container.Run`" cannot be an iptables rule on the
  spawn's bridge — that rule already exists and already drops UDP. The only
  places to filter are what the embedded resolver is allowed to forward, or
  replacing it (`--dns` at a resolver that holds the folder's allowlist). The
  residual channel is also narrower than "arbitrary UDP/53": the agent cannot
  choose a resolver, only the NAMES it asks docker's one for — which is still
  the NXDOMAIN side channel, and still not visible in any CONNECT line.
- **Unchanged and still true:** `container/network.go:159` creates the network
  `--internal`; the docs' § "No DNS filter" states the measurement and both
  options without taking one.

- **THE ONE QUESTION:** does the egress guarantee cover the NAMES a spawn can
  make docker's embedded resolver look up, or does it stop at the destinations
  the spawn can connect to?
  - Answer "names too" → the spawn gets `--dns` at a resolver that reads the
    folder's `network_rules`, and `SECURITY.md`'s protocol wording is replaced
    with the forwarded-versus-resolver distinction above.
  - Answer "destinations only" → the residual channel is accepted and written
    as accepted, and `SECURITY.md:110` stops naming an absent default route as
    the mechanism, because it is not the one doing the work.

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

## ✅ FIXED 2026-09-21 L182 — the sloth host clock is 18 s behind Discord, so every inbound `time=` is in the future of `now:` (2026-09-05, open — infra)

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
- **Status:** fixed (infra)
- **Fix:**

**FIXED 2026-09-21, and the entry understated it: the HOST clock had never
synchronised at all.** This was filed as sloth being 18 s behind Discord — a
one-instance annoyance. Measured, `chronyd` was running with stratum 0,
reference ID `00000000` and a ref time of the Unix epoch, and every server in
`2.debian.pool.ntp.org` sat at `Reach 0`. The clock was free-running, so every
timestamp on the box was wrong, on every instance.

Not a local firewall — `iptables -L OUTPUT` is ACCEPT and `nft` has no matching
rule. **The provider blocks outbound UDP/123 to public pools and allows its
own**, proven both ways:

```
$ sudo chronyd -Q -t 8 'server time.cloudflare.com iburst'   → Timeout reached
$ sudo chronyd -Q -t 8 'server ntp1.hetzner.de iburst'       → System clock wrong by 0.72s
```

`/etc/chrony/sources.d/hetzner.sources` now names ntp1/2/3.hetzner.de, with the
measurement recorded in the file so the next person does not re-diagnose it.
After `chronyc reload sources`: stratum 3, tracking ntp1.hetzner.de,
`NTPSynchronized=yes`, **0.000000135 seconds** off.

Worth knowing for anything that reads a timestamp: the skew was not constant,
so a stored `time=` written before today is not correctable by a fixed offset.

- **Status:** fixed (host config)
- **Fix:** `/etc/chrony/sources.d/hetzner.sources`

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

## ✅ FIXED 2026-09-21 L190 — `6/9` restated substrate facts that `substrate.Profile` declares; the prose is a pointer now (2026-09-05)

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
- **Scope:** `specs/6/9-crackbox-sandboxing.md`
- **Affected:** readers of the specs
- **Source:** `substrate/substrate.go`; `container/egress.go` Profile, Effective; `sandbox/sandbox.go` Profile
- **Status:** ✅ fixed — the one remaining restatement is a pointer
- **Fix:** `6/9`'s `AllowList` comment, §"Egress enforcement" and the third
  acceptance clause now name `sandbox.Profile.Egress` instead of restating a
  granularity

- **RE-MEASURED at `fdb59418` 2026-09-21. Two of the three sites were already
  pointers; only `6/9` still restated, and it restated the value WRONGLY.**

  **Already done, so the entry over-counted:** `6/16` §"The four layers" item 3
  reads "What a substrate can do is declared once, as a value: `substrate.Profile`
  … This spec restates none of it", and the package axis has no `dockbox` row —
  it has a `container` row reading "the Docker substrate; `container.Profile`",
  followed by "What either can do is read from those values (`6/25` §2), not from
  this table." `6/25` has no §Order; its §State reads "All three are adopted".
  Nothing to edit in either.

  **`6/9` was the live one, and it is the same line `L189` names.** Three sites
  restated the granularity and all three asserted the OPPOSITE of the
  declaration: the `VMConfig` comment said "host allowlist enforced by netfilter
  on the VM's tap", §"Egress enforcement" said enforcement IS netfilter rules,
  and the acceptance clause promised a spawn "reaches a listed host". The
  declaration says `EgressAddress` — "A host name in the allowlist is not a
  rule" (`substrate/substrate.go`).

  **The pointer is what keeps this a correction rather than `L189`'s decision.**
  Naming `sandbox.Profile.Egress` asserts nothing about host names; it delegates.
  Adopting `L189` (a) or (b) would move the declaration to `EgressDomain` and
  `6/9` would need no edit — which is the argument for the pointer over writing
  "address-only" into the prose, and why `L189`'s `specs/6/9` scope is discharged
  without taking its decision.

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

## ✅ FIXED 2026-09-21 L201 — nothing mechanical stops a sourced package from mounting a core owner's store or the docker socket (2026-09-05, FIXED 2026-09-21 at both writers of a sourced fragment; the `${KEY}` interpolation half is `L339`)

Contract rule 1: a package brings its own dependencies and arizuko's store is
untouched. `TestNoFragmentMountsTheWholeDataDir` and
`TestStatefulFragmentsMountTheirStateDir` enforce it for the shipped catalog
only; `packages install <source>` accepts any `volumes:` line, so a package
may bind `${DATA_DIR}/store/routd`, `${DATA_DIR}/.env`, `${DATA_DIR}/ipc` or
`/var/run/docker.sock` and install cleanly. Routes have the analogue already
(`compose.ReservedRouteClaim`, refused at install and again at generate).

Re-verified at HEAD 2026-09-21: `readFragmentAssets` hashes every `*.yml` and
`installPackage` writes it; nothing between them reads a byte of it. The
proposal's blacklist was too narrow to be the fix: `privileged: true`,
`pid: host`, a `driver_opts: {device: /var/run/docker.sock}` behind a volume
name, `extends: {file: ../docker-compose.yml, service: routd}`, a config
`file: ../.env` and `env_file: ['../env/routd.env']` all reach the same
places and none is a bind source; and "an owner store dir" leaves out
`store/whatsapp-auth` (whapd's session) and `groups/` (every agent's home).

**Fixed at `d1d857b9`.** `compose.CheckSourcedFragment` holds a fragment from
a source to its own name — a whitelist on the bind, not the blacklist above:
services named `<name>`, `<name>-*` or `<name>_*` (the catalog's own
practice: `litellm` + `litellm_db`); binds only `${DATA_DIR}/store/<name>` or
below it with `..` resolved, or a named volume; no `env_file` (generate
writes none for a package outside the release, `L200`, and every existing
one is another daemon's secrets); no `privileged`, `cap_add`, `devices`,
`pid`, `userns_mode`, `cgroup`, `volumes_from`, host network or IPC, or an
`unconfined` security_opt; no `build`, `extends` or `include`; no volume
`driver`/`driver_opts`; no config or secret read from a host `file:` or
`environment:`. An unreadable fragment is refused, not skipped. Both writers
of a sourced fragment call it before their first write: `installPackage`,
ahead of the env block that mints keys (a refusal after it left a
`generate:` key in `.env` for a package that never installed — the test
asserts the order), and `reapplyPackage`, the one updater, so `upgrade` dies
naming the line and `sync` reports and skips it the way it skips a dirty
asset. Install also refuses a core service's name and a file that would
replace one `services/` already holds — `install ./teled` overwrote the
catalog's link with no message. The catalog is exempt, the same bundled
exception `ReservedRouteClaim` makes; its fragments are held by the
`fragments_test.go` suite at build.

Generate is NOT the second reader here, unlike routes, and on purpose: the
fragment is what `upgrade` rewrites, so the updater is the second WRITER and
is where the check belongs; generate cannot read a linked fragment from the
deploy container at all (`L202`), and a real file in `services/` there is
the operator's own or a frozen release copy (`L41`) — a generate-time
refusal would turn a frozen copy into an outage on the next restart.

- **Severity:** medium (supply-chain: an installed package reads every
  owner DB and every secret)
- **Scope:** package install + upgrade + sync
- **Affected:** any sourced package
- **Source:** `compose/contain.go`; `cmd/arizuko/packages.go`
  `checkSourcedFiles`, called from `installPackage` and `reapplyPackage`;
  `compose/contain_test.go`, `cmd/arizuko/packages_contain_test.go`
- **Status:** fixed. What a sourced fragment can still read is `${KEY}` —
  docker interpolates every `.env` key into it, so `environment: {K:
'${SECRETS_KEY}'}` is a value the bound above does not see (`L339`).
- **Fix:** `d1d857b9`

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
