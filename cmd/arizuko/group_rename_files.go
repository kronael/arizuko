package main

import (
	"bytes"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

type renameFile struct {
	Operation, Source, Destination, Original string
	Before, After                            []byte
	Mode                                     uint32
	UID, GID                                 int
	Device, Inode                            uint64
	Size                                     int64
	Edited                                   bool
	AccessTime, ModifyTime                   int64
	Xattrs                                   map[string][]byte
}

func (mapping renameMap) filePath(root, path string) string {
	for _, tree := range []string{"groups", "ipc", "web/pub", "web/priv", "web"} {
		prefix := filepath.Join(root, tree) + string(filepath.Separator)
		if strings.HasPrefix(path, prefix) {
			relative := filepath.ToSlash(strings.TrimPrefix(path, prefix))
			next := mapping.folder(relative)
			if next != relative {
				return prefix + filepath.FromSlash(next)
			}
		}
	}
	return path
}

func renameFileImage(operation, source, destination string, info os.FileInfo) renameFile {
	stat := info.Sys().(*syscall.Stat_t)
	return renameFile{Operation: operation, Source: source, Destination: destination, Original: source,
		Mode: uint32(info.Mode()), UID: int(stat.Uid), GID: int(stat.Gid), Device: uint64(stat.Dev), Inode: stat.Ino, Size: info.Size(), AccessTime: stat.Atim.Nano(), ModifyTime: stat.Mtim.Nano()}
}

func renamePlanFiles(root string, mapping renameMap) ([]renameFile, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = renameCheckMounts(root); err != nil {
		return nil, err
	}
	plan := []renameFile{}
	directories := map[string]bool{}
	destinations := map[string]string{}
	originals := map[os.FileInfo]string{}
	var mkdir func(string, os.FileInfo) error
	mkdir = func(path string, info os.FileInfo) error {
		if directories[path] {
			return nil
		}
		found, err := os.Lstat(path)
		if err == nil {
			if !found.IsDir() {
				return fmt.Errorf("destination ancestor %s is not a directory", path)
			}
			directories[path] = true
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = mkdir(filepath.Dir(path), info); err != nil {
			return err
		}
		directories[path] = true
		operation := renameFileImage("mkdir", path, "", info)
		operation.Xattrs, err = renameXattrs(originals[info])
		if err != nil {
			return err
		}
		plan = append(plan, operation)
		return nil
	}
	var move func(string, string) error
	move = func(source, destination string) error {
		if prior, ok := destinations[destination]; ok && prior != source {
			return fmt.Errorf("filesystem mapping collision at %s", destination)
		}
		destinations[destination] = source
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		originals[info] = source
		if info.Mode()&os.ModeSocket != 0 {
			return fmt.Errorf("socket %s remains; remove transient sockets after stopping all writers", source)
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported filesystem object %s", source)
		}
		target, err := os.Lstat(destination)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		exists := err == nil
		if exists && (!info.IsDir() || !target.IsDir()) {
			return fmt.Errorf("filesystem collision at %s", destination)
		}
		if err = mkdir(filepath.Dir(destination), info); err != nil {
			return err
		}
		descendants := false
		relative := ""
		for _, tree := range []string{"groups", "ipc", "web/pub", "web/priv", "web"} {
			prefix := filepath.Join(root, tree) + string(filepath.Separator)
			if strings.HasPrefix(source, prefix) {
				relative = filepath.ToSlash(strings.TrimPrefix(source, prefix))
				break
			}
		}
		for _, entry := range mapping {
			if relative != "" && entry.Source != relative && renameContains(relative, entry.Source) {
				descendants = true
			}
		}
		if !info.IsDir() || (!exists && !descendants) {
			plan = append(plan, renameFileImage("move", source, destination, info))
			return nil
		}
		if err = mkdir(destination, info); err != nil {
			return err
		}
		children, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, child := range children {
			path := filepath.Join(source, child.Name())
			if err = move(path, mapping.filePath(root, path)); err != nil {
				return err
			}
		}
		operation := renameFileImage("rmdir", source, "", info)
		operation.Xattrs, err = renameXattrs(source)
		if err != nil {
			return err
		}
		plan = append(plan, operation)
		return nil
	}
	broad := mapping[len(mapping)-1]
	for _, tree := range []string{"groups", "ipc", "web/pub", "web/priv", "web"} {
		source := filepath.Join(root, tree, filepath.FromSlash(broad.Source))
		if _, err = os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err = move(source, mapping.filePath(root, source)); err != nil {
			return nil, err
		}
	}
	edits := []renameFile{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		components := strings.Split(filepath.ToSlash(relative), "/")
		if entry.IsDir() {
			if components[0] == "images" || components[0] == "packages" {
				return filepath.SkipDir
			}
			for _, name := range components {
				if name == ".git" || name == "store" || name == "keys" || name == "backups" {
					return filepath.SkipDir
				}
			}
			if strings.Contains(filepath.ToSlash(relative), ".claude/projects") || strings.Contains(filepath.ToSlash(relative), ".codex/sessions") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "record.json" && strings.HasPrefix(filepath.ToSlash(relative), "vms/") {
			return fmt.Errorf("VM record %s remains; reap guests before rename", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		final := mapping.filePath(root, path)
		if info.Mode()&os.ModeSocket != 0 && final != path {
			return fmt.Errorf("socket %s remains; remove transient sockets after stopping writers", path)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			before, err := os.Readlink(path)
			if err != nil {
				return err
			}
			target := before
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			target = mapping.filePath(root, filepath.Clean(target))
			after := target
			if !filepath.IsAbs(before) {
				after, err = filepath.Rel(filepath.Dir(final), target)
				if err != nil {
					return err
				}
			}
			if before != after {
				operation := renameFileImage("symlink", final, "", info)
				operation.Original = path
				operation.Xattrs, err = renameLinkXattrs(path)
				if err != nil {
					return err
				}
				operation.Before = []byte(before)
				operation.After = []byte(after)
				edits = append(edits, operation)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if len(components) > 0 && components[0] == "groups" {
			operational := entry.Name() == ".mcp.json" || entry.Name() == ".env" || strings.Contains(filepath.ToSlash(relative), "/.claude/settings") || strings.Contains(filepath.ToSlash(relative), "/.claude/hooks/") || strings.HasSuffix(filepath.ToSlash(relative), "/.codex/config.toml")
			if !operational {
				return nil
			}
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".md":
			if len(components) == 0 || components[0] != "web" {
				return nil
			}
		case ".html", ".htm", ".css", ".js", ".ts", ".sh", ".bash", ".yaml", ".yml", ".json", ".toml", ".env":
		default:
			if entry.Name() != ".env" {
				return nil
			}
		}
		if info.Size() > 4<<20 {
			return fmt.Errorf("operational file %s exceeds scan limit; classify it before rename", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		next, err := mapping.operational(root, data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !bytes.Equal(data, next) {
			for _, temporary := range []string{path + ".arizuko-rename", final + ".arizuko-rename"} {
				if _, err = os.Lstat(temporary); err == nil {
					return fmt.Errorf("staging collision at %s", temporary)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			operation := renameFileImage("edit", final, "", info)
			operation.Original = path
			operation.Before = data
			operation.After = next
			if info.Sys().(*syscall.Stat_t).Nlink > 1 {
				return fmt.Errorf("operational edit %s is hard-linked; classify it before rename", path)
			}
			operation.Xattrs, err = renameXattrs(path)
			if err != nil {
				return err
			}
			edits = append(edits, operation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range plan {
		for _, edit := range edits {
			if plan[i].Operation == "move" && plan[i].Destination == edit.Source {
				plan[i].Edited = true
			}
		}
	}
	plan = append(plan, edits...)
	for _, operation := range plan {
		path := operation.Source
		if operation.Destination != "" {
			path = operation.Destination
		}
		if err = renameCheckAncestors(root, filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func (mapping renameMap) operational(root string, data []byte) ([]byte, error) {
	prefixes := []string{"/pub/", "/priv/", "/dav/"}
	for _, tree := range []string{"groups", "ipc", "web/pub", "web/priv", "web"} {
		prefixes = append(prefixes, filepath.Join(root, tree)+"/")
	}
	pattern := regexp.MustCompile(`[^\s"'<>\x60)\]};,]+`)
	result := pattern.ReplaceAllFunc(data, func(token []byte) []byte {
		text := string(token)
		for _, prefix := range prefixes {
			start := strings.Index(text, prefix)
			if start < 0 {
				continue
			}
			value := text[start+len(prefix):]
			next := mapping.folder(value)
			if next != value {
				return []byte(text[:start] + prefix + next)
			}
		}
		return token
	})
	for _, prefix := range []string{"--folder ", "--folder=", "FOLDER=", "GROUP_FOLDER=", "folder: ", "owner_folder: ", "target_folder: "} {
		if strings.Contains(string(result), prefix) && mapping.hasReference(string(result)) {
			return nil, fmt.Errorf("unclassified folder setting; edit its declared coordinate before preflight")
		}
	}
	return result, nil
}

func renameFileState(operation renameFile) (bool, bool, error) {
	info, err := os.Lstat(operation.Source)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	switch operation.Operation {
	case "mkdir":
		if !exists {
			return true, false, nil
		}
		if !info.IsDir() {
			return false, false, nil
		}
		return false, true, nil
	case "rmdir":
		if !exists {
			return false, true, nil
		}
		if !info.IsDir() {
			return false, false, nil
		}
		entries, err := os.ReadDir(operation.Source)
		return len(entries) == 0, false, err
	case "move":
		destination, err := os.Lstat(operation.Destination)
		targetExists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, false, err
		}
		return exists && !targetExists && renameSameFile(info, operation), !exists && targetExists && renameSameFile(destination, operation), nil
	case "symlink":
		if !exists || info.Mode()&os.ModeSymlink == 0 {
			return false, false, nil
		}
		target, err := os.Readlink(operation.Source)
		return target == string(operation.Before), target == string(operation.After), err
	case "edit":
		if !exists || !info.Mode().IsRegular() {
			return false, false, nil
		}
		data, err := os.ReadFile(operation.Source)
		return bytes.Equal(data, operation.Before), bytes.Equal(data, operation.After), err
	default:
		return false, false, fmt.Errorf("unknown file operation %q", operation.Operation)
	}
}

func renameSameFile(info os.FileInfo, operation renameFile) bool {
	stat := info.Sys().(*syscall.Stat_t)
	return (operation.Edited || stat.Ino == operation.Inode) && uint64(stat.Dev) == operation.Device && uint32(info.Mode()) == operation.Mode &&
		(info.IsDir() || info.Size() == operation.Size)
}

func renameApplyFile(operation renameFile, rollback bool) error {
	action := operation.Operation
	source, destination := operation.Source, operation.Destination
	data := operation.After
	if rollback {
		data = operation.Before
		switch action {
		case "move":
			source, destination = destination, source
		case "mkdir":
			action = "rmdir"
		case "rmdir":
			action = "mkdir"
		}
	}
	var err error
	switch action {
	case "move":
		err = os.Rename(source, destination)
	case "mkdir":
		err = os.Mkdir(source, os.FileMode(operation.Mode).Perm())
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(source)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("directory staging collision at %s", source)
			}
			err = nil
		}
		if err == nil {
			err = renameRestoreDirectory(source, operation)
		}
	case "rmdir":
		err = os.Remove(source)
	case "symlink":
		temporary := source + ".arizuko-rename"
		if err = os.Symlink(string(data), temporary); err != nil {
			target, readErr := os.Readlink(temporary)
			if !errors.Is(err, os.ErrExist) || readErr != nil || target != string(data) {
				return fmt.Errorf("symlink staging collision at %s", temporary)
			}
		}
		if err = os.Lchown(temporary, operation.UID, operation.GID); err != nil {
			return err
		}
		for name, value := range operation.Xattrs {
			if err = unix.Lsetxattr(temporary, name, value, 0); err != nil {
				return err
			}
		}
		times := []unix.Timespec{unix.NsecToTimespec(operation.AccessTime), unix.NsecToTimespec(operation.ModifyTime)}
		if err = unix.UtimesNanoAt(unix.AT_FDCWD, temporary, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		err = os.Rename(temporary, source)
	case "edit":
		temporary := source + ".arizuko-rename"
		if err = renameDurableFile(temporary, data, os.FileMode(operation.Mode).Perm()); err != nil {
			info, statErr := os.Lstat(temporary)
			if statErr != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("file staging is not a regular file at %s", temporary)
			}
			staged, readErr := os.ReadFile(temporary)
			if !errors.Is(err, os.ErrExist) || readErr != nil {
				return fmt.Errorf("file staging collision at %s", temporary)
			}
			if !bytes.Equal(staged, data) {
				if info.Sys().(*syscall.Stat_t).Nlink != 1 {
					return fmt.Errorf("staging file has multiple links at %s", temporary)
				}
				file, openErr := os.OpenFile(temporary, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
				if openErr != nil {
					return openErr
				}
				_, writeErr := file.Write(data)
				syncErr := file.Sync()
				closeErr := file.Close()
				if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
					return err
				}
			}
		}
		if err = os.Chown(temporary, operation.UID, operation.GID); err != nil {
			return err
		}
		if err = os.Chmod(temporary, os.FileMode(operation.Mode)); err != nil {
			return err
		}
		if err = renameRestoreMetadata(temporary, operation); err != nil {
			return err
		}
		err = os.Rename(temporary, source)
	default:
		return fmt.Errorf("unknown file operation %q", action)
	}
	if err != nil {
		return err
	}
	for _, parent := range slices.Compact([]string{filepath.Dir(source), filepath.Dir(destination)}) {
		if destination == "" && parent == "." {
			continue
		}
		if err = renameSyncDir(parent); err != nil {
			return err
		}
	}
	return nil
}

func renameXattrs(path string) (map[string][]byte, error) {
	n, err := unix.Listxattr(path, nil)
	if err != nil {
		return nil, err
	}
	names := make([]byte, n)
	n, err = unix.Listxattr(path, names)
	if err != nil {
		return nil, err
	}
	attrs := map[string][]byte{}
	for _, name := range strings.Split(string(names[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err := unix.Getxattr(path, name, nil)
		if err != nil {
			return nil, err
		}
		value := make([]byte, size)
		size, err = unix.Getxattr(path, name, value)
		if err != nil {
			return nil, err
		}
		attrs[name] = value[:size]
	}
	return attrs, nil
}

func renameRestoreMetadata(path string, operation renameFile) error {
	for name, value := range operation.Xattrs {
		if err := unix.Setxattr(path, name, value, 0); err != nil {
			return err
		}
	}
	return os.Chtimes(path, time.Unix(0, operation.AccessTime), time.Unix(0, operation.ModifyTime))
}

func renameCheckMounts(root string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		path := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(fields[4])
		if path != root && strings.HasPrefix(path, root+string(filepath.Separator)) {
			return fmt.Errorf("mount %s remains under instance data; unmount guest staging before rename", path)
		}
	}
	return nil
}

func renameCheckAncestors(root, path string) error {
	for path != filepath.Dir(root) && renameContains(filepath.ToSlash(root), filepath.ToSlash(path)) {
		info, err := os.Lstat(path)
		if err == nil && !info.IsDir() {
			return fmt.Errorf("operational ancestor %s is not a directory", path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		path = filepath.Dir(path)
	}
	return nil
}

func renameRestoreDirectory(path string, operation renameFile) error {
	if err := os.Chown(path, operation.UID, operation.GID); err != nil {
		return err
	}
	if err := os.Chmod(path, os.FileMode(operation.Mode)); err != nil {
		return err
	}
	return renameRestoreMetadata(path, operation)
}

func renameDiscardStaging(operation renameFile, reverse bool) error {
	if operation.Operation != "edit" && operation.Operation != "symlink" {
		return nil
	}
	path := operation.Source + ".arizuko-rename"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if operation.Operation == "edit" {
		if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			return fmt.Errorf("unclassified staging object at %s", path)
		}
	} else {
		expected := operation.After
		if reverse {
			expected = operation.Before
		}
		target, err := os.Readlink(path)
		if err != nil || target != string(expected) {
			return fmt.Errorf("unclassified staging link at %s", path)
		}
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return renameSyncDir(filepath.Dir(path))
}

func renameLinkXattrs(path string) (map[string][]byte, error) {
	n, err := unix.Llistxattr(path, nil)
	if errors.Is(err, unix.EOPNOTSUPP) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]byte, n)
	n, err = unix.Llistxattr(path, names)
	if err != nil {
		return nil, err
	}
	attrs := map[string][]byte{}
	for _, name := range strings.Split(string(names[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			return nil, err
		}
		value := make([]byte, size)
		size, err = unix.Lgetxattr(path, name, value)
		if err != nil {
			return nil, err
		}
		attrs[name] = value[:size]
	}
	return attrs, nil
}
