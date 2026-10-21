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
turn ends, the guest powers off and the writable layer is gone. A folder runs one guest at a
time, and a message that arrives during a turn goes into the running guest.

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

## Getting started

You need a Linux x86_64 host with `/dev/kvm`, Docker with Compose v2.20 or
newer, Go, write access to `/srv/data/`, and a token for one channel.

<!-- twin:install -->

```bash
git clone https://github.com/kronael/arizuko && cd arizuko
make build                                        # ./arizuko + daemon binaries
sudo make images DATA_DIR=/srv/data/arizuko_demo  # daemon images + the guest rootfs
sudo make kernel DATA_DIR=/srv/data/arizuko_demo  # the guest kernel, built once
./arizuko create demo                             # /srv/data/arizuko_demo + .env
./arizuko packages demo add extenddb              # the store server, two passwords
./arizuko generate demo
cd /srv/data/arizuko_demo
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb_db
sudo docker compose run --rm extenddb init \
  --pg-host extenddb_db --pg-port 5432 \
  --pg-user arizuko_demo --pg-pass "$STORE_DB_PASSWORD" \
  --extenddb-user arizuko_demo_app --extenddb-pass "$STORE_APP_PASSWORD" \
  --bind-addr 0.0.0.0 --tls-san extenddb \
  --config /var/lib/extenddb/extenddb.toml
vim .env    # STORE_ADMIN_PASSWORD and STORE_ACCOUNT_ID, which init prints once;
            # ASSISTANT_NAME, a model key, a bot token, RUNED_KERNEL_DIR, RUNED_ROOTFS_DIR
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_demo \
  extenddb settings -c /var/lib/extenddb/extenddb.toml set control_plane_delay_seconds 0
echo "STORE_CA=$(sudo base64 -w0 store/extenddb/.extenddb/tls/cert.pem)" | sudo tee -a .env
echo 'DYNAMODB_URL=https://extenddb:18443' | sudo tee -a .env
# one store key per daemon: INSTALL.md § The per-daemon store keys
cd -
./arizuko packages demo add teled                 # one channel adapter
./arizuko group demo add telegram:group/123456789 main
./arizuko run demo                                # generate compose, docker compose up
```

<!-- /twin -->

[INSTALL.md](INSTALL.md) has the full walk: prerequisites, the store server
and its access keys, adapter tokens, backup, and the checklist for an agent
that does not reply. Mint the access key of a daemon before you enable its
package. A daemon that reaches the store with no key exits, and the whole
instance restarts in a loop. [EXTENDING.md](EXTENDING.md) covers packages,
products and adapters.

## How it works

Three daemons form the core. `routd` appends every message and decides which
folder answers. `runed` boots one KVM guest per turn. `authd` signs every
token. Adapters, the web chat and the scheduler call `routd` over HTTP. Each
daemon keeps its own tables on one store server and signs in with its own key.
[ARCHITECTURE.md](ARCHITECTURE.md#where-a-change-lands) draws the repository
map, the table owners and the turn from start to end.

Each chat platform has its own adapter daemon.
[EXTENDING.md](EXTENDING.md#verb-support-matrix) lists the adapters and the
verbs each one supports. [components/channels.html](template/web/pub/arizuko/components/channels.html)
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
lives for one turn filters the egress of the guest. Only model credentials
enter the guest. One function reads the `acl` table and decides every action,
and a deny row wins. Only `proxyd` stamps identity headers. [SECURITY.md](SECURITY.md)
holds the threat model and the boundary table.

## The store is the source of truth

- Each stored resource, such as routes or tasks, has one handler. It serves a
  REST interface for people and an MCP interface for agents, behind one
  authorization gate. [ARCHITECTURE.md](ARCHITECTURE.md#cold-tier-vs-hot-tier)
  lists the exceptions.
- The store holds the live state. `arizuko export` writes it to a YAML
  manifest, and `arizuko apply` reads a manifest back. A manifest is a copy,
  not a second source.
- Designed, not built: the core daemons of one world in one process.
- Planned, not started: end-user agent provisioning, where a POST of a
  definition returns a tenant and a chat token, and the rename of `group` and
  `folder` to `path`.

## Build and test

[INSTALL.md](INSTALL.md) lists the build, image and smoke targets.
[tests/README.md](tests/README.md) says how to run the tests and what each
suite needs. [`anteval/`](anteval/README.md) checks what an agent can do. It
runs real tasks through the public REST, HTTP and MCP interfaces of a live
instance and grades the visible effects.

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
|       | [`specs/`](specs/)                               | Which change is in flight, and why?                                   |
|       | [`BUGS.md`](BUGS.md)                             | What is open, and what waits on a person?                             |
|       | [LICENSE](LICENSE)                               | MIT.                                                                  |

## Thanks

| Project                                                  | Author        | Contribution                                        |
| -------------------------------------------------------- | ------------- | --------------------------------------------------- |
| [nanoclaw](https://github.com/qwibitai/nanoclaw)         | qwibitai      | Container-per-session model                         |
| [kanipi](https://github.com/kronael/kanipi)              | kronael       | TS proof-of-concept; routing, MCP IPC, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS       | character.json persona model                        |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic     | The agent runtime                                   |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face  | Code-as-action framing                              |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis   | QEMU micro-VM isolation, 3-tier chronicle memory    |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research | Self-improving skill learning across sessions       |
| [takopi](https://github.com/banteg/takopi)               | banteg        | Telegram dispatch, progress streaming               |
