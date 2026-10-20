# Installing arizuko

From a bare Linux host to a first reply. Written so a person, or an assistant
helping one, can follow it top to bottom without another document open.

## Prerequisites checklist

Run these checks and note which fail:

```bash
# 1. OS (Linux x86_64 required)
uname -s -m   # expect: Linux x86_64

# 2. Go 1.21+ (any version that understands toolchain switching)
go version    # missing = blocker; <1.21 = blocker
              # go.mod pins `toolchain go1.27rc2`. GOTOOLCHAIN=auto (the
              # default) fetches that toolchain on first build, so an older
              # Go is fine. GOTOOLCHAIN=local or an offline box is NOT —
              # install go1.27rc2 by hand (see "Go missing or too old")

# 3. Docker daemon running
docker info >/dev/null 2>&1 && echo "docker ok" || echo "docker MISSING"

# 4. Docker Compose v2.20+
docker compose version   # need v2.20+ (generated compose uses top-level `include:`);
                         # "command not found" = blocker, < 2.20 = upgrade first

# 5. Docker socket access (for arizuko run)
# User must either be in docker group OR run with sudo
groups | grep -q docker && echo "docker group ok" || echo "not in docker group (will need sudo)"

# 6. git + make
git --version && make --version | head -1

# 7. Data directory writable
# Default: /srv/data — needs write access for arizuko create
# Alternative: set PREFIX env to use a different path
ls -ld /srv/data 2>/dev/null || echo "/srv/data does not exist"

# 8. KVM (the turn substrate: one guest per turn)
ls -l /dev/kvm /dev/vhost-vsock /dev/net/tun 2>&1
# missing /dev/kvm = blocker (see "KVM not available" below); the other two
# come from the kernel modules `vhost_vsock` and `tun`, both usually built in

# 9. Memory for the guests
grep MemTotal /proc/meminfo
```

Size the host before you set the cap: runed refuses to start when
`RUNED_GUEST_MEM` × `MAX_CONCURRENT_CONTAINERS` exceeds `MemTotal`
(`runed/qemuhost.go` `memoryFits`). The defaults, 2 GiB × 5, need 10 GiB.
Disk: each release rootfs is about 8.3 GB, and two are kept.

## Fixing blockers

### Go missing or too old

Use **`g`** — a minimal Go version manager that installs without root and
manages multiple versions cleanly:

```bash
curl -sSL https://raw.githubusercontent.com/stefanmaric/g/main/bin/g-install | bash
# Re-source your shell profile (path the installer just printed), then:
g install 1.26.5
g set 1.26.5
go version   # verify: go1.26.5 linux/amd64
```

`g` adds itself and Go to `~/.local/bin` (or `~/go` by default). No sudo
needed, no system Go touched.

`go.mod` pins `toolchain go1.27rc2`, a release candidate, because arizuko uses
Go 1.27's standard library. The `go` directive is `go 1.27`, because it does
not accept an `rc` version. `g` carries only stable releases, so install the
latest stable as above and let `GOTOOLCHAIN=auto` (Go's default) fetch the
pinned toolchain on the first `go build`.

Two more pins move with that toolchain:

- **The Docker base image is pinned by hand.** Each Dockerfile that builds the
  root module names `golang:1.27rc2-alpine` (`Dockerfile`, `anteval/`,
  `egress/`, `linkd/`, `ttsd/`); nothing derives it from `go.mod`. Bump them
  in the same change as `go.mod`, or the image builds on a different
  toolchain than the host. `kernel/guest-init` is its own module on `go 1.25`
  and keeps its own base.
- **`golang.org/x/net` stays at v0.57.0 or newer** (`go.mod`). The 1.27 bump
  needed it: an older x/net does not export `http2.TrailerPrefix`, and grpc
  then fails to compile.

Two cases get no auto-fetch, and you install the toolchain by hand: the
machine sets `GOTOOLCHAIN=local`, or it has no network to `go.dev`.

```bash
go install golang.org/dl/go1.27rc2@latest && go1.27rc2 download
```

### Docker Compose v2 missing

```bash
mkdir -p ~/.docker/cli-plugins
curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o ~/.docker/cli-plugins/docker-compose
chmod +x ~/.docker/cli-plugins/docker-compose
docker compose version   # verify
```

### Docker socket access

`arizuko run` needs to talk to the Docker daemon. Either:

```bash
# Option A: add user to docker group (recommended, requires re-login)
sudo usermod -aG docker $USER
# Log out and back in, then verify:
docker ps

# Option B: run arizuko commands with sudo
sudo ./arizuko run <name>
```

### /srv/data not writable

Option A — chown to your user (simplest):

```bash
sudo mkdir -p /srv/data
sudo chown -R $(whoami):$(whoami) /srv/data
```

Option B — use a different prefix:

```bash
export PREFIX=~/.arizuko
# All arizuko commands will use $PREFIX instead of /srv/data
```

### KVM not available

`/dev/kvm` missing means the host cannot run the turn substrate at all — this
is a hardware/hypervisor blocker, not an arizuko setting:

```bash
ls -l /dev/kvm || echo "no /dev/kvm"
lsmod | grep kvm                          # kvm_intel or kvm_amd should be loaded
sudo modprobe kvm_intel || sudo modprobe kvm_amd
```

If both modprobes fail, check virtualization is enabled in the BIOS/UEFI
(Intel VT-x / AMD-V), and on a cloud instance or a VM confirm the provider
exposes nested virtualization — a plain (non-nested) guest has no `/dev/kvm`
of its own.

## Build

```bash
git clone https://github.com/kronael/arizuko && cd arizuko
make build                  # ./arizuko CLI + daemon binaries
make test                   # sanity check (go test ./... -short)
```

## Build Docker images

`DATA_DIR` names the directory the guest kernel and the release rootfs land
in — the same `/srv/data/arizuko_<name>` the next section creates (`mkdir -p`
makes it now if it does not exist yet). One value serves every instance that
runs the same `arizuko-ant` image, so a host with several instances can point
them all at one shared `DATA_DIR` instead of paying for the export twice.
What `make kernel` builds and why it is its own artifact:
[`kernel/README.md`](kernel/README.md).

```bash
sudo make images DATA_DIR=/srv/data/arizuko_<name>
# builds arizuko:latest + arizuko-ant:latest + adapters, then exports the
# release rootfs the turn substrate boots (DATA_DIR/images/rootfs-<hash>/)
sudo make kernel DATA_DIR=/srv/data/arizuko_<name>
# builds the pinned guest kernel + initrd once (DATA_DIR/images/kernel-<hash>/);
# rebuilds only when kernel/Dockerfile, kernel/arizuko.fragment or
# kernel/guest-init/*.go change — not on every release
```

`make images` keeps the two newest rootfs trees: the one a running turn may
have mounted, and the one before it, so a rollback needs no re-export. It then
runs `make rootfs-stale`, which names every older tree and deletes none: a
turn may hold one mounted, so removing it is yours. **Never run
`docker image prune -a` on an arizuko host**: no running container holds
`arizuko-ant`, so the prune deletes it, and the rootfs export reads that
image. `docker builder prune -f` is the lever that frees build cache.

This builds the complete image set. Individual adapter images (whapd, etc.)
are included. Only build separately if you need to rebuild one without
rebuilding all:

```bash
sudo make agent             # just arizuko-ant:latest (Claude Code agent)
sudo make -C whapd image    # just WhatsApp adapter
```

## Create instance

```bash
./arizuko create <name>     # e.g., ./arizuko create mybot
# Creates /srv/data/arizuko_<name>/ with .env template
```

This auto-generates `AUTH_SECRET` and `SECRETS_KEY`. You don't need to
create these manually.

`create` makes the instance's FILES and nothing else — the directory, the
`.env`, the compose fragments and the default group's own tree — so it runs
before the instance has a store server. Every ROW is its owner daemon's, written
at that daemon's first boot: routd seeds the ACL floor, the egress floor and the
`main` group there, carrying the `--product` choice `create` wrote into
`ARIZUKO_DEFAULT_PRODUCT`.

The instance directory, `${PREFIX:-/srv}/data/arizuko_<name>/`, holds:

```
.env                 config and anchor secrets; daemons read it from cwd
store/               extenddb/ server config and certificate, extenddb_db/
                     PostgreSQL data, <adapter>/ files an adapter owns
groups/<folder>/     the agent's home: session state in .claude/, diary,
                     logs, media/<YYYYMMDD>/ inbound attachments
ipc/<folder>/        the turn's MCP socket
web/pub/ web/priv/   the web trees the per-folder slots project into
```

Business state, such as gates, grants and onboarding, is
rows; infra toggles are env vars.

## The store server

Every owner's tables live on a DynamoDB-protocol server — ExtendDB over
PostgreSQL. A deployment is an ACCOUNT on that server, never a database or a
PostgreSQL role, so **one server can hold every instance on a host**. The
figure at the top of [`objstore/README.md`](objstore/README.md) shows the
layers from a daemon down to PostgreSQL. Follow this section for a server of
the instance's own; follow "A shared server" below to put several instances on
one.

The package brings both containers; the server's own init run is the
operator's, because it prints an admin password once and mints one access key
per daemon.

```bash
./arizuko packages <name> add extenddb   # mints STORE_DB_PASSWORD + STORE_APP_PASSWORD
./arizuko generate <name>
cd /srv/data/arizuko_<name>
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb_db    # the instance's own PostgreSQL role
sudo docker compose run --rm extenddb init \
  --pg-host extenddb_db --pg-port 5432 \
  --pg-user arizuko_<name> --pg-pass "$STORE_DB_PASSWORD" \
  --extenddb-user arizuko_<name>_app --extenddb-pass "$STORE_APP_PASSWORD" \
  --bind-addr 0.0.0.0 --tls-san extenddb \
  --config /var/lib/extenddb/extenddb.toml
```

`init` prints the admin password and the account id once. Put both in `.env`
as `STORE_ADMIN_PASSWORD` and `STORE_ACCOUNT_ID` before you lose them, then
start the server and turn the control-plane delay off, so a table is servable
the moment `CreateTable` returns:

```bash
set -a; . ./.env; set +a                          # load the STORE_* values into this shell
sudo docker compose up -d extenddb
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb settings -c /var/lib/extenddb/extenddb.toml set control_plane_delay_seconds 0
```

#### The per-daemon store keys

Each daemon signs as itself: its own IAM user, its own access key in its own
`env/<daemon>.env`, and a policy bounded to the tables it actually opens. The
account is the tenancy boundary; the POLICY is the only boundary inside it.
`dynstore.Prefix` puts the owner in front of every entity name, so `routd.acl`
is routd's and a resource of `routd.*` means exactly "routd's tables".

**Do not give a key `dynamodb:*` on `*`.** That is a key with attribution and
no isolation — one compromised adapter reaches every owner's tables on the
instance, and it is what all 19 users on sloth carried (BUGS `L318`).

The reach below is MEASURED from the call sites, not described: `StoreReach` in
`store/owner_reach.go` declares it and `TestStoreReachMatchesTheCallSites` fails
when a daemon opens an owner the table does not list, or lists one it does not
open. Change a daemon's store use and that test tells you which policy to
rewrite.

| daemon        | policy resources                |
| ------------- | ------------------------------- |
| `authd`       | `authd.*`                       |
| `bskyd`       | `bskyd.*`                       |
| `cmd/arizuko` | `authd.*`, `onbod.*`, `routd.*` |
| `dashd`       | `onbod.*`, `routd.*`            |
| `discd`       | `discd.*`                       |
| `emaid`       | `emaid.*`                       |
| `gitd`        | `routd.*`, `gitd.*`             |
| `linkd`       | `linkd.*`                       |
| `onbod`       | `onbod.*`, `routd.*`            |
| `proxyd`      | `routd.*`                       |
| `reditd`      | `reditd.*`                      |
| `routd`       | `routd.*`                       |
| `runed`       | `runed.*`                       |
| `slakd`       | `routd.*`, `slakd.*`            |
| `teled`       | `teled.*`                       |
| `timed`       | —                               |
| `ttsd`        | —                               |
| `webd`        | `routd.*`                       |

Two of those need care. **`gitd` and `slakd` carry `routd.*` on top of their own
adapter prefix** — both open routd's tables directly (`gitd/main.go`,
`slakd/main.go`), so an adapter-prefix-only policy leaves them authenticated and
unable to read anything. Every other adapter gets its own prefix and nothing
else. **`timed` and `ttsd` need no key at all**: `timed` writes through the
owner's HTTP API with a service token, which is the split write-discipline
working, and `ttsd` opens no store.

`cmd/arizuko` is the operator CLI on the host, and its key goes in
`env/operator.env` rather than a daemon's file.

For each row, create the user, put the policy and mint the key:

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
`AWS_SECRET_ACCESS_KEY`; a later `generate` carries them forward. A daemon whose
policy is too narrow does NOT fail at deploy. It fails the first time it
touches a table it was never granted, so roll one daemon at a time and watch
it answer.

A daemon with the server's address and no key at all fails differently, and
harder. `dynstore` refuses to dial (`endpoint "https://extenddb:18443" takes an
access key and a secret`), the daemon exits at boot, and because `docker
compose up` runs under systemd, that one exit restart-loops the whole instance:
two containers up out of fifteen and `systemctl is-active` stuck on
`activating` (BUGS `L406`, measured on sloth). `arizuko generate` names every
such daemon before you deploy, at ERROR in its log (`store enrolment is
partial: these daemons reach the store and hold no key`), and then continues
and writes the compose file anyway. Read that line before you restart. The reach table above says who needs a key: every daemon but `timed`
and `ttsd`, adapters included.

TLS is mandatory — the server refuses to start without it — so every daemon
needs the certificate the init run generated:

```bash
echo "STORE_CA=$(sudo base64 -w0 store/extenddb/.extenddb/tls/cert.pem)" | sudo tee -a .env
echo 'DYNAMODB_URL=https://extenddb:18443' | sudo tee -a .env
./arizuko generate <name>
```

`DYNAMODB_URL` names the server every daemon opens its owner on. A daemon
without it refuses to start and says so: there is no file to fall back to.

`generate` writes the host's own address for the same server into
`env/operator.env`, which is where `arizuko` on the host reads it: the service
name above resolves inside the instance network and nowhere else. **A second
instance on this host must set `STORE_HOST_PORT` in its `.env` first** — the
published port defaults to 18443 and two instances cannot share it. Set the
key, never edit `template/services/extenddb.yml`: an edited fragment is frozen
from the catalog and stops taking store changes.

### A shared server for several instances

One engine and one PostgreSQL pair per instance is a waste once a host carries
more than two. The account is already the tenancy boundary, so the shared
shape costs nothing in isolation. The reasoning is in
`specs/6/43-shared-store-server.md`; the procedure is here.

**A shared PostgreSQL usually withholds `CREATEDB`.** `init` needs it only to
create the two databases, and `init` refuses to run against databases that
already exist — it says `Run 'destroy' first`, and `destroy` drops them. Where
the role cannot create databases, the operator creates them and completes the
deployment by hand. That is four rows, and nothing else.

1. **Create the pair**, as a role that has `CREATEDB`, and hand both to the
   application role:

   ```sql
   CREATE ROLE dynamo LOGIN PASSWORD '<pw>';
   CREATE DATABASE dynamo OWNER dynamo;
   CREATE DATABASE dynamo_catalog OWNER dynamo;
   ```

2. **Apply the catalog schema** with `extenddb migrate`. The role owns both
   databases, so no further grant is needed.

3. **Generate the certificate.** TLS is mandatory. `init` is the only
   subcommand that writes a certificate, and it writes it before it touches
   PostgreSQL, so run it once for that alone — it will then fail on
   `AlreadyExists`, which is expected here. Give it every address a client
   will use:

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

4. **Write the four rows** `init` would have written. Without them the server
   looks for the data tables in the catalog and exits on
   `Encryption key not found in settings table`:

   ```sql
   INSERT INTO settings (key, value) VALUES
     ('encryption_key',                  '<openssl rand -base64 32>'),
     ('data_database_name',              'dynamo'),
     ('data_database_connection_string', 'postgresql://dynamo:<pw>@<pg-host>:5432/dynamo'),
     ('default_account_id',              '<12 digits>')
   ON CONFLICT (key) DO UPDATE SET value = excluded.value;
   ```

   `encryption_key` is the deployment's root secret. It encrypts every stored
   credential, it is generated once, and losing it loses them all.

5. **Write `extenddb.toml`**, mode `0600`, owned by the service uid. The
   `connection_string` names the **catalog**, and the port is unique on the
   host — the fleet holds 18443, 18444 and 18445:

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

6. **Run it.** The command must carry `--foreground`; without it the server
   daemonizes and systemd reports `PID file not created within 5 seconds`. The
   image `ENTRYPOINT` is already `extenddb`, so the command is
   `serve --config … --foreground`, never `extenddb serve …`.

7. **Verify before pointing anything at it:**

   ```bash
   sudo docker run --rm --network=host -u 1000:1000 \
     -v /srv/data/extenddb:/var/lib/extenddb \
     ghcr.io/extenddb/extenddb-postgres:0.1.11 verify \
     --config /var/lib/extenddb/extenddb.toml
   ```

   It must print `HEALTHY: All checks passed`.

An instance then joins with its own account and its own key pair, and points
`DYNAMODB_URL` at this server. Creating that account and minting that key is a
hand procedure through `extenddb manage`; `arizuko` has no enrolment
subcommand (BUGS `L319`).

## Configure .env

Edit `/srv/data/arizuko_<name>/.env`. The only required additions are:

```bash
# Display name (required)
ASSISTANT_NAME=MyBot

# Claude API credential (required — pick one)
ANTHROPIC_API_KEY=sk-ant-...            # direct API key
# OR
CLAUDE_CODE_OAUTH_TOKEN=...             # from claude.ai subscription

# Channel credentials (at least one required — see next section)
TELEGRAM_BOT_TOKEN=...                  # example: Telegram

# The turn substrate: where `make images`/`make kernel` exported the release
# rootfs and the guest kernel (required — runed refuses to start without both)
RUNED_KERNEL_DIR=/srv/data/arizuko_<name>/images/kernel-<hash>
RUNED_ROOTFS_DIR=/srv/data/arizuko_<name>/images/rootfs-<hash>
```

Optional settings:

```bash
# Only required if using web interface / OAuth login
WEB_HOST=myhost.example.com

# Already auto-generated by arizuko create — don't change unless you know why
# AUTH_SECRET=...
# SECRETS_KEY=...

# Host path of the arizuko checkout (this repo). `arizuko packages add`
# links an adapter's compose fragment at this path rather than copying it,
# so a template fix reaches this instance on the next `arizuko generate`.
# Required when `arizuko generate` runs inside a container, which cannot
# see the checkout on its own.
HOST_APP_DIR=/path/to/arizuko
```

## Channel adapter credentials

Add credentials for your chosen adapter(s):

| Adapter              | Env vars                                                                                                  | How to get                           |
| -------------------- | --------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| **teled** (Telegram) | `TELEGRAM_BOT_TOKEN`                                                                                      | @BotFather → /newbot                 |
| **discd** (Discord)  | `DISCORD_BOT_TOKEN`                                                                                       | discord.com/developers → Bot → Token |
| **slakd** (Slack)    | `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`                                                                 | api.slack.com → App → OAuth          |
| **bskyd** (Bluesky)  | `BLUESKY_HANDLE`, `BLUESKY_APP_PASSWORD`                                                                  | Settings → App passwords             |
| **emaid** (Email)    | `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`                                   | Your email provider                  |
| **whapd** (WhatsApp) | (none — pairs via QR)                                                                                     | `./arizuko pair <name> whapd`        |
| **gitd** (GitHub)    | (none in `.env` — `./arizuko github <name> add …` mints the secret; `GITHUB_TOKEN` optional, for replies) | GitHub → Settings → Webhooks / App   |

**Telegram is simplest** — just needs one token from @BotFather.

## Enable adapter

Add the package for your chosen adapter to the instance:

```bash
./arizuko packages <name> list           # available vs enabled
./arizuko packages <name> add teled      # Telegram
./arizuko packages <name> add discd      # Discord
./arizuko packages <name> add slakd      # Slack
./arizuko packages <name> add gitd       # GitHub webhooks (then `arizuko github <name> add …`)
```

Without this step, no adapter starts. Each adapter needs its package in
`services/`.

Every adapter but `ttsd` reads the store, so mint its access key into
`env/<adapter>.env` BEFORE you enable it (§ The per-daemon store keys). An
adapter enabled without a key exits at boot and restart-loops the whole
instance; `arizuko generate` names it first.

## Optional: model gateway

By default every agent guest receives the model credential from `.env`.
The `litellm` package moves it into a gateway container instead; the agent
gets a virtual key that only the gateway accepts (`specs/6/23-llm-gateway.md`).

```bash
./arizuko packages <name> add litellm
# generated MODEL_GATEWAY_KEY for litellm into .env
./arizuko generate <name>
```

`add` mints `MODEL_GATEWAY_KEY` and `MODEL_GATEWAY_DB_PASSWORD` into
`/srv/data/arizuko_<name>/.env` (the package's `litellm.yaml` declares both
`generate`), and every generate sets `MODEL_GATEWAY_URL=http://litellm:4000`
unless `.env` names another gateway. To rotate a secret, delete its line; the
next generate mints a new one.

`MODEL_GATEWAY_KEY` is the gateway's MASTER key and goes to routd, which mints
one virtual key per turn with it, bounded by that folder's remaining daily
budget and expiring with the run. runed gets only the URL; the guest gets
only the minted key. The package also starts `litellm_db`, the Postgres litellm
keeps those keys and its spend log in — that database is the gateway's
bookkeeping, in a named volume, and separate from the instance's own store
server.

The gateway reads `ANTHROPIC_API_KEY`, or `CLAUDE_CODE_OAUTH_TOKEN` when the
API key is unset, from the same `.env`. Which one you put there matters:

- **API key** — every harness (`claude`, `codex`, `pi`) works through the
  gateway, billed per token.
- **Subscription OAuth token** — only first-party clients draw on plan
  limits. Measured 2026-09-04: `claude` and `codex` complete turns on plan
  quota; `pi` gets HTTP 400 from Anthropic ("Third-party apps now draw from
  your extra usage, not your plan limits") and needs extra-usage credits or
  an API key. arizuko sends no forged client identity to change that.

With the gateway configured, litellm is the ledger and arizuko stores no
spend (`specs/6/39-gateway-owns-spend.md`). An `arizuko budget` cap becomes the
folder's team budget at the gateway, so it binds on the request itself, on a
number the agent cannot choose, and the dashboard reads the same figure back.

Without a gateway there is no meter. `/dash/usage/` says so instead of showing
a total, and a cap set on such an instance is enforced by nothing — routd logs
that at ERROR every time the gate runs. A cap needs a gateway.

The gateway's JSON log lines land in `journalctl -u arizuko_<name>` with every
other daemon, and its per-request token and cost spans go to the OTLP collector
when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.

## Register first group

```bash
# Telegram example (get chat ID by adding @userinfobot to your group)
# Format: telegram:group/<positive_id> (drop the leading minus)
./arizuko group <name> add telegram:group/123456789 main

# Discord example
./arizuko group <name> add discord:<guild_id>/<channel_id> main

# Slack example
./arizuko group <name> add slack:<team_id>/channel/<channel_id> main
```

## Run

```bash
./arizuko run <name>        # generates docker-compose.yml + starts containers (foreground)
```

This runs `docker compose up` in the foreground. Use Ctrl+C to stop.

For background operation:

```bash
./arizuko run <name> &      # background with job control
# OR create a systemd unit (see Run under systemd)
```

### Run under systemd

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

## Verify

```bash
# Check running daemon containers
docker compose -f /srv/data/arizuko_<name>/docker-compose.yml ps

# Check daemon health
docker ps --filter "name=arizuko_<name>" --format "{{.Names}} {{.Status}}"

# View logs
docker compose -f /srv/data/arizuko_<name>/docker-compose.yml logs --tail=50

# Run smoke test
make smoke SMOKE_INSTANCE=<name>
```

All core containers (routd, authd, runed, adapter) should show `(healthy)`.
A turn itself is not a container — it is a KVM guest runed starts and reaps
per turn (spec `6/41`) — so send the agent a message and check
`journalctl -u arizuko_<name>` or `/dash/runed/` for the spawn, not `docker
ps`.

## Upgrade

An upgrade rebuilds the images from the new checkout and restarts the
instance. Run the block in the arizuko checkout. Each guest artifact is a
directory whose name carries its content hash, so a new artifact is a new
directory, and `.env` must name it.

```bash
git pull                                          # the new release
make build                                        # ./arizuko + daemon binaries
sudo make images DATA_DIR=/srv/data/arizuko_demo  # prints rootfs: <dir>
sudo make kernel DATA_DIR=/srv/data/arizuko_demo  # prints kernel: <dir>
sudoedit /srv/data/arizuko_demo/.env              # RUNED_ROOTFS_DIR, RUNED_KERNEL_DIR
./arizuko generate demo                           # read every warning it prints
sudo systemctl restart arizuko_demo               # arizuko run regenerates, starts
./arizuko send demo main "ping" --wait            # one reply proves the path
```

- `make images` exports a new rootfs tree only when the `arizuko-ant` image
  changed. It keeps the newest two trees. To roll back the guest, set
  `RUNED_ROOTFS_DIR` to the older tree and restart.
- `make kernel` builds only when `kernel/Dockerfile`, `kernel/arizuko.fragment`,
  `kernel/check-config.sh` or `kernel/guest-init/*.go` changed. Otherwise it
  prints `is current` and `RUNED_KERNEL_DIR` stays as it is.
- `arizuko generate` names a fragment that is FROZEN and a daemon with no store
  key. Fix both before the restart. A daemon without a store key restart-loops
  the whole instance.
- A release that bumps `MIGRATION_VERSION` makes routd queue one `/migrate`
  turn for every folder. Replies wait behind them. The queue is empty when
  `pgrep -c qemu-system-x86` prints 0.
- A maintainer's release runs the five post-deploy gates in
  [`.claude/skills/eval/gates.md`](.claude/skills/eval/gates.md). The last
  gate, `anteval --smoke`, is the only one that proves a live agent answers.

## Backup and restore

A tar of the stopped data directory is the whole instance. `arizuko archive`
is a logical copy that `archive apply` restores into the same instance or
into another one.

### A tar of the stopped instance

The tar carries every row, because the store server's PostgreSQL data sits
under `store/`. Stop the instance first. PostgreSQL writes its files while it
runs, so a tar of a running instance is not a backup.

```bash
sudo systemctl stop arizuko_<name>
sudo tar -C /srv/data -czf arizuko_<name>-$(date +%F).tar.gz arizuko_<name>
sudo systemctl start arizuko_<name>
```

The tar holds the PostgreSQL data under `store/extenddb_db/`, the server's
config and TLS key under `store/extenddb/`, the group folders, `.env` with
`SECRETS_KEY`, and `images/`. Every rootfs tree in `images/` is about 8.3 GB
by the Makefile's measure. `--exclude=arizuko_<name>/images` leaves them out;
a restore then runs `make images` and `make kernel` and repoints `.env`. An
instance on a shared store server keeps its rows on that server, so its tar
holds no rows.

To restore, move the current directory aside and unpack the tar in its place:

```bash
sudo systemctl stop arizuko_<name>
sudo mv /srv/data/arizuko_<name> /srv/data/arizuko_<name>.before-restore
sudo tar -C /srv/data -xzf arizuko_<name>-<date>.tar.gz
sudo systemctl start arizuko_<name>
```

### Export and apply an archive

`arizuko archive export <name> [file] [--quiesced]` writes one tar:

| Entry                | Holds                                                                             |
| -------------------- | --------------------------------------------------------------------------------- |
| `archive.yaml`       | `format_version` and `consistency`                                                |
| `routd.yaml`         | routd's config rows, the same document `arizuko export` writes                    |
| `onbod.yaml`         | onbod's config rows                                                               |
| `routd.secrets.yaml` | secret values, sealed; route token hashes; invites; pending onboarding admissions |
| `routd.messages.jl`  | the whole message history                                                         |
| `groups.tar`         | every `groups/<folder>/` tree                                                     |

authd's and runed's stores, `.env`, `SECRETS_KEY` and the guest artifacts are
not in it. Export reads the live store, so the store server must run. Without
`--quiesced`, `consistency` is `live`: each subsystem is a snapshot taken at
its own moment. `--quiesced` only writes `quiesced` into `archive.yaml`. It
stops nothing and checks nothing, so pass it only when every writer is down.
Export writes the file with mode 0644; `chmod 600` it.

`arizuko archive apply <name> <archive.tar> [--force] [--stopped]` restores:

- It refuses an archive whose `format_version` is newer than the binary, and a
  row that names a folder which is not a group.
- It applies the config documents through the same claim-gated path as
  `arizuko apply`, with a rollback if a later subsystem fails. `--force` skips
  the content-hash check.
- It opens every secret against the target's `SECRETS_KEY` before it writes
  one. A target with another key needs the source key in its keyring:
  `SECRETS_KEY=<target key>,<source key>`.
- Route tokens, invites and pending onboarding restore only with `--force`,
  and only into an empty table.
- It skips a group folder that exists and is not empty. `--force` overwrites
  it.
- It appends the message history and sets each chat's cursor to its newest
  message, so the restored agent does not answer old messages again.

Before apply overwrites a folder's tree, it claims that folder's run slot from
runed at `RUNED_URL`, so no turn writes the folder during the restore. The
default `RUNED_URL` is `http://runed:8080`, a compose name the host does not
resolve, and apply stops when it cannot claim the slot. `--stopped` skips the
claim: stop the instance, start only the store server, then apply.

```bash
sudo systemctl stop arizuko_<name>
cd /srv/data/arizuko_<name>
sudo docker compose up -d extenddb_db extenddb
cd -
./arizuko archive apply <name> <archive.tar> --stopped
sudo systemctl start arizuko_<name>
```

The archive carries the config rows and not a daemon's runtime settings such
as its port (BUGS `L249`).

## Troubleshooting

**Agent doesn't reply (healthchecks green):** usually one of

1. **runed refuses to start.** journalctl shows `this host cannot run turns;
N precondition(s) missing:` and lists every one at once — a missing
   `/dev/kvm`, an unset `RUNED_KERNEL_DIR`/`RUNED_ROOTFS_DIR`, a missing
   `egress`/`iptables`/`ip` binary, or the kernel/rootfs artifacts not yet
   built. Fix the named ones and restart; a fix for one item never masks the
   others (`runed/qemuhost.go` `qemuPreconditions`).
2. **Adapter disconnected.** `docker ps` shows `(unhealthy)`, or its
   `/health` returns 503 `{status:"disconnected"}` while the process is up
   (whapd waiting for a QR scan, a stream dropped). Check the adapter's
   logs, not routd's:
   `sudo curl -s -o /dev/null -w '%{http_code}\n' http://localhost:<port>/health`
3. **Adapter silent.**
   `sudo journalctl -u arizuko_<name> --since "10 min ago" | grep -viE health`
4. **A turn fails to start** in runed's own logs — `turn did not start` names
   the folder, the handle and the reason (a `virtiofsd`/tap/proxy start
   failure). An image without `iptables`, `iproute2` or the `egress` binary
   causes it: rebuild with `sudo make images DATA_DIR=...`.

Read `journalctl -u arizuko_<name>`, never `docker logs`: a host whose
Docker log driver is `none` answers `docker logs` with nothing on stdout.

Every other incident, with the line to read, the cause and the fix, is in
[howto/operate.html](template/web/pub/arizuko/howto/operate.html).

## Quick reference

The whole install, one block. The sections above explain each line.

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
