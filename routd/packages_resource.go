package routd

// packages_resource.go registers the installed-package record (spec 5/28) as a
// resreg.Resource, closing the review-blocker root CLAUDE.md names: every
// cold-tier management entity is a resreg resource, so one handler wears the
// operator's REST face and the agent's derived MCP face.
//
// THE SURFACE IS READ-ONLY, and that is the design, not a stub. Install /
// upgrade / remove is a phased, side-effecting lifecycle — git-clone at a pinned
// revision, compose fragments and skills written onto the HOST filesystem, acl +
// proxyd_routes rows applied, then `arizuko generate` and a compose restart
// (cmd/arizuko/packages.go). A write face here would have to reimplement that
// pipeline (a second path that drifts from the CLI — forbidden) or write a
// record for assets nobody installed. So `installed_packages` publishes exactly
// what it can serve honestly: list + get, on both faces. The reason F1 was filed
// was that an agent could not SEE what an operator installed; that is what this
// fixes. What it does not do, it does not advertise.
//
// CONTAINMENT: the record is keyed (folder, name) since 0031, but that key is
// the LOCK's — it is not a per-tenant slice, and authorization did not move with
// it. Every package the CLI installs is instance-wide (folder ""), `list` reads
// across all folders, and the record carries cross-folder data regardless (the
// acl grants an install applied, every public route path it opened). Both faces
// therefore still bind the same target, the whole tree
// (installedPackagesScope), and run the ONE evaluator on it: a caller whose
// grant covers only its own subtree does not match `**` and is denied. Two
// identity sources (JWT sub vs socket principal), two injected gates, one
// decision procedure — CLAUDE.md "auth is a uniform middleware". Narrowing this
// to a per-folder read is composition's call (spec 5/28), not the re-key's.
//
// No action mutates, so resreg opens no tx and writes no audit_log mutation row;
// denials and errors still land there (resreg.emitAudit), which is the whole
// forensic value a read surface has.

import (
	"context"
	"net/http"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// installedPackagesScope is the target BOTH faces authorize against. The record
// is instance-global, so the only scope that contains it is the whole tree —
// the same "**" the operator role holds (DB.IsOperator). A folder-scoped grant
// (`acme/**`) does not glob-match it, which is the containment.
const installedPackagesScope = "**"

// installedPackagesMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources. Aliased here for the Gate's action→policy-name lookup.
var installedPackagesMCPNames = resources.InstalledPackagesMCPNames

// installedPackagesResource is the single renderer for both faces. Endpoints,
// MCPDoc and MCPNames all come from resreg/resources so the catalog decl (which
// feeds /openapi.json and ipc's tool browser) and this mounted decl cannot
// disagree about the action set. Store is non-nil so resreg runs the Gate at all
// — a nil Store marks a forwarder, and invoke skips authorization for those.
func (s *Server) installedPackagesResource() resreg.Resource {
	return resreg.Resource{
		Name:      resources.InstalledPackagesName,
		Endpoints: resources.InstalledPackagesEndpoints,
		MCPDoc:    resources.InstalledPackagesMCPDoc,
		MCPArgs:   resources.InstalledPackagesMCPArgs,
		MCPNames:  installedPackagesMCPNames,
		// Containment is the injected Gate's, run against installedPackagesScope;
		// there is no per-call scope to derive from args.
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.installedPackagesHandler,
		Store:   store.New(s.db.SQL()),
	}
}

// mountInstalledPackages wires GET /v1/installed_packages[/{name}] onto the same
// handler the agent's list_packages/get_package tools use.
func (s *Server) mountInstalledPackages(mux *http.ServeMux) {
	res := s.installedPackagesResource()
	res.Gate = s.installedPackagesRESTGate
	resreg.RegisterREST(mux, res, s.installedPackagesRESTCaller)
}

// installedPackagesRESTCaller builds the REST Caller: identity via the Verifier,
// Caller.Folder = the JWT folder (the audit row's target). A nil Verifier is open
// (local-dev), mirroring s.authz and every sibling REST caller.
func (s *Server) installedPackagesRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub, folder string
	if s.verify != nil {
		var err error
		sub, _, folder, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{Sub: sub, Folder: folder}, nil
}

// installedPackagesRESTGate is the operator/human twin of the agent Gate below:
// the same evaluator, on the same instance-wide target, keyed by the REST action
// name. Only a grant covering `**` (the operator role, or an explicit
// installed_packages:<action> row at that scope) passes; a tenant token bound to
// its own subtree does not.
func (s *Server) installedPackagesRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil // local-dev open mode, mirrors s.authz
	}
	action := "installed_packages:" + string(x.Action)
	if !s.db.Authorize(x.Caller.Sub, installedPackagesScope, action, nil) {
		return resreg.Errorf(http.StatusForbidden,
			"%s reads instance-wide package state: not permitted (needs a grant scoped **)", action)
	}
	return nil
}

// installedPackagesPostBuild mounts the package tools on the agent socket with
// the db.Authorize Gate + the turn's visibility view injected. The target is the
// whole tree, so the record is default-deny for a folder agent: only /root or an
// operator grant scoped `**` reaches it.
func (s *Server) installedPackagesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.installedPackagesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := installedPackagesMCPNames[x.Action]
		if !authorize(callerSub, installedPackagesScope, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden,
				"%s reads instance-wide package state: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// installedPackagesHandler serves list/get off routd.db's installed-package
// record. Both actions render through installedPackageRow, so the JSON an agent
// and an operator receive IS the struct /openapi.json documents.
func (s *Server) installedPackagesHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		recs, err := s.db.InstalledPackages()
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]resources.InstalledPackagesRow, 0, len(recs))
		for _, rec := range recs {
			out = append(out, installedPackageRow(rec))
		}
		return out, nil

	case resreg.ActionGet:
		name := strings.TrimSpace(argString(x.Args, "name"))
		if name == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "name required")
		}
		// The record is keyed (folder, name); an omitted folder asks for the
		// instance-wide row, which is every row the CLI writes.
		folder := strings.TrimSpace(argString(x.Args, "folder"))
		rec, ok, err := s.db.InstalledPackage(folder, name)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !ok {
			if folder == InstanceWide {
				return nil, resreg.Errorf(http.StatusNotFound,
					"no package %q installed instance-wide", name)
			}
			return nil, resreg.Errorf(http.StatusNotFound,
				"no package %q installed at folder %q", name, folder)
		}
		return installedPackageRow(rec), nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// installedPackageRow maps the store DTO onto the catalog Row — the one struct
// /openapi.json emits a schema from — so the wire body and the doc cannot drift.
// The raw JSON shadow columns stay empty: they exist for the engine's ScanAll,
// and both are json:"-".
func installedPackageRow(p InstalledPackage) resources.InstalledPackagesRow {
	return resources.InstalledPackagesRow{
		Folder:      p.Folder,
		Name:        p.Name,
		Source:      p.Source,
		Revision:    p.Revision,
		InstalledAt: p.InstalledAt,
		Manifest:    p.Manifest,
		AssetHashes: p.AssetHashes,
	}
}
