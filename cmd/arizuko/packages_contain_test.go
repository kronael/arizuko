package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BUGS L201: `install` took any compose fragment a source shipped, so a package
// could bind an owner's store, `.env` or the docker socket and install cleanly.
// The bound holds at both writers of a sourced fragment — install and the one
// updater upgrade and sync share — and a refused install mints nothing first.

const cleanFragment = "services:\n  grab:\n    image: v1\n"

const socketFragment = "services:\n  grab:\n    image: v2\n" +
	"    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n"

// writeSourced writes a package source dir with the given fragment body.
func writeSourced(t *testing.T, base, name, fragment, manifest string) string {
	t.Helper()
	src := filepath.Join(base, name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, name+".yml"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(src, name+".yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestInstallRefusesFragmentReachingTheInstance(t *testing.T) {
	pkgChildRun()
	const own = "services:\n  grab:\n    image: x\n"
	for label, fragment := range map[string]string{
		"docker socket": own + "    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n",
		"owner store":   own + "    volumes: ['${DATA_DIR}/store/routd:/store']\n",
		"shared .env":   own + "    env_file: ['../.env']\n",
		"privileged":    own + "    privileged: true\n",
	} {
		t.Run(label, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("ARIZUKO_DATA_DIR", base)
			dataDir := seedPkgInstance(t, base, "ct")
			src := writeSourced(t, base, "grab", fragment, "env:\n  GRAB_KEY:\n    generate: true\n")

			if code := runPkgExpectingDeath(t, []string{"ct", "install", src}); code == 0 {
				t.Fatalf("a package whose fragment reaches the %s installed", label)
			}
			assertNothingInstalled(t, dataDir, "grab")
			// The env block mints into .env; a refusal that came after it would
			// leave a key behind for a package that never installed.
			if env, err := os.ReadFile(filepath.Join(dataDir, ".env")); err == nil && strings.Contains(string(env), "GRAB_KEY") {
				t.Error("the refused install minted its env key first")
			}
		})
	}
}

// A revision that reaches out after a clean install is refused by upgrade with
// the installed fragment and record untouched.
func TestUpgradeRefusesFragmentReachingTheInstance(t *testing.T) {
	pkgChildRun()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "cu")
	src := writeSourced(t, base, "grab", cleanFragment, "")
	cmdPackages([]string{"cu", "install", src})

	if err := os.WriteFile(filepath.Join(src, "grab.yml"), []byte(socketFragment), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runPkgExpectingDeath(t, []string{"cu", "upgrade", "grab"}); code == 0 {
		t.Fatal("upgrade applied a revision that mounts the docker socket")
	}
	assertFragmentKept(t, dataDir, "grab", cleanFragment)
	if rec := mustRec(t, dataDir, "grab"); rec.AssetHashes["file:grab.yml"] != sha256hex([]byte(cleanFragment)) {
		t.Errorf("the record moved to the refused revision: %+v", rec)
	}
}

// sync reports the refused package and moves on, the disposition it gives a
// dirty one, so one bad revision cannot block the rest of the instance.
func TestSyncSkipsARefusedRevision(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "cs")
	src := writeSourced(t, base, "grab", cleanFragment, "")
	cmdPackages([]string{"cs", "install", src})

	if err := os.WriteFile(filepath.Join(src, "grab.yml"), []byte(socketFragment), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"cs", "sync"})
	assertFragmentKept(t, dataDir, "grab", cleanFragment)
	if rec := mustRec(t, dataDir, "grab"); rec.AssetHashes["file:grab.yml"] != sha256hex([]byte(cleanFragment)) {
		t.Errorf("the record moved to the refused revision: %+v", rec)
	}
}

// A package's name is its ground, so a source may not take a core service's
// name, and its files may not replace a fragment the instance already holds.
func TestInstallRefusesACoreNameOrAPresentFragment(t *testing.T) {
	pkgChildRun()
	t.Run("core name", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("ARIZUKO_DATA_DIR", base)
		dataDir := seedPkgInstance(t, base, "cn")
		src := writeSourced(t, base, "routd", "services:\n  routd:\n    image: x\n", "")
		if code := runPkgExpectingDeath(t, []string{"cn", "install", src}); code == 0 {
			t.Fatal("a package named routd installed")
		}
		assertNothingInstalled(t, dataDir, "routd")
	})
	t.Run("present fragment", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("ARIZUKO_DATA_DIR", base)
		dataDir := seedPkgInstance(t, base, "cp")
		const operators = "services:\n  keep:\n    image: mine\n"
		if err := os.MkdirAll(filepath.Join(dataDir, "services"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "services", "keep.yml"), []byte(operators), 0o644); err != nil {
			t.Fatal(err)
		}
		src := writeSourced(t, base, "keep", "services:\n  keep:\n    image: theirs\n", "")
		if code := runPkgExpectingDeath(t, []string{"cp", "install", src}); code == 0 {
			t.Fatal("a package replaced a fragment the instance already had")
		}
		assertFragmentKept(t, dataDir, "keep", operators)
	})
}

func assertFragmentKept(t *testing.T, dataDir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dataDir, "services", name+".yml"))
	if err != nil {
		t.Fatalf("read the installed fragment: %v", err)
	}
	if string(got) != want {
		t.Errorf("services/%s.yml was rewritten:\n%s", name, got)
	}
}
