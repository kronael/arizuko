---
title: Core Narrative Pillars
updated: 2026-04-01
---

# Pillars

Three durable claims every post traces back to, ordered by priority.
Arizuko is the demonstration vehicle throughout — not the subject.
The subject is always the principle.

---

## 1. Boundaries must be economical — only where concepts are genuinely distinct.

Most systems fail in one of two directions: no boundaries (a monolith where everything touches
everything) or too many boundaries (a microservice per noun, a class per verb). Both produce
complexity. The first makes things hard to change. The second makes things hard to understand.
Meaningful simplicity is neither. It is the minimum number of boundaries that reflects real
distinctions in the domain — where each boundary tells you something true about what is on
either side of it.

A boundary earns its existence by carrying information. If you can remove it and nothing
conceptually changes, it was noise. If you remove it and two different things merge, it was real.
Arizuko has ~10 components not because we decomposed maximally, but because the domain has
~10 genuine concerns: routing, storage, auth, channel adapters, the agent runtime, the web
layer, scheduling. Each boundary exists where the concepts actually differ.

**Evidence:** A channel adapter is ~200 lines because the concept is small: receive a message,
forward it, send a reply. Splitting it further would separate things that belong together.
Merging it into the gateway would couple message routing to transport protocol — two genuinely
different concerns. The boundary is where it is because that is where the domain is.

**Counter:** "You could merge X and Y and reduce the component count." → Sometimes yes —
and we have. The test is not component count. It is whether the merged thing has one coherent
job or two different jobs that happen to coexist.

**Won't say:** That every boundary in arizuko is optimal, or that the right decomposition is
obvious in advance. It emerges from understanding the domain. The practice is learning to
ask: is this boundary earning its keep?

---

## 2. Convention encodes decisions that should not be made twice.

Configuration is a tax on the user. Every option the user must set is a decision the system
refused to make. Good convention makes the right thing the default — encoding accumulated
expertise so it does not have to be rediscovered. Convention is not restriction; it is
compressed wisdom made structural.

**Evidence:** Every route except `/pub/` requires auth — no config. Skills are discovered by
directory convention — no registration. The data dir is always `/srv/data/arizuko_<name>/` —
no override needed. Each of these conventions eliminated a class of misconfiguration entirely.

**Counter:** "Convention reduces flexibility." → Conventions do not prevent different choices —
they make the common case require no thought, leaving energy for the cases that actually differ.
The test: can you state the convention in one sentence? If not, it is a special case, not a convention.

**Won't say:** That convention is always obvious in advance. Most of these conventions emerged
from mistakes. Convention is how you prevent the second mistake.

---

## 3. A complete product with clear extension boundaries is what LLMs can actually use.

A framework is a set of parts. A product is a thing that works before you touch it. The
distinction matters most when the customisation layer is an LLM. An LLM cannot hold together
a half-finished framework — but it can extend a complete system if the contracts are explicit
and written to be read. Microservices are not an architecture pattern here; they are what makes
extension safe, legible, and composable by a language model.

**Evidence:** An arizuko skill is a single markdown file the agent reads and executes. A new
adapter is ~200 lines against one documented HTTP contract. The agent runs `/migrate` to
propagate its own skill updates to every group. The system can reason about and modify itself
because the boundaries are explicit and the contracts are in plain text.

**Counter:** "LLMs will hallucinate extensions." → Yes — when contracts are implicit. When
the contract is a readable file with concrete examples, the failure rate drops to what the
model's reasoning ability determines. The boundary is the documentation.

**Won't say:** That LLM-driven customisation is reliable in general. It is reliable in proportion
to how explicitly the contracts are written. Garbage in, garbage out — but well-specified
contracts are a solvable engineering problem.
