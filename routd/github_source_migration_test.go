package routd

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestOpenMigratesGitHubSourceRoutes(t *testing.T) {
	ctx := context.Background()
	h, err := store.Handle(store.OwnerRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOwner(ctx, h, store.OwnerRoutd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		entities, err := h.Entities(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		for _, entity := range entities {
			if err := h.DropEntity(ctx, entity); err != nil {
				t.Error(err)
			}
		}
	})
	s := store.NewOver(h)
	s.SetSecretKeys([]byte("migration-test"))
	if _, err := s.AddGitHubSource(store.GitHubSource{Source: "app/sloth", OwnerFolder: "sloth/discord"}, "sloth/discord"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: store.GitHubSources,
		Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "app/sloth"}, Remove: []string{"target_folder", "route_ids"}}); err != nil {
		t.Fatal(err)
	}
	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if hit, err := db.store().RemoveGitHubSource("app/sloth", "sloth/discord"); err != nil || !hit {
		t.Fatalf("remove after startup migration = %v, %v", hit, err)
	}
}
