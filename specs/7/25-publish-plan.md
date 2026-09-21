---
status: defected
defects: [L199, L200]
depends:
  [
    ../5/28-packages,
    ../5/27-compose-native-packaging,
    ../5/8-yaml-manifests,
    ../5/34-channel-protocol,
    ../6/16-daemon-standalone-matrix,
  ]
---

# specs/7/25 — the package contract

Every component is a package. The maintainer's words, 2026-09-05:

> Ship components as packages, and in the one repo for now. Others can
> however ship other packages, which are represented by processes and
> hook into the system.
>
> A package brings its own dependencies; arizuko's core is untouched.
>
> Don't have optionality — but if optionality is just disabled, that is
> good and ok.
>
> It's mostly like Hurd! But this one should work.

So the distinction between a daemon this repo ships and a package someone
installs collapses. `gitd`, the channel adapters, `ttsd`, `litellm` —
one contract, one install path. They live in this repo, which is a
repository layout choice, not a difference in kind.

**A package is a process plus a declaration of how it hooks into the
system.** The process is the compose fragment `<name>.yml` (docker's
format, included verbatim, `5/27`); the declaration is the manifest
`<name>.yaml` (arizuko's format, `5/8` rows, installed per `5/28`) plus
an optional `skills/` tree. Two files because two owners define the
formats.

## The acceptance test

**Could someone outside this repo ship a package that satisfies the
contract, without patching arizuko?** If a package must add a Go import,
edit a switch, or land a file the binary compiles in, it is internal
wiring wearing the word "package". Every verdict below is that test,
measured at `02d0fbc1`.

Optionality follows from it. There is no build-time or flag-time
optionality: a package that is not installed is a package that is
disabled, and nothing else says `false` (`5/27` §"Opt-in by omission").

## Hurd

A Hurd translator is "a normal program acting as an object server",
attached to a filesystem node with `settrans` and implementing the fs and
io interfaces (gnu.org/software/hurd/hurd/translator.html). That is this
contract: a process, attached at a declared point, speaking the
interfaces the point defines. The maintainer's reading of Hurd is that
its contract was elegant and unsatisfiable, and it shipped no useful
subset. arizuko runs three live instances, so its rule is the reverse:
**keep shipping the useful subset while the contract catches up; never
hold the subset for the contract.** The gaps below are measured so the
contract can catch up. None of them stops a package shipping today.

## What a manifest can declare — measured

Two verbs write a package into an instance, and they do not deliver the
same declaration (`cmd/arizuko/packages.go`):

| Verb                                     | Source          | Rows applied                                                    | Skills | Routes reach proxyd via               |
| ---------------------------------------- | --------------- | --------------------------------------------------------------- | ------ | ------------------------------------- |
| `packages add <name>`                    | bundled catalog | **none** — the files are linked; `parsePkgManifests` never runs | no     | `generate` → `compose.ManifestRoutes` |
| `packages install <source> [--folder …]` | dir or git URL  | every resource `apply` rebuilds, one transaction (`L44`)        | yes    | the same reader, plus the applied row |

Through `install` a manifest may carry ten resources: `acl` (stamped
`granted_by = package:<name>`, scope bounded to `--folder`, `**` and `*`
refused — `L46`), `acl_membership`, `groups`, `network_rules`,
`onboarding_gates`, `pending_actions`, `proxyd_routes` (owner stamped,
core paths refused), `routes`, `scheduled_tasks`, `web_routes`. Six
registered resources are refused because `apply` never rebuilds them:
`secrets`, `route_tokens`, `invites`, `onboarding`, `installed_packages`,
`github_sources`. `5/28` lists five; `github_sources` is the sixth
(`resreg/resources/github_sources.go`).

Through `add`, the verb every shipped package ships through, one key
reaches the instance: `proxyd_routes`. `compose.ManifestRoutes` reads
that key and `continue`s past any other (`compose/compose.go:513-516`),
so a catalog manifest declaring a scheduled task or a network rule
parses, links, prints "run `arizuko generate` to apply", and delivers
nothing — a silent no-op on an operator path (BUGS `L199`).

The ten shipped manifests (`template/services/*.yaml`: bskyd, discd,
emaid, gitd, linkd, reditd, slakd, teled, twitd, whapd) declare
`proxyd_routes` and nothing else. That is not a coincidence; it is the
ceiling of the verb they ship through. `litellm`, `kokoro` and `ttsd`
ship a fragment and no manifest.

`5/8` says the manifest carries "ACL, routes, secrets metadata,
scheduled tasks, proxyd routes, web routes, network rules, group
registration". That is true of the instance manifest `export` and
`apply` move. For a package it is true on the `install` verb only, and
only for the ten resources above.

## What a manifest cannot declare — measured

A package's process hooks in at five points that are names in Go, not
rows in a manifest. Each is a place the outsider test fails.

| Hook                                     | Where the name lives                                                            | What an outsider gets without it                                                                                                                                                                       |
| ---------------------------------------- | ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| a service principal (`service:<daemon>`) | `compose.adapterDaemons` (`compose/compose.go:250-254`) → `wireServiceKey`      | no `AUTHD_SERVICE_KEY`; `POST /v1/channels/register` answers 401 under a Verifier (`routd/channels.go`, `channelAuth`)                                                                                 |
| an env file with named keys              | `compose.daemonKeys` (`compose/compose.go:98-238`) → `writeEnvFiles`            | no `env/<name>.env`; a fragment that names one fails `compose up` (compose's `env_file` `required` defaults to true); `envFileName`'s fallback hands the service the shared `.env`, every secret in it |
| env on a core daemon                     | `hasService(services, "ttsd")` (`compose/compose.go:788`) sets `TTS_*` on runed | a TTS package under another name enables nothing; the operator sets the vars by hand                                                                                                                   |
| a core-declared route                    | `/dash/ttsd/` in `compose.coreProxydRoutes` (`compose/compose.go:369`)          | the adapter dashboards declare theirs in the manifest; ttsd's is core's                                                                                                                                |
| a hub tile                               | `dashd/services.go:57-67`                                                       | the `/dash/<name>/` route works; the hub does not list it                                                                                                                                              |

> **Correction (2026-09-21, at `f8b82a90`).** Rows 3 and 4 of the table are
> closed. `hasService("ttsd")` is gone (`166a0223`): `ttsd.yaml` declares
> `TTS_ENABLED` and `TTS_BASE_URL` as `env` defaults, so "env on a core
> daemon" is a manifest key. `/dash/ttsd/` moved to `ttsd.yaml`
> `proxyd_routes` (`f8b82a90`), so `coreProxydRoutes` holds core daemons
> only and ttsd fails the outsider test on `daemonKeys` alone. One hook the
> table missed: `authd/http.go` `serviceGrants` — a `service:<name>` outside
> it mints a token with no scope and every inbound 403s, so the principal
> row lives in two tables (`compose.adapterDaemons` and this), and
> `c0594f32` added a line to each. The channel layer is therefore three
> rows from passing, not one map edit. And since `d1d857b9` a fragment from
> a source is held to its own name — no `env_file`, binds under
> `${DATA_DIR}/store/<name>` only, no namespace escape — so the "shared
> `.env`" failure in row 2 is a refusal at install (BUGS `L201`).

Two packages hook in through a database file as well. `gitd` mounts
`store/routd/` and opens `routd.db` for `github_sources`
(`gitd/main.go:34`); `slakd` mounts it for `pane_sessions`
(`slakd/main.go:47`). A mount of another owner's database is a coupling,
not a declaration: it needs the owner's schema, the owner's `SECRETS_KEY`
(`daemonKeys["gitd"]`), and in gitd's case a resource, a store package, a
migration and a CLI verb in core. Commit `c0594f32` touched
`cmd/arizuko/github.go`, `compose/compose.go`,
`resreg/resources/github_sources.go`, `store/github_sources.go` and
`store/migrations/0088` to add one adapter. `5/27` says "adding an
adapter touches no Go"; the measurement says five files of it.

The wire contract is not the problem. `5/34` is language-neutral —
register over HTTP, `POST /v1/messages` in, `/send` out — and `whapd` and
`twitd` prove it from TypeScript with no Go import. What fails the
outsider test is the deployment contract: two maps in `compose.go` decide
who gets a principal and an env file, and a manifest cannot say "I am a
channel adapter" or "hand me these keys" (BUGS `L200`, proposal).

## The components against the contract — measured

Direct imports from `go list -f '{{join .Imports "\n"}}'`; transitive
from `go list -deps`; lines are non-test Go or TS under the directory.

| Component                                | Lines         | Direct arizuko imports                                                                                                 | Process         | Hook                                                                                                              | Outsider test                                                                                                                                                                                                     |
| ---------------------------------------- | ------------- | ---------------------------------------------------------------------------------------------------------------------- | --------------- | ----------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `litellm`                                | image         | —                                                                                                                      | yes             | fragment; `MODEL_GATEWAY_URL/KEY` read generically (`core/config.go:175`, `container/runner.go:688`)              | **passes** — the one package that does; the two env vars are the operator's                                                                                                                                       |
| `teled` `discd` `bskyd` `reditd` `linkd` | 884–1,106     | `chanlib`                                                                                                              | yes             | fragment + manifest + service key by name                                                                         | fails on `adapterDaemons` + `daemonKeys` only                                                                                                                                                                     |
| `emaid`                                  | 1,276         | `chanlib`, `db_utils`                                                                                                  | yes             | same                                                                                                              | same                                                                                                                                                                                                              |
| `whapd` `twitd` (TS)                     | 2,126 / 1,318 | none — HTTP only                                                                                                       | yes             | same, own image                                                                                                   | same; proves the wire half                                                                                                                                                                                        |
| `slakd`                                  | 2,177         | `chanlib`, `store`                                                                                                     | yes             | same + `routd.db` mount                                                                                           | fails on the maps and the mount                                                                                                                                                                                   |
| `gitd`                                   | 676           | `chanlib`, `core`, `store`                                                                                             | yes             | same + `routd.db` mount + a core resource, store, migration and CLI verb                                          | fails on all of it                                                                                                                                                                                                |
| `ttsd` + `kokoro`                        | 191 + image   | `auth`, `chanlib`, `obs`, `theme`                                                                                      | yes             | fragment; `hasService("ttsd")`; core route `/dash/ttsd/`; `daemonKeys`                                            | fails on three name switches                                                                                                                                                                                      |
| `egress`                                 | 667           | none                                                                                                                   | yes, per spawn  | `EGRESS_IMAGE`; runed passes `--listen --client --allow` and probes `/health` (`container/egress.go:252-257,286`) | passes as a process — a replacement image with the same flags needs no Go edit; runed's one Go import from it is the `/health` path constant. `store` and `routd` import `egress/match`, so the matcher is core's |
| `mcpfw`                                  | 370           | none                                                                                                                   | yes, standalone | **none** — no fragment, env var, daemon or `ant` reference (`6/12`)                                               | vacuous: a tool with no hook is not a package of this system                                                                                                                                                      |
| `obs`                                    | 547           | none                                                                                                                   | no              | imported by every daemon                                                                                          | not a package; a library a package may vendor                                                                                                                                                                     |
| `chanlib`                                | 2,513         | `auth`, `db_utils`, `obs`, `theme`; transitively `core`, `store`, `router`, `groupfolder`, `audit`, `egress/match` too | no              | the Go SDK for `5/34`                                                                                             | not a package; the SDK. A Go outsider who imports it vendors the platform; the contract does not require it                                                                                                       |
| `resreg`                                 | 5,591         | `audit`, `auth`, `store`; nine transitively                                                                            | no              | the engine that reads manifests                                                                                   | not a package; core                                                                                                                                                                                               |
| `groupfolder`, `substrate`               | 204, 71       | none                                                                                                                   | no              | core types                                                                                                        | not packages                                                                                                                                                                                                      |

Three readings:

- **Libraries are not packages.** `obs`, `chanlib`, `resreg`,
  `groupfolder`, `substrate` have no process and hook into
  nothing; they are what a package vendors, or what core is. "Publishable
  library" is `tests/components/imports_test.go`'s question — `go list`
  decides it — and it is a different question from this spec's. The
  single `go.mod` stays: the import graph enforces library orthogonality,
  and a module split would trade an enforced rule for repositories to keep
  in step. `6/16`'s "zero domain coupling" for `chanlib` holds for direct
  imports only; `auth` pulls `store`, `core` and `router` behind it.
- **Import purity does not predict the outsider test.** `mcpfw` imports
  nothing and hooks into nothing; `litellm` is a third-party image and is
  the only package that passes; `teled` imports `chanlib`, transitively
  half the tree, and fails only on two maps. The test is about the hook,
  not the import graph.
- **The channel layer is one map edit from passing.** Nine adapters share
  a language-neutral wire contract and fail on the same two names. That is
  the cheapest gap in the table, and the one that turns the adapter fleet
  into packages an outsider can add.

## Ties

`5/28` (install, upgrade, remove; the installed record) · `5/27` (the
fragment; opt-in by omission) · `5/8` (the manifest rows) · `5/34` (the
wire contract every adapter package satisfies) · `6/16` (the standalone
matrix; its import axis is the library question, not this one) · `6/12`
(mcpfw) · `6/23` (litellm) · `6/24` (egress per spawn) · `7/22`
(a package may be Rust because the contract is a process) · `7/23` (the
package as the habitat's participant shape) · BUGS `L44` (rows are one
asset kind), `L46` (a package's authority is what the operator handed
it), `L137` (sandbox folds into runed), `L199`, `L200`.
