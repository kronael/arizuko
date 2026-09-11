package store

// secrets_oauth.go is the surrogate-OAuth half of the `secrets` entity (spec
// 5/15): the four attributes `provider`, `refresh_val`, `expires_at` and
// `scope_list`. value + refresh_val seal through the SAME storeValue/open
// AES-256-GCM keyring as a pasted PAT — only these methods know a row came from
// OAuth. A PAT row carries none of the four and never touches this file.
//
// IT WRITES THROUGH secrets.go's HELPERS and declares no coordinate of its own.
// One key builder, one attribute-name set: a second spelling of either would put
// this file's items where the other's read does not look, and the two halves of
// one credential would live at two coordinates.
//
// `provider` IS WHAT TELLS THE TWO APART, so every read here filters on its
// PRESENCE rather than on a value — absent is the only null this model has, and
// setAttr never writes an empty one, so an item that carries the attribute is an
// OAuth row and one that does not is a PAT. That is exactly what
// `provider IS NOT NULL AND provider <> ''` said.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

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
//
// A zero expiry writes NO `expires_at` attribute — a non-expiring credential —
// and an empty scope no `scope_list`, because absent is the only null this model
// has. The upsert owns all six attributes, so a PAT that this call replaces
// becomes an OAuth row whole.
func (s *Store) PutOAuthSecret(userSub, key, value, provider, refresh string, expiresAt time.Time, scope string) error {
	if err := validateScope(ScopeUser, userSub, key); err != nil {
		return err
	}
	owned, err := s.oauthAttrs(value, refresh, expiresAt, scope)
	if err != nil {
		return err
	}
	setAttr(owned, attrSecProvider, provider)
	owned[attrSecCreatedAt] = core.NowTS()
	return s.putSecret(context.Background(), nil, ScopeUser, userSub, key, owned)
}

// UpdateOAuthSecret reseals value+refresh and updates expiry/scope for an
// existing user OAuth row — the broker's near-expiry refresh writeback. Returns
// ErrSecretNotFound when there is no such row, which is what the statement's
// zero matched rows said: an Update always requires the item to exist, and a
// refused batch is that answer.
//
// A zero expiry or an empty scope TAKES THE ATTRIBUTE AWAY rather than writing
// an empty one, so a refresh that returns no expiry leaves the same shape a mint
// with none does. `created_at` is NOT touched: it is when the connection was
// made, and a refresh is not a new connection.
func (s *Store) UpdateOAuthSecret(userSub, key, value, refresh string, expiresAt time.Time, scope string) error {
	k, err := secretKey(ScopeUser, userSub, key)
	if err != nil {
		return err
	}
	attrs, err := s.oauthAttrs(value, refresh, expiresAt, scope)
	if err != nil {
		return err
	}
	w := objstore.Write{Op: objstore.Update, Entity: Secrets, Key: k, Attrs: attrs}
	for _, name := range []string{attrSecExpiresAt, attrSecScopeList} {
		if _, ok := attrs[name]; !ok {
			w.Remove = append(w.Remove, name)
		}
	}
	updated, err := s.TryWriteEntities(context.Background(), nil, w)
	if err != nil {
		return err
	}
	if !updated {
		return ErrSecretNotFound
	}
	return nil
}

// ClearOAuthSecret takes `expires_at` and `refresh_val` away from a user OAuth
// row (refresh rejected → reconnect required). value + provider stay so the
// connections page can show a stale link that needs reconnecting.
//
// The two go in ONE write, because a reset that left one behind is a row the
// broker still reads as refreshable. Taking them away is the only way to make
// them missing: writing "" leaves an attribute a presence test still finds.
//
// A row that is not there is an ERROR: the refresh path read it a moment ago, so
// a refusal is the row having gone under the clear, not a clear with nothing to
// do.
func (s *Store) ClearOAuthSecret(userSub, key string) error {
	k, err := secretKey(ScopeUser, userSub, key)
	if err != nil {
		return err
	}
	err = s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Secrets, Key: k,
		Remove: []string{attrSecExpiresAt, attrSecRefreshVal},
	})
	return err
}

// UserOAuthSecrets returns the user-scoped OAuth rows (provider set) among
// `keys`; PAT rows carry no provider and are skipped. Value + Refresh decrypted.
func (s *Store) UserOAuthSecrets(userSub string, keys []string) ([]OAuthSecret, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return s.oauthSecrets(userSub, func(key string) bool { return slices.Contains(keys, key) })
}

// ListUserConnections returns every user-scoped OAuth row for the connections
// page, in key order. Values decrypted (the page shows only key/provider/expiry,
// never token).
func (s *Store) ListUserConnections(userSub string) ([]OAuthSecret, error) {
	return s.oauthSecrets(userSub, func(string) bool { return true })
}

// oauthSecrets is the ONE read both callers above share, so they cannot disagree
// about which rows count as OAuth. The scope is a leading fragment of the
// coordinate, so it is a begins_with; `key` is the coordinate's last field, so
// the read comes back in key order.
func (s *Store) oauthSecrets(userSub string, want func(key string) bool) ([]OAuthSecret, error) {
	items, err := s.secretsIn(context.Background(), ScopeUser, userSub)
	if err != nil {
		return nil, err
	}
	var out []OAuthSecret
	for _, it := range items {
		provider := it.Attrs[attrSecProvider]
		if provider == "" {
			continue
		}
		key, err := secretKeyOf(it)
		if err != nil {
			return nil, err
		}
		if !want(key) {
			continue
		}
		oa := OAuthSecret{Key: key, Provider: provider, Scope: it.Attrs[attrSecScopeList]}
		if oa.Value, err = s.open(it.Attrs[attrSecValue]); err != nil {
			return nil, err
		}
		if ref := it.Attrs[attrSecRefreshVal]; ref != "" {
			if oa.Refresh, err = s.open(ref); err != nil {
				return nil, err
			}
		}
		if exp := it.Attrs[attrSecExpiresAt]; exp != "" {
			oa.ExpiresAt = parseTime(exp)
		}
		out = append(out, oa)
	}
	return out, nil
}

// oauthAttrs seals the two credentials and renders the metadata beside them.
func (s *Store) oauthAttrs(value, refresh string, expiresAt time.Time, scope string) (map[string]string, error) {
	sealedVal, err := s.storeValue(value)
	if err != nil {
		return nil, err
	}
	sealedRef, err := s.storeValue(refresh)
	if err != nil {
		return nil, err
	}
	attrs := map[string]string{
		attrSecValue:      sealedVal,
		attrSecRefreshVal: sealedRef,
	}
	if !expiresAt.IsZero() {
		attrs[attrSecExpiresAt] = expiresAt.UTC().Format(time.RFC3339)
	}
	setAttr(attrs, attrSecScopeList, scope)
	return attrs, nil
}

// parseTime accepts the RFC3339 form an expiry is written in, tolerating a bare
// SQLite datetime ("2006-01-02 15:04:05") for hand-set test rows.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
