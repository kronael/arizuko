package main

// migrate_routd.go moves routd's thirteen tables onto the entities it reads
// (spec 6/37 step 7). One way, once, with routd stopped.
//
// It is the SOURCE half of the move and the only half that knows the old
// schema: it reads the tables into plain values and hands them to
// store.ImportRoutd, which owns the item shape. routd, onbod, dashd and proxyd
// hold no statement against them any more, so the statements below are here.
//
// THE DATABASE IS routd.db, and the daemon that must be stopped is routd. dashd,
// webd, proxyd, onbod, gitd and slakd all hold the same file — dashd and onbod
// write it — so they must be idle too.
//
// IT READS EVERY COLUMN THE TABLE HAS, from the schema rather than from a list
// in this file. Six of the thirteen carry columns no Go struct in the tree names
// — the sealed secret and its four OAuth fields, a GitHub source's webhook
// secret, a token's `kind` — and a hand-written column list is where one of them
// gets forgotten. That is the failure BUGS L265 is: a column nobody named,
// silently reset.
//
// The old tables are RENAMED, not dropped. The rows an operator may need as
// evidence stay on disk, and the rename is also what lets an entity keep the
// name of the table it replaces.
//
// `audit_log`, `pane_sessions` and `secret_use_log` are NOT here even though
// routd.db carries all three: the first belongs to audit and four owners share
// it (`arizuko migrate-audit`), and the other two moved with
// `arizuko migrate-store`. A tool that claimed a table another tool owns would
// be two things that can disagree about one table.

import (
	"context"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// cmdMigrateRoutd is `arizuko migrate-routd`.
func cmdMigrateRoutd(args []string) {
	fs := flag.NewFlagSet("migrate-routd", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the routd.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-routd <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-routd --db <path/to/routd.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		path = store.OwnerDBPath(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"), store.OwnerRoutd)
	}
	if err := migrateRoutd(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateRoutd reads path's legacy tables, writes the entities, and reads them
// back through the methods routd itself calls to check what landed.
func migrateRoutd(path string, dryRun bool) error {
	if err := db_utils.RequireDB(path); err != nil {
		return err
	}
	db, err := db_utils.Open(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, store.RoutdEntities)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(store.RoutdEntities, ", "))
		return nil
	}
	snap, err := readRoutdSnapshot(db, sources)
	if err != nil {
		return err
	}
	rows := map[string]int{}
	for entity, list := range snap {
		rows[entity] = len(list)
	}
	reportSource(path, store.RoutdEntities, sources, rows)
	if _, err := store.CheckRoutdSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, store.RoutdEntities, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	// The THIRTEEN and nothing else: CreateEntity probes the three columns it
	// needs, so asking for routd's whole set would fail on `pane_sessions` when
	// `arizuko migrate-store` has not run yet — and that tool would fail on `acl`
	// for the same reason. Naming the subset is what lets either run first.
	if err := store.EnsureEntitySet(ctx, s, store.OwnerRoutd, store.RoutdEntities...); err != nil {
		return err
	}
	// The version marker too: it is what a replace claims and a consistent read
	// compares, and Bump is an Update, so a marker nobody created would have the
	// whole batch carrying it refused (objstore.EnsureScope).
	if err := resreg.EnsureSubsystem(ctx, s, s, resreg.SubsystemRoutd); err != nil {
		return err
	}
	st := store.New(db)
	counts, err := st.ImportRoutd(ctx, snap)
	if err != nil {
		return err
	}
	fmt.Println()
	for _, entity := range store.RoutdEntities {
		fmt.Printf("  wrote %-19s %d\n", entity, counts[entity])
	}
	return verifyRoutd(st, snap)
}

// readRoutdSnapshot reads every legacy row of whichever tables src names.
//
// The column list comes from the SCHEMA, so a column this file does not know
// about still travels: the coordinate's fields are named by the entity's own
// declaration (store.RoutdKeyFields) and everything else becomes an attribute
// under its own column name. A NULL arrives as the empty string, which is what
// an absent attribute means on the other side.
func readRoutdSnapshot(db *sql.DB, src map[string]string) (store.RoutdSnapshot, error) {
	snap := store.RoutdSnapshot{}
	for _, entity := range store.RoutdEntities {
		table, ok := src[entity]
		if !ok {
			continue
		}
		cols, err := tableColumnNames(db, table)
		if err != nil {
			return nil, err
		}
		key, ok := store.RoutdKeyFields(entity)
		if !ok {
			return nil, fmt.Errorf("%s declares no coordinate", entity)
		}
		for _, field := range key {
			if !slices.Contains(cols, field) {
				return nil, fmt.Errorf("%s has no column %q, which is part of %s's "+
					"coordinate", table, field, entity)
			}
		}
		rows, err := db.Query("SELECT " + strings.Join(cols, ", ") + " FROM " + table)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", table, err)
		}
		list, err := drainRoutdRows(rows, cols, key)
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", table, err)
		}
		snap[entity] = list
	}
	return snap, nil
}

// drainRoutdRows turns one table's rows into coordinate + attributes.
func drainRoutdRows(rows *sql.Rows, cols, key []string) ([]store.RoutdRow, error) {
	var out []store.RoutdRow
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			return nil, err
		}
		row := store.RoutdRow{
			Key:   make([]string, len(key)),
			Attrs: map[string]string{},
		}
		for i, col := range cols {
			text := cellText(*(cells[i].(*any)))
			if at := slices.Index(key, col); at >= 0 {
				row.Key[at] = text
				continue
			}
			row.Attrs[col] = text
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// cellText renders one column value as the text an attribute holds.
//
// A BLOB becomes HEX, which is the one conversion this move performs on a value:
// route_tokens.token_hash is 32 raw bytes and a coordinate is text, so the
// digest is encoded here — the same hex store.TokenRef renders, so a token
// minted before the move resolves after it.
func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return hex.EncodeToString(t)
	case string:
		return t
	case int64:
		return fmt.Sprintf("%d", t)
	case bool:
		if t {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// tableColumnNames reads one table's columns in declaration order.
func tableColumnNames(db *sql.DB, table string) ([]string, error) {
	q := `SELECT name FROM pragma_table_info(?)`
	if db_utils.IsPostgres(db) {
		q = `SELECT column_name FROM information_schema.columns ` +
			`WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`
	}
	rows, err := db.Query(q, table)
	if err != nil {
		return nil, fmt.Errorf("columns of %s: %w", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// verifyRoutd reads the entities back through the methods routd itself calls and
// compares the counts with the source. It is the "read it back" half of the
// move: an import that wrote the wrong attribute name would report its counts
// happily, and only a read through the real methods catches it.
func verifyRoutd(st *store.Store, snap store.RoutdSnapshot) error {
	ctx := context.Background()
	checks := []struct {
		entity string
		count  func() (int, error)
	}{
		{store.Groups, func() (int, error) { return len(st.AllGroups()), nil }},
		{store.ACL, func() (int, error) {
			rows, err := st.ListACL("")
			return len(rows), err
		}},
		{store.Routes, func() (int, error) {
			rows, err := st.RouteTable()
			return len(rows), err
		}},
		{store.RouteTokens, func() (int, error) { return st.CountRouteTokens(ctx) }},
		{store.NetworkRules, func() (int, error) {
			rows, err := st.AllNetworkRules()
			return len(rows), err
		}},
		{store.ProxydRoutes, func() (int, error) {
			rows, err := st.AllProxydRoutes()
			return len(rows), err
		}},
		{store.WebRoutes, func() (int, error) {
			rows, err := st.AllWebRoutes()
			return len(rows), err
		}},
	}
	fmt.Println()
	for _, c := range checks {
		got, err := c.count()
		if err != nil {
			return fmt.Errorf("read %s back: %w", c.entity, err)
		}
		want := len(snap[c.entity])
		if c.entity == store.RouteTokens {
			// CountRouteTokens counts BOTH kinds, which is every row the table
			// held, so the comparison is against the whole source.
			want = len(snap[store.RouteTokens])
		}
		if got != want {
			return fmt.Errorf("%d %s read back, %d were written", got, c.entity, want)
		}
		fmt.Printf("  verified %-19s %d\n", c.entity, got)
	}
	return nil
}
