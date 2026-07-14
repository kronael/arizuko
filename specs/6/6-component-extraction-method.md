---
status: draft
---

# What makes a component — the extraction method

`6/5` catalogues candidate components by import coupling. That is a
necessary filter, not the answer: **import-purity says what _compiles_
alone, not what _is_ a component.** crackbox is the counterexample — 0
internal deps AND its core promise (default-deny egress) is broken today
(fail-open, `BUGS.md` 2026-07-14). Deciding what each piece really is,
and extracting it cleanly, is per-component design work. This spec is the
method; it deliberately holds **no answers**.

## The three questions

For each candidate, before calling it a component:

1. **What is its contract / boundary?** The one promise it makes to a
   caller who knows nothing about arizuko. If you can't state it in one
   sentence, it isn't a component yet — it's a pile of code that happens
   to compile.
2. **Does the promise hold?** Test it adversarially. A component whose
   guarantee fails is not "ready with 0 deps" — it's broken. This is
   where real "readiness" lives, and it is the question the import-count
   cannot answer.
3. **What does a stranger need, and what is arizuko-shaped cruft?** The
   minimal surface an external consumer imports or runs, minus every
   assumption that only makes sense inside arizuko (folder ids, the gated
   socket, a specific daemon's resolve path). That delta is the
   extraction work.

Answering all three **is** the extraction: design + hardening +
adversarial test, not a grep of the import graph.

## Method — go deep on one, learn the shape

Do not spread thin across the `6/5` list. Take ONE candidate, answer the
three questions to completion, ship it, and let that pass teach the
method for the rest. Pick by: (a) we want it, (b) its promise is
testable, (c) failing that test is instructive.

### First case — crackbox

- **Q1 — contract:** default-deny egress per source id; a host not on
  that source's allowlist is refused at CONNECT (403 on every path).
- **Q2 — holds?** **No.** `libtest` (pypi-only allowlist) reached
  `example.com`; routd swallows the allowlist-resolve error → nil list →
  fail-open (`BUGS.md` 2026-07-14, HIGH). The fix is the real work:
  fail-CLOSED on an empty per-id policy, surface (don't swallow) the
  resolve error, add a containment test (tight allowlist → 403 on a
  non-listed host).
- **Q3 — stranger vs cruft:** the proxy + per-source allowlist + DNS
  filter is the stranger's surface; routd's resolve path and folder-id
  plumbing are arizuko-shaped. A clean crackbox is the former with a
  documented policy input.

crackbox is first because a security tool that fails open is worth fixing
regardless of any toolbox story.

## Not answers

This spec is a method and a work-queue, not a verdict. `6/5`'s readiness
column is a set of hypotheses to be run through the three questions, one
piece at a time. Expect the list to change as each is actually examined —
that changing is the point, not a failure.

## Ties

`6/5` (candidate catalogue) · `6/8` (crackbox) · `BUGS.md` (the
fail-open finding) · `CLAUDE.md` (import-graph rule — the necessary
filter, not the sufficient one).
