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

// Query reads the items q selects.
func (s *Store) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	b := &stmt{d: s.dialect}
	if err := checkIdent("entity", q.Entity); err != nil {
		return nil, err
	}
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

	rows, err := s.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", q.Entity, err)
	}
	defer rows.Close()
	out := []objstore.Item{}
	for rows.Next() {
		item, err := scanItem(rows, q.Project)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.Entity, err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query %s: %w", q.Entity, err)
	}
	return out, nil
}

// scanItem reads one row into an item. A projected attribute the item does not
// carry comes back NULL and stays ABSENT from the map, so a projected read and
// a whole read report a missing attribute the same way.
func scanItem(rows *sql.Rows, project []string) (objstore.Item, error) {
	item := objstore.Item{Attrs: map[string]string{}}
	if len(project) == 0 {
		var doc []byte
		if err := rows.Scan(&item.Key.Partition, &item.Key.Sort, &doc); err != nil {
			return item, err
		}
		if err := json.Unmarshal(doc, &item.Attrs); err != nil {
			return item, fmt.Errorf("decode the document at %q: %w", item.Key.Sort, err)
		}
		return item, nil
	}
	cells := make([]sql.NullString, len(project))
	dest := make([]any, 0, len(project)+2)
	dest = append(dest, &item.Key.Partition, &item.Key.Sort)
	for i := range cells {
		dest = append(dest, &cells[i])
	}
	if err := rows.Scan(dest...); err != nil {
		return item, err
	}
	for i, name := range project {
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
func (b *stmt) keyPredicate(k objstore.KeyPredicate) error {
	switch k.Op {
	case objstore.AnyKey:
		return nil
	case objstore.ExactKey:
		b.write(" AND sort = ")
		b.bind(k.Value)
		return nil
	case objstore.PrefixKey:
		prefix := k.Value + k.Sep
		b.write(" AND (sort = ")
		b.bind(k.Value)
		b.write(" OR substr(sort, 1, ")
		b.bind(utf8.RuneCountInString(prefix))
		b.write(") = ")
		b.bind(prefix)
		b.write(")")
		return nil
	default:
		return fmt.Errorf("unknown key predicate %d", k.Op)
	}
}

// where appends the attribute comparisons, each as a further " AND ".
func (b *stmt) where(cmps []objstore.Cmp) error {
	for _, c := range cmps {
		if err := checkIdent("attribute", c.Attr); err != nil {
			return err
		}
		b.write(" AND " + b.d.attr(c.Attr))
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
			return fmt.Errorf("unknown comparison %d on attribute %q", c.Op, c.Attr)
		}
		b.bind(c.Value)
	}
	return nil
}
