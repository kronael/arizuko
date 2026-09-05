package routd

// github_sources_resource.go is the one renderer for gitd's delivery-source
// registry (spec 6/27): the agent's three tools and the operator REST face
// share this handler. Registering a source seeds route rows, so the REST gate
// is the routes gate; the owner_folder is ALWAYS the caller's folder and the
// arg-carried target_folder only picks where the seeded routes deliver.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

const (
	githubSourcesActionRegister = resreg.Action("register")
	githubSourcesActionRevoke   = resreg.Action("revoke")
)

var githubSourcesMCPNames = resources.GitHubSourcesMCPNames

func (s *Server) githubSourcesResource() resreg.Resource {
	r := resreg.Mount(resources.GitHubSourcesName)
	r.Handler = s.githubSourcesHandler
	r.Store = s.db.secretStore()
	return r
}

func (s *Server) githubSourcesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	folder := x.Caller.Folder
	st := s.db.secretStore()
	switch x.Action {
	case resreg.ActionList:
		rows, err := st.ListGitHubSources(folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]resources.GitHubSourcesRow, len(rows))
		for i, r := range rows {
			out[i] = resources.GitHubSourcesRow{Source: r.Source, OwnerFolder: r.OwnerFolder,
				CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")}
		}
		return out, nil

	case githubSourcesActionRegister:
		if folder == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "owner_folder required")
		}
		key, err := store.GitHubSourceKey(strings.TrimSpace(argString(x.Args, "kind")),
			strings.TrimSpace(argString(x.Args, "name")))
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		target := strings.TrimSpace(argString(x.Args, "target_folder"))
		if target == "" {
			target = folder
		}
		if x.Surface == audit.SurfaceREST && !groupfolder.Contains(folder, target) {
			return nil, resreg.Errorf(http.StatusForbidden, "target_folder must equal or descend from owner_folder")
		}
		secret, err := st.AddGitHubSourceTx(ctx, x.Tx, store.GitHubSource{Source: key, OwnerFolder: folder}, target)
		if errors.Is(err, store.ErrGitHubSourceExists) {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		if errors.Is(err, store.ErrNoSecretsKey) {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		url := ""
		if base := core.PublicBaseURL(s.webHost); base != "" {
			url = base + "/github/" + key
		}
		slog.Info("register_github_source", "folder", folder, "target", target, "source", key)
		return map[string]any{"source": key, "url": url, "secret": secret}, nil

	case githubSourcesActionRevoke:
		key := strings.TrimSpace(argString(x.Args, "source"))
		if _, _, err := store.ParseGitHubSourceKey(key); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		deleted, err := st.RemoveGitHubSourceTx(ctx, x.Tx, key, folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("revoke_github_source", "folder", folder, "source", key, "deleted", deleted)
		return map[string]any{"deleted": deleted}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// mountGitHubSources wires the operator REST face. Same gate and owner
// resolution as route tokens: a source is a routed ingress credential.
func (s *Server) mountGitHubSources(mux *http.ServeMux) {
	res := s.githubSourcesResource()
	res.Gate = s.routeTokensRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(routeTokensOwner))
}

// githubSourcesPostBuild mounts the tools on the agent socket. Registering is
// default-deny like issue_webhook: only a grant covering the target authorizes.
func (s *Server) githubSourcesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.githubSourcesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := githubSourcesMCPNames[x.Action]
		target := folder
		if x.Action == githubSourcesActionRegister {
			if t := strings.TrimSpace(argString(x.Args, "target_folder")); t != "" {
				target = t
			}
		}
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}
