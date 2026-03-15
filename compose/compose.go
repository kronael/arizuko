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

	var b strings.Builder
	b.WriteString("services:\n")
	b.WriteString(routerService(dataDir, env))
	b.WriteString(schedulerService(dataDir, env))
	for _, s := range services {
		b.WriteString(renderService(s.name, s.cfg, env))
	}
	return b.String(), nil
}

type namedService struct {
	name string
	cfg  ServiceConfig
}

func routerService(dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", "8080")
	hostData := envOr(env, "HOST_DATA_DIR", dataDir)
	hostApp := envOr(env, "HOST_APP_DIR", "")
	var b strings.Builder
	fmt.Fprintf(&b, "  router:\n")
	fmt.Fprintf(&b, "    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    command: ['run']\n")
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

func schedulerService(dataDir string, env map[string]string) string {
	tz := envOr(env, "TZ", "UTC")
	var b strings.Builder
	fmt.Fprintf(&b, "  scheduler:\n")
	fmt.Fprintf(&b, "    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['timed']\n")
	fmt.Fprintf(&b, "    volumes:\n")
	fmt.Fprintf(&b, "      - %s/store:/srv/data/store\n", dataDir)
	fmt.Fprintf(&b, "    environment:\n")
	fmt.Fprintf(&b, "      DATABASE: /srv/data/store/messages.db\n")
	fmt.Fprintf(&b, "      TIMEZONE: '%s'\n", tz)
	fmt.Fprintf(&b, "    depends_on: [router]\n")
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

func renderService(name string, cfg ServiceConfig, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
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
		deps = []string{"router"}
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
