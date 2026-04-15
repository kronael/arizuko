package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
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
	authBaseURL  string
	secureCookie bool
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

	if err := registerSelf(db, cfg.listenAddr); err != nil {
		slog.Error("register channel", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		handleSend(w, r, db, cfg)
	})
	mux.HandleFunc("GET /onboard", func(w http.ResponseWriter, r *http.Request) {
		handleOnboard(w, r, db, cfg)
	})
	mux.HandleFunc("POST /onboard", func(w http.ResponseWriter, r *http.Request) {
		handleOnboardPost(w, r, db, cfg)
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
		listenAddr:   ":8092",
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
	cfg.authBaseURL = os.Getenv("AUTH_BASE_URL")
	cfg.secureCookie = strings.HasPrefix(cfg.authBaseURL, "https://")

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

func registerSelf(db *sql.DB, listenAddr string) error {
	url := "http://onbod" + listenAddr
	caps := `{"receive_only":true}`
	_, err := db.Exec(
		`INSERT OR REPLACE INTO channels (name, url, capabilities) VALUES ('onbod', ?, ?)`,
		url, caps)
	return err
}

// --- Chat-side: /send commands (operator approve/reject) ---

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
	platform, room := core.JidPlatform(senderJID), core.JidRoom(senderJID)
	rows, err := db.Query(`
		SELECT r.match FROM routes r
		JOIN groups rg ON rg.folder = r.target
		WHERE rg.parent IS NULL OR rg.parent = ''`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var match string
		rows.Scan(&match)
		if routeMatchesJID(match, platform, room) {
			return true
		}
	}
	return false
}

func routeMatchesJID(match, platform, room string) bool {
	if match == "" {
		return false
	}
	for _, tok := range strings.Fields(match) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || v == "" || strings.ContainsAny(v, "*?[") {
			return false
		}
		switch k {
		case "room":
			if v != room {
				return false
			}
		case "platform":
			if v != platform {
				return false
			}
		case "chat_jid":
			if v != platform+":"+room && v != room {
				return false
			}
		}
	}
	return true
}

func jidFromMatch(match string) string {
	var platform, room string
	for _, tok := range strings.Fields(match) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || v == "" || strings.ContainsAny(v, "*?[") {
			continue
		}
		switch k {
		case "platform":
			platform = v
		case "room":
			room = v
		case "chat_jid":
			return v
		}
	}
	if room == "" {
		return ""
	}
	if platform == "" {
		return room
	}
	return platform + ":" + room
}

func handleApprove(w http.ResponseWriter, db *sql.DB, cfg config, senderJID, targetJID, folder string) {
	if !isTier0(db, senderJID) {
		sendReply(cfg, senderJID, "Permission denied.")
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

	match := "room=" + core.JidRoom(jid)
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM routes WHERE match = ? AND target = ?`,
		match, folder).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.Exec(
			`INSERT INTO routes (seq, match, target) VALUES (?, ?, ?)`,
			0, match, folder); err != nil {
			return err
		}
	}
	welcomeID := core.MsgID("onboard-welcome")
	welcomeBody := fmt.Sprintf(
		`<system_event type="onboard_welcome">Your room %s is ready. Welcome!</system_event>`,
		folder)
	if _, err := tx.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source)
		 VALUES (?, ?, 'system', ?, ?, 1, 1, '')`,
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
		sendReply(cfg, senderJID, "Permission denied.")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	db.Exec(`UPDATE onboarding SET status = 'rejected' WHERE jid = ?`, targetJID)

	msg := "Rejected: " + targetJID
	notifyRoots(db, cfg, senderJID, msg)
	slog.Info("rejected", "jid", targetJID)
	w.WriteHeader(http.StatusOK)
}

// --- Poll: send auth links to unprompted JIDs ---

func poll(db *sql.DB, cfg config) {
	promptUnprompted(db, cfg)
}

func promptUnprompted(db *sql.DB, cfg config) {
	rows, err := db.Query(
		`SELECT jid FROM onboarding WHERE status = 'awaiting_message' AND prompted_at IS NULL`)
	if err != nil {
		slog.Error("promptUnprompted query", "err", err)
		return
	}
	var pending []string
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		pending = append(pending, jid)
	}
	rows.Close()

	now := time.Now().Format(time.RFC3339)
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	for _, jid := range pending {
		token := genToken()
		db.Exec(`UPDATE onboarding SET token = ?, token_expires = ?, prompted_at = ? WHERE jid = ?`,
			token, expires, now, jid)

		base := strings.TrimRight(cfg.authBaseURL, "/")
		link := base + "/onboard?token=" + token
		prompt := "Set up your account: " + link
		if cfg.greeting != "" {
			prompt = cfg.greeting + "\n" + prompt
		}
		sendReply(cfg, jid, prompt)
		slog.Info("sent auth link", "jid", jid)
	}
}

func genToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Web: /onboard handlers ---

func handleOnboard(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	token := r.URL.Query().Get("token")
	userSub := r.Header.Get("X-User-Sub")

	if token != "" {
		handleTokenLanding(w, r, db, cfg, token)
		return
	}

	if userSub != "" {
		handleDashboard(w, r, db, cfg, userSub)
		return
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func handleTokenLanding(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, token string) {
	var jid, expires string
	err := db.QueryRow(
		`SELECT jid, token_expires FROM onboarding WHERE token = ? AND status = 'awaiting_message'`,
		token).Scan(&jid, &expires)
	if err != nil {
		renderPage(w, "Invalid Link", "<p>This link is invalid or has already been used.</p>")
		return
	}
	exp, _ := time.Parse(time.RFC3339, expires)
	if time.Now().After(exp) {
		renderPage(w, "Link Expired", "<p>This link has expired. Please message the bot again for a new one.</p>")
		return
	}

	// Set cookies: onboard_token (for linking JID after OAuth) + auth_return (redirect after OAuth)
	http.SetCookie(w, &http.Cookie{
		Name: "onboard_token", Value: token, Path: "/",
		MaxAge: 86400, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "auth_return", Value: "/onboard", Path: "/",
		MaxAge: 86400, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
	})

	// If user already authenticated, link directly
	if userSub := r.Header.Get("X-User-Sub"); userSub != "" {
		linkJID(db, token, jid, userSub)
		http.Redirect(w, r, "/onboard", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func handleDashboard(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, userSub string) {
	// Check for pending token cookie — link JID if present
	if c, err := r.Cookie("onboard_token"); err == nil && c.Value != "" {
		var jid string
		err := db.QueryRow(
			`SELECT jid FROM onboarding WHERE token = ? AND status = 'awaiting_message'`,
			c.Value).Scan(&jid)
		if err == nil {
			linkJID(db, c.Value, jid, userSub)
		}
		http.SetCookie(w, &http.Cookie{
			Name: "onboard_token", Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
		})
	}

	// Check if user has a world
	var username string
	err := db.QueryRow(`SELECT username FROM auth_users WHERE sub = ?`, userSub).Scan(&username)
	if err != nil {
		renderPage(w, "Error", "<p>User not found.</p>")
		return
	}

	// Check if user has any groups
	var groupCount int
	db.QueryRow(`SELECT COUNT(*) FROM user_groups WHERE user_sub = ?`, userSub).Scan(&groupCount)

	if groupCount == 0 {
		renderUsernamePicker(w, username, userSub)
		return
	}

	renderDashboard(w, db, userSub, username)
}

func handleOnboardPost(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	userSub := r.Header.Get("X-User-Sub")
	if userSub == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	action := r.FormValue("action")
	switch action {
	case "create_world":
		handleCreateWorld(w, r, db, cfg, userSub)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,29}$`)

func handleCreateWorld(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, userSub string) {
	username := strings.TrimSpace(r.FormValue("username"))
	if !usernameRe.MatchString(username) {
		renderPage(w, "Invalid Username",
			"<p>Username must be 3-30 chars, lowercase letters/numbers/hyphens, start with a letter.</p>"+
				`<p><a href="/onboard">Try again</a></p>`)
		return
	}

	// Check uniqueness
	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, username).Scan(&exists)
	if exists > 0 {
		renderPage(w, "Username Taken",
			"<p>That username is already in use.</p>"+
				`<p><a href="/onboard">Try again</a></p>`)
		return
	}

	// Update auth_users username
	db.Exec(`UPDATE auth_users SET username = ? WHERE sub = ?`, username, userSub)

	// Create world group
	folder := username
	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("create world: load config", "err", err)
		renderPage(w, "Error", "<p>Internal error.</p>")
		return
	}
	if err := container.SetupGroup(coreCfg, folder, cfg.prototype); err != nil {
		slog.Error("create world: setup group", "folder", folder, "err", err)
		renderPage(w, "Error", "<p>Internal error.</p>")
		return
	}

	now := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT OR IGNORE INTO groups (folder, name, parent, added_at) VALUES (?, ?, NULL, ?)`,
		folder, username, now)
	db.Exec(`INSERT OR IGNORE INTO user_groups (user_sub, folder) VALUES (?, ?)`,
		userSub, folder)

	// Route all claimed JIDs to the new world
	rows, _ := db.Query(`SELECT jid FROM user_jids WHERE user_sub = ?`, userSub)
	if rows != nil {
		var jids []string
		for rows.Next() {
			var j string
			rows.Scan(&j)
			jids = append(jids, j)
		}
		rows.Close()
		for _, jid := range jids {
			match := "room=" + core.JidRoom(jid)
			db.Exec(`INSERT OR IGNORE INTO routes (seq, match, target) VALUES (0, ?, ?)`,
				match, folder)
		}
	}

	slog.Info("world created", "folder", folder, "user", userSub)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

func linkJID(db *sql.DB, token, jid, userSub string) {
	now := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT OR IGNORE INTO user_jids (user_sub, jid, claimed) VALUES (?, ?, ?)`,
		userSub, jid, now)
	db.Exec(`UPDATE onboarding SET status = 'approved', user_sub = ? WHERE token = ?`,
		userSub, token)
	slog.Info("linked jid to user", "jid", jid, "user", userSub)
}

// --- HTML rendering ---

func renderPage(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>%s</title>
<style>
body{font-family:system-ui;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;background:#f5f5f5}
.card{background:#fff;padding:2rem;border-radius:8px;box-shadow:0 2px 8px rgba(0,0,0,.1);max-width:500px;width:100%%}
h2{margin:0 0 1rem}
input{width:100%%;padding:.5rem;margin:.25rem 0 1rem;box-sizing:border-box;border:1px solid #ddd;border-radius:4px}
button{padding:.5rem 1rem;background:#333;color:#fff;border:none;border-radius:4px;cursor:pointer}
a{color:#333}
table{width:100%%;border-collapse:collapse;margin:.5rem 0}
td,th{padding:.25rem .5rem;text-align:left;border-bottom:1px solid #eee}
</style></head><body>
<div class="card"><h2>%s</h2>%s</div>
</body></html>`, title, title, body)
}

func renderUsernamePicker(w http.ResponseWriter, currentUsername, userSub string) {
	body := fmt.Sprintf(`<p>Pick a username for your workspace.</p>
<form method="POST" action="/onboard">
<input type="hidden" name="action" value="create_world">
<input name="username" placeholder="Username" value="%s" required autofocus
 pattern="[a-z][a-z0-9-]{2,29}" title="3-30 chars, lowercase, starts with letter">
<button type="submit">Create workspace</button>
</form>`, currentUsername)
	renderPage(w, "Create Workspace", body)
}

func renderDashboard(w http.ResponseWriter, db *sql.DB, userSub, username string) {
	// Fetch JIDs
	var jidsHTML string
	rows, _ := db.Query(`SELECT jid, claimed FROM user_jids WHERE user_sub = ? ORDER BY claimed`, userSub)
	if rows != nil {
		for rows.Next() {
			var jid, claimed string
			rows.Scan(&jid, &claimed)
			jidsHTML += fmt.Sprintf("<tr><td>%s</td><td>%s</td></tr>", jid, claimed[:10])
		}
		rows.Close()
	}
	if jidsHTML == "" {
		jidsHTML = "<tr><td colspan=2>No linked accounts yet. Message the bot from a new platform to link it.</td></tr>"
	}

	// Fetch groups
	var groupsHTML string
	rows2, _ := db.Query(`SELECT folder FROM user_groups WHERE user_sub = ? ORDER BY folder`, userSub)
	if rows2 != nil {
		for rows2.Next() {
			var folder string
			rows2.Scan(&folder)
			groupsHTML += fmt.Sprintf("<tr><td>%s</td></tr>", folder)
		}
		rows2.Close()
	}

	// Fetch routes for user's JIDs
	var routesHTML string
	rows3, _ := db.Query(`
		SELECT uj.jid, r.target FROM user_jids uj
		JOIN routes r ON r.match LIKE '%%room=' || REPLACE(REPLACE(uj.jid, uj.jid, SUBSTR(uj.jid, INSTR(uj.jid, ':')+1)), ':', '') || '%%'
		WHERE uj.user_sub = ?
		ORDER BY uj.jid, r.target`, userSub)
	if rows3 != nil {
		for rows3.Next() {
			var jid, target string
			rows3.Scan(&jid, &target)
			routesHTML += fmt.Sprintf("<tr><td>%s</td><td>%s</td></tr>", jid, target)
		}
		rows3.Close()
	}
	if routesHTML == "" {
		routesHTML = "<tr><td colspan=2>No routes configured.</td></tr>"
	}

	body := fmt.Sprintf(`
<p>Logged in as <strong>%s</strong></p>

<h3>My accounts</h3>
<table><tr><th>Platform</th><th>Linked</th></tr>%s</table>

<h3>My groups</h3>
<table><tr><th>Group</th></tr>%s</table>

<h3>Routing</h3>
<table><tr><th>From</th><th>To</th></tr>%s</table>
`, username, jidsHTML, groupsHTML, routesHTML)
	renderPage(w, "Dashboard", body)
}

// --- Shared utilities ---

func notifyRoots(db *sql.DB, cfg config, senderJID, msg string) {
	for _, jid := range rootJIDs(db) {
		if jid != senderJID {
			sendReply(cfg, jid, msg)
		}
	}
	sendReply(cfg, senderJID, msg)
}

func rootJIDs(db *sql.DB) []string {
	rows, err := db.Query(
		`SELECT r.match FROM routes r
		 JOIN groups g ON g.folder = r.target
		 WHERE g.parent IS NULL OR g.parent = ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	var jids []string
	for rows.Next() {
		var match string
		rows.Scan(&match)
		jid := jidFromMatch(match)
		if jid == "" {
			continue
		}
		if _, dup := seen[jid]; dup {
			continue
		}
		seen[jid] = struct{}{}
		jids = append(jids, jid)
	}
	return jids
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func sendReply(cfg config, jid, text string) {
	payload := map[string]string{"jid": jid, "text": text}
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
