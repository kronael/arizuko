package store

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

type Store struct {
	// obj is the handle every read and write goes through.
	obj objstore.Store
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

	// release drops the private namespace OpenMem reserved. A store
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

// OpenMem hands ONE test its own private namespace on the endpoint this process
// dials, with BOTH owners' entities made in it. store is what reads and writes
// routd's tables and onbod's alike, so a fixture for its own suite carries both.
// Close drops the namespace.
func OpenMem() (*Store, error) {
	h, release, err := TestNamespace()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	for _, owner := range []string{OwnerRoutd, OwnerOnbod} {
		if err := PrepareOwner(ctx, h, owner); err != nil {
			if rerr := release(); rerr != nil {
				return nil, fmt.Errorf("%w (and dropping the namespace: %v)", err, rerr)
			}
			return nil, err
		}
	}
	return &Store{obj: h, release: release}, nil
}

// Close ends what the open holds. A store over a handle its caller owns holds
// no namespace, so there is nothing to drop and every daemon's `defer Close`
// stays correct.
func (s *Store) Close() error {
	if s.release != nil {
		return s.release()
	}
	return nil
}

// NewOver wraps a handle the caller owns. The store holds no namespace of its
// own, so Close ends nothing and the reads and writes are what it is for.
func NewOver(obj objstore.Store) *Store {
	return &Store{obj: obj}
}

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
func (s *Store) Obj() objstore.Store { return s.obj }

// btoi converts a bool to the 0/1 an INTEGER column stores. It lives here rather
// than beside one caller because the tables that still have columns share it.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
