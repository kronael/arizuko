package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// OnboardingGatesRow mirrors the onboarding_gates entity
// (store/onboarding.go), whose sort key is Gate and whose two other db:-tagged
// fields are its attributes. enabled stays an int: every attribute value is
// text and the engine renders an int field with strconv, which is the same
// "0"/"1" store.PutGateTx writes, so the two readers of these items agree byte
// for byte. YAML callers see the int too — `enabled: 1` / `enabled: 0`. A
// `bool` would be friendlier and would force a rendering somebody has to
// choose and both readers have to agree with.
type OnboardingGatesRow struct {
	Gate        string `db:"gate"          yaml:"gate"          json:"gate"`
	LimitPerDay int    `db:"limit_per_day" yaml:"limit_per_day" json:"limit_per_day"`
	Enabled     int    `db:"enabled"       yaml:"enabled"       json:"enabled"`
}

// OnboardingGatesEndpoints mirrors onbod's hand-rolled gate REST face
// (onbod/admin.go handleGate{List,Put,Delete}): the table is served at /v1/gates
// (GET list, PUT/DELETE by {gate}), NOT the resource-name path /v1/gates would be
// under the PK-CRUD convention (/v1/onboarding_gates). onbod mounts these via
// the shared resreg handler (onbod/gates_resource.go); this list is the single
// declaration the /openapi.json doc also reads, so served routes and doc cannot
// drift.
var OnboardingGatesEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/gates", Action: resreg.ActionList},
	{Verb: "PUT", Path: "/v1/gates/{gate}", Action: resreg.ActionUpdate},
	{Verb: "DELETE", Path: "/v1/gates/{gate}", Action: resreg.ActionDelete},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          OnboardingGatesName,
		Table:         "onboarding_gates",
		DB:            resreg.SubsystemOnbod,
		RowType:       reflect.TypeFor[OnboardingGatesRow](),
		PKFields:      []string{"Gate"},
		EntitySortKey: "Gate",
		Endpoints:     OnboardingGatesEndpoints,
	})
}
