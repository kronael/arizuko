package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
)

// TestGrantActionReachesEgress pins the gap migration 0023 opened. It removed the
// depth-derived tiers and documented that operators re-delegate `egress` and
// `web:publish` explicitly — but runGrant hardcoded Action:"admin", and the lattice
// (auth.actionCovers) makes admin cover only `interact` and `mcp:*`. So the recovery
// path the migration prescribes could not be typed. On all three live instances this
// showed up as agents spawning with no network and no web mount.
func TestGrantActionReachesEgress(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	// The default grant is unchanged, and deliberately does NOT reach egress.
	if err := runGrant(s, "folder:demo", "demo", "", &out); err != nil {
		t.Fatalf("admin grant: %v", err)
	}
	if auth.Authorize(s, auth.Caller{Principal: "folder:demo"}, "egress", "demo", nil) {
		t.Fatal("an admin grant must not confer egress — the lattice stops at mcp:*")
	}

	// An explicit action does.
	if err := runGrant(s, "folder:demo", "demo", "egress", &out); err != nil {
		t.Fatalf("egress grant: %v", err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "folder:demo"}, "egress", "demo", nil) {
		t.Fatal("explicit egress grant did not authorize egress")
	}
	if !strings.Contains(out.String(), "egress") {
		t.Errorf("output does not name the action granted: %q", out.String())
	}
}

// TestGrantRejectsUnknownAction: a typo must fail loud, not write an inert row that
// reads as granted in `arizuko group grants`.
func TestGrantRejectsUnknownAction(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	err := runGrant(s, "folder:demo", "demo", "egres", &out)
	if err == nil {
		t.Fatal("typo'd action was accepted; an inert acl row now reads as a grant")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error does not name the problem: %v", err)
	}
	if len(s.ListACL("folder:demo")) != 0 {
		t.Error("a rejected grant still wrote a row")
	}
}
