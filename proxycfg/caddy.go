// Package proxycfg renders arizuko's proxyd_routes rows as an off-the-shelf
// proxy's configuration (spec 6/22 step 2).
//
// It is a second READER of one declaration, never a second format: the rows are
// written by `arizuko generate` (store.SyncProxydRoutes) and read by proxyd's
// boot path. This package adds a third reader for the same rows, which is what
// makes "cut proxyd to its irreplaceable half" a config question rather than a
// rewrite — the routing table already lives somewhere both can read.
//
// What it CANNOT render, and why that is step 3 rather than a gap here: proxyd
// mints a `service:proxyd` ES256 bearer per request and trust-stamps X-User-*,
// and no off-the-shelf proxy can mint that. Every authenticated route therefore
// emits a `forward_auth` hop to an arizuko endpoint that must answer with the
// proof. Until that endpoint exists the rendered config routes correctly and
// authenticates nothing, so it is not a drop-in replacement and does not
// pretend to be.
package proxycfg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kronael/arizuko/store"
)

// Options are the deployment facts the rows do not carry.
type Options struct {
	// Listen is the Caddy site address, e.g. ":8080" or "arizuko.example".
	Listen string
	// ForwardAuth is the arizuko endpoint that answers 200 + X-User-* or 401,
	// and returns the transit proof (step 3). Empty renders authenticated
	// routes with a loud comment instead of a hop that would silently pass.
	ForwardAuth string
}

// authHeaders are the identity headers a backend trusts once the transit proof
// holds (SECURITY.md §"Identity header trust"). They must survive the auth hop.
var authHeaders = []string{"X-User-Sub", "X-User-Groups", "Authorization"}

// Caddyfile renders the routes. Order is longest-prefix-first because proxyd
// matches that way and Caddy matches its handle blocks in written order — the
// one place where a faithful render depends on emission order rather than on
// the fields.
func Caddyfile(routes []store.ProxydRoute, opts Options) (string, error) {
	if opts.Listen == "" {
		return "", fmt.Errorf("proxycfg: Listen is required; the rows do not carry the site address")
	}
	// gated_by is NOT re-evaluated here. It is generate-time-only metadata
	// (proxyd/routes.go:21) — compose.collectProxydRoutes drops a gated route
	// before the row is written, so every row that EXISTS has already passed
	// its gate. Re-gating on the render dropped two live krons routes (ttsd,
	// davd) whose vars live in the generated env/ rather than .env — the L45
	// shape a second time: a second evaluation of one decision.
	kept := append([]store.ProxydRoute(nil), routes...)
	sort.SliceStable(kept, func(i, j int) bool { return len(kept[i].Path) > len(kept[j].Path) })

	var b strings.Builder
	fmt.Fprintf(&b, "# Generated from proxyd_routes by arizuko (spec 6/22 step 2).\n")
	fmt.Fprintf(&b, "# Edits are overwritten; change the rows, not this file.\n")
	fmt.Fprintf(&b, "%s {\n", opts.Listen)
	for _, r := range kept {
		if err := renderRoute(&b, r, opts); err != nil {
			return "", err
		}
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func renderRoute(b *strings.Builder, r store.ProxydRoute, opts Options) error {
	if r.Path == "" {
		return fmt.Errorf("proxycfg: route with empty path (backend %q)", r.Backend)
	}
	// A redirect route has no backend and never reaches a proxy hop.
	if r.RedirectTo != "" {
		fmt.Fprintf(b, "\tredir %s %s 302\n", matcher(r.Path), r.RedirectTo)
		return nil
	}
	if r.Backend == "" {
		return fmt.Errorf("proxycfg: route %q has neither backend nor redirect_to", r.Path)
	}
	// handle_path strips the prefix, handle keeps it — the same two shapes
	// proxyd's director picks between on StripPrefix.
	directive := "handle"
	if r.StripPrefix {
		directive = "handle_path"
	}
	fmt.Fprintf(b, "\t%s %s {\n", directive, matcher(r.Path))
	if authenticated(r.Auth) {
		if opts.ForwardAuth == "" {
			fmt.Fprintf(b, "\t\t# REFUSED: auth=%q needs a forward-auth endpoint (6/22 step 3);\n", r.Auth)
			fmt.Fprintf(b, "\t\t# rendering the proxy hop without it would serve this route unauthenticated.\n")
			b.WriteString("\t\trespond 503\n\t}\n")
			return nil
		}
		fmt.Fprintf(b, "\t\tforward_auth %s {\n", opts.ForwardAuth)
		fmt.Fprintf(b, "\t\t\turi /auth/forward?route=%s\n", r.Path)
		fmt.Fprintf(b, "\t\t\tcopy_headers %s\n", strings.Join(authHeaders, " "))
		b.WriteString("\t\t}\n")
	}
	for _, h := range r.PreserveHeaders {
		fmt.Fprintf(b, "\t\trequest_header %s {http.request.header.%s}\n", h, h)
	}
	fmt.Fprintf(b, "\t\treverse_proxy %s\n", r.Backend)
	b.WriteString("\t}\n")
	return nil
}

// authenticated reports whether a route's auth class needs the forward-auth hop.
// `public` does not; `optional` does, because proxyd identifies a caller who
// offers a credential and the hop is where that happens.
func authenticated(auth string) bool { return auth != "" && auth != "public" }

// matcher turns proxyd's longest-prefix path into a Caddy path matcher. A
// trailing slash is a prefix in proxyd, so it becomes a wildcard here; anything
// else matches exactly, which is what a bare path means on both sides.
func matcher(path string) string {
	if strings.HasSuffix(path, "/") {
		return path + "*"
	}
	return path
}
