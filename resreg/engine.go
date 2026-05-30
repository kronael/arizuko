package resreg

// Schema-driven CRUD engine — spec 5/36.
//
// One reflection pass at Register() turns a Go struct's `db:` tags into
// a cached column list + scan/insert binders. Steady-state SQL is plain
// `SELECT cols FROM table` / `INSERT INTO table (cols) VALUES (?, …)`
// with no per-call reflection beyond Field(i)/Interface() — the typed
// engine writes the SQL once, reflection just walks the struct.
//
// Three transports (REST, MCP, YAML) decode into instances of the same
// RowType; the schema lives once. Drift is a compile error.
//
// Resources that need custom semantics (encryption, JSON-blob columns,
// nullable→sentinel mappings) supply Hooks. The engine still handles
// shape; hooks handle semantics.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kronael/arizuko/audit"
)

// ScopeSpec declares how DeleteScope filters rows. Field is the Go
// struct-field name carrying the home-folder string; Column is the
// SQL column to filter (defaults to the field's `db:` tag).
type ScopeSpec struct {
	Field  string
	Column string
}

// Hooks are optional per-resource semantics callbacks. All are nil-safe.
type Hooks struct {
	// BeforeInsert mutates a row pointer before INSERT (set timestamps,
	// JSON-encode blobs, encrypt). Runs inside the tx.
	BeforeInsert func(ctx context.Context, tx *sql.Tx, row any) error

	// AfterScan mutates a row pointer after SELECT (decrypt, JSON-decode).
	AfterScan func(row any) error

	// ValidateRow runs in-tx, post-decode. Returns engine-level error.
	ValidateRow func(ctx context.Context, tx *sql.Tx, row any) error

	// ColumnOverride remaps a struct field to a custom SQL expression on
	// read (Read) and/or a custom write binder (Write). Use sparingly —
	// the engine handles the strict subset; overrides are escape hatches
	// for nullable columns mapped to non-pointer Go fields and the like.
	ColumnOverride map[string]ColumnHook
}

// ColumnHook is a per-field escape hatch. Read replaces the SELECT
// expression for this column (e.g. "COALESCE(model,'')"); Write maps
// the struct field's value to the bind argument (nil-coalesce, JSON-
// encode, encrypt). Either may be empty.
type ColumnHook struct {
	Read  string
	Write func(fieldVal any) (any, error)
}

// resourceMeta is the reflection-derived schema cached at Register-time.
// Stored on Resource.meta. All steady-state CRUD reads from this struct.
type resourceMeta struct {
	rowType    reflect.Type
	fields     []fieldMeta
	pkFields   []fieldMeta
	scopeField *fieldMeta // nil if ScopeSpec unset
	columns    []string   // SELECT column list (with overrides applied)
	colsRaw    []string   // raw column names (for INSERT/DELETE)
	stampedIdx []int      // struct-field indices Diff ignores (server-stamped)
}

type fieldMeta struct {
	idx        int    // struct field index
	name       string // Go struct field name
	col        string // SQL column name (db: tag)
	readExpr   string // SELECT expression (col, or COALESCE(col,'') via override)
	writeHook  func(fieldVal any) (any, error)
	scanTarget func(rowVal reflect.Value) any // returns &row.field for sql.Rows.Scan
}

// initMeta builds resourceMeta from RowType + tags. Called at Register;
// panics on misconfiguration (caller-fault, not runtime fault).
func (r *Resource) initMeta() {
	if r.RowType == nil {
		return
	}
	rt := r.RowType
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		panic(fmt.Sprintf("resreg: %s RowType must be struct, got %s", r.Name, rt.Kind()))
	}
	if r.Table == "" {
		panic(fmt.Sprintf("resreg: %s Table is empty", r.Name))
	}
	m := &resourceMeta{rowType: rt}
	dbToField := map[string]int{}
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		tag := sf.Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		col := strings.Split(tag, ",")[0]
		fm := fieldMeta{idx: i, name: sf.Name, col: col, readExpr: col}
		idx := i
		fm.scanTarget = func(rowVal reflect.Value) any {
			return rowVal.Field(idx).Addr().Interface()
		}
		if hook, ok := r.Hooks.ColumnOverride[sf.Name]; ok {
			if hook.Read != "" {
				fm.readExpr = hook.Read
			}
			if hook.Write != nil {
				fm.writeHook = hook.Write
			}
		}
		m.fields = append(m.fields, fm)
		m.colsRaw = append(m.colsRaw, col)
		m.columns = append(m.columns, fm.readExpr)
		dbToField[sf.Name] = len(m.fields) - 1
	}
	for _, pkName := range r.PKFields {
		i, ok := dbToField[pkName]
		if !ok {
			panic(fmt.Sprintf("resreg: %s PKFields[%q] not found or missing db: tag", r.Name, pkName))
		}
		m.pkFields = append(m.pkFields, m.fields[i])
	}
	if r.Scope.Field != "" {
		i, ok := dbToField[r.Scope.Field]
		if !ok {
			panic(fmt.Sprintf("resreg: %s Scope.Field %q not in struct", r.Name, r.Scope.Field))
		}
		fm := m.fields[i]
		if r.Scope.Column != "" {
			fm.col = r.Scope.Column
		}
		m.scopeField = &fm
	}
	for _, name := range r.StampedFields {
		i, ok := dbToField[name]
		if !ok {
			panic(fmt.Sprintf("resreg: %s StampedFields %q not found or missing db: tag", r.Name, name))
		}
		m.stampedIdx = append(m.stampedIdx, m.fields[i].idx)
	}
	r.meta = m
}

// newRowPtr returns a freshly-allocated pointer to a row, used by Scan
// loops and parse paths.
func (r *Resource) newRowPtr() reflect.Value {
	return reflect.New(r.meta.rowType)
}

// ScanAll reads every row in the table, ordered by PK columns (or by
// the first column when PKFields is empty). Returns a `[]RowType`.
func (r *Resource) ScanAll(db *sql.DB) (any, error) {
	if r.meta == nil {
		return nil, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	q := "SELECT " + strings.Join(r.meta.columns, ", ") + " FROM " + r.Table + r.orderBy()
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("%s: query: %w", r.Name, err)
	}
	defer rows.Close()
	slice := reflect.MakeSlice(reflect.SliceOf(r.meta.rowType), 0, 16)
	for rows.Next() {
		ptr := r.newRowPtr()
		targets := r.scanTargets(ptr.Elem())
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", r.Name, err)
		}
		if r.Hooks.AfterScan != nil {
			if err := r.Hooks.AfterScan(ptr.Interface()); err != nil {
				return nil, fmt.Errorf("%s: after-scan: %w", r.Name, err)
			}
		}
		slice = reflect.Append(slice, ptr.Elem())
	}
	return slice.Interface(), rows.Err()
}

// Insert binds row (a struct value or pointer) and inserts via tx.
// Runs Hooks.BeforeInsert + Hooks.ValidateRow if set.
func (r *Resource) Insert(ctx context.Context, tx *sql.Tx, row any) error {
	if r.meta == nil {
		return fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	rv := reflect.ValueOf(row)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Type() != r.meta.rowType {
		return fmt.Errorf("%s: Insert wants %s, got %s", r.Name, r.meta.rowType, rv.Type())
	}
	rowPtr := reflect.New(r.meta.rowType)
	rowPtr.Elem().Set(rv)
	if r.Hooks.BeforeInsert != nil {
		if err := r.Hooks.BeforeInsert(ctx, tx, rowPtr.Interface()); err != nil {
			return fmt.Errorf("%s: before-insert: %w", r.Name, err)
		}
	}
	if r.Hooks.ValidateRow != nil {
		if err := r.Hooks.ValidateRow(ctx, tx, rowPtr.Interface()); err != nil {
			return fmt.Errorf("%s: validate: %w", r.Name, err)
		}
	}
	values, err := r.insertValues(rowPtr.Elem())
	if err != nil {
		return err
	}
	placeholders := strings.Repeat("?,", len(r.meta.fields))
	placeholders = placeholders[:len(placeholders)-1]
	q := "INSERT INTO " + r.Table + " (" + strings.Join(r.meta.colsRaw, ", ") + ") VALUES (" + placeholders + ")"
	_, err = tx.ExecContext(ctx, q, values...)
	if err != nil {
		return fmt.Errorf("%s: insert: %w", r.Name, err)
	}
	return nil
}

// InsertAll iterates a slice (RowType-shaped) and calls Insert on each
// row. Stops on first error.
func (r *Resource) InsertAll(ctx context.Context, tx *sql.Tx, rows any) error {
	if rows == nil {
		return nil
	}
	rv := reflect.ValueOf(rows)
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("%s: InsertAll wants slice, got %s", r.Name, rv.Kind())
	}
	for i := 0; i < rv.Len(); i++ {
		if err := r.Insert(ctx, tx, rv.Index(i).Interface()); err != nil {
			return err
		}
	}
	return nil
}

// DeleteScope deletes rows whose scope column equals scope. Resources
// without a ScopeSpec return an error — caller must use DeleteAll for
// global resources.
func (r *Resource) DeleteScope(ctx context.Context, tx *sql.Tx, scope string) error {
	if r.meta == nil {
		return fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	if r.meta.scopeField == nil {
		return fmt.Errorf("resreg: %s has no ScopeSpec; use DeleteAll", r.Name)
	}
	q := "DELETE FROM " + r.Table + " WHERE " + r.meta.scopeField.col + " = ?"
	_, err := tx.ExecContext(ctx, q, scope)
	if err != nil {
		return fmt.Errorf("%s: delete-scope: %w", r.Name, err)
	}
	return nil
}

// DeleteAll wipes every row in the table. Used by full-rebuild apply.
func (r *Resource) DeleteAll(ctx context.Context, tx *sql.Tx) error {
	if r.meta == nil {
		return fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM "+r.Table)
	if err != nil {
		return fmt.Errorf("%s: delete-all: %w", r.Name, err)
	}
	return nil
}

// HasScope reports whether the resource declares a clean folder-scope
// column (ScopeSpec.Field set). Apply uses this to pick scoped vs
// wholesale DELETE; resources without one rebuild wholesale.
func (r *Resource) HasScope() bool {
	return r.meta != nil && r.meta.scopeField != nil
}

// manifestScopes returns the distinct scope-column values across a
// manifest's []RowType rows (the folders the manifest mentions), in
// sorted order. Empty when the resource has no ScopeSpec or rows is nil.
func (r *Resource) manifestScopes(rows any) []string {
	if !r.HasScope() || rows == nil {
		return nil
	}
	rv := reflect.ValueOf(rows)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	seen := map[string]bool{}
	for i := 0; i < rv.Len(); i++ {
		v := rv.Index(i).Field(r.meta.scopeField.idx).Interface()
		seen[fmt.Sprintf("%v", v)] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ParseRows decodes a YAML node (sequence of mappings) into a `[]RowType`.
// Strict per spec 5/36 §"Apply lifecycle" step 1: unknown row fields
// reject (an operator typo on a field name must error, not silently
// drop). Hooks.AfterScan does NOT run on parse (it's a post-SQL hook);
// use BeforeInsert for transforms that should happen on the write path.
func (r *Resource) ParseRows(node *yaml.Node) (any, error) {
	if r.meta == nil {
		return nil, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	if node == nil {
		return reflect.MakeSlice(reflect.SliceOf(r.meta.rowType), 0, 0).Interface(), nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: expected YAML sequence, got kind %d", r.Name, node.Kind)
	}
	// yaml.v3 KnownFields lives on Decoder, not Node.Decode — round-trip
	// the node through a strict Decoder so an unknown row field rejects.
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("%s: re-encode: %w", r.Name, err)
	}
	slicePtr := reflect.New(reflect.SliceOf(r.meta.rowType))
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(slicePtr.Interface()); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", r.Name, err)
	}
	return slicePtr.Elem().Interface(), nil
}

// EmitRows marshals a `[]RowType` to a YAML node, sorted by PK string
// for deterministic output (spec 5/36 §"Canonical key order").
func (r *Resource) EmitRows(rows any) (*yaml.Node, error) {
	if r.meta == nil {
		return nil, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	rv := reflect.ValueOf(rows)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		out := &yaml.Node{Kind: yaml.SequenceNode}
		return out, nil
	}
	sorted := r.sortByPK(rv)
	node := &yaml.Node{}
	if err := node.Encode(sorted.Interface()); err != nil {
		return nil, fmt.Errorf("%s: emit: %w", r.Name, err)
	}
	return node, nil
}

// pkKey returns the lexicographic PK string for a row value, used by
// sortByPK (deterministic emit) and Diff (match rows across manifest +
// live state). When PKFields is empty, falls back to all fields — both
// callers want a total order, not just the natural key.
func (r *Resource) pkKey(v reflect.Value) string {
	fields := r.meta.pkFields
	if len(fields) == 0 {
		fields = r.meta.fields
	}
	var b strings.Builder
	for _, fm := range fields {
		fmt.Fprintf(&b, "%v|", v.Field(fm.idx).Interface())
	}
	return b.String()
}

// sortByPK returns a copy of rv with elements sorted lexicographically
// by concatenated PK string. When PKFields is empty, sorts by all
// fields concatenated — defensive determinism.
func (r *Resource) sortByPK(rv reflect.Value) reflect.Value {
	n := rv.Len()
	src := reflect.MakeSlice(rv.Type(), n, n)
	reflect.Copy(src, rv)
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		keys[i] = r.pkKey(src.Index(i))
	}
	indexes := make([]int, n)
	for i := range indexes {
		indexes[i] = i
	}
	sort.SliceStable(indexes, func(i, j int) bool { return keys[indexes[i]] < keys[indexes[j]] })
	sorted := reflect.MakeSlice(rv.Type(), n, n)
	for i, idx := range indexes {
		sorted.Index(i).Set(src.Index(idx))
	}
	return sorted
}

// orderBy returns "" or " ORDER BY col1, col2, …" for stable Scan output.
func (r *Resource) orderBy() string {
	cols := r.meta.pkFields
	if len(cols) == 0 {
		if len(r.meta.fields) == 0 {
			return ""
		}
		return " ORDER BY " + r.meta.fields[0].col
	}
	names := make([]string, len(cols))
	for i, fm := range cols {
		names[i] = fm.col
	}
	return " ORDER BY " + strings.Join(names, ", ")
}

// scanTargets returns []any of pointer addresses into rowVal's fields,
// in column order, suitable for sql.Rows.Scan.
func (r *Resource) scanTargets(rowVal reflect.Value) []any {
	out := make([]any, len(r.meta.fields))
	for i, fm := range r.meta.fields {
		out[i] = fm.scanTarget(rowVal)
	}
	return out
}

// insertValues returns []any of bind values, applying ColumnOverride.Write
// where set.
func (r *Resource) insertValues(rowVal reflect.Value) ([]any, error) {
	out := make([]any, len(r.meta.fields))
	for i, fm := range r.meta.fields {
		v := rowVal.Field(fm.idx).Interface()
		if fm.writeHook != nil {
			conv, err := fm.writeHook(v)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: write hook: %w", r.Name, fm.name, err)
			}
			out[i] = conv
		} else {
			out[i] = v
		}
	}
	return out, nil
}

// ErrVersionMismatch is returned by Apply when manifest config_version
// does not match DB config_meta.version and force is false.
var ErrVersionMismatch = errors.New("config_version mismatch")

// ApplyOpts carries the optional audit context for an apply. When
// non-nil, Apply writes exactly ONE audit_log row in the same tx
// (spec 5/36 §"CAS implementation" (3): "one audit row per apply, not
// N"). nil → no audit row (engine isolation tests against a minimal
// schema with no audit_log table).
type ApplyOpts struct {
	Actor          string // who ran the apply (CLI sub, "system", …)
	ManifestDigest string // sha256 of the manifest bytes, for forensic correlation
}

// Apply runs the scoped-rebuild lifecycle in one BEGIN IMMEDIATE tx:
// CAS check → per-resource DELETE (scoped or wholesale) → INSERT rows
// from manifest → bump config_version → one audit row. Single tx, atomic.
//
// `force` skips the CAS check; the version still advances.
//
// `manifestRows` maps Resource.Name → []RowType slice. Resources with a
// clean folder-scope column (HasScope) DELETE only the folders the
// manifest mentions (spec 5/36 §"Atomicity model": scoped DELETE+INSERT),
// so a partial manifest leaves out-of-scope rows untouched; a resource
// the manifest omits entirely is not touched. Scope-less resources
// rebuild wholesale (DeleteAll) — only when the manifest mentions them.
// SkipApplyRebuild resources (secrets) are never wiped/rebuilt; the
// version still bumps once per tx.
func Apply(ctx context.Context, db *sql.DB, manifestVersion int64, force bool, manifestRows map[string]any, opts *ApplyOpts) (int64, error) {
	// Audit counts are computed from the pre-tx snapshot (a plain read on
	// the primary connection) before BeginTx — an in-tx read would open a
	// 2nd connection that, for :memory: DBs, can't see the schema.
	var counts map[string]any
	if opts != nil {
		c, err := applyCounts(db, manifestRows)
		if err != nil {
			return 0, err
		}
		counts = c
	}
	// Take a write lock immediately. modernc.org/sqlite serializes
	// concurrent writers via SQLite's RESERVED lock; doing a `_dummy`
	// write at tx start upgrades the implicit DEFERRED tx to IMMEDIATE,
	// matching spec §"Optimistic locking" — concurrent applies block
	// rather than racing. Cheap: one no-op row in config_meta.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE config_meta SET version = version"); err != nil {
		return 0, fmt.Errorf("acquire write lock: %w", err)
	}
	var current int64
	if err := tx.QueryRowContext(ctx, "SELECT version FROM config_meta").Scan(&current); err != nil {
		return 0, fmt.Errorf("read config_version: %w", err)
	}
	if !force && current != manifestVersion {
		return current, fmt.Errorf("%w: db=%d manifest=%d", ErrVersionMismatch, current, manifestVersion)
	}
	for _, r := range All() {
		if r.RowType == nil || r.SkipApplyRebuild {
			continue
		}
		rows, mentioned := manifestRows[r.Name]
		if !mentioned {
			continue // resource absent from manifest → untouched
		}
		if r.HasScope() {
			for _, scope := range r.manifestScopes(rows) {
				if err := r.DeleteScope(ctx, tx, scope); err != nil {
					return current, err
				}
			}
		} else if err := r.DeleteAll(ctx, tx); err != nil {
			return current, err
		}
		if err := r.InsertAll(ctx, tx, rows); err != nil {
			return current, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE config_meta SET version = version + 1"); err != nil {
		return current, fmt.Errorf("bump config_version: %w", err)
	}
	var newVer int64
	if err := tx.QueryRowContext(ctx, "SELECT version FROM config_meta").Scan(&newVer); err != nil {
		return current, fmt.Errorf("read new config_version: %w", err)
	}
	if opts != nil {
		if err := emitApplyAudit(ctx, tx, opts, counts, newVer); err != nil {
			return current, fmt.Errorf("audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return current, fmt.Errorf("commit: %w", err)
	}
	return newVer, nil
}

// applyCounts diffs the manifest against the pre-tx live DB for every
// mentioned, rebuildable resource and returns per-resource
// {add,update,delete} counts for the audit summary. Read-only.
func applyCounts(db *sql.DB, manifestRows map[string]any) (map[string]any, error) {
	counts := map[string]any{}
	for _, r := range All() {
		if r.RowType == nil || r.SkipApplyRebuild {
			continue
		}
		rows, mentioned := manifestRows[r.Name]
		if !mentioned {
			continue
		}
		d, err := r.Diff(db, rows)
		if err != nil {
			return nil, err
		}
		counts[r.Name] = map[string]any{
			"add": len(d.Add), "update": len(d.Update), "delete": len(d.Remove),
		}
	}
	return counts, nil
}

// emitApplyAudit writes the single per-apply summary row in-tx
// (spec 5/36 §"CAS implementation" (3)): actor, manifest digest,
// per-resource add/update/delete counts, final config_version.
func emitApplyAudit(ctx context.Context, tx *sql.Tx, opts *ApplyOpts, counts map[string]any, newVer int64) error {
	return audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategoryMutation,
		Action:   "config.apply",
		Actor:    opts.Actor,
		Surface:  audit.SurfaceCLI,
		Resource: "manifest",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"manifest_digest": opts.ManifestDigest,
			"config_version":  newVer,
			"resources":       counts,
		},
	})
}

// ConfigVersion returns the current config_meta.version, or 0 if the
// table is empty / missing.
func ConfigVersion(db *sql.DB) (int64, error) {
	var v int64
	err := db.QueryRow("SELECT version FROM config_meta").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}

// Export reads every registered resource via ScanAll and assembles a
// manifest map keyed by Resource.Name. Includes "config_version" at
// the top level. Deterministic per canonical key order.
func Export(db *sql.DB) (map[string]any, error) {
	ver, err := ConfigVersion(db)
	if err != nil {
		return nil, fmt.Errorf("config_version: %w", err)
	}
	out := map[string]any{
		"config_version": ver,
	}
	for _, r := range All() {
		if r.RowType == nil {
			continue
		}
		rows, err := r.ScanAll(db)
		if err != nil {
			return nil, err
		}
		out[r.Name] = rows
	}
	return out, nil
}

// GetResource emits a single-resource manifest fragment: a map with only
// the named resource's rows (live `SELECT *`), shaped exactly as Export
// would nest it so the fragment re-applies to a no-op (spec 5/36
// §"arizuko get round-trip"). Returns an error for unknown resources or
// resources without a RowType. No config_version is stamped — a fragment
// is scoped, not a full dump.
func GetResource(db *sql.DB, name string) (map[string]any, error) {
	r := Lookup(name)
	if r == nil || r.RowType == nil {
		return nil, fmt.Errorf("resreg: unknown or schema-less resource %q", name)
	}
	rows, err := r.ScanAll(db)
	if err != nil {
		return nil, err
	}
	return map[string]any{name: rows}, nil
}

// ResourceDelta is one resource's plan summary: PK strings sorted into
// add (in manifest, not in DB), update (PK present in both, payload
// differs), unchanged (PK + payload identical), and remove (in DB, not
// in manifest). Empty when the manifest omits the resource AND the DB
// has no rows for it. SkipApplyRebuild marks resources (secrets) whose
// add/update/remove are informational only — apply never mutates them,
// so Changed() ignores the lists for these.
type ResourceDelta struct {
	Resource         string
	Add              []string
	Update           []string
	Unchanged        []string
	Remove           []string
	SkipApplyRebuild bool
}

// Changed reports whether the delta would mutate any row. SkipApplyRebuild
// resources never mutate via apply, so they never report a change — plan
// and apply must agree (spec 5/36 §"Secret safety": plan shows secrets as
// set/unset, not actionable +/~/- deltas).
func (d ResourceDelta) Changed() bool {
	if d.SkipApplyRebuild {
		return false
	}
	return len(d.Add) > 0 || len(d.Update) > 0 || len(d.Remove) > 0
}

// Diff computes the ResourceDelta for one resource: manifestRows (a
// []RowType slice, or nil) vs the live table. Matching is by PK string;
// payload equality ignores server-stamped fields (StampedFields) so a
// hand-written manifest omitting timestamps reads as unchanged.
//
// Scope-aware (matches scoped apply): for a scoped resource, only live
// rows within the folders the manifest mentions are candidates for
// Remove — apply leaves out-of-scope rows alone, so plan must too
// (spec 5/36 §"Surface": plan shows what a scoped restore would change).
// Scope-less resources rebuild wholesale, so every live row is in scope.
//
// SkipApplyRebuild resources still diff (so `plan`/`get` report metadata)
// — the apply path is what skips the write, not the diff.
func (r *Resource) Diff(db *sql.DB, manifestRows any) (ResourceDelta, error) {
	d := ResourceDelta{Resource: r.Name, SkipApplyRebuild: r.SkipApplyRebuild}
	if r.meta == nil {
		return d, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	live, err := r.ScanAll(db)
	if err != nil {
		return d, err
	}
	liveByPK := r.byPK(reflect.ValueOf(live))
	manByPK := map[string]reflect.Value{}
	if manifestRows != nil {
		rv := reflect.ValueOf(manifestRows)
		if rv.Kind() == reflect.Slice {
			manByPK = r.byPK(rv)
		}
	}
	for pk, mrow := range manByPK {
		lrow, ok := liveByPK[pk]
		switch {
		case !ok:
			d.Add = append(d.Add, pk)
		case r.payloadEqual(mrow, lrow):
			d.Unchanged = append(d.Unchanged, pk)
		default:
			d.Update = append(d.Update, pk)
		}
	}
	inScope := r.scopeFilter(manifestRows)
	for pk, lrow := range liveByPK {
		if _, ok := manByPK[pk]; ok {
			continue
		}
		if inScope == nil || inScope[r.rowScope(lrow)] {
			d.Remove = append(d.Remove, pk)
		}
	}
	sort.Strings(d.Add)
	sort.Strings(d.Update)
	sort.Strings(d.Unchanged)
	sort.Strings(d.Remove)
	return d, nil
}

// scopeFilter returns the set of folder scopes the manifest mentions for
// a scoped resource, or nil for a scope-less resource (nil == every live
// row is in scope, wholesale rebuild). A scoped resource with zero
// manifest rows returns an empty (non-nil) set → nothing is in scope, so
// no Remove fires, matching apply (scoped DELETE touches no folder).
func (r *Resource) scopeFilter(manifestRows any) map[string]bool {
	if !r.HasScope() {
		return nil
	}
	set := map[string]bool{}
	for _, s := range r.manifestScopes(manifestRows) {
		set[s] = true
	}
	return set
}

// rowScope returns a live row's scope-column value as a string.
func (r *Resource) rowScope(v reflect.Value) string {
	return fmt.Sprintf("%v", v.Field(r.meta.scopeField.idx).Interface())
}

// payloadEqual compares two rows for Diff, treating server-stamped
// fields (StampedFields) as equal regardless of value. A hand-written
// manifest leaves created_at/granted_at/added_at empty; the live row
// carries the stamp — without this they'd diff as a phantom update on
// every plan (spec 5/36 §"Apply lifecycle" step 3).
func (r *Resource) payloadEqual(a, b reflect.Value) bool {
	if len(r.meta.stampedIdx) == 0 {
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
	ac := reflect.New(r.meta.rowType).Elem()
	ac.Set(a)
	bc := reflect.New(r.meta.rowType).Elem()
	bc.Set(b)
	zero := reflect.Zero(r.meta.rowType)
	for _, idx := range r.meta.stampedIdx {
		ac.Field(idx).Set(zero.Field(idx))
		bc.Field(idx).Set(zero.Field(idx))
	}
	return reflect.DeepEqual(ac.Interface(), bc.Interface())
}

// byPK indexes a []RowType slice by pkKey. Last-writer-wins on duplicate
// PKs (the manifest parser rejects in-file dupes upstream).
func (r *Resource) byPK(rv reflect.Value) map[string]reflect.Value {
	out := map[string]reflect.Value{}
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return out
	}
	for i := 0; i < rv.Len(); i++ {
		out[r.pkKey(rv.Index(i))] = rv.Index(i)
	}
	return out
}

// Plan diffs a parsed manifest (Resource.Name → []RowType, plus a
// "config_version" key Diff ignores) against the live DB for every
// resource the manifest mentions, in catalog order. Non-mutating. Backs
// `arizuko plan` (spec 5/36 §"Apply lifecycle" step 3).
//
// Resources the manifest omits are skipped — apply leaves them untouched
// (scoped restore: absent scopes are not deleted), so plan reports no
// delta for them and stays exactly equal to apply.
func Plan(db *sql.DB, manifest map[string]any) ([]ResourceDelta, error) {
	var out []ResourceDelta
	for _, r := range All() {
		if r.RowType == nil {
			continue
		}
		rows, ok := manifest[r.Name]
		if !ok {
			continue
		}
		d, err := r.Diff(db, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// EmitYAML writes the manifest map (from Export) as a deterministic YAML
// document. Top-level keys sort: config_version first, then resource
// keys lexicographic. Per-resource rows are sorted by PK via EmitRows.
func EmitYAML(manifest map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(manifest))
	for k := range manifest {
		if k != "config_version" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	root := &yaml.Node{Kind: yaml.MappingNode}
	if v, ok := manifest["config_version"]; ok {
		vn := &yaml.Node{}
		if err := vn.Encode(v); err != nil {
			return nil, err
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "config_version"}, vn)
	}
	resByName := map[string]*Resource{}
	for _, r := range All() {
		resByName[r.Name] = r
	}
	for _, k := range keys {
		var valNode *yaml.Node
		if r, ok := resByName[k]; ok && r.RowType != nil {
			n, err := r.EmitRows(manifest[k])
			if err != nil {
				return nil, err
			}
			valNode = n
		} else {
			n := &yaml.Node{}
			if err := n.Encode(manifest[k]); err != nil {
				return nil, err
			}
			valNode = n
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k}, valNode)
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	enc.Close()
	return buf.Bytes(), nil
}

// ParseYAML decodes a manifest document into a map of resource name →
// []RowType slice. The reserved key "config_version" stays in the map
// as int64. Strict per spec 5/36 §"Apply lifecycle" step 1: an unknown
// top-level key (a typo'd resource name) rejects before the DB is
// touched, so an operator's intended config can't silently fail to apply.
func ParseYAML(data []byte) (map[string]any, int64, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = *doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, 0, fmt.Errorf("manifest root must be a mapping")
	}
	out := map[string]any{}
	var version int64
	resByName := map[string]*Resource{}
	for _, r := range All() {
		resByName[r.Name] = r
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		val := doc.Content[i+1]
		if key == "config_version" {
			if err := val.Decode(&version); err != nil {
				return nil, 0, fmt.Errorf("config_version: %w", err)
			}
			out[key] = version
			continue
		}
		r, ok := resByName[key]
		if !ok || r.RowType == nil {
			return nil, 0, fmt.Errorf("unknown resource key %q (line %d)", key, doc.Content[i].Line)
		}
		rows, err := r.ParseRows(val)
		if err != nil {
			return nil, 0, err
		}
		out[key] = rows
	}
	return out, version, nil
}

// --- registry ---------------------------------------------------------

var registry []*Resource

// Register adds r to the package-level registry. Idempotent on Name;
// re-registering replaces the prior entry (test ergonomics). Initialises
// schema cache (panics on misconfiguration — caller fault, surfaces at
// process start, not at first query).
func Register(r Resource) *Resource {
	cp := r
	cp.initMeta()
	for i, existing := range registry {
		if existing.Name == r.Name {
			registry[i] = &cp
			return &cp
		}
	}
	registry = append(registry, &cp)
	return &cp
}

// All returns the registered resources in registration order.
func All() []*Resource {
	out := make([]*Resource, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the resource registered under name, or nil.
func Lookup(name string) *Resource {
	for _, r := range registry {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// reset clears the registry. Test-only.
func reset() {
	registry = nil
}
