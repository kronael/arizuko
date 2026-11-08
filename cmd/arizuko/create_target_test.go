package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestOwnerReadableEnrollment(t *testing.T) {
	if os.Getenv(dyntest.EnvEndpoint) == "" {
		t.Skip("set ARIZUKO_TEST_DYNAMO_ENDPOINT for owner-readable enrollment acceptance")
	}
	if os.Geteuid() != containerUID {
		binary, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		for _, path := range []string{filepath.Dir(dir), dir} {
			if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(dir, "arizuko.test")
		if err := os.WriteFile(path, binary, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sudo", "--preserve-env", "-u", "#1000", path,
			"-test.run=^TestOwnerReadableEnrollment$")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("UID-1000 enrollment: %v\n%s", err, out)
		}
		return
	}
	instance, dir := newInstance(t)
	cmdCreate([]string{instance})
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	for daemon, prefixes := range store.StoreReach {
		if len(prefixes) == 0 {
			continue
		}
		_, file := enrollee(daemon)
		path := filepath.Join(dir, "env", file)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 || info.Sys().(*syscall.Stat_t).Uid != 1000 {
			t.Fatalf("%s must be mode 0600 and owned by UID 1000", file)
		}
		pairOf(t, dir, file)
	}
	if _, err := os.Stat(filepath.Join(dir, "env", "store-admin.env")); !os.IsNotExist(err) {
		t.Fatalf("administrator credential file exists: %v", err)
	}
	env, err := view(dir)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	for key, value := range pairOf(t, dir, "routd.env") {
		t.Setenv(key, value)
	}
	db, err := routd.Open()
	if err != nil {
		t.Fatalf("owner boot with enrolled credentials: %v", err)
	}
	db.Close()
}

func TestSnapshotStartupWithSourceStopped(t *testing.T) {
	endpoint := os.Getenv("ARIZUKO_TEST_MOVE_ENDPOINT")
	container := os.Getenv("ARIZUKO_TEST_MOVE_SOURCE_CONTAINER")
	if endpoint == "" || container == "" {
		t.Skip("set ARIZUKO_TEST_MOVE_ENDPOINT, ARIZUKO_TEST_MOVE_CA and ARIZUKO_TEST_MOVE_SOURCE_CONTAINER for isolated move acceptance")
	}
	source := uniq("src")
	src := account(t, source)
	name := "routd.snapshot"
	makeTable(t, src, name)
	put(t, src, name, map[string]types.AttributeValue{
		"pk": s("source"), "sk": s("1"), "value": s("unchanged"),
	})
	before := scan(t, src, name)
	snap := snapshot(t, source)
	instance, dir := newInstance(t)
	cmdCreate([]string{instance})
	ca, err := os.ReadFile(os.Getenv("ARIZUKO_TEST_MOVE_CA"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := dyntest.Config(t)
	cfg.Endpoint, cfg.CA = endpoint, ca
	writeFile(t, filepath.Join(dir, ".env"), storeConfigEnv(cfg))
	out, err := enrollRun(t, instance, snap)
	if err != nil || !strings.Contains(out, name+" 1 equal") {
		t.Fatalf("copy: %v\n%s", err, out)
	}
	if got := scan(t, src, name); !reflect.DeepEqual(got, before) {
		t.Fatal("copy changed the source")
	}
	if out, err := exec.Command("sudo", "docker", "stop", container).CombinedOutput(); err != nil {
		t.Fatalf("stop source: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("sudo", "docker", "start", container).CombinedOutput(); err != nil {
			t.Errorf("restart source: %v\n%s", err, out)
			return
		}
		if err := dynstore.WaitTable(context.Background(), src, &name, 10*time.Second); err != nil {
			t.Errorf("source restart: %v", err)
			return
		}
		if got := scan(t, src, name); !reflect.DeepEqual(got, before) {
			t.Error("destination startup changed the source")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := src.ListTables(ctx, &dynamodb.ListTablesInput{}); err == nil {
		t.Fatal("source endpoint still answers after stop")
	}
	env, err := core.ReadInstanceEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	for key, value := range pairOf(t, dir, "routd.env") {
		t.Setenv(key, value)
	}
	db, err := routd.Open()
	if err != nil {
		t.Fatalf("destination owner boot with source stopped: %v", err)
	}
	defer db.Close()
	if _, ok := store.NewOver(db.Obj()).AllGroups()["main"]; !ok {
		t.Fatal("destination owner did not seed main")
	}
	t.Setenv("DATA_DIR", dir)
	path := generateCompose(dir, instance)
	yml, err := os.ReadFile(path)
	if err != nil || len(yml) == 0 {
		t.Fatalf("destination generation: %v", err)
	}
	if got := pairOf(t, dir, "routd.env")[dynstore.EnvURL]; got != endpoint {
		t.Fatalf("generated owner endpoint = %q, want %q", got, endpoint)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	daemon := uniq("s643-routd-")
	t.Cleanup(func() {
		if out, err := exec.Command("sudo", "docker", "rm", "-f", daemon).CombinedOutput(); err != nil {
			t.Errorf("remove destination daemon: %v\n%s", err, out)
		}
	})
	cmd := exec.Command("sudo", "docker", "run", "--pull=never", "-d",
		"--name", daemon, "--network", "host", "--log-driver=local",
		"--no-healthcheck", "--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()),
		"-v", dir+":/srv/app/home", "--env-file", filepath.Join(dir, "env", "routd.env"),
		"-e", "DATA_DIR=/srv/app/home", "-e", "APP_SRC_DIR=/opt/arizuko",
		"-e", "HOST_APP_DIR=/opt/arizuko", "-e", "AUTHD_URL=",
		"-e", "SECRETS_KEY="+strings.Repeat("1", 64),
		"-e", "LISTEN_ADDR="+address, "arizuko:latest", "routd")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start destination daemon: %v\n%s", err, out)
	}
	client := &http.Client{Timeout: time.Second}
	healthy := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/health")
		if err == nil {
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			healthy = response.StatusCode == http.StatusOK
			if healthy {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		out, err := exec.Command("sudo", "docker", "logs", daemon).CombinedOutput()
		t.Fatalf("destination daemon did not become healthy: %v\n%s", err, out)
	}
	dst, err := clientOf(env, env[dynstore.EnvKeyID], env[dynstore.EnvSecret])
	if err != nil {
		t.Fatal(err)
	}
	if got := scan(t, dst, name); !reflect.DeepEqual(got, before) {
		t.Fatal("destination differs after startup with source stopped")
	}
}

// Creation writes files; the owner's boot seeds main and its scheduled tasks.
func TestAFreshInstanceIsBornWithMain(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")

	cmdCreate([]string{"freshinst"})

	storeDir := filepath.Join(base, "arizuko_freshinst", "store")
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("create left no store dir for adapter files: %v", err)
	}

	db, err := routd.Open()
	if err != nil {
		t.Fatalf("routd.Open (the owner daemon makes the rows): %v", err)
	}
	defer db.Close() //nolint:errcheck // the test is ending
	s := store.NewOver(db.Obj())

	if _, ok := s.AllGroups()["main"]; !ok {
		t.Errorf("default group `main` missing; got %v", keysOf(s.AllGroups()))
	}
	if n := len(s.ListTasks("main", false)); n == 0 {
		t.Error("scheduled_tasks for `main` = 0, want the seeded defaults")
	}
}
