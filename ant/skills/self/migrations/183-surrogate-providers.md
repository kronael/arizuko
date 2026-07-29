## operator-configurable OAuth providers — surrogate (spec `5/15`)

**Operator feature, no agent action.** Adding a "Connect &lt;service&gt;" OAuth
provider is now pure configuration — no code, no rebuild.

- Drop `<datadir>/surrogate/<name>.toml` (`auth_url`/`token_url`/`secret_key`
  required; a missing one is a hard boot error naming the file). A same-named
  file overrides an embedded default.
- Set `SURROGATE_<NAME>_CLIENT_ID` / `_CLIENT_SECRET` in `.env` (NAME upper-cased,
  `-`→`_`). Creds now bind for **every** registered provider, not just github.
- Register the redirect URI `<WEB_HOST>/dash/me/connections/<name>/callback`,
  restart → "Connect &lt;name&gt;" appears in `/dash/me/connections`.

`github` and `google` ship built-in. Recipe: `EXTENDING.md` §"Add an OAuth
provider" / web how-to `howto/oauth-provider.html`.
