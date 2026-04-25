package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

// dockerGID returns the gid that owns /var/run/docker.sock, or 999 fallback.
// gated must be in this group to spawn agent containers as uid 1000.
func dockerGID() int {
	var st syscall.Stat_t
	if err := syscall.Stat("/var/run/docker.sock", &st); err == nil {
		return int(st.Gid)
	}
	return 999
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
	// DATA_DIR is NOT in commonKeys: that would leak the host path
	// into env/<daemon>.env and into the container. DATA_DIR is the
	// container-internal mount point, set per-daemon in the compose
	// `environment:` block.
}

// daemonKeys: per-daemon secrets + config. Unlisted keys never reach the daemon.
var daemonKeys = map[string][]string{
	"gated": {
		"CHANNEL_SECRET", "AUTH_SECRET", "AUTH_BASE_URL",
		"CONTAINER_IMAGE", "CONTAINER_TIMEOUT",
		"IDLE_TIMEOUT", "MAX_CONCURRENT_CONTAINERS",
		"MEDIA_ENABLED", "MEDIA_MAX_FILE_BYTES", "WHISPER_BASE_URL",
		"VOICE_TRANSCRIPTION_ENABLED", "VIDEO_TRANSCRIPTION_ENABLED", "WHISPER_MODEL",
		"IMPULSE_ENABLED", "SEND_DISABLED_CHANNELS", "SEND_DISABLED_GROUPS",
	},
	"timed": {"CHANNEL_SECRET"},
	"onbod": {
		"CHANNEL_SECRET", "AUTH_SECRET", "AUTH_BASE_URL",
		"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_ALLOWED_ORG",
		"DISCORD_CLIENT_ID", "DISCORD_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ALLOWED_EMAILS",
		"ONBOARDING_ENABLED", "ONBOARDING_PLATFORMS",
		"ONBOARDING_PROTOTYPE", "ONBOARDING_GREETING",
		"ONBOARD_POLL_INTERVAL", "ONBOD_LISTEN_ADDR",
	},
	"dashd":  {"AUTH_SECRET", "DASH_PORT"},
	"webd":   {"CHANNEL_SECRET", "AUTH_SECRET", "AUTH_BASE_URL", "ROUTER_URL"},
	"proxyd": {"AUTH_SECRET", "AUTH_BASE_URL"},
	"teled":  {"CHANNEL_SECRET", "TELEGRAM_BOT_TOKEN"},
	"discd":  {"CHANNEL_SECRET", "DISCORD_BOT_TOKEN"},
	"mastd":  {"CHANNEL_SECRET", "MASTODON_ACCESS_TOKEN", "MASTODON_INSTANCE"},
	"bskyd":  {"CHANNEL_SECRET", "BLUESKY_HANDLE", "BLUESKY_APP_PASSWORD"},
	"reditd": {"CHANNEL_SECRET", "REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "REDDIT_USERNAME", "REDDIT_PASSWORD"},
	"linkd":  {"CHANNEL_SECRET", "LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET", "LINKEDIN_ACCESS_TOKEN", "LINKEDIN_REFRESH_TOKEN"},
	"emaid":  {"CHANNEL_SECRET", "IMAP_HOST", "IMAP_USER", "IMAP_PASSWORD", "SMTP_HOST", "SMTP_USER", "SMTP_PASSWORD"},
	"whapd":  {"CHANNEL_SECRET"},
	"twitd":  {"CHANNEL_SECRET", "TWITTER_USERNAME", "TWITTER_PASSWORD", "TWITTER_EMAIL", "TWITTER_2FA_SECRET", "TWITTER_POLL_INTERVAL"},
}

// envFileFor returns the scoped env_file block for a daemon, falling back to
// the shared .env for services not in daemonKeys.
func envFileFor(name string) string {
	if _, ok := daemonKeys[name]; ok {
		return fmt.Sprintf("    env_file:\n      - env/%s.env\n", name)
	}
	return "    env_file:\n      - .env\n"
}

// writeEnvFiles emits env/<daemon>.env with only the keys each daemon needs.
// Called from Generate before rendering compose; failure is non-fatal.
func writeEnvFiles(dataDir string, env map[string]string) error {
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
	Image       string            `toml:"image"`
	Entrypoint  []string          `toml:"entrypoint"`
	Restart     string            `toml:"restart"`
	DependsOn   []string          `toml:"depends_on"`
	Environment map[string]string `toml:"environment"`
	Ports       []string          `toml:"ports"`
	Volumes     []string          `toml:"volumes"`
	Command     []string          `toml:"command"`
}

func Generate(dataDir string) (string, error) {
	env, _ := godotenv.Read(filepath.Join(dataDir, ".env"))
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range map[string]string{
		"API_PORT":       "8080",
		"ASSISTANT_NAME": "arizuko",
		"DATA_DIR":       dataDir, // host path; used in extra service volume strings
	} {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	// Per-daemon env files: non-fatal if it fails; log for triage.
	if werr := writeEnvFiles(dataDir, env); werr != nil {
		fmt.Fprintf(os.Stderr, "compose: writeEnvFiles: %v\n", werr)
	}
	servicesDir := filepath.Join(dataDir, "services")

	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return "", fmt.Errorf("read services/: %w", err)
	}

	type svc struct {
		name string
		cfg  ServiceConfig
	}
	var services []svc
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
		services = append(services, svc{name, cfg})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].name < services[j].name })

	project := filepath.Base(dataDir)
	app, flavor, _ := strings.Cut(project, "_")
	if !identRE.MatchString(app) {
		return "", fmt.Errorf("invalid compose project app %q (derived from data dir basename)", app)
	}
	if flavor != "" && !identRE.MatchString(flavor) {
		return "", fmt.Errorf("invalid instance flavor %q (derived from data dir basename)", flavor)
	}

	profile := envOr(env, "PROFILE", "full")

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", project)
	b.WriteString("services:\n")
	b.WriteString(gatedService(app, flavor, dataDir, env))
	webPort := envOr(env, "WEB_PORT", "")
	if webPort != "" && profile != "minimal" {
		b.WriteString(webdService(app, flavor, dataDir, env))
		b.WriteString(proxydService(app, flavor, dataDir, env))
		b.WriteString(vitedService(app, flavor, dataDir, env))
	}
	if profile != "minimal" && profile != "web" {
		b.WriteString(timedService(app, flavor, dataDir, env))
		if profile == "full" {
			b.WriteString(dashdService(app, flavor, dataDir, env))
			if webPort != "" && envOr(env, "WEBDAV_ENABLED", "") == "true" {
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
	return b.String(), nil
}

type svcDef struct {
	name        string
	app         string
	flavor      string
	entrypoint  string
	dataDir     string
	ports       []string
	environment map[string]string
	dependsOn   string
	// noHealth skips the standard /health healthcheck. Set for daemons
	// without an HTTP server (timed) or without /health (vited).
	noHealth bool
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
	fmt.Fprintf(&b, "    volumes:\n      - %s:/srv/app/home\n", def.dataDir)
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
	b.WriteString("      DATA_DIR: '/srv/app/home'\n")
	if len(def.environment) > 0 {
		writeEnv(&b, def.environment)
	}
	dep := def.dependsOn
	if dep == "" {
		dep = "gated"
	}
	fmt.Fprintf(&b, "    depends_on: [%s]\n", dep)
	if !def.noHealth {
		b.WriteString(healthBlock)
	}
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func gatedService(app, flavor, dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", "8080")
	hostApp := envOr(env, "HOST_APP_DIR", "")

	// API_PORT override pins gated's internal listen to 8080 (unified).
	// Host-publish side uses the .env value as external port.
	environment := map[string]string{
		"API_PORT": "8080",
		"DATA_DIR": "/srv/app/home",
	}

	var b strings.Builder
	b.WriteString("  gated:\n")
	fmt.Fprintf(&b, "    container_name: %s_gated_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['gated']\n")
	// uid 1000 matches agent container so shared data dir files round-trip;
	// group_add into docker gid grants docker.sock access for spawning agents.
	b.WriteString("    user: '1000:1000'\n")
	fmt.Fprintf(&b, "    group_add: ['%d']\n", dockerGID())
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s:/srv/app/home\n", dataDir)
	b.WriteString("      - /var/run/docker.sock:/var/run/docker.sock\n")
	if hostApp != "" {
		fmt.Fprintf(&b, "      - %s:/srv/app/arizuko:ro\n", hostApp)
	}
	b.WriteString("    ports:\n")
	fmt.Fprintf(&b, "      - '%s:8080'\n", apiPort)
	b.WriteString("    extra_hosts:\n")
	b.WriteString("      - 'host.docker.internal:host-gateway'\n")
	b.WriteString(envFileFor("gated"))
	b.WriteString("    environment:\n")
	writeEnv(&b, environment)
	b.WriteString(healthBlock)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func timedService(app, flavor, dataDir string, env map[string]string) string {
	return writeSvc(svcDef{
		name:       "timed",
		app:        app,
		flavor:     flavor,
		entrypoint: "timed",
		dataDir:    dataDir,
		// TIMEZONE is the only compose-side transform; timed reads this
		// name while the rest of the world uses TZ.
		environment: map[string]string{"TIMEZONE": envOr(env, "TZ", "UTC")},
	})
}

func onbodService(app, flavor, dataDir string, env map[string]string) string {
	return writeSvc(svcDef{
		name:       "onbod",
		app:        app,
		flavor:     flavor,
		entrypoint: "onbod",
		dataDir:    dataDir,
		// Force ONBOARDING_ENABLED=true inside the container regardless of
		// how the flag is expressed in .env (gate for daemon inclusion was
		// already decided by Generate's caller).
		environment: map[string]string{"ONBOARDING_ENABLED": "true"},
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
			"DB_PATH":   "/srv/app/home/store/messages.db",
			"DASH_PORT": "8080",
		},
	}
	if dashPort := envOr(env, "DASH_PORT", ""); dashPort != "" {
		def.ports = []string{dashPort + ":8080"}
	}
	return writeSvc(def)
}

func proxydService(app, flavor, dataDir string, env map[string]string) string {
	webPort := envOr(env, "WEB_PORT", "8095")
	// Optional upstreams — peer URLs for always-on services (dashd, webd,
	// vited) are defaulted in proxyd's code to http://<svc>:8080. Only
	// feature-gated targets (davd, onbod) need explicit addressing so
	// proxyd's "empty = disabled" check keeps them as 404s when off.
	environment := map[string]string{}
	if envOr(env, "WEBDAV_ENABLED", "") == "true" {
		environment["DAV_ADDR"] = "http://davd:8080"
	}
	if envOr(env, "ONBOARDING_ENABLED", "") == "true" {
		environment["ONBOD_ADDR"] = "http://onbod:8080"
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
	return writeSvc(svcDef{
		name:        "proxyd",
		app:         app,
		flavor:      flavor,
		entrypoint:  "proxyd",
		dataDir:     dataDir,
		ports:       ports,
		environment: environment,
		dependsOn:   "gated, dashd, webd",
	})
}

func davdService(app, flavor, dataDir string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("  davd:\n")
	fmt.Fprintf(&b, "    container_name: %s_davd_%s\n", app, flavor)
	b.WriteString("    image: sigoden/dufs:latest\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s/groups:/data:ro\n", dataDir)
	// Host-side dav port exposure for direct access, if DAV_PORT is set.
	if davPort := envOr(env, "DAV_PORT", ""); davPort != "" {
		b.WriteString("    ports:\n")
		fmt.Fprintf(&b, "      - '%s:8080'\n", davPort)
	}
	b.WriteString("    command:\n")
	b.WriteString("      - dufs\n      - --port\n      - '8080'\n      - /data\n")
	b.WriteString("    depends_on: [gated]\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func webdService(app, flavor, dataDir string, env map[string]string) string {
	// webd's defaults for WEBD_LISTEN/WEBD_URL/ROUTER_URL resolve to
	// http://webd:8080 and http://gated:8080 — no compose-side env needed.
	return writeSvc(svcDef{
		name: "webd", app: app, flavor: flavor,
		entrypoint: "webd", dataDir: dataDir,
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
		writeEnv(&b, interped)
	}
	deps := cfg.DependsOn
	if len(deps) == 0 {
		deps = []string{"gated"}
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
