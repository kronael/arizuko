package container

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

type adoptionBinding struct {
	folder           string
	snapshot         PackageSnapshot
	source, revision string
}

type adoptionTarget struct {
	root, relative, folder string
	unit                   adoptionUnit
	mount                  bool
}

type adoptionEntry struct {
	path        string
	mode        fs.FileMode
	fingerprint string
}

type adoptionTree struct {
	target  adoptionTarget
	entries []adoptionEntry
	marker  *adoptionEntry
}

type adoptionScope struct {
	folder   string
	active   PackageAdoptionSelection
	selected PackageAdoptionSelection
	receipts []store.InstalledPackage
	history  []PackageAdoptionHistory
	plan     PackageAdoptionPlan
}

type adoptionBatch struct {
	cfg      core.Config
	scopes   []adoptionScope
	bindings []adoptionBinding
}

type adoptionPhase uint8

const (
	adoptionPrepare adoptionPhase = iota
	adoptionApply
	adoptionActivate
)

type adoptionReceipt struct {
	binding  adoptionBinding
	previous store.InstalledPackage
	hashes   map[string]string
	trees    []adoptionTree
}

func cloneAdoptionReceipt(receipt store.InstalledPackage) store.InstalledPackage {
	receipt.Manifest = maps.Clone(receipt.Manifest)
	if receipt.Manifest == nil {
		receipt.Manifest = map[string][]string{}
	}
	for key, values := range receipt.Manifest {
		receipt.Manifest[key] = slices.Clone(values)
	}
	receipt.AssetHashes = maps.Clone(receipt.AssetHashes)
	if receipt.AssetHashes == nil {
		receipt.AssetHashes = map[string]string{}
	}
	return receipt
}

func adoptionCoordinate(folder, name string) string { return folder + ":" + name }

func isSameAdoptionSelection(a, b []PackageSnapshot) bool {
	left, right := slices.Clone(a), slices.Clone(b)
	for _, selected := range [][]PackageSnapshot{left, right} {
		slices.SortFunc(selected, func(a, b PackageSnapshot) int {
			if a.Name != b.Name {
				return strings.Compare(a.Name, b.Name)
			}
			return strings.Compare(a.Digest, b.Digest)
		})
	}
	return slices.Equal(left, right)
}

func readAdoptionBatch(cfg *core.Config, database *store.Store, batch adoptionBatch,
	phase adoptionPhase) ([]adoptionReceipt, error) {
	if database == nil || cfg == nil || cfg.ProjectRoot == "" || cfg.GroupsDir == "" ||
		cfg.ProjectRoot != batch.cfg.ProjectRoot || cfg.GroupsDir != batch.cfg.GroupsDir ||
		cfg.EffectiveAppSrcDir() != batch.cfg.EffectiveAppSrcDir() || len(batch.bindings) == 0 {
		return nil, fmt.Errorf("adoption requires a store and unchanged configured roots")
	}
	folders, err := database.GroupFolders(0)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, folder := range folders {
		registered[folder] = true
	}
	bindings := map[string]adoptionBinding{}
	system := false
	for _, binding := range batch.bindings {
		if err := checkAdoptionBinding(binding); err != nil {
			return nil, err
		}
		key := adoptionCoordinate(binding.folder, binding.snapshot.Name)
		if _, duplicate := bindings[key]; duplicate || binding.folder != "" && !registered[binding.folder] {
			return nil, fmt.Errorf("duplicate or unregistered adoption binding")
		}
		bindings[key] = binding
		system = system || binding.folder == ""
	}
	rows, err := database.InstalledPackages()
	if err != nil {
		return nil, err
	}
	persisted := map[string]store.InstalledPackage{}
	for _, row := range rows {
		persisted[adoptionCoordinate(row.Folder, row.Name)] = cloneAdoptionReceipt(row)
		if HasPendingAdoption(row.AssetHashes) {
			if _, bound := bindings[adoptionCoordinate(row.Folder, row.Name)]; !bound {
				return nil, fmt.Errorf("pending adoption is outside the operation witness")
			}
		}
	}
	completed := map[string]bool{}
	inventory := map[string][]string{}
	for key, binding := range bindings {
		row := persisted[key]
		dir, err := ResolvePackage(cfg.ProjectRoot, binding.snapshot)
		if err != nil {
			return nil, err
		}
		names, err := ReadPackageSkills(dir)
		if err != nil {
			return nil, err
		}
		inventory[key] = names
		completed[key] = !HasPendingAdoption(row.AssetHashes) && row.Source == binding.source &&
			row.Revision == binding.revision && row.InstalledAt != "" &&
			slices.Equal(row.Manifest[PackageSourceArtifact], []string{binding.snapshot.Digest}) &&
			row.AssetHashes[PackageSourceArtifact] == binding.snapshot.Digest &&
			slices.Equal(row.Manifest["skill"], names)
	}
	replaying := false
	originals := map[string]store.InstalledPackage{}
	targets := map[string][]adoptionTarget{}
	plans := map[string]PackageAdoptionPlan{}
	freshErrors := map[string]error{}
	changedReceipts := map[string]bool{}
	seen := map[string]bool{}
	var proposedSystem []PackageSnapshot
	for i, scope := range batch.scopes {
		if scope.folder == "" || !registered[scope.folder] || seen[scope.folder] || len(scope.plan.Conflicts) != 0 {
			return nil, fmt.Errorf("invalid adoption scope")
		}
		seen[scope.folder] = true
		if i == 0 {
			proposedSystem = scope.selected.System
		} else if !isSameAdoptionSelection(proposedSystem, scope.selected.System) {
			return nil, fmt.Errorf("conflicting proposed system selections")
		}
		related := system
		group := false
		for _, binding := range batch.bindings {
			if binding.folder == scope.folder {
				related = true
				group = true
			}
			if binding.folder != "" && binding.folder != scope.folder {
				continue
			}
			selected := scope.selected.System
			if binding.folder != "" {
				selected = scope.selected.Group
			}
			if !slices.Contains(selected, binding.snapshot) {
				return nil, fmt.Errorf("adoption candidate differs from proposed selection")
			}
		}
		if !related {
			return nil, fmt.Errorf("unrelated adoption scope")
		}
		for i, selected := range [][]PackageSnapshot{scope.selected.System, scope.selected.Group} {
			if i == 0 && !system || i == 1 && !group {
				continue
			}
			folder := ""
			if i == 1 {
				folder = scope.folder
			}
			for _, snapshot := range selected {
				binding, found := bindings[adoptionCoordinate(folder, snapshot.Name)]
				if !found || binding.snapshot != snapshot {
					return nil, fmt.Errorf("activation candidate lacks its complete adoption binding")
				}
			}
		}
		for j, target := range []string{"", scope.folder} {
			active, proposed := scope.active.System, scope.selected.System
			if j == 1 {
				active, proposed = scope.active.Group, scope.selected.Group
			}
			current, err := ReadPackageSelection(cfg.ProjectRoot, target)
			if err != nil {
				return nil, err
			}
			if !isSameAdoptionSelection(current, active) {
				if !isSameAdoptionSelection(current, proposed) {
					return nil, fmt.Errorf("package selection changed after adoption preview")
				}
				replaying = true
			}
		}
		expected := map[string]store.InstalledPackage{}
		for _, row := range scope.receipts {
			key := adoptionCoordinate(row.Folder, row.Name)
			if row.Folder != "" && row.Folder != scope.folder {
				return nil, fmt.Errorf("receipt is outside adoption scope")
			}
			if _, duplicate := expected[key]; duplicate {
				return nil, fmt.Errorf("duplicate adoption receipt preimage")
			}
			row = cloneAdoptionReceipt(row)
			if previous, found := originals[key]; found && !reflect.DeepEqual(previous, row) {
				return nil, fmt.Errorf("conflicting receipt preimages")
			}
			expected[key], originals[key] = row, row
		}
		for key, row := range persisted {
			if row.Folder != "" && row.Folder != scope.folder {
				continue
			}
			old, found := expected[key]
			binding, bound := bindings[key]
			plain := cloneAdoptionReceipt(row)
			maps.DeleteFunc(plain.AssetHashes, func(key, _ string) bool { return strings.HasPrefix(key, adoptionPrefix) })
			if bound && !found {
				old = cloneAdoptionReceipt(store.InstalledPackage{Folder: binding.folder, Name: binding.snapshot.Name,
					Source: binding.source, Revision: binding.revision})
			}
			if !found && !bound || !reflect.DeepEqual(plain, old) && !completed[key] {
				return nil, fmt.Errorf("adoption receipt preimage changed: %s", key)
			}
			if !reflect.DeepEqual(plain, old) {
				changedReceipts[key] = true
			}
		}
		for key := range expected {
			if _, found := persisted[key]; !found {
				return nil, fmt.Errorf("adoption receipt preimage disappeared")
			}
		}
		units, err := readPackageAdoptionUnits(cfg, scope.folder, scope.selected, scope.receipts, scope.history)
		if err != nil {
			return nil, err
		}
		derived := packageAdoptionTargets(scope.folder, units)
		captured := map[string]PackageAdoptionCopy{}
		for _, copy := range scope.plan.Copies {
			if _, duplicate := captured[copy.Path]; duplicate {
				return nil, fmt.Errorf("duplicate adoption preview copy")
			}
			captured[copy.Path] = copy
		}
		for _, target := range derived {
			folder := ""
			for _, snapshot := range scope.selected.Group {
				if snapshot.Name == target.unit.owner {
					folder = scope.folder
				}
			}
			key := adoptionCoordinate(folder, target.unit.owner)
			display, err := adoptionDisplay(cfg, target)
			if err != nil {
				return nil, err
			}
			copy, copied := captured[display]
			delete(captured, display)
			_, bound := bindings[key]
			if copied && !bound {
				return nil, fmt.Errorf("copied unit lacks an adoption binding")
			}
			row := persisted[key]
			recorded := HasPendingAdoption(row.AssetHashes) &&
				row.AssetHashes[adoptionEntryKey(target, "tree", ".")] != ""
			if !recorded && (phase == adoptionPrepare || !copied || !completed[key]) {
				root, rel, err := openAdoptionTarget(cfg, target)
				if err != nil {
					return nil, err
				}
				fresh, conflicts, err := compareAdoptionUnit(root, rel, target.unit, target.mount)
				if err := errors.Join(err, root.Close()); err != nil {
					return nil, err
				}
				if len(conflicts) != 0 || copied != (len(fresh) == 1) || copied && !reflect.DeepEqual(copy, fresh[0]) {
					changed := fmt.Errorf("adoption preview changed at %s", display)
					if copied && completed[key] && phase == adoptionPrepare {
						freshErrors[key] = changed
					} else {
						return nil, changed
					}
				}
			}
			if !bound {
				continue
			}
			duplicate := false
			for _, previous := range targets[key] {
				if previous.root == target.root && previous.relative == target.relative {
					if target.root != "skills" || previous.unit.owner != target.unit.owner || previous.unit.path != target.unit.path {
						return nil, fmt.Errorf("conflicting adoption targets")
					}
					duplicate = true
				}
			}
			if !duplicate {
				targets[key] = append(targets[key], target)
			}
			if copied {
				plan := plans[key]
				duplicate = false
				for _, previous := range plan.Copies {
					if previous.Path == copy.Path {
						if !reflect.DeepEqual(previous, copy) {
							return nil, fmt.Errorf("conflicting shared adoption copy")
						}
						duplicate = true
					}
				}
				if !duplicate {
					plan.Copies = append(plan.Copies, copy)
					plans[key] = plan
				}
			}
		}
		if len(captured) != 0 {
			return nil, fmt.Errorf("adoption preview contains an unmatched copy")
		}
	}
	if len(seen) == 0 || system && len(seen) != len(registered) {
		return nil, fmt.Errorf("adoption scopes differ from the group registry")
	}
	for _, binding := range batch.bindings {
		if binding.folder != "" && !seen[binding.folder] {
			return nil, fmt.Errorf("adoption binding lacks its exact scope")
		}
	}
	var result []adoptionReceipt
	claimed := map[string]bool{}
	for _, binding := range batch.bindings {
		key := adoptionCoordinate(binding.folder, binding.snapshot.Name)
		row, exists := persisted[key]
		hashes := row.AssetHashes
		pending := HasPendingAdoption(hashes)
		if !pending {
			if (phase != adoptionPrepare || replaying) && len(plans[key].Copies) != 0 && !completed[key] {
				return nil, fmt.Errorf("adoption receipt has no pending proof or matching completion")
			}
			hashes, err = encodePackageAdoption(cfg, binding, targets[key], plans[key])
			if err != nil {
				return nil, err
			}
		}
		trees, err := readPackageAdoptionLedger(binding, hashes, targets[key])
		if err != nil {
			return nil, err
		}
		if phase != adoptionPrepare && !exists && len(trees) != 0 {
			return nil, fmt.Errorf("adoption receipt was not prepared")
		}
		if changedReceipts[key] {
			old, found := originals[key]
			if !found {
				old = store.InstalledPackage{Source: binding.source, Revision: binding.revision}
			}
			expected := packageActivationReceipt(binding.folder, binding.snapshot, old, inventory[key])
			expected.Source, expected.Revision = binding.source, binding.revision
			expected = completeAdoptionReceipt(expected, old, trees)
			expected.InstalledAt = row.InstalledAt
			if !reflect.DeepEqual(expected, row) {
				return nil, fmt.Errorf("adoption receipt preimage changed: %s", key)
			}
		}
		for _, tree := range trees {
			name := tree.target.root + "/" + tree.target.relative
			if claimed[name] {
				return nil, fmt.Errorf("multiple receipts claim an adoption target")
			}
			claimed[name] = true
		}
		if err := checkPackageAdoptionTrees(cfg, trees); err != nil {
			return nil, err
		}
		if phase == adoptionActivate || !pending &&
			(phase != adoptionPrepare || replaying || freshErrors[key] != nil || changedReceipts[key]) {
			if err := checkAdoptionTerminal(cfg, trees); err != nil {
				return nil, err
			}
			if !pending {
				hashes = nil
			}
		}
		if !exists {
			row = cloneAdoptionReceipt(store.InstalledPackage{Folder: binding.folder, Name: binding.snapshot.Name,
				Source: binding.source, Revision: binding.revision})
		}
		result = append(result, adoptionReceipt{binding: binding, previous: row, hashes: hashes, trees: trees})
	}
	return result, nil
}

func prepareAdoptionReceipts(cfg *core.Config, database *store.Store, batch adoptionBatch) error {
	rows, err := readAdoptionBatch(cfg, database, batch, adoptionPrepare)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if !HasPendingAdoption(row.hashes) || HasPendingAdoption(row.previous.AssetHashes) {
			continue
		}
		receipt := cloneAdoptionReceipt(row.previous)
		maps.Copy(receipt.AssetHashes, row.hashes)
		if err := database.PutInstalledPackage(receipt); err != nil {
			return fmt.Errorf("prepare adoption receipt %s: %w", receipt.Name, err)
		}
	}
	return nil
}

func openAdoptionTarget(cfg *core.Config, target adoptionTarget) (*os.Root, string, error) {
	if target.root == "groups" {
		root, rel, err := openPackageHome(cfg.GroupsDir, target.folder)
		return root, path.Join(rel, strings.TrimPrefix(target.relative, target.folder+"/")), err
	}
	root, err := openPackageRoot(cfg.ProjectRoot)
	return root, path.Join("skills", target.relative), err
}

func unlinkAdoptionEntry(cfg *core.Config, tree adoptionTree, entry adoptionEntry) error {
	if !entry.mode.IsRegular() || path.Base(entry.path) == ".disabled" || !slices.Contains(tree.entries, entry) {
		return fmt.Errorf("adoption unlink requires a recorded regular file")
	}
	root, rel, err := openAdoptionTarget(cfg, tree.target)
	if err != nil {
		return err
	}
	// Directory handles carry no pending writes.
	defer root.Close() //nolint:errcheck
	rel = path.Join(rel, entry.path)
	if err := checkPackageDirs(root, path.Dir(rel)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	resident, err := readPackageAdoptionUnit(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(resident.Entries) != 1 || resident.Entries[0].Mode != entry.mode {
		return fmt.Errorf("adoption file kind or mode changed before unlink")
	}
	fingerprint, err := adoptionFingerprint(entry.mode, hashBytes(resident.Entries[0].Data))
	if err != nil || fingerprint != entry.fingerprint {
		return fmt.Errorf("adoption file bytes changed before unlink")
	}
	return root.Remove(rel)
}

func applyPackageAdoption(cfg *core.Config, database *store.Store, batch adoptionBatch) error {
	rows, err := readAdoptionBatch(cfg, database, batch, adoptionApply)
	if err != nil {
		return err
	}
	for _, row := range rows {
		for _, tree := range row.trees {
			for _, entry := range tree.entries {
				if entry.mode.IsRegular() {
					if err := unlinkAdoptionEntry(cfg, tree, entry); err != nil {
						return err
					}
				}
			}
			entries := slices.Clone(tree.entries)
			slices.SortFunc(entries, func(a, b adoptionEntry) int { return strings.Compare(b.path, a.path) })
			root, rel, err := openAdoptionTarget(cfg, tree.target)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !entry.mode.IsDir() || entry.path == "." && (tree.target.mount || tree.marker != nil) {
					continue
				}
				name := path.Join(rel, entry.path)
				if err = checkPackageDirs(root, path.Dir(name)); err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						err = nil
						continue
					}
					break
				}
				info, statErr := root.Lstat(name)
				if errors.Is(statErr, fs.ErrNotExist) {
					continue
				}
				if statErr != nil || info.Mode() != entry.mode {
					err = fmt.Errorf("adoption directory changed before removal: %s", name)
					break
				}
				if err = root.Remove(name); err != nil {
					break
				}
			}
			if err := errors.Join(err, root.Close()); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkAdoptionTerminal(cfg *core.Config, trees []adoptionTree) error {
	for _, tree := range trees {
		root, rel, err := openAdoptionTarget(cfg, tree.target)
		if err != nil {
			return err
		}
		for _, entry := range tree.entries {
			if entry.path == "." && entry.mode.IsDir() && (tree.target.mount || tree.marker != nil) {
				continue
			}
			_, err = root.Lstat(path.Join(rel, entry.path))
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
				continue
			}
			if err == nil {
				err = fmt.Errorf("adoption payload remains before activation")
			}
			break
		}
		if err := errors.Join(err, root.Close()); err != nil {
			return err
		}
	}
	return nil
}

func packageAdoptionTargets(folder string, units map[string]adoptionUnit) []adoptionTarget {
	var targets []adoptionTarget
	for _, unit := range units {
		if name, skill := strings.CutPrefix(unit.path, "skills/"); skill {
			for i, relative := range packageSkillTargets(name) {
				targets = append(targets, adoptionTarget{root: "groups", relative: path.Join(folder, relative),
					folder: folder, unit: unit, mount: i == 0})
			}
			targets = append(targets, adoptionTarget{root: "skills", relative: name, unit: unit})
		} else {
			targets = append(targets, adoptionTarget{root: "groups",
				relative: path.Join(folder, packageFileTarget(unit.path)), folder: folder, unit: unit, mount: true})
		}
	}
	return targets
}

func isAdoptionMode(mode fs.FileMode) bool {
	allowed, required := fs.FileMode(0o644), fs.FileMode(0o400)
	if mode.IsDir() {
		allowed, required = fs.ModeDir|0o755, fs.ModeDir|0o500
	}
	return mode & ^allowed == 0 && mode&required == required
}

func HasPendingAdoption(hashes map[string]string) bool {
	for key := range hashes {
		if strings.HasPrefix(key, adoptionPrefix) {
			return true
		}
	}
	return false
}

const adoptionPrefix = "pending:adopt:"

func checkAdoptionBinding(binding adoptionBinding) error {
	if !packageNameRE.MatchString(binding.snapshot.Name) || !isPackageDigest(binding.snapshot.Digest) ||
		binding.source == "" || binding.revision == "" {
		return fmt.Errorf("invalid adoption binding")
	}
	if _, err := packageTarget(binding.folder); err != nil {
		return err
	}
	if binding.snapshot.Name == CorePackageName && (binding.folder != "" ||
		binding.source != corePackageSource || binding.revision != binding.snapshot.Digest) {
		return fmt.Errorf("invalid core adoption binding")
	}
	return nil
}

func checkAdoptionTargets(binding adoptionBinding, targets []adoptionTarget) error {
	if err := checkAdoptionBinding(binding); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if !packageNameRE.MatchString(target.unit.owner) || !isAdoptionPath(target.relative) {
			return fmt.Errorf("invalid adoption target")
		}
		if _, err := packageTarget(target.folder); err != nil {
			return err
		}
		for _, proof := range target.unit.proofs {
			if proof.id == "" {
				continue
			}
			if err := checkAdoptionUnit(proof.unit, target.unit.owner); err != nil {
				return err
			}
			if proof.unit.Path != target.unit.path {
				return fmt.Errorf("adoption target differs from its source unit")
			}
		}
		valid := false
		units := map[string]adoptionUnit{target.unit.path: target.unit}
		for _, derived := range packageAdoptionTargets(target.folder, units) {
			if derived.root == target.root && derived.relative == target.relative &&
				derived.folder == target.folder && derived.mount == target.mount {
				valid = true
			}
		}
		key := target.root + "/" + target.relative
		if !valid || seen[key] || binding.folder != "" && target.unit.owner == binding.snapshot.Name &&
			target.folder != "" && target.folder != binding.folder {
			return fmt.Errorf("invalid or duplicate adoption target %s", key)
		}
		seen[key] = true
	}
	return nil
}

func adoptionDisplay(cfg *core.Config, target adoptionTarget) (string, error) {
	root := cfg.GroupsDir
	if target.root == "skills" {
		root = filepath.Join(cfg.ProjectRoot, "skills")
	}
	return filepath.Abs(filepath.Join(root, filepath.FromSlash(target.relative)))
}

func appendAdoptionString(data []byte, value string) []byte {
	data = binary.BigEndian.AppendUint64(data, uint64(len(value)))
	return append(data, value...)
}

func adoptionSource(binding adoptionBinding) string {
	var data []byte
	for _, value := range []string{"adopt-source-v1", binding.folder, binding.snapshot.Name,
		binding.source, binding.revision} {
		data = appendAdoptionString(data, value)
	}
	return hashBytes(data)
}

func adoptionFingerprint(mode fs.FileMode, content string) (string, error) {
	kind := "file"
	if mode.IsDir() {
		kind = "dir"
	}
	data := binary.BigEndian.AppendUint32(appendAdoptionString(nil, kind), uint32(mode.Perm()))
	if !mode.IsDir() {
		decoded, err := hex.DecodeString(content)
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid adoption content hash")
		}
		data = append(data, decoded...)
	}
	return hashBytes(data), nil
}

func adoptionEntryKind(entry adoptionEntry) string {
	if entry.mode.IsDir() {
		return "dir"
	}
	return "file"
}

func adoptionEntryKey(target adoptionTarget, kind, rel string) string {
	return adoptionPrefix + kind + ":" + path.Join(target.root, target.relative, rel)
}

func adoptionSet(hashes map[string]string) (string, error) {
	var keys []string
	for key := range hashes {
		if key != adoptionPrefix+"source" && key != adoptionPrefix+"package_source" &&
			strings.HasPrefix(key, adoptionPrefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	data := appendAdoptionString(nil, "adopt-set-v1")
	for _, key := range keys {
		data = appendAdoptionString(data, key)
		if strings.HasPrefix(key, adoptionPrefix+"tree:") {
			continue
		}
		decoded, err := hex.DecodeString(hashes[key])
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid adoption entry hash")
		}
		data = append(data, decoded...)
	}
	return hashBytes(data), nil
}

func adoptionTreeHash(binding adoptionBinding, set, proof string, tree adoptionTree) (string, error) {
	data := appendAdoptionString(nil, "adopt-tree-v1")
	for _, value := range []string{binding.snapshot.Digest, adoptionSource(binding), set} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid adoption tree binding")
		}
		data = append(data, decoded...)
	}
	data = appendAdoptionString(data, adoptionEntryKey(tree.target, "tree", "."))
	data = appendAdoptionString(data, proof)
	pairs := map[string]string{}
	for _, entry := range tree.entries {
		pairs[adoptionEntryKind(entry)+":"+entry.path] = entry.fingerprint
	}
	if tree.marker != nil {
		pairs["marker:"+tree.marker.path] = tree.marker.fingerprint
	}
	var keys []string
	for key := range pairs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		data = appendAdoptionString(data, key)
		decoded, err := hex.DecodeString(pairs[key])
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid adoption tree fingerprint")
		}
		data = append(data, decoded...)
	}
	return hashBytes(data), nil
}

func encodePackageAdoption(cfg *core.Config, binding adoptionBinding,
	targets []adoptionTarget, plan PackageAdoptionPlan) (map[string]string, error) {
	if err := checkAdoptionTargets(binding, targets); err != nil {
		return nil, err
	}
	if len(plan.Conflicts) != 0 {
		return nil, fmt.Errorf("adoption plan contains conflicts")
	}
	byPath := map[string]adoptionTarget{}
	for _, target := range targets {
		display, err := adoptionDisplay(cfg, target)
		if err != nil {
			return nil, err
		}
		byPath[display] = target
	}
	hashes := map[string]string{}
	var trees []adoptionTree
	var proofs []string
	seen := map[string]bool{}
	for _, copy := range plan.Copies {
		target, found := byPath[copy.Path]
		if !found || seen[copy.Path] {
			return nil, fmt.Errorf("unmatched or duplicate adoption copy")
		}
		seen[copy.Path] = true
		if err := checkAdoptionUnit(PackageAdoptionUnit{Path: target.unit.path, Entries: copy.Entries},
			target.unit.owner); err != nil {
			return nil, err
		}
		matched := false
		for _, proof := range target.unit.proofs {
			if proof.id == copy.Proof && proof.id != "" && matchesAdoptionProof(copy.Entries, proof, target.mount) {
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("adoption copy has no complete bound proof")
		}
		tree := adoptionTree{target: target}
		for _, entry := range copy.Entries {
			if !isAdoptionMode(entry.Mode) {
				return nil, fmt.Errorf("invalid adoption copy mode")
			}
			fingerprint, err := adoptionFingerprint(entry.Mode, hashBytes(entry.Data))
			if err != nil {
				return nil, err
			}
			tree.entries = append(tree.entries, adoptionEntry{entry.Path, entry.Mode, fingerprint})
		}
		if marker := copy.Disabled; marker != nil {
			if marker.Path != ".disabled" || !marker.Mode.IsRegular() || !isAdoptionMode(marker.Mode) ||
				!strings.HasPrefix(target.unit.path, "skills/") {
				return nil, fmt.Errorf("invalid adoption marker")
			}
			fingerprint, err := adoptionFingerprint(marker.Mode, hashBytes(marker.Data))
			if err != nil {
				return nil, err
			}
			tree.marker = &adoptionEntry{marker.Path, marker.Mode, fingerprint}
		}
		if target.unit.owner != binding.snapshot.Name {
			continue
		}
		trees = append(trees, tree)
		proofs = append(proofs, copy.Proof)
		hashes[adoptionEntryKey(target, "tree", ".")] = strings.Repeat("0", 64)
		for _, entry := range tree.entries {
			hashes[adoptionEntryKey(target, adoptionEntryKind(entry), entry.path)] = entry.fingerprint
		}
		if tree.marker != nil {
			hashes[adoptionEntryKey(target, "marker", tree.marker.path)] = tree.marker.fingerprint
		}
	}
	if len(trees) == 0 {
		return hashes, nil
	}
	hashes[adoptionPrefix+"package_source"] = binding.snapshot.Digest
	hashes[adoptionPrefix+"source"] = adoptionSource(binding)
	set, err := adoptionSet(hashes)
	if err != nil {
		return nil, err
	}
	for i, tree := range trees {
		value, err := adoptionTreeHash(binding, set, proofs[i], tree)
		if err != nil {
			return nil, err
		}
		hashes[adoptionEntryKey(tree.target, "tree", ".")] = value
	}
	return hashes, nil
}

func readPackageAdoptionLedger(binding adoptionBinding,
	hashes map[string]string, targets []adoptionTarget) ([]adoptionTree, error) {
	if err := checkAdoptionTargets(binding, targets); err != nil {
		return nil, err
	}
	if !HasPendingAdoption(hashes) {
		return nil, nil
	}
	byPath := map[string]adoptionTarget{}
	for _, target := range targets {
		if target.unit.owner == binding.snapshot.Name {
			byPath[target.root+"/"+target.relative] = target
		}
	}
	values := map[string]map[string]adoptionEntry{}
	treeValues := map[string]string{}
	seen := map[string]bool{}
	for key, value := range hashes {
		rest, pending := strings.CutPrefix(key, adoptionPrefix)
		if !pending {
			continue
		}
		if !isPackageDigest(value) {
			return nil, fmt.Errorf("invalid adoption ledger hash")
		}
		if rest == "source" || rest == "package_source" {
			continue
		}
		kind, rel, found := strings.Cut(rest, ":")
		root, relative, rooted := strings.Cut(rel, "/")
		if !found || !rooted || (root != "groups" && root != "skills") || !isAdoptionPath(relative) ||
			(kind != "tree" && kind != "file" && kind != "dir" && kind != "marker") {
			return nil, fmt.Errorf("invalid adoption ledger key")
		}
		if kind == "tree" {
			if _, found := byPath[rel]; !found {
				return nil, fmt.Errorf("adoption tree is not a bound target")
			}
			treeValues[rel] = value
			continue
		}
		if seen[rel] {
			return nil, fmt.Errorf("duplicate adoption ledger kinds")
		}
		seen[rel] = true
		if values[kind] == nil {
			values[kind] = map[string]adoptionEntry{}
		}
		mode := fs.FileMode(0)
		if kind == "dir" {
			mode = fs.ModeDir
		}
		values[kind][rel] = adoptionEntry{mode: mode, fingerprint: value}
	}
	if len(treeValues) == 0 || hashes[adoptionPrefix+"package_source"] != binding.snapshot.Digest ||
		hashes[adoptionPrefix+"source"] != adoptionSource(binding) {
		return nil, fmt.Errorf("incomplete or changed adoption binding")
	}
	for name := range treeValues {
		for other := range treeValues {
			if name != other && strings.HasPrefix(name, other+"/") {
				return nil, fmt.Errorf("overlapping adoption trees")
			}
		}
	}
	trees := map[string]adoptionTree{}
	for name := range treeValues {
		trees[name] = adoptionTree{target: byPath[name]}
	}
	for kind, entries := range values {
		for name, entry := range entries {
			matched := false
			for treePath, tree := range trees {
				if name != treePath && !strings.HasPrefix(name, treePath+"/") {
					continue
				}
				entry.path = strings.TrimPrefix(strings.TrimPrefix(name, treePath), "/")
				if entry.path == "" {
					entry.path = "."
				}
				if kind == "marker" {
					if entry.path != ".disabled" || !strings.HasPrefix(tree.target.unit.path, "skills/") {
						return nil, fmt.Errorf("invalid adoption ledger marker")
					}
					tree.marker = &entry
				} else {
					if path.Base(entry.path) == ".disabled" {
						return nil, fmt.Errorf("adoption marker is removable")
					}
					tree.entries = append(tree.entries, entry)
				}
				trees[treePath] = tree
				matched = true
			}
			if !matched {
				return nil, fmt.Errorf("orphan adoption entry")
			}
		}
	}
	set, err := adoptionSet(hashes)
	if err != nil {
		return nil, err
	}
	var result []adoptionTree
	for name, tree := range trees {
		matched := false
		for _, proof := range tree.target.unit.proofs {
			if proof.id == "" || proof.hashes != nil && !tree.target.mount ||
				len(proof.unit.Entries) != len(tree.entries) {
				continue
			}
			expected := map[string]PackageAdoptionEntry{}
			for _, entry := range proof.unit.Entries {
				expected[entry.Path] = entry
			}
			recovered := slices.Clone(tree.entries)
			valid := true
			for i, entry := range recovered {
				original, found := expected[entry.path]
				if !found || original.Mode.IsDir() != entry.mode.IsDir() {
					valid = false
					break
				}
				content := hashBytes(original.Data)
				if proof.hashes != nil {
					content = proof.hashes[entry.path]
				}
				count := 0
				for permissions := fs.FileMode(0); permissions <= 0o777; permissions++ {
					mode := permissions | entry.mode.Type()
					if !isAdoptionMode(mode) {
						continue
					}
					fingerprint, err := adoptionFingerprint(mode, content)
					if err != nil {
						return nil, err
					}
					if fingerprint == entry.fingerprint {
						recovered[i].mode = mode
						count++
					}
				}
				if count != 1 {
					valid = false
					break
				}
			}
			value, err := adoptionTreeHash(binding, set, proof.id, tree)
			if err != nil {
				return nil, err
			}
			if valid && value == treeValues[name] {
				tree.entries = recovered
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("adoption tree has no complete original proof")
		}
		slices.SortFunc(tree.entries, func(a, b adoptionEntry) int { return strings.Compare(a.path, b.path) })
		result = append(result, tree)
	}
	slices.SortFunc(result, func(a, b adoptionTree) int {
		return strings.Compare(a.target.root+"/"+a.target.relative, b.target.root+"/"+b.target.relative)
	})
	return result, nil
}

func checkPackageAdoptionTrees(cfg *core.Config, trees []adoptionTree) error {
	for _, tree := range trees {
		var root *os.Root
		var rel string
		var err error
		if tree.target.root == "groups" {
			root, rel, err = openPackageHome(cfg.GroupsDir, tree.target.folder)
			rel = path.Join(rel, strings.TrimPrefix(tree.target.relative, tree.target.folder+"/"))
		} else {
			root, err = openPackageRoot(cfg.ProjectRoot)
			rel = path.Join("skills", tree.target.relative)
		}
		if err != nil {
			return err
		}
		err = checkAdoptionSurvivors(root, rel, tree)
		if err := errors.Join(err, root.Close()); err != nil {
			return err
		}
	}
	return nil
}

func checkAdoptionSurvivors(root *os.Root, rel string, tree adoptionTree) error {
	if err := checkPackageDirs(root, path.Dir(rel)); err != nil {
		return err
	}
	_, err := root.Lstat(rel)
	protected := tree.target.mount && strings.HasPrefix(tree.target.unit.path, "skills/")
	if errors.Is(err, fs.ErrNotExist) && !protected && tree.marker == nil {
		return nil
	}
	resident, err := readPackageAdoptionUnit(root, rel)
	if err != nil {
		return err
	}
	expected := map[string]adoptionEntry{}
	for _, entry := range tree.entries {
		expected[entry.path] = entry
	}
	marker := false
	for _, entry := range resident.Entries {
		want, found := expected[entry.Path]
		if entry.Path == ".disabled" && tree.marker != nil {
			want, found, marker = *tree.marker, true, true
		}
		fingerprint, err := adoptionFingerprint(entry.Mode, hashBytes(entry.Data))
		if err != nil {
			return err
		}
		if !found || !isAdoptionMode(entry.Mode) || fingerprint != want.fingerprint ||
			entry.Path != ".disabled" && entry.Mode != want.mode {
			return fmt.Errorf("adoption survivor differs at %s", filepath.Join(root.Name(), rel, entry.Path))
		}
	}
	if tree.marker != nil && !marker {
		return fmt.Errorf("adoption marker is missing")
	}
	return nil
}
