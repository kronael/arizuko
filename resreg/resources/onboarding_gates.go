package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// OnboardingGatesRow mirrors the onboarding_gates entity (store/onboarding.go):
// sort key Gate. enabled stays an int so the engine's strconv rendering is
// byte for byte the "0"/"1" store.PutGateTx writes; a `bool` would force a
// rendering both readers have to agree on.
type OnboardingGatesRow struct {
	Gate        string `db:"gate"          yaml:"gate"          json:"gate"`
	LimitPerDay int    `db:"limit_per_day" yaml:"limit_per_day" json:"limit_per_day"`
	Enabled     int    `db:"enabled"       yaml:"enabled"       json:"enabled"`
}

// OnboardingGatesEndpoints serves the table at /v1/gates, NOT
// /v1/onboarding_gates. The single declaration onbod's handler and the
// /openapi.json doc both read.
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
		EntitySortKey: []string{"Gate"},
		Endpoints:     OnboardingGatesEndpoints,
	})
}
