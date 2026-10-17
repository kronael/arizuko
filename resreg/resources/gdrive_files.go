package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// GDriveFilesRow mirrors gdrive_files: one Drive file a folder binds. The item
// holds no secret, so every column is returned. Apply never rebuilds this table
// (specs/6/45-gdrive-comments-channel.md).
type GDriveFilesRow struct {
	FileID       string `db:"file_id"       yaml:"file_id"       json:"file_id"`
	OwnerFolder  string `db:"owner_folder"  yaml:"owner_folder"  json:"owner_folder"`
	TargetFolder string `db:"target_folder" yaml:"target_folder" json:"target_folder"`
	CreatedAt    string `db:"created_at"    yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// GDriveFilesResponses: list and register answer the row.
var GDriveFilesResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList:         reflect.TypeFor[[]GDriveFilesRow](),
	resreg.Action("register"): reflect.TypeFor[GDriveFilesRow](),
}

// GDriveFilesEndpoints is the single owner of the endpoint set for BOTH the
// agent's tools and the operator REST face. A file id holds no `/`, so the
// revoke path needs no wildcard segment.
var GDriveFilesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/gdrive_files", Action: resreg.Action("register"), Status: 201},
	{Verb: "GET", Path: "/v1/gdrive_files", Action: resreg.ActionList},
	{Verb: "DELETE", Path: "/v1/gdrive_files/{file_id}", Action: resreg.Action("revoke")},
}

var GDriveFilesMCPNames = map[resreg.Action]string{
	resreg.Action("register"): "register_gdrive_file",
	resreg.ActionList:         "list_gdrive_files",
	resreg.Action("revoke"):   "revoke_gdrive_file",
}

var GDriveFilesMCPDoc = map[resreg.Action]string{
	resreg.Action("register"): "Bind a Google Drive file and route its comments to target_folder. " +
		"Share the file with the service account first; the binding does not grant access. " +
		"Comment threads land on gdrive:<file_id>/c/<comment_id> JIDs; human comments and " +
		"mentions trigger a turn, everything else is observed. Edit the seeded `routes` rows " +
		"to change that. A file is bound to one folder per instance. Spec 6/45.",
	resreg.ActionList: "List the Drive files bound by your folder. Returns " +
		"{file_id, owner_folder, target_folder, created_at}. Spec 6/45.",
	resreg.Action("revoke"): "Unbind a Drive file by its id. The adapter stops polling it and " +
		"the seeded route rows are removed. Caller must own the binding. Spec 6/45.",
}

var GDriveFilesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("register"): {
		{Name: "file_id", Type: "string", Required: true,
			Description: "Drive file id: the segment of the document URL after /d/. Letters, digits, - and _ only."},
		{Name: "target_folder", Type: "string",
			Description: "Folder the seeded routes deliver to. Defaults to your own folder; you may only target a folder your acl rows already cover."},
	},
	resreg.Action("revoke"): {
		{Name: "file_id", Type: "string", Required: true,
			Description: "Id of the bound file to unbind."},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:             GDriveFilesName,
		Table:            "gdrive_files",
		DB:               resreg.SubsystemRoutd,
		RowType:          reflect.TypeFor[GDriveFilesRow](),
		PKFields:         []string{"FileID"},
		EntitySortKey:    []string{"FileID"},
		Endpoints:        GDriveFilesEndpoints,
		Responses:        GDriveFilesResponses,
		MCPDoc:           GDriveFilesMCPDoc,
		MCPArgs:          GDriveFilesMCPArgs,
		MCPNames:         GDriveFilesMCPNames,
		StampedFields:    []string{"CreatedAt"},
		SkipApplyRebuild: true,
	})
}
