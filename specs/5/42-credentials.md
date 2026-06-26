---
status: draft
depends:
  [
    5-uniform-mcp-rest,
    5/41-ext-mcp,
    specs/4/9-acl-unified,
    specs/11/14-surrogate-oauth,
  ]
supersedes:
  [
    specs/5/41 §Secrets-table,
    specs/5/32 §Phase-C-secrets,
    specs/7/E §Anthropic-keys,
  ]
---

# specs/5/42 — credential model

Three credential types that must not share an abstraction.
Conflating them caused silent injection bugs and a wrong `scope_kind` split.

---

## Three types

### 1. Env-profile keys

`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`.

- **Owner**: user only. Never folder-scoped, never operator-table-scoped.
- **Storage**: `secrets(scope_kind='user', scope_id=user_sub, key, value)` —
  same table as capability credentials. Store layer rejects
  `scope_kind='folder'` for these keys at write time.
- **Injection**: at container spawn. `mergeSecrets` layers user override on
  top of operator platform fallback. Claude Code CLI reads from container env
  directly — cannot be broker-injected at call time.
- **Platform fallback**: operator `.env` via `readSecrets()`. NOT the table.
- **Enforcement**: store layer rejects any write of these keys to `secrets`
  or with `scope_kind='folder'`.

### 2. Capability credentials

`GITHUB_TOKEN`, `BUILDKITE_TOKEN`, `CF_API_TOKEN`, and any third-party API key.

- **Owner**: user. Grant determines who else can trigger tool calls
  that consume the key — but the credential always belongs to the user.
- **Storage**: `secrets(scope_kind='user', scope_id=user_sub, key, value)`.
  Folder scope allowed only for shared team keys (e.g. a GitHub org token
  for a whole team) — user-scoped wins over folder-scoped.
- **Injection**: today — spawn-time, via `RunRequest.Secrets → mergeSecrets`
  (same path as env-profile keys, interim). Target — call-time broker per
  shape 3 (spec 41): arizuko makes the HTTP call, key never lands in
  container env. For MCP subprocess connectors, `ConnectorSecrets` narrows
  to declared keys at call time.
- **Resolution**: `ConnectorSecrets(folder, callerSub)` →
  `FolderSecretsForUser(folder, callerSub)`. The triggering user's key
  resolves, never a folder-level default unless the user has no override.
- **Tool visibility**: a connector tool appears in MCP `tools/list` only
  for sessions where `Authorize(folder, "mcp:"+tool)` passes.

### 3. Infra / operator credentials

`CHANNEL_SECRET`, `AUTH_SECRET`, `SLACK_BOT_TOKEN`, `TELEGRAM_BOT_TOKEN`, etc.

- **Owner**: operator. Never user-accessible, never in the `secrets` table.
- **Storage**: host `.env` only.
- **Injection**: adapter tokens read by each daemon from env at boot.
  Operator anchors read by `container/runner.go:readSecrets()` at spawn.

---

## Storage model

| type        | table       | scope                                                  | user write         | operator write                           |
| ----------- | ----------- | ------------------------------------------------------ | ------------------ | ---------------------------------------- |
| env-profile | `secrets`   | `scope_kind='user'` only (folder rejected at store)    | `/dash/me/env`     | `.env` (platform fallback)               |
| capability  | `secrets`   | `scope_kind='user'` (or `folder` for shared team keys) | `/dash/me/secrets` | `arizuko secret <inst> set <folder> KEY` |
| infra       | host `.env` | n/a                                                    | never              | `.env` or systemd override               |

---

## Resolution chain

**At dispatch** (`routd/dispatch.go:dispatchRun`):

```
callerSub = last.Sender       // real-user trigger → user+folder resolution
           "service:routd"    // timed-* / system → folder scope only
```

**At container spawn**, two layers merged:

```
mergeSecrets(
  readSecrets(),                          // operator anchors (host env)
  FolderSecretsForUser(folder, caller),   // capability creds: folder walk + user overlay
)
```

Env-profile keys arrive via the same `mergeSecrets` path only when the
user has set a personal override in `secrets(scope_kind='user')`; otherwise
the operator platform key from `readSecrets()` is the fallback.

**At MCP connector tool-call**, narrowed (after the ConnectorSecrets fix):

```
ConnectorSecrets(folder, callerSub)
  = FolderSecretsForUser(folder, callerSub) narrowed to connector.Secrets
```

`callerSub` is captured from `turnMCP.trigger` in `routd/mcp.go:buildStoreFns`
— the same resolved caller used at dispatch. It is NOT the ACL principal
(`"folder:"+folder`) used in `ServeTurnMCP:callerSub`; those serve different
purposes.

**`chats.kind` gate removed.** Spec 32 gated user-secret overlay on
`chats.kind ∈ {dm, slink}`. Dropped: what matters is whether `callerSub`
is a real user sub, not the chat shape. Group chats and DMs resolve the
same way.

---

## Grant model for capability credentials

The user who sets a key implicitly holds `mcp:<connector>:<tool>` for
any tool that key enables. Sharing with a folder:

```
acl(principal=folder:<path>, action=mcp:github:*, params=nil, effect=allow)
```

The credential always resolves from the **triggering user's** `callerSub`.
The folder grant controls _who may invoke the tool_; it does not transfer
key ownership.

Two trigger scenarios:

| trigger                 | callerSub       | key resolved from                          |
| ----------------------- | --------------- | ------------------------------------------ |
| real user (DM or group) | user JID        | user-scoped row; folder fallback if absent |
| cron / timed-\*         | `service:routd` | folder-scoped row only (shared team key)   |

Cron tasks that need a capability credential require a folder-scoped key
set explicitly by the operator — they cannot inherit a user's personal key.

---

## Tool announcement (grant-gated tools/list)

Target: a connector tool appears in `tools/list` only for sessions where
`Authorize(folder, "mcp:"+localName)` passes. Agents see only what they
can use. The user says "use GitHub" → tool is discoverable only because
the grant exists.

Current state: `ipc/ipc.go:1019` registers all connector tools
unconditionally via `granted()`; the grant check fires only at call time.
Tools are visible to agents even without a grant. Not yet fixed — requires
per-session tool-list filtering at `ipc.ServeMCP` or a dynamic MCP
tool-list hook.

---

## Write paths

### /dash/me/secrets — capability credentials

Existing. `scope_kind='user'`, `scope_id=caller.sub`. Add:

- Reject env-profile key names at the handler with a clear error pointing to
  `/dash/me/env`.

### /dash/me/env — env-profile keys (PROPOSED)

New endpoint or separate section within `/dash/me/secrets`. Writes to
`secrets(scope_kind='user')` with env-profile key names.

- Keys allowed: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`,
  `OPENAI_API_KEY`, `CODEX_API_KEY`.
- UI: separate section in dashd profile — "Model API keys — injected into
  your agent container at spawn".
- Operator fallback shown read-only: "Platform key active" when no user
  override exists.
- Store layer rejects these key names at `scope_kind='folder'` regardless
  of call path.

---

## What's shipped

| piece                                                     | location                                              | state           |
| --------------------------------------------------------- | ----------------------------------------------------- | --------------- |
| `secrets` table + AES-256-GCM                             | `store/secrets.go`                                    | ✓               |
| Env-profile key enforcement at store layer                | `store/secrets.go:EnvProfileKeys`, `validateScope`    | ✓               |
| `FolderSecretsResolvedForUser`                            | `store/secrets.go`, `routd/dispatch.go`               | ✓               |
| Spawn-time capability inject (interim: via container env) | `routd/dispatch.go`, `container/runner.go`            | ✓               |
| `ConnectorSecrets` user-scope (callerSub threaded)        | `routd/sibling_db.go`, `routd/mcp.go:buildStoreFns`   | ✓               |
| Grant-gated `tools/list`                                  | `ipc/ipc.go:1019` Authorize check before registration | ✓               |
| dashd `/dash/me/secrets` HTML + JSON                      | `dashd/me_secrets.go`                                 | ✓               |
| dashd `/dash/me/env` HTML + JSON                          | `dashd/me_env.go`                                     | ✓               |
| Operator CLI `arizuko secret`                             | `cmd/arizuko/secret.go`                               | ✓               |
| OAuth write path                                          | `specs/11/14-surrogate-oauth.md`                      | ✓               |
| Shape 3 REST descriptor                                   | `ipc/extcall.go`, `routd/ext.go`, built-in providers  | ✓ (see spec 41) |

## What's not shipped

All credential-model pieces from this spec are shipped. Remaining gaps belong
to spec 41 (`registerWithSecrets` for Go handlers, `secret_use_log` per-key
audit rows).

---

## Supersedes

- `specs/5/41-ext-mcp.md` §Secrets table — replaced by this doc; 41 keeps
  handler shapes (subprocess connector, REST descriptor) only.
- `specs/5/32-tenant-self-service.md` §Phase-C §credentials §user-secret-injection —
  `chats.kind` gate and scope model replaced here.
- `specs/7/E-encryption-at-rest.md` §Anthropic-keys — operator anchors are
  host env, not secrets table; only user BYOA overrides land in the table.
