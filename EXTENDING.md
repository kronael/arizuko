# Extending arizuko

This file lists the extension points of arizuko.

An extension point lets you add an integration. Examples are a channel
adapter, a speech backend, an oracle skill, a mount for one folder, an
autocall, a scheduled task and a network egress rule. An extension point does
not change the system core. The core is routd, runed, authd, store, ipc, auth,
the grants, proxyd, webd, dashd, timed, onbod, the container runner and
chanlib/chanreg. The core changes as a unit, through specs and not through
these points. [ARCHITECTURE.md](ARCHITECTURE.md#core-vs-integrations) explains
the split, and [its repository map](ARCHITECTURE.md#where-a-change-lands) shows
where a change lands.

## Each extension point has an owner and a mechanism

| Point         | Location                               | Extensible by  | Mechanism                                              |
| ------------- | -------------------------------------- | -------------- | ------------------------------------------------------ |
| Channels      | external containers                    | Developer      | HTTP protocol (latest: `slakd/`)                       |
| Proxyd routes | `template/services/<name>/arizuko.yml` | Daemon author  | `proxyd_routes` block of the manifest, no Go edits     |
| Chat links    | `webd/route_token.go`, `webd/mcp.go`   | External agent | `/chat/<token>/` widget and MCP at `/chat/<token>/mcp` |
| Chat SDK      | `webd/assets/arizuko-client.js`        | Page author    | Embedded JS served at `/assets/`                       |
| Actions       | MCP tools                              | Agent/Plugin   | Registry + MCP                                         |
| Autocalls     | `routd/prompt.go`                      | Router dev     | Registry slice                                         |
| Routing rules | `router/`                              | Agent          | MCP tools; `target=<folder>[#mode]` fragment           |
| Mounts        | `container/`                           | Agent          | Container config                                       |
| Skills        | `ant/skills/`                          | Agent          | File-based                                             |
| Tasks         | `timed/`                               | Agent          | IPC actions                                            |
| Diary         | `diary/`                               | Agent          | File-based                                             |
| Network rules | `store/network.go`                     | Operator       | CLI + DB rows                                          |
| Web routes    | `store/web_routes.go`                  | Agent          | MCP tools (`set_web_route` / `del_web_route`)          |
| Public pages  | `template/web/pub/`                    | Operator       | Plain HTML, copied into `<data-dir>/web/pub/`          |
| Agent harness | the ant protocol (MCP socket)          | Developer      | Replace ant with a process that speaks it              |

## Adding a host mount at `/mnt/<name>`

The operator can mount host directories, read-only, into every guest of a group
at `/mnt/<name>`. A mount named `data/binance_perp` appears at
`/mnt/data/binance_perp`. Two settings control a mount:

1. **The allowlist of the instance.** Set `MOUNT_ALLOWED_ROOTS=/path1,/path2`
   in the `.env` of the instance. Then run `arizuko generate` and restart
   `runed`. `virtiofsd` is a child process of `runed`, so a guest can see only a
   path that `runed` can open. `generate` therefore bind-mounts every allowed
   root into the container of `runed` at the same absolute path. `runed`
   rejects and logs a host path that is not under an allowed root.
2. **The mounts of a group.** `container_config.Mounts` on the row of the group
   is a JSON array of `{"Host":"/path","Container":"name","RO":true}`. Read the
   row with `arizuko get -i krons groups`:
   ```yml
   groups:
     - folder: atlas/content
       container_config: '{"Mounts":[{"Host":"/srv/data/binance_perp","Container":"data/binance_perp","RO":true}]}'
   ```
   No `arizuko` command writes `container_config`: `group add` and
   `package` never touch it, and `archive apply` restores a whole backup.

After either change, restart `runed`. The next spawn uses the change, and a
running session waits until then. The mount table in
`ant/skills/self/workspace.md` shows the view of the agent.

## Adding an autocall

An autocall puts a fact with no arguments, one line and no side effect into the
`<autocalls>` block at the top of every prompt. An autocall costs less than an
MCP tool when the schema would cost more than the data. It has no schema for the
agent to read, no tool call and one line of output for each turn.

Use this test. If the name, the description, the input schema and the output
shape of a tool would exceed three times the data it returns, make it an
autocall. Most facts of the form "what is X right now" qualify.

| Criterion                  | Autocall        | MCP tool             |
| -------------------------- | --------------- | -------------------- |
| Result size                | at most 1 line  | bounded but variable |
| Needs args                 | no              | yes                  |
| Agent decides when to read | no, every turn  | yes, on demand       |
| Side effects               | none            | allowed              |
| Freshness                  | at prompt build | at call time         |

An autocall follows these rules:

- The result is at most one line of text. An empty string skips the line.
- It takes no arguments, makes no network call and takes no lock. It must
  resolve in microseconds.
- It derives its value from `autocallCtx`, which holds the instance, the
  folder, the session and the current time.
- If one of these rules does not hold, use an MCP tool instead.

The autocall `session` is the one that reads the store. `autocallsBlock` asks
`db.SessionID` before the block renders, and the value is cut to 8 characters.
It still costs no schema and no round trip that the agent sees.

Add an entry to the `autocalls` registry slice in `routd/prompt.go`. This
example shows the shape of an entry. No `world` autocall exists:

```go
{"world", func(c autocallCtx) string {
    return strings.SplitN(c.Folder, "/", 2)[0]
}},
```

Then update the autocalls section of `ant/skills/self/SKILL.md`.

## Designing MCP tools for LLMs

[`ipc/README.md`](ipc/README.md#designing-a-tool) explains how to write the
description of a tool and when an intent earns its own tool.

<a id="verb-support-matrix"></a>

## Each adapter advertises only the verbs it implements

The table lists the outbound MCP verbs and the adapters that advertise them.
`routd` forwards a verb only to an adapter that registered the capability.
[`chanreg/README.md`](chanreg/README.md) § Capabilities gate every verb says how.
For an empty cell, `routd` answers with `UnsupportedError` and makes no call. Read
the table from the `caps` of each adapter in `<adapter>/main.go`, or from the
`capabilities` block in `src/client.ts` for whapd.

| Verb         | discd | slakd | teled | emaid | whapd | gitd | googd |
| ------------ | ----- | ----- | ----- | ----- | ----- | ---- | ----- |
| `send`       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓¹   | ✓²    |
| `reply`      | ✓     | ✓     | ✓     | ✓     | ✓     | ✓¹   | ✓²    |
| `send_file`  | ✓     | ✓     | ✓     |       | ✓     | ✓¹   |       |
| `send_voice` | ✓     |       | ✓     |       | ✓     |      |       |
| `post`       |       | ✓     | ✓     |       |       |      |       |
| `like`       | ✓     | ✓     | ✓     |       | ✓     |      |       |
| `dislike`    |       | ✓     |       |       |       |      |       |
| `delete`     | ✓     | ✓     | ✓     |       | ✓     |      |       |
| `edit`       | ✓     | ✓     | ✓     |       | ✓     |      |       |
| `forward`    |       |       | ✓     |       | ✓     |      |       |
| `quote`      | ✓     |       |       |       |       |      |       |
| `repost`     |       |       |       |       |       |      |       |
| `pin`        | ✓     | ✓     | ✓     |       |       |      |       |
| `unpin`      | ✓     | ✓     | ✓     |       |       |      |       |
| `unpin_all`  | ³     | ✓     | ✓     |       |       |      |       |

¹ gitd advertises its verbs only when `GITHUB_TOKEN` is set. It posts a comment
on the PR or issue that the JID names. It advertises no `edit`, so GitHub shows
the `👀` mark and no progress comment. Changes to the state of the repository, such as merge, label or review, are
tools of the GitHub connector.
² googd advertises `send` only while Google accepts its service-account key,
and its `Send` still answers 400. The reply path is not built, see `googd/README.md`.
³ Discord has no bulk unpin. Call `unpin_message` for each id.

`pin_message`, `unpin_message` and `unpin_all` are three tool names over one
`/unpin` route. `unpin_all` is `/unpin` with `all: true`. The names differ
because the intents differ. An adapter with no pin primitive embeds
`chanlib.NoPinSupport`. The rule of the platform decides which message an agent
may edit or delete, and arizuko adds no grant for the message of another
author. No tool looks up a target id. The agent reads message ids from the `id`
and `platform_id` attributes of the messages in its prompt.

## Adding web routes

The `web_routes` table lets an agent open or gate URL paths under `/pub/` on
the proxyd host, without a redeploy. Three MCP tools manage the table. `routd`
registers them from `routd/web_routes_resource.go`:

| Tool              | Effect                                                                |
| ----------------- | --------------------------------------------------------------------- |
| `set_web_route`   | Upsert a row from `path`, `access` and, for a redirect, `redirect_to` |
| `del_web_route`   | Remove a row that the calling folder owns                             |
| `list_web_routes` | List the rows that the calling folder owns                            |

The value of `access` is one of four words. `public` needs no login. `auth`
requires a login. `deny` answers 403. `redirect` answers 302 to `redirect_to`. `proxyd` picks the row with
the longest matching prefix, see `matchWebRoute` in `proxyd/main.go`. The rows
apply only to requests under `/pub/`. They have no effect on channel routing.

`set_web_route` enforces three rules:

- `path` must start with `/`.
- `redirect_to` must point into the slot of the calling folder, `/pub/<folder>/`
  or `/priv/<folder>/`. This rule prevents an open redirect.
- A path outside the own slot of the folder is claimed by the first folder that
  sets it. A second folder gets 403. A redirect above the slot of any other
  group is refused with 409, because the longest-prefix match would take over
  the publish path of that group.

## Installing packages

A package is a directory with an `arizuko.yml` that defines it. The operator
selects packages with `arizuko package` commands. Each selection is a row in
the `installed_packages` resource.

```sh
arizuko package add [-g folder] <source>
arizuko package rm [-g folder] <name>
arizuko package sync
```

`add` installs a package and records the selection. `rm` withdraws the
selection. `sync` installs every selected package again from its source.
Without `-g`, the package applies to the whole instance. With `-g folder`, it
applies to that group only. Use `-i/--instance NAME` to select another
instance. The default comes from the configured environment. Use `-f/--force`
only to replace an existing destination that no package owns.

Installing `github.com/kronael/tools` requires `uv` on the host and can download
Python and dependencies. The installed checker runs offline in the guest.

### Package definition

`arizuko.yml` in a package source defines that package and nothing else. It
holds the package `name`, brand, tagline, environment needs, the `install`
command and the resource rows that the package owns, for example
`network_rules`, `scheduled_tasks` and routes. It never selects a package.
Only the `installed_packages` rows select packages.

`catalog:<name>` selects a bundled service package. Other sources are local
directories or Git sources. Relative paths resolve under `HOST_APP_DIR`.
Each source declares `name` in its own `arizuko.yml`; the source directory name
does not define package identity.
The bundled `ant/` declares `core`, an ordinary package installed for every group.
Startup refreshes core from the release without running a build.

Resource rows in `arizuko.yml` are typed Arizuko rows.
Each YAML document targets one owning daemon; use `---` between owners.
The existing resource engine checks types, authorization, checksums and audit.
A supplied checksum guards against concurrent changes; an omitted checksum
uses the current state. A package cannot declare `acl` rows.

[Compose package flow](compose/README.md#packages) shows installation through service deployment.

`package add` creates the rows and services that the package declares.
`package rm` withdraws them. `package sync` writes compose configuration.
The existing deployment procedure starts or restarts services.

### Order and overrides

Core is always first. Packages follow in the order the operator added them.
To move a package later, run `package rm` and then `package add`.

A later package overrides an earlier package on the same skill, hook, tool
(`bin/`) or `group/` path. `package sync` prints one line for each override:

```text
instance: kronael-tools overrides core on skills/diary
```

### Package assets

A package can supply these assets:

- `group/` supplies files for the group folder at the same relative paths. Package-root documentation stays with the source.
- `skills/<name>/SKILL.md` mounts read-only at `~/.claude/skills/<name>/`.
- `bin/<name>` mounts read-only at `/usr/local/bin/<name>`, already on the guest `PATH`.
- `hooks/` supplies scripts at the read-only package path and `~/.claude/hooks/`.
- `.claude/settings.json` and `.codex/hooks.json` supply native hook configuration.
- Root `CLAUDE.md` supplies a manual; the renderer combines selected manuals once at `~/.claude/CLAUDE.md`.

Each turn mounts the skills, hooks and `bin/` tools read-only from the
installed package.
Codex reads selected hooks through `/etc/codex/hooks.json`, enabled by
`/etc/codex/config.toml`. Its existing harness policy hooks stay in `$CODEX_HOME`.
Hook commands and manual imports can use `/opt/arizuko/packages/<name>/`.
[runed's mount table](runed/README.md#mounts) lists the complete guest projection.

A local `.disabled` marker suppresses a packaged skill mount.

### Install command

The optional `install` string in `arizuko.yml` runs through `sh` from the source
directory on each `package add` and `package sync`. It runs on the host as the
operator who runs the command; the installer adds no privilege.
`PREFIX` names the final package directory, `<data>/packages/<name>/`.
The build never relocates it. The guest sees the same directory at
`/opt/arizuko/packages/<name>/`.
The command must create `$PREFIX/bin/` for executables. Each `bin/*` file
appears at `/usr/local/bin/` in the guest. A Cargo build can use
`cargo install --path . --root "$PREFIX"`. Agent turns run no host installer.

The common installer replaces `arizuko.yml`, `CLAUDE.md`, `skills/`, `hooks/`,
`bin/`, `.claude/` and `.codex/` in the current package directory.
Keep writable custom skills and runbooks in the group home.

### Services, resources and environment

A service package supplies `<name>.yml`, a Docker compose fragment, beside its
`arizuko.yml`. Docker owns compose syntax; Arizuko owns the manifest syntax.
`package add` installs the fragment in `services/` and applies the package rows.
The existing `installed_packages` resource records source, resolved revision,
scope, owned row keys and service hashes. Updates replace owned rows;
removal withdraws those rows and leaves unrelated operator rows alone.
Edited service fragments refuse replacement. Unselected payload directories stay on disk.
The [package dashboard](dashd/README.md) exposes installed status.

Only a package with a service fragment provisions shared instance `.env` values:
`generate: true` mints once, `default` supplies a compose default, and
`required: true` refuses an unset value. Environment requirements in a
skills-only package are metadata; provide scoped credentials through
[group secrets](SECURITY.md), not shared `.env`.
The existing resource engine refuses payloads it cannot reconstruct, including
secrets, route tokens, invites, onboarding and installed-package records.

## Package files in the group folder

The `group/` directory of a package holds the files that the package puts in
the group folder, for example `PERSONA.md`. A file that the operator or the
agent must edit is not a `group/` file, because `package sync` rewrites it.
Ship such a file outside `group/`, for example in `facts/` beside `group/`, and
tell the operator to copy it once.

Each `package sync` writes every `group/` file of the winning package into the
group folder. The write replaces any edit that the agent made to that file.
A file that a package stops shipping is removed. A file that no package ships
stays the file of the group.

The settings of the turn carry a deny rule `Edit(~/<path>)` for each package
file. The rule tells the agent not to edit the file. It is advisory and is not
a security boundary.

If a file already exists before any package installed it, the install refuses
that file unless the operator passes `-f`.

`mcpServers`, `tasks.toml` and package migrations refuse installation because
no package reader executes them. Configure connectors and scheduled tasks
through their owning resources.
[The CUZK package](ant/examples/cuzk/README.md) provides a complete group example.

`runed/packages_test.go` covers native assets and read-only mounts.

## Adding a channel adapter

A channel adapter is a standalone HTTP daemon. It connects a chat platform to
`routd` through `chanlib`. It registers a JID prefix, serves inbound webhooks
or polls the platform, and exposes the outbound verbs (`/send`, `/like`,
`/edit` and so on) and `/health`. The latest reference is [`slakd/`](slakd/),
with the Slack Events API, an HMAC signing secret and dislike through
`reactions.add`. Follow these steps:

1. Create `<name>d/` with a `main.go` that wires `chanlib.Run`, a `bot.go` for
   the platform I/O, and a parser and formatter for the JID.
2. Add `template/services/<name>d/<name>d.yml`. It is a partial compose file
   that defines one service. Declare package metadata and any `proxyd_routes`
   in `template/services/<name>d/arizuko.yml`. See the
   section on proxyd routes below. You need no edit of `compose.go`.
3. Implement only the verbs that the platform supports natively. Return
   `chanlib.Unsupported(tool, platform, hint)` for the others, and advertise
   exactly the verbs that you implement.
4. Add a row to the verb matrix above.

Every adapter keeps this delivery contract, whatever its platform:

- **Claim before you post.** Every outbound message carries `idempotency_key`.
  Claim the key durably before the platform call, and record the platform id
  after it. Answer a settled key with the recorded id. Refuse an unsettled key
  and do not post it again, because a lost response and a lost request look the
  same. `chanlib` does this in `deliverOnce`. A TypeScript adapter does it
  itself.
- **Pick the error class.** Return `ErrNotDelivered` only when the platform
  provably accepted nothing, such as after a 429. Every other failure is
  ambiguous and keeps the claim ([`chanlib/README.md`](chanlib/README.md)
  § Four error classes decide retry and the claim).
- **The dashboard route is the registration.** Declare `/dash/<name>/` in
  `template/services/<name>/arizuko.yml`, gated by the adapter's credential.
  The route and the tile on the hub then exist exactly when the adapter runs.

`chanlib.InboundMsg` carries no forward attribution. A message that a user
forwarded from elsewhere arrives as plain content, and no adapter records where
it came from. `forwarded_from` on a stored row is something else. It is the
return address of a delegation. See
[ROUTING.md](ROUTING.md#reply-routing).

### Showing progress and placing threads

`routd` shows a running turn through capabilities in the registration of
the adapter. It sends a channel nothing that the adapter did not advertise.
Advertise only what you implement. `chanlib.CapImplReport` checks every verb in
the map except `typing`.

- `typing` commits `POST /typing {chat_jid, on}` to show presence while a turn
  runs. `on` arrives at dispatch, and `off` arrives when the turn ends and not
  at the first reply. The indicator therefore outlives a `reply` in the middle
  of the turn. `off` can fail to arrive, so a native indicator goes through
  `chanlib.TypingRefresher`, which stops by itself at
  `chanlib.DefaultTypingMaxTTL`. A platform with no indicator uses
  `chanlib.AckMark`. You supply the add call and the remove call of the
  reaction. `AckMark` keeps the mark in the adapter store and removes a stale
  mark at boot. The reaction must carry no social meaning. Use `👀` and never a
  like or an upvote. `Typing` returns nothing, because a failed indicator never
  fails the turn.
- `edit` commits `POST /edit` to replace the text of a message that this
  adapter sent. The id that its `/send` returned names the message. Any other
  id is `chanlib.ErrInvalidRequest`, and an adapter never edits the message of
  another party. The optional `thread_id` gives edit context where the
  platform needs it. Progress edits change one `⏳` message. At the end of the
  turn the message becomes `✓` or `✗`, unless the channel also advertises
  `delete`: then `routd` deletes the message and edits it only when the delete
  is refused.
- `thread_id` places text, files and voice in an existing thread.
- `thread_root` creates or reuses a thread rooted at a message before it sends
  text, files or voice. A refused thread destination returns an error. The
  adapter never sends the child at the parent instead.
- With `edit` and compatible thread placement, `routd` posts an immutable `🤖`
  summary opener and keeps progress and answers in its thread. An existing
  thread wins. Without new-thread support, `routd` keeps one editable progress
  card.
- With neither `typing` nor `edit`, `routd` sends the channel no presence and
  no progress message. That is the right choice for a platform where a second
  post is a public statement.

### Offering pickable answers with the buttons capability

`SendRequest.Options []Option{label, data}` carries the answers that a reader
can pick without typing. It is a render capability on `send_text` and not a
verb. It has no `/send-options` route and no entry in the verb matrix.

Declare `"buttons": true` in your caps map only if the platform draws buttons
natively. Then read `req.Options` in `Send` and attach the widget of the
platform. **Set the callback payload of each button to `Option.Send()` as it
is.** That string is the command that the press stands for. Map the press back
to an ordinary `InboundMsg` whose `Content` is exactly that payload, from the
platform id of the person who pressed. An adapter never interprets an option
and never authorizes one. A press and a typed command are then the same
inbound text, and they meet the same gate. This rule keeps a button from
becoming a second path of authorization.

If you declare nothing, you are done. `chanreg.HTTPChannel.SendOptionsCtx`
folds the answers into text that a reader can type, before the request leaves
`routd`. An adapter without the capability never sees an options field, and its
readers still get a prompt that they can answer. That fold is the one fold.
Never write it again in an adapter.

The platform limits, checked against the reference of each vendor:

- Discord draws five buttons in a row. Slack allows 25 in an actions block.
  Both adapters drop the rest and do not fail the send.
- Telegram limits `callback_data` to 64 bytes. Discord limits `custom_id` to
  100 bytes.
- Telegram allows eight buttons in a row. All three adapters draw the answers
  side by side, because a verdict is a choice between them, and a stack reads as
  separate actions.

**Reactions need nothing from you.** `routd` reads a green or red reaction on a
held notice as its verdict. It uses the `Reaction` and `ReplyTo` that your
adapter already delivers. A platform with no button surface can answer a prompt
this way. Report the target of a reaction as the other adapters do. The lookup
takes the last path segment, so both `<id>` and `<chat-jid>/<id>` match. A
reaction counts only on the notice itself, in its chat, while the call is still
held. Anywhere else it is an ordinary reaction. 👍 and 👎 never count. They are
social reactions, and reading one as an approval would record an approval that
nobody gave, see `chanlib.VerdictEmoji`.

Generic HTTP webhook ingest uses the same path. Any caller can POST
`/v1/messages` with the envelope of the channel protocol. Route inbound
webhooks through proxyd with the routes file of the receiving daemon. No
separate webhook adapter exists.

## Checking a component against the contract

You can check any daemon against the component contract by probing it while it
runs. This holds for your own daemon and for a daemon from outside this
repository:

```bash
arizuko probe http://localhost:8081
arizuko probe https://host --token <bearer> --foreign-folder <someone-elses>
```

The probe verifies these points:

- `/openapi.json` answers without credentials. It must be mounted before
  authentication, or nothing can discover the daemon.
- The document is OpenAPI 3.x.
- The daemon serves every path that the document advertises.
- Non-public paths refuse an unauthenticated request.
- With the two flags, the daemon binds a folder parameter to the caller and
  does not honor the value as given.

An empty document passes. A component that owns no `/v1` resource, such as
dashd, which serves only `/dash/` pages, advertises nothing, and that is
correct. The probe cannot see the opposite drift, a resource that is mounted
but not advertised. Proxyd answers every path from a `/` catch-all, so probing
cannot establish that direction. In this repository a Go test covers it, see `resregtest.AssertServesNoForeignResource`.

## Adding a proxyd route

The route table of `proxyd` comes from two sources. The first is the
`proxyd_routes` block of the `packages/<name>/arizuko.yml` of each package, which
`generate` reads at compose-generate time. The second is a static slice of core
routes in `compose/compose.go`, see `coreProxydRoutes`. The slice holds the
surfaces of webd, onbod, davd and dashd, and the `/dash/<daemon>/` page of
routd, runed, authd, proxyd, onbod, timed and webd. Routes cannot live in the
compose fragment, because `proxyd` is their consumer and Docker is not. A new
inbound web path is one manifest block and needs no Go edit:

```yaml
proxyd_routes:
  - path: /<prefix>/
    backend: http://<name>d:8080
    auth: public
    gated_by: <ENV_VAR>
    preserve_headers: [X-Webhook-Sig]
```

[`proxyd/README.md`](proxyd/README.md#route-rows) explains each field, the four
`auth` values and the rules for the owner. `compose.go` evaluates `gated_by`
against the `.env` of the operator and drops the disabled routes. `arizuko
generate` writes the remaining routes into `proxyd_routes` as rows that
arizuko owns. A route that a later release adds therefore reaches an existing
instance.

The dashboard of a daemon is one more route, `/dash/<name>/` to its own
`:8080`. The route is the registration. The hub of `dashd` links a tile exactly
when that row exists, and nothing discovers a dashboard by itself.
Longest-prefix matching puts every `/dash/<name>/` ahead of the `/dash/`
catch-all that reaches `dashd`. A packaged daemon declares its dashboard route
in its own manifest, for example `template/services/slakd/arizuko.yml`. A daemon with
no route has a tile that does not link.

`package sync` and compose generation read the same `arizuko.yml` through the strict
`resreg` decoder. `package sync` applies resource rows; compose reads routes and service
environment requirements before daemons start. These are two readers of one declaration.

## Adding an MCP connector

Connectors are global, and the operator defines them. Every `[[connector]]`
block in `<data_dir>/connectors.toml` registers at the level of the deployment.
A group reaches the tools of a connector through grant rows and never by
registering an MCP endpoint of its own. A tool is named
`<connector>_<tool>`. A group needs `mcp:<connector>_<tool>` or `mcp:*`.

An action glob such as `mcp:github:*` covers nothing. An action matches
exactly, or as `*`, or as `mcp:*` for every `mcp:` action, or as `admin`, see `actionCovers` in `auth/authorize.go`. `connectors.toml` is the one source.
No `mcp_connectors` resource exists, so a change to a connector is an edit of
the file and a restart of `routd`. `routd` reads the file once at boot, and the
file is the destination allowlist.

A block with `transport = "mcp"` declares one HTTPS endpoint. It has a `url`,
the `secrets` that the broker may resolve for it, and a `headers` map. The
broker renders the `{secret:KEY}` placeholders of the map on each call. `routd`
discovers the tools of the server for each folder, with the credential of that
folder. It names them `<connector>_<remote_tool>` and registers each through
the broker chain. A call resolves the declared secrets, renders the headers,
proxies `tools/call` and scrubs every resolved value out of the result. The
loader refuses any other table name or transport value. A misspelled block
therefore stops `routd` at boot. A block
without `transport` is `mcp`, see `connectorTransport` in `routd/ext.go`. Copies
for operators live in `template/connectors/`.

The two transports share one block and stay mechanically distinct. An `mcp`
block reads `headers` only and ignores `query`, `body` and `tool`. A `rest`
block reads all four. A field is absent for a transport and never has a second
meaning, because a field that means two things in two transports is the failure
that this shape prevents. The grant namespaces also stay separate. The tools of
an `mcp` connector authorize on `mcp:`. The tools of a `rest` connector add
their `ext:` scope. No subprocess transport exists. An endpoint needs no
runtime, and the image of `routd` has none.

Boot parses and validates the file and dials nothing, because the credential
belongs to a folder and boot has no folder. A `url` that is not `https://` is
refused at load, except `localhost` and `127.0.0.1`, because the credential
rides in a header. Discovery runs for each folder with the credential of that
folder. A connector that cannot be reached or authenticated registers no tools
and logs an error. It never fails the turn, because one unreachable third party
must not cost the agent every other tool.

```toml
[[connector]]
name      = "github"
transport = "mcp"
url       = "https://api.githubcopilot.com/mcp/"
secrets   = ["GITHUB_TOKEN"]

[connector.headers]
Authorization = "Bearer {secret:GITHUB_TOKEN}"
```

- `secrets` names the broker keys that this connector may see. It never sees
  the other secrets of the folder. Resolution tries `user(caller.Sub)` first and
  then `folder(caller.Folder)`.
- The credential is a request header and never a process environment variable.
  No subprocess exists.
- Scrubbing replaces every non-empty resolved secret value with `«redacted»` in
  the `mcp.CallToolResult` before the agent sees it.
- `CONNECTOR_CALL_TIMEOUT_MS` caps each call. The default is 30 seconds.

A folder secret and a daemon environment variable can have the same name and
hold different values. `GH_TOKEN` in the `.env` of the instance belongs to a
daemon. `GH_TOKEN` in the `secrets` rows of a folder is the value that the
broker renders into the request of the connector. Identify a credential by the
door that uses it and not by the first place where its name appears.

The operator writes the token of a folder with `arizuko secret -i <inst> set
<folder> KEY --value V`. The operator writes the token of a user with `arizuko
user-secret -i <inst> set <sub> KEY --value V`, or gives the user the page
`/dash/me/secrets`.

## Adding a REST connector

Some HTTP APIs have no MCP server of their own. Examples are Cloudflare,
Porkbun, Gandi and Namecheap. Such an API plugs in as the same `[[connector]]`
block with `transport = "rest"` and one `[[connector.tool]]` for each endpoint.
`LoadExtProviders` in `routd/ext.go` reads the blocks at boot. Each tool
becomes one REST-backed MCP tool, see `ExtTool` in `ipc/extcall.go`, that maps a tool call to one outbound HTTP request. The block
below holds the first two tools of the shipped
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

- **Three credential sinks share one vocabulary.** `headers`, `query` and `body`
  each carry `{secret:KEY}` placeholders. The broker renders them on each call
  against the resolved values, and the rows of the user win over the rows of the
  folder. A placeholder that resolves to nothing is dropped and not sent blank.
  The broker scrubs every resolved value from the response before the agent
  sees it.
- **Path placeholders are arguments.** `{zone_id}` in `path` becomes a required
  string argument of the tool without a `param` block. A
  `[[connector.tool.param]]` adds a typed body or query argument.
- **The scope is a grant.** The `scope` of each tool, in the form `ext:<service>:<operation>`,
  is an action that the folder must hold for the tool to register. The folder
  needs the exact `ext:cloudflare:dns:write` or `*`. `ext:cloudflare:*` covers
  nothing. The call itself is gated as `mcp:<connector>_<tool>`, so the folder
  needs that action or `mcp:*` as well. The REST tool loop in `ipc/ipc.go`
  does this.
- **Built-in providers can be replaced.** `routd/extproviders/*.toml` ships with
  `routd`. An operator block with the same `<connector>_<tool>` name replaces
  the built-in and logs the replacement. A single file that declares one name
  twice is refused.

The rule for folder secrets from the MCP connector section applies unchanged.
The token comes from the `secrets` row of the folder, and a daemon environment
variable with the same name is a different value.

<a id="add-an-oauth-provider"></a>

## Add an OAuth provider

Some APIs need an OAuth "Connect" flow, in which the user authorizes arizuko to
act as the user. For these APIs, add a surrogate OAuth provider. It is pure
config. You write no Go and rebuild nothing.

1. **Drop a descriptor** at `<datadir>/surrogate/<name>.toml`. To change a
   built-in provider, shadow its name with a file of the same name.

   ```toml
   auth_url       = "https://slack.com/oauth/v2/authorize"
   token_url      = "https://slack.com/api/oauth.v2.access"
   revoke_url     = "https://slack.com/api/auth.revoke"
   scopes         = ["chat:write", "channels:read"]
   secret_key     = "SLACK_TOKEN"        # key in the secrets table that receives the token
   allowed_domain = "slack.com"
   access_type    = ""                   # "offline" for providers that gate refresh_tokens (google)
   ```

   `allowed_domain` is documentation only. No code enforces it, so it bounds
   nothing. The API host of the provider still needs its own `network_rules`
   row for any agent shell that calls it.

   `auth_url`, `token_url` and `secret_key` are required. A missing field is a
   hard boot error that names the file. Two providers that share one
   `secret_key` are also a boot error, because the flow upserts by that key
   alone, and the second `Connect` would destroy the credential of the first. If
   you shadow the name of a built-in provider and change its `auth_url` or
   `token_url`, `routd` logs a warning. Credentials bind by name, so the
   `client_secret` of the operator for the built-in provider goes to the
   endpoint that your file names.

2. **Register the redirect URI** in the app console of the provider:
   `<WEB_HOST>/dash/me/connections/<name>/callback`.

3. **Set the client credentials of the operator** in the `.env` of the instance:
   `SURROGATE_<NAME>_CLIENT_ID` and `SURROGATE_<NAME>_CLIENT_SECRET`. Write NAME
   in upper case and replace `-` with `_`.

4. **Restart** the instance. `Connect <name>` appears in `/dash/me/connections`.
   The flow writes the access token and the refresh token to the `secrets`
   table under `secret_key`, and the broker refreshes the token near expiry.

The token then works like any other secret. Reference `secret_key` from a
`[[connector]]` block with `transport = "rest"` or `"mcp"` to give agents tools
that the OAuth grant of the user backs. `github` and `google` ship as built-in
providers. Connecting grants nothing. The agent still needs the grant of the
connector.

The flow has these limits:

- One row exists for each user and `secret_key`. A second account at one
  provider replaces the first.
- Only confidential clients work. Every flow carries PKCE and presents the
  client secret of the operator.
- The broker refreshes only at tool-call time. A provider whose `secret_key` is
  one of the five model keys reaches the guest at spawn as stored, with no
  refresh.

## Adding a chat-SDK page

A page from a third party talks to a chat link through the embedded JS SDK at
`/assets/arizuko-client.js`. The SDK wraps the `POST` and SSE round trip. The
page calls `Arizuko.connect(token)` and streams frames.

- The SDK source is `webd/assets/arizuko-client.js`, embedded in `webd`.
- The walkthrough
  [howto/chat-sdk.html](template/web/pub/arizuko/howto/chat-sdk.html) carries
  the sample page. Copy the sample to `<data-dir>/web/pub/<app>/index.html` and
  replace the token.
- [`webd/README.md`](webd/README.md#the-browser-sdk) covers caching, CORS and
  the `/config` discovery field.

For pages that an agent writes, the slot `~/public_html/` is bind-mounted from
`<data>/web/pub/<folder>/` and serves at `/pub/<folder>/`. For a deployment with
several apps, nest each app in its own subdirectory:
`~/public_html/<app>/` serves at `/pub/<folder>/<app>/`. Content that needs a
JWT lives in `~/private_html/` or `~/private_html/<app>/`, and it serves at
`/priv/<folder>/` or `/priv/<folder>/<app>/`. The redirect of the world host is
in [`proxyd/README.md`](proxyd/README.md#a-worlds-host-redirects-to-its-public-slot).
The rules of `web_routes` are in the section "Adding web routes" above.

## Extending the public site

The public documentation site at `/pub/` is plain HTML. `arizuko create`
copies it unchanged from `template/web/pub/` into `<data-dir>/web/pub/` of the
instance. No build step exists, so you edit the HTML directly.
`template/web/CLAUDE.md` holds the conventions for each page (breadcrumbs, the
prose container, `hub.css` and `hub.js`) and the layout of the site (products,
components, reference, howto and security). The page that states the position
for operators is `template/web/pub/arizuko/security/index.html`.

## Inspect tools

The inspect tools are a family of read-only MCP tools, registered in
`ipc/inspect.go`: `inspect_messages`, `inspect_routing`, `inspect_tasks`,
`inspect_session` and `inspect_identity`. They delegate to `store.*` accessors
and perform no destructive operation. A tool that changes state belongs in its
own family. A `/root`-elevated turn, which is `auth.Identity.IsRoot`, sees every folder.
An ordinary turn sees only the subtree of its own folder. To extend the family,
add a handler to `registerInspect` and wire a function into `ipc.StoreFns`.

`find_messages` searches by content. It is registered in `ipc/ipc.go` and
backed by `StoreFns.FindMessages` over the in-memory BM25 index of `routd`, see `routd/msgsearch`. It accepts terms that must all appear, `"phrases"` and
`-exclusions`. It ranks by bm25, returns a snippet marked with « and », and
applies the ACL gate `JIDRoutedToFolder` after the fetch. `routd` derives the
index from the `messages` entity. It walks the store into the index at boot and
feeds the index from its own writes. A search made while the walk is running
is refused and is not answered empty. Use `find_messages` to find a message by
what was said. Use `inspect_messages` to walk recent history by chat.
[`ipc/README.md`](ipc/README.md) § Tool surface explains why the family is a
set of tools and not `Bash`, and why there is no `inspect_logs`.

## Adapter `/health` contract

Each adapter exposes `GET /health` and returns `{ok, name, status, caps}`.
`chanlib.NewAdapterMux` is the canonical implementation. See
[`chanlib/README.md`](chanlib/README.md).

## Skills

Packaged skills follow the selection and paths in [Installing
packages](#installing-packages). A group can also keep writable custom skills
under `groups/<folder>/.claude/skills/`. A `.disabled` marker inside a skill
directory suppresses the packaged mount for that name. Skills provide
instructions; tool authority still comes from grants.

A skill has this layout:

```
<name>/
  SKILL.md              # required: prompt injection
  CLAUDE.md             # optional
  references/           # optional supporting files
```

A skill directory name must match `^[a-z0-9-]+$`. `SKILL.md` carries
YAML frontmatter with `name` and `description`. The model reads the description
to decide whether the skill applies. The description therefore names the
triggers in prose and not in a separate field.

## Session opening ritual

When a new Claude Code session starts, `routd` injects a
`<system event="new_session">` directive into the prompt of the turn. The agent
already reads the `CLAUDE.md` of its group at the start of a session. To
enforce an opening ritual, such as loading context, reading a plan file or
scanning skills, add a `## Session opening` section to the `CLAUDE.md` of the
group:

```markdown
## Session opening

Before your first reply:

1. Read `~/plans/<topic>.md` if it exists (the durable plan-of-record).
2. Scan `~/.claude/skills/` for skills relevant to the current goal.
3. Read `~/facts/` entries relevant to the user's question.
```

The `new_session` system event is the reliable hook, and it fires exactly once
for each new session. The instructions run as a directive at the session
boundary and are not buried in the middle of a file.

## Host-tool capabilities

Some integrations are pure host-tool surfaces. They need no daemon, no MCP and
no protocol. The operator installs a CLI in the agent image, or mounts host
state into it, and ships a skill that drives the CLI as a subprocess. The agent
sees an ordinary command on `PATH`, and the skill is how the agent discovers
it. Authentication comes from a host-side mount or, for a model key, from the
host `.env` or the own `/dash/me/env` row of the user. A capability credential
cannot back such a tool, because it never reaches the environment of the guest.
Host tools differ from MCP tools, which are in-band, typed by schema and
mediated by `routd`. They also differ from channel adapters, which are
out-of-band and use HTTP.

One capability ships today:

| Capability | Binary  | Skill                        | Auth                                                                                                                                  |
| ---------- | ------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `oracle`   | `codex` | `ant/skills/oracle/SKILL.md` | `HOST_CODEX_DIR` mount on the agent guest **OR** `CODEX_API_KEY` / `OPENAI_API_KEY` from the host `.env` or the user's `/dash/me/env` |

To add one:

1. Install the binary in `ant/Dockerfile`. Pin the version where upstream
   supports it.
2. If the tool needs host-side credentials or state, add a `HOST_*_DIR`
   environment variable on `core.Config`. Plumb it through `compose.Generate`
   and the turn renderer, so that it is bind-mounted into spawns. The pattern is
   `HOST_CODEX_DIR`, which has a layered mount at `/home/node/.codex` through
   `runed/mounts.go`.
3. Write `ant/skills/<name>/SKILL.md`. It needs a sharp frontmatter
   `description`, because `/dispatch` matches on it. It also needs a "when to
   invoke" section, invocations that can be copied and pasted, and a fallback
   for missing authentication that fails soft and does not crash the turn.
4. Rebuild the agent image for bundled tools, or install a source package for
   selected group tools.

`ant/skills/oracle/SKILL.md` is the reference for the shape of the skill body.

## Swapping the agent harness

ant wraps an agentic harness and is never one. The extension point is the
process boundary and not an interface inside ant. `routd` serves the MCP
socket of each turn in-process, in `ServeTurnMCP` in `routd/mcp.go`. ant is
only a client of it, through `socat STDIO UNIX-CONNECT:/run/ipc/gated.sock`
in `ant/src/mcp-servers.ts`. A different harness replaces ant completely when it
speaks that protocol as its own process. It publishes its agent tools through
`tools/list`, and it reports the turn with the `submit_turn` method. No arizuko
code changes, and any language works.

That process must do three things. It reads the JSON turn input on stdin. It
runs the turn against the socket. It delivers through an explicit delivery tool
(`reply`, `send`, `send_file` or `send_voice`) and calls `submit_turn`. Prose is
never a reply. A nonempty `nudge` in the `submit_turn` response continues the
same turn ID and session, and the harness then submits again. The hidden
`check_turn` method lets a Stop hook ask the same decision early. The broker
hands out one nudge across both readers. The
harness handles steering in the middle of a turn in its own way. ant drains
`/run/ipc/input` between tool calls (`pollIpcDuringQuery` in
`ant/src/index.ts`), and another harness maps it to whatever its protocol
offers.

A full replacement is not the only option. ant ships a harness chassis. The
runtime half is reused: it parses stdin, steers by IPC, runs the `<status>`
pipeline and makes `submit_turn` idempotent. The harness half is one interface and one file for each backend. The
interface takes a `Turn`, returns `Frame`s, and has one `run()` and a capability
profile. The files are `ant/src/claude.ts`, the default, `codex.ts` and
`pi.ts`. Select a backend with `ARIZUKO_BACKEND=codex` in the `.env` of
the instance. ant refuses `pi` until it has a delivery-tool bridge. The CLI of the backend must be baked into the agent image. The
`HarnessProfile` of each backend in `ant/src/backend.ts` says what it can and
cannot do with MCP tools, skills and the skill guard. The profile states the
differences truthfully and does not claim parity.
See [`ant/README.md`](ant/README.md) § The harness.

## Permissions

Capability comes from `acl` rows and never from the place of a folder in the
tree. A path is a routing target, a JID prefix, a guest home and a web vhost.
Depth means nothing to authorization.

Create binds every folder to `role:member`, which carries the messaging verbs.
Everything beyond that is an explicit row that someone delegated. That covers
`register_group`, `routes`, `network_*`, `schedule_*`, `observe_*`, `invite_*`,
the minting of tokens and `acl`. `role:operator` holds `*` on `**` WITH GRANT
OPTION and is the root of every chain. A principal may delegate onward only the
rows that it holds, and only those that it holds WITH GRANT OPTION, so
authority strictly decreases, see `auth.Delegate`. Root is a grant that the
operator invokes with `/root`, not a folder.

A world gets two rows instead, see `writeWorld` in `onbod/main.go`: `mcp:*` and
`hold:mcp:*`, both scoped to its own subtree. The world may reach any tool
inside itself, and a human answers for every call it makes. Approval is what a
person controls there, and the grant is not. `role:member` carries the other
half: a skip-approval row, which is `hold:mcp:<tool>` at `deny`, for each messaging verb
and for the reads that every turn mounts. The agent therefore still answers a
message and reads its own notes without asking anyone. `store.SeedRoutdFloor`
writes both halves. The test `TestWorld_HoldsOnlyWhatAPersonShouldSee` in
`routd` fails if a new tool lands on the wrong side of that line.

To gate a new action, take these steps:

1. Pick an action name. Use `mcp:<tool>` for an MCP tool.
2. Bind the action at registration through the injected `Gate`. The Gate is
   `auth.Authorize` for the operator REST interface, and
   `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` on the
   agent socket.
3. Seed a row if the action should be on by default.

Never add a second check inside the handler. The scope glob of a grant is its
containment. For example, `mcp:register_group` scoped `acme/**` registers under
`acme` and nowhere else. The agent gate reads two axes. The folder of the turn
confers, and the attested sender adds inside the subtree of that folder. See
[`auth/README.md`](auth/README.md#authorization-is-one-row-and-one-question).
A new action is therefore grantable to a folder, to a person or to both.

`escalate_group` sends a message to the parent and grants no permission.
[`auth/README.md`](auth/README.md) and [GRANTS.md](GRANTS.md) describe the
model, the row and the evaluator. The page `reference/grants.html` describes
the grammar of `params`.
