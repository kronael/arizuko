package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// RouteTokensRow mirrors route_tokens MINUS the token_hash BLOB PK: the raw
// token is shown once at issue time and only sha256(token) is persisted, so it
// never appears in a read surface (spec 5/W). The exported columns are the same
// safe {jid, owner_folder, created_at} shape list_tokens returns. Apply never
// rebuilds this table (SkipApplyRebuild) — tokens are runtime-minted, not
// manifest state — so the missing PK column is moot for the engine.
type RouteTokensRow struct {
	JID         string `db:"jid"          yaml:"jid"          json:"jid"`
	OwnerFolder string `db:"owner_folder" yaml:"owner_folder" json:"owner_folder"`
	CreatedAt   string `db:"created_at"   yaml:"created_at,omitempty" json:"created_at,omitempty"`
	Context     string `db:"context"      yaml:"context,omitempty"    json:"context,omitempty"`
}

// RouteTokensEndpoints is the single owner of the route_tokens endpoint set that
// drives BOTH the agent's token tools (routd route_tokens_resource.go) AND the
// operator REST face (routd tokens_http.go mountRouteTokens, spec 5/16 fold).
// issue_chat/issue_hook are custom POST verbs at /chat + /hook and revoke is a
// jid-addressed DELETE, so the real faces diverge from the PK-CRUD convention.
// The REST-only resolve (URL token → jid, webd) has no MCP twin and stays
// hand-rolled. Both faces now share routeTokensHandler, so /openapi.json
// advertises these paths.
var RouteTokensEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/route_tokens/chat", Action: resreg.Action("issue_chat"), Status: 201},
	{Verb: "POST", Path: "/v1/route_tokens/hook", Action: resreg.Action("issue_hook"), Status: 201},
	{Verb: "GET", Path: "/v1/route_tokens", Action: resreg.ActionList},
	{Verb: "DELETE", Path: "/v1/route_tokens/{jid}", Action: resreg.Action("revoke")},
}

// RouteTokensMCPNames maps each action to the flat tool name the live agent
// already calls; routd's route_tokens_resource.go references it (agent socket
// derivation) and ipc.ListTools reads it via the registry walk. Spec 5/16.
var RouteTokensMCPNames = map[resreg.Action]string{
	resreg.Action("issue_chat"): "issue_chat_link",
	resreg.Action("issue_hook"): "issue_webhook",
	resreg.ActionList:           "list_tokens",
	resreg.Action("revoke"):     "revoke_token",
}

// RouteTokensMCPDoc is the single owner of the token tools' agent-facing
// one-liners. Copy verbatim — the agent wire contract.
var RouteTokensMCPDoc = map[resreg.Action]string{
	resreg.Action("issue_chat"): "Mint a route token that serves the anonymous web chat widget at " +
		"/chat/<token>/. Returns {token, url, jid}; the token is shown " +
		"once. Inbound messages append at jid=web:<target_folder>[/<jid_suffix>]. " +
		"Use when you want a public, password-less chat surface for the " +
		"folder — paste the URL into a website, share with a visitor. " +
		"target_folder defaults to your own folder. Spec 5/W.",
	resreg.Action("issue_hook"): "Mint a route token for an inbound webhook surface at /hook/<token>. " +
		"POSTs append a message at jid=hook:<target_folder>/<source_label>[/<jid_suffix>], " +
		"sender=<source_label>; the agent sees it like any other inbound. " +
		"Returns {token, url, jid} once. Use to register an external " +
		"system (GitHub, Linear, Stripe, …) as a fire-and-forget event " +
		"source for the folder. Spec 5/W.",
	resreg.ActionList: "List route tokens (chat links + webhooks) owned by your folder. " +
		"Returns rows with {jid, owner_folder, created_at, context}. Raw tokens " +
		"are NOT returned — they're shown once at issue time. Spec 5/W.",
	resreg.Action("revoke"): "Revoke a route token by JID. Caller must own the token " +
		"(owner_folder = your folder). After revocation the URL " +
		"returns 404 immediately — no grace period. Spec 5/W.",
}

// RouteTokensMCPArgs is the explicit per-action arg list — the agent face carries
// custom mint args ({target_folder, jid_suffix, source_label} / {jid}), NOT the
// RowType columns, so this overrides RowType reflection for the derived tools.
var RouteTokensMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("issue_chat"): {
		{Name: "target_folder", Type: "string",
			Description: "Folder the token routes to. Defaults to your own folder; you may only target a folder your acl rows already cover."},
		{Name: "jid_suffix", Type: "string",
			Description: "Optional path appended to the JID (web:<folder>/<suffix>) — useful to partition multiple chat surfaces under one folder."},
		{Name: "context", Type: "string",
			Description: "Optional processing instructions for this link's inbound (e.g. 'bug reports from the acme site; triage, don't chat'). Rendered to the handling agent as <link-context> on every message arriving through this token. Spec 5/W."},
	},
	resreg.Action("issue_hook"): {
		{Name: "source_label", Type: "string", Required: true,
			Description: "Short identifier of the upstream system (e.g. github, linear, stripe). Becomes the JID's source segment and the inbound sender field."},
		{Name: "target_folder", Type: "string",
			Description: "Folder the token routes to. Defaults to your own folder. Tier rules match issue_chat_link."},
		{Name: "jid_suffix", Type: "string",
			Description: "Optional path appended to the JID — partition multiple webhooks under one source_label."},
		{Name: "context", Type: "string",
			Description: "Optional processing instructions for this hook's inbound (e.g. 'Stripe events; summarize daily, no replies'). Rendered to the handling agent as <link-context> on every message arriving through this token. Spec 5/W."},
	},
	resreg.Action("revoke"): {
		{Name: "jid", Type: "string", Required: true,
			Description: "JID of the token to revoke (e.g. web:acme or hook:acme/github)."},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "route_tokens",
		Table:     "route_tokens",
		RowType:   reflect.TypeFor[RouteTokensRow](),
		PKFields:  []string{"JID"},
		Endpoints: RouteTokensEndpoints,
		MCPDoc:    RouteTokensMCPDoc,
		MCPArgs:   RouteTokensMCPArgs,
		MCPNames:  RouteTokensMCPNames,
		// token_hash is minted imperatively and never round-trips through a
		// manifest — Apply must never DELETE+INSERT this table (mirrors secrets).
		StampedFields:    []string{"CreatedAt"},
		SkipApplyRebuild: true,
	})
}
