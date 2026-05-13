---
status: spec
depends: [9-crackbox-standalone, 10-crackbox-arizuko]
---

# Egred secrets injection

> Secrets never enter the sandbox. Egred (the proxy) replaces
> placeholders on egress.

## Problem

Container/VM has secrets in env → can exfiltrate them. Even with
domain filtering, a compromised agent could POST secrets to an
allowed domain.

## Solution

1. Sandbox gets **placeholder** values, not real secrets.
2. Real secrets stored in egred (per-spawn-id, alongside the
   allowlist).
3. Egred replaces placeholders with real values in outbound
   requests.

Sandbox never sees real secrets. Can't leak what you don't have.

## Where this lives

- **egred** (`crackbox/cmd/egred/`, `crackbox/pkg/proxy/`) — the
  placeholder→real substitution at egress. Only the proxy can
  MITM cleanly.
- **arizuko's `secrets` table** (`store/migrations/0034-secrets.sql`)
  — owns the real values and per-folder / per-user scoping.
- **`gated` (today's `container.Run`)** — at spawn time, resolves
  the (folder, caller-user) overlay → flat placeholder map → POSTs
  to egred alongside the allowlist register.

```
secrets table → arizuko picks (folder ∪ user) overlay → flat
  {env_name: placeholder} map for container env
  {placeholder: {value, header, domains}} map for egred register
  → egred holds the map → on outbound, substitutes inline
```

## Scopes — channel and per-user overlay

Two scopes compose at spawn:

1. **Channel-scoped** (`scope_kind='folder', scope_id=<folder>`).
   Operator-managed. API keys the team's bot uses for shared tools.
2. **Per-user overlay** (`scope_kind='user', scope_id=<auth_users.sub>`).
   User-managed. Each teammate's own credentials.

Resolution at spawn:

```
env := base ∪ folder-secrets-for(<folder>) ∪ user-secrets-for(<caller_sub>)
```

Per-user values **override** channel-scoped values when both define
the same env name. The spawn is per-turn and known-single-caller
(see `container/runner.go:138`), so resolution is unambiguous.

### Lifting the `is_group=1` filter

`container/runner.go`'s `resolveSpawnEnv` currently skips the
per-user overlay for group chats. That guard was conservative; the
caller is just as known in a group chat as in a DM. Remove it:

```diff
- if !resolver.GetChatIsGroup(chatJID) {
-     if userSub, ok := resolver.UserSubByJID(chatJID); ok {
-         ...
-     }
- }
+ if userSub, ok := resolver.UserSubByJID(chatJID); ok {
+     ...
+ }
```

## Identity unification

A teammate in Slack arrives with a platform-bound user_jid (e.g.
`slack:T012/U345`). The same person signs in to the dashboard via
identity OAuth (GitHub / Google / Discord / Telegram) and is known
to arizuko as `auth_users.sub`. The two are linked through the
existing `user_jids` table — writes to `/dash/me/secrets` land
under the signed-in user's `sub`; spawn-time overlay resolves the
inbound `chat_jid` via `SecretsResolver.UserSubByJID` (already wired
in `container/runner.go:109`). A user with no linked identity for
the inbound platform contributes no per-user overlay on that turn —
no-op, not error.

## Spec format (egred register payload)

Extended from the existing `/v1/register`:

```yaml
POST /v1/register
{
  "ip": "10.99.x.y",
  "id": "<spawn_id>",                    // per-spawn random; egred keys by this
  "allowlist": ["api.anthropic.com", "api.github.com"],
  "secrets": {
    "ANTHROPIC_API_KEY": {               // channel-scoped
      "placeholder": "sk-ant-PLACEHOLDER-...",
      "value": "sk-ant-api03-real-key-here",
      "inject": [{"header": "x-api-key"}],
      "domains": ["api.anthropic.com"]
    },
    "GITHUB_TOKEN": {                    // per-user overlay
      "placeholder": "ghp_PLACEHOLDER_...",
      "value": "ghp_real-token",
      "inject": [{"header": "authorization", "format": "Bearer {value}"}],
      "domains": ["api.github.com"]
    }
  }
}
```

The map is flat-keyed by **env name**, not by user identity. Per-user
selection happens before the payload is built; egred sees one
unambiguous map per spawn-id.

## Placeholder requirements

Placeholders must:

- Be unique enough to not collide with real data
- Match expected format (prefix, length) so client validation passes
- Be obviously fake on inspection
- Be **allocated per spawn** (random suffix), so the placeholder
  string never leaks identity into the wire format

Suggested pattern: `{prefix}PLACEHOLDER_{rand8}`

Examples:

- `sk-ant-PLACEHOLDER-anthropic_a3f9b21c` (Anthropic format)
- `ghp_PLACEHOLDER_github_e1d2c4ff` (GitHub format)
- `sk-PLACEHOLDER-openai_77a01b3e` (OpenAI format)

## Injection modes

### Header injection (default; **only mode allowed for user-scope**)

Replace placeholder in any header value:

```
GET /v1/messages HTTP/1.1
x-api-key: sk-ant-PLACEHOLDER-anthropic_a3f9b21c
           ↓ egred replaces ↓
x-api-key: sk-ant-api03-real-key-here
```

### Header with format

Use `format: 'Bearer {value}'` to wrap the secret in a template.

### Body injection (**channel-scope only**)

Replace in request body (JSON, form data, etc.):

```json
{"api_key": "sk-PLACEHOLDER-openai_77a01b3e", "prompt": "hello"}
              ↓ egred replaces ↓
{"api_key": "sk-real-openai-key", "prompt": "hello"}
```

**Caution**: Body injection is string replacement, not JSON-aware.
Placeholder must not appear in user content. User-scoped rows
reject `inject_mode='body'` (string-replace is too leaky for
content the user can influence); operator-scoped channel rows may
opt in.

## TLS termination

CONNECT-tunneled HTTPS is opaque to egred today (the whole point
of the v1 forward proxy is no MITM). Secrets injection requires
egred to **terminate TLS for whitelisted destinations** so it can
modify request bytes.

This is selective MITM:

- Only for destinations in the per-id allowlist
- Only for ids that have secrets configured
- Per-destination CA cert distributed to the sandbox via env or
  filesystem mount

Anything not in the secrets-enabled set keeps the current
CONNECT-splice behavior — opaque, no certificate manipulation.

## Write path — dashboard

Self-service per-user secrets under `/dash/me/secrets`:

| Route                    | Method | Purpose                                     |
| ------------------------ | ------ | ------------------------------------------- |
| `/dash/me/secrets`       | GET    | List the caller's secrets (redacted values) |
| `/dash/me/secrets`       | POST   | Add a row                                   |
| `/dash/me/secrets/{key}` | PATCH  | Rotate value                                |
| `/dash/me/secrets/{key}` | DELETE | Remove                                      |

Authentication: existing identity OAuth. POST body:
`{key, value, header, target_domain}`. Server rejects
`inject_mode='body'` for the user scope (HTTP 400).

Channel-scoped secrets are operator-managed through the CLI; no
self-service UI on the dashboard for them (operator territory).

## Operator CLI

Folder-scope (channel) secrets:

```bash
arizuko secret <inst> set <folder> ANTHROPIC_API_KEY \
  --placeholder "sk-ant-PLACEHOLDER-..." \
  --value "sk-ant-real-key" \
  --header x-api-key \
  --domain api.anthropic.com

arizuko secret <inst> list <folder>
arizuko secret <inst> rm <folder> ANTHROPIC_API_KEY
```

User-scope overlay:

```bash
arizuko user-secret <inst> set <user_sub> GITHUB_TOKEN \
  --value "ghp_real-token" \
  --header authorization \
  --domain api.github.com

arizuko user-secret <inst> list <user_sub>
arizuko user-secret <inst> delete <user_sub> GITHUB_TOKEN
```

Neither CLI exists in `cmd/arizuko/` today — both ship as the first
operator-grade tools of the family in this spec. The in-process
`store.SetSecret` API exists already.

Standalone form for `crackbox run` (no arizuko store):

```bash
crackbox run --allow api.anthropic.com \
  --secret ANTHROPIC_API_KEY=sk-real,header=x-api-key,placeholder=sk-PLACEHOLDER-... \
  -- claude
```

## Schema

Existing `secrets` table (migration `0034-secrets.sql`):

```
secrets(scope_kind TEXT, scope_id TEXT, key TEXT, enc_value BLOB,
        created_at TEXT, PRIMARY KEY (scope_kind, scope_id, key))
```

Additive migration to carry egred-injection metadata:

```sql
ALTER TABLE secrets ADD COLUMN inject_mode TEXT NOT NULL DEFAULT 'header';
ALTER TABLE secrets ADD COLUMN header      TEXT NOT NULL DEFAULT '';
ALTER TABLE secrets ADD COLUMN target_domain TEXT NOT NULL DEFAULT '';
```

(Do not add a column named `kind` — collides visually with
`scope_kind`.)

## Audit trail

Each spawn produces one register-side log record per secret entry,
plus one substitution-side record per actual swap on the wire:

- **Register-side** (arizuko, new table `secret_register_log`):
  `(spawn_id, user_sub, env_name, destination_host, action, at)`.
- **Substitution-side** (egred, in-proc log emit):
  `(spawn_id, env_name, destination_host, request_id, at)`.

No secret values in logs.

## Security properties

1. **No exfiltration**: Sandbox can't leak secrets it doesn't have.
2. **Scoped injection**: Secrets only injected for that secret's
   allowed domains.
3. **Per-spawn isolation**: A spawn for caller Alice receives only
   Alice's overlay + the folder default; Bob's secrets never enter
   that spawn's register payload.
4. **Audit trail**: Egred logs which secret was used, when, where;
   arizuko logs the register handoff.
5. **Revocation**: Change the row in arizuko's store; next spawn
   picks up the new value; the sandbox is unaffected.

## Acceptance

1. **Channel-scope**: Operator runs
   `arizuko secret <inst> set <folder> ANTHROPIC_API_KEY --value ... --header x-api-key --domain api.anthropic.com`.
   Next spawn for that folder: container env has
   `ANTHROPIC_API_KEY=<placeholder>`; egred `/v1/register` carries
   the mapping; outbound request to `api.anthropic.com` has the
   real key.
2. **Per-user overlay**: A teammate signs in at `/dash/me/secrets`,
   POSTs `{key:GITHUB_TOKEN, value:ghp_..., header:authorization,
target_domain:api.github.com}`. Row lands at
   `(scope_kind='user', scope_id=<sub>, key='GITHUB_TOKEN')`,
   AES-GCM-encrypted. Next spawn triggered by that teammate's
   inbound message carries `GITHUB_TOKEN=<random-placeholder>` in
   env; the egred register POST contains
   `<placeholder> → ghp_...` with header + domain.
3. **MCP/tool use**: A skill reads `process.env.GITHUB_TOKEN`, sends
   `Authorization: Bearer <placeholder>` to `api.github.com`; egred
   substitutes; GitHub returns the user's data.
4. **Cross-user isolation**: Spawn for a different caller in the
   same channel gets a different placeholder and (if that user has
   one) a different real value. Bob's secret never enters Alice's
   spawn.
5. **Phase A reject body for user-scope**: POST to `/dash/me/secrets`
   with `inject_mode=body` → 400, no row written.
6. **Audit**: each spawn emits N `secret_register_log` rows for
   its N injected secrets.

## Out of scope

- Surrogate OAuth tokens (third-party login as the user, tokens
  used by the bot in their turns) — deferred, see
  [`specs/12/h-surrogate-oauth.md`](../12/h-surrogate-oauth.md).
- Body injection for user-scope secrets — header-only in Phase A;
  revisit when a documented use case appears.
- Per-tool grants on per-user secrets — today, scope is
  `(user, env_name, target_domain)`; egred allowlist enforces
  destination matching.
- HSM/KMS for secrets at rest.
- Secret rotation mid-run.
- Response scanning.
- Non-HTTP secret access.

## Decisions

- **Per-spawn random placeholders**. Egred disambiguates by
  `spawn_id` (the register key); placeholders never embed user
  identity. Avoids leaking identity into the wire format.
- **Flat env-name keys** in the register payload. Per-user
  selection happens before payload build.
- **Header-only for user-scope**. Operator-scoped folder rows may
  still opt into body injection.
- **No new IPC plumbing**. Spawn-time resolution + the existing
  egred register POST is enough; no per-turn envelope changes, no
  MCP tool for secret access.

## Implementation plan

Six milestones, each a single git commit, each green-builds
(`make test` + `make lint`).

### Blockers (gaps to close)

1. Operator CLIs `arizuko secret` and `arizuko user-secret` don't
   exist yet (`cmd/arizuko/main.go:38-65`). Both ship in this work.
2. `crackbox/pkg/admin/api.go:62-72` and `crackbox/pkg/client/client.go:41-43`
   carry only `{IP, ID, Allowlist}`. The `secrets` map is added in M0.
3. Egred selective TLS-MITM doesn't exist; `crackbox/pkg/proxy/*` is
   CONNECT-splice only (`peek.go`, `transparent.go`). M0.
4. No centralized audit-log table or library today. M1 adds
   `secret_register_log`.
5. `dashd/main.go:87` opens the DB read-only; the dashboard write
   path needs either a writable handle or DB-handle split. Decision:
   split — `dashd.secretsDB` writable handle just for this use case.
   `dashd` also needs `AUTH_SECRET` in its env to decrypt — env-var
   addition required.

### M0. Spec 11 prereqs (egred wire + selective MITM + CA distribution)

**Files**:

- `crackbox/pkg/admin/api.go` (lines 62-72: extend `WireEntry` with
  `Secrets map[string]SecretInject` + `SecretInject` struct).
- `crackbox/pkg/admin/registry.go` (`Set` signature gains `secrets`).
- `crackbox/pkg/client/client.go` (lines 41-43: extend `Register`).
- `crackbox/pkg/proxy/proxy.go`, `crackbox/pkg/proxy/transparent.go`,
  new `crackbox/pkg/proxy/mitm.go` (selective MITM for hosts with a
  registered `secrets` entry).
- `crackbox/cmd/crackbox/main.go` (CA-bootstrap flag, on-disk path).
- `crackbox/pkg/host/host.go:110`, `crackbox/pkg/run/run.go:96`,
  `container/egress.go:102` — adapt to new `Register` signature
  (pass empty `secrets`; real values populated in M2).

**Tests**:

- `crackbox/pkg/admin/api_test.go`: `TestRegister_AcceptsSecretsField`,
  `TestState_RoundTripsSecrets`.
- `crackbox/pkg/client/client_test.go`: `TestRegister_SerializesSecrets`.
- `crackbox/pkg/proxy/mitm_test.go`: `TestMITM_ReplacesHeaderPlaceholder`,
  `TestMITM_NotMITMedOutsideSecretsHosts`, `TestMITM_LeavesBodyAlone`
  (header-only).

**Verify**: `make -C crackbox test`, then `make test` repo-root.

**Migration**: none (additive wire field with `omitempty`). No DB.

### M1. Schema migration + audit table

**Files**:

- `store/migrations/0047-secret-injection-metadata.sql`:
  `ALTER TABLE secrets ADD COLUMN inject_mode TEXT NOT NULL DEFAULT 'header';`
  `ALTER TABLE secrets ADD COLUMN header TEXT NOT NULL DEFAULT '';`
  `ALTER TABLE secrets ADD COLUMN target_domain TEXT NOT NULL DEFAULT '';`
- `store/migrations/0048-secret-audit.sql`:
  `CREATE TABLE secret_register_log (spawn_id, user_sub, env_name, destination_host, action, at);`
- `store/secrets.go`: extend `Secret` struct; add `SetSecretWithMeta`;
  change `UserSecrets` / `FolderSecretsResolved` return type from
  `map[string]string` to `map[string]ResolvedSecret`.
- `store/audit.go` (new): `LogSecretRegister(...)`.

**Tests**: `store/secrets_test.go`, `store/audit_test.go`.

**Verify**: `make test ./store/...`.

**Migration**: SQLite `ALTER TABLE ADD COLUMN` is in-place. No agent
`MIGRATION_VERSION` bump (bump happens in M6).

### M2. `resolveSpawnEnv` lift + placeholder generation + egred register

**Files**:

- `container/runner.go`: extend `SecretsResolver` to return
  `ResolvedSecret`; lift `!GetChatIsGroup` guard in `resolveSpawnEnv`;
  return type becomes `SpawnSecrets{Env, Inject}`.
- `container/placeholder.go` (new): `newPlaceholder(envName, format)`
  with prefix heuristic (`ghp_`, `xoxb-`, `sk-ant-`).
- `container/egress.go`: `registerEgress` gains `secrets` param;
  allocates `spawnID`.
- `gateway/gateway.go`: wire `AuditFn` Input field.

**Phase A guard**: `resolveSpawnEnv` drops user-scoped rows with
`inject_mode != "header"` (logged at `slog.Warn`).

**Tests** (`container/secrets_test.go`, new `placeholder_test.go`,
extend `egress_test.go`):

- **Delete** `TestResolveSpawnEnv_NoUserSecretsInGroupChat`.
- Add `TestResolveSpawnEnv_UserOverlaysAppliedInGroupChat`.
- Add `TestResolveSpawnEnv_PlaceholderEnvSwapped`.
- Add `TestResolveSpawnEnv_InjectMapMirrorsValues`.
- Add `TestResolveSpawnEnv_UserOverridesFolderSameKey`.
- Add `TestResolveSpawnEnv_PhaseA_RejectsBodyInjectForUser`.

**Verify**: `make test`; fake egred records POSTs; `docker exec ... env | grep PLACEHOLDER` shows placeholder, never real value.

### M3. `/dash/me/secrets` CRUD + CSRF

**Files**:

- `dashd/me_secrets.go` (new): GET/POST/PATCH/DELETE handlers.
  Validates `key ~ ^[A-Z][A-Z0-9_]*$`, rejects `inject_mode=body`,
  uses identity-bound `X-User-Sub`.
- `dashd/csrf.go` (new): `HMAC(X-User-Sub + day, AUTH_SECRET)`
  per-session token. Hidden form field, verified on writes.
- `dashd/main.go`: add `AUTH_SECRET` env read; open separate writable
  handle for secrets table; register four routes; add nav entry.

**Tests** (`dashd/me_secrets_test.go`):
`GET_ListsCallerOnly`, `POST_AcceptsHeaderInject`,
`POST_RejectsBodyInject_PhaseA`, `PATCH_Rotates`, `DELETE_Removes`,
`NoIdentityHeader_403`, `CrossUser_CannotReadOthers`,
`CSRF_RequiredOnWrite`.

**Verify**: `make test ./dashd/...`; manual deploy to krons.

**Migration**: dashd compose env gains `AUTH_SECRET`.

### M4. Operator CLIs

**Files**:

- `cmd/arizuko/secret.go` (new): folder-scope CRUD.
- `cmd/arizuko/user_secret.go` (new): user-scope CRUD.
- `cmd/arizuko/main.go`: extend usage banner + `cmds` map.

**Tests** (`cmd/arizuko/{secret,user_secret}_test.go`):
`Set_PersistsRow`, `List_RedactsValue`, `Delete_RemovesRow`,
`Set_RequiresHeaderAndDomain` (Phase A).

### M5. Audit-log emit (register-side)

**Files**:

- `container/runner.go` / `container/egress.go`: after egred POST,
  call `auditFn(spawnID, userSub, envName, host, "register")` per
  inject entry.
- `gateway/gateway.go`: bind `Input.AuditFn` to
  `g.store.LogSecretRegister`.
- `container/runner.go` `Input`: add `AuditFn` field (`json:"-"`).

**Tests** (`container/audit_test.go`, new):
`TestRun_EmitsAuditPerInjectEntry`.

### M6. Release

Per `CLAUDE.md:220-258`.

- `CHANGELOG.md`: prepend release block (`>` blockquote ≤ 9 lines,
  3-6 bullets) plus `### Added/Changed/Schema` sections.
- `ant/skills/self/migrations/118-vX.Y.0-user-secret-overlay.md`
  (new; stub body fine).
- `ant/skills/self/MIGRATION_VERSION`: `117` → `118`.
- `ant/skills/self/SKILL.md`: bump "Latest migration version".
- `git tag vX.Y.0`; tag docker images.
- `.diary/YYYYMMDD.md`.

**Verify**: `make build && make lint && make test && make test-e2e`;
deploy to krons; `make smoke SMOKE_INSTANCE=krons`; e2e identity-OAuth
→ POST `GITHUB_TOKEN` → Slack turn → GitHub API call returns user
data.

### Integration test (`tests/integration/user_secret_e2e_test.go`)

1. Seed `auth_users` row for `google:alice`, `user_jids` linking
   `google:alice ↔ slack:T1/U1`.
2. POST to `/dash/me/secrets` with `X-User-Sub: google:alice`.
3. Trigger gateway spawn with `chatJID=slack:T1/U1` (group chat) via
   fake docker that captures env.
4. Assert env contains placeholder, not real value.
5. Assert fake-egred admin endpoint received the secret with right
   placeholder, value, header, domain.
6. Assert `secret_register_log` has one row keyed to spawn.

Edge cases: identity-link no-op (no `user_jids` row → only folder
secrets, no user-keyed log row); cross-user isolation (two
sequential spawns → distinct placeholders).

### Rollback

| Step                      | Reversibility                                         |
| ------------------------- | ----------------------------------------------------- |
| M0 wire-format            | Forward-compatible (`omitempty`).                     |
| M1 schema                 | Additive only; no down-migration.                     |
| M2 `resolveSpawnEnv` lift | Pure code revert.                                     |
| M3 dashd routes           | Pure code revert; routes 404.                         |
| M4 CLI                    | Pure code revert.                                     |
| M5 audit emit             | Pure code revert; table remains.                      |
| M6 release                | `git tag -d`; revert CHANGELOG + `MIGRATION_VERSION`. |

### Critical-path estimate

| Day | Work                                                            | Milestones     |
| --- | --------------------------------------------------------------- | -------------- |
| 1   | Wire-format + selective MITM PoC + CA mount.                    | M0 (if needed) |
| 2   | M0 finish + tests.                                              | M0             |
| 3   | M1 schema + store extensions. M2 lift + placeholder + register. | M1 + M2        |
| 4   | M3 dashd CRUD + CSRF. M5 audit emit.                            | M3 + M5        |
| 5   | M4 CLIs. Integration test. M6 release + krons deploy + smoke.   | M4 + M6        |

If M0 already done: collapse to 3 days.
