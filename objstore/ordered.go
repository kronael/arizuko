package objstore

// ordered.go is the one modelling rule this package imposes, made enforceable.
//
// Every attribute value is a string and an ordered comparison is a BYTE
// comparison, so a value that must order has to be written fixed-width by its
// writer. Left to discipline, that rule is already broken in live data:
// messages.timestamp on krons carries seven different widths across 24,013
// rows, and 32 pairs share a second across two of them. Byte order then
// disagrees with time order at exactly those pairs, which is BUGS L97 — the
// re-issue trigger that no held call ever fired.
//
// So a writer DECLARES the width once and every driver refuses a value of
// another width, the way checkIdent refuses a name it would write into a
// statement. Declaration is registration rather than a constructor argument for
// the same reason database/sql.Register is: the fact belongs to the package
// that OWNS the entity, it is the same fact for every driver, and a driver
// picked up later must not be able to be handed a different one.

import (
	"fmt"
	"sync"
)

// widths holds every declared width, keyed by entity and attribute. The empty
// attribute name is the sort key, which is the field the ordered predicates
// compare and therefore the one that most needs the rule.
var widths = struct {
	sync.RWMutex
	m map[[2]string]int
}{m: map[[2]string]int{}}

// OrderedSort declares that an entity's SORT KEY is compared by order, so every
// key written to it is exactly width bytes.
//
// This is the declaration routd's messages entity needs: its sort key is the
// timestamp the poll cursor compares, and the seven widths live data carries are
// what made the cursor skip rows. core.TS is the layout that satisfies it, at 30
// bytes.
func OrderedSort(entity string, width int) { declare(entity, "", width) }

// OrderedAttr declares that an attribute is compared by order, so every value
// written to it is exactly width bytes.
func OrderedAttr(entity, attr string, width int) {
	if attr == "" {
		panic("objstore: OrderedAttr with an empty attribute name; the sort key is OrderedSort")
	}
	declare(entity, attr, width)
}

// declare records one width. A second declaration with a DIFFERENT width is a
// panic rather than a last-writer-wins, because two packages disagreeing about
// a stored layout is exactly the bug this file exists to stop, and it must fail
// at init rather than at the first row that straddles the two.
func declare(entity, attr string, width int) {
	if width <= 0 {
		panic(fmt.Sprintf("objstore: ordered %s/%s declared with width %d", entity, attr, width))
	}
	widths.Lock()
	defer widths.Unlock()
	id := [2]string{entity, attr}
	if was, ok := widths.m[id]; ok && was != width {
		panic(fmt.Sprintf("objstore: ordered %s/%s is declared %d bytes wide and again %d",
			entity, attr, was, width))
	}
	widths.m[id] = width
}

// orderedWidth reports the declared width of one field, and whether it has one.
func orderedWidth(entity, attr string) (int, bool) {
	widths.RLock()
	defer widths.RUnlock()
	w, ok := widths.m[[2]string{entity, attr}]
	return w, ok
}

// checkOrderedSort refuses a sort key of the wrong width.
func checkOrderedSort(entity string, keys ...string) error {
	width, ok := orderedWidth(entity, "")
	if !ok {
		return nil
	}
	for _, k := range keys {
		if len(k) != width {
			return fmt.Errorf("%s sort key %q is %d bytes and the entity's ordered key is %d; "+
				"a comparison on it is a byte comparison, so a short value sorts into the "+
				"wrong place instead of failing (BUGS L97)", entity, k, len(k), width)
		}
	}
	return nil
}

// checkOrderedAttr refuses an attribute value of the wrong width.
func checkOrderedAttr(entity, attr, value string) error {
	width, ok := orderedWidth(entity, attr)
	if !ok {
		return nil
	}
	if len(value) != width {
		return fmt.Errorf("%s.%s is %d bytes and the declared ordered width is %d; "+
			"a comparison on it is a byte comparison, so a short value sorts into the "+
			"wrong place instead of failing (BUGS L97)", entity, attr, len(value), width)
	}
	return nil
}
