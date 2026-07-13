# USELESS.md — the case against arizuko

This file argues arizuko is overengineered and mostly reinvents what
already exists. It is kept in-repo on purpose — read it before believing
README.md. Method: every system gets exactly one orthogonal component;
the rest is filler. Applied to ourselves, adversarially, by three
parallel critics (two Claude agents + a codex pass; codex was
unreachable this round — noted for honesty, not padded over). A
counter-section follows, then the parts that survive it. Numbers verified
against the repo 2026-07-13.

The honest headline is **not** "delivers nothing new" — that's a strawman
this file refuses (§4 says why). The true indictment is harsher in a
different direction: arizuko is **behind** simpler systems on most of what
a user touches, and its "extra" is largely infrastructure to manage
complexity it chose to create.

## 1. Someone already does each piece — often better, with less

**Channels — the flagship, and the most bloated.** Hermes speaks the
same six chat platforms (Telegram, Discord, Slack, WhatsApp, Signal, CLI)
from **one gateway process**. arizuko runs them as **separate daemons**
(teled, discd, slakd, mastd, bskyd, reditd, whapd, …), each with its own
compose service, port, healthcheck, systemd unit, and service-token
exchange. The daemon-per-adapter pattern buys **nothing** Hermes' single
process doesn't already do — and it manufactures its own bug class: the
`adapter_service_name` 401 outage, the `SECRETS_KEY` crash-loop landmine,
the "restart proxyd → gated recreated" side effect are all _products of
the daemon count_, not independent defects.

**Skills — we're behind, not ahead.** Hermes ships a skills marketplace
with trust tiers (builtin > official > trusted > community), version
stamping, progressive disclosure (cheap listing → detailed → full), and
an ~850-LOC threat scanner gating installs. arizuko's skills are flat
markdown, eagerly loaded at boot, no provenance, no scanner — the port is
a _spec_ (`specs/12/…`), not code. Our own memory admits it: "only the
SECURITY pieces transfer cleanly" and lists four Hermes features we lack.

**Secrets boundary.** NemoClaw/Centaur/IronClaw keep the key out of the
sandbox entirely (host-side inject, WASM tools that never see values).
arizuko injects secrets **into** the container at spawn — prompt injection
can read ours; it cannot read theirs. The stronger boundary (egred
HTTPS-MITM swap) is a spec, not code.

**Container isolation.** NanoClaw shipped container-by-default first;
Muaddib uses QEMU micro-VMs; Edera runs a dedicated kernel under Xen. We
share the host kernel and one docker daemon and call mid-spectrum a
security model.

**Grants DSL.** Our `[!]action(param=glob)` is bespoke, and we already
logged OPA/Rego as its replacement candidate (2026-05-22). A policy
language you plan to replace is scaffolding.

**Scale.** ElizaOS scales horizontally (Postgres + pgvector). We are one
host, SQLite WAL, single-writer per DB. The ceiling is one box, and it is
load-bearing.

## 2. The overengineering charges (numbers, 2026-07-13)

- **21 daemons** for a single-host system — and the count is going _up_,
  not converging. Every daemon is a token exchange, a healthcheck, a
  failure mode.
- **5 independently-migrated SQLite schemas** (store 74 migrations, routd
  19, authd 4, runed 2, onbod 2) for one logical conversation.
- **88k non-test Go LOC** to relay chat into a Docker container running
  Claude Code CLI.
- **Every cold-tier resource declared twice, by design** — 20
  `*_resource.go` handlers vs 40 `resreg/resources/*.go` declarations. Our
  own memo (`openapi_two_declarations`) flags this as unreconciled drift
  risk. The `resreg` reflection layer exists _specifically_ so MCP and
  REST can't drift — and drift happened anyway (proxyd's live resource
  said `routes` while its catalog said `proxyd_routes`, fixed 2026-07-01).
  This is table-stakes "REST wraps MCP" plumbing sold as a design
  principle with its own spec.
- **233 spec files, 174 skill-migrations (v173), ~99 config env vars,
  1,184 lines of AI-maintainer CLAUDE.md** (473 root + 711 `ant/`) — before
  a reader touches code. Specs are cheap to write and expensive to keep
  true; several are the promissory notes the security/tenancy pitch rests
  on (egred, git-as-truth, tenant self-service — all unshipped).
- **62 open bugs / 1,264-line BUGS.md**, not consistently triaged. The
  live ones indict the design directly:
  - **The codebase violates its own rules in production.** CLAUDE.md
    mandates "No duplication — amend the original." As of today there are
    **two structurally different `cost_log` tables** (routd's vs store's),
    flagged in-file as a policy violation awaiting consolidation.
  - **The complexity's own security regression.** Codex-in-container broke
    because the operator's `0600` creds aren't readable by the container's
    uid-1000 — "unblocked" today by `chmod o+r`-ing a ChatGPT OAuth token
    **world-readable**. The container-uid isolation created the problem;
    the stopgap undoes it.
  - **Onboarding is still manual.** README/ARCHITECTURE describe automated
    admission; the HIGH-severity live entry is "new chat → route-miss →
    silence → hand-run `arizuko group add`" — per new user, today.
- **Architecture churn as a failure factory.** The gated monolith (~8,000
  LOC) was deleted three months after it worked. The split that replaced
  it **silently dropped every agent reply for ~2 days** (fixed 91937baf) —
  routd recorded the turn and discarded the prose while **healthchecks
  stayed green**. And a documented mount feature (`MOUNT_ALLOWED_ROOTS`)
  turned out **never plumbed through compose generation** — dead
  platform-wide, unnoticed until 2026-07-13. That is what N daemons buys:
  contracts between them that slip silently.

The pattern: complexity is paid **daily**; several headline benefits are
**promissory** (specs, not code).

## 3. Validation, honestly

Three instances — krons, sloth, marinade — **all operated by the author.
Zero known external installs.** The validated-usefulness cell reads:
autobiographical. 21 daemons, 5 databases, 233 specs, to serve a fleet of
one operator's three boxes.

## 4. Where the critique fails (why "nothing new" is a lie)

**The folder coordinate is real and nobody else has it.** One primitive —
the folder path — is simultaneously tenant, ACL scope, route target,
egress scope, web host, and file tree. Run the four lenses across the
survey: OpenClaw has no isolation; NanoClaw is group-safe but
single-user; Muaddib's channels share a VM; ElizaOS scales but runs
plugins in-process; **Hermes' profiles are not a tenant boundary**;
IronClaw is explicitly single-owner. **Multi-tenancy is the empty column
of the entire survey.** arizuko is the only system that serves sibling
tenants with per-tenant container + per-tenant egress allowlist +
per-tenant ACL on one box, same code from `solo/inbox` to
`corp/eng/sre/oncall`. That is the orthogonal component. It is real, and
it is narrow.

**Same-plane coordination.** Agents delegate/schedule/route by posting to
routd — the coordination bus _is_ the conversation plane. No surveyed
system collapses these; they grow a second bus or reject subagents.

**Agent-as-data, exercised.** An agent is a folder you can git-diff and
revert, with a working upgrade broadcast (173 migration versions shipped
over live agents). Hermes and ElizaOS _configure_ agents; they don't make
the agent BE a file tree.

**Some of the daemon count is the tenancy, not decoration.** One process =
one blast radius, which is exactly why the single-process rivals'
multi-tenancy cell is empty. _Some_ of the split is the price of the
orthogonal component. **Not all of it** — the daemon-per-adapter is not.

## 5. What still stands after the counter

1. Secrets enter container memory. NemoClaw/Centaur/IronClaw prove the
   stronger boundary is buildable. Ship egred or stop claiming secrets is
   a strength.
2. One host, one kernel, SQLite ceiling. Real tenants will hit it.
3. One runtime (Claude Code). Hermes routes on cost; brainpro routes per
   task. We cannot.
4. **We are behind on skills** — no marketplace, no versioning, no
   scanner, no progressive disclosure. Hermes shipped all four.
5. Validation is autobiographical. One stranger running one instance is
   worth more than the next five daemons.
6. Spec debt: the security and tenancy pitch leans on unshipped specs
   (egred, git-as-truth, tenant self-service). Until they ship, parts of
   README.md §Direction are marketing by other means.

**Verdict: not useless — misweighted, and behind.** Over-built for the
proven audience (one operator, three boxes), under-proven for the
designed audience (multi-tenant fleets), and lapped by simpler systems on
skills, secrets, and channel plumbing. Exactly one thing — the folder
coordinate — is genuinely novel and unfilled by the field; it does _not_
justify 21 daemons and 5 databases at the current scale. Everything that
doesn't serve that coordinate is a candidate for deletion; everything
that does deserves the next line of code. Delete first, then earn the
complexity back one real external tenant at a time.
