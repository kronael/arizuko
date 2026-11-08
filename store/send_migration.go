package store

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// MigrateMemberSend removes the system-seeded wildcard send grant once.
func (s *Store) MigrateMemberSend(ctx context.Context) error {
	marker := objstore.Key{Partition: OwnerRoutd, Sort: "member-send-floor"}
	items, err := s.obj.Query(ctx, objstore.Query{
		Entity: Counters, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: marker.Sort},
	})
	if err != nil || len(items) != 0 {
		return err
	}
	key, err := aclKey(core.ACLRow{
		Principal: RoleMember, Action: "mcp:send", Scope: "**", Effect: EffectAllow,
	})
	if err != nil {
		return err
	}
	items, err = s.obj.Query(ctx, objstore.Query{
		Entity: ACL, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return err
	}
	writes := []objstore.Write{{
		Op: objstore.Put, Entity: Counters, Key: marker,
		Attrs: map[string]string{"done": "1"}, Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}}
	if len(items) != 0 && items[0].Attrs[attrACLGrantedBy] == "system:4r" && items[0].Attrs[attrACLGrantOption] == "0" {
		writes = append(writes, objstore.Write{
			Op: objstore.Delete, Entity: ACL, Key: key,
			Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
				{Attr: attrACLGrantedBy, Op: objstore.Eq, Value: "system:4r"},
				{Attr: attrACLGrantOption, Op: objstore.Eq, Value: "0"},
			}},
		})
	}
	ok, err := s.TryWriteEntities(ctx, nil, writes...)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("member send migration conflict")
	}
	return nil
}
