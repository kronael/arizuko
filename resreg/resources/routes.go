package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// RoutesRow mirrors store/routes — EVERY column of it. Apply rebuilds the table
// wholesale, so a column this does not name is a column reset to its DDL default
// by any apply that mentions routes (BUGS L265).
//
// `added_by`/`added_via` are that loss made real: migration 0028 added them so a
// route records WHO exercised routing authority over its target and BY WHICH
// ACT, onbod and routd both stamp them, and every one of the 84 rows on the
// three live instances carries NULL in both — an apply reinserted five columns
// and the other two took their defaults.
//
// `id` is the harder one, and it is DECLARED rather than left out. It is
// AUTOINCREMENT, so nothing in a manifest has to state one, and it is also the
// handle `delete_route(id)` addresses — a live agent wire contract, and
// list_routes is where the agent reads it. Undeclared, an apply renumbers every
// route, so an id the agent holds names a different rule afterwards. Declared,
// the manifest carries the handle and the round trip preserves it: it is a
// StampedField, so a hand-written manifest that omits it still reads as
// unchanged and the engine assigns one. The object path has no AUTOINCREMENT and
// no int coordinate, so the writer mints it there — which is the same answer
// spec 6/40 gives for generated ids, and it needs this field to exist.
//
// PK is (seq, match, target) per the spec catalog; `id` is a handle, not
// identity. Nullable columns map with 0/” → NULL on write.
type RoutesRow struct {
	ID                    int    `db:"id"                       yaml:"id,omitempty"             json:"id,omitempty"`
	Seq                   int    `db:"seq"                      yaml:"seq"                      json:"seq"`
	Match                 string `db:"match"                    yaml:"match"                    json:"match"`
	Target                string `db:"target"                   yaml:"target"                   json:"target"`
	ObserveWindowMessages int    `db:"observe_window_messages"  yaml:"observe_window_messages,omitempty"  json:"observe_window_messages,omitempty"`
	ObserveWindowChars    int    `db:"observe_window_chars"     yaml:"observe_window_chars,omitempty"     json:"observe_window_chars,omitempty"`
	AddedBy               string `db:"added_by"                 yaml:"added_by,omitempty"       json:"added_by,omitempty"`
	AddedVia              string `db:"added_via"                yaml:"added_via,omitempty"      json:"added_via,omitempty"`
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
// (seq,match,target) PK. Spec 5/16.
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
		"Fields: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe), or #announce to flag this channel as the group's release-note target (inbound-neutral; the migrate skill posts release notes only to #announce channels, one per server). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
	resreg.Action("set"): "Bulk-overwrite the full routing table for this folder subtree. Use only for wholesale reconfiguration where you've already read the current set. Prefer add_route/delete_route for targeted edits — this clobbers everything else. " +
		"Each route: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe), or #announce to flag this channel as the group's release-note target (inbound-neutral; the migrate skill posts release notes only to #announce channels, one per server). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
	resreg.ActionDelete: "Remove one routing rule by id. Use after list_routes/inspect_routing to surgically drop a rule. Not for bulk clear (set_routes with empty array).",
	resreg.ActionList:   "Return the routing table rows this group can see, each annotated with mode (trigger/observe), fires_turn, triggers_on, announce (release-note target), a plain explain, and shadowed_by (earlier rule that intercepts it). Prefer inspect_routing when you also want JID→folder resolution or errored-chat context.",
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
		Name:      RoutesName,
		Table:     "routes",
		DB:        resreg.SubsystemRoutd,
		RowType:   reflect.TypeFor[RoutesRow](),
		PKFields:  []string{"Seq", "Match", "Target"},
		Endpoints: RoutesEndpoints,
		MCPDoc:    RoutesMCPDoc,
		MCPArgs:   RoutesMCPArgs,
		MCPNames:  RoutesMCPNames,
		// No folder scope: routes.target carries #observe/#topic fragments
		// (spec 5/8 §"FK posture") — not column-equal to a folder, so Apply
		// rebuilds routes wholesale rather than per-folder.
		// The engine assigns `id` when the row does not carry one, so Diff ignores
		// it and it is never a create argument. The write hook below is what
		// leaves the assignment to the engine: 0 binds as NULL, and AUTOINCREMENT
		// answers. A stamped field with a write hook is never auto-filled (F49),
		// which is what keeps an int out of the string stamp.
		StampedFields: []string{"ID"},
		Hooks: resreg.Hooks{
			ColumnOverride: map[string]resreg.ColumnHook{
				"ID": {Write: nilIfZeroInt},
				"ObserveWindowMessages": {
					Read:  "COALESCE(observe_window_messages, 0)",
					Write: nilIfZeroInt,
				},
				"ObserveWindowChars": {
					Read:  "COALESCE(observe_window_chars, 0)",
					Write: nilIfZeroInt,
				},
				// NULL is what both stamping writers store for an unrecorded
				// actor (routd/routes_resource.go, onbod/main.go), so the apply
				// stores the same thing rather than a second spelling of absent.
				"AddedBy":  {Read: "COALESCE(added_by, '')", Write: nilIfEmptyString},
				"AddedVia": {Read: "COALESCE(added_via, '')", Write: nilIfEmptyString},
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
