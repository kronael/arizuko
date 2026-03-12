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
	Restart     string            `toml:"restart"`
	DependsOn   []string          `toml:"depends_on"`
	Environment map[string]string `toml:"environment"`
	Ports       []string          `toml:"ports"`
	Volumes     []string          `toml:"volumes"`
	Command     []string          `toml:"command"`
}

func Generate(dataDir string) (string, error) {
	env := loadEnv(filepath.Join(dataDir, ".env"))
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
	var b strings.Builder
	fmt.Fprintf(&b, "  router:\n")
	fmt.Fprintf(&b, "    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    command: ['run']\n")
	fmt.Fprintf(&b, "    volumes:\n")
	fmt.Fprintf(&b, "      - %s:/srv/data\n", dataDir)
	fmt.Fprintf(&b, "      - /var/run/docker.sock:/var/run/docker.sock\n")
	fmt.Fprintf(&b, "    ports:\n")
	fmt.Fprintf(&b, "      - '%s:%s'\n", apiPort, apiPort)
	fmt.Fprintf(&b, "    environment:\n")
	fmt.Fprintf(&b, "      DATA_DIR: /srv/data\n")
	fmt.Fprintf(&b, "      API_PORT: '%s'\n", apiPort)
	secret := envOr(env, "CHANNEL_SECRET", "")
	if secret != "" {
		fmt.Fprintf(&b, "      CHANNEL_SECRET: '%s'\n", secret)
	}
	for _, k := range routerEnvKeys {
		if v := envOr(env, k, ""); v != "" {
			fmt.Fprintf(&b, "      %s: '%s'\n", k, v)
		}
	}
	fmt.Fprintf(&b, "    restart: on-failure\n")
	return b.String()
}

var routerEnvKeys = []string{
	"ASSISTANT_NAME",
	"CONTAINER_IMAGE",
	"CONTAINER_TIMEOUT",
	"IDLE_TIMEOUT",
	"MAX_CONCURRENT_CONTAINERS",
	"HOST_DATA_DIR",
	"HOST_APP_DIR",
	"TELEGRAM_BOT_TOKEN",
	"AUTH_SECRET",
	"WEB_PORT",
}

func renderService(name string, cfg ServiceConfig, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    image: %s\n", cfg.Image)
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
		keys := sortedKeys(cfg.Environment)
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

func loadEnv(path string) map[string]string {
	m, err := godotenv.Read(path)
	if err != nil {
		return map[string]string{}
	}
	return m
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

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func yamlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "'" + s + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
