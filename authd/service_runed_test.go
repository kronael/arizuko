package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runed authenticates nobody: it verifies its callers against authd's JWKs and
// never exchanges AUTHD_SERVICE_KEY, so service:runed needs no grant. The
// broker ceiling that used to sit in serviceGrants was the last remnant of the
// per-spawn capability token deleted in BUGS B1 (L8).
//
// This reads SOURCE because the defect is an absence: a value-level test cannot
// see that nothing mints. If runed ever does call auth.ServiceToken, this fails
// and the grant has to come back with the scopes that call needs.
func TestRunedMintsNoServiceToken(t *testing.T) {
	if g, ok := serviceGrants["service:runed"]; ok {
		t.Errorf("service:runed carries grant %v, but runed mints no token", g)
	}

	var callers []string
	for _, dir := range []string{"../runed", "../runed/cmd/runed"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, rerr := os.ReadFile(filepath.Join(dir, name))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if strings.Contains(string(src), "auth.ServiceToken(") {
				callers = append(callers, filepath.Join(dir, name))
			}
		}
	}
	if len(callers) > 0 {
		t.Errorf("runed exchanges a service token in %v — serviceGrants must grant "+
			"service:runed the scopes those calls present, or they 403", callers)
	}
}
