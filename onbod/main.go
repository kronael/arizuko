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
	"github.com/onvos/arizuko/theme"
	_ "modernc.org/sqlite"
)

type gate struct {
	kind        string // "github", "google", "email", "*"
	param       string // "org=mycompany", "domain=company.com", etc.
	limitPerDay int
}

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
	gates        []gate
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
	mux.HandleFunc("GET /invite/{token}", func(w http.ResponseWriter, r *http.Request) {
		handleInvite(w, r, db, cfg)
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
	admitFromQueue(db, cfg)
	var admitCount int
	for {
		select {
		case <-tick.C:
			promptUnprompted(db, cfg)
			admitCount++
			// Admit every ~1 minute (pollInterval ticks until we reach 60s worth).
			if admitCount*int(cfg.pollInterval.Seconds()) >= 60 {
				admitFromQueue(db, cfg)
				admitCount = 0
			}
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

	if gatesEnv := os.Getenv("ONBOARDING_GATES"); gatesEnv != "" {
		g, err := parseGates(gatesEnv)
		if err != nil {
			return cfg, fmt.Errorf("ONBOARDING_GATES: %w", err)
		}
		cfg.gates = g
	}

	return cfg, nil
}

func parseGates(s string) ([]gate, error) {
	var gates []gate
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		g, err := parseOneGate(part)
		if err != nil {
			return nil, fmt.Errorf("bad gate %q: %w", part, err)
		}
		gates = append(gates, g)
	}
	return gates, nil
}

func parseOneGate(s string) (gate, error) {
	// Formats: "*:50/day", "github:org=mycompany:10/day",
	// "google:domain=example.com:20/day", "email:domain=example.com:5/day"
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return gate{}, fmt.Errorf("need at least kind:limit")
	}
	kind := parts[0]
	var param, limitStr string
	if kind == "*" {
		limitStr = parts[1]
	} else {
		if len(parts) < 3 {
			return gate{}, fmt.Errorf("need kind:param:limit")
		}
		param = parts[1]
		limitStr = parts[2]
	}
	limitStr = strings.TrimSuffix(limitStr, "/day")
	var limit int
	if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil || limit <= 0 {
		return gate{}, fmt.Errorf("invalid limit %q", limitStr)
	}
	return gate{kind: kind, param: param, limitPerDay: limit}, nil
}

// gateKey returns a stable identifier for a gate (used in DB gate column).
func gateKey(g gate) string {
	if g.kind == "*" {
		return "*"
	}
	return g.kind + ":" + g.param
}

// matchGate returns the first gate matching a user sub, or nil.
func matchGate(gates []gate, userSub string) *gate {
	for i := range gates {
		g := &gates[i]
		switch g.kind {
		case "*":
			return g
		case "github":
			if strings.HasPrefix(userSub, "github:") {
				return g
			}
		case "google":
			if !strings.HasPrefix(userSub, "google:") {
				continue
			}
			// param is "domain=X"; extract domain and match
			if d := paramVal(g.param, "domain"); d != "" {
				if emailDomain(userSub) == d {
					return g
				}
			} else {
				return g
			}
		case "email":
			if d := paramVal(g.param, "domain"); d != "" {
				if strings.HasSuffix(userSub, "@"+d) {
					return g
				}
			}
		}
	}
	return nil
}

func paramVal(param, key string) string {
	if strings.HasPrefix(param, key+"=") {
		return param[len(key)+1:]
	}
	return ""
}

// emailDomain extracts domain from "google:user@example.com" style subs.
func emailDomain(sub string) string {
	if i := strings.LastIndex(sub, "@"); i >= 0 {
		return sub[i+1:]
	}
	return ""
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
		linkJID(db, jid, userSub, cfg.gates)
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
			linkJID(db, c.Value, userSub, cfg.gates)
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

	// If gates are active and this user has a queued JID, show queue position.
	if len(cfg.gates) > 0 {
		var qGate, qAt string
		err := db.QueryRow(
			`SELECT gate, queued_at FROM onboarding WHERE user_sub = ? AND status = 'queued' LIMIT 1`,
			userSub).Scan(&qGate, &qAt)
		if err == nil {
			renderQueuePosition(w, db, cfg, qGate, qAt)
			return
		}
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

func linkJID(db *sql.DB, jid, userSub string, gates []gate) {
	var existingSub string
	if err := db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = ?`, jid).Scan(&existingSub); err == nil && existingSub != userSub {
		slog.Warn("jid already claimed", "jid", jid, "existing", existingSub, "attempted", userSub)
		return
	}
	now := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT OR IGNORE INTO user_jids (user_sub, jid, claimed) VALUES (?, ?, ?)`,
		userSub, jid, now)

	if len(gates) > 0 {
		if g := matchGate(gates, userSub); g != nil {
			db.Exec(
				`UPDATE onboarding SET status = 'queued', user_sub = ?, gate = ?, queued_at = ? WHERE jid = ?`,
				userSub, gateKey(*g), now, jid)
			slog.Info("queued jid", "jid", jid, "user", userSub, "gate", gateKey(*g))
			return
		}
		// No matching gate — reject
		slog.Warn("no matching gate", "jid", jid, "user", userSub)
		return
	}

	db.Exec(`UPDATE onboarding SET status = 'approved', user_sub = ? WHERE jid = ?`,
		userSub, jid)
	slog.Info("linked jid", "jid", jid, "user", userSub)
}

func renderQueuePosition(w http.ResponseWriter, db *sql.DB, cfg config, gateStr, queuedAt string) {
	var pos int
	db.QueryRow(
		`SELECT COUNT(*) FROM onboarding WHERE status = 'queued' AND gate = ? AND queued_at < ?`,
		gateStr, queuedAt).Scan(&pos)
	pos++ // 1-indexed

	var etaMsg string
	for _, g := range cfg.gates {
		if gateKey(g) == gateStr {
			mins := pos * 1440 / g.limitPerDay
			if mins < 60 {
				etaMsg = fmt.Sprintf("~%d minutes", mins)
			} else {
				etaMsg = fmt.Sprintf("~%d hours", mins/60)
			}
			break
		}
	}

	body := fmt.Sprintf(
		`<p>You're in the queue.</p>`+
			`<p>Position <strong>#%d</strong></p>`+
			`<p class="dim">Estimated wait: %s</p>`+
			`<p class="dim">This page will update automatically.</p>`,
		pos, html.EscapeString(etaMsg))
	w.Header().Set("Refresh", "30")
	renderPage(w, "Queued", body)
}

// admitFromQueue promotes queued users up to each gate's daily limit.
func admitFromQueue(db *sql.DB, cfg config) {
	if len(cfg.gates) == 0 {
		return
	}
	today := time.Now().Format("2006-01-02")
	for _, g := range cfg.gates {
		k := gateKey(g)
		var admitted int
		db.QueryRow(
			`SELECT COUNT(*) FROM onboarding
			 WHERE gate = ? AND status = 'approved' AND queued_at LIKE ?`,
			k, today+"%").Scan(&admitted)
		remaining := g.limitPerDay - admitted
		if remaining <= 0 {
			continue
		}
		rows, err := db.Query(
			`SELECT jid, user_sub FROM onboarding
			 WHERE status = 'queued' AND gate = ?
			 ORDER BY queued_at ASC LIMIT ?`, k, remaining)
		if err != nil {
			slog.Error("admitFromQueue query", "gate", k, "err", err)
			continue
		}
		var batch []struct{ jid, sub string }
		for rows.Next() {
			var jid, sub string
			rows.Scan(&jid, &sub)
			batch = append(batch, struct{ jid, sub string }{jid, sub})
		}
		rows.Close()
		for _, b := range batch {
			db.Exec(
				`UPDATE onboarding SET status = 'approved' WHERE jid = ?`,
				b.jid)
			slog.Info("admitted from queue", "jid", b.jid, "gate", k)
		}
	}
}

func handleInvite(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	token := r.PathValue("token")
	if token == "" {
		renderPage(w, "Invalid Invite", "<p>No invite token provided.</p>")
		return
	}

	userSub := r.Header.Get("X-User-Sub")
	if userSub == "" {
		// Save return URL and redirect to login.
		http.SetCookie(w, &http.Cookie{
			Name: "auth_return", Value: "/invite/" + token, Path: "/",
			MaxAge: 86400, HttpOnly: true,
			Secure:   cfg.secureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Validate and consume the invitation.
	var folder string
	var createdAt, expires sql.NullString
	var uses, maxUses int
	err := db.QueryRow(
		`SELECT folder, created_at, uses, max_uses, expires
		 FROM invitations WHERE token = ?`, token,
	).Scan(&folder, &createdAt, &uses, &maxUses, &expires)
	if err != nil {
		renderPage(w, "Invalid Invite",
			"<p>This invite link is invalid or does not exist.</p>")
		return
	}
	if expires.Valid && expires.String != "" {
		exp, _ := time.Parse(time.RFC3339, expires.String)
		if !exp.IsZero() && time.Now().After(exp) {
			renderPage(w, "Invite Expired",
				"<p>This invite link has expired.</p>")
			return
		}
	}
	if uses >= maxUses {
		renderPage(w, "Invite Used",
			"<p>This invite link has already been used the maximum number of times.</p>")
		return
	}

	// Consume one use.
	db.Exec(`UPDATE invitations SET uses = uses + 1 WHERE token = ?`, token)

	// Grant access: insert user_groups row.
	db.Exec(
		`INSERT OR IGNORE INTO user_groups (user_sub, folder) VALUES (?, ?)`,
		userSub, folder)

	// Create routes for any linked JIDs.
	if rows, err := db.Query(
		`SELECT jid FROM user_jids WHERE user_sub = ?`, userSub,
	); err == nil {
		var jids []string
		for rows.Next() {
			var jid string
			rows.Scan(&jid)
			jids = append(jids, jid)
		}
		rows.Close()
		for _, jid := range jids {
			db.Exec(
				`INSERT OR IGNORE INTO routes (seq, match, target) VALUES (0, ?, ?)`,
				"room="+core.JidRoom(jid), folder)
		}
	}

	slog.Info("invite accepted", "token", token[:8]+"...",
		"folder", folder, "user", userSub)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

func createInvitation(db *sql.DB, folder, createdBy string, maxUses int) string {
	token := genToken()
	now := time.Now().Format(time.RFC3339)
	db.Exec(
		`INSERT INTO invitations (token, folder, created_by, created_at, max_uses)
		 VALUES (?, ?, ?, ?, ?)`,
		token, folder, createdBy, now, maxUses)
	return token
}

func renderPage(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, theme.Page(title, body))
}

func renderUsernamePicker(w http.ResponseWriter, currentUsername string) {
	body := fmt.Sprintf(`<p class="dim" style="margin-bottom:.8em">Pick a username for your workspace. Lowercase letters, numbers, and hyphens only.</p>
<form method="POST" action="/onboard">
<input type="hidden" name="action" value="create_world">
<input name="username" placeholder="username" value="%s" required autofocus
 pattern="[a-z][a-z0-9-]{2,29}" title="3-30 chars, lowercase, starts with letter"
 style="margin-bottom:1rem">
<button type="submit" style="width:100%%">Create workspace</button>
</form>`, html.EscapeString(currentUsername))
	renderPage(w, "Create Workspace", body)
}

func renderDashboard(w http.ResponseWriter, db *sql.DB, userSub, username string) {
	esc := html.EscapeString

	var jidsHTML string
	if rows, err := db.Query(
		`SELECT jid, claimed FROM user_jids WHERE user_sub = ? ORDER BY claimed`, userSub,
	); err == nil {
		for rows.Next() {
			var jid, claimed string
			rows.Scan(&jid, &claimed)
			platform := jid
			if i := strings.Index(jid, ":"); i > 0 {
				platform = jid[:i]
			}
			jidsHTML += fmt.Sprintf(
				`<tr><td><span class="dot dot-ok"></span> %s</td><td>%s</td><td class="dim">%s</td></tr>`,
				esc(platform), esc(jid), esc(claimed[:10]))
		}
		rows.Close()
	}
	if jidsHTML == "" {
		jidsHTML = `<tr><td colspan="3" class="empty">No linked accounts. Message the bot from any platform to link it.</td></tr>`
	}

	var groupsHTML string
	if rows, err := db.Query(
		`SELECT folder FROM user_groups WHERE user_sub = ? ORDER BY folder`, userSub,
	); err == nil {
		for rows.Next() {
			var folder string
			rows.Scan(&folder)
			groupsHTML += fmt.Sprintf(
				`<tr><td><span class="dot dot-ok"></span> %s</td></tr>`, esc(folder))
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
			routesHTML += fmt.Sprintf(
				`<tr><td>%s</td><td>%s</td></tr>`, esc(jid), esc(target))
		}
		rows.Close()
	}
	if routesHTML == "" {
		routesHTML = `<tr><td colspan="2" class="empty">No routes configured.</td></tr>`
	}

	initial := "?"
	if len(username) > 0 {
		initial = strings.ToUpper(username[:1])
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body>
<div class="page-wide">
<div class="user-header">
  <div class="user-avatar">%s</div>
  <div class="user-meta">
    <div class="brand">%s</div>
    <div class="dim">%s</div>
  </div>
</div>

<div class="cols">
  <div class="section">
    <h3>Accounts</h3>
    <table><tr><th>Platform</th><th>JID</th><th>Linked</th></tr>%s</table>
  </div>
  <div class="section">
    <h3>Groups</h3>
    <table><tr><th>Folder</th></tr>%s</table>
  </div>
  <div class="section card-full">
    <h3>Routing</h3>
    <table><tr><th>From</th><th>To</th></tr>%s</table>
  </div>
</div>
</div></body></html>`,
		theme.Head("Dashboard"),
		esc(initial), esc(username), esc(userSub),
		jidsHTML, groupsHTML, routesHTML)
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
