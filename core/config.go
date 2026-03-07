package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Name          string
	TelegramToken string
	DiscordToken  string
	Image         string
	Timeout       time.Duration
	IdleTimeout   time.Duration
	MaxContainers int
	PollInterval  time.Duration
	Timezone      string
	WebPort       int
	VitePort      int
	REDACTEDUsers    string
	AuthSecret    string
	WebHost       string

	ProjectRoot     string
	HostProjectRoot string
	HostAppDir      string
	StoreDir        string
	GroupsDir       string
	DataDir         string
	HostGroupsDir   string
	WebDir          string

	EmailIMAP     string
	EmailSMTP     string
	EmailAccount  string
	EmailPassword string

	MediaEnabled  bool
	MediaMaxBytes int64
	WhisperURL    string
	VoiceEnabled  bool
	VideoEnabled  bool
	WhisperModel  string

	TriggerRE *regexp.Regexp
}

func LoadConfig() (*Config, error) {
	_ = godotenv.Load(".env")

	root := envOr("DATA_DIR", mustCwd())
	hostRoot := envOr("HOST_DATA_DIR", root)
	appDir := envOr("HOST_APP_DIR", execDir())
	name := envOr("ASSISTANT_NAME", "Andy")

	webPort := envInt("WEB_PORT", 0)
	vitePort := envInt("VITE_PORT_INTERNAL", 0)
	if vitePort == 0 && webPort > 0 {
		vitePort = webPort + 1
	}

	c := &Config{
		Name:          name,
		TelegramToken: envOr("TELEGRAM_BOT_TOKEN", ""),
		DiscordToken:  envOr("DISCORD_BOT_TOKEN", ""),
		Image:         envOr("CONTAINER_IMAGE", "arizuko-agent:latest"),
		Timeout:       envDur("CONTAINER_TIMEOUT", 30*time.Minute),
		IdleTimeout:   envDur("IDLE_TIMEOUT", 30*time.Minute),
		MaxContainers: envInt("MAX_CONCURRENT_CONTAINERS", 5),
		PollInterval:  2 * time.Second,
		Timezone:      resolveTimezone(),
		WebPort:       webPort,
		VitePort:      vitePort,
		REDACTEDUsers:    envOr("REDACTED_USERS", ""),
		AuthSecret:    envOr("AUTH_SECRET", ""),
		WebHost:       envOr("WEB_HOST", ""),

		ProjectRoot:     root,
		HostProjectRoot: hostRoot,
		HostAppDir:      appDir,
		StoreDir:        filepath.Join(root, "store"),
		GroupsDir:       filepath.Join(root, "groups"),
		DataDir:         filepath.Join(root, "data"),
		HostGroupsDir:   filepath.Join(hostRoot, "groups"),
		WebDir:          filepath.Join(root, "web"),

		EmailIMAP:     envOr("EMAIL_IMAP_HOST", ""),
		EmailSMTP:     envOr("EMAIL_SMTP_HOST", ""),
		EmailAccount:  envOr("EMAIL_ACCOUNT", ""),
		EmailPassword: envOr("EMAIL_PASSWORD", ""),

		MediaEnabled:  envOr("MEDIA_ENABLED", "false") == "true",
		MediaMaxBytes: int64(envInt("MEDIA_MAX_FILE_BYTES", 20*1024*1024)),
		WhisperURL:    envOr("WHISPER_BASE_URL", "http://localhost:8080"),
		VoiceEnabled:  envOr("VOICE_TRANSCRIPTION_ENABLED", "false") == "true",
		VideoEnabled:  envOr("VIDEO_TRANSCRIPTION_ENABLED", "false") == "true",
		WhisperModel:  envOr("WHISPER_MODEL", "turbo"),
	}

	if c.EmailSMTP == "" && c.EmailIMAP != "" {
		c.EmailSMTP = strings.Replace(c.EmailIMAP, "imap.", "smtp.", 1)
	}

	c.TriggerRE = regexp.MustCompile(
		fmt.Sprintf(`(?i)^@%s\b`, regexp.QuoteMeta(c.Name)))

	return c, nil
}

func (c *Config) IsRoot(folder string) bool {
	return !strings.Contains(folder, "/")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func envDur(key string, fallback time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	ms, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
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
