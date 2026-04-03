# Content spec

Meta articles on building software. Arizuko is the demonstration vehicle —
a real system built on these principles, not a toy example.

The through-line: **meaningful simplicity comes from boundaries that earn their
existence — only where concepts are genuinely distinct.** Too few boundaries
and everything is coupled. Too many and nothing is comprehensible. Every post
makes this argument through concrete decisions made in arizuko.

---

## Foundation: Minimalism as method

This is not a theme among others. It is the lens through which everything
else is seen. Every design decision in arizuko is a minimalism decision.

The argument: most complexity is accidental. It comes from premature
abstraction, unclear boundaries, and fear of starting over. The antidote is
not simplicity-as-aesthetic but decomposition-as-practice — breaking things
until each part has one job and no hidden dependencies.

Arizuko is a case study: a multi-tenant AI agent router built from ~10 small
binaries, a SQLite database, and a file convention. Every post traces a
design decision back to this method.

---

## Theme 1: Only what you need

**The argument**: The minimal system is not the stripped-down system.
It is the system where nothing is present that shouldn't be. Achieving this
requires courage to remove, not just restraint about adding.

**Posts**

1. **"The cost of every line"** — Complexity is not proportional to features.
   It is proportional to dependencies between parts. Why the first question
   when adding anything should be: what does this touch?

2. **"Zero to running, nothing hidden"** — Walk a full arizuko deploy.
   One binary, one `.env`, one compose file. Trace every decision that makes
   this possible. The hidden work that produces apparent simplicity.

3. **"Delete as design"** — How we reduced proxyd's access model from three
   config flags to one rule. The discipline of removing options. Why fewer
   choices make a system more useful, not less.

4. **"Isolation is the cheapest safety"** — One container per conversation.
   No shared state. Why isolation-by-default is not a performance tradeoff —
   it is the only design that doesn't leak complexity across components.

---

## Theme 2: Convention as compressed wisdom

**The argument**: Convention over configuration is not a shortcut. It is the
mechanism by which hard-won decisions become structural. Good conventions make
the right thing the easy thing — they encode expertise so the user doesn't
have to rediscover it.

**Posts**

5. **"The decision you make once"** — Every path in arizuko
   (`/workspace/self/`, `/pub/`, `/srv/data/arizuko_<name>/`) is a decision
   made once that ripples everywhere. How naming conventions carry
   architectural meaning. Why consistency is more valuable than optimality.

6. **"Auth by default"** — Why every route except `/pub/` requires auth
   without configuration. Convention as encoded security posture. The cost
   of misconfiguration when convention is absent.

7. **"Skills as portable behaviour"** — Drop a `SKILL.md`, it runs. No
   registration, no imports. How file-system convention turns contribution
   into a single-file operation. The protocol is the directory structure.

8. **"When to break the convention"** — Convention is not dogma. The test:
   can you state the convention in one sentence? If not, it isn't a
   convention — it's a special case masquerading as one.

---

## Theme 3: Complete products, open extension

**The argument**: A framework is a set of parts. A product is a complete
thing that works before you touch it. The distinction matters most when LLMs
are the customization layer. An LLM cannot work with a half-finished
framework — but it can extend a complete product if the extension boundaries
are clear and the contracts are readable.

Microservices are not an architecture pattern here — they are the mechanism
that makes extension safe and composable. Each daemon has one job and a
documented contract. An LLM that reads the contract can write a new adapter,
a new skill, a new sidecar.

**Posts**

9. **"Finished, not done"** — The difference between shipping a framework
   and shipping a product. Arizuko on day one: agent answering Telegram,
   web layer running, auth working. Why completeness is a prerequisite for
   extensibility.

10. **"Contracts over coupling"** — Each arizuko component touches the world
    through a contract: SQLite schema, HTTP API, file convention, MCP
    protocol. How explicit contracts make the system decomposable without
    making it fragile.

11. **"Writing for the LLM reader"** — `SKILL.md` is not documentation. It
    is an instruction set the agent executes. How to write system
    descriptions that an LLM can act on. The difference between explaining
    what something is and explaining what to do with it.

12. **"Extending without forking"** — Three extension patterns: new adapter
    (~200 lines, one HTTP contract), new skill (one markdown file), new MCP
    sidecar (any language, one unix socket). Why the microservice boundary is
    the correct abstraction for LLM-driven customization.

13. **"The self-modifying system"** — When the agent reads its own SKILL.md,
    updates its own CLAUDE.md, and runs `/migrate` to propagate changes to
    all groups — that is the culmination of the architecture. Simplicity,
    convention, and clear contracts together produce a system the agent can
    reason about and modify safely.

---

## Format

- 1000–1500 words each. Argument-first, concrete second. Code only when
  it proves a point that prose cannot.
- Arizuko is the example throughout, never the subject. The subject is
  the principle.
- Publish: `REDACTED/pub/arizuko/`. Series index page.
- Write in order: 1 → 13. Each post assumes the prior argument.

## Voice — Rich Hickey register

"Simple Made Easy" as the model. Key characteristics:

- **Distinguish before arguing.** Define terms precisely and early.
  "Simple" is not "easy". "Convention" is not "restriction". "Complete"
  is not "finished". The argument depends on the distinction holding.

- **Name the complecting.** Identify specifically what is braided with
  what. Not "this is complex" but "this couples the routing decision to
  the auth policy — now you cannot change one without touching the other."

- **First principles, not analogies.** Derive the conclusion. Don't
  illustrate with metaphors until the argument is already standing.

- **No hedging.** State the claim directly. "Isolation is the correct
  design." Not "isolation might be worth considering."

- **Dense, not long.** Every sentence advances the argument. No
  scene-setting, no warm-up paragraphs, no summary conclusions.

- **The talk structure.** Identify a confusion in how people think about
  the topic. Resolve the confusion by making a distinction. Show the
  practical consequence of the distinction. Let the example confirm what
  the argument already established.
