package resreg

import (
	"reflect"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

func TestApplyValidatesAllResourcesBeforeWriting(t *testing.T) {
	for _, invalid := range [][]EntityRow{
		{{Key: ""}},
		{{Key: "duplicate"}, {Key: "duplicate"}},
	} {
		t.Run(invalid[0].Key, func(t *testing.T) {
			s, r := freshEntity(t)
			Register(Resource{
				Name: "laterrows", Table: "laterrows", DB: entitySubsystem,
				RowType:  reflect.TypeFor[EntityRow](),
				PKFields: []string{"Key"}, EntitySortKey: []string{"Key"},
			})
			if err := s.(EntityMaker).CreateEntity(t.Context(), "laterrows"); err != nil {
				t.Fatal(err)
			}
			putItem(t, s, "existing", map[string]string{"count": "1"})
			be := On(s)
			sum, err := Checksum(be, entitySubsystem)
			if err != nil {
				t.Fatal(err)
			}
			manifest := map[string]any{
				"entityrows": []EntityRow{{Key: "replacement"}}, "laterrows": invalid,
			}
			if _, err := Apply(t.Context(), be, entitySubsystem, sum, false, manifest, nil); err == nil {
				t.Fatal("invalid manifest accepted")
			}
			rows, err := r.ScanAll(be)
			if err != nil || !reflect.DeepEqual(rows, []EntityRow{{Key: "existing", Count: 1}}) {
				t.Fatalf("rows = %v, error = %v", rows, err)
			}
			if _, err := Apply(t.Context(), be, entitySubsystem, sum, false,
				map[string]any{"entityrows": []EntityRow{{Key: "existing", Count: 1}}}, nil); err != nil {
				t.Fatalf("invalid manifest retained claim: %v", err)
			}
			items, err := s.Query(t.Context(), objstore.Query{Entity: "laterrows", Partition: entitySubsystem})
			if err != nil || len(items) != 0 {
				t.Fatalf("later rows = %v, error = %v", items, err)
			}
		})
	}
}
