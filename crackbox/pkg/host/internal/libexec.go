package internal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// LibexecDir returns the directory containing crackbox-cloudinit and crackbox-tap.
// Search order:
//  1. $CRACKBOX_LIBEXEC env (operator override)
//  2. /usr/local/libexec/crackbox (default install prefix)
//  3. /usr/libexec/crackbox      (system package install)
//  4. dev fallback: walk up from this source file's compile-time path
//     until a directory containing libexec/crackbox-cloudinit is found
func LibexecDir() (string, error) {
	if v := os.Getenv("CRACKBOX_LIBEXEC"); v != "" {
		if hasTools(v) {
			return v, nil
		}
		return "", errors.New("CRACKBOX_LIBEXEC set but crackbox-cloudinit/crackbox-tap not found in: " + v)
	}

	for _, candidate := range []string{
		"/usr/local/libexec/crackbox",
		"/usr/libexec/crackbox",
	} {
		if hasTools(candidate) {
			return candidate, nil
		}
	}

	// Dev fallback: walk up from this source file's compile-time embedded path.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("libexec: cannot determine source path; set CRACKBOX_LIBEXEC")
	}
	dir := filepath.Dir(thisFile)
	for {
		candidate := filepath.Join(dir, "libexec")
		if hasTools(candidate) {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", errors.New("libexec: crackbox-cloudinit/crackbox-tap not found; set CRACKBOX_LIBEXEC or run make install")
}

func hasTools(dir string) bool {
	for _, name := range []string{"crackbox-cloudinit", "crackbox-tap"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
