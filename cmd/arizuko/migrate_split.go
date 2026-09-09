package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"

	"github.com/kronael/arizuko/audit"

	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/resreg"
)

// cmdMigrateSplit populates routd.db + runed.db + auth.db from an existing
// instance's messages.db. messages.db stays ALIVE: dashd keeps writing the orphan
// tables. acl/acl_membership, secrets/secret_use_log, scheduled_tasks/task_run_logs
// and pane_sessions all moved to routd's own DB, so they are COPIED, not left.
// Idempotent (INSERT OR IGNORE on primary keys) and safe to run on a copy.
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
	transfm bool   // true → column names/shape differ (logged distinctly)
}

// routdSpecs map messages.db → routd.db. Column lists were derived from the
// live schemas (store/migrations, routd/migrations), NOT the design brief —
// see migrateSplit's summary for the deltas found.
var routdSpecs = []copySpec{
	// straight copies (shared column intersection)
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
	{dst: "turn_results", src: "turn_results",
		cols: "folder, turn_id, session_id, status, recorded_at",
		sel:  "folder, turn_id, session_id, status, recorded_at"},
	{dst: "group_watchers", src: "group_watchers",
		cols: "observer, source",
		sel:  "observer, source"},
	// scheduled_tasks + task_run_logs: routd now OWNS these (routd migration 0009
	// mirrors store 0001/0011 final shape). Straight copies — identical schema
	// both sides. task_run_logs.id is AUTOINCREMENT but copied verbatim so the
	// FK to scheduled_tasks(id) stays intact.
	{dst: "task_run_logs", src: "task_run_logs",
		cols: "id, task_id, run_at, duration_ms, status, result, error",
		sel:  "id, task_id, run_at, duration_ms, status, result, error"},
	// user_profiles: routd.db OWNS it (routd migrations 0011 + 0025; the cap column
	// references it). Split onbod reads+writes it cross-DB on routd.db (xdb), so it MUST
	// be copied — left an orphan, every existing user vanishes from onboarding /
	// world-create. The source is a PRE-split messages.db: there the table predates the
	// rename and is still auth_users; `hash` (dropped by 0025) and `linked_to_sub`
	// (dropped by 0027 — nothing ever wrote it) are not copied.
	{dst: "user_profiles", src: "auth_users", transfm: true,
		cols: "id, sub, username, name, created_at, cost_cap_cents_per_day",
		sel:  "id, sub, username, name, created_at, cost_cap_cents_per_day"},

	// audit_log, pane_sessions, secret_use_log, routd's own thirteen and the hot
	// tier's five are NOT here: each is an objstore entity, so none is a column
	// list and no INSERT … SELECT can carry one. copyAuditLog, copyStoreEntities,
	// copyRoutdEntities and copyHotEntities below read the monolith's rows
	// through the same source readers `arizuko migrate-audit`, `migrate-store`,
	// `migrate-routd` and `migrate-hot` use, and hand them to the packages that
	// own the item shape.
	// cost_log_archive: the spend a pre-split instance recorded, landing in the
	// table that holds spend history (spec 6/39 — the model gateway is the ledger,
	// and these rows predate one metering anything).
	//
	// messages.db has no turn_id column (the archive's PK is folder,turn_id,model).
	// Synthesize a UNIQUE turn_id per source row ('mig-'||rowid) — a constant ''
	// would collapse every legacy (folder,model) pair to ONE row under INSERT OR
	// IGNORE, dropping cost history. input_tok→input_tokens, output_tok→output_tokens,
	// cents→cost_cents, ts→recorded_at; user_sub/cache_read/cache_write dropped.
	{dst: "cost_log_archive", src: "cost_log", transfm: true,
		cols: "folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at",
		sel:  "folder, 'mig-'||rowid, model, input_tok, output_tok, cents, ts"},
}

// The monolith's only runed-owned table is session_log, and it travels through
// runstore's own import rather than a copySpec: it is an ENTITY now, so the
// destination has no `session_log` table to INSERT into, and the id the copy
// would have carried is the AUTOINCREMENT sequence a document store does not
// have (spec 6/37 rule 4). `spawns` and `circuit_breaker` have no pre-split
// source at all.

// onbod's three owned tables have NO copySpec, and they are the second set to
// lose one: onboarding, onboarding_gates and invites are ENTITIES, so the
// destination has no table to INSERT into and they travel through store's own
// import (copyOnbodEntities), the way session_log and the panes already do.
// Neither bearer column is plaintext, and messages.db is only ever opened via
// store.Open, which runs the hash-at-rest backfills first, so the source is
// already hashed by the time this read-only ATTACH runs.

// bootstrapOnbod prepares onbod.db to receive the copy. It creates NO table:
// onbod's own chain creates none either, so the three halves below are three
// owners' entity declarations and nothing else — audit's log, store's three, and
// the version marker resreg's apply claims.
//
// onbod's migration FS is package-private (onbod is package main), which is why
// this is here rather than a call into it; there is no DDL left to mirror, so
// there is nothing left to drift.
func bootstrapOnbod(db *sql.DB) error {
	ctx := context.Background()
	obj := sqlstore.New(db)
	if err := audit.EnsureEntities(ctx, obj); err != nil {
		return err
	}
	if err := store.EnsureEntities(ctx, obj, store.OwnerOnbod); err != nil {
		return err
	}
	return resreg.EnsureSubsystem(ctx, obj, obj, resreg.SubsystemOnbod)
}

// onbodBootstrapVersion is how far onbod's chain bootstrapOnbod stands in for.
// migrateSplit stamps exactly these rows into `migrations` so onbod's next boot
// resumes after them.
//
// It stays 4 because 0001..0004 now build nothing — each is a comment and a
// `SELECT 1;`, since the three tables they created and reshaped are entities.
// Claiming too FEW replays them harmlessly now; claiming too MANY would silently
// skip 0005, which still creates invite_redemptions.
// TestOnbodSchemaMatchesMigrations pins the two.
const onbodBootstrapVersion = 4

// orphanTables stay in messages.db post-cutover: no daemon reads them. Every table
// a daemon still needs moved to routd.db / onbod.db / runed.db or is federated over
// HTTP. onboarding/invites/onboarding_gates, auth_users and audit_log/proxyd_routes
// are NOT orphans — each has an owner listed above. auth_sessions stays (the
// cookie-session path is gone); config_meta stays pending the apply repoint.
//
// Listed so the summary tells the operator what is left behind. messages.db is
// RETIRED but NOT deleted: it holds pre-split history, and disposing of it is the
// operator's call.
var orphanTables = []string{
	"router_state", "config_meta", "cli_audit", "ipc_audit", "auth_sessions",
}

// recordOnbodBootstrap stamps `migrations` rows 1..onbodBootstrapVersion for
// service "onbod", declaring how far bootstrapOnbod stands in for. Rows
// are written ONLY when onbod has recorded none of its own: a DB onbod already
// migrated owns its own version, and overwriting it could claim past a
// migration that DB has genuinely not applied. The table DDL matches
// db_utils.Migrate's, which creates it the same way on onbod's next boot.
func recordOnbodBootstrap(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM migrations WHERE service='onbod'").Scan(&n); err != nil {
		return fmt.Errorf("read onbod migration rows: %w", err)
	}
	if n > 0 {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	for v := 1; v <= onbodBootstrapVersion; v++ {
		if _, err := db.Exec(
			"INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
			"onbod", v, now); err != nil {
			return fmt.Errorf("record onbod migration %d: %w", v, err)
		}
	}
	return nil
}

// copyAuditLog moves the monolith's audit trail into routd.db's entity and
// answers how many events landed. The source half is the reader migrate-audit
// already has, so the two tools read one table one way.
func copyAuditLog(dst *sql.DB, msgPath string, dryRun bool) (int, error) {
	src, err := db_utils.Open(db_utils.FileDSN(msgPath))
	if err != nil {
		return 0, err
	}
	defer src.Close() //nolint:errcheck // read-only, and the process ends either way
	sources, err := legacySources(src, auditTables)
	if err != nil {
		return 0, err
	}
	if len(sources) == 0 {
		return 0, nil
	}
	snap, err := readAuditSnapshot(src, sources["audit_log"])
	if err != nil {
		return 0, err
	}
	if _, err := audit.CheckSnapshot(snap); err != nil {
		return 0, err
	}
	if dryRun {
		return len(snap.Rows), nil
	}
	return audit.Import(context.Background(), sqlstore.New(dst), snap)
}

// copySessionLog moves the monolith's run history into runed.db's entity and
// answers how many sessions landed. The source half is the reader migrate-runed
// already has, so the two tools read one table one way.
func copySessionLog(dst *sql.DB, msgPath string, dryRun bool) (int, error) {
	src, err := db_utils.Open(db_utils.FileDSN(msgPath))
	if err != nil {
		return 0, err
	}
	defer src.Close() //nolint:errcheck // read-only, and the process ends either way
	sources, err := legacySources(src, []string{"session_log"})
	if err != nil {
		return 0, err
	}
	if len(sources) == 0 {
		return 0, nil
	}
	snap, err := readRunedSnapshot(src, sources)
	if err != nil {
		return 0, err
	}
	if _, err := runstore.CheckSnapshot(snap); err != nil {
		return 0, err
	}
	if dryRun {
		return len(snap.Sessions), nil
	}
	counts, err := runstore.New(sqlstore.New(dst)).Import(context.Background(), snap)
	return counts.Sessions, err
}

// copyStoreEntities moves the monolith's panes and secret uses into routd.db's
// entities and answers what landed. The source half is the reader `arizuko
// migrate-store` already has, so the two tools read one schema one way.
func copyStoreEntities(dst *sql.DB, msgPath string, dryRun bool) (store.Counts, error) {
	src, err := db_utils.Open(db_utils.FileDSN(msgPath))
	if err != nil {
		return store.Counts{}, err
	}
	defer src.Close() //nolint:errcheck // read-only, and the process ends either way
	sources, err := legacySources(src, storeTables)
	if err != nil {
		return store.Counts{}, err
	}
	if len(sources) == 0 {
		return store.Counts{}, nil
	}
	snap, err := readStoreSnapshot(src, sources)
	if err != nil {
		return store.Counts{}, err
	}
	counts, err := store.CheckSnapshot(snap)
	if err != nil {
		return store.Counts{}, err
	}
	if dryRun {
		return counts, nil
	}
	return store.New(dst).Import(context.Background(), snap)
}

// copyOnbodEntities carries the monolith's onboarding, onboarding_gates and
// invites into onbod.db's entities. It reuses migrate-onbod's reader, so the
// cutover and the move cannot disagree about what a row holds — the same reason
// copyStoreEntities reuses migrate-store's.
func copyOnbodEntities(dst *sql.DB, msgPath string, dryRun bool) (store.OnbodCounts, error) {
	src, err := db_utils.Open(db_utils.FileDSN(msgPath))
	if err != nil {
		return store.OnbodCounts{}, err
	}
	defer src.Close() //nolint:errcheck // read-only, and the process ends either way
	sources, err := legacySources(src, onbodTables)
	if err != nil {
		return store.OnbodCounts{}, err
	}
	if len(sources) == 0 {
		return store.OnbodCounts{}, nil
	}
	snap, err := readOnbodSnapshot(src, sources)
	if err != nil {
		return store.OnbodCounts{}, err
	}
	counts, err := store.CheckOnbodSnapshot(snap)
	if err != nil {
		return store.OnbodCounts{}, err
	}
	if dryRun {
		return counts, nil
	}
	return store.New(dst).ImportOnbod(context.Background(), snap)
}

// copyRoutdEntities carries the monolith's thirteen routd tables into routd.db's
// entities. It reuses migrate-routd's reader, so the cutover and the in-place
// move cannot disagree about what a row holds — the same reason
// copyOnbodEntities reuses migrate-onbod's.
//
// The reader takes every column the monolith's table HAS, from the schema, so a
// column the pre-split chain carried and the split chain did not still travels
// as the attribute it becomes. That is what the fifteen hand-written column
// lists above could not promise.
func copyRoutdEntities(dst *sql.DB, msgPath string, dryRun bool) (store.RoutdCounts, error) {
	src, err := db_utils.Open(db_utils.FileDSN(msgPath))
	if err != nil {
		return nil, err
	}
	defer src.Close() //nolint:errcheck // read-only, and the process ends either way
	sources, err := legacySources(src, store.RoutdEntities)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return store.RoutdCounts{}, nil
	}
	snap, err := readRoutdSnapshot(src, sources)
	if err != nil {
		return nil, err
	}
	counts, err := store.CheckRoutdSnapshot(snap)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return counts, nil
	}
	return store.New(dst).ImportRoutd(context.Background(), snap)
}

// hotColumnAlias renames the pre-split monolith's system_messages columns onto
// the names routd.db gave them. It is the one place the two schemas differ over
// the five, and it is a declaration rather than a second reader.
var hotColumnAlias = map[string]map[string]string{
	"system_messages": {"group_id": "folder", "origin": "source", "event": "kind",
		"created_at": "created"},
}

// copyHotEntities carries the monolith's five hot-tier tables into routd.db as
// items, through the same source reader `arizuko migrate-hot` uses.
//
// IT RUNS AFTER THE MESSAGE ROWS ARE NORMALISED, and that order is the whole
// reason it is a separate step rather than four more copySpecs: the two cursors
// it carries are compared against messages.timestamp, and a cursor that moves
// relative to its rows re-feeds or skips everything between the two values
// (routd migration 0044). The reader refuses a cursor of the wrong width and
// this step refuses to run while any destination message row is still unpadded,
// so neither order can be got wrong by accident.
func copyHotEntities(dst *sql.DB, msgPath string, dryRun bool) (store.RoutdCounts, error) {
	src, err := db_utils.Open(db_utils.FileDSN(msgPath))
	if err != nil {
		return nil, err
	}
	defer src.Close() //nolint:errcheck // read-only, and the process ends either way
	sources, err := legacySources(src, store.HotEntities)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return store.RoutdCounts{}, nil
	}
	if err := requirePaddedMessageRows(dst, sources); err != nil {
		return nil, err
	}
	snap, err := readHotSnapshot(src, sources, hotColumnAlias)
	if err != nil {
		return nil, err
	}
	counts, err := store.CheckHotSnapshot(snap)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return counts, nil
	}
	return store.New(dst).ImportHot(context.Background(), snap)
}

func migrateSplit(storeDir string, dryRun bool) error {
	msgPath := filepath.Join(storeDir, "messages.db")
	if _, err := os.Stat(msgPath); err != nil {
		return fmt.Errorf("messages.db not found at %s: %w", msgPath, err)
	}

	// Create destinations (the cutover is the one place besides `arizuko create`
	// that legitimately makes an owner DB) so migrations run first → all target
	// tables exist before we copy into them.
	rdb, err := routd.Create(storeDir)
	if err != nil {
		return fmt.Errorf("open routd.db: %w", err)
	}
	defer rdb.Close()
	udb, err := runed.Create(storeDir)
	if err != nil {
		return fmt.Errorf("open runed.db: %w", err)
	}
	defer udb.Close()
	// onbod.db: onbod OWNS onboarding/invites/onboarding_gates, and all three are
	// entities, so what the copy target needs is the driver's declarations rather
	// than a table (bootstrapOnbod).
	odb, err := db_utils.Open(db_utils.FileDSN(filepath.Join(storeDir, "onbod.db")))
	if err != nil {
		return fmt.Errorf("open onbod.db: %w", err)
	}
	defer odb.Close()
	if !dryRun {
		if err := bootstrapOnbod(odb); err != nil {
			return fmt.Errorf("onbod.db schema: %w", err)
		}
		if err := recordOnbodBootstrap(odb); err != nil {
			return fmt.Errorf("onbod.db bootstrap version: %w", err)
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
	// The copy is SQL-to-SQL, so messages.timestamp, chats.agent_cursor and
	// sessions.observed_cursor arrive in whatever width the source wrote —
	// and routd.Create has already run the migration that normalises them, so
	// the sequence will not revisit these rows. Hand them to routd's own
	// normaliser here, or the column's string order stops being its time order
	// and a cursor skips a row for good (BUGS L219, L97).
	if !dryRun {
		if err := routd.NormalizeFixedWidthTimestamps(rdb.SQL()); err != nil {
			return fmt.Errorf("routd.db: %w", err)
		}
	}
	// The monolith's audit trail lands in routd.db, which is where dashd, webd
	// and proxyd all record. It is an entity rather than a table, so it travels
	// through audit's own import instead of copyInto — and the import carries
	// the sequence numbers, so the order the ids had survives.
	aN, err := copyAuditLog(rdb.SQL(), msgPath, dryRun)
	if err != nil {
		return fmt.Errorf("routd.db: %w", err)
	}
	// The monolith's thirteen routd tables land in routd.db as entities, and for
	// the same reason the audit trail does: none of them is a column list any
	// more, so none can travel through an INSERT … SELECT.
	eN, err := copyRoutdEntities(rdb.SQL(), msgPath, dryRun)
	if err != nil {
		return fmt.Errorf("routd.db: %w", err)
	}
	// The hot tier's five, after the normaliser above and never before it: two of
	// them carry a cursor into messages.timestamp, and the reader refuses one
	// whose width the rows no longer match.
	hN, err := copyHotEntities(rdb.SQL(), msgPath, dryRun)
	if err != nil {
		return fmt.Errorf("routd.db: %w", err)
	}
	// 5/33: bind every migrated folder to role:member (the messaging floor). The
	// import writes group items directly, so no creating writer ran and no edge
	// came with them — a migrated agent would have no grants and could not even
	// send. Idempotent: the edge write is create-if-absent.
	if !dryRun {
		st := store.New(rdb.SQL())
		for f := range rdb.AllGroups() {
			if err := st.PutMembership("folder:"+f, "role:member", "system:4r-migrate"); err != nil {
				return fmt.Errorf("assign role:member to %s: %w", f, err)
			}
		}
	}
	// The monolith's panes and secret uses land in routd.db too, and for the same
	// reason they travel through store's own import rather than copyInto.
	sN, err := copyStoreEntities(rdb.SQL(), msgPath, dryRun)
	if err != nil {
		return fmt.Errorf("routd.db: %w", err)
	}
	uN, err := copySessionLog(udb.SQL(), msgPath, dryRun)
	if err != nil {
		return fmt.Errorf("runed.db: %w", err)
	}
	// The monolith's admissions, gates and invites land in onbod.db, and for the
	// same reason they travel through store's own import rather than copyInto.
	oN, err := copyOnbodEntities(odb, msgPath, dryRun)
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
	for _, entity := range store.RoutdEntities {
		fmt.Printf("  routd.db %-19s %d\n", entity+":", eN[entity])
	}
	for _, entity := range store.HotEntities {
		fmt.Printf("  routd.db %-19s %d\n", entity+":", hN[entity])
	}
	fmt.Printf("  routd.db audit_log: %d events\n", aN)
	fmt.Printf("  routd.db pane_sessions: %d panes, secret_use_log: %d uses\n", sN.Panes, sN.Uses)
	fmt.Printf("  runed.db session_log: %d sessions\n", uN)
	fmt.Printf("  onbod.db onboarding: %d admissions, onboarding_gates: %d gates, "+
		"invites: %d\n", oN.Admissions, oN.Gates, oN.Invites)
	fmt.Printf("\norphan tables LEFT IN messages.db (not copied — no daemon reads them):\n  %v\n",
		orphanTables)
	fmt.Println("  (messages.db is RETIRED — no daemon opens it after this run. The FILE is kept:")
	fmt.Println("   it holds pre-split history; deleting it is your call, separately.)")
	if !dryRun {
		chownMatch(msgPath, storeDir, "routd.db", "runed.db", "onbod.db")
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
