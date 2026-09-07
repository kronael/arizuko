package sqlstore

// query.go generates the read, and the predicate fragments the write path
// shares with it. There is ONE renderer for a key predicate and one for an
// attribute comparison; a check, a conditional write and a query all reach
// them, so a fix to containment cannot land on some paths and miss others.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/kronael/arizuko/objstore"
)

// Query reads one page of the items q selects.
//
// The page bound is applied to the items the read EXAMINES, before the filter
// narrows them, because that is where DynamoDB applies it: "This limit applies
// before any FilterExpression or ProjectionExpression is applied to the
// results." So the filter cannot be a SQL WHERE term — a WHERE would let the
// engine discard rows this driver still has to count against the bound, and the
// same read would then come back complete on SQL and partial on the other
// engine, at a fixture size no unit test reaches.
//
// It is emitted as a SELECTED COLUMN instead: the engine still evaluates the
// comparison, so a projection still leaves what it does not name inside the
// engine, and this driver still learns the verdict per row. The cost is that a
// filtered read walks its key selection, which is what a filter costs on every
// engine and the reason a selective predicate belongs on the sort key.
func (s *Store) Query(ctx context.Context, q objstore.Query) (objstore.Page, error) {
	if err := objstore.ValidateQuery(q); err != nil {
		return objstore.Page{}, err
	}
	if err := checkIdent("entity", q.Entity); err != nil {
		return objstore.Page{}, err
	}
	b := &stmt{d: s.dialect}
	b.write("SELECT part, sort")
	for _, name := range q.Project {
		if err := checkIdent("attribute", name); err != nil {
			return objstore.Page{}, err
		}
		b.write(", " + s.dialect.attr(name))
	}
	if len(q.Project) == 0 {
		b.write(", attrs")
	}
	b.write(", " + s.dialect.docBytes)
	if len(q.Where) > 0 {
		b.write(", ")
		keep, err := b.conjunction(q.Where)
		if err != nil {
			return objstore.Page{}, err
		}
		b.write(s.dialect.boolInt(keep))
	}
	b.write(" FROM " + q.Entity + " WHERE part = ")
	b.bind(q.Partition)
	if err := b.keyPredicate(q.Key); err != nil {
		return objstore.Page{}, err
	}
	b.after(q.After, q.Descending)
	b.write(" ORDER BY sort")
	if q.Descending {
		b.write(" DESC")
	}
	if q.Limit > 0 {
		b.write(" LIMIT " + strconv.Itoa(q.Limit))
	}

	rows, err := s.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return objstore.Page{}, fmt.Errorf("query %s: %w", q.Entity, err)
	}
	defer rows.Close()
	page, err := drain(rows, q)
	if err != nil {
		return objstore.Page{}, fmt.Errorf("query %s: %w", q.Entity, err)
	}
	return page, nil
}

// drain reads rows until the page bound stops it, keeping the ones the filter
// column marked.
//
// The bound is checked AFTER the item is taken, so a page always makes progress:
// an item bigger than the whole budget would otherwise be unreadable, and the
// cursor would point at it forever.
func drain(rows *sql.Rows, q objstore.Query) (objstore.Page, error) {
	page := objstore.Page{Items: []objstore.Item{}}
	var bytes int
	last := ""
	for rows.Next() {
		item, size, keep, err := scanItem(rows, q)
		if err != nil {
			return objstore.Page{}, err
		}
		page.Examined++
		bytes += size
		last = item.Key.Sort
		if keep {
			page.Items = append(page.Items, item)
		}
		if bytes >= objstore.MaxPageBytes {
			// The read stopped on the bound, so there may be more. Whether there
			// IS more is a question only the next page answers, which is the
			// contract LastEvaluatedKey states.
			page.More = last
			return page, nil
		}
	}
	if err := rows.Err(); err != nil {
		return objstore.Page{}, err
	}
	if q.Limit > 0 && page.Examined == q.Limit {
		// The examined-items limit is a page boundary too, and it is the one a
		// caller sets deliberately. A read that stopped there has to say so, or
		// paging by Limit silently loses the tail.
		page.More = last
	}
	return page, nil
}

// scanItem reads one row into an item, its stored size, and whether the filter
// kept it. A projected attribute the item does not carry comes back NULL and
// stays ABSENT from the map, so a projected read and a whole read report a
// missing attribute the same way.
func scanItem(rows *sql.Rows, q objstore.Query) (objstore.Item, int, bool, error) {
	item := objstore.Item{Attrs: map[string]string{}}
	var (
		doc   []byte
		cells = make([]sql.NullString, len(q.Project))
		size  int
		keep  = 1
		dest  = make([]any, 0, len(q.Project)+4)
	)
	dest = append(dest, &item.Key.Partition, &item.Key.Sort)
	if len(q.Project) == 0 {
		dest = append(dest, &doc)
	}
	for i := range cells {
		dest = append(dest, &cells[i])
	}
	dest = append(dest, &size)
	if len(q.Where) > 0 {
		dest = append(dest, &keep)
	}
	if err := rows.Scan(dest...); err != nil {
		return item, 0, false, err
	}
	if len(q.Project) == 0 {
		if err := json.Unmarshal(doc, &item.Attrs); err != nil {
			return item, 0, false, fmt.Errorf("decode the document at %q: %w", item.Key.Sort, err)
		}
		return item, size, keep == 1, nil
	}
	for i, name := range q.Project {
		if cells[i].Valid {
			item.Attrs[name] = cells[i].String
		}
	}
	return item, size, keep == 1, nil
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
// The five ordered operators need no such care: `<`, `<=`, `>`, `>=` and a
// closed range are the same comparison in both dialects on a TEXT column, which
// is why widening the predicate set cost this file five lines and no
// portability.
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
	case objstore.BetweenKey:
		cmp(">=", k.Value)
		cmp("<=", k.Upper)
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

// after appends the resume bound, which follows the read's DIRECTION: a
// descending page resumes BELOW its cursor, and a driver that always emitted `>`
// would replay the page it just returned, forever.
func (b *stmt) after(cursor string, descending bool) {
	if cursor == "" {
		return
	}
	if descending {
		b.write(" AND sort < ")
	} else {
		b.write(" AND sort > ")
	}
	b.bind(cursor)
}

// conjunction renders the attribute comparisons as ONE boolean expression and
// returns it, binding its values into this statement in the order they are
// written.
//
// It returns the text instead of appending it because the same conjunction has
// two sinks: a WHERE term on the write path, where the engine decides the row,
// and a SELECTED COLUMN on the read path, where the engine decides the row but
// this driver counts it against the page bound first. One renderer, two sinks —
// a fix to a comparison cannot land on the write path and miss the read.
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
