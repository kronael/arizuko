package runed

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The teardown deletes only the jumps that are there, then flushes and removes
// the tap's chain; a tap that never had a chain gets no -X.
func TestRulesDown(t *testing.T) {
	rec := &vmRecord{Tap: "tap0", GuestIP: "10.0.0.1", HostIP: "10.0.0.2", ProxyPort: 8080}
	jumps := tapJumps(rec)
	for _, tc := range []struct {
		name      string
		installed []int
		chain     bool
	}{
		{"absent", nil, false},
		{"partial", []int{1, 3}, true},
		{"installed", []int{0, 1, 2, 3}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			firewall := filepath.Join(dir, "firewall")
			state := filepath.Join(dir, "state")
			log := filepath.Join(dir, "log")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$FIREWALL_LOG"
op=$1
shift
case $op in
  -C) grep -Fxq -- "$*" "$FIREWALL_STATE" ;;
  -F) grep -Fxq -- "-N $1" "$FIREWALL_STATE" ;;
esac
`
			if err := os.WriteFile(firewall, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			var installed []string
			for _, i := range tc.installed {
				installed = append(installed, strings.Join(jumps[i], " "))
			}
			if tc.chain {
				installed = append(installed, "-N "+rec.Tap)
			}
			if err := os.WriteFile(state, []byte(strings.Join(installed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FIREWALL_LOG", log)
			t.Setenv("FIREWALL_STATE", state)
			q := &qemuRuntime{paths: qemuPaths{Firewall: firewall}}
			q.rulesDown(rec)
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for i, jump := range jumps {
				args := strings.Join(jump, " ")
				want = append(want, "-C "+args)
				for _, index := range tc.installed {
					if i == index {
						want = append(want, "-D "+args)
					}
				}
			}
			want = append(want, "-F "+rec.Tap)
			if tc.chain {
				want = append(want, "-X "+rec.Tap)
			}
			got := strings.Split(strings.TrimSpace(string(data)), "\n")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("commands = %q, want %q", got, want)
			}
		})
	}
}
