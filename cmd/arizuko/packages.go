package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

const (
	manifestFiles  = "compose_fragment"
	manifestSkills = "skill"
)

// pkgNameRE matches a package name — the same identifier shape the compose
// generator validates for discovered fragment filenames. It forbids `.`/`/`
// leads and every path separator, so a name can never traverse outside
// services/ when joined into a path (`remove ../docker-compose` is rejected).
var pkgNameRE = regexp.MustCompile(container.PackageNamePattern)

// baseDaemons are the always-present compose services a fragment may depend on
// without shipping them as a package. A depends_on outside this set that is not
// itself an enabled package is an unsatisfied dependency (warned on add).
var baseDaemons = map[string]bool{
	"authd": true, "routd": true, "runed": true, "proxyd": true, "webd": true,
	"vited": true, "timed": true, "onbod": true, "davd": true, "dashd": true,
}

// mustPkgName rejects a package name that could escape services/.
func mustPkgName(name string) {
	if !pkgNameRE.MatchString(name) {
		die("Failed: invalid package name %q (allowed chars: [A-Za-z0-9_.-], no leading '.', max 63)", name)
	}
}

// packageTemplates returns the bundled catalog dir this process can read.
func packageTemplates(dataDir string) string {
	hostAppDir := ""
	if cfg, err := core.LoadConfigFrom(dataDir); err == nil {
		hostAppDir = cfg.HostAppDir
	}
	return compose.CatalogDir(hostAppDir)
}

// provisionPkgEnv resolves what a package needs from .env through the same
// function generate runs, so a refusal (`required` unset) lands here, before
// the first file is written, and a minted key exists before the first
// generate. Reports by key name only.
func provisionPkgEnv(dataDir, manifestPath, name string) {
	env, err := loadInstanceEnv(dataDir)
	if os.IsNotExist(err) {
		env = map[string]string{}
	} else if err != nil {
		die("Failed: read .env: %v", err)
	}
	generated, err := compose.ProvisionPackageEnv(dataDir, manifestPath, name, env)
	if err != nil {
		die("Failed: %v", err)
	}
	for _, key := range generated {
		fmt.Printf("generated %s for %s into .env\n", key, name)
	}
}

func reportPackageInstall(name, revision string, files []string, applied map[string][]string) {
	fmt.Printf("installed %s@%s (%d file(s))\n", name, revision, len(files))
	for _, res := range slices.Sorted(maps.Keys(applied)) {
		fmt.Printf("  %s: %d row(s), applied now\n", res, len(applied[res]))
	}
}

// isGitURL reports whether src is a remote git source rather than a local dir.
func isGitURL(src string) bool {
	return strings.HasPrefix(src, "git+") || strings.HasPrefix(src, "git@") ||
		strings.HasPrefix(src, "github.com/") || strings.Contains(src, "://")
}

// fetchSource resolves a package source to a local directory + a revision. A
// local path is made absolute (revision "local"); a git URL is shallow-cloned to a
// temp dir and its HEAD commit recorded. cleanup removes any temp clone (noop
// for a local path). Recursive removal is of arizuko's OWN temp dir only.
func fetchSource(src string) (dir, revision string, cleanup func()) {
	if !isGitURL(src) {
		dir, err := filepath.Abs(src)
		if err != nil {
			die("Failed: resolve source %s: %v", src, err)
		}
		return dir, "local", func() {}
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

// checkSourcedFiles bounds what a package from a source may put into
// services/: no file that would replace one the instance already has (the
// catalog's link, an operator's own fragment), no name of a core service, and
// external fragments stay within compose.CheckSourcedFragment. owned names the
// files this package's record already holds, which an upgrade may rewrite.
func checkSourcedFiles(svcDir, name, srcDir string, files []string, owned map[string]string, bundled, force bool) error {
	if baseDaemons[name] {
		return fmt.Errorf("%s is a core service; a package from a source may not take its name", name)
	}
	for _, n := range files {
		if _, mine := owned["file:"+n]; !mine && !force {
			if _, err := os.Lstat(filepath.Join(svcDir, n)); err == nil {
				return fmt.Errorf("services/%s is already there and not this package's — "+
					"withdraw the package that owns it first", n)
			}
		}
		if bundled || !strings.HasSuffix(n, ".yml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(srcDir, n))
		if err != nil {
			return err
		}
		if err := compose.CheckSourcedFragment(name, n, b); err != nil {
			return err
		}
	}
	return nil
}

func readFragmentAssets(dir, name string) (files []string, hashes map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		die("Failed: read source %s: %v", dir, err)
	}
	hashes = map[string]string{}
	declaration, err := os.ReadFile(filepath.Join(dir, container.ManifestFile))
	if err != nil {
		die("Failed: read package manifest: %v", err)
	}
	hashes["file:"+container.ManifestFile] = sha256hex(declaration)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n != name+".yml" {
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
// edited (dirty) asset (see compose/README.md).
func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// mustOpenRoutd dials routd's tables for the installed-package record. The CLI
// writes the owner's tables directly per the split write-discipline (CLAUDE.md).
func mustOpenRoutd() *routd.DB {
	rdb, err := routd.Open()
	if err != nil {
		die("Failed: open routd's store: %v", err)
	}
	return rdb
}

// describeGrant is the operator-facing line for one acl row: WHO may do WHAT,
// WHERE, and under which narrowing. A bare count hides the fact the install
// handed somebody admin on a folder — the same reason the audit row names every
// row it wrote (BUGS Q5/L36).
//
// IT NAMES EVERY FIELD OF THE COORDINATE. store.aclKey joins principal, action,
// scope, params, predicate and effect, so a line that prints fewer of them
// shows two different rows as one and leaves an operator unable to type the
// `ungrant` that removes either (BUGS L138).
//
// It renders the DOMAIN row, and the package installer converts its manifest
// row into one. Two renderers drift, and this one already has two sinks: the
// install summary and `arizuko group grant`/`ungrant`.
func describeGrant(row core.ACLRow) string {
	effect := row.Effect
	if effect == "" {
		effect = store.EffectAllow
	}
	s := fmt.Sprintf("%s: %s %s on %s", effect, row.Principal, row.Action, row.Scope)
	if row.Params != "" {
		s += " (" + row.Params + ")"
	}
	if row.Predicate != "" {
		s += " when " + row.Predicate
	}
	if row.GrantOption {
		s += " WITH GRANT OPTION"
	}
	return s
}

// pkgFragment is one subsystem's worth of a package's declared rows: resource
// name → `[]RowType` slice, exactly the shape resreg.Apply consumes.
type pkgFragment struct {
	subsystem string
	rows      map[string]any
}

// parsePkgManifests returns one fragment per document of a package's
// declaration. It writes nothing: an unknown resource key, an acl row or a
// resource `apply` never rebuilds all fail here, before the install touches the
// instance.
//
// proxyd_routes rows are stamped `owner = package:<name>`, so the row names
// where it came from and `rm` can find it. This is the one place a package's
// rows enter the instance, so it is also where a package is held to what the
// operator handed it (BUGS L46).
func parsePkgManifests(name string, declaration container.ProductManifest, bundled string) []pkgFragment {
	var out []pkgFragment
	for _, doc := range declaration.Documents {
		for nameKey, rows := range doc.Rows {
			r := resreg.Lookup(nameKey)
			if r.SkipApplyRebuild || !pkgSubsystemServed(r.DB) {
				die("Failed: %s declares %s, which apply cannot write", container.ManifestFile, nameKey)
			}
			switch nameKey {
			case resources.ACLName:
				die("Failed: %s declares acl rows; a package never grants — run arizuko group grant", container.ManifestFile)
			case resources.ProxydRoutesName:
				doc.Rows[nameKey] = stampPkgRoutes(name, container.ManifestFile, rows, bundled)
			}
		}
		if len(doc.Rows) > 0 {
			out = append(out, pkgFragment{subsystem: doc.Subsystem, rows: doc.Rows})
		}
	}
	return out
}

// stampPkgRoutes sets owner on every proxyd route a package declares, refuses the
// ones that claim core arizuko's own surfaces, and refuses a package that names
// its own owner (BUGS L46). The owner is stamped, never read from the file:
// `arizuko` would let the row pass as core-shipped, and `operator` would opt it
// out of every later generate.
func stampPkgRoutes(name, file string, rows any, bundled string) any {
	routes, ok := rows.([]resources.ProxydRoutesRow)
	if !ok {
		die("Failed: %s: proxyd_routes rows have unexpected type %T", file, rows)
	}
	owner := store.RouteOwnerPackage(name)
	for i := range routes {
		if routes[i].Owner != "" {
			die("Failed: %s: route %q declares owner %q — a package never names its own owner; "+
				"install stamps %q (BUGS L46)", file, routes[i].Path, routes[i].Owner, owner)
		}
		if claimed := compose.ReservedRouteClaim(bundled, routes[i].Path); claimed != "" {
			die("Failed: %s: route %q claims the core surface %q — a package may not serve "+
				"core arizuko's own paths (BUGS L46)", file, routes[i].Path, claimed)
		}
		routes[i].Owner = owner
	}
	return routes
}

// pkgSubsystemServed reports whether a package's rows can reach this owner DB.
// It is the two stores pkgSubsystemStores opens; every other registered
// resource (audit, dead_letters, sessions, signing_keys) is a read-only
// projection over other tables that `apply` never writes. Spelled once so the
// parser's refusal and the opener's backstop cannot disagree about the set.
func pkgSubsystemServed(sub string) bool {
	return sub == resreg.SubsystemRoutd || sub == resreg.SubsystemOnbod
}

// dropFiles deletes the named files from dir — the install's own unwind, by
// explicit name, never a recursive removal of a directory.

// dirtyAssets returns the record's file assets whose current on-disk content no
// longer matches the install hash — the locally edited ones an upgrade must
// refuse to overwrite (see compose/README.md). A missing file is not
// dirty (upgrade recreates it). Result is sorted for a stable message.
func dirtyAssets(svcDir string, rec routd.InstalledPackage) []string {
	var dirty []string
	for k, recorded := range rec.AssetHashes {
		fn, isFile := strings.CutPrefix(k, "file:")
		if !isFile {
			continue
		}
		if fn == container.ManifestFile {
			continue
		}
		path := filepath.Join(svcDir, fn)
		cur, err := os.ReadFile(path)
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

// relinkCatalog converts every installed fragment that is still a byte-identical
// copy of the bundled catalog into a symlink, so a catalog fix reaches this
// instance without a copy falling behind (BUGS R1). Runs on every generate — it
// never touches a hand-edited or multi-account-variant fragment. A no-op when
// HOST_APP_DIR is unset: there is then no host-resolvable path to link to.
func relinkCatalog(dataDir string) {
	hostDir, ok := compose.HostCatalogDir(dataDir)
	if !ok {
		return
	}
	svcDir := filepath.Join(dataDir, "services")
	relinked, diverged, err := compose.RelinkCatalog(svcDir, packageTemplates(dataDir), hostDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "services: relink to catalog: %v\n", err)
		return
	}
	for _, name := range relinked {
		fmt.Printf("services/%s.yml now links to the bundled catalog (was an identical copy)\n", name)
	}
	for _, d := range diverged {
		fmt.Fprintf(os.Stderr, "services: services/%s.yml differs from the catalog and is FROZEN — "+
			"it will never pick up a mount or env var the template gains; compare it against "+
			"%s/%s.yml (BUGS L41)\n", d.Name, hostDir, d.Name)
		if len(d.Missing) == 0 {
			fmt.Fprintf(os.Stderr, "  it carries every line the catalog has, so nothing is withheld today\n")
			continue
		}
		fmt.Fprintf(os.Stderr, "  %d catalog line(s) absent from it:\n", len(d.Missing))
		for _, line := range d.Missing {
			fmt.Fprintf(os.Stderr, "    %s\n", line)
		}
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
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

var dependsOnRE = regexp.MustCompile(`(?m)^\s*depends_on:\s*\[([^\]]*)\]`)

// warnUnsatisfiedDeps warns when the fragment depends_on a service that is
// neither an always-present base daemon nor an enabled package (e.g. `ttsd`
// depends on `kokoro`, a separate package). A missing dependency makes the
// whole Compose model invalid at up time.
