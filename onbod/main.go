package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
)

type gate struct {
	kind        string // "github", "google", "email", "*"
	param       string // "org=mycompany", "domain=company.com", etc.
	limitPerDay int
}

type config struct {
	core         *core.Config
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

	// Two handles (spec 5/16-mcp-rest-unification): obdb is onbod's OWNED tables
	// (onboarding/invites/onboarding_gates); xdb is the CROSS tables
	// (acl/acl_membership/groups/user_profiles/routes), all routd-OWNED. Split is the
	// only topology — onbod owns its tables and cross-reads routd's, and opens NO
	// messages.db. audit.Init targets obdb so the invite/gate writers' in-tx audit
	// rows land with the mutation. A missing routd store is a misconfigured split,
	// so a failure to open is fatal — no silent empty cross-read.
	obdb, err := openOwned()
	if err != nil {
		slog.Error("open onbod's store", "err", err)
		os.Exit(1)
	}
	defer obdb.Close() //nolint:errcheck // the process is ending
	slog.Info("onbod owns its tables", "store", dynstore.URL())

	// OpenOwner probes for the `acl` entity, so an owner that has never booted
	// is loud instead of a cross-read against zero groups and zero acl with the
	// boot still green (BUGS F52).
	xdb, err := store.OpenOwner(store.OwnerRoutd)
	if err != nil {
		slog.Error("open routd's store", "err", err)
		os.Exit(1)
	}
	defer xdb.Close() //nolint:errcheck // the process is ending
	slog.Info("onbod cross-reads routd's tables", "store", dynstore.URL())

	audit.Init(obdb.Obj(), os.Getenv("ARIZUKO_INSTANCE"))
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
	// Register the signal handler BEFORE the listener accepts: a SIGTERM in
	// between takes Go's default disposition and kills the process, so the
	// Shutdown below never runs (BUGS L127).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("onbod listening", "addr", cfg.listenAddr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
		}
	}()

	tick := time.NewTicker(cfg.pollInterval)
	defer tick.Stop()

	promptUnprompted(xdb, obdb, cfg)
	observePairings(xdb, obdb, cfg)
	admitFromQueue(obdb)
	var admitCount int
	for {
		select {
		case <-tick.C:
			promptUnprompted(xdb, obdb, cfg)
			observePairings(xdb, obdb, cfg)
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

// newOnbodMux builds onbod's HTTP surface. Extracted from main so a test can
// read the ROUTING TABLE onbod actually builds rather than a restatement of it —
// the doc-vs-mux guard has to compare the emitted document against the real
// mux, and a mux built inline in main is unreachable.
func newOnbodMux(xdb, obdb *store.Store, cfg config, ks *auth.KeySet) *http.ServeMux {
	mux := http.NewServeMux()
	// stripUnsigned keeps X-User-Sub only when the request PROVES it transited
	// proxyd. The bearer is a transit proof ONLY: /onboard reads X-User-Sub as the
	// OAuth'd end-user, so we must NOT stamp the bearer's own service:proxyd subject
	// over it. An unproven X-User-Sub is stripped. No verifier (local dev) → pass
	// through.
	//
	// What is left after the strip is a caller proxyd could not name, and every
	// handler below answers 401 to one. proxyd owns the login bounce and the
	// auth_return cookie authd resumes from (spec 5/31-identity-pairing); a second bounce here
	// sends an already-signed-in caller back to a login that returns them just
	// as anonymous, which is the loop that made onboarding unreachable on every
	// instance (BUGS L99). Refusing keeps the failure where its cause is.
	stripUnsigned := stripUnsignedGuard(ks)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("onbod", mux))
	mux.HandleFunc("GET /onboard", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleOnboard(w, r, xdb, obdb, cfg)
	}))
	mux.HandleFunc("POST /onboard", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleOnboardPost(w, r, xdb, obdb, cfg)
	}))
	mux.HandleFunc("GET /invite/{token}", stripUnsigned(func(w http.ResponseWriter, r *http.Request) {
		handleInvite(w, r, xdb, obdb)
	}))

	// Bearer-gated admin surface (spec 5/16-mcp-rest-unification § One owner + federation). onbod OWNS
	// invites + onboarding_gates; the writers (dashd, CLI, routd's /invite +
	// /gate) reach them here instead of writing the DB directly in the split.
	// Verified against authd's JWKS (ks); nil ks (AUTHD_URL unset / monolith) =
	// open, like routd's nil-verifier local-dev path.
	adm := &admin{st: obdb, ks: ks}
	adm.mountOnboarding(mux) // /v1/onboarding via the shared resreg handler (spec 5/16)
	adm.mountInvites(mux)    // /v1/invites via the shared resreg handler (spec 5/16)
	adm.mountGates(mux)      // /v1/gates via the shared resreg handler (spec 5/16)
	adm.mountAudit(mux)      // /v1/audit — onbod is the fourth audit_log owner (spec 5/I-tool-call-logging)

	// Operator dashboard (spec 5/16): the proxyd transit proof (stripUnsigned)
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

// stripUnsignedGuard is the lenient identity gate for onbod's public surface: it
// keeps an inbound X-User-Sub only when the request proves it transited proxyd,
// otherwise it strips the identity headers so a request reaching onbod directly
// cannot forge a user. The proxyd-stamped end-user X-User-Sub flows through
// untouched. A nil keyset (local dev) passes through.
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

// knownStatuses is the complete set the pipeline can advance. A row in anything
// else is stranded: no query selects it, and its jid PRIMARY KEY blocks a fresh
// insert, so the user can never onboard from that chat again. Two such rows exist
// in production (BUGS O1).
//
// `token_used` is gone with the token it named (spec 5/31-identity-pairing): redemption moved to
// webd's /pair/{token}. `refused` replaces its silent dead-end role, loudly.
var knownStatuses = map[string]bool{
	"awaiting_message": true,
	"queued":           true,
	"approved":         true,
	"refused":          true,
}

// knownStatusList renders knownStatuses for the operator who has to repair a
// stranded row. It is DERIVED, because the hand-written copy it replaces named
// `token_used` — gone with the token (spec 5/31-identity-pairing) — and omitted `refused`, so
// the error told the operator to write a status the pipeline cannot advance.
func knownStatusList() string {
	return strings.Join(slices.Sorted(maps.Keys(knownStatuses)), "/")
}

// warnedStranded remembers which (jid, status) pairs this process has already
// reported, so a stranded row is ERROR once and not once per poll. The scan
// runs from promptUnprompted on a 10s poll: unbounded repetition buried real
// errors on two instances and made a permanent condition look like a live
// incident (BUGS L61). A row whose status CHANGES warns again — that is a new
// fact; the same row in the same state is not.
var warnedStranded sync.Map

// warnStrandedRows surfaces rows the state machine cannot move. It cannot
// repair them — picking a status for a row of unknown provenance is the
// operator's call — but silence is what let them sit unnoticed.
func warnStrandedRows(db *store.Store) {
	rows, err := db.ListOnboarding("")
	if err != nil {
		slog.Error("stranded-row scan", "err", err)
		return
	}
	for _, r := range rows {
		if knownStatuses[r.Status] {
			continue
		}
		if _, seen := warnedStranded.LoadOrStore(r.JID+"\x00"+r.Status, true); seen {
			continue
		}
		slog.Error("onboarding row stranded in an unknown status — it will never advance",
			"jid", r.JID, "status", r.Status, "known", knownStatusList())
	}
}

// promptUnprompted greets every unrouted chat that has no live link, with a
// PAIRING link (spec 5/31-identity-pairing) rather than an onboarding token of its own. onbod mints
// no second credential and writes no second acl_membership edge.
//
// The mint's owner_folder is the constant "" → NULL. InsertOnboarding only ever
// fires from the route-MISS branch, so "this JID routes nowhere" is proved by the
// row's existence and is not re-derived here.
func promptUnprompted(xdb, obdb *store.Store, cfg config) {
	warnStrandedRows(obdb)
	pending, err := obdb.UnpromptedJIDs()
	if err != nil {
		slog.Error("promptUnprompted query", "err", err)
		return
	}

	base := strings.TrimRight(cfg.authBaseURL, "/")
	for _, jid := range pending {
		// Mint BEFORE the claim. The two writes are in different databases with
		// no shared transaction, and this order is the one that cannot strand a
		// user: a mint failure leaves the row unclaimed for the next tick, while
		// a lost claim race leaves a token nobody was ever told, which expires in
		// PairingTTL. Claiming first would stamp prompted_at with no link sent.
		token, err := xdb.IssuePairingLink(context.Background(), jid, "")
		if err != nil {
			slog.Error("promptUnprompted mint pairing link", "jid", jid, "err", err)
			continue
		}
		// Claim-per-row: the write re-checks that prompted_at is absent, so two
		// overlapping ticks can't both win the same jid. Only the tick whose
		// claim was taken sends the link (else the other tick already did).
		claimed, err := obdb.ClaimOnboardingPrompt(jid)
		if err != nil {
			slog.Error("promptUnprompted claim", "jid", jid, "err", err)
			continue
		}
		if !claimed {
			continue
		}

		prompt := "Set up your account: " + base + "/pair/" + token
		if cfg.greeting != "" {
			prompt = cfg.greeting + "\n" + prompt
		}
		// The claim stays taken even when the send fails, and the row is NOT retried.
		// Releasing it looks kinder and is worse: a permanently unroutable jid (BUGS
		// L66) would mint a fresh pairing token on every tick, forever. A 502 for a
		// jid that matches no route is a configuration fault, not a blip.
		//
		// What must NOT happen is the old behaviour: sendReply swallowed the outcome
		// and this line logged "sent pairing link" directly under "send reply non-2xx"
		// (BUGS L67).
		if err := sendReply(cfg, jid, prompt); err != nil {
			slog.Error("pairing link NOT delivered; the jid stays claimed and will not be retried",
				"jid", jid, "err", err)
			continue
		}
		slog.Info("sent pairing link", "jid", jid)
	}
}

// observePairings is the admission trigger after the fold (spec 5/31-identity-pairing).
// RedeemPairing writes the edge in webd and knows nothing about gates; this tick
// discovers the edge and runs the admission half over data that write committed.
//
// A poll, not a hook: webd has no relationship to onbod, and a hook would put
// routd's identity-write path one round trip from onbod's uptime, while onbod
// already holds a direct handle on routd.db.
//
// The dedup is onboarding's jid primary key plus the `user_sub IS NULL` guard, so
// a row is admitted once and an agent-minted pairing is invisible here.
func observePairings(xdb, obdb *store.Store, cfg config) {
	awaiting, err := obdb.UnadmittedJIDs()
	if err != nil {
		slog.Error("observePairings query", "err", err)
		return
	}

	for _, jid := range awaiting {
		// The PAIRING edge and not any edge above the jid: a role membership is
		// not a consent, and reading one as the parent would admit the chat to
		// whoever the role names.
		edges, err := xdb.ParentEdges(jid)
		if err != nil {
			slog.Error("observePairings read pairing edge", "jid", jid, "err", err)
			continue
		}
		for _, e := range edges {
			if e.AddedBy != store.PairingAddedBy {
				continue
			}
			admitJID(obdb, cfg, jid, e.Parent)
			break
		}
	}
}

// admitJID advances a paired JID's onboarding row: queued (a gate matched),
// approved (no gates configured), or refused (gates configured, none matched). It
// writes NO membership edge — RedeemPairing already did, in routd's own
// transaction. That split is the point of the fold: a pairing can no longer fail
// for an admission reason at the browser, and one writer owns the edge.
//
// The refusal used to be a 403 the user saw. It is now discovered on a tick, after
// that page is gone, so it travels to the chat instead.
func admitJID(obdb *store.Store, cfg config, jid, userSub string) {
	gates := loadGates(obdb)

	if len(gates) > 0 {
		g := matchGate(gates, userSub)
		if g == nil {
			// Terminal. user_sub is stamped so the row stops matching and the
			// refusal is sent exactly once.
			if err := obdb.RefuseOnboarding(jid, userSub); err != nil {
				slog.Error("refuse jid", "jid", jid, "err", err)
				return
			}
			auditAdmission(jid, userSub, "onboarding.refuse", "none")
			slog.Warn("refused jid: no matching gate", "jid", jid, "user", userSub)
			if err := sendReply(cfg, jid, "This account can't be set up here: no onboarding gate matches it. Ask the operator for an invite."); err != nil {
				slog.Error("refusal notice not delivered; the user was refused in silence",
					"jid", jid, "err", err)
			}
			return
		}
		k := gateKey(*g)
		if err := obdb.QueueOnboarding(jid, userSub, k); err != nil {
			slog.Error("queue jid", "jid", jid, "err", err)
			return
		}
		auditAdmission(jid, userSub, "onboarding.queue", k)
		slog.Info("queued jid", "jid", jid, "user", userSub, "gate", k)
		if err := sendReply(cfg, jid, "You're in the queue. Open "+
			strings.TrimRight(cfg.authBaseURL, "/")+"/onboard to see your position."); err != nil {
			slog.Error("queue notice not delivered; the user does not know they are queued",
				"jid", jid, "err", err)
		}
		return
	}

	if err := obdb.AdmitOnboarding(jid, userSub); err != nil {
		slog.Error("approve jid", "jid", jid, "err", err)
		return
	}
	auditAdmission(jid, userSub, "onboarding.approve", "none")
	slog.Info("approved jid", "jid", jid, "user", userSub, "gate", "none")
}

func auditAdmission(jid, userSub, action, gateKey string) {
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   action,
		Actor:    "user:" + userSub,
		ActorSub: userSub,
		Surface:  audit.SurfaceCron,
		Resource: "onboarding/" + jid,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"jid":  jid,
			"gate": gateKey,
		},
	})
}

// handleOnboard and the flow below take two handles: db is the CROSS-table DB and
// obdb is onbod's OWNED-table DB. Owned-table queries route through obdb;
// cross-table queries stay on db.
// /onboard carries no token of its own since the fold (spec 5/31-identity-pairing): the greeting
// link redeems at webd's /pair/{token}, which writes the edge and sends the user
// here. This page reads the edge and asks where the chat should go.
func handleOnboard(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, cfg config) {
	userSub := r.Header.Get("X-User-Sub")
	if userSub == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	handleDashboard(w, r, db, obdb, userSub, ensureCSRFToken(w, r, cfg))
}

// csrf is the double-submit token GET /onboard just minted; every form this
// renders MUST echo it, because handleOnboardPost rejects a POST without it.
func handleDashboard(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, userSub, csrf string) {
	username, known, err := db.Username(userSub)
	if err != nil {
		// A read failure is not "no such user": it is reported, and the page still
		// stops, because every branch below is named after this person.
		slog.Error("username lookup", "user", userSub, "err", err)
	}
	if !known || err != nil {
		renderPage(w, "Error", template.HTML("<p>User not found.</p>"))
		return
	}

	// A read failure here is not "not queued": it is reported and the page falls
	// through to the same branches an unqueued caller takes.
	queued, isQueued, err := obdb.QueuedAdmission(userSub)
	if err != nil {
		slog.Error("queued-admission lookup", "user", userSub, "err", err)
	}
	if isQueued {
		renderQueuePosition(w, obdb, queued.Gate, queued.QueuedAt)
		return
	}

	groupCount := len(userFolders(db, userSub))

	if groupCount == 0 {
		// Two authorities, one picker: a recorded subgroup-invite redemption, or
		// (spec 5/18 step 8) an admission the gates approved. handleCreateWorld
		// re-checks the same pair with the same two functions, so this branch is
		// presentation only — the write is the control.
		_, invited := latestRedemption(obdb, userSub)
		if invited || mayCreateFirstWorld(obdb, userSub) {
			renderUsernamePicker(w, username, csrf)
			return
		}
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to join. Ask an admin for one.</p>`))
		return
	}

	// Spec 5/18 step 6 — choose. A paired JID with no route is silent, so ask
	// where it goes before showing anything else. Keyed on that STATE, not on
	// how the user arrived: it is the same branch whether webd's pair success
	// page sent them here, an invite redemption did, or they reloaded. That is
	// also why the fold needed no hook — membershipJIDs applies no added_by
	// filter, so the edge RedeemPairing wrote is found like any other.
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
// (create_world / delete_route / add_route) write CROSS-table state
// (user_profiles/groups/acl/routes) through db; create_world additionally READS
// onbod's owned onboarding/onboarding_gates through obdb for its second
// authority (spec 5/18 step 8).
func handleOnboardPost(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, cfg config) {
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
		handleCreateWorld(w, r, db, obdb, cfg, userSub)
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

// mayCreateFirstWorld is spec 5/18 step 8: an admitted stranger's second authority
// to create a world. It is the WRITE precondition in handleCreateWorld;
// handleDashboard calls it to decide whether to render the picker, because a form
// that is merely hidden is not a control.
//
// The gate condition keeps the default posture closed. With no enabled gate,
// admitJID approves EVERY paired identity, so `status='approved'` alone would turn
// any instance that ever ran ONBOARDING_PLATFORMS into open signup. It reads the
// gates through loadGates so the same enabled=1 rows that make admitJID selective
// are the ones that make its verdict grant something.
//
// Every failure fails closed: an unreadable DB grants nothing.
func mayCreateFirstWorld(obdb *store.Store, userSub string) bool {
	if userSub == "" || len(loadGates(obdb)) == 0 {
		return false
	}
	n, err := obdb.ApprovedAdmissions(userSub)
	if err != nil {
		slog.Error("approved-admission lookup", "user", userSub, "err", err)
		return false
	}
	return n > 0
}

// latestRedemption returns the item store.consumeInvite wrote when this caller
// redeemed a subgroup invite. It is the ONLY thing that can name a parent
// folder: the request names none, so a caller can no longer ask for a world
// inside a subtree nobody invited them into (BUGS F50).
//
// It answers the caller's MOST RECENT unspent one, because a caller holding two
// is answering the invite they just followed; each is spent on its own, so two
// invites still yield two worlds.
//
// Fails closed like mayCreateFirstWorld: an unreadable partition is no
// authority.
func latestRedemption(obdb *store.Store, userSub string) (store.Redemption, bool) {
	r, held, err := obdb.LatestRedemption(context.Background(), userSub)
	if err != nil {
		slog.Error("invite redemption lookup", "user", userSub, "err", err)
		return store.Redemption{}, false
	}
	return r, held
}

// spendRedemption consumes the redemption, making it single-use. The delete's
// own refusal is the claim, not a report: two create_world posts that derived
// the same redemption cannot both spend it, so one invite redemption is one
// world.
func spendRedemption(obdb *store.Store, userSub string, r store.Redemption) (bool, error) {
	return obdb.SpendRedemption(context.Background(), userSub, r)
}

func handleCreateWorld(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, cfg config, userSub string) {
	// The parent folder is DERIVED from what the redemption recorded, never from
	// the request. Until F50 it came from the `pending_target` cookie and was used
	// verbatim, so `Cookie: pending_target=victim/` created victim/<name> and took
	// admin over it for any authenticated caller holding no grant anywhere.
	redemption, invited := latestRedemption(obdb, userSub)
	if !invited && !mayCreateFirstWorld(obdb, userSub) {
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to create a workspace.</p>`))
		return
	}

	// An approved admission authorizes a TOP-LEVEL folder and nothing else, and
	// that stays structural rather than checked: only a redemption sets parent,
	// and its value is the operator-chosen target_glob. The two authorities
	// cannot be combined into a deeper one.
	var parent string
	if invited {
		parent = strings.TrimSuffix(redemption.TargetGlob, "/")
	}

	// The parent must still exist. A folder delete sweeps routd's tables and
	// cannot reach onbod's, so `invites` and `invite_redemptions` outlive the
	// folder they name (BUGS L244) — and redeeming one then admits a person into
	// a subtree whose owner is gone. The redemption is the authority; the parent
	// is what it is authority OVER, so a missing parent is a dead authority.
	if parent != "" {
		if _, ok := db.GroupByFolder(parent); !ok {
			slog.Warn("invite names a folder that no longer exists; refusing",
				"parent", parent, "user_sub", userSub)
			renderPage(w, "Invitation Expired",
				template.HTML("<p>The folder this invitation was for no longer exists.</p>"+
					"<p>Ask whoever invited you for a new invitation.</p>"))
			return
		}
	}

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

	if _, taken := db.GroupByFolder(folder); taken {
		renderPage(w, "Username Taken",
			template.HTML("<p>That username is already in use.</p>"+
				`<p><a href="/onboard">Try again</a></p>`))
		return
	}

	// Spend the authority before acting on it. The DELETE is the claim, so a
	// replay of the same redemption — a resubmitted form, a second tab — finds
	// nothing to derive a parent from and creates no second world.
	if invited {
		spent, err := spendRedemption(obdb, userSub, redemption)
		if err != nil {
			slog.Error("create world: spend invite redemption", "user", userSub, "err", err)
			http.Error(w, "create world failed", http.StatusInternalServerError)
			return
		}
		if !spent {
			renderPage(w, "Invite Used",
				template.HTML("<p>That invite has already been used to create a workspace.</p>"))
			return
		}
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
	if err := writeWorld(db, folder, username, userSub); err != nil {
		slog.Error("create world: db tx", "folder", folder, "err", err)
		http.Error(w, "create world failed", http.StatusInternalServerError)
		return
	}

	if err := db.SeedDefaultTasks(folder, folder); err != nil {
		slog.Warn("create world: seed default tasks", "folder", folder, "err", err)
	}

	slog.Info("world created", "folder", folder, "parent", parent, "user", userSub)
	// Spec 5/W: post-onboarding lands on /onboard; chat tokens are
	// issued explicitly (no auto-redirect to a slink link).
	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

// membershipJIDs lists the platform JIDs bound to userSub (acl_membership), in
// jid order — the parent is the second field of the edge coordinate, so the read
// takes the graph and filters, and the order it answers in is the child's.
func membershipJIDs(db *store.Store, userSub string) []string {
	return db.Members(userSub)
}

// writeWorld writes the group + member edge + admin grant in ONE guarded batch,
// then names the person. The groups write is create-if-absent and the only
// guarded one in the batch, so a refusal detects a concurrent creator and fails
// closed instead of granting admin on someone else's world.
//
// THE PERSON IS RECORDED AFTER THE FOLDER IS WON, and that is what the
// transaction was protecting: a caller who loses the race must not be left
// named. Landing it after the batch gives that directly — a lost race writes
// nothing at all — where the transaction gave it by rolling both back.
//
// It is store.RecordPerson and not a rename: this IS the path that admits a
// person, so it is the path that creates their profile. It used to rename a
// profile nothing ever created, and every person admitted stayed nameless on
// every operator surface (BUGS L269). It cannot ride in the batch because the
// batch is all-or-nothing and this one write is create-or-rename.
//
// The batch runs through store.SeedMemberRole, which is objstore.AddEdge, so the
// cycle walk compare-and-swaps routd's marker in the write it decided (BUGS
// L256).
//
// It writes NO routes (spec 5/18 step 7): it used to route every JID the sub had
// ever paired at the new folder, which is a blast radius, not an act. The redirect
// lands on /onboard, whose step-6 branch routes the ONE unrouted JID at hand.
func writeWorld(db *store.Store, folder, username, userSub string) error {
	ctx := context.Background()
	row, err := store.GroupCreateWrite(core.Group{
		Folder: folder, AddedAt: time.Now(), Product: core.DefaultProduct,
	})
	if err != nil {
		return err
	}
	// A groups row implies the member edge: without it the new world expands to
	// nothing and comes up mute (BUGS L119).
	grant, err := store.AdminGrantWrite(userSub, folder, "onbod")
	if err != nil {
		return err
	}
	// A WORLD MAY DO EVERYTHING AND ITS OWNER ANSWERS FOR IT. Two rows, not one
	// per tool: the tool grant says the world can reach any tool inside its own
	// subtree, and the hold beside it says a human sees every one of those calls
	// before it runs. A world granted nothing could not create a cron and nobody
	// could tell why; a world granted everything with no hold would act unwatched.
	//
	// The messaging floor and the always-mounted reads carry skip rows
	// (store.skipApprovalFloor), so the hold falls on what a person would want to
	// see and not on the agent answering a message.
	reach, err := store.ACLGrantWrite(core.ACLRow{
		Principal: "folder:" + folder, Action: "mcp:*", Scope: folder + "/**",
		Effect: store.EffectAllow, GrantedBy: "onbod",
	})
	if err != nil {
		return err
	}
	hold, err := store.ACLGrantWrite(core.ACLRow{
		Principal: "folder:" + folder, Action: store.HoldPrefix + "*", Scope: folder + "/**",
		Effect: store.EffectAllow, GrantedBy: "onbod",
	})
	if err != nil {
		return err
	}
	created, err := store.SeedMemberRole(ctx, db, folder, row, grant, reach, hold)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("folder %q already exists", folder)
	}
	return db.RecordPerson(ctx, userSub, username, username)
}

func loadGates(db *store.Store) []gate {
	rows, err := db.EnabledGates()
	if err != nil {
		slog.Error("loadGates query", "err", err)
		return nil
	}
	var out []gate
	for _, g := range rows {
		out = append(out, gateFromKey(g.Gate, g.LimitPerDay))
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

// adminFolders lists every folder the sub may administer. Spec 5/18 step 6 is the
// caller CHOOSING which of THEIR worlds a JID lands in, so the whole set is the
// answer.
//
// Deliberately NOT `JOIN acl a ON a.scope = g.folder`: acl scopes are PATTERNS, so
// string equality misses every subtree grant — an owner holding `acme/**` matched
// no folder at all (BUGS W1). SQL cannot glob segments, so the rows are filtered
// by the single evaluator rather than by a second matcher written here.
func adminFolders(db *store.Store, userSub string) []string {
	folders, err := scanFolders(db)
	if err != nil {
		slog.Error("adminFolders groups", "err", err)
		return nil
	}
	// The cursor is drained and closed by scanFolders before the loop below:
	// Authorize issues further queries on this same handle, and holding a read
	// cursor open across them deadlocks a single-connection DB.
	var out []string
	for _, f := range folders {
		if auth.Authorize(db, auth.Caller{Principal: userSub}, "admin", f, nil) {
			out = append(out, f)
		}
	}
	return out
}

// jidRoutes returns the rules that name one JID's room. It is the ONE routing
// predicate onbod asks, so the page that reports a chat as routed and the check
// that calls it unrouted cannot disagree.
//
// `match` is an ATTRIBUTE of a route item and half the predicate is a LIKE, so
// this reads the table and filters. THE WILDCARD SEMANTICS ARE KEPT: `_` and `%`
// are legal in a room id and are LIKE wildcards, and sqlite folds ASCII case on
// top, so a room carrying one matches rules that do not name it — narrowing that
// here would silently change which chats count as routed, in the direction that
// leaves a live chat looking unrouted and re-onboards it.
func jidRoutes(routes []core.Route, jid string) []core.Route {
	room := core.JidRoom(jid)
	exact := "room=" + room
	pattern := exact + " %"
	var out []core.Route
	for _, r := range routes {
		if r.Match == exact || likeMatch(r.Match, pattern) {
			out = append(out, r)
		}
	}
	return out
}

// likeMatch is sqlite's LIKE: `%` takes any run of characters, `_` takes exactly
// one, and everything else compares with ASCII case folded. The comparison is
// over RUNES because `_` counts characters, not bytes.
func likeMatch(text, pattern string) bool {
	t, p := []rune(text), []rune(pattern)
	ti, pi, star, mark := 0, 0, -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '_' || foldASCII(p[pi]) == foldASCII(t[ti])):
			ti++
			pi++
		case pi < len(p) && p[pi] == '%':
			star, mark = pi, ti
			pi++
		case star >= 0:
			// Backtrack: the last `%` swallows one more character.
			mark++
			ti, pi = mark, star+1
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

// foldASCII lowercases one ASCII letter and leaves every other rune alone —
// which is exactly how far sqlite's LIKE folds.
func foldASCII(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// unroutedJID returns the sub's first paired JID that no route names, or "".
// That state IS the silence spec 5/18 opens with: the chat is proven and
// linked, its messages land in routd.db, and nothing surfaces them.
func unroutedJID(db *store.Store, sub string) string {
	routes, err := db.RouteTable()
	if err != nil {
		// A table nobody could read is not a table with no rules in it: offering
		// to route a JID that already has one writes a second rule for one chat.
		slog.Error("unroutedJID route lookup", "sub", sub, "err", err)
		return ""
	}
	for _, jid := range membershipJIDs(db, sub) {
		if len(jidRoutes(routes, jid)) == 0 {
			return jid
		}
	}
	return ""
}

func scanFolders(db *store.Store) ([]string, error) {
	return db.GroupFolders(0)
}

func renderQueuePosition(w http.ResponseWriter, db *store.Store, gateStr, queuedAt string) {
	pos, err := db.QueuePosition(gateStr, queuedAt)
	if err != nil {
		slog.Error("queue-position count", "gate", gateStr, "err", err)
	}
	pos++ // 1-indexed

	var etaMsg string
	g, found, err := db.Gate(gateStr)
	if err != nil {
		slog.Error("queue-position gate lookup", "gate", gateStr, "err", err)
	}
	if limit := g.LimitPerDay; found && g.Enabled && limit > 0 {
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

func admitFromQueue(db *store.Store) {
	gates := loadGates(db)
	if len(gates) == 0 {
		return
	}
	// Day-range on admitted_at (not a prefix match) so timezone drift doesn't
	// miscount admissions.
	todayStart := time.Now().Format("2006-01-02") + "T00:00:00Z"
	tomorrowStart := time.Now().Add(24*time.Hour).Format("2006-01-02") + "T00:00:00Z"
	for _, g := range gates {
		k := gateKey(g)
		admitted, err := db.AdmittedInWindow(k, todayStart, tomorrowStart)
		if err != nil {
			slog.Error("admitFromQueue count", "gate", k, "err", err)
			continue
		}
		remaining := g.limitPerDay - admitted
		if remaining <= 0 {
			continue
		}
		batch, err := db.QueuedJIDs(k, remaining)
		if err != nil {
			slog.Error("admitFromQueue query", "gate", k, "err", err)
			continue
		}
		// A partial write is REPORTED and its landed prefix is audited, where the
		// transaction this replaces rolled the whole gate's claim back. The
		// admissions are independent and the next tick re-reads the queue, so
		// what a failure leaves behind is work a re-run finishes.
		n, err := db.AdmitQueued(batch)
		if err != nil {
			slog.Error("admitFromQueue update", "gate", k, "admitted", n, "err", err)
		}
		for _, jid := range batch[:n] {
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

func handleInvite(w http.ResponseWriter, r *http.Request, db, obdb *store.Store) {
	token := r.PathValue("token")
	if token == "" {
		renderPage(w, "Invalid Invite", template.HTML("<p>No invite token provided.</p>"))
		return
	}

	userSub := r.Header.Get("X-User-Sub")
	if userSub == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// invites is onbod-OWNED → read/consume through obdb. The redemption's acl admin
	// row is routd-OWNED, so ConsumeInviteNoGrant touches only onbod.db's invites and
	// the acl grant is written separately to routd.db via the AUDITED AddACLRow.
	// audit.Init pointing at obdb does not force the audit-free twin: AddACLRow
	// appends the record to its own batch (audit.WriteFor), so the grant and the
	// row that records it commit together on the store the grant is written to.
	split := db != obdb

	inv, err := obdb.GetInvite(token)
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

	consume := obdb.ConsumeInvite
	if split {
		consume = obdb.ConsumeInviteNoGrant
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
		if perr := db.AddACLRow(core.ACLRow{
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
			if rerr := obdb.RestoreInvite(token); rerr != nil {
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

	// Subgroup invites carry their parent in the invite_redemptions row consumeInvite
	// just wrote, not in a cookie the caller can rewrite (F50).
	//
	// Redemption grants authority over the target and stops there. Spec 5/18 step 7:
	// it used to route EVERY JID the sub had ever paired at the target, silently
	// moving a returning user's already-routed chats — the seq-0 rows it wrote outrank
	// any higher-seq route those chats already had. /onboard's step-6 branch routes
	// the one unrouted JID instead.
	//
	// Spec 5/W: no slink redirect — the operator or agent issues a chat link on
	// demand.
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
// the proven JID lands in. It posts to handleAddRoute, which re-checks both halves
// — the radio list is a convenience, never the authorization. The csrf field is
// not optional: handleOnboardPost 403s any POST whose form value does not match
// the cookie, which is how the create_world form shipped broken (BUGS F1).
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

func renderDashboard(w http.ResponseWriter, db *store.Store, userSub, username string) {
	esc := html.EscapeString

	var jidsHTML string
	if edges, err := db.MemberEdges(userSub); err == nil {
		// Oldest claim first. `added_at` is an attribute, so the read arrives in
		// child order and the sort is here.
		slices.SortStableFunc(edges, func(a, b store.MembershipEdge) int {
			return strings.Compare(a.AddedAt, b.AddedAt)
		})
		for _, e := range edges {
			platform := e.Child
			if i := strings.Index(e.Child, ":"); i > 0 {
				platform = e.Child[:i]
			}
			// The date, and only from an edge that carries one: an edge with no
			// added_at renders an empty cell rather than slicing a short string.
			claimed := e.AddedAt
			if len(claimed) >= 10 {
				claimed = claimed[:10]
			} else {
				claimed = ""
			}
			jidsHTML += fmt.Sprintf(
				`<tr><td><span class="dot dot-ok"></span> %s</td><td>%s</td><td class="dim">%s</td></tr>`,
				esc(platform), esc(e.Child), esc(claimed))
		}
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
	if routes, err := db.RouteTable(); err == nil {
		// One row per (jid, rule) pair, ordered by jid then target — the join's
		// order, rebuilt here because the rule that names a jid is found by the
		// same predicate unroutedJID asks and not by a key.
		for _, jid := range db.Members(userSub) {
			matched := jidRoutes(routes, jid)
			targets := make([]string, 0, len(matched))
			for _, r := range matched {
				targets = append(targets, r.Target)
			}
			slices.Sort(targets)
			for _, target := range targets {
				routesHTML += fmt.Sprintf(
					`<tr><td>%s</td><td>%s</td></tr>`, esc(jid), esc(target))
			}
		}
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

// sendReply returns its outcome. It used to return nothing, so a caller could
// not tell a delivery from a 502 and logged success either way — on sloth the
// pairing-link tick logged "sent pairing link" on the line directly after
// "send reply non-2xx" every night for months (BUGS L67).
func sendReply(cfg config, jid, text string) error {
	payload := map[string]string{"jid": jid, "text": text}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", cfg.gatedURL+"/v1/outbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Present the service:onbod JWT (messages:write) on routd's JWT-gated
	// /v1/outbound. A nil source or token blip drops the reply rather than sending
	// a credential routd rejects. svcToken is always set in production (loadConfig).
	if cfg.svcToken == nil {
		return fmt.Errorf("onbod has no service token")
	}
	tok, err := cfg.svcToken(req.Context())
	if err != nil {
		return fmt.Errorf("service-token exchange: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	obs.InjectRequest(req.Context(), req)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post outbound: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("outbound %s: status %d", jid, resp.StatusCode)
	}
	return nil
}

// userFolders returns the distinct allow-scopes the sub has access to via the
// unified acl tables (post-0053 cutover). Operator (`role:operator` membership)
// returns the `**` scope through the role's row.
func userFolders(db *store.Store, sub string) []string {
	if sub == "" {
		return nil
	}
	// Every principal the sub speaks as: itself, and the transitive closure of
	// acl_membership above it.
	principals := append([]string{sub}, db.Ancestors(sub)...)
	// One begins_with per principal, which is the exactness `principal IN (…)`
	// had: the separator ends the principal, so `folder:a` cannot reach
	// `folder:ab`.
	rows, err := db.ACLRowsFor(principals)
	if err != nil {
		slog.Error("userFolders read grants", "sub", sub, "err", err)
		return nil
	}
	var folders []string
	for _, r := range rows {
		if r.Effect != "allow" || r.Scope == "" {
			continue
		}
		if !slices.Contains(folders, r.Scope) {
			folders = append(folders, r.Scope)
		}
	}
	slices.Sort(folders)
	return folders
}

func isOperator(folders []string) bool {
	return slices.Contains(folders, "**")
}

func handleDeleteRoute(w http.ResponseWriter, r *http.Request,
	db *store.Store, sub string, folders []string) {
	id, err := strconv.ParseInt(r.FormValue("route_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid route_id", http.StatusBadRequest)
		return
	}

	// RouteByID, not a read that collapses "absent" into "unreadable": an
	// operator told "route not found" about a rule that is still firing goes and
	// creates a second one. An ambiguous id is a 409 naming both coordinates,
	// because the delete below refuses it anyway (BUGS `L364`, `L366`).
	route, ok, err := db.RouteByID(id)
	if errors.Is(err, store.ErrRouteIDAmbiguous) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		slog.Error("route lookup", "id", id, "sub", sub, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if !auth.MatchGroups(folders, route.Target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := deleteRoute(r.Context(), db, id); err != nil {
		slog.Error("route delete", "id", id, "sub", sub, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
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
func userOwnsMatch(db *store.Store, sub, match string) bool {
	const prefix = "room="
	if !strings.HasPrefix(match, prefix) {
		return false
	}
	room := match[len(prefix):]
	if room == "" {
		return false
	}
	for _, jid := range db.Members(sub) {
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
// unattributed row. Authorization belongs to the caller; this records who acted
// and how.
//
// (seq, target, match) is the rule's COORDINATE, so a rule onbod already wrote
// is not written twice: the write is create-if-absent and answers with the id
// the existing rule holds. That is what a routing table addressed by its own
// content means — two rules that dispatch identically are one rule, and a second
// id for it would hand delete_route(id) a handle that removes somebody else's.
func insertRoute(db *store.Store, match, target, sub, via string) error {
	ctx := context.Background()
	ws := &store.WriteSet{}
	_, err := db.PutRouteInto(ctx, ws, core.Route{Seq: 0, Match: match, Target: target},
		sub, via, fmt.Errorf("route %s → %s was created under this call", match, target))
	if err != nil {
		return err
	}
	return db.Apply(ctx, ws)
}

// deleteRoute removes one rule and NO audit row: onbod's route writers are the
// onboarding form's two acts, and the row that records them is the onboarding
// state, not an audit_log entry the other writers of this table emit.
//
// A rule that is not there is not an error — the form is the only caller, and it
// deletes a rule it just listed.
func deleteRoute(ctx context.Context, st *store.Store, id int64) error {
	ws := &store.WriteSet{}
	found, err := st.DeleteRouteInto(ctx, ws, id,
		fmt.Errorf("route %d was removed under this call", id))
	if err != nil || !found {
		return err
	}
	return st.Apply(ctx, ws)
}

func handleAddRoute(w http.ResponseWriter, r *http.Request,
	db *store.Store, sub string, folders []string) {
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
