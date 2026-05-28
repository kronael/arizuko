package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// SecretsRow mirrors secrets metadata. The encrypted value blob is
// imperatively managed via `arizuko secret set …` — manifests carry
// the (scope_kind, scope_id, key) triple ONLY. Per spec 5/36 §"Secret
// safety", the blob never appears in YAML, plan output, or error
// messages.
//
// Apply path: SkipApplyRebuild=true, so Apply never DELETE+INSERTs this
// table. The resource is export-only — `arizuko export` emits the
// metadata triples (the engine's SELECT omits the `enc_value` BLOB
// because it's not in this struct), but apply leaves the rows untouched.
// secrets is also excluded from the `config_meta` count (spec §"CAS
// implementation"), so out-of-band blob rotation doesn't invalidate a
// pending manifest apply. Rebuilding triples from YAML on apply (so a
// manifest can declare secret shape before the blob lands) is drafted.
type SecretsRow struct {
	ScopeKind string `db:"scope_kind" yaml:"scope_kind" json:"scope_kind"`
	ScopeID   string `db:"scope_id"   yaml:"scope_id"   json:"scope_id"`
	Key       string `db:"key"        yaml:"key"        json:"key"`
	CreatedAt string `db:"created_at" yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

func init() {
	resreg.Register(resreg.Resource{
		Name:             "secrets",
		Table:            "secrets",
		RowType:          reflect.TypeOf(SecretsRow{}),
		PKFields:         []string{"ScopeKind", "ScopeID", "Key"},
		Scope:            resreg.ScopeSpec{Field: "ScopeID"},
		BumpVersion:      false, // per spec §"CAS implementation": exempt from config_version
		SkipApplyRebuild: true,  // enc_value blobs are set imperatively, never via apply
	})
}
