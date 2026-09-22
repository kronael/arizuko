package store

// system_messages.go holds every read and write of `system_messages`: one item
// per system event queued for a folder's next agent prompt, appended by the
// producer and drained by the prompt build.
//
// The partition is OwnerRoutd and the folder leads the sort key, with the queue
// position after it: every operation names a folder, and a group delete purges
// a subtree, so the folder cannot be the partition (sessions.go's reason). The
// queue position is an instant, not a sequence: core.TS plus a suffix that
// makes two events queued in one nanosecond two items, and a Put that refuses
// makes a collision loud. THE DRAIN DELETES WHAT IT READ, not what matches the
// folder: an event that arrived after the read is never deleted unrendered.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a queued event carries. The folder and the instant are the
// coordinate.
const (
	attrSysSource = "source"
	attrSysKind   = "kind"
	attrSysBody   = "body"
)

// sysSuffixHex is how many hex digits separate two events queued in the same
// nanosecond.
const sysSuffixHex = 8

// SysStampWidth is the width of the queue position. Exported because the move
// that carries these rows builds one from a legacy row's id.
const SysStampWidth = tsWidth + 1 + sysSuffixHex

// SystemMessage is one queued event, and the position it was queued at. The
// position is unexported: a caller that could name one could delete an event
// it never rendered, and Drain takes the values this read produced.
type SystemMessage struct {
	Source string
	Kind   string
	Body   string
	at     string
}

// SysStamp assembles a queue position from a rendered instant and a suffix, so
// the live writer and the move build one shape.
func SysStamp(ts, suffix string) string { return ts + "-" + suffix }

// sysNowStamp is the position an event queued now takes.
func sysNowStamp() (string, error) {
	raw := make([]byte, sysSuffixHex/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("queue position: %w", err)
	}
	return SysStamp(core.NowTS(), hex.EncodeToString(raw)), nil
}

// sysMsgKey is the coordinate of one queued event.
func sysMsgKey(folder, at string) (objstore.Key, error) {
	if folder == "" {
		return objstore.Key{}, fmt.Errorf("system message carries no folder, which is half " +
			"its coordinate")
	}
	if strings.Contains(folder, KeySep) {
		return objstore.Key{}, fmt.Errorf("system message folder %q carries the separator "+
			"that divides the two fields of its coordinate", folder)
	}
	if len(at) != SysStampWidth {
		return objstore.Key{}, fmt.Errorf("system message queue position %q is %d bytes and "+
			"a position is %d; the folder's queue is read in position order, so a short one "+
			"sorts into the wrong place instead of failing (BUGS L97)", at, len(at), SysStampWidth)
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: folder + KeySep + at}, nil
}

// SysMsgFolder reads the folder out of one item's coordinate. Exported because
// folder_scope.go decides a purge from it.
func SysMsgFolder(sortKey string) (string, bool) {
	folder, _, ok := strings.Cut(sortKey, KeySep)
	return folder, ok
}

// EnqueueSystemMessage appends one event to a folder's queue. emitSystemEvents
// is the producer; the prompt build is the consumer.
func (s *Store) EnqueueSystemMessage(folder, source, kind, body string) error {
	at, err := sysNowStamp()
	if err != nil {
		return err
	}
	key, err := sysMsgKey(folder, at)
	if err != nil {
		return err
	}
	attrs := map[string]string{}
	setAttr(attrs, attrSysSource, source)
	setAttr(attrs, attrSysKind, kind)
	setAttr(attrs, attrSysBody, body)
	queued, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: SystemMessages, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("queue a system event for %s: %w", folder, err)
	}
	if !queued {
		return fmt.Errorf("queue a system event for %s: position %s is taken, so two events "+
			"were minted at one instant with one suffix", folder, at)
	}
	return nil
}

// CountSystemMessages answers how many events are queued across every folder.
// `arizuko migrate-hot` reads it back to check that every row travelled.
func (s *Store) CountSystemMessages(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: SystemMessages, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the queued system events: %w", err)
	}
	return len(items), nil
}

// QueuedSystemMessages reads a folder's queue in the order it was written.
func (s *Store) QueuedSystemMessages(ctx context.Context, folder string) ([]SystemMessage, error) {
	if folder == "" {
		return nil, nil
	}
	if strings.Contains(folder, KeySep) {
		return nil, fmt.Errorf("system message folder %q carries the coordinate separator", folder)
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: SystemMessages, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: folder + KeySep},
	})
	if err != nil {
		return nil, fmt.Errorf("read the system events queued for %s: %w", folder, err)
	}
	out := make([]SystemMessage, 0, len(items))
	for _, it := range items {
		_, at, ok := strings.Cut(it.Key.Sort, KeySep)
		if !ok {
			return nil, fmt.Errorf("system message %q does not split into "+
				"(folder, position)", it.Key.Sort)
		}
		out = append(out, SystemMessage{
			Source: it.Attrs[attrSysSource],
			Kind:   it.Attrs[attrSysKind],
			Body:   it.Attrs[attrSysBody],
			at:     at,
		})
	}
	return out, nil
}

// DrainSystemMessages removes exactly the events a read produced, and reports
// whether it got them all. Delivery is at most once.
func (s *Store) DrainSystemMessages(ctx context.Context,
	folder string, msgs []SystemMessage) (bool, error) {
	writes := make([]objstore.Write, 0, len(msgs))
	for _, m := range msgs {
		key, err := sysMsgKey(folder, m.at)
		if err != nil {
			return false, err
		}
		// The delete IS the claim: an event another flush already took refuses,
		// and the whole set stays queued for a reader that can name it. No
		// event reaches two prompts and none is lost.
		writes = append(writes, objstore.Write{
			Op: objstore.Delete, Entity: SystemMessages, Key: key,
			Cond: objstore.Cond{Item: objstore.ItemPresent},
		})
	}
	if len(writes) == 0 {
		return false, nil
	}
	return s.TryWriteEntities(ctx, nil, writes...)
}
