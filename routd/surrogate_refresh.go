package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth/surrogate"
)

// nearExpiryWindow is the lead time before an OAuth access token's expiry at
// which the broker refreshes it (spec 5/15: refresh when expires_at−now < 60s).
const nearExpiryWindow = 60 * time.Second

// refreshOAuth refreshes user-scoped surrogate-OAuth rows among `required`,
// writing the fresh token into `out` and the DB. Returns a non-nil "reconnect"
// error naming the keys whose refresh_token the provider rejected (their oauth
// columns are nulled and the key dropped from out); a transient failure keeps
// the stale token and reports no error.
//
// force=false is the PROACTIVE half (spec 5/15): refresh only a row within
// nearExpiryWindow of expiry; a non-expiring row (expires_at NULL) is left as
// is. force=true is the REACTIVE half behind a 401 — it refreshes every OAuth
// row regardless of expires_at, which is the whole point: the reactive path
// exists for providers whose expires_in is optimistic, so the stored expiry
// still looks healthy while the token is already dead.
func (d *DB) refreshOAuth(userSub string, required []string, out map[string]string, force bool) error {
	rows, err := d.secretStore().UserOAuthSecrets(userSub, required)
	if err != nil {
		slog.Warn("surrogate: read oauth rows", "sub", userSub, "err", err)
		return nil // best-effort: don't fail the connector call on a read error
	}
	var reconnect []string
	for _, oa := range rows {
		if !force && (oa.ExpiresAt.IsZero() || time.Until(oa.ExpiresAt) >= nearExpiryWindow) {
			out[oa.Key] = oa.Value // fresh; ensure the user-scoped value wins
			continue
		}
		tok, rerr := d.surrogate.Refresh(context.Background(), oa.Provider, oa.Refresh)
		if rerr != nil {
			if errors.Is(rerr, surrogate.ErrReconnect) {
				if cerr := d.secretStore().ClearOAuthSecret(userSub, oa.Key); cerr != nil {
					slog.Warn("surrogate: clear revoked row", "sub", userSub, "key", oa.Key, "err", cerr)
				}
				delete(out, oa.Key)
				reconnect = append(reconnect, oa.Key)
				continue
			}
			slog.Warn("surrogate: transient refresh failure; using stale token",
				"sub", userSub, "key", oa.Key, "provider", oa.Provider, "err", rerr)
			continue
		}
		if uerr := d.secretStore().UpdateOAuthSecret(userSub, oa.Key, tok.Access, tok.Refresh, tok.ExpiresAt, tok.Scope); uerr != nil {
			slog.Warn("surrogate: write refreshed row", "sub", userSub, "key", oa.Key, "err", uerr)
		}
		out[oa.Key] = tok.Access
	}
	if len(reconnect) > 0 {
		return fmt.Errorf("credential %s revoked — reconnect at /dash/me/connections", strings.Join(reconnect, ", "))
	}
	return nil
}
