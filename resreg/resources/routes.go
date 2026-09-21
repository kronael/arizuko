package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
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
// `id` is the harder one, and it is DECLARED rather than left out. It is the
// handle `delete_route(id)` addresses — a live agent wire contract, and
// list_routes is where the agent reads it. Undeclared, an apply renumbers every
// route, so an id the agent holds names a different rule afterwards. Declared,
// the manifest carries the handle and the round trip preserves it: it is a
// StampedField, so a hand-written manifest that omits it still reads as
// unchanged and the writer mints one past the largest the set states
// (resreg/entity.go mintNumbers).
//
// PK is (seq, match, target) and so is the COORDINATE, because that is what an
// apply addresses a rule by. `seq` is a number and a sort key is compared as
// text, so it declares the padded encoding: without it "10" sorts before "2"
// and a table whose whole meaning is "lower seq wins, first match" reads back
// in an order that contradicts itself (resreg/codec.go).
//
// A nullable column needs no handling at all now: an attribute an item does not
// carry is absent, and absent is the only null this model has.
type RoutesRow struct {
	ID                    int    `db:"id"                       yaml:"id,omitempty"             json:"id,omitempty"`
	Seq                   int    `db:"seq,pad"                  yaml:"seq"                      json:"seq"`
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

// RoutesResponses declares what each mounted REST face ANSWERS with. list is
// the only one: routd annotates every row (mode, fires_turn, triggers_on,
// announce, explain, shadowed_by) and answers []router.RouteView, never the
// stored RoutesRow. add answers {"id"}, set {"updated","count"} — ad-hoc maps
// with no Go type to name, so they declare nothing and document no body
// (BUGS L120).
var RoutesResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]router.RouteView](),
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
		Name:     RoutesName,
		Table:    "routes",
		DB:       resreg.SubsystemRoutd,
		RowType:  reflect.TypeFor[RoutesRow](),
		PKFields: []string{"Seq", "Match", "Target"},
		// (seq, target, match) is unique on all three live instances, which is
		// what makes it a coordinate. The order puts `seq` first because that is
		// the order the route table is READ in — a driver answers in sort-key
		// order, so the read comes back in precedence order with no sort after it.
		EntitySortKey: []string{"Seq", "Target", "Match"},
		EntityKeySep:  store.KeySep,
		Endpoints:     RoutesEndpoints,
		Responses:     RoutesResponses,
		MCPDoc:        RoutesMCPDoc,
		MCPArgs:       RoutesMCPArgs,
		MCPNames:      RoutesMCPNames,
		// No folder scope: routes.target carries #observe/#topic fragments
		// (spec 5/8 §"FK posture") — not column-equal to a folder, so Apply
		// rebuilds routes wholesale rather than per-folder.
		// `id` is server-owned, so Diff ignores it and it is never a create
		// argument. A row that carries none gets one past the largest the set
		// states (resreg/entity.go mintNumbers) — the answer AUTOINCREMENT gave.
		StampedFields: []string{"ID"},
	})
}
