---
status: reference
depends:
  [
    13-ext-mcp,
    14-credentials,
    15-surrogate-oauth,
    17-openapi-mcp,
    19-hitl-firewall,
  ]
---

# specs/5/25 — integration catalog: the surface, derived from use cases

> **Terminal state: `reference` (2026-08-07).** This is an analysis doc, not a
> deliverable — the surface it derives is implemented by the specs it names
> (`5/13` shapes 2 and 3 ship; `openapi2mcp` is `8/A`; the OAuth wall is `5/15`;
> the stakes axis is `5/19`, shipped 2026-08-07). Nothing here waits on a
> decision, and there is no `specs/5` code left to write against it. Its sibling
> corpus [`26-integration-usecases.md`](26-integration-usecases.md) carries the
> same status for the same reason.

The niche: arizuko is **not the next agent** — it is the **gated, audited
integration fabric plus a curated service catalog** an agent operates.
Browse ready templates or point at any OpenAPI directly; every call is
grant-gated, credential-brokered (secrets never enter the container), and
audited through the one `5/13` dispatch chain. Bring your own agent/model
— arizuko sells the wiring, governance, and teaching, not the intelligence.

The surface below is **derived** from a ~120-use-case corpus
([`26-integration-usecases.md`](26-integration-usecases.md)), not
asserted.

## What the corpus shows

1. **Acquisition is a precedence, not a choice.** The same service is
   reachable four ways; take the highest that applies.
   1. **Vendor MCP server** — prefer when it exists (GitHub, Stripe,
      Shopify, Supabase, Notion, Linear, Tavily, …). It tracks the
      vendor's own auth/param evolution so we don't. → `connectors.toml`
      (shipped, `5/13` shape 2).
   2. **OpenAPI auto-import** — the DEFAULT for API-first services
      (Cloudflare, Vercel, Datadog, Sentry, PayPal, Airtable, …). →
      `openapi2mcp`, **the key gap** (`8/A`; researched, not built).
   3. **Hand REST descriptor** — the workhorse for small/stable/no-spec
      APIs (Porkbun, Vault, Prometheus, Ghost, Home Assistant, …). →
      `[[ext]]` TOML (shipped, shape 3).
   4. **Go handler** — the un-descriptor-able: AWS SigV4, GCP SA-JWT,
      registry bearer-challenge, 2-step token exchange, non-HTTP
      (SMTP/IMAP). → shape 1.
2. **Auth is the real fault line, not the tool count.** Three tiers:
   static-cred self-serve (~55–60%: `bearer`/`apikey-header`/
   `apikey-query`/`basic`/`json-body` — a TOML field plus one
   `secret set`); **OAuth-only (~⅓**, concentrated in Google, Microsoft
   Graph, Salesforce, Shopify, PayPal, QuickBooks, Xero) which needs a
   human consent flow and refresh management, **not** a TOML field — this
   is the actual wall on agent self-serve, routed to the `5/15` broker;
   and signature/derived (SigV4, GCP JWT) which can't be a descriptor at
   all.
3. **`read|write` is too coarse — add a stakes axis.** Reads are
   low-stakes → default-grant. Writes split: **money movement** (refund,
   capture, invoice) and **destructive** (row delete, schema migration,
   S3 overwrite, DNS change) are **high-stakes** → a `5/19` HITL confirm
   plus idempotency and a dry-run affordance, not just a grant check.
4. **The descriptor must express more than base+method+path.** Recurring
   across all four domains: typed path-params; three pagination
   conventions (cursor / offset / JSON:API); auto-generated idempotency
   keys for high-stakes writes; `response ∈ json|binary|xml` (Figma, TTS,
   maps, PDF move bytes; Namecheap returns XML); async submit→poll pairs
   with a terminal-state contract; a standardized `{filter, limit,
cursor}` triple mapped to each vendor's DSL rather than exposing raw
   query syntax to the agent; and a large-text truncation convention
   (scrapers blow the tool-result budget).
5. **Auth is not one-header.** `apikey-header` needs a header name, an
   optional value template (PagerDuty `Token token=%s`), and multiple
   headers (Datadog `DD-API-KEY` + `DD-APPLICATION-KEY`).

## The template — an integration is one manifest

Extends `5/13`'s `[[ext]]`. Net-new fields, each earned by a finding
above: `acquisition` (mcp|openapi|rest|go), `stakes` (low → default-grant,
high → `5/19` confirm + idempotency), `kind` (sync | async submit+poll),
`response` (json|binary|xml), `pagination` (none|cursor|offset|jsonapi),
`idempotent`, plus multi/templated auth headers and an `oauth2` method
that refs the `5/15` broker instead of naming a secret. Catalog metadata
(`provider`, description, docs URL) rides along so the operator sees the
governance before installing.

**Nothing in the runtime changes.** A template compiles to the same gated
MCP tools the agent already calls, through the existing chain: grants →
secret injection → handler → audit.

## Acquisition, concretely

- **`arizuko integration search [query]`** — browse the bundled catalog
  plus any given source. Surfaces per service: tools, scopes, auth tier
  (self-serve vs OAuth), and stakes.
- **`arizuko integration add <inst> <folder> <source>`** — source is a
  catalog name, a git repo of templates, OR an **OpenAPI URL** (→
  `openapi2mcp` derives the template; scope + stakes annotation is a
  curation step, and large specs are filtered to the tools the folder
  needs). Prints the exact `secret set` or OAuth-consent link and any
  `network allow` egress the tools require. **NEVER auto-grants.**
- **Direct use is the same path with an OpenAPI URL** — no catalog entry
  needed. Catalog and direct-use produce one artifact.

## The skills

A stock skill set teaches the agent to operate the fabric: discover a
service → install its template → request the secret/OAuth from the
operator → smoke-test a read tool → use it; and modify a template (add a
tool, tighten a scope). This is the existing `ant/skills/` mechanism, not
new machinery.

## Honest risks

1. **OAuth onboarding is the wall.** ~⅓ of the corpus can't be
   agent-self-served. The catalog must say "operator setup", not
   "one-click".
2. **Auto-OpenAPI→MCP is not free.** Schema fidelity, hundred-op specs
   needing curation not blind import, and auth still manual afterward. It
   reduces hand-authoring; it does not erase setup.
3. **Signature / async / binary need escape hatches**, not the happy
   path. A "one request → one JSON response" surface silently fails them.
4. **High-stakes writes stay human-gated.** Do not sell autonomous
   money-movement — the field hasn't shipped it and the corpus says don't.
5. **Third-party templates carry the third-party trust problem.** A
   template's tool runs with the folder's real grants, secrets, and
   egress; folder containment bounds cross-tenant blast radius, not
   in-tenant authority. Curated catalog first; third-party repos need
   `6/17`'s third-party posture — skill-guard scan at add, explicit
   confirm, and crackbox fail-CLOSED as the precondition.

## Ties

`5/13` (the dispatch mechanism this catalogs) · `5/14` (credentials) ·
`5/15` (OAuth broker — the auth wall) · `5/17` (two-face; arizuko's OWN
resources are the reflexive case) · `5/19` (HITL hold) · `6/12` (MCP
firewall) · `8/A` (openapi2mcp) · `6/1` (positioning) ·
[`5/26`](26-integration-usecases.md) (the corpus).
