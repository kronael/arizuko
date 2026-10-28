package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type imagesFixture struct {
	dataDir string
	procDir string
	outside string
}

func newImagesFixture(t *testing.T, env string) imagesFixture {
	t.Helper()
	root := t.TempDir()
	f := imagesFixture{
		dataDir: filepath.Join(root, "data", "arizuko_t"),
		procDir: filepath.Join(root, "proc"),
		outside: filepath.Join(root, "outside"),
	}
	for _, dir := range []string{
		"rootfs-a", "rootfs-b", "kernel-1", "kernel-2", "notes", "rootfs-",
	} {
		mustWrite(t, filepath.Join(f.dataDir, "images", dir, "blob"), "0123456789")
	}
	mustWrite(t, filepath.Join(f.dataDir, "images", "rootfs-file"), "x")
	mustWrite(t, filepath.Join(f.dataDir, "images", ".rootfs-partial.1", "blob"), "x")
	mustWrite(t, filepath.Join(f.outside, "keep"), "x")
	if err := os.Symlink(f.outside, filepath.Join(f.dataDir, "images", "rootfs-link")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.dataDir, ".env"), env)
	mustWrite(t, filepath.Join(f.procDir, "self", "mountinfo"),
		"20 1 8:1 / / rw - ext4 /dev/sda1 rw\n")
	mustWrite(t, filepath.Join(f.procDir, "41", "mountinfo"),
		"30 20 8:1 /srv/other /srv/app/home rw - ext4 /dev/sda1 rw\n")
	return f
}

const inUseEnv = "RUNED_ROOTFS_DIR=/srv/app/home/images/rootfs-b\n" +
	"RUNED_KERNEL_DIR=/srv/app/home/images/kernel-2\n"

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f imagesFixture) exists(t *testing.T, rel string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(f.dataDir, "images", rel))
	return err == nil
}

func TestImagesPruneRemovesOnlyUnusedTrees(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	var out bytes.Buffer
	if err := runImages(&out, f.dataDir, f.procDir, true); err != nil {
		t.Fatalf("prune: %v\n%s", err, out.String())
	}
	for _, gone := range []string{"rootfs-a", "kernel-1"} {
		if f.exists(t, gone) {
			t.Errorf("%s survived the prune", gone)
		}
	}
	for _, kept := range []string{
		"rootfs-b", "kernel-2", "notes", "rootfs-", "rootfs-file",
		".rootfs-partial.1", "rootfs-link",
	} {
		if !f.exists(t, kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	if _, err := os.Stat(filepath.Join(f.outside, "keep")); err != nil {
		t.Errorf("the prune followed a symlink out of images: %v", err)
	}
}

func TestImagesPruneAcceptsHostPath(t *testing.T) {
	f := newImagesFixture(t, "")
	host := filepath.Join(f.dataDir, "images")
	mustWrite(t, filepath.Join(f.dataDir, ".env"),
		"RUNED_ROOTFS_DIR="+host+"/rootfs-b\nRUNED_KERNEL_DIR="+host+"/kernel-2\n")
	if err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true); err != nil {
		t.Fatal(err)
	}
	if !f.exists(t, "rootfs-b") || f.exists(t, "rootfs-a") {
		t.Error("host-path in-use tree was not honoured")
	}
}

func TestImagesListRemovesNothingAndMarksState(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	var out bytes.Buffer
	if err := runImages(&out, f.dataDir, f.procDir, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rootfs-a", "rootfs-b", "kernel-1", "kernel-2"} {
		if !f.exists(t, name) {
			t.Errorf("list removed %s", name)
		}
	}
	lines := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			lines[fields[0]] = line
		}
	}
	if !strings.HasSuffix(lines["rootfs-b"], "in use") ||
		!strings.HasSuffix(lines["kernel-2"], "in use") {
		t.Errorf("in-use trees not marked:\n%s", out.String())
	}
	if !strings.HasSuffix(lines["rootfs-a"], "unused") {
		t.Errorf("stale tree not marked unused:\n%s", out.String())
	}
	if _, ok := lines["notes"]; ok {
		t.Errorf("non-matching name listed:\n%s", out.String())
	}
}

func TestImagesPruneRefusesMountedTree(t *testing.T) {
	cases := map[string]func(dataDir string) string{
		"runed view point": func(string) string {
			return "40 30 8:1 /x /srv/app/home/images/rootfs-a/usr ro - ext4 /dev/sda1 rw\n"
		},
		"host view root": func(dataDir string) string {
			return "40 30 8:1 " + dataDir + "/images/rootfs-a /vms/h/rootfs ro - ext4 /dev/sda1 rw\n"
		},
		"escaped point": func(string) string {
			return "40 30 8:1 /x /srv/app/home/images/rootfs-a/a\\040b ro - ext4 /dev/sda1 rw\n"
		},
	}
	for name, mountLine := range cases {
		t.Run(name, func(t *testing.T) {
			f := newImagesFixture(t, inUseEnv)
			mustWrite(t, filepath.Join(f.procDir, "77", "mountinfo"), mountLine(f.dataDir))
			err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true)
			if err == nil || !strings.Contains(err.Error(), "rootfs-a") {
				t.Fatalf("want a refusal naming rootfs-a, got %v", err)
			}
			for _, name := range []string{"rootfs-a", "rootfs-b", "kernel-1", "kernel-2"} {
				if !f.exists(t, name) {
					t.Errorf("%s removed despite the refusal", name)
				}
			}
		})
	}
}

func TestImagesPruneRefusesMountSeenOnlyThroughFilesystemRelativeRoot(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	mustWrite(t, filepath.Join(f.procDir, "self", "mountinfo"),
		"20 1 8:1 / / rw - ext4 /dev/sda1 rw\n"+
			"21 20 8:2 / "+filepath.Dir(f.dataDir)+" rw - ext4 /dev/sdb1 rw\n")
	mustWrite(t, filepath.Join(f.procDir, "77", "mountinfo"),
		"40 30 8:2 /arizuko_t/images/rootfs-a /vms/h/tree/rootfs ro - ext4 /dev/sdb1 rw\n")
	err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true)
	if err == nil || !f.exists(t, "rootfs-a") {
		t.Fatalf("want refusal and rootfs-a kept, got %v", err)
	}
}

func TestImagesIgnoresMountBesideTree(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	mustWrite(t, filepath.Join(f.procDir, "77", "mountinfo"),
		"40 30 8:1 /srv/data/arizuko_t /srv/app/home rw - ext4 /dev/sda1 rw\n"+
			"41 30 8:1 /x /srv/app/home/images/rootfs-ab rw - ext4 /dev/sda1 rw\n")
	if err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true); err != nil {
		t.Fatalf("a sibling name prefix must not count as mounted: %v", err)
	}
}

func TestImagesRefusesUnknownInUseTree(t *testing.T) {
	good := inUseEnv
	cases := map[string]string{
		"no .env keys":     "ASSISTANT_NAME=x\n",
		"rootfs missing":   "RUNED_KERNEL_DIR=/srv/app/home/images/kernel-2\n",
		"kernel missing":   "RUNED_ROOTFS_DIR=/srv/app/home/images/rootfs-b\n",
		"rootfs empty":     "RUNED_ROOTFS_DIR=\nRUNED_KERNEL_DIR=/srv/app/home/images/kernel-2\n",
		"outside images":   "RUNED_ROOTFS_DIR=/srv/shared/rootfs-b\nRUNED_KERNEL_DIR=/srv/app/home/images/kernel-2\n",
		"nested path":      "RUNED_ROOTFS_DIR=/srv/app/home/images/rootfs-b/usr\nRUNED_KERNEL_DIR=/srv/app/home/images/kernel-2\n",
		"swapped prefixes": "RUNED_ROOTFS_DIR=/srv/app/home/images/kernel-2\nRUNED_KERNEL_DIR=/srv/app/home/images/rootfs-b\n",
		"tree not on disk": "RUNED_ROOTFS_DIR=/srv/app/home/images/rootfs-zz\nRUNED_KERNEL_DIR=/srv/app/home/images/kernel-2\n",
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			f := newImagesFixture(t, good)
			mustWrite(t, filepath.Join(f.dataDir, ".env"), env)
			if err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true); err == nil {
				t.Fatal("want a refusal")
			}
			for _, name := range []string{"rootfs-a", "rootfs-b", "kernel-1", "kernel-2"} {
				if !f.exists(t, name) {
					t.Errorf("%s removed despite the refusal", name)
				}
			}
		})
	}
}

func TestImagesRefusesMissingEnvFile(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	if err := os.Remove(filepath.Join(f.dataDir, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true); err == nil {
		t.Fatal("want a refusal")
	}
	if !f.exists(t, "rootfs-a") {
		t.Error("rootfs-a removed without an .env")
	}
}

func TestImagesRefusesSymlinkedImagesDir(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	real := filepath.Join(f.dataDir, "images")
	moved := filepath.Join(filepath.Dir(f.dataDir), "moved")
	if err := os.Rename(real, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, real); err != nil {
		t.Fatal(err)
	}
	if err := runImages(&bytes.Buffer{}, f.dataDir, f.procDir, true); err == nil {
		t.Fatal("want a refusal")
	}
	if _, err := os.Stat(filepath.Join(moved, "rootfs-a")); err != nil {
		t.Errorf("rootfs-a removed through the symlink: %v", err)
	}
}

func TestRemoveImageTreeRefusesWhatIsNotATree(t *testing.T) {
	f := newImagesFixture(t, inUseEnv)
	images := filepath.Join(f.dataDir, "images")
	for _, name := range []string{"notes", "rootfs-link", "rootfs-file", "../images", "rootfs-a/.."} {
		if err := removeImageTree(images, name); err == nil {
			t.Errorf("removeImageTree(%q) did not refuse", name)
		}
	}
	if !f.exists(t, "notes") || !f.exists(t, "rootfs-link") {
		t.Error("a refused name was removed")
	}
}

func TestParseImagesFlags(t *testing.T) {
	inst, prune, err := parseImagesFlags([]string{"krons", "--prune"})
	if err != nil || inst != "krons" || !prune {
		t.Errorf("got %q %v %v", inst, prune, err)
	}
	inst, prune, err = parseImagesFlags([]string{"--prune", "krons"})
	if err != nil || inst != "krons" || !prune {
		t.Errorf("got %q %v %v", inst, prune, err)
	}
	if _, _, err = parseImagesFlags([]string{"-p", "krons"}); err == nil {
		t.Error("-p accepted: prune is long-only")
	}
	inst, prune, err = parseImagesFlags([]string{"krons"})
	if err != nil || inst != "krons" || prune {
		t.Errorf("got %q %v %v", inst, prune, err)
	}
	if _, _, err = parseImagesFlags(nil); err == nil {
		t.Error("want an error without an instance")
	}
}
