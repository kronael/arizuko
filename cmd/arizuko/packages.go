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
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// The two non-resource keys of an installed-package record's manifest. Every
// OTHER key is a resreg resource name whose value is the PK strings of the rows
// this package owns, so `remove` reverses by resource without a per-kind table
// here (spec 5/28, BUGS L44).
const (
	manifestFiles  = "compose_fragment"
	manifestSkills = "skill"
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

// readFragmentAssets returns the file asset names in dir and their content
// hashes. A package ships exactly two kinds of file, split by who OWNS the
// format: `<name>.yml` is docker's compose fragment, included verbatim, and
// `<name>.yaml` is arizuko's 5/8 manifest fragment — the rows install applies
// and generate reads.
func readFragmentAssets(dir string) (files []string, hashes map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		die("Failed: read source %s: %v", dir, err)
	}
	hashes = map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml")) {
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

// checkGrantPredicate refuses a grant whose predicate can never be evaluated.
// `acl.predicate` is matched against the CALLER'S JWT claims (auth.Authorize →
// predicateMatches). A `folder:` principal is the agent MCP socket's own
// principal, and that gate builds its Caller with no claims at all
// (routd/sibling_db.go), so every non-empty predicate on such a row fails and
// the grant is inert on the one surface it names. It fails closed, so this is a
// silent no-op rather than a leak — the same class as a product declaring
// `mcpServers`, and it gets the same answer: refuse the write instead of
// reporting success for a row that does nothing. A package's grants are the only
// writer of a non-empty predicate today (grantACLTx and dashd's grant form both
// write ''). Spec 5/5 §Open 2, BUGS F59.
func checkGrantPredicate(row resources.ACLRow) error {
	if row.Predicate == "" || !strings.HasPrefix(row.Principal, "folder:") {
		return nil
	}
	return fmt.Errorf("grant %s %q carries predicate %q, and a folder: principal is only ever "+
		"matched on the agent socket, which carries no JWT claims — the row would never match; "+
		"drop the predicate or grant a user: principal (spec 5/5 Open 2)",
		row.Principal, row.Action, row.Predicate)
}

// describeGrant is the operator-facing line for one applied grant: WHO may do
// WHAT, WHERE. A bare count hides the fact the install handed somebody admin on
// a folder — the same reason the audit row names every row it wrote (BUGS
// Q5/L36).
func describeGrant(row resources.ACLRow) string {
	effect := row.Effect
	if effect == "" {
		effect = "allow"
	}
	s := fmt.Sprintf("  %s: %s %s on %s", effect, row.Principal, row.Action, row.Scope)
	if row.Params != "" {
		s += " (" + row.Params + ")"
	}
	if row.GrantOption {
		s += " WITH GRANT OPTION"
	}
	return s
}

// packageStore wraps routd.db for the package writers, attributed to the
// operator who ran the CLI — a package brings the grants and the routes, but a
// person chose to install it, and that is who the trail must name.
func packageStore(rdb *routd.DB) *store.Store {
	return store.New(rdb.SQL()).AsCLI(os.Getenv("USER"))
}

// pkgFragment is one subsystem's worth of a package's declared rows: resource
// name → `[]RowType` slice, exactly the shape resreg.Apply consumes.
type pkgFragment struct {
	subsystem string
	rows      map[string]any
}

// parsePkgManifests reads every `<name>.yaml` in dir — the package's 5/8
// manifest fragments — and groups their rows by owner subsystem. It writes
// nothing: an unknown resource key, an unknown row field, an unevaluable grant
// predicate or a resource `apply` never rebuilds all fail here, before the
// install touches the instance.
//
// acl rows are stamped `granted_by = package:<name>` whatever the file says, so
// the row itself names where the authority came from and `remove` can find it.
func parsePkgManifests(name, dir string, files []string) []pkgFragment {
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
			manifest, _, perr := resreg.ParseYAML(doc)
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
				if res == resources.ACLName {
					rows = stampPkgGrants(name, n, rows)
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
// refuses the ones the agent socket could never evaluate.
func stampPkgGrants(name, file string, rows any) any {
	grants, ok := rows.([]resources.ACLRow)
	if !ok {
		die("Failed: %s: acl rows have unexpected type %T", file, rows)
	}
	for i := range grants {
		if err := checkGrantPredicate(grants[i]); err != nil {
			die("Failed: %s: %v", file, err)
		}
		grants[i].GrantedBy = "package:" + name
	}
	return grants
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
		if s != resreg.SubsystemOnbod {
			die("Failed: package manifest targets subsystem %q, which no owner DB serves", s)
		}
		ob, err := store.OpenOnbod(filepath.Join(dataDir, "store"))
		if err != nil {
			die("Failed: open onbod.db: %v", err)
		}
		stores[s] = ob.AsCLI(os.Getenv("USER"))
		closers = append(closers, func() { ob.Close() })
	}
	return stores, func() {
		for _, c := range closers {
			c()
		}
	}
}

// installPkgRows writes a package's declared rows through the SAME `arizuko
// apply` path the operator drives by hand, and returns resource name → PK
// strings for the installed-package record.
//
// Apply rebuilds a resource from the document it is handed, so each fragment is
// MERGED onto its subsystem's live projection first (resreg.MergeRows) and the
// whole projection is applied. That is what buys the transaction: a package
// whose third grant is invalid now leaves grants one and two unwritten, where
// the old hand-rolled loop called die() mid-write and left them behind with no
// record (BUGS L44). The CAS token is the live checksum read a moment earlier,
// so a concurrent writer makes the apply refuse rather than clobber.
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
// wrote it — one transaction, pre-image rollback, no second delete path.
//
// A manifest key that names no registered resource is fatal. The alternative is
// to skip it and report a clean removal while its rows stay live, which is the
// one thing `remove` may never do.
func removePkgRows(dataDir string, rdb *routd.DB, rec routd.InstalledPackage) {
	byOwner := map[string]map[string][]string{}
	var subs []string
	for key, pks := range rec.Manifest {
		if key == manifestFiles || key == manifestSkills || len(pks) == 0 {
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
	live, err := resreg.Export(st.DB(), subsystem)
	if err != nil {
		die("Failed: read %s config: %v", subsystem, err)
	}
	return live
}

// pkgDoc stamps the projection with the checksum it was read at, so the apply's
// in-tx recheck refuses instead of clobbering a concurrent writer.
func pkgDoc(st *store.Store, subsystem string, manifest map[string]any, prune ...string) parsedDoc {
	sum, err := resreg.Checksum(st.DB(), subsystem)
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
	if _, err := applyDocs(context.Background(), stores, docs, false, cliActor(), digest, extra); err != nil {
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

// reapplyResult reports what one reapplyPackage call did.
type reapplyResult struct {
	Revision string   // revision resolved from the source this run
	Dirty    []string // locally edited assets; non-empty means nothing was written
	Changed  bool     // an asset's content or the resolved revision actually moved
}

// reapplyPackage re-fetches rec.Source and rewrites every file asset the record
// owns, drops the ones the new revision no longer ships, and updates the record.
// It writes NOTHING and returns Dirty when an asset diverged locally — spec
// 5/28's no-blind-overwrite rule. `upgrade` (one package, dirty is fatal) and
// `sync` (every package, dirty is reported and skipped) are the two callers, so
// there is ONE updater rather than a second one beside it.
//
// Only the file half of the manifest is replaced: the row and skill identities
// install recorded are re-applied by neither verb, so overwriting them would
// strand rows `remove` is supposed to delete.
func reapplyPackage(svcDir string, rdb *routd.DB, rec routd.InstalledPackage) reapplyResult {
	if dirty := dirtyAssets(svcDir, rec); len(dirty) > 0 {
		return reapplyResult{Dirty: dirty}
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

// cmdPackages manages which adapter packages an instance runs: each is a
// compose fragment (plus an optional `<name>.yaml` manifest) copied from the
// bundled catalog into <dataDir>/services/. `arizuko generate` includes every
// fragment it finds there.
func cmdPackages(args []string) {
	usage := "arizuko packages <instance> list | add <name> | install <source-dir> | upgrade <name> | sync | remove <name>"
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
		// Confirm the kind exists before writing anything — a package installs
		// whole or not at all (no half-written fragment on a routes read error).
		fragment, err := os.ReadFile(filepath.Join(tmplDir, name+".yml"))
		if err != nil {
			die("Failed: add %s: %v (catalog: %s)", name, err, tmplDir)
		}
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			die("Failed: mkdir services: %v", err)
		}
		hasManifest := false
		if _, rErr := os.Stat(filepath.Join(tmplDir, name+".yaml")); rErr == nil {
			hasManifest = true
		} else if !os.IsNotExist(rErr) {
			die("Failed: add %s manifest: %v", name, rErr)
		}
		// Prefer a symlink at the catalog over a copy: a copy never refreshes
		// (BUGS R1 — the exact bug this replaces), a symlink always reads the
		// current template. Falls back to a copy only when HOST_APP_DIR isn't
		// configured, since the symlink target must be host-resolvable (see
		// compose.HostCatalogDir) — never derived from where this process
		// happens to run.
		if hostDir, ok := compose.HostCatalogDir(dataDir); ok {
			if err := compose.SymlinkFragment(svcDir, hostDir, name+".yml"); err != nil {
				die("Failed: add %s: %v", name, err)
			}
			if hasManifest {
				if err := compose.SymlinkFragment(svcDir, hostDir, name+".yaml"); err != nil {
					die("Failed: add %s manifest: %v", name, err)
				}
			}
		} else {
			if err := writeFileAtomic(filepath.Join(svcDir, name+".yml"), fragment); err != nil {
				die("Failed: add %s: %v", name, err)
			}
			if hasManifest {
				manifest, rErr := os.ReadFile(filepath.Join(tmplDir, name+".yaml"))
				if rErr != nil {
					die("Failed: add %s manifest: %v", name, rErr)
				}
				if err := writeFileAtomic(filepath.Join(svcDir, name+".yaml"), manifest); err != nil {
					die("Failed: add %s manifest: %v", name, err)
				}
			}
			fmt.Fprintln(os.Stderr, "warning: HOST_APP_DIR not set in .env — installed a local copy instead "+
				"of a catalog link; it will not receive template fixes automatically. Set HOST_APP_DIR (see INSTALL.md).")
		}
		warnUnsatisfiedDeps(name, fragment, svcDir)
		fmt.Printf("added %s — run `arizuko generate %s` to apply\n", name, args[0])
	case "install":
		// Spec 5/28: install a package from a source dir or git URL, recording
		// exactly what was installed. A package ships two file kinds, split by
		// who owns the format: `<name>.yml` is docker's compose fragment,
		// `<name>.yaml` is arizuko's 5/8 manifest — grants, routes, tasks,
		// network rules, web routes — applied through `apply`'s own engine.
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
		// Parse and validate the whole manifest BEFORE a byte is written: a
		// package installs whole or not at all, and every refusal this path can
		// raise (unknown resource, bad row field, inert grant predicate) belongs
		// on the near side of the first write.
		frags := parsePkgManifests(name, dir, files)
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
		manifest := map[string][]string{manifestFiles: files}
		if len(frags) > 0 {
			rows, aerr := installPkgRows(dataDir, rdb, name, frags, pkgManifestDigest(dir, files))
			if aerr != nil {
				// The rows are the half that is live the moment it lands, and the
				// apply left none of them. Take the inert half back down too, so a
				// refused install is not half-present at the next `generate`.
				dropFiles(svcDir, files)
				die("Failed: install %s: %v", name, aerr)
			}
			maps.Copy(manifest, rows)
		}
		if skills := applyPackageSkills(dataDir, dir); len(skills) > 0 {
			manifest[manifestSkills] = skills
		}
		grants := installedGrants(frags)
		// Folder is InstanceWide: install writes compose fragments, proxyd routes
		// and host files, none of which belong to a group (spec 5/28).
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
		fmt.Printf("installed %s@%s (%d file(s), %d route(s), %d grant(s)) — run `arizuko generate %s` to apply\n",
			name, revision, len(files), len(manifest[resources.ProxydRoutesName]), len(grants), args[0])
		// Grants are named, not counted: files and routes are inert until
		// `generate`, a grant is live the moment it lands.
		for _, row := range grants {
			fmt.Println(describeGrant(row))
		}
	case "upgrade":
		// P3 (spec 5/28): re-install a package's assets from its recorded source,
		// but REFUSE to overwrite a locally edited (dirty) asset — emit which and
		// stop, rather than clobber the operator's change.
		need(args, 3, usage)
		name := args[2]
		mustPkgName(name)
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		rec, ok, err := rdb.InstalledPackage(routd.InstanceWide, name)
		if err != nil {
			die("Failed: read record: %v", err)
		}
		if !ok {
			die("Failed: %s is not installed", name)
		}
		res := reapplyPackage(svcDir, rdb, rec)
		if len(res.Dirty) > 0 {
			die("Failed: %s has locally edited asset(s): %s\n"+
				"  upgrade refuses to overwrite them (spec 5/28). Resolve first — take-theirs:\n"+
				"  delete the file(s) then upgrade; keep-mine: file the diff upstream + fork.",
				name, strings.Join(res.Dirty, ", "))
		}
		if !res.Changed {
			fmt.Printf("%s already at %s — nothing to do\n", name, res.Revision)
			return
		}
		fmt.Printf("upgraded %s@%s — run `arizuko generate %s` to apply\n", name, res.Revision, args[0])
	case "sync":
		// Spec 5/28: re-apply every installed package's fragment from its recorded
		// source. Idempotent — an unchanged package prints "unchanged" and writes
		// nothing — and it is `upgrade` over the whole record set, NOT a second
		// updater: both go through reapplyPackage, so dirty-refusal is identical.
		// A dirty package is reported and SKIPPED rather than fatal, so one
		// hand-edited fragment cannot block the rest of the instance from syncing.
		// Group-scoped records are the composition half of the same lock and go
		// through the mix that owns them (below), not through reapplyPackage.
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		recs, err := rdb.InstalledPackages()
		if err != nil {
			die("Failed: list installed packages: %v", err)
		}
		if len(recs) == 0 {
			fmt.Println("no installed packages — nothing to sync")
			return
		}
		// A group-scoped record is a product in that group's ordered mix (spec
		// 5/28 composition), not a compose fragment — reapplyPackage would find
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
			applyProductMix(dataDir, rdb, folder)
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
		rdb := mustOpenRoutd(dataDir)
		defer rdb.Close()
		// Prefer the installed record (spec 5/28): delete exactly what the record
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

// relinkCatalog converts every installed fragment that's still a
// byte-identical copy of the bundled catalog into a symlink, so a catalog fix
// reaches this instance without a copy ever falling behind (BUGS R1). Runs on
// every generate, unconditionally — no operator flag, since it never touches
// a hand-edited or multi-account-variant fragment (compose.RelinkCatalog). A
// no-op when HOST_APP_DIR isn't set in .env: without it there is no
// host-resolvable path to link to, so fragments stay plain copies exactly as
// before (degraded, not broken).
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
	for _, name := range diverged {
		fmt.Fprintf(os.Stderr, "services: services/%s.yml differs from the catalog and is FROZEN — "+
			"a mount or env var added to the template since it was written will never reach this "+
			"instance; diff it against template/services/%s.yml (BUGS L41)\n", name, name)
	}
}

// migrateStoreLayout moves an instance's owner DBs into store/<owner>/ and
// creates the directories compose binds there, then chowns store/ to the
// container uid — docker materialises a missing bind source as ROOT and every
// daemon runs as uid 1000. Runs on every generate, like relinkCatalog and for
// the same reason: systemd invokes `arizuko generate` after `compose down` and
// before `up`, the one moment the whole tree is in one process's hands with no
// daemon holding a DB open. A daemon could not do this for itself — the
// narrowing is exactly what takes the flat path out of its own mount.
//
// A failure is fatal, and that is the point: the compose written next binds
// store/<owner> and points DB_PATH/ONBOD_DB_PATH/ROUTD_DB_PATH/RUNED_DB_PATH
// inside it, while every owner daemon refuses to boot on a missing file
// (db_utils.RequireDBFile). Generating that against a tree still flat turns a
// deploy into a fleet outage.
func migrateStoreLayout(dataDir string) {
	storeDir := filepath.Join(dataDir, "store")
	moved, err := compose.MigrateStoreLayout(storeDir)
	if err != nil {
		die("Failed: %v", err)
	}
	chownStore(storeDir)
	for _, p := range moved {
		fmt.Printf("store: moved %s into its owner directory\n", p)
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
