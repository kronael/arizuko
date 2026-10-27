package routd

// groups_resource.go folds the agent's group tools onto ONE resreg.Resource
// (spec 5/16). register_group's FS side-effects CANNOT ride a resreg tx, so
// this is a FORWARDER (Store nil): resreg writes no audit row and the handler
// emits the event itself. resreg SKIPS the Gate for forwarders, so ALL of
// register_group's auth rides Authz: db.Authorize scoped to cover the CHILD
// folder, plus the one non-scope residue that worlds are CLI-only.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// groupsActionRegister is register_group's verb (not CRUD create): its FS
// side-effects ride a forwarder, not the engine's create tx.
const groupsActionRegister = resreg.Action("register")

// groupsResource is the single renderer for group reads and registration. FORWARDER
// (Store nil); for a forwarder invoke runs Authz, never Gate.
func (s *Server) groupsResource(authz func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error)) resreg.Resource {
	r := resreg.Mount(resources.GroupsName)
	r.Authz = authz
	r.Handler = s.groupsHandler
	return r
}

// groupsHandler runs register/list/get. register: the row, room route and git-init
// via s.registerGroup, then the audit emit. list (refresh_groups): every
// group's folder, limited only by the mcp:refresh_groups grant at visibility.
func (s *Server) groupsHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		groups := s.db.AllGroups()
		out := make([]resources.GroupsFolderRef, 0, len(groups))
		for _, g := range groups {
			// The REST face scopes to the caller's subtree (the rest_listall leak
			// class); the agent face stays unscoped: an agent needs the whole
			// tree to discover delegation targets.
			if x.Surface == audit.SurfaceREST && !ownsFolder(x.Caller.Folder, g.Folder) {
				continue
			}
			out = append(out, resources.GroupsFolderRef{Folder: g.Folder})
		}
		return out, nil

	case resreg.ActionGet:
		folder := x.Args.Str("folder")
		group, ok := s.db.store().GroupByFolder(folder)
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "no group %s", folder)
		}
		return group, nil

	case groupsActionRegister:
		jid := argString(x.Args, "jid")
		gfld := argString(x.Args, "folder")
		if gfld == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "folder required")
		}
		// Every other create path validates. registerGroup git-inits a Join'd
		// path, and Join resolves `..`, so an unvalidated folder could seed a
		// group dir outside the base (BUGS L78).
		if !groupfolder.IsValidFolder(gfld) {
			return nil, resreg.Errorf(http.StatusBadRequest, "invalid folder %q", gfld)
		}
		gr := core.Group{Folder: gfld, AddedAt: time.Now()}
		if err := s.registerGroup(jid, gr); err != nil {
			s.emitGroupRegisterAudit(x.Caller, gfld, jid, err)
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		s.emitGroupRegisterAudit(x.Caller, gfld, jid, nil)
		slog.Info("group registered", "jid", jid, "folder", gfld, "sourceGroup", x.Caller.Folder)
		return map[string]any{"registered": true, "folder": gfld, "jid": jid}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// emitGroupRegisterAudit writes the register_group system-audit event (resreg
// writes none for a forwarder). Folder is the CHILD folder, actor the caller's
// socket principal. A nil s.audit is a no-op.
func (s *Server) emitGroupRegisterAudit(c resreg.Caller, childFolder, jid string, err error) {
	if s.audit == nil {
		return
	}
	actor := c.Sub
	if actor == "" {
		actor = "agent:" + c.Folder
	}
	outcome := audit.Outcome{Status: "ok"}
	if err != nil {
		outcome = audit.Outcome{Status: "error", Detail: err.Error()}
	}
	s.audit.EmitSystem(audit.SystemEvent{
		ActorSub: actor,
		Tool:     "register_group",
		Folder:   childFolder,
		Params:   map[string]any{"jid": jid},
		Outcome:  outcome,
	})
}

// groupsPostBuild mounts the group tools on the agent socket.
// Forwarder auth rides Authz: registration and detail reads authorize their
// target folder; refresh_groups carries no runtime authz. Both are gated at
// VISIBILITY by the same held-grant view, so dashd's tool browser mirrors the
// socket exactly.
func (s *Server) groupsPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	authz := func(_ resreg.Caller, a resreg.Action, args resreg.Args) (string, map[string]string, error) {
		if a == resreg.ActionGet {
			target := args.Str("folder")
			if !groupfolder.IsValidFolder(target) {
				return "", nil, resreg.Errorf(http.StatusBadRequest, "invalid folder")
			}
			params := map[string]string{"folder": target}
			if !authorize(target, "mcp:"+resources.GroupsMCPNames[a], params) {
				return "", nil, resreg.Errorf(http.StatusForbidden, "forbidden")
			}
			return target, params, nil
		}
		if a != groupsActionRegister {
			return "", nil, nil // refresh_groups: read, no runtime authz (visibility-gated only)
		}
		// An empty folder falls through to the handler's "folder required".
		gfld := argString(args, "folder")
		if gfld == "" {
			return "", nil, nil
		}
		// Worlds are CLI-only — the one NON-scope residue.
		if callerID.IsRoot && !strings.Contains(gfld, "/") {
			return "", nil, resreg.Errorf(http.StatusForbidden, "worlds are CLI-only")
		}
		// One evaluator: a delegated row scoped `acme/**` authorizes registering
		// under acme.
		if !authorize(gfld, "mcp:register_group", nil) {
			return "", nil, resreg.Errorf(http.StatusForbidden, "register_group on %s: not permitted", gfld)
		}
		return "", nil, nil
	}
	res := s.groupsResource(authz)
	return mountAgentResource(res, callerSub, folder, visible)
}
