package store

// system_messages.go holds every read and write of `system_messages`: one item
// per system event queued for a folder's next agent prompt, appended by the
// producer and drained by the prompt build.
//
// THE PARTITION IS OwnerRoutd AND THE FOLDER LEADS THE SORT KEY, with the queue
// position after it. Every operation names a folder — the append, the read and
// the drain, and there is no fourth — so rule 1's tenancy coordinate is what the
// reads ask for. It is not the PARTITION for the reason sessions.go gives: a
// group delete purges a folder SUBTREE, a query must name one partition, and
// enumerating partitions is not an operation the interface has. With the folder
// leading the sort key the same read is a begins_with, so it costs less than the
// statement it replaces — `WHERE folder = ?` answered `SCAN system_messages` on
// krons, because the table has no index but its rowid (EXPLAIN QUERY PLAN,
// 2026-09-09).
//
// THE QUEUE POSITION IS AN INSTANT AND NOT A SEQUENCE, which is rule 4. The
// statement this replaces read `ORDER BY id ASC` over an AUTOINCREMENT id, and
// no document store mints one. The position is core.TS at its fixed width plus
// the suffix that makes two events queued in the same nanosecond two items —
// audit_log's shape, and for the same reason: several processes write into this
// database and a Put that refuses rather than overwrites is what makes a
// collision loud instead of a lost event.
//
// THE DRAIN DELETES WHAT IT READ, not what matches the folder. An entity has no
// predicate delete, so the items are queried and removed one write each — and
// that is stricter than the statement it replaces, which deleted by folder and
// so could take an event that arrived after the read and was never rendered.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a queued event carries. The folder and the instant are the
// coordinate and are not among them.
const (
	attrSysSource = "source"
	attrSysKind   = "kind"
	attrSysBody   = "body"
)

// sysSuffixHex is how many hex digits separate two events queued in the same
// nanosecond. Four bytes of randomness, for audit_log's reason.
const sysSuffixHex = 8

// SysStampWidth is the width of the queue position: core.TS's thirty bytes, a
// separator, and the suffix. It is exported because the move that carries these
// rows builds one from a legacy row's id and has to render the same width.
const SysStampWidth = tsWidth + 1 + sysSuffixHex

// SystemMessage is one queued event, and the position it was queued at.
//
// The position is unexported: it is the item's coordinate, and a caller that
// could name one could delete an event it never rendered. Drain takes the values
// this read produced, which is what makes "delete what was rendered" exact.
type SystemMessage struct {
	Source string
	Kind   string
	Body   string
	at     string
}

// SysStamp assembles a queue position from a rendered instant and a suffix, so
// the live writer and the move that carries an existing row build one shape.
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

// SysMsgFolder reads the folder out of one item's coordinate. It is exported
// because folder_scope.go decides a purge from it.
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
	queued, err := s.WriteEntities(context.Background(), nil, objstore.Write{
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

// QueuedSystemMessagesTx reads a folder's queue in the order it was written, on
// a transaction the CALLER owns — the same transaction the drain runs in, so no
// event is rendered by one flush and deleted by another.
func (s *Store) QueuedSystemMessagesTx(ctx context.Context, tx *sql.Tx, folder string) ([]SystemMessage, error) {
	if folder == "" {
		return nil, nil
	}
	if strings.Contains(folder, KeySep) {
		return nil, fmt.Errorf("system message folder %q carries the coordinate separator", folder)
	}
	items, err := s.reads(tx).Query(ctx, objstore.Query{
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

// DrainSystemMessagesTx removes exactly the events a read produced, on the
// caller's transaction. Delivery is at most once, so the drain commits with the
// prompt that carried them.
func (s *Store) DrainSystemMessagesTx(ctx context.Context, tx *sql.Tx,
	folder string, msgs []SystemMessage) error {
	writes := make([]objstore.Write, 0, len(msgs))
	for _, m := range msgs {
		key, err := sysMsgKey(folder, m.at)
		if err != nil {
			return err
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Delete, Entity: SystemMessages, Key: key,
		})
	}
	if len(writes) == 0 {
		return nil
	}
	_, err := applyChunks(ctx, "drain the system events of "+folder, writes,
		func(ctx context.Context, chunk []objstore.Write) (bool, error) {
			return s.WriteEntitiesInTx(ctx, tx, nil, chunk...)
		})
	return err
}
