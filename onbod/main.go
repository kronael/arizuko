package main

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/notify"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "onbod"

var nameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

type config struct {
	dsn          string
	secret       string
	listenAddr   string
	gatedURL     string
	pollInterval time.Duration
	prototype    string
	groupsDir    string
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if os.Getenv("ONBOARDING_ENABLED") == "0" {
		slog.Info("onboarding disabled")
		os.Exit(0)
	}

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", cfg.dsn+"?_busy_timeout=5000")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		slog.Warn("set WAL mode", "err", err)
	}

	if err := migrate(db); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	if err := seedRoutes(db); err != nil {
		slog.Error("seed routes", "err", err)
		os.Exit(1)
	}

	if err := registerSelf(db, cfg.listenAddr); err != nil {
		slog.Error("register channel", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		handleSend(w, r, db, cfg)
	})

	srv := &http.Server{Addr: cfg.listenAddr, Handler: mux}
	go func() {
		slog.Info("onbod listening", "addr", cfg.listenAddr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	tick := time.NewTicker(cfg.pollInterval)
	defer tick.Stop()

	poll(db, cfg)
	for {
		select {
		case <-tick.C:
			poll(db, cfg)
		case <-stop:
			srv.Close()
			slog.Info("onbod stopped")
			return
		}
	}
}

func loadConfig() (config, error) {
	cfg := config{
		listenAddr:   ":8091",
		pollInterval: 10 * time.Second,
	}

	cfg.dsn = os.Getenv("DATABASE")
	if cfg.dsn == "" {
		dataDir := os.Getenv("DATA_DIR")
		if dataDir == "" {
			return cfg, fmt.Errorf("DATABASE or DATA_DIR env required")
		}
		cfg.dsn = filepath.Join(dataDir, "store", "messages.db")
		cfg.groupsDir = filepath.Join(dataDir, "groups")
	}

	cfg.secret = os.Getenv("CHANNEL_SECRET")
	cfg.prototype = os.Getenv("ONBOARDING_PROTOTYPE")

	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		apiPort = "8080"
	}
	cfg.gatedURL = "http://gated:" + apiPort

	if g := os.Getenv("GROUPS_DIR"); g != "" {
		cfg.groupsDir = g
	}
	if addr := os.Getenv("ONBOD_LISTEN_ADDR"); addr != "" {
		cfg.listenAddr = addr
	}
	if iv := os.Getenv("ONBOARD_POLL_INTERVAL"); iv != "" {
		if d, err := time.ParseDuration(iv); err == nil {
			cfg.pollInterval = d
		}
	}

	if cfg.groupsDir == "" {
		return cfg, fmt.Errorf("groups dir not set (set DATA_DIR or GROUPS_DIR)")
	}

	return cfg, nil
}

// --- Migration ---

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var max int
	if err := db.QueryRow("SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?",
		serviceName).Scan(&max); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	entries, _ := migrationFS.ReadDir("migrations")
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		ver, _ := strconv.Atoi(f[:4])
		if ver <= max {
			continue
		}
		if ver != max+1 {
			return fmt.Errorf("migration gap: expected %d, got %d", max+1, ver)
		}
		if err := runMigration(db, f, ver); err != nil {
			return err
		}
		max = ver
	}
	return nil
}

func runMigration(db *sql.DB, f string, ver int) error {
	raw, err := migrationFS.ReadFile("migrations/" + f)
	if err != nil {
		return fmt.Errorf("%s: read: %w", f, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(string(raw)); err != nil {
		return fmt.Errorf("%s: %w", f, err)
	}
	if _, err := tx.Exec(
		"INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
		serviceName, ver, time.Now().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("%s: record: %w", f, err)
	}
	return tx.Commit()
}

// --- Startup ---

func seedRoutes(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO routes (jid, seq, type, match, target)
		VALUES ('*', -10, 'command', '/approve', 'onbod'),
		       ('*', -10, 'command', '/reject',  'onbod')`)
	return err
}

func registerSelf(db *sql.DB, listenAddr string) error {
	url := "http://onbod" + listenAddr
	caps := `{"receive_only":true}`
	_, err := db.Exec(
		`INSERT OR REPLACE INTO channels (name, url, capabilities) VALUES ('onbod', ?, ?)`,
		url, caps)
	return err
}

// --- HTTP /send endpoint ---

func handleSend(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	auth := r.Header.Get("Authorization")
	if cfg.secret != "" && (!strings.HasPrefix(auth, "Bearer ") ||
		strings.TrimPrefix(auth, "Bearer ") != cfg.secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		JID  string `json:"jid"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	text := strings.TrimSpace(req.Text)
	switch {
	case strings.HasPrefix(text, "/approve "):
		target := strings.TrimSpace(strings.TrimPrefix(text, "/approve "))
		handleApprove(w, db, cfg, req.JID, target)
	case strings.HasPrefix(text, "/reject "):
		target := strings.TrimSpace(strings.TrimPrefix(text, "/reject "))
		handleReject(w, db, cfg, req.JID, target)
	default:
		http.Error(w, "unknown command", http.StatusBadRequest)
	}
}

// --- Command handlers ---

func isTier0(db *sql.DB, senderJID string) bool {
	var parent *string
	err := db.QueryRow(`
		SELECT rg.parent FROM routes r
		JOIN registered_groups rg ON rg.folder = r.target
		WHERE r.jid = ? AND r.seq = 0 LIMIT 1`, senderJID).Scan(&parent)
	return err == nil && parent == nil
}

func handleApprove(w http.ResponseWriter, db *sql.DB, cfg config, senderJID, targetJID string) {
	if !isTier0(db, senderJID) {
		sendReply(cfg, senderJID, "Permission denied.")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var worldName string
	err := db.QueryRow(
		`SELECT world_name FROM onboarding WHERE jid = ? AND status = 'pending'`,
		targetJID).Scan(&worldName)
	if err != nil {
		slog.Warn("approve: onboarding not found", "jid", targetJID, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	groupDir := filepath.Join(cfg.groupsDir, worldName)
	if err := os.MkdirAll(groupDir, 0755); err != nil {
		slog.Error("approve: mkdir", "dir", groupDir, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if cfg.prototype != "" {
		if err := copyDir(cfg.prototype, groupDir); err != nil {
			slog.Warn("approve: copy prototype", "err", err)
		}
	}

	now := time.Now().Format(time.RFC3339)

	if _, err := db.Exec(`
		INSERT OR IGNORE INTO registered_groups (jid, name, folder, added_at)
		VALUES (?, ?, ?, ?)`,
		targetJID, worldName, worldName, now); err != nil {
		slog.Error("approve: insert registered_groups", "err", err)
	}

	if cfg, err := core.LoadConfig(); err == nil {
		if err := container.SeedGroupDir(cfg, worldName); err != nil {
			slog.Warn("approve: seed group dir", "folder", worldName, "err", err)
		}
	}

	if _, err := db.Exec(`
		INSERT OR IGNORE INTO routes (jid, seq, type, match, target)
		VALUES (?, 0, 'default', NULL, ?),
		       (?, -2, 'prefix', '@', ?),
		       (?, -1, 'prefix', '#', ?)`,
		targetJID, worldName,
		targetJID, worldName,
		targetJID, worldName); err != nil {
		slog.Error("approve: insert routes", "err", err)
	}

	welcomeID := fmt.Sprintf("onboard-welcome-%s-%d", targetJID, time.Now().UnixNano())
	welcomeBody := fmt.Sprintf(
		`<system_event type="onboard_welcome">Your workspace %s is ready. Welcome!</system_event>`,
		worldName)
	if _, err := db.Exec(`
		INSERT INTO messages
		  (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source, group_folder)
		VALUES (?, ?, 'system', ?, ?, 1, 1, 'onboarding', '')`,
		welcomeID, targetJID, welcomeBody, time.Now().Format(time.RFC3339Nano)); err != nil {
		slog.Error("approve: insert welcome message", "err", err)
	}

	if _, err := db.Exec(`UPDATE onboarding SET status = 'approved' WHERE jid = ?`, targetJID); err != nil {
		slog.Error("approve: update onboarding status", "err", err)
	}

	seedDefaultTasks(db, worldName, targetJID)

	roots := rootJIDs(db)
	notify.Send(roots, "Approved: "+targetJID+" -> "+worldName+"/",
		func(jid, text string) error { sendReply(cfg, jid, text); return nil })

	slog.Info("approved", "jid", targetJID, "world", worldName)
	w.WriteHeader(http.StatusOK)
}

func handleReject(w http.ResponseWriter, db *sql.DB, cfg config, senderJID, targetJID string) {
	if !isTier0(db, senderJID) {
		sendReply(cfg, senderJID, "Permission denied.")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	db.Exec(`UPDATE onboarding SET status = 'rejected' WHERE jid = ?`, targetJID)

	roots := rootJIDs(db)
	notify.Send(roots, "Rejected: "+targetJID,
		func(jid, text string) error { sendReply(cfg, jid, text); return nil })

	slog.Info("rejected", "jid", targetJID)
	w.WriteHeader(http.StatusOK)
}

// --- Poll loop ---

func poll(db *sql.DB, cfg config) {
	promptNew(db, cfg)
	checkNameResponse(db, cfg)
	checkPendingMessages(db, cfg)
}

func promptNew(db *sql.DB, cfg config) {
	rows, err := db.Query(
		`SELECT jid FROM onboarding WHERE status = 'awaiting_name' AND prompted_at IS NULL`)
	if err != nil {
		slog.Error("promptNew query", "err", err)
		return
	}
	defer rows.Close()

	now := time.Now().Format(time.RFC3339)
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		sendReply(cfg, jid, "Pick a name for your workspace:")
		db.Exec(`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`, now, jid)
		slog.Info("prompted new user", "jid", jid)
	}
}

type onboardRow struct{ jid, promptedAt string }

func queryOnboarding(db *sql.DB, where string) ([]onboardRow, error) {
	rows, err := db.Query(`SELECT jid, prompted_at FROM onboarding WHERE ` + where)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []onboardRow
	for rows.Next() {
		var r onboardRow
		rows.Scan(&r.jid, &r.promptedAt)
		out = append(out, r)
	}
	return out, nil
}

func checkNameResponse(db *sql.DB, cfg config) {
	pending, err := queryOnboarding(db, "status = 'awaiting_name' AND prompted_at IS NOT NULL")
	if err != nil {
		slog.Error("checkNameResponse query", "err", err)
		return
	}

	for _, r := range pending {
		var content string
		err := db.QueryRow(`
			SELECT content FROM messages
			WHERE chat_jid = ? AND timestamp > ? AND is_bot_message = 0
			ORDER BY timestamp DESC LIMIT 1`,
			r.jid, r.promptedAt).Scan(&content)
		if err != nil {
			continue
		}

		name := strings.TrimSpace(content)
		if !nameRE.MatchString(name) {
			sendReply(cfg, r.jid, "Invalid name. Use lowercase letters, numbers, and hyphens only. Try again:")

			db.Exec(`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`,
				time.Now().Format(time.RFC3339), r.jid)
			continue
		}

		if nameTaken(db, name, r.jid) {
			sendReply(cfg, r.jid, "That name is already taken. Try another:")
			db.Exec(`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`,
				time.Now().Format(time.RFC3339), r.jid)
			continue
		}

		db.Exec(
			`UPDATE onboarding SET status = 'pending', world_name = ?, prompted_at = ? WHERE jid = ?`,
			name, time.Now().Format(time.RFC3339), r.jid)

		roots := rootJIDs(db)
		msg := fmt.Sprintf(
			"New onboarding request: %s wants world %q. Send /approve %s or /reject %s",
			r.jid, name, r.jid, r.jid)
		notify.Send(roots, msg,
			func(jid2, text string) error { sendReply(cfg, jid2, text); return nil })

		slog.Info("onboarding pending", "jid", r.jid, "world", name)
	}
}

func checkPendingMessages(db *sql.DB, cfg config) {
	pending, err := queryOnboarding(db, "status = 'pending' AND prompted_at IS NOT NULL")
	if err != nil {
		slog.Error("checkPendingMessages query", "err", err)
		return
	}

	for _, r := range pending {
		var count int
		db.QueryRow(`
			SELECT COUNT(*) FROM messages
			WHERE chat_jid = ? AND timestamp > ? AND is_bot_message = 0`,
			r.jid, r.promptedAt).Scan(&count)
		if count == 0 {
			continue
		}

		sendReply(cfg, r.jid, "Still waiting for approval.")
		db.Exec(`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`,
			time.Now().Format(time.RFC3339), r.jid)
	}
}

// --- Helpers ---

func nameTaken(db *sql.DB, name, excludeJID string) bool {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM registered_groups WHERE folder = ?`, name).Scan(&n)
	if n > 0 {
		return true
	}
	db.QueryRow(`
		SELECT COUNT(*) FROM onboarding
		WHERE world_name = ? AND status IN ('awaiting_name','pending') AND jid != ?`,
		name, excludeJID).Scan(&n)
	return n > 0
}

func rootJIDs(db *sql.DB) []string {
	rows, err := db.Query(`SELECT jid FROM registered_groups WHERE parent IS NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		jids = append(jids, jid)
	}
	return jids
}

func sendReply(cfg config, jid, text string) {
	body, _ := json.Marshal(map[string]string{"jid": jid, "text": text})
	req, _ := http.NewRequest("POST", cfg.gatedURL+"/v1/outbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cfg.secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("send reply failed", "jid", jid, "err", err)
		return
	}
	resp.Body.Close()
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

var defaultTasks = [][2]string{
	{"/compact-memories episodes day", "0 2 * * *"},
	{"/compact-memories episodes week", "0 3 * * 1"},
	{"/compact-memories episodes month", "0 4 1 * *"},
	{"/compact-memories diary week", "0 3 * * 1"},
	{"/compact-memories diary month", "0 4 1 * *"},
}

func seedDefaultTasks(db *sql.DB, folder, chatJID string) {
	now := time.Now().Format(time.RFC3339)
	for i, t := range defaultTasks {
		id := fmt.Sprintf("%s-mem-%d", folder, i)
		db.Exec(`INSERT OR IGNORE INTO scheduled_tasks
			(id,owner,chat_jid,prompt,cron,next_run,status,created_at,context_mode)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			id, folder, chatJID, t[0], t[1], now, "active", now, "isolated")
	}
	slog.Info("seeded default tasks", "folder", folder)
}
