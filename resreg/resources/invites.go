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
//
// `token` is the live bearer: whoever holds it redeems the grant. It stays on
// the struct because it is the DB PK (engine matching), but it is projected on
// NO read surface — `yaml:"-"` keeps it out of `arizuko export`, `json:"-"`
// keeps it out of both the list response and the /openapi.json schema. The
// create response carries it once, out of band (onbod's inviteCreatedJSON),
// exactly as route_tokens' issue verbs do (spec 5/8 §"Secret safety").
//
// `ref` (store.InviteRef = hex(sha256(token))) is the non-secret identity every
// read surface hands out and the DELETE path takes. It is derived, not stored,
// so it carries no `db:` tag — the engine skips it and it never round-trips
// through a manifest.
type InvitesRow struct {
	Token       string `db:"token"         yaml:"-"                      json:"-"`
	Ref         string `                   yaml:"-"                      json:"ref"`
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
		RowType:   reflect.TypeOf(InvitesRow{}),
		PKFields:  []string{"Token"},
		Endpoints: InvitesEndpoints,
		// Invite tokens are minted imperatively (invite_create / POST /v1/invites)
		// and never round-trip through a manifest — Apply must never DELETE+INSERT
		// this table, or an apply would revoke every live invite (mirrors
		// route_tokens and secrets).
		SkipApplyRebuild: true,
	})
}
