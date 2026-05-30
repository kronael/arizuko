package routd

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// route_tokens, web_routes, and the idempotency ledger. route tokens are
// the /chat/ /hook/ widget bearer tokens routd owns outright (spec 5/W) —
// a distinct credential family from authd capability tokens.

// IssueRouteToken mints a 32-byte token for jid under owner_folder, stores
// sha256(token), and returns the raw token once.
func (d *DB) IssueRouteToken(jid, ownerFolder string) (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	created := nowTS()
	_, err := d.db.Exec(`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at)
		VALUES(?,?,?,?)`, h[:], jid, ownerFolder, created)
	if err != nil {
		return "", "", err
	}
	return token, created, nil
}

// ResolveRouteToken hashes the raw token and returns (jid, owner_folder).
func (d *DB) ResolveRouteToken(token string) (jid, owner string, err error) {
	h := sha256.Sum256([]byte(token))
	err = d.db.QueryRow("SELECT jid, owner_folder FROM route_tokens WHERE token_hash=?", h[:]).Scan(&jid, &owner)
	if err == sql.ErrNoRows {
		return "", "", ErrNotFound
	}
	return jid, owner, err
}

// RouteTokenRow is a listed token (never the raw value).
type RouteTokenRow struct {
	JID         string
	OwnerFolder string
	CreatedAt   string
}

// ListRouteTokens returns the tokens owned by ownerFolder (no raw token).
func (d *DB) ListRouteTokens(ownerFolder string) ([]RouteTokenRow, error) {
	rows, err := d.db.Query("SELECT jid, owner_folder, created_at FROM route_tokens WHERE owner_folder=?", ownerFolder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteTokenRow
	for rows.Next() {
		var r RouteTokenRow
		if err := rows.Scan(&r.JID, &r.OwnerFolder, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RevokeRouteTokens deletes all tokens for jid under ownerFolder; returns
// the deleted count.
func (d *DB) RevokeRouteTokens(jid, ownerFolder string) (int64, error) {
	res, err := d.db.Exec("DELETE FROM route_tokens WHERE jid=? AND owner_folder=?", jid, ownerFolder)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- web_routes (5/V) ---

// WebRouteRow is a URL-tree access entry.
type WebRouteRow struct {
	PathPrefix string
	Access     string
	RedirectTo string
	Folder     string
	CreatedAt  string
}

// PutWebRoute upserts a web route.
func (d *DB) PutWebRoute(r WebRouteRow) error {
	_, err := d.db.Exec(`INSERT INTO web_routes(path_prefix, access, redirect_to, folder, created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(path_prefix) DO UPDATE SET access=excluded.access,
		redirect_to=excluded.redirect_to, folder=excluded.folder`,
		r.PathPrefix, r.Access, r.RedirectTo, r.Folder, nowTS())
	return err
}

// WebRoutes lists routes for a folder (or all when folder is empty).
func (d *DB) WebRoutes(folder string) ([]WebRouteRow, error) {
	q := "SELECT path_prefix, access, COALESCE(redirect_to,''), folder, created_at FROM web_routes"
	var args []any
	if folder != "" {
		q += " WHERE folder=?"
		args = append(args, folder)
	}
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebRouteRow
	for rows.Next() {
		var r WebRouteRow
		if err := rows.Scan(&r.PathPrefix, &r.Access, &r.RedirectTo, &r.Folder, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteWebRoute removes a web route by (path_prefix, folder).
func (d *DB) DeleteWebRoute(pathPrefix, folder string) (bool, error) {
	res, err := d.db.Exec("DELETE FROM web_routes WHERE path_prefix=? AND folder=?", pathPrefix, folder)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// --- idempotency ledger ---

// IdemRecord is a stored idempotent response.
type IdemRecord struct {
	RequestHash string
	Status      int
	Response    string
}

// IdemClaim attempts to claim (endpoint, key) with reqHash. Returns
// (claimed=true, _) when this caller won the insert (it must then execute
// and IdemFinish). When lost, returns (false, prior) with the stored
// record; the caller replays prior.Response if reqHash matches, else 409.
func (d *DB) IdemClaim(endpoint, key, reqHash string) (bool, IdemRecord, error) {
	created := time.Now().UTC()
	expires := created.Add(24 * time.Hour)
	res, err := d.db.Exec(`INSERT OR IGNORE INTO idempotency_keys
		(endpoint, key, request_hash, status, response, created_at, expires_at)
		VALUES(?,?,?,0,'',?,?)`,
		endpoint, key, reqHash, created.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano))
	if err != nil {
		return false, IdemRecord{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, IdemRecord{}, nil
	}
	var rec IdemRecord
	err = d.db.QueryRow("SELECT request_hash, status, response FROM idempotency_keys WHERE endpoint=? AND key=?",
		endpoint, key).Scan(&rec.RequestHash, &rec.Status, &rec.Response)
	return false, rec, err
}

// IdemFinish records the real (status, response) for a claimed ledger row.
func (d *DB) IdemFinish(endpoint, key string, status int, response string) error {
	_, err := d.db.Exec("UPDATE idempotency_keys SET status=?, response=? WHERE endpoint=? AND key=?",
		status, response, endpoint, key)
	return err
}

// SweepIdempotency drops ledger rows past expires_at (hourly GC).
func (d *DB) SweepIdempotency() error {
	_, err := d.db.Exec("DELETE FROM idempotency_keys WHERE expires_at < ?", nowTS())
	return err
}
