package core

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// ContainerHome is the home directory inside every agent container.
// ipc, gateway, and container/runner all need this; it lives here to
// avoid import cycles (container imports ipc).
const ContainerHome = "/home/node"

// DefaultImage is the agent container image name used when CONTAINER_IMAGE
// is not set. cmd/arizuko seeds this value into generated .env files; both
// must stay in sync.
const DefaultImage = "arizuko-ant:latest"

// DefaultAPIPort is the default daemon HTTP listen port, used as the default
// for API_PORT in config, compose generation, and seeded .env files.
const DefaultAPIPort = 8080

// The defaults routd's main reads as well as LoadConfig. TWO READERS OF ONE ENV
// VAR ARE FINE; two declarations of its DEFAULT are not — they drift silently
// and the drift only shows on the instance that hits the fallback. routd builds
// its LoopConfig from the environment directly rather than through LoadConfig,
// so these are what stop the two from disagreeing (BUGS L194, the shape F71
// already paid for).
const (
	DefaultAssistantName = "arizuko"
	DefaultWhisperModel  = "turbo"
	DefaultMediaMaxBytes = 20 * 1024 * 1024
)

// AppSrcDir resolves where the release's own tree lives, for every reader.
//
// IT IS A FUNCTION BECAUSE THE FALLBACK CHAIN IS THE PART THAT DRIFTED: this
// resolves APP_SRC_DIR, then HOST_APP_DIR, then the running binary's directory.
// routd's own copy stopped at HOST_APP_DIR and fell to "", so a bare `routd`
// outside compose resolved an empty app dir while every other reader resolved
// the binary's. Compose always sets HOST_APP_DIR, which is exactly why the
// difference stayed latent (BUGS L194).
func AppSrcDir() string {
	return EnvOr("APP_SRC_DIR", EnvOr("HOST_APP_DIR", execDir()))
}

type Config struct {
	Name          string
	TelegramToken string
	Image         string
	// ModelGatewayURL points every harness at an LLM gateway that holds the
	// real model credential, meters and logs; ModelGatewayKey is the virtual
	// credential the container presents to it. One declaration, translated
	// per harness in ant/src/gateway.ts. Empty = the harness's own
	// credential, unchanged. Static infra config, like AgentBackend.
	ModelGatewayURL string
	ModelGatewayKey string
	// AgentBackend selects the container occupant (spec 6/16 — the harness
	// seam): claude (default, empty), codex, pi. Static infra config — a
	// backend exists only if its CLI is baked into Image — so it lives in the
	// instance .env, never the DB.
	AgentBackend        string
	Timeout             time.Duration
	IdleTimeout         time.Duration
	MaxContainers       int
	Timezone            string
	AuthSecret          string
	WebHost             string
	AuthBaseURL         string
	GitHubClientID      string
	GitHubSecret        string
	DiscordClientID     string
	DiscordSecret       string
	GoogleClientID      string
	GoogleSecret        string
	GoogleAllowedEmails string
	GitHubAllowedOrg    string

	// Dex login (spec 6/22 step 4); unset → /auth/dex stays unmounted.
	DexIssuer   string
	DexClientID string
	DexSecret   string

	ProjectRoot     string
	HostProjectRoot string
	HostAppDir      string
	AppSrcDir       string // in-container source path; defaults to HostAppDir via EffectiveAppSrcDir()
	StoreDir        string
	GroupsDir       string
	IpcDir          string
	WebDir          string

	// MountAllowedRoots is the operator-level gate for GroupConfig.Mounts:
	// comma-separated host paths, and only paths under these roots may be
	// bind-mounted into agent containers. All additional mounts are read-only
	// unless the root also sets AllowReadWrite. Empty → deny all.
	MountAllowedRoots []string

	// HostCodexDir, when non-empty, bind-mounts a host-side codex login state
	// dir into each spawned agent at /home/node/.codex (rw), so agents share the
	// operator's `codex login` auth without per-folder secrets. Empty disables the
	// mount; agents fall back to CODEX_API_KEY / OPENAI_API_KEY.
	HostCodexDir string

	APIPort int

	MediaEnabled  bool
	MediaMaxBytes int64
	VoiceEnabled  bool
	VideoEnabled  bool
	WhisperModel  string

	// Egress isolation (spec 6/24). Enabled when EgressImage is non-empty.
	// No separate EGRESS_ISOLATION switch — name the image or don't.
	// Per-folder networks are created lazily under EgressNetworkPrefix
	// with /24s carved from EgressParentSubnet; each spawn gets its own
	// proxy container from EgressImage, with its allowlist baked in.
	EgressNetworkPrefix string // e.g. "arizuko_krons" — folder networks are <prefix>_<sanitized-folder>
	EgressImage         string // image holding the `egress` binary (e.g. arizuko-egress:latest)
	EgressUplinkNetwork string // network giving each spawn's proxy its route out (compose project default)
	EgressParentSubnet  string // parent CIDR carved into per-folder /24s (default 10.99.0.0/16)

	// ENGAGEMENT_TTL, the TTS_*, SEND_DISABLED_*, OBSERVE_WINDOW_*, retry and
	// cost-cap vars belong to routd alone (routd/cmd/routd/main.go) and must
	// not gain a field here: a second default nothing reads is what the
	// operator docs quoted for months (BUGS F12).

	// SecretsKey is the AES-256-GCM keyring for the secrets table (spec 5/14).
	// Required by routd — secrets are encrypted at rest, with no plaintext mode and
	// no AUTH_SECRET fallback. Comma-separate to rotate: the first key seals new
	// writes, the rest decrypt-only. Parse with SecretKeyring.
	SecretsKey string
}

// SecretKeyring splits a SECRETS_KEY value into its keyring: comma-separated,
// the first non-empty entry is the active seal key and the rest are decrypt-only
// (rotation/migration). Returns nil when raw is empty/blank.
func SecretKeyring(raw string) [][]byte {
	var out [][]byte
	for p := range strings.SplitSeq(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, []byte(p))
		}
	}
	return out
}

// ErrNoSecretsKey reports an absent or blank SECRETS_KEY.
var ErrNoSecretsKey = errors.New("SECRETS_KEY required: secrets are encrypted at rest; set SECRETS_KEY in the instance .env")

// RequireSecretKeyring parses SECRETS_KEY and REFUSES an empty result, so one rule
// decides the question for every caller that writes secrets. store.storeValue
// enforces the same rule at the write itself. An earlier build only logged a warning,
// so an un-keyed instance persisted every secret readable on disk while each
// healthcheck reported it healthy (BUGS S2).
func RequireSecretKeyring(raw string) ([][]byte, error) {
	kr := SecretKeyring(raw)
	if len(kr) == 0 {
		return nil, ErrNoSecretsKey
	}
	return kr, nil
}

// LoadConfigFrom loads dir/.env and resolves the config with dir as the
// data root unless DATA_DIR (env or .env) overrides. Host CLI callers pass
// the instance dir explicitly; without this default the root fell back to
// the operator's cwd and `arizuko create`/`group add` wrote the groups/
// skeleton into whatever directory the command ran from (2026-07-11).
func LoadConfigFrom(dir string) (*Config, error) {
	LoadInstanceEnv(dir)
	if os.Getenv("DATA_DIR") == "" {
		if err := os.Setenv("DATA_DIR", dir); err != nil {
			return nil, err
		}
	}
	return LoadConfig()
}

// loadedInstanceDir is the dir of the first LoadInstanceEnv call in this process.
var loadedInstanceDir string

// LoadInstanceEnv puts one instance's environment into this process: its .env,
// and the operator's own store credential from its own file. That credential is
// not in .env because .env reaches every service that has no scoped env file,
// and it reaches every owner's tables.
//
// Every host CLI door calls it, and that is not a convenience: DYNAMODB_URL
// decides whether a store is the server or a file, so a subcommand that skipped
// this wrote to the file while every daemon read the server (BUGS L294).
//
// ONE INSTANCE PER PROCESS. Both mutations below are one-way — godotenv writes
// into the process env and nothing unsets it, and LoadConfigFrom pins DATA_DIR
// only when it is empty — so a second dir silently inherits the first one's
// values. Nothing in the tree loops instances today; the warning names the
// constraint for the day something does (BUGS L50).
func LoadInstanceEnv(dir string) {
	if loadedInstanceDir == "" {
		loadedInstanceDir = dir
	} else if loadedInstanceDir != dir {
		slog.Warn("LoadInstanceEnv: a second instance in one process inherits the "+
			"first one's env; every var already set keeps the first instance's value "+
			"and DATA_DIR still points at it",
			"first", loadedInstanceDir, "now", dir)
	}
	// A process that already names the store is inside the instance's network
	// and keeps what it was given; the operator's file is the HOST's view of the
	// same server, and only fills in for a process that has none.
	inNetwork := os.Getenv("DYNAMODB_URL") != ""
	_ = godotenv.Load(filepath.Join(dir, ".env"))
	if inNetwork {
		// The credential still comes from the operator's own file — it is not in
		// .env, which every service without a scoped env file reads.
		url := os.Getenv("DYNAMODB_URL")
		_ = godotenv.Overload(filepath.Join(dir, "env", "operator.env"))
		_ = os.Setenv("DYNAMODB_URL", url)
		return
	}
	_ = godotenv.Overload(filepath.Join(dir, "env", "operator.env"))
}

func LoadConfig() (*Config, error) {
	_ = godotenv.Load(".env")

	root := EnvOr("DATA_DIR", mustCwd())
	hostRoot := EnvOr("HOST_DATA_DIR", root)
	appDir := EnvOr("HOST_APP_DIR", execDir())
	appSrcDir := AppSrcDir()
	name := EnvOr("ASSISTANT_NAME", DefaultAssistantName)

	c := &Config{
		Name:                name,
		TelegramToken:       EnvOr("TELEGRAM_BOT_TOKEN", ""),
		Image:               EnvOr("CONTAINER_IMAGE", DefaultImage),
		AgentBackend:        EnvOr("ARIZUKO_BACKEND", ""),
		ModelGatewayURL:     EnvOr("MODEL_GATEWAY_URL", ""),
		ModelGatewayKey:     EnvOr("MODEL_GATEWAY_KEY", ""),
		Timeout:             envDur("CONTAINER_TIMEOUT", 60*time.Minute),
		IdleTimeout:         envDur("IDLE_TIMEOUT", 60*time.Minute),
		MaxContainers:       EnvInt("MAX_CONCURRENT_CONTAINERS", 5),
		Timezone:            Timezone(),
		AuthSecret:          EnvOr("AUTH_SECRET", ""),
		WebHost:             EnvOr("WEB_HOST", ""),
		AuthBaseURL:         EnvOr("AUTH_BASE_URL", ""),
		GitHubClientID:      EnvOr("GITHUB_CLIENT_ID", ""),
		GitHubSecret:        EnvOr("GITHUB_CLIENT_SECRET", ""),
		DiscordClientID:     EnvOr("DISCORD_CLIENT_ID", ""),
		DiscordSecret:       EnvOr("DISCORD_CLIENT_SECRET", ""),
		GoogleClientID:      EnvOr("GOOGLE_CLIENT_ID", ""),
		GoogleSecret:        EnvOr("GOOGLE_CLIENT_SECRET", ""),
		GoogleAllowedEmails: EnvOr("GOOGLE_ALLOWED_EMAILS", ""),
		GitHubAllowedOrg:    EnvOr("GITHUB_ALLOWED_ORG", ""),

		DexIssuer:   EnvOr("DEX_ISSUER", ""),
		DexClientID: EnvOr("DEX_CLIENT_ID", ""),
		DexSecret:   EnvOr("DEX_CLIENT_SECRET", ""),

		ProjectRoot:       root,
		HostProjectRoot:   hostRoot,
		HostAppDir:        appDir,
		AppSrcDir:         appSrcDir,
		StoreDir:          filepath.Join(root, "store"),
		GroupsDir:         filepath.Join(root, "groups"),
		IpcDir:            filepath.Join(root, "ipc"),
		WebDir:            filepath.Join(root, "web"),
		HostCodexDir:      EnvOr("HOST_CODEX_DIR", ""),
		MountAllowedRoots: ParseCSV(EnvOr("MOUNT_ALLOWED_ROOTS", "")),

		APIPort: EnvInt("API_PORT", DefaultAPIPort),

		MediaEnabled:  EnvOr("MEDIA_ENABLED", "false") == "true",
		MediaMaxBytes: int64(EnvInt("MEDIA_MAX_FILE_BYTES", DefaultMediaMaxBytes)),
		VoiceEnabled:  EnvOr("VOICE_TRANSCRIPTION_ENABLED", "false") == "true",
		VideoEnabled:  EnvOr("VIDEO_TRANSCRIPTION_ENABLED", "false") == "true",
		WhisperModel:  EnvOr("WHISPER_MODEL", DefaultWhisperModel),

		EgressNetworkPrefix: EnvOr("EGRESS_NETWORK_PREFIX", ""),
		EgressImage:         EnvOr("EGRESS_IMAGE", ""),
		EgressUplinkNetwork: EnvOr("EGRESS_UPLINK_NETWORK", ""),
		EgressParentSubnet:  EnvOr("EGRESS_SUBNET", "10.99.0.0/16"),

		SecretsKey: EnvOr("SECRETS_KEY", ""),
	}

	// Validation of the egress vars lives in runed (the only daemon that uses
	// egress isolation, in docker.go), not here — other daemons call
	// LoadConfig too and must not fail when egress is on.

	// ASSISTANT_NAME and data dir basename end up in container_name and
	// YAML scalars unquoted — reject anything that would break them.
	if strings.ContainsAny(c.Name, " \t\r\n:'\"\\/") {
		return nil, fmt.Errorf("ASSISTANT_NAME %q contains whitespace or special characters", c.Name)
	}
	flavor := filepath.Base(c.ProjectRoot)
	if strings.ContainsAny(flavor, " \t\r\n:'\"\\") {
		return nil, fmt.Errorf("data dir basename %q contains whitespace or special characters", flavor)
	}

	return c, nil
}

// EffectiveAppSrcDir returns the in-container source path for reading ant/
// skills and output-styles. Falls back to HostAppDir so tests that only set
// HostAppDir work without also needing AppSrcDir explicitly.
func (c *Config) EffectiveAppSrcDir() string {
	if c.AppSrcDir != "" {
		return c.AppSrcDir
	}
	return c.HostAppDir
}

// env helpers: core owns these so it does not depend on the channel-adapter
// library (chanlib) for trivial parsing — that edge created an auth→core→chanlib
// cycle once chanlib started exchanging service tokens via auth (spec 5/1).
// An unset OR unparseable value lands on the fallback, never on the zero
// value: a poll interval of 0 is a busy loop. DurOr reads a Go duration
// ("20m"); envDur parses integer milliseconds (the legacy
// CONTAINER_TIMEOUT/IDLE_TIMEOUT encoding).
func EnvOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func EnvInt(k string, fallback int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func DurOr(k string, fallback time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envDur(k string, fallback time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return fallback
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

func execDir() string {
	ex, err := os.Executable()
	if err != nil {
		return mustCwd()
	}
	return filepath.Dir(ex)
}

func ParseCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// PublicBaseURL turns a configured WEB_HOST into a base URL that always carries a
// scheme, so callers can concatenate a path onto it.
//
// WEB_HOST is a BARE hostname on every live instance while AUTH_BASE_URL carries the
// scheme. Concatenating a path onto the bare value hands the user
// "krons.fiu.wtf/pair/x": Telegram autolinks a bare domain, most clients do not, and
// a browser resolves it as a relative path. Normalising here is the one place, never
// a per-call-site "https://" prefix (BUGS F43).
//
// A value that already carries a scheme is returned unchanged; localhost gets http.
func PublicBaseURL(webHost string) string {
	host := strings.TrimRight(strings.TrimSpace(webHost), "/")
	switch {
	case host == "":
		return ""
	case strings.Contains(host, "://"):
		return host
	case strings.HasPrefix(host, "localhost"), strings.HasPrefix(host, "127.0.0.1"):
		return "http://" + host
	default:
		return "https://" + host
	}
}
