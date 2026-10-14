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

	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/store"
)

// containerDataMount is the container-side path where the data dir is mounted.
const containerDataMount = "/srv/app/home"

// containerSrcMount is where HOST_APP_DIR is mounted under APP_SRC_DEV;
// bakedAppSrc is where the Dockerfile stages `ant/`, the default (appSrc).
const (
	containerSrcMount = "/srv/app/arizuko"
	bakedAppSrc       = "/opt/arizuko"
)

// routdURL is the in-network base URL every ROUTER_URL points at.
const routdURL = "http://routd:8080"

// provisionServiceKey returns the daemon's AUTHD_SERVICE_KEY, reusing the one
// in env/<daemon>.env so a redeploy keeps the same service identity.
func provisionServiceKey(dataDir, daemon string) string {
	if existing := readEnvFileKey(filepath.Join(dataDir, "env", daemon+".env"), "AUTHD_SERVICE_KEY"); existing != "" {
		return existing
	}
	return core.GenHexToken()
}

// readEnvFileKey scans a KEY=VALUE env file for one key; empty on miss/error.
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

// Per-daemon env scoping: each known daemon gets env/<daemon>.env with only
// the vars it needs; unknown services fall back to the shared .env.
var commonKeys = []string{
	"ASSISTANT_NAME", "TZ", "LOG_LEVEL", "ARIZUKO_DEV",
	"HOST_DATA_DIR", "HOST_APP_DIR", "WEB_HOST",
	"API_PORT",
	// The daemon's service-token exchange principal, set per daemon by
	// wireServiceKey; an adapter exchanges as this, not its CHANNEL_NAME.
	"AUTHD_SERVICE_NAME",
	// OTLP export (spec 5/O-observability). Unset -> stock JSON handler, zero overhead.
	"ARIZUKO_INSTANCE",
	// METRICS_ENABLED gates the `/metrics` endpoint EIGHT daemons mount
	// (obs.MetricsEnabled). It belongs here because compose is the only thing that
	// can deliver it: a var a daemon reads and compose never emits is silently
	// dead, and this one left `6/44`'s gate armed over a counter nothing could
	// scrape — the spec waits on a 7-day number that could not be taken (BUGS
	// `L380`).
	"METRICS_ENABLED",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_HEADERS",
	"OTEL_RESOURCE_ATTRIBUTES",
	// The store server (spec 6/42-dynamo-over-postgres): one endpoint for every daemon, the CA as
	// base64 because a container has no file of its own. The access key is
	// per daemon (perDaemon), so a daemon signs as itself.
	dynstore.EnvURL,
	dynstore.EnvRegion,
	dynstore.EnvCA,
	dynstore.EnvKeyID,
	dynstore.EnvSecret,
	// DATA_DIR is NOT here: it would leak the host path into the container. It
	// is set per-daemon in the compose `environment:` block.
}

// daemonKeys: per-daemon secrets + config. Unlisted keys never reach the daemon.
var daemonKeys = map[string][]string{
	// timed federates the fire loop over routd with a service:timed token.
	"timed": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "ROUTER_URL"},
	// authd: auth.db only; no message DB, no docker, no egress. Seeded with the
	// service keys (hashed at compare time) + OAuth provider config.
	"authd": {
		"AUTH_SECRET", "AUTH_BASE_URL",
		"AUTHD_SERVICE_KEY", "AUTHD_SERVICE_KEYS", "GRANTS_URL",
		"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_ALLOWED_ORG",
		"DISCORD_CLIENT_ID", "DISCORD_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ALLOWED_EMAILS",
		"DEX_ISSUER", "DEX_CLIENT_ID", "DEX_CLIENT_SECRET",
	},
	// routd: conversation state. NO egress proxy, NO docker socket.
	"routd": {
		"AUTHD_URL", "AUTHD_SERVICE_KEY", "ONBOD_URL",
		// routd sizes its dispatch deadline as runTimeout+2m to out-wait runed's
		// kill. Without it here routd kept the 20m default while runed honoured
		// 30m, so routd's deadline fired first and the next poll ran the SAME
		// turn twice.
		"RUNED_RUN_TIMEOUT",
		// routd detects the route miss that fires chat-initiated onboarding;
		// without these a new group is stored, never onboarded.
		"ONBOARDING_ENABLED", "ONBOARDING_PLATFORMS",
		"OBSERVE_WINDOW_MESSAGES", "OBSERVE_WINDOW_CHARS",
		"ENGAGEMENT_TTL",
		"SEND_DISABLED_CHANNELS", "SEND_DISABLED_GROUPS",
		// get_web_presence reads the same vhost env proxyd does (spec 5/V-web-vhosts).
		"HOSTING_DOMAIN", "WEB_VHOST_ALIASES",
		// routd OWNS secrets, so it needs the key to decrypt them.
		"SECRETS_KEY",
		// Instance-wide fallback model when a group has no per-group model.
		"ARIZUKO_DEFAULT_MODEL", "ARIZUKO_DEFAULT_EFFORT",
		// The product `arizuko create` was given; routd carries it onto the
		// default group's row at boot.
		"ARIZUKO_DEFAULT_PRODUCT",
		// The model gateway (spec 6/23, 6/39): routd mints each turn's virtual
		// key with the master key and reads the folder's spend; runed gets the
		// URL only.
		"MODEL_GATEWAY_URL", "MODEL_GATEWAY_KEY",
		// Inbound media enrichment and send_voice run IN routd. With the env
		// only on runed, routd defaulted MEDIA_ENABLED off and every inbound
		// attachment silently dropped (krons, 2026-06-10). Both daemons need
		// them: duplicated, not moved.
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES",
		"WHISPER_BASE_URL", "WHISPER_MODEL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED",
		"TTS_ENABLED", "TTS_BASE_URL", "TTS_VOICE", "TTS_MODEL", "TTS_TIMEOUT",
		// Surrogate OAuth (spec 5/15): routd's broker refreshes near-expiry tokens.
		"SURROGATE_GITHUB_CLIENT_ID", "SURROGATE_GITHUB_CLIENT_SECRET",
	},
	// runed: execution plane, the ONLY daemon wired to /dev/kvm.
	"runed": {
		"AUTHD_URL", "AUTHD_SERVICE_KEY",
		"CONTAINER_TIMEOUT",
		// Bounds the run: the guest hard-kill AND (minus 30s) the agent
		// query timeout. Default 20m.
		"RUNED_RUN_TIMEOUT",
		// The turn substrate and everything the QEMU one reads (spec 6/41
		// §"The seam"). A var runed reads and compose does not emit is SILENTLY
		// dead — the class that cost three outages (BUGS reference
		// `ant_env_delivery`) — so `TestRunedEnvKeysCoverEveryRunedVar` derives
		// this set from runed's own source instead of trusting this list.
		"RUNED_QEMU", "RUNED_KERNEL_DIR", "RUNED_ROOTFS_DIR",
		"RUNED_VIRTIOFSD",
		"RUNED_EGRESS_BIN", "RUNED_FIREWALL_BIN", "RUNED_IP_BIN",
		"RUNED_GUEST_MEM", "RUNED_GUEST_SMP", "RUNED_VSOCK_PORT_BASE",
		"RUNED_TAP_SUBNET", "RUNED_PROXY_PORT", "RUNED_VM_DIR", "RUNED_VSOCK_CID_BASE",
		"RUNED_BOOT_TIMEOUT", "RUNED_REAP_GRACE",
		// How long runed's own shutdown waits for in-flight handlers
		// (runed/cmd/runed/main.go). It lives in the daemon's main, which is why the
		// guard that should have caught its absence could not see it (BUGS `L388`).
		"RUNED_SHUTDOWN_GRACE",
		"IDLE_TIMEOUT", "MAX_CONCURRENT_CONTAINERS",
		// Gates per-group host mounts; absent from runed's env they are
		// default-denied.
		"MOUNT_ALLOWED_ROOTS",
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES", "WHISPER_BASE_URL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED", "WHISPER_MODEL",
		"TTS_ENABLED", "TTS_BASE_URL", "TTS_VOICE", "TTS_MODEL", "TTS_TIMEOUT",
		"HOST_CODEX_DIR",
		// Picks the harness (claude|codex|pi); absent here the .env value never
		// reaches runed, and a restart silently kept spawning claude.
		"ARIZUKO_BACKEND",
		// runed re-emits it as container env and allows the host through the
		// spawn's egress proxy. NOT the master key: routd mints each turn's key
		// and runed injects only the one it is handed (spec 6/23), so the daemon
		// that starts the guests has no credential of its own.
		"MODEL_GATEWAY_URL",
		// NO SECRETS_KEY: runed injects the credentials routd already decrypted
		// and opens no secrets table, so the key had no reader here, only a
		// wider blast radius (BUGS X3). The credentials runed does need come
		// from store.EnvProfileKeys in writeEnvFiles (BUGS M1).
	},
	// onbod reads AUTH_BASE_URL (redirect base + cookie Secure flag) and
	// nothing else from the auth block.
	"onbod": {
		"AUTH_BASE_URL",
		"ONBOARDING_ENABLED", "ONBOARDING_PLATFORMS",
		"ONBOARDING_GREETING",
		"ONBOARD_POLL_INTERVAL", "ONBOD_LISTEN_ADDR",
		// service:onbod token for routd /v1/outbound (spec 5/1).
		"AUTHD_URL", "AUTHD_SERVICE_KEY",
	},
	// dashd verifies proxyd's transit bearer against authd's JWKS and presents
	// service:dashd on the whapd re-pair proxy. AUTH_SECRET keys the
	// Connect-GitHub CSRF state and is read with os.Getenv, not
	// core.LoadConfig (BUGS L7).
	"dashd": {
		"AUTH_SECRET", "DASH_PORT", "WHAPD_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY",
		"SURROGATE_GITHUB_CLIENT_ID", "SURROGATE_GITHUB_CLIENT_SECRET",
		// dashd writes user secrets into routd's store directly, so it seals
		// with the same key routd reads with; without it every user secret was
		// written in plaintext (BUGS S2).
		"SECRETS_KEY",
	},
	// webd + proxyd present a service:<daemon> token as the channel proof for
	// the X-User-* headers they forward; proxyd reads AUTH_SECRET as its
	// auth-enabled flag.
	"webd":   {"ROUTER_URL", "AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"proxyd": {"AUTH_SECRET", "AUTHD_URL", "AUTHD_SERVICE_KEY", "HOSTING_DOMAIN", "WEB_VHOST_ALIASES"},
	// Channel adapters exchange a service:<adapter> JWT presented on EVERY
	// routd call (spec 5/1).
	"teled": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "TELEGRAM_BOT_TOKEN"},
	"discd": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "DISCORD_BOT_TOKEN"},
	// bskyd reads BLUESKY_IDENTIFIER/BLUESKY_PASSWORD, not
	// BLUESKY_HANDLE/BLUESKY_APP_PASSWORD.
	"bskyd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"reditd": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "REDDIT_USERNAME", "REDDIT_PASSWORD"},
	"slakd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "SLAKD_USERS_CACHE_TTL"},
	"linkd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET", "LINKEDIN_ACCESS_TOKEN", "LINKEDIN_REFRESH_TOKEN"},
	"emaid":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "EMAIL_IMAP_HOST", "EMAIL_SMTP_HOST", "EMAIL_ACCOUNT", "EMAIL_PASSWORD", "EMAIL_IMAP_PORT", "EMAIL_SMTP_PORT"},
	"whapd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY"},
	"twitd":  {"AUTHD_URL", "AUTHD_SERVICE_KEY", "TWITTER_USERNAME", "TWITTER_PASSWORD", "TWITTER_EMAIL", "TWITTER_2FA_SECRET", "TWITTER_POLL_INTERVAL"},
	// gitd unseals github_sources secrets from routd's store, so it needs the
	// same SECRETS_KEY. GITHUB_TOKEN is optional: it only enables send_text.
	"gitd": {"AUTHD_URL", "AUTHD_SERVICE_KEY", "SECRETS_KEY", "GITHUB_TOKEN"},
	// ttsd's fragment sets its three vars in `environment:`; the entry exists
	// to move it off the shared .env, which handed it every secret.
	"ttsd": {},
	// kokoro's own compose declares everything it reads; TZ only, so its logs
	// carry the instance's clock.
	"kokoro": {"TZ"},
}

// foreignImages run an image arizuko does not build: none of commonKeys is
// read, and handing over OTEL headers plus HOST_*_DIR would re-open the leak
// the shared .env opened. Their env file is daemonKeys only.
var foreignImages = map[string]bool{"kokoro": true}

// adapterDaemons is the set of message-posting channel adapters; multi-account
// variants (`<adapter>-<label>`) resolve to the base. Keep in sync with authd
// serviceGrants.
var adapterDaemons = map[string]bool{
	"teled": true, "whapd": true, "discd": true, "slakd": true,
	"bskyd": true, "reditd": true, "emaid": true, "twitd": true, "linkd": true,
	"gitd": true,
}

// envFileName returns the env file a service reads. `<adapter>-<label>` variants
// share the base adapter's file (spec 5/R-multi-account).
//
// A name with no key list gets NO file, not the shared `.env`. The fallback used to
// hand a service every secret the instance holds — the whole `.env`, including
// `SECRETS_KEY` and every channel token — the moment somebody added a stanza without
// adding its key list, and it would have looked like it worked. Nothing takes that path
// today (measured: every `svcDef` name has a `daemonKeys` entry), so the fallback was
// a dead branch whose only possible use was the wrong one.
//
// `checkEveryServiceHasKeys` is the loud half: it refuses generation rather than
// leaving a daemon with no env at all (BUGS `L200`).
func envFileName(name string) string {
	if _, ok := daemonKeys[name]; ok {
		return "env/" + name + ".env"
	}
	if base, _, ok := strings.Cut(name, "-"); ok {
		if _, ok := daemonKeys[base]; ok {
			return "env/" + base + ".env"
		}
	}
	return ""
}

// checkNoServiceReadsTheSharedEnv refuses a rendered compose file in which a service
// points `env_file` at the instance's shared `.env`.
//
// That file holds every secret the instance has — `SECRETS_KEY`, every channel token,
// the model credential. `envFileName` used to fall back to it for any name with no
// `daemonKeys` entry, so adding a stanza without adding its key list handed that
// service the lot, and it looked like it worked. It now returns "" instead, and this
// is the loud half: `env_file: - .env` in the output is the shape to catch, because it
// is the one that ships (BUGS `L200`).
//
// A service with NO `env_file` is fine and not this check's business: `vited` serves a
// read-only volume and reads nothing, and forcing a key list on it would invent one.
func checkNoServiceReadsTheSharedEnv(composeYAML string) error {
	var cur string
	var bad []string
	for _, line := range strings.Split(composeYAML, "\n") {
		if m := serviceKeyRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			continue
		}
		if cur != "" && strings.TrimSpace(line) == "- .env" {
			bad = append(bad, cur)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("compose: %s read the shared .env, which holds every secret the "+
			"instance has: give each an entry in daemonKeys so only its own variables reach it",
			strings.Join(bad, ", "))
	}
	return nil
}

// serviceKeyRe matches a service name under `services:` — two spaces, a name, a colon,
// nothing else. Four spaces is a property of the service above it.
var serviceKeyRe = regexp.MustCompile(`^  ([a-z][a-z0-9_-]*):\s*$`)

// envFileFor returns the scoped env_file block for a daemon stanza.
func envFileFor(name string) string {
	return fmt.Sprintf("    env_file:\n      - %s\n", envFileName(name))
}

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

// writeEnvFiles emits env/<daemon>.env with only the keys each daemon needs;
// perDaemon holds keys scoped to a single daemon that must NOT leak across
// the shared env map.
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

// storeService is the compose service name of the store server; a
// DYNAMODB_URL naming it resolves inside the instance network and nowhere
// else, so it is exactly the value the host CLI cannot use.
const storeService = "extenddb"

// storeHostPortKey names the host half of the store server's published port
// (template/services/extenddb.yaml): docker publishes on it and
// writeOperatorEnv addresses the same port.
const storeHostPortKey = "STORE_HOST_PORT"

// operatorEnvPath is the host CLI's own env file, loaded over .env. It carries
// the operator's store credential, which stays out of .env because .env
// reaches every service without a scoped env file.
func operatorEnvPath(dataDir string) string {
	return filepath.Join(dataDir, "env", "operator.env")
}

// writeOperatorEnv points the host CLI at the store port THIS instance
// publishes: on the host the compose service name in DYNAMODB_URL resolves to
// nothing (BUGS L311). The credential lines are minted by a by-hand `extenddb
// init` and cannot be generated, so the file is never rewritten wholesale: one
// URL line is edited, and only when it is absent, names the in-network
// service, or is the loopback line a previous generate wrote — a moved
// STORE_HOST_PORT has to reach it, or the host CLI keeps dialling the old
// port. An address on any other host is the operator's own and is left alone.
func writeOperatorEnv(dataDir string, env map[string]string) error {
	inNetwork, err := url.Parse(env[dynstore.EnvURL])
	if err != nil {
		// An unset key parses clean to an empty URL, so reaching here means the
		// operator set a value and got it wrong. Skipping the file quietly would
		// leave the host CLI pointed at nothing.
		return fmt.Errorf("parse %s=%q: %w", dynstore.EnvURL, env[dynstore.EnvURL], err)
	}
	if inNetwork.Hostname() != storeService {
		// No store server, or one the operator addressed themselves.
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
	case !isGeneratedStoreLine(lines[at]):
		// The operator's line; only the mode is still ours.
		return chmodOperatorEnv(path)
	default:
		lines[at] = line
	}
	return writeEnvAtomic(dataDir, path, strings.Join(lines, "\n")+"\n")
}

// namesStoreService reports whether a KEY=VALUE line's URL addresses the store
// server by its compose service name; an unparseable value counts, it reaches
// nothing either way.
func isGeneratedStoreLine(line string) bool {
	_, value, _ := strings.Cut(line, "=")
	have, err := url.Parse(strings.TrimSpace(value))
	return err != nil || have.Hostname() == storeService || have.Hostname() == "127.0.0.1"
}

// chmodOperatorEnv takes the file back to 0600 without touching a byte: sloth
// shipped it at 0644 holding the account key that reaches every owner's
// tables (BUGS L311). Best-effort for the same reason mkdirOwned's chown is.
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
// `proxyd_routes` block of their `services/<name>.yaml` manifest.
type ProxydRoute struct {
	Path            string   `json:"path"`
	Backend         string   `json:"backend"`
	Auth            string   `json:"auth"`
	GatedBy         string   `json:"gated_by,omitempty"`
	PreserveHeaders []string `json:"preserve_headers,omitempty"`
	StripPrefix     bool     `json:"strip_prefix,omitempty"`
	RedirectTo      string   `json:"redirect_to,omitempty"`
	// Owner is who may rewrite the row. Generate stamps it from where the
	// route came from — never from the manifest, which a package writes (BUGS L46).
	Owner string `json:"owner,omitempty"`
}

// coreProxydRoutes are the always-emitted routes for core daemons. GatedBy
// maps to the env vars that also decide the daemon's compose profile, so
// route presence tracks the daemon.
var coreProxydRoutes = []ProxydRoute{
	// Per-daemon /dash/<daemon>/ surfaces route to the owning daemon;
	// longest-prefix matching puts them before the catch-all /dash/ → dashd.
	{Path: "/dash/onbod/", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
	{Path: "/dash/routd/", Backend: "http://routd:8080", Auth: "user"},
	{Path: "/dash/authd/", Backend: "http://authd:8080", Auth: "user"},
	// proxyd answers its own dashboard from its mux before this table is
	// consulted; the row reserves the path and lets the hub link it.
	{Path: "/dash/proxyd/", Backend: "http://proxyd:8080", Auth: "user"},
	{Path: "/dash/runed/", Backend: "http://runed:8080", Auth: "user"},
	{Path: "/dash/timed/", Backend: "http://timed:8080", Auth: "user"},
	{Path: "/dash/webd/", Backend: "http://webd:8080", Auth: "user"},
	// /dash/ catch-all → dashd (hub + cross-cutting pages).
	{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
	// The route-token surface (spec 5/W). `optional`: proxyd identifies a
	// caller who offers a bearer and forwards everyone else anonymously — the
	// token is the credential.
	{Path: "/chat/", Backend: "http://webd:8080", Auth: "optional"},
	{Path: "/hook/", Backend: "http://webd:8080", Auth: "optional"},
	// Identity pairing (spec 5/31-identity-pairing). `user` so an anonymous visitor is bounced
	// through OAuth with the pairing URL as the return path.
	{Path: "/pair/", Backend: "http://webd:8080", Auth: "user"},
	// The user portal. `user`: every handler is behind webd's requireUser. Bare
	// + slash because webd's mux 301s /me → /me/, which never happens if proxyd
	// drops /me.
	{Path: "/me", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/me/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/panel/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/api/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/x/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/static/", Backend: "http://webd:8080", Auth: "user"},
	{Path: "/mcp", Backend: "http://webd:8080", Auth: "user"},
	// The browser SDK webd bakes into its binary. Public because a widget on a
	// third-party page loads it with no session (BUGS L322).
	{Path: "/assets/", Backend: "http://webd:8080", Auth: "public"},
	// /slink/* — webd answers 308 → /chat/<token>/. Public.
	{Path: "/slink/", Backend: "http://webd:8080", Auth: "public"},
	{Path: "/dav/", Backend: "http://davd:8080", Auth: "user", StripPrefix: true, GatedBy: "WEBDAV_ENABLED"},
	// Onboarding — `user`, as /pair/: every handler names the caller, and
	// `public` forwards a browser anonymously, so the newcomer these pages
	// exist for arrived unidentified (BUGS L99). Bare, so MatchRoute requires
	// an exact path: a "/onboard/" twin matches the whole SUBTREE, and onbod
	// serves no path under /onboard, so that row forwarded every /onboard/...
	// to a 404 (BUGS L331).
	{Path: "/onboard", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
	{Path: "/invite/", Backend: "http://onbod:8080", Auth: "user", GatedBy: "ONBOARDING_ENABLED"},
}

// gatedByOn reports whether the named env-gate is enabled: empty key = no
// gate; boolean gates require literal "true"; other keys any non-empty value.
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

// collectProxydRoutes returns the routes that survive gated_by filtering, so a
// surface whose daemon is off is not routed at a dead backend (spec 5/7).
// Routes live in the `services/<name>.yaml` manifest, not the fragment,
// because proxyd — not docker — is their consumer. This is the generate-time
// READER of the manifest `arizuko apply` also installs: routes are needed
// before any daemon or DB exists — a second reader, never a second format
// (BUGS L44).
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
			// Checked HERE as well as at install: `packages upgrade` rewrites the
			// fragment without re-applying its rows, so a revision that added
			// `/dash/` would reach proxyd_routes through this path alone (BUGS L46).
			if claimed := ReservedRouteClaim(bundledName(catalogDir, name), r.Path); claimed != "" {
				return nil, fmt.Errorf("%s.yaml declares route %q, which claims the core surface %q; "+
					"a package may not serve core arizuko's own paths (BUGS L46)", name, r.Path, claimed)
			}
			if gatedByOn(env, r.GatedBy) {
				// Stamped from the fragment's name, never trusted from the file: a
				// package that wrote `owner` itself could pass as core-shipped.
				r.Owner = store.RouteOwnerPackage(name)
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// ReservedRoutePaths returns every path core arizuko serves — the ground a
// package may not claim (BUGS L46), derived from coreProxydRoutes so there is
// no second list. Gates are deliberately ignored: a package allowed to take a
// gated path while the gate was off would front-door the surface the moment
// it turned on.
func ReservedRoutePaths() []string {
	out := make([]string, 0, len(coreProxydRoutes))
	for _, r := range coreProxydRoutes {
		out = append(out, r.Path)
	}
	return out
}

// CoreRouteAuth returns the auth mode core declares for the exact path, or ""
// when no core route claims it. The reader is a daemon's own test: a handler
// that keys on the X-User-Sub proxyd stamps is only reachable behind a route
// that makes proxyd stamp one, and that declaration lives here (BUGS L99).
func CoreRouteAuth(path string) string {
	for _, r := range coreProxydRoutes {
		if r.Path == path {
			return r.Auth
		}
	}
	return ""
}

// ReservedRouteClaim returns the core path that `path` would take over, or "".
// `bundled` names a fragment the release itself ships, the ONE exception: such
// a daemon serves its own `/dash/<name>/` console. The name must come from the
// release's catalog, so pass "" for a package installed from a source and for
// an operator's hand-written row; `/dash/` stays closed to both (BUGS L46).
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

// activeProfiles derives COMPOSE_PROFILES from the instance feature flags —
// the single gate that decides which optional daemon docker brings up.
func activeProfiles(env map[string]string) []string {
	var p []string
	if envOr(env, "WEB_PORT", "") != "" {
		// One profile for webd/proxyd/vited/dashd: compose rejects a
		// depends_on into an inactive profile.
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

// writeManagedEnv rewrites the compose-managed block of <dataDir>/.env,
// leaving operator lines untouched. Identity is configured here, never
// derived inside a container.
func writeManagedEnv(dataDir, app, flavor string, profiles []string) error {
	path := filepath.Join(dataDir, ".env")
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Strip only a single well-formed [begin, end] block: a malformed marker
	// fails loud rather than silently dropping every operator line after it.
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
// proxyd route table the same run decided on. The routes are rows the caller
// writes into routd's store, NOT config on the compose file: an env var
// seeded proxyd_routes once and was then ignored forever (BUGS L45).
func Generate(dataDir string) (string, []ProxydRoute, error) {
	env, _ := godotenv.Read(filepath.Join(dataDir, ".env"))
	if env == nil {
		env = map[string]string{}
	}
	seedSharedDefaults(env)

	// Compose owns the project name; the derived names are written into env
	// so daemons inside containers never guess them from filesystem paths.
	project := filepath.Base(dataDir)
	app, flavor, _ := strings.Cut(project, "_")
	if !identRE.MatchString(app) {
		return "", nil, fmt.Errorf("invalid compose project app %q (derived from data dir basename)", app)
	}
	if flavor != "" && !identRE.MatchString(flavor) {
		return "", nil, fmt.Errorf("invalid instance flavor %q (derived from data dir basename)", flavor)
	}
	// ARIZUKO_INSTANCE is the OTLP service.instance.id (spec 5/O-observability).
	if _, ok := env["ARIZUKO_INSTANCE"]; !ok {
		if flavor != "" {
			env["ARIZUKO_INSTANCE"] = flavor
		} else {
			env["ARIZUKO_INSTANCE"] = app
		}
	}
	// Service-key provisioning (spec 5/E-routd + 5/P-runed): each daemon authenticates to
	// authd with its own AUTHD_SERVICE_KEY, which authd recognises via
	// AUTHD_SERVICE_KEYS (hashed at compare time). Keys persist in the
	// per-daemon env files so a redeploy keeps the same identity.
	perDaemon := map[string]map[string]string{}
	_, pinned := env["AUTHD_SERVICE_KEYS"]
	seedKeys := !pinned
	var keyPairs []string
	// wireServiceKey scopes the key into the daemon's env file and, unless the
	// operator pinned AUTHD_SERVICE_KEYS, registers it in the authd seed.
	wireServiceKey := func(daemon string) {
		if _, done := perDaemon[daemon]; done {
			return // already wired (two multi-account variants of one adapter)
		}
		key := provisionServiceKey(dataDir, daemon)
		// AUTHD_SERVICE_NAME pins the exchange principal to the DAEMON name, so
		// an adapter whose CHANNEL_NAME differs exchanges as service:<daemon>.
		perDaemon[daemon] = map[string]string{"AUTHD_SERVICE_KEY": key, "AUTHD_SERVICE_NAME": daemon}
		if seedKeys {
			keyPairs = append(keyPairs, fmt.Sprintf("service:%s=%s", daemon, key))
		}
	}
	wireServiceKey("routd")
	wireServiceKey("runed")
	wireServiceKey("timed")
	wireServiceKey("onbod")
	wireServiceKey("proxyd")
	wireServiceKey("webd")
	wireServiceKey("dashd")
	if _, ok := env["AUTHD_URL"]; !ok {
		env["AUTHD_URL"] = "http://authd:8080"
	}
	// routd federates /invite + /gate to onbod (spec 5/16-mcp-rest-unification); harmless when onbod
	// does not run (nil client).
	if _, ok := env["ONBOD_URL"]; !ok {
		env["ONBOD_URL"] = "http://onbod:8080"
	}

	servicesDir := filepath.Join(dataDir, "services")
	services, err := readFragments(servicesDir)
	if err != nil {
		return "", nil, err
	}

	// Each message-posting adapter in services/ gets a service:<adapter> key
	// (spec 5/1); multi-account variants reuse the base adapter's env file.
	for _, name := range services {
		base, _, _ := strings.Cut(name, "-")
		if adapterDaemons[base] {
			wireServiceKey(base)
		}
	}
	if seedKeys {
		env["AUTHD_SERVICE_KEYS"] = strings.Join(keyPairs, ",")
	}

	// Store credentials (spec 6/42-dynamo-over-postgres): one access key per daemon, written by the
	// operator into that daemon's env file and carried forward on regenerate.
	// It stays OUT of the shared .env. This runs after the service keys so it
	// merges into their maps rather than standing where wireServiceKey tests
	// for its own work.
	mergeStoreCredentials(dataDir, perDaemon)

	// What each package needs from .env, resolved before any env file is
	// written, so a minted key reaches the daemons that read it and a package
	// default gates its routes below.
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

	// Fatal on failure: a silent one lets generation succeed while a daemon's
	// env file goes stale — the `/login` outage class (BUGS C11).
	if werr := writeEnvFiles(dataDir, env, perDaemon); werr != nil {
		return "", nil, fmt.Errorf("write per-daemon env files: %w", werr)
	}
	// Same shape one door along: silence leaves every `arizuko <cmd> <inst>`
	// dialling a name only a container resolves (BUGS L311).
	if werr := writeOperatorEnv(dataDir, env); werr != nil {
		return "", nil, fmt.Errorf("write the operator env file: %w", werr)
	}

	// app-src/ holds the release's ant/, staged by runed. Generation creates
	// it because generation runs as root while runed runs as uid 1000 and the
	// data-dir root is not writable by it.
	if derr := mkdirOwned(filepath.Join(dataDir, "app-src"), containerUID); derr != nil {
		fmt.Fprintf(os.Stderr, "compose: app-src: %v\n", derr)
	}

	routes, err := collectProxydRoutes(servicesDir, catalogDir, services, env)
	if err != nil {
		return "", nil, err
	}
	// Fatal: without the managed block every fragment interpolates to an empty
	// container name and an empty host mount.
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
	// authd + routd + runed are the only topology (spec 5/E-routd + 5/P-runed), so they
	// carry no profile.
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
	// No egress proxy service: runed starts one `egress` PROCESS per turn on the host
	// side of that turn's tap and kills it with the turn (spec 6/41 §"Egress by name,
	// sealed below the kernel"). There is no image to name: EGRESS_IMAGE went with the
	// Docker substrate, and runed refuses to start without the proxy binary.
	out := b.String()
	// The last gate before the file ships: a service nobody declared env keys for
	// names no env_file and would come up unconfigured (BUGS `L200`).
	if err := checkNoServiceReadsTheSharedEnv(out); err != nil {
		return "", nil, err
	}
	return out, routes, nil
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

// appSrc decides where routd and runed read `ant/` from, returning the volume
// line (empty when none) and the APP_SRC_DIR value. The default is
// bakedAppSrc: a release IS an image. APP_SRC_DEV bind-mounts HOST_APP_DIR
// read-only instead — a developer loop, never a deployment: krons served
// skills off the checkout, so an edit reached production with no build, tag
// or restart (BUGS M1).
func appSrc(env map[string]string) (volume, dir string) {
	hostApp := envOr(env, "HOST_APP_DIR", "")
	if envOr(env, "APP_SRC_DEV", "") == "" || hostApp == "" {
		return "", bakedAppSrc
	}
	return fmt.Sprintf("%s:%s:ro", hostApp, containerSrcMount), containerSrcMount
}

// yamlQuote emits a double-quoted YAML scalar with escapes, so a value holding
// newlines or quotes cannot inject.
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

// mergeStoreCredentials folds each daemon's own store key out of its scoped
// env file into perDaemon. Neither key present is NORMAL — no store identity
// yet. Exactly one is a fault: skipping it silently turns a config error here
// into an auth failure on that daemon's first store call.
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

// warnPartialEnrolment reports an enrolment that stopped half way.
// store.StoreReach declares which daemons need a key, so this is a second
// reader, not a list to drift (BUGS L319). NONE enrolled is silent (a fresh
// `arizuko create`), ALL enrolled is done; only the middle is a fault — the
// L45 class, a generated value that silently stops applying.
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

// seedSharedDefaults fills the env keys whose default is DECLARED in core.
// Both values are read from there, never respelled: a second literal here
// reaches every daemon's scoped env file and drifts from the constant
// (BUGS L340).
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

// dataMounts returns the bind-mount specs rooting a daemon at
// containerDataMount. With no subdirs it gets the whole instance tree; naming
// subdirs mounts only those children (spec 5/16, BUGS A1). Sources must
// pre-exist: docker creates a missing bind source as root.
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
	// COMPOSE_PROFILES (writeManagedEnv) is the only gate on a profiled service.
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
	// DATA_DIR is the container-internal mount point, which .env does not know.
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

// authdService emits the auth authority: NO message DB, NO docker socket, NO
// egress proxy, no data mount, and no depends_on — nothing it relies on must
// come up first.
func authdService(app, flavor string) string {
	var b strings.Builder
	b.WriteString("  authd:\n")
	fmt.Fprintf(&b, "    container_name: %s_authd_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['authd']\n")
	b.WriteString("    user: '1000:1000'\n")
	b.WriteString(envFileFor("authd"))
	b.WriteString("    environment:\n")
	// authd snapshots login/refresh scopes from routd's ACL owner; unset ->
	// empty-scope sessions (spec 5/1-auth-standalone).
	fmt.Fprintf(&b, "      GRANTS_URL: '%s'\n", routdURL)
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

// routdService emits the canonical router: NO egress proxy, NO docker socket.
// Publishes API_PORT:8080 on loopback so the host CLI reaches /v1/channels;
// only proxyd is an entry point from off the host (BUGS L392).
func routdService(app, flavor, dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", fmt.Sprintf("%d", core.DefaultAPIPort))
	def := svcDef{
		name:       "routd",
		app:        app,
		flavor:     flavor,
		entrypoint: "routd",
		dataDir:    dataDir,
		ports:      []string{fmt.Sprintf("127.0.0.1:%s:%d", apiPort, core.DefaultAPIPort)},
		dependsOn:  "authd, runed",
	}
	// routd reads ant/skills/self/MIGRATION_VERSION to enqueue /migrate; pointed
	// at the wrong tree the auto-migrate trigger silently never fires.
	vol, src := appSrc(env)
	def.environment = map[string]string{"APP_SRC_DIR": src}
	if vol != "" {
		def.volumes = []string{vol}
	}
	return writeSvc(def)
}

// hostViewPaths are the host paths runed must be able to open: the operator's
// shared codex login, every root the operator allows a group to mount, and the two
// RELEASE ARTIFACTS the guest boots from. Empty values and duplicates are dropped,
// and the order is stable so a regenerate is a no-op.
//
// The kernel and the rootfs are named by version and content hash, so one copy
// serves every instance on the host — two instances on the same `arizuko-ant`
// image produce byte-identical trees, and a second 7 GB export buys nothing. A
// value INSIDE the data dir needs no mount, which is what the prefix test below
// answers; a value outside it is a shared artifact and gets one.
func hostViewPaths(env map[string]string) []string {
	var out []string
	seen := map[string]bool{containerDataMount: true}
	for _, v := range append([]string{
		env["HOST_CODEX_DIR"], env["RUNED_KERNEL_DIR"], env["RUNED_ROOTFS_DIR"],
	}, core.ParseCSV(env["MOUNT_ALLOWED_ROOTS"])...) {
		v = strings.TrimSpace(v)
		if v == "" || !strings.HasPrefix(v, "/") || seen[v] {
			continue
		}
		// Already reachable: the data dir is mounted whole.
		if strings.HasPrefix(v, containerDataMount+"/") {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// runedService emits the execution plane: one KVM guest per turn, so three device
// nodes and two capabilities, and NO docker socket (spec 6/41 §"The runed deploy
// topology"). The docker-substrate branch that mounted /var/run/docker.sock went
// with the substrate at step 4 of that spec's order.
func runedService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  runed:\n")
	fmt.Fprintf(&b, "    container_name: %s_runed_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['runed']\n")
	// Root, because virtiofsd carries guest credentials to host files and the
	// staging tree's binds are made in runed's own mount namespace. The privilege
	// does not RISE: the docker socket it replaces was already root-equivalent on
	// the host. It is explicit instead — three nodes and two capabilities, listed
	// in one generator.
	b.WriteString("    user: '0:0'\n")
	b.WriteString("    devices:\n")
	b.WriteString("      - /dev/kvm\n")
	b.WriteString("      - /dev/vhost-vsock\n")
	b.WriteString("      - /dev/net/tun\n")
	// NET_ADMIN for the tap and its rule set; SYS_ADMIN for the binds.
	b.WriteString("    cap_add: [NET_ADMIN, SYS_ADMIN]\n")
	// AppArmor's docker-default denies mount(2) for every container WHATEVER its
	// capabilities, so SYS_ADMIN alone is necessary and not sufficient: the staging
	// tree's binds answer "permission denied" and every turn fails (BUGS `L371`,
	// measured both ways on krons). Seccomp needs nothing — docker's default
	// profile already allows mount to a holder of CAP_SYS_ADMIN. A custom profile
	// that permits this one mount is tighter and is the operator's to write.
	b.WriteString("    security_opt: [apparmor=unconfined]\n")
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s:%s\n", dataDir, containerDataMount)
	// A guest can be shown only what RUNED can open: virtiofsd is runed's own
	// child, not a daemon on the host, so a host path outside the data dir needs a
	// mount here or the turn's staging tree refuses it (BUGS `L370`). Each one
	// lands at the SAME absolute path, so the string the renderer already holds
	// resolves and no code reads a second spelling. Read-only, because these are
	// the operator's own files and a turn has no business writing them.
	for _, p := range hostViewPaths(env) {
		fmt.Fprintf(&b, "      - %s:%s:ro\n", p, p)
	}
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

// timedService emits the scheduler. NO data-dir mount: timed federates its
// fire loop over routd HTTP and reads no file.
func timedService(app, flavor string, env map[string]string) string {
	// timed reads TIMEZONE while the rest of the world uses TZ.
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
	// ONBOARDING_ENABLED is forced true inside the container: the profile
	// decides whether onbod runs. groups/ + web/ only: SetupGroup creates the
	// folder and its web slots; it never touches ipc/, so a new world can be
	// provisioned without reach over any running agent's MCP socket.
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
	// dashd's stock-skills picker enumerates ant/skills/ through appSrc, not a
	// host path read from inside the container (BUGS F54).
	vol, src := appSrc(env)
	def := svcDef{
		name:       "dashd",
		app:        app,
		flavor:     flavor,
		entrypoint: "dashd",
		dataDir:    dataDir,
		// groups/ + surrogate/ only: a whole-tree mount hands dashd .env — every
		// secret — and ipc/, the live agent MCP sockets (BUGS F51).
		dataSubdirs: []string{"groups", "surrogate"},
		profile:     "web",
		// The internal listen stays :8080 (the healthcheck target) even when
		// .env sets DASH_PORT for host-side publish.
		environment: map[string]string{
			"DASH_PORT":   fmt.Sprintf("%d", core.DefaultAPIPort),
			"APP_SRC_DIR": src,
		},
	}
	if vol != "" {
		def.volumes = []string{vol}
	}
	if dashPort := envOr(env, "DASH_PORT", ""); dashPort != "" {
		def.ports = []string{"127.0.0.1:" + dashPort + ":8080"}
	}
	return writeSvc(def)
}

// proxydService emits proxyd's compose block. It carries no route config: the
// route table is rows this generate run writes, read live per request.
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
	// dashd + webd share proxyd's `web` profile: compose rejects a depends_on
	// into an inactive profile. No data mount: proxyd reads routd's tables on
	// the store server and serves no file from the instance tree.
	return writeSvc(svcDef{
		name:       "proxyd",
		app:        app,
		flavor:     flavor,
		entrypoint: "proxyd",
		profile:    "web",
		ports:      ports,
		dependsOn:  "routd, dashd, webd",
	})
}

func davdService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  davd:\n")
	fmt.Fprintf(&b, "    container_name: %s_davd_%s\n", app, flavor)
	// sigoden/dufs wrapped in alpine, which adds wget for the healthcheck.
	b.WriteString("    image: arizuko-davd:latest\n")
	b.WriteString("    profiles: ['davd']\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s/groups:/data\n", dataDir)
	if davPort := envOr(env, "DAV_PORT", ""); davPort != "" {
		b.WriteString("    ports:\n")
		fmt.Fprintf(&b, "      - '127.0.0.1:%s:8080'\n", davPort)
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
	// ROUTER_URL is pinned so webd never falls back to a code default. No data
	// mount: webd serves the chat widget from the binary, not from web/.
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
	// Read-only: vite's dep cache lives at a container-local cacheDir.
	fmt.Fprintf(&b, "    volumes:\n      - %s/web:/web:ro\n", dataDir)
	// The vite dev server has no /health; /@vite/client is always 200 in dev.
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: ['CMD', 'wget', '-qO-', '--tries=1', '--timeout=3', 'http://127.0.0.1:8080/@vite/client']\n")
	b.WriteString("      interval: 30s\n      timeout: 5s\n      retries: 3\n      start_period: 15s\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

// envOr reads a generation input from the INSTANCE's `.env` and nowhere else, falling
// back only to the literal default the call site names.
//
// It used to consult the generator's own process environment in between. That made the
// rendered compose file depend on ambient state: `arizuko generate` runs inside a
// container (the systemd unit's ExecStartPre), so a key absent from the instance's
// `.env` could be satisfied by whatever that container happened to carry, and the same
// instance rendered differently on two runs with nobody able to see why.
//
// A DEFAULT is fine and several call sites need one — a port, a timezone, a boolean's
// off state. A FALLBACK to a second source is not: the `.env` is the declaration, and
// `generate` writes every key it needs into it, so a second source can only ever
// disagree with the first.
func envOr(env map[string]string, key, fallback string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	return fallback
}
