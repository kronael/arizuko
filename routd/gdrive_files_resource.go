package routd

// gdrive_files_resource.go is the one renderer for googd's file bindings
// (specs/6/45-gdrive-comments-channel.md): the agent's three tools and the
// operator REST face share this handler. Registering a binding seeds route
// rows, so the REST gate is the routes gate; owner_folder is ALWAYS the
// caller's folder and the arg-carried target_folder only picks where the seeded
// routes deliver. The binding holds no secret, so the store opens without a
// keyring.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

const (
	gdriveFilesActionRegister = resreg.Action("register")
	gdriveFilesActionRevoke   = resreg.Action("revoke")
)

var gdriveFilesMCPNames = resources.GDriveFilesMCPNames

func (s *Server) gdriveFilesResource() resreg.Resource {
	r := resreg.Mount(resources.GDriveFilesName)
	r.Handler = s.gdriveFilesHandler
	r.Store = s.db.store()
	return r
}

func gdriveFilesRow(f store.GDriveFile) resources.GDriveFilesRow {
	return resources.GDriveFilesRow{FileID: f.FileID, OwnerFolder: f.OwnerFolder,
		TargetFolder: f.TargetFolder, CreatedAt: f.CreatedAt.UTC().Format(time.RFC3339)}
}

func (s *Server) gdriveFilesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	folder := x.Caller.Folder
	st := s.db.store()
	switch x.Action {
	case resreg.ActionList:
		rows, err := st.ListGDriveFiles(folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]resources.GDriveFilesRow, len(rows))
		for i, f := range rows {
			out[i] = gdriveFilesRow(f)
		}
		return out, nil

	case gdriveFilesActionRegister:
		if folder == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "owner_folder required")
		}
		fileID := strings.TrimSpace(argString(x.Args, "file_id"))
		if err := store.CheckGDriveFileID(fileID); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		target := strings.TrimSpace(argString(x.Args, "target_folder"))
		if target == "" {
			target = folder
		}
		if !groupfolder.IsValidFolder(target) {
			return nil, resreg.Errorf(http.StatusBadRequest, "invalid target_folder %q", target)
		}
		if x.Surface == audit.SurfaceREST && !groupfolder.Contains(folder, target) {
			return nil, resreg.Errorf(http.StatusForbidden, "target_folder must equal or descend from owner_folder")
		}
		taken := resreg.Errorf(http.StatusConflict, "%v: %s", store.ErrGDriveFileExists, fileID)
		bound := store.GDriveFile{FileID: fileID, OwnerFolder: folder, TargetFolder: target, CreatedAt: time.Now()}
		if err := st.AddGDriveFileInto(ctx, x.Writes, bound, taken); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		slog.Info("register_gdrive_file", "folder", folder, "target", target, "file_id", fileID)
		return gdriveFilesRow(bound), nil

	case gdriveFilesActionRevoke:
		fileID := strings.TrimSpace(argString(x.Args, "file_id"))
		if err := store.CheckGDriveFileID(fileID); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		deleted, err := st.RemoveGDriveFileInto(ctx, x.Writes, fileID, folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("revoke_gdrive_file", "folder", folder, "file_id", fileID, "deleted", deleted)
		return map[string]any{"deleted": deleted}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// mountGDriveFiles wires the operator REST face. Same gate and owner
// resolution as route tokens: a binding is a routed ingress.
func (s *Server) mountGDriveFiles(mux *http.ServeMux) {
	res := s.gdriveFilesResource()
	res.Gate = s.routeTokensRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(routeTokensOwner))
}

// gdriveFilesPostBuild mounts the tools on the agent socket. Every action is
// authorized against the turn gate: register on the target folder, the rest on
// the caller's own.
func (s *Server) gdriveFilesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.gdriveFilesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := gdriveFilesMCPNames[x.Action]
		target := folder
		if x.Action == gdriveFilesActionRegister {
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
