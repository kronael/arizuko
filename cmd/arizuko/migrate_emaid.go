package main

// migrate_emaid.go moves an existing emaid.db from the two tables emaid used to
// run statements against onto the entities emaistore reads (spec 6/37 step 7).
// One way, once, with the adapter stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to
// emaistore.Import, which owns the item shape. emaistore holds no SQL, so the
// statements below are here.
//
// The old tables are RENAMED, not dropped. `email_threads_v1` is the evidence
// an operator needs if a conversation stops threading, and leaving it costs
// nothing — emaid never opens it again. The rename is also what lets an entity
// keep the name of the table it replaces.
//
// chanlib's `outbound_dedup` shares this file and is untouched: it is chanlib's
// table, written by chanlib's own statements, and it moves when chanlib does.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// emaidTables are the two emaid used to own.
var emaidTables = []string{"email_threads", "email_msg_ids"}

// cmdMigrateEmaid is `arizuko migrate-emaid`.
func cmdMigrateEmaid(args []string) {
	fs := flag.NewFlagSet("migrate-emaid", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the emaid.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-emaid <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-emaid --db <path/to/emaid.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		// emaid.db is adapter-private state under the adapter's own DATA_DIR,
		// which template/services/emaid.yml mounts at <instance>/store/emaid.
		path = filepath.Join(mustInstanceDir(fs.Arg(0)), "store", "emaid", "emaid.db")
		if _, err := os.Stat(path); err != nil {
			// An instance that runs no email adapter is not a source that
			// failed, and a run of every tool in turn must reach the next one.
			// A path the operator NAMED is different: that one still fails.
			fmt.Printf("%s: absent, nothing to migrate\n", path)
			return
		}
	}
	if err := migrateEmaid(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateEmaid reads path's legacy tables, writes the entities, and reads them
// back through emaistore's own methods to check what landed.
func migrateEmaid(path string, dryRun bool) error {
	if err := db_utils.RequireDB(path); err != nil {
		return err
	}
	db, err := db_utils.Open(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, emaidTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(emaidTables, ", "))
		return nil
	}
	snap, err := readEmaidSnapshot(db, sources)
	if err != nil {
		return err
	}
	reportSource(path, emaidTables, sources, map[string]int{
		"email_threads": len(snap.Threads),
		"email_msg_ids": len(snap.MsgIDs),
	})
	counts, err := emaistore.CheckSnapshot(snap)
	if err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if counts.Dangling > 0 {
		fmt.Printf("\n  %d index rows name a thread the source has no row for; they carry "+
			"over as dead weight and read as a miss, which is what the old join answered.\n",
			counts.Dangling)
	}
	if dryRun {
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, emaidTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	for _, entity := range emaistore.Entities {
		if err := s.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	st := emaistore.New(s)
	written, err := st.Import(snap)
	if err != nil {
		return err
	}
	fmt.Printf("\nwrote %d threads and %d message-id mappings\n", written.Threads, written.MsgIDs)
	return verifyEmaid(st, snap)
}

// readEmaidSnapshot reads every legacy row. A NULL column arrives as the empty
// string, which is what an absent attribute means on the other side.
func readEmaidSnapshot(db *sql.DB, src map[string]string) (emaistore.Snapshot, error) {
	var snap emaistore.Snapshot
	if t, ok := src["email_threads"]; ok {
		rows, err := db.Query(`SELECT thread_id, from_address, root_msg_id FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var r emaistore.ThreadRow
			if err := rows.Scan(&r.ThreadID, &r.FromAddress, &r.RootMsgID); err != nil {
				return snap, err
			}
			snap.Threads = append(snap.Threads, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["email_msg_ids"]; ok {
		rows, err := db.Query(`SELECT msg_id, thread_id FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var r emaistore.MsgIDRow
			if err := rows.Scan(&r.MsgID, &r.ThreadID); err != nil {
				return snap, err
			}
			snap.MsgIDs = append(snap.MsgIDs, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// verifyEmaid reads the entities back through the methods emaid itself calls
// and compares them with the source. It is the "export again and compare" half
// of the move: an import that wrote the wrong attribute name would still report
// its counts happily, and only a read through the real methods catches it.
//
// It walks the INDEX rather than the threads, because the index read is the one
// that decides whether an inbound reply joins its conversation, and it exercises
// both entities on the way.
func verifyEmaid(st *emaistore.Store, snap emaistore.Snapshot) error {
	threads := map[string]emaistore.ThreadRow{}
	for _, t := range snap.Threads {
		got, ok := st.Thread(t.ThreadID)
		if !ok {
			return fmt.Errorf("thread %s is not in the store", t.ThreadID)
		}
		if got.FromAddress != t.FromAddress || got.RootMsgID != t.RootMsgID {
			return fmt.Errorf("thread %s reads back as %+v, want %s under %s",
				t.ThreadID, got, t.FromAddress, t.RootMsgID)
		}
		threads[t.ThreadID] = t
	}
	resolved, dangling := 0, 0
	for _, m := range snap.MsgIDs {
		got, ok := st.ThreadForMessage(m.MsgID)
		if _, held := threads[m.ThreadID]; !held {
			if ok {
				return fmt.Errorf("message %s names thread %s, which the source has no row "+
					"for, and it resolved to %+v", m.MsgID, m.ThreadID, got)
			}
			dangling++
			continue
		}
		if !ok || got.ThreadID != m.ThreadID {
			return fmt.Errorf("message %s resolves to %+v (found=%v), want thread %s",
				m.MsgID, got, ok, m.ThreadID)
		}
		resolved++
	}
	fmt.Printf("verified: %d threads read back, %d messages resolve to theirs, %d dangling\n",
		len(snap.Threads), resolved, dangling)
	return nil
}
