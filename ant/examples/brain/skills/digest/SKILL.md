---
name: digest
description: >
  Daily or weekly (`--week`) digest of a team's Discord threads and GitHub
  work: two research halves in parallel (digest-work, digest-discourse), a
  combine step, a blind review, then ONE message in #main, edited in place.
  USE on the digest cron, "/digest", "/digest --week", "daily digest",
  "weekly digest", "what happened", "what should I work on". NOT for a
  single lookup (use recall-messages), research (use find), or diary writes
  (use diary).
user-invocable: true
arg: "[--week] [--end RFC3339] [--state FILE] [--out DIR]"
managed: local
---

# digest — threads, members, GitHub, critique, post

Scripts: `scripts/` next to this file; `SKILL_DIR=$(dirname <this file>)`.
Run dir: `$DIGEST_ROOT/<RUN_ID>/` (default `$HOME/tmp/digest`). ALWAYS resume
from an existing run dir; its files are the state.

## Who + where

Everything the scripts need is in the group config `~/digest.toml`
(`$DIGEST_CONFIG`), read by `scripts/config.py`: `repo`, `clone`, `post_jid`
(the ONE chat the digest posts to), `bot_user`, `agent_name`, `agent_github`,
`agent_folder`, `account_regex`, `[humans]` (sender id → name),
`[[channels]]` (`jid`, `name`, `signal`). The scripts exit when it is missing.
Daily runs use state `~/.digest-state.json`; the Monday `--week` run uses
`~/.weekly-digest-state.json` (`window.py --week --state <file>`), one state
file per cadence, never a second one. `window.py` picks the state file.

## Protocol

### 0. Window + state — `scripts/window.py [--week] [--end E] [--state F] [--out DIR]`
Writes `<run>/window.txt`: `MODE RUN_ID START END LABEL STATE OUT MISSED PREV
HISTORY`. `START = max(END − 24h | 7d, state.last_end)`; `MISSED=1` = a dead
run is covered too, say so in the report caption. `PREV` = last posted run
dir. `HISTORY` = dir of every posted digest, dated. ALWAYS `source
<run>/window.txt` first; NEVER estimate dates.

### 1. Research — two halves, in parallel, separate; all data first
Start BOTH in ONE message as background subagents (`sonnet`):
- work: "Follow `<SKILL_DIR>/../digest-work/SKILL.md` with run dir `<run>`."
- discourse: "Follow `<SKILL_DIR>/../digest-discourse/SKILL.md` with run dir `<run>`."
They do not see each other; both read `$PREV` and `$HISTORY` and date every
item; neither ranks. Wait for both `DONE` files. A half that dies: resume it
with the same prompt. NEVER combine with one half missing — post nothing,
say which half failed.

### 2. Combine → `<run>/combined.md`
Read `work.md`, `discourse.md`, `github.md`, `threads/*.md`, `$HISTORY/*.md`.
Index `~/diary/`, `~/episodes/` only to see which agent tasks ran.
2a. Cross-link conversations ↔ PRs/components. Unlinked stays unlinked.
2b. Per person = work (built) + discourse (argued, decided). Say when one side
is empty.
2c. Delta vs last period. Baseline: the newest `$HISTORY/*.md` whose window
ends at or before START, plus `$PREV/combined.md`; none → measure inside the
window, write `no baseline`. `done` = merged this period (whenever opened) or
`vs before: finished`; `new` = opened and still open, or `started`; `shift` =
`changed`, each with its `why` and the PRs that carry it; theme = the biggest
change, 3 words.
2d. Narrative. For each item in the last digest: closed, changed, still open
with news, or dropped. Carry forward each one that moved and say what it
became ("last week's roadmap todos became #360 and #382"). If the last digest
called an item settled and it is open, say so.
2e. Rank with all data on the table. Importance = consequence for the team
next week: 1 a decision that changes direction, product shape or a standing
constraint; 2 capital at risk or lost, weighted by amount and repeat (a
silent monitoring failure outranks one small loss); 3 a blocker or open
question that stops work or money; 4 one keystone PR; 5 started work. NOT
importance: recency, counts, release numbers, loudness, the `open` flag
alone, agent-tooling threads. Tie-break: consequence × irreversibility. One
priority per person: the first line of their block. No single week
highlight; the title's theme carries the biggest change. One item, one place: team-level
when it moves the team's money or direction, else in its owner's block.
2f. Write `combined.md`: one section per person (highlight first), then
done / new / shift / open question, then the narrative links. Every line
keeps its cite (PR number, thread id + quote time) and its date.
2g. Metrics: `scripts/metrics_accounts.py <run>` → `metrics_accounts.json`;
definitions and the last row in `metrics.md`. Numbers line =
`~N accounts · N PRs · N msgs`, nothing more.

### 3. Blind adversarial pass → `<run>/critique.md`
Write `<run>/draft.md` in the step 4 format. Start ONE reviewer (`opus`, or
`codex` via `/oracle`) with ONLY: `draft.md`, `threads/*.md`, `work/work.md`,
`discourse/discourse.md`, `combined.md`, `github.md`, `members.json`,
`$HISTORY/*.md`, and `scripts/review_prompt.md`. It never sees the chat or
this session. Fold in every defect: unsupported fact → drop it or name its
owner; omitted dissent → the `?` line names who vs who; unsupported
"finished" → out of ✓. Keep `critique.md`; no `critique.md`, no post.

### 4. Compose — ONE message, Discord, mobile
Reader = a team member who was away all week: from this message alone they
understand what happened for everyone at a high level; detail stays in the
threads. Every line is a full claim — actor, result, the why when a human gave one —
in Simplified Technical English: active voice, simple tense, one idea,
6–13 words (target 10–12; tags and symbols not counted). A topic tag
("detection risk") is not a line. Shortness is a weight, not a cap: cut
whole lines, never a claim to a tag.
```
**27 Oct–3 Nov: collectors before launch**                 window + 3-word theme
~12 accounts · 56 PRs · 166 msgs                          numbers, joined by ·
✓ [#403 news-collector](<https://github.com/acme/app/pull/403>): now serves its own feed and reads
+ last week's roadmap todos became [#360 billing-api](<…/pull/360>) and [#382 billing-api](<…/pull/382>)
~ priorities: ana finishes news and terminal first, then launch   changed + why
? automate the full user journey now (ana) or later (ben)   open question
<@111111111111111111> set the rule: no LLMs ever on production servers   the person's priority
· merged 52 of 56 PRs, 28 of them in news-collector        the rest, by importance
<@222222222222222222> reframed the product: shared-ban risk limits it, not cost
<@333333333333333333> two breaches; account-6 idled 5 weeks while the dashboard showed it online
```
- Team TLDR = the lines before the first tag: title (window + 3-word theme =
  the biggest change), numbers, then ✓ finished and + started (0–2 each,
  optional), then ~ and ? — set in stone, 1–2 lines each, always present:
  `~` = what changed vs last period and the reason a human gave;
  `?` = the open question that blocks work or money, who vs who. Team lines
  carry only items that move the team's money or direction.
- One block per name in `members.json`: a real Discord tag `<@id>` (from
  `sender_id`) + that person's highlight, then 0–3 lines starting `· `, by
  importance; ≤ 4 lines, each relevant. Quiet person: `<@id> quiet this
  period: 0 rows, 0 PRs`. Tag nobody else; a GitHub-only login gets no block.
- Narrative: an item that continues one from the last digest says so.
- Every PR number is a link with its scope: `[#403 news-collector](<url>)`.
  Scope = the title's conventional-commit scope, else the top-level directory
  of the change (`pr_scope.json` from `gather_github.sh`). `<>` stops the embed.
- Cut with the 2e order, whole lines from the bottom. ≤ 1900 chars. No `#`
  headers, no labels, no agent as a speaker.
Write `<run>/blocks.json` = `[{"slot": "digest", "text": "<the lines>"}]`.
Full report `~/reports/digest-<RUN_ID>.md` = message, `combined.md`,
`work.md`, `discourse.md`, critique defect count.
Gate: `scripts/check_format.py <run>` must print `OK`.

### 5. Deliver + checkpoint
Preview (caller says "do not post to #main yet"): `scripts/post_blocks.py
<run> --preview <chat-jid>` sends once to that chat, writes no state, refuses
#main; stop.
Post: `post_blocks.py <run> --dry-run`, read the plan (edit a stored id,
reuse an orphaned id before any send, delete only leftovers), then
`--apply`: edits/sends/deletes via MCP, writes state (`slots`, `last_end`,
`posted_run`, `threads`) and `$HISTORY/<RUN_ID>.md`. The digest is the
ONLY message in #main: NEVER send the report file, a caption or any detail
there (owner rule, 2026-11-04); the report stays in `~/reports/`.
Append this run's row to `metrics.md`. `/diary` one entry: window,
edited/sent counts, critique defects.
Redo (same window): `window.py` with the same `--end` and a new `--out` —
NEVER delete the old run dir — steps 1–4, then `--apply --redo`; the stored
ids stay, the edit lands on the same message.

## Weekly mode (`--week`)
Same pipeline; START = END − 7d, RUN_ID `YYYY-WNN`, state
`~/.weekly-digest-state.json`.

## Rules
- ALWAYS source `window.txt`; run steps 0–5 in order; resume from the run dir.
- ALWAYS write each ✓ + ~ line as a change vs last period, with its date.
- NEVER post a topic tag as a line; a line states a result or it is cut.
- NEVER write "finished/agreed/closed" without the last human message agreeing.
- NEVER report the agent as a speaker or source; `endorsed_by` text is that
  human's position.
- ALWAYS verify every PR number and URL against the gather files; NEVER
  copy claims from `episodes/` or `diary/`; NEVER fabricate a number, quote
  or decision.
- NEVER emit `<status>` before posting (it anchors a Discord thread).
- NEVER delete-and-repost a slot that can be edited; NEVER edit an id that is
  not in the state file; NEVER post to any chat but `post_jid`; NEVER `--apply` twice for
  one window without `--redo`.

## Anti-patterns (each one happened)
- The agent's own reversal under push reported as "landed"; one member's line
  as the team's decision; a chat claim repeated while the code says the
  opposite. Cite code or write `name: …`.
- A 24h window that saw only the tail of a 33-row thread — why every active
  thread is read from its root.
- Words cut inside lines until each line was a topic tag ("detection risk").

## Salvage
Rerun from `<run>/`; files are the state. `blocks.json` alone → `check_format.py`,
then `post_blocks.py`. No `critique.md` → do step 3. A missing script: look
for `scripts/<name>.quarantined` and report it; NEVER rewrite a script by hand.
