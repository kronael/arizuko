package compose

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/store"
)

// containerDataMount is the container-side path where the data dir is mounted.
const containerDataMount = "/srv/app/home"

// containerSrcMount is the container-side path where HOST_APP_DIR is mounted
// under APP_SRC_DEV. bakedAppSrc is where the Dockerfile stages `ant/` into
// arizuko:latest, and is the default source — see appSrc.
const (
	containerSrcMount = "/srv/app/arizuko"
	bakedAppSrc       = "/opt/arizuko"
)

// routdURL is the in-network base URL of the canonical router. webd/onbod/timed
// and every adapter fragment point their ROUTER_URL here.
const routdURL = "http://routd:8080"

// dockerGID returns the gid that owns /var/run/docker.sock, or 999 fallback.
// runed must be in this group to spawn agent containers as uid 1000.
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
func provisionServiceKey(dataDir, daemon string) string {
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
	for line := range strings.SplitSeq(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// identRE matches safe identifiers for container_name components, service
// names, and service fragment filenames.
var identRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`)

// Per-daemon env scoping: each known daemon gets env/<daemon>.env with only the
// vars it needs; unknown services fall back to the shared .env. commonKeys flow
// into every arizuko daemon env file.
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
	// The store server (spec 6/42). Every daemon reaches its owner's tables over
	// the same endpoint and trusts the certificate that server generated, which
	// the operator puts in .env as base64 because a container has its
	// environment and no file of its own. The access key is per daemon and
	// arrives through perDaemon below, so a daemon signs as itself and a policy
	// can bound what it may touch.
	dynstore.EnvURL,
	dynstore.EnvRegion,
	dynstore.EnvCA,
	dynstore.EnvKeyID,
	dynstore.EnvSecret,
	// DATA_DIR is NOT in commonKeys: that would leak the host path into the
	// container. It is the container-internal mount point, set per-daemon in the
	// compose `environment:` block.
}

// daemonKeys: per-daemon secrets + config. Unlisted keys never reach the daemon.
var daemonKeys = map[string][]string{
	// timed: scheduler. Federates the fire loop over routd (ROUTER_URL) with a
	// service:timed token exchanged from AUTHD_SERVICE_KEY at AUTHD_URL.
	"timed": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "ROUTER_URL"},
	// authd: auth authority. auth.db only; no message DB, no docker, no egress.
	// Serves JWKS; seeded with the service keys (AUTHD_SERVICE_KEYS, hashed at
	// compare time inside authd) + OAuth provider config.
	"authd": {
		"AUTH_SECRET", "AUTH_BASE_URL",
		"AUTHD_SERVICE_KEY", "AUTHD_SERVICE_KEYS", "GRANTS_URL",
		"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_ALLOWED_ORG",
		"DISCORD_CLIENT_ID", "DISCORD_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ALLOWED_EMAILS",
		"DEX_ISSUER", "DEX_CLIENT_ID", "DEX_CLIENT_SECRET",
	},
	// routd: conversation state. routd.db + adapters (/v1/send) + calls runed.
	// Verifies via authd JWKS. NO egress proxy, NO docker socket.
	"routd": {
		"AUTHD_URL", "AUTHD_SERVICE_KEY", "ONBOD_URL",
		// RUNED_RUN_TIMEOUT: routd sizes its dispatch deadline as runTimeout+2m,
		// deliberately out-waiting runed's container kill. Without it here routd kept
		// the 20m default while runed honoured a longer value, so routd's deadline
		// fired first and the next poll re-fed the SAME turn: the user's message ran
		// twice. marinade ships 30m, so this was live.
		"RUNED_RUN_TIMEOUT",
		// routd detects the route miss that fires chat-initiated onboarding; the flag
		// gates it. Without these on routd the greeting and admission queue never fire
		// and a new group is stored, never onboarded.
		"ONBOARDING_ENABLED", "ONBOARDING_PLATFORMS",
		"OBSERVE_WINDOW_MESSAGES", "OBSERVE_WINDOW_CHARS",
		"SEND_DISABLED_CHANNELS", "SEND_DISABLED_GROUPS",
		// get_web_presence reports a folder's derived/aliased canonical host
		// (spec 5/V) — routd reads the same vhost env proxyd does, to report.
		"HOSTING_DOMAIN", "WEB_VHOST_ALIASES",
		// routd OWNS secrets in the split (routd 0008) → needs the key to
		// decrypt connector/scoped secrets. Without it: "SECRETS_KEY unset".
		"SECRETS_KEY",
		// Instance-wide fallback model when a group has no per-group model.
		"ARIZUKO_DEFAULT_MODEL", "ARIZUKO_DEFAULT_EFFORT",
		// The product `arizuko create` was given. routd writes the default
		// group's row at boot and carries the choice onto it; without this the
		// instance is born with a group that names no product.
		"ARIZUKO_DEFAULT_PRODUCT",
		// The model gateway (spec 6/23, 6/39). routd mints one virtual key per
		// turn with the master key, and the gateway is the ledger it reads the
		// folder's spend from, so the pair lives with the daemon that owns the
		// budget gate. runed gets the URL only.
		"MODEL_GATEWAY_URL", "MODEL_GATEWAY_KEY",
		// Inbound media enrichment and outbound send_voice run IN routd. The split left
		// the env only on runed, so routd defaulted MEDIA_ENABLED off → every inbound
		// attachment silently dropped and send_voice disabled (krons, 2026-06-10). Both
		// daemons need them, so these are duplicated, not moved.
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES",
		"WHISPER_BASE_URL", "WHISPER_MODEL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED",
		"TTS_ENABLED", "TTS_BASE_URL", "TTS_VOICE", "TTS_MODEL", "TTS_TIMEOUT",
		// Surrogate OAuth (spec 5/15): routd's broker refreshes near-expiry
		// tokens at call time (routd/cmd/routd/main.go surrogate registry).
		"SURROGATE_GITHUB_CLIENT_ID", "SURROGATE_GITHUB_CLIENT_SECRET",
	},
	// runed: execution plane. The ONLY daemon wired to docker.sock +
	// the per-spawn egress proxies + the per-folder agent networks.
	"runed": {
		"AUTHD_URL", "AUTHD_SERVICE_KEY",
		"CONTAINER_IMAGE", "CONTAINER_TIMEOUT",
		// RUNED_RUN_TIMEOUT bounds the run: the container hard-kill AND
		// (minus 30s) the agent query timeout (spec, runed/README). Default 20m.
		"RUNED_RUN_TIMEOUT",
		"IDLE_TIMEOUT", "MAX_CONCURRENT_CONTAINERS",
		// MOUNT_ALLOWED_ROOTS gates GroupConfig.Mounts (container/runner.go
		// buildMounts). Without it in runed's env, per-group host mounts are
		// default-denied — must survive `arizuko generate` regen, so it lives here.
		"MOUNT_ALLOWED_ROOTS",
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES", "WHISPER_BASE_URL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED", "WHISPER_MODEL",
		"TTS_ENABLED", "TTS_BASE_URL", "TTS_VOICE", "TTS_MODEL", "TTS_TIMEOUT",
		"EGRESS_SUBNET", "EGRESS_NETWORK_PREFIX",
		"EGRESS_IMAGE", "EGRESS_UPLINK_NETWORK",
		"HOST_CODEX_DIR",
		// ARIZUKO_BACKEND picks the harness occupant (claude|codex|pi). Without it here
		// the instance .env value never reaches runed, so `ARIZUKO_BACKEND=codex;
		// restart` silently kept spawning claude.
		"ARIZUKO_BACKEND",
		// MODEL_GATEWAY_URL: runed re-emits it as container env
		// (container/runner.go buildArgs) and allows the gateway host through
		// the spawn's egress proxy; absent here the instance .env value never
		// reaches it, the same silent no-op as ARIZUKO_BACKEND. NOT the master
		// key — routd mints each turn's virtual key and runed only injects the
		// one it is handed (spec 6/23), so the daemon holding docker.sock has
		// no credential the gateway would honour on its own.
		"MODEL_GATEWAY_URL",
		// NO SECRETS_KEY. runed injects the model credentials routd already decrypted;
		// it opens no secrets table, so the fleet's decryption key had no reader here —
		// only a wider blast radius on the one daemon holding docker.sock (BUGS X3).
		// The credentials runed does need come from store.EnvProfileKeys in
		// writeEnvFiles, which is what keeps this from repeating the v0.62 /login
		// outage (BUGS M1).
	},
	// onbod reads AUTH_BASE_URL (redirect base + cookie Secure flag) and nothing
	// else from the auth block: authd took OAuth in a56202d4, so the provider
	// credentials onbod's own login flow used are gone from its code.
	"onbod": {
		"AUTH_BASE_URL",
		"ONBOARDING_ENABLED", "ONBOARDING_PLATFORMS",
		"ONBOARDING_GREETING",
		"ONBOARD_POLL_INTERVAL", "ONBOD_LISTEN_ADDR",
		// service:onbod token for routd /v1/outbound (spec 5/1).
		"AUTHD_URL", "AUTHD_SERVICE_KEY",
	},
	// dashd verifies proxyd's ES256 transit bearer against authd's JWKS and presents
	// its own service:dashd token on the whapd re-pair proxy. AUTH_SECRET keys the
	// Connect-GitHub CSRF state and is read with os.Getenv, not core.LoadConfig — a
	// reader search over the config struct misses it (BUGS L7).
	"dashd": {
		"AUTH_SECRET", "DASH_PORT", "WHAPD_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY",
		"SURROGATE_GITHUB_CLIENT_ID", "SURROGATE_GITHUB_CLIENT_SECRET",
		// dashd writes user secrets into routd.db directly, so it seals with the same
		// key routd reads with. Omitting it here made dashd write every user secret in
		// plaintext on each live instance (BUGS S2).
		"SECRETS_KEY",
	},
	// webd + proxyd present a service:<daemon> ES256 token as the channel proof for
	// the X-User-* headers they forward. proxyd reads AUTH_SECRET as its
	// auth-enabled flag; webd reads neither it nor AUTH_BASE_URL.
	"webd":   {"ROUTER_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"proxyd": {"AUTH_SECRET", "AUTHD_URL", "AUTHD_SERVICE_KEY", "HOSTING_DOMAIN", "WEB_VHOST_ALIASES"},
	// Channel adapters: AUTHD_URL + AUTHD_SERVICE_KEY let each exchange a
	// service:<adapter> JWT presented on EVERY routd call — register, /v1/messages,
	// /v1/pane (spec 5/1; no CHANNEL_SECRET remains).
	"teled": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "TELEGRAM_BOT_TOKEN"},
	"discd": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "DISCORD_BOT_TOKEN"},
	// bskyd reads BLUESKY_IDENTIFIER/BLUESKY_PASSWORD. The keys listed here were
	// BLUESKY_HANDLE/BLUESKY_APP_PASSWORD — names no Go code has ever read.
	"bskyd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"reditd": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "REDDIT_USERNAME", "REDDIT_PASSWORD"},
	"slakd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "SLAKD_USERS_CACHE_TTL"},
	"linkd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET", "LINKEDIN_ACCESS_TOKEN", "LINKEDIN_REFRESH_TOKEN"},
	"emaid":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "EMAIL_IMAP_HOST", "EMAIL_SMTP_HOST", "EMAIL_ACCOUNT", "EMAIL_PASSWORD", "EMAIL_IMAP_PORT", "EMAIL_SMTP_PORT"},
	"whapd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"twitd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "TWITTER_USERNAME", "TWITTER_PASSWORD", "TWITTER_EMAIL", "TWITTER_2FA_SECRET", "TWITTER_POLL_INTERVAL"},
	// gitd unseals github_sources secrets from routd.db, so it needs the same
	// SECRETS_KEY routd seals with. GITHUB_TOKEN is optional: it only enables
	// send_text (the agent's reply as a comment).
	"gitd": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "SECRETS_KEY", "GITHUB_TOKEN"},
	// ttsd reads exactly TTSD_ADDR, TTS_BACKEND_URL and LOG_LEVEL, and its fragment
	// sets all three in `environment:`, which outranks env_file. The entry exists to
	// move it off the shared .env, which handed it SECRETS_KEY, AUTH_SECRET and
	// every bot token.
	"ttsd": {},
	// kokoro's own compose (remsky/Kokoro-FastAPI docker/cpu) declares PYTHONPATH,
	// ONNX_*, API_LOG_LEVEL and DOWNLOAD_MODEL — nothing arizuko sets. TZ only, so
	// its logs carry the instance's clock.
	"kokoro": {"TZ"},
}

// foreignImages are services running an image arizuko does not build. A
// third-party image reads none of commonKeys, and handing it
// OTEL_EXPORTER_OTLP_HEADERS plus HOST_DATA_DIR/HOST_APP_DIR would re-open the
// leak the shared .env opened. Its env file is daemonKeys only.
var foreignImages = map[string]bool{"kokoro": true}

// adapterDaemons is the set of message-posting channel adapters. Each
// authenticates to routd's /v1/messages with a service:<adapter> JWT (spec 5/1).
// Multi-account adapters (`<adapter>-<label>`) resolve to the base adapter via
// envFileFor's `-` split. Keep in sync with authd serviceGrants.
var adapterDaemons = map[string]bool{
	"teled": true, "whapd": true, "discd": true, "slakd": true,
	"bskyd": true, "reditd": true, "emaid": true, "twitd": true, "linkd": true,
	"gitd": true,
}

// envFileName returns the env file a service reads, relative to the data dir.
// Services outside daemonKeys fall back to the shared .env; `<adapter>-<label>`
// variants share the base adapter's file (specs/5/R-multi-account.md).
func envFileName(name string) string {
	if _, ok := daemonKeys[name]; ok {
		return "env/" + name + ".env"
	}
	if base, _, ok := strings.Cut(name, "-"); ok {
		if _, ok := daemonKeys[base]; ok {
			return "env/" + base + ".env"
		}
	}
	return ".env"
}

// envFileFor returns the scoped env_file block for a base daemon stanza in the
// generated compose (paths relative to the data dir, where the compose lives).
func envFileFor(name string) string {
	return fmt.Sprintf("    env_file:\n      - %s\n", envFileName(name))
}

// writeEnvFiles emits env/<daemon>.env with only the keys each daemon needs.
// perDaemon holds keys scoped to a single daemon that must NOT leak across the
// shared env map.
// containerUID is the uid every daemon and agent container runs as.
const containerUID = 1000

// mkdirOwned creates dir and hands it to uid. Chown is best-effort: it needs
// root, and a non-root generate run on a dev box is legitimate.
func mkdirOwned(dir string, uid int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chown(dir, uid, uid); err != nil && os.Geteuid() == 0 {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	return nil
}

func writeEnvFiles(dataDir string, env map[string]string, perDaemon map[string]map[string]string) error {
	dir := filepath.Join(dataDir, "env")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for daemon, keys := range daemonKeys {
		var all []string
		if !foreignImages[daemon] {
			all = append(all, commonKeys...)
		}
		all = append(all, keys...)
		if daemon == "runed" {
			for key := range store.EnvProfileKeys {
				all = append(all, key)
			}
		}
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

// storeService is the compose service name of the store server
// (template/services/extenddb.yml). A DYNAMODB_URL naming it resolves inside
// the instance network and nowhere else, so it is exactly the value the host
// CLI cannot use.
const storeService = "extenddb"

// storeHostPortKey is the .env key naming the host half of the store server's
// published port, declared in template/services/extenddb.yaml. One declaration:
// docker publishes on it and writeOperatorEnv addresses the same port.
const storeHostPortKey = "STORE_HOST_PORT"

// operatorEnvPath is the host CLI's own env file, loaded over .env by
// core.LoadInstanceEnv. It carries the operator's store credential, which stays
// out of .env because .env reaches every service without a scoped env file, and
// that credential reaches every owner's tables.
func operatorEnvPath(dataDir string) string {
	return filepath.Join(dataDir, "env", "operator.env")
}

// writeOperatorEnv points the host CLI at the store port THIS instance
// publishes. `arizuko` runs on the host, outside the instance network, where
// the compose service name in DYNAMODB_URL resolves to nothing; the loopback
// address of the published port is the same server seen from here. That
// knowledge used to live in a hand-written comment in one instance's file and
// so never reached the second, whose every subcommand died in DNS (BUGS L311).
//
// The credential lines in this file are minted by a by-hand `extenddb init`
// (INSTALL.md § The store server) and cannot be generated from nothing, so the
// file is never rewritten wholesale: one URL line is edited and every other
// byte, comments included, is kept. The line is written only when it is absent
// or when it names the in-network service — a value the host provably cannot
// reach. Any other endpoint is the operator's own and is left alone, because a
// published port is not derivable from an endpoint they pointed elsewhere.
func writeOperatorEnv(dataDir string, env map[string]string) error {
	inNetwork, err := url.Parse(env[dynstore.EnvURL])
	if err != nil || inNetwork.Hostname() != storeService {
		// No store server (the URL is empty or unparseable), or one the
		// operator addressed themselves. Their file, untouched.
		return nil
	}
	port := env[storeHostPortKey]
	if port == "" {
		port = inNetwork.Port()
	}
	onHost := *inNetwork
	onHost.Host = net.JoinHostPort("127.0.0.1", port)

	path := operatorEnvPath(dataDir)
	prev, rerr := os.ReadFile(path)
	if rerr != nil && !os.IsNotExist(rerr) {
		return fmt.Errorf("read %s: %w", path, rerr)
	}
	if os.IsNotExist(rerr) || strings.TrimSpace(string(prev)) == "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		body := fmt.Sprintf("# The host's address for the store server this instance publishes.\n"+
			"# The operator's own access key belongs here too, beside it: `arizuko` on\n"+
			"# the host reaches every owner's tables, so that key is not in .env, which\n"+
			"# every service without a scoped env file reads.\n%s=%s\n",
			dynstore.EnvURL, onHost.String())
		return writeEnvAtomic(dataDir, path, body)
	}
	lines := strings.Split(strings.TrimRight(string(prev), "\n"), "\n")
	// Last wins, the way godotenv reads the file.
	at := -1
	for i, line := range lines {
		if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == dynstore.EnvURL {
			at = i
		}
	}
	line := fmt.Sprintf("%s=%s", dynstore.EnvURL, onHost.String())
	switch {
	case at == -1:
		lines = append(lines, line)
	case !namesStoreService(lines[at]):
		// Reachable from here: the operator's line, and the platform does not
		// edit an operator's line. Only the mode is still ours.
		return chmodOperatorEnv(path)
	default:
		lines[at] = line
	}
	return writeEnvAtomic(dataDir, path, strings.Join(lines, "\n")+"\n")
}

// namesStoreService reports whether one KEY=VALUE line's URL addresses the
// store server by its compose service name. An unparseable value counts: it
// reaches nothing either way.
func namesStoreService(line string) bool {
	_, value, _ := strings.Cut(line, "=")
	have, err := url.Parse(strings.TrimSpace(value))
	return err != nil || have.Hostname() == storeService
}

// chmodOperatorEnv takes the file back to 0600 without touching a byte of it.
// sloth shipped this file at 0644 holding the account key that reaches every
// owner's tables, readable by any host user (BUGS L311). A chmod needs the
// owner, and a non-root generate on a dev box is legitimate — the same reason
// mkdirOwned leaves its chown best-effort there.
func chmodOperatorEnv(path string) error {
	if err := os.Chmod(path, 0o600); err != nil && os.Geteuid() == 0 {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// healthBlock: every Go daemon exposes /health on :8080 internally.
const healthBlock = "    healthcheck:\n" +
	"      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:8080/health']\n" +
	"      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 15s\n"

// ProxydRoute mirrors proxyd's Route shape. Adapters declare theirs in the
// `proxyd_routes` block of their `services/<name>.yaml` manifest; Generate
// returns the survivors for the caller to write into proxyd_routes.
type ProxydRoute struct {
	Path            string   `json:"path"`
	Backend         string   `json:"backend"`
	Auth            string   `json:"auth"`
	GatedBy         string   `json:"gated_by,omitempty"`
	PreserveHeaders []string `json:"preserve_headers,omitempty"`
	StripPrefix     bool     `json:"strip_prefix,omitempty"`
	RedirectTo      string   `json:"redirect_to,omitempty"`
	// Owner is who may rewrite the row (store.RouteOwner*). Generate stamps it
	// from where the route came from — never from the manifest file, which a
	// package writes (BUGS L46).
	Owner string `json:"owner,omitempty"`
}

// coreProxydRoutes are the always-emitted routes for core daemons. Backend URLs
// follow the unified-port convention (DNS name + :8080). GatedBy maps to the env
// vars that also decide the daemon's compose profile, so route presence tracks
// the daemon.
var coreProxydRoutes = []ProxydRoute{
	// Federated cockpit: per-daemon /dash/<daemon>/ surfaces route to the
	// owning daemon. Longest-prefix matching (proxyd/routes.go:MatchRoute)
	// means these must come before the catch-all /dash/ → dashd route.
	{Path: "/dash/onbod/", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
	{Path: "/dash/routd/", Backend: "http://routd:8080", Auth: "user"},
	{Path: "/dash/authd/", Backend: "http://authd:8080", Auth: "user"},
	// proxyd serves its own dashboard; the route exists so the hub links it and
	// so the path is reserved, but proxyd answers it from its own mux before
	// s.route ever consults this table.
	{Path: "/dash/proxyd/", Backend: "http://proxyd:8080", Auth: "user"},
	{Path: "/dash/runed/", Backend: "http://runed:8080", Auth: "user"},
	{Path: "/dash/timed/", Backend: "http://timed:8080", Auth: "user"},
	{Path: "/dash/webd/", Backend: "http://webd:8080", Auth: "user"},
	// /dash/ catch-all → dashd (hub + cross-cutting pages).
	{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
	// /chat/ + /hook/ — the route-token surface, served end to end by webd (spec
	// 5/W). `optional`: proxyd identifies a caller who offers a bearer so webd can
	// attribute the message, and forwards everyone else anonymously — the token is
	// the credential.
	{Path: "/chat/", Backend: "http://webd:8080", Auth: "optional"},
	{Path: "/hook/", Backend: "http://webd:8080", Auth: "optional"},
	// /pair/ — identity pairing (spec 5/31). `user` so an anonymous visitor is
	// bounced through OAuth with the pairing URL as the return path; the human
	// must be signed in before the confirm page is even rendered.
	{Path: "/pair/", Backend: "http://webd:8080", Auth: "user"},
	// /me — the user portal. `user`: every handler is behind webd's requireUser, so
	// a public route would only trade the OAuth bounce for a bare 401. Bare + slash
	// because webd's mux 301s /me → /me/, which never happens if proxyd drops /me.
	{Path: "/me", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/me/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/panel/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/api/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/x/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/static/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/mcp", Backend: "http://webd:8080", Auth: "user"},
	// /assets/ — the browser SDK webd bakes into its binary and advertises as
	// `"sdk": "/assets/arizuko-client.js"`. Public because a widget on a
	// third-party page loads it with no session; without the row the script tag
	// 404s unless a world's /pub/ slot happens to hold a copy (BUGS L322).
	{Path: "/assets/", Backend: "http://webd:8080", Auth: "public"},
	// /slink/* — webd answers 308 → /chat/<token>/. Public.
	{Path: "/slink/", Backend: "http://webd:8080", Auth: "public"},
	{Path: "/dav/", Backend: "http://davd:8080", Auth: "user", StripPrefix: true, GatedBy: "WEBDAV_ENABLED"},
	// Onboarding — `user`, the same requirement /pair/ has. Every handler behind
	// these paths names the caller: /onboard shows the worlds they hold routing
	// authority over and /invite/ redeems in their name. `public` forwards a
	// browser anonymously (proxyd resolves a session only in requireAuth), so
	// the surface was unreachable — the newcomer these pages exist for is
	// exactly the caller who arrives with no session (BUGS L99).
	// Bare, so MatchRoute requires an exact path. There is deliberately no
	// "/onboard/" twin: a trailing-slash row matches the whole SUBTREE, and
	// onbod serves no path under /onboard — only the exact GET and POST. That
	// row forwarded every /onboard/... to a 404 while the route table said the
	// subtree was served (BUGS L331).
	{Path: "/onboard", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
	{Path: "/invite/", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
}

// gatedByOn reports whether the named env-gate is enabled. Empty key = no gate.
// Boolean gates require literal "true"; other keys enable on any non-empty value.
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

// collectProxydRoutes returns surviving routes after gated_by filtering. Routes
// live in the `services/<name>.yaml` manifest, not the compose fragment, because
// proxyd — not docker — is their consumer. A route whose GatedBy env is unset at
// generate time is dropped, so a surface whose daemon is off is not routed at a
// dead backend (specs/5/7-proxyd-standalone.md).
//
// This is the generate-time READER of the manifest a package also installs
// through `arizuko apply`: routes are needed before any daemon or DB exists,
// which justifies a second reader, never a second format (BUGS L44).
func collectProxydRoutes(servicesDir, catalogDir string, services []string, env map[string]string) ([]ProxydRoute, error) {
	var out []ProxydRoute
	for _, r := range coreProxydRoutes {
		if gatedByOn(env, r.GatedBy) {
			out = append(out, r)
		}
	}
	for _, name := range services {
		routes, err := ManifestRoutes(manifestPath(servicesDir, catalogDir, name))
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			// Checked HERE as well as at install because generate is the SECOND reader
			// of the same manifest: `packages upgrade` rewrites the fragment without
			// re-applying its rows, so a revision that added `/dash/` would otherwise
			// reach proxyd_routes through this path alone (BUGS L46).
			if claimed := ReservedRouteClaim(bundledName(catalogDir, name), r.Path); claimed != "" {
				return nil, fmt.Errorf("%s.yaml declares route %q, which claims the core surface %q; "+
					"a package may not serve core arizuko's own paths (BUGS L46)", name, r.Path, claimed)
			}
			if gatedByOn(env, r.GatedBy) {
				// Stamped from the fragment's own name rather than trusted from the file:
				// a package that could write `owner` itself would pass as core-shipped or
				// opt out of every later generate.
				r.Owner = store.RouteOwnerPackage(name)
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// ReservedRoutePaths returns every path core arizuko serves from its own route
// table — the ground a package may not claim (BUGS L46). Derived from
// coreProxydRoutes so there is no second list to drift.
//
// Gates are deliberately ignored: a path core ships behind ONBOARDING_ENABLED is
// core's whether the gate is on or not, and a package allowed to take it while
// the gate was off would front-door the surface the moment it turned on.
func ReservedRoutePaths() []string {
	out := make([]string, 0, len(coreProxydRoutes))
	for _, r := range coreProxydRoutes {
		out = append(out, r.Path)
	}
	return out
}

// CoreRouteAuth returns the auth mode core arizuko declares for the exact path,
// or "" when no core route claims it. Derived from coreProxydRoutes, like
// ReservedRoutePaths, so there is no second list to drift.
//
// The reader is a daemon's own test: a handler that keys on the X-User-Sub
// proxyd stamps is only reachable behind a route that makes proxyd stamp one,
// and that declaration lives here rather than next to the handler. onbod's
// three onboarding surfaces sat behind `public` and bounced every arrival back
// to a login that returned them unidentified (BUGS L99).
func CoreRouteAuth(path string) string {
	for _, r := range coreProxydRoutes {
		if r.Path == path {
			return r.Auth
		}
	}
	return ""
}

// ReservedRouteClaim returns the core path that `path` would take over, or "".
//
// `bundled` is the name of a fragment the arizuko release itself ships, and it
// names the ONE exception: such a daemon serves its own `/dash/<name>/`
// console, which is where proxyd routes an adapter dashboard. Ten shipped
// adapters declare exactly that in their own manifest. The name must come from
// the release's catalog — a third-party package cannot put a file there — so
// pass "" for every package installed from a source and for an operator's
// hand-written row. Neither gets the exception, and `/dash/` stays closed to
// both (BUGS L46).
func ReservedRouteClaim(bundled, path string) string {
	if bundled != "" && strings.HasPrefix(path, "/dash/"+bundled+"/") {
		return ""
	}
	for _, reserved := range ReservedRoutePaths() {
		if path == reserved {
			return reserved
		}
		if strings.HasSuffix(reserved, "/") && strings.HasPrefix(path, reserved) {
			return reserved
		}
	}
	return ""
}

// readFragments lists the compose service fragments in <dataDir>/services,
// sorted by name. Each `<name>.yml` is included verbatim by docker compose.
func readFragments(servicesDir string) ([]string, error) {
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return nil, fmt.Errorf("read services/: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yml")
		if !identRE.MatchString(name) {
			return nil, fmt.Errorf("invalid service filename %q (allowed chars: [A-Za-z0-9_.-])", e.Name())
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// activeProfiles derives COMPOSE_PROFILES from the instance feature flags.
// Every optional daemon carries `profiles: [<name>]` in the generated compose;
// this list is the single gate that decides which of them docker brings up.
func activeProfiles(env map[string]string) []string {
	var p []string
	if envOr(env, "WEB_PORT", "") != "" {
		// webd/proxyd/vited/dashd share one profile: proxyd depends_on dashd +
		// webd, and compose rejects a depends_on into an inactive profile.
		p = append(p, "web")
		if envOr(env, "WEBDAV_ENABLED", "true") == "true" {
			p = append(p, "davd")
		}
	}
	p = append(p, "timed")
	if envOr(env, "ONBOARDING_ENABLED", "false") == "true" {
		p = append(p, "onbod")
	}
	sort.Strings(p)
	return p
}

const (
	managedBegin = "# --- compose-managed (do not edit) ---"
	managedEnd   = "# --- end compose-managed ---"
)

// writeManagedEnv rewrites the compose-managed block of <dataDir>/.env, leaving
// operator lines untouched. Docker interpolates the service fragments with
// these. Identity is configured here, never derived inside a container.
func writeManagedEnv(dataDir, app, flavor string, profiles []string) error {
	path := filepath.Join(dataDir, ".env")
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Strip only a single well-formed [begin, end] block. A malformed marker is
	// corruption — fail loud rather than silently drop every operator line after
	// it (which would erase AUTH_SECRET/SECRETS_KEY/tokens).
	lines := strings.Split(string(prev), "\n")
	begin, end := -1, -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, managedBegin):
			if begin != -1 {
				return fmt.Errorf(".env has multiple %q markers — resolve manually", managedBegin)
			}
			begin = i
		case strings.HasPrefix(line, managedEnd):
			if begin == -1 {
				return fmt.Errorf(".env has %q with no preceding begin — resolve manually", managedEnd)
			}
			if end != -1 {
				return fmt.Errorf(".env has multiple %q markers — resolve manually", managedEnd)
			}
			end = i
		}
	}
	if begin != -1 && end == -1 {
		return fmt.Errorf(".env has %q with no matching end — refusing to rewrite; resolve manually", managedBegin)
	}
	var keep []string
	if begin != -1 {
		keep = append(keep, lines[:begin]...)
		keep = append(keep, lines[end+1:]...)
	} else {
		keep = lines
	}
	body := strings.TrimRight(strings.Join(keep, "\n"), "\n")
	var b strings.Builder
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\nAPP=%s\nFLAVOR=%s\nDATA_DIR=%s\nCOMPOSE_PROFILES=%s\n%s\n",
		managedBegin, app, flavor, dataDir, strings.Join(profiles, ","), managedEnd)
	return writeEnvAtomic(dataDir, path, b.String())
}

// Generate renders the instance's docker-compose.yml and returns it with the
// proxyd route table the same run decided on. The routes are NOT config on the
// compose file — a `PROXYD_ROUTES_JSON` env var seeded proxyd_routes once and was
// then ignored forever (BUGS L45). They are rows the caller writes into routd.db.
func Generate(dataDir string) (string, []ProxydRoute, error) {
	env, _ := godotenv.Read(filepath.Join(dataDir, ".env"))
	if env == nil {
		env = map[string]string{}
	}
	seedSharedDefaults(env)

	// Compose owns the project name. When egress isolation is on, write the derived
	// names explicitly into env so daemons inside containers don't guess them from
	// filesystem paths.
	project := filepath.Base(dataDir)
	app, flavor, _ := strings.Cut(project, "_")
	if !identRE.MatchString(app) {
		return "", nil, fmt.Errorf("invalid compose project app %q (derived from data dir basename)", app)
	}
	if flavor != "" && !identRE.MatchString(flavor) {
		return "", nil, fmt.Errorf("invalid instance flavor %q (derived from data dir basename)", flavor)
	}
	// ARIZUKO_INSTANCE is the OTLP service.instance.id resource attribute (spec
	// 5/O). Defaults to the flavor segment; the operator can override it in .env.
	if _, ok := env["ARIZUKO_INSTANCE"]; !ok {
		if flavor != "" {
			env["ARIZUKO_INSTANCE"] = flavor
		} else {
			env["ARIZUKO_INSTANCE"] = app
		}
	}
	// EGRESS_IMAGE is the master switch for egress isolation: name the image holding
	// the `egress` binary and runed starts one proxy per spawn from it, with that
	// folder's allowlist baked in (spec 6/24).
	//
	// The uplink network is written here, never derived at runtime: it is the compose
	// project's default network, which is both the proxy's route out and runed's path
	// to probe its /health before the spawn starts.
	if envOr(env, "EGRESS_IMAGE", "") != "" {
		if _, ok := env["EGRESS_NETWORK_PREFIX"]; !ok {
			if flavor != "" {
				env["EGRESS_NETWORK_PREFIX"] = app + "_" + flavor
			} else {
				env["EGRESS_NETWORK_PREFIX"] = app
			}
		}
		if _, ok := env["EGRESS_UPLINK_NETWORK"]; !ok {
			env["EGRESS_UPLINK_NETWORK"] = project + "_default"
		}
	}

	// Service-key provisioning (spec 5/E + 5/P). routd and runed authenticate to
	// authd with a per-daemon AUTHD_SERVICE_KEY; authd recognises them via
	// AUTHD_SERVICE_KEYS (principal=secret pairs, SHA-256-hashed at compare time).
	// Keys persist in the per-daemon env files so a redeploy keeps the same identity
	// instead of invalidating in-flight tokens.
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
		key := provisionServiceKey(dataDir, daemon)
		// AUTHD_SERVICE_NAME pins the exchange principal to the DAEMON name so an
		// adapter whose CHANNEL_NAME differs (telegram→teled) — and its multi-account
		// variants sharing this env file — exchange as service:<daemon>.
		perDaemon[daemon] = map[string]string{"AUTHD_SERVICE_KEY": key, "AUTHD_SERVICE_NAME": daemon}
		if seedKeys {
			keyPairs = append(keyPairs, fmt.Sprintf("service:%s=%s", daemon, key))
		}
	}
	wireServiceKey("routd")
	wireServiceKey("runed")
	// timed federates its fire loop over routd in the split (no messages.db);
	// it needs a service:timed token like routd/runed.
	wireServiceKey("timed")
	// onbod posts the onboarding greeting to routd /v1/outbound with a
	// service:onbod token (spec 5/1).
	wireServiceKey("onbod")
	// proxyd presents service:proxyd to backends; webd presents service:webd to
	// proxyd's /v1/routes and routd register; dashd presents service:dashd on the
	// whapd re-pair proxy.
	wireServiceKey("proxyd")
	wireServiceKey("webd")
	wireServiceKey("dashd")
	if _, ok := env["AUTHD_URL"]; !ok {
		env["AUTHD_URL"] = "http://authd:8080"
	}
	// routd federates /invite + /gate to onbod, which OWNS invites and
	// onboarding_gates (spec 5/5). Only meaningful when onbod runs; harmless
	// otherwise (nil client).
	if _, ok := env["ONBOD_URL"]; !ok {
		env["ONBOD_URL"] = "http://onbod:8080"
	}

	servicesDir := filepath.Join(dataDir, "services")
	services, err := readFragments(servicesDir)
	if err != nil {
		return "", nil, err
	}

	// Channel adapters present in services/ each get a service:<adapter> key so they
	// exchange it for a messages:write JWT against routd (spec 5/1). Only the
	// message-posting adapters are wired; multi-account variants reuse the base
	// adapter's env file.
	for _, name := range services {
		base, _, _ := strings.Cut(name, "-")
		if adapterDaemons[base] {
			wireServiceKey(base)
		}
	}
	if seedKeys {
		env["AUTHD_SERVICE_KEYS"] = strings.Join(keyPairs, ",")
	}

	// Store credentials (spec 6/42). The server mints one access key per daemon
	// and the operator writes it into that daemon's env file; a
	// regenerate carries it forward for the same reason a service key is
	// carried: a daemon whose key changed under it signs every request wrong.
	// It stays OUT of the shared .env, so one daemon's key is not every
	// daemon's. This runs after the service keys so it merges into their maps
	// rather than standing where wireServiceKey tests for its own work.
	mergeStoreCredentials(dataDir, perDaemon)

	// What each package needs from .env, resolved before any env file is
	// written: a minted key reaches the daemons that read it (runed re-emits
	// MODEL_GATEWAY_KEY) and a package default gates its routes below.
	catalogDir := CatalogDir(env["HOST_APP_DIR"])
	for _, name := range services {
		generated, err := ProvisionPackageEnv(dataDir, manifestPath(servicesDir, catalogDir, name), name, env)
		if err != nil {
			return "", nil, err
		}
		for _, key := range generated {
			fmt.Printf("env: generated %s for %s into .env\n", key, name)
		}
	}

	// Per-daemon env files: fatal on failure. Written after the services scan so
	// service-triggered env lands. A silent failure would let generation succeed
	// while a daemon's env file goes stale — the `/login` outage class (BUGS C11).
	if werr := writeEnvFiles(dataDir, env, perDaemon); werr != nil {
		return "", nil, fmt.Errorf("write per-daemon env files: %w", werr)
	}
	// The operator's own env file is the same shape of failure one door along:
	// silence here leaves every `arizuko <cmd> <inst>` dialling a name only a
	// container resolves (BUGS L311).
	if werr := writeOperatorEnv(dataDir, env); werr != nil {
		return "", nil, fmt.Errorf("write the operator env file: %w", werr)
	}

	// app-src/ holds the release's ant/, staged by runed for agent containers.
	// Generation creates it because generation runs as root while runed runs as uid
	// 1000 and the data-dir root is not writable by it — runed's mkdir failed with
	// permission denied and no agent could migrate.
	if derr := mkdirOwned(filepath.Join(dataDir, "app-src"), containerUID); derr != nil {
		fmt.Fprintf(os.Stderr, "compose: app-src: %v\n", derr)
	}

	routes, err := collectProxydRoutes(servicesDir, catalogDir, services, env)
	if err != nil {
		return "", nil, err
	}
	// Fatal on failure: without the managed block every fragment interpolates to
	// an empty container name and an empty host mount.
	if err := writeManagedEnv(dataDir, app, flavor, activeProfiles(env)); err != nil {
		return "", nil, fmt.Errorf("write .env compose block: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", project)
	if len(services) > 0 {
		b.WriteString("include:\n")
		for _, name := range services {
			fmt.Fprintf(&b, "  - ./services/%s.yml\n", name)
		}
	}
	b.WriteString("services:\n")
	// The split daemons (spec 5/E + 5/P) are the conversation/auth/execution plane —
	// authd (auth.db) + routd (routd.db) + runed (runed.db, the ONLY daemon wired to
	// docker.sock, per-spawn egress proxies and per-folder agent networks). This is
	// the only topology, so they carry no profile.
	b.WriteString(authdService(app, flavor))
	b.WriteString(routdService(app, flavor, dataDir, env))
	b.WriteString(runedService(app, flavor, dataDir, env))
	b.WriteString(webdService(app, flavor))
	b.WriteString(proxydService(app, flavor, env))
	b.WriteString(vitedService(app, flavor, dataDir))
	b.WriteString(dashdService(app, flavor, dataDir, env))
	b.WriteString(timedService(app, flavor, env))
	b.WriteString(onbodService(app, flavor, dataDir))
	b.WriteString(davdService(app, flavor, dataDir, env))
	// No egress proxy service: there is no long-lived shared proxy to declare.
	// runed starts one proxy container per spawn from EGRESS_IMAGE and kills it
	// with the turn (spec 6/24).
	return b.String(), routes, nil
}

type svcDef struct {
	name        string
	app         string
	flavor      string
	entrypoint  string
	dataDir     string   // bind-mounted at containerDataMount; "" = no mount, no DATA_DIR
	dataSubdirs []string // mount only these children of dataDir; empty = the whole tree
	profile     string   // compose profile; "" = always up
	volumes     []string // extra volume specs after the data-dir mount
	ports       []string
	environment map[string]string
	dependsOn   string
}

// appSrc decides where routd and runed read `ant/` from — skills, CLAUDE.md and
// output-styles. It returns the volume line (empty when none is needed) and the
// APP_SRC_DIR value.
//
// The default is bakedAppSrc, the copy the Dockerfile stages into the image: a
// release IS an image. APP_SRC_DEV bind-mounts HOST_APP_DIR read-only instead —
// a developer loop and never a deployment. krons served skills straight off the
// checkout, so an edit reached production with no build, tag or restart (BUGS M1).
func appSrc(env map[string]string) (volume, dir string) {
	hostApp := envOr(env, "HOST_APP_DIR", "")
	if envOr(env, "APP_SRC_DEV", "") == "" || hostApp == "" {
		return "", bakedAppSrc
	}
	return fmt.Sprintf("%s:%s:ro", hostApp, containerSrcMount), containerSrcMount
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

// dataMounts returns the bind-mount specs rooting a daemon at containerDataMount.
// With no subdirs it gets the whole instance tree — every other daemon's SQLite
// file, every group's files, and every live MCP socket. Naming subdirs mounts
// only those children (spec 5/16, BUGS A1). Sources must pre-exist: docker
// creates a missing bind source as root, and every daemon runs as uid 1000.

// mergeStoreCredentials folds each daemon's own store key out of its scoped
// env file into perDaemon, so one daemon's key is not every daemon's.
//
// Neither key present is NORMAL — that daemon has no store identity yet.
// Exactly one is a fault: the file is half written, and skipping it silently
// turns a config error here into an auth failure on that daemon's first store
// call, far from the file that caused it.
func mergeStoreCredentials(dataDir string, perDaemon map[string]map[string]string) {
	var enrolled []string
	for daemon := range daemonKeys {
		file := filepath.Join(dataDir, "env", daemon+".env")
		keyID := readEnvFileKey(file, dynstore.EnvKeyID)
		secret := readEnvFileKey(file, dynstore.EnvSecret)
		if (keyID == "") != (secret == "") {
			have, missing := dynstore.EnvKeyID, dynstore.EnvSecret
			if keyID == "" {
				have, missing = dynstore.EnvSecret, dynstore.EnvKeyID
			}
			slog.Error("store credential is half written; the daemon will fail to "+
				"authenticate at its first store call, not here",
				"daemon", daemon, "file", file, "has", have, "missing", missing)
		}
		if keyID == "" || secret == "" {
			continue
		}
		if perDaemon[daemon] == nil {
			perDaemon[daemon] = map[string]string{}
		}
		perDaemon[daemon][dynstore.EnvKeyID] = keyID
		perDaemon[daemon][dynstore.EnvSecret] = secret
		enrolled = append(enrolled, daemon)
	}
	warnPartialEnrolment(enrolled)
}

// warnPartialEnrolment reports an enrolment that stopped half way: some daemons
// that need a store key hold one and others do not. `store.StoreReach` declares
// which daemons need one — `timed` and `ttsd` need none — so this is that
// declaration's second reader rather than a list to drift (BUGS L319).
//
// NONE enrolled is silent, because that is a fresh `arizuko create` before the
// operator has enrolled anything, and a warning there would fire on every new
// instance. ALL enrolled is silent because it is done. Only the middle is a
// fault, and it is the `L45` class: a generated value that silently stops
// applying while the instance looks healthy.
func warnPartialEnrolment(enrolled []string) {
	var need []string
	for daemon, owners := range store.StoreReach {
		if len(owners) > 0 {
			need = append(need, daemon)
		}
	}
	var missing []string
	for _, daemon := range need {
		if !slices.Contains(enrolled, daemon) {
			missing = append(missing, daemon)
		}
	}
	if len(missing) == 0 || len(missing) == len(need) {
		return
	}
	slices.Sort(missing)
	slog.Error("store enrolment is partial: these daemons reach the store and hold "+
		"no key, so each fails at its first store call while the instance looks healthy",
		"missing", missing, "enrolled", len(enrolled), "need", len(need))
}

// seedSharedDefaults fills the env keys whose default is DECLARED in core, for
// an instance `.env` that omits them. Both values are read from there, never
// respelled: `ASSISTANT_NAME` is in commonKeys, so a second literal here
// reaches every daemon's scoped env file and the constant surfaces only
// outside compose — which is the drift L194 was closed to prevent, and which
// happened anyway (BUGS L340).
func seedSharedDefaults(env map[string]string) {
	for k, v := range map[string]string{
		"API_PORT":       fmt.Sprintf("%d", core.DefaultAPIPort),
		"ASSISTANT_NAME": core.DefaultAssistantName,
	} {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
}

func dataMounts(dataDir string, subdirs []string) []string {
	if len(subdirs) == 0 {
		return []string{fmt.Sprintf("%s:%s", dataDir, containerDataMount)}
	}
	out := make([]string, 0, len(subdirs))
	for _, sub := range subdirs {
		out = append(out, fmt.Sprintf("%s/%s:%s/%s", dataDir, sub, containerDataMount, sub))
	}
	return out
}

func writeSvc(def svcDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", def.name)
	fmt.Fprintf(&b, "    container_name: %s_%s_%s\n", def.app, def.name, def.flavor)
	b.WriteString("    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['%s']\n", def.entrypoint)
	// Docker brings a profiled service up only when the profile is in
	// COMPOSE_PROFILES (written to .env by writeManagedEnv) — the only gate.
	if def.profile != "" {
		fmt.Fprintf(&b, "    profiles: ['%s']\n", def.profile)
	}
	b.WriteString("    user: '1000:1000'\n")
	vols := def.volumes
	if def.dataDir != "" {
		vols = append(dataMounts(def.dataDir, def.dataSubdirs), vols...)
	}
	if len(vols) > 0 {
		b.WriteString("    volumes:\n")
		for _, v := range vols {
			fmt.Fprintf(&b, "      - %s\n", v)
		}
	}
	if len(def.ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range def.ports {
			fmt.Fprintf(&b, "      - '%s'\n", p)
		}
	}
	b.WriteString(envFileFor(def.name))
	// DATA_DIR is always the container-internal mount point — .env doesn't
	// know this path, so every mounted arizuko daemon needs the override.
	if def.dataDir != "" || len(def.environment) > 0 {
		b.WriteString("    environment:\n")
	}
	if def.dataDir != "" {
		fmt.Fprintf(&b, "      DATA_DIR: '%s'\n", containerDataMount)
	}
	if len(def.environment) > 0 {
		writeEnv(&b, def.environment)
	}
	dep := def.dependsOn
	if dep == "" {
		dep = "routd"
	}
	fmt.Fprintf(&b, "    depends_on: [%s]\n", dep)
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

// authdService emits the auth authority. auth.db only — NO message DB, NO docker
// socket, NO egress proxy. No depends_on: authd is the authority, nothing it
// relies on must come up first. store/authd/ is its only mount, so it reaches
// neither the other owners' databases, nor the groups tree, nor ipc/.
func authdService(app, flavor string) string {
	var b strings.Builder
	b.WriteString("  authd:\n")
	fmt.Fprintf(&b, "    container_name: %s_authd_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['authd']\n")
	b.WriteString("    user: '1000:1000'\n")
	// No data mount: authd's tables are on the store server and it reads no file
	// of its own, so the instance tree is not its to see.
	b.WriteString(envFileFor("authd"))
	b.WriteString("    environment:\n")
	// authd snapshots login/refresh scopes by calling routd's ACL owner
	// (GET /v1/users/{sub}/scopes). Unset -> empty-scope sessions (spec 5/5).
	fmt.Fprintf(&b, "      GRANTS_URL: '%s'\n", routdURL)
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

// routdService emits the conversation-state daemon — the canonical router.
// routd.db + adapters + calls runed; verifies via authd JWKS. NO egress proxy, NO
// docker socket. Publishes API_PORT:8080 so the host CLI reaches /v1/channels.
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
	}
	// routd reads ant/skills/self/MIGRATION_VERSION via checkMigrationVersion to
	// enqueue /migrate; pointed at the wrong tree the auto-migrate trigger
	// silently never fires.
	vol, src := appSrc(env)
	def.environment = map[string]string{"APP_SRC_DIR": src}
	if vol != "" {
		def.volumes = []string{vol}
	}
	return writeSvc(def)
}

// runedService emits the execution plane — the ONLY daemon wired to the docker
// socket, the per-spawn egress proxies and the per-folder agent networks.
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
	vol, src := appSrc(env)
	runedEnv := map[string]string{"DATA_DIR": containerDataMount, "APP_SRC_DIR": src}
	if vol != "" {
		fmt.Fprintf(&b, "      - %s\n", vol)
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

// timedService emits the scheduler. NO data-dir mount: timed opens no database
// and reads no file — it federates its fire loop over routd HTTP, so mounting
// the data dir would only hand it every other daemon's database.
func timedService(app, flavor string, env map[string]string) string {
	// TIMEZONE is the only compose-side transform; timed reads this name while
	// the rest of the world uses TZ. AUTHD_URL + AUTHD_SERVICE_KEY arrive via
	// env/timed.env.
	return writeSvc(svcDef{
		name:       "timed",
		app:        app,
		flavor:     flavor,
		entrypoint: "timed",
		profile:    "timed",
		environment: map[string]string{
			"TIMEZONE":   envOr(env, "TZ", "UTC"),
			"ROUTER_URL": routdURL,
		},
	})
}

func onbodService(app, flavor, dataDir string) string {
	// ONBOARDING_ENABLED is forced true inside the container: the onbod profile is
	// what decides whether onbod runs at all.
	//
	// groups/ + web/: SetupGroup creates groups/<folder> plus the per-group web
	// slots. onbod's own tables and routd's are on the store server, so it mounts
	// neither. It never touches ipc/, so a new world can be provisioned without
	// reach over any running agent's MCP socket.
	return writeSvc(svcDef{
		name:        "onbod",
		app:         app,
		flavor:      flavor,
		entrypoint:  "onbod",
		dataDir:     dataDir,
		dataSubdirs: []string{"groups", "web"},
		profile:     "onbod",
		environment: map[string]string{
			"ONBOARDING_ENABLED": "true",
			"ROUTER_URL":         routdURL,
		},
	})
}

func dashdService(app, flavor, dataDir string, env map[string]string) string {
	// dashd's stock-skills picker enumerates ant/skills/ through appSrc, not the
	// HOST_APP_DIR host path — dashd had no source mount at all, so the picker read a
	// host path from inside the container and silently returned nothing (BUGS F54).
	vol, src := appSrc(env)
	def := svcDef{
		name:       "dashd",
		app:        app,
		flavor:     flavor,
		entrypoint: "dashd",
		dataDir:    dataDir,
		// dashd is the operator console over the groups tree and surrogate/. Its
		// owner tables are on the store server, so it mounts none. The whole-tree
		// mount it used to take handed it .env — AUTH_SECRET, SECRETS_KEY, every
		// platform token — and ipc/, the live per-turn agent MCP sockets
		// (BUGS F51).
		dataSubdirs: []string{"groups", "surrogate"},
		profile:     "web",
		// DASH_PORT override pins internal listen to :8080 (healthcheck target)
		// even when .env sets DASH_PORT for host-side publish.
		environment: map[string]string{
			"DASH_PORT":   fmt.Sprintf("%d", core.DefaultAPIPort),
			"APP_SRC_DIR": src,
		},
	}
	if vol != "" {
		def.volumes = []string{vol}
	}
	if dashPort := envOr(env, "DASH_PORT", ""); dashPort != "" {
		def.ports = []string{dashPort + ":8080"}
	}
	return writeSvc(def)
}

// proxydService emits proxyd's compose block. It carries no route config: the
// route table is rows in routd.db, written by the same generate run (see
// Generate), and proxyd reads them live per request.
func proxydService(app, flavor string, env map[string]string) string {
	webPort := envOr(env, "WEB_PORT", "8095")
	ports := []string{webPort + ":8080"}
	if aliases := envOr(env, "WEB_PORT_ALIASES", ""); aliases != "" {
		for a := range strings.SplitSeq(aliases, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				ports = append(ports, a+":8080")
			}
		}
	}
	// dashd + webd share proxyd's `web` profile: compose rejects a depends_on that
	// points into an inactive profile.
	// store/routd/ only: proxyd opens routd.db for route/identity rows and makes no
	// other filesystem call. /pub/ is a redirect to vited, not a static serve.
	return writeSvc(svcDef{
		name:       "proxyd",
		app:        app,
		flavor:     flavor,
		entrypoint: "proxyd",
		// No data mount: proxyd reads routd's tables on the store server and
		// serves no file from the instance tree.
		profile:   "web",
		ports:     ports,
		dependsOn: "routd, dashd, webd",
	})
}

func davdService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  davd:\n")
	fmt.Fprintf(&b, "    container_name: %s_davd_%s\n", app, flavor)
	// arizuko-davd is sigoden/dufs wrapped in alpine — same binary,
	// adds wget for the healthcheck (dufs is distroless).
	b.WriteString("    image: arizuko-davd:latest\n")
	b.WriteString("    profiles: ['davd']\n")
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
	b.WriteString("    depends_on: [routd]\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func webdService(app, flavor string) string {
	// webd registers as a channel and posts inbound to the router. Pin ROUTER_URL so
	// webd never falls back to a code default.
	// No data mount: webd reads routd's tables on the store server and serves the
	// chat widget from the binary, not from web/.
	return writeSvc(svcDef{
		name: "webd", app: app, flavor: flavor,
		entrypoint:  "webd",
		profile:     "web",
		environment: map[string]string{"ROUTER_URL": routdURL},
	})
}

func vitedService(app, flavor, dataDir string) string {
	var b strings.Builder
	b.WriteString("  vited:\n")
	fmt.Fprintf(&b, "    container_name: %s_vited_%s\n", app, flavor)
	b.WriteString("    image: arizuko-vite:latest\n")
	b.WriteString("    profiles: ['web']\n")
	// Read-only: vite serves /web and writes nothing into it — its dep cache
	// lives at a container-local cacheDir (ant/vite.config.js).
	fmt.Fprintf(&b, "    volumes:\n      - %s/web:/web:ro\n", dataDir)
	// vite dev server has no /health; probe /@vite/client (always 200 in dev).
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:8080/@vite/client']\n")
	b.WriteString("      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 15s\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
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
