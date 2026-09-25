package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
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
	// The report names the ROW, not the handle typed: "removed route 37" is what
	// let BUGS L364 run to a destroyed rule (cmd/arizuko/route.go runRouteRm).
	for _, want := range []string{"removed route", "platform=telegram", "shop"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("rm output = %q, want the coordinate it removed (%q)", out.String(), want)
		}
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

// TestRouteLandsInRoutdDB proves the CLI route path writes the route into
// routd's own tables — the owner of `routes` — and takes it back out again.
func TestRouteLandsInRoutdDB(t *testing.T) {
	s := routdStore(t)
	var out bytes.Buffer
	if err := runRouteAdd(s, 0, "platform=telegram", "main", &out); err != nil {
		t.Fatalf("runRouteAdd: %v", err)
	}

	// The target is the SECOND field of a route's coordinate, so the match reads
	// the middle of the key rather than a leading fragment.
	targetIsMain := func(it objstore.Item) bool {
		parts := strings.SplitN(it.Key.Sort, store.KeySep, 3)
		return len(parts) == 3 && parts[1] == "main"
	}
	if n := countItems(t, s, store.Routes, targetIsMain); n != 1 {
		t.Errorf("routes = %d, want 1", n)
	}

	s2 := s
	id := s2.AllRoutes()[0].ID
	if err := runRouteRm(s2, id, &out); err != nil {
		t.Fatalf("runRouteRm: %v", err)
	}
	if n := countItems(t, s, store.Routes, targetIsMain); n != 0 {
		t.Errorf("routes after rm = %d, want 0", n)
	}
}
