---
status: draft
---

# Replaceability research

> Before the next homegrown component, prove the off-the-shelf alternatives
> wouldn't have worked.

## Why

crackbox was built in two days and shipped to production. The features are
small, but each one is a maintenance line forever. A clean piece of
off-the-shelf software plus a sentence of glue would be cheaper.

The bar is **not** "is there any alternative". It is: _would any of these
have shipped the same outcome in the same time, with less code we own?_

Bias toward replacement. Code we don't write doesn't break, doesn't need
updates, doesn't sit in our brain.

## Process

For each shippable component:

1. Pick the top three candidates.
2. Write a one-page evaluation: feature matrix, LOC-saved estimate, ops-cost
   delta, and what we'd lose.
3. If a candidate clearly wins, file a migration spec. If none does, record
   the verdict and **stop second-guessing** — that record is half the value.

**Acceptance: an audit written by someone who actually ran the alternative
for at least an afternoon.** "I read the docs and they look fine" does not
count, and is exactly how a component gets rebuilt anyway.

## Components, and the honest starting point

- **egred** (per-source-IP egress allowlist, shipped under
  [6/9-crackbox-sandboxing](../6/9-crackbox-sandboxing.md)). squid does
  per-source ACL + CONNECT tunneling out of the box and reloads ACLs via an
  `external_acl` helper; mitmproxy is scriptable and would matter if we ever
  want credential injection ([8/Z-egred-mitm](../5/13-ext-mcp.md)); envoy
  is industrial but a large config surface for one allowlist. Cilium/Calico
  are Kubernetes-only and off our path.
- **mcp-firewall** (specced, not built —
  [6/12-mcp-firewall](../6/12-mcp-firewall.md)). **The most likely to be
  replaceable outright**: the Claude Agent SDK already has a `canUseTool`
  callback, which is zero code. The only real question is whether its policy
  expressivity matches. Do not ship our own without checking this first.
- **Message routing** (routd's inbound persist + per-folder dispatch). NATS
  JetStream would replace SQLite-as-queue with a real queue and keep the
  ordered, single-consumer-per-folder semantics. But the parts that are
  genuinely ours — per-folder container spawn, MCP socket wiring, agent
  output capture — stay arizuko-specific whatever carries the messages.
- **Container orchestration** (runed's per-turn spawn). Nomad and Kubernetes
  jobs win on GC and scheduling but are heavy; `systemd-run` transient units
  are already on the host; firecracker/kata isolate harder and cost more to
  operate. Hardest to replace, because the lifecycle is woven into the turn
  loop.

## Out of scope

- The daemons that _are_ arizuko's domain — routing rules, the grants
  engine, onboarding. Those are the value; they stay.
- Cloud-managed alternatives. The deployment model is a single-host VPS.
- Anything requiring a Kubernetes cluster.
