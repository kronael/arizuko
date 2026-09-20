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
required-scopes, target-resolver)` bound at registration as an injected
  `Gate` — resreg owns no auth policy. Operator REST uses the default
  `auth.Authorize` (scope/ACL); the agent MCP socket injects
  `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` (granted `acl` rows —
  the row's scope glob IS the containment; no depth-derived default).
  A handler resolving a `jid`/`folder`/`run_id` param binds it to the
  caller's folder under whichever gate is mounted.
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
   the three tiers of `5/14`: env-profile keys, which are the only
   table-backed credential that reaches container env; capability
   credentials, which are broker-only; infra credentials, which live in
   the host `.env`). The tier framing must name the new thing, not leave
   it uncategorized.
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

## Citing a spec

**Cite by FILENAME wherever a link is possible** — `specs/5/16-mcp-rest-unification.md`,
or `5/16-mcp-rest-unification` in prose. The bare `N/M` form is reserved for a
sentence that also describes the spec it names, so a reader can tell from the
sentence alone whether the pointer still fits.

The filename form is **self-checking** and the bare number is not. A filename
stops resolving the moment the file moves, and `tests/specrefs` says so. A bare
`5/5` keeps resolving after a renumber and silently names a different spec —
`5/5` was `uniform-mcp-rest` and is `worlds-agents-sessions` today. Neither
`tests/specrefs` nor `tests/webdocs` can flag that, by construction: both ask
whether the number resolves, never whether it names the same document
(BUGS `L237`).

This is not hypothetical arithmetic. **MEASURED 2026-09-20: 56 spec numbers are
live today and named a different file before** — among them `5/5`, `5/9`,
`5/33`, `5/34`, `6/1`, `6/41`, `6/42` and every number in phases 16 and 17.

Two corollaries, both of which have cost real work:

- **A retired number stays burnt.** Never reuse one for new content; a citation
  written against the old meaning cannot be distinguished from one written
  against the new.
- **Never rewrite a spec ID globally.** A number is only safe to replace where
  it had ONE meaning across the whole corpus, and in phases 6 and 7 it never
  did. Rewrite by reading each site's claim and choosing the spec whose CURRENT
  text supports it. A sweep on 2026-09-20 found the destination lay outside the
  retirement commit's own account of where the content went, for two of three
  families — the claim moves with the code, not with the paperwork.
