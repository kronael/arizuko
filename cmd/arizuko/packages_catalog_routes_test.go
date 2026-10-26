package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestPackagesAddBundledGitdRoutes(t *testing.T) {
	pkgChildRun()
	catalog := map[string]string{}
	for _, name := range []string{"gitd.yml", "arizuko.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "template", "services", "gitd", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "arizuko.yml" {
			catalog["gitd/arizuko.yml"] = string(data)
		} else {
			catalog[name] = string(data)
		}
	}
	dataDir := seedSyncInstance(t, "gitd-catalog", catalog, map[string]string{})
	seedRoutdDB(t, dataDir)
	if code := runPkgExpectingDeath(t, []string{"gitd-catalog", "add", "gitd"}); code != 0 {
		t.Fatalf("stock catalog gitd add failed: exit %d", code)
	}
	rec := mustRec(t, dataDir, "gitd")
	if rec.Source != catalogOrigin || len(rec.Manifest["proxyd_routes"]) != 2 {
		t.Fatalf("installed receiver record: %+v", rec)
	}
	db, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	routes, err := store.NewOver(db.Obj()).AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, route := range routes {
		if route.Path == "/github/" || route.Path == "/dash/gitd/" {
			if route.Owner != store.RouteOwnerPackage("gitd") || route.Backend != "http://gitd:8080" {
				t.Fatalf("receiver route provenance changed: %+v", route)
			}
			if route.Path == "/github/" && !slices.Equal(route.PreserveHeaders, []string{"X-Hub-Signature-256", "X-GitHub-Event", "X-GitHub-Delivery"}) {
				t.Fatalf("receiver verification headers changed: %v", route.PreserveHeaders)
			}
			found[route.Path] = route.Auth
		}
	}
	if found["/github/"] != "public" || found["/dash/gitd/"] != "user" {
		t.Fatalf("stock route auth changed: %v", found)
	}
}

func TestPackagesAddCatalogRoutesRemainBounded(t *testing.T) {
	pkgChildRun()
	for name, manifest := range map[string]string{
		"other-console":  "proxyd_routes:\n  - path: /dash/routd/\n    backend: http://gitd:8080\n    auth: user\n",
		"core-prefix":    "proxyd_routes:\n  - path: /dash/\n    backend: http://gitd:8080\n    auth: user\n",
		"core-endpoint":  "proxyd_routes:\n  - path: /mcp\n    backend: http://gitd:8080\n    auth: public\n",
		"core-owner":     "proxyd_routes:\n  - path: /dash/gitd/\n    backend: http://gitd:8080\n    auth: user\n    owner: arizuko\n",
		"operator-owner": "proxyd_routes:\n  - path: /dash/gitd/\n    backend: http://gitd:8080\n    auth: user\n    owner: operator\n",
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := seedSyncInstance(t, "catalog-guard", map[string]string{"gitd.yml": "services:\n  gitd:\n    image: x\n", "gitd/arizuko.yml": "name: gitd\n" + manifest}, map[string]string{})
			seedRoutdDB(t, dataDir)
			if code := runPkgExpectingDeath(t, []string{"catalog-guard", "add", "gitd"}); code == 0 {
				t.Fatalf("catalog claim %s installed", name)
			}
			assertNothingInstalled(t, dataDir, "gitd")
		})
	}
}

func TestPackagesInstallCatalogNamedSourceRoutesDenied(t *testing.T) {
	pkgChildRun()
	for _, name := range []string{"gitd", "catalog"} {
		t.Run(name, func(t *testing.T) {
			manifest := "proxyd_routes:\n  - path: /dash/" + name + "/\n    backend: http://" + name + ":8080\n    auth: user\n"
			dataDir := seedSyncInstance(t, "source-route", map[string]string{name + ".yml": "services:\n  " + name + ":\n    image: x\n", name + "/arizuko.yml": "name: " + name + "\n" + manifest}, map[string]string{})
			seedRoutdDB(t, dataDir)
			source := writePkg(t, filepath.Dir(dataDir), name, manifest)
			if name == "catalog" {
				t.Chdir(filepath.Dir(dataDir))
				source = name
			}
			if code := runPkgExpectingDeath(t, []string{"source-route", "install", source}); code == 0 {
				t.Fatal("sourced package acquired catalog console exception")
			}
			assertNothingInstalled(t, dataDir, name)
		})
	}
}
