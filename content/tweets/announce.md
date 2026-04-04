# Announce Tweets — arizuko

Feature announcements as they ship. One tweet per feature, posted
when the code lands. Same voice as product tweets: name the problem,
show the solution.

---

## Format

```
[feature name]

Problem: [what was missing or broken]
Now: [what shipped]
[optional: one concrete example]
```

---

## Shipped

### 2026-04-04 — Provider independence

Claude just limited API access for subscribers. If your
entire agent system is one SDK call to one provider, you're
down until they decide otherwise.

arizuko is built orthogonally. Each component does one thing
behind a contract: HTTP for channels, MCP for tools, SQLite
for state, markdown for skills. The agent runtime is Claude
Code today — but it's a container that reads JSON from stdin
and writes to stdout. Swap it for anything.

Channel adapters don't know about the agent. The agent
doesn't know about channels. Memory is files on disk. Skills
are markdown. Routing is a database table.

Nothing assumes permanence from any single provider. If one
piece gets limited, banned, or deprecated — you replace that
piece. Not the system.

Build infrastructure that outlasts the services it depends on.
