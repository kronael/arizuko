package main

import (
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/store"
)

func seedStoreEnv(t *testing.T, dir string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	f, err := os.OpenFile(filepath.Join(dir, ".env"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("DYNAMODB_URL=https://store.example.com\nSTORE_CA=" + base64.StdEncoding.EncodeToString(ca) + "\nAWS_REGION=us-east-1\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := dyntest.Config(t)
	for daemon, owners := range store.StoreReach {
		if len(owners) == 0 {
			continue
		}
		if daemon == "cmd/arizuko" {
			daemon = "operator"
		}
		mustWrite(t, filepath.Join(dir, "env", daemon+".env"), "AWS_ACCESS_KEY_ID="+cfg.KeyID+"\nAWS_SECRET_ACCESS_KEY="+cfg.Secret+"\n")
	}
}

// seedSyncInstance builds an instance whose services/ holds installed copies,
// with each package of sources written to its own directory, sourceDir names it.
func seedSyncInstance(t *testing.T, name string, sources, installed map[string]string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_"+name)
	t.Setenv("DATA_DIR", dataDir)
	seedCoreSource(t, filepath.Join(base, "app"))

	srcDir := filepath.Join(base, "src")
	svcDir := filepath.Join(dataDir, "services")
	for dir, files := range map[string]map[string]string{srcDir: sources, svcDir: installed} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, body := range files {
			path := filepath.Join(dir, f)
			if dir == srcDir && strings.HasSuffix(f, ".yml") && !strings.Contains(f, "/") {
				packageName := strings.TrimSuffix(f, ".yml")
				path = filepath.Join(dir, packageName, f)
				if _, declared := files[packageName+"/arizuko.yml"]; !declared {
					mustWrite(t, filepath.Join(dir, packageName, "arizuko.yml"), "name: "+packageName+"\n")
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	env := "HOST_APP_DIR=" + filepath.Join(base, "app") + "\nAWS_REGION=us-east-1\n"
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	seedStoreEnv(t, dataDir)
	// godotenv.Load (core.LoadConfigFrom) never overrides an already-set
	// process env var, so an earlier test's HOST_APP_DIR would otherwise leak
	// into this one. t.Setenv both pins the correct value up front and
	// restores it when this test ends.
	t.Setenv("HOST_APP_DIR", filepath.Join(base, "app"))
	return dataDir
}

// sourceDir is the directory seedSyncInstance wrote package name to.
func sourceDir(name string) string {
	return filepath.Join(os.Getenv("ARIZUKO_DATA_DIR"), "src", name)
}
