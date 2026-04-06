package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	_ "modernc.org/sqlite"
)

type config struct {
	dsn          string
	secret       string
	listenAddr   string
	gatedURL     string
	pollInterval time.Duration
	prototype    string
	greeting     string
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

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		return cfg, fmt.Errorf("DATA_DIR env required")
	}
	cfg.dsn = filepath.Join(dataDir, "store", "messages.db")
	cfg.secret = os.Getenv("CHANNEL_SECRET")
	cfg.prototype = os.Getenv("ONBOARDING_PROTOTYPE")
	cfg.greeting = os.Getenv("ONBOARDING_GREETING")

	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		apiPort = "8080"
	}
	cfg.gatedURL = "http://gated:" + apiPort

	if addr := os.Getenv("ONBOD_LISTEN_ADDR"); addr != "" {
		cfg.listenAddr = addr
	}
	if iv := os.Getenv("ONBOARD_POLL_INTERVAL"); iv != "" {
		if d, err := time.ParseDuration(iv); err == nil {
			cfg.pollInterval = d
		}
	}

	return cfg, nil
}

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
		args := strings.Fields(strings.TrimPrefix(text, "/approve "))
		if len(args) < 2 {
			http.Error(w, "usage: /approve <jid> <folder>", http.StatusBadRequest)
			return
		}
		handleApprove(w, db, cfg, req.JID, args[0], args[1])
	case strings.HasPrefix(text, "/reject "):
		target := strings.TrimSpace(strings.TrimPrefix(text, "/reject "))
		handleReject(w, db, cfg, req.JID, target)
	default:
		http.Error(w, "unknown command", http.StatusBadRequest)
	}
}

func isTier0(db *sql.DB, senderJID string) bool {
	var parent *string
	err := db.QueryRow(`
		SELECT rg.parent FROM routes r
		JOIN groups rg ON rg.folder = r.target
		WHERE r.jid = ? AND r.seq = 0 LIMIT 1`, senderJID).Scan(&parent)
	return err == nil && parent == nil
}

func handleApprove(w http.ResponseWriter, db *sql.DB, cfg config, senderJID, targetJID, folder string) {
	if !isTier0(db, senderJID) {
		sendReply(cfg, senderJID, "Permission denied.", "")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var status string
	err := db.QueryRow(
		`SELECT status FROM onboarding WHERE jid = ? AND status = 'pending'`,
		targetJID).Scan(&status)
	if err != nil {
		slog.Warn("approve: onboarding not found", "jid", targetJID, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("approve: load config", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := container.SetupGroup(coreCfg, folder, cfg.prototype); err != nil {
		slog.Error("approve: setup group", "folder", folder, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := approveInTx(db, targetJID, folder); err != nil {
		slog.Error("approve tx", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	msg := "Approved: " + targetJID + " -> " + folder
	notifyRoots(db, cfg, senderJID, msg)
	slog.Info("approved", "jid", targetJID, "folder", folder)
	w.WriteHeader(http.StatusOK)
}

func approveInTx(db *sql.DB, jid, folder string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Format(time.RFC3339)

	// Create parent groups if they don't exist (world, world/house)
	parts := strings.Split(folder, "/")
	for i := range parts {
		parent := ""
		if i > 0 {
			parent = strings.Join(parts[:i], "/")
		}
		seg := strings.Join(parts[:i+1], "/")
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO groups (folder, name, parent, added_at) VALUES (?, ?, ?, ?)`,
			seg, parts[i], nilStr(parent), now); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO routes (jid, seq, type, match, target)
		 VALUES (?, 0, 'default', NULL, ?), (?, -2, 'prefix', '@', ?), (?, -1, 'prefix', '#', ?)`,
		jid, folder, jid, folder, jid, folder); err != nil {
		return err
	}
	welcomeID := fmt.Sprintf("onboard-welcome-%s-%d", jid, time.Now().UnixNano())
	welcomeBody := fmt.Sprintf(
		`<system_event type="onboard_welcome">Your room %s is ready. Welcome!</system_event>`,
		folder)
	if _, err := tx.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source, group_folder)
		 VALUES (?, ?, 'system', ?, ?, 1, 1, 'onboarding', '')`,
		welcomeID, jid, welcomeBody, time.Now().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE onboarding SET status = 'approved' WHERE jid = ?`, jid); err != nil {
		return err
	}

	seedDefaultTasksTx(tx, folder, jid)
	return tx.Commit()
}

func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func handleReject(w http.ResponseWriter, db *sql.DB, cfg config, senderJID, targetJID string) {
	if !isTier0(db, senderJID) {
		sendReply(cfg, senderJID, "Permission denied.", "")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	db.Exec(`UPDATE onboarding SET status = 'rejected' WHERE jid = ?`, targetJID)

	msg := "Rejected: " + targetJID
	notifyRoots(db, cfg, senderJID, msg)
	slog.Info("rejected", "jid", targetJID)
	w.WriteHeader(http.StatusOK)
}

func poll(db *sql.DB, cfg config) {
	promptUnprompted(db, cfg)
	checkResponses(db, cfg)
	checkPendingMessages(db, cfg)
}

func promptUnprompted(db *sql.DB, cfg config) {
	rows, err := db.Query(
		`SELECT jid, channel FROM onboarding WHERE status = 'awaiting_message' AND prompted_at IS NULL`)
	if err != nil {
		slog.Error("promptUnprompted query", "err", err)
		return
	}
	type row struct{ jid, channel string }
	var pending []row
	for rows.Next() {
		var r row
		rows.Scan(&r.jid, &r.channel)
		pending = append(pending, r)
	}
	rows.Close()

	now := time.Now().Format(time.RFC3339)
	for _, r := range pending {
		prompt := "Leave a message for the admin:"
		if cfg.greeting != "" {
			prompt = cfg.greeting + "\n" + prompt
		}
		sendReply(cfg, r.jid, prompt, r.channel)
		db.Exec(`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`, now, r.jid)
		slog.Info("prompted user", "jid", r.jid)
	}
}

type onboardRow struct{ jid, promptedAt, channel string }

func checkResponses(db *sql.DB, cfg config) {
	rows, err := db.Query(
		`SELECT jid, prompted_at, channel FROM onboarding
		 WHERE status = 'awaiting_message' AND prompted_at IS NOT NULL`)
	if err != nil {
		slog.Error("checkResponses query", "err", err)
		return
	}
	defer rows.Close()
	var pending []onboardRow
	for rows.Next() {
		var r onboardRow
		rows.Scan(&r.jid, &r.promptedAt, &r.channel)
		pending = append(pending, r)
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
		now := time.Now().Format(time.RFC3339)
		db.Exec(`UPDATE onboarding SET status = 'pending', prompted_at = ? WHERE jid = ?`, now, r.jid)
		submitForApproval(db, cfg, r, strings.TrimSpace(content))
	}
}

func submitForApproval(db *sql.DB, cfg config, r onboardRow, userMsg string) {
	msg := fmt.Sprintf("New onboarding request from %s", r.jid)
	if userMsg != "" {
		msg += fmt.Sprintf("\nMessage: %s", userMsg)
	}
	msg += fmt.Sprintf("\n/approve %s <folder> or /reject %s", r.jid, r.jid)
	for _, root := range rootJIDs(db) {
		sendReply(cfg, root, msg, "")
	}
	slog.Info("onboarding pending", "jid", r.jid)
}

func checkPendingMessages(db *sql.DB, cfg config) {
	rows, err := db.Query(
		`SELECT jid, prompted_at, channel FROM onboarding
		 WHERE status = 'pending' AND prompted_at IS NOT NULL`)
	if err != nil {
		slog.Error("checkPendingMessages query", "err", err)
		return
	}
	defer rows.Close()
	var pending []onboardRow
	for rows.Next() {
		var r onboardRow
		rows.Scan(&r.jid, &r.promptedAt, &r.channel)
		pending = append(pending, r)
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

		sendReply(cfg, r.jid, "Still waiting for approval.", r.channel)
		db.Exec(`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`,
			time.Now().Format(time.RFC3339), r.jid)
	}
}

func notifyRoots(db *sql.DB, cfg config, senderJID, msg string) {
	for _, jid := range rootJIDs(db) {
		if jid != senderJID {
			sendReply(cfg, jid, msg, "")
		}
	}
	sendReply(cfg, senderJID, msg, "")
}

func rootJIDs(db *sql.DB) []string {
	rows, err := db.Query(
		`SELECT DISTINCT r.jid FROM routes r
		 JOIN groups g ON g.folder = r.target
		 WHERE g.parent IS NULL OR g.parent = ''`)
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

var httpClient = &http.Client{Timeout: 10 * time.Second}

func sendReply(cfg config, jid, text, channel string) {
	payload := map[string]string{"jid": jid, "text": text}
	if channel != "" {
		payload["channel"] = channel
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", cfg.gatedURL+"/v1/outbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cfg.secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.secret)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Warn("send reply failed", "jid", jid, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("send reply non-2xx", "jid", jid, "status", resp.StatusCode)
	}
}

var defaultTasks = [][2]string{
	{"/compact-memories episodes day", "0 2 * * *"},
	{"/compact-memories episodes week", "0 3 * * 1"},
	{"/compact-memories episodes month", "0 4 1 * *"},
	{"/compact-memories diary week", "0 3 * * 1"},
	{"/compact-memories diary month", "0 4 1 * *"},
}

func seedDefaultTasksTx(tx *sql.Tx, folder, chatJID string) {
	now := time.Now().Format(time.RFC3339)
	for i, t := range defaultTasks {
		id := fmt.Sprintf("%s-mem-%d", folder, i)
		tx.Exec(`INSERT OR IGNORE INTO scheduled_tasks
			(id,owner,chat_jid,prompt,cron,next_run,status,created_at,context_mode)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			id, folder, chatJID, t[0], t[1], now, "active", now, "isolated")
	}
	slog.Info("seeded default tasks", "folder", folder)
}
