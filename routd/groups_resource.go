package routd

// groups_resource.go folds the agent's group tools (register_group + refresh_groups)
// onto ONE resreg.Resource (spec 5/16).
//
// register_group's side-effects (a group DB row, a room route, a git-init'd group dir,
// all via the existing s.registerGroup) CANNOT ride a resreg SQL tx, so this resource
// is a FORWARDER (Store nil): resreg opens no tx and writes no audit_log row, and the
// handler emits the register_group system-audit event itself.
//
// Because resreg SKIPS the injected Gate for forwarders, ALL of register_group's auth
// rides Authz: db.Authorize for mcp:register_group scoped to cover the CHILD folder,
// plus the one non-scope residue that worlds are CLI-only. Holding the grant IS the
// authority to create.
//
// Only the AGENT face rides this resource; the operator group forms are dashd's.

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

// groupsActionRegister is register_group's resource-specific verb (not CRUD create):
// its FS side-effects ride a forwarder, not the engine's create tx. list reuses
// resreg.ActionList (refresh_groups, read-only).
const groupsActionRegister = resreg.Action("register")

// groupsResource is the single renderer for the agent's two group tools. FORWARDER
// (Store nil): register_group's dir/route side-effects can't ride a resreg tx, so
// resreg opens none and the handler owns the DB row (s.registerGroup) + the audit
// emit. authz is the per-turn gate closure (the grant check on the child folder)
// built in groupsPostBuild — for a forwarder invoke runs Authz, never Gate.
func (s *Server) groupsResource(authz func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error)) resreg.Resource {
	r := resreg.Mount(resources.GroupsName)
	r.Authz = authz
	r.Handler = s.groupsHandler
	return r
}

// groupsHandler runs register/list against routd.db. register is the manual
// register_group path (containment already ran in Authz): the group row, room route
// and git-init via s.registerGroup, then the audit emit. list is refresh_groups: every
// registered group's folder, unscoped — the mcp:refresh_groups grant is the only
// limit, applied at visibility.
func (s *Server) groupsHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		groups := s.db.AllGroups()
		out := make([]resources.GroupsFolderRef, 0, len(groups))
		for _, g := range groups {
			// The operator REST face (/v1/groups) scopes to the caller's subtree —
			// an unscoped list-all would leak every tenant's folders to any operator
			// (the rest_listall class). The agent MCP face (refresh_groups) stays
			// unscoped: an agent needs the whole tree to discover delegation targets.
			if x.Surface == audit.SurfaceREST && !ownsFolder(x.Caller.Folder, g.Folder) {
				continue
			}
			out = append(out, resources.GroupsFolderRef{Folder: g.Folder})
		}
		return out, nil

	case groupsActionRegister:
		jid := argString(x.Args, "jid")
		gfld := argString(x.Args, "folder")
		if gfld == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "folder required")
		}
		// Every other create path validates (the CLI, dashd, invites, archive import);
		// this one did not, so it was the single way to get an unvalidated folder into
		// `groups`. registerGroup then git-inits a Join'd path, and Join resolves `..`,
		// so an agent whose scope admits the target could seed a group dir outside the
		// base (BUGS L78).
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

// emitGroupRegisterAudit writes the register_group system-audit event through routd's
// Audit sink — the forwarder equivalent of the deleted emitSys (resreg writes no
// audit_log row for a forwarder). Folder is the CHILD folder (the audit target),
// actor the caller's socket principal. A nil s.audit (tests without SetAudit) is a
// no-op.
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

// groupsPostBuild returns the ServeMCP seam that mounts register_group +
// refresh_groups on the agent socket. Forwarder auth rides Authz: register_group runs
// db.Authorize scoped to cover the child folder; refresh_groups carries no runtime
// authz. Both are gated at VISIBILITY by the same held-grant view, so the dashd tool
// browser mirrors the socket exactly.
func (s *Server) groupsPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	authz := func(_ resreg.Caller, a resreg.Action, args resreg.Args) (string, map[string]string, error) {
		if a != groupsActionRegister {
			return "", nil, nil // refresh_groups: read, no runtime authz (visibility-gated only)
		}
		// An empty folder falls through to the handler's "folder required".
		gfld := argString(args, "folder")
		if gfld == "" {
			return "", nil, nil
		}
		// Worlds are CLI-only — register_group's one NON-scope residue (a root caller
		// may not mint a top-level world from the agent socket).
		if callerID.IsRoot && !strings.Contains(gfld, "/") {
			return "", nil, resreg.Errorf(http.StatusForbidden, "worlds are CLI-only")
		}
		// 5/33: one evaluator — does the caller hold register_group scoped to cover the
		// child folder? A delegated row scoped `acme/**` authorizes registering under acme.
		if !authorize(gfld, "mcp:register_group", nil) {
			return "", nil, resreg.Errorf(http.StatusForbidden, "register_group on %s: not permitted", gfld)
		}
		return "", nil, nil
	}
	res := s.groupsResource(authz)
	return mountAgentResource(res, callerSub, folder, visible)
}
