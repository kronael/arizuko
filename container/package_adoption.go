package container

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"golang.org/x/sys/unix"
)

type PackageAdoptionSelection struct {
	System []PackageSnapshot
	Group  []PackageSnapshot
}

type PackageAdoptionHistory struct {
	Folder  string
	Package string
	Source  string
	Commit  string
	Units   []PackageAdoptionUnit
}

type PackageAdoptionUnit struct {
	Path    string
	Entries []PackageAdoptionEntry
}

type PackageAdoptionEntry struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

type PackageAdoptionCopy struct {
	Path     string
	Proof    string
	Entries  []PackageAdoptionEntry
	Disabled *PackageAdoptionEntry
}

type PackageAdoptionConflict struct {
	Path   string
	Reason string
}

func (conflict PackageAdoptionConflict) Error() string {
	return conflict.Path + ": " + conflict.Reason
}

type PackageAdoptionPlan struct {
	Copies    []PackageAdoptionCopy
	Conflicts []PackageAdoptionConflict
}

type adoptionProof struct {
	unit   PackageAdoptionUnit
	id     string
	hashes map[string]string
}

type adoptionUnit struct {
	path   string
	owner  string
	proofs []adoptionProof
}

func readPackageAdoptionUnits(cfg *core.Config, folder string,
	selected PackageAdoptionSelection, receipts []store.InstalledPackage,
	history []PackageAdoptionHistory) (map[string]adoptionUnit, error) {
	if folder == "" {
		return nil, fmt.Errorf("adoption requires an exact group folder")
	}
	if _, err := packageTarget(folder); err != nil {
		return nil, err
	}
	inventory, err := readPackageInventory(cfg.ProjectRoot, selected)
	if err != nil {
		return nil, err
	}
	units := map[string]adoptionUnit{}
	add := func(unitPath, owner string, proof adoptionProof) error {
		unit := units[unitPath]
		if unit.owner != "" && unit.owner != owner {
			return fmt.Errorf("overlapping providers for %s: %s and %s", unitPath, unit.owner, owner)
		}
		unit.owner = owner
		unit.path = unitPath
		if proof.id != "" {
			unit.proofs = append(unit.proofs, proof)
		}
		units[unitPath] = unit
		return nil
	}
	providers := map[string]string{}
	for i, snapshots := range [][]PackageSnapshot{selected.System, selected.Group} {
		for _, snapshot := range snapshots {
			bound := ""
			if i == 1 {
				bound = folder
			}
			providers[snapshot.Name] = bound
			dir, err := ResolvePackage(cfg.ProjectRoot, snapshot)
			if err != nil {
				return nil, err
			}
			names, err := ReadPackageSkills(dir)
			if err != nil {
				return nil, err
			}
			var paths []string
			for _, name := range names {
				paths = append(paths, path.Join("skills", name))
			}
			if snapshot.Name == CorePackageName {
				targets, err := packageFileTargets(inventory)
				if err != nil {
					return nil, err
				}
				for _, target := range targets {
					paths = append(paths, strings.TrimPrefix(target, ".claude/"))
				}
			}
			root, err := openPackageRoot(dir)
			if err != nil {
				return nil, err
			}
			for _, rel := range paths {
				unit, err := readPackageAdoptionUnit(root, rel)
				if err == nil {
					err = checkAdoptionUnit(unit, snapshot.Name)
				}
				if err == nil {
					err = add(rel, snapshot.Name, adoptionProof{unit: unit,
						id: "snapshot:" + snapshot.Name + ":" + snapshot.Digest})
				}
				if err != nil {
					return nil, errors.Join(err, root.Close())
				}
			}
			if err := root.Close(); err != nil {
				return nil, err
			}
		}
	}
	owned := map[string]store.InstalledPackage{}
	for _, receipt := range receipts {
		if receipt.Folder != "" && receipt.Folder != folder ||
			!packageNameRE.MatchString(receipt.Name) || receipt.Source == "" || receipt.Revision == "" {
			return nil, fmt.Errorf("invalid adoption receipt binding for %q", receipt.Name)
		}
		if bound, found := providers[receipt.Name]; found && bound != receipt.Folder {
			return nil, fmt.Errorf("receipt folder differs from selection for %s", receipt.Name)
		}
		if _, found := owned[receipt.Name]; found {
			return nil, fmt.Errorf("duplicate adoption receipt %s", receipt.Name)
		}
		if receipt.Name == CorePackageName && (receipt.Folder != "" || receipt.Source != corePackageSource) {
			return nil, fmt.Errorf("invalid required core receipt binding")
		}
		owned[receipt.Name] = receipt
		proofs, err := readAdoptionReceipt(receipt)
		if err != nil {
			return nil, err
		}
		for unitPath, proof := range proofs {
			if err := add(unitPath, receipt.Name, proof); err != nil {
				return nil, err
			}
		}
	}
	for _, previous := range history {
		receipt, found := owned[previous.Package]
		core := previous.Package == CorePackageName && previous.Folder == "" && previous.Source == corePackageSource
		if !isAdoptionCommit(previous.Commit) || !core && (!found || previous.Folder != receipt.Folder ||
			previous.Source != receipt.Source || previous.Commit != receipt.Revision) {
			return nil, fmt.Errorf("unbound adoption history for %q", previous.Package)
		}
		seen := map[string]bool{}
		for _, unit := range previous.Units {
			if err := checkAdoptionUnit(unit, previous.Package); err != nil {
				return nil, err
			}
			if seen[unit.Path] {
				return nil, fmt.Errorf("duplicate historical unit %s", unit.Path)
			}
			seen[unit.Path] = true
			if !core && !slices.Contains(receipt.Manifest["skill"], strings.TrimPrefix(unit.Path, "skills/")) {
				return nil, fmt.Errorf("history unit %s is not receipt-owned", unit.Path)
			}
			proof := adoptionProof{unit: unit, id: fmt.Sprintf("git:%q:%q:%q:%s",
				previous.Folder, previous.Package, previous.Source, previous.Commit)}
			if err := add(unit.Path, previous.Package, proof); err != nil {
				return nil, err
			}
		}
	}
	return units, nil
}

// PlanPackageAdoption requires the package lock. Conflicts refuse the entire
// plan; Copies never authorize applying a subset. Historical bytes are trusted
// source-reader input, not a caller-provided filesystem or diagnostic export.
func PlanPackageAdoption(cfg *core.Config, folder string,
	selected PackageAdoptionSelection, receipts []store.InstalledPackage,
	history []PackageAdoptionHistory) (PackageAdoptionPlan, error) {
	var plan PackageAdoptionPlan
	units, err := readPackageAdoptionUnits(cfg, folder, selected, receipts, history)
	if err != nil {
		return plan, err
	}
	root, home, err := openPackageHome(cfg.GroupsDir, folder)
	if err != nil {
		return plan, err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	data, err := openPackageRoot(cfg.ProjectRoot)
	if err != nil {
		return plan, err
	}
	// Closing a directory handle has no pending writes.
	defer data.Close() //nolint:errcheck
	for _, target := range packageAdoptionTargets(folder, units) {
		anchored, rel := root, path.Join(home, strings.TrimPrefix(target.relative, folder+"/"))
		if target.root == "skills" {
			anchored, rel = data, path.Join("skills", target.relative)
		}
		copy, conflict, err := compareAdoptionUnit(anchored, rel, target.unit, target.mount)
		if err != nil {
			return plan, err
		}
		plan.Copies = append(plan.Copies, copy...)
		plan.Conflicts = append(plan.Conflicts, conflict...)
	}
	slices.SortFunc(plan.Copies, func(a, b PackageAdoptionCopy) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(plan.Conflicts, func(a, b PackageAdoptionConflict) int { return strings.Compare(a.Path, b.Path) })
	return plan, nil
}

func readPackageAdoptionUnit(root *os.Root, rel string) (PackageAdoptionUnit, error) {
	unit := PackageAdoptionUnit{Path: rel}
	if err := checkPackageDirs(root, path.Dir(rel)); err != nil {
		return unit, err
	}
	err := readAdoptionEntries(root, rel, ".", &unit.Entries)
	return unit, err
}

func readAdoptionEntries(root *os.Root, rel, entryPath string, entries *[]PackageAdoptionEntry) error {
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	entry := PackageAdoptionEntry{Path: entryPath, Mode: info.Mode()}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return PackageAdoptionConflict{Path: filepath.Join(root.Name(), rel), Reason: "unsafe entry"}
	}
	if info.IsDir() {
		child, err := root.OpenRoot(rel)
		if err != nil {
			return err
		}
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return errors.Join(fmt.Errorf("directory changed at %s", rel), err, child.Close())
		}
		*entries = append(*entries, entry)
		file, err := child.Open(".")
		if err != nil {
			return errors.Join(err, child.Close())
		}
		names, err := file.ReadDir(-1)
		if err := errors.Join(err, file.Close()); err != nil {
			return errors.Join(err, child.Close())
		}
		slices.SortFunc(names, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, name := range names {
			if err := readAdoptionEntries(child, name.Name(), path.Join(entryPath, name.Name()), entries); err != nil {
				return errors.Join(err, child.Close())
			}
		}
		return child.Close()
	}
	file, err := root.OpenFile(rel, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || info.Mode() != opened.Mode() {
		return errors.Join(fmt.Errorf("file changed at %s", rel), err, file.Close())
	}
	entry.Data, err = io.ReadAll(file)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	*entries = append(*entries, entry)
	return nil
}

func checkAdoptionUnit(unit PackageAdoptionUnit, owner string) error {
	name, skill := strings.CutPrefix(unit.Path, "skills/")
	style := strings.HasPrefix(unit.Path, "output-styles/") && path.Dir(unit.Path) == "output-styles"
	if skill && !skillNameRe.MatchString(name) || !skill &&
		(owner != CorePackageName || unit.Path != "CLAUDE.md" && !style) ||
		!isAdoptionPath(unit.Path) || path.Base(unit.Path) == ".disabled" {
		return fmt.Errorf("invalid adoption unit %q", unit.Path)
	}
	entries := map[string]PackageAdoptionEntry{}
	for _, entry := range unit.Entries {
		if !isAdoptionPath(entry.Path) || path.Base(entry.Path) == ".disabled" ||
			entry.Mode & ^(fs.ModeDir|0o777) != 0 || entry.Mode.IsDir() && len(entry.Data) != 0 {
			return fmt.Errorf("invalid proof entry %s/%s", unit.Path, entry.Path)
		}
		if _, found := entries[entry.Path]; found {
			return fmt.Errorf("duplicate proof entry %s/%s", unit.Path, entry.Path)
		}
		entries[entry.Path] = entry
	}
	root, found := entries["."]
	if !found || root.Mode.IsDir() != skill || !skill && len(entries) != 1 {
		return fmt.Errorf("invalid proof root %s", unit.Path)
	}
	for rel := range entries {
		if rel != "." && !entries[path.Dir(rel)].Mode.IsDir() {
			return fmt.Errorf("missing proof directory for %s/%s", unit.Path, rel)
		}
	}
	if entry, found := entries["SKILL.md"]; skill && (!found || !entry.Mode.IsRegular()) {
		return fmt.Errorf("proof requires SKILL.md at %s", unit.Path)
	}
	return nil
}

func isAdoptionPath(rel string) bool {
	return fs.ValidPath(rel) && !strings.ContainsAny(rel, "\\\x00\r\n")
}

func isAdoptionCommit(commit string) bool {
	decoded, err := hex.DecodeString(commit)
	return err == nil && (len(decoded) == 20 || len(decoded) == 32) && hex.EncodeToString(decoded) == commit
}

func readAdoptionReceipt(receipt store.InstalledPackage) (map[string]adoptionProof, error) {
	proofs := map[string]adoptionProof{}
	for _, name := range receipt.Manifest["skill"] {
		if !skillNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid receipt skill %q", name)
		}
		unitPath := path.Join("skills", name)
		if _, found := proofs[unitPath]; found {
			return nil, fmt.Errorf("duplicate receipt skill %s", name)
		}
		proofs[unitPath] = adoptionProof{unit: PackageAdoptionUnit{Path: unitPath}, hashes: map[string]string{}}
	}
	for key, hash := range receipt.AssetHashes {
		rel, found := strings.CutPrefix(key, "file:.claude/skills/")
		if !found {
			continue
		}
		name, entry, found := strings.Cut(rel, "/")
		proof, owned := proofs[path.Join("skills", name)]
		if !found || !owned || !isAdoptionPath(entry) || entry == "." ||
			path.Base(entry) == ".disabled" || !isPackageDigest(hash) {
			return nil, fmt.Errorf("invalid receipt ownership key %q", key)
		}
		proof.hashes[entry] = hash
	}
	for unitPath, proof := range proofs {
		if len(proof.hashes) == 0 {
			continue
		}
		dirs := map[string]bool{".": true}
		for rel := range proof.hashes {
			proof.unit.Entries = append(proof.unit.Entries, PackageAdoptionEntry{Path: rel})
			for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
				dirs[parent] = true
			}
		}
		for dir := range dirs {
			proof.unit.Entries = append(proof.unit.Entries, PackageAdoptionEntry{Path: dir, Mode: fs.ModeDir})
		}
		if err := checkAdoptionUnit(proof.unit, receipt.Name); err != nil {
			return nil, err
		}
		proof.id = fmt.Sprintf("receipt:%q:%q:%q:%q", receipt.Folder, receipt.Name, receipt.Source, receipt.Revision)
		proofs[unitPath] = proof
	}
	return proofs, nil
}

func compareAdoptionUnit(root *os.Root, rel string, unit adoptionUnit, mount bool) (
	[]PackageAdoptionCopy, []PackageAdoptionConflict, error) {
	display := filepath.Join(root.Name(), rel)
	resident, err := readPackageAdoptionUnit(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		if _, absent := root.Lstat(rel); errors.Is(absent, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	if err != nil {
		var conflict PackageAdoptionConflict
		if errors.As(err, &conflict) {
			return nil, []PackageAdoptionConflict{conflict}, nil
		}
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return nil, nil, err
		}
		return nil, []PackageAdoptionConflict{{Path: display, Reason: err.Error()}}, nil
	}
	copy := PackageAdoptionCopy{Path: display}
	skill := strings.HasPrefix(unit.path, "skills/")
	if resident.Entries[0].Mode.IsDir() != skill {
		return nil, []PackageAdoptionConflict{{Path: display, Reason: "unexpected resident kind"}}, nil
	}
	for _, entry := range resident.Entries {
		if !isAdoptionMode(entry.Mode) {
			return nil, []PackageAdoptionConflict{{Path: filepath.Join(display, entry.Path),
				Reason: "unexpected resident mode"}}, nil
		}
		if entry.Path == ".disabled" && entry.Mode.IsRegular() {
			marker := entry
			copy.Disabled = &marker
		} else {
			copy.Entries = append(copy.Entries, entry)
		}
	}
	if mount && len(copy.Entries) == 1 && (copy.Entries[0].Mode.IsDir() || len(copy.Entries[0].Data) == 0) {
		return nil, nil, nil
	}
	for _, proof := range unit.proofs {
		if matchesAdoptionProof(copy.Entries, proof, mount) {
			copy.Proof = proof.id
			return []PackageAdoptionCopy{copy}, nil, nil
		}
	}
	return nil, []PackageAdoptionConflict{{Path: display,
		Reason: "resident unit has no complete matching ownership proof"}}, nil
}

func matchesAdoptionProof(entries []PackageAdoptionEntry, proof adoptionProof, mount bool) bool {
	if proof.hashes != nil && !mount || len(entries) != len(proof.unit.Entries) {
		return false
	}
	expected := map[string]PackageAdoptionEntry{}
	for _, entry := range proof.unit.Entries {
		expected[entry.Path] = entry
	}
	for _, entry := range entries {
		want, found := expected[entry.Path]
		if !found || entry.Mode.IsDir() != want.Mode.IsDir() ||
			!entry.Mode.IsDir() && (proof.hashes == nil && !bytes.Equal(entry.Data, want.Data) ||
				proof.hashes != nil && hashBytes(entry.Data) != proof.hashes[entry.Path]) {
			return false
		}
		delete(expected, entry.Path)
	}
	return len(expected) == 0
}
