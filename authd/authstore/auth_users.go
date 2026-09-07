package authstore

// auth_users.go holds every read and write of `auth_users` AND of
// `oauth_identities`. The two tables are one entity here: an account exists to
// be reached through a provider identity, and neither operation below touches
// one table without the other. Splitting them by table would split the
// transaction that keeps an identity row from existing without its user row,
// which is 6/37's third rule.

import "strings"

// OAuthIdentityForSub resolves a sub to the canonical user that has linked it,
// plus every sub that user has linked — the read half of LinkOAuthIdentity.
// It is also the mint-time alias resolver: ONE reader, so what
// /v1/identities/{sub} reports and what the token subject becomes can never
// disagree.
func (s *Store) OAuthIdentityForSub(sub string) (userID, name, createdAt string, subs []string, ok bool) {
	provider, providerSub, found := strings.Cut(sub, ":")
	if !found || provider == "" || providerSub == "" {
		return "", "", "", nil, false
	}
	err := s.db.QueryRow(
		`SELECT u.user_id, u.name, u.created_at
		   FROM oauth_identities oi JOIN auth_users u ON u.user_id = oi.user_id
		  WHERE oi.provider = ? AND oi.provider_sub = ?`,
		provider, providerSub).Scan(&userID, &name, &createdAt)
	if err != nil {
		return "", "", "", nil, false
	}
	rows, err := s.db.Query(
		`SELECT provider, provider_sub FROM oauth_identities
		  WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return "", "", "", nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var p, ps string
		if err := rows.Scan(&p, &ps); err != nil {
			return "", "", "", nil, false
		}
		subs = append(subs, p+":"+ps)
	}
	if err := rows.Err(); err != nil {
		return "", "", "", nil, false
	}
	return userID, name, createdAt, subs, true
}

// LinkOAuthIdentity binds a provider identity to a canonical account, creating
// the account if it is absent. Both writes share one transaction because an
// identity row without its user row is a dangling principal. providerSub is
// stored bare.
//
// `auth_users.user_id` IS the account's canonical sub: dispatch sets it to the
// FIRST login's value and nothing ever rewrites it. That immutability is
// load-bearing — `acl.principal` rows key on this value, so a canonical that moved
// would silently strip an account of its grants. Do not add an UPDATE of user_id,
// and do not let an unlink remove the identity whose provider:provider_sub equals
// user_id (spec 5/32 § Alias resolution).
func (s *Store) LinkOAuthIdentity(userID, name, provider, providerSub string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO auth_users (user_id, name, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET name = excluded.name`,
		userID, name, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO oauth_identities (user_id, provider, provider_sub, linked_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET provider_sub = excluded.provider_sub`,
		userID, provider, providerSub, now()); err != nil {
		return err
	}
	return tx.Commit()
}
