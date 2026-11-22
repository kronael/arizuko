package main

import (
	"archive/tar"
	"bytes"
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
			root, err := os.OpenRoot(group)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = extractTarEntry(root, "atlas", &tar.Header{Name: "atlas/" + name, Typeflag: tar.TypeReg, Mode: 0600}, strings.NewReader("overwrite"))
			data, readErr := os.ReadFile(victim)
			if readErr != nil || err == nil || string(data) != "original" {
				t.Fatalf("err=%v read=%v outside=%q", err, readErr, data)
			}
		})
	}
	for _, name := range []string{"..notes", "nested/file"} {
		group := t.TempDir()
		root, err := os.OpenRoot(group)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		err = extractTarEntry(root, "atlas", &tar.Header{Name: "atlas/" + name, Typeflag: tar.TypeReg, Mode: 0600}, strings.NewReader("valid"))
		data, readErr := os.ReadFile(filepath.Join(group, name))
		if err != nil || readErr != nil || string(data) != "valid" {
			t.Fatalf("%s: err=%v read=%v data=%q", name, err, readErr, data)
		}
	}
}

func TestRestoreGroupRootSymlink(t *testing.T) {
	dataDir, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "groups"), 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "MEMORY.md")
	if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "groups", "atlas")); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	if err := tw.WriteHeader(&tar.Header{Name: "atlas/MEMORY.md", Mode: 0600, Size: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("overwrite")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := extractGroups(dataDir, []string{"atlas"}, data.Bytes(), true, nil)
	got, readErr := os.ReadFile(victim)
	if err == nil || readErr != nil || string(got) != "original" {
		t.Fatalf("err=%v read=%v outside=%q", err, readErr, got)
	}
}
