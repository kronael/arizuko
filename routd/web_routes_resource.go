package routd

// web_routes_resource.go is the spec 5/16 pilot: the agent's web_route tools
// on ONE resreg.Resource, with routd injecting the auth POLICY (a Gate keyed
// mcp:<tool> on the socket's own folder, a Visible predicate). The two writes
// are rendered HERE: store declares the coordinate and attribute names and
// leaves the writes to routd (one declaration, two readers — proxyd snapshots
// the inventory, this face changes it), through *store.Store so the version
// marker moves (BUGS L264). The route always belongs to the agent socket's
// folder, never a client arg, so cross-folder writes are impossible by
// construction; the REST twin rides the SAME handler.

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

// webRoutesMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources; the Gate's action→policy-name lookup.
var webRoutesMCPNames = resources.WebRoutesMCPNames

// webRoutesResource is the single renderer for the three web_route tools.
// Endpoints exist only to drive deriveMCPTools; the REST face is not mounted
// from this resource.
func (s *Server) webRoutesResource() resreg.Resource {
	r := resreg.Mount(resources.WebRoutesName)
	r.Handler = s.webRoutesHandler
	r.Store = s.db.store()
	return r
}

// webRoutesHandler runs create(upsert)/delete/list: self-slot + first-claim
// ownership on create, empty-folder (root) delete widening. The target folder
// is always x.Caller.Folder, never a client-supplied one.
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
			// redirect_to must land in this folder's own slot: no open redirect,
			// no cross-folder impersonation.
			if !strings.HasPrefix(redirectTo, pubSlot(folder)) &&
				!strings.HasPrefix(redirectTo, privSlot(folder)) {
				return nil, resreg.Errorf(http.StatusBadRequest,
					"redirect_to must point into this folder's own slot: /pub/%s/... or /priv/%s/...", folder, folder)
			}
			// Shadow constraint (spec 5/V §5): proxyd resolves /pub/* by LONGEST
			// prefix, so a redirect above a group's slot answers for the whole
			// slot. One `/pub/` row took publishing off four of five folders for
			// three months (BUGS L314); the refusal names the folders it would take.
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
		// Path-claim (spec 5/V §4): the own slot is always allowed; another
		// prefix only if unclaimed by another folder.
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
		// Containment keys on the EMPTY folder claim: root widens to any
		// folder's route; every NAMED folder, a top-level tenant included, is
		// bound to its own (keying on tier-0 let a tenant hijack a sibling's
		// route). The delete conditions on the rule being there AND being this
		// folder's, so its refusal IS the not-found answer, reached from the batch.
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
// the OAuth-gated twin (spec 5/V §Agent web slots). One declaration for every
// reader in this file.
func pubSlot(folder string) string  { return "/pub/" + folder + "/" }
func privSlot(folder string) string { return "/priv/" + folder + "/" }

// shadowedFolders names the groups whose publish slot a route at pathPrefix
// would answer for. proxyd matches a raw string prefix, longest wins
// (proxyd.matchWebRoute), so `/pub/a` covers `atlas` — the test is proxyd's
// own, not a segment walk. An exact claim on the caller's own slot is not
// shadowing; anything ABOVE it is. A nil group read is refused rather than read
// as "this instance has no groups".
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

// webRoutesPostBuild mounts the web_route tools on the agent socket with the
// db.Authorize Gate + the turn's visibility view.
func (s *Server) webRoutesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.webRoutesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := webRoutesMCPNames[x.Action]
		// The tools act on the caller's OWN folder; one evaluator.
		if !authorize(folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// argString reads a string arg from a resreg.Args map; absent is "".
func argString(args resreg.Args, key string) string {
	s, _ := args[key].(string)
	return s
}

// putWebRouteInto buffers one web_routes upsert on resreg's write set. It is
// ONE Merge, which is what makes it buffer-able: a create-then-update pair
// names one item twice, which no batch may do (objstore.ValidateBatch).
// `created_at` is an Init, so the first claim's instant survives every upsert,
// and it is core.TS at 30 bytes — every writer of these items must agree byte
// for byte, because a comparison over TEXT answers by byte order (BUGS L97).
func (d *DB) putWebRouteInto(ws *store.WriteSet, r WebRouteRow) error {
	key, err := store.WebRouteKey(r.PathPrefix)
	if err != nil {
		return err
	}
	owned := map[string]string{store.AttrWebAccess: r.Access, store.AttrWebFolder: r.Folder}
	// Absent is the only null: a rule that redirects nowhere carries no
	// redirect_to, and a merge that left it alone would keep a stale target.
	// REMOVED, not emptied.
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

// deleteWebRouteInto buffers one web_routes removal; refused is "no such rule
// of yours". A non-empty `folder` must match the item's own, widening ONLY when
// empty, so containment lives entirely in how the caller resolves its folder —
// the agent socket's own group, or the REST target bounded to the JWT subtree.
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
