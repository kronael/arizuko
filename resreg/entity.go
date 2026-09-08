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
	if r.Scope.Field != "" {
		// A scoped delete over an entity is an unbounded row set — read the
		// scope's items, then delete them — so it belongs inside the versioned
		// replace beside the wipe, not in a DeleteScope of its own. No
		// entity-backed resource declares a Scope, so the shape goes in with the
		// first one that does.
		panic(fmt.Sprintf("resreg: %s is entity-backed and declares a ScopeSpec; "+
			"a scoped delete over items is an unbounded read-then-delete and "+
			"belongs inside the replace", r.Name))
	}
	if len(r.StampedFields) > 0 {
		// The column path's auto-stamp fills a stamped field that is EMPTY, and
		// F49 made it skip one whose empty value stores a deliberate NULL. On an
		// object an empty string and a NULL are the same thing — the attribute is
		// absent — so that distinction has no representation here and the stamp
		// would move a checksum the operator changed nothing to move.
		panic(fmt.Sprintf("resreg: %s is entity-backed and declares StampedFields; "+
			"an absent attribute is the only null an item has, so server-OWNED "+
			"and server-INVENTED cannot be told apart on this path (BUGS F49)", r.Name))
	}
	if r.Hooks.BeforeInsert != nil || r.Hooks.AfterScan != nil ||
		r.Hooks.ValidateRow != nil || len(r.Hooks.ColumnOverride) > 0 {
		panic(fmt.Sprintf("resreg: %s is entity-backed and declares Hooks; "+
			"ColumnOverride.Read is a SQL expression and .Write is a bind "+
			"conversion the absent-attribute model already makes unnecessary, and "+
			"BeforeInsert/ValidateRow take the *sql.Tx this path does not open", r.Name))
	}
	e := &entityMeta{sort: sortFields, sep: r.EntityKeySep}
	inKey := map[int]bool{}
	for _, fm := range sortFields {
		inKey[fm.idx] = true
	}
	for _, fm := range m.fields {
		if inKey[fm.idx] {
			continue
		}
		switch k := m.rowType.Field(fm.idx).Type.Kind(); k {
		case reflect.String, reflect.Int:
		default:
			// Every attribute value is a string (objstore). A bool, a slice and a
			// map each need a rendering somebody has to choose and a reader has to
			// agree with, and no entity-backed resource carries one — so the
			// encoding goes in with the row that needs it rather than being
			// guessed here.
			panic(fmt.Sprintf("resreg: %s.%s is a %s and %s is entity-backed; an "+
				"attribute value is text, and this path renders string and int only",
				r.Name, fm.name, k, r.Name))
		}
		e.attrs = append(e.attrs, fm)
	}
	m.entity = e
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
// the row states rather than one it omits.
func (r *Resource) itemFor(v reflect.Value) (objstore.Write, error) {
	e := r.meta.entity
	sort, err := r.sortKeyFor(v)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := make(map[string]string, len(e.attrs))
	for _, fm := range e.attrs {
		f := v.Field(fm.idx)
		if f.Kind() == reflect.String {
			if s := f.String(); s != "" {
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
			if err := replaceRows(ctx, rp, rd, subsystem, manifestRows); err != nil {
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
// There is no scoped branch and no ApplyOpts.PruneScopes here, and neither is
// silently dropped: an entity-backed resource cannot declare a ScopeSpec
// (initEntity), so HasScope is false for every one of them, ManifestScopes
// answers nothing for such a subsystem, and the rollback that fills PruneScopes
// has nothing to put in it. The wipe is already total.
func replaceRows(ctx context.Context, rp *objstore.Replacer, rd reader, subsystem string,
	manifestRows map[string]any) error {
	for _, r := range BySubsystem(subsystem) {
		if r.RowType == nil || r.SkipApplyRebuild {
			continue
		}
		rows, mentioned := manifestRows[r.Name]
		if !mentioned {
			continue // resource absent from manifest → untouched
		}
		wipe, err := r.entityWipe(rd)
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
