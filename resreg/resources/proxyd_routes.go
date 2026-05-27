package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// ProxydRoutesRow mirrors proxyd_routes. PreserveHeaders is a JSON
// array string in DB but exposed as []string on the Row. StripPrefix
// is 0/1 INTEGER in DB but a clean Go field — engine uses two
// "shadow" columns for the raw DB representation and BeforeInsert /
// AfterScan keep them in sync.
//
// Trade-off note (spec 5/36 §"What does NOT come for free"): JSON-
// blob columns force a hook. We keep the public field shape natural
// (`[]string`, `bool`) and pay the conversion in two short hooks.
type ProxydRoutesRow struct {
	Path                string   `db:"path"             yaml:"path"             json:"path"`
	Backend             string   `db:"backend"          yaml:"backend"          json:"backend"`
	Auth                string   `db:"auth"             yaml:"auth"             json:"auth"`
	GatedBy             string   `db:"gated_by"         yaml:"gated_by,omitempty"         json:"gated_by,omitempty"`
	PreserveHeadersRaw  string   `db:"preserve_headers" yaml:"-"                json:"-"`
	StripPrefixRaw      int      `db:"strip_prefix"     yaml:"-"                json:"-"`
	PreserveHeaders     []string `db:"-"                yaml:"preserve_headers,omitempty" json:"preserve_headers,omitempty"`
	StripPrefix         bool     `db:"-"                yaml:"strip_prefix,omitempty"     json:"strip_prefix,omitempty"`
}

func init() {
	resreg.Register(resreg.Resource{
		Name:     "proxyd_routes",
		Table:    "proxyd_routes",
		RowType:  reflect.TypeOf(ProxydRoutesRow{}),
		PKFields: []string{"Path"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(_ context.Context, _ *sql.Tx, row any) error {
				r := row.(*ProxydRoutesRow)
				headers := r.PreserveHeaders
				if headers == nil {
					headers = []string{}
				}
				b, err := json.Marshal(headers)
				if err != nil {
					return err
				}
				r.PreserveHeadersRaw = string(b)
				if r.StripPrefix {
					r.StripPrefixRaw = 1
				} else {
					r.StripPrefixRaw = 0
				}
				return nil
			},
			AfterScan: func(row any) error {
				r := row.(*ProxydRoutesRow)
				if r.PreserveHeadersRaw != "" {
					if err := json.Unmarshal([]byte(r.PreserveHeadersRaw), &r.PreserveHeaders); err != nil {
						return err
					}
				}
				r.StripPrefix = r.StripPrefixRaw != 0
				return nil
			},
		},
	})
}
