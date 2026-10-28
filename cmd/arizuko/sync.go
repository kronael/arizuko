package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

type syncCandidate struct {
	name, source, revision, dir string
	manifest                    container.ProductManifest
	skills, files               []string
	hashes                      map[string]string
}

type syncBinding struct {
	candidate *syncCandidate
	folder    string
	fragments []pkgFragment
	record    routd.InstalledPackage
	order     routd.InstalledPackage
}

func composeSyncDocs(stores map[string]*store.Store, records []routd.InstalledPackage, fragments []pkgFragment) ([]parsedDoc, error) {
	touched := map[string]map[string]bool{}
	add := func(sub, name string) {
		if touched[sub] == nil {
			touched[sub] = map[string]bool{}
		}
		touched[sub][name] = true
	}
	for _, record := range records {
		for name, pks := range record.Manifest {
			if r := resreg.Lookup(name); r != nil && len(pks) > 0 {
				add(r.DB, name)
			}
		}
	}
	for _, f := range fragments {
		for name := range f.rows {
			add(f.subsystem, name)
		}
	}
	var docs []parsedDoc
	for _, sub := range slices.Sorted(maps.Keys(touched)) {
		st := stores[sub]
		if st == nil {
			return nil, fmt.Errorf("no store for subsystem %s", sub)
		}
		live, checksum, _, err := resreg.ExportSnapshot(context.Background(), resreg.On(st.Obj()), sub)
		if err != nil {
			return nil, err
		}
		doc := parsedDoc{subsystem: sub, checksum: checksum, manifest: map[string]any{}}
		for name := range touched[sub] {
			r := resreg.Lookup(name)
			rows := live[name]
			doc.pruneScopes = append(doc.pruneScopes, r.Scopes(rows)...)
			for _, record := range records {
				rows, err = r.DropRows(rows, record.Manifest[name])
				if err != nil {
					return nil, err
				}
			}
			for _, f := range fragments {
				if f.subsystem != sub {
					continue
				}
				overlay, ok := f.rows[name]
				if !ok {
					continue
				}
				rows, err = r.MergeRows(rows, overlay)
				if err != nil {
					return nil, err
				}
			}
			doc.manifest[name] = rows
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

const packageUsage = "arizuko package [-i instance] sync | add <source> | rm <name> [-g|--group folder] [-f|--force]"

// cmdPackage installs what the installed_packages rows select. add and rm
// change one selection first; sync changes none. Nothing else selects a package.
func cmdPackage(args []string) {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace an unmanaged destination")
	fs.BoolVar(force, "f", false, "replace an unmanaged destination")
	scope := fs.String("group", "", "group folder; none selects for the instance")
	fs.StringVar(scope, "g", "", "group folder; none selects for the instance")
	err := flexParse(fs, args[1:])
	op, target := fs.Arg(0), fs.Arg(1)
	if want, ok := map[string]int{"sync": 1, "add": 2, "rm": 2}[op]; err != nil || !ok || fs.NArg() != want || (op == "sync" && *scope != "") {
		usage(err, packageUsage)
	}
	if op == "rm" && *scope == routd.InstanceWide && target == container.CorePackageName {
		die("Failed: %s is required and cannot be withdrawn", target)
	}
	dataDir := mustInstanceDir(args[0])
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		die("Failed: open stores: %v", err)
	}
	defer closeStores(stores)
	rdb := routd.OpenOver(stores[resreg.SubsystemRoutd].Obj())
	records, err := rdb.InstalledPackages()
	if err != nil {
		die("Failed: list packages: %v", err)
	}
	selections := map[string][]routd.InstalledPackage{routd.InstanceWide: nil}
	for folder := range rdb.AllGroups() {
		if folder == "." || !groupfolder.IsValidFolder(folder) {
			die("Failed: invalid folder %q", folder)
		}
		selections[folder] = nil
	}
	if _, ok := selections[*scope]; !ok {
		die("Failed: unknown group %q", *scope)
	}
	withdrawn := false
	for _, record := range slices.SortedStableFunc(slices.Values(records), routd.SelectionOrder) {
		_, live := selections[record.Folder]
		switch {
		case op == "rm" && record.Folder == *scope && record.Name == target:
			withdrawn = true
		case live:
			selections[record.Folder] = append(selections[record.Folder], record)
		}
	}
	if op == "rm" && !withdrawn {
		die("Failed: %s is not selected for %q", target, *scope)
	}
	if instance := selections[routd.InstanceWide]; len(instance) == 0 || instance[0].Name != container.CorePackageName {
		selections[routd.InstanceWide] = append([]routd.InstalledPackage{{Source: "ant"}}, instance...)
	}
	if op == "add" {
		selections[*scope] = append(selections[*scope], routd.InstalledPackage{Source: target, InstalledAt: core.TS(time.Now())})
	}
	candidates := map[string]*syncCandidate{}
	names := map[string]string{}
	var ordered []*syncCandidate
	var bindings []syncBinding
	folders := slices.Sorted(maps.Keys(selections))
	for _, folder := range folders {
		seen := map[string]bool{}
		for _, entry := range selections[folder] {
			if entry.Source == "" || (entry.Source == "ant" && (folder != "" || seen[container.CorePackageName])) {
				die("Failed: invalid package source %q", entry.Source)
			}
			candidate := candidates[entry.Source]
			if candidate == nil {
				dir, revision, cleanup := filepath.Join(cfg.EffectiveAppSrcDir(), "ant"), "release", func() {}
				if entry.Source != "ant" {
					dir, revision, cleanup = resolveProductSource(cfg, entry.Source)
				}
				defer cleanup()
				m, err := container.ReadProductManifest(dir)
				if err != nil {
					die("Failed: read %s: %v", entry.Source, err)
				}
				if !pkgNameRE.MatchString(m.Name) || (entry.Source == "ant") != (m.Name == container.CorePackageName) {
					die("Failed: invalid package name %q", m.Name)
				}
				if source, ok := names[m.Name]; ok && source != entry.Source {
					die("Failed: package %s already uses source %s", m.Name, source)
				}
				candidate = &syncCandidate{name: m.Name, source: entry.Source, revision: revision, dir: dir, manifest: m}
				candidate.files, candidate.hashes = readFragmentAssets(dir, m.Name)
				candidates[entry.Source] = candidate
				names[m.Name] = entry.Source
				ordered = append(ordered, candidate)
			}
			if seen[candidate.name] || (entry.Name != "" && entry.Name != candidate.name) {
				die("Failed: package %s in %q conflicts with selection %q", candidate.name, folder, entry.Name)
			}
			seen[candidate.name] = true
			binding := syncBinding{candidate: candidate, folder: folder, record: entry,
				order: routd.InstalledPackage{Folder: folder, Name: candidate.name, InstalledAt: entry.InstalledAt}}
			held := routd.InstalledPackage{AssetHashes: map[string]string{}}
			for _, record := range records {
				if record.Name == candidate.name {
					maps.Copy(held.AssetHashes, record.AssetHashes)
				}
			}
			svcDir := filepath.Join(dataDir, "services")
			if dirty := dirtyAssets(svcDir, held); len(dirty) > 0 {
				die("Failed: locally edited service assets: %s", strings.Join(dirty, ", "))
			}
			if err := checkSourcedFiles(svcDir, candidate.name, candidate.dir, candidate.files, held.AssetHashes, strings.HasPrefix(candidate.source, "catalog:"), *force); err != nil {
				die("Failed: %v", err)
			}
			bundled := ""
			if strings.HasPrefix(candidate.source, "catalog:") {
				bundled = candidate.name
			}
			binding.fragments = parsePkgManifests(candidate.name, candidate.manifest, bundled)
			bindings = append(bindings, binding)
		}
	}
	mixes, managed := map[string][]container.Product{}, map[string][]string{}
	for _, b := range bindings {
		mixes[b.folder] = append(mixes[b.folder], container.Product{Name: b.candidate.name, Dir: b.candidate.dir})
	}
	for _, record := range records {
		managed[record.Folder] = append(managed[record.Folder], record.Manifest[container.ManifestGroupFiles]...)
	}
	for _, folder := range folders[1:] {
		paths, err := container.UnmanagedPaths(filepath.Join(cfg.GroupsDir, folder), mixes[folder], managed[folder])
		if err != nil {
			die("Failed: %s: %v", folder, err)
		}
		if len(paths) > 0 && !*force {
			die("Failed: %s: files no package manages (-f replaces them): %s", folder, strings.Join(paths, ", "))
		}
	}
	var fragments []pkgFragment
	added := map[string][]string{}
	for i := range bindings {
		b := &bindings[i]
		b.record = routd.InstalledPackage{InstalledAt: b.record.InstalledAt, Manifest: map[string][]string{manifestFiles: b.record.Manifest[manifestFiles]}}
		for _, f := range b.fragments {
			fragments = append(fragments, f)
			for name, rows := range f.rows {
				pks, err := resreg.Lookup(name).PKs(rows)
				if err != nil {
					die("Failed: %v", err)
				}
				b.record.Manifest[name] = append(b.record.Manifest[name], pks...)
				added[name] = append(added[name], pks...)
			}
		}
	}
	docs, err := composeSyncDocs(stores, records, fragments)
	if err != nil {
		die("Failed: compose: %v", err)
	}
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		die("Failed: invalid folders: %v", bad)
	}
	for _, b := range bindings {
		if len(b.candidate.files) > 0 {
			c := b.candidate
			provisionPkgEnv(dataDir, filepath.Join(c.dir, container.ManifestFile), c.name)
		}
	}
	for _, candidate := range ordered {
		_, candidate.skills, err = container.InstallPackage(dataDir, candidate.dir, candidate.source != "ant")
		if err != nil {
			die("Failed: build %s: %v", candidate.source, err)
		}
	}
	var instance []string
	for _, folder := range folders {
		var scoped []syncBinding
		for _, b := range bindings {
			if b.folder == "" || b.folder == folder {
				scoped = append(scoped, b)
			}
		}
		slices.SortStableFunc(scoped, func(a, b syncBinding) int { return routd.SelectionOrder(a.order, b.order) })
		var names []string
		for _, b := range scoped {
			names = append(names, b.candidate.name)
		}
		inventory, err := container.ReadPackageInventory(dataDir, names)
		if err != nil {
			die("Failed: %v", err)
		}
		for _, line := range inventory.Overrides {
			if folder == "" {
				instance = append(instance, line)
			} else if slices.Contains(instance, line) {
				continue
			}
			fmt.Printf("%s: %s\n", cmp.Or(folder, "instance"), line)
		}
	}
	removed := map[string][]string{}
	var packages []string
	for _, record := range records {
		for name, pks := range record.Manifest {
			if resreg.Lookup(name) != nil {
				removed[name] = append(removed[name], pks...)
			}
		}
	}
	note := func(name string) {
		if name != container.CorePackageName && !slices.Contains(packages, name) {
			packages = append(packages, name)
		}
	}
	for _, b := range bindings {
		note(b.candidate.name)
	}
	for _, record := range records {
		note(record.Name)
	}
	extra := map[string]any{"packages": packages, "added": added, "removed": removed}
	digest := ""
	if len(packages) == 1 {
		extra["package"] = packages[0]
		if candidate := candidates[names[packages[0]]]; candidate != nil {
			digest = candidate.hashes["file:"+container.ManifestFile]
		}
	}
	if _, err := applyDocs(context.Background(), stores, docs, applyRun{actor: cliActor(), digest: digest, extra: extra}); err != nil {
		die("Failed: apply: %v", err)
	}
	for i := range bindings {
		b := &bindings[i]
		c := b.candidate
		b.record.Folder, b.record.Name, b.record.Source, b.record.Revision = b.folder, c.name, c.source, c.revision
		b.record.Manifest[manifestSkills] = c.skills
		b.record.InstalledAt = cmp.Or(b.record.InstalledAt, core.TS(time.Now()))
		svcDir := filepath.Join(dataDir, "services")
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			die("Failed: %v", err)
		}
		for _, file := range c.files {
			data, err := os.ReadFile(filepath.Join(c.dir, file))
			if err != nil {
				die("Failed: %v", err)
			}
			if err := writeFileAtomic(filepath.Join(svcDir, file), data); err != nil {
				die("Failed: %v", err)
			}
		}
		for _, file := range b.record.Manifest[manifestFiles] {
			if !slices.Contains(c.files, file) {
				if err := os.Remove(filepath.Join(svcDir, file)); err != nil && !os.IsNotExist(err) {
					die("Failed: %v", err)
				}
			}
		}
		b.record.Manifest[manifestFiles] = c.files
		b.record.AssetHashes = c.hashes
		applied := map[string][]string{}
		for _, f := range b.fragments {
			for name := range f.rows {
				applied[name] = b.record.Manifest[name]
			}
		}
		reportPackageInstall(c.name, c.revision, c.files, applied)
	}
	for _, folder := range folders[1:] {
		blended, overrides, err := container.ComposeGroup(cfg, folder, mixes[folder], managed[folder])
		if err != nil {
			die("Failed: blend %s: %v", folder, err)
		}
		for _, line := range overrides {
			fmt.Printf("%s: %s\n", folder, line)
		}
		for _, blend := range blended {
			i := slices.IndexFunc(bindings, func(b syncBinding) bool { return b.folder == folder && b.candidate.name == blend.Name })
			maps.Copy(bindings[i].record.Manifest, blend.Manifest)
		}
	}
	for _, b := range bindings {
		if err := rdb.PutInstalledPackage(b.record); err != nil {
			die("Failed: record %s: %v", b.candidate.name, err)
		}
	}
	for _, record := range records {
		if _, ok := selections[record.Folder]; !ok || slices.ContainsFunc(bindings, func(b syncBinding) bool { return b.folder == record.Folder && b.candidate.name == record.Name }) {
			continue
		}
		for _, file := range record.Manifest[manifestFiles] {
			if slices.ContainsFunc(bindings, func(b syncBinding) bool { return slices.Contains(b.candidate.files, file) }) {
				continue
			}
			if err := os.Remove(filepath.Join(dataDir, "services", file)); err != nil && !os.IsNotExist(err) {
				die("Failed: %v", err)
			}
		}
		if _, err := rdb.DeleteInstalledPackage(record.Folder, record.Name); err != nil {
			die("Failed: %v", err)
		}
	}
	generateCompose(dataDir)
	fmt.Println("synced")
}
