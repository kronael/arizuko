---
name: support
description: >
  Answer concrete factual questions about a domain entity (validator, account,
  transaction, epoch, order, ticket, named record) with a primary-source
  citation. USE for yes/no or specific-value questions like "is X flagged?",
  "what's the bid for Y?", "Z's last 10 epochs", "verify this", "check your
  answer", "show me the source", "self-correct on this". Tracks multi-turn
  cases on the same entity; verifies before send; self-corrects on derivation.
  NOT for research into how something works (use /find), howto pages
  (use /howto), or greetings (use /hello).
user-invocable: true
arg: <question, optional — defaults to the latest user message>
---

# Support

Answer with a primary-source citation. Verify before send. Self-correct
on derivation. Track the case across turns.

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

Lead with the answer in one short sentence. Body follows (relevant content,
any range data, explanation if asked). **Citation block at the end** of
the message:

```
Flagged for BidTooLow in epoch 967.

The drop from 0.046 → 0.041 cPMPE fell below the historical floor of
0.04489. Coefficient 0.3609 applied.

---
source: refs/ds-sam-pipeline/auctions/967.49127/outputs/results.json
field:  bidTooLowPenalty.coef = 0.3609
```

For a range, paste all rows or summarise with an explicit count: "checked
958–967, only 967 flagged." If the source is silent: "field not present —
can't verify either way." Never "likely", "probably", "should be" — either
you read it or you didn't.

Voice register is anchored by the gateway's per-turn `<persona>` block.
If the register feels drifted mid-reply, run `/persona` to re-read.

## 4. Verify before send

Before sending, re-grep the source you just cited for the value you're
claiming:

```bash
grep -F "<entity-id>" <source-path>
grep -F "<the-claimed-value>" <source-path>
```

If either grep returns nothing — STOP. You derived. Go to step 5.

## 5. Investigate (only when verify fails)

You shipped a derivation, not a source read. Self-correct:

- Re-verify the entity ID literally (pubkey / epoch / hash). Typos
  cascade silently.
- Re-verify the source path — is it the right file? Check
  `~/facts/sources.md` for the canonical mapping for this entity
  type. Source may have moved or been renamed.
- Re-verify the field name. The field you reached for may not exist;
  the recorded outcome may be in a different key.
- Re-grep `~/facts/` for past corrections on the same entity. The
  operator may have already taught you what you just got wrong.
- Re-run step 2 (Gather) from scratch with the new assumptions.

If verify still fails after investigation:

> Send: "I can't verify this. Checked `<path1>`, `<path2>`, `~/facts/<x>.md`.
> The field I expected (`<field>`) isn't there. Possible alternatives:
> `<list>`. Tell me which to look in."

Never send a confident answer you couldn't verify. The honest "I can't
verify" is the floor, not the failure mode.

## 6. Persist

- New canonical source learned → append to `~/facts/sources.md`
- User correction → `/users` for this sender
- New general knowledge surfaced (formula, policy, mechanism) → `/find`
- Verify-fail that surfaced a wrong assumption you'd made before → write
  a `~/facts/` entry so the next case doesn't repeat the mistake
