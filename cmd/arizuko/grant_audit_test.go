package main

import (
	"bytes"
	"testing"

	"github.com/kronael/arizuko/store"
)

// `arizuko grant|ungrant` mutates acl / acl_membership — the highest-privilege
// mutation the CLI makes, and the one an audit trail exists for. Both must leave
// an audit_log row naming the operator, as the same grant through dashd and MCP
// already does. routd.db has carried audit_log since routd migration 0016, so
// "routd.db has no audit_log" never justified skipping it (BUGS.md Q5).
//
// Falsifiable per writer: swap runGrant back to s.PutACLRow (or the `**` branch
// to s.PutMembership, runUngrant to the deleted Bare twins) and the acl row still
// lands, but routeAudit finds nothing and only that case fails. Dropping AsCLI
// fails too, with actor=system — the bar is the operator, not merely a row.

// grantStore opens dir's routd.db as the CLI opens it: attributed to osUser.
func grantStore(t *testing.T, dir, osUser string) *store.Store {
	t.Helper()
	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if osUser == "" {
		return s
	}
	return s.AsCLI(osUser)
}

func TestGrantACLAudited(t *testing.T) {
	dir := setupSplitStore(t)
	s := grantStore(t, dir, "alice")
	var out bytes.Buffer
	if err := runGrant(s, "github:1", "atlas/eng", "", &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}

	actor, sub, surface := routeAudit(t, dir, "acl.add")
	if actor != "cli:alice" || sub != "alice" || surface != "cli" {
		t.Errorf("acl.add = (%q, %q, %q), want (cli:alice, alice, cli)", actor, sub, surface)
	}
}

func TestUngrantACLAudited(t *testing.T) {
	dir := setupSplitStore(t)
	s := grantStore(t, dir, "alice")
	var out bytes.Buffer
	if err := runGrant(s, "github:1", "atlas/eng", "", &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}
	if err := runUngrant(s, "github:1", "atlas/eng", &out); err != nil {
		t.Fatalf("runUngrant: %v", err)
	}

	actor, sub, surface := routeAudit(t, dir, "acl.remove")
	if actor != "cli:alice" || sub != "alice" || surface != "cli" {
		t.Errorf("acl.remove = (%q, %q, %q), want (cli:alice, alice, cli)", actor, sub, surface)
	}
}

// The `**` pattern is the operator grant — a role:operator membership edge, not
// an acl row. It is the single most privileged thing the CLI can hand out, so it
// carries its own audit assertion rather than riding on the acl case.
func TestGrantOperatorMembershipAudited(t *testing.T) {
	dir := setupSplitStore(t)
	s := grantStore(t, dir, "alice")
	var out bytes.Buffer
	if err := runGrant(s, "github:1", "**", "", &out); err != nil {
		t.Fatalf("runGrant **: %v", err)
	}

	actor, sub, surface := routeAudit(t, dir, "membership.add")
	if actor != "cli:alice" || sub != "alice" || surface != "cli" {
		t.Errorf("membership.add = (%q, %q, %q), want (cli:alice, alice, cli)", actor, sub, surface)
	}
}

func TestUngrantOperatorMembershipAudited(t *testing.T) {
	dir := setupSplitStore(t)
	s := grantStore(t, dir, "alice")
	var out bytes.Buffer
	if err := runGrant(s, "github:1", "**", "", &out); err != nil {
		t.Fatalf("runGrant **: %v", err)
	}
	if err := runUngrant(s, "github:1", "**", &out); err != nil {
		t.Fatalf("runUngrant **: %v", err)
	}

	actor, sub, surface := routeAudit(t, dir, "membership.remove")
	if actor != "cli:alice" || sub != "alice" || surface != "cli" {
		t.Errorf("membership.remove = (%q, %q, %q), want (cli:alice, alice, cli)", actor, sub, surface)
	}
}

// An unattributed Store keeps the pre-AsCLI output, so the daemons that never set
// an operator are unaffected by the CLI gaining one. AddMembership additionally
// falls back to addedBy, the grantor its own row records.
func TestGrantAuditUnattributedUnchanged(t *testing.T) {
	dir := setupSplitStore(t)
	s := grantStore(t, dir, "")
	var out bytes.Buffer
	if err := runGrant(s, "github:1", "atlas/eng", "", &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}
	if err := runGrant(s, "github:2", "**", "", &out); err != nil {
		t.Fatalf("runGrant **: %v", err)
	}

	actor, sub, surface := routeAudit(t, dir, "acl.add")
	if actor != "arizuko grant" || sub != "arizuko grant" || surface != "gateway" {
		t.Errorf("acl.add = (%q, %q, %q), want the granted_by fallback over gateway", actor, sub, surface)
	}
	actor, sub, surface = routeAudit(t, dir, "membership.add")
	if actor != "arizuko grant" || sub != "arizuko grant" || surface != "gateway" {
		t.Errorf("membership.add = (%q, %q, %q), want the added_by fallback over gateway", actor, sub, surface)
	}
}
