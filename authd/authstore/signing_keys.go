package authstore

// signing_keys.go holds every read and write of the trust root: one key signs,
// and each retired key keeps verifying until its window closes.
//
// TWO entities, and the second one is the invariant. `signing_keys` holds the
// keypairs, sorted by kid — the kid is `<unix>-<hex>`, so key order IS creation
// order and the metadata read needs no sort of its own. `signing_key_slot`
// holds ONE item, at sort key `active`, naming the kid that signs.
//
// A key carries no `active` attribute, and that is deliberate. "Exactly one
// active key" was a partial-unique index on a column, which no key-value engine
// has; as a slot it is a property of the coordinate instead, so two active keys
// are not a state this store can represent. Rotation moves the slot under a
// compare-and-set on the kid it is replacing, which is what makes two racing
// rotations elect one winner on every engine rather than on the one with the
// index.

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg/resources"
)

// activeSlot is the sort key of the one item that names the signing key.
const activeSlot = "active"

// ErrRotationLost is what a rotation reports when another rotation moved the
// slot first. It is a REFUSAL, not a fault — the loser must not sign — and it
// is an error because that is what the caller has always seen: the unique index
// failed its INSERT the same way.
var ErrRotationLost = errors.New("another rotation moved the active signing key first")

// StoredKey is a signing key as stored, with its parsed private key.
type StoredKey struct {
	Key       *auth.SigningKey
	Active    bool
	RetiredAt *time.Time
}

// SigningKeysWithMaterial reads every signing key WITH its private half and
// parses each PEM. It is the only reader in the tree that takes priv_pem, and
// it feeds the in-process signer; SigningKeyMetadata answers the operator's
// question without ever naming the attribute.
//
// It applies no serving window: the caller holds maxAccessTTL and decides which
// of these keys still verify.
func (s *Store) SigningKeysWithMaterial() ([]StoredKey, error) {
	active, err := s.activeKid()
	if err != nil {
		return nil, err
	}
	items, err := s.s.Query(ctx(), objstore.Query{Entity: SigningKeys, Partition: Partition})
	if err != nil {
		return nil, err
	}
	var out []StoredKey
	for _, it := range items {
		priv, err := auth.ParseECPrivateKeyPEM(it.Attrs["priv_pem"])
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", it.Key.Sort, err)
		}
		k := StoredKey{
			Key:    &auth.SigningKey{Kid: it.Key.Sort, Priv: priv},
			Active: it.Key.Sort == active,
		}
		if t, ok := parseTS(it.Attrs["retired_at"]); ok {
			k.RetiredAt = &t
		}
		out = append(out, k)
	}
	return out, nil
}

// RotateActiveKey retires the key holding the slot as of `at` and installs k in
// it, in ONE batch. Two concurrent rotations cannot both land: the slot write
// is conditioned on the kid it is replacing, so the loser's batch is refused
// whole — its new key is never written and the trust root never splits into two
// signers.
func (s *Store) RotateActiveKey(k *auth.SigningKey, at time.Time) error {
	privPEM, pubPEM, err := EncodeKey(k.Priv)
	if err != nil {
		return err
	}
	old, err := s.activeKid()
	if err != nil {
		return err
	}
	writes := []objstore.Write{{
		Op: objstore.Put, Entity: SigningKeys, Key: key(k.Kid),
		Attrs: map[string]string{
			"priv_pem": privPEM, "pub_pem": pubPEM, "created_at": now(),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}}
	if old == "" {
		// No key holds the slot: a first boot, or the window an emergency
		// revoke opens between RetireActiveKeys and the rotation that follows
		// it. ItemAbsent is what elects one winner there, and it is the engine's
		// own uniqueness rather than a read this package performed.
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: SigningKeySlot, Key: key(activeSlot),
			Attrs: map[string]string{"kid": k.Kid},
			Cond:  objstore.Cond{Item: objstore.ItemAbsent},
		})
	} else {
		writes = append(writes,
			objstore.Write{
				Op: objstore.Update, Entity: SigningKeySlot, Key: key(activeSlot),
				Attrs: map[string]string{"kid": k.Kid},
				Cond: objstore.Cond{Where: []objstore.Cmp{
					{Attr: "kid", Op: objstore.Eq, Value: old},
				}},
			},
			objstore.Write{
				Op: objstore.Update, Entity: SigningKeys, Key: key(old),
				Attrs: map[string]string{"retired_at": at.UTC().Format(time.RFC3339)},
				Cond:  objstore.Cond{Item: objstore.ItemPresent},
			})
	}
	committed, err := s.s.GuardedBatch(ctx(), nil, writes)
	if err != nil {
		return err
	}
	if !committed {
		return ErrRotationLost
	}
	return nil
}

// RetireActiveKeys marks the key holding the slot retired as of `at` and empties
// the slot. Used by emergency revoke (at backdated, window closes immediately);
// normal rotation retires in-batch via RotateActiveKey.
//
// An empty slot is not a failure: there is nothing to retire, which is the
// answer the set-wide UPDATE this replaces also gave by touching no row. Nor is
// a refused batch — it means another caller emptied the slot in between, and
// the key it named is retired either way.
func (s *Store) RetireActiveKeys(at time.Time) error {
	kid, err := s.activeKid()
	if err != nil || kid == "" {
		return err
	}
	_, err = s.s.GuardedBatch(ctx(), nil, []objstore.Write{
		{
			Op: objstore.Delete, Entity: SigningKeySlot, Key: key(activeSlot),
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: "kid", Op: objstore.Eq, Value: kid},
			}},
		},
		{
			Op: objstore.Update, Entity: SigningKeys, Key: key(kid),
			Attrs: map[string]string{"retired_at": at.UTC().Format(time.RFC3339)},
			Cond:  objstore.Cond{Item: objstore.ItemPresent},
		},
	})
	return err
}

// SigningKeyMetadata reads the non-secret attributes of every signing key and
// derives each key's serving state.
//
// The PROJECTION is the security boundary of this read. `priv_pem` is not in
// it, `pub_pem` is not in it, and there is no argument, filter or action that
// can add one — the list is a constant, and the driver never fetches what it
// does not project, so the private half does not leave the engine.
// SigningKeysWithMaterial is the only reader that takes it, and it is
// in-process and feeds the signer.
//
// Status is the time-based window spec 5/1 pins, computed once here rather than
// left for each reader to re-derive: a key serves while it holds the slot, OR
// while now < retired_at + maxAccessTTL. `retiring` is a retired key whose
// already-issued tokens still verify; `retired` no longer verifies anything and
// is waiting on GC.
func (s *Store) SigningKeyMetadata(maxAccessTTL time.Duration, at time.Time) ([]resources.SigningKeysRow, error) {
	active, err := s.activeKid()
	if err != nil {
		return nil, err
	}
	items, err := s.s.Query(ctx(), objstore.Query{
		Entity: SigningKeys, Partition: Partition, Descending: true,
		Project: []string{"created_at", "retired_at"},
	})
	if err != nil {
		return nil, err
	}
	out := []resources.SigningKeysRow{}
	for _, it := range items {
		r := resources.SigningKeysRow{
			Kid:       it.Key.Sort,
			Alg:       "ES256",
			Active:    it.Key.Sort == active,
			CreatedAt: it.Attrs["created_at"],
			Status:    "retired",
		}
		if retired, ok := it.Attrs["retired_at"]; ok {
			r.RetiredAt = retired
			if t, ok := parseTS(retired); ok {
				serves := t.Add(maxAccessTTL)
				r.ServesUntil = serves.Format(time.RFC3339)
				if at.Before(serves) {
					r.Status = "retiring"
				}
			}
		}
		if r.Active {
			r.Status = "active"
		}
		out = append(out, r)
	}
	return out, nil
}

// activeKid reads the kid holding the slot, empty when no key does.
func (s *Store) activeKid() (string, error) {
	it, ok, err := s.point(SigningKeySlot, activeSlot, "kid")
	if err != nil || !ok {
		return "", err
	}
	return it.Attrs["kid"], nil
}

// parseTS reads a stored timestamp. An absent attribute and an unparseable one
// are the same answer to every caller here — "this key has no retirement" —
// which is what the nullable column said too.
func parseTS(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

// EncodeKey renders a private key into the two PEM attributes a signing key
// stores, which is this layer's serialization of a key rather than the
// signer's.
func EncodeKey(priv *ecdsa.PrivateKey) (privPEM, pubPEM string, err error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", err
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privPEM, pubPEM, nil
}
