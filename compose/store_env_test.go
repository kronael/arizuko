package compose

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
)

func TestStoreRefusal(t *testing.T) {
	for _, tc := range []struct{ name, file, key, value string }{
		{"url missing", ".env", dynstore.EnvURL, ""},
		{"url invalid", ".env", dynstore.EnvURL, "http://store"},
		{"url malformed", ".env", dynstore.EnvURL, "https://[::1"},
		{"url host missing", ".env", dynstore.EnvURL, "https://:443"},
		{"region missing", ".env", dynstore.EnvRegion, ""},
		{"ca missing", ".env", dynstore.EnvCA, ""},
		{"ca base64", ".env", dynstore.EnvCA, "%%%"},
		{"ca pem", ".env", dynstore.EnvCA, "Zm9v"},
		{"daemon id", "env/routd.env", dynstore.EnvKeyID, ""},
		{"daemon secret", "env/routd.env", dynstore.EnvSecret, ""},
		{"operator id", "env/operator.env", dynstore.EnvKeyID, ""},
		{"operator secret", "env/operator.env", dynstore.EnvSecret, ""},
		{"daemon quoted empty", "env/routd.env", dynstore.EnvSecret, `""`},
		{"operator quoted empty", "env/operator.env", dynstore.EnvSecret, `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := seed(t, "")
			body := read(t, dir, tc.file)
			if tc.file != ".env" {
				body = dynstore.EnvKeyID + "=key\n"
				if tc.key == dynstore.EnvKeyID {
					body = dynstore.EnvSecret + "=secret\n"
				}
			}
			write(t, dir, tc.file, body+tc.key+"="+tc.value+"\n")
			write(t, dir, "services/pkg.yml", "services: {}\n")
			before := tree(t, dir)
			if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected %s refusal, got %v", tc.key, err)
			}
			if after := tree(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatal("files changed on refusal")
			}
		})
	}
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[path] = "directory"
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestInstalledAdapterRequiresCredential(t *testing.T) {
	dir := seed(t, "")
	if err := os.Remove(filepath.Join(dir, "env", "slakd.env")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Generate(dir, "test"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{dynstore.EnvKeyID, dynstore.EnvSecret} {
		write(t, dir, "env/slakd.env", key+"=partial\n")
		before := tree(t, dir)
		if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), "slakd.env") {
			t.Fatalf("uninstalled partial credential: %v", err)
		}
		if !reflect.DeepEqual(tree(t, dir), before) {
			t.Fatal("partial credential changed on refusal")
		}
	}
	if err := os.Remove(filepath.Join(dir, "env", "slakd.env")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "services/slakd.yml", "services: {}\n")
	if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), "slakd.env") {
		t.Fatalf("expected slakd refusal: %v", err)
	}
}

func TestSeededAssistantNameIsTheSharedDefault(t *testing.T) {
	env := map[string]string{}
	seedSharedDefaults(env)
	if env["ASSISTANT_NAME"] != core.DefaultAssistantName {
		t.Fatal(env)
	}
}

func TestNoOnboardSubtreeRouteWithoutASubtreeHandler(t *testing.T) {
	var bare bool
	for _, r := range coreProxydRoutes {
		if r.Path == "/onboard" {
			bare = true
		}
		if r.Path == "/onboard/" {
			t.Fatal("unexpected subtree route")
		}
	}
	if !bare {
		t.Fatal("missing onboard route")
	}
}

func TestEachDaemonKeepsItsOwnStoreKey(t *testing.T) {
	dir := seed(t, "")
	write(t, dir, "env/routd.env", dynstore.EnvKeyID+"=routd\n"+dynstore.EnvSecret+"=routdsecret\n")
	write(t, dir, "env/authd.env", dynstore.EnvKeyID+"=authd\n"+dynstore.EnvSecret+"=authdsecret\n")
	gen(t, dir)
	for _, daemon := range []string{"routd", "authd"} {
		body := read(t, dir, "env/"+daemon+".env")
		if !strings.Contains(body, dynstore.EnvKeyID+"="+daemon+"\n") || !strings.Contains(body, dynstore.EnvSecret+"="+daemon+"secret\n") {
			t.Fatalf("%s credentials changed: %s", daemon, body)
		}
	}
	if strings.Contains(read(t, dir, ".env"), dynstore.EnvKeyID) {
		t.Fatal("shared env contains credentials")
	}
}

func TestStoreEndpointReachesEveryDaemon(t *testing.T) {
	dir := seed(t, "")
	env, err := godotenv.Read(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	gen(t, dir)
	for _, daemon := range []string{"routd", "runed", "authd", "onbod", "dashd", "webd"} {
		body, err := godotenv.Read(filepath.Join(dir, "env", daemon+".env"))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{dynstore.EnvURL, dynstore.EnvRegion, dynstore.EnvCA} {
			if body[key] != env[key] {
				t.Errorf("%s: %s changed", daemon, key)
			}
		}
	}
}

func TestGenerateRejectsOperatorStoreOverride(t *testing.T) {
	for _, key := range []string{dynstore.EnvURL, dynstore.EnvCA} {
		t.Run(key, func(t *testing.T) {
			dir := seed(t, "")
			path := "env/operator.env"
			write(t, dir, path, read(t, dir, path)+key+"=different\n")
			before := tree(t, dir)
			if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected %s refusal: %v", key, err)
			}
			if !reflect.DeepEqual(before, tree(t, dir)) {
				t.Fatal("files changed on refusal")
			}
		})
	}
}

func TestGenerateRejectsMalformedCredentialFile(t *testing.T) {
	dir := seed(t, "")
	write(t, dir, "env/routd.env", "not a valid env line!\n")
	before := tree(t, dir)
	if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), "routd.env") {
		t.Fatalf("expected malformed env refusal: %v", err)
	}
	if !reflect.DeepEqual(before, tree(t, dir)) {
		t.Fatal("files changed on refusal")
	}
}
