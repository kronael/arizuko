package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
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
	ownDSN       string // ONBOD_DB_PATH: onbod.db for the OWNED tables
	listenAddr   string
	gatedURL     string
	pollInterval time.Duration
	greeting     string
	authBaseURL  string
	secureCookie bool
	// svcToken is onbod's service:onbod JWT source for routd's JWT-gated
	// /v1/outbound (spec 5/1); always set (required at load).
	svcToken func(context.Context) (string, error)
}

func main() {
	defer obs.Setup("onbod", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("onbod", os.Getenv("ARIZUKO_INSTANCE"))()

	if os.Getenv("ONBOARDING_ENABLED") == "0" {
		slog.Info("onboarding disabled")
		os.Exit(0)
	}

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	// Two handles, dual-path (spec 5/5):
	//   obdb — onbod's OWNED tables (onboarding/invites/onboarding_gates).
	//   xdb  — the CROSS tables (acl/acl_membership/groups/user_profiles/routes), all
	//          routd-OWNED in the split.
	// Monolith (ONBOD_DB_PATH unset): both are the shared messages.db — every
	// owned- and cross-table query stays exactly as before. Split (set): obdb is a
	// separate onbod.db and xdb is routd.db, opened straight from the mounted data
	// dir (same FS-access discipline as dashd's dbRoutd; no token plumbing) so
	// onbod stops touching messages.db entirely. audit.Init targets obdb so the
	// invite/gate store writers' in-tx audit rows land with the mutation (onbod.db
	// owns its own audit_log; monolith reuses messages.db's).
	// Split is the only topology: onbod owns onbod.db and cross-reads routd.db
	// (routd OWNS the cross tables). ONBOD_DB_PATH is required — compose always
	// emits it; onbod opens NO messages.db. A missing routd.db is a misconfigured
	// split, so a failure to open is fatal (no silent empty-DB cross-read).
	if cfg.ownDSN == "" {
		slog.Error("ONBOD_DB_PATH required")
		os.Exit(1)
	}
	var obdb, xdb *sql.DB
	obdb, err = openOwnedDB(cfg.ownDSN)
	if err != nil {
		slog.Error("open onbod.db", "err", err)
		os.Exit(1)
	}
	defer obdb.Close()
	slog.Info("onbod owns split DB", "path", cfg.ownDSN)

	routdPath := filepath.Join(filepath.Dir(cfg.ownDSN), "routd.db")
	xdb, err = sql.Open("sqlite", routdPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		slog.Error("open routd.db", "path", routdPath, "err", err)
		os.Exit(1)
	}
	defer xdb.Close()
	slog.Info("onbod cross-reads routd.db", "path", routdPath)

	audit.Init(obdb, os.Getenv("ARIZUKO_INSTANCE"))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceREST,
		Resource: "daemons/onbod",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"addr": cfg.listenAddr,
		},
	})

	// guard proves a request transited proxyd, then trusts the proxyd-stamped
	// X-User-Sub as the END-USER (the OAuth'd visitor) identity. When AUTHD_URL
	// is set, build authd's JWKS so the ES256 transit bearer (proxyd attaches
	// its service:proxyd token) verifies; unset → nil ks → HMAC-only.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ks *auth.KeySet
	if authdURL := strings.TrimRight(os.Getenv("AUTHD_URL"), "/"); authdURL != "" {
		var kerr error
		if ks, kerr = auth.FetchKeys(ctx, authdURL); kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
	}

	srv := &http.Server{
		Addr:    cfg.listenAddr,
		Handler: obs.HTTPMiddleware("onbod")(newOnbodMux(xdb, obdb, cfg, ks)),
	}
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

	promptUnprompted(obdb, cfg)
	admitFromQueue(obdb)
	var admitCount int
	for {
		select {
		case <-tick.C:
			promptUnprompted(obdb, cfg)
			admitCount++
			if admitCount*int(cfg.pollInterval.Seconds()) >= 60 {
				admitFromQueue(obdb)
				admitCount = 0
			}
		case <-stop:
			srv.Close()
			slog.Info("onbod stopped")
			return
		}
	}
}

// onbodOpenAPIResources names the resources onbod's /openapi.json advertises.
// onbod OWNS all three (spec 5/5 § Daemon ownership); newOnbodMux mounts them
// through the shared resreg handler.
//
// newOnbodMux is the routing table this list is checked against; keep them
// together so a resource added here without a mount fails openapi_test.go
// (BUGS F40).
var onbodOpenAPIResources = []string{"onboarding", "onboarding_gates", "invites"}

// newOnbodMux builds onbod's HTTP surface. Extracted from main so a test can
// read the ROUTING TABLE onbod actually builds rather than a restatement of it —
// the doc-vs-mux guard has to compare the emitted document against the real
// mux, and a mux built inline in main is unreachable.
func newOnbodMux(xdb, obdb *sql.DB, cfg config, ks *auth.KeySet) *http.ServeMux {
	mux := http.NewServeMux()
	// stripUnsigned keeps X-User-Sub only when the request PROVES it transited
	// proxyd — a valid authd ES256 service:proxyd transit bearer. The bearer is a
	// transit proof ONLY: onbod's /onboard reads X-User-Sub as the OAuth'd end-user
	// (matchGate's github:/google: checks), so we must NOT stamp the bearer's own
	// service:proxyd subject over it. An unproven X-User-Sub is stripped (a request
	// bypassing proxyd cannot forge identity); the public flow then redirects to
	// /auth/login. No verifier (local dev, AUTHD_URL unset) → pass through.
	stripUnsigned := stripUnsignedGuard(ks)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("onbod", onbodOpenAPIResources))
	mux.HandleFunc("GET /onboard", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleOnboard(w, r, xdb, obdb, cfg)
	}))
	mux.HandleFunc("POST /onboard", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleOnboardPost(w, r, xdb, cfg)
	}))
	mux.HandleFunc("GET /invite/{token}", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleInvite(w, r, xdb, obdb, cfg)
	}))

	// Bearer-gated admin surface (spec 5/5 § Daemon ownership). onbod OWNS
	// invites + onboarding_gates; the writers (dashd, CLI, routd's /invite +
	// /gate) reach them here instead of writing the DB directly in the split.
	// Verified against authd's JWKS (ks); nil ks (AUTHD_URL unset / monolith) =
	// open, like routd's nil-verifier local-dev path.
	adm := &admin{db: obdb, ks: ks}
	adm.mountOnboarding(mux) // /v1/onboarding via the shared resreg handler (spec 5/16)
	adm.mountInvites(mux)    // /v1/invites via the shared resreg handler (spec 5/16)
	adm.mountGates(mux)      // /v1/gates via the shared resreg handler (spec 5/16)

	// Operator dashboard (spec 6/7): the proxyd transit proof (stripUnsigned)
	// admits the proxyd-stamped end-user identity; handleDash then gates on
	// operator (`**` in X-User-Groups). Distinct from the /v1 bearer gate.
	mux.HandleFunc("GET /dash/onbod/", stripUnsigned(adm.handleDash))
	mux.HandleFunc("POST /dash/onbod/approve/{jid}", stripUnsigned(adm.handleDashApprove))
	mux.HandleFunc("POST /dash/onbod/deny/{jid}", stripUnsigned(adm.handleDashDeny))
	mux.HandleFunc("POST /dash/onbod/reprompt/{jid}", stripUnsigned(adm.handleDashReprompt))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}
	return mux
}

// stripUnsignedGuard is the lenient identity gate for onbod's public surface.
// It keeps an inbound X-User-Sub only when the request proves it transited proxyd
// (auth.ProxydTransit: a valid authd ES256 service:proxyd bearer, ks != nil);
// otherwise it strips the identity headers so a request reaching onbod directly
// cannot forge a user. The bearer is a transit proof only — the proxyd-stamped
// end-user X-User-Sub flows through untouched (never overwritten with the
// bearer's service:proxyd subject). No verifier (nil ks → local dev) passes
// through.
func stripUnsignedGuard(ks *auth.KeySet) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(obs.ExtractRequest(r))
			if r.Header.Get("X-User-Sub") == "" {
				next(w, r)
				return
			}
			if ks == nil {
				next(w, r) // local dev: no JWKS to verify against, trust the header
				return
			}
			if auth.ProxydTransit(r, ks) {
				next(w, r)
				return
			}
			slog.Warn("onbod: unproven identity stripped",
				"path", r.URL.Path, "attempted_sub", r.Header.Get("X-User-Sub"))
			for _, h := range []string{"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig"} {
				r.Header.Del(h)
			}
			next(w, r)
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
		ownDSN:       os.Getenv("ONBOD_DB_PATH"),
		authBaseURL:  coreCfg.AuthBaseURL,
		secureCookie: strings.HasPrefix(coreCfg.AuthBaseURL, "https://"),
		greeting:     os.Getenv("ONBOARDING_GREETING"),
		gatedURL:     chanlib.EnvOr("ROUTER_URL", "http://routd:8080"),
		listenAddr:   chanlib.EnvOr("ONBOD_LISTEN_ADDR", ":8080"),
		pollInterval: 10 * time.Second,
	}
	if iv := os.Getenv("ONBOARD_POLL_INTERVAL"); iv != "" {
		if d, err := time.ParseDuration(iv); err == nil {
			cfg.pollInterval = d
		}
	}
	// Split (spec 5/1) is the only topology: exchange AUTHD_SERVICE_KEY for a
	// service:onbod JWT, presented on routd's JWT-gated /v1/outbound. Required —
	// compose always emits it; an unsigned call to routd is rejected.
	authdURL, key := os.Getenv("AUTHD_URL"), os.Getenv("AUTHD_SERVICE_KEY")
	if authdURL == "" || key == "" {
		return config{}, fmt.Errorf("onbod: AUTHD_URL + AUTHD_SERVICE_KEY required")
	}
	src, err := auth.ServiceToken(authdURL, "onbod", key)
	if err != nil {
		return config{}, fmt.Errorf("onbod service-token source: %w", err)
	}
	cfg.svcToken = src.Token
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
	if _, domain, found := strings.CutLast(sub, "@"); found {
		return domain
	}
	return ""
}

// knownStatuses is the complete set the pipeline can advance. A row in
// anything else is stranded: no query selects it, so it is never prompted,
// queued or admitted, and its jid PRIMARY KEY blocks a fresh insert — the user
// can never onboard from that chat again. Two such rows exist in production
// (BUGS O1), in a 'pending' status no code writes.
var knownStatuses = map[string]bool{
	"awaiting_message": true,
	"token_used":       true,
	"queued":           true,
	"approved":         true,
}

// warnStrandedRows surfaces rows the state machine cannot move. It cannot
// repair them — picking a status for a row of unknown provenance is the
// operator's call — but silence is what let them sit unnoticed.
func warnStrandedRows(db *sql.DB) {
	rows, err := db.Query(`SELECT jid, status FROM onboarding`)
	if err != nil {
		slog.Error("stranded-row scan", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var jid, status string
		if err := rows.Scan(&jid, &status); err != nil {
			slog.Error("stranded-row scan", "err", err)
			return
		}
		if !knownStatuses[status] {
			slog.Error("onboarding row stranded in an unknown status — it will never advance",
				"jid", jid, "status", status, "known", "awaiting_message/token_used/queued/approved")
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("stranded-row scan", "err", err)
	}
}

func promptUnprompted(db *sql.DB, cfg config) {
	warnStrandedRows(db)
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
		// The raw token goes out in the link and is never stored — only
		// store.TokenRef(token) = hex(sha256) lands in token_ref (Z3).
		token := core.GenHexToken()
		// Claim-per-row: the UPDATE re-checks prompted_at IS NULL so two
		// overlapping ticks can't both win the same jid. Only the tick whose
		// UPDATE affected a row sends the link (else the other tick already did).
		res, err := db.Exec(
			`UPDATE onboarding SET token_ref = ?, token_expires = ?, prompted_at = ?
			 WHERE jid = ? AND prompted_at IS NULL`,
			store.TokenRef(token), expires, now, jid)
		if err != nil {
			slog.Error("promptUnprompted claim", "jid", jid, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}

		link := base + "/onboard?token=" + token
		prompt := "Set up your account: " + link
		if cfg.greeting != "" {
			prompt = cfg.greeting + "\n" + prompt
		}
		sendReply(cfg, jid, prompt)
		slog.Info("sent auth link", "jid", jid)
	}
}

// handleOnboard and the onboarding flow below take two handles: db is the
// CROSS-table DB (user_profiles/acl/groups/routes/acl_membership — messages.db in
// monolith, routd.db/auth.db owners in the split) and obdb is onbod's OWNED-
// table DB (onboarding/invites/onboarding_gates — onbod.db in the split). In
// the monolith obdb == db. Owned-table queries route through obdb; cross-table
// queries stay on db.
func handleOnboard(w http.ResponseWriter, r *http.Request, db, obdb *sql.DB, cfg config) {
	token := r.URL.Query().Get("token")
	userSub := r.Header.Get("X-User-Sub")

	if token != "" {
		handleTokenLanding(w, r, db, obdb, cfg, token)
		return
	}
	if userSub != "" {
		handleDashboard(w, r, db, obdb, cfg, userSub, ensureCSRFToken(w, r, cfg))
		return
	}
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func handleTokenLanding(w http.ResponseWriter, r *http.Request, db, obdb *sql.DB, cfg config, token string) {
	now := time.Now().Format(time.RFC3339)
	var jid string
	// The URL carries the RAW token; only its hash is stored (Z3), so hash
	// what was presented and look that up. A token that resolves to no row
	// falls through to the refusal page below — never a silent no-op.
	err := obdb.QueryRow(
		`SELECT jid FROM onboarding
		 WHERE token_ref = ?
		   AND status IN ('awaiting_message', 'token_used')
		   AND user_sub IS NULL
		   AND (token_expires IS NULL OR token_expires > ?)`,
		store.TokenRef(token), now).Scan(&jid)
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
		// Atomic claim by token: two parallel landings of the same token can
		// both pass the SELECT above, but only the request whose UPDATE returns
		// a row links the JID. The loser's RETURNING yields no row → no
		// double-linkJID.
		// Crash ordering: the membership edge is written BEFORE the token is
		// consumed. The two live in different databases (edge in routd's,
		// token in onbod's) with no cross-DB transaction, so one of them must
		// survive a crash between the writes. Edge-first is safe wherever it
		// crashes — AddMembership is INSERT OR IGNORE, so a replay writes the
		// identical row and the still-live token simply gets used again.
		// Consume-first would strand the user: token spent, no edge, no retry.
		if jid, ok := jidForToken(obdb, token); ok {
			if err := linkJID(db, obdb, jid, userSub); err != nil {
				writeLinkErr(w, err)
				return
			}
			if _, ok := claimByToken(obdb, token, userSub); !ok {
				// Lost the race to a concurrent landing that wrote the same
				// edge. Not an error.
				slog.Info("onboarding token already claimed", "jid", jid)
			}
		}
		http.Redirect(w, r, "/onboard", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// claimByToken atomically binds userSub to the onboarding row for token in one
// statement, returning the claimed jid only when this caller won the race.
// jidForToken resolves a live token to its JID WITHOUT consuming it. The
// consume is a separate step that runs only after the membership edge is
// durable — see the crash-ordering note on handleTokenLanding.
// Both take the RAW token — what a redemption request presents — and hash it
// internally, mirroring store.GetInvite/ConsumeInvite after I1.
func jidForToken(obdb *sql.DB, token string) (string, bool) {
	var jid string
	// Keyed on the token's ref alone: the token is the secret and its ref is
	// NULLed only by the consume step, so its presence IS "unclaimed".
	// user_sub cannot be the guard here — linkJID sets it while approving,
	// which would make a crash-replay look already-claimed and strand the
	// still-live token.
	err := obdb.QueryRow(
		`SELECT jid FROM onboarding WHERE token_ref = ?`,
		store.TokenRef(token)).Scan(&jid)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		slog.Error("resolve onboarding token", "err", err)
		return "", false
	}
	return jid, true
}

// claimByToken consumes the token. Concurrent landings race here and exactly
// one wins the UPDATE; the loser has already written the identical (idempotent)
// edge, so nothing is lost.
func claimByToken(obdb *sql.DB, token, userSub string) (string, bool) {
	var jid string
	// Keyed on the token's ref alone, for the same reason as jidForToken:
	// linkJID has already set user_sub by this point. Atomicity is preserved
	// because exactly one UPDATE can null a given token_ref and RETURN its row.
	err := obdb.QueryRow(
		`UPDATE onboarding
		 SET user_sub = ?, status = 'token_used', token_ref = NULL
		 WHERE token_ref = ?
		 RETURNING jid`,
		userSub, store.TokenRef(token)).Scan(&jid)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		slog.Error("claim onboarding by token", "err", err)
		return "", false
	}
	return jid, true
}

func claimOnboarding(obdb *sql.DB, jid, userSub string) bool {
	res, err := obdb.Exec(
		`UPDATE onboarding
		 SET user_sub = ?, status = 'token_used', token_ref = NULL
		 WHERE jid = ? AND user_sub IS NULL`,
		userSub, jid)
	if err != nil {
		slog.Error("claim onboarding", "jid", jid, "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// csrf is the double-submit token GET /onboard just minted; every form this
// renders MUST echo it, because handleOnboardPost rejects a POST without it.
func handleDashboard(w http.ResponseWriter, r *http.Request, db, obdb *sql.DB, cfg config, userSub, csrf string) {
	if c, err := r.Cookie("onboard_jid"); err == nil && c.Value != "" {
		claimed := claimOnboarding(obdb, c.Value, userSub)
		// Single-use cookie: clear regardless of claim outcome.
		http.SetCookie(w, &http.Cookie{
			Name: "onboard_jid", Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: cfg.secureCookie, SameSite: http.SameSiteLaxMode,
		})
		if claimed {
			if err := linkJID(db, obdb, c.Value, userSub); err != nil {
				writeLinkErr(w, err)
				return
			}
		}
	}

	var username string
	if err := db.QueryRow(`SELECT username FROM user_profiles WHERE sub = ?`, userSub).Scan(&username); err != nil {
		renderPage(w, "Error", template.HTML("<p>User not found.</p>"))
		return
	}

	var qGate, qAt string
	if obdb.QueryRow(
		`SELECT COALESCE(gate,''), COALESCE(queued_at,'') FROM onboarding WHERE user_sub = ? AND status = 'queued' LIMIT 1`,
		userSub).Scan(&qGate, &qAt) == nil {
		renderQueuePosition(w, obdb, qGate, qAt)
		return
	}

	groupCount := len(userFolders(db, userSub))

	if groupCount == 0 {
		if c, err := r.Cookie("pending_target"); err == nil && c.Value != "" {
			renderUsernamePicker(w, username, csrf)
			return
		}
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to join. Ask an admin for one.</p>`))
		return
	}

	// Spec 5/18 step 6 — choose. A paired JID with no route is silent, so ask
	// where it goes before showing anything else. Keyed on that state rather
	// than on the single-use onboard_jid cookie the claim above just cleared:
	// a reload of this page must still reach the choice, not drop the JID.
	if jid := unroutedJID(db, userSub); jid != "" {
		switch folders := adminFolders(db, userSub); len(folders) {
		case 0:
			renderNoWorld(w, jid)
			return
		case 1:
			// One world is not a choice.
			if err := insertRoute(db, "room="+core.JidRoom(jid), folders[0],
				userSub, routeViaSoleWorld); err != nil {
				slog.Error("route paired jid", "jid", jid, "target", folders[0], "err", err)
				http.Error(w, "could not route "+jid+"; try again or contact the operator",
					http.StatusInternalServerError)
				return
			}
			slog.Info("route added", "match", "room="+core.JidRoom(jid),
				"target", folders[0], "sub", userSub, "via", routeViaSoleWorld)
		default:
			renderWorldPicker(w, jid, folders, csrf)
			return
		}
	}

	renderDashboard(w, db, userSub, username)
}

// csrfCookieName double-submit token: set on GET /onboard, required on POST.
// Prevents cross-site forms from exploiting the auth proxy cookie.
const csrfCookieName = "onbod_csrf"

func ensureCSRFToken(w http.ResponseWriter, r *http.Request, cfg config) string {
	return auth.EnsureCSRF(w, r, csrfCookieName, cfg.secureCookie)
}

func checkCSRF(r *http.Request) bool {
	return auth.CheckCSRF(r, csrfCookieName)
}

// handleOnboardPost dispatches the dashboard form actions. All three
// (create_world / delete_route / add_route) operate on CROSS-table state
// (user_profiles/groups/acl/routes); none touches an onbod-owned table.
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

	coreCfg := cfg.core
	if coreCfg == nil {
		var err error
		if coreCfg, err = core.LoadConfig(); err != nil {
			slog.Error("create world: load config", "err", err)
			renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
			return
		}
	}
	// FS setup BEFORE the DB tx: a DB failure after FS leaves a stray group
	// dir (re-creatable, harmless), whereas a committed group row with no FS
	// is a broken world. Order so the durable record is last.
	if err := container.SetupGroup(coreCfg, folder, ""); err != nil {
		slog.Error("create world: setup group", "folder", folder, "err", err)
		renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
		return
	}

	// All cross-table writes (user_profiles/groups/acl/routes) share one handle
	// (messages.db in monolith, routd.db in the split) → one tx makes the
	// world atomic. The race guard is INSERT OR IGNORE + RowsAffected on
	// groups, not the TOCTOU check above (that's a fast-path UX hint only).
	now := time.Now().Format(time.RFC3339)
	if err := createWorldTx(db, folder, username, userSub, now); err != nil {
		slog.Error("create world: db tx", "folder", folder, "err", err)
		http.Error(w, "create world failed", http.StatusInternalServerError)
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

	slog.Info("world created", "folder", folder, "parent", parent, "user", userSub)
	// Spec 5/W: post-onboarding lands on /onboard; chat tokens are
	// issued explicitly (no auto-redirect to a slink link).
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

// membershipJIDs lists the platform JIDs bound to userSub (acl_membership).
func membershipJIDs(db *sql.DB, userSub string) []string {
	rows, err := db.Query(`SELECT child FROM acl_membership WHERE parent = ?`, userSub)
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

// createWorldTx writes the username + group + admin grant in one tx. Spec 5/W:
// no automatic chat token at folder creation. The groups INSERT OR IGNORE +
// RowsAffected==0 detects a concurrent creator (TOCTOU) and fails closed
// instead of granting admin on someone else's world.
//
// It writes NO routes. Spec 5/18 step 7: it used to route every JID the sub had
// ever paired at the new folder, which is a blast radius, not an act — the
// caller asked for a world, not for their other chats to move. The redirect
// lands on /onboard, whose step-6 branch routes the ONE unrouted JID at hand
// with an explicit, attributed act.
func createWorldTx(db *sql.DB, folder, username, userSub, now string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE user_profiles SET username = ? WHERE sub = ?`,
		username, userSub); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO groups (folder, added_at, product) VALUES (?, ?, ?)`,
		folder, now, core.DefaultProduct)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("folder %q already exists", folder)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO acl
		(principal, action, scope, effect, params, predicate, granted_at, granted_by)
		VALUES (?, 'admin', ?, 'allow', '', '', datetime('now'), 'onbod')`,
		userSub, folder+"/**"); err != nil {
		return err
	}
	return tx.Commit()
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

// linkJID binds a platform JID to userSub (acl_membership — CROSS) then advances
// the JID's onboarding row (queued or approved per the gates — OWNED). db is the
// acl-membership DB; obdb owns onboarding + onboarding_gates.
// errLinkRefused marks a user-facing refusal (already claimed, no gate match)
// as distinct from an infrastructure failure: the caller answers 403 rather
// than 500, but either way the user is told.
var errLinkRefused = errors.New("link refused")

func linkJID(db, obdb *sql.DB, jid, userSub string) error {
	// Role memberships are excluded: acl_membership carries BOTH pairing edges
	// (jid -> canonical sub) and role membership (jid -> role:operator), and a
	// JID may legitimately hold both. Without the filter QueryRow can return
	// the role row in undefined order and refuse a valid re-pair, naming a role
	// as the "other account" — sloth has exactly this shape today.
	var existingSub string
	if err := db.QueryRow(
		`SELECT parent FROM acl_membership WHERE child = ? AND parent NOT LIKE 'role:%'`, jid,
	).Scan(&existingSub); err == nil && existingSub != userSub {
		slog.Warn("jid already claimed", "jid", jid, "existing", existingSub, "attempted", userSub)
		return fmt.Errorf("%w: %s is already linked to another account", errLinkRefused, jid)
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO acl_membership (child, parent, added_at, added_by)
		 VALUES (?, ?, ?, 'linkJID')`,
		jid, userSub, now); err != nil {
		return fmt.Errorf("write pairing edge for %s: %w", jid, err)
	}

	gates := loadGates(obdb)
	if len(gates) > 0 {
		if g := matchGate(gates, userSub); g != nil {
			k := gateKey(*g)
			if _, err := obdb.Exec(
				`UPDATE onboarding SET status = 'queued', user_sub = ?, gate = ?, queued_at = ? WHERE jid = ?`,
				userSub, k, now, jid); err != nil {
				return fmt.Errorf("queue %s: %w", jid, err)
			}
			audit.Emit(context.Background(), audit.Event{
				Category: audit.CategoryMutation,
				Action:   "onboarding.queue",
				Actor:    "user:" + userSub,
				ActorSub: userSub,
				Surface:  audit.SurfaceREST,
				Resource: "onboarding/" + jid,
				Outcome:  audit.OutcomeOK,
				ParamsSummary: map[string]any{
					"jid":  jid,
					"gate": k,
				},
			})
			slog.Info("queued jid", "jid", jid, "user", userSub, "gate", k)
			return nil
		}
		// Gates configured and none matched: a dead end, not a success. The row
		// stays at token_used and no later pass revisits it, so the caller must
		// tell the user rather than redirect them to an empty dashboard.
		slog.Warn("no matching gate", "jid", jid, "user", userSub)
		return fmt.Errorf("%w: no onboarding gate matches this account", errLinkRefused)
	}

	if _, err := obdb.Exec(`UPDATE onboarding SET status = 'approved', user_sub = ?, admitted_at = ? WHERE jid = ?`,
		userSub, time.Now().UTC().Format(time.RFC3339), jid); err != nil {
		return fmt.Errorf("approve %s: %w", jid, err)
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "onboarding.approve",
		Actor:    "user:" + userSub,
		ActorSub: userSub,
		Surface:  audit.SurfaceREST,
		Resource: "onboarding/" + jid,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"jid":  jid,
			"gate": "none",
		},
	})
	slog.Info("approved jid", "jid", jid, "user", userSub, "gate", "none")
	return nil
}

// adminFolders lists every folder the sub may administer. Spec 5/18 step 6 is
// the caller CHOOSING which of THEIR worlds a JID lands in, so the whole set is
// the answer — returning the first one SQLite happens to hand back picked for
// the user, which is wrong the moment they hold two.
//
// Deliberately NOT `JOIN acl a ON a.scope = g.folder`: acl scopes are PATTERNS,
// so string equality misses every subtree grant — an owner holding `acme/**`
// matched no folder at all, which is why the creator grant had to stay
// bare-scoped and could never reach its own subgroups (BUGS W1). SQL cannot
// glob segments, so the rows are filtered by the single evaluator rather than
// by a second matcher written here: Authorize answers for ONE group, so asking
// for all of them is a loop over groups.
func adminFolders(db *sql.DB, userSub string) []string {
	folders, err := scanFolders(db)
	if err != nil {
		slog.Error("adminFolders groups", "err", err)
		return nil
	}
	// The cursor is drained and closed by scanFolders before the loop below:
	// Authorize issues further queries on this same handle, and holding a read
	// cursor open across them deadlocks a single-connection DB.
	st := store.New(db)
	var out []string
	for _, f := range folders {
		if auth.Authorize(st, auth.Caller{Principal: userSub}, "admin", f, nil) {
			out = append(out, f)
		}
	}
	return out
}

// unroutedJID returns the sub's first paired JID that no route names, or "".
// That state IS the silence spec 5/18 opens with: the chat is proven and
// linked, its messages land in routd.db, and nothing surfaces them. The match
// predicate mirrors renderDashboard's routing table so the page that asks and
// the page that reports agree on what "routed" means.
func unroutedJID(db *sql.DB, sub string) string {
	for _, jid := range membershipJIDs(db, sub) {
		room := core.JidRoom(jid)
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM routes WHERE match = ? OR match LIKE ?`,
			"room="+room, "room="+room+" %").Scan(&n); err != nil {
			slog.Error("unroutedJID route lookup", "jid", jid, "err", err)
			return ""
		}
		if n == 0 {
			return jid
		}
	}
	return ""
}

func scanFolders(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT folder FROM groups ORDER BY folder`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// writeLinkErr surfaces a linkJID failure to the person who clicked the link.
// Silently redirecting to an empty dashboard is what made the no-gate dead end
// invisible: the row stalls, nothing revisits it, and the user is never told.
func writeLinkErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errLinkRefused) {
		slog.Warn("onboarding link refused", "err", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	slog.Error("onboarding link failed", "err", err)
	http.Error(w, "could not complete account link; try again or contact the operator",
		http.StatusInternalServerError)
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
	// Day-range on admitted_at (not LIKE prefix) so timezone drift doesn't miscount admissions.
	now := time.Now().UTC().Format(time.RFC3339)
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
			   AND admitted_at >= ? AND admitted_at < ?`,
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
		updateErr := false
		for _, jid := range batch {
			if _, err := tx.Exec(
				`UPDATE onboarding SET status = 'approved', admitted_at = ? WHERE jid = ?`,
				now, jid); err != nil {
				slog.Error("admitFromQueue update", "jid", jid, "err", err)
				updateErr = true
				break
			}
		}
		if updateErr {
			// Don't commit a partial batch — roll back the whole gate's claim.
			tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			slog.Error("admitFromQueue commit", "gate", k, "err", err)
			continue
		}
		for _, jid := range batch {
			audit.Emit(context.Background(), audit.Event{
				Category: audit.CategoryMutation,
				Action:   "onboarding.approve",
				Actor:    "system",
				Surface:  audit.SurfaceCron,
				Resource: "onboarding/" + jid,
				Outcome:  audit.OutcomeOK,
				ParamsSummary: map[string]any{
					"jid":  jid,
					"gate": k,
				},
			})
			slog.Info("admitted from queue", "jid", jid, "gate", k)
		}
	}
}

func handleInvite(w http.ResponseWriter, r *http.Request, db, obdb *sql.DB, cfg config) {
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

	// invites is onbod-OWNED → read/consume through obdb. The redemption's acl
	// admin row is routd-OWNED: in the monolith (db == obdb) ConsumeInvite writes
	// invites + acl in one atomic tx on the shared messages.db. In the split
	// (db != obdb) ConsumeInviteNoGrant touches only onbod.db's invites; the acl
	// grant is written separately to routd.db (db here = xdb) via the AUDITED
	// AddACLRow. audit.Init pointing at obdb does not force the audit-free twin
	// here: AddACLRow emits with audit.EmitInTx, which writes the tx's own DB —
	// routd.db, which has had audit_log since routd migration 0016 — so the grant
	// and its audit row commit together.
	st := store.New(obdb)
	split := db != obdb

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

	consume := st.ConsumeInvite
	if split {
		consume = st.ConsumeInviteNoGrant
	}
	consumed, err := consume(token, userSub)
	if err != nil {
		slog.Warn("invite invalid", "reason", "exhausted",
			"token_hash", chanlib.ShortHash(token), "user", userSub, "err", err)
		renderPage(w, "Invite Used",
			template.HTML("<p>This invite link has already been used the maximum number of times.</p>"))
		return
	}

	target := consumed.TargetGlob

	// Split: write the redemption's acl grant to routd.db (db = xdb). Mirrors
	// ConsumeInvite's in-tx insert — non-subgroup invites (no trailing slash)
	// grant admin on the target folder; subgroup invites defer to create_world.
	if split && !strings.HasSuffix(target, "/") {
		if perr := store.New(db).AddACLRow(core.ACLRow{
			Principal: userSub, Action: "admin", Scope: target,
			Effect: "allow", GrantedBy: "invite",
		}); perr != nil {
			// The grant (routd.db) failed AFTER the consume (onbod.db) — separate
			// DBs, no shared tx. Roll back the consume so the invite isn't burned
			// without a grant (silent permanent lockout), and surface the failure
			// instead of redirecting as success. AddACLRow rolls its own tx back on
			// an audit-insert failure, so a failed grant leaves no acl row behind.
			slog.Error("invite acl grant failed; rolling back consume",
				"user", userSub, "scope", target, "err", perr)
			if rerr := st.RestoreInvite(token); rerr != nil {
				slog.Error("invite rollback FAILED — invite burned without grant",
					"token_hash", chanlib.ShortHash(token), "err", rerr)
			}
			renderPage(w, "Setup Failed", template.HTML(
				"<p>We couldn't finish setting up your access. Please try the invite link again.</p>"))
			return
		}
	}

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

	// Redemption grants authority over the target and stops there. Spec 5/18
	// step 7: it used to route EVERY JID the sub had ever paired at the target,
	// which silently moved a returning user's already-routed chats into the
	// world they had just been invited to — the seq-0 rows it wrote outrank any
	// higher-seq route those chats already had. It also discarded the Exec
	// error. /onboard's step-6 branch routes the one unrouted JID instead.
	//
	// Spec 5/W: no slink redirect — the operator (or agent) issues a
	// chat link on demand. Land on /onboard after invite redemption.
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

func renderPage(w http.ResponseWriter, title string, body template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, theme.Page(title, body))
}

func renderUsernamePicker(w http.ResponseWriter, currentUsername, csrf string) {
	body := fmt.Sprintf(`<p class="dim" style="margin-bottom:.8em">Pick a username for your workspace. Lowercase letters, numbers, and hyphens only.</p>
<form method="POST" action="/onboard">
<input type="hidden" name="action" value="create_world">
<input type="hidden" name="%s" value="%s">
<input name="username" placeholder="username" value="%s" required autofocus
 pattern="[a-z][a-z0-9-]{2,29}" title="3-30 chars, lowercase, starts with letter"
 style="margin-bottom:1rem">
<button type="submit" style="width:100%%">Create workspace</button>
</form>`, auth.CSRFField, html.EscapeString(csrf), html.EscapeString(currentUsername))
	renderPage(w, "Create Workspace", template.HTML(body))
}

// renderWorldPicker is spec 5/18 step 6: the caller picks which of THEIR worlds
// the proven JID lands in. It posts to handleAddRoute, which re-checks both
// halves (authority over target, ownership of match) — the radio list is a
// convenience, never the authorization.
//
// The csrf field is not optional: handleOnboardPost 403s any POST whose form
// value does not match the onbod_csrf cookie, which is how the create_world
// form shipped broken (BUGS F1).
func renderWorldPicker(w http.ResponseWriter, jid string, folders []string, csrf string) {
	esc := html.EscapeString
	var opts strings.Builder
	for i, f := range folders {
		checked := ""
		if i == 0 {
			checked = " checked"
		}
		opts.WriteString(fmt.Sprintf(
			`<label style="display:block;padding:.3rem 0">`+
				`<input type="radio" name="target" value="%s" style="width:auto;margin-right:.5rem"%s>%s</label>`,
			esc(f), checked, esc(f)))
	}
	body := fmt.Sprintf(`<p class="dim" style="margin-bottom:.8em">Pick the world that should handle <span class="id">%s</span>. Messages from that chat go to the world you choose; earlier messages stay where they are.</p>
<form method="POST" action="/onboard">
<input type="hidden" name="action" value="add_route">
<input type="hidden" name="%s" value="%s">
<input type="hidden" name="match" value="room=%s">
%s
<button type="submit" style="width:100%%;margin-top:1rem">Route this chat</button>
</form>`, esc(jid), auth.CSRFField, esc(csrf), esc(core.JidRoom(jid)), opts.String())
	renderPage(w, "Choose a world", template.HTML(body))
}

// renderNoWorld is the terminal page for an empty choice set. Rendering a
// picker with no options, or silently showing the dashboard, both leave the
// chat unrouted with no explanation — the user must be told why.
func renderNoWorld(w http.ResponseWriter, jid string) {
	renderPage(w, "Nowhere to route", template.HTML(fmt.Sprintf(
		`<p><span class="id">%s</span> is linked to your account, but you don't administer any world it could go to, so it stays silent.</p>`+
			`<p class="dim">Ask an admin for an invite to a world, then open this page again.</p>`,
		html.EscapeString(jid))))
}

func renderDashboard(w http.ResponseWriter, db *sql.DB, userSub, username string) {
	esc := html.EscapeString

	var jidsHTML string
	if rows, err := db.Query(
		`SELECT child, added_at FROM acl_membership WHERE parent = ? ORDER BY added_at`, userSub,
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

	var groupsHTML strings.Builder
	for _, folder := range userFolders(db, userSub) {
		groupsHTML.WriteString(fmt.Sprintf(
			`<tr><td><span class="dot dot-ok"></span> %s</td></tr>`, esc(folder)))
	}

	var routesHTML string
	if rows, err := db.Query(`
		SELECT am.child, r.target FROM acl_membership am
		JOIN routes r ON r.match = 'room=' || SUBSTR(am.child, INSTR(am.child, ':')+1)
		   OR r.match LIKE 'room=' || SUBSTR(am.child, INSTR(am.child, ':')+1) || ' %'
		WHERE am.parent = ?
		ORDER BY am.child, r.target`, userSub); err == nil {
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
		jidsHTML, groupsHTML.String(), routesHTML)
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func sendReply(cfg config, jid, text string) {
	payload := map[string]string{"jid": jid, "text": text}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", cfg.gatedURL+"/v1/outbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Present the service:onbod JWT (messages:write) on routd's JWT-gated
	// /v1/outbound. A nil source or token blip drops the reply rather than sending
	// a credential routd rejects. svcToken is always set in production (loadConfig).
	if cfg.svcToken == nil {
		slog.Error("onbod has no service token; dropping reply", "jid", jid)
		return
	}
	tok, err := cfg.svcToken(req.Context())
	if err != nil {
		slog.Error("onbod service-token exchange failed; dropping reply", "jid", jid, "err", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	obs.InjectRequest(req.Context(), req)
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

// userFolders returns the distinct allow-scopes the sub has access to via the
// unified acl tables (post-0053 cutover). Operator (`role:operator` membership)
// returns the `**` scope through the role's row.
func userFolders(db *sql.DB, sub string) []string {
	if sub == "" {
		return nil
	}
	principals := []string{sub}
	// Walk acl_membership transitively from sub upward.
	queue := []string{sub}
	seen := map[string]bool{sub: true}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		rows, err := db.Query(`SELECT parent FROM acl_membership WHERE child = ?`, next)
		if err != nil {
			slog.Error("userFolders membership walk", "child", next, "err", err)
			return nil
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				slog.Error("userFolders membership scan", "child", next, "err", err)
				return nil
			}
			if p != "" && !seen[p] {
				seen[p] = true
				principals = append(principals, p)
				queue = append(queue, p)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			slog.Error("userFolders membership rows", "child", next, "err", err)
			return nil
		}
		rows.Close()
	}
	placeholders := strings.Repeat("?,", len(principals))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(principals))
	for _, p := range principals {
		args = append(args, p)
	}
	rows, err := db.Query(
		`SELECT DISTINCT scope FROM acl
		 WHERE effect='allow' AND principal IN (`+placeholders+`)
		 ORDER BY scope`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var folders []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			slog.Error("userFolders scope scan", "err", err)
			return nil
		}
		if f != "" {
			folders = append(folders, f)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("userFolders scope rows", "err", err)
		return nil
	}
	return folders
}

func isOperator(folders []string) bool {
	return slices.Contains(folders, "**")
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

// userOwnsMatch reports whether match references a room the user has claimed via acl_membership.
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
		`SELECT child FROM acl_membership WHERE parent = ?`, sub)
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

// Route provenance, stamped into routes.added_via beside added_by's WHO. Spec
// 5/18 step 7: the two onboarding acts are not the same act, and an operator
// asking "why does this chat go here?" gets a materially different answer from
// each — one is a choice the caller made, the other is a route onbod wrote
// without asking.
const (
	routeViaPicker    = "picker"     // the caller chose the world (step 6's form)
	routeViaSoleWorld = "sole_world" // one administrable world, so no choice to offer
)

// insertRoute is onbod's ONLY routes writer, so no onbod path can produce an
// unattributed row. Authorization belongs to the caller (handleAddRoute's
// MatchGroups + userOwnsMatch; the sole-world branch's adminFolders +
// unroutedJID) — this records who acted and how, and nothing else.
//
// Plain INSERT, not INSERT OR IGNORE: `routes` carries no UNIQUE constraint, so
// OR IGNORE ignored nothing and only swallowed genuine write errors.
func insertRoute(db *sql.DB, match, target, sub, via string) error {
	_, err := db.Exec(
		`INSERT INTO routes (seq, match, target, added_by, added_via) VALUES (0, ?, ?, ?, ?)`,
		match, target, sub, via)
	return err
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

	if err := insertRoute(db, match, target, sub, routeViaPicker); err != nil {
		slog.Error("add route", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("route added", "match", match, "target", target, "sub", sub,
		"via", routeViaPicker)
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}
