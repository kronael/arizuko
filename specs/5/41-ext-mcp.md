---
status: draft
depends: [5-uniform-mcp-rest, specs/4/9-acl-unified]
---

# specs/5/41 — external MCP tool injection

## What this solves

Agents need to call external services (DNS providers, registrars, billing
APIs). The naive approach — store a raw API key in a folder secret and let
the agent call the HTTP API directly — is ungrantable: the agent can do
anything the key permits with no audit trail and no per-operation ACL.

This spec defines a general injection mechanism: external services are
declared as arizuko tool sets, auth is injected from folder secrets at
call time, and the existing grants DSL governs which tools the agent may
invoke.

## Existing ecosystem

Three classes of provider:

| class               | examples                              | status                         |
| ------------------- | ------------------------------------- | ------------------------------ |
| Official MCP server | Cloudflare (OAuth), Route53 (AWS IAM) | exists, no grants enforcement  |
| Community MCP       | Porkbun                               | partial, no grants enforcement |
| REST-only           | Gandi, Namecheap, most DNS APIs       | no MCP; simple API key         |

The official servers work but bypass arizuko's grants layer — the agent
sees a raw tool list with no ACL gate. The generic layer below adds that
gate uniformly across all three classes.

## Design

### Service descriptor

Operators place a service descriptor in the group's config (or as a folder
secret bundle). The descriptor maps tool names to REST endpoints:

```toml
[[ext]]
name    = "cloudflare"
base    = "https://api.cloudflare.com/client/v4"

  [ext.auth]
  method = "bearer"          # bearer | apikey-header | apikey-query | basic
  secret = "CF_API_TOKEN"    # key in folder secrets

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

### Auth methods

| method          | wire form                                                  |
| --------------- | ---------------------------------------------------------- |
| `bearer`        | `Authorization: Bearer <secret>`                           |
| `apikey-header` | `X-Api-Key: <secret>` (header name configurable)           |
| `apikey-query`  | `?api_key=<secret>` (param name configurable)              |
| `basic`         | `Authorization: Basic base64(user:secret)`                 |
| `dual-key`      | two secrets injected as separate headers (Porkbun pattern) |

OAuth flows are provider-specific and out of scope here; operators who
want OAuth-gated services (e.g. Cloudflare's own MCP) run those as
upstream MCP servers and wire them through the proxy path below.

### Grants matching

The grant DSL matches on `ext:<service>:<scope-suffix>`:

```
!ext:cloudflare:dns:write   # deny Cloudflare DNS writes
ext:cloudflare:*            # allow all Cloudflare tools
ext:*:dns:read              # allow DNS read across any service
```

The tool's `scope` field in the descriptor IS the grant string checked at
call time. No tool fires without a passing grant row for the caller's folder.
Every call is written to `audit_log`.

### Wire path

```
agent  →  MCP tool call: dns_set(zone_id=X, name=Y, type=MX, ...)
              ↓
          ipc layer: resolve ext service + check grants(caller, tool.scope)
              ↓
          inject auth from folder secrets (never sent to agent)
              ↓
          HTTP call to provider REST API
              ↓
          audit_log entry: folder, tool, params, status, latency
              ↓
          return JSON result to agent
```

### Upstream MCP proxy (for official MCP servers)

When the provider already has an MCP server (Cloudflare, Route53), the
operator can register it as an upstream instead of a REST descriptor:

```toml
[[ext]]
name     = "cloudflare-mcp"
upstream = "wss://mcp.cloudflare.com/sse"  # or local socket path

  [ext.auth]
  method = "bearer"
  secret = "CF_API_TOKEN"

  [[ext.tool]]
  name  = "workers_list"
  scope = "ext:cloudflare:workers:read"
  # upstream tool name may differ; mapped here
  upstream_tool = "list_workers"
```

arizuko proxies the MCP call through, injects auth, and gates on grants
before forwarding. The agent's view is identical to the REST descriptor
path.

## DNS use case (motivating example)

Setting up email for a new arizuko group requires:

1. MX record pointing to the host
2. SPF TXT record (`v=spf1 a mx ~all`)
3. DKIM TXT record (public key)
4. DMARC TXT record

With this spec, the agent runs:

```
dns_set(zone_id="...", name="@", type="MX", content="mail.host.com", priority=10)
dns_set(zone_id="...", name="@", type="TXT", content="v=spf1 a mx ~all")
```

No API key is visible to the agent. The operator's grant row
`ext:cloudflare:dns:write` is what enables it, scoped per folder.

## Built-in service definitions

Ship provider definitions for common DNS providers so operators only need
to supply the API key. Suggested first set:

| provider   | auth                                       | notes                 |
| ---------- | ------------------------------------------ | --------------------- |
| Cloudflare | bearer (CF_API_TOKEN)                      | zone scoped           |
| Porkbun    | dual-key (PB_API_KEY + PB_SECRET)          | domain scoped         |
| Gandi      | bearer (GANDI_PAT)                         | livedns API           |
| Namecheap  | apikey-query (NAMECHEAP_KEY)               | requires IP whitelist |
| Route53    | AWS SigV4 (AWS_ACCESS_KEY_ID + AWS_SECRET) | needs SigV4 signing   |

Route53 needs SigV4 request signing — a separate auth method; low priority,
implement after the simpler key-based providers.

## Implementation sketch

- `extd` or inline in `runed`/`routd`: loads ext descriptors from group
  config, registers tools into the agent's MCP tool list at spawn time
- `auth/ext.go`: auth injectors per method (bearer, apikey-header, etc.)
- `grants/ext.go`: `CheckExtTool(folder, scope string) bool` — delegates
  to existing ACL engine
- Built-in descriptors: `ext/providers/cloudflare.toml`, `porkbun.toml`, etc.
- Audit: every ext tool call → `audit_log` with params (secrets redacted)

## What this is NOT

- Not a general HTTP proxy for arbitrary URLs — tools must be declared
- Not a replacement for channel adapters (teled, slakd, etc.) — those are
  message channels, not capability APIs
- Not OAuth for agents — operators pre-authenticate, secrets live in the
  folder secret store
