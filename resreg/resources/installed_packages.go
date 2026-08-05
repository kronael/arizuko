package resources

import (
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// InstalledPackagesRow mirrors installed_packages (spec 5/28's installed-package
// record). manifest + asset_hashes are JSON TEXT columns exposed as native Go
// maps, so the engine keeps a raw shadow column per field and AfterScan decodes
// it — the same trade-off ProxydRoutesRow pays for its JSON blob.
type InstalledPackagesRow struct {
	Name           string              `db:"name"         yaml:"name"         json:"name"`
	Source         string              `db:"source"       yaml:"source"       json:"source"`
	Revision       string              `db:"revision"     yaml:"revision"     json:"revision"`
	InstalledAt    string              `db:"installed_at" yaml:"installed_at" json:"installed_at"`
	ManifestRaw    string              `db:"manifest"     yaml:"-"            json:"-"`
	AssetHashesRaw string              `db:"asset_hashes" yaml:"-"            json:"-"`
	Manifest       map[string][]string `db:"-"            yaml:"manifest,omitempty"     json:"manifest,omitempty"`
	AssetHashes    map[string]string   `db:"-"            yaml:"asset_hashes,omitempty" json:"asset_hashes,omitempty"`
}

// InstalledPackagesEndpoints is the single owner of the endpoint set that drives
// BOTH faces: routd's mounted handler (routd/packages_resource.go) and the
// /openapi.json doc. READ-ONLY BY DESIGN — there is no create/update/delete.
//
// Installing a package is not a row write: `arizuko packages install` resolves a
// git source to a revision, writes compose fragments and skills onto the host
// filesystem, applies acl + proxyd_routes rows, and only then records what it
// owns here (cmd/arizuko/packages.go). A REST/MCP create would either reimplement
// that pipeline — a second path that drifts from the CLI, which root CLAUDE.md
// forbids — or write a record for assets nobody installed. A delete is worse: the
// record is what `remove` reads to find the identities to reverse, so dropping it
// orphans the routes, grants, and files it named. The lifecycle stays the CLI's;
// what the resource adds is the read both an agent and an operator were missing.
var InstalledPackagesEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/installed_packages", Action: resreg.ActionList, Status: http.StatusOK},
	{Verb: "GET", Path: "/v1/installed_packages/{name}", Action: resreg.ActionGet, Status: http.StatusOK},
}

// InstalledPackagesMCPNames maps each action to the flat agent tool name, matching
// the flat convention every other agent-facing resource uses (list_tokens,
// add_route, network_allow) rather than the dotted default. routd's mounted
// resource aliases this map and ipc.ListTools reads it via the registry walk.
var InstalledPackagesMCPNames = map[resreg.Action]string{
	resreg.ActionList: "list_packages",
	resreg.ActionGet:  "get_package",
}

// InstalledPackagesMCPArgs is the explicit per-action arg list. It exists because
// a resource has TWO declarations — this catalog entry (RowType, drives
// /openapi.json) and routd's mounted handler decl (no RowType, drives the live
// tools) — and only the first can reflect its args. Declaring them once here is
// what keeps the two faces asking for the same thing; deriving from RowType would
// give the mounted tool no args at all, and get_package would reject every call.
var InstalledPackagesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionGet: {
		{Name: "name", Type: "string", Required: true,
			Description: "Installed package name, as list_packages reports it (e.g. ttsd)."},
	},
}

// InstalledPackagesMCPDoc is the single owner of the package tools' agent-facing
// one-liners; openapi.go folds the same strings in as x-mcp-when. Both say
// read-only out loud, because the tools an agent can see are the whole surface it
// gets to reason about.
var InstalledPackagesMCPDoc = map[resreg.Action]string{
	resreg.ActionList: "List the packages installed on this instance: name, git source, " +
		"resolved revision, install time, the identities each install owns " +
		"(compose fragments, proxyd routes, acl grants, skills) and its per-asset " +
		"content hashes. Instance-wide, so it needs a grant covering the whole " +
		"tree. Read-only — installing, upgrading, and removing a package is the " +
		"`arizuko packages` CLI, because it also writes files and restarts " +
		"sidecars. Spec 5/28.",
	resreg.ActionGet: "Read one installed package's record by name — same fields as " +
		"list_packages, for a single package. Absent package returns 404. " +
		"Read-only. Spec 5/28.",
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "installed_packages",
		Table:     "installed_packages",
		DB:        resreg.SubsystemRoutd,
		RowType:   reflect.TypeFor[InstalledPackagesRow](),
		PKFields:  []string{"Name"},
		Endpoints: InstalledPackagesEndpoints,
		MCPDoc:    InstalledPackagesMCPDoc,
		MCPArgs:   InstalledPackagesMCPArgs,
		MCPNames:  InstalledPackagesMCPNames,
		// The record states what an install DID — a revision resolved from a git
		// HEAD, hashes of files actually written. Rebuilding it from a manifest
		// would forge a record of an install that never ran, and `remove` then
		// reverses identities off that forgery. Export reads it; Apply never
		// rewrites it (mirrors secrets + route_tokens).
		SkipApplyRebuild: true,
		Hooks: resreg.Hooks{
			AfterScan: func(row any) error {
				r := row.(*InstalledPackagesRow)
				if err := json.Unmarshal([]byte(r.ManifestRaw), &r.Manifest); err != nil {
					return err
				}
				return json.Unmarshal([]byte(r.AssetHashesRaw), &r.AssetHashes)
			},
		},
	})
}
