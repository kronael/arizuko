---
status: shipped
---

# Tenant self-service — the arizuko org-chart model

> Reference + planning spec. Nails down vocabulary, primitives,
> operations, and implementation phases. Code primitives this depends
> on: groups (existing), user_groups (existing), routes (existing),
> chats (existing — extends one column), plus two new tables (invites,
> secrets). Three implementation phases lined up after this lands.

## Vocabulary

A **group** is a folder identified by a path. Depth determines default
grant behavior; segment names are advisory. See [`ant/CLAUDE.md`
Tenancy section] for the depth → label cross-walk.

A **topic** is the transient work-unit overlaid on a group. Many
topics per group. Topics have a `kind` (task / project / meeting /
question / discussion / incident).

## The org-chart framing

arizuko encodes a working organization:

| Real-org concept      | arizuko primitive             |
| --------------------- | ----------------------------- |
| Organization          | world (top-level group)       |
| Department / function | sub-group at depth 2          |
| Team / role           | sub-group at depth 3+         |
| Job description       | grant rule list               |
| Mailroom / dispatcher | routes table (JID → folder)   |
| Reporting structure   | folder hierarchy              |
| Chain of command      | escalate / delegate verbs     |
| Hiring                | invite + grant                |
| Off-boarding          | revoke grant                  |
| Org-wide tools        | world (tier 1) secrets        |
| Team-specific tools   | sub-group secrets             |
| Personal tools        | user-scope secrets (1:1 only) |
| Ticket / case         | topic                         |

This isn't metaphor — every element 1:1-maps to standard org concepts.

## Invites

`invites` table — opaque tokens that produce `user_groups` rows on
acceptance.

```sql
CREATE TABLE invites (
  token         TEXT PRIMARY KEY,
  target_glob   TEXT NOT NULL,         -- e.g. "atlas/" (subgroup-create) or "atlas/support" (join)
  issued_by_sub TEXT NOT NULL,
  issued_at     DATETIME NOT NULL,
  expires_at    DATETIME,              -- nullable
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0
);
```

Two modes determined by `target_glob`:

- **Trailing slash** (e.g. `atlas/`) — subgroup-create mode: recipient picks a
  username, a new group `atlas/<username>` is created from `groups/atlas/prototype/`,
  and the user is granted access to it.
- **No trailing slash** (e.g. `atlas/support`) — join mode: user is granted
  direct access to the existing group.

`**` is a reserved folder name and is rejected as a target_glob.

Lifecycle:

1. Issuer (with grants on `target_glob`) calls `invite_create(target_glob, max_uses, expires_at)` → `token`
2. Recipient visits `/invite/<token>` → OAuth login → token consumed
3. On accept: group created (subgroup-create) or grant issued (join) → `INSERT INTO user_groups`
4. `used_count` increments; row stays for audit even after exhaustion

## Secrets

`secrets` table — two scope kinds.

```sql
CREATE TABLE secrets (
  scope_kind    TEXT NOT NULL,         -- "folder" | "user"
  scope_id      TEXT NOT NULL,         -- folder path OR user_sub
  key           TEXT NOT NULL,
  enc_value     BLOB NOT NULL,         -- AES-GCM(AUTH_SECRET)
  created_at    DATETIME NOT NULL,
  PRIMARY KEY (scope_kind, scope_id, key)
);
```

Resolution at container spawn for folder F, chat_jid C:

```
1. folder_secrets = walk F → root, last-wins
2. is_single_user = chats.kind in {"dm", "slink"}
3. if is_single_user:
       user_sub = unique user mapped to C
       user_secrets = lookup secrets where scope_kind="user", scope_id=user_sub
       env = base ∪ folder_secrets ∪ user_secrets
   else:
       env = base ∪ folder_secrets
```

Last-wins per key: deeper folder overrides shallower. User secrets
overlay folder secrets when single-user.

## chats.kind

Add column `chats.kind TEXT` with values: `dm | group | feed | room | slink`.

Adapter sets at first inbound on a chat_jid:

| Adapter | dm-attestation                  | group-attestation               |
| ------- | ------------------------------- | ------------------------------- |
| teled   | chat.id > 0                     | chat.id < 0                     |
| discd   | channel.type == DM              | channel.type == GuildText       |
| mastd   | visibility=direct on inbound    | otherwise                       |
| bskyd   | DM API endpoint                 | feed event                      |
| reditd  | inbox-message.kind == "t4" (DM) | submission/comment in subreddit |
| whapd   | jid not ending in `g.us`        | jid ending in `g.us`            |
| emaid   | 1:1 by default                  | mailing list (future)           |
| webd    | slink                           | (n/a)                           |
| twitd   | DM API                          | mention/reply on public tweet   |

Predicate `is_single_user(chat_jid) := chats.kind in {"dm", "slink"}`.

## Topic kinds

Topics carry an optional `kind` attribute:

- `task` — goal-oriented, has due-date, completes
- `project` — multi-step, has milestones, long-running
- `meeting` — scheduled, has attendees, has start/end
- `question` — single-resolution Q&A
- `discussion` — open-ended, may not complete
- `incident` — urgent, postmortem at end
- `thread` (default) — generic conversation

Kind drives kind-specific workflow operations (`set_due` on tasks,
`set_attendees` on meetings, etc.). Kind is metadata on the topic
node, not a separate hierarchy level.

## Operations matrix

Three orthogonal axes: hierarchy depth × operation group × actor.

```
Operation group      | Subjects                    | Actor       | Surface
---------------------|-----------------------------|-------------|----------
structural           | groups (any depth)          | human+agent | CLI / dashd / MCP
membership           | invites + user_groups       | human       | CLI / dashd
workflow             | topics (incl. kinds)        | human+agent | MCP / chat / dashd
```

Verbs:

- **Structural**: create, rename, archive, move, clone
- **Membership**: invite, accept_invite, grant, revoke, attach_channel
- **Workflow**: set_kind, set_due, set_assignee, complete, reopen,
  split, merge, delegate (push down), escalate (push up), move_topic
  (relocate to different group)

Existing arizuko already implements: `register_group`, `escalate_group`,
`delegate_group`, `schedule_task`, `pause_task`, `resume_task`,
`cancel_task`, `set_routes`, `add_route`, `delete_route`. New verbs
land via three sub-specs (40/41/42) only if surface grows past inline
description here.

## Implementation phases

Each phase is one or two commits. Build/test green at every step.

| Phase | What                                                                                                          | Lift     |
| ----- | ------------------------------------------------------------------------------------------------------------- | -------- |
| A     | This spec (you are reading it)                                                                                | shipped  |
| B     | `invites` table + invite-create / accept handlers in onbod + dashd                                            | 3-4 days |
| C     | `secrets` table + resolution at container spawn (`container/runner.go`)                                       | 4-5 days |
| D     | `chats.kind` column + adapter classification on first contact                                                 | 2-3 days |
| E     | dashd structural + membership UI (`/dash/<group>/structure`, `/dash/<group>/people`, `/dash/<group>/secrets`) | 5-7 days |
| F     | Topic kinds metadata + workflow ops MCP tools                                                                 | 3-4 days |
| G     | Cross-group operations (move_topic, split, merge)                                                             | 3-4 days |

Ship in order; each phase is useful even if later phases never land.

## What this consolidates

Specs already shipped or planned that this references (do not duplicate
their content):

- `5/28-mass-onboarding.md` — gates and admission queue (shipped)
- `5/29-acl.md` — `user_groups` glob ACL (shipped)
- `7/6-dynamic-channels.md` — channel adapter credentials (special case
  of folder-scope secrets); planned
- `5/30-inspect-tools.md` — read-only introspection (shipped)
- `GRANTS.md` (root) — composition mechanics
- `7/7-auth-tunneling.md` — web-based credential capture; planned
- `7/8-cli-auth-helper.md` — CLI auth dispatcher; planned

Channels become a _consumer_ of the secrets table (channel-creds-as-folder-scope-secrets-with-platform-validators). Auth-tunnel writes to the same secrets table.

## Out of scope (future)

- Time-bounded grants (`expires_at` on user_groups) — additive, ship later
- Audit log of permission changes — separate spec when needed
- Substitute / on-call rotation for routing — additive
- Cross-org collaboration — disallowed by design (orgs are isolation
  boundaries)
