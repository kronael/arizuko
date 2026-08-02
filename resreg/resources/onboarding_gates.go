package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// OnboardingGatesRow mirrors onboarding_gates. SQLite stores enabled
// as INTEGER 0/1; we keep the Go field as int for engine simplicity
// (the db column type and Go field type match exactly). YAML callers
// see int too — `enabled: 1` / `enabled: 0`. Trade-off: a `bool` would
// be friendlier but would force a custom (de)serializer; the spec
// favors uniform engine handling over per-resource ergonomics.
type OnboardingGatesRow struct {
	Gate        string `db:"gate"          yaml:"gate"          json:"gate"`
	LimitPerDay int    `db:"limit_per_day" yaml:"limit_per_day" json:"limit_per_day"`
	Enabled     int    `db:"enabled"       yaml:"enabled"       json:"enabled"`
}

// OnboardingGatesEndpoints mirrors onbod's hand-rolled gate REST face
// (onbod/admin.go handleGate{List,Put,Delete}): the table is served at /v1/gates
// (GET list, PUT/DELETE by {gate}), NOT the resource-name path /v1/gates would be
// under the PK-CRUD convention (/v1/onboarding_gates). onbod mounts these with
// raw mux handlers, so this list is the doc's single declaration of the shape —
// keep it in sync with onbod/main.go if those routes change.
var OnboardingGatesEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/gates", Action: resreg.ActionList},
	{Verb: "PUT", Path: "/v1/gates/{gate}", Action: resreg.ActionUpdate},
	{Verb: "DELETE", Path: "/v1/gates/{gate}", Action: resreg.ActionDelete},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "onboarding_gates",
		Table:     "onboarding_gates",
		RowType:   reflect.TypeFor[OnboardingGatesRow](),
		PKFields:  []string{"Gate"},
		Endpoints: OnboardingGatesEndpoints,
	})
}
