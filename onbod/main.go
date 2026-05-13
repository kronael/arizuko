package main

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
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

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
	_ "modernc.org/sqlite"
)

type gate struct {
	kind        string // "github", "google", "email", "*"
	param       string // "org=mycompany", "domain=company.com", etc.
	limitPerDay int
}

type config struct {
	core         *core.Config
	dsn          string
	secret       string
	authSecret   string
	listenAddr   string
	gatedURL     string
	pollInterval time.Duration
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
	stripUnsigned := auth.StripUnsigned(os.Getenv("PROXYD_HMAC_SECRET"))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /onboard", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleOnboard(w, r, db, cfg)
	}))
	mux.HandleFunc("POST /onboard", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleOnboardPost(w, r, db, cfg)
	}))
	mux.HandleFunc("GET /invite/{token}", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleInvite(w, r, db, cfg)
	}))

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
	coreCfg, err := core.LoadConfig()
	if err != nil {
		return config{}, err
	}
	if coreCfg.ProjectRoot == "" {
		return config{}, fmt.Errorf("DATA_DIR env required")
	}
	cfg := config{
		core:         coreCfg,
		dsn:          filepath.Join(coreCfg.ProjectRoot, "store", "messages.db"),
		secret:       coreCfg.ChannelSecret,
		authSecret:   coreCfg.AuthSecret,
		authBaseURL:  coreCfg.AuthBaseURL,
		secureCookie: strings.HasPrefix(coreCfg.AuthBaseURL, "https://"),
		greeting:     os.Getenv("ONBOARDING_GREETING"),
		gatedURL:     chanlib.EnvOr("ROUTER_URL", "http://gated:8080"),
		listenAddr:   chanlib.EnvOr("ONBOD_LISTEN_ADDR", ":8080"),
		pollInterval: 10 * time.Second,
	}
	if iv := os.Getenv("ONBOARD_POLL_INTERVAL"); iv != "" {
		if d, err := time.ParseDuration(iv); err == nil {
			cfg.pollInterval = d
		}
	}
	return cfg, nil
}

func gateKey(g gate) string {
	if g.kind == "*" {
		return "*"
	}
	return g.kind + ":" + g.param
}

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

func emailDomain(sub string) string {
	if i := strings.LastIndex(sub, "@"); i >= 0 {
		return sub[i+1:]
	}
	return ""
}

// promptCoolDown is the minimum interval between re-prompts for a JID
// whose user_sub never bound. Prevents thrashing if the user clicks the
// link, bails on OAuth, and re-messages repeatedly.
const promptCoolDown = 30 * time.Minute

func promptUnprompted(db *sql.DB, cfg config) {
	resetRow(db)

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
	base := strings.TrimRight(cfg.authBaseURL, "/")
	for _, jid := range pending {
		token := core.GenHexToken()
		db.Exec(`UPDATE onboarding SET token = ?, token_expires = ?, prompted_at = ? WHERE jid = ?`,
			token, expires, now, jid)

		link := base + "/onboard?token=" + token
		prompt := "Set up your account: " + link
		if cfg.greeting != "" {
			prompt = cfg.greeting + "\n" + prompt
		}
		sendReply(cfg, jid, prompt)
		slog.Info("sent auth link", "jid", jid)
	}
}

func resetRow(db *sql.DB) {
	cutoff := time.Now().Add(-promptCoolDown).Format(time.RFC3339)
	res, err := db.Exec(
		`UPDATE onboarding
		 SET status = 'awaiting_message', token = NULL, prompted_at = NULL
		 WHERE status = 'token_used' AND user_sub IS NULL
		   AND prompted_at IS NOT NULL AND prompted_at < ?`,
		cutoff)
	if err != nil {
		slog.Error("resetRow update", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("reset stale onboarding rows for re-prompt", "count", n)
	}
}

func handleOnboard(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
	token := r.URL.Query().Get("token")
	userSub := r.Header.Get("X-User-Sub")

	if token != "" {
		handleTokenLanding(w, r, db, cfg, token)
		return
	}
	if userSub != "" {
		ensureCSRFToken(w, r, cfg)
		handleDashboard(w, r, db, cfg, userSub)
		return
	}
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func handleTokenLanding(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, token string) {
	now := time.Now().Format(time.RFC3339)
	var jid string
	err := db.QueryRow(
		`SELECT jid FROM onboarding
		 WHERE token = ?
		   AND status IN ('awaiting_message', 'token_used')
		   AND user_sub IS NULL
		   AND (token_expires IS NULL OR token_expires > ?)`,
		token, now).Scan(&jid)
	if err != nil {
		slog.Warn("onboard token invalid",
			"token_hash", chanlib.ShortHash(token), "remote", r.RemoteAddr)
		renderPage(w, "Invalid Link",
			template.HTML("<p>This link is invalid, already used, or has expired.</p>"))
		return
	}
	slog.Info("onboard token presented", "jid", jid, "token_hash", chanlib.ShortHash(token))

	http.SetCookie(w, &http.Cookie{
		Name: "onboard_jid", Value: jid, Path: "/",
		MaxAge: 86400, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "auth_return", Value: "/onboard", Path: "/",
		MaxAge: 86400, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
	})

	if userSub := r.Header.Get("X-User-Sub"); userSub != "" {
		claimOnboarding(db, jid, userSub)
		linkJID(db, jid, userSub)
		http.Redirect(w, r, "/onboard", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func claimOnboarding(db *sql.DB, jid, userSub string) bool {
	res, err := db.Exec(
		`UPDATE onboarding
		 SET user_sub = ?, status = 'token_used', token = NULL
		 WHERE jid = ? AND user_sub IS NULL`,
		userSub, jid)
	if err != nil {
		slog.Error("claim onboarding", "jid", jid, "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func handleDashboard(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config, userSub string) {
	if c, err := r.Cookie("onboard_jid"); err == nil && c.Value != "" {
		claimed := claimOnboarding(db, c.Value, userSub)
		// Single-use cookie: clear regardless of claim outcome.
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
	if err := db.QueryRow(`SELECT username FROM auth_users WHERE sub = ?`, userSub).Scan(&username); err != nil {
		renderPage(w, "Error", template.HTML("<p>User not found.</p>"))
		return
	}

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
		if c, err := r.Cookie("pending_target"); err == nil && c.Value != "" {
			renderUsernamePicker(w, username)
			return
		}
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to join. Ask an admin for one.</p>`))
		return
	}

	renderDashboard(w, db, userSub, username)
}

// csrfCookieName double-submit token: set on GET /onboard, required on POST.
// Prevents cross-site forms from exploiting the auth proxy cookie.
const csrfCookieName = "onbod_csrf"

func ensureCSRFToken(w http.ResponseWriter, r *http.Request, cfg config) {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: core.GenHexToken(), Path: "/",
		MaxAge: 86400, HttpOnly: false, Secure: cfg.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.FormValue("csrf")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

func handleOnboardPost(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg config) {
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
	var pendingTarget string
	if c, err := r.Cookie("pending_target"); err == nil {
		pendingTarget = c.Value
	}
	if pendingTarget == "" {
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to create a workspace.</p>`))
		return
	}

	parent := strings.TrimSuffix(pendingTarget, "/")

	username := strings.TrimSpace(r.FormValue("username"))
	if !usernameRe.MatchString(username) {
		renderPage(w, "Invalid Username",
			template.HTML("<p>Username must be 3-30 chars, lowercase letters/numbers/hyphens, start with a letter.</p>"+
				`<p><a href="/onboard">Try again</a></p>`))
		return
	}

	folder := username
	if parent != "" {
		folder = parent + "/" + username
	}

	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, folder).Scan(&exists)
	if exists > 0 {
		renderPage(w, "Username Taken",
			template.HTML("<p>That username is already in use.</p>"+
				`<p><a href="/onboard">Try again</a></p>`))
		return
	}

	db.Exec(`UPDATE auth_users SET username = ? WHERE sub = ?`, username, userSub)

	coreCfg := cfg.core
	if coreCfg == nil {
		var err error
		if coreCfg, err = core.LoadConfig(); err != nil {
			slog.Error("create world: load config", "err", err)
			renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
			return
		}
	}
	prototype := filepath.Join(coreCfg.GroupsDir, parent, "prototype")
	if _, err := os.Stat(prototype); err != nil {
		prototype = ""
	}
	if err := container.SetupGroup(coreCfg, folder, prototype); err != nil {
		slog.Error("create world: setup group", "folder", folder, "err", err)
		renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
		return
	}
	if err := store.New(db).SeedDefaultTasks(folder, folder); err != nil {
		slog.Warn("create world: seed default tasks", "folder", folder, "err", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name: "pending_target", Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: cfg.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})

	parentSQL := sql.NullString{String: parent, Valid: parent != ""}
	now := time.Now().Format(time.RFC3339)
	slinkToken := core.GenSlinkToken()
	db.Exec(`INSERT OR IGNORE INTO groups (folder, name, parent, added_at, slink_token, product) VALUES (?, ?, ?, ?, ?, ?)`,
		folder, username, parentSQL, now, slinkToken, core.DefaultProduct)
	db.QueryRow(`SELECT slink_token FROM groups WHERE folder = ?`, folder).Scan(&slinkToken)

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

	slog.Info("world created", "folder", folder, "parent", parent, "user", userSub)
	if slinkToken != "" {
		http.Redirect(w, r, "/slink/"+slinkToken, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

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
			k := gateKey(*g)
			db.Exec(
				`UPDATE onboarding SET status = 'queued', user_sub = ?, gate = ?, queued_at = ? WHERE jid = ?`,
				userSub, k, now, jid)
			slog.Info("queued jid", "jid", jid, "user", userSub, "gate", k)
			return
		}
		slog.Warn("no matching gate", "jid", jid, "user", userSub)
		return
	}

	db.Exec(`UPDATE onboarding SET status = 'approved', user_sub = ? WHERE jid = ?`,
		userSub, jid)
	slog.Info("approved jid", "jid", jid, "user", userSub, "gate", "none")
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

func admitFromQueue(db *sql.DB) {
	gates := loadGates(db)
	if len(gates) == 0 {
		return
	}
	// Day-range on queued_at (not LIKE prefix) so timezone drift doesn't miscount admissions.
	todayStart := time.Now().Format("2006-01-02") + "T00:00:00Z"
	tomorrowStart := time.Now().Add(24*time.Hour).Format("2006-01-02") + "T00:00:00Z"
	for _, g := range gates {
		k := gateKey(g)
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
		http.SetCookie(w, &http.Cookie{
			Name: "auth_return", Value: "/invite/" + token, Path: "/",
			MaxAge: 86400, HttpOnly: true,
			Secure:   cfg.secureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	st := store.New(db)

	inv, err := st.GetInvite(token)
	if err != nil {
		slog.Warn("invite invalid", "reason", "not_found",
			"token_hash", chanlib.ShortHash(token), "user", userSub)
		renderPage(w, "Invalid Invite",
			template.HTML("<p>This invite link is invalid or does not exist.</p>"))
		return
	}
	if inv.ExpiresAt != nil && time.Now().After(*inv.ExpiresAt) {
		slog.Warn("invite invalid", "reason", "expired",
			"token_hash", chanlib.ShortHash(token), "user", userSub)
		renderPage(w, "Invite Expired",
			template.HTML("<p>This invite link has expired.</p>"))
		return
	}

	consumed, err := st.ConsumeInvite(token, userSub)
	if err != nil {
		slog.Warn("invite invalid", "reason", "exhausted",
			"token_hash", chanlib.ShortHash(token), "user", userSub, "err", err)
		renderPage(w, "Invite Used",
			template.HTML("<p>This invite link has already been used the maximum number of times.</p>"))
		return
	}

	target := consumed.TargetGlob

	slog.Info("invite accepted", "token_hash", chanlib.ShortHash(token),
		"target_glob", target, "user", userSub)

	if strings.HasSuffix(target, "/") {
		http.SetCookie(w, &http.Cookie{
			Name: "pending_target", Value: target, Path: "/",
			MaxAge: 600, HttpOnly: true, Secure: cfg.secureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/onboard", http.StatusSeeOther)
		return
	}

	if rows, err := db.Query(`SELECT jid FROM user_jids WHERE user_sub = ?`, userSub); err == nil {
		var jids []string
		for rows.Next() {
			var jid string
			rows.Scan(&jid)
			jids = append(jids, jid)
		}
		rows.Close()
		for _, jid := range jids {
			db.Exec(`INSERT OR IGNORE INTO routes (seq, match, target) VALUES (0, ?, ?)`,
				"room="+core.JidRoom(jid), target)
		}
	}

	if !strings.Contains(target, "*") {
		var slinkToken string
		db.QueryRow(`SELECT slink_token FROM groups WHERE folder = ?`, target).Scan(&slinkToken)
		if slinkToken != "" {
			http.Redirect(w, r, "/slink/"+slinkToken, http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

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

func userRoutes(db *sql.DB, folders []string) []dashRoute {
	var rows *sql.Rows
	var err error
	if folders == nil {
		rows, err = db.Query(
			`SELECT id, seq, match, target FROM routes ORDER BY seq, id`)
	} else {
		args := make([]any, len(folders))
		for i, f := range folders {
			args[i] = f
		}
		ph := strings.Repeat("?,", len(folders))
		rows, err = db.Query(
			`SELECT id, seq, match, target FROM routes
			 WHERE target IN (`+ph[:len(ph)-1]+`)
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

func isOperator(folders []string) bool {
	for _, f := range folders {
		if f == "**" {
			return true
		}
	}
	return false
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
	if !auth.MatchGroups(folders, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	db.Exec(`DELETE FROM routes WHERE id = ?`, id)
	slog.Info("route deleted", "id", id, "sub", sub)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

// matchRe bounds allowed match patterns: ASCII printable, no spaces/wildcards.
// Prevents cross-tenant interception via wildcards or whitespace-smuggling
// patterns that confuse the matcher. Operators (`**` grant) bypass the
// userOwnsMatch step but still go through this validator.
var matchRe = regexp.MustCompile(`\A[A-Za-z0-9_.:=@/-]+\z`)

// userOwnsMatch reports whether match references a room the user has a user_jids row for.
// Only "room=<id>" is supported; more expressive patterns require operator grants.
func userOwnsMatch(db *sql.DB, sub, match string) bool {
	const prefix = "room="
	if !strings.HasPrefix(match, prefix) {
		return false
	}
	room := match[len(prefix):]
	if room == "" {
		return false
	}
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
	if !auth.MatchGroups(folders, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
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
