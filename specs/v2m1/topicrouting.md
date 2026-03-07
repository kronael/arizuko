# Topic routing and agent hierarchy

Not arizuko-specific. General pattern for multi-agent
message routing in chat systems.

## Events are messages

System events (join, leave, agent-done, task-fired,
session-start, session-end) are messages on the bus.
Same table, different origin. Pipelines react to them
like any other message.

## Routing symbols

Two routing prefixes that change message destination:

### @agent — routes to group

`@researcher` routes the message to the `researcher`
group. That group has its own agent, folder, config.
This is the existing group routing but with explicit
addressing.

- `@agent` is a leaf target — a specific agent identity
- If defined, the message goes to that group's container
- If not defined, falls through to default routing
- Each target can define a different agent (command,
  image, config, character)
- Products can redefine the agent mapping

### #topic — routes to session

`#deploy-review` routes to a session namespace within
the same group. Container destination and setup are
identical — but the session/context is separate.

- `#topic` creates or resumes a named session
- Same agent, same container config, different context
- Like namespaces — isolated conversation threads
- Agent sees only messages tagged with that topic
- Useful for parallel workstreams in one group

### Difference

|              | @agent                        | #topic                           |
| ------------ | ----------------------------- | -------------------------------- |
| Routes to    | different group/container     | same group, different session    |
| Agent config | can differ (image, character) | same                             |
| Folder       | different                     | same                             |
| Context      | separate                      | separate                         |
| Container    | separate                      | same (or separate, configurable) |

## Agents are commands

An agent is just a command that reads stdin and writes
stdout. The routing table maps names to commands:

```toml
# Routing config (per instance or per group)
[agents]
researcher = { image = "arizuko-agent", character = "researcher.json" }
writer     = { image = "arizuko-agent", character = "writer.json" }
coder      = { image = "arizuko-agent", mounts = ["/src:/workspace/src"] }
summarizer = { cmd = "python summarize.py" }  # not even docker
```

The gateway resolves `@researcher` → spawn container with
researcher config. The agent binary/image is configurable
per target. A product (instance) redefines the whole table.

## Agent hierarchy

Agents can create and manage subagents:

```
@manager receives "review the PR"
  → delegates to @coder "review code changes"
  → delegates to @writer "draft release notes"
  → collects results, responds
```

This is the existing delegate mechanism but with named
targets instead of folder paths. The routing table
defines what's available. Agents discover available
subagents via MCP tools (list_agents, delegate).

## Pipelines

A pipeline is a sequence of routing steps triggered
by events or messages:

```
event:join → @greeter → send welcome
event:message(keyword="deploy") → @deployer
@researcher:done → @writer → @reviewer
```

Pipelines are configurable per product/instance.
They react to messages (including system events)
and route to agents.

**Open**: how to define pipelines. Config file?
Agent-created? Both? How expressive — linear chains
only or DAGs?

## Session topics in detail

```
user: #deploy-review let's look at the PR
  → gateway creates/resumes session "deploy-review"
  → agent sees context from that session only

user: #backend-refactor what about the db layer
  → separate session, same agent, same group

user: (no prefix) general question
  → default session (main)
```

Topics are lightweight — no new container, no new group.
Just a session ID prefix that partitions conversation
context. The agent's CLAUDE.md, skills, character are
the same.

Agent can create topics:

```
agent: I'll track this in #auth-migration
  → creates session "auth-migration"
  → subsequent messages tagged #auth-migration route there
```

## Open questions

- How does the user list active topics? `/topics`?
- Can topics span groups? (probably not — that's @agent)
- Topic lifecycle — auto-close after idle? Manual close?
- Can a topic have a different character/system prompt
  even though it shares the group config?
- Pipeline definition format — TOML? YAML? Agent-created
  via MCP tool?
- How do products override the default routing table?
- Security: can any user route to any agent, or is there
  ACL per @agent and #topic?
