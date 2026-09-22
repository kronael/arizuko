package groupfolder

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// schemeLiteralKeepers names every declaration outside this package that may
// spell "web:" or "hook:" itself, with the reason it is not a JID⇄folder
// conversion. A conversion goes through WebJID, HookJID, JidFolder or
// store.RouteToken.Folder (BUGS L148); an entry here records that the site
// asks a different question. The key is <file>:<declaration>.
var schemeLiteralKeepers = map[string]string{
	// scheme tests: "which surface is this JID?", never "which folder?"
	"dashd/authz.go:jidFolder":                "decides whether a token lookup applies; the folder is then READ from the row, JidFolder last",
	"routd/dispatch.go:processGroupMessages":  "a web chat ignores the route's pinned topic; no folder is derived",
	"routd/server.go:isChannelJID":            "exempts web:/hook: from the channel ingress-ownership check",
	"store/route_tokens.go:RouteTokenJIDKind": "classifies a token row by surface (chat|hook)",
	"webd/ratelimit.go:ceiling":               "picks the hook vs web rate cap",
	// the routing grammar: a chat_jid=web:* PATTERN in a route rule, not a JID
	"store/routes.go:MatchesWebJID": "refuses a route rule whose chat_jid pattern names a web chat (BUGS L57)",
	// the guarded last resort for a token minted before target_folder existed: web: only (BUGS L4), and JidFolder would widen it to hook:
	"routd/server.go:ownsJID": "reads the mint's target first; the web: grammar is the last resort and stays narrow",
	"ipc/ipc.go:authorizeJID": "reads the mint's target first; the web: grammar is the last resort and stays narrow",
	// the hook SOURCE segment, JidFolder's complement; the folder half reads row.TargetFolder
	"webd/route_token.go:hookSender": "extracts the source label, not the folder",
	// the channel's declaration of the scheme it serves; converts nothing
	"webd/main.go:main":           "chanreg registration of the web: prefix",
	"webd/server.go:handleHealth": "the health payload's jid_prefixes",
}

func isSchemePrefix(v string) bool {
	return v == "web:" || v == "hook:" ||
		strings.HasPrefix(v, "web:%") || strings.HasPrefix(v, "hook:%")
}

func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.ValueSpec:
				return s.Names[0].Name
			case *ast.TypeSpec:
				return s.Name.Name
			}
		}
	}
	return ""
}

// TestSchemeLiteralsLiveHere fails when non-test Go outside this package spells
// the "web:" or "hook:" prefix — as a bare literal or a format template — at a
// site schemeLiteralKeepers does not name, and when an entry there goes stale.
func TestSchemeLiteralsLiveHere(t *testing.T) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	checked := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// dot-dirs hold agent worktrees and scratch checkouts of this tree
			if name := d.Name(); name != ".." && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			switch d.Name() {
			case "third_party", "refs", "node_modules", "tmp", "dist":
				return fs.SkipDir
			}
			if path == filepath.Join("..", "groupfolder") {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		checked++
		rel, _ := filepath.Rel("..", path)
		for _, decl := range file.Decls {
			key := rel + ":" + declName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, uerr := strconv.Unquote(lit.Value)
				if uerr != nil || !isSchemePrefix(v) {
					return true
				}
				if _, ok := schemeLiteralKeepers[key]; ok {
					seen[key] = true
					return true
				}
				t.Errorf("%s: spells %s itself; a JID⇄folder conversion goes through "+
					"groupfolder.WebJID, HookJID, JidFolder or store.RouteToken.Folder, and a "+
					"site that asks a different question is named in schemeLiteralKeepers "+
					"with its reason", fset.Position(lit.Pos()), lit.Value)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("walked no Go files; the guard checked nothing")
	}
	for key := range schemeLiteralKeepers {
		if !seen[key] {
			t.Errorf("schemeLiteralKeepers names %s, which spells no scheme literal now; drop the entry", key)
		}
	}
}
