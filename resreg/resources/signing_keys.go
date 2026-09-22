package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// SigningKeysRow is signing_keys MINUS every private column: `priv_pem` and
// `pub_pem` are named nowhere in this file and nowhere in the read that fills
// it, so no caller, scope or argument can reach them. It publishes the
// lifecycle metadata the JWK Set at `GET /v1/keys` does not carry (spec 5/1
// § JWK rotation mechanics). ServesUntil is derived: a key verifies while
// active OR while now < retired_at + maxAccessTTL. The `db:` tags mark the
// four fields that ARE columns, so even an engine-driven read could only
// ever name these four.
type SigningKeysRow struct {
	Kid         string `db:"kid"        json:"kid"`
	Alg         string `json:"alg"`
	Active      bool   `db:"active"     json:"active"`
	Status      string `json:"status"`
	CreatedAt   string `db:"created_at" json:"created_at"`
	RetiredAt   string `db:"retired_at" json:"retired_at,omitempty"`
	ServesUntil string `json:"serves_until,omitempty"`
}

// SigningKeysResponses: SigningKeyMetadata answers []SigningKeysRow.
var SigningKeysResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]SigningKeysRow](),
}

// SigningKeysEndpoints is list-only. No create/delete: rotation and emergency
// revoke must go through the daemon so the in-memory serving set is rebuilt
// with the row. No MCP face: signing keys are instance-global, so there is no
// containment predicate to bind an agent tool to.
var SigningKeysEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/signing_keys", Action: resreg.ActionList, Status: http.StatusOK},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      SigningKeysName,
		RowType:   reflect.TypeFor[SigningKeysRow](),
		PKFields:  []string{"Kid"},
		Endpoints: SigningKeysEndpoints,
		Responses: SigningKeysResponses,
		Table:     "signing_keys",
		// DB is deliberately EMPTY, as for audit: a signing key is the trust
		// root, not declarative config.
		DB: "",
	})
}
