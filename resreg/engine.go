package resreg

// Schema-driven CRUD engine — spec 5/8.
//
// One reflection pass at Register() turns a Go struct's `db:` tags into a cached
// column list plus scan/insert binders; steady-state SQL is plain SELECT/INSERT.
// Three transports (REST, MCP, YAML) decode into the same RowType, so drift is a
// compile error. Resources needing custom semantics (encryption, JSON-blob
// columns, nullable→sentinel) supply Hooks: the engine handles shape, hooks
// handle semantics.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
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
	// BeforeInsert fills a row's declared defaults before it is stored — the
	// value an operator's manifest may omit, like `effect='allow'` on an acl row
	// or `status='active'` on a scheduled task.
	//
	// It runs where the item is rendered (entity.go), and it takes the ROW and
	// nothing else: an apply holds a claim rather than a transaction, so a hook
	// asking for a *sql.Tx would be a declaration nothing could honour. A default
	// the storage layer applies is the writer's, exactly as a stamp is
	// (StampedFields), and the declaration is what tells a manifest the field is
	// optional (spec 6/40 §"What a registered resource still needs", answer 5).
	BeforeInsert func(row any) error

	// AfterScan mutates a row pointer after SELECT (decrypt, JSON-decode).
	AfterScan func(row any) error

	// ValidateRows refuses a manifest whose row SET an apply must not store —
	// the invariant that is a property of the whole set rather than of one row,
	// which is why it takes the set and not a row.
	//
	// It runs BEFORE the delete that precedes the writes, so a refused manifest
	// leaves the subsystem untouched: an apply has no pre-image and no rollback,
	// so a check that ran between the wipe and the puts would leave an operator
	// with the rows deleted and none written (entity.go applyEntities).
	//
	// The set it checks is the one the apply is about to make the resource EQUAL
	// TO — an apply replaces a resource's manifest-visible projection wholesale
	// (RowFilter names what is outside it), so the graph, the counter or the
	// uniqueness the hook tests is the manifest's own. It takes no transaction
	// for the same reason BeforeInsert does not: an apply holds a claim, and a
	// hook that needed a *sql.Tx would be a declaration nothing could honour.
	ValidateRows func(ctx context.Context, rows any) error

	// ColumnOverride remaps a struct field to a custom SQL expression on
	// read (Read) and/or a custom write binder (Write). Use sparingly —
	// the engine handles the strict subset; overrides are escape hatches
	// for nullable columns mapped to non-pointer Go fields and the like.
	ColumnOverride map[string]ColumnHook
}

// ColumnHook is a per-field escape hatch. Read replaces the SELECT
// expression for this column (e.g. "COALESCE(model,”)"); Write maps
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
	stampedIdx []int      // struct-field indices Diff ignores (server-stamped)
	// autoStampIdx is the subset of stampedIdx the writer may FILL with now(). A
	// stamped field that has already declared what its empty value stores is not
	// one of them, because inventing a value would overwrite a deliberate absence.
	// Server-OWNED and server-INVENTED are different claims (BUGS F49).
	autoStampIdx []int
	// entity is the item metadata, non-nil for exactly the resources that declared
	// EntitySortKey. It IS the shape switch ScanAll branches on, and the
	// declaration Apply requires of every resource in a subsystem (entity.go).
	entity *entityMeta
}

type fieldMeta struct {
	idx        int    // struct field index
	name       string // Go struct field name
	col        string // SQL column name (db: tag)
	readExpr   string // SELECT expression (col, or COALESCE(col,'') via override)
	writeHook  func(fieldVal any) (any, error)
	scanTarget func(rowVal reflect.Value) any // returns &row.field for sql.Rows.Scan
	// codec is the declared encoding of a field whose Go type is not text, nil
	// for one that needs none. It is the rendering the engine writes a value
	// through and the one it reads the value back through, so a stored text has a
	// single meaning (codec.go).
	codec *codec
}

// initMeta builds resourceMeta from RowType + tags. Called at Register;
// panics on misconfiguration (caller-fault, not runtime fault).
func (r *Resource) initMeta() {
	r.checkMCPArgTypes()
	r.checkRowFilter()
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
		col, encoding, _ := strings.Cut(tag, ",")
		fm := fieldMeta{idx: i, name: sf.Name, col: col, readExpr: col,
			codec: codecFor(r.Name, sf.Name, encoding, sf.Type.Kind())}
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
	absent := nameSet(r.AbsentStamps)
	for _, name := range r.StampedFields {
		i, ok := dbToField[name]
		if !ok {
			panic(fmt.Sprintf("resreg: %s StampedFields %q not found or missing db: tag", r.Name, name))
		}
		m.stampedIdx = append(m.stampedIdx, m.fields[i].idx)
		if m.fields[i].writeHook == nil && !absent[name] {
			m.autoStampIdx = append(m.autoStampIdx, m.fields[i].idx)
		}
	}
	for _, name := range r.AbsentStamps {
		if !slices.Contains(r.StampedFields, name) {
			panic(fmt.Sprintf("resreg: %s names %q in AbsentStamps and not in StampedFields; "+
				"the second is the subset of the first the writer does not invent, and a "+
				"field nobody stamps has no invention to refuse", r.Name, name))
		}
	}
	r.initEntity(m, dbToField)
	if m.entity == nil {
		for _, fm := range m.fields {
			if fm.codec != nil && fm.codec.name == padCodec {
				// The padding renders a KEY, and a column-shaped resource has no
				// key to render — it has an INTEGER column, and the codec's output
				// IS the bind value (codec.go).
				panic(fmt.Sprintf("resreg: %s.%s declares the %q encoding and %s is "+
					"column-backed; padding orders a sort key, and this resource has none",
					r.Name, fm.name, padCodec, r.Name))
			}
		}
	}
	r.meta = m
}

func (r *Resource) newRowPtr() reflect.Value {
	return reflect.New(r.meta.rowType)
}

// Querier is the read subset both *sql.DB and *sql.Tx satisfy, so a caller can
// read via the live DB or via an in-flight *sql.Tx — Apply's in-transaction CAS
// recheck must see exactly the snapshot it is about to write against (spec 5/8
// §"Content-hash CAS").
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// filterSQL renders the declared RowFilter as a bound SQL expression: the
// conjunction and the values it compares against, so a filter value reaches the
// engine as a BIND and never as text spliced into a statement.
//
// It is the column path's reading of the one declaration the object path passes
// straight to Query.Where. The operator set is the one Register admits, so a
// term this cannot render cannot have been registered.
func (r *Resource) filterSQL() (string, []any) {
	if len(r.RowFilter) == 0 {
		return "", nil
	}
	terms := make([]string, len(r.RowFilter))
	args := make([]any, len(r.RowFilter))
	for i, c := range r.RowFilter {
		op := "="
		if c.Op == objstore.Ne {
			op = "<>"
		}
		terms[i] = c.Attr + " " + op + " ?"
		args[i] = c.Value
	}
	return strings.Join(terms, " AND "), args
}

// whereFilter opens a statement's WHERE with the RowFilter, or adds nothing.
// ScanAll reaches it, so a resource's excluded rows stay out of every
// manifest-visible read.
func (r *Resource) whereFilter() (string, []any) {
	expr, args := r.filterSQL()
	if expr == "" {
		return "", nil
	}
	return " WHERE (" + expr + ")", args
}

// checkRowFilter refuses a filter no reader can honour, at Register.
//
// The operators are Eq and Ne because those are what both shapes answer alike
// and what the two declared filters ask for. An attribute name is spliced into
// SQL rather than bound — it is a column name, which no dialect binds — so it
// has to be an identifier and nothing else.
func (r *Resource) checkRowFilter() {
	for _, c := range r.RowFilter {
		if c.Op != objstore.Eq && c.Op != objstore.Ne {
			panic(fmt.Sprintf("resreg: %s filters on %s with comparison %d; a row filter is "+
				"Eq or Ne, which is what the column path renders and the object path passes "+
				"to Query.Where", r.Name, c.Attr, c.Op))
		}
		if !plainIdent(c.Attr) {
			panic(fmt.Sprintf("resreg: %s filters on %q, which is not a column name; the "+
				"attribute is spliced into SQL because no dialect binds an identifier",
				r.Name, c.Attr))
		}
	}
}

// plainIdent reports whether s is a bare column name: a letter or underscore,
// then letters, digits and underscores.
func plainIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, ch := range s {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '_':
		case i > 0 && ch >= '0' && ch <= '9':
		default:
			return false
		}
	}
	return true
}

// ScanAll reads every row of the resource and returns a `[]RowType`.
//
// It is the SINGLE row reader every verb goes through, and the one place the
// two shapes part: a resource that declared EntitySortKey is read as items and
// one that did not is read as columns (entity.go). The branch is on the
// DECLARATION, so which statement runs is decided at registration and not by
// anything the storage says back.
//
// On the column path the order is PK columns (or the first column when PKFields
// is empty) and RowFilter-excluded rows never appear. On the object path the
// order is the sort key's; EmitRows sorts by PK before emitting either way, so
// the checksum does not see the difference.
func (r *Resource) ScanAll(be Backend) (any, error) { return r.scanAll(be.read()) }

// scanAll is ScanAll over the handle one read holds, so Apply's in-transaction
// recheck reads the same rows through its own.
func (r *Resource) scanAll(rd reader) (any, error) {
	if r.meta == nil {
		return nil, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	if r.meta.entity == nil {
		return nil, fmt.Errorf("resreg: %s declares no EntitySortKey, so nothing can read it", r.Name)
	}
	return r.scanEntities(rd)
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

// Retarget returns a copy of rows with every row's Scope.Field column set to
// newFolder — the engine-owned building block for "export folder X, apply as
// folder Y" (spec 5/8 §"Path-retargeting apply"). Apply derives its DELETE+INSERT
// scope from the rows' CURRENT Scope.Field, so a retargeted row lands under
// newFolder with zero change to Apply.
//
// Deliberately narrow: it touches ONLY the column ScopeSpec.Field names, and
// errors for a resource with no declared Scope rather than guessing which column
// is the folder. A resource WITH a declared Scope.Field can still embed the
// folder elsewhere in row content (web_routes.redirect_to) — a named gap the
// caller must rewrite itself.
func (r *Resource) Retarget(rows any, newFolder string) (any, error) {
	if !r.HasScope() {
		return nil, fmt.Errorf("resreg: %s has no ScopeSpec; Retarget refuses to guess which field is the folder", r.Name)
	}
	rv := reflect.ValueOf(rows)
	if !rv.IsValid() {
		return rows, nil
	}
	if rv.Kind() != reflect.Slice {
		return nil, fmt.Errorf("resreg: %s Retarget wants a slice, got %s", r.Name, rv.Kind())
	}
	out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	for i := 0; i < rv.Len(); i++ {
		row := reflect.New(rv.Type().Elem()).Elem()
		row.Set(rv.Index(i))
		field := row.Field(r.meta.scopeField.idx)
		if field.Kind() != reflect.String {
			return nil, fmt.Errorf("resreg: %s Scope.Field %q is not a string column", r.Name, r.meta.scopeField.name)
		}
		field.SetString(newFolder)
		out.Index(i).Set(row)
	}
	return out.Interface(), nil
}

// rowSlice coerces rows to a reflect.Value of `[]RowType`. A nil `rows`
// becomes an empty slice, so a caller holding "the resource this manifest
// never mentioned" needs no special case.
func (r *Resource) rowSlice(rows any) (reflect.Value, error) {
	if r.meta == nil {
		return reflect.Value{}, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	if rows == nil {
		return reflect.MakeSlice(reflect.SliceOf(r.meta.rowType), 0, 0), nil
	}
	rv := reflect.ValueOf(rows)
	if rv.Kind() != reflect.Slice || rv.Type().Elem() != r.meta.rowType {
		return reflect.Value{}, fmt.Errorf("resreg: %s wants a []%s, got %T", r.Name, r.meta.rowType, rows)
	}
	return rv, nil
}

// Scopes returns the distinct scope values across rows, or nil when the resource
// declares no ScopeSpec. A caller that REMOVES rows needs it: a scoped resource
// derives the folders it DELETEs from the rows the document still mentions, so
// dropping the last row in a folder makes that folder vanish from the document
// and apply deletes nothing there. `packages remove` hit exactly that — a
// package's egress rule was the only row in its folder, so remove reported
// success and left the rule live. Feed these into ApplyOpts.PruneScopes.
func (r *Resource) Scopes(rows any) []string { return r.manifestScopes(rows) }

// PKs returns the primary-key string of every row in rows (a `[]RowType`), in
// slice order. This is the identity `arizuko packages install` records, so a
// later `remove` names exactly the rows the install added and nothing else.
func (r *Resource) PKs(rows any) ([]string, error) {
	rv, err := r.rowSlice(rows)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, rv.Len())
	for i := range rv.Len() {
		out = append(out, r.pkKey(rv.Index(i)))
	}
	return out, nil
}

// MergeRows returns base with overlay's rows added, overlay winning on a PK
// collision. Apply rebuilds a resource from the document it is handed, so a
// caller holding a FRAGMENT merges it onto the live projection here and hands
// Apply the complete document. That keeps ONE apply path with its transaction and
// pre-image rollback (BUGS L44).
func (r *Resource) MergeRows(base, overlay any) (any, error) {
	bv, err := r.rowSlice(base)
	if err != nil {
		return nil, err
	}
	ov, err := r.rowSlice(overlay)
	if err != nil {
		return nil, err
	}
	replaced := map[string]bool{}
	for i := range ov.Len() {
		replaced[r.pkKey(ov.Index(i))] = true
	}
	out := reflect.MakeSlice(bv.Type(), 0, bv.Len()+ov.Len())
	for i := range bv.Len() {
		if replaced[r.pkKey(bv.Index(i))] {
			continue
		}
		out = reflect.Append(out, bv.Index(i))
	}
	return reflect.AppendSlice(out, ov).Interface(), nil
}

// DropRows returns base (a `[]RowType`) without the rows whose PK string is in
// pks — the inverse of MergeRows, and how `packages remove` subtracts exactly
// the rows the installed-package record names from the live projection.
func (r *Resource) DropRows(base any, pks []string) (any, error) {
	bv, err := r.rowSlice(base)
	if err != nil {
		return nil, err
	}
	drop := make(map[string]bool, len(pks))
	for _, pk := range pks {
		drop[pk] = true
	}
	out := reflect.MakeSlice(bv.Type(), 0, bv.Len())
	for i := range bv.Len() {
		if drop[r.pkKey(bv.Index(i))] {
			continue
		}
		out = reflect.Append(out, bv.Index(i))
	}
	return out.Interface(), nil
}

// ParseRows decodes a YAML node (sequence of mappings) into a `[]RowType`.
// Strict per spec 5/8 §"Apply lifecycle" step 1: unknown row fields
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
	slicePtr := reflect.New(reflect.SliceOf(r.meta.rowType))
	if err := decodeStrict(node, slicePtr.Interface()); err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name, err)
	}
	return slicePtr.Elem().Interface(), nil
}

// decodeStrict decodes node into out, refusing an unknown field. yaml.v3
// KnownFields lives on Decoder, not Node.Decode, so the node round-trips
// through a strict Decoder.
func decodeStrict(node *yaml.Node, out any) error {
	raw, err := yaml.Marshal(node)
	if err != nil {
		return fmt.Errorf("re-encode: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}

// EmitRows marshals a `[]RowType` to a YAML node, sorted by PK string
// for deterministic output (spec 5/8 §"Canonical key order").
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
	for i := range n {
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

// scanTargets returns []any of pointer addresses into rowVal's fields, in
// column order, suitable for sql.Rows.Scan — and the function that finishes the
// row.
//
// A codec'd field is scanned into a STAGING sql.NullString and decoded after,
// because a driver hands back the stored text and only the codec knows the
// value it stands for. NULL and the empty string both leave the zero value,
// which is the one null this model has and the same answer the object path's
// absent attribute gives (codec.go).
func (r *Resource) scanTargets(rowVal reflect.Value) ([]any, func() error) {
	out := make([]any, len(r.meta.fields))
	var staged []func() error
	for i, fm := range r.meta.fields {
		if fm.codec == nil {
			out[i] = fm.scanTarget(rowVal)
			continue
		}
		text := new(sql.NullString)
		out[i] = text
		field, dec, col := rowVal.Field(fm.idx), fm.codec.decode, fm.col
		staged = append(staged, func() error {
			if err := dec(text.String, field); err != nil {
				return fmt.Errorf("%s: %w", col, err)
			}
			return nil
		})
	}
	return out, func() error {
		for _, fn := range staged {
			if err := fn(); err != nil {
				return err
			}
		}
		return nil
	}
}

// ErrChecksumMismatch is returned by Apply when the manifest's declared
// checksum does not match the live DB's recomputed content hash and force
// is false.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// --- the missing-group rule (spec 5/8 §"The missing-group rule") ---------

// ErrMissingGroup is returned by ValidateFolderRefs when a manifest row
// references a folder that is neither declared as a groups row in the
// manifest set nor already live in the target DB.
var ErrMissingGroup = errors.New("references a folder that is not a group")

// GroupsResource names the resource whose rows DEFINE which folders exist.
// Every other folder reference is checked against it.
const GroupsResource = "groups"

// KnownFolders returns every folder that will exist once manifests apply: the
// groups rows the manifests declare, plus the groups already live in q. Both
// halves read through the groups resource's own ScopeSpec.Field column, so "what
// is a folder" has one definition here.
//
// Pass every document of the restore, not one: a folder-scoped row may sit in a
// different document from the groups row that declares it.
func KnownFolders(be Backend, manifests ...map[string]any) (map[string]bool, error) {
	g := Lookup(GroupsResource)
	if g == nil {
		return nil, fmt.Errorf("resreg: %s resource is not registered", GroupsResource)
	}
	live, err := g.scanAll(be.read())
	if err != nil {
		return nil, fmt.Errorf("read live %s: %w", GroupsResource, err)
	}
	out := map[string]bool{}
	for _, f := range g.manifestScopes(live) {
		out[f] = true
	}
	for _, m := range manifests {
		for _, f := range g.manifestScopes(m[GroupsResource]) {
			out[f] = true
		}
	}
	return out, nil
}

// ValidateFolderRefs is the missing-group rule: refuse a manifest document whose
// rows name a folder absent from known, BEFORE any transaction opens. The two
// FK'd references would fail SQLite's own check mid-write; the ones with no FK —
// network_rules — produce a silently orphaned row instead, which is the gap this
// closes.
//
// Checks exactly the references that are DECLARATIVELY folders: a resource's
// ScopeSpec.Field column. It does not guess which other string column holds a
// folder, for the same reason Retarget refuses to. A resource that CAN decide
// that for its own polymorphic column adds a Hooks.ValidateRows.
//
// The empty scope is exempt: network_rules carries folder="" for instance-global
// rules, which names no group by design.
func ValidateFolderRefs(subsystem string, manifest map[string]any, known map[string]bool) error {
	var bad []string
	for _, r := range BySubsystem(subsystem) {
		if !r.HasScope() {
			continue
		}
		rows, mentioned := manifest[r.Name]
		if !mentioned {
			continue
		}
		for _, folder := range r.manifestScopes(rows) {
			if folder == "" || known[folder] {
				continue
			}
			bad = append(bad, r.Name+" -> "+folder)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("%s %w: %s", subsystem, ErrMissingGroup, strings.Join(bad, ", "))
}

// ApplyOpts carries the optional audit context for an apply. When
// non-nil, Apply writes exactly ONE audit_log row in the same tx
// (spec 5/8 §"CAS implementation" (3): "one audit row per apply, not
// N"). nil → no audit row (engine isolation tests against a minimal
// schema with no audit_log table).
type ApplyOpts struct {
	Actor          string // who ran the apply (CLI sub, "system", …)
	ManifestDigest string // sha256 of the manifest bytes, for forensic correlation

	// PruneScopes names extra scope values every scoped resource DELETEs before
	// inserting. Exists for the cross-subsystem rollback: a forward apply that
	// CREATED rows under a folder the pre-image never mentioned leaves nothing for
	// the pre-image's own scopes to prune, so those rows would survive a rollback
	// that is supposed to be total. Sound only because a pre-image is a COMPLETE
	// subsystem projection.
	PruneScopes []string

	// Extra carries caller context into the apply's single audit row. `arizuko
	// packages` puts the package name and the PK of every row it added or withdrew
	// there: one row saying only "2 acl added" hides WHICH authority the install
	// handed out (BUGS Q5/F2/L36). Keep values short — the summary caps at 512
	// bytes. The three engine keys always win a name collision.
	Extra map[string]any

	// TakeOver finishes an entity-backed subsystem whose claim a DIFFERENT
	// apply left behind, for the operator who cannot run that apply again.
	//
	// The ordinary recovery needs nothing here: an apply of the same manifest
	// carries the same intent, so it takes its own claim over and rolls forward
	// (objstore.Claim). This is the other case, and it costs what that one does
	// not — the rows the interrupted apply was writing OUTSIDE this manifest's
	// delete set stay part-written, and the scope is settled over them. Column
	// shapes ignore it: they roll back rather than forward.
	TakeOver bool
}

// ManifestScopes returns every scope value a manifest's rows mention across
// subsystem's scoped resources, sorted and deduplicated. The rollback feeds
// this back as ApplyOpts.PruneScopes; nothing else needs it, which is why the
// per-resource manifestScopes stays unexported.
func ManifestScopes(subsystem string, manifest map[string]any) []string {
	seen := map[string]bool{}
	for _, r := range BySubsystem(subsystem) {
		rows, mentioned := manifest[r.Name]
		if !mentioned || !r.HasScope() {
			continue
		}
		for _, s := range r.manifestScopes(rows) {
			seen[s] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Checksum computes the content hash of one subsystem's manifest-visible
// projection: sha256 of the canonical EmitYAML bytes of Export, minus the
// checksum field itself. Recomputed fresh from live rows at read time: no
// counter, no table, nothing for a writer to remember to bump.
func Checksum(be Backend, subsystem string) (string, error) {
	return checksum(be.read(), subsystem)
}

// checksum is Checksum over the handles one read holds — Apply's in-transaction
// recheck and its post-write re-read reach it through the *sql.Tx they write in.
func checksum(rd reader, subsystem string) (string, error) {
	m, err := exportRows(rd, subsystem)
	if err != nil {
		return "", err
	}
	return hashManifest(m)
}

// Apply runs the scoped-rebuild lifecycle for ONE subsystem (one owner DB) in one
// tx: in-tx checksum recheck → per-resource DELETE → INSERT rows from manifest →
// recompute checksum → one audit row. Atomic. Returns the new checksum. `force`
// skips the checksum check.
//
// Every key of `manifestRows` must name a resource registered with Resource.DB ==
// subsystem. Resources with a clean folder-scope column DELETE only the folders
// the manifest mentions, so a partial manifest leaves out-of-scope rows
// untouched; a resource the manifest omits is not touched at all. Scope-less
// resources rebuild wholesale. SkipApplyRebuild resources (secrets, route_tokens,
// invites) are never wiped. RowFilter-excluded rows never enter the checksum, the
// DELETE, or the recomputed checksum.
func Apply(ctx context.Context, be Backend, subsystem string, wantChecksum string, force bool, manifestRows map[string]any, opts *ApplyOpts) (string, error) {
	if err := requireEntities(subsystem); err != nil {
		return "", err
	}
	for name := range manifestRows {
		r := Lookup(name)
		if r == nil {
			return "", fmt.Errorf("resreg: unknown resource %q", name)
		}
		if r.DB != subsystem {
			return "", fmt.Errorf("resreg: resource %q belongs to subsystem %q, not %q", name, r.DB, subsystem)
		}
	}
	// Audit counts are read BEFORE the replace claims the scope, so they describe
	// the state the writes below are about to replace.
	var counts map[string]any
	if opts != nil {
		c, err := applyCounts(be.read(), subsystem, manifestRows)
		if err != nil {
			return "", err
		}
		counts = c
	}
	return applyEntities(ctx, be, subsystem, wantChecksum, force, manifestRows, opts, counts)
}

// validateRows runs the resource's declared set check, or nothing.
//
// It runs before the delete that precedes the writes, so a refused manifest
// leaves the resource exactly as it was (Hooks.ValidateRows).
func (r *Resource) validateRows(ctx context.Context, rows any) error {
	if r.Hooks.ValidateRows == nil {
		return nil
	}
	if err := r.Hooks.ValidateRows(ctx, rows); err != nil {
		return fmt.Errorf("%s: validate: %w", r.Name, err)
	}
	return nil
}

// mergeScopes returns the sorted union of two scope lists.
func mergeScopes(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// applyCounts diffs the manifest against the pre-tx live DB for every
// mentioned, rebuildable resource in subsystem and returns per-resource
// {add,update,delete} counts for the audit summary. Read-only.
func applyCounts(rd reader, subsystem string, manifestRows map[string]any) (map[string]any, error) {
	counts := map[string]any{}
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil || r.SkipApplyRebuild {
			continue
		}
		rows, mentioned := manifestRows[r.Name]
		if !mentioned {
			continue
		}
		d, err := r.diff(rd, rows)
		if err != nil {
			return nil, err
		}
		counts[r.Name] = map[string]any{
			"add": len(d.Add), "update": len(d.Update), "delete": len(d.Remove),
		}
	}
	return counts, nil
}

// applyEvent builds the single per-apply summary row (spec 5/8 §"CAS
// implementation" (3)): actor, manifest digest, per-resource add/update/delete
// counts, final checksum. It commits as the last fenced write of the replace
// (entity.go), which is what makes the row and the projection one act.
func applyEvent(opts *ApplyOpts, counts map[string]any, newChecksum string) audit.Event {
	params := map[string]any{}
	for k, v := range opts.Extra {
		params[k] = v
	}
	params["manifest_digest"] = opts.ManifestDigest
	params["checksum"] = newChecksum
	params["resources"] = counts
	return audit.Event{
		Category:      audit.CategoryMutation,
		Action:        "config.apply",
		Actor:         opts.Actor,
		Surface:       audit.SurfaceCLI,
		Resource:      "manifest",
		Outcome:       audit.OutcomeOK,
		ParamsSummary: params,
	}
}

// Export reads every registered resource of ONE subsystem via ScanAll and
// assembles a manifest map keyed by Resource.Name. No checksum key — Export IS
// the projection Checksum hashes, so baking one in would hash itself.
// Deterministic per canonical key order.
func Export(be Backend, subsystem string) (map[string]any, error) {
	return exportRows(be.read(), subsystem)
}

// exportRows is Export over the handles one read holds.
func exportRows(rd reader, subsystem string) (map[string]any, error) {
	out := map[string]any{}
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil {
			continue
		}
		rows, err := r.scanAll(rd)
		if err != nil {
			return nil, err
		}
		out[r.Name] = rows
	}
	return out, nil
}

// GetResource emits a single-resource manifest fragment, shaped exactly as Export
// would nest it so the fragment re-applies to a no-op. No checksum is stamped —
// a fragment is scoped, not a full subsystem dump.
func GetResource(be Backend, name string) (map[string]any, error) {
	r := Lookup(name)
	if r == nil || r.RowType == nil {
		return nil, fmt.Errorf("resreg: unknown or schema-less resource %q", name)
	}
	rows, err := r.scanAll(be.read())
	if err != nil {
		return nil, err
	}
	return map[string]any{name: rows}, nil
}

// ResourceDelta is one resource's plan summary: PK strings sorted into add,
// update, unchanged and remove. SkipApplyRebuild marks resources whose lists are
// informational only — apply never mutates them, so Changed() ignores them.
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
// and apply must agree (spec 5/8 §"Secret safety": plan shows secrets as
// set/unset, not actionable +/~/- deltas).
func (d ResourceDelta) Changed() bool {
	if d.SkipApplyRebuild {
		return false
	}
	return len(d.Add) > 0 || len(d.Update) > 0 || len(d.Remove) > 0
}

// Diff computes the ResourceDelta for one resource: manifestRows vs the live
// table. Matching is by PK string; payload equality ignores server-stamped fields
// so a hand-written manifest omitting timestamps reads as unchanged.
//
// Scope-aware, matching scoped apply: for a scoped resource only live rows within
// the folders the manifest mentions are candidates for Remove. SkipApplyRebuild
// resources still diff — the apply path is what skips the write, not the diff.
func (r *Resource) Diff(be Backend, manifestRows any) (ResourceDelta, error) {
	return r.diff(be.read(), manifestRows)
}

// diff is Diff over the handles one read holds.
func (r *Resource) diff(rd reader, manifestRows any) (ResourceDelta, error) {
	d := ResourceDelta{Resource: r.Name, SkipApplyRebuild: r.SkipApplyRebuild}
	if r.meta == nil {
		return d, fmt.Errorf("resreg: %s has no schema (RowType unset)", r.Name)
	}
	live, err := r.scanAll(rd)
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

// scopeFilter returns the folder scopes the manifest mentions for a scoped
// resource, or nil for "every live row is in scope". nil fires when the resource
// is scope-less, OR when a scoped resource is mentioned with zero rows — an
// explicitly-present empty list wipes the whole resource.
func (r *Resource) scopeFilter(manifestRows any) map[string]bool {
	if !r.HasScope() {
		return nil
	}
	scopes := r.manifestScopes(manifestRows)
	if len(scopes) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, s := range scopes {
		set[s] = true
	}
	return set
}

func (r *Resource) rowScope(v reflect.Value) string {
	return fmt.Sprintf("%v", v.Field(r.meta.scopeField.idx).Interface())
}

// payloadEqual compares two rows for Diff, treating server-stamped
// fields (StampedFields) as equal regardless of value. A hand-written
// manifest leaves created_at/granted_at/added_at empty; the live row
// carries the stamp — without this they'd diff as a phantom update on
// every plan (spec 5/8 §"Apply lifecycle" step 3).
func (r *Resource) payloadEqual(a, b reflect.Value) bool {
	if len(r.meta.stampedIdx) == 0 {
		return equalIgnoringEmpty(a, b)
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
	return equalIgnoringEmpty(ac, bc)
}

// equalIgnoringEmpty is DeepEqual with one difference: a nil slice or map equals
// an empty one. Both spell the same thing in a manifest, but reflect.DeepEqual
// calls them different. The two sides of a Diff reach it by different routes — an
// `omitempty` field with no value re-parses as nil, while the live row's
// AfterScan builds an empty slice from the stored "[]". Without this every such
// row reported a phantom `update`: 12 of 12 proxyd_routes rows on krons (BUGS Z2).
func equalIgnoringEmpty(a, b reflect.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Slice, reflect.Map:
		if a.Len() == 0 && b.Len() == 0 {
			return true
		}
		if a.Len() != b.Len() {
			return false
		}
		if a.Kind() == reflect.Slice {
			for i := 0; i < a.Len(); i++ {
				if !equalIgnoringEmpty(a.Index(i), b.Index(i)) {
					return false
				}
			}
			return true
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() || !equalIgnoringEmpty(a.MapIndex(k), bv) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !equalIgnoringEmpty(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		return equalIgnoringEmpty(a.Elem(), b.Elem())
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
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

// Plan diffs a parsed manifest against the live DB for every resource in
// subsystem the manifest mentions, in catalog order. Non-mutating. Resources the
// manifest omits are skipped, because apply leaves them untouched.
func Plan(be Backend, subsystem string, manifest map[string]any) ([]ResourceDelta, error) {
	rd := be.read()
	var out []ResourceDelta
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil {
			continue
		}
		rows, ok := manifest[r.Name]
		if !ok {
			continue
		}
		d, err := r.diff(rd, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// EmitYAML writes the manifest map (from Export, plus an optional injected
// "checksum" string key) as a deterministic YAML document. Top-level keys
// sort: checksum first, then resource keys lexicographic. Per-resource rows
// are sorted by PK via EmitRows.
func EmitYAML(manifest map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(manifest))
	for k := range manifest {
		if k != "checksum" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	root := &yaml.Node{Kind: yaml.MappingNode}
	if v, ok := manifest["checksum"]; ok {
		vn := &yaml.Node{}
		if err := vn.Encode(v); err != nil {
			return nil, err
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "checksum"}, vn)
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

// EnvNeed is one entry of a package manifest's `env` section: a key the
// package's process reads from the instance .env, and where its value comes
// from when the operator has set none. Exactly one source is set.
//
// `generate` is minted once and written into .env, where it then belongs to
// the operator like every other line; `default` is derived on every generate
// and never written, so setting the key in .env is the whole override story;
// `required` has no value anyone can invent, so install and generate refuse
// until the operator sets it — never a fragment that installs and then will
// not start.
type EnvNeed struct {
	Generate bool   `yaml:"generate,omitempty"`
	Default  string `yaml:"default,omitempty"`
	Required bool   `yaml:"required,omitempty"`
}

// PackageEnvKey is the reserved top-level manifest key holding EnvNeed entries.
const PackageEnvKey = "env"

var envKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ParseYAML decodes ONE config manifest document into a map of resource name →
// rows. The reserved key "checksum" is extracted and returned separately so it
// can never be mistaken for a resource name. Strict: an unknown top-level key
// rejects before the DB is touched, so an operator's intended config cannot
// silently fail. A package's `env` section is refused: `apply` writes rows, and
// accepting the section would report a success that provisioned nothing.
func ParseYAML(data []byte) (map[string]any, string, error) {
	rows, env, checksum, err := ParseManifest(data)
	if err != nil {
		return nil, "", err
	}
	if len(env) > 0 {
		return nil, "", fmt.Errorf("`%s` is a package declaration that only `arizuko packages` "+
			"provisions — install this file with `packages add` or `packages install`", PackageEnvKey)
	}
	return rows, checksum, nil
}

// ParseManifest is ParseYAML for the document a package ships: the rows plus
// its `env` section, read by install and by generate alike (one declaration,
// many readers).
func ParseManifest(data []byte) (map[string]any, map[string]EnvNeed, string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, "", fmt.Errorf("yaml unmarshal: %w", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = *doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, nil, "", fmt.Errorf("manifest root must be a mapping")
	}
	out := map[string]any{}
	var env map[string]EnvNeed
	var checksum string
	resByName := map[string]*Resource{}
	for _, r := range All() {
		resByName[r.Name] = r
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		val := doc.Content[i+1]
		switch key {
		case "checksum":
			if err := val.Decode(&checksum); err != nil {
				return nil, nil, "", fmt.Errorf("checksum: %w", err)
			}
			continue
		case PackageEnvKey:
			parsed, err := parseEnvNeeds(val)
			if err != nil {
				return nil, nil, "", err
			}
			env = parsed
			continue
		}
		r, ok := resByName[key]
		if !ok || r.RowType == nil {
			return nil, nil, "", fmt.Errorf("unknown resource key %q (line %d)", key, doc.Content[i].Line)
		}
		rows, err := r.ParseRows(val)
		if err != nil {
			return nil, nil, "", err
		}
		out[key] = rows
	}
	return out, env, checksum, nil
}

// parseEnvNeeds decodes the `env` mapping strictly: a key must be an env var
// name and an entry names exactly one source, so a typo like `defualt:` refuses
// instead of installing a package that needs a value nothing supplies.
func parseEnvNeeds(node *yaml.Node) (map[string]EnvNeed, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: must map KEY to {generate|default|required} (line %d)",
			PackageEnvKey, node.Line)
	}
	out := map[string]EnvNeed{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		line := node.Content[i].Line
		if !envKeyRE.MatchString(key) {
			return nil, fmt.Errorf("%s: %q is not an env var name (line %d)", PackageEnvKey, key, line)
		}
		var need EnvNeed
		if err := decodeStrict(node.Content[i+1], &need); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", PackageEnvKey, key, err)
		}
		sources := 0
		for _, set := range []bool{need.Generate, need.Default != "", need.Required} {
			if set {
				sources++
			}
		}
		if sources != 1 {
			return nil, fmt.Errorf("%s.%s: name exactly one of generate, default, required (line %d)",
				PackageEnvKey, key, line)
		}
		out[key] = need
	}
	return out, nil
}

// SplitDocuments splits a `---`-separated multi-document YAML stream into its
// documents. Done via a decoder loop rather than string-splitting on "---" so a
// literal "---" inside a scalar value never misparses as a boundary.
func SplitDocuments(data []byte) ([][]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs [][]byte
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("split documents: %w", err)
		}
		b, err := yaml.Marshal(&doc)
		if err != nil {
			return nil, fmt.Errorf("split documents: re-encode: %w", err)
		}
		docs = append(docs, b)
	}
	return docs, nil
}

// SubsystemOf returns the single subsystem a parsed manifest's resource keys all
// belong to. Errors on an empty manifest or on keys spanning more than one — a
// defense against a hand-edited manifest that merged two owner DBs into one file.
func SubsystemOf(manifest map[string]any) (string, error) {
	seen := map[string]bool{}
	for name := range manifest {
		r := Lookup(name)
		if r == nil {
			return "", fmt.Errorf("resreg: unknown resource %q", name)
		}
		seen[r.DB] = true
	}
	switch len(seen) {
	case 0:
		return "", fmt.Errorf("resreg: manifest document names no resource; cannot infer subsystem")
	case 1:
		for s := range seen {
			return s, nil
		}
	}
	subs := make([]string, 0, len(seen))
	for s := range seen {
		subs = append(subs, s)
	}
	sort.Strings(subs)
	return "", fmt.Errorf("resreg: manifest document mixes subsystems %v; one document must target one owner DB", subs)
}

// mcpArgTypes are the MCPArg.Type spellings mcpArgOption understands. Anything
// else falls through its default and declares a STRING property, so an argument
// the caller sends as a JSON bool or number never arrives and the write is a
// silent no-op — `grant_option` was declared "boolean" and landed that way.
// Registration is init-time, so the panic is a build failure, never a runtime
// one.
var mcpArgTypes = map[string]bool{
	"": true, "string": true, "number": true, "bool": true, "array": true,
}

func (r *Resource) checkMCPArgTypes() {
	for action, args := range r.MCPArgs {
		for _, a := range args {
			if !mcpArgTypes[a.Type] {
				panic(fmt.Sprintf("resreg: %s %s arg %q: unknown MCPArg.Type %q (want one of string/number/bool/array)",
					r.Name, action, a.Name, a.Type))
			}
		}
	}
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

// Mount returns the registered SHAPE of a resource — everything Register stored —
// for a daemon to fill in with BEHAVIOR (Authz, Gate, Handler, Store) and mount.
// It is the only way to build a mountable Resource, which is what makes "one
// owner" structural (spec 5/16). A mount site used to restate the shape as a
// struct literal, so it could name a different slice, omit MCPDoc and silently
// lose the agent's tools, or carry an MCPArgs map the catalog never had.
//
// Panics on an unregistered name: a resource no catalog knows is
// served-but-invisible to /openapi.json and the CLI manifest, and that must stop
// the binary at boot.
func Mount(name string) Resource {
	r := Lookup(name)
	if r == nil {
		panic("resreg: Mount of unregistered resource " + name +
			" — a resource's name is its wire identity; register it in resreg/resources first")
	}
	return *r
}

// BySubsystem returns the registered resources whose Resource.DB equals
// subsystem, in registration order — the per-owner-DB view Export/Apply/Plan
// operate over (spec 5/8: "one subsystem = one owner DB").
func BySubsystem(subsystem string) []*Resource {
	var out []*Resource
	for _, r := range registry {
		if r.DB == subsystem {
			out = append(out, r)
		}
	}
	return out
}

// reset clears the registry. Test-only.
func reset() {
	registry = nil
}
