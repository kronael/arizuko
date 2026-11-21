package runed

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"syscall"
	"testing"
	"time"
)

func prefixScanners() int {
	var b bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&b, 2)
	return strings.Count(b.String(), "newPrefixWriter.func1")
}

func TestVirtiofsdClosesLogOnExit(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "virtiofsd")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\ntouch \"${1#--socket-path=}\"\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"failed start", "early exit", "normal exit", "record failure"} {
		t.Run(name, func(t *testing.T) {
			before := prefixScanners()
			turnDir := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
			q := &qemuRuntime{paths: qemuPaths{Virtiofsd: fixture, Grace: time.Second}}
			rec := &vmRecord{TurnDir: turnDir, Folder: "demo"}
			switch name {
			case "failed start":
				q.paths.Virtiofsd = filepath.Join(dir, "missing")
			case "early exit":
				q.paths.Virtiofsd = "/bin/false"
			case "record failure":
				if err := os.WriteFile(turnDir, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := q.startVirtiofsd(rec, filepath.Join(dir, "socket_"+strings.ReplaceAll(name, " ", "_")), dir, false)
			if name == "normal exit" && err != nil {
				t.Fatal(err)
			}
			if name != "normal exit" && err == nil {
				t.Fatal("expected startup failure")
			}
			if len(rec.VirtiofsdPids) > 0 {
				pid := rec.VirtiofsdPids[0]
				if name == "normal exit" {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) && syscall.Kill(pid, 0) == nil {
					time.Sleep(10 * time.Millisecond)
				}
				if syscall.Kill(pid, 0) == nil {
					t.Errorf("child %d was not reaped", pid)
					_ = syscall.Kill(pid, syscall.SIGKILL)
					_, _ = syscall.Wait4(pid, nil, 0, nil)
				}
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && prefixScanners() != before {
				time.Sleep(10 * time.Millisecond)
			}
			if got := prefixScanners(); got != before {
				t.Errorf("log scanners = %d, want %d", got, before)
			}
		})
	}
}
