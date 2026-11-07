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

Run on the instance host with `arizuko`, Docker, systemd and KVM available.
The private configuration file supplies existing instance keys:
`DYNAMODB_URL`, `STORE_CA`, `AWS_REGION`, `EXTENDDB_PASSWORD`, `HOST_APP_DIR`,
`RUNED_KERNEL_DIR`, `RUNED_ROOTFS_DIR`, `RUNED_VSOCK_CID_BASE`,
`RUNED_VSOCK_PORT_BASE`, `RUNED_TAP_SUBNET` and `RUNED_PROXY_PORT`.
Use an external test store and a separate vsock, tap and proxy allocation.
Supply `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` explicitly.
`ARIZUKO_DEFAULT_MODEL` is optional. The runner never reads another instance's
configuration or credentials.

The sink hostname must reach this host from the guests. The runner binds its
declared port and allows that hostname through the eval folder's egress rules.
`--sink-addr` overrides the local bind. The public target uses the same hostname
and a separately allocated web port. IP literals are refused.

The runner generates `anteval-<unix-ms>` or uses the fresh name supplied by
`--target`. An existing data directory, systemd unit or Compose project is
refused before creation. `--product` selects a product for the eval group.
Each run uses a private data base under `ARIZUKO_DATA_DIR`, or `/srv/data`.
The CLI lifecycle creates the instance, enrolls its store account, starts its
services, installs the model gateway, and sets up the group, grants and tokens.

```text
create → store enroll → run → package add → restart run
       → group / grants / network / tokens → cases → stop child → rm
```

The runner always calls `arizuko rm -i NAME` after an attempted creation,
including setup failure, case failure and SIGINT or SIGTERM. Removal errors
fail the run and report the exact name and data base for a manual retry.
Reports stay outside the disposable instance. Cases run sequentially and wait
for gateway metering between turns.

**Tiers** (2026-08-21; `cases/anteval.toml` notes in place the traps that
rule a case out): `--smoke` is the shorter manual suite —
9 cases, ~10 min measured on a healthy run (krons 2026-08-21: passing cases
22–156s, avg ~50s), bounded by the wall caps at ~21 min. The full catalog —
23 cases, ~30 min healthy, ≤~55 min cap-bounded per backend — runs when
the owner chooses to certify a backend (`ARIZUKO_BACKEND` = claude | codex | pi).
The suite's cost is failure-dominated: a failing case burns its whole
`max_wall_ms` (a misconfigured sink once made the catalog read ~4 min/case),
so a case that cannot pass is pure burn — the reduction cut ~30 min of cap
exposure per backend, of which three cases were guaranteed-fail on the
documented setup. A catalog too expensive to run certifies nothing; a case
earns its slot only by evidence no cheaper tier can produce.

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

Each case declares `requires = [...]`. The runner grants its MCP actions to
the eval folder before cases start. A timeout includes the declared requirements.

Exit is non-zero if any case fails. `dash` renders a saved JSON report.

## How a case proves itself

Templates expand per run: `{nonce}` (unique per run+case), `{sink}`, `{target}`,
`{chat}`, and `{cb.KEY}` — a query param the agent handed back through the
callback sink (e.g. a freshly minted chat-link token). Checkers:

- `callback` — the agent wired an artifact (skill, MCP tool, app, webhook,
  child) that fires `{sink}/cb/{nonce}`; firing is the proof.
- `http_status` — a `{cb.url}`/`{cb.token}` URL's FIRST response carries the
  expected code (publish → 200, gated → 303-to-login, deleted/denied → 404);
  redirects are asserted, never followed.
- `rest_reply` — a **bot-authored** message carrying `{nonce}` is readable in
  `{chat}` via REST (the user-injected prompt is excluded, so the marker the
  harness itself sent can't false-pass). `rest_observe` matches any author.
- `mcp_roundtrip` — same over the MCP face.
- `parity_sentinel` — `{nonce}` is identical via REST and MCP (uniform surface).

The vocabulary is closed: a case with any other `kind` fails to load
(`pkg/spec/spec.go`). One case tests one capability with one checker and
scores pass or fail. A case that needs negation or diagnosis hands it to the
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
  topic (`timed/split.go`), so a group-context follow-up resumes the ROOT
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
