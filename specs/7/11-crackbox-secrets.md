---
status: draft
depends: [9-crackbox-standalone, 10-crackbox-arizuko]
---

# Crackbox secrets injection at proxy

> Secrets never enter the sandbox. Proxy replaces placeholders on egress.

## Problem

Container has secrets in env → can exfiltrate them. Even with domain
filtering, a compromised agent could POST secrets to an allowed domain.

## Solution

1. Container gets **placeholder** values, not real secrets
2. Real secrets stored in crackbox proxy (per-sandbox)
3. Proxy replaces placeholders with real values in outbound requests

Container never sees real secrets. Can't leak what you don't have.

## Spec format

```yaml
secrets:
  ANTHROPIC_API_KEY:
    placeholder: 'sk-ant-PLACEHOLDER-anthropic'
    value: 'sk-ant-api03-real-key-here'
    inject:
      - header: 'x-api-key'
      - body: true
    domains: [api.anthropic.com] # optional: restrict injection to these domains
```

## Placeholder requirements

Placeholders must:

- Be unique enough to not collide with real data
- Match expected format (prefix, length) so validation passes
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
           ↓ proxy replaces ↓
x-api-key: sk-ant-api03-real-key-here
```

### Header with format

Use `format: 'Bearer {value}'` to wrap the secret in a template.

### Body injection

Replace in request body (JSON, form data, etc.):

```json
{"api_key": "sk-PLACEHOLDER-openai", "prompt": "hello"}
              ↓ proxy replaces ↓
{"api_key": "sk-real-openai-key", "prompt": "hello"}
```

**Caution**: Body injection is string replacement, not JSON-aware.
Placeholder must not appear in user content.

## CLI

```bash
# Add secret to sandbox
crackbox secret <id> set ANTHROPIC_API_KEY \
  --placeholder "sk-ant-PLACEHOLDER-anthropic" \
  --value "sk-ant-real-key"

# Add with format
crackbox secret <id> set GITHUB_TOKEN \
  --placeholder "ghp_PLACEHOLDER" \
  --value "ghp_realtoken" \
  --header Authorization \
  --format "Bearer {value}"

# List secrets (shows placeholders, not values)
crackbox secret <id> list
# ANTHROPIC_API_KEY  sk-ant-PLACEHOLDER-anthropic  [header:x-api-key, body]
# GITHUB_TOKEN       ghp_PLACEHOLDER               [header:Authorization]

# Remove secret
crackbox secret <id> rm GITHUB_TOKEN

# Inject from env (auto-generate placeholder)
crackbox secret <id> from-env ANTHROPIC_API_KEY
# → placeholder: sk-ant-PLACEHOLDER-0a3f (random suffix)
# → value: (read from $ANTHROPIC_API_KEY)
```

## Security properties

1. **No exfiltration**: Container can't leak secrets it doesn't have
2. **Scoped injection**: Secrets only injected for allowed domains
3. **Audit trail**: Proxy can log which secrets were used, when, where
4. **Revocation**: Change secret in proxy, container unaffected

## Arizuko integration

`SecretsFromMap(secrets)` auto-generates placeholders, returns container env

- proxy rules. Secrets resolved via `store.ResolveSecrets(folder, userJID)`
  per spec 5/32. MCP servers receive placeholders; HTTP requests get injected
  at proxy egress.

## Out of scope

Secret rotation, response scanning, HSM integration, non-HTTP secret access
(secrets socket for MCP servers needing local auth).
