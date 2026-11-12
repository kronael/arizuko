# Installing arizuko

Check the host, then run the fresh-store bootstrap. Use the remaining sections
to configure a channel and operate the instance.

## Check the host before you install

Run these checks and note which ones fail:

```bash
# 1. OS: Linux x86_64 is required
uname -s -m   # expect: Linux x86_64

# 2. Go 1.21 or newer
go version    # missing or older than 1.21 is a blocker
              # go.mod pins `toolchain go1.27.3`. With GOTOOLCHAIN=auto, the
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
`RUNED_GUEST_MEM` times `MAX_CONCURRENT_AGENTS` is more than `MemTotal`, see
`memoryFits` in `runed/qemuhost.go`. The defaults are 2 GiB and 5, so they need
10 GiB. Each release rootfs needs about 7 GB of disk, and `make images` removes
no old tree.

`MAX_CONCURRENT_AGENTS` caps concurrent agents in both routd and runed.
Scheduled tasks use at most one of those slots. `arizuko generate` and the
daemons refuse a `.env` that sets `MAX_CONCURRENT_CONTAINERS`.

`RUNED_RUN_TIMEOUT` (default 60m) ends a run. A turn that runs out of time is
not retried; its status says "Time limit reached." Another failed turn
retries three times: 30 s, 2 min and 5 min for chat, 15, 45 and 180 min for a
scheduled task. `MAX_TURN_RETRY` lowers that (`-1` disables retries). While a
retry waits, no new agent starts on the instance; the retry is the probe. A
`/stop` in the chat ends the folder's turn and posts "Turn stopped."

## Fix a failed check

### Go is missing or too old

Install a stable Go release, 1.21 or newer, from <https://go.dev/dl/>.

`go.mod` has the directive `go 1.27` and pins `toolchain go1.27.3`. With
`GOTOOLCHAIN=auto`, Go fetches the pinned toolchain on the first `go build`.

Two more pins move with that toolchain:

- **The Docker base image.** Each Dockerfile that builds the root module names
  `golang:1.27.3-alpine`. The files are `Dockerfile`, `anteval/Dockerfile`,
  `egress/Dockerfile`, `mcpfw/Dockerfile` and `ttsd/Dockerfile`. Nothing derives
  the name from `go.mod`, so change them in the same commit as `go.mod`.
  `kernel/guest-init` is its own module on `go 1.25` and keeps its own base.
- **`golang.org/x/net`.** `go.mod` requires v0.60.0, and the version must not
  go down.

Go does not fetch the toolchain in two cases: the host sets `GOTOOLCHAIN=local`,
or the host cannot reach `go.dev`. Install the toolchain by hand:

```bash
go install golang.org/dl/go1.27.3@latest && go1.27.3 download
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

# Option B: run Docker commands with sudo
sudo docker ps
```

### /srv/data is not writable

The fresh-store bootstrap uses sudo to create the instance under `/srv/data`.
The operator does not need write access to `/srv/data`.

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

## Get the repository

```bash
git clone https://github.com/kronael/arizuko && cd arizuko
```

## Create and enroll an instance

Configure the external server first under [Share one store server between instances](#share-one-store-server-between-instances).
The host and every container must reach the same HTTPS URL.
The server certificate must cover that address.
Keep the server's administrator password outside the instance directory.

This procedure requires a UID 1000 operator with sudo, Python 3, OpenSSL,
curl, Go and Docker Compose. Run it from the repository in Bash.
Choose a new instance name. The command refuses to overwrite its directory.

The commands build the daemon image and guest artifacts.
Enter the absolute artifact directories printed by the build when prompted.
Enrollment creates scoped keys in `env/` before the owner daemons create their tables.
Keep the instance and temporary directory private.
If a command fails, inspect both directories before retrying.
Guest execution needs separate verification.

<!-- twin:install -->

```bash
# Run from the repository as the instance owner, with UID 1000.
set -Eeuo pipefail
umask 077
source_dir=$PWD
flavor=demo
data="/srv/data/arizuko_$flavor"
[ "$(id -u)" = 1000 ] || { echo 'Use the UID 1000 instance owner.'; exit 1; }
# mkdir fails if the instance already exists. Never reuse an instance here.
sudo mkdir -p /srv/data
sudo mkdir -m 0711 -- "$data"
sudo chown "$(id -u):$(id -g)" "$data"
proof=$(mktemp -d)
mkdir "$proof/bin"
# Stop only the bootstrap owner processes if the commands fail.
cleanup_owners() {
  for name in onbod routd authd; do
    if [ -f "$proof/$name.pid" ]; then
      pid=$(cat "$proof/$name.pid"); kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
}
trap cleanup_owners EXIT
freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
# Keep the CLI in the checkout for later commands.
go build -o ./arizuko ./cmd/arizuko
go build -o "$proof/bin/authd" ./authd
go build -o "$proof/bin/routd" ./routd/cmd/routd
go build -o "$proof/bin/onbod" ./onbod
env -i PATH=/usr/bin:/bin HOST_APP_DIR="$source_dir" ./arizuko create -i "$flavor" > "$proof/create.log" 2>&1
sudo make images DATA_DIR="$data"
sudo make kernel DATA_DIR="$data"
read -r -p 'Guest kernel directory: ' kernel_dir
read -r -p 'Guest rootfs directory: ' rootfs_dir
sudo test -s "$kernel_dir/bzImage" && sudo test -s "$kernel_dir/initrd.gz"
[ -d "$rootfs_dir" ]
read -r -p 'External store HTTPS URL: ' store_url
read -r -p 'Store signing region: ' store_region
read -r -p 'Server PEM certificate path: ' store_cert
store_ca=$(base64 -w0 "$store_cert")
printf 'HOST_APP_DIR=%s\nDYNAMODB_URL=%s\nAWS_REGION=%s\nSTORE_CA=%s\nRUNED_KERNEL_DIR=%s\nRUNED_ROOTFS_DIR=%s\n' "$source_dir" "$store_url" "$store_region" "$store_ca" "$kernel_dir" "$rootfs_dir" >> "$data/.env"
printf 'AUTHD_SERVICE_KEY=%s\n' "$(openssl rand -hex 32)" > "$proof/service.env"
read -r -s -p 'Store administrator password: ' EXTENDDB_PASSWORD
printf '\n'
export EXTENDDB_PASSWORD
./arizuko store enroll -i "$flavor"
unset EXTENDDB_PASSWORD
common=(PATH=/usr/bin:/bin HOST_APP_DIR="$source_dir" DYNAMODB_URL="$store_url" AWS_REGION="$store_region" STORE_CA="$store_ca")
health() { local name=$1 hp=$2; for n in $(seq 1 60); do if curl -fsS "http://127.0.0.1:$hp/health" >/dev/null 2>&1; then return 0; fi; kill -0 "$(cat "$proof/$name.pid")" 2>/dev/null || return 1; sleep 1; done; return 1; }
auth_port=$(freeport); rout_port=$(freeport); onbod_port=$(freeport)
set -a; . "$data/env/authd.env"; . "$proof/service.env"; set +a
env -i "${common[@]}" DATA_DIR="$data" AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" LISTEN_ADDR="127.0.0.1:$auth_port" AUTHD_SERVICE_KEYS="service:onbod=$AUTHD_SERVICE_KEY" "$proof/bin/authd" > "$proof/authd.log" 2>&1 & printf '%s\n' "$!" > "$proof/authd.pid"
health authd "$auth_port"
set -a; . "$data/env/routd.env"; set +a
env -i "${common[@]}" DATA_DIR="$data" AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" LISTEN_ADDR="127.0.0.1:$rout_port" "$proof/bin/routd" > "$proof/routd.log" 2>&1 & printf '%s\n' "$!" > "$proof/routd.pid"
health routd "$rout_port"
set -a; . "$data/env/onbod.env"; set +a
env -i "${common[@]}" DATA_DIR="$data" AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" ONBOD_LISTEN_ADDR="127.0.0.1:$onbod_port" AUTHD_URL="http://127.0.0.1:$auth_port" AUTHD_SERVICE_KEY="$AUTHD_SERVICE_KEY" ROUTER_URL="http://127.0.0.1:$rout_port" "$proof/bin/onbod" > "$proof/onbod.log" 2>&1 & printf '%s\n' "$!" > "$proof/onbod.pid"
health onbod "$onbod_port"
for name in onbod routd authd; do kill "$(cat "$proof/$name.pid")"; wait "$(cat "$proof/$name.pid")" || true; rm "$proof/$name.pid"; done
printf 'owner health passed\n'
env -i PATH=/usr/bin:/bin ./arizuko package sync -i "$flavor"
# The instance is new; give its runtime files to the daemon user.
sudo chown -R 1000:1000 "$data/packages" "$data/groups" "$data/app-src" "$data/surrogate" "$data/web"
# Initial startup runs in the foreground. Use the systemd unit below for deployment.
sudo env HOST_APP_DIR="$source_dir" ./arizuko run -i "$flavor"
```

<!-- /twin -->

## Build the binaries

For development or an upgrade, build the binaries and run the tests:

```bash
make build                  # ./arizuko CLI and the daemon binaries
make test                   # go test ./... -short
```

## Build the images and the guest kernel

The bootstrap builds both artifacts. To rebuild them, `DATA_DIR` names the
instance directory that receives the guest kernel and the release rootfs,
`/srv/data/arizuko_<name>`.
Instances that use the same agent build can share one `DATA_DIR`, so
the export runs once. [`kernel/README.md`](kernel/README.md) says what
`make kernel` builds and why it is a separate artifact.

```bash
sudo make images DATA_DIR=/srv/data/arizuko_<name>
# builds arizuko:latest, arizuko-ant:latest and the adapter images, then exports
# the release rootfs that the guest boots (DATA_DIR/images/rootfs-<id>/)
# and removes arizuko-ant:latest after a successful export
sudo make kernel DATA_DIR=/srv/data/arizuko_<name>
# builds the pinned guest kernel and initrd (DATA_DIR/images/kernel-<hash>/).
# It rebuilds only when kernel/Dockerfile, kernel/arizuko.fragment,
# kernel/check-config.sh or kernel/guest-init/*.go change.
```

`make images` removes no tree. Each tree in `images/` is about 7 GB, so old
trees fill the disk. Free them with the operator command, on the host, as root:

```bash
sudo ./arizuko images -i <name>            # list every rootfs and kernel tree, with its size
sudo ./arizuko images -i <name> --prune    # remove every tree that is not in use
```

The in-use trees are the two that `RUNED_ROOTFS_DIR` and `RUNED_KERNEL_DIR` name
in `/srv/data/arizuko_<name>/.env`. The command refuses to run if the `.env` does
not name both, or if a named tree is not on disk. It removes nothing if a mount
exists at or under a tree that it would remove. It removes only the direct
children of `images/` named `rootfs-*` or `kernel-*`. To keep a rollback tree,
set `RUNED_ROOTFS_DIR` to it first, or do not prune. The listed size adds the
size of every file name in a tree, so a hard-linked file counts once for each
name, and the sizes can add up to more than a prune frees.

To free the build cache, run `docker builder prune -f`.

`make images` builds the complete image set, the adapter images included. To
rebuild one image only, use these targets:

```bash
sudo make agent             # arizuko-ant:latest only
sudo make -C whapd image    # the WhatsApp adapter only
```

## The instance directory

The bootstrap creates `/srv/data/arizuko_<name>/` and its `.env` file. It writes
`ASSISTANT_NAME`, `AUTH_SECRET` and `SECRETS_KEY`.

The instance directory, `${PREFIX:-/srv}/data/arizuko_<name>/`, holds these
paths:

```
.env                 operator settings and anchor secrets; generation input
env/<daemon>.env     scoped runtime values and the daemon's store key
env/operator.env     the host CLI's store key
store/               <adapter>/ files an adapter owns
groups/<folder>/     the home of the agent: session state in .claude/, diary,
                     logs, media/<YYYYMMDD>/ inbound attachments
ipc/<folder>/        the MCP socket of the turn
web/pub/ web/priv/   the web trees that the slots of each folder use
```

Business state, such as gates, grants and onboarding, is stored as rows.
Infrastructure settings are environment variables.

## The store server holds every table of the instance

Every table lives on an external ExtendDB server over PostgreSQL.
Each instance is one account on that server.
The server runs outside the instance's Compose project and systemd unit.
The server owns PostgreSQL, its catalog, TLS files and administrator credential.
Instances hold scoped access keys and no PostgreSQL credentials.
[`objstore/README.md`](objstore/README.md) draws the storage layers.
[Share one store server between instances](#share-one-store-server-between-instances)
configures the server.

Set these values in the instance's `.env` before enrollment:

```bash
DYNAMODB_URL=https://store.example.com:18443
STORE_CA=<base64 PEM certificate from the server operator>
AWS_REGION=us-east-1
```

The host and containers use the same URL and CA.
`env/operator.env` holds the operator's scoped key pair.
Remove any differing `DYNAMODB_URL` or `STORE_CA` override from that file.
`generate` refuses an invalid HTTPS URL, missing or invalid PEM CA, missing
region, or an incomplete required key pair before it writes runtime output.

<a id="the-per-daemon-store-keys"></a>

### The per-daemon store keys limit each client to declared owner prefixes

Each store client signs in as itself. It has its own IAM user and access key in
`env/<daemon>.env`, and a policy that covers only the tables it opens. The
account is the tenant boundary, and the policy is the only boundary inside the
account. `dynstore.Prefix` puts the owner name in front of every table name.
The resource `routd.*` therefore means exactly the tables of routd.

Never give a key `dynamodb:*` on `*`. Such a key names its owner but isolates
nothing. One compromised adapter could then reach the tables of every owner on
the instance.

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
| `googd`       | `routd.*`, `googd.*`            |
| `onbod`       | `onbod.*`, `routd.*`            |
| `proxyd`      | `routd.*`                       |
| `routd`       | `routd.*`                       |
| `runed`       | `runed.*`                       |
| `slakd`       | `routd.*`, `slakd.*`            |
| `teled`       | `teled.*`                       |
| `ttsd`        | none                            |
| `webd`        | `routd.*`                       |

Three rows need care:

- `gitd`, `googd` and `slakd` open the tables of routd directly, see `gitd/main.go`, `googd/main.go` and `slakd/main.go`. A policy with only their own prefix lets them
  authenticate and then read nothing.
- `ttsd` opens no store.
- `cmd/arizuko` is the operator command on the host. Its key goes in
  `env/operator.env`, not in the file of a daemon.

Every other adapter in the table gets its own prefix and nothing else.
`whapd` opens no store and needs no key. It is absent from `StoreReach`
and does not call `OpenAdapterStore`.

One command does this for every row of the table, after `.env` holds
`DYNAMODB_URL`, `STORE_CA` and `AWS_REGION`. Run it as a user that can read `.env` and write
`env/`. The bootstrap runs it as the owner of the new instance. On a deployed
instance root owns `.env` and `env/`, so use `sudo` and keep the password in the
environment of the command:

```bash
export EXTENDDB_PASSWORD=<the password of the admin user of the store server>
sudo --preserve-env=EXTENDDB_PASSWORD ./arizuko store enroll -i <name>
unset EXTENDDB_PASSWORD
```

The command signs in to the external server's management API as `admin`.
Supply `EXTENDDB_PASSWORD` only for enrollment.
Keep the administrator credential with the server configuration, outside the instance.

It makes the account `arizuko_<name>` on the server that `DYNAMODB_URL` names.
For each row it makes the user, puts the policy `tables` from `StoreReach`,
mints the key, and writes the pair into `env/<daemon>.env` with mode `0600`.
The row `cmd/arizuko` is the user `operator`, and its pair goes into
`env/operator.env`. The command enrols every row, also a daemon that is not
installed, so a later `package add` finds its key.

The output is one line for each row, and nothing more. It does not print the
account or its id:

```text
authd authd <key id> minted
onbod onbod,routd <key id> kept
webd routd <key id> minted replaced <old key id>
```

The fields are the user, the prefixes of its policy, the key id, and `minted`
or `kept`. `replaced <old key id>` means that the file held a key id that no
user on this server holds, for example a key of another store server. The
command never prints a secret. A failed call stops the run with a non-zero
status.

Run it again at any time. A row that already holds a valid key is `kept`, and the
command checks the pair with one signed request. A file with half a pair, a key
of another account or user, or an inactive key stops the run before it writes
anything. To rotate one key, delete its two lines in the env file and run the
command again, then delete the old key with `extenddb manage delete-access-key`
after the restart.

The command writes each pair to its file immediately after the mint. A run that
stops between the mint and the write leaves one key on the server that no file
holds. The command does not find or print such a key. To find it, list the keys
of the user and compare each key id with `AWS_ACCESS_KEY_ID` in the file of the
row:

```bash
sudo --preserve-env=EXTENDDB_PASSWORD docker exec -e EXTENDDB_PASSWORD <store container> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  list-access-keys --account-id <account id> --user-name <user>
```

`list-accounts` with the same first two lines prints the id of the account
`arizuko_<name>`. Delete a key that no file holds with `delete-access-key` and
the same flags, plus `--access-key-id <key id>`.

With a second argument, the command also moves the rows of the instance from
a source account. "Move an instance onto the shared store server" gives
every step.

Only the ExtendDB management API is supported.

Run enrollment before `generate` or `run`.
Generation requires complete scoped pairs for the installed store users and the operator.
Run enrollment again after an upgrade adds a store user.

### Share one store server between instances

Run this server independently of every instance, including instances on the same host.
Each instance uses its own account and scoped keys.
Back up the server catalog, data database and configuration independently of instance archives.

A shared PostgreSQL usually withholds `CREATEDB`. `init` needs that right only
to create the two databases. `init` also refuses to run when the databases
already exist, and it says `Run 'destroy' first`. The `destroy` command drops
them. When the role cannot create databases, you create them and finish the
deployment by hand. The work is four settings rows, one admin row, and nothing
else.

1. **Create the pair** as a role that has `CREATEDB`, and give both databases to
   the application role:

   ```sql
   CREATE ROLE dynamo LOGIN PASSWORD '<pw>';
   CREATE DATABASE dynamo OWNER dynamo;
   CREATE DATABASE dynamo_catalog OWNER dynamo;
   ```

2. **Generate the certificate.** TLS is required. Only `init` writes a
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

3. **Write `extenddb.toml`** with mode `0600`, owned by the service user. The
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

4. **Apply the catalog schema** with `extenddb migrate`. It reads the
   connection string from `extenddb.toml`, so it runs after step 3. The role
   owns both databases, so it needs no other grant:

   ```bash
   sudo docker run --rm --network=host -u 1000:1000 \
     -v /srv/data/extenddb:/var/lib/extenddb -e EXTENDDB_PG_PASSWORD="$PW" \
     ghcr.io/extenddb/extenddb-postgres:0.1.11 migrate \
     --config /var/lib/extenddb/extenddb.toml --pg-user dynamo --yes
   ```

5. **Write the four settings** in the catalog database
   `dynamo_catalog`. Without them, the server looks for the data tables in the
   catalog and exits with `Encryption key not found in settings table`:

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

6. **Create the admin user.** Only `init` creates the first admin user, and
   `init` stopped at the databases, so the table `admin_users` is empty. The
   management call that creates an admin user needs an admin user itself, so
   the first row goes in with SQL. The server compares the password with a
   bcrypt hash. `mkpasswd` from the Debian package `whois` makes the hash from
   a file and prints no password. `psql` replaces `:'hash'` only in a script
   that it reads from its input, never in a `-c` command:

   ```bash
   sudo install -m 0600 /dev/null /srv/data/extenddb/admin.pw
   openssl rand -hex 24 | sudo tee /srv/data/extenddb/admin.pw >/dev/null
   hash=$(sudo cat /srv/data/extenddb/admin.pw | mkpasswd -m bcrypt -R 12 --stdin)
   psql 'postgresql://dynamo@<pg-host>:5432/dynamo_catalog' -v hash="$hash" <<'SQL'
   INSERT INTO admin_users (admin_name, password_hash) VALUES ('admin', :'hash');
   SQL
   unset hash
   psql 'postgresql://dynamo@<pg-host>:5432/dynamo_catalog' -Atc 'SELECT admin_name FROM admin_users'
   ```

   The last command must print `admin`. The user name must be `admin`, because
   `arizuko store enroll` signs in as `admin`. The content of `admin.pw` is the
   `EXTENDDB_PASSWORD` for `enroll` on this server. It controls every account on
   the server, so only root may read the file.

7. **Run the server.** The command must carry `--foreground`. Without it, the
   server daemonizes, and systemd reports `PID file not created within 5
seconds`. The image `ENTRYPOINT` is already `extenddb`, so the command is
   `serve --config … --foreground`. Never write `extenddb serve …`.

8. **Verify the server** before you point anything at it:

   ```bash
   sudo docker run --rm --network=host -u 1000:1000 \
     -v /srv/data/extenddb:/var/lib/extenddb \
     ghcr.io/extenddb/extenddb-postgres:0.1.11 verify \
     --config /var/lib/extenddb/extenddb.toml
   ```

   The command must print `HEALTHY: All checks passed`.

Create the instance, configure its URL, CA and region, then run `arizuko store enroll`.
Enrollment creates the account and its key pairs.
The bootstrap above starts the owners before package synchronization and initial startup.

### Move an instance onto the shared store server

`arizuko store enroll -i <name> <snapshot>` copies rows from a source account
into the instance's destination account.
The source and destination servers run outside the instance.
Test the copy on isolated accounts before moving an instance.

Before the copy, the source administrator grants the snapshot's operator key
`ListTables`, `DescribeTable` and `Scan` over every source table.
The ordinary operator policy covers only `authd`, `onbod` and `routd`.

```bash
export EXTENDDB_PASSWORD=<source administrator password>
sudo --preserve-env=EXTENDDB_PASSWORD docker exec -e EXTENDDB_PASSWORD <source store container> \
  extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin \
  put-user-policy --account-id <source account id> --user-name operator --policy-name move \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["dynamodb:ListTables","dynamodb:DescribeTable","dynamodb:Scan"],"Resource":"arn:aws:dynamodb:*:*:table/*"}]}'
unset EXTENDDB_PASSWORD
```

1. Stop the instance and save its configuration:

   ```bash
   sudo systemctl stop arizuko_<name>
   sudo install -d -m 0700 <snapshot>
   sudo cp -a /srv/data/arizuko_<name>/.env /srv/data/arizuko_<name>/env <snapshot>/
   ```

   Keep the snapshot unchanged. It holds the source URL, CA and operator key.
   Keep the source server running during the copy.

2. Set the destination `DYNAMODB_URL`, `STORE_CA` and `AWS_REGION` in `.env`.
   Remove URL or CA overrides from `env/operator.env`.
   The host and containers must reach the destination URL.
   Its certificate must cover that address.

3. Enroll and copy with the destination administrator password:

   ```bash
   export EXTENDDB_PASSWORD=<destination administrator password>
   sudo --preserve-env=EXTENDDB_PASSWORD ./arizuko store enroll -i <name> <snapshot>
   unset EXTENDDB_PASSWORD
   ```

   The command must exit 0 and print `<table> <count> equal` for every table.
   It refuses unsupported schemas, secondary indexes, streams and unknown owner prefixes.
   The copy reads the source and uses conditional writes at the destination.
   A retry keeps valid keys and existing items.
   Repeat only before the destination accepts writes from the instance.

4. Regenerate and start the instance:

   ```bash
   sudo ./arizuko generate -i <name>
   sudo systemctl start arizuko_<name>
   ```

   Keep both servers and the snapshot until verification completes.
   Verify daemon health, dashboard login and one reply through each adapter.
   An isolated acceptance run must also start with the source server stopped.

Before destination startup, restore `.env` and `env/` from the snapshot to return to the source.
After destination startup, a return loses rows written only at the destination.

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
Code. `ARIZUKO_BACKEND=codex` or `pi` selects another supported backend.
All three use the same arizuko tools and explicit delivery. The agent image
includes their CLIs.

Any pi provider works, including a local model. Use the operator's normal pi
provider setup and add the provider host to `network_rules`. Custom provider
configuration uses a read-only `models.json` mount at `/mnt/pi/models.json`,
as described in [Swapping the agent harness](EXTENDING.md#swapping-the-agent-harness).

To lend the host user's Codex login (ChatGPT sign-in) to chosen folders, set
`HOST_CODEX_DIR` to that `.codex` directory in `.env`, then grant each folder
`codex`: `arizuko group grant folder:<f> <f> codex`. A folder without the grant
gets no login. Codex also needs `chatgpt.com` and `auth.openai.com` in the
egress allowlist: `arizuko network allow <f> chatgpt.com`.

Keep capability tokens in encrypted group secrets. For shell access, follow
the paired secret and grant recipe in [Secret injection](SECURITY.md#secret-injection).

These settings are optional:

```bash
# Required only for the web interface and OAuth login
WEB_HOST=myhost.example.com

# `arizuko create` writes these. Change them only for a reason.
# AUTH_SECRET=...
# SECRETS_KEY=...

# Host path of the arizuko checkout (this repository).
HOST_APP_DIR=/path/to/arizuko
```

## Get a credential for the channel adapter

Add the credentials for the adapters that you choose:

| Adapter              | Env vars                                                                                                     | Where to get them                  |
| -------------------- | ------------------------------------------------------------------------------------------------------------ | ---------------------------------- |
| **teled** (Telegram) | `TELEGRAM_BOT_TOKEN`                                                                                         | @BotFather, then /newbot           |
| **discd** (Discord)  | `DISCORD_TOKEN=Bot <token>`                                                                                  | discord.com/developers, Bot, Token |
| **slakd** (Slack)    | `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`                                                                    | api.slack.com, App, OAuth          |
| **emaid** (Email)    | `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`                                      | Your email provider                |
| **whapd** (WhatsApp) | none; the adapter pairs with a QR code                                                                       | `./arizuko pair -i <name> whapd`   |
| **gitd** (GitHub)    | none in `.env`; `./arizuko github -i <name> add …` mints the secret. `GITHUB_TOKEN` is optional, for replies | GitHub, Settings, Webhooks or App  |

Telegram is the simplest choice. It needs one token from @BotFather.

## Enable the channel adapter

Select the adapters you want with `arizuko package add`, for example:

```sh
./arizuko package add -i <name> teled
./arizuko package add -i <name> gitd
```

A bare name selects a service package of the release the `arizuko` binary was
built from. Each `add` installs the package and records the selection in the
`installed_packages` rows. `./arizuko package sync -i <name>` installs every
selected package again. An adapter starts only when its package is installed in
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

Run `arizuko package add -i <name> litellm`.
The command writes `MODEL_GATEWAY_KEY` and `MODEL_GATEWAY_DB_PASSWORD` into
`/srv/data/arizuko_<name>/.env`. The package `arizuko.yml` declares both as
`generate`. Every `generate` sets
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
./arizuko group -i <name> add telegram:group/123456789 main

# Discord
./arizuko group -i <name> add discord:<guild_id>/<channel_id> main

# Slack
./arizuko group -i <name> add slack:<team_id>/channel/<channel_id> main
```

## Run the instance

The bootstrap starts the instance. For later deployments, restart its systemd
service. `arizuko run` is for initial setup.

### systemd keeps the instance running

Replace `<name>` and `<release-image>` with the instance name and the local
release image tag. `make images` builds `arizuko:latest`; use that local tag
or tag the image with the version you choose.
The unit runs the generator from that image and starts Compose in the foreground.
Enroll the instance and boot its owners before enabling the unit.
The external server stays running when the instance stops.

```bash
sudo tee /etc/systemd/system/arizuko_<name>.service <<'EOF'
[Unit]
Description=arizuko <name> (docker compose)
After=docker.service
Requires=docker.service

[Service]
Type=simple
WorkingDirectory=/srv/data/arizuko_<name>
ExecStartPre=/usr/bin/docker compose -f /srv/data/arizuko_<name>/docker-compose.yml down --remove-orphans
ExecStartPre=/usr/bin/docker run --rm --pull=never -v /srv/data/arizuko_<name>:/srv/data/arizuko_<name> -v /var/run/docker.sock:/var/run/docker.sock <release-image> arizuko generate -i <name>
ExecStart=/usr/bin/docker compose -f /srv/data/arizuko_<name>/docker-compose.yml up --remove-orphans
ExecStop=/usr/bin/docker compose -f /srv/data/arizuko_<name>/docker-compose.yml down
Restart=on-failure
RestartSec=5
TimeoutStartSec=infinity

[Install]
WantedBy=default.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now arizuko_<name>
sudo systemctl status arizuko_<name>
sudo journalctl -u arizuko_<name> -f
```

For each deployment, run `sudo systemctl restart arizuko_<name>`. The unit
runs the generator from its configured release image on every start.
When the release image tag changes, update the unit and run
`sudo systemctl daemon-reload` before the restart.

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
./arizuko get -i demo groups > groups-before.yml  # includes stored settings
```

Before the restart, read `groups-before.yml` for groups whose `config` JSON
contains top-level `model` or `effort` keys. The runtime reads overrides only
from `config.harness`.

If any group needs conversion, copy the inventory:

```bash
cp groups-before.yml groups-harness.yml
```

In `groups-harness.yml`, keep only the affected rows. `apply` replaces each
named group row. Preserve every exported row field and every unrelated setting
in `config`. Remove the top-level
`model` and `effort` keys. Set `harness` to one complete JSON object with the
group's intended backend, model and effort. Do not infer the backend from the
model name. Replace the placeholders in this rows-file shape with the group's
values:

```yaml
groups:
  - folder: <folder>
    product: <product>
    config: |-
      {"harness":{"backend":"<backend>","model":"<model>","effort":"<effort>"}}
```

The backend is `claude`, `codex` or `pi`. Use an effort value that the selected
backend accepts. Preview the changes. Then apply the file:

```bash
./arizuko plan -i demo groups-harness.yml
./arizuko apply -i demo groups-harness.yml
./arizuko get -i demo groups
```

Confirm each affected group's stored harness matches its intended values.
Do not continue the rollout until every affected group has a complete harness
document. If the inventory contains no flat overrides, continue without a conversion.

```bash
sudo make images DATA_DIR=/srv/data/arizuko_demo  # prints rootfs: <dir>
sudo make kernel DATA_DIR=/srv/data/arizuko_demo  # prints kernel: <dir>
sudoedit /srv/data/arizuko_demo/.env              # RUNED_ROOTFS_DIR, RUNED_KERNEL_DIR
./arizuko generate -i demo                           # read every warning it prints
sudoedit /etc/systemd/system/arizuko_demo.service  # set the release image tag if it changed
sudo systemctl daemon-reload                       # load the unit if it changed
sudo systemctl restart arizuko_demo               # the service generates and starts
./arizuko send -i demo main "ping" --wait            # one reply proves the path
```

- `make images` exports a new rootfs tree only when the `arizuko-ant` image
  changed. It removes `arizuko-ant:latest` after a successful export or reuse.
  It removes no tree. To roll back the guest, set `RUNED_ROOTFS_DIR`
  to an older tree that is still on disk and restart. `arizuko images -i <name>
--prune` removes every tree that `.env` does not name.
- `make kernel` builds only when `kernel/Dockerfile`, `kernel/arizuko.fragment`,
  `kernel/check-config.sh` or `kernel/guest-init/*.go` changed. Otherwise it
  prints `is current` and `RUNED_KERNEL_DIR` stays as it is.
- `arizuko generate` refuses `MAX_CONCURRENT_CONTAINERS`. Rename the key in
  `.env` before you generate:
  `sudo sed -i 's/^MAX_CONCURRENT_CONTAINERS=/MAX_CONCURRENT_AGENTS=/' /srv/data/arizuko_demo/.env`.
- `arizuko generate` names every fragment that is frozen and every daemon that
  has no store key. Fix both before the restart. A daemon without a store key
  restarts the whole instance in a loop.
- Startup installs the release's required core package. Existing sessions
  continue; the next turn reads the installed skills and manual.
- A maintainer runs post-deploy gates 1–4 in
  [`.claude/skills/eval/gates.md`](.claude/skills/eval/gates.md). The owner may
  run `anteval` by hand against a throwaway instance to check agent behavior.

### Operators must rotate GitHub secrets disclosed to non-operators

Only instance operators can register GitHub sources or control routes that select
GitHub ingress. This applies to repository, organization and App sources.
The registration gate does not invalidate existing secrets.

Before trusting GitHub ingress after this upgrade, rotate every source secret
disclosed to a non-operator before this release. On sloth, this includes
`app/sloth`, owned by `sloth/discord`, and `repo/vumilan/morha`, owned by
`sloth/morha`. Disable the affected webhooks on GitHub during rotation.
Use `arizuko github -i <instance> list` to inventory sources.
If the registration authority is uncertain, rotate the secret.
For each affected source, record its routes and target folder, then run
`arizuko github -i <instance> rm <kind>/<name> <owner_folder>`.
Register the source as an operator:

```bash
arizuko github -i <instance> add <kind> <name> <folder>
```

The CLI sets both owner and target to the approved `<folder>`.
Configure GitHub with the newly printed secret before
enabling deliveries. Verify the seeded routes before restoring any custom routes.
If removal refuses missing ownership metadata, use the procedure below.

### Operators must remove and recreate skipped legacy GitHub sources

Startup binds a legacy source only when each seed has one matching route at its
owner folder. Missing, ambiguous or edited seeds can leave the source without
`target_folder` or `route_ids`. Startup records completion even when it skips a
source. Editing routes and restarting does not retry the source.
Normal revocation refuses a source without valid ownership metadata.

1. Disable the affected GitHub webhook. Stop the instance with
   `sudo systemctl stop arizuko_<instance>` and take a store backup.
2. Use the instance's operator store credentials and a DynamoDB client to inspect
   `routd.github_sources` and `routd.routes`. Record the source's `owner_folder`
   and every candidate route's `part`, `sort`, `id`, match and target. The source
   key is `{"part":{"S":"routd"},"sort":{"S":"<kind>/<name>"}}`.
3. Decide which candidate routes belong to this source. Delete only those exact
   route keys with DynamoDB `DeleteItem`, conditioned on their recorded `id`.
   Preserve routes owned by other sources or tenants. If ownership remains
   uncertain, keep the webhook disabled until an operator resolves it.
4. Delete the exact source key from `routd.github_sources` with `DeleteItem`,
   conditioned on its recorded `owner_folder`. Leave the
   `routd.counters` item `github-source-route-ownership` unchanged.
5. To retain the source, register it with the CLI as above and configure GitHub
   with its new secret. Verify the owner, target and routes, then restart the
   instance and enable its webhook. For removal only, leave the webhook deleted.

Do not guess route identities or clear the migration marker to force rebinding.
Source registration writes the new target and route identities explicitly.

## A tar or a dump restores an instance

An instance archive holds files and credentials, never server rows.
Back up the external server separately. `arizuko dump` makes a logical copy.
`arizuko apply` restores that copy into the same instance or another instance.

### An instance archive holds files and credentials

Stop the instance before copying its files.
Leave the external server running.
Back up the server catalog, data database, TLS files and configuration separately.
The catalog backup must include its `encryption_key` setting, which protects stored credentials.

```bash
sudo systemctl stop arizuko_<name>
sudo tar -C /srv/data -czf arizuko_<name>-$(date +%F).tar.gz arizuko_<name>
sudo systemctl start arizuko_<name>
```

The tar holds these items:

- the scoped key pairs in `env/`
- adapter files under `store/`
- the group folders
- `.env`, with `SECRETS_KEY`
- `images/`

Every rootfs tree in `images/` is about 7 GB.
`--exclude=arizuko_<name>/images` leaves them out. A restore then runs
`make images` and `make kernel` and sets the new paths in `.env`.

To restore the files, keep the external server running and unpack the archive:

```bash
sudo systemctl stop arizuko_<name>
sudo mv /srv/data/arizuko_<name> /srv/data/arizuko_<name>.before-restore
sudo tar -C /srv/data -xzf arizuko_<name>-<date>.tar.gz
sudo systemctl start arizuko_<name>
```

### A dump copies one instance into another

`arizuko dump -i <name> [file] [--quiesced]` writes one tar. Without
`file`, it writes the tar to standard output. The tar holds these entries:

| Entry                | Holds                                                                             |
| -------------------- | --------------------------------------------------------------------------------- |
| `archive.yaml`       | `format_version` and `consistency`                                                |
| `routd.yaml`         | the config rows of routd, the document that `arizuko apply` reads as a rows file  |
| `onbod.yaml`         | the config rows of onbod                                                          |
| `routd.secrets.yaml` | secret values, sealed; route token hashes; invites; pending onboarding admissions |
| `routd.messages.jl`  | the whole message history                                                         |
| `groups.tar`         | every `groups/<folder>/` tree                                                     |

The dump does not hold the stores of `authd` and `runed`, `.env`,
`SECRETS_KEY` or the guest artifacts. `dump` reads the live store, so the store
server must run. Without `--quiesced`, `consistency` is `live`, and each
subsystem is a snapshot from its own moment. The flag `--quiesced` only writes
`quiesced` into `archive.yaml`. It stops nothing and checks nothing, so use it
only when every writer is down. `dump` writes the file with mode 0600.

`arizuko apply -i <name> <dump.tar> [--force|-f] [--stopped]` restores
the dump:

- It refuses a dump with a `format_version` newer than the binary. It also
  refuses a row that names a folder that is not a group.
- It applies the config documents through the same claim-gated path as
  `arizuko package sync`. If a later subsystem fails, it rolls back. `--force` skips
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
`--stopped` skips the claim. Stop the instance and leave the external server running:

```bash
sudo systemctl stop arizuko_<name>
./arizuko apply -i <name> <dump.tar> --stopped
sudo systemctl start arizuko_<name>
```

The dump carries the config rows. It does not carry the runtime settings of
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
