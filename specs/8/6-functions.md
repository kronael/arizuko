---
status: draft
depends: specs/5/W-webhook-routes.md, specs/5/I-tool-call-logging.md, specs/7/F-audit-stream.md, specs/5/5-uniform-mcp-rest.md, specs/8/3-git-as-truth.md, specs/5/36-yaml-manifests.md
---

# specs/8/6 — functions: agent-authored lambda primitive

## Why

Persistent services cost RAM, PIDs, and fds whether or not they
receive traffic. A platform that wants to host many tenant features
in one host pays that cost N times. Functions invert it: **N idle
functions cost ~0; cold start is per-invocation**.

Three properties stack:

1. **Density.** Per-folder cgroup slice + per-invocation cgroup unit
   bound total spend. Idle functions are filesystem rows. Active
   functions are short-lived transient units the kernel cleans up
   for us.
2. **Boring substrate.** `systemd-run --transient` is documented,
   well-trodden, kernel-enforced. journald captures stderr for free.
   No new runtime, no warm-pool scheduler, no language SDK.
3. **Agent-extensibility.** Agents author shell scripts in their own
   persistent folder; the platform routes webhooks, cron fires, and
   chain triggers to those scripts. The agent literally programs
   platform extensions — the killer property of the functions plane.

Functions are orthogonal to the persistent agent container. The
container stays as the LLM's runtime; functions are the transient
plane around it.

## What this is NOT

- A generic FaaS runtime. No language SDKs, packaging artifacts
  (zip / layers), warm-pool scheduler, or auto-scaling.
- A replacement for the persistent agent container.
- Kubernetes, serverless framework, service mesh.
- Container-per-invocation in v1 (`kind: container-function`
  parked in open Q 2).
- A high-QPS data-plane substrate. v1 is a control-plane primitive
  for webhooks, cron jobs, and agent-orchestrated workflows.
  Operating envelope: **≤10 invocations/sec/folder**. Above that,
  systemd-run setup overhead (~30–80ms per spawn) dominates and the
  density story breaks. Operators who need higher rates run a
  persistent adapter daemon instead.

## The shape

`functions` is a top-level YAML manifest key per
[`5/36`](../5/36-yaml-manifests.md)'s flat resource namespace, dispatched
through resreg owned by `gated`.

```yaml
functions:
  - name: build-fn
    folder: corp/eng
    trigger: webhook:prod-events # webhook:<token-name> | cron:<expr> | fn:<other> | manual
    command: ./functions/build-fn.sh # relative to group folder
    stdin: payload # payload | none | envelope
    response_type: application/json # webhook-only; default application/octet-stream
    timeout_s: 60
    memory_mb: 256
    max_concurrent: 4
    stdout_max_b: 1048576
```

Backing table owned by `gated`:

```sql
CREATE TABLE functions (
  name           TEXT NOT NULL,
  folder         TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
  trigger        TEXT NOT NULL,
  command        TEXT NOT NULL,
  stdin_mode     TEXT NOT NULL DEFAULT 'payload',
  response_type  TEXT NOT NULL DEFAULT 'application/octet-stream',
  timeout_s      INTEGER NOT NULL DEFAULT 60,
  memory_mb      INTEGER NOT NULL DEFAULT 256,
  max_concurrent INTEGER NOT NULL DEFAULT 1,
  stdout_max_b   INTEGER NOT NULL DEFAULT 1048576,
  command_sha256 TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  PRIMARY KEY (folder, name)
);
```

`command_sha256` is the hash of the script file at create/update
time; recorded at the cold-tier boundary per `7/5`'s Markdown-hashing
discipline. Drift between hash and on-disk file fails invocation
fast (see "Cold-tier integrity").

Cold-tier resource. `state: absent` deletes; group deletion cascades.

## Trigger taxonomy (v1)

Four trigger kinds. Each declares one trigger string in the manifest;
the spawner dispatches uniformly.

1. **`webhook:<token-name>`** — bound to a route_token (see
   [`5/W`](../5/W-webhook-routes.md), amended below) with
   `kind=function`. On `POST /hook/<token>`, webd looks up the row,
   sees `kind=function`, RPCs gated's `spawn_function(folder, name,
body, headers)`; HTTP response = function stdout with `Content-Type:
<response_type>`.

2. **`cron:<expr>`** — bound to a `scheduled_tasks` row with
   `kind=function`. When `timed` fires, it RPCs `spawn_function`
   instead of injecting a prompt message.

3. **`fn:<other-fn>`** — function chain. On upstream `fn.complete`,
   gated's chain subscriber spawns this one with the upstream's
   stdout piped as stdin. Depth-capped at 8 hops (see Operator failure
   modes below).

4. **`manual`** — MCP `functions.invoke name=<fn> args=...`. REST
   mirror `POST /v1/functions/{name}:invoke`. Agent-initiated for
   testing or workflow composition.

**Apply-time referential validation.** `functions.create` /
`functions.update` validates that webhook and cron trigger strings
resolve to companion resources in the same `arizuko apply` run
(class 3, with `route_tokens` and `scheduled_tasks`). `fn:<other>`
references are validated to exist within the same folder. Unresolved
references reject the create. Manifest-order applies within the
class let operators declare token + function in one file.

## Execution model

**Spawner lives on the host.** `gated` runs inside docker; a process
inside a container cannot reliably shell out to `systemd-run --user`
on the host (no DBus, no XDG_RUNTIME_DIR, no matching uid). v1 ships
a thin host-resident helper `fnspd` (`fnspd/main.go`) that:

- runs as a systemd user unit under the instance host uid (the uid
  that owns `/srv/data/arizuko_<inst>/`);
- exposes a unix-domain socket at
  `/srv/data/arizuko_<inst>/ipc/fnspd.sock`, peercred-gated to the
  gated container's uid;
- accepts `Spawn{folder, name, stdin_mode, command_path, env,
timeout_s, memory_mb, callid}` JSON-RPC calls;
- shells out to `systemd-run --user` per the unit template below;
- streams stdout/stderr/exit back to the caller.

The socket is bind-mounted into `gated`'s container. No HTTP gate —
peercred is the trust boundary, same shape as the existing MCP
socket.

`fnspd` is a system-core daemon ([CLAUDE.md "core vs integrations"])
shipped alongside `gated`. Its only responsibility is spawning;
it never reads SQLite, never holds business state.

systemd-run invocation:

```
systemd-run --user \
  --transient --pipe --collect --wait \
  --slice=arizuko-<instance>-<folder-slug>.slice \
  --unit=fn-<name>-<callid> \
  --property=RuntimeMaxSec=<timeout_s> \
  --property=MemoryMax=<memory_mb>M \
  --setenv=ARIZUKO_FN_NAME=<name> \
  --setenv=ARIZUKO_FN_TRIGGER=<trigger> \
  --setenv=ARIZUKO_FN_FOLDER=<folder> \
  --setenv=ARIZUKO_FN_CALL_ID=<callid> \
  --setenv=ARIZUKO_FN_CHAIN_DEPTH=<depth> \
  --setenv=ARIZUKO_FN_UPSTREAM_CALL=<callid_or_empty> \
  -- <command-path>
```

`--wait --pipe` synchronously runs and captures stdout. `--collect`
removes the unit on exit. The instance install creates the host
slice file per folder at group registration (see Substrate).

## Substrate: per-folder cgroup slice (v1 release-blocker)

Density is a property of the slice, not the unit. Without per-folder
slices, function spend is unbounded by the platform and the density
pitch is hollow. v1 ships the slice substrate:

- `install/systemd/user/arizuko-<instance>-<folder-slug>.slice` —
  template installed at instance creation, materialized per folder
  at group registration (gated calls `systemctl --user daemon-reload`
  and starts the slice via fnspd).
- Defaults: `MemoryMax=2G`, `CPUWeight=100` per folder; overridable
  per-instance via `.env` and per-folder via the `groups` resource
  (open: `groups` row needs `memory_mb` / `cpu_weight` columns — see
  open Q 1).
- Slice file install + per-folder materialization happens at `arizuko
apply` time for the `groups` resource (class 1), before `functions`
  rows (class 3) can land.

Per-unit caps (`MemoryMax`, `RuntimeMaxSec`) constrain individual
invocations; slice caps constrain total folder spend. The kernel
enforces both — the slice is the only mechanism that bounds
noisy-neighbor behavior between folders on the same host.

If for any reason the slice substrate is unavailable on a host (older
systemd, restricted environments), the instance refuses to materialize
`functions` rows. No silent fallback to per-unit-only — that breaks
the density contract.

## Stdin/stdout contract

Three stdin modes, declared per-function at create time. No magic
auto-detection.

- **`payload`** (default) — raw bytes of the trigger input:
  - `webhook`: exact HTTP request body, byte-for-byte. webd MUST pass
    bytes through to fnspd unchanged (no charset normalization, no
    content-encoding decode). Body limit per 5/W (1 MiB,
    env-configurable).
  - `cron`: empty.
  - `fn:<other>`: upstream stdout, byte-for-byte.
  - `manual`: `args.stdin` bytes (base64-decoded if from MCP).
- **`none`** — `/dev/null`. Trigger metadata in env vars only.
- **`envelope`** — JSON envelope on stdin, uniform across all four
  trigger kinds:
  ```json
  {
    "trigger": "webhook|cron|fn|manual",
    "headers": {"x-github-event": "push"},   // webhook only
    "method": "POST",                         // webhook only
    "body_b64": "<base64 of raw body>",       // all kinds; "" when none
    "upstream_call_id": "<fn callid>",        // fn only
    "args": {...}                             // manual only
  }
  ```
  Binary-safe via base64. The fourth mode oracle round 1 flagged
  was the same as envelope-everywhere — adopted.

Stdout: captured to a bounded buffer (`stdout_max_b`, default 1 MiB).

- `webhook`: stdout = HTTP response body, `Content-Type: <response_type>`.
- `cron` / `fn` / `manual`: stdout captured into the `fn.complete`
  audit row's `params_summary` (truncated to ~1 KB, full bytes
  available in fnspd's journald capture for the unit name).

Stderr: streamed to journald via systemd-run's default capture.
Operator inspects via `journalctl --user-unit=fn-<name>-<callid>`.

Exit code: 0 = `fn.complete`, non-zero = `fn.error`, SIGTERM
on `RuntimeMaxSec` = `fn.timeout`.

## Persistence (filesystem layout)

Agent writes scripts to its persistent folder:

```
/srv/data/arizuko_<inst>/groups/<folder>/functions/
  <name>.sh          # the executable command
  <name>.README.md   # optional, agent-authored
```

Flat. One file per function. Multi-file functions bundle to one
script (vendored deps, bash with heredocs, or `pyinstaller`-style
single-file). Directory-per-fn deferred (open Q 6).

Host-side `fnspd` reads from the same path. Survives container
exits, redeploys, and worktree forks ([`8/3`](3-git-as-truth.md)
fork lifecycle).

Permissions: directory mode `0755` owned by the instance uid;
scripts mode `0750`. v1 does not auto-chmod — `functions.create`
fails fast if the file is not executable.

## Cold-tier integrity (git-as-truth composition)

Per [`8/3`](3-git-as-truth.md), function manifest rows are cold-tier
state; the script files live alongside Markdown sidecars in
`groups/<folder>/functions/`. Both flow through the gateway's
per-turn commit. The integrity contract:

- `functions.create` / `functions.update` reads the script at the
  declared path, records `command_sha256`, validates execute bit,
  inserts the row inside the resreg tx, and stages the script file
  for the turn's git commit. Manifest row and script file land in
  one commit.
- `spawn_function` re-hashes the script before exec and refuses to
  invoke if it does not match `command_sha256`. `fn.error` audit row
  with `reason=hash-drift`.
- `arizuko plan` shows drift: file hash != stored `command_sha256`.
  `arizuko apply` re-records the hash (no auto-rewrite of the
  script).

The on-disk file is the executable. The DB row is the contract. Git
is the source of truth for both.

## Concurrency back-pressure

Per-function `max_concurrent`. Counter = active spawns tracked by
fnspd (in-process semaphore per `(folder, name)`, decremented on
unit exit). Over-cap behavior:

- **webhook:** HTTP 429 Too Many Requests; no platform retry. Source
  retry policy is webhook caller's responsibility.
- **cron:** skip the firing, audit `fn.skipped reason=concurrency-cap`.
  Next fire happens per cron expression — no makeup.
- **fn-chain:** skip, audit `fn.skipped reason=concurrency-cap`.
  Upstream `fn.complete` still recorded; chain breaks at this
  hop.
- **manual (MCP):** JSON-RPC error
  `{code: -32602, message: "concurrency cap"}`.

## Resource limits

- **Per-function:** `timeout_s` (default 60, max 3600),
  `memory_mb` (default 256, capped by slice), `max_concurrent`
  (default 1), `stdout_max_b` (default 1 MiB).
- **Per-folder slice:** `MemoryMax`, `CPUWeight` — set on the
  `arizuko-<inst>-<folder>.slice` unit. The kernel enforces; no
  in-process accounting needed.
- **Instance-wide:** sum of folder slices + host capacity. No
  platform-level cap beyond that.

The slice always wins. A unit may declare `MemoryMax` lower than
the slice but cannot exceed it.

## Audit shape

Per [`5/I`](../5/I-tool-call-logging.md) and
[`7/F`](../7/F-audit-stream.md). New `tool` values in `audit_log`
(no schema change — `tool` is text):

| Action             | When                                                                  |
| ------------------ | --------------------------------------------------------------------- |
| `functions.create` | `functions.create` MCP/REST call.                                     |
| `functions.update` | `functions.update` MCP/REST call.                                     |
| `functions.delete` | `functions.delete` MCP/REST call.                                     |
| `fn.invoke`        | Spawn initiated (any trigger). One row per invocation.                |
| `fn.complete`      | Exit 0.                                                               |
| `fn.error`         | Exit non-zero (excluding timeout) OR hash-drift OR fnspd RPC failure. |
| `fn.timeout`       | systemd-run killed for `RuntimeMaxSec`.                               |
| `fn.skipped`       | Concurrency cap, chain-depth cap, or cron-fire collision.             |

Invocation `params_summary` carries: `trigger`, `input_size_b`,
`stdin_mode`, `upstream_call_id` (for `fn` trigger), `chain_depth`.
Completion rows carry: `exit`, `duration_ms`, `output_size_b`,
`truncated` (bool).

`turn_id` propagates through the chain — all hops of a chain share
the originating turn's id, so a turn's audit slice covers the whole
chain.

## Lifecycle (MCP + REST per 7/1)

| Action          | MCP                | REST                               |
| --------------- | ------------------ | ---------------------------------- |
| Create          | `functions.create` | `POST /v1/functions`               |
| List            | `functions.list`   | `GET /v1/functions`                |
| Get             | `functions.get`    | `GET /v1/functions/{name}`         |
| Update          | `functions.update` | `PATCH /v1/functions/{name}`       |
| Delete          | `functions.delete` | `DELETE /v1/functions/{name}`      |
| Invoke (manual) | `functions.invoke` | `POST /v1/functions/{name}:invoke` |

One hand-rolled resreg handler per [`5/5`](../5/5-uniform-mcp-rest.md).
`state: absent` row in a YAML manifest deletes. Group delete
cascades.

## ACL action namespacing

Action names live in the `auth/authorize.go` lattice. v1 adds:

- `functions:create`, `functions:update`, `functions:delete`,
  `functions:invoke` — direct grants.
- `mcp:functions.*` — covered by `mcp:*` (legacy lattice).

**`actionCovers` lattice extension (required code change):** the
existing rule `admin ⊃ interact ∪ mcp:*` does NOT cover
`functions:*` as written. v1 extends `actionCovers` so:

```
admin ⊃ interact ∪ mcp:* ∪ functions:*
```

This preserves operator expectation that an `admin` grant covers
new cold-tier resources at the same scope. Migration note: existing
admin grants automatically gain function authority on rollout. The
extension is mechanical (one switch case) and lands in the same
commit as the resreg handler.

Mint scopes follow [`5/W`](../5/W-webhook-routes.md)'s tier table for
function-bound route_tokens.

## Failure semantics (per trigger)

- **Webhook:** exit≠0 → HTTP 500, audit `fn.error`. Timeout → HTTP
  504, audit `fn.timeout`. Hash drift → HTTP 500, audit `fn.error
reason=hash-drift`. No platform retry; the caller's webhook source
  retries (if it does).
- **Cron:** error/timeout/drift audited. One-shot per fire; no
  retry. Next fire per cron expression.
- **fn-chain:** downstream fires ONLY on upstream `fn.complete`.
  Upstream `fn.error` / `fn.timeout` / `fn.skipped` breaks the chain
  at that hop; no downstream audit row.
- **Manual:** error returned to caller; caller decides retry.

## Operator failure modes (chain cycles, recovery)

Cycle detection is depth-cap, not graph analysis. Each spawn
increments `ARIZUKO_FN_CHAIN_DEPTH`; at depth 9, the would-be spawn
is rejected with audit row:

```
tool=fn.skipped
params_summary={
  "reason": "chain-depth",
  "depth": 9,
  "function": "<name>",
  "upstream_call_id": "<callid>",
  "originating_turn_id": "<turn>"
}
```

Operator-visible recovery path:

1. Inspect: `sqlite3 messages.db "SELECT * FROM audit_log WHERE
tool='fn.skipped' AND ...;"` — the row carries the upstream call
   and originating turn.
2. Walk the chain backward via `upstream_call_id` chain in
   `fn.invoke` rows for that turn.
3. Break the cycle by editing the offending `fn:<other>` trigger
   (`functions.update` with a different trigger or `state: absent`).
4. v1 has no replay primitive. If the chain needs to re-run from a
   specific call, operator manually invokes `functions.invoke` with
   the captured stdin (read from journald for that call). A
   `--stdin-from-call <callid>` escape hatch is parked for v2.

The depth cap (8 hops) is configurable per-instance via
`.env: FUNCTIONS_MAX_CHAIN_DEPTH`. Default chosen to be deep enough
for legitimate composition (build → test → notify → archive) but
shallow enough to surface accidents fast.

## How this slots into existing platform

1. **Amends [`5/W`](../5/W-webhook-routes.md).** `route_tokens` gets
   a `kind` column (`message` | `function`, default `message`). webd's
   `/hook/<token>` POST handler branches:
   - `kind=message` → existing append-inbound path.
   - `kind=function` → RPC fnspd via gated's
     `spawn_function(folder, name, body, headers)` and stream
     response. 5/W's "JID prefix bound to URL prefix" rule stays;
     function tokens still use `hook:` JIDs (the JID is the audit
     locator for inbound traffic; function execution is a separate
     audit lineage). 5/W's shipped tests are extended, not replaced.
2. **Amends [`5/W`](../5/W-webhook-routes.md) issuance.**
   `issue_webhook(source_label, kind="function", function_name="...")`
   for function-bound tokens. Existing `issue_webhook(source_label)`
   defaults to `kind=message` unchanged.
3. **Extends `scheduled_tasks`.** Same `kind` column. `timed`'s fire
   loop branches on `kind`.
4. **Adds `gated.spawn_function`** Go function reused by webd, timed,
   and the fn-chain subscriber.
5. **Adds `fnspd`** host-side spawner daemon (system-core).
6. **Extends `auth/authorize.go`** `actionCovers` lattice to include
   `functions:*` under `admin`.
7. **Adds `functions` resreg.Resource** in gated, dependency class 3
   alongside `route_tokens`, `scheduled_tasks` per
   [`5/36`](../5/36-yaml-manifests.md).

No new HTTP-facing daemon. No new socket beyond fnspd's local one.

## Open questions

1. **`groups` row needs `memory_mb` + `cpu_weight` columns** for
   per-folder slice config. Either add now (small schema change) or
   defer and use instance-wide defaults in v1. Lean: add now — the
   slice IS the v1 substrate, so config that drives it should be
   first-class.
2. **`kind: container-function`.** Some operators want stronger
   isolation (untrusted code, runtime dependency conflicts). Opt-in
   later: `kind: container-function` runs the command inside an
   ephemeral `docker run --rm` with the same systemd-run wrapping.
   v1 = host-side script only.
3. **Warm pool / cold start at higher rates.** Operating envelope
   declared as ≤10 invocations/sec/folder. Above that, what's the
   migration path — persistent adapter daemon, or a v2 warm-pool
   primitive? Decide when a real workload demands it; for now,
   document the envelope.
4. **fnspd alternative substrates.** `nsjail`, `bubblewrap`, or a
   custom Go exec server could replace systemd-run if cold-start
   measurements push us off systemd. v1 keeps systemd-run; fnspd is
   the abstraction boundary that makes substrate swap a localized
   change.
5. **Per-uid isolation per folder.** v1 runs all functions as the
   single instance uid. Tenant-tier instances may want per-folder
   uids. Requires uid pool management; parked.
6. **Filesystem layout — flat vs nested.** `~/functions/<name>.sh`
   chosen for v1 (matches "boring" + "one renderer"). Nested
   `~/functions/<name>/{run,deps,README}` deferred.
7. **Chain replay primitive.** `functions.invoke --stdin-from-call
<callid>` escape hatch parked. v1 recovery is manual.
8. **OTLP correlation across chain hops.** `turn_id` propagates,
   giving OTLP traces the same root span per
   [`5/O`](../5/O-otlp-export.md). Per-hop span correlation is
   automatic via that propagation; no new spec work.

## Non-goals (v1)

- Filesystem-event triggers (`fs:`), in-process gated bus-event
  triggers, identity-event triggers (login/logout). The trigger
  taxonomy is closed at four kinds in v1. New triggers land via
  their own spec extension.
- Container per invocation (open Q 2).
- Auto-scaling, warm pool, cluster.
- Language SDKs / function packaging (zip artifacts, layers,
  dependency management).
- Service mesh / inter-fn discovery beyond `fn:<name>` chain.
- Distributed tracing across fn calls beyond `turn_id` propagation —
  covered if operator opts into OTLP per
  [`5/O`](../5/O-otlp-export.md).
- WebSocket / bidirectional streaming response. v1 is
  request/response only; chunked transfer is fine, bidi is not.
- High-QPS data-plane workloads. Operating envelope is ≤10
  invocations/sec/folder (see "What this is NOT").
- Per-fn rate limit beyond `max_concurrent`. Use route_token rate
  limit ([`5/W`](../5/W-webhook-routes.md)) for inbound shaping on
  the webhook surface.
- Chain replay / dead-letter queue. v1 = manual re-invocation.

## Cross-refs

- [`../5/W-webhook-routes.md`](../5/W-webhook-routes.md) — route_tokens
  substrate; this spec amends it with the `kind` column and
  function-bound issuance.
- [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md) — audit
  field schema; this spec fills in the `tool` values listed above.
- [`../7/F-audit-stream.md`](../7/F-audit-stream.md) — audit_log
  table. New `tool` values only; no schema change.
- [`../5/5-uniform-mcp-rest.md`](../5/5-uniform-mcp-rest.md) — one
  resreg handler per resource; `functions` joins the unified surface.
- [`../8/3-git-as-truth.md`](3-git-as-truth.md) — function manifest
  row + script file are cold-tier state; per-turn commit picks them
  up; cold-tier integrity contract above implements the hashing
  discipline.
- [`../5/36-yaml-manifests.md`](../5/36-yaml-manifests.md) — `functions` as
  top-level resource key; `state: absent` semantics.
- [`../5/35-proxyd-standalone.md`](../5/35-proxyd-standalone.md) — no
  functions-specific proxyd route; webd serves `/hook/<token>` for
  both kinds.

## Acceptance

- `arizuko apply` with a `functions:` block creates table rows + emits
  audit rows; second apply is a no-op (idempotent per `7/5`).
- `POST /hook/<token>` where the token has `kind=function` spawns the
  script via fnspd and returns stdout as the response body with the
  declared `response_type`.
- `cron:<expr>` schedule fires the function; error path emits
  `fn.error` audit row.
- `fn:A → fn:B` chain: A complete → B invoked; A error → B not
  invoked.
- Depth-cap test: a 10-hop chain hits cap, emits `fn.skipped
reason=chain-depth` with operator-visible upstream call id.
- Concurrency-cap test: webhook surface returns 429; cron/fn/manual
  paths emit `fn.skipped reason=concurrency-cap`.
- Timeout test: `sleep 999` with `timeout_s=1` audited as `fn.timeout`.
- Hash-drift test: tamper script after create; next invocation fails
  with `fn.error reason=hash-drift`.
- Slice substrate test: per-folder `MemoryMax` enforced — a function
  that allocates beyond the slice cap is OOM-killed by the kernel.
- Existing `admin` grant covers `functions:create`/`update`/`delete`
  after the lattice extension (no per-action grants needed).
- `make test -short` passes; integration tests cover all four trigger
  paths against a `/bin/cat`-style fixture function.

## Pointers

- Plan: [`.ship/plan-7-6-functions.md`](../../.ship/plan-7-6-functions.md)
- Oracle critique (round 1): [`.ship/oracle-7-6-round1.md`](../../.ship/oracle-7-6-round1.md)
- Existing route_token implementation: `webd/route_token.go`,
  `ipc/ipc.go` (issue/list/revoke MCP), `gateway/gateway.go` (writer)
- Existing audit_log emitter: `resreg/resreg.go`, `audit/log.go`
- Existing actionCovers lattice: `auth/authorize.go:216–233`
