package dashstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// ErrSeedMemberRole and ErrGrantAdmin name the two steps of a group create the
// operator can act on: one says the folder would expand to nothing, the other
// that nobody would be able to open it. Every other step reports as a plain
// failed write, because the operator's next move is the same either way.
var (
	ErrSeedMemberRole = errors.New("seed member role")
	ErrGrantAdmin     = errors.New("grant admin")
)

// GroupExists reports whether a folder already has a group row.
func (s *Store) GroupExists(folder string) (bool, error) {
	folders, err := s.st().GroupFolders(0)
	if err != nil {
		return false, err
	}
	for _, f := range folders {
		if f == folder {
			return true, nil
		}
	}
	return false, nil
}

// CreateGroupWithAdmin writes the group row, its member edge and the creator's
// admin grant in one transaction, with the audit row inside it.
//
// The three belong together. A group row with no member edge expands to
// nothing, matches zero allow rows and comes up with no messaging tools
// (BUGS L119); a group row with no admin grant is a folder nobody can open.
// store.PutGroup writes the row on a transaction of its own and so cannot
// carry either, which is why the create is CreateGroupTx — the tx-bound writer
// — rather than a call to it.
//
// A folder that is already there is refused rather than taken over: the create
// is create-if-absent, so the operator racing another creator is told so
// instead of being granted admin on somebody else's world.
func (s *Store) CreateGroupWithAdmin(ctx context.Context, folder, product, admin string, ev audit.Event) error {
	st := s.st()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	created, err := st.CreateGroupTx(ctx, tx, core.Group{
		Folder: folder, AddedAt: time.Now(), Product: product,
	})
	if err != nil {
		return fmt.Errorf("insert group: %w", err)
	}
	if !created {
		return fmt.Errorf("insert group: folder %q already exists", folder)
	}
	if err := store.SeedMemberRoleTx(ctx, st, tx, folder); err != nil {
		return fmt.Errorf("%w: %w", ErrSeedMemberRole, err)
	}
	if err := st.PutACLRowTx(ctx, tx, core.ACLRow{
		Principal: admin, Action: "admin", Scope: folder,
		Effect: "allow", GrantedBy: "dashd",
	}); err != nil {
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
