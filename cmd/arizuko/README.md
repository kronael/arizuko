# cmd/arizuko

CLI entrypoint. Builds the `arizuko` binary.

## Purpose

Operator-facing commands: instance creation, compose generation, group
and gate administration, status. Runs locally (no daemon dependency
beyond `docker` for `run` and `pair`).

## Entry points

- Binary: `cmd/arizuko/main.go` → `./arizuko`
- Commands:
  - `arizuko create <name> [--product <product>]` — seed the data dir and write its `.env` from create's own key set (`ASSISTANT_NAME`, `API_PORT`, a generated `AUTH_SECRET` + `SECRETS_KEY`, `ARIZUKO_DEFAULT_PRODUCT`). It writes no egress switch — the turn substrate's tap accepts only its own proxy by construction, so there is nothing to turn off. It does NOT read `template/env.example` — that file is the operator's reference for the remaining knobs. `create` writes files and no row: at create time no store server exists. `--product` declares the product as `ARIZUKO_DEFAULT_PRODUCT`, copies its skills and facts from `ant/examples/<product>/` into the default group's tree and prints its `[[env]]` list. routd writes the `main` group row with that product at its first boot (`store.SeedDefaultGroup`, called from `routd/db.go:75`)
  - `arizuko generate <instance>` — write `docker-compose.yml` + the compose-managed `.env` block. Also relinks any installed fragment that is still a byte-identical copy of the bundled catalog into a symlink there (`compose.RelinkCatalog`) — no flag, runs on every generate/deploy. A fragment that has diverged (a hand edit, even comment-only) or whose filename encodes a multi-account variant (`<kind>-<label>.yml`) is left as a real file, never touched. Requires `HOST_APP_DIR` in `.env` to be a host-resolvable path — see `compose/README.md` "Packages"
  - `arizuko packages <inst> list | add <name> [--folder <scope>]... | install <source-dir> [--folder <scope>]... | upgrade <name> | sync | remove <name>` — `add` and `install` run one install (`installPackage`): validate the whole `<name>.yaml` manifest, provision its `env` block against `.env` (mint a `generate` key, refuse an unset `required` one), place the files, apply the rows through `arizuko apply`'s own path, record an `installed_packages` row so `remove` withdraws exactly the rows it names. `add` takes the package from the bundled catalog and links its files there (a copy with a warning when `HOST_APP_DIR` isn't set); `install` takes a git URL or local dir and copies, and holds its fragments to `compose.CheckSourcedFragment` — own-name services, `${DATA_DIR}/store/<name>` or named volumes only, no `env_file`, no namespace escape — plus no file that would replace one `services/` already holds and no core service's name (BUGS L201). `upgrade` applies the same bound to the new revision, refuses a dirty asset and leaves a catalog record alone, since its files follow the catalog
  - `arizuko packages <inst> sync` — `upgrade` over every installed package in one pass: re-applies each record's fragment from its recorded source, reports `updated` / `unchanged` / `SKIPPED`, and writes nothing for a package already at its recorded revision. A locally edited asset is reported and skipped rather than fatal, so one hand-edited fragment can't block the rest of the instance. Not to be confused with `generate`'s catalog relink above — that converts an identical copy into a symlink and never reads the installed-package record
  - `arizuko products <inst> list` — the bundled catalog under `<HOST_APP_DIR>/ant/examples`, one row per product: `name`, `brand`, `tagline` from its `PRODUCT.md`. A directory whose manifest won't parse is listed as such rather than omitted, so a broken manifest can't read as "not shipped". These are the names `arizuko create --product <name>` accepts; the published pages under `/pub/arizuko/products/` are a different, overlapping set (BUGS F64). Four pages, `company-brain`, `discord-team`, `marble` and `supermarket`, describe a shape and have no template, so `create --product` refuses their names
  - `arizuko products <inst> apply <folder>` — blend the ordered product mix declared in `groups/<folder>/products.toml` (`[[product]]` blocks with a `source =`; relative sources join `HOST_APP_DIR`) into that group, per payload kind, and record one `installed_packages` row per product under `(folder, name)` — the only writer of a non-empty folder. Idempotent; a managed asset edited since the last apply is reported and skipped, never overwritten. `packages sync` re-applies every group's mix too
  - `arizuko proxy-config <instance> [--listen :8080] [--forward-auth <host:port>]` — render the live route table as a Caddyfile. Without `--forward-auth` every authenticated route renders as a 503 rather than an open hop
  - `arizuko route <inst> list | add <match> <target> [--seq N] | rm <id>` — the message-routing table. Writes go through the audited `AddRoute`/`DeleteRoute`, so the route and its audit row commit together; `AsCLI` attributes the row to the invoking operator, since the CLI has no `X-User-*` header to learn a sub from
  - `arizuko probe <base-url> [--token T] [--foreign-folder F]` — check a DEPLOYED component against the environment contract (`6/1`). Go tests read the source; this reads the instance, which no test can — a daemon passes every test and still ships with `openapi.json` behind auth, or a gate unwired. It detects paths the doc advertises and the mux does not serve, never the reverse: proxyd answers every path from a `/` catch-all, so "it responded" never proves "this resource is mounted here" (BUGS `F33`)
  - `arizuko run <instance>` — generate + `docker compose up`
  - `arizuko status <instance>` — show compose services + channels
  - `arizuko pair <instance> <svc>` — `docker compose run --rm`
  - `arizuko group <inst> list | add | rm | grant | ungrant | grants`
  - `arizuko group <inst> grant|ungrant <sub> <pattern> [action] [--effect allow|deny] [--params <matcher>]` — one acl row, named by its whole coordinate. `store.aclKey` joins principal, action, scope, params, predicate and effect, so a revoke naming fewer fields addresses a different item and deletes nothing. `--effect deny` on a `hold:mcp:<tool>` action IS the skip-approval rule `/always` writes; `--params` carries the argument matcher it was narrowed to, and `grants` prints that column. `<sub> ** admin` with no params stays the operator-role shortcut, both ways
  - `arizuko gate  <inst> list | add | rm | enable | disable`
  - `arizuko network <inst> allow|deny <folder> <target> | list | resolve <folder>` — per-folder egress allow/deny rules
  - `arizuko secret <inst> set <folder> KEY --value V | list <folder> | delete <folder> KEY | seal-legacy [--dry-run]` — folder-scoped secrets; `seal-legacy` encrypts any still-unsealed row in place (rehearse with `--dry-run` — it rewrites the attribute every credential lives in)
  - `arizuko user-secret <inst> set <user_sub> KEY --value V | list <user_sub> | delete <user_sub> KEY` — user-scoped secrets
  - `arizuko token <inst> issue chat <folder> [<suffix>] | issue webhook <folder> <label> | list <folder> | revoke <jid> [<owner_folder>]` — manage `route_tokens` (chat / webhook capability URLs)
  - `arizuko github <inst> add <repo|org|app> <name> <folder> | list [<folder>] | rm <kind>/<name> <folder>` — manage `github_sources` (gitd webhook secrets, spec `6/27`): `add` prints the delivery URL + secret once and seeds the route rows that deliver the source's events to `<folder>`
  - `arizuko gdrive <inst> add <file_id> <folder> | list [<folder>] | rm <file_id>` — manage `gdrive_files` (googd bindings, spec `6/45`): `add` binds one Drive file and seeds the route rows that deliver its comments to `<folder>`; share the file with googd's service account first
  - `arizuko token <inst> issue bearer <folder> --scope|-s s1,s2 [--ttl|-t 1h] [--sub SUB]` — mint a folder-scoped ES256 access JWT signed with authd's active key (read from authd's `signing_keys`; operator-only). E.g. `--scope messages:write,messages:read` for anteval's inject+inspect
  - `arizuko invite <inst> create <target_glob> [--max-uses N] [--expires DURATION]`
  - `arizuko invite <inst> list [--issued-by SUB]`
  - `arizuko invite <inst> revoke <ref>`
  - `arizuko send <inst> <folder> [<message>] [--wait | --stream] [--stdin] [--from <sender>] [--topic <topic>] [--token <raw>]` — inject a message into a folder's queue (uses topic for conversation continuity). Default is **operator-direct**: no token, writes the inbound straight to routd's tables on `web:<folder>` (the operator already reaches them, same authority as `create`/`grant`/`secret`); the gateway poll loop runs the agent and `--wait`/`--stream` prints its reply. Pass `--token`/`ARIZUKO_CHAT_TOKEN` to instead POST the public `/chat/<token>` endpoint as a non-operator caller.
  - `arizuko budget <inst> set folder|user <name> --daily N` / `show folder|user <name>` — per-folder or per-user daily spend cap in cents (0 = uncapped); pre-spawn gate enforces lower of (folder cap, user cap)
  - `arizuko apply <inst> <manifest.yaml> [--force] [--take-over] [--as-folder <folder>]` — restore cold-tier config from a YAML dump, one claim-gated replace per owner subsystem, with a pre-image rollback if a later one fails. CAS is a content hash recomputed from the live rows, not a stored counter; `--force` skips it. Refuses (even with `--force`) if a row names a folder that is not a group. `--as-folder` re-scopes a single-folder manifest onto another folder. An entity-backed subsystem an interrupted apply left part-written keeps its claim, and re-running THAT manifest rolls it forward on its own; `--take-over` is for the operator who cannot, and it settles the scope over whatever the interrupted apply left outside this document's delete set (`L267`)
  - `arizuko plan <inst> <manifest.yaml>` — non-mutating diff of a manifest vs live config; also reports a missing-group refusal `apply` would raise
  - `arizuko get <inst> <resource>` — emit one resource's live rows as a YAML fragment (round-trips to a no-op)
  - `arizuko export <inst> [out.yaml]` — dump cold-tier config to canonical-ordered YAML
  - `arizuko archive export <inst> [out.tar] [--quiesced]` — full-instance backup: the config dump plus secret values, message history, pending onboarding admissions and every `groups/<folder>/` tree, as one tar
  - `arizuko archive apply <inst> <archive.tar> [--force] [--stopped]` — full restore. Claims each folder's runed run slot before touching its tree, so an unreachable runed is fatal; `--stopped` asserts the instance is down instead. Credential-bearing documents (`route_tokens`, `invites`, `onboarding`) restore only with `--force` onto a proven-empty target
  - `arizuko chat <instance>` — interactive Claude Code session bound to root MCP socket
  - `arizuko talk [@name] [-g <folder>] [-t <thread>] [-p [prompt]] [--url U] [--token T]` — hold a conversation with the agent from a terminal, spec `6/26` gap 1. No instance argument and no store: it POSTs `<url>/mcp` (`list_groups` + `send`) and reads the round off `<url>/me/chats/<folder>/<thread>/sse`, both through the PUBLIC proxyd URL, so it runs from a second machine. Config `~/.config/arizuko/talk.toml` (`url`/`token`/optional `group` at the top level are the one chat; a `[work]` section is `@work`), refused unless mode `0600`. Precedence flag > `@name` > top-level > `ARIZUKO_CHAT_URL`+`ARIZUKO_CHAT_TOKEN` > an error that prints the file to write. `-p` runs one turn (prompt inline or on stdin) and is ONE ITERATION of the REPL loop, never a second send path; `-t` defaults to the fixed literal `talk`. Note `-t` is the THREAD here, not the token as in `send` — the bearer flag is `--token` only

**`send` vs `chat` vs `talk`** — three ways to reach an agent, and they are not interchangeable:

- `send` — **operator-direct injection**. Opens the instance's store and writes the inbound itself; needs the data dir, runs on the host, is the operator's own authority.
- `chat` — **an interactive Claude Code on the ROOT MCP socket**. An operator tool for driving the platform, not a conversation with the persona.
- `talk` — **a thin token client, no store**. Two HTTP surfaces and an authd bearer; the only one that works from a machine that is not the host.

### Manifests and the archive

- **The store is the truth; YAML is transport.** `export` dumps, `apply`
  restores, and nothing syncs the store back to a file, applies at startup
  or reloads on SIGHUP. A dump never claims to be live, so "drift" between a
  file and the store is not a state anyone has to manage.
- **`apply --as-folder <f>`** needs `--force`, because a rewritten manifest
  can never match the target's checksum. The manifest's scoped rows must
  name one folder; the empty scope counts as a folder of its own, so an
  instance-wide `network_rules` row is never turned into one folder's. It
  rewrites the declared scope field of `groups`, `network_rules`,
  `pending_actions` and `web_routes`, plus `web_routes.redirect_to`, which
  embeds the folder; rows of resources with no scope pass through as they
  are. It moves no files: copying a group is `export` plus `apply
--as-folder` for the rows and a copy of `groups/<folder>/` for the files.
  Routing never creates a group.
- **The archive** is one tar: `archive.yaml` (`format_version` and the
  `consistency` stamp, `live` or `quiesced`), `routd.yaml`, `onbod.yaml`,
  `routd.secrets.yaml` (secret, route-token, invite and pending-onboarding
  values, kept apart so a plain `export` reader never sees one),
  `routd.messages.jl` and `groups.tar`. authd's and runed's stores are not in
  it. `archive apply` refuses a `format_version` newer than the binary.
- **Messages travel whole.** `routd.messages.jl` is the full history, and
  importing it is an idempotent append. Import derives each chat's
  `agent_cursor` from its newest message, because without it the restored
  poller would treat all history as unseen. Secret values travel sealed,
  `SECRETS_KEY` never travels, and every secret row is checked against the
  target's keyring before any write.

## Dependencies

- `compose`, `container`, `core`, `routd` (installed-package record), `store`

## Files

- `main.go` — command dispatch + `create`/`generate`/`run`/`status`/`pair`/`group`/`gate`/`invite`/`chat`
- `route.go` — `route`, the audited message-routing table CLI
- `routes.go` — `proxy-config`, the Caddyfile renderer
- `probe.go` — `probe`, the deployed-component contract check
- `skills.go`, `flags.go` — the skill enumerator and the shared flag parser
- `apply.go` — `apply`/`plan`/`get`/`export` (YAML manifests, spec 5/8), the missing-group preflight, and the cross-subsystem pre-image rollback both `apply` and `archive apply` share
- `archive.go` — `archive export`/`archive apply` (full-instance backup, spec 5/8)
- `retarget.go` — `apply --as-folder`, the recipe over `resreg.Resource.Retarget`
- `budget.go` — `budget` spend caps
- `network.go` — `network` egress rules
- `packages.go` — `packages` catalog `add` + source-based `install`/`upgrade`/`sync`/`remove` with the `installed_packages` record; `reapplyPackage` is the single updater `upgrade` and `sync` share
- `products.go` — `products list`, the catalog reader, + `products apply`: the `products.toml` reader and the group-scoped half of the `installed_packages` record; the blend itself is `container/blend.go`
- `secret.go` — `secret` + `user-secret`
- `send.go` — `send` message injection, plus the SSE reader (`openSSE`/`readSSE`/`handleSSEFrame`) that `talk` shares
- `talk.go` / `talk_config.go` — `talk`: argument grammar, the MCP+SSE turn, and the `~/.config/arizuko/talk.toml` reader
- `token.go` — `token` route-token management
- `github.go` — `github` webhook-source management for gitd (spec `6/27`)
- `gdrive.go` — `gdrive` file-binding management for googd (spec `6/45`)

## Coverage

`go test ./cmd/arizuko/ -short -cover` reports 57.8% (2026-09-13), and the gap
is one shape.

It covers the parsing, planning and wire logic: the
manifest readers, the apply plan, the package resolver, the `.env` parser, and
both `send --wait` paths (`streamRound` over SSE, `pollRound` as the fallback). `talk` is covered end to end against a real streamable-HTTP MCP server plus an SSE stub (`talk_test.go`): argument grammar, config precedence and the 0600 refusal, folder resolution, and one turn on the wire.

It does NOT cover the `cmdX` subcommand entry points, and cannot in-process:
each one ends at `die()` (`main.go:110`), which calls `os.Exit(1)`. A test that
reaches a `die` takes the whole test binary with it. That is a deliberate CLI
shape — an operator wants a status code and one line on stderr, not an error
value threaded back to `main` — so the entry points are exercised end to end by
`make test-e2e` and by `tests/`, not by unit tests here.

So the uncovered 42% is thirty `cmdX` entry points, not thirty untested
decisions: each is an argument unpack in front of a function that IS covered.

## Related docs

- Top-level `README.md` (Quick Start)
- `ARCHITECTURE.md`

### Offline group rename

Stop all instance writers, ingress, guests and writable staging mounts. Keep
only the database service running. Take a complete database backup and retain
mutable files and keys. A config archive omits runtime state.

```bash
arizuko group sloth rename main sloth --map main/main=sloth/discord \
  --inventory /private/rename-inventory.json \
  --endpoint https://127.0.0.1:18444 --dry-run
arizuko group sloth rename main sloth --map main/main=sloth/discord \
  --inventory /private/rename-inventory.json \
  --endpoint https://127.0.0.1:18444 --stopped --yes \
  --journal /private/sloth-rename
```

The inventory is a private `0600` JSON document from the stopped database's
complete table catalog. `owners` maps each owner to every table, then each
partition to its exact item count. Include empty tables as `{}`. This evidence
supplies partition enumeration; the CLI reads and writes through the object
store. The inventory also carries `instance`, `endpoint`,
`configured_endpoint`, `ca_sha256`, and `key_ids_sha256` (owner to credential-ID
hash). Hash decoded `STORE_CA` bytes and each `AWS_ACCESS_KEY_ID` with SHA-256.
`configured_endpoint` equals generated `DYNAMODB_URL`; `endpoint` equals the
operator URL passed on the command line. Every `env/<owner>.env` must identify
the selected instance through `ARIZUKO_INSTANCE`. Credentials come from those
files, with no process-environment override.

Preflight checks table coverage, counts, destination collisions, active records,
mounts, sockets and unsupported references. More specific mappings use original
coordinates. Platform JIDs, including legacy `local:` feeds, opaque IDs,
ciphertext, cursor positions, historical prose and audit evidence retain their bytes. Directories merge only
without leaf collisions; unrelated destination files remain intact. Workspace
files move with their directory. The CLI updates symlink targets, operational
web URLs, absolute workspace paths and declared agent settings. It refuses
unclassified folder settings. Unqualified route `room` operands retain platform
room names; only explicit `platform=web` or `platform=hook` room operands move.
Hook JIDs and explicit hook rooms retain their source labels.
Coordinate operands in ACL Params and Predicate require exact values; globs
refuse preflight, including on unchanged ACL rows. When a token JID changes,
preflight compares its parsed folder with its recorded target. If the target
is set and differs, preflight rejects the ambiguous alias.

Execution creates a private `0700` journal outside the instance directory.
`plan.json` contains complete before/after item images and changed file bytes.
`progress.jl` records durable batch boundaries. Cross-owner work is not one
transaction. If execution stops, keep the instance fenced and repeat the exact
command with `--resume` or `--rollback`. Keep the inventory and original mapping
unchanged. Rollback refuses drift. Verify routing, permissions, feeds, secrets,
sessions and files before restarting. Signed JWT claims need fresh login if
those claims carry an old folder.

`arizuko chat <instance> --folder <folder>` selects the group's IPC socket;
the default folder is `main`.
