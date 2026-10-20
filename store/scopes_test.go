package store

// scopes_test.go proves the two halves of BUGS L264: that a write to a scoped
// entity is SEEN by a consistent read over that scope, and that no writer in
// this package can leave the marker out.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
)

// TestAConsistentReadSeesAnInviteMintedUnderIt is the regression itself.
//
// objstore.ReadConsistent reads a scope's marker, runs the caller's queries, and
// reads the marker again; it detects EXACTLY the writers that moved the marker
// in their own batch. resreg.ExportSnapshot reads onbod's three resources
// through it, so a mint that commits between the two marker reads is either
// seen on a second attempt or silently missing from an export that then calls
// itself consistent.
//
// The interleave is FORCED rather than raced: conformance.SlipAt fires the mint
// after the read's own query returns, which is the one window the protocol
// compare-and-swaps across. Two concurrency tests in this project stayed green
// against deliberately broken code because that collision usually does not land
// by itself.
func TestAConsistentReadSeesAnInviteMintedUnderIt(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if _, _, err := s.CreateInvite("alpha", "operator", 1, nil); err != nil {
		t.Fatalf("mint the invite the export starts from: %v", err)
	}

	var mintErr error
	slipped := conformance.SlipAt(s.Obj(), Invites, 1, func() {
		_, _, mintErr = s.CreateInvite("beta", "operator", 1, nil)
	})
	seen := -1
	if err := objstore.ReadConsistent(ctx, slipped, OwnerScope(OwnerOnbod),
		func(ctx context.Context) error {
			// A retry re-runs this, so what it accumulates it discards first.
			items, err := slipped.Query(ctx, objstore.Query{Entity: Invites, Partition: OwnerOnbod})
			if err != nil {
				return err
			}
			seen = len(items)
			return nil
		}); err != nil {
		t.Fatalf("the consistent read failed: %v", err)
	}
	if mintErr != nil {
		t.Fatalf("mint an invite under the read: %v", mintErr)
	}
	if seen != 2 {
		t.Errorf("the consistent read reported %d invites, want 2: a mint committed between "+
			"the two marker reads, the marker did not move, and an export over this scope "+
			"reports itself consistent over a state no writer ever saw (BUGS L264)", seen)
	}
}

// TestTheMarkerMovesOnlyForAScopedEntity is the other side of that: a write to
// an entity no resource projects must NOT move a marker, or a secret use — one
// per tool call that resolves one — would spend an attempt of every export that
// does not read it.
func TestTheMarkerMovesOnlyForAScopedEntity(t *testing.T) {
	bumps := scopeBumps([]objstore.Write{
		{Entity: PaneSessions}, {Entity: SecretUseLog}, {Entity: "audit_log"},
		// The hot tier, which is written per inbound message and per turn: a
		// marker on any of them would move routd's version under every export
		// that does not read them.
		{Entity: Chats}, {Entity: Sessions}, {Entity: ChatReplyState},
		{Entity: ChatProactive}, {Entity: SystemMessages}, {Entity: UserProfiles},
	})
	if len(bumps) != 0 {
		t.Errorf("an unscoped batch rendered %d markers, want none", len(bumps))
	}
	bumps = scopeBumps([]objstore.Write{
		{Entity: Invites}, {Entity: Onboarding}, {Entity: OnboardingGates}, {Entity: PaneSessions},
	})
	if len(bumps) != 1 {
		t.Fatalf("a batch over onbod's three entities rendered %d markers, want 1: a batch may "+
			"name each item once, and one scope is one item", len(bumps))
	}
	want := objstore.Bump(OwnerScope(OwnerOnbod))
	if bumps[0].Entity != want.Entity || bumps[0].Key != want.Key || bumps[0].Op != want.Op {
		t.Errorf("the marker write is %v, want one against %s/%s/%s",
			bumps[0], want.Entity, want.Key.Partition, want.Key.Sort)
	}
}

// TestNoWriterGoesRoundTheBump is the enforcement, and it is a source test for
// the reason runAudited is a funnel rather than a rule in a comment: the failure
// is SILENCE. A writer that reaches the driver itself commits, returns nil and
// passes every behavioural test in this package — and the export that raced it
// reports a state no writer ever saw.
//
// So the funnel is the only door, and this is the lock on it: every driver write
// in this package goes through WriteEntities, WriteEntitiesInTx or
// incrementEntity, all three of which live in scopes.go and all three of which
// carry the marker.
func TestNoWriterGoesRoundTheBump(t *testing.T) {
	const funnel = "scopes.go"
	round := map[string]string{
		"GuardedBatch":     "WriteEntities",
		"GuardedBatchInTx": "WriteEntitiesInTx",
		"Increment":        "incrementEntity",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read this package's own directory: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" ||
			strings.HasSuffix(name, "_test.go") || name == funnel {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			instead, ok := round[sel.Sel.Name]
			if !ok {
				return true
			}
			t.Errorf("%s calls %s directly; it must call %s, which carries the version "+
				"marker the write moves. A write that reaches the driver itself is invisible "+
				"to every consistent read over its scope, the same silent way a missing audit "+
				"row is (BUGS L264, %s)", fset.Position(call.Pos()), sel.Sel.Name, instead, funnel)
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source file was checked, so this test asserts nothing")
	}
}

// mustCommitDoors are the writers that answer an error and no verdict, and
// tryDoors the ones that answer (committed, error). The two sets are what makes
// BUGS L268 unwritable: the first has no verdict to drop, and the second says in
// its NAME that a false is expected.
var (
	mustCommitDoors = map[string]bool{
		"WriteEntities": true, "WriteEntitiesInTx": true, "writeVia": true,
	}
	tryDoors = map[string]bool{
		"TryWriteEntities": true, "TryWriteEntitiesInTx": true, "tryWriteVia": true,
	}
)

// TestNoCallerDropsTheVerdict is the other lock on the funnel, and it walks the
// WHOLE module rather than this package alone: the bump rule above binds this
// package's writers, because they are the ones that owe a marker, but the
// verdict rule binds every caller of the doors, and routd, onbod and the
// fixtures are among them.
//
// It refuses three shapes. A door called as a bare statement drops the error. A
// `_` in the error position drops it too, and go vet does not see either. And a
// `_` in a Try door's verdict position is allowed ONLY under a comment, because
// a tolerated refusal is a claim about the data — "the row is already right",
// "the observer read the work list one tick ago" — and a claim with no reason is
// indistinguishable from the accident (objstore.EnsureScope writes its reason
// down; so does RestoreInvite).
//
// The fourth shape needs no rule: a verdict bound to a name and never read is a
// compile error, which Go gives for free.
func TestNoCallerDropsTheVerdict(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	blank := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "_"
	}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// .claude holds agent worktrees and refs holds reference checkouts of
			// other projects; neither is this module's source.
			switch d.Name() {
			case ".git", ".claude", "refs", "node_modules", "tmp":
				return filepath.SkipDir
			}
			// A directory with its own .git is another checkout, such as a
			// worktree under the repo root, not this module's source.
			if path != ".." {
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		checked++
		// The comment that excuses a dropped verdict has to sit immediately above
		// the statement, so the reason is read with the line it excuses.
		reasoned := map[int]bool{}
		for _, g := range f.Comments {
			reasoned[fset.Position(g.End()).Line+1] = true
		}
		at := func(n ast.Node) string { return filepath.ToSlash(fset.Position(n.Pos()).String()) }
		door := func(call *ast.CallExpr) string {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return ""
			}
			if mustCommitDoors[sel.Sel.Name] || tryDoors[sel.Sel.Name] {
				return sel.Sel.Name
			}
			return ""
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.ExprStmt:
				call, ok := s.X.(*ast.CallExpr)
				if !ok {
					return true
				}
				if name := door(call); name != "" {
					t.Errorf("%s calls %s and keeps nothing: the error goes with the "+
						"verdict, and a write that failed reads as one that happened "+
						"(BUGS L268)", at(s), name)
				}
			case *ast.AssignStmt:
				if len(s.Rhs) != 1 {
					return true
				}
				call, ok := s.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				name := door(call)
				if name == "" {
					return true
				}
				if blank(s.Lhs[len(s.Lhs)-1]) {
					t.Errorf("%s drops the error of %s", at(s), name)
					return true
				}
				if !tryDoors[name] || len(s.Lhs) != 2 || !blank(s.Lhs[0]) {
					return true
				}
				if !reasoned[fset.Position(s.Pos()).Line] {
					t.Errorf("%s drops %s's verdict with no reason above it: a refusal "+
						"the caller tolerates is a claim about the data, and a claim with "+
						"no reason reads exactly like the accident in BUGS L268. Write "+
						"down what a refusal MEANS here, or call the must-commit door",
						at(s), name)
				}
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
}
