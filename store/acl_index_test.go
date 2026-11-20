package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestACLWildcardIndexReadsOnlyWildcardRows(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for i := range 40 {
		if err := s.AddACLRow(core.ACLRow{Principal: fmt.Sprintf("google:user%d", i), Action: "mcp:*", Scope: "demo/**"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	wild := core.ACLRow{Principal: "google:*", Action: "interact", Scope: "demo", Effect: EffectDeny}
	if err := s.AddACLRow(wild, ""); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	reader := NewOver(c)
	rows, err := reader.ACLWildcardRows()
	if err != nil || len(rows) != 1 || rows[0].Principal != wild.Principal || rows[0].Effect != EffectDeny || c.rows != 3 {
		t.Fatalf("wild rows=%v reads=%d err=%v", rows, c.rows, err)
	}
	if err := s.RemoveACLRow(wild, ""); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	rows, err = reader.ACLWildcardRows()
	if err != nil || len(rows) != 0 || c.rows != 1 {
		t.Fatalf("revoked rows=%v reads=%d err=%v", rows, c.rows, err)
	}
}

type aclMigrationFault struct {
	objstore.Store
	fail bool
}

func (r *aclMigrationFault) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if r.fail && w.Entity == objstore.IndexEntity && w.Key.Partition == "migrations" && w.Key.Sort == "acl-wildcard-v1" {
			r.fail = false
			return false, errors.New("marker unavailable")
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}
func TestACLWildcardMigrationValidatesAndResumes(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := t.Context()
	if err := s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.Key{Partition: "migrations", Sort: aclWildcardMigration}}); err != nil {
		t.Fatal(err)
	}
	bad := objstore.Key{Partition: OwnerRoutd, Sort: "google:*"}
	if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Put, Entity: ACL, Key: bad, Attrs: map[string]string{}}}); err != nil || !ok {
		t.Fatalf("seed %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(ctx); err == nil {
		t.Fatal("invalid legacy coordinate accepted")
	}
	marker := objstore.Key{Partition: "migrations", Sort: "acl-wildcard-v1"}
	if _, done, err := s.itemAt(ctx, objstore.IndexEntity, marker); err != nil || done {
		t.Fatalf("invalid migration marked done=%v err=%v", done, err)
	}
	row := aclDefaults(core.ACLRow{Principal: "google:*", Action: "interact", Scope: "demo"})
	key, _ := aclKey(row)
	if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Delete, Entity: ACL, Key: bad}, {Op: objstore.Put, Entity: ACL, Key: key, Attrs: aclAttrs(row)}}); err != nil || !ok {
		t.Fatalf("repair %v %v", ok, err)
	}
	fault := &aclMigrationFault{Store: s.Obj(), fail: true}
	if err := NewOver(fault).MigrateStateIndexes(ctx); err == nil {
		t.Fatal("marker failure hidden")
	}
	if err := NewOver(fault).MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ACLWildcardRows()
	if err != nil || len(rows) != 1 || rows[0].Principal != row.Principal {
		t.Fatalf("migrated rows=%v err=%v", rows, err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	if err := NewOver(c).MigrateStateIndexes(ctx); err != nil || c.rows != 12 {
		t.Fatalf("repeat reads=%d err=%v", c.rows, err)
	}
}

var errACLRead = errors.New("unavailable")

type aclReadFault struct {
	objstore.Store
	entity string
}

func (r aclReadFault) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == r.entity {
		return nil, errACLRead
	}
	return r.Store.Query(ctx, q)
}
func TestACLWildcardIndexFailsClosed(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	row := core.ACLRow{Principal: "google:*", Action: "interact", Scope: "demo", Effect: EffectDeny}
	if err := s.AddACLRow(row, ""); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []string{objstore.IndexEntity, ACL} {
		if rows, err := NewOver(aclReadFault{Store: s.Obj(), entity: entity}).ACLWildcardRows(); !errors.Is(err, errACLRead) || len(rows) != 0 {
			t.Fatalf("%s rows=%v err=%v", entity, rows, err)
		}
	}
	key, _ := aclKey(row)
	if ok, err := s.TryWriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: ACL, Key: key, Attrs: map[string]string{}, Cond: objstore.Cond{Item: objstore.ItemAbsent}}); err != nil || ok {
		t.Fatalf("refused write %v %v", ok, err)
	}
	rows, err := s.ACLWildcardRows()
	if err != nil || len(rows) != 1 || rows[0].Effect != EffectDeny {
		t.Fatalf("refusal lost deny %v %v", rows, err)
	}
}
