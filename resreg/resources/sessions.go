package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// SessionsRow is one refresh-token FAMILY, a login and everything it rotated
// into, projected without a single credential column: `token_hash` is absent
// here and unnamed in the read that fills this, and authd never stores a raw
// token. FamilyID is a random uuid that authenticates nothing on its own.
// Status collapses the tombstones plus the clock into revoked | expired |
// active. The `db:` tags mark the six fields that ARE columns, so even an
// engine-driven read of this table could only ever name these six.
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

// SessionsResponses: SessionsByFamily answers []SessionsRow. The revoke
// answers {"family_id","revoked"} and declares nothing.
var SessionsResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]SessionsRow](),
}

// SessionsEndpoints is the operator session surface: list the families, and
// kill one (the incident-response verb of spec 5/1, BUGS F15). DELETE, not
// POST /revoke: the standard action gets the audit row written by
// resreg.invoke. The row is not deleted; `revoked_at` is set, because the
// tombstone IS the reuse-detection evidence. No MCP face: a session is keyed
// by `sub` with no folder column, so no predicate could contain an agent's
// reach over this table.
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
		Responses: SessionsResponses,
		Table:     "refresh_tokens",
		// DB is deliberately EMPTY, as for audit: a manifest that could
		// recreate a session would be a session-forgery tool.
		DB: "",
	})
}
