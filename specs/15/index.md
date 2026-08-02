---
status: planned
---

# specs/15 — later

Committed in direction, not scheduled. Each needs a dedicated sprint.

| Spec                                                         | Status     | Description                                                                              |
| ------------------------------------------------------------ | ---------- | ---------------------------------------------------------------------------------------- |
| [e-extend-gateway-self.md](e-extend-gateway-self.md)         | draft      | Agent modifies platform code — plugin dir vs staging tree vs agent branch; scope unmade. |
| [f-replaceability-research.md](f-replaceability-research.md) | draft      | Prove off-the-shelf wouldn't have worked before building the next component.             |
| [g-pay-sh.md](g-pay-sh.md)                                   | draft      | pay.sh micropayments — agent-native paid APIs via HTTP 402; blocked on HITL.             |
| ~~a-crackbox-sandboxing.md~~                                 | superseded | KVM/qemu sandboxing → [6/9-crackbox-sandboxing.md](../6/9-crackbox-sandboxing.md)        |

## Removed in the 2026-08-02 minimization

- `d-agent-code-modification.md` — **merged into**
  [e-extend-gateway-self.md](e-extend-gateway-self.md). Both were the same
  concern (the agent changing platform code), `e` already linked to `d` as
  "related", and between them they were 37 lines. The staging tree is now
  one of three strawmen in `e`, which is what it always was.
