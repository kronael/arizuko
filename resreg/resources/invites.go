package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// InvitesRow mirrors the invites entity (store/invites.go): sort key Ref,
// every other db:-tagged field one attribute. This row drives the
// /openapi.json schema only; onbod's handler reads via store.ListInvites.
// `ref` is hex(sha256(token)): the raw bearer is never persisted, so there is
// no token field to leak. `yaml:"-"` keeps ref out of `arizuko export` (it is
// runtime-minted, not manifest state); `json:"ref"` is what every read
// surface hands out and the DELETE path takes.
type InvitesRow struct {
	Ref         string `db:"ref"           yaml:"-"                      json:"ref"`
	TargetGlob  string `db:"target_glob"   yaml:"target_glob"            json:"target_glob"`
	IssuedBySub string `db:"issued_by_sub" yaml:"issued_by_sub"          json:"issued_by_sub"`
	IssuedAt    string `db:"issued_at"     yaml:"issued_at"              json:"issued_at"`
	ExpiresAt   string `db:"expires_at"    yaml:"expires_at,omitempty"   json:"expires_at,omitempty"`
	MaxUses     int    `db:"max_uses"      yaml:"max_uses"               json:"max_uses"`
	UsedCount   int    `db:"used_count"    yaml:"used_count"             json:"used_count"`
}

// InvitesEndpoints is the single declaration onbod's mounted handler and the
// /openapi.json doc both read. The DELETE key is the ref, not the bearer:
// a live token in a URL leaks to request and proxy logs.
var InvitesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/invites", Action: resreg.ActionCreate},
	{Verb: "GET", Path: "/v1/invites", Action: resreg.ActionList},
	{Verb: "DELETE", Path: "/v1/invites/{ref}", Action: resreg.ActionDelete},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          InvitesName,
		Table:         "invites",
		DB:            resreg.SubsystemOnbod,
		RowType:       reflect.TypeOf(InvitesRow{}),
		PKFields:      []string{"Ref"},
		EntitySortKey: []string{"Ref"},
		Endpoints:     InvitesEndpoints,
		// Invite tokens are minted imperatively and never round-trip through a
		// manifest; an apply that rebuilt this table would revoke every live
		// invite.
		SkipApplyRebuild: true,
	})
}
