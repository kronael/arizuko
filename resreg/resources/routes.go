package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// RoutesRow mirrors store/routes — `id` is omitted (AUTOINCREMENT
// generated, not addressable via manifest). PK is (seq, match, target)
// per spec catalog. Nullable observe_window columns map to int with
// 0 → NULL on write.
type RoutesRow struct {
	Seq                   int    `db:"seq"                      yaml:"seq"                      json:"seq"`
	Match                 string `db:"match"                    yaml:"match"                    json:"match"`
	Target                string `db:"target"                   yaml:"target"                   json:"target"`
	ObserveWindowMessages int    `db:"observe_window_messages"  yaml:"observe_window_messages,omitempty"  json:"observe_window_messages,omitempty"`
	ObserveWindowChars    int    `db:"observe_window_chars"     yaml:"observe_window_chars,omitempty"     json:"observe_window_chars,omitempty"`
}

// RoutesEndpoints is the single owner of the routes REST endpoint set: routd's
// mounted routing tools (routes_resource.go) reference it so the mounted faces,
// the derived MCP tools, and /openapi.json read one list and can't drift. Routes
// are addressed by the autoincrement `id` on delete (add/set carry the row in
// the body), NOT the (seq,match,target) PK — hence the explicit set over the
// PK-CRUD convention.
var RoutesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/routes", Action: resreg.Action("add")},
	{Verb: "PUT", Path: "/v1/routes", Action: resreg.Action("set")},
	{Verb: "DELETE", Path: "/v1/routes/{id}", Action: resreg.ActionDelete},
	{Verb: "GET", Path: "/v1/routes", Action: resreg.ActionList},
}

// RoutesMCPNames maps each action to the flat tool name the live agent already
// calls; routd's routes_resource.go references it (agent socket derivation) and
// ipc.ListTools reads it via the registry walk, so the tool wire contract has one
// owner. delete addresses the row by the autoincrement `id` arg, not the
// (seq,match,target) PK. Spec 5/44.
var RoutesMCPNames = map[resreg.Action]string{
	resreg.Action("add"): "add_route",
	resreg.Action("set"): "set_routes",
	resreg.ActionDelete:  "delete_route",
	resreg.ActionList:    "list_routes",
}

// RoutesMCPDoc is the single owner of the routing tools' agent-facing one-liners
// (routd derives the agent socket tools from it; dashd's tool browser reads it via
// ipc.ListTools). Copy verbatim — the agent wire contract.
var RoutesMCPDoc = map[resreg.Action]string{
	resreg.Action("add"): "Append one routing rule. Use for targeted routing changes (route one chat, one platform pattern) — preferred over set_routes for everything except full rewrites. " +
		"Fields: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
	resreg.Action("set"): "Bulk-overwrite the full routing table for this folder subtree. Use only for wholesale reconfiguration where you've already read the current set. Prefer add_route/delete_route for targeted edits — this clobbers everything else. " +
		"Each route: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
	resreg.ActionDelete: "Remove one routing rule by id. Use after list_routes/inspect_routing to surgically drop a rule. Not for bulk clear (set_routes with empty array).",
	resreg.ActionList:   "Return the routing table rows this group can see, each annotated with mode (trigger/observe), fires_turn, triggers_on, a plain explain, and shadowed_by (earlier rule that intercepts it). Prefer inspect_routing when you also want JID→folder resolution or errored-chat context.",
}

// RoutesMCPArgs is the explicit per-action arg list. The agent face carries raw
// JSON strings + the numeric id, NOT the RowType-reflected columns, so this
// overrides RowType reflection for the derived agent/browser tools.
var RoutesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("add"): {{Name: "route", Type: "string", Required: true}},
	resreg.Action("set"): {{Name: "routes", Type: "string", Required: true}},
	resreg.ActionDelete:  {{Name: "id", Type: "number", Required: true}},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "routes",
		Table:     "routes",
		RowType:   reflect.TypeOf(RoutesRow{}),
		PKFields:  []string{"Seq", "Match", "Target"},
		Endpoints: RoutesEndpoints,
		MCPDoc:    RoutesMCPDoc,
		MCPArgs:   RoutesMCPArgs,
		MCPNames:  RoutesMCPNames,
		// No folder scope: routes.target carries #observe/#topic fragments
		// (spec 5/36 §"FK posture") — not column-equal to a folder, so Apply
		// rebuilds routes wholesale rather than per-folder.
		Hooks: resreg.Hooks{
			ColumnOverride: map[string]resreg.ColumnHook{
				"ObserveWindowMessages": {
					Read:  "COALESCE(observe_window_messages, 0)",
					Write: nilIfZeroInt,
				},
				"ObserveWindowChars": {
					Read:  "COALESCE(observe_window_chars, 0)",
					Write: nilIfZeroInt,
				},
			},
		},
	})
}

func nilIfZeroInt(v any) (any, error) {
	n, _ := v.(int)
	if n == 0 {
		return nil, nil
	}
	return n, nil
}

func nilIfEmptyString(v any) (any, error) {
	s, _ := v.(string)
	if s == "" {
		return nil, nil
	}
	return s, nil
}
