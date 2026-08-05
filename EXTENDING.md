# Extending arizuko

Catalog of extension points. Keep current as the system evolves.

Extension points are how you **add an integration** — a new channel
adapter, a TTS backend, an oracle skill, a per-folder mount, a custom
autocall, a scheduled task, a network-egress rule. They are NOT how you
change the **system core** (routd, runed, authd, store, ipc, auth,
grants, proxyd, webd, dashd, timed, onbod, container runner,
chanlib/chanreg). Core evolves as a unit through specs, not via these
extension points. See
[README.md](README.md) for the core-vs-integration breakdown and
[ARCHITECTURE.md](ARCHITECTURE.md) for the package graph.

## Extension points

| Point         | Location                               | Extensible by  | Mechanism                                     |
| ------------- | -------------------------------------- | -------------- | --------------------------------------------- |
| Channels      | external containers                    | Developer      | HTTP protocol (latest: `slakd/`)              |
| Proxyd routes | `template/services/<name>-routes.json` | Daemon author  | JSON route array, no Go edits                 |
| Slink         | `webd/slink*.go`                       | External agent | Chat UI + MCP transport at `/slink/<token>`   |
| Slink SDK     | `webd/assets/arizuko-client.js`        | Page author    | Embedded JS served at `/assets/`              |
| Actions       | MCP tools                              | Agent/Plugin   | Registry + MCP                                |
| Autocalls     | `routd/prompt.go`                      | Router dev     | Registry slice                                |
| Routing rules | `router/`                              | Agent          | MCP tools; `target=<folder>[#mode]` fragment  |
| Mounts        | `container/`                           | Agent          | Container config                              |
| Skills        | `ant/skills/`                          | Agent          | File-based                                    |
| Tasks         | `timed/`                               | Agent          | IPC actions                                   |
| Diary         | `diary/`                               | Agent          | File-based                                    |
| Network rules | `store/network.go`                     | Operator       | CLI + DB rows                                 |
| Web routes    | `store/web_routes.go`                  | Agent          | MCP tools (`set_web_route` / `del_web_route`) |
| Public pages  | `template/web/pub/`                    | Operator       | Plain HTML, copied into `<data-dir>/web/pub/` |
| Output styles | `ant/output-styles/`                   | Channel author | `<channel>-<surface>.md`; picked per-session  |

## Adding an output-style file

Per-channel + per-surface tone, length, and formatting hints applied
at session bind. `container/runner.go::pickOutputStyle` derives
`<channel>-<surface>` from inbound JID + topic + `paneLookup`; falls
back to `<channel>.md` for channels with no per-surface split. Spec:
`specs/5/Y-output-styles-per-surface.md`. Conventions, file list, and
adding-a-new-file recipe: `ant/output-styles/README.md`.

No registration step — `seedOutputStyles` discovers files by directory
listing at spawn (`container/runner.go:669`). Drop the file, rebuild
the agent image (`make agent`), redeploy.

## Adding an autocall

Autocalls inject zero-arg, one-line, pure-read facts into the
`<autocalls>` block at the top of every prompt. Cheaper than an MCP
tool when the schema cost exceeds the data returned: no agent-visible
schema, no tool call, one line of output per turn.

Rules:

- Result is ≤ 1 line of text. Empty string = skip the line.
- No args, no I/O, no locks. Must resolve in microseconds.
- Derives from `autocallCtx` (instance, folder, session, now).
- If any of these don't hold, use an MCP tool instead.

Add an entry to the `autocalls` registry slice in `routd/prompt.go`:

```go
{"world", func(c autocallCtx) string {
    return strings.SplitN(c.Folder, "/", 2)[0]
}},
```

Then update `ant/skills/self/SKILL.md` autocalls section and ship a
migration under `ant/skills/self/migrations/`.

## Designing MCP tools for LLMs

The MCP description is the model's training material every turn. It is
read on every prompt, costs tokens, and shapes which tool the model
picks. Two principles follow.

**Descriptions answer "when", not just "what".** Every tool description
states _use when X_ and _not for Y_, naming the sibling tool that
covers Y. The model picks instantly instead of reasoning about
disambiguation at call time. See `ipc/ipc.go` registrations
(`reply`, `like`, `reset_session`, `register_group`, …) for the
canonical pattern.

**No surrogates — `Unsupported(...)` with a hint.** When a verb has no
native primitive on a platform, the adapter returns
`chanlib.Unsupported(tool, platform, hint)` carrying a concrete
alternative tool the agent should call instead. Do not synthesize a
fake implementation by gluing other primitives together. The hint
travels through HTTP 501 → `*chanlib.UnsupportedError` →
`toolMaybeUnsupported` and is rendered to the agent as
`unsupported: <tool> on <platform>\nhint: <alternative>`.

**Distinct intents → distinct tool names.** Default to one tool per
intent. A sharp per-intent description outperforms a fuzzy umbrella
description with a `kind`/`mode`/`type` enum: the umbrella forces the
model to disambiguate at call time and dilutes signal in every other
tool's description by proximity.

Only collapse two names into one tool when the action is mechanically
identical AND the same description naturally covers both — e.g.
`reply` covers comment/reply because both create a threaded
response to a parent message. Do NOT collapse repost/forward/quote
into `share(kind=…)`; three intents, three tools.

Architectural overlap under the hood is fine. Email's `forward` may
compile to `send` + `Fwd:` subject; Telegram's `forward` uses a native
protocol field. Both expose `forward` as a distinct MCP tool because
the agent's intent is the same ("show X this thing I saw"). The
adapter does the translation.

UNIX precedent: `cp` / `mv` / `ln` are three commands with three man
pages, not `relocate(kind=copy|move|link)`.

The autocall-vs-MCP-tool decision (above) is the same principle on a
different axis: minimize the model's per-turn cost of choosing and
calling. Zero-arg pure-read facts → autocall. Distinct intents →
distinct tools.

## Verb support matrix

The 15 outbound MCP verbs and their per-platform native support. An
empty cell means the adapter returns `*UnsupportedError` with a
concrete hint.

| Verb         | discd | slakd | mastd | bskyd | reditd | teled | emaid | linkd | whapd | twitd |
| ------------ | ----- | ----- | ----- | ----- | ------ | ----- | ----- | ----- | ----- | ----- |
| `send`       | ✓     | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     |
| `reply`      | ✓     | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     |
| `send_file`  | ✓     | ✓     |       |       |        | ✓     |       |       | ✓     | ✓     |
| `send_voice` | ✓     |       |       |       |        | ✓     |       |       | ✓     |       |
| `post`       | ✓     | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |
| `like`       | ✓     | ✓     | ✓     | ✓     | ✓      | ✓     |       |       | ✓     | ✓     |
| `delete`     | ✓     | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |
| `forward`    |       |       |       |       |        | ✓     |       |       | ✓     |       |
| `quote`      |       |       |       | ✓     |        |       |       |       |       | ✓     |
| `repost`     |       |       | ✓     | ✓     |        |       |       |       |       | ✓     |
| `dislike`    |       | ✓     |       |       | ✓      |       |       |       |       |       |
| `edit`       | ✓     | ✓     | ✓     |       |        | ✓     |       |       | ✓     |       |
| `pin`        | ✓     | ✓     |       |       |        | ✓     |       |       |       |       |
| `unpin`      | ✓     | ✓     |       |       |        | ✓     |       |       |       |       |
| `unpin_all`  |       | ✓     |       |       |        | ✓     |       |       |       |       |

`delete` and `edit` are own-message by default — the platform enforces
authorship; `:any` is a distinct grant (spec 5/Z). `pin`/`unpin` are
the `pin_message`/`unpin_message` MCP tools; `unpin_all` clears every
pin (Discord has no bulk primitive — call `unpin_message` per id).
Adapters with no pin primitive embed `chanlib.NoPinSupport`.

## Adding web routes

The `web_routes` table lets agents expose or gate specific URL paths on
the proxyd host without redeploying. Three MCP tools, registered in
`ipc/ipc.go`:

| Tool              | Effect                                                        |
| ----------------- | ------------------------------------------------------------- |
| `set_web_route`   | Upsert a row: `path_prefix`, `access`, optional `redirect_to` |
| `del_web_route`   | Remove a row owned by the calling folder                      |
| `list_web_routes` | List rows owned by the calling folder                         |

`access` values: `public` (no auth), `auth` (require login), `deny` (403),
`redirect` (302 to `redirect_to`). proxyd evaluates longest-prefix first
via `store.MatchWebRoute`. These rows only take effect if proxyd is
configured with a store (`s.st != nil`); they have no effect on channel
routing.

## Installing packages

`arizuko packages <instance> install | upgrade | remove` manages packages
(spec `5/28`). A package is a git source or local dir shipping any subset of
asset kinds:

- **compose fragment** — `<name>.yml` (+ `<name>-routes.json`) → copied to
  `services/`, brought up on `arizuko generate`.
- **proxyd route** — `<name>-routes.json` → hot-applied to the live
  `proxyd_routes` table; proxyd reads it per request, so it takes effect with
  **no restart**.
- **grant** — `<name>-grants.json` (array of `{principal, action, scope}`) →
  applied to `acl`.
- **skills** — `skills/<name>/` → copied to `<datadir>/skills/`, layered into
  every group by `seedSkills` at spawn.

```bash
arizuko packages krons install github.com/org/pkg   # or a local dir
arizuko packages krons upgrade pkg                   # refuses locally-edited (dirty) assets
arizuko packages krons remove  pkg                   # withdraws routes, then drops fragments
```

Each install records an **installed-package record** in `routd.db` (source +
resolved revision + owned identities + per-asset content hash). That record
drives `upgrade` (dirty-detection — never overwrites a locally edited asset),
`remove` (deletes exactly the identities it owns), and reproducibility. A
group seed is not a package asset — that is create-time
`arizuko create --product` / a `5/21` product, not an instance-wide install.

## Adding a channel adapter

A channel adapter is a standalone HTTP daemon that bridges a chat
platform to `routd` via `chanlib`. It registers a JID prefix, serves
inbound webhooks (or polls), and exposes outbound verbs (`/send`,
`/like`, `/edit`, …) plus `/health`. Latest reference:
[`slakd/`](slakd/) (Slack Events API, signing-secret HMAC, dislike-
via-`reactions.add`). Steps:

1. Create `<name>d/` with `main.go` wiring `chanlib.Run`, a `bot.go`
   for platform I/O, and a JID parser/formatter.
2. Add `template/services/<name>d.yml` — a partial compose file
   defining one service. If you need inbound webhooks via proxyd, add
   `template/services/<name>d-routes.json` (see below) — no
   `compose.go` edits.
3. Implement only the verbs the platform supports natively; return
   `chanlib.Unsupported(tool, platform, hint)` for the rest.
4. Spec under `specs/2/<letter>-<name>.md`; per-platform native
   support belongs in the verb matrix above.

Generic HTTP webhook ingest is the same path: any caller can POST
`/v1/messages` with the channel-protocol envelope; route inbound
webhooks through proxyd via the receiving daemon's routes file. No
separate "webhook adapter" abstraction.

## Adding a proxyd route

`proxyd`'s route table is built from each package's
`services/<name>-routes.json`, collected at compose-generate time, plus
a static core-route slice in `compose/compose.go` (`coreProxydRoutes`,
for dashd/webd/davd/onbod). Routes cannot live in the compose fragment
— they are assembled into ONE env var on proxyd. Adding a new inbound
web path = one JSON file, no Go edits:

```json
[
  {
    "path": "/<prefix>/",
    "backend": "http://<name>d:8080",
    "auth": "public",
    "gated_by": "<ENV_VAR>",
    "preserve_headers": ["X-Webhook-Sig"]
  }
]
```

`path` with a trailing `/` matches longest-prefix; `auth` is
`public` | `user` | `operator`; `gated_by` drops the route when that
env var is unset; `preserve_headers` passes listed headers verbatim.
`compose.go` evaluates `gated_by` against the operator's `.env`, drops
disabled routes, and emits the survivors as `PROXYD_ROUTES_JSON` on
proxyd. Reference: `specs/5/7-proxyd-standalone.md`,
`template/services/slakd-routes.json`.

## Adding an MCP connector

**Connector scope (2026-07-27):** Connectors are **global and operator-defined**.
All `[[mcp_connector]]` blocks register at the deployment level — there is no
per-group `MCP.json`. Groups get access to connectors via grant rows
(`mcp:<connector>:*`), not by registering their own MCP endpoints. The
`connectors.toml` loader is the current mechanism; migration to a
`mcp_connectors` resreg resource (routd.db, REST + agent MCP face) is tracked
in `specs/5/16`. HTTP upstream connectors (`transport = "http"`, shape 4 in
`specs/5/13`) are planned for long-running sidecar MCP servers.

Third-party MCP servers (github-mcp, linear-mcp, gdrive-mcp…) plug
in via `<data_dir>/connectors.toml`. Each `[[mcp_connector]]` block
declares one stdio subprocess; routd discovers its tools at boot
with `tools/list`, namespaces them `<connector>_<remote_tool>`, and
registers each through the broker chain. Per-call invocation
resolves the declared secrets, renders the env template, spawns the
subprocess, proxies `tools/call`, scrubs the result, tears the
subprocess down. Spec `specs/5/13-ext-mcp.md` § "Connector declaration".

```toml
[[mcp_connector]]
name         = "github"
command      = ["github-mcp-server", "stdio"]
secrets      = ["GITHUB_TOKEN"]
env_template = { GITHUB_PERSONAL_ACCESS_TOKEN = "{secret:GITHUB_TOKEN}" }
scope        = "per_call"
```

- `secrets` lists the broker keys the connector needs; resolved at
  call time via `user(caller.Sub)` → `folder(caller.Folder)` fallback.
- `env_template` keys are the env vars the subprocess sees;
  `{secret:KEY}` references the corresponding entry in `secrets`.
  Nothing else from routd's env leaks in.
- `scope = "per_call"` is the only v1 mode — subprocess lives one
  call. `per_session` (pool keyed by `(connector, caller.Sub)`) is
  reserved for a future spec.
- Result scrubbing: any non-empty resolved secret value is
  exact-string-replaced with `«redacted»` in the
  `mcp.CallToolResult` before the agent sees it.
- `CONNECTOR_CALL_TIMEOUT_MS` (env) caps each call (default 30 s).
- Connector subprocess stderr sinks into routd's `slog.Debug` under
  the connector name; never reaches the agent.

Operator writes per-user tokens via `arizuko user-secret <inst> set
<sub> KEY --value V` or by handing the user `/dash/me/secrets`.

## Adding an `[[ext]]` REST provider

An external HTTP API with no MCP server of its own (Cloudflare, Porkbun,
Gandi, Namecheap…) plugs in as a REST descriptor. An operator declares one
`[[ext]]` block; `routd/ext.go:LoadExtProviders` reads them at boot and each
`[[ext.tool]]` becomes one REST-backed MCP tool (`ipc/extcall.go` `ExtTool`)
that maps a tool call to one outbound HTTP request. This is **shape 3** of
the credential model (`specs/5/13-ext-mcp.md`); the MCP connector above is
shape 2.

```toml
[[ext]]
name = "cloudflare"
base = "https://api.cloudflare.com/client/v4"

  [ext.auth]
  method = "bearer"        # bearer | apikey-header | apikey-query | basic | json-body
  secret = "CF_API_TOKEN"  # key in the secrets table (user- or folder-scoped)
  # apikey-header: header = "X-Api-Key"
  # apikey-query:  param  = "api_key"      (scrubbed from the URL in the response)
  # basic:         secret = user, secret2 = pass
  # json-body:     header/header2 = JSON body field names, secret/secret2 = values

  [[ext.tool]]
  name   = "dns_set"
  scope  = "ext:cloudflare:dns:write"
  method = "POST"
  path   = "/zones/{zone_id}/dns_records"
```

- **Auth method** (`[ext.auth].method`) — one of `bearer`, `apikey-header`,
  `apikey-query`, `basic`, `json-body`. The credential comes from `secret`
  (plus `secret2` for two-part auth like basic or a paired key). Resolved
  from the `secrets` table at call time (user rows win over folder), on the
  host — never injected into the container. The resolved value is
  exact-string-scrubbed from the HTTP response before the agent sees it,
  including the query string for `apikey-query`.
- **Path-param substitution** — `{name}` segments in `path` are filled from
  the tool call's arguments (`{zone_id}` ← the `zone_id` arg); consumed args
  are removed before the rest become the query/body.
- **Grant scope** — each tool's `scope` (`ext:<service>:<operation>`) is the
  ACL scope an agent must hold to call it. Grant it like any other:
  `ext:cloudflare:*` or the exact `ext:cloudflare:dns:write`.

Operator sets the credential via `arizuko secret <inst> set <folder> KEY
--value V` (folder) or the user via `/dash/me/secrets` (user). Spec
`specs/5/13-ext-mcp.md`.

## Add an OAuth provider

Where an API needs an OAuth "Connect" dance (the user authorizes arizuko to
act as them) rather than a static key, add a **surrogate OAuth provider** (spec
`specs/5/15`). Pure config — no Go, no rebuild:

1. **Drop a descriptor** at `<datadir>/surrogate/<name>.toml` (or edit an
   embedded default by shadowing its name):

   ```toml
   auth_url       = "https://slack.com/oauth/v2/authorize"
   token_url      = "https://slack.com/api/oauth.v2.access"
   revoke_url     = "https://slack.com/api/auth.revoke"
   scopes         = ["chat:write", "channels:read"]
   secret_key     = "SLACK_TOKEN"        # secrets-table key the token lands in
   allowed_domain = "slack.com"
   access_type    = ""                   # "offline" for providers that gate refresh_tokens (google)
   ```

   `auth_url`, `token_url`, `secret_key` are required — a missing one is a hard
   boot error naming the file.

2. **Register the redirect URI** at the provider's app console:
   `<WEB_HOST>/dash/me/connections/<name>/callback`.

3. **Set the operator client creds** in the instance `.env`:
   `SURROGATE_<NAME>_CLIENT_ID` / `SURROGATE_<NAME>_CLIENT_SECRET` (NAME
   upper-cased, `-`→`_`).

4. **Restart** the instance. `Connect <name>` appears in `/dash/me/connections`;
   the obtained access+refresh token is written to the `secrets` table under
   `secret_key` and the broker refreshes it near expiry.

The token then reads like any other secret — reference `secret_key` from an
`[[ext]]` REST provider or an `[[mcp_connector]]` (above) to give agents tools
backed by the user's OAuth grant. `github` and `google` ship as built-ins.

## Adding a slink-driven page

Third-party pages talk to a slink via the embedded JS SDK at
`/assets/arizuko-client.js`. The SDK wraps the `POST → SSE` round-
handle dance; pages call `Arizuko.connect(token)` and stream frames.

- SDK source: `webd/assets/arizuko-client.js` (baked into `webd` via
  `embed.FS`, served with `Cache-Control: public, max-age=3600`,
  CORS `*`).
- Sample page: `template/web/pub/examples/slink-sdk.html` — copy to
  `<data-dir>/web/pub/<app>/index.html`, swap the token.
- Agent skill: `ant/skills/slink/SKILL.md` (drop-in HTML template
  - API table). Sibling skill `slink-mcp/` covers MCP-over-HTTP.
- Spec: `specs/1/Z2-slink-sdk.md`.

For agent-written pages, the slot itself (`~/public_html/`) is
bind-mounted from `<data>/web/pub/<folder>/` and serves at
`/pub/<folder>/`. For multi-app deployments, nest each app under its
own subdir: `~/public_html/<app>/` → `/pub/<folder>/<app>/`. JWT-gated
content lives in `~/private_html/` (or `~/private_html/<app>/`),
served at `/priv/<folder>/` (or `/priv/<folder>/<app>/`). See
`specs/5/V-web-vhosts.md`.

## Extending the public site

The public doc site at `/pub/` is plain HTML copied verbatim from
`template/web/pub/` into each instance's `<data-dir>/web/pub/` on
`arizuko create`. No build step; edit HTML directly. Per-page
conventions (breadcrumbs, prose container, `hub.css` + `hub.js`) and
the site layout (products / components / reference / howto / security)
live in `template/web/CLAUDE.md`. Operator-facing positioning page:
`template/web/pub/arizuko/security/index.html`.

## Inspect tools

Read-only MCP introspection family, registered in `ipc/inspect.go`:
`inspect_messages`, `inspect_routing`, `inspect_tasks`,
`inspect_session`, `inspect_identity`. Delegate to `store.*` accessors; no destructive
operations (those stay in `control_*`). A `/root`-elevated turn
(`auth.Identity.IsRoot`) sees every folder; an ordinary turn is scoped to
its own folder subtree. Extend by adding a handler to
`registerInspect` and wiring a fn into `ipc.StoreFns`.

`find_messages` (registered in `ipc/ipc.go`, backed by
`StoreFns.FindMessages` over the `messages_fts` FTS5 table) is the
content-search complement: FTS5 query syntax (phrase / OR / NOT /
prefix / NEAR), bm25 ranking, snippet, post-fetch `JIDRoutedToFolder`
ACL gate. Use it to find a message by what was said; `inspect_messages`
walks recent history by chat. Spec: `specs/5/C-message-mcp.md`.

## Adapter `/health` contract

Each adapter exposes `GET /health` returning `{ok, name, status, caps}`.
`chanlib.NewAdapterMux` provides the canonical implementation; see
`chanlib/README.md`.

## Skills

Three scopes, no inheritance:

- `ant/skills/` — global, baked into image, read-only
- `groups/<folder>/.claude/skills/` — per-group, persistent
- `.claude/skills/` — per-session, seeded from global on first spawn

**Operator boundary (2026-07-27):** Skills define the capability surface.
In multi-tenant deployments, users cannot add or modify skills — only the
operator can. A user changing skills = expanding their own capability surface,
which bypasses the grants model. Single-user deployments: the user IS the
operator. Per-group skills (`groups/<folder>/.claude/skills/`) are
operator-seeded via `arizuko apply` or `arizuko packages install`; agents
can read and use them but cannot create new skill directories.

Canonical definitions at `/opt/arizuko/ant/skills/` (ro mount) for
`/migrate` diffing. `MIGRATION_VERSION` integer + `/migrate` skill
drive upgrades. `/migrate` runs a real 3-way merge against
`.claude/.merge-base/<path>` (snapshot laid down at seed-time), so
local edits survive upstream changes. A `.disabled` sentinel inside a
skill dir opts that skill out of seeding + migration entirely.

Skill layout:

```
<name>/
  SKILL.md              # required: prompt injection
  CLAUDE.md             # optional
  migrations/           # optional numbered upgrade scripts
```

## Session opening ritual

When a new Claude Code session starts, routd injects a
`<system event="new_session">` directive into the turn's prompt. The agent
already reads its group `CLAUDE.md` at session start. To enforce an opening
ritual (load context, read a plan file, scan skills), add a
`## Session opening` section to the group's `CLAUDE.md`:

```markdown
## Session opening

Before your first reply:

1. Read `~/plans/<topic>.md` if it exists (the durable plan-of-record).
2. Scan `~/.claude/skills/` for skills relevant to the current goal.
3. Read `~/facts/` entries relevant to the user's question.
```

The `new_session` system event is the reliable hook — it fires exactly once
per new session. Instructions here run as a directive at the session boundary,
not buried mid-file.

## Host-tool capabilities

Some integrations are **pure host-tool surfaces** — no daemon, no MCP,
no protocol. The operator installs a CLI in the agent image (or
mounts host state into it) and ships a skill that drives it as a
subprocess. The agent sees an ordinary command on `PATH`; the
skill is the discovery surface; auth flows from a host-side mount or
folder secret. Distinct from MCP tools (in-band, schema-typed,
routd-mediated) and channel adapters (out-of-band, HTTP).

Currently shipping:

| Capability | Binary  | Skill                        | Auth                                                                                                  |
| ---------- | ------- | ---------------------------- | ----------------------------------------------------------------------------------------------------- |
| `oracle`   | `codex` | `ant/skills/oracle/SKILL.md` | `HOST_CODEX_DIR` mount on the agent container **OR** `CODEX_API_KEY` / `OPENAI_API_KEY` folder secret |

Adding one:

1. Install the binary in `ant/Dockerfile` (pinned version where
   upstream supports it).
2. If the tool needs host-side credentials/state, add a `HOST_*_DIR`
   env on `core.Config`, plumb through `compose.Generate` and
   `container.runner` so it's bind-mounted into spawns. Pattern:
   `HOST_CODEX_DIR` → layered mount at `/home/node/.codex` via
   `container/runner.go`.
3. Write `ant/skills/<name>/SKILL.md` with sharp frontmatter
   `description` (this is what `/dispatch` matches on), a "when to
   invoke" section, copy-pasteable invocations, and a missing-auth
   fallback that fails soft instead of crashing the turn.
4. Bump `ant/skills/self/MIGRATION_VERSION` so the auto-migrate
   broadcast fires on next spawn.

Skill body shape: see `ant/skills/oracle/SKILL.md` as the reference.

## Permissions

Capability comes from `acl` rows, never from where a folder sits. A path
is a routing target, JID prefix, container home, and web vhost — depth
means nothing to authorization.

Every folder is bound to `role:member` at create, carrying the messaging
verbs. Everything beyond that (`register_group`, `routes`, `network_*`,
`schedule_*`, `observe_*`, `invite_*`, token mint, `acl`) is an explicit
row someone delegated. `role:operator` holds `*` on `**` WITH GRANT
OPTION and roots every chain; a principal may delegate onward only rows
it holds, and only those it holds WITH GRANT OPTION — so authority
strictly decreases (`auth.Delegate`). Root is a grant the operator
invokes with `/root`, not a folder.

To gate a new action: pick an action name (`mcp:<tool>` for MCP tools),
bind it at registration through the injected `Gate` — `auth.Authorize`
for the operator REST face, `db.Authorize(sub, folder, "mcp:"+tool,
params)` on the agent socket — and seed a row if it should be on by
default. Never add a second check inside the handler. A grant's scope
glob IS its containment: `mcp:register_group` scoped `acme/**` registers
under `acme` and nowhere else.

`escalate_group` sends a message to the parent; it does not grant
permissions. See [`specs/5/33-paths-roles.md`](specs/5/33-paths-roles.md)
(the model), [`specs/5/32-acl-unified.md`](specs/5/32-acl-unified.md)
(the row + evaluator), and `specs/4/19-action-grants.md` (the `params`
rule grammar).
