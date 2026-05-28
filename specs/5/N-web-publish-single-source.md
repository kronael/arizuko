---
status: draft
depends: [V-web-vhosts, W-webhook-routes, 36-yaml-manifests, 5-uniform-mcp-rest]
---

# One URL, one backing store: web publish single-source

## Problem

`/pub/<seg>/<path>` can be produced by three mechanisms that share one
URL namespace and drift:

1. vited static files under `<data>/web/pub/` (the whole tree is vited's
   root; `compose/compose.go`, mount `<data>/web:/web`).
2. A group's `~/public_html`, bind-mounted from `<data>/web/pub/<folder>/`
   (`container/runner.go`, FHS slot model — migration 150 / `5/V`).
3. A `web_routes` redirect row consulted by proxyd (`proxyd/main.go`,
   longest-prefix match).

Mechanism 2 is a _view into a subdir_ of mechanism 1 — that part is
single-source and correct. The break is the REST of mechanism 1: vited's
root also holds **top-level, ownerless** sections (`pub/guides/`,
`pub/killer/`, `pub/index.html`, …) that no group's slot mounts. They are
populated by a human rsync (`CLAUDE.md` "Updating the web docs") and by
hand-copies.

Incident (marinade, 2026-05-28): the guide
`maximize-validator-sol-earnings.html` existed at `pub/guides/`,
`pub/atlas/`, and `pub/atlas/guides/`. The atlas agent could update its own
slot (`pub/atlas/...`) but not `pub/guides/` — that path is outside its
container mount. A human hand-synced all three; they drifted. `web_routes`
and `vhosts.json` were both empty, so every `/pub/*` request was raw vited
static serving with no indirection.

Violates "one renderer, many sinks" (CLAUDE.md): N file copies feed one URL.

## Decided model

**Each `/pub/<seg>/` URL is backed by exactly one store. There are no
ownerless static trees under `<data>/web/pub/`.**

1. **Every top-level segment of `/pub/` is owned by a group.** A group's
   slot projects to `<data>/web/pub/<folder>/`. The only writers to
   `<data>/web/pub/` are (a) group containers, each into its own slot, and
   (b) tier-0 root, which owns the top level and the shared frame
   `/pub/arizuko/` (root's `public_html` projects to the tree top per
   ant/CLAUDE.md "Tier 0").

2. **Cross-group / aliased URLs are redirects, never copies.** A top-level
   alias like `/pub/guides/` is a `web_routes` row
   `{path_prefix:/pub/guides/, access:redirect, redirect_to:/pub/atlas/guides/, folder:atlas}`.
   Longest-prefix match in proxyd already serves this. No second file tree.

3. **The agent publishes via an action, never by writing a path it cannot
   mount.** Publish = write into `~/public_html/` (its slot) +
   `set_web_route` for any alias. An agent never needs `pub/guides/` on its
   filesystem; it owns the content at `pub/<folder>/...` and points the
   alias at it. This is the MCP+REST-uniform publish path.

4. **Ownership of top-level prefixes is explicit and first-claim.**
   `set_web_route` today (`ipc/ipc.go`) constrains only `redirect_to` to
   the caller's slot; it leaves `path` (the prefix) unconstrained — any
   group can claim `/pub/guides/`. Tighten: a `web_routes` row whose
   `path_prefix` is a top-level prefix outside the caller's own
   `/pub/<folder>/` is allowed only if unclaimed (no existing row) —
   recorded with `folder` = claimant. The `0068` FK
   (`web_routes.folder → groups`, CASCADE) already retires the claim when
   the owner dies. Operator-curated top-level paths (`/pub/arizuko/`,
   marketing `/pub/index.html`) are root-owned and declared in the instance
   manifest's `web_routes` (`5/36`, `owner: system`).

## Migration

1. Inventory `<data>/web/pub/` top-level entries. For each ownerless
   subtree that duplicates a group's content, keep ONE canonical copy in
   the owning group's slot; replace the others with a `web_routes` redirect
   (or delete if redundant). Operator docs collapse to `/pub/arizuko/`
   (root slot) — the `template/web/pub/` rsync target becomes
   `<data>/web/pub/arizuko/` only.
2. Marinade specifically: canonical = `<data>/web/pub/atlas/guides/...`;
   add `web_routes` `/pub/guides/ → /pub/atlas/guides/` owned by atlas;
   delete `<data>/web/pub/guides/` and the `.bak-*` artifacts.
3. Constrain `set_web_route` path-claim to unclaimed top-level prefixes
   (code change in `ipc/ipc.go`).
4. Document in ROUTING.md "HTTP Routing" that vited's static root holds
   ONLY group-owned slots + root's top-level; cross-segment aliases are
   `web_routes` redirects, never file copies.

## What gets deleted

- Ownerless duplicate static trees under `<data>/web/pub/` (per-instance;
  marinade: `pub/guides/`, stray hand-copies, `*.bak-*`).
- The implicit "rsync to any subdir of web/pub/" affordance: the CLAUDE.md
  web-docs workflow targets `<data>/web/pub/arizuko/` only.

## Related

- `5/V-web-vhosts.md` — the FHS slot model this constrains (slot = single
  view of one subtree); this spec forbids the parallel ownerless trees `V`
  left implicit.
- `5/W-webhook-routes.md` — same shape: one row, one owner folder, FK to
  groups; publish/claim is the static-content analog.
- `5/36-yaml-manifests.md` — `web_routes` is a manifest-managed resource
  (`owner: system` rows declare operator-owned top-level prefixes).
- migration 150 (`ant/skills/self/migrations/150-v0.45.11-fhs-and-web-slots.md`)
  — the bind-mount slot definition.
- `ROUTING.md` "HTTP Routing (proxyd)" — the precedence list this pins down.
