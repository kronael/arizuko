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

func init() {
	resreg.Register(resreg.Resource{
		Name:     "routes",
		Table:    "routes",
		RowType:  reflect.TypeOf(RoutesRow{}),
		PKFields: []string{"Seq", "Match", "Target"},
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
