---
status: draft
relates-to: [../8/36-publish-plan, 26-single-world-deployment]
---

# specs/7/59 — a package runs one command and listens on one socket

A package that is not part of arizuko runs as a daemon of the deployment.
It declares one command. Daemons find each other through one socket directory.

## Today

A daemon outside the core cannot be an adapter without Go patches (BUGS
`L200`). Its service key, env file, routes and dashboard tile live in core Go
tables. An adapter registers with `POST /v1/channels/register` and a service
token (`routd/channels.go:32`).

## The change

A package's `arizuko.yml` declares how it runs:

```yaml
run: go run github.com/x/whatsapp-adapter@v1.4.0
env: [WHATSAPP_TOKEN]
routes: [/wa/]
```

- arizuko renders one compose service from its Debian base image, which
  has `go` and `bun`, with `command:` set to `run`. A package may set its own
  `image:` instead. The command fetches and builds; arizuko builds nothing.
- Every daemon listens on `/run/arizuko/<name>.sock` and serves its existing
  HTTP handlers there. Compose mounts that directory into every service.
- A daemon exists when its socket exists. There is no registration call.
- Socket file permissions decide who may connect. Local daemons need no
  service key.
- `env` names the `.env` keys that reach the daemon. `routes` are its public
  paths.

There is no D-Bus and no second supervisor. Compose supervises.

## Open question

How does routd know that a socket is a channel adapter? Recommendation: routd
asks each socket once for its channel description, so the adapter states it
and no core table lists it.

## Acceptance

1. A package outside this repo with only `run`, `env` and `routes` becomes a
   working channel adapter with no change to core Go.
2. Deleting its socket file removes it from routd and the dashboard.
3. `POST /v1/channels/register` and the per-adapter service keys are gone.
