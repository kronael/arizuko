package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestGDriveAddListRm(t *testing.T) {
	s := routdStore(t)
	if err := s.PutGroup(core.Group{Folder: "eng"}); err != nil {
		t.Fatal(err)
	}
	cli := s.AsCLI("alice")
	var out bytes.Buffer

	if err := runGDriveAdd(cli, "1AbC_d-e", "eng", &out); err != nil {
		t.Fatalf("add: %v", err)
	}
	if n := len(s.AllRoutes()); n != 4 {
		t.Fatalf("seeded routes = %d, want 4", n)
	}
	if actor, sub, surface := routeAudit(t, s, "gdrive_file.register"); actor != "cli:alice" || sub != "alice" || surface != "cli" {
		t.Errorf("register audit = %q %q %q", actor, sub, surface)
	}

	out.Reset()
	if err := runGDriveList(cli, "", &out); err != nil || !strings.Contains(out.String(), "1AbC_d-e") {
		t.Fatalf("list err=%v out=%q", err, out.String())
	}
	out.Reset()
	if err := runGDriveList(cli, "other", &out); err != nil || strings.Contains(out.String(), "1AbC_d-e") {
		t.Fatalf("list for another folder err=%v out=%q", err, out.String())
	}

	if err := runGDriveAdd(cli, "1AbC_d-e", "eng", &out); err == nil {
		t.Fatal("a second add of the same file should be refused")
	}
	if n := len(s.AllRoutes()); n != 4 {
		t.Fatalf("a refused add changed the routes: %d", n)
	}

	if err := runGDriveRm(s.AsCLI("bob"), "1AbC_d-e", &out); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if n := len(s.AllRoutes()); n != 0 {
		t.Fatalf("routes left after rm = %d", n)
	}
	if actor, _, _ := routeAudit(t, s, "gdrive_file.revoke"); actor != "cli:bob" {
		t.Errorf("revoke audit actor = %q, want cli:bob", actor)
	}
	if err := runGDriveRm(cli, "1AbC_d-e", &out); err == nil {
		t.Fatal("rm of an unbound file should fail")
	}
}

func TestGDriveAddRefuses(t *testing.T) {
	s := routdStore(t)
	if err := s.PutGroup(core.Group{Folder: "eng"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, tc := range []struct{ file, folder string }{
		{"f1", "missing"},
		{"f1", "../eng"},
		{"*", "eng"},
		{"a/b", "eng"},
	} {
		if err := runGDriveAdd(s, tc.file, tc.folder, &out); err == nil {
			t.Errorf("add %q %q should be refused", tc.file, tc.folder)
		}
	}
	if n := len(s.AllRoutes()); n != 0 {
		t.Fatalf("a refused add seeded routes: %d", n)
	}
}
