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
# mint the injecting bearer (operator, on the instance host):
arizuko token -i throwaway issue bearer eval --scope messages:write,messages:read,cost:read
./anteval run https://<throwaway-host> \
  --api   http://localhost:8081 \ # routd /v1 base reachable from the eval host
  --token "$AGENTEVAL_TOKEN" \    # bearer minted above, for the eval folder
  --chat  web:eval \              # chat JID tasks are injected into (folder must own it)
  --folder eval \                # folder owning --chat; anchors the root-session reset
  --sink-addr :9099 \            # local bind for the callback sink (routable iface)
  --sink  http://<host>:9099 \     # URL the agent guests call back to; a KVM guest cannot reach a docker-gateway address (BUGS L379)
  --smoke                         # gate subset (9 cases); omit for all 23
# selectors: --dimension web | --case pub-200 ; output: --md report.md --json report.json
./anteval dash report.json      # re-render a saved report
make validate                     # load+validate the case catalog (no target)
```

Live preconditions: the eval folder exists
(`arizuko group -i <inst> add web:eval eval`), it holds the grants the cases need,
its egress allows the sink, and the sink URL answers from an agent guest.
Mint the bearer with `--ttl 6h`: the default lifetime is one hour, and a full
catalog run is longer. The cases run one at a time on one chat: routd
dispatches one turn per folder, so after each case the driver waits until the
model gateway meters that case's turn before it injects the next. Without a
gateway there is no meter, and the wait runs to its bound.
The operator recipe, with the commands and the two traps that abort a run, is
`.claude/skills/eval/gates.md` § "Bringing `anteval` up on an instance that has
never run it".

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

**anteval is a manual, operator-invoked tool.** It needs a running throwaway
instance and a minted token, and it drives a real agent at real cost. Never wire it into CI,
a git hook, a cron, a scheduled task, or any `make test*`/`make smoke` gate —
`make validate` (catalog load + schema check, no target) is the only anteval
step that belongs in an automated pipeline.

**Session isolation** — why `--folder` is required: each case runs in its own
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

**Grant the eval folder the tools its cases need, or half the run measures your
grant table instead of the agent.** The `role:member` floor is messaging only
(send/reply/react); `register_group`, `issue_webhook`, `issue_chat_token`,
`add_acl` and `web_route_set` are all explicit delegations. On the 2026-08-08
live run four of eight smoke cases failed purely because the eval folder held
none of them — and the agent had correctly named the missing grant in chat
while the harness reported a bare `timeout: no callback`.

Each case now declares `requires = [...]`; a timeout quotes it back so the
reader can tell an ungranted tool from a failed agent. To grant them:

```bash
for a in mcp:register_group mcp:delegate_group mcp:issue_webhook \
         mcp:issue_chat_token mcp:add_acl mcp:web_route_set; do
  arizuko group -i <inst> grant folder:eval 'eval/**' "$a"
done
```

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
- `mcp_roundtrip` — same over the MCP face (`--mcp`).
- `parity_sentinel` — `{nonce}` is identical via REST and MCP (uniform surface).

The vocabulary is closed: a case with any other `kind` fails to load
(`pkg/spec/spec.go`). One case tests one capability with one checker and
scores pass or fail. A case that needs negation or diagnosis hands it to the
agent: the prompt asks the agent to call back only the marker that matches
what it found, so the harness compares a marker and never interprets.

The run mints a nonce per case and embeds it in every name/URL/body, so runs are
idempotent and never collide; teardown is best-effort.

## Wiring seam

`pkg/run/target.go` (`HTTPTarget`) is the only place that knows the surface
contract: it injects tasks via routd `POST /v1/messages` (ack `{ok,id}`) and
reads chats via `GET /v1/messages/inspect?jid=` (rows carry `content` +
`is_bot_message`). `--api` points at routd's reachable `/v1` base (the eval is
an operator-host tool, like the `eval` skill reaching localhost ports); the
target arg is proxyd's public base for `/pub` `/priv` `/chat` probes. The
injecting token needs `messages:write` + read scope on the eval folder, and the
`--chat` JID must be a real chat that folder owns.

`Cost()` reads routd `GET /v1/cost?turn_id=` (token needs `cost:read`); a 404
— no cost recorded yet, or a routd predating the endpoint — reads as 0, other
failures surface.

`--mcp` points at the platform's own MCP face. `McpMessages` speaks streamable
HTTP to it and calls `get_history`, which webd serves on `POST /mcp` and on
`POST /chat/{token}/mcp` from one builder, returning user frames as well as
assistant ones — which is what a sentinel needs. The tool reads by FOLDER and
TOPIC, so the reader takes the per-case topic (the run's nonce) and resolves the
folder from the chat JID through `groupfolder.JidFolder`; a suffixed JID names
no folder and is refused rather than read as some other group's history. Its
payload is a JSON string inside the MCP result envelope, so it is decoded twice,
and `role` is the bot flag REST spells `is_bot_message`. Without `--mcp` the
read answers "surface not configured" and an mcp or parity case fails loudly
instead of passing on nothing.

The callback sink binds locally; the agent guests must be able to reach
`--sink`, so the eval host has to be on the target folder's egress
allowlist (or run on an already-allowed host). A default-deny refusal there is a
deploy gap, not a capability failure.

## Continuity coverage — what is and is not measured live

The `continuity` dimension (`knowledge-outlives-transcript`) proves the
platform's group-knowledge layer carries a fact to a turn that has NO
conversation transcript (a `contextMode='isolated'` scheduled follow-up —
isolated means no `--resume` by definition). Run it by hand like any case:

```bash
./anteval run <target> --api ... --token ... --chat web:eval --folder eval \
  --case knowledge-outlives-transcript --sink-addr :9099 --sink http://<gw>:9099
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

`cmdRun` and `main` are not covered and cannot be here: `cmdRun` drives a LIVE
instance over HTTP (that is what the tool is), and `usage`/`fatal` are
`os.Exit`. The `pkg/` packages that do the real work — `spec`, `check`, `run`,
`report` — sit at 76–97%.
