package resreg

// entity.go is the OBJECT path: how the engine reads and writes a resource
// whose rows are objstore items (spec 6/37 step 6, BUGS L259).
//
// THERE IS ONE SHAPE PER RESOURCE AND THE RESOURCE DECLARES IT
// (Resource.EntitySortKey); the engine never probes the storage and never
// falls back, because two paths for one table drift. THE COORDINATE IS A
// LIST joined by a declared separator, and a value carrying the separator is
// refused at WRITE (spec 6/40 §1). An attribute an item does not carry is
// ABSENT, the only null, so a nullable value needs no COALESCE and no
// ColumnOverride. WHAT IT REFUSES it refuses at Register, so a misdeclared
// resource stops the binary rather than answering a wrong row later.

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

// On is the Backend for one owner's tables; every resource an owner
// subsystem declares is entity-shaped (resreg/resources/shape_test.go).
func On(obj objstore.Store) Backend { return Backend{Obj: obj} }

// Backend is one owner's tables, as the handle every resource is read and
// written through.
type Backend struct {
	Obj objstore.Store
}

// reader is one read's handle.
type reader struct {
	obj objstore.Store
}

// read is the reader for a whole Backend.
func (b Backend) read() reader { return reader{obj: b.Obj} }

// entityMeta is the item half of the reflection Register does, built only for
// a resource that declared EntitySortKey.
type entityMeta struct {
	// sort is the fields whose values, joined by sep, ARE the item's sort key.
	// None of them is among attrs: storing the coordinate twice is two facts
	// that can disagree.
	sort []fieldMeta
	// sep joins them, and is empty for a single-field key.
	sep string
	// attrs is every other `db:`-tagged field, one attribute each.
	attrs []fieldMeta
	// stamped is the struct-field index of every StampedField the writer may
	// INVENT: resourceMeta.autoStampIdx, not stampedIdx (BUGS F49).
	stamped map[int]bool
	// scope is how a scoped delete SELECTS one scope's items, nil for a
	// resource that declares no ScopeSpec.
	scope *entityScope
	// filtered is the struct-field index of every attribute the RowFilter
	// compares. itemFor writes those even when EMPTY: an absent attribute
	// answers no comparison, so a row that left one off would write itself
	// out of its own projection.
	filtered map[int]bool
}

// entityScope is the read that stands in for a scoped delete. An entity has
// no predicate delete, so a scoped delete is a QUERY and then one guarded
// delete per item, which is why it belongs inside the versioned replace (spec
// 6/40 §3). Three shapes, picked by the DECLARATION at Register: an attribute
// is a Where term; a sort key is an exact key; the first field of a composite
// coordinate is a begins_with on `scope+sep`, exact because no value may
// carry the separator.
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

// initEntity builds the item metadata, or refuses the declaration with a
// panic at Register.
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
	// A separator beside a single field is refused rather than ignored: an
	// ignored declaration is one a later reader believes.
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
		// A coordinate is TEXT compared as text, so "10" sorts before "2"; the
		// pad codec is the only rendering a number in a coordinate takes.
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
	// A filter is a Where over ATTRIBUTES and the coordinate is the key, so a
	// filter on a sort field is refused rather than answered by a read that
	// ignores it.
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
			// A stamped coordinate would address a different item on every apply.
			panic(fmt.Sprintf("resreg: %s stamps %q and names it in EntitySortKey; a "+
				"coordinate the writer invents addresses a new item on every write",
				r.Name, name))
		}
	}
	// PKFields must name every sort field (it may name more), or Diff indexes
	// two items under one PK and reports whichever it read last (BUGS L260).
	inPK := nameSet(r.PKFields)
	for _, name := range r.EntitySortKey {
		if !inPK[name] {
			panic(fmt.Sprintf("resreg: %s keys items on %v and declares PKFields %v, which "+
				"omits %q; two items would share one manifest identity and Diff would "+
				"report one of them", r.Name, r.EntitySortKey, r.PKFields, name))
		}
	}
	if r.Hooks.AfterScan != nil || len(r.Hooks.ColumnOverride) > 0 {
		// These two ask for something an item has not got: a read expression
		// with no column to name, and a post-scan decode a declared codec does
		// instead (codec.go). BeforeInsert and ValidateRows are fine.
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
			// The padding orders a KEY; a number stored as an attribute already
			// has a rendering.
			panic(fmt.Sprintf("resreg: %s.%s declares the %q encoding and is not part of "+
				"the coordinate (%s); padding orders a KEY, and an attribute that is a "+
				"number is already rendered the same way on both shapes",
				r.Name, fm.name, padCodec, strings.Join(r.EntitySortKey, ",")))
		}
		switch k := m.rowType.Field(fm.idx).Type.Kind(); {
		case k == reflect.String, k == reflect.Int, fm.codec != nil:
		default:
			// Every attribute value is a string; a bool, slice or map DECLARES its
			// rendering in the `db:` tag (codec.go) or is refused rather than
			// stored as whatever fmt.Sprint made of it.
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

// entityScopeOf resolves the ScopeSpec into the predicate a scoped delete
// runs, or refuses the declaration: a scope no query can SELECT on must not be
// answered by a wider delete that takes a folder's rows the manifest never
// mentioned.
func (r *Resource) entityScopeOf(m *resourceMeta, sortFields []fieldMeta) *entityScope {
	if m.scopeField == nil {
		return nil
	}
	if r.Scope.Column != "" {
		// The attribute name IS the `db:` tag; a second spelling would select
		// nothing, silently.
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
		// A begins_with reaches a leading fragment only, so a scope in the
		// middle of a coordinate has no predicate at all.
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
		// An empty string is an ABSENT attribute (itemFor), so the instance-wide
		// scope "" is selected by Absent; Eq "" matches no item and would leave
		// the scope's rows behind under the puts.
		q.Where = append([]objstore.Cmp{{Attr: e.scope.attr, Op: objstore.Absent}}, q.Where...)
	case e.scope.attr != "":
		q.Where = append([]objstore.Cmp{{Attr: e.scope.attr, Op: objstore.Eq, Value: scope}}, q.Where...)
	case e.scope.prefix:
		// The separator is part of the prefix, not Query.Key.Sep: the subtree
		// separator would match the value ITSELF, which is no item's key here.
		q.Key = objstore.KeyPredicate{Op: objstore.PrefixKey, Value: scope + e.sep}
	default:
		q.Key = objstore.KeyPredicate{Op: objstore.ExactKey, Value: scope}
	}
	return q
}

// entityScopeDeletes reads one scope's live items and renders the writes that
// remove them.
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

// IsEntity reports the shape a resource declared; it reads the declaration,
// never the storage.
func (r *Resource) IsEntity() bool { return r.meta != nil && r.meta.entity != nil }

// entityPartition is the partition every one of a resource's items lives in:
// the owner subsystem (EntitySortKey).
func (r *Resource) entityPartition() string { return r.DB }

// scanEntities reads every item of the resource, in sort-key order. EmitRows
// sorts by PK before emitting and Diff indexes by PK, so nothing downstream
// depends on that order.
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

// rowFrom builds one row from one stored item; the sort-key fields come back
// out of the KEY. A composite key that does not split into exactly as many
// parts as there are fields is a LOUD fault: half a coordinate names a row
// nobody wrote. An absent attribute leaves its field at the zero value; a
// numeric attribute that is not a number is an ERROR, since reading it as 0
// would put a wrong `max_uses` into an export the operator then applies.
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
// join of the several that make it up. A value CARRYING the separator is
// refused: joined, two rows would address one item. An EMPTY part is allowed
// (network_rules spells an instance-wide rule folder="") and an empty whole
// key is not.
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

// itemFor renders one row as the item it is stored as. An empty STRING
// attribute is left OFF the item, because absent is the only null and an
// empty string is a value a tombstone test still finds. An INT and a CODEC'D
// field are always written: their zero value is a value the row states. A
// STAMPED field the row leaves empty is filled HERE with core.TS, the
// fixed-width form that makes byte order and time order the same (BUGS L97).
func (r *Resource) itemFor(v reflect.Value) (objstore.Write, error) {
	e := r.meta.entity
	if r.Hooks.BeforeInsert != nil {
		// The row is COPIED first: the hook mutates a pointer, and filling a
		// default in place would edit the manifest the apply is still reading.
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

// entityPuts renders a `[]RowType` as the writes that store it. A row set
// naming one coordinate twice is refused here, over the WHOLE set, because a
// replace splits its writes across batches (objstore.Replace).
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
// routes.id is why it exists: the id is the handle `delete_route(id)`
// addresses, a live agent wire contract, and a manifest need not state one.
// The number is one past the LARGEST the set already states, so a set that
// carries its ids round-trips them unchanged. The set is the whole answer
// because the apply wipes the resource first; initEntity refuses the scoped
// pairing that would break that. Zero is the unset value: no live row
// carries 0.
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
	// The rows belong to the caller's slice, which every later checksum and
	// Diff read, so the mint lands in a copy.
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

// entityWipe reads the resource's live items and renders the writes that
// remove them. Reading inside the replace's claim keeps a second replace's
// rows out of this one's delete set (objstore.Replace).
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
// scopes' items for a scoped resource with rows, and every item otherwise. An
// explicitly-present empty list is the operator declaring "no rows" and falls
// to the total wipe.
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

// requireEntities refuses a subsystem holding a resource that declares no
// entity shape, per SUBSYSTEM: Apply replaces the whole subsystem's projection
// under one version marker, and a column-shaped resource beside that would
// leave the operator unable to say which half their apply landed in.
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
// THERE: store holds the writers whose every mutation has to move that
// subsystem's marker, and store cannot import this package (BUGS L264).

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureSubsystem prepares a database to serve one subsystem's entity-backed
// resources: the entity behind each, the entity the version marker lives in,
// and the marker itself. The marker has to exist before any write: Bump is an
// Update, and an Update against a missing item refuses the WHOLE batch
// (objstore.EnsureScope). It creates nothing for a subsystem no entity-backed
// resource names, so an opener calls it unconditionally.
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

// applyEntities is Apply as the versioned replace of spec 6/40 §3. There is
// no pre-image and no rollback: a replace that dies after writing leaves the
// subsystem PART-APPLIED with its claim held, and recovery is to run the same
// apply again. THE ROLL-FORWARD IS THE SAME CALL: re-running carries the
// intent the interrupted apply recorded, so it takes that claim over instead
// of answering ErrScopeHeld for good (BUGS L267), and it converges because
// every delete set is re-read live under the claim. The claim gates replaces,
// not ordinary writers.
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
			// Read under the claim, so the checksum is the state the writes below
			// replace and not one a second replace moved.
			if current, err = checksum(rd, subsystem); err != nil {
				return fmt.Errorf("checksum: %w", err)
			}
			// A takeover has no pre-image left to compare: the CAS would refuse
			// the only action that can settle the scope.
			if !force && !rp.TookOver() && current != wantChecksum {
				// Before any write, so Replace releases the claim.
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

// applyClaim renders what one apply is about to write, so a later apply of
// the same work can take a claim this one leaves behind (objstore.Claim). It
// is DERIVED from the document and the pruned scopes, never caller-chosen: a
// fresh value per run could never take its own claim back, and a constant
// would take over an apply whose rows this one does not cover. `force` and
// wantChecksum are absent because neither changes a single write.
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

// replaceRows issues one subsystem's deletes and puts under the claim, in
// catalog order. The wipe and the puts are TWO calls: a delete and the put
// over the same coordinate cannot ride one call (objstore.Replacer.Write). The
// SCOPED branch exists because a manifest naming two folders must not take a
// third folder's rows, and a folder purge that leaves rows behind hands the
// next tenant of the path the last one's egress rules and web routes (BUGS
// L253).
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
