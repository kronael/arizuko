# arizuko Helm chart

A **starter** Helm chart that maps arizuko's Docker Compose topology onto
Kubernetes. It is honest about the one hard part — agent spawning — and gives
you a working single-node deployment you can grow, not a 20-microservice
monster.

This chart is derived directly from `compose/compose.go` and
`template/services/*.toml`. The in-cluster port for every daemon is `:8080`
(the arizuko convention); Services expose `:8080`.

## What it deploys

| Template        | Daemon                                                         | Role                                                                                  | From compose                       |
| --------------- | -------------------------------------------------------------- | ------------------------------------------------------------------------------------- | ---------------------------------- |
| `gated.yaml`    | gated                                                          | Router + execution plane; spawns the per-turn agent container; owns the SQLite WAL DB | `gatedService` (CUTOVER_SPLIT off) |
| `webd.yaml`     | webd                                                           | Chat widget, operator panel, `/api`, `/me` portal                                     | `webdService`                      |
| `proxyd.yaml`   | proxyd                                                         | Public reverse proxy + auth gate (Ingress target)                                     | `proxydService`                    |
| `vited.yaml`    | vited                                                          | Static web tree (`/pub`, `/priv`)                                                     | `vitedService`                     |
| `timed.yaml`    | timed                                                          | Scheduled messages                                                                    | `timedService`                     |
| `dashd.yaml`    | dashd                                                          | Operator dashboard (`/dash`)                                                          | `dashdService`                     |
| `onbod.yaml`    | onbod                                                          | Onboarding / admission queue (`/onboard`)                                             | `onbodService`                     |
| `davd.yaml`     | davd                                                           | WebDAV over `groups/` (`/dav`)                                                        | `davdService`                      |
| `crackbox.yaml` | crackbox                                                       | Egress-isolation proxy (advanced; parity only)                                        | `crackboxService`                  |
| `adapters.yaml` | teled / discd / slakd / mastd / bskyd / reditd / emaid / linkd | Channel adapters, one Deployment+Service per enabled `.Values.adapters` entry         | `template/services/*.toml`         |

Supporting objects: `pvc.yaml` (the data dir, RWO), `configmap.yaml`
(non-secret env), `secret.yaml` (signing secrets + platform tokens),
`serviceaccount.yaml`, `ingress.yaml` (optional).

Every data-plane daemon mounts the **same** data PVC at `/srv/app/home`
(compose's `containerDataMount`) and pins to one node — arizuko's SQLite WAL
DB has exactly one writer. `replicas: 1`, no HPA, `strategy: Recreate`.

### Not included by default

- **whapd (WhatsApp) and twitd (X/Twitter)** use separate images and an
  auth-dir volume in compose. They're left out of the default `adapters` list
  — add a custom entry with the right image/volume if you run them.
- **The split daemons (authd / routd / runed)** behind `CUTOVER_SPLIT` are
  not modeled — this chart deploys the default gated monolith.
- **ttsd / kokoro / whisper** transcription and TTS sidecars.

## Agent spawn limitation (read this)

arizuko's `gated` daemon spawns a **fresh `arizuko-ant` container per turn**
through the host's container-runtime socket (`compose.go` mounts
`/var/run/docker.sock` and adds gated to the docker group). **Kubernetes pods
have no docker socket by default.**

**v1 approach (this chart):** mount the node's container-runtime socket into
the gated pod via a `hostPath` volume, gated behind `agentRuntime.dockerSocket`.

```yaml
agentRuntime:
  dockerSocket: true
  socketPath: /var/run/docker.sock
  dockerGid: 999 # `stat -c '%g' /var/run/docker.sock` on the node
```

This works on **single-node / docker-runtime clusters** (Docker Desktop,
k3s+docker, a kubeadm node using the docker shim) where that socket exists and
the runtime is docker. The agent containers gated launches are **siblings on
the node**, not k8s pods — Kubernetes does not see or schedule them, and the
`arizuko-ant` image must already be present on that node (`docker pull` it, or
`make agent`).

**It does NOT work** on containerd/cri-o nodes without a docker-compatible
socket, on managed clusters that block hostPath, or across multiple nodes
(the spawned containers land wherever the socket lives — pin gated with
`nodeName`).

**Future work (not built here):** a k8s-native runner where gated/runed
creates a `Job`/`Pod` per turn via the Kubernetes API (with a ServiceAccount +
RBAC) instead of the docker socket. That removes the docker-runtime
requirement and lets the scheduler place agent runs. Tracked as the natural
next step; this chart deliberately ships the socket model first because it
mirrors the current, working topology exactly.

## Prerequisites

- A Kubernetes cluster, ideally **single-node with a docker runtime** (see
  above).
- The two images built and reachable by the node:
  - `arizuko:latest` — `make images` (or `make build` + image build)
  - `arizuko-ant:latest` — `make agent`
  - plus `arizuko-vite:latest` (vited) and, if WebDAV is on,
    `arizuko-davd:latest`.
- A `StorageClass` that can provision an RWO `PersistentVolumeClaim`.

## Install

```bash
# 1. Set the required secrets + your web host. Generate the HMAC/signing
#    values with: openssl rand -hex 32
helm install arizuko deploy/helm/arizuko \
  --namespace arizuko --create-namespace \
  --set config.webHost=arizuko.example.com \
  --set secrets.channelSecret=$(openssl rand -hex 32) \
  --set secrets.authSecret=$(openssl rand -hex 32) \
  --set secrets.proxydHmacSecret=$(openssl rand -hex 32) \
  --set secrets.claudeCodeOauthToken=$CLAUDE_CODE_OAUTH_TOKEN \
  --set agentRuntime.dockerGid=$(stat -c '%g' /var/run/docker.sock)
```

For anything beyond a couple of `--set`s, use a values file:

```bash
helm install arizuko deploy/helm/arizuko -n arizuko --create-namespace \
  -f my-values.yaml
```

### Values you must set

| Value                                                           | Why                                                                                                                                   |
| --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `config.webHost`                                                | Public hostname; used by webd/proxyd and the Ingress.                                                                                 |
| `secrets.channelSecret`                                         | Adapter ↔ router HMAC (`CHANNEL_SECRET`).                                                                                             |
| `secrets.authSecret`                                            | JWT signing (`AUTH_SECRET`).                                                                                                          |
| `secrets.proxydHmacSecret`                                      | proxyd ↔ webd identity HMAC (`PROXYD_HMAC_SECRET`).                                                                                   |
| `secrets.claudeCodeOauthToken` **or** `secrets.anthropicApiKey` | The agent's LLM credential — gated forwards it into every spawned `arizuko-ant` container. Without it the agent can't call the model. |
| `agentRuntime.dockerGid`                                        | GID owning the node's docker socket.                                                                                                  |

Prefer `secrets.existingSecret: <name>` in production — point it at a Secret
you manage out-of-band (sealed-secrets, external-secrets, …) carrying the same
keys (`CHANNEL_SECRET`, `AUTH_SECRET`, `PROXYD_HMAC_SECRET`,
`CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY`, and any platform tokens). When
set, the chart renders no inline Secret.

### Enabling channel adapters

Adapters are off by default. Enable per adapter and supply its token:

```yaml
adapters:
  - name: teled
    enabled: true
    entrypoint: ['teled']
    secretEnv:
      - TELEGRAM_BOT_TOKEN
secrets:
  telegramBotToken: '123:abc...' # -> Secret key TELEGRAM_BOT_TOKEN
```

`secretEnv` lists Secret keys to inject as env vars of the same name; set the
matching `secrets.*` value (or provide via `secrets.existingSecret`). The env
var names match `template/services/<name>.toml` exactly.

### Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  host: arizuko.example.com # defaults to config.webHost
  tls:
    - secretName: arizuko-tls
      hosts: [arizuko.example.com]
```

All public traffic enters at proxyd `/`; proxyd auth-gates and fans out to
webd / vited / dashd internally.

## Verify

```bash
# Pods Ready
kubectl -n arizuko get pods -l app.kubernetes.io/instance=arizuko
kubectl -n arizuko rollout status deploy/arizuko-gated

# gated /health from inside the cluster
kubectl -n arizuko run -it --rm curl --image=curlimages/curl --restart=Never -- \
  curl -s http://arizuko-gated:8080/health

# proxyd reachable (port-forward if no Ingress)
kubectl -n arizuko port-forward svc/arizuko-proxyd 8080:8080
curl -sI http://localhost:8080/   # proxyd should respond

# Agent spawn working: send a message and watch gated
kubectl -n arizuko logs deploy/arizuko-gated -f
```

Red flags (mirror the root CLAUDE.md "Nothing works" checklist):

- `pull access denied for arizuko-ant` in gated logs → the agent image isn't
  on the node. `make agent` / pull it.
- Turns hang with no spawn → `agentRuntime.dockerSocket` off, wrong
  `socketPath`, wrong `dockerGid`, or a non-docker node runtime.
- Adapter pod `/health` returns 503 → the platform link is down (token wrong,
  QR not scanned, stream dropped), not a chart problem.

## Uninstall

```bash
helm uninstall arizuko -n arizuko
# The data PVC is retained by default — delete it manually if you mean it:
kubectl -n arizuko delete pvc arizuko-data
```
