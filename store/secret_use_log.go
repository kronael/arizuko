package store

// secret_use_log.go holds every read and write of `secret_use_log`: one item
// per (tool call × resolved key), written by the broker middleware and never
// touched again (spec 5/13 § Audit).
//
// THE PARTITION IS A CONSTANT AND THE SORT KEY IS THE INSTANT, which is
// audit_log's shape and it is here for audit_log's reason. This entity is
// append-only history with a single writer, and the only thing that will ever
// read it back is a window of it in time order — an operator at `sqlite3`
// today, the forensic page 5/13 sketches later. Time IS the sort key, so
// ordering, paging and a limit are one range and the read is one query.
//
// NO READ DECIDED IT, because there is no reader: nothing in the tree calls
// this file for anything but the write, on any instance. That is worth stating
// rather than hiding, because it is the only honest argument available — a
// folder partition would be chosen for a folder read that does not exist, and a
// coordinate picked for an imagined caller is the mistake 6/37 step 3 wrote up
// for authd. When the read arrives it is a page of history, and the shape it
// wants is the one already here.
//
// WHAT THE CONSTANT COSTS is one item collection per database that grows
// without bound and has no drainage. Measured 2026-09-07 on all three live
// snapshots: zero rows on every one of them — the broker writes a row only when
// a container tool resolves a secret, and the fleet has one secret. `folder` is
// an attribute rather than a coordinate for the same reason it is on audit_log:
// a folder-bound read filters, and containment has no operator on an attribute.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// secretUsePartition is the one partition every use lives in. The file header
// says why it is a constant and what that costs.
const secretUsePartition = "secret_use"

// SecretUseIDWidth is the width of one use's id, which is the sort key:
// core.TS's thirty bytes, a separator, and eight hex digits.
const SecretUseIDWidth = tsWidth + 1 + secretUseSuffixHex

// secretUseSuffixHex is how many hex digits separate two uses that share an
// instant. A turn resolves several keys in a loop (routd/mcp.go:541), so two
// items landing in one nanosecond is the ordinary case rather than a collision
// to be surprised by, and a Put that refuses rather than overwrites is what
// makes a genuine one loud instead of a lost row.
const secretUseSuffixHex = 8

// The ordered declaration is the whole reason a string id can be a time cursor:
// every driver refuses a key of another width, so a short one cannot sort into
// the wrong place (BUGS L97).
func init() { objstore.OrderedSort(SecretUseLog, SecretUseIDWidth) }

// The attributes an item carries. `ts` is NOT among them: the id IS the
// instant, and storing the time twice is two facts that can disagree.
const (
	attrUseSpawnID   = "spawn_id"
	attrUseCallerSub = "caller_sub"
	attrUseFolder    = "folder"
	attrUseTool      = "tool"
	attrUseKey       = "key"
	attrUseScope     = "scope"
	attrUseStatus    = "status"
	attrUseLatency   = "latency_ms"
)

// SecretUseRow is one use as the broker middleware reports it. Spec 5/13.
// Persists as one item AND one audit_log row (category=secret,
// action=secret.read) per call.
type SecretUseRow struct {
	TS        time.Time
	SpawnID   string
	CallerSub string
	Folder    string
	Tool      string
	Key       string
	Scope     string // "user" | "folder" | "missing"
	Status    string // "ok" | "err" | "timeout"
	LatencyMS int64
}

// secretUseKey is the coordinate of one use.
func secretUseKey(id string) objstore.Key {
	return objstore.Key{Partition: secretUsePartition, Sort: id}
}

// newSecretUseID is one use's identity and its position in time: the instant it
// happened, at core.TS's fixed width so byte order is time order, plus the
// suffix that makes two uses in one nanosecond two items.
func newSecretUseID(at time.Time) string {
	var b [secretUseSuffixHex / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand: " + err.Error())
	}
	return core.TS(at) + "-" + hex.EncodeToString(b[:])
}

// secretUseWrite renders one use as the write that records it. It is the single
// renderer: whether the item rides in the live batch or in the import, it is
// built here and nowhere else.
func secretUseWrite(id string, r SecretUseRow) objstore.Write {
	attrs := map[string]string{
		attrUseTool:   r.Tool,
		attrUseKey:    r.Key,
		attrUseScope:  r.Scope,
		attrUseStatus: r.Status,
	}
	setAttr(attrs, attrUseSpawnID, r.SpawnID)
	setAttr(attrs, attrUseCallerSub, r.CallerSub)
	setAttr(attrs, attrUseFolder, r.Folder)
	if r.LatencyMS != 0 {
		attrs[attrUseLatency] = strconv.FormatInt(r.LatencyMS, 10)
	}
	return objstore.Write{
		Op: objstore.Put, Entity: SecretUseLog, Key: secretUseKey(id), Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}
}

// LogSecretUse records one resolved secret and the audit_log row that reports
// it, in ONE write.
//
// The two commit together or not at all, which is the guarantee one batch gives
// through a transaction and this gives through the batch itself (spec 6/40
// §"The audited write commits in a GuardedBatch"). Both items are entities in
// one database, so a transaction is no longer what carries the pair — the batch
// is, and it is the shape every engine has.
//
// A refusal is a FAULT here rather than a race: both ids carry eight hex digits
// of randomness on top of the instant, so the only way a condition fails is a
// genuine collision, and a secret read that silently recorded nothing is the
// one outcome spec 5/13 exists to prevent.
func (s *Store) LogSecretUse(r SecretUseRow) error {
	if r.TS.IsZero() {
		r.TS = time.Now().UTC()
	}
	out := audit.OutcomeOK
	errMsg := ""
	switch r.Status {
	case "err", "timeout":
		out = audit.OutcomeError
		errMsg = r.Status
	}
	ev := audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.read",
		Actor:    r.CallerSub,
		ActorSub: r.CallerSub,
		// The one production writer is routd's MCP broker middleware,
		// `ResolveConnectorSecrets` (`routd/mcp.go:530`), so the surface a secret
		// read happened on is `mcp`. Stamping `crackbox` named a sandbox that is
		// not on this path and made every broker read unfindable by surface
		// (BUGS L53).
		Surface:    audit.SurfaceMCP,
		Resource:   "secrets/" + r.Scope + "/" + r.Folder + "/" + r.Key,
		Scope:      r.Scope,
		Folder:     r.Folder,
		Outcome:    out,
		ErrorMsg:   errMsg,
		DurationMS: r.LatencyMS,
		ParamsSummary: map[string]any{
			"tool":     r.Tool,
			"spawn_id": r.SpawnID,
		},
	}
	id := newSecretUseID(r.TS)
	_, record := audit.WriteFor(ev)
	committed, err := s.TryWriteEntities(context.Background(), nil,
		secretUseWrite(id, r), record)
	if err != nil {
		return fmt.Errorf("record the use of %s: %w", r.Key, err)
	}
	if !committed {
		return fmt.Errorf("record the use of %s: %s is already recorded", r.Key, id)
	}
	return nil
}
