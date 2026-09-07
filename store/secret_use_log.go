package store

import (
	"context"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
)

// SecretUseRow is one audit row written by the broker middleware per
// (tool call × resolved key). Spec 5/13. Persists to the legacy
// secret_use_log table AND emits one audit_log row (category=access /
// secret, action=secret.read) per call.
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

func (s *Store) LogSecretUse(r SecretUseRow) error {
	ts := r.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO secret_use_log
		 (ts, spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		core.TS(ts), r.SpawnID, r.CallerSub, r.Folder,
		r.Tool, r.Key, r.Scope, r.Status, r.LatencyMS); err != nil {
		return err
	}
	out := audit.OutcomeOK
	errMsg := ""
	switch r.Status {
	case "err", "timeout":
		out = audit.OutcomeError
		errMsg = r.Status
	}
	if err := audit.EmitInTx(ctx, s.Obj(), tx, audit.Event{
		Category:   audit.CategorySecret,
		Action:     "secret.read",
		Actor:      r.CallerSub,
		ActorSub:   r.CallerSub,
		Surface:    audit.SurfaceCrackbox,
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
	}); err != nil {
		return err
	}
	return tx.Commit()
}
