package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// One-shot converter for data dirs seeded before native compose packaging
// (spec 5/27). Adapters used to be custom TOML parsed by compose.go; they are
// now `services/<name>.yml` compose fragments included verbatim. Every
// `arizuko generate` rewrites any leftover `.toml` in place and deletes it, so
// an instance keeps running across the binary upgrade. Delete this file once
// no deployed data dir carries a `.toml`.

// legacyService is the retired TOML service shape.
type legacyService struct {
	Image        string            `toml:"image"`
	Entrypoint   []string          `toml:"entrypoint"`
	Restart      string            `toml:"restart"`
	DependsOn    []string          `toml:"depends_on"`
	Environment  map[string]string `toml:"environment"`
	Ports        []string          `toml:"ports"`
	Volumes      []string          `toml:"volumes"`
	Command      []string          `toml:"command"`
	ProxydRoutes []legacyRoute     `toml:"proxyd_route"`
}

// legacyRoute is the retired `[[proxyd_route]]` block; it becomes an entry in
// the sibling `services/<name>-routes.json`.
type legacyRoute struct {
	Path            string   `toml:"path"`
	Backend         string   `toml:"backend"`
	Auth            string   `toml:"auth"`
	GatedBy         string   `toml:"gated_by"`
	PreserveHeaders []string `toml:"preserve_headers"`
	StripPrefix     bool     `toml:"strip_prefix"`
}

// imageRefRE constrains docker image references to alnum, dots, colons,
// slashes, underscores, dashes, and @ (digest). No whitespace, no newlines —
// prevents YAML injection through a legacy TOML `image`.
var imageRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,254}$`)

// convertLegacyTOML rewrites every `services/*.toml` as a compose fragment and
// removes the TOML. Missing services dir is left to readFragments to report.
func convertLegacyTOML(servicesDir string) error {
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		if !identRE.MatchString(name) {
			return fmt.Errorf("invalid service filename %q (allowed chars: [A-Za-z0-9_.-])", e.Name())
		}
		var cfg legacyService
		tomlPath := filepath.Join(servicesDir, e.Name())
		if _, err := toml.DecodeFile(tomlPath, &cfg); err != nil {
			return fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if !imageRefRE.MatchString(cfg.Image) {
			return fmt.Errorf("service %q has invalid image %q (must match image-ref regex)", name, cfg.Image)
		}
		// A native fragment already sitting next to the legacy TOML is a mixed
		// state (partial migration, or a `packages add` after seeding). Refuse to
		// silently overwrite the operator's `.yml`/`.json`; the operator resolves
		// which source wins by deleting the stale one.
		ymlPath := filepath.Join(servicesDir, name+".yml")
		if _, err := os.Stat(ymlPath); err == nil {
			return fmt.Errorf("both %s.toml and %s.yml exist — refusing to overwrite; delete one to resolve", name, name)
		}
		routesPath := filepath.Join(servicesDir, name+"-routes.json")
		if len(cfg.ProxydRoutes) > 0 {
			if _, err := os.Stat(routesPath); err == nil {
				return fmt.Errorf("both %s.toml (with routes) and %s-routes.json exist — refusing to overwrite; delete one to resolve", name, name)
			}
		}
		if err := os.WriteFile(ymlPath, []byte(renderFragment(name, cfg)), 0o644); err != nil {
			return fmt.Errorf("write %s.yml: %w", name, err)
		}
		if len(cfg.ProxydRoutes) > 0 {
			routes := make([]ProxydRoute, len(cfg.ProxydRoutes))
			for i, r := range cfg.ProxydRoutes {
				// legacy TOML routes predate redirect_to; copy the shared fields.
				routes[i] = ProxydRoute{
					Path:            r.Path,
					Backend:         r.Backend,
					Auth:            r.Auth,
					GatedBy:         r.GatedBy,
					PreserveHeaders: r.PreserveHeaders,
					StripPrefix:     r.StripPrefix,
				}
			}
			b, err := json.MarshalIndent(routes, "", "  ")
			if err != nil {
				return fmt.Errorf("encode %s routes: %w", name, err)
			}
			if err := os.WriteFile(routesPath, append(b, '\n'), 0o644); err != nil {
				return fmt.Errorf("write %s-routes.json: %w", name, err)
			}
		}
		// Back up rather than delete: the generated compose uses top-level
		// `include`, which needs Docker Compose 2.20+. On an older host compose
		// rejects the new model AFTER conversion — keeping `<name>.toml.bak` lets
		// the operator restore the pre-v0.62 inputs and downgrade the binary.
		if err := os.Rename(tomlPath, tomlPath+".bak"); err != nil {
			return fmt.Errorf("back up %s: %w", e.Name(), err)
		}
	}
	return nil
}

// renderFragment renders one legacy service as a compose fragment. Identity
// (`${APP}`/`${FLAVOR}`/`${DATA_DIR}`) and every other `${VAR}` stay as
// references — docker interpolates them from the data dir's .env at up time.
// `${CONTAINER_DATA}` was a generate-time-only var, so it is resolved here.
func renderFragment(name string, cfg legacyService) string {
	var b strings.Builder
	b.WriteString("# Converted from services/" + name + ".toml by `arizuko generate`.\n")
	b.WriteString("services:\n")
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    container_name: ${APP}_%s_${FLAVOR}\n", name)
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
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(resolveContainerData(v)))
		}
	}
	if len(cfg.Ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range cfg.Ports {
			fmt.Fprintf(&b, "      - '%s'\n", p)
		}
	}
	// env_file paths resolve against the fragment's own directory (services/).
	fmt.Fprintf(&b, "    env_file: ['../%s']\n", envFileName(name))
	if len(cfg.Environment) > 0 {
		b.WriteString("    environment:\n")
		env := make(map[string]string, len(cfg.Environment))
		for k, v := range cfg.Environment {
			env[k] = resolveContainerData(v)
		}
		// Re-point any declared ROUTER_URL at the canonical router: pre-split
		// data dirs still carry `http://gated:8080`.
		if _, ok := env["ROUTER_URL"]; ok {
			env["ROUTER_URL"] = routdURL
		}
		writeEnv(&b, env)
	}
	deps := cfg.DependsOn
	if len(deps) == 0 {
		deps = []string{"routd"}
	}
	fmt.Fprintf(&b, "    depends_on: [%s]\n", strings.Join(deps, ", "))
	restart := cfg.Restart
	if restart == "" {
		restart = "on-failure"
	}
	fmt.Fprintf(&b, "    restart: %s\n", restart)
	return b.String()
}

// resolveContainerData substitutes the retired generate-time ${CONTAINER_DATA}
// var (docker knows nothing about it) with the container-side mount path.
func resolveContainerData(s string) string {
	return strings.ReplaceAll(s, "${CONTAINER_DATA}", containerDataMount)
}

func yamlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = yamlQuote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
