# TODO

## Channel adapters

Port from kanipi TS, strip to minimal standalone adapters.
Each is a separate process speaking the channel protocol
(`specs/7/1-channel-protocol.md`). No router imports, no
shared state.

| Adapter   | Source                      | Language | Priority |
| --------- | --------------------------- | -------- | -------- |
| whatsapp  | kanipi channels/whatsapp.ts | TS       | high     |
| discord   | kanipi channels/discord.ts  | TS       | high     |
| email     | kanipi channels/email.ts    | Go       | medium   |
| web/slink | kanipi slink.ts + web.ts    | Go       | medium   |
| reddit    | kanipi channels/reddit/     | TS       | low      |
| twitter   | kanipi channels/twitter/    | TS       | low      |
| facebook  | kanipi channels/facebook/   | TS       | low      |
| mastodon  | kanipi channels/mastodon/   | TS       | low      |
| bluesky   | kanipi channels/bluesky/    | TS       | low      |

Rules:

- Strip all kanipi integration (deps callbacks, shared state)
- Replace with HTTP calls to router API
- Each adapter is self-contained (own package.json or go.mod)
- Testable in isolation without the router running
- Start minimal (inbound only), add outbound incrementally

## Telegram webhook mode

teled currently polls via `getUpdates` (30s long-poll). Webhook mode pushes
updates from Telegram immediately, lower latency and CPU.

Code changes are small (~100 lines):

- Add `POST /webhook` handler to `teled/server.go` — parse `tgbotapi.Update`,
  validate `X-Telegram-Bot-Api-Secret-Token` header, call `b.handle()`
- Remove `bot.poll()` and offset state file (`teled-offset`)
- Call `api.SetWebhook(url)` on startup (or once manually)

Blocker: **HTTPS endpoint** — Telegram requires TLS on 443/80/88/8443.
teled already has an HTTP server (`:9001`); just needs TLS termination in
front (nginx/Caddy). Once infra exists the code change is straightforward.

- [ ] Add webhook handler + remove polling once HTTPS infra is confirmed

## Remaining kanipi feature ports

| Feature            | Where     | Notes                    |
| ------------------ | --------- | ------------------------ |
| prototype spawning | container | clone group on missing   |
| reply-to outbound  | chanreg   | Channel interface change |
| whisper pipeline   | mime      | voice/video transcribe   |

## Container tooling

Already in container: git, bun, go, rust, python+uv, chromium, ffmpeg, ripgrep, fd,
fzf, bat, jq, shellcheck, pandoc, imagemagick, yt-dlp, tesseract, optipng, jpegoptim,
marp-cli, biome, prettier, ruff, pyright, pandas, matplotlib, plotly, numpy, scipy,
python-pptx, openpyxl, weasyprint.

### Code hosting / VCS

- [x] `gh` — GitHub CLI: issues, PRs, releases, gists, Actions

### Data / query

- [x] `sqlite3` — explicit CLI (query local DBs, not just via Python)
- [x] `duckdb` — in-process analytics on CSV/JSON/parquet, no server needed
- [x] `psql` — Postgres client (pg_dump, query remote DBs)
- [x] `redis-cli` — query Redis instances
- [ ] `xsv` — fast CSV slicing/sorting/joining (Rust, single binary)
- [x] `yq` — YAML processor (jq for YAML; configs, k8s, CI files)
- [x] `miller` — stream CSV/JSON/TSV like awk (complements xsv)

### HTTP / API / network

- [ ] `xh` — modern curl alternative (Rust httpie; cleaner API testing output)
- [ ] `websocat` — WebSocket client/server for testing WS endpoints
- [x] `grpcurl` — gRPC reflection + call testing
- [ ] `hurl` — file-based HTTP test sequences (CI-friendly)
- [x] `socat` — bidirectional data relay; Unix socket debugging

### Git / diff

- [x] `delta` — syntax-highlighted git diffs (agent-readable output)
- [x] `shfmt` — shell script formatter (pair with shellcheck)

### Linting / static analysis

- [x] `hadolint` — Dockerfile linter
- [x] `sqlfluff` — SQL formatter and linter
- [x] `semgrep` — multi-language static analysis / secret scanning
- [ ] `yamllint` — YAML strict linter (catches tab issues, duplicates)
- [ ] `vale` — prose linter (docs, changelogs, READMEs)

### Build / task runners

- [ ] `just` — justfile task runner (simpler make; one per project)
- [ ] `watchexec` — re-run commands on file change (dev loops)
- [ ] `hyperfine` — command benchmarking with stats

### Load / perf testing

- [ ] `k6` — scriptable HTTP load testing (JS scripts)

### Diagrams / visualization

- [x] `graphviz` — dot → SVG/PNG (architecture, dependency graphs)
- [ ] `gnuplot` — terminal/file plotting from data
- [ ] `typst` — modern typesetting (PDF reports, whitepapers; lighter than LaTeX)

### Media / documents

- [x] `ghostscript` — PDF merge/split/compress
- [x] `exiftool` — read/write media metadata
- [x] `sox` — audio format conversion and processing
- [x] `mediainfo` — detailed media file inspection
- [x] `qrencode` — generate QR codes from CLI

### Security / secrets

- [ ] `age` — modern file encryption (replaces GPG for most cases)
- [ ] `sops` — encrypted secrets files (YAML/JSON/env with age/GPG keys)

### Infrastructure

- [ ] `kubectl` — Kubernetes cluster management
- [ ] `opentofu` — IaC (open Terraform fork; provision cloud resources)
- [ ] `aws` CLI — S3, Lambda, ECR, CloudWatch from agent

### Blockchain / crypto

- [ ] `solana` CLI — keypairs, airdrop, deploy, account queries (Atlas/REDACTED)
- [ ] `cast` (Foundry) — EVM: call contracts, send txs, decode data

No binary tools needed for: Hyperliquid (`hyperliquid-python-sdk` / REST+WS),
Ethereum (`web3` py / `viem` js), Polymarket (`py-clob-client` / REST).
Install on-demand with `uv pip install` or `bun add`.

### Runtime (one new language)

- [ ] `ruby` — scripting, Jekyll, occasional gem tooling

### Misc CLI

- [x] `parallel` — GNU parallel; fan-out batch operations
- [ ] `hexyl` — hex dump with ASCII sidebar (binary file inspection)
- [x] `rsync` — efficient file sync (local and remote)
- [ ] `mkcert` — locally-trusted dev HTTPS certs
- [x] `ps`/`free` extras — `sysstat` package for `sar`, `iostat`, `mpstat` (scriptable, not TUI)
