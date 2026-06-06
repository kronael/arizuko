// Package runed is the execution plane carved out of gated (spec 5/P): the
// work queue, the per-spawn container lifecycle, and the brokering of
// downscoped capability tokens. It is a pure container-spawner — routd hosts
// the agent MCP socket in-process and is the sole message appender; authd is
// the sole token signer. runed mounts the ipc dir routd's socket lives in into
// each spawn (container.Input.ExternalMCP) and manages spawn/steer/kill.
package runed

import (
	"database/sql"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "runed"

// ErrNotFound signals an absent row to the HTTP layer (404).
var ErrNotFound = errors.New("not found")

// DB owns runed.db: execution runtime state with no home in routd (spawns,
// session_log, spawn_logs, mcp_tokens). Times are RFC3339 TEXT.
type DB struct {
	db *sql.DB
}

// Open opens runed.db at dir/runed.db (WAL, FK on) and runs migrations.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "runed.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	return open(dsn)
}

// OpenMem opens a fresh isolated in-memory runed.db for tests. The DB name
// is unique per call so tests don't share state via the shared cache.
func OpenMem() (*DB, error) {
	return open("file:runed_mem_" + randHex(8) + "?mode=memory&cache=shared&_pragma=foreign_keys(on)")
}

func open(dsn string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := sqldb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqldb.Close()
		return nil, err
	}
	if err := db_utils.Migrate(sqldb, migrationFS, "migrations", serviceName); err != nil {
		sqldb.Close()
		return nil, err
	}
	return &DB{db: sqldb}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// SQL returns the raw handle for callers that need it (tests).
func (d *DB) SQL() *sql.DB { return d.db }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }

// --- session_log ---

// RecordSession opens a session_log row at spawn start, returning its id.
func (d *DB) RecordSession(folder, sessionID string) (int64, error) {
	res, err := d.db.Exec(`INSERT INTO session_log(group_folder, session_id, started_at)
		VALUES(?,?,?)`, folder, sessionID, nowTS())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EndSession closes a session_log row at exit. A non-empty newSessionID
// (harness-emitted) overwrites the started session_id via COALESCE.
func (d *DB) EndSession(id int64, newSessionID, result, errMsg string, msgs int) error {
	_, err := d.db.Exec(`UPDATE session_log
		SET ended_at=?, session_id=COALESCE(NULLIF(?,''), session_id),
		    result=?, error=?, message_count=?
		WHERE id=?`, nowTS(), newSessionID, result, errMsg, msgs, id)
	return err
}

// SessionRow is one session_log entry (dashd run history).
type SessionRow struct {
	ID           int64
	SessionID    string
	StartedAt    string
	EndedAt      string
	Result       string
	MessageCount int
}

// RecentSessions lists a folder's session_log rows, newest first.
func (d *DB) RecentSessions(folder string, limit int) ([]SessionRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.Query(`SELECT id, session_id, started_at,
		COALESCE(ended_at,''), COALESCE(result,''), COALESCE(message_count,0)
		FROM session_log WHERE group_folder=? ORDER BY id DESC LIMIT ?`, folder, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.StartedAt, &r.EndedAt, &r.Result, &r.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionRecord is a full session_log row (group_folder + error included),
// the shape routd federates over GET /v1/sessions/recent for its new_session
// hint + inspect_session tool. The dashd-facing SessionRow drops both fields.
type SessionRecord struct {
	ID           int64
	GroupFolder  string
	SessionID    string
	StartedAt    string
	EndedAt      string
	Result       string
	Error        string
	MessageCount int
}

// RecentSessionRecords lists a folder's full session_log rows, newest first.
// This is the federated read routd used to make by opening runed.db directly.
func (d *DB) RecentSessionRecords(folder string, limit int) ([]SessionRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.Query(`SELECT id, group_folder, session_id, started_at,
		COALESCE(ended_at,''), COALESCE(result,''), COALESCE(error,''), COALESCE(message_count,0)
		FROM session_log WHERE group_folder=? ORDER BY id DESC LIMIT ?`, folder, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		if err := rows.Scan(&r.ID, &r.GroupFolder, &r.SessionID, &r.StartedAt,
			&r.EndedAt, &r.Result, &r.Error, &r.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- spawns ---

// Spawn is one container spawn (the execution-session envelope).
type Spawn struct {
	RunID         string
	Folder        string
	Topic         string
	ContainerName string
	SessionLogID  int64
	MCPTokenJTI   string
	SessionID     string
	State         string
	Outcome       string
	ExitCode      int
	Steered       bool
	CreatedAt     string
	StartedAt     string
	EndedAt       string
}

// CreateSpawn inserts a spawns row in state=queued.
func (d *DB) CreateSpawn(s Spawn) error {
	_, err := d.db.Exec(`INSERT INTO spawns
		(run_id, folder, topic, container_name, session_log_id, mcp_token_jti,
		 session_id, state, created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		s.RunID, s.Folder, s.Topic, s.ContainerName, nz64(s.SessionLogID),
		nullStr(s.MCPTokenJTI), nullStr(s.SessionID), s.State, nowTS())
	return err
}

// SetSpawnToken records the brokered token's jti on a spawn once brokering
// succeeds (the spawns row is created BEFORE the broker call so a returned
// run_id is GET-able even on the broker-failure path).
func (d *DB) SetSpawnToken(runID, jti string) error {
	_, err := d.db.Exec("UPDATE spawns SET mcp_token_jti=? WHERE run_id=?", jti, runID)
	return err
}

// StartSpawn flips a spawn to state=running with its resolved session_id.
func (d *DB) StartSpawn(runID, sessionID string) error {
	_, err := d.db.Exec(`UPDATE spawns SET state='running', session_id=?, started_at=? WHERE run_id=?`,
		sessionID, nowTS(), runID)
	return err
}

// EndSpawn records the terminal state + outcome + exit code at teardown.
// A 'killed' state is sticky: a later teardown UPDATE (the spawn goroutine
// returning after a deliberate DELETE killed the container) won't clobber it
// back to 'error' (spec 5/P § DELETE: don't set outcome=error for a kill).
func (d *DB) EndSpawn(runID, state, outcome string, exitCode int) error {
	_, err := d.db.Exec(`UPDATE spawns SET state=?, outcome=?, exit_code=?, ended_at=?
		WHERE run_id=? AND state!='killed'`,
		state, outcome, exitCode, nowTS(), runID)
	return err
}

// KillSpawn records state='killed' ONLY for a still-active run — the TOCTOU
// between Kill's GetSpawn and this write can let a run complete normally
// (state='exited'/'error') in between; the WHERE clause guards that so a kill
// never overwrites an already-terminal state (spec 5/P § DELETE).
func (d *DB) KillSpawn(runID string) error {
	_, err := d.db.Exec(`UPDATE spawns SET state='killed', outcome='', exit_code=-1, ended_at=?
		WHERE run_id=? AND state IN ('queued','running')`,
		nowTS(), runID)
	return err
}

// MarkSteered records that a steer-into-running write happened on a spawn.
func (d *DB) MarkSteered(runID string) error {
	_, err := d.db.Exec("UPDATE spawns SET steered=1 WHERE run_id=?", runID)
	return err
}

// GetSpawn returns a spawn by run_id; ErrNotFound when absent.
func (d *DB) GetSpawn(runID string) (Spawn, error) {
	var s Spawn
	var logID sql.NullInt64
	var jti, sess, outcome, started, ended sql.NullString
	var exit sql.NullInt64
	var steered int
	err := d.db.QueryRow(`SELECT run_id, folder, topic, container_name, session_log_id,
		mcp_token_jti, session_id, state, outcome, exit_code, steered, created_at, started_at, ended_at
		FROM spawns WHERE run_id=?`, runID).Scan(
		&s.RunID, &s.Folder, &s.Topic, &s.ContainerName, &logID,
		&jti, &sess, &s.State, &outcome, &exit, &steered, &s.CreatedAt, &started, &ended)
	if err == sql.ErrNoRows {
		return Spawn{}, ErrNotFound
	}
	if err != nil {
		return Spawn{}, err
	}
	s.SessionLogID = logID.Int64
	s.MCPTokenJTI = jti.String
	s.SessionID = sess.String
	s.Outcome = outcome.String
	s.ExitCode = int(exit.Int64)
	s.Steered = steered == 1
	s.StartedAt = started.String
	s.EndedAt = ended.String
	return s, nil
}

// --- mcp_tokens ---

// RecordToken persists the REF of the downscoped token runed brokered for a
// spawn (never the raw JWS). UNIQUE(run_id) enforces one token per spawn.
func (d *DB) RecordToken(jti, runID, parentJTI, folder, scopeJSON, expiresAt string) error {
	_, err := d.db.Exec(`INSERT INTO mcp_tokens(jti, run_id, parent_jti, folder, scope, issued_at, expires_at)
		VALUES(?,?,?,?,?,?,?)`, jti, runID, parentJTI, folder, scopeJSON, nowTS(), expiresAt)
	return err
}

// SweepExpired drops spawns older than retention (cascading spawn_logs +
// mcp_tokens) and any mcp_tokens past expires_at (hourly GC).
func (d *DB) SweepExpired(retention time.Duration) error {
	cutoff := time.Now().Add(-retention).UTC().Format(time.RFC3339)
	if _, err := d.db.Exec("DELETE FROM spawns WHERE created_at < ?", cutoff); err != nil {
		return err
	}
	_, err := d.db.Exec("DELETE FROM mcp_tokens WHERE expires_at < ?", nowTS())
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nz64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
