package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	for k, v := range map[string]string{
		"API_PORT":       "8080",
		"ASSISTANT_NAME": "arizuko",
	} {
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
	app, flavor, _ := strings.Cut(project, "_")

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", project)
	b.WriteString("services:\n")
	b.WriteString(gatedService(app, flavor, dataDir, env))
	b.WriteString(timedService(app, flavor, dataDir, env))
	b.WriteString(dashdService(app, flavor, dataDir, env))
	if webPort := envOr(env, "WEB_PORT", ""); webPort != "" {
		b.WriteString(proxydService(app, flavor, dataDir, env))
		b.WriteString(vitedService(app, flavor, dataDir, env))
		if envOr(env, "WEBDAV_ENABLED", "") == "true" {
			b.WriteString(davdService(app, flavor, dataDir, env))
		}
	}
	if envOr(env, "ONBOARDING_ENABLED", "") == "true" {
		b.WriteString(onbodService(app, flavor, dataDir, env))
	}
	for _, s := range services {
		b.WriteString(renderService(app, flavor, s.name, s.cfg, env))
	}
	return b.String(), nil
}

type namedService struct {
	name string
	cfg  ServiceConfig
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
}

func writeEnv(b *strings.Builder, env map[string]string) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "      %s: '%s'\n", k, env[k])
	}
}

func writeSvc(def svcDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", def.name)
	fmt.Fprintf(&b, "    container_name: %s_%s_%s\n", def.app, def.name, def.flavor)
	b.WriteString("    image: arizuko:latest\n")
	fmt.Fprintf(&b, "    entrypoint: ['%s']\n", def.entrypoint)
	fmt.Fprintf(&b, "    volumes:\n      - %s:/srv/app/home\n", def.dataDir)
	if len(def.ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range def.ports {
			fmt.Fprintf(&b, "      - '%s'\n", p)
		}
	}
	b.WriteString("    environment:\n")
	writeEnv(&b, def.environment)
	dep := def.dependsOn
	if dep == "" {
		dep = "gated"
	}
	fmt.Fprintf(&b, "    depends_on: [%s]\n", dep)
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func gatedService(app, flavor, dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", "8080")
	hostData := envOr(env, "HOST_DATA_DIR", dataDir)
	hostApp := envOr(env, "HOST_APP_DIR", "")

	environment := map[string]string{"API_PORT": apiPort}
	if secret := envOr(env, "CHANNEL_SECRET", ""); secret != "" {
		environment["CHANNEL_SECRET"] = secret
	}
	if hostData != "" {
		environment["HOST_DATA_DIR"] = hostData
	}
	if hostApp != "" {
		environment["HOST_APP_DIR"] = hostApp
	}
	for _, k := range routerEnvKeys {
		if v := envOr(env, k, ""); v != "" {
			environment[k] = v
		}
	}

	ports := []string{apiPort + ":" + apiPort}

	var b strings.Builder
	fmt.Fprintf(&b, "  gated:\n")
	fmt.Fprintf(&b, "    container_name: %s_gated_%s\n", app, flavor)
	b.WriteString("    image: arizuko:latest\n")
	b.WriteString("    entrypoint: ['gated']\n")
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s:/srv/app/home\n", dataDir)
	b.WriteString("      - /var/run/docker.sock:/var/run/docker.sock\n")
	if hostApp != "" {
		fmt.Fprintf(&b, "      - %s:/srv/app/arizuko:ro\n", hostApp)
	}
	b.WriteString("    ports:\n")
	for _, p := range ports {
		fmt.Fprintf(&b, "      - '%s'\n", p)
	}
	b.WriteString("    environment:\n")
	writeEnv(&b, environment)
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
		environment: map[string]string{
			"DATA_DIR": "/srv/app/home",
			"TIMEZONE": envOr(env, "TZ", "UTC"),
		},
	})
}

func onbodService(app, flavor, dataDir string, env map[string]string) string {
	environment := map[string]string{
		"DATA_DIR":           "/srv/app/home",
		"ONBOARDING_ENABLED": "true",
		"ONBOD_LISTEN_ADDR":  envOr(env, "ONBOD_LISTEN_ADDR", ":8092"),
	}
	if p := envOr(env, "ONBOARDING_PROTOTYPE", ""); p != "" {
		environment["ONBOARDING_PROTOTYPE"] = p
	}
	if s := envOr(env, "CHANNEL_SECRET", ""); s != "" {
		environment["CHANNEL_SECRET"] = s
	}
	return writeSvc(svcDef{
		name:        "onbod",
		app:         app,
		flavor:      flavor,
		entrypoint:  "onbod",
		dataDir:     dataDir,
		environment: environment,
	})
}

func dashdService(app, flavor, dataDir string, env map[string]string) string {
	dashPort := envOr(env, "DASH_PORT", "8090")
	environment := map[string]string{
		"DATA_DIR":  "/srv/app/home",
		"DB_PATH":   "/srv/app/home/store/messages.db",
		"DASH_PORT": dashPort,
	}
	if s := envOr(env, "AUTH_SECRET", ""); s != "" {
		environment["AUTH_SECRET"] = s
	}
	if h := envOr(env, "WEB_HOST", ""); h != "" {
		environment["WEB_HOST"] = h
	}
	return writeSvc(svcDef{
		name:        "dashd",
		app:         app,
		flavor:      flavor,
		entrypoint:  "dashd",
		dataDir:     dataDir,
		ports:       []string{dashPort + ":" + dashPort},
		environment: environment,
	})
}

func proxydService(app, flavor, dataDir string, env map[string]string) string {
	webPort := envOr(env, "WEB_PORT", "8095")
	dashPort := envOr(env, "DASH_PORT", "8090")
	vitePort := vitePortFrom(webPort)
	dashAddr := envOr(env, "DASH_ADDR", "http://dashd:"+dashPort)
	environment := map[string]string{
		"WEB_PORT":  webPort,
		"DASH_ADDR": dashAddr,
		"VITE_ADDR": "http://vited:" + vitePort,
	}
	if s := envOr(env, "AUTH_SECRET", ""); s != "" {
		environment["AUTH_SECRET"] = s
	}
	if p := envOr(env, "WEB_PUBLIC", ""); p != "" {
		environment["WEB_PUBLIC"] = p
	}
	if r := envOr(env, "WEB_REDIRECTS", ""); r != "" {
		environment["WEB_REDIRECTS"] = r
	}
	if envOr(env, "WEBDAV_ENABLED", "") == "true" {
		davPort := envOr(env, "DAV_PORT", "8097")
		environment["DAV_ADDR"] = "http://davd:" + davPort
	}
	return writeSvc(svcDef{
		name:        "proxyd",
		app:         app,
		flavor:      flavor,
		entrypoint:  "proxyd",
		dataDir:     dataDir,
		ports:       []string{webPort + ":" + webPort},
		environment: environment,
		dependsOn:   "gated, dashd",
	})
}

func davdService(app, flavor, dataDir string, env map[string]string) string {
	davPort := envOr(env, "DAV_PORT", "8097")
	var b strings.Builder
	fmt.Fprintf(&b, "  davd:\n")
	fmt.Fprintf(&b, "    container_name: %s_davd_%s\n", app, flavor)
	b.WriteString("    image: sigoden/dufs:latest\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s/groups:/data:ro\n", dataDir)
	b.WriteString("    ports:\n")
	fmt.Fprintf(&b, "      - '%s:%s'\n", davPort, davPort)
	b.WriteString("    command:\n")
	fmt.Fprintf(&b, "      - dufs\n      - --port\n      - '%s'\n      - /data\n", davPort)
	b.WriteString("    depends_on: [gated]\n")
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func vitedService(app, flavor, dataDir string, env map[string]string) string {
	webPort := envOr(env, "WEB_PORT", "8095")
	vitePort := vitePortFrom(webPort)

	var b strings.Builder
	fmt.Fprintf(&b, "  vited:\n")
	fmt.Fprintf(&b, "    container_name: %s_vited_%s\n", app, flavor)
	b.WriteString("    image: arizuko-vite:latest\n")
	fmt.Fprintf(&b, "    volumes:\n      - %s/web:/web\n", dataDir)
	b.WriteString("    environment:\n")
	fmt.Fprintf(&b, "      VITE_PORT: '%s'\n", vitePort)
	if webRoot := envOr(env, "WEB_ROOT", ""); webRoot != "" {
		fmt.Fprintf(&b, "      WEB_ROOT: '%s'\n", webRoot)
	}
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func vitePortFrom(webPort string) string {
	n, err := strconv.Atoi(webPort)
	if err != nil {
		return "8096"
	}
	return strconv.Itoa(n + 1)
}

var routerEnvKeys = []string{
	"ASSISTANT_NAME",
	"CONTAINER_IMAGE",
	"CONTAINER_TIMEOUT",
	"IDLE_TIMEOUT",
	"MAX_CONCURRENT_CONTAINERS",
	"AUTH_SECRET",
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
		quoted[i] = "'" + s + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
