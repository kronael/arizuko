package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/onvos/arizuko/chanlib"
)

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
	StoreDir        string
	GroupsDir       string
	IpcDir          string
	HostGroupsDir   string
	WebDir          string

	APIPort              int
	ChannelSecret        string
	OnboardingEnabled    bool
	OnboardingPlatforms  []string
	ImpulseEnabled       bool
	SendDisabledChannels []string
	SendDisabledGroups   []string

	MediaEnabled  bool
	MediaMaxBytes int64
	WhisperURL    string
	VoiceEnabled  bool
	VideoEnabled  bool
	WhisperModel  string

	// Egress isolation (crackbox). Disabled when EgressNetwork is empty.
	EgressNetwork string // Docker network name to attach agent containers to
	EgressAPI     string // crackbox admin HTTP API base URL (e.g. http://crackbox:3129)
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
	name := envOr("ASSISTANT_NAME", "Andy")

	c := &Config{
		Name:                name,
		TelegramToken:       envOr("TELEGRAM_BOT_TOKEN", ""),
		Image:               envOr("CONTAINER_IMAGE", "arizuko-ant:latest"),
		Timeout:             envDur("CONTAINER_TIMEOUT", 60*time.Minute),
		IdleTimeout:         envDur("IDLE_TIMEOUT", 30*time.Minute),
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
		StoreDir:        filepath.Join(root, "store"),
		GroupsDir:       filepath.Join(root, "groups"),
		IpcDir:          filepath.Join(root, "ipc"),
		HostGroupsDir:   filepath.Join(hostRoot, "groups"),
		WebDir:          filepath.Join(root, "web"),

		APIPort:              envInt("API_PORT", 8080),
		ChannelSecret:        envOr("CHANNEL_SECRET", ""),
		OnboardingEnabled:    envOr("ONBOARDING_ENABLED", "false") == "true",
		OnboardingPlatforms:  parseCSV(envOr("ONBOARDING_PLATFORMS", "")),
		ImpulseEnabled:       envOr("IMPULSE_ENABLED", "true") == "true",
		SendDisabledChannels: parseCSV(envOr("SEND_DISABLED_CHANNELS", "")),
		SendDisabledGroups:   parseCSV(envOr("SEND_DISABLED_GROUPS", "")),

		MediaEnabled:  envOr("MEDIA_ENABLED", "false") == "true",
		MediaMaxBytes: int64(envInt("MEDIA_MAX_FILE_BYTES", 20*1024*1024)),
		WhisperURL:    envOr("WHISPER_BASE_URL", "http://localhost:8080"),
		VoiceEnabled:  envOr("VOICE_TRANSCRIPTION_ENABLED", "false") == "true",
		VideoEnabled:  envOr("VIDEO_TRANSCRIPTION_ENABLED", "false") == "true",
		WhisperModel:  envOr("WHISPER_MODEL", "turbo"),

		EgressNetwork: envOr("EGRESS_NETWORK", ""),
		EgressAPI:     envOr("CRACKBOX_ADMIN_API", envOr("EGRED_API", "")),
	}

	dev := os.Getenv("ARIZUKO_DEV") == "true" || os.Getenv("ARIZUKO_DEV") == "1"
	if !dev && c.ChannelSecret == "" {
		return nil, fmt.Errorf("CHANNEL_SECRET is empty: channel authentication disabled (set ARIZUKO_DEV=true to override)")
	}
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

var (
	envOr   = chanlib.EnvOr
	envInt  = chanlib.EnvInt
	envBool = chanlib.EnvBool
	envDur  = chanlib.EnvDur
)

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
