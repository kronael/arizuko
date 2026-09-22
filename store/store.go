package store

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

type Store struct {
	obj objstore.Store
	// secretKeys[0] seals new writes; the rest are decrypt-only, kept so a
	// value sealed under a rotated-out key still reads. Empty: no secret can be
	// written.
	secretKeys [][32]byte

	// Empty auditSub is the daemon acting on its own, recorded as "system" over
	// the gateway surface. Set via AsUser (REST) or AsCLI (CLI).
	auditSub     string
	auditSurface string

	// release drops the private namespace OpenMem reserved; nil over a handle
	// the caller owns.
	release func() error
}

// AsUser returns a shallow copy of s whose audited writers attribute their
// audit_log rows to sub over the REST surface.
func (s *Store) AsUser(sub string) *Store {
	cp := *s
	cp.auditSub = sub
	cp.auditSurface = audit.SurfaceREST
	return &cp
}

// AsCLI is AsUser for the `arizuko` binary: the operator is the invoking OS
// user and the surface is "cli", the actor form LogCLIAudit writes.
func (s *Store) AsCLI(osUser string) *Store {
	cp := *s
	cp.auditSub = osUser
	cp.auditSurface = audit.SurfaceCLI
	return &cp
}

// auditIdentity renders the (Actor, ActorSub, Surface) triple every audited
// writer stamps, so the anonymous and attributed forms cannot drift apart.
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
// raws[0] seals; the rest are decrypt-only. No raws leaves storeValue refusing
// every write (BUGS S2).
func (s *Store) SetSecretKeys(raws ...[]byte) {
	keys := make([][32]byte, 0, len(raws))
	for _, r := range raws {
		keys = append(keys, sha256.Sum256(r))
	}
	s.secretKeys = keys
}

// OpenMem hands ONE test its own private namespace with BOTH owners' entities
// made in it. Close drops the namespace.
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

// Close drops the namespace OpenMem reserved; a store over a caller-owned
// handle holds none, so every daemon's `defer Close` stays correct.
func (s *Store) Close() error {
	if s.release != nil {
		return s.release()
	}
	return nil
}

// NewOver wraps a handle the caller owns.
func NewOver(obj objstore.Store) *Store {
	return &Store{obj: obj}
}

// Obj is this database through objstore's two primitives (spec 6/37 step 5).
func (s *Store) Obj() objstore.Store { return s.obj }

// btoi converts a bool to the 0/1 an INTEGER column stores.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
