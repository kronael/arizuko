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
- **Storage**: `user_env(user_sub, key, value)` — separate from `secrets`.
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
- **Injection**: broker at tool-call time only. Narrowed to the keys a
  specific connector declared — the subprocess never sees the full map.
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
| env-profile | `user_env`  | user only                                              | `/dash/me/env`     | `.env` (platform fallback)               |
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

Env-profile keys (Tier 1) arrive via the same `mergeSecrets` path only
when the user has set a personal override in `user_env`; otherwise the
operator platform key from `readSecrets()` is the fallback.

**At connector tool-call**, narrowed:

```
ConnectorSecrets(folder, callerSub)
  = FolderSecretsForUser(folder, callerSub) narrowed to connector.Secrets
```

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

A connector tool must only appear in `tools/list` for sessions where
`Authorize(folder, "mcp:"+localName)` passes. Agents must not see tools
they have no grant for.

Current bug: `ipc/ipc.go:1019` registers all connector tools
unconditionally; the grant fires only at call time. Fix: filter at
`ipc.NewSession` or via the MCP server's dynamic tool-list hook.

Agent preamble pattern: the agent learns which external tools it has access
to from the filtered `tools/list` — no explicit preamble injection needed.
The user says "use GitHub" → agent discovers the tool exists and uses it.

---

## Write paths

### /dash/me/secrets — capability credentials

Existing. `scope_kind='user'`, `scope_id=caller.sub`. Add:

- Reject env-profile key names at the handler with a clear error pointing to
  `/dash/me/env`.

### /dash/me/env — env-profile keys (PROPOSED)

New endpoint. Same CRUD shape as `/dash/me/secrets`. Writes to `user_env`.

- Keys allowed: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`,
  `OPENAI_API_KEY`, `CODEX_API_KEY`.
- UI: separate section in dashd profile — "Model API keys — injected into
  your agent container at spawn".
- Operator fallback shown read-only: "Platform key active" when no user
  override is set.

---

## What's shipped

| piece                                                     | location                                   | state |
| --------------------------------------------------------- | ------------------------------------------ | ----- |
| `secrets` table + AES-256-GCM                             | `store/secrets.go`                         | ✓     |
| `FolderSecretsResolvedForUser`                            | `store/secrets.go`, `routd/dispatch.go`    | ✓     |
| Spawn-time capability inject (interim: via container env) | `routd/dispatch.go`, `container/runner.go` | ✓     |
| dashd `/dash/me/secrets` HTML + JSON                      | `dashd/me_secrets.go`                      | ✓     |
| Operator CLI `arizuko secret`                             | `cmd/arizuko/secret.go`                    | ✓     |
| OAuth write path                                          | `specs/11/14-surrogate-oauth.md`           | ✓     |

## What's not shipped

| piece                           | gap                                                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `user_env` table                | env-profile keys land in `secrets`; no enforcement of user-only scope                                                                 |
| `/dash/me/env` endpoint         | no UI distinction between env-profile and capability keys                                                                             |
| `ConnectorSecrets` user-scope   | **bug**: `sibling_db.go:ConnectorSecrets` calls `FolderSecrets` (folder only) — user BYOA capability key never reaches MCP subprocess |
| `callerSub` in connector path   | not threaded into `ipc/ipc.go:1027` dispatch                                                                                          |
| Grant-gated `tools/list`        | all connectors announced unconditionally                                                                                              |
| Env-profile key reject at store | writing `ANTHROPIC_API_KEY` with `scope_kind='folder'` silently succeeds                                                              |
| Shape 3 REST descriptor         | `[[ext]]` TOML loader + HTTP dispatcher (see spec 41)                                                                                 |

---

## Supersedes

- `specs/5/41-ext-mcp.md` §Secrets table — replaced by this doc; 41 keeps
  handler shapes (subprocess connector, REST descriptor) only.
- `specs/5/32-tenant-self-service.md` §Phase-C §credentials §user-secret-injection —
  `chats.kind` gate and scope model replaced here.
- `specs/7/E-encryption-at-rest.md` §Anthropic-keys — operator anchors are
  host env, not secrets table; only user BYOA overrides land in the table.
