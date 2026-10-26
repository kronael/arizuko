package container

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
)

const PackageSourceArtifact = "package_source"

func ReadPackageSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// ActivatePackages requires the instance package lock and borrows database.
// A nil database selects only filesystem state during pre-store creation.
// A receipt failure leaves the selection active for repair by the same operation.
func ActivatePackages(dataDir, target string, database *store.Store,
	snapshots []PackageSnapshot, receipts []store.InstalledPackage) error {
	return activatePackages(dataDir, target, database, snapshots, receipts, nil)
}

func activatePackages(dataDir, target string, database *store.Store,
	snapshots []PackageSnapshot, receipts []store.InstalledPackage, adoption *adoptionBatch) error {
	var adopted []adoptionReceipt
	if adoption != nil {
		if dataDir != adoption.cfg.ProjectRoot {
			return fmt.Errorf("adoption activation roots changed")
		}
		var err error
		adopted, err = readAdoptionBatch(&adoption.cfg, database, *adoption, adoptionActivate)
		if err != nil {
			return err
		}
		matched := false
		for _, scope := range adoption.scopes {
			proposed := scope.selected.System
			if target != "" {
				if target != scope.folder {
					continue
				}
				proposed = scope.selected.Group
			}
			if !isSameAdoptionSelection(proposed, snapshots) {
				return fmt.Errorf("activation selection differs from adoption witness")
			}
			matched = true
		}
		if !matched {
			return fmt.Errorf("activation target lacks an adoption witness")
		}
	} else if database != nil {
		rows, err := database.InstalledPackages()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if HasPendingAdoption(row.AssetHashes) {
				return fmt.Errorf("pending package adoption requires its operation witness")
			}
		}
	}
	if database == nil {
		return SelectPackages(dataDir, target, snapshots)
	}
	if len(snapshots) != len(receipts) {
		return fmt.Errorf("package snapshots and receipts differ in length")
	}
	inventory := make([][]string, len(snapshots))
	for i, snapshot := range snapshots {
		dir, err := ResolvePackage(dataDir, snapshot)
		if err != nil {
			return err
		}
		names, err := ReadPackageSkills(dir)
		if err != nil {
			return err
		}
		inventory[i] = names
	}
	updates := make([]store.InstalledPackage, 0, len(snapshots))
	for i, snapshot := range snapshots {
		receipt := packageActivationReceipt(target, snapshot, receipts[i], inventory[i])
		previous, found, err := database.InstalledPackage(target, snapshot.Name)
		if err != nil {
			return err
		}
		if adoption != nil {
			for _, binding := range adoption.bindings {
				if binding.folder == target && binding.snapshot.Name == snapshot.Name &&
					(binding.snapshot != snapshot || receipt.Source != binding.source || receipt.Revision != binding.revision) {
					return fmt.Errorf("final receipt differs from adoption binding")
				}
			}
			var trees []adoptionTree
			for _, row := range adopted {
				if row.binding.folder != target || row.binding.snapshot != snapshot {
					continue
				}
				trees = row.trees
			}
			receipt = completeAdoptionReceipt(receipt, previous, trees)
		}
		receipt.InstalledAt = previous.InstalledAt
		if found && previous.InstalledAt != "" && reflect.DeepEqual(previous, receipt) {
			continue
		}
		receipt.InstalledAt = time.Now().UTC().Format(time.RFC3339)
		updates = append(updates, receipt)
	}
	if err := SelectPackages(dataDir, target, snapshots); err != nil {
		return err
	}
	for _, receipt := range updates {
		if err := database.PutInstalledPackage(receipt); err != nil {
			return fmt.Errorf("incomplete package operation: %s is active but its receipt failed; retry the same operation: %w",
				receipt.Name, err)
		}
	}
	return nil
}

func packageActivationReceipt(target string, snapshot PackageSnapshot,
	receipt store.InstalledPackage, skills []string) store.InstalledPackage {
	receipt = cloneAdoptionReceipt(receipt)
	receipt.Folder, receipt.Name = target, snapshot.Name
	receipt.Manifest[PackageSourceArtifact] = []string{snapshot.Digest}
	receipt.AssetHashes[PackageSourceArtifact] = snapshot.Digest
	if previous, found := receipt.Manifest["skill"]; !found || !slices.Equal(previous, skills) {
		receipt.Manifest["skill"] = slices.Clone(skills)
	}
	return receipt
}

func completeAdoptionReceipt(receipt, previous store.InstalledPackage,
	trees []adoptionTree) store.InstalledPackage {
	for key, value := range previous.AssetHashes {
		if strings.HasPrefix(key, PendingSettingPrefix) || strings.HasPrefix(key, "pending:settings:") {
			receipt.AssetHashes[key] = value
		}
	}
	maps.DeleteFunc(receipt.AssetHashes, func(key, _ string) bool {
		if strings.HasPrefix(key, adoptionPrefix) {
			return true
		}
		for _, tree := range trees {
			if name, skill := strings.CutPrefix(tree.target.unit.path, "skills/"); skill &&
				(key == "skill:"+name || strings.HasPrefix(key, "file:.claude/skills/"+name+"/")) {
				return true
			}
		}
		return false
	})
	return receipt
}
