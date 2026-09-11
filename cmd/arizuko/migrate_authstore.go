package main

// migrate_authstore.go moves an existing auth.db from the four tables authd
// used to run statements against onto the entities authstore reads (spec 6/37
// step 7). One way, once, with the daemon stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to
// authstore.Import, which owns the item shape. authstore holds no SQL, so the
// statements below are here.
//
// The old tables are RENAMED, not dropped. `refresh_tokens_v1` is the evidence
// an operator needs if a session stops working, and leaving it costs nothing —
// authd never opens it again. The rename is also what lets an entity keep the
// name of the table it replaces.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// authstoreTables are the four authd used to own. The suffix is what a migrated
// database carries any set-aside table under, whichever daemon moved.
var authstoreTables = []string{"signing_keys", "auth_users", "oauth_identities", "refresh_tokens"}

const legacySuffix = "_v1"

// cmdMigrateAuthstore is `arizuko migrate-authstore`.
func cmdMigrateAuthstore(args []string) {
	fs := flag.NewFlagSet("migrate-authstore", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the auth.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-authstore <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-authstore --db <path/to/auth.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		path = store.OwnerDBPath(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"), store.OwnerAuthd)
	}
	if err := migrateAuthstore(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateAuthstore reads path's legacy tables, writes the entities, and reads
// them back through authstore's own methods to check what landed.
func migrateAuthstore(path string, dryRun bool) error {
	if err := db_utils.RequireDB(path); err != nil {
		return err
	}
	db, err := db_utils.Open(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, authstoreTables)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(authstoreTables, ", "))
		return nil
	}
	snap, err := readAuthstoreSnapshot(db, sources)
	if err != nil {
		return err
	}
	reportSource(path, authstoreTables, sources, map[string]int{
		"signing_keys":     len(snap.Keys),
		"auth_users":       len(snap.Users),
		"oauth_identities": len(snap.Identities),
		"refresh_tokens":   len(snap.Tokens),
	})
	if dryRun {
		// The check runs on a dry run too, so an operator learns that a family
		// cannot be represented BEFORE the run that would stop half way.
		if err := authstore.CheckSnapshot(snap); err != nil {
			return err
		}
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, authstoreTables, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	for _, entity := range authstore.Entities {
		if err := s.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	st := authstore.New(s)
	counts, err := st.Import(snap)
	if err != nil {
		return err
	}
	fmt.Printf("\nwrote %d signing keys (%d holding the slot), %d accounts, %d identities, "+
		"%d refresh tokens in %d families\n",
		counts.Keys, counts.Slot, counts.Users, counts.Identities, counts.Tokens, counts.Families)
	return verifyAuthstore(st, snap)
}

// legacySources maps each of tables to the name it is readable under: its own
// if the move has not run, the `_v1` name if it has. A table under neither name
// is absent from the map, which is how a database a daemon created after the
// move reports that there is nothing to read.
//
// PRESENCE IS NOT ENOUGH, and finding that out cost a confusing failure. An
// entity keeps the name of the table it replaces, so `signing_keys` exists
// either way; run this on a migrated database whose `_v1` tables an operator
// has since deleted and the read asks the three-column entity for `kid`. So a
// table counts as a source only if it is NOT an entity, and the shape is what
// says which.
func legacySources(db *sql.DB, tables []string) (map[string]string, error) {
	out := map[string]string{}
	for _, table := range tables {
		for _, name := range []string{table + legacySuffix, table} {
			legacy, err := isLegacyShape(db, name)
			if err != nil {
				return nil, err
			}
			if legacy {
				out[table] = name
				break
			}
		}
	}
	return out, nil
}

// isLegacyShape reports whether name is a table holding the old columns. An
// absent table is not, and neither is an objstore entity — those are exactly
// (part, sort, attrs), which is what sqlstore.CreateEntity writes.
func isLegacyShape(db *sql.DB, name string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, name)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return false, err
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(cols) == 0 {
		return false, nil
	}
	return strings.Join(cols, ",") != "part,sort,attrs", nil
}

// setAside renames each table the move is about to replace, so the rows an
// operator may need as evidence stay on disk and the entity can keep the name
// of the table it replaces.
func setAside(db *sql.DB, tables []string, src map[string]string) error {
	for _, table := range tables {
		if src[table] != table {
			continue // already set aside by an earlier run, or absent
		}
		if _, err := db.Exec("ALTER TABLE " + table + " RENAME TO " + table + legacySuffix); err != nil {
			return fmt.Errorf("set %s aside: %w", table, err)
		}
		fmt.Printf("  set aside: %s -> %s\n", table, table+legacySuffix)
	}
	return nil
}

// readAuthstoreSnapshot reads every legacy row. A NULL column arrives as the
// empty string, which is what an absent attribute means on the other side.
func readAuthstoreSnapshot(db *sql.DB, src map[string]string) (authstore.Snapshot, error) {
	var snap authstore.Snapshot
	if t, ok := src["signing_keys"]; ok {
		rows, err := db.Query(`SELECT kid, priv_pem, pub_pem, active, created_at, retired_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				k       authstore.KeyRow
				active  int
				retired sql.NullString
			)
			if err := rows.Scan(&k.Kid, &k.PrivPEM, &k.PubPEM, &active, &k.CreatedAt, &retired); err != nil {
				return snap, err
			}
			k.Active, k.RetiredAt = active == 1, retired.String
			snap.Keys = append(snap.Keys, k)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["auth_users"]; ok {
		rows, err := db.Query(`SELECT user_id, name, created_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var u authstore.UserRow
			if err := rows.Scan(&u.UserID, &u.Name, &u.CreatedAt); err != nil {
				return snap, err
			}
			snap.Users = append(snap.Users, u)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["oauth_identities"]; ok {
		rows, err := db.Query(`SELECT user_id, provider, provider_sub, linked_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var i authstore.IdentityRow
			if err := rows.Scan(&i.UserID, &i.Provider, &i.ProviderSub, &i.LinkedAt); err != nil {
				return snap, err
			}
			snap.Identities = append(snap.Identities, i)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	if t, ok := src["refresh_tokens"]; ok {
		rows, err := db.Query(`SELECT token_hash, family_id, sub, scope, aud, issued_at,
		                              expires_at, used_at, revoked_at FROM ` + t)
		if err != nil {
			return snap, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r             authstore.TokenRow
				used, revoked sql.NullString
			)
			if err := rows.Scan(&r.TokenHash, &r.FamilyID, &r.Sub, &r.Scope, &r.Aud,
				&r.IssuedAt, &r.ExpiresAt, &used, &revoked); err != nil {
				return snap, err
			}
			r.UsedAt, r.RevokedAt = used.String, revoked.String
			snap.Tokens = append(snap.Tokens, r)
		}
		if err := rows.Err(); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// reportSource prints what the source holds, per table, so the operator can
// compare it with what the import writes. Zero of something is worth seeing:
// "0 rows matched" is the shape of a migration that read the wrong database,
// and it is never an all-clear on its own.
func reportSource(path string, tables []string, src map[string]string, rows map[string]int) {
	fmt.Printf("%s\n", path)
	for _, table := range tables {
		name, ok := src[table]
		if !ok {
			fmt.Printf("  %-17s absent\n", table)
			continue
		}
		fmt.Printf("  %-17s %4d rows (reading %s)\n", table, rows[table], name)
	}
}

// verify reads the entities back through the methods authd itself calls and
// compares them with the source. It is the "export again and compare" half of
// the move: an import that wrote the wrong attribute name would still report
// its counts happily, and only a read through the real methods catches it.
func verifyAuthstore(st *authstore.Store, snap authstore.Snapshot) error {
	keys, err := st.SigningKeysWithMaterial()
	if err != nil {
		return fmt.Errorf("read the signing keys back: %w", err)
	}
	if len(keys) != len(snap.Keys) {
		return fmt.Errorf("%d signing keys read back, %d were written", len(keys), len(snap.Keys))
	}
	signers := 0
	for _, k := range keys {
		if k.Active {
			signers++
		}
	}
	want := 0
	for _, k := range snap.Keys {
		if k.Active {
			want = 1
		}
	}
	if signers != want {
		return fmt.Errorf("%d keys hold the slot, %d did in the source", signers, want)
	}

	for _, i := range snap.Identities {
		owner, _, _, _, ok := st.OAuthIdentityForSub(i.Provider + ":" + i.ProviderSub)
		if !ok || owner != i.UserID {
			return fmt.Errorf("identity %s:%s reads back as %q (found=%v), want %q",
				i.Provider, i.ProviderSub, owner, ok, i.UserID)
		}
	}

	// The rotations summed over the families must equal the token count. That
	// IS the correlated COUNT(*) the listing used to run, so agreement here is
	// the round trip: the derived field answers what the subquery answered.
	rows, err := st.SessionsByFamily("", 200, time.Now())
	if err != nil {
		return fmt.Errorf("read the sessions back: %w", err)
	}
	families := map[string]bool{}
	for _, r := range snap.Tokens {
		families[r.FamilyID] = true
	}
	if len(rows) != len(families) {
		return fmt.Errorf("%d families read back, %d are in the source", len(rows), len(families))
	}
	total := 0
	for _, r := range rows {
		total += r.Rotations
		if r.StartedAt == "" || r.RenewedAt == "" || r.ExpiresAt == "" {
			return fmt.Errorf("family %s came back without its lifecycle: %+v", r.FamilyID, r)
		}
	}
	if total != len(snap.Tokens) {
		return fmt.Errorf("the families account for %d tokens, %d were written",
			total, len(snap.Tokens))
	}
	fmt.Printf("verified: %d keys (%d signing), %d identities resolve, "+
		"%d families accounting for %d tokens\n",
		len(keys), signers, len(snap.Identities), len(rows), total)
	return nil
}
