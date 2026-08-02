package routd

// groups_resource.go is the spec 5/16 step after route_tokens — the LAST agent-face
// fold and the trickiest: the agent's group tools (register_group + refresh_groups)
// ride ONE resreg.Resource instead of two hand-rolled ipc/ipc.go tool bodies.
//
// register_group's side-effects (a group DB row, a room route, a git-init'd group
// dir — all via the EXISTING s.registerGroup) CANNOT ride a resreg SQL tx, so this
// resource is a FORWARDER (Store nil), like secrets: resreg opens no tx and writes
// no audit_log row; the handler calls s.registerGroup and emits the register_group
// system-audit event itself via s.audit, exactly as the deleted ipc emitSys did.
//
// Because resreg SKIPS the injected Gate for forwarders (invoke runs Authz, never
// Gate), ALL of register_group's auth rides Authz: the 5/33 single evaluator —
// db.Authorize for mcp:register_group scoped to cover the CHILD folder — plus the
// one non-scope residue that worlds (top-level folders) are CLI-only. The spawn cap
// (auth.CheckSpawnAllowed) stays in the HANDLER, matching the deleted body's order:
// containment (Authz), then the max_children cap (handler), then the write.
//
// Only the AGENT face rides this resource. The operator group forms are dashd's
// FS-managed /dash/groups/* (container.SetupGroup) — a separate surface, untouched.
//
// fromPrototype note: routd's buildGatedFns never wired SpawnGroup/SetupGroup on the
// agent socket, so register_group has ONLY ever created the row + route + git-init
// (never a prototype clone or a skill-skeleton seed) since the split. The fold
// preserves that EXACTLY — fromPrototype=true returns the same "not configured"
// error, and no dir-seed runs. Wiring the prototype spawn is a feature, out of a
// fold's scope.

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
	return resreg.Resource{
		Name:      "groups",
		Endpoints: resources.GroupsAgentEndpoints, // single source: doc + MCP read one list
		MCPDoc:    resources.GroupsMCPDoc,         // single source (resreg/resources)
		MCPArgs:   resources.GroupsMCPArgs,
		MCPNames:  resources.GroupsMCPNames,
		Authz:     authz,
		Handler:   s.groupsHandler,
	}
}

// groupsHandler runs register/list against routd.db. register is the manual
// register_group path (containment already ran in Authz): the spawn cap, then the
// group row + room route + git-init via s.registerGroup, then the register_group
// audit emit. list is refresh_groups: every registered group's folder — unscoped,
// matching the deleted body (the mcp:refresh_groups grant is the only limit,
// applied at visibility, never here).
func (s *Server) groupsHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		groups := s.db.AllGroups()
		out := make([]struct {
			Folder string `json:"folder"`
		}, 0, len(groups))
		for _, g := range groups {
			// The operator REST face (/v1/groups) scopes to the caller's subtree —
			// an unscoped list-all would leak every tenant's folders to any operator
			// (the rest_listall class). The agent MCP face (refresh_groups) stays
			// unscoped: an agent needs the whole tree to discover delegation targets.
			if x.Surface == audit.SurfaceREST && !ownsFolder(x.Caller.Folder, g.Folder) {
				continue
			}
			out = append(out, struct {
				Folder string `json:"folder"`
			}{Folder: g.Folder})
		}
		return out, nil

	case groupsActionRegister:
		jid := argString(x.Args, "jid")
		if argBool(x.Args, "fromPrototype") {
			// routd never wired the prototype spawn on the agent socket (see header):
			// preserve the pre-fold "not configured" behavior verbatim.
			return nil, resreg.Errorf(http.StatusBadRequest, "register_group: fromPrototype not configured")
		}
		gfld := argString(x.Args, "folder")
		if gfld == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "folder required when fromPrototype is false")
		}
		// Spawn cap (max_children): after Authz's containment, before the write —
		// matching the deleted ipc order. No audit on cap denial (ditto).
		groups := s.db.AllGroups()
		if pg, ok := groups[x.Caller.Folder]; ok {
			if err := auth.CheckSpawnAllowed(pg, groups); err != nil {
				return nil, resreg.Errorf(http.StatusForbidden, "%v", err)
			}
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
		Params:   map[string]any{"jid": jid, "fromPrototype": false},
		Outcome:  outcome,
	})
}

// groupsPostBuild returns the ServeMCP seam that mounts register_group + refresh_groups
// on the agent socket. Forwarder auth rides Authz (invoke skips Gate): register_group
// runs db.Authorize for mcp:register_group scoped to cover the child folder;
// refresh_groups (list) carries no runtime authz, matching the deleted direct-AddTool
// body. Both tools are gated at VISIBILITY by the same held-grant view, so the dashd
// tool browser — which reads the acl rows, not a folder — mirrors the socket exactly.
func (s *Server) groupsPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	authz := func(_ resreg.Caller, a resreg.Action, args resreg.Args) (string, map[string]string, error) {
		if a != groupsActionRegister {
			return "", nil, nil // refresh_groups: read, no runtime authz (visibility-gated only)
		}
		// The prototype path derives the child folder (no arg target); an empty folder
		// falls through to the handler's "folder required".
		if argBool(args, "fromPrototype") {
			return "", nil, nil
		}
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
		if !authorize(callerSub, gfld, "mcp:register_group", nil) {
			return "", nil, resreg.Errorf(http.StatusForbidden, "register_group on %s: not permitted", gfld)
		}
		return "", nil, nil
	}
	res := s.groupsResource(authz)
	return mountAgentResource(res, callerSub, folder, visible)
}

// argBool reads a bool arg from a resreg.Args map (MCP bool args decode to a Go bool
// via decodeMCPArgs; an absent/other-typed arg yields false).
func argBool(args resreg.Args, key string) bool {
	b, _ := args[key].(bool)
	return b
}
