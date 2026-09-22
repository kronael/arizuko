// Package authstore is authd's storage layer. It holds no SQL: every method is
// objstore's two primitives, and the driver is chosen by the daemon that
// constructs it (spec 6/37). One file per entity, except that auth_users.go
// also carries oauth_identities, because no operation touches an identity
// without the account it belongs to.
//
// authd's two hard invariants live here as guarded batches: exactly one active
// signing key, and a refresh token redeemable exactly once.
package authstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/kronael/arizuko/objstore"
)

// ctx is the context every call here passes the driver. No method takes one;
// the one cancellable call, the grants fetch on the refresh path, has its own
// (authd/server.go Refresh).
func ctx() context.Context { return context.Background() }

// The entities authd reads.
const (
	// SigningKeys holds one item per ES256 keypair, sorted by kid.
	SigningKeys = "signing_keys"
	// SigningKeySlot holds the single item that names the signing key; one
	// item is what makes two signers unrepresentable.
	SigningKeySlot = "signing_key_slot"
	// AuthUsers holds one item per canonical account, sorted by its sub.
	AuthUsers = "auth_users"
	// OAuthIdentities holds one item per provider login, sorted by
	// "<provider>:<provider_sub>".
	OAuthIdentities = "oauth_identities"
	// RefreshTokens holds one item per issued refresh token, sorted by the
	// token's hash.
	RefreshTokens = "refresh_tokens"
	// RefreshFamilies holds one item per login lineage, sorted by family id.
	RefreshFamilies = "refresh_families"
)

// Entities is the set a driver must serve before authd boots, applied by
// whoever holds the driver (authd's main, the import tool).
var Entities = []string{
	SigningKeys, SigningKeySlot, AuthUsers, OAuthIdentities, RefreshTokens, RefreshFamilies,
}

// Partition is the one partition every item authd owns lives in. A partition
// is what a caller FILTERS on (6/37 rule 1), and authd filters on none: a
// signing key is instance-global and a session is keyed by `sub`.
const Partition = "authd"

// Store answers authd's questions out of the object store it was handed.
type Store struct{ s objstore.Store }

// New wraps a driver.
func New(s objstore.Store) *Store { return &Store{s: s} }

// key is the coordinate of one item inside authd's single partition.
func key(sort string) objstore.Key { return objstore.Key{Partition: Partition, Sort: sort} }

// point is the read that addresses ONE item and applies no predicate. It
// answers "is it there", never "is it live", which every caller decides for
// itself from the tombstones.
func (s *Store) point(entity, sort string, project ...string) (objstore.Item, bool, error) {
	items, err := s.s.Query(ctx(), objstore.Query{
		Entity: entity, Partition: Partition,
		Key:     objstore.KeyPredicate{Op: objstore.ExactKey, Value: sort},
		Project: project,
	})
	if err != nil || len(items) == 0 {
		return objstore.Item{}, false, err
	}
	return items[0], true, nil
}

// HashToken maps a raw credential to the value stored for it; a refresh token
// is persisted by its hash alone.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// now is the timestamp layout every attribute authd writes carries: 20 bytes
// of UTC seconds. No read ORDERS on a stored timestamp through the engine, so
// its width is a property of the data, not something a comparison depends on
// (BUGS L97).
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Reachable is one point read of the item that names the signing key, for
// authd's health check: an engine reached over a network offers no pool to
// ping.
func (s *Store) Reachable() error {
	_, _, err := s.point(SigningKeySlot, activeSlot)
	return err
}
