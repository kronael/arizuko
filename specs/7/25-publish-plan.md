---
status: draft
depends: [24-skill-search]
---

# specs/7/25 — publishing the parts

`6/16` asks which pieces stand alone. The import graph already answers
it, so this spec is a plan rather than an investigation.

## The test, and what passes it

A component is publishable when it imports nothing arizuko-internal.
That is not a preference; it is the whole of what "standalone" means,
and `go list` decides it. Measured 2026-09-05, non-test lines:

| Component     | Lines | arizuko imports     |
| ------------- | ----- | ------------------- |
| `mcpfw`       | 370   | none                |
| `obs`         | 547   | none                |
| `egress`      | 682   | none                |
| `groupfolder` | 212   | none                |
| `crackbox`    | 414   | `egress`, `sandbox` |
| `chanlib`     | 2,508 | seven packages      |
| `resreg`      | 5,591 | five packages       |

Four pass today. `crackbox` passes as a set with its two siblings.
`chanlib` and `resreg` do not, and no amount of packaging changes that —
they would have to be cut first, which is a different spec.

## Order, and why

**1. `mcpfw`.** The only one that is already a product: a binary, a
ruleset, a README, acceptance tests that run against a stub with no
arizuko process. It answers a question people outside this project
have — how to gate an agent's MCP tool calls — and nothing else in the
market does exactly that. Publish it first because it needs no work.

**2. `egress`.** A forward proxy that resolves a destination and checks
the resulting address before dialling it, so a name cannot resolve past
the gate. Small, sharp, and the check is the interesting part rather
than the proxying. It ships with `crackbox` as the local-development
front end, which is what `crackbox` already is.

**3. `obs`.** Useful and unremarkable: slog fan-out to OTLP with a
deterministic trace id per turn. Publish only if someone asks; it wins
no attention on its own.

`groupfolder` is 212 lines of path containment. It is a file, not a
project. Fold it into whatever ships beside it.

## What publishing actually costs

Each published component gains a public surface that can no longer
change silently. That is the real price, not the packaging work. So
publish only what has an acceptance test that runs with no arizuko
process — `mcpfw` and `egress` do; that test IS the contract.

The single `go.mod` stays. `6/16`'s rule already enforces orthogonality
through the import graph, and a module split would trade one enforced
rule for four repositories to keep in step.

## Non-goals

- Publishing `chanlib` or `resreg`. Both are entangled by measurement.
- A monorepo split. The import rule is the enforcement.
- Publishing anything without a stub-driven acceptance test.

## Ties

`6/16` (the standalone matrix) · `6/12` (mcpfw) · `6/24` (egress
isolation) · `7/22` (the same three components, judged for Rust).
