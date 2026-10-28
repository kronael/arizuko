---
status: shipped
depends:
  [17-openapi-mcp, 5/13-ext-mcp, specs/5/32-acl-unified, 5/15-surrogate-oauth]
supersedes:
  [
    specs/5/13 §Secrets-table,
    specs/5/5 §Phase-C-secrets,
    specs/8/E §Anthropic-keys,
  ]
---

# specs/5/14 — credential model

Three credential types that **must not share an abstraction**. Conflating them
caused silent injection bugs and a wrong `scope_kind` split.

## 1. Env-profile keys

`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`,
`CODEX_API_KEY` — the canonical set is `store.EnvProfileKeys`
(`store/secrets.go:103`).

- **Owner**: the user only. Never folder-scoped.
- **Storage**: `secrets(scope_kind='user', scope_id=user_sub, …)`. The store
  layer **rejects** these key names at `scope_kind='folder'` regardless of call
  path (`store/secrets.go:118` `validateScope`) — the enforcement point, not a
  handler convention.
- **Injection**: at container spawn, and this type is the ONLY table-backed
  credential that gets there. The Claude Code CLI spends the key from its own
  process env, so there is no tool call to attach a broker to. `routd.DB`
  exposes exactly one reader for it — `EnvProfileSecrets(userSub)`
  (`routd/sibling_db.go`) — which queries `scope_kind='user'` and keeps only
  `EnvProfileKeys`, so no folder row and no capability credential can reach
  `RunRequest.Secrets`. The narrowing lives in that reader, not at the dispatch
  call site, because a call-site filter leaves an exported bulk reader for the
  next writer of `RunRequest.Secrets` to reopen the hole with.
- **Platform fallback**: the operator's `.env` via `readSecrets()`, NOT the
  table. A user row overrides it.

> Omitting a canonical key from the generated `env/runed.env` caused a
> fleet-wide `/login` outage after the v0.62 regeneration. The generator must
> carry the full `EnvProfileKeys` set.

## 2. Capability credentials

`GITHUB_TOKEN`, `CF_API_TOKEN`, and any third-party API key.

- **Owner**: the user. A grant decides who else may trigger tool calls that
  consume the key; the credential still belongs to the user.
- **Storage**: `secrets(scope_kind='user', …)`, or `folder` for a shared team
  key. User scope wins over folder.
- **Resolution**: `FolderSecretsResolvedForUser(folder, callerSub)`
  (`store/secrets.go:449`) — folder walk (deeper wins) with a user overlay. The
  **triggering user's** key resolves; a folder default only when they have none.
- **Injection**: **never into the container.** The credential reaches a tool
  only through the call-time broker (`5/13` shape 2 and 3): `ConnectorSecrets`
  narrows it to the keys that call declares, the value lands in the connector
  subprocess's env or the outbound REST request **on the host**, and it is
  scrubbed from the result. An agent that reads its whole environment finds no
  capability credential (BUGS `X1`, closed 2026-08-09).

## 3. Infra / operator credentials

`AUTH_SECRET`, `SLACK_BOT_TOKEN`, `TELEGRAM_BOT_TOKEN`, and the per-daemon
`AUTHD_SERVICE_KEY`.

- **Owner**: the operator. Never user-accessible, never in the `secrets` table.
- **Storage**: host `.env` only.
- **Injection**: read from env at daemon boot; operator anchors read by
  `container/runner.go` `readSecrets()` at spawn.

(`CHANNEL_SECRET` was an infra credential of this class and is **retired** —
adapters present a `service:<daemon>` ES256 JWT instead, `5/1`.)

## Storage summary

| type        | table       | scope                                      | user write         | operator write                              |
| ----------- | ----------- | ------------------------------------------ | ------------------ | ------------------------------------------- |
| env-profile | `secrets`   | `user` only (folder rejected at the store) | `/dash/me/env`     | `.env` (platform fallback)                  |
| capability  | `secrets`   | `user`, or `folder` for a shared team key  | `/dash/me/secrets` | `arizuko secret -i <inst> set <folder> KEY` |
| infra       | host `.env` | n/a                                        | never              | `.env` or a systemd override                |

## Who is the caller

At dispatch, `callerSub` is the real user's sender for a human-triggered turn
and `service:routd` for a `timed-*`/system turn. That single resolved caller
threads through to `ConnectorSecrets` at call time (`routd/mcp.go`
`buildStoreFns`). It is **not** the ACL principal (`"folder:"+folder`); the two
serve different purposes and must not be conflated.

Consequence: a cron task that needs a capability credential requires a
**folder-scoped** key set by the operator — it cannot inherit a user's personal
key.

**The `chats.kind` gate is removed.** An earlier design gated the user-secret
overlay on `chats.kind ∈ {dm, slink}`. What matters is whether `callerSub` is a
real user sub, not the chat shape; group chats and DMs resolve the same way.

## Grants stay orthogonal to ownership

Setting a key does not grant anything, and granting does not transfer ownership.
An `acl` row `(folder:<path>, mcp:github:*, allow)` controls _who may invoke the
tool_; the credential still resolves from the triggering user's `callerSub`.

**Tool announcement follows the grant.** A connector tool appears in `tools/list`
only for sessions where `Authorize(folder, "mcp:"+localName)` passes
(`ipc/ipc.go:987`); ext tools gate on their declared `scope`. Agents see only
what they can use, and the call-time check remains as defence in depth.

## Write paths

- **`/dash/me/secrets`** (`dashd/me_secrets.go`) — capability credentials,
  `scope_kind='user'`. Rejects env-profile key names with an error pointing at
  `/dash/me/env`.
- **`/dash/me/env`** (`dashd/me_env.go`) — env-profile keys only. Shows the
  operator fallback read-only ("Platform key active") when no user override
  exists.
- **`arizuko secret`** (`cmd/arizuko/secret.go`) — the operator path.
- **Connect** ([`5/15`](15-surrogate-oauth.md)) — the OAuth-automated writer for
  both types. It writes the same rows a paste would, plus `expires_at` and a
  refresh token.

## Encryption at rest

Storage is `store/secrets.go`. Every row in the `secrets` table is encrypted
with AES-256-GCM. The stored form is `"v2:" + base64(nonce||ciphertext)`.

`SECRETS_KEY` supplies the key. There is no plaintext mode and no `AUTH_SECRET`
fallback. Two components enforce this:

- **`routd` refuses to start** without `SECRETS_KEY`
  (`routd/cmd/routd/main.go`, via `core.RequireSecretKeyring`). routd owns the
  table, so an instance whose owner cannot seal must not run.
- **`store.storeValue` refuses every write** when the keyring is empty, and
  returns `store.ErrNoSecretsKey`. Each write path calls it — `SetSecret`,
  `PutSecretRow`, `PutOAuthSecret` and `UpdateOAuthSecret` — so no caller can
  persist a value the store did not seal. The error travels to the caller: the
  `/dash/me/env` and `/dash/me/secrets` pages return it, and `arizuko secret`
  refuses before it opens the store.

Compose gives `SECRETS_KEY` to `routd`, to `runed`, and to `dashd`, which writes
user secrets into `routd.db` directly.

A row without the `"v2:"` prefix is a legacy plaintext row that a build before
this rule wrote. Reads still open it. No write path creates one.
`store.EncryptPlaintextSecrets` seals such rows in place, but no daemon calls
it yet — an operator seals a legacy row by writing it again.

> An earlier build only logged a warning when `SECRETS_KEY` was absent, and the
> store then wrote the plaintext. An un-keyed instance persisted every secret
> readable on disk while each healthcheck reported it healthy (BUGS `S2`).

### Rotation

`SECRETS_KEY` holds one key, or several keys separated by commas
(`core.SecretKeyring`). **The first key seals all new writes. The other keys
only decrypt.**

To rotate, put the new key first and keep the old key after it:

```
SECRETS_KEY=<new-key>,<old-key>
```

Each new write then seals under `<new-key>`. A read tries every key in the
keyring until one authenticates the value, so rows sealed under `<old-key>`
stay readable. Writing a secret again re-seals it under the active key, which is
how an operator moves a row onto `<new-key>`. Remove `<old-key>` from the list
only after every row has been written again — a value the keyring cannot open
reads as an error, not as plaintext.

## Supersedes

- [`5/13`](13-ext-mcp.md) §Secrets table — replaced here; `5/13` keeps the
  handler shapes only.
- [`5/5`](5-worlds-agents-sessions.md) §Secrets (Phase C) — the `chats.kind`
  gate and scope model are replaced here.
- [`8/E`](../8/E-encryption-at-rest.md) §Anthropic-keys — operator anchors are
  host env, not the secrets table; only user BYOA overrides land in the table.
