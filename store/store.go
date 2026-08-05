package store

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

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
	// audit_log rows to. Empty = the daemon acting on its own, recorded as
	// "system" over the gateway surface — right for background work, wrong for
	// a request a human made. Set via AsUser.
	auditSub string
}

// AsUser returns a shallow copy of s whose audited writers attribute their
// audit_log rows to sub over the REST surface. Request-scoped callers use it so
// a dashboard mutation is recorded against the operator who made it rather than
// as "system": dashd learns sub from the proxyd-stamped X-User-* headers.
func (s *Store) AsUser(sub string) *Store {
	cp := *s
	cp.auditSub = sub
	return &cp
}

// auditIdentity renders the (Actor, ActorSub, Surface) triple the audited
// writers stamp. One renderer, so the anonymous and operator-attributed forms
// cannot drift apart.
func (s *Store) auditIdentity() (actor, actorSub, surface string) {
	if s.auditSub == "" {
		return "system", "", audit.SurfaceGateway
	}
	return "user:" + s.auditSub, s.auditSub, audit.SurfaceREST
}

// SetSecretKeys derives a 32-byte AES key from each raw value via SHA-256.
// raws[0] becomes the active (seal) key; the rest are decrypt-only retired
// keys for rotation. Call after Open. No raws = encryption disabled.
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
	// foreign_keys must ride the DSN: modernc.org/sqlite defaults FK
	// enforcement OFF per-connection, and database/sql pools many
	// connections — a one-shot PRAGMA only covers one of them, so
	// ON DELETE CASCADE (migrations 0068/0069) would silently no-op on
	// the others. DSN pragmas apply to every pooled connection.
	dsn := filepath.Join(dir, "messages.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenRoutd opens routd.db at dir/routd.db (WAL, FK on) and wraps it as a
// *Store WITHOUT running store's migrations — routd owns that file and already
// created its tables (acl/secrets/tasks/pane via routd's own migration set).
// The host-admin CLI (arizuko grant/secret/user-secret) writes acl + secrets
// here in the split topology instead of opening messages.db, since those tables
// now live in routd.db (spec 5/5 § Daemon ownership). Strict: errors if routd.db
// has no acl table (routd never booted to migrate it) rather than silently
// creating a divergent schema.
func OpenRoutd(dir string) (*Store, error) {
	dsn := filepath.Join(dir, "routd.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	var n int
	if err := db.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name='acl'").Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("routd.db at %s has no acl table (routd must boot to migrate it first): %w", dir, err)
	}
	return &Store{db: db}, nil
}

// OpenOnbod opens onbod.db at dir/onbod.db (WAL, FK on) and wraps it as a
// *Store WITHOUT running store's migrations — onbod owns that file and already
// created its tables (onboarding/invites/onboarding_gates + audit_log via
// onbod's own migration set). The host-admin CLI (arizuko invite/gate) and the
// FS-mounted dashd write invites + gates here in the split topology instead of
// opening messages.db, since those tables now live in onbod.db (spec 5/5
// § Daemon ownership). Strict: errors if onbod.db has no invites table (onbod
// never booted to migrate it) rather than silently creating a divergent schema.
func OpenOnbod(dir string) (*Store, error) {
	dsn := filepath.Join(dir, "onbod.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	var n int
	if err := db.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name='invites'").Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("onbod.db at %s has no invites table (onbod must boot to migrate it first): %w", dir, err)
	}
	return &Store{db: db}, nil
}

// memDBSeq names each OpenMem's in-memory DB uniquely so two stores in the
// same process never share rows/schema. A bare `file::memory:?cache=shared`
// DSN is process-wide: every OpenMem in a test binary mapped onto ONE shared
// DB, leaking rows across tests -> order-dependent failures (commit 5671e695).
var memDBSeq atomic.Int64

func OpenMem() (*Store, error) {
	// `:memory:` SQLite is per-connection; database/sql can pool a second
	// connection that sees an empty DB. A NAMED memdb with `cache=shared` is
	// shared across the connections of THIS pool yet private to this DSN, so
	// each OpenMem gets an isolated DB. SQLite drops a shared-cache memdb when
	// its last connection closes, so pin one idle connection for the pool's
	// lifetime (the schema would otherwise vanish between calls).
	dsn := fmt.Sprintf(
		"file:memdb-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)",
		memDBSeq.Add(1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	s := &Store{db: db}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate applies store's embedded SQL migrations to db. Used by test
// fixtures in other packages that open a raw *sql.DB.
func Migrate(db *sql.DB) error {
	return migrate(db)
}

// migrate applies store's embedded SQL migrations, then carries forward the
// rows whose tokens must be hashed at rest — pre-I1 plaintext invites
// (BackfillInviteRefs) and pre-Z3 plaintext onboarding tokens
// (BackfillOnboardingTokenRefs). SQLite has no sha256(), so neither step can
// be pure SQL. Every opener (Open, OpenMem, the exported Migrate) routes
// through this one function so none can skip them.
func migrate(db *sql.DB) error {
	if err := db_utils.Migrate(db, migrationFS, "migrations", serviceName); err != nil {
		return err
	}
	if err := BackfillInviteRefs(db); err != nil {
		return err
	}
	return BackfillOnboardingTokenRefs(db)
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
