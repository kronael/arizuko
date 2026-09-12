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
// ROWS BEFORE CURSORS, AND THE ORDER IS NOW IMPOSSIBLE TO GET WRONG.
// `chats.agent_cursor` holds a whole message KEY, which does not exist until the
// messages are items — so store.ImportHot writes the rows and then derives each
// chat's cursor as the greatest key at or before the instant it held. The
// precondition this tool used to CHECK is now one the shape enforces.
// `sessions.observed_cursor` is still a bare instant and is still carried
// verbatim, because the ambient window compares it `> cursor` over the newest
// rows and a bound just past an instant is exactly what that meant.
//
// `messages` MOVES HERE TOO, and it is what makes the cursor derivable at all.
// `messages_fts` is not an entity and does not move: ranked search leaves the
// store and becomes its own service (spec 6/37 §"Search is the one that is
// answered by leaving"), so the FTS5 table stays where it is. It is REBUILT once
// the rows have landed, because the index this run sets aside holds the legacy
// table's rowids and the entity's rows are new ones (store.EnsureMessageSearch).

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
	if err := db_utils.RequireDB(path); err != nil {
		return err
	}
	db, err := db_utils.Open(path)
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
	if err := store.EnsureEntitySet(ctx, s, store.OwnerRoutd,
		append(slices.Clone(store.HotEntities), store.MessageEntities...)...); err != nil {
		return err
	}
	st := store.New(db)
	counts, err := st.ImportHot(ctx, snap)
	if err != nil {
		return err
	}
	// The messages are items now, so the index over their content is built again
	// from them: the one this run set aside indexes the legacy table's rowids.
	if err := store.EnsureMessageSearch(ctx, db); err != nil {
		return err
	}
	fmt.Println()
	for _, entity := range store.HotEntities {
		fmt.Printf("  wrote %-19s %d\n", entity, counts[entity])
	}
	return verifyHot(ctx, st, snap, before)
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

// cursorRow is one cursor as the legacy table holds it, plus the number of rows
// that sit after it — which is what the move has to leave unchanged.
type cursorRow struct {
	coord  string
	value  string
	unfed  int
	derive bool
}

// readCursors reads every stored cursor out of the legacy tables, and for the
// DERIVED one also counts the rows it leaves unfed.
//
// It exists because an unchanged cursor POSITION is the thing this move has to
// prove: a count says every row travelled, and only the position says none of
// them moved in time. `sessions.observed_cursor` is carried verbatim, so its
// value is comparable directly. `chats.agent_cursor` is DERIVED — its new value
// names the newest message at or before the old instant, which is a different
// instant on purpose — so what is compared is the set it leaves: the rows after
// the old cursor, against the rows after the new one.
func readCursors(db *sql.DB, src map[string]string) (map[string][]cursorRow, error) {
	out := map[string][]cursorRow{}
	reads := []struct {
		entity string
		query  string
		derive bool
	}{
		{entity: store.Chats, derive: true,
			query: "SELECT jid, COALESCE(agent_cursor,'') FROM %s ORDER BY jid"},
		{entity: store.Sessions,
			query: "SELECT group_folder || '\x1f' || topic, COALESCE(observed_cursor,'') " +
				"FROM %s ORDER BY group_folder, topic"},
	}
	msgs := src[store.Messages]
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
			c := cursorRow{derive: r.derive}
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
		if r.derive && msgs != "" {
			for i := range list {
				if list[i].value == "" {
					continue // no cursor, so every row is unfed either way
				}
				n, err := unfedRows(db, msgs, list[i].coord, list[i].value)
				if err != nil {
					return nil, err
				}
				list[i].unfed = n
			}
		}
		out[r.entity] = list
	}
	return out, nil
}

// unfedRows counts the messages a chat's stored cursor leaves after it, read off
// the LEGACY table before the move touches anything.
//
// IT COMPARES INSTANTS, NOT BYTES, and that is the whole difficulty. Both sides
// of the comparison are exactly what BUGS L97 is about: the stored cursor may be
// short, the rows may be short, and either mismatch makes a byte comparison
// disagree with time inside one second. A SQL `timestamp > ?` here would measure
// the bug rather than the move — a correction would read as a shift and abort a
// migration that had just done its job. So each row is parsed and compared as a
// moment, which is what the cursor MEANT whatever width it was written at.
//
// The read is one chat's rows, once, in a tool that runs once with the daemon
// stopped.
func unfedRows(db *sql.DB, table, jid, cursor string) (int, error) {
	at, err := readInstant(cursor)
	if err != nil {
		return 0, fmt.Errorf("read %s's cursor %q: %w", jid, cursor, err)
	}
	rows, err := db.Query("SELECT timestamp FROM "+table+" WHERE chat_jid = ?", jid)
	if err != nil {
		return 0, fmt.Errorf("count %s's unfed rows: %w", jid, err)
	}
	defer rows.Close() //nolint:errcheck // the error below is the one that decides
	n := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		t, err := store.ReadMessageInstant(raw)
		if err != nil {
			// A row no position can be built from is one the move refuses, so it
			// is not a row the cursor leaves unfed either.
			continue
		}
		if t.After(at) {
			n++
		}
	}
	return n, rows.Err()
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
		{store.TurnContexts, func() (int, error) { return st.CountTurnContexts(ctx) }},
		{store.Messages, func() (int, error) { return st.CountMessages(ctx) }},
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
	return verifyCursors(ctx, st, before)
}

// verifyCursors reads every cursor back and reports whether any names a
// different POSITION than it did.
//
// IT IS THE HALF THAT MATTERS: a count says every row travelled, and only the
// position says none of them moved in time. A carried cursor may change its
// RENDERING and never the instant it names. A DERIVED one changes its instant on
// purpose — it names the newest consumed message rather than the moment the
// poller stopped — so what is compared there is the set: the rows the old cursor
// left unfed, against the rows the new one leaves.
func verifyCursors(ctx context.Context, st *store.Store, before map[string][]cursorRow) error {
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
			if row.derive {
				unfed, err := st.NewMessagesForChat(ctx, row.coord, now, 0)
				if err != nil {
					return err
				}
				if len(unfed) != row.unfed {
					fmt.Printf("  MOVED    %s %s: %d rows were unfed, %d are now\n",
						entity, row.coord, row.unfed, len(unfed))
					moved++
					continue
				}
				if now != row.value {
					repadded++
				}
				continue
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
		fmt.Printf("  verified %-19s %d cursors, same position (%d re-rendered)\n",
			entity, len(rows), repadded)
	}
	if moved > 0 {
		return fmt.Errorf("%d cursors name a different position than they did; a cursor that "+
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

// readInstant parses one stored cursor.
//
// It is store.ReadMessageInstant and nothing else, because a cursor and the rows
// it is compared against have to be read the SAME way: a move that read a naive
// datetime as UTC in a row and refused it in a cursor would shift one relative
// to the other, which is the failure the whole ordering discipline exists to
// stop.
func readInstant(raw string) (time.Time, error) { return store.ReadMessageInstant(raw) }

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
