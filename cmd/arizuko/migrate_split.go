package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
)

// cmdMigrateSplit populates routd.db + runed.db + auth.db from an existing
// instance's messages.db for the CUTOVER_SPLIT topology. messages.db stays
// ALIVE: dashd keeps writing the orphan tables. ACL moved to routd's own DB
// (routd 0007), secrets to routd's own DB (routd 0008), tasks to routd's own DB
// (routd 0009), pane_sessions to routd's own DB (routd 0010), and identity to
// authd's auth.db (authd 0004), so acl/acl_membership + secrets/secret_use_log +
// scheduled_tasks/task_run_logs + pane_sessions +
// identities/identity_claims/identity_codes are COPIED, not left. So this
// migrator COPIES the conversation/routing/run/acl/secrets/tasks/pane state into
// the new DBs and identity into auth.db; the orphan tables stay where they are.
// It is idempotent (INSERT OR IGNORE on primary keys) and safe to run on a copy.
func cmdMigrateSplit(args []string) {
	// flexParse lets --dry-run sit on either side of <instance>; it requires
	// EXACTLY one positional so a typo'd flag errors instead of being silently
	// treated as the instance name.
	fs := flag.NewFlagSet("migrate-split", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report source row counts; do not write")
	if err := flexParse(fs, args); err != nil || fs.NArg() != 1 {
		fmt.Println("usage: arizuko migrate-split <instance> [--dry-run]")
		os.Exit(1)
	}
	storeDir := filepath.Join(mustInstanceDir(fs.Arg(0)), "store")
	if err := migrateSplit(storeDir, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// copySpec is one source-table → dest-table copy. cols are the DESTINATION
// columns; sel is the matching SELECT expression list against the ATTACHed
// source (msg.<srcTable>). A straight copy lists the shared columns on both
// sides; a transform remaps differing column names / supplies defaults for
// columns the source lacks. The INSERT is always INSERT OR IGNORE so a re-run
// against an already-populated dest is a no-op on existing PKs.
type copySpec struct {
	dst     string // destination table
	src     string // source table in messages.db (ATTACHed as `msg`)
	cols    string // destination column list
	sel     string // SELECT expression list against msg.<src>
	transfm bool    // true → column names/shape differ (logged distinctly)
}

// routdSpecs map messages.db → routd.db. Column lists were derived from the
// live schemas (store/migrations, routd/migrations), NOT the design brief —
// see migrateSplit's summary for the deltas found.
var routdSpecs = []copySpec{
	// straight copies (shared column intersection)
	{dst: "groups", src: "groups",
		cols: "folder, added_at, container_config, updated_at, product, cost_cap_cents_per_day, open, observe_window_messages, observe_window_chars, model",
		sel:  "folder, added_at, container_config, updated_at, product, cost_cap_cents_per_day, open, observe_window_messages, observe_window_chars, model"},
	// messages.db chats has no `errored` column (dropped in store 0023);
	// routd.db chats adds it → defaults to 0.
	{dst: "chats", src: "chats",
		cols: "jid, agent_cursor, sticky_group, sticky_topic, is_group",
		sel:  "jid, agent_cursor, sticky_group, sticky_topic, is_group"},
	// messages.db messages has `errored` (store 0030); routd.db lacks it → dropped.
	// transform: COALESCE every nullable column to routd's runtime default. routd's
	// own inserts use '' (FireProactive/PutMessage), but legacy gated rows carry
	// NULLs (sender_name/reply_to_id/source were `TEXT` nullable; the rest gained
	// NOT NULL DEFAULTs in later migrations but pre-existing rows kept NULL).
	// scanMessages scans most of these into plain `string` → a NULL aborts every
	// routd poll/read (Scan error). Default each to what a fresh routd insert
	// writes so reads never hit NULL.
	{dst: "messages", src: "messages", transfm: true,
		cols: "id, chat_jid, sender, sender_name, content, timestamp, is_from_me, is_bot_message, forwarded_from, reply_to_id, reply_to_text, reply_to_sender, topic, routed_to, verb, attachments, source, is_observed, turn_id, status, platform_id, chat_name",
		sel: "id, chat_jid, sender, COALESCE(sender_name,''), content, timestamp, " +
			"COALESCE(is_from_me,0), COALESCE(is_bot_message,0), COALESCE(forwarded_from,''), " +
			"COALESCE(reply_to_id,''), COALESCE(reply_to_text,''), COALESCE(reply_to_sender,''), " +
			"COALESCE(topic,''), COALESCE(routed_to,''), COALESCE(verb,'message'), " +
			"COALESCE(attachments,''), COALESCE(source,''), COALESCE(is_observed,0), " +
			"COALESCE(turn_id,''), COALESCE(status,'sent'), COALESCE(platform_id,''), COALESCE(chat_name,'')"},
	{dst: "routes", src: "routes",
		cols: "id, seq, match, target, observe_window_messages, observe_window_chars",
		sel:  "id, seq, match, target, observe_window_messages, observe_window_chars"},
	{dst: "sessions", src: "sessions",
		cols: "group_folder, topic, session_id, parent_topic, forked_at, observed_cursor",
		sel:  "group_folder, topic, session_id, parent_topic, forked_at, observed_cursor"},
	{dst: "route_tokens", src: "route_tokens",
		cols: "token_hash, jid, owner_folder, created_at",
		sel:  "token_hash, jid, owner_folder, created_at"},
	{dst: "turn_results", src: "turn_results",
		cols: "folder, turn_id, session_id, status, recorded_at",
		sel:  "folder, turn_id, session_id, status, recorded_at"},
	{dst: "web_routes", src: "web_routes",
		cols: "path_prefix, access, redirect_to, folder, created_at",
		sel:  "path_prefix, access, redirect_to, folder, created_at"},
	{dst: "network_rules", src: "network_rules",
		cols: "folder, target, created_at, created_by",
		sel:  "folder, target, created_at, created_by"},
	{dst: "chat_reply_state", src: "chat_reply_state",
		cols: "jid, topic, last_reply_id, engaged_until, engaged_folder",
		sel:  "jid, topic, last_reply_id, engaged_until, engaged_folder"},
	{dst: "group_watchers", src: "group_watchers",
		cols: "observer, source",
		sel:  "observer, source"},
	// acl + acl_membership: routd now OWNS these (routd migration 0007 mirrors
	// store 0052). Straight copies — identical schema both sides.
	{dst: "acl", src: "acl",
		cols: "principal, action, scope, effect, params, predicate, granted_by, granted_at",
		sel:  "principal, action, scope, effect, params, predicate, granted_by, granted_at"},
	{dst: "acl_membership", src: "acl_membership",
		cols: "child, parent, added_by, added_at",
		sel:  "child, parent, added_by, added_at"},
	// secrets + secret_use_log: routd now OWNS these (routd migration 0008
	// mirrors store 0034/0047/0048 final shape). Straight copies — encrypted
	// `value` bytes move verbatim (same SECRETS_KEY decrypts on the routd side).
	{dst: "secrets", src: "secrets",
		cols: "scope_kind, scope_id, key, value, created_at",
		sel:  "scope_kind, scope_id, key, value, created_at"},
	{dst: "secret_use_log", src: "secret_use_log",
		cols: "ts, spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms",
		sel:  "ts, spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms"},
	// scheduled_tasks + task_run_logs: routd now OWNS these (routd migration 0009
	// mirrors store 0001/0011 final shape). Straight copies — identical schema
	// both sides. task_run_logs.id is AUTOINCREMENT but copied verbatim so the
	// FK to scheduled_tasks(id) stays intact.
	{dst: "scheduled_tasks", src: "scheduled_tasks",
		cols: "id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode",
		sel:  "id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode"},
	{dst: "task_run_logs", src: "task_run_logs",
		cols: "id, task_id, run_at, duration_ms, status, result, error",
		sel:  "id, task_id, run_at, duration_ms, status, result, error"},
	// pane_sessions: routd now OWNS this (routd migration 0010 mirrors store 0056
	// final shape). Straight copy — identical schema both sides. This was the LAST
	// messages.db sibling-read in routd; after it routd opens NO sibling DB.
	{dst: "pane_sessions", src: "pane_sessions",
		cols: "team_id, user_id, thread_ts, channel_id, context_jid, opened_at, last_status_at",
		sel:  "team_id, user_id, thread_ts, channel_id, context_jid, opened_at, last_status_at"},
	// auth_users: routd.db OWNS it (routd migration 0011; cost_log.user_sub references
	// it). Split onbod reads+writes it cross-DB on routd.db (xdb), so it MUST be copied
	// — left an orphan, every existing user vanishes from onboarding / world-create.
	{dst: "auth_users", src: "auth_users",
		cols: "id, sub, username, hash, name, created_at, linked_to_sub, cost_cap_cents_per_day",
		sel:  "id, sub, username, hash, name, created_at, linked_to_sub, cost_cap_cents_per_day"},

	// transforms (schemas differ — explicit column remap)
	// system_messages: group_id→folder, origin→source, event→kind, created_at→created; `attrs` dropped.
	{dst: "system_messages", src: "system_messages", transfm: true,
		cols: "id, folder, source, kind, body, created",
		sel:  "id, group_id, origin, event, body, created_at"},
	// cost_log: messages.db has no turn_id column (routd PK is folder,turn_id,model).
	// Synthesize a UNIQUE turn_id per source row ('mig-'||rowid) — a constant ''
	// would collapse every legacy (folder,model) pair to ONE row under INSERT OR
	// IGNORE, dropping cost history. input_tok→input_tokens, output_tok→output_tokens,
	// cents→cost_cents, ts→recorded_at; user_sub/cache_read/cache_write dropped.
	{dst: "cost_log", src: "cost_log", transfm: true,
		cols: "folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at",
		sel:  "folder, 'mig-'||rowid, model, input_tok, output_tok, cents, ts"},
}

// runedSpecs map messages.db → runed.db. session_log is a straight copy
// (identical columns); spawns/spawn_logs/mcp_tokens have no pre-split source.
var runedSpecs = []copySpec{
	{dst: "session_log", src: "session_log",
		cols: "id, group_folder, session_id, started_at, ended_at, result, error, message_count",
		sel:  "id, group_folder, session_id, started_at, ended_at, result, error, message_count"},
}

// authdSpecs map messages.db → auth.db. authd now OWNS identity (authd migration
// 0004 mirrors store 0035): identities/identity_claims/identity_codes are
// straight copies — identical schema both sides.
var authdSpecs = []copySpec{
	{dst: "identities", src: "identities",
		cols: "id, name, created_at",
		sel:  "id, name, created_at"},
	{dst: "identity_claims", src: "identity_claims",
		cols: "sub, identity_id, claimed_at",
		sel:  "sub, identity_id, claimed_at"},
	{dst: "identity_codes", src: "identity_codes",
		cols: "code, identity_id, expires_at",
		sel:  "code, identity_id, expires_at"},
}

// authdIdentitySchema mirrors authd/migrations/0004-identities.sql so the
// migrator can bootstrap auth.db's identity tables before copying into them
// (authd's migration FS is package-private; this one-shot DDL is the copy-target
// bootstrap, IF NOT EXISTS so it's a no-op when authd already migrated).
const authdIdentitySchema = `
CREATE TABLE IF NOT EXISTS identities (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS identity_claims (
  sub         TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  claimed_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_identity_claims_id ON identity_claims(identity_id);
CREATE TABLE IF NOT EXISTS identity_codes (
  code        TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);`

// onbodSpecs map messages.db → onbod.db. onbod now OWNS the onboarding admission
// state machine + invite links + per-gate limits (onbod migration 0001 mirrors
// store 0009/0023/0024/0027/0071 for onboarding, 0032 for invites, 0029 for
// onboarding_gates). Straight copies — identical schema both sides.
var onbodSpecs = []copySpec{
	{dst: "onboarding", src: "onboarding",
		cols: "jid, status, prompted_at, created, token, token_expires, user_sub, gate, queued_at, admitted_at",
		sel:  "jid, status, prompted_at, created, token, token_expires, user_sub, gate, queued_at, admitted_at"},
	{dst: "invites", src: "invites",
		cols: "token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count",
		sel:  "token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count"},
	{dst: "onboarding_gates", src: "onboarding_gates",
		cols: "gate, limit_per_day, enabled",
		sel:  "gate, limit_per_day, enabled"},
}

// onbodSchema mirrors onbod/migrations/*.sql so the migrator can bootstrap
// onbod.db's owned tables before copying into them (onbod's migration FS is
// package-private — onbod is package main; this one-shot DDL is the copy-target
// bootstrap, IF NOT EXISTS so it's a no-op when onbod already migrated).
const onbodSchema = `
CREATE TABLE IF NOT EXISTS onboarding (
  jid           TEXT PRIMARY KEY,
  status        TEXT NOT NULL,
  prompted_at   TEXT,
  created       TEXT NOT NULL,
  token         TEXT,
  token_expires TEXT,
  user_sub      TEXT,
  gate          TEXT,
  queued_at     TEXT,
  admitted_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_onboarding_token ON onboarding(token);
CREATE TABLE IF NOT EXISTS invites (
  token         TEXT PRIMARY KEY,
  target_glob   TEXT NOT NULL,
  issued_by_sub TEXT NOT NULL,
  issued_at     TEXT NOT NULL,
  expires_at    TEXT,
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS onboarding_gates (
  gate          TEXT PRIMARY KEY,
  limit_per_day INTEGER NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  category        TEXT    NOT NULL,
  action          TEXT    NOT NULL,
  actor           TEXT    NOT NULL,
  actor_sub       TEXT,
  resource        TEXT,
  scope           TEXT,
  surface         TEXT,
  params_summary  TEXT,
  outcome         TEXT    NOT NULL,
  error_msg       TEXT,
  duration_ms     INTEGER,
  turn_id         TEXT,
  folder          TEXT,
  instance        TEXT,
  request_id      TEXT,
  source_ip       TEXT
);`

// orphanTables stay in messages.db post-cutover: dashd owns some (audit_log,
// …); authd's auth.db starts fresh. routd reads NONE of them — every table it
// needs moved to routd.db or federated over HTTP. onboarding/invites/
// onboarding_gates are NOT orphans (onbod OWNS them → onbodSpecs); auth_users is
// NOT an orphan either (routd.db owns it → routdSpecs, read cross-DB by onbod).
// auth_sessions stays — login sessions are ephemeral, re-minted on next login.
// Listed so the summary tells the operator messages.db is NOT retired.
var orphanTables = []string{
	"audit_log", "router_state",
	"proxyd_routes", "config_meta", "cli_audit", "ipc_audit",
	"auth_sessions",
}

func migrateSplit(storeDir string, dryRun bool) error {
	msgPath := filepath.Join(storeDir, "messages.db")
	if _, err := os.Stat(msgPath); err != nil {
		return fmt.Errorf("messages.db not found at %s: %w", msgPath, err)
	}

	// Open destinations via their own Open so migrations run first → all
	// target tables exist before we copy into them.
	rdb, err := routd.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open routd.db: %w", err)
	}
	defer rdb.Close()
	udb, err := runed.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open runed.db: %w", err)
	}
	defer udb.Close()
	// auth.db: authd OWNS identity (authd 0004). Open it and bootstrap the
	// identity schema (IF NOT EXISTS — no-op when authd already migrated) so the
	// copy target exists. authd's migration FS is package-private, hence the
	// inline DDL (authdIdentitySchema mirrors it verbatim).
	adb, err := sql.Open("sqlite", filepath.Join(storeDir, "auth.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		return fmt.Errorf("open auth.db: %w", err)
	}
	defer adb.Close()
	if !dryRun {
		if _, err := adb.Exec(authdIdentitySchema); err != nil {
			return fmt.Errorf("auth.db identity schema: %w", err)
		}
	}
	// onbod.db: onbod OWNS onboarding/invites/onboarding_gates (onbod 0001).
	// Bootstrap the schema (IF NOT EXISTS — no-op when onbod already migrated) so
	// the copy target exists. onbod's migration FS is package-private (package
	// main), hence the inline DDL (onbodSchema mirrors it verbatim).
	odb, err := sql.Open("sqlite", filepath.Join(storeDir, "onbod.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		return fmt.Errorf("open onbod.db: %w", err)
	}
	defer odb.Close()
	if !dryRun {
		if _, err := odb.Exec(onbodSchema); err != nil {
			return fmt.Errorf("onbod.db schema: %w", err)
		}
	}

	fmt.Printf("migrate-split: %s\n", storeDir)
	if dryRun {
		fmt.Println("  (dry-run: source row counts only, no writes)")
	}

	rN, err := copyInto(rdb.SQL(), msgPath, routdSpecs, dryRun)
	if err != nil {
		return fmt.Errorf("routd.db: %w", err)
	}
	uN, err := copyInto(udb.SQL(), msgPath, runedSpecs, dryRun)
	if err != nil {
		return fmt.Errorf("runed.db: %w", err)
	}
	aN, err := copyInto(adb, msgPath, authdSpecs, dryRun)
	if err != nil {
		return fmt.Errorf("auth.db: %w", err)
	}
	oN, err := copyInto(odb, msgPath, onbodSpecs, dryRun)
	if err != nil {
		return fmt.Errorf("onbod.db: %w", err)
	}

	// Rebuild the FTS index from the copied messages — we never copy the
	// internal messages_fts* shadow tables; the routd triggers only fire on
	// INSERT-through-the-table, which our INSERT…SELECT path bypasses.
	if !dryRun && rN["messages"] > 0 {
		if _, err := rdb.SQL().Exec(
			`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`); err != nil {
			return fmt.Errorf("rebuild messages_fts: %w", err)
		}
		fmt.Println("  routd.db messages_fts: rebuilt")
	}

	fmt.Println("\nsummary:")
	fmt.Printf("  routd.db rows: %s\n", fmtCounts(routdSpecs, rN))
	fmt.Printf("  runed.db rows: %s\n", fmtCounts(runedSpecs, uN))
	fmt.Printf("  auth.db rows:  %s\n", fmtCounts(authdSpecs, aN))
	fmt.Printf("  onbod.db rows: %s\n", fmtCounts(onbodSpecs, oN))
	fmt.Printf("\norphan tables LEFT IN messages.db (not copied — messages.db is NOT retired):\n  %v\n",
		orphanTables)
	fmt.Println("  (dashd keeps writing messages.db; acl+secrets+tasks+pane copied to routd.db; identity copied to auth.db; onboarding+invites+gates copied to onbod.db; routd opens NO sibling DB.)")
	if !dryRun {
		chownMatch(msgPath, storeDir, "routd.db", "runed.db", "auth.db", "onbod.db")
	}
	return nil
}

// chownMatch sets each <storeDir>/<name>{,-wal,-shm} to messages.db's owner so
// the uid-1000 daemons can WRITE them. migrate-split typically runs under sudo
// (to read the root-owned data dir), which would leave the new DBs root-owned →
// every daemon then crash-loops on SQLITE_READONLY (cost a sloth outage
// 2026-06-07). Best-effort: a chown error (already-correct owner, non-root run)
// is harmless.
func chownMatch(refPath, storeDir string, names ...string) {
	var st syscall.Stat_t
	if err := syscall.Stat(refPath, &st); err != nil {
		return
	}
	uid, gid := int(st.Uid), int(st.Gid)
	for _, n := range names {
		for _, suf := range []string{"", "-wal", "-shm"} {
			_ = os.Chown(filepath.Join(storeDir, n+suf), uid, gid)
		}
	}
}

// copyInto ATTACHes messages.db read-only on a single pooled connection and
// runs every spec's INSERT OR IGNORE … SELECT on it. A single *sql.Conn is
// pinned so the ATTACH (connection-scoped in SQLite) is visible to every
// statement. Returns rows-affected per destination table.
func copyInto(dst *sql.DB, msgPath string, specs []copySpec, dryRun bool) (map[string]int64, error) {
	ctx := context.Background()
	conn, err := dst.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx,
		fmt.Sprintf("ATTACH DATABASE 'file:%s?mode=ro' AS msg", msgPath)); err != nil {
		return nil, fmt.Errorf("attach messages.db: %w", err)
	}
	defer conn.ExecContext(ctx, "DETACH DATABASE msg")

	// Bulk import: disable FK enforcement on this pinned connection. The source
	// messages.db may carry legacy orphan rows (e.g. task_run_logs whose
	// scheduled_task was deleted before FK cascades were enforced); with
	// foreign_keys=on the INSERT…SELECT aborts mid-copy. Runtime daemons re-open
	// with foreign_keys=on and never re-check existing rows, so copied orphans are
	// harmless. Connection-scoped + outside any tx, so it takes effect here.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return nil, fmt.Errorf("disable FK for bulk copy: %w", err)
	}

	counts := map[string]int64{}
	for _, sp := range specs {
		if dryRun {
			var n int64
			if err := conn.QueryRowContext(ctx,
				fmt.Sprintf("SELECT COUNT(*) FROM msg.%s", sp.src)).Scan(&n); err != nil {
				return nil, fmt.Errorf("count msg.%s: %w", sp.src, err)
			}
			counts[sp.dst] = n
			logCopy(sp, n)
			continue
		}
		res, err := conn.ExecContext(ctx, fmt.Sprintf(
			"INSERT OR IGNORE INTO main.%s (%s) SELECT %s FROM msg.%s",
			sp.dst, sp.cols, sp.sel, sp.src))
		if err != nil {
			return nil, fmt.Errorf("copy %s→%s: %w", sp.src, sp.dst, err)
		}
		n, _ := res.RowsAffected()
		counts[sp.dst] = n
		logCopy(sp, n)
	}
	return counts, nil
}

func logCopy(sp copySpec, n int64) {
	kind := "copy"
	if sp.transfm {
		kind = "transform"
	}
	fmt.Printf("  %-9s %-16s ← %-16s %6d rows\n", kind, sp.dst, sp.src, n)
}

func fmtCounts(specs []copySpec, counts map[string]int64) string {
	var total int64
	for _, sp := range specs {
		total += counts[sp.dst]
	}
	return fmt.Sprintf("%d total across %d tables", total, len(specs))
}
