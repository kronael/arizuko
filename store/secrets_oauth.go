package store

import (
	"database/sql"
	"strings"
	"time"
)

// secrets_oauth.go is the surrogate-OAuth write/read half of the secrets table
// (spec 5/15): the four columns migration routd/0017 adds (provider, refresh_val,
// expires_at, scope_list). value + refresh_val seal through the SAME storeValue/
// open AES-256-GCM keyring as a pasted PAT — only these methods know a row came
// from OAuth. A PAT row leaves all four NULL and never touches this file.

// OAuthSecret is a user-scoped surrogate-OAuth credential. Value + Refresh are
// DECRYPTED. ExpiresAt zero = the provider returned no expiry (non-expiring).
type OAuthSecret struct {
	Key       string
	Value     string
	Provider  string
	Refresh   string
	ExpiresAt time.Time
	Scope     string
}

// PutOAuthSecret upserts a user-scoped OAuth credential row: value + refresh
// sealed under the keyring (same seal as value), plus provider/expiry/scope
// metadata. The dashd Connect-dance write path. No audit row (FS-mounted daemon
// writes are audit-free, like PutSecretRow).
func (s *Store) PutOAuthSecret(userSub, key, value, provider, refresh string, expiresAt time.Time, scope string) error {
	if err := validateScope(ScopeUser, userSub, key); err != nil {
		return err
	}
	sealedVal, err := s.storeValue(value)
	if err != nil {
		return err
	}
	sealedRef, err := s.storeValue(refresh)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at, provider, refresh_val, expires_at, scope_list)
		 VALUES ('user', ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
		   value = excluded.value, created_at = excluded.created_at, provider = excluded.provider,
		   refresh_val = excluded.refresh_val, expires_at = excluded.expires_at, scope_list = excluded.scope_list`,
		userSub, key, sealedVal, time.Now().UTC().Format(time.RFC3339), provider, sealedRef, nullTime(expiresAt), scope)
	return err
}

// UpdateOAuthSecret reseals value+refresh and updates expiry/scope for an
// existing user OAuth row — the broker's near-expiry refresh writeback.
func (s *Store) UpdateOAuthSecret(userSub, key, value, refresh string, expiresAt time.Time, scope string) error {
	sealedVal, err := s.storeValue(value)
	if err != nil {
		return err
	}
	sealedRef, err := s.storeValue(refresh)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE secrets SET value = ?, refresh_val = ?, expires_at = ?, scope_list = ?
		 WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		sealedVal, sealedRef, nullTime(expiresAt), scope, userSub, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// ClearOAuthSecret nulls expires_at + refresh_val on a user OAuth row (refresh
// rejected → reconnect required). value + provider stay so the connections page
// can show a stale link that needs reconnecting.
func (s *Store) ClearOAuthSecret(userSub, key string) error {
	_, err := s.db.Exec(
		`UPDATE secrets SET expires_at = NULL, refresh_val = NULL
		 WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		userSub, key)
	return err
}

// UserOAuthSecrets returns the user-scoped OAuth rows (provider set) among
// `keys`; PAT rows (provider NULL) are skipped. Value + Refresh decrypted.
func (s *Store) UserOAuthSecrets(userSub string, keys []string) ([]OAuthSecret, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(keys)+1)
	args = append(args, userSub)
	for _, k := range keys {
		args = append(args, k)
	}
	return s.queryOAuth(
		`SELECT key, value, provider, COALESCE(refresh_val,''), expires_at, COALESCE(scope_list,'')
		 FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ? AND provider IS NOT NULL AND provider <> ''
		   AND key IN (`+sqlPH(len(keys))+`)`, args...)
}

// ListUserConnections returns every user-scoped OAuth row for the connections
// page. Values decrypted (the page shows only key/provider/expiry, never token).
func (s *Store) ListUserConnections(userSub string) ([]OAuthSecret, error) {
	return s.queryOAuth(
		`SELECT key, value, provider, COALESCE(refresh_val,''), expires_at, COALESCE(scope_list,'')
		 FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ? AND provider IS NOT NULL AND provider <> ''
		 ORDER BY key`, userSub)
}

func (s *Store) queryOAuth(q string, args ...any) ([]OAuthSecret, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthSecret
	for rows.Next() {
		var oa OAuthSecret
		var val, ref string
		var exp sql.NullString
		if err := rows.Scan(&oa.Key, &val, &oa.Provider, &ref, &exp, &oa.Scope); err != nil {
			return nil, err
		}
		if oa.Value, err = s.open(val); err != nil {
			return nil, err
		}
		if ref != "" {
			if oa.Refresh, err = s.open(ref); err != nil {
				return nil, err
			}
		}
		if exp.Valid && exp.String != "" {
			oa.ExpiresAt = parseTime(exp.String)
		}
		out = append(out, oa)
	}
	return out, rows.Err()
}

// nullTime maps a zero time to SQL NULL (non-expiring), else RFC3339 UTC.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime accepts the RFC3339 form nullTime writes, tolerating a bare
// SQLite datetime ("2006-01-02 15:04:05") for hand-set test rows.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
