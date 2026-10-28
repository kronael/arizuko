package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// A package install opens public URLs onto backends with no restart, and until
// BUGS.md F2 it left nothing behind. The route is the larger blast radius of the
// two row kinds an install writes, so the apply row has to name it — the path
// alone does not say what it exposes.
//
// The summary carries row IDENTITIES, not whole rows: audit.marshalParams caps
// params_summary at 512 bytes and a row-valued summary collapsed to
// {"_truncated":true}. A route's identity is its path; the backend and auth it
// fronts are one SELECT on proxyd_routes away, and the installed-package record
// names the same paths.
//
// Falsifiable: drop the added/removed key from ApplyOpts.Extra and the routes
// still land (TestPackagesInstallRoutesHotApply stays green) while every
// assertion here fails.

// installRoutePkg installs a two-route package as osUser and answers the
// instance store it landed in.
func installRoutePkg(t *testing.T, osUser string) *store.Store {
	t.Helper()
	seedSyncInstance(t, "raudit", nil, nil)
	t.Setenv("USER", osUser)
	st := instanceStore(t)
	h, err := store.Handle(store.OwnerOnbod)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOwner(context.Background(), h, store.OwnerOnbod); err != nil {
		t.Fatal(err)
	}
	if err := resreg.EnsureSubsystem(context.Background(), h, h, resreg.SubsystemOnbod); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "routepkg")
	mustWrite(t, filepath.Join(src, "routepkg.yml"), "services:\n  routepkg:\n    image: x\n")
	mustWrite(t, filepath.Join(src, "arizuko.yml"), "name: routepkg\n"+
		"proxyd_routes:\n"+
		"  - path: /rp/\n    backend: http://routepkg:8080\n    auth: public\n"+
		"  - path: /rp-admin/\n    backend: http://routepkg:8080\n    auth: auth\n")
	packageRuns(t, "raudit", "add", src)
	return st
}

// One row for the apply, naming the operator who installed the package and every
// route it opened.
func TestPackageRoutesAudited(t *testing.T) {
	s := installRoutePkg(t, "alice")

	rows := aclAuditRows(t, s, "config.apply")
	if len(rows) != 1 {
		t.Fatalf("config.apply rows = %d, want 1", len(rows))
	}
	if rows[0][0] != "cli:alice" || rows[0][1] != "cli" {
		t.Errorf("actor/surface = (%q, %q), want (cli:alice, cli)", rows[0][0], rows[0][1])
	}
	if !strings.Contains(rows[0][2], "/rp/") || !strings.Contains(rows[0][2], "/rp-admin/") {
		t.Errorf("params_summary does not name both routes: %q", rows[0][2])
	}
}

// The path alone does not say what the route exposes; the backend it fronts and
// the auth in front of it ARE the blast radius. They land in proxyd_routes
// itself, which is what a reader auditing "what did this install open" reads
// once the audit row has named the path.
func TestPackageRouteAuditNamesBackendAndAuth(t *testing.T) {
	s := installRoutePkg(t, "alice")

	all, err := s.AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]store.ProxydRoute{}
	for _, r := range all {
		byPath[r.Path] = r
	}
	for path, auth := range map[string]string{"/rp/": "public", "/rp-admin/": "auth"} {
		got := byPath[path]
		if got.Backend != "http://routepkg:8080" || got.Auth != auth {
			t.Errorf("route %s = %+v, want backend http://routepkg:8080 auth %s", path, got, auth)
		}
	}
}

// Withdrawal must record too. A trail that shows routes opening and never
// closing reads as "still live" for a route that is gone — worse than silence,
// because it looks complete.
