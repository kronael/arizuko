package objstore

// ordered_test.go proves the lead rule by breaking it. Each body below names the
// edit that turns it red, because a width guard that accepts everything looks
// exactly like a width guard that works.

import (
	"strings"
	"testing"
)

// tsWidth is core.TS's width. It is written out rather than imported because
// objstore may not import core: core is where the layout lives, and this package
// is what every daemon's storage reaches through.
const tsWidth = 30

// instant is a rendered core.TS value, the lead every message key carries.
const instant = "2026-09-12T02:31:44.000000000Z"

// leadKey is a whole message key: the instant, the separator and an id.
func leadKey(id string) string { return instant + "\x1f" + id }

// TestALeadKeyOfAnyTailLengthIsAccepted is the body that fails if `holds` loses
// its lead branch — the mutation is `return len(v) == r.width` for every rule,
// which refuses every key this entity will ever write.
//
// The three ids are the extremes measured on the live instances: 3 bytes is the
// shortest and 60 the longest, and a whole-value rule refuses all of them.
func TestALeadKeyOfAnyTailLengthIsAccepted(t *testing.T) {
	const entity = "ordered_lead_accepts"
	OrderedLead(entity, "", tsWidth)
	for _, id := range []string{"abc", "3EB0C43A1F0E4B8E9D21", strings.Repeat("f", 60)} {
		key := leadKey(id)
		w := Write{Op: Put, Entity: entity, Key: Key{Partition: "p", Sort: key},
			Attrs: map[string]string{"a": "1"}}
		if err := ValidateBatch(nil, []Write{w}); err != nil {
			t.Fatalf("a %d-byte key with a %d-byte id was refused: %v", len(key), len(id), err)
		}
	}
}

// TestAKeyShorterThanTheLeadIsRefused is the body that fails if the lead rule is
// dropped altogether — the mutation is `return true`, or deleting the
// OrderedLead declaration from an entity's init.
//
// 19 bytes is the naive datetime two krons rows carry (`2026-04-02 12:38:47`).
// In front of a 40-byte id it makes a 59-byte value that any whole-value rule
// would wave through, and it sorts by the space at byte 10 rather than by time.
func TestAKeyShorterThanTheLeadIsRefused(t *testing.T) {
	const entity = "ordered_lead_refuses"
	OrderedLead(entity, "", tsWidth)
	short := "2026-04-02 12:38:47"
	w := Write{Op: Put, Entity: entity, Key: Key{Partition: "p", Sort: short},
		Attrs: map[string]string{"a": "1"}}
	err := ValidateBatch(nil, []Write{w})
	if err == nil {
		t.Fatal("a 19-byte key was accepted onto an entity whose ordered lead is 30 bytes")
	}
	if !strings.Contains(err.Error(), "leads with 30 bytes") {
		t.Errorf("the refusal does not say what the rule asks: %v", err)
	}
}

// TestABareInstantIsAValidQueryBound is why the lead rule is `>=` and not `>`.
//
// A read for "everything before this instant" compares the sort key against the
// bare instant, which is 30 bytes and is below every key at that instant,
// because every key carries the separator and an id after it. Tightening `holds`
// to `len(v) > r.width` turns this red and takes `MessagesBefore` with it.
func TestABareInstantIsAValidQueryBound(t *testing.T) {
	const entity = "ordered_lead_bound"
	OrderedLead(entity, "", tsWidth)
	q := Query{Entity: entity, Partition: "p", Key: KeyPredicate{Op: LtKey, Value: instant}}
	if err := ValidateQuery(q); err != nil {
		t.Fatalf("a bare instant was refused as a bound: %v", err)
	}
	if instant >= leadKey("a") {
		t.Fatal("the bare instant does not sort below a key at the same instant, so the " +
			"bound selects the wrong rows")
	}
}

// TestAWholeValueRuleStillRefusesAnotherWidth pins that adding the lead rule did
// not loosen the rule every other entity already rests on. The mutation is
// making `holds` answer `len(v) >= r.width` for every rule, which would let
// `audit_log` take a key of any length past its own.
func TestAWholeValueRuleStillRefusesAnotherWidth(t *testing.T) {
	const entity = "ordered_whole_value"
	OrderedSort(entity, tsWidth)
	for _, key := range []string{instant + "x", "2026-04-02 12:38:47"} {
		w := Write{Op: Put, Entity: entity, Key: Key{Partition: "p", Sort: key},
			Attrs: map[string]string{"a": "1"}}
		if err := ValidateBatch(nil, []Write{w}); err == nil {
			t.Errorf("a %d-byte key was accepted onto a 30-byte whole-value entity", len(key))
		}
	}
}

// TestTwoDeclarationsThatDisagreeArePanics keeps the declaration a fact rather
// than a last-writer-wins, including across the two KINDS: an entity whose key
// leads with thirty bytes is not the same promise as one whose key is thirty.
func TestTwoDeclarationsThatDisagreeArePanics(t *testing.T) {
	const entity = "ordered_disagreement"
	OrderedSort(entity, tsWidth)
	defer func() {
		if recover() == nil {
			t.Fatal("re-declaring a whole-value key as a lead key was accepted, so two " +
				"packages can hold different opinions of one stored layout")
		}
	}()
	OrderedLead(entity, "", tsWidth)
}
