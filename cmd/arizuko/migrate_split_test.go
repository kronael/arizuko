package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/store"

	"github.com/kronael/arizuko/audit"

	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/resreg"
)

// legacyRoutdSchema is the pre-split shape of the thirteen tables routd owns,
// as a monolith on disk carries them.
//
// THE FIXTURE BUILDS IT, and that is the point rather than a workaround. The
// subject of every test below is what `arizuko migrate-split` does with a
// database an instance ALREADY HAS, and `store.Migrate` builds the schema this
// tree ships — which for these thirteen is the entities they are now. A source
// the migrator would find nothing legacy in cannot exercise the migrator. So the
// legacy shape is stated here, where the test that needs it can see it, and the
// tool's own `isLegacyShape` probe decides what to read.
//
// The columns are the final pre-split ones, measured against all three live
// instances: every column each table carries, including the four the split's
// own column lists never named (`secrets.provider` and its three OAuth
// siblings) and the two `installed_packages` gained.
//
// The entities `store.Migrate` created for these thirteen are DROPPED first,
// because a pre-split monolith holds one shape per name and it is the column
// one. No FOREIGN KEY either: nothing in the thirteen references another of
// them, and the split reads each table on its own.
const legacyRoutdSchema = `
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS acl;
DROP TABLE IF EXISTS acl_membership;
DROP TABLE IF EXISTS routes;
DROP TABLE IF EXISTS route_tokens;
DROP TABLE IF EXISTS scheduled_tasks;
DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS pending_actions;
DROP TABLE IF EXISTS network_rules;
DROP TABLE IF EXISTS proxyd_routes;
DROP TABLE IF EXISTS web_routes;
DROP TABLE IF EXISTS installed_packages;
DROP TABLE IF EXISTS github_sources;
CREATE TABLE groups (
  folder TEXT PRIMARY KEY, added_at TEXT NOT NULL, container_config TEXT,
  updated_at TEXT, product TEXT NOT NULL DEFAULT 'assistant',
  cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0, config TEXT);
CREATE TABLE acl (
  principal TEXT NOT NULL, action TEXT NOT NULL, scope TEXT NOT NULL,
  effect TEXT NOT NULL DEFAULT 'allow', params TEXT NOT NULL DEFAULT '',
  predicate TEXT NOT NULL DEFAULT '', granted_by TEXT, granted_at TEXT NOT NULL,
  grant_option INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (principal, action, scope, params, predicate, effect));
CREATE TABLE acl_membership (
  child TEXT NOT NULL, parent TEXT NOT NULL, added_by TEXT, added_at TEXT NOT NULL,
  PRIMARY KEY (child, parent));
CREATE TABLE routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER NOT NULL DEFAULT 0,
  match TEXT NOT NULL DEFAULT '', target TEXT NOT NULL,
  observe_window_messages INTEGER, observe_window_chars INTEGER,
  added_by TEXT, added_via TEXT);
CREATE TABLE route_tokens (
  token_hash BLOB PRIMARY KEY, jid TEXT NOT NULL, owner_folder TEXT,
  created_at TEXT NOT NULL, context TEXT, kind TEXT NOT NULL DEFAULT 'route',
  target_folder TEXT);
CREATE TABLE scheduled_tasks (
  id TEXT PRIMARY KEY, owner TEXT NOT NULL, chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL, cron TEXT, next_run TEXT,
  status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL,
  context_mode TEXT NOT NULL DEFAULT 'group');
CREATE TABLE secrets (
  scope_kind TEXT NOT NULL, scope_id TEXT NOT NULL, key TEXT NOT NULL,
  value BLOB NOT NULL, created_at TEXT NOT NULL, provider TEXT,
  refresh_val BLOB, expires_at TEXT, scope_list TEXT,
  PRIMARY KEY (scope_kind, scope_id, key));
CREATE TABLE pending_actions (
  id TEXT PRIMARY KEY, group_folder TEXT NOT NULL, caller_agent TEXT NOT NULL,
  tool TEXT NOT NULL, args TEXT NOT NULL DEFAULT '{}',
  args_final TEXT NOT NULL DEFAULT '{}', args_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'held', chat_jid TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, reviewed_by TEXT NOT NULL DEFAULT '',
  reviewed_at TEXT NOT NULL DEFAULT '', reviewer_note TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '', mode TEXT NOT NULL DEFAULT 'async',
  notice_id TEXT NOT NULL DEFAULT '');
CREATE TABLE network_rules (
  folder TEXT NOT NULL, target TEXT NOT NULL, created_at TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '', PRIMARY KEY (folder, target));
CREATE TABLE proxyd_routes (
  path TEXT PRIMARY KEY, backend TEXT NOT NULL, auth TEXT NOT NULL,
  gated_by TEXT NOT NULL DEFAULT '', preserve_headers TEXT NOT NULL DEFAULT '[]',
  strip_prefix INTEGER NOT NULL DEFAULT 0, redirect_to TEXT,
  owner TEXT NOT NULL DEFAULT 'arizuko');
CREATE TABLE web_routes (
  path_prefix TEXT PRIMARY KEY,
  access TEXT NOT NULL CHECK(access IN ('public','auth','deny','redirect')),
  redirect_to TEXT, folder TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE installed_packages (
  folder TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, source TEXT NOT NULL,
  revision TEXT NOT NULL, manifest TEXT NOT NULL DEFAULT '{}',
  asset_hashes TEXT NOT NULL DEFAULT '{}', installed_at TEXT NOT NULL,
  PRIMARY KEY (folder, name));
CREATE TABLE github_sources (
  source TEXT PRIMARY KEY, secret TEXT NOT NULL, owner_folder TEXT NOT NULL,
  created_at TEXT NOT NULL);
`

// legacyHotSchema is the pre-split shape of the hot-tier tables the monolith
// carries. They are entities in routd.db now, so store.Open makes them as
// (part, sort, attrs) and the fixture has to put the columns back to have
// something legacy for the migrator to find.
//
// `system_messages` is the one whose column NAMES differ from routd.db's — the
// monolith spells them group_id/origin/event/created_at — which is what
// hotColumnAlias renames; `user_profiles` is the one whose TABLE name differs,
// and it is built by hand beside the other pre-rename shapes. `chat_proactive`
// has no monolith table at all.
const legacyHotSchema = `
DROP TABLE IF EXISTS messages;
CREATE TABLE messages (
  id TEXT PRIMARY KEY, chat_jid TEXT NOT NULL, sender TEXT NOT NULL,
  sender_name TEXT, content TEXT NOT NULL, timestamp TEXT NOT NULL,
  is_from_me INTEGER NOT NULL DEFAULT 0, is_bot_message INTEGER NOT NULL DEFAULT 0,
  forwarded_from TEXT, reply_to_id TEXT, reply_to_text TEXT, reply_to_sender TEXT,
  topic TEXT NOT NULL DEFAULT '', routed_to TEXT NOT NULL DEFAULT '',
  verb TEXT NOT NULL DEFAULT 'message', attachments TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '', is_observed INTEGER NOT NULL DEFAULT 0,
  turn_id TEXT, status TEXT NOT NULL DEFAULT 'sent', platform_id TEXT,
  chat_name TEXT NOT NULL DEFAULT '', errored INTEGER NOT NULL DEFAULT 0);
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS chat_reply_state;
DROP TABLE IF EXISTS system_messages;
DROP TABLE IF EXISTS turn_results;
DROP TABLE IF EXISTS group_watchers;
DROP TABLE IF EXISTS task_run_logs;
CREATE TABLE chats (
  jid TEXT PRIMARY KEY, agent_cursor TEXT, sticky_group TEXT,
  sticky_topic TEXT, is_group INTEGER NOT NULL DEFAULT 0);
CREATE TABLE sessions (
  group_folder TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL, parent_topic TEXT, forked_at TEXT,
  observed_cursor TEXT, PRIMARY KEY (group_folder, topic));
CREATE TABLE chat_reply_state (
  jid TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '',
  last_reply_id TEXT NOT NULL, engaged_until TEXT,
  engaged_folder TEXT NOT NULL DEFAULT '', PRIMARY KEY (jid, topic));
CREATE TABLE system_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, group_id TEXT NOT NULL,
  origin TEXT NOT NULL, event TEXT NOT NULL, attrs TEXT,
  body TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE turn_results (
  folder TEXT NOT NULL, turn_id TEXT NOT NULL, session_id TEXT,
  status TEXT NOT NULL, recorded_at TEXT NOT NULL,
  PRIMARY KEY (folder, turn_id));
CREATE TABLE group_watchers (
  observer TEXT NOT NULL, source TEXT NOT NULL, PRIMARY KEY (observer, source));
CREATE TABLE task_run_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, run_at TEXT NOT NULL,
  duration_ms INTEGER, status TEXT NOT NULL, result TEXT, error TEXT);
`

// seedLegacyRoutdTables gives the monolith the thirteen tables a pre-split
// instance carries, so the migrator has something legacy to find.
func seedLegacyRoutdTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(legacyRoutdSchema); err != nil {
		t.Fatalf("build the pre-split shape of routd's thirteen: %v", err)
	}
	if _, err := db.Exec(legacyHotSchema); err != nil {
		t.Fatalf("build the pre-split shape of the hot tier: %v", err)
	}
}

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

	seedLegacyRoutdTables(t, db)

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
	// user_profiles: routd.db OWNS it; split onbod reads it cross-DB → must be copied.
	// The source is a PRE-split messages.db, where the table still has its old name
	// and its `hash` column (store migration 0076 renamed + dropped both), so create
	// that legacy shape by hand rather than leaning on the current store schema.
	exec(`CREATE TABLE auth_users (
		id INTEGER PRIMARY KEY, sub TEXT UNIQUE NOT NULL, username TEXT UNIQUE NOT NULL,
		hash TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL,
		linked_to_sub TEXT, cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0)`)
	exec(`INSERT INTO auth_users(sub, username, hash, name, created_at)
		VALUES('github:alice','alice','h','Alice','2026-01-01T00:00:00Z')`)

	// session_log → runed.db (straight copy)
	exec(`INSERT INTO session_log(group_folder, session_id, started_at, message_count)
		VALUES('main','sess-1','2026-01-02T00:00:00Z',7)`)

	// secrets: routd OWNS it now → copied to routd.db.
	exec(`INSERT INTO secrets(scope_kind, scope_id, key, value, created_at)
		VALUES('folder','main','API_KEY','v2:cipherbytes','2026-01-01T00:00:00Z')`)
	// A pre-split monolith carries the TABLE secret_use_log used to be, which
	// nothing creates any more — so the fixture builds it, then fills it. The
	// entity of the same name is what store.Open made, and only one of the two
	// can hold the name.
	exec(`DROP TABLE secret_use_log`)
	exec(`CREATE TABLE secret_use_log (
	  ts TEXT NOT NULL, spawn_id TEXT NOT NULL DEFAULT '',
	  caller_sub TEXT NOT NULL DEFAULT '', folder TEXT NOT NULL DEFAULT '',
	  tool TEXT NOT NULL, key TEXT NOT NULL, scope TEXT NOT NULL,
	  status TEXT NOT NULL, latency_ms INTEGER NOT NULL DEFAULT 0)`)
	exec(`INSERT INTO secret_use_log(ts, tool, key, scope, status)
		VALUES('2026-01-01T00:00:00Z','get_secret','API_KEY','folder','ok')`)

	// scheduled_tasks + task_run_logs: routd OWNS these now → copied to routd.db.
	exec(`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode)
		VALUES('task-1','main','web:main','daily','0 9 * * *','2026-02-01T09:00:00Z','active','2026-01-01T00:00:00Z','group')`)
	exec(`INSERT INTO task_run_logs(task_id, run_at, duration_ms, status)
		VALUES('task-1','2026-01-02T09:00:00Z',12,'ok')`)

	// pane_sessions: routd OWNS this now → copied to routd.db. Same shape as
	// secret_use_log above — the monolith's TABLE, rebuilt over the entity that
	// replaced it.
	exec(`DROP TABLE pane_sessions`)
	exec(`CREATE TABLE pane_sessions (
	  team_id TEXT NOT NULL, user_id TEXT NOT NULL, thread_ts TEXT NOT NULL,
	  channel_id TEXT NOT NULL, context_jid TEXT, opened_at TEXT NOT NULL,
	  PRIMARY KEY (team_id, user_id, thread_ts))`)
	exec(`INSERT INTO pane_sessions(team_id, user_id, thread_ts, channel_id, context_jid, opened_at)
		VALUES('T1','U99','1700.1','D0XY','slack:T1/channel/C42','2026-01-01T00:00:00Z')`)

	// onboarding + invites + onboarding_gates: onbod OWNS these now → copied to
	// onbod.db. Same shape as secret_use_log and pane_sessions above — the
	// monolith's TABLES, rebuilt over the entities that replaced them, because
	// the split tool's source IS a pre-split file and a fixture that was not one
	// would prove nothing about it. onbod.db's own move is a second step
	// (`arizuko migrate-onbod`), which is why this hands over columns.
	exec(`DROP TABLE onboarding`)
	exec(`CREATE TABLE onboarding (
	  jid TEXT PRIMARY KEY, status TEXT NOT NULL, prompted_at TEXT,
	  created TEXT NOT NULL, token_ref TEXT, token_expires TEXT,
	  user_sub TEXT, gate TEXT, queued_at TEXT, admitted_at TEXT)`)
	exec(`DROP TABLE invites`)
	exec(`CREATE TABLE invites (
	  ref TEXT PRIMARY KEY, target_glob TEXT NOT NULL, issued_by_sub TEXT NOT NULL,
	  issued_at TEXT NOT NULL, expires_at TEXT,
	  max_uses INTEGER NOT NULL DEFAULT 1, used_count INTEGER NOT NULL DEFAULT 0)`)
	exec(`DROP TABLE onboarding_gates`)
	exec(`CREATE TABLE onboarding_gates (
	  gate TEXT PRIMARY KEY, limit_per_day INTEGER NOT NULL,
	  enabled INTEGER NOT NULL DEFAULT 1)`)
	exec(`INSERT INTO onboarding(jid, status, created, user_sub, gate, queued_at, admitted_at)
		VALUES('tg:1','approved','2026-01-01T00:00:00Z','github:alice','*','2026-01-01T00:00:00Z','2026-01-01T01:00:00Z')`)
	// messages.db is opened via store.Open above, which already runs the I1
	// hash-at-rest migration — invites is ref-shaped here, not token-shaped.
	exec(`INSERT INTO invites(ref, target_glob, issued_by_sub, issued_at, max_uses, used_count)
		VALUES(?,'main/','cli','2026-01-01T00:00:00Z',5,2)`, store.TokenRef("inv-tok"))
	exec(`INSERT INTO onboarding_gates(gate, limit_per_day, enabled) VALUES('*',10,1)`)

	// proxyd_routes + audit_log: routd OWNS both (migrations 0015/0016) → copied to
	// routd.db. These moved here when routd's boot-time legacy copiers were deleted;
	// migrate-split is now the ONLY path that carries them off the monolith.
	exec(`INSERT INTO proxyd_routes(path, backend, auth, gated_by, preserve_headers, strip_prefix, redirect_to)
		VALUES('/panel/','http://up','user','','[]',0,'')`)
	// A pre-split monolith carries the TABLE audit_log used to be, which nothing
	// creates any more — so the fixture builds it, then fills it. The entity of
	// the same name is what store.Open made, and only one of the two can hold
	// the name.
	exec(`DROP TABLE audit_log`)
	exec(`CREATE TABLE audit_log (
	  id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL,
	  category TEXT NOT NULL, action TEXT NOT NULL, actor TEXT NOT NULL,
	  actor_sub TEXT, resource TEXT, scope TEXT, surface TEXT, params_summary TEXT,
	  outcome TEXT NOT NULL, error_msg TEXT, duration_ms INTEGER, turn_id TEXT,
	  folder TEXT, instance TEXT, request_id TEXT, source_ip TEXT)`)
	exec(`INSERT INTO audit_log(id, created_at, category, action, actor, outcome)
		VALUES(4242,'2026-01-01T00:00:00Z','authz','acl.add','system','ok')`)

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

	rdb, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()
	udb, err := runed.Create(storeDir)
	if err != nil {
		t.Fatalf("runed.Open: %v", err)
	}
	defer udb.Close()
	r, u := rdb.SQL(), udb.SQL()

	// straight-copy counts
	for tbl, want := range map[string]int{
		"groups": 1, "messages": 2, "chats": 1, "routes": 1, "sessions": 1,
		"route_tokens": 1, "web_routes": 1,
		// network_rules: routd seeds 2 base rows (folder='') + our 1 → 3.
		"network_rules": 3, "chat_reply_state": 1,
		// cost_log_archive: 2 same-(folder,model) rows BOTH survive (synthetic
		// per-row turn_id).
		"system_messages": 1, "cost_log_archive": 2,
		// user_profiles: routd.db owns it → copied (split onbod reads it cross-DB).
		"user_profiles": 1,
		// acl (5/33 unified evaluator): 1 seeded folder:main row + role:operator's
		// root grant + role:member's 12 messaging-verb rows + the 22 skip-approval
		// rows that keep those verbs and the always-mounted reads out of a folder's
		// blanket hold = 36. No per-tier bundles, no backfill containment rows —
		// auth.Authorize reads the grant scope on the actual target. acl_membership:
		// the test's 1 seeded edge + the migrate-time folder:main→role:member floor
		// assignment = 2.
		"acl": 36, "acl_membership": 2,
		// secrets: routd OWNS them now → copied (1 row each).
		"secrets": 1, "secret_use_log": 1,
		// scheduled_tasks + task_run_logs: routd OWNS them now → copied (1 row each).
		"scheduled_tasks": 1, "task_run_logs": 1,
		// pane_sessions: routd OWNS it now → copied (1 row).
		"pane_sessions": 1,
		// proxyd_routes + audit_log: routd OWNS both → copied (1 row each). routd.Open
		// no longer backfills them, so a miss here means the rows are stranded.
		"proxyd_routes": 1, "audit_log": 1,
	} {
		if got := count(t, r, tbl); got != want {
			t.Errorf("routd.%s: got %d rows, want %d", tbl, got, want)
		}
	}
	if got := count(t, u, "session_log"); got != 1 {
		t.Errorf("runed.session_log: got %d rows, want 1", got)
	}

	// messages: every attribute travelled, and the delivery status is the ABSENCE
	// of a delivery item — a sent row owes nothing, so it is in neither feed.
	rst := store.New(r)
	m1, ok := rst.MessageByID("tg:1", "m1")
	if !ok {
		t.Fatal("read routd.messages m1: the row did not travel")
	}
	if m1.Content != "hi" || m1.TurnID != "t1" || m1.PlatformID != "pid1" || m1.ChatName != "Chat A" {
		t.Errorf("messages m1 payload wrong: %+v", m1)
	}
	if dead, _ := rst.DeadLetterByID(context.Background(), "m1"); dead.ID != "" {
		t.Errorf("a sent row carries a dead-letter item: %+v", dead)
	}

	// The errored flag is ABSENT, not "0": the source had no such column, and a
	// flag an item does not carry is the only way this model spells false.
	if flagged, ferr := store.New(r).ErroredChatJIDs(); ferr != nil || len(flagged) != 0 {
		t.Errorf("errored chats after the split = %v (err %v), want none", flagged, ferr)
	}

	// transform: system_messages remapped correctly. The monolith's unused
	// `attrs` column travels as an attribute nothing reads (hotColumnAlias). The three
	// values come back through the reader the prompt build calls; the folder and
	// the queue instant are the item's COORDINATE, so they are read off the key
	// the migration wrote.
	queued, qerr := store.New(r).QueuedSystemMessagesTx(context.Background(), nil, "main")
	if qerr != nil {
		t.Fatalf("read routd.system_messages: %v", qerr)
	}
	if len(queued) != 1 {
		t.Fatalf("routd.system_messages holds %d events for main, want 1", len(queued))
	}
	if queued[0].Source != "system" || queued[0].Kind != "migrate" || queued[0].Body != "hello" {
		t.Errorf("system_messages remap wrong: %+v", queued[0])
	}
	var sortKey string
	if err := r.QueryRow(`SELECT sort FROM system_messages LIMIT 1`).Scan(&sortKey); err != nil {
		t.Fatalf("read the system_messages coordinate: %v", err)
	}
	wantKey := "main\x1f" + core.TS(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)) + "-00000001"
	if sortKey != wantKey {
		t.Errorf("system_messages coordinate = %q, want %q — the folder, the queue instant "+
			"at core.TS's width, and the legacy id that keeps two events in one instant apart",
			sortKey, wantKey)
	}

	// transform: the legacy spend lands in cost_log_archive, remapped; turn_id
	// synthesized 'mig-'||rowid (UNIQUE per source row so INSERT OR IGNORE does not
	// collapse same-(folder,model) history).
	var cf, cTurn, cModel, cRecorded string
	var cin, cout, cents int
	if err := r.QueryRow(
		`SELECT folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at FROM cost_log_archive LIMIT 1`).
		Scan(&cf, &cTurn, &cModel, &cin, &cout, &cents, &cRecorded); err != nil {
		t.Fatalf("read routd.cost_log_archive: %v", err)
	}
	if cf != "main" || !strings.HasPrefix(cTurn, "mig-") || cModel != "claude" || cRecorded == "" {
		t.Errorf("cost_log_archive remap wrong: folder=%q turn=%q model=%q in=%d out=%d cents=%d at=%q",
			cf, cTurn, cModel, cin, cout, cents, cRecorded)
	}
	// #5: both same-(folder,model) rows survive (no PK collapse).
	var costN int
	if err := r.QueryRow(`SELECT COUNT(*) FROM cost_log_archive`).Scan(&costN); err != nil {
		t.Fatalf("count routd.cost_log_archive: %v", err)
	}
	if costN != 2 {
		t.Errorf("cost_log_archive rows = %d, want 2 (turn_id collapse dropped history)", costN)
	}
	// #6: user_profiles copied to routd.db (split onbod reads it cross-DB), read
	// back through the method onbod itself calls.
	auName, known, err := store.New(r).Username("github:alice")
	if err != nil || !known {
		t.Fatalf("user_profiles not copied to routd.db: known=%v err=%v", known, err)
	}
	if auName != "alice" {
		t.Errorf("user_profiles.username = %q, want alice", auName)
	}
	// The password digest the monolith carried does NOT travel: nothing reads it,
	// and a secret carried into a document by default is one nothing scrubs.
	profiles, err := store.New(r).ProfileNames(t.Context())
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles = %v, err = %v, want one", profiles, err)
	}
	var carried int
	if err := r.QueryRow(`SELECT COUNT(*) FROM user_profiles WHERE attrs LIKE '%hash%'`).
		Scan(&carried); err != nil {
		t.Fatalf("look for a carried digest: %v", err)
	}
	if carried != 0 {
		t.Errorf("%d profile items carry the monolith's password digest", carried)
	}

	// acl, secrets and scheduled_tasks are entities in routd.db, read back
	// through the methods routd itself calls — an import that wrote the wrong
	// attribute name would report its counts happily, and only a read through
	// the real methods catches it.
	// Scope to the migrated legacy row specifically — the 5/33 floor adds other
	// role rows that would otherwise shadow it.
	aclRows, err := rst.ListACL("folder:main")
	if err != nil {
		t.Fatalf("read routd.acl: %v", err)
	}
	var aclFound bool
	for _, row := range aclRows {
		if row.Action != "mcp:send" {
			continue
		}
		aclFound = true
		if row.Scope != "main" || row.Effect != "allow" {
			t.Errorf("acl row wrong: principal=%q action=%q scope=%q effect=%q",
				row.Principal, row.Action, row.Scope, row.Effect)
		}
	}
	if !aclFound {
		t.Errorf("acl rows for folder:main = %v, want the migrated mcp:send grant", aclRows)
	}

	// secrets: the encrypted `value` bytes move verbatim — the same SECRETS_KEY
	// decrypts on the routd side, so the read is of the sealed text itself.
	secs, err := rst.ListSecrets(store.SecretScopeFolder, "main")
	if err != nil {
		t.Fatalf("read routd.secrets: %v", err)
	}
	if len(secs) != 1 || secs[0].Key != "API_KEY" {
		t.Fatalf("secrets for main = %v, want one API_KEY", secs)
	}

	tasks := rst.ListTasks("", true)
	var task core.Task
	for _, tk := range tasks {
		if tk.ID == "task-1" {
			task = tk
		}
	}
	if task.ID == "" {
		t.Fatalf("scheduled_tasks = %v, want the migrated task-1", tasks)
	}
	if task.Owner != "main" || task.Cron != "0 9 * * *" || task.Status != "active" {
		t.Errorf("scheduled_tasks row wrong: owner=%q cron=%q status=%q",
			task.Owner, task.Cron, task.Status)
	}
	runs := store.New(r).TaskRunLogs("task-1", 10)
	if len(runs) != 1 {
		t.Fatalf("task_run_logs = %v, want the migrated run", runs)
	}
	if runs[0].Status != "ok" {
		t.Errorf("task_run_logs status = %q want ok", runs[0].Status)
	}

	// pane_sessions: an entity in routd.db (routd OWNS it now), read back
	// through the method paneHints itself calls — an import that wrote the wrong
	// attribute name would report its count happily and only this catches it.
	pane, ok := store.New(r).PaneInChannel("D0XY")
	if !ok {
		t.Fatalf("routd.pane_sessions: D0XY names no pane")
	}
	if pane.ContextJID != "slack:T1/channel/C42" {
		t.Errorf("pane context_jid = %q want slack:T1/channel/C42", pane.ContextJID)
	}
	if pane.TeamID != "T1" || pane.UserID != "U99" || pane.ThreadTS != "1700.1" {
		t.Errorf("pane triple = %s/%s/%s want T1/U99/1700.1",
			pane.TeamID, pane.UserID, pane.ThreadTS)
	}

	// onboarding + invites + onboarding_gates: copied to onbod.db (onbod OWNS them
	// now). routd.db must NOT carry invites; onbod.db must round-trip the rows.
	var invTbl string
	if err := r.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='invites'`).Scan(&invTbl); err != sql.ErrNoRows {
		t.Errorf("routd.db must not contain an `invites` table (federated to onbod), found %q (err=%v)", invTbl, err)
	}
	odb, err := db_utils.Open(filepath.Join(storeDir, "onbod.db"))
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
	// The invite is an ITEM in onbod.db now, so it is read back through the
	// method onbod itself calls — which is also what proves the copy wrote the
	// attributes the reader looks for and not just some document.
	inv, ok := inviteByRef(t, odb, store.TokenRef("inv-tok"))
	if !ok {
		t.Fatalf("the copied invite is not in onbod.db")
	}
	if inv.TargetGlob != "main/" || inv.MaxUses != 5 || inv.UsedCount != 2 {
		t.Errorf("invite wrong: glob=%q max=%d used=%d",
			inv.TargetGlob, inv.MaxUses, inv.UsedCount)
	}

	// The split carries ranked search with the rows. routd.Create builds the
	// index and its triggers over the message entity before copyHotEntities
	// writes a single item, so each row is indexed as it lands and no rebuild
	// runs here. A copy that landed the rows without the index would leave a
	// migrated instance whose find_messages answers nothing, silently.
	hits, ferr := rdb.FindMessages("hi", "", "", "", 10)
	if ferr != nil {
		t.Fatalf("FindMessages after the split: %v", ferr)
	}
	if len(hits) != 1 || hits[0].ChatJID != "tg:1" || !strings.Contains(hits[0].Content, "«hi»") {
		t.Errorf("the split left the index empty or wrong: %+v", hits)
	}

	rdb.Close()
	udb.Close()

	// idempotent: a second run must not error and must not duplicate.
	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit (re-run): %v", err)
	}
	rdb2, err := routd.Create(storeDir)
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
	// The seeded monolith carries the TABLE audit_log used to be, so it is opened
	// the way `arizuko migrate-split` opens it — the pool, not store.Open, whose
	// job is to CREATE a monolith rather than to reopen one.
	s, err := db_utils.Open(filepath.Join(storeDir, "messages.db"))
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	// sender_name + reply_to_id are plain `TEXT` (nullable) at the migrated head
	// AND scanMessages reads them into plain `string` — the exact pair routd's own
	// inserts force to '' (routd/db.go FireProactive) precisely to dodge this.
	// turn_id/platform_id are nullable too (scanned via NullString). NULL all four
	// — the legacy shape that bricked the split poll loop. (source/topic/routed_to
	// are NOT NULL at head — store migration 0023 rebuilt them — so a verbatim NULL
	// can't occur there; COALESCE on them is harmless defence.)
	if _, err := s.Exec(
		`INSERT INTO messages(id, chat_jid, sender, sender_name, content, timestamp,
			reply_to_id, turn_id, platform_id)
		 VALUES('mnull','tg:1','carol',NULL,'legacy','2026-01-02T00:03:00Z',NULL,NULL,NULL)`); err != nil {
		t.Fatalf("seed NULL message: %v", err)
	}
	s.Close()

	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit: %v", err)
	}

	rdb, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()

	// 1. A NULL column arrives as an ABSENT attribute, which every reader renders
	// as "" — the same guarantee the COALESCE gave, through the model's own null.
	mnull, ok := store.New(rdb.SQL()).MessageByID("tg:1", "mnull")
	if !ok {
		t.Fatal("read routd.messages mnull: the row did not travel")
	}
	for label, got := range map[string]string{
		"sender_name": mnull.Name, "reply_to_id": mnull.ReplyToID, "source": mnull.Source,
		"topic": mnull.Topic, "routed_to": mnull.RoutedTo, "chat_name": mnull.ChatName,
	} {
		if got != "" {
			t.Errorf("routd.messages.%s = %q, want the empty string a NULL becomes", label, got)
		}
	}
	if mnull.Verb != "message" {
		t.Errorf("routd.messages.verb = %q, want 'message'", mnull.Verb)
	}
	// `sent` is the absence of a delivery item, so what says the row is sent is
	// that neither feed holds it.
	if dead, _ := store.New(rdb.SQL()).DeadLetterByID(context.Background(), "mnull"); dead.ID != "" {
		t.Errorf("a sent row carries a dead-letter item: %+v", dead)
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

// schemaOf returns db's shape as object name -> column list (tables) or "index"
// (indexes), skipping SQLite's implicit autoindexes. Comparing this instead of
// sqlite_master.sql text tolerates formatting differences between a bootstrap
// DDL and the migration chain that ends in the same shape.
func schemaOf(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.Query(`SELECT type, name FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' AND name <> 'migrations'`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	var objs [][2]string
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		objs = append(objs, [2]string{typ, name})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	rows.Close()
	for _, o := range objs {
		typ, name := o[0], o[1]
		if typ != "table" {
			out[typ+":"+name] = typ
			continue
		}
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, name)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", name, err)
		}
		var list []string
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				t.Fatalf("scan column of %s: %v", name, err)
			}
			list = append(list, c)
		}
		cols.Close()
		out["table:"+name] = strings.Join(list, ",")
	}
	return out
}

// TestOnbodBootstrapBuildsExactlyTheDeclaredEntities is what remains to guard
// once onbod has no chain: bootstrapOnbod is a SECOND caller of the three
// declarations onbod's own boot calls, and the two are in different packages. A
// declaration added for onbod and not reached here leaves migrate-split producing
// a database missing one of onbod's tables.
//
// The comparison is against the declarations themselves, not against a list
// written here — a list would be the third copy.
func TestOnbodBootstrapBuildsExactlyTheDeclaredEntities(t *testing.T) {
	bootstrapped := dbtest.Open(t)
	if err := bootstrapOnbod(bootstrapped); err != nil {
		t.Fatalf("bootstrapOnbod: %v", err)
	}
	declared := dbtest.Open(t)
	obj := sqlstore.New(declared)
	if err := audit.EnsureEntities(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureEntities(t.Context(), obj, store.OwnerOnbod); err != nil {
		t.Fatal(err)
	}
	if err := resreg.EnsureSubsystem(t.Context(), obj, obj, resreg.SubsystemOnbod); err != nil {
		t.Fatal(err)
	}

	want, got := schemaOf(t, declared), schemaOf(t, bootstrapped)
	for k, wantCols := range want {
		gotCols, ok := got[k]
		if !ok {
			t.Errorf("bootstrapOnbod is missing %s, which onbod's own boot declares — "+
				"migrate-split would hand onbod a database without it", k)
			continue
		}
		if gotCols != wantCols {
			t.Errorf("%s shape differs:\n  bootstrapOnbod: %s\n  declarations:   %s", k, gotCols, wantCols)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("bootstrapOnbod creates %s that onbod's own boot does not", k)
		}
	}
}

// TestMigrateSplitLeavesOnbodBootable replays onbod's next boot over the database
// migrate-split produced. The boot builds the same entities again, so it must be
// a no-op that moves no row — the Z3b failure was a boot that reshaped the
// freshly copied invites and left onbod refusing to start. Run twice, because a
// boot is not the last one.
//
// The `_legacy` assertion is what says the split produces neither table. It is
// the check the two deleted Go backfills stood on: nothing in the chain reshapes
// a table aside any more, so there is nothing left to carry.
func TestMigrateSplitLeavesOnbodBootable(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	seedMessagesDB(t, storeDir)
	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit: %v", err)
	}
	db, err := db_utils.Open(filepath.Join(storeDir, "onbod.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for boot := 1; boot <= 2; boot++ {
		if err := bootstrapOnbod(db); err != nil {
			t.Fatalf("boot %d: schema: %v", boot, err)
		}
		for _, legacy := range []string{"invites_legacy", "onboarding_legacy"} {
			exists, eerr := db_utils.TableExists(db, legacy)
			if eerr != nil {
				t.Fatal(eerr)
			}
			if exists {
				t.Errorf("boot %d left %s behind — something reshaped the copy", boot, legacy)
			}
		}
		if _, ok := inviteByRef(t, db, store.TokenRef("inv-tok")); !ok {
			t.Fatalf("boot %d: the copied invite is gone", boot)
		}
	}
}

// inviteByRef reads one invite out of a database through the reader onbod uses.
// An invite is an item, so there is no `WHERE ref = ?` to run: the ref is the
// coordinate, and the read that finds it is the one production makes.
func inviteByRef(t *testing.T, db *sql.DB, ref string) (store.Invite, bool) {
	t.Helper()
	rows, err := store.New(db).ListInvites("")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	for _, inv := range rows {
		if inv.Ref == ref {
			return inv, true
		}
	}
	return store.Invite{}, false
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
	// The seeded monolith carries the TABLE audit_log used to be, so it is opened
	// the way `arizuko migrate-split` opens it — the pool, not store.Open, whose
	// job is to CREATE a monolith rather than to reopen one.
	s, err := db_utils.Open(filepath.Join(storeDir, "messages.db"))
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	// Pin one conn so PRAGMA foreign_keys=OFF + the orphan INSERT share it; the
	// monolith historically had FK off when these orphans accrued.
	s.SetMaxOpenConns(1)
	if _, err := s.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := s.Exec(
		`INSERT INTO task_run_logs(task_id, run_at, status) VALUES(999999,'2026-01-05T00:00:00Z','ok')`); err != nil {
		t.Fatalf("seed orphan run log: %v", err)
	}
	s.Close()

	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit must tolerate orphan task_run_logs (got: %v)", err)
	}
	rdb, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()
	if got := count(t, rdb.SQL(), "task_run_logs"); got != 2 {
		t.Errorf("routd.task_run_logs: got %d, want 2 (seeded + orphan)", got)
	}
}

// The cutover is a bulk SQL copy, and routd.Create has already run the
// migration that makes messages.timestamp, chats.agent_cursor and
// sessions.observed_cursor fixed width — so the sequence never revisits what
// the copy lands. A legacy monolith holds a whole-second cursor and a
// fractional message inside that same second; `.` is 0x2E and `Z` is 0x5A, so
// copied verbatim the LATER message sorts BEHIND the cursor and the agent never
// sees it (BUGS L219, L97).
func TestMigrateSplitLandsTimestampsFixedWidth(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	seedMessagesDB(t, storeDir)

	// The seeded monolith carries the TABLE audit_log used to be, so it is opened
	// the way `arizuko migrate-split` opens it — the pool, not store.Open, whose
	// job is to CREATE a monolith rather than to reopen one.
	s, err := db_utils.Open(filepath.Join(storeDir, "messages.db"))
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	// tg:1's agent_cursor is the whole second 2026-01-02T00:01:00Z. This message
	// is 882ms LATER in that same second.
	const later = "2026-01-02T00:01:00.882458214Z"
	if _, err := s.Exec(
		`INSERT INTO messages(id, chat_jid, sender, content, timestamp)
		 VALUES('mfrac','tg:1','alice','the newest message',?)`, later); err != nil {
		t.Fatalf("seed fractional message: %v", err)
	}
	if _, err := s.Exec(
		`UPDATE sessions SET observed_cursor='2026-01-02T00:01:00Z' WHERE group_folder='main'`); err != nil {
		t.Fatalf("seed observed_cursor: %v", err)
	}
	s.Close()

	if err := migrateSplit(storeDir, false); err != nil {
		t.Fatalf("migrateSplit: %v", err)
	}
	rdb, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer rdb.Close()

	// The message is later in time, so it has to sort after the cursor — the
	// whole point of the fixed width. The cursor is an attribute now, so the
	// probe reads it back through the entity and compares in Go, which is what
	// the poll loop's own read does.
	cursor, err := store.New(rdb.SQL()).AgentCursor("tg:1")
	if err != nil {
		t.Fatalf("visibility probe: %v", err)
	}
	unfed, err := store.New(rdb.SQL()).NewMessagesForChat(context.Background(), "tg:1", cursor, 0)
	if err != nil {
		t.Fatalf("visibility probe: %v", err)
	}
	seen := false
	for _, m := range unfed {
		if m.ID == "mfrac" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("the copied message sorts behind the copied cursor %q; the agent never "+
			"sees it. the feed holds %+v", cursor, unfed)
	}

	// Every value, not just the pair above: the message column by SQL, and the
	// two cursors through the entity that now holds them.
	width := len(core.TS(time.Now()))
	mixed := 0
	rows, err := rdb.SQL().Query(`SELECT sort FROM messages`)
	if err != nil {
		t.Fatalf("width probe messages: %v", err)
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		// A position LEADS with a fixed-width instant and carries the id after
		// it, so what has to hold is the lead, not the whole key.
		if ts, _, ok := store.MsgCoord(key); !ok || len(ts) != width {
			mixed++
		}
	}
	rows.Close()
	if mixed != 0 {
		t.Errorf("messages: %d copied positions do not lead with a fixed-width instant", mixed)
	}
	st := store.New(rdb.SQL())
	// The dispatch cursor is a whole message POSITION, so what has to be fixed
	// width is the instant it leads with — the id after it is 3 to 60 bytes.
	got, _ := st.AgentCursor("tg:1")
	if ts, _, ok := store.MsgCoord(got); !ok || len(ts) != width {
		t.Errorf("chats.agent_cursor = %q, which does not lead with a %d-byte instant",
			got, width)
	}
	if line, ok := st.TopicLineage("main", ""); !ok || len(line.ObservedCursor) != width {
		t.Errorf("sessions.observed_cursor = %q, want %d bytes", line.ObservedCursor, width)
	}
}
