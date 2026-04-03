# Daily Feature Tweets — arizuko

12 standalone tweets, one per day. Each builds on the previous.

Core message: every agent you build needs the same outside structure
— routing, auth, permissions, channels, containers. arizuko handles
that. Claude Code is the agent. Each component works on its own —
use one piece or all of them. No all-or-nothing adoption. Start
with what you need, add the rest when you need it.

---

## Day 1 — What it is

Every agent you build needs the same outside structure — routing,
auth, permissions, channels, containers. arizuko handles that.
Claude Code is the agent. Each component works on its own — use one
or all of them. No all-or-nothing. Start with what you need.

---

## Day 2 — Channels

8 adapters: Telegram, Discord, WhatsApp, email, Mastodon, Bluesky,
Reddit, web. Each is ~200 lines, one HTTP contract. Use one adapter
standalone or all eight together. Your agent doesn't know which
channel it's on. Add a platform without touching agent code.

---

## Day 3 — Containers

Each session runs in its own Docker container. Claude Code inside,
workspace mounted. Fresh process, no state leaks between sessions.
But files persist — diary, facts, config survive. Isolation is not
a feature. It's the only design that doesn't leak.

---

## Day 4 — Skills

A skill is a markdown file. Drop it in a directory, the agent reads
it next session. No code, no deploy, no registry. Plain text that
humans and LLMs both read. Write it once, share it across agents.
Skills are portable — they don't depend on the rest of the stack.

---

## Day 5 — Memory

Diary entries, facts, episodes — files on the workspace. 14 days
of summaries injected at session start. The agent knows what it
worked on yesterday without being told. No vector database. Just
files. Works the same whether you use one component or ten.

---

## Day 6 — Scheduling

A scheduled task is a message on the bus. Cron, one-shot, intervals.
The scheduler doesn't know about agents. Agents don't know about
the scheduler. Use the scheduler standalone or with the full stack.
They compose because the contract is a message, not a function call.

---

## Day 7 — Permissions

Groups form a hierarchy. Parent delegates to children, children
escalate to parent. Each group narrows permissions downward —
children never expand what the parent grants. Use this for one agent
or twenty. Every product needs it. Build it once.

---

## Day 8 — Auth

Every route except /pub/ requires auth. No config needed. JWT,
Google OAuth, GitHub OAuth, Discord OAuth, Telegram login — built
in. Public is opt-in, not opt-out. Drop it in front of any service.
Convention made it zero-config.

---

## Day 9 — Routing

Route by @name, prefix, keyword, sender. One chat fans out to many
agents. Default routes, sticky sessions, reply-chain tracking.
Routing is declarative. Use it to orchestrate one agent or a
hierarchy of them. Same component either way.

---

## Day 10 — MCP sidecars

Tool servers in separate containers, connected via unix socket.
Go, Python, Rust — anything that speaks MCP. Write a sidecar once,
plug it into any agent. Works with arizuko or without it. The
contract is MCP. Language doesn't matter.

---

## Day 11 — Self-modification

The agent edits its own skills, updates its config, runs /migrate
to propagate changes across groups. Same contracts a human would
use — markdown files, git commits. No deploy pipeline. The agent
is the deployment tool. That's what evolvable means.

---

## Day 12 — Why this way

Most agent frameworks are all-or-nothing. arizuko says: the agent
is Claude Code. Everything around it — routing, permissions, auth,
channels, containers — is independent infrastructure. Each piece
works alone. Use what you need, ignore the rest. Each new agent
starts from working parts instead of a blank page.

---

## Notes

- Each tweet standalone, reader saw previous ones
- Repo link in Day 1 and Day 12
- No emojis, no hashtags
- Voice: system designer, not marketing
- Core distinctions:
  - Inside (your agent's logic) vs outside (infrastructure)
  - Each component usable per se — no all-or-nothing adoption
  - Claude Code is the agent, arizuko is the infrastructure
- Adoption path: start with one component, add more as needed
