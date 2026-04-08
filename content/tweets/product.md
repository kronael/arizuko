# Product Tweets — arizuko

12 standalone tweets, one per day. Each names a specific problem
people hit building agents and shows how arizuko solves it.
Day 1 lays out the concrete picture. Rest go deep on one problem each.

---

## Day 1 — The picture

arizuko: multitenant Claude agent router. Telegram, WhatsApp,
Discord, email, web — all route to containerized Claude Code
agents. Auth, permissions, routing, memory, scheduling as
independent components. Add a platform without touching agent
code. The infrastructure grows with you.

github.com/kronael/arizuko

---

## Day 2 — Adding a channel

Problem: you built an agent for Telegram. Now you need WhatsApp.
That's a rewrite. In arizuko, a channel adapter is ~200 lines and
one HTTP contract. The agent never knows which channel it's on.
Add a platform in an afternoon without touching agent code.

---

## Day 3 — Session bleed

Problem: one agent session corrupts another. Shared memory, shared
state, shared crashes. In arizuko, each session gets its own Docker
container. Fresh process, no leaks. But the workspace persists —
files, config, diary survive between sessions. Isolation by default.

---

## Day 4 — Teaching the agent

Problem: giving the agent a new capability means changing code and
redeploying. In arizuko, a skill is a markdown file. Drop it in a
directory. Next session the agent reads it and can do the thing.
No code, no deploy, no registry. Write once, share across agents.

---

## Day 5 — Forgetting everything

Problem: the agent has no idea what it did yesterday. Every session
starts blank. In arizuko, the agent writes diary entries and facts
to its workspace. 14 days of context injected at startup. Memory
is files on disk. No vector database. It just remembers.

---

## Day 6 — Running on schedule

Problem: you need the agent to check something every hour or post
a weekly report. In arizuko, a scheduled task is a message on the
bus. Cron, one-shot, intervals. The scheduler is a separate daemon.
Agents don't know about it. It just sends messages on time.

---

## Day 7 — Permissions chaos

Problem: you have five agents and no idea who can do what. One
misconfigured agent accesses everything. In arizuko, groups form a
hierarchy. Each level narrows permissions — children never expand
what the parent grants. Principle of least privilege, structural.

---

## Day 8 — Auth as afterthought

Problem: you ship the agent, then remember auth. Bolt it on, miss
an endpoint, get burned. In arizuko, every route except /pub/
requires auth from day one. JWT, Google/GitHub/Discord OAuth,
Telegram login — built in. Public is opt-in. Zero config.

---

## Day 9 — Who handles what

Problem: messages arrive but you're manually routing them to the
right agent. In arizuko, routing is declarative — @name, prefix,
keyword, sender rules. One chat fans out to many agents. Sticky
sessions, reply-chain tracking. The agent just sees its messages.

---

## Day 10 — Agent needs tools

Problem: the agent needs to call an API, query a database, run a
script. In arizuko, MCP sidecars run in separate containers over
unix socket. Go, Python, Rust — anything that speaks MCP. Write it
once, plug it into any agent. The contract is the protocol.

---

## Day 11 — Agent evolving itself

Problem: updating agent behavior means a deploy. In arizuko, the
agent edits its own skills, updates its config, runs /migrate to
push changes to every group. Markdown files and git commits. No
pipeline. The agent is the deployment tool.

---

## Day 12 — Why not just build it yourself

You could build all of this. Routing, auth, permissions, channels,
containers, memory, scheduling. You'd spend months on structure
instead of on your agent. arizuko already did it. Claude Code is
the agent. Start from working infrastructure, not a blank page.

---

## Thread replies (posted as replies to Day 1)

### Reply 1 — Research product

Problem: you researched something last month. It's gone — scattered
across chats and tabs you closed.

arizuko agents build a knowledge base that persists. /facts
researches a topic, verifies it, stores it with timestamps.
/recall-memories searches everything. Deploy findings as a web
page with OAuth. Your research compounds.

github.com/kronael/arizuko

---

## Notes

- Each tweet standalone, assumes reader saw previous ones
- Repo link in Day 1 and Day 12
- No emojis, no hashtags
- Voice: name the problem, show the solution, no fluff
- Every tweet follows: Problem → arizuko's answer → why it matters
- Day 1 is the overview, Days 2-11 go deep, Day 12 closes
