---
status: draft
depends: [21-storage-seam]
---

# specs/7/22 — the Rust track

Phase 7 is the cloud track. This spec answers the second question put to
it: what, if anything, is rewritten in Rust, and how that relates to
getting arizuko into the cloud.

The finding is that the two are orthogonal, and that treating them as one
programme is the mistake this document exists to prevent.

## Measured, 2026-09-05

Go, excluding tests, vendored `third_party/` and the `refs/` reading
material:

| Scope         | Lines  |
| ------------- | ------ |
| arizuko total | 83,587 |
| `ipc`         | 3,012  |
| `container`   | 2,898  |
| `egress`      | 682    |
| `crackbox`    | 414    |
| `mcpfw`       | 370    |

The three isolation-boundary siblings — `egress`, `crackbox`, `mcpfw` —
are **1,466 lines together**, 1.8% of the tree.

## Rust buys nothing for the cloud

`7/21` established the cloud blocker and it is relational, not
linguistic: ~27 of 43 transactions are multi-table, and each one prevents
a specific privilege escalation, so the storage seam cannot be a
key-value interface. That constraint is identical in any language. A
daemon rewritten in Rust still needs the same transaction to stop a group
delete conferring inherited admin.

So the cloud track proceeds through the storage seam whatever happens
here, and a Rust rewrite would not advance it by a line.

## Where Rust plausibly pays

The isolation boundary, and only there. `egress` is a forward proxy on
every agent's network path; `crackbox` is the sandbox; `mcpfw` is the
tool-call gate. All three are small, have no SQLite dependency, hold no
schema, ship as single static binaries, and sit exactly where a memory
bug is a tenancy breach rather than a crash.

They are also already extractable by construction — `6/16`'s ship-in-parts
requirement forces string-typed public surfaces with nothing
grant-shaped, so each has a contract narrow enough to reimplement against
its own acceptance tests rather than against a reading of its source.

That is 1,466 lines with a testable contract, not a rewrite.

## Where it does not

The remaining ~82,000 lines are SQLite-backed business logic: routing,
grants, onboarding, scheduling, the dashboard. Go is already the right
tool for them, the transactions are the correctness argument rather than
the performance one, and no user-visible property improves by moving
them. `## Non-goals` below states this as a rule rather than a
preference.

## The rule

A component is a Rust candidate only when ALL of these hold:

1. It sits on a trust boundary where a memory error is a security failure.
2. It owns no schema and opens no database.
3. Its public surface is string-typed and already extractable per `6/16`.
4. It has acceptance tests that pass against a stub, with no arizuko
   process running — so a reimplementation is verified against behaviour,
   not against the original's internals.

`egress`, `crackbox` and `mcpfw` satisfy all four today. Nothing else in
the tree does.

## Sequencing

Independent of the cloud track, and lower priority than it. A rewrite
that begins before a component's acceptance tests exist has no oracle,
and the tests are the deliverable that makes the rewrite verifiable —
write them first, in Go, against the Go implementation.

## Non-goals

- Rewriting any daemon that owns a schema.
- A staged port that leaves one component half in each language.
- Rust for performance. No measurement identifies any of these three as a
  bottleneck, and this spec does not assert one.

## Honest gaps

- No measurement shows a memory-safety defect in the Go implementations.
  The argument is that the boundary is where such a defect would be
  costly, not that one exists.
- A second toolchain costs build time, CI, and the ability of one
  maintainer to work anywhere in the tree. For 1.8% of the lines that
  cost is real and is not quantified here.

## Ties

`21-storage-seam` (the actual cloud blocker) · `6/16` daemon standalone
matrix (what makes these three extractable) · `6/12` mcpfw · `6/24`
egress isolation.
