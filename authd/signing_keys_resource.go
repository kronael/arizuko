package main

// signing_keys_resource.go publishes signing-key METADATA at
// GET /v1/signing_keys — kid, algorithm, lifecycle timestamps, and whether the
// key still verifies. Never key material: see selectSigningKeys below, whose
// column list is the enforcement.
//
// It closes the first of spec 5/1's open items. `GET /v1/keys` serves the JWK
// Set and `auth.PublicJWKS` emits {kty,crv,x,y,kid,alg,use} — everything needed
// to verify a token and nothing about the key's life, so `active`, `created_at`
// and `retired_at` were reachable only with sqlite3 on the box. An operator
// could not answer "did the rotation take" from any surface arizuko serves.
//
// The gate, the caller-builder and the mount are audit_resource.go's, reused
// rather than re-invented (5/I is the template): one bearer verification
// against authd's own in-process key set, one literal resource:verb scope.

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// signingKeysResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/signing_keys.go). Store is non-nil only because a nil Store
// marks a forwarder and resreg.invoke skips authorization for those; list does
// not mutate, so no transaction is ever opened on it.
func (s *server) signingKeysResource() resreg.Resource {
	return resreg.Resource{
		Name:      "signing_keys",
		Endpoints: resources.SigningKeysEndpoints,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Gate:    instanceWideGate("signing_keys", "read"),
		Handler: s.signingKeysHandler,
		Store:   s.auditStore(),
	}
}

func (s *server) mountSigningKeys(m *http.ServeMux) {
	resreg.RegisterREST(m, s.signingKeysResource(), s.auditCaller)
}

func (s *server) signingKeysHandler(_ context.Context, x resreg.Execution) (any, error) {
	if x.Action != resreg.ActionList {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	rows, err := selectSigningKeys(s.a.db, s.a.maxAccessTTL, time.Now())
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}

// selectSigningKeys reads the metadata columns of signing_keys and derives each
// key's serving state.
//
// The SELECT list is the security boundary of this whole file. `priv_pem` is
// not in it, `pub_pem` is not in it, and there is no argument, filter or action
// that can add a column — the statement is a constant. loadKeys (store.go) is
// the only reader that takes priv_pem, and it is in-process and feeds the
// signer.
//
// Status is the time-based window spec 5/1 pins, computed once here rather than
// left for each reader to re-derive: a key serves while active, OR while
// now < retired_at + maxAccessTTL. `retiring` is a retired key whose
// already-issued tokens still verify; `retired` no longer verifies anything and
// is waiting on GC.
func selectSigningKeys(db *sql.DB, maxAccessTTL time.Duration, at time.Time) ([]resources.SigningKeysRow, error) {
	rows, err := db.Query(
		`SELECT kid, active, created_at, retired_at FROM signing_keys ORDER BY kid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resources.SigningKeysRow{}
	for rows.Next() {
		var (
			kid       string
			active    int
			createdAt string
			retiredAt sql.NullString
		)
		if err := rows.Scan(&kid, &active, &createdAt, &retiredAt); err != nil {
			return nil, err
		}
		r := resources.SigningKeysRow{
			Kid:       kid,
			Alg:       "ES256",
			Active:    active == 1,
			CreatedAt: createdAt,
			Status:    "retired",
		}
		if retiredAt.Valid {
			r.RetiredAt = retiredAt.String
			if t, err := time.Parse(time.RFC3339, retiredAt.String); err == nil {
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
	return out, rows.Err()
}
