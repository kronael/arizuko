package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// GitHubSourcesRow mirrors github_sources MINUS the sealed secret column: the raw
// secret is shown once at registration and the engine never selects the sealed
// value. Apply never rebuilds this table (spec 6/27).
type GitHubSourcesRow struct {
	Source      string `db:"source"       yaml:"source"       json:"source"`
	OwnerFolder string `db:"owner_folder" yaml:"owner_folder" json:"owner_folder"`
	CreatedAt   string `db:"created_at"   yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// GitHubSourcesResponses: list answers the row; register answers the webhook
// URL and its signing secret, neither a column, so it declares nothing.
var GitHubSourcesResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]GitHubSourcesRow](),
}

// GitHubSourcesEndpoints is the single owner of the endpoint set for BOTH the
// agent's tools and the operator REST face.
var GitHubSourcesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/github_sources", Action: resreg.Action("register"), Status: 201},
	{Verb: "GET", Path: "/v1/github_sources", Action: resreg.ActionList},
	{Verb: "DELETE", Path: "/v1/github_sources/{source...}", Action: resreg.Action("revoke")},
}

var GitHubSourcesMCPNames = map[resreg.Action]string{
	resreg.Action("register"): "register_github_source",
	resreg.ActionList:         "list_github_sources",
	resreg.Action("revoke"):   "revoke_github_source",
}

var GitHubSourcesMCPDoc = map[resreg.Action]string{
	resreg.Action("register"): "Register a GitHub webhook source — a repo (kind=repo, name=<owner>/<repo>), " +
		"an organization (kind=org, name=<owner>) or a GitHub App (kind=app, name=<slug>) — " +
		"and route its events to target_folder. Returns {source, url, secret} once: paste " +
		"url + secret into GitHub's webhook settings (content type JSON). Events land on " +
		"github:<owner>/<repo>[/pr/<n>|/issue/<n>] JIDs; comments and new PRs/issues trigger " +
		"a turn, everything else is observed. Edit the seeded `routes` rows to change that. Spec 6/27.",
	resreg.ActionList: "List the GitHub webhook sources owned by your folder. Returns " +
		"{source, owner_folder, created_at}; secrets are never returned. Spec 6/27.",
	resreg.Action("revoke"): "Revoke a GitHub webhook source by its key (repo/<owner>/<repo>, " +
		"org/<owner>, app/<slug>). Deliveries to its URL 404 immediately and the seeded route " +
		"rows are removed. Caller must own the source. Spec 6/27.",
}

var GitHubSourcesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("register"): {
		{Name: "kind", Type: "string", Required: true, Description: "repo | org | app"},
		{Name: "name", Type: "string", Required: true,
			Description: "<owner>/<repo> for repo, <owner> for org, the App slug for app"},
		{Name: "target_folder", Type: "string",
			Description: "Folder the seeded routes deliver to. Defaults to your own folder; you may only target a folder your acl rows already cover."},
	},
	resreg.Action("revoke"): {
		{Name: "source", Type: "string", Required: true,
			Description: "Source key to revoke (e.g. repo/kronael/arizuko, org/kronael, app/arizuko-krons)."},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:             GitHubSourcesName,
		Table:            "github_sources",
		DB:               resreg.SubsystemRoutd,
		RowType:          reflect.TypeFor[GitHubSourcesRow](),
		PKFields:         []string{"Source"},
		EntitySortKey:    []string{"Source"},
		Endpoints:        GitHubSourcesEndpoints,
		Responses:        GitHubSourcesResponses,
		MCPDoc:           GitHubSourcesMCPDoc,
		MCPArgs:          GitHubSourcesMCPArgs,
		MCPNames:         GitHubSourcesMCPNames,
		StampedFields:    []string{"CreatedAt"},
		SkipApplyRebuild: true,
	})
}
