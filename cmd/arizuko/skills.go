package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// syncPackageSkills layers every installed package's skills into every EXISTING
// group. It is the delivery half of `arizuko generate` for the skills asset
// kind, the same role syncProxydRoutes plays for routes.
//
// Without it a package's skill reached a group only if the package was already
// installed when that group was created: container.SetupGroup is the sole
// non-test caller of the seeder and only group creation reaches it, while
// `packages install/upgrade/sync` all printed "run `arizuko generate` to apply"
// — which was true of fragments and rows and false of skills (BUGS L47).
//
// Fatal on failure, like the route half. A generate that reports success while
// a group keeps running last month's skill is the failure this replaces.
func syncPackageSkills(dataDir string) {
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	if !dirExists(container.PackageSkillsDir(cfg)) {
		return // no package on this instance ships a skill
	}
	rdb := mustOpenRoutd()
	defer func() { _ = rdb.Close() }()

	groups := store.NewOver(rdb.Obj()).AllGroups()
	folders := make([]string, 0, len(groups))
	for folder := range groups {
		folders = append(folders, folder)
	}
	sort.Strings(folders)

	var seeded, touched int
	for _, folder := range folders {
		n, err := container.SeedPackageSkills(cfg, folder)
		if err != nil {
			die("Failed: seed package skills into %s: %v", folder, err)
		}
		if n > 0 {
			seeded += n
			touched++
		}
	}
	// routd.Open leaves -wal/-shm beside routd.db, and generate usually runs as
	// root while the daemons run as uid 1000.
	chownStore(filepath.Join(dataDir, "store"))
	fmt.Printf("package skills: %d seeded across %d group(s)\n", seeded, touched)
}
