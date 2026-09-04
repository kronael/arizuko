package proxycfg

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

func render(t *testing.T, routes []store.ProxydRoute, opts Options) string {
	t.Helper()
	if opts.Listen == "" {
		opts.Listen = ":8080"
	}
	out, err := Caddyfile(routes, opts)
	if err != nil {
		t.Fatalf("Caddyfile: %v", err)
	}
	return out
}

// TestCaddyfile_LongestPrefixFirst — proxyd matches longest-prefix; Caddy
// matches handle blocks in WRITTEN order. So a faithful render depends on
// emission order, not just on the fields: `/dash/approvals/` must be emitted
// before `/dash/`, or every approvals request lands on the dashboard.
func TestCaddyfile_LongestPrefixFirst(t *testing.T) {
	out := render(t, []store.ProxydRoute{
		{Path: "/dash/", Backend: "dashd:8080", Auth: "public"},
		{Path: "/dash/approvals/", Backend: "dashd:8080", Auth: "public"},
		{Path: "/", Backend: "vited:8080", Auth: "public"},
	}, Options{})

	iApprovals := strings.Index(out, "/dash/approvals/")
	iDash := strings.Index(out, "handle /dash/*")
	iRoot := strings.Index(out, "handle /*")
	if iApprovals < 0 || iDash < 0 || iRoot < 0 {
		t.Fatalf("a route is missing:\n%s", out)
	}
	if !(iApprovals < iDash && iDash < iRoot) {
		t.Errorf("emission order is not longest-first:\n%s", out)
	}
}

// TestCaddyfile_AuthenticatedRouteRefusesWithoutTheHop is the honesty check.
// proxyd mints a service:proxyd bearer per request and no off-the-shelf proxy
// can. Rendering the proxy hop with no forward-auth endpoint would serve an
// authenticated route to anyone, so it must refuse instead.
func TestCaddyfile_AuthenticatedRouteRefusesWithoutTheHop(t *testing.T) {
	out := render(t, []store.ProxydRoute{
		{Path: "/dash/", Backend: "dashd:8080", Auth: "user"},
	}, Options{})

	if strings.Contains(out, "reverse_proxy dashd:8080") {
		t.Errorf("an authenticated route was rendered as a bare proxy hop:\n%s", out)
	}
	if !strings.Contains(out, "respond 503") {
		t.Errorf("want a refusal, got:\n%s", out)
	}
}

// TestCaddyfile_AuthClassesMapToTheHop — `public` forwards directly; every
// other class, INCLUDING `optional`, takes the hop. optional is where proxyd
// identifies a caller who offers a credential, so skipping it there would drop
// attribution on the route-token surface.
func TestCaddyfile_AuthClassesMapToTheHop(t *testing.T) {
	for _, c := range []struct {
		auth    string
		wantHop bool
	}{
		{"public", false},
		{"user", true},
		{"operator", true},
		{"optional", true},
	} {
		out := render(t, []store.ProxydRoute{
			{Path: "/x/", Backend: "b:8080", Auth: c.auth},
		}, Options{ForwardAuth: "authd:8080"})
		got := strings.Contains(out, "forward_auth authd:8080")
		if got != c.wantHop {
			t.Errorf("auth=%q hop=%v, want %v:\n%s", c.auth, got, c.wantHop, out)
		}
		if !strings.Contains(out, "reverse_proxy b:8080") {
			t.Errorf("auth=%q lost its backend:\n%s", c.auth, out)
		}
	}
}

// TestCaddyfile_IdentityHeadersSurviveTheHop — a backend trusts X-User-* only
// when the transit proof holds, so the hop must copy them through. Dropping
// them silently logs every caller out.
func TestCaddyfile_IdentityHeadersSurviveTheHop(t *testing.T) {
	out := render(t, []store.ProxydRoute{
		{Path: "/dash/", Backend: "dashd:8080", Auth: "user"},
	}, Options{ForwardAuth: "authd:8080"})
	for _, h := range []string{"X-User-Sub", "X-User-Groups", "Authorization"} {
		if !strings.Contains(out, h) {
			t.Errorf("copy_headers is missing %s:\n%s", h, out)
		}
	}
}

// TestCaddyfile_GatedRowsAreAlreadyFiltered — gated_by is generate-time-only
// metadata (proxyd/routes.go:21): compose drops a gated route BEFORE writing
// the row, so a row that exists has passed its gate. Re-evaluating it on the
// render dropped two live krons routes whose vars live in the generated env/
// rather than .env.
func TestCaddyfile_GatedRowsAreAlreadyFiltered(t *testing.T) {
	out := render(t, []store.ProxydRoute{
		{Path: "/dash/ttsd/", Backend: "ttsd:8080", Auth: "public", GatedBy: "TTS_ENABLED"},
	}, Options{})
	if !strings.Contains(out, "reverse_proxy ttsd:8080") {
		t.Errorf("a row carrying gated_by must still render — it already passed:\n%s", out)
	}
}

// TestCaddyfile_StripPrefixPicksTheOtherDirective — proxyd's director either
// strips the prefix or keeps it, and Caddy spells that as two different
// directives rather than a flag.
func TestCaddyfile_StripPrefixPicksTheOtherDirective(t *testing.T) {
	keep := render(t, []store.ProxydRoute{{Path: "/a/", Backend: "b:1", Auth: "public"}}, Options{})
	strip := render(t, []store.ProxydRoute{{Path: "/a/", Backend: "b:1", Auth: "public", StripPrefix: true}}, Options{})
	if !strings.Contains(keep, "handle /a/*") || strings.Contains(keep, "handle_path") {
		t.Errorf("keep-prefix used the wrong directive:\n%s", keep)
	}
	if !strings.Contains(strip, "handle_path /a/*") {
		t.Errorf("strip-prefix used the wrong directive:\n%s", strip)
	}
}

// TestCaddyfile_RefusesARouteThatGoesNowhere — a row with neither backend nor
// redirect is a generate-time fault; rendering it as an empty block would make
// the proxy answer 200 with nothing.
func TestCaddyfile_RefusesARouteThatGoesNowhere(t *testing.T) {
	if _, err := Caddyfile([]store.ProxydRoute{{Path: "/x/", Auth: "public"}}, Options{Listen: ":8080"}); err == nil {
		t.Fatal("a route with no backend and no redirect must be refused")
	}
	if _, err := Caddyfile(nil, Options{}); err == nil {
		t.Fatal("an empty Listen must be refused: the rows do not carry it")
	}
}

// TestCaddyfile_RedirectNeedsNoBackend — a redirect row never reaches a proxy.
func TestCaddyfile_RedirectNeedsNoBackend(t *testing.T) {
	out := render(t, []store.ProxydRoute{
		{Path: "/old/", RedirectTo: "https://new.example", Auth: "public"},
	}, Options{})
	if !strings.Contains(out, "redir /old/* https://new.example 302") {
		t.Errorf("redirect not rendered:\n%s", out)
	}
	if strings.Contains(out, "reverse_proxy") {
		t.Errorf("a redirect must not proxy:\n%s", out)
	}
}
