---
status: partial
depends: [17-openapi-mcp, specs/4/9-acl-unified, specs/5/5-tenant-self-service]
---

# specs/5/13 — external capability injection

> arizuko is the broker between agents and the web. Capability credentials
> stay with the broker — the target is they never enter the container (§Trust
> model records the spawn-env interim, BUGS X1). Every external call is governed
> by grants and written to the audit log. The agent invokes a tool by name;
> arizuko resolves the credential, calls the external service, and returns the
> result.

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

Ownership model, resolution chain, and write paths moved to
[`specs/5/14-credentials.md`](14-credentials.md). The handler shapes
below remain here.

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

## Handler shape 3 — REST descriptor (SHIPPED)

Declarative TOML mapping tool names to REST endpoints. No subprocess, no
Go handler — arizuko makes the HTTP call directly. Targets providers that
don't ship an MCP server (Porkbun, Gandi, Namecheap, etc.).

```toml
[[ext]]
name = "cloudflare"
base = "https://api.cloudflare.com/client/v4"

  [ext.auth]
  method = "bearer"        # bearer | apikey-header | apikey-query | basic | json-body
  secret = "CF_API_TOKEN"  # key in secrets table (folder-scoped)
  # apikey-header: header = "X-Api-Key"
  # apikey-query:  param = "api_key"
  # json-body: secret2 = "PB_SECRET", header/header2 = JSON body field names

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

| method          | wire                                          |
| --------------- | --------------------------------------------- |
| `bearer`        | `Authorization: Bearer <secret>`              |
| `apikey-header` | configurable header name                      |
| `apikey-query`  | configurable query param name                 |
| `basic`         | `Authorization: Basic base64(user:secret)`    |
| `json-body`     | secret(s) injected into the JSON request body |

### Built-in provider definitions

Operators supply only the API key; tool schemas ship with arizuko:

| provider   | auth         | secret key(s)                      | notes                            |
| ---------- | ------------ | ---------------------------------- | -------------------------------- |
| Cloudflare | bearer       | `CF_API_TOKEN`                     | zone-scoped DNS, Workers, KV     |
| Porkbun    | json-body    | `PB_API_KEY` + `PB_SECRET`         | domain-scoped                    |
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

The `secret_use_log` table EXISTS (`routd/migrations/0008-secrets.sql`;
store `0048`) and `audit/audit.go` reads it — but `store.LogSecretUse`
has **zero production callers**, so no per-secret row is ever written.
The M2 gap is the missing WRITER at the broker path, not the table.

---

## Trust model

| scope                       | where                                          | reaches agent?                                                                                                                                                                                |
| --------------------------- | ---------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operator anchors            | container env (`ANTHROPIC_API_KEY`, bot creds) | yes — Claude Code CLI needs them (set in `.env`, injected at spawn via `container/runner.go:readSecrets()`; separate from `secrets` table)                                                    |
| Folder secrets (capability) | `secrets` table                                | **target: no** (broker-only). **Interim today: yes** — spawn-injected via container env (`5/14` §Injection "interim", BUGS X1). Realizing the target = inject only `EnvProfileKeys` at spawn. |
| Per-user secrets            | `secrets` table, broker only                   | no                                                                                                                                                                                            |

Three escape paths closed by the broker:

1. **Tool result echoes the token** — broker scrubs known secret values from
   `mcp.CallToolResult` before returning to agent (exact-string match on
   the declared keys for that call).
2. **Subprocess stderr** — routed to a broker-owned sink, never reaches agent.
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

Handler shapes only — the credential model (secrets table, resolution,
write paths) lives in [`5/14`](14-credentials.md).

| piece                          | location                                                       | state |
| ------------------------------ | -------------------------------------------------------------- | ----- |
| Connector discovery + dispatch | `ipc/connector.go`, `routd/connectors.go`                      | ✓     |
| REST descriptor layer          | `ipc/extcall.go`, `routd/ext.go`                               | ✓     |
| Built-in provider definitions  | `routd/extproviders/{cloudflare,porkbun,gandi,namecheap}.toml` | ✓     |
| `connectors.toml` loader       | `routd/connectors.go`                                          | ✓     |
| `[[ext]]` loader               | `routd/ext.go:LoadExtProviders`                                | ✓     |

## What's not yet shipped

| piece                                 | gap                                                                                    |
| ------------------------------------- | -------------------------------------------------------------------------------------- |
| `registerWithSecrets` for Go handlers | plain (non-connector) built-in tools can't receive `secrets map[string]string`         |
| `secret_use_log` per-key audit rows   | table + reader exist; `store.LogSecretUse` never called — no writer at the broker path |

---

## Capability layer model (settled 2026-07-27)

Skills and connectors are **operator-defined and global**. Groups USE the
capability surface; they do not define it.

- `ant/skills/` — global, seeded into every container at spawn. Operator
  adds skills; users cannot. Changing skills = changing the capability
  surface, which is an operator concern (security boundary).
- `connectors.toml` / `mcp_connectors` resreg resource — global,
  operator-defined. All MCP connectors live at the deployment layer.
- **Per-group `MCP.json` is dropped.** Groups get access to connectors
  via grant rows, not via per-folder MCP registration files. A group that
  needs the `candles` connector gets a grant row `mcp:candles:*`; it does
  not register its own MCP endpoint.
- Single-user deployment: user IS the operator, adds skills/connectors
  freely. No conflict — the security boundary only applies when users ≠
  operator.

This closes the trust gap where a group agent could register an arbitrary
MCP server via its own `MCP.json` and route calls through it.

## Handler shape 4 — HTTP upstream MCP (planned)

Replaces stdio subprocess for connectors that ship as long-running HTTP
services (sidecar MCP servers, hosted providers like `mcp.linear.app/mcp`):

```toml
[[mcp_connector]]
name      = "candles"
transport = "http"
url       = "http://candles-mcp:8080"
secrets   = ["BINANCE_API_KEY"]
scope     = "per_session"
```

Same dispatch chain as shape 2: grants → inject secrets → proxy
`tools/list` + `tools/call` over HTTP → audit. No subprocess spawn.
`connectors.toml` → `mcp_connectors` resreg resource (routd.db) so
agents can `list_mcp_connectors` and operators manage via dashd — tracked
in `5/16` adoption list.

## Out of scope

- OAuth token dance + refresh — `specs/5/15-surrogate-oauth.md` (writes
  access token into the `secrets` table the broker reads)
- Per-tool secret-scope overrides (refuse folder fallback) — add
  `MCPTool.SecretScopes` if needed, not v1
- HSM / KMS integration
- MITM-isolated egress for opaque HTTP clients — `specs/8/Z-egred-mitm.md`
  (additive: catches clients the broker can't reach)
