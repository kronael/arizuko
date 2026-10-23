# arizuko Dashboard — Screen Specifications

Source of truth for dashd UI. Implementations read this; changes here
drive implementation, not the reverse. Confirm against `dashd/main.go`
`registerRoutes` for all route registrations.

---

## Design principles

1. **Status before configuration.** Every page answers "is it working?"
   before offering controls. Incident visibility is never gated behind a
   drill-down that doesn't exist yet.

2. **Glanceability at the top.** The first 120px of any page must answer
   the primary question without scrolling. KPI strip → summary banner →
   detail tables. Never bury the error count below a form.

3. **Color reserved for status only.** `--ok` (green), `--warn` (amber),
   `--danger` (red) appear on dots and banners. Never used for decoration.
   `unknown` / `dim` for genuinely absent data.

4. **One call to action per empty state.** Empty tables show one sentence +
   one next step. Never a wall of instructions; never a silent blank.

5. **Identity always on screen.** The persistent nav shows the current
   user name and operator badge (`◆`). No page should be operable without
   knowing who you are.

6. **Actions must exist where the status is shown.** A table row flagging
   an error must carry the action that resolves it (retry, kill, revoke) —
   not a pointer to a different page. Read-only status pages are incomplete.

7. **Relative time by default, ISO on hover.** Every timestamp renders
   as `5m`, `2h`, `3d`; the ISO full form is in the `<abbr title>`.
   Operators parse human time faster than nanosecond UTC strings.

8. **Progressive disclosure via `<details>`.** Rarely-needed config
   (advanced options, danger zone) lives inside a collapsed `<details>` on
   the same page. Secondary pages only when the content is genuinely
   distinct (grants, tokens).

---

## Information architecture

### Nav order (all roles)

```
arizuko · status · tasks · activity · groups · routes · memory · profile
```

Additional links for operator (`**` grant):

```
services · packages · invites · approvals · audit · usage · proactive · engagement
```

Identity badge always right-aligned in nav: `{name} ◆` (operator) or
`{name}`.

### Page hierarchy

```
/dash/                          Portal home (tile grid + health banner)
├── /dash/services/             Daemon health grid (operator only)
├── /dash/packages/             Installed packages, read-only (operator only)
├── /dash/audit/                Federated audit log, every daemon (operator only)
├── /dash/approvals/            Held tool-call review queue (operator only)
├── /dash/usage/                Instance-wide usage/cost cockpit (operator only)
├── /dash/proactive/            Which groups may speak unprompted (operator only)
├── /dash/engagement/           Active early-disengage windows + control (operator only)
├── /dash/routd/                Message-router cockpit: errored chats + retry (operator only)
│   └── retry                   Per-chat retry
├── /dash/runed/                Container-runner cockpit: live/recent runs + kill (operator only)
├── /dash/authd/                Identity cockpit: signing key, sessions, sign-out (operator only)
├── /dash/status/               Instance health summary
├── /dash/activity/             Live message feed (auto-refresh)
├── /dash/tasks/                Scheduled task list + create form
│   └── /dash/tasks/{id}        Task detail + run log
├── /dash/groups/               Group hierarchy
│   ├── /dash/groups/new        Create group form
│   └── /dash/groups/{folder}/  Group detail
│       ├── settings            Model, open flag, observe window, skills
│       ├── grants              ACL list + add/revoke
│       └── tools               MCP tool browser
├── /dash/routes/                Full route table (CRUD)
├── /dash/tokens/{folder}/      Web route tokens (list + revoke; no mint)
├── /dash/invites/              Onboarding invites (operator only)
├── /dash/proxyd/               proxyd route table (operator only)
├── /dash/channels/whatsapp/pair  WhatsApp re-pair (QR/code) (operator only)
├── /dash/memory/                Agent memory file browser
├── /dash/profile/               Account ID + add-a-provider + secrets
├── /dash/me/secrets             Per-user capability-secret CRUD (GITHUB_TOKEN, etc.)
├── /dash/me/env                 Per-user env-profile key CRUD (ANTHROPIC_API_KEY, etc.)
└── /dash/me/connections         Per-user surrogate OAuth connections (connect/disconnect)
```

External pages reached from the dashboard (not served by dashd):

- `/dash/onbod/` — onboarding queue (onbod daemon)
- `/dash/timed/` — scheduled message queue (timed daemon)
- `/me/chats` — the conversation list and `+ new chat` (webd daemon). The
  cockpit's `chat` tile and every folder link point here. dashd had its own
  chat portal until it was deleted (BUGS `V2`): it duplicated this page and
  persisted the RAW reusable route bearer to render "continue" links.
- `/chat/{token}/` — web chat widget (webd daemon)
- `/dav/{folder}/` — WebDAV workspace (davd daemon)

### User roles

| Role         | Who                          | What they see                             |
| ------------ | ---------------------------- | ----------------------------------------- |
| operator     | `**` in ACL grants           | all groups, services, invites, all data   |
| group-scoped | listed folders in ACL grants | only their granted folders and subfolders |

Operator-only nav items: `services`, `invites`. Hidden from non-operators;
routes also 403 on direct access.

---

## Pages

---

### /dash/ — Portal

#### Purpose

Landing page. Answers "is the instance healthy?" in ≤3 seconds.
Navigation hub to every section.

#### User stories

- As an operator, I want to see error count and instance name at a glance
  so I can judge whether an incident is in progress before clicking anything.
- As an operator, I want one-click access to every section from the landing
  so I don't hunt through nav for unfamiliar pages.
- As a group member, I want to see only sections I have access to so I'm
  not confused by links that 403.

#### Layout

```
[nav: arizuko · status · tasks · activity · groups · routes · memory · profile · name ◆]  [theme toggle]

h1: arizuko         p.dim: {instance_name} operator dashboard

[banner-err: "2 errored chats · 1 failed task" → /dash/status/]   (conditional; hidden when all-green)

[tiles grid — 3 columns auto-fit, minmax 200px]
  status     tasks      activity
  groups     routes     chat  (→ /me/chats, webd)
  memory     profile    services (operator only)
             invites    (operator only)
```

Each tile: `h2: {name} [dot]` + `p.dim: {one-line description}`. Every tile
but `chat` links inside dashd; `chat` leaves for webd's `/me/chats`.

#### Data shown

- `erroredCount` — distinct errored chats (visible to caller)
- `failedTasks` — task run errors in last 24h (visible to caller)
- Status dot on `status` tile: ok/warn (erroredCount > 0)
- Tasks dot on `tasks` tile: ok/warn (1-2 failures) / err (≥3 failures)
- Instance name from `ARIZUKO_INSTANCE` env in subtitle

#### Actions

- Click any tile → navigate to section
- Click banner → /dash/status/ filtered to errored chats

#### Empty states / edge cases

- No groups yet: banner-warn "No groups configured. Run `arizuko invite`
  to onboard a user."
- All-green: no banner rendered; tiles show plain nav links
- Non-operator: services and invites tiles hidden; remaining tiles shown

#### What's missing (spec gap)

- Instance name not currently in portal subtitle (hardcoded "Operator dashboard")
- Banner count is text in a `banner-warn`; not yet a link to filtered errored list

---

### /dash/services/ — Daemon Health Grid

**Operator only.** 403 for non-operators.

#### Purpose

Single-glance cockpit for daemon health. Answers "which daemons are up?"
and links into per-daemon control planes when built.

#### User stories

- As an operator, I want to see every daemon's health at once so I can
  identify which one is causing an incident.
- As an operator, I want to click a daemon tile to reach its control plane
  so I can act (kill a run, drain a queue, reset a breaker).

#### Layout

```
h1: Services
p.dim: Daemon health (N ok, M err, K unknown). Auto-refresh: manual (reload).

[summary counts: "N ok, M err, K unknown"]

[services-grid — 3 columns auto-fill, minmax 220px]
  [service-tile] [service-tile] [service-tile]
  ...

Each tile:
  h3: {status-glyph} {name-as-link-or-text}
  p.dim: {desc}
  [status dot colored by status]
```

Status glyphs: `✓` ok, `✗` err, `?` unknown.

#### Data shown

Per daemon (probed concurrently, 500ms timeout):

- Name, description
- Status: `ok` (HTTP 2xx), `err` (refused or non-2xx in deployed env),
  `unknown` (DNS failure = container not deployed or local dev)
- Link to `/dash/{daemon}/` only when `Built=true` AND status is not `unknown`

Current daemon list: `routd`, `runed`, `authd`, `proxyd`, `onbod`, `timed`,
`webd`, `davd`. New daemons are added by editing `services.go`.

#### Actions

- Click tile link → per-daemon control plane (when built)
- Reload page → re-probe all daemons

#### Edge cases / empty states

- All unknown: show "Local dev: daemon DNS names don't resolve outside
  Docker network." This distinguishes dev from a production outage where
  `err` would appear.
- Built=false tile: name is plain text (no link), tooltip or label
  "control plane: coming"
- Daemon down in production: `err` status + `✗` glyph (NOT `unknown`).
  `unknown` is reserved for DNS failure (undeployed or local-dev name
  mismatch).

#### Built tiles vs. the rest

`/dash/routd/`, `/dash/runed/`, `/dash/authd/` and `/dash/proxyd/` are built;
onbod, timed, webd and davd are not. A tile with `Built=false` must NOT render
a dead link — it shows the name as plain text plus "(coming soon)".

The flag and the route are maintained in different files, so
`TestServicesBuiltFlagMatchesMountedRoutes` ties them together in both
directions: `Built` without a mounted route is a link that 404s, and a mounted
route without `Built` is a shipped page nobody can reach — the cockpits are
deliberately absent from `navLinks`, so this hub is their only nav path.

---

### /dash/status/ — Instance Status

#### Purpose

Health summary for the instance. Shows group count, errored chat count,
and (operator-only) active session breakdown per group.

#### User stories

- As an operator, I want to see how many groups are active and how many
  chats have errors so I can prioritize incident response.
- As a group member, I want to know if my group has any active errors so
  I can report them.

#### Layout

```
h1: Status
p.dim: Service health

[banner-ok/warn: "N groups · M errored chats (link → /dash/activity/)"]

[operator only]
h2: Active sessions  {total} total
[table: group | sessions]
  alice    3
  bob/work 1
  ...
```

#### Data shown

- Group count (visible to caller)
- Errored chat count with link to activity page
- Sessions breakdown: per-group session counts from `sessions` table
  (operator only)

#### Actions

- Click errored chat count → `/dash/activity/` (linked to errored rows only
  when filter is added; currently just navigates to activity)

#### Edge cases / empty states

- Zero groups: banner-warn with link to groups page
- Zero errored chats: banner-ok "N groups · 0 errored chats"
- No sessions (operator): omit sessions table entirely

#### What's missing (spec gap)

- No per-group error action (kill, retry, reprompt) — status is read-only
- No instance-wide usage row (total $/7d, message volume from GroupUsageBulk)
- No staleness label ("as of {time}") — page is a point-in-time snapshot

---

### /dash/tasks/ — Scheduled Tasks

#### Purpose

List and create scheduled tasks (cron-triggered agent messages).

#### User stories

- As an operator, I want to see all scheduled tasks and their next run times
  so I can identify failed or stuck jobs.
- As a group member, I want to see tasks I own and create new ones so I can
  automate recurring agent interactions.

#### Layout

```
h1: Tasks
p.dim: Scheduled jobs. Auto-refreshes every 10s.

[table: ID | Group | Prompt | Cron | Status | Created | Next Run]
  htmx-refresh: every 10s → /dash/tasks/x/list (tbody swap)

[section: Create task]
  form:
    Group (owner):  [input text, placeholder: alice]
    Chat JID:       [input text, placeholder: alice@s.whatsapp.net]
    Prompt:         [textarea rows=3]
    Cron:           [input text, placeholder: 0 9 * * *]
    [button: create]
  p.dim: Cron in UTC. Owner = group folder. Chat JID = the chat this task posts to.
```

#### Data shown

Per task row:

- ID (link to `/dash/tasks/{id}`)
- Owner (group folder)
- Prompt (truncated to 64 chars, full in `title` attr)
- Cron expression
- Status dot + label (`active` = green, other = amber)
- Created timestamp (relative + ISO in abbr title)
- Next run timestamp (relative + ISO in abbr title)

#### Actions

- Click task ID → task detail page
- Create task form → POST /dash/tasks/ → redirect back
- (on task detail) pause / resume / delete / run-now

#### Edge cases / empty states

- No tasks: "No scheduled tasks. Ask the agent to schedule one (e.g.
  'remind me every morning at 8am')."
- Create form validation: group, chat_jid, prompt, cron all required
- Scoped users: table shows only tasks whose `owner` is in their visible folders

---

### /dash/tasks/{id} — Task Detail

#### Purpose

Full task info plus run log and action controls.

#### User stories

- As an operator, I want to see a task's full prompt, schedule, and recent
  run history so I can diagnose why it failed.
- As an operator, I want to pause, resume, or trigger a task manually so
  I can control it without editing the DB.

#### Layout

```
crumbs: Tasks › {id}
h1: Task {id}

[table: field | value]
  ID        {id}
  Owner     {folder}
  Prompt    {full prompt}
  Cron      {cron}
  Status    [dot] {status}
  Created   {abbr ts}
  Next Run  {abbr ts}

[section: Actions]
  [pause] [resume] [run now] [delete]

[section: Run log]
  [table: Run At | Status | Output (truncated)]
```

#### Data shown

- Task metadata (all fields)
- Run log from `task_run_logs` (last 50, newest first)
- Per-run: run_at (relative), status dot, output (first 120 chars)

#### Actions

- Pause: POST /dash/tasks/{id}/pause
- Resume: POST /dash/tasks/{id}/resume
- Run now: POST /dash/tasks/{id}/run
- Delete: POST /dash/tasks/{id}/delete (confirm dialog)

#### Edge cases / empty states

- Run log empty: "No runs yet."
- Task not found: 404 + link back to /dash/tasks/

---

### /dash/activity/ — Activity Feed

#### Purpose

Live feed of the last 50 messages across all visible channels.

#### User stories

- As an operator, I want to see the message flow across all groups so I
  can spot unexpected traffic or errors.
- As a group member, I want to see my group's recent messages so I can
  verify the agent is responding.

#### Layout

```
h1: Activity
p.dim: Last 50 messages across all channels. Auto-refreshes every 10s.

[table: Time | Source | Chat | Sender | Verb | Content]
  htmx-refresh: every 10s → /dash/activity/x/recent (tbody swap)
```

#### Data shown

Per row:

- Time: `<abbr title="{ISO ts}">{relative}</abbr>`
- Source: platform (telegram, web, slack, etc.)
- Chat: chat_jid as `<code>` linked to `/dash/groups/{folder}` when folder
  is resolvable
- Sender: `<code>{sender}</code>`
- Verb: message verb from messages table
- Content: first 80 chars

#### Actions

- Click chat link → group detail page
- Reload / auto-refresh every 10s

#### Edge cases / empty states

- No messages: "No messages yet."
- Scoped user: over-fetches 1000 rows, filters to visible folders, shows
  at most 50
- Content truncated at 80 chars (no expand affordance currently)

#### What's missing (spec gap)

- No pagination or "older" link — hard-capped at 50 visible rows
- No filter by group, channel, or verb
- No errored-row highlighting (errored=1 in messages table)

---

### /dash/groups/ — Group Hierarchy

#### Purpose

Browse all accessible groups with usage stats. Entry point for
per-group management.

#### User stories

- As an operator, I want to see all groups with their usage so I can
  identify inactive or high-cost groups.
- As an operator, I want to create a new group from this page so I have
  one place for group management.
- As a group member, I want to see my groups and navigate to their settings.

#### Layout

```
h1: Groups
p.dim: Group hierarchy. Expand a row to see routing rules and links.

[+ New group → /dash/groups/new]

[accordion list of groups, ordered by folder]

[group row] <details>
  <summary>
    <code>{folder}</code> [root label if parent==""]
    <span.dim>({N msgs} · {K}k tok / 7d · ${X} / 7d · last {date})</span>
  </summary>
  <div.group-detail>
    [table: folder | parent | links]
    links: settings · grants · tokens
    [routes sub-table: Seq | Match | Target]
  </div>
</details>
```

#### Data shown

Per group:

- Folder path (code)
- Parent folder (from groupfolder.ParentOf)
- Usage: msg count, tokens/7d, cents/7d, last_active (from GroupUsageBulk)
- Routes targeting this group (seq, match, target)

#### Actions

- Expand accordion → show routes and sub-links
- Click settings → `/dash/groups/{folder}/settings`
- Click grants → `/dash/groups/{folder}/grants`
- Click tokens → `/dash/tokens/{folder}/`
- Click `+ New group` → `/dash/groups/new`

#### Edge cases / empty states

- No groups: "No groups configured. Run `arizuko invite` to onboard a user."
- Group with no routes: "no routes targeting this group"
- Usage query error: log + skip usage fields (show group row without stats)

---

### /dash/groups/new — Create Group

#### Purpose

Form to create a new group folder and seed it with default skills/tasks.

#### User stories

- As an operator, I want to create a group by folder name and product type
  so I can onboard a new team or user without CLI access.

#### Layout

```
crumbs: Groups › New
h1: New group

p.dim: Create a group folder. Use parent/child to nest.

<form method=post action=/dash/groups/new>
  Folder:  [input text, placeholder: solo/inbox, required]
  Product: [select: assistant (default) | oracle]
  [button: create]
</form>

p.dim: The folder skeleton (skills, settings, default tasks) is seeded via
container.SetupGroup; admin is granted to the creator.
```

#### Actions

- Submit form → POST /dash/groups/new → redirect to /dash/groups/
- Validation: folder empty → 400; folder invalid → 400; folder exists → 409

#### Edge cases / empty states

- Conflict (folder exists): HTTP 409 + error message
- Caller lacks admin on folder or parent: 403

---

### /dash/groups/{folder}/settings — Group Settings

**Requires visible (read) or admin (write) on folder.**

#### Purpose

View and edit group configuration: model, open flag, observe window,
skills, danger zone.

#### User stories

- As an operator, I want to change a group's Claude model so I can
  control cost per group.
- As a group admin, I want to enable/disable individual skills so the
  agent's tool set matches the group's use case.
- As an operator, I want to delete a group and clean up its DB rows so
  decommissioned groups don't accumulate.

#### Layout

```
crumbs: Groups › {folder} › Settings
h1: Settings — {folder}

p.dim: Group {folder} · product {product}

<form method=post action=/dash/groups/{folder}/settings>
  Model:                  [select: instance default | Claude Opus 4.7 | Claude Sonnet 4.6 | Claude Haiku 4.5]
  open:                   [checkbox] open — sibling groups can see messages sent here
  observe_window_messages: [number input] max messages a sibling sees (0 = default 50)
  observe_window_chars:   [number input] max chars per observation (0 = default 2000)
  [button: save]
</form>

h2: Agent files
p.dim: Edit in workspace browser — dufs opens text files in its built-in editor.
  • CLAUDE.md — /dav/{folder}/CLAUDE.md (new tab)
  • PERSONA.md — /dav/{folder}/PERSONA.md (new tab)
  • MEMORY.md  — /dav/{folder}/MEMORY.md (new tab)
  • workspace/ — /dav/{folder}/ (new tab)

[if skills list available]
h2: Skills
p.dim: Unchecked skills are disabled on next agent run.
  [checkbox list: {skill-name} for each stock skill]
  (disabled skills have .disabled marker in .claude/skills/{name}/)

  [button: save] (same form)

[links: Manage grants → /dash/groups/{folder}/grants]
[links: Browse tools  → /dash/groups/{folder}/tools]

<details>
  <summary>Danger zone</summary>
  <form method=post action=/dash/groups/{folder}/delete onsubmit="return confirm(...)">
    [button.btn-danger: delete group]
  </form>
</details>
```

#### Data shown

- Product, model (from groups table)
- open flag, observe_window settings (from groups table / store helpers)
- Stock skills list (from ant/skills/ in appDir)
- Per-skill disabled state (from .claude/skills/{name}/.disabled marker)

#### Actions

- Save form → POST /dash/groups/{folder}/settings → redirect back
- Delete group → POST /dash/groups/{folder}/delete (confirm JS dialog)
  removes DB row + purges ACL + routes + best-effort rm of groups dir
- Open workspace links → /dav/{folder}/ in new tab
- Manage grants → sub-page
- Browse tools → sub-page

#### Edge cases / empty states

- Group not found: banner-err "group not found"
- appDir unset: skills section omitted
- Skills list empty: omit skills section
- Save error (DB write): 500 + error message

---

### /dash/groups/{folder}/grants — Group Grants

**Requires visible (read) or admin (write) on folder.**

#### Purpose

Manage ACL grants for a group. View current grants, add new, revoke.

#### User stories

- As an operator, I want to grant a user access to a group so they can
  use the dashboard and chat.
- As an operator, I want to revoke a grant so a departed user loses access.

#### Layout

```
crumbs: Groups › {folder} › Grants
h1: Grants — {folder}

[table: Principal | Action | Scope | Effect | Granted At | Granted By | ]
  {sub}   admin   {folder}  allow  {ts}      dashd        [revoke button]

[section: Add grant]
<form method=post action=/dash/groups/{folder}/grants>
  Principal: [input text, placeholder: user:google:alice@example.com]
  Action:    [select: admin | mcp:* | ...]
  Effect:    [select: allow | deny]
  [button: add]
</form>

[button: ← Back to settings]
```

#### Data shown

- All ACL rows where scope = folder or scope LIKE folder/%
- principal, action, scope, effect, granted_at, granted_by

#### Actions

- Add grant → POST /dash/groups/{folder}/grants
- Revoke → POST /dash/groups/{folder}/grants/revoke (form with principal+action)

#### Edge cases / empty states

- No grants: "No grants for this group."
- Revoke last admin: should warn but currently not validated

---

### /dash/groups/{folder}/tools — Tool Browser

**Requires visible on folder.**

#### Purpose

Browse the MCP tools available to the group's agent.

#### User stories

- As a group admin, I want to see which tools the agent can call so I can
  verify the correct skill set is active.

#### Layout

```
crumbs: Groups › {folder} › Tools
h1: Tools — {folder}

p.dim: MCP tools available to the agent for this group.

[list of .tool-card <details>]
  <summary><code>{tool-name}</code></summary>
  <p>{description}</p>
  <pre>{input schema}</pre>
```

#### Data shown

- Tool names, descriptions, input schemas from the agent's MCP server

#### Actions

- None (read-only browser)

#### Edge cases / empty states

- No tools: "No tools registered."
- appDir unset: empty or error message

---

### /dash/routes/ — Route Table

**Operator-gated write; non-operators see routes for their visible folders.**

#### Purpose

Full routing rule table. Create, edit, delete routes.

#### User stories

- As an operator, I want to see and edit the full routing table so I can
  control which chats go to which group.
- As an operator, I want to add a route for a new Telegram group without
  CLI access.

#### Layout

```
h1: Routes
p.dim: Route table. Higher seq = earlier match.

[table: Seq | Match | Target | ]
  1   telegram:group/123456  alice     [edit inline] [delete]
  2   telegram:*             main/ops  [edit inline] [delete]

[section: Add route]
<form method=post action=/dash/routes/>
  Seq:    [number input]
  Match:  [input text, placeholder: telegram:group/123456]
  Target: [input text, placeholder: alice]
  [button: add]
</form>
```

#### Data shown

- All routes from `routes` table: seq, match, target
- Ordered by seq (ascending)

#### Actions

- Add route → POST /dash/routes/
- Edit route → PATCH /dash/routes/{id} (inline edit or dedicated form)
- Delete route → DELETE /dash/routes/{id} (or POST …/delete)

#### Edge cases / empty states

- No routes: "No routes. Messages will not be delivered until at least one
  route is configured."
- Seq conflict: last one wins (INSERT, not UPSERT)
- Target not in groups table: route is valid but will match nothing

---

### /dash/tokens/{folder}/ — Route Tokens

**Read: requireVisible. Revoke: requireAdmin.**

#### Purpose

List and revoke a group's web chat and webhook tokens.

**The cockpit mints nothing.** Issuance belongs to the resource owner —
routd's `POST /v1/route_tokens/{chat,hook}`, the agent's `issue_chat_link` /
`issue_webhook` / `issue_pairing_link`, and `arizuko token`. A mint form here
was a second writer over `route_tokens` carrying its own label validation
(BUGS `V2`; spec `6/30` §webd: "There is no token minting anywhere in the
cockpit"). Revocation stays: it is the action an operator needs where the
status is shown (design principle 6).

#### User stories

- As an operator, I want to revoke a token that was compromised so access
  stops immediately.
- As a group admin, I want to see which tokens are active, and with what
  context, so I know the group's access surface.

#### Layout

```
h1: Tokens — {folder}

[table: JID | Kind | Created | Context | ]
  web:alice   chat    2026-06-01 09:00                      [revoke]
  hook:alice/github  hook  2026-06-02 11:30  push events…   [revoke]

[section: Issuing a token]
  p.dim: Tokens are minted where they are owned, not here. Ask the agent
  for a chat link or a webhook, POST /v1/route_tokens/chat or
  /v1/route_tokens/hook, or run `arizuko token`. This page lists and revokes.
```

#### Data shown

- All route_tokens for folder: jid, kind (chat or hook), created_at UTC,
  context (spec 5/W link context)
- JID displayed as `<code>`. The token VALUE is never shown — `route_tokens`
  stores only its hash.

#### Actions

- Revoke → POST /dash/tokens/{folder}/{jid}/revoke → redirect back

#### Edge cases / empty states

- No tokens: "No tokens for this group."
- Revoke 0 rows: 500 error
- POST to the page itself: not routed (405) — the mint route is gone

---

### /dash/invites/ — Onboarding Invites

**Operator only.**

#### Purpose

Issue and revoke onboarding invites. Invited users follow the onboarding
flow (onbod) to create their group.

#### User stories

- As an operator, I want to issue an invite code so I can onboard a new
  user without CLI access.
- As an operator, I want to revoke an unused invite so it can't be used by
  the wrong person.
- As an operator, I want to see when invites were created and whether they
  were used so I know onboarding status.

#### Layout

```
h1: Invites
p.dim: Onboarding invites. Share the token with a new user to start onboarding.

[table: Token | Created | Expires | Used | ]
  {token-prefix}…  2026-06-01 10:00  2026-07-01  no   [revoke]
  {token-prefix}…  2026-05-28 09:00  2026-06-28  yes  —

[section: Create invite]
<form method=post action=/dash/invites/>
  [button: create invite]
</form>
```

#### Data shown

- All invites from onbod's store (dashd holds onbod's handle): token
  (truncated or prefix), created_at,
  expires_at, used flag

#### Actions

- Create invite → POST /dash/invites/ → redirect back (token visible in row)
- Revoke → POST /dash/invites/{token}/revoke → redirect back

#### Edge cases / empty states

- No invites: "No invites. Create one to onboard a user."
- onbod store unavailable: banner-err; section disabled
- Already used invite: revoke button hidden or disabled

---

### /dash/memory/ — Agent Memory Browser

#### Purpose

View and edit per-group agent memory files: MEMORY.md, PERSONA.md,
CLAUDE.md, diary, episodes, users, facts.

#### User stories

- As an operator, I want to read a group's MEMORY.md so I can understand
  what the agent has retained.
- As an operator, I want to edit MEMORY.md directly so I can correct
  wrong or stale memories.
- As a group admin, I want to view my group's diary entries so I can see
  what the agent has logged.

#### Layout

```
h1: Memory
p.dim: Browse per-group MEMORY.md, CLAUDE.md, diary, episodes, users, facts.

<form method=get class=form-narrow>
  Group: [select — all visible folders]
         (auto-submits on change)
</form>

[if no group selected]
p.empty: Select a group above to view its memory.

[if group selected]
[button.btn-secondary: Open workspace ↗ → /dav/{folder}/ (new tab)]

[section: MEMORY.md]
  p.dim: {N bytes} · modified {date} [· truncated at 1MB]
  <pre>{content}</pre>
  [edit: textarea + PUT /dash/memory/{folder}/MEMORY.md]
  [delete button → DELETE /dash/memory/{folder}/MEMORY.md]

[section: PERSONA.md — only if file exists]
  <details><summary>PERSONA.md</summary><pre>{content}</pre></details>

[section: CLAUDE.md — only if file exists]
  <details><summary>CLAUDE.md</summary><pre>{content}</pre></details>

[section: Diary]
  <details open><summary>{N} files</summary>
    <ul>
      <li><b>{filename}</b> {first-line-summary}</li> (newest first, max 100)
    </ul>
  </details>

[section: Episodes] (same pattern, collapsed)
[section: Users]    (same pattern, collapsed)
[section: Facts]    (same pattern, collapsed)
```

#### Data shown

- File content (capped at 1MB, truncated flag shown)
- File mtime
- Directory listings: newest-first, max 100 files per directory
- Truncated dirs: "showing newest 100" note

#### Actions

- Select group → form submit → re-render with group's memory
- Edit MEMORY.md → PUT /dash/memory/{folder}/MEMORY.md (body = new content)
- Delete MEMORY.md → DELETE /dash/memory/{folder}/MEMORY.md
- Open workspace → /dav/{folder}/ (new tab, full file editor)

#### Edge cases / empty states

- MEMORY.md not found: "MEMORY.md not found" (showMissing=true for this
  file only)
- PERSONA.md / CLAUDE.md not found: section omitted (showMissing=false)
- Diary/episodes/users/facts empty: section omitted (no `<details>` rendered)
- Symlink escape: "unavailable (symlink escape)" shown in place of content
- Non-operator accessing other folder: 403 (visible check before any file read)
- Read error: "read error" in place of content (showMissing=true paths only)

#### Allowed paths

MEMORY.md, `.claude/CLAUDE.md`, `PERSONA.md`, or any `*.md` directly
under `diary/`, `facts/`, `users/`, `episodes/` (no subdirs). All other
paths return 403.

---

### /dash/profile/ — User Profile

#### Purpose

Show current identity, linked OAuth accounts, and option to link more.

#### User stories

- As any user, I want to see my canonical identity so I can confirm which
  account I'm logged in as.
- As a user, I want to link a second OAuth provider so I can sign in via
  multiple methods.

#### Layout

```
h1: Profile
p.dim: Your account and the providers you can add to it.

[table: field | value]
  Your account ID  {sub (code)}
  Name             {name}        (if set)

[section: Add a provider]
  [oauth-btn: Link Google]    (hidden for the signed-in sub's own provider)
  [oauth-btn: Link GitHub]
  [oauth-btn: Link Discord]
  p.empty: All known providers already linked.  (if none to add)
```

Providers: Google, GitHub, Discord. Each button omitted when
that provider's prefix already appears in the canonical or linked sub list.

#### Actions

- Link provider → GET /auth/{provider}?intent=link&return=/dash/profile/
  → OAuth flow → redirects back with new sub linked

#### Edge cases / empty states

- No X-User-Sub: banner-err "no identity — sign in via proxyd"
- No linked accounts: table empty (no empty-state message — just no rows)
- All providers linked: "All known providers already linked."

---

### /dash/me/secrets — Per-User Secrets

**Identity-bound to signed-in X-User-Sub. CSRF: same-origin check.**

#### Purpose

View, create, update, and delete per-user secrets (e.g. API keys the
agent uses on behalf of this user).

#### User stories

- As a user, I want to store a personal API key so the agent can call
  external services on my behalf.
- As a user, I want to update a rotated key without asking an operator.

#### Layout

```
h1: Secrets
p.dim: Personal secrets bound to your identity. Visible only to you and
agents running on your behalf.

[table: Key | Created | ]
  OPENAI_API_KEY  2026-05-01  [edit] [delete]
  GITHUB_TOKEN    2026-06-01  [edit] [delete]

[section: Add secret]
<form method=post action=/dash/me/secrets>
  Key:   [input text, placeholder: MY_SECRET]
  Value: [input password]
  [button: add]
</form>
```

Values are never shown after creation. Rows show key name + created date only.

#### Actions

- Create → POST /dash/me/secrets
- Update value → PATCH /dash/me/secrets/{key}
- Delete → DELETE /dash/me/secrets/{key}

#### Edge cases / empty states

- No secrets: "No secrets. Add one above."
- Key conflict: 409 or update-in-place
- Value too long: 413

---

### /dash/channels/whatsapp/pair — WhatsApp Re-pair

**Operator only.**

#### Purpose

Re-pair the WhatsApp adapter when the session expires, without CLI access.

#### User stories

- As an operator, I want to re-pair whapd via the dashboard so I don't
  need SSH access to the server.

#### Layout

```
h1: WhatsApp pairing
p.dim: Re-pair the WhatsApp adapter when the session expires.

[section: Status]
GET /dash/channels/whatsapp/pair/status → polling display
  [status text: paired | awaiting QR | awaiting pairing code | unknown]

[section: Start pairing]
<form method=post action=/dash/channels/whatsapp/pair/start>
  Phone number: [input tel, placeholder: +1234567890]
  [button: request pairing code]
</form>

[if pairing code issued]
<code class=code-xl>{PAIRING_CODE}</code>
p.dim: Enter this code in WhatsApp → Linked Devices → Link a device.
```

#### Actions

- GET status → polls /dash/channels/whatsapp/pair/status (htmx or manual refresh)
- POST start → requests pairing code from whapd via dashd svc token

#### Edge cases / empty states

- AUTHD_SERVICE_KEY unset: form disabled, note "pair proxy unavailable (local dev)"
- whapd unreachable: status shows "unknown"
- Already paired: status shows "paired", form hidden or disabled

---

## Operator control planes

Each daemon serves its own control plane and dashd only links to it:
`/dash/routd/`, `/dash/runed/`, `/dash/authd/` and `/dash/proxyd/` are
mounted by routd, runed, authd and proxyd, and proxyd routes each prefix to
the owning daemon (spec [6/30](specs/6/30-per-daemon-dashboards.md)). Their
screens are specified next to the source, not here; `dashd/services.go`
carries the link row for each.

The pages below are dashd's own.

---

### /dash/approvals/ — Held-Call Review Queue

**Any signed-in caller; each row needs `admin` on its folder.**

#### Purpose

Spec 5/19's review queue: every tool call a `hold:mcp:<tool>` rule has
suspended, with approve/reject verdicts. The chat notice is the nudge; this
page is the queue an operator comes back to.

#### User stories

- As a folder's admin, I want to see every call waiting on ME — with its full
  arguments — so I can review before anything runs.
- As a folder's admin, I want to approve or reject from the row, with an
  optional note, so the verdict and its reason land in one action.

#### Layout

```
h1: approvals
p.dim: A hold rule pauses a risky tool call until a human says go. …

[banner-ok: "approved — the waiting agent runs the call now"]                (?msg=approved)
[banner-ok: "rejected — the call will not run"]                              (?msg=rejected)
[banner-err: "<routd's own reason>"]                                         (?err=…)

h2: Waiting for a verdict
[table: Group | Tool | Arguments | Chat | Held | ]
  {folder link}  <code>{tool}</code>  <details>{args}</details>  {chat link}  {abbr age}
  [note input] [approve] [reject.btn-danger]   (one form, two submit buttons)

h2: Recent verdicts   (omitted when none)
[table: Group | Tool | Outcome | By | When | Note]   (newest 20)
```

#### Data shown

`GET {ROUTER_URL}/v1/pending_actions?reviewer=<sub>` with the service:dashd
bearer (`pending_actions:read`) — status, tool, full args JSON, chat,
timestamps, reviewer fields. routd returns only the folders that sub holds
`admin` on, which is the same question its verdict gate asks. Arguments are
shown in full inside a collapsed `<details>`: they are the material under
review, never hover-only.

#### Actions

- Approve/Reject → `POST /dash/approvals/{id}/resolve` (verdict from the
  pressed button, note rides along) → routd
  `POST /v1/pending_actions/{id}/approve|reject` with the caller's sub as
  `reviewer` → 303 back with `?msg=`. The agent is suspended on the call, so
  the verdict reaching the row IS the delivery: the waiting turn runs it with
  the arguments shown, or is told it was refused and why.

#### Edge cases / empty states

- Nothing held: "Nothing is waiting. Held calls appear here the moment a hold
  rule fires."
- routd unreachable / non-2xx: banner-err with routd's reason; page ends
- Double verdict (raced with chat `/approve`): routd answers 409; the reason
  surfaces in the err banner
- Caller with no admin anywhere: the page renders empty, because routd returned
  nothing for them; unauthenticated: 401 and dashd makes no upstream call
- Verdict on a folder the caller does not hold: routd answers 403 and the
  reason surfaces in the err banner
- Portal shows a banner-warn "N tool calls held for your approval" linking
  here, counting only the calls that caller may answer

---

### /dash/audit/ — Audit Log

**Operator only.**

#### Purpose

Read-only browser for the `audit_log` table. Newest first, 50 rows per
page, with category/actor/folder filters and cursor-based pagination.

#### User stories

- As an operator, I want to audit who did what to which resource so I can
  investigate security incidents or unexpected state changes.
- As an operator, I want to filter by actor or folder to narrow the log to
  one user or group.

#### Layout

```
h1: audit

[inline filter form — GET /dash/audit/]
  <select name="cat">  all categories | {distinct categories from DB}  </select>
  <input name="actor"  placeholder="actor">
  <input name="folder" placeholder="folder">
  [button: filter]

[table: Time | Category | Action | Actor | Folder | Outcome | Resource | Surface | Params]
  <abbr title="{ISO}">{relative}</abbr>
  {category}
  {action}
  {actor}
  {folder}
  <span class="status-ok|err">{outcome}</span>  (error_msg in <abbr title> when set)
  <code>{resource}</code>
  {surface}
  {params_summary}

[button.btn: older →]  (when >50 rows; href carries filters + ?before={lastID})
```

#### Data shown

Federated, not read from a database: `audit_log` is per-daemon, so
`auditSources` fans out to `GET /v1/audit` on routd, runed and authd (plus
onbod when its profile is on) and `handleAudit` merges the answers newest
first (`dashd/audit_page.go`, spec 5/I).

- Fields: id, created_at, category, action, actor, folder, outcome, resource,
  surface, params_summary, error_msg, plus the source daemon
- Each source is asked for `auditPageSize`+1 rows under the caller's
  category/actor/folder filters; the extra row is what tells "exactly 50"
  from "more behind it"
- The cursor is composite — one `before` position per source
- `auditFanoutTimeout` (10s) bounds the whole fan-out

#### Actions

- Filter → GET /dash/audit/?cat=&actor=&folder=
- Older → GET /dash/audit/?before={id}&cat=&actor=&folder=

#### Edge cases / empty states

- A source that fails gets a named banner and the survivors still render. A
  source that cannot be read is NOT an empty source — rendering the rest
  silently would tell the operator "nothing happened in runed" when the truth
  is "runed did not answer"
- No rows matching filters: empty table
- error_msg set: shown as tooltip on the outcome cell (`<abbr title>`)
- No delete/purge UI — non-repudiation by design

---

### /dash/usage/ — Usage Analytics

**Operator only.**

#### Purpose

Instance-wide and per-group usage cockpit: aggregate message/token/cost
totals and a 7-day daily message-volume table.

#### User stories

- As an operator, I want to see total instance cost and message volume so I
  can judge spend against budget.
- As an operator, I want a per-group breakdown sorted by token usage so I
  can identify high-cost groups.
- As an operator, I want a day-by-day volume table for the last 7 days so I
  can spot traffic spikes or gaps.

#### Layout

```
h1: usage

[summary cards — .cols 3-column]
  .card  h3: {N}          p.dim: total messages
  .card  h3: {N}k         p.dim: tokens / 7d
  .card  h3: ${X.XX}      p.dim: cost / 7d

h2: Per-group
[table: Group | Msgs | Tokens/7d | $/7d | Last active]
  <a /dash/groups/{folder}/>{folder}</a>
  {n or —}   {Nk or —}   {$X.XX or —}   <abbr title="{ISO}">{relative}</abbr> or —
  (sorted by Tokens/7d descending)

h2: 7-day volume
[table: Date | Messages]
  2026-06-17  {n}
  2026-06-16  {n}
  ...
```

#### Data shown

Every figure arrives over routd's `/v1`; dashd reads no table of its own here.

- Folders: `usageFolders` → routd `/v1` group list, alphabetical
- Spend: `costSummary` → routd `GET /v1/cost/summary` in one call, 7-day window
- Per-group: `groupUsageBulk` joins that spend with routd's message counts,
  sorted descending by Tokens7d; "—" when zero
- 7-day volume: `messageVolumeViaRoutd`. routd owns `messages` and applies the
  `is_bot_message` filter itself, so this page cannot widen the histogram to
  include the agent's own output
- Summary totals: the per-folder rows summed; rendered raw, `{N}k` (≥1000),
  `${X.XX}`

#### Actions

- Click group link → `/dash/groups/{folder}/`

#### Edge cases / empty states

- Store unavailable: banner-err; page ends
- routd unreachable for the volume table: banner-err in that section only
- No model gateway on the instance: a banner says so, and every token and cost
  cell reads "no meter" — unknown, not zero
- Group with zero activity: all cells show "—"
- No messages at all: summary shows 0/0k/$0.00; per-group table empty

---

## HTML primitives available

From `theme/theme.go` (CSS classes) and `dashd/html_helpers.go`:

| Primitive                                      | Usage                                                    |
| ---------------------------------------------- | -------------------------------------------------------- |
| `.page-wide`                                   | Max-width 1100px centered page wrapper                   |
| `.card`, `.card-sm/md/lg/full`                 | Content card with border + shadow                        |
| `.tiles`                                       | Auto-fit tile grid (minmax 200px)                        |
| `.tile`                                        | Tile link card (hover border accent)                     |
| `.services-grid`                               | Auto-fill grid for service tiles (minmax 220px)          |
| `.service-tile`                                | Service health tile                                      |
| `.banner-ok/warn/err`                          | Status banners                                           |
| `.dot`, `.dot-ok/warn/err`                     | 8px inline status dots                                   |
| `.status-ok/err/unknown`                       | Status-colored text                                      |
| `.empty`                                       | Centered italic dim empty-state text                     |
| `.dim`                                         | Muted secondary text (.85em, --dim color)                |
| `.crumbs`                                      | Breadcrumb paragraph                                     |
| `.cols`                                        | Two-column responsive grid                               |
| `.section`                                     | Section card (card + border-bottom h3)                   |
| `.group-detail`                                | Indented group accordion body                            |
| `.form-narrow`                                 | Max-width 420px form container                           |
| `.tool-card`                                   | Collapsible tool browser card                            |
| `.btn`, `.btn-danger`, `.btn-secondary`        | Button variants                                          |
| `.code-xl`                                     | Large monospace (1.4em, for tokens)                      |
| `.accent3`                                     | Sky blue used for h2 section headers (--accent3 CSS var) |
| `<abbr title="{ISO}">`                         | Relative timestamp with ISO on hover                     |
| `hx-get`, `hx-trigger`, `hx-target`, `hx-swap` | htmx auto-refresh                                        |
| `htmx-indicator`, `#global-spinner`            | Loading indicator                                        |

Colors: `--ok` (#4ade80 dark / #1a7f37 light), `--warn` (#fa0), `--danger`
(#e5484d dark / #cf222e light), `--accent` (#58a6ff dark / #0969da light),
`--accent3` (#87ceeb dark / #1e40af light).
Dark mode is default; light mode via `[data-theme=light]` on `<html>`.
