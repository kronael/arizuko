# Changelog

All notable changes to arizuko are documented here.

arizuko is a fork of [nanoclaw](https://github.com/nicholasgasior/nanoclaw)
(upstream at v1.1.3).

<!-- Each release entry opens with a `>` blockquote — that's the
     chat broadcast, extracted verbatim. Format spec in root
     CLAUDE.md "## Announcing". ≤ 9 lines, 3-6 bullets, user
     benefit first, end with the changelog link. -->

---

## [Unreleased]

## [v0.33.13] — 2026-05-05

> arizuko v0.33.13 — 05 May 2026
>
> • Typing indicator stops on Telegram 403 (blocked user) — no more polling drop → outbox flood → adapter unhealthy loop
> • Agent think-only output fixed — `<think>`-only turns no longer counted as delivered; logged INFO not WARN
> • Agent CLAUDE.md — think blocks always closed; steered messages follow same response rules as regular inbound
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Fixes the outbox flooding pattern seen in production: a user blocking the bot
caused teled to retry typing every 4s, Telegram dropped polling, adapter went
unhealthy, outbox filled. Fix is in `chanlib.TypingRefresher` — `send` now
returns bool; the per-JID loop stops on first false.

Also closes the silent-agent gap: gateway was marking `hadOutput=true` on any
text, including think-only output that gets stripped before delivery. Now
`hadOutput` is set only after stripping. Agents are also fixed at the CLAUDE.md
level — the bogus unclosed-`<think>`-for-silence instruction is replaced with
correct closed-block behavior.

### Fixed

- **`chanlib/typing.go`** — `TypingRefresher.send` signature changed from
  `func(string)` to `func(string) bool`; loop exits on `false`.
- **`teled/bot.go`** — typing interval 4s → 10s; `sendTyping` returns `false`
  on any Telegram error (403, network, etc.); loop stops immediately.
- **`discd/bot.go`** — same `sendTyping` bool return pattern.
- **`gateway/gateway.go`** — `hadOutput` set after stripping think/status
  blocks, not before. Agent-silent log downgraded WARN → INFO.
- **`ant/CLAUDE.md`** — `# When to respond` rewritten: closed `<think>` blocks
  only for silence; steered messages follow same response rules; half the length.

## [v0.33.12] — 2026-05-03

> arizuko v0.33.12 — 03 May 2026
>
> • Codex per-group dirs no longer race Docker on cold start — gated pre-seeds `.codex/` for every group at boot, before auto-migrate fires
> • Fixes silent codex `Permission denied` on first spawn after restart, when parallel agent boots let Docker materialize the bind source as root
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Closes the v0.33.11 cold-start race: when many groups spawn in
parallel (auto-migrate at gated boot), the runner's lazy
`os.MkdirAll(<groupDir>/.codex)` could lose the race against
`docker run`, leaving Docker to materialize the bind source as
root. The agent (uid 1000) then couldn't write codex state and
`codex login status` failed with `Permission denied`. Warm
single-spawn paths were never affected. Fix is a tiny startup hook.

### Fixed

- **`gateway/gateway.go`** — new `seedCodexDirs()` walks all known
  groups at gateway start (synchronously, before
  `checkMigrationVersion` enqueues any auto-migrate spawn) and
  ensures each `<groupDir>/.codex/` exists. Runs as gated's uid
  1000, so the dirs land with correct ownership.
- The runner's lazy `os.MkdirAll` at spawn time is preserved as a
  safety net for groups added mid-flight.

### Operator notes

Existing root-owned `.codex/` dirs from v0.33.11 cold starts won't
be repaired automatically (gated as uid 1000 can't chown root).
Manual one-time fix on each instance:

```bash
sudo find /srv/data/arizuko_<inst>/groups -mindepth 2 -maxdepth 3 \
  -name .codex -user root -exec chown -R 1000:1000 {} \;
```

After deploy, new spawns will use the pre-seeded 1000-owned dir.

## [v0.33.11] — 2026-05-03

> arizuko v0.33.11 — 03 May 2026
>
> • Codex mount per-group isolated — each agent gets its own writable `.codex/` workspace; only `auth.json` + `config.toml` are shared (RO)
> • End-to-end test harness — slink-driven scenarios run on release via `make test-e2e`; CI gates them on tag pushes
> • Operator one-time: `chmod 644 ~/.codex/auth.json ~/.codex/config.toml` so the container can read host creds
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Closes the codex isolation gap and adds a real release-only e2e
test path so silent mount-skips like v0.33.9's get caught earlier.

### Changed

- **`container/runner.go`** — codex mount is now layered:
  `<groupDir>/.codex` rw + RO file overmounts of `auth.json` and
  `config.toml` from `HOST_CODEX_DIR`. Per-group writable workspace
  (history, sessions, memories, sqlite state); shared credentials.
- **`container/run_test.go`** — `TestRun_CodexDirMountWhenSet`
  asserts the parent dir mount + both file overmounts and verifies
  parent precedes file overmounts (mount order matters).

### Added

- **`webd/slink_e2e_test.go`** — 4 active e2e scenarios + 1 stub:
  `DropAndRead`, `SSEStream`, `Steer`, `GetThread`, plus an oracle
  surface stub. Each guards on `testing.Short()`.
- **`Makefile` / `webd/Makefile`** — `test-e2e` target separate
  from `test`; the latter now passes `-short` and stays fast.
- **`.github/workflows/ci.yml`** — `e2e` job gated on
  `refs/tags/v*` so the slow path runs on release.

### Operator notes

- Host-side cred files must be readable by container uid 1000:
  `chmod 644 ~/.codex/auth.json ~/.codex/config.toml`. Mode 600
  blocks the bind mount even though the file is RO.
- Per-group `.codex/` dirs land under
  `/srv/data/arizuko_<inst>/groups/<folder>/.codex/` — first
  spawn creates them.

## [v0.33.10] — 2026-05-03

> arizuko v0.33.10 — 03 May 2026
>
> • `HOST_CODEX_DIR` mount now actually lands on spawned agents (v0.33.9 silently skipped due to a stat check)
> • `~/.codex` from the host now appears at `/home/node/.codex` rw in every agent
> • No skill-side action — codex CLI just works once the operator sets `HOST_CODEX_DIR` and restarts gated
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Hotfix release. v0.33.9 added the mount logic but guarded it with
`os.Stat(cfg.HostCodexDir)` running inside the gated container —
where the HOST path isn't visible — so the check always failed and
the mount silently skipped on every spawn.

### Fixed

- **`container/runner.go`**: dropped the `os.Stat` guard on the
  codex-dir mount. `cfg.HostCodexDir` is a HOST path that the
  docker daemon resolves at agent-spawn time, not a path that gated
  needs to read. Loud failure (docker errors at startup if the
  path is wrong) beats silent skip.
- **`container/run_test.go`**: simplified the test — uses a
  literal `/host/codex` path, doesn't try to mkdir-and-stat
  anymore. `TestRun_CodexDirMountSkippedWhenMissing` retired
  (the runner doesn't probe; that test was testing the bug).

## [v0.33.9] — 2026-05-03

> arizuko v0.33.9 — 03 May 2026
>
> • `oracle` skill now picks up the operator's `codex login` from a host-side `~/.codex` mount
> • Agents share refresh-token rotation, sessions, history with the host — single login serves every group
> • API-key path (`CODEX_API_KEY` / `OPENAI_API_KEY` in folder secrets) still works alongside
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Closes the auth-path ambiguity surfaced when the operator
investigated oracle and found `auth_mode: chatgpt` in
`~/.codex/auth.json` — a different mechanism from the env-var path
the v0.33.4 skill was built around. Both now coexist.

### Added

- **`HOST_CODEX_DIR` env knob** (`core/config.go`,
  `compose/compose.go`, `container/runner.go`): when set on the
  gated daemon's env, the host path is bind-mounted into every
  spawned agent at `/home/node/.codex` (rw). codex CLI then reads
  `auth.json` directly — same as on the host, including refresh
  rotation. Empty disables; agents fall back to the env-var path.
- **Skill update** (`ant/skills/oracle/SKILL.md`): documents both
  auth paths (host-mount vs folder-secret), unified missing-auth
  detection (`codex login status` OR env check), corrected
  description to surface the new mount option for `/dispatch`.
- Tests: `container/run_test.go` covers the mount being present
  when `HostCodexDir` is set + a real dir, and skipped when unset
  or pointing to a missing dir.

### Notes

- ChatGPT-Plus / Free quota applies to the operator's account when
  using the OAuth path; no per-agent rate-limit isolation.
- All agents share `~/.codex/sessions/`, `~/.codex/history.jsonl`,
  `~/.codex/memories/` with the host — minor cross-group context
  leak in codex's own state, acceptable for one-shot oracle calls.
- Operators who want strict per-folder isolation should leave
  `HOST_CODEX_DIR` empty and use the env-var path with one API key
  per folder.

## [v0.33.8] — 2026-05-03

> arizuko v0.33.8 — 03 May 2026
>
> • Issues skill — clearer how/where to log unresolved user-reported bugs
> • Agent now exposes `/issues` as a discoverable skill instead of buried CLAUDE.md prose
> • Leaner `ant/CLAUDE.md` — procedure moved to its own page
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

### Added

- `ant/skills/issues/SKILL.md` — dedicated skill owning the
  user-reported-bug recording workflow. Frontmatter `description`
  surfaces it on `/dispatch`; body covers when to log, format, and
  the operator-consolidation contract.

### Changed

- `ant/CLAUDE.md` "Recording user-reported issues" section reduced
  to a one-line pointer at `~/.claude/skills/issues/SKILL.md`.
  CLAUDE.md is seeded into every group on spawn, so trimming
  procedure-heavy prose keeps the per-group bootstrap lean.

### Migration

- `ant/skills/self/migrations/098-v0.33.8-issues-skill.md` —
  no data migration; just announces the new skill location.
- `ant/skills/self/MIGRATION_VERSION`: 97 → 98.

## [v0.33.7] — 2026-05-03

> arizuko v0.33.7 — 3 May 2026
>
> • Docs sweep against HEAD — typed-JID examples, dropped `groups.state`, current verb matrix
> • `send_voice` now visible in the verb support matrix and chanlib route list
> • Oracle + ttsd reclassified from "planned" to shipped integrations across ARCHITECTURE.md and CLAUDE.md
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Docs-only release. Walks the contributor-ring docs against the
codebase as it stands at v0.33.6 and fixes drift accumulated over
the seven releases shipped today.

### Changed

- **`ROUTING.md`** — platform-JID example column rewritten to typed
  forms post-v0.33.0 (`telegram:user/<id>`, `telegram:group/<id>`,
  `discord:dm/<channel>`, `reddit:comment/<id>`,
  `mastodon:account/<id>`); route example uses
  `chat_jid=telegram:group/12345`; sender-expansion example uses
  `discord:user/<id>`.
- **`GRANTS.md`** — `groups` table column list reflects migration
  0041 dropping `state` / `spawn_ttl_days` / `archive_closed_days`.
- **`ARCHITECTURE.md`** — `Group` Key Type description drops `state`;
  oracle skill and `ttsd` reclassified from "planned" to shipped
  integrations.
- **`CLAUDE.md`** (root) — core-vs-integrations bullet matches the
  shipped reality (TTS via `ttsd`, oracle via folder secrets).
- **`EXTENDING.md`** — verb support matrix gained the `send_voice`
  row; count updated 11 → 12. discd / teled / whapd native;
  twitd hint-only; rest unsupported.
- **`chanlib/README.md`** — `NewAdapterMux` handler tree now lists
  `/send-voice`; calls out `NoVoiceSender` and `NoFileSender` mixins.
- **`ipc/README.md`** — tool surface adds `send_voice` and
  `inject_message`; documents `submit_turn` JSON-RPC for per-turn
  agent output.
- **`SECURITY.md`** — boundaries table gained a slink-MCP row
  (`POST /slink/<token>/mcp`, token IS the auth).
- **`crackbox/README.md`** — fixed `specscs` typo (5 occurrences) →
  `specs`.
- Migration `097-v0.33.7-docs-sweep.md` + version bump 96 → 97.

## [v0.33.6] — 2026-05-02

> arizuko v0.33.6 — 2 May 2026
>
> • emaid compose env-var names match the daemon — IMAP/SMTP wiring no longer starts with empty config
> • Adapters embedding `NoFileSender` now return structured `*UnsupportedError` with per-platform hints
> • mastd / reditd / linkd carry concrete `send_file` hints pointing at a viable alternative
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

### Fixed

- `template/services/emaid.toml`: env-var names now match what
  `emaid/main.go` reads (`EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`,
  `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`). Compose-deployed emaid was
  starting with empty config because the toml declared `IMAP_HOST` /
  `SMTP_USER` / etc. while the Go side requires the `EMAIL_` prefix.
  Go is the source of truth — operators with working `.env` files keep
  working.
- `chanlib.NoFileSender.SendFile` returns
  `Unsupported("send_file", "", ...)` instead of a bare
  `errors.New("send-file not supported")`. Adapter HTTP layer encodes
  this as 501 with the structured tool/platform/hint envelope so the
  agent gets actionable diagnostic.
- `mastd`, `reditd`, `linkd` override `SendFile` with platform-specific
  hints (Mastodon v2 media not wired; Reddit's 3-step image flow not
  wired; LinkedIn UGC `registerUpload` not wired) — each points the
  agent at inlining a URL via `post(content=...)` for now.

## [v0.33.5] — 2026-05-02

> arizuko v0.33.5 — 2 May 2026
>
> • Foundation for standalone `ant/` Go package — folder-as-agent CLI scaffolding
> • Skill-portability partition: 37 portable / 1 arizuko-only (`self`)
> • Existing TS runtime + `arizuko-ant:latest` unchanged this pass
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Foundation pass for a standalone `ant/` Go package — drives the
official `claude` CLI against an ant-folder, shippable outside
arizuko. This pass lays the package skeleton only; the existing TS
runtime in `ant/src/` and `arizuko-ant:latest` are unchanged.

### Added

- `ant/cmd/ant/main.go` — CLI flag stub:
  `ant <folder> [--prompt=<text>] [--mcp [--socket=<path>]]
[--sandbox=none|dockbox|crackbox]`. `--help` exits 0; body is
  unimplemented and exits `64` (EX_USAGE) so misconfigured callers
  fail loud.
- `ant/pkg/agent/loader.go` — `LoadFolder(path)` resolves
  `SOUL.md` / `CLAUDE.md` / `skills/` / `diary/` / `secrets/` /
  `MCP.json` / `workspace/`; `ErrNotFound` for missing or non-dir
  paths. Three unit tests.
- `ant/pkg/host/`, `ant/pkg/runtime/` — package stubs (sandbox
  abstraction + Claude CLI driver). Doc comments only; wiring lands
  with the runtime port.
- `ant/scripts/curate-skills.sh` — portable-vs-arizuko-only partition
  gate. Greps `@gated|@arizuko|gated.sock` in each `SKILL.md`.
  Current count: **37 portable, 1 arizuko-only** (`self`). No skills
  moved this pass.
- `ant/README.md` — three-question intro; documents the deferred work.
- Migration `095-v0.33.5-ant-foundation.md` + version bump 94 → 95.

### Notes

- `ant/cmd/`, `ant/pkg/agent`, `ant/pkg/host`, `ant/pkg/runtime`
  import zero arizuko-internal packages — same orthogonality property
  as `crackbox/`, enforced by the import graph.
- The new `ant` Go binary is not yet in the root `Makefile`'s
  `COMPONENTS` recursion (existing `ant/Makefile` does not implement
  `build`/`lint`/`test` targets); `go build ./ant/cmd/ant` builds it
  ad-hoc, `go test ./...` typechecks it.

## [v0.33.4] — 2026-05-02

> arizuko v0.33.4 — 2 May 2026
>
> • New `/oracle` skill — agent can ask the `codex` CLI for a second opinion on tricky algorithms or unfamiliar libraries
> • Auth via `OPENAI_API_KEY` / `CODEX_API_KEY` in folder secrets — folders without the key fall back gracefully
> • No new MCP tool, no new daemon — skill + binary on the agent image, secret in the existing folder secrets path
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Lets Claude consult a second model when uncertain — disagreement
with self, sanity check on a non-obvious implementation, library
Claude doesn't know well. Subprocess invocation, no new IPC.
Spec `specs/5/H-call-llm-mcp.md` is now shipped.

### Added

- **`/oracle` agent skill** (`ant/skills/oracle/SKILL.md`) — drives
  the `codex` CLI as a one-shot subprocess (`codex exec "<prompt>"`
  argv, stdin, or piped). Documents when to invoke (tricky
  algorithm, unknown library, sanity check), the missing-key
  fallback, and how to add the secret via the existing folder
  secrets path. Output is advisory; cite when acting on it.
- **`@openai/codex` on the agent image** (`ant/Dockerfile`) — added
  to the same global npm install as `@anthropic-ai/claude-code` and
  `@apify/mcpc`. Binary lands at `/usr/local/bin/codex` on `PATH`.

Agent migration 094.

## [v0.33.3] — 2026-05-02

> arizuko v0.33.3 — 2 May 2026
>
> • Reply pointer renders as a structural header above the `<message>` (`<reply-to id="..." sender="..."/>`) — no more buried attribute
> • Self-closing when parent is in session; carries excerpt body when not — no duplicate context
> • Same `id` attribute name on `<reply-to>` and `<message>` for consistency
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Surfaces the reply target where the agent reads it first. Triggered
by sloth/content havoc earlier today: 4-hour session with strong
"writing Reddit posts" task gravity ignored reply pointers buried as
XML attributes. Structural prominence, no new instructions, same
session continuation.

### Changed

- **Inbound XML format** (`router/router.go FormatMessages`):
  `<reply-to id="X" sender="Y"/>` is now a sibling header rendered
  above its `<message>`, not an attribute and not an inline element.
  Self-closing when parent in session window; element body carries
  the excerpt when not. The `<message>` tag now also exposes its
  own `id`, matching `<reply-to>`'s `id` for symmetry. Retired
  `reply_to=` attribute on `<message>` and the inline
  `<reply_to sender="..." id="...">excerpt</reply_to>` element.
- **`ant/CLAUDE.md` ## How messages arrive**: example rewritten to
  show the new shape. Notes the pointer as the user's intent signal.
- **`specs/4/13-message-ids.md` ## Router XML** and
  **`specs/1/N-memory-messages.md`**: spec text + examples track
  the new shape.

## [v0.33.2] — 2026-05-02

> arizuko v0.33.2 — 2 May 2026
>
> • Public changelog page (`/pub/changelog/`) — back-broadcast from v0.33.1
> • Concept-page drift fixes — typed-JID migration numbers, voice platform table
> • Release broadcasts now fire on every version, not just skill-changing ones
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Process release. Closes a gap where docs-only versions never
triggered the chat broadcast. Going forward, every CHANGELOG
version ships with a matching migration file — stub if no skill
changes — and the existing auto-migrate hook drives both skill
updates and announce in one mechanism. Spec
`specs/4/P-personas.md ## Versioning` rewritten to document this;
root `CLAUDE.md` "## Shipping changes" enforces it.

### Changed

- **Spec `specs/4/P-personas.md` ## Versioning**: rewritten to
  require a migration file per release (file name
  `NNN-vX.Y.Z-summary.md`). Stub body fine for docs-only releases.
  Names the single trigger path:
  `gateway.checkMigrationVersion` → `/migrate` → broadcast.
- **Root `CLAUDE.md` ## Shipping changes**: step 2 spells out
  "every release, including docs-only".
- **`ant/skills/self/migrations/CLAUDE.md`**: naming convention
  `NNN-vX.Y.Z-summary.md`; explicit stub example for docs-only
  releases.

## [v0.33.1] — 2026-05-02

> arizuko v0.33.1 — 2 May 2026
>
> • Public changelog page at `/pub/changelog/` — dated, terse, in the chat-broadcast voice
> • Concept-page drift fixes — typed-JID migration numbers, voice platform table, routing example
> • Release-announce format documented in `CLAUDE.md` for authors
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

Docs-only release. Source ships with this tag; existing instances'
`/workspace/web/pub/` is agent-owned and updates only when the root
agent runs `/migrate`.

### Added

- **`/pub/changelog/index.html`** — public-facing release notes
  page in the same onepager voice as the concept pages. Reverse
  chrono, per-release block: H2 with version + date, one-paragraph
  context, 3–6 user-benefit bullets, link to the canonical
  CHANGELOG.md anchor on GitHub. Cross-linked from the root
  onepager's "go deeper" list.
- **Release-announce format spec** in root `CLAUDE.md` ("##
  Announcing"): each CHANGELOG entry's leading `>` blockquote is
  the chat broadcast verbatim — ≤ 9 lines, 3–6 bullets, user
  benefit before internal detail, no migration numbers / file
  paths / SHAs (those stay in `### Added/Fixed`). Mirrored as a
  one-line HTML-comment pointer at the top of `CHANGELOG.md` and
  in `ant/skills/migrate/SKILL.md`'s broadcast example.

### Fixed

- **`concepts/jid.html`**: migration reference was `0038-typed-jids`
  (which is actually `message-turn-id`); corrected to
  `0042-typed-jids` + the `0043-typed-jids-tail` companion that
  picked up `routes.match room=`, `scheduled_tasks.chat_jid`, and
  `chat_reply_state.jid`.
- **`concepts/voice.html`**: platform-support table missing
  LinkedIn and X (Twitter) rows; both now listed as Unsupported
  (matches the structured `chanlib.Unsupported` returns).
- **`concepts/routing.html`**: pre-typed-JID example
  `chat_jid=telegram:12345` retired in favor of the kind-discriminated
  form `chat_jid=telegram:user/12345`.

## [v0.33.0] — 2026-05-02

> arizuko v0.33.0 — 2 May 2026
>
> • Voice replies (`send_voice`) — Telegram/WhatsApp PTT, Discord audio
> • Thread-scoped history (`get_thread` MCP)
> • External agents drive groups via `/slink/<token>/mcp`
> • OAuth account linking + collision UX (`/dash/profile`)
> • Typed JID routing (`telegram:group/*` instead of sign-bit guess)
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md

### Added

- **`send_voice` MCP tool + TTS pipeline**: agent-controlled voice
  output. Gateway resolves the voice (arg > SOUL.md frontmatter >
  `TTS_VOICE` env), POSTs to the OpenAI-compatible
  `/v1/audio/speech` at `TTS_BASE_URL`, caches by
  `sha256(text+voice+model)` under `<data>/tts/`, and dispatches via
  the new `Channel.SendVoice` interface method. Telegram routes via
  `sendVoice` (push-to-talk, distinct from the music-attachment
  `sendAudio` used by `send_file`); WhatsApp via Baileys
  `audio + ptt:true`; Discord via inline audio attachment with
  `audio/ogg` ContentType. Mastodon, Bluesky, Reddit, LinkedIn,
  Email, X return structured Unsupported. New bundled `ttsd` daemon
  (thin OpenAI-compatible proxy with `/health`) defaults to a
  Kokoro-FastAPI backend at `http://kokoro:8880`; operators who
  prefer Piper/Coqui/OpenAI-cloud override `TTS_BACKEND_URL` and
  skip the bundled service. Config: `TTS_ENABLED`, `TTS_BASE_URL`,
  `TTS_VOICE`, `TTS_MODEL`, `TTS_TIMEOUT`. Spec
  `specs/5/T-voice-synthesis.md` is now shipped. Agent migration 088.
- **OAuth account linking + collision UX**: a new
  `auth_users.linked_to_sub` column collapses linked provider subs
  onto a canonical sub at JWT mint time (single resolve point in
  `auth.issueSession`). The OAuth callback fans out to seven cases
  — link-already / link-conflict / link-new / passive collision /
  etc. — and renders a small HTML page with two buttons (link to
  current, or log out and become the other) when the new sub
  doesn't fit silently. `/dash/profile` lists the user's linked
  subs and one "Link account" button per supported provider.
  Migration `0040-auth-users-linked-to-sub.sql`. Agent migration 086.
- **`get_thread` MCP tool**: scoped local-DB query keyed on
  (chat_jid, topic). Sits next to `inspect_messages` (whole chat,
  DB truth) and `fetch_history` (whole chat, platform truth);
  shares the new `store.MessagesByThread` helper with web chat's
  `MessagesByTopic`. Tier-gated like `inspect_messages` — non-root
  callers only see JIDs routed to their folder. Spec
  `specs/5/C-message-mcp.md` is now shipped. Agent migration 084.
- **slink MCP transport** at `POST /slink/<token>/mcp`. External
  agents can register a slink-token-bound URL as a remote MCP
  server in Claude Code (or any MCP client) and call three
  group-scoped tools: `send_message`, `steer`, `get_round`. The
  token is the auth — possessing it = group membership; no JWT,
  no bearer. Streamable HTTP, stateless, served via the same
  `mcp-go` library used by the existing per-instance `/mcp`.
  Shared `injectSlink` helper underneath `handleSlinkPost` and the
  MCP tools. Spec `specs/5/J-sse.md` is now shipped. Agent
  migration 085.

### Changed

- **CHANGELOG entries open with a `>` blockquote summary** in plain
  user language (1-3 lines). `/migrate` skill step (e) now extracts
  ONLY the blockquote for the chat broadcast — the dev sections
  (`### Added/Changed/Fixed`) stay in the file but never reach
  end-users. Convention documented in agent migration 083.
  Backfilled v0.32.0, v0.32.1, v0.32.2.
- **`send_file` media-type dispatch audited per adapter**: telegram
  now also recognizes `.webm` (video), `.m4a`, `.flac` (audio).
  Discord sets per-extension `ContentType` so the rich-media bubble
  renders even when the upstream filename is ambiguous. Bluesky
  splits SendFile by extension — image extensions go through the
  PDS embed upload, non-images return Unsupported (PDS only accepts
  image blobs; agent should host the file elsewhere and send a link
  in post text). Whapd/twitd already dispatched correctly and were
  verified, no change. Mastodon/Reddit/LinkedIn/Email still
  `NoFileSender` — tracked in `bugs.md`.

### Fixed

- **Typed-JID migration tail (`0043-typed-jids-tail.sql`)**: `0042`
  rewrote JID-shaped values in `messages`, `chats`, `user_jids`,
  `grants`, `onboarding`, and the `chat_jid=`/`sender=` predicates
  in `routes.match` — but missed three columns that also hold JIDs:
  `routes.match` `room=` predicates, `scheduled_tasks.chat_jid`, and
  `chat_reply_state.jid`. After `0042` ran, every routed telegram
  chat became unrouted because `JidRoom("telegram:user/<id>")`
  returns `"user/<id>"` while the route still matched on the bare
  ID. The gateway then ran `InsertOnboarding` for each inbound, and
  `onbod` re-prompted operational chats with auth links. `0043`
  rewrites all three columns using the same kind-discriminator
  semantics as `0042` and is idempotent.
- **`IDLE_TIMEOUT` default raised to 60 minutes**
  (`core/config.go`): the previous 30-minute default left short
  setups misreading a hand-edited `IDLE_TIMEOUT=60000` (60 ms) as
  60 seconds and killing containers mid-turn. Operators should
  strip the line from per-instance `.env` rather than carry stale
  values.
- **Agent silent on chat clarification requests** (`ant/CLAUDE.md`):
  when the user asked a clarification-shaped question, the agent
  sometimes called the Claude Code SDK `AskUserQuestion` tool — an
  interactive prompt with no chat fallback — and produced no
  output. CLAUDE.md now explicitly bans `AskUserQuestion`; agents
  ask via `send_message` / `send_reply`. Agent migration 091.

## [v0.32.2] — 2026-04-30

> Cleaner URLs for the new HTTP API your agent now speaks. Plus a
> behind-the-scenes fix so docker rebuilds no longer briefly break
> agent spawning.

URL polish on the slink round-handle protocol shipped in v0.32.0.
The `/turn/` infix and `?steer=` query parameter both disappear;
the second URL segment after the token IS the round handle.

### Changed

- **Round-handle URLs**: drop `/turn/` infix and `?steer=` query
  parameter. The verb does the work — POST to the round URL means
  "extend this round" (steering), GET means "observe":
  ```
  GET  /slink/<token>/<id>            (was /slink/<token>/turn/<id>)
  GET  /slink/<token>/<id>/status     (was .../turn/<id>/status)
  GET  /slink/<token>/<id>/sse        (was .../turn/<id>/sse)
  POST /slink/<token>/<turn_id>       (was POST /slink/<token>?steer=<turn_id>)
  ```
  Cleaner REST shape, fewer special tokens to remember. Spec:
  `specs/1/W-slink.md`. Agent migration 082.
- `arizuko send` CLI URL builder + web docs at `/pub/slink/` and
  `/pub/slink/reference/` updated to the new shape.

## [v0.32.1] — 2026-04-30

> Quiet bugfix: agents in one world can no longer accidentally send
> to chats that belong to other worlds. You'll see fewer duplicate
> notifications during releases.

Closes the cross-world spam vector revealed by v0.32.0's release
broadcast: tier-1 world agents were sending to chats that route to
OTHER worlds' folders, producing the same release notice 2-4× per
chat. Outbound MCP now enforces JID-folder ownership; subtree
containment is the only rule.

### Fixed

- **Outbound MCP authorization**: `send`, `send_file`, `reply`,
  `post`, `like`, `dislike`, `delete`, `edit`, `forward`, `quote`,
  `repost` resolve the JID's owning folder via the `routes` table
  (`store.DefaultFolderForJID`) and check subtree containment via
  `auth.Authorize`. Caller folder must equal target folder or be a
  proper ancestor (target == X or target under X/). No tier bypass —
  even the instance root cannot direct-send to a JID that routes to
  a different world. Inter-world communication uses `delegate_group`
  / `escalate_group`. Unrouted JIDs are denied for every caller.
  Spec: `specs/4/11-auth.md` "Outbound JID authorization." Agent
  migration 081.

## [v0.32.0] — 2026-04-30

> You can now poke your agent over a simple HTTP URL — drop a
> message, watch the reply. Useful for cron jobs, web pages, and
> scripts that need to talk to the agent without a full chat client.
> See [/pub/concepts/slink.html](/pub/concepts/slink.html).

Slink becomes the universal "drop a message, observe the round"
surface. Each agent run is exposed as a first-class object with a
stable handle (`turn_id`), pageable assistant frames, an SSE stream
that closes cleanly on round_done, and steering for chained
follow-ups. New `arizuko send` CLI for server-side use; full public
docs at `/pub/concepts/slink.html`. Rolls up the v0.31.1 follow-on
fixes (crackbox DNS alias, davd healthcheck, migration 079).

### Added

- **Slink round-handle protocol** (`specs/1/W-slink.md`). Each agent
  run is exposed as a first-class object keyed by `turn_id` (= the
  inbound message id). Default `POST /slink/<token>` returns
  `{user, turn_id, status:pending}` synchronously and the round can
  be observed via:
  - `GET /slink/<token>/turn/<id>` — snapshot (status + frames)
  - `GET /slink/<token>/turn/<id>?after=<msg_id>` — cursor paging
  - `GET /slink/<token>/turn/<id>/status` — cheap status check
  - `GET /slink/<token>/turn/<id>/sse` — live stream + `round_done`
    Round_done arrives as an SSE event after `submit_turn`, then the
    stream closes. Cursor uses `messages.id` (same convention as
    `get_history`'s `before`).
- **Steering**: `POST /slink/<token>?steer=<turn_id>` injects a
  follow-up that lands on the in-flight round's topic. Per-folder
  queue serialization makes it the immediate next round; response
  carries `chained_from`. If the round has already finished, the
  steer becomes a fresh round (no `chained_from`).
- `messages.turn_id` (migration 0038) — gateway stamps every
  outbound assistant message with the inbound msg_id that triggered
  the run. Indexed; replaces time-window correlation.
- Gateway → webd `POST /v1/round_done` notification (channel-secret
  authed). Fires from `handleSubmitTurn` only when the round was on
  a `web:` chat. Enables the SSE terminal event.
- **`arizuko send <instance> <folder> "<msg>" [--wait | --stream | --steer <turn_id>]`** —
  operator CLI to inject messages from the server into any group via
  the slink round-handle protocol. Trivial wrapper; reads
  `slink_token` + `WEB_HOST` from instance state, posts, optionally
  polls or streams the round to stdout.
- Public web docs at `/pub/slink/` (landing) +
  `/pub/slink/reference/` (full protocol reference).
- Agent migration 080 documents the round-handle protocol for ants.
- Spec `specs/8/f-replaceability-research.md`: audit each shipped
  homegrown component (crackbox, future messaging-gateway,
  mcp-firewall, gated container orchestration) against off-the-shelf
  alternatives (squid, mitmproxy, envoy, NATS, Anthropic permission
  system). Bias toward replacement.

### Changed

- `core.Channel.Send(jid, text, replyTo, threadID, turnID)` —
  signature widened by one positional `turnID` so adapters can carry
  it through the wire. All three implementations
  (`HTTPChannel`, `LocalChannel`, `FakeChannel`) updated.
- `chanlib.SendRequest` body to webd's `/send` carries optional
  `turn_id`; webd handleSend stamps it on its outbound `core.Message`
  and includes it in the SSE assistant payload so subscribers can
  filter by turn.

### Fixed

- `container/network`: `docker network connect --alias crackbox`
  on per-folder networks. The `crackbox` short-name DNS only resolved
  on Docker Compose's default network; agent containers on per-folder
  networks hit DNS-fail on `HTTPS_PROXY=http://crackbox:3128` and
  hung 30 min until idle timeout. Fixes 1-2 minute reply latency
  observed on krons after v0.31.1.
- `davd`: wrap `sigoden/dufs` in alpine to add a `/health` probe.
  dufs ships distroless (no shell, no wget), so no healthcheck was
  possible. Multi-stage Dockerfile keeps the dufs binary, adds wget +
  ca-certificates + standard `HEALTHCHECK` probing root. Image:
  `arizuko-davd:latest`.

### Removed

- **Group state machinery**: `groups.state`,
  `groups.spawn_ttl_days`, and `groups.archive_closed_days` columns
  dropped (migration 0041). `core.Group.State` field removed.
  `timed.cleanupSpawns` daily loop and tar.gz archival removed.
  `webd /api/groups` `active` field dropped; `dashd /dash/groups`
  state badge dropped. Groups exist until explicitly removed; no
  closure-then-archive cycle. Agent migration 087.

### Typed JIDs

- **JID format**: kind discriminator in the first path segment
  (`telegram:user/<id>`, `telegram:group/<id>`,
  `discord:<guild>/<channel>`, `discord:dm/<channel>`,
  `mastodon:account/<id>`, `reddit:user/<name>`,
  `reddit:subreddit/<name>`, `email:thread/<id>`,
  `email:address/<addr>`, `bluesky:user/<percent-encoded-did>`,
  `linkedin:user/<urn>`, `linkedin:post/<urn>`). Whatsapp and twitter
  already conformed.
- `core.JID`, `core.ChatJID`, `core.UserJID` typed wire-form values
  with `database/sql` Scanner/Valuer + `encoding/json` marshalers
  (wire format stays a string).
- `core.MatchJID(pattern, value)` glob matcher; router uses it
  (`path.Match` — `*` doesn't cross `/`). `chat_jid=telegram:group/*`
  scopes to groups but not DMs.
- Mastodon drops the host segment (single-instance per arizuko
  deployment); re-add when multi-instance lands.
- Discord legacy rows without a stored guild*id migrate to
  `discord:*/<channel>`placeholder. New inbound from discd carries
the real`discord:<guild>/<channel>`.
- Store migration `0042-typed-jids.sql` rewrites every JID-shaped
  value (messages, chats, user_jids, grants, onboarding, routes.match)
  to the new shape. Idempotent. Adapter inbound paths emit canonical
  forms; outbound accepts both legacy and typed during cutover.
  Agent migration 088.

## [v0.31.1] — 2026-04-30

Day-after polish + production-deploy fixes for crackbox. All three
instances (krons, sloth, marinade) now run the same image with
default-deny egress.

### Added

- `crackbox`: transparent-proxy listener on `:3127` (default-on,
  disable via `~/.crackboxrc`). Reads `SO_ORIGINAL_DST`, peeks
  SNI/Host header, runs the same allowlist as the forward path.
- `crackbox`: TOML config file lookup (`--config`,
  `$XDG_CONFIG_HOME/crackbox/crackbox.toml`, `~/.crackboxrc`,
  `/etc/crackbox.toml`). Precedence: flags > env > config > defaults.
- `pkg/match.Host`: bare `*` allowlist entry matches any host.
  Used by tier 0/1 spawns to route through the proxy for logging
  and future secret injection without name filtering.
- `Makefile::smoke INSTANCE=<name>` — post-deploy verification.
- Public web docs at `/pub/crackbox/` (landing) + `/pub/crackbox/reference/`
  (commands, config, threat model, library).

### Changed

- Egress is now controlled by the presence of `CRACKBOX_ADMIN_API`
  env. The `EGRESS_ISOLATION` boolean is gone — set the URL or
  don't. Operators can drop `EGRESS_ISOLATION=true` from their .env.
- Tier 0 (root) and tier 1 (world) bots route through crackbox with
  a `*` wildcard appended to their resolved allowlist. Strict
  filtering applies only to tier 2+ (buildings + rooms).
- `compose/compose.go` now writes `EGRESS_NETWORK_PREFIX` and
  `EGRESS_CRACKBOX` into gated's env file explicitly. No more
  filesystem-path-derivation inside daemons.

### Fixed

- Path-derivation outage: `core/config.go` no longer parses
  `filepath.Base(c.ProjectRoot)` to guess the network prefix.
  Inside a daemon container that yielded `home`; on krons the
  bug caused every `docker network connect` to fail and the
  retry loop replayed every inbound message forever. Compose
  generation owns the project name and writes it into env.
- Retry-loop bound: `gateway.processSenderBatch` advances the
  cursor past a failed batch instead of resetting to the prior
  cursor. A permanent failure no longer blocks the queue.
- Subnet-overlap retry: when `docker network create` reports
  "Pool overlaps with other one on this address space" (orphan
  network from a prior instance name), the allocator retries
  with the next /24 slot up to 8 attempts.
- `EgressCrackbox` was reading `CRACKBOX_CONTAINER` env while
  compose wrote `EGRESS_CRACKBOX`. Renamed to read the right
  one. New regression test pins every `EGRESS_*` env var to its
  config field.
- Admin auth: `CRACKBOX_ADMIN_SECRET` bearer token on `/v1/register`
  and `/v1/unregister` (read-only endpoints stay open). Empty
  secret keeps prior behavior + warns at startup.
- Registry persistence: optional `CRACKBOX_STATE_PATH` writes the
  registry to a JSON file atomically on every Set/Remove. Survives
  container restart.
- `/health` on the admin port now self-tests the proxy listener
  via `net.Dial`. Returns 503 `{"status":"proxy_down"}` if the
  proxy is dead — no more silent admin-green-but-proxy-broken.

### Wisdom

- "Identity is configured, never derived." `CLAUDE.md` and global
  `~/.claude/CLAUDE.md` updated. Don't `filepath.Base()` a runtime
  path to guess project / container / network names.
- "Components stay in single go.mod." Sibling tools live in arizuko's
  monorepo with one `go.mod`; orthogonality is enforced by the
  import graph, not module separation.

## [v0.31.0] — 2026-04-29

Headline: **crackbox** ships as a sibling component with default-deny
network egress, per-folder Docker network isolation (east-west
containment), admin auth on mutating endpoints, on-disk registry
persistence, and a standalone `crackbox run --allow X -- <cmd>` CLI
for use on a developer laptop with no arizuko around. Replaces the
`egred/` prototype shipped earlier the same day. Specs 6/9 + 6/10 +
8/b shipped.

### Added (since v0.30.0 not already covered below)

- `crackbox/pkg/admin`: optional bearer-token auth on `/v1/register`
  and `/v1/unregister` (env `CRACKBOX_ADMIN_SECRET`). Read-only
  `/v1/state` and `/health` stay open. Empty secret keeps the prior
  behavior + warns at startup.
- `crackbox/pkg/admin`: optional registry persistence via JSON file
  (env `CRACKBOX_STATE_PATH`). Atomic `tmp + rename` per mutation;
  corrupt or missing file resets to empty with a warning. Survives
  container restart and `docker compose down/up`. Empty path keeps
  the prior RAM-only behavior.
- `crackbox/pkg/admin /health` performs a TCP self-test against the
  proxy listener and returns 503 `{status:"proxy_down"}` if the proxy
  port is dead. Catches the "admin green, proxy crashed" silent fail.

### Fixed (regressions from the same release cycle)

- `core/config.go`: dropped the auto-derive of egress network prefix /
  crackbox container name from `filepath.Base(c.ProjectRoot)`. Inside
  a daemon container, ProjectRoot is `/srv/app/home`, so derivation
  returned `home` instead of the host's `arizuko_<flavor>`. Compose
  generation (which knows the project name) now writes
  `EGRESS_NETWORK_PREFIX` and `EGRESS_CRACKBOX` into gated's env file
  explicitly. Daemons read env, never parse paths.
- `gateway`: bound the failure-retry loop. `processSenderBatch` on
  failure now advances the cursor past the failed batch; previously it
  reset to the prior cursor, replaying the same broken spawn forever
  whenever any permanent error (e.g. egress register against a
  missing crackbox container) hit.
- `container/network.go`: when `docker network create` returns "Pool
  overlaps with other one on this address space" (orphan from a prior
  instance name on the same /24), the allocator now retries with the
  next slot up to 8 times instead of looping forever.

### Changed

- `container`: per-folder egress network isolation. Each agent's folder
  now gets its own internal Docker network (`<prefix>_<sanitized-folder>`)
  carved as a /24 inside `EGRESS_SUBNET` (default `10.99.0.0/16` -> 256
  /24 slots). Crackbox is the sole shared container, attached to every
  folder network at agent-spawn time via `docker network connect`.
  East-west compromise containment: a hijacked agent in folder A can no
  longer ARP/scan agents in folder B — only crackbox is reachable.
  Networks persist for the instance lifetime; operators can
  `docker network rm <name>` to reclaim slots. New env: `EGRESS_ISOLATION`
  (master switch, replaces the old `EGRESS_NETWORK=<name>` enable
  signal), `EGRESS_NETWORK_PREFIX` (defaults to `<app>_<flavor>`),
  `CRACKBOX_CONTAINER` (defaults to `<app>_crackbox_<flavor>`).
  `EgressConfig.Network`/`Subnet`/`Enabled()` replaced with
  `Enabled bool`/`NetworkPrefix`/`CrackboxContainer`/`ParentSubnet`.
  Compose no longer declares an `agents` network — folder networks are
  runtime-managed by gated.
- `egred/` → `crackbox/` (specs 6/9 + 6/10): the network-isolation
  proxy moves to a sibling component (per `specs/8/b-orthogonal-components.md`)
  with its own CLI, `pkg/proxy`, `pkg/match`, `pkg/admin`, `pkg/run`,
  `pkg/client` layout. No semantic change — daemon-mode wire shape
  (admin API at `:3129`, proxy at `:3128`) stays identical. Adds
  `crackbox run --allow X -- <cmd>` for standalone single-shot use
  on a developer laptop. Image renamed `arizuko-egred` → `crackbox`.
  Container env now points at `http://crackbox:3128`. arizuko's
  `container/egress.go` switches to `crackbox.Client`.

### Added

- `egred`: new daemon — forward HTTP/HTTPS proxy with per-folder
  allowlist for agent containers. Listens on `:3128` for proxy
  traffic (HTTP forward + CONNECT tunnel for HTTPS) and `:3129`
  for register/unregister HTTP API. Allowlist matched by hostname
  on the per-source-IP map. No MITM, no TLS termination. Allowlist
  core (`isAllowed`, `looksLikeDomain`, `looksLikeIP`) ported from
  `crackbox/internal/vm/{proxy,netfilter}.go` including test
  fixtures. (A transparent / iptables REDIRECT design was prototyped
  first and rejected: Docker user-defined bridges put the host on
  the gateway IP, not a container, so egred could not intercept
  packets.)
- `ant`: `proxy-shim.js` — tiny `NODE_OPTIONS=--require` shim that
  wires Node's built-in fetch (undici) to honor `HTTPS_PROXY`. Auto-
  loaded when egress isolation is on. curl/wget/pip/go/npm honor
  the env vars natively.
- `store`: `network_rules` table — per-folder domain/IP allowlist.
  `AddNetworkRule` / `RemoveNetworkRule` / `ListNetworkRules` /
  `AllNetworkRules` / `ResolveAllowlist(folder)` (folder-walk + dedupe).
  Default seed: `anthropic.com`, `api.anthropic.com`. Migration 0037.
- `container`: optional egress isolation via `EgressConfig`. When
  `EGRESS_NETWORK` + `EGRED_API` are set, agent containers spawn on
  the named Docker network and gated registers their IP with egred.
  Allowlist resolved per-folder via `store.ResolveAllowlist`.
- `compose`: emits `egred` service block + `agents` internal network
  when `EGRESS_ISOLATION=true`. CAP_NET_ADMIN granted to egred only.
- `arizuko network <instance> allow|deny|list|resolve` — CLI for
  managing per-folder allowlist rules.

### Fixed

- `queue`: serialize concurrent runs by group folder. Two JIDs that route
  to the same folder (e.g. `telegram:...` from `recoverPendingMessages`
  and `local:atlas` from `checkMigrationVersion` at startup) no longer
  spawn parallel containers. The second JID parks on the waiting list
  and resumes after the first finishes. Eliminates duplicated agent log
  lines (auto-migrate, session init, message turns) at deploy time.

## [v0.30.0] — 2026-04-28

### Added

- `store`: `secrets` table — AES-GCM encrypted folder/user-scoped k=v
  store keyed off `sha256(AUTH_SECRET)`. `SetSecret` / `GetSecret` /
  `ListSecrets` / `DeleteSecret` + `FolderSecretsResolved(folder)` for
  parent-walk last-wins resolution + `UserSecrets(sub)` overlay.
  Migration 0034.
- `container`: folder + user secrets resolved at agent spawn and injected
  into env (both SDK process and MCP server children). Single-user chats
  (`chats.is_group=0`) get `UserSecrets(UserSubByJID(jid))` overlaid;
  group chats get folder-scope only. `gated` opens store via
  `OpenWithSecret(AuthSecret)`; empty `AUTH_SECRET` no-ops gracefully.
- `proxyd`: WebDAV write-block + logs read-only middleware. Writes to
  `.env`, `*.pem`, any `.git` segment → 403; non-read methods on
  `<group>/logs/**` → 403. Reads pass through.
- `webdav`: default-on for new instances (`WEBDAV_ENABLED=true` in
  generated compose). Existing instances pick up on next compose regen.
  Per-group dufs container at `/dav/<group>/`. Concepts page at
  `/pub/concepts/webdav.html`.
- `arizuko chat`: interactive Claude Code session bound to root MCP
  socket. Local-operator only — socket access == root.
- `invites` rewrite (phase B of specs/7/35 tenant self-service):
  token-issuance vs realized-grant separation, atomic `ConsumeInvite`,
  `target_glob` replaces folder so a single token grants a path
  subtree. New CLI `arizuko invite <inst> create|list|revoke`,
  `invite_create` MCP tool (tier 0/1, target inside own world),
  `GET /invite/<token>`. Migration 0032 atomically rewrites the old
  `invitations` table; agent migration 076.
- `chats.is_group` column + per-adapter classification (Discord DM,
  Telegram negative chat.id, Mastodon visibility, Reddit modmail,
  etc.). Predicate `core.Chat.IsSingleUser()` gates user-scope
  secret injection at spawn. Migration 0033.
- `linkd`: LinkedIn adapter (OAuth2, poll + publish, chanlib-based).
  Native `post` (UGC, gated by `LINKEDIN_AUTO_PUBLISH`), `like`,
  `reply` (comments), `repost` (reshare), `delete`. Default port `:9010`.
- `fetch_history` MCP tool — channel-first history fetch with local-DB
  fallback. Native impls on `discd` (Discord messages before-cursor),
  `bskyd` (getAuthorFeed), `reditd` (subreddit `new.json`), `mastd`
  (notifications by account), `linkd` (UGC comments), `emaid` (IMAP
  SEARCH). `teled` and `whapd` return honest unsupported.
- `gateway`: ship 4+1 partial spec gaps —
  - `escalate_group` parent reply now routes back to the child chat
  - `dashd`: `PUT/DELETE /dash/memory/<folder>/<rel>` with path allowlist
  - `gateway`: `/status` auto-reply; `/approve`/`/reject` stubs
  - `container`/`ipc`: `work.md` + `get_work`/`set_work` MCP tools
  - `ipc`: `list_sidecars` + `configure_sidecar`
- `gateway`: mute mode — `SEND_DISABLED_GROUPS` now records outbound to
  `messages` table (visible in dashd / inspect_messages) while skipping
  the platform send. Previously short-circuited before `PutMessage`.
- `auth`: shared identity-sig middleware (`auth.RequireSigned` strict,
  `auth.StripUnsigned` lenient). Replaces duplicated `webd` /
  `onbod` middlewares; same crypto, same header-strip on failure.

### Changed

- `grants`: tier 3+ now derives `[send_reply, send_file]` (file send
  was missing on leaf rooms — agents reported "no file-sending tool
  available" because the tool was absent from MCP surface).
- `reditd`: default poll interval 30s → 5min (configurable via
  `REDDIT_POLL_INTERVAL`); `pollStaleAfter` 3min → 15min to match.
- `router`: glob semantics tightened — `key=*` now requires non-empty
  value, `key=` requires empty, exact/glob/omit unchanged. Operators
  get a clean three-state semantic (Go's `path.Match("*", "")` was
  matching empty).
- `twitd`: JID prefix `x:` → `twitter:` for naming convention parity
  with all other adapters (full platform name).
- `ant`: persona is opt-in. Removed "read SOUL.md, embody its persona"
  from session preamble; `/soul` skill remains for explicit invocation.
  New baseline `Rigor` section in `ant/CLAUDE.md` — cite sources, verify
  numbers, refuse fabrication. Agent migrations 067, 068.
- `ant`: skill `/facts` renamed to `/find`. Agent migration 069.
- `ant`: `@anthropic-ai/claude-agent-sdk` auto-updates to `@latest` on
  every image build (was pinned in `package-lock.json` while the CLI
  was already auto-updating).

### Fixed

- `onbod`: replay-safe onboard link. Token presentation is now
  idempotent (SELECT only); single-shot consume happens at user_sub
  binding in `handleDashboard`. Previously a user clicking the link,
  bouncing through OAuth, and returning would hit "Invalid Link"
  because the token was consumed on first click without binding.
- `onbod`: rows stuck at `status=token_used` + `user_sub IS NULL` after
  30-min cool-down are reset to `awaiting_message` so the user gets a
  fresh link via the original channel. Pairs with the replay fix.
- `chanlib`: adapter re-registers on 401 from `/v1/messages` and retries
  once. When `gated` auto-deregisters a stale channel, the adapter's
  per-channel token becomes invalid; previously delivery looped on 401
  forever (krons logged ~40/min for hours). `RouterClient` now caches
  Register params and re-issues on 401.
- `proxyd`: vhost bare-root preserves trailing slash. `GET /` on a
  custom vhost (e.g. `lore.krons.cx`) was rewritten to `/<world>` (no
  trailing slash) — static upstreams serve `index.html` only at
  `/<world>/`, so bare host returned wrong content.
- `compose`: dufs entrypoint already runs `/bin/dufs`; dropped redundant
  `'dufs'` arg that caused exit 2 → restart loop on `sigoden/dufs:latest`.
- `onbod`: verifies `X-User-Sub` HMAC signature; strips spoofed identity
  headers if `X-User-Sig` invalid. Closes the trust hole flagged by
  TODOs at `handleOnboard` / `handleOnboardPost`.

### Removed

- `container` / `ipc`: per-group MCP sidecar subsystem (unused).

### Added

- Cross-channel identity (spec 5/9). New `identities` /
  `identity_claims` / `identity_codes` tables (migration 0035) link
  multiple platform sender subs to a canonical user. New endpoint
  `POST /auth/link-code` mints a 10-minute, single-shot
  `link-XXXXXXXXXXXX` code; the gateway intercepts inbound messages
  whose body is a bare code and binds the sender sub to the issuer's
  identity (transport-layer auth — never burns an agent turn). New
  read-only MCP tool `inspect_identity(sub)` returns
  `{identity:{id,name,created_at}, subs:[...]}` so agents can
  recognize the same user across channels. Advisory only — agents
  query, never enforce. New CLI: `arizuko identity list | link <sub>
[--name N] [--id ID] | unlink <sub>`. Agent migration 077.
- `submit_turn` JSON-RPC method on the gated MCP socket: agent
  delivers per-turn results (`turn_id`, `session_id`, `status`,
  `result`) over the existing unix socket. Hidden from `tools/list`;
  registered as a raw method handler so the LLM never sees it.
  Idempotent on `(folder, turn_id)` via the new `turn_results`
  table (migration 0036). Replaces the stdout-marker delivery path —
  a single channel for ant→gated where there used to be two. Agent
  migration 078.

### Removed (submit_turn cutover)

- `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---` stdout
  markers. The marker scanner in `container/runner.go` is gone:
  `Run` now spawns and waits for exit, no stdout parsing. Heartbeat
  messages are gone (the marker scanner that needed them is gone).
  `OnOutput` field on `container.Input` is removed; per-turn delivery
  arrives through `submit_turn` instead.

- `twitd`: X / Twitter adapter via browser emulation
  (`agent-twitter-client@0.0.18`, the ai16z fork of
  `@the-convocation/twitter-scraper`). No official Twitter API.
  Bun + TypeScript daemon mirroring `whapd`'s shape. JID prefix `twitter:`
  with `home`, `tweet/<id>`, `dm/<id>`, `user/<handle>` surfaces.
  Native verbs: `send` (DM), `post`, `reply`, `repost`, `quote`,
  `like`, `delete`, `send_file`. Hint-only: `forward`, `dislike`,
  `edit`. Auth via 3 paths in priority order: cookie file
  (`$TWITTER_AUTH_DIR/cookies.json`), username/password env vars,
  `--pair` CLI. Cookies rotate atomically to `cookies.json.bak`.
  Polling loop drains mentions on `TWITTER_POLL_INTERVAL` (default
  90s); cursors persist in `cursors.json`. Risks documented in
  `twitd/README.md`: account suspensions, library churn, 2FA
  challenges, web-side rate limits.

### Changed

- `dislike` outbound is no longer native on `discd`, `teled`, `whapd` —
  emoji reactions are mechanically `like(emoji=...)`. Those adapters now
  return `*UnsupportedError` whose hint redirects to
  `like(target_id=..., emoji="👎")`. Native `dislike` stays on `reditd`
  (Reddit has a true downvote primitive). Inbound emoji classification
  is unchanged. Agent migration 075.

### Added

- `like` / `dislike` are now native on `reditd` (POST /api/vote dir=±1),
  `teled` (Bot API setMessageReaction), and `whapd` (Baileys reactions).
  Previously hint-only on these adapters; now real platform calls.
- Inbound emoji-reaction events emit synthetic `like` / `dislike` verbs
  on `discd` (MessageReactionAdd), `teled` (message_reaction updates,
  Bot API 6.4+), and `whapd` (`messages.reaction` Baileys event). The
  raw emoji is carried on `InboundMsg.Reaction`.
- `chanlib.ClassifyEmoji(emoji)` shared classifier — small explicit
  negative set; everything else (including unknown) → `like`.
- `teled` long-poll loop replaced with a custom `getUpdates` caller
  (allowed_updates includes `message_reaction`) since
  matterbridge/telegram-bot-api v6.5.0 doesn't model the type.

### Changed

- Social-event verb and MCP tool renamed `react` → `like`. Downvote
  counterpart for reddit (future) will be `dislike`.
- Verb taxonomy: MCP tools `send_message` → `send`, `send_reply` →
  `reply` (hard cutover, no aliases). Stored grant rules rewritten by
  migration `0031-grant-renames.sql`. Agent migration 073.

### Added

- Five new MCP tools end-to-end: `forward`, `quote`, `repost`,
  `dislike`, `edit`. Native implementations: forward (Telegram,
  WhatsApp), quote (Bluesky), repost (Mastodon, Bluesky), dislike
  (Discord), edit (Discord, Mastodon, Telegram, WhatsApp). Adapters
  without native primitives return a structured `*UnsupportedError`
  with a per-(tool, platform) hint pointing at a concrete alternative,
  so the agent learns instead of dead-ending.
- `chanlib.UnsupportedError{Tool, Platform, Hint}` — typed unsupported
  error. `errors.Is(err, chanlib.ErrUnsupported)` chains so existing
  call sites are unaffected. Adapter HTTP 501 carries the hint as JSON
  body; chanreg decodes; ipc renders as `unsupported: <tool> on
<platform>\nhint: <alt>` in the tool result.
- `grants`: `platformChatActions` (`forward`) split from
  `platformFeedActions` (`post`, `quote`, `repost`, `like`, `dislike`,
  `delete`, `edit`). Tier 3+ default gains `edit` so leaf rooms can
  correct their own messages: `{reply, send_file, like, edit}`.

### Added

- `ipc`: `post` / `like` / `delete` MCP tools — wire adapter-level
  implementations to the agent MCP surface. Platform-scoped grants derived
  per tier; `like` available at tier 3 (reply-adjacent).
- `chanlib`: channel-level liveness. Adapter `/health` now flips 503
  `{status:"stale"}` when no inbound message has been successfully
  delivered to the router within the staleness threshold (5m realtime,
  10m email), catching "connected but not flowing" breakages (e.g.
  whapd Baileys socket open but messages.upsert silent). Response
  includes `last_inbound_at` (unix seconds) and `stale_seconds` when
  stale. Order: disconnected > stale > ok.
- `ipc`: read-only `inspect_*` MCP family — `inspect_routing`,
  `inspect_tasks`, `inspect_session`. Tier 0 sees all instances, tier
  ≥1 scoped to own folder subtree. Replaces ad-hoc `Bash sqlite3 …`
  introspection. `inspect_logs`/`inspect_health` deferred — need
  journal/docker-socket the agent container doesn't have.
  See `specs/7/33-inspect-tools.md`.

### Changed

- `gateway`: inline `<autocalls>` block replaces `<clock/>` at the top
  of every prompt. Resolves `now`, `instance`, `folder`, `tier`,
  `session` at prompt-build time. Zero-arg read-only facts now cost one
  line each instead of paying per-turn MCP schema. `router.ClockXml`
  deleted. Registry is a flat slice in `gateway/autocalls.go`; empty
  eval output skips the line. See `specs/5/31-autocalls.md` and
  `EXTENDING.md` "Adding an autocall".

### Fixed

- `chanlib`: adapter `/health` now requires platform-connected, not
  just process-up. `NewAdapterMux` takes a required `isConnected
func() bool`; `/health` returns 503 `{status:"disconnected"}` when
  the platform link is down (whapd showing QR, mastd stream dropped,
  …). Docker `HEALTHCHECK` flips the container `(unhealthy)`
  automatically. All Go adapters + whapd updated.
- `grants`: tier-1 now hardcodes `send_message`/`send_file`/`send_reply`.
  Production routes store `room=X` without a `platform=` key, so
  `platformRules` returned empty and tier-1 agents had no send rules.
  Tier-2 got the same fix on the same day.
- `compact-memories` skill: recognizes XML-wrapped telegram messages
  (`<messages><message ...>`) as real user activity. Previous heuristic
  discarded them along with tool-result turns, producing false "no
  user activity" summaries.
- `gateway`: `errored` is now message-level, not chat-level. Previously
  a single crash set `chats.errored=1`, silencing the whole chat until
  a manual sqlite clear. Now `store.MarkMessagesErrored` tags the
  failing batch (`messages.errored=1`); the rows are re-fed to the
  agent next poll with `errored="true"` in the prompt so it can try
  differently. After 3 consecutive chat-level failures the circuit
  breaker (`gateway.onCircuitBreakerOpen`) calls
  `store.DeleteErroredMessages` + resets the session — no permanent
  quarantine. `chats.errored` column dropped (migration 0030).

### Testing

- Per-daemon integration tests landed for all daemons (gated, container,
  timed, onbod, dashd, webd, proxyd, teled, discd, mastd, bskyd, reditd,
  emaid, linkd, whapd) plus MCP socket round-trip.
- New `tests/testutils` package with `FakeChannel`, `FakePlatform`,
  `NewInstance` helpers.
- `container.Runner` interface extracted for test injection; `run_test.go`
  covers docker arg assembly and marker parsing.
- `gateway/integration_test.go` exercises poll loop + runner contract.
- `emaid`: SMTP send happy-path via injectable sender.
- `whapd`: vitest integration test for send handler.

### Changed

- **chanlib**: absorbed cross-package primitives. Single `URLCache` (12-hex
  LRU, cap 4096) replaces three divergent private `fileCache` impls in
  discd/mastd/reditd. `CopyDirNoSymlinks`+`CopyFile` (fsutil) replace
  duplicated copies in container + gateway (io.Copy path wins). `EnvInt`,
  `EnvDur` join existing `EnvOr`; core no longer carries its own copies.
  `ShortHash` replaces identical 4-byte sha256 log tags in onbod + webd.
- **mastd/reditd**: deduped message conversion. mastd `handleNotification`
  now calls `notificationToMsg`; reditd extracted `thingToMsg` shared by
  `handleThing` (poll) and `FetchHistory` (backfill). ~75 lines removed.
- **bskyd**: dropped no-op `oldestInPage` branch in `FetchHistory`; fixed
  staticcheck lints (numeric 401 → `http.StatusUnauthorized`,
  `t.Sub(time.Now())` → `time.Until`).

### Removed

- **store**: `ConsumeInvitation` (dead — onbod has its own atomic consume).
- **webd**: unused `authSecret` + `trustedProxies` config fields.

### Fixed

- **onbod**: `genToken` silently discarded `crypto/rand` errors; now panics
  on RNG failure (matches `core.GenSlinkToken`). A zero-entropy token
  would be a guessable credential.

## [v0.29.4] — 2026-04-19

### Changed

- **ipc**: replace the (disabled) token preamble with kernel-attested
  `SO_PEERCRED` on every MCP connection. `ServeMCP` takes an
  `expectedUID int` parameter (1000 = ant image's `node` user in prod,
  host uid when `--user` override fires in dev) and rejects any peer
  whose kernel-reported uid doesn't match. No client changes needed —
  standard MCP, unchanged socat bridge. Removed dead code:
  `GenerateRuntimeToken`, `verifyToken`, `McpToken` field,
  `ARIZUKO_MCP_TOKEN` env stamp. Security boundary remains per-group
  mount isolation (`buildMounts` bind-mounts only the group's own
  `ipcDir` → `/workspace/ipc`, validated by `folders.IpcPath`);
  peer-uid check is a cheap sanity gate, not the boundary.

## [v0.29.3] — 2026-04-19

### Fixed

- **ipc**: MCP token preamble enforcement disabled. Ant's socat bridge
  (`/ant/src/index.ts`, settings.json mcpServers.arizuko) connects to
  the unix socket and sends MCP JSON-RPC directly — it never wrote the
  `{"token":"<hex>"}\n` preamble that `ipc.ServeMCP` required, so every
  MCP connection was rejected and every `get_history`/`get_facts`/any
  gateway tool failed silently. Symptom: agents replied "nemám záznam"
  / "no context" because `get_history` was unreachable. Fix: pass empty
  token to `ServeMCP`; socket is already 0660 + chowned to container
  uid, filesystem perms are the real isolation boundary.

## [v0.29.2] — 2026-04-17

### Added

- **webd/mcp**: single JWT-gated MCP streamable-HTTP endpoint at `/mcp`.
  One endpoint per instance — the authed user can reach any folder in
  their `user_groups` ACL via `folder` arguments on each tool. Three
  tools: `list_groups` (filtered by grants), `send_message` (stamps
  authed sub/name), `get_history` (topic-scoped). No anonymous MCP.
- **webd/slink**: `POST /slink/<token>` with `Accept: text/event-stream`
  holds the connection open and streams user bubble + subsequent
  assistant responses on the same (folder, topic). Callers can now
  submit and receive on one request instead of POST + separate
  `/slink/stream` SSE.
- **webd/slink**: REST variant — `POST /slink/<token>` with
  `Accept: application/json` returns `{user: {...}}`. Add `?wait=<sec>`
  (1-120) to block until the first assistant reply, returning
  `{user, assistant}`. Enables scriptable curl-style usage without SSE
  plumbing.
- **container**: new env vars for bot identity — `ARIZUKO_GROUP_FOLDER`,
  `ARIZUKO_GROUP_NAME`, `ARIZUKO_GROUP_PARENT`, `ARIZUKO_WORLD` (tier-1
  top-level folder), `ARIZUKO_TIER` (0 root, 1 world, 2 building,
  3+ room). Hello/howto skills use these for in-persona greetings.
- **ant/skills/soul**: user-initiated `/soul` brainstorming skill that
  writes `~/SOUL.md`. Hello + howto read SOUL.md when present and
  inject tagline + persona into output.

### Changed

- **auth/store**: operator is implicit — emergent from grants, not a
  nil-sentinel. `store.UserGroups` now returns plain `[]string`
  (previously `*[]string` with nil = operator). `Claims.Groups` and
  `setUserHeaders` follow suit: always a slice, `**` inside it is the
  only operator signal. `auth.MatchGroups` handles `**` uniformly, so
  downstream gates (davRoute, webd.requireFolder) no longer need a
  "missing header = operator" special case. Less indirection, one
  code path.
- **webd/hub**: `serveSSE` flushes headers immediately on connect so
  plain net/http clients return from `Do` without waiting for the
  first event. Logging middleware's `statusWriter` now passes through
  `http.Flusher`.
- **compose/daemons**: unified internal listen port on `:8080` for
  every daemon (gated, webd, dashd, onbod, proxyd, vited). Host-side
  publish ports (`API_PORT`, `WEB_PORT`, `DASH_PORT`, `DAV_PORT`) map
  to container `:8080`. Peer URLs (`WEBD_URL`, `ROUTER_URL`,
  `DASH_ADDR`, `WEBD_ADDR`, `VITE_ADDR`) default in code to
  `http://<service>:8080` — compose no longer sets them. `proxyd`
  now reads `PROXYD_LISTEN` (default `:8080`) instead of `WEB_PORT`
  for its container-internal listen.
- **compose/env**: every arizuko daemon gets `env_file: ['.env']`.
  Shared config and secrets flow implicitly from the instance `.env`;
  per-service `environment:` blocks now hold only compose-side
  overrides (container paths, `TIMEZONE` transform, `API_PORT=8080`
  pin, feature-gated `DAV_ADDR`/`ONBOD_ADDR`). Eliminates the
  per-service env whitelists. Adapter TOMLs also pick this up.
- **template/services**: adapter TOMLs use literal
  `ROUTER_URL = "http://gated:8080"` — `${API_PORT}` interpolation is
  no longer correct since gated's container-internal port is fixed.
  Deployments with customized services/_.toml must be updated
  manually (e.g. `sed -i 's|:${API_PORT}|:8080|' services/_.toml`).

### Fixed

- **auth/proxyd**: new OAuth users with no groups now land on `/onboard`
  instead of `/`; unauthenticated requests to deep links (`/chat/X`,
  `/dash/Y`) preserve the original path through login via `auth_return`
  cookie (10-min TTL).
- **healthchecks**: unified all daemons on internal `:8080/health`.
  `dashd` compose now pins internal `DASH_PORT=8080` (host publish still
  uses `.env`); `timed` now runs a small HTTP server exposing `/health`
  (db.Ping); `onbod` registers `/health`; `vited` probes `/@vite/client`
  (Vite has no `/health`); `whapd` Dockerfile healthcheck moved from
  `:9002` to `:8080`; adapter service TOMLs (`teled`, `discd`, `mastd`,
  `bskyd`, `reditd`, `whapd`) set `LISTEN_ADDR=:8080`. Eliminates the
  mass of `(unhealthy)` containers from the port-unification migration.
- **webd (mobile)**: chat page now viewport-aware — `100dvh`, safe-area
  padding on footer, larger touch targets (44px send, 36px header
  buttons), 16px textarea font (prevents iOS zoom), `enterkeyhint=send`,
  hidden tagline <640px, bubble max-width 88% on phones.

### Ops

- **Makefile**: `make images` now runs `docker image prune -af` first
  to reclaim dangling layers (prevents disk-full on hosts with a ~100G
  root during successive agent-image rebuilds).
- **tests**: coverage expansion — `teled` →58.5%, `discd` →70.5%,
  `mastd` →69.5%, `bskyd` →84.2%, `timed` →72.1%, `dashd`, `onbod`.
  Integration tests drive inbound/outbound paths against in-process
  httptest mocks per platform.

## [v0.29.1] — 2026-04-16

### Changed

- **ant/resolve**: classification section headings (`## Classify`,
  `Continuation —`, `New task —`) are internal only — never emitted
  to the user. Fixes scaffolding leak observed on marinade Apr 16.
- **ant/compact-memories**: episodes now preserve user corrections
  verbatim rather than agent-drawn conclusions. Conclusions get
  redrawn fresh each recall; corrections don't.
- **ant/recall-memories**: weight corrections over conclusions.
  Never reuse a prior agent summary as a fact.
- **ant/migrate**: `~/.announced-version` is written BEFORE the
  broadcast loop, not after. Prevents a mid-fanout container restart
  from re-announcing the whole release. Also: fix broken
  `refresh_groups | jq .jid` (MCP tool returns folder, not jid) by
  looking up JIDs from the `routes` table.
- **ant/CLAUDE.md**: attachment rule — `[Document: …]` placeholder
  without `<attachment path=…>` tag means the file did NOT arrive.
  Do not claim you read it.

### Fixed

- **store.UserGroups**: correctness — only `**` marks operator (was
  checking `*`). Aligns with spec and CLAUDE.md.
- **onbod.userGroups**: same — drop `*` branch, align on `**`.

## [v0.29.0] — 2026-04-16

### Added

- **cli**: `arizuko group <instance> grant|ungrant|grants` — manage
  `user_groups` ACL rows from the host CLI instead of hand-editing
  SQLite. `grant <sub> <pattern>` is idempotent, `grants [<sub>]`
  prints an aligned table, `ungrant` reports zero rows cleanly.
  Migration `0026-user-groups-granted-at.sql` adds a nullable
  `granted_at` timestamp column.
- **auth**: `MatchGroups(allowed, folder)` helper for glob-matched ACL
  (`auth/acl.go`). `**` matches anything; otherwise `path.Match`
  semantics. Shared by `onbod` route-creation guard and `proxyd.davRoute`.
- **onbod**: second-JID auto-link. When a user who already has a world
  messages from a new platform, the dashboard handler auto-routes the
  new JID into the existing folder and skips the username picker.
- **ant**: `/migrate` now broadcasts new releases — after migrations
  apply, root agent fans out the latest CHANGELOG entry to every
  registered group via `send_message`. Per-group `~/.announced-version`
  prevents re-broadcast. Manual fan-out until the automatic db_utils-based
  announcement path (`specs/3/e-migration-announce.md`) is implemented.

### Changed

- **proxyd**: `davRoute` uses `auth.MatchGroups` for folder
  authorization (was a dumb prefix check). Missing `X-User-Groups`
  header still means operator (unrestricted).
- **onbod**: `handleCreateWorld` gates route INSERTs behind
  `auth.MatchGroups` against the user's `user_groups` entries.
- **groupfolder**: `*` and `**` are now reserved folder names so they
  cannot collide with ACL glob patterns.
- **db_utils**: renamed from `dbmig/` to `db_utils/` (matches the
  `*_utils` convention). Unified schema ownership: `gated` (via `store/`)
  owns the shared DB schema; `timed` and `onbod` connect to the
  already-migrated DB and no longer carry their own migrations.
- **store**: now uses `db_utils.Run` instead of a duplicated inline
  migration runner; exposes `store.Migrate(db)` for tests that need a
  schema'd fixture.

### Removed

- **timed**: `timed/migrations/` (redundant — store creates the same
  tables) and the migration runner in `timed/main.go`.
- **auth**: `auth/migrations/` (dead code — never loaded; tables live
  in store migrations).

### Fixed

- **queue**: remove duplicate error notification — gateway's
  `Failed: ...` message is now the single error surface. Queue-side
  `notifyError` was firing a second message for every failure.

## [v0.28.0] — 2026-04-15

### Added

- **auth**: ACL flip — no user_groups rows = no access, `*` = operator
- **onbod**: token-based web onboarding — chat sends auth link, user picks username on web dashboard
- **store**: user_jids table with unique JID constraint, migration 0024-0025

### Fixed

- **onbod**: XSS escaping on all user-controlled HTML output
- **onbod**: token consumed on first use (prevents replay attacks)
- **onbod**: JID uniqueness enforced (prevents hijacking)
- **ant**: bare-URL rule in output styles, dashboard vs docs clarification
- **ant**: 13 skills synced from host (aligned versions)

### Removed

- **onbod**: dead approve/reject chat flow, registerSelf, onboarding.channel column

## [v0.27.0] — 2026-04-15

### Added

- **webd**: web chat (slink) integrated into compose, auto-generate slink tokens
- **agent**: Python 3.14 via uv, uv/uvx instructions in CLAUDE.md and python skill
- **agent**: web routing table, auth flow, gateway commands in CLAUDE.md
- **skills**: web chat (slink) documentation in howto and web skills
- **proxyd**: unknown paths redirect to /pub/ prefix (public fallback)
- **eval**: checks 17-20 for skill seeding, dispatch discovery, consistency, resolve wiring
- **specs**: user-centric identity model (28), local CLI (29)

### Fixed

- **ant**: drain stale IPC nudges after query to prevent duplicate responses
- **ant**: only discard self-generated nudges, preserve gateway steers
- **ant**: progress nudge thresholds raised (200→500 msgs, 10→15 min)
- **ant**: prevent agents from self-creating or modifying SOUL.md without sign-off
- **proxyd**: /pub/\* always routes to vite (was broken when webd upstream set)
- **gateway**: replace groupForJid with per-message resolveGroup
- **gateway**: mark unrouted JIDs errored to stop drain loop
- **gateway**: skip unrouted JIDs in recoverPendingMessages
- **gateway**: filter silent refusal outputs (No response requested, etc)
- **gateway**: replace silent refusal regex with \<think\> block approach
- **skills**: compact-memories date-filters transcripts, globs all project dirs
- **skills**: web skill enforces /pub/ only, documents all proxyd routes
- **whapd**: Dockerfile COPY paths for repo-root build context

## [v0.26.1] — 2026-04-13

### Fixed

- **gateway: cursor advances once on delivery, not twice**: steer path
  records timestamps in `steeredTs` map; `advanceAgentCursor` merges
  `max(batch, steered)` into a single cursor write on container completion
- **gateway: auto-migrate message starts with /migrate**: ensures the
  `/migrate` skill dispatches correctly instead of being treated as plain text
- **ant: silent means silent**: agents produce no output when declining
  to respond, preventing empty reply messages
- **container: WEB_PREFIX uses full folder path**: `pub/<world>/<child>`
  matching the actual web route hierarchy
- **store: PendingChatJIDs SQL**: replaced brute-force 7-platform recovery
  loop with single SQL query (-180 lines)
- **hello skill**: rewritten to lead with use cases, not mechanics

## [v0.26.0] — 2026-04-13

### Added

- **auto-migrate on startup**: gateway checks agent MIGRATION_VERSION on
  boot, injects `/migrate` system message to outdated root groups
- **`/resolve` skill**: gateway-nudged task classification, context recall,
  and skill dispatch on every prompt (replaces /dispatch which had 0%
  compliance). Migration 054 cleans stale dispatch refs
- **`/root` gateway command**: delegates messages to instance root group
  with grants-based auth (tier <= 1)
- **migration 055 — bookkeeping cron tasks**: seeds the 5 compact-memories
  scheduled tasks for groups missing them
- **mcpc in agent image**: `@apify/mcpc` MCP CLI for ad-hoc scripts to
  call MCP tools. Migration 052 documents usage
- **agent-generated status messages**: `<status>` blocks in agent output
  are stripped and delivered as interim progress messages via IPC nudge
  (replaces generic heartbeat)
- **session inactivity reset**: sessions idle >2 days reset to fresh
  instead of resuming stale context
- **fact staleness check**: `/resolve` recall checks `verified_at` on
  facts, triggers re-research if stale (>14 days)
- **skill 'Use when' triggers**: 9 skill descriptions now include explicit
  trigger conditions for better semantic dispatch matching
- **agent container tools**: xh, websocat, hurl, age, sops added to image
- **eval skill**: episodic + knowledge memory checks across all instances

### Changed

- **`/research` skill renamed to `/hub`**: avoids shadowing Claude's
  built-in `research` tool. Migration 053 cleans stale overlays
- **gated runs as uid 1000**: no longer runs as root inside the container
- **IPC user-message drain**: moved from poll timer to PostToolUse hook
  for lower-latency mid-loop message injection

### Fixed

- **gateway: auto-migrate recovery bugs**: mount path mismatch
  (/srv/app/arizuko vs HostAppDir), parent field check for root detection,
  recoverPendingMessages LIMIT bug (replaced with route-based per-chat check)
- **gateway: @prefix router dropped messages with @handles**: anchored
  prefix regexes to start-of-message; fall through on non-existent child
- **gateway: strip @botname suffix from Telegram commands**: `/new@botname`
  was silently ignored
- **gateway: late-bind channel in makeOutputCallback**: fixes race when
  channel connects after container starts
- **gateway: chown .git to 1000:1000 after git init**: fixes permission
  errors when gated runs as uid 1000
- **whapd: typing indicator across long runs**: ported TypingRefresher
  (15s refresh, 10min cap), closes last adapter typing gap
- **whapd: extract reply metadata from Baileys contextInfo**: inbound
  replies now populate reply_to/reply_to_text/reply_to_sender
- **typing: call clear on maxTTL expiry**: indicator would not stop after
  10min runs in both Go and TS adapters
- **typing: log/validate silent failures** in handler, discd, httpchan
- **store: write sticky-resolved topic on inbound messages**
- **dashd: resolve symlinks in renderMemorySection**
- **compose: add host.docker.internal extra_hosts to gated**
- **ipc: purge legacy file-based IPC dirs**
- **ant: raise progress count threshold from 100 to 200 messages**

### Removed

- **arizuko-mcp CLI**: custom Python MCP client replaced by mcpc
- **backwards-compat shims and band-aid fixups**: dead code cleanup
- **`arizuko create` chownTree and subdir mkdirs**: unnecessary post-seed ops
- **Makefile: dead per-daemon Dockerfile build targets**

## [v0.25.1] — 2026-04-09

### Fixed

- **gateway**: `pollOnce` steering branch now advances `agentCursor` for the
  full steered batch via `SendMessages`. Previously the cursor was left
  behind, so after the container exited `drainGroupLocked` saw the same
  rows as unprocessed and respawned a new container on the same inputs
  (duplicate delivery). Success is now logged at Info:
  `"poll: steered messages into running container" count=N`.

### Changed

- **ant**: true mid-loop steering via a `PostToolUse` hook
  (`createIpcDrainHook`) wired into `query()` options. The hook drains the
  IPC input dir between tool calls and returns queued messages as
  `hookSpecificOutput.additionalContext`, appended to the tool result
  Claude is about to read — injecting follow-ups inside the active
  agentic loop instead of waiting for the next turn. `pollIpcDuringQuery`
  remains as a `stream.push` fallback for text-only turns. A
  `drainIpcInputMutex` flag shared with the poll timer prevents
  double-draining the same files.
- **queue**: `SendMessage(jid, text)` renamed to
  `SendMessages(jid, []string)`. Loops `writeIpcFile` per message and
  signals the container once per batch. Success log at Info level.
- **store**: `messages.source` is now the canonical adapter-of-record per
  message. Inbound messages stamp the receiving adapter; outbound delivery
  resolves the adapter via `store.LatestSource(jid)` (latest non-bot inbound).
  Replaces three stacked layers of channel-pin hotfixes.
- **api/handleMessage**: stamps `messages.source` with the registered adapter
  name on every inbound delivery (was previously a no-op write of `''`).
- **api/handleOutbound**: resolution order is now (1) explicit `channel`
  field, (2) `LatestSource(jid)`, (3) `chanreg.ForJID(jid)`.
- **onbod**: dropped per-onboarding `channel` pin — outbound delivery uses
  `/v1/outbound`'s `LatestSource` lookup instead of explicit channel routing.
- **whapd**: dropped `@lid` -> phone-number translation entirely. Baileys
  removed `makeInMemoryStore` so the contacts/LID discovery paths no longer
  worked; arizuko now treats `@lid` as the canonical opaque WhatsApp identifier.
  Removed `whapd/src/lid.ts` and the dead `normalizeJID` helper in `ipc/`.
- **specs**: scrubbed stale routes/outbound/delegation references, updated
  WhatsApp examples to `@lid` form, and rewrote audit-log + JID-format +
  worlds-rooms specs to reflect post-0023 schema and source semantics.

### Removed

- **store schema (migration 0023)**: dropped dead columns —
  - `chats.name`, `chats.channel`, `chats.is_group`, `chats.last_message_time`
    (chats now `(jid, errored, agent_cursor, sticky_group, sticky_topic)`)
  - `messages.group_folder` (only ever written, never read)
  - `onboarding.sender`, `onboarding.world_name`, `onboarding.channel`
    (onboarding now `(jid, status, prompted_at, created)`)
- **store**: `Store.PutChat` removed — chats rows are no longer pre-created
  per message. `MarkChatErrored` now upserts.

---

## [v0.25.0] — 2026-04-08

### Changed

- **routes**: collapsed routes table to `(id, seq, match, target, impulse_config)` —
  dropped `jid` and `type` columns. Replaced route types (command/verb/pattern/
  keyword/sender/prefix/default) with a single match expression language:
  space-separated `key=glob` pairs over platform/room/chat_jid/sender/verb,
  using Go `path.Match` globs. Empty match = wildcard. See specs/1/F-group-routing.md.
- **gateway**: three-layer pipeline — sticky → command → prefix → routing. Only
  the routing layer reads the routes table; commands and prefixes are in-code.
- **gateway**: `pollOnce` no longer pre-filters by registered JIDs — fetches all
  new messages, resolves each via `DefaultFolderForJID` (routes table). The old
  `RegisteredChatJIDs`/`RegisteredRooms` helpers are removed.
- **gateway**: unified inbound/outbound message paths — agent output now written
  to messages table via `PutMessage` instead of separate `StoreOutbound`.
- **gateway**: delegation is now message-based — `delegateViaMessage` writes to
  `local:targetFolder` with `forwarded_from` as return address. No more closures.
- **gateway**: `#topic` prefix route converted from `EnqueueTask` closure to
  `PutMessage` + `EnqueueMessageCheck`.
- **ipc**: `delegate_group` and `escalate_group` write messages to DB directly
  instead of calling gateway closures.
- **ipc**: `send_message`/`send_reply` record output via `PutMessage` to unified
  messages table.
- **queue**: removed `EnqueueTask`, `taskFn`, `queuedTask`, `runTask` — all work
  flows through messages, no more task closures.

### Removed

- **core**: `OutboundEntry` type.
- **store**: `StoreOutbound()` function.
- **ipc**: `DelegateToChild`/`DelegateToParent` from `GatedFns`.
- **ipc**: `StoreOutbound` from `StoreFns`.

---

## [v0.24.2] — 2026-04-07

### Fixed

- **ant**: output style (e.g. telegram.md) now injected into custom system
  prompts — SDK only injects it for the preset prompt, which we don't use.
- **ant**: progress interval 5min → 10min to reduce noise.

### Changed

- **queue**: DB-implicit pending — removed in-memory `pendingMessages` boolean,
  drain now queries `store.HasPendingMessages` via callback. SendMessage race
  fixed (single lock).
- **container**: hard deadline 30min → 60min; soft deadline warns agent 2min
  before kill via IPC message + SIGUSR1.
- **ant**: time-based progress updates — agent sends status every 10min or 100
  SDK messages, whichever first.

---

## [v0.24.1] — 2026-04-07

### Fixed

- **teled**: use `Request` instead of `Send` for `sendChatAction` — Telegram
  returns `bool`, not `Message`. Was spamming WARN every 4s on every active chat.
- **migration**: onboarding simplification migration now skips JIDs that already
  have routes, preventing re-onboarding of existing users.

### Changed

- **ant**: PreCompact hook names vital files (SOUL.md, CLAUDE.md, diary/, facts/,
  users/) by reference so the summarizer preserves them across compaction.

---

## [v0.24.0] — 2026-04-06

### Fixed

- **gateway**: reply-to always targets triggering user message, not stale bot
  reply from previous run. Steering messages (follow-ups mid-run) now update
  reply-to target so subsequent bot chunks reply to the latest user message.
- **file send**: preserve original filename through temp dir + chanreg multipart
  header. MCP `send_file` tool description updated — caption IS the message.
- **teled**: log typing API errors at warn level instead of silently swallowing.
- **whapd**: resolve LID JIDs via `WHATSAPP_LID_MAP` env + `onWhatsApp` fallback.
- **chanreg**: `CreateFormFile` uses `name` param when non-empty instead of
  `filepath.Base(path)` which sent temp filenames.

### Changed

- **onbod**: simplified from 4-step hierarchical flow to 2-step — greeting +
  leave message, admin picks folder on `/approve <jid> <folder>`.
  `ONBOARDING_GREETING` env var added to compose + all instance configs.
- **gateway**: output callback takes channel directly, eliminating duplicate
  `findChannel` per output chunk. `replyTo` is a local var, no DB read per
  chunk. `SetLastReplyID` persists for IPC consumers only.
- **emaid**: re-fetch attachments from IMAP on demand, no local storage.
- **adapters**: all 6 channels now have consistent file proxy (`GET /files/{id}`
  auth-gated) and inbound media extraction.

### Refactored

- **core**: `MsgID(prefix)` utility replaces 9 scattered `fmt.Sprintf` ID
  generation patterns across 8 packages.
- **groupfolder**: `IpcInputDir`, `IpcSocket`, `IpcSidecars`, `GroupMediaDir`
  helpers replace hardcoded path joins across container/queue/gateway.

### Docs

- Updated stale onboarding refs in ARCHITECTURE.md, specs/4/21-onboarding.md,
  docs/arizuko.html. Routing guide added to docs/routing.md. History backfill
  spec added.

---

## [v0.23.2] — 2026-04-06

### Changed

- **onbod**: redesigned onboarding from single "pick a workspace" to 4-step
  hierarchical flow: pick a world → pick a house → pick a room → leave a
  message for the admin. Name steps validate `[a-z0-9_-]`, message step
  accepts any text. `approveInTx` auto-creates parent groups in the
  hierarchy.
- **container**: unified group filesystem setup into `container.SetupGroup`.
  CLI, IPC, and onbod all use one function for mkdir + prototype copy + seed.
  Removed duplicate `copyDir` from onbod, dead `seedGroupDir` helper from CLI.

---

## [v0.23.1] — 2026-04-06

### Fixed

- **gateway**: canonical file extensions for inbound attachments — pin `.jpg`,
  `.png`, `.gif`, `.webp`, `.ogg`, `.mp3`, `.m4a`, `.mp4` so agents can read
  photos regardless of OS mime table (`mime.ExtensionsByType` returned `.jfif`
  on Debian, breaking Claude Read).
- **gateway**: single-write agent output — `makeOutputCallback` no longer
  dual-writes `unsent-*` + `out-unsent-*` rows; `StoreOutbound` now carries
  `topic` + `routed_to` so `MessagesByTopic` finds bot replies.
- **proxyd**: forward WebSocket upgrades on `/` to vited instead of 302
  redirecting to `/pub/` — Vite HMR client opens `wss://host/` for live reload.
- **vite**: enable `watch.usePolling` (docker bind-mount inotify unreliable)
  and `hmr.clientPort: 443, protocol: 'wss'` for proxy setup.
- **gateway**: onboarding no longer triggers for JIDs that already have a
  default route in the DB.

### Changed

- **gateway**: removed all 4 in-memory DB caches (`groups`, `jidToFolder`,
  `jidAdapters`, `agentCursors`). All lookups now query SQLite directly.
  Route/group changes take effect on next poll cycle without restart. -99 LOC.
- **ant**: added tenancy model section to CLAUDE.md — tier 0 root, tier 1
  world, tier 2 building, tier 3 room, isolation boundaries, threads.

---

## [v0.23.0] — 2026-04-05

### Fixed

- **store**: `StoreOutbound` now sets `is_bot_message=1`. Commit 6cf0f10 added
  `sender='bot'` but left the flag at 0, so `MessagesSince`'s
  `is_bot_message=0 AND sender NOT LIKE botName%` filter kept matching the
  agent's own output — every agent reply was re-ingested as inbound, producing
  a self-reply loop across REDACTED and REDACTED. 71 poisoned rows repaired in-place.
  Regression test `TestStoreOutbound_ExcludedFromMessagesSince` pins the fix.
- **gateway**: `downloadFile` now sends `Authorization: Bearer <CHANNEL_SECRET>`
  when fetching attachments. teled `/files/` is wrapped in `chanlib.Auth`, so
  unauthenticated downloads 401'd silently; `enrichAttachments` swallowed the
  error and the agent received the raw `[Document: …]` placeholder, leading to
  hallucinated "I can't access the document" responses.
- **onbod**: `/v1/outbound` accepts an optional `channel` field and onbod
  threads `onboarding.channel` through every `sendReply` call site (prompt,
  invalid, taken, waiting). Fixes the REDACTED 502 where messages arriving via
  `telegram-REDACTED` were routed via `reg.ForJID` to primary `telegram`, which
  wasn't a member of that chat.
- **onbod**: name-taken branch no longer leaks an enumeration oracle for
  registered worlds; collapsed into the invalid-name path.
- **proxyd**: logging middleware now wraps the whole mux. Previously only `/`
  was wrapped, so `/auth/*` and `/pub/*` routes bypassed logging entirely.
- **ipc**: `register_group fromPrototype` now passes the parent folder instead
  of the child JID. The child's JID was unregistered so `SpawnGroup(jid, jid)`
  always returned "parent group not found". Contract updated to
  `SpawnGroup(parentFolder, childJID)`.
- **ipc**: guard against deleting a tier-0 default route from under a running
  agent.
- **gateway**: persist and seed adapter pinning from `chats.channel` so
  cross-adapter replies keep flowing through the originating adapter.
- **gateway**: advance agent cursor after prefix-route handoff — `@nobody` and
  other non-matching prefix routes no longer reprocess on restart.
- **gateway/store**: `session_log` now records every container run.
  `RecordSession` takes a caller-provided start time for true duration,
  `EndSession` always runs (not gated on a new session id), and continued
  sessions log per-run rows with COALESCE backfill when the session id is
  learned mid-run.
- **store**: `StoreOutbound` no longer silently drops rows on the NOT NULL
  `sender` constraint; synthesizes a unique `out-unsent-<nano>` ID when
  `PlatformMsgID` is empty so failed sends don't collide on the `out-` PK.
- **whapd**: `registerWithRetry` with backoff instead of `process.exit(1)` on
  router register failure. Also `recoverCredsIfEmpty` + atomic `backupCreds`
  so Baileys' non-atomic `writeFile` can no longer corrupt `creds.json` across
  restart loops.
- **proxyd**: fail closed on empty `AUTH_SECRET`; redirect bare `/` to `/pub/`
  and bare `/pub` → `/pub/` so trailing-slash is optional.
- **vited**: bake MPA mode + trailing-slash 301 plugin into the image.
- **auth**: add policy case for `get_grants`/`set_grants`.

### Added

- **chanlib**: `TypingRefresher` wired into teled (4s refresh) and discd (8s
  refresh) via `BotHandler.Typing` → `typing.Set`. Telegram/Discord native
  typing expires in 5–10s, so long agent runs were losing the indicator.
- **auth**: `CheckSpawnAllowed(parent, groups)` helper unifies `MaxChildren`
  enforcement across `gateway/spawn.go` and `ipc/ipc.go` (logic was literally
  duplicated across two buckets).
- **tests**: 15 new regression tests. 9 in `chanreg` covering
  `ForJID`/`Resolve`/`Entry.Owns` (primary-over-variant preference, fallback
  chain, multi-prefix, no-match) and 6 in `api` covering `/v1/outbound`
  (channel-pinned regression test, ForJID fallback, stale-channel fallback,
  404, validation, auth). Plus `TestStoreOutbound_ExcludedFromMessagesSince`.
- **specs/6**: `6-workflows.md` (workflowd — declarative flows reading the
  shared SQLite bus) and HITL firewall + authoring product drafts.

### Refactored

Full refinement pass across 10 subsystem buckets. 58 `[refined]` commits,
~-155 LOC net. Selected highlights:

- **store**: `msgCols` constant + `scanMessage`, `routeCols` + `scanRoute`,
  COALESCE-flattened nullable scans, removed kanipi-era `seedFromPragma` shim,
  dropped unused `TaskCompleted`/`SpawnTTL`/`ArchiveDays` fields.
- **gateway**: `tryExternalRoute` dedupes poll/process routing, `logAgentError`
  flattens callbacks, `containsFold` helper, `strings.Cut` for command
  parsing, consolidated `cmdNew` branches.
- **container**: extracted `prepareInput`, unexported `BuildMounts`/
  `VolumeMount`/`ReadonlyMountArgs`, dropped dead last-line output fallback.
- **queue**: removed dead `idleWaiting` field and `closeStdinLocked` helper.
- **ipc**: `granted()` wrapper dedupes tool registration, folded `CheckAction`
  nil-gate into it, dropped `groupFolderByJid` (use `folderForJid` via routes
  table).
- **auth**: inlined single-use `getTier`, simplified `splitArgon2`, fixed
  login limiter eviction.
- **grants**: unified `matchGlob`/`matchValueGlob` via boundary function.
- **chanreg/api**: deduped auth + JID-owns checks, deduped `HealthCheck`.
- **chanlib**: `Run` helper collapses main.go boilerplate across adapters;
  adapters implement `BotHandler` directly, wrapper layer removed.
- **adapters**: bskyd/mastd/reditd dropped `router_client.go` alias wrappers;
  reditd merged `get`/`post` into single `do`; bskyd merged
  `xrpcAuth`/`xrpcWithAuth` and inlined `createSession`; teled replaced
  `stubHandler` reimpl with real handler + stub bot; whapd dropped unused
  `link-preview-js`/`form-data`/`thread_id`/`topic` fields.
- **onbod**: delegated schema to gated (dropped own migrations + `dbmig`
  dep); `sendReply` takes required channel; `approveInTx` owns timestamp +
  welcome construction; inlined `notify` (single-caller library removed).
- **dashd**: trust proxyd identity headers (dropped JWT reverify); `dash`
  methods own writer helpers; inlined single-use `writeGroupMessageCount`.
- **timed**: dropped `checkedSpawns` debug counter; `cleanupSpawns` via
  routes table.
- **proxyd/webd**: `davRoute` shares `X-User-Groups` parse; dropped dead
  `Description` field, trivial comments, and `fmt` dep for single Hijack
  error; simplified `XTopics`, inlined `hubKey`.
- **cmd/arizuko**: `die` helper, extracted `seedGroupDir` and
  `requireCompose`.
- **compose**: inlined `namedService`, moved `CHANNEL_SECRET` into
  `routerEnvKeys`, dropped dead `HOST_DATA_DIR` empty check.
- **template**: dropped unused `REDACTED_USERS` from `env.example`.
- **groupfolder**: unified `GroupPath` and `IpcPath` via `resolve` helper.
- **mountsec**: dropped `LoadAllowlist` and tests — `container/runner.go`
  always passes empty `Allowlist{}`, so the file loader was dead on arrival.
  ARCHITECTURE.md section corrected.

---

## [v0.22.0] — 2026-04-04

### Changed

- **schema**: `registered_groups` table renamed to `groups`, rekeyed by `folder` (PK) instead of `jid`. Migration 0020 handles the transform automatically.
- **schema**: `agent_cursor` moved from groups to `chats` table (per-JID, not per-folder)
- **schema**: all JID→folder mappings now stored as `type='default'` entries in `routes` table
- **gateway**: dual-map architecture (`groups` + `jidToFolder`) for folder-keyed group lookup with JID resolution via routes
- **cli**: `group rm` now takes folder (was JID); `group list` shows `folder\tname`

### Fixed

- **media**: use original filename for downloaded attachments when available
- **gateway**: `get_history` now checks routes table for JID→folder access (was checking only registered_groups which no longer has JID)
- **gateway**: advance agent cursor after delegation to prevent message replay on restart

### Refactored

- **store/groups.go**: full rewrite for folder-keyed CRUD, `JIDFolderMap()` for route-based JID resolution
- **gateway**: removed dead `isVoiceMime`, inlined `groupByFolderLocked` at 7 call sites, extracted `groupCols` const
- **onbod**: SQL updated for `groups` table; `isTier0` checks routes+groups join
- **timed**: `cleanupSpawns` queries routes for JIDs instead of groups table
- **dashd**: all queries updated for `groups` table
- **docs**: 20+ spec/architecture files updated for consistent `groups` naming

---

## [v0.21.1] — 2026-04-02

### Refactored

- **layout**: flattened `data/ipc/` to `ipc/` at project root; removed `DataDir`, added `IpcDir`
- **ipc**: removed legacy `/workspace/group/` path support (pre-v1, no compat needed)
- **gateway**: removed old cursor format migration in `loadState`
- **container**: removed NANOCLAW\_ env var cleanup loop (no old vars exist)
- **core**: removed dead `AuthUsername` config field

### Removed

- 7 nanoclaw/kanipi name references across Dockerfile, DEV.md, CLAUDE.md, TODO.md, docs
- TODO.md "remaining kanipi ports" section (all features already implemented)

### Changed

- **skills**: synced latest skills + CLAUDE.md to all deployed groups (REDACTED 5 + REDACTED 1)
- **docs**: updated ARCHITECTURE.md, MIGRATION.md, specs for new layout

---

## [v0.21.0] — 2026-04-02

Full daemon audit: 50+ bugs found and fixed across 25+ files. Skills audit and simplification.

### Fixed — Audit rounds 1-3

- **container**: parseBuf unbounded growth; large agent output could OOM the gateway
- **container**: seedSettings race condition; concurrent spawns for the same group could corrupt
  settings files
- **container**: copyFile did not check close error; silent data loss on full disk
- **container**: stdin.Write error swallowed; container could run with no/partial input
- **chanlib**: Chunk byte-split broke multi-byte Unicode sequences at chunk boundaries
- **auth**: extractBearer accepted any Authorization scheme, not just Bearer
- **store**: scanMessage ignored Scan errors, returning zero-value messages
- **onbod**: approve/reject not transactional; crash mid-operation left inconsistent state
- **timed**: fire race condition allowed duplicate task execution
- **store**: FlushSysMsgs not atomic; crash mid-flush could lose messages
- **teled**: `/files` endpoint had no auth; anyone could proxy Telegram files
- **gateway**: downloadFile had no HTTP timeout; slow upstream blocked enricher
- **chanreg**: All() returned shared pointers; callers could mutate registry state (data race)
- **proxyd**: per-IP rate limiter entries never evicted; memory leak over time
- **discd**: dropped attachment captions; made per-message API call instead of batching
- **router**: mdToHTML did not escape HTML entities; user input could inject HTML
- **gateway/spawn**: data race in spawnFromPrototype; `g.groups` map read without mutex
- **emaid**: SMTP header injection via unsanitized CRLF in `to` and `rootMsgID`
- **auth**: JWT injected into script tag without escaping (`%s` → `%q`)
- **gateway**: downloadFile leaves orphan file on error; also checked close error
- **emaid**: IMAP Store command result never awaited; `\Seen` flag could fail silently
- **auth**: loginLimiter memory leak; per-IP entries never evicted (added 10K cap)
- **compose**: yamlList doesn't escape single quotes; could produce invalid docker-compose.yml
- **emaid**: upsertThread tx.Exec errors discarded silently

### Fixed — Audit round 4

- **gateway**: silent delivery failure drops agent responses; now stores response on send error
- **proxyd**: raw AUTH_SECRET bypass removed; only JWTs accepted
- **gateway/spawn**: MaxChildren semantics unified with auth.IsDirectChild
- **container**: seedSettings wrote to wrong path (DataDir instead of GroupsDir); root agent
  never saw ARIZUKO_IS_ROOT
- **container**: stale NANOCLAW\_ env vars cleaned from settings.json on every spawn
- **proxyd**: vhosts rewrite path internally instead of 301 redirect; redirect caused infinite
  loop because browser keeps Host header

### Fixed — Skills audit (11 bugs across 9 skills)

- **skills/info**: migration version hardcoded as 1 instead of 51
- **skills/self**: MCP tools table missing 5 tools
- **skills/hello**: ARIZUKO_IS_WORLD_ADMIN env var never set by runner
- **skills/howto**: same ARIZUKO_IS_WORLD_ADMIN issue
- **skills/reload**: description said "gateway process" but kills container PID 1
- **skills/recall-messages**: get_history described as raw JSON IPC, is MCP tool
- **skills/compact-memories**: schedule_task examples used wrong param names
- **skills/web**: vite restart section referenced PID file inside another container
- **skills/infra**: referenced 301 redirect, now internal path rewrite
- **skills/acquire**: tilde in curl -F doesn't expand; relative Read path
- **skills/tweet**: referenced nonexistent examples/ directory

### Added

- **chanlib**: `NewAdapterMux` — shared adapter HTTP server (send, send-file, typing, health)
- **dbmig**: shared SQL migration framework extracted from onbod + timed
- **chanlib**: 13 handler tests
- **chanreg**: health endpoint test
- **discd**: bot/mentions tests
- **gateway**: impulse test, 8 makeOutputCallback/sendMessageReply tests
- **store**: sessions test
- **skills/migrate**: group discovery via `refresh_groups` MCP tool

### Changed

- **timed**: added SIGTERM handler for graceful shutdown
- **dashd**: added SIGTERM handler for graceful shutdown
- **proxyd**: added SIGTERM handler for graceful shutdown
- **teled, discd, mastd, bskyd, reditd**: converted to use `chanlib.NewAdapterMux`
- **grants**: uses `core.JidPlatform`, removed local duplicate

### Refined

- **skills**: 9 skills simplified — removed defensive checks, assume consistent state
  (recall-memories, recall-messages, self, info, hello, migrate, howto, web, infra)

### Removed

- **store**: dead code — `UnroutedMessages`, `RecentMessages`, `GroupMessages`,
  `SetMessageStatus` (unused)
- **auth**: dead code — `Middleware` function, `publicPrefixes`, `publicExact`

---

## [v0.20.2] — 2026-04-02

### Added

- **agent**: auto-recall on ambiguous tasks — agents run `/recall-memories` to disambiguate
  unclear references before asking the user to clarify

---

## [v0.20.1] — 2026-04-02

### Added

- **ipc/send_file**: `caption` param on `send_file` MCP tool; teled sends as native caption,
  whapd passes through, discd ignores
- **core**: `Channel.SendFile` takes `caption string` param (migration 050)
- **gateway**: inbound media attachment pipeline — enricher downloads attachments to
  `groups/<folder>/media/<YYYYMMDD>/` before container spawn; voice transcribed via Whisper
  when `VOICE_TRANSCRIPTION_ENABLED=true` and `WHISPER_BASE_URL` set
- **chanlib**: `InboundAttachment` struct; channel adapters populate `Attachments` field in
  inbound messages
- **teled**: serves `GET /files/{fileID}` proxy to Telegram CDN for attachment downloads
- **discd**: extracts attachment metadata from Discord message events
- **store**: migration 0019 adds `attachments TEXT` column to `messages`; `EnrichMessage(id, content)`
  updates content and clears attachments after enrichment
- **agent**: sees attachments as `<attachment path="..." mime="..." filename="..."/>` XML in
  message content (path is container-side `/home/node/media/...`)
- **skills**: `/dispatch` skill for task-level skill discovery and reconciliation
- **chanlib**: 17 tests covering RouterClient, Auth, Chunk, WriteJSON, WriteErr

### Fixed

- **container**: scope orphan cleanup and container names to instance; multi-instance deployments
  were killing each other's containers on startup
- **container**: mount GroupsDir at `/workspace/data/groups` for root containers; migrate skill
  was broken because path was never mounted
- **gateway**: log errors in `delegateToFolder` and `handlePrefixRoute` send paths; delivery
  callback tests added
- **gateway**: simplify message loop, retry logic, cache mention regex
- **gateway**: log `sendMessageReply` errors instead of discarding them
- **teled**: remove duplicate typing loop; gateway `keepTyping` already refreshes every 6s
- **teled**: capture reply-to threading from Telegram
- **vite**: bake `allowedHosts` config into Dockerfile; CLI flag not supported in Vite 8
- **skills**: remove dead `ARIZUKO_GROUP_FOLDER`/`TIER`/`CHAT_JID` env vars; fix NANOCLAW→ARIZUKO
  naming in skill files

---

## [v0.20.0] — 2026-03-27

### Fixed

- **router**: `chat_id` attr missing from `<message>` XML (spec N-memory-messages)
- **store**: `<system_message>` tag → `<system>` per spec Y-system-messages
- **ipc**: `once`/ISO-8601 schedule type in `schedule_task` MCP tool was silently broken
- **timed**: `task_run_logs.error` column never populated; `get_history` GetInt cast
- **webd**: anon sender was literal `"anon"` — now `anon:<ip-hash>` per spec W-slink
- **diary/episodes**: block-scalar YAML (`summary: |`, `summary: >`) parsed as empty — diary and episode context was silently dropped from agent prompts
- **mastd**: `follow`/`favourite`/`reblog` notifications were dropped; now mapped to correct verbs
- **bskyd**: all notifications delivered with empty verb; now set from `reason` field
- **reditd**: verb and topic not set; subreddit JID prefix wrong (`reddit:golang` → `reddit:r_golang`)
- **router**: `verb` route type never matched (missing `case "verb":` in routeMatches)
- **gateway**: sticky commands with embedded newlines now rejected

### Added

- **router**: `platform`, `verb`, `thread` attrs in FormatMessages XML per spec i-social-events
- **gateway**: `SEND_DISABLED_CHANNELS` / `SEND_DISABLED_GROUPS` env vars to suppress sends
- **ant/CLAUDE.md**: `<observed>` message guidance — watch-only routing context
- **dashd**: episodes, users, facts sections in memory dashboard
- **core**: migration 049 documenting `get_history` MCP tool for agents
- **specs**: phases 7+8 dissolved into correct phases; 88 files got frontmatter; LinkedIn channel specced

---

## [v0.19.2] — 2026-03-27

### Added

- **whapd: media inbound** — images, video, audio, voice notes, documents
  downloaded via Baileys `downloadMediaMessage` and forwarded to router as
  attachment fields. Media-only messages (no caption) deliver a description
  (`[Image]`, `[Voice Note]`, `[File: name]`).
- **whapd: LID→phone JID translation** — in-memory cache maps WhatsApp LID
  format to phone-based JIDs; required for modern WA accounts.
- **whapd: group metadata sync** — `groupFetchAllParticipating()` on connect
  - 24h refresh; group names passed to router on `sendChat`.
- **whapd: outbound message queue** — messages queued when disconnected,
  flushed on reconnect; `/send` returns `{ queued: true }` instead of 502.
- **whapd: `/send-file` endpoint** — send images, video, audio, documents
  back to users; accepts base64 `data` + `mime` + optional `filename`/`caption`.

---

## [v0.19.1] — 2026-03-27

### Fixed

- **whapd**: `saveCreds()` now awaited before closing socket on pairing —
  `creds.json` was 0 bytes on first pair, forcing repeated re-pair cycles.
- **whapd**: Bot loop guard — skip inbound messages whose `pushName` matches
  `ASSISTANT_NAME` (prevents agent self-reply loops in group chats).
- **whapd**: Read receipts — mark messages read after delivery so users don't
  see perpetual unread badges.
- **whapd**: Markdown→WhatsApp formatting on outbound send — `**bold**` →
  `*bold*`, `~~strike~~` → `~strike~`.
- **whapd**: `makeSocket()` now returns `saveCreds` for callers that need explicit flush.

---

## [v0.19.0] — 2026-03-27

### Changed

- **ant/ rename**: `container/agent-runner/` → `ant/`; `container/skills/` →
  `ant/skills/`; `container/CLAUDE.md` → `ant/CLAUDE.md`. Go spawn code stays
  in `container/`. The in-container agent is now called "ant".

- **Image rename**: `arizuko-agent:latest` → `arizuko-ant:latest` throughout
  config defaults, env.example, and generated `.env` on `arizuko create`.

- **Sessions path collapsed**: `data/sessions/<folder>/.claude/` merged into
  `groups/<folder>/.claude/`. Group folder mount at `/home/node` already covers
  `.claude/`; separate mount removed. Matches kanipi model.

- **SeedGroupDir at creation time**: Skills seeding (`seedSkills`) moved from
  `container.Run()` (every message) to group creation. `container.SeedGroupDir()`
  is now called from `arizuko group add`, `onbod /approve`, and the
  `register_group` MCP tool. `seedSettings()` stays in `Run()` for runtime
  values (grants, session ID, socat MCP config).

- **Service catalog embedded**: `template/services/*.toml` now embedded in
  `arizuko:latest` at `/opt/arizuko/template/services/`. Ansible extracts from
  image — no duplicate TOML files in role.

- **gated.sock**: MCP socket renamed from `router.sock` → `gated.sock`
  throughout docs, specs, and skills.

### Fixed

- MCP socket permissions: `ipc.go` now sets mode `0666` so agent (uid=1000)
  can connect without being blocked.
- `RegisterGroup` error was silently dropped with `//nolint` in `ipc/ipc.go`;
  now logged as warn.
- Dead `sessions/` volume mount for root groups removed from `BuildMounts()`.
- `cmdCreate` now calls `SeedGroupDir` instead of writing a static `CLAUDE.md`
  that diverged from what `cmdGroup add` and onbod produce.
- `groupRunnerDir` (copying `ant/src` on first run) removed — source is baked
  into the container image.

### Added

- **emaid IMAP IDLE**: Replaced 30s poll loop with RFC 2177 IMAP IDLE push;
  28-min safety timer for RFC-compliant reconnect. Eliminates latency and
  reduces load.
- **whapd**: `--pair <phone>` flag for phone-number pairing; QR via
  `qrcode-terminal`; exponential backoff on reconnect; exit on 405.
- **teled-REDACTED**: Service template for second Telegram bot in merged instance.
- **ant/ant**: Standalone CLI script (like dockbox) wrapping `docker run
arizuko-ant` with correct mounts for use outside arizuko.

---

## [v0.18.0] — 2026-03-26

### Added

- **Impulse gate**: Weight-based event batching per JID before agent wake-up.
  Messages accumulate weight; agent fires when threshold (default 100) is
  reached or 5-minute max-hold timeout expires. Social verb events (join/edit/
  delete) carry weight 0 so they don't trigger agents alone. Config via
  `defaultImpulseCfg()` in `gateway/impulse.go`.

- **Verb field on messages**: `core.Message` and `chanlib.InboundMsg` now carry
  a `verb` field (`"join"`, `"edit"`, `"delete"`, etc.; default `""`).
  Stored in SQLite via migration 0017. Channel adapters set `verb` on
  non-standard events; the impulse gate weights by verb type.

- **WebDAV via dufs**: `WEBDAV_ENABLED=true` in `.env` adds a `davd` service
  (`sigoden/dufs:latest`) that mounts `groups/` read-only. `proxyd` exposes
  `/dav/` as an auth-gated reverse proxy with path prefix stripping.
  Controlled via `DAV_ADDR` env in proxyd and `WEBDAV_ENABLED` in compose.

- **Social adapter service templates**: `template/services/mastd.toml`,
  `bskyd.toml`, `reditd.toml` — drop-in service definitions for Mastodon
  (port 9004), Bluesky (port 9005), and Reddit (port 9006) adapters.

- **Auth hardening**:
  - Login rate limiting: 5 attempts per 15 minutes per IP (sliding window,
    in-memory, resets on restart).
  - Secure cookie flag: `refresh_token` and OAuth state cookies set
    `Secure: true` when `WEB_HOST` (or `LISTEN_URL`) starts with `https://`.
  - Telegram replay protection: `auth_date` in widget payload must be within
    5 minutes of now; stale logins are rejected.
  - GitHub OAuth (`GITHUB_CLIENT_ID`/`GITHUB_CLIENT_SECRET`): login button
    shown when configured; optional org membership check via `GITHUB_ALLOWED_ORG`.
  - Discord OAuth (`DISCORD_CLIENT_ID`/`DISCORD_CLIENT_SECRET`): login button
    shown when configured.

- **Voice synthesis spec**: `ttsd` daemon design and `send_voice` MCP tool
  spec in `specs/8/6-voice-synthesis.md`. Open questions resolved.

### Fixed

- **Agent container CWD**: working directory changed from `/workspace/group`
  to `/home/node` (the mounted group folder). Diary paths, conversation
  archive paths, and `cwd` in agent queries all corrected.
- **Diary skill path**: updated from `/workspace/group/diary/` to `~/diary/`
  to match the corrected mount point.
- **Dockerfile mkdir**: removed stale `/workspace/group` from `mkdir -p`
  (no longer a mount target).

### Changed

- **`containerHome` constant**: extracted `/home/node` to a named constant in
  `container/runner.go` and `container/agent-runner/src/index.ts` to avoid
  repeated string literals.
- **License**: public domain (Unlicense). No restrictions. If you build on
  arizuko, acknowledge it — not because you have to, because that's how
  good work compounds.
- **README**: removed Japanese subtitle, added "Why arizuko" section explaining
  the real Claude Code CLI approach vs SDK wrappers.

---

## [v0.17.0] — 2026-03-23

### Added

- **`arizuko generate`**: new subcommand that writes `docker-compose.yml`
  without running the stack. Systemd units now use
  `docker run arizuko:latest arizuko generate <name>` (ExecStartPre) +
  `docker compose up` (ExecStart) — no host binary required. Compose
  version always matches the image.

- **Agent dev tooling**: `gh`, `duckdb`, `grpcurl`, `delta`, `semgrep`, and
  15+ more tools added to the agent container image.

### Fixed

- **Delta install**: switched from `.deb` to tarball binary to avoid `dpkg`
  dependency errors on the agent container build.

### Changed

- **Compose generator**: deduplicated `writeEnv` helper (was copy-pasted
  three times); removed comments that restated code.
- **Renamed throughout**: `webd` → `proxyd` in compose generator, systemd
  Ansible template, and docs.

---

## [v0.16.0] — 2026-03-23

### Fixed

- **Queue**: messages delivered to a running container via stdin injection
  (`SendMessage`) were silently dropped if the container died before
  processing them. `SendMessage` now sets `pendingMessages=true` so the
  next drain spawns a retry. `processGroupMessages` returning no messages
  is now treated as success (was incorrectly incrementing failure counter).

### Added

- **Web chat**: `web:` JID routing in gateway, per-topic agent runs
  (`processWebTopics`), `user_groups` table + `Groups` field in JWT,
  proxyd slink token resolution with per-IP rate limiting (10 req/min),
  `requireFolder` middleware in webd.
- **Structured logging**: info/debug log coverage across `gated`, `timed`,
  `gateway`, `ipc`, `chanlib`, `container/runner` — all routing decisions,
  MCP tool calls, container lifecycle events, channel registration, and
  task scheduling are now traceable from logs alone.
- **Specs**: agent-managed services (`servd`, specs/7/28), self-improvement
  loop (specs/7/29).

### Changed

- **MCP server name**: renamed from `nanoclaw` to `arizuko` in
  `ipc/ipc.go`, `container/runner.go`, and `agent-runner`. Tool names
  visible to agents are now `mcp__arizuko__*`.
- **Isolated container names**: `timed` now encodes the task ID in the
  sender field (`scheduler-isolated:<task_id>`); gateway builds container
  name as `arizuko-<folder>-task-<task_id>` instead of a timestamp.

---

## [v0.15.0] — 2026-03-21

### Changed

- **Build**: all daemons now build their binary in-place (own directory) via
  individual Makefiles. Root Makefile delegates uniformly via `DAEMONS` list.
  `gated`, `onbod`, `dashd`, `proxyd`, `timed` each gain their own `Makefile`.
- **CI**: workflow now sets up Go, installs and runs `pre-commit --all-files`,
  and runs `make lint` + `make test` across all packages.
- **`.gitignore`**: per-dir binary entries for all daemons (`onbod/onbod`,
  `dashd/dashd` were missing).

---

## [v0.14.0] — 2026-03-21

### Changed

- **Agent persona**: agents are now described as "unalive" — not alive, not
  alien. Removed ant/dead metaphors from bio, greeting, and README.
- **Howto skill**: replaced static 66 KB HTML template with `CONTENT.md`
  (20 sections as markdown) + `STYLE.md` (5-axis style generation guide).
  Agent now generates the page fresh each deployment with a unique visual
  style — palette, typography, density, decoration, and dark mode chosen
  from named archetypes or by imitating a given URL.

### Removed

- `.refs/` directory (175 files, ~8 MB of old nanoclaw and arizuka reference
  code) removed from repo and git history. Added to `.gitignore`.
- `docs/kanipi.html` — stale landing page from pre-rename.
- `template/web/pub/howto/index.html` — superseded by CONTENT.md + STYLE.md.

### Docs

- `CLAUDE.md`: noted `mastd`, `bskyd`, `reditd`, `chanlib`; CGO requirement
  for `gated`; single-test command pattern.

---

## [v0.13.0] — 2026-03-21

### Added

- **Email channel** (`emaid/`): IMAP TLS polling (every 30s) + SMTP STARTTLS replies; thread
  tracking via local SQLite; JID `email:<thread_id>`. Config: `EMAIL_IMAP_HOST`,
  `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`.
- **Mastodon channel** (`mastd/`): WebSocket streaming for mention notifications; posts and
  replies via REST API. Config: `MASTODON_INSTANCE_URL`, `MASTODON_ACCESS_TOKEN`.
- **Bluesky channel** (`bskyd/`): Polls AT Protocol notifications (replies + mentions) every
  10s; creates posts/replies via XRPC; session persistence. Config: `BLUESKY_IDENTIFIER`,
  `BLUESKY_PASSWORD`.
- **Reddit channel** (`reditd/`): OAuth2 password grant; polls inbox + configured subreddits
  every 30s; replies via `/api/comment`. Config: `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET`,
  `REDDIT_USERNAME`, `REDDIT_PASSWORD`, `REDDIT_SUBREDDITS`.

- **Reply-to threading** (`core/`, `gateway/gateway.go`): `Channel.Send` signature
  is `Send(jid, text, replyTo string) (string, error)` — accepts a `replyTo`
  message ID and returns the sent message ID; gateway passes the last agent-sent
  message ID as reply context on each outbound send.
- **Chunk chaining** (`gateway/gateway.go`): `Send` returns the sent message ID;
  gateway chains `lastSentID` per agent run so multi-chunk replies thread correctly.
- **recall-memories / recall-messages skills** (`container/skills/`): `recall`
  skill renamed to `recall-memories`; new `recall-messages` skill added for
  message history lookup.
- **Google OAuth workspace hint** (`auth/oauth.go`): `hd=` parameter appended to
  Google OAuth redirect when `GOOGLE_ALLOWED_EMAILS` patterns share a single domain
  (e.g. `*@example.com`), restricting the sign-in picker to that workspace. Supports
  multiple patterns — hint only added when all share one domain.
- **`register_group` fromPrototype** (`ipc/ipc.go`, `gateway/gateway.go`): `register_group`
  now accepts `fromPrototype=true` to copy this group's `prototype/` directory into a new
  child folder (folder derived from jid); `name` is now optional (defaults to jid); merges
  the former `spawn_group` tool.
- **Agent-runner exits on empty IPC input** (`container/agent-runner/src/index.ts`):
  `waitForIpcMessage` (async Promise wrapper) replaced with synchronous
  `checkIpcMessage`; runner exits immediately when IPC input dir is empty,
  enabling L-chat-bound sessions.

### Changed

- `gateway/gateway.go`: `runAgentWithOpts` variadic `msgID ...string` collapsed
  to plain `msgID string`; 3 call sites updated. Removes optional-string ambiguity.
- `ipc/ipc.go`: `send_reply` handler nil-guard on `SendReply` removed; collapsed
  to single unconditional call (guard was dead — `SendReply` is never nil).
- `grants/grants.go`: `sortedKeys` uses `sort.Strings` instead of insertion sort;
  `nullStr` deduped to single definition.
- `auth/jwt.go`, `auth/identity.go`: `mintJWT`, `isInWorld` unexported (internal only).
- `router/router.go`: `EscapeXml`, `TimeAgo`, `StripThinkBlocks`, `SenderToUserFileID`,
  `ExpandTarget` unexported (internal only).

### Removed

- `store/outbound.go`: deleted (was empty file).
- `store/tasks.go`: `LogTaskRun`, `ListTaskRuns`, `TaskRun` removed (dead code,
  unused since timed daemon uses direct SQL).
- `core/config.go`: `VitePort` field removed (unused).

---

## [v0.12.0] — 2026-03-19

### Added

- **Kanipi skill sync** (`container/skills/`, `container/output-styles/`): 6 new
  agent skills (acquire, compact-memories, recall, specs, users, infra), 3 output
  style guides (discord, telegram, email). Agent migrations 015-043 added;
  `MIGRATION_VERSION` bumped 16 → 38. `hello` skill updated to comprehensive format.
  Sync head: fdbac9f.
- **REDACTED instance**: `REDACTED` running. Groups: root, REDACTED, happy.
  Port layout: gated 8081, dashd 8091, web 49165. Telegram bot: REDACTED.

### Changed

- **Compose container naming** (`compose/compose.go`): all services named
  `<app>_<daemon>_<flavor>` (e.g. `arizuko_gated_REDACTED`, `arizuko_teled_REDACTED`).
  Applies to built-in services (gated, timed, dashd) and user-defined services
  from `services/*.toml`. Prevents container name conflicts when multiple instances
  run on the same host.
- **Onbod in compose**: `onbod` auto-included when `ONBOARDING_ENABLED=true`.
  Compose sets `ONBOD_LISTEN_ADDR=:8092` to avoid conflict with `dashd` (`:8090`).

### Infrastructure

- **Ansible role** (`core/ansible/roles/arizuko-service/`): systemd unit generation
  from template. Instances declared as `arizuko_instances: [{flavor: REDACTED}, {flavor: REDACTED}]`
  in `host_vars`. Data dir and secrets are NOT managed by Ansible.

---

## [v0.11.0] — 2026-03-19

### Added

- **Google OAuth** (`auth/oauth.go`, `auth/web.go`, `auth/middleware.go`):
  `/auth/google` and `/auth/google/callback` routes. Login page gains a
  Google button when `GOOGLE_CLIENT_ID` env is set.
- **Prototype spawning** (`gateway/spawn.go`, `store/migrations/0010-prototype-spawn.sql`):
  when a route targets an unregistered folder and the parent group has a
  `prototype/` subdirectory, a child group is auto-created by copying the
  prototype. Groups gain `state`, `spawn_ttl_days`, `archive_closed_days`
  columns. Daily cleanup in `timed` marks idle spawns closed and archives
  them as `.tar.gz`.
- **Episode injection** (`container/episodes.go`, `container/runner.go`):
  `episodes/` YAML-frontmatter files in a group folder are read at session
  start and injected as `<episodes>` XML into the prompt.
- **Dashd** (`dashd/main.go`): operator dashboard daemon (HTMX, read-only
  SQLite). Pages: status, tasks, activity, groups, memory browser. Auth via
  existing JWT session cookie. Added to Makefile and compose generation.
- **Bot-mention guidance** (`container/CLAUDE.md`): agents always respond
  when @mentioned by name, stay silent otherwise.

## [v0.10.0] — 2026-03-18

### Added

- **Topic sessions** (`gateway/gateway.go`, `store/sessions.go`,
  `store/migrations/0008-topic-sessions.sql`): `#topic` prefix routes
  messages to isolated sessions within a group. `/new #topic` resets only
  that topic. `sessions` table gains a `topic` column; PK is now
  `(group_folder, topic)`.
- **Prefix routing** (`router/router.go`): new `prefix` route type. `@name`
  dispatches to a named group, `#topic` selects a topic session. Evaluated
  before `pattern` in tier order.
- **Grants engine** (`grants/grants.go`): `CheckAction`, `NarrowRules`,
  `MatchingRules`, `DeriveRules`. Rules are derived at container spawn
  (`container/runner.go`) and injected into `start.json`.
- **IPC grants integration** (`ipc/ipc.go`): MCP manifest filtered by grants
  rules so agents only see permitted tools. `set_grants`/`get_grants` tools
  added. `delegate_group` calls `NarrowRules` before persisting child rules.
- **Onboarding daemon** (`onbod/main.go`, `store/onboarding.go`,
  `store/migrations/0009-onboarding.sql`): state machine
  `awaiting_name → pending → approved/rejected`. Poll loop prompts users,
  validates names, notifies tier-0 operators. `/approve` and `/reject`
  commands handled via `/send` HTTP endpoint. On approval, creates group dir,
  inserts routes, sends welcome system event.

### Changed

- `gateway/commands.go`: `cmdText()` strips media placeholders and routing
  prefixes before command detection; `isGatewayCommand()` predicate added;
  `handleCommand()` uses `cmdText()` consistently.
- `gateway/gateway.go`: `processGroupMessages()` filters gateway commands
  from agent context (they are never forwarded to the container);
  `pollOnce()` includes unrouted JIDs when `ONBOARDING_ENABLED` is set;
  `insertOnboarding` hook seeds `onboarding` table for new unrouted JIDs.
- `store/groups.go`: `UnroutedChatJIDs(since time.Time)` returns chat JIDs
  with recent messages that have no entry in the routes table.
- `container/runner.go`: `seedSkills()` seeds `.claude.json` if missing
  (SDK requires it); takes `folder` param for stable userID hash.
- `core/config.go`: `OnboardingEnabled bool` field (`ONBOARDING_ENABLED` env).

## [v0.9.1] — 2026-03-17

Channel adapters, flat layout, dead code cleanup, container fix.

### Architecture

- **Flat layout**: services hoisted from `services/` to top-level dirs
  (`gated/`, `timed/`, `teled/`, `discd/`, `whapd/`). Each is a
  standalone program tree.
- **gated split**: gateway daemon is own binary at `gated/main.go`,
  no longer embedded in `cmd/arizuko/`. `arizuko run <instance>`
  generates compose and runs `docker compose up`.
- **Discord adapter** (`discd/`): Go, ~250 LOC. WebSocket events,
  mention rewriting, file sending. Registers via channel protocol.
- **WhatsApp adapter** (`whapd/`): TypeScript/baileys, ~270 LOC.
  QR auth, session persistence, reconnect. Shows multilang versatility.

### Fixed

- Container hangs 30min after output: timer resets to 5s after final
  output (identified by `newSessionId`), not the full idle timeout.

### Removed

- `mime/` package (never imported)
- `store.AllChats()`, `core.ChatInfo` (unused)
- `router.SpawnFolderName()` and related regexes (unused)

### Docs

- All docs updated for flat layout (no more `services/` prefix)
- Specs: discd/whapd status → running (was planned)
- ARCHITECTURE, README, CLAUDE.md aligned with current code

---

## [v0.9.0] — 2026-03-15

Docker compose orchestration, daemon isolation, comprehensive test
coverage, code refinement.

### Architecture

- **Docker compose deployment**: `arizuko compose <instance>` generates
  docker-compose.yml from `.env` + `services/*.toml`. Systemd runs
  `docker compose up` in foreground.
- **`arizuko run`**: gated-only gateway entrypoint. timed and teled are
  separate containers in the compose stack.
- **Single Docker image**: all five binaries (arizuko, gated, timed,
  teled, discd) built into one image, differentiated by entrypoint.
- **Daemon naming**: compose services use spec names (gated, timed, teled)
  with `container_name` for clean log prefixes.

### Changed

- `arizuko create` generates random CHANNEL_SECRET (was empty)
- `instanceDir()` helper replaces 4 repeated sprintf calls
- `delegateToChild`/`delegateToParent` collapsed into `delegateToFolder`
- `groupByFolder`/`groupJIDs` helpers extracted from duplicated loops
- Dead `"verb"` routing case removed from router
- ipc/auth reclassified as libraries (not daemons) in docs

### Tests

- 21 gateway tests (commands, routing, state, channels, system events)
- 20 container tests (sanitize, mounts, args, settings, output parsing)
- 15 timed tests (migration, fire, cron, concurrent dedup)
- Fixed concurrent test: shared-cache SQLite for in-memory multi-goroutine

### Docs

- CLAUDE.md, ARCHITECTURE.md, README.md aligned with deletions
- Service table shows type (daemon/library)
- Routing rules renumbered (verb tier removed)

---

## [v0.8.0] — 2026-03-15

Microservice architecture. Scheduler extracted to standalone daemon,
schema simplified, dead code removed, specs aligned with code.

### Architecture

- **services/timed/**: standalone scheduler daemon (~150 LOC), polls
  scheduled_tasks, inserts into messages. Zero dependencies on gateway.
  Own migration runner (service name: `timed`).
- **Daemon specs**: gated (9), timed (8), actid (10), auth (11) —
  one spec per daemon with clear table ownership.
- **0-architecture.md**: lean service overview replacing 579-line monolith.

### Breaking: scheduled_tasks schema

- `group_folder` → `owner`
- `schedule_type` + `schedule_value` → `cron` (nullable, NULL = one-shot)
- Removed: `context_mode`, `last_run`, `last_result`
- Removed: `task_run_logs` table
- Migration renumbered (0003-0004)

### Removed

- `scheduler/` package (embedded in gateway, replaced by services/timed/)
- `actions/` package (dead code, unused)
- `store.DueTasks()`, `store.LogRun()`, `store.AllTasks()`,
  `store.UnreportedRuns()`, `store.MarkRunsReported()` (old scheduler methods)
- `container.Input.IsTask` field (dead)
- `core.Task.SchedTyp`, `SchedVal`, `CtxMode` fields (old schema)
- `gateway.formatTaskRuns()` (used removed task_run_logs)

### Changed

- `schedule_task` MCP tool: takes `targetJid`, `prompt`, `cron` (optional).
  No more `schedule_type`/`schedule_value`/`context_mode` params.
- `store.UpdateTask`: consolidated from two SQL queries to one.
- `store.ListTasks`: replaced duplicate `AllTasks()`.

### Tests

- 10 timed daemon unit tests (migration, poll, cron, one-shot, paused, future)
- 12 microservice contract integration tests (schema compat, message insertion,
  task lifecycle, table ownership isolation)
- 16 store task edge case tests (duplicates, nonexistent, empty patch, filters)

### Specs

- Tool names aligned with code: `delegate_group`, `reset_session`,
  `get_routes`/`set_routes`/`add_route`/`delete_route`
- Parameter names: camelCase (`targetJid`, `taskId`)
- actid/auth marked as design (currently inline in gated)

---

## [v0.7.0] — 2026-03-07

Go rewrite. All core gateway functionality ported from TypeScript.
16 packages, ~4,700 LOC Go (vs ~9,400 LOC TS).

### Packages

- **core/** — Config, types (Message, Group, Task, Channel interface)
- **store/** — SQLite persistence (12 tables, WAL mode, PRAGMA user_version migrations)
- **gateway/** — Main loop, message routing, commands (/new, /ping, /chatid, /stop)
- **container/** — Docker spawn, 8 volume mount types, MCP sidecar lifecycle, skills seeding
- **queue/** — Per-group concurrency, stdin piping, circuit breaker (3 failures)
- **router/** — XML message formatting, 5-tier routing rules, outbound filtering
- **ipc/** — File-based request/reply + legacy fire-and-forget, SIGUSR1 wake
- **scheduler/** — Cron/interval/once task runner (robfig/cron), run logging
- **diary/** — YAML frontmatter diary annotations with age labels
- **groupfolder/** — Group path resolution and validation
- **mountsec/** — Mount allowlist validation (blocked patterns, read-only enforcement)
- **runtime/** — Docker binary abstraction, orphan cleanup
- **logger/** — slog JSON handler init
- **cmd/arizuko/** — CLI entrypoint (run, create, group subcommands)

### Features

- Per-chat error tracking with cursor rollback on agent failure
- Forward/reply message metadata (forwarded_from, reply_to_text, reply_to_sender)
- MCP sidecar management (start/stop/settings wiring via Unix sockets)
- Gateway capabilities manifest (.gateway-caps TOML)
- Per-channel output styling (outputStyle in settings.json)
- Diary annotations injected into agent context
- Docker-in-docker path translation via HOST_DATA_DIR/HOST_APP_DIR

### Not ported

- Channel adapters (telegram, discord, whatsapp, email) — Channel interface exists
- Action registry (unified action system with typed schemas)
- Web proxy + auth layer
- MIME enricher (attachment pipeline)
- Slink web channel

---

## TypeScript releases (pre-Go rewrite)

---

## [v0.6.3] — 2026-03-06

### Features

- Media-aware file sending: telegram routes photos/videos/audio/animations
  to native API methods (inline display); whatsapp routes by MIME type
- Diary spec: Stop hook nudge after 100 turns, task tracking in entries,
  terse summary format

### Fixes

- Replace agent error retry loop with circuit breaker (manual retry only,
  warns after 3 consecutive failures per group)
- Telegram: removed dead `method` variable in sendDocument

---

## [v0.6.2] — 2026-03-06

### Features

- Telegram: images (PNG/JPG/GIF/WEBP) sent via `sendPhoto` for inline
  display instead of `sendDocument` (file attachment)

### Fixes

- Agent CLAUDE.md: `send_file` no longer prompts follow-up text description

---

## [v0.6.1] — 2026-03-06

### Fixes

- Container stop: `exec()` → `execFileSync`/`spawn` (no shell anywhere)
- Command handlers: `await` instead of fire-and-forget (race condition)
- Cursor rollback: restore cursor on agent error when no output was sent
  (previousCursor was saved but never used — messages in DB but invisible)
- Routing schema: `.max(200)` on pattern/sender Zod fields (was only
  enforced at runtime, silent failure)
- Sidecar socket cleanup: catch only ENOENT (was `catch {}`)
- Agent container: use `bunx tsc` for build, validate-only compile step

---

## [v0.6.0] — 2026-03-06

### Fixes

- IPC: catch only ENOENT on file cleanup (was swallowing all errors)
- IPC: validate envelope id/type fields, reject malformed requests
- IPC: delete failed files instead of accumulating in errors/ dir
- Routing: cap regex pattern length at 200 chars (ReDoS mitigation)
- Config: validate TIMEZONE via Intl.DateTimeFormat, fallback to UTC
- Sidecar: use spawn() instead of exec() for lifecycle (shell injection fix)

### Features

- **Hierarchical group routing**: parent groups delegate to children via
  routing rules (command, pattern, keyword, sender, default). Authorization
  enforces same-world, direct parent-child only. Max delegation depth 3.
- **Sidecar isolation**: per-group MCP sidecars via `SIDECAR_<NAME>_IMAGE`
  env vars. Socket transport at `/workspace/ipc/sidecars/<name>.sock`.
  Gateway manages lifecycle (start, probe, reconcile settings, stop).
- **Action input validation**: Zod schemas on all actions; malformed
  IPC requests rejected with typed error replies.
- **New actions**: `delegate_group`, `set_routing_rules`
- **Session history**: `session_history` table replaces `sessions`;
  new-session injection includes last 2 previous sessions

---

## [v0.5.0] — 2026-03-06

### Features

- **Action registry**: unified action system — all IPC handlers, MCP
  tools, and commands reference a single `Action` interface with typed
  Zod schemas and authorization. `src/action-registry.ts` + `src/actions/`
- **Request-response IPC**: agents write to `requests/`, poll `replies/`.
  Gateway dispatches through action registry and writes typed replies.
  Fire-and-forget IPC retained for backwards compat during rollout.
- **Tool discovery**: gateway writes `action_manifest.json` at spawn
  time. Agent MCP server reads manifest for dynamic tool registration.
- **Agent MCP self-registration**: agent-written `mcpServers` in
  `settings.json` are merged with built-in `nanoclaw` server.
  Dynamic `allowedTools` includes `mcp__<name>__*` wildcards.
- **Message threading types**: `SendOpts { replyTo }` on Channel
  interface, `replyTo` field on `NewMessage`

### Breaking

- `processTaskIpc` moved from `ipc.ts` to `ipc-compat.ts`
- IPC handlers refactored into `src/actions/` modules

---

## [v0.4.0] — 2026-03-06

### Breaking

- `NANOCLAW_IS_MAIN` env var → `NANOCLAW_IS_ROOT`
- `/workspace/global` mount → `/workspace/share`
- `isMain` removed from `ContainerInput` interface

### Changes

- `isMain` → `isRoot(folder)` — structural check (`!folder.includes('/')`)
  replaces hardcoded `MAIN_GROUP_FOLDER = 'main'` comparison
- `groups/global/` → `groups/<world>/share/` — shared state lives inside
  world root, always mounted (rw for root, ro for children)
- Folder validation allows `/` separator for future hierarchy
- Reserved folder `global` → `share`

---

## [v0.3.0] — 2026-03-06

### Features

- **System messages**: `system_messages` and `sessions` DB tables. Gateway
  enqueues context annotations (new-session history, new-day marker, command
  context) and flushes them as XML before user messages in agent stdin.
- **Session recording**: every container spawn/exit recorded in `sessions`
  table with timing, message count, result, and error. New-session injection
  includes last 2 previous sessions as `<previous_session>` XML elements.
- **Command registry** (`src/commands/`): pluggable handlers replace
  hardcoded telegram commands. `/new` (session reset with continuity),
  `/ping`, `/chatid` shipped. Commands intercepted in message loop before
  agent routing.
- **`reset_session` IPC**: agent can clear its own session via IPC message.
- **Error notification**: on agent error, user receives retry prompt and
  message cursor rolls back. If output was already sent, cursor is preserved
  to prevent duplicate delivery.
- **Agent SKILL.md**: documents system message origins, session history
  access (`~/.claude/projects/`), group configuration files, whisper
  language config. Migrations 005-007.
- **agent-runner CLAUDE.md**: session layout documentation for in-container
  agent.

### Fixes

- System message format corrected (origin+event attributes, no colon).
- Voice transcription label now `[voice/auto→en: ...]` (was `[voice: ...]`).

---

## [v0.2.8] — 2026-03-05

### Features

- Agent self-skill documents session history access (`~/.claude/projects/`)
  and `.whisper-language` group configuration file.
- Migration 005: whisper language config docs. Migration 006: session history.

### Fixes

- System message format corrected in specs/SKILL.md (origin+event, no colon).
- Voice transcription label now `[voice/auto→en: ...]` (was `[voice: ...]`).

---

## [v0.2.7] — 2026-03-05

### Fixes

- **Voice transcription in active sessions**: second voice message in a
  running container session was missing transcription. Root cause: message
  objects fetched before `waitForEnrichments`, then used stale after wait.
  Both dispatch paths (new container + stdin pipe) now re-fetch from DB
  after enrichment completes, so voice/video content is always included.
- IPC drain race: concurrent `drainGroupMessages` calls for same group
  caused duplicate file sends. Fixed with per-group boolean lock.

### Features

- Whisper large-v3 model for better multilingual accuracy.
- Per-group language configuration via `.whisper-language` file.
- Parallel transcription passes: auto-detect + each configured language.
  Output labeled `[voice/auto→{detected}]` or `[voice/{forced}]`.
- Sidecar returns detected language in response; whisper.ts returns
  `WhisperResult { text, language }`.
- Whisper timeout increased to 60s for large-v3 multi-pass.

### Testing

- `src/mime-enricher.test.ts`: 7 tests covering enrichment pipeline,
  race condition (fast-settling enrichment before wait), error swallowing.
- `src/mime-handlers/voice.test.ts`: updated for multi-pass labels and
  `WhisperResult` return type.
- `src/mime-handlers/whisper.test.ts`: updated for `WhisperResult`,
  60s abort timeout.
- `specs/2/2-autotesting.md`: test strategy for all subsystems.

---

## [v0.2.6] — 2026-03-04

### Testing

- `vitest` added as devDependency; `make test` and npm scripts use bare
  `vitest run` (no npx/bunx wrapper)
- `src/config.test.ts`: live-binding assertions for config overrides;
  `_resetConfig()` restores defaults from env in `afterEach`
- `container-runner.ts`: `export let _spawnProcess = spawn` seam allows
  mocking docker without a running daemon
- Fixed container-runner test mocks: missing `HOST_APP_DIR`/`WEB_HOST`
  constants; `readFileSync` mock returning `''` now returns `'{}'`
- `specs/1/b-testing.md`: all testability gaps marked shipped

### Config

- 7 constants changed `const` → `let` in `config.ts`: `SLINK_ANON_RPM`,
  `SLINK_AUTH_RPM`, `WHISPER_BASE_URL`, `VOICE_TRANSCRIPTION_ENABLED`,
  `VIDEO_TRANSCRIPTION_ENABLED`, `MEDIA_ENABLED`, `MEDIA_MAX_FILE_BYTES`
- `_overrideConfig` mutates live bindings directly (was partial)
- `_resetConfig()` added to restore defaults from env; both gated behind
  `NODE_ENV=test`

---

## [v0.2.5] — 2026-03-04

### Gateway

- Fix `hostPath()` to replace `PROJECT_ROOT` instead of `APP_DIR`, fixing
  wrong host mount paths for IPC/session dirs when running inside Docker
- Fix `ipc.ts` file sending: use `HOST_GROUPS_DIR` (host path) instead of
  `GROUPS_DIR` (container-internal path), fixing ENOENT on `sendDocument`

### Skills

- Auto-migration nudge: gateway prepends annotation to agent prompt when
  group skills are behind `MIGRATION_VERSION`
- `MIGRATION_VERSION` bumped to 4

### Specs

- All `specs/1/` marked with shipped/partial/open status
- `specs/1/X-sync.md` rewritten as solved

### Cleanup

- Delete stale `template/workspace/mcporter.json` artifact
- Fix stale template path in `container/skills/howto/SKILL.md`

---

## [v0.2.4] — 2026-03-04

### CLI

- `arizuko config <instance> user list|add|rm|passwd` for local user management;
  passwords hashed with argon2; values passed via env vars to prevent shell injection

### Auth

- `POST /auth/refresh`: token rotation — issues new access + refresh token pair,
  invalidates old refresh token
- `POST /auth/refresh` JWT now carries correct user name (was using sub string)
- OAuth providers deferred to `specs/v3/auth-oauth.md`

### Specs

- `specs/1/3-auth.md`: updated to reflect v1 implementation

---

## [v0.2.3] — 2026-03-04

### Gateway

- Email channel: IMAP IDLE loop with SMTP reply threading, routes to main
  group; enabled by `EMAIL_IMAP_HOST` config
- `send_file` Discord support: `sendDocument` via `AttachmentBuilder`
- `send_file` WhatsApp support: `sendDocument` via baileys document message
- `src/mime.ts`: shared `mimeFromFile()` helper using file-type (magic bytes)
- `email_threads` table in DB: `getEmailThread`, `getEmailThreadByMsgId`,
  `storeEmailThread` for SMTP reply threading
- Explicit `DATA_DIR`/`HOST_DATA_DIR`/`HOST_APP_DIR` env vars replace brittle
  `/proc/self/mountinfo` host-path detection; gateway cwd stays at `/srv/app`

### Agent skills

- Migration 004: enforce `send_file` for file delivery (CLAUDE.md rule);
  `send_file` accepts any `/workspace` path, not restricted to `media/`

---

## [v0.2.2] — 2026-03-04

### Gateway

- Outbound file sending: `send_file` MCP tool lets agents send files to users
  as document attachments (Telegram); IPC `type:'file'` handler with
  path-safety check against GROUPS_DIR
- Session error eviction: on agent error output, session ID is not persisted;
  on error status, the session pointer is removed from DB (JSONL kept on disk)
  so the next retry starts a fresh session rather than re-entering a corrupted one
- Inject `NANOCLAW_IS_MAIN` into agent `settings.json` on every spawn (was
  never set, so agents always saw it as empty)

### Agent skills

- `migrate` skill: replace `/workspace/global` dir-existence check with
  `NANOCLAW_IS_MAIN != 1` check — the dir always exists due to Dockerfile
  mkdir, making the old check unreliable for main-group detection

---

## [v0.2.1] — 2026-03-04

### Agent runner

- Progress updates: every 100 SDK messages, emits last assistant text snippet
  to the channel so users see activity on long runs
- `error_max_turns` recovery: resumes the session with `maxTurns=3` and asks
  Claude to summarise what was accomplished and what remains, then prompts the
  user to say "continue"

---

## [v0.2.0] — 2026-03-04

### Slink web channel

- Added `POST /pub/s/:token` endpoint — web channel for groups registered as `web:<name>`
- Served `REDACTED.js` client widget at `/pub/REDACTED.js`
- Verified JWT signatures (HS256) for authenticated senders
- Added anon/auth rate limiting via `SLINK_ANON_RPM` / `SLINK_AUTH_RPM` config
- Supported `media_url` attachments with MIME type guessing
- Added SSE stream at `/slink/stream` for agent-to-browser push
- Added `slink_token` column on `registered_groups`; added `generateSlinkToken` helper
- Fixed expired JWT treated as anon (now returns 401)
- Fixed slink deduplication and SSE error logging

### Auth layer

- Added auth DB schema: `users`, `sessions`, `oauth_accounts` tables
- Added auth query functions: `createUser`, `getUserByProvider`, `createSession`, etc.
- Added `AUTH_SECRET` config constant for JWT signing
- Added web UI auth spec at `specs/1/3-auth.md`

### Whisper sidecar

- Added self-contained `arizuko-whisper` docker image, deployed via Ansible
- Added `whisperTranscribe` helper with 30s abort timeout
- Updated voice and video handlers to use shared whisper endpoint

### Mime pipeline

- Added attachment enrichment before agent dispatch
- Added handler registry: voice, video, image handlers
- Dispatched handlers in parallel with `allSettled` (partial failure safe)
- Added MIME type detection, file save, and annotation lines

### Workspace and agent identity

- Mounted `/workspace/self` read-only to expose full arizuko source to agent
- Replaced `SOUL.md` with ElizaOS-style `character.json`
- Added per-query field randomisation and global override merge in `character.json`
- Split `web/pub/` as unauthenticated boundary; `/pub/` prefix is public

### Skills and migrations

- Added `self` skill: agent introspection — layout, skills, channels, migration version
- Added `migrate` skill: main-group skill sync + migration runner across all groups
- Added migration system: `container/skills/self/migrations/` with versioned files
- Added migration 001: move `web/` root files to `web/pub/` per new layout convention
- Added YAML frontmatter to `web/SKILL.md`
- Updated `info/SKILL.md` to report migration version and warn if migrations pending

### Build

- Added `container/Makefile` for `arizuko-agent` image builds
- Added `sidecar/whisper/Makefile` for `arizuko-whisper` image builds
- Root `make image` now builds only the gateway (`arizuko`)

### Testing

- Added testability seams: `_initTestDatabase`, `setDatabase`, `_overrideConfig`
- Reached 306 tests across 22 files

---

## [v0.1.2] — 2026-03-01

### Added

- Signal-driven IPC: gateway sends SIGUSR1 after writing IPC file; agent
  wakes immediately, falls back to 500ms poll — eliminates busy-waiting

### Fixed

- Race condition in wakeup/timer assignment in agent IPC polling
- `cleanupOrphans` dual-filter restored to OR logic (AND regression in v0.1.1)
- Typing indicator now stops correctly when agent finishes responding
- Extracted `signalContainer` and `scanGroupFolders` helpers to deduplicate
  signal-sending logic

---

## [v0.1.1] — 2026-03-01

### Added

- Skills consolidated into `container/skills/`; seeded once per group on
  first container run
- Vite web server integrated into gateway startup via IPC restart
- Web app template seeded from `template/web/` on `arizuko create`
- Group management CLI (`arizuko group list|add|rm <instance>`)
- `hello` and `howto` skills bundled in agent image
- Pre-commit hooks: prettier, typecheck, hygiene (`.pre-commit-config.yaml`)
- Makefile targets: `build`, `lint`, `test`
- Discord channel via discord.js (`channels/discord.ts`)
- Env-based channel toggling: Telegram by `TELEGRAM_BOT_TOKEN`, Discord by
  `DISCORD_BOT_TOKEN`, WhatsApp by `store/auth/creds.json` presence

### Changed

- `TELEGRAM_ONLY` flag removed; channel selection is token/credential-driven
- Unified `ChannelOpts` type across all three channel modules

### Fixed

- Render markdown as HTML in Telegram; keep typing indicator alive during
  long responses
- Agent-team subcontainers cleaned up on gateway startup
- Fallback to script-relative template dir when not running inside container
- Docker-in-docker mount paths and agent container write permissions
- Bootstrap chicken-and-egg: `group add` now creates DB schema if missing
- `appDir` used for skills source path instead of `process.cwd()`

---

## [v0.1.0] — 2026-03-01

Initial arizuko release — nanoclaw fork with Telegram support and
multitenant instance model.

### Added

- Fork of nanoclaw at upstream v1.1.3
- Telegram channel (`channels/telegram.ts` via grammy)
- `arizuko` bash entrypoint: `create`, `group`, and instance-run commands
- Per-instance data layout: `/srv/data/arizuko_<name>/`
- systemd unit file templating via `arizuko create <name>`
- `container/agent-runner/` in-container Claude Code entrypoint
- Docker-in-docker host path translation (`detectHostPath()` via
  `/proc/self/mountinfo`)

### Inherited from nanoclaw v1.1.x

- Mount project root read-only (container escape prevention)
- Symlink and path-escape blocking in skills file ops
- `fetchLatestWaWebVersion` to prevent WhatsApp 405 failures
- Host timezone propagation to agent container
- `assistantName` passed to agent (was hardcoded as `'Andy'`)
- Idle preemption correctly triggered for scheduled tasks
