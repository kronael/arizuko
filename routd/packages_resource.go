package routd

// packages_resource.go registers the installed-package record (spec 5/28) as
// a resreg.Resource. READ-ONLY by design: install/upgrade/remove is a
// side-effecting lifecycle on the HOST (cmd/arizuko/packages.go), and a write
// face here would be a second path that drifts from the CLI. The record is
// keyed (folder, name), but that key is the LOCK's, not a per-tenant slice —
// it carries cross-folder data — so both faces bind the whole tree
// (installedPackagesScope): a grant covering only a subtree does not match `**`.

import (
	"context"
	"net/http"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// installedPackagesScope is the target BOTH faces authorize against: the
// record is instance-global, so the only scope that contains it is the whole
// tree, and a folder-scoped grant (`acme/**`) does not glob-match it.
const installedPackagesScope = "**"

// installedPackagesMCPNames is single-sourced in resreg/resources.
var installedPackagesMCPNames = resources.InstalledPackagesMCPNames

// installedPackagesResource is the single renderer for both faces. Store is
// non-nil so resreg runs the Gate at all — a nil Store marks a forwarder, and
// invoke skips authorization for those.
func (s *Server) installedPackagesResource() resreg.Resource {
	r := resreg.Mount(resources.InstalledPackagesName)
	r.Handler = s.installedPackagesHandler
	r.Store = s.db.store()
	return r
}

// mountInstalledPackages wires GET /v1/installed_packages[/{name}] onto the
// same handler the agent's list_packages/get_package tools use.
func (s *Server) mountInstalledPackages(mux *http.ServeMux) {
	res := s.installedPackagesResource()
	res.Gate = s.installedPackagesRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// installedPackagesRESTGate is the REST twin of the agent Gate: the same
// evaluator on the same instance-wide target, keyed by the REST action name.
// A tenant token bound to its own subtree does not pass.
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

// installedPackagesPostBuild mounts the package tools on the agent socket. The
// target is the whole tree, so only /root or an operator grant scoped `**`
// reaches it.
func (s *Server) installedPackagesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.installedPackagesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := installedPackagesMCPNames[x.Action]
		if !authorize(installedPackagesScope, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden,
				"%s reads instance-wide package state: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// installedPackagesHandler serves list/get; both render through
// installedPackageRow, so the JSON IS the struct /openapi.json documents.
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
		// An omitted folder asks for the instance-wide row.
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
// /openapi.json emits a schema from — so the wire body and the doc cannot
// drift. The raw JSON shadow columns stay empty: they exist for the engine's
// ScanAll, and both are json:"-".
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
