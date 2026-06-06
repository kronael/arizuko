---
status: draft
---

# specs/6/G — Slack multi-workspace + OAuth install

## What this solves

One slakd instance handles one workspace (manual `xoxb-` token). Enterprise
orgs need OAuth install flow (admin console approval) and multiple workspaces
per deployment without running N separate instances.

## Scope

- OAuth 2.0 install flow: `/slack/oauth/start` → Slack → `/slack/oauth/callback`
  stores `access_token` per `team_id` in the `secrets` table
- Multi-workspace dispatch: slakd routes events by `team_id` to the right
  group folder; one slakd process, N workspaces
- Per-workspace bot token stored encrypted (depends: specs/6/E)
- Workspace → folder mapping configurable via routes table or a new `workspaces` table

## Not in scope

- Enterprise Grid (org-level install)
- Socket Mode (HTTP webhooks only)
- Slash commands / modals / Block Kit
- Per-turn cost telemetry (separate spec)

## Open questions

- Single `workspaces` table or reuse `secrets` table with a `slack_token:<team_id>` key?
- Does each workspace get its own group subtree or share one?
- Token refresh: Slack bot tokens don't expire; rotation is manual revoke + reinstall.
