package main

// migrate_runed.go moves an existing runed.db from the three tables runed used
// to run statements against onto the entities runstore reads (spec 6/37 step
// 7). One way, once, with the daemon stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to
// runstore.Import, which owns the item shape. runstore holds no SQL, so the
// statements below are here.
//
// The old tables are RENAMED, not dropped. `session_log_v1` is the run history
// an operator may still want to read, and leaving it costs nothing — runed
// never opens it again. The rename is also what lets an entity keep the name of
// the table it replaces.
//
// `audit_log` is NOT here even though runed.db carries one: it belongs to
// audit, four owners share the entity, and `arizuko migrate-audit` moves all
// four in one run. Two tools that both claimed it would be two things that can
// disagree about one table.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

// runedTables are the three runed used to own. The order is the order the
// import needs them read in — a spawn's link resolves through the sessions —
// and it is also the order an operator reads the report in.
var runedTables = []string{"spawns", "session_log", "circuit_breaker"}

// cmdMigrateRuned is `arizuko migrate-runed`.
func cmdMigrateRuned(args []string) {
	fs := flag.NewFlagSet("migrate-runed", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the runed.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-runed <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-runed --db <path/to/runed.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		path = store.OwnerDBPath(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"), store.OwnerRuned)
	}
	if err := migrateRuned(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateRuned reads path's legacy tables, writes the entities, and reads them
// back through runstore's own methods to check what landed.
func migrateRuned(path string, dryRun bool) error {
	if err := db_utils.RequireDB(path); err != nil {
		return err
	}
	db, err := db_utils.Open(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, runedTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(runedTables, ", "))
		return nil
	}
	snap, err := readRunedSnapshot(db, sources)
	if err != nil {
		return err
	}
	reportSource(path, runedTables, sources, map[string]int{
		"spawns":          len(snap.Spawns),
		"session_log":     len(snap.Sessions),
		"circuit_breaker": len(snap.Breakers),
	})
	if _, err := runstore.CheckSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, runedTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	if err := runstore.EnsureEntities(ctx, s); err != nil {
		return err
	}
	st := runstore.New(s)
	counts, err := st.Import(ctx, snap)
	if err != nil {
		return err
	}
	fmt.Printf("\nwrote %d runs, %d sessions, %d failure streaks\n",
		counts.Spawns, counts.Sessions, counts.Breakers)
	return verifyRuned(ctx, st, snap)
}

// readRunedSnapshot reads every legacy row of whichever tables src names. A NULL
// column arrives as the empty string, which is what an absent attribute means on
// the other side.
//
// It reads a MAP of tables rather than a fixed set because `arizuko
// migrate-split` reuses it for the one table the monolith carried: one reader
// for one schema, so the cutover and the move cannot disagree about what a
// session_log row holds.
func readRunedSnapshot(db *sql.DB, src map[string]string) (runstore.Snapshot, error) {
	var snap runstore.Snapshot
	if t, ok := src["session_log"]; ok {
		rows, err := db.Query(`SELECT id, group_folder, session_id, started_at, ended_at,
			result, error, message_count FROM ` + t + ` ORDER BY id`)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r    runstore.SessionRow
				text = make([]sql.NullString, 4)
				msgs sql.NullInt64
			)
			if err := rows.Scan(&r.ID, &r.GroupFolder, &text[0], &r.StartedAt,
				&text[1], &text[2], &text[3], &msgs); err != nil {
				return snap, err
			}
			r.SessionID, r.EndedAt, r.Result, r.Error =
				text[0].String, text[1].String, text[2].String, text[3].String
			r.MessageCount = int(msgs.Int64)
			snap.Sessions = append(snap.Sessions, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["spawns"]; ok {
		rows, err := db.Query(`SELECT run_id, folder, topic, container_name, session_log_id,
			session_id, kind, state, outcome, exit_code, steered, created_at, started_at,
			ended_at FROM ` + t + ` ORDER BY created_at, rowid`)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r             runstore.SpawnRow
				logID, exit   sql.NullInt64
				text          = make([]sql.NullString, 4)
				steered       int
				topic, kindNS sql.NullString
			)
			if err := rows.Scan(&r.RunID, &r.Folder, &topic, &r.ContainerName, &logID,
				&text[0], &kindNS, &r.State, &text[1], &exit, &steered,
				&r.CreatedAt, &text[2], &text[3]); err != nil {
				return snap, err
			}
			r.Topic, r.Kind = topic.String, kindNS.String
			r.SessionLogID = logID.Int64
			r.SessionID, r.Outcome = text[0].String, text[1].String
			r.ExitCode = int(exit.Int64)
			r.Steered = steered == 1
			r.StartedAt, r.EndedAt = text[2].String, text[3].String
			snap.Spawns = append(snap.Spawns, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["circuit_breaker"]; ok {
		// last_failure is deliberately not read: the entity does not carry it,
		// because nothing ever read it (runed/runstore/circuit_breaker.go).
		rows, err := db.Query(`SELECT folder, failures FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var r runstore.BreakerRow
			if err := rows.Scan(&r.Folder, &r.Failures); err != nil {
				return snap, err
			}
			snap.Breakers = append(snap.Breakers, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// verifyRuned reads the entities back through the methods runed itself calls and
// compares them with the source. It is the "export again and compare" half of
// the move: an import that wrote the wrong attribute name would report its
// counts happily, and only a read through the real methods catches it.
//
// The SESSIONS are compared as a whole sequence per folder rather than sampled,
// because the ORDER is the thing most likely to be wrong — the id is now the
// instant plus the old sequence number, so a page that read them back in another
// order would still hold every row.
//
// THE ORDER IT COMPARES AGAINST IS THE CLOCK'S, not the sequence's, and on live
// data those are not always the same thing. Measured 2026-09-07 across all three
// instances, 31,110 sessions: 39 sloth rows in one folder on one day carry a
// started_at EARLIER than the row before them in the sequence, by one to three
// seconds. The new key orders by the instant with the sequence number as the
// tiebreak, so those rows change position relative to the old `ORDER BY id
// DESC` — and that is the right way round: every reader of this history renders
// started_at, so the instant is the order they mean, and the sequence was a
// proxy for it that this data disagrees with. The run reports how many moved
// rather than passing silently over it.
func verifyRuned(ctx context.Context, st *runstore.Store, snap runstore.Snapshot) error {
	for _, r := range snap.Spawns {
		got, err := st.SpawnByRunID(ctx, r.RunID)
		if err != nil {
			return fmt.Errorf("read run %s back: %w", r.RunID, err)
		}
		if got.Folder != r.Folder || got.State != r.State || got.ContainerName != r.ContainerName {
			return fmt.Errorf("run %s reads back as %s/%s/%s, want %s/%s/%s",
				r.RunID, got.Folder, got.State, got.ContainerName,
				r.Folder, r.State, r.ContainerName)
		}
	}

	byFolder := map[string][]runstore.SessionRow{}
	for _, r := range snap.Sessions {
		byFolder[r.GroupFolder] = append(byFolder[r.GroupFolder], r)
	}
	moved := 0
	for folder, rows := range byFolder {
		want, reordered, err := sessionsNewestFirst(rows)
		if err != nil {
			return err
		}
		moved += reordered
		got, err := st.RecentSessions(ctx, folder, len(want))
		if err != nil {
			return fmt.Errorf("read %s's sessions back: %w", folder, err)
		}
		if len(got) != len(want) {
			return fmt.Errorf("%s reads back %d sessions, want %d", folder, len(got), len(want))
		}
		for i, g := range got {
			w := want[i]
			id, err := runstore.LegacySessionID(w)
			if err != nil {
				return err
			}
			if g.ID != id {
				return fmt.Errorf("%s session %d reads back as %s, want %s — the page is not "+
					"in the order the key gives", folder, i, g.ID, id)
			}
			if g.SessionID != w.SessionID || g.MessageCount != w.MessageCount {
				return fmt.Errorf("session %s reads back as %s/%d, want %s/%d",
					g.ID, g.SessionID, g.MessageCount, w.SessionID, w.MessageCount)
			}
		}
	}

	for _, r := range snap.Breakers {
		n, err := st.FailureStreak(ctx, r.Folder)
		if err != nil {
			return fmt.Errorf("read %s's failure streak back: %w", r.Folder, err)
		}
		if n != r.Failures {
			return fmt.Errorf("%s reads back %d failures, want %d", r.Folder, n, r.Failures)
		}
	}
	fmt.Printf("  verified: %d runs, %d sessions, %d streaks\n",
		len(snap.Spawns), len(snap.Sessions), len(snap.Breakers))
	if moved > 0 {
		fmt.Printf("  %d sessions changed position: their started_at runs backwards against "+
			"the id sequence, and the page now orders by the instant it renders\n", moved)
	}
	return nil
}

// sessionsNewestFirst answers one folder's rows in the order the new key gives
// them — newest first — and how many of them the sequence would have put
// somewhere else.
func sessionsNewestFirst(rows []runstore.SessionRow) ([]runstore.SessionRow, int, error) {
	keys := make(map[int64]string, len(rows))
	for _, r := range rows {
		id, err := runstore.LegacySessionID(r)
		if err != nil {
			return nil, 0, err
		}
		keys[r.ID] = id
	}
	bySeq := append([]runstore.SessionRow(nil), rows...)
	sort.Slice(bySeq, func(i, j int) bool { return bySeq[i].ID > bySeq[j].ID })
	byKey := append([]runstore.SessionRow(nil), rows...)
	sort.Slice(byKey, func(i, j int) bool { return keys[byKey[i].ID] > keys[byKey[j].ID] })
	moved := 0
	for i := range byKey {
		if byKey[i].ID != bySeq[i].ID {
			moved++
		}
	}
	return byKey, moved, nil
}
