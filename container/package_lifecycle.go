package container

import (
	"fmt"
	"os"
	"path/filepath"
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
	if err := SelectPackages(dataDir, target, snapshots); err != nil {
		return err
	}
	for i, snapshot := range snapshots {
		receipt := receipts[i]
		if receipt.Manifest == nil {
			receipt.Manifest = map[string][]string{}
		}
		if receipt.AssetHashes == nil {
			receipt.AssetHashes = map[string]string{}
		}
		receipt.Folder, receipt.Name = target, snapshot.Name
		receipt.Manifest[PackageSourceArtifact] = []string{snapshot.Digest}
		receipt.AssetHashes[PackageSourceArtifact] = snapshot.Digest
		receipt.Manifest["skill"] = inventory[i]
		receipt.InstalledAt = time.Now().UTC().Format(time.RFC3339)
		if err := database.PutInstalledPackage(receipt); err != nil {
			return fmt.Errorf("incomplete package operation: %s is active but its receipt failed; retry the same operation: %w",
				receipt.Name, err)
		}
	}
	return nil
}
