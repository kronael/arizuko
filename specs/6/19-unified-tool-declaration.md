---
status: draft
source: paradigmxyz/centaur@20f3021 (Apache-2.0) peel, 2026-08-10
depends: [../5/13-ext-mcp, ../5/14-credentials, 17-egress-credential-proxy]
---

# specs/6/19 — one declaration shape for external tools

Proposal: collapse the two `connectors.toml` block shapes into one
`[[connector]]` declaration with a `transport` discriminator, adopt a
typed secret-entry vocabulary shared with the egress proxy
([`6/17`](17-egress-credential-proxy.md)), and add the one loader rule
both shapes miss today: ordered sources, later shadows earlier, loud
warn. Execution shapes stay exactly as `5/13` ships them — this unifies
the DECLARATION, never the dispatch.

## arizuko today: one file, two shapes, no collision rule

External capability tools split across two TOML shapes with two loaders
and two registration loops:

- `ExtTool` (`ipc/extcall.go:20-42`) — `[[ext]]` REST descriptors:
  positional `SecretKey`/`SecretKey2` plus per-method fields
  (`Header`/`Header2`/`Param`), auth method enum
  `bearer|apikey-header|apikey-query|basic|json-body`.
- `ConnectorSpec` (`ipc/connector.go:32-39`) — `[[mcp_connector]]` stdio
  subprocess: `command`, flat `secrets` list, `env_template`.
- Registration is two parallel loops (`ipc/ipc.go:997-1069` connectors,
  `ipc/ipc.go:1073-1104` ext tools).
- `LoadExtProviders` (`routd/ext.go:110`) appends operator `[[ext]]`
  blocks after the embedded built-ins with **no shadow or collision
  rule** — an operator block reusing a built-in provider name yields
  duplicate `LocalName`s with unspecified precedence.
- `5/13` §Shape 4 plans a THIRD variant (HTTP upstream MCP:
  `transport = "http"` + `url`) — as written it lands beside the other
  two, not inside one shape.

resreg resources and the hot-tier hand-authored tools are NOT part of
this: they are platform surface with their own two-face discipline
(CLAUDE.md §MCP + REST hand-rolled and uniform). Folding them into a
tool catalog is the DSL the repo already rejected.

## Centaur's evidence

One schema, `[tool.centaur]` in each tool's `pyproject.toml`, covers
every tool's identity, egress hosts, and credentials
(`services/api-rs/crates/centaur-perms/src/tools.rs:358-395`):

- **Typed secret entries.** `secrets = [{ type = "http" | "oauth_token"
| "aws_auth" | "hmac_sign" | "pg_dsn" | "gcp_auth" | "gcp_id_token" |
"brokered_token", name, hosts, ... }]` — each type carries its own
  fields (`tools/infra/datadog/pyproject.toml:26-40` http replace-mode;
  `tools/infra/cloudwatch/pyproject.toml:36-47` aws_auth with
  `allowed_services`; `tools/productivity/gsuite/pyproject.toml:35-62`
  oauth_token pulling `fields[].json_key` out of one JSON secret;
  `tools/research/websearch/pyproject.toml:36-39` `optional_secrets`
  with `mode = "inject"` — the tool process never sees a value).
- **One declaration feeds two enforcement planes.** The same parsed
  secrets build the per-sandbox egress-proxy config at spawn
  (`centaur-api-server/src/tool_discovery.rs:139-159`
  `discover_tool_proxy_fragment`) AND the operator-side credential
  provisioning (`centaur-perms/src/main.rs:605-648` → iron-control).
  Declaration-derived enforcement is the load-bearing idea: a tool
  cannot need a host or a credential its declaration doesn't name.
- **Ordered sources, later shadows earlier, loud.** `tools.toml:1-5`
  declares ordered `plugin_dirs`; the collector overwrites an
  earlier-dir entry and logs a shadow warning
  (`tool_discovery.rs:347-407`, resolution order `:90-124`).

Their cautionary tale is equally load-bearing: that schema is parsed by
TWO Rust implementations (`centaur-perms/src/tools.rs`, ~1084 lines, and
`centaur-api-server/src/tool_discovery.rs`, ~1977 lines) which both cite
a Python `services/api/api/tool_manager.py` as "the source of truth"
(`tools.rs:3-9`) — a file that does not exist in the tree. Two live
copies drifting against a phantom canonical is exactly what the
one-renderer rule exists to prevent, and arizuko's two-shape loader is
the same disease at smaller scale.

Also noted for scale, not for copying: Centaur has exactly ONE execution
model — every tool is a `uvx`-run CLI shim, no MCP anywhere
(`centaur-sandbox-agent-k8s/src/tools.rs:1-7`,
`services/sandbox/install_tool_shims.py`). One execution shape is what
makes their one-declaration trivially true. arizuko keeps its three
execution shapes; only the declaration unifies.

## The delta

1. **One `[[connector]]` block, `transport = "stdio" | "http" | "rest"`.**
   Today's `[[mcp_connector]]` is `transport="stdio"`; today's `[[ext]]`
   is `transport="rest"` (its `[[ext.tool]]` list moves under the block
   unchanged); `5/13` shape 4 lands as `transport="http"` instead of a
   third top-level shape. One loader replaces
   `LoadExtProviders` + the `mcp_connector` parse; one registration walk
   replaces the two loops. Grant namespaces (`mcp:` / `ext:`) and
   dispatch paths do not move.
2. **The loader rule Centaur has and arizuko lacks**: sources are
   ordered (embedded built-ins, then operator file), later shadows
   earlier BY NAME, every shadow logged loud. Kills the duplicate-
   `LocalName` ambiguity at the one place it can be killed.
3. **Typed secret entries as the shared vocabulary.** `secrets` becomes
   a list of `{ key, type, ... }` where `rest`'s current auth-method
   enum is the `http` type's mode field, and the vocabulary is the SAME
   one `6/17`'s proxy rules use (`http` replace/inject, `hmac_sign`,
   `aws_auth`) — so a credential either rides the broker (this file) or
   the proxy (`6/17`) but is described identically in both. `hosts` on
   a declaration is the seed `6/17` §per-tool rules consume; for
   broker-side transports it is documentation and audit context only
   (connector subprocesses run on the host, outside crackbox).

## Cost

A `connectors.toml` format migration (fleet exposure is small — one
`secrets` row fleet-wide as of 2026-08-04, few operator connector
blocks; the embedded `routd/extproviders/*.toml` migrate in-repo), the
loader rewrite plus a compatibility read for the two old block names for
one release, and touching the `ipc.go` registration walk. Risk:
over-generalizing — held off by the rule that `transport` values must
stay mechanically distinct dispatch paths (`mcp_tool_naming`
discipline); the moment a field means different things per transport,
the shape has failed.

## Verdict

Worth it, but **timed, not standalone**: do the merge WHEN `5/13` shape
4 (HTTP upstream MCP) is built — that is the moment a third parallel
shape would otherwise appear, and the migration then pays for itself.
Churning a working file purely for symmetry is not minimal. The shadow
rule (delta 2) is independently shippable earlier if the duplicate-name
ambiguity bites first.

## Attribution

Analysis derives from reading `paradigmxyz/centaur` (Apache-2.0), commit
`20f3021`: `tools.toml`, `tools/*/pyproject.toml`,
`services/api-rs/crates/centaur-perms/src/tools.rs`,
`services/api-rs/crates/centaur-api-server/src/tool_discovery.rs`,
`services/api-rs/crates/centaur-sandbox-agent-k8s/src/tools.rs`,
`services/sandbox/install_tool_shims.py`. No code was copied.
