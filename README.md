# arizuko

arizuko runs many AI agents on one Linux server you own. Each agent is a
folder of plain files: who it is, what it remembers, what it can do, and which
chats reach it. You change an agent by talking to it, and you read the result
as a diff on disk. Small daemons around the folders decide which chats reach
which agent and what each agent may do beyond its own folder.

[Install](INSTALL.md) • [How it works](ARCHITECTURE.md) •
[Security](SECURITY.md) • [Routing](ROUTING.md) • [Extending](EXTENDING.md) •
[Web docs](template/web/pub/arizuko/) (served at `/pub/arizuko/` on every
instance)

Self-hosted only, on one Linux host with Docker and KVM. Sized for agent teams,
not consumer traffic.

## Glossary

- **folder**: one agent: its persona, skills, memory, diary and grants.
- **group**: a folder that routd knows; `arizuko group` manages it.
- **world**: a top-level folder, such as `corp` in `corp/eng/sre`.
- **JID**: a chat's address, such as `telegram:group/123456789`.
- **route**: a row that sends a chat's messages to one folder.
- **topic**: a thread inside a chat; each topic keeps its own session.
- **turn**: one agent run: a KVM guest boots, answers, powers off.
- **grant**: an `acl` row that allows one action on one scope.
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

## How a message becomes a reply

Each folder keeps only the conversations routed to it. Agents coordinate on
the same conversation plane they serve people on: a turn can route to a sibling, delegate to a child, schedule a task or publish a web
page by posting to `routd`. There is no second coordination bus.

Every event runs the same six steps:

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

Channels, tasks, webhooks, secrets, delegation and workflows are all
compositions of these six, with identity (folders, JIDs, roles, scopes) as the
coordinate system they are addressed in.

| Layer      | What it is           | Examples                                                          |
| ---------- | -------------------- | ----------------------------------------------------------------- |
| Primitives | the concepts         | Event, Routing, Agent, Authorization, Turn, State                 |
| Components | Go packages          | `store`, `router`, `auth`, `ipc`, `runed`, `groupfolder`          |
| Daemons    | deployable processes | `authd` + `routd` + `runed`; `webd`, `timed`, `onbod`, `slakd`, … |
| Products   | installable agents   | Slack team agent, reality agent, company brain                    |

A product is a folder's contents. The same pipeline runs every product.

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

You need a Linux x86_64 host with `/dev/kvm`, Docker with the compose plugin,
Go, write access to `/srv/data/`, and a token for one channel.

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
and its per-daemon access keys, adapter tokens, backup, and the "healthchecks
green but no reply" checklist. A daemon that reaches the store with no access
key restart-loops the whole instance, so mint its key before you enable a
package. Packages, products and adapters: [EXTENDING.md](EXTENDING.md).

## How it works

Three daemons form the core: `routd` appends every message and decides which
folder answers, `runed` boots one KVM guest per turn, and `authd` signs every
token. Adapters, the web chat and the scheduler call `routd` over HTTP, and
each daemon keeps its own tables on one store server under its own key. The
repository map, table ownership and the turn end to end are drawn in
[ARCHITECTURE.md](ARCHITECTURE.md#where-a-change-lands). Each chat platform has
its own adapter daemon; the adapters and the verbs each supports are in
[EXTENDING.md § Verb support matrix](EXTENDING.md#verb-support-matrix), and the
protocol they speak in
[components/channels.html](template/web/pub/arizuko/components/channels.html).

## What arizuko does not include

- connector crawlers for Confluence, Notion, Google Drive, Jira or Salesforce
- an embedding pipeline or a vector database
- permission sync from external systems into per-document ACLs
- multi-model routing across providers; the agent runtime is Claude Code
- a managed control plane or a hosted service

You can mount or write documents into a group's folder, and the agent reads
them directly. For large-scale retrieval, pair arizuko with a retrieval stack
and let it do what it does: receive messages, keep per-group memory, enforce
grants, schedule tasks, act across channels.

## Security model

Each turn runs in its own KVM guest on its own tap, a per-turn proxy filters
its egress, and only model credentials ever enter the guest. One `acl` table
read by one function decides every action, deny wins, and `proxyd` alone
stamps identity headers. Threat model and boundary table:
[SECURITY.md](SECURITY.md).

## Direction

- Every cold-tier resource has one handler with a REST face for people and an
  MCP face for agents, behind one auth gate; the exceptions are listed in
  ARCHITECTURE.md § Cold tier vs hot tier.
- The store is authoritative. YAML manifests dump and import the cold tier
  (`arizuko export`, `arizuko apply`); they are not a synced source.
- Designed, not built: the core daemons of one world in one process.
- Planned, not started: end-user agent provisioning (POST a definition, get a
  tenant and a chat token) and renaming `group`/`folder` to `path`.

## Build and test

Build, image and smoke targets: [INSTALL.md](INSTALL.md). How to run the tests
and what each suite needs: [tests/README.md](tests/README.md).
[`anteval/`](anteval/README.md) is the agent-capability gate: it runs real
tasks through the public REST, HTTP and MCP surfaces of a live instance and
grades the observable effects.

## How to read this

| Order | File                                                                                | The question it answers                                               |
| ----- | ----------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| 1     | this file                                                                           | What is it, and how do I start?                                       |
| 2     | [INSTALL.md](INSTALL.md)                                                            | How do I get an instance running, and what do I do when it is silent? |
| 3     | [ARCHITECTURE.md](ARCHITECTURE.md)                                                  | Where does each piece live, and how does a turn run?                  |
| 4     | [ROUTING.md](ROUTING.md)                                                            | Which folder answers a message, and how does the reply get back?      |
| 5     | [SECURITY.md](SECURITY.md)                                                          | What is isolated from what, and by which mechanism?                   |
| 6     | [GRANTS.md](GRANTS.md)                                                              | Who may do what, and where is that decided?                           |
| 7     | [EXTENDING.md](EXTENDING.md)                                                        | How do I add a channel, a connector, a package or a skill?            |
| 8     | [CHANGELOG.md](CHANGELOG.md)                                                        | What shipped, and when?                                               |
|       | [web docs](template/web/pub/arizuko/), live at `https://krons.fiu.wtf/pub/arizuko/` | How do I run and operate it, page by page?                            |
|       | [`cmd/arizuko/README.md`](cmd/arizuko/README.md)                                    | What does each `arizuko` subcommand do?                               |
|       | [`tests/README.md`](tests/README.md)                                                | How do I run the tests, and what does each suite need?                |
|       | [`specs/`](specs/)                                                                  | Which change is in flight, and why?                                   |
|       | [`BUGS.md`](BUGS.md)                                                                | What is open, and what waits on a person?                             |
|       | [LICENSE](LICENSE)                                                                  | MIT.                                                                  |

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
