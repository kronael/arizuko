package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/googd/googstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func pollState(it objstore.Item) (*filePoll, error) {
	a := it.Attrs
	s := &filePoll{cursor: a["cursor"], status: a["status"], name: a["name"], lastPoll: a["last_poll"]}
	var err error
	if a["next_poll_at"] != "" {
		s.next, err = time.Parse(time.RFC3339Nano, a["next_poll_at"])
	}
	if err != nil {
		return nil, err
	}
	if a["backoff"] != "" {
		s.delay, err = time.ParseDuration(a["backoff"])
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (p *poller) boundFiles(ctx context.Context) (*[]store.GDriveFile, error) {
	return p.snapshot.Read(ctx, p.bindings.Obj(), store.OwnerScope(store.OwnerRoutd), func(context.Context) (*[]store.GDriveFile, error) {
		files, err := p.bindings.ListGDriveFiles("")
		return &files, err
	})
}

func (p *poller) reconcileBindings(ctx context.Context) error {
	files, err := p.boundFiles(ctx)
	if err != nil {
		return err
	}
	if p.reconciled != nil && slices.Equal(*p.reconciled, *files) {
		p.reconciled = files
		return nil
	}
	rows, err := p.db.Query(ctx, objstore.Query{Entity: googstore.PollEntity, Partition: p.name})
	if err != nil {
		return err
	}
	existing := map[string]objstore.Item{}
	for _, row := range rows {
		existing[row.Key.Sort] = row
	}
	var writes []objstore.Write
	for _, file := range *files {
		old := existing[file.FileID]
		delete(existing, file.FileID)
		created := core.TS(file.CreatedAt)
		if old.Attrs["active"] == "1" && old.Attrs["binding_created_at"] == created {
			continue
		}
		writes = append(writes, objstore.Write{Op: objstore.Merge, Entity: googstore.PollEntity,
			Key:   objstore.Key{Partition: p.name, Sort: file.FileID},
			Attrs: map[string]string{"active": "1", "binding_created_at": created, "next_poll_at": core.TS(time.Time{}), "backoff": "0s"}})
	}
	for _, row := range rows {
		if _, removed := existing[row.Key.Sort]; !removed || row.Attrs["active"] != "1" {
			continue
		}
		writes = append(writes, objstore.Write{Op: objstore.Update, Entity: googstore.PollEntity, Key: row.Key,
			Attrs: map[string]string{"active": "0"}, Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: objstore.IndexRevision, Op: objstore.Eq, Value: row.Attrs[objstore.IndexRevision]}}}})
	}
	for len(writes) > 0 {
		n := min(len(writes), objstore.IndexBatchSize)
		ok, err := p.db.GuardedBatch(ctx, nil, writes[:n])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("Drive bindings changed during reconciliation")
		}
		writes = writes[n:]
	}
	p.reconciled = files
	return nil
}
