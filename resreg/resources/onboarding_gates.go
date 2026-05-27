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

func init() {
	resreg.Register(resreg.Resource{
		Name:     "onboarding_gates",
		Table:    "onboarding_gates",
		RowType:  reflect.TypeOf(OnboardingGatesRow{}),
		PKFields: []string{"Gate"},
	})
}
