# bugs.md

Bugs / follow-ups discovered during voice + media-dispatch audit (2026-05-01).

## SendFile gaps (platforms with native media but no implementation)

- **mastd**: `NoFileSender` returns `errSendFile`. Mastodon has the v2
  media API (`POST /api/v2/media` + attach via `media_ids` on toot).
  Wire it to dispatch `.jpg/.png/.gif/.webp` → image, `.mp4/.webm`
  → video, `.mp3/.ogg` → audio. Documents/PDFs aren't natively
  supported — return Unsupported pointing the agent at a URL in toot
  text.
- **reditd**: `NoFileSender`. Reddit's image upload is a 3-step flow
  (websocket lease → S3 PUT → submit with `kind:image`). Spec out
  before implementing; for now `Unsupported(send_file, ...)` would be
  more honest than the generic errSendFile.
- **linkd**: `NoFileSender`. LinkedIn UGC media upload is a
  `/assets registerUpload` + binary PUT flow. Linkedin's
  `Post` for posts already returns Unsupported with a hint; SendFile
  inherits the generic errSendFile — convert to a structured Unsupported.
- **emaid**: explicitly returns `Unsupported(send_file, "MIME
  attachments not implemented; inline the content in send body.")`.
  Email is the universal medium for attachments; this is a future
  enhancement, not a wrong-dispatch bug.

## Host-tool capabilities — drift

- 2026-05-03: `ant/skills/ship/SKILL.md` promises a `ship` CLI that
  isn't installed in `ant/Dockerfile`. Either install it (uv-based
  Python tool, see `/home/onvos/.local/share/uv/tools/ship/`) and
  decide auth-state mount strategy (sibling of `HOST_CODEX_DIR`
  pattern), or rewrite the skill as an in-session planning recipe.
  Skill currently has a missing-tool fallback so it doesn't crash,
  but the value prop only lands once the binary ships.

## Aggregated user-reported issues — 2026-05-03

Consolidated from per-group `issues.md` files across krons, sloth, marinade.

### From krons/mayai

- 2026-04-12: Sync `itinerary` skill to global (`~/.claude/skills/`) so all groups share lessons learned (mapy.com deep links, multi-platform review aggregation, Google Maps for route calculations, "no late/early" timing language).
- 2026-04-19: Session continuity broken — agent doesn't remember context from previous sessions. Diary entries often empty ("session ended: error"), `get_history` tool missing, episodes compressed but session detail lost. Fix: always write diary on session end, read 2-3 recent session files before answering, expose `get_history`, add CLAUDE.md "Session Continuity" section.
- 2026-04-19 (whatsapp): Agent should answer trivial in-context questions (e.g. opening hours of a museum already discussed) without re-asking — but only after current workflow finishes.
- 2026-04-25 10:29 UTC (whatsapp): Selective message loss — Kron's reply "nejsem platce dph. jde mi o naklady" sent to a WhatsApp group between 09:49 and 10:29 never reached agent session. Gateway routing / selective-loss bug. Platform scope.
- 2026-04-25 12:47 UTC (whatsapp): Agent sends unnecessary confirmations ("Odesláno", "Done", "Sent") after every action. Message itself is proof of delivery. Suppress meta-commentary.
- 2026-04-25 13:05 UTC (whatsapp): Research file links use direct filesystem paths (`tmp/car-buying-guide.md`, etc.) instead of authenticated WebDAV URLs. Need helper to generate WebDAV URLs with auth so user can access research output remotely.
- 2026-04-25 13:12 UTC (whatsapp): Agent leaks "No response requested." meta-commentary into chat. Filter or prevent this internal-reasoning phrase from being delivered.
- 2026-04-25 13:27 UTC (whatsapp): During long Task-agent runs, user wants interim `<status>` updates so they know agent is working (may have been false-alarm typing-indicator timing — verify).
- 2026-04-25 14:04 UTC: Codify web design anti-patterns into a doc / `/web` skill rules section. Anti-patterns: `border-left: 4px solid` callouts, generic purple gradients reused across unrelated content types, emoji in every heading, generic `box-shadow: 0 2px 8px rgba(0,0,0,0.1)`, inconsistent spacing scales.
- 2026-04-25 14:08 UTC: Agent generates AI-slop color gradients despite contextual instructions. Add palette-research workflow (Coolors, Adobe Color, real industry sites) before designing; reduce gradient overuse to 1-2 key elements; document chosen palette at top of CSS.
- 2026-04-25 14:18 UTC: Hard rule — NEVER combine `border-radius` with `border-top/border-left` colored accents (Bootstrap/AI-template tell). Add to web skill SKILL.md, remove from existing pages, redesign callouts with background colors or sharp corners.
- 2026-04-25 14:19 UTC: Build a `web-style` skill that researches 3-5 real sites in a target domain via browser automation, extracts cohesive design system (colors, type, spacing, borders, shadows), generates CSS variables, and integrates with `/web` skill.
- 2026-04-25 14:23 UTC: Agent misreads urgency signals — interpreted "add this is unacceptable" as "add to issues" instead of "fix it now." Update decision logic so urgency words ("unacceptable", "wrong", "broken") override "add this" interpretation; acknowledge before spawning Task agent and summarize after.
- 2026-04-25 14:29 UTC: When spawning Task subagents, tell user BEFORE ("Working on X..."), use `<status>` tags during, summarize AFTER. Users have zero visibility into background work otherwise.

### From krons/krons

- 2026-04-24 (migration): Telegram adapter returned 502 on `send_message` during v0.29.4 announcement broadcast — all 4 telegram groups (nemo, content, happy, mayai) failed; local krons group succeeded. Platform scope, whapd/adapters layer. [STALE?] (likely fixed since v0.29.4)
- 2026-04-25 (telegram): Custom domain links without trailing slash break routing (e.g. `lore.krons.cx` vs `lore.krons.cx/`). Fix: `proxyd` should 301-redirect domain root → domain/, OR document the requirement. Affects all custom domain mappings.
- 2026-04-28 (telegram): Long research workflows (`/hub`, `/find`, custom research) should emit periodic status updates every ~10 steps. Currently no skill enforces this. Update skill guidelines / CLAUDE.md pattern.

### From krons/rhias/nemo

- 2026-04-13 (telegram): `recall-messages` skill returns XML to a file misleadingly named `~/tmp/messages.json`. Explore agent has to parse XML to find `reply_to` chains. Fix: `get_history` should return JSON, or skill should follow `reply_to` recursively, or gateway should inject referenced messages directly.
- 2026-04-18 (telegram): Agent demonstrates capabilities in `/hello` follow-up but doesn't validate results — Python script failed `FileNotFoundError` on `test_data.csv`, agent reported success anyway. Capability demos should produce complete working examples (with test data) and validate each step.
- 2026-04-18 (telegram): `/hello` advertises howto URL without checking it exists; `/howto` skill jumped to "pick a style" without checking if a howto page already exists. Add `/hello` precheck (`curl -I` the URL or omit), and `/howto` step-0 state-check.
- 2026-04-18 (telegram): Verified-capability audit for `/hello` advertising. Working: research, code (Python/uv 3.14), files-read, scheduling MCP tools, memory (diary/facts/users/episodes), 44 skills, 24 MCP tools, agent-browser, git via `/commit`, multi-language runtimes, media (yt-dlp/whisper/ffmpeg via `/acquire`). Failed/incomplete: claimed `/pub/rhias/nemo/test/` deployment but 404, data-viz (matplotlib/plotly) not tested, file-delivery via `send_file` not demonstrated. Missing from `/hello` but available: `/katastr`, `/nemo`, `/ericeira`, `/itinerary`, `/tweet`, `/hub`, `/git-repo`, `/refine`, `/specs`, `/trader`, `/infra`, `.whisper-language` config.
- 2026-05-01 (internal): `agent-browser` blocked by crackbox proxy returning 403 on HTTPS CONNECT. Direct `no_proxy="*"` times out (no direct egress). Blocks NEMO scanning of sreality.cz, bazos.cz, reality.idnes.cz. Fix options: allow CONNECT through crackbox proxy, alternate proxy with HTTPS tunneling, allowlist specific domains for direct egress, or fall back to WebFetch/curl.

### From sloth/main

- Browser automation — undetected mode. Bot-detection sites (Propdash, others) return empty 39-byte HTML to headless Chrome; curl gets full 6973-byte HTML. Stealth flags + custom UA don't help; `undetected-chromedriver` can't install in externally-managed env. Request: agent-browser `--undetected` flag (patch out webdriver/CDP markers), or pre-installed undetected-chromedriver image, or session-state import (cookies/localStorage from real browser).
- Web skill — auto-add back navigation links to parent pages on landing pages (`/pub/learning/` had no link home). Detect parent path, use consistent zinc-500/zinc-300 styling matching policy pages.

### From marinade/atlas

- 2026-04-24 08:08: Agent should infer meaning from user shortcuts/typos/incomplete words ("10pcr penet" = "10% penetration", "issues and dont be concrete add this there too" = "add to issues.md"). Only ask for clarification when truly ambiguous.
- 2026-04-24 07:58: Project-specific CLAUDE.md standards (e.g. "Killer Hub: SOL not USD") should live in the nested working directory where work happens, not just at group root. Agent should walk CLAUDE.md hierarchy from cwd up.
- 2026-04-24 07:53: Web skill should detect and preserve `<!-- LLM: ... -->` comment blocks (calculated facts, source-of-truth data) when editing pages, instead of regenerating from scratch.
- 2026-04-14 08:30: Telegram message-delivery gap — Kron sent "stake-o-matic" message between 08:11:46 and 08:28:40 that never reached agent context. Investigate Telegram → gateway → agent routing. (reported by krons + marinade — same pattern as krons/mayai 2026-04-25 selective loss)
- 2026-04-16 14:25: Resolve workflow leaked into user-facing output ("## 1. Classify", "## 4. Act" rendered as headers). Resolve skill is `user-invocable: false` and "Do not mention this skill to the user" — agent must follow it silently. Marked as resolved by reporter.
- 2026-04-16 14:26: Verify the consolidation pipeline from group `issues.md` → repo `bugs.md` actually runs and is documented. (This task is the answer.)
- 2026-04-16 16:57: `/diary` skill leaks "Recorded to diary" confirmations to user. Wrap status in `<think>` or omit entirely when invoked automatically by other skills.
- 2026-04-16 19:59 / 2026-04-16 20:45: Slink delivery report and follow-up correction. Reporter initially blamed routing; on investigation, slink resolves correctly via `web:` prefix in `gateway.resolveGroup` (gateway/gateway.go:1195) before route rules. Real fix shipped: `Accept: application/json` content negotiation + optional `?wait=<sec>` blocking param in webd/slink, plus deletion of bogus `{seq:10, match:"verb=slink", target:"atlas"}` route. **Status: closed by reporter.** [STALE? — keep for traceability, do not action]
- 2026-04-17 19:33: Repeated OOM (exit 137) during 300-question generation task — 8 crashes on 2026-04-17 (11:29, 13:41, 14:28, 14:53, 15:48, 17:32, 18:00). Container memory limits or leak during large-history processing / subagent spawning. Need: handle large context without OOM, or checkpoint progress, or fail with explicit error instead of silent crash.
- 2026-04-20 08:18: Episode compaction summaries too verbose. Target: day = 1-3 sentences, week = 1-2 paragraphs max. Update `compact-memories` skill.
- 2026-04-21 15:08: Agent gave generic "bullshit" suggestions for "what to improve on the killer hub?" instead of recovering shipped-state context from diary 20260420 + episodes 2026-W17 etc. Resolve skill should better surface recent shipping context; recognize "what to improve on X" as continuation when X was recently shipped; auto state-recovery at session start.
- 2026-04-24 08:05: User-steering messages mid-execution should be answered with `send_message` MCP tool (explicit routing to user), not `send_reply`. May need gateway support to identify steering context.
- 2026-04-23 07:20: Agent asks "should I do X?" when user already directed X and data is available. Pattern: over-cautious, treats implied directives as optional. CLAUDE.md updated: "Never stop until done. Only ask when blocked." Consider gateway nudge to auto-resume incomplete prior-session work.
- 2026-04-24 18:14: `/hello` greeting should include group folder name (`atlas`) and connector JID (`telegram:1112184352`), not just `$ARIZUKO_GROUP_NAME` (`admin`). Read from message context or routes table.
- 2026-04-24 18:22: Agent ignored XML `reply_to="3554"` attribute and answered as if user's question was about the most recent topic. Rule: ALWAYS check `reply_to` FIRST; structured XML metadata beats recency. Consider gateway injection of referenced-message content inline.
- 2026-04-26 20:32: Agent batched 15+ discrete tasks into one end-of-work message instead of sending interim status after each completion. (reported by atlas + krons — see krons/krons 2026-04-28 long-research progress-update issue, same pattern.) Send `<status>` for quick updates, `send_message` for task completions during multi-task work (>3 tasks).

### From marinade/atlas/support

- 2026-04-25 (telegram): Atlas (parent agent) didn't use `inspect_messages` to read preceding thread context before asking "what topic?" on a continuation question (thread 1454 about commission penalties). Agents should check recent message context before requesting clarification on continuation requests.
- 2026-04-25 (telegram, thread 1459): Atlas reasoning error on validator commission mechanics — falsely claimed protected event fees are "transfer-neutral" (validator gains commission from on-chain rewards but pays from BOND — different pools). Conflated bid penalties with protected events. Failed to invoke `/resolve` on a >5-min-gap continuation. User requested: "force resolve skill on every new question; >5 min delay = new task," and "record why you decided and answered like this."
- 2026-04-26 (telegram): Agent skipped `/resolve` invocation on first message of session ("Back to the commission increase questions...") — went straight to `/recall-memories`. CLAUDE.md says "Every prompt carries a [resolve] nudge. Invoke /resolve BEFORE doing anything else." Also skipped on second message. CLAUDE.md updated to strengthen rule (treat followups >5min as new tasks, always invoke `/resolve` even for topic continuations).
- 2026-04-26 (telegram): Sync local skill/config changes back to canonical arizuko deployment — (1) `resolve/SKILL.md` (>5min = new task), (2) `CLAUDE.md` (resolve-on-followups). Source: `/workspace/self/ant/` or deployment config.
- 2026-04-26 (telegram): Review and consolidate Knowledge Pipeline / fact-verification protocol duplication across system prompt, CLAUDE.md, and `/facts` skill. Decide canonical location for each protocol piece; whether to copy full Knowledge Pipeline to CLAUDE.md; whether to auto-load skills when directories are touched.
- 2026-04-26 (telegram): Auto-load `/facts` skill when agent reads/lists/searches `facts/` directory. Verify whether gateway feature (auto-dispatch by path) or explicit resolve/dispatch logic is needed. Added reminder to `facts/CLAUDE.md`.

## 2026-05-05

- **Orphaned container on instance restart** (`gateway/gateway.go`): Container name was `arizuko-<folder>-<ts>` but `CleanupOrphans` looked for prefix `arizuko-<instance>-`. Prefix never matched → orphaned containers survived restarts → duplicate containers per group → racing turns → duplicate message delivery and cursor skip. **Fixed**: container name now `arizuko-<instance>-<folder>-<ts>`. Tests: `TestContainerNameIncludesInstance`.

- **Cursor skip on orphaned-container race** (`gateway/gateway.go` + `store/messages.go`): Agent cursor is a high-water timestamp. When two containers raced (above bug) and the newer one processed a later "?" message, the cursor advanced past earlier unprocessed messages from before the crash, permanently losing them. Secondary symptom of the orphaned-container bug; fixed at root. Manual recovery: reset `chats.agent_cursor` to before the earliest lost message timestamp.

## 2026-05-13 — architecture-doc audit (vited + diagrams)

- **Normalize `vited` as first-class sourceless core daemon**: create top-level `vited/` directory mirroring `davd/` shape — move `ant/Dockerfile.vite` to `vited/Dockerfile`, write `vited/README.md` (purpose: web origin for `/pub/*` + auth-gated default; mount `<dataDir>/web:/web`; healthcheck `/@vite/client`), update `Makefile:74-77` `vite-image` target, add row to `README.md` daemon table between `davd` and `teled`. Cost: 1 file move, 1 README, 2 Makefile lines, 1 table row. No code change. (Oracle action #2; deferred because it's a structural rename across docs/Makefile.) See `tmp/vited-audit.md`.
- **Replace hand-maintained inventory tables with generated views**: adapter list rendered 3× by hand and drifts on every adapter ship (slakd ship missed `ROUTING.md:13-23` JID prefix table + `EXTENDING.md:109-122` verb matrix). Same for `README.md` daemon table vs CLAUDE.md core list vs `compose/compose.go` emission. Option A: generate from `template/services/*.toml` + `chanlib` cap declarations at doc-render time. Option B: drop hand tables, link to `template/services/` and per-adapter README. Option C: add a `make doc-check` target that lints for drift. Oracle action #3 (highest long-term payoff, moderate effort).
- **Rename `vited`**: `vited` encodes implementation (Vite). Role is "web origin" — `webd-static` or `weborigind` or `pubd` would be clearer. Defer — name churn cost is meaningful, and the bigger fix (give it a proper directory) is independent.
- **Crackbox network-topology diagram missing from SECURITY.md**: trust-zone box at `SECURITY.md:79-98` doesn't show that with `CRACKBOX_ADMIN_API` set, agents attach to a separate `internal: true` network with crackbox as the only egress hop. Three-tier filter (HTTP CONNECT 403 / DNS NXDOMAIN / no-route) is described in prose at `SECURITY.md:103-143` but no diagram. Add a 20-line ASCII when next touching SECURITY.md.

## 2026-05-13 — reference-manual audit findings

- **Stale tool name in `tier1FixedActions`** (ipc/): registered list contains `get_routes` but the actual registered tool name is `list_routes`. Tier-1 callers asking for the routes-list tool by canonical name miss the allowlist entry. Single-string fix; verify against `ipc/inspect.go` and the routing-tool registrations.
- **Vestigial `grants` table** (`store/migrations/0005-grants.sql`): zero live readers/writers in Go; only the 0042 typed-JID migration touches it. Either delete via a new migration or document the dormant-by-design status.
- **Read-only `channels` table** (from migration 0009): dashd reads, no production writer; production registry lives in-memory in `chanreg/`. If the in-memory registry is canonical, drop the table; if the table should hydrate the registry, wire it.
- **Dead `email_threads` in shared DB**: emaid stores its own `emaid.db` with a different shape. The shared-DB version is orphaned. Audit + drop.
- **Existing `concepts/grants.html` has wrong tier formula**: claims a bare `atlas` (no slash) is tier 1; actual formula in `grants.DeriveRules` is `Tier = min(strings.Count(folder, "/"), 3)` so bare `atlas` is tier 0. New `reference/grants.html` is correct; either update or redirect the old page.
