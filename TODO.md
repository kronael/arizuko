# TODO

Active backlog. See `specs/index.md` for phase status, `CHANGELOG.md`
for shipped milestones.

## Now

- **Automate the version bumps, keep the pins (owner asked 2026-10-19).**
  Measured: 13 hand-pinned tool versions in `ant/Dockerfile` plus `KVER` and
  `FC_REF` in `kernel/Dockerfile`, and six base images. Every one moves by hand
  today. The owner wants latest everywhere automatically.

  The answer is a bot that MOVES the pin, not `latest` tags that remove it.
  `ant/Dockerfile:4` states a reproducibility contract, and `KVER=6.1.112` is
  what keeps a guest stable when the host kernel moves — a floating tag would
  make a rebuild silently differ from the last one with nothing to bisect.

  One constraint decides the shape: the GitHub token on this host has NO
  `workflow` scope, so `.github/workflows` cannot be pushed (LOCAL.md). That
  points at a `make update-pins` target or a host cron rather than a GitHub
  Action.

  Two drifts to fix on the way: three Go versions live in one repo
  (`GO_VERSION=1.24.1`, `golang:1.25-bookworm`, `golang:1.27rc2-alpine`), and
  the contract is already half-broken because the Anthropic SDK and
  `@anthropic-ai/claude-code` auto-update on every build.

- **An automated gate that proves a live agent works, without the $5
  (owner-gated, after 2026-10-18 21:00).** `anteval --smoke` is the only one
  of the five post-deploy gates that costs money, because it is the only one
  that drives real agent turns — and it is also the only one that catches a
  fleet whose agents answer nothing (`L149` shipped green through the other
  four). The ask is to bundle what it proves into an automated integration
  test. The open question is which half is reachable without a model: the
  dispatch path, the MCP socket, `submit_turn`, delivery and the reply's
  shape can all be driven by a scripted harness, while "did the agent
  understand the question" cannot. Decide the split before writing any of it;
  a test that stubs the model and still calls itself a smoke test is the
  `gate_cannot_fail` failure with a bigger budget.

  The owner's second option is cheaper and keeps a real model in the loop:
  run the existing cases on HAIKU instead of the turn's usual model. That
  keeps "a live agent answered" true, which is the whole value of gate 5,
  and cuts the bill rather than the coverage. It needs one measurement
  first — whether the eval's cases still pass on haiku, because a case that
  only a larger model answers would turn the gate red for a reason that is
  not a regression. Measure before adopting.

  Two levels, assigned by what each case actually tests:
  - **haiku 5.5 at effort `low`** for the cases that are mostly code —
    the answer is checkable and the reasoning is shallow.
  - **sonnet 5.5 at effort `medium`** for the cases that test whether the
    agent SEES its tools and reacts correctly — tool choice is the thing
    under test, so the model has to be good enough that a miss means a
    platform regression and not a model limit.

  Both levels think: haiku 5.5 runs adaptive thinking ON by default. Set
  `effort` EXPLICITLY on both, because haiku 5.5 defaults to `medium` and
  sonnet 5.5 to `high`, so an unset comparison is not like-for-like — and
  both named levels are a step DOWN from those defaults. Prices per MTok:
  haiku 5.5 $0.10/$0.50, sonnet 5.5 $2.00/$10.00. The open question stays
  the same: run the cases at these levels first and see which fail, because
  a case that only a bigger model passes turns the gate red for a reason
  that is not a regression. See
  `.claude/skills/eval/gates.md` and `anteval/`.

- **Deploy pending** — Phase B+D of 5/5 (invites + chats.is_group),
  cli-chat, doc refresh, web UI gaps, and the verb-taxonomy work all
  sit on detached HEAD. krons is the test target; `sudo make images
&& sudo systemctl restart arizuko_krons`. sloth only on explicit
  request.
- **Phase C of 5/5** (secrets resolver) — Phase B (invites) and
  Phase D (chats.is_group) shipped. C reads `chats.is_group` and
  layers folder/user-scope secrets at container spawn. Spec at
  `specs/5/5-worlds-agents-sessions.md`.
- **Operator view of pairing edges (`5/31` follow-up, needs a call).**
  Listing outstanding pairing TOKENS is correctly not a surface — a
  10-minute single-use bearer gives an operator nothing to act on. The
  resulting `acl_membership` claims are the opposite: durable, and the
  answer to "why does this Telegram account carry Alice's authority?".
  dashd today reads them only as `<datalist>` autocomplete on the grants
  page (`dashd/grants_admin.go:111`) — no list, no cut. The blocker is a
  design call, not markup: `DELETE /v1/acl_membership` binds the caller's
  sub to `parent` on purpose, so an operator cannot use it, and the spec
  says the third path is `arizuko apply`. Either that answer stands and
  dashd shows the edges read-only, or the operator gets a face — decide
  before building.
- **`/pub/arizuko/` 13-post series** — specced in
  `content/spec.md`, voice in `content/meta/voice.md`. Long-form
  content authorship; separate session.

## Products, templates, and skill mixins

Current template/skill system is flat file layering — no first-class
"product" or "mixin" abstraction. Issues:

- **No product concept**: Can't define "a finance bot" as a bundle of
  PERSONA.md + skills + output-style + CLAUDE.md sections. Template overlays
  exist (`/migrate` + TEMPLATES file) but are manual, root-only, and
  undocumented.
- **CLAUDE.md is one-shot**: Seeded once from `ant/CLAUDE.md`, then
  diverges. No merge-on-update path without running `/migrate`.
- **Skill re-seeding is all-or-nothing**: `seedSkills()` copies the full
  `ant/skills/` tree every spawn. No per-group skill selection — groups
  get all 38 skills even if they only need 5.
- **No mixin composition**: Template overlays overwrite PERSONA.md, additive-
  merge CLAUDE.md sections. No conflict resolution, no ordering, no
  dependency between mixins.
- **No product registry**: Operators can't browse/select products. Must
  hand-edit TEMPLATES file and know the template directory names.

Desired direction: a "product" is a named bundle (SOUL + skills + CLAUDE
sections + output-style + config). Groups select one product at creation.
Mixins are optional add-ons that compose on top. Both declared in TOML or
similar, resolved at seed time by `container/runner.go` (group setup).

## Next (small wins)

- **A chat axis on the gateway meter (`6/39` amendment — needs a call).**
  Spend attribution is three coordinates and they are named in the spec:
  folder→team, person→`user_id`, turn→key. The mint writes exactly those
  (`routd/gateway.go:100-113`) and no chat, so a folder's many chats collapse
  to one folder total on every rollup surface. Per-TURN cost IS answerable —
  `turn_context` keeps the turn's `chat_jid` (`store/turn_context.go:97`) and
  `GET /v1/cost?turn_id=` resolves the turn to its key — so the number exists
  per turn and only the rollup is missing. The honest shape is a LiteLLM tag
  set at mint and read back from `/spend/tags`: one writer, one reader
  (`spendByChat` beside `spendByFolder`), and no arizuko-side spend table,
  which `6/39` deleted on purpose. Two questions come first: whether a chat
  rollup is wanted at all, or folder + turn is the honest granularity; and
  whether the gateway's tag index takes one value per chat jid unbounded over
  an instance's life. A fourth coordinate is an amendment to a shipped spec,
  so it is a decision before it is work.
- **JID model evolution** — four debts queued: chat-jid
  vs user-jid as distinct types, unified struct representation
  (string-prefix → typed), consistent `<resource>/<id>` naming across
  platforms (kill `t1_/t2_/t3_/t4_`), guild always in Discord JID. Pick
  up together when JID gets focus — most invasive done early before
  Discord rooms accumulate. Concept doc draft at `template/web/pub/
concepts/jid.html` waits on this.
- **Daemon test gaps** — `dashd` and `proxyd` have partial coverage
  but miss auth gate + path-traversal tests. (`whapd` now at 37 tests
  across server/reply/queue/typing; remaining gaps listed below.)
- **Skill search MCP tool** — integrate browse/search across skill hubs
  (ClawHub, Skills.sh, Hermes skills, others) as an arizuko MCP tool so
  agents can discover + fetch skills on demand instead of bundling
  everything in the image. Prior art: Hermes Agent's `skills browse` /
  `skills search`.
- ~~Module-path mismatch~~ — done 2026-05-13. `go.mod` and all imports
  use `github.com/kronael/arizuko`. Shippable sibling components stay
  in the single `go.mod`; orthogonality enforced by the import-graph
  rule.

## Microservice port — open phases

### Phase 5: Adapters not yet ported

- twitter (low priority — no clean API)
- facebook (low priority — fca-unofficial)
- web/slink as standalone adapter (currently router-internal)

### Phase 6: Web extraction (if needed)

- Separate web server process or keep router-internal
- If separate: HTTP to router like other adapters
- Auth, slink, vite proxy

### Open

- Extension packaging: how to distribute/install adapters
- Event types beyond messages (reactions, edits, joins)
- Large file delivery (base64 vs upload endpoint)

## Telegram webhook mode

`teled` polls via `getUpdates` (30s long-poll). Webhook mode pushes
updates immediately, lower latency and CPU. ~100 LOC change:

- `POST /webhook` handler in `teled/server.go`: parse `tgbotapi.Update`,
  validate `X-Telegram-Bot-Api-Secret-Token`, call `b.handle()`
- Remove `bot.poll()` and offset state file
- Call `api.SetWebhook(url)` on startup

**Blocker**: HTTPS endpoint required (Telegram needs TLS on
443/80/88/8443). Need TLS termination in front of `teled:9001`.

## Test infrastructure debt

## Daemon test gaps (high priority)

Zero test coverage on the following daemons. Risk of regression high
because runtime branching/state machines are uncovered:

- **onbod** (586 LOC, state machine + approval): state transitions,
  permission check, name validation/collision, approval action,
  `seedDefaultTasks`. **Highest priority** — auth-adjacent, large file.
- **dashd**: all routes, JWT auth gate, path-traversal guard in
  `renderMemorySection`, DB error swallowing.
- **proxyd**: auth gate, OAuth callback, `/slink/*` rate limiter,
  vhost matching, `/dav` rewrite.
- **whapd**: server.ts routes + reply + queue + typing covered;
  remaining gaps — `flushOutboundQueue` (not exported from main.ts,
  needs refactor to extract into testable module), `pair()` /
  `reconnectOnly()` 515 recursion (needs baileys socket mock infra
  not present in server.test.ts). **LID translation: won't do** —
  routing is by chat_jid, onbod handles unknown `<num>@lid` DMs via
  awaiting_message, sender LID is cosmetic.

## Daemon boundary leaks (medium)

Adapters do work that belongs in the gateway. Cleanup as adapters are
touched, not all at once:

- **All Go adapters**: mention rewriting, attachment formatting,
  message chunking, markdown→HTML — move to gateway/router.
- **gateway/gateway.go**: routing logic duplicates `router.ResolveRoute`;
  sticky routing managed by gateway, not router.
- **timed/main.go**: spawn archiving/TTL — gateway lifecycle concern.
- **onbod**: direct DB writes to `groups`/`routes`, calls
  `container.SeedGroupDir` directly — should go through gateway API.

## Daemon dead code / redundancy (low)

- `reditd/client.go:27-28` — in-memory cursor lost on restart, causes
  re-polls.
- _(removed 2026-04-11)_ Claim of "raw-secret bypass in proxyd" was
  stale: proxyd requires a signed JWT (Bearer) or a hashed session
  cookie (`auth.HashToken` lookup), never the raw `AUTH_SECRET`. dashd
  has no auth by design — proxyd fronts it. Pinned by
  `proxyd/TestProxydRequireAuthRawSecretRejected` +
  `TestProxydRouteRawSecretAsRefreshCookieRejected` and
  `dashd/TestDashIgnoresAuthHeader`.

## Channel adapters status

| Adapter   | Status  | Language |
| --------- | ------- | -------- |
| whatsapp  | shipped | TS       |
| discord   | shipped | Go       |
| email     | shipped | Go       |
| telegram  | shipped | Go       |
| reddit    | shipped | Go       |
| bluesky   | shipped | Go       |
| web/slink | open    | Go       |
| twitter   | open    | —        |
| facebook  | open    | —        |

## Container tooling — remaining

Already in container: git, bun, go, rust, python+uv, chromium, ffmpeg,
ripgrep, fd, fzf, bat, jq, shellcheck, pandoc, imagemagick, yt-dlp,
tesseract, optipng, jpegoptim, marp-cli, biome, prettier, ruff, pyright,
pandas, matplotlib, plotly, numpy, scipy, python-pptx, openpyxl,
weasyprint, gh, sqlite3, duckdb, psql, redis-cli, yq, miller, grpcurl,
socat, delta, shfmt, hadolint, sqlfluff, semgrep, graphviz, ghostscript,
exiftool, sox, mediainfo, qrencode, parallel, rsync, sysstat.

Not yet:

- **Data**: `xsv`
- **HTTP**: `xh`, `websocat`, `hurl` — ant/Dockerfile edit drafted, not
  yet image-built (disk-full at time of authoring 2026-04-09)
- **Lint**: `yamllint`, `vale`
- **Build**: `just`, `watchexec`, `hyperfine`
- **Load**: `k6`
- **Diagrams**: `gnuplot`, `typst`
- **Security**: `age`, `sops` — drafted with the above, same status
- **Infra**: `kubectl`, `opentofu`, `aws`
- **Crypto**: `solana`, `cast` (Foundry)
- **Lang**: `ruby`
- **Misc**: `hexyl`, `mkcert`

## Direction notes — 2026-05-26

### Filestore = WebDAV; auth extensions later

The platform's filestore primitive is `davd` (WebDAV via dufs, spec
`specs/5/M-webdav.md`, shipped). Today auth is proxyd JWT/cookie. Bigger
auth surface (per-folder API keys, signed pre-share URLs, time-bounded
tokens, public-read for specific subtrees) is a future extension —
parked, not active.

### Ambitious projects to seed the platform

Agents should be able to iterate + build out the platform itself. Open
target list (capture; pick + spec as they mature):

- **Hive — per-group habitat of services**: webhooks + JSON KV + web
  proxy + systemd as the four primitives. See 2026-05-26 design
  discussion. Likely phase 8 spec when triggered.
- **YAML manifest model**: declarative resource intent dispatched per
  daemon, k8s-style. Shipped as `specs/5/8-yaml-manifests.md`.
- **Product registry + clone/distribute**: agent products as
  distributable artifacts (git repo manifest + skill bundle +
  PERSONA). Foundation for the "agents managing agents" loop.
- **Self-improving agent loop**: agent reviews its own audit_log +
  diary, proposes patches to its own skills/persona/ACL via MCP,
  emits a manifest diff for operator review.
- **Cross-instance federation**: gateway-to-gateway peer protocol so
  one operator's agent can delegate to a peer instance's agent.
  Requires capability token federation + cross-instance audit.
- **Tenant-managed channel adapters**: each tenant onboards its own
  Slack/Discord/WhatsApp bot through a UI; today still operator-level.
- **Filestore auth extensions**: per-folder API keys, signed pre-share
  URLs, public-read subtrees, time-bounded tokens.
- **Agent-authored skill marketplace**: agents publish skills to a
  registry; other agents discover + install with manifest + auth gate.
- **Turn-level undo** (from choreographr, 2026-08-04): retract a bad turn
  instead of re-prompting around it. That project links messages by
  `parent_id` and walks the subtree; arizuko does not need the tree —
  `messages.turn_id` already groups a turn, so the import is one `deleted`
  column, a filter on read, and a verb. **The hard part is not the schema.**
  Conversation state has two owners: `routd.db` (the delivery log) and Claude
  Code's own session file inside the container. Soft-deleting in `routd.db`
  alone desyncs them — the agent still remembers the undone turn on its next
  spawn. So the design question that needs answering BEFORE any code is what
  undo means: hide from the user only (honest, cheap, and the agent may then
  reference something the user can no longer see), or roll back the container's
  session too (fragile — arizuko does not own that file format). Needs sign-off
  per the redesign gate.

Each of these earns a spec when it moves to active. Parked here as
raw direction, not yet committed to.

## Audit every registered callback against its real callers

**82 exported func-typed fields across 14 files** — `RegisterSteer`,
`AfterScan`, `BeforeInsert`, `AllowlistFn`, `Authorize`, `Authz`,
`AddGroupWatcher`, `BumpEngagement` and the rest. Each one is an injection
point a spec or an interface declares, and each is a place where the
declaration and the use can drift apart without anything failing.

The sweep: for every such field, find who ASSIGNS it and who CALLS it. Three
outcomes worth separating, because they need different fixes:

- **Assigned and never called** — dead injection. Delete the field.
- **Called with exactly one assignment, forever** — an abstraction with one
  implementation. It is a direct call wearing a seam; inline it unless a
  second implementation is actually planned.
- **Genuinely many-to-one** — the seam earns its place. Say so in the doc
  comment so the next reader does not delete it.

Use `gopls go_symbol_references` per field, never grep: `Authorize` and
`Authz` alone appear in several packages with different meanings, and a
name-based sweep answers about the wrong one (the `L355` failure).

Why it matters here: `runed.Runtime`'s `RegisterSteer` is one of these, and
the minimal-port work (`6/41-qemu-substrate` §"The seam") turns on whether
that closure is load-bearing or ceremony. Settling the class settles that
field too.

Parked as direction. Not started.

## Later

- Proactive operator delivery: the error and health-check trigger, dedup and rate limit for error bursts, the listener-digest format; address the operator through `role:operator` membership, nothing routes there today
- Explore [Plannotator](https://docs.plannotator.ai/open-source/reference/hooks) for browser review of specs and HTML, JSON feedback into Arizuko, and reusable UI components with GitHub as the review record store
