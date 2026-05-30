package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// GroupsRow mirrors groups. The shape carries every column the existing
// store readers scan, including the JSON-blob `container_config` and
// the nullable `model`. We model `container_config` as raw JSON string
// rather than a typed struct because the column is operator-opaque
// (mounts, timeouts, max_children — different per product), and the
// engine's job is shape, not semantics. Decoding to GroupConfig happens
// in the imperative store path, not on the manifest round-trip.
type GroupsRow struct {
	Folder                string `db:"folder"                 yaml:"folder"                 json:"folder"`
	AddedAt               string `db:"added_at"               yaml:"added_at,omitempty"     json:"added_at,omitempty"`
	ContainerConfig       string `db:"container_config"       yaml:"container_config,omitempty" json:"container_config,omitempty"`
	Product               string `db:"product"                yaml:"product"                json:"product"`
	Model                 string `db:"model"                  yaml:"model,omitempty"        json:"model,omitempty"`
	UpdatedAt             string `db:"updated_at"             yaml:"updated_at,omitempty"   json:"updated_at,omitempty"`
	Open                  int    `db:"open"                   yaml:"open"                   json:"open"`
	ObserveWindowMessages int    `db:"observe_window_messages" yaml:"observe_window_messages,omitempty" json:"observe_window_messages,omitempty"`
	ObserveWindowChars    int    `db:"observe_window_chars"    yaml:"observe_window_chars,omitempty"    json:"observe_window_chars,omitempty"`
	CostCapCentsPerDay    int    `db:"cost_cap_cents_per_day"  yaml:"cost_cap_cents_per_day,omitempty" json:"cost_cap_cents_per_day,omitempty"`
}

func init() {
	resreg.Register(resreg.Resource{
		Name:     "groups",
		Table:    "groups",
		RowType:  reflect.TypeOf(GroupsRow{}),
		PKFields:      []string{"Folder"},
		Scope:         resreg.ScopeSpec{Field: "Folder"},
		StampedFields: []string{"AddedAt", "UpdatedAt"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(_ context.Context, _ *sql.Tx, row any) error {
				r := row.(*GroupsRow)
				if r.Product == "" {
					r.Product = "assistant"
				}
				now := time.Now().UTC().Format(time.RFC3339)
				if r.AddedAt == "" {
					r.AddedAt = now
				}
				if r.UpdatedAt == "" {
					r.UpdatedAt = now
				}
				// Default `open` to 1 (visible) — matches groups.open DEFAULT 1.
				// 0 stays 0; a YAML manifest must set `open: 0` explicitly to close.
				if r.ContainerConfig == "" {
					// Empty container_config column allowed (legacy rows) — but JSON
					// canonicalization helps deterministic emit. Empty string passes.
					r.ContainerConfig = ""
				} else {
					// Normalize JSON for deterministic emit: parse → re-marshal so
					// key order is canonical. Cheap; runs only on apply.
					var v map[string]any
					if json.Unmarshal([]byte(r.ContainerConfig), &v) == nil {
						if b, err := json.Marshal(v); err == nil {
							r.ContainerConfig = string(b)
						}
					}
				}
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"Model": {
					Read:  "COALESCE(model, '')",
					Write: nilIfEmptyString,
				},
				"ContainerConfig": {
					Read:  "COALESCE(container_config, '')",
					Write: nilIfEmptyString,
				},
				"UpdatedAt": {
					Read:  "COALESCE(updated_at, '')",
					Write: nilIfEmptyString,
				},
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
