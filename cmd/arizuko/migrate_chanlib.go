package main

// migrate_chanlib.go moves an adapter database from the two tables chanlib used
// to run statements against onto the entities chanstore reads (spec 6/37 step
// 7). One way, once, with the adapter stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to
// chanstore.Import, which owns the item shape. chanstore holds no SQL, so the
// statements below are here.
//
// The old tables are RENAMED, not dropped. `outbound_dedup_v1` is the evidence
// an operator needs if a message arrives twice, and leaving it costs nothing —
// no adapter opens it again. The rename is also what lets an entity keep the
// name of the table it replaces.
//
// IT MIGRATES A WHOLE INSTANCE, not one file, and that is what makes it
// different from its two neighbours. chanlib's entities land in EVERY adapter's
// own database — eight of them, one per running channel — and an instance may
// hold two of one kind, so the databases are FOUND under the instance's store
// rather than named. A daemon whose database the run missed keeps its old
// tables and its adapter then dedups against nothing.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// chanlibTables are the two chanlib used to own.
var chanlibTables = []string{"outbound_dedup", "inbound_pending"}

// chanlibDaemons are the daemons that open an adapter database, so a file named
// `<daemon>.db` under an instance's store is one of these. The DIRECTORY is the
// instance's channel name and the FILE is the daemon's, which is how krons
// carries both `store/teled/teled.db` and `store/teled-rhias/teled.db`.
var chanlibDaemons = []string{
	"bskyd", "discd", "emaid", "gitd", "linkd", "reditd", "slakd", "teled",
}

// cmdMigrateChanlib is `arizuko migrate-chanlib`.
func cmdMigrateChanlib(args []string) {
	fs := flag.NewFlagSet("migrate-chanlib", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the sources and check them; write nothing")
	dbPath := fs.String("db", "", "one adapter database to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-chanlib <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-chanlib --db <path/to/teled.db> [--dry-run]")
		os.Exit(1)
	}
	paths := []string{*dbPath}
	if *dbPath == "" {
		found, err := adapterDBs(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"))
		if err != nil {
			die("Failed: %v", err)
		}
		if len(found) == 0 {
			die("Failed: no adapter database under %s/store; chanlib's entities live in one "+
				"file per channel, so an instance with no channel has nothing to migrate",
				mustInstanceDir(fs.Arg(0)))
		}
		paths = found
	}
	for _, path := range paths {
		if err := migrateChanlib(path, *dryRun); err != nil {
			die("Failed on %s: %v", path, err)
		}
	}
}

// adapterDBs finds every adapter database under an instance's store. It looks
// for a daemon's own filename inside each channel directory rather than
// assuming the two agree: a multi-account channel names its directory after the
// account and its file after the daemon.
func adapterDBs(storeDir string) ([]string, error) {
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, daemon := range chanlibDaemons {
			path := filepath.Join(storeDir, e.Name(), daemon+".db")
			if _, err := os.Stat(path); err == nil {
				found = append(found, path)
			}
		}
	}
	sort.Strings(found)
	return found, nil
}

// migrateChanlib reads path's legacy tables, writes the entities, and reads them
// back through chanstore's own methods to check what landed.
func migrateChanlib(path string, dryRun bool) error {
	if err := db_utils.RequireDBFile(path); err != nil {
		return err
	}
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, chanlibTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(chanlibTables, ", "))
		return nil
	}
	snap, err := readChanlibSnapshot(db, sources)
	if err != nil {
		return err
	}
	reportSource(path, chanlibTables, sources, map[string]int{
		"outbound_dedup":  len(snap.Claims),
		"inbound_pending": len(snap.Held),
	})
	if _, err := chanstore.CheckSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("  --dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, chanlibTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	for _, entity := range chanstore.Entities {
		if err := s.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	st := chanstore.New(s)
	written, err := st.Import(ctx, snap)
	if err != nil {
		return err
	}
	fmt.Printf("  wrote %d delivery claims and %d retained inbound\n",
		written.Claims, written.Held)
	return verifyChanlib(st, snap)
}

// readChanlibSnapshot reads every legacy row. A NULL column arrives as the empty
// string, which is what an absent attribute means on the other side.
func readChanlibSnapshot(db *sql.DB, src map[string]string) (chanstore.Snapshot, error) {
	var snap chanstore.Snapshot
	if t, ok := src["outbound_dedup"]; ok {
		rows, err := db.Query(
			`SELECT idempotency_key, chat_jid, state, platform_id, created_at, settled_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				c                   chanstore.ClaimRow
				platform, settledAt sql.NullString
			)
			if err := rows.Scan(&c.IdempotencyKey, &c.ChatJID, &c.State,
				&platform, &c.CreatedAt, &settledAt); err != nil {
				return snap, err
			}
			c.PlatformID, c.SettledAt = platform.String, settledAt.String
			snap.Claims = append(snap.Claims, c)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["inbound_pending"]; ok {
		rows, err := db.Query(
			`SELECT id, payload, attempts, first_seen_at, next_attempt_at, last_error FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				h        chanstore.HeldRow
				lastErr  sql.NullString
				attempts sql.NullInt64
			)
			if err := rows.Scan(&h.ID, &h.Payload, &attempts,
				&h.FirstSeenAt, &h.NextAttemptAt, &lastErr); err != nil {
				return snap, err
			}
			h.Attempts, h.LastError = int(attempts.Int64), lastErr.String
			snap.Held = append(snap.Held, h)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// verifyChanlib reads the entities back through the methods chanlib itself calls
// and compares them with the source. It is the "export again and compare" half
// of the move: an import that wrote the wrong attribute name would still report
// its counts happily, and only a read through the real methods catches it.
//
// The claims are read through RecordedClaim, which is the read ClaimDelivery
// itself performs — the one that decides whether a re-sent message is posted
// twice. It is that read and not the claim, because a claim on a key the import
// missed would CREATE the record and the check would repair its own evidence.
func verifyChanlib(st *chanstore.Store, snap chanstore.Snapshot) error {
	ctx := context.Background()
	for _, c := range snap.Claims {
		got, held, err := st.RecordedClaim(ctx, c.IdempotencyKey)
		if err != nil {
			return fmt.Errorf("read claim %s back: %w", c.IdempotencyKey, err)
		}
		if !held {
			return fmt.Errorf("claim %s is not in the store", c.IdempotencyKey)
		}
		want := chanstore.ClaimInflight
		if c.State == "sent" {
			want = chanstore.ClaimSent
		}
		if got.State != want {
			return fmt.Errorf("claim %s reads back as %v, want %v (source state %q)",
				c.IdempotencyKey, got.State, want, c.State)
		}
		if want == chanstore.ClaimSent && got.PlatformID != c.PlatformID {
			return fmt.Errorf("claim %s reads back with platform id %q, want %q",
				c.IdempotencyKey, got.PlatformID, c.PlatformID)
		}
	}
	// The held messages are read through the sweep's own read, far enough ahead
	// that no backoff hides one — a message the sweep cannot see is a message
	// that is never re-delivered.
	due, err := st.DueForRedelivery(ctx, farFuture, 0)
	if err != nil {
		return fmt.Errorf("read the held inbound back: %w", err)
	}
	held := map[string]chanstore.Held{}
	for _, d := range due {
		held[d.ID] = d
	}
	for _, h := range snap.Held {
		got, ok := held[h.ID]
		if !ok {
			return fmt.Errorf("held message %s is not in the store", h.ID)
		}
		if got.Payload != h.Payload || got.Attempts != h.Attempts {
			return fmt.Errorf("held message %s reads back on attempt %d with %d payload bytes, "+
				"want attempt %d and %d", h.ID, got.Attempts, len(got.Payload),
				h.Attempts, len(h.Payload))
		}
	}
	fmt.Printf("  verified: %d claims answer as they did, %d retained the sweep can see\n",
		len(snap.Claims), len(snap.Held))
	return nil
}

// farFuture is later than any backoff a held message carries, so the sweep's own
// read answers with all of them. The ceiling is 15 minutes (chanlib's
// inboundBackoffMax); a year is past it by any margin an operator cares about.
const farFuture = "2999-01-01T00:00:00.000000000Z"
