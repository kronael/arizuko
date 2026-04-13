---
name: ops
description: >
  Use when writing Dockerfiles, CI pipelines, Ansible playbooks,
  or deployment config. Covers monitoring, logging, systemd.
---

# Ops

## Docker Build Patterns

### Base Images

- ALWAYS pin version explicitly (ubuntu:22.04, rust:1.75, python:3.12)
- NEVER use :latest or unversioned tags
- ALWAYS use multi-stage if intermediate layers >100MB
- ALWAYS use ENTRYPOINT for production, CMD for development

### Layer Caching

ALWAYS optimize for cache reuse. Order:

1. Base image + system deps
2. Language deps (Cargo.toml, requirements.txt, package.json)
3. Install/fetch dependencies
4. Copy source
5. Build

```dockerfile
# CORRECT - deps before source
COPY Cargo.toml Cargo.lock ./
RUN cargo fetch
COPY src ./src
RUN cargo build --release

# WRONG - source changes invalidate deps
COPY . .
RUN cargo build --release
```

### Cross-Compilation

ALWAYS volume mount source, NEVER copy into build image:

```bash
docker run -v $(pwd):/src builder make -C /src build
```

### Resource Limits

- ALWAYS set Docker memory limits (2GB typical)
- ALWAYS set build timeout (30m default)
- NEVER let builds run unbounded

## Configuration Management

- Three-level hierarchy: base TOML → env.toml → env vars
- Environment file: `${PREFIX:-/srv}/key/env.toml` overrides TOML
- Validation on load (fail fast at startup, not at runtime)

## Secrets Management

- Base config: `cfg/config.toml` (committed)
- Secrets: `/srv/key/env.toml` (NOT committed)
- Precedence: env.toml > env vars > config.toml
- chmod 600 for keypairs, service user owns certs

## Logging

Format: `Mon DD HH:MM:SS.fff [LEVEL] message key=value`

- error/warn/info/debug — standard levels; info for production
- Log rotation via logrotate (not in app)
- CRITICAL prefix for monitoring alerts

RUST_LOG:

```bash
RUST_LOG=info                    # Production
RUST_LOG=debug                   # Development
RUST_LOG=module::path=debug,info # Selective
```

## Monitoring

- Heartbeat files in ./tmp/<service>.heartbeat
- Health check endpoints: /.well-known/live
- Metrics: Prometheus format on /metrics

### Prometheus Cardinality

**NEVER as labels:** unbounded values, client-controlled input
**Safe labels:** bounded enums, validated against fixed set
**High cardinality → logs** (use trace context)

## Error Handling

- Retry: exponential backoff 100ms→1600ms
- ONLY retry transient errors (connection, timeout, unavailable)
- NEVER retry validation or business logic errors
- Alert on persistent errors (>10 failures)

## Process Management

- PID file on startup: `${PREFIX:-/srv}/run/<service>.pid`
- Graceful shutdown: SIGTERM/SIGINT (30s timeout)
- Exit codes: 0=success, 1=config error, 2=runtime error
- NEVER killall, ALWAYS kill by PID

## Data Storage

- Configuration: `${PREFIX:-/srv}/key/`
- Runtime state: `${PREFIX:-/srv}/run/`
- Data: `${PREFIX:-/srv}/data/<project_name>/`
- Logs: ./log/ (local) or syslog (production)
- NEVER use global /tmp/ for state

## Anti-Patterns

- Window calculations: use EWMA (alpha _ val + (1-alpha) _ prev), not sliding windows
- NEVER manually .close() async context managers — trust context managers

## Ansible

- Roles for common services (docker, nginx, grafana, rsyslog)
- Host-specific variables in host_vars/
- Secrets in ansible-vault encrypted files
- Idempotent tasks (safe to re-run), tags for selective deployment

### docker-service Role

- Containers MUST have `./main` or `python -m main`
- Naming: service names use underscores, image names use dashes
- Network: `--network=host` (no port mapping); EXPOSE for docs only
- Config: `/cfg/<server>/<service>.toml` as last argument
- Volumes: `/srv/spool/<name>` (persistent), `/srv/run/<name>` (runtime)

**host_vars service definition**:

```yaml
service:
  - image: my-service # Long-running
  - image: my-timer # Cron timer
    minute: '*/5'
    timeout: 600
  - image: my-calendar # Systemd calendar timer
    oncalendar: 'daily'
```

## Deployment

- Git-ops: config in git, changes via PRs, automated sync
- Blue-green or rolling updates (zero downtime)
- Rollback via git revert + redeploy

## CI/CD

ALWAYS use explicit make targets in CI: `make prepare`, `make image`, `make test`

NEVER: release builds locally, skip clean before CI, mix debug/release artifacts.

## Deployment Checklist

- Config: TOML schema, env overrides, secrets in `/srv/key/`
- Logging: structured format, RUST_LOG, logrotate
- Monitoring: Prometheus `/metrics`, `/.well-known/live`
- Storage: `${PREFIX}/data/`, state persistence
- Security: no secrets in logs, TLS at proxy, chmod 600 keys
- Operations: graceful shutdown, PID files, memory limits
