package runed

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRulesDown(t *testing.T) {
	rec := &vmRecord{Tap: "tap0", GuestIP: "10.0.0.1", HostIP: "10.0.0.2", ProxyPort: 8080}
	rules := tapRules(rec)
	for _, tc := range []struct {
		name      string
		installed []int
	}{
		{"absent", nil},
		{"partial", []int{1, 4}},
		{"installed", []int{0, 1, 2, 3, 4, 5}},
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
if [ "$op" = -C ]; then
  grep -Fxq -- "$*" "$FIREWALL_STATE"
fi
`
			if err := os.WriteFile(firewall, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			var installed []string
			for _, i := range tc.installed {
				installed = append(installed, strings.Join(rules[i], " "))
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
			for i, rule := range rules {
				args := strings.Join(rule, " ")
				want = append(want, "-C "+args)
				for _, index := range tc.installed {
					if i == index {
						want = append(want, "-D "+args)
					}
				}
			}
			got := strings.Split(strings.TrimSpace(string(data)), "\n")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("commands = %q, want %q", got, want)
			}
		})
	}
}
