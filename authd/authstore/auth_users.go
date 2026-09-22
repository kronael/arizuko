package authstore

// auth_users.go holds every read and write of `auth_users` AND of
// `oauth_identities`: neither operation touches one without the other, and
// splitting them would split the batch that keeps an identity from existing
// without its account.
//
// An identity is sorted by "<provider>:<provider_sub>", the sub a login
// presents, so an identity another account already owns is ONE item and a
// link that targets it cannot create a second (the hard fail of spec 5/1,
// enforced by the key). One identity per (account, provider) is not the key's
// uniqueness, so LinkOAuthIdentity displaces the previous identity in its own
// batch.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// OAuthIdentityForSub resolves a sub to the canonical account that has linked
// it, plus every sub that account has linked. It is also the mint-time alias
// resolver: ONE reader, so /v1/identities/{sub} and the token subject can
// never disagree.
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
// the account if it is absent; both writes share one batch. providerSub is
// stored bare.
//
// `auth_users`' sort key IS the account's canonical sub, set by the FIRST
// login and never rewritten: `acl.principal` rows key on it, so a canonical
// that moved would silently strip an account of its grants. Do not add a
// write that moves the item, and do not let an unlink remove the identity
// whose provider:provider_sub equals the account's sub (spec 5/32-acl-unified § Alias
// resolution). A link that targets an identity another account owns is
// REFUSED, twice over: this read sees the other owner, and the identity's own
// Put refuses to overwrite one that appeared since.
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
	committed, err := s.s.GuardedBatch(ctx(), nil, writes)
	if err != nil {
		return err
	}
	if !committed {
		return fmt.Errorf("linking %s to %s was refused: another writer changed the "+
			"identity or the account first", sub, userID)
	}
	return nil
}

// accountWrite is the account half of a link: create it, or refresh the name on
// the one that is there. Two shapes because a Put over an existing account
// would reset `created_at`.
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

// displaced removes the account's earlier identity for the same provider. The
// delete is conditioned on the owner, so it can only remove an identity this
// account holds.
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

// identitiesOf lists the identities one account holds, in provider order.
func (s *Store) identitiesOf(userID string) ([]objstore.Item, error) {
	return s.s.Query(ctx(), objstore.Query{
		Entity: OAuthIdentities, Partition: Partition,
		Where:   []objstore.Cmp{{Attr: "user_id", Op: objstore.Eq, Value: userID}},
		Project: []string{"user_id"},
	})
}
