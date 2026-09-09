package main

// migrate_onbod.go moves onbod's three tables onto the entities it reads
// (spec 6/37 step 7). One way, once, with onbod stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to
// store.ImportOnbod, which owns the item shape. onbod and store hold no
// statement against them any more, so the statements below are here.
//
// THE DATABASE IS onbod.db, and the daemon that must be stopped is onbod.
// dashd and the CLI hold the same file through store.OpenOnbod, so they must be
// idle too.
//
// The old tables are RENAMED, not dropped. `onboarding_v1` is the admission
// history an operator may still want to read, and leaving it costs nothing. The
// rename is also what lets an entity keep the name of the table it replaces.
//
// `audit_log` is NOT here even though onbod.db carries one: it belongs to
// audit, four owners share the entity, and `arizuko migrate-audit` moves all
// four in one run. Two tools that both claimed it would be two things that can
// disagree about one table.
//
// `invite_redemptions` is here too, and its coordinate is INVENTED: the rows
// were addressed by an AUTOINCREMENT id nothing outside onbod's own read-then-
// delete pair ever held, so the move carries the instant they were redeemed at
// and that id as the suffix which keeps two of one second in the order the
// operator read them in.

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
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// onbodTables are the three onbod used to own as columns in onbod.db. The order
// is the order an operator reads the report in.
var onbodTables = []string{"onboarding", "invites", "onboarding_gates", "invite_redemptions"}

// cmdMigrateOnbod is `arizuko migrate-onbod`.
func cmdMigrateOnbod(args []string) {
	fs := flag.NewFlagSet("migrate-onbod", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the onbod.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-onbod <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-onbod --db <path/to/onbod.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		path = store.OwnerDBPath(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"), store.OwnerOnbod)
	}
	if err := migrateOnbod(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateOnbod reads path's legacy tables, writes the entities, and reads them
// back through onbod's own methods to check what landed.
func migrateOnbod(path string, dryRun bool) error {
	if err := db_utils.RequireDBFile(path); err != nil {
		return err
	}
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, onbodTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(onbodTables, ", "))
		return nil
	}
	snap, err := readOnbodSnapshot(db, sources)
	if err != nil {
		return err
	}
	reportSource(path, onbodTables, sources, map[string]int{
		"onboarding":         len(snap.Admissions),
		"invites":            len(snap.Invites),
		"onboarding_gates":   len(snap.Gates),
		"invite_redemptions": len(snap.Redemptions),
	})
	if _, err := store.CheckOnbodSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, onbodTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	if err := store.EnsureEntities(ctx, s, store.OwnerOnbod); err != nil {
		return err
	}
	// The version marker too: it is what a replace claims and a consistent read
	// compares, and Bump is an Update, so a marker nobody created would have the
	// whole batch carrying it refused (objstore.EnsureScope).
	if err := resreg.EnsureSubsystem(ctx, s, s, resreg.SubsystemOnbod); err != nil {
		return err
	}
	st := store.New(db)
	counts, err := st.ImportOnbod(ctx, snap)
	if err != nil {
		return err
	}
	fmt.Printf("\nwrote %d admissions, %d invites, %d gates, %d redemptions\n",
		counts.Admissions, counts.Invites, counts.Gates, counts.Redemptions)
	return verifyOnbod(st, snap)
}

// readOnbodSnapshot reads every legacy row of whichever tables src names. A NULL
// column arrives as the empty string, which is what an absent attribute means on
// the other side.
func readOnbodSnapshot(db *sql.DB, src map[string]string) (store.OnbodSnapshot, error) {
	var snap store.OnbodSnapshot
	if t, ok := src["onboarding"]; ok {
		rows, err := db.Query(`SELECT jid, status, user_sub, gate, created,
			prompted_at, queued_at, admitted_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r                                         store.OnboardingRow
				userSub, gate, prompted, queued, admitted sql.NullString
			)
			if err := rows.Scan(&r.JID, &r.Status, &userSub, &gate, &r.Created,
				&prompted, &queued, &admitted); err != nil {
				return snap, err
			}
			r.UserSub, r.Gate = userSub.String, gate.String
			r.PromptedAt, r.QueuedAt, r.AdmittedAt = prompted.String, queued.String, admitted.String
			snap.Admissions = append(snap.Admissions, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["invites"]; ok {
		rows, err := db.Query(`SELECT ref, target_glob, issued_by_sub, issued_at,
			expires_at, max_uses, used_count FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r       store.ArchiveInviteRow
				expires sql.NullString
			)
			if err := rows.Scan(&r.Ref, &r.TargetGlob, &r.IssuedBySub, &r.IssuedAt,
				&expires, &r.MaxUses, &r.UsedCount); err != nil {
				return snap, err
			}
			r.ExpiresAt = expires.String
			snap.Invites = append(snap.Invites, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["onboarding_gates"]; ok {
		rows, err := db.Query(`SELECT gate, limit_per_day, enabled FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				g  store.OnboardingGate
				en int
			)
			if err := rows.Scan(&g.Gate, &g.LimitPerDay, &en); err != nil {
				return snap, err
			}
			g.Enabled = en != 0
			snap.Gates = append(snap.Gates, g)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["invite_redemptions"]; ok {
		rows, err := db.Query(`SELECT id, user_sub, target_glob, redeemed_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var r store.RedemptionRow
			if err := rows.Scan(&r.RowID, &r.UserSub, &r.TargetGlob, &r.RedeemedAt); err != nil {
				return snap, err
			}
			snap.Redemptions = append(snap.Redemptions, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// verifyOnbod reads the entities back through the methods onbod itself calls and
// compares them with the source. It is the "export again and compare" half of
// the move: an import that wrote the wrong attribute name would report its
// counts happily, and only a read through the real methods catches it.
func verifyOnbod(st *store.Store, snap store.OnbodSnapshot) error {
	got, err := st.ListOnboarding("")
	if err != nil {
		return fmt.Errorf("read the admissions back: %w", err)
	}
	if len(got) != len(snap.Admissions) {
		return fmt.Errorf("%d admissions read back, %d were written",
			len(got), len(snap.Admissions))
	}
	want := map[string]store.OnboardingRow{}
	for _, r := range snap.Admissions {
		want[r.JID] = r
	}
	for _, r := range got {
		w, ok := want[r.JID]
		if !ok {
			return fmt.Errorf("admission %s reads back and was never written", r.JID)
		}
		if r != w {
			return fmt.Errorf("admission %s reads back %+v, want %+v", r.JID, r, w)
		}
	}
	invites, err := st.ListInvites("")
	if err != nil {
		return fmt.Errorf("read the invites back: %w", err)
	}
	if len(invites) != len(snap.Invites) {
		return fmt.Errorf("%d invites read back, %d were written",
			len(invites), len(snap.Invites))
	}
	gates, err := st.ListGates()
	if err != nil {
		return fmt.Errorf("read the gates back: %w", err)
	}
	if len(gates) != len(snap.Gates) {
		return fmt.Errorf("%d gates read back, %d were written", len(gates), len(snap.Gates))
	}
	// The redemptions are read back PER PERSON, because the person is the
	// partition: there is no whole-entity read, and the read onbod itself calls
	// names one sub. A redemption that landed under the wrong sub is invisible to
	// its own owner, which is exactly what this catches.
	held := map[string]int{}
	for _, r := range snap.Redemptions {
		held[r.UserSub]++
	}
	redemptions := 0
	for sub, n := range held {
		back, err := st.CountRedemptions(context.Background(), sub)
		if err != nil {
			return fmt.Errorf("read %s's redemptions back: %w", sub, err)
		}
		if back != n {
			return fmt.Errorf("%d redemptions read back for %s, %d were written", back, sub, n)
		}
		redemptions += back
	}
	fmt.Printf("  verified: %d admissions, %d invites, %d gates, %d redemptions\n",
		len(got), len(invites), len(gates), redemptions)
	return nil
}
