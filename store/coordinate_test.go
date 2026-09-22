package store

import (
	"strings"
	"testing"
)

// keyFields is read by a switch that must carry a case for every entity it
// names. A table entry with no case reaches the fallthrough, and a seed for
// that entity fails at the coordinate rather than at the attribute it meant to
// place.
func TestEveryDeclaredEntityHasAKeyBuilder(t *testing.T) {
	for entity, fields := range keyFields {
		_, err := entityKey(entity, make([]string, len(fields)))
		if err != nil && strings.Contains(err.Error(), "has no coordinate builder") {
			t.Errorf("%s declares the coordinate (%s) and no case builds it",
				entity, strings.Join(fields, ","))
		}
	}
}

// The arity in the table is the arity the caller must pass, and it is checked
// BEFORE any field is read. A coordinate of the wrong length would otherwise
// index past its fields, or drop one and write where a shorter key points.
func TestACoordinateOfTheWrongLengthIsRefused(t *testing.T) {
	for entity, fields := range keyFields {
		if _, err := entityKey(entity, make([]string, len(fields)+1)); err == nil {
			t.Errorf("%s accepted %d coordinate fields and declares %d",
				entity, len(fields)+1, len(fields))
		}
		if len(fields) > 1 {
			if _, err := entityKey(entity, make([]string, len(fields)-1)); err == nil {
				t.Errorf("%s accepted %d coordinate fields and declares %d",
					entity, len(fields)-1, len(fields))
			}
		}
	}
}

// An entity the table does not name has no coordinate at all; the seed must say
// so rather than write to a key built from nothing.
func TestAnUnknownEntityIsRefused(t *testing.T) {
	if _, err := entityKey("not_an_entity", []string{"x"}); err == nil {
		t.Error("an entity no table declares was given a coordinate")
	}
}
