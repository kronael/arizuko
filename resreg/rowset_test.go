package resreg

import (
	"slices"
	"testing"
)

// MergeRows / DropRows / PKs exist because a package holds a FRAGMENT — two
// routes, not the instance's whole route table — while Apply rebuilds a
// resource from the document it is handed. Composing the fragment onto the live
// projection here is what lets `arizuko packages` reuse the one apply path with
// its transaction and pre-image rollback, instead of the hand-rolled hot-apply
// loops BUGS L44 deletes.

// A merge adds the overlay's rows and lets the overlay win a PK collision —
// the upsert semantics the old PutProxydRoute / AddACLRow writers had.
func TestMergeRows_OverlayWinsOnPK(t *testing.T) {
	_, r := freshEngine(t)
	base := []TestRow{
		{Kind: "a", Name: "keep", Value: "1"},
		{Kind: "a", Name: "clash", Value: "old"},
	}
	overlay := []TestRow{
		{Kind: "a", Name: "clash", Value: "new"},
		{Kind: "a", Name: "added", Value: "2"},
	}
	got, err := r.MergeRows(base, overlay)
	if err != nil {
		t.Fatalf("MergeRows: %v", err)
	}
	out := got.([]TestRow)
	if len(out) != 3 {
		t.Fatalf("merged rows = %d, want 3: %v", len(out), out)
	}
	byName := map[string]string{}
	for _, row := range out {
		byName[row.Name] = row.Value
	}
	if byName["keep"] != "1" || byName["added"] != "2" || byName["clash"] != "new" {
		t.Errorf("merged = %v, want keep=1 added=2 clash=new", byName)
	}
	if base[1].Value != "old" {
		t.Errorf("MergeRows mutated the base slice in place: %v", base)
	}
}

// A nil base is "the resource has no live rows" — the common case on a fresh
// instance, and it must not need a special case at every call site.
func TestMergeRows_NilBase(t *testing.T) {
	_, r := freshEngine(t)
	got, err := r.MergeRows(nil, []TestRow{{Kind: "a", Name: "x", Value: "1"}})
	if err != nil {
		t.Fatalf("MergeRows: %v", err)
	}
	if out := got.([]TestRow); len(out) != 1 || out[0].Name != "x" {
		t.Fatalf("merged = %v, want the one overlay row", out)
	}
}

// DropRows is the inverse, and it is what makes `packages remove` exact: it
// subtracts the PKs the installed-package record names and leaves every other
// row — an operator's own route at another path — alone.
func TestDropRows_RemovesOnlyNamedPKs(t *testing.T) {
	_, r := freshEngine(t)
	rows := []TestRow{
		{Kind: "a", Name: "mine", Value: "1"},
		{Kind: "a", Name: "pkg", Value: "2"},
	}
	pks, err := r.PKs([]TestRow{{Kind: "a", Name: "pkg"}})
	if err != nil {
		t.Fatalf("PKs: %v", err)
	}
	got, err := r.DropRows(rows, pks)
	if err != nil {
		t.Fatalf("DropRows: %v", err)
	}
	out := got.([]TestRow)
	if len(out) != 1 || out[0].Name != "mine" {
		t.Fatalf("remaining = %v, want only the operator's own row", out)
	}
}

// The PK is computed from PKFields only, so a recorded PK still matches a live
// row whose non-key columns moved since the install.
func TestPKs_IgnoreNonKeyFields(t *testing.T) {
	_, r := freshEngine(t)
	recorded, err := r.PKs([]TestRow{{Kind: "a", Name: "x", Value: "at-install"}})
	if err != nil {
		t.Fatalf("PKs: %v", err)
	}
	live, err := r.PKs([]TestRow{{Kind: "a", Name: "x", Value: "edited-since"}})
	if err != nil {
		t.Fatalf("PKs: %v", err)
	}
	if !slices.Equal(recorded, live) {
		t.Fatalf("PK moved with a non-key column: %v vs %v", recorded, live)
	}
}

// A slice of the wrong row type is a programming error, not a row to coerce.
func TestMergeRows_RefusesForeignSlice(t *testing.T) {
	_, r := freshEngine(t)
	if _, err := r.MergeRows(nil, []string{"nope"}); err == nil {
		t.Fatal("want error: overlay is not a []TestRow")
	}
}
