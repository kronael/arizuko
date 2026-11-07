package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveInstance(t *testing.T) {
	base := t.TempDir()
	bin := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	capture := filepath.Join(t.TempDir(), "docker")
	t.Setenv("CAPTURE", capture)
	writeFile(t, filepath.Join(bin, "systemctl"), "#!/bin/sh\nexit \"$UNIT_STATUS\"\n")
	writeFile(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE\"\n")
	for _, name := range []string{"systemctl", "docker"} {
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(base, "arizuko_eval")
	var storeRead bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storeRead = r.URL.Path == "/management/accounts"
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	t.Setenv("EXTENDDB_PASSWORD", "password")
	writeFile(t, filepath.Join(dir, ".env"), "DYNAMODB_URL="+srv.URL+"\n")
	writeFile(t, filepath.Join(dir, "docker-compose.yml"), "services: {}\n")
	other := filepath.Join(base, "arizuko_other", ".env")
	writeFile(t, other, "keep")
	t.Setenv("UNIT_STATUS", "0")
	if err := removeInstance("eval"); err == nil {
		t.Fatal("active unit was removed")
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("compose ran for active unit")
	}
	t.Setenv("UNIT_STATUS", "3")
	if err := removeInstance("eval"); err != nil {
		t.Fatal(err)
	}
	if !storeRead {
		t.Fatal("store account was not checked")
	}
	args, err := os.ReadFile(capture)
	if err != nil || !strings.Contains(string(args), "-p\narizuko_eval\n-f\n"+filepath.Join(dir, "docker-compose.yml")+"\ndown\n-v\n--remove-orphans\n") {
		t.Fatalf("docker arguments %q: %v", args, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("instance directory remains")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
}
