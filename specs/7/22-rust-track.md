---
status: draft
depends: [../6/37-store-model, ../6/42-dynamo-over-postgres, 25-publish-plan]
---

# specs/7/22 — the Rust track

Phase 7 is the cloud track. This spec answers the second question put to
it: what, if anything, is rewritten in Rust, and how that relates to
getting arizuko into the cloud.

The finding is that the two are orthogonal, and that treating them as one
programme is the mistake this document exists to prevent.

## Measured, 2026-09-05 (at `02d0fbc1`)

Non-test Go, excluding `third_party/`, `refs/` and the agent worktrees
under `.claude/`:

| Scope              | Lines  |
| ------------------ | ------ |
| arizuko total      | 84,909 |
| `ipc`              | 3,012  |
| `container`        | 2,973  |
| `egress`           | 667    |
| — of which `match` | 129    |
| `crackbox`         | 414    |
| `mcpfw`            | 370    |

The three isolation-boundary siblings — `egress`, `crackbox`, `mcpfw` —
are **1,451 lines together**, 1.7% of the tree. `egress/match` (129
lines) is imported by `store/network.go` and
`routd/network_rules_resource.go`: it is core's rule grammar, filed under
`egress/`, so the Rust unit in `egress` is the proxy — `address.go`,
`proxy.go`, `cmd/` — 538 lines, not the directory.

## Rust buys nothing for the cloud

`6/37` established that the cloud constraint is relational, not
linguistic: 26 of 47 transactions are multi-table, and each one prevents
a specific privilege escalation, so no key-value or document store
carries them. `6/42` settles what that resolves to: the seam is
`objstore.Store` — two methods and four protocols — and arizuko speaks the
DynamoDB API to a server that stores in PostgreSQL. Every daemon changes by
zero lines at that seam. The constraint is identical in any language: a
daemon rewritten in Rust still needs the same transaction to stop a group
delete conferring inherited admin.

So the cloud track proceeds through the store move whatever happens
here, and a Rust rewrite would not advance it by a line.

## Where Rust plausibly pays

The isolation boundary, and only there. `egress` is a forward proxy on
every agent's network path; `crackbox` is the sandbox; `mcpfw` is the
tool-call gate. All three are small, reach no store, hold no
schema, ship as single static binaries, and sit exactly where a memory
bug is a tenancy breach rather than a crash.

Two of the three are extractable by construction — `6/16`'s
ship-in-parts requirement forces string-typed public surfaces with
nothing grant-shaped, and `tests/components/imports_test.go` pins the
import graph — so each has a contract narrow enough to reimplement
against its own acceptance tests rather than against a reading of its
source. The third, `egress`, is extractable as a process (runed holds it
to three flags and a `/health` probe, `container/egress.go:252-286`) but
not as a directory: `match` stays in Go as long as `store` and `routd`
import it.

That is 1,451 lines with a testable contract, not a rewrite.

## The package contract makes the language invisible

`7/25` settles that every component is a package: a process plus a
declaration of how it hooks into the system. A process has no language
on the wire. So a Rust package needs no permission from this spec, and
this spec's rule bounds one thing only — **what this repo rewrites** of
the Go it already ships. The boundary does not move: the isolation
boundary is still the only place where a memory error is a tenancy
breach, and a package that sits anywhere else gains nothing from Rust it
did not have in Go.

The contract also names a cost the rule below hides. `egress` hooks in
as a process. `mcpfw` hooks into nothing (`6/12`), so
a rewrite of it has no consumer to break and none to serve; its
stub-driven acceptance tests are its only oracle, which is what rule 4
asks for and also the reason to leave it last.

## Where it does not

The remaining ~82,000 lines are store-backed business logic: routing,
grants, onboarding, scheduling, the dashboard. Go is already the right
tool for them, the transactions are the correctness argument rather than
the performance one, and no user-visible property improves by moving
them.

## The rule

A component is a Rust candidate only when ALL of these hold:

1. It sits on a trust boundary where a memory error is a security failure.
2. It owns no schema and opens no database.
3. Its public surface is string-typed and already extractable per `6/16`.
4. It has acceptance tests that pass against a stub, with no arizuko
   process running — so a reimplementation is verified against behaviour,
   not against the original's internals.

`crackbox` and `mcpfw` satisfy all four today; `egress` satisfies them
for the proxy and fails rule 3 for `match`. Nothing else in the tree
satisfies any of them.

## Sequencing

Independent of the cloud track, and lower priority than it. A rewrite
that begins before a component's acceptance tests exist has no oracle,
and the tests are the deliverable that makes the rewrite verifiable —
write them first, in Go, against the Go implementation.

## Ties

`6/37` (the actual cloud constraint) · `25-publish-plan` (the
package contract; language is invisible to it) · `6/16` daemon standalone
matrix (what makes these three extractable) · `6/12` mcpfw · `6/24`
egress isolation.
