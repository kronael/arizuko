package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/proxycfg"
	"github.com/kronael/arizuko/store"
)

// syncProxydRoutes writes the route table this generate run decided on into
// routd.db. It is the delivery half of `arizuko generate`: proxyd_routes is the
// only place a route persists, and this is the only writer that carries a
// release's routes to an existing instance.
//
// Before this, compose emitted the same list as `PROXYD_ROUTES_JSON` and proxyd
// seeded it ONCE — once the table had a row the env var was ignored forever, so
// seven routes arizuko shipped (/invite/, /pair/, /me, /me/, /panel/,
// /dash/onbod/, /dash/timed/) never reached krons and the instance looked
// healthy the whole time (BUGS L45).
//
// Fatal on failure. A generate that renders the compose file but silently skips
// the route table is exactly the failure this replaces.
func syncProxydRoutes(dataDir string, routes []compose.ProxydRoute) {
	rdb := mustOpenRoutd()
	defer func() { _ = rdb.Close() }()
	// AsCLI attributes the write to the invoking operator: generate has no
	// X-User-* header to learn a sub from, same as grant/secret.
	st := store.NewOver(rdb.Obj()).AsCLI(os.Getenv("USER"))

	rows := make([]store.ProxydRoute, 0, len(routes))
	for _, r := range routes {
		rows = append(rows, store.ProxydRoute{
			Path:            r.Path,
			Backend:         r.Backend,
			Auth:            r.Auth,
			GatedBy:         r.GatedBy,
			PreserveHeaders: r.PreserveHeaders,
			StripPrefix:     r.StripPrefix,
			RedirectTo:      r.RedirectTo,
			Owner:           r.Owner,
		})
	}
	written, kept, err := st.SyncProxydRoutes(rows)
	if err != nil {
		die("Failed: write proxyd routes: %v", err)
	}
	// routd.Open creates -wal/-shm beside routd.db, and generate usually runs as
	// root while the daemons run as uid 1000 — leaving them root-owned makes
	// every daemon write fail with SQLITE_CANTOPEN.
	chownStore(filepath.Join(dataDir, "store"))

	fmt.Printf("proxyd routes: %d written, %d operator-owned left alone\n", written, kept)
}

// cmdProxyConfig renders the instance's proxyd_routes rows as a Caddyfile
// (spec 6/22 step 2). It PRINTS rather than installs: proxyd is the running
// proxy, so which proxy fronts an instance is the operator's decision and a
// renderer that wrote into a live proxy's config dir would take it for them.
//
// --forward-auth names the endpoint that answers with the transit proof
// (proxyd's /auth/forward). Unset, every authenticated route renders as a 503
// with the reason — see proxycfg.
func cmdProxyConfig(args []string) {
	need(args, 1, "arizuko proxy-config [-i instance] [--listen :8080] [--forward-auth host:port]")
	mustInstanceDir(args[0])
	opts := proxycfg.Options{Listen: ":8080"}
	for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
		if i+1 >= len(args) {
			die("Failed: %s needs a value", args[i])
		}
		switch args[i] {
		case "--listen":
			opts.Listen = args[i+1]
		case "--forward-auth":
			opts.ForwardAuth = args[i+1]
		default:
			die("Failed: unknown flag %s", args[i])
		}
	}
	rdb := mustOpenRoutd()
	defer func() { _ = rdb.Close() }()
	rows, err := store.NewOver(rdb.Obj()).AllProxydRoutes()
	if err != nil {
		die("Failed: read proxyd_routes: %v", err)
	}
	out, err := proxycfg.Caddyfile(rows, opts)
	if err != nil {
		die("Failed: %v", err)
	}
	fmt.Print(out)
}
