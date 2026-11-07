package core

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Both of LoadInstanceEnv's writes are one-way: godotenv puts the instance's
// .env into the process env and nothing unsets it, and LoadConfigFrom pins
// DATA_DIR only when it is empty. So the FIRST instance a process touches owns
// every var, and a second one silently inherits them. Nothing loops instances
// today, which is why this stayed invisible (BUGS L50). The warning is what
// makes the day it happens say so.
func TestLoadInstanceEnvWarnsOnASecondInstanceInOneProcess(t *testing.T) {
	logs := captureWarnings(t)
	restore := loadedInstanceDir
	t.Cleanup(func() { loadedInstanceDir = restore })

	loadedInstanceDir = ""
	if err := LoadInstanceEnv(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("the first instance must be silent, logged: %s", got)
	}

	second := t.TempDir()
	if err := LoadInstanceEnv(second); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if !strings.Contains(got, "second instance") {
		t.Fatalf("no warning for a second instance, logged: %q", got)
	}
	if !strings.Contains(got, second) {
		t.Errorf("the warning names neither directory, so an operator cannot act on it: %q", got)
	}
}

// The same directory twice is every normal CLI command — two doors of one
// process loading one instance. It must stay silent, or the warning is noise
// and nobody reads it when it matters.
func TestLoadInstanceEnvIsSilentForTheSameInstanceTwice(t *testing.T) {
	logs := captureWarnings(t)
	restore := loadedInstanceDir
	t.Cleanup(func() { loadedInstanceDir = restore })

	loadedInstanceDir = ""
	dir := t.TempDir()
	if err := LoadInstanceEnv(dir); err != nil {
		t.Fatal(err)
	}
	if err := LoadInstanceEnv(dir); err != nil {
		t.Fatal(err)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("one instance loaded twice must be silent, logged: %s", got)
	}
}

// captureWarnings points the default logger at a buffer for one test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestOperatorCannotRedirectStore(t *testing.T) {
	for _, key := range []string{"DYNAMODB_URL", "STORE_CA"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "env"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=same\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "env", "operator.env")
			if err := os.WriteFile(path, []byte(key+"=same\nAWS_ACCESS_KEY_ID=operator\n"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadInstanceEnv(dir)
			if err != nil || got["AWS_ACCESS_KEY_ID"] != "operator" {
				t.Fatalf("matching values: %v %v", got, err)
			}
			if err := os.WriteFile(path, []byte(key+"=different\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, "original")
			if err := LoadInstanceEnv(dir); err == nil {
				t.Fatal("accepted override")
			}
			if got := os.Getenv(key); got != "original" {
				t.Fatalf("environment mutated: %s", got)
			}
		})
	}
}
