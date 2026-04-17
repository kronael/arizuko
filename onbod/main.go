package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/onvos/arizuko/auth"
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
	authSecret   string
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
	admitFromQueue(db)
	var admitCount int
	for {
		select {
		case <-tick.C:
			promptUnprompted(db, cfg)
			admitCount++
			// Admit every ~1 minute (pollInterval ticks until we reach 60s worth).
			if admitCount*int(cfg.pollInterval.Seconds()) >= 60 {
				admitFromQueue(db)
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
	cfg.authSecret = os.Getenv("AUTH_SECRET")
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
	// TODO(proxyd-auth): X-User-Sub is trusted unconditionally; see comment
	// in handleOnboardPost for the shared-secret-HMAC plan.
	token := r.URL.Query().Get("token")
	userSub := r.Header.Get("X-User-Sub")

	if token != "" {
		handleTokenLanding(w, r, db, cfg, token)
		return
	}

	if userSub != "" {
		// Mint/refresh the CSRF cookie on every authenticated GET so POST
		// handlers have a matching double-submit token available.
		ensureCSRFToken(w, r, cfg)
		handleDashboard(w, r, db, cfg, userSub)
		return
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func handleTokenLanding(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, token string) {
	// Atomic consume: single UPDATE ... RETURNING guards status, token, and
	// expiry. Concurrent requests with the same token race here — only one
	// wins. Prevents SELECT-then-UPDATE replays.
	now := time.Now().Format(time.RFC3339)
	var jid string
	err := db.QueryRow(
		`UPDATE onboarding
		 SET status = 'token_used', token = NULL
		 WHERE token = ? AND status = 'awaiting_message'
		   AND (token_expires IS NULL OR token_expires > ?)
		 RETURNING jid`, token, now).Scan(&jid)
	if err != nil {
		renderPage(w, "Invalid Link",
			template.HTML("<p>This link is invalid, already used, or has expired.</p>"))
		return
	}

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
	// the user's existing world. Atomic claim prevents replay: once the
	// onboarding row is claimed (user_sub set), a stolen cookie can no longer
	// bind the victim's JID to a different account.
	if c, err := r.Cookie("onboard_jid"); err == nil && c.Value != "" {
		res, err := db.Exec(
			`UPDATE onboarding SET user_sub = ?
			 WHERE jid = ? AND status = 'token_used' AND user_sub IS NULL`,
			userSub, c.Value)
		claimed := false
		if err == nil {
			if n, _ := res.RowsAffected(); n == 1 {
				claimed = true
			}
		}
		// Always clear the cookie: single-use regardless of outcome.
		http.SetCookie(w, &http.Cookie{
			Name: "onboard_jid", Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
		})
		if claimed {
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
	}

	var username string
	err := db.QueryRow(`SELECT username FROM auth_users WHERE sub = ?`, userSub).Scan(&username)
	if err != nil {
		renderPage(w, "Error", template.HTML("<p>User not found.</p>"))
		return
	}

	// If this user has a queued JID, show queue position.
	var qGate, qAt string
	if db.QueryRow(
		`SELECT gate, queued_at FROM onboarding WHERE user_sub = ? AND status = 'queued' LIMIT 1`,
		userSub).Scan(&qGate, &qAt) == nil {
		renderQueuePosition(w, db, qGate, qAt)
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

// csrfCookieName is a short-lived token bound to a dashboard session. The
// server sets it on GET /onboard and requires it back on POST in a form
// field. Without the double-submit, any cross-site form that forges a
// POST could exploit the auth proxy's cookie to mutate state.
const csrfCookieName = "onbod_csrf"

// ensureCSRFToken reads (or mints) the CSRF token cookie for this session.
// Returns the current token value.
func ensureCSRFToken(w http.ResponseWriter, r *http.Request, cfg config) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	tok := genToken()
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: tok, Path: "/",
		MaxAge: 86400, HttpOnly: false, // JS does not read it; scripts can't cross-origin anyway
		Secure: cfg.secureCookie, SameSite: http.SameSiteStrictMode,
	})
	return tok
}

// checkCSRF verifies the double-submit token: the form field must equal the
// cookie value using constant-time compare.
func checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.FormValue("csrf")
	if got == "" || len(got) != len(c.Value) {
		return false
	}
	return subtleCompare(got, c.Value)
}

// subtleCompare is a constant-time equality check.
func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func handleOnboardPost(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	// TODO(proxyd-auth): X-User-Sub is trusted unconditionally. When onbod is
	// reachable without proxyd in front (dev, misconfigured networks, exposed
	// port), anyone can impersonate any user by setting the header. Add a
	// shared-secret header (e.g. X-Proxyd-Auth = HMAC(secret, X-User-Sub))
	// verified here; see /home/onvos/app/arizuko/proxyd/main.go where the
	// header is set (around line 387).
	userSub := r.Header.Get("X-User-Sub")
	if userSub == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "csrf token invalid", http.StatusForbidden)
		return
	}
	switch r.FormValue("action") {
	case "create_world":
		handleCreateWorld(w, r, db, cfg, userSub)
	case "delete_route":
		folders := userFolders(db, userSub)
		handleDeleteRoute(w, r, db, userSub, folders)
	case "add_route":
		folders := userFolders(db, userSub)
		handleAddRoute(w, r, db, userSub, folders)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,29}$`)

func handleCreateWorld(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, userSub string) {
	username := strings.TrimSpace(r.FormValue("username"))
	if !usernameRe.MatchString(username) {
		renderPage(w, "Invalid Username",
			template.HTML("<p>Username must be 3-30 chars, lowercase letters/numbers/hyphens, start with a letter.</p>"+
				`<p><a href="/onboard">Try again</a></p>`))
		return
	}

	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, username).Scan(&exists)
	if exists > 0 {
		renderPage(w, "Username Taken",
			template.HTML("<p>That username is already in use.</p>"+
				`<p><a href="/onboard">Try again</a></p>`))
		return
	}

	db.Exec(`UPDATE auth_users SET username = ? WHERE sub = ?`, username, userSub)

	folder := username
	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("create world: load config", "err", err)
		renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
		return
	}
	if err := container.SetupGroup(coreCfg, folder, cfg.prototype); err != nil {
		slog.Error("create world: setup group", "folder", folder, "err", err)
		renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
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

// loadGates reads enabled gates from the onboarding_gates DB table.
func loadGates(db *sql.DB) []gate {
	rows, err := db.Query(
		`SELECT gate, limit_per_day FROM onboarding_gates WHERE enabled = 1`)
	if err != nil {
		slog.Error("loadGates query", "err", err)
		return nil
	}
	defer rows.Close()
	var out []gate
	for rows.Next() {
		var key string
		var limit int
		rows.Scan(&key, &limit)
		out = append(out, gateFromKey(key, limit))
	}
	return out
}

func gateFromKey(key string, limit int) gate {
	if key == "*" {
		return gate{kind: "*", limitPerDay: limit}
	}
	kind, param, _ := strings.Cut(key, ":")
	return gate{kind: kind, param: param, limitPerDay: limit}
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

	gates := loadGates(db)
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

func renderQueuePosition(w http.ResponseWriter, db *sql.DB, gateStr, queuedAt string) {
	var pos int
	db.QueryRow(
		`SELECT COUNT(*) FROM onboarding WHERE status = 'queued' AND gate = ? AND queued_at < ?`,
		gateStr, queuedAt).Scan(&pos)
	pos++ // 1-indexed

	var etaMsg string
	var limit int
	if db.QueryRow(
		`SELECT limit_per_day FROM onboarding_gates WHERE gate = ? AND enabled = 1`,
		gateStr).Scan(&limit) == nil && limit > 0 {
		mins := pos * 1440 / limit
		if mins < 60 {
			etaMsg = fmt.Sprintf("~%d minutes", mins)
		} else {
			etaMsg = fmt.Sprintf("~%d hours", mins/60)
		}
	}

	body := fmt.Sprintf(
		`<p>You're in the queue.</p>`+
			`<p>Position <strong>#%d</strong></p>`+
			`<p class="dim">Estimated wait: %s</p>`+
			`<p class="dim">This page will update automatically.</p>`,
		pos, html.EscapeString(etaMsg))
	w.Header().Set("Refresh", "30")
	renderPage(w, "Queued", template.HTML(body))
}

// admitFromQueue promotes queued users up to each gate's daily limit.
func admitFromQueue(db *sql.DB) {
	gates := loadGates(db)
	if len(gates) == 0 {
		return
	}
	// Use a day-range on queued_at rather than a LIKE prefix so timezone
	// drift at the writer does not miscount today's admissions.
	todayStart := time.Now().Format("2006-01-02") + "T00:00:00Z"
	tomorrowStart := time.Now().Add(24*time.Hour).Format("2006-01-02") + "T00:00:00Z"
	for _, g := range gates {
		k := gateKey(g)
		// Wrap count + select + update in a single transaction so concurrent
		// poll cycles cannot exceed limitPerDay.
		tx, err := db.Begin()
		if err != nil {
			slog.Error("admitFromQueue begin", "gate", k, "err", err)
			continue
		}
		var admitted int
		tx.QueryRow(
			`SELECT COUNT(*) FROM onboarding
			 WHERE gate = ? AND status = 'approved'
			   AND queued_at >= ? AND queued_at < ?`,
			k, todayStart, tomorrowStart).Scan(&admitted)
		remaining := g.limitPerDay - admitted
		if remaining <= 0 {
			tx.Rollback()
			continue
		}
		rows, err := tx.Query(
			`SELECT jid FROM onboarding
			 WHERE status = 'queued' AND gate = ?
			 ORDER BY queued_at ASC LIMIT ?`, k, remaining)
		if err != nil {
			slog.Error("admitFromQueue query", "gate", k, "err", err)
			tx.Rollback()
			continue
		}
		var batch []string
		for rows.Next() {
			var jid string
			rows.Scan(&jid)
			batch = append(batch, jid)
		}
		rows.Close()
		for _, jid := range batch {
			if _, err := tx.Exec(
				`UPDATE onboarding SET status = 'approved' WHERE jid = ?`,
				jid); err != nil {
				slog.Error("admitFromQueue update", "jid", jid, "err", err)
			}
		}
		if err := tx.Commit(); err != nil {
			slog.Error("admitFromQueue commit", "gate", k, "err", err)
			continue
		}
		for _, jid := range batch {
			slog.Info("admitted from queue", "jid", jid, "gate", k)
		}
	}
}

func handleInvite(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	token := r.PathValue("token")
	if token == "" {
		renderPage(w, "Invalid Invite", template.HTML("<p>No invite token provided.</p>"))
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

	// Check existence + expiry first for clearer user-facing messages.
	var expires sql.NullString
	if err := db.QueryRow(
		`SELECT expires FROM invitations WHERE token = ?`, token,
	).Scan(&expires); err != nil {
		renderPage(w, "Invalid Invite",
			template.HTML("<p>This invite link is invalid or does not exist.</p>"))
		return
	}
	if expires.Valid && expires.String != "" {
		exp, _ := time.Parse(time.RFC3339, expires.String)
		if !exp.IsZero() && time.Now().After(exp) {
			renderPage(w, "Invite Expired",
				template.HTML("<p>This invite link has expired.</p>"))
			return
		}
	}

	// Atomic consume: guard uses < max_uses inside the UPDATE itself so
	// concurrent redemptions of a 1-use invite cannot both succeed.
	var folder string
	err := db.QueryRow(
		`UPDATE invitations SET uses = uses + 1
		 WHERE token = ? AND uses < max_uses
		 RETURNING folder`, token,
	).Scan(&folder)
	if err != nil {
		renderPage(w, "Invite Used",
			template.HTML("<p>This invite link has already been used the maximum number of times.</p>"))
		return
	}

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

// renderPage writes a full HTML page. body is template.HTML; callers MUST
// html.EscapeString any user input before wrapping with template.HTML(...).
func renderPage(w http.ResponseWriter, title string, body template.HTML) {
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
	renderPage(w, "Create Workspace", template.HTML(body))
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

// userFolders returns the folders the user has access to.
// nil means operator (unrestricted).
func userFolders(db *sql.DB, sub string) []string {
	rows, err := db.Query(
		`SELECT folder FROM user_groups WHERE user_sub = ? ORDER BY folder`, sub)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var folders []string
	for rows.Next() {
		var f string
		rows.Scan(&f)
		if f != "" {
			folders = append(folders, f)
		}
	}
	return folders
}

type dashRoute struct {
	ID     int64
	Seq    int
	Match  string
	Target string
}

// userRoutes returns routes targeting the user's folders (or all if operator).
func userRoutes(db *sql.DB, folders []string) []dashRoute {
	var rows *sql.Rows
	var err error
	if folders == nil {
		rows, err = db.Query(
			`SELECT id, seq, match, target FROM routes ORDER BY seq, id`)
	} else {
		// Build placeholders
		ph := make([]string, len(folders))
		args := make([]any, len(folders))
		for i, f := range folders {
			ph[i] = "?"
			args[i] = f
		}
		rows, err = db.Query(
			`SELECT id, seq, match, target FROM routes
			 WHERE target IN (`+strings.Join(ph, ",")+`)
			 ORDER BY seq, id`, args...)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dashRoute
	for rows.Next() {
		var r dashRoute
		rows.Scan(&r.ID, &r.Seq, &r.Match, &r.Target)
		out = append(out, r)
	}
	return out
}


// isOperator reports whether folders grants unrestricted access. Either a
// nil slice (internal bypass) or any `**` entry qualifies.
func isOperator(folders []string) bool {
	if folders == nil {
		return true
	}
	for _, f := range folders {
		if f == "**" {
			return true
		}
	}
	return false
}

func folderAllowed(folders []string, target string) bool {
	if folders == nil {
		return true // no grants queried (internal caller bypass)
	}
	return auth.MatchGroups(folders, target)
}

func handleDeleteRoute(w http.ResponseWriter, r *http.Request,
	db *sql.DB, sub string, folders []string) {
	id, err := strconv.ParseInt(r.FormValue("route_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid route_id", http.StatusBadRequest)
		return
	}

	var target string
	err = db.QueryRow(
		`SELECT target FROM routes WHERE id = ?`, id).Scan(&target)
	if err != nil {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if !folderAllowed(folders, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	db.Exec(`DELETE FROM routes WHERE id = ?`, id)
	slog.Info("route deleted", "id", id, "sub", sub)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

// matchRe bounds allowed match patterns: ASCII printable, no spaces/wildcards.
// Operators (folders == nil) bypass this. Prevents cross-tenant interception
// via wildcards or whitespace-smuggling patterns that confuse the matcher.
var matchRe = regexp.MustCompile(`^[A-Za-z0-9_.:=@/-]+$`)

// userOwnsMatch reports whether the supplied match pattern references a room
// (JID suffix) that the user has a linked user_jids row for. Operators
// (folders == nil) bypass this check.
func userOwnsMatch(db *sql.DB, sub, match string) bool {
	// Only support the canonical "room=<id>" form for the onboarding UI.
	// More expressive patterns require operator grants.
	const prefix = "room="
	if !strings.HasPrefix(match, prefix) {
		return false
	}
	room := match[len(prefix):]
	if room == "" {
		return false
	}
	// Check user_jids for a jid whose room portion (after ':') matches.
	rows, err := db.Query(
		`SELECT jid FROM user_jids WHERE user_sub = ?`, sub)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		if i := strings.Index(jid, ":"); i > 0 && jid[i+1:] == room {
			return true
		}
	}
	return false
}

func handleAddRoute(w http.ResponseWriter, r *http.Request,
	db *sql.DB, sub string, folders []string) {
	match := strings.TrimSpace(r.FormValue("match"))
	target := strings.TrimSpace(r.FormValue("target"))
	if match == "" || target == "" {
		http.Error(w, "match and target required", http.StatusBadRequest)
		return
	}
	if len(match) > 256 || len(target) > 256 {
		http.Error(w, "match or target too long", http.StatusBadRequest)
		return
	}
	if !matchRe.MatchString(match) {
		http.Error(w, "invalid match characters", http.StatusBadRequest)
		return
	}
	if !folderAllowed(folders, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Non-operators may only route from their own JIDs. Operators (folders
	// either nil-bypass or a `**` grant) may create any match → target pair.
	if !isOperator(folders) && !userOwnsMatch(db, sub, match) {
		http.Error(w, "forbidden: match does not reference a linked account",
			http.StatusForbidden)
		return
	}

	_, err := db.Exec(
		`INSERT INTO routes (seq, match, target) VALUES (0, ?, ?)`,
		match, target)
	if err != nil {
		slog.Error("add route", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("route added", "match", match, "target", target, "sub", sub)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}
