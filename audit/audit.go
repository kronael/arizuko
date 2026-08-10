package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
	"uuid"
)

// envOr / envInt: audit's own env readers. audit must not depend on the
// channel-adapter library (chanlib) for trivial env parsing — that edge created
// an auth→audit→chanlib import cycle once chanlib started exchanging service
// tokens via the auth package (spec 5/1).
func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func envInt(k string, fallback int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// Config holds all audit configuration loaded from environment.
type Config struct {
	Enabled            bool
	DataDir            string
	Instance           string
	MaxBytes           int64
	RotateHours        int
	WebhookURL         string
	WebhookURLSystem   string
	WebhookURLMessages string
	WebhookURLWeb      string
	WebhookSecret      string
}

func LoadConfig(dataDir, instance string) Config {
	return Config{
		Enabled:            envOr("AUDIT_ENABLED", "false") == "true",
		DataDir:            dataDir,
		Instance:           instance,
		MaxBytes:           int64(envInt("AUDIT_MAX_BYTES", 100*1024*1024)),
		RotateHours:        envInt("AUDIT_ROTATE_HOURS", 24),
		WebhookURL:         envOr("AUDIT_WEBHOOK_URL", ""),
		WebhookURLSystem:   envOr("AUDIT_WEBHOOK_URL_SYSTEM", ""),
		WebhookURLMessages: envOr("AUDIT_WEBHOOK_URL_MESSAGES", ""),
		WebhookURLWeb:      envOr("AUDIT_WEBHOOK_URL_WEB", ""),
		WebhookSecret:      envOr("AUDIT_WEBHOOK_SECRET", ""),
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
	mu            sync.Mutex
	path          string
	maxBytes      int64
	rotateAge     time.Duration
	openedAt      time.Time
	f             *os.File
	webhookURL    string
	webhookSecret string
	batch         [][]byte
	lastFlush     time.Time
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

	line := w.writeLine(v)
	if line == nil {
		return
	}
	if w.webhookURL != "" {
		w.batch = append(w.batch, line)
	}
}

func (w *writer) writeLine(v any) []byte {
	line, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	line = append(line, '\n')

	w.rotateIfNeeded()
	if w.f == nil {
		if err := w.open(); err != nil {
			slog.Warn("audit open", "path", w.path, "err", err)
			return nil
		}
	}
	if _, err := w.f.Write(line); err != nil {
		slog.Warn("audit write", "path", w.path, "err", err)
	}
	return line
}

// flushWebhookLocked sends buffered lines when ≥200 or >5s since last flush.
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

	line := w.writeLine(v)
	if line == nil {
		return
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
	a.web.mu.Lock()
	a.web.flushWebhookLocked(false)
	a.web.mu.Unlock()
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
