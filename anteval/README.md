# anteval

Agent-capability eval — a black-box prober that certifies a live arizuko
agent can operate the platform itself. It is a shipped binary and not a
`go test`, because it exercises a running instance. The owner runs it by hand
only against a throwaway instance, never against a live instance.

Each case injects one real task through a public surface, lets the live agent
do it with its own MCP tools, and asserts an **externally observable effect** —
an HTTP status, a callback the agent's artifact made, or a message visible via
REST/MCP — never the agent's prose, never the instance's internal state. Zero
arizuko-internal imports: a black-box client over the same surfaces a human or
external tool uses, so it can ship alone.

The platform catalog covers self-modification, delegated children, web and
chat apps, REST/MCP parity, routing, egress, backend selection and continuity.
Its composite task asks a child agent to publish an app with a public chat
entrypoint. The privilege pair checks access through the public surface:
allowed access returns 200; denied access returns 404.

Operational health belongs to the `eval` skill. `create-eval` writes a project's
evaluation skill, and `make test-e2e` drives one in-tree round through webd.
Neither certifies a running model's platform capability.

## Run

```bash
make build
./anteval run --env /private/anteval.env \
  --sink http://eval-host.example:9099 --smoke \
  --md report.md --json report.json
# selectors: --dimension web | --case pub-200 ; output: --md report.md --json report.json
./anteval dash report.json      # re-render a saved report
make validate                     # load+validate the case catalog (no target)
```

Run on the instance host with `arizuko`, Docker, systemd and KVM available,
as root or through `sudo`. Under `sudo` the runner hands `SUDO_UID` to the CLI,
so git reads a `HOST_APP_DIR` checkout that the invoking user owns.
The private configuration file supplies existing instance keys:
`DYNAMODB_URL`, `STORE_CA`, `AWS_REGION`, `EXTENDDB_PASSWORD`, `HOST_APP_DIR`,
`RUNED_KERNEL_DIR`, `RUNED_ROOTFS_DIR`, `RUNED_GUEST_MEM`, `RUNED_VSOCK_CID_BASE`,
`RUNED_VSOCK_PORT_BASE`, `RUNED_TAP_SUBNET` and `RUNED_PROXY_PORT`.
Use an external test store and a separate vsock, tap and proxy allocation.
Supply `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` explicitly.
`ARIZUKO_DEFAULT_MODEL` and `ARIZUKO_BACKEND` are optional. The runner never reads another instance's
configuration or credentials.
`ARIZUKO_BACKEND=codex` instead needs `HOST_CODEX_DIR`, the absolute path of
a Codex login directory holding `auth.json`, such as the operator's `~/.codex`. That run installs no gateway: it runs direct,
allows `chatgpt.com` and `auth.openai.com` for the eval folder, and each case
waits out its settle bound because no meter reports the turn. A run whose
cases include one with a codex `harness` (below) also needs `HOST_CODEX_DIR`
and allows the same two hosts.

The sink hostname must reach this host from the guests. The runner binds its
declared port and allows that hostname through the eval folder's egress rules.
`--sink-addr` overrides the local bind. The public target uses the same hostname
and a separately allocated web port. IP literals are refused.

The runner generates `anteval-<unix-ms>` or uses the fresh name supplied by
`--target`. An existing data directory, systemd unit or Compose project is
refused before creation. `--product` selects a product for the eval group. It
takes a package source as `arizuko create --product` does: the absolute path of
the product tree, a `github.com/<owner>/<repo>` reference or a `.tgz`. A product
run is `--product <checkout>/packages/<name> --cases <checkout>/packages/<name>/eval`.
Before creation, the runner checks the KVM devices, kernel and rootfs artifacts,
and locally cached daemon, web and gateway images. The gateway image names come
from the litellm Compose fragment under `HOST_APP_DIR`. A run that installs no
gateway checks no gateway image.
The runner checks the declared CID, vsock ports, tap subnet and proxy bind
against host inventory. These checks are point-in-time; another process can
occupy a resource after the checks.
Each run uses a private data base under `ARIZUKO_DATA_DIR`, or `/srv/data`.
The CLI lifecycle creates the instance, enrolls its store account, starts its
services, installs the model gateway (not for Codex), and sets up the group,
grants and tokens.

```text
create → store enroll → run → package add → restart run
       → group / grants / network / tokens → cases → stop child → rm
```

The runner calls `arizuko rm -i NAME` whenever the creation left a data
directory: after setup failure, case failure, success and SIGINT or SIGTERM. Removal errors
fail the run and report the exact name and data base for a manual retry.
Reports stay outside the disposable instance. Cases run sequentially and wait
for gateway metering between turns.

`--smoke` selects the shorter suite; the full catalog certifies the selected
backend. [cases/anteval.toml](cases/anteval.toml) owns the case list and
`smoke` flags. A failing case spends its wall cap, so duplicate effects and
checks that also pass on total failure do not earn a case.

**anteval is a manual, operator-invoked tool.** It creates a throwaway
instance and drives a real agent at real cost. Never wire it into CI,
a git hook, a cron, a scheduled task, or any `make test*`/`make smoke` gate —
`make validate` (catalog load + schema check, no target) is the only anteval
step that belongs in an automated pipeline.

**Session isolation**: each case runs in its own
topic, and the first turn of any non-main topic forks the folder's MAIN
session (`ROUTING.md` § Topic routing). A root session left by prior runs or operator chat
would therefore sit in front of every case — the cross-case contamination the
per-case topics were built to remove, reintroduced through the fork (BUGS
L54 "Known consequence"). Before every case the driver reads
`GET /v1/topic_sessions?folder=<folder>&topic=` and, when a root session
exists, sends a bare `/new` to the chat and waits until the session is
observably gone; only then is the case injected (steer consumes `/new` only
as the newest pending message, so injecting early could leak it into a root
turn as literal text). It re-checks per case because a group-context
scheduled follow-up (the `state-persist` pattern) fires topic-less on the
ROOT topic and re-persists a session there mid-run. A root session that will
not clear aborts the run loudly — quiesce the eval chat (no concurrent
operator conversation, no stray group-context crons) and re-run. A sticky
`#topic` set on the eval chat also breaks isolation (it overrides every
case's topic); leave the eval chat unpinned.

Each case declares `requires = [...]`. Before cases start the runner grants
each `mcp:<tool>` entry to the eval folder and allows each `egress:<host>` entry
on its allowlist. A timeout includes the declared requirements.

A case may set `harness`, the group's harness document as JSON text (a JSON
object, for example `{"backend":"codex"}`). The runner writes it to the eval
folder through routd `PATCH /v1/groups/{folder}` before the case's first turn
and clears it after the case's turns, so a later case without one runs on the
instance defaults. A run with at least one such case mints the eval bearer with
the extra scope `routes:write:own_group`; any other run does not.
`backends/backends.toml` uses the field to answer on the default backend, on pi
and on codex on one instance: `--cases backends`, with `ARIZUKO_BACKEND=codex`
in the env file. It holds no claude case: a disposable run must not copy the
credential of an instance, and no Claude account exists that no live instance
uses.

Exit is non-zero if any case fails. `dash` renders a saved JSON report.

## One release, one isolated instance

`anteval run` uses the ordinary `arizuko` CLI lifecycle; anteval owns no second
Compose generator, startup path or removal implementation. The release image
supplies packages and manuals. `APP_SRC_DEV` stays off, and release kernel and
rootfs assets stay read-only. A run certifies that release, not a serving
instance's data.

Every CLI call supplies the fresh instance name and private data base.
Inherited instance settings cannot select a target. The external test store
serves no user instance; `store enroll` creates an isolated account owning all
eval tables and credentials. `rm` removes that account, never the store server.
Model credentials come from the run's explicit configuration, never another
instance. Missing credentials fail before creation and name the backend.

The runner sets `MAX_CONCURRENT_AGENTS=1`. Memory preflight reserves 1536 MiB
for services and gateway plus the configured guest. Each CID, vsock, tap and
proxy allocation must be separate from other instances.

Cleanup stops and reaps the foreground child before `arizuko rm`, with an
uncanceled cleanup context. A create failure with no data directory needs no
removal. Cleanup completes before process exit on all handled paths. A hard
crash can leave resources; use the reported instance name and data base with
`arizuko rm` before retrying. Removal never sweeps by name prefix.

Only lifecycle setup invokes the operator CLI. Cases, driver and checkers use
public HTTP, REST and MCP surfaces, with no daemon or storage imports and no
instance-file reads. [lifecycle tests](cmd/anteval/lifecycle_test.go) cover
existing-target refusal, unrelated-instance isolation, partial creation,
report failure, signals and cleanup with fake commands.

## Measured on a disposable instance

The acceptance record uses daemon image v0.75.28, Codex with an explicit
operator login, and CLI/anteval built from the source tree on 2026-11-25.
The isolated store used PostgreSQL 17 and ExtendDB 0.1.13. Each listed run left
no owned data directory, Compose project, services, guests, tap addresses,
volumes, networks, systemd unit, or store account, keys and tables.

| Path                                                        | Result                                              |
| ----------------------------------------------------------- | --------------------------------------------------- |
| Create failure before a data directory                      | Error; no removal needed.                           |
| Partial startup, interrupted by SIGINT                      | Canceled; cleanup completed.                        |
| Setup failure after account enrollment and healthy services | Error; cleanup completed.                           |
| Case failure                                                | 0/1; exit 1.                                        |
| `rest-roundtrip`                                            | 1/1; exit 0.                                        |
| `--cases backends`                                          | 3/3; exit 0; harness documents written and cleared. |
| Helpdesk product smoke                                      | 2/2; exit 0.                                        |
| Smoke                                                       | 6/9; 43 minutes; exit 1.                            |
| Full catalog                                                | 14/24; 112 minutes; exit 1.                         |
| Egress dimension after case corrections                     | 3/3; exit 0.                                        |

These runs used a host process to set newly staged `.codex`, `.claude/skills`
and `.claude/hooks` directories to uid 1000. The image under test lacked the
runed ownership fixes covered by `TestBuildMountsNewGroupHomeWritable` and
`TestStageTargetsInsideAHomeBelongToTheRunUID`. This evidence does not certify
a run without that assistance or establish a pass rate for the current image.

### Capability acceptance limits

The catalog record above does not certify the current case definitions.
The delegated web-child registration, unknown-addressee rule and public-slot
web-route cases have corrections covered by `routd/groups_github_test.go`,
`tests/resident/resident_test.go` and `proxyd/anteval_web_route_test.go`.
Those seven cases have no recorded rerun on a release image with the
corrections. `world-create-refusal-honest` remains an agent failure: a denied
world creation did not produce either required callback marker.

The accepted rules without observable checks stay listed once in
[BUGS L188](../BUGS.md). A static resident-file check establishes pointers and
required text, not live compliance with those rules.

## Products use the same runner

A product ships `eval/*.toml`. `--cases <dir>` loads each TOML file through the
same schema, checkers and driver as the platform catalog. Validate with
`anteval validate --cases <checkout>/packages/<name>/eval`; run with
`anteval run --product <checkout>/packages/<name> --cases <checkout>/packages/<name>/eval`
and the Run options above.

Platform cases certify platform capabilities. Product cases certify the
behavior its persona, rules and facts promise, usually through `rest_reply`.
The helpdesk suite at [packages/helpdesk/eval](../packages/helpdesk/eval/helpdesk.toml)
also checks that a product skill reaches its agent. Authoring instructions:
[build a product](../template/web/pub/arizuko/howto/build-a-product.html).

## How a case proves itself

Templates expand per run: `{nonce}` (unique per run+case), `{sink}`, `{target}`,
`{chat}`, and `{cb.KEY}` — a query param the agent handed back through the
callback sink (e.g. a freshly minted chat-link token). A prompt or check with
any other brace fails to load, because the runner cannot expand it. Checkers:

- `callback` — the agent wired an artifact (skill, MCP tool, app, webhook,
  child) that fires `{sink}/cb/{nonce}`; firing is the proof.
- `http_status` — a `{cb.url}`/`{cb.token}` URL's FIRST response carries the
  expected code (publish → 200, gated → 303-to-login, deleted/denied → 404);
  redirects are asserted, never followed. A page outside `/pub/` and `/priv/`
  answers 302 to `/pub/<path>`, so a case names a page in a web slot.
- `rest_reply` — a **bot-authored** message carrying `{nonce}` is readable in
  `{chat}` via REST (the user-injected prompt is excluded, so the marker the
  harness itself sent can't false-pass). `rest_observe` matches any author.
  A check that names no `chat` reads the chat of the run, and one that names
  no `text` looks for `{nonce}`.
- `mcp_roundtrip` — same over the MCP face.
- `parity_sentinel` — `{nonce}` is identical via REST and MCP (uniform surface).

The vocabulary is closed: a case with any other `kind` fails to load
(`pkg/spec/spec.go`). One case tests one capability with one checker and
scores pass or fail; reports aggregate the pass rate per dimension. A case that needs negation or diagnosis hands it to the
agent: the prompt asks the agent to call back only the marker that matches
what it found, so the harness compares a marker and never interprets.

The run mints a nonce per case and embeds it in every name/URL/body, so runs are
isolated. Instance removal is required for a successful exit.

## Wiring seam

`pkg/run/target.go` (`HTTPTarget`) is the only place that knows the surface
contract: it injects tasks via routd `POST /v1/messages` (ack `{ok,id}`) and
reads chats via `GET /v1/messages/inspect?jid=` (rows carry `content` +
`is_bot_message`). Setup supplies routd's local API address, proxyd's public
base and the `web:eval` chat. Setup mints the injecting token with
`messages:write`, `messages:read` and `cost:read`.

`Cost()` reads routd `GET /v1/cost?turn_id=` (token needs `cost:read`); a 404
— no cost recorded yet, or a routd predating the endpoint — reads as 0, other
failures surface.

Setup supplies a fresh chat token for the platform's MCP face. `McpMessages` speaks streamable
HTTP to it and calls `get_history`, which webd serves on `POST /mcp` and on
`POST /chat/{token}/mcp` from one builder, returning user frames as well as
assistant ones — which is what a sentinel needs. The tool reads by FOLDER and
TOPIC, so the reader takes the per-case topic and the folder supplied by setup. Its
payload is a JSON string inside the MCP result envelope, so it is decoded twice,
and `role` is the bot flag REST spells `is_bot_message`.

The callback sink binds locally; the agent guests must be able to reach
`--sink`, so the eval host has to be on the target folder's egress
allowlist (or run on an already-allowed host). A default-deny refusal there is a
deploy gap, not a capability failure.

## Continuity coverage — what is and is not measured live

The `continuity` dimension (`knowledge-outlives-transcript`) proves the
group diary, read on demand, carries a fact to a turn that has NO
conversation transcript (a `contextMode='isolated'` scheduled follow-up —
isolated means no `--resume` by definition). Run it by hand like any case:

```bash
./anteval run --env /private/anteval.env --sink http://eval-host.example:9099 \
  --case knowledge-outlives-transcript
```

Two continuity properties stay UNMEASURED live, deliberately:

- **Transcript resume across turns (layer 1).** A scheduled fire carries no
  topic (`routd/schedule.go`), so a group-context follow-up resumes the ROOT
  session while every case runs in its own topic; injecting a case into the
  root topic would contaminate the baseline the driver just established. The
  one-inject case model cannot express "learn in turn 1, recall in turn 2 of
  the same session".
- **The fork path (a topic born from a reply).** `Inject` posts
  `{chat_jid, topic, content, sender}` and deliberately does not set
  `reply_to_id`; adding it would be harness machinery for a single case.

Both are pinned at the integration tier instead:
`tests/feat_continuity_test.go` drives a real fork through ingest → poll →
dispatch and asserts the shipped session id passes ant's own resume guard
(pattern extracted from `ant/src/index.ts`, so the Go copy cannot drift),
the child transcript exists restamped, and the observed-cursor trade holds
on both axes.

## v1 limits

`max_wall_ms` and `max_tokens` each fail a case. The wall clock stops the
wait; a case that passed is failed when the gateway metered its turn over
`max_tokens` (`pkg/run/run.go`). `dash` renders markdown; a hosted HTML
dashboard would be a later `serve` split.

## Coverage

`anteval validate` and `anteval dash` are covered, including a sweep that loads
every product's shipped `eval/*.toml` on each test run — a suite that stops
parsing is a product whose behaviour nothing checks any more, silently, because
the harness finds zero cases and reports a clean run.

The run command uses CLI and process fakes to test setup, existing-instance
refusal, case failure, report failure, partial creation and SIGINT cleanup.
These tests create no real instance. They do not certify guest reachability or
live agent capabilities.
