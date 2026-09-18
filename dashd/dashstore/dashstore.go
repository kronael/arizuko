// Package dashstore is dashd's storage layer: the one package in dashd that
// holds SQL. Handlers ask it for what a page needs and it answers with rows, so
// a second driver reimplements this package and no handler changes (spec 6/37
// step 1).
//
// Every read here reaches routd's own tables. routd OWNS them; dashd writes
// them directly, which is the split write-discipline root CLAUDE.md sets for a
// daemon that reaches the owner's store. The handle is nil in read-only tests
// and on an instance whose routd profile is off, so New returns nil for a nil
// handle and every page keeps its "store unavailable" banner.
package dashstore

import (
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// Store holds the handle every read and write in this package goes through.
type Store struct{ obj objstore.Store }

// New wraps routd's store handle. A nil handle yields a nil Store: dashd's
// pages test the handle before they render, and a Store over no database could
// only fail one query at a time.
func New(obj objstore.Store) *Store {
	if obj == nil {
		return nil
	}
	return &Store{obj: obj}
}

// st is routd's store as the store package reads and writes it. routd's thirteen
// registered resources are objstore entities whose coordinate and attribute
// names store OWNS, so a page that needs one asks store rather than rendering a
// second copy of the same item — two renderings of one coordinate put a write
// where the other's read does not look, and the failure is silent.
func (s *Store) st() *store.Store { return store.NewOver(s.obj) }
