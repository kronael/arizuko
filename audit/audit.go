package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kronael/arizuko/chanlib"
)

// Config holds all audit configuration loaded from environment.
type Config struct {
	Enabled      bool
	DataDir      string
	Instance     string
	MaxBytes     int64
	RotateHours  int
	WebhookURL   string
	WebhookURLSystem   string
	WebhookURLMessages string
	WebhookURLWeb      string
	WebhookSecret string
}

func LoadConfig(dataDir, instance string) Config {
	return Config{
		Enabled:            chanlib.EnvOr("AUDIT_ENABLED", "false") == "true",
		DataDir:            dataDir,
		Instance:           instance,
		MaxBytes:           int64(chanlib.EnvInt("AUDIT_MAX_BYTES", 100*1024*1024)),
		RotateHours:        chanlib.EnvInt("AUDIT_ROTATE_HOURS", 24),
		WebhookURL:         chanlib.EnvOr("AUDIT_WEBHOOK_URL", ""),
		WebhookURLSystem:   chanlib.EnvOr("AUDIT_WEBHOOK_URL_SYSTEM", ""),
		WebhookURLMessages: chanlib.EnvOr("AUDIT_WEBHOOK_URL_MESSAGES", ""),
		WebhookURLWeb:      chanlib.EnvOr("AUDIT_WEBHOOK_URL_WEB", ""),
		WebhookSecret:      chanlib.EnvOr("AUDIT_WEBHOOK_SECRET", ""),
	}
}

// SystemEvent is one entry in audit-system.jl.
type SystemEvent struct {
	ID       string         `json:"id"`
	TS       string         `json:"ts"`
	Stream   string         `json:"stream"`
	Instance string         `json:"instance"`
	ActorSub string         `json:"actor_sub"`
	Tool     string         `json:"tool"`
	Folder   string         `json:"folder,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Outcome  Outcome        `json:"outcome"`
}

// MessageEvent is one entry in audit-messages.jl.
type MessageEvent struct {
	ID       string         `json:"id"`
	TS       string         `json:"ts"`
	Stream   string         `json:"stream"`
	Instance string         `json:"instance"`
	Folder   string         `json:"folder,omitempty"`
	ChatJID  string         `json:"chat_jid,omitempty"`
	Actor    string         `json:"actor,omitempty"`
	Action   string         `json:"action"`
	Params   map[string]any `json:"params,omitempty"`
	Outcome  Outcome        `json:"outcome"`
}

// WebEvent is one entry in audit-web.jl.
type WebEvent struct {
	TS        string `json:"ts"`
	Stream    string `json:"stream"`
	Instance  string `json:"instance"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	ActorSub  string `json:"actor_sub,omitempty"`
	IP        string `json:"ip,omitempty"`
}

// Outcome is the result of a system or message event.
type Outcome struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// writer appends JSON lines to one file with size/age rotation.
type writer struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	rotateAge  time.Duration
	openedAt   time.Time
	f          *os.File
	webhookURL string
	webhookSecret string
	batch      [][]byte
	lastFlush  time.Time
}

func newWriter(path string, maxBytes int64, rotateHours int, webhookURL, secret string) *writer {
	return &writer{
		path:          path,
		maxBytes:      maxBytes,
		rotateAge:     time.Duration(rotateHours) * time.Hour,
		webhookURL:    webhookURL,
		webhookSecret: secret,
		lastFlush:     time.Now(),
	}
}

func (w *writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	w.f = f
	w.openedAt = time.Now()
	return nil
}

func (w *writer) rotateIfNeeded() {
	if w.f == nil {
		return
	}
	info, err := w.f.Stat()
	if err != nil {
		return
	}
	aged := w.rotateAge > 0 && time.Since(w.openedAt) > w.rotateAge
	tooBig := w.maxBytes > 0 && info.Size() >= w.maxBytes
	if !aged && !tooBig {
		return
	}
	w.f.Close()
	w.f = nil

	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	rotated := w.path + "." + stamp
	if err := os.Rename(w.path, rotated); err != nil {
		slog.Warn("audit rotate rename", "path", w.path, "err", err)
		return
	}
	// Keep only 2 rotated files.
	glob, _ := filepath.Glob(w.path + ".*")
	if len(glob) > 2 {
		sort.Strings(glob)
		for _, old := range glob[:len(glob)-2] {
			os.Remove(old)
		}
	}
}

// write appends one JSON-encoded event line.
func (w *writer) write(v any) {
	w.mu.Lock()
	defer w.mu.Unlock()

	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	line = append(line, '\n')

	w.rotateIfNeeded()
	if w.f == nil {
		if err := w.open(); err != nil {
			slog.Warn("audit open", "path", w.path, "err", err)
			return
		}
	}
	if _, err := w.f.Write(line); err != nil {
		slog.Warn("audit write", "path", w.path, "err", err)
	}

	if w.webhookURL != "" {
		w.batch = append(w.batch, line)
	}
}

// flushWebhook sends buffered lines when ≥200 or >5s since last flush.
// Must be called with w.mu held.
func (w *writer) flushWebhookLocked(force bool) {
	if w.webhookURL == "" || len(w.batch) == 0 {
		return
	}
	if !force && len(w.batch) < 200 && time.Since(w.lastFlush) < 5*time.Second {
		return
	}
	payload := bytes.Join(w.batch, nil)
	w.batch = nil
	w.lastFlush = time.Now()
	go postWebhook(w.webhookURL, w.webhookSecret, payload)
}

// writeImmediate writes + flushes webhook right away (for system events).
func (w *writer) writeImmediate(v any) {
	w.mu.Lock()
	defer w.mu.Unlock()

	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	line = append(line, '\n')

	w.rotateIfNeeded()
	if w.f == nil {
		if err := w.open(); err != nil {
			slog.Warn("audit open", "path", w.path, "err", err)
			return
		}
	}
	if _, err := w.f.Write(line); err != nil {
		slog.Warn("audit write", "path", w.path, "err", err)
	}

	if w.webhookURL != "" {
		go postWebhook(w.webhookURL, w.webhookSecret, line)
	}
}

func postWebhook(url, secret string, payload []byte) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
		}
		req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-ndjson")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return
		}
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	slog.Warn("audit webhook failed", "url", url, "err", lastErr)
}

// Audit is the main entry point for all three streams.
type Audit struct {
	cfg      Config
	system   *writer
	messages *writer
	web      *writer
}

var noop = &Audit{}

func New(cfg Config) *Audit {
	if !cfg.Enabled {
		return noop
	}
	systemURL := cfg.WebhookURLSystem
	if systemURL == "" {
		systemURL = cfg.WebhookURL
	}
	messagesURL := cfg.WebhookURLMessages
	if messagesURL == "" {
		messagesURL = cfg.WebhookURL
	}
	webURL := cfg.WebhookURLWeb
	if webURL == "" {
		webURL = cfg.WebhookURL
	}
	return &Audit{
		cfg: cfg,
		system: newWriter(
			filepath.Join(cfg.DataDir, "audit-system.jl"),
			cfg.MaxBytes, cfg.RotateHours, systemURL, cfg.WebhookSecret,
		),
		messages: newWriter(
			filepath.Join(cfg.DataDir, "audit-messages.jl"),
			cfg.MaxBytes, cfg.RotateHours, messagesURL, cfg.WebhookSecret,
		),
		web: newWriter(
			filepath.Join(cfg.DataDir, "audit-web.jl"),
			cfg.MaxBytes, cfg.RotateHours, webURL, cfg.WebhookSecret,
		),
	}
}

func (a *Audit) enabled() bool { return a.system != nil }

// EmitSystem appends one event to audit-system.jl synchronously.
func (a *Audit) EmitSystem(e SystemEvent) {
	if !a.enabled() {
		return
	}
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	e.Stream = "system"
	e.Instance = a.cfg.Instance
	a.system.writeImmediate(e)
}

// EmitWeb appends one event to audit-web.jl synchronously.
func (a *Audit) EmitWeb(e WebEvent) {
	if !a.enabled() {
		return
	}
	e.Stream = "web"
	e.Instance = a.cfg.Instance
	a.web.write(e)
}

// emitMessage appends one event to audit-messages.jl (internal, batched).
func (a *Audit) emitMessage(e MessageEvent) {
	if !a.enabled() {
		return
	}
	e.Stream = "messages"
	e.Instance = a.cfg.Instance
	a.messages.write(e)
	a.messages.mu.Lock()
	a.messages.flushWebhookLocked(false)
	a.messages.mu.Unlock()
}

// cursor tracks the last exported integer ID per table.
type cursor struct {
	mu   sync.Mutex
	data map[string]int64
	path string
}

func loadCursor(path string) *cursor {
	c := &cursor{path: path, data: make(map[string]int64)}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	json.Unmarshal(raw, &c.data)
	return c
}

func (c *cursor) get(table string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data[table]
}

func (c *cursor) set(table string, id int64) {
	c.mu.Lock()
	c.data[table] = id
	c.mu.Unlock()
}

func (c *cursor) save() {
	c.mu.Lock()
	raw, err := json.Marshal(c.data)
	c.mu.Unlock()
	if err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return
	}
	os.Rename(tmp, c.path)
}

// StartPoll launches the message-stream poll goroutine.
// db must be the messages.db *sql.DB; ctx controls lifecycle.
func (a *Audit) StartPoll(ctx context.Context, db *sql.DB) {
	if !a.enabled() {
		return
	}
	cur := loadCursor(filepath.Join(a.cfg.DataDir, "audit-cursor.json"))
	go a.poll(ctx, db, cur)
}

func (a *Audit) poll(ctx context.Context, db *sql.DB, cur *cursor) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.pollMessages(db, cur)
			a.pollSessionLog(db, cur)
			a.pollTurnResults(db, cur)
			a.pollTaskRunLogs(db, cur)
			a.pollSecretUseLog(db, cur)
			a.pollCLIAudit(db, cur)
			cur.save()
			a.messages.mu.Lock()
			a.messages.flushWebhookLocked(true)
			a.messages.mu.Unlock()
		}
	}
}

func (a *Audit) pollMessages(db *sql.DB, cur *cursor) {
	const table = "messages"
	last := cur.get(table)
	rows, err := db.Query(
		`SELECT id, timestamp, chat_jid, COALESCE(sender,''), COALESCE(verb,''),
		        COALESCE(source,''), COALESCE(topic,''), is_from_me, errored,
		        COALESCE(routed_to,'')
		 FROM messages WHERE id > ? ORDER BY id LIMIT 500`, last)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ts, chatJID, sender, verb, source, topic, routedTo string
		var fromMe, errored int
		if err := rows.Scan(&id, &ts, &chatJID, &sender, &verb, &source,
			&topic, &fromMe, &errored, &routedTo); err != nil {
			continue
		}
		action := "message_in"
		if errored == 1 {
			action = "error"
		} else if fromMe == 1 {
			action = "message_out"
		}
		actor := sender
		if fromMe == 1 {
			actor = "bot"
		}
		e := MessageEvent{
			ID:      fmt.Sprintf("%s:%d", table, id),
			TS:      ts,
			Folder:  routedTo,
			ChatJID: chatJID,
			Actor:   actor,
			Action:  action,
			Params: map[string]any{
				"verb": verb, "source": source, "topic": topic,
				"msg_id": fmt.Sprintf("%d", id),
			},
			Outcome: Outcome{Status: "ok"},
		}
		a.emitMessage(e)
		cur.set(table, id)
	}
}

func (a *Audit) pollSessionLog(db *sql.DB, cur *cursor) {
	const table = "session_log"
	last := cur.get(table)
	rows, err := db.Query(
		`SELECT id, COALESCE(started_at,''), COALESCE(group_folder,''),
		        COALESCE(session_id,''), COALESCE(message_count,0)
		 FROM session_log WHERE id > ? ORDER BY id LIMIT 500`, last)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ts, folder, sessionID string
		var msgCount int
		if err := rows.Scan(&id, &ts, &folder, &sessionID, &msgCount); err != nil {
			continue
		}
		e := MessageEvent{
			ID:     fmt.Sprintf("%s:%d", table, id),
			TS:     ts,
			Folder: folder,
			Action: "turn_start",
			Params: map[string]any{
				"session_id": sessionID, "message_count": msgCount,
			},
			Outcome: Outcome{Status: "ok"},
		}
		a.emitMessage(e)
		cur.set(table, id)
	}
}

func (a *Audit) pollTurnResults(db *sql.DB, cur *cursor) {
	const table = "turn_results"
	last := cur.get(table)
	rows, err := db.Query(
		`SELECT id, COALESCE(recorded_at,''), COALESCE(folder,''),
		        COALESCE(turn_id,''), COALESCE(session_id,''), COALESCE(status,'')
		 FROM turn_results WHERE id > ? ORDER BY id LIMIT 500`, last)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ts, folder, turnID, sessionID, status string
		if err := rows.Scan(&id, &ts, &folder, &turnID, &sessionID, &status); err != nil {
			continue
		}
		e := MessageEvent{
			ID:     fmt.Sprintf("%s:%d", table, id),
			TS:     ts,
			Folder: folder,
			Action: "turn_end",
			Params: map[string]any{
				"turn_id": turnID, "session_id": sessionID, "status": status,
			},
			Outcome: Outcome{Status: "ok"},
		}
		a.emitMessage(e)
		cur.set(table, id)
	}
}

func (a *Audit) pollTaskRunLogs(db *sql.DB, cur *cursor) {
	const table = "task_run_logs"
	last := cur.get(table)
	rows, err := db.Query(
		`SELECT trl.id, COALESCE(trl.run_at,''), COALESCE(st.group_folder,''),
		        trl.task_id, COALESCE(trl.duration_ms,0), COALESCE(trl.status,''),
		        COALESCE(trl.error,'')
		 FROM task_run_logs trl
		 LEFT JOIN scheduled_tasks st ON st.id = trl.task_id
		 WHERE trl.id > ? ORDER BY trl.id LIMIT 500`, last)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ts, folder, taskID, status, errStr string
		var durMS int64
		if err := rows.Scan(&id, &ts, &folder, &taskID, &durMS, &status, &errStr); err != nil {
			continue
		}
		outcome := Outcome{Status: "ok"}
		if errStr != "" {
			outcome = Outcome{Status: "error", Detail: errStr}
		}
		e := MessageEvent{
			ID:     fmt.Sprintf("%s:%d", table, id),
			TS:     ts,
			Folder: folder,
			Action: "task_run",
			Params: map[string]any{
				"task_id": taskID, "duration_ms": durMS, "status": status,
			},
			Outcome: outcome,
		}
		a.emitMessage(e)
		cur.set(table, id)
	}
}

// pollSecretUseLog uses ts as cursor (ISO string sentinel).
func (a *Audit) pollSecretUseLog(db *sql.DB, cur *cursor) {
	const table = "secret_use_log"
	lastTS := ""
	if v := cur.get(table); v > 0 {
		lastTS = time.Unix(0, v).UTC().Format(time.RFC3339Nano)
	}
	var rows *sql.Rows
	var err error
	if lastTS == "" {
		rows, err = db.Query(
			`SELECT ts, COALESCE(folder,''), COALESCE(caller_sub,''),
			        COALESCE(tool,''), COALESCE(key,''), COALESCE(scope,''),
			        COALESCE(status,''), COALESCE(latency_ms,0)
			 FROM secret_use_log ORDER BY ts LIMIT 500`)
	} else {
		rows, err = db.Query(
			`SELECT ts, COALESCE(folder,''), COALESCE(caller_sub,''),
			        COALESCE(tool,''), COALESCE(key,''), COALESCE(scope,''),
			        COALESCE(status,''), COALESCE(latency_ms,0)
			 FROM secret_use_log WHERE ts > ? ORDER BY ts LIMIT 500`, lastTS)
	}
	if err != nil {
		return
	}
	defer rows.Close()
	var maxTS string
	for rows.Next() {
		var ts, folder, callerSub, tool, key, scope, status string
		var latencyMS int64
		if err := rows.Scan(&ts, &folder, &callerSub, &tool, &key, &scope,
			&status, &latencyMS); err != nil {
			continue
		}
		e := MessageEvent{
			ID:     fmt.Sprintf("%s:%s", table, ts),
			TS:     ts,
			Folder: folder,
			Actor:  callerSub,
			Action: "secret_use",
			Params: map[string]any{
				"tool": tool, "key": key, "scope": scope,
				"caller_sub": callerSub, "latency_ms": latencyMS,
			},
			Outcome: Outcome{Status: status},
		}
		a.emitMessage(e)
		if ts > maxTS {
			maxTS = ts
		}
	}
	if maxTS != "" {
		t, _ := time.Parse(time.RFC3339Nano, maxTS)
		cur.set(table, t.UnixNano())
	}
}

func (a *Audit) pollCLIAudit(db *sql.DB, cur *cursor) {
	const table = "cli_audit"
	last := cur.get(table)
	rows, err := db.Query(
		`SELECT id, ts, os_user, command, args
		 FROM cli_audit WHERE id > ? ORDER BY id LIMIT 500`, last)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ts, osUser, command, args string
		if err := rows.Scan(&id, &ts, &osUser, &command, &args); err != nil {
			continue
		}
		e := SystemEvent{
			ID:       fmt.Sprintf("cli:%d", id),
			TS:       ts,
			ActorSub: "cli:" + osUser,
			Tool:     command,
			Params:   map[string]any{"args": args},
			Outcome:  Outcome{Status: "ok"},
		}
		// cli_audit events go into the system stream via emitSystemFromPoll
		// (no webhook flush — already batched at tick boundary, but we use
		// the same writer for consistency).
		a.emitSystemFromPoll(e)
		cur.set(table, id)
	}
}

// emitSystemFromPoll writes a system event from the poll path (not immediate).
func (a *Audit) emitSystemFromPoll(e SystemEvent) {
	if !a.enabled() {
		return
	}
	e.Stream = "system"
	e.Instance = a.cfg.Instance
	a.system.write(e)
}
