package routd

import (
	"log/slog"

	"github.com/kronael/arizuko/store"
)

// route_tokens and web_routes. route tokens are the
// /chat/ /hook/ widget bearer tokens routd owns outright — a distinct credential
// family from authd capability tokens.

// RouteTokenTarget returns the folder the delivery tokens for jid route to, or
// "" when no token records one. store.RouteTokenTargetByJID is the single
// reader; this wraps it for the loop's call site.
func (d *DB) RouteTokenTarget(jid string) string {
	target, err := store.RouteTokenTargetByJID(d.db, jid)
	if err != nil {
		// A read failure here looks exactly like a pre-column row, and the
		// grammar fallback then routes to the wrong folder. Say so.
		slog.Error("route token target read failed; falling back to the JID grammar",
			"jid", jid, "err", err)
		return ""
	}
	return target
}

// RouteTokenRow is a listed token (never the raw value).
type RouteTokenRow struct {
	JID          string
	OwnerFolder  string
	CreatedAt    string
	Context      string
	TargetFolder string
}

// ListRouteTokens returns the delivery tokens owned by ownerFolder (no raw
// token). Pairing rows are excluded — see store.ListRouteTokens.
func (d *DB) ListRouteTokens(ownerFolder string) ([]RouteTokenRow, error) {
	rows, err := d.db.Query("SELECT jid, owner_folder, created_at, COALESCE(context,''), COALESCE(target_folder,'') FROM route_tokens WHERE owner_folder=? AND kind=?",
		ownerFolder, store.RouteTokenKindRoute)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteTokenRow
	for rows.Next() {
		var r RouteTokenRow
		if err := rows.Scan(&r.JID, &r.OwnerFolder, &r.CreatedAt, &r.Context, &r.TargetFolder); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WebRouteRow is a URL-tree access entry.
type WebRouteRow struct {
	PathPrefix string
	Access     string
	RedirectTo string
	Folder     string
	CreatedAt  string
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
