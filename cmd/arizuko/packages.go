package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
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

// parseInstallFlags splits `install`'s args into the source and the grant bound
// the operator handed the package: `--folder <scope>`, repeatable. A package's
// authority is what the OPERATOR named, never what the package asks for (BUGS
// L46). No `--folder` is not "unbounded" — it is a bound of nothing.
func parseInstallFlags(args []string, usage string) (origin string, bound []string) {
	var rest []string
	for i := 0; i < len(args); i++ {
		name, val, inline := strings.Cut(args[i], "=")
		switch name {
		case "--folder", "-folder":
			if !inline {
				i++
				if i >= len(args) {
					die("Failed: --folder needs a scope\n%s", usage)
				}
				val = args[i]
			}
			if val == "" {
				die("Failed: --folder needs a scope\n%s", usage)
			}
			bound = append(bound, val)
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 1 {
		die("Failed: install takes exactly one source\n%s", usage)
	}
	return rest[0], bound
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

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
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

// catalogOrigin is the Source and Revision an installed-package record carries
// for a package taken from the bundled catalog: its files are links the catalog
// refreshes by itself, so upgrade and sync have nothing to re-fetch.
const catalogOrigin = "catalog"

// installPackage is the one install path. It validates the whole manifest
// before a byte is written — a package installs whole or not at all, and every
// refusal (unknown resource, bad row field, inert grant predicate, a grant wider
// than --folder, a route on core ground, an unset required env) belongs on the
// near side of the first write. Then it places the files (linked at hostDir
// when given, else copied from srcDir), applies the rows through `apply`'s own
// engine, and records what was installed.
// bundled comes only from add after reading the release catalog.
func installPackage(dataDir, inst, name, srcDir string, files []string, hashes map[string]string,
	bound []string, origin, revision, hostDir, bundled string) {
	frags := parsePkgManifests(name, srcDir, files, bound, bundled)
	svcDir := filepath.Join(dataDir, "services")
	if origin != catalogOrigin {
		if err := checkSourcedFiles(svcDir, name, srcDir, files, nil); err != nil {
			die("Failed: install %s: %v", name, err)
		}
	}
	provisionPkgEnv(dataDir, filepath.Join(srcDir, name+".yaml"), name)
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		die("Failed: mkdir services: %v", err)
	}
	for _, n := range files {
		if hostDir != "" {
			if err := compose.SymlinkFragment(svcDir, hostDir, n); err != nil {
				die("Failed: install %s: %v", n, err)
			}
			continue
		}
		b, err := os.ReadFile(filepath.Join(srcDir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		if err := writeFileAtomic(filepath.Join(svcDir, n), b); err != nil {
			die("Failed: install %s: %v", n, err)
		}
	}
	rdb := mustOpenRoutd()
	defer func() { _ = rdb.Close() }()
	manifest := map[string][]string{manifestFiles: files}
	applied := map[string][]string{}
	if len(frags) > 0 {
		rows, aerr := installPkgRows(dataDir, rdb, name, frags, pkgManifestDigest(srcDir, files))
		if aerr != nil {
			// The rows are the half that is live the moment it lands, and the
			// apply left none of them. Take the inert half back down too, so a
			// refused install is not half-present at the next `generate`.
			dropFiles(svcDir, files)
			die("Failed: install %s: %v", name, aerr)
		}
		maps.Copy(manifest, rows)
		applied = rows
	}
	grants := installedGrants(frags)
	// Folder is InstanceWide: install writes compose fragments, proxyd routes
	// and host files, none of which belong to a group (spec 5/28-packages).
	if err := rdb.PutInstalledPackage(routd.InstalledPackage{
		Folder:      routd.InstanceWide,
		Name:        name,
		Source:      origin,
		Revision:    revision,
		Manifest:    manifest,
		AssetHashes: hashes,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		die("Failed: record install of %s: %v", name, err)
	}
	// Only the files are inert until `generate`; every row the apply wrote is
	// in its owner's table already. So the row half of the report is DERIVED
	// from what the apply returned, and a resource the manifest engine gains
	// reports itself. A list of kinds written out beside the apply drifts from
	// it: a package declaring a scheduled task and an egress rule installed
	// both, live, under `0 route(s), 0 grant(s)` and named neither (BUGS L199).
	fmt.Printf("installed %s@%s (%d file(s)) — run `arizuko generate %s` to apply\n",
		name, revision, len(files), inst)
	for _, res := range slices.Sorted(maps.Keys(applied)) {
		fmt.Printf("  %s: %d row(s), applied now\n", res, len(applied[res]))
	}
	// Grants are named on top of being counted: a count cannot distinguish
	// `reply on eng/*` from `admin on eng/tts`, and a grant is the one asset
	// kind that changes who can do what (BUGS L36).
	for _, row := range grants {
		fmt.Println("  " + describeGrant(grantRowOf(row)))
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
func packageName(origin string) string {
	s := strings.TrimSuffix(strings.TrimRight(strings.TrimPrefix(origin, "git+"), "/"), ".git")
	return filepath.Base(s)
}

// checkSourcedFiles bounds what a package from a source may put into
// services/: no file that would replace one the instance already has (the
// catalog's link, an operator's own fragment), no name of a core service, and
// each fragment within compose.CheckSourcedFragment. owned names the files
// this package's record already holds, which an upgrade may rewrite.
func checkSourcedFiles(svcDir, name, srcDir string, files []string, owned map[string]string) error {
	if baseDaemons[name] {
		return fmt.Errorf("%s is a core service; a package from a source may not take its name", name)
	}
	for _, n := range files {
		if _, mine := owned["file:"+n]; !mine {
			if _, err := os.Lstat(filepath.Join(svcDir, n)); err == nil {
				return fmt.Errorf("services/%s is already there and not this package's — "+
					"`packages remove` what owns it first", n)
			}
		}
		if !strings.HasSuffix(n, ".yml") {
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

// readFragmentAssets returns the file asset names in dir and their content
// hashes. A package ships exactly two kinds of file, split by who OWNS the
// format: `<name>.yml` is docker's compose fragment, included verbatim, and
// `<name>.yaml` is arizuko's 5/8 manifest fragment — the rows install applies
// and generate reads.
func readFragmentAssets(dir, name string) (files []string, hashes map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		die("Failed: read source %s: %v", dir, err)
	}
	hashes = map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || (n != name+".yml" && n != name+".yaml") {
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
// edited (dirty) asset (spec 5/28-packages, P3).
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

// checkGrantPredicate refuses a grant whose predicate can never be evaluated.
// `acl.predicate` is matched against the CALLER'S JWT claims, and a `folder:`
// principal is the agent MCP socket's own principal, whose gate builds its Caller
// with no claims at all — so every non-empty predicate on such a row fails and
// the grant is inert on the one surface it names. It fails closed, so this is a
// silent no-op rather than a leak, and it gets the same answer as a product
// declaring `mcpServers`: refuse the write (spec 5/5-worlds-agents-sessions §Open 2, BUGS F59).
func checkGrantPredicate(row resources.ACLRow) error {
	if row.Predicate == "" || !strings.HasPrefix(row.Principal, "folder:") {
		return nil
	}
	return fmt.Errorf("grant %s %q carries predicate %q, and a folder: principal is only ever "+
		"matched on the agent socket, which carries no JWT claims — the row would never match; "+
		"drop the predicate or grant a user: principal "+
		"(spec 5/5 §\"Open 2\", settled 2026-08-15)",
		row.Principal, row.Action, row.Predicate)
}

// checkGrantScope refuses a grant scope a package may not ask for (BUGS L46).
// `**` and `*` are refused whatever the operator named: operator-ness is emergent
// from exactly such a row, so a package able to declare one hands itself the
// instance. Every other scope must lie inside the folder(s) named at install —
// subset-of-held, the delegation model 5/33-paths-roles already applies to a person.
func checkGrantScope(bound []string, row resources.ACLRow) error {
	switch row.Scope {
	case "**", "*":
		return fmt.Errorf("grant %s %q declares scope %q, which reaches every folder — "+
			"instance-wide authority is not a package's to ask for (spec 5/33, BUGS L46)",
			row.Principal, row.Action, row.Scope)
	case "":
		return fmt.Errorf("grant %s %q declares no scope, and an unscoped row is not a bounded one",
			row.Principal, row.Action)
	}
	if len(bound) == 0 {
		return fmt.Errorf("grant %s %q declares scope %q, but this install named no folder — "+
			"re-run with --folder <scope> to hand the package the ground it may grant within (BUGS L46)",
			row.Principal, row.Action, row.Scope)
	}
	if !scopeWithin(bound, row.Scope) {
		return fmt.Errorf("grant %s %q declares scope %q, which reaches outside the installed bound %s",
			row.Principal, row.Action, row.Scope, strings.Join(bound, ", "))
	}
	return nil
}

// scopeWithin reports whether every folder `scope` can reach is also covered by
// `bound`. auth.MatchGroups answers pattern-against-PATH and both sides here are
// patterns, so the package's scope is turned into its witnesses — the paths
// hardest for a bound to cover — and each is put through that one matcher.
// Containment is still decided in exactly one place (5/33-paths-roles decision 8).
func scopeWithin(bound []string, scope string) bool {
	if slices.Contains(bound, scope) {
		return true // what the operator wrote verbatim needs no witness
	}
	for _, w := range scopeWitnesses(bound, scope) {
		if !auth.MatchGroups(bound, w) {
			return false
		}
	}
	return true
}

// scopeWitnessSeg is a path segment no folder can equal (folder names are
// validated by groupfolder), so a literal bound segment cannot match it while a
// wildcard one can — which is exactly the distinction a subset test needs.
const scopeWitnessSeg = "\x00"

// scopeWitnesses returns concrete paths `scope` matches, chosen so a bound that
// covers all of them covers the whole pattern. Every wildcard segment becomes a
// sentinel, and `**` is expanded BOTH to nothing and to one segment deeper than
// the longest bound — so `eng/**` fails a bound of `eng/*` (it reaches deeper)
// and fails a bound of `eng/*` at the shallow end too (it also covers `eng`),
// while `eng/*` passes a bound of `eng/**`.
func scopeWitnesses(bound []string, scope string) []string {
	depth := 1
	for _, b := range bound {
		if n := len(strings.Split(b, "/")); n >= depth {
			depth = n + 1
		}
	}
	var shallow, deep []string
	for _, seg := range strings.Split(scope, "/") {
		switch {
		case seg == "**":
			for i := 0; i < depth; i++ {
				deep = append(deep, scopeWitnessSeg)
			}
		case strings.ContainsAny(seg, "*?["):
			shallow = append(shallow, scopeWitnessSeg)
			deep = append(deep, scopeWitnessSeg)
		default:
			shallow = append(shallow, seg)
			deep = append(deep, seg)
		}
	}
	return []string{strings.Join(shallow, "/"), strings.Join(deep, "/")}
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

// grantRowOf converts a package manifest's acl row into the domain row every
// writer and the renderer above take. The manifest type is the wire shape
// `resreg/resources` declares; core.ACLRow is what store writes.
func grantRowOf(row resources.ACLRow) core.ACLRow {
	return core.ACLRow{
		Principal: row.Principal, Action: row.Action, Scope: row.Scope,
		Effect: row.Effect, Params: row.Params, Predicate: row.Predicate,
		GrantedBy: row.GrantedBy, GrantedAt: row.GrantedAt,
		GrantOption: row.GrantOption,
	}
}

// packageStore wraps routd.db for the package writers, attributed to the
// operator who ran the CLI — a package brings the grants and the routes, but a
// person chose to install it, and that is who the trail must name.
func packageStore(rdb *routd.DB) *store.Store {
	return store.NewOver(rdb.Obj()).AsCLI(os.Getenv("USER"))
}

// pkgFragment is one subsystem's worth of a package's declared rows: resource
// name → `[]RowType` slice, exactly the shape resreg.Apply consumes.
type pkgFragment struct {
	subsystem string
	rows      map[string]any
}

// parsePkgManifests reads every `<name>.yaml` in dir and groups their rows by
// owner subsystem. It writes nothing: an unknown resource key, an unknown row
// field, an unevaluable grant predicate or a resource `apply` never rebuilds all
// fail here, before the install touches the instance.
//
// acl rows are stamped `granted_by = package:<name>` whatever the file says, and
// proxyd_routes rows `owner = package:<name>`, so the row names where the
// authority came from and `remove` can find it. This is the one place a package's
// rows enter the instance, so it is also where a package is held to what the
// operator handed it (BUGS L46).
func parsePkgManifests(name, dir string, files []string, bound []string, bundled string) []pkgFragment {
	bySubsystem := map[string]map[string]any{}
	var order []string
	for _, n := range files {
		if !strings.HasSuffix(n, ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		docs, serr := resreg.SplitDocuments(b)
		if serr != nil {
			die("Failed: %s: %v", n, serr)
		}
		for _, doc := range docs {
			// ParseManifest, not ParseYAML: a package's document may carry an
			// `env` section. provisionPkgEnv reads that half; this reader takes
			// the rows (one declaration, two readers).
			manifest, _, _, perr := resreg.ParseManifest(doc)
			if perr != nil {
				die("Failed: parse %s: %v", n, perr)
			}
			if len(manifest) == 0 {
				continue
			}
			subsystem, sserr := resreg.SubsystemOf(manifest)
			if sserr != nil {
				die("Failed: %s: %v", n, sserr)
			}
			if _, seen := bySubsystem[subsystem]; !seen {
				bySubsystem[subsystem] = map[string]any{}
				order = append(order, subsystem)
			}
			for res, rows := range manifest {
				r := resreg.Lookup(res)
				if r.SkipApplyRebuild {
					die("Failed: %s declares %s, which `apply` never writes — it is managed "+
						"imperatively (spec 5/8 §Secret safety); the rows would install as a silent no-op", n, res)
				}
				if !pkgSubsystemServed(r.DB) {
					die("Failed: %s declares %s, which no owner applies — it is a read-only "+
						"projection over other tables, not one this CLI writes; the rows would "+
						"install as a silent no-op", n, res)
				}
				switch res {
				case resources.ACLName:
					rows = stampPkgGrants(name, n, bound, rows)
				case resources.ProxydRoutesName:
					rows = stampPkgRoutes(name, n, rows, bundled)
				}
				merged, merr := r.MergeRows(bySubsystem[subsystem][res], rows)
				if merr != nil {
					die("Failed: %s: %v", n, merr)
				}
				bySubsystem[subsystem][res] = merged
			}
		}
	}
	sort.Strings(order)
	out := make([]pkgFragment, 0, len(order))
	for _, s := range order {
		out = append(out, pkgFragment{subsystem: s, rows: bySubsystem[s]})
	}
	return out
}

// stampPkgGrants sets granted_by on every acl row a package declares and
// refuses the ones the agent socket could never evaluate or the operator never
// handed it.
func stampPkgGrants(name, file string, bound []string, rows any) any {
	grants, ok := rows.([]resources.ACLRow)
	if !ok {
		die("Failed: %s: acl rows have unexpected type %T", file, rows)
	}
	for i := range grants {
		if err := checkGrantPredicate(grants[i]); err != nil {
			die("Failed: %s: %v", file, err)
		}
		if err := checkGrantScope(bound, grants[i]); err != nil {
			die("Failed: %s: %v", file, err)
		}
		grants[i].GrantedBy = "package:" + name
	}
	return grants
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

// pkgSubsystemStores opens one *store.Store per named subsystem. routd is
// always present (the installed-package record and the groups table both live
// there); onbod is opened only on demand, so an instance that never ran
// onboarding still installs a routd-only package.
func pkgSubsystemStores(dataDir string, rdb *routd.DB, subsystems []string) (map[string]*store.Store, func()) {
	stores := map[string]*store.Store{resreg.SubsystemRoutd: packageStore(rdb)}
	var closers []func()
	for _, s := range subsystems {
		if _, ok := stores[s]; ok {
			continue
		}
		if !pkgSubsystemServed(s) {
			die("Failed: package manifest targets subsystem %q, which no owner serves", s)
		}
		ob, err := mustOpenOwner(store.OwnerOnbod, dataDir)
		if err != nil {
			die("Failed: open onbod's store: %v", err)
		}
		stores[s] = ob.AsCLI(os.Getenv("USER"))
		closers = append(closers, func() { _ = ob.Close() })
	}
	return stores, func() {
		for _, c := range closers {
			c()
		}
	}
}

// installPkgRows writes a package's declared rows through the SAME `arizuko
// apply` path the operator drives by hand, and returns resource name → PK strings
// for the installed-package record.
//
// Apply rebuilds a resource from the document it is handed, so each fragment is
// MERGED onto its subsystem's live projection first and the whole projection is
// applied. That is what buys the transaction: the old hand-rolled loop called
// die() mid-write and left earlier grants behind with no record (BUGS L44). The
// CAS token is the live checksum read a moment earlier.
func installPkgRows(dataDir string, rdb *routd.DB, name string, frags []pkgFragment, digest string) (map[string][]string, error) {
	subs := make([]string, 0, len(frags))
	for _, f := range frags {
		subs = append(subs, f.subsystem)
	}
	stores, closeStores := pkgSubsystemStores(dataDir, rdb, subs)
	defer closeStores()

	owned := map[string][]string{}
	var docs []parsedDoc
	for _, f := range frags {
		live := pkgProjection(stores[f.subsystem], f.subsystem)
		doc := map[string]any{}
		for res, rows := range f.rows {
			r := resreg.Lookup(res)
			merged, err := r.MergeRows(live[res], rows)
			if err != nil {
				die("Failed: %s: %v", res, err)
			}
			doc[res] = merged
			pks, perr := r.PKs(rows)
			if perr != nil {
				die("Failed: %s: %v", res, perr)
			}
			owned[res] = pks
		}
		docs = append(docs, pkgDoc(stores[f.subsystem], f.subsystem, doc))
	}
	return owned, pkgApply(stores, docs, name, digest, map[string]any{"package": name, "added": owned})
}

// removePkgRows subtracts exactly the rows the installed-package record names
// from each owner's live projection and applies the result the same way install
// wrote it — one transaction, pre-image rollback, no second delete path. A
// manifest key that names no registered resource is fatal: skipping it would
// report a clean removal while its rows stay live.
func removePkgRows(dataDir string, rdb *routd.DB, rec routd.InstalledPackage) {
	byOwner := map[string]map[string][]string{}
	var subs []string
	for key, pks := range rec.Manifest {
		if key == manifestFiles || key == manifestSkills || key == manifestArtifact || len(pks) == 0 {
			continue
		}
		r := resreg.Lookup(key)
		if r == nil {
			die("Failed: the record for %s names %q, which is no resource — it predates the manifest "+
				"cutover (BUGS L44). Delete the stale rows by hand, then re-install.", rec.Name, key)
		}
		if _, seen := byOwner[r.DB]; !seen {
			byOwner[r.DB] = map[string][]string{}
			subs = append(subs, r.DB)
		}
		byOwner[r.DB][key] = pks
	}
	if len(subs) == 0 {
		return
	}
	sort.Strings(subs)
	stores, closeStores := pkgSubsystemStores(dataDir, rdb, subs)
	defer closeStores()

	removed := map[string][]string{}
	var docs []parsedDoc
	for _, sub := range subs {
		live := pkgProjection(stores[sub], sub)
		doc := map[string]any{}
		var prune []string
		for res, pks := range byOwner[sub] {
			r := resreg.Lookup(res)
			// Scopes of the PRE-drop projection, not the reduced one. A scoped
			// resource derives its delete set from the rows still present, so
			// withdrawing the last row in a folder would otherwise leave that
			// folder unmentioned and the live row untouched. Sound because the
			// projection is COMPLETE: prune every scope it covers, re-insert
			// the reduced set, and that scope ends up exactly right.
			prune = append(prune, r.Scopes(live[res])...)
			reduced, err := r.DropRows(live[res], pks)
			if err != nil {
				die("Failed: %s: %v", res, err)
			}
			doc[res] = reduced
			removed[res] = pks
		}
		docs = append(docs, pkgDoc(stores[sub], sub, doc, prune...))
	}
	if err := pkgApply(stores, docs, rec.Name, "", map[string]any{"package": rec.Name, "removed": removed}); err != nil {
		die("Failed: withdraw %s rows: %v", rec.Name, err)
	}
}

// pkgProjection reads one subsystem's complete manifest projection — the base
// every package change is merged onto or subtracted from. Only the resources
// the package actually touches go into the document: apply leaves an
// unmentioned resource alone, so naming the other ten would rebuild ten tables
// (and count them in the audit row) to no purpose.
func pkgProjection(st *store.Store, subsystem string) map[string]any {
	live, err := resreg.Export(resreg.On(st.Obj()), subsystem)
	if err != nil {
		die("Failed: read %s config: %v", subsystem, err)
	}
	return live
}

// pkgDoc stamps the projection with the checksum it was read at, so the apply's
// in-tx recheck refuses instead of clobbering a concurrent writer.
func pkgDoc(st *store.Store, subsystem string, manifest map[string]any, prune ...string) parsedDoc {
	sum, err := resreg.Checksum(resreg.On(st.Obj()), subsystem)
	if err != nil {
		die("Failed: checksum %s: %v", subsystem, err)
	}
	return parsedDoc{subsystem: subsystem, checksum: sum, manifest: manifest, pruneScopes: prune}
}

// pkgApply runs the missing-group preflight and the apply itself. Both package
// verbs go through here so install and remove cannot diverge on transaction,
// rollback or audit shape. Returns the error rather than dying: install has
// files on disk by then and must take them back down.
func pkgApply(stores map[string]*store.Store, docs []parsedDoc, name, digest string, extra map[string]any) error {
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		return fmt.Errorf("%s names folders that are not groups; create the groups first: %w",
			name, errors.Join(bad...))
	}
	if _, err := applyDocs(context.Background(), stores, docs, applyRun{actor: cliActor(), digest: digest, extra: extra}); err != nil {
		return err
	}
	return nil
}

// installedGrants returns the acl rows the package's fragments declare, for the
// install summary. A grant is the one asset kind that changes who can do what
// and is live the moment it lands, so it is named rather than counted (BUGS L36).
func installedGrants(frags []pkgFragment) []resources.ACLRow {
	var out []resources.ACLRow
	for _, f := range frags {
		if rows, ok := f.rows[resources.ACLName].([]resources.ACLRow); ok {
			out = append(out, rows...)
		}
	}
	return out
}

// pkgManifestDigest is the sha256 of the package's manifest fragments, in the
// order install read them — the forensic correlation token the apply audit row
// carries, same as `arizuko apply <file>`.
func pkgManifestDigest(dir string, files []string) string {
	h := sha256.New()
	for _, n := range files {
		if !strings.HasSuffix(n, ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dropFiles deletes the named files from dir — the install's own unwind, by
// explicit name, never a recursive removal of a directory.
func dropFiles(dir string, files []string) {
	for _, n := range files {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s after a failed install: %v\n", n, err)
		}
	}
}

// dirtyAssets returns the record's file assets whose current on-disk content no
// longer matches the install hash — the locally edited ones an upgrade must
// refuse to overwrite (spec 5/28-packages: no blind overwrite). A missing file is not
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

// reapplyResult reports what one reapplyPackage call did.
type reapplyResult struct {
	Revision string   // revision resolved from the source this run
	Dirty    []string // locally edited assets; non-empty means nothing was written
	Drifted  []string // catalog manifests whose bytes moved since `add`
	Refused  error    // the new revision reaches outside the package; nothing was written
	Changed  bool     // an asset's content or the resolved revision actually moved
}

// driftReport is the one sentence `upgrade` and `sync` both print for a catalog
// package whose manifest moved: one renderer, so the two verbs cannot describe
// the same state differently.
func driftReport(name string, drifted []string) string {
	return fmt.Sprintf("%s moved in the catalog since `add` — its rows were applied then and no verb "+
		"re-applies them; `packages remove %s` then `packages add %s` installs the new set",
		strings.Join(drifted, ", "), name, name)
}

// reapplyPackage re-fetches rec.Source, rewrites every file asset the record owns,
// drops the ones the new revision no longer ships, and updates the record. It
// writes NOTHING and returns Dirty when an asset diverged locally — spec 5/28-packages's
// no-blind-overwrite rule. `upgrade` and `sync` are the two callers, so there is
// ONE updater. Only the file half of the manifest is replaced: overwriting the
// recorded row and skill identities would strand rows `remove` must delete.
func reapplyPackage(svcDir string, rdb *routd.DB, rec routd.InstalledPackage) reapplyResult {
	if len(rec.Manifest[manifestArtifact]) > 0 {
		selected, err := container.ReadPackageSelection(filepath.Dir(svcDir), "")
		if err != nil {
			return reapplyResult{Refused: err}
		}
		if !slices.ContainsFunc(selected, func(snapshot container.PackageSnapshot) bool {
			return snapshot.Name == rec.Name
		}) {
			return reapplyResult{Refused: fmt.Errorf("incomplete removal: %s is inactive; retry packages remove", rec.Name)}
		}
		dir, revision, cleanup := fetchSource(rec.Source)
		defer cleanup()
		dataDir := filepath.Dir(svcDir)
		candidate, err := preparePackageCandidate(dataDir, dir, rec.Source, revision)
		if err == nil && candidate.snapshot.Name != rec.Name {
			err = fmt.Errorf("source declares %s, installed package is %s", candidate.snapshot.Name, rec.Name)
		}
		if err == nil {
			err = installManagedPackage(dataDir, rdb, candidate, nil)
		}
		return reapplyResult{Revision: candidate.revision, Refused: err,
			Changed: rec.AssetHashes[manifestArtifact] != candidate.snapshot.Digest}
	}
	if rec.Source == catalogOrigin {
		// A catalog record has nothing to re-fetch — but only its FILE half
		// tracks the catalog. The rows a manifest declares were applied once,
		// by `add`, and neither verb re-applies them, so a manifest that moved
		// since then is drift the operator is TOLD about rather than being
		// answered "nothing to do" (BUGS L315). The `.yml` fragment needs no
		// report: generate reads it through the link, and a copy that fell
		// behind is already relinkCatalog's FROZEN warning.
		var moved []string
		for _, f := range dirtyAssets(svcDir, rec) {
			if strings.HasSuffix(f, ".yaml") {
				moved = append(moved, f)
			}
		}
		return reapplyResult{Revision: rec.Revision, Drifted: moved}
	}
	if dirty := dirtyAssets(svcDir, rec); len(dirty) > 0 {
		return reapplyResult{Dirty: dirty}
	}
	dir, revision, cleanup := fetchSource(rec.Source)
	defer cleanup()
	newFiles, newHashes := readFragmentAssets(dir, rec.Name)
	if len(newFiles) == 0 {
		die("Failed: no compose fragment in source %s", rec.Source)
	}
	if err := checkSourcedFiles(svcDir, rec.Name, dir, newFiles, rec.AssetHashes); err != nil {
		return reapplyResult{Refused: err}
	}
	for _, n := range newFiles {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			die("Failed: read %s: %v", n, err)
		}
		if err := writeFileAtomic(filepath.Join(svcDir, n), b); err != nil {
			die("Failed: apply %s: %v", n, err)
		}
	}
	for k := range rec.AssetHashes {
		if _, present := newHashes[k]; present {
			continue
		}
		if fn, isFile := strings.CutPrefix(k, "file:"); isFile {
			if err := os.Remove(filepath.Join(svcDir, fn)); err != nil && !os.IsNotExist(err) {
				die("Failed: drop %s: %v", fn, err)
			}
		}
	}
	if rec.Revision == revision && maps.Equal(rec.AssetHashes, newHashes) {
		return reapplyResult{Revision: revision}
	}
	if rec.Manifest == nil {
		rec.Manifest = map[string][]string{}
	}
	rec.Manifest[manifestFiles] = newFiles
	rec.Revision = revision
	rec.AssetHashes = newHashes
	rec.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	if err := rdb.PutInstalledPackage(rec); err != nil {
		die("Failed: record %s: %v", rec.Name, err)
	}
	return reapplyResult{Revision: revision, Changed: true}
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

func cmdPackages(args []string) {
	usage := "arizuko packages <instance> list | add <name> | install <source-dir> [--folder <scope>]... | " +
		"upgrade <name> | sync | remove <name> [--group <exact-folder>]"
	args, target := parsePackageGroup(args)
	need(args, 2, usage)
	dataDir := mustInstanceDir(args[0])
	lock := lockPackageOperation(dataDir)
	// Closing the lock releases this operation's exclusive ownership.
	defer lock.Close() //nolint:errcheck
	tmplDir := packageTemplates(dataDir)
	svcDir := filepath.Join(dataDir, "services")

	switch args[1] {
	case "list":
		selected, err := container.ReadPackageSelection(dataDir, target)
		if err != nil {
			die("Failed: list package selection: %v", err)
		}
		if len(selected) > 0 {
			rdb := mustOpenRoutd()
			// The store has no pending writes.
			defer rdb.Close() //nolint:errcheck
			candidates, _, err := existingPackageCandidates(dataDir, target, rdb)
			if err != nil {
				die("Failed: %v", err)
			}
			for _, candidate := range candidates {
				fmt.Printf("%-12s selected (%s)\n", candidate.snapshot.Name, candidate.snapshot.Digest)
			}
		}
		if target != "" {
			return
		}
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
		if target != "" {
			die("Failed: catalog service packages are system-only; use install <source> --group <folder> for group skills")
		}
		// A catalog package installs through the SAME path a sourced one does;
		// only where its files come from differs. The fragment is a symlink at
		// the catalog, so a shipped fix reaches the instance on the next
		// generate (BUGS R1) — a copy only when HOST_APP_DIR is unset, since the
		// link target must be host-resolvable (compose.HostCatalogDir), never
		// derived from where this process runs.
		name, bound := parseInstallFlags(args[2:], usage)
		mustPkgName(name)
		fragment, err := os.ReadFile(filepath.Join(tmplDir, name+".yml"))
		if err != nil {
			die("Failed: add %s: %v (catalog: %s)", name, err, tmplDir)
		}
		files := []string{name + ".yml"}
		hashes := map[string]string{"file:" + name + ".yml": sha256hex(fragment)}
		if manifest, rErr := os.ReadFile(filepath.Join(tmplDir, name+".yaml")); rErr == nil {
			files = append(files, name+".yaml")
			hashes["file:"+name+".yaml"] = sha256hex(manifest)
		} else if !os.IsNotExist(rErr) {
			die("Failed: add %s manifest: %v", name, rErr)
		}
		hostDir, linked := compose.HostCatalogDir(dataDir)
		if !linked {
			fmt.Fprintln(os.Stderr, "warning: HOST_APP_DIR not set in .env — installing a local copy instead "+
				"of a catalog link; it will not receive template fixes automatically. Set HOST_APP_DIR (see INSTALL.md).")
		}
		installPackage(dataDir, args[0], name, tmplDir, files, hashes, bound, catalogOrigin, catalogOrigin, hostDir, name)
		warnUnsatisfiedDeps(name, fragment, svcDir)
	case "install":
		// Spec 5/28-packages: install a package from a source dir or git URL, recording
		// exactly what was installed.
		origin, bound := parseInstallFlags(args[2:], usage)
		dir, revision, cleanup := fetchSource(origin)
		defer cleanup()
		if !isGitURL(origin) {
			origin = dir
		}
		if target != "" {
			candidate, err := preparePackageCandidate(dataDir, dir, origin, revision)
			if err != nil {
				die("Failed: %v", err)
			}
			rdb := mustOpenRoutd()
			// The store has no pending writes.
			defer rdb.Close() //nolint:errcheck
			installGroupPackage(dataDir, target, candidate, rdb)
			return
		}
		if isManifestSource(dir) {
			candidate, err := preparePackageCandidate(dataDir, dir, origin, revision)
			if err != nil {
				die("Failed: %v", err)
			}
			rdb := mustOpenRoutd()
			// The store has no pending writes.
			defer rdb.Close() //nolint:errcheck
			if err := installManagedPackage(dataDir, rdb, candidate, bound); err != nil {
				die("Failed: %v", err)
			}
			fmt.Printf("installed %s@%s\n", candidate.snapshot.Name, candidate.revision)
			return
		}
		name := packageName(origin)
		mustPkgName(name)
		files, hashes := readFragmentAssets(dir, name)
		if len(files) == 0 {
			die("Failed: no compose fragment (*.yml) in source %s", origin)
		}
		installPackage(dataDir, args[0], name, dir, files, hashes, bound, origin, revision, "", "")
	case "upgrade":
		// P3 (spec 5/28-packages): re-install a package's assets from its recorded source,
		// but REFUSE to overwrite a locally edited (dirty) asset — emit which and
		// stop, rather than clobber the operator's change.
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		rdb := mustOpenRoutd()
		defer func() { _ = rdb.Close() }()
		rec, ok, err := rdb.InstalledPackage(target, name)
		if err != nil {
			die("Failed: read record: %v", err)
		}
		if !ok {
			die("Failed: %s is not installed", name)
		}
		if target != "" {
			cfg, err := core.LoadConfigFrom(dataDir)
			if err != nil {
				die("Failed: load config: %v", err)
			}
			dir, revision, cleanup := resolveProductSource(cfg, rec.Source)
			defer cleanup()
			candidate, err := preparePackageCandidate(dataDir, dir, rec.Source, revision)
			if err != nil {
				die("Failed: %v", err)
			}
			if candidate.snapshot.Name != name {
				die("Failed: source declares %s, installed package is %s", candidate.snapshot.Name, name)
			}
			applyProductMixLocked(dataDir, rdb, target, nil, &candidate)
			return
		}
		res := reapplyPackage(svcDir, rdb, rec)
		if len(res.Dirty) > 0 {
			die("Failed: %s has locally edited asset(s): %s\n"+
				"  upgrade refuses to overwrite them (spec 5/28). Resolve first — take-theirs:\n"+
				"  delete the file(s) then upgrade; keep-mine: file the diff upstream + fork.",
				name, strings.Join(res.Dirty, ", "))
		}
		if res.Refused != nil {
			die("Failed: upgrade %s: %v", name, res.Refused)
		}
		if len(res.Drifted) > 0 {
			fmt.Println(driftReport(name, res.Drifted))
			return
		}
		if !res.Changed {
			fmt.Printf("%s already at %s — nothing to do\n", name, res.Revision)
			return
		}
		fmt.Printf("upgraded %s@%s — run `arizuko generate %s` to apply\n", name, res.Revision, args[0])
	case "sync":
		// Spec 5/28-packages: re-apply every installed package's fragment from its recorded
		// source. Idempotent, and `upgrade` over the whole record set rather than a
		// second updater — both go through reapplyPackage, so dirty-refusal is
		// identical. A dirty package is reported and SKIPPED rather than fatal, so one
		// hand-edited fragment cannot block the rest of the instance from syncing.
		rdb := mustOpenRoutd()
		defer func() { _ = rdb.Close() }()
		recs, err := rdb.InstalledPackages()
		if err != nil {
			die("Failed: list installed packages: %v", err)
		}
		if target != "" {
			applyProductMixLocked(dataDir, rdb, target, nil, nil)
			return
		}
		if len(recs) == 0 {
			fmt.Println("no installed packages — nothing to sync")
			return
		}
		// A group-scoped record is a product in that group's ordered mix (spec
		// 5/28-packages composition), not a compose fragment — reapplyPackage would find
		// no `*.yml` and die. Re-apply those through the mix that owns them,
		// once per folder, because union / last-wins are properties of the whole
		// mix and not of one product.
		var mixed []string
		seen := map[string]bool{}
		for _, rec := range recs {
			if rec.Folder != routd.InstanceWide && !seen[rec.Folder] {
				seen[rec.Folder] = true
				mixed = append(mixed, rec.Folder)
			}
		}
		sort.Strings(mixed)
		for _, folder := range mixed {
			applyProductMixLocked(dataDir, rdb, folder, nil, nil)
		}

		var instanceWide, updated, skipped int
		for _, rec := range recs {
			if rec.Folder != routd.InstanceWide {
				continue
			}
			instanceWide++
			res := reapplyPackage(svcDir, rdb, rec)
			switch {
			case len(res.Dirty) > 0:
				skipped++
				fmt.Printf("%-12s SKIPPED — locally edited: %s\n", rec.Name, strings.Join(res.Dirty, ", "))
			case res.Refused != nil:
				skipped++
				fmt.Printf("%-12s SKIPPED — %v\n", rec.Name, res.Refused)
			case len(res.Drifted) > 0:
				fmt.Printf("%-12s %s\n", rec.Name, driftReport(rec.Name, res.Drifted))
			case res.Changed:
				updated++
				fmt.Printf("%-12s updated to %s\n", rec.Name, res.Revision)
			default:
				fmt.Printf("%-12s unchanged (%s)\n", rec.Name, res.Revision)
			}
		}
		fmt.Printf("synced %d package(s): %d updated, %d skipped — run `arizuko generate %s` to apply\n",
			instanceWide, updated, skipped, args[0])
	case "remove":
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		rdb := mustOpenRoutd()
		defer func() { _ = rdb.Close() }()
		managed, exists, err := rdb.InstalledPackage(target, name)
		if err != nil {
			die("Failed: read receipt: %v", err)
		}
		if target != "" || (exists && len(managed.Manifest[manifestArtifact]) > 0) {
			if err := removeManagedPackage(dataDir, target, name, rdb); err != nil {
				die("Failed: %v", err)
			}
			fmt.Printf("removed %s\n", name)
			return
		}
		// Prefer the installed record (spec 5/28-packages): delete exactly what the record
		// says this package owns, then drop the record. Fall back to the legacy
		// `<name>.yml` deletion for a catalog `add` that left no record.
		if rec, ok, _ := rdb.InstalledPackage(routd.InstanceWide, name); ok {
			// P4 reverse order: withdraw the live rows FIRST, so no request is
			// ever routed at a sidecar mid-teardown, THEN drop the fragment
			// files. Bring-up health-gating is compose's (healthcheck +
			// depends_on) — the CLI can't reach docker-internal backends.
			removePkgRows(dataDir, rdb, rec)
			for _, sk := range rec.Manifest[manifestSkills] {
				_ = os.RemoveAll(filepath.Join(dataDir, "skills", sk))
			}
			for k := range rec.AssetHashes {
				if fn, isFile := strings.CutPrefix(k, "file:"); isFile {
					if err := os.Remove(filepath.Join(svcDir, fn)); err != nil && !os.IsNotExist(err) {
						die("Failed: remove %s: %v", fn, err)
					}
				}
			}
			if _, err := rdb.DeleteInstalledPackage(routd.InstanceWide, name); err != nil {
				die("Failed: drop record for %s: %v", name, err)
			}
			fmt.Printf("removed %s — run `arizuko generate %s` to apply\n", name, args[0])
			return
		}
		if err := os.Remove(filepath.Join(svcDir, name+".yml")); err != nil {
			die("Failed: remove %s: %v", name, err)
		}
		if err := os.Remove(filepath.Join(svcDir, name+".yaml")); err != nil && !os.IsNotExist(err) {
			die("Failed: remove %s manifest: %v", name, err)
		}
		fmt.Printf("removed %s — run `arizuko generate %s` to apply\n", name, args[0])
	default:
		die("usage: " + usage)
	}
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
func warnUnsatisfiedDeps(name string, fragment []byte, svcDir string) {
	m := dependsOnRE.FindSubmatch(fragment)
	if m == nil {
		return
	}
	for dep := range strings.SplitSeq(string(m[1]), ",") {
		dep = strings.Trim(strings.TrimSpace(dep), `'"`)
		if dep == "" || dep == name || baseDaemons[dep] {
			continue
		}
		if _, err := os.Stat(filepath.Join(svcDir, dep+".yml")); err != nil {
			fmt.Printf("warning: %s depends on %q — add it too: `arizuko packages <instance> add %s`\n", name, dep, dep)
		}
	}
}
