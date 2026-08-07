package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/store"
)

// MigrateStoreLayout moves an instance's owner DBs from the flat store/<db>
// layout into the per-owner one the mounts and env paths now assume,
// store/<owner>/<db> (spec 5/16 step 7). A DB moves with its -wal and -shm
// siblings: -wal holds committed frames not yet checkpointed, so leaving it
// behind loses transactions silently.
//
// This runs on the HOST, never inside a daemon, and that is forced rather than
// chosen: the narrowing the move enables is exactly what removes the flat path
// from a daemon's own mount, so nothing can migrate itself. `arizuko generate`
// is the one place with the whole tree in hand and no container running —
// systemd invokes it after `compose down` and before `up`.
//
// Without it a live instance goes DOWN on the deploy that ships the new
// layout: `store.OwnerDBPath` resolves into a directory the tree does not have,
// and every owner daemon now refuses to boot rather than manufacture an empty
// database (db_utils.RequireDBFile).
//
// Idempotent — a DB already in place is left alone and a second run moves
// nothing. A file present in BOTH places is an error, never a guess: one of the
// two holds rows nobody asked to lose. A crash between two renames leaves a
// triple split across the two directories and the next run reunites it, which
// is safe precisely because nothing opens either path in between.
func MigrateStoreLayout(storeDir string) ([]string, error) {
	var moved []string
	for owner, file := range store.OwnerDBs {
		dir := store.OwnerDir(storeDir, owner)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return moved, fmt.Errorf("store layout: create %s: %w", dir, err)
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			flat := filepath.Join(storeDir, file+suffix)
			nested := filepath.Join(dir, file+suffix)
			present, err := exists(flat)
			if err != nil {
				return moved, err
			}
			if !present {
				continue
			}
			clash, err := exists(nested)
			if err != nil {
				return moved, err
			}
			if clash {
				return moved, fmt.Errorf(
					"store layout: %s exists at both %s and %s — one of them holds rows; "+
						"move the stale copy aside by hand, then re-run", file+suffix, flat, nested)
			}
			if err := os.Rename(flat, nested); err != nil {
				return moved, fmt.Errorf("store layout: move %s to %s: %w", flat, nested, err)
			}
			moved = append(moved, nested)
		}
	}
	return moved, nil
}

// exists reports whether path is there. A stat error that is NOT "no such file"
// — a permission problem on the data dir, say — is returned rather than read as
// absence: treating it as absence would rename a live DB onto one already
// sitting at the destination.
func exists(path string) (bool, error) {
	switch _, err := os.Stat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("store layout: stat %s: %w", path, err)
	}
}
