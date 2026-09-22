package compose

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// sourcedFragment is the slice of a compose fragment through which a service
// reaches the host, the instance tree or another service.
type sourcedFragment struct {
	Include  any                       `yaml:"include"`
	Services map[string]sourcedService `yaml:"services"`
	Volumes  map[string]map[string]any `yaml:"volumes"`
	Configs  map[string]map[string]any `yaml:"configs"`
	Secrets  map[string]map[string]any `yaml:"secrets"`
}

type sourcedService struct {
	Build       any      `yaml:"build"`
	Extends     any      `yaml:"extends"`
	EnvFile     any      `yaml:"env_file"`
	Volumes     []any    `yaml:"volumes"`
	VolumesFrom []string `yaml:"volumes_from"`
	Privileged  bool     `yaml:"privileged"`
	CapAdd      []string `yaml:"cap_add"`
	Devices     []any    `yaml:"devices"`
	Pid         string   `yaml:"pid"`
	Ipc         string   `yaml:"ipc"`
	NetworkMode string   `yaml:"network_mode"`
	UsernsMode  string   `yaml:"userns_mode"`
	Cgroup      string   `yaml:"cgroup"`
	SecurityOpt []string `yaml:"security_opt"`
}

var volumeNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// CheckSourcedFragment refuses a compose fragment that reaches outside the
// package named; the release's own catalog is exempt. A service carries the
// package's name, binds only `${DATA_DIR}/store/<name>` or a named volume,
// reads no env file, and escapes no namespace. `docker compose` refuses none
// of it, so a package could quietly mount an owner's store, `.env` or the
// docker socket (BUGS L201), and an `env_file` fails the whole instance at
// `up` instead of the one install (BUGS L200).
func CheckSourcedFragment(name, file string, body []byte) error {
	var f sourcedFragment
	if err := yaml.Unmarshal(body, &f); err != nil {
		return fmt.Errorf("%s: %w — a fragment install cannot read is not one it can bound", file, err)
	}
	reach := func(where, why string) error {
		return fmt.Errorf("%s: %s %s; a package from a source keeps to its own name, "+
			"${DATA_DIR}/store/%s and named volumes (BUGS L201)", file, where, why, name)
	}
	if f.Include != nil {
		return reach("include", "pulls another compose file in")
	}
	for _, svc := range slices.Sorted(maps.Keys(f.Services)) {
		s := f.Services[svc]
		at := func(key string) string { return "services." + svc + "." + key }
		if svc != name && !strings.HasPrefix(svc, name+"-") && !strings.HasPrefix(svc, name+"_") {
			return reach("services."+svc, "is not named after the package, so it can collide with a service the instance already runs")
		}
		if s.Build != nil || s.Extends != nil {
			return reach(at("build/extends"), "reads the host at build or merge time")
		}
		if s.EnvFile != nil {
			return reach(at("env_file"), "names a file generate writes for release daemons only, and every existing one holds another daemon's secrets (BUGS L200)")
		}
		if s.Privileged || len(s.CapAdd) > 0 || len(s.Devices) > 0 || len(s.VolumesFrom) > 0 ||
			s.Pid != "" || s.UsernsMode != "" || s.Cgroup != "" {
			return reach(at("privileged/cap_add/devices/volumes_from/pid/userns_mode/cgroup"), "escapes the container")
		}
		if s.NetworkMode != "" && s.NetworkMode != "none" && s.NetworkMode != "bridge" {
			return reach(at("network_mode"), "joins another namespace's network")
		}
		if s.Ipc == "host" || strings.HasPrefix(s.Ipc, "service:") {
			return reach(at("ipc"), "joins another namespace's IPC")
		}
		for _, opt := range s.SecurityOpt {
			if strings.Contains(opt, "unconfined") || strings.Contains(opt, "disable") {
				return reach(at("security_opt"), "drops a confinement")
			}
		}
		for i, v := range s.Volumes {
			if why := volumeReach(name, v); why != "" {
				return reach(fmt.Sprintf("%s[%d]", at("volumes"), i), why)
			}
		}
	}
	for _, vol := range slices.Sorted(maps.Keys(f.Volumes)) {
		if _, ok := f.Volumes[vol]["driver"]; ok {
			return reach("volumes."+vol+".driver", "can bind a host path behind a volume name")
		}
		if _, ok := f.Volumes[vol]["driver_opts"]; ok {
			return reach("volumes."+vol+".driver_opts", "can bind a host path behind a volume name")
		}
	}
	if err := hostReadReach("configs", f.Configs, reach); err != nil {
		return err
	}
	return hostReadReach("secrets", f.Secrets, reach)
}

// hostReadReach refuses a config or secret sourced from a host file or the host
// environment; `content:` is the package's own bytes and passes.
func hostReadReach(kind string, entries map[string]map[string]any, reach func(where, why string) error) error {
	for _, k := range slices.Sorted(maps.Keys(entries)) {
		if _, ok := entries[k]["file"]; ok {
			return reach(kind+"."+k+".file", "reads a host file")
		}
		if _, ok := entries[k]["environment"]; ok {
			return reach(kind+"."+k+".environment", "reads the host environment")
		}
	}
	return nil
}

// volumeReach returns why one `volumes:` entry reaches outside the package, or
// "" when it stays on its own ground. Short syntax is `source:target[:mode]`;
// long syntax is a map with `type`.
func volumeReach(name string, v any) string {
	switch spec := v.(type) {
	case string:
		source, _, hasTarget := strings.Cut(spec, ":")
		if !hasTarget {
			return ""
		}
		return sourceReach(name, source)
	case map[string]any:
		source, _ := spec["source"].(string)
		switch spec["type"] {
		case "tmpfs":
			return ""
		case "volume":
			if source == "" || volumeNameRE.MatchString(source) {
				return ""
			}
			return fmt.Sprintf("names volume %q, which is not a volume name", source)
		case "bind":
			return sourceReach(name, source)
		}
		return fmt.Sprintf("has type %v, which install cannot bound", spec["type"])
	}
	return "is not a volume install can read"
}

func sourceReach(name, source string) string {
	if volumeNameRE.MatchString(source) {
		return ""
	}
	rest, ok := strings.CutPrefix(source, "${DATA_DIR}/")
	if ok {
		rest = path.Clean(rest)
		own := "store/" + name
		if rest == own || strings.HasPrefix(rest, own+"/") {
			return ""
		}
	}
	return fmt.Sprintf("binds %q, which is not under ${DATA_DIR}/store/%s", source, name)
}
