package routd

// web_routes_resource.go is the spec 5/16 pilot: the agent's web_route management
// tools ride ONE resreg.Resource instead of three hand-rolled ipc tool bodies.
//
// resreg owns the plumbing; routd owns the auth POLICY. The agent socket is not the
// operator socket (spec 5/16 §Blocker), so routd injects a Gate (db.Authorize keyed
// mcp:<tool> on the socket's own folder, NOT resreg's operator default) and a Visible
// predicate (auth.EffectiveActions over the caller's acl rows).
//
// The two writes are rendered HERE rather than in store, because store declares
// `web_routes`' coordinate and attribute names and leaves the writes to routd
// (store/web_routes.go says so): one declaration with two readers at different
// times — proxyd snapshots the inventory, this face changes it — never two
// formats. They ride resreg.invoke's tx so the item and the call's single
// audit_log row commit together, and they go through *store.Store so the version
// marker moves with them; a write that reached objstore itself would be
// invisible to every `arizuko export` of routd's projection (BUGS L264).
//
// The route always belongs to the agent socket's folder, never a client arg, so
// cross-folder writes are impossible by construction. The REST twin rides the SAME
// shared handler with a REST Gate + Caller injected, and the scope binds to
// Caller.Folder under either gate.

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
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
// store.Store over routd.db so resreg.invoke sends the mutation+audit batch there.
func (s *Server) webRoutesResource() resreg.Resource {
	r := resreg.Mount(resources.WebRoutesName)
	r.Handler = s.webRoutesHandler
	r.Store = s.db.store()
	return r
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
			if !strings.HasPrefix(redirectTo, pubSlot(folder)) &&
				!strings.HasPrefix(redirectTo, privSlot(folder)) {
				return nil, resreg.Errorf(http.StatusBadRequest,
					"redirect_to must point into this folder's own slot: /pub/%s/... or /priv/%s/...", folder, folder)
			}
			// Shadow constraint (spec 5/V §5): the claim above is per-EXACT-row,
			// but proxyd resolves /pub/* by LONGEST prefix, so a redirect sitting
			// above a group's slot answers for that whole slot and bounces it into
			// this folder's tree. One `/pub/` row took publishing off four of five
			// folders on an instance for three months (BUGS L314), and nothing in
			// the write path said so — the refusal names the folders it would take.
			shadowed, err := s.shadowedFolders(p, folder)
			if err != nil {
				return nil, err
			}
			if len(shadowed) > 0 {
				return nil, resreg.Errorf(http.StatusConflict,
					"redirect at %s shadows the publish path of %s: proxyd matches the longest prefix, so %s lands on this row too — narrow the path so it covers no group's slot",
					p, strings.Join(shadowed, ", "), pubSlot(shadowed[0]))
			}
		}
		// Path-claim (spec 5/V §4): a path in the caller's own slot is always
		// allowed; a top-level/other prefix only if unclaimed by another folder.
		inOwnSlot := strings.HasPrefix(p, pubSlot(folder)) ||
			strings.HasPrefix(p, privSlot(folder))
		if !inOwnSlot {
			if owner, ok := s.db.WebRouteOwner(p); ok && owner != folder {
				return nil, resreg.Errorf(http.StatusForbidden, "path prefix already claimed by folder: %s", owner)
			}
		}
		if err := s.db.putWebRouteInto(x.Writes, WebRouteRow{
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
		// Containment keys on the EMPTY folder claim: the genuine root/operator caller
		// widens to delete any folder's route via the SQL `?=''` arm; every NAMED folder,
		// including a top-level tenant, is bound to its own routes. The retired tier model
		// keyed this on tier-0, which a top-level tenant also held — that let a tenant
		// delete and hijack a sibling's route. The agent socket folder is always a named
		// group, so no agent widens.
		// The delete conditions on the rule being there AND on it being this
		// folder's, so its refusal IS the not-found answer — declared here because
		// it reaches the caller from the batch.
		if err := s.db.deleteWebRouteInto(x.Writes, p, folder,
			resreg.Errorf(http.StatusNotFound,
				"route not found or not owned by this folder")); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("del_web_route", "folder", folder, "path", p)
		return apiv1.OK{OK: true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// pubSlot is the URL prefix a folder's `~/public_html/` is served at, privSlot
// the OAuth-gated twin of it (spec 5/V §Agent web slots). One declaration, and
// every reader in this file — the redirect self-slot constraint, the path-claim,
// the shadow guard — asks it rather than spelling the prefix again.
func pubSlot(folder string) string  { return "/pub/" + folder + "/" }
func privSlot(folder string) string { return "/priv/" + folder + "/" }

// shadowedFolders names the groups whose publish slot a route at pathPrefix
// would answer for, in folder order. proxyd matches a raw string prefix and the
// longest one wins (proxyd.matchWebRoute), so `/pub/` covers every folder and
// `/pub/a` covers `atlas`; the test here is proxyd's own, not a segment walk.
//
// A folder governs its own root, so an exact claim on the caller's own slot is
// not shadowing — anything ABOVE it still is, because it reaches the siblings.
//
// A group read that fails answers nil, which would let every prefix through, so
// nil is refused here rather than read as "this instance has no groups".
func (s *Server) shadowedFolders(pathPrefix, caller string) ([]string, error) {
	groups := s.db.AllGroups()
	if groups == nil {
		return nil, resreg.Errorf(http.StatusInternalServerError,
			"cannot read the group list, so cannot tell what %s would shadow", pathPrefix)
	}
	var out []string
	for f := range groups {
		slot := pubSlot(f)
		if !strings.HasPrefix(slot, pathPrefix) || (f == caller && slot == pathPrefix) {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
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
		if !authorize(folder, "mcp:"+name, nil) {
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

// putWebRouteInto buffers one web_routes upsert on resreg's write set, so the
// mutation rides the batch that carries its audit_log row.
//
// IT IS ONE WRITE, a Merge, and that is what makes it buffer-able at all: the
// pair it replaces — a create-if-absent whose refusal selected an Update — named
// one item twice, which no batch may do (objstore.ValidateBatch), and the
// refusal it turned on could not answer for a write that has not been sent.
// `created_at` is an Init, so the instant the rule's first claim recorded
// survives every later upsert, which is what the Put half was for.
//
// `created_at` is core.TS at 30 bytes: that is the width resreg's own entity path
// stamps a StampedField at, and every writer of these items has to agree byte for
// byte because a comparison over TEXT answers by byte order (BUGS L97).
func (d *DB) putWebRouteInto(ws *store.WriteSet, r WebRouteRow) error {
	key, err := store.WebRouteKey(r.PathPrefix)
	if err != nil {
		return err
	}
	owned := map[string]string{store.AttrWebAccess: r.Access, store.AttrWebFolder: r.Folder}
	// Absent is the only null this model has, so an access rule that redirects
	// nowhere carries no redirect_to at all — and a merge that leaves the
	// attribute alone would keep the target of a rule the operator has since
	// pointed elsewhere. It is REMOVED rather than emptied.
	var drop []string
	if r.RedirectTo == "" {
		drop = []string{store.AttrWebRedirectTo}
	} else {
		owned[store.AttrWebRedirectTo] = r.RedirectTo
	}
	ws.Add(objstore.Write{
		Op: objstore.Merge, Entity: store.WebRoutes, Key: key, Attrs: owned, Remove: drop,
		Init: map[string]string{store.AttrWebCreatedAt: core.NowTS()},
	})
	return nil
}

// deleteWebRouteInto buffers one web_routes removal on the caller's write set,
// and refused is what "no such rule of yours" means. The path prefix is the
// coordinate, so the delete names one item; a non-empty `folder` must match the
// item's own, which widens ONLY when folder is empty. Containment therefore lives
// entirely in how the caller resolves its folder — the agent socket's own group,
// or the REST target bounded to the JWT subtree — never here.
func (d *DB) deleteWebRouteInto(ws *store.WriteSet, pathPrefix, folder string, refused error) error {
	key, err := store.WebRouteKey(pathPrefix)
	if err != nil {
		return err
	}
	w := objstore.Write{
		Op: objstore.Delete, Entity: store.WebRoutes, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	}
	if folder != "" {
		w.Cond.Where = []objstore.Cmp{
			{Attr: store.AttrWebFolder, Op: objstore.Eq, Value: folder},
		}
	}
	return ws.AddGuarded(refused, w)
}
