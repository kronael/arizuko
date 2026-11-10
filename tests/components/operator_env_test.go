package components

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The operator's store credential lives in env/operator.env so that no daemon
// receives it (core.LoadInstanceEnv). Only the host CLI may load that file; a
// daemon that calls these loaders runs with the operator's rights and fails on
// an instance whose env files are root-owned.
func TestOnlyTheCLILoadsTheOperatorEnv(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	loader := regexp.MustCompile(`core\.(LoadConfigFrom|LoadInstanceEnv|ReadInstanceEnv)\(`)
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "tmp" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) != "main.go" || strings.HasPrefix(rel, "cmd/arizuko/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loader.Match(src) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("daemons load the operator env: %v", offenders)
	}
}
