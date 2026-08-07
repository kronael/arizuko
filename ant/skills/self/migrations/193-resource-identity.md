# 193 — release marker: nothing changes for you

No skill changes, no new tools, no changed tool names, nothing different in
your home. This file exists so auto-migrate fires.

What landed is internal: every management resource's name — the thing that is
both its `/v1/<name>` URL and the prefix of your tool for it — used to be
written down in two places and is now written once. Your tools keep the exact
names they had.

Spec: `specs/5/16-mcp-rest-unification.md` (shipped).
