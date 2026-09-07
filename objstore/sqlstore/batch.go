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
func (s *Store) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if err := objstore.ValidateBatch(checks, writes); err != nil {
		return false, err
	}
	// Every statement is generated before a lock is taken: a malformed batch is
	// a programming error, and it must not hold the writer while it is found.
	checkStmts := make([]*stmt, len(checks))
	for i, c := range checks {
		b, err := s.checkStmt(c)
		if err != nil {
			return false, err
		}
		checkStmts[i] = b
	}
	writeStmts := make([]*stmt, len(writes))
	guarded := make([]bool, len(writes))
	for i, w := range writes {
		b, g, err := s.writeStmt(w)
		if err != nil {
			return false, err
		}
		writeStmts[i], guarded[i] = b, g
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close() //nolint:errcheck // the pool reclaims it either way
	if _, err := conn.ExecContext(ctx, s.dialect.begin); err != nil {
		return false, fmt.Errorf("open the batch: %w", err)
	}
	committed, err := s.runBatch(ctx, conn, checkStmts, writeStmts, guarded)
	if err != nil || !committed {
		if _, rerr := conn.ExecContext(ctx, "ROLLBACK"); rerr != nil && err == nil {
			return false, fmt.Errorf("roll the refused batch back: %w", rerr)
		}
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, fmt.Errorf("commit the batch: %w", err)
	}
	return true, nil
}

// runBatch is the body inside the transaction, so GuardedBatch owns the ROLLBACK
// and COMMIT in one place. A false return is a REFUSAL — a condition did not
// hold — and never a fault.
func (s *Store) runBatch(ctx context.Context, conn *sql.Conn, checks, writes []*stmt, guarded []bool) (bool, error) {
	for _, b := range checks {
		var one int
		err := conn.QueryRowContext(ctx, b.String(), b.args...).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("check: %w", err)
		}
	}
	for i, b := range writes {
		res, err := conn.ExecContext(ctx, b.String(), b.args...)
		if err != nil {
			return false, fmt.Errorf("write: %w", err)
		}
		if !guarded[i] {
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
	case objstore.Delete:
		return s.deleteStmt(w)
	default:
		return nil, false, fmt.Errorf("unknown write op %d on %s", w.Op, w.Entity)
	}
}

// putStmt stores an item's whole attribute set.
//
// It carries no attribute condition: a Put replaces the document, so a condition
// on the document it replaces is a read-modify-write wearing a Put's name, and
// Update is that operation. What a Put may condition on is the item's existence,
// which is the create-if-absent every engine has.
func (s *Store) putStmt(w objstore.Write) (*stmt, bool, error) {
	if len(w.Cond.Where) > 0 || w.Cond.Item == objstore.ItemPresent {
		return nil, false, fmt.Errorf("put on %s/%s: a Put conditions only on the item's "+
			"existence; to change an item under a condition on its attributes, use Update",
			w.Entity, w.Key.Sort)
	}
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

// updateStmt merges attributes into an item that must already exist.
//
// Presence is implied rather than optional. SQL's UPDATE touches nothing when
// the row is missing and DynamoDB's UpdateItem CREATES it, so a driver-neutral
// Update has to pick one, and "the item must exist" is the one both engines can
// state exactly — `attribute_exists(pk)` there, a row count of zero here.
func (s *Store) updateStmt(w objstore.Write) (*stmt, bool, error) {
	if w.Cond.Item == objstore.ItemAbsent {
		return nil, false, fmt.Errorf("update on %s/%s: an Update always requires the item to "+
			"exist, so ItemAbsent can never hold", w.Entity, w.Key.Sort)
	}
	if len(w.Attrs) == 0 {
		return nil, false, fmt.Errorf("update on %s/%s sets nothing", w.Entity, w.Key.Sort)
	}
	patch, err := encodeAttrs(w.Attrs)
	if err != nil {
		return nil, false, fmt.Errorf("update on %s/%s: %w", w.Entity, w.Key.Sort, err)
	}
	b := &stmt{d: s.dialect}
	b.write("UPDATE " + w.Entity + " SET attrs = " + s.dialect.merge(b.mark(patch)))
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
