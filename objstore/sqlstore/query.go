package sqlstore

// query.go generates the read, and the predicate fragments the write path
// shares with it. There is ONE renderer for a key predicate and one for an
// attribute comparison; a check, a conditional write and a query all reach
// them, so a fix to containment cannot land on some paths and miss others.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/kronael/arizuko/objstore"
)

// Query reads every item q selects.
//
// The whole selection, in one statement. A SQL engine has no page cap of its
// own, so this driver pages by not needing to: the rows stream and the caller
// gets all of them. An engine that answers a read a page at a time walks its
// own cursor here instead — the completeness is the contract, and where the
// loop lives is the driver's business (objstore.Store).
//
// Limit is therefore a plain SQL LIMIT: the bound is on the rows RETURNED, and
// the filter is a WHERE term the engine applies before counting them.
func (s *Store) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	return s.query(ctx, s.db, q)
}

// QueryInTx answers the same read inside a transaction the CALLER owns, and it
// is deliberately not on objstore.Store for the reason GuardedBatchInTx is not:
// an engine with no interactive transaction has nothing to implement, and a
// caller reaching for this through the interface would write code only this
// driver can run.
//
// IT EXISTS FOR THE WRITER THAT READS ITS OWN DECISION. A group delete queries
// each scope and deletes what it finds; a membership write walks the graph and
// then adds an edge; an upsert reads the settings document it is about to merge
// into. Every one of those runs on a transaction the caller opened, and reading
// through the POOL instead is wrong twice over. It is a torn read — the writes
// this transaction has already made are not visible, so the delete set misses
// the row the same transaction just wrote. And on the engine the tests run,
// SQLite in shared-cache memory, it does not merely tear: the transaction holds
// the write lock and a read on another connection of the same cache waits for
// it, so the caller waits for itself. That is a hang, not an error, and it is
// how `arizuko group rm` and every group creation stopped answering.
//
// The isolation is whatever the caller's BeginTx took, which is the same thing
// GuardedBatchInTx says about its writes.
func (s *Store) QueryInTx(ctx context.Context, tx *sql.Tx, q objstore.Query) ([]objstore.Item, error) {
	if tx == nil {
		return nil, errors.New("query in a transaction that is nil")
	}
	return s.query(ctx, tx, q)
}

// querier is the read subset *sql.DB and *sql.Tx both satisfy, so one statement
// generator serves the pool and a caller's transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) query(ctx context.Context, q0 querier, q objstore.Query) ([]objstore.Item, error) {
	if err := objstore.ValidateQuery(q); err != nil {
		return nil, err
	}
	if err := checkIdent("entity", q.Entity); err != nil {
		return nil, err
	}
	b := &stmt{d: s.dialect}
	b.write("SELECT part, sort")
	for _, name := range q.Project {
		if err := checkIdent("attribute", name); err != nil {
			return nil, err
		}
		b.write(", " + s.dialect.attr(name))
	}
	if len(q.Project) == 0 {
		b.write(", attrs")
	}
	b.write(" FROM " + q.Entity + " WHERE part = ")
	b.bind(q.Partition)
	if err := b.keyPredicate(q.Key); err != nil {
		return nil, err
	}
	if err := b.where(q.Where); err != nil {
		return nil, err
	}
	b.write(" ORDER BY sort")
	if q.Descending {
		b.write(" DESC")
	}
	if q.Limit > 0 {
		b.write(" LIMIT " + strconv.Itoa(q.Limit))
	}

	rows, err := q0.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", q.Entity, err)
	}
	defer rows.Close()
	items, err := drain(rows, q)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", q.Entity, err)
	}
	return items, nil
}

// drain reads every row the statement returned.
func drain(rows *sql.Rows, q objstore.Query) ([]objstore.Item, error) {
	items := []objstore.Item{}
	for rows.Next() {
		item, err := scanItem(rows, q)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// scanItem reads one row into an item. A projected attribute the item does not
// carry comes back NULL and stays ABSENT from the map, so a projected read and
// a whole read report a missing attribute the same way.
func scanItem(rows *sql.Rows, q objstore.Query) (objstore.Item, error) {
	item := objstore.Item{Attrs: map[string]string{}}
	var (
		doc   []byte
		cells = make([]sql.NullString, len(q.Project))
		dest  = make([]any, 0, len(q.Project)+3)
	)
	dest = append(dest, &item.Key.Partition, &item.Key.Sort)
	if len(q.Project) == 0 {
		dest = append(dest, &doc)
	}
	for i := range cells {
		dest = append(dest, &cells[i])
	}
	if err := rows.Scan(dest...); err != nil {
		return item, err
	}
	if len(q.Project) == 0 {
		if err := json.Unmarshal(doc, &item.Attrs); err != nil {
			return item, fmt.Errorf("decode the document at %q: %w", item.Key.Sort, err)
		}
		return item, nil
	}
	for i, name := range q.Project {
		if cells[i].Valid {
			item.Attrs[name] = cells[i].String
		}
	}
	return item, nil
}

// keyPredicate appends the sort-key selection, opening with " AND " when it
// selects anything at all.
//
// PrefixKey is an exact character comparison, never LIKE. LIKE reads `_` and
// `%` as wildcards on both engines and is case-insensitive for ASCII on SQLite,
// so folder `a_b` would reach `aXb` and folder `coach` would reach `Coach`
// (BUGS L66, L122, L252). substr comparison has no metacharacters at all, and
// the length is a RUNE count because both engines' substr counts characters.
//
// The self-match disjunct is what makes a subtree include its own root: `a`
// contains `a` and everything under `a/`, and not `ab`.
//
// The four ordered operators need no such care: `<`, `<=`, `>` and `>=` are the
// same comparison in both dialects on a TEXT column.
func (b *stmt) keyPredicate(k objstore.KeyPredicate) error {
	cmp := func(op, value string) {
		b.write(" AND sort " + op + " ")
		b.bind(value)
	}
	switch k.Op {
	case objstore.AnyKey:
		return nil
	case objstore.ExactKey:
		cmp("=", k.Value)
	case objstore.LtKey:
		cmp("<", k.Value)
	case objstore.LeKey:
		cmp("<=", k.Value)
	case objstore.GtKey:
		cmp(">", k.Value)
	case objstore.GeKey:
		cmp(">=", k.Value)
	case objstore.PrefixKey:
		prefix := k.Value + k.Sep
		b.write(" AND (sort = ")
		b.bind(k.Value)
		b.write(" OR substr(sort, 1, ")
		b.bind(utf8.RuneCountInString(prefix))
		b.write(") = ")
		b.bind(prefix)
		b.write(")")
	default:
		return fmt.Errorf("unknown key predicate %d", k.Op)
	}
	return nil
}

// conjunction renders the attribute comparisons as ONE boolean expression and
// returns it, binding its values into this statement in the order they are
// written.
//
// It returns the text instead of appending it so that `where` owns the
// parenthesis and the leading AND: a filter on a read and a condition on a
// write reach the identical renderer, so a fix to a comparison cannot land on
// the write path and miss the read.
//
// A comparison against an attribute the item does not carry yields NULL on both
// engines, which is not TRUE, so the row is excluded — the same answer Absent
// states explicitly.
func (b *stmt) conjunction(cmps []objstore.Cmp) (string, error) {
	held := b.sql
	b.sql = nil
	defer func() { b.sql = held }()
	for i, c := range cmps {
		if err := checkIdent("attribute", c.Attr); err != nil {
			return "", err
		}
		if i > 0 {
			b.write(" AND ")
		}
		b.write(b.d.attr(c.Attr))
		switch c.Op {
		case objstore.Absent:
			b.write(" IS NULL")
			continue
		case objstore.Present:
			b.write(" IS NOT NULL")
			continue
		case objstore.Eq:
			b.write(" = ")
		case objstore.Ne:
			b.write(" <> ")
		case objstore.Lt:
			b.write(" < ")
		case objstore.Le:
			b.write(" <= ")
		case objstore.Gt:
			b.write(" > ")
		case objstore.Ge:
			b.write(" >= ")
		default:
			return "", fmt.Errorf("unknown comparison %d on attribute %q", c.Op, c.Attr)
		}
		b.bind(c.Value)
	}
	return string(b.sql), nil
}

// where appends the attribute comparisons as further AND terms of a WHERE
// clause.
func (b *stmt) where(cmps []objstore.Cmp) error {
	if len(cmps) == 0 {
		return nil
	}
	expr, err := b.conjunction(cmps)
	if err != nil {
		return err
	}
	b.write(" AND (" + expr + ")")
	return nil
}
