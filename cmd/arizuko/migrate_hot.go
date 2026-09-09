package main

// migrate_hot.go moves routd's hot-tier tables onto the entities they are now
// (spec 6/37 step 7). One way, once, with routd stopped.
//
// It is the SOURCE half of the move and the only half that knows the old schema:
// it reads the tables into plain values and hands them to store.ImportHot, which
// owns the item shape. It shares every source-side half with `migrate-routd` —
// legacySources, setAside, reportSource, the column reader and the row drain —
// because a second copy of any of them is a second thing that can disagree about
// what a legacy table is.
//
// THE DATABASE IS routd.db, and the daemon that must be stopped is routd. dashd
// reads `chat_proactive` off the same file, so it must be idle too.
//
// ROWS BEFORE CURSORS, CHECKED RATHER THAN REMEMBERED. `chats.agent_cursor` and
// `sessions.observed_cursor` are compared against `messages.timestamp`, and
// moving a cursor relative to its rows is what re-fed 28 consumed messages on
// krons (routd migration 0044). store.ImportHot carries both VERBATIM and
// refuses a cursor of the wrong width; this file refuses to start at all while
// any message row that migration would pad is still unpadded. So the only order
// in which this tool can run is the safe one.
//
// `messages` is NOT here and does not move. Ranked search leaves the store and
// becomes its own service (spec 6/37 §"Search is the one that is answered by
// leaving"), so `messages` and its FTS table wait for that decision — which is
// also why this tool can check them: they are still a table of columns.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// cmdMigrateHot is `arizuko migrate-hot`.
func cmdMigrateHot(args []string) {
	fs := flag.NewFlagSet("migrate-hot", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "read the source and check it; write nothing")
	dbPath := fs.String("db", "", "the routd.db to migrate, instead of an instance's own")
	if err := flexParse(fs, args); err != nil || (fs.NArg() != 1 && *dbPath == "") {
		fmt.Println("usage: arizuko migrate-hot <instance> [--dry-run]")
		fmt.Println("       arizuko migrate-hot --db <path/to/routd.db> [--dry-run]")
		os.Exit(1)
	}
	path := *dbPath
	if path == "" {
		path = store.OwnerDBPath(filepath.Join(mustInstanceDir(fs.Arg(0)), "store"), store.OwnerRoutd)
	}
	if err := migrateHot(path, *dryRun); err != nil {
		die("Failed: %v", err)
	}
}

// migrateHot reads path's legacy tables, writes the entities, and reads them
// back through the methods routd itself calls to check what landed.
func migrateHot(path string, dryRun bool) error {
	if err := db_utils.RequireDBFile(path); err != nil {
		return err
	}
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the process is about to end either way

	sources, err := legacySources(db, store.HotEntities)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Printf("%s carries none of %s: nothing to migrate.\n",
			path, strings.Join(store.HotEntities, ", "))
		return nil
	}
	if err := requirePaddedMessageRows(db, sources); err != nil {
		return err
	}
	snap, err := readHotSnapshot(db, sources, nil)
	if err != nil {
		return err
	}
	rows := map[string]int{}
	for entity, list := range snap {
		rows[entity] = len(list)
	}
	reportSource(path, store.HotEntities, sources, rows)
	before, err := readCursors(db, sources)
	if err != nil {
		return err
	}
	if _, err := store.CheckHotSnapshot(snap); err != nil {
		// The check runs on a real run too, so a row the entities cannot address
		// is named BEFORE the run that would stop half way.
		return err
	}
	if dryRun {
		fmt.Println("\n--dry-run: the source can be represented; nothing written.")
		return nil
	}

	ctx := context.Background()
	if err := setAside(db, store.HotEntities, sources); err != nil {
		return err
	}
	s := sqlstore.New(db)
	// THIS SET and nothing else: CreateEntity probes the three columns it needs,
	// so asking for routd's whole set would fail on a table a different tool has
	// not moved yet, and that tool would fail on these for the same reason.
	// Naming the subset is what lets either run first.
	if err := store.EnsureEntitySet(ctx, s, store.OwnerRoutd, store.HotEntities...); err != nil {
		return err
	}
	st := store.New(db)
	counts, err := st.ImportHot(ctx, snap)
	if err != nil {
		return err
	}
	fmt.Println()
	for _, entity := range store.HotEntities {
		fmt.Printf("  wrote %-19s %d\n", entity, counts[entity])
	}
	return verifyHot(ctx, st, snap, before)
}

// paddedRowGlobs are migration 0044's two GLOBs: the RFC3339-with-Z shapes it
// pads, minus the ones already at core.TS's width. A row matching both is one
// the migration would still rewrite.
//
// They are written here rather than parsed out of the file because a GLOB is not
// a statement this tool can run against a table it does not know the name of —
// and because what this asks is narrower than what the migration does: it counts
// rather than rewrites, and it must count exactly what the migration would
// change.
const (
	paddedRowGlob = `[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z`
	paddedNotGlob = `[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].` +
		`[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z`
)

// requirePaddedMessageRows refuses to move a cursor while the rows it compares
// against are still unpadded.
//
// It is skipped when neither cursor-bearing table is a source, because then no
// cursor moves and the rows are nobody's business here. `messages` absent is not
// an error either: a database with cursors and no messages has nothing for a
// cursor to skip.
func requirePaddedMessageRows(db *sql.DB, sources map[string]string) error {
	if _, ok := sources[store.Chats]; !ok {
		if _, ok := sources[store.Sessions]; !ok {
			return nil
		}
	}
	var unpadded int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages
		 WHERE timestamp GLOB ? AND timestamp NOT GLOB ?`, paddedRowGlob, paddedNotGlob).Scan(&unpadded)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return fmt.Errorf("check the message rows the cursors compare against: %w", err)
	}
	if unpadded == 0 {
		return nil
	}
	return fmt.Errorf("%d message rows are still at the widths routd's migration 0044 pads, "+
		"and this tool carries the two cursors that compare against them VERBATIM. Moving the "+
		"cursors first is what re-fed 28 consumed messages on krons. Run the migration "+
		"(open the database with routd, or routd.NormalizeFixedWidthTimestamps) and try again",
		unpadded)
}

// readHotSnapshot reads every legacy row of whichever tables src names.
//
// The column list comes from the SCHEMA, so a column this file does not know
// about still travels: the coordinate's fields are named by the entity's own
// declaration (store.HotKeyFields) and everything else becomes an attribute
// under its own column name.
//
// `alias` renames a legacy column onto the name the entity uses, and it exists
// for exactly one caller: the pre-split monolith spells system_messages'
// columns (group_id, origin, event, created_at) differently from routd.db's.
// Renaming here rather than in a second reader is what keeps ONE source half —
// a second would be a second thing that can disagree about what a legacy row is.
func readHotSnapshot(db *sql.DB, src map[string]string,
	alias map[string]map[string]string) (store.RoutdSnapshot, error) {
	snap := store.RoutdSnapshot{}
	for _, entity := range store.HotEntities {
		table, ok := src[entity]
		if !ok {
			continue
		}
		cols, err := tableColumnNames(db, table)
		if err != nil {
			return nil, err
		}
		named := make([]string, len(cols))
		for i, col := range cols {
			named[i] = col
			if to, has := alias[entity][col]; has {
				named[i] = to
			}
		}
		key, ok := store.HotKeyFields(entity)
		if !ok {
			return nil, fmt.Errorf("%s declares no coordinate", entity)
		}
		for _, field := range key {
			if !slices.Contains(named, field) {
				return nil, fmt.Errorf("%s has no column %q, which is part of %s's "+
					"coordinate", table, field, entity)
			}
		}
		rows, err := db.Query("SELECT " + strings.Join(cols, ", ") + " FROM " + table)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", table, err)
		}
		list, err := drainRoutdRows(rows, named, key)
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", table, err)
		}
		snap[entity] = list
	}
	return snap, nil
}

// cursorRow is one cursor as the legacy table holds it, so the report can show
// that the move left it alone.
type cursorRow struct {
	coord string
	value string
}

// readCursors reads every stored cursor out of the legacy tables.
//
// It exists because an UNCHANGED cursor is the thing this move has to prove: a
// count says every row travelled, and only the value says none of them moved in
// time. The read runs before the tables are set aside and again through the
// entities afterwards.
func readCursors(db *sql.DB, src map[string]string) (map[string][]cursorRow, error) {
	out := map[string][]cursorRow{}
	reads := []struct {
		entity string
		query  string
	}{
		{store.Chats, "SELECT jid, COALESCE(agent_cursor,'') FROM %s ORDER BY jid"},
		{store.Sessions, "SELECT group_folder || '\x1f' || topic, COALESCE(observed_cursor,'') " +
			"FROM %s ORDER BY group_folder, topic"},
	}
	for _, r := range reads {
		table, ok := src[r.entity]
		if !ok {
			continue
		}
		rows, err := db.Query(fmt.Sprintf(r.query, table))
		if err != nil {
			return nil, fmt.Errorf("read %s's cursors: %w", r.entity, err)
		}
		var list []cursorRow
		for rows.Next() {
			var c cursorRow
			if err := rows.Scan(&c.coord, &c.value); err != nil {
				rows.Close()
				return nil, err
			}
			list = append(list, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out[r.entity] = list
	}
	return out, nil
}

// verifyHot reads the entities back through the methods routd itself calls and
// compares them with the source.
//
// The counts are the first half and the CURSORS are the second, and the second
// is the one that matters: an import that carried every row and shifted one
// cursor by a byte would report itself complete and re-feed a chat's whole
// backlog.
func verifyHot(ctx context.Context, st *store.Store, snap store.RoutdSnapshot,
	before map[string][]cursorRow) error {
	fmt.Println()
	counts := []struct {
		entity string
		count  func() (int, error)
	}{
		{store.Chats, func() (int, error) { return st.CountChats(ctx) }},
		{store.Sessions, func() (int, error) { return st.CountTopics(ctx) }},
		{store.ChatReplyState, func() (int, error) { return st.CountReplyStates(ctx) }},
		{store.ChatProactive, func() (int, error) { return st.CountProactiveChats(ctx) }},
		{store.SystemMessages, func() (int, error) { return st.CountSystemMessages(ctx) }},
		{store.UserProfiles, func() (int, error) { return st.CountUserProfiles(ctx) }},
		{store.TurnResults, func() (int, error) { return st.CountTurnResults(ctx) }},
		{store.GroupWatchers, func() (int, error) { return st.CountGroupWatchers(ctx) }},
		{store.TaskRunLogs, func() (int, error) { return st.CountTaskRunLogs(ctx) }},
	}
	for _, c := range counts {
		got, err := c.count()
		if err != nil {
			return fmt.Errorf("read %s back: %w", c.entity, err)
		}
		if want := len(snap[c.entity]); got != want {
			return fmt.Errorf("%d %s read back, %d were written", got, c.entity, want)
		}
		fmt.Printf("  verified %-19s %d\n", c.entity, got)
	}
	return verifyCursors(st, before)
}

// verifyCursors reads every carried cursor back and reports whether any names a
// different instant than it did.
//
// IT IS THE HALF THAT MATTERS, and it is a function of its own because BOTH
// tools that carry these rows owe it: a count says every row travelled, and only
// the value says none of them moved in TIME. A cursor may change its RENDERING —
// a whole second is padded to core.TS's width, which is the move's whole job —
// and it may never change the instant it names, because everything between the
// two values is either re-fed to the agent or skipped for good.
func verifyCursors(st *store.Store, before map[string][]cursorRow) error {
	moved := 0
	for _, entity := range store.HotEntities {
		rows := before[entity]
		if len(rows) == 0 {
			continue
		}
		repadded := 0
		for _, row := range rows {
			now, err := cursorNow(st, entity, row.coord)
			if err != nil {
				return err
			}
			same, err := sameInstant(row.value, now)
			if err != nil {
				return fmt.Errorf("%s %s: %w", entity, row.coord, err)
			}
			if !same {
				fmt.Printf("  MOVED    %s %s: %q -> %q\n", entity, row.coord, row.value, now)
				moved++
				continue
			}
			if now != row.value {
				repadded++
			}
		}
		fmt.Printf("  verified %-19s %d cursors, same instant (%d re-rendered)\n",
			entity, len(rows), repadded)
	}
	if moved > 0 {
		return fmt.Errorf("%d cursors name a different instant than they did; a cursor that "+
			"moved back re-feeds every message between the two values, and one that moved "+
			"forward skips them", moved)
	}
	return nil
}

// sameInstant reports whether two renderings name the same moment. An empty
// value on both sides is the same absence.
func sameInstant(was, now string) (bool, error) {
	if was == "" || now == "" {
		return was == now, nil
	}
	a, err := readInstant(was)
	if err != nil {
		return false, err
	}
	b, err := readInstant(now)
	if err != nil {
		return false, err
	}
	return a.Equal(b), nil
}

// readInstant parses one stored cursor. The layouts are the ones a cursor can
// hold: core.TS's, and the two RFC3339 shapes routd's migration 0044 pads.
func readInstant(raw string) (time.Time, error) {
	for _, layout := range []string{core.TSLayout, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an instant this move can read", raw)
}

// cursorNow reads one cursor back through the entity.
func cursorNow(st *store.Store, entity, coord string) (string, error) {
	switch entity {
	case store.Chats:
		return st.AgentCursor(coord)
	case store.Sessions:
		folder, topic, _ := strings.Cut(coord, store.KeySep)
		line, ok := st.TopicLineage(folder, topic)
		if !ok {
			return "", fmt.Errorf("session %s#%s did not travel", folder, topic)
		}
		return line.ObservedCursor, nil
	}
	return "", fmt.Errorf("%s carries no cursor", entity)
}
