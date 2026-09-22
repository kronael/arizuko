package routd

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// tokens.go is routd's read face onto `route_tokens` and `web_routes`. Route
// tokens are the /chat/ /hook/ widget bearers routd owns outright, a distinct
// credential family from authd capability tokens; the raw bearer is never
// stored, only its digest, which is the item's coordinate.

// RouteTokenTarget returns the folder the delivery tokens for jid route to, or
// "" when no token records one.
func (d *DB) RouteTokenTarget(jid string) string {
	target, err := store.RouteTokenTargetByJID(context.Background(), d.Obj(), jid)
	if err != nil {
		// A read failure here looks exactly like a token whose mint recorded no
		// target, and the grammar fallback then routes to the wrong folder. Say so.
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
// token), newest first. Pairing rows are excluded — see store.ListRouteTokens.
func (d *DB) ListRouteTokens(ownerFolder string) ([]RouteTokenRow, error) {
	tokens := d.store().ListRouteTokens(ownerFolder)
	out := make([]RouteTokenRow, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, RouteTokenRow{
			JID: t.JID, OwnerFolder: t.OwnerFolder, CreatedAt: core.TS(t.CreatedAt),
			Context: t.Context, TargetFolder: t.TargetFolder,
		})
	}
	return out, nil
}

// WebRouteRow is a URL-tree access entry.
type WebRouteRow struct {
	PathPrefix string
	Access     string
	RedirectTo string
	Folder     string
	CreatedAt  string
}

// WebRoutes lists routes for a folder (or all when empty) in path-prefix
// order — the coordinate's own order, which proxyd longest-prefix-matches.
func (d *DB) WebRoutes(folder string) ([]WebRouteRow, error) {
	routes, err := d.store().AllWebRoutes()
	if err != nil {
		return nil, err
	}
	out := make([]WebRouteRow, 0, len(routes))
	for _, r := range routes {
		if folder != "" && r.Folder != folder {
			continue
		}
		out = append(out, WebRouteRow{
			PathPrefix: r.PathPrefix, Access: r.Access, RedirectTo: r.RedirectTo,
			Folder: r.Folder, CreatedAt: core.TS(r.CreatedAt),
		})
	}
	return out, nil
}
