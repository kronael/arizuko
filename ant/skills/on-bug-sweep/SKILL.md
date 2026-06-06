---
name: on-bug-sweep
description: Auto-trigger reflex, NOT user-invoked. On finding/fixing ANY bug, sweep the codebase for siblings of the same bug-class AND trace adjacent flows for the same gap, before moving on. NOT for a planned multi-file audit/refactor (use sweep-fix-verify).
when_to_use: The instant any bug surfaces — 401/403/404/500, panic, nil deref, "converting NULL", failing test, crash-loop, silent failure, missing grant/scope/credential/env/wiring, wrong default, "found a bug", "fixed X", "is this elsewhere", "similar bugs", "verify everything". Especially after a refactor/split/migration where one daemon or path lost a capability another still has.
---

A bug is one instance of a class. The cheap minutes spent finding its siblings
now beat the production incident later. ALWAYS run both moves before moving on;
NEVER fix-and-forget a bug as if it were unique.

## 1. Sweep the class
- ALWAYS name the bug's class in one sentence, then grep every site that fits it.
- Classes seen: "a daemon needs a grant/credential/env the refactor never wired"
  → check every daemon/principal; "a nullable column breaks this scan" → check
  every scan of a nullable column; "this caller lacks the scope its endpoint
  requires" → check every caller of every gated endpoint.
- ALWAYS fix each sibling or log it to bugs.md — NEVER leave a known sibling silent.

## 2. Trace the flows
- The bug lives in a flow (a turn, an onboarding, a creation, an auth handshake).
- ALWAYS trace the ADJACENT flows for the same gap: how new things get created,
  come alive, and self-configure. A bug in the agent-turn path predicts bugs in
  the onboarding-greeting and first-spawn paths.

## Tests lie by omission
- If every test passed but the bug still reached production, the tests hand-wired
  around the gap. ALWAYS strengthen the test to exercise the REAL path (real token
  mint, real adapter POST) — NEVER trust a green suite that fabricates the thing
  production actually lacks.

## Why this earned a skill (real failure)
The split cutover's runed token-downscope 403 (a missing `serviceGrants` entry)
was one instance of "split daemon needs a capability the monolith had but the
split never wired." Sweeping the class at once found two MORE production-breakers
— adapters 401 on inbound, onbod 401 on the onboarding greeting — that the tests
missed because they fabricated the exact tokens production lacked. One bug, three
breakers.
