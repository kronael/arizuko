---
status: shipped
depends: [17-openapi-mcp, specs/5/32-acl-unified, specs/5/5-tenant-self-service]
---

# specs/5/13 — external capability injection

> arizuko is the broker between agents and the web. The agent invokes a tool by
> name; arizuko resolves the credential, calls the external service, and returns
> the result. Every call is gated by an `acl` row and written to the audit log.

This is a core primitive — the same mechanism that lets a group agent manage DNS
records, open a GitHub PR, or call any API the operator configures. The agent
sees a tool; arizuko owns the credential.

The credential model itself (ownership, resolution, write paths) is
[`5/14`](14-credentials.md). **This spec owns the handler shapes only.**

## The dispatch chain

Every handler shape shares one chain:

```
agent MCP call
  → gate        auth.Authorize("mcp:"+toolName) — may this folder call this tool?
  → inject      resolve folder/user secrets → map[string]string (never logged)
  → recover/timeout
  → handler     Go function | REST call | MCP subprocess
  → audit       audit_log row: folder, tool, scope, status, latency_ms
  → result      (secret values scrubbed from the response)
```

## Shape 1 — Go handler with secrets: NOT BUILT, deliberately

`registerWithSecrets` has **no consumer**. Every in-container tool registers via
`registerRaw` (`ipc/ipc.go:884`), and the operator-configurable credential
surface is fully covered by shapes 2 and 3. A plain Go built-in that needs
folder secrets would be the first consumer — none does, so building the plumbing
now is a speculative primitive. Add it WITH its first consumer.

## Shape 2 — MCP subprocess connector (shipped)

A third party ships its own MCP server; arizuko spawns it per call as a stdio
subprocess. No Go handler needed. Declared in `connectors.toml` with a command,
a `secrets` list, an `env_template` rendering `{secret:KEY}` placeholders, and a
`scope` of `per_call` (torn down immediately) or `per_session` (pooled per
`(connector, caller.sub)`, never shared across users).

1. **Boot** — `DiscoverConnectorTools` (`ipc/connector.go:77`) spawns with empty
   env, calls `tools/list`, caches the catalog. Each remote tool registers as
   `<connector>_<remote_name>`.
2. **Per call** — `ConnectorSecrets` (`routd/sibling_db.go:141`) narrows the
   folder secret map to only the keys the connector declared; the connector
   never sees the full folder secret set.
3. **Dispatch** — `ipc/ipc.go:997` renders the env template, spawns, proxies
   `tools/call` (`ipc/connector.go:125`), scrubs known secret values out of the
   result JSON.

## Shape 3 — REST descriptor (shipped)

Declarative TOML mapping tool names to REST endpoints — no subprocess, no Go
handler. Targets providers that ship no MCP server. Each `[[ext]]` block names a
base URL, an auth method (`bearer` | `apikey-header` | `apikey-query` | `basic` |
`json-body`) with the `secrets` key(s) it reads, and a list of `[[ext.tool]]`
entries carrying `name`, `scope`, `method`, and `path`.

Built-in provider definitions ship with the binary at
`routd/extproviders/*.toml` (cloudflare, porkbun, gandi, namecheap) and are
merged with operator-defined blocks at boot; the operator supplies only the API
key. Loader: `routd/ext.go` `LoadExtProviders`; caller: `ipc/extcall.go`.

**The tool's `scope` field IS the grant string** checked at call time — same
evaluator and glob syntax as `5/32`, e.g. `ext:cloudflare:dns:write`,
`!ext:cloudflare:*`, `ext:*:dns:read`.

## Grants

| shape           | grant prefix                | example                          |
| --------------- | --------------------------- | -------------------------------- |
| Go handler      | any custom string           | `github:pr:write`                |
| MCP subprocess  | `mcp:<connector>:<tool>`    | `mcp:github:create_pull_request` |
| REST descriptor | `ext:<service>:<operation>` | `ext:cloudflare:dns:write`       |

All checked via `auth.Authorize` before any handler fires. There is **no
tier-default derivation** — `5/33` deleted it, so a tool with no matching grant
is denied loud.

## Audit

Every call writes an `audit_log` row `(ts, folder, caller_sub, tool, scope_kind,
scope_id, key, status, latency_ms)`, `scope_kind ∈ {user, folder, missing}`,
`status ∈ {ok, err, timeout}`, one row per `(call × resolved key)`. Secret
values are never written.

The writer fires at the broker resolve seam (`routd/mcp.go`
`ResolveConnectorSecrets`, commit `c02b97c5`), emitting a `secret_use_log` row
per resolved key which `audit/audit.go` polls into `audit_log`. This closed the
M2 gap where the table and reader existed with no writer.

## Trust model

| scope                       | where             | reaches the agent?                                                                                                                                                                 |
| --------------------------- | ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operator anchors            | container env     | yes — the Claude Code CLI needs them (`.env` → `container/runner.go` `readSecrets()`; separate from the `secrets` table)                                                           |
| Folder secrets (capability) | `secrets` table   | **target: no** (broker-only). **Interim today: yes**, spawn-injected via container env (`5/14` §Injection, BUGS X1). Realizing the target = inject only `EnvProfileKeys` at spawn. |
| Per-user secrets            | `secrets`, broker | no                                                                                                                                                                                 |

Three escape paths the broker closes:

1. **Tool result echoes the token** — the broker scrubs known secret values from
   the result before returning to the agent (exact-string match on the keys
   declared for that call).
2. **Subprocess stderr** — routed to a broker-owned sink, never the agent
   (`ipc/connector.go` `slog.Debug` under the connector name).
3. **Agent steers the tool to leak** — connector registration is operator-only;
   agents cannot add connectors or REST descriptors.

## Capability layer model (settled 2026-07-27)

Skills and connectors are **operator-defined and global**. Groups USE the
capability surface; they do not define it.

- `ant/skills/` — global, seeded into every container at spawn. Changing skills
  changes the capability surface, which is a security boundary.
- `connectors.toml` / the `mcp_connectors` resource — global, operator-defined.
- **Per-group `MCP.json` is dropped.** A group that needs a connector gets a
  grant row (`mcp:candles:*`); it does not register its own MCP endpoint. This
  closes the gap where a group agent could register an arbitrary MCP server and
  route calls through it.
- Single-user deployments have no conflict: the user IS the operator. The
  boundary only applies when users ≠ operator.

## Shape 4 — HTTP upstream MCP (planned)

Replaces the stdio subprocess for connectors that ship as long-running HTTP
services (sidecar MCP servers, hosted providers like `mcp.linear.app/mcp`): a
`transport = "http"` + `url` in the connector block, same dispatch chain, no
spawn. `connectors.toml` becomes an `mcp_connectors` resreg resource in routd.db
so agents can list them and operators manage via dashd — tracked in `5/16`.

## Out of scope

- OAuth token dance + refresh — [`5/15`](15-surrogate-oauth.md).
- Per-tool secret-scope overrides (refuse folder fallback) — add
  `MCPTool.SecretScopes` if a consumer appears.
- HSM / KMS integration.
- MITM-isolated egress for opaque HTTP clients —
  `8/Z`, additive.
