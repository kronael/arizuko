package resources

// Single-source guard for a resource's wire IDENTITY, the half
// routd/endpoints_source_test.go's TestResourceEndpoints_SingleSource cannot
// reach.
//
// That test resolves each mounted resource's expectation THROUGH its own Name,
// so a name that drifts off a registered identity fails there. What it cannot
// see is where the name came from: at runtime `resources.ACLName` and `"acl"`
// are the same string, so a mount site restating the literal compares clean
// forever — right up until someone edits one of the two spellings. That is not
// hypothetical. proxyd's live route resource carried Name: "routes" while its
// own catalog and /openapi.json said proxyd_routes (fixed 2026-07-01), and
// routd/acl_resource.go imported ACLEndpoints/ACLMCPDoc/ACLMCPArgs/ACLMCPNames
// on four consecutive lines while writing Name: "acl" three lines above them.
//
// So this guard reads SOURCE, not values: no resreg.Resource composite literal
// anywhere in the tree may spell its Name as a string literal. Every one must
// reference a constant, and names.go is where those constants live — one
// spelling per resource, for both the catalog registration and the mounted
// handler. Spec 5/16 §"One owner + federation".

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

// repoRoot walks up from the package directory to the module root, so the scan
// covers every daemon rather than the one package this test happens to live in.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// isResourceType reports whether expr names resreg.Resource. The bare-Ident
// form is accepted only inside package resreg itself — elsewhere a plain
// `Resource{...}` is some other package's unrelated type.
func isResourceType(expr ast.Expr, pkg string) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && x.Name == "resreg" && e.Sel.Name == "Resource"
	case *ast.Ident:
		return pkg == "resreg" && e.Name == "Resource"
	}
	return false
}

// resourceLits returns every resreg.Resource composite literal in file,
// including elements written with the type elided inside a []resreg.Resource or
// map[...]resreg.Resource literal.
func resourceLits(file *ast.File) []*ast.CompositeLit {
	pkg := file.Name.Name
	var out []*ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isResourceType(lit.Type, pkg) {
			out = append(out, lit)
			return true
		}
		// []resreg.Resource{{...}} / map[K]resreg.Resource{k: {...}} — the
		// elements carry no type of their own.
		var elem ast.Expr
		switch t := lit.Type.(type) {
		case *ast.ArrayType:
			elem = t.Elt
		case *ast.MapType:
			elem = t.Value
		}
		if elem == nil || !isResourceType(elem, pkg) {
			return true
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				e = kv.Value
			}
			if inner, ok := e.(*ast.CompositeLit); ok && inner.Type == nil {
				out = append(out, inner)
			}
		}
		return true
	})
	return out
}

// TestResourceName_NoStringLiteral is the source half of the single-source
// claim: a resource's Name is spelled in exactly one place (names.go) and
// every resreg.Resource — the catalog registration AND the owning daemon's
// mounted handler — references that constant.
//
// Test files are exempt: they mount nothing, and a fixture resource
// (resreg/engine_test.go's "testrows") has no registry twin to drift from.
func TestResourceName_NoStringLiteral(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	found := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendored code and dot-dirs — .claude/worktrees holds whole
			// copies of this repo, which would scan foreign trees as if they
			// were ours.
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "third_party" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		for _, lit := range resourceLits(file) {
			found++
			for _, e := range lit.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Name" {
					continue
				}
				bl, ok := kv.Value.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				t.Errorf("%s:%d: resreg.Resource sets Name: %s as a string literal — "+
					"a resource's Name IS its wire identity (/v1/<name> and the MCP tool "+
					"prefix), and it is spelled once, in resreg/resources/names.go. Use the "+
					"constant (e.g. resources.ACLName). A literal here is what let proxyd's "+
					"live resource read Name: \"routes\" while its catalog said proxyd_routes.",
					rel, fset.Position(bl.Pos()).Line, bl.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-vacuity, derived from the registry rather than a written-down count:
	// every registered resource has a catalog declaration, so a scan that found
	// fewer literals than there are registrations saw nothing it should have —
	// a broken walk, a bad type match, or a skipped directory. Without this the
	// loop above passes by finding zero resources.
	if n := len(resreg.All()); found < n {
		t.Fatalf("scanned %s and found %d resreg.Resource literals, want at least %d "+
			"(one catalog declaration per registered resource) — this guard is not "+
			"seeing the declarations it claims to check", root, found, n)
	}
}
