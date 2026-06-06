package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// TestRouteAddListRm exercises the full CLI round-trip: add → list (shows it) →
// rm → list (gone), against an in-memory store.
func TestRouteAddListRm(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runRouteList(s, &out); err != nil {
		t.Fatalf("runRouteList (empty): %v", err)
	}
	if !strings.Contains(out.String(), "no routes") {
		t.Errorf("empty list = %q, want 'no routes'", out.String())
	}

	out.Reset()
	if err := runRouteAdd(s, 5, "platform=telegram", "shop", &out); err != nil {
		t.Fatalf("runRouteAdd: %v", err)
	}
	if !strings.Contains(out.String(), "added route") {
		t.Errorf("add output = %q", out.String())
	}

	out.Reset()
	if err := runRouteList(s, &out); err != nil {
		t.Fatalf("runRouteList: %v", err)
	}
	if !strings.Contains(out.String(), "platform=telegram") || !strings.Contains(out.String(), "shop") {
		t.Errorf("list missing route: %q", out.String())
	}

	rs := s.AllRoutes()
	if len(rs) != 1 {
		t.Fatalf("AllRoutes len = %d, want 1", len(rs))
	}

	out.Reset()
	if err := runRouteRm(s, rs[0].ID, &out); err != nil {
		t.Fatalf("runRouteRm: %v", err)
	}
	if !strings.Contains(out.String(), "removed route") {
		t.Errorf("rm output = %q", out.String())
	}

	out.Reset()
	if err := runRouteList(s, &out); err != nil {
		t.Fatalf("runRouteList (after rm): %v", err)
	}
	if !strings.Contains(out.String(), "no routes") {
		t.Errorf("after rm list = %q, want 'no routes'", out.String())
	}
}

// TestRouteCatchAllRendering proves "*" is stored as an empty Match (the
// router's catch-all representation) and rendered back as "*" in list.
func TestRouteCatchAllRendering(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runRouteAdd(s, 0, "*", "supermarket", &out); err != nil {
		t.Fatalf("runRouteAdd: %v", err)
	}
	if rs := s.AllRoutes(); len(rs) != 1 || rs[0].Match != "" {
		t.Fatalf("catch-all Match = %q, want empty", rs[0].Match)
	}

	out.Reset()
	if err := runRouteList(s, &out); err != nil {
		t.Fatalf("runRouteList: %v", err)
	}
	if !strings.Contains(out.String(), "*") {
		t.Errorf("catch-all not rendered as '*': %q", out.String())
	}
}

// TestParseRouteAdd proves flexParse fixes the footgun: --seq parses correctly
// whether it's before, after, or short-form among the positionals, including a
// negative value; and wrong positional counts error instead of silently
// dropping a misplaced flag.
func TestParseRouteAdd(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantErr   bool
		wantSeq   int
		wantMatch string
		wantTgt   string
	}{
		{name: "seq before positionals", args: []string{"--seq", "-10", "m", "t"}, wantSeq: -10, wantMatch: "m", wantTgt: "t"},
		{name: "seq after positionals", args: []string{"m", "t", "--seq", "-10"}, wantSeq: -10, wantMatch: "m", wantTgt: "t"},
		{name: "short seq after positionals", args: []string{"m", "t", "-s", "-10"}, wantSeq: -10, wantMatch: "m", wantTgt: "t"},
		{name: "no seq defaults zero", args: []string{"m", "t"}, wantSeq: 0, wantMatch: "m", wantTgt: "t"},
		{name: "three positionals errors", args: []string{"m", "t", "x", "--seq", "5"}, wantErr: true},
		{name: "one positional errors", args: []string{"m"}, wantErr: true},
		{name: "unknown flag errors", args: []string{"m", "t", "--nope", "1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seq, match, target, err := parseRouteAdd(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRouteAdd(%v) = nil error, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouteAdd(%v) error: %v", tc.args, err)
			}
			if seq != tc.wantSeq || match != tc.wantMatch || target != tc.wantTgt {
				t.Errorf("parseRouteAdd(%v) = (%d, %q, %q), want (%d, %q, %q)",
					tc.args, seq, match, target, tc.wantSeq, tc.wantMatch, tc.wantTgt)
			}
		})
	}
}

func TestRouteRmNonexistent(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runRouteRm(s, 999, &out); err == nil {
		t.Error("expected error removing nonexistent route")
	}
}

func TestRouteAddRejectsEmptyTarget(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runRouteAdd(s, 0, "platform=telegram", "", &out); err == nil {
		t.Error("expected error for empty target")
	}
}

// TestRouteLandsInRoutdDB proves the CLI route path writes into routd.db (which
// owns routes in the split topology and has NO audit_log table), leaving
// messages.db untouched. The audit-free PutRouteRow/DeleteRouteRow are required
// here — the audited AddRoute/DeleteRoute would roll back against routd.db.
func TestRouteLandsInRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runRouteAdd(s, 0, "platform=telegram", "main", &out); err != nil {
		t.Fatalf("runRouteAdd: %v", err)
	}
	s.Close()

	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM routes WHERE target='main'"); n != 1 {
		t.Errorf("routd.db routes = %d, want 1", n)
	}
	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM routes WHERE target='main'"); n != 0 {
		t.Errorf("messages.db routes = %d, want 0 (CLI must not write the monolith)", n)
	}

	// rm path also targets routd.db.
	s2, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	id := s2.AllRoutes()[0].ID
	if err := runRouteRm(s2, id, &out); err != nil {
		t.Fatalf("runRouteRm: %v", err)
	}
	s2.Close()
	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM routes WHERE target='main'"); n != 0 {
		t.Errorf("routd.db routes after rm = %d, want 0", n)
	}
}
