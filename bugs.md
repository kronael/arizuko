# bugs.md

Open-issues queue. Resolved entries are moved to `.diary/` — see e.g.
`.diary/20260525.md` for the most recent cleanup. New finds: record
date + scope + severity + suspected fix-path; don't auto-fix during
general audits (CLAUDE.md bug-triage protocol). Workflow: `/bugs` skill.

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

## Adapter cap maps — drift

- 2026-05-28 (reditd, low): `reditd/client.go` implements a working
  `Delete()` (own posts/comments via Reddit API), but `reditd/main.go`'s
  `Caps` map omits `"delete": true`. `chanreg.HTTPChannel.Delete` gates
  on `HasCap("delete")`, so the `delete` MCP verb returns
  `UnsupportedError` for reddit despite the impl existing. Fix: add
  `"delete": true` to reditd's cap map + a `delete` round-trip test.
  Spec 5/Z coverage table marks reditd `delete ✗` to match shipped
  behavior. Behavior change → not a refine-pass edit.

- 2026-05-28 (grants, low): pin verbs (`pin_message`/`unpin_message`/
  `unpin_all`) are absent from `grants/grants.go` `platformActions`, so
  only tier-0 (`*`) holds them by default; tier-1/2 need an explicit
  grant. If pin should be a tier-1/2 default (like send/reply), add the
  three verbs to `platformActions`. Product decision, not a bug —
  flagged by the 5/Z refine pass; 5/Z spec documents the shipped behavior.

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

## 2026-05-13 — architecture-doc audit (vited + diagrams)

- **Normalize `vited` as first-class sourceless core daemon**: create top-level `vited/` directory mirroring `davd/` shape — move `ant/Dockerfile.vite` to `vited/Dockerfile`, write `vited/README.md` (purpose: web origin for `/pub/*` + auth-gated default; mount `<dataDir>/web:/web`; healthcheck `/@vite/client`), update `Makefile:74-77` `vite-image` target, add row to `README.md` daemon table between `davd` and `teled`. Cost: 1 file move, 1 README, 2 Makefile lines, 1 table row. No code change. (Oracle action #2; deferred because it's a structural rename across docs/Makefile.) See `tmp/vited-audit.md`.
- **Replace hand-maintained inventory tables with generated views**: adapter list rendered 3× by hand and drifts on every adapter ship (slakd ship missed `ROUTING.md:13-23` JID prefix table + `EXTENDING.md:109-122` verb matrix). Same for `README.md` daemon table vs CLAUDE.md core list vs `compose/compose.go` emission. Option A: generate from `template/services/*.toml` + `chanlib` cap declarations at doc-render time. Option B: drop hand tables, link to `template/services/` and per-adapter README. Option C: add a `make doc-check` target that lints for drift. Oracle action #3 (highest long-term payoff, moderate effort).
- **Rename `vited`**: `vited` encodes implementation (Vite). Role is "web origin" — `webd-static` or `weborigind` or `pubd` would be clearer. Defer — name churn cost is meaningful, and the bigger fix (give it a proper directory) is independent.
- **Crackbox network-topology diagram missing from SECURITY.md**: trust-zone box at `SECURITY.md:79-98` doesn't show that with `CRACKBOX_ADMIN_API` set, agents attach to a separate `internal: true` network with crackbox as the only egress hop. Three-tier filter (HTTP CONNECT 403 / DNS NXDOMAIN / no-route) is described in prose at `SECURITY.md:103-143` but no diagram. Add a 20-line ASCII when next touching SECURITY.md.

## 2026-05-13 — reference-manual audit findings

- **Stale tool name in `tier1FixedActions`** (ipc/): registered list contains `get_routes` but the actual registered tool name is `list_routes`. Tier-1 callers asking for the routes-list tool by canonical name miss the allowlist entry. Single-string fix; verify against `ipc/inspect.go` and the routing-tool registrations.
- **`concepts/grants.html` tier formula** — superseded by deeper bug below (two tier formulas collide). Page itself documents one tier model while agent runtime uses another. Fix is at the code level, not the doc level.

## Tier-formula collision (surfaced 2026-05-25)

Two competing formulas compute "tier" for the same folder:

- `auth/identity.go:19` — `Tier = min(strings.Count(folder, "/"), 3)`.
  Bare `atlas` (no slash) → tier 0. Used by `auth.Authorize`,
  `auth/policy.go` (Structural and other authz checks).
- `container/runner.go:70` — `strings.Count(folder, "/") + 1` (when
  not root). Bare `atlas` → tier 1. Used to set `ARIZUKO_TIER` env
  var injected into agent containers. Agent reads this and follows
  the tier-1 web-publishing recipe in `~/.claude/CLAUDE.md`.

So `atlas` is simultaneously tier 0 (per auth) and tier 1 (per agent
env). They encode different concepts under the same word —
"privilege class" vs "nesting depth." Authz is looser than the
agent thinks; the agent applies a recipe (Case B: subdomain vhost)
that authz wouldn't enforce.

Concrete symptom: atlas/strengths failure traced 2026-05-25 — agent
saw `tier=1` env, applied subdomain recipe, subdomain didn't exist,
files went to wrong path. v0.45.10 patched the recipe to be
defensive; the formula split itself remains.

Fix: pick one canonical formula, replace the other call sites,
align all docs. Likely the `runner.go` form (privilege class) is
the canonical one for both env injection AND authz — `min(...,3)`
collapses tiers 3+ into "no surface" which is a different concern.
Worth a dedicated spec.

## dashd nested-folder path routing (2026-05-16, found via playwright)

- **dashd admin endpoints can't address nested folders.**
  `mux.HandleFunc("GET /dash/groups/{folder}/settings", ...)` — Go's
  `{folder}` path-value matches a single segment. `/dash/groups/solo/inbox/settings`
  falls through to the list handler and silently returns the wrong page
  with status 200. Affects: settings, delete, settings POST. Fix:
  use `{folder...}` (trailing wildcard) and trim the suffix.
  Production groups like `solo/inbox` or `corp/eng/sre/oncall` are
  unreachable by these endpoints today. Caught by
  `tests/dashd-playwright/groups.spec.ts` (working around via flat seed).

## Episode compaction audit (2026-05-25, all instances)

Three-instance audit (krons + marinade + sloth) found systemic
silent-success in compact-memories cron skill. Code/skill fixes
shipped in v0.45.9 (commit `ff2c0eb`) — see `.diary/20260525.md`.
Operator-data items below remain open.

- **L — Schedule key naming inconsistency**. `<folder>-mem-N` vs
  `task-<ts>-<hash>` IDs. Two creation paths. Not addressed in v0.45.9;
  cosmetic only.

### Operator-data (left as zero-output per user)

- **D — JID identity drift**. `Coach` (capital) vs `coach` folder on
  sloth; `Strengths` non-canonical on marinade; `local:main` orphaned
  after sub-group split on sloth. Per user direction (2026-05-25):
  these tasks produce zero output but are harmless — DO NOT delete.

### Operator-data (still to fix)

- **I — Missing schedules**. sloth `main/content` is an active group
  (938KB jsonl, 14 diary days) with zero compact-memories scheduled
  tasks. Action: provision the 5 cron tasks (episodes day/week/month,
  diary week/month). Investigate whether onbod/SetupGroup auto-provisions
  these for new groups; if so, why did `main/content` miss them.

### Backfills queued (post-v0.45.9 deploy)

For each (instance, group, period) tuple where the audit found a
sources-present-but-output-missing case, queue a one-off invocation
using the new override arg. Target list assembled from audit reports;
execution sequenced after v0.45.9 image deploy.

## Tier-2 web publishing recipe breaks when WEB_PREFIX is unset (2026-05-25)

Found via atlas/strengths confusion eval: when a tier-2 group runs on an
instance where `WEB_PREFIX` is unset (single-vhost deployment, no
`<prefix>.<host>` subdomain configured), the Case-C tier-2 recipe in
`ant/CLAUDE.md ## How to publish a web page` emits broken URLs:
`https://.fab.krons.cx/...` and writes files to wrong paths
(`/workspace/web/<groupname>/<app>/`).

The case check should fall through to Case A (root `pub/`) when:
- `WEB_PREFIX` is empty OR unset
- `/workspace/web/pub/` exists

Today the agent reasons through tier from depth and picks the matching
Case directly, ignoring the env-var sanity check. Worth tightening the
case-selection logic in the platform CLAUDE.md so a single-vhost
deployment without a subdomain configured doesn't dispatch agents to
the tier-2 recipe.

Operator-data fix landed for atlas/strengths
(`/srv/data/arizuko_marinade/groups/atlas/strengths/CLAUDE.md` —
explicit override disabling the tier-2 recipe). Platform fix queued
for triage.

## Discord thread voice/file leak to parent channel (2026-05-25)

Carryover from the thread fetch_history fix (commit `098996c`, see
`.diary/20260525.md`): `SendVoice` and `SendFile` signatures in
`chanlib.BotHandler` don't carry a `topic` field, so voice/file replies
from within a Discord thread land in the **parent channel**, not the
thread. Text replies via `Send` route correctly via
`SendRequest.ThreadID`. Low priority — voice/file in discord threads is
rare; needs a signature extension across all adapters when addressed.

## whapd_krons session-401 restart loop (2026-05-25, ops)

`arizuko_whapd_krons` is in a restart loop every ~60s. Pattern: WHATSAPP
session token invalidated (`code=401`, `"session invalidated, delete
auth dir and re-pair"`), container exits, systemd restarts, repeat.
Memory note `reference_whapd_pairing` from 2026-05-21: pairing code
`LMDTPW8J` was issued to user's phone (+420735544891); user hasn't
completed the pairing flow on the device side. **Status: waiting on
user phone action; not a platform bug.** Side effect: `arizuko_gated_krons`
emits WARN every ~30s about the failed channel health check (DNS
miss → 503 disconnected). No traffic impact (whatsapp routes won't fire
anyway with no session). To clear: complete pairing on the phone via
LMDTPW8J, OR remove whapd from `template/services/whapd.toml` for the
krons compose if WhatsApp on krons is no longer wanted.

## 900s container timeout — design limit (2026-05-25)

900s is the Claude Code SDK abortion cap; user-visible as `errored=1`
on long parallel-research turns. Not a bug — see
`ARCHITECTURE.md ## Long-running tasks — the 900s container timeout`
for mitigation hints (split tasks across turns, checkpoint to
`~/facts/`, fewer parallel subagents).

## Compaction — sloth has zero compact-log entries post-v0.45.9 (2026-05-25)

v0.45.9 introduced `.compact-log.jl` artifacts under
`<group>/episodes/` and `<group>/diary/`. After redeploy:

- krons: 9 WRITE + 1 SKIP_NO_SOURCES (8 diary-month + 1 month written)
- marinade: 2 WRITE + 9 SKIP_NO_SOURCES (3 diary-month written)
- **sloth: 0 entries — no compaction has executed since v0.45.9 deploy**

Likely root cause: sloth's scheduled_tasks point at non-canonical
chat_jids (`Coach` capital vs lowercase `coach` folder; `local:main`
after sub-group split; `Strengths` shape). The cron prompts dispatch
but the agent container for those JIDs never spawns — no agent =
no compact-log. Per prior user direction (bugs.md "Operator-data —
left as zero-output"): **do not delete** these tasks. But the
secondary effect is sloth gets no compaction at all. Tension between
"don't delete" and "system should be perfect." Worth a re-decision:
either route sloth's cron tasks to a valid agent (e.g. rename `Coach`
chat_jid to `coach`, migrate `local:main` to a sub-group, provision
proper task for `main/content`), or accept sloth runs without
compaction indefinitely.

## Diary/month — partial coverage after v0.45.9 (2026-05-25)

April 2026 diary-month rollups produced for 7 groups across krons +
marinade. Still missing for: `krons/krons` (root), `atlas/tom`,
`atlas/content`, all sloth groups. Likely a mix of:

1. Groups where the May-1 04:00 UTC diary-month cron hasn't fired
   yet on a post-v0.45.9 image. Will fix itself on June 1.
2. Groups where the cron task is misconfigured (sloth's JID drift).
3. Groups where source data (`diary/week/*.md`) doesn't exist for
   the period — legitimate SKIP_NO_SOURCES.

Backfill mechanism exists per v0.45.9 (`/compact-memories diary
month 2026-04` override arg). Not auto-queued. Operator can run
manually per affected group.

## marinade emaid IMAP auth failure — 8 days continuous (2026-05-25)

`arizuko_emaid_marinade` has been logging `imap: NO [AUTHENTICATIONFAILED]
Invalid credentials (Failure)` against `imap.gmail.com:993` every ~60s
since **2026-05-17 18:13 UTC** (8 days, 11,463 error rows). Account:
`im.atlas.the.alpha@gmail.com`. Most likely cause: Gmail app-specific
password was revoked or 2FA changed.

Effect: marinade's email channel is dead (no inbound, no outbound),
emaid container is healthy but performing zero useful work. Burns CPU
on the auth-retry loop.

To clear: operator regenerates a Gmail app password for the account,
updates `EMAIL_PASSWORD` in `/srv/data/arizuko_marinade/.env`, runs
`sudo systemctl restart arizuko_marinade`. Alternatively, if email on
marinade is no longer wanted, remove emaid from the marinade compose
profile (or set `EMAIL_ACCOUNT=` empty so compose skips it).

Not present on krons or sloth (0 IMAP errors). marinade-only.

## Telegram blocked-bot is a permanent fail, not transient (platform follow-up from atlas/tom removal)

teled correctly emits `Forbidden: bot was blocked by the user` as a
403 error, but gateway treats it as transient and retries indefinitely
until `outboundMaxAge`. A 403/blocked is permanent (only the user can
unblock from the Telegram client). Surfaced 2026-05-25 when atlas/tom
crons were deleted (see `.diary/20260525.md`). Patch sketch:

- `teled/bot.go::Send` — classify "bot was blocked by the user" /
  "user is deactivated" as a typed permanent-fail error
- `gateway/gateway.go::dispatchOutbound` — on permanent-fail, set
  status='failed' immediately, skip retry queue
- Optional: mark the chat as `blocked` (new `chats.blocked` column);
  skip future outbound until next inbound from that user
  (auto-unblocks)
- Optional: pause scheduled_tasks targeting a `blocked` chat; resume
  on first inbound

Low priority — atlas/tom was the only affected JID across all three
instances. Will become more valuable if other recipients block bots.

## Aggregated user-reported issues — 2026-05-25 (post-2026-05-03 sweep)

Three parallel audits ran across krons / marinade / sloth groups + per-group
`issues.md` + per-group `.diary/` entries dated after 2026-05-03. Debounced
against the 2026-05-03 aggregation above. 27 new issues, 11 stale revivals,
10 cross-cutting patterns.

### Cross-cutting platform patterns (highest leverage)

#### CC1 — Cross-folder / tier-0 broadcast still blocked

Every migration cycle from v0.34 (2026-05-14) through v0.45.10 (2026-05-25)
logs the same workaround: MCP `send` enforces folder scope even for tier-0
root, so a migrate skill running in mayai can't broadcast version
announcements to krons/rhias/happy. The migrate skill papers over with
per-group fires; the underlying `auth/policy.go` has no tier-0 broadcast
escape hatch.

- **Severity:** medium (recurring; single most-repeated user friction)
- **Scope:** platform — `auth/policy.go` + new tier-0 broadcast tool needed
- **Affected:** krons/mayai, recurring across all releases since v0.34
- **Sources:** `/srv/data/arizuko_krons/groups/mayai/issues.md:5`; diary
  blasts in rhias 2026-05-19, rhias/nemo 2026-05-17
- **Status:** open. Root `bugs.md` 2026-05-03 entry marked `[STALE? — likely
  fixed since v0.29.4]` — audit confirms NOT fixed. Re-open.

#### CC2 — crackbox network-connect race on container restart

Across krons (2026-05-19, ~600 recovery entries across 4 group diaries) and
marinade (2026-05-16, 18, 19 — 100+ retries each), the pattern is the same:
crackbox container disappears (restart, redeploy, OOM), agent containers
hit `connection refused` on `egress register: crackbox register: Post
http://crackbox:3129/v1/register`, retry-loop without backoff and without
self-healing the docker-network attach. The "identity is configured, never
derived" CLAUDE.md note hints the fix landed for one class; the
network-attach side still has hard dependency on a container that can vanish.

- **Severity:** high (took down ingestion across 4 krons groups for ~50min on 2026-05-19)
- **Scope:** platform — crackbox lifecycle + container network-attach race
- **Affected:** krons (rhias + sibling groups), marinade (atlas)
- **Sources:** `/srv/data/arizuko_krons/groups/rhias/issues.md:1-14`;
  `/srv/data/arizuko_krons/groups/{krons,rhias,happy,mayai}/diary/20260519.md`;
  marinade atlas diaries
- **Status:** open. Probably interacts with the proxyd-redeploy side-effect
  memory note — restart sequencing also part of the fix.

#### CC3 — `/web` skill drift on multi-tier deployments

Tier-2 groups (`rhias/nemo`, `atlas/strengths`) hit the recipe wrong: 404s,
wrong paths, missing `/workspace/web/` mounts. Tier formula collision +
`WEB_PREFIX` empty + per-page custom CLAUDE.md not respected. v0.45.10
shipped a defensive recipe but the underlying mount-gap + case-selection
logic isn't bullet-proof.

- **Severity:** medium-high (persistent operator friction; high "fuck the style" factor)
- **Scope:** platform (mount strategy) + skill (`/web` per-tier docs)
- **Affected:** krons/rhias/nemo (mount missing), marinade/atlas/strengths
  (case-selection wrong), krons (custom CLAUDE.md overrides not honored)
- **Sources:** `/srv/data/arizuko_krons/groups/rhias/nemo/issues.md:5-7`;
  root bugs.md tier-2 web entry (2026-05-25)
- **Status:** in-progress. Defensive recipe landed in v0.45.10; mount + case
  selection logic still pending.

#### CC4 — Adapter 502 cluster (Slack + Discord)

Three Slack 502s on marinade (May 15 send / May 20 like / May 25 /pub/) +
two Discord 502s on sloth (May 14, two within 46min on same channel).
Could be coincidence with upstream APIs, but the time-cluster on marinade
suggests proxyd or vited had a marinade-instance-wide event. No
root-cause investigation has happened.

- **Severity:** medium (sporadic but unconfirmed root cause)
- **Scope:** adapter (slakd + discd) or proxyd/vited
- **Affected:** marinade/atlas, sloth/main
- **Sources:** `/srv/data/arizuko_marinade/groups/atlas/issues.md:1-96`;
  `/srv/data/arizuko_sloth/groups/main/issues.md:3-4`
- **Status:** open. Worth a single investigation rather than three independent fixes.

#### CC5 — Subagent context drift

Task-delegated subagents repeatedly ignore parent-context standards:
visual language file (`~/killer-visual-language.md`), parent CLAUDE.md
hierarchy, project-specific styling. Operator quote: "fuck the style so
much", "horrendous". Partial fix in `~/.claude/skills/hub/SKILL.md`
mandating reference-read; structural fix (shared stylesheet or per-subdir
CLAUDE.md with visual language inline) outstanding.

- **Severity:** medium (recurring per-build regression risk)
- **Scope:** platform (subagent context inheritance) + skill (hub/web)
- **Affected:** marinade/atlas (NEAR validators hub, strengths form)
- **Sources:** `/srv/data/arizuko_marinade/groups/atlas/issues.md:98-133`
- **Status:** in-progress

#### CC6 — 900s container timeout on long parallel research

Hit on 4 dates in May (atlas 2026-05-07, 19, 25; krons 2026-05-25;
support 2026-05-19). Same pattern as 2026-04-17 OOM (atlas, 8 crashes in
one day). Hard cap in Claude Code SDK; user-visible as "Container exited
with code 1" reply.

- **Severity:** medium (intentional design limit; user-visible as failure)
- **Scope:** docs (ARCHITECTURE.md / EXTENDING.md) — mitigation hints not
  written yet despite earlier note
- **Affected:** krons + marinade atlas/atlas-support
- **Sources:** root bugs.md `Krons agent 900s timeout`; new krons diary
  + marinade atlas + atlas-support diaries
- **Status:** logged as design limit; ARCHITECTURE.md note still missing.

#### CC7 — Agent over-cautious asking + chat-routing discipline

Two manifestations of the same agent-discipline class. **Over-asking**:
"should I commit and create skill?" when the user already directed it
(krons 2026-05-17 — same as 2026-04-23 atlas). **Routing override**: agent
inserts `chatJid=` in send/reply when the platform already handles
routing, lands reply in wrong channel (sloth 2026-05-16); separately,
agent ignores `thread=` attr on inbound and doesn't switch chat_jid
(sloth 2026-05-23).

- **Severity:** medium (cumulative friction)
- **Scope:** skill / CLAUDE.md guidance — `/resolve`, `/issues` template
- **Affected:** krons/krons, sloth/main
- **Sources:** `/srv/data/arizuko_krons/groups/krons/issues.md:5`;
  `/srv/data/arizuko_sloth/groups/main/issues.md:7,10`
- **Status:** open. CLAUDE.md tightening shipped previously but not enough.

#### CC8 — Egress isolation hits user features

Two sloth cases: `agent-browser` blocked on bot-detection sites (in root
bugs.md 2026-05-03 already); crypto data APIs (CoinMarketCap, CoinGecko,
CoinPaprika) all blocked from agent container (sloth main/main
2026-05-25). Same root cause: crackbox egress allowlist doesn't include
"research APIs" or "stealth browser endpoints".

- **Severity:** high (blocks core trading + research workflows on sloth)
- **Scope:** platform (crackbox egress) — allowlist or trusted-egress channel
- **Affected:** sloth/main, sloth/main/main
- **Sources:** root bugs.md 2026-05-03 agent-browser line;
  `/srv/data/arizuko_sloth/groups/main/main/issues.md:1`
- **Status:** open

#### CC9 — Research-correctness on volatile data

Sloth's research-style workflows surfaced two distinct correctness gaps:
**oracle verbosity** in chat context (full long-form analysis when a
brief summary was wanted — May 16) and **date-confusion in BTC pricing**
(cited May-15 article as May-17 context, actual price ~3% off — May 17,
caught by user). Both came from research/oracle calls; both warrant a
shared "research output discipline" (brief-summary + artifact via
send_file; source-date verification before quoting).

- **Severity:** medium (oracle UX) + high (correctness on financial claims)
- **Scope:** skill (oracle + research) — output discipline
- **Affected:** sloth/main
- **Sources:** `/srv/data/arizuko_sloth/groups/main/issues.md:6,8`
- **Status:** open

#### CC10 — Per-group `issues.md` not being groomed

Already-resolved entries linger across instances. Marinade atlas
2026-05-21/22 strengths-form routing items marked **fixed** in the file
but never deleted; atlas/support 2026-05-13 recall-memories also marked
Fixed but still present. The 2026-04-16 atlas issue ("verify the
consolidation pipeline runs and is documented") is essentially the meta-
bug describing this very process — and it's still manual.

- **Severity:** low (operational)
- **Scope:** ops / skill (groom per-group issues.md after consolidation)
- **Affected:** all instances
- **Status:** open

### Per-instance unique items not covered above

#### krons

- **2026-05-21** Oracle skill leaks cost line ("cost: $0.11") into user chat.
  Skill / `/oracle` output post-processing. Low severity. (`krons/krons/issues.md:7`)
- **2026-05-24** Feature request: `/feed` (changelog feed) skill — diary →
  feed.json → /pub/index.html re-render. Low. (`krons/krons/issues.md:9`)
- **2026-05-24** Feature request: install best-in-class frontend / wiki
  design-system skills. Low. (`krons/krons/issues.md:8`)
- **2026-05-14** Migration broadcast can't cross folder boundaries —
  already covered under CC1.

#### marinade

- **2026-05-24** Agent sends bare URLs as plain text; user wants explicit
  `[text](url)` markdown link formatting. Low.
  (`marinade/atlas/issues.md:56-73`)
- **2026-05-06** PSR-dashboard GUIDE.md bond formula references external
  coefficient names without values. Operator-owned external project doc,
  not arizuko platform. Logged here because it sits in the per-group
  queue. (`marinade/atlas/support/issues.md:6-32`)

#### sloth

- **2026-05-23** News-backtest skill manual workflow shipped; real-time
  path open (bot not in paid/private Telegram channels Zoomer, aggrnews,
  Solid Intel, Layergg, PhoenixNews). Low (feature).
  (`sloth/main/issues.md:9`)
- **main/trading 0 compaction** — already covered by root bugs.md
  "Compaction — sloth has zero compact-log entries post-v0.45.9".

### Stale revivals worth re-checking

- **Selective Telegram message loss** (atlas 2026-04-25, krons/mayai
  2026-04-25 same date) — never confirmed fix; no recurrence in May logs
  either. Spot-check still warranted.
- **/resolve skipping on continuations** (atlas-support 2026-04-25/26) —
  CLAUDE.md tightened, no audit confirms behavior change.
- **2026-04-25 sloth/main items** (`routing-task-to-wrong-session`,
  `social-actions-pin-messages`, `topic-reset-on-inactivity`,
  `topic-visibility-and-replies`) — partially addressed by topic MCP
  tools (`fork_topic` etc. shipped v0.40.0); verify operator-facing
  resolution.
- **OOM during large tasks** (atlas 2026-04-17, 8 crashes one day) — no
  recurrence in May, but 900s timeout (CC6) is the same long-task class
  resurfacing. Worth unifying.
- **Migration-skill diary noise** — krons mayai 2026-05-17 has duplicate
  "13:51" diary entries from auto-migrate. Migrate skill may be
  double-writing.
- **Sloth/main pre-2026-05-03 items in root bugs.md** (`agent-browser
  undetected mode`, `web auto-add back-nav`) — no fix shipped; the
  2026-05-25 crypto-API-blocked entry is plausibly the same class.

### Totals

- Instances audited: 3
- Per-group `issues.md` files scanned: 10 (krons 4, marinade 3, sloth 3)
- Per-group `.diary/` scanned: ~45 files across the three instances
- **New unresolved issues since 2026-05-03: 27**
- **Cross-cutting platform patterns: 10**
- **Stale items worth re-checking: 11**
- Group-level `bugs.md` files: 0 (all aggregation rolls up to root)

### What to fix first (suggested priority)

1. **CC2 crackbox lifecycle** (high; recurring; took down 4 groups for ~50min on 2026-05-19)
2. **CC8 egress isolation** (high; blocks core sloth trading workflow today)
3. **Sloth date-confusion on financial claims** (high; correctness)
4. **CC1 cross-folder broadcast** (medium; recurring; needs new tier-0 tool)
5. **CC3 /web tier drift** (medium-high; v0.45.10 softened, structural fix outstanding)
6. **CC4 adapter 502 cluster** (medium; needs root-cause investigation, not per-incident fix)
7. **CC5 subagent context drift** (medium; partial fix in hub skill, structural pending)

Everything else is medium-low and can ride the next refine cycle.

## LOCAL.md canonical-skills path drift (2026-05-25)

Surfaced during the oracle cost-leak fix (`.diary/20260525.md`):
LOCAL.md still points at
`~/app/tools/assistants/claude-template/global/skills/`, which
doesn't exist on this host — the actual canonical is
`~/app/tools/skills/oracle/`. LOCAL.md needs a refresh.

## proxyd: in-memory route cache violates no-cache discipline (2026-05-27)

**Scope**: `proxyd/resource.go` (`routesResource`, `RWMutex`),
`proxyd/main.go` (`loadInitialRoutes`, per-route `httputil.ReverseProxy`
instances).

**Problem**: proxyd caches `proxyd_routes` rows in memory and only
updates the cache via its own resreg handler. A direct DB write (e.g.
from `arizuko apply`) bypasses the handler and leaves the cache stale.
Architectural rule: all daemons share one SQLite file on one host;
no in-memory config cache is needed or correct. DB reads are
microseconds — cheaper than cache invalidation complexity.

**Fix path**: remove `routesResource` mutex + snapshot pattern. Read
`proxyd_routes` from DB on each request (one indexed query by path).
Keep `httputil.ReverseProxy` instances in a `sync.Map` keyed by
backend URL only (not by route config) — backend URLs are stable;
route rows are not. That preserves connection-pool reuse without
coupling proxy lifetime to route config.

## Agent asks "which group are you?" instead of reading configured identity (2026-05-27)

**Scope**: `ant/` agent prompt / skill system — group identity at runtime.

**Observed**: agent replied "Can't access /workspace/web… which group
are you?" — asking the user for its own group identity rather than
reading it from configuration.

**Problem**: violates CLAUDE.md rule "identity is configured, never
derived." The agent's group folder, instance name, and workspace path
are injected at container start via env vars / PERSONA.md. The agent
must read those, never ask the user and never reverse-engineer from
filesystem paths.

**Fix path**: audit `ant/` system prompt and skill CLAUDE.md to
confirm that `ARIZUKO_GROUP`, `ARIZUKO_INSTANCE`, and the group
folder are explicitly available to the agent at turn start. If any
of those vars are missing from the container env or not surfaced in
the prompt, add them to the compose template and system prompt. The
agent should surface its identity in the first line of any confused
response ("I am atlas on krons, I cannot access X") — never ask.
