package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// InstalledPackagesRow mirrors installed_packages (spec 5/28-packages). manifest +
// asset_hashes are Go maps, so each DECLARES its encoding (resreg/codec.go).
// The PK is composite (folder, name): composition blends a product mix per
// GROUP, so the lock names which group (BUGS F30). Folder "" is instance-wide.
type InstalledPackagesRow struct {
	Folder      string              `db:"folder"       yaml:"folder"       json:"folder"`
	Name        string              `db:"name"         yaml:"name"         json:"name"`
	Source      string              `db:"source"       yaml:"source"       json:"source"`
	Revision    string              `db:"revision"     yaml:"revision"     json:"revision"`
	InstalledAt string              `db:"installed_at" yaml:"installed_at" json:"installed_at"`
	Manifest    map[string][]string `db:"manifest,json"     yaml:"manifest,omitempty"     json:"manifest,omitempty"`
	AssetHashes map[string]string   `db:"asset_hashes,json" yaml:"asset_hashes,omitempty" json:"asset_hashes,omitempty"`
}

// InstalledPackagesResponses: both reads answer the row itself.
var InstalledPackagesResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]InstalledPackagesRow](),
	resreg.ActionGet:  reflect.TypeFor[InstalledPackagesRow](),
}

// InstalledPackagesEndpoints is READ-ONLY BY DESIGN. Installing a package is
// not a row write: the CLI writes files and rows and only then records what
// it owns here, so a create would forge a record for assets nobody installed,
// and a delete would orphan the routes, grants and files `remove` reads this
// record to reverse.
var InstalledPackagesEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/installed_packages", Action: resreg.ActionList, Status: http.StatusOK},
	{Verb: "GET", Path: "/v1/installed_packages/{name}", Action: resreg.ActionGet, Status: http.StatusOK},
}

// InstalledPackagesMCPNames maps each action to the flat agent tool name.
var InstalledPackagesMCPNames = map[resreg.Action]string{
	resreg.ActionList: "list_packages",
	resreg.ActionGet:  "get_package",
}

// InstalledPackagesMCPArgs is the explicit per-action arg list; the mounted
// decl has no RowType to reflect, and without this get_package would reject
// every call.
var InstalledPackagesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionGet: {
		{Name: "name", Type: "string", Required: true,
			Description: "Installed package name, as list_packages reports it (e.g. ttsd)."},
		// The PK is (folder, name); omitting folder asks for the instance-wide
		// row the CLI writes.
		{Name: "folder", Type: "string",
			Description: "Owning folder, as list_packages reports it. Omit for an " +
				"instance-wide package (the default, and every package the " +
				"`arizuko packages` CLI installs)."},
	},
}

// InstalledPackagesMCPDoc is the single owner of the package tools'
// agent-facing one-liners.
var InstalledPackagesMCPDoc = map[resreg.Action]string{
	resreg.ActionList: "List the packages installed on this instance: owning folder, name, " +
		"git source, resolved revision, install time, the identities each install owns " +
		"(compose fragments, proxyd routes, acl grants, skills) and its per-asset " +
		"content hashes. An empty `folder` means the package is installed " +
		"instance-wide. Reads across every folder, so it needs a grant covering the " +
		"whole tree. Read-only — installing, upgrading, and removing a package is the " +
		"`arizuko packages` CLI, because it also writes files and restarts " +
		"sidecars. Spec 5/28.",
	resreg.ActionGet: "Read one installed package's record — same fields as " +
		"list_packages, for a single package. Keyed by `name` plus the owning " +
		"`folder`; omit `folder` for an instance-wide package. Absent package " +
		"returns 404. Read-only. Spec 5/28.",
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          InstalledPackagesName,
		Table:         "installed_packages",
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[InstalledPackagesRow](),
		PKFields:      []string{"Folder", "Name"},
		EntitySortKey: []string{"Folder", "Name"},
		EntityKeySep:  store.KeySep,
		Endpoints:     InstalledPackagesEndpoints,
		Responses:     InstalledPackagesResponses,
		MCPDoc:        InstalledPackagesMCPDoc,
		MCPArgs:       InstalledPackagesMCPArgs,
		MCPNames:      InstalledPackagesMCPNames,
		// The record states what an install DID; rebuilding it from a manifest
		// would forge a record of an install that never ran.
		SkipApplyRebuild: true,
	})
}
