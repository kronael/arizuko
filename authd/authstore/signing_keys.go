package authstore

// signing_keys.go holds every read and write of the trust root: one key signs,
// and each retired key keeps verifying until its window closes.
// `signing_keys` holds the keypairs, sorted by kid (`<unix>-<hex>`, so key
// order IS creation order). `signing_key_slot` holds ONE item, at sort key
// `active`, naming the kid that signs: a key carries no `active` attribute, so
// two active keys are not a state this store can represent. Rotation moves
// the slot under a compare-and-set on the kid it replaces.

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
// slot first. It is a REFUSAL, not a fault: the loser must not sign.
var ErrRotationLost = errors.New("another rotation moved the active signing key first")

// StoredKey is a signing key as stored, with its parsed private key.
type StoredKey struct {
	Key       *auth.SigningKey
	Active    bool
	RetiredAt *time.Time
}

// SigningKeysWithMaterial reads every signing key WITH its private half. It is
// the only reader that takes priv_pem, and it feeds the in-process signer. It
// applies no serving window: the caller holds maxAccessTTL.
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

// RotateActiveKey retires the key holding the slot as of `at` and installs k,
// in ONE batch. The slot write is conditioned on the kid it replaces, so the
// loser's batch is refused whole and the trust root never splits into two
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
		// No key holds the slot: a first boot, or a slot an operator deleted.
		// ItemAbsent elects one winner there.
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

// SigningKeyMetadata reads the non-secret attributes of every signing key and
// derives each key's serving state. The PROJECTION is the security boundary:
// `priv_pem` and `pub_pem` are not in it and nothing can add them, so the
// private half never leaves the engine. Status is spec 5/1's window: a key
// serves while it holds the slot, OR while now < retired_at + maxAccessTTL;
// `retiring` still verifies already-issued tokens, `retired` verifies nothing.
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
// both mean "this key has no retirement".
func parseTS(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

// EncodeKey renders a private key into the two PEM attributes a signing key
// stores.
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
