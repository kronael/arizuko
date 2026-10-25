package main

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestMigrationReviewCompleteImages(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			inventory, drivers := renameFixture(t)
			renameSeed(t, &inventory, drivers, "routd", store.Groups,
				objstore.Key{Partition: "routd", Sort: "main"},
				map[string]string{"added_at": "unchanged"})
			plan, err := renamePreflight(context.Background(), "fixture", "",
				renameTestMap(t), inventory, drivers)
			if err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(t.TempDir(), "journal")
			if rollback {
				if err := renameExecute(context.Background(), journal, plan, drivers, false, false); err != nil {
					t.Fatal(err)
				}
			}
			changed := plan.Rows[0]
			image := changed.Before
			if rollback {
				image = changed.After
			}
			if image.Key == (objstore.Key{}) {
				t.Fatal("fixture did not select a recorded row")
			}
			image.Attrs = maps.Clone(image.Attrs)
			image.Attrs["operator_added"] = "must survive"
			renameSeed(t, &inventory, drivers, changed.Owner, changed.Entity, image.Key, image.Attrs)
			counter := &renameFaultStore{Store: drivers[changed.Owner]}
			drivers[changed.Owner] = counter
			err = renameExecute(context.Background(), journal, plan, drivers, false, rollback)
			if err == nil || !strings.Contains(err.Error(), changed.Owner+"."+changed.Entity) ||
				!strings.Contains(err.Error(), fmt.Sprintf("%q", image.Key.Sort)) {
				t.Errorf("changed row must refuse and name coordinate: %v", err)
			}
			if counter.Calls != 0 {
				t.Errorf("changed image permitted %d database writes", counter.Calls)
			}
			_, matches, err := renameRowState(context.Background(), drivers[changed.Owner], changed.Entity, image)
			if err != nil || !matches {
				t.Fatalf("changed row was overwritten or deleted: %v", err)
			}
		})
	}
}
