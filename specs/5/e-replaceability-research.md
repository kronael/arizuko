---
status: research
---

# Replaceability research

> Before the next homegrown component, prove the off-the-shelf
> alternatives wouldn't have worked.

## Why

We built crackbox in two days and shipped it to production. The
features are small, but each one is a maintenance line forever. A
clean piece of off-the-shelf software with a sentence of glue would
be cheaper. This spec is the audit: for every shippable arizuko
component, list the existing tools that solve the same problem, what
the friction is, and whether the homegrown version pays its rent.

The bar isn't "is there _any_ alternative." It's "would any of these
have shipped the same outcome in the same time, with less code we
own?"

## Components to research

### crackbox — forward + transparent proxy with per-source allowlist

What it does: receives `HTTPS_PROXY` traffic from agent containers,
matches the destination host against a per-source-IP allowlist,
splices the connection through. Default-deny.

Alternatives to evaluate (with concrete checklists):

- **squid** with `acl` rules. ACL syntax is messy but it does
  per-source allowlist + CONNECT tunneling out of the box. Has
  half-close handling we don't, has logs we don't, has caching we
  don't want. Open question: per-IP dynamic ACL reload without
  restart — squid has it via `external_acl`. Cost: write the
  external_acl helper, ~50 LOC bash or Go.
- **mitmproxy** in `--mode transparent` or `--mode regular`. Has SNI
  inspection, request inspection, scriptable via Python. Heavier
  than crackbox; designed for inspection more than gating. Worth it
  if we ever want spec 6/11 (placeholder injection). Cost: a
  ~30-line Python addon, no Go code.
- **envoy** with an HTTP filter chain. Industrial-strength. Big
  config surface. Probably overkill for a per-folder allowlist
  but if we end up wanting metrics, retries, circuit-breaking, it's
  there.
- **Cilium** / **Calico** NetworkPolicy. Kubernetes-only. Not on
  our deployment path today.
- **Custom iptables** + `--owner` rules per container. Doesn't
  scale; folder-walk allowlist resolution would still be ours.
- **OpenSnitch** / **dnsmasq + ipset**. Workstation tools, not
  daemonized.

Verdict template: write down for each option what we'd lose
vs gain, what the LOC budget looks like, where the cliff is.

### messaging-gateway (future, extracted from gated)

What it does: accept inbound from N channel adapters (HTTP), persist
to SQLite, dispatch to per-folder runners.

Alternatives:

- **NATS** with subjects per channel. JetStream gives durability.
  Native Go client. Probably a 3-line publish + subscribe instead
  of `gated`'s poll loop. We'd replace SQLite-as-queue with a
  proper queue. Tradeoff: gated's poll-loop semantics
  (ordered, single-consumer per folder) are easy in NATS too.
- **RabbitMQ** / **Redis Streams**. Same shape, more ops.
- **Temporal**. Workflow primitives we don't need yet.
- **n8n** / **Zapier-clones**. UI-driven; doesn't fit code-only.
- **Bento** / **Benthos** / **Vector**. Stream processors that could
  replace gated's normalize-and-route step.

Hard parts gated does that aren't off-the-shelf: per-folder
container spawn, MCP socket wiring, agent-output capture. Those
stay arizuko-specific even if the routing is replaced.

### mcp-firewall (future)

What it does (specced, not built): intercept JSON-RPC between agent
and MCP servers, gate by tool name + arg patterns.

Alternatives:

- **Anthropic's permission system** in claude-agent-sdk. Already
  has a `canUseTool` callback. Cost: zero code; it's already in
  the SDK. Question is whether the policy expressivity matches.
- **mcp-proxy** / **mcp-bridge** projects on GitHub. Some exist.
  Audit the licenses + activity; pick one if it covers the cases.

This is the spec most likely to be replaceable. Don't ship our own
without checking.

### gated container orchestration

What it does: spawns one Docker container per agent invocation,
sets env, mounts, network, captures output.

Alternatives:

- **Nomad** / **Kubernetes jobs**. Heavy. Wins reliability + GC +
  scheduling we don't have.
- **systemd-run** with transient units. Already on the host. Could
  replace the Docker dependency for non-isolated workloads.
- **firecracker** / **kata-containers**. Stronger isolation than
  Docker. Expensive to operate.

Probably the hardest piece to replace because gated's lifecycle is
tightly woven with arizuko's message poll loop.

## Process

For each component:

1. Pick the top 3 candidates from the lists above.
2. Build a one-page evaluation: features matrix, LOC-saved
   estimate, ops cost delta, what we'd lose.
3. If any candidate clearly wins, file a follow-up spec to migrate.
   If none does, document the verdict here and stop second-guessing.

Bias toward replacement. Code we don't write doesn't break, doesn't
need updates, doesn't sit in our brain.

## Out of scope

- Replacing the daemons that _are_ arizuko's domain (gateway routing
  rules, grants engine, onboarding flow). Those are the value; they
  stay.
- Cloud-managed alternatives (AWS WAF, Cloudflare Workers). The
  deployment model is single-host VPS today.
- Anything that requires a Kubernetes cluster.

## Acceptance

A short audit document per component, written by someone who
actually tried the alternative for at least an afternoon. "I read
the docs and they look fine" doesn't count.
