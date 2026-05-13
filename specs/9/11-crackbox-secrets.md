---
status: spec
depends: [9-crackbox-standalone, 10-crackbox-arizuko, 6/5-uniform-mcp-rest]
---

# Tool-level secret brokering

> Secrets live in arizuko, never enter the container, and reach external
> APIs only via MCP tool handlers running on the host.

## Problem

Per-user API tokens (GitHub, Jira, OpenAI, …) must reach external APIs
on the user's behalf without materializing inside the agent container.
The previous design (TLS-MITM on `egred` with placeholder substitution
at egress) added a CA-distribution surface, an HTTP/1.1 ALPN constraint,
and bytes-in-the-middle injection. Dropped: the agent is not the
credential carrier.

## Solution: the broker

When an MCP tool declares `requires_secrets: ["GITHUB_TOKEN"]`, the
gateway resolves those keys at tool-call time and passes them as
**call arguments to the handler function in arizuko's host process**.
The handler makes the outbound HTTP with the real credential. The
agent invokes the tool by name and sees only the handler's response.

Muaddib ships this pattern: `ToolContext` carries `authStorage:
AuthStorage` (`refs/muaddib/src/agent/tools/types.ts:49-62`); tools
call `options.authStorage.getApiKey("jina")`
(`refs/muaddib/src/agent/tools/web.ts:156`) and `...getApiKey(
"openrouter")` (`refs/muaddib/src/agent/tools/image.ts:369`) on the
host. The Gondolin VM never holds the key. arizuko mirrors this over
its MCP-over-unix-socket surface.

## Trust boundaries

| Scope            | Where secrets live                             | Reaches agent? |
| ---------------- | ---------------------------------------------- | -------------- |
| Operator anchors | Container env (`ANTHROPIC_API_KEY`, bot creds) | Yes, by design |
| Folder secrets   | `secrets` table, broker only                   | No             |
| Per-user secrets | `secrets` table, broker only                   | No             |

`ANTHROPIC_API_KEY` stays in container env — Claude Code CLI needs it
and the container _is_ the LLM caller. Operator-trusted scope, not
user-tenant scope. Generic HTTP inside the container (curl, custom MCP
servers spawned by the agent) reaches only env vars; per-user tokens
are out of reach by construction.

## Storage

Existing `secrets` table from `store/migrations/0034-secrets.sql`,
column renamed by `0047-secrets-plaintext.sql`:
`(scope_kind, scope_id, key, value, created_at)` with PK
`(scope_kind, scope_id, key)`. `scope_kind ∈ {folder, user}`;
`scope_id` is folder path or `auth_users.sub`.

v1 stores plaintext in `value` (operator-trusted disk + FS perms).
AES-GCM was removed in this release; if encryption at rest becomes
required, add it back behind a future spec.

## Tool declaration

This spec adds **one new field on a tool descriptor** (`RequiresSecrets []string`) and **one middleware** in the dispatch chain. It does not rewrite `ipc/`. The descriptor + middleware shape is the same pattern docker-mcp-gateway and muaddib use (`refs/docker-mcp-gateway/pkg/interceptors/interceptors.go:21`; `refs/muaddib/src/agent/tools/types.ts:49-62`); we adopt the field, not the surrounding architecture.

`ipc/ipc.go:519-528` (today: `registerRaw(name, desc, opts, handler)`)
gains one optional axis: which secret keys the handler needs.

```go
registerWithSecrets("github_pr",
    "Create or list pull requests on a GitHub repo.",
    []string{"GITHUB_TOKEN"},
    []mcp.ToolOption{ /* params */ },
    func(ctx context.Context, req mcp.CallToolRequest, secrets map[string]string) (*mcp.CallToolResult, error) {
        token := secrets["GITHUB_TOKEN"]
        if token == "" { return toolErr("github_pr: no GITHUB_TOKEN; set at /dash/me/secrets") }
        // outbound HTTP using token; return result to agent
    })
```

The handler runs in `gated`, not the container. The resolved value is
a Go string local to the handler — never logged, never marshaled into
the `mcp.CallToolResult` returned to the agent.

## Connector declaration (MCP-as-subprocess)

The Go-handler path above covers built-in tools. For third-party APIs
the MCP ecosystem already ships standardized servers (`@modelcontextprotocol/server-github`,
Linear, Notion, GDrive, etc.) — JSON-RPC subprocesses configured by
env, the same shape Claude Desktop runs locally. The broker spawns
them per call with `caller.sub`'s secrets injected as env.

```toml
[[mcp_connector]]
name         = "github"
command      = ["docker","run","-i","--rm","ghcr.io/anthropic/mcp-github"]
secrets      = ["GITHUB_TOKEN"]
env_template = { GITHUB_PERSONAL_ACCESS_TOKEN = "{secret:GITHUB_TOKEN}" }
scope        = "per_call"   # subprocess lifetime; "per_session" later
```

On first connect gated calls `tools/list` on the subprocess once,
caches the catalog, namespaces each tool with the connector prefix
(`github_create_pr`, `github_list_issues`), and registers them as
`MCPTool` entries whose `RequiresSecrets` is the connector's
`secrets` list. Subsequent `tools/call` for a `github_*` tool:

1. Broker middleware resolves `secrets` via the same `user`∥`folder`
   path the Go-handler case uses.
2. Spawner renders `env_template`, spawns the connector subprocess
   with that env (no other env from gated leaks in).
3. JSON-RPC `tools/call(<unprefixed name>, params)` proxied through.
4. Result returned to agent. Subprocess torn down (`per_call`) or
   returned to pool keyed by `(connector, caller.sub)` (`per_session`).

The handler shape converges: built-in Go tool and connector tool
both go through `Chain(GrantsCheck, InjectSecrets, Recover, Timeout,
Audit, Handler)`; the only difference is whether `Handler` is in-
process Go code or `connector.Call(toolName, params, secrets)`.
`SpawnEnv` reuses `container/runner.go`'s env-injection primitive at
finer grain — same mental model, smaller blast radius.

## Why the agent can't leak the credential

The container holds no per-user token: by construction it never
enters `docker exec <container> env`. The MCP subprocess holds the
token only for its lifetime (per-call: tens of ms; per-session:
pooled per caller, never cross-user). Three escape paths and their
gates:

1. **Tool result echoes the token** — the broker scrubs known
   secret values from the `mcp.CallToolResult` JSON before returning
   to the agent (a finite list per call, exact-string match). A
   connector that echoes a token in an error message gets the
   echoed value redacted; the call still completes.
2. **Subprocess stderr** — routed to a sink owned by gated, not
   the agent. Audit log records `status='ok'|'err'|'timeout'`, never
   stderr content.
3. **Prompt-injected agent steers the call to leak via the tool** —
   the MCP server's tool surface is its own API; it can't introspect
   its env via tool calls unless it's malicious-by-design. Connector
   registration is operator-only; agents can't add connectors.

The audit row (`secret_use_log`) records that the call happened and
which scope resolved the key. The value is never written anywhere
gated keeps around past the subprocess lifetime.

## Resolution (the broker middleware)

The resolution step is implemented as **one middleware** in the
dispatch chain, callable as `InjectSecrets(handler)`. It sits between
`GrantsCheck` (which spec 6/5 owns) and the wrapped handler:

```
GrantsCheck  →  InjectSecrets  →  Recover/Timeout  →  Handler
```

Today `ipc/ipc.go:519-528` does grant checks inline. The broker
introduces only the `InjectSecrets` middleware; the other chain
positions are notional (the existing code does Recover/Timeout
already; spec 6/5 lays out grants). Each tool dispatch already has a
`Caller` per [`specs/6/5-uniform-mcp-rest.md`](../6/5-uniform-mcp-rest.md).
The middleware does:

```
for each key in tool.RequiresSecrets:
    secret = lookup(scope_kind='user',   scope_id=caller.Sub,    key)
          || lookup(scope_kind='folder', scope_id=caller.Folder, key)
    secrets[key] = secret.Value        // "" if neither row exists
```

`user` wins over `folder` when both define the same key. Folder lookup
walks parents to `root` as `Store.FolderSecretsResolved` does today
(`store/secrets.go:205-207`). Missing keys flow through as `""`.

The container spawn path (`container/runner.go:235,640-660`,
`resolveSpawnEnv`) **no longer merges user secrets into env**.
Folder-scoped env for operator anchors stays. `SecretsResolver` shrinks:
`UserSecrets` and `UserSubByJID` move to a new `BrokerResolver`
consumed at tool-call time, not at spawn time.

## Write paths

- **Operator CLI** (new in `cmd/arizuko/`):
  ```
  arizuko secret <inst> set    <folder>   KEY --value V
  arizuko secret <inst> list   <folder>
  arizuko secret <inst> delete <folder>   KEY
  arizuko user-secret <inst> set    <user_sub> KEY --value V
  arizuko user-secret <inst> list   <user_sub>
  arizuko user-secret <inst> delete <user_sub> KEY
  ```
- **User self-service** at `/dash/me/secrets`: GET (list, redacted),
  POST (add), PATCH (rotate), DELETE. Identity-bound to signed-in
  `X-User-Sub`. CSRF on writes. Rejects empty values; rejects keys not
  matching `^[A-Z][A-Z0-9_]*$`.

No `inject_mode`, `header`, `target_domain`, `placeholder` columns.
The handler owns how the credential is used; storage is "give me the
value for this key."

## Audit middleware

Audit is itself a middleware sitting at the end of the chain
(`Chain(GrantsCheck, InjectSecrets, Recover, Timeout, Audit)`), not
ad-hoc `slog.Info` calls scattered through handlers.

It writes structured rows into `secret_use_log`:

```
secret_use_log(ts, spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms)
```

with `scope ∈ {user, folder, missing}` and `status ∈ {ok, err, timeout}`.
One row per `(tool call × resolved key)`. Renamed from
`secret_register_log` — registration is gone; we record _use_. No
secret values in the log.

The audit middleware shape (one place, structured) is taken from
docker-mcp-gateway (`pkg/interceptors/interceptors.go:21`) — measurable
upside vs scattered logging is "single grep to answer 'what did the
agent call?'". The middleware framing is the only piece of that
project's architecture we adopt; HTTP transport, Bearer auth,
upstream-MCP aggregation, and `se://` vault URIs are explicitly
**not** imported.

## Acceptance

1. **Per-user token**: Alice POSTs `{key:GITHUB_TOKEN, value:ghp_xxx}`.
   Agent in Alice's spawn invokes `github_pr`; handler receives
   `secrets["GITHUB_TOKEN"]="ghp_xxx"`. `docker exec <container> env |
grep GITHUB_TOKEN` is empty.
2. **Folder fallback**: Operator sets folder `JIRA_TOKEN`. Alice (no
   per-user override) invokes `create_jira_issue`; handler receives the
   folder's value. `secret_use_log.scope='folder'`.
3. **Missing**: Bob invokes `github_pr` with no row at either scope.
   Handler receives `""`, returns structured error;
   `secret_use_log.scope='missing'`.
4. **Cross-user isolation**: Alice's and Bob's `github_pr` calls in the
   same channel see distinct tokens; neither sees the other's value.
5. **Container egress unchanged**: `curl https://api.anthropic.com/...`
   inside the container works (operator anchor). Per-user GITHUB_TOKEN
   never reaches container env.
6. **One audit row per resolution**.

## Implementation plan

| M   | Work                                                                             | LOC  |
| --- | -------------------------------------------------------------------------------- | ---- |
| M0  | `ipc.registerWithSecrets`; `MCPTool.RequiresSecrets []string`                    | ~30  |
| M1  | `gateway/secrets_broker.go`: `user`∥`folder` resolve, pass `map[string]string`   | ~80  |
| M2  | `store/audit.go`: `LogSecretUse` + `0048-secret-use-log.sql`                     | ~40  |
| M3  | `dashd/me_secrets.go`: GET/POST/PATCH/DELETE + CSRF                              | ~150 |
| M4  | `cmd/arizuko/secret.go` + `user_secret.go`: operator CLI                         | ~100 |
| M5  | Drop user-overlay from `container/runner.go`; remove `WireEntry.Secrets`         | ~50  |
| M6  | Connector path: `mcp_connector` TOML, per-call subprocess spawner, env injection | ~200 |
| M7  | First connector lands: github-mcp-server. PAT-only; user pastes at M3 surface    | ~30  |
| M8  | Release: CHANGELOG, migration, version bump                                      | —    |

No proxy changes. No CA. No TLS termination. No
`crackbox/pkg/proxy/mitm.go`. `WireEntry.Secrets` in
`crackbox/pkg/admin/api.go` becomes dead and is removed in M5.

## Open questions

- **Per-tool override of fallback order?** A tool intrinsically
  user-scoped (e.g. `github_pr`) may want to refuse folder fallback.
  Add `MCPTool.SecretScopes map[string]Scope` if needed; not v1.
- **`caller.Folder` source for chat-routed calls** — `ipc/ipc.go`
  builds it per turn from the spawn folder; the per-call `Caller` shape
  lands with spec 6/5. v1 reads `folder` from the existing closure.

## Out of scope

- TLS-MITM at egress (dropped; revisit if an SDK can't be wrapped).
- AES-GCM at rest — cipher code removed from `store/secrets.go`
  (v1 stores plaintext). Re-add behind `AUTH_SECRET` when threat
  model demands.
- **OAuth dance + token refresh** — [`specs/9/14-surrogate-oauth.md`](14-surrogate-oauth.md).
  The broker treats `secrets.value` as opaque; whether it landed
  there via user paste (`/dash/me/secrets`) or via a completed OAuth
  flow is the writer's concern. v1 ships PAT-only: pastable
  long-lived tokens (GitHub fine-grained PAT, Linear PAT, OpenAI
  key). 9/14 adds the dance + refresh wrapper.
- MCP handler isolation beyond subprocess boundary (containerized
  per-call MCP servers ship under spec 9/12 sandboxing extensions).
- HSM / KMS integration.

## Cross-references

- [`specs/7/product-slack-team.md`](../7/product-slack-team.md) — per-user
  GitHub token flow, the canonical v1 user.
- [`specs/6/5-uniform-mcp-rest.md`](../6/5-uniform-mcp-rest.md) — `Caller`
  shape consumed here.
- [`specs/9/10-crackbox-arizuko.md`](10-crackbox-arizuko.md) — egred
  keeps CONNECT-splice + per-source allowlists; untouched by this spec.
- [`specs/9/14-surrogate-oauth.md`](14-surrogate-oauth.md) — OAuth
  dance + refresh wrapper; writer-side feed into the `secrets` table
  the broker reads. Independent ship: 9/11 ships PAT-only and is
  useful end-to-end without it.
