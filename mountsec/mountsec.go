package mountsec

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

var defaultBlocked = []string{
	".ssh", ".gnupg", ".gpg", ".aws", ".azure", ".gcloud",
	".kube", ".docker", "credentials", ".env", ".netrc",
	".npmrc", ".pypirc", "id_rsa", "id_ed25519",
	"private_key", ".secret",
}

type AllowedRoot struct {
	Path           string `json:"path"`
	AllowReadWrite bool   `json:"allowReadWrite"`
	Description    string `json:"description,omitempty"`
}

type Allowlist struct {
	AllowedRoots    []AllowedRoot `json:"allowedRoots"`
	BlockedPatterns []string      `json:"blockedPatterns"`
	NonMainReadOnly bool          `json:"nonMainReadOnly"`
}

type AdditionalMount struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath,omitempty"`
	Readonly      *bool  `json:"readonly,omitempty"`
}

type ValidMount struct {
	HostPath      string
	ContainerPath string
	Readonly      bool
}

// ValidateFilePath resolves symlinks before checking containment and
// blocked patterns; returns the resolved real path.
func ValidateFilePath(path, root string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("path not found")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("root not found")
	}
	if !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside allowed directory")
	}
	if pat := matchesBlocked(real, defaultBlocked); pat != "" {
		return "", fmt.Errorf("blocked path pattern %q", pat)
	}
	return real, nil
}

func ValidateAdditionalMounts(
	mounts []AdditionalMount,
	groupName string,
	isRoot bool,
	al Allowlist,
) []ValidMount {
	var out []ValidMount
	for _, m := range mounts {
		v, ok, reason := validateOne(m, isRoot, al)
		if ok {
			out = append(out, v)
			slog.Debug("mount validated",
				"group", groupName,
				"host", v.HostPath,
				"container", v.ContainerPath,
				"readonly", v.Readonly)
		} else {
			slog.Warn("mount rejected",
				"group", groupName,
				"host", m.HostPath,
				"reason", reason)
		}
	}
	return out
}

func validateOne(m AdditionalMount, isRoot bool, al Allowlist) (ValidMount, bool, string) {
	if len(al.AllowedRoots) == 0 {
		return ValidMount{}, false, "no allowlist configured"
	}

	expanded := expandHome(m.HostPath)
	if !filepath.IsAbs(expanded) {
		return ValidMount{}, false, "host path not absolute after expansion: " + expanded
	}

	real, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return ValidMount{}, false, "host path does not exist: " + expanded
	}

	if pat := matchesBlocked(real, al.BlockedPatterns); pat != "" {
		return ValidMount{}, false, "matches blocked pattern \"" + pat + "\": " + real
	}

	root := findAllowedRoot(real, al.AllowedRoots)
	if root == nil {
		return ValidMount{}, false, "not under any allowed root: " + real
	}

	cp := m.ContainerPath
	if cp == "" {
		cp = filepath.Base(m.HostPath)
	}
	if !validContainerPath(cp) {
		return ValidMount{}, false, "invalid container path: " + cp
	}

	ro := true
	if m.Readonly != nil && !*m.Readonly {
		switch {
		case !isRoot && al.NonMainReadOnly:
			slog.Info("mount forced readonly for non-main group", "host", m.HostPath)
		case !root.AllowReadWrite:
			slog.Info("mount forced readonly, root disallows rw", "host", m.HostPath, "root", root.Path)
		default:
			ro = false
		}
	}

	return ValidMount{
		HostPath:      real,
		ContainerPath: "/workspace/extra/" + cp,
		Readonly:      ro,
	}, true, ""
}

func expandHome(p string) string {
	home, _ := os.UserHomeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func matchesBlocked(real string, patterns []string) string {
	for _, pat := range patterns {
		if strings.Contains(real, pat) {
			return pat
		}
	}
	return ""
}

func findAllowedRoot(real string, roots []AllowedRoot) *AllowedRoot {
	for i := range roots {
		expanded := expandHome(roots[i].Path)
		rr, err := filepath.EvalSymlinks(expanded)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rr, real)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return &roots[i]
		}
	}
	return nil
}

func validContainerPath(p string) bool {
	return strings.TrimSpace(p) != "" &&
		!strings.Contains(p, "..") &&
		!strings.HasPrefix(p, "/")
}
