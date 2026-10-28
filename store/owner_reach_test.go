package store

// owner_reach_test.go binds StoreReach to the call sites and to INSTALL.md.
//
// It is the check BUGS L318 found missing: nothing in the repo generated,
// documented or verified the per-daemon store policies, so 19 of them drifted
// to `dynamodb:* on *` and the one sentence that described them named an owner
// dashd has never opened.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// daemonOf names the daemon a source file belongs to, the way the deployment
// does: the top-level directory, except the operator CLI, which is one binary
// under cmd/. An empty answer means the file is not a daemon's.
func daemonOf(path string) string {
	parts := strings.Split(filepath.ToSlash(strings.TrimPrefix(path, "../")), "/")
	if len(parts) < 2 {
		return ""
	}
	if parts[0] == "cmd" && len(parts) > 2 {
		return "cmd/" + parts[1]
	}
	// store declares the doors; tests and reference trees are not deployed.
	switch parts[0] {
	case "store", "tests", "third_party", "resreg", "objstore":
		return ""
	}
	return parts[0]
}

// ownerArg reads the owner or adapter prefix a door was called with. It reads
// only the call's OWN arguments, never an expression nested inside one, so a
// query that carries store.OwnerRoutd as a partition is not mistaken for a
// daemon opening routd.
//
// An owner door must name a `store.Owner*` CONSTANT. A literal is accepted only
// for the adapter door, because `Handle` and friends are ordinary method names
// and `http.Handle("GET /metrics", …)` opens no store.
func ownerArg(call *ast.CallExpr, adapter bool) (string, bool) {
	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.SelectorExpr:
			if adapter {
				continue
			}
			if pkg, ok := a.X.(*ast.Ident); ok && pkg.Name == "store" &&
				strings.HasPrefix(a.Sel.Name, "Owner") {
				return strings.ToLower(strings.TrimPrefix(a.Sel.Name, "Owner")), true
			}
		case *ast.BasicLit:
			if adapter && a.Kind == token.STRING {
				return strings.Trim(a.Value, `"`), true
			}
		}
	}
	return "", false
}

// doorName answers the door a call names, whether it is qualified
// (store.OpenOwner) or the package's own wrapper (mustOpen), and whether
// it is the adapter door, whose argument is a literal.
func doorName(call *ast.CallExpr) (name string, adapter bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	case *ast.Ident:
		name = fn.Name
	default:
		return "", false
	}
	if name == adapterStoreDoor {
		return name, true
	}
	if storeDoors[name] {
		return name, false
	}
	return "", false
}

// measureReach walks the module and answers which owners each daemon opens.
func measureReach(t *testing.T) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A DOT DIRECTORY IS NOT PART OF THE MODULE. The go tool ignores
			// them and so must this walk: the repo root carries untracked
			// working copies of the whole tree (`.migrate/`, `.migrate2/`) and
			// every agent worktree under `.claude/`, each holding another
			// `routd/` and `authd/`. Walking one reports a daemon named
			// `.migrate` that opens twelve owners — a failure about a
			// directory, not about this instance's keys.
			if name := d.Name(); name != ".." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "refs", "node_modules", "tmp", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		daemon := daemonOf(path)
		if daemon == "" {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			door, adapter := doorName(call)
			if door == "" {
				return true
			}
			owner, ok := ownerArg(call, adapter)
			if !ok {
				// A door called with a variable is the wrapper's own body; its
				// call sites carry the constant.
				return true
			}
			if !slices.Contains(got[daemon], owner) {
				got[daemon] = append(got[daemon], owner)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	if checked == 0 {
		t.Fatal("no source file was checked, so this test asserts nothing")
	}
	for d := range got {
		sort.Strings(got[d])
	}
	return got
}

// TestStoreReachMatchesTheCallSites is the lock on the policy table. A daemon
// that opens an owner the map does not list would be deployed with a key the
// policy denies — and the failure lands the first time it touches that table,
// not at deploy. A daemon listed for an owner it does not open is the opposite
// error, and the one INSTALL.md shipped: prose that granted dashd `runed.*`.
func TestStoreReachMatchesTheCallSites(t *testing.T) {
	got := measureReach(t)
	for daemon, owners := range got {
		want, declared := StoreReach[daemon]
		if !declared {
			t.Errorf("%s opens %v and StoreReach does not list it: a daemon absent from "+
				"the map is a daemon whose store key nobody scoped (BUGS L318)", daemon, owners)
			continue
		}
		sorted := slices.Clone(want)
		sort.Strings(sorted)
		if !slices.Equal(owners, sorted) {
			t.Errorf("%s opens %v, StoreReach declares %v; the policy on its access key "+
				"is written from the declaration, so the difference is either a table it "+
				"cannot reach or a table it should not", daemon, owners, sorted)
		}
	}
	for daemon, want := range StoreReach {
		if len(want) > 0 && got[daemon] == nil {
			t.Errorf("StoreReach gives %s %v and it opens no store at all: the key is "+
				"broader than the daemon, which is the whole of BUGS L318", daemon, want)
		}
	}
}

// TestInstallDocumentsTheReach keeps the operator's copy honest. The policies
// are put in place by hand, so the table an operator reads IS the interface —
// and it is the thing that drifted. The failure prints the table to paste.
func TestInstallDocumentsTheReach(t *testing.T) {
	doc, err := os.ReadFile("../INSTALL.md")
	if err != nil {
		t.Fatalf("read INSTALL.md: %v", err)
	}
	// PRETTIER OWNS THE LAYOUT. The pre-commit hook pads every markdown table to
	// its widest cell, so an exact-string match passes on the tree the author
	// typed and fails on the tree that gets committed. Both sides are compared
	// on collapsed whitespace, which is the content the row carries.
	rows := map[string]bool{}
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			rows[collapse(line)] = true
		}
	}
	var missing []string
	for _, daemon := range slices.Sorted(maps.Keys(StoreReach)) {
		row := installRow(daemon, StoreReach[daemon])
		if !rows[collapse(row)] {
			missing = append(missing, row)
		}
	}
	if len(missing) > 0 {
		t.Errorf("INSTALL.md does not carry these rows of the store-key table; "+
			"paste them into § The store server:\n%s", strings.Join(missing, "\n"))
	}
}

// installRow renders one row of the table INSTALL.md carries, so the doc and
// the declaration cannot say different things about the same daemon.
func installRow(daemon string, owners []string) string {
	if len(owners) == 0 {
		return "| `" + daemon + "` | none |"
	}
	quoted := make([]string, len(owners))
	for i, o := range owners {
		quoted[i] = "`" + o + ".*`"
	}
	return "| `" + daemon + "` | " + strings.Join(quoted, ", ") + " |"
}

// collapse reduces a table row to its content: every run of whitespace becomes
// one space, so a padded cell and an unpadded one read the same.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// storeDoors are the calls that open a store on one of the four owners. A
// daemon reaches an owner's tables through one of these and no other way. The
// two unexported names are the local wrappers dashd and the CLI put in front of
// OpenOwner; their call sites carry the constant, their bodies take a variable.
//
// The owner arrives as a `store.Owner*` CONSTANT, never as a literal, and the
// reach test demands that. `Handle` is a common method name — `http.Handle("GET
// /metrics", …)` is not a daemon opening a store called `GET /metrics` — so the
// constant is what separates a door from its namesake.
var storeDoors = map[string]bool{
	"OpenOwner": true, "OwnerDriver": true, "Handle": true,
	"PrepareOwner": true, "TestOwner": true,
	"mustOpen": true, "openSiblingOwner": true,
}

// adapterStoreDoor opens an adapter's OWN tables, and its prefix is the service
// name as a literal rather than one of the four owner constants — an adapter's
// tables are its own (chanlib.OpenAdapterStore).
const adapterStoreDoor = "OpenAdapterStore"
