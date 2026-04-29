# TODO

Active backlog. See `specs/index.md` for phase status, `CHANGELOG.md`
for shipped milestones.

## Now

- **Deploy pending** — Phase B+D of 7/35 (invites + chats.is_group),
  cli-chat, doc refresh, web UI gaps, and the verb-taxonomy work all
  sit on detached HEAD. krons is the test target; `sudo make images
&& sudo systemctl restart arizuko_krons`. Marinade only on explicit
  request.
- **Phase C of 7/35** (secrets resolver) — Phase B (invites) and
  Phase D (chats.is_group) shipped. C reads `chats.is_group` and
  layers folder/user-scope secrets at container spawn. Spec at
  `specs/7/35-tenant-self-service.md`.
- **`/pub/arizuko/` 13-post series** — specced in
  `content/spec.md`, voice in `content/meta/voice.md`. Long-form
  content authorship; separate session.

## Products, templates, and skill mixins

Current template/skill system is flat file layering — no first-class
"product" or "mixin" abstraction. Issues:

- **No product concept**: Can't define "a finance bot" as a bundle of
  SOUL.md + skills + output-style + CLAUDE.md sections. Template overlays
  exist (`/migrate` + TEMPLATES file) but are manual, root-only, and
  undocumented.
- **CLAUDE.md is one-shot**: Seeded once from `ant/CLAUDE.md`, then
  diverges. No merge-on-update path without running `/migrate`.
- **Skill re-seeding is all-or-nothing**: `seedSkills()` copies the full
  `ant/skills/` tree every spawn. No per-group skill selection — groups
  get all 38 skills even if they only need 5.
- **No mixin composition**: Template overlays overwrite SOUL.md, additive-
  merge CLAUDE.md sections. No conflict resolution, no ordering, no
  dependency between mixins.
- **No product registry**: Operators can't browse/select products. Must
  hand-edit TEMPLATES file and know the template directory names.

Desired direction: a "product" is a named bundle (SOUL + skills + CLAUDE
sections + output-style + config). Groups select one product at creation.
Mixins are optional add-ons that compose on top. Both declared in TOML or
similar, resolved at seed time by `container/runner.go`.

## Next (small wins)

- **JID model evolution** — four debts queued in
  `specs/5/S-jid-format.md` ("Adjacent open evolution work"): chat-jid
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
- **Split gated into focused daemons** — `gated` today blends ~70%
  message work (poll loop, persist, route, deliver), ~20% container
  orchestration, ~10% channel registration + health. Name is also
  generic ("gateway" = any HTTP boundary). Candidate split: `msgd`
  (messages + routing + DB owner), `runnd` (container spawn + MCP
  socket), keep channel registration small enough to live in either.
  Invasive — gated is referenced everywhere. Pick up when one of the
  three concerns starts hurting (e.g. container orchestration grows
  into spec 9 QEMU backend, or channel registry grows tiers).

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
| mastodon  | shipped | Go       |
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
