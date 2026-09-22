package store

// secret_use_log.go holds every read and write of `secret_use_log`: one item
// per (tool call × resolved key), written by the broker middleware and never
// touched again (spec 5/13 § Audit).
//
// The partition is a constant and the sort key the instant: audit_log's shape,
// for audit_log's reason. It is append-only history with a single writer and
// NO READER in the tree: an operator reads the `audit_log` twin (`secret.read`)
// instead, and a coordinate picked for an imagined caller is the mistake 6/37
// step 3 wrote up for authd. The collection grows without bound and has no
// drainage (zero rows fleet-wide, 2026-09-07). `folder` is an attribute, as on
// audit_log: containment has no operator on an attribute.

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

// secretUsePartition is the one partition every use lives in.
const secretUsePartition = "secret_use"

// SecretUseIDWidth is the width of one use's id, which is the sort key:
// core.TS's thirty bytes, a separator, and eight hex digits.
const SecretUseIDWidth = tsWidth + 1 + secretUseSuffixHex

// secretUseSuffixHex is how many hex digits separate two uses that share an
// instant. A turn resolves several keys in a loop (routd/mcp.go), so two
// items in one nanosecond is the ordinary case; a Put that refuses rather than
// overwrites makes a genuine collision loud instead of a lost row.
const secretUseSuffixHex = 8

// The ordered declaration is the whole reason a string id can be a time
// cursor: every driver refuses a key of another width (BUGS L97).
func init() { objstore.OrderedSort(SecretUseLog, SecretUseIDWidth) }

// The attributes an item carries. `ts` is NOT among them: the id IS the
// instant.
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

// SecretUseRow is one use as the broker middleware reports it (spec 5/13). It
// persists as one item AND one audit_log row (category=secret,
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

// newSecretUseID is one use's identity and its position in time: the instant
// at core.TS's fixed width, plus the suffix that makes two uses in one
// nanosecond two items.
func newSecretUseID(at time.Time) string {
	var b [secretUseSuffixHex / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand: " + err.Error())
	}
	return core.TS(at) + "-" + hex.EncodeToString(b[:])
}

// secretUseWrite renders one use as the write that records it: the single
// renderer for the live batch and the import.
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
// it, in ONE write (spec 6/40). A refusal is a FAULT rather than a race: both
// ids carry eight hex digits of randomness on top of the instant, and a secret
// read that silently recorded nothing is the one outcome spec 5/13 exists to
// prevent.
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
		// The one production writer is routd's MCP broker middleware, so the
		// surface is `mcp`; stamping `crackbox` made every broker read
		// unfindable by surface (BUGS L53).
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
