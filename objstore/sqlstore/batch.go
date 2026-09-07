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

// GuardedBatch applies every write or none of them.
//
// The transaction is opened as a WRITER before the first check runs. A check
// that read outside the write lock would be answering about a state the write
// can no longer assume, which is the whole failure the guarded transition
// exists to prevent.
func (s *Store) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (objstore.Outcome, error) {
	if err := objstore.ValidateBatch(checks, writes); err != nil {
		return objstore.Outcome{}, err
	}
	// Every statement is generated before a lock is taken: a malformed batch is
	// a programming error, and it must not hold the writer while it is found.
	checkStmts := make([]*stmt, len(checks))
	for i, c := range checks {
		b, err := s.checkStmt(c)
		if err != nil {
			return objstore.Outcome{}, err
		}
		checkStmts[i] = b
	}
	writeStmts := make([]*stmt, len(writes))
	guarded := make([]bool, len(writes))
	for i, w := range writes {
		b, g, err := s.writeStmt(w)
		if err != nil {
			return objstore.Outcome{}, err
		}
		writeStmts[i], guarded[i] = b, g
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return objstore.Outcome{}, err
	}
	defer conn.Close() //nolint:errcheck // the pool reclaims it either way
	if _, err := conn.ExecContext(ctx, s.dialect.begin); err != nil {
		return objstore.Outcome{}, fmt.Errorf("open the batch: %w", err)
	}
	out, err := s.runBatch(ctx, conn, checks, writes, checkStmts, writeStmts, guarded)
	if err != nil || !out.Committed {
		if _, rerr := conn.ExecContext(ctx, "ROLLBACK"); rerr != nil && err == nil {
			return objstore.Outcome{}, fmt.Errorf("roll the refused batch back: %w", rerr)
		}
		return out, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return objstore.Outcome{}, fmt.Errorf("commit the batch: %w", err)
	}
	return out, nil
}

// runBatch is the body inside the transaction, so GuardedBatch owns the ROLLBACK
// and COMMIT in one place. A refused Outcome is a REFUSAL — a condition did not
// hold — and never a fault.
//
// The pre-image is read INSIDE the transaction, before the rollback, so it is
// the state the condition was evaluated against and not whatever the row looks
// like by the time the caller asks. It costs one read on the refusal path and
// nothing on the committing one.
func (s *Store) runBatch(ctx context.Context, conn *sql.Conn,
	checks []objstore.Check, writes []objstore.Write,
	checkStmts, writeStmts []*stmt, guarded []bool) (objstore.Outcome, error) {
	for i, b := range checkStmts {
		var one int
		err := conn.QueryRowContext(ctx, b.String(), b.args...).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return s.refusal(ctx, conn, objstore.CheckRefused, i, checks[i].Entity, checks[i].Key)
		}
		if err != nil {
			return objstore.Outcome{}, fmt.Errorf("check: %w", err)
		}
	}
	for i, b := range writeStmts {
		res, err := conn.ExecContext(ctx, b.String(), b.args...)
		if err != nil {
			return objstore.Outcome{}, fmt.Errorf("write: %w", err)
		}
		if !guarded[i] {
			continue
		}
		n, err := res.RowsAffected()
		if err != nil {
			return objstore.Outcome{}, fmt.Errorf("write: %w", err)
		}
		if n == 0 {
			return s.refusal(ctx, conn, objstore.WriteRefused, i, writes[i].Entity, writes[i].Key)
		}
	}
	return objstore.Committed, nil
}

// refusal reads the item the condition was evaluated against and reports it with
// the refusal.
//
// This is what turns "false" into an answer. A claim that lost a race and a
// claim on a row that was already spent are the same bool and different
// incidents, and only the item the engine held tells them apart. DynamoDB gives
// the same thing through ReturnValuesOnConditionCheckFailure.
func (s *Store) refusal(ctx context.Context, conn *sql.Conn, kind objstore.RefusalKind,
	index int, entity string, key objstore.Key) (objstore.Outcome, error) {
	b := &stmt{d: s.dialect}
	b.write("SELECT attrs FROM " + entity + " WHERE part = ")
	b.bind(key.Partition)
	b.write(" AND sort = ")
	b.bind(key.Sort)
	var doc []byte
	err := conn.QueryRowContext(ctx, b.String(), b.args...).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		// The item is not there, which is a third answer again: nobody won a
		// race, the coordinate is simply empty.
		return objstore.Refused(kind, index, entity, key, nil), nil
	}
	if err != nil {
		return objstore.Outcome{}, fmt.Errorf("read the refused item %s/%s: %w",
			entity, key.Sort, err)
	}
	item := objstore.Item{Key: key, Attrs: map[string]string{}}
	if err := json.Unmarshal(doc, &item.Attrs); err != nil {
		return objstore.Outcome{}, fmt.Errorf("decode the refused item %s/%s: %w",
			entity, key.Sort, err)
	}
	return objstore.Refused(kind, index, entity, key, &item), nil
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
	case objstore.Delete:
		return s.deleteStmt(w)
	default:
		return nil, false, fmt.Errorf("unknown write op %d on %s", w.Op, w.Entity)
	}
}

// putStmt stores an item's whole attribute set, in one of three shapes.
//
// With no condition it is an upsert. With ItemAbsent it is the create-if-absent
// every engine has, and the engine's own uniqueness decides it. With a condition
// on the item's ATTRIBUTES it is a whole-document replace that requires the item
// to be there and to look a certain way — DynamoDB's conditional PutItem, which
// fails the same way against an item that is not there, because an absent item
// carries no attribute a condition could hold on.
//
// The third shape is an UPDATE rather than an upsert for exactly that reason: an
// INSERT … ON CONFLICT DO UPDATE … WHERE would take the INSERT branch when the
// item is missing, and commit the write the condition was there to refuse.
func (s *Store) putStmt(w objstore.Write) (*stmt, bool, error) {
	doc, err := encodeAttrs(w.Attrs)
	if err != nil {
		return nil, false, fmt.Errorf("put on %s/%s: %w", w.Entity, w.Key.Sort, err)
	}
	if len(w.Cond.Where) > 0 || w.Cond.Item == objstore.ItemPresent {
		if w.Cond.Item == objstore.ItemAbsent {
			return nil, false, fmt.Errorf("put on %s/%s asks the item to be absent AND to "+
				"carry attributes; an absent item carries none", w.Entity, w.Key.Sort)
		}
		b := &stmt{d: s.dialect}
		b.write("UPDATE " + w.Entity + " SET attrs = ")
		b.bind(doc)
		b.write(" WHERE part = ")
		b.bind(w.Key.Partition)
		b.write(" AND sort = ")
		b.bind(w.Key.Sort)
		if err := b.where(w.Cond.Where); err != nil {
			return nil, false, err
		}
		return b, true, nil
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
		doc = s.dialect.merge(b.mark(patch))
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
