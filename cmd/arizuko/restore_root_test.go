package main

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreRootConfinement(t *testing.T) {
	for _, name := range []string{"link/victim", "victim"} {
		t.Run(name, func(t *testing.T) {
			group, outside := t.TempDir(), t.TempDir()
			victim := filepath.Join(outside, "victim")
			if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			link, target := filepath.Join(group, "link"), outside
			if name == "victim" {
				link, target = filepath.Join(group, "victim"), victim
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			err := extractTarEntry(group, "atlas", &tar.Header{Name: "atlas/" + name, Typeflag: tar.TypeReg, Mode: 0600}, strings.NewReader("overwrite"))
			data, readErr := os.ReadFile(victim)
			if readErr != nil || err == nil || string(data) != "original" {
				t.Fatalf("err=%v read=%v outside=%q", err, readErr, data)
			}
		})
	}
	for _, name := range []string{"..notes", "nested/file"} {
		group := t.TempDir()
		err := extractTarEntry(group, "atlas", &tar.Header{Name: "atlas/" + name, Typeflag: tar.TypeReg, Mode: 0600}, strings.NewReader("valid"))
		data, readErr := os.ReadFile(filepath.Join(group, name))
		if err != nil || readErr != nil || string(data) != "valid" {
			t.Fatalf("%s: err=%v read=%v data=%q", name, err, readErr, data)
		}
	}
}
