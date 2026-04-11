# TODO

Active backlog. See ROADMAP.md for version-level strategy.

## Now

_(empty)_

## Verified shipped (2026-04-11)

- **audit-log source semantics** (`specs/3/c-audit-log.md`): all write
  paths in gateway/ipc/onbod/timed/api use `source=''` for outbound
  and `source=<adapter>` for inbound. Live DB legacy `agent`/`mcp`
  rows are pre-fix historical data (latest 2026-04-10 22:50). Post-fix
  messages are correct. GREEN.
- **onbod welcome `source=''`**: `onbod/main.go:334` explicitly sets
  `source=''` on welcome insert, `onbod/main_test.go:191` asserts
  it. GREEN.

## Next (small wins)

- **Daemon test gaps** — `dashd` and `proxyd` have partial coverage
  but miss auth gate + path-traversal tests. (`whapd` now at 37 tests
  across server/reply/queue/typing; remaining gaps listed below.)
- **Skill search MCP tool** — integrate browse/search across skill hubs
  (ClawHub, Skills.sh, Hermes skills, others) as an arizuko MCP tool so
  agents can discover + fetch skills on demand instead of bundling
  everything in the image. Prior art: Hermes Agent's `skills browse` /
  `skills search`.

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
