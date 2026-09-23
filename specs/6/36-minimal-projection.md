---
status: shipped
depends:
  [
    ../5/A-primitives-framing,
    ../5/17-openapi-mcp,
    ../5/33-paths-roles,
    ../9/2-data-model,
    16-daemon-standalone-matrix,
    22-proxyd-cut,
    28-cockpit-index,
  ]
---

# specs/6/36 — the minimal projection: four verbs, one coordinate, one transaction

A synthesis spec. It changes no behavior. It names the primitive set the
code rests on, maps every mechanism onto that set, and lists the cuts that
leave the system doing what it does now with less. Every claim of
redundancy cites the call path. Measured 2026-09-05 at `7752b524`:
83,217 non-test Go lines, 119,690 test lines, 3,458 TypeScript adapter
lines, 3,269 TypeScript agent-runtime lines, 21 daemons.

The maintainer's own sentence is the frame: _one standard for messages
getting in, one standard for messages getting out and actions, a mechanism
in between that meters and asserts and decides where a message goes and
which resources the agent may use; no connection that does not need to
exist; a single user gets a system that works with no configuration, an
enterprise configures ACL the way AWS lets it._ Read against the code, that
is two standards, four verbs and two nouns. The four verbs are exactly
`routd`'s four jobs. Nothing in the tree needs a fifth verb.

| Verb                 | The one site                                                                                                                                  | What it reads                                                    |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| **Ingest**           | `putMessage` — `routd/db.go:244`                                                                                                              | `apiv1.Message` — `routd/api/v1/types.go:13`                     |
| **Route**            | `Loop.resolve` — `routd/loop.go:682`                                                                                                          | `routes` rows, `chat_reply_state`, `route_tokens`                |
| **Grant**            | `auth.Authorize` — `auth/authorize.go:59`                                                                                                     | `acl` + `acl_membership` rows                                    |
| **Meter and assert** | `audit.Event` through `store.runAudited`; the spawn-time capability snapshot — `routd/dispatch.go:738-771`; `budgetGate` `routd/budget.go:11` | `audit_log` ×4, `cost_log`, spend caps, `network_rules`, `hold:` |

The two nouns: the **folder** is the coordinate — the agent's files, the
tenancy key, and the grant subject `folder:<f>` in one string
(`auth.FolderPrincipal`, `auth/authorize.go`; `groupfolder/folder.go:105`).
The **turn** is the transaction — the scope inside which Route has decided,
Grant has been snapshotted, and Meter has one `turn_id` to key on
(`turn_context`, `routd/db.go:768`; one blocking `POST /v1/runs`,
`runed/api/v1/client.go:113`).

Everything else in the tree is one of three things: a **service** the agent
reaches through Grant (model gateway, the folder over WebDAV, `/pub`, TTS,
the secret broker at `ipc/extcall.go:59`), the **install-time write face** of
the nouns (`packages`, `products`, `apply`, `archive`, compose generation),
or the **read face** of Meter (every `/dash/` page, all on one `theme.Page`,
`theme/theme.go:385`). [`5/A`](../5/A-primitives-framing.md)'s six-stage
pipeline is the same set in runtime order; this spec folds Agent into the
folder noun and State into the two durable stores 5/A already names.

## 1. Two standards, not one

The asymmetry is in the code and the projection keeps it. Ingest is a
**struct**; egress is an **interface**.

**In: one envelope, N normalizers.** Every source — ten adapters, webd,
timed, the agent's own `send`, a scheduled task, a silence trigger — ends as
one `apiv1.Message` (`routd/api/v1/types.go:13`) appended by one function
(`routd/db.go:244`). The envelope carries a `Verb` discriminator
(`message|like|dislike|edit|delete|join`, `types.go:26`), so a reaction, an
edit and a join arrive as the same shape with a tag, and routd enriches them
generically after the append (`routd/server.go:555-600`, `routd/enrich.go`).
This is EIP's Normalizer plus Canonical Data Model, and Cockburn's driving
side: the adapter calls routd.

**Out: one interface, typed acts.** Every act on a platform is one of the 14
methods of `chanlib.BotHandler` (`chanlib/handler.go:61-76`), each with its
own body (`SendRequest` … `UnpinRequest`, `handler.go:18-129`), plus the
`round_done` signal (`chanreg/httpchan.go:313` → `webd/channel.go:48`). The
outbound has two kinds and the code keeps them apart: a **message** out is a
`messages` row first (`FromMe`, `BotMsg`, `ipc/ipc.go:718`) and is retried
from the row (`PendingOutbound`, `routd/loop.go:416`); an **action** out
(like, edit, pin, delete) touches no row and writes an audit event instead
(`routd/social_audit.go:12`). This is Cockburn's driven side: routd calls
the adapter. The same process plays both roles, which is why one adapter mux
carries a POST to routd and 14 handlers from routd.

Where the code bends the asymmetry, each bend is a cut or a fix:

- `POST /v1/outbound` (`routd/server.go:634`, `deliver.SendVia`) sends with
  no `messages` row. It is the only outbound that skips the ledger; onbod's
  prompts and replies (`onbod/main.go:1396`) are therefore invisible to
  `find_messages` and to the transcript. A row-first send is the standard;
  this entry should append and then deliver like `ipc.go:718`.
- The 14-verb interface is declared five times (§2); one declaration with
  four readers is the shape.

## 2. Where each mechanism lands

The test for each row: is it the verb, a second declaration of the verb, or
a second owner of the verb? Only the middle kind is a cut.

| Mechanism                                               | Verb            | Anchor                                                                           | Verdict                                                                                                  |
| ------------------------------------------------------- | --------------- | -------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| 8 Go channel adapters                                   | Ingest          | `chanlib.Run` `chanlib/run.go:31`; `NewAdapterMux` `chanlib/handler.go:306`      | one renderer, eight platform clients (27–325 lines of `server.go` each)                                  |
| whapd, twitd (TypeScript)                               | Ingest          | `whapd/src/server.ts` 513, `twitd/src/server.ts` 373, two `dash.ts`              | **the same mux and dashboard twice more**: 1,223 lines re-typed (`chanlib/dash.go:13-15` says so)        |
| webd `web:` channel                                     | Ingest          | `webd/main.go:128` registers `web:`; own mux `webd/server.go:72`                 | a registered channel with its own mux; holds SSE state no adapter has                                    |
| `hook:` JIDs                                            | Ingest          | minted `routd/route_tokens_resource.go:267`; exempt `routd/server.go:849`        | a JID scheme with no registry owner; delivered by source name, `routd/deliver.go:55-64`                  |
| bare-folder JIDs (timed, tasks, steer)                  | Ingest          | `routd/loop.go:535`, `routd/steer.go:547`, `timed/split.go:260`                  | third internal scheme; the same `isChannelJID` covers all three                                          |
| `chanlib.InboundMsg` → `apiv1.Message`                  | Ingest          | posted `chanlib/chanlib.go:249`; decoded `routd/server.go:495`                   | **one wire type declared twice**, matched by JSON tag; `Attachments[].Size` is dropped on the wire       |
| `arizuko send`                                          | Ingest          | `cmd/arizuko/send.go:177` `store.PutMessage`                                     | **a second appender** beside routd, which `README` calls the sole appender                               |
| timed                                                   | Ingest          | `GET /v1/tasks/due` → `POST /v1/messages`, `timed/split.go:253-278`              | a clock that owns no state; every row it touches is routd's                                              |
| proactive interjection                                  | Ingest          | `routd/proactive.go:15-20`                                                       | a silence trigger, not a second clock                                                                    |
| ipc `send`/`reply`/delegate/escalate                    | Ingest          | `ipc/ipc.go:718`, `:1921`, `:1960`                                               | the agent as a source; same appender                                                                     |
| `POST /v1/outbound`                                     | Egress          | `routd/server.go:634`                                                            | the daemon-facing send; the only outbound with no `messages` row (§1)                                    |
| route table + engagement + sticky + observe             | Route           | `routd/loop.go:682-729`; `router/router.go:371`                                  | one resolver, layered overrides                                                                          |
| `route_tokens.target_folder`                            | Route           | read first in `directFolder`, `routd/loop.go:762-775`                            | a second JID→folder map, by design ([`5/W`](../5/W-webhook-routes.md))                                   |
| steer `@folder`, delegate, escalate                     | Route           | `routd/steer.go`; `ipc/ipc.go:1779`, `:1828`                                     | Route with a different trigger                                                                           |
| `proxyd_routes`, `web_routes`, `route_tokens`, `routes` | four primitives | `store/proxyd_routes.go`, `store/web_routes.go`, `store/route_tokens.go`         | four tables that share a word: HTTP ingress map, published-path grant, ingress credential, message route |
| `DB.Authorize` / `DB.AuthorizeTurn`                     | Grant           | `routd/sibling_db.go:274`, `:290`                                                | one evaluator, two identity sources (JWT sub; socket folder + trigger sender)                            |
| `auth.CheckHold`, `auth.ShellEnvKeys`                   | Grant           | `auth/authorize.go:603`, `:672` → `namespacedEffect` `:614`                      | the same rows and matchers, exact match rather than the lattice (Hazard 2 at `:617`)                     |
| `auth.HasScope`                                         | Grant           | `auth/scope.go:13`; `runed/server.go:81`, `authd/http.go:305`                    | a bearer's capability, not a principal's; daemons with no `acl` table                                    |
| `network_rules` + `egress/match`                        | Grant           | `routd/network.go:36` (own ancestry `:63`), `egress/match/match.go:73`           | a second predicate language over a different subject (host); see §8                                      |
| onbod gates, proxyd `auth:` attribute                   | Grant           | `onbod/main.go:312`; `compose/compose.go:364`                                    | admission before a principal has a folder; per-path level in a generic proxy                             |
| `web:publish`, `egress`, `mcp:share_mount`              | Grant           | `routd/dispatch.go:760-763`                                                      | ordinary `acl` actions, snapshotted at spawn                                                             |
| `audit_log` ×4                                          | Meter           | `audit/log.go`; `dashd/audit_page.go:58-61` reads owners over `/v1`              | one writer, one row shape, one file per owner                                                            |
| `cost_log`, `secret_use_log`, `task_run_logs`           | Meter           | `routd/db.go:1076`; `store/secret_use_log.go`; `store/tasks.go`                  | per-subject ledgers, one writer each                                                                     |
| spend caps                                              | Assert          | `routd/budget.go:11` `budgetGate`; `FolderCap`/`UserCap` `routd/db.go:1147`      | a per-folder and per-user ceiling read before dispatch                                                   |
| per-spawn egress proxy, hold gate, proxyd transit       | Assert          | `container/egress.go:34-153`; `routd/pending_actions.go`; `auth.ProxydTransit`   | the three enforcement points a message or call crosses                                                   |
| `resreg` catalog + mount                                | (surface)       | `resreg/engine.go:1332` `Register`, `:1372` `Mount`                              | one declaration, two readers at two times (`resreg/resources/names.go:5-14`)                             |
| `store.Store` vs `routd.DB`                             | (state)         | `store/*.go` 186 methods; `routd/db.go` + `sibling_db.go` 116 methods, 54 shared | **two data layers over one file**; the shared names carry different SQL (§7, S3)                         |
| dashd admin pages                                       | (read face)     | `dashd/routes_admin.go:278`, `tasks_admin.go:204`, `groups_admin.go:133`         | **raw SQL beside the resreg handlers** for routes, tasks, groups (§7, S1)                                |
| CLI grant/route/invite/secret/network                   | (write face)    | `cmd/arizuko/main.go:605`, `route.go:101`, `secret.go`, `network.go`             | a third writer through `store`; `resreg.Apply` already exists for the offline case (`apply.go:193`)      |
| onbod world creation                                    | Grant bootstrap | `onbod/main.go:923-940` raw `INSERT` into routd's `groups`, `acl`, `routes`      | a second group-create transaction beside `store.SeedMemberRoleTx` (`store/membership.go:118`)            |
| `sandbox/`                                              | (substrate)     | `runed/cmd/runed/main.go:102` wires `NewDockerRuntime` only                      | not in the turn path until [`6/41`](41-qemu-substrate.md) step 1 folds it into `runed/`                  |
| `proxycfg/caddy.go`                                     | (surface)       | `cmd/arizuko/routes.go:60-95` prints, never installs                             | a third reader of `proxyd_routes`, staged by [`6/22`](22-proxyd-cut.md)                                  |

Two rows deserve a sentence each. The 14-verb set is declared five times:
`BotHandler` (`chanlib/handler.go:61`), the adapter mux (`:314-327`), the
client (`chanreg/httpchan.go`), and the two TypeScript servers. The
per-adapter `caps_test.go` files exist to catch the drift a fivefold
declaration invites, and all eight Go adapters carry one. The
two TypeScript declarations (`whapd/src/client.ts:23`,
`twitd/src/client.ts:38`) carry none, so a cap they announce and do not
implement is caught by nothing. And `HasCap` is read only inside the deliverer
(`chanreg/httpchan.go`, 18 sites); the agent's tool list is channel-blind
(`ipc/ipc.go:1217` registers `send_file` for every turn), so a model learns
a channel's limits by failing a call.

## 3. The connections that need not exist

The maintainer's rule: if there does not need to be a connection, there
should not be one. The seam is `/v1` over HTTP plus the MCP socket. A
connection that is not the seam — a daemon opening another daemon's SQLite
file, or a package importing another package for one call — is either
load-bearing or incidental. The load-bearing ones, from
`go list -f '{{.Imports}}'`: adapters → routd (`/v1/messages`, one
registration); routd → adapters (the 14 verbs); routd → runed (`POST
/v1/runs`); every daemon → authd (`/v1/service-token`, `/v1/keys`); timed →
routd (`/v1/tasks/*`); proxyd → every backend. Everything below is not the
seam, ranked by what its removal deletes:

1. **dashd opens `routd.db`, `onbod.db` and `runed.db`** (`dashd/main.go:368-374`)
   and imports `container` (`SetupGroup`), `ipc` (`ListTools`), `compose`
   (`ReservedRoutePaths`) and `proactive` (`Parse`) for four page reads.
   [`6/28`](28-cockpit-index.md) already rules reads go over `/v1`. S1.
2. **proxyd opens `routd.db`** (`proxyd/main.go:930`) to derive a user's
   scopes from `acl` (`auth.UserScopes(s.stRoutd, …)`, `proxyd/main.go:795`)
   while routd serves the same derivation at `GET /v1/users/{sub}/scopes`
   (`routd/server.go:651`); and to read `proxyd_routes`, a resource proxyd
   owns whose table lives in routd's file — there is no `store/proxyd/`.
   The mount is what makes proxyd the "irreplaceable half" of
   [`6/22`](22-proxyd-cut.md); serving the scope over `/v1` or in the token
   is what would let an off-the-shelf proxy take the commodity half.
3. **webd opens `routd.db`** (`webd/main.go:66`) for groups, messages,
   `turn_results`, `route_tokens` and `audit_log`, while routd serves message
   reads on `/v1/messages/*` and route tokens through resreg, and the one
   thing webd needs live — the turn-closed push — already rides the channel
   protocol (`/v1/round_done`).
4. **onbod opens `routd.db`** and calls `container.SetupGroup`
   (`onbod/main.go:913-940`). S11.
5. **`container` imports `ipc`** (`container/runner.go:108-111`, `:255`
   `ipc.ServeMCP`) for an in-runner MCP server the split never takes
   (`runed/docker.go:64-65`: "no in-process ServeMCP"), and `router` for
   `UserContextXml`. The substrate links the whole tool surface for a branch
   that `ExternalMCP` skips.
6. **`store` imports `router`** (`store/groups.go:218`, `store/messages.go:695`):
   the schema library evaluates the route table to answer
   `JIDRoutableToFolder`, a Grant question ("may this folder reach this
   JID") answered with Route logic inside the data layer.
7. **proxyd and dashd import `compose`** (`proxyd/dash.go:68`,
   `dashd/services.go`) for `ReservedRouteClaim`: two runtime daemons compiled
   against the generator's reserved-path list. A column on `proxyd_routes`
   removes both imports.
8. **slakd imports `store`** (`slakd/bot.go:38-44`) to read pane sessions —
   the one adapter that opens routd's file. routd serves the write half
   (`POST /v1/pane`, `routd/server.go:687`) and no read half.
9. **routd imports `container`** (`WriteGroupsSnapshot`, `WriteTasksSnapshot`,
   `CopySession`, `routd/dispatch.go`) to write files into the group tree for
   the agent to read. A second reader in a different shape is allowed; routd
   knowing the container's filesystem layout is the coupling. runed holds
   the `RunRequest` and could write the same files.
10. **`cmd/arizuko` imports `routd` and `runed`** as packages (migrate,
    probe): the CLI links daemon internals it could reach over `/v1` or
    through `resreg.Apply`. S4.
11. **`auth` imports `theme`** (`auth/dashauth.go`): the evaluator library
    renders a login page.

Items 1–4 are the same finding four times: the routd file is opened by five
processes, so its ownership is a convention, not a boundary. Items 5–11 are
one-call imports whose removal is small and whose presence makes the parts
un-shippable alone ([`6/16`](16-daemon-standalone-matrix.md)'s toolbox test
fails on every one of them).

## 4. The turn is ephemeral — what that costs and what it buys

The maintainer's sentence leaves it implicit; the code makes it explicit.
`dispatchRun` resolves the session id, model, container config, egress
allowlist, decrypted env-profile secrets, `shell:env` secrets and the three
container-capability grants **before** the spawn and ships them frozen in
one `RunRequest` (`routd/dispatch.go:729-771`). Route decides before an
agent exists; Grant attaches to the folder because the folder is the only
thing that outlives the container.

Checked table by table, the machinery that exists only because the
container dies is smaller than it looks:

- **Disappears under a persistent agent:** `sessions` (the `--resume`
  pointer, `routd/db.go:980-1066`); `spawns` (`runed/db.go:193-341`); the
  every-turn `PERSONA.md` re-read (`routd/prompt.go:108-137`); the
  `new_session` system message and the `session` autocall
  (`routd/prompt.go:154-159`, `:233`); the async hold mode and its re-issue
  message (`routd/pending_actions.go:310-329`, `awaitVerdict` `:503-531`,
  which flips a row to async because the container's run TTL is shorter
  than a human); the run slot as a DB row (`runed/manager.go:22-33`).
- **Stays:** `circuit_breaker` (runed restarts, not container deaths);
  `turn_results` and `idempotency_keys` (at-least-once delivery);
  `pending_actions` in blocking mode; `chat_reply_state`, `group_watchers`,
  `chat_proactive`, `scheduled_tasks` (tenancy and clocks); the socat bridge
  (`container/runner.go:900`, an stdio adapter any out-of-process agent
  needs); the `new_day` message (the model has no clock in any topology).

Two of thirteen durable tables are pure ephemerality; two are half. What
ephemerality buys is larger than what it costs: a grant change is live at
the next spawn with no invalidation path, because mounts, networks and
secrets are re-resolved every turn; a credential never outlives a turn
(`SECURITY.md`; [`6/23`](23-llm-gateway.md)'s virtual key); and a turn's
grant snapshot is the audit's ground truth. A persistent agent would delete
two tables and one hold mode, then need a process supervisor, a restart on
every mount or network grant change, and a long-lived credential in the
container. The turn is essential, not incidental; the coordinator's
hypothesis is right in kind and wrong in size.

## 5. The permission model against IAM

The live row is `principal | action | scope | effect | params | predicate |
granted_by | granted_at | grant_option` (`store/acl.go:16-20`). Against
IAM's Principal / Action / Resource / Effect / Condition it is the same
tuple: `scope` is the Resource, a folder glob with `**`
(`auth/authorize.go:59` doc); `params` and `predicate` are the Condition —
`params` on the call's arguments, `predicate` on the caller's claims
(`predicateMatches`, `auth/authorize.go:406-409`); `effect` is deny-wins
(`:59`). `grant_option` is SQL's WITH GRANT OPTION and IAM has no clean
equivalent; `auth/delegate.go` checks that a delegated row is a subset of a
held one.

The three example rows go through one evaluator:

- `role:operator | * | ** | allow` — the `*` action covers every action
  through the lattice (`actionCovers`), the `**` scope every folder;
  `auth.Authorize` answers it like any other row. It is seeded with
  `grant_option` by migration `0022`.
- `role:member | mcp:reply | ** | allow` — the floor. Every folder is a
  member through `folder:<f>` → `role:member` (`store.SeedMemberRoleTx`,
  `store/membership.go:118`; migrations `0023`, `0030`), and a turn's Route
  axis expands through it (`auth/authorize.go` "TWO AXES").
- `folder:X | web_route.set | X | allow` — one narrow agent-granted
  capability, matched on the Identity axis inside `X`'s subtree
  (`routd/sibling_db.go:290` `AuthorizeTurn`).

Beside the evaluator there are side doors, and the question is which are
the same door twice:

- **`elevated`** (`routd/agent_gate.go:37-41`, `routd/dispatch.go:760-763`)
  answers `true` without reading a row. It is `sudo`: `cmdRoot` gates it on
  `IsOperator` (`routd/steer.go:225`), and it is the only path by which a
  person's authority reaches outside the turn's subtree. Load-bearing; the
  two-axis confinement would otherwise confine the operator too.
- **`IsOperator`** has two spellings (`routd/sibling_db.go:310-318`):
  membership in `role:operator`, or `admin` on `**`. One question, two
  answers; the second exists for a direct grant with no role edge.
- **Bearer scopes** are derived from the same rows (`auth.UserScopes`,
  `auth/authorize.go:239`) into a second vocabulary (`messages:write`
  against `mcp:reply`) and checked by `auth.HasScope` on daemons that own no
  `acl` table (`runed/server.go:81`, `authd/http.go:305`). This is IAM's
  session policy: a credential's ceiling, evaluated offline. One
  declaration, a second reader in a different shape; the mapping between
  the two vocabularies lives in one function.
- **`network_rules`**, **`onboarding_gates`** (`onbod/main.go:37-41`: kind,
  param, limit per day) and **proxyd's `auth:` level** are three coarse
  pre-gates on three other subjects (host, admission, path). None is the
  evaluator wearing a coat; egress is the one that could become rows.
- **`grant_option` is unreachable from two of three faces**: the live
  `add_acl` tool and `/v1/acl` write the row without it
  (`routd/acl_resource.go:290-312`), the MCP args carry no such field
  (`resreg/resources/acl.go:73-90`), and dashd's bootstrap insert omits it
  (`dashd/groups_admin.go:150`). Only `arizuko apply` mints a
  re-delegatable grant. The AWS-shaped half of the model is declared and
  not served (S6).

Verdict: one evaluator serves all three rows; the parallel paths are
different subjects, not copies, except the one that is missing — the
delegation column no live surface can set.

## 6. The gradient: zero configuration to enterprise

For each primitive: what a single user gets with no configuration, what
deep configuration looks like, and where the gradient breaks.

| Primitive      | Zero-configuration default                                                                                                                 | Deep configuration                                                                                              | Finding                                                                                                                                                                                    |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Ingest         | one adapter env token; `arizuko create` seeds group `main` (`cmd/arizuko/main.go:339`); the adapter registers itself (`chanlib/run.go:63`) | route tokens with link context, `hook:` sources, multi-account adapters, source pinning                         | none                                                                                                                                                                                       |
| Route          | a direct-address JID needs no row; `arizuko group add <jid> main` is one row                                                               | verbs, mention, observe, topic kinds, engagement, sticky, delegation depth-capped at 1                          | none                                                                                                                                                                                       |
| Grant          | `role:member` floor per folder (`SeedMemberRoleTx`); `role:operator \| * \| **` with `grant_option` (migration `0022`)                     | per-row scope glob, `params`, `predicate` on claims, holds (block / async / skip), `shell:env`, `grant_option`  | `grant_option` unreachable from every live face (§5); the enterprise half of delegation exists only in `apply`                                                                             |
| Meter / assert | `audit_log` and `cost_log` always on; egress **off** unless `EGRESS_IMAGE`; no holds; no caps                                              | OTLP export; per-folder allowlists with ancestry (`routd/network.go:63`); holds per tool; `FolderCap`/`UserCap` | `POST /v1/outbound` sends with no `messages` row (§1): a message leaves unmetered by the transcript                                                                                        |
| Folder         | `main`, seeded with skills and settings by `container.SetupGroup`                                                                          | hierarchy, products blended per kind (`container/blend.go`), per-group model and container config               | none                                                                                                                                                                                       |
| Turn           | Docker, default model, 20-minute run TTL                                                                                                   | per-group config, isolated tasks, the model gateway ([`6/23`](23-llm-gateway.md))                               | none                                                                                                                                                                                       |
| Identity       | one operator, addressed by chat JID; web login needs an OAuth provider configured in authd                                                 | five providers plus Dex; pairing (`5/31`); `predicate` on claims                                                | a chat JID and a web sub are two principals until paired (diary 2026-09-03, the Discord case): the single user pays a pairing step to be one person; the small-user test fails at identity |
| Admission      | none needed — the operator adds groups                                                                                                     | gates by kind, param and daily limit; invites with max-uses and expiry; the queue                               | [`5/A`](../5/A-primitives-framing.md) decision 6 puts onbod in the smallest deployment; a single user never calls it. The small-user test says optional (§9)                               |

Two findings survive the table: delegation is declared and not served
(enterprise fails), and identity is two strings for one person until a
pairing step (single user fails). Both are rows and a handler, not
architecture.

## 7. The slimming, ranked by lines removed against risk

| #   | Cut                                                                                                  | Removes                                             | Risk   |
| --- | ---------------------------------------------------------------------------------------------------- | --------------------------------------------------- | ------ |
| S1  | dashd's admin SQL becomes calls on routd's `/v1` resources                                           | ~1.5–2k lines; the `onbod.db` and `runed.db` mounts | medium |
| S2  | **taken** — the `/v1/turns` REST twin is gone (`3b94cf67`)                                           | 722 lines, 14 routes, their scope sets, tests       | —      |
| S3  | `routd.DB` forwards to `store.Store` for every shared name, as `DeleteGroup` already does            | ~500 lines of `routd/db.go`                         | medium |
| S4  | CLI runtime mutations become HTTP calls; `create`/`generate`/`migrate`/`archive` keep the filesystem | ~1k lines of `cmd/arizuko`                          | medium |
| S5  | one TypeScript `chanlib` shared by whapd and twitd, or twitd in Go                                   | ~600 lines shared; 1,318 if twitd moves to Go       | low    |
| S6  | exported `*Tx` writers in `store` for acl, membership, invites; the three copies call them           | ~120 lines; closes the `grant_option` gap           | low    |
| S7  | mount the `invites` resource on the agent socket; delete the two hand-rolled ipc tools               | ~90 lines                                           | low    |
| S8  | timed becomes a routd goroutine                                                                      | ~400 lines, one daemon, one service token           | medium |
| S9  | `chanlib` imports `apiv1.Message`; `InboundMsg` goes                                                 | ~40 lines; restores `Size` on the wire              | low    |
| S10 | one verb table drives the adapter mux and the client                                                 | ~150 lines and the reason for `caps_test.go`        | low    |
| S11 | onbod's world creation calls routd's group create                                                    | ~60 lines; one group-create transaction             | medium |
| S12 | `sandbox/`, runed's `mcp_tokens` table, the `content/` `project/` `sidecar/` directories             | 205 lines, one dead table, three stray dirs         | low    |
| S13 | proxyd reads scopes from `/v1/users/{sub}/scopes`; `container` drops its `ipc` branch                | two of the §3 couplings; the rest follow S1 and S4  | low    |

**S1 — dashd as a client.** [`6/28`](28-cockpit-index.md) §"Read-path"
already rules that a dashboard reads another daemon only over `/v1`, and
[`6/29`](29-dashd-hub.md) leaves dashd no per-daemon pages. The writes
stay direct "by rule" because `store.AsUser` (`store/store.go:44`) is how the
operator's sub reaches the audit row and `bearerCall` carries only dashd's
service bearer. That reason does not hold: `webd/routes_mcp.go:135-174`
forwards `X-User-*` under webd's own `service:webd` bearer and proxyd's gate
runs `auth.Authorize` as that user — the identity bridge exists and is in
production for one resource. What breaks: `groups` has no create/delete on
`/v1` (`resreg/resources/groups.go:32-36` says creation stays dashd's
`SetupGroup`), so S1 needs a `groups.create` handler in routd first, which
S11 also needs; `/dash/me/secrets` and `/dash/me/env` keep their direct read,
the exception `6/28` names. What it fixes on the way:
`dashd/routes_admin.go:278` deletes a route with no resreg gate, tx or
audit; `dashd/tasks_admin.go:204-306` writes `scheduled_tasks` with a plain
`Exec` and a separate audit emit, so a crash between them loses the audit
row.

**S2 — taken.** Root `CLAUDE.md` rules that hot-tier actions have no REST
twin, and `/v1/turns/` is registered nowhere in the tree. `routd/turns.go` is
244 lines carrying the turn's own outbound lifecycle — `appendAndDeliver`
(`:55`) renders what `submit_turn` returns, and the `<status>` blocks, into
`messages` rows — while `ipc/ipc.go:718` stays the single append-and-deliver
chain for the agent's write tools. [`5/E`](../5/E-routd.md) §Turn, `5/Z`,
[`5/P`](../5/P-runed.md), `routd/README.md` and the `components/routd.html`
page still describe the twin: that is the drift this cut leaves behind.

**S3 — two data layers over `routd.db`.** 54 method names exist on both
`routd.DB` and `store.Store`, and the bodies differ: `routd/db.go:115`
`PutGroup` merges `config` with `json_set` while `store/groups.go:21`
replaces it; `routd/db.go:170` `AllGroups` reads four columns while
`store/groups.go:148` also reads `open` and the observe window, so every
caller of `s.db.AllGroups()` (`routd/groups_resource.go:60`,
`routd/steer.go:163`, `routd/dispatch.go:343`) sees zeroed fields. The
repository's own fix is in the file: `routd/db.go:143-149` forwards
`DeleteGroup` to `store` with the comment "routd having its own copy is what
let the CLI ship a delete with no cascade at all". S3 finishes that. What
breaks: the `config` merge choice is a behavior decision, and routd's
message-cursor readers (`NewMessages(since string)`, `routd/db.go:296`)
carry routd-only semantics that stay.

**S4 — the CLI as a client.** `cmd/arizuko/talk.go:1-30` is the shape:
"an HTTP CLIENT and nothing else … imports no `store`". Nine files write
through `store` instead (`main.go` grant, `route.go`, `secret.go`,
`network.go`, `token.go`, `packages.go`, `apply.go`, `skills.go`,
`migrate_split.go`), and `send.go:177` appends a `messages` row that routd
never sees ingest. Keep the filesystem-time verbs — `create` seeds owner
DBs before any daemon runs (`main.go:369`), `archive apply --stopped` and
`apply` run against a stopped instance through `resreg.Apply`
(`apply.go:193`, the same engine routd's handlers use). Move every
runtime mutation to `/v1`. What breaks: an operator whose routd is down
loses `arizuko grant` until it is up; `apply` covers that case with the
engine, not with a second SQL literal.

**S5 — the TypeScript copies.** whapd needs Baileys, which is TypeScript;
twitd's only platform dependency is `agent-twitter-client`
(`twitd/package.json`). The redundancy is not Go-versus-TypeScript; it is
whapd-versus-twitd: two copies of `server.ts`, `dash.ts`, `auth.ts`,
`dedup.ts`, `typing.ts`, `client.ts`. One shared TypeScript module removes
one copy; a Go twitd removes both TypeScript and one Dockerfile at the cost
of re-implementing the scraper client.

**S6 — the store's self-opening transactions.** `store.AddACLRow`
(`store/acl.go:16`) writes `grant_option`; `routd/acl_resource.go:290`
`grantACLTx` re-types the INSERT without it, and `resreg/resources/acl.go:73`
exposes no `grant_option` argument, so the live `add_acl` tool and
`/v1/acl` mint rows that can never re-delegate — [`5/33`](../5/33-paths-roles.md)'s
WITH GRANT OPTION is reachable only through `arizuko apply`. The membership
cycle check exists three times (`store/membership.go:24`, `:145` unexported,
`routd/acl_resource.go:343`), and `store/invites.go:105` has a twin at
`onbod/invites_resource.go:118`. `store/membership.go:126-144` already shows
the right split (`PutMembership` over `putMembershipTx`); S6 applies it to
acl and invites and exports the `Tx` half. Adding `grant_option` to the
collapsed write is a capability change and needs sign-off.

**S7 — invites' missing agent face.** `ipc/ipc.go:1985-2035` hand-rolls
`invite_create`/`invite_revoke` two lines under the comment that moved
`add_acl` off that shape (`:1978-1982`). The catalog row exists
(`resreg/resources/invites.go`) and onbod mounts the REST face
(`onbod/invites_resource.go:39`); only the socket mount is missing. Tool
names and args must stay byte-identical for the live agents.

**S8 — timed.** `timed/main.go` is 58 lines; the daemon owns no table, opens
no file, and calls four routd endpoints on a 60-second tick
(`timed/split.go:94`, `:249-278`). [`5/A`](../5/A-primitives-framing.md)
decision 6 puts it in the smallest real deployment; [`6/16`](16-daemon-standalone-matrix.md)
line 252 calls it optional. If it is core, its daemon boundary buys one
cockpit page and costs a service token, a compose service, a heartbeat, and
a fourth `audit_log`. The two specs contradict each other and the answer
decides S8.

**S9 — one wire type.** `routd/api/v1/types.go` imports nothing and routd
already imports `chanlib`, so `chanlib` importing `apiv1` creates no cycle;
`timed/split.go` and `dashd/engagement_page.go` already import it for the
same purpose. The field diff is the cost of the copy: `ForwardedFrom` and
the flat whapd attachment fields exist only on the wire type,
`InboundAttachment.Size` only on the adapter type, and `core/types.go:34`
comments the stored `Attachments` column as `[]chanlib.InboundAttachment`
while `routd/routes_http.go:31` marshals `[]apiv1.Attachment` into it.

**S11 — one group-create transaction.** `onbod/main.go:913-940` inserts into
routd's `groups`, `acl` and `routes` with raw SQL inside one transaction,
and the `acl` insert bypasses the audited `AddACLRow`. `store.SeedMemberRoleTx`
(`store/membership.go:118`) is the seed routd and dashd use. The multi-table
transaction is the invariant (a group without its member edge expands to
nothing); it must stay one transaction, which is why S11 depends on a
`groups.create` handler that seeds inside its own tx, not on three REST calls.

**S13 — the small couplings.** proxyd calls `GET /v1/users/{sub}/scopes`
(`routd/server.go:651`) in place of `auth.UserScopes` over its own handle to
`routd.db` (`proxyd/main.go:795`); `container/runner.go` drops the
`ServeMCP` branch the split never takes (`:108-111`, `:255`) and with it the
`ipc` import. Each is one call site. The remaining §3 items ride S1 (dashd's
four imports), S4 (the CLI's daemon imports) and S11 (onbod's mount).

## 8. What must stay plural

- **The two standards.** A struct in, an interface out (§1). A symmetric
  "connector" primitive would re-create the REST twin this spec deletes.
- **One evaluator, two questions.** `Authorize` walks the action lattice;
  `CheckHold` and `ShellEnvKeys` match exactly, through `namespacedEffect`.
  Routing a hold through the lattice holds every tool for the `**` holder
  (`auth/authorize.go:617-623`). Same rows, two entry points, on purpose.
- **A principal's grant and a bearer's scope.** `HasScope` limits what a
  credential may do wherever it lands; `acl` limits what a person may do.
  A leaked token must carry less than its owner. Two trust boundaries,
  never merged (`resreg/resreg.go:82-92`).
- **The operator gate and the turn gate.** Same `auth.Authorize`, two
  callers: the JWT sub is the whole "who" on REST; the socket folder plus
  the trigger sender are the two axes on MCP (`routd/sibling_db.go:274-310`).
- **`elevated`.** The one path outside the subtree, gated on `IsOperator`;
  removing it makes the operator subject to the confinement built for
  everyone else (§5).
- **Egress as its own language — for now.** `network_rules` answers "which
  host" over a folder, with host-glob validation at write
  (`store/network.go:29`) and ancestry resolution at spawn. It is a second
  format for a grant, and root `CLAUDE.md` allows a second reader but not a
  second format. Folding it into `acl` as `net:<host>` rows is a
  [`5/33`](../5/33-paths-roles.md) decision, and the `grantableAction` door
  (`cmd/arizuko/main.go:584-589`) shows the cost of a namespace nobody
  opened.
- **Per-owner databases and per-owner `audit_log`.** Ownership is a mount
  boundary for authd and runed. It is not one for routd: dashd, proxyd,
  webd, onbod and the CLI open `routd.db` (§3). S1, S4, S11 and S13 are what
  make the routd boundary real; the four files stay four.
- **Compose fragment and manifest per package.** Two files because two
  owners define the formats (`template/services/<name>.yml` and `.yaml`);
  root `CLAUDE.md` already rules it.
- **Nine platform clients on one renderer.** The clients are nine APIs.
  The cut is the copy between the two TypeScript ones, not the count.
- **Blocking and async holds.** Two times — the container's TTL and a
  human's — so two modes while the turn is ephemeral (§4).
- **The spawn-time snapshot and the per-call gate.** Mounts and networks
  are Docker facts fixed at spawn; tool calls are gated per call
  (`routd/agent_gate.go:37`). Two times, one evaluator.
- **The four "route" tables.** Four primitives; they should stop sharing a
  word, not a schema.

## 9. The daemon count

Twenty-two on disk: `authd routd runed onbod dashd proxyd webd timed ttsd`,
ten adapters, `egress` (one per spawn), `davd`, `vited`, and `litellm` when
configured. The projection:

- **Stay, as core:** `authd` (keys, sessions, five login providers),
  `routd` (the four verbs), `runed` (the turn substrate), `proxyd` until
  [`6/22`](22-proxyd-cut.md) lands its off-the-shelf half, `egress`.
- **Stay, as clients of the core:** `dashd` (after S1 it opens no database
  and ships alone, which [`6/16`](16-daemon-standalone-matrix.md) wants),
  `webd` (a channel plus the app surface), the ten adapters, `ttsd`,
  `davd`, `vited`, `litellm` — services with no arizuko state.
- **Fold candidates:** `timed` (S8). `onbod` after S11 and S4: admission is
  a Grant bootstrap (`matchGate`), an invite is an ingress credential like a
  route token, and its OAuth pages redirect to authd
  (`onbod/main.go:610`, `:1162`). What remains after those is `invites`,
  `invite_redemptions`, `onboarding_gates` and a queue — a resource set,
  which is how [`5/A`](../5/A-primitives-framing.md) decision 6 names it
  ("admission"), not a process. Folding it is a decision on ownership of
  `onbod.db`, not a code cut, and this spec does not make it.

Nineteen is the floor this reading supports without a decision on onbod;
eighteen with it. The adapters are the count and they are correct.

## 10. Sequencing

- **Independent, now:** S9 (one wire type), S6 (the `Tx` split), S7
  (invites' socket mount), S10 (with S2 taken, the verb table has one fewer
  reader), S12 (dead code), S13 (two call sites), and the `/v1/outbound`
  row-first fix (§1).
- **S3 before S1 and S4:** the surviving data layer must be decided before
  two writers move onto it.
- **`groups.create` on `/v1` before S1 and S11:** both need the one
  transaction routd owns.
- **S1 before the onbod decision:** it proves the identity bridge carries
  an operator's audit actor over REST for every resource, which is the
  precondition for any daemon to give up its direct write.
- **S8 after 5/A decision 6 and 6/16 line 252 agree.**
- **Egress into `acl` after a 5/33 decision;** it touches the
  `grantableAction` door and the 0023 migration's lesson.

## 11. Premises checked against the tree

- `AuthorizeTurn` is at `routd/sibling_db.go:290`; `HoldPrefix` at
  `auth/authorize.go:547`, the evaluator `CheckHold` at `:603`.
- webd does register with chanreg (`webd/main.go:128`, prefix `web:`).
  `hook:` is the unregistered scheme; its replies resolve by the row's
  source name (`routd/deliver.go:55-64`), never by prefix ownership.
- `HasCap` lives in the deliverer (`chanreg/httpchan.go`, 18 sites), not in
  routd's loop; the tool list never reads it.
- Eight adapter dashboards are one renderer (`chanlib/dash.go:63`); the two
  TypeScript adapters carry copies.
- Tenancy rides the JID through `routes`, and also through
  `route_tokens.target_folder`, which `directFolder` reads first.
- The tree holds no four grant evaluators: one lattice evaluator, one
  exact-match body over the same rows, and two checks on different subjects.
- The `acl` row is IAM's tuple plus `grant_option`, and one evaluator
  serves the operator row, the member floor and a narrow agent grant (§5).
- `mcp_tokens` (runed) has no reader or writer outside its migration.
- webd registers once with no heartbeat (`chanlib/run.go:69-88` re-registers
  every three minutes; `webd/main.go:120-128` does not), so three failed
  health probes deregister the `web` channel until a restart. Not in
  `BUGS.md`; belongs there.
