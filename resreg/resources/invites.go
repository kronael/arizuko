package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// InvitesRow mirrors the invites table (store/migrations/0032-invites-rewrite).
// expires_at is nullable in DB but exposed as an (omitempty) string; used_count
// and max_uses are INTEGER. Kept as plain scalars for uniform engine handling —
// this row drives the /openapi.json schema only (onbod's handler does its own
// scan via store.ListInvites), so no nullable-scan hook is needed here.
type InvitesRow struct {
	Token       string `db:"token"         yaml:"token"                  json:"token"`
	TargetGlob  string `db:"target_glob"   yaml:"target_glob"            json:"target_glob"`
	IssuedBySub string `db:"issued_by_sub" yaml:"issued_by_sub"          json:"issued_by_sub"`
	IssuedAt    string `db:"issued_at"     yaml:"issued_at"              json:"issued_at"`
	ExpiresAt   string `db:"expires_at"    yaml:"expires_at,omitempty"   json:"expires_at,omitempty"`
	MaxUses     int    `db:"max_uses"      yaml:"max_uses"               json:"max_uses"`
	UsedCount   int    `db:"used_count"    yaml:"used_count"             json:"used_count"`
}

// InvitesEndpoints mirrors onbod's hand-rolled invite REST face
// (onbod/admin.go handleInvite{Create,List,Revoke}): served at /v1/invites
// (POST create, GET list, DELETE by {token}), NOT the PK-CRUD convention path
// /v1/invites/{token} for create. onbod mounts these via the shared resreg
// handler (onbod/invites_resource.go); this list is the single declaration the
// /openapi.json doc also reads, so served routes and doc cannot drift.
var InvitesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/invites", Action: resreg.ActionCreate},
	{Verb: "GET", Path: "/v1/invites", Action: resreg.ActionList},
	{Verb: "DELETE", Path: "/v1/invites/{token}", Action: resreg.ActionDelete},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "invites",
		Table:     "invites",
		RowType:   reflect.TypeOf(InvitesRow{}),
		PKFields:  []string{"Token"},
		Endpoints: InvitesEndpoints,
	})
}
