package sqlstore

// dialect.go holds everything the two engines genuinely disagree about, and
// nothing they agree on. It is one driver with two emitters, not two drivers
// (spec 6/37 step 8): the statements are generated once, and only the fragments
// below differ.
//
// Two differences the design expected turned out not to exist, measured against
// PostgreSQL 17.11 and the vendored SQLite 3.51.2, so neither is in this file:
//
//   - substr(text, int, int) is a Postgres function as well as a SQLite one, so
//     the containment predicate is byte-identical on both engines. There is no
//     substr/substring split.
//   - INSERT ... ON CONFLICT (cols) DO NOTHING / DO UPDATE is SQLite syntax as
//     well as Postgres syntax. SQLite's INSERT OR IGNORE is a second spelling of
//     something both engines already share, so the generator never emits it.

import (
	"database/sql"
	"strconv"

	"github.com/kronael/arizuko/db_utils"
)

// dialect is the emitter for one engine.
type dialect struct {
	name string

	// placeholder renders the n-th bind marker, 1-based.
	placeholder func(n int) string

	// begin opens the batch's transaction as a WRITER.
	//
	// A guarded batch reads a condition and then writes on it, so the read must
	// not be able to go stale before the write lands. SQLite's plain BEGIN is
	// DEFERRED: it takes a read lock and upgrades on the first write, and in WAL
	// mode an upgrade whose snapshot another writer has moved past fails with
	// SQLITE_BUSY_SNAPSHOT — a lost batch rather than a wrong one, but lost for
	// no reason. BEGIN IMMEDIATE takes the write lock up front, which is the
	// idiom db_utils.migrate already uses for the same reason.
	//
	// Postgres has no such upgrade problem for the rows it LOCKS, which is what
	// lockRead is for.
	//
	// It is ONE statement, not a list. A list would have a partial-open state —
	// the second statement failing after the first succeeded — that returns a
	// connection to the pool holding a transaction nobody will close, and on
	// SQLite that is the write lock.
	begin string

	// lockRead is the suffix a check's read carries so a concurrent batch cannot
	// change the item between this batch's read and its write.
	//
	// Postgres runs at READ COMMITTED, where an unlocked SELECT sees a snapshot
	// another transaction may already have replaced; FOR UPDATE takes the row
	// lock the write would have taken. SQLite needs nothing here because begin
	// already holds the single writer lock for the whole database.
	lockRead string

	// attrsColumn is the DDL type of the document column.
	attrsColumn string

	// attr renders the TEXT of one attribute of the attrs document. Every stored
	// value is a string, so both engines return TEXT here and a comparison means
	// the same thing on each — which is the point of objstore's string-valued
	// attribute rule.
	attr func(name string) string

	// docBytes renders the SIZE of the stored document, which is what a page
	// bound is measured in.
	//
	// It is the size of the item as the engine holds it, not of what the query
	// projected, because DynamoDB's 1 MB "applies before any FilterExpression or
	// ProjectionExpression is applied". A driver that measured the projected
	// bytes would page later than the engine it is modelled on, and the caller
	// that relied on the longer page would break on the other one.
	docBytes string

	// boolInt renders a boolean expression as 0 or 1, so one scan reads the
	// filter verdict on both engines: SQLite yields an integer, Postgres a
	// boolean, and a comparison against a missing attribute yields NULL on
	// both — which is FALSE for a filter and must not arrive as a scan error.
	boolInt func(expr string) string

	// remove renders the document that results from taking names out of doc.
	//
	// The names are written into the statement rather than bound, for the same
	// reason attr writes them: an attribute is a key inside a JSON path and a
	// path is not a value. checkIdent is the guard that makes both safe, and it
	// is the same guard.
	//
	// Removing a name the item does not carry is a no-op on both engines, which
	// is DynamoDB's REMOVE as well: "if the attributes don't exist, nothing
	// happens".
	remove func(doc string, names []string) string

	// merge renders the document that results from merging patch into attrs.
	// patch is an already-rendered placeholder holding a JSON object.
	//
	// Neither form may carry a JSON null: SQLite's json_patch is RFC 7386
	// merge-patch, where a null DELETES the key, and Postgres's || sets the key
	// to null instead. objstore has no stored null, so the divergence is
	// unreachable rather than handled.
	merge func(patch string) string
}

var sqliteDialect = dialect{
	name:        "sqlite",
	placeholder: func(int) string { return "?" },
	begin:       "BEGIN IMMEDIATE",
	lockRead:    "",
	attrsColumn: "TEXT NOT NULL",
	attr:        func(name string) string { return "json_extract(attrs, '$." + name + "')" },
	docBytes:    "octet_length(attrs)",
	boolInt:     func(expr string) string { return "COALESCE(" + expr + ", 0)" },
	remove: func(doc string, names []string) string {
		out := "json_remove(" + doc
		for _, n := range names {
			out += ", '$." + n + "'"
		}
		return out + ")"
	},
	merge: func(patch string) string { return "json_patch(attrs, " + patch + ")" },
}

var postgresDialect = dialect{
	name:        "postgres",
	placeholder: func(n int) string { return "$" + strconv.Itoa(n) },
	begin:       "BEGIN",
	lockRead:    " FOR UPDATE",
	attrsColumn: "JSONB NOT NULL",
	attr:        func(name string) string { return "attrs ->> '" + name + "'" },
	docBytes:    "octet_length(attrs::text)",
	boolInt:     func(expr string) string { return "(COALESCE(" + expr + ", FALSE))::int" },
	remove: func(doc string, names []string) string {
		out := "(" + doc + ")"
		for _, n := range names {
			out += " - '" + n + "'::text"
		}
		return out
	},
	merge: func(patch string) string { return "attrs || (" + patch + ")::jsonb" },
}

// dialectFor reads the engine off the pool db_utils.Open returned. The DSN
// chose it; nothing here re-asks, and the driver type has one reader, in the
// package that opened the pool.
func dialectFor(db *sql.DB) dialect {
	if db_utils.IsPostgres(db) {
		return postgresDialect
	}
	return sqliteDialect
}
