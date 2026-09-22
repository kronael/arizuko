package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

// RoutesRow mirrors routes, EVERY column of it: apply rebuilds the table
// wholesale, so a column this does not name is a value gone (BUGS L265;
// `added_by`/`added_via` were lost that way). `id` is DECLARED: it is the
// handle `delete_route(id)` addresses, a live agent wire contract, and
// undeclared an apply would renumber every route. As a StampedField a
// manifest that omits it still reads as unchanged and the writer mints one
// (mintNumbers). `seq` declares the padded encoding: a sort key is compared
// as text, and "10" before "2" contradicts "lower seq wins" (resreg/codec.go).
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

// RoutesEndpoints is the single owner of the routes REST endpoint set. Routes
// are addressed by `id` on delete, NOT the (seq,match,target) PK.
var RoutesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/routes", Action: resreg.Action("add")},
	{Verb: "PUT", Path: "/v1/routes", Action: resreg.Action("set")},
	{Verb: "DELETE", Path: "/v1/routes/{id}", Action: resreg.ActionDelete},
	{Verb: "GET", Path: "/v1/routes", Action: resreg.ActionList},
}

// RoutesResponses: list answers []router.RouteView (every row annotated),
// never the stored RoutesRow; add and set answer ad-hoc maps and declare
// nothing (BUGS L120).
var RoutesResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]router.RouteView](),
}

// RoutesMCPNames maps each action to the flat tool name the live agent calls
// (spec 5/16).
var RoutesMCPNames = map[resreg.Action]string{
	resreg.Action("add"): "add_route",
	resreg.Action("set"): "set_routes",
	resreg.ActionDelete:  "delete_route",
	resreg.ActionList:    "list_routes",
}

// RoutesMCPDoc is the single owner of the routing tools' agent-facing
// one-liners. Copy verbatim — the agent wire contract.
var RoutesMCPDoc = map[resreg.Action]string{
	resreg.Action("add"): "Append one routing rule. Use for targeted routing changes (route one chat, one platform pattern) — preferred over set_routes for everything except full rewrites. " +
		"Fields: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe), or #announce to flag this channel as the group's release-note target (inbound-neutral; the migrate skill posts release notes only to #announce channels, one per server). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
	resreg.Action("set"): "Bulk-overwrite the full routing table for this folder subtree. Use only for wholesale reconfiguration where you've already read the current set. Prefer add_route/delete_route for targeted edits — this clobbers everything else. " +
		"Each route: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe), or #announce to flag this channel as the group's release-note target (inbound-neutral; the migrate skill posts release notes only to #announce channels, one per server). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
	resreg.ActionDelete: "Remove one routing rule by id. Use after list_routes/inspect_routing to surgically drop a rule. Not for bulk clear (set_routes with empty array).",
	resreg.ActionList:   "Return the routing table rows this group can see, each annotated with mode (trigger/observe), fires_turn, triggers_on, announce (release-note target), a plain explain, and shadowed_by (earlier rule that intercepts it). Prefer inspect_routing when you also want JID→folder resolution or errored-chat context.",
}

// RoutesMCPArgs is the agent face's wire shape (raw JSON strings + the
// numeric id), overriding RowType reflection.
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
		// `seq` leads because a driver answers in sort-key order, so the read
		// comes back in precedence order with no sort after it.
		EntitySortKey: []string{"Seq", "Target", "Match"},
		EntityKeySep:  store.KeySep,
		Endpoints:     RoutesEndpoints,
		Responses:     RoutesResponses,
		MCPDoc:        RoutesMCPDoc,
		MCPArgs:       RoutesMCPArgs,
		MCPNames:      RoutesMCPNames,
		// No folder scope: routes.target carries #observe/#topic fragments,
		// not column-equal to a folder (spec 5/8 §"FK posture"), so Apply
		// rebuilds routes wholesale. `id` is server-owned: Diff ignores it, it
		// is never a create argument, and a row without one gets one minted.
		StampedFields: []string{"ID"},
	})
}
