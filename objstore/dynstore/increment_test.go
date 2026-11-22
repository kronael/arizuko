package dynstore_test

import (
	"strconv"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestIncrementIntegerBounds(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	for _, tc := range []struct {
		name       string
		value      int
		delta      int
		want       int
		isOverflow bool
	}{
		{"overflow", maxInt, 1, maxInt, true},
		{"underflow", minInt, -1, minInt, true},
		{"large_overflow", maxInt, maxInt, maxInt, true},
		{"large_underflow", minInt, minInt, minInt, true},
		{"maximum", maxInt - 1, 1, maxInt, false},
		{"minimum", minInt + 1, -1, minInt, false},
		{"opposite_signs", maxInt, minInt, -1, false},
		{"zero", minInt, 0, minInt, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := dyntest.Store(t, "counts")
			key := objstore.Key{Partition: "p", Sort: "counter"}
			if ok, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
				Op: objstore.Put, Entity: "counts", Key: key,
				Attrs: map[string]string{"n": strconv.Itoa(tc.value)},
			}}); err != nil || !ok {
				t.Fatalf("seed: %v %v", ok, err)
			}
			value, committed, err := objstore.Increment(t.Context(), s, "counts", key, "n", tc.delta, nil, nil)
			if tc.isOverflow {
				if err == nil || committed {
					t.Fatalf("overflow: value=%d committed=%v error=%v", value, committed, err)
				}
			} else if err != nil || !committed || value != tc.want {
				t.Fatalf("increment: value=%d committed=%v error=%v", value, committed, err)
			}
			rows, err := s.Query(t.Context(), objstore.Query{Entity: "counts", Partition: "p"})
			if err != nil || len(rows) != 1 || rows[0].Attrs["n"] != strconv.Itoa(tc.want) {
				t.Fatalf("persisted counter: %v %v", rows, err)
			}
		})
	}
}
