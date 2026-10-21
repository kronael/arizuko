package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/compose"
)

const (
	rootfsPrefix = "rootfs-"
	kernelPrefix = "kernel-"
	envRootfsDir = "RUNED_ROOTFS_DIR"
	envKernelDir = "RUNED_KERNEL_DIR"
)

type imageTree struct {
	name   string
	path   string
	bytes  int64
	inUse  bool
	mounts []string
}

type mountEntry struct {
	root  string
	point string
}

func cmdImages(args []string) {
	instance, prune, err := parseImagesFlags(args)
	if err != nil {
		fmt.Println("usage: arizuko images <instance> [--prune|-p]")
		os.Exit(1)
	}
	dataDir := mustInstanceDir(instance)
	if err := runImages(os.Stdout, dataDir, "/proc", prune); err != nil {
		die("Failed: %v", err)
	}
}

func parseImagesFlags(args []string) (instance string, prune bool, err error) {
	fset := flag.NewFlagSet("images", flag.ContinueOnError)
	fset.BoolVar(&prune, "prune", false, "remove every tree that is not in use")
	fset.BoolVar(&prune, "p", false, "remove every tree that is not in use")
	if err = flexParse(fset, args); err != nil {
		return "", false, err
	}
	if fset.NArg() != 1 {
		return "", false, errors.New("expected <instance>")
	}
	return fset.Arg(0), prune, nil
}

func runImages(w io.Writer, dataDir, procDir string, prune bool) error {
	imagesDir := filepath.Join(dataDir, "images")
	info, err := os.Lstat(imagesDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory (a symlink is refused)", imagesDir)
	}
	rootfs, err := inUseTree(dataDir, envRootfsDir, rootfsPrefix)
	if err != nil {
		return err
	}
	kernel, err := inUseTree(dataDir, envKernelDir, kernelPrefix)
	if err != nil {
		return err
	}
	trees, err := listImageTrees(imagesDir)
	if err != nil {
		return err
	}
	for _, want := range []string{rootfs, kernel} {
		if !hasImageTree(trees, want) {
			return fmt.Errorf("in-use tree %s is not in %s; .env and disk disagree",
				want, imagesDir)
		}
	}
	mounts, self, err := readMounts(procDir)
	if err != nil {
		return err
	}
	for i := range trees {
		t := &trees[i]
		t.inUse = t.name == rootfs || t.name == kernel
		t.mounts = mountsUnder(mounts, treeViews(t.path, dataDir, self))
	}
	printImageTrees(w, trees)
	if !prune {
		return nil
	}
	return pruneImageTrees(w, imagesDir, trees)
}

func inUseTree(dataDir, key, prefix string) (string, error) {
	envFile := filepath.Join(dataDir, ".env")
	env, err := godotenv.Read(envFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w (run as the owner of the file)", envFile, err)
	}
	value := strings.TrimSpace(env[key])
	if value == "" {
		return "", fmt.Errorf("%s does not name %s, so the in-use tree is unknown", envFile, key)
	}
	dir := filepath.Dir(filepath.Clean(value))
	hostImages := filepath.Join(dataDir, "images")
	containerImages := filepath.Join(compose.ContainerDataMount, "images")
	if dir != hostImages && dir != containerImages {
		return "", fmt.Errorf("%s=%s is not directly in %s or %s", key, value,
			hostImages, containerImages)
	}
	name := filepath.Base(value)
	if !isImageTreeName(name) || !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("%s=%s is not a %s* tree", key, value, prefix)
	}
	return name, nil
}

func isImageTreeName(name string) bool {
	return len(name) > len(rootfsPrefix) &&
		(strings.HasPrefix(name, rootfsPrefix) || strings.HasPrefix(name, kernelPrefix))
}

func hasImageTree(trees []imageTree, name string) bool {
	for _, t := range trees {
		if t.name == name {
			return true
		}
	}
	return false
}

func listImageTrees(imagesDir string) ([]imageTree, error) {
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, err
	}
	var trees []imageTree
	for _, e := range entries {
		if !e.Type().IsDir() || !isImageTreeName(e.Name()) {
			continue
		}
		path := filepath.Join(imagesDir, e.Name())
		size, err := treeBytes(path)
		if err != nil {
			return nil, err
		}
		trees = append(trees, imageTree{name: e.Name(), path: path, bytes: size})
	}
	return trees, nil
}

func treeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func printImageTrees(w io.Writer, trees []imageTree) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TREE\tSIZE\tSTATE")
	for _, t := range trees {
		state := "unused"
		switch {
		case t.inUse:
			state = "in use"
		case len(t.mounts) > 0:
			state = "mounted"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", t.name, formatGB(t.bytes), state)
	}
	_ = tw.Flush()
}

func formatGB(n int64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

func pruneImageTrees(w io.Writer, imagesDir string, trees []imageTree) error {
	var victims []imageTree
	for _, t := range trees {
		if t.inUse {
			continue
		}
		if len(t.mounts) > 0 {
			return fmt.Errorf("%s has a mount at %s; nothing removed, retry when it is gone",
				t.name, t.mounts[0])
		}
		victims = append(victims, t)
	}
	var freed int64
	for _, t := range victims {
		if err := removeImageTree(imagesDir, t.name); err != nil {
			return err
		}
		freed += t.bytes
		_, _ = fmt.Fprintf(w, "removed %s (%s)\n", t.path, formatGB(t.bytes))
	}
	_, _ = fmt.Fprintf(w, "freed %s in %d trees\n", formatGB(freed), len(victims))
	return nil
}

func removeImageTree(imagesDir, name string) error {
	if !isImageTreeName(name) || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("refusing to remove %q: not an image tree name", name)
	}
	path := filepath.Join(imagesDir, name)
	if filepath.Dir(path) != imagesDir {
		return fmt.Errorf("refusing to remove %s: not a direct child of %s", path, imagesDir)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to remove %s: not a directory (a symlink is refused)", path)
	}
	return os.RemoveAll(path)
}

func readMounts(procDir string) (all, self []mountEntry, err error) {
	self, err = readMountinfo(filepath.Join(procDir, "self", "mountinfo"))
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if _, convErr := strconv.Atoi(e.Name()); convErr != nil {
			continue
		}
		mounts, err := readMountinfo(filepath.Join(procDir, e.Name(), "mountinfo"))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read mounts of process %s: %w (run as root)",
				e.Name(), err)
		}
		all = append(all, mounts...)
	}
	return all, self, nil
}

func readMountinfo(path string) ([]mountEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// read-only file; close error unactionable
	defer f.Close() //nolint:errcheck
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var mounts []mountEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 {
			continue
		}
		mounts = append(mounts, mountEntry{
			root:  unescape.Replace(fields[3]),
			point: unescape.Replace(fields[4]),
		})
	}
	return mounts, sc.Err()
}

func treeViews(hostTree, dataDir string, self []mountEntry) []string {
	views := []string{hostTree}
	if rel, err := filepath.Rel(dataDir, hostTree); err == nil {
		views = append(views, filepath.Join(compose.ContainerDataMount, rel))
	}
	best := ""
	for _, m := range self {
		if isUnder(hostTree, m.point) && len(m.point) > len(best) {
			best = m.point
		}
	}
	if best != "" && best != "/" {
		views = append(views, "/"+strings.TrimPrefix(strings.TrimPrefix(hostTree, best), "/"))
	}
	return views
}

func mountsUnder(mounts []mountEntry, views []string) []string {
	var hits []string
	for _, m := range mounts {
		for _, v := range views {
			if isUnder(m.point, v) || isUnder(m.root, v) {
				hits = append(hits, m.point)
				break
			}
		}
	}
	return hits
}

func isUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}
