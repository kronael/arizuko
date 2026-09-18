// Package authstore is authd's storage layer: the daemon asks it for what a
// request needs and it answers with rows, so a second driver reimplements this
// package and no handler changes (spec 6/37 step 1).
//
// It holds no SQL. Every method below is objstore's two primitives — Query and
// GuardedBatch — and the driver is chosen by the daemon that constructs this
// package, never in here (spec 6/37 step 5). One file per entity, except that
// auth_users.go also carries oauth_identities: no operation touches an identity
// without the account it belongs to, and 6/37's third rule keeps such an
// operation whole in the file of the entity it protects.
//
// authd is the sole ES256 signer and holds one refresh row per issued token,
// which is where its two hard invariants live: exactly one active signing key,
// and a refresh token redeemable exactly once. Both are guarded batches here,
// stated on the item that decides them.
package authstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/kronael/arizuko/objstore"
)

// ctx is the context every call here passes the driver. None of this package's
// methods takes one, because none of them ever did: the daemon's handlers call
// them without a deadline and the reads they make are point reads on a database
// the daemon owns alone. Threading a context through fifteen signatures is a
// change to every caller, and the one place a cancellable call genuinely
// belongs — the grants fetch on the refresh path — already has its own
// (authd/server.go Refresh).
func ctx() context.Context { return context.Background() }

// The entities authd reads. An entity is a name a driver resolves; what it
// costs to reach an item inside one is the (partition, sort) coordinate each
// file documents.
const (
	// SigningKeys holds one item per ES256 keypair, sorted by kid.
	SigningKeys = "signing_keys"
	// SigningKeySlot holds the single item that names the signing key. It is
	// a separate entity because it is a separate question — "who signs now" —
	// and because one item is what makes two signers unrepresentable.
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

// Entities is the set a driver must be able to serve before authd boots. It is
// declared here, beside the code that reads them, and applied by whoever holds
// the driver — authd's main for the running daemon, the import tool for the
// data move. One declaration, two readers; an engine with no DDL applies
// nothing and still answers every query below.
var Entities = []string{
	SigningKeys, SigningKeySlot, AuthUsers, OAuthIdentities, RefreshTokens, RefreshFamilies,
}

// Partition is the one partition every item authd owns lives in.
//
// A partition is the coordinate a caller FILTERS on (6/37 rule 1), and authd
// filters on none: a signing key is instance-global and a session is keyed by
// `sub`, which is why authd's REST resources refuse a folder-claimed caller
// outright (authd/audit_resource.go instanceWideGate). So there is one
// partition and it names the daemon that owns it. Measured 2026-09-07 across
// three live instances: 3 signing keys, 2 accounts, 1 identity and 78 refresh
// tokens, so nothing here approaches a partition's size.
const Partition = "authd"

// Store answers authd's questions out of the object store it was handed.
type Store struct{ s objstore.Store }

// New wraps a driver. The daemon picks which one; this package never names it,
// which is the whole point of the seam (spec 6/37).
func New(s objstore.Store) *Store { return &Store{s: s} }

// key is the coordinate of one item inside authd's single partition.
func key(sort string) objstore.Key { return objstore.Key{Partition: Partition, Sort: sort} }

// point is the read that addresses ONE item and applies no predicate of any
// kind. It answers "is it there, and what does it carry" — never "is it live",
// which every caller here decides for itself from the tombstones.
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

// HashToken maps a raw credential to the value stored for it. A refresh token
// is persisted by its hash alone, so this is how a presented secret addresses
// its item.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// now is the timestamp layout every attribute authd writes carries. Measured on
// all three live instances: every stored timestamp is exactly this, 20 bytes of
// UTC seconds, so the import needs no conversion.
//
// No read below ORDERS on a stored timestamp through the engine. SessionsByFamily
// orders on parsed instants and the two key-ordered reads order on the sort key,
// so the width of this layout is a property of the data rather than something a
// comparison depends on (BUGS L97 is the cost of the other arrangement).
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Reachable asks the store the cheapest question it has: one point read of the
// item that names the signing key. It answers authd's health check, where the
// claim is that the STORE answers — an engine reached over a network offers no
// pool to ping, and a pool that pings proves nothing about the entities.
func (s *Store) Reachable() error {
	_, _, err := s.point(SigningKeySlot, activeSlot)
	return err
}
