// Package dashstore is dashd's storage layer: the one package in dashd that
// holds SQL. Handlers ask it for what a page needs and it answers with rows, so
// a second driver reimplements this package and no handler changes (spec 6/37
// step 1). dashd/sql_layer_test.go fails the build if a statement appears
// anywhere else in dashd.
//
// Every statement here runs against routd.db. routd OWNS those tables; dashd is
// FS-mounted on the file and reads and writes them directly, which is the split
// write-discipline root CLAUDE.md sets for a mounted daemon. The handle is nil
// in read-only tests and on an instance whose routd profile is off, so New
// returns nil for a nil handle and every page keeps its "store unavailable"
// banner.
package dashstore

import (
	"database/sql"

	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// Store holds the routd.db handle every statement in this package runs against.
type Store struct{ db *sql.DB }

// New wraps a routd.db handle. A nil handle yields a nil Store: dashd's pages
// test the handle before they render, and a Store over no database could only
// fail one query at a time.
func New(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// st is routd.db as the store package reads and writes it. routd's thirteen
// registered resources are objstore entities whose coordinate and attribute
// names store OWNS, so a page that needs one asks store rather than rendering a
// second copy of the same item — two renderings of one coordinate put a write
// where the other's read does not look, and the failure is silent.
func (s *Store) st() *store.Store { return store.New(s.db) }

// obj is the driver the audit row of an audited write reaches routd.db through.
// The mutation and the row that records it commit on one transaction, so audit
// needs the driver that can write into it, and it is the SAME driver the store
// above writes its entities through.
func (s *Store) obj() *sqlstore.Store { return s.st().Obj() }
