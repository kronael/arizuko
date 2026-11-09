package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateAgentLimit(t *testing.T) {
	for _, body := range []string{
		"MAX_CONCURRENT_CONTAINERS=1\n",
		"# limit\n export MAX_CONCURRENT_CONTAINERS = '2' # agents\r\nOTHER=keep\r\n",
		"MAX_CONCURRENT_AGENTS=3\n",
		"OTHER=keep\n",
	} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".env")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := MigrateEnv(dir); err != nil {
					t.Fatal(err)
				}
			}
			got := read(t, dir, ".env")
			want := strings.ReplaceAll(body, "MAX_CONCURRENT_CONTAINERS", "MAX_CONCURRENT_AGENTS")
			if got != want {
				t.Fatalf("env=%q want=%q", got, want)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("env permissions: %v %v", info, err)
			}
		})
	}
	for _, body := range []string{
		"MAX_CONCURRENT_CONTAINERS=1\nMAX_CONCURRENT_AGENTS=2\n",
		"MAX_CONCURRENT_CONTAINERS=1\nMAX_CONCURRENT_CONTAINERS=2\n",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := MigrateEnv(dir); err == nil {
			t.Fatal("ambiguous cap accepted")
		}
		if got := read(t, dir, ".env"); got != body {
			t.Fatal("refused migration changed .env")
		}
	}
}

func TestGenerateRequiresAgentLimitMigration(t *testing.T) {
	dir := seed(t, "MAX_CONCURRENT_CONTAINERS=2\n")
	if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), "arizuko migrate") {
		t.Fatalf("stale cap accepted: %v", err)
	}
	if err := MigrateEnv(dir); err != nil {
		t.Fatal(err)
	}
	gen(t, dir)
	for _, daemon := range []string{"routd", "runed"} {
		if env := read(t, dir, "env/"+daemon+".env"); !strings.Contains(env, "MAX_CONCURRENT_AGENTS=2") {
			t.Fatalf("%s did not receive migrated cap", daemon)
		}
	}
}
