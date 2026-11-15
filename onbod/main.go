package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
)

type gate struct {
	kind        string
	param       string
	limitPerDay int
}

type config struct {
	core         *core.Config
	listenAddr   string
	pollInterval time.Duration
	greeting     string
	authBaseURL  string
	secureCookie bool
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

	obdb, err := openOwned()
	if err != nil {
		slog.Error("open onbod's store", "err", err)
		os.Exit(1)
	}
	defer obdb.Close() //nolint:errcheck
	slog.Info("onbod owns its tables", "store", dynstore.URL())

	xdb, err := store.OpenOwner(store.OwnerRoutd)
	if err != nil {
		slog.Error("open routd's store", "err", err)
		os.Exit(1)
	}
	defer xdb.Close() //nolint:errcheck
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

	pollOnboarding(xdb, obdb, cfg)
	for {
		select {
		case <-tick.C:
			pollOnboarding(xdb, obdb, cfg)
		case <-stop:
			_ = srv.Close()
			slog.Info("onbod stopped")
			return
		}
	}
}

func newOnbodMux(xdb, obdb *store.Store, cfg config, ks *auth.KeySet) *http.ServeMux {
	mux := http.NewServeMux()

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

	adm := &admin{st: obdb, ks: ks}
	adm.mountOnboarding(mux)
	adm.mountInvites(mux)
	adm.mountGates(mux)
	adm.mountAudit(mux)

	mux.HandleFunc("GET /dash/onbod/", stripUnsigned(adm.handleDash))
	mux.HandleFunc("POST /dash/onbod/approve/{jid}", stripUnsigned(adm.handleDashApprove))
	mux.HandleFunc("POST /dash/onbod/deny/{jid}", stripUnsigned(adm.handleDashDeny))
	mux.HandleFunc("POST /dash/onbod/reprompt/{jid}", stripUnsigned(adm.handleDashReprompt))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}
	return mux
}

func stripUnsignedGuard(ks *auth.KeySet) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(obs.ExtractRequest(r))
			if r.Header.Get("X-User-Sub") == "" {
				next(w, r)
				return
			}
			if ks == nil {
				next(w, r)
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
		listenAddr:   core.EnvOr("ONBOD_LISTEN_ADDR", ":8080"),
		pollInterval: 10 * time.Second,
	}
	if iv := os.Getenv("ONBOARD_POLL_INTERVAL"); iv != "" {
		if d, err := time.ParseDuration(iv); err == nil {
			cfg.pollInterval = d
		}
	}

	if os.Getenv("AUTHD_URL") == "" {
		return config{}, fmt.Errorf("onbod: AUTHD_URL required")
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
	if _, domain, found := strings.CutLast(sub, "@"); found {
		return domain
	}
	return ""
}

func pollOnboarding(xdb, obdb *store.Store, cfg config) {
	awaiting, err := obdb.ReadyOnboardingPrompts(context.Background(), time.Now())
	if err != nil {
		slog.Error("onboarding poll query", "err", err)
		return
	}
	promptUnprompted(xdb, obdb, cfg, awaiting)
	consumePairings(xdb, obdb, cfg)
	admitFromQueue(obdb)
	queueNotices(xdb, obdb, cfg)
}

func promptUnprompted(xdb, obdb *store.Store, cfg config, awaiting []store.OnboardingRow) {
	ctx := context.Background()
	for _, row := range awaiting {
		if row.Sender == "" {
			continue
		}
		if _, err := obdb.ClaimOnboardingPrompt(row.JID); err != nil {
			slog.Error("claim onboarding greeting", "jid", row.JID, "err", err)
			continue
		}
		id, at, err := obdb.OnboardingPrompt(ctx, row.JID)
		if err != nil || id == "" {
			if err != nil {
				slog.Error("read onboarding greeting", "jid", row.JID, "err", err)
			}
			continue
		}
		if !observePairings(xdb, obdb, cfg, []store.OnboardingRow{row}) {
			if err := obdb.CompleteOnboardingPrompt(ctx, row.JID, id, false); err != nil {
				slog.Error("defer pairing lookup", "err", err)
			}
			continue
		}
		current, _, err := obdb.OnboardingPrompt(ctx, row.JID)
		if err != nil || current != id {
			continue
		}
		err = xdb.QueuePairingGreeting(ctx, id, row.JID, row.Sender, at, cfg.authBaseURL, cfg.greeting)
		if err != nil {
			slog.Error("queue pairing greeting", "jid", row.JID, "err", err)
		}
		if ackErr := obdb.CompleteOnboardingPrompt(ctx, row.JID, id, err == nil); ackErr != nil {
			slog.Error("acknowledge pairing greeting", "jid", row.JID, "err", ackErr)
		}
	}
}

func queueNotices(xdb, obdb *store.Store, cfg config) {
	ctx := context.Background()
	work, err := obdb.ReadyOnboardingNotices(ctx, time.Now())
	if err != nil {
		slog.Error("read onboarding notices", "err", err)
		return
	}
	for _, item := range work {
		at, err := time.Parse(time.RFC3339Nano, item.Attrs["_notice_at"])
		if err != nil {
			slog.Error("onboarding notice instant", "err", err)
			continue
		}
		var text string
		switch item.Attrs["_notice_state"] {
		case "queued":
			text = "You're in the queue. Open " + strings.TrimRight(cfg.authBaseURL, "/") + "/onboard to see your position."
		case "refused":
			text = "This account can't be set up here: no onboarding gate matches it. Ask the operator for an invite."
		default:
			slog.Error("unknown onboarding notice", "state", item.Attrs["_notice_state"])
			continue
		}
		_, err = xdb.PutOutboundOnce(ctx, core.Message{ID: item.Attrs["_notice_id"], ChatJID: item.Key.Sort, Timestamp: at, Sender: store.SystemSender, BotMsg: true, Content: text})
		if err != nil {
			slog.Error("queue onboarding notice", "jid", item.Key.Sort, "err", err)
		}
		if ackErr := obdb.CompleteOnboardingNotice(ctx, item, err == nil); ackErr != nil {
			slog.Error("acknowledge onboarding notice", "err", ackErr)
		}
	}
}

func consumePairings(xdb, obdb *store.Store, cfg config) {
	ctx := context.Background()
	work, err := xdb.PairingWork(ctx)
	if err != nil {
		slog.Error("read pairing work", "err", err)
		return
	}
	for _, item := range work {
		sender, _, err := store.PairingWorkIdentity(item)
		if err != nil {
			slog.Error("pairing work identity", "err", err)
			continue
		}
		rows, err := obdb.AwaitingSender(ctx, sender)
		if err != nil {
			slog.Error("read paired sender", "err", err)
			continue
		}
		if !observePairings(xdb, obdb, cfg, rows) {
			continue
		}
		left, err := obdb.AwaitingSender(ctx, sender)
		if err != nil || len(left) > 0 {
			continue
		}
		if err := xdb.AcknowledgePairing(ctx, item); err != nil {
			slog.Error("acknowledge pairing", "err", err)
		}
	}
}

func observePairings(xdb, obdb *store.Store, cfg config, awaiting []store.OnboardingRow) bool {
	for _, row := range awaiting {
		if row.Sender == "" || row.UserSub != "" {
			continue
		}
		jid := row.JID

		edges, err := xdb.ParentEdges(row.Sender)
		if err != nil {
			slog.Error("observePairings read pairing edge", "jid", jid, "err", err)
			return false
		}
		for _, e := range edges {
			if e.AddedBy != store.PairingAddedBy {
				continue
			}
			if err := admitJID(obdb, cfg, jid, e.Parent); err != nil {
				slog.Error("admit paired sender", "jid", jid, "err", err)
				return false
			}
			break
		}
	}
	return true
}

func admitJID(obdb *store.Store, cfg config, jid, userSub string) error {
	gates, err := loadGates(obdb)
	if err != nil {
		return err
	}

	if len(gates) > 0 {
		g := matchGate(gates, userSub)
		if g == nil {
			if err := obdb.RefuseOnboarding(jid, userSub); err != nil {
				slog.Error("refuse jid", "jid", jid, "err", err)
				return err
			}
			auditAdmission(jid, userSub, "onboarding.refuse", "none")
			slog.Warn("refused jid: no matching gate", "jid", jid, "user", userSub)

			return nil
		}
		k := gateKey(*g)
		if err := obdb.QueueOnboarding(jid, userSub, k); err != nil {
			slog.Error("queue jid", "jid", jid, "err", err)
			return err
		}
		auditAdmission(jid, userSub, "onboarding.queue", k)
		slog.Info("queued jid", "jid", jid, "user", userSub, "gate", k)

		return nil
	}

	if err := obdb.AdmitOnboarding(jid, userSub); err != nil {
		slog.Error("approve jid", "jid", jid, "err", err)
		return err
	}
	auditAdmission(jid, userSub, "onboarding.approve", "none")
	slog.Info("approved jid", "jid", jid, "user", userSub, "gate", "none")
	return nil
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

func handleOnboard(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, cfg config) {
	userSub := r.Header.Get("X-User-Sub")
	if userSub == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	handleDashboard(w, r, db, obdb, userSub, ensureCSRFToken(w, r, cfg))
}

func handleDashboard(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, userSub, csrf string) {
	username, known, err := db.Username(userSub)
	if err != nil {

		slog.Error("username lookup", "user", userSub, "err", err)
	}
	if !known || err != nil {
		renderPage(w, "Error", template.HTML("<p>User not found.</p>"))
		return
	}

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

		_, invited := latestRedemption(obdb, userSub)
		if invited || mayCreateFirstWorld(obdb, userSub) {
			renderUsernamePicker(w, username, csrf)
			return
		}
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to join. Ask an admin for one.</p>`))
		return
	}

	if jid := unroutedJID(db, obdb, userSub); jid != "" {
		switch folders := adminFolders(db, userSub); len(folders) {
		case 0:
			renderNoWorld(w, jid)
			return
		case 1:

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

	renderDashboard(w, db, obdb, userSub, username)
}

const csrfCookieName = "onbod_csrf"

func ensureCSRFToken(w http.ResponseWriter, r *http.Request, cfg config) string {
	return auth.EnsureCSRF(w, r, csrfCookieName, cfg.secureCookie)
}

func checkCSRF(r *http.Request) bool {
	return auth.CheckCSRF(r, csrfCookieName)
}

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
		handleAddRoute(w, r, db, obdb, userSub, folders)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,29}$`)

func mayCreateFirstWorld(obdb *store.Store, userSub string) bool {
	gates, err := loadGates(obdb)
	if err != nil || userSub == "" || len(gates) == 0 {
		return false
	}
	n, err := obdb.ApprovedAdmissions(userSub)
	if err != nil {
		slog.Error("approved-admission lookup", "user", userSub, "err", err)
		return false
	}
	return n > 0
}

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

const createWorldAttempts = 8

func issuerGranter(issuedBy string) (auth.Caller, bool) {
	switch issuedBy {
	case "cli", "onbod", "routd":
		return auth.Caller{}, false
	}
	if strings.HasPrefix(issuedBy, "agent:") {
		return auth.Caller{Principal: "folder:" + strings.TrimPrefix(issuedBy, "agent:")}, true
	}
	return auth.Caller{Principal: issuedBy}, true
}

func checkIssuer(db *store.Store, issuer string, rows []core.ACLRow) (objstore.Version, error) {
	caller, check := issuerGranter(issuer)
	if !check {
		return "", nil
	}
	mutations := make([]auth.Mutation, len(rows))
	for i, row := range rows {
		mutations[i] = auth.Mutation{Row: row}
	}
	return auth.CheckGrant(db, caller, auth.Ask{}, mutations...)
}

func handleCreateWorld(w http.ResponseWriter, r *http.Request, db, obdb *store.Store, cfg config, userSub string) {

	redemption, invited := latestRedemption(obdb, userSub)
	if !invited && !mayCreateFirstWorld(obdb, userSub) {
		renderPage(w, "Invite Required",
			template.HTML(`<p>You need an invite link to create a workspace.</p>`))
		return
	}

	var parent string
	if invited {
		parent = strings.TrimSuffix(redemption.TargetGlob, "/")
	}

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

	var version objstore.Version
	if invited {
		var err error
		version, err = checkIssuer(db, redemption.IssuedBy, store.WorldGrantRows(userSub, folder))
		if err != nil {
			if !errors.Is(err, auth.ErrGrantRefused) {
				http.Error(w, "grant check failed", http.StatusInternalServerError)
				return
			}
			renderPage(w, "Invitation Expired", template.HTML("<p>The invitation no longer grants access.</p>"))
			return
		}
	}

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

	if err := container.SetupGroup(coreCfg, folder); err != nil {
		slog.Error("create world: setup group", "folder", folder, "err", err)
		renderPage(w, "Error", template.HTML("<p>Internal error.</p>"))
		return
	}

	var writeErr error
	for attempt := 0; attempt < createWorldAttempts; attempt++ {
		writeErr = writeWorld(db, folder, username, userSub, version)
		if !invited || !errors.Is(writeErr, objstore.ErrScopeMoved) || attempt == createWorldAttempts-1 {
			break
		}
		version, writeErr = checkIssuer(db, redemption.IssuedBy, store.WorldGrantRows(userSub, folder))
		if writeErr != nil {
			break
		}
	}
	if err := writeErr; err != nil {
		slog.Error("create world: db tx", "folder", folder, "err", err)
		http.Error(w, "create world failed", http.StatusInternalServerError)
		return
	}

	if err := db.SeedDefaultTasks(folder, folder); err != nil {
		slog.Warn("create world: seed default tasks", "folder", folder, "err", err)
	}

	slog.Info("world created", "folder", folder, "parent", parent, "user", userSub)

	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

func membershipJIDs(db, obdb *store.Store, userSub string) []string {
	rows, err := obdb.ListOnboarding("")
	if err != nil {
		slog.Error("read paired sender chats", "err", err)
		return nil
	}
	senders := db.Members(userSub)
	var chats []string
	for _, row := range rows {
		if row.Sender != "" && slices.Contains(senders, row.Sender) {
			chats = append(chats, row.JID)
		}
	}
	slices.Sort(chats)
	return slices.Compact(chats)
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
func writeWorld(db *store.Store, folder, username, userSub string, version objstore.Version) error {
	ctx := context.Background()
	row, err := store.GroupCreateWrite(core.Group{
		Folder: folder, AddedAt: time.Now(), Product: core.DefaultProduct,
	})
	if err != nil {
		return err
	}

	rows := store.WorldGrantRows(userSub, folder)
	grant, err := store.ACLGrantWrite(rows[0])
	if err != nil {
		return err
	}
	reach, err := store.ACLGrantWrite(rows[1])
	if err != nil {
		return err
	}
	world, err := store.WorldEdgeWrite(folder)
	if err != nil {
		return err
	}
	created, err := store.SeedMemberRole(ctx, db, folder, version, row, grant, reach, world)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("folder %q already exists", folder)
	}
	return db.RecordPerson(ctx, userSub, username, username)
}

func loadGates(db *store.Store) ([]gate, error) {
	rows, err := db.EnabledGates()
	if err != nil {
		slog.Error("loadGates query", "err", err)
		return nil, err
	}
	var out []gate
	for _, g := range rows {
		out = append(out, gateFromKey(g.Gate, g.LimitPerDay))
	}
	return out, nil
}

func gateFromKey(key string, limit int) gate {
	if key == "*" {
		return gate{kind: "*", limitPerDay: limit}
	}
	kind, param, _ := strings.Cut(key, ":")
	return gate{kind: kind, param: param, limitPerDay: limit}
}

func adminFolders(db *store.Store, userSub string) []string {
	folders, err := scanFolders(db)
	if err != nil {
		slog.Error("adminFolders groups", "err", err)
		return nil
	}

	var out []string
	for _, f := range folders {
		if auth.Authorize(db, auth.Caller{Principal: userSub}, "admin", f, nil) {
			out = append(out, f)
		}
	}
	return out
}

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

func foldASCII(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

func unroutedJID(db, obdb *store.Store, sub string) string {
	routes, err := db.RouteTable()
	if err != nil {

		slog.Error("unroutedJID route lookup", "sub", sub, "err", err)
		return ""
	}
	for _, jid := range membershipJIDs(db, obdb, sub) {
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
	pos++

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
	ctx := context.Background()
	now := time.Now()
	work, err := db.DueAdmissionGates(ctx, now)
	if err != nil {
		slog.Error("read due admissions", "err", err)
		return
	}
	for _, item := range work {
		for {
			ids, err := db.AdmitGate(ctx, item, now)
			if err != nil {
				slog.Error("admit gate", "gate", item.Key.Sort, "err", err)
				break
			}
			for _, jid := range ids {
				auditAdmission(jid, "", "onboarding.approve", item.Key.Sort)
			}
			if len(ids) == 0 {
				break
			}
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

	row := store.InviteGrantRow(inv.TargetGlob)
	row.Principal = userSub
	version, err := checkIssuer(db, inv.IssuedBySub, []core.ACLRow{row})
	if err != nil {
		if !errors.Is(err, auth.ErrGrantRefused) {
			http.Error(w, "grant check failed", http.StatusInternalServerError)
			return
		}
		renderPage(w, "Invitation Expired", template.HTML("<p>The invitation no longer grants access.</p>"))
		return
	}
	consumed, err := obdb.ConsumeInviteNoGrant(token, userSub)
	if err != nil {
		slog.Warn("invite invalid", "reason", "exhausted",
			"token_hash", chanlib.ShortHash(token), "user", userSub, "err", err)
		renderPage(w, "Invite Used",
			template.HTML("<p>This invite link has already been used the maximum number of times.</p>"))
		return
	}

	target := consumed.TargetGlob

	if !strings.HasSuffix(target, "/") {
		if perr := db.AddACLRow(row, version); perr != nil {

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

	http.Redirect(w, r, "/onboard", http.StatusSeeOther)
}

func renderPage(w http.ResponseWriter, title string, body template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, theme.Page(title, body))
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

func renderWorldPicker(w http.ResponseWriter, jid string, folders []string, csrf string) {
	esc := html.EscapeString
	var opts strings.Builder
	for i, f := range folders {
		checked := ""
		if i == 0 {
			checked = " checked"
		}
		fmt.Fprintf(&opts,
			`<label style="display:block;padding:.3rem 0">`+
				`<input type="radio" name="target" value="%s" style="width:auto;margin-right:.5rem"%s>%s</label>`,
			esc(f), checked, esc(f))
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

func renderNoWorld(w http.ResponseWriter, jid string) {
	renderPage(w, "Nowhere to route", template.HTML(fmt.Sprintf(
		`<p><span class="id">%s</span> is linked to your account, but you don't administer any world it could go to, so it stays silent.</p>`+
			`<p class="dim">Ask an admin for an invite to a world, then open this page again.</p>`,
		html.EscapeString(jid))))
}

func renderDashboard(w http.ResponseWriter, db, obdb *store.Store, userSub, username string) {
	esc := html.EscapeString

	var jidsHTML string
	if edges, err := db.MemberEdges(userSub); err == nil {

		slices.SortStableFunc(edges, func(a, b store.MembershipEdge) int {
			return strings.Compare(a.AddedAt, b.AddedAt)
		})
		for _, e := range edges {
			platform := e.Child
			if i := strings.Index(e.Child, ":"); i > 0 {
				platform = e.Child[:i]
			}

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
		fmt.Fprintf(&groupsHTML,
			`<tr><td><span class="dot dot-ok"></span> %s</td></tr>`, esc(folder))
	}

	var routesHTML string
	if routes, err := db.RouteTable(); err == nil {

		for _, jid := range membershipJIDs(db, obdb, userSub) {
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
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body>
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

func userFolders(db *store.Store, sub string) []string {
	folders, err := auth.UserScopes(db, sub)
	if err != nil {
		slog.Error("userFolders read policy", "sub", sub, "err", err)
		return nil
	}
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

var matchRe = regexp.MustCompile(`\A[A-Za-z0-9_.:=@/-]+\z`)

func userOwnsMatch(db, obdb *store.Store, sub, match string) bool {
	const prefix = "room="
	if !strings.HasPrefix(match, prefix) {
		return false
	}
	room := match[len(prefix):]
	if room == "" {
		return false
	}
	for _, jid := range membershipJIDs(db, obdb, sub) {
		if i := strings.Index(jid, ":"); i > 0 && jid[i+1:] == room {
			return true
		}
	}
	return false
}

const (
	routeViaPicker    = "picker"
	routeViaSoleWorld = "sole_world"
)

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
	db, obdb *store.Store, sub string, folders []string) {
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
	if !isOperator(folders) && !userOwnsMatch(db, obdb, sub, match) {
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
