package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// SecretsRow mirrors secrets metadata: the (scope_kind, scope_id, key) triple
// ONLY. The encrypted blob is set imperatively and never appears in YAML,
// plan output or error messages (spec 5/8 §"Secret safety"); it is not in
// this struct, so no read reaches it.
type SecretsRow struct {
	ScopeKind string `db:"scope_kind" yaml:"scope_kind" json:"scope_kind"`
	ScopeID   string `db:"scope_id"   yaml:"scope_id"   json:"scope_id"`
	Key       string `db:"key"        yaml:"key"        json:"key"`
	CreatedAt string `db:"created_at" yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// SecretsEndpoints is WRITE-ONLY by construction: create and a key-addressed
// delete, NO list/get, since a sealed value must never appear in any read
// surface (spec 5/8 §"Secret safety"). No MCPDoc entry, so no agent tool.
var SecretsEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/secrets", Action: resreg.ActionCreate},
	{Verb: "DELETE", Path: "/v1/secrets/{key}", Action: resreg.ActionDelete},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          SecretsName,
		Table:         "secrets",
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[SecretsRow](),
		Endpoints:     SecretsEndpoints,
		PKFields:      []string{"ScopeKind", "ScopeID", "Key"},
		EntitySortKey: []string{"ScopeKind", "ScopeID", "Key"},
		EntityKeySep:  store.KeySep,
		// No folder scope: scope_id is polymorphic by scope_kind (spec 5/8
		// §"FK posture").
		StampedFields:    []string{"CreatedAt"},
		SkipApplyRebuild: true, // enc_value blobs are set imperatively, never via apply
	})
}
