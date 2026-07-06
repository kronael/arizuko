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

func init() {
	resreg.Register(resreg.Resource{
		Name:      "routes",
		Table:     "routes",
		RowType:   reflect.TypeOf(RoutesRow{}),
		PKFields:  []string{"Seq", "Match", "Target"},
		Endpoints: RoutesEndpoints,
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
