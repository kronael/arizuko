package routd

// web_routes_resource.go is the spec 5/16 pilot: the agent's web_route
// management tools (set_web_route/del_web_route/list_web_routes) ride ONE
// resreg.Resource instead of three hand-rolled ipc/ipc.go tool bodies.
//
// resreg owns the plumbing (handler dispatch + one tx wrapping the mutation
// AND its audit_log row); routd owns the auth POLICY. The agent socket is
// not the operator socket (spec 5/16 §Blocker), so routd injects:
//
//   - Gate: db.Authorize keyed mcp:<tool> on the socket's own folder (the 4/R
//     single evaluator), NOT resreg's operator default.
//   - Visible: auth.EffectiveActions over the caller's acl rows, so a caller
//     that could not SEE a tool in tools/list still can't.
//
// The route always belongs to the agent socket's folder (never a client arg),
// so cross-folder writes are impossible by construction — the same guarantee
// the deleted ipc bodies gave. The REST twin (/v1/web_routes) rides the SAME
// shared handler (web_routes_http.go) via resreg.RegisterREST with a REST Gate
// + Caller injected; the delete/list scope binds to Caller.Folder under either
// gate, so containment is uniform across both faces.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// webRoutesMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (WebRoutesMCPNames). Aliased here for the Gate's action→policy-name
// lookup below.
var webRoutesMCPNames = resources.WebRoutesMCPNames

// webRoutesResource is the single renderer for the agent's three web_route
// tools. Endpoints exist only to drive deriveMCPTools (Action ∩ MCPDoc) — the
// REST face is NOT mounted from this resource (see file header). Store is a
// store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
func (s *Server) webRoutesResource() resreg.Resource {
	return resreg.Resource{
		Name:      "web_routes",
		Endpoints: resources.WebRoutesEndpoints, // single source: doc + MCP read one list
		MCPDoc:    resources.WebRoutesMCPDoc,     // single source (resreg/resources)
		MCPArgs:   resources.WebRoutesMCPArgs,
		MCPNames:  webRoutesMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.webRoutesHandler,
		Store:   store.New(s.db.SQL()),
	}
}

// webRoutesHandler runs create(upsert)/delete/list against routd.db, folding in
// the bespoke semantics the deleted ipc bodies enforced: self-slot + first-claim
// ownership on create, empty-folder (root/operator) delete widening. The target
// folder is always the caller's own folder (x.Caller.Folder = the agent socket's
// folder); web_routes is never a client-supplied folder here.
func (s *Server) webRoutesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	folder := x.Caller.Folder
	switch x.Action {
	case resreg.ActionList:
		rows, err := s.db.WebRoutes(folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]ipc.WebRoute, len(rows))
		for i, r := range rows {
			out[i] = ipc.WebRoute{
				PathPrefix: r.PathPrefix, Access: r.Access, RedirectTo: r.RedirectTo,
				Folder: r.Folder, CreatedAt: r.CreatedAt,
			}
		}
		return out, nil

	case resreg.ActionCreate:
		p := argString(x.Args, "path")
		if p == "" || p[0] != '/' {
			return nil, resreg.Errorf(http.StatusBadRequest, "path must start with /")
		}
		access := argString(x.Args, "access")
		switch access {
		case "public", "auth", "deny", "redirect":
		default:
			return nil, resreg.Errorf(http.StatusBadRequest, "access must be one of: public, auth, deny, redirect")
		}
		redirectTo := argString(x.Args, "redirect_to")
		if access == "redirect" {
			if redirectTo == "" {
				return nil, resreg.Errorf(http.StatusBadRequest, "redirect_to required when access=redirect")
			}
			// Self-slot constraint: redirect_to must land in this folder's own
			// /pub/<folder>/ or /priv/<folder>/ slot — no external URLs, no other
			// folders (prevents open-redirect + cross-folder impersonation).
			if !strings.HasPrefix(redirectTo, "/pub/"+folder+"/") &&
				!strings.HasPrefix(redirectTo, "/priv/"+folder+"/") {
				return nil, resreg.Errorf(http.StatusBadRequest,
					"redirect_to must point into this folder's own slot: /pub/%s/... or /priv/%s/...", folder, folder)
			}
		}
		// Path-claim (spec 5/V §4): a path in the caller's own slot is always
		// allowed; a top-level/other prefix only if unclaimed by another folder.
		inOwnSlot := strings.HasPrefix(p, "/pub/"+folder+"/") ||
			strings.HasPrefix(p, "/priv/"+folder+"/")
		if !inOwnSlot {
			if owner, ok := s.db.WebRouteOwner(p); ok && owner != folder {
				return nil, resreg.Errorf(http.StatusForbidden, "path prefix already claimed by folder: %s", owner)
			}
		}
		if err := putWebRouteTx(ctx, x.Tx, WebRouteRow{
			PathPrefix: p, Access: access, RedirectTo: redirectTo, Folder: folder,
		}); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("set_web_route", "folder", folder, "path", p, "access", access)
		return apiv1.OK{OK: true}, nil

	case resreg.ActionDelete:
		p := argString(x.Args, "path")
		if p == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		// Containment keys on the EMPTY folder claim: the genuine root/operator
		// caller (empty folder) widens to delete any folder's route via the SQL
		// `?=''` arm; every NAMED folder — including a top-level tenant — is bound
		// to its own routes. The retired tier model keyed this on tier-0, which a
		// top-level tenant also held: that let a tenant delete + hijack a sibling
		// tenant's route (the 5/16 list-all leak class). The agent socket folder is
		// always a named group, so no agent widens; only the root REST/CLI operator
		// (folder="") does.
		ok, err := deleteWebRouteTx(ctx, x.Tx, p, folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "route not found or not owned by this folder")
		}
		slog.Info("del_web_route", "folder", folder, "path", p)
		return apiv1.OK{OK: true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// webRoutesPostBuild returns the ServeMCP seam that mounts the web_route tools
// on the agent socket, with the db.Authorize Gate + the turn's visibility view
// injected. Only grants the caller already holds can widen visibility, so a
// denied caller sees nothing new.
func (s *Server) webRoutesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.webRoutesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := webRoutesMCPNames[x.Action]
		// web_route tools act on the caller's OWN folder (the handler scopes every row
		// by folder). One evaluator: the caller must hold the tool for its folder.
		if !authorize(callerSub, folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// argString reads a string arg from a resreg.Args map (MCP args decode to
// their declared Go type; string args land as string or are absent).
func argString(args resreg.Args, key string) string {
	s, _ := args[key].(string)
	return s
}

// putWebRouteTx upserts a web_routes row on tx (mirrors DB.PutWebRoute so the
// mutation lands in resreg.invoke's tx alongside its audit_log row). ON CONFLICT
// keeps the original created_at, matching DB.PutWebRoute.
func putWebRouteTx(ctx context.Context, tx *sql.Tx, r WebRouteRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO web_routes(path_prefix, access, redirect_to, folder, created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(path_prefix) DO UPDATE SET access=excluded.access,
		redirect_to=excluded.redirect_to, folder=excluded.folder`,
		r.PathPrefix, r.Access, r.RedirectTo, r.Folder, nowTS())
	return err
}

// deleteWebRouteTx removes a web_routes row on tx. The `(folder=? OR ?='')`
// predicate widens ONLY when folder is empty — the genuine root/operator caller
// (empty folder claim) deletes any folder's route; every named folder matches
// exactly its own. Containment therefore lives entirely in how the caller
// resolves its folder — the agent socket's own group, or the REST target bounded
// to the JWT subtree — never in this SQL.
func deleteWebRouteTx(ctx context.Context, tx *sql.Tx, pathPrefix, folder string) (bool, error) {
	res, err := tx.ExecContext(ctx,
		"DELETE FROM web_routes WHERE path_prefix=? AND (folder=? OR ?='')",
		pathPrefix, folder, folder)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
