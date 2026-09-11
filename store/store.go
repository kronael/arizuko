package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
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

// Open creates and migrates dir/messages.db — the FROZEN pre-split monolith. Only
// a caller whose subject IS that database wants it; see Migrate.
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
	exists, err := db_utils.TableExists(db, table)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%s at %s: look for the %s table: %w", file, path, table, err)
	}
	if !exists {
		db.Close()
		return nil, fmt.Errorf("%s at %s has no %s table (%s must boot to migrate it first)",
			file, path, table, owner)
	}
	return &Store{db: db}, nil
}

// OpenMem opens a fresh, private, empty store for one test and migrates it — on
// the FROZEN pre-split schema, so it belongs to a test whose subject is that
// database and to no other (see Migrate). db_utils.OpenTestDB owns the engine and
// the DSN, so ARIZUKO_TEST_PG_DSN moves this and every other test database
// together (spec 6/37 step 6). Close releases the database.
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

// Migrate applies store's embedded SQL migrations to db — the FROZEN pre-split
// messages.db schema, and nothing else. No daemon runs this chain: routd, onbod,
// authd, runed and chanlib each own and migrate their own file. The callers are
// `arizuko migrate-split` (which reads the monolith it builds) and the tests that
// simulate an old instance for it.
//
// So this is not a general test fixture. A test that opens it to get a `messages`
// or an `acl` table is asserting against a schema production does not have, and it
// stays green while the live one drifts: `arizuko budget set` wrote its cap here
// while routd's gate read routd.db, and every cap the verb reported as set was
// invisible to enforcement (BUGS Q1). A test whose subject is a live schema opens
// that daemon's own chain — routd.OpenMem, testutils.NewRoutdStore, or the
// equivalent. TestMigrationsAreFrozen keeps the chain from growing again.
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
	// The moved tables are entities rather than migrations, so they are created
	// by the driver here — the audited writers in this package record into
	// whichever database they were opened over (entities.go). audit owns its own
	// declaration and every opener asks for it separately.
	//
	// BOTH owners' sets, because this chain builds the pre-split monolith and
	// carrying every owner's tables in one file is what pre-split MEANS. A split
	// instance's openers ask for one owner each (routd/db.go, onbod/db.go).
	ctx := context.Background()
	for _, owner := range []string{OwnerRoutd, OwnerOnbod} {
		if err := EnsureEntities(ctx, sqlstore.New(db), owner); err != nil {
			return err
		}
	}
	if err := audit.EnsureEntities(ctx, sqlstore.New(db)); err != nil {
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

// Obj is this database through objstore's two primitives (spec 6/37 step 5).
// The audited writers below run their mutation as SQL on a transaction of their
// own and the row that records it joins that transaction, so audit needs the
// driver that can write into one — and dashd, webd and proxyd, which hold this
// store over routd's file, reach the same driver for their own audit sink
// rather than each naming one.
//
// It is built per call rather than held: sqlstore.New reads the engine off the
// pool with a type assertion, and a field would have to be filled in at four
// constructors and survive every shallow copy AsUser makes.
func (s *Store) Obj() *sqlstore.Store { return sqlstore.New(s.db) }
