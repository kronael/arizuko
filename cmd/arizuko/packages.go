package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// pkgGrant is a package's grant asset (lowercase JSON), mapped to core.ACLRow.
type pkgGrant struct {
	Principal string `json:"principal"`
	Action    string `json:"action"`
	Scope     string `json:"scope"`
	Effect    string `json:"effect,omitempty"`
	Params    string `json:"params,omitempty"`
	Predicate string `json:"predicate,omitempty"`
}

func (g pkgGrant) row() core.ACLRow {
	e := g.Effect
	if e == "" {
		e = "allow" // match store.PutACLRow's default so RemoveACLRow matches
	}
	return core.ACLRow{Principal: g.Principal, Action: g.Action, Scope: g.Scope,
		Effect: e, Params: g.Params, Predicate: g.Predicate}
}

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

// isGitURL reports whether src is a remote git source rather than a local dir.
func isGitURL(src string) bool {
	return strings.HasPrefix(src, "git+") || strings.HasPrefix(src, "git@") ||
		strings.HasPrefix(src, "github.com/") || strings.Contains(src, "://")
}

// fetchSource resolves a package source to a local directory + a revision. A
// local path is used as-is (revision "local"); a git URL is shallow-cloned to a
// temp dir and its HEAD commit recorded. cleanup removes any temp clone (noop
// for a local path). Recursive removal is of arizuko's OWN temp dir only.
func fetchSource(src string) (dir, revision string, cleanup func()) {
	if !isGitURL(src) {
		return strings.TrimRight(src, "/"), "local", func() {}
	}
	url := strings.TrimPrefix(src, "git+")
	if strings.HasPrefix(url, "github.com/") {
		url = "https://" + url
	}
	tmp, err := os.MkdirTemp("", "arizuko-pkg-")
	if err != nil {
		die("Failed: temp dir: %v", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	if out, err := exec.Command("git", "clone", "--depth", "1", url, tmp).CombinedOutput(); err != nil {
		cleanup()
		die("Failed: git clone %s: %v\n%s", url, err, out)
	}
	rev, err := exec.Command("git", "-C", tmp, "rev-parse", "HEAD").Output()
	if err != nil {
		cleanup()
		die("Failed: git rev-parse %s: %v", url, err)
	}
	return tmp, strings.TrimSpace(string(rev)), cleanup
}

// packageName derives the package name from its source — the last path segment
// (git URL or local dir), minus a trailing `.git`.
func packageName(origin string) string {
	s := strings.TrimSuffix(strings.TrimRight(strings.TrimPrefix(origin, "git+"), "/"), ".git")
	return filepath.Base(s)
}

// readFragmentAssets returns the compose-fragment asset filenames in dir
// (`*.yml` + `<name>-routes.json`) and their content hashes.
func readFragmentAssets(dir string) (files []string, hashes map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		die("Failed: read source %s: %v", dir, err)
	}
	hashes = map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, "-routes.json")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		files = append(files, n)
		hashes["file:"+n] = sha256hex(b)
	}
	return files, hashes
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

// hasTable reports whether routd.db carries a table (present once the daemon has
// migrated the full schema — proxyd_routes / acl live in the store schema).
func hasTable(rdb *routd.DB, name string) bool {
	var n int
	return rdb.SQL().QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n) == nil
}

// applyPackageGrants hot-applies every grant in the package's `*-grants.json`
// files into routd.db's acl table (spec 5/28 acl asset kind). Grants are not
// compose-fragment assets (not copied to services/), so this scans the source
// dir directly. Returns each applied grant as JSON for the record, so remove
// can reverse it exactly.
func applyPackageGrants(rdb *routd.DB, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		die("Failed: read source %s: %v", dir, err)
	}
	st := store.New(rdb.SQL())
	var recorded []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, "-grants.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		var grants []pkgGrant
		if err := json.Unmarshal(b, &grants); err != nil {
			die("Failed: parse %s: %v", n, err)
		}
		for _, g := range grants {
			row := g.row()
			if err := st.PutACLRow(row); err != nil {
				die("Failed: apply grant %s/%s: %v", row.Principal, row.Action, err)
			}
			j, _ := json.Marshal(row)
			recorded = append(recorded, string(j))
		}
	}
	return recorded
}

// applyPackageRoutes hot-applies every route in the package's `*-routes.json`
// assets into routd.db's proxyd_routes table. proxyd reads that table live per
// request, so the route takes effect WITHOUT a restart — the 5/27 C2 fix
// (spec 5/28 P2). Returns the applied route paths for the record.
func applyPackageRoutes(rdb *routd.DB, dir string, files []string) []string {
	st := store.New(rdb.SQL())
	var paths []string
	for _, n := range files {
		if !strings.HasSuffix(n, "-routes.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		var routes []store.ProxydRoute
		if err := json.Unmarshal(b, &routes); err != nil {
			die("Failed: parse %s: %v", n, err)
		}
		for _, r := range routes {
			if err := st.PutProxydRoute(r); err != nil {
				die("Failed: apply route %s: %v", r.Path, err)
			}
			paths = append(paths, r.Path)
		}
	}
	return paths
}

// copyTree recursively copies a directory (skills can carry subdirs + scripts).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// applyPackageSkills installs a package's `skills/<name>/` dirs into the
// per-instance `<datadir>/skills/<name>/` (spec 5/28 skills asset kind), which
// seedSkills layers into every group. Returns the installed skill names for the
// record. Instance-wide by design (like stock skills); per-agent targeting is a
// future refinement.
func applyPackageSkills(dataDir, dir string) []string {
	src := filepath.Join(dir, "skills")
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil // no skills/ in this package
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || !pkgNameRE.MatchString(e.Name()) {
			continue
		}
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dataDir, "skills", e.Name())); err != nil {
			die("Failed: install skill %s: %v", e.Name(), err)
		}
		names = append(names, e.Name())
	}
	return names
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
		origin := args[2]
		name := packageName(origin)
		mustPkgName(name)
		dir, revision, cleanup := fetchSource(origin)
		defer cleanup()
		files, hashes := readFragmentAssets(dir)
		if len(files) == 0 {
			die("Failed: no compose fragment (*.yml) in source %s", origin)
		}
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			die("Failed: mkdir services: %v", err)
		}
		for _, n := range files {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				die("Failed: read %s: %v", n, err)
			}
			if err := writeFileAtomic(filepath.Join(svcDir, n), b); err != nil {
				die("Failed: install %s: %v", n, err)
			}
		}
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		manifest := map[string][]string{"compose_fragment": files}
		if hasTable(rdb, "proxyd_routes") {
			if paths := applyPackageRoutes(rdb, dir, files); len(paths) > 0 {
				manifest["proxyd_route"] = paths
			}
		}
		if hasTable(rdb, "acl") {
			if grants := applyPackageGrants(rdb, dir); len(grants) > 0 {
				manifest["grant"] = grants
			}
		}
		if skills := applyPackageSkills(dataDir, dir); len(skills) > 0 {
			manifest["skill"] = skills
		}
		if err := rdb.PutInstalledPackage(routd.InstalledPackage{
			Name:        name,
			Source:      origin,
			Revision:    revision,
			Manifest:    manifest,
			AssetHashes: hashes,
			InstalledAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			die("Failed: record install of %s: %v", name, err)
		}
		fmt.Printf("installed %s@%s (%d file(s), %d route(s)) — run `arizuko generate %s` to apply\n",
			name, revision, len(files), len(manifest["proxyd_route"]), args[0])
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
		dir, revision, cleanup := fetchSource(rec.Source)
		defer cleanup()
		newFiles, newHashes := readFragmentAssets(dir)
		if len(newFiles) == 0 {
			die("Failed: no compose fragment in source %s", rec.Source)
		}
		for _, n := range newFiles {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				die("Failed: read %s: %v", n, err)
			}
			if err := writeFileAtomic(filepath.Join(svcDir, n), b); err != nil {
				die("Failed: upgrade %s: %v", n, err)
			}
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
		rec.Revision = revision
		rec.Manifest = map[string][]string{"compose_fragment": newFiles}
		rec.AssetHashes = newHashes
		rec.InstalledAt = time.Now().UTC().Format(time.RFC3339)
		if err := rdb.PutInstalledPackage(rec); err != nil {
			die("Failed: record upgrade: %v", err)
		}
		fmt.Printf("upgraded %s@%s (%d file(s)) — run `arizuko generate %s` to apply\n", name, revision, len(newFiles), args[0])
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
			// P4 reverse order: withdraw the live route(s) FIRST, so no request
			// is ever routed at a sidecar mid-teardown, THEN drop the fragment
			// files. Bring-up health-gating is compose's (healthcheck +
			// depends_on) — the CLI can't reach docker-internal backends.
			st := store.New(rdb.SQL())
			if paths := rec.Manifest["proxyd_route"]; len(paths) > 0 && hasTable(rdb, "proxyd_routes") {
				for _, p := range paths {
					if _, err := st.DeleteProxydRoute(p); err != nil {
						die("Failed: remove route %s: %v", p, err)
					}
				}
			}
			if grants := rec.Manifest["grant"]; len(grants) > 0 && hasTable(rdb, "acl") {
				for _, gj := range grants {
					var row core.ACLRow
					if err := json.Unmarshal([]byte(gj), &row); err != nil {
						die("Failed: decode recorded grant: %v", err)
					}
					if err := st.RemoveACLRow(row); err != nil {
						die("Failed: remove grant %s/%s: %v", row.Principal, row.Action, err)
					}
				}
			}
			for _, sk := range rec.Manifest["skill"] {
				_ = os.RemoveAll(filepath.Join(dataDir, "skills", sk))
			}
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
