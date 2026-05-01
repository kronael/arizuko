---
status: draft
depends: [9-crackbox-standalone, 10-crackbox-arizuko]
---

# Egred secrets injection

> Secrets never enter the sandbox. Egred (the proxy) replaces
> placeholders on egress.

## Problem

Container/VM has secrets in env → can exfiltrate them. Even with
domain filtering, a compromised agent could POST secrets to an
allowed domain.

## Solution

1. Sandbox gets **placeholder** values, not real secrets.
2. Real secrets stored in egred (per-source-id, alongside the
   allowlist).
3. Egred replaces placeholders with real values in outbound
   requests.

Sandbox never sees real secrets. Can't leak what you don't have.

## Where this lives

- **egred** (`crackbox/cmd/egred/`, `crackbox/pkg/proxy/`) — the
  placeholder→real substitution at egress. Only the proxy can
  MITM cleanly.
- **arizuko's [`store.secrets`](../5/32-tenant-self-service.md)
  table** — owns the actual secret values and per-folder/per-user
  scoping.
- **`sandd`** (or today's `gated`) — at spawn time, derives the
  per-id placeholder map and POSTs it to egred alongside the
  allowlist register.

```
secrets table → arizuko picks (folder, user) overlay → flat
  {placeholder: real} map → POST /v1/register with secrets
  → egred holds the map → on outbound, replace inline
```

## Spec format

Sent to egred via the existing register endpoint, augmented:

```yaml
POST /v1/register
{
  "ip": "10.99.x.y",
  "id": "rhias",
  "allowlist": ["api.anthropic.com"],
  "secrets": {
    "ANTHROPIC_API_KEY": {
      "placeholder": "sk-ant-PLACEHOLDER-anthropic",
      "value": "sk-ant-api03-real-key-here",
      "inject": [
        {"header": "x-api-key"},
        {"body": true}
      ],
      "domains": ["api.anthropic.com"]
    }
  }
}
```

## Placeholder requirements

Placeholders must:

- Be unique enough to not collide with real data
- Match expected format (prefix, length) so client validation passes
- Be obviously fake on inspection

Suggested pattern: `{prefix}PLACEHOLDER_{name}`

Examples:

- `sk-ant-PLACEHOLDER-anthropic` (Anthropic format)
- `ghp_PLACEHOLDER_github` (GitHub format)
- `sk-PLACEHOLDER-openai` (OpenAI format)
- `xoxb-PLACEHOLDER-slack` (Slack format)

## Injection modes

### Header injection (default)

Replace placeholder in any header value:

```
GET /v1/messages HTTP/1.1
x-api-key: sk-ant-PLACEHOLDER-anthropic
           ↓ egred replaces ↓
x-api-key: sk-ant-api03-real-key-here
```

### Header with format

Use `format: 'Bearer {value}'` to wrap the secret in a template.

### Body injection

Replace in request body (JSON, form data, etc.):

```json
{"api_key": "sk-PLACEHOLDER-openai", "prompt": "hello"}
              ↓ egred replaces ↓
{"api_key": "sk-real-openai-key", "prompt": "hello"}
```

**Caution**: Body injection is string replacement, not JSON-aware.
Placeholder must not appear in user content.

## TLS termination

CONNECT-tunneled HTTPS is opaque to egred today (the whole point
of the v1 forward proxy is no MITM). Secrets injection requires
egred to **terminate TLS for whitelisted destinations** so it can
modify request bytes.

This is selective MITM:

- Only for destinations in the per-id allowlist
- Only for ids that have secrets configured
- Per-destination CA cert distributed to the sandbox via env or
  filesystem mount

Anything not in the secrets-enabled set keeps the current
CONNECT-splice behavior — opaque, no certificate manipulation.

## Operator CLI

```bash
# Add secret to a folder's spawns
arizuko secret <inst> set <folder> ANTHROPIC_API_KEY \
  --placeholder "sk-ant-PLACEHOLDER-anthropic" \
  --value "sk-ant-real-key" \
  --header x-api-key \
  --domain api.anthropic.com

# List (shows placeholders, not values)
arizuko secret <inst> list <folder>

# Remove
arizuko secret <inst> rm <folder> ANTHROPIC_API_KEY
```

Plus a standalone form for the `crackbox run` CLI:

```bash
crackbox run --allow api.anthropic.com \
  --secret ANTHROPIC_API_KEY=sk-real,header=x-api-key,placeholder=sk-PLACEHOLDER \
  -- claude
```

## Security properties

1. **No exfiltration**: Sandbox can't leak secrets it doesn't have.
2. **Scoped injection**: Secrets only injected for allowed domains.
3. **Audit trail**: Egred can log which secrets were used, when,
   where.
4. **Revocation**: Change secret in arizuko store, next spawn picks
   it up; sandbox unaffected.

## Out of scope

Secret rotation mid-run, response scanning, HSM integration,
non-HTTP secret access (secrets socket for MCP servers needing
local auth).
