package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// GroupsRow mirrors groups. Per-group behavioral settings (model,
// thread_replies, observe_window_*, open) live inside the `config` JSON
// blob; `container_config` carries spawn shape. Both are modeled as raw
// JSON strings rather than typed structs because they are operator-opaque
// and the engine's job is shape, not semantics — decoding happens in the
// imperative store path, not on the manifest round-trip.
type GroupsRow struct {
	Folder             string `db:"folder"           yaml:"folder"           json:"folder"`
	AddedAt            string `db:"added_at"         yaml:"added_at,omitempty"     json:"added_at,omitempty"`
	ContainerConfig    string `db:"container_config" yaml:"container_config,omitempty" json:"container_config,omitempty"`
	Product            string `db:"product"          yaml:"product"          json:"product"`
	Config             string `db:"config"           yaml:"config,omitempty" json:"config,omitempty"`
	UpdatedAt          string `db:"updated_at"       yaml:"updated_at,omitempty"   json:"updated_at,omitempty"`
	CostCapCentsPerDay int    `db:"cost_cap_cents_per_day" yaml:"cost_cap_cents_per_day,omitempty" json:"cost_cap_cents_per_day,omitempty"`
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
				// Normalize the JSON blobs for deterministic emit: parse →
				// re-marshal so key order is canonical. Cheap; runs only on apply.
				r.ContainerConfig = canonJSON(r.ContainerConfig)
				r.Config = canonJSON(r.Config)
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"Config": {
					Read:  "COALESCE(config, '')",
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
			},
		},
	})
}

// canonJSON re-marshals a JSON object string so its key order is canonical,
// for deterministic manifest emit. Empty or unparseable input passes through
// unchanged (legacy/empty rows stay as-is).
func canonJSON(s string) string {
	if s == "" {
		return s
	}
	var v map[string]any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(b)
}
