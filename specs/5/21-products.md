---
status: shipped
shipped: 2026-08-07
relates-to: [../5/28-packages]
---

# Products — curated agent templates (producer side)

A product is a curated template for an ant: a persona, skills, and seed
files, so an operator spins up a configured agent with one command instead
of building from scratch.

> **Scope split (2026-07-14, repointed 2026-08-04).** This spec is the
> PRODUCER side — what a product contains and how to author one.
> Composition, distribution, and updates live in
> [`28-packages.md`](28-packages.md) (`5/20` dissolved into it), which
> supersedes this file's single-template narrative: a group holds an
> ORDERED MIX of products, blended per payload kind.

> **Product vs prototype (`5/5`).** A product instantiates at **group
> creation** ([`5-worlds-agents-sessions.md`](5-worlds-agents-sessions.md)
> Tier 2). That spec's Tier-3 **prototype** — the `groups/<world>/prototype/`
> seed folder, applied at spawn time — is the same shape under a second name,
> not merged into one authored asset — see its design resolutions.

## A product is a recomposition, not new machinery

A product introduces no new code path. It is the same fixed reaction
pipeline (Event → Routing → Agent → Authorization → Turn → State,
[`A-primitives-framing.md`](A-primitives-framing.md)) with different
**folder contents** and different **routing**. That is the whole product:
the bottom of the four-layer stack — primitives → components → daemons →
products — where the operator works. Two consequences:

- **A product page must name its recomposition.** Every product spec
  carries a table mapping each promise on the public page to the primitive
  that already supports it, so "free today vs needs work" is provable
  rather than asserted.
- **"Focused" is the wedge, not a limitation**
  ([`../17/9-positioning.md`](../17/9-positioning.md)). Constraining an
  agent to its job's skills — and gating the rest via the ACL — removes the
  failure modes general agents have. A product is a focused recomposition
  you own and edit as files, not a general blob you rent.

## What a product is

Products live in `ant/examples/<name>/`; each folder is a prototype for the
group workspace seeded into a new instance. `PRODUCT.md` (TOML: name,
skills list, `[[env]]` hints) is required — those three are what
`productManifest` (`cmd/arizuko/main.go:29`) parses. Every shipped
`PRODUCT.md` also declares `brand` and `tagline`, which
`arizuko products <inst> list` prints (they were dead data until BUGS `F65`
was closed). Optional and
copied verbatim into the group dir: `PERSONA.md` (the agent's identity),
`CLAUDE.md` (the per-group runbook read every session), `facts/` (seed
knowledge), `tasks.toml` (seed scheduled tasks). Most products ship both
Markdown files.

**Two kinds of skill, don't conflate them.** The `skills` LIST in
`PRODUCT.md` is a whitelist SELECTING stock skills from the image —
informational today (`SetupGroup` seeds all of them), slated to become real
gating. A product MAY ALSO bundle its OWN `skills/` directory: the managed
payload `5/28` install/upgrade governs via dirty-detection and the 3-way
merge. Stock selection ≠ bundled payload; a product may use either or both.

`[[env]]` blocks are printed by `arizuko create` as setup instructions.
They do **not** validate the env file — they are a checklist, not a gate.

## Installing and authoring

`arizuko create <inst> --product <name>` parses
`ant/examples/<name>/PRODUCT.md`, creates the instance data dir and the
`main` group row with `product=<name>`, then calls
`container.SetupGroup(cfg, "main", productDir)` to copy the product files,
seed `.claude/`, and chown to UID 1000. It finishes by printing the
`[[env]]` hints. Implementation: `cmd/arizuko/main.go` (`cmdCreate`,
`productManifest`).

Authoring a new product is **filesystem-only — no code changes**:
`cmdCreate` discovers products by scanning `ant/examples/` at runtime. Add
the folder, then a spec at `specs/17/product-<name>.md` and a row in
`specs/17/index.md`.

The seedable catalog is `ant/examples/` itself; public pages render at
`/pub/arizuko/products/<name>/` when the web layer runs. The two sets
overlap in four names today and are not expected to match: a page may
sell a positioning product with no template, and a template may ship
before its page (BUGS `F64` tracks the current split). **Company brain**
is the deliberate case — positioning, not a seeded template
([`../17/8-company-brain.md`](../17/8-company-brain.md)): arizuko is the
action layer over a retrieval backend, and it ships as framing plus a setup
recipe until connector skills land.

`arizuko products <inst> list` enumerates the catalog — `name`, `brand`,
`tagline` per `PRODUCT.md` — and names a directory whose manifest won't
parse instead of omitting it, so a broken manifest cannot read as "not
shipped" (`cmd/arizuko/products.go`, `listProducts`).

## Open

- Skill whitelisting is not enforced; all global skills are seeded. The
  `skills` list in `PRODUCT.md` stays informational until it is.
- Third-party products (per-instance product dirs) — deferred.
- `--product` accepting a URL or git repo — deferred; `5/20`'s source
  resolvers are the intended path.

None of the three blocks the producer side this spec defines. The first is
stated on the surface it affects rather than hidden; the other two are
distribution, which is `5/28`'s concern.
