---
status: draft
depends: [23-agent-habitat, 24-skill-search, 25-publish-plan]
relates-to: [../5/33-paths-roles, ../6/41-qemu-substrate]
---

# specs/7/27 — the minimum arizuko: a folder, a way to speak, and a skill

A decision record, not work to do. It names the smallest thing that is still
arizuko, because the cloud track has to deploy something and "all of it" is not
an answer. [`23-agent-habitat.md`](23-agent-habitat.md) says what the layer IS
— ingest, route, grant, over an ephemeral turn. This says what an operator has
to HAND an agent before any of that matters.

## The claim

Give an agent three things:

1. **A folder it can read and write, reachable as files.** Not an API for
   storage — a directory, over WebDAV, that a person opens in Finder and the
   agent sees at `/home/node`.
2. **A way to message the other agents.** Not a queue to configure — four verbs
   that already know where the other folders are.
3. **A skill.** A Markdown file in that folder which changes what the agent
   does next turn.

Everything else arizuko has is in service of those three surviving contact with
a second tenant.

## Measured, 2026-10-10

All three ship today. This spec exists to name them as the minimum, not to add
anything.

**The folder, as files.** `davd` wraps `sigoden/dufs` and serves
`<DATA_DIR>/groups/` over WebDAV (`davd/README.md`, `davd/Dockerfile`), mounted
at `/dav/` behind the user gate and gated on `WEBDAV_ENABLED`
(`compose/compose.go:534`). The same bytes are the agent's `/home/node`, and
three more slots hang off it: `/var/lib/share` for the world's handoff space,
writable, downgraded to read-only by a `share_mount` grant
(`runed/mounts.go:67-76`); and `~/public_html` / `~/private_html`, which exist
only when a grant says so (`runed/mounts.go:150-151`).

One surface, three readers: a person in a file browser, the agent in its own
home, and the web tree. Nothing converts between them because there is nothing
to convert — it is one directory.

**The way to speak.** Four hand-authored MCP verbs, the hot tier that
`specs/CLAUDE.md` names as the only MCP-only tools: `send` and `reply`
(`ipc/ipc.go:1231`, `:1254`) reach a chat; `delegate_group` hands work down and
`escalate_group` hands a prompt up (`ipc/ipc.go:1951`). An agent addresses a
folder, not a transport. Which folders it may address is the `acl` row, and
`5/33-paths-roles.md` is the model.

**The skill.** 95 ship in `ant/skills/` and reach a folder by seeding
(`container/runner.go:222` for package skills, over the stock set). A folder's
own `.claude/skills/` outranks both, so an agent that writes a skill into its
own folder has changed itself — which is the whole of the loop, and why
`24-skill-search.md` is about finding them rather than installing them.

## Why this is a 7 spec

The cloud track asks what a deployable part is
([`25-publish-plan.md`](25-publish-plan.md)). This is the floor under that
question: a part that cannot give an agent a folder, a verb and a skill is not
an arizuko, whatever else it ships. It is the acceptance test for a minimal
deployment, and it is deliberately not a feature list.

## What this spec does NOT say

It does not say the three are sufficient. They are sufficient for ONE tenant,
and arizuko's weight is almost entirely the second one:

- The folder needs a tenancy coordinate, or two agents share it. That is the
  folder path itself, and `5/33-paths-roles.md` is why depth confers nothing.
- The verbs need a gate, or any agent messages any folder. That is
  `db.AuthorizeTurn` on the agent socket (root `CLAUDE.md`).
- The skill needs a boundary, or a folder's own skill reaches the platform's.
  That is the seeding order above.
- And the turn needs to end, or none of the three decisions can be made before
  the agent exists. [`../6/41-qemu-substrate.md`](../6/41-qemu-substrate.md)
  §"The turn" is where that lands now.

So the honest form of the claim is: **three things for the first agent, and
everything else for the second.** A reader who takes the first half as the whole
design will rebuild the second half by hand, which is the misreading this
section exists to prevent.

## Ties

[`23-agent-habitat.md`](23-agent-habitat.md) (what the layer is) ·
[`24-skill-search.md`](24-skill-search.md) (finding a skill once there are many) ·
[`25-publish-plan.md`](25-publish-plan.md) (what a deployable part is) ·
[`../5/33-paths-roles.md`](../5/33-paths-roles.md) (the tenancy coordinate) ·
[`../6/41-qemu-substrate.md`](../6/41-qemu-substrate.md) (why the turn ends)
