package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
)

func TestRotationSameInstant(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		config := Config{Enabled: true, DataDir: dir, MaxBytes: 1}
		a := New(config)
		for i := 0; i < 8; i++ {
			if i == 5 {
				if err := a.system.f.Close(); err != nil {
					t.Fatal(err)
				}
				a = New(config)
				if err := a.system.open(); err != nil {
					t.Fatal(err)
				}
			}
			a.EmitSystem(SystemEvent{ID: fmt.Sprintf("event-%d", i), Tool: "probe"})
			paths, err := filepath.Glob(filepath.Join(dir, "audit-system.jl*"))
			if err != nil {
				t.Fatal(err)
			}
			var all string
			for _, path := range paths {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				all += string(b)
			}
			for j := 0; j <= i; j++ {
				present := strings.Contains(all, fmt.Sprintf(`"id":"event-%d"`, j))
				if present != (j >= i-2) {
					t.Errorf("after %d, event %d present=%v: %s", i, j, present, all)
				}
			}
		}
		if err := a.system.f.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
