# audit/PLAN.md — comprehensive audit-log coverage

Status: round-1 plan for `audit_log` SQLite table as source of truth for
security and state-change events. Driven by [`specs/5/I`](../specs/5/I-tool-call-logging.md)
and [`specs/6/F`](../specs/6/F-audit-stream.md).

## Goal

One SQLite table — `audit_log` — captures every security event and every
state transition with a homogeneous JSON-shaped event. Written in the
same DB transaction as the underlying mutation. Never lossy.

slog → journald remains operational telemetry: same field shape, but
lossy by design (journald rotation, level filtering). If the two
disagree, the table wins.

## Standards consulted

The schema and category taxonomy below are cross-referenced with:

- **AWS CloudTrail event schema** — fields like `eventTime`,
  `eventSource`, `eventName`, `userIdentity`, `requestParameters`,
  `responseElements`, `errorCode`. Maps cleanly to our
  `created_at` / `category` / `action` / `actor` / `params_summary` /
  `outcome` / `error_msg`.
- **Kubernetes audit (audit.k8s.io v1)** — `level`, `stage`,
  `requestURI`, `verb`, `user`, `objectRef`, `responseStatus`. The
  per-resource `verb` + `objectRef` shape is exactly `action` +
  `resource` here.
- **OCSF (Open Cybersecurity Schema Framework)** — vendor-neutral
  category taxonomy (`Identity & Access Management`, `System Activity`,
  `Network Activity`, `Application Activity`). Our 10 categories
  collapse OCSF's classes into the surface area arizuko actually
  exposes.
- **OWASP ASVS chapter 8 (logging & monitoring)** — V8.2 requires
  "all security-relevant events" including logon, privilege
  changes, secret access, denied attempts. V8.3 requires no PII
  / no secrets in logs. Drives our `params_summary` redaction rule.
- **Linux auditd** — `type=` + `msg=` pair, comma-separated. We
  diverge (one row, JSON params), but the read-write split (auditd's
  `SYSCALL` vs `PATH`) inspires our state-change vs read split.

The schema in [`5/I`](../specs/5/I-tool-call-logging.md) is the canonical
field set; this plan extends it with the columns needed to serve the
adversary queries in round-3 ("who escalated grants on this folder six
months ago").

## Final field schema

Per [`5/I`](../specs/5/I-tool-call-logging.md) plus the bookkeeping
columns the spec defers to [`6/F`](../specs/6/F-audit-stream.md):

```sql
CREATE TABLE audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  category        TEXT    NOT NULL,
  action          TEXT    NOT NULL,
  actor           TEXT    NOT NULL,
  actor_sub       TEXT,
  resource        TEXT,
  scope           TEXT,                 -- folder or other scope identifier
  surface         TEXT,                 -- mcp | rest | cli | gateway | cron | crackbox | agent_pretool | agent_posttool
  params_summary  TEXT,                 -- JSON object, secrets redacted, ≤512 chars
  outcome         TEXT    NOT NULL,     -- ok | error | denied
  error_msg       TEXT,
  duration_ms     INTEGER,
  turn_id         TEXT,
  folder          TEXT,
  instance        TEXT,
  request_id      TEXT,
  source_ip       TEXT
);

CREATE INDEX audit_log_created_at ON audit_log(created_at);
CREATE INDEX audit_log_actor      ON audit_log(actor_sub) WHERE actor_sub IS NOT NULL;
CREATE INDEX audit_log_folder     ON audit_log(folder)    WHERE folder    IS NOT NULL;
CREATE INDEX audit_log_category   ON audit_log(category, action);
```

`scope` plus `folder` are deliberately both present: `folder` is the
group-folder dimension (the most common partition); `scope` is the
broader resource scope (`user`, `folder`, `system`) used for
secrets / grants where the same action applies at multiple scope
levels.

`outcome` is a closed enum of three values. `denied` (vs `error`)
distinguishes "authz refused the call" from "the call ran but failed";
forensic queries always want them apart.

## Category taxonomy (10 categories)

Closed set. New events must fit one of these; if not, add a category
in a follow-up commit rather than smuggling it in as a one-off action.

| Category    | Meaning                                                      | OCSF analogue                |
| ----------- | ------------------------------------------------------------ | ---------------------------- |
| `authn`     | Authentication events — login, logout, token mint, expiry    | IAM 3002 Authentication      |
| `authz`     | Authorization decisions — every allow + every deny           | IAM 3003 Authorization       |
| `access`    | Resource reads worth recording (secret read; sensitive list) | IAM 3005 Account Change      |
| `mutation`  | Generic state change on a registry resource                  | Application 6003             |
| `system`    | Daemon lifecycle, migration, config load                     | System 1001 File Activity    |
| `network`   | Egress allowlist mutation, crackbox connect/deny             | Network 4001 Network         |
| `channel`   | Adapter (re)register, channel inbound/outbound bind          | Application 6004             |
| `agent`     | Container lifecycle, turn boundaries, MCP tool call          | Process 1007 / 6005          |
| `secret`    | Secret CRUD + secret read (high-signal subset of `access`)   | IAM 3005                     |
| `scheduler` | Timed-task fire, dispatch, completion                        | Application 6003 (scheduled) |

Justification:

- `authn` vs `authz` — separate categories because forensic queries
  ("show me failed logins" vs "show me denied tool calls") run on
  different axes.
- `access` (carved out from `mutation`) — read events that matter
  forensically. We log secret reads, not every list query. ASVS 8.2.
- `secret` (carved out of `mutation` and `access`) — secrets warrant
  a dedicated stream because they're the highest-value forensic
  signal. CloudTrail dedicates a separate "AWS KMS" event source for
  the same reason.
- `network` (vs `system`) — egress allowlist decisions are a
  distinct surface (crackbox + container/network) with their own
  threat model, mirrors OCSF Network class.
- `agent` (vs `mutation`) — turn boundaries / container lifecycle
  don't mutate any registry resource but ARE critical state changes
  for forensic reconstruction.
- `scheduler` (vs `agent`) — different actor model: cron-driven, no
  human caller, no turn_id.

## Action vocabulary (~50 actions)

```
authn:
  login                  user authenticates (REST /auth/login, OAuth, magic-link)
  logout                 session destroyed
  token.mint             auth-session row created OR JWT minted
  token.expire           session expired or revoked
  token.revoke           explicit revocation
  identity.link          new sub joined existing identity
  identity.unlink        sub removed from identity
  invite.create          invitation token minted
  invite.consume         invitation accepted (sub redeemed)
  invite.revoke          invitation cancelled
  link_code.mint         identity link-code issued
  link_code.consume      link-code used

authz:
  authz.deny             any policy decision that refused the call
  authz.allow.sensitive  optional — only logged for tier-0 ACLs (operator scope)
  acl.add                acl row inserted
  acl.remove             acl row deleted
  membership.add         acl_membership row inserted (role nesting)
  membership.remove      acl_membership row deleted
  role.grant             member added to a group/role
  role.revoke            member removed

access:
  secret.read            secret returned to a caller (broker / dashd /me)
                         (also fires `secret.read` under category=secret)

mutation:
  route.create           proxyd_routes / routes table insert
  route.update           proxyd_routes update
  route.delete           proxyd_routes / routes delete
  web_route.set          web_routes upsert
  web_route.delete       web_routes delete
  task.create            scheduled_tasks insert
  task.update            scheduled_tasks update
  task.delete            scheduled_tasks delete
  group.create           groups insert
  group.update           groups update (open flag, model, container_config)
  group.delete           groups delete
  watcher.add            group_watchers insert
  watcher.remove         group_watchers delete
  pane.create            pane_sessions insert
  pane.update            pane_sessions update
  gate.set               onboarding_gates upsert
  gate.delete            onboarding_gates delete
  onboarding.queue       onboarding flow advanced (queue/approve)
  user.cost_cap.set      auth_users.cost_cap_cents_per_day update
  group.cost_cap.set     groups.cost_cap_cents_per_day update

system:
  daemon.start           any daemon completed startup
  daemon.stop            any daemon shutting down (SIGTERM/SIGINT)
  migration.apply        store migration newly applied (one row per file)
  config.load            .env parsed (instance start)

network:
  egress.allow.add       store.AddNetworkRule
  egress.allow.remove    store.RemoveNetworkRule
  egress.connect         crackbox: outbound TCP allowed by proxy
  egress.block           crackbox: outbound TCP refused by proxy

channel:
  channel.register       chanreg.Register row inserted
  channel.deregister     chanreg.Register row removed
  channel.bot_join       inbound: bot acknowledged invite to a chat
  route_token.mint       store.InsertRouteToken
  route_token.revoke     store.RevokeRouteToken

agent:
  container.spawn        container.Run launches docker container
  container.exit         container.Run returns (with exit_code)
  container.kill         explicit kill (timeout or operator stop)
  turn.start             session_log row inserted (turn begins)
  turn.end               turn_results row inserted (turn ends with status)
  mcp.tool.invoke        MCP tool call processed (one row per state-change call)

secret:
  secret.set             store.SetSecret
  secret.delete          store.DeleteSecret
  secret.read            broker resolved secret for a tool invocation
  secret.rotate          (future) — explicit rotation, separate from set

scheduler:
  task.fire              timed/main.go scheduled_tasks → 'firing'
  task.complete          timed/main.go set status='completed' or 'active' (recurring)
  task.error             timed/main.go set errored
```

## Master event list

Every event below MUST emit exactly one `audit_log` row. Pseudo-keyed
by `category.action`. Trigger sites are file references (line numbers
will drift; the function name is the anchor).

| Cat       | Action              | Trigger site                                                                               | Resource shape          | Who          | Rationale                                                |
| --------- | ------------------- | ------------------------------------------------------------------------------------------ | ----------------------- | ------------ | -------------------------------------------------------- |
| authn     | login               | `onbod/main.go` magic-link consume / OAuth callback                                        | `identities/<sub>`      | user         | OWASP V8.2: every authentication                         |
| authn     | logout              | `store.DeleteAuthSession` callers                                                          | `sessions/<hash>`       | user         | session destruction                                      |
| authn     | token.mint          | `store.CreateAuthSession`                                                                  | `sessions/<hash>`       | user         | every credential issuance                                |
| authn     | token.expire        | scheduled prune of expired sessions                                                        | `sessions/<hash>`       | system       | distinguish revoke vs expiry                             |
| authn     | identity.link       | `store.LinkSub` / `LinkSubToCanonical`                                                     | `identities/<id>`       | system/user  | identity drift forensics                                 |
| authn     | identity.unlink     | `store.UnlinkSub`                                                                          | `identities/<id>`       | operator     | inverse of link                                          |
| authn     | invite.create       | `store.CreateInvite`                                                                       | `invites/<token-hash>`  | operator     | onboarding chain of custody                              |
| authn     | invite.consume      | `store.ConsumeInvite`                                                                      | `invites/<token-hash>`  | user         | who used which invite                                    |
| authn     | invite.revoke       | `store.RevokeInvite`                                                                       | `invites/<token-hash>`  | operator     | revocation trail                                         |
| authn     | link_code.mint      | `store.MintLinkCode`                                                                       | `link_codes/<code>`     | user         | identity linking                                         |
| authn     | link_code.consume   | `store.ConsumeLinkCode`                                                                    | `link_codes/<code>`     | user         | identity linking                                         |
| authz     | authz.deny          | `auth.AuthorizeStructural` failure; `resreg.audit … denied`; `ipc.emitAuthzDenied`         | n/a                     | any          | every refused call (CloudTrail `errorCode=AccessDenied`) |
| authz     | acl.add             | `store.AddACLRow`                                                                          | `acl/<row>`             | operator     | every grant change                                       |
| authz     | acl.remove          | `store.RemoveACLRow`                                                                       | `acl/<row>`             | operator     | grant change                                             |
| authz     | membership.add      | `store.AddMembership`                                                                      | `acl_membership/<row>`  | operator     | role nesting change                                      |
| authz     | membership.remove   | `store.RemoveMembership`                                                                   | `acl_membership/<row>`  | operator     | role nesting change                                      |
| access    | secret.read         | crackbox broker secret resolve; `store.LookupSecret` from `dashd /me`                      | `secrets/<scope>/<key>` | agent/user   | forensic — who saw which secret when                     |
| mutation  | route.create        | `store.AddRoute` / `store.SetRoutes`                                                       | `routes/<id>`           | operator/MCP | route table mutation                                     |
| mutation  | route.update        | `store.UpdateProxydRoute`                                                                  | `proxyd_routes/<path>`  | operator     | proxyd-route mutation                                    |
| mutation  | route.delete        | `store.DeleteRoute` / `store.DeleteProxydRoute`                                            | `routes/<id>`           | operator/MCP | route removal                                            |
| mutation  | web_route.set       | `store.SetWebRoute`                                                                        | `web_routes/<prefix>`   | operator     | webd routing change                                      |
| mutation  | web_route.delete    | `store.DelWebRoute`                                                                        | `web_routes/<prefix>`   | operator     | webd routing change                                      |
| mutation  | task.create         | `store.CreateTask`                                                                         | `scheduled_tasks/<id>`  | operator/MCP | crontab mutation                                         |
| mutation  | task.update         | `store.UpdateTask`                                                                         | `scheduled_tasks/<id>`  | operator/MCP | crontab mutation                                         |
| mutation  | task.delete         | `store.DeleteTask`                                                                         | `scheduled_tasks/<id>`  | operator/MCP | crontab mutation                                         |
| mutation  | group.create        | `store.PutGroup` (insert path) / `onbod.SetupGroup`                                        | `groups/<folder>`       | operator     | group lifecycle                                          |
| mutation  | group.update        | `store.PutGroup` (update path), `SetGroupOpen`, `SetGroupModel`, `SetGroupContainerConfig` | `groups/<folder>`       | operator     | per-group toggle                                         |
| mutation  | group.delete        | `store.DeleteGroup`                                                                        | `groups/<folder>`       | operator     | group teardown                                           |
| mutation  | watcher.add         | `store.AddGroupWatcher`                                                                    | `group_watchers/<row>`  | operator     | cross-folder visibility                                  |
| mutation  | watcher.remove      | `store.RemoveGroupWatcher`                                                                 | `group_watchers/<row>`  | operator     | cross-folder visibility                                  |
| mutation  | pane.create         | `store.InsertPaneSession`                                                                  | `pane_sessions/<id>`    | user         | dashd session                                            |
| mutation  | pane.update         | `store.UpdatePaneSession`                                                                  | `pane_sessions/<id>`    | user         | dashd session                                            |
| mutation  | gate.set            | `store.PutGate` / `EnableGate`                                                             | `onboarding_gates/<g>`  | operator     | onboarding-tier change                                   |
| mutation  | gate.delete         | `store.DeleteGate`                                                                         | `onboarding_gates/<g>`  | operator     | onboarding-tier change                                   |
| mutation  | onboarding.queue    | `onbod/main.go` UPDATE onboarding SET status=...                                           | `onboarding/<jid>`      | system       | who joined when                                          |
| mutation  | user.cost_cap.set   | `store.SetUserCostCap`                                                                     | `auth_users/<sub>`      | operator     | budget gate change                                       |
| mutation  | group.cost_cap.set  | `store.SetGroupCostCap`                                                                    | `groups/<folder>`       | operator     | budget gate change                                       |
| system    | daemon.start        | each daemon's `main()`                                                                     | `daemons/<name>`        | system       | startup signal                                           |
| system    | daemon.stop         | signal handler                                                                             | `daemons/<name>`        | system       | shutdown signal                                          |
| system    | migration.apply     | `db_utils.Migrate` per applied file                                                        | `migrations/<file>`     | system       | schema-evolution trail                                   |
| system    | config.load         | `core.LoadConfig`                                                                          | `config`                | system       | what config was active when                              |
| network   | egress.allow.add    | `store.AddNetworkRule`                                                                     | `network_rules/<row>`   | operator     | network policy change                                    |
| network   | egress.allow.remove | `store.RemoveNetworkRule`                                                                  | `network_rules/<row>`   | operator     | network policy change                                    |
| network   | egress.connect      | crackbox proxy `(allowed=true)`                                                            | `egress/<host>`         | container    | per-connection log                                       |
| network   | egress.block        | crackbox proxy `(allowed=false)`                                                           | `egress/<host>`         | container    | denied connections (highest forensic value)              |
| channel   | channel.register    | `chanreg.Register`                                                                         | `channels/<jid>`        | operator     | adapter binding                                          |
| channel   | channel.deregister  | inverse                                                                                    | `channels/<jid>`        | operator     | adapter binding                                          |
| channel   | route_token.mint    | `store.InsertRouteToken`                                                                   | `route_tokens/<hash>`   | operator     | public chat link issuance                                |
| channel   | route_token.revoke  | `store.RevokeRouteToken`                                                                   | `route_tokens/<hash>`   | operator     | revocation                                               |
| agent     | container.spawn     | `container.Run` (start)                                                                    | `containers/<name>`     | gateway      | sandbox lifecycle                                        |
| agent     | container.exit      | `container.Run` (end, exit-code in params)                                                 | `containers/<name>`     | gateway      | sandbox lifecycle                                        |
| agent     | container.kill      | `container.StopContainerArgs` invocation                                                   | `containers/<name>`     | gateway      | sandbox lifecycle                                        |
| agent     | turn.start          | `store.LogSession`                                                                         | `sessions/<id>`         | gateway      | conversational turn boundary                             |
| agent     | turn.end            | `store.LogTurnResult`                                                                      | `sessions/<id>`         | gateway      | conversational turn boundary                             |
| agent     | mcp.tool.invoke     | `ipc.ServeMCP` emitSys (every state-change tool)                                           | `mcp/<tool>`            | agent        | every state-change MCP call                              |
| secret    | secret.set          | `store.SetSecret`                                                                          | `secrets/<scope>/<key>` | operator     | secret CRUD                                              |
| secret    | secret.delete       | `store.DeleteSecret`                                                                       | `secrets/<scope>/<key>` | operator     | secret CRUD                                              |
| secret    | secret.read         | crackbox broker resolve / `dashd/me_secrets.go`                                            | `secrets/<scope>/<key>` | agent/user   | high-frequency, high-value                               |
| scheduler | task.fire           | `timed/main.go` SET status='firing'                                                        | `scheduled_tasks/<id>`  | cron         | per-fire row                                             |
| scheduler | task.complete       | `timed/main.go` SET status IN ('active','completed')                                       | `scheduled_tasks/<id>`  | cron         | per-fire result                                          |
| scheduler | task.error          | `timed/main.go` error path                                                                 | `scheduled_tasks/<id>`  | cron         | failed fires                                             |

**Count: 53 distinct events** (covers the user's ≥40 floor).

### SKIP (documented gaps)

- **`messages` table inserts / updates** — every inbound/outbound chat
  message is data, not a security event. The `messages` row IS the
  audit trail of "what arrived" by construction. We do NOT emit a
  `mutation` row for each chat message; volume is wrong (10–100k/day)
  and the value is zero (the row itself is the record).
- **`messages.status` updates** (sent/delivered) — same rationale.
  These are operational state on a row that's already its own log.
- **`cost_log` inserts** — already its own append-only audit-shaped
  table per [`5/34`](../specs/5/34-cost-budget.md); polling it from
  `audit_log` would double-write. Audit `mutation` references it via
  budget cap changes (above).
- **`secret_use_log`** — replaced by `secret.read` rows in
  `audit_log` once the audit table lands (round 2 deletes both
  `ipc_audit` and `cli_audit`; `secret_use_log` consolidates in
  round 3 if hot-path volume permits).

## JSON examples (one per category)

```json
{
  "category": "authn",
  "action": "login",
  "actor": "user:google:114alice",
  "actor_sub": "google:114alice",
  "surface": "rest",
  "outcome": "ok",
  "request_id": "r-7f2a",
  "source_ip": "10.1.2.3",
  "instance": "krons",
  "params_summary": "{\"method\":\"oauth\",\"provider\":\"google\"}",
  "duration_ms": 134
}
```

```json
{
  "category": "authz",
  "action": "authz.deny",
  "actor": "agent:atlas/support",
  "surface": "mcp",
  "outcome": "denied",
  "folder": "atlas/support",
  "resource": "mcp/secret.set",
  "instance": "krons",
  "params_summary": "{\"reason\":\"no_grant\"}",
  "turn_id": "t-abc"
}
```

```json
{
  "category": "access",
  "action": "secret.read",
  "actor": "agent:atlas/support",
  "actor_sub": "agent:atlas/support",
  "surface": "agent_pretool",
  "resource": "secrets/folder/atlas/support/OPENAI_API_KEY",
  "folder": "atlas/support",
  "scope": "folder",
  "outcome": "ok",
  "turn_id": "t-abc",
  "duration_ms": 4,
  "instance": "krons"
}
```

```json
{
  "category": "mutation",
  "action": "route.create",
  "actor": "operator:ondrej",
  "actor_sub": "google:114ondrej",
  "surface": "mcp",
  "resource": "proxyd_routes/auth/login",
  "outcome": "ok",
  "instance": "krons",
  "duration_ms": 11,
  "params_summary": "{\"verb\":\"POST\",\"path\":\"/auth/login\",\"target\":\"onbod:8080\"}"
}
```

```json
{
  "category": "system",
  "action": "migration.apply",
  "actor": "system",
  "surface": "gateway",
  "resource": "migrations/0066-audit-log.sql",
  "outcome": "ok",
  "instance": "krons",
  "duration_ms": 8
}
```

```json
{
  "category": "network",
  "action": "egress.block",
  "actor": "agent:atlas/support",
  "surface": "crackbox",
  "resource": "egress/raw.githubusercontent.com:443",
  "folder": "atlas/support",
  "outcome": "denied",
  "params_summary": "{\"reason\":\"not_in_allowlist\"}",
  "turn_id": "t-abc",
  "instance": "krons"
}
```

```json
{
  "category": "channel",
  "action": "route_token.mint",
  "actor": "operator:ondrej",
  "actor_sub": "google:114ondrej",
  "surface": "rest",
  "resource": "route_tokens/sha256:abc...",
  "params_summary": "{\"owner_folder\":\"atlas/support\",\"jid_kind\":\"web\"}",
  "outcome": "ok",
  "instance": "krons"
}
```

```json
{
  "category": "agent",
  "action": "container.spawn",
  "actor": "system",
  "surface": "gateway",
  "resource": "containers/arizuko-krons-atlas-support",
  "folder": "atlas/support",
  "turn_id": "t-abc",
  "outcome": "ok",
  "params_summary": "{\"image\":\"arizuko-ant:v0.45.10\"}",
  "duration_ms": 1842,
  "instance": "krons"
}
```

```json
{
  "category": "secret",
  "action": "secret.set",
  "actor": "operator:ondrej",
  "actor_sub": "google:114ondrej",
  "surface": "cli",
  "resource": "secrets/folder/atlas/support/OPENAI_API_KEY",
  "scope": "folder",
  "folder": "atlas/support",
  "outcome": "ok",
  "params_summary": "{\"value\":\"<redacted:60chars>\"}",
  "instance": "krons",
  "duration_ms": 9
}
```

```json
{
  "category": "scheduler",
  "action": "task.fire",
  "actor": "system",
  "surface": "cron",
  "resource": "scheduled_tasks/daily-summary",
  "folder": "atlas/support",
  "outcome": "ok",
  "params_summary": "{\"cron\":\"0 9 * * *\",\"next_run\":\"2026-05-26T09:00Z\"}",
  "duration_ms": 2,
  "instance": "krons"
}
```

## Read-vs-write split

Per [`5/I`](../specs/5/I-tool-call-logging.md):

- **State-changing** ops emit `audit_log` row AND slog line, same
  DB transaction as the mutation. Roll back on audit insert failure.
- **Read-only** ops emit slog only. Exception: `secret.read` (high
  forensic value, ASVS V8.2.5).

## Redaction rules

`params_summary` is JSON, ≤512 chars after redaction:

- Keys matching `(?i)pass(word)?|token|secret|key|api_key|authorization|cookie` →
  `<redacted:Nchars>`.
- Values longer than 200 chars → truncated with `…<Nchars>` suffix.
- The full JSON is dropped to `{}` if total length post-redaction
  still exceeds 512 chars (with a `_truncated: true` field).

## Consolidation plan

Round 2:

- Create `audit_log` table (migration `0066-audit-log.sql`).
- Drop `ipc_audit` data (`ipc_audit` is 60 days old, low value;
  acceptable loss) — confirm by querying its row count first.
- Drop `cli_audit` data — same.
- Both tables themselves are dropped in `0066`; helpers in
  `store/cli_audit.go` and `store/ipc_audit.go` are deleted; callers
  switch to `audit.EmitInTx`.

## Public API sketch

```go
// audit.Event is the homogeneous shape. Zero-value safe.
type Event struct {
    Category      string
    Action        string
    Actor         string
    ActorSub      string
    Resource      string
    Scope         string
    Surface       string
    ParamsSummary map[string]any // redacted + truncated by Emit
    Outcome       string         // "ok" | "error" | "denied"
    ErrorMsg      string
    DurationMS    int64
    TurnID        string
    Folder        string
    Instance      string
    RequestID     string
    SourceIP      string
}

// Emit inserts one row using the package-level *sql.DB.
// Returns the inserted ID. Errors are non-fatal — slog warn + drop.
func Emit(ctx context.Context, e Event) (int64, error)

// EmitInTx inserts one row inside an already-open transaction.
// Used by state-change handlers that want roll-back-on-audit-failure
// semantics. The tx must NOT be Committed/Rolled back before this returns.
func EmitInTx(tx *sql.Tx, e Event) (int64, error)

// Init wires the package-level db. Daemons call once at startup.
func Init(db *sql.DB, instance string)
```

## References (citations in commit log)

- `specs/5/I-tool-call-logging.md` (canonical field set)
- `specs/6/F-audit-stream.md` (audit_log table direction)
- `specs/5/5-uniform-mcp-rest.md` (resreg.audit() surface)
- `specs/5/34-cost-budget.md` (cost_log adjacency)
- AWS CloudTrail: docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-event-reference-record-contents.html
- Kubernetes audit: kubernetes.io/docs/tasks/debug/debug-cluster/audit/
- OCSF: schema.ocsf.io
- OWASP ASVS V8: owasp.org/www-project-application-security-verification-standard/
