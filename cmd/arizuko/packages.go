package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kronael/arizuko/core"
)

// pkgNameRE matches a package name — the same identifier shape the compose
// generator validates for discovered fragment filenames. It forbids `.`/`/`
// leads and every path separator, so a name can never traverse outside
// services/ when joined into a path (`remove ../docker-compose` is rejected).
var pkgNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`)

// baseDaemons are the always-present compose services a fragment may depend on
// without shipping them as a package. A depends_on outside this set that is not
// itself an enabled package is an unsatisfied dependency (warned on add).
var baseDaemons = map[string]bool{
	"authd": true, "routd": true, "runed": true, "proxyd": true, "webd": true,
	"vited": true, "timed": true, "onbod": true, "davd": true, "dashd": true,
	"crackbox": true,
}

// mustPkgName rejects a package name that could escape services/.
func mustPkgName(name string) {
	if !pkgNameRE.MatchString(name) {
		die("Failed: invalid package name %q (allowed chars: [A-Za-z0-9_.-], no leading '.', max 63)", name)
	}
}

// packageTemplates returns the bundled catalog dir: the repo's
// template/services when HOST_APP_DIR points at a checkout, else the copy
// baked into arizuko:latest by the Dockerfile.
func packageTemplates(dataDir string) string {
	cfg, err := core.LoadConfigFrom(dataDir)
	if err == nil && cfg.HostAppDir != "" {
		if dir := filepath.Join(cfg.HostAppDir, "template", "services"); dirExists(dir) {
			return dir
		}
	}
	return "/opt/arizuko/template/services"
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// listFragments returns the `<name>.yml` service fragments in dir.
func listFragments(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yml"))
		}
	}
	return names
}

// cmdPackages manages which adapter packages an instance runs: each is a
// compose fragment (plus optional `<name>-routes.json`) copied from the
// bundled catalog into <dataDir>/services/. `arizuko generate` includes every
// fragment it finds there.
func cmdPackages(args []string) {
	usage := "arizuko packages <instance> list | add <name> | remove <name>"
	need(args, 2, usage)
	dataDir := mustInstanceDir(args[0])
	tmplDir := packageTemplates(dataDir)
	svcDir := filepath.Join(dataDir, "services")

	switch args[1] {
	case "list":
		enabled := map[string]bool{}
		for _, n := range listFragments(svcDir) {
			enabled[n] = true
		}
		for _, n := range listFragments(tmplDir) {
			state := "available"
			if enabled[n] {
				state = "enabled"
			}
			fmt.Printf("%-12s %s\n", n, state)
			delete(enabled, n)
		}
		for n := range enabled {
			fmt.Printf("%-12s enabled (local)\n", n)
		}
	case "add":
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		// Read every source before writing any destination: a package installs
		// whole or not at all (no half-written fragment on a routes read error).
		fragment, err := os.ReadFile(filepath.Join(tmplDir, name+".yml"))
		if err != nil {
			die("Failed: add %s: %v (catalog: %s)", name, err, tmplDir)
		}
		// A package's proxyd routes travel with it — proxyd's route table is
		// assembled from these at generate time. Optional.
		routes, rErr := os.ReadFile(filepath.Join(tmplDir, name+"-routes.json"))
		if rErr != nil && !os.IsNotExist(rErr) {
			die("Failed: add %s routes: %v", name, rErr)
		}
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			die("Failed: mkdir services: %v", err)
		}
		if err := writeFileAtomic(filepath.Join(svcDir, name+".yml"), fragment); err != nil {
			die("Failed: add %s: %v", name, err)
		}
		if rErr == nil {
			if err := writeFileAtomic(filepath.Join(svcDir, name+"-routes.json"), routes); err != nil {
				die("Failed: add %s routes: %v", name, err)
			}
		}
		warnUnsatisfiedDeps(name, fragment, svcDir)
		fmt.Printf("added %s — run `arizuko generate %s` to apply\n", name, args[0])
	case "remove":
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		if err := os.Remove(filepath.Join(svcDir, name+".yml")); err != nil {
			die("Failed: remove %s: %v", name, err)
		}
		if err := os.Remove(filepath.Join(svcDir, name+"-routes.json")); err != nil && !os.IsNotExist(err) {
			die("Failed: remove %s routes: %v", name, err)
		}
		fmt.Printf("removed %s — run `arizuko generate %s` to apply\n", name, args[0])
	default:
		die("usage: " + usage)
	}
}

// writeFileAtomic writes b to path via a temp file + rename, so a failure mid
// operation never leaves a truncated fragment behind.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pkg.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

var dependsOnRE = regexp.MustCompile(`(?m)^\s*depends_on:\s*\[([^\]]*)\]`)

// warnUnsatisfiedDeps warns when the fragment depends_on a service that is
// neither an always-present base daemon nor an enabled package (e.g. `ttsd`
// depends on `kokoro`, a separate package). A missing dependency makes the
// whole Compose model invalid at up time.
func warnUnsatisfiedDeps(name string, fragment []byte, svcDir string) {
	m := dependsOnRE.FindSubmatch(fragment)
	if m == nil {
		return
	}
	for _, dep := range strings.Split(string(m[1]), ",") {
		dep = strings.Trim(strings.TrimSpace(dep), `'"`)
		if dep == "" || dep == name || baseDaemons[dep] {
			continue
		}
		if _, err := os.Stat(filepath.Join(svcDir, dep+".yml")); err != nil {
			fmt.Printf("warning: %s depends on %q — add it too: `arizuko packages <instance> add %s`\n", name, dep, dep)
		}
	}
}
