package main

import (
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
)

// Test fixtures for the two owner DBs dashd reads. They run the DAEMONS' OWN
// embedded migrations — a fixture built from routd.OpenMem/runed.OpenMem cannot
// drift, because there is no second statement of the schema to drift from.
//
// The hand-written CREATE TABLEs these replace had drifted twice with the whole
// suite green (BUGS F44): runedDB's `spawns` was missing `kind` (added by runed
// migration 0004) AND still carried `mcp_token_jti` (dropped by runed 0003), so
// every runed-page test passed against a schema the daemon has not had for
// weeks — wrong in both directions at once. Adding one `kind` read to the page
// query turned every one of them red with `no such column`.
//
// OpenMem is `cache=shared` under a per-call unique name. That matters for more
// than isolation: a handler running a nested query (outer rows iterator + inner
// query) takes a second pool connection, and with a plain `:memory:` DSN that
// second connection is a DIFFERENT, empty database. Standing the fixture up on
// a temp FILE was the old workaround (testDBFile); shared-cache removes the
// need, so there is one constructor per owner rather than two.

// routdDB is a migrated, empty routd.db. Every table dashd's admin and page
// handlers read — groups, messages, routes, scheduled_tasks, task_run_logs,
// sessions, acl, secrets, audit_log, cost_log, installed_packages,
// chat_proactive, user_profiles, pending_actions — comes from routd's chain.
func routdDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d.SQL()
}

// runedDB is a migrated, empty runed.db: spawns, session_log, circuit_breaker,
// audit_log.
func runedDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := runed.OpenMem()
	if err != nil {
		t.Fatalf("runed.OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d.SQL()
}
