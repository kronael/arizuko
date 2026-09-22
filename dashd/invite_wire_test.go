package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// dashd reads onbod's invites over /v1, and onbod's inviteJSON lives in ITS
// package main, so the compiler cannot bind the two shapes. A field renamed on
// onbod's side would leave dashd decoding it as a zero value — the invites page
// would render blank refs and no error at all, because JSON does not mind.
//
// This reads onbod's declaration out of its source and demands the same tag set
// dashd decodes into, which is the only check available across two mains.
func TestInviteRowMatchesOnbodsWireShape(t *testing.T) {
	theirs := jsonTagsOf(t, "../onbod/admin.go", "inviteJSON")
	if len(theirs) == 0 {
		t.Fatal("onbod/admin.go declares no inviteJSON; this guard is reading the wrong file")
	}
	ours := jsonTagsOf(t, "invites.go", "inviteRow")
	if !reflect.DeepEqual(ours, theirs) {
		t.Errorf("dashd decodes %v; onbod serves %v — the page would render the "+
			"missing fields as blanks and report nothing", ours, theirs)
	}
}

// jsonTagsOf returns the sorted json names of one struct's fields, dropping
// options like omitempty: the NAME is the wire contract, the option is not.
func jsonTagsOf(t *testing.T, file, typeName string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			if field.Tag == nil {
				continue
			}
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				out = append(out, name)
			}
		}
		return false
	})
	sort.Strings(out)
	return out
}
