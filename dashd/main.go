package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/diary"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
	_ "modernc.org/sqlite"
)

//go:embed htmx.min.js
var htmxJS []byte

// Caps bound memory from adversarial writes by a compromised agent.
const (
	maxFileBytes  = 1 << 20
	maxDirEntries = 100
)

var errEscape = errors.New("path escapes sandbox")

func readCapped(path string) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(data) > maxFileBytes
	if truncated {
		data = data[:maxFileBytes]
	}
	return data, truncated, nil
}

func safeJoin(base, leaf string) (string, error) {
	p := filepath.Join(base, leaf)
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
		return "", errEscape
	}
	return real, nil
}

var esc = template.HTMLEscapeString

// folderPath URL-encodes a group folder for use in path segments.
// Nested folders like "rhias/nemo" become "rhias%2Fnemo" so Go's
// net/http mux {folder} wildcard matches them as a single segment.
func folderPath(folder string) string {
	return url.PathEscape(folder)
}

func main() {
	defer obs.Setup("dashd", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("dashd", os.Getenv("ARIZUKO_INSTANCE"))()

	dataDir := os.Getenv("DATA_DIR")
	groupsDir := filepath.Join(dataDir, "groups")

	dsn := os.Getenv("DB_PATH")
	if dsn == "" {
		if dataDir == "" {
			slog.Error("DB_PATH or DATA_DIR env required")
			os.Exit(1)
		}
		dsn = filepath.Join(dataDir, "store", "messages.db")
	}

	port := os.Getenv("DASH_PORT")
	if port == "" {
		port = ":8080"
	}
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	audit.Init(db, os.Getenv("ARIZUKO_INSTANCE"))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceREST,
		Resource: "daemons/dashd",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"port": port,
		},
	})

	slog.Info("dashd started", "db", dsn, "port", port)

	appDir := os.Getenv("HOST_APP_DIR")

	// guard admits a request that PROVES it transited proxyd (an authd-minted
	// ES256 service:proxyd bearer, verified against authd's JWKS), then trusts the
	// proxyd-stamped X-User-Sub/-Groups as the END-USER identity. dashd's authz
	// reads X-User-Sub/-Groups directly, so — unlike webd's MCP gate — the bearer
	// must NOT become the identity (it carries service:proxyd, not the user); we
	// verify it as a transit proof and leave the stamped end-user headers
	// untouched. No verifier (AUTHD_URL unset) → guard is open (local dev),
	// matching webd/proxyd.
	var ks *auth.KeySet
	// svcSrc is dashd's service:dashd ES256 token source, presented on the only
	// daemon-to-daemon call dashd makes: the operator WhatsApp re-pair proxy to
	// whapd's /v1/pair/* (HMAC retire step 5; no CHANNEL_SECRET remains). nil →
	// local dev (no AUTHD_URL); the pair call goes out unauthenticated.
	var svcSrc func(context.Context) (string, error)
	if authdURL := strings.TrimRight(os.Getenv("AUTHD_URL"), "/"); authdURL != "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var kerr error
		if ks, kerr = auth.FetchKeys(ctx, authdURL); kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
		if svcKey := os.Getenv("AUTHD_SERVICE_KEY"); svcKey != "" {
			name := chanlib.EnvOr("AUTHD_SERVICE_NAME", "dashd")
			ts, terr := auth.ServiceToken(authdURL, name, svcKey)
			if terr != nil {
				slog.Error("build dashd service token", "err", terr)
				os.Exit(1)
			}
			svcSrc = ts.Token
		}
	}
	if ks == nil {
		slog.Warn("AUTHD_URL unset in dashd — identity verification disabled, dashd open (local dev)")
	}

	// routd OWNS acl/groups/routes/route_tokens/secrets in the split topology
	// (spec 5/5); dashd mounts the data dir, so it points its admin SQL straight
	// at routd.db (no token plumbing — same FS-access discipline as messages.db).
	var dbRoutd *sql.DB
	if routdPath := filepath.Join(filepath.Dir(dsn), "routd.db"); routdPath != dsn {
		if s, err := sql.Open("sqlite", routdPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"); err == nil {
			dbRoutd = s
			defer dbRoutd.Close()
		} else {
			slog.Warn("open routd.db for admin tables", "path", routdPath, "err", err)
		}
	}

	// onbod OWNS invites/onboarding_gates in the split topology (spec 5/5); the
	// invites admin page writes there directly (same FS-access discipline).
	var dbOnbod *sql.DB
	if onbodPath := filepath.Join(filepath.Dir(dsn), "onbod.db"); onbodPath != dsn {
		if _, statErr := os.Stat(onbodPath); statErr == nil {
			if s, err := sql.Open("sqlite", onbodPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"); err == nil {
				dbOnbod = s
				defer dbOnbod.Close()
			} else {
				slog.Warn("open onbod.db for invites", "path", onbodPath, "err", err)
			}
		}
	}

	mux := http.NewServeMux()
	d := &dash{db: db, dbRW: db, dbRoutd: dbRoutd, dbOnbod: dbOnbod, dbPath: dsn, groupsDir: groupsDir, appDir: appDir, ks: ks, svc: svcSrc}
	d.registerRoutes(mux)
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}

	srv := &http.Server{Addr: port, Handler: obs.HTTPMiddleware("dashd")(chanlib.LogMiddleware(mux))}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("dashd stopped")
		srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}

type dash struct {
	db      *sql.DB
	dbRW    *sql.DB // alias of db for write paths; nil in some tests (read-only paths)
	dbRoutd *sql.DB // routd.db handle. routd OWNS acl/groups/routes/route_tokens/
	// secrets AND the live messages/sessions/cost_log/auth_users tables in the
	// split topology (spec 5/5); dashd reads+writes those tables here. The legacy
	// messages.db twin (db/dbRW) is frozen at cutover — read it for nothing routd owns.
	dbOnbod *sql.DB // onbod.db handle. onbod OWNS invites/onboarding_gates in the
	// split topology (spec 5/5); dashd's invites page reads+writes them here.
	dbPath    string
	groupsDir string
	appDir    string                                // HOST_APP_DIR; used to enumerate stock skills
	ks        *auth.KeySet                          // authd JWKS; verifies proxyd's ES256 transit bearer (nil → local dev open)
	svc       func(context.Context) (string, error) // service:dashd token for the whapd re-pair proxy (nil → local dev)
}

// adminDB returns the handle the admin paths (grants/groups/routes/route_tokens)
// read+write. routd OWNS those tables in the split topology, so dashd points its
// admin SQL straight at routd.db (dbRoutd).
func (d *dash) adminDB() *sql.DB { return d.dbRoutd }

// secretsDB returns the handle the /dash/me/secrets paths read+write — the same
// routd.db handle, since routd also OWNS the secrets table.
func (d *dash) secretsDB() *sql.DB { return d.adminDB() }

// invitesDB returns the handle the invites admin page reads+writes. onbod OWNS
// invites in the split topology, so dashd points its invite SQL straight at
// onbod.db (dbOnbod).
func (d *dash) invitesDB() *sql.DB { return d.dbOnbod }

// guard proves a request transited proxyd (auth.ProxydTransit: an authd-minted
// service:proxyd ES256 bearer) before letting a handler trust the proxyd-stamped
// X-User-Sub/-Groups identity. The bearer is a TRANSIT proof only — dashd's
// authz reads X-User-Sub/-Groups directly, so we deliberately do NOT stamp the
// bearer's own subject over them (it is service:proxyd, not the user); the
// proxyd-stamped end-user headers flow through untouched. No verifier (nil ks →
// local dev / tests) passes through open. Failure redirects to /auth/login.
func (d *dash) guard(next http.HandlerFunc) http.HandlerFunc {
	if d.ks == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(obs.ExtractRequest(r))
		if auth.ProxydTransit(r, d.ks) {
			next(w, r)
			return
		}
		slog.Warn("dashd: identity proof failed",
			"path", r.URL.Path,
			"attempted_sub", r.Header.Get("X-User-Sub"),
			"remote", r.Header.Get("X-Forwarded-For"))
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	}
}

func (d *dash) registerRoutes(mux *http.ServeMux) {
	// Public — no signed identity required. /health is the container
	// healthcheck target; /openapi.json is mounted before auth by contract;
	// the htmx asset is a static file. Guarding any of these breaks the
	// healthcheck or the unauthenticated doc/asset surface.
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("dashd", []string{}))
	mux.HandleFunc("GET /dash/assets/htmx.min.js", handleHtmxAsset)

	// Everything below is operator UI / mutations — every handler reads the
	// proxyd-stamped X-User-Sub. guard verifies proxyd's service:proxyd transit
	// bearer (open when AUTHD_URL is unset for local dev) so a request bypassing
	// proxyd cannot forge an operator identity.
	g := d.guard
	mux.HandleFunc("GET /dash/", g(d.handlePortal))
	mux.HandleFunc("GET /dash/services/", g(d.handleServices))
	mux.HandleFunc("GET /dash/status/", g(d.handleStatus))
	mux.HandleFunc("GET /dash/tasks/", g(d.handleTasks))
	mux.HandleFunc("GET /dash/activity/", g(d.handleActivity))
	mux.HandleFunc("GET /dash/groups/{folder...}", g(d.dispatchGroupsGet))
	mux.HandleFunc("GET /dash/memory/", g(d.handleMemory))
	mux.HandleFunc("GET /dash/profile/", g(d.handleProfile))
	mux.HandleFunc("PUT /dash/memory/", g(d.handleMemoryWrite))
	mux.HandleFunc("DELETE /dash/memory/", g(d.handleMemoryDelete))
	mux.HandleFunc("GET /dash/tasks/x/list", g(d.handleTasksPartial))
	mux.HandleFunc("GET /dash/activity/x/recent", g(d.handleActivityPartial))

	// Task detail, actions, create.
	mux.HandleFunc("GET /dash/tasks/{id}", g(d.handleTaskDetail))
	mux.HandleFunc("POST /dash/tasks/", g(d.handleTaskCreate))
	mux.HandleFunc("POST /dash/tasks/{id}/{action}", g(d.handleTaskAction))

	// /dash/me/secrets — per-user secret CRUD. Identity-bound to signed-in
	// X-User-Sub (proxyd-verified). CSRF on writes via same-origin check.
	mux.HandleFunc("GET /dash/me/secrets", g(d.handleMeSecrets))
	mux.HandleFunc("POST /dash/me/secrets", g(d.handleMeSecretCreate))
	mux.HandleFunc("PATCH /dash/me/secrets/{key}", g(d.handleMeSecretUpdate))
	mux.HandleFunc("DELETE /dash/me/secrets/{key}", g(d.handleMeSecretDelete))

	// WhatsApp re-pair — operator-only (** super-grant). Spec 8/15.
	mux.HandleFunc("GET /dash/channels/whatsapp/pair", g(d.handleWhatsappPair))
	mux.HandleFunc("GET /dash/channels/whatsapp/pair/status", g(d.handleWhatsappPairStatus))
	mux.HandleFunc("POST /dash/channels/whatsapp/pair/start", g(d.handleWhatsappPairStart))

	// Routes editor — admin-gated CRUD against the `routes` table.
	mux.HandleFunc("GET /dash/routes/", g(d.handleRoutes))
	mux.HandleFunc("POST /dash/routes/", g(d.handleRouteCreate))
	mux.HandleFunc("PATCH /dash/routes/{id}", g(d.handleRouteUpdate))
	mux.HandleFunc("DELETE /dash/routes/{id}", g(d.handleRouteDelete))
	mux.HandleFunc("POST /dash/routes/{id}/delete", g(d.handleRouteDelete))

	// Group create / delete / settings — admin-gated. Settings + delete use
	// trailing-wildcard ({folder...}) dispatchers so nested folders like
	// "corp/eng/sre" (multi-segment) address correctly; Go's single-segment
	// {folder} would let them fall through to the list/other handlers.
	mux.HandleFunc("GET /dash/groups/new", g(d.handleGroupNewForm))
	mux.HandleFunc("POST /dash/groups/new", g(d.handleGroupCreate))
	mux.HandleFunc("POST /dash/groups/{folder...}", g(d.dispatchGroupsPost))
	mux.HandleFunc("DELETE /dash/groups/{folder...}", g(d.handleGroupDelete))
	mux.HandleFunc("GET /dash/groups/{folder}/tools", g(d.handleGroupTools))
	mux.HandleFunc("GET /dash/groups/{folder}/grants", g(d.handleGroupGrants))
	mux.HandleFunc("POST /dash/groups/{folder}/grants", g(d.handleGroupGrantAdd))
	mux.HandleFunc("POST /dash/groups/{folder}/grants/revoke", g(d.handleGroupGrantRevoke))

	// Route tokens — issue/revoke per folder.
	mux.HandleFunc("GET /dash/tokens/{folder}/", g(d.handleTokensFolder))
	mux.HandleFunc("POST /dash/tokens/{folder}/", g(d.handleTokensFolder))
	mux.HandleFunc("POST /dash/tokens/{folder}/{jid}/revoke", g(d.handleTokensRevoke))

	// Invite management — operator-gated.
	mux.HandleFunc("GET /dash/invites/", g(d.handleInvites))
	mux.HandleFunc("POST /dash/invites/", g(d.handleInviteCreate))
	mux.HandleFunc("POST /dash/invites/{token}/revoke", g(d.handleInviteRevoke))
}

func (d *dash) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"ok":true}`)
}

func handleHtmxAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(htmxJS)
}

// navLinks: nav order + href + label + operator-only flag. Path-prefix match
// decides which gets aria-current. /dash/ is special-cased so child pages
// don't all light up the home link. Operator-only links (invites) render only
// for callers holding `**`; the scoped links (status/tasks/activity/groups/
// routes/memory/profile) always show — their pages filter to visible folders.
var navLinks = []struct {
	Href, Label string
	operator    bool
}{
	{"/dash/", "arizuko", false},
	{"/dash/services/", "services", true},
	{"/dash/status/", "status", false},
	{"/dash/tasks/", "tasks", false},
	{"/dash/activity/", "activity", false},
	{"/dash/groups/", "groups", false},
	{"/dash/routes/", "routes", false},
	{"/dash/invites/", "invites", true},
	{"/dash/memory/", "memory", false},
	{"/dash/profile/", "profile", false},
}

// dashNavFor renders the top nav with aria-current="page" on the link whose
// href is a prefix of the request path. urlPath "" or "/dash/" lights the home
// link only. Operator-only links are hidden from non-operators (`**` absent
// from the signed X-User-Groups).
func dashNavFor(r *http.Request) string {
	urlPath := r.URL.Path
	operator := false
	for _, g := range callerGroups(r) {
		if g == "**" {
			operator = true
			break
		}
	}
	var b strings.Builder
	b.WriteString(`<nav hx-boost="true" hx-target="#content" hx-swap="innerHTML" hx-indicator="#global-spinner">`)
	for _, l := range navLinks {
		if l.operator && !operator {
			continue
		}
		active := false
		if l.Href == "/dash/" {
			active = urlPath == "" || urlPath == "/dash/" || urlPath == "/dash"
		} else {
			active = strings.HasPrefix(urlPath, l.Href)
		}
		if active {
			fmt.Fprintf(&b, `<a href=%q aria-current="page">%s</a>`, l.Href, l.Label)
		} else {
			fmt.Fprintf(&b, `<a href=%q>%s</a>`, l.Href, l.Label)
		}
	}
	b.WriteString(`</nav><button class="theme-toggle"></button>`)
	b.WriteString(`<div id="global-spinner" class="htmx-indicator dim" style="position:fixed;top:8px;right:12px;font-size:0.85em">loading…</div>`)
	return b.String()
}

func dashHead(title string) string {
	return `<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + esc(title) + ` — arizuko</title>` +
		`<script src="/dash/assets/htmx.min.js"></script>` +
		`<style>` + theme.CSS + `</style>` + theme.ThemeScript + theme.ToggleScript + `</head>`
}

var portalTmpl = template.Must(template.New("portal").Parse(`<!DOCTYPE html><html>{{.Head}}<body>
<div class="page-wide">{{.Nav}}
<h1>arizuko</h1>
<p class="dim">Operator dashboard</p>
{{if .Err}}<div class="banner-err">portal: {{.Err}}</div>{{end}}
<div class="tiles">
<a class="tile" href="/dash/status/"><h2>status<span class="dot {{.StatusDot}}"></span></h2><p>service health</p></a>
<a class="tile" href="/dash/tasks/"><h2>tasks<span class="dot {{.TasksDot}}"></span></h2><p>scheduled jobs</p></a>
<a class="tile" href="/dash/activity/"><h2>activity</h2><p>message flow</p></a>
<a class="tile" href="/dash/groups/"><h2>groups</h2><p>group hierarchy</p></a>
<a class="tile" href="/dash/memory/"><h2>memory</h2><p>knowledge browser</p></a>
<a class="tile" href="/dash/profile/"><h2>profile</h2><p>linked accounts</p></a>
</div>
</div></body></html>`))

func (d *dash) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dash/" {
		http.NotFound(w, r)
		return
	}
	allowed, operator := d.callerScope(r)

	erroredCount := d.countVisibleErroredChats(allowed, operator)
	failedTasks := d.countVisibleFailedTasks(allowed, operator)

	statusDot := "ok"
	if erroredCount > 0 {
		statusDot = "warn"
	}

	tasksDot := "ok"
	if failedTasks >= 3 {
		tasksDot = "err"
	} else if failedTasks > 0 {
		tasksDot = "warn"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := portalTmpl.Execute(w, struct {
		Head      template.HTML
		Nav       template.HTML
		StatusDot string
		TasksDot  string
		Err       string
	}{template.HTML(dashHead("arizuko")), template.HTML(dashNavFor(r)), statusDot, tasksDot, ""}); err != nil {
		slog.Warn("portal: template execute", "err", err)
	}
}

// pageTopFor renders the page shell (or htmx partial) with path-aware nav.
// Full load: DOCTYPE→nav→spinner→<div id="content">→crumbs→h1
// htmx swap: bare crumbs→h1 only — htmx injects this as innerHTML of the
// existing #content div; emitting <div id="content"> would nest it.
func pageTopFor(w http.ResponseWriter, r *http.Request, title string, crumbs ...struct{ Href, Label string }) {
	if !isHtmx(r) {
		fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body><div class="page-wide">%s<div id="content">`,
			dashHead(title), dashNavFor(r))
	}
	if len(crumbs) > 0 {
		var b strings.Builder
		b.WriteString(`<p class="crumbs">`)
		for i, c := range crumbs {
			if i > 0 {
				b.WriteString(` &rsaquo; `)
			}
			if c.Href != "" {
				fmt.Fprintf(&b, `<a href=%q>%s</a>`, c.Href, esc(c.Label))
			} else {
				b.WriteString(esc(c.Label))
			}
		}
		b.WriteString(`</p>`)
		w.Write([]byte(b.String()))
	}
	fmt.Fprintf(w, `<h1>%s</h1>`, esc(title))
}

// pageClose closes the full shell. htmx swaps emit bare content — no wrapper
// to close.
func pageClose(w http.ResponseWriter, r *http.Request) {
	if isHtmx(r) {
		return
	}
	fmt.Fprint(w, `</div></div></body></html>`)
}

func (d *dash) handleStatus(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Status")

	groupCount := d.countVisibleGroups(allowed, operator)
	erroredCount := d.countVisibleErroredChats(allowed, operator)
	var sessionCount int
	// Sessions count is instance-wide infra; only shown in full to operators.
	if operator {
		if err := d.adminDB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount); err != nil {
			slog.Warn("status: sessions scan", "err", err)
		}
	}

	bannerText := fmt.Sprintf("%d groups, %d errored chats", groupCount, erroredCount)
	bannerClass := "ok"
	if erroredCount > 0 {
		bannerClass = "warn"
	}
	fmt.Fprint(w, `<p class="dim">Service health</p>`)
	fmt.Fprint(w, htmlBanner(bannerClass, bannerText))

	metricRows := [][]string{
		{`Groups`, fmt.Sprintf(`%d`, groupCount)},
		{`Active sessions`, fmt.Sprintf(`%d`, sessionCount)},
	}
	fmt.Fprint(w, htmlTable([]string{"metric", "value"}, metricRows))

	pageClose(w, r)
}

func (d *dash) handleTasks(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Tasks")
	fmt.Fprint(w, `<p class="dim">Scheduled jobs. Auto-refreshes every 10s.</p>`)
	fmt.Fprint(w, `<table hx-get="/dash/tasks/x/list" hx-trigger="every 10s" hx-target="tbody" hx-swap="innerHTML">`+
		`<thead><tr><th>ID</th><th>Group</th><th>Prompt</th><th>Cron</th><th>Status</th><th>Created</th><th>Next Run</th></tr></thead>`+
		`<tbody>`)
	d.writeTaskRows(w, allowed, operator)
	fmt.Fprint(w, `</tbody></table>`)

	fmt.Fprint(w, htmlSection("Create task",
		`<form method="post" action="/dash/tasks/">`+
			htmlFormRow("Group (owner)", `<input type="text" name="owner" placeholder="alice" required size="40">`)+
			htmlFormRow("Chat JID", `<input type="text" name="chat_jid" placeholder="alice@s.whatsapp.net" required size="50">`)+
			htmlFormRow("Prompt", `<textarea name="prompt" rows="3" cols="60" required></textarea>`)+
			htmlFormRow("Cron", `<input type="text" name="cron" placeholder="0 9 * * *" required size="20">`)+
			`<p><button type="submit">create</button></p>`+
			`</form>`+
			`<p class="dim">Cron in UTC. Owner = group folder. Chat JID = the chat this task posts to.</p>`,
	))
	pageClose(w, r)
}

func (d *dash) handleTasksPartial(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.writeTaskRows(w, allowed, operator)
}

func (d *dash) writeTaskRows(w http.ResponseWriter, allowed []string, operator bool) {
	rows, err := d.adminDB().Query(
		`SELECT id, owner, COALESCE(prompt,''), cron, status, created_at, next_run
		 FROM scheduled_tasks ORDER BY owner, id LIMIT 500`)
	if err != nil {
		slog.Warn("tasks: query", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=7 class="empty">error: %s</td></tr>`, esc(err.Error()))
		return
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var id, owner, prompt, status, createdAt string
		var cron, nextRun sql.NullString
		if err := rows.Scan(&id, &owner, &prompt, &cron, &status, &createdAt, &nextRun); err != nil {
			slog.Warn("tasks: scan row", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=7 class="empty">scan error: %s</td></tr>`,
				esc(err.Error()))
			continue
		}
		if !visible(allowed, operator, owner) {
			continue
		}
		dot := "dot-ok"
		if status != "active" {
			dot = "dot-warn"
		}
		fmt.Fprintf(w,
			`<tr>`+
				`<td><a href="/dash/tasks/%s"><code>%s</code></a></td>`+
				`<td>%s</td>`+
				`<td title="%s">%s</td>`+
				`<td><code>%s</code></td>`+
				`<td><span class="dot %s"></span>%s</td>`+
				`<td>%s</td>`+
				`<td>%s</td>`+
				`</tr>`,
			esc(id), esc(id),
			esc(owner),
			esc(prompt), esc(truncate64(prompt)),
			esc(cron.String),
			dot, esc(status),
			esc(createdAt),
			esc(nextRun.String),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("tasks: rows", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=7 class="empty">rows error: %s</td></tr>`,
			esc(err.Error()))
		return
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=7 class="empty">No scheduled tasks. `+
			`Ask the agent to schedule one (e.g. "remind me every morning at 8am").</td></tr>`)
	}
}

func (d *dash) handleActivity(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Activity")
	fmt.Fprint(w, `<p class="dim">Last 50 messages across all channels. Auto-refreshes every 10s.</p>`)
	fmt.Fprint(w, `<table hx-get="/dash/activity/x/recent" hx-trigger="every 10s" hx-target="tbody" hx-swap="innerHTML">`+
		`<thead><tr><th>Time</th><th>Source</th><th>Chat</th><th>Sender</th><th>Verb</th><th>Content</th></tr></thead>`+
		`<tbody>`)
	d.writeActivityRows(w, allowed, operator)
	fmt.Fprint(w, `</tbody></table>`)
	pageClose(w, r)
}

func (d *dash) handleActivityPartial(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.writeActivityRows(w, allowed, operator)
}

func (d *dash) writeActivityRows(w http.ResponseWriter, allowed []string, operator bool) {
	// Non-operators may filter out most of the newest 50; over-fetch so a
	// scoped view still fills the table. Operators read exactly 50.
	limit := 50
	if !operator {
		limit = 1000
	}
	rows, err := d.adminDB().Query(
		`SELECT timestamp, source, chat_jid, sender, verb, substr(content,1,80)
		 FROM messages ORDER BY timestamp DESC LIMIT ?`, limit)
	if err != nil {
		slog.Warn("activity: query", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6 class="empty">error: %s</td></tr>`, esc(err.Error()))
		return
	}
	defer rows.Close()
	var n int
	const maxShown = 50
	folderOf := map[string]string{} // memoize chat_jid → folder across rows
	for rows.Next() {
		if n >= maxShown {
			break
		}
		var ts, source, chatJID, sender, content string
		var verb sql.NullString
		if err := rows.Scan(&ts, &source, &chatJID, &sender, &verb, &content); err != nil {
			slog.Warn("activity: scan row", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=6 class="empty">scan error: %s</td></tr>`,
				esc(err.Error()))
			continue
		}
		if !operator {
			folder, seen := folderOf[chatJID]
			if !seen {
				folder = d.jidFolder(chatJID)
				folderOf[chatJID] = folder
			}
			if !visible(allowed, operator, folder) {
				continue
			}
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td><code>%s</code></td><td><code>%s</code></td><td>%s</td><td>%s</td></tr>`,
			esc(ts),
			esc(source),
			esc(chatJID),
			esc(sender),
			esc(verb.String),
			esc(content),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("activity: rows", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6 class="empty">rows error: %s</td></tr>`,
			esc(err.Error()))
		return
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">No messages yet.</td></tr>`)
	}
}

// dispatchGroupsGet routes GET /dash/groups/{folder...}. The trailing
// wildcard captures the whole tail so multi-segment folders (corp/eng/sre)
// resolve; we split off the known action suffix and delegate.
func (d *dash) dispatchGroupsGet(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("folder")
	switch {
	case rest == "":
		d.handleGroups(w, r)
	case strings.HasSuffix(rest, "/settings"):
		d.handleGroupSettings(w, r)
	case strings.HasSuffix(rest, "/tools"):
		d.handleGroupTools(w, r)
	case strings.HasSuffix(rest, "/grants"):
		d.handleGroupGrants(w, r)
	default:
		d.handleGroups(w, r)
	}
}

// dispatchGroupsPost routes POST /dash/groups/{folder...} (settings save,
// delete alias, grant add/revoke) for nested folders.
func (d *dash) dispatchGroupsPost(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("folder")
	switch {
	case strings.HasSuffix(rest, "/settings"):
		d.handleGroupSettingsSave(w, r)
	case strings.HasSuffix(rest, "/delete"):
		d.handleGroupDelete(w, r)
	case strings.HasSuffix(rest, "/grants/revoke"):
		d.handleGroupGrantRevoke(w, r)
	case strings.HasSuffix(rest, "/grants"):
		d.handleGroupGrantAdd(w, r)
	default:
		http.NotFound(w, r)
	}
}

// groupFromPath extracts the folder from a trailing-wildcard {folder...}
// path value by stripping the trailing action segment(s).
func groupFromPath(r *http.Request, suffix string) string {
	rest := r.PathValue("folder")
	return strings.TrimSuffix(rest, suffix)
}

func (d *dash) handleGroups(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Groups")
	fmt.Fprint(w, `<p class="dim">Group hierarchy. Expand a row to see routing rules and links.</p>`+
		`<p><a href="/dash/groups/new">+ New group</a></p>`)

	rows, err := d.adminDB().Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("groups: query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "error: "+err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	var folders []string
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			slog.Warn("groups: scan row", "err", err)
			continue
		}
		if !visible(allowed, operator, folder) {
			continue
		}
		folders = append(folders, folder)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("groups: rows", "err", err)

		fmt.Fprint(w, htmlBanner("err", "rows error: "+err.Error()))
		pageClose(w, r)
		return
	}

	// Bulk-fetch usage in one pass; fall through with empty map on error.
	type usageKey struct {
		tokens7d, cents7d, msgCount int
		lastActive                  string
	}
	usageMap := map[string]usageKey{}
	if len(folders) > 0 {
		s := store.New(d.db)
		if summaries, err := s.GroupUsageBulk(folders); err != nil {
			slog.Warn("groups: usage query", "err", err)
		} else {
			for _, u := range summaries {
				usageMap[u.Folder] = usageKey{u.Tokens7d, u.Cents7d, u.MsgCount, u.LastActive}
			}
		}
	}

	for _, folder := range folders {
		parent := groupfolder.ParentOf(folder)
		label := ""
		if parent == "" {
			label = ` <span class="dim">(root)</span>`
		}
		u := usageMap[folder]
		usageParts := fmt.Sprintf(`%d msgs`, u.msgCount)
		if u.tokens7d > 0 {
			usageParts += fmt.Sprintf(` &middot; %dk tok / 7d`, u.tokens7d/1000)
		}
		if u.cents7d > 0 {
			usageParts += fmt.Sprintf(` &middot; $%.2f / 7d`, float64(u.cents7d)/100)
		}
		if u.lastActive != "" {
			usageParts += ` &middot; last ` + esc(u.lastActive[:10])
		}
		fmt.Fprintf(w,
			`<details><summary><code>%s</code>%s <span class="dim">(%s)</span></summary>`+
				`<div class="group-detail">`,
			esc(folder), label, usageParts,
		)
		parentDisp := parent
		if parentDisp == "" {
			parentDisp = "—"
		}
		fp := folderPath(folder)
		links := fmt.Sprintf(`<a href="/dash/groups/%s/settings">settings</a> &middot; `+
			`<a href="/dash/groups/%s/grants">grants</a> &middot; `+
			`<a href="/dash/tokens/%s/">tokens</a>`, fp, fp, fp)
		fmt.Fprint(w, `<table>`+
			htmlDetail("folder", `<code>`+esc(folder)+`</code>`)+
			htmlDetail("parent", `<code>`+esc(parentDisp)+`</code>`)+
			htmlDetail("links", links)+
			`</table>`)
		d.writeGroupRoutes(w, folder)
		fmt.Fprint(w, `</div></details>`)
	}
	if len(folders) == 0 {
		fmt.Fprint(w, `<p class="empty">No groups configured. `+
			`Run <code>arizuko invite</code> to onboard a user.</p>`)
	}
	pageClose(w, r)
}

func (d *dash) writeGroupRoutes(w http.ResponseWriter, folder string) {
	rows, err := d.adminDB().Query(
		`SELECT seq, match, target FROM routes WHERE target=? OR target LIKE ? ORDER BY seq LIMIT 200`,
		folder, folder+"/%")
	if err != nil {
		slog.Warn("groups: routes query", "err", err, "folder", folder)
		fmt.Fprint(w, htmlBanner("err", "routes error: "+err.Error()))
		return
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		if n == 0 {
			fmt.Fprint(w, `<table><thead><tr><th>Seq</th><th>Match</th><th>Target</th></tr></thead><tbody>`)
		}
		var seq int
		var match, target string
		if err := rows.Scan(&seq, &match, &target); err != nil {
			slog.Warn("groups: routes scan row", "err", err, "folder", folder)
			continue
		}
		fmt.Fprintf(w, `<tr><td>%d</td><td><code>%s</code></td><td><code>%s</code></td></tr>`,
			seq,
			esc(match),
			esc(target),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("groups: routes rows", "err", err, "folder", folder)
	}
	if n > 0 {
		fmt.Fprint(w, `</tbody></table>`)
	} else {
		fmt.Fprint(w, `<p class="empty">no routes targeting this group</p>`)
	}
}

// Allowed: MEMORY.md, .claude/CLAUDE.md, *.md under diary/facts/users/episodes/ (no subdirs).
func memoryPathAllowed(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return false
	}
	clean := filepath.Clean(rel)
	if clean != rel {
		return false
	}
	if clean == "MEMORY.md" || clean == ".claude/CLAUDE.md" || clean == "PERSONA.md" {
		return true
	}
	for _, sub := range []string{"diary/", "facts/", "users/", "episodes/"} {
		if strings.HasPrefix(clean, sub) && strings.HasSuffix(clean, ".md") &&
			!strings.ContainsAny(clean[len(sub):], "/") {
			return true
		}
	}
	return false
}

func parseMemoryPath(urlPath string) (string, string) {
	const prefix = "/dash/memory/"
	if !strings.HasPrefix(urlPath, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(urlPath, prefix)
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", ""
	}
	folder := rest[:i]
	rel := rest[i+1:]
	if folder == "" || rel == "" {
		return "", ""
	}
	return folder, rel
}

func (d *dash) resolveMemoryFile(folder, rel string) (string, error) {
	if !memoryPathAllowed(rel) {
		return "", errors.New("path not allowed")
	}
	groupDir := filepath.Join(d.groupsDir, filepath.Clean(folder))
	if !strings.HasPrefix(groupDir, d.groupsDir+string(filepath.Separator)) &&
		groupDir != d.groupsDir {
		return "", errEscape
	}
	real, err := filepath.EvalSymlinks(groupDir)
	if err != nil {
		return "", err
	}
	if real != d.groupsDir &&
		!strings.HasPrefix(real, d.groupsDir+string(filepath.Separator)) {
		return "", errEscape
	}
	target := filepath.Join(real, rel)
	if st, err := os.Lstat(target); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", errEscape
	}
	if !strings.HasPrefix(target, real+string(filepath.Separator)) {
		return "", errEscape
	}
	return target, nil
}

func (d *dash) handleMemoryWrite(w http.ResponseWriter, r *http.Request) {
	folder, rel := parseMemoryPath(r.URL.Path)
	if folder == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	target, err := d.resolveMemoryFile(folder, rel)
	if err != nil {
		slog.Warn("memory write: resolve", "folder", folder, "rel", rel, "err", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFileBytes+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxFileBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		slog.Warn("memory write: mkdir", "path", target, "err", err)
		http.Error(w, "mkdir", http.StatusInternalServerError)
		return
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		slog.Warn("memory write: tmp", "path", tmp, "err", err)
		http.Error(w, "write", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		slog.Warn("memory write: rename", "path", target, "err", err)
		http.Error(w, "rename", http.StatusInternalServerError)
		return
	}
	slog.Info("memory write", "folder", folder, "rel", rel, "bytes", len(body))
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	folder, rel := parseMemoryPath(r.URL.Path)
	if folder == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	target, err := d.resolveMemoryFile(folder, rel)
	if err != nil {
		slog.Warn("memory delete: resolve", "folder", folder, "rel", rel, "err", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("memory delete: remove", "path", target, "err", err)
		http.Error(w, "remove", http.StatusInternalServerError)
		return
	}
	slog.Info("memory delete", "folder", folder, "rel", rel)
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleMemory(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	selectedGroup := r.URL.Query().Get("group")

	// Refuse to render another tenant's files: a non-operator who hand-crafts
	// ?group=foreign gets 403 before any file read.
	if selectedGroup != "" && !visible(allowed, operator, selectedGroup) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	pageTopFor(w, r, "Memory")
	fmt.Fprint(w, `<p class="dim">Browse per-group MEMORY.md, CLAUDE.md, diary, episodes, users, facts.</p>`)

	rows, err := d.adminDB().Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("memory: groups query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "groups query error: "+err.Error()))
	} else {
		defer rows.Close()
		fmt.Fprint(w, `<form method="get" class="form-narrow">
<label class="dim" for="group">Group</label>
<select id="group" name="group" onchange="this.form.submit()">
<option value="">— select a group —</option>`)
		for rows.Next() {
			var folder string
			if err := rows.Scan(&folder); err != nil {
				slog.Warn("memory: scan row", "err", err)
				continue
			}
			if !visible(allowed, operator, folder) {
				continue
			}
			sel := ""
			if folder == selectedGroup {
				sel = ` selected`
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`,
				esc(folder),
				sel,
				esc(folder),
			)
		}
		if err := rows.Err(); err != nil {
			slog.Warn("memory: rows", "err", err)
		}
		fmt.Fprint(w, `</select></form>`)
	}

	if selectedGroup != "" {
		d.renderMemorySection(w, selectedGroup)
	} else if err == nil {
		fmt.Fprint(w, `<p class="empty">Select a group above to view its memory.</p>`)
	}

	pageClose(w, r)
}

func (d *dash) renderMemorySection(w http.ResponseWriter, folder string) {
	groupDir := filepath.Join(d.groupsDir, filepath.Clean(folder))
	if !strings.HasPrefix(groupDir, d.groupsDir+string(filepath.Separator)) &&
		groupDir != d.groupsDir {
		fmt.Fprint(w, `<p>Invalid group path.</p>`)
		return
	}
	// Resolve symlinks to block escapes; ErrNotExist is fine (group dir may not exist yet).
	if real, err := filepath.EvalSymlinks(groupDir); err == nil {
		if real != d.groupsDir && !strings.HasPrefix(real, d.groupsDir+string(filepath.Separator)) {
			fmt.Fprint(w, `<p>Invalid group path.</p>`)
			return
		}
		groupDir = real
	} else if !os.IsNotExist(err) {
		fmt.Fprint(w, `<p>Invalid group path.</p>`)
		return
	}

	fmt.Fprintf(w, `<p class="dim">Edit in workspace: `+
		`<a href="/dav/%s/MEMORY.md" target="_blank">MEMORY.md</a> &middot; `+
		`<a href="/dav/%s/PERSONA.md" target="_blank">PERSONA.md</a> &middot; `+
		`<a href="/dav/%s/CLAUDE.md" target="_blank">CLAUDE.md</a> &middot; `+
		`<a href="/dav/%s/" target="_blank">workspace/</a></p>`,
		esc(folder), esc(folder), esc(folder), esc(folder))

	var memBuf strings.Builder
	renderCappedFile(&memBuf, groupDir, "MEMORY.md", true)
	fmt.Fprint(w, htmlSection("MEMORY.md", memBuf.String()))

	var personaBuf strings.Builder
	renderCappedFile(&personaBuf, groupDir, "PERSONA.md", false)
	if s := personaBuf.String(); s != "" {
		fmt.Fprint(w, htmlSection("PERSONA.md", s))
	}

	var claudeBuf strings.Builder
	renderCappedFile(&claudeBuf, groupDir, "CLAUDE.md", false)
	if s := claudeBuf.String(); s != "" {
		fmt.Fprint(w, htmlSection("CLAUDE.md", s))
	}

	renderEntries(w, groupDir, "diary", "Diary", true)
	renderEntries(w, groupDir, "episodes", "Episodes", false)
	renderEntries(w, groupDir, "users", "Users", false)
	renderEntries(w, groupDir, "facts", "Facts", false)
}

func renderCappedFile(w io.Writer, groupDir, leaf string, showMissing bool) {
	path, err := safeJoin(groupDir, leaf)
	if err != nil {
		if errors.Is(err, errEscape) {
			slog.Warn("memory: symlink escape", "file", leaf, "groupDir", groupDir)
			fmt.Fprintf(w, `<p><em>%s unavailable (symlink escape)</em></p>`,
				esc(leaf))
			return
		}
		if os.IsNotExist(err) && showMissing {
			fmt.Fprintf(w, `<p><em>%s not found</em></p>`, esc(leaf))
		}
		return
	}
	data, truncated, err := readCapped(path)
	if err != nil {
		slog.Warn("memory: read file", "err", err, "path", path)
		if showMissing {
			fmt.Fprintf(w, `<p><em>%s read error</em></p>`, esc(leaf))
		}
		return
	}
	mtime := ""
	if info, err := os.Stat(path); err == nil {
		mtime = info.ModTime().Format("2006-01-02 15:04")
	}
	truncNote := ""
	if truncated {
		truncNote = fmt.Sprintf(" (truncated at %d bytes)", maxFileBytes)
	}
	if leaf == "MEMORY.md" {
		fmt.Fprintf(w, `<p class="dim">%d bytes &middot; modified %s%s</p><pre>%s</pre>`,
			len(data), mtime, truncNote, esc(string(data)))
	} else {
		fmt.Fprintf(w, `<details><summary>%s%s</summary><pre>%s</pre></details>`,
			esc(leaf), truncNote, esc(string(data)))
	}
}

func renderEntries(w io.Writer, groupDir, sub, title string, openDetails bool) {
	dirPath, err := safeJoin(groupDir, sub)
	if err != nil {
		if errors.Is(err, errEscape) {
			slog.Warn("memory: dir symlink escape", "sub", sub, "groupDir", groupDir)
		}
		return
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}
	var mdFiles []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, e)
		}
	}
	if len(mdFiles) == 0 {
		return
	}
	total := len(mdFiles)
	limit := total
	if limit > maxDirEntries {
		limit = maxDirEntries
	}
	summaryLabel := fmt.Sprintf("%d files", total)
	if total > maxDirEntries {
		summaryLabel = fmt.Sprintf("%d files (showing newest %d)", total, maxDirEntries)
	}
	openAttr := ""
	if openDetails {
		openAttr = " open"
	}
	var items strings.Builder
	items.WriteString(fmt.Sprintf(`<details%s><summary>%s</summary><ul>`, openAttr, esc(summaryLabel)))
	shown := 0
	for i := total - 1; i >= 0 && shown < limit; i-- {
		e := mdFiles[i]
		leafPath, err := safeJoin(dirPath, e.Name())
		if err != nil {
			if errors.Is(err, errEscape) {
				slog.Warn("memory: entry symlink escape", "file", e.Name(), "dir", dirPath)
			}
			continue
		}
		summary := mdSummary(leafPath)
		fmt.Fprintf(&items, `<li><b>%s</b> %s</li>`, esc(e.Name()), esc(summary))
		shown++
	}
	items.WriteString(`</ul></details>`)
	fmt.Fprint(w, htmlSection(title, items.String()))
}

func mdSummary(path string) string {
	if s := diary.ExtractSummary(path); s != "" {
		return s
	}
	data, _, err := readCapped(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && l != "---" {
			return l
		}
	}
	return ""
}
