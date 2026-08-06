package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// SigningKeysRow is signing_keys MINUS every private column, the same shape of
// projection RouteTokensRow makes over route_tokens. authd is the sole ES256
// signer, so this is the strictest such projection in the tree and the omission
// is the point: `priv_pem` and `pub_pem` are named nowhere in this file and
// nowhere in the query that fills it (authd/signing_keys_resource.go), so no
// caller, scope or argument can reach them.
//
// What it publishes is the operational metadata `GET /v1/keys` drops on the
// floor. The JWK Set carries {kty,crv,x,y,kid,alg,use} — enough to VERIFY a
// token, nothing about the key's lifecycle — so an operator asking "which key
// is signing right now", "when was it rotated" or "when does the retired one
// stop verifying" had to open auth.db with sqlite3. Spec 5/1 § JWK rotation
// mechanics is the model this makes visible.
//
// ServesUntil is derived, not stored: a key verifies while active OR while
// now < retired_at + maxAccessTTL, and maxAccessTTL is a daemon constant
// (authd/main.go), not a column. Publishing the computed instant is what makes
// the time-based window legible without reimplementing the rule in the reader.
//
// The `db:` tags mark the four fields that ARE columns; Alg, Status and
// ServesUntil are derived and carry none. That set is also the engine's
// projection of this table, so even a future engine-driven read of it could
// only ever name these four.
type SigningKeysRow struct {
	Kid         string `db:"kid"        json:"kid"`
	Alg         string `json:"alg"`
	Active      bool   `db:"active"     json:"active"`
	Status      string `json:"status"`
	CreatedAt   string `db:"created_at" json:"created_at"`
	RetiredAt   string `db:"retired_at" json:"retired_at,omitempty"`
	ServesUntil string `json:"serves_until,omitempty"`
}

// SigningKeysEndpoints is the read-only key-metadata surface. List only, and
// deliberately so on all three axes:
//
//   - No GET by kid. The set is a handful of rows with one active member; a
//     window IS the unit of use, exactly as for audit.
//   - No create/delete. Rotation is `Authd.Rotate`, emergency revoke is
//     `RevokeAllNow`, and both must go through the daemon so the in-memory
//     serving set is rebuilt with the row. A REST write straight at the table
//     would leave the running signer disagreeing with its own DB.
//   - No MCP face. Declaring MCPDoc is what mints an agent tool, and this
//     resource declares none: agents are folder-scoped and signing keys are
//     instance-global, so there is no containment predicate to bind a tool to.
//     A tool no socket can safely mount is a declaration, not a face.
var SigningKeysEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/signing_keys", Action: resreg.ActionList, Status: http.StatusOK},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "signing_keys",
		RowType:   reflect.TypeFor[SigningKeysRow](),
		PKFields:  []string{"Kid"},
		Endpoints: SigningKeysEndpoints,
		Table:     "signing_keys",
		// DB is deliberately EMPTY, exactly as for audit: BySubsystem never
		// returns this resource, so Export/Plan/Apply never touch it. A signing
		// key is the trust root, not declarative config — `arizuko apply` must
		// never rebuild the signer's key table from a YAML document, and the
		// projection above has no private column to rebuild it from anyway.
		DB: "",
	})
}
