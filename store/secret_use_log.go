package store

import "time"

// SecretUseRow is one audit row written by the broker middleware per
// (tool call × resolved key). Spec 9/11.
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
	_, err := s.db.Exec(
		`INSERT INTO secret_use_log
		 (ts, spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts.Format(time.RFC3339Nano), r.SpawnID, r.CallerSub, r.Folder,
		r.Tool, r.Key, r.Scope, r.Status, r.LatencyMS)
	return err
}

// LookupSecret returns the value for (scope, scopeID, key) or "" + false
// when no row exists. Distinct from GetSecret which returns ErrSecretNotFound;
// the broker treats "missing" as a normal flow.
func (s *Store) LookupSecret(scope SecretScope, scopeID, key string) (string, bool) {
	var v string
	err := s.db.QueryRow(
		`SELECT value FROM secrets
		 WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		string(scope), scopeID, key,
	).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}
