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

// CreateGroupWithAdmin writes the group row, its member edge, the creator's
// admin grant and the audit row that records the act, in ONE guarded batch.
//
// The four belong together. A group row with no member edge expands to nothing,
// matches zero allow rows and comes up with no messaging tools (BUGS L119); a
// group row with no admin grant is a folder nobody can open; and an audit row
// that outlived a refused create would record a folder nobody got.
//
// The batch runs through store.SeedMemberRole, which is objstore.AddEdge: the
// cycle walk decides the edge and compare-and-swaps routd's marker in the same
// write, so the edge cannot land on a graph that moved under it. The
// transaction this replaces rested on SQLite's single writer for that instead,
// which Postgres at READ COMMITTED does not give (BUGS L256).
//
// A folder that is already there is refused rather than taken over: the groups
// write is the ONLY guarded one in the batch — the edge and the grant are
// create-if-absent merges and refuse nothing — so a refusal names exactly that,
// and the operator racing another creator is told so instead of being granted
// admin on somebody else's world.
func (s *Store) CreateGroupWithAdmin(ctx context.Context, folder, product, admin string, ev audit.Event) error {
	st := s.st()
	row, err := store.GroupCreateWrite(core.Group{
		Folder: folder, AddedAt: time.Now(), Product: product,
	})
	if err != nil {
		return fmt.Errorf("insert group: %w", err)
	}
	grant, err := store.AdminGrantWrite(admin, folder, "dashd")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGrantAdmin, err)
	}
	_, rec := audit.WriteFor(ev)
	created, err := store.SeedMemberRole(ctx, st, folder, row, grant, rec)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSeedMemberRole, err)
	}
	if !created {
		return fmt.Errorf("insert group: folder %q already exists", folder)
	}
	return nil
}
