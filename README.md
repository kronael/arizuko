# arizuko

arizuko runs many AI agents on one Linux server you own. Each agent is a
folder of plain files: who it is, what it remembers, what it can do, and which
chats reach it. You change an agent by talking to it, and you read the result
as a diff on disk. Small daemons around the folders decide which chats reach
which agent and what each agent may do beyond its own folder.

[Install](INSTALL.md) • [How it works](ARCHITECTURE.md) •
[Security](SECURITY.md) • [Routing](ROUTING.md) • [Extending](EXTENDING.md) •
[Web docs](template/web/pub/arizuko/), served at `/pub/arizuko/` on every
instance

arizuko is simple on purpose, and the simplicity is the reason it is flexible.
An agent is a folder, a route is a row, and a grant is a row. You read a folder
as files and a row with the `arizuko` command.

arizuko runs only on a host you own: one Linux host with Docker and KVM. It
serves agent teams, not consumer traffic.

krons, at https://krons.fiu.wtf, is the reference deployment. The maintainers
run it, every release deploys there first, and it serves the web docs at
https://krons.fiu.wtf/pub/arizuko/.

## Glossary

- **folder**: one agent, with its persona, skills, memory and diary.
- **group**: a folder that routd has registered. `arizuko group` manages it.
- **world**: a top-level folder, such as `corp` in `corp/eng/sre`.
- **JID**: the address of a chat, such as `telegram:group/123456789`.
- **route**: a row that sends the messages of a chat to one folder.
- **topic**: a thread inside a chat. Each topic keeps its own session.
- **turn**: one agent run. A KVM guest boots, answers, and powers off.
- **grant**: an `acl` row. It allows or denies one action on one scope for one
  principal.
- **route token**: a secret URL, `/chat/<token>/` or `/hook/<token>`.
- **package**: a source of skills, native hooks, tools or system services.
  Core is always installed. [Install packages](EXTENDING.md#installing-packages)
  system-wide or for one group.

## What you can run with it

- A team agent in Slack, Discord or Telegram that answers on mention or in DMs,
  with its own persona and memory per channel.
- A personal assistant that keeps conversation history, a diary and a profile
  of you across restarts.
- One agent across several channels at once. Slack, Discord, email, WhatsApp
  and web chat all feed the same store.
- Scheduled work. `timed` fires a turn on a cron schedule; no webhook needed.
- An email or webhook agent. `emaid` reads IMAP and checks DMARC; any caller
  can POST to `/hook/<token>`.
- Search over your own documents. Mount a tree over WebDAV (`davd`) and the
  agent reads and cites it; `find_messages` ranks past conversation by
  content.
- Many tenants on one deployment. `corp/eng/sre` and `solo/inbox` run the
  same code at any folder depth.
  [concepts/scaling.html](template/web/pub/arizuko/concepts/scaling.html)
  adds a tenant with rows and a folder.

Under all of these, one folder path binds the tenant, its grants, its routes,
its egress, its web host and its file tree, and one evaluator answers both
the agent over MCP and a person over REST.

## Every message passes six steps

Each folder sees only the conversations that routes send to it. Every event,
from a chat message, a webhook or a scheduled task, passes the same six steps:

<!-- twin:six-steps -->

```
Slack @mention in #eng
  → Event          one inbox row (routd.messages)
  → Routing        → corp/eng/oncall
  → Agent          folder loaded: persona, skills, memory
  → Authorization  may it read / send / delegate here?
  → Turn           one ephemeral KVM guest run, then it exits
  → State          rows written, files edited
  → reply in the Slack thread
```

<!-- /twin -->

Channels, scheduled tasks, webhooks, secrets and delegation all use these six
steps. The path of a folder, such as `corp/eng/oncall`, names the agent in each
step.

A product is a template for the contents of a folder, such as `support` or
`personal`. Every product passes the same six steps.

## Each turn runs on its own machine

Every turn boots its own KVM guest. The guest root is the release rootfs,
read-only, under a writable layer of 512 MiB in RAM. The folder home and its
web directories come from the host and keep what the agent writes. When the
turn ends, the guest powers off and the writable layer is gone. A folder runs
one guest at a time, and a message that arrives during a turn goes into the
running guest.

An agent gives work to a sub-agent on a machine of its own through a second
folder. `register_group` creates a child folder, and `delegate_group` puts a
prompt in the queue of that folder. The child runs its own turn, in its own
guest and session, and replies in the chat that the parent names.
`escalate_group` sends a prompt up to the parent folder. Each tool needs a
grant. Claude Code subagents that start inside a turn share the guest of that
turn. [concepts/turn.html](template/web/pub/arizuko/concepts/turn.html#children)
gives the limits.

## When to use arizuko

Use it when an agent has to live in real chat channels and keep a separate
identity per team, customer or workflow:

- one agent per folder, each with its own persona, memory, skills and
  permission rows
- Slack, Discord, Telegram, email, web chat and webhooks as first-class inputs
- scheduled work, delegation between agents and long-running background jobs
- one machine, inspectable as files, routes and store rows

Do not pick it for enterprise document search. There is no connector sync and
no vector index; pair it with a retrieval system and let arizuko act on the
results (see [What arizuko does not include](#what-arizuko-does-not-include)).
For a coding assistant on one developer machine, a single-user tool is a
closer fit. arizuko is for agents that sit behind channels and serve teams.
[strategy/index.html](template/web/pub/arizuko/strategy/index.html#comparable)
lists the comparable systems and how each one differs.

## Getting started

You need a Linux x86_64 host with `/dev/kvm`, Docker with Compose v2.20 or
newer, Go, write access to `/srv/data/`, and a token for one channel. The
commands below show the intended instance workflow. Before the first
`package add`, the instance needs its images, store server and owner tables provisioned:

```bash
./arizuko create -i demo --product slack-team  # makes /srv/data/arizuko_demo
$EDITOR /srv/data/arizuko_demo/.env             # set credentials and image paths
# Provision the store and owner tables; see the bootstrap gap below.
./arizuko package add -i demo catalog:slakd    # select a service package
./arizuko run -i demo                          # generates compose, starts the instance
# In a second terminal, after routd has booted, install the product files:
./arizuko package sync -i demo
```

[INSTALL.md](INSTALL.md#bootstrap-a-fresh-store) describes manual store
bootstrap and configuration. Mint the store key of
a daemon before you enable its package, or the whole instance restarts in a
loop. INSTALL.md also holds the checklist for an agent that does not reply.
[EXTENDING.md](EXTENDING.md) covers packages, products and adapters.

## How it works

Three daemons form the core. `routd` appends every message and decides which
folder answers. `runed` boots one KVM guest per turn. `authd` signs every
token. Adapters, the web chat and the scheduler call `routd` over HTTP. Each
daemon keeps its own tables on one store server and signs in with its own key.
[ARCHITECTURE.md](ARCHITECTURE.md#where-a-change-lands) draws the repository
map, the table owners and the turn from start to end. Each chat platform has
its own adapter daemon: [EXTENDING.md](EXTENDING.md#verb-support-matrix) lists
the adapters and the verbs each supports, and
[components/channels.html](template/web/pub/arizuko/components/channels.html)
describes the protocol they speak.

## What arizuko does not include

- connector crawlers for Confluence, Notion, Google Drive, Jira or Salesforce
- an embedding pipeline or a vector database
- permission sync from external systems into per-document ACLs
- a different model provider per task; one harness serves the whole instance,
  Claude Code by default, or `codex` or `pi` through `ARIZUKO_BACKEND`
- a managed control plane or a hosted service

You can mount or write documents into a group's folder, and the agent reads
them directly. For large-scale retrieval, pair arizuko with a retrieval stack
and let it do what it does: receive messages, keep per-group memory, enforce
grants, schedule tasks, act across channels.

## Security model

Each turn runs in its own KVM guest with its own network tap. A proxy that
lives for one turn filters the egress of the guest. Model credentials enter the
guest, and so does each key that a `shell:env:<KEY>` grant names. One function
reads the `acl` table and decides every action, and a deny row wins. Only
`proxyd` stamps identity headers. [SECURITY.md](SECURITY.md) holds the threat
model and the boundary table.

## One handler serves each stored resource

Each stored resource, such as routes or tasks, has one handler. It serves a
REST interface for people and an MCP interface for agents, behind one
authorization gate. [ARCHITECTURE.md](ARCHITECTURE.md#cold-tier-vs-hot-tier)
lists the exceptions. The store holds messages, routes, grants and tasks. The
folder on disk holds the files of each agent. Both survive a restart.
`arizuko export` writes store configuration to YAML. The
`installed_packages` rows select packages, and `arizuko package sync` installs them.

## How to read this

| Order | File                                             | The question it answers                                               |
| ----- | ------------------------------------------------ | --------------------------------------------------------------------- |
| 1     | this file                                        | What is it, and how do I start?                                       |
| 2     | [INSTALL.md](INSTALL.md)                         | How do I get an instance running, and what do I do when it is silent? |
| 3     | [ARCHITECTURE.md](ARCHITECTURE.md)               | Where does each piece live, and how does a turn run?                  |
| 4     | [ROUTING.md](ROUTING.md)                         | Which folder answers a message, and how does the reply get back?      |
| 5     | [SECURITY.md](SECURITY.md)                       | What is isolated from what, and by which mechanism?                   |
| 6     | [GRANTS.md](GRANTS.md)                           | Who may do what, and where is that decided?                           |
| 7     | [EXTENDING.md](EXTENDING.md)                     | How do I add a channel, a connector, a package or a skill?            |
| 8     | [CHANGELOG.md](CHANGELOG.md)                     | What shipped, and when?                                               |
|       | [web docs](template/web/pub/arizuko/)            | How do I run and operate it, page by page?                            |
|       | [`cmd/arizuko/README.md`](cmd/arizuko/README.md) | What does each `arizuko` subcommand do?                               |
|       | [FEATURES.md](FEATURES.md)                       | What does it do today, and which test proves it?                      |
|       | [`tests/README.md`](tests/README.md)             | How do I run the tests, and what does each suite need?                |
|       | [`anteval/`](anteval/README.md)                  | What can an agent do on a throwaway instance, graded on real tasks?   |
|       | [`specs/index.md`](specs/index.md)               | Where do design directions live, and how do I list the retired specs? |
|       | [`BUGS.md`](BUGS.md)                             | What is open, and what waits on a person?                             |
|       | [LICENSE](LICENSE)                               | MIT.                                                                  |

## Thanks

| Project                                                  | Author        | Contribution                                        |
| -------------------------------------------------------- | ------------- | --------------------------------------------------- |
| [nanoclaw](https://github.com/nanocoai/nanoclaw)         | nanocoai      | Container-per-session model                         |
| [kanipi](https://github.com/kronael/kanipi)              | kronael       | TS proof-of-concept; routing, MCP IPC, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS       | character.json persona model                        |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic     | The agent runtime                                   |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face  | Code-as-action framing                              |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis   | QEMU micro-VM isolation, 3-tier chronicle memory    |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research | Self-improving skill learning across sessions       |
| [takopi](https://github.com/banteg/takopi)               | banteg        | Telegram dispatch, progress streaming               |
