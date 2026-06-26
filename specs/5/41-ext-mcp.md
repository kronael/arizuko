---
status: partial
depends:
  [5-uniform-mcp-rest, specs/4/9-acl-unified, specs/5/32-tenant-self-service]
---

# specs/5/41 — external capability injection

> arizuko is the broker between agents and the web. Credentials never enter
> the container. Every external call is governed by grants and written to the
> audit log. The agent invokes a tool by name; arizuko resolves the
> credential, calls the external service, and returns the result.

This is a core arizuko primitive — the same mechanism that lets a group
agent manage DNS records, open a GitHub PR, send a transactional email, or
call any API the operator configures. The agent sees a tool. arizuko owns
the credential.

---

## The dispatch chain

All handler shapes share one chain:

```
agent MCP call
       │
       ▼
  GrantsCheck       auth.Authorize("mcp:"+toolName) — may this folder call this tool?
       │
       ▼
  InjectSecrets     resolve folder/user secrets → map[string]string (never logged)
       │
       ▼
  Recover/Timeout
       │
       ▼
  Handler           Go function  |  REST call  |  MCP subprocess
       │
       ▼
  Audit             audit_log row: folder, tool, scope, status, latency_ms
       │
       ▼
  result to agent   (secret values scrubbed from response)
```

---

## Secrets table

`secrets(scope_kind, scope_id, key, value, created_at)`
PK `(scope_kind, scope_id, key)`.

- `scope_kind ∈ {folder, user}`; `scope_id` = folder path or `auth_users.sub`
- **Resolution**: folder-ancestry walk, deepest child wins. Per-user override
  (`user` scope row beats `folder` scope row for same key) resolved by
  `store/secrets.go:FolderSecretsResolvedForUser` (folder + user scope;
  user wins).
- **Encryption at rest**: plaintext by default; AES-256-GCM when `SECRETS_KEY`
  set — stored `v2:base64(nonce‖ct)`, decrypted transparently on read.
  Enabling runs a one-time idempotent encrypt-in-place migration.
- Never injected into container env. `ANTHROPIC_API_KEY` and other operator
  anchors are separate (container env, not this table).

### Write paths

| surface            | who      | how                                                           |
| ------------------ | -------- | ------------------------------------------------------------- |
| Operator CLI       | operator | `arizuko secret <inst> set <folder> KEY --value V`            |
| dashd self-service | end user | `GET/POST/PATCH/DELETE /dash/me/secrets`                      |
| OAuth dance        | platform | `specs/11/14-surrogate-oauth.md` — writes access+refresh here |

---

## Handler shape 1 — Go handler (built-in tools)

```go
registerWithSecrets("github_pr",
    "Create or list pull requests on a GitHub repo.",
    []string{"GITHUB_TOKEN"},
    params,
    func(ctx context.Context, req mcp.CallToolRequest,
         secrets map[string]string) (*mcp.CallToolResult, error) {
        token := secrets["GITHUB_TOKEN"]
        if token == "" {
            return toolErr("no GITHUB_TOKEN — set at /dash/me/secrets")
        }
        // outbound HTTP using token; value never logged or returned
    })
```

`registerWithSecrets` is not yet implemented — the plumbing for passing
`secrets map[string]string` to plain (non-connector) Go handlers is the
missing piece alongside per-user resolution.

---

## Handler shape 2 — MCP subprocess connector (SHIPPED)

Third-party services ship their own MCP server; arizuko spawns it per call
as a stdio subprocess. No Go handler needed.

### connectors.toml

```toml
[[mcp_connector]]
name         = "github"
command      = ["docker", "run", "-i", "--rm", "ghcr.io/anthropic/mcp-github"]
secrets      = ["GITHUB_TOKEN"]
env_template = { GITHUB_PERSONAL_ACCESS_TOKEN = "{secret:GITHUB_TOKEN}" }
scope        = "per_call"   # subprocess lifetime; "per_session" pools per caller
```

### Lifecycle

1. Boot: `DiscoverConnectorTools` spawns with empty env, calls `tools/list`,
   caches catalog. Each tool registered as `<connector>_<remote_name>` with
   `RequiresSecrets` = connector's `secrets` list. (`ipc/connector.go:74`)
2. Per call: `ConnectorSecrets(folder, required)` narrows the folder secret map
   to only the keys the connector declared — connector never sees the full
   folder secret set. (`routd/sibling_db.go:119`)
3. Dispatch: env_template rendered with resolved values, subprocess spawned,
   `tools/call` proxied, result returned. (`ipc/connector.go:120`)
4. Scrub: known secret values stripped from result JSON before returning
   to agent.
5. Teardown: `per_call` subprocesses torn down immediately; `per_session`
   pooled per `(connector, caller.sub)`, never shared across users.

### Code path (shipped)

```
routd/mcp.go:569    ResolveConnectorSecrets: s.db.ConnectorSecrets
ipc/ipc.go:1027     if db.ResolveConnectorSecrets != nil && tool.Connector != nil {
                        secrets = db.ResolveConnectorSecrets(folder, tool.Connector.Secrets)
ipc/ipc.go:1031         return CallConnectorTool(ctx, tool, req.GetArguments(), secrets)
```

Grants: `auth.Authorize("mcp:"+localToolName)` — the `mcp:` prefix triggers
tier-default grant derivation in `auth/authorize.go:102`.

---

## Handler shape 3 — REST descriptor (UNSHIPPED)

Declarative TOML mapping tool names to REST endpoints. No subprocess, no
Go handler — arizuko makes the HTTP call directly. Targets providers that
don't ship an MCP server (Porkbun, Gandi, Namecheap, etc.).

```toml
[[ext]]
name = "cloudflare"
base = "https://api.cloudflare.com/client/v4"

  [ext.auth]
  method = "bearer"        # bearer | apikey-header | apikey-query | basic | dual-key
  secret = "CF_API_TOKEN"  # key in secrets table (folder-scoped)
  # apikey-header: header = "X-Api-Key"
  # apikey-query:  param = "api_key"
  # dual-key: secret2 = "CF_SECRET_KEY", header2 = "X-Secret-API-Key"

  [[ext.tool]]
  name   = "dns_list"
  scope  = "ext:cloudflare:dns:read"
  method = "GET"
  path   = "/zones/{zone_id}/dns_records"

  [[ext.tool]]
  name   = "dns_set"
  scope  = "ext:cloudflare:dns:write"
  method = "POST"
  path   = "/zones/{zone_id}/dns_records"

  [[ext.tool]]
  name   = "dns_delete"
  scope  = "ext:cloudflare:dns:write"
  method = "DELETE"
  path   = "/zones/{zone_id}/dns_records/{id}"
```

Auth wire forms:

| method          | wire                                           |
| --------------- | ---------------------------------------------- |
| `bearer`        | `Authorization: Bearer <secret>`               |
| `apikey-header` | configurable header name                       |
| `apikey-query`  | configurable query param name                  |
| `basic`         | `Authorization: Basic base64(user:secret)`     |
| `dual-key`      | two secrets injected as separate named headers |

### Built-in provider definitions

Operators supply only the API key; tool schemas ship with arizuko:

| provider   | auth         | secret key(s)                      | notes                            |
| ---------- | ------------ | ---------------------------------- | -------------------------------- |
| Cloudflare | bearer       | `CF_API_TOKEN`                     | zone-scoped DNS, Workers, KV     |
| Porkbun    | dual-key     | `PB_API_KEY` + `PB_SECRET`         | domain-scoped                    |
| Gandi      | bearer       | `GANDI_PAT`                        | livedns API                      |
| Namecheap  | apikey-query | `NAMECHEAP_KEY`                    | requires IP whitelist on account |
| Route53    | AWS SigV4    | `AWS_ACCESS_KEY_ID` + `AWS_SECRET` | needs SigV4 — low priority       |

Path: `ext/providers/<name>.toml` shipped with the binary; merged with any
operator-defined `[[ext]]` blocks at boot.

### Grants for REST tools

```
ext:cloudflare:dns:write    # allow Cloudflare DNS writes for this folder
!ext:cloudflare:*           # deny all Cloudflare tools
ext:*:dns:read              # read-only DNS across any registered service
```

The tool's `scope` field IS the grant string checked at call time. Same
ACL engine and glob syntax as `4/9-acl-unified`.

---

## Grants summary

| handle shape    | grant prefix                | example                          |
| --------------- | --------------------------- | -------------------------------- |
| Go handler      | any custom string           | `github:pr:write`                |
| MCP subprocess  | `mcp:<connector>:<tool>`    | `mcp:github:create_pull_request` |
| REST descriptor | `ext:<service>:<operation>` | `ext:cloudflare:dns:write`       |

All checked via `auth.Authorize` before any handler fires.

---

## Audit

Every tool call through this layer writes to `audit_log`:

```
(ts, folder, caller_sub, tool, scope_kind, scope_id, key, status, latency_ms)
```

`scope_kind ∈ {user, folder, missing}`. `status ∈ {ok, err, timeout}`.
Secret values never written. One row per `(call × resolved key)`.

The `secret_use_log` table design from the old broker spec is the target
shape; it is **not yet created** — the current `audit_log` table does not
include per-secret rows. This is the M2 gap.

---

## Trust model

| scope            | where                                          | reaches agent?                                                                                                                             |
| ---------------- | ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| Operator anchors | container env (`ANTHROPIC_API_KEY`, bot creds) | yes — Claude Code CLI needs them (set in `.env`, injected at spawn via `container/runner.go:readSecrets()`; separate from `secrets` table) |
| Folder secrets   | `secrets` table, broker only                   | no                                                                                                                                         |
| Per-user secrets | `secrets` table, broker only                   | no                                                                                                                                         |

Three escape paths closed by the broker:

1. **Tool result echoes the token** — broker scrubs known secret values from
   `mcp.CallToolResult` before returning to agent (exact-string match on
   the declared keys for that call).
2. **Subprocess stderr** — routed to a gated-owned sink, never reaches agent.
   `slog.Debug` under connector name (`ipc/connector.go:196`).
3. **Agent steers the tool to leak** — connector registration is
   operator-only; agents cannot add connectors or new REST descriptors.

---

## DNS use case (motivating example)

Setting up email for a new group requires MX, SPF, DKIM, DMARC. With this
spec, the agent runs:

```
dns_set(zone_id="abc123", name="@",    type="MX",  content="mail.host.com", priority=10)
dns_set(zone_id="abc123", name="@",    type="TXT", content="v=spf1 a mx ~all")
dns_set(zone_id="abc123", name="mail", type="A",   content="1.2.3.4")
```

`CF_API_TOKEN` lives in the folder secrets table. The agent never sees it.
Grant row `ext:cloudflare:dns:write` scoped to the folder is what enables
the calls. Every call appears in `audit_log`.

---

## What's shipped

| piece                           | location                                              | state                                      |
| ------------------------------- | ----------------------------------------------------- | ------------------------------------------ |
| `secrets` table + migrations    | `store/secrets.go`, `0034-secrets.sql`                | ✓ shipped                                  |
| AES-256-GCM at rest             | `store/secrets.go` `seal`/`open`                      | ✓ shipped (opt-in)                         |
| `FolderSecretsResolvedForUser`  | `store/secrets.go`, `routd/dispatch.go (dispatchRun)` | ✓ shipped (folder + user scope; user wins) |
| Connector discovery + dispatch  | `ipc/connector.go`                                    | ✓ shipped                                  |
| `ConnectorSecrets`              | `routd/sibling_db.go:119`                             | ✓ shipped                                  |
| `ResolveConnectorSecrets` wired | `routd/mcp.go:569`                                    | ✓ shipped                                  |
| `connectors.toml` loader        | `gateway/connectors.go`                               | ✓ shipped                                  |
| dashd `/dash/me/secrets`        | `dashd/`                                              | ✓ shipped                                  |
| Operator CLI                    | `cmd/arizuko/secret.go`                               | ✓ shipped                                  |

## What's not yet shipped

| piece                                 | gap                                                                            |
| ------------------------------------- | ------------------------------------------------------------------------------ |
| `registerWithSecrets` for Go handlers | plain (non-connector) built-in tools can't receive `secrets map[string]string` |
| REST descriptor layer                 | `[[ext]]` TOML loader, HTTP dispatcher, path param substitution                |
| Built-in provider definitions         | `ext/providers/*.toml` files                                                   |
| `secret_use_log` per-key audit rows   | current `audit_log` has no per-secret granularity                              |

---

## Out of scope

- OAuth token dance + refresh — `specs/11/14-surrogate-oauth.md` (writes
  access token into the `secrets` table the broker reads)
- Hosted-remote MCP servers (e.g. `mcp.linear.app/mcp`) — need HTTP
  upstream proxy mode, not stdio subprocess; design TBD
- Per-tool secret-scope overrides (refuse folder fallback) — add
  `MCPTool.SecretScopes` if needed, not v1
- HSM / KMS integration
- MITM-isolated egress for opaque HTTP clients — `specs/7/Z-egred-mitm.md`
  (additive: catches clients the broker can't reach)
