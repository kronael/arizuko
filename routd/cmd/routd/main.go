// Command routd runs the routd daemon: the conversation state machine —
// routing rules + the message/event store + the orchestration loop +
// channel ingress/egress. routd is the SOLE appender of messages and a
// token VERIFIER, not a signer (spec 5/E). It owns routd.db and calls
// runed (POST /v1/runs) to execute turns.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/auth/surrogate"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/routd"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func main() {
	defer obs.Setup("routd", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("routd", os.Getenv("ARIZUKO_INSTANCE"))()

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		slog.Error("DATA_DIR env required")
		os.Exit(1)
	}
	listenAddr := core.EnvOr("LISTEN_ADDR", ":8080")
	authdURL := os.Getenv("AUTHD_URL")
	runedURL := core.EnvOr("RUNED_URL", "http://runed:8080")
	onbodURL := os.Getenv("ONBOD_URL")
	webHost := os.Getenv("WEB_HOST")

	// routd.Open is the door: it dials the instance's store server and makes
	// routd's entities there.
	db, err := routd.Open()
	if err != nil {
		slog.Error("open routd's store", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	// The ranked message index is this process's: it is memory, built from the
	// store, and only the process that answers `find_messages` needs it.
	db.StartSearch()

	// audit_log sink: routd owns audit_log (migration 0016), so its own
	// ACL/secret/task mutations emit through the audited store variants. Init wires
	// the instance name onto those rows, and it takes whichever handle routd was
	// opened over so the sink follows the store.
	audit.Init(db.Obj(), os.Getenv("ARIZUKO_INSTANCE"))

	// SECRETS_KEY keyring: routd OWNS secrets in routd.db. The key is REQUIRED —
	// routd refuses to start without it. An earlier build only logged a warning and
	// started, so an un-keyed instance persisted every secret readable on disk while
	// each healthcheck reported it healthy (BUGS S2). Comma-separate to rotate.
	kr, err := core.RequireSecretKeyring(os.Getenv("SECRETS_KEY"))
	if err != nil {
		slog.Error("secrets keyring", "err", err)
		os.Exit(1)
	}
	db.SetSecretKeys(kr...)

	// Surrogate OAuth (spec 5/15): the broker refreshes near-expiry capability
	// credentials at call time. Always wired — refresh only fires for user rows
	// carrying provider metadata, so an unconfigured instance is inert.
	if eng, err := surrogate.NewEngine(dataDir); err != nil {
		slog.Error("build surrogate engine", "err", err)
		os.Exit(1)
	} else {
		db.SetSurrogate(eng)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Verifier: offline-verify bearer tokens against authd's JWKs. routd
	// holds no signing key (spec 5/E § Auth). Unset AUTHD_URL → no
	// verifier (single-tenant / local-dev); the Server treats nil as open.
	var verify routd.Verifier
	// dashKeys arms the /dash/routd/ operator gate from the SAME keyset the /v1
	// verifier uses; nil (no AUTHD_URL) leaves the dashboard open in local dev.
	var dashKeys *auth.KeySet
	if authdURL != "" {
		ks, kerr := auth.FetchKeys(ctx, authdURL)
		if kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
		verify = keysetVerifier{ks: ks}
		dashKeys = ks
	}

	// runed client: routd exchanges AUTHD_SERVICE_KEY for an authd-minted
	// service:routd ES256 token at boot and refreshes it; authd's issuer-pin would
	// 401 the static ROUTD_SERVICE_TOKEN. When AUTHD_URL or AUTHD_SERVICE_KEY is
	// unset, fall back to the static env for local dev.
	runTimeout := core.DurOr("RUNED_RUN_TIMEOUT", 20*time.Minute)
	// routd must OUT-WAIT runed's kill-deadline. runed kills the container at
	// RUNED_RUN_TIMEOUT, so routd's dispatch deadline gets a grace margin on top. If
	// they were equal, routd's deadline could fire first → transport error →
	// turn_context stays `running` → the next poll re-feeds the SAME turn into a
	// second container, so the user's message executes twice.
	runedWait := runTimeout + 2*time.Minute
	var runedClient *runedv1.Client
	// identity resolves a sender sub → canonical identity at authd's
	// GET /v1/identities/{sub} (authd OWNS identity — spec 5/9), reusing the same
	// service:routd token source as the runed client. nil when authd/the service
	// key is unwired → inspect_identity answers unclaimed (auth-only deployment).
	var identity routd.IdentityResolver
	// onbod federates the /invite + /gate slash commands (onbod OWNS invites +
	// onboarding_gates — spec 5/5), reusing the same service:routd token source.
	// nil when ONBOD_URL or the service key is unwired → the commands report the
	// federation gap, exactly as the pre-federation stubs did.
	var onbod routd.OnbodClient
	// svcSrc is the service:routd token source, reused for the adapter-egress
	// Bearer (chanreg) too: routd→adapter calls (/send, /files, /v1/pane, …)
	// present this ES256 token (spec 5/1; no CHANNEL_SECRET remains). nil → local
	// dev (no AUTHD_URL); egress goes out unauthenticated.
	var svcSrc func(context.Context) (string, error)
	svcKey := os.Getenv("AUTHD_SERVICE_KEY")
	if ts, err := auth.ServiceToken(authdURL, "routd", svcKey); err == nil {
		svcSrc = ts.Token
		runedClient = runedv1.NewClientWithSource(runedURL, ts.Token, runedWait)
		identity = routd.NewIdentityResolver(authdURL, ts.Token)
		onbod = routd.NewOnbodClient(onbodURL, ts.Token)
		slog.Info("routd service-token bootstrap via authd", "authd", authdURL)
	} else {
		// Split wires authd + a key; a failed exchange there means every federated
		// call (runed/authd/onbod) will 401 and the static fallback is NOT compose-
		// provisioned — fail loud instead of looking healthy until the first call.
		if authdURL != "" && svcKey != "" {
			slog.Error("routd service-token exchange FAILED though AUTHD_SERVICE_KEY is set; "+
				"federated calls will 401 — check authd reachability + key", "authd", authdURL, "err", err)
		}
		runedClient = runedv1.NewClient(runedURL, os.Getenv("ROUTD_SERVICE_TOKEN"), runedWait)
	}

	// Channel plane: adapters register their egress URL + owned jid prefixes; the
	// Deliverer resolves them on the way out (latest inbound source → registry
	// prefix match). In-memory registry — adapters re-register on routd restart.
	reg := chanreg.New()
	reg.SetBearer(svcSrc) // service:routd egress token; nil → local dev (no auth)
	deliver := routd.NewChannelDeliverer(
		reg, core.ParseCSV(os.Getenv("SEND_DISABLED_CHANNELS")), db.LatestSource)

	// The model gateway pair (spec 6/23). A URL with no master key mints no
	// key, and every turn would then fail at dispatch; refuse at boot instead,
	// the way runed refuses a half-configured egress substrate.
	gatewayURL := os.Getenv("MODEL_GATEWAY_URL")
	gatewayKey := os.Getenv("MODEL_GATEWAY_KEY")
	if gatewayURL != "" && gatewayKey == "" {
		slog.Error("MODEL_GATEWAY_URL is set and MODEL_GATEWAY_KEY is not; "+
			"routd mints each turn's virtual key with that master key and can mint none without it",
			"gateway", gatewayURL)
		os.Exit(1)
	}

	loop := routd.NewLoop(db, runedClient, routd.LoopConfig{
		RunTimeout: runedWait,
		IpcDir:     filepath.Join(dataDir, "ipc"),
		// The SAME variable runed reads for its own ceiling. routd's queue
		// paces the work and runed admits it, so the two must agree: while
		// this was unwired, routd held a hardcoded 5 and an operator raising
		// MAX_CONCURRENT_CONTAINERS moved one cap and not the other (BUGS F71).
		MaxRuns:   core.EnvInt("MAX_CONCURRENT_CONTAINERS", 5),
		Deliver:   deliver,
		Proactive: routd.LoadProactiveConfig(os.Getenv),
		GroupsDir: filepath.Join(dataDir, "groups"),
		// Auto-migrate source root (ant/skills/self/MIGRATION_VERSION lives
		// under it); APP_SRC_DIR falls back to HOST_APP_DIR (core.LoadConfig).
		AppSrcDir: core.EnvOr("APP_SRC_DIR", os.Getenv("HOST_APP_DIR")),
		// Prompt envelope (buildAgentPrompt).
		InstanceName:          core.EnvOr("ASSISTANT_NAME", "Andy"),
		ObserveWindowMessages: core.EnvInt("OBSERVE_WINDOW_MESSAGES", 10),
		ObserveWindowChars:    core.EnvInt("OBSERVE_WINDOW_CHARS", 4000),
		// Pre-spawn budget gate (spec 11/19, 6/39); default-on. Off clears the
		// gateway's budgets too, so the escape hatch reaches what enforces them.
		CostCapsEnabled: core.EnvOr("COST_CAPS_ENABLED", "true") == "true",
		// Model gateway (spec 6/23, 6/39). With a URL set, routd mints one
		// virtual key per turn against the master key below, and the gateway is
		// the ledger every spend surface reads. Unset leaves the model
		// credential in the container and there is no meter at all.
		GatewayURL: gatewayURL,
		GatewayKey: gatewayKey,
		// Spawn-time stale-session reset threshold (default 2 days).
		// SESSION_IDLE_EXPIRY overrides.
		SessionIdle: core.DurOr("SESSION_IDLE_EXPIRY", 0),
		// Chat-initiated onboarding (ONBOARDING_ENABLED / ONBOARDING_PLATFORMS).
		// A route miss federates an onboarding insert to onbod when enabled.
		OnboardingEnabled:   core.EnvOr("ONBOARDING_ENABLED", "false") == "true",
		OnboardingPlatforms: core.ParseCSV(os.Getenv("ONBOARDING_PLATFORMS")),
		// Instance-wide default model: a group with no per-group model falls
		// back to this. No vendor default. The seam says model is a BACKEND-NATIVE id and
		// that absent means the harness picks its own (ant/src/backend.ts:29).
		// A hardcoded claude id made "absent" unexpressible, so switching
		// ARIZUKO_BACKEND to codex sent it a claude model and every turn 400d
		// (BUGS L129). Empty reaches ant as falsy, and each CLI defaults itself.
		DefaultModel: core.EnvOr("ARIZUKO_DEFAULT_MODEL", ""),
		// Turn retry on SIGKILL/OOM/timeout (spec 5/12). Default 3.
		MaxTurnRetry: core.EnvInt("MAX_TURN_RETRY", 3),
		// Transient delivery failures a pending bot row absorbs before the
		// outbound sweep dead-letters it (spec 5/34). Default 10.
		MaxOutboundRetry: core.EnvInt("MAX_OUTBOUND_RETRY", 10),
		// Inbound media enrichment (download + Whisper transcription); unset
		// MEDIA_ENABLED leaves it off. The MEDIA_* and WHISPER_MODEL defaults
		// mirror core.LoadConfig, which runed reads for the agent's media config.
		Media: routd.MediaConfig(
			core.EnvOr("MEDIA_ENABLED", "false") == "true",
			int64(core.EnvInt("MEDIA_MAX_FILE_BYTES", 20*1024*1024)),
			core.EnvOr("WHISPER_BASE_URL", "http://localhost:8080"),
			core.EnvOr("WHISPER_MODEL", "turbo"),
			core.EnvOr("VOICE_TRANSCRIPTION_ENABLED", "false") == "true",
			core.EnvOr("VIDEO_TRANSCRIPTION_ENABLED", "false") == "true",
			svcSrc, // service:routd token for the adapter /files download
		),
	})

	loop.SetOnbodClient(onbod)
	srv := routd.NewServer(db, loop, deliver, verify, core.DurOr("ENGAGEMENT_TTL", routd.DefaultEngagementTTL), webHost)
	srv.SetDashKeySet(dashKeys)
	srv.SetIdentityResolver(identity)
	// session_log run history federated from runed (runed OWNS it — spec 5/P):
	// reuse routd's existing runed client, no new auth wiring. nil client →
	// the new_session hint / inspect_session render "no prior session".
	srv.SetSessionResolver(routd.NewSessionResolver(runedClient))
	// Close the Loop↔Server cycle and supply the dirs the in-process MCP
	// file-path tools resolve against (web dir = dataDir/web, per core.Config).
	loop.BindServer(srv)
	srv.SetDirs(filepath.Join(dataDir, "groups"), filepath.Join(dataDir, "web"))
	// Mirror proxyd's vhost config so get_web_presence can report a folder's
	// derived (<folder>.<HOSTING_DOMAIN>) / aliased canonical host (spec 5/V).
	srv.SetVhosts(
		strings.ToLower(strings.TrimSuffix(os.Getenv("HOSTING_DOMAIN"), ".")),
		groupfolder.ParseVhostAliases(os.Getenv("WEB_VHOST_ALIASES")),
	)
	// SEND_DISABLED_GROUPS: muted folders persist outbound but don't deliver it
	// (Server.mutedGroup). SEND_DISABLED_CHANNELS (jid-prefix) stays in the
	// Deliverer; this is the group-folder mute applied in appendAndDeliver.
	srv.SetDisabledGroups(core.ParseCSV(os.Getenv("SEND_DISABLED_GROUPS")))
	// send_voice synthesis config (TTS_* env); unset TTS_ENABLED leaves voice
	// off. Cache lives under DATA_DIR/tts.
	srv.SetTTS(routd.TTSConfig(
		core.EnvOr("TTS_ENABLED", "false") == "true",
		core.EnvOr("TTS_BASE_URL", "http://ttsd:8880"),
		core.EnvOr("TTS_VOICE", "af_bella"),
		core.EnvOr("TTS_MODEL", "kokoro"),
		core.DurOr("TTS_TIMEOUT", 15*time.Second),
		filepath.Join(dataDir, "tts"),
	))
	srv.SetChannelRegistry(reg)
	// Audit writer for mutating MCP tool calls (GatedFns.Audit). Writes
	// audit-system.jl in DATA_DIR — observability only, never the routd.db
	// audit_log table (audit.Init above owns that). AUDIT_ENABLED unset → noop.
	srv.SetAudit(audit.New(audit.LoadConfig(dataDir, os.Getenv("ARIZUKO_INSTANCE"))))
	// MCP connectors (spec 5/13 § connectors.toml): load <DATA_DIR>/connectors.toml
	// (or $CONNECTORS_TOML) and hand the specs to every per-turn MCP socket, which
	// discovers each catalog with the folder's broker-resolved credential. Nothing
	// is dialled here: boot has no folder, so it has no token. Missing file is
	// fine; a bad file is a boot failure (fail-fast).
	if conns, cerr := routd.LoadConnectors(dataDir); cerr != nil {
		slog.Error("connectors: load failed", "err", cerr)
		os.Exit(1)
	} else if len(conns) > 0 {
		srv.SetConnectors(conns)
		slog.Info("connectors loaded", "connectors", len(conns))
	}
	// The rest half of the same file, on the same fail-fast terms: a rejected
	// block means the agent gets NO ext tools, and a log line the operator never
	// reads is not a report. Refusing to boot is how the operator hears it.
	if extTools, eerr := routd.LoadExtProviders(ctx, dataDir); eerr != nil {
		slog.Error("ext providers: load failed", "err", eerr)
		os.Exit(1)
	} else if len(extTools) > 0 {
		srv.SetExtTools(extTools)
	}
	reg.StartHealthLoop(ctx)
	mux := srv.Handler().(*http.ServeMux)
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("routd", mux))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}

	httpd := &http.Server{Addr: listenAddr, Handler: obs.HTTPMiddleware("routd")(mux)}
	// Register the signal handler BEFORE the listener accepts: a SIGTERM in
	// between takes Go's default disposition and kills the process, so the
	// Shutdown below never runs (BUGS L127).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("routd started", "addr", listenAddr, "db", dataDir, "runed", runedURL)
		if err := httpd.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()
	go loop.Run(ctx)

	<-stop
	slog.Info("routd stopping")
	cancel()
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = httpd.Shutdown(sctx)

	// Wait for the poll loop before the deferred db.Close() runs (BUGS L160).
	// On a timeout, exit WITHOUT closing: an unclosed SQLite WAL is recovered on
	// the next open, a dropped inbound message is not.
	if !loop.Wait(loopStopTimeout) {
		slog.Warn("poll loop still running at shutdown; exiting without closing the DB",
			"timeout", loopStopTimeout)
		os.Exit(0)
	}
}

// loopStopTimeout bounds the shutdown wait for the poll loop.
const loopStopTimeout = 10 * time.Second

// keysetVerifier adapts auth.FetchKeys → routd.Verifier (offline JWT
// verify). routd is a verifier, not a signer.
type keysetVerifier struct{ ks *auth.KeySet }

func (v keysetVerifier) Verify(r *http.Request) (sub string, scope []string, folder string, err error) {
	s, verr := auth.VerifyHTTP(r, v.ks)
	if verr != nil {
		return "", nil, "", verr
	}
	return s.Sub, s.Scope, s.Extra["arz/folder"], nil
}
