# specs/CLAUDE.md — shipping a spec

Loads when you touch `specs/`. Not needed for everyday code; it IS
needed the moment you implement or change a spec. The spec is source of
truth (see the `specs` skill for lifecycle + spec-first discipline);
this file is the **definition of done** — a new impl is not shipped
until every item below holds. "It works" is not "done".

## Architectural invariants (a new resource/feature MUST satisfy)

These are the settled rules from root `CLAUDE.md` — repeated here as a
checklist because new impl is exactly where they get violated.

- **Cold-tier management entity → resreg `Resource`.** REST handler +
  `x-mcp-*` annotations; MCP is derived, not hand-rolled. NEVER add a
  bespoke `ipc/ipc.go` tool + direct-DB CRUD for a management table —
  that drifts the agent and operator surfaces apart. A management
  resource without a resreg registration is a review-blocker.
- **Name IS wire identity, globally unique.** The resreg `Name` is the
  `/v1/<name>` REST path AND the MCP tool prefix. Two daemons must never
  register the same name.
- **One handler, two faces.** Agent-MCP (derived via
  `resreg.deriveMCPTools`) + human-REST (annotated `openapi.json` with
  `x-mcp-when`). One renderer, many sinks — never a second hand-rolled
  shape.
- **Auth is uniform middleware.** authn (who) + authz `(action,
required-scopes, target-resolver)` bound at registration. A handler
  resolving a `jid`/`folder`/`run_id` param binds it to the caller's
  folder — same gate for MCP and REST.
- **Per-daemon `audit_log`; correlation IDs flow across (APM).**
  Mutations write audit rows in their own tx. Each daemon owns + migrates
  its own DB and its own `audit_log`.
- **OpenAPI is engine-emitted** from `RowType` reflection — no `huma`,
  no `swag`, no hand JSON, no codegen.
- **Hot-tier agent actions** (`reply`/`send`/`inspect_*`, no REST twin)
  are the ONLY MCP-only, hand-authored tools. Everything else is a
  resreg resource with both faces.

## Definition of done — every surface

Ship is complete only when ALL hold. Miss one and the surfaces drift.

1. **Code** — minimal + orthogonal; `make build && make lint && go test
./... -short` green. Tests in the SAME commit — every new param,
   response field, MCP tool, REST endpoint, or behavior delta.
2. **Spec** — frontmatter `status: shipped`; `specs/index.md` +
   `specs/5/index.md` row updated; HOW trimmed, WHY + code pointers kept.
3. **Repo docs** — the aspect lands where a reader looks: root UPPERCASE
   (`README`/`ARCHITECTURE`/`SECURITY`/`EXTENDING`/`GRANTS`) + the owning
   daemon's `<pkg>/README.md`.
4. **Tiers** — placed in the hot/cold-tier model (and, for credentials,
   the broker / spawn-inject / .env credential tiers). The tier framing
   must name the new thing, not leave it uncategorized.
5. **Online** — operator-facing web docs under `template/web/pub/`
   (`concepts/` + `reference/`); the daemon's `openapi.json` is listed on
   `reference/openapi.html`; the `CHANGELOG.md` blockquote is consistent.
   Deploy via the `cp`/rsync workflow and verify `/pub/...` returns 200.
6. **Dashboard** — if operators manage it, the `dashd` surface exists
   (view AND control via `/v1`), not just the API. An operator-managed
   resource with no dashboard row is not done.
7. **Migration + broadcast** — migration file under
   `ant/skills/self/migrations/` + `MIGRATION_VERSION` bump + agent image
   rebuild. This is the single trigger for skill updates AND the chat
   broadcast (root `CLAUDE.md ## Shipping changes`).
8. **13yo test** — a smart novice can find and understand it, in both the
   web docs and the dashboard. Jargon a 13yo can't read is a doc bug.

## Ship order

spec-first → code + tests → repo docs → tier placement → web docs →
dashboard → migration/version bump → verify (green + `/pub` 200).

If a step reveals the design is wrong, fix the spec first, then the code —
never leave code that adds an unspecced structure.
