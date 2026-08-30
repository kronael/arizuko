---
status: shipped
depends:
  [17-openapi-mcp, specs/5/32-acl-unified, specs/5/5-worlds-agents-sessions]
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
  → inject      resolve folder/user secrets ON THE HOST → map[string]string (never logged)
  → recover/timeout
  → handler     Go function | REST call | MCP HTTP connector
  → audit       audit_log row: folder, tool, scope, status, latency_ms
  → result      (secret values scrubbed from the response)
```

## Shape 1 — Go handler with secrets: NOT BUILT, deliberately

`registerWithSecrets` has **no consumer**. Every in-container tool registers via
`registerRaw` (`ipc/ipc.go:861`), and the operator-configurable credential
surface is fully covered by shapes 2 and 3. A plain Go built-in that needs
folder secrets would be the first consumer — none does, so building the plumbing
now is a speculative primitive. Add it WITH its first consumer.

## Shape 2 — MCP HTTP connector (shipped)

A third party hosts its own MCP server; arizuko calls it over streamable HTTP.
No Go handler, no subprocess, no runtime to install. Declared in
`connectors.toml` with a `url`, a `secrets` list, and a `headers` map rendering
`{secret:KEY}` placeholders.

1. **Boot** — `LoadConnectors` (`routd/connectors.go`) parses and validates the
   blocks. It dials NOTHING: the credential is per folder and boot has no
   folder. A `http://` endpoint is refused here, because the credential rides in
   a header.
2. **Per folder** — at each turn's socket build, `discoverConnector`
   (`ipc/connector.go`) resolves the folder's secrets through the SAME audited
   broker seam a call uses (recorded as `<connector>_tools/list`, because a
   remote `tools/list` genuinely spends the credential), then calls `tools/list`.
   Each remote tool registers as `<connector>_<remote_name>`. A connector that
   cannot be reached or authenticated registers no tools and is logged at error;
   it never fails the turn.
3. **Per call** — `ConnectorSecrets` (`routd/sibling_db.go`) narrows the folder
   secret map to only the keys the connector declared; the connector never sees
   the full folder secret set.
4. **Dispatch** — `ipc/ipc.go` resolves, `CallConnectorTool`
   (`ipc/connector.go`) renders the headers, proxies `tools/call`, and scrubs
   known secret values out of the result.

### Why HTTP and not a subprocess

The stdio shape shipped and could never run. The connector subprocess spawns
inside **routd**, and routd's image is static Go binaries — no node, no npx, no
`github-mcp-server` — while routd deliberately holds no docker socket (runed is
the only daemon that does). All three commands `template/connectors/github.toml`
offered were therefore dead on arrival, and no instance ever configured one
(BUGS `L132`, closed 2026-08-30 as the wrong fix: shipping a binary into the
shared runtime image would have bloated every daemon that shares it — proxyd,
teled, onbod — to serve one connector).

An endpoint needs no runtime. `connectors.toml` is operator-written and
immutable after boot, so the reachable destination set is CONFIGURED, not
agent-chosen: that file is the allowlist, and the agent picks a tool, never a
host. routd already makes outbound HTTPS on this exact path — surrogate OAuth
refreshes tokens against the provider at call time (`routd/surrogate_refresh.go`)
— so no new egress boundary is crossed.

### OAuth is the next step, not this one

MCP's authorization spec is reachable only over HTTP: an stdio server "SHOULD
NOT" follow it and takes credentials from the environment instead. Moving the
transport is what puts that door in reach. It is NOT wired yet — the connector
authenticates with the broker-resolved static token, which for GitHub may be a
PAT or a `5/15` surrogate access token, since `auth/surrogate/providers/github.toml`
already targets the same authorization server GitHub's remote MCP server names
in its protected-resource metadata.

What full MCP OAuth would still need:

- RFC 9728 protected-resource-metadata discovery off the `401` challenge, RFC
  8414 authorization-server metadata, and RFC 7591 dynamic client registration —
  the last is what a per-provider TOML cannot replace for an unknown server.
- The `resource` parameter (RFC 8707) binding the token to the MCP endpoint.
- **A consent flow with no user present.** Discovery and refresh run inside
  routd; an authorization-code dance needs a browser and a human. `5/15` already
  solves this for the folder credential by consenting once at `/dash/me/connections`
  and storing the refresh token, so the connector inherits that answer — but a
  server requiring per-client DCR has no equivalent, and that gap is the reason
  this is a separate change.

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
| MCP HTTP        | `mcp:<connector>:<tool>`    | `mcp:github:create_pull_request` |
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
`ResolveConnectorSecrets`, commit `c02b97c5`), calling `store.LogSecretUse`
per resolved key, which writes the `secret_use_log` row AND the `audit_log`
row in the same transaction — synchronous, no poller. This closed the
M2 gap where the table and reader existed with no writer.

`scope_kind` and `tool` are THREADED, never re-derived: the scope comes back
from `Store.FolderSecretsResolvedForUser`, the one point where user-vs-folder
provenance exists before the overlay collapses it, and `missing` is stamped
after any surrogate refresh so a dropped key does not read as spent; the tool
name arrives as an argument of `ipc.StoreFns.ResolveConnectorSecrets`, since the
call site is the only place it is known.

## Trust model

| scope                       | where             | reaches the agent?                                                                                                                    |
| --------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| Operator anchors            | host `.env`       | yes — the Claude Code CLI needs them (`container/runner.go` `readSecrets()`; separate from the `secrets` table)                       |
| Per-user env-profile keys   | `secrets` table   | yes — the model credential is spent by the SDK subprocess, so it has no tool call to broker (`routd.DB.EnvProfileSecrets`, `5/14` §1) |
| Folder secrets (capability) | `secrets` table   | **no** — broker-only, resolved per call on the host (`5/14` §2). Shipped 2026-08-09, BUGS `X1`.                                       |
| Per-user capability secrets | `secrets`, broker | **no** — same broker path; a user's own key wins over the folder default for their calls                                              |
| Shell credentials           | `secrets`, grant  | **only when granted** — a `shell:env:<KEY>` acl row names each key; no row, nothing crosses (`routd.DB.ShellEnvSecrets`, below)       |

The container env therefore holds the operator anchors, the triggering user's own
`store.EnvProfileKeys`, and the capability keys a grant names one at a time.
Nothing else from `secrets` crosses the boundary.

### Shell credentials

The broker's contract is that a capability credential reaches a TOOL and nothing
else. That contract holds only where there is a tool to broker. A CLI the agent
drives itself — `gh`, `buildkite` — is a `Bash` call, so there is no tool name to
resolve a secret for and no subprocess of ours to inject it into. Such a CLI can
authenticate only from the shell env.

That is a real widening of the boundary above, so it is a GRANT and never a
default. `auth.ShellEnvKeys` reads `shell:env:<KEY>` rows, matching the action
EXACTLY for the reason `CheckHold` does (spec 5/19 Hazard 2): an operator's
(`*`, `**`) row covers every action, so evaluating this through `Authorize`
would drop every folder secret into every operator turn's env — the grant would
widen hardest exactly where it should not. `shell:env:*` is the deliberate
all-keys row; a deny row wins. `store.EnvProfileKeys` are excluded even under
`shell:env:*`: they have their own path, and keeping a model credential out of a
shell the agent drives is the whole of BUGS `L131`.

**What this tier does NOT enforce.** Once a token is in the shell, arizuko can no
longer bound what it buys. The four broker escape paths below do not apply: the
agent holds the credential directly, so it can spend it with `curl` as easily as
with `gh`, and no `acl` row sees the difference. A refusal in the `Bash` write
gate (`ant/src/guards.ts`) raises the cost of one spelling; it is not the lock.
**The lock is the token's own scope at the issuing service** — a PAT minted
without `pull_requests: write` cannot approve a pull request however it is
called. Grant a shell credential only where the credential itself is already
bounded to what the folder may do.

Four escape paths the broker closes:

1. **Tool result echoes the token** — the broker scrubs known secret values from
   the result before returning to the agent (exact-string match on the keys
   declared for that call).
2. **Transport errors and upstream diagnostics** — surfaced to routd's log, never
   returned as tool output (`ipc/connector.go` wraps every transport failure with
   the connector name and returns it to the caller without the rendered headers).
3. **Agent steers the tool to leak** — connector registration is operator-only;
   agents cannot add connectors or REST descriptors.
4. **Agent reads its own environment** — the spawn ships only the caller's
   `EnvProfileKeys`, so `env` inside the container names no capability
   credential to read, write to disk, or replay after the turn.

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

## Still open — `mcp_connectors` as a resource

`connectors.toml` is a file read once at boot. Making it an `mcp_connectors`
resreg resource in routd.db — so agents can list connectors and operators manage
them via dashd — is tracked in `5/16`. Until then a connector change is an
operator file edit plus a routd restart, and the file's immutability is what
makes it the destination allowlist.

## Out of scope

- OAuth token dance + refresh — [`5/15`](15-surrogate-oauth.md).
- Per-tool secret-scope overrides (refuse folder fallback) — add
  `MCPTool.SecretScopes` if a consumer appears.
- HSM / KMS integration.
- Opaque in-container HTTP clients (`aws`, `gh`, `boto3`) — the gap this
  spec named `8/Z`. DECLINED 2026-08-20 —
  which closes it with `credential_process` plus a host-side request tool.
  MITM-isolated egress was DECLINED there (2026-08-18): header injection
  was the only thing needing TLS interception, and a CA inside every
  container cost more than the residue it covered.
