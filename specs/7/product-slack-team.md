---
status: planned
brand: slack-team
---

# Product: slack-team

_One agent in your team's Slack. Shared channel context, per-user
memory and grants._

Public pitch: `template/web/pub/products/slack-team/index.html`.
Shipped as `ant/examples/slack-team/` via the existing `--product`
flag on `arizuko create`. NO Go changes, NO migrations, NO new
daemons — everything below composes from primitives already on
`main`.

## What's actually free today

Verified by reading the source before writing this spec:

| Promise on the product page                    | Primitive that already supports it                                                                                                                                                                                                                                  |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Slack channel → group folder                   | `slakd/` adapter exists (`slakd/jid.go`, `slakd/main.go`); routes a `room=slack:<ws>/channel/<id>` match to a folder via `arizuko group add` / dashd.                                                                                                               |
| Per-channel `CLAUDE.md` override               | `container.SetupGroup` copies `<prototype>` verbatim into `groups/<folder>/`; agent reads `~/CLAUDE.md` on session start.                                                                                                                                           |
| Per-channel `PERSONA.md`                       | `gateway/persona.go` — frontmatter `summary:` re-anchored every turn; full body loaded once at session start.                                                                                                                                                       |
| Per-user memory file                           | `router.UserContextXml` (`router/router.go:224`) writes `<user id=… memory="~/users/<id>.md"/>` if the file exists; `ant/skills/users/SKILL.md` reads/writes it.                                                                                                    |
| Per-user grants                                | `user_groups` rows + GRANTS.md "deepest match wins"; no template work needed.                                                                                                                                                                                       |
| Channel-scoped secrets injected into agent env | `container.resolveSpawnEnv` (`container/runner.go:642`) merges `FolderSecretsResolved(folder)` into the agent container spawn env.                                                                                                                                  |
| Per-user secrets at tool-call time             | **Not free.** Storage exists (`store/migrations/0034-secrets.sql`, `Store.SetSecret`/`GetSecret`, `UserSecrets`); resolution path is folder-only for group chats — `resolveSpawnEnv` only overlays user secrets when `chats.is_group = 0`. See "Honest gaps" below. |
| `SECRETS.toml`                                 | **Vapour today.** The only repo refs are the product HTML page and `specs/8/14-plugins.md`. The actual at-rest store is the `secrets` SQLite table. Treat `SECRETS.toml` as the eventual operator-edit format; ship the product against the table.                  |

## File set to ship

Drop these into `ant/examples/slack-team/`. Mirrors the existing
`support` template structure (`specs/7/P-product-templates.md`).

```
ant/examples/slack-team/
  PRODUCT.md          manifest (TOML; consumed by cmd/arizuko cmdCreate)
  CLAUDE.md           channel runbook (KB lookup, per-user memory rules,
                      secret-redaction rules, escalation)
  PERSONA.md          channel persona; frontmatter summary: drives per-turn block
  facts/              empty placeholder + README for operator
  users/              empty placeholder + README documenting `<channel>-<id>.md` format
  SECRETS.example.toml  documentation-only; explains the eventual file format
                        and the `arizuko secret set` runbook below (this file is
                        copied into the group dir but is never read by code)
```

`PRODUCT.md` (concrete):

```toml
name    = "slack-team"
brand   = "slack-team"
tagline = "Team agent in your Slack channel — shared persona, per-user memory and grants."
skills  = ["diary", "facts", "recall-memories", "users", "issues", "web", "hello"]

# Operator setup
#
# 1. Create the Slack App, set bot token + signing secret in .env
# 2. Subscribe events to https://<WEB_HOST>/slack/events
# 3. arizuko run <instance>
# 4. Invite the bot to a Slack channel
# 5. arizuko group <instance> add slack:<workspace>/channel/<chanid> <name>
# 6. Each teammate signs in via OAuth at /auth/login to link their Slack
#    user id → canonical sub (see auth skill)
# 7. (Optional) operator pre-seeds per-folder API tokens via
#    `sqlite3 store/messages.db` (runbook below)

[[env]]
key      = "SLACK_BOT_TOKEN"
required = true
hint     = "xoxb-… from Slack App OAuth & Permissions"

[[env]]
key      = "SLACK_SIGNING_SECRET"
required = true
hint     = "Slack App > Basic Information > Signing Secret"

[[env]]
key      = "WEB_HOST"
required = true
hint     = "Public hostname; Slack delivers events to https://<WEB_HOST>/slack/events"

[[env]]
key      = "AUTH_SECRET"
required = true
hint     = "32-byte hex; AES-GCM key for secrets at rest (see SECURITY.md)"
```

`CLAUDE.md` body (channel-specific runbook — drop-in like
`ant/examples/support/CLAUDE.md`):

- Per-user memory: on first inbound from a Slack user, check
  `~/users/<id>.md` (`<id>` resolved by gateway as
  `sl-<workspace>/channel/<chanid>/<userid>` — note quirk below).
  Use `/users update <id>` to record durable facts.
- Channel boundary: never echo files outside `~/`, never reveal
  another channel's persona or facts.
- Secrets: env vars set by the spawn-time merge (folder secrets) are
  accessible via `process.env` inside agent tools. Never print them.
- Escalation: log unresolved questions to `~/issues.md`.

`PERSONA.md` frontmatter mirrors `ant/examples/support/PERSONA.md`
but with a team-oriented register (collaborative, concise,
context-aware that there are multiple humans in the room).

## User-file id resolution — the slack quirk

`router.platformShort` (`router/router.go:201`) hard-codes
`{telegram→tg, whatsapp→wa, discord→dc, email→em, web→web}`. `slack`
is absent, so `senderToUserFileID` falls back to "first 2 chars of
platform" → user files land at
`~/users/sl-<workspace>/channel/<chanid>/<userid>.md`.

The `/` inside the platform-id is **legal in filenames** but means
the file is actually at
`~/users/sl-<workspace>/channel/<chanid>/<userid>.md` — i.e. nested
directories. The skill `ant/skills/users/SKILL.md` accepts the id
verbatim from the gateway-injected `<user>` tag; the agent doesn't
need to know the structure as long as it uses the id the gateway
gave it.

If we want flat files (`sl-<workspace>-<chanid>-<userid>.md`), that
is a 5-line change to `senderToUserFileID` — **out of scope for
this spec** by the no-Go-changes rule. The nested form works today.

Document this in `users/README.md` inside the template so operators
who browse the WebDAV mount aren't surprised.

## Setup runbook (single screen)

```bash
# 1. Seed instance from the slack-team template
arizuko create acme --product slack-team

# 2. Fill secrets in .env (printed checklist after step 1)
cd /srv/data/arizuko_acme
$EDITOR .env   # SLACK_BOT_TOKEN, SLACK_SIGNING_SECRET, WEB_HOST, AUTH_SECRET

# 3. Boot
arizuko run acme

# 4. In Slack: create app, set scopes, set event URL to
#    https://<WEB_HOST>/slack/events, install to workspace,
#    invite the bot to a channel.

# 5. Bind the Slack channel JID to a group folder
arizuko group acme add slack:T01ABC/channel/C02XYZ eng-support

# 6. (Optional, until per-user-secret UI ships) pre-seed a folder secret
#    for an API the agent calls — e.g. a shared Jira token for #eng-support:
sudo sqlite3 /srv/data/arizuko_acme/store/messages.db \
  "INSERT INTO secrets (scope_kind, scope_id, key, enc_value, created_at) \
   VALUES ('folder','eng-support','JIRA_TOKEN', \
   X'$(arizuko secret encrypt acme "atlassian-pat-...")', \
   datetime('now'));"
# (NOTE: `arizuko secret encrypt` does not exist today either — see gap 2)

# 7. Teammates sign in via OAuth so per-user grants/memory link to canonical sub
#    Open https://<WEB_HOST>/auth/login on each teammate's first visit.
```

## Honest gaps — what doesn't work without code

1. **Per-user secrets in a Slack channel.**
   `container/runner.go:642` overlays `UserSecrets(userSub)` only
   when `GetChatIsGroup(chatJID) == false`. Slack channel JIDs are
   group chats, so user secrets are **never merged** into the agent
   spawn env for them. The agent can't access Alice's GitHub PAT
   on a turn caused by Alice's message in `#eng-support`.

   Spec covering this: **`specs/9/11-crackbox-secrets.md`** (with
   scope extension and dashboard write path folded in).
   Phase A adds:
   - `/dash/me/secrets` UI for users to paste tokens.
   - Spawn-time per-user overlay (the `is_group=1` filter in
     `resolveSpawnEnv` is removed; since spawn is per-turn and
     single-caller, the caller user_jid is known at spawn).
   - Container env carries an opaque placeholder; egred substitutes
     the real value into outbound HTTP headers at egress per spec 11.
     No MCP tool, no per-turn IPC; tools read placeholders from env
     exactly like channel-scoped secrets today.

   Until that ships, the product covers **shared, channel-scoped
   tokens** (a team's shared Jira/GitHub bot key), not per-teammate
   credentials. The page promises both; the template should be
   honest in `PRODUCT.md` comments that per-user secrets are
   pending.

2. **No CLI / dash UI to set a folder secret.**
   `Store.SetSecret` exists but nothing wraps it. Operators today
   need direct SQLite + manual AES-GCM encryption (which means
   writing a one-off Go program — not realistically operator-grade).
   Smallest no-code fix that's still operator-usable:
   - Ship a tiny shell helper at `template/products/slack-team/init.sh`
     that the operator runs once: it prompts for `JIRA_TOKEN` etc.,
     calls `arizuko chat <instance>` to drop into the root MCP
     socket, and asks the root agent to call a `set_folder_secret`
     MCP tool — **except that tool doesn't exist either**.
   - Cleanest no-code path: defer secret-setting to the
     forthcoming `specs/9/11-crackbox-secrets.md` dashboard UX. For now, document the
     direct-SQLite pattern (with a small Go helper at
     `cmd/arizuko-secret/` — but that IS a new binary, so out of
     scope here).

   **Recommended template behaviour**: `PRODUCT.md` lists
   `SLACK_BOT_TOKEN` / `SLACK_SIGNING_SECRET` as standard `.env`
   vars (already free — adapter env, not folder secret). Agent-side
   tool API keys go into the same `.env` for v0 of the product.
   Folder-scoped secrets (true `secrets` table) require either:
   (a) `specs/8/14-plugins.md` (operator UX via `arizuko plugin`),
   or (b) `specs/9/11-crackbox-secrets.md` (dashboard
   `/dash/me/secrets`). Both are
   out of scope here.

3. **"Remember my Jira token is XXX" via DM.**
   No skill exists today that calls `Store.SetSecret`. Writing one
   would need a new MCP tool (`store_user_secret`), which is a
   Go change. Out of scope.

## Acceptance criteria

The operator can verify the template is correctly installed with:

1. `arizuko create acme --product slack-team` exits 0 and prints
   the env checklist for `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`,
   `WEB_HOST`, `AUTH_SECRET`.
2. `ls /srv/data/arizuko_acme/groups/main/` shows
   `CLAUDE.md`, `PERSONA.md`, `facts/`, `users/`,
   `SECRETS.example.toml`, `.claude/`, `logs/`.
3. `/srv/data/arizuko_acme/groups/main/.claude/skills/users/SKILL.md`
   exists (seeded by `seedSkills`).
4. After `arizuko run acme` and a Slack channel binding, an
   inbound message in the channel produces a prompt containing
   `<user id="sl-<workspace>/channel/<chan>/<user>" />` (verify
   with `inspect_session` or by tailing `logs/`).
5. After the agent writes `~/users/<id>.md` with `name:` frontmatter,
   the next inbound prompt shows
   `<user id="…" name="…" memory="~/users/<id>" />`.
6. `~/PERSONA.md` frontmatter `summary:` text appears in every
   per-turn `<persona>` block.
7. A folder secret manually inserted into `secrets` (scope_kind=
   `folder`, scope_id=`main`) appears as an env var in the agent
   container — verify with a one-shot skill: agent runs
   `echo $JIRA_TOKEN` and the value matches.

If 1–6 pass, the product template is mechanically correct. (7)
verifies the secret integration path that the page promises for
_channel-scoped_ keys; per-user secrets require
`specs/9/11-crackbox-secrets.md`.

## Open

- Should `slack` be added to `router.platformShort` (`sl`) so user
  files land at flat paths like `sl-<chanid>-<userid>.md`? Separate
  one-line PR; not part of this product spec.
- `SECRETS.toml` as the canonical operator-edit format vs the
  `secrets` SQLite table as ground truth: pick one, write a
  reconciler. See `specs/8/14-plugins.md`. Out of scope here.
- `arizuko secret <set|list>` CLI subcommand to remove the
  direct-SQLite step from step 6 of the runbook. Trivial code,
  but new code — defer to its own spec.
