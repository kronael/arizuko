package resreg

// entity.go is the OBJECT path: how the engine reads and writes a resource
// whose rows are objstore items instead of table columns (spec 6/37 step 6,
// BUGS L259).
//
// THERE IS ONE SHAPE PER RESOURCE AND THE RESOURCE DECLARES IT. Resource.
// EntitySortKey is the whole declaration: naming fields there makes Table an
// entity and every other `db:` tag an attribute name. The engine reads that
// declaration and branches; it never probes the storage, never asks the database
// what shape it found, and never falls back from one path to the other. A
// fallback would be two paths for one table, and two paths drift.
//
// THE COORDINATE IS A LIST, joined by a separator the resource declares, because
// two resources have no single-field identity: network_rules is keyed
// (folder, target) and installed_packages (folder, name). A value carrying the
// separator is refused at WRITE, which is the only end that can see two rows
// joining onto one key (spec 6/40 §1).
//
// WHAT THE OBJECT PATH DELETES, and it is the reason the move is a
// simplification rather than a translation. An attribute an item does not carry
// is ABSENT, and absent is the only null this model has (objstore.Item), so the
// COALESCE-on-read / nil-on-write pair every nullable column needed on the
// column path has nothing left to do: invites' `expires_at` and onboarding's
// five optional columns are simply attributes the item does not carry. That is
// six ColumnOverride hooks gone with the shape, not ported to it.
//
// WHAT IT REFUSES it refuses at Register, which is process start, because a
// misdeclared resource is a caller fault and must stop the binary rather than
// answer a wrong row later. Each refusal below has a reason from the data, not
// a "not implemented yet": see initEntity.

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// OnDB is the Backend for one owner database: the pool itself for a
// column-shaped resource, and this repo's SQL driver over the same pool for an
// entity-shaped one.
//
// The driver is chosen HERE rather than by the caller because a *sql.DB has
// exactly one driver that can serve it, and resreg already builds the same one
// for the audit row it writes into an apply's transaction. A caller holding a
// handle to some other engine builds the Backend itself; there is nothing to
// derive then.
func OnDB(db *sql.DB) Backend { return Backend{DB: db, Obj: sqlstore.New(db)} }

// Backend is one owner database, reachable BOTH ways, because a subsystem's
// resources declare which way each of them answers.
//
// Exactly one half serves any given resource and the declaration decides which
// (Resource.EntitySortKey). Carrying both is not a fallback: it is that a verb
// holds one database handle and the resources inside it do not all have to have
// moved on the same day.
type Backend struct {
	// DB reads and writes a column-shaped resource, and it is also what Apply
	// opens its transaction on.
	DB *sql.DB
	// Obj reads and writes an entity-shaped resource.
	Obj objstore.Store
}

// reader is one read's handles.
//
// It is not Backend because Apply's in-transaction checksum recheck reads
// through the *sql.Tx it is about to write in, and a transaction is not a pool
// — Querier is the interface both satisfy (engine.go). The object half needs no
// such distinction: an entity-backed subsystem's Apply takes a claim rather than
// a transaction, so nothing on that path ever reads through one.
type reader struct {
	sql Querier
	obj objstore.Store
}

// read is the reader for a whole Backend.
func (b Backend) read() reader { return reader{sql: b.DB, obj: b.Obj} }

// entityMeta is the object path's half of the reflection Register does. It is
// built only for a resource that declared EntitySortKey, so a column-shaped
// resource carries none of it.
type entityMeta struct {
	// sort is the fields whose values, joined by sep, ARE the item's sort key.
	// None of them is among attrs: storing the coordinate twice is two facts
	// that can disagree, and the read puts them back from the key they came
	// from.
	sort []fieldMeta
	// sep joins them, and is empty for a single-field key. No value of any sort
	// field may contain it (Resource.EntityKeySep).
	sep string
	// attrs is every other `db:`-tagged field, one attribute each, named by the
	// tag.
	attrs []fieldMeta
	// stamped is the struct-field index of every StampedField, which itemFor
	// fills when the row leaves it empty. It is the same set resourceMeta
	// carries: on this path stampedIdx and autoStampIdx are ONE set, because a
	// ColumnOverride is refused here and that override is the only thing that
	// ever separated them (BUGS F49).
	stamped map[int]bool
	// scope is how a scoped delete SELECTS one scope's items, nil for a
	// resource that declares no ScopeSpec.
	scope *entityScope
}

// entityScope is the read that stands in for `DELETE FROM t WHERE scope = ?`.
//
// An entity has no table to delete from and no predicate delete, so a scoped
// delete is a QUERY and then one guarded delete per item — an unbounded
// read-then-delete, which is why it belongs inside the versioned replace beside
// the wipe rather than in a DeleteScope of its own (spec 6/40 §3).
//
// There are three shapes and the DECLARATION picks which, at Register, because
// where the scope value lives decides what can select on it. An attribute is a
// Where term. A sort key IS the coordinate, so it is an exact key. The first
// field of a composite coordinate is a begins_with on `scope+sep`, which is
// exact because no value may carry the separator — that is what makes
// network_rules' (folder, target) purgeable by folder at all.
type entityScope struct {
	// attr is the attribute the scope value equals, and is empty when the scope
	// is part of the coordinate.
	attr string
	// prefix is true when the scope is the FIRST field of a composite
	// coordinate; false with an empty attr means the coordinate IS the scope.
	prefix bool
}

// sortCols names the coordinate the way an error message has to spell it: one
// column, or the joined list a composite key is made of.
func (e *entityMeta) sortCols() string {
	cols := make([]string, len(e.sort))
	for i, fm := range e.sort {
		cols[i] = fm.col
	}
	return strings.Join(cols, ",")
}

// initEntity builds the object path's metadata, or refuses the declaration.
//
// It runs inside initMeta, so every refusal is a panic at Register: the process
// stops at boot rather than serving a resource whose rows it would read wrong.
// Every one of them names what the object path would need, because a "not
// supported" with no reason is the kind of gap somebody works around.
func (r *Resource) initEntity(m *resourceMeta, byField map[string]int) {
	if len(r.EntitySortKey) == 0 {
		if r.EntityKeySep != "" {
			panic(fmt.Sprintf("resreg: %s declares EntityKeySep %q and no EntitySortKey; "+
				"a separator with nothing to join is a fact no reader uses", r.Name, r.EntityKeySep))
		}
		return
	}
	if r.DB == "" {
		panic(fmt.Sprintf("resreg: %s is entity-backed and names no owner subsystem; "+
			"the subsystem IS the partition every read filters on, and a query without "+
			"one is refused by every driver", r.Name))
	}
	// The separator is what makes a composite key readable back: the read splits
	// on it and expects exactly as many parts as there are fields. One field has
	// nothing to join, so a separator beside it is refused rather than ignored —
	// an ignored declaration is one a later reader believes.
	if len(r.EntitySortKey) > 1 && r.EntityKeySep == "" {
		panic(fmt.Sprintf("resreg: %s has a composite EntitySortKey %v and declares no "+
			"EntityKeySep; the fields would run together into one key no read can split",
			r.Name, r.EntitySortKey))
	}
	if len(r.EntitySortKey) == 1 && r.EntityKeySep != "" {
		panic(fmt.Sprintf("resreg: %s declares EntityKeySep %q for the single-field "+
			"EntitySortKey %q; there is nothing to join", r.Name, r.EntityKeySep,
			r.EntitySortKey[0]))
	}
	var sortFields []fieldMeta
	seen := map[string]bool{}
	for _, name := range r.EntitySortKey {
		if seen[name] {
			panic(fmt.Sprintf("resreg: %s names %q twice in EntitySortKey; one field cannot "+
				"be two parts of a coordinate", r.Name, name))
		}
		seen[name] = true
		i, ok := byField[name]
		if !ok {
			panic(fmt.Sprintf("resreg: %s EntitySortKey %q is not a field of %s, or has no db: tag",
				r.Name, name, m.rowType))
		}
		fm := m.fields[i]
		if m.rowType.Field(fm.idx).Type.Kind() != reflect.String {
			panic(fmt.Sprintf("resreg: %s EntitySortKey %q is a %s; a sort key is the item's "+
				"coordinate and every coordinate is text", r.Name, name,
				m.rowType.Field(fm.idx).Type.Kind()))
		}
		sortFields = append(sortFields, fm)
	}
	if r.RowFilter != "" {
		// Query.Where is a CONJUNCTION and acl_membership's filter is
		// `(added_by IS NULL OR added_by != 'pairing')`, which has no conjunctive
		// form. The read half could be done in Go over the items; the DELETE half
		// could not, because a reader-side filter does not narrow a write, and
		// RowFilter gates DeleteScope and DeleteAll as well as ScanAll. So a
		// half-answer here would widen an apply's delete silently, which is the
		// worst of the three outcomes.
		panic(fmt.Sprintf("resreg: %s is entity-backed and declares a RowFilter; "+
			"a filter that also has to gate a DELETE cannot be a reader-side "+
			"predicate, and Where is a conjunction with no OR", r.Name))
	}
	for _, name := range r.StampedFields {
		if seen[name] {
			// A stamped coordinate is one the SERVER invents, so the same row
			// would address a different item on every apply and the wipe before
			// it would never find the one it replaced.
			panic(fmt.Sprintf("resreg: %s stamps %q and names it in EntitySortKey; a "+
				"coordinate the writer invents addresses a new item on every write",
				r.Name, name))
		}
	}
	if r.Hooks.AfterScan != nil || r.Hooks.ValidateRow != nil || len(r.Hooks.ColumnOverride) > 0 {
		// BeforeInsert is NOT here: a row default is the writer's on this path
		// too, and itemFor applies it (spec 6/40 answer 5). These three still
		// take something this path does not have. ColumnOverride.Read is a SQL
		// expression and .Write is a bind conversion the absent-attribute model
		// already makes unnecessary; ValidateRow takes the *sql.Tx an entity's
		// apply never opens, because it holds a claim instead.
		panic(fmt.Sprintf("resreg: %s is entity-backed and declares AfterScan, ValidateRow "+
			"or ColumnOverride; the first two take the *sql.Tx this path does not open, and "+
			"a column override is a SQL expression with no column to name", r.Name))
	}
	e := &entityMeta{sort: sortFields, sep: r.EntityKeySep, stamped: map[int]bool{}}
	for _, idx := range m.stampedIdx {
		e.stamped[idx] = true
	}
	inKey := map[int]bool{}
	for _, fm := range sortFields {
		inKey[fm.idx] = true
	}
	for _, fm := range m.fields {
		if inKey[fm.idx] {
			continue
		}
		switch k := m.rowType.Field(fm.idx).Type.Kind(); {
		case k == reflect.String, k == reflect.Int, fm.codec != nil:
		default:
			// Every attribute value is a string (objstore). A bool, a slice and a
			// map each need a rendering somebody has to choose and every reader has
			// to agree with, so the field DECLARES one in its `db:` tag
			// (codec.go) and a field with neither a renderable type nor a
			// declaration is refused here rather than stored as whatever
			// fmt.Sprint happened to make of it.
			panic(fmt.Sprintf("resreg: %s.%s is a %s and %s is entity-backed; an "+
				"attribute value is text, so this path renders string and int, and "+
				"anything else declares its encoding in the db: tag",
				r.Name, fm.name, k, r.Name))
		}
		e.attrs = append(e.attrs, fm)
	}
	e.scope = r.entityScopeOf(m, sortFields)
	m.entity = e
}

// entityScopeOf resolves the ScopeSpec into the predicate a scoped delete runs,
// or refuses the declaration.
//
// A scope the object path cannot SELECT on is refused here rather than answered
// by a wider delete: a scoped apply that deleted more than the scope would take
// a folder's rows the manifest never mentioned, which is the worst of the three
// outcomes the RowFilter refusal above names.
func (r *Resource) entityScopeOf(m *resourceMeta, sortFields []fieldMeta) *entityScope {
	if m.scopeField == nil {
		return nil
	}
	if r.Scope.Column != "" {
		// On this path the attribute name IS the `db:` tag, so a second spelling
		// would name an attribute no item carries and the delete would select
		// nothing — silently, because an empty result is a legal answer.
		panic(fmt.Sprintf("resreg: %s is entity-backed and its ScopeSpec names the column "+
			"%q; an attribute is named by its db: tag and nothing else", r.Name, r.Scope.Column))
	}
	for i, fm := range sortFields {
		if fm.idx != m.scopeField.idx {
			continue
		}
		if i == 0 {
			return &entityScope{prefix: len(sortFields) > 1}
		}
		// A begins_with reaches a leading fragment of the key and nothing else,
		// and the coordinate is not an attribute a Where can filter on. So a
		// scope sitting in the middle of a coordinate has no predicate at all,
		// and the answer is to put it first.
		panic(fmt.Sprintf("resreg: %s scopes on %q, which is part %d of its coordinate; "+
			"a key predicate reaches a LEADING fragment, so a scope inside a "+
			"coordinate has to be the first field of it", r.Name, r.Scope.Field, i+1))
	}
	return &entityScope{attr: m.scopeField.col}
}

// entityScopeQuery is the read that finds one scope's items.
func (r *Resource) entityScopeQuery(scope string) objstore.Query {
	e := r.meta.entity
	q := objstore.Query{Entity: r.Table, Partition: r.entityPartition()}
	switch {
	case e.scope.attr != "" && scope == "":
		// An empty string is an ABSENT attribute on this path (itemFor), so the
		// scope every resource spells "" — instance-wide — is selected by Absent
		// and not by Eq "", which matches no item at all and would leave the
		// scope's rows behind while the puts went in over them.
		q.Where = []objstore.Cmp{{Attr: e.scope.attr, Op: objstore.Absent}}
	case e.scope.attr != "":
		q.Where = []objstore.Cmp{{Attr: e.scope.attr, Op: objstore.Eq, Value: scope}}
	case e.scope.prefix:
		// The separator is part of the prefix, not Query.Key.Sep: the subtree
		// separator matches the value ITSELF as well, and `folder` alone is not
		// a coordinate here — every item's key carries the separator after it.
		q.Key = objstore.KeyPredicate{Op: objstore.PrefixKey, Value: scope + e.sep}
	default:
		q.Key = objstore.KeyPredicate{Op: objstore.ExactKey, Value: scope}
	}
	return q
}

// entityScopeDeletes reads one scope's live items and renders the writes that
// remove them — the object path's DeleteScope, and a READ then a write for the
// same reason entityWipe is.
func (r *Resource) entityScopeDeletes(rd reader, scope string) ([]objstore.Write, error) {
	if rd.obj == nil {
		return nil, fmt.Errorf("resreg: %s is entity-backed and the backend carries no "+
			"object store", r.Name)
	}
	items, err := rd.obj.Query(context.Background(), r.entityScopeQuery(scope))
	if err != nil {
		return nil, fmt.Errorf("%s: query scope %q: %w", r.Name, scope, err)
	}
	out := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		out = append(out, objstore.Write{Op: objstore.Delete, Entity: r.Table, Key: it.Key})
	}
	return out, nil
}

// IsEntity reports the shape a resource declared: true when its rows are
// objstore items, false when they are table columns. It is the one question the
// engine asks, and it reads the declaration rather than the storage.
func (r *Resource) IsEntity() bool { return r.meta != nil && r.meta.entity != nil }

// entityPartition is the coordinate every one of a resource's items lives in.
// See EntitySortKey on why it is the subsystem and what that costs.
func (r *Resource) entityPartition() string { return r.DB }

// scanEntities reads every item of the resource, in sort-key order.
//
// The ORDER is the sort key's, where the column path's is PKFields'. Nothing
// downstream depends on the difference: EmitRows sorts by PK before emitting, so
// the checksum is the same value either way, and Diff indexes by PK. A test
// pins that rather than leaving it to be re-derived.
func (r *Resource) scanEntities(rd reader) (any, error) {
	if rd.obj == nil {
		return nil, fmt.Errorf("resreg: %s is entity-backed and the backend carries no "+
			"object store; the verb built a Backend for the column path only", r.Name)
	}
	items, err := rd.obj.Query(context.Background(), objstore.Query{
		Entity:    r.Table,
		Partition: r.entityPartition(),
	})
	if err != nil {
		return nil, fmt.Errorf("%s: query: %w", r.Name, err)
	}
	slice := reflect.MakeSlice(reflect.SliceOf(r.meta.rowType), 0, len(items))
	for _, it := range items {
		row, err := r.rowFrom(it)
		if err != nil {
			return nil, err
		}
		slice = reflect.Append(slice, row)
	}
	return slice.Interface(), nil
}

// rowFrom builds one row from one stored item. The sort-key fields come back out
// of the KEY, which is where they were written.
//
// A composite key is SPLIT on the declared separator and the parts have to count
// exactly, because no value may carry the separator (itemFor refuses one that
// does). A key that does not split is a LOUD fault rather than a partly-filled
// row: half a coordinate names a row nobody wrote.
//
// An attribute the item does not carry leaves its field at the zero value, which
// is what absent means. A numeric attribute that is not a number is an ERROR
// rather than a zero: a row that cannot be read is a loud fault, and reading it
// as 0 would put a wrong `max_uses` into an export the operator then applies.
func (r *Resource) rowFrom(it objstore.Item) (reflect.Value, error) {
	e := r.meta.entity
	ptr := reflect.New(r.meta.rowType)
	row := ptr.Elem()
	if len(e.sort) == 1 {
		row.Field(e.sort[0].idx).SetString(it.Key.Sort)
	} else {
		parts := strings.Split(it.Key.Sort, e.sep)
		if len(parts) != len(e.sort) {
			return reflect.Value{}, fmt.Errorf("%s item %q splits on %q into %d parts and "+
				"its coordinate (%s) has %d; the key was written by something that does not "+
				"share this declaration", r.Name, it.Key.Sort, e.sep, len(parts),
				e.sortCols(), len(e.sort))
		}
		for i, fm := range e.sort {
			row.Field(fm.idx).SetString(parts[i])
		}
	}
	for _, fm := range e.attrs {
		text, ok := it.Attrs[fm.col]
		if !ok {
			continue
		}
		f := row.Field(fm.idx)
		if fm.codec != nil {
			if err := fm.codec.decode(text, f); err != nil {
				return reflect.Value{}, fmt.Errorf("%s %s=%q: %s: %w",
					r.Name, e.sortCols(), it.Key.Sort, fm.col, err)
			}
			continue
		}
		if f.Kind() == reflect.String {
			f.SetString(text)
			continue
		}
		n, err := strconv.Atoi(text)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("%s %s=%q: %s is %q, which is not a number: %w",
				r.Name, e.sortCols(), it.Key.Sort, fm.col, text, err)
		}
		f.SetInt(int64(n))
	}
	return row, nil
}

// sortKeyFor renders one row's coordinate: the sort field, or the declared
// join of the several that make it up.
//
// A value CARRYING the separator is refused rather than joined, which is the
// rule store/pane_sessions already enforces over its three fields. Joined, two
// different rows would address one item and the last write would win, and the
// read that splits the key back apart would answer a tuple neither row has.
//
// An EMPTY part is allowed and an empty whole key is not. network_rules spells
// an instance-wide rule folder="" and installed_packages spells an
// instance-wide package the same way, so refusing the part would refuse every
// global row; the parts still separate, because the separator count is fixed.
func (r *Resource) sortKeyFor(v reflect.Value) (string, error) {
	e := r.meta.entity
	parts := make([]string, len(e.sort))
	for i, fm := range e.sort {
		parts[i] = v.Field(fm.idx).String()
		if e.sep != "" && strings.Contains(parts[i], e.sep) {
			return "", fmt.Errorf("%s row carries %s=%q, and %q separates the %d fields of "+
				"its coordinate (%s); two rows would address one item",
				r.Name, fm.col, parts[i], e.sep, len(e.sort), e.sortCols())
		}
	}
	key := strings.Join(parts, e.sep)
	if key == "" {
		return "", fmt.Errorf("%s row carries no %s, which is its sort key; "+
			"every such row would address one item and the last write would win",
			r.Name, e.sortCols())
	}
	return key, nil
}

// itemFor renders one row as the item it is stored as.
//
// An empty STRING attribute is left OFF the item, because absent is the only
// null and an empty string written into the document is a value a tombstone test
// still finds. An INT is always written: a Go int has no unset, so 0 is a value
// the row states rather than one it omits — and a CODEC'D field is written for
// the same reason, its zero value rendered as its own text (codec.go).
//
// A STAMPED field the row leaves empty is filled HERE, because an entity has no
// engine between the caller and the item — there is no Insert to hang a
// BeforeInsert on, and the column path's auto-stamp runs in one (engine.go).
// The layout is core.TS and not the column path's RFC3339: a stored instant is
// compared as TEXT and core.TS is the fixed-width form that makes byte order and
// time order the same thing, which is the rule objstore.OrderedSort enforces
// where an attribute is declared ordered (BUGS L97). The two paths therefore
// stamp DIFFERENT widths, and a table migrating from columns to items carries
// its old 20-byte values with it — the migration normalises them, not this.
//
// The declaration STAYS on an entity-backed resource even though no engine
// reads it to insert: it is what tells an operator's manifest the field is not
// theirs to set (rowMCPArgsFor drops it from create and update, Diff ignores it
// when comparing payloads).
func (r *Resource) itemFor(v reflect.Value) (objstore.Write, error) {
	e := r.meta.entity
	if r.Hooks.BeforeInsert != nil {
		// The row is COPIED first: the hook mutates a pointer, and the value
		// handed in belongs to the caller's slice — filling a default in place
		// would edit the manifest the apply is still reading.
		ptr := reflect.New(r.meta.rowType)
		ptr.Elem().Set(v)
		if err := r.Hooks.BeforeInsert(ptr.Interface()); err != nil {
			return objstore.Write{}, fmt.Errorf("%s: before-insert: %w", r.Name, err)
		}
		v = ptr.Elem()
	}
	sort, err := r.sortKeyFor(v)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := make(map[string]string, len(e.attrs))
	for _, fm := range e.attrs {
		f := v.Field(fm.idx)
		if fm.codec != nil {
			text, err := fm.codec.encode(f)
			if err != nil {
				return objstore.Write{}, fmt.Errorf("%s.%s: %w", r.Name, fm.name, err)
			}
			attrs[fm.col] = text
			continue
		}
		if f.Kind() == reflect.String {
			s := f.String()
			if s == "" && e.stamped[fm.idx] {
				s = core.NowTS()
			}
			if s != "" {
				attrs[fm.col] = s
			}
			continue
		}
		attrs[fm.col] = strconv.FormatInt(f.Int(), 10)
	}
	return objstore.Write{
		Op:     objstore.Put,
		Entity: r.Table,
		Key:    objstore.Key{Partition: r.entityPartition(), Sort: sort},
		Attrs:  attrs,
	}, nil
}

// entityPuts renders a `[]RowType` as the writes that store it.
//
// A row set naming one coordinate twice is refused here rather than at the
// driver, because a replace splits its writes across transactions: which two
// writes share one batch is not the caller's to see, so a collision has to be
// caught over the WHOLE set (objstore.Replace).
func (r *Resource) entityPuts(rows any) ([]objstore.Write, error) {
	rv, err := r.rowSlice(rows)
	if err != nil {
		return nil, err
	}
	out := make([]objstore.Write, 0, rv.Len())
	seen := make(map[string]bool, rv.Len())
	for i := range rv.Len() {
		w, err := r.itemFor(rv.Index(i))
		if err != nil {
			return nil, err
		}
		if seen[w.Key.Sort] {
			return nil, fmt.Errorf("%s: two rows carry %s=%q, so one would overwrite the other",
				r.Name, r.meta.entity.sortCols(), w.Key.Sort)
		}
		seen[w.Key.Sort] = true
		out = append(out, w)
	}
	return out, nil
}

// entityWipe reads the resource's live items and renders the writes that remove
// them — the object path's DeleteAll.
//
// It is a READ then a write because the delete set is unbounded and no engine
// deletes by predicate here. Reading it inside the replace's claim is what keeps
// a second replace's rows out of this one's delete set (objstore.Replace).
func (r *Resource) entityWipe(rd reader) ([]objstore.Write, error) {
	if rd.obj == nil {
		return nil, fmt.Errorf("resreg: %s is entity-backed and the backend carries no "+
			"object store", r.Name)
	}
	items, err := rd.obj.Query(context.Background(), objstore.Query{
		Entity:    r.Table,
		Partition: r.entityPartition(),
	})
	if err != nil {
		return nil, fmt.Errorf("%s: query: %w", r.Name, err)
	}
	out := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		out = append(out, objstore.Write{Op: objstore.Delete, Entity: r.Table, Key: it.Key})
	}
	return out, nil
}

// entityDeletes is the delete set one resource's apply issues: the mentioned
// scopes' items for a scoped resource with rows, and every item otherwise.
//
// The two branches and the empty-list case are the column path's, restated over
// items rather than re-derived — an explicitly-present empty list is the
// operator declaring "no rows for this resource" and falls to the total wipe,
// while a resource absent from the manifest never reaches here at all.
func (r *Resource) entityDeletes(rd reader, rows any, opts *ApplyOpts) ([]objstore.Write, error) {
	scopes := r.manifestScopes(rows)
	if !r.HasScope() || len(scopes) == 0 {
		return r.entityWipe(rd)
	}
	if opts != nil {
		scopes = mergeScopes(scopes, opts.PruneScopes)
	}
	var out []objstore.Write
	for _, scope := range scopes {
		dels, err := r.entityScopeDeletes(rd, scope)
		if err != nil {
			return nil, err
		}
		out = append(out, dels...)
	}
	return out, nil
}

// --- the subsystem's shape --------------------------------------------

// shape is which of the two paths a subsystem's resources declared.
type shape uint8

const (
	shapeColumn shape = iota
	shapeEntity
)

// subsystemShape answers which shape one subsystem's registered resources
// declared, and REFUSES a subsystem whose resources disagree.
//
// The declaration is per RESOURCE and the refusal is per SUBSYSTEM, and the
// difference is Apply's. Apply's atomicity is a subsystem-wide property: the
// column path runs every DELETE and INSERT in ONE transaction, so a failure
// part-way rolls the whole subsystem back to its pre-image (BUGS L44), while the
// object path is a marker-gated sequence with no pre-image at all and recovery
// is to run it again. A MIXED subsystem would give an operator half of each —
// the columns undone and the items part-written — with no way to say which half
// their apply landed in. So the shapes may differ between subsystems and never
// inside one, which is what lets onbod's three resources move without waiting
// for routd's thirteen.
//
// A subsystem with no schema-bearing resource is shapeColumn, which is what its
// empty transaction has always been.
func subsystemShape(subsystem string) (shape, error) {
	var columns, entities []string
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil {
			continue
		}
		if r.IsEntity() {
			entities = append(entities, r.Name)
		} else {
			columns = append(columns, r.Name)
		}
	}
	if len(entities) > 0 && len(columns) > 0 {
		sort.Strings(columns)
		sort.Strings(entities)
		return shapeColumn, fmt.Errorf("resreg: subsystem %s mixes shapes — %s are entities "+
			"and %s are columns; apply is atomic per subsystem, and a mixed one would roll "+
			"the columns back and leave the items part-written",
			subsystem, strings.Join(entities, ", "), strings.Join(columns, ", "))
	}
	if len(entities) > 0 {
		return shapeEntity, nil
	}
	return shapeColumn, nil
}

// --- the version marker one subsystem's projection is replaced under ----

// The scope this engine claims and compares is store.OwnerScope, declared
// THERE rather than here for the reason SubsystemOnbod IS store.OwnerOnbod: a
// subsystem is an owner database, store holds the writers whose every mutation
// has to move that subsystem's marker, and store cannot import this package
// (BUGS L264). One declaration, two readers.

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureSubsystem prepares a database to serve one subsystem's entity-backed
// resources: the entity behind each of them, the entity the version marker lives
// in, and the marker itself.
//
// A daemon calls it where it already calls CreateEntity, and the marker has to
// exist before any write: Bump is an Update and an Update against a missing item
// refuses the WHOLE batch it rides in, so a bounded writer bumping a marker
// nobody created would lose its mutation with it (objstore.EnsureScope).
//
// It is a no-op for a subsystem still on the column path, so an opener calls it
// unconditionally and the set it creates follows the registrations rather than a
// second list beside them.
func EnsureSubsystem(ctx context.Context, m EntityMaker, s objstore.Store, subsystem string) error {
	var entities []string
	for _, r := range BySubsystem(subsystem) {
		if r.IsEntity() {
			entities = append(entities, r.Table)
		}
	}
	if len(entities) == 0 {
		return nil
	}
	for _, entity := range append(entities, store.ScopeEntity) {
		if err := m.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return objstore.EnsureScope(ctx, s, store.OwnerScope(subsystem))
}

// --- the object path's apply -------------------------------------------

// applyEntities is Apply for a subsystem whose resources are entities: the
// versioned replace of spec 6/40 §3, in place of the one transaction the column
// path opens.
//
// WHAT IT GIVES UP, said plainly because an operator pays it. The column path
// rolls the whole subsystem back to its pre-image when anything fails part-way
// (BUGS L44). This has no pre-image and no rollback: a replace that dies after
// it has written leaves the subsystem PART-APPLIED with its claim still held,
// and recovery is to run the same apply again — roll forward, never undo. That
// is the price 6/38 costed for an unbounded row set, and objstore.Replace's own
// error says it in the words the operator sees.
//
// The reads inside the body run UNDER the claim, which is what keeps a second
// apply's rows out of this one's delete set. It does not exclude an ordinary
// writer: the claim gates replaces, so a daemon writing one of these entities
// during an apply still commits, where the column path's write lock would have
// blocked it. That is the second thing the shape costs.
func applyEntities(ctx context.Context, be Backend, subsystem, wantChecksum string, force bool,
	manifestRows map[string]any, opts *ApplyOpts, counts map[string]any) (string, error) {
	if be.Obj == nil {
		return "", fmt.Errorf("resreg: %s is entity-backed and the backend carries no object store",
			subsystem)
	}
	rd := be.read()
	var current, applied string
	_, err := objstore.Replace(ctx, be.Obj, store.OwnerScope(subsystem), "",
		func(ctx context.Context, rp *objstore.Replacer) error {
			var err error
			// Read under the claim, so the checksum this compares is the state the
			// writes below are about to replace and not one a second replace moved.
			if current, err = checksum(rd, subsystem); err != nil {
				return fmt.Errorf("checksum: %w", err)
			}
			if !force && current != wantChecksum {
				// Before any write, so Replace releases the claim rather than
				// leaving the scope wedged over an operator's stale manifest.
				return fmt.Errorf("%w: db=%s manifest=%s", ErrChecksumMismatch, current, wantChecksum)
			}
			if err := replaceRows(ctx, rp, rd, subsystem, manifestRows, opts); err != nil {
				return err
			}
			if applied, err = checksum(rd, subsystem); err != nil {
				return fmt.Errorf("checksum after write: %w", err)
			}
			if opts == nil {
				return nil
			}
			_, w := audit.WriteFor(applyEvent(opts, counts, applied))
			committed, err := rp.Write(ctx, w)
			if err != nil {
				return fmt.Errorf("audit: %w", err)
			}
			if !committed {
				return fmt.Errorf("audit: the apply row is already recorded")
			}
			return nil
		})
	if err != nil {
		return current, err
	}
	return applied, nil
}

// replaceRows issues one entity-backed subsystem's deletes and puts under the
// claim, in catalog order, resource by resource.
//
// The wipe and the puts are TWO calls because a delete and the put that replaces
// the same coordinate cannot ride one call: which two writes share a batch is
// decided by where the batch boundary falls, so objstore.Replacer refuses the
// pair outright rather than answering differently depending on the row count.
// That is the same order the column path runs, DeleteAll then InsertAll.
//
// The SCOPED branch is the same one the column path takes and the reason it has
// to exist here is the same: a manifest naming two folders must not take a
// third folder's rows, and a folder purge that leaves them behind hands the next
// tenant of the path the last one's egress rules, cron tasks, held actions and
// web routes (BUGS L253). What differs is that a scope's delete set is READ
// first — an entity has no predicate delete — and that read runs UNDER THE
// CLAIM, which is what keeps a second replace's rows out of this one's delete
// set. ApplyOpts.PruneScopes joins the mentioned scopes exactly as it does on
// the column path, so the cross-subsystem rollback still reaches the folders a
// forward apply created.
func replaceRows(ctx context.Context, rp *objstore.Replacer, rd reader, subsystem string,
	manifestRows map[string]any, opts *ApplyOpts) error {
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil || r.SkipApplyRebuild {
			continue
		}
		rows, mentioned := manifestRows[r.Name]
		if !mentioned {
			continue // resource absent from manifest → untouched
		}
		wipe, err := r.entityDeletes(rd, rows, opts)
		if err != nil {
			return err
		}
		if _, err := rp.Write(ctx, wipe...); err != nil {
			return err
		}
		puts, err := r.entityPuts(rows)
		if err != nil {
			return err
		}
		if _, err := rp.Write(ctx, puts...); err != nil {
			return err
		}
	}
	return nil
}
