---
name: infra
description: >
  Root-group only — set the instance hosting domain + wildcard DNS so
  per-world hostnames resolve, verify reachability. USE for instance-level
  web setup. NOT for non-root groups (no permission), NOT for app code
  deploy (use web).
user-invocable: true
---

# Infra

Root-only. Per-world hostnames are **derived**, not assigned: world `W`
is served at `W.<HOSTING_DOMAIN>`, which proxyd 302s to `/pub/W/`. There
is no host-mapping file to edit — the host is the deterministic
composition of the world folder and `HOSTING_DOMAIN`.

## Steps

1. Set `HOSTING_DOMAIN` once in the instance `.env` (e.g. `fiu.wtf`).
   Every world hostname derives from it.
2. DNS: add a wildcard `*.<HOSTING_DOMAIN>` A/AAAA record pointing at the
   deployment, plus a wildcard TLS cert (reverse proxy / Let's Encrypt).
3. Verify: `dig +short <world>.<HOSTING_DOMAIN>` resolves, and
   `curl -sI https://<world>.<HOSTING_DOMAIN>/` returns `302 → /pub/<world>/`.

## Host label ≠ world name

When a world must answer on a host whose label is not its folder name,
add an explicit alias to the instance `.env`:
`WEB_VHOST_ALIASES=fab.krons.cx=atlas` (`host=world`, comma-separated).
proxyd consults the alias map before deriving. Aliases are the small
configured exception to the derived default — operator env, not a
web-dir file.

Spec: `specs/5/V-web-vhosts.md`.
