# proxycfg

proxycfg renders the `proxyd_routes` rows as a Caddyfile, so an operator can
front arizuko with an off-the-shelf proxy. `arizuko proxy-config <instance>
[--listen :8080] [--forward-auth host:port]` prints the file and writes
nothing. The rows and their fields are described in
[`proxyd/README.md` § Route rows](../proxyd/README.md#route-rows).

The render is a second reader of one declaration, never a second format:
`arizuko generate` writes the rows, proxyd reads them per request, and
`proxycfg.Caddyfile` reads the same rows. Four rules keep the render
faithful to what proxyd does (`proxycfg/caddy.go`):

- **Emission order carries longest-prefix.** proxyd picks the longest
  matching path; Caddy matches `handle` blocks in written order. The render
  sorts rows longest path first. A trailing-slash path becomes a `path*`
  matcher; any other path matches exactly.
- **`optional` takes the auth hop.** Every `auth` value except `public`
  renders a `forward_auth` to arizuko's `/auth/forward`, because that hop
  is where a caller who offers a credential gets identified.
- **`copy_headers` is the whole of `auth.TransitHeaders`.** A render that
  copies a subset drops `X-User-Name`, and every caller becomes anonymous
  while every request still returns 200.
- **`gated_by` is not evaluated again.** `generate` dropped a gated route
  before it wrote the row, so every row that exists already passed its
  gate. A second evaluation reads env that the render does not see and
  drops live routes.

Without `--forward-auth`, an authenticated route renders as `respond 503`
with a comment. A proxy hop without the auth hop would serve the route to
anyone.

The render covers the rows only. proxyd's hard-wired paths, `/pub/*`,
`/priv/*`, `/auth/*`, `/health`, the world-host redirect and the `/dav/`
write-block, do not appear in it. A Caddy front that replaces proxyd
therefore loses them.
