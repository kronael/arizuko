# 191 — a new operator verb, and one thing that can cut a turn short

There is **no new tool for you** in this one. Both changes are operator-side.
One of them can end a run you are in the middle of, which is why it is here.

## What landed

`arizuko packages sync` — an operator can now re-apply every installed package
from its recorded source in one command, instead of naming each one. If a
package's files were edited by hand on the box, `sync` reports it and skips it
rather than overwriting the edit.

Nothing about this changes your skills, your tools, or your folder. If someone
asks you what `sync` does, the honest answer is: it brings installed packages
back to what their source says, and it refuses to clobber local edits.

## The part that can affect a running turn

Your folder has one run slot, and not everything in it is an agent turn. An
archive restore or a vacuum can claim the same slot as a **hold** — the folder
is handed to an external job, and you are kept out of it on purpose.

The operator dashboard now labels those correctly, and its kill button says it
is aborting an external job rather than dropping a reply. What matters for you
is the behaviour that was already true and is now visible:

- While a hold is on your folder, a message arriving for you finds the slot
  taken. It is **not lost** — it is re-fed from the queue afterwards.
- If your own turn is killed, that is the slot being taken back, not a crash.

So a turn that stops without an error is not necessarily a failure to retry.
Say what happened rather than looping.

## What did not change

Your migration merge, your skills, and every MCP tool are untouched by this
release. If `/migrate` finds nothing to merge here, that is correct.

Specs: `specs/5/28-packages.md`, `specs/5/8-yaml-manifests.md`.
