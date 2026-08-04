package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// InvitesRow mirrors the invites table (store/migrations/0077-invites-hash-at-rest).
// expires_at is nullable in DB but exposed as an (omitempty) string; used_count
// and max_uses are INTEGER. Kept as plain scalars for uniform engine handling —
// this row drives the /openapi.json schema only (onbod's handler does its own
// scan via store.ListInvites). ExpiresAt DOES need the nullable-scan hook below
// (corrected — found by the 5/8 archive round-trip test: a real, unexpiring
// invite — `arizuko invite create` with no --expires — scans NULL and crashed
// ScanAll outright, breaking `arizuko export`/`get`/`plan` on onbod for any
// instance holding one).
//
// `ref` (store.InviteRef = hex(sha256(token))) IS the DB primary key (I1) —
// the raw bearer is never persisted, so there is no token field to leak here
// even by omission-bug. `yaml:"-"` keeps ref out of `arizuko export` (it's
// runtime-minted, not manifest state, like route_tokens' token_hash); `json:"ref"`
// is what every read surface hands out and the DELETE path takes.
type InvitesRow struct {
	Ref         string `db:"ref"           yaml:"-"                      json:"ref"`
	TargetGlob  string `db:"target_glob"   yaml:"target_glob"            json:"target_glob"`
	IssuedBySub string `db:"issued_by_sub" yaml:"issued_by_sub"          json:"issued_by_sub"`
	IssuedAt    string `db:"issued_at"     yaml:"issued_at"              json:"issued_at"`
	ExpiresAt   string `db:"expires_at"    yaml:"expires_at,omitempty"   json:"expires_at,omitempty"`
	MaxUses     int    `db:"max_uses"      yaml:"max_uses"               json:"max_uses"`
	UsedCount   int    `db:"used_count"    yaml:"used_count"             json:"used_count"`
}

// InvitesEndpoints mirrors onbod's hand-rolled invite REST face
// (onbod/admin.go handleInvite{Create,List,Revoke}): served at /v1/invites
// (POST create, GET list, DELETE by {ref}), NOT the PK-CRUD convention path
// /v1/invites/{token} for create. The DELETE key is the ref, not the bearer:
// putting a live token in a URL leaks it to request and proxy logs. onbod
// mounts these via the shared resreg handler (onbod/invites_resource.go); this
// list is the single declaration the /openapi.json doc also reads, so served
// routes and doc cannot drift.
var InvitesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/invites", Action: resreg.ActionCreate},
	{Verb: "GET", Path: "/v1/invites", Action: resreg.ActionList},
	{Verb: "DELETE", Path: "/v1/invites/{ref}", Action: resreg.ActionDelete},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "invites",
		Table:     "invites",
		DB:        resreg.SubsystemOnbod,
		RowType:   reflect.TypeOf(InvitesRow{}),
		PKFields:  []string{"Ref"},
		Endpoints: InvitesEndpoints,
		// Invite tokens are minted imperatively (invite_create / POST /v1/invites)
		// and never round-trip through a manifest — Apply must never DELETE+INSERT
		// this table, or an apply would revoke every live invite (mirrors
		// route_tokens and secrets).
		SkipApplyRebuild: true,
		Hooks: resreg.Hooks{
			ColumnOverride: map[string]resreg.ColumnHook{
				// expires_at is NULL for an invite minted with no --expires
				// (store/migrations/0077); every other nullable-mapped-to-
				// non-pointer column in this package uses the identical
				// COALESCE+nilIfEmptyString pair (route_tokens.Context,
				// groups.UpdatedAt).
				"ExpiresAt": {
					Read:  "COALESCE(expires_at, '')",
					Write: nilIfEmptyString,
				},
			},
		},
	})
}
