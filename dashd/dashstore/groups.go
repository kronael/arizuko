package dashstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/store"
)

// ErrSeedMemberRole and ErrGrantAdmin name the two steps of a group create the
// operator can act on: one says the folder would expand to nothing, the other
// that nobody would be able to open it. Every other step reports as a plain
// failed insert, because the operator's next move is the same either way.
var (
	ErrSeedMemberRole = errors.New("seed member role")
	ErrGrantAdmin     = errors.New("grant admin")
)

// GroupExists reports whether a folder already has a group row.
func (s *Store) GroupExists(folder string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, folder).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CreateGroupWithAdmin writes the group row, its member edge and the creator's
// admin grant in one transaction, with the audit row inside it.
//
// The three belong together. A group row with no member edge expands to
// nothing, matches zero allow rows and comes up with no messaging tools
// (BUGS L119); a group row with no admin grant is a folder nobody can open.
// store.PutGroup writes the row on a transaction of its own and so cannot
// carry either, which is why the insert is here rather than a call to it.
func (s *Store) CreateGroupWithAdmin(ctx context.Context, folder, product, admin string, ev audit.Event) error {
	now := time.Now().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO groups (folder, added_at, product, updated_at) VALUES (?, ?, ?, ?)`,
		folder, now, product, now); err != nil {
		return fmt.Errorf("insert group: %w", err)
	}
	if err := store.SeedMemberRoleTx(tx, folder); err != nil {
		return fmt.Errorf("%w: %w", ErrSeedMemberRole, err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO acl
		(principal, action, scope, effect, params, predicate, granted_at, granted_by)
		VALUES (?, 'admin', ?, 'allow', '', '', datetime('now'), 'dashd')`,
		admin, folder); err != nil {
		return fmt.Errorf("%w: %w", ErrGrantAdmin, err)
	}
	if err := audit.EmitInTx(ctx, s.obj(), tx, ev); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
