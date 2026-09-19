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
// WHAT AN ITEM MAKES UNNECESSARY, and it is the reason the shape is a
// simplification rather than a translation. An attribute an item does not carry
// is ABSENT, and absent is the only null this model has (objstore.Item), so a
// nullable value needs no COALESCE on read and no nil on write: invites'
// `expires_at` and onboarding's five optional columns are simply attributes the
// item does not carry, and no resource declares a ColumnOverride for one.
//
// WHAT IT REFUSES it refuses at Register, which is process start, because a
// misdeclared resource is a caller fault and must stop the binary rather than
// answer a wrong row later. Each refusal below has a reason from the data, not
// a "not implemented yet": see initEntity.

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// On is the Backend for one owner's tables. Every resource an owner subsystem
// declares is entity-shaped, and resreg/resources/shape_test.go is the lock on
// that, so the store addressed by key is the whole of it.
func On(obj objstore.Store) Backend { return Backend{Obj: obj} }

// Backend is one owner's tables, as the handle every resource is read and
// written through.
type Backend struct {
	// Obj reads and writes an entity-shaped resource.
	Obj objstore.Store
}

// reader is one read's handle. It is not Backend because Apply's in-transaction
// recheck reads through a handle of its own.
type reader struct {
	obj objstore.Store
}

// read is the reader for a whole Backend.
func (b Backend) read() reader { return reader{obj: b.Obj} }

// entityMeta is the item half of the reflection Register does. It is built only
// for a resource that declared EntitySortKey, so a column-shaped resource
// carries none of it.
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
	// stamped is the struct-field index of every StampedField the writer may
	// INVENT — resourceMeta.autoStampIdx, not stampedIdx. The two differ where a
	// resource declares AbsentStamps: `groups.updated_at` is server-owned and
	// legitimately absent, and inventing one makes the first `export | apply`
	// move a checksum the operator did not (BUGS F49).
	stamped map[int]bool
	// scope is how a scoped delete SELECTS one scope's items, nil for a
	// resource that declares no ScopeSpec.
	scope *entityScope
	// filtered is the struct-field index of every attribute the resource's own
	// RowFilter compares. itemFor writes those even when they are EMPTY, because
	// an absent attribute answers no comparison: a row that left one off would
	// write itself out of its own projection, where no export shows it and no
	// apply replaces it.
	filtered map[int]bool
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

// initEntity builds the item metadata, or refuses the declaration.
//
// It runs inside initMeta, so every refusal is a panic at Register: the process
// stops at boot rather than serving a resource whose rows it would read wrong.
// Every one of them names what an item would need, because a "not supported"
// with no reason is the kind of gap somebody works around.
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
		// A coordinate is TEXT, so a field that is not one needs a rendering the
		// key can carry — and for a number that rendering is not strconv's,
		// because the key is compared as text and "10" sorts before "2". The pad
		// codec is that rendering and the only one a coordinate takes (codec.go).
		if k := m.rowType.Field(fm.idx).Type.Kind(); k != reflect.String {
			if fm.codec == nil || fm.codec.name != padCodec {
				panic(fmt.Sprintf("resreg: %s EntitySortKey %q is a %s and declares no %q "+
					"encoding; a sort key is the item's coordinate, every coordinate is "+
					"text, and a number's own rendering does not sort like a number",
					r.Name, name, k, padCodec))
			}
		}
		sortFields = append(sortFields, fm)
	}
	// A filter term reaches an item as a Where, which compares an ATTRIBUTE. The
	// coordinate is not one: it is the key, and a key predicate reaches a leading
	// fragment of it, never one field in the middle. So a filter on a sort field
	// is refused rather than answered by a read that ignores it — the same
	// refusal entityScopeOf makes, for the same reason.
	for _, c := range r.RowFilter {
		for _, fm := range sortFields {
			if fm.col == c.Attr {
				panic(fmt.Sprintf("resreg: %s filters on %q, which is part of its coordinate "+
					"(%s); a filter is a Where over attributes and the coordinate is the key",
					r.Name, c.Attr, strings.Join(r.EntitySortKey, ",")))
			}
		}
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
	// The coordinate is unique by construction — it IS the key one item answers
	// to — and the manifest identity has to be at least as fine, or Diff indexes
	// two items under one PK and reports whichever it read last. So PKFields has
	// to name every sort field. It may name more: a wider PK is still unique.
	//
	// This is checkable and the uniqueness it stands in for is not, which is why
	// it is the declared rule. route_tokens keyed items on `token_hash` and
	// declared PKFields {"JID"}; krons held 43 tokens over 27 jids, so 16 live
	// ingress credentials shared one identity and `arizuko plan` counted them
	// once (BUGS L260).
	inPK := nameSet(r.PKFields)
	for _, name := range r.EntitySortKey {
		if !inPK[name] {
			panic(fmt.Sprintf("resreg: %s keys items on %v and declares PKFields %v, which "+
				"omits %q; two items would share one manifest identity and Diff would "+
				"report one of them", r.Name, r.EntitySortKey, r.PKFields, name))
		}
	}
	if r.Hooks.AfterScan != nil || len(r.Hooks.ColumnOverride) > 0 {
		// BeforeInsert and ValidateRows are NOT here. A row default is the
		// writer's and itemFor applies it (spec 6/40 answer 5); a set check takes
		// the manifest's own rows and no transaction, so replaceRows runs it
		// before the wipe. These two ask for something an item has not got:
		// ColumnOverride.Read is a SQL expression and .Write a bind conversion the
		// absent-attribute model already makes unnecessary, and AfterScan decodes
		// a shadow COLUMN, which is what a declared codec does instead (codec.go).
		panic(fmt.Sprintf("resreg: %s is entity-backed and declares AfterScan or "+
			"ColumnOverride; an override is a SQL expression with no column to name, and a "+
			"post-scan decode is a codec the db: tag declares", r.Name))
	}
	e := &entityMeta{sort: sortFields, sep: r.EntityKeySep, stamped: map[int]bool{},
		filtered: map[int]bool{}}
	for _, idx := range m.autoStampIdx {
		e.stamped[idx] = true
	}
	for _, c := range r.RowFilter {
		for _, fm := range m.fields {
			if fm.col == c.Attr {
				e.filtered[fm.idx] = true
			}
		}
	}
	inKey := map[int]bool{}
	for _, fm := range sortFields {
		inKey[fm.idx] = true
	}
	for _, fm := range m.fields {
		if inKey[fm.idx] {
			continue
		}
		if fm.codec != nil && fm.codec.name == padCodec {
			// The padding orders a KEY, and an attribute is not one. A number
			// stored as an attribute already has a rendering, and a second one is
			// the drift codec.go exists to prevent.
			panic(fmt.Sprintf("resreg: %s.%s declares the %q encoding and is not part of "+
				"the coordinate (%s); padding orders a KEY, and an attribute that is a "+
				"number is already rendered the same way on both shapes",
				r.Name, fm.name, padCodec, strings.Join(r.EntitySortKey, ",")))
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
	if e.scope != nil {
		for _, fm := range e.attrs {
			if e.stamped[fm.idx] && m.rowType.Field(fm.idx).Type.Kind() == reflect.Int {
				panic(fmt.Sprintf("resreg: %s stamps the number %s and declares a scope; "+
					"a scoped apply wipes only the scopes its manifest mentions, so a number "+
					"minted against that manifest would collide with a row it never touched",
					r.Name, fm.col))
			}
		}
	}
	m.entity = e
}

// entityScopeOf resolves the ScopeSpec into the predicate a scoped delete runs,
// or refuses the declaration.
//
// A scope no query can SELECT on is refused here rather than answered by a
// wider delete: a scoped apply that deleted more than the scope would take
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
	q := objstore.Query{Entity: r.Table, Partition: r.entityPartition(), Where: r.RowFilter}
	switch {
	case e.scope.attr != "" && scope == "":
		// An empty string is an ABSENT attribute on this path (itemFor), so the
		// scope every resource spells "" — instance-wide — is selected by Absent
		// and not by Eq "", which matches no item at all and would leave the
		// scope's rows behind while the puts went in over them.
		q.Where = append([]objstore.Cmp{{Attr: e.scope.attr, Op: objstore.Absent}}, q.Where...)
	case e.scope.attr != "":
		q.Where = append([]objstore.Cmp{{Attr: e.scope.attr, Op: objstore.Eq, Value: scope}}, q.Where...)
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
// remove them. It is a READ then a write for the same reason entityWipe is.
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
		Where:     r.RowFilter,
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
	parts := []string{it.Key.Sort}
	if len(e.sort) > 1 {
		parts = strings.Split(it.Key.Sort, e.sep)
		if len(parts) != len(e.sort) {
			return reflect.Value{}, fmt.Errorf("%s item %q splits on %q into %d parts and "+
				"its coordinate (%s) has %d; the key was written by something that does not "+
				"share this declaration", r.Name, it.Key.Sort, e.sep, len(parts),
				e.sortCols(), len(e.sort))
		}
	}
	for i, fm := range e.sort {
		f := row.Field(fm.idx)
		if fm.codec != nil {
			if err := fm.codec.decode(parts[i], f); err != nil {
				return reflect.Value{}, fmt.Errorf("%s item %q: %s: %w",
					r.Name, it.Key.Sort, fm.col, err)
			}
			continue
		}
		f.SetString(parts[i])
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
		if fm.codec != nil {
			text, err := fm.codec.encode(v.Field(fm.idx))
			if err != nil {
				return "", fmt.Errorf("%s.%s: %w", r.Name, fm.name, err)
			}
			parts[i] = text
		} else {
			parts[i] = v.Field(fm.idx).String()
		}
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
// A STAMPED field the row leaves empty is filled HERE, because there is no
// engine between the caller and the item to hang the fill on. The layout is
// core.TS: a stored instant is compared as TEXT and core.TS is the fixed-width
// form that makes byte order and time order the same thing, which is the rule
// objstore.OrderedSort enforces where an attribute is declared ordered (BUGS
// L97). A table a migration carries into items brings its 20-byte values with
// it — the migration normalises those, not this.
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
			if s != "" || e.filtered[fm.idx] {
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
	rv = r.mintNumbers(rv)
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

// mintNumbers hands a value to every NUMERIC stamped field a row leaves unset.
//
// It stands in for AUTOINCREMENT, and routes.id is why it exists: the id
// is the handle `delete_route(id)` addresses, a live agent wire contract, and a
// manifest is not required to state one. An entity has no engine between the
// caller and the item and no sequence to draw from, so the writer mints — the
// same answer a stamped instant gets (itemFor), for a number instead of a time.
//
// The number is one past the LARGEST the set already states, so a set that
// carries its ids round-trips them unchanged and a hand-written one gets ids
// nothing else in it holds. The set is the whole answer because the apply wipes
// the resource first: afterwards the resource IS this set. initEntity refuses
// the pairing that would break that — a scoped resource wipes only the scopes
// its manifest mentions, so a number minted against this set could collide with
// a row the apply never touched.
//
// Zero is the unset value and not a stated one: SQLite's AUTOINCREMENT hands out
// 1 first, so no live row carries 0.
func (r *Resource) mintNumbers(rv reflect.Value) reflect.Value {
	var fields []int
	for _, fm := range r.meta.entity.attrs {
		if r.meta.entity.stamped[fm.idx] && r.meta.rowType.Field(fm.idx).Type.Kind() == reflect.Int {
			fields = append(fields, fm.idx)
		}
	}
	if len(fields) == 0 {
		return rv
	}
	// The rows belong to the caller's slice and every later resource's checksum
	// and Diff read the same one, so the mint lands in a copy — the answer
	// itemFor already gives a row a BeforeInsert hook would edit.
	out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	reflect.Copy(out, rv)
	for _, idx := range fields {
		next := int64(0)
		for i := range out.Len() {
			next = max(next, out.Index(i).Field(idx).Int())
		}
		for i := range out.Len() {
			f := out.Index(i).Field(idx)
			if f.Int() != 0 {
				continue
			}
			next++
			f.SetInt(next)
		}
	}
	return out
}

// entityWipe reads the resource's live items and renders the writes that remove
// them.
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
		Where:     r.RowFilter,
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
// The two branches and the empty-list case are the MANIFEST's and not the
// storage's — an explicitly-present empty list is the operator declaring "no
// rows for this resource" and falls to the total wipe, while a resource absent
// from the manifest never reaches here at all.
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

// --- what a subsystem's resources have to be --------------------------

// requireEntities refuses a subsystem holding a resource that declares no entity
// shape. Apply and ExportSnapshot read and write ITEMS — a marker-gated sequence
// over objstore — and a resource that is not an entity has nothing for them to
// run it through.
//
// The refusal is per SUBSYSTEM because Apply's atomicity is: the whole
// subsystem's projection is replaced under one version marker, and recovery from
// a failure part-way is to run it again. A subsystem holding one column-shaped
// resource would leave the operator with the items replaced and that one
// resource untouched, with nothing to say which half their apply landed in.
//
// resreg/resources/shape_test.go is where the registered set is held to this;
// here it is the gate in front of the two verbs.
func requireEntities(subsystem string) error {
	var columns []string
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil || r.IsEntity() {
			continue
		}
		columns = append(columns, r.Name)
	}
	if len(columns) == 0 {
		return nil
	}
	sort.Strings(columns)
	return fmt.Errorf("resreg: subsystem %s holds %s, which declare no EntitySortKey; "+
		"apply replaces a subsystem's items under one version marker and there is "+
		"nothing to replace a column-shaped resource under",
		subsystem, strings.Join(columns, ", "))
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
// It creates nothing for a subsystem no entity-backed resource names, so an
// opener calls it unconditionally and the set it creates follows the
// registrations rather than a second list beside them.
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

// --- the apply ----------------------------------------------------------

// applyEntities is Apply for a subsystem whose resources are entities: the
// versioned replace of spec 6/40 §3.
//
// WHAT IT COSTS, said plainly because an operator pays it. There is no
// pre-image and no rollback: a replace that dies after it has written leaves the
// subsystem PART-APPLIED with its claim still held, and recovery is to run the
// same apply again — roll forward, never undo. That is the price 6/38 costed for
// an unbounded row set, and objstore.Replace's own error says it in the words
// the operator sees.
//
// THE ROLL-FORWARD IS THE SAME CALL, which is why the claim carries an intent.
// Re-running this apply carries the intent the interrupted one recorded, so it
// takes that claim over and finishes the work instead of answering ErrScopeHeld
// for good (BUGS L267). It is sound because THIS is the operation that converges:
// every delete set below is derived from the manifest's own scopes and re-read
// live, so deleting whatever is there now and putting the document over it lands
// on the state a clean run would have produced, whether the dead apply stopped
// at the first row or the last.
//
// The reads inside the body run UNDER the claim, which is what keeps a second
// apply's rows out of this one's delete set. It does not exclude an ordinary
// writer: the claim gates replaces, so a daemon writing one of these entities
// during an apply still commits. That is the second thing an apply costs.
func applyEntities(ctx context.Context, be Backend, subsystem, wantChecksum string, force bool,
	manifestRows map[string]any, opts *ApplyOpts, counts map[string]any) (string, error) {
	if be.Obj == nil {
		return "", fmt.Errorf("resreg: %s is entity-backed and the backend carries no object store",
			subsystem)
	}
	claim, err := applyClaim(subsystem, manifestRows, opts)
	if err != nil {
		return "", err
	}
	rd := be.read()
	var current, applied string
	_, err = objstore.Replace(ctx, be.Obj, store.OwnerScope(subsystem), "", claim,
		func(ctx context.Context, rp *objstore.Replacer) error {
			var err error
			// Read under the claim, so the checksum this compares is the state the
			// writes below are about to replace and not one a second replace moved.
			if current, err = checksum(rd, subsystem); err != nil {
				return fmt.Errorf("checksum: %w", err)
			}
			// A takeover has no pre-image left to compare: the checksum the
			// operator planned against is exactly what the interrupted apply
			// began overwriting, so the CAS would refuse the only action that can
			// settle the scope.
			if !force && !rp.TookOver() && current != wantChecksum {
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

// applyClaim renders what one apply is about to write, so a later apply of the
// same work can take a claim this one leaves behind and finish it
// (objstore.Claim).
//
// It is DERIVED from the material that decides the write set — the document and
// the scopes ApplyOpts asks pruned — rather than passed in by each caller. A
// caller-chosen value would be the same mistake in both directions: a fresh one
// per run could never take its own claim back, and a constant would take over an
// apply whose rows this one does not cover. The digest is the same renderer the
// checksum uses, so two callers that would write the same rows agree without
// coordinating.
//
// `force` and wantChecksum are absent because neither changes a single write:
// they gate whether the apply runs, not what it lays down.
func applyClaim(subsystem string, manifestRows map[string]any, opts *ApplyOpts) (objstore.Claim, error) {
	sum, err := hashManifest(manifestRows)
	if err != nil {
		return objstore.Claim{}, fmt.Errorf("intent: %w", err)
	}
	var prune []string
	if opts != nil {
		prune = append(prune, opts.PruneScopes...)
		sort.Strings(prune)
	}
	return objstore.Claim{
		Intent: "apply " + subsystem + " " + sum + " prune=" + strings.Join(prune, ","),
		Take:   opts != nil && opts.TakeOver,
	}, nil
}

// replaceRows issues one entity-backed subsystem's deletes and puts under the
// claim, in catalog order, resource by resource.
//
// The wipe and the puts are TWO calls because a delete and the put that replaces
// the same coordinate cannot ride one call: which two writes share a batch is
// decided by where the batch boundary falls, so objstore.Replacer refuses the
// pair outright rather than answering differently depending on the row count.
// The delete goes first and the puts follow.
//
// The SCOPED branch exists because a manifest naming two folders must not take
// a third folder's rows, and a folder purge that leaves them behind hands the
// next tenant of the path the last one's egress rules, cron tasks, held actions
// and web routes (BUGS L253). A scope's delete set is READ first — an entity has
// no predicate delete — and that read runs UNDER THE CLAIM, which is what keeps
// a second replace's rows out of this one's delete set. ApplyOpts.PruneScopes
// joins the mentioned scopes, so the cross-subsystem rollback still reaches the
// folders a forward apply created.
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
		if err := r.validateRows(ctx, rows); err != nil {
			return err
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
