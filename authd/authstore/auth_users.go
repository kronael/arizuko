package authstore

// auth_users.go holds every read and write of `auth_users` AND of
// `oauth_identities`. The two are one entity here: an account exists to be
// reached through a provider identity, and neither operation below touches one
// without the other. Splitting them by entity would split the batch that keeps
// an identity from existing without its account, which is 6/37's third rule.
//
// An identity is sorted by "<provider>:<provider_sub>", which is the sub a
// login presents. That coordinate carries the constraint that matters: an
// identity another account already owns is ONE item, so a link that targets it
// cannot create a second — the hard fail of spec 5/1, enforced by the key
// rather than by a check that could be forgotten. Sorting by that string also
// puts a user's identities in provider order for free.
//
// The other uniqueness the table declared, one identity per (account,
// provider), is not the key's, so LinkOAuthIdentity displaces the account's
// previous identity for the same provider in its own batch. That is what the
// `ON CONFLICT(user_id, provider) DO UPDATE` it replaces did.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// OAuthIdentityForSub resolves a sub to the canonical account that has linked
// it, plus every sub that account has linked — the read half of
// LinkOAuthIdentity. It is also the mint-time alias resolver: ONE reader, so
// what /v1/identities/{sub} reports and what the token subject becomes can never
// disagree.
func (s *Store) OAuthIdentityForSub(sub string) (userID, name, createdAt string, subs []string, ok bool) {
	provider, providerSub, found := strings.Cut(sub, ":")
	if !found || provider == "" || providerSub == "" {
		return "", "", "", nil, false
	}
	ident, ok, err := s.point(OAuthIdentities, sub, "user_id")
	if err != nil || !ok {
		return "", "", "", nil, false
	}
	userID = ident.Attrs["user_id"]
	user, ok, err := s.point(AuthUsers, userID, "name", "created_at")
	if err != nil || !ok {
		return "", "", "", nil, false
	}
	linked, err := s.identitiesOf(userID)
	if err != nil {
		return "", "", "", nil, false
	}
	for _, it := range linked {
		subs = append(subs, it.Key.Sort)
	}
	return userID, user.Attrs["name"], user.Attrs["created_at"], subs, true
}

// LinkOAuthIdentity binds a provider identity to a canonical account, creating
// the account if it is absent. Both writes share one batch because an identity
// without its account is a dangling principal. providerSub is stored bare.
//
// `auth_users`' sort key IS the account's canonical sub: dispatch sets it to the
// FIRST login's value and nothing ever rewrites it. That immutability is
// load-bearing — `acl.principal` rows key on this value, so a canonical that
// moved would silently strip an account of its grants. Do not add a write that
// moves the item, and do not let an unlink remove the identity whose
// provider:provider_sub equals the account's sub (spec 5/32 § Alias
// resolution).
//
// A link that targets an identity another account owns is REFUSED here, which
// is the hard fail spec 5/1 asks for and not a merge. It is refused twice over:
// this read sees the other owner, and if the item appeared between that read and
// the batch, the identity's own Put refuses to overwrite it.
func (s *Store) LinkOAuthIdentity(userID, name, provider, providerSub string) error {
	sub := provider + ":" + providerSub
	existing, held, err := s.point(OAuthIdentities, sub, "user_id")
	if err != nil {
		return err
	}
	if held && existing.Attrs["user_id"] != userID {
		return fmt.Errorf("identity %s is already linked to %s", sub, existing.Attrs["user_id"])
	}
	writes, err := s.accountWrite(userID, name)
	if err != nil {
		return err
	}
	if held {
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: OAuthIdentities, Key: key(sub),
			Attrs: map[string]string{"linked_at": now()},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: "user_id", Op: objstore.Eq, Value: userID},
			}},
		})
	} else {
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: OAuthIdentities, Key: key(sub),
			Attrs: map[string]string{"user_id": userID, "linked_at": now()},
			Cond:  objstore.Cond{Item: objstore.ItemAbsent},
		})
	}
	displaced, err := s.displaced(userID, provider, sub)
	if err != nil {
		return err
	}
	writes = append(writes, displaced...)
	out, err := s.s.GuardedBatch(ctx(), nil, writes)
	if err != nil {
		return err
	}
	if !out.Committed {
		return fmt.Errorf("linking %s to %s was refused: another writer changed the "+
			"identity or the account first", sub, userID)
	}
	return nil
}

// accountWrite is the account half of a link: create it, or refresh the name on
// the one that is there. It is two shapes because a Put replaces the whole item
// and `created_at` is the account's birth — a Put over an existing account would
// reset it.
func (s *Store) accountWrite(userID, name string) ([]objstore.Write, error) {
	_, held, err := s.point(AuthUsers, userID, "created_at")
	if err != nil {
		return nil, err
	}
	if held {
		return []objstore.Write{{
			Op: objstore.Update, Entity: AuthUsers, Key: key(userID),
			Attrs: map[string]string{"name": name},
			Cond:  objstore.Cond{Item: objstore.ItemPresent},
		}}, nil
	}
	return []objstore.Write{{
		Op: objstore.Put, Entity: AuthUsers, Key: key(userID),
		Attrs: map[string]string{"name": name, "created_at": now()},
		Cond:  objstore.Cond{Item: objstore.ItemAbsent},
	}}, nil
}

// displaced removes the account's earlier identity for the same provider, which
// is what one identity per (account, provider) means once the key is the
// provider sub. The delete is conditioned on the owner, so it can only ever
// remove an identity this account holds.
func (s *Store) displaced(userID, provider, keep string) ([]objstore.Write, error) {
	held, err := s.identitiesOf(userID)
	if err != nil {
		return nil, err
	}
	var out []objstore.Write
	for _, it := range held {
		if it.Key.Sort == keep || !strings.HasPrefix(it.Key.Sort, provider+":") {
			continue
		}
		out = append(out, objstore.Write{
			Op: objstore.Delete, Entity: OAuthIdentities, Key: it.Key,
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: "user_id", Op: objstore.Eq, Value: userID},
			}},
		})
	}
	return out, nil
}

// identitiesOf lists the identities one account holds, in provider order — the
// sort key opens with the provider, so the engine's own order is the one the
// caller wants.
func (s *Store) identitiesOf(userID string) ([]objstore.Item, error) {
	return objstore.QueryAll(ctx(), s.s, objstore.Query{
		Entity: OAuthIdentities, Partition: Partition,
		Where:   []objstore.Cmp{{Attr: "user_id", Op: objstore.Eq, Value: userID}},
		Project: []string{"user_id"},
	})
}
