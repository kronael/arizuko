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
// Apply path: this resource's DELETE+INSERT cycle rebuilds the
// metadata triples. Because the actual `enc_value` BLOB column is
// NOT in this struct, the engine's INSERT statement omits it — and
// SQLite's `enc_value BLOB NOT NULL` constraint makes apply REJECT
// new triples that don't already have a blob. This is by design: an
// operator declares the SHAPE in YAML, then sets the blob out-of-
// band. New triples without a blob = error, caught by the schema.
//
// For now, apply preserves the blob across delete+insert by ALSO
// emitting `enc_value` in INSERT as a SELECT subquery — but that's a
// later refinement. v1 ships the metadata round-trip and excludes
// secrets from the `config_meta` count (spec §"CAS implementation"),
// so rotating secrets doesn't invalidate manifests.
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
