package sqlstore

// batch.go generates the guarded write. Every statement it emits carries its
// own condition, so the engine decides the race rather than this package: a
// conditional UPDATE that changes no row and a conditional INSERT that inserts
// no row are both "someone else got there first", reported by the row count the
// engine returns and never by a read this driver performed itself.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// execer is what a batch's statements run on. A batch that opens its own
// transaction runs on the connection holding it; a batch that joins a caller's
// runs on that transaction. Both are the same statements in the same order, so
// the body below takes either and there is one implementation of a batch.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// plan is a batch's statements, generated before any lock is taken.
type plan struct {
	checks  []*stmt
	writes  []*stmt
	guarded []bool
}

// GuardedBatch applies every write or none of them.
//
// The transaction is opened as a WRITER before the first check runs. A check
// that read outside the write lock would be answering about a state the write
// can no longer assume, which is the whole failure the guarded transition
// exists to prevent.
func (s *Store) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	p, err := s.planBatch(checks, writes)
	if err != nil {
		return false, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close() //nolint:errcheck // the pool reclaims it either way
	if _, err := conn.ExecContext(ctx, s.dialect.begin); err != nil {
		return false, fmt.Errorf("open the batch: %w", err)
	}
	committed, err := s.runBatch(ctx, conn, p)
	if err != nil || !committed {
		if _, rerr := conn.ExecContext(ctx, "ROLLBACK"); rerr != nil && err == nil {
			return false, fmt.Errorf("roll the refused batch back: %w", rerr)
		}
		return committed, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, fmt.Errorf("commit the batch: %w", err)
	}
	return true, nil
}

// GuardedBatchInTx applies the same batch inside a transaction the CALLER owns,
// and it is deliberately not on objstore.Store: an engine with no interactive
// transaction has nothing to implement, and a caller that reached for this
// through the interface would be writing code only this driver can run.
//
// It exists for the audited write. An audit row commits or rolls back with the
// mutation it records, and a caller that still runs its mutation as SQL holds
// that mutation on a *sql.Tx — so the row joins the caller's transaction rather
// than opening a second one, which could not be atomic with it. When such a
// caller moves its mutation onto Query and GuardedBatch, its audit row rides in
// the same batch and this entry point loses that caller.
//
// TWO THINGS ARE THE CALLER'S, and both follow from the caller owning the
// transaction. The isolation is whatever the caller's BeginTx took, not the
// writer lock GuardedBatch opens with; and a refusal — committed=false — leaves
// the writes before it applied INSIDE the caller's transaction, so a caller that
// does not roll back on a refusal commits half a batch.
func (s *Store) GuardedBatchInTx(ctx context.Context, tx *sql.Tx, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	if tx == nil {
		return false, errors.New("batch in a transaction that is nil")
	}
	p, err := s.planBatch(checks, writes)
	if err != nil {
		return false, err
	}
	return s.runBatch(ctx, tx, p)
}

// planBatch validates the batch and generates every statement it runs.
//
// Generation happens before a lock is taken: a malformed batch is a programming
// error, and it must not hold the writer while it is found.
func (s *Store) planBatch(checks []objstore.Check, writes []objstore.Write) (plan, error) {
	if err := objstore.ValidateBatch(checks, writes); err != nil {
		return plan{}, err
	}
	p := plan{
		checks:  make([]*stmt, len(checks)),
		writes:  make([]*stmt, len(writes)),
		guarded: make([]bool, len(writes)),
	}
	for i, c := range checks {
		b, err := s.checkStmt(c)
		if err != nil {
			return plan{}, err
		}
		p.checks[i] = b
	}
	for i, w := range writes {
		b, g, err := s.writeStmt(w)
		if err != nil {
			return plan{}, err
		}
		p.writes[i], p.guarded[i] = b, g
	}
	return p, nil
}

// runBatch is the body inside the transaction, whoever opened it, so the two
// entry points above run the identical statements in the identical order.
// committed=false is a REFUSAL — a condition did not hold — and never a fault.
func (s *Store) runBatch(ctx context.Context, on execer, p plan) (bool, error) {
	for _, b := range p.checks {
		var one int
		err := on.QueryRowContext(ctx, b.String(), b.args...).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("check: %w", err)
		}
	}
	for i, b := range p.writes {
		res, err := on.ExecContext(ctx, b.String(), b.args...)
		if err != nil {
			return false, fmt.Errorf("write: %w", err)
		}
		if !p.guarded[i] {
			continue
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("write: %w", err)
		}
		if n == 0 {
			return false, nil
		}
	}
	return true, nil
}

// checkStmt generates the read that decides one check.
//
// ItemAbsent is refused rather than implemented. On SQLite the writer lock makes
// it exact, but Postgres runs at READ COMMITTED and FOR UPDATE locks the rows a
// query FOUND — there are none — so a concurrent insert between this read and
// the batch's writes is invisible. A caller that wants "this item must not
// exist" writes it as a Put with ItemAbsent, where the engine's own uniqueness
// enforces it on both engines. Offering a check the SQL driver cannot honour is
// the drift the interface refuses.
func (s *Store) checkStmt(c objstore.Check) (*stmt, error) {
	if err := checkIdent("entity", c.Entity); err != nil {
		return nil, err
	}
	switch c.Cond.Item {
	case objstore.ItemAbsent:
		return nil, fmt.Errorf("check on %s/%s: ItemAbsent has no atomic read; write it as a "+
			"Put with ItemAbsent, whose uniqueness the engine enforces", c.Entity, c.Key.Sort)
	case objstore.ItemAny:
		if len(c.Cond.Where) == 0 {
			return nil, fmt.Errorf("check on %s/%s asks nothing", c.Entity, c.Key.Sort)
		}
	}
	b := &stmt{d: s.dialect}
	b.write("SELECT 1 FROM " + c.Entity + " WHERE part = ")
	b.bind(c.Key.Partition)
	b.write(" AND sort = ")
	b.bind(c.Key.Sort)
	if err := b.where(c.Cond.Where); err != nil {
		return nil, err
	}
	b.write(s.dialect.lockRead)
	return b, nil
}

// writeStmt generates one mutation and reports whether its row count decides the
// batch. An unguarded write — an upsert, an idempotent delete — always touches
// what it meant to, so its row count answers nothing.
func (s *Store) writeStmt(w objstore.Write) (*stmt, bool, error) {
	if err := checkIdent("entity", w.Entity); err != nil {
		return nil, false, err
	}
	switch w.Op {
	case objstore.Put:
		return s.putStmt(w)
	case objstore.Update:
		return s.updateStmt(w)
	case objstore.Merge:
		return s.mergeStmt(w)
	case objstore.Delete:
		return s.deleteStmt(w)
	default:
		return nil, false, fmt.Errorf("unknown write op %d on %s", w.Op, w.Entity)
	}
}

// putStmt stores an item's whole attribute set, in one of two shapes.
//
// With no condition it is an upsert. With ItemAbsent it is the create-if-absent
// every engine has, and the engine's own uniqueness decides it — never a read
// this driver performed. ValidateBatch has already refused any other condition,
// so there is no third shape to generate.
func (s *Store) putStmt(w objstore.Write) (*stmt, bool, error) {
	doc, err := encodeAttrs(w.Attrs)
	if err != nil {
		return nil, false, fmt.Errorf("put on %s/%s: %w", w.Entity, w.Key.Sort, err)
	}
	b := &stmt{d: s.dialect}
	b.write("INSERT INTO " + w.Entity + " (part, sort, attrs) VALUES (")
	b.bind(w.Key.Partition)
	b.write(", ")
	b.bind(w.Key.Sort)
	b.write(", ")
	b.bind(doc)
	b.write(") ON CONFLICT (part, sort) DO ")
	if w.Cond.Item == objstore.ItemAbsent {
		b.write("NOTHING")
		return b, true, nil
	}
	b.write("UPDATE SET attrs = excluded.attrs")
	return b, false, nil
}

// updateStmt merges attributes into an item that must already exist, and takes
// the named ones away.
//
// Presence is implied rather than optional. SQL's UPDATE touches nothing when
// the row is missing and DynamoDB's UpdateItem CREATES it, so a driver-neutral
// Update has to pick one, and "the item must exist" is the one both engines can
// state exactly — `attribute_exists(pk)` there, a row count of zero here.
//
// The two halves compose into ONE document expression, which is what makes a
// five-attribute reset atomic. Their names are disjoint (ValidateBatch refuses
// the overlap DynamoDB also refuses), so merging before removing and removing
// before merging give the same document, and neither can leave the item half
// reset.
func (s *Store) updateStmt(w objstore.Write) (*stmt, bool, error) {
	if w.Cond.Item == objstore.ItemAbsent {
		return nil, false, fmt.Errorf("update on %s/%s: an Update always requires the item to "+
			"exist, so ItemAbsent can never hold", w.Entity, w.Key.Sort)
	}
	if len(w.Attrs) == 0 && len(w.Remove) == 0 {
		return nil, false, fmt.Errorf("update on %s/%s sets nothing", w.Entity, w.Key.Sort)
	}
	for _, name := range w.Remove {
		if err := checkIdent("attribute", name); err != nil {
			return nil, false, err
		}
	}
	patch, err := encodeAttrs(w.Attrs)
	if err != nil {
		return nil, false, fmt.Errorf("update on %s/%s: %w", w.Entity, w.Key.Sort, err)
	}
	b := &stmt{d: s.dialect}
	b.write("UPDATE " + w.Entity + " SET attrs = ")
	doc := "attrs"
	if len(w.Attrs) > 0 {
		doc = s.dialect.merge(doc, b.mark(patch))
	}
	if len(w.Remove) > 0 {
		doc = s.dialect.remove(doc, w.Remove)
	}
	b.write(doc)
	b.write(" WHERE part = ")
	b.bind(w.Key.Partition)
	b.write(" AND sort = ")
	b.bind(w.Key.Sort)
	if err := b.where(w.Cond.Where); err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// mergeStmt merges attributes into an item and CREATES it when there is none,
// in one statement, which is the whole reason this op exists: the pair it
// replaces was two actions on one item, and a batch may name an item once.
//
// The insert carries Init and Attrs together — what the item is born with — and
// the conflict branch carries Attrs alone, so a create-only attribute is stated
// exactly where it applies. DynamoDB writes the same split as
// `SET a = :a, b = if_not_exists(b, :b)` in one update expression.
//
// A merge with nothing to merge is DO NOTHING rather than a write of the
// unchanged document: that is `INSERT OR IGNORE`, and it is the shape an edge
// writer needs — land the row, leave an existing one alone, and do not make the
// row count an answer.
//
// THE DOCUMENT IS QUALIFIED, and that is not decoration: Postgres refuses a bare
// `attrs` inside DO UPDATE as ambiguous between the existing row and the
// proposed one ("column reference \"attrs\" is ambiguous", measured on 17.11),
// and SQLite accepts the same qualified name, so one spelling serves both.
//
// It reports FALSE for guarded, always. A merge cannot refuse, so its row count
// answers nothing and a batch carrying one is decided by its other writes.
func (s *Store) mergeStmt(w objstore.Write) (*stmt, bool, error) {
	born := make(map[string]string, len(w.Init)+len(w.Attrs))
	for name, value := range w.Init {
		born[name] = value
	}
	for name, value := range w.Attrs {
		born[name] = value
	}
	doc, err := encodeAttrs(born)
	if err != nil {
		return nil, false, fmt.Errorf("merge on %s/%s: %w", w.Entity, w.Key.Sort, err)
	}
	for _, name := range w.Remove {
		if err := checkIdent("attribute", name); err != nil {
			return nil, false, err
		}
	}
	b := &stmt{d: s.dialect}
	b.write("INSERT INTO " + w.Entity + " (part, sort, attrs) VALUES (")
	b.bind(w.Key.Partition)
	b.write(", ")
	b.bind(w.Key.Sort)
	b.write(", ")
	b.bind(doc)
	b.write(") ON CONFLICT (part, sort) DO ")
	if len(w.Attrs) == 0 && len(w.Remove) == 0 {
		b.write("NOTHING")
		return b, false, nil
	}
	patch, err := encodeAttrs(w.Attrs)
	if err != nil {
		return nil, false, fmt.Errorf("merge on %s/%s: %w", w.Entity, w.Key.Sort, err)
	}
	held := w.Entity + ".attrs"
	if len(w.Attrs) > 0 {
		held = s.dialect.merge(held, b.mark(patch))
	}
	if len(w.Remove) > 0 {
		held = s.dialect.remove(held, w.Remove)
	}
	b.write("UPDATE SET attrs = " + held)
	return b, false, nil
}

// deleteStmt removes an item. With no condition it is idempotent, which is what
// a delete means on every engine; with one, a row count of zero refuses the
// batch.
func (s *Store) deleteStmt(w objstore.Write) (*stmt, bool, error) {
	if w.Cond.Item == objstore.ItemAbsent {
		return nil, false, fmt.Errorf("delete on %s/%s: ItemAbsent asks the engine to delete "+
			"what is not there", w.Entity, w.Key.Sort)
	}
	b := &stmt{d: s.dialect}
	b.write("DELETE FROM " + w.Entity + " WHERE part = ")
	b.bind(w.Key.Partition)
	b.write(" AND sort = ")
	b.bind(w.Key.Sort)
	if err := b.where(w.Cond.Where); err != nil {
		return nil, false, err
	}
	return b, w.Cond.Item == objstore.ItemPresent || len(w.Cond.Where) > 0, nil
}

// encodeAttrs renders an attribute set as the stored document. json.Marshal
// sorts map keys, so the same attributes always render the same bytes.
func encodeAttrs(attrs map[string]string) (string, error) {
	if attrs == nil {
		attrs = map[string]string{}
	}
	for name := range attrs {
		if err := checkIdent("attribute", name); err != nil {
			return "", err
		}
	}
	doc, err := json.Marshal(attrs)
	if err != nil {
		return "", err
	}
	return string(doc), nil
}
