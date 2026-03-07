# Escalation (Upward Delegation)

**Status**: not started. Depends on specs/v1m1/permissions.md.

## Problem

Restricted agents need to ask their parent for help.
Delegation is downward only (parent → child). The atlas
support pattern needs the inverse: child → parent.

Also: a support ingress agent handles both public forum
questions and private tickets. It can't see private data
but needs knowledge mined from ticket resolution. The
knowledge mining must happen at the world/support level,
not at the public-facing agent level.

## Architecture

```
atlas/                      world (tier 1)
├── atlas/support           agent (tier 2, rw workdir)
│   ├── answers tickets directly
│   ├── mines facts from resolved tickets
│   └── publishes sanitized knowledge to atlas/support/web
└── atlas/support/web       worker (tier 3, ro)
    ├── answers public questions from mined facts
    ├── escalates to atlas/support when facts insufficient
    └── CANNOT see ticket data (different JID, not mounted)
```

The agent (atlas/support) is the knowledge gate:

- Sees tickets, can write facts
- Mines patterns from resolved tickets
- Controls what knowledge reaches the worker
- Subagents never spill private ticket content

The worker (atlas/support/web) is the public face:

- Reads facts (ro), answers questions
- Escalates when facts are insufficient
- Never sees raw ticket data
- Parent returns sanitized findings only

## Escalation protocol

```typescript
// Tier 3 worker sends:
{
  action: 'escalate',
  input: {
    request: string,     // what the agent needs help with
    context?: string,    // relevant user message snippet
    expectReply: true    // synchronous: wait for parent response
  }
}

// Parent receives as system message:
<system origin="child" event="escalation" from="atlas/support/web">
  <request>User asks about validator bonds APY calculation</request>
  <context>How is the APY for validator bonds calculated?</context>
</system>

// Parent responds via IPC reply:
{
  findings: string,     // sanitized answer for child to present
  newFacts?: string[],  // optional: paths to new facts written
}
```

## Knowledge flow

```
ticket resolved → atlas/support mines facts → facts/ dir
                                            ↓
atlas/support/web reads facts/ (ro) → answers public questions
                                      ↓ (insufficient)
escalate to atlas/support → research → return sanitized findings
```

The support agent acts as a knowledge firewall:

- Private ticket details stay at support level
- Only generalized facts flow down to forum
- Escalation responses are sanitized (no customer names,
  no ticket IDs, no private context)

## Open Questions

1. **Synchronous vs async** — should escalation block the
   restricted agent until parent responds? Or fire-and-forget
   with callback? Sync is simpler, async handles slow research.

2. **Escalation depth** — can a world (tier 1) escalate to
   root (tier 0)? Probably yes, but what's the use case?

3. **Facts mining trigger** — how does atlas/support know when
   to mine facts from a resolved ticket? Scheduled task?
   System message on ticket closure? Agent self-triggers?

4. **Knowledge sanitization** — is it the support agent's
   responsibility (prompt-based) or enforced by gateway
   (strip patterns, PII detection)?

5. **Multiple escalation targets** — can a tier 3 worker
   escalate to a sibling instead of parent? Probably not —
   keep it simple, always parent.

6. **Rate limiting** — should escalation have quotas?
   Restricted agent hammering parent with requests.
