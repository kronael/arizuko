package main

// migrate_audit.go moves an instance's audit trails from the table `audit`
// used to run statements against onto the entity it reads (spec 6/37 step 7).
// One way, once, with the daemons stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the table into plain values and hands them to audit.Import,
// which owns the item shape. audit holds no SQL, so the statements below are
// here.
//
// The old table is RENAMED, not dropped. `audit_log_v1` is the compliance
// record an operator may be asked to produce, and leaving it costs nothing — no
// daemon opens it again. The rename is also what lets the entity keep the name
// of the table it replaces.
//
// IT MIGRATES A WHOLE INSTANCE, not one file, because audit_log is in FOUR
// databases at once: routd's, authd's, onbod's and runed's. Each daemon owns
// its own trail and correlation across them is the turn_id, so a run that
// missed one leaves that daemon refusing to boot — which is the loud half of
// the move, not a partial success.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// auditTables is the one table audit used to own.
var auditTables = []string{"audit_log"}

// auditOwners are the daemons whose database carries a trail.
var auditOwners = []string{store.OwnerRoutd, store.OwnerAuthd, store.OwnerOnbod, store.OwnerRuned}

// cmdMigrateAudit is `arizuko migrate-audit`.
func cmdMigrateAudit(args []string) {
	fs := flag.NewFlagSet("migrate-audit", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the sources and check them; write nothing")
	dbPath := fs.String("db", "", "one database to migrate, instead of an instance's four")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-audit <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-audit --db <path/to/routd.db> [--dry-run]")
		os.Exit(1)
	}
	paths := []string{*dbPath}
	if *dbPath == "" {
		storeDir := filepath.Join(mustInstanceDir(fs.Arg(0)), "store")
		paths = nil
		for _, owner := range auditOwners {
			path := store.OwnerDBPath(storeDir, owner)
			if _, err := os.Stat(path); err != nil {
				// A database an instance does not run — onbod is an optional
				// profile — is not a source that failed.
				fmt.Printf("%s: absent, nothing to migrate\n", path)
				continue
			}
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			die("Failed: no owner database under %s", storeDir)
		}
	}
	for _, path := range paths {
		if err := migrateAudit(path, *dryRun); err != nil {
			die("Failed on %s: %v", path, err)
		}
	}
}

// migrateAudit reads path's legacy table, writes the entity, and reads it back
// through audit's own Query to check what landed.
func migrateAudit(path string, dryRun bool) error {
	if err := db_utils.RequireDB(path); err != nil {
		return err
	}
	db, err := db_utils.Open(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, auditTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries no legacy audit_log: nothing to migrate.\n", path)
		return nil
	}
	snap, err := readAuditSnapshot(db, sources["audit_log"])
	if err != nil {
		return err
	}
	reportSource(path, auditTables, sources, map[string]int{"audit_log": len(snap.Rows)})
	if _, err := audit.CheckSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entity cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("  --dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, auditTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	if err := audit.EnsureEntities(ctx, s); err != nil {
		return err
	}
	written, err := audit.Import(ctx, s, snap)
	if err != nil {
		return err
	}
	fmt.Printf("  wrote %d events\n", written)
	return verifyAudit(s, snap)
}

// readAuditSnapshot reads every legacy row. A NULL column arrives as the empty
// string, which is what an absent attribute means on the other side.
func readAuditSnapshot(db *sql.DB, table string) (audit.Snapshot, error) {
	var snap audit.Snapshot
	rows, err := db.Query(`SELECT id, created_at, category, action, actor, actor_sub,
		resource, scope, surface, params_summary, outcome, error_msg, duration_ms,
		turn_id, folder, instance, request_id, source_ip FROM ` + table + ` ORDER BY id`)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    audit.LegacyRow
			text = make([]sql.NullString, 12)
			dur  sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.Category, &r.Action, &r.Actor,
			&text[0], &text[1], &text[2], &text[3], &text[4], &r.Outcome, &text[5],
			&dur, &text[6], &text[7], &text[8], &text[9], &text[10]); err != nil {
			return snap, err
		}
		r.ActorSub, r.Resource, r.Scope, r.Surface = text[0].String, text[1].String, text[2].String, text[3].String
		r.ParamsSummary, r.ErrorMsg = text[4].String, text[5].String
		r.DurationMS = dur.Int64
		r.TurnID, r.Folder, r.Instance, r.RequestID, r.SourceIP =
			text[6].String, text[7].String, text[8].String, text[9].String, text[10].String
		snap.Rows = append(snap.Rows, r)
	}
	return snap, rows.Err()
}

// verifyAudit reads the entity back through the method every daemon serves
// /v1/audit with and compares it with the source. It is the "export again and
// compare" half of the move: an import that wrote the wrong attribute name
// would report its count happily, and only a read through the real method
// catches it.
//
// It compares the WHOLE sequence rather than a sample, because the thing most
// likely to be wrong is the ORDER — the id is now the instant plus the old
// sequence number, and a page that reads them back in another order would still
// hold every row.
func verifyAudit(s *sqlstore.Store, snap audit.Snapshot) error {
	ctx := context.Background()
	var before string
	seen := 0
	// The source is oldest-first and the read is newest-first.
	for i := len(snap.Rows) - 1; i >= 0; {
		page, err := audit.Query(ctx, s, audit.Filter{Before: before, Limit: audit.MaxLimit})
		if err != nil {
			return fmt.Errorf("read the events back: %w", err)
		}
		if len(page) == 0 {
			return fmt.Errorf("the read ran out after %d of %d events", seen, len(snap.Rows))
		}
		for _, got := range page {
			want, err := audit.LegacyID(snap.Rows[i])
			if err != nil {
				return err
			}
			if got.ID != want {
				return fmt.Errorf("event %d reads back as %s, want %s — the order the "+
					"sequence had was not carried", seen, got.ID, want)
			}
			if got.Action != snap.Rows[i].Action || got.Actor != snap.Rows[i].Actor {
				return fmt.Errorf("event %s reads back as %s/%s, want %s/%s",
					got.ID, got.Action, got.Actor, snap.Rows[i].Action, snap.Rows[i].Actor)
			}
			before = got.ID
			seen++
			i--
			if i < 0 {
				break
			}
		}
	}
	tail, err := audit.Query(ctx, s, audit.Filter{Before: before, Limit: 1})
	if err != nil {
		return err
	}
	if len(tail) != 0 {
		return fmt.Errorf("the entity holds %s, which the source did not", tail[0].ID)
	}
	fmt.Printf("  verified: %d events read back in the order the sequence had\n", seen)
	return nil
}
