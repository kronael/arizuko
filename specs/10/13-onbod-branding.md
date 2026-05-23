---
status: draft
---

# 13 — onbod branding

Make onbod's user-facing surfaces brand-aware so a deployment can
present its own product identity. Branding is operator-configured
per-instance — never derived from filesystem paths.

## Knobs

| Var                       | Effect                                                       |
| ------------------------- | ------------------------------------------------------------ |
| `BRAND_NAME`              | Service name in page titles, dashboard header, OAuth blurbs. |
| `BRAND_TAGLINE`           | One-line subtitle on the auth landing page.                  |
| `BRAND_CONTACT`           | Mailto / URL on invalid/expired/used invite pages.           |
| `BRAND_TERMS_URL`         | Optional terms-of-service link in the auth-link prompt.      |
| `BRAND_PRIVACY_URL`       | Optional privacy policy link.                                |
| `BRAND_THEME`             | Named built-in palette (`slate`, `forest`, `amber`, `zinc`). |
| `BRAND_ASSET_DIR`         | Path containing `logo.svg`, `favicon.ico`, `style.css`.      |
| `BRAND_OAUTH_SCOPE_BLURB` | Plain-language blurb shown above OAuth provider buttons.     |

Defaults preserve current behaviour: `BRAND_NAME=arizuko`, no tagline,
no asset overrides, built-in dark theme.

`ONBOARDING_GREETING` stays — it's a per-message greeting, not brand.

## Where it shows

- **Auth-link prompt** (`promptUnprompted`): prepend `BRAND_NAME` (+
  `BRAND_TAGLINE` if set); `ONBOARDING_GREETING` remains a separate layer.
- **All `renderPage` calls**: `<title>` becomes `<page-title> · <BRAND_NAME>`.
- **Dashboard `/onboard?...`**: render `BRAND_NAME` in the existing
  `<div class="brand">` user-header.
- **Auth-landing page**: show `BRAND_NAME` + `BRAND_TAGLINE` above OAuth
  buttons; `BRAND_OAUTH_SCOPE_BLURB` below.
- **Queue page**: brand header instead of bare "You're in the queue."
- **Invalid / expired / used invite pages**: include `BRAND_CONTACT`.
- **Static asset routes** (new):
  - `GET /onbod/logo.svg` → `BRAND_ASSET_DIR/logo.svg` or bundled default.
  - `GET /onbod/favicon.ico` → ditto.
  - `GET /onbod/style.css` → operator stylesheet, included after built-in
    theme so it can override.

## Asset shipping

`BRAND_ASSET_DIR` is mounted read-only into `arizuko_onbod_<inst>` via
the compose generator. Operators drop `logo.svg` / `style.css` in
`<data-dir>/brand/`; compose mounts it. Brand is data, not code.

## Acceptance

- No brand vars set → identical to today.
- `BRAND_NAME=Foo` → "Foo" in all page titles, dashboard header, queue page.
- `BRAND_ASSET_DIR` with `logo.svg` + `style.css` → rendered on every onbod
  page over the built-in base theme.
- `BRAND_TERMS_URL` / `BRAND_PRIVACY_URL` → optional links in auth-link
  prompt; absent → prompt unchanged.
- Tests: extend `onbod/main_test.go` — brand-rendering case asserting
  `BRAND_NAME` in title, header, queue, and invite-error pages; assert
  default rendering unchanged when vars are absent.
