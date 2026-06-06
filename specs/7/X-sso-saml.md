---
status: draft
---

# specs/7/X — Enterprise SSO: SAML 2.0 + OIDC

## What this solves

OAuth social login (GitHub, Google, Discord) requires each user to have a
personal account on those platforms. Enterprise IT cannot use it: they need
authentication against their corporate IdP (Okta, Azure AD, Ping, Keycloak)
via SAML 2.0 or OIDC so that:

- Onboarding/offboarding is controlled by IT, not by the operator manually
  revoking `user_groups` rows
- MFA, conditional access, and device posture policies apply automatically
- A single identity spans all enterprise apps — no per-app account creation

This spec adds SAML 2.0 SP-initiated SSO and OIDC as two new auth providers
layered on top of the existing `auth/oauth.go` flow. The resulting principal
sub (`saml:<nameID>` or `oidc:<sub>`) fits the existing `user_groups` /
`canoncial_sub` / grants machinery unchanged.

## Scope

- **SAML 2.0 SP-initiated flow**: `/auth/saml/start` → IdP → `/auth/saml/callback`
- **OIDC Authorization Code flow**: `/auth/oidc/start` → IdP → `/auth/oidc/callback`
- **SP metadata endpoint**: `/auth/saml/metadata` (XML, consumed by IdP admin)
- **SCIM v2 provisioning endpoint**: `/auth/scim/v2/Users` — push-based
  user+group sync from Okta/Azure AD (see below)
- **JIT provisioning**: on first SSO login, insert `user_groups` row for
  the configured default glob (e.g. `world/*`) if no existing rows match
- **IdP-initiated logout** (SAML SLO): optional, controlled by `SAML_SLO=true`

Out of scope: SAML attribute-to-grants mapping (use SCIM or manual grants);
multi-IdP per instance (one IdP per instance, use OIDC federation at IdP layer
for multiple directories); Kerberos/NTLM.

## Sub format

| Flow | Sub format         | Example                       |
| ---- | ------------------ | ----------------------------- |
| SAML | `saml:<NameID>`    | `saml:alice@corp.example.com` |
| OIDC | `oidc:<sub claim>` | `oidc:A1B2C3D4`               |

`CanonicalSub` normalises these the same way existing providers are normalised.
Existing `user_groups` rows with matching subs continue to work — no migration.

## Config

| Env var                 | Default                | Description                                                         |
| ----------------------- | ---------------------- | ------------------------------------------------------------------- |
| `SAML_IDP_METADATA_URL` | ``                     | URL to IdP SAML metadata XML (Okta/Azure AD URL)                    |
| `SAML_IDP_METADATA_XML` | ``                     | Inline IdP metadata XML (alternative to URL)                        |
| `SAML_SP_CERT`          | ``                     | Path to PEM SP signing cert (auto-generated if absent)              |
| `SAML_SP_KEY`           | ``                     | Path to PEM SP private key                                          |
| `SAML_NAMEID_FORMAT`    | `emailAddress`         | NameID format to request                                            |
| `SAML_SLO`              | `false`                | Enable SP-initiated SAML Single Logout                              |
| `OIDC_ISSUER`           | ``                     | OIDC issuer URL (discovery via `/.well-known/openid-configuration`) |
| `OIDC_CLIENT_ID`        | ``                     | OIDC client ID                                                      |
| `OIDC_CLIENT_SECRET`    | ``                     | OIDC client secret                                                  |
| `OIDC_SCOPES`           | `openid email profile` | Space-separated scopes                                              |
| `SSO_DEFAULT_GLOB`      | ``                     | `user_groups` glob inserted on JIT provisioning (e.g. `world/*`)    |
| `SCIM_BEARER_TOKEN`     | ``                     | Bearer token the IdP uses to authenticate SCIM calls                |

SAML is enabled when `SAML_IDP_METADATA_URL` or `SAML_IDP_METADATA_XML` is
set. OIDC is enabled when `OIDC_ISSUER` is set. Both can be active; users land
on whichever login button they click. Existing OAuth buttons remain.

## Endpoints

```
GET  /auth/saml/metadata       SP metadata XML (register with IdP admin)
GET  /auth/saml/start          redirect to IdP login
POST /auth/saml/callback       ACS (AssertionConsumerService) endpoint
GET  /auth/saml/logout         SP-initiated SLO (SAML_SLO=true only)

GET  /auth/oidc/start          redirect to IdP OIDC authorisation endpoint
GET  /auth/oidc/callback        exchange code, fetch userinfo, issue JWT
```

All new endpoints live in `auth/sso.go` (new file). Registered in `auth/web.go`
alongside the existing OAuth routes — no change to proxyd routing or JWT
issuance.

## SCIM v2 provisioning

When `SCIM_BEARER_TOKEN` is set, the endpoint `/auth/scim/v2/Users` accepts
push events from Okta/Azure AD:

| Method      | Path                       | Action                                                  |
| ----------- | -------------------------- | ------------------------------------------------------- |
| `POST`      | `/auth/scim/v2/Users`      | Create: insert `user_groups` row for `SSO_DEFAULT_GLOB` |
| `PUT/PATCH` | `/auth/scim/v2/Users/{id}` | Update: rename sub if email changed                     |
| `DELETE`    | `/auth/scim/v2/Users/{id}` | Deprovision: delete all `user_groups` rows for sub      |

SCIM subjects map to `saml:<email>` or `oidc:<externalId>` subs (same as SSO
login) so SCIM provisioning and SSO login share the same principal identity.

SCIM is optional — JIT provisioning alone is sufficient for most deployments.
SCIM adds automatic deprovisioning (user removed from IdP → `user_groups` rows
deleted within seconds rather than waiting for next login attempt).

SCIM response schema: RFC 7643 `User` resource, `id` = sub. No group sync in
v1 (grants are managed by the operator; SCIM only controls presence/absence in
`user_groups`).

## Implementation sketch

**`auth/sso.go`** (new):

```
// SAML
registerSAML(cfg) — loads IdP metadata, generates SP cert if absent,
  registers /auth/saml/metadata, /auth/saml/start, /auth/saml/callback
  using crewjam/saml or a thin hand-rolled SP (see library choice below)

// OIDC
registerOIDC(cfg) — discovers endpoints from OIDC issuer URL,
  registers /auth/oidc/start, /auth/oidc/callback
  using coreos/go-oidc + golang.org/x/oauth2

// SCIM
registerSCIM(cfg) — registers /auth/scim/v2/Users REST handler
```

**State and JWT issuance**: identical to existing `dispatchOAuth` path —
SSO callback resolves to a sub string and calls `dispatchOAuth(sub, name, ...)`.
No changes to JWT issuance, cookie handling, or `CanonicalSub` logic.

**Library choice**: `crewjam/saml` is the de-facto Go SAML SP library
(MIT, well-maintained, used by HashiCorp Vault). `coreos/go-oidc` for OIDC
(same ecosystem). Both are thin wrappers — no frameworks.

**SP cert auto-generation**: if `SAML_SP_CERT`/`SAML_SP_KEY` are absent,
generate a self-signed cert on startup and persist it to `<DATA_DIR>/saml.crt`
/ `saml.key`. Log the cert fingerprint so the operator can register it with
the IdP. Regenerating on every restart would break the IdP registration — persist
once, never overwrite unless deleted.

## JIT provisioning flow

1. SSO callback resolves sub (e.g. `saml:alice@corp.example.com`)
2. `store.CanonicalSub(sub)` — returns existing canonical sub if linked, else sub
3. `store.UserGroupsForSub(sub)` — check for existing `user_groups` rows
4. If none and `SSO_DEFAULT_GLOB != ""`: insert `user_groups(sub, SSO_DEFAULT_GLOB)`
5. Continue to JWT issuance (same as OAuth)

If `SSO_DEFAULT_GLOB` is empty and the user has no rows, they land on a
"no access configured" page — same behaviour as an unrecognised OAuth user today.

## What's out of scope

- SAML attribute-to-grants mapping (`eduPersonEntitlement` → `user_groups` rows).
  Use SCIM group sync or operator-managed grants.
- IdP-initiated SAML flow (IdP pushes assertion without SP redirect). Most
  enterprise IdPs support SP-initiated; IdP-initiated adds attack surface.
- Multi-IdP per instance. Federation at the IdP layer (Okta → Azure AD chaining)
  covers this without complicating the SP.
- Encrypted SAML assertions (signing is required; encryption is optional and
  adds key management complexity for minimal security gain when TLS is in use).
