package compose

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// writeFlat puts a file where the pre-step-7 flat layout kept it.
func writeFlat(t *testing.T, storeDir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func absent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return false
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return true
}

// TestMigrateStoreLayout_SeedsEveryOwnerDir: compose binds store/<owner> as a
// mount SOURCE, and docker materialises a missing source as a root-owned
// directory that no uid-1000 daemon can write. The directories must therefore
// exist before the first `up`, even where there is nothing to move.
func TestMigrateStoreLayout_SeedsEveryOwnerDir(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	moved, err := MigrateStoreLayout(storeDir)
	if err != nil {
		t.Fatalf("MigrateStoreLayout: %v", err)
	}
	if len(moved) != 0 {
		t.Errorf("moved %v on an empty tree, want nothing", moved)
	}
	for owner := range store.OwnerDBs {
		dir := store.OwnerDir(storeDir, owner)
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			t.Errorf("%s not created: err=%v", dir, err)
		}
	}
}

// TestMigrateStoreLayout_MovesTheWholeTriple: -wal holds committed frames not
// yet checkpointed and -shm is its index. Moving the .db and leaving a -wal
// behind loses every transaction in it, silently — SQLite just opens the .db
// without them. krons's routd.db-wal was 5.8 MB when this shipped. The sidecars
// are written by hand here because a cleanly closed SQLite DB has none (close
// checkpoints and unlinks both), while a killed container leaves exactly this.
func TestMigrateStoreLayout_MovesTheWholeTriple(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	for _, file := range store.OwnerDBs {
		writeFlat(t, storeDir, file, "db:"+file)
		writeFlat(t, storeDir, file+"-wal", "wal:"+file)
		writeFlat(t, storeDir, file+"-shm", "shm:"+file)
	}

	moved, err := MigrateStoreLayout(storeDir)
	if err != nil {
		t.Fatalf("MigrateStoreLayout: %v", err)
	}
	if want := 3 * len(store.OwnerDBs); len(moved) != want {
		t.Errorf("moved %d files, want %d (a triple per owner): %v", len(moved), want, moved)
	}
	for owner, file := range store.OwnerDBs {
		for prefix, suffix := range map[string]string{"db": "", "wal": "-wal", "shm": "-shm"} {
			nested := store.OwnerDBPath(storeDir, owner) + suffix
			if got, want := mustRead(t, nested), prefix+":"+file; got != want {
				t.Errorf("%s = %q, want %q", nested, got, want)
			}
			if !absent(t, filepath.Join(storeDir, file+suffix)) {
				t.Errorf("%s still sits flat in the store tree", file+suffix)
			}
		}
	}
}

// TestMigrateStoreLayout_LeavesEverythingWithoutAnOwnerAlone: messages.db is
// the frozen pre-split file, and store/ also carries 0-byte leftovers and the
// adapters' own state directories. A mover that swept "every .db" would strand
// messages.db where `arizuko migrate-split` no longer looks, and would move an
// adapter's state out from under its bind. All three shapes are real: krons has
// a 0-byte proxyd.db and six whatsapp-auth directories, sloth a 0-byte timed.db.
func TestMigrateStoreLayout_LeavesEverythingWithoutAnOwnerAlone(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	writeFlat(t, storeDir, "messages.db", "monolith")
	writeFlat(t, storeDir, "proxyd.db", "")
	if err := os.MkdirAll(filepath.Join(storeDir, "whatsapp-auth"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateStoreLayout(storeDir); err != nil {
		t.Fatalf("MigrateStoreLayout: %v", err)
	}
	if got := mustRead(t, filepath.Join(storeDir, "messages.db")); got != "monolith" {
		t.Errorf("messages.db = %q, want it untouched at the flat path", got)
	}
	if absent(t, filepath.Join(storeDir, "proxyd.db")) {
		t.Error("proxyd.db was moved; only owner DBs move")
	}
	if absent(t, filepath.Join(storeDir, "whatsapp-auth")) {
		t.Error("whatsapp-auth/ was moved; adapter state directories are not owner DBs")
	}
}

// TestMigrateStoreLayout_Idempotent: the mover runs on EVERY `arizuko
// generate`, i.e. every restart, so the second run must be a no-op. Identity is
// checked by inode, not mtime: a second-granular timestamp compare passes even
// when the file was rewritten inside the same second.
func TestMigrateStoreLayout_Idempotent(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	writeFlat(t, storeDir, store.OwnerDBs[store.OwnerRoutd], "rows")
	writeFlat(t, storeDir, store.OwnerDBs[store.OwnerRoutd]+"-wal", "frames")

	if _, err := MigrateStoreLayout(storeDir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	nested := store.OwnerDBPath(storeDir, store.OwnerRoutd)
	before, err := os.Stat(nested)
	if err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateStoreLayout(storeDir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(moved) != 0 {
		t.Errorf("second run moved %v, want nothing", moved)
	}
	after, err := os.Stat(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("second run replaced routd.db with a different file")
	}
	if got := mustRead(t, nested); got != "rows" {
		t.Errorf("routd.db = %q after the second run, want %q", got, "rows")
	}
	if got := mustRead(t, nested+"-wal"); got != "frames" {
		t.Errorf("routd.db-wal = %q after the second run, want %q", got, "frames")
	}
}

// TestMigrateStoreLayout_ReunitesAHalfMovedTriple: a crash between the two
// renames leaves the .db in the owner directory and the -wal still flat. The
// next run must reunite them, and no committed frame is lost because nothing
// opens either path in between — the mover runs after `compose down`.
func TestMigrateStoreLayout_ReunitesAHalfMovedTriple(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	file := store.OwnerDBs[store.OwnerRoutd]
	if err := os.MkdirAll(store.OwnerDir(storeDir, store.OwnerRoutd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.OwnerDBPath(storeDir, store.OwnerRoutd), []byte("rows"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFlat(t, storeDir, file+"-wal", "frames")

	moved, err := MigrateStoreLayout(storeDir)
	if err != nil {
		t.Fatalf("MigrateStoreLayout: %v", err)
	}
	if len(moved) != 1 {
		t.Errorf("moved %v, want just the stranded -wal", moved)
	}
	if got := mustRead(t, store.OwnerDBPath(storeDir, store.OwnerRoutd)+"-wal"); got != "frames" {
		t.Errorf("-wal = %q, want it reunited with its DB", got)
	}
	if !absent(t, filepath.Join(storeDir, file+"-wal")) {
		t.Error("-wal left flat; the DB it belongs to is in the owner directory")
	}
}

// TestMigrateStoreLayout_RefusesTwoCopies: with a DB in both places one of them
// holds rows and the mover cannot know which. Overwriting either is data loss,
// so it stops — and it stops BEFORE `generate` writes a compose whose mounts
// assume the move happened.
func TestMigrateStoreLayout_RefusesTwoCopies(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	file := store.OwnerDBs[store.OwnerRoutd]
	writeFlat(t, storeDir, file, "flat rows")
	if err := os.MkdirAll(store.OwnerDir(storeDir, store.OwnerRoutd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.OwnerDBPath(storeDir, store.OwnerRoutd), []byte("nested rows"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := MigrateStoreLayout(storeDir)
	if err == nil {
		t.Fatal("MigrateStoreLayout = nil error with routd.db in both places, want a refusal")
	}
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("error %q should say the file exists in both places", err)
	}
	if got := mustRead(t, filepath.Join(storeDir, file)); got != "flat rows" {
		t.Errorf("flat routd.db = %q, want it untouched by the refusal", got)
	}
	if got := mustRead(t, store.OwnerDBPath(storeDir, store.OwnerRoutd)); got != "nested rows" {
		t.Errorf("nested routd.db = %q, want it untouched by the refusal", got)
	}
}

// TestMigrateStoreLayout_DaemonBootsOnTheMovedDB is the whole point. routd.Open
// now REQUIRES the file at the owner path and refuses to create one, so a mover
// that stranded a live DB turns a deploy into an outage. This walks the real
// boot path against a real migrated routd.db that started out flat, and reads
// back a row seeded before the move.
func TestMigrateStoreLayout_DaemonBootsOnTheMovedDB(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")

	seeded, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("routd.Create (seed): %v", err)
	}
	if err := seeded.PutGroup(core.Group{Folder: "acme/eng"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	seeded.Close()

	// Stage the pre-step-7 tree this instance would really be sitting on.
	file := store.OwnerDBs[store.OwnerRoutd]
	if err := os.Rename(store.OwnerDBPath(storeDir, store.OwnerRoutd), filepath.Join(storeDir, file)); err != nil {
		t.Fatalf("stage the flat layout: %v", err)
	}
	for _, owner := range []string{store.OwnerAuthd, store.OwnerOnbod, store.OwnerRoutd, store.OwnerRuned} {
		_ = os.Remove(store.OwnerDir(storeDir, owner))
	}
	if _, err := routd.Open(storeDir); err == nil {
		t.Fatal("routd.Open succeeded on the flat tree; this test proves nothing unless it cannot")
	}

	if _, err := MigrateStoreLayout(storeDir); err != nil {
		t.Fatalf("MigrateStoreLayout: %v", err)
	}

	db, err := routd.Open(storeDir)
	if err != nil {
		t.Fatalf("routd.Open after the move: %v", err)
	}
	defer db.Close()
	if _, ok := db.AllGroups()["acme/eng"]; !ok {
		t.Error("the seeded group is gone — routd opened a different file than the one that moved")
	}
}
