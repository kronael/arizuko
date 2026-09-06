package store

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "store"

type Store struct {
	db *sql.DB
	// secretKeys is the AES-256 keyring for the secrets table. keys[0] is the
	// active key used to seal new writes; the rest are decrypt-only, retained
	// so an operator can rotate the active key without losing access to values
	// sealed under an older one (set the new key first, old ones after, then a
	// startup re-seal migrates them). Empty = encryption disabled.
	secretKeys [][32]byte

	// auditSub is the operator this Store's audited writers attribute their
	// audit_log rows to, and auditSurface the surface they arrived over. Empty
	// auditSub = the daemon acting on its own, recorded as "system" over the
	// gateway surface — right for background work, wrong for a request a human
	// made. Set via AsUser (REST) or AsCLI (CLI).
	auditSub     string
	auditSurface string

	// release closes the pool and drops the database OpenMem reserved. A store
	// opened against a file leaves the file alone, and Close closes the pool.
	release func() error
}

// AsUser returns a shallow copy of s whose audited writers attribute their
// audit_log rows to sub over the REST surface. Request-scoped callers use it so
// a dashboard mutation is recorded against the operator who made it rather than
// as "system": dashd learns sub from the proxyd-stamped X-User-* headers.
func (s *Store) AsUser(sub string) *Store {
	cp := *s
	cp.auditSub = sub
	cp.auditSurface = audit.SurfaceREST
	return &cp
}

// AsCLI is AsUser for the `arizuko` binary: same seam, but the CLI has no
// X-User-* header, so the operator is the invoking OS user and the surface is
// "cli". `cli:<osUser>` is the actor form LogCLIAudit already writes — recording
// a CLI mutation as a REST one would be a lie the audit trail cannot detect.
func (s *Store) AsCLI(osUser string) *Store {
	cp := *s
	cp.auditSub = osUser
	cp.auditSurface = audit.SurfaceCLI
	return &cp
}

// auditIdentity renders the (Actor, ActorSub, Surface) triple the audited
// writers stamp. One renderer, so the anonymous and operator-attributed forms
// cannot drift apart.
func (s *Store) auditIdentity() (actor, actorSub, surface string) {
	switch {
	case s.auditSub == "":
		return "system", "", audit.SurfaceGateway
	case s.auditSurface == audit.SurfaceCLI:
		return "cli:" + s.auditSub, s.auditSub, audit.SurfaceCLI
	default:
		return "user:" + s.auditSub, s.auditSub, audit.SurfaceREST
	}
}

// SetSecretKeys derives a 32-byte AES key from each raw value via SHA-256.
// raws[0] becomes the active (seal) key; the rest are decrypt-only retired
// keys for rotation. Call after Open. No raws leaves the store unable to write
// a secret: storeValue then refuses every write (BUGS S2).
func (s *Store) SetSecretKeys(raws ...[]byte) {
	keys := make([][32]byte, 0, len(raws))
	for _, r := range raws {
		keys = append(keys, sha256.Sum256(r))
	}
	s.secretKeys = keys
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	db, err := db_utils.Open(db_utils.FileDSN(filepath.Join(dir, "messages.db")))
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenRoutd opens routd.db at dir/routd.db (WAL, FK on) and wraps it as a *Store
// WITHOUT running store's migrations — routd owns that file and already created its
// tables. The host-admin CLI writes acl + secrets here. Strict: errors if routd.db has
// no acl table rather than silently creating a divergent schema.
func OpenRoutd(dir string) (*Store, error) {
	return OpenRoutdAt(OwnerDBPath(dir, OwnerRoutd))
}

// OpenRoutdAt is OpenRoutd against an explicitly configured routd.db path —
// dashd's DB_PATH, onbod's ROUTD_DB_PATH. Same strict acl probe.
func OpenRoutdAt(path string) (*Store, error) {
	return openOwned(path, "routd.db", "acl", "routd")
}

// OpenOnbod opens onbod.db at dir/onbod.db (WAL, FK on) and wraps it as a *Store
// WITHOUT running store's migrations — onbod owns that file and already created its
// tables. The host-admin CLI and the FS-mounted dashd write invites + gates here.
// Strict: errors if onbod.db has no invites table.
func OpenOnbod(dir string) (*Store, error) {
	return OpenOnbodAt(OwnerDBPath(dir, OwnerOnbod))
}

// OpenOnbodAt is OpenOnbod against an explicitly configured onbod.db path —
// dashd's and onbod's ONBOD_DB_PATH. Same strict invites probe.
func OpenOnbodAt(path string) (*Store, error) {
	return openOwned(path, "onbod.db", "invites", "onbod")
}

// openOwned attaches read/write to another daemon's owner DB and proves it is
// the right file by probing for a table only that daemon's migrations create.
// The probe is what makes a wrong path loud: SQLite would otherwise open an
// empty file happily and every read would return zero rows (spec 5/16 step 7).
func openOwned(path, file, table, owner string) (*Store, error) {
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		return nil, err
	}
	var n int
	if err := db.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s at %s has no %s table (%s must boot to migrate it first): %w",
			file, path, table, owner, err)
	}
	return &Store{db: db}, nil
}

// OpenMem opens a fresh, private, empty store for one test and migrates it.
// db_utils.OpenTestDB owns the engine and the DSN, so ARIZUKO_TEST_PG_DSN moves
// this and every other test database together (spec 6/37 step 6). Close releases
// the database.
func OpenMem() (*Store, error) {
	db, release, err := db_utils.OpenTestDB("store")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		// the caller gets no handle, so the failed open releases the database
		if rerr := release(); rerr != nil {
			return nil, fmt.Errorf("%w (and releasing the database: %v)", err, rerr)
		}
		return nil, err
	}
	return &Store{db: db, release: release}, nil
}

func (s *Store) Close() error {
	if s.release != nil {
		return s.release()
	}
	return s.db.Close()
}

// Migrate applies store's embedded SQL migrations to db. Used by test
// fixtures in other packages that open a raw *sql.DB.
func Migrate(db *sql.DB) error {
	return migrate(db)
}

// migrate applies store's embedded SQL migrations, then runs the two Go steps a
// reshaping migration cannot: BackfillInviteRefs hashes pre-I1 plaintext invites
// at rest (SQLite has no sha256()), and CarryOnboardingLegacy moves the rows the
// onboarding reshape renamed aside. Every opener (Open, OpenMem, the exported
// Migrate) routes through this one function so none can skip them.
func migrate(db *sql.DB) error {
	if err := db_utils.Migrate(db, migrationFS, "migrations", serviceName); err != nil {
		return err
	}
	if err := BackfillInviteRefs(db); err != nil {
		return err
	}
	return CarryOnboardingLegacy(db)
}

// New wraps an already-open *sql.DB as a *Store. Caller owns the db lifetime
// and must have run migrations.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB for callers that need raw query access
// (e.g. audit poll goroutine). The caller must not close it.
func (s *Store) DB() *sql.DB { return s.db }

// sqlPH returns n comma-separated `?` placeholders for use in SQL IN (...).
// e.g. sqlPH(3) == "?,?,?"
func sqlPH(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, n*2-1)
	for i := range n {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
