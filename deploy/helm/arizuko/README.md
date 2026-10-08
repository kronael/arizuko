# arizuko Helm chart

The topology `compose/compose.go` generates, on Kubernetes: `authd`, `routd`
and `runed`, the web plane (`proxyd`, `webd`, `vited`), the operator daemons
(`dashd`, `onbod`, `timed`, `davd`) and a values-driven set of channel
adapters — one Deployment + Service + Secret each. One release is one
instance. Every daemon listens on `:8080` inside its container.

## What is verified, and what is not

- `helm lint deploy/helm/arizuko -f deploy/helm/arizuko/ci/lint-values.yaml`
  is clean, and `helm template` with the same file renders every template
  (11 Deployments, 10 Services, 9 Secrets, the ConfigMap and the PVC). Each
  daemon's image, entrypoint, env, mounts and privileges were read off
  `compose/compose.go` and `template/services/*.yml` at v0.70.2.
- A bare `helm lint deploy/helm/arizuko` **fails, on purpose**: every value an
  instance cannot run without is `required`, and the render stops on the
  first missing one with a sentence naming it (§ Values you must set).
  `ci/lint-values.yaml` holds placeholders, not an instance.
- **Nothing has installed this chart.** No cluster has run it and no pod has
  started from it. What that leaves open is listed under § Unmeasured.

## What it deploys

| Template        | Daemon                                                 | Role                                                                                     | Volume                  | From compose                |
| --------------- | ------------------------------------------------------ | ---------------------------------------------------------------------------------------- | ----------------------- | --------------------------- |
| `authd.yaml`    | authd                                                  | Auth authority: JWKS, service-token exchange, login                                      | none                    | `authdService`              |
| `routd.yaml`    | routd                                                  | Router: conversation state, group tree, one MCP socket per turn                          | whole                   | `routdService`              |
| `runed.yaml`    | runed                                                  | Execution plane: one KVM guest per turn                                                  | whole + `/dev/*`        | `runedService`              |
| `webd.yaml`     | webd                                                   | Chat widget, panel, `/api`, `/me`                                                        | none                    | `webdService`               |
| `proxyd.yaml`   | proxyd                                                 | Public reverse proxy + auth gate; the Ingress target                                     | none                    | `proxydService`             |
| `vited.yaml`    | vited                                                  | Static `/pub` + `/priv` tree                                                             | `web/`, read-only       | `vitedService`              |
| `timed.yaml`    | timed                                                  | Scheduled messages                                                                       | none                    | `timedService`              |
| `dashd.yaml`    | dashd                                                  | Operator dashboard (`/dash`)                                                             | `groups/`, `surrogate/` | `dashdService`              |
| `onbod.yaml`    | onbod                                                  | Onboarding / admission queue (`/onboard`)                                                | `groups/`, `web/`       | `onbodService`              |
| `davd.yaml`     | davd                                                   | WebDAV over `groups/` (`/dav`)                                                           | `groups/`               | `davdService`               |
| `adapters.yaml` | teled / discd / slakd / bskyd / reditd / emaid / linkd | Channel adapters, one Deployment + Service + Secret per enabled `.Values.adapters` entry | none                    | `template/services/<d>.yml` |

`authd`, `routd` and `runed` always render; the rest have an `enabled`
toggle. Supporting objects: `configmap.yaml` (non-secret env, every daemon
reads it), `pvc.yaml`, `serviceaccount.yaml`, `ingress.yaml` (optional).

## The store

Every daemon's owner tables live on one DynamoDB-protocol server, reached at
`DYNAMODB_URL`; no daemon opens a database file. **The chart runs no store.**
`store.url` names one you run: the `extenddb` pair from
`template/services/extenddb.yml` (ExtendDB over PostgreSQL), a PostgreSQL in
another cluster, or Amazon DynamoDB. The daemons speak one protocol and never
learn which is behind it.

Two things about it are one-time runs by hand, not chart steps:

- `extenddb init` creates the databases, generates the server's self-signed
  certificate and mints one access key per daemon
  (`INSTALL.md` § The store server). The certificate goes into `store.ca` as
  base64 (`STORE_CA`): a daemon reads its environment and mounts no file.
  Each key pair goes into `secrets.daemons.<daemon>.storeKeyId` /
  `.storeSecret`, so every daemon signs as itself.
- Which daemons hold a key is `store.StoreReach` (`store/owner_reach.go`):
  every daemon the chart runs except `timed`, which writes through routd's
  HTTP API, and `davd`/`vited`, which are not arizuko binaries.

## The data volume

One RWO claim, mounted at `/srv/app/home` (compose's `containerDataMount`),
holding the files and nothing else: `groups/` (the agent homes routd reads
and runed mounts into each guest), `web/`, `ipc/` (one Unix socket per turn —
routd binds it, runed's bridge dials it), `app-src/`, `images/` (the kernel
and rootfs artifacts) and `vms/`. No rows live on it, so it is sized for group
files and media.

It is one volume on one node because the per-turn socket under `ipc/` is an
inode routd and runed must both see. That is why `routd` and `runed` run one
replica each with `strategy: Recreate`, and why every pod that mounts the
volume (routd, runed, onbod, dashd, davd, vited) follows the same `nodeName`
or `nodeSelector`. Seed it once with `arizuko create` before the first start;
that is not a per-rollout step.

## runed needs a KVM node

A turn is one `qemu-system-x86_64` guest (`specs/6/41-qemu-substrate.md`).
`runed.yaml` expresses what `compose/compose.go`'s `runedService` gives the
container, and each line has a reason there:

| In the pod                                     | Why                                                                                                                    |
| ---------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun` | the guest, its vsock channel to the host, and the tap it egresses through — hostPath `CharDevice` mounts from the node |
| `capabilities.add: [NET_ADMIN, SYS_ADMIN]`     | NET_ADMIN for the tap and its rule set; SYS_ADMIN for the staging tree's bind mounts                                   |
| `appArmorProfile: Unconfined`                  | the default profile denies `mount(2)` whatever the capabilities, so SYS_ADMIN alone is necessary and not sufficient    |
| `runAsUser: 0`                                 | virtiofsd carries guest credentials onto host files, and the binds are made in runed's own mount namespace             |
| `RUNED_KERNEL_DIR`, `RUNED_ROOTFS_DIR`         | the `make kernel` and `make rootfs` artifacts, by the path runed's container sees; runed refuses to start without them |

This pins runed to a node that has `/dev/kvm`, and the data volume pins every
other file-holding pod to the same node. `runed.nodeSelector` picks that node
and is merged over the global `nodeSelector`; give the global selector (or
`nodeName`) the same node so routd lands beside it. **Do not read this as
portable.** A managed cluster that blocks hostPath, denies `SYS_ADMIN`, or
runs no KVM-capable nodes cannot run runed, and therefore cannot run a turn.
runed checks every precondition at start and names each unmet one in its
first log lines, so a wrong node fails loudly once rather than per turn.

The `appArmorProfile` field is Kubernetes 1.30 or newer.

## Secrets

One Secret per daemon, named `<release>-<daemon>`, holding exactly the keys
compose scopes into that daemon's env file (`compose/compose.go`
`daemonKeys`). The chart renders them from `secrets.*`:

| Key                                              | Daemons                                                  | Value                                              |
| ------------------------------------------------ | -------------------------------------------------------- | -------------------------------------------------- |
| `AUTHD_SERVICE_KEY`                              | every daemon but authd                                   | `secrets.daemons.<d>.serviceKey`                   |
| `AUTHD_SERVICE_KEYS`                             | authd — `service:<d>=<key>,…` built from the same values | derived                                            |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`     | every daemon in `store.StoreReach`                       | `secrets.daemons.<d>.storeKeyId/Secret`            |
| `AUTH_SECRET`                                    | authd, proxyd, dashd                                     | `secrets.authSecret`                               |
| `SECRETS_KEY`                                    | routd, dashd                                             | `secrets.secretsKey`                               |
| `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` | runed                                                    | `secrets.claudeCodeOauthToken` / `anthropicApiKey` |
| platform tokens                                  | the adapter whose `secretEnv` lists them                 | `secrets.<token>`                                  |

`secrets.external: true` renders no Secret at all. Create each
`<release>-<daemon>` yourself (External Secrets, sealed-secrets, …) with the
keys above; the Deployments reference those names unchanged.

Service keys are `openssl rand -hex 32`, one per daemon. The daemon exchanges
its key at authd for a `service:<daemon>` token and exits without one
(`chanlib/run.go`); authd recognises the set from `AUTHD_SERVICE_KEYS`. Both
come from `secrets.daemons`, so a key is declared once and read twice.

## Values you must set

Each of these is `required`; the render stops on the first one missing and
the error names it.

| Value                                                                | Env                                           | Why                                                     |
| -------------------------------------------------------------------- | --------------------------------------------- | ------------------------------------------------------- |
| `config.webHost`                                                     | `WEB_HOST`                                    | the public hostname routd, proxyd and the Ingress serve |
| `store.url`                                                          | `DYNAMODB_URL`                                | the store server; the chart runs none                   |
| `runed.kernelDir`, `runed.rootfsDir`                                 | `RUNED_KERNEL_DIR`, `RUNED_ROOTFS_DIR`        | runed refuses to start without the artifacts            |
| `secrets.authSecret`                                                 | `AUTH_SECRET`                                 | session cookies and OAuth state                         |
| `secrets.secretsKey`                                                 | `SECRETS_KEY`                                 | routd refuses to start without it                       |
| `secrets.claudeCodeOauthToken` **or** `secrets.anthropicApiKey`      | the model credential                          | runed hands it to every turn                            |
| `secrets.daemons.<d>.serviceKey`, for every enabled daemon but authd | `AUTHD_SERVICE_KEY`                           | the daemon exits without one                            |
| `secrets.daemons.<d>.storeKeyId` / `.storeSecret`, per `StoreReach`  | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | from `extenddb init`                                    |
| every `secrets.*` an enabled adapter's `secretEnv` lists             | that env var                                  | the adapter's platform credential                       |

Under `secrets.external: true` only the first three rows apply.

## Install

```bash
# One-time, by hand, before the first start: `extenddb init` against your
# store (INSTALL.md § The store server) and `arizuko create` on the data volume.
helm install arizuko deploy/helm/arizuko \
  --namespace arizuko --create-namespace \
  -f my-values.yaml
```

`my-values.yaml` carries § Values you must set; `ci/lint-values.yaml` shows
the shape with placeholders. The images (`make images`) must be reachable
from the node: `arizuko`, `arizuko-vite`, and `arizuko-davd` if WebDAV is on.
The agent image is not pulled by any pod — `make rootfs` exports it into
`runed.rootfsDir` on the volume.

## Adapters

Adapters are off by default. Enable one, give it a service key and a store key
pair, and set the `secrets.*` values its `secretEnv` lists:

```yaml
adapters:
  - name: teled
    enabled: true
    entrypoint: ['teled']
    secretEnv:
      - TELEGRAM_BOT_TOKEN
secrets:
  telegramBotToken: '123:abc...'
  daemons:
    teled:
      serviceKey: '...'
      storeKeyId: '...'
      storeSecret: '...'
```

Each adapter gets `ROUTER_URL` at routd, `LISTEN_URL` at its own Service (routd
calls it back there), and `AUTHD_SERVICE_NAME=<name>` so it exchanges as
`service:<name>`. The env var names match `template/services/<name>.yml`.

## proxyd routes

proxyd forwards by the `proxyd_routes` rows on the store, read live per
request; the chart writes none. `arizuko generate` is the writer, and it
names backends as `http://<daemon>:8080` — compose's service names
(`compose/compose.go` `coreProxydRoutes`). This chart's Services are
`<release>-<daemon>`, so rows seeded that way do not resolve here: the
backends must name the chart's Services, which `arizuko generate` does not
do.

## Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  host: arizuko.example.com # defaults to config.webHost
  tls:
    - secretName: arizuko-tls
      hosts: [arizuko.example.com]
```

All public traffic enters at proxyd `/`; proxyd serves plain HTTP on `:8080`
and terminates no TLS itself.

## Verify

```bash
kubectl -n arizuko get pods -l app.kubernetes.io/instance=arizuko
kubectl -n arizuko rollout status deploy/arizuko-routd
kubectl -n arizuko rollout status deploy/arizuko-runed

# runed names every unmet host precondition in its first log lines
kubectl -n arizuko logs deploy/arizuko-runed

# routd /health from inside the cluster
kubectl -n arizuko run -it --rm curl --image=curlimages/curl --restart=Never -- \
  curl -s http://arizuko-routd:8080/health

# proxyd (port-forward if no Ingress)
kubectl -n arizuko port-forward svc/arizuko-proxyd 8080:8080
```

## Not included

- **whapd, twitd, gitd, ttsd, kokoro**: other images and auth volumes in
  compose; add them as your own entries.
- **The model gateway** (`MODEL_GATEWAY_URL`, spec 6/23) and **OAuth
  provider credentials** for authd (`GITHUB_CLIENT_ID`, …): not modeled.
- **Adapter state volumes**: teled, bskyd, reditd and slakd each keep a small
  state file under `DATA_DIR` in compose (the Telegram offset, the Bluesky
  session, the Reddit cursors, Slack's watched messages). The chart mounts no
  volume into an adapter, so that state dies with the pod.

## Unmeasured

- Whether a hostPath mount of `/dev/kvm`, `/dev/vhost-vsock` and `/dev/net/tun`
  alone lets runed open them under your container runtime's device cgroup. If
  it does not, runed says `/dev/kvm does not open` at start and exits; a
  device plugin that advertises the three nodes, or a privileged container,
  is yours to add.
- Whether a remote-attach CSI volume can back a virtiofsd export; the
  instances arizuko runs on use a local path.
- Everything after `helm template`: no cluster has run this chart.

## Uninstall

```bash
helm uninstall arizuko -n arizuko
# The data PVC is retained by default — delete it manually if you mean it:
kubectl -n arizuko delete pvc arizuko-data
```
