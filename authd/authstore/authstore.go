// Package authstore is authd's storage layer: the one package in authd that
// holds SQL. The daemon asks it for what a request needs and it answers with
// rows, so a second driver reimplements this package and no handler changes
// (spec 6/37 step 1). db_utils/sql_layer_test.go fails the build if a statement
// appears anywhere else in authd.
//
// Every statement here runs against auth.db, which authd owns and migrates
// alone (spec 5/16 step 7). One file per table it reads, except that
// auth_users.go also carries oauth_identities: no operation touches an identity
// without the account it belongs to, and 6/37's third rule keeps such an
// operation whole in the file of the entity it protects.
//
// The schema itself stays in authd/migrations/*.sql, embedded by the daemon,
// because db_utils.Migrate runs it before this package has a Store to run it
// on.
package authstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// Store holds the auth.db handle every statement in this package runs against.
type Store struct{ db *sql.DB }

// New wraps an auth.db handle.
func New(db *sql.DB) *Store { return &Store{db: db} }

// execer is the write surface *sql.DB and *sql.Tx share, so a statement that
// runs both standalone and inside a caller's transaction has ONE owner.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// HashToken maps a raw credential to the value stored for it. A refresh token
// is persisted by its hash alone, so this is how a presented secret addresses
// its row.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// now is the timestamp format every column in auth.db is written in.
func now() string { return time.Now().Format(time.RFC3339) }
