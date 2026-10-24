package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/store"
)

type renameInventory struct {
	Instance           string                               `json:"instance"`
	Endpoint           string                               `json:"endpoint"`
	ConfiguredEndpoint string                               `json:"configured_endpoint"`
	CASHA256           string                               `json:"ca_sha256"`
	KeyIDSHA256        map[string]string                    `json:"key_ids_sha256"`
	Owners             map[string]map[string]map[string]int `json:"owners"`
}

type renameRow struct {
	Owner, Entity string
	Before, After objstore.Item
}
type renamePlan struct {
	Instance, DataDir string
	Inventory         renameInventory
	Mapping           renameMap
	Rows              []renameRow
	Files             []renameFile
	Accounts          map[string]string `json:",omitempty"`
}

type renameList []string

func (list *renameList) String() string         { return strings.Join(*list, ",") }
func (list *renameList) Set(value string) error { *list = append(*list, value); return nil }

func cmdGroupRename(instance, dataDir string, args []string) {
	fs := flag.NewFlagSet("group rename", flag.ContinueOnError)
	var exceptions renameList
	fs.Var(&exceptions, "map", "repeat old=new for a nested folder mapping")
	inventoryPath := fs.String("inventory", "", "private, complete quiesced owner partition counts JSON")
	endpoint := fs.String("endpoint", "", "operator endpoint; must match inventory endpoint (owner env credentials)")
	journal := fs.String("journal", "", "private operation directory (required for writes)")
	dryRun := fs.Bool("dry-run", false, "preflight and print counts without writing")
	stopped := fs.Bool("stopped", false, "assert all instance writers, guests and writable mounts are stopped")
	yes := fs.Bool("yes", false, "execute the operation without a prompt")
	resume := fs.Bool("resume", false, "finish the operation recorded in --journal")
	rollback := fs.Bool("rollback", false, "reverse the operation recorded in --journal while stopped")
	if err := flexParse(fs, args); err != nil {
		die("Failed: %v", err)
	}
	if fs.NArg() != 2 || *inventoryPath == "" || *endpoint == "" || (*resume && *rollback) {
		die("usage: arizuko group <instance> rename <src> <dst> --inventory <file> --endpoint <url> [--map old=new] --dry-run | --stopped --yes --journal <dir> [--resume|--rollback]")
	}
	if !*dryRun && (!*stopped || !*yes || *journal == "") {
		die("Failed: execution requires --stopped --yes --journal; keep all writers stopped until verification")
	}
	mapping, err := newRenameMap(fs.Arg(0), fs.Arg(1), exceptions)
	if err != nil {
		die("Failed: %v", err)
	}
	var inventory renameInventory
	if err = renameReadJSON(*inventoryPath, &inventory); err != nil {
		die("Failed: inventory: %v", err)
	}
	drivers, err := renameOpenOwners(instance, dataDir, *endpoint, inventory)
	if err != nil {
		die("Failed: target preflight: %v", err)
	}
	ctx := context.Background()
	var plan renamePlan
	if *resume || *rollback {
		err = renameReadJSON(filepath.Join(*journal, "plan.json"), &plan)
		if err == nil && (plan.Instance != instance || plan.DataDir != dataDir ||
			!reflect.DeepEqual(plan.Inventory, inventory) || !reflect.DeepEqual(plan.Mapping, mapping)) {
			err = fmt.Errorf("journal target, inventory or original mapping differs from this command")
		}
	} else {
		plan, err = renamePreflight(ctx, instance, dataDir, mapping, inventory, drivers)
	}
	if err != nil {
		die("Failed: rename preflight: %v", err)
	}
	fmt.Printf("rename: %d items, %d filesystem operations\n", len(plan.Rows), len(plan.Files))
	if *dryRun {
		return
	}
	if err = renameExecute(ctx, *journal, plan, drivers, *resume, *rollback); err != nil {
		die("Failed: %v; keep the instance stopped and resume or roll back using %s", err, *journal)
	}
	fmt.Println("rename complete; keep writers stopped through verification")
}

func renameReadJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s must be a regular private file with mode 0600", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close() //nolint:errcheck
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected JSON after document")
	}
	return nil
}

func renameDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func renameOpenOwners(instance, dataDir, endpoint string, inventory renameInventory) (map[string]objstore.Store, error) {
	if inventory.Instance != instance || inventory.Endpoint != endpoint || inventory.ConfiguredEndpoint == "" || inventory.CASHA256 == "" {
		return nil, fmt.Errorf("inventory must bind instance, configured endpoint, operator endpoint and CA digest")
	}
	for _, owner := range []string{"routd", "runed", "authd", "onbod"} {
		if _, ok := inventory.Owners[owner]; !ok {
			return nil, fmt.Errorf("inventory is missing owner %s", owner)
		}
	}
	drivers := map[string]objstore.Store{}
	for _, owner := range slices.Sorted(maps.Keys(inventory.Owners)) {
		entities, err := renameEntities(owner)
		if err != nil {
			return nil, err
		}
		for _, entity := range entities {
			if _, ok := inventory.Owners[owner][entity]; !ok {
				return nil, fmt.Errorf("inventory is missing %s.%s", owner, entity)
			}
		}
		for entity := range inventory.Owners[owner] {
			if !slices.Contains(entities, entity) {
				return nil, fmt.Errorf("unclassified table %s.%s", owner, entity)
			}
		}
		raw, err := os.ReadFile(filepath.Join(dataDir, "env", owner+".env"))
		if err != nil {
			return nil, err
		}
		env := map[string]string{}
		for _, line := range strings.Split(string(raw), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				env[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"'")
			}
		}
		ca, err := base64.StdEncoding.DecodeString(env[dynstore.EnvCA])
		if err != nil {
			return nil, fmt.Errorf("%s STORE_CA: %w", owner, err)
		}
		matches := env["ARIZUKO_INSTANCE"] == instance && env[dynstore.EnvURL] == inventory.ConfiguredEndpoint &&
			renameDigest(ca) == inventory.CASHA256 && renameDigest([]byte(env[dynstore.EnvKeyID])) == inventory.KeyIDSHA256[owner]
		if !matches {
			return nil, fmt.Errorf("%s env identity, endpoint, CA or credential ID differs from inventory", owner)
		}
		driver, err := dynstore.Dial(dynstore.Config{Endpoint: endpoint, Region: env[dynstore.EnvRegion], Prefix: dynstore.Prefix(owner), KeyID: env[dynstore.EnvKeyID], Secret: env[dynstore.EnvSecret], CA: ca})
		if err != nil {
			return nil, err
		}
		drivers[owner] = driver
	}
	return drivers, nil
}

func renamePreflight(ctx context.Context, instance, dataDir string, mapping renameMap, inventory renameInventory, drivers map[string]objstore.Store) (renamePlan, error) {
	plan := renamePlan{Instance: instance, DataDir: dataDir, Mapping: mapping, Inventory: inventory}
	sourceGroups := 0
	groupFolders := []string{}
	aclScopes := [][2]string{}
	markers := map[string]objstore.Item{}
	for _, owner := range slices.Sorted(maps.Keys(inventory.Owners)) {
		driver := drivers[owner]
		if driver == nil {
			return plan, fmt.Errorf("missing driver for %s", owner)
		}
		entities, err := renameEntities(owner)
		if err != nil {
			return plan, err
		}
		for entity := range inventory.Owners[owner] {
			if !slices.Contains(entities, entity) {
				return plan, fmt.Errorf("unclassified table %s.%s", owner, entity)
			}
		}
		for _, entity := range entities {
			partitions, ok := inventory.Owners[owner][entity]
			if !ok {
				return plan, fmt.Errorf("missing inventory table %s.%s", owner, entity)
			}
			images := map[objstore.Key]objstore.Item{}
			readPartitions := map[string]bool{}
			for _, partition := range slices.Sorted(maps.Keys(partitions)) {
				count := partitions[partition]
				if partition == "" || count < 0 {
					return plan, fmt.Errorf("invalid partition/count in %s.%s", owner, entity)
				}
				query := objstore.Query{Entity: entity, Partition: partition}
				if entity == audit.Entity || entity == store.SecretUseLog {
					query.Project = []string{"__rename_count"}
				}
				items, err := driver.Query(ctx, query)
				if err != nil {
					return plan, fmt.Errorf("read %s.%s: %w", owner, entity, err)
				}
				if len(items) != count {
					return plan, fmt.Errorf("%s.%s partition count changed: got %d, inventory %d", owner, entity, len(items), count)
				}
				readPartitions[partition] = true
				for _, item := range items {
					if entity == audit.Entity || entity == store.SecretUseLog {
						continue
					}
					images[item.Key] = item
				}
			}
			if entity == audit.Entity || entity == store.SecretUseLog {
				continue
			}
			if entity == store.ScopeEntity {
				for _, image := range images {
					if _, held := image.Attrs["holder"]; held {
						return plan, fmt.Errorf("%s replace is in flight; settle it before rename", owner)
					}
					if image.Key == store.OwnerScope(owner).Key {
						markers[owner] = image
					}
				}
			}
			keys := slices.SortedFunc(maps.Keys(images), func(a, b objstore.Key) int {
				if n := strings.Compare(a.Partition, b.Partition); n != 0 {
					return n
				}
				return strings.Compare(a.Sort, b.Sort)
			})
			destinations := map[objstore.Key]bool{}
			for _, key := range keys {
				before := images[key]
				after, err := mapping.row(owner, entity, before, dataDir)
				if err != nil {
					return plan, fmt.Errorf("%s.%s: %w", owner, entity, err)
				}

				switch entity {
				case store.Groups:
					groupFolders = append(groupFolders, before.Key.Sort)
				case store.ACL:
					aclScopes = append(aclScopes, [2]string{strings.Split(before.Key.Sort, store.KeySep)[2], strings.Split(after.Key.Sort, store.KeySep)[2]})
				}
				if destinations[after.Key] {
					return plan, fmt.Errorf("mapping collides in %s.%s", owner, entity)
				}
				destinations[after.Key] = true
				if reflect.DeepEqual(before, after) {
					continue
				}
				if entity == store.Groups {
					sourceGroups++
				}
				if before.Key != after.Key {
					if !readPartitions[after.Key.Partition] {
						items, err := driver.Query(ctx, objstore.Query{Entity: entity, Partition: after.Key.Partition})
						if err != nil {
							return plan, err
						}
						if len(items) > 0 {
							return plan, fmt.Errorf("destination partition exists outside inventory in %s.%s", owner, entity)
						}
						readPartitions[after.Key.Partition] = true
					}
					if _, exists := images[after.Key]; exists {
						return plan, fmt.Errorf("destination exists in %s.%s", owner, entity)
					}
				}
				plan.Rows = append(plan.Rows, renameRow{owner, entity, before, after})
			}
		}
	}
	if sourceGroups == 0 {
		return plan, fmt.Errorf("source subtree contains no registered groups")
	}

	for _, scope := range aclScopes {
		for _, folder := range groupFolders {
			nextFolder := mapping.folder(folder)
			if auth.MatchGroups([]string{scope[0]}, folder) != auth.MatchGroups([]string{scope[1]}, nextFolder) {
				return plan, fmt.Errorf("ACL scope %q -> %q changes authorization for %q -> %q", scope[0], scope[1], folder, nextFolder)
			}
		}
	}
	for _, row := range plan.Rows {
		if _, ok := store.EntityScope(row.Entity); ok && markers[row.Owner].Attrs["version"] == "" {
			return plan, fmt.Errorf("owner %s has no cold version marker", row.Owner)
		}
	}
	files, err := renamePlanFiles(dataDir, mapping)
	plan.Files = files
	return plan, err
}

func renameRowState(ctx context.Context, driver objstore.Store, entity string, item objstore.Item) (bool, bool, error) {
	if item.Key == (objstore.Key{}) {
		return false, true, nil
	}
	items, err := driver.Query(ctx, objstore.Query{Entity: entity, Partition: item.Key.Partition, Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: item.Key.Sort}})
	if err != nil {
		return false, false, err
	}
	if len(items) == 0 {
		return false, false, nil
	}
	return true, reflect.DeepEqual(items[0], item), nil
}

func renameBatchState(ctx context.Context, rows []renameRow, drivers map[string]objstore.Store) (bool, bool, error) {
	oldState, newState := true, true
	for _, row := range rows {
		driver := drivers[row.Owner]
		present, old, err := renameRowState(ctx, driver, row.Entity, row.Before)
		if err != nil {
			return false, false, err
		}
		if row.Before.Key == row.After.Key {
			_, next, err := renameRowState(ctx, driver, row.Entity, row.After)
			if err != nil {
				return false, false, err
			}
			oldState = oldState && old
			newState = newState && next
		} else {
			nextPresent, next, err := renameRowState(ctx, driver, row.Entity, row.After)
			if err != nil {
				return false, false, err
			}
			oldState = oldState && old && !nextPresent
			newState = newState && next && !present
		}
	}
	return oldState, newState, nil
}

func renameApplyBatch(ctx context.Context, rows []renameRow, drivers map[string]objstore.Store, rollback bool) error {
	writes := []objstore.Write{}
	var scope objstore.Scope
	bump := false
	for _, row := range rows {
		before, after := row.Before, row.After
		if rollback {
			before, after = after, before
		}
		cond := objstore.Cond{Item: objstore.ItemPresent}
		for _, field := range slices.Sorted(maps.Keys(before.Attrs)) {
			cond.Where = append(cond.Where, objstore.Cmp{Attr: field, Op: objstore.Eq, Value: before.Attrs[field]})
		}
		if before.Key == (objstore.Key{}) {
			writes = append(writes, objstore.Write{Op: objstore.Put,
				Entity: row.Entity, Key: after.Key, Attrs: after.Attrs,
				Cond: objstore.Cond{Item: objstore.ItemAbsent}})
		} else if after.Key == (objstore.Key{}) {
			writes = append(writes, objstore.Write{Op: objstore.Delete,
				Entity: row.Entity, Key: before.Key, Cond: cond})
		} else if before.Key != after.Key {
			writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: row.Entity, Key: before.Key, Cond: cond},
				objstore.Write{Op: objstore.Put, Entity: row.Entity, Key: after.Key, Attrs: after.Attrs, Cond: objstore.Cond{Item: objstore.ItemAbsent}})
		} else {
			remove := []string{}
			for field := range before.Attrs {
				if _, ok := after.Attrs[field]; !ok {
					remove = append(remove, field)
				}
			}
			writes = append(writes, objstore.Write{Op: objstore.Update, Entity: row.Entity, Key: before.Key, Attrs: after.Attrs, Remove: remove, Cond: cond})
		}
		if marked, ok := store.EntityScope(row.Entity); ok {
			scope = marked
			bump = true
		}
	}
	if bump {
		writes = append(writes, objstore.Bump(scope))
	}
	committed, err := drivers[rows[0].Owner].GuardedBatch(ctx, nil, writes)
	if err != nil {
		return err
	}
	if !committed {
		return fmt.Errorf("rename batch refused: state changed")
	}
	return nil
}

type renameStep struct {
	Rows []renameRow
	File *renameFile
}
type renameProgress struct {
	Step          int
	Reverse, Done bool
}

func renameSteps(plan renamePlan) []renameStep {
	steps := []renameStep{}
	for offset := 0; offset < len(plan.Rows); {
		end := offset
		bytes := 0
		for end < len(plan.Rows) && end-offset < 40 && plan.Rows[end].Owner == plan.Rows[offset].Owner {
			image, _ := json.Marshal(plan.Rows[end])
			if end > offset && bytes+len(image) > 1<<20 {
				break
			}
			bytes += len(image)
			end++
		}
		steps = append(steps, renameStep{Rows: plan.Rows[offset:end]})
		offset = end
	}
	for i := range plan.Files {
		steps = append(steps, renameStep{File: &plan.Files[i]})
	}
	return steps
}

func renameExecute(ctx context.Context, journal string, plan renamePlan, drivers map[string]objstore.Store, resume, rollback bool) error {
	if plan.DataDir != "" {
		absolute, err := filepath.Abs(journal)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if errors.Is(err, os.ErrNotExist) {
			parent, name := filepath.Split(absolute)
			parent, err = filepath.EvalSymlinks(parent)
			if err != nil {
				return err
			}
			resolved = filepath.Join(parent, name)
		} else if err != nil {
			return err
		}
		root, err := filepath.Abs(plan.DataDir)
		if err != nil {
			return err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("journal must stay outside the instance data directory")
		}
	}
	if !resume && !rollback {
		if err := os.Mkdir(journal, 0o700); err != nil {
			return fmt.Errorf("create a new journal directory: %w", err)
		}
		if err := renameSyncDir(filepath.Dir(journal)); err != nil {
			return err
		}
		data, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		if err = renameDurableFile(filepath.Join(journal, "plan.json"), data, 0o600); err != nil {
			return err
		}
	}
	info, err := os.Stat(journal)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("journal directory must have mode 0700")
	}
	lock, err := os.OpenFile(filepath.Join(journal, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("journal is in use: %w", err)
	}
	progressPath := filepath.Join(journal, "progress.jl")
	progress, err := os.OpenFile(progressPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer progress.Close() //nolint:errcheck
	if err := renameSyncDir(journal); err != nil {
		return err
	}
	done := map[int]bool{}
	pending := map[int]renameProgress{}
	reader := bufio.NewReader(progress)
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			if len(line) > 0 {
				if err = progress.Truncate(offset); err != nil {
					return err
				}
				if err = progress.Sync(); err != nil {
					return err
				}
			}
			break
		}
		if readErr != nil {
			return readErr
		}
		var record renameProgress
		if err = json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("journal progress is invalid: %w", err)
		}
		offset += int64(len(line))
		if record.Done {
			done[record.Step] = !record.Reverse
			delete(pending, record.Step)
		} else {
			pending[record.Step] = record
		}
	}
	steps := renameSteps(plan)
	if len(pending) > 1 {
		return fmt.Errorf("journal carries multiple uncertain steps")
	}
	for i, record := range pending {
		if i < 0 || i >= len(steps) {
			return fmt.Errorf("invalid journal step")
		}
		old, next, err := renameStepState(ctx, steps[i], drivers)
		if err != nil {
			return err
		}
		if !old && !next {
			return fmt.Errorf("uncertain step %d differs from both journal images", i)
		}
		if steps[i].File != nil && ((old && !record.Reverse) || (next && record.Reverse)) {
			if err = renameDiscardStaging(*steps[i].File, record.Reverse); err != nil {
				return err
			}
		}
		done[i] = next
		record.Done = true
		record.Reverse = old
		if steps[i].File != nil && ((next && steps[i].File.Operation == "mkdir") || (old && steps[i].File.Operation == "rmdir")) {
			if err = renameRestoreDirectory(steps[i].File.Source, *steps[i].File); err != nil {
				return err
			}
		}
		if err = renameRecord(progress, record); err != nil {
			return err
		}
	}
	order := make([]int, len(steps))
	for i := range order {
		order[i] = i
	}
	if rollback {
		slices.Reverse(order)
	}
	for _, i := range order {
		if done[i] == !rollback {
			continue
		}
		if steps[i].File != nil {
			old, next, err := renameFileState(*steps[i].File)
			if err != nil {
				return err
			}
			if (!rollback && !old) || (rollback && !next) {
				return fmt.Errorf("step %d changed since preflight; no write", i)
			}
		}
		record := renameProgress{Step: i, Reverse: rollback}
		if err = renameRecord(progress, record); err != nil {
			return err
		}
		if steps[i].File != nil {
			err = renameApplyFile(*steps[i].File, rollback)
		} else {
			err = renameApplyBatch(ctx, steps[i].Rows, drivers, rollback)
		}
		if err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		record.Done = true
		if err = renameRecord(progress, record); err != nil {
			return err
		}
	}
	for _, operation := range plan.Files {
		if (!rollback && operation.Operation == "mkdir") || (rollback && operation.Operation == "rmdir") {
			if err := renameRestoreDirectory(operation.Source, operation); err != nil {
				return err
			}
			if err := renameSyncDir(operation.Source); err != nil {
				return err
			}
		}
	}
	for _, operation := range plan.Files {
		if operation.Operation == "edit" || operation.Operation == "symlink" || operation.Operation == "move" && !operation.Edited {
			if rollback && (operation.Operation == "edit" || operation.Operation == "symlink") && operation.Original != "" {
				operation.Source = operation.Original
			}
			old, next, err := renameFileState(operation)
			if err != nil {
				return err
			}
			if (!rollback && !next) || (rollback && !old) {
				return fmt.Errorf("final filesystem image changed at %s", operation.Source)
			}
		}
	}
	if err := renameVerifyRows(ctx, plan.Rows, drivers, rollback); err != nil {
		return err
	}
	return nil
}

func renameStepState(ctx context.Context, step renameStep, drivers map[string]objstore.Store) (bool, bool, error) {
	if step.File != nil {
		return renameFileState(*step.File)
	}
	return renameBatchState(ctx, step.Rows, drivers)
}

func renameRecord(file *os.File, record renameProgress) error {
	if err := json.NewEncoder(file).Encode(record); err != nil {
		return err
	}
	return file.Sync()
}

func renameDurableFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	if err = file.Sync(); err != nil {
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	if err = file.Close(); err != nil {
		return err
	}
	return renameSyncDir(filepath.Dir(path))
}

func renameSyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func renameVerifyRows(ctx context.Context, rows []renameRow, drivers map[string]objstore.Store, rollback bool) error {
	caches := map[[3]string]map[objstore.Key]objstore.Item{}
	read := func(owner, entity, partition string) (map[objstore.Key]objstore.Item, error) {
		key := [3]string{owner, entity, partition}
		if items, ok := caches[key]; ok {
			return items, nil
		}
		result, err := drivers[owner].Query(ctx, objstore.Query{Entity: entity, Partition: partition})
		if err != nil {
			return nil, err
		}
		items := map[objstore.Key]objstore.Item{}
		for _, item := range result {
			items[item.Key] = item
		}
		caches[key] = items
		return items, nil
	}
	for _, row := range rows {
		image, removed := row.After, row.Before
		if rollback {
			image, removed = removed, image
		}
		if image.Key != (objstore.Key{}) {
			items, err := read(row.Owner, row.Entity, image.Key.Partition)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(items[image.Key], image) {
				return fmt.Errorf("final verification changed in %s.%s", row.Owner, row.Entity)
			}
		}
		if removed.Key != (objstore.Key{}) && image.Key != removed.Key {
			old, err := read(row.Owner, row.Entity, removed.Key.Partition)
			if err != nil {
				return err
			}
			if _, exists := old[removed.Key]; exists {
				return fmt.Errorf("old coordinate reappeared in %s.%s", row.Owner, row.Entity)
			}
		}
	}
	return nil
}
