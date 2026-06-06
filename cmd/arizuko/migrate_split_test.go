package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// seedMessagesDB creates a migrated messages.db at storeDir and inserts a row
// (or two) into each source table the migrator reads, including the two
// transform tables and the `errored`-column edge (messages.errored present,
// routd lacks it) plus an orphan table (secrets) that must NOT be copied.
func seedMessagesDB(t *testing.T, storeDir string) {
	t.Helper()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	db := s.DB()
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	exec(`INSERT INTO groups(folder, added_at, product) VALUES('main','2026-01-01T00:00:00Z','assistant')`)
	// messages.errored is set on this row — must copy WITHOUT it (routd has no such column).
	exec(`INSERT INTO messages(id, chat_jid, sender, content, timestamp, errored, status, turn_id, platform_id, chat_name)
		VALUES('m1','tg:1','alice','hi','2026-01-02T00:00:00Z',1,'sent','t1','pid1','Chat A')`)
	exec(`INSERT INTO messages(id, chat_jid, sender, content, timestamp, errored)
		VALUES('m2','tg:1','bob','yo','2026-01-02T00:01:00Z',0)`)
	// chats: messages.db has NO errored column → routd.errored defaults to 0.
	exec(`INSERT INTO chats(jid, agent_cursor, is_group) VALUES('tg:1','2026-01-02T00:01:00Z',1)`)
	exec(`INSERT INTO routes(seq, match, target) VALUES(0,'*','main')`)
	exec(`INSERT INTO sessions(group_folder, topic, session_id) VALUES('main','','sess-1')`)
	exec(`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at)
		VALUES(X'deadbeef','web:main/x','main','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO turn_results(folder, turn_id, session_id, status, recorded_at)
		VALUES('main','t1','sess-1','ok','2026-01-02T00:02:00Z')`)
	exec(`INSERT INTO web_routes(path_prefix, access, folder, created_at)
		VALUES('/pub/main','public','main','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO network_rules(folder, target, created_at, created_by)
		VALUES('main','coingecko.com','2026-01-01T00:00:00Z','op')`)
	exec(`INSERT INTO chat_reply_state(jid, topic, last_reply_id, engaged_folder)
		VALUES('tg:1','','m1','main')`)
	exec(`INSERT INTO group_watchers(observer, source) VALUES('main','main/trading')`)
	// acl + acl_membership: routd OWNS these now → copied to routd.db.
	exec(`INSERT INTO acl(principal, action, scope, effect, granted_at)
		VALUES('folder:main','mcp:send','main','allow','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO acl_membership(child, parent, added_at)
		VALUES('tg:1','role:operator','2026-01-01T00:00:00Z')`)
	// identities/identity_claims/identity_codes: authd OWNS these now → copied to auth.db.
	exec(`INSERT INTO identities(id, name, created_at) VALUES('idn-alice','alice','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO identity_claims(sub, identity_id, claimed_at) VALUES('tg:42','idn-alice','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO identity_claims(sub, identity_id, claimed_at) VALUES('discord:7','idn-alice','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO identity_codes(code, identity_id, expires_at) VALUES('link-x','idn-alice','2099-01-01T00:00:00Z')`)

	// transform: system_messages (group_id→folder, origin→source, event→kind, created_at→created; attrs dropped)
	exec(`INSERT INTO system_messages(group_id, origin, event, attrs, body, created_at)
		VALUES('main','system','migrate','{"k":1}','hello','2026-01-03T00:00:00Z')`)
	// transform: cost_log (no turn_id in source; input_tok/output_tok/cents/ts remap)
	exec(`INSERT INTO cost_log(ts, folder, user_sub, model, input_tok, cache_read, cache_write, output_tok, cents)
		VALUES('2026-01-04T00:00:00Z','main','u:1','claude',100,5,3,50,12)`)

	// session_log → runed.db (straight copy)
	exec(`INSERT INTO session_log(group_folder, session_id, started_at, message_count)
		VALUES('main','sess-1','2026-01-02T00:00:00Z',7)`)

	// secrets + secret_use_log: routd OWNS these now → copied to routd.db.
	exec(`INSERT INTO secrets(scope_kind, scope_id, key, value, created_at)
		VALUES('folder','main','API_KEY','v2:cipherbytes','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO secret_use_log(ts, tool, key, scope, status)
		VALUES('2026-01-01T00:00:00Z','get_secret','API_KEY','folder','ok')`)

	if err := s.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestMigrateSplit(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	seedMessagesDB(t, storeDir)

	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit: %v", err)
	}

	rdb, err := routd.Open(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()
	udb, err := runed.Open(storeDir)
	if err != nil {
		t.Fatalf("runed.Open: %v", err)
	}
	defer udb.Close()
	r, u := rdb.SQL(), udb.SQL()

	// straight-copy counts
	for tbl, want := range map[string]int{
		"groups": 1, "messages": 2, "chats": 1, "routes": 1, "sessions": 1,
		"route_tokens": 1, "turn_results": 1, "web_routes": 1,
		// network_rules: routd seeds 2 base rows (folder='') + our 1 → 3.
		"network_rules": 3, "chat_reply_state": 1, "group_watchers": 1,
		"system_messages": 1, "cost_log": 1,
		// acl: our 1 seeded row + the role:operator row migration 0053 seeds = 2.
		"acl": 2, "acl_membership": 1,
		// secrets: routd OWNS them now → copied (1 row each).
		"secrets": 1, "secret_use_log": 1,
	} {
		if got := count(t, r, tbl); got != want {
			t.Errorf("routd.%s: got %d rows, want %d", tbl, got, want)
		}
	}
	if got := count(t, u, "session_log"); got != 1 {
		t.Errorf("runed.session_log: got %d rows, want 1", got)
	}

	// messages: errored column dropped, payload columns intact.
	var content, status, turnID, platformID, chatName string
	if err := r.QueryRow(
		`SELECT content, status, turn_id, platform_id, chat_name FROM messages WHERE id='m1'`).
		Scan(&content, &status, &turnID, &platformID, &chatName); err != nil {
		t.Fatalf("read routd.messages m1: %v", err)
	}
	if content != "hi" || status != "sent" || turnID != "t1" || platformID != "pid1" || chatName != "Chat A" {
		t.Errorf("messages m1 payload wrong: %q %q %q %q %q", content, status, turnID, platformID, chatName)
	}

	// chats.errored defaulted to 0 (source had no such column).
	var errored int
	if err := r.QueryRow(`SELECT errored FROM chats WHERE jid='tg:1'`).Scan(&errored); err != nil {
		t.Fatalf("read routd.chats: %v", err)
	}
	if errored != 0 {
		t.Errorf("chats.errored = %d, want 0 (default)", errored)
	}

	// transform: system_messages remapped correctly, attrs dropped.
	var folder, source, kind, body, created string
	if err := r.QueryRow(
		`SELECT folder, source, kind, body, created FROM system_messages LIMIT 1`).
		Scan(&folder, &source, &kind, &body, &created); err != nil {
		t.Fatalf("read routd.system_messages: %v", err)
	}
	if folder != "main" || source != "system" || kind != "migrate" || body != "hello" || created != "2026-01-03T00:00:00Z" {
		t.Errorf("system_messages remap wrong: folder=%q source=%q kind=%q body=%q created=%q",
			folder, source, kind, body, created)
	}

	// transform: cost_log remapped; turn_id defaulted to ''.
	var cf, cTurn, cModel, cRecorded string
	var cin, cout, cents int
	if err := r.QueryRow(
		`SELECT folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at FROM cost_log LIMIT 1`).
		Scan(&cf, &cTurn, &cModel, &cin, &cout, &cents, &cRecorded); err != nil {
		t.Fatalf("read routd.cost_log: %v", err)
	}
	if cf != "main" || cTurn != "" || cModel != "claude" || cin != 100 || cout != 50 || cents != 12 || cRecorded != "2026-01-04T00:00:00Z" {
		t.Errorf("cost_log remap wrong: folder=%q turn=%q model=%q in=%d out=%d cents=%d at=%q",
			cf, cTurn, cModel, cin, cout, cents, cRecorded)
	}

	// acl: copied to routd.db (routd OWNS it now) with columns intact.
	var aclPrin, aclAction, aclScope, aclEffect string
	if err := r.QueryRow(
		`SELECT principal, action, scope, effect FROM acl WHERE principal='folder:main'`).
		Scan(&aclPrin, &aclAction, &aclScope, &aclEffect); err != nil {
		t.Fatalf("read routd.acl: %v", err)
	}
	if aclPrin != "folder:main" || aclAction != "mcp:send" || aclScope != "main" || aclEffect != "allow" {
		t.Errorf("acl row wrong: principal=%q action=%q scope=%q effect=%q",
			aclPrin, aclAction, aclScope, aclEffect)
	}

	// identity: copied to auth.db (authd OWNS it now). routd.db must NOT have it.
	var idTbl string
	if err := r.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='identities'`).Scan(&idTbl); err != sql.ErrNoRows {
		t.Errorf("routd.db must not contain an `identities` table (federated to authd), found %q (err=%v)", idTbl, err)
	}
	adb, err := sql.Open("sqlite", filepath.Join(storeDir, "auth.db"))
	if err != nil {
		t.Fatalf("open auth.db: %v", err)
	}
	defer adb.Close()
	if got := count(t, adb, "identities"); got != 1 {
		t.Errorf("auth.identities: got %d rows, want 1", got)
	}
	if got := count(t, adb, "identity_claims"); got != 2 {
		t.Errorf("auth.identity_claims: got %d rows, want 2", got)
	}
	if got := count(t, adb, "identity_codes"); got != 1 {
		t.Errorf("auth.identity_codes: got %d rows, want 1", got)
	}
	var idName string
	if err := adb.QueryRow(`SELECT name FROM identities WHERE id='idn-alice'`).Scan(&idName); err != nil {
		t.Fatalf("read auth.identities: %v", err)
	}
	if idName != "alice" {
		t.Errorf("auth.identities name = %q want alice", idName)
	}

	// secrets: copied to routd.db (routd OWNS it now) with the encrypted `value`
	// bytes intact — same SECRETS_KEY decrypts on the routd side.
	var secScope, secKey, secVal string
	if err := r.QueryRow(
		`SELECT scope_kind, key, value FROM secrets WHERE scope_id='main'`).
		Scan(&secScope, &secKey, &secVal); err != nil {
		t.Fatalf("read routd.secrets: %v", err)
	}
	if secScope != "folder" || secKey != "API_KEY" || secVal != "v2:cipherbytes" {
		t.Errorf("secrets row wrong: scope=%q key=%q value=%q", secScope, secKey, secVal)
	}

	// FTS index rebuilt from copied messages → searchable.
	var ftsHit int
	if err := r.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'hi'`).Scan(&ftsHit); err != nil {
		t.Fatalf("query messages_fts: %v", err)
	}
	if ftsHit != 1 {
		t.Errorf("messages_fts MATCH 'hi' = %d, want 1 (rebuilt index)", ftsHit)
	}

	rdb.Close()
	udb.Close()

	// idempotent: a second run must not error and must not duplicate.
	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit (re-run): %v", err)
	}
	rdb2, err := routd.Open(storeDir)
	if err != nil {
		t.Fatalf("routd.Open re-run: %v", err)
	}
	defer rdb2.Close()
	if got := count(t, rdb2.SQL(), "messages"); got != 2 {
		t.Errorf("after re-run routd.messages = %d, want 2 (idempotent)", got)
	}
	if got := count(t, rdb2.SQL(), "network_rules"); got != 3 {
		t.Errorf("after re-run routd.network_rules = %d, want 3 (idempotent)", got)
	}
}

func TestMigrateSplitMissingDB(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := migrateSplit(storeDir, false); err == nil {
		t.Fatal("expected error when messages.db is absent")
	}
}
