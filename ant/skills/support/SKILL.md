---
name: support
description: >
  Answer a concrete factual question about a domain entity (validator, account,
  transaction, epoch, order, ticket, named record) with a primary-source citation.
  Track multi-turn cases on the same entity.
when_to_use: >
  Use when the user asks a yes/no or specific-value question about a named thing —
  "is X flagged?", "what's the bid for Y?", "Z's last 10 epochs". NOT for research
  into how something works (→ /find), howto pages (→ /howto), greetings (→ /hello).
user-invocable: true
arg: <question, optional — defaults to the latest user message>
---

# Support

Answer with a primary-source citation. Track the case across turns.

## 1. Case

Extract entities: pubkeys, epoch numbers, transaction hashes, account names,
ticket IDs. Treat the turn as a continuation when:

- an entity matches one in the prior 3 assistant turns
- the message is a `<reply-to>` to an assistant turn
- the user said "verify", "you said", "that's wrong", "no it's", "show me",
  "explain why"

Continuation → carry the source path and prior cite. New entity with no
shared signal → new case, reset.

## 2. Gather

Find the canonical source for each entity in `~/facts/sources.md`. Unknown
entity type → `/find <entity-type> canonical source`, then append to that
file so the next case is one grep away.

Open the source. Grep the literal entity ID. Read the recorded outcome —
don't derive it from a formula when a recorded field exists. For range
queries ("last N", "all X", "over M"), enumerate the full range; never
sample one and conclude.

## 3. Reply

Lead with the answer in one short sentence. Then the cite — source path +
field:

```
Flagged for BidTooLow in epoch 967.
source: refs/ds-sam-pipeline/auctions/967.49127/outputs/results.json
field:  bidTooLowPenalty.coef = 0.3609
```

For a range, paste all rows or summarise with an explicit count: "checked
958–967, only 967 flagged." If the source is silent: "field not present —
can't verify either way." Never "likely", "probably", "should be" — either
you read it or you didn't.

Re-read `~/SOUL.md` Voice section and rewrite the reply in that register
before sending. SOUL beats default formatting; drop tables and headers if
voice rejects them.

## 4. Persist

- New canonical source learned → append to `~/facts/sources.md`
- User correction → `/users` for this sender
- New general knowledge surfaced (formula, policy, mechanism) → `/find`
