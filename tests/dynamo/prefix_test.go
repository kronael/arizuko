package dynamo_test

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

// A served store holds several owners, so an entity's table name carries the
// owner in front of it. This proves the separator survives the server.
func TestOwnerPrefixedTableName(t *testing.T) {
	cfg := dyntest.Config(t)
	cfg.Prefix = "routd."
	s, err := dynstore.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateEntity(ctx, "acl"); err != nil {
		t.Fatalf("create routd.acl: %v", err)
	}
	t.Cleanup(func() { _ = s.DropEntity(ctx, "acl") })
	if ok, err := s.GuardedBatch(ctx, nil, []objstore.Write{{
		Entity: "acl", Key: objstore.Key{Partition: "p", Sort: "s"},
		Attrs: map[string]string{"effect": "allow"},
	}}); err != nil || !ok {
		t.Fatalf("write into routd.acl: ok=%v err=%v", ok, err)
	}
	items, err := s.Query(ctx, objstore.Query{Entity: "acl", Partition: "p"})
	if err != nil || len(items) != 1 {
		t.Fatalf("read back: %d items, err=%v", len(items), err)
	}
	names, err := s.Entities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("entities under the prefix: %v", names)
}
