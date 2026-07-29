package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
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

// sha256hex returns the hex sha256 of b — the per-asset content hash the
// installed-package record stores so a later upgrade can detect a locally
// edited (dirty) asset (spec 5/28, P3).
func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// mustOpenRoutd opens the instance's routd.db (migrating it if needed) for the
// installed-package record. The CLI writes the owner DB directly per the split
// write-discipline (CLAUDE.md).
func mustOpenRoutd(dataDir string) *routd.DB {
	rdb, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open routd.db: %v", err)
	}
	return rdb
}

// dirtyAssets returns the record's file assets whose current on-disk content no
// longer matches the install hash — the locally edited ones an upgrade must
// refuse to overwrite (spec 5/28: no blind overwrite). A missing file is not
// dirty (upgrade recreates it). Result is sorted for a stable message.
func dirtyAssets(svcDir string, rec routd.InstalledPackage) []string {
	var dirty []string
	for k, recorded := range rec.AssetHashes {
		fn, isFile := strings.CutPrefix(k, "file:")
		if !isFile {
			continue
		}
		cur, err := os.ReadFile(filepath.Join(svcDir, fn))
		if err != nil {
			continue
		}
		if sha256hex(cur) != recorded {
			dirty = append(dirty, fn)
		}
	}
	sort.Strings(dirty)
	return dirty
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
	usage := "arizuko packages <instance> list | add <name> | install <source-dir> | upgrade <name> | remove <name>"
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
	case "install":
		// P1 (spec 5/28): install a package from a source directory, recording
		// exactly what was installed (the installed-package record). This slice
		// handles the compose-fragment asset kind (`*.yml` + `<name>-routes.json`)
		// and writes the record; git source resolution, skills, and row assets
		// (proxyd_routes / acl via REST) are P1b/P2.
		need(args, 3, usage)
		src := strings.TrimRight(args[2], "/")
		name := filepath.Base(src)
		mustPkgName(name)
		entries, err := os.ReadDir(src)
		if err != nil {
			die("Failed: read source %s: %v", src, err)
		}
		var fragments []string
		hashes := map[string]string{}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !(strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, "-routes.json")) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(src, n))
			if err != nil {
				die("Failed: read %s: %v", n, err)
			}
			hashes["file:"+n] = sha256hex(b)
			// Stage all reads before any write (whole-or-nothing); collect here,
			// write below only once every asset read cleanly.
			fragments = append(fragments, n)
		}
		if len(fragments) == 0 {
			die("Failed: no compose fragment (*.yml) in source %s", src)
		}
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			die("Failed: mkdir services: %v", err)
		}
		for _, n := range fragments {
			b, _ := os.ReadFile(filepath.Join(src, n))
			if err := writeFileAtomic(filepath.Join(svcDir, n), b); err != nil {
				die("Failed: install %s: %v", n, err)
			}
		}
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		if err := rdb.PutInstalledPackage(routd.InstalledPackage{
			Name:        name,
			Source:      src,
			Revision:    "local",
			Manifest:    map[string][]string{"compose_fragment": fragments},
			AssetHashes: hashes,
			InstalledAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			die("Failed: record install of %s: %v", name, err)
		}
		fmt.Printf("installed %s (%d file(s)) — run `arizuko generate %s` to apply\n", name, len(fragments), args[0])
	case "upgrade":
		// P3 (spec 5/28): re-install a package's assets from its recorded source,
		// but REFUSE to overwrite a locally edited (dirty) asset — emit which and
		// stop, rather than clobber the operator's change.
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		rec, ok, err := rdb.InstalledPackage(name)
		if err != nil {
			die("Failed: read record: %v", err)
		}
		if !ok {
			die("Failed: %s is not installed", name)
		}
		if dirty := dirtyAssets(svcDir, rec); len(dirty) > 0 {
			die("Failed: %s has locally edited asset(s): %s\n"+
				"  upgrade refuses to overwrite them (spec 5/28). Resolve first — take-theirs:\n"+
				"  delete the file(s) then upgrade; keep-mine: file the diff upstream + fork.",
				name, strings.Join(dirty, ", "))
		}
		entries, err := os.ReadDir(rec.Source)
		if err != nil {
			die("Failed: read source %s: %v", rec.Source, err)
		}
		newHashes := map[string]string{}
		var newFiles []string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !(strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, "-routes.json")) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(rec.Source, n))
			if err != nil {
				die("Failed: read %s: %v", n, err)
			}
			if err := writeFileAtomic(filepath.Join(svcDir, n), b); err != nil {
				die("Failed: upgrade %s: %v", n, err)
			}
			newHashes["file:"+n] = sha256hex(b)
			newFiles = append(newFiles, n)
		}
		// Delete assets the old record owned that the new source dropped.
		for k := range rec.AssetHashes {
			if _, present := newHashes[k]; present {
				continue
			}
			if fn, isFile := strings.CutPrefix(k, "file:"); isFile {
				_ = os.Remove(filepath.Join(svcDir, fn))
			}
		}
		rec.Manifest = map[string][]string{"compose_fragment": newFiles}
		rec.AssetHashes = newHashes
		rec.InstalledAt = time.Now().UTC().Format(time.RFC3339)
		if err := rdb.PutInstalledPackage(rec); err != nil {
			die("Failed: record upgrade: %v", err)
		}
		fmt.Printf("upgraded %s (%d file(s)) — run `arizuko generate %s` to apply\n", name, len(newFiles), args[0])
	case "remove":
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		// Prefer the installed record (spec 5/28): delete exactly what the record
		// says this package owns, then drop the record. Fall back to the legacy
		// `<name>.yml` deletion for a catalog `add` that left no record.
		if rec, ok, _ := rdb.InstalledPackage(name); ok {
			for k := range rec.AssetHashes {
				if fn, isFile := strings.CutPrefix(k, "file:"); isFile {
					if err := os.Remove(filepath.Join(svcDir, fn)); err != nil && !os.IsNotExist(err) {
						die("Failed: remove %s: %v", fn, err)
					}
				}
			}
			if _, err := rdb.DeleteInstalledPackage(name); err != nil {
				die("Failed: drop record for %s: %v", name, err)
			}
			fmt.Printf("removed %s — run `arizuko generate %s` to apply\n", name, args[0])
			return
		}
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
