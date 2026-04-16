package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
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

	mux := http.NewServeMux()
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

	promptUnprompted(db, cfg)
	for {
		select {
		case <-tick.C:
			promptUnprompted(db, cfg)
		case <-stop:
			srv.Close()
			slog.Info("onbod stopped")
			return
		}
	}
}

func loadConfig() (config, error) {
	cfg := config{
		listenAddr:   ":8080",
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

	cfg.gatedURL = os.Getenv("ROUTER_URL")
	if cfg.gatedURL == "" {
		cfg.gatedURL = "http://gated:8080"
	}

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

	db.Exec(`UPDATE onboarding SET status = 'token_used', token = NULL WHERE token = ?`, token)

	http.SetCookie(w, &http.Cookie{
		Name: "onboard_jid", Value: jid, Path: "/",
		MaxAge: 86400, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "auth_return", Value: "/onboard", Path: "/",
		MaxAge: 86400, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
	})

	if userSub := r.Header.Get("X-User-Sub"); userSub != "" {
		linkJID(db, jid, userSub)
		http.Redirect(w, r, "/onboard", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func handleDashboard(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, userSub string) {
	// Second-platform auto-link: consume pending JID cookie and route it into
	// the user's existing world.
	if c, err := r.Cookie("onboard_jid"); err == nil && c.Value != "" {
		var status string
		err := db.QueryRow(
			`SELECT status FROM onboarding WHERE jid = ? AND status = 'token_used'`,
			c.Value).Scan(&status)
		if err == nil {
			linkJID(db, c.Value, userSub)
			var folder string
			if err := db.QueryRow(
				`SELECT g.folder FROM groups g
				 JOIN user_groups ug ON ug.folder = g.folder
				 WHERE ug.user_sub = ? LIMIT 1`, userSub).Scan(&folder); err == nil {
				db.Exec(`INSERT OR IGNORE INTO routes (seq, match, target) VALUES (0, ?, ?)`,
					"room="+core.JidRoom(c.Value), folder)
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name: "onboard_jid", Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
		})
	}

	var username string
	err := db.QueryRow(`SELECT username FROM auth_users WHERE sub = ?`, userSub).Scan(&username)
	if err != nil {
		renderPage(w, "Error", "<p>User not found.</p>")
		return
	}

	var groupCount int
	db.QueryRow(`SELECT COUNT(*) FROM user_groups WHERE user_sub = ?`, userSub).Scan(&groupCount)

	if groupCount == 0 {
		renderUsernamePicker(w, username)
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
	if r.FormValue("action") != "create_world" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	handleCreateWorld(w, r, db, cfg, userSub)
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

	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, username).Scan(&exists)
	if exists > 0 {
		renderPage(w, "Username Taken",
			"<p>That username is already in use.</p>"+
				`<p><a href="/onboard">Try again</a></p>`)
		return
	}

	db.Exec(`UPDATE auth_users SET username = ? WHERE sub = ?`, username, userSub)

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

	var jids []string
	if rows, err := db.Query(`SELECT jid FROM user_jids WHERE user_sub = ?`, userSub); err == nil {
		for rows.Next() {
			var jid string
			rows.Scan(&jid)
			jids = append(jids, jid)
		}
		rows.Close()
	}
	for _, jid := range jids {
		db.Exec(`INSERT OR IGNORE INTO routes (seq, match, target) VALUES (0, ?, ?)`,
			"room="+core.JidRoom(jid), folder)
	}

	slog.Info("world created", "folder", folder, "user", userSub)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

func linkJID(db *sql.DB, jid, userSub string) {
	var existingSub string
	if err := db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = ?`, jid).Scan(&existingSub); err == nil && existingSub != userSub {
		slog.Warn("jid already claimed", "jid", jid, "existing", existingSub, "attempted", userSub)
		return
	}
	now := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT OR IGNORE INTO user_jids (user_sub, jid, claimed) VALUES (?, ?, ?)`,
		userSub, jid, now)
	db.Exec(`UPDATE onboarding SET status = 'approved', user_sub = ? WHERE jid = ?`,
		userSub, jid)
	slog.Info("linked jid", "jid", jid, "user", userSub)
}

func renderPage(w http.ResponseWriter, title, body string) {
	safeTitle := html.EscapeString(title)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>arizuko — %s</title>
<style>
:root{--bg:#0a0a0a;--fg:#e0e0e0;--accent:#4ade80;--accent2:#a78bfa;--accent3:#58a6ff;--dim:#666;--border:#222;--card:#111}
[data-theme=light]{--bg:#fafafa;--fg:#1a1a1a;--accent:#16a34a;--accent2:#7c3aed;--accent3:#0969da;--dim:#888;--border:#ddd;--card:#fff}
*{box-sizing:border-box}
body{font-family:"SF Mono","Fira Code","JetBrains Mono",Consolas,monospace;font-size:14px;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;color:var(--fg);background:var(--bg)}
.card{background:var(--card);border:1px solid var(--border);padding:2rem;border-radius:6px;max-width:520px;width:calc(100%% - 2rem)}
.brand{color:var(--accent);font-weight:bold;font-size:1.1em;margin:0 0 .3em}
h2{margin:0 0 1rem;font-size:1.15em;color:var(--accent3)}
p{margin:.4em 0}
input{width:100%%;padding:.5rem;margin:.25rem 0 1rem;border:1px solid var(--border);border-radius:4px;background:var(--bg);color:var(--fg);font-family:inherit;font-size:.9em}
input:focus{outline:none;border-color:var(--accent3)}
button{padding:.55rem 1.2rem;background:var(--accent);color:var(--bg);border:none;border-radius:4px;cursor:pointer;font-family:inherit;font-weight:bold;font-size:.9em}
button:hover{opacity:.9}
a{color:var(--accent)}
a:hover{text-decoration:underline}
table{width:100%%;border-collapse:collapse;margin:.5rem 0;font-size:.9em}
td,th{padding:.35rem .6rem;text-align:left;border-bottom:1px solid var(--border)}
th{color:var(--accent2);font-weight:normal;text-transform:uppercase;font-size:.8em;letter-spacing:.05em}
</style>
<script>(function(){var t=localStorage.getItem('hub-theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t)})();</script>
</head><body>
<div class="card"><p class="brand">arizuko</p><h2>%s</h2>%s</div>
</body></html>`, safeTitle, safeTitle, body)
}

func renderUsernamePicker(w http.ResponseWriter, currentUsername string) {
	body := fmt.Sprintf(`<p>Pick a username for your workspace.</p>
<form method="POST" action="/onboard">
<input type="hidden" name="action" value="create_world">
<input name="username" placeholder="Username" value="%s" required autofocus
 pattern="[a-z][a-z0-9-]{2,29}" title="3-30 chars, lowercase, starts with letter">
<button type="submit">Create workspace</button>
</form>`, html.EscapeString(currentUsername))
	renderPage(w, "Create Workspace", body)
}

func renderDashboard(w http.ResponseWriter, db *sql.DB, userSub, username string) {
	esc := html.EscapeString

	var jidsHTML string
	if rows, err := db.Query(`SELECT jid, claimed FROM user_jids WHERE user_sub = ? ORDER BY claimed`, userSub); err == nil {
		for rows.Next() {
			var jid, claimed string
			rows.Scan(&jid, &claimed)
			jidsHTML += fmt.Sprintf("<tr><td>%s</td><td>%s</td></tr>", esc(jid), esc(claimed[:10]))
		}
		rows.Close()
	}
	if jidsHTML == "" {
		jidsHTML = "<tr><td colspan=2>No linked accounts yet. Message the bot from a new platform to link it.</td></tr>"
	}

	var groupsHTML string
	if rows, err := db.Query(`SELECT folder FROM user_groups WHERE user_sub = ? ORDER BY folder`, userSub); err == nil {
		for rows.Next() {
			var folder string
			rows.Scan(&folder)
			groupsHTML += fmt.Sprintf("<tr><td>%s</td></tr>", esc(folder))
		}
		rows.Close()
	}

	var routesHTML string
	if rows, err := db.Query(`
		SELECT uj.jid, r.target FROM user_jids uj
		JOIN routes r ON r.match = 'room=' || SUBSTR(uj.jid, INSTR(uj.jid, ':')+1)
		   OR r.match LIKE 'room=' || SUBSTR(uj.jid, INSTR(uj.jid, ':')+1) || ' %'
		WHERE uj.user_sub = ?
		ORDER BY uj.jid, r.target`, userSub); err == nil {
		for rows.Next() {
			var jid, target string
			rows.Scan(&jid, &target)
			routesHTML += fmt.Sprintf("<tr><td>%s</td><td>%s</td></tr>", esc(jid), esc(target))
		}
		rows.Close()
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
`, esc(username), jidsHTML, groupsHTML, routesHTML)
	renderPage(w, "Dashboard", body)
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
