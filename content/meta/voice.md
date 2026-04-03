---
title: Voice
updated: 2026-04-01
---

# Voice

## Persona

The voice is that of a system designer explaining decisions they lived through —
not a teacher, not a marketer. Direct, precise, willing to name what went wrong.
We assume the reader is a builder who has been burned by complexity before and
is looking for a way of thinking, not a list of features.

Peer-to-peer. We are talking to people who will read the code if they don't
trust the argument. Write as if they will.

Rich Hickey register. "Simple Made Easy" is the model: identify a confusion
people carry, make one precise distinction that resolves it, show the practical
consequence, let the example confirm what the argument already established.
The argument stands before the example arrives.

---

## Rules

**Open with the distinction, not the context.**
Wrong: "As AI systems become more complex, it's worth thinking about simplicity..."
Right: "Simple and easy are not the same thing. Conflating them is why most systems stay complicated."

**Name the complecting specifically.**
Wrong: "this adds complexity"
Right: "this couples the routing decision to the auth policy — now you cannot change one without touching the other"

**State claims directly. No hedging unless uncertainty is the point.**
Wrong: "isolation might be a useful approach"
Right: "isolation is the correct default"
If something is uncertain, say why it is uncertain — not "might", but "we don't know yet because we haven't measured X".

**First principles, not analogies.**
Derive the conclusion before reaching for an analogy. Analogies illustrate;
they don't establish. Use them only after the argument stands on its own.

**Every sentence advances the argument.**
No scene-setting. No warm-up paragraphs. No summary conclusions that restate
what you just said. End on the implication or the next question — not a recap.

**Show the cost, not just the benefit.**
If a decision trades operational complexity for architectural simplicity, say so.
If a convention required removing something that felt useful, name what was removed.
Credibility comes from naming tradeoffs, not hiding them.

**Arizuko is the example, not the subject.**
Every post argues a principle. Arizuko decisions are evidence for that principle.
"We found that..." not "arizuko does...".

---

## What we never say

- "Simple" when we mean "easy" or "small"
- "Powerful" — this is always a substitute for a concrete claim
- "Flexible" without specifying what varies and what doesn't
- "Best practice" without deriving why it is best
- Framework feature lists — we are arguing a method, not selling a product
- Future promises: what we are building, what is coming
- Competitor comparisons by name
