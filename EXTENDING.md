# Extending arizuko

Catalog of extension points.

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

| Point         | Location                             | Extensible by  | Mechanism                                              |
| ------------- | ------------------------------------ | -------------- | ------------------------------------------------------ |
| Channels      | external containers                  | Developer      | HTTP protocol (latest: `slakd/`)                       |
| Proxyd routes | `template/services/<name>.yaml`      | Daemon author  | `proxyd_routes` block of the manifest, no Go edits     |
| Chat links    | `webd/route_token.go`, `webd/mcp.go` | External agent | `/chat/<token>/` widget and MCP at `/chat/<token>/mcp` |
| Chat SDK      | `webd/assets/arizuko-client.js`      | Page author    | Embedded JS served at `/assets/`                       |
| Actions       | MCP tools                            | Agent/Plugin   | Registry + MCP                                         |
| Autocalls     | `routd/prompt.go`                    | Router dev     | Registry slice                                         |
| Routing rules | `router/`                            | Agent          | MCP tools; `target=<folder>[#mode]` fragment           |
| Mounts        | `container/`                         | Agent          | Container config                                       |
| Skills        | `ant/skills/`                        | Agent          | File-based                                             |
| Tasks         | `timed/`                             | Agent          | IPC actions                                            |
| Diary         | `diary/`                             | Agent          | File-based                                             |
| Network rules | `store/network.go`                   | Operator       | CLI + DB rows                                          |
| Web routes    | `store/web_routes.go`                | Agent          | MCP tools (`set_web_route` / `del_web_route`)          |
| Public pages  | `template/web/pub/`                  | Operator       | Plain HTML, copied into `<data-dir>/web/pub/`          |
| Output styles | `ant/output-styles/`                 | Channel author | `<channel>-<surface>.md`; picked per-session           |
| Agent harness | the ant protocol (MCP socket)        | Developer      | Replace ant with a process that speaks it              |

## Adding an output-style file

Per-channel + per-surface tone, length, and formatting hints applied
at session bind. `runed/settings.go::pickOutputStyle` derives
`<channel>-<surface>` from inbound JID + topic + `paneLookup`; falls
back to `<channel>.md` for channels with no per-surface split. Spec:
`specs/5/Y-output-styles-per-surface.md`. Conventions, file list, and
adding-a-new-file recipe: `ant/output-styles/README.md`.

No registration step — `container.SeedOutputStyles` discovers files by
directory listing at spawn. Drop the file, rebuild the agent image
(`make agent`), redeploy.

## Adding a host mount (`/mnt/<name>`)

The operator can bind-mount host dirs (read-only) into every guest of
a group at `/mnt/<name>` — a mount named `data/binance_perp` appears at
`/mnt/data/binance_perp`. Two parts:

1. **Instance allowlist** — `MOUNT_ALLOWED_ROOTS=/path1,/path2` in the
   instance `.env`, then `arizuko generate` and restart `runed`. `virtiofsd`
   is runed's own child, so a guest can be shown only a path runed can open
   itself; `generate` bind-mounts every allowed root into runed's own
   container at the same absolute path (spec `6/41` §"The runed deploy
   topology"). A host path not under an allowed root is rejected and logged.
2. **Per-group mounts** — `container_config.Mounts` on the group's row, a
   JSON array of `{"Host":"/path","Container":"name","RO":true}`. Read it,
   edit it, apply it back:
   ```bash
   arizuko get krons groups > groups.yaml    # edit container_config
   arizuko apply krons groups.yaml
   ```
   ```yaml
   groups:
     - folder: atlas/content
       container_config: '{"Mounts":[{"Host":"/srv/data/binance_perp","Container":"data/binance_perp","RO":true}]}'
   ```

After either change restart `runed`; the next spawn picks it up, a running
session waits until then. The agent-side view is the mount table in
`ant/skills/self/workspace.md`.

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

| Verb         | discd | slakd | bskyd | reditd | teled | emaid | linkd | whapd | twitd | gitd |
| ------------ | ----- | ----- | ----- | ------ | ----- | ----- | ----- | ----- | ----- | ---- |
| `send`       | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     | ✓¹   |
| `reply`      | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     | ✓¹   |
| `send_file`  | ✓     | ✓     |       |        | ✓     |       |       | ✓     | ✓     |      |
| `send_voice` | ✓     |       |       |        | ✓     |       |       | ✓     |       |      |
| `post`       | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |      |
| `like`       | ✓     | ✓     | ✓     | ✓      | ✓     |       |       | ✓     | ✓     |      |
| `delete`     | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |      |
| `forward`    |       |       |       |        | ✓     |       |       | ✓     |       |      |
| `quote`      |       |       | ✓     |        |       |       |       |       | ✓     |      |
| `repost`     |       |       | ✓     |        |       |       |       |       | ✓     |      |
| `dislike`    |       | ✓     |       | ✓      |       |       |       |       |       |      |
| `edit`       | ✓     | ✓     |       |        | ✓     |       |       | ✓     |       |      |
| `pin`        | ✓     | ✓     |       |        | ✓     |       |       |       |       |      |
| `unpin`      | ✓     | ✓     |       |        | ✓     |       |       |       |       |      |
| `unpin_all`  |       | ✓     |       |        | ✓     |       |       |       |       |      |

¹ gitd posts a comment on the PR/issue the JID names, only when `GITHUB_TOKEN`
is set; repo state changes (merge, label, review) are GitHub connector tools
(spec `6/27`).

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
(`matchWebRoute`, `proxyd/main.go`). These rows only take effect if proxyd is
configured with a store (`s.st != nil`); they have no effect on channel
routing.

## Installing packages

`arizuko packages <instance> add | install | upgrade | remove` manages packages
(spec `5/28`). `add <name>` takes the package from the bundled catalog and
`install <source>` from a git URL or local dir; both run the same install. A
package is a process plus a declaration of how it hooks in — **two file
kinds**, split by who owns the format:

- **compose fragment** — `<name>.yml`, docker's format, included verbatim →
  placed in `services/` (a link at the catalog for `add`, a copy for
  `install`), brought up on `arizuko generate`.
- **manifest** — `<name>.yaml`, arizuko's `5/8` config manifest → applied
  through the same engine as `arizuko apply`: one transaction per owner DB
  with pre-image rollback, so a package installs whole or not at all. Every
  resource `apply` writes is available to a package — `proxyd_routes`, `acl`,
  `scheduled_tasks`, `network_rules`, `web_routes`, `routes`, `groups`. Rows
  land live (proxyd reads `proxyd_routes` per request, so a route takes effect
  with **no restart**); the compose fragment waits for `generate`. The
  manifest's `env` section names what the process needs from the instance
  `.env`, per key: `generate: true` (minted once at install, then the
  operator's line), `default: <value>` (derived on every generate, never
  written; a `.env` value wins), `required: true` (install and generate refuse
  until the operator sets it — never a fragment that installs and then fails
  to start). Docker interpolates `${KEY}` from `.env`, so a third-party image
  reads a minted key with no env file of its own.
- **skills** — `skills/<name>/` → copied to `<datadir>/skills/`, layered into
  every group by `seedSkills` at spawn.

```yaml
# helpbot.yaml — a package manifest
env:
  HELPBOT_KEY: { generate: true }
proxyd_routes:
  - path: /helpbot/
    backend: http://helpbot:8080
    auth: user
acl:
  - principal: folder:main
    action: mcp:send
    scope: main
    effect: allow
network_rules:
  - folder: main
    target: helpbot.example.com
```

`installed_packages` records the PK of every row the package owns, so `remove`
withdraws exactly those and leaves an operator's own rows at other keys alone.
Resources `apply` never rebuilds (`secrets`, `route_tokens`, `invites`,
`onboarding`, `installed_packages`) are refused at install rather than accepted
as a silent no-op.

```bash
arizuko packages krons install github.com/org/pkg   # or a local dir
arizuko packages krons upgrade pkg                   # refuses locally-edited (dirty) assets
arizuko packages krons remove  pkg                   # withdraws routes, then drops fragments
```

Each install records an **installed-package record** in routd's
`installed_packages` (source +
resolved revision + owned identities + per-asset content hash), keyed
`(folder, name)` with `''` meaning instance-wide. That record drives `upgrade`
(dirty-detection — never overwrites a locally edited asset), `remove` (deletes
exactly the identities it owns), and reproducibility.

## Blending several products into one group

A group is not limited to one product. Write `groups/<folder>/products.toml`
(the agent sees it as `~/products.toml`) with one block per product, in the
order they blend, then apply it:

```toml
[[product]]
source = "ant/examples/support"     # relative → joined to HOST_APP_DIR

[[product]]
source = "github.com/org/our-brand" # git → shallow-cloned, pinned to HEAD
```

```bash
arizuko products krons apply main
```

For a browser skill without a persona, [examples/cuzk](examples/cuzk/README.md)
uses the same composition with `source = "examples/cuzk"`.

Blending is per PAYLOAD KIND, never a content merge (two products share no
merge base). `skills/` union by name with the last product winning wholesale;
`PERSONA.md`/`SOUL.md` first-provider-wins; `CLAUDE.md` becomes one marked
region per product (`<!-- arizuko:package:<name> BEGIN/END -->`) so operator
text between regions is never touched; `facts/` unions and
REFUSES a filename collision; `tasks.toml` and
`migrations/` REFUSE the apply outright, because nothing reads a product's
tasks and nothing runs a product's
migrations, and seeding them would report success for instructions that never
execute (`5/28`); `.claude/settings.json`
keys union and refuse a key collision, except `mcpServers`, which REFUSES the
apply — no MCP server loads from that key, so ship third-party tools as an
arizuko connector instead (BUGS `J15`); at most one
`Dockerfile.ant`; **anything else is copied whole, first provider wins**.

Check any of this against the running code rather than this paragraph:
`tests/lab/product-lab.sh` builds a throwaway instance, applies
`ant/examples/helpdesk` (the reference product — one of every payload kind that
has a consumer), asserts each rule above, and installs a package shipping a
compose fragment, a proxyd route and a grant. No docker, no root, no model
credits. A rule that stops holding fails the script.

Identity and knowledge seed once and become the group's own state — re-applying
never rewrites them. Skills stay upstream-managed: re-apply
takes a new revision, but a file edited since the last apply is reported and
skipped, never clobbered. `arizuko packages <inst> sync` re-applies every
group's mix alongside the instance's packages. Full table: spec `5/28`.

`arizuko create --product` / `group add --product` still take exactly one
product and copy it verbatim — a single template, not a mix.

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
   `template/services/<name>d.yaml` with a `proxyd_routes` block (see
   below) — no `compose.go` edits.
3. Implement only the verbs the platform supports natively; return
   `chanlib.Unsupported(tool, platform, hint)` for the rest.
4. Spec under `specs/2/<letter>-<name>.md`; per-platform native
   support belongs in the verb matrix above.

### Pickable answers (the `buttons` capability)

`SendRequest.Options []Option{label, data}` carries the answers a reader
can pick without typing. It is a RENDER capability on `send_text`, not a
verb, so it has no `/send-options` route and no entry in the verb matrix.

Declare `"buttons": true` in your caps map only if the platform draws them
natively, then read `req.Options` in `Send` and attach the platform's own
widget. **Set each button's callback payload to `Option.Send()` verbatim**
— that string is the command the press stands for — and map the press back
to an ordinary `InboundMsg` whose `Content` is exactly that payload, from
the presser's own platform id. An adapter never interprets an option and
never authorizes one: a press and a typed command are then the same
inbound text meeting the same gate, which is what keeps a button from
becoming a second authorization path (spec
[`5/19`](specs/5/19-hitl-firewall.md)).

Declare nothing and you are done: `chanreg.HTTPChannel.SendOptionsCtx`
folds the answers into typable text before the request leaves routd, so an
adapter without the cap never sees an options field and its readers still
get an answerable prompt. That fold is the ONE fold — never reimplement it
adapter-side.

Platform limits, verified against each vendor's reference: Discord draws
five buttons per row and Slack 25 per actions block (both adapters drop the
rest rather than fail the send); Telegram caps `callback_data` at 64 bytes
and Discord `custom_id` at 100. Telegram allows eight to a row; all three
adapters draw the answers side by side, because a verdict is a choice
BETWEEN them and a stack reads as separate actions.

**Reactions need nothing from you.** A green/red reaction on a held notice is
read as its verdict by routd, from the `Reaction` / `ReplyTo` your adapter
already delivers — which is how a platform with no button surface still answers
a prompt. Report a reaction's target the way the others do and it works: the
lookup takes the last path segment, so `<id>` and `<chat-jid>/<id>` both match.

Generic HTTP webhook ingest is the same path: any caller can POST
`/v1/messages` with the channel-protocol envelope; route inbound
webhooks through proxyd via the receiving daemon's routes file. No
separate "webhook adapter" abstraction.

## Checking a component against the contract

Any daemon — yours, or one shipped from outside this repo — is checked
against the component contract by
probing it while it runs:

```bash
arizuko probe http://localhost:8081
arizuko probe https://host --token <bearer> --foreign-folder <someone-elses>
```

It verifies that `/openapi.json` answers **without** credentials (mounted
before auth, or nothing can discover you), that the document is OpenAPI
3.x, that every path it advertises is actually served, that non-public
paths refuse an unauthenticated request, and — with the two flags — that a
folder param is bound to the caller rather than honoured as given.

An empty document passes: a component that owns no `/v1` resource (dashd
serves only `/dash/` pages) truthfully advertises nothing. What the probe
cannot see is the opposite drift — a resource mounted but not advertised.
Probing cannot establish that, because proxyd answers every path from a
`/` catch-all; in-repo that direction is a Go test
(`resregtest.AssertServesNoForeignResource`).

## Adding a proxyd route

`proxyd`'s route table is built from the `proxyd_routes` block of each
package's `services/<name>.yaml`, read at compose-generate time, plus a
static core-route slice in `compose/compose.go` (`coreProxydRoutes`, for
dashd/webd/davd/onbod). Routes cannot live in the compose fragment —
proxyd, not docker, is their consumer. Adding a new inbound web path =
one manifest block, no Go edits:

```yaml
proxyd_routes:
  - path: /<prefix>/
    backend: http://<name>d:8080
    auth: public
    gated_by: <ENV_VAR>
    preserve_headers: [X-Webhook-Sig]
```

`path` with a trailing `/` matches longest-prefix; `auth` is
`public` | `user` | `operator`; `gated_by` drops the route when that
env var is unset; `preserve_headers` passes listed headers verbatim.
`compose.go` evaluates `gated_by` against the operator's `.env` and drops
disabled routes; `arizuko generate` writes the survivors into
`proxyd_routes` as arizuko-owned rows, so a route added to a later release
reaches an existing instance.

That file is the SAME manifest `arizuko packages install` applies — one
declaration with two readers, because generate needs the routes before any
daemon or DB exists. It is never a second format (BUGS `L44`), and both
readers parse it with `resreg.ParseYAML`, so a typo is rejected identically
by each. Reference: `specs/5/7-proxyd-standalone.md`,
`template/services/slakd.yaml`.

## Adding an MCP connector

Connectors are global and operator-defined. Every `[[connector]]` block in
`<data_dir>/connectors.toml` registers at the deployment level, and a group
reaches a connector's tools through grant rows (`mcp:<connector>:*`), never
by registering an MCP endpoint of its own.

A block with `transport = "mcp"` declares one HTTPS endpoint: `url`, the
`secrets` the broker may resolve for it, and a `headers` map whose
`{secret:KEY}` placeholders the broker renders per call. routd discovers the
server's tools per folder with that folder's credential, names them
`<connector>_<remote_tool>`, and registers each through the broker chain. A
call resolves the declared secrets, renders the headers, proxies
`tools/call`, and scrubs every resolved value out of the result. The loader
refuses any other table name or transport value, so a misspelt block fails
routd's boot instead of configuring nothing. Specs: `specs/5/13-ext-mcp.md`
§ "Shape 2 — MCP HTTP connector", `specs/6/19-unified-tool-declaration.md`;
operator copies in `template/connectors/`.

```toml
[[connector]]
name      = "github"
transport = "mcp"
url       = "https://api.githubcopilot.com/mcp/"
secrets   = ["GITHUB_TOKEN"]

[connector.headers]
Authorization = "Bearer {secret:GITHUB_TOKEN}"
```

- `secrets` names the broker keys this connector may see; the folder's
  other secrets are never visible to it. Resolution is `user(caller.Sub)`
  first, then `folder(caller.Folder)`.
- The credential is a request header, never a process environment. There is
  no subprocess, so an endpoint needs no runtime inside routd's image.
- Result scrubbing replaces every non-empty resolved secret value with
  `«redacted»` in the `mcp.CallToolResult` before the agent sees it.
- `CONNECTOR_CALL_TIMEOUT_MS` caps each call (default 30 s).

A folder secret and a daemon environment variable can share a name and hold
different values. `GH_TOKEN` in the instance `.env` belongs to a daemon;
`GH_TOKEN` in a folder's `secrets` rows is what the broker renders into the
connector's request. Identify a credential by the door that uses it, not by
the first place its name appears.

The operator writes a folder's token with `arizuko secret <inst> set
<folder> KEY --value V`, a user's with `arizuko user-secret <inst> set <sub>
KEY --value V`, or hands the user `/dash/me/secrets`.

## Adding a REST connector

An HTTP API with no MCP server of its own (Cloudflare, Porkbun, Gandi,
Namecheap) plugs in as the same `[[connector]]` block with
`transport = "rest"` and one `[[connector.tool]]` per endpoint.
`routd/ext.go` `LoadExtProviders` reads the blocks at boot, and each tool
becomes one REST-backed MCP tool (`ipc/extcall.go` `ExtTool`) that maps a
tool call to one outbound HTTP request. This is shape 3 of the credential
model (`specs/5/13-ext-mcp.md`); the MCP connector above is shape 2. The
block below is the first two tools of the shipped
`routd/extproviders/cloudflare.toml`:

```toml
[[connector]]
name      = "cloudflare"
transport = "rest"
url       = "https://api.cloudflare.com/client/v4"
secrets   = ["CF_API_TOKEN"]
headers   = { Authorization = "Bearer {secret:CF_API_TOKEN}" }

  [[connector.tool]]
  name        = "dns_list"
  description = "List DNS records for a Cloudflare zone."
  scope       = "ext:cloudflare:dns:read"
  method      = "GET"
  path        = "/zones/{zone_id}/dns_records"

  [[connector.tool]]
  name        = "dns_create"
  description = "Create a DNS record in a Cloudflare zone."
  scope       = "ext:cloudflare:dns:write"
  method      = "POST"
  path        = "/zones/{zone_id}/dns_records"

    [[connector.tool.param]]
    name        = "type"
    required    = true
    description = "Record type: A, AAAA, CNAME, TXT, MX, ..."
```

- **Three credential sinks, one vocabulary.** `headers`, `query` and `body`
  each carry `{secret:KEY}` placeholders, rendered per call against the
  broker-resolved values (user rows win over folder rows). A placeholder that
  resolves to nothing is dropped rather than sent blank. Every resolved value
  is scrubbed from the response before the agent sees it.
- **Path placeholders are arguments.** `{zone_id}` in `path` becomes a
  required string argument on the tool without a `param` block; a
  `[[connector.tool.param]]` adds a typed body or query argument.
- **Grant scope.** Each tool's `scope` (`ext:<service>:<operation>`) is the
  ACL scope an agent must hold to call it: `ext:cloudflare:*` or the exact
  `ext:cloudflare:dns:write`.
- **Built-ins and shadowing.** `routd/extproviders/*.toml` ships with routd.
  An operator block with the same `<connector>_<tool>` name replaces the
  built-in and logs the shadow; two declarations of one name inside a single
  file are refused.

The folder-secret rule from the MCP connector above applies unchanged: the
token comes from the folder's `secrets` row, and a same-named daemon env var
is a different value.

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
   boot error naming the file. So is two providers sharing a `secret_key`: the
   dance upserts by that key alone, so the second `Connect` would destroy the
   first one's credential. Shadowing a built-in's NAME while changing its
   `auth_url`/`token_url` logs a warning — creds bind by name, so the built-in's
   operator `client_secret` goes to the endpoint your file names.

2. **Register the redirect URI** at the provider's app console:
   `<WEB_HOST>/dash/me/connections/<name>/callback`.

3. **Set the operator client creds** in the instance `.env`:
   `SURROGATE_<NAME>_CLIENT_ID` / `SURROGATE_<NAME>_CLIENT_SECRET` (NAME
   upper-cased, `-`→`_`).

4. **Restart** the instance. `Connect <name>` appears in `/dash/me/connections`;
   the obtained access+refresh token is written to the `secrets` table under
   `secret_key` and the broker refreshes it near expiry.

The token then reads like any other secret — reference `secret_key` from an
`[[connector]]` block, `transport = "rest"` or `"mcp"` (above), to give agents tools
backed by the user's OAuth grant. `github` and `google` ship as built-ins.

## Adding a chat-SDK page

Third-party pages talk to a chat link via the embedded JS SDK at
`/assets/arizuko-client.js`. The SDK wraps the `POST` and SSE round trip;
pages call `Arizuko.connect(token)` and stream frames.

- SDK source: `webd/assets/arizuko-client.js`, embedded in `webd`.
- Walkthrough and sample page:
  `template/web/pub/arizuko/howto/chat-sdk.html` and
  `template/web/pub/arizuko/examples/chat-sdk.html`. Copy the sample to
  `<data-dir>/web/pub/<app>/index.html` and swap the token.
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
`StoreFns.FindMessages` over routd's in-memory BM25 index,
`routd/msgsearch`) is the content-search complement: terms that must all
appear, `"phrases"`, `-exclusions`, bm25 ranking, a «»-marked snippet,
post-fetch `JIDRoutedToFolder` ACL gate. The index is derived from the
`messages` entity — routd walks the store into it at boot and feeds it
from its own writes — so a search made while the walk is running is
refused rather than answered empty. Use it to find a message by what was
said; `inspect_messages` walks recent history by chat. Specs:
`specs/5/C-message-mcp.md`, `specs/6/42-dynamo-over-postgres.md` §8.

## Adapter `/health` contract

Each adapter exposes `GET /health` returning `{ok, name, status, caps}`.
`chanlib.NewAdapterMux` provides the canonical implementation; see
`chanlib/README.md`.

## Skills

Three scopes, no inheritance:

- `ant/skills/` — global, baked into image, read-only
- `groups/<folder>/.claude/skills/` — per-group, persistent
- `.claude/skills/` — per-session, seeded from global on first spawn

**Operator boundary:** Skills define the capability surface.
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
skill is the discovery surface. Auth flows from a host-side mount or, for a
model key, the host `.env` / the user's own `/dash/me/env` row — a capability
credential cannot back one of these, because it never reaches guest env
(spec `5/14`). Distinct from MCP tools (in-band, schema-typed,
routd-mediated) and channel adapters (out-of-band, HTTP).

Currently shipping:

| Capability | Binary  | Skill                        | Auth                                                                                                                                  |
| ---------- | ------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `oracle`   | `codex` | `ant/skills/oracle/SKILL.md` | `HOST_CODEX_DIR` mount on the agent guest **OR** `CODEX_API_KEY` / `OPENAI_API_KEY` from the host `.env` or the user's `/dash/me/env` |

Adding one:

1. Install the binary in `ant/Dockerfile` (pinned version where
   upstream supports it).
2. If the tool needs host-side credentials/state, add a `HOST_*_DIR`
   env on `core.Config`, plumb through `compose.Generate` and the turn
   renderer so it's bind-mounted into spawns. Pattern: `HOST_CODEX_DIR` →
   layered mount at `/home/node/.codex` via `runed/mounts.go`.
3. Write `ant/skills/<name>/SKILL.md` with sharp frontmatter
   `description` (this is what `/dispatch` matches on), a "when to
   invoke" section, copy-pasteable invocations, and a missing-auth
   fallback that fails soft instead of crashing the turn.
4. Bump `ant/skills/self/MIGRATION_VERSION` so the auto-migrate
   broadcast fires on next spawn.

Skill body shape: see `ant/skills/oracle/SKILL.md` as the reference.

## Swapping the agent harness

ant wraps an agentic harness; it never is one. The extension point is the
**process boundary**, not an interface inside ant: `routd` serves the
per-turn MCP socket in-process (`routd/mcp.go` `ServeTurnMCP`), and ant
is just a client of it (`socat STDIO UNIX-CONNECT:/run/ipc/gated.sock`,
`ant/src/mcp-servers.ts`). A different harness replaces ant wholesale by
speaking that protocol as its own process — the agent tools it publishes
via `tools/list`, plus the `submit_turn` method that reports the turn. No
arizuko code change, any language.

What that process owes: read the JSON turn input on stdin, run the turn
against the socket, call `submit_turn` exactly once. Mid-turn steering is
the harness's own business. ant drains `/run/ipc/input` between tool calls
(`ant/src/index.ts` `pollIpcDuringQuery`); another harness maps it to
whatever its protocol offers.

Short of a wholesale replacement, ant ships a harness chassis: its runtime
half (stdin parse, IPC steering, the `<status>` pipeline, idempotent
`submit_turn`) is reused, and the harness half is one interface (`Turn` in,
`Frame`s out, one `run()` plus a capability profile) plus one file per
backend — `ant/src/claude.ts` (default), `codex.ts`, `pi.ts`.
Select with `ARIZUKO_BACKEND=codex|pi` in the instance `.env`; the CLI
must be baked into the agent image. What each backend can and cannot do
(MCP tools, skills, the skill-guard) is the spec's conformance matrix —
truthful deltas, not parity. Spec [`5/P`](specs/5/P-runed.md) § "ant
wraps a harness, it never is one" still governs the wholesale lane.

## Permissions

Capability comes from `acl` rows, never from where a folder sits. A path
is a routing target, JID prefix, guest home, and web vhost — depth
means nothing to authorization.

Every folder is bound to `role:member` at create, carrying the messaging
verbs. Everything beyond that (`register_group`, `routes`, `network_*`,
`schedule_*`, `observe_*`, `invite_*`, token mint, `acl`) is an explicit
row someone delegated. `role:operator` holds `*` on `**` WITH GRANT
OPTION and roots every chain; a principal may delegate onward only rows
it holds, and only those it holds WITH GRANT OPTION — so authority
strictly decreases (`auth.Delegate`). Root is a grant the operator
invokes with `/root`, not a folder.

A **world** is granted two rows instead (`onbod` `writeWorld`): `mcp:*`
and `hold:mcp:*`, both scoped to its own subtree. It may reach any tool
inside itself, and a human answers for every call it makes — approval,
not the grant, is what a person controls there. `role:member` carries
the other half: a skip-approval row (`hold:mcp:<tool>` at `deny`) for
each messaging verb and for the reads every turn mounts, so the agent
still answers a message and reads its own notes without asking anyone.
`store.SeedRoutdFloor` writes both halves; `routd`'s
`TestWorld_HoldsOnlyWhatAPersonShouldSee` fails if a new tool lands on
the wrong side of that line.

To gate a new action: pick an action name (`mcp:<tool>` for MCP tools),
bind it at registration through the injected `Gate` — `auth.Authorize`
for the operator REST face, `db.AuthorizeTurn(sender, turn-folder,
target, "mcp:"+tool, params)` on the agent socket — and seed a row if it
should be on by default. Never add a second check inside the handler. A
grant's scope glob IS its containment: `mcp:register_group` scoped
`acme/**` registers under `acme` and nowhere else. The agent gate reads
two axes — the turn's folder confers, the attested sender adds inside
that folder's subtree (`specs/5/33` §"Two axes") — so a new action is
grantable to a folder, to a person, or to both.

`escalate_group` sends a message to the parent; it does not grant
permissions. See [`specs/5/33-paths-roles.md`](specs/5/33-paths-roles.md)
(the model), [`specs/5/32-acl-unified.md`](specs/5/32-acl-unified.md)
(the row + evaluator), and `specs/4/19-action-grants.md` (the `params`
rule grammar).
