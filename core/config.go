package core

import (
	"fmt"
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

type Config struct {
	Name                string
	TelegramToken       string
	Image               string
	Timeout             time.Duration
	IdleTimeout         time.Duration
	MaxContainers       int
	PollInterval        time.Duration
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

	ProjectRoot     string
	HostProjectRoot string
	HostAppDir      string
	AppSrcDir       string // in-container source path; defaults to HostAppDir via EffectiveAppSrcDir()
	StoreDir        string
	GroupsDir       string
	IpcDir          string
	HostGroupsDir   string
	WebDir          string

	// MountAllowedRoots is the operator-level gate for GroupConfig.Mounts.
	// Comma-separated host paths; only paths under these roots may be
	// bind-mounted into agent containers. All additional mounts are read-only
	// unless the root also sets AllowReadWrite (not exposed via env — set via
	// code if needed). Empty → deny all additional mounts (default).
	// Set via MOUNT_ALLOWED_ROOTS env var.
	MountAllowedRoots []string

	// HostCodexDir, when non-empty, bind-mounts a host-side codex login
	// state dir (typically `~/.codex` on the host) into each spawned
	// agent at /home/node/.codex (rw). Lets agents using the `oracle`
	// skill share the operator's `codex login` ChatGPT/API auth without
	// per-folder secrets. Empty disables the mount; agents fall back to
	// CODEX_API_KEY / OPENAI_API_KEY env vars from folder secrets.
	HostCodexDir string

	APIPort              int
	OnboardingEnabled    bool
	OnboardingPlatforms  []string
	SendDisabledChannels []string
	SendDisabledGroups   []string

	// Observe-mode context window defaults (per-route overrides on
	// routes.observe_window_messages / routes.observe_window_chars).
	ObserveWindowMessages int
	ObserveWindowChars    int

	MediaEnabled  bool
	MediaMaxBytes int64
	WhisperURL    string
	VoiceEnabled  bool
	VideoEnabled  bool
	WhisperModel  string

	// TTS pipeline. TTSURL is the OpenAI-compatible /v1/audio/speech base
	// URL (point at bundled ttsd or any external server). TTSVoice is the
	// instance default; agents can override per-call via send_voice args
	// or per-group via SOUL.md `voice:` frontmatter.
	TTSEnabled bool
	TTSURL     string
	TTSVoice   string
	TTSModel   string
	TTSTimeout time.Duration

	// Egress isolation (crackbox). Enabled when EgressAPI is non-empty.
	// No separate EGRESS_ISOLATION switch — set the API URL or don't.
	// Per-folder networks are created lazily under EgressNetworkPrefix
	// with /24s carved from EgressParentSubnet.
	EgressNetworkPrefix string // e.g. "arizuko_krons" — folder networks are <prefix>_<sanitized-folder>
	EgressCrackbox      string // crackbox container name (attached to every folder network)
	EgressAPI           string // crackbox admin HTTP API base URL (e.g. http://crackbox:3129)
	EgressProxyURL      string // HTTP(S)_PROXY value for the agent (e.g. http://crackbox:3128)
	EgressParentSubnet  string // parent CIDR carved into per-folder /24s (default 10.99.0.0/16)
	EgressAdminSecret   string // optional bearer token for crackbox admin API mutations

	// Cost caps (spec 5/34). Enabled by default; pre-spawn gate consults
	// per-folder + per-user caps in store/cost_log. Set false to bypass
	// the gate entirely (escape hatch for operators).
	CostCapsEnabled bool

	// EngagementTTL is the stay-in-conversation window (spec 5/G).
	// On a bot outbound or inbound verb=mention, engaged_until is set
	// to now+TTL. Engaged (jid, topic) pairs fall through the routing
	// miss branch to whichever folder most recently spoke there.
	EngagementTTL time.Duration

	// MaxTurnRetry is the maximum number of retry attempts when a turn
	// fails without delivering a reply (SIGKILL/OOM/timeout). Default 3.
	MaxTurnRetry int

	// SecretsKey is the AES-256-GCM keyring for the secrets table (spec 6/Y).
	// Required by gated — secrets are encrypted at rest, no plaintext mode and no
	// AUTH_SECRET fallback. Comma-separate to rotate: the first key seals new
	// writes, the rest decrypt-only (retired). Parse with SecretKeyring.
	SecretsKey string
}

// SecretKeyring splits a SECRETS_KEY value into its keyring: comma-separated,
// the first non-empty entry is the active seal key and the rest are decrypt-only
// (rotation/migration). Returns nil when raw is empty/blank.
func SecretKeyring(raw string) [][]byte {
	var out [][]byte
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, []byte(p))
		}
	}
	return out
}

func LoadConfigFrom(dir string) (*Config, error) {
	_ = godotenv.Load(filepath.Join(dir, ".env"))
	return LoadConfig()
}

func LoadConfig() (*Config, error) {
	_ = godotenv.Load(".env")

	root := envOr("DATA_DIR", mustCwd())
	hostRoot := envOr("HOST_DATA_DIR", root)
	appDir := envOr("HOST_APP_DIR", execDir())
	appSrcDir := envOr("APP_SRC_DIR", appDir)
	name := envOr("ASSISTANT_NAME", "Andy")

	c := &Config{
		Name:                name,
		TelegramToken:       envOr("TELEGRAM_BOT_TOKEN", ""),
		Image:               envOr("CONTAINER_IMAGE", DefaultImage),
		Timeout:             envDur("CONTAINER_TIMEOUT", 60*time.Minute),
		IdleTimeout:         envDur("IDLE_TIMEOUT", 60*time.Minute),
		MaxContainers:       envInt("MAX_CONCURRENT_CONTAINERS", 5),
		PollInterval:        2 * time.Second,
		Timezone:            resolveTimezone(),
		AuthSecret:          envOr("AUTH_SECRET", ""),
		WebHost:             envOr("WEB_HOST", ""),
		AuthBaseURL:         envOr("AUTH_BASE_URL", ""),
		GitHubClientID:      envOr("GITHUB_CLIENT_ID", ""),
		GitHubSecret:        envOr("GITHUB_CLIENT_SECRET", ""),
		DiscordClientID:     envOr("DISCORD_CLIENT_ID", ""),
		DiscordSecret:       envOr("DISCORD_CLIENT_SECRET", ""),
		GoogleClientID:      envOr("GOOGLE_CLIENT_ID", ""),
		GoogleSecret:        envOr("GOOGLE_CLIENT_SECRET", ""),
		GoogleAllowedEmails: envOr("GOOGLE_ALLOWED_EMAILS", ""),
		GitHubAllowedOrg:    envOr("GITHUB_ALLOWED_ORG", ""),

		ProjectRoot:     root,
		HostProjectRoot: hostRoot,
		HostAppDir:      appDir,
		AppSrcDir:       appSrcDir,
		StoreDir:        filepath.Join(root, "store"),
		GroupsDir:       filepath.Join(root, "groups"),
		IpcDir:          filepath.Join(root, "ipc"),
		HostGroupsDir:   filepath.Join(hostRoot, "groups"),
		WebDir:          filepath.Join(root, "web"),
		HostCodexDir:      envOr("HOST_CODEX_DIR", ""),
		MountAllowedRoots: parseCSV(envOr("MOUNT_ALLOWED_ROOTS", "")),

		APIPort:              envInt("API_PORT", DefaultAPIPort),
		OnboardingEnabled:    envOr("ONBOARDING_ENABLED", "false") == "true",
		OnboardingPlatforms:  parseCSV(envOr("ONBOARDING_PLATFORMS", "")),
		SendDisabledChannels: parseCSV(envOr("SEND_DISABLED_CHANNELS", "")),
		SendDisabledGroups:   parseCSV(envOr("SEND_DISABLED_GROUPS", "")),

		ObserveWindowMessages: envInt("OBSERVE_WINDOW_MESSAGES", 10),
		ObserveWindowChars:    envInt("OBSERVE_WINDOW_CHARS", 4000),

		MediaEnabled:  envOr("MEDIA_ENABLED", "false") == "true",
		MediaMaxBytes: int64(envInt("MEDIA_MAX_FILE_BYTES", 20*1024*1024)),
		WhisperURL:    envOr("WHISPER_BASE_URL", "http://localhost:8080"),
		VoiceEnabled:  envOr("VOICE_TRANSCRIPTION_ENABLED", "false") == "true",
		VideoEnabled:  envOr("VIDEO_TRANSCRIPTION_ENABLED", "false") == "true",
		WhisperModel:  envOr("WHISPER_MODEL", "turbo"),

		TTSEnabled: envOr("TTS_ENABLED", "false") == "true",
		TTSURL:     envOr("TTS_BASE_URL", "http://ttsd:8880"),
		TTSVoice:   envOr("TTS_VOICE", "af_bella"),
		TTSModel:   envOr("TTS_MODEL", "kokoro"),
		TTSTimeout: envDur("TTS_TIMEOUT", 15*time.Second),

		EgressNetworkPrefix: envOr("EGRESS_NETWORK_PREFIX", ""),
		EgressCrackbox:      envOr("EGRESS_CRACKBOX", ""),
		EgressAPI:           envOr("CRACKBOX_ADMIN_API", ""),
		EgressProxyURL:      envOr("CRACKBOX_PROXY_URL", "http://crackbox:3128"),
		EgressParentSubnet:  envOr("EGRESS_SUBNET", "10.99.0.0/16"),
		EgressAdminSecret:   envOr("CRACKBOX_ADMIN_SECRET", ""),

		CostCapsEnabled: envOr("COST_CAPS_ENABLED", "true") == "true",

		EngagementTTL: envDur("ENGAGEMENT_TTL", 20*time.Minute),

		MaxTurnRetry: envInt("MAX_TURN_RETRY", 3),

		SecretsKey: envOr("SECRETS_KEY", ""),
	}

	// Validation of EgressNetworkPrefix / EgressCrackbox lives in gated
	// (the only daemon that uses egress isolation), not here — other
	// daemons call LoadConfig too and must not fail when egress is on.

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
// envDur parses integer milliseconds (the legacy CONTAINER_TIMEOUT/IDLE_TIMEOUT
// encoding), matching the old chanlib.EnvDur contract.
func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func envInt(k string, fallback int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
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

func resolveTimezone() string {
	tz := os.Getenv("TZ")
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		return "UTC"
	}
	return tz
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

func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
