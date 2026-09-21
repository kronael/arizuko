package main

import (
	"context"
	_ "embed"
	"encoding/json"
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
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/auth/surrogate"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/dashd/dashstore"
	"github.com/kronael/arizuko/diary"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
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

// brandName is the operator-configured product name (ASSISTANT_NAME env var).
// Set at startup; default "arizuko". Used in page titles and the portal header.
var brandName = "arizuko"

// accentOverride is an optional CSS color string (DASH_ACCENT_COLOR env var)
// that overrides --accent for white-labelling. Empty = use theme default.
var accentOverride = ""

// shortTS trims a full ISO timestamp to HH:MM for compact display.
// Input may be "2026-06-16T15:58:28Z" or "2026-06-16 15:58:28"; output "15:58".
func shortTS(ts string) string {
	for i, r := range ts {
		if r == 'T' || r == ' ' {
			rest := ts[i+1:]
			if len(rest) >= 5 {
				return rest[:5]
			}
		}
	}
	if len(ts) > 16 {
		return ts[:16]
	}
	return ts
}

// relativeTS returns a human-relative label ("now", "5m", "2h", "3d") for use
// in <abbr> display alongside the full ISO timestamp as the title.
func relativeTS(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return shortTS(ts)
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// abbrTS is the one renderer for a timestamp cell: relative label visible, full
// ISO form on hover (SCREENS.md principle 7).
func abbrTS(ts string) string {
	return `<abbr title="` + esc(ts) + `">` + esc(relativeTS(ts)) + `</abbr>`
}

// remainingTS is relativeTS for a FUTURE deadline: "12m", "2h", "3d" of time
// left. It exists because relativeTS measures time.Since, so every not-yet-
// reached deadline lands in its `d < time.Minute` arm and renders "now" — an
// engagement window with 28 minutes left would read as expiring this second.
// A deadline already passed renders "expired" rather than a negative span.
func remainingTS(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return shortTS(ts)
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return "expired"
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

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

	port := os.Getenv("DASH_PORT")
	if port == "" {
		port = ":8080"
	}
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	if v := os.Getenv("ASSISTANT_NAME"); v != "" {
		brandName = v
		navLinks[0].Label = v
	}
	if v := os.Getenv("DASH_ACCENT_COLOR"); v != "" {
		accentOverride = v
	}

	// store.OpenOwner probes for the `acl` entity, so an endpoint routd has never
	// booted against fails loudly at boot instead of serving an empty dashboard
	// (BUGS F58).
	routdOwn, err := store.OpenOwner(store.OwnerRoutd)
	if err != nil {
		slog.Error("open routd's store", "err", err)
		os.Exit(1)
	}
	defer routdOwn.Close() //nolint:errcheck // the process is ending

	slog.Info("dashd started", "store", dynstore.URL(), "port", port)

	// APP_SRC_DIR is the container-side path to ant/ (routd/runed read stock
	// skills the same way); HOST_APP_DIR is a HOST path and unusable from
	// inside the container unless mounted (BUGS F54).
	appDir := os.Getenv("APP_SRC_DIR")
	if appDir == "" {
		appDir = os.Getenv("HOST_APP_DIR")
	}

	// guard admits a request that PROVES it transited proxyd, then trusts the
	// proxyd-stamped X-User-Sub/-Groups as the END-USER identity. dashd's authz reads
	// those headers directly, so — unlike webd's MCP gate — the bearer must NOT become
	// the identity: it carries service:proxyd, not the user. No verifier (AUTHD_URL
	// unset) → guard is open.
	var ks *auth.KeySet
	// svcSrc is dashd's service:dashd ES256 token source, presented on the only
	// daemon-to-daemon call dashd makes: the operator WhatsApp re-pair proxy to
	// whapd's /v1/pair/*. nil → local dev (no AUTHD_URL); the pair call goes out
	// unauthenticated.
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

	// audit_log is routd's (its owner, routd migration 0016), which is the store
	// opened above — so the audit sink, the /dash/audit/ reader, and every admin
	// mutation share one handle.
	audit.Init(routdOwn.Obj(), os.Getenv("ARIZUKO_INSTANCE"))
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

	// onbod OWNS invites/onboarding_gates; the invites page writes them directly,
	// same FS-access discipline.
	//
	// The path is CONFIGURED, never derived as a sibling of dsn: under per-owner
	// subdirectories the siblings are not siblings, and deriving one owner's file from
	// another's is the pattern CLAUDE.md bans. Unavailable = onbod's profile is off,
	// and the view renders a "store unavailable" banner.
	var onbodHandle objstore.Store
	if onbodOwn := openSiblingOwner(store.OwnerOnbod); onbodOwn != nil {
		defer onbodOwn.Close() //nolint:errcheck // the process is ending
		onbodHandle = onbodOwn.Obj()
	}

	// SECRETS_KEY keyring: dashd writes user secrets into routd.db directly and MUST
	// seal them at rest under the same key routd reads with. Unset → every write is
	// refused, so the secret pages error instead of persisting plaintext (BUGS S2).
	// dashd does not exit: the key gates two pages, not the dashboard.
	secretKeyring := core.SecretKeyring(os.Getenv("SECRETS_KEY"))
	if len(secretKeyring) == 0 {
		slog.Warn("SECRETS_KEY unset in dashd; /dash/me/env and /dash/me/secrets writes will fail until the operator sets it")
	}

	// Surrogate OAuth (spec 5/15): the "Connect <provider>" dance under
	// /dash/me/connections. Engine loads the embedded provider registry; creds
	// come from .env (SURROGATE_<PROVIDER>_CLIENT_ID/_SECRET). stateSecret HMAC-
	// signs the CSRF state cookie (AUTH_SECRET); connBaseURL is the configured
	// callback base (AUTH_BASE_URL, then WEB_HOST).
	surrogateEng, serr := surrogate.NewEngine(dataDir)
	if serr != nil {
		slog.Error("build surrogate engine", "err", serr)
		os.Exit(1)
	}
	for _, name := range surrogateEng.Names() {
		slog.Info("surrogate provider", "name", name, "connectable", surrogateEng.HasCreds(name))
	}
	connBaseURL := strings.TrimRight(os.Getenv("AUTH_BASE_URL"), "/")
	if connBaseURL == "" {
		connBaseURL = strings.TrimRight(os.Getenv("WEB_HOST"), "/")
	}

	mux := http.NewServeMux()
	d := &dash{routd: routdOwn.Obj(), onbod: onbodHandle, groupsDir: groupsDir, appDir: appDir, ks: ks, svc: svcSrc, runedURL: backendURL("RUNED_URL", "runed"), proxydURL: backendURL("PROXYD_URL", "proxyd"), routdURL: backendURL("ROUTER_URL", "routd"), authdURL: backendURL("AUTHD_URL", "authd"), onbodURL: backendURL("ONBOD_URL", "onbod"), secretKeyring: secretKeyring, surrogate: surrogateEng, stateSecret: []byte(os.Getenv("AUTH_SECRET")), connBaseURL: connBaseURL}
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

// backendURL resolves a sibling daemon's intra-container base URL: the env override
// when set, else the compose service name on the fixed in-container port. Root
// CLAUDE.md pins every daemon to :8080, so the URL is derivable, not configuration
// — compose emits neither PROXYD_URL nor RUNED_URL, and without the default the
// kill button answered 503 in every deploy (BUGS F23). One renderer for both.
func backendURL(envKey, service string) string {
	return strings.TrimRight(chanlib.EnvOr(envKey, "http://"+service+":8080"), "/")
}

// upstreamMessage pulls the operator-readable half out of a sibling daemon's error
// body. routd and proxyd both answer apiv1.Err, so `message` is preferred and
// `error` is the fallback. A body that is not that shape is returned raw rather
// than swallowed: a bare status number tells an operator nothing.
func upstreamMessage(body []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if e.Error != "" {
			return e.Error
		}
	}
	return strings.TrimSpace(string(body))
}

// upstreamErr renders a non-2xx from a sibling daemon as one operator-readable
// sentence. One renderer for every daemon dashd calls; proxydUpstreamErr wraps
// it to add the 403 grant hint that is proxyd's alone.
func upstreamErr(daemon string, status int, body []byte) string {
	return fmt.Sprintf("%s said %d: %s", daemon, status, upstreamMessage(body))
}

type dash struct {
	// routd is the handle onto routd's tables — dashd's ONLY message/config
	// store. routd OWNS acl/groups/routes/route_tokens/secrets/audit_log AND the
	// live messages/chats/sessions/user_profiles tables (spec 5/5); dashd
	// reads+writes them here. A handle rather than a pool, so the same handlers
	// run on an instance served from a DynamoDB endpoint (spec 6/42 §7). nil in
	// read-only tests.
	routd objstore.Store
	// onbod is the handle onto onbod's tables. onbod OWNS invites/onboarding_gates
	// in the split topology (spec 5/5); dashd's invites page reads+writes them
	// here. nil when that optional profile is off.
	onbod     objstore.Store
	groupsDir string
	appDir    string                                // APP_SRC_DIR (container path to ant/); used to enumerate stock skills
	ks        *auth.KeySet                          // authd JWKS; verifies proxyd's ES256 transit bearer (nil → local dev open)
	svc       func(context.Context) (string, error) // service:dashd token for the whapd re-pair proxy (nil → local dev)
	runedURL  string                                // runed's /v1 face; target of the /dash/runed/kill operator-kill proxy. backendURL defaults it to the compose service name, so it is empty only in tests ("" → kill 503s)
	// proxydURL is proxyd's own /v1 face, read+written by /dash/proxyd/. proxyd
	// OWNS proxyd_routes, so dashd goes over HTTP rather than into the table —
	// that is also what makes proxyd write the audit row in the mutation's tx.
	proxydURL string
	// routdURL is routd's /v1 face, read AND written by /dash/engagement/. routd OWNS
	// the engagement columns, so dashd asks over HTTP rather than touching
	// chat_reply_state directly — that is what puts the containment check and the audit
	// row on the mutation's own transaction.
	routdURL string
	// authdURL is authd's /v1 face. /dash/audit/ reads its GET /v1/audit — the
	// login rows that record which provider identity was presented, which live
	// in auth.db and nowhere else. dashd is NOT FS-mounted on auth.db and must
	// not become so; HTTP is the only contract authd offers for them.
	authdURL string
	// onbodURL is onbod's /v1 face. dashd IS FS-mounted on onbod.db, but the audit read
	// still goes over HTTP: /dash/audit/ is a fan-out of ONE path across four owners,
	// and a direct-DB fourth source would be a second mechanism that drifts from the
	// other three (spec 5/I, BUGS F35).
	onbodURL string
	// secretKeyring is the SECRETS_KEY material handed to secretStore so user-secret
	// writes seal at rest under the same key routd reads with. Empty → plaintext.
	secretKeyring [][]byte

	// surrogate drives the /dash/me/connections "Connect <provider>" OAuth dance
	// (spec 5/15); nil in read-only tests / when no engine is wired. stateSecret
	// HMAC-signs the dance's CSRF state cookie (reuses auth.SignState). connBaseURL
	// is the absolute base the provider redirects the callback to — configured
	// (must match the provider app), never derived from the request.
	surrogate   *surrogate.Engine
	stateSecret []byte
	connBaseURL string
}

// routdStore is routd's tables as the store package reads and writes them — the
// one door the admin paths (grants/groups/routes/route_tokens) and every page
// read go through. routd OWNS those tables in the split topology, so dashd
// points its admin reads straight at routd's own store.
//
// nil when there is no handle, which is how the pages know to banner instead of
// render. Built per call rather than held, so secretStore can set the keyring
// on its own copy and on nobody else's.
func (d *dash) routdStore() *store.Store {
	if d.routd == nil {
		return nil
	}
	return store.NewOver(d.routd)
}

// onbodStore is onbod's tables the same way. onbod OWNS invites in the split
// topology, so the invites page asks onbod's own store; nil when that profile
// is off and the page banners.
func (d *dash) onbodStore() *store.Store {
	if d.onbod == nil {
		return nil
	}
	return store.NewOver(d.onbod)
}

// ds returns dashd's storage layer over routd's tables — every statement dashd
// runs lives behind it (dashd/dashstore, spec 6/37 step 1). nil for a nil
// handle, the same banner path routdStore answers on.
func (d *dash) ds() *dashstore.Store { return dashstore.New(d.routd) }

// openSiblingOwner opens another daemon's owner store through store.OpenOwner.
// That door is the only place the dial is written (spec 6/42 §7), so dashd asks
// it rather than deciding for itself what it is talking to.
//
// A store that will not open means that daemon's profile is off — an optional
// compose profile onbod has and routd does not — so nil is the honest answer
// and the page banners. The reason is logged, because an endpoint whose owner
// has never booted reads like an owner that is simply not deployed.
func openSiblingOwner(owner string) *store.Store {
	s, err := store.OpenOwner(owner)
	if err != nil {
		slog.Info("owner store unavailable; its dashd view will banner",
			"owner", owner, "err", err)
		return nil
	}
	return s
}

// secretStore is routd's store with the SECRETS_KEY keyring set, so user-secret
// writes (PutSecretRow) seal `v2:` values the same way routd's secretStore does
// — the BYOA encryption-at-rest path. The keys land on the store routdStore
// just built and on no other, so the admin door stays unable to seal one. nil
// when there is no handle (some read-only tests).
func (d *dash) secretStore() *store.Store {
	s := d.routdStore()
	if s == nil {
		return nil
	}
	if len(d.secretKeyring) > 0 {
		s.SetSecretKeys(d.secretKeyring...)
	}
	return s
}

// guard proves a request transited proxyd before letting a handler trust the
// proxyd-stamped identity. The bearer is a TRANSIT proof only — dashd's authz reads
// X-User-Sub/-Groups directly, so we deliberately do NOT stamp the bearer's own
// subject over them. No verifier passes through open; failure redirects to
// /auth/login.
// operatorCtxKey carries the caller's operator verdict for the length of one
// request.
//
// IT EXISTS BECAUSE TWO READERS ANSWERED ONE QUESTION DIFFERENTLY. `callerScope`
// reads the proxyd-stamped header and falls back to an `auth.UserScopes` ACL
// read; `dashNavFor` read the header alone. So an operator BY GRANT, arriving
// without the header, was served the pages and shown no links to them
// (BUGS L324). One derivation, decided once per request, read by both.
type operatorCtxKey struct{}

// operatorFromCtx reports the verdict the guard decided, and whether it decided
// one. A handler mounted outside the guard has none, and says so rather than
// reading a false `false`.
func operatorFromCtx(ctx context.Context) (bool, bool) {
	v, ok := ctx.Value(operatorCtxKey{}).(bool)
	return v, ok
}

// guard proves the request came through proxyd and decides the operator verdict
// once for the handlers below it.
//
// THE WRAPPER IS UNCONDITIONAL. A nil keyset means local dev with no AUTHD_URL
// — the transit check cannot run — but the verdict still must, and returning
// `next` bare skipped BOTH. That is also how every dashd test runs, so a fix
// seated here and gated on the keyset would have been invisible to the suite
// that covers it.
func (d *dash) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(obs.ExtractRequest(r))
		if d.ks != nil && !auth.ProxydTransit(r, d.ks) {
			slog.Warn("dashd: identity proof failed",
				"path", r.URL.Path,
				"attempted_sub", r.Header.Get("X-User-Sub"),
				"remote", r.Header.Get("X-Forwarded-For"))
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		_, operator := d.callerScope(r)
		next(w, r.WithContext(context.WithValue(r.Context(), operatorCtxKey{}, operator)))
	}
}

func (d *dash) registerRoutes(mux *http.ServeMux) {
	// Public — no signed identity required. /health is the container
	// healthcheck target; /openapi.json is mounted before auth by contract;
	// the htmx asset is a static file. Guarding any of these breaks the
	// healthcheck or the unauthenticated doc/asset surface.
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("dashd", mux))
	mux.HandleFunc("GET /dash/assets/htmx.min.js", handleHtmxAsset)

	// Everything below is operator UI / mutations — every handler reads the
	// proxyd-stamped X-User-Sub. guard verifies proxyd's service:proxyd transit
	// bearer (open when AUTHD_URL is unset for local dev) so a request bypassing
	// proxyd cannot forge an operator identity.
	g := d.guard
	mux.HandleFunc("GET /dash/", g(d.handlePortal))
	mux.HandleFunc("GET /dash/services/", g(d.handleServices))
	mux.HandleFunc("GET /dash/packages/", g(d.handlePackages))
	mux.HandleFunc("GET /dash/audit/", g(d.handleAudit))
	mux.HandleFunc("GET /dash/approvals/", g(d.handleApprovals))
	mux.HandleFunc("POST /dash/approvals/{id}/resolve", g(d.handleApprovalResolve))
	mux.HandleFunc("GET /dash/usage/", g(d.handleUsage))
	mux.HandleFunc("GET /dash/proactive/", g(d.handleProactive))
	mux.HandleFunc("GET /dash/engagement/", g(d.handleEngagement))
	mux.HandleFunc("POST /dash/engagement/disengage", g(d.handleEngagementDisengage))
	// /dash/routd/ moved to routd itself (spec 6/28, 6/30): proxyd routes the
	// prefix to routd:8080, so a copy here would be unreachable code rendering
	// the same data from a direct read of routd.db.
	// /dash/runed/ moved to runed itself (spec 6/28, 6/30).
	// /dash/authd/ moved to authd itself (spec 6/28, 6/30).

	// proxyd control plane — the reverse-proxy route table. proxyd owns it, so
	// every handler here is an HTTP call to proxyd's /v1/proxyd_routes.
	// /dash/proxyd/ moved to proxyd itself, forms included (spec 6/28, 6/30).
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

	mux.HandleFunc("GET /dash/tasks/{id}", g(d.handleTaskDetail))
	mux.HandleFunc("POST /dash/tasks/", g(d.handleTaskCreate))
	mux.HandleFunc("POST /dash/tasks/{id}/{action}", g(d.handleTaskAction))

	// /dash/me/secrets — capability credential CRUD (GITHUB_TOKEN, etc.).
	// /dash/me/env   — env-profile key CRUD (ANTHROPIC_API_KEY, etc.).
	// Both identity-bound to signed-in X-User-Sub; CSRF on writes.
	mux.HandleFunc("GET /dash/me/secrets", g(d.handleMeSecrets))
	mux.HandleFunc("POST /dash/me/secrets", g(d.handleMeSecretCreate))
	mux.HandleFunc("PATCH /dash/me/secrets/{key}", g(d.handleMeSecretUpdate))
	mux.HandleFunc("DELETE /dash/me/secrets/{key}", g(d.handleMeSecretDelete))
	mux.HandleFunc("GET /dash/me/env", g(d.handleMeEnv))
	mux.HandleFunc("POST /dash/me/env", g(d.handleMeEnvCreate))
	mux.HandleFunc("PATCH /dash/me/env/{key}", g(d.handleMeEnvUpdate))
	mux.HandleFunc("DELETE /dash/me/env/{key}", g(d.handleMeEnvDelete))

	// /dash/me/connections — surrogate OAuth (spec 5/15). "Connect <provider>"
	// runs the outbound dance and writes the token to the same secrets row a PAT
	// lands in. start/callback/disconnect; identity-bound to X-User-Sub.
	mux.HandleFunc("GET /dash/me/connections", g(d.handleMeConnections))
	mux.HandleFunc("POST /dash/me/connections/{provider}/start", g(d.handleMeConnectionStart))
	mux.HandleFunc("GET /dash/me/connections/{provider}/callback", g(d.handleMeConnectionCallback))
	mux.HandleFunc("DELETE /dash/me/connections/{provider}", g(d.handleMeConnectionDelete))

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

	// Route tokens: list + revoke only. No POST /dash/tokens/{folder}/ — the
	// cockpit mints nothing (routd's route_tokens resource owns issuance).
	mux.HandleFunc("GET /dash/tokens/{folder}/", g(d.handleTokensFolder))
	mux.HandleFunc("POST /dash/tokens/{folder}/{jid}/revoke", g(d.handleTokensRevoke))

	// Invite management — operator-gated.
	mux.HandleFunc("GET /dash/invites/", g(d.handleInvites))
	mux.HandleFunc("POST /dash/invites/", g(d.handleInviteCreate))
	mux.HandleFunc("POST /dash/invites/{ref}/revoke", g(d.handleInviteRevoke))
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

// navLinks: nav order + href + label + operator-only flag. Path-prefix match decides
// aria-current, with /dash/ special-cased so child pages don't light up the home
// link. Operator-only links render only for callers holding `**`; the scoped links
// always show, since their pages filter to visible folders.
var navLinks = []struct {
	Href, Label string
	operator    bool
}{
	{"/dash/", "arizuko", false},
	{"/dash/status/", "status", false},
	{"/dash/tasks/", "tasks", false},
	{"/dash/activity/", "activity", false},
	{"/dash/groups/", "groups", false},
	{"/dash/routes/", "routes", false},
	{"/dash/services/", "services", true},
	{"/dash/packages/", "packages", true},
	{"/dash/invites/", "invites", true},
	{"/dash/approvals/", "approvals", false},
	{"/dash/audit/", "audit", true},
	{"/dash/usage/", "usage", true},
	{"/dash/proactive/", "proactive", true},
	{"/dash/engagement/", "engagement", true},
	{"/dash/memory/", "memory", false},
}

// dashNavFor renders the top nav with aria-current="page" on the link whose
// href is a prefix of the request path. urlPath "" or "/dash/" lights the home
// link only. Operator-only links are hidden from non-operators (`**` absent
// from the signed X-User-Groups).
func dashNavFor(r *http.Request) string {
	urlPath := r.URL.Path
	// The guard decided this once, from the same source callerScope trusts —
	// the stamped header, or an ACL read when it is absent. Falling back to the
	// header alone here is what hid every operator-by-grant's links (BUGS L324).
	operator, decided := operatorFromCtx(r.Context())
	if !decided {
		operator = auth.IsOperator(r)
	}
	name := r.Header.Get("X-User-Name")
	if name == "" {
		sub := r.Header.Get("X-User-Sub")
		if _, after, ok := strings.Cut(sub, ":"); ok {
			name = after
		} else {
			name = sub
		}
	}

	var b strings.Builder
	b.WriteString(`<nav hx-boost="true" hx-target="#content" hx-swap="innerHTML" hx-indicator="#global-spinner" style="display:flex;align-items:center;flex-wrap:wrap">`)
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
	if name != "" {
		badge := esc(name)
		if operator {
			badge += " ◆"
		}
		fmt.Fprintf(&b, `<a href="/dash/profile/" style="margin-left:auto;font-size:.85em;white-space:nowrap">%s</a>`, badge)
	}
	b.WriteString(`</nav><button class="theme-toggle"></button>`)
	b.WriteString(`<div id="global-spinner" class="htmx-indicator dim" style="position:fixed;top:8px;right:12px;font-size:0.85em">loading…</div>`)
	return b.String()
}

const navActiveScript = `<script>document.addEventListener('htmx:afterSettle',function(){var p=location.pathname;document.querySelectorAll('nav a').forEach(function(a){var h=a.getAttribute('href');var active=h==='/dash/'?(p==='/dash/'||p==='/dash'):p.startsWith(h);active?a.setAttribute('aria-current','page'):a.removeAttribute('aria-current')})});</script>`

func dashHead(title string) string {
	css := theme.CSS
	// accentOverride is an operator-set env var (DASH_ACCENT_COLOR), not user
	// input, so it goes into the <style> block raw — no untrusted HTML path.
	if accentOverride != "" {
		css += `:root{--accent:` + accentOverride + `}`
	}
	return `<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + esc(title) + ` — ` + esc(brandName) + `</title>` +
		`<script src="/dash/assets/htmx.min.js"></script>` +
		`<style>` + css + `</style>` + theme.ThemeScript + theme.ToggleScript + navActiveScript + `</head>`
}

// portalTmpl is the portal's INNER content only — the shell (head, nav,
// #content) comes from pageShell like every other page, so boosted nav links
// keep their swap target on the portal too.
var portalTmpl = template.Must(template.New("portal").Parse(`<h1 class="brand">{{.Brand}}</h1>
<p class="dim">{{.Subtitle}}</p>
{{if eq .GroupCount 0}}<div class="banner-warn">No groups configured. <a href="/dash/groups/new">Create your first group</a> to get started.</div>{{end}}
{{if gt .ErroredCount 0}}<div class="banner-warn"><a href="/dash/status/">{{.ErroredCount}} errored chat{{if gt .ErroredCount 1}}s{{end}}{{if gt .FailedTasks 0}} · {{.FailedTasks}} failed task{{if gt .FailedTasks 1}}s{{end}}{{end}}</a></div>{{end}}
{{if gt .HeldApprovals 0}}<div class="banner-warn"><a href="/dash/approvals/">{{.HeldApprovals}} tool call{{if gt .HeldApprovals 1}}s{{end}} held for your approval</a></div>{{end}}
<div class="tiles">
<a class="tile" href="/dash/status/"><h2>status<span class="dot {{.StatusDot}}"></span></h2><p>service health</p></a>
<a class="tile" href="/dash/tasks/"><h2>tasks<span class="dot {{.TasksDot}}"></span></h2><p>scheduled jobs</p></a>
<a class="tile" href="/dash/activity/"><h2>activity</h2><p>message flow</p></a>
<a class="tile" href="/me/chats"><h2>chat</h2><p>web chat sessions</p></a>
<a class="tile" href="/dash/packages/"><h2>packages</h2><p>installed packages</p></a>
<a class="tile" href="/dash/groups/"><h2>groups</h2><p>group hierarchy</p></a>
<a class="tile" href="/dash/routes/"><h2>routes</h2><p>message routing table</p></a>
<a class="tile" href="/dash/memory/"><h2>memory</h2><p>knowledge browser</p></a>
<a class="tile" href="/dash/profile/"><h2>profile</h2><p>account and API keys</p></a>
{{if .Operator}}<a class="tile" href="/dash/services/"><h2>services</h2><p>daemon health</p></a>{{end}}
{{if .Operator}}<a class="tile" href="/dash/invites/"><h2>invites</h2><p>onboarding invites</p></a>{{end}}
</div>`))

func (d *dash) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dash/" {
		http.NotFound(w, r)
		return
	}
	allowed, operator := d.callerScope(r)

	groupCount := d.countVisibleGroups(allowed, operator)
	erroredCount := d.countVisibleErroredChats(allowed, operator)
	failedTasks := d.countVisibleFailedTasks(allowed, operator)

	// The nudge counts only the calls this caller may answer, which is what the
	// page it links to shows (spec 5/19): a verdict needs `admin` on the held
	// call's folder, so a world owner is nudged about their own world and nobody
	// is nudged toward a queue that renders empty for them.
	heldApprovals := d.countHeldApprovals(r)

	statusDot := "dot-ok"
	if erroredCount > 0 {
		statusDot = "dot-warn"
	}

	tasksDot := "dot-ok"
	if failedTasks >= 3 {
		tasksDot = "dot-err"
	} else if failedTasks > 0 {
		tasksDot = "dot-warn"
	}

	subtitle := "operator dashboard"
	if inst := os.Getenv("ARIZUKO_INSTANCE"); inst != "" {
		subtitle = inst + " operator dashboard"
	}

	pageShell(w, r, brandName)
	if err := portalTmpl.Execute(w, struct {
		Brand         string
		Subtitle      string
		Operator      bool
		StatusDot     string
		TasksDot      string
		GroupCount    int
		ErroredCount  int
		FailedTasks   int
		HeldApprovals int
	}{brandName, subtitle, operator, statusDot, tasksDot, groupCount, erroredCount, failedTasks, heldApprovals}); err != nil {
		slog.Warn("portal: template execute", "err", err)
	}
	pageClose(w, r)
}

// crumb is one breadcrumb segment; an empty Href renders a plain label.
type crumb struct{ Href, Label string }

// pageShell opens the page shell (Content-Type→DOCTYPE→head→nav→#content) —
// the ONE shell renderer, portal included, so #content exists on every page
// the nav's hx-boost targets (a page without it makes every boosted nav click
// die on htmx:targetError). htmx swaps skip the shell: the fragment lands as
// innerHTML of the existing #content div; emitting it again would nest it.
func pageShell(w http.ResponseWriter, r *http.Request, title string) {
	if isHtmx(r) {
		return
	}
	fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body><div class="page-wide">%s<div id="content">`,
		dashHead(title), dashNavFor(r))
}

// pageTopFor renders the page shell (or htmx partial) with crumbs + h1.
func pageTopFor(w http.ResponseWriter, r *http.Request, title string, crumbs ...crumb) {
	pageShell(w, r, title)
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
	pageTopFor(w, r, "Status")

	groupCount := d.countVisibleGroups(allowed, operator)
	erroredCount := d.countVisibleErroredChats(allowed, operator)

	bannerClass := "ok"
	if erroredCount > 0 {
		bannerClass = "warn"
	}
	errLink := fmt.Sprintf(`%d groups`, groupCount)
	if erroredCount > 0 {
		errLink += fmt.Sprintf(` &middot; <a href="/dash/activity/">%d errored chat%s</a>`,
			erroredCount, pluralS(erroredCount))
	}
	fmt.Fprint(w, `<p class="dim">Service health</p>`)
	fmt.Fprint(w, htmlBannerRaw(bannerClass, errLink))
	if groupCount == 0 {
		fmt.Fprint(w, htmlBannerRaw("warn", `No groups configured. <a href="/dash/groups/new">Create your first group.</a>`))
	}

	if operator {
		fmt.Fprint(w, d.answeringSection(r.Context()))

		counts, err := d.sessionCountsViaRoutd(r.Context())
		if err != nil {
			// An unreachable owner is an ERROR and never empty data (spec 6/29):
			// an absent session table reads as "no sessions", which is a false
			// statement an operator acts on. The Answering section directly above
			// renders the same failure as a banner, and one page cannot hold two
			// answers to "what does a dead routd look like" (BUGS L325).
			slog.Warn("status: session counts via routd", "err", err)
			fmt.Fprint(w, htmlBanner("err", "could not reach routd for session counts"))
		} else {
			var sessionRows [][]string
			var total int
			for _, c := range counts {
				fp := folderPath(c.Folder)
				link := fmt.Sprintf(`<a href="/dash/groups/%s">%s</a>`, esc(fp), esc(c.Folder))
				sessionRows = append(sessionRows, []string{link, fmt.Sprintf("%d", c.Count)})
				total += c.Count
			}
			if len(sessionRows) > 0 {
				fmt.Fprintf(w, `<h2>Active sessions <span class="dim" style="font-weight:normal">%d total</span></h2>`, total)
				fmt.Fprint(w, htmlTable([]string{"group", "sessions"}, sessionRows))
			}
		}
	}

	pageClose(w, r)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (d *dash) handleTasks(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	pageTopFor(w, r, "Tasks")
	fmt.Fprint(w, `<p class="dim">Scheduled jobs. Auto-refreshes every 10s.</p>`)
	fmt.Fprint(w, `<table hx-get="/dash/tasks/x/list" hx-trigger="every 10s" hx-target="tbody" hx-swap="innerHTML">`+
		`<thead><tr><th>ID</th><th>Group</th><th>Prompt</th><th>Cron</th><th>Status</th><th>Created</th><th>Next Run</th></tr></thead>`+
		`<tbody>`)
	d.writeTaskRows(r.Context(), w, allowed, operator)
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
	d.writeTaskRows(r.Context(), w, allowed, operator)
}

func (d *dash) writeTaskRows(ctx context.Context, w http.ResponseWriter, allowed []string, operator bool) {
	// routd owns scheduled_tasks; the page asks rather than reads its file
	// (6/28). dashd's service token carries an empty folder claim, which routd
	// reads as list-all and does not cap — so the visibility filter and the
	// display window both apply HERE, after the full set is in hand. That
	// ordering is the point: filtering after a SQL `LIMIT 500` dropped a scoped
	// user's tasks whenever 500 other-owner rows sorted ahead (the reason the
	// old query pushed the scope into its WHERE).
	tasks, err := d.tasksViaRoutd(ctx)
	if err != nil {
		slog.Warn("tasks: read via routd", "err", err)
		fmt.Fprint(w, `<tr><td colspan=7 class="empty">could not reach routd for the task list</td></tr>`)
		return
	}
	var n int
	for _, t := range tasks {
		if !visible(allowed, operator, t.Owner) {
			continue
		}
		if n >= taskPageSize {
			break
		}
		dot := "dot-ok"
		if t.Status != "active" {
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
			esc(t.ID), esc(t.ID),
			esc(t.Owner),
			esc(t.Prompt), esc(truncate64(t.Prompt)),
			esc(t.Cron),
			dot, esc(t.Status),
			abbrTS(t.Created),
			abbrTS(t.NextRun),
		)
		n++
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=7 class="empty">No scheduled tasks. `+
			`Ask the agent to schedule one (e.g. "remind me every morning at 8am").</td></tr>`)
	}
}

// taskPageSize bounds the rendered task table. It is a DISPLAY window applied
// after the visibility filter, never a fetch cap applied before it.
const taskPageSize = 500

// activityPageSize is the rendered window for one activity page.
const activityPageSize = 50

func (d *dash) handleActivity(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	before := strings.TrimSpace(r.URL.Query().Get("before"))
	pageTopFor(w, r, "Activity")
	fmt.Fprint(w, `<p class="dim">Last 50 messages across all channels.</p>`)
	// Auto-refresh only the live (first) page: polling an "older" page back to
	// the newest 50 would silently discard the operator's pagination.
	tableAttrs := ""
	if before == "" {
		tableAttrs = ` hx-get="/dash/activity/x/recent" hx-trigger="every 10s" hx-target="tbody" hx-swap="innerHTML"`
	}
	fmt.Fprintf(w, `<table%s>`+
		`<thead><tr><th>Time</th><th>Source</th><th>Chat</th><th>Sender</th><th>Verb</th><th>Content</th></tr></thead>`+
		`<tbody>`, tableAttrs)
	older := d.writeActivityRows(r.Context(), w, allowed, operator, before)
	fmt.Fprint(w, `</tbody></table>`)
	if older != "" {
		fmt.Fprintf(w, `<p><a class="btn" href="/dash/activity/?before=%s">older &rarr;</a></p>`, url.QueryEscape(older))
	}
	pageClose(w, r)
}

func (d *dash) handleActivityPartial(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.writeActivityRows(r.Context(), w, allowed, operator, "")
}

// writeActivityRows renders up to activityPageSize rows older than the before
// cursor (a raw timestamp string; empty = newest). It returns the cursor for
// the next "older" page, or "" when there is nothing further back.
func (d *dash) writeActivityRows(ctx context.Context, w http.ResponseWriter, allowed []string, operator bool, before string) string {
	if d.routdStore() == nil {
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">backend unavailable</td></tr>`)
		return ""
	}
	// Non-operators may filter out most of the newest page; over-fetch so a
	// scoped view still fills the table. Operators read one past the page size
	// so we can tell "exactly 50" from "more behind it".
	limit := activityPageSize + 1
	if !operator {
		limit = 1000
	}
	// routd owns `messages` and truncates the preview itself, so this page can
	// never widen it (6/28 read path, BUGS L142). The per-row folder visibility
	// below is still dashd's job — routd's limit is the outer bound.
	feed, err := d.activityViaRoutd(ctx, before, limit)
	if err != nil {
		slog.Warn("activity: read via routd", "err", err)
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">could not reach routd for activity</td></tr>`)
		return ""
	}
	var n int
	var lastTS string
	var more bool
	folderOf := map[string]string{} // memoize chat_jid → folder across rows
	for _, m := range feed {
		if n >= activityPageSize {
			more = true
			break
		}
		ts, source, chatJID, sender, content := m.Timestamp, m.Source, m.ChatJID, m.Sender, m.Preview
		verb := m.Verb
		folder, seen := folderOf[chatJID]
		if !seen {
			folder = d.jidFolder(chatJID)
			folderOf[chatJID] = folder
		}
		if !operator && !visible(allowed, operator, folder) {
			continue
		}
		chatCell := `<code>` + esc(chatJID) + `</code>`
		if folder != "" {
			chatCell = `<a href="/dash/groups/` + esc(folderPath(folder)) + `">` + chatCell + `</a>`
		}
		fmt.Fprintf(w, `<tr><td><abbr title="%s">%s</abbr></td><td>%s</td><td>%s</td><td><code>%s</code></td><td>%s</td><td>%s</td></tr>`,
			esc(ts), esc(relativeTS(ts)),
			esc(source),
			chatCell,
			esc(sender),
			esc(verb),
			esc(content),
		)
		lastTS = ts
		n++
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">No messages yet.</td></tr>`)
		return ""
	}
	if !more {
		return ""
	}
	return lastTS
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
	pageTopFor(w, r, "Groups")
	fmt.Fprint(w, `<p class="dim">Group hierarchy. Expand a row to see routing rules and links.</p>`+
		`<p><a href="/dash/groups/new">+ New group</a></p>`)

	if d.routdStore() == nil {
		fmt.Fprint(w, htmlBanner("err", "backend unavailable"))
		pageClose(w, r)
		return
	}
	all, err := d.foldersViaRoutd(r.Context())
	if err != nil {
		slog.Warn("groups: read via routd", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach routd for the group list"))
		pageClose(w, r)
		return
	}
	var folders []string
	for _, folder := range all {
		if !visible(allowed, operator, folder) {
			continue
		}
		folders = append(folders, folder)
	}
	// Bulk-fetch usage in one pass; fall through with empty map on error.
	type usageKey struct {
		tokens7d, cents7d, msgCount int
		lastActive                  string
	}
	usageMap := map[string]usageKey{}
	if len(folders) > 0 && d.routdStore() != nil {
		// The metered flag is the usage page's concern: this listing shows a
		// spend figure only when there is one, so an unmetered instance simply
		// omits it rather than needing to say so per row.
		if summaries, _, err := d.groupUsageBulk(r.Context(), folders); err != nil {
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
			usageParts += ` &middot; last <abbr title="` + esc(u.lastActive) + `">` + relativeTS(u.lastActive) + `</abbr>`
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
	routes, err := d.ds().RoutesIntoFolder(folder)
	if err != nil {
		slog.Warn("groups: routes query", "err", err, "folder", folder)
		fmt.Fprint(w, htmlBanner("err", "routes error: "+err.Error()))
		return
	}
	if len(routes) == 0 {
		fmt.Fprint(w, `<p class="empty">no routes targeting this group</p>`)
		return
	}
	fmt.Fprint(w, `<table><thead><tr><th>Seq</th><th>Match</th><th>Target</th></tr></thead><tbody>`)
	for _, rt := range routes {
		fmt.Fprintf(w, `<tr><td>%d</td><td><code>%s</code></td><td><code>%s</code></td></tr>`,
			rt.Seq,
			esc(rt.Match),
			esc(rt.Target),
		)
	}
	fmt.Fprint(w, `</tbody></table>`)
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

// parseMemoryPath splits /dash/memory/<folder>/<rel> into (folder, rel) where folder
// may be multi-segment. Splitting on the first '/' misattributed nested folders, so
// instead scan split points from the right and pick the one whose trailing part is
// an allowed memory path.
func parseMemoryPath(urlPath string) (string, string) {
	const prefix = "/dash/memory/"
	if !strings.HasPrefix(urlPath, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(urlPath, prefix)
	for i := strings.IndexByte(rest, '/'); i >= 0; i = indexByteFrom(rest, '/', i+1) {
		folder := rest[:i]
		rel := rest[i+1:]
		if folder != "" && memoryPathAllowed(rel) {
			return folder, rel
		}
	}
	return "", ""
}

// indexByteFrom returns the index of c in s at or after start, or -1.
func indexByteFrom(s string, c byte, start int) int {
	if start > len(s) {
		return -1
	}
	if j := strings.IndexByte(s[start:], c); j >= 0 {
		return start + j
	}
	return -1
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
	selectedGroup := r.URL.Query().Get("group")

	// Refuse to render another tenant's files: a non-operator who hand-crafts
	// ?group=foreign gets 403 before any file read.
	if selectedGroup != "" && !visible(allowed, operator, selectedGroup) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	pageTopFor(w, r, "Memory")
	fmt.Fprint(w, `<p class="dim">Browse per-group MEMORY.md, CLAUDE.md, diary, episodes, users, facts.</p>`)

	if d.routdStore() == nil {
		fmt.Fprint(w, htmlBanner("err", "backend unavailable"))
		pageClose(w, r)
		return
	}
	folders, err := d.foldersViaRoutd(r.Context())
	if err != nil {
		slog.Warn("memory: read groups via routd", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach routd for the group list"))
	} else {
		fmt.Fprint(w, `<form method="get" class="form-narrow">
<label class="dim" for="group">Group</label>
<select id="group" name="group" onchange="this.form.submit()">
<option value="">— select a group —</option>`)
		for _, folder := range folders {
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

	fmt.Fprintf(w, `<p><a class="btn btn-secondary" href="/dav/%s/" target="_blank">Open workspace ↗</a></p>`,
		esc(folder))

	var memBuf strings.Builder
	renderCappedFile(&memBuf, groupDir, "MEMORY.md", true)
	fmt.Fprint(w, htmlSection("MEMORY.md", memBuf.String()))

	var personaBuf strings.Builder
	renderCappedFile(&personaBuf, groupDir, "PERSONA.md", false)
	if s := personaBuf.String(); s != "" {
		fmt.Fprint(w, s)
	}

	var claudeBuf strings.Builder
	renderCappedFile(&claudeBuf, groupDir, "CLAUDE.md", false)
	if s := claudeBuf.String(); s != "" {
		fmt.Fprint(w, s)
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
	limit := min(total, maxDirEntries)
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
	for l := range strings.SplitSeq(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && l != "---" {
			return l
		}
	}
	return ""
}
