# Installing arizuko

This guide takes a bare Linux host to a first reply. Follow it from top to
bottom.

## Check the host before you install

Run these checks and note which ones fail:

```bash
# 1. OS: Linux x86_64 is required
uname -s -m   # expect: Linux x86_64

# 2. Go 1.21 or newer
go version    # missing or older than 1.21 is a blocker
              # go.mod pins `toolchain go1.27rc2`. With GOTOOLCHAIN=auto, the
              # default, Go fetches that toolchain on the first build.
              # GOTOOLCHAIN=local or an offline host must install it by hand.

# 3. Docker daemon running
docker info >/dev/null 2>&1 && echo "docker ok" || echo "docker MISSING"

# 4. Docker Compose v2.20 or newer
docker compose version   # the generated compose file uses a top-level `include:`

# 5. Docker socket access
groups | grep -q docker && echo "docker group ok" || echo "not in docker group (will need sudo)"

# 6. git and make
git --version && make --version | head -1

# 7. Data directory writable
ls -ld /srv/data 2>/dev/null || echo "/srv/data does not exist"

# 8. KVM, the turn substrate: one guest per turn
ls -l /dev/kvm /dev/vhost-vsock /dev/net/tun 2>&1
# a missing /dev/kvm is a blocker. The other two devices come from the kernel
# modules `vhost_vsock` and `tun`, which are usually built in.

# 9. Memory for the guests
grep MemTotal /proc/meminfo
```

Check the memory before you set the guest cap. `runed` refuses to start when
`RUNED_GUEST_MEM` times `MAX_CONCURRENT_CONTAINERS` is more than `MemTotal`, see
`memoryFits` in `runed/qemuhost.go`. The defaults are 2 GiB and 5, so they need
10 GiB. Each release rootfs needs about 7 GB of disk, and `make images` removes
no old tree.

## Fix a failed check

### Go is missing or too old

Install a stable Go release, 1.21 or newer, from <https://go.dev/dl/>.

`go.mod` has the directive `go 1.27` and pins `toolchain go1.27rc2`, a release
candidate. The directive does not accept an `rc` version. With
`GOTOOLCHAIN=auto`, Go fetches the pinned toolchain on the first `go build`.

Two more pins move with that toolchain:

- **The Docker base image.** Each Dockerfile that builds the root module names
  `golang:1.27rc2-alpine`. The files are `Dockerfile`, `anteval/Dockerfile`,
  `egress/Dockerfile` and `ttsd/Dockerfile`. Nothing derives
  the name from `go.mod`, so change them in the same commit as `go.mod`.
  `kernel/guest-init` is its own module on `go 1.25` and keeps its own base.
- **`golang.org/x/net`.** `go.mod` requires v0.57.0, and the version must not
  go down.

Go does not fetch the toolchain in two cases: the host sets `GOTOOLCHAIN=local`,
or the host cannot reach `go.dev`. Install the toolchain by hand:

```bash
go install golang.org/dl/go1.27rc2@latest && go1.27rc2 download
```

### Docker Compose v2 is missing

```bash
mkdir -p ~/.docker/cli-plugins
curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o ~/.docker/cli-plugins/docker-compose
chmod +x ~/.docker/cli-plugins/docker-compose
docker compose version   # verify
```

### The user cannot reach the Docker socket

`arizuko run` talks to the Docker daemon. Pick one option:

```bash
# Option A: add the user to the docker group. Log out and in again afterwards.
sudo usermod -aG docker $USER
docker ps

# Option B: run the arizuko commands with sudo
sudo ./arizuko run <name>
```

### /srv/data is not writable

Option A gives your user the directory:

```bash
sudo mkdir -p /srv/data
sudo chown -R $(whoami):$(whoami) /srv/data
```

Option B sets another prefix. Every `arizuko` command then keeps an instance in
`$PREFIX/data/arizuko_<name>`. The variable `ARIZUKO_DATA_DIR` has a higher
priority than `PREFIX`. It names the directory that holds the instance
directories.

```bash
export PREFIX=~/.arizuko
```

### /dev/kvm is missing

Without `/dev/kvm`, the host cannot run turns. This is a hardware or
hypervisor limit. No arizuko setting changes it.

```bash
ls -l /dev/kvm || echo "no /dev/kvm"
lsmod | grep kvm                          # kvm_intel or kvm_amd must be loaded
sudo modprobe kvm_intel || sudo modprobe kvm_amd
```

If both `modprobe` commands fail, enable virtualization in the BIOS or UEFI. The
setting is Intel VT-x or AMD-V. On a cloud instance, check that the provider offers nested
virtualization. A guest without nesting has no `/dev/kvm`.

## Build the binaries

```bash
git clone https://github.com/kronael/arizuko && cd arizuko
make build                  # ./arizuko CLI and the daemon binaries
make test                   # go test ./... -short
```

## Build the images and the guest kernel

`DATA_DIR` names the directory that receives the guest kernel and the release
rootfs. Use the directory that the next section creates,
`/srv/data/arizuko_<name>`. `mkdir -p` creates it now if it does not exist.
Instances that run the same `arizuko-ant` image can share one `DATA_DIR`, so
the export runs once. [`kernel/README.md`](kernel/README.md) says what
`make kernel` builds and why it is a separate artifact.

```bash
sudo make images DATA_DIR=/srv/data/arizuko_<name>
# builds arizuko:latest, arizuko-ant:latest and the adapter images, then exports
# the release rootfs that the guest boots (DATA_DIR/images/rootfs-<hash>/)
sudo make kernel DATA_DIR=/srv/data/arizuko_<name>
# builds the pinned guest kernel and initrd (DATA_DIR/images/kernel-<hash>/).
# It rebuilds only when kernel/Dockerfile, kernel/arizuko.fragment,
# kernel/check-config.sh or kernel/guest-init/*.go change.
```

`make images` removes no tree. Each tree in `images/` is about 7 GB, so old
trees fill the disk. Free them with the operator command, on the host, as root:

```bash
sudo arizuko images <name>            # list every rootfs and kernel tree, with its size
sudo arizuko images <name> --prune    # remove every tree that is not in use
```

The in-use trees are the two that `RUNED_ROOTFS_DIR` and `RUNED_KERNEL_DIR` name
in `/srv/data/arizuko_<name>/.env`. The command refuses to run if the `.env` does
not name both, or if a named tree is not on disk. It removes nothing if a mount
exists at or under a tree that it would remove. It removes only the direct
children of `images/` named `rootfs-*` or `kernel-*`. To keep a rollback tree,
set `RUNED_ROOTFS_DIR` to it first, or do not prune. The listed size adds the
size of every file name in a tree, so a hard-linked file counts once for each
name, and the sizes can add up to more than a prune frees.

Never run `docker image prune -a` on an arizuko host. No container holds
`arizuko-ant` between turns, so the prune deletes it, and the rootfs export
reads that image. To free the build cache, run `docker builder prune -f`.

`make images` builds the complete image set, the adapter images included. To
rebuild one image only, use these targets:

```bash
sudo make agent             # arizuko-ant:latest only
sudo make -C whapd image    # the WhatsApp adapter only
```

## Create the instance

```bash
./arizuko create <name>     # for example ./arizuko create mybot
```

The command creates `/srv/data/arizuko_<name>/` with a `.env` file. It writes
`ASSISTANT_NAME`, `AUTH_SECRET` and `SECRETS_KEY` for you, so do not create
the two secrets by hand.

`create` makes the files of the instance and nothing else. It makes the
directory, the `.env`, the compose fragments and the tree of the default group.
It runs before the instance has a store server. The owner daemon of each row
writes that row at its first boot. `routd` seeds the access floor, the egress
floor and the `main` group, and it uses the `--product` choice that `create`
wrote to `ARIZUKO_DEFAULT_PRODUCT`.

The instance directory, `${PREFIX:-/srv}/data/arizuko_<name>/`, holds these
paths:

```
.env                 settings and anchor secrets; the daemons read it from cwd
store/               extenddb/ server config and certificate, extenddb_db/
                     PostgreSQL data, <adapter>/ files an adapter owns
groups/<folder>/     the home of the agent: session state in .claude/, diary,
                     logs, media/<YYYYMMDD>/ inbound attachments
ipc/<folder>/        the MCP socket of the turn
web/pub/ web/priv/   the web trees that the slots of each folder use
```

Business state, such as gates, grants and onboarding, is stored as rows.
Infrastructure settings are environment variables.

## The store server holds every table of the instance

Every table lives on a server that speaks the DynamoDB protocol. The server is
ExtendDB on top of PostgreSQL. A deployment is an account on that server. It is
never a database or a PostgreSQL role, so one server can hold every instance on
a host. [`objstore/README.md`](objstore/README.md) draws the layers from a
daemon down to PostgreSQL. Follow this section for a server of the instance's
own. Follow "Share one store server between instances" to put several
instances on one server.

The package starts both containers. You run the `init` command of the server
yourself, because it prints an admin password once and mints one access key for
each daemon.

```bash
./arizuko packages <name> add extenddb   # mints STORE_DB_PASSWORD and STORE_APP_PASSWORD
./arizuko generate <name>
cd /srv/data/arizuko_<name>
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb_db    # PostgreSQL of the instance
sudo docker compose run --rm extenddb init \
  --pg-host extenddb_db --pg-port 5432 \
  --pg-user arizuko_<name> --pg-pass "$STORE_DB_PASSWORD" \
  --extenddb-user arizuko_<name>_app --extenddb-pass "$STORE_APP_PASSWORD" \
  --bind-addr 0.0.0.0 --tls-san extenddb \
  --config /var/lib/extenddb/extenddb.toml
```

`init` prints the admin password and the account id once. Save both in `.env`
before you lose them. The names `STORE_ADMIN_PASSWORD` and `STORE_ACCOUNT_ID`
are only shell variables for this guide, and no daemon reads them. Then start
the server and set the control-plane delay to zero. A table is then ready the
moment `CreateTable` returns.

```bash
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb settings -c /var/lib/extenddb/extenddb.toml set control_plane_delay_seconds 0
```

<a id="the-per-daemon-store-keys"></a>

### The per-daemon store keys limit each daemon to its own tables

Each daemon signs in as itself. It has its own IAM user, its own access key in
`env/<daemon>.env`, and a policy that covers only the tables it opens. The
account is the tenant boundary, and the policy is the only boundary inside the
account. `dynstore.Prefix` puts the owner name in front of every table name.
The resource `routd.*` therefore means exactly the tables of routd.

Never give a key `dynamodb:*` on `*`. Such a key names its owner but isolates
nothing. One compromised adapter could then reach the tables of every owner on
the instance. All 19 keys on the sloth instance had this policy, see BUGS `L318`.

The table below comes from the call sites. `StoreReach` in
`store/owner_reach.go` declares it, and `TestStoreReachMatchesTheCallSites`
fails when a daemon opens an owner that the table does not list, or lists an
owner that the daemon does not open. When you change what a daemon opens, that
test names the policy to rewrite.

| daemon        | policy resources                |
| ------------- | ------------------------------- |
| `authd`       | `authd.*`                       |
| `cmd/arizuko` | `authd.*`, `onbod.*`, `routd.*` |
| `dashd`       | `onbod.*`, `routd.*`            |
| `discd`       | `discd.*`                       |
| `emaid`       | `emaid.*`                       |
| `gitd`        | `routd.*`, `gitd.*`             |
| `onbod`       | `onbod.*`, `routd.*`            |
| `proxyd`      | `routd.*`                       |
| `routd`       | `routd.*`                       |
| `runed`       | `runed.*`                       |
| `slakd`       | `routd.*`, `slakd.*`            |
| `teled`       | `teled.*`                       |
| `timed`       | none                            |
| `ttsd`        | none                            |
| `webd`        | `routd.*`                       |

Three rows need care:

- `gitd` and `slakd` open the tables of routd directly, see `gitd/main.go` and `slakd/main.go`. A policy with only their own prefix lets them
  authenticate and then read nothing.
- `timed` needs no key. It writes through the HTTP interface of the owner with
  a service token. `ttsd` opens no store.
- `cmd/arizuko` is the operator command on the host. Its key goes in
  `env/operator.env`, not in the file of a daemon.

Every other adapter in the table gets its own prefix and nothing else. `whapd`
and `googd` open no store and need no key: none of them is in
`StoreReach`, and none calls `OpenAdapterStore`.

For each row, create the user, put the policy, and mint the key. This example
is for `routd`:

```bash
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  create-user --account $STORE_ACCOUNT_ID --name routd

sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  put-user-policy --account $STORE_ACCOUNT_ID --name routd --policy-name tables \
  --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:*","Resource":["arn:aws:dynamodb:*:*:table/routd.*"]}]}'

sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  create-access-key --account $STORE_ACCOUNT_ID --name routd
```

Write the printed pair into `env/routd.env` as `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`. A later `generate` keeps them. A daemon with a policy
that is too narrow does not fail at deploy. It fails the first time it touches
a table that the policy does not cover. Restart one daemon at a time and watch
it answer.

A daemon that has the server address and no key fails earlier and does more
damage. `dynstore` refuses to dial and prints `endpoint "https://extenddb:18443"
takes an access key and a secret`. The daemon exits at boot. Because `docker
compose up` runs under systemd, that one exit restarts the whole instance in a
loop. On sloth, two of fifteen containers stayed up and `systemctl is-active`
stayed on `activating`, see BUGS `L406`.

`arizuko generate` logs an ERROR line for every such daemon before you deploy:
`store enrolment is partial: these daemons reach the store and hold no key`.
Then it continues and writes the compose file. Read that line before you
restart. Every daemon in the table except `timed` and `ttsd` needs a key.

The server requires TLS and refuses to start without it. Every daemon therefore
needs the certificate that the `init` run made:

```bash
echo "STORE_CA=$(sudo base64 -w0 store/extenddb/.extenddb/tls/cert.pem)" | sudo tee -a .env
echo 'DYNAMODB_URL=https://extenddb:18443' | sudo tee -a .env
./arizuko generate <name>
```

`DYNAMODB_URL` names the server that every daemon opens its tables on. A daemon
without it refuses to start and says so.

`generate` also writes the address of the server as the host sees it into
`env/operator.env`. The `arizuko` command on the host reads that address,
because the service name `extenddb` resolves only inside the instance network.
The published port is 18443 by default, and two instances cannot share it. A
second instance on the same host must set `STORE_HOST_PORT` in its `.env`
first. Never edit `template/services/extenddb.yml`. `generate` never updates an
edited fragment again, so the fragment stops taking store changes.

### Share one store server between instances

One server and one PostgreSQL pair for each instance wastes resources when a
host runs more than two instances. The account is already the tenant boundary,
so a shared server costs no isolation.

A shared PostgreSQL usually withholds `CREATEDB`. `init` needs that right only
to create the two databases. `init` also refuses to run when the databases
already exist, and it says `Run 'destroy' first`. The `destroy` command drops
them. When the role cannot create databases, you create them and finish the
deployment by hand. The work is four rows and nothing else.

1. **Create the pair** as a role that has `CREATEDB`, and give both databases to
   the application role:

   ```sql
   CREATE ROLE dynamo LOGIN PASSWORD '<pw>';
   CREATE DATABASE dynamo OWNER dynamo;
   CREATE DATABASE dynamo_catalog OWNER dynamo;
   ```

2. **Apply the catalog schema** with `extenddb migrate`. The role owns both
   databases, so it needs no other grant.

3. **Generate the certificate.** TLS is required. Only `init` writes a
   certificate, and it writes the certificate before it touches PostgreSQL.
   Run it once for that purpose. It then fails with `AlreadyExists`, and that
   failure is expected. Give it every address that a client uses:

   ```bash
   sudo docker run --rm --network=host -u 1000:1000 \
     -v /srv/data/extenddb:/var/lib/extenddb \
     -e EXTENDDB_PG_PASSWORD="$PW" -e EXTENDDB_APP_PASSWORD="$PW" \
     ghcr.io/extenddb/extenddb-postgres:0.1.11 init \
     --pg-host <pg-host> --pg-port 5432 --pg-user dynamo --extenddb-user dynamo \
     --data-db dynamo --catalog-db dynamo_catalog \
     --bind-addr 0.0.0.0 --tls-san <host-ip> --tls-san <host-fqdn> \
     --config /var/lib/extenddb/extenddb.toml --overwrite
   ```

4. **Write the four rows** that `init` would write. Without them, the server
   looks for the data tables in the catalog and exits with
   `Encryption key not found in settings table`:

   ```sql
   INSERT INTO settings (key, value) VALUES
     ('encryption_key',                  '<openssl rand -base64 32>'),
     ('data_database_name',              'dynamo'),
     ('data_database_connection_string', 'postgresql://dynamo:<pw>@<pg-host>:5432/dynamo'),
     ('default_account_id',              '<12 digits>')
   ON CONFLICT (key) DO UPDATE SET value = excluded.value;
   ```

   `encryption_key` is the root secret of the deployment. It encrypts every
   stored credential. Generate it once. If you lose it, you lose every
   credential.

5. **Write `extenddb.toml`** with mode `0600`, owned by the service user. The
   `connection_string` names the catalog. The port must be unique on the host.
   The fleet uses 18443, 18444 and 18445.

   ```toml
   [server]
   bind_addr = "0.0.0.0"
   port = 18445

   [server.tls]
   cert_path = "/var/lib/extenddb/.extenddb/tls/cert.pem"
   key_path  = "/var/lib/extenddb/.extenddb/tls/key.pem"

   [storage]
   backend = "postgres"

   [storage.postgres]
   connection_string = "postgresql://dynamo:<pw>@<pg-host>:5432/dynamo_catalog"
   ```

6. **Run the server.** The command must carry `--foreground`. Without it, the
   server daemonizes, and systemd reports `PID file not created within 5
seconds`. The image `ENTRYPOINT` is already `extenddb`, so the command is
   `serve --config … --foreground`. Never write `extenddb serve …`.

7. **Verify the server** before you point anything at it:

   ```bash
   sudo docker run --rm --network=host -u 1000:1000 \
     -v /srv/data/extenddb:/var/lib/extenddb \
     ghcr.io/extenddb/extenddb-postgres:0.1.11 verify \
     --config /var/lib/extenddb/extenddb.toml
   ```

   The command must print `HEALTHY: All checks passed`.

An instance then joins with its own account and its own key pair, and it points
`DYNAMODB_URL` at this server. You create that account and mint that key by
hand with `extenddb manage`. `arizuko` has no subcommand for it, see BUGS `L319`.

## Set the model key and the channel token in .env

Edit `/srv/data/arizuko_<name>/.env`. `create` already wrote `ASSISTANT_NAME`.
You must add the following settings:

```bash
# Display name of the assistant. Change it if you want another name.
ASSISTANT_NAME=MyBot

# Model credential: set one of the model keys
ANTHROPIC_API_KEY=sk-ant-...            # an API key
# OR
CLAUDE_CODE_OAUTH_TOKEN=...             # a claude.ai subscription token

# Channel credentials: set at least one (see the next section)
TELEGRAM_BOT_TOKEN=...                  # for example Telegram

# The turn substrate: the guest kernel and the release rootfs that
# `make images` and `make kernel` exported. runed refuses to start without both.
RUNED_KERNEL_DIR=/srv/data/arizuko_<name>/images/kernel-<hash>
RUNED_ROOTFS_DIR=/srv/data/arizuko_<name>/images/rootfs-<hash>
```

The model keys that arizuko accepts are `ANTHROPIC_API_KEY`,
`ANTHROPIC_OAUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY` and
`CODEX_API_KEY`, see `store.EnvProfileKeys`. The default agent harness is Claude
Code. `ARIZUKO_BACKEND=codex` or `pi` selects another harness, and the CLI of
that harness must be in the agent image.

Keep capability tokens in encrypted group secrets. For shell access, follow
the paired secret and grant recipe in [Secret injection](SECURITY.md#secret-injection).

These settings are optional:

```bash
# Required only for the web interface and OAuth login
WEB_HOST=myhost.example.com

# `arizuko create` writes these. Change them only for a reason.
# AUTH_SECRET=...
# SECRETS_KEY=...

# Host path of the arizuko checkout (this repository). `arizuko packages add`
# links the compose fragment of an adapter at this path and does not copy it,
# so a template fix reaches the instance on the next `arizuko generate`.
# Required when `arizuko generate` runs inside a container, because the
# container cannot see the checkout.
HOST_APP_DIR=/path/to/arizuko
```

## Get a credential for the channel adapter

Add the credentials for the adapters that you choose:

| Adapter              | Env vars                                                                                                  | Where to get them                  |
| -------------------- | --------------------------------------------------------------------------------------------------------- | ---------------------------------- |
| **teled** (Telegram) | `TELEGRAM_BOT_TOKEN`                                                                                      | @BotFather, then /newbot           |
| **discd** (Discord)  | `DISCORD_BOT_TOKEN`                                                                                       | discord.com/developers, Bot, Token |
| **slakd** (Slack)    | `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`                                                                 | api.slack.com, App, OAuth          |
| **emaid** (Email)    | `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`                                   | Your email provider                |
| **whapd** (WhatsApp) | none; the adapter pairs with a QR code                                                                    | `./arizuko pair <name> whapd`      |
| **gitd** (GitHub)    | none in `.env`; `./arizuko github <name> add …` mints the secret. `GITHUB_TOKEN` is optional, for replies | GitHub, Settings, Webhooks or App  |

Telegram is the simplest choice. It needs one token from @BotFather.

## Enable the channel adapter

Add the package of the adapter to the instance:

```bash
./arizuko packages <name> list           # available and enabled packages
./arizuko packages <name> add teled      # Telegram
./arizuko packages <name> add discd      # Discord
./arizuko packages <name> add slakd      # Slack
./arizuko packages <name> add gitd       # GitHub webhooks, then `arizuko github <name> add …`
```

No adapter starts until you add its package. Each adapter needs its package in
`services/`.

Mint the access key of the adapter into `env/<adapter>.env` before you enable
it. See "The per-daemon store keys". An adapter without a key exits
at boot, and the whole instance restarts in a loop. `arizuko generate` names
that adapter first.

## Add a model gateway

This step is optional. By default, every agent guest receives the model
credential from `.env`. The `litellm` package moves the credential into a
gateway container. The agent then gets a virtual key that only the gateway
accepts.

```bash
./arizuko packages <name> add litellm
# generated MODEL_GATEWAY_KEY for litellm into .env
./arizuko generate <name>
```

`add` writes `MODEL_GATEWAY_KEY` and `MODEL_GATEWAY_DB_PASSWORD` into
`/srv/data/arizuko_<name>/.env`. The `litellm.yaml` file of the package
declares both as `generate`. Every `generate` sets
`MODEL_GATEWAY_URL=http://litellm:4000` unless `.env` names another gateway. To
rotate a secret, delete its line. The next `generate` mints a new value.

`MODEL_GATEWAY_KEY` is the master key of the gateway. It goes to `routd`, which
mints one virtual key for each turn. The key is limited by the remaining daily
budget of the folder and expires with the run. `runed` gets only the URL, and
the guest gets only the minted key. The package also starts `litellm_db`, a
PostgreSQL database in a named volume. The gateway keeps its keys and its spend
log there. That database is separate from the store server of the instance.

The gateway reads `ANTHROPIC_API_KEY` from the same `.env`. When the API key is
not set, it reads `CLAUDE_CODE_OAUTH_TOKEN`. The choice has a consequence:

- **An API key.** Every harness works through the gateway (`claude`, `codex` and
  `pi`), and Anthropic bills each token.
- **A subscription OAuth token.** Only first-party clients use plan limits. On
  2026-09-04 a measurement showed that `claude` and `codex` complete turns on
  plan quota. `pi` received HTTP 400 from Anthropic with the text "Third-party
  apps now draw from your extra usage, not your plan limits". `pi` needs
  extra-usage credits or an API key. arizuko sends no forged client identity to
  change that.

With a gateway, litellm keeps the ledger and arizuko stores no spend. An
`arizuko budget` cap becomes the team budget of the folder at the gateway. The
gateway enforces the cap on the request itself, using a number that the agent
cannot choose. The dashboard reads the same number back.

Without a gateway, nothing meters spend. The page `/dash/usage/` says so and
shows no total. Nothing enforces a cap on such an instance, and `routd` logs
that fact at ERROR every time the gate runs. A cap needs a gateway.

The JSON log lines of the gateway go to `journalctl -u arizuko_<name>` with the
logs of every other daemon. When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, the
spans for tokens and cost of each request go to the OTLP collector.

## Register the first group

```bash
# Telegram. Add @userinfobot to your group to get the chat id.
# The JID uses the positive number and drops the minus sign of a group id.
./arizuko group <name> add telegram:group/123456789 main

# Discord
./arizuko group <name> add discord:<guild_id>/<channel_id> main

# Slack
./arizuko group <name> add slack:<team_id>/channel/<channel_id> main
```

## Run the instance

```bash
./arizuko run <name>        # writes docker-compose.yml and starts the containers in the foreground
```

The command runs `docker compose up` in the foreground. Press Ctrl+C to stop.
To run it in the background, use job control or run it under systemd as shown
in the next section:

```bash
./arizuko run <name> &
```

### systemd keeps the instance running

systemd keeps `arizuko run` up and sends its output to the journal:

```bash
sudo tee /etc/systemd/system/arizuko_<name>.service <<EOF
[Unit]
Description=arizuko <name>
After=docker.service
Requires=docker.service

[Service]
Type=simple
WorkingDirectory=/srv/data/arizuko_<name>
ExecStart=/path/to/arizuko run <name>
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now arizuko_<name>
sudo systemctl status arizuko_<name>
sudo journalctl -u arizuko_<name> -f
```

## Check that the instance answers

```bash
# Running daemon containers
docker compose -f /srv/data/arizuko_<name>/docker-compose.yml ps

# Health of the daemons
docker ps --filter "name=arizuko_<name>" --format "{{.Names}} {{.Status}}"

# Smoke test
make smoke SMOKE_INSTANCE=<name>
```

The containers of routd, authd, runed and the adapter must show
`(healthy)`. A turn is not a container. It is a KVM guest that `runed` starts
and removes for each turn. To check a turn, send the agent a message. Then read
`journalctl -u arizuko_<name>` or open `/dash/runed/` and look for the spawn.
`docker ps` does not show a turn.

## An upgrade rebuilds the images and restarts the instance

Run the block in the arizuko checkout. The name of each guest artifact
directory contains a hash of its content. A new artifact is a new directory,
and `.env` must name it.

```bash
git pull                                          # the new release
make build                                        # ./arizuko and the daemon binaries
sudo make images DATA_DIR=/srv/data/arizuko_demo  # prints rootfs: <dir>
sudo make kernel DATA_DIR=/srv/data/arizuko_demo  # prints kernel: <dir>
sudoedit /srv/data/arizuko_demo/.env              # RUNED_ROOTFS_DIR, RUNED_KERNEL_DIR
./arizuko generate demo                           # read every warning it prints
sudo systemctl restart arizuko_demo               # arizuko run regenerates and starts
./arizuko send demo main "ping" --wait            # one reply proves the path
```

- `make images` exports a new rootfs tree only when the `arizuko-ant` image
  changed. It removes no tree. To roll back the guest, set `RUNED_ROOTFS_DIR`
  to an older tree that is still on disk and restart. `arizuko images <name>
--prune` removes every tree that `.env` does not name.
- `make kernel` builds only when `kernel/Dockerfile`, `kernel/arizuko.fragment`,
  `kernel/check-config.sh` or `kernel/guest-init/*.go` changed. Otherwise it
  prints `is current` and `RUNED_KERNEL_DIR` stays as it is.
- `arizuko generate` names every fragment that is frozen and every daemon that
  has no store key. Fix both before the restart. A daemon without a store key
  restarts the whole instance in a loop.
- A release that increases `MIGRATION_VERSION` makes `routd` queue one
  `/migrate` turn for every folder. Replies wait behind those turns. The queue
  is empty when `pgrep -c qemu-system-x86` prints 0.
- A maintainer's release runs the five post-deploy gates in
  [`.claude/skills/eval/gates.md`](.claude/skills/eval/gates.md). The last gate,
  `anteval --smoke`, is the only gate that proves that a live agent answers.

## A tar or an archive restores an instance

A tar of the stopped data directory is the whole instance. `arizuko archive`
makes a logical copy. `archive apply` restores that copy into the same instance
or into another instance.

### A tar of the stopped instance holds every row

The tar carries every row, because the PostgreSQL data of the store server
lives under `store/`. Stop the instance first. PostgreSQL writes its files
while it runs, so a tar of a running instance is not a backup.

```bash
sudo systemctl stop arizuko_<name>
sudo tar -C /srv/data -czf arizuko_<name>-$(date +%F).tar.gz arizuko_<name>
sudo systemctl start arizuko_<name>
```

The tar holds these items:

- the PostgreSQL data in `store/extenddb_db/`
- the config and TLS key of the server in `store/extenddb/`
- the group folders
- `.env`, with `SECRETS_KEY`
- `images/`

Every rootfs tree in `images/` is about 7 GB.
`--exclude=arizuko_<name>/images` leaves them out. A restore then runs
`make images` and `make kernel` and sets the new paths in `.env`. An instance
on a shared store server keeps its rows on that server, so its tar holds no
rows.

To restore, move the current directory aside and unpack the tar in its place:

```bash
sudo systemctl stop arizuko_<name>
sudo mv /srv/data/arizuko_<name> /srv/data/arizuko_<name>.before-restore
sudo tar -C /srv/data -xzf arizuko_<name>-<date>.tar.gz
sudo systemctl start arizuko_<name>
```

### An archive copies one instance into another

`arizuko archive export <name> [file] [--quiesced]` writes one tar. Without
`file`, it writes the tar to standard output. The tar holds these entries:

| Entry                | Holds                                                                             |
| -------------------- | --------------------------------------------------------------------------------- |
| `archive.yaml`       | `format_version` and `consistency`                                                |
| `routd.yaml`         | the config rows of routd, the document that `arizuko export` writes               |
| `onbod.yaml`         | the config rows of onbod                                                          |
| `routd.secrets.yaml` | secret values, sealed; route token hashes; invites; pending onboarding admissions |
| `routd.messages.jl`  | the whole message history                                                         |
| `groups.tar`         | every `groups/<folder>/` tree                                                     |

The archive does not hold the stores of `authd` and `runed`, `.env`,
`SECRETS_KEY` or the guest artifacts. Export reads the live store, so the store
server must run. Without `--quiesced`, `consistency` is `live`, and each
subsystem is a snapshot from its own moment. The flag `--quiesced` only writes
`quiesced` into `archive.yaml`. It stops nothing and checks nothing, so use it
only when every writer is down. Export writes the file with mode 0600.

`arizuko archive apply <name> <archive.tar> [--force] [--stopped]` restores
the archive:

- It refuses an archive with a `format_version` newer than the binary. It also
  refuses a row that names a folder that is not a group.
- It applies the config documents through the same claim-gated path as
  `arizuko apply`. If a later subsystem fails, it rolls back. `--force` skips
  the content-hash check.
- It opens every secret with the `SECRETS_KEY` of the target before it writes
  any secret. A target with another key needs the source key in its keyring:
  `SECRETS_KEY=<target key>,<source key>`.
- It restores route tokens, invites and pending onboarding only with `--force`,
  and only into an empty table.
- It skips a group folder that exists and is not empty. `--force` overwrites
  the folder.
- It appends the message history and sets the cursor of each chat to the newest
  message. The restored agent does not answer old messages again.

Before apply overwrites the tree of a folder, it claims the run slot of that
folder from `runed` at `RUNED_URL`. No turn then writes the folder during the
restore. The default `RUNED_URL` is `http://runed:8080`. That is a compose name
that the host cannot resolve, so apply stops when it cannot claim the slot.
`--stopped` skips the claim. Stop the instance, start only the store server,
and then apply:

```bash
sudo systemctl stop arizuko_<name>
cd /srv/data/arizuko_<name>
sudo docker compose up -d extenddb_db extenddb
cd -
./arizuko archive apply <name> <archive.tar> --stopped
sudo systemctl start arizuko_<name>
```

The archive carries the config rows. It does not carry the runtime settings of
a daemon, such as its port, see BUGS `L249`.

## Find out why an agent does not reply

The health checks can be green while the agent stays silent. Check these four
causes in order:

1. **`runed` refuses to start.** `journalctl` shows `this host cannot run
turns; N precondition(s) missing:` and lists every missing item at once.
   The items are a missing `/dev/kvm`, an unset `RUNED_KERNEL_DIR` or
   `RUNED_ROOTFS_DIR`, a missing `egress`, `iptables` or `ip` binary, and
   kernel or rootfs artifacts that are not built. Fix the named items and
   restart. A fix for one item never hides the others, see `qemuPreconditions` in
   `runed/qemuhost.go`.
2. **An adapter is disconnected.** `docker ps` shows `(unhealthy)`, or the
   `/health` of the adapter returns 503 with `{status:"disconnected"}` while
   the process is up. An example is `whapd` that waits for a QR scan, or a
   dropped stream. Read the logs of the adapter, not the logs of `routd`:
   `sudo curl -s -o /dev/null -w '%{http_code}\n' http://localhost:<port>/health`
3. **An adapter is silent.** Read the recent log lines that are not health
   checks:
   `sudo journalctl -u arizuko_<name> --since "10 min ago" | grep -viE health`
4. **A turn fails to start.** The log of `runed` shows `turn did not start`
   with the folder, the handle and the reason. The reason is a failed start of
   `virtiofsd`, of the tap, or of the proxy. An image without `iptables`,
   `iproute2` or the `egress` binary causes this failure. Rebuild with
   `sudo make images DATA_DIR=...`.

Read `journalctl -u arizuko_<name>` and never `docker logs`. A host whose Docker
log driver is `none` answers `docker logs` with nothing on standard output.

[howto/operate.html](template/web/pub/arizuko/howto/operate.html) lists every
other incident with the line to read, the cause and the fix.

## The whole install in one block

The sections above explain each line.

<!-- twin:install -->

```bash
git clone https://github.com/kronael/arizuko && cd arizuko
make build                                        # ./arizuko + daemon binaries
sudo make images DATA_DIR=/srv/data/arizuko_demo  # daemon images + the guest rootfs
sudo make kernel DATA_DIR=/srv/data/arizuko_demo  # the guest kernel, built once
./arizuko create demo                             # /srv/data/arizuko_demo + .env
./arizuko packages demo add extenddb              # the store server, two passwords
./arizuko generate demo
cd /srv/data/arizuko_demo
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb_db
sudo docker compose run --rm extenddb init \
  --pg-host extenddb_db --pg-port 5432 \
  --pg-user arizuko_demo --pg-pass "$STORE_DB_PASSWORD" \
  --extenddb-user arizuko_demo_app --extenddb-pass "$STORE_APP_PASSWORD" \
  --bind-addr 0.0.0.0 --tls-san extenddb \
  --config /var/lib/extenddb/extenddb.toml
vim .env    # STORE_ADMIN_PASSWORD and STORE_ACCOUNT_ID, which init prints once;
            # ASSISTANT_NAME, a model key, a bot token, RUNED_KERNEL_DIR, RUNED_ROOTFS_DIR
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_demo \
  extenddb settings -c /var/lib/extenddb/extenddb.toml set control_plane_delay_seconds 0
echo "STORE_CA=$(sudo base64 -w0 store/extenddb/.extenddb/tls/cert.pem)" | sudo tee -a .env
echo 'DYNAMODB_URL=https://extenddb:18443' | sudo tee -a .env
# one store key per daemon: INSTALL.md § The per-daemon store keys
cd -
./arizuko packages demo add teled                 # one channel adapter
./arizuko group demo add telegram:group/123456789 main
./arizuko run demo                                # generate compose, docker compose up
```

<!-- /twin -->
