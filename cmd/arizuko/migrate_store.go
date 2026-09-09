package main

// migrate_store.go moves the tables store has taken off SQL onto the entities
// it reads (spec 6/37 step 7). One way, once, with the daemons stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to store.Import,
// which owns the item shape. store holds no statement against them any more, so
// the statements below are here.
//
// THE DATABASE IS routd.db, not messages.db. `pane_sessions` and
// `secret_use_log` are store's code and routd's file — routd migrations 0008 and
// 0010 created them there — so the daemon that must be stopped is routd, and
// dashd, webd and proxyd hold the same file.
//
// The old tables are RENAMED, not dropped. `pane_sessions_v1` is the pane
// history an operator may still want to read, and leaving it costs nothing. The
// rename is also what lets an entity keep the name of the table it replaces.
//
// `audit_log` is NOT here even though routd.db carries one: it belongs to audit,
// four owners share the entity, and `arizuko migrate-audit` moves all four in
// one run. Two tools that both claimed it would be two things that can disagree
// about one table.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// storeTables are the two store used to own in routd.db. The order is the order
// an operator reads the report in.
var storeTables = []string{"pane_sessions", "secret_use_log"}

// cmdMigrateStore is `arizuko migrate-store`.
func cmdMigrateStore(args []string) {
	fs := flag.NewFlagSet("migrate-store", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the routd.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-store <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-store --db <path/to/routd.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		path = store.OwnerDBPath(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"), store.OwnerRoutd)
	}
	if err := migrateStore(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateStore reads path's legacy tables, writes the entities, and reads them
// back through store's own methods to check what landed.
func migrateStore(path string, dryRun bool) error {
	if err := db_utils.RequireDBFile(path); err != nil {
		return err
	}
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, storeTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(storeTables, ", "))
		return nil
	}
	snap, err := readStoreSnapshot(db, sources)
	if err != nil {
		return err
	}
	reportSource(path, storeTables, sources, map[string]int{
		"pane_sessions":  len(snap.Panes),
		"secret_use_log": len(snap.Uses),
	})
	if _, err := store.CheckSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, storeTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	// The TWO this tool moves and nothing else: CreateEntity probes the three
	// columns it needs, so asking for routd's whole set would fail on `acl` when
	// `arizuko migrate-routd` has not run yet — and that tool would fail on
	// `pane_sessions` for the same reason. Naming the subset is what lets either
	// run first.
	if err := store.EnsureEntitySet(ctx, s, store.OwnerRoutd, storeTables...); err != nil {
		return err
	}
	st := store.New(db)
	counts, err := st.Import(ctx, snap)
	if err != nil {
		return err
	}
	fmt.Printf("\nwrote %d panes, %d secret uses\n", counts.Panes, counts.Uses)
	return verifyStore(st, snap)
}

// readStoreSnapshot reads every legacy row of whichever tables src names. A NULL
// column arrives as the empty string, which is what an absent attribute means on
// the other side.
//
// It reads a MAP of tables rather than a fixed set because `arizuko
// migrate-split` reuses it for the same two tables in the monolith: one reader
// for one schema, so the cutover and the move cannot disagree about what a row
// holds.
func readStoreSnapshot(db *sql.DB, src map[string]string) (store.Snapshot, error) {
	var snap store.Snapshot
	if t, ok := src["pane_sessions"]; ok {
		rows, err := db.Query(`SELECT team_id, user_id, thread_ts, channel_id, context_jid,
			opened_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r   store.PaneRow
				ctx sql.NullString
			)
			if err := rows.Scan(&r.TeamID, &r.UserID, &r.ThreadTS, &r.ChannelID,
				&ctx, &r.OpenedAt); err != nil {
				return snap, err
			}
			r.ContextJID = ctx.String
			snap.Panes = append(snap.Panes, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["secret_use_log"]; ok {
		// The rowid is SELECTed because the table declared no primary key, and it
		// is what separates two uses recorded in one instant (store/import.go).
		rows, err := db.Query(`SELECT rowid, ts, spawn_id, caller_sub, folder, tool, key,
			scope, status, latency_ms FROM ` + t + ` ORDER BY rowid`)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var r store.UseRow
			if err := rows.Scan(&r.RowID, &r.TS, &r.SpawnID, &r.CallerSub, &r.Folder,
				&r.Tool, &r.Key, &r.Scope, &r.Status, &r.LatencyMS); err != nil {
				return snap, err
			}
			snap.Uses = append(snap.Uses, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// verifyStore reads the entities back through the methods the daemons themselves
// call and compares them with the source. It is the "export again and compare"
// half of the move: an import that wrote the wrong attribute name would report
// its counts happily, and only a read through the real methods catches it.
//
// THE PANES ARE COMPARED PER CHANNEL rather than one at a time, because the
// channel read answers the NEWEST pane and that is the thing most likely to be
// wrong: opened_at is re-rendered on the way in (8 of marinade's 81 rows carry a
// stripped fraction), so a comparison that only asked "is this pane there" would
// pass over a re-rendering that changed which one a channel resolves to.
//
// `secret_use_log` has no reader to verify through — that is what its file
// header says and it is true of this tool as well, so what is checked is the
// count the import reports against the count the source held. A read invented
// here to make the verification look symmetric would be a reader nothing else
// has.
func verifyStore(st *store.Store, snap store.Snapshot) error {
	newest := map[string]store.PaneRow{}
	for _, r := range snap.Panes {
		if was, seen := newest[r.ChannelID]; !seen || r.OpenedAt > was.OpenedAt {
			newest[r.ChannelID] = r
		}
	}
	for channel, want := range newest {
		got, ok := st.PaneInChannel(channel)
		if !ok {
			return fmt.Errorf("channel %s reads back no pane, want %s/%s/%s",
				channel, want.TeamID, want.UserID, want.ThreadTS)
		}
		if got.TeamID != want.TeamID || got.UserID != want.UserID || got.ThreadTS != want.ThreadTS {
			return fmt.Errorf("channel %s reads back pane %s/%s/%s, want %s/%s/%s",
				channel, got.TeamID, got.UserID, got.ThreadTS,
				want.TeamID, want.UserID, want.ThreadTS)
		}
		if got.ContextJID != want.ContextJID {
			return fmt.Errorf("pane %s/%s/%s reads back context %q, want %q",
				got.TeamID, got.UserID, got.ThreadTS, got.ContextJID, want.ContextJID)
		}
	}
	fmt.Printf("  verified: %d panes over %d channels, %d secret uses\n",
		len(snap.Panes), len(newest), len(snap.Uses))
	return nil
}
