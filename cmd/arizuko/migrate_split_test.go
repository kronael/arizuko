package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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
	// transform: cost_log (no turn_id in source; input_tok/output_tok/cents/ts remap).
	// TWO rows with the SAME (folder, model) — both must survive (synthetic per-row
	// turn_id, not a constant that would collapse them under INSERT OR IGNORE).
	exec(`INSERT INTO cost_log(ts, folder, user_sub, model, input_tok, cache_read, cache_write, output_tok, cents)
		VALUES('2026-01-04T00:00:00Z','main','u:1','claude',100,5,3,50,12)`)
	exec(`INSERT INTO cost_log(ts, folder, user_sub, model, input_tok, cache_read, cache_write, output_tok, cents)
		VALUES('2026-01-04T01:00:00Z','main','u:1','claude',200,5,3,80,24)`)
	// auth_users: routd.db OWNS it; split onbod reads it cross-DB → must be copied.
	exec(`INSERT INTO auth_users(sub, username, hash, name, created_at)
		VALUES('github:alice','alice','h','Alice','2026-01-01T00:00:00Z')`)

	// session_log → runed.db (straight copy)
	exec(`INSERT INTO session_log(group_folder, session_id, started_at, message_count)
		VALUES('main','sess-1','2026-01-02T00:00:00Z',7)`)

	// secrets + secret_use_log: routd OWNS these now → copied to routd.db.
	exec(`INSERT INTO secrets(scope_kind, scope_id, key, value, created_at)
		VALUES('folder','main','API_KEY','v2:cipherbytes','2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO secret_use_log(ts, tool, key, scope, status)
		VALUES('2026-01-01T00:00:00Z','get_secret','API_KEY','folder','ok')`)

	// scheduled_tasks + task_run_logs: routd OWNS these now → copied to routd.db.
	exec(`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode)
		VALUES('task-1','main','web:main','daily','0 9 * * *','2026-02-01T09:00:00Z','active','2026-01-01T00:00:00Z','group')`)
	exec(`INSERT INTO task_run_logs(task_id, run_at, duration_ms, status)
		VALUES('task-1','2026-01-02T09:00:00Z',12,'ok')`)

	// pane_sessions: routd OWNS this now → copied to routd.db.
	exec(`INSERT INTO pane_sessions(team_id, user_id, thread_ts, channel_id, context_jid, opened_at)
		VALUES('T1','U99','1700.1','D0XY','slack:T1/channel/C42','2026-01-01T00:00:00Z')`)

	// onboarding + invites + onboarding_gates: onbod OWNS these now → copied to onbod.db.
	exec(`INSERT INTO onboarding(jid, status, created, user_sub, gate, queued_at, admitted_at)
		VALUES('tg:1','approved','2026-01-01T00:00:00Z','github:alice','*','2026-01-01T00:00:00Z','2026-01-01T01:00:00Z')`)
	exec(`INSERT INTO invites(token, target_glob, issued_by_sub, issued_at, max_uses, used_count)
		VALUES('inv-tok','main/','cli','2026-01-01T00:00:00Z',5,2)`)
	exec(`INSERT INTO onboarding_gates(gate, limit_per_day, enabled) VALUES('*',10,1)`)

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
		// cost_log: 2 same-(folder,model) rows BOTH survive (synthetic per-row turn_id).
		"system_messages": 1, "cost_log": 2,
		// auth_users: routd.db owns it → copied (split onbod reads it cross-DB).
		"auth_users": 1,
		// acl: our 1 seeded row + the role:operator row migration 0053 seeds = 2.
		"acl": 2, "acl_membership": 1,
		// secrets: routd OWNS them now → copied (1 row each).
		"secrets": 1, "secret_use_log": 1,
		// scheduled_tasks + task_run_logs: routd OWNS them now → copied (1 row each).
		"scheduled_tasks": 1, "task_run_logs": 1,
		// pane_sessions: routd OWNS it now → copied (1 row).
		"pane_sessions": 1,
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

	// transform: cost_log remapped; turn_id synthesized 'mig-'||rowid (UNIQUE per
	// source row so INSERT OR IGNORE doesn't collapse same-(folder,model) history).
	var cf, cTurn, cModel, cRecorded string
	var cin, cout, cents int
	if err := r.QueryRow(
		`SELECT folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at FROM cost_log LIMIT 1`).
		Scan(&cf, &cTurn, &cModel, &cin, &cout, &cents, &cRecorded); err != nil {
		t.Fatalf("read routd.cost_log: %v", err)
	}
	if cf != "main" || !strings.HasPrefix(cTurn, "mig-") || cModel != "claude" || cRecorded == "" {
		t.Errorf("cost_log remap wrong: folder=%q turn=%q model=%q in=%d out=%d cents=%d at=%q",
			cf, cTurn, cModel, cin, cout, cents, cRecorded)
	}
	// #5: both same-(folder,model) rows survive (no PK collapse).
	var costN int
	if err := r.QueryRow(`SELECT COUNT(*) FROM cost_log`).Scan(&costN); err != nil {
		t.Fatalf("count routd.cost_log: %v", err)
	}
	if costN != 2 {
		t.Errorf("cost_log rows = %d, want 2 (turn_id collapse dropped history)", costN)
	}
	// #6: auth_users copied to routd.db (split onbod reads it cross-DB).
	var auName string
	if err := r.QueryRow(`SELECT username FROM auth_users WHERE sub='github:alice'`).Scan(&auName); err != nil {
		t.Fatalf("auth_users not copied to routd.db: %v", err)
	}
	if auName != "alice" {
		t.Errorf("auth_users.username = %q, want alice", auName)
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

	// scheduled_tasks: copied to routd.db (routd OWNS it now) verbatim, FK to the
	// task intact for the copied task_run_logs row.
	var taskOwner, taskCron, taskStatus string
	if err := r.QueryRow(
		`SELECT owner, cron, status FROM scheduled_tasks WHERE id='task-1'`).
		Scan(&taskOwner, &taskCron, &taskStatus); err != nil {
		t.Fatalf("read routd.scheduled_tasks: %v", err)
	}
	if taskOwner != "main" || taskCron != "0 9 * * *" || taskStatus != "active" {
		t.Errorf("scheduled_tasks row wrong: owner=%q cron=%q status=%q", taskOwner, taskCron, taskStatus)
	}
	var runStatus string
	if err := r.QueryRow(
		`SELECT status FROM task_run_logs WHERE task_id='task-1'`).Scan(&runStatus); err != nil {
		t.Fatalf("read routd.task_run_logs: %v", err)
	}
	if runStatus != "ok" {
		t.Errorf("task_run_logs status = %q want ok", runStatus)
	}

	// pane_sessions: copied to routd.db (routd OWNS it now) — the context_jid
	// keyed by channel_id resolves the way paneHints reads it back.
	var paneCtx string
	if err := r.QueryRow(
		`SELECT context_jid FROM pane_sessions WHERE channel_id='D0XY'`).Scan(&paneCtx); err != nil {
		t.Fatalf("read routd.pane_sessions: %v", err)
	}
	if paneCtx != "slack:T1/channel/C42" {
		t.Errorf("pane_sessions context_jid = %q want slack:T1/channel/C42", paneCtx)
	}

	// onboarding + invites + onboarding_gates: copied to onbod.db (onbod OWNS them
	// now). routd.db must NOT carry invites; onbod.db must round-trip the rows.
	var invTbl string
	if err := r.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='invites'`).Scan(&invTbl); err != sql.ErrNoRows {
		t.Errorf("routd.db must not contain an `invites` table (federated to onbod), found %q (err=%v)", invTbl, err)
	}
	odb, err := sql.Open("sqlite", filepath.Join(storeDir, "onbod.db"))
	if err != nil {
		t.Fatalf("open onbod.db: %v", err)
	}
	defer odb.Close()
	for tbl, want := range map[string]int{
		"onboarding": 1, "invites": 1, "onboarding_gates": 1,
	} {
		if got := count(t, odb, tbl); got != want {
			t.Errorf("onbod.%s: got %d rows, want %d", tbl, got, want)
		}
	}
	var invGlob string
	var invMax, invUsed int
	if err := odb.QueryRow(
		`SELECT target_glob, max_uses, used_count FROM invites WHERE token='inv-tok'`).
		Scan(&invGlob, &invMax, &invUsed); err != nil {
		t.Fatalf("read onbod.invites: %v", err)
	}
	if invGlob != "main/" || invMax != 5 || invUsed != 2 {
		t.Errorf("invites row wrong: glob=%q max=%d used=%d", invGlob, invMax, invUsed)
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

// TestMigrateSplitCoalescesNullMessageCols: legacy gated rows carry NULLs in
// columns scanMessages reads into plain `string` (sender_name, reply_to_id,
// source were nullable TEXT; topic/routed_to/verb/status/chat_name kept NULL on
// rows predating their NOT-NULL-DEFAULT migrations). A verbatim copy lands NULLs
// in routd.db; the next poll aborts with `converting NULL to string is
// unsupported`. The COALESCE in the messages copySpec must default each to what
// a fresh routd insert writes, so routd's own read path (scanMessages, via
// MessagesBefore) drains the row without error.
func TestMigrateSplitCoalescesNullMessageCols(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	seedMessagesDB(t, storeDir)

	// Inject a row with the genuinely-nullable source columns set to NULL.
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// sender_name + reply_to_id are plain `TEXT` (nullable) at the migrated head
	// AND scanMessages reads them into plain `string` — the exact pair routd's own
	// inserts force to '' (routd/db.go FireProactive) precisely to dodge this.
	// turn_id/platform_id are nullable too (scanned via NullString). NULL all four
	// — the legacy shape that bricked the split poll loop. (source/topic/routed_to
	// are NOT NULL at head — store migration 0023 rebuilt them — so a verbatim NULL
	// can't occur there; COALESCE on them is harmless defence.)
	if _, err := s.DB().Exec(
		`INSERT INTO messages(id, chat_jid, sender, sender_name, content, timestamp,
			reply_to_id, turn_id, platform_id)
		 VALUES('mnull','tg:1','carol',NULL,'legacy','2026-01-02T00:03:00Z',NULL,NULL,NULL)`); err != nil {
		t.Fatalf("seed NULL message: %v", err)
	}
	s.Close()

	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit: %v", err)
	}

	rdb, err := routd.Open(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()

	// 1. The copied row's plain-string columns must be '' (not NULL).
	var name, replyTo, source, topic, routedTo, verb, status, chatName sql.NullString
	if err := rdb.SQL().QueryRow(
		`SELECT sender_name, reply_to_id, source, topic, routed_to, verb, status, chat_name
		 FROM messages WHERE id='mnull'`).
		Scan(&name, &replyTo, &source, &topic, &routedTo, &verb, &status, &chatName); err != nil {
		t.Fatalf("read routd.messages mnull: %v", err)
	}
	for label, c := range map[string]sql.NullString{
		"sender_name": name, "reply_to_id": replyTo, "source": source,
		"topic": topic, "routed_to": routedTo, "chat_name": chatName,
	} {
		if !c.Valid || c.String != "" {
			t.Errorf("routd.messages.%s = %v (valid=%v), want '' not NULL", label, c.String, c.Valid)
		}
	}
	if !verb.Valid || verb.String != "message" {
		t.Errorf("routd.messages.verb = %v, want 'message'", verb.String)
	}
	if !status.Valid || status.String != "sent" {
		t.Errorf("routd.messages.status = %v, want 'sent'", status.String)
	}

	// 2. routd's OWN read path (scanMessages via MessagesBefore) must drain the
	// row without the NULL-scan error that bricked the split poll loop.
	msgs, err := rdb.MessagesBefore("tg:1", "", 50)
	if err != nil {
		t.Fatalf("MessagesBefore (scanMessages path) must not error on migrated NULLs: %v", err)
	}
	var found bool
	for _, m := range msgs {
		if m.ID == "mnull" {
			found = true
			if m.Name != "" || m.ReplyToID != "" || m.Source != "" {
				t.Errorf("mnull scanned non-empty: name=%q reply=%q source=%q", m.Name, m.ReplyToID, m.Source)
			}
		}
	}
	if !found {
		t.Error("mnull row not returned by MessagesBefore")
	}
}

// TestMigrateSplitMigrationsIdempotent: migrate-split bootstraps auth.db +
// onbod.db with CREATE TABLE IF NOT EXISTS but records NO row in the
// `migrations(service,version)` table. So on the next authd/onbod boot,
// db_utils.Migrate sees version 0 and re-runs 0004-identities.sql /
// 0001-onboarding.sql against the already-bootstrapped tables. If those CREATEs
// aren't idempotent, authd/onbod crash-loop with `table already exists`. This
// replays that exact boot by exec'ing the real migration .sql against the
// migrate-split-bootstrapped DBs — it must be a no-op, not an error.
//
// authd/onbod are package main (their migration embed.FS is unreachable here),
// so we read the migration files from disk — the same bytes db_utils.Migrate
// would exec.
func TestMigrateSplitMigrationsIdempotent(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	seedMessagesDB(t, storeDir)
	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit: %v", err)
	}

	cases := []struct {
		db    string // file in storeDir bootstrapped by migrate-split
		mig   string // the migration file db_utils.Migrate would re-run on boot
		table string // a table both pre-create — the collision point
	}{
		{"auth.db", "../../authd/migrations/0004-identities.sql", "identities"},
		{"onbod.db", "../../onbod/migrations/0001-onboarding.sql", "onboarding"},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(c.mig)
		if err != nil {
			t.Fatalf("read %s: %v", c.mig, err)
		}
		db, err := sql.Open("sqlite", filepath.Join(storeDir, c.db))
		if err != nil {
			t.Fatalf("open %s: %v", c.db, err)
		}
		// Re-running the migration against the bootstrapped DB must NOT error
		// (this is exactly what authd/onbod's db_utils.Migrate does at boot).
		if _, err := db.Exec(string(raw)); err != nil {
			t.Errorf("%s re-applied to migrate-split %s must be a no-op, got: %v", c.mig, c.db, err)
		}
		// Table still present and singular (no duplication, no drop).
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, c.table).Scan(&n); err != nil {
			t.Fatalf("count %s table in %s: %v", c.table, c.db, err)
		}
		if n != 1 {
			t.Errorf("%s.%s table count = %d, want 1", c.db, c.table, n)
		}
		db.Close()
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

// TestMigrateSplitToleratesOrphanRunLog: legacy monolith data carries
// task_run_logs whose scheduled_task was deleted (the old messages.db never
// enforced the FK). routd.db DOES declare the FK (migration 0009), so without
// FK-off on the bulk-copy connection the migration aborts. This pins the
// orphan-tolerance: copyInto disables foreign_keys for the import.
func TestMigrateSplitToleratesOrphanRunLog(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	seedMessagesDB(t, storeDir) // seeds 1 scheduled_task + 1 valid task_run_log

	// inject an orphan run log: task_id points at no scheduled_task (source has
	// no FK, so this is exactly the legacy shape krons carries).
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// Pin one conn so PRAGMA foreign_keys=OFF + the orphan INSERT share it; the
	// monolith historically had FK off when these orphans accrued.
	s.DB().SetMaxOpenConns(1)
	if _, err := s.DB().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO task_run_logs(task_id, run_at, status) VALUES(999999,'2026-01-05T00:00:00Z','ok')`); err != nil {
		t.Fatalf("seed orphan run log: %v", err)
	}
	s.Close()

	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit must tolerate orphan task_run_logs (got: %v)", err)
	}
	rdb, err := routd.Open(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()
	if got := count(t, rdb.SQL(), "task_run_logs"); got != 2 {
		t.Errorf("routd.task_run_logs: got %d, want 2 (seeded + orphan)", got)
	}
}
