---
status: draft
---

# 13 — onbod branding

Make the onboarding daemon's user-facing surfaces (auth-link
prompts, OAuth landing, dashboard, queue page, invite redemption)
brand-aware so a deployment can present its own product identity
instead of arizuko defaults. Branding is operator-configured
per-instance — never derived from filesystem paths.

## Why

`onbod` is the only daemon every new user hits before reaching the
agent. It currently shows arizuko-default copy and the only knob is
`ONBOARDING_GREETING` (a single line prepended to the auth-link
prompt). That's insufficient for any deployment that wants to
present itself as a distinct product (e.g. a sandboxed agent
service, a tenant-scoped offering, a partner-branded lobby).

The user shouldn't have to know they're using arizuko. The operator
shouldn't have to fork onbod or patch HTML to brand their service.

## What changes

A small set of env-vars + one optional asset directory:

| Knob                      | Effect                                                             |
| ------------------------- | ------------------------------------------------------------------ |
| `BRAND_NAME`              | Service name in page titles, dashboard header, OAuth blurbs.       |
| `BRAND_TAGLINE`           | One-line subtitle under the name on the auth landing page.         |
| `BRAND_CONTACT`           | Mailto / URL shown on Invalid/Expired/Used invite pages.           |
| `BRAND_TERMS_URL`         | Optional terms-of-service link in the auth-link prompt body.       |
| `BRAND_PRIVACY_URL`       | Optional privacy policy link.                                      |
| `BRAND_THEME`             | Named built-in palette (`slate`, `forest`, `amber`, `zinc`).       |
| `BRAND_ASSET_DIR`         | Path containing `logo.svg`, `favicon.ico`, `style.css`.            |
| `BRAND_OAUTH_SCOPE_BLURB` | Free-text plain-language blurb shown above OAuth provider buttons. |

Defaults preserve current behaviour: `BRAND_NAME=arizuko`, no
tagline, no asset overrides, theme = built-in dark.

`ONBOARDING_GREETING` stays — it's the per-message greeting, not
the brand.

## Where it shows

- **Auth-link prompt** (`promptUnprompted`): prepend
  `BRAND_NAME` (and `BRAND_TAGLINE` if set) to the first prompt;
  keep `ONBOARDING_GREETING` as a separate optional layer for
  message-level voice.
- **All `renderPage` calls**: page `<title>` becomes
  `<page-title> · <BRAND_NAME>`.
- **Dashboard `/onboard?...`**: `<div class="brand">` already
  exists in the user-header — render `BRAND_NAME` there next to
  the logo (today it shows username).
- **Auth-landing page** (token-presented but not yet OAuth'd):
  show `BRAND_NAME` + `BRAND_TAGLINE` above the OAuth provider
  buttons; `BRAND_OAUTH_SCOPE_BLURB` below them.
- **Queue page** (gated admission): brand header instead of bare
  "You're in the queue."
- **Invalid / expired / used invite pages**: include
  `BRAND_CONTACT` so users have somewhere to ask for a new link.
- **Static asset routes** (new):
  - `GET /onbod/logo.svg` → `BRAND_ASSET_DIR/logo.svg` (or
    bundled default).
  - `GET /onbod/favicon.ico` → ditto.
  - `GET /onbod/style.css` → operator's stylesheet, included
    after the built-in theme so it can override.

## What does NOT change

- Identity, container names, network names — still configured via
  the existing `ASSISTANT_NAME` / compose generation. Branding
  affects user-visible surfaces only; never operator-visible
  identity.
- The OAuth provider list itself. `GITHUB_*`, `GOOGLE_*`,
  `ALLOWED_ORG`, etc. retain their current semantics.
- Skill-level branding (per-group `SOUL.md`). Brand env vars are
  instance-wide; group personality stays group-local.

## Why env vars (not DB)

Brand identity is instance-level infrastructure config, not
per-user state. It's set once at deploy time and rarely changes.
DB rows are for state that changes per-user / per-group. Following
the existing CLAUDE.md split: business state in DB, infra toggles
in env.

## Asset shipping

`BRAND_ASSET_DIR` is mounted read-only into `arizuko_onbod_<inst>`
via the existing compose generator. Operators drop their `logo.svg`
and `style.css` in `<data-dir>/brand/`; compose mounts it. No
change to the build pipeline; brand is data, not code.

## Acceptance

- A deployment with no brand vars set looks identical to today.
- A deployment with `BRAND_NAME=Foo` shows "Foo" everywhere a
  user-facing label is rendered, including page titles and the
  dashboard header.
- A deployment with `BRAND_ASSET_DIR=…/brand/` containing
  `logo.svg` and `style.css` renders those on every onbod page;
  built-in theme remains as the layered base.
- `BRAND_TERMS_URL` / `BRAND_PRIVACY_URL` produce optional
  links in the auth-link prompt body — without them, the prompt
  is the same one users get today.
- Tests: extend `onbod/main_test.go` with a brand-rendering case
  asserting `BRAND_NAME` appears in title, header, queue page,
  and invite-error pages; assert default rendering unchanged when
  brand vars are absent.

## Out of scope

- Branded email subject/body templates (defer to a later spec
  alongside the Mastodon/Reddit/LinkedIn/Email outbound work).
- Per-folder branding overrides (one tenant inside a
  shared deployment running its own brand). The path-tier model
  doesn't currently expose folder-level web identity; if needed,
  this gets its own spec.
- Theming the agent's chat replies. Persona/voice already lives
  in `~/SOUL.md` per group; branding is the wrapper around it,
  not a replacement.
