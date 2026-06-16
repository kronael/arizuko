package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/core"
)

// containerDataMount is the container-side path where HOST_DATA_DIR is mounted.
const containerDataMount = "/srv/app/home"

// containerSrcMount is the container-side path where HOST_APP_DIR is mounted.
const containerSrcMount = "/srv/app/arizuko"

// routerSvc is the canonical router service name. The split (authd/routd/runed)
// is the only topology — gated is gone — so the router is always routd. Every
// depends_on default and the host-published API port follow this.
func routerSvc(map[string]string) string {
	return "routd"
}

// routerURL is the in-network base URL of the canonical router. webd/onbod and
// any adapter ROUTER_URL is pinned to this in the generated compose.
func routerURL(env map[string]string) string {
	return "http://" + routerSvc(env) + ":8080"
}

// dockerGID returns the gid that owns /var/run/docker.sock, or 999 fallback.
// runed must be in this group to spawn agent
// containers as uid 1000.
func dockerGID() int {
	var st syscall.Stat_t
	if err := syscall.Stat("/var/run/docker.sock", &st); err == nil {
		return int(st.Gid)
	}
	return 999
}

// provisionServiceKey returns the AUTHD_SERVICE_KEY for a daemon, reusing the
// value already persisted in env/<daemon>.env (so a redeploy keeps the same
// service identity) and minting a fresh random hex key on first generate.
func provisionServiceKey(dataDir, daemon string, env map[string]string) string {
	if existing := readEnvFileKey(filepath.Join(dataDir, "env", daemon+".env"), "AUTHD_SERVICE_KEY"); existing != "" {
		return existing
	}
	return core.GenHexToken()
}

// readEnvFileKey scans a KEY=VALUE env file for one key. Empty on miss/error —
// callers treat that as "generate fresh".
func readEnvFileKey(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// imageRefRE constrains docker image references to alnum, dots, colons,
// slashes, underscores, dashes, and @ (digest). No whitespace, no
// newlines — prevents YAML injection through services/*.toml `image`.
var imageRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,254}$`)

// identRE matches safe identifiers for container_name components, service
// names, and entrypoint binary names.
var identRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`)

// Per-daemon env scoping: each known daemon gets env/<daemon>.env containing
// only the vars it needs. Unknown services (custom services/*.toml) fall back
// to the shared .env. Secrets do not leak across daemons.
//
// commonKeys flow into every arizuko daemon env file.
var commonKeys = []string{
	"ASSISTANT_NAME", "TZ", "LOG_LEVEL", "ARIZUKO_DEV",
	"HOST_DATA_DIR", "HOST_APP_DIR", "WEB_HOST",
	"API_PORT",
	// AUTHD_SERVICE_NAME: the daemon's service-token exchange principal, set per
	// daemon by wireServiceKey. Here so it reaches every wired adapter's env file
	// (the adapter exchanges as this, not its CHANNEL_NAME — see chanlib/run.go).
	"AUTHD_SERVICE_NAME",
	// OTLP export (spec 5/O). Unset -> stock JSON handler, zero overhead.
	"ARIZUKO_INSTANCE",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_HEADERS",
	"OTEL_RESOURCE_ATTRIBUTES",
	// DATA_DIR is NOT in commonKeys: that would leak the host path
	// into env/<daemon>.env and into the container. DATA_DIR is the
	// container-internal mount point, set per-daemon in the compose
	// `environment:` block.
}

// daemonKeys: per-daemon secrets + config. Unlisted keys never reach the daemon.
var daemonKeys = map[string][]string{
	// timed: scheduler. Federates the fire loop over routd (ROUTER_URL) with a
	// service:timed token exchanged from AUTHD_SERVICE_KEY at AUTHD_URL.
	"timed": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "ROUTER_URL"},
	// authd: auth authority. auth.db only; no message DB, no docker, no crackbox.
	// Serves JWKS; seeded with the service keys (AUTHD_SERVICE_KEYS, hashed at
	// compare time inside authd) + OAuth provider config.
	"authd": {
		"AUTH_SECRET", "AUTH_BASE_URL",
		"AUTHD_SERVICE_KEY", "AUTHD_SERVICE_KEYS", "GRANTS_URL",
		"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_ALLOWED_ORG",
		"DISCORD_CLIENT_ID", "DISCORD_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ALLOWED_EMAILS",
	},
	// routd: conversation state. routd.db + adapters (/v1/send) + calls runed.
	// Verifies via authd JWKS. NO crackbox, NO docker socket.
	"routd": {
		"AUTHD_URL", "AUTHD_SERVICE_KEY", "ONBOD_URL",
		"OBSERVE_WINDOW_MESSAGES", "OBSERVE_WINDOW_CHARS",
		"SEND_DISABLED_CHANNELS", "SEND_DISABLED_GROUPS",
		// get_web_presence reports a folder's derived/aliased canonical host
		// (spec 5/V) — routd reads the same vhost env proxyd does, to report.
		"HOSTING_DOMAIN", "WEB_VHOST_ALIASES",
		// routd OWNS secrets in the split (routd 0008) → needs the key to
		// decrypt connector/scoped secrets. Without it: "SECRETS_KEY unset".
		"SECRETS_KEY",
		// Instance-wide fallback model when a group has no per-group model.
		"ARIZUKO_DEFAULT_MODEL",
		// Inbound media enrichment (download + Whisper transcription) and outbound
		// send_voice (TTS) run IN routd (routd/enrich.go, routd/tts.go). The split
		// lifted these out of gated but left the env only on runed (which uses a
		// SUBSET for the agent's in-container [media] config, container/runner.go),
		// so routd defaulted MEDIA_ENABLED off → every inbound attachment silently
		// dropped + send_voice disabled (krons poj.zip, 2026-06-10). Both daemons
		// need them: routd to download/transcribe/synthesize, runed for the agent's
		// own media settings — so these are duplicated, not moved.
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES",
		"WHISPER_BASE_URL", "WHISPER_MODEL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED",
		"TTS_ENABLED", "TTS_BASE_URL", "TTS_VOICE", "TTS_MODEL", "TTS_TIMEOUT",
	},
	// runed: execution plane. The ONLY daemon wired to docker.sock +
	// crackbox + the per-folder agent networks.
	"runed": {
		"AUTHD_URL", "AUTHD_SERVICE_KEY",
		"CONTAINER_IMAGE", "CONTAINER_TIMEOUT",
		// RUNED_RUN_TIMEOUT bounds the run: the container hard-kill AND
		// (minus 30s) the agent query timeout (spec, runed/README). Default 20m.
		"RUNED_RUN_TIMEOUT",
		"IDLE_TIMEOUT", "MAX_CONCURRENT_CONTAINERS",
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES", "WHISPER_BASE_URL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED", "WHISPER_MODEL",
		"TTS_ENABLED", "TTS_BASE_URL", "TTS_VOICE", "TTS_MODEL", "TTS_TIMEOUT",
		"EGRESS_SUBNET", "EGRESS_NETWORK_PREFIX", "EGRESS_CRACKBOX",
		"CRACKBOX_ADMIN_API", "CRACKBOX_PROXY_URL", "CRACKBOX_ADMIN_SECRET",
		"HOST_CODEX_DIR",
		// runed spawns the agent + injects folder secrets (decrypted with the
		// key) into the container env — same role gated had in the monolith.
		"SECRETS_KEY",
	},
	"onbod": {
		"AUTH_SECRET", "AUTH_BASE_URL",
		"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_ALLOWED_ORG",
		"DISCORD_CLIENT_ID", "DISCORD_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ALLOWED_EMAILS",
		"ONBOARDING_ENABLED", "ONBOARDING_PLATFORMS",
		"ONBOARDING_PROTOTYPE", "ONBOARDING_GREETING",
		"ONBOARD_POLL_INTERVAL", "ONBOD_LISTEN_ADDR",
		// service:onbod token for routd /v1/outbound (spec 5/1).
		"AUTHD_URL", "AUTHD_SERVICE_KEY",
	},
	// dashd VERIFIES proxyd's ES256 transit bearer against authd's JWKS (AUTHD_URL)
	// AND presents its own service:dashd token on the whapd re-pair proxy
	// (AUTHD_SERVICE_KEY).
	"dashd": {"AUTH_SECRET", "DASH_PORT", "WHAPD_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY"},
	// webd + proxyd present a service:<daemon> ES256 token as the channel proof
	// for the X-User-* headers they forward: proxyd→backends, webd→proxyd
	// /v1/routes + webd→routd register. Exchanged from AUTHD_SERVICE_KEY at AUTHD_URL.
	"webd":   {"AUTH_SECRET", "AUTH_BASE_URL", "ROUTER_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"proxyd": {"AUTH_SECRET", "AUTH_BASE_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY", "HOSTING_DOMAIN", "WEB_VHOST_ALIASES"},
	// Channel adapters: AUTHD_URL + AUTHD_SERVICE_KEY let each exchange a
	// service:<adapter> JWT presented on EVERY routd call — register, /v1/messages,
	// /v1/pane (spec 5/1; no CHANNEL_SECRET remains).
	"teled":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "TELEGRAM_BOT_TOKEN"},
	"discd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "DISCORD_BOT_TOKEN"},
	"mastd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "MASTODON_ACCESS_TOKEN", "MASTODON_INSTANCE"},
	"bskyd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "BLUESKY_HANDLE", "BLUESKY_APP_PASSWORD"},
	"reditd":   {"AUTHD_URL", "AUTHD_SERVICE_KEY", "REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "REDDIT_USERNAME", "REDDIT_PASSWORD"},
	"slakd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "SLAKD_USERS_CACHE_TTL"},
	"linkd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET", "LINKEDIN_ACCESS_TOKEN", "LINKEDIN_REFRESH_TOKEN"},
	"emaid":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "EMAIL_IMAP_HOST", "EMAIL_SMTP_HOST", "EMAIL_ACCOUNT", "EMAIL_PASSWORD", "EMAIL_IMAP_PORT", "EMAIL_SMTP_PORT"},
	"whapd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"twitd":    {"AUTHD_URL", "AUTHD_SERVICE_KEY", "TWITTER_USERNAME", "TWITTER_PASSWORD", "TWITTER_EMAIL", "TWITTER_2FA_SECRET", "TWITTER_POLL_INTERVAL"},
	"crackbox": {"CRACKBOX_PROXY_ADDR", "CRACKBOX_ADMIN_ADDR", "CRACKBOX_ADMIN_SECRET", "CRACKBOX_STATE_PATH"},
}

// adapterDaemons is the set of message-posting channel adapters. In the split
// each authenticates to routd's /v1/messages with a service:<adapter> JWT
// exchanged from its own AUTHD_SERVICE_KEY (spec 5/1), not the shared
// CHANNEL_SECRET. Non-message daemons (ttsd/davd/vited/crackbox/kokoro) are not
// here — they never post inbound. Multi-account adapters (`<adapter>-<label>`)
// resolve to the base adapter via envFileFor's `-` split, so they share the
// base principal. Keep in sync with authd serviceGrants.
var adapterDaemons = map[string]bool{
	"teled": true, "whapd": true, "discd": true, "mastd": true, "slakd": true,
	"bskyd": true, "reditd": true, "emaid": true, "twitd": true, "linkd": true,
}

// envFileFor returns the scoped env_file block for a daemon, falling back to
// the shared .env for services not in daemonKeys. Multi-account services
// named `<adapter>-<label>` (per specs/5/R-multi-account.md) share the base
// adapter's env file so per-daemon scoping holds across accounts.
func envFileFor(name string) string {
	if _, ok := daemonKeys[name]; ok {
		return fmt.Sprintf("    env_file:\n      - env/%s.env\n", name)
	}
	if base, _, ok := strings.Cut(name, "-"); ok {
		if _, ok := daemonKeys[base]; ok {
			return fmt.Sprintf("    env_file:\n      - env/%s.env\n", base)
		}
	}
	return "    env_file:\n      - .env\n"
}

// writeEnvFiles emits env/<daemon>.env with only the keys each daemon needs.
// perDaemon holds keys scoped to a single daemon (e.g. each daemon's own
// AUTHD_SERVICE_KEY) that must NOT leak across the shared env map. Called from
// Generate before rendering compose; failure is non-fatal.
func writeEnvFiles(dataDir string, env map[string]string, perDaemon map[string]map[string]string) error {
	dir := filepath.Join(dataDir, "env")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for daemon, keys := range daemonKeys {
		all := append([]string{}, commonKeys...)
		all = append(all, keys...)
		sort.Strings(all)
		var b strings.Builder
		fmt.Fprintf(&b, "# Generated per-daemon env for %s. Do not edit by hand.\n", daemon)
		for _, k := range all {
			if v, ok := perDaemon[daemon][k]; ok && v != "" {
				fmt.Fprintf(&b, "%s=%s\n", k, v)
				continue
			}
			if v, ok := env[k]; ok && v != "" {
				fmt.Fprintf(&b, "%s=%s\n", k, v)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, daemon+".env"), []byte(b.String()), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// healthBlock: every Go daemon exposes /health on :8080 internally.
const healthBlock = "    healthcheck:\n" +
	"      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:8080/health']\n" +
	"      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 15s\n"

type ServiceConfig struct {
	Image        string            `toml:"image"`
	Entrypoint   []string          `toml:"entrypoint"`
	Restart      string            `toml:"restart"`
	DependsOn    []string          `toml:"depends_on"`
	Environment  map[string]string `toml:"environment"`
	Ports        []string          `toml:"ports"`
	Volumes      []string          `toml:"volumes"`
	Command      []string          `toml:"command"`
	ProxydRoutes []ProxydRoute     `toml:"proxyd_route"`
}

// ProxydRoute mirrors proxyd's Route shape. Each adapter TOML may declare
// [[proxyd_route]] blocks; compose collects survivors (after gated_by env
// evaluation) into PROXYD_ROUTES_JSON. JSON tags match proxyd/routes.go.
type ProxydRoute struct {
	Path            string   `toml:"path" json:"path"`
	Backend         string   `toml:"backend" json:"backend"`
	Auth            string   `toml:"auth" json:"auth"`
	GatedBy         string   `toml:"gated_by" json:"gated_by,omitempty"`
	PreserveHeaders []string `toml:"preserve_headers" json:"preserve_headers,omitempty"`
	StripPrefix     bool     `toml:"strip_prefix" json:"strip_prefix,omitempty"`
}

// coreProxydRoutes are the always-emitted routes for core daemons rendered
// directly by compose (dashd, webd, davd, onbod). Their backend URLs follow
// the unified-port convention (DNS name + :8080). GatedBy maps to env vars
// that toggle daemon emission so the route presence tracks the daemon.
var coreProxydRoutes = []ProxydRoute{
	// Federated cockpit: per-daemon /dash/<daemon>/ surfaces route to the
	// owning daemon. Longest-prefix matching (proxyd/routes.go:MatchRoute)
	// means these must come before the catch-all /dash/ → dashd route.
	{Path: "/dash/onbod/", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
	// /dash/ catch-all → dashd (hub + cross-cutting pages).
	{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
	// /chat/ — bespoke handler in proxyd (dispatchRouteToken). Token in
	// path → public; no token segment (operator panel moved to /panel/)
	// → handler routes by presence. Spec 5/W.
	{Path: "/chat/", Backend: "http://webd:8080", Auth: "public"},
	{Path: "/hook/", Backend: "http://webd:8080", Auth: "public"},
	{Path: "/panel/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/api/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/x/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/static/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/mcp", Backend: "http://webd:8080", Auth: "user"},
	// Legacy /slink/* → handled by webd as 301 → /chat/. Public.
	{Path: "/slink/", Backend: "http://webd:8080", Auth: "public"},
	{Path: "/dav/", Backend: "http://davd:8080", Auth: "user", StripPrefix: true, GatedBy: "WEBDAV_ENABLED"},
	{Path: "/onboard", Backend: "http://onbod:8080", Auth: "public", GatedBy: "ONBOARDING_ENABLED"},
	{Path: "/onboard/", Backend: "http://onbod:8080", Auth: "public", GatedBy: "ONBOARDING_ENABLED"},
	{Path: "/invite/", Backend: "http://onbod:8080", Auth: "public", GatedBy: "ONBOARDING_ENABLED"},
}

// gatedByOn reports whether the named env-gate is enabled. Empty key = no
// gate. Boolean gates require literal "true" (with per-key default for the
// unset case); other keys follow secret-style semantics where any non-empty
// value enables the route.
func gatedByOn(env map[string]string, key string) bool {
	switch key {
	case "":
		return true
	case "WEBDAV_ENABLED":
		return envOr(env, key, "true") == "true"
	case "ONBOARDING_ENABLED":
		return envOr(env, key, "false") == "true"
	}
	return envOr(env, key, "") != ""
}

// collectProxydRoutes returns surviving routes after gated_by filtering.
// Per spec (specs/5/35-proxyd-standalone.md "Field semantics"): a route whose
// GatedBy env is unset or empty at compose-generate time is dropped. Core
// routes come first (skipped under PROFILE=minimal, /dash/ skipped unless
// full); per-service routes appended in service-name order.
func collectProxydRoutes(services []svcWithCfg, env map[string]string, profile string) []ProxydRoute {
	var out []ProxydRoute
	if profile != "minimal" {
		for _, r := range coreProxydRoutes {
			if !gatedByOn(env, r.GatedBy) {
				continue
			}
			if r.Path == "/dash/" && profile != "full" {
				continue
			}
			out = append(out, r)
		}
	}
	for _, s := range services {
		for _, r := range s.cfg.ProxydRoutes {
			if !gatedByOn(env, r.GatedBy) {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

type svcWithCfg struct {
	name string
	cfg  ServiceConfig
}

// hasService reports whether services contains an entry named name.
func hasService(services []svcWithCfg, name string) bool {
	for _, s := range services {
		if s.name == name {
			return true
		}
	}
	return false
}

func Generate(dataDir string) (string, error) {
	env, _ := godotenv.Read(filepath.Join(dataDir, ".env"))
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range map[string]string{
		"API_PORT":       fmt.Sprintf("%d", core.DefaultAPIPort),
		"ASSISTANT_NAME": "arizuko",
		"DATA_DIR":       dataDir,            // host path; used in extra service volume strings
		"CONTAINER_DATA": containerDataMount, // container-internal data path for TOML templates
	} {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}

	// Compose owns the project name (it generates the YAML and writes
	// container/network names). When egress isolation is on, write the
	// derived names explicitly into env so daemons inside containers
	// don't have to guess from filesystem paths.
	project := filepath.Base(dataDir)
	app, flavor, _ := strings.Cut(project, "_")
	if !identRE.MatchString(app) {
		return "", fmt.Errorf("invalid compose project app %q (derived from data dir basename)", app)
	}
	if flavor != "" && !identRE.MatchString(flavor) {
		return "", fmt.Errorf("invalid instance flavor %q (derived from data dir basename)", flavor)
	}
	// ARIZUKO_INSTANCE is the operator-facing instance identifier used as
	// the OTLP service.instance.id resource attribute (spec 5/O). Default
	// is the flavor segment (e.g. "krons" from "arizuko_krons"); operator
	// can override by setting ARIZUKO_INSTANCE in .env.
	if _, ok := env["ARIZUKO_INSTANCE"]; !ok {
		if flavor != "" {
			env["ARIZUKO_INSTANCE"] = flavor
		} else {
			env["ARIZUKO_INSTANCE"] = app
		}
	}
	// CRACKBOX_ADMIN_API is the master switch for egress: when set, the
	// crackbox service is emitted and runed gets the per-folder network
	// derivations it needs. No separate EGRESS_ISOLATION boolean.
	if envOr(env, "CRACKBOX_ADMIN_API", "") != "" {
		if _, ok := env["EGRESS_NETWORK_PREFIX"]; !ok {
			if flavor != "" {
				env["EGRESS_NETWORK_PREFIX"] = app + "_" + flavor
			} else {
				env["EGRESS_NETWORK_PREFIX"] = app
			}
		}
		if _, ok := env["EGRESS_CRACKBOX"]; !ok {
			if flavor != "" {
				env["EGRESS_CRACKBOX"] = app + "_crackbox_" + flavor
			} else {
				env["EGRESS_CRACKBOX"] = app + "_crackbox"
			}
		}
	}

	// Service-key provisioning (split-daemon soak, spec 5/E + 5/P). routd and
	// runed authenticate to authd with a per-daemon AUTHD_SERVICE_KEY; authd
	// recognises them via AUTHD_SERVICE_KEYS (principal=secret pairs — the raw
	// secret; authd SHA-256-hashes both sides at compare time). Keys persist in
	// the per-daemon env files so a redeploy keeps the same identity instead of
	// invalidating in-flight tokens. AUTHD_URL is the authd in-network base URL
	// every consumer (routd, runed, proxyd, webd, onbod) verifies/exchanges
	// against. All additive: gated keeps its own wiring untouched.
	perDaemon := map[string]map[string]string{}
	_, pinned := env["AUTHD_SERVICE_KEYS"]
	seedKeys := !pinned
	var keyPairs []string
	// wireServiceKey provisions a daemon's AUTHD_SERVICE_KEY, scopes it into the
	// daemon's env file, and (unless the operator pinned AUTHD_SERVICE_KEYS)
	// registers service:<daemon>=<key> in the authd seed.
	wireServiceKey := func(daemon string) {
		if _, done := perDaemon[daemon]; done {
			return // already wired (e.g. two multi-account variants of one adapter)
		}
		key := provisionServiceKey(dataDir, daemon, env)
		// AUTHD_SERVICE_NAME pins the exchange principal to the DAEMON name so an
		// adapter whose CHANNEL_NAME differs (telegram→teled) — and its multi-account
		// variants (teled-rhias) sharing this env file — exchange as service:<daemon>,
		// matching what we seed below + authd grants.
		perDaemon[daemon] = map[string]string{"AUTHD_SERVICE_KEY": key, "AUTHD_SERVICE_NAME": daemon}
		if seedKeys {
			keyPairs = append(keyPairs, fmt.Sprintf("service:%s=%s", daemon, key))
		}
	}
	wireServiceKey("routd")
	wireServiceKey("runed")
	// timed federates its fire loop over routd in the split (no messages.db);
	// it needs a service:timed token like routd/runed. Monolith timed never
	// reads its env file's AUTHD_* vars (no ROUTER_URL → direct-DB path).
	wireServiceKey("timed")
	// onbod posts the onboarding greeting to routd /v1/outbound with a
	// service:onbod token (spec 5/1).
	wireServiceKey("onbod")
	// proxyd presents service:proxyd to backends; webd presents service:webd to
	// proxyd's /v1/routes + routd register; dashd presents service:dashd on the
	// whapd re-pair proxy — each the channel proof for the identity it forwards
	// (ES256 service tokens; HMAC + X-User-Sig retired).
	wireServiceKey("proxyd")
	wireServiceKey("webd")
	wireServiceKey("dashd")
	if _, ok := env["AUTHD_URL"]; !ok {
		env["AUTHD_URL"] = "http://authd:8080"
	}
	// routd federates /invite + /gate to onbod (onbod OWNS invites +
	// onboarding_gates — spec 5/5). ONBOD_URL is onbod's in-network base URL;
	// routd's env-file scope (daemonKeys["routd"]) includes it. Only meaningful
	// when onbod runs (ONBOARDING_ENABLED); harmless otherwise (nil client).
	if _, ok := env["ONBOD_URL"]; !ok {
		env["ONBOD_URL"] = "http://onbod:8080"
	}

	servicesDir := filepath.Join(dataDir, "services")

	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return "", fmt.Errorf("read services/: %w", err)
	}

	var services []svcWithCfg
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		var cfg ServiceConfig
		if _, err := toml.DecodeFile(filepath.Join(servicesDir, e.Name()), &cfg); err != nil {
			return "", fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		if !identRE.MatchString(name) {
			return "", fmt.Errorf("invalid service filename %q (allowed chars: [A-Za-z0-9_.-])", e.Name())
		}
		if !imageRefRE.MatchString(cfg.Image) {
			return "", fmt.Errorf("service %q has invalid image %q (must match image-ref regex)", name, cfg.Image)
		}
		services = append(services, svcWithCfg{name, cfg})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].name < services[j].name })

	// Channel adapters present in services/ each get a service:<adapter> key so
	// they exchange it for a messages:write JWT against routd (spec 5/1). Only
	// the discovered message-posting adapters are wired — ttsd/davd/vited etc.
	// never post inbound. Multi-account variants (`<adapter>-<label>`) reuse the
	// base adapter's env file (envFileFor), so the base principal covers them.
	for _, svc := range services {
		base, _, _ := strings.Cut(svc.name, "-")
		if adapterDaemons[base] {
			wireServiceKey(base)
		}
	}
	if seedKeys {
		env["AUTHD_SERVICE_KEYS"] = strings.Join(keyPairs, ",")
	}

	// services/ttsd.toml present → auto-enable TTS on the execution plane
	// (runed). Operator opted in by dropping the TOML; no second env-var flip.
	// Explicit .env values still win (e.g. external Kokoro / OpenAI cloud
	// override TTS_BASE_URL).
	if hasService(services, "ttsd") {
		if _, set := env["TTS_ENABLED"]; !set {
			env["TTS_ENABLED"] = "true"
		}
		if _, set := env["TTS_BASE_URL"]; !set {
			env["TTS_BASE_URL"] = "http://ttsd:8880"
		}
	}

	// Per-daemon env files: non-fatal if it fails; log for triage.
	// Written after services scan so service-triggered env (TTS_*) lands.
	if werr := writeEnvFiles(dataDir, env, perDaemon); werr != nil {
		fmt.Fprintf(os.Stderr, "compose: writeEnvFiles: %v\n", werr)
	}

	profile := envOr(env, "PROFILE", "full")
	routes := collectProxydRoutes(services, env, profile)

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", project)
	b.WriteString("services:\n")
	// The split daemons (spec 5/E + 5/P) are the conversation/auth/execution
	// plane — authd (auth.db) + routd (routd.db, the router every adapter/webd/
	// proxyd talks to) + runed (runed.db, the ONLY daemon wired to docker.sock +
	// crackbox + per-folder agent networks). This is the only topology.
	b.WriteString(authdService(app, flavor, dataDir, env))
	b.WriteString(routdService(app, flavor, dataDir, env))
	b.WriteString(runedService(app, flavor, dataDir, env))
	webPort := envOr(env, "WEB_PORT", "")
	if webPort != "" && profile != "minimal" {
		b.WriteString(webdService(app, flavor, dataDir, env))
		b.WriteString(proxydService(app, flavor, dataDir, env, routes, profile))
		b.WriteString(vitedService(app, flavor, dataDir, env))
	}
	if profile != "minimal" && profile != "web" {
		b.WriteString(timedService(app, flavor, dataDir, env))
		if profile == "full" {
			b.WriteString(dashdService(app, flavor, dataDir, env))
			if webPort != "" && envOr(env, "WEBDAV_ENABLED", "true") == "true" {
				b.WriteString(davdService(app, flavor, dataDir, env))
			}
			if envOr(env, "ONBOARDING_ENABLED", "") == "true" {
				b.WriteString(onbodService(app, flavor, dataDir, env))
			}
		}
	}
	for _, s := range services {
		b.WriteString(renderService(app, flavor, s.name, s.cfg, env))
	}
	if envOr(env, "CRACKBOX_ADMIN_API", "") != "" {
		b.WriteString(crackboxService(app, flavor, dataDir, env))
	}
	return b.String(), nil
}

// crackboxService emits the crackbox proxy service. No `agents` network
// here — folder networks are created at runtime by runed and crackbox is
// attached to each via `docker network connect`. Crackbox stays on the
// compose default bridge so it has outbound internet access.
func crackboxService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  crackbox:\n")
	fmt.Fprintf(&b, "    container_name: %s_crackbox_%s\n", app, flavor)
	b.WriteString("    image: crackbox:latest\n")
	b.WriteString("    command: ['proxy', 'serve']\n")
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s/crackbox:/data/crackbox\n", dataDir)
	b.WriteString(envFileFor("crackbox"))
	b.WriteString("    environment:\n")
	writeEnv(&b, map[string]string{
		"CRACKBOX_STATE_PATH": "/data/crackbox/state.json",
	})
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:3129/health']\n")
	b.WriteString("      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 10s\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

type svcDef struct {
	name        string
	app         string
	flavor      string
	entrypoint  string
	dataDir     string
	volumes     []string // extra volume specs after the data-dir mount
	ports       []string
	environment map[string]string
	dependsOn   string
	env         map[string]string // instance env, for router-name resolution
}

// appSrcVolume returns the read-only HOST_APP_DIR→containerSrcMount volume
// spec, or "" when HOST_APP_DIR is unset (pure-REST tests run without a
// source mount). Daemons that get this mount also need APP_SRC_DIR set to
// containerSrcMount — routd reads ant/skills/self/MIGRATION_VERSION from it
// (auto-migrate trigger), runed reads agent assets for spawns.
func appSrcVolume(env map[string]string) string {
	hostApp := envOr(env, "HOST_APP_DIR", "")
	if hostApp == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s:ro", hostApp, containerSrcMount)
}

// yamlQuote emits a double-quoted YAML scalar with escapes for control
// chars, quotes, and backslashes. Prevents injection via values containing
// newlines, carriage returns, or embedded quotes.
func yamlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func writeEnv(b *strings.Builder, env map[string]string) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "      %s: %s\n", k, yamlQuote(env[k]))
	}
}

func writeSvc(def svcDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", def.name)
	fmt.Fprintf(&b, "    container_name: %s_%s_%s\n", def.app, def.name, def.flavor)
	b.WriteString("    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['%s']\n", def.entrypoint)
	b.WriteString("    user: '1000:1000'\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s:%s\n", def.dataDir, containerDataMount)
	for _, v := range def.volumes {
		fmt.Fprintf(&b, "      - %s\n", v)
	}
	if len(def.ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range def.ports {
			fmt.Fprintf(&b, "      - '%s'\n", p)
		}
	}
	b.WriteString(envFileFor(def.name))
	// DATA_DIR is always the container-internal mount point — .env doesn't
	// know this path, so every arizuko daemon needs the override.
	b.WriteString("    environment:\n")
	fmt.Fprintf(&b, "      DATA_DIR: '%s'\n", containerDataMount)
	if len(def.environment) > 0 {
		writeEnv(&b, def.environment)
	}
	dep := def.dependsOn
	if dep == "" {
		dep = routerSvc(def.env)
	}
	fmt.Fprintf(&b, "    depends_on: [%s]\n", dep)
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

// authdService emits the auth authority. auth.db only — NO message DB, NO
// docker socket, NO crackbox. Serves JWKS to every verifier; reads its own
// service keys + OAuth provider config from env/authd.env. No depends_on:
// authd is the authority, nothing it relies on must come up first.
func authdService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  authd:\n")
	fmt.Fprintf(&b, "    container_name: %s_authd_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['authd']\n")
	b.WriteString("    user: '1000:1000'\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s:%s\n", dataDir, containerDataMount)
	b.WriteString(envFileFor("authd"))
	b.WriteString("    environment:\n")
	fmt.Fprintf(&b, "      DATA_DIR: '%s'\n", containerDataMount)
	// authd snapshots login/refresh scopes by calling routd's ACL owner
	// (GET /v1/users/{sub}/scopes). Unset -> empty-scope sessions (spec 5/5).
	b.WriteString("      GRANTS_URL: 'http://routd:8080'\n")
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

// routdService emits the conversation-state daemon — the canonical router.
// routd.db + adapters (/v1/send) + calls runed; verifies via authd JWKS. NO
// crackbox, NO docker socket. Depends on authd (verifier keyset) + runed (run
// dispatch). Publishes API_PORT:8080 to the host (replacing gated): the host
// CLI reaches the router's /v1/channels here (arizuko status/send).
func routdService(app, flavor, dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", fmt.Sprintf("%d", core.DefaultAPIPort))
	def := svcDef{
		name:       "routd",
		app:        app,
		flavor:     flavor,
		entrypoint: "routd",
		dataDir:    dataDir,
		ports:      []string{fmt.Sprintf("%s:%d", apiPort, core.DefaultAPIPort)},
		dependsOn:  "authd, runed",
		env:        env,
	}
	// routd reads ant/skills/self/MIGRATION_VERSION via checkMigrationVersion
	// to enqueue /migrate; without this mount it sees the host path and the
	// auto-migrate trigger silently never fires.
	if vol := appSrcVolume(env); vol != "" {
		def.volumes = []string{vol}
		def.environment = map[string]string{"APP_SRC_DIR": containerSrcMount}
	}
	return writeSvc(def)
}

// runedService emits the execution plane — the ONLY new daemon wired to the
// docker socket + crackbox + the per-folder agent networks. It mirrors gated's
// spawn wiring (uid 1000, group_add into docker gid, docker.sock mount, the
// read-only HOST_APP_DIR source mount). Depends on authd (broker downscope).
func runedService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  runed:\n")
	fmt.Fprintf(&b, "    container_name: %s_runed_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['runed']\n")
	// uid 1000 matches agent container so shared data dir files round-trip;
	// group_add into docker gid grants docker.sock access for spawning agents.
	b.WriteString("    user: '1000:1000'\n")
	fmt.Fprintf(&b, "    group_add: ['%d']\n", dockerGID())
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s:%s\n", dataDir, containerDataMount)
	b.WriteString("      - /var/run/docker.sock:/var/run/docker.sock\n")
	runedEnv := map[string]string{"DATA_DIR": containerDataMount}
	if vol := appSrcVolume(env); vol != "" {
		fmt.Fprintf(&b, "      - %s\n", vol)
		runedEnv["APP_SRC_DIR"] = containerSrcMount
	}
	b.WriteString("    extra_hosts:\n")
	b.WriteString("      - 'host.docker.internal:host-gateway'\n")
	b.WriteString(envFileFor("runed"))
	b.WriteString("    environment:\n")
	writeEnv(&b, runedEnv)
	b.WriteString("    depends_on: [authd]\n")
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func timedService(app, flavor, dataDir string, env map[string]string) string {
	// TIMEZONE is the only compose-side transform; timed reads this name while
	// the rest of the world uses TZ.
	environment := map[string]string{"TIMEZONE": envOr(env, "TZ", "UTC")}
	// timed federates its fire loop over routd HTTP (no messages.db). AUTHD_URL +
	// AUTHD_SERVICE_KEY arrive via env/timed.env.
	environment["ROUTER_URL"] = routerURL(env)
	return writeSvc(svcDef{
		name:        "timed",
		app:         app,
		flavor:      flavor,
		entrypoint:  "timed",
		dataDir:     dataDir,
		environment: environment,
		env:         env,
	})
}

func onbodService(app, flavor, dataDir string, env map[string]string) string {
	// Force ONBOARDING_ENABLED=true inside the container regardless of how the
	// flag is expressed in .env (gate for daemon inclusion was already decided by
	// Generate's caller). ROUTER_URL pinned to the canonical router so onbod's
	// outbound greeting reaches it.
	environment := map[string]string{
		"ONBOARDING_ENABLED": "true",
		"ROUTER_URL":         routerURL(env),
	}
	// onbod OWNS onboarding/invites/onboarding_gates in onbod.db (spec 5/5).
	// ONBOD_DB_PATH points it there. Container-internal, so .env can't know it.
	environment["ONBOD_DB_PATH"] = containerDataMount + "/store/onbod.db"
	return writeSvc(svcDef{
		name:        "onbod",
		app:         app,
		flavor:      flavor,
		entrypoint:  "onbod",
		dataDir:     dataDir,
		environment: environment,
		env:         env,
	})
}

func dashdService(app, flavor, dataDir string, env map[string]string) string {
	def := svcDef{
		name:       "dashd",
		app:        app,
		flavor:     flavor,
		entrypoint: "dashd",
		dataDir:    dataDir,
		// DB_PATH is container-internal; .env can't know it.
		// DASH_PORT override pins internal listen to :8080 (healthcheck target)
		// even when .env sets DASH_PORT for host-side publish.
		environment: map[string]string{
			"DB_PATH":   containerDataMount + "/store/messages.db",
			"DASH_PORT": fmt.Sprintf("%d", core.DefaultAPIPort),
		},
		env: env,
	}
	if dashPort := envOr(env, "DASH_PORT", ""); dashPort != "" {
		def.ports = []string{dashPort + ":8080"}
	}
	return writeSvc(def)
}

func proxydService(app, flavor, dataDir string, env map[string]string, routes []ProxydRoute, profile string) string {
	webPort := envOr(env, "WEB_PORT", "8095")
	environment := map[string]string{}
	if b, err := json.Marshal(routes); err == nil {
		environment["PROXYD_ROUTES_JSON"] = string(b)
	}
	ports := []string{webPort + ":8080"}
	if aliases := envOr(env, "WEB_PORT_ALIASES", ""); aliases != "" {
		for _, a := range strings.Split(aliases, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				ports = append(ports, a+":8080")
			}
		}
	}
	// dashd is full-profile only; depending on it in web/standard profiles
	// yields "depends on undefined service dashd" — a fatal compose error.
	router := routerSvc(env)
	deps := router + ", webd"
	if profile == "full" {
		deps = router + ", dashd, webd"
	}
	return writeSvc(svcDef{
		name:        "proxyd",
		app:         app,
		flavor:      flavor,
		entrypoint:  "proxyd",
		dataDir:     dataDir,
		ports:       ports,
		environment: environment,
		dependsOn:   deps,
		env:         env,
	})
}

func davdService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  davd:\n")
	fmt.Fprintf(&b, "    container_name: %s_davd_%s\n", app, flavor)
	// arizuko-davd is sigoden/dufs wrapped in alpine — same binary,
	// adds wget for the healthcheck (dufs is distroless).
	b.WriteString("    image: arizuko-davd:latest\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s/groups:/data\n", dataDir)
	if davPort := envOr(env, "DAV_PORT", ""); davPort != "" {
		b.WriteString("    ports:\n")
		fmt.Fprintf(&b, "      - '%s:8080'\n", davPort)
	}
	b.WriteString("    command:\n")
	b.WriteString("      - --port\n      - '8080'\n      - --allow-all\n      - /data\n")
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:8080/']\n")
	b.WriteString("      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 10s\n")
	fmt.Fprintf(&b, "    depends_on: [%s]\n", routerSvc(env))
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func webdService(app, flavor, dataDir string, env map[string]string) string {
	// webd registers as a channel + posts inbound to the router. Pin ROUTER_URL
	// explicitly to the canonical router so webd never falls back to a code
	// default that disagrees with the selected plane.
	return writeSvc(svcDef{
		name: "webd", app: app, flavor: flavor,
		entrypoint: "webd", dataDir: dataDir,
		environment: map[string]string{"ROUTER_URL": routerURL(env)},
		env:         env,
	})
}

func vitedService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  vited:\n")
	fmt.Fprintf(&b, "    container_name: %s_vited_%s\n", app, flavor)
	b.WriteString("    image: arizuko-vite:latest\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s/web:/web\n", dataDir)
	// vite dev server has no /health; probe /@vite/client (always 200 in dev).
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:8080/@vite/client']\n")
	b.WriteString("      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 15s\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func renderService(app, flavor, name string, cfg ServiceConfig, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    container_name: %s_%s_%s\n", app, name, flavor)
	fmt.Fprintf(&b, "    image: %s\n", cfg.Image)
	if len(cfg.Entrypoint) > 0 {
		fmt.Fprintf(&b, "    entrypoint: %s\n", yamlList(cfg.Entrypoint))
	}
	if len(cfg.Command) > 0 {
		fmt.Fprintf(&b, "    command: %s\n", yamlList(cfg.Command))
	}
	if len(cfg.Volumes) > 0 {
		b.WriteString("    volumes:\n")
		for _, v := range cfg.Volumes {
			fmt.Fprintf(&b, "      - %s\n", interpolate(v, env))
		}
	}
	if len(cfg.Ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range cfg.Ports {
			fmt.Fprintf(&b, "      - '%s'\n", p)
		}
	}
	b.WriteString(envFileFor(name))
	if len(cfg.Environment) > 0 {
		b.WriteString("    environment:\n")
		interped := make(map[string]string, len(cfg.Environment))
		for k, v := range cfg.Environment {
			interped[k] = interpolate(v, env)
		}
		// Force-pin any declared ROUTER_URL to the canonical router (routd) so an
		// adapter TOML carrying a stale value re-points on regenerate without
		// re-seeding services/. Narrow: only ROUTER_URL, only when present.
		if _, ok := interped["ROUTER_URL"]; ok {
			interped["ROUTER_URL"] = routerURL(env)
		}
		writeEnv(&b, interped)
	}
	deps := cfg.DependsOn
	if len(deps) == 0 {
		deps = []string{routerSvc(env)}
	}
	fmt.Fprintf(&b, "    depends_on: [%s]\n", strings.Join(deps, ", "))
	restart := cfg.Restart
	if restart == "" {
		restart = "on-failure"
	}
	fmt.Fprintf(&b, "    restart: %s\n", restart)
	return b.String()
}

func interpolate(s string, env map[string]string) string {
	for k, v := range env {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

func envOr(env map[string]string, key, fallback string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func yamlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = yamlQuote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
