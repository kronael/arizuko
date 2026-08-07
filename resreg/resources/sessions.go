package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// SessionsRow is one refresh-token FAMILY — a login and everything it rotated
// into — projected out of refresh_tokens without a single credential column.
// `token_hash` is absent here and unnamed in the query that fills this
// (authd/sessions_resource.go), the same discipline RouteTokensRow applies to
// its own hash PK. There is no raw-token column to omit: authd never stores one
// (authd/store.go insertRefresh persists sha256 only), so the strongest thing a
// reader can learn from this surface is that a session exists.
//
// The family, not the row, is the unit. A 30-day session is dozens of rows —
// one per rotation, all but the newest tombstoned — and an operator asking "who
// is logged in" means the lineage. FamilyID is the handle the revoke below
// takes, and it is a random uuid that authenticates nothing on its own: holding
// one lets a caller who ALREADY passed the sessions:write gate name a target.
//
// Status collapses the two tombstone columns plus the clock into the word an
// operator actually needs: revoked (a kill landed, by logout, by reuse
// detection, or by the DELETE below), expired (past its 30 days), active.
//
// The `db:` tags mark the six fields that ARE columns; StartedAt, Rotations
// and Status are aggregates or derived and carry none. That set is also the
// engine's projection of refresh_tokens, so even a future engine-driven read
// of this table could only ever name these six — token_hash, used_at and
// revoked_at are unreachable through it.
type SessionsRow struct {
	FamilyID  string `db:"family_id"  json:"family_id"`
	Sub       string `db:"sub"        json:"sub"`
	Scope     string `db:"scope"      json:"scope"`
	Aud       string `db:"aud"        json:"aud,omitempty"`
	StartedAt string `json:"started_at"`
	RenewedAt string `db:"issued_at"  json:"renewed_at"`
	ExpiresAt string `db:"expires_at" json:"expires_at"`
	Rotations int    `json:"rotations"`
	Status    string `json:"status"`
}

// SessionsEndpoints is the operator session surface: list the families, and
// kill one. The DELETE is the incident-response verb spec 5/1 frames and BUGS
// F15 records as missing — self-service logout already existed
// (`POST /auth/logout`, the caller's own cookie), and what did not was killing
// a session whose holder cannot or will not do it.
//
// DELETE, not POST /revoke: revoking a family is exactly resreg's `delete`
// action over the session resource, and taking the standard action is what
// gets the audit row written inside the mutation's own transaction
// (resreg.invoke) instead of needing a bespoke emit. The row is not deleted —
// `revoked_at` is set, because the tombstone IS the reuse-detection evidence.
//
// No MCP face, for the same reason as signing_keys: MCPDoc is what mints a
// tool, and none is declared. A session is keyed by `sub`, refresh_tokens
// carries no folder column, and an agent is folder-scoped — so there is no
// predicate that could contain an agent's reach over this table. Rather than
// mount a tool whose gate would have to be "operator only, trust us", the
// resource stays REST-only and says why.
var SessionsEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/sessions", Action: resreg.ActionList, Status: http.StatusOK},
	{Verb: "DELETE", Path: "/v1/sessions/{family_id}", Action: resreg.ActionDelete, Status: http.StatusOK},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      SessionsName,
		RowType:   reflect.TypeFor[SessionsRow](),
		PKFields:  []string{"FamilyID"},
		Endpoints: SessionsEndpoints,
		Table:     "refresh_tokens",
		// DB is deliberately EMPTY, exactly as for audit and signing_keys:
		// BySubsystem never returns this resource, so Export/Plan/Apply never
		// touch it. Live credentials are not declarative config — a manifest
		// that could recreate a session would be a session-forgery tool.
		DB: "",
	})
}
