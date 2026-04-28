---
status: draft
depends: 9-crackbox-standalone
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
# crackbox secrets config (per-sandbox or global)
secrets:
  ANTHROPIC_API_KEY:
    placeholder: 'sk-ant-PLACEHOLDER-anthropic'
    value: 'sk-ant-api03-real-key-here'
    # Optional: injection points
    inject:
      - header: 'x-api-key' # replace in header value
      - header: 'Authorization' # replace in Authorization header
      - body: true # replace in request body

  GITHUB_TOKEN:
    placeholder: 'ghp_PLACEHOLDER_github'
    value: 'ghp_realtoken123'
    inject:
      - header: 'Authorization'
        format: 'Bearer {value}' # wrap in format string

  OPENAI_API_KEY:
    placeholder: 'sk-PLACEHOLDER-openai'
    value: 'sk-real-openai-key'
    inject:
      - header: 'Authorization'
        format: 'Bearer {value}'
      - body: true
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

Wrap value in format string:

```
Authorization: Bearer ghp_PLACEHOLDER_github
               ↓ proxy replaces ↓
Authorization: Bearer ghp_realtoken123
```

Or if placeholder is the raw value:

```yaml
GITHUB_TOKEN:
  placeholder: 'PLACEHOLDER_GITHUB'
  value: 'ghp_realtoken123'
  inject:
    - header: 'Authorization'
      format: 'Bearer {value}'
```

```
Authorization: Bearer PLACEHOLDER_GITHUB
               ↓ proxy replaces ↓
Authorization: Bearer ghp_realtoken123
```

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

## Proxy implementation

```go
// In proxy request handler
func (p *Proxy) injectSecrets(req *http.Request, sandbox *Sandbox) {
    for _, sec := range sandbox.Secrets {
        // Header injection
        for name, values := range req.Header {
            for i, v := range values {
                if strings.Contains(v, sec.Placeholder) {
                    replacement := sec.Value
                    if sec.Format != "" {
                        replacement = strings.Replace(sec.Format, "{value}", sec.Value, 1)
                    }
                    req.Header[name][i] = strings.Replace(v, sec.Placeholder, replacement, -1)
                }
            }
        }

        // Body injection (if enabled and body exists)
        if sec.InjectBody && req.Body != nil {
            body, _ := io.ReadAll(req.Body)
            body = bytes.ReplaceAll(body, []byte(sec.Placeholder), []byte(sec.Value))
            req.Body = io.NopCloser(bytes.NewReader(body))
            req.ContentLength = int64(len(body))
        }
    }
}
```

## Container env

Container receives placeholders as env vars:

```bash
# Inside sandbox
echo $ANTHROPIC_API_KEY
# sk-ant-PLACEHOLDER-anthropic

# HTTP request uses placeholder
curl -H "x-api-key: $ANTHROPIC_API_KEY" https://api.anthropic.com/v1/messages
# Proxy intercepts, replaces placeholder with real key
# Request reaches Anthropic with real key
```

## Security properties

1. **No exfiltration**: Container can't leak secrets it doesn't have
2. **Scoped injection**: Secrets only injected for allowed domains
3. **Audit trail**: Proxy can log which secrets were used, when, where
4. **Revocation**: Change secret in proxy, container unaffected

## Domain binding (optional)

Restrict secret injection to specific domains:

```yaml
ANTHROPIC_API_KEY:
  placeholder: 'sk-ant-PLACEHOLDER'
  value: 'sk-ant-real'
  domains:
    - api.anthropic.com
    - anthropic.com
```

Proxy only injects this secret for requests to those domains.
Prevents secret from being sent to wrong endpoint.

## Arizuko integration

In spec 44 context, secrets come from `secrets` table:

```go
// gated resolves secrets per folder/user
secrets := store.ResolveSecrets(folder, userJID)

// Pass to crackbox as placeholder→value map
iso.Run(crackbox.RunOpts{
    Secrets: crackbox.SecretsFromMap(secrets),
    // auto-generates placeholders, injects into container env
})
```

The `SecretsFromMap` helper:

- Generates placeholder per secret (prefix + random)
- Returns container env (placeholder values)
- Configures proxy injection rules

## Migration from current injection

Current arizuko flow (pre-crackbox):

```
gated → container env vars → ant process.env → injectMcpEnv() → MCP servers
         (real secrets)      (real secrets)     (real secrets)
```

Post-crackbox flow:

```
gated → crackbox proxy (stores real secrets)
     ↓
     → container env vars → ant process.env → MCP servers
        (placeholders)       (placeholders)    (placeholders)
                                    ↓
                            HTTP request with placeholder
                                    ↓
                            crackbox proxy intercepts
                                    ↓
                            replaces placeholder → real secret
                                    ↓
                            request reaches API with real secret
```

The `injectMcpEnv()` in `ant/src/index.ts` remains but becomes a no-op for
secrets — it passes placeholders through. Proxy handles the real injection.

MCP servers that make HTTP requests (GitHub, filesystem with remote, etc.)
work unchanged — their requests go through proxy, placeholders get replaced.

MCP servers that need secrets for non-HTTP purposes (local auth, signing)
need special handling — either:

1. Don't sandbox them (run on host)
2. Use a secrets socket (MCP server requests secret via IPC, gets real value)

Option 2 requires a `crackbox secret-socket` that MCP servers can query.
Out of scope for this spec.

## Not in this spec

- Secret rotation (future)
- Per-request secret selection (future)
- Response body scanning/redaction (future)
- HSM/vault integration (future)
- Non-HTTP secret access (secrets socket for local use)
