package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

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
	defaults := map[string]string{
		"API_PORT":       "8080",
		"ASSISTANT_NAME": "arizuko",
	}
	for k, v := range defaults {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	servicesDir := filepath.Join(dataDir, "services")

	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return "", fmt.Errorf("read services/: %w", err)
	}

	var services []namedService
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		var cfg ServiceConfig
		if _, err := toml.DecodeFile(filepath.Join(servicesDir, e.Name()), &cfg); err != nil {
			return "", fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		services = append(services, namedService{name, cfg})
	}

	sort.Slice(services, func(i, j int) bool {
		return services[i].name < services[j].name
	})

	project := filepath.Base(dataDir)
	// project = "REDACTED" → app = "arizuko", flavor = "REDACTED"
	app, flavor, _ := strings.Cut(project, "_")

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", project)
	b.WriteString("services:\n")
	b.WriteString(gatedService(app, flavor, dataDir, env))
	b.WriteString(timedService(app, flavor, dataDir, env))
	b.WriteString(dashdService(app, flavor, dataDir, env))
	for _, s := range services {
		b.WriteString(renderService(app, flavor, s.name, s.cfg, env))
	}
	return b.String(), nil
}

type namedService struct {
	name string
	cfg  ServiceConfig
}

func gatedService(app, flavor, dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", "8080")
	hostData := envOr(env, "HOST_DATA_DIR", dataDir)
	hostApp := envOr(env, "HOST_APP_DIR", "")
	var b strings.Builder
	fmt.Fprintf(&b, "  gated:\n")
	fmt.Fprintf(&b, "    container_name: %s_gated_%s\n", app, flavor)
	fmt.Fprintf(&b, "    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['gated']\n")
	fmt.Fprintf(&b, "    volumes:\n")
	fmt.Fprintf(&b, "      - %s:/srv/app/home\n", dataDir)
	fmt.Fprintf(&b, "      - /var/run/docker.sock:/var/run/docker.sock\n")
	if hostApp != "" {
		fmt.Fprintf(&b, "      - %s:/srv/app/arizuko:ro\n", hostApp)
	}
	fmt.Fprintf(&b, "    ports:\n")
	fmt.Fprintf(&b, "      - '%s:%s'\n", apiPort, apiPort)
	webPort := envOr(env, "WEB_PORT", "")
	if webPort != "" {
		fmt.Fprintf(&b, "      - '%s:%s'\n", webPort, webPort)
	}
	fmt.Fprintf(&b, "    environment:\n")
	fmt.Fprintf(&b, "      API_PORT: '%s'\n", apiPort)
	secret := envOr(env, "CHANNEL_SECRET", "")
	if secret != "" {
		fmt.Fprintf(&b, "      CHANNEL_SECRET: '%s'\n", secret)
	}
	if hostData != "" {
		fmt.Fprintf(&b, "      HOST_DATA_DIR: '%s'\n", hostData)
	}
	if hostApp != "" {
		fmt.Fprintf(&b, "      HOST_APP_DIR: '%s'\n", hostApp)
	}
	for _, k := range routerEnvKeys {
		if v := envOr(env, k, ""); v != "" {
			fmt.Fprintf(&b, "      %s: '%s'\n", k, v)
		}
	}
	fmt.Fprintf(&b, "    restart: on-failure\n")
	return b.String()
}

func timedService(app, flavor, dataDir string, env map[string]string) string {
	tz := envOr(env, "TZ", "UTC")
	var b strings.Builder
	fmt.Fprintf(&b, "  timed:\n")
	fmt.Fprintf(&b, "    container_name: %s_timed_%s\n", app, flavor)
	fmt.Fprintf(&b, "    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['timed']\n")
	fmt.Fprintf(&b, "    volumes:\n")
	fmt.Fprintf(&b, "      - %s:/srv/app/home\n", dataDir)
	fmt.Fprintf(&b, "    environment:\n")
	fmt.Fprintf(&b, "      DATA_DIR: /srv/app/home\n")
	fmt.Fprintf(&b, "      TIMEZONE: '%s'\n", tz)
	fmt.Fprintf(&b, "    depends_on: [gated]\n")
	fmt.Fprintf(&b, "    restart: on-failure\n")
	return b.String()
}

func dashdService(app, flavor, dataDir string, env map[string]string) string {
	dashPort := envOr(env, "DASH_PORT", "8090")
	authSecret := envOr(env, "AUTH_SECRET", "")
	webHost := envOr(env, "WEB_HOST", "")
	var b strings.Builder
	fmt.Fprintf(&b, "  dashd:\n")
	fmt.Fprintf(&b, "    container_name: %s_dashd_%s\n", app, flavor)
	fmt.Fprintf(&b, "    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['dashd']\n")
	fmt.Fprintf(&b, "    volumes:\n")
	fmt.Fprintf(&b, "      - %s:/srv/app/home\n", dataDir)
	fmt.Fprintf(&b, "    ports:\n")
	fmt.Fprintf(&b, "      - '%s:%s'\n", dashPort, dashPort)
	fmt.Fprintf(&b, "    environment:\n")
	fmt.Fprintf(&b, "      DATA_DIR: /srv/app/home\n")
	fmt.Fprintf(&b, "      DB_PATH: /srv/app/home/store/messages.db\n")
	fmt.Fprintf(&b, "      DASH_PORT: '%s'\n", dashPort)
	if authSecret != "" {
		fmt.Fprintf(&b, "      AUTH_SECRET: '%s'\n", authSecret)
	}
	if webHost != "" {
		fmt.Fprintf(&b, "      WEB_HOST: '%s'\n", webHost)
	}
	fmt.Fprintf(&b, "    depends_on: [gated]\n")
	fmt.Fprintf(&b, "    restart: on-failure\n")
	return b.String()
}

var routerEnvKeys = []string{
	"ASSISTANT_NAME",
	"CONTAINER_IMAGE",
	"CONTAINER_TIMEOUT",
	"IDLE_TIMEOUT",
	"MAX_CONCURRENT_CONTAINERS",
	"AUTH_SECRET",
	"WEB_PORT",
	"WEB_HOST",
	"MEDIA_ENABLED",
	"VOICE_TRANSCRIPTION_ENABLED",
	"WHISPER_BASE_URL",
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
	if len(cfg.Environment) > 0 {
		b.WriteString("    environment:\n")
		keys := make([]string, 0, len(cfg.Environment))
		for k := range cfg.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := interpolate(cfg.Environment[k], env)
			fmt.Fprintf(&b, "      %s: '%s'\n", k, v)
		}
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
		quoted[i] = "'" + s + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
