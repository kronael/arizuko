# 192 — your folder can now be built from several products at once

There is **no new tool for you** in this one. It changes where some of the
files in your home came from, and which of them are yours to edit.

## What landed

A group used to be seeded from exactly one product template. It can now be
seeded from an ordered LIST of them: the operator writes `~/products.toml` in
your home, one block per product, and runs `arizuko products <instance> apply
<folder>`.

```toml
[[product]]
source = "ant/examples/support"

[[product]]
source = "github.com/org/our-brand"
```

Blending happens per kind of file, never by merging two files' text — two
products share no common ancestor, so there is nothing to merge against.

## What this means for the files in your home

- **`PERSONA.md` (or `SOUL.md`), `facts/`, `tasks.toml`, and everything else** —
  written once, then yours. Re-applying the mix never rewrites them. Edit them
  freely.
- **`CLAUDE.md`** — each product owns a marked region:

  ```
  <!-- arizuko:package:support BEGIN -->
  …that product's runbook…
  <!-- arizuko:package:support END -->
  ```

  Anything you write OUTSIDE those markers is yours and is never read,
  rewritten, or reordered. Do not hand-edit the marker lines: an unbalanced or
  duplicated marker makes the whole apply refuse rather than guess, which is
  correct but means an operator has to untangle it.
- **`~/.claude/skills/<name>/`** — a skill a product ships stays
  upstream-managed. A new revision replaces it, EXCEPT when you have edited it:
  then the apply reports it and leaves your version alone. Nothing you wrote is
  overwritten silently. The same is true of `mcpServers` entries in
  `~/.claude/settings.json`.

## What did not change

Your tools, your folder, your grants, and the `/migrate` merge for stock skills
are untouched. `arizuko create --product` still seeds one product verbatim; the
mix is a separate, additive command.

If someone asks why two products cannot both supply, say, `facts/pricing.md`:
the apply refuses instead of picking one, because picking would silently discard
the other product's knowledge.

Specs: `specs/5/28-packages.md`.
