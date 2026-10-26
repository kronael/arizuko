package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

var adoptionReleaseTagRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var adoptionSkillNameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

func readCorePackageAdoptionHistory(cfg *core.Config) (
	[]container.PackageAdoptionHistory, error) {
	if cfg.HostAppDir == "" {
		return nil, fmt.Errorf("core history requires configured HOST_APP_DIR")
	}
	prefix, err := runAdoptionGit(cfg.HostAppDir, []string{"rev-parse", "--show-prefix"})
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(prefix)) != 0 {
		return nil, fmt.Errorf("configured HOST_APP_DIR must name the source checkout root")
	}
	head, err := runAdoptionGit(cfg.HostAppDir, []string{
		"rev-parse", "--verify", "HEAD^{commit}",
	})
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(head))
	if !isAdoptionObjectID(commit) {
		return nil, fmt.Errorf("invalid core HEAD commit %q", commit)
	}
	tags, err := runAdoptionGit(cfg.HostAppDir, []string{
		"tag", "--list", "v*",
	})
	if err != nil {
		return nil, err
	}
	var history []container.PackageAdoptionHistory
	seen := map[string]bool{}
	for tag := range strings.FieldsSeq(string(tags)) {
		if !adoptionReleaseTagRE.MatchString(tag) {
			continue
		}
		resolved, err := runAdoptionGit(cfg.HostAppDir, []string{
			"rev-parse", "--verify", "refs/tags/" + tag + "^{commit}",
		})
		if err != nil {
			return nil, err
		}
		revision := strings.TrimSpace(string(resolved))
		if !isAdoptionObjectID(revision) {
			return nil, fmt.Errorf("invalid release commit %q", revision)
		}
		_, err = runAdoptionGit(cfg.HostAppDir, []string{
			"merge-base", "--is-ancestor", revision, commit,
		})
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				continue
			}
			return nil, err
		}
		if seen[revision] {
			continue
		}
		seen[revision] = true
		units, err := readGitAdoptionUnits(cfg.HostAppDir, revision, "ant/",
			[]string{"skills", "CLAUDE.md", "output-styles"})
		if err != nil {
			return nil, err
		}
		history = append(history, container.PackageAdoptionHistory{
			Package: container.CorePackageName, Source: "ant", Commit: revision, Units: units,
		})
	}
	return history, nil
}

func readOptionalPackageAdoptionHistory(folder string,
	receipt store.InstalledPackage, source packageCandidate, dir string) (
	container.PackageAdoptionHistory, error) {
	history := container.PackageAdoptionHistory{}
	if receipt.Folder != "" && receipt.Folder != folder ||
		receipt.Name == container.CorePackageName || !pkgNameRE.MatchString(receipt.Name) ||
		receipt.Name != source.snapshot.Name || receipt.Source == "" ||
		receipt.Source != source.source || !isGitURL(receipt.Source) ||
		!isAdoptionObjectID(receipt.Revision) || dir == "" {
		return history, fmt.Errorf("unbound optional history for %q", receipt.Name)
	}
	history = container.PackageAdoptionHistory{
		Folder: receipt.Folder, Package: receipt.Name, Source: receipt.Source,
		Commit: receipt.Revision,
	}
	var paths []string
	for _, name := range receipt.Manifest[manifestSkills] {
		if !adoptionSkillNameRE.MatchString(name) ||
			slices.Contains(paths, "skills/"+name) {
			return history, fmt.Errorf("invalid receipt skill %q", name)
		}
		paths = append(paths, "skills/"+name)
	}
	if len(paths) == 0 {
		return history, nil
	}
	prefix, err := runAdoptionGit(dir, []string{"rev-parse", "--show-prefix"})
	if err != nil {
		return history, err
	}
	if len(bytes.TrimSpace(prefix)) != 0 {
		return history, fmt.Errorf("optional source must name the checkout root")
	}
	resolved, err := runAdoptionGit(dir, []string{
		"rev-parse", "--verify", receipt.Revision + "^{commit}",
	})
	if err != nil {
		return history, err
	}
	if strings.TrimSpace(string(resolved)) != receipt.Revision {
		return history, fmt.Errorf("receipt revision is not a commit")
	}
	units, err := readGitAdoptionUnits(dir, receipt.Revision, "", paths)
	if err != nil {
		return history, err
	}
	if len(units) != len(paths) {
		return history, fmt.Errorf("receipt revision lacks complete skill units")
	}
	history.Units = units
	return history, nil
}

func readGitAdoptionUnits(dir, commit, prefix string, paths []string) (
	[]container.PackageAdoptionUnit, error) {
	args := []string{"ls-tree", "-r", "-t", "-z", commit, "--"}
	for _, rel := range paths {
		args = append(args, prefix+rel)
	}
	listing, err := runAdoptionGit(dir, args)
	if err != nil {
		return nil, err
	}
	units := map[string]container.PackageAdoptionUnit{}
	entries := map[string]fs.FileMode{}
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, name, found := strings.Cut(string(record), "\t")
		fields := strings.Fields(metadata)
		if !found || len(fields) != 3 || !isAdoptionObjectID(fields[2]) {
			return nil, fmt.Errorf("malformed Git tree entry %q", record)
		}
		if prefix != "" && name == strings.TrimSuffix(prefix, "/") &&
			fields[0] == "040000" && fields[1] == "tree" {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			return nil, fmt.Errorf("unexpected Git tree path %q", name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if !fs.ValidPath(rel) || strings.ContainsAny(rel, "\\\x00\r\n") ||
			path.Base(rel) == ".disabled" {
			return nil, fmt.Errorf("invalid Git tree path %q", rel)
		}
		var mode fs.FileMode
		switch {
		case fields[0] == "040000" && fields[1] == "tree":
			mode = fs.ModeDir | 0o755
		case fields[0] == "100644" && fields[1] == "blob":
			mode = 0o644
		case fields[0] == "100755" && fields[1] == "blob":
			mode = 0o755
		default:
			return nil, fmt.Errorf("unsupported Git entry %q at %s", metadata, rel)
		}
		if rel == "skills" || rel == "output-styles" {
			if !mode.IsDir() {
				return nil, fmt.Errorf("git entry %s is not a directory", rel)
			}
			continue
		}
		unitPath, entryPath := rel, "."
		if skill, found := strings.CutPrefix(rel, "skills/"); found {
			name, rest, nested := strings.Cut(skill, "/")
			if !adoptionSkillNameRE.MatchString(name) {
				return nil, fmt.Errorf("invalid historical skill %q", name)
			}
			unitPath = "skills/" + name
			if nested {
				entryPath = rest
			} else if !mode.IsDir() {
				return nil, fmt.Errorf("historical skill %s is not a directory", name)
			}
		} else if rel != "CLAUDE.md" &&
			(path.Dir(rel) != "output-styles" || mode.IsDir()) {
			return nil, fmt.Errorf("invalid historical unit %q", rel)
		}
		if rel == "CLAUDE.md" && mode.IsDir() {
			return nil, fmt.Errorf("historical manual is not a file")
		}
		if _, found := entries[rel]; found {
			return nil, fmt.Errorf("duplicate historical entry %q", rel)
		}
		if entryPath != "." && !entries[path.Dir(rel)].IsDir() {
			return nil, fmt.Errorf("missing historical directory for %q", rel)
		}
		entries[rel] = mode
		var data []byte
		if mode.IsRegular() {
			data, err = runAdoptionGit(dir, []string{"cat-file", "blob", fields[2]})
			if err != nil {
				return nil, err
			}
		}
		unit := units[unitPath]
		unit.Path = unitPath
		unit.Entries = append(unit.Entries, container.PackageAdoptionEntry{
			Path: entryPath, Mode: mode, Data: data,
		})
		units[unitPath] = unit
	}
	var result []container.PackageAdoptionUnit
	for _, unit := range units {
		if strings.HasPrefix(unit.Path, "skills/") {
			found := slices.ContainsFunc(unit.Entries,
				func(entry container.PackageAdoptionEntry) bool {
					return entry.Path == "SKILL.md" && entry.Mode.IsRegular()
				})
			if !found {
				return nil, fmt.Errorf("historical skill %s lacks SKILL.md", unit.Path)
			}
		}
		result = append(result, unit)
	}
	slices.SortFunc(result, func(a, b container.PackageAdoptionUnit) int {
		return strings.Compare(a.Path, b.Path)
	})
	return result, nil
}

func isAdoptionObjectID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && (len(decoded) == 20 || len(decoded) == 32) &&
		hex.EncodeToString(decoded) == value
}

func runAdoptionGit(dir string, args []string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{
		"--no-replace-objects", "--literal-pathspecs", "-C", dir,
	}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_GRAFT_FILE=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read package history with git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}
