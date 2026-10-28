# cmd/arizuko

cmd/arizuko builds the `arizuko` binary, the command line of the operator. It creates an instance and generates its compose file. It also administers groups, grants, secrets, routes, packages and cold-tier config.

## Where the CLI runs

The CLI runs on the host of the instance. Every command except `talk` and `probe` takes `-i/--instance NAME` anywhere before `--`; without it the command uses the configured default. `-h/--help` after a command prints its usage to stdout and exits 0; `arizuko -h` prints the usage of all commands. `instanceDir` resolves the data directory `/srv/data/arizuko_<name>`, or `$ARIZUKO_DATA_DIR/arizuko_<name>`, or `$PREFIX/data/arizuko_<name>`. It then loads the `.env` of the instance into its own environment. A command that reads or writes rows opens the store of the owner directly through `DYNAMODB_URL`. So the store server must run, and the owner daemon must have booted once. Most commands call no daemon. These commands need more:

- `run`, `pair` and `status` call `docker compose`.
- `chat` needs `claude` and `socat` on the path, and the IPC socket of the instance.
- `archive apply` claims the run slot of each folder at runed, unless you pass `--stopped`.

## Commands

The binary is `cmd/arizuko/main.go`, and the argument grammar of each command is in its source file.

Instance:

- `arizuko create [-i NAME] [--product|-p <product>]` seeds the data directory and writes its `.env` from its own key set: `ASSISTANT_NAME`, `API_PORT`, a generated `AUTH_SECRET` and `SECRETS_KEY`, and `ARIZUKO_DEFAULT_PRODUCT`. It does not read `template/env.example`, which is the reference of the operator for the other variables. `create` writes files and no row, because no store server exists at create time. `-p/--product` declares the product as `ARIZUKO_DEFAULT_PRODUCT` and prints its `env` list. It copies no workspace file. At its first boot routd writes the `main` group row with that product and one `installed_packages` row for `main` with the source `ant/examples/<product>`: `routd.Open` calls `store.SeedDefaultGroup` for this. The product files reach the group at the next `arizuko package sync`.
- `arizuko generate [-i NAME]` writes `docker-compose.yml` and the compose-managed block of `.env`. It also relinks every installed fragment that is still a byte-identical copy of the bundled catalog into a symlink (`compose.RelinkCatalog`). This takes no flag and runs on every generate and deploy. A fragment that diverged, even by a comment, stays a real file, and so does a fragment whose filename encodes a multi-account variant (`<kind>-<label>.yml`). `HOST_APP_DIR` in `.env` must be a path that the host can resolve. See the "Packages" section of `compose/README.md`.
- `arizuko run [-i NAME]` runs `generate`, then `docker compose up --remove-orphans`.
- `arizuko status [-i NAME]` shows the compose services and the channels that the router reports.
- `arizuko pair [-i NAME] <service> [-- container args...]` runs `docker compose run --rm <service>`. Container arguments follow `--`.
- `arizuko images [-i NAME] [--prune]` lists every `rootfs-*` and `kernel-*` tree in `<data_dir>/images/` with its size, and marks the in-use trees. `--prune` removes every other tree. The in-use trees are the ones that `RUNED_ROOTFS_DIR` and `RUNED_KERNEL_DIR` name in the `.env` of the instance. The command reads that file itself, so run it as root. It refuses if either key is unset, names a path outside `images/`, or names a tree that is not on disk. It removes nothing while a mount exists at or under a tree that it would remove. It checks the `mountinfo` of every process, because the guest mounts live in the namespace of runed. It removes only direct children of `images/`, and never a symlink.
- `arizuko proxy-config [-i NAME] [--listen :8080] [--forward-auth <host:port>]` renders the live route table as a Caddyfile. Without `--forward-auth`, every authenticated route renders as a 503 and never as an open hop.
- `arizuko probe <base-url> [--token|-t T] [--foreign-folder F]` checks a deployed component against the component environment. `ARCHITECTURE.md` section "Decided, and why" describes that environment. Go tests read the source, and this command reads the instance. A daemon can pass every test and still ship with `openapi.json` behind auth, or with a gate unwired. The command detects paths that the document advertises and the mux does not serve, and never the reverse. proxyd answers every path from a `/` catch-all, so "it responded" never proves that a resource is mounted there.

Groups and access:

- `arizuko group [-i NAME] list | add <jid> <folder> [-p|--product <name>] | rm <folder> | grant | ungrant | grants | rename`. `add` also writes the first route for the JID. With `-p`, `add` writes one `installed_packages` selection row for the folder with the source `ant/examples/<name>`. The files arrive on the next `arizuko package sync`. A Discord guild channel gets a `verb=mention` rule and a catch-all `#observe` rule, so that messages without a mention only add context.
- `arizuko chat [-i NAME] thread-replies <jid> [on|off|default]` reads or changes the chat's thread reply setting. Reset enables threads for group chats and disables them for direct messages.
- `arizuko group [-i NAME] rm [-f|--force] <folder>` removes the group and every group under it, then stops the live run of each removed folder at runed (`POST /v1/runs/stop`). After every stop succeeds, it deletes their `groups/`, `web/pub/` and `web/priv/` directories and prints each deleted path. It refuses invalid paths and symlinks that escape these roots before it removes any row or file. It reads `RUNED_URL` (default `http://runed:8080`) and `RUNED_TOKEN` from the instance `.env`, as `archive apply` does, and probes `GET /health` before it removes anything. When runed is unreachable, the command removes nothing and exits non-zero. `-f` removes the rows and directories without stopping any run, and without contacting runed: use it only when no run is live.
- `arizuko group [-i NAME] grant|ungrant <sub> <pattern> [action] [--effect allow|deny] [--params <matcher>]` writes one acl row, named by its whole coordinate. `store.aclKey` joins principal, action, scope, params, predicate and effect, so a revoke that names fewer fields addresses a different item and deletes nothing. `--effect deny` on a `hold:mcp:<tool>` action is the skip-approval rule that `/always` writes. `--params` carries the argument matcher that the row was narrowed to, and `grants` prints that column. `<sub> ** admin` with no params stays the shortcut for the operator role, in both directions.
- `arizuko gate [-i NAME] list | add | rm | enable | disable` manages the onboarding gates.
- `arizuko invite [-i NAME] create <target_glob> [--max-uses|-n N] [--expires|-e DURATION]`, `invite [-i NAME] list [--issued-by|-b SUB]` and `invite [-i NAME] revoke <ref>` manage invites.
- `arizuko network [-i NAME] allow|deny <folder> <target> | list [<folder>] | resolve <folder>` manages the egress rules of a folder.
- `arizuko budget [-i NAME] set folder|user <name> --daily|-d N` and `show folder|user <name>` set and show the daily spend cap in cents, where 0 means uncapped. The caps live in the tables of routd. The gate before spawn pushes both caps to the model gateway, which meters the spend and refuses a request that exceeds either cap. `show` prints the caps and no spend, because spend belongs to the gateway and the CLI holds no master key.

Secrets and tokens:

- `arizuko secret [-i NAME] set <folder> KEY --value|-v V | list <folder> | delete <folder> KEY | seal-legacy [--dry-run]` manages folder-scoped secrets. `list` prints key names and creation times, never values. Shell access also needs an explicit grant, see [Secret injection](../../SECURITY.md#secret-injection). `seal-legacy` encrypts every row that is still unsealed, in place. Rehearse it with `--dry-run`, because it rewrites the attribute that every credential lives in.
- `arizuko user-secret [-i NAME] set <user_sub> KEY --value|-v V | list <user_sub> | delete <user_sub> KEY` manages user-scoped secrets.
- `arizuko token [-i NAME] issue chat <folder> [<suffix>] [--context|-c TEXT]` and `issue webhook <folder> <label> [<suffix>] [--context|-c TEXT]` mint capability URLs. `token <instance> list <folder>` lists `route_tokens`. `token <instance> revoke <jid> [<owner_folder>]` revokes one.
- `arizuko token [-i NAME] issue bearer <folder> --scope|-s s1,s2 [--ttl 1h] [--sub SUB]` mints an ES256 access JWT for a folder, signed with the active key of authd. The command reads the key from the `signing_keys` of authd and is for operators only. Example: `--scope messages:write,messages:read` serves the inject and inspect steps of anteval.
- `arizuko github [-i NAME] add <repo|org|app> <name> <folder> | list [<folder>] | rm <kind>/<name> <folder>` manages `github_sources`, the webhook sources of gitd. `add` prints the delivery URL and the secret once, and it seeds the route rows that deliver the events of the source to `<folder>`.
- `arizuko gdrive [-i NAME] add <file_id> <folder> | list [<folder>] | rm <file_id>` manages `gdrive_files`, the bindings of googd. `add` binds one Drive file and seeds the route rows that deliver its comments to `<folder>`. Share the file with the service account of googd first.

Routing and packages:

- `arizuko route [-i NAME] list | add <match> <target> [--seq N] | rm <id>` manages the message-routing table. Writes go through the audited `AddRoute` and `DeleteRoute`, so the route and its audit row commit together. `AsCLI` attributes the row to the invoking operator, because the CLI has no `X-User-*` header to learn a sub from.
- `arizuko package [-i NAME] add [-g|--group <folder>] [-f|--force] <source>` installs one package and records its selection in the `installed_packages` resource. Without `-g` the selection covers the whole instance. With `-g` it covers that group only.
- `arizuko package [-i NAME] rm [-g|--group <folder>] <name>` withdraws one selection with its rows and services. The `core` package cannot be withdrawn.
- `arizuko package [-i NAME] sync` installs every selected package again from its source, writes the `group/` payload of each group, prints one line for each override, and generates compose. `sync` takes neither `-g` nor a source.
- `-f/--force` replaces only a destination that no package owns: a service file or a `group/` file that exists and that no package installed. Without `-f`, the command stops and names the file.
- The order is core first, then the order of `add`. A later package overrides an earlier one on the same skill, hook, `bin/` or `group/` path. To move a package later, run `rm` and then `add`. `arizuko.yml` in a package source only defines that package; it never selects one. The group `arizuko.yml` files are not read. [EXTENDING.md](../../EXTENDING.md#installing-packages) defines source layout, native assets, order and `group/` files.

Config and backup:

- `arizuko plan [-i NAME] <manifest.yml>` shows the diff of a manifest against the live config without a write. It also reports the missing-group refusal that `package sync` would raise.
- `arizuko get [-i NAME] <resource>` emits the live rows of one resource as a YAML fragment, which round-trips to a no-op.
- `arizuko export [-i NAME] [output.yml]` dumps the cold-tier config to YAML in canonical order.
- `arizuko archive [-i NAME] export [file] [--quiesced]` makes a full-instance backup in one tar: the config dump, secret values, message history, pending onboarding admissions and every `groups/<folder>/` tree.
- `arizuko archive [-i NAME] apply <archive.tar> [--force|-f] [--stopped]` restores a full backup. It claims the run slot of each folder at runed before it touches the tree of that folder, so an unreachable runed is fatal. `--stopped` asserts instead that the instance is down. The credential-bearing documents `route_tokens`, `invites` and `onboarding` restore only with `--force`, onto a target that is proven empty.

Talking to an agent:

- `arizuko send [-i NAME] <folder> [<message>] [--wait|-w | --stream|-S] [--stdin] [--from <sender>] [--topic|-T <topic>] [--token|-t <raw>]` injects a message into the queue of a folder. The topic keeps the conversation continuous. The default is operator-direct injection: it needs no token and writes the inbound row to the tables of routd on `web:<folder>`. The operator already reaches those tables with the same authority as `create`, `grant` and `secret`. The poll loop of routd runs the agent, and `--wait` or `--stream` prints its reply. With `--token` or `ARIZUKO_CHAT_TOKEN` set, the command posts to the public `/chat/<token>` endpoint as a caller who is not an operator.
- `arizuko chat [-i NAME] [-g|--group <folder>]` starts an interactive Claude Code session bound to the MCP socket of the folder. The default folder is `main`.
- `arizuko talk [@name] [-g|--group <folder>] [--thread <name>] [--prompt [text]] [--url U] [-t|--token T]` holds a conversation with the agent from a terminal. It takes no instance argument and opens no store. It posts to `<url>/mcp` (`list_groups` and `send`) and reads the round from `<url>/me/chats/<folder>/<thread>/sse`, both through the public proxyd URL, so it runs from a second machine. The config is `~/.config/arizuko/talk.toml`, and the command refuses it unless the mode is `0600`. At the top level, `url`, `token` and the optional `group` are the one chat, and a `[work]` section is `@work`. The precedence is the flag, then `@name`, then the top level, then `ARIZUKO_CHAT_URL` with `ARIZUKO_CHAT_TOKEN`, then an error that prints the file to write. `--prompt` runs one turn, with the prompt inline or on stdin, and is one iteration of the REPL loop and never a second send path. `--thread` defaults to the fixed literal `talk`. `-t` is the token in `talk`, as in `send` and `probe`.

**`send`, `chat` and `talk` are three ways to reach an agent, and they are not interchangeable.**

- `send` is operator-direct injection. It opens the store of the instance and writes the inbound row itself. It needs the data directory, runs on the host, and uses the own authority of the operator.
- `chat` is an interactive Claude Code on the root MCP socket. It is an operator tool to drive the platform and not a conversation with the persona.
- `talk` is a thin token client with no store. It uses two HTTP surfaces and an authd bearer, and it is the only one of the three that works from a machine that is not the host.

### Manifests and the archive

- **The store holds live rows.** `arizuko package add`, `rm` and `sync` change the package selection in `installed_packages`. `get` and `export` inspect live rows; `plan` previews a manifest without writing. Package membership and owned keys remain visible through the `installed_packages` resource.
- **The archive is one tar.** It holds `archive.yaml` (`format_version` and the `consistency` stamp, `live` or `quiesced`), `routd.yaml`, `onbod.yaml`, `routd.secrets.yaml`, `routd.messages.jl` and `groups.tar`. `routd.secrets.yaml` holds the values of secrets, route tokens, invites and pending onboarding, apart from the other documents so that a plain `export` reader never sees one. The stores of authd and runed are not in the archive. `archive apply` refuses a `format_version` newer than the binary.
- **Messages travel whole.** `routd.messages.jl` is the full history, and the import is an idempotent append. The import derives the `agent_cursor` of each chat from its newest message, because without it the restored poller would treat all history as unseen. Secret values travel sealed, and `SECRETS_KEY` never travels. The command checks every secret row against the keyring of the target before any write.

## Dependencies

`store`, `core`, `groupfolder`, `auth`, `routd` for the installed-package record, `resreg`, `container`, `compose`, `audit`, `objstore`, `proxycfg` and `ipc`. The CLI also opens the store layers of authd, runed, emaid and chanlib.

## Files

- `main.go` dispatches the commands and holds `create`, `generate`, `run`, `status`, `pair`, `group`, `gate`, `invite` and `chat`.
- `route.go` is the audited CLI for the message-routing table.
- `routes.go` renders `proxy-config`, the Caddyfile.
- `probe.go` runs `probe`, the contract check for a deployed component.
- `flags.go` holds the shared flag parser.
- `apply.go` holds `plan`, `get`, `export`, the missing-group preflight and the cross-subsystem rollback shared by sync and archive restore.
- `archive.go` holds `archive export` and `archive apply`.
- `budget.go` and `network.go` hold the spend caps and the egress rules.
- `sync.go` holds `package add`, `package rm` and `package sync`: one reconcile of every selection in `installed_packages`.
- `packages.go` holds package source, asset, environment and scope helpers used by `package`.
- `products.go` resolves a package source: `catalog:<name>`, a path under `HOST_APP_DIR`, or a Git URL. `resreg` owns the shared YAML schema.
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
arizuko group -i sloth rename main sloth --map main/main=sloth/discord \
  --inventory /private/rename-inventory.json \
  --endpoint https://127.0.0.1:18444 --dry-run
arizuko group -i sloth rename main sloth --map main/main=sloth/discord \
  --inventory /private/rename-inventory.json \
  --endpoint https://127.0.0.1:18444 --stopped --yes \
  --journal /private/sloth-rename
```

The inventory is a private `0600` JSON document, taken from the complete table catalog of the stopped database. The key `owners` maps each owner to every table, then each partition of a table to its exact item count. Include empty tables as `{}`. This evidence supplies the partition enumeration, and the CLI reads and writes through the object store. The inventory also carries `instance`, `endpoint`, `configured_endpoint`, `ca_sha256` and `key_ids_sha256`, which maps each owner to a credential-ID hash. Hash the decoded `STORE_CA` bytes and each `AWS_ACCESS_KEY_ID` with SHA-256. `configured_endpoint` equals the generated `DYNAMODB_URL`. `endpoint` equals the operator URL that you pass on the command line. Every `env/<owner>.env` must identify the selected instance through `ARIZUKO_INSTANCE`. The credentials come from those files, and no process-environment value overrides them.

The preflight checks table coverage, counts, destination collisions, active records, mounts, sockets and unsupported references. A more specific mapping uses original coordinates. These items keep their bytes: platform JIDs, including `local:` feeds, opaque IDs, ciphertext, cursor positions, historical prose and audit evidence. Directories merge only without leaf collisions, and unrelated destination files remain intact. Workspace files move with their directory. The CLI updates symlink targets, operational web URLs, absolute workspace paths and declared agent settings. It refuses unclassified folder settings. Unqualified route `room` operands keep the platform room names, and only explicit `platform=web` or `platform=hook` room operands move. Hook JIDs and explicit hook rooms keep their source labels. Coordinate operands in ACL Params and Predicate require exact values. Globs fail the preflight, including on ACL rows that do not change. When a token JID changes, the preflight compares its parsed folder with its recorded target. If the target is set and differs, the preflight rejects the ambiguous alias.

Execution creates a private `0700` journal outside the instance directory. `plan.json` holds the complete before and after item images, package selections and changed file bytes. `progress.jl` records the durable batch boundaries. Work across owners is not one transaction. If execution stops, keep the instance fenced and repeat the exact command with `--resume` or `--rollback`. Keep the inventory and the original mapping unchanged. Rollback refuses drift. Verify routing, permissions, feeds, secrets, sessions and files before you restart. A signed JWT claim needs a fresh login if the claim carries an old folder.

Rename moves package membership with each registered folder's rows. Package
payloads stay under their shared names. Dry run writes nothing. Group deletion
purges membership rows with the group; installed payload directories remain.
