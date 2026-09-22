package objstore

// ordered.go is the one modelling rule this package imposes, made enforceable:
// an ordered comparison is a BYTE comparison, so a value that must order is
// written fixed-width. Left to discipline the rule breaks — messages.timestamp
// on krons carried two widths and byte order disagreed with time order at
// 10,488 pairs (BUGS L97). So the owning package DECLARES the width once, by
// registration like database/sql.Register, and every driver refuses another.

import (
	"fmt"
	"sync"
)

// widths holds every declared width, keyed by entity and attribute. The empty
// attribute name is the sort key.
var widths = struct {
	sync.RWMutex
	m map[[2]string]rule
}{m: map[[2]string]rule{}}

// rule is one declaration: how wide the ordered field is, and whether the value
// IS that field or only leads with it.
type rule struct {
	width int
	lead  bool
}

// OrderedSort declares that an entity's SORT KEY is compared by order, so every
// key written to it is exactly width bytes.
func OrderedSort(entity string, width int) { declare(entity, "", rule{width: width}) }

// OrderedAttr declares that an attribute is compared by order, so every value
// written to it is exactly width bytes.
func OrderedAttr(entity, attr string, width int) {
	if attr == "" {
		panic("objstore: OrderedAttr with an empty attribute name; the sort key is OrderedSort")
	}
	declare(entity, attr, rule{width: width})
}

// OrderedLead declares that a value LEADS with width bytes of ordered field and
// carries a tiebreak after it. `messages` needs it: its sort key is an instant
// plus a message id of 3 to 60 bytes, so the whole key has no one width and
// OrderedSort would refuse every write (BUGS L97).
//
// A value of EXACTLY width bytes is ACCEPTED, because a query bound is one: a
// read for "everything before this instant" compares against the bare instant.
// The entity's own key builder is what refuses a stored KEY of that width.
func OrderedLead(entity, attr string, width int) {
	declare(entity, attr, rule{width: width, lead: true})
}

// declare records one rule. A second declaration that DISAGREES panics at init
// rather than last-writer-wins: two packages disagreeing about a stored layout
// is exactly the bug this file exists to stop.
func declare(entity, attr string, r rule) {
	if r.width <= 0 {
		panic(fmt.Sprintf("objstore: ordered %s/%s declared with width %d", entity, attr, r.width))
	}
	widths.Lock()
	defer widths.Unlock()
	id := [2]string{entity, attr}
	if was, ok := widths.m[id]; ok && was != r {
		panic(fmt.Sprintf("objstore: ordered %s/%s is declared %s and again %s",
			entity, attr, was.says(), r.says()))
	}
	widths.m[id] = r
}

// orderedRule reports the rule declared for one field, and whether it has one.
func orderedRule(entity, attr string) (rule, bool) {
	widths.RLock()
	defer widths.RUnlock()
	r, ok := widths.m[[2]string{entity, attr}]
	return r, ok
}

// holds reports whether one value satisfies a rule.
func (r rule) holds(v string) bool {
	if r.lead {
		return len(v) >= r.width
	}
	return len(v) == r.width
}

// says renders what a rule asks, for an error a human reads.
func (r rule) says() string {
	if r.lead {
		return fmt.Sprintf("leads with %d bytes", r.width)
	}
	return fmt.Sprintf("is %d bytes", r.width)
}

// checkOrderedSort refuses a sort key of the wrong width.
func checkOrderedSort(entity string, keys ...string) error {
	r, ok := orderedRule(entity, "")
	if !ok {
		return nil
	}
	for _, k := range keys {
		if !r.holds(k) {
			return fmt.Errorf("%s sort key %q is %d bytes and the entity's ordered key %s; "+
				"a comparison on it is a byte comparison, so a short value sorts into the "+
				"wrong place instead of failing (BUGS L97)", entity, k, len(k), r.says())
		}
	}
	return nil
}

// checkOrderedAttr refuses an attribute value of the wrong width.
func checkOrderedAttr(entity, attr, value string) error {
	r, ok := orderedRule(entity, attr)
	if !ok {
		return nil
	}
	if !r.holds(value) {
		return fmt.Errorf("%s.%s is %d bytes and the declared ordered width %s; "+
			"a comparison on it is a byte comparison, so a short value sorts into the "+
			"wrong place instead of failing (BUGS L97)", entity, attr, len(value), r.says())
	}
	return nil
}
