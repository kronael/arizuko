package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
)

func TestMain(m *testing.M) {
	if os.Getenv("ARIZUKO_STARTUP_CHILD") != "" {
		os.Exit(m.Run())
	}
	dyntest.Main(m)
}

// Receipt refusal stops startup; retry completes the same active release.
func TestCorePackageStartupReceiptRepair(t *testing.T) {
	dataDir, release := t.TempDir(), t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("APP_SRC_DIR", release)
	t.Setenv("SECRETS_KEY", "")
	for name, body := range map[string]string{
		"arizuko.toml": "name = \"core\"\n", "CLAUDE.md": "core manual",
		"skills/base/SKILL.md": "core skill", "output-styles/brief.md": "core style",
	} {
		path := filepath.Join(release, "ant", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return err
			}
			return os.Chmod(path, 0o755)
		})
	})
	endpoint, err := url.Parse(os.Getenv("DYNAMODB_URL"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(endpoint)
	var refuse atomic.Bool
	refuse.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if refuse.Load() && strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".TransactWriteItems") &&
			bytes.Contains(body, []byte("installed_packages")) {
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"receipt write refused"}`))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("DYNAMODB_URL", server.URL)
	var before os.FileInfo
	for _, failing := range []bool{true, false} {
		refuse.Store(failing)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCorePackageStartupChild$")
		cmd.Env = append(os.Environ(), "ARIZUKO_STARTUP_CHILD=1")
		out, err := cmd.CombinedOutput()
		cancel()
		want := "active but its receipt failed"
		if !failing {
			want = "secrets keyring"
		}
		if err == nil || !strings.Contains(string(out), want) {
			t.Fatalf("startup outcome missing %q: %v %s", want, err, out)
		}
		after, err := os.Lstat(filepath.Join(dataDir, "package-active/system"))
		if err != nil || before != nil && !os.SameFile(before, after) {
			t.Fatalf("startup repair changed active reference: %v", err)
		}
		before = after
	}
	db, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	selected, err := container.ReadPackageSelection(dataDir, "")
	receipt, found, rerr := db.InstalledPackage("", "core")
	if err != nil || rerr != nil || !found || len(selected) != 1 ||
		receipt.Source != "ant" || receipt.Revision != selected[0].Digest {
		t.Fatalf("startup receipt missing: %+v %+v %v %v", selected, receipt, err, rerr)
	}
}

func TestCorePackageStartupChild(t *testing.T) {
	if os.Getenv("ARIZUKO_STARTUP_CHILD") == "" {
		return
	}
	main()
}

// Missing release bytes stop the real daemon before auth or runtime activity.
func TestCorePackageStartupMissingSource(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected startup activity", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("APP_SRC_DIR", t.TempDir())
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("SECRETS_KEY", "startup-test-key")
	t.Setenv("AUTHD_URL", server.URL)
	t.Setenv("RUNED_URL", server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCorePackageStartupChild$")
	cmd.Env = append(os.Environ(), "ARIZUKO_STARTUP_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "required core source") {
		t.Fatalf("missing core did not stop startup: %v, %s", err, out)
	}
	if requests.Load() != 0 {
		t.Fatalf("startup issued %d requests before validating core", requests.Load())
	}
}
