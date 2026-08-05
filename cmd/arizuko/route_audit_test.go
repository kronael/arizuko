package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/store"
)

// `arizuko route add|rm` mutates the live route table, so it must leave an
// audit_log row naming the operator — the same mutation through dashd and MCP
// already does. routd.db has carried audit_log since routd migration 0016, so
// "routd.db has no audit_log" never justified skipping it.
//
// These are falsifiable: swap runRouteAdd back to s.PutRouteRow (or runRouteRm
// to s.DeleteRouteRow) and the mutation still lands, but routeAudit finds no row
// and the case fails. Dropping AsCLI is caught too — actor falls back to
// "system"/gateway, which is the wrong answer for an operator action.

// routeAudit returns (actor, actor_sub, surface) of the newest audit_log row for
// action in dir/routd.db, or zero values when the mutation recorded nothing.
func routeAudit(t *testing.T, dir, action string) (actor, actorSub, surface string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "routd.db"))
	if err != nil {
		t.Fatalf("open routd.db: %v", err)
	}
	defer db.Close()
	err = db.QueryRow(
		`SELECT COALESCE(actor, ''), COALESCE(actor_sub, ''), COALESCE(surface, '')
		 FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(
		&actor, &actorSub, &surface)
	if err != nil {
		return "", "", ""
	}
	return actor, actorSub, surface
}

func TestRouteAddAudited(t *testing.T) {
	dir := setupSplitStore(t)
	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runRouteAdd(s.AsCLI("alice"), 0, "platform=telegram", "main", &out); err != nil {
		t.Fatalf("runRouteAdd: %v", err)
	}
	s.Close()

	actor, sub, surface := routeAudit(t, dir, "route.create")
	if actor != "cli:alice" {
		t.Errorf("route.create actor = %q, want cli:alice", actor)
	}
	if sub != "alice" {
		t.Errorf("route.create actor_sub = %q, want alice", sub)
	}
	if surface != "cli" {
		t.Errorf("route.create surface = %q, want cli", surface)
	}
}

func TestRouteRmAudited(t *testing.T) {
	dir := setupSplitStore(t)
	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runRouteAdd(s.AsCLI("alice"), 0, "platform=telegram", "main", &out); err != nil {
		t.Fatalf("runRouteAdd: %v", err)
	}
	id := s.AllRoutes()[0].ID
	if err := runRouteRm(s.AsCLI("bob"), id, &out); err != nil {
		t.Fatalf("runRouteRm: %v", err)
	}
	s.Close()

	actor, sub, surface := routeAudit(t, dir, "route.delete")
	if actor != "cli:bob" {
		t.Errorf("route.delete actor = %q, want cli:bob", actor)
	}
	if sub != "bob" {
		t.Errorf("route.delete actor_sub = %q, want bob", sub)
	}
	if surface != "cli" {
		t.Errorf("route.delete surface = %q, want cli", surface)
	}
}

// A delete that matched nothing is not a mutation, so it must NOT leave an
// audit row claiming one happened.
func TestRouteRmMissingRecordsNothing(t *testing.T) {
	dir := setupSplitStore(t)
	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runRouteRm(s.AsCLI("alice"), 999, &out); err == nil {
		t.Error("runRouteRm(999) = nil, want no-such-route error")
	}
	s.Close()

	if actor, _, _ := routeAudit(t, dir, "route.delete"); actor != "" {
		t.Errorf("route.delete recorded %q for a route that never existed", actor)
	}
}

// An unattributed Store keeps the pre-AsCLI output, so daemons that never set an
// operator are unaffected by the CLI gaining one.
func TestRouteAuditUnattributedStaysSystem(t *testing.T) {
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

	actor, sub, surface := routeAudit(t, dir, "route.create")
	if actor != "system" || sub != "" || surface != "gateway" {
		t.Errorf("unattributed = (%q, %q, %q), want (system, \"\", gateway)", actor, sub, surface)
	}
}
