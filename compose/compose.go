package compose

import (
	"fmt"
	"os"
	"path/filepath"
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

// All daemons share /srv/data/<instance>/.env via env_file. Secrets and
// shared config flow implicitly; per-service environment: blocks only
// hold compose-side overrides (container paths, port transforms, feature
// flags that diverge from .env).
const envFileLine = "    env_file:\n      - .env\n"

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
		services = append(services, svc{strings.TrimSuffix(e.Name(), ".toml"), cfg})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].name < services[j].name })

	project := filepath.Base(dataDir)
	app, flavor, _ := strings.Cut(project, "_")

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
}

func writeEnv(b *strings.Builder, env map[string]string) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "      %s: '%s'\n", k, strings.ReplaceAll(env[k], "'", "''"))
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
	b.WriteString(envFileLine)
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
	b.WriteString("    restart: on-failure\n")
	return b.String()
}

func gatedService(app, flavor, dataDir string, env map[string]string) string {
	apiPort := envOr(env, "API_PORT", "8080")
	hostApp := envOr(env, "HOST_APP_DIR", "")

	// API_PORT override pins gated's internal listen to 8080 (unified).
	// Host-publish side uses the .env value as external port.
	environment := map[string]string{"API_PORT": "8080"}

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
	b.WriteString(envFileLine)
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
		// DB_PATH is a container-side path; .env doesn't know where inside
		// the container the data volume lands.
		environment: map[string]string{"DB_PATH": "/srv/app/home/store/messages.db"},
	}
	// If DASH_PORT is set, expose dashd directly on the host for debugging.
	// Normal access is via proxyd → /dash/.
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
	b.WriteString(envFileLine)
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
		quoted[i] = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
