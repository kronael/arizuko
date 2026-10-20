# cmd/arizuko

cmd/arizuko builds the `arizuko` binary, the command line of the operator. It creates an instance and generates its compose file. It also administers groups, grants, secrets, routes, packages and cold-tier config.

## Where the CLI runs

The CLI runs on the host of the instance. `instanceDir` resolves the data directory `/srv/data/arizuko_<name>`, or `$ARIZUKO_DATA_DIR/arizuko_<name>`, or `$PREFIX/data/arizuko_<name>`. It then loads the `.env` of the instance into its own environment. A command that reads or writes rows opens the store of the owner directly through `DYNAMODB_URL`. So the store server must run, and the owner daemon must have booted once. Most commands call no daemon. These commands need more:

- `run`, `pair` and `status` call `docker compose`.
- `chat` needs `claude` and `socat` on the path, and the IPC socket of the instance.
- `archive apply` claims the run slot of each folder at runed, unless you pass `--stopped`.

## Commands

The binary is `cmd/arizuko/main.go`, and the argument grammar of each command is in its source file.

Instance:

- `arizuko create <name> [--product|-p <product>]` seeds the data directory and writes its `.env` from its own key set: `ASSISTANT_NAME`, `API_PORT`, a generated `AUTH_SECRET` and `SECRETS_KEY`, and `ARIZUKO_DEFAULT_PRODUCT`. It writes no egress switch, because the tap of the turn substrate accepts only its own proxy, so nothing exists to turn off. It does not read `template/env.example`, which is the reference of the operator for the other variables. `create` writes files and no row, because no store server exists at create time. `--product` declares the product as `ARIZUKO_DEFAULT_PRODUCT`, copies its skills and facts from `ant/examples/<product>/` into the tree of the default group and prints its `[[env]]` list. routd writes the `main` group row with that product at its first boot. `routd.Open` calls `store.SeedDefaultGroup` for this.
- `arizuko generate <instance>` writes `docker-compose.yml` and the compose-managed block of `.env`. It also relinks every installed fragment that is still a byte-identical copy of the bundled catalog into a symlink (`compose.RelinkCatalog`). This takes no flag and runs on every generate and deploy. A fragment that diverged, even by a comment, stays a real file, and so does a fragment whose filename encodes a multi-account variant (`<kind>-<label>.yml`). `HOST_APP_DIR` in `.env` must be a path that the host can resolve. See the "Packages" section of `compose/README.md`.
- `arizuko run <instance>` runs `generate`, then `docker compose up --remove-orphans`.
- `arizuko status <instance>` shows the compose services and the channels that the router reports.
- `arizuko pair <instance> <service> [args...]` runs `docker compose run --rm <service>`.
- `arizuko proxy-config <instance> [--listen :8080] [--forward-auth <host:port>]` renders the live route table as a Caddyfile. Without `--forward-auth`, every authenticated route renders as a 503 and never as an open hop.
- `arizuko probe <base-url> [--token|-t T] [--foreign-folder|-f F]` checks a deployed component against the component environment. `ARCHITECTURE.md` section "Decided, and why" describes that environment. Go tests read the source, and this command reads the instance. A daemon can pass every test and still ship with `openapi.json` behind auth, or with a gate unwired. The command detects paths that the document advertises and the mux does not serve, and never the reverse. proxyd answers every path from a `/` catch-all, so "it responded" never proves that a resource is mounted there.

Groups and access:

- `arizuko group <instance> list | add <jid> <folder> [--product <name>] | rm <folder> | grant | ungrant | grants | thread-replies <folder> <on|off|default> | rename`. `add` also writes the first route for the JID. A Discord guild channel gets a `verb=mention` rule and a catch-all `#observe` rule, so that messages without a mention only add context.
- `arizuko group <instance> grant|ungrant <sub> <pattern> [action] [--effect allow|deny] [--params <matcher>]` writes one acl row, named by its whole coordinate. `store.aclKey` joins principal, action, scope, params, predicate and effect, so a revoke that names fewer fields addresses a different item and deletes nothing. `--effect deny` on a `hold:mcp:<tool>` action is the skip-approval rule that `/always` writes. `--params` carries the argument matcher that the row was narrowed to, and `grants` prints that column. `<sub> ** admin` with no params stays the shortcut for the operator role, in both directions.
- `arizuko gate <instance> list | add | rm | enable | disable` manages the onboarding gates.
- `arizuko invite <instance> create <target_glob> [--max-uses|-n N] [--expires|-e DURATION]`, `invite <instance> list [--issued-by|-b SUB]` and `invite <instance> revoke <ref>` manage invites.
- `arizuko network <instance> allow|deny <folder> <target> | list [<folder>] | resolve <folder>` manages the egress rules of a folder.
- `arizuko budget <instance> set folder|user <name> --daily|-d N` and `show folder|user <name>` set and show the daily spend cap in cents, where 0 means uncapped. The caps live in the tables of routd. The gate before spawn pushes both caps to the model gateway, which meters the spend and refuses a request that exceeds either cap. `show` prints the caps and no spend, because spend belongs to the gateway and the CLI holds no master key.

Secrets and tokens:

- `arizuko secret <instance> set <folder> KEY --value|-v V | list <folder> | delete <folder> KEY | seal-legacy [--dry-run]` manages folder-scoped secrets. `seal-legacy` encrypts every row that is still unsealed, in place. Rehearse it with `--dry-run`, because it rewrites the attribute that every credential lives in.
- `arizuko user-secret <instance> set <user_sub> KEY --value|-v V | list <user_sub> | delete <user_sub> KEY` manages user-scoped secrets.
- `arizuko token <instance> issue chat <folder> [<suffix>] [--context|-c TEXT]` and `issue webhook <folder> <label> [<suffix>] [--context|-c TEXT]` mint capability URLs. `token <instance> list <folder>` lists `route_tokens`. `token <instance> revoke <jid> [<owner_folder>]` revokes one.
- `arizuko token <instance> issue bearer <folder> --scope|-s s1,s2 [--ttl|-t 1h] [--sub SUB]` mints an ES256 access JWT for a folder, signed with the active key of authd. The command reads the key from the `signing_keys` of authd and is for operators only. Example: `--scope messages:write,messages:read` serves the inject and inspect steps of anteval.
- `arizuko github <instance> add <repo|org|app> <name> <folder> | list [<folder>] | rm <kind>/<name> <folder>` manages `github_sources`, the webhook sources of gitd. `add` prints the delivery URL and the secret once, and it seeds the route rows that deliver the events of the source to `<folder>`.
- `arizuko gdrive <instance> add <file_id> <folder> | list [<folder>] | rm <file_id>` manages `gdrive_files`, the bindings of googd. `add` binds one Drive file and seeds the route rows that deliver its comments to `<folder>`. Share the file with the service account of googd first.

Routing and packages:

- `arizuko route <instance> list | add <match> <target> [--seq|-s N] | rm <id>` manages the message-routing table. Writes go through the audited `AddRoute` and `DeleteRoute`, so the route and its audit row commit together. `AsCLI` attributes the row to the invoking operator, because the CLI has no `X-User-*` header to learn a sub from.
- `arizuko packages <instance> list | add <name> [--folder <scope>]... | install <source-dir> [--folder <scope>]... | upgrade <name> | sync | remove <name>`. `add` and `install` run one install (`installPackage`). It validates the whole `<name>.yaml` manifest and provisions its `env` block against `.env`: it mints a `generate` key and refuses an unset `required` key. It places the files, applies the rows through the path of `arizuko apply`, and records an `installed_packages` row, so that `remove` withdraws exactly the rows the record names. `add` takes the package from the bundled catalog and links its files there, or copies them with a warning when `HOST_APP_DIR` is not set. `install` takes a git URL or a local directory and copies. It holds its fragments to `compose.CheckSourcedFragment`: services with their own name, `${DATA_DIR}/store/<name>` or named volumes only, no `env_file`, and no namespace escape. It also refuses a file that would replace one that `services/` already holds, and the name of a core service. `upgrade` applies the same bound to the new revision and refuses a dirty asset. It leaves a catalog record alone, because the files of such a record follow the catalog.
- `arizuko packages <instance> sync` runs `upgrade` over every installed package in one pass. It applies the fragment of each record again from its recorded source. It reports `updated`, `unchanged` or `SKIPPED`. It writes nothing for a package that is already at its recorded revision. It reports a locally edited asset and skips it, so one hand-edited fragment cannot block the rest of the instance. `sync` never reads the catalog relink of `generate`, which converts an identical copy into a symlink and never reads the installed-package record.
- `arizuko products <instance> list` lists the bundled catalog under `<HOST_APP_DIR>/ant/examples`, one row per product, with `name`, `brand` and `tagline` from its `PRODUCT.md`. A directory whose manifest does not parse is listed as such and not omitted, so a broken manifest cannot read as "not shipped". These are the names that `arizuko create --product <name>` accepts. The published pages under `/pub/arizuko/products/` are a different set that overlaps it. Four pages, `company-brain`, `discord-team`, `marble` and `supermarket`, describe a shape and have no template, so `create --product` refuses their names.
- `arizuko products <instance> apply <folder>` blends the ordered product mix that `groups/<folder>/products.toml` declares into that group, per payload kind. The file holds `[[product]]` blocks with a `source =`, and relative sources join `HOST_APP_DIR`. The command records one `installed_packages` row per product under `(folder, name)`, and it is the only writer of a non-empty folder. It is idempotent. It reports a managed asset that changed since the last apply and skips it, and it never overwrites it. `packages sync` applies the mix of every group again too.

Config and backup:

- `arizuko apply <instance> <manifest.yaml> [--force|-f] [--take-over] [--as-folder <folder>]` restores cold-tier config from a YAML dump. It runs one claim-gated replace per owner subsystem, with a pre-image rollback if a later one fails. The compare-and-swap token is a content hash computed from the live rows, and `--force` skips it. The command refuses a row that names a folder that is not a group, and `--force` does not override that. `--as-folder` re-scopes a single-folder manifest onto another folder. An entity-backed subsystem that an interrupted apply left part-written keeps its claim, and running that same manifest again rolls it forward. `--take-over` is for the operator who cannot run it again, and it settles the scope over whatever the interrupted apply left outside the delete set of this document.
- `arizuko plan <instance> <manifest.yaml>` shows the diff of a manifest against the live config without a write. It also reports the missing-group refusal that `apply` would raise.
- `arizuko get <instance> <resource>` emits the live rows of one resource as a YAML fragment, which round-trips to a no-op.
- `arizuko export <instance> [output.yaml]` dumps the cold-tier config to YAML in canonical order.
- `arizuko archive export <instance> [file] [--quiesced]` makes a full-instance backup in one tar: the config dump, secret values, message history, pending onboarding admissions and every `groups/<folder>/` tree.
- `arizuko archive apply <instance> <archive.tar> [--force] [--stopped]` restores a full backup. It claims the run slot of each folder at runed before it touches the tree of that folder, so an unreachable runed is fatal. `--stopped` asserts instead that the instance is down. The credential-bearing documents `route_tokens`, `invites` and `onboarding` restore only with `--force`, onto a target that is proven empty.

Talking to an agent:

- `arizuko send <instance> <folder> [<message>] [--wait|-w | --stream|-S] [--stdin] [--from|-f <sender>] [--topic|-T <topic>] [--token|-t <raw>]` injects a message into the queue of a folder. The topic keeps the conversation continuous. The default is operator-direct injection: it needs no token and writes the inbound row to the tables of routd on `web:<folder>`. The operator already reaches those tables with the same authority as `create`, `grant` and `secret`. The poll loop of routd runs the agent, and `--wait` or `--stream` prints its reply. With `--token` or `ARIZUKO_CHAT_TOKEN` set, the command posts to the public `/chat/<token>` endpoint as a caller who is not an operator.
- `arizuko chat <instance> [--folder <folder>]` starts an interactive Claude Code session bound to the MCP socket of the folder. The default folder is `main`.
- `arizuko talk [@name] [-g <folder>] [-t <thread>] [-p [prompt]] [--url U] [--token T]` holds a conversation with the agent from a terminal. It takes no instance argument and opens no store. It posts to `<url>/mcp` (`list_groups` and `send`) and reads the round from `<url>/me/chats/<folder>/<thread>/sse`, both through the public proxyd URL, so it runs from a second machine. The config is `~/.config/arizuko/talk.toml`, and the command refuses it unless the mode is `0600`. At the top level, `url`, `token` and the optional `group` are the one chat, and a `[work]` section is `@work`. The precedence is the flag, then `@name`, then the top level, then `ARIZUKO_CHAT_URL` with `ARIZUKO_CHAT_TOKEN`, then an error that prints the file to write. `-p` runs one turn, with the prompt inline or on stdin, and is one iteration of the REPL loop and never a second send path. `-t` defaults to the fixed literal `talk`. Here `-t` is the thread, and in `send` it is the token. The bearer flag of `talk` is `--token` only.

**`send`, `chat` and `talk` are three ways to reach an agent, and they are not interchangeable.**

- `send` is operator-direct injection. It opens the store of the instance and writes the inbound row itself. It needs the data directory, runs on the host, and uses the own authority of the operator.
- `chat` is an interactive Claude Code on the root MCP socket. It is an operator tool to drive the platform and not a conversation with the persona.
- `talk` is a thin token client with no store. It uses two HTTP surfaces and an authd bearer, and it is the only one of the three that works from a machine that is not the host.

### Manifests and the archive

- **The store is the truth, and YAML is transport.** `export` dumps and `apply` restores. Nothing syncs the store back to a file, applies at startup or reloads on SIGHUP. A dump never claims to be live, so "drift" between a file and the store is not a state that anyone has to manage.
- **`apply --as-folder <f>` needs `--force`**, because a rewritten manifest can never match the checksum of the target. The scoped rows of the manifest must name one folder. The empty scope counts as a folder of its own, so an instance-wide `network_rules` row never turns into the row of one folder. The command rewrites the declared scope field of `groups`, `network_rules`, `pending_actions` and `web_routes`, and also `web_routes.redirect_to`, which embeds the folder. Rows of resources with no scope pass through unchanged. The command moves no files. To copy a group, run `export` plus `apply --as-folder` for the rows, and copy `groups/<folder>/` for the files. Routing never creates a group.
- **The archive is one tar.** It holds `archive.yaml` (`format_version` and the `consistency` stamp, `live` or `quiesced`), `routd.yaml`, `onbod.yaml`, `routd.secrets.yaml`, `routd.messages.jl` and `groups.tar`. `routd.secrets.yaml` holds the values of secrets, route tokens, invites and pending onboarding, apart from the other documents so that a plain `export` reader never sees one. The stores of authd and runed are not in the archive. `archive apply` refuses a `format_version` newer than the binary.
- **Messages travel whole.** `routd.messages.jl` is the full history, and the import is an idempotent append. The import derives the `agent_cursor` of each chat from its newest message, because without it the restored poller would treat all history as unseen. Secret values travel sealed, and `SECRETS_KEY` never travels. The command checks every secret row against the keyring of the target before any write.

## Dependencies

`store`, `core`, `groupfolder`, `auth`, `routd` for the installed-package record, `resreg`, `container`, `compose`, `audit`, `objstore`, `proxycfg` and `ipc`. The CLI also opens the store layers of authd, runed, emaid and chanlib.

## Files

- `main.go` dispatches the commands and holds `create`, `generate`, `run`, `status`, `pair`, `group`, `gate`, `invite` and `chat`.
- `route.go` is the audited CLI for the message-routing table.
- `routes.go` renders `proxy-config`, the Caddyfile.
- `probe.go` runs `probe`, the contract check for a deployed component.
- `skills.go` and `flags.go` hold the skill enumerator and the shared flag parser.
- `apply.go` holds `apply`, `plan`, `get` and `export`, the missing-group preflight, and the cross-subsystem pre-image rollback that `apply` and `archive apply` share.
- `archive.go` holds `archive export` and `archive apply`.
- `retarget.go` implements `apply --as-folder`, the recipe over `resreg.Resource.Retarget`.
- `budget.go` and `network.go` hold the spend caps and the egress rules.
- `packages.go` holds the catalog `add`, and the source-based `install`, `upgrade`, `sync` and `remove`, with the `installed_packages` record. `reapplyPackage` is the one updater that `upgrade` and `sync` share.
- `products.go` holds `products list`, the catalog reader, and `products apply`: the `products.toml` reader and the group-scoped half of the `installed_packages` record. The blend itself is `container/blend.go`.
- `secret.go` holds `secret` and `user-secret`.
- `send.go` holds `send` and the SSE reader (`openSSE`, `readSSE`, `handleSSEFrame`) that `talk` shares.
- `talk.go` and `talk_config.go` hold the argument grammar of `talk`, its MCP and SSE turn, and the reader for `~/.config/arizuko/talk.toml`.
- `token.go` manages route tokens.
- `github.go` manages the webhook sources of gitd.
- `gdrive.go` manages the file bindings of googd.
- `group_rename.go`, `group_rename_files.go` and `group_rename_rows.go` implement the offline `group rename`.
- `grant_canonical.go` holds the identity reader that the CLI uses where routd would federate to authd.

## Coverage

`go test ./cmd/arizuko/ -short -cover` reports 59.5%, measured in this pass. On a loaded host the run needs `-timeout 3600s`, because the default timeout of 10 minutes is too short. The gap is mostly one shape.

The tests cover the parsing, planning and wire logic. The covered parts are the manifest readers, the apply plan, the package resolver, the `.env` parser, and both `send --wait` paths. Those paths are `streamRound` over SSE and `pollRound` as the fallback. `talk` is covered end to end against a real streamable-HTTP MCP server and an SSE stub (`talk_test.go`). The tests cover its argument grammar, config precedence, the 0600 refusal, folder resolution and one turn on the wire.

The tests do not cover the `cmdX` subcommand entry points, and they cannot do so in-process. Each entry point ends at `die()` in `main.go`, which calls `os.Exit(1)`. A test that reaches `die` takes the whole test binary with it. That is a deliberate CLI shape: an operator wants a status code and one line on stderr, and not an error value passed back to `main`. So `make test-e2e` and `tests/` exercise the entry points end to end, and the unit tests here do not.

The uncovered part is therefore mostly the `cmdX` entry points and not untested decisions. Each entry point is an argument unpack in front of a function that tests cover.

## Related docs

- The top-level `README.md`
- `ARCHITECTURE.md`

### Offline group rename

Stop all instance writers, ingress, guests and writable staging mounts. Keep only the database service running. Take a complete database backup and keep the mutable files and keys. A config archive omits runtime state.

```bash
arizuko group sloth rename main sloth --map main/main=sloth/discord \
  --inventory /private/rename-inventory.json \
  --endpoint https://127.0.0.1:18444 --dry-run
arizuko group sloth rename main sloth --map main/main=sloth/discord \
  --inventory /private/rename-inventory.json \
  --endpoint https://127.0.0.1:18444 --stopped --yes \
  --journal /private/sloth-rename
```

The inventory is a private `0600` JSON document, taken from the complete table catalog of the stopped database. The key `owners` maps each owner to every table, then each partition of a table to its exact item count. Include empty tables as `{}`. This evidence supplies the partition enumeration, and the CLI reads and writes through the object store. The inventory also carries `instance`, `endpoint`, `configured_endpoint`, `ca_sha256` and `key_ids_sha256`, which maps each owner to a credential-ID hash. Hash the decoded `STORE_CA` bytes and each `AWS_ACCESS_KEY_ID` with SHA-256. `configured_endpoint` equals the generated `DYNAMODB_URL`. `endpoint` equals the operator URL that you pass on the command line. Every `env/<owner>.env` must identify the selected instance through `ARIZUKO_INSTANCE`. The credentials come from those files, and no process-environment value overrides them.

The preflight checks table coverage, counts, destination collisions, active records, mounts, sockets and unsupported references. A more specific mapping uses original coordinates. These items keep their bytes: platform JIDs, including `local:` feeds, opaque IDs, ciphertext, cursor positions, historical prose and audit evidence. Directories merge only without leaf collisions, and unrelated destination files remain intact. Workspace files move with their directory. The CLI updates symlink targets, operational web URLs, absolute workspace paths and declared agent settings. It refuses unclassified folder settings. Unqualified route `room` operands keep the platform room names, and only explicit `platform=web` or `platform=hook` room operands move. Hook JIDs and explicit hook rooms keep their source labels. Coordinate operands in ACL Params and Predicate require exact values. Globs fail the preflight, including on ACL rows that do not change. When a token JID changes, the preflight compares its parsed folder with its recorded target. If the target is set and differs, the preflight rejects the ambiguous alias.

Execution creates a private `0700` journal outside the instance directory. `plan.json` holds the complete before and after item images and the changed file bytes. `progress.jl` records the durable batch boundaries. Work across owners is not one transaction. If execution stops, keep the instance fenced and repeat the exact command with `--resume` or `--rollback`. Keep the inventory and the original mapping unchanged. Rollback refuses drift. Verify routing, permissions, feeds, secrets, sessions and files before you restart. A signed JWT claim needs a fresh login if the claim carries an old folder.
