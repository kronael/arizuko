# TODO

Active backlog. See ROADMAP.md for version-level strategy.

## Now

_(empty — deploy-verify web skill re-seed + onbod source='' fix; see
bugs.md "Verify shipped")_

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
  not present in server.test.ts), LID translation.

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
- `proxyd/main.go:327-361` vs `dashd/main.go:97-115` — inconsistent
  auth posture (raw-secret bypass present in proxyd, absent in dashd).

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
