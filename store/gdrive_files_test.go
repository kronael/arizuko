package store

import (
	"errors"
	"strings"
	"testing"
)

func TestGDriveFileRoutes_Shape(t *testing.T) {
	rows, err := GDriveFileRoutes("1AbC_d-e", "eng")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		seq    int
		match  string
		target string
	}{
		{10, "chat_jid=gdrive:1AbC_d-e/c/* verb=message", "eng"},
		{10, "chat_jid=gdrive:1AbC_d-e/c/* verb=mention", "eng"},
		{100, "chat_jid=gdrive:1AbC_d-e/c/*", "eng#observe"},
		{100, "chat_jid=gdrive:1AbC_d-e", "eng#observe"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].Seq != w.seq || rows[i].Match != w.match || rows[i].Target != w.target {
			t.Errorf("row %d = %+v, want seq=%d match=%q target=%q", i, rows[i], w.seq, w.match, w.target)
		}
	}
}

// requireLiteralFileID fails when a rendered match does not carry the bound
// file id literally: no `*` anywhere before `/c/`, and the id itself is the
// text between `gdrive:` and the end of the JID or `/c/`.
func requireLiteralFileID(t *testing.T, fileID, match string) {
	t.Helper()
	jid, _, _ := strings.Cut(strings.TrimPrefix(match, "chat_jid="), " ")
	rest, ok := strings.CutPrefix(jid, "gdrive:")
	if !ok {
		t.Errorf("match %q is not a gdrive: JID", match)
		return
	}
	file, _, _ := strings.Cut(rest, "/c/")
	if strings.Contains(file, "*") {
		t.Errorf("match %q carries a wildcard before /c/", match)
	}
	if file != fileID {
		t.Errorf("match %q carries file %q, want the bound %q", match, file, fileID)
	}
}

func TestGDriveFileRoutes_FileIDIsLiteral(t *testing.T) {
	const fileID = "1AbC_d-e"
	rows, err := GDriveFileRoutes(fileID, "eng")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		requireLiteralFileID(t, fileID, r.Match)
	}
}

func TestGDriveFileRoutes_RefusesAnIDThatCouldWiden(t *testing.T) {
	for _, id := range []string{"", "*", "a*b", "a/b", "a/c/b", "a b", "a=b", "a\tb", "..", "a.b"} {
		if rows, err := GDriveFileRoutes(id, "eng"); err == nil {
			t.Errorf("GDriveFileRoutes(%q) = %+v, want error", id, rows)
		}
		if err := CheckGDriveFileID(id); err == nil {
			t.Errorf("CheckGDriveFileID(%q) accepted", id)
		}
	}
	if _, err := GDriveFileRoutes("abc", ""); err == nil {
		t.Error("empty folder accepted")
	}
}

func TestGDriveFileRoutes_NoSenderPredicate(t *testing.T) {
	rows, err := GDriveFileRoutes("abc", "eng")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if strings.Contains(r.Match, "sender=") {
			t.Errorf("row carries a sender predicate: %+v", r)
		}
	}
}

func TestGDriveFile_AddListRemove(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "eng")
	seedFolder(t, s, "eng/docs")

	f := GDriveFile{FileID: "fileA", OwnerFolder: "eng", TargetFolder: "eng/docs"}
	if err := s.AddGDriveFile(f); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListGDriveFiles("eng")
	if err != nil || len(list) != 1 || list[0].FileID != "fileA" ||
		list[0].OwnerFolder != "eng" || list[0].TargetFolder != "eng/docs" || list[0].CreatedAt.IsZero() {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if all, _ := s.ListGDriveFiles(""); len(all) != 1 {
		t.Fatalf("list all = %+v", all)
	}
	if other, _ := s.ListGDriveFiles("other"); len(other) != 0 {
		t.Fatalf("list other = %+v", other)
	}
	routes := s.AllRoutes()
	if len(routes) != 4 {
		t.Fatalf("seeded routes = %d, want 4: %+v", len(routes), routes)
	}
	for _, r := range routes {
		requireLiteralFileID(t, "fileA", r.Match)
		if !strings.HasPrefix(r.Target, "eng/docs") {
			t.Errorf("route target %q is not the binding's target folder", r.Target)
		}
	}
	if err := s.AddGDriveFile(f); !errors.Is(err, ErrGDriveFileExists) {
		t.Fatalf("duplicate bind err = %v", err)
	}
	if left := s.AllRoutes(); len(left) != 4 {
		t.Fatalf("a refused bind changed the routes: %+v", left)
	}
	if hit, err := s.RemoveGDriveFile("fileA", "other"); hit || err != nil {
		t.Fatalf("cross-folder revoke = %v, %v", hit, err)
	}
	if left := s.AllRoutes(); len(left) != 4 {
		t.Fatalf("a cross-folder revoke removed routes: %+v", left)
	}
	if hit, err := s.RemoveGDriveFile("nobody", "eng"); hit || err != nil {
		t.Fatalf("unknown revoke = %v, %v", hit, err)
	}
	hit, err := s.RemoveGDriveFile("fileA", "eng")
	if err != nil || !hit {
		t.Fatalf("revoke = %v, %v", hit, err)
	}
	if left := s.AllRoutes(); len(left) != 0 {
		t.Fatalf("routes left after revoke: %+v", left)
	}
	if left, _ := s.ListGDriveFiles(""); len(left) != 0 {
		t.Fatalf("file left after revoke: %+v", left)
	}
}

func TestGDriveFile_RevokeKeepsAnotherFilesRoutes(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "eng")
	for _, id := range []string{"fileA", "fileB"} {
		if err := s.AddGDriveFile(GDriveFile{FileID: id, OwnerFolder: "eng", TargetFolder: "eng"}); err != nil {
			t.Fatal(err)
		}
	}
	if hit, err := s.RemoveGDriveFile("fileA", "eng"); err != nil || !hit {
		t.Fatalf("revoke = %v, %v", hit, err)
	}
	routes := s.AllRoutes()
	if len(routes) != 4 {
		t.Fatalf("routes = %d, want fileB's 4: %+v", len(routes), routes)
	}
	for _, r := range routes {
		requireLiteralFileID(t, "fileB", r.Match)
	}
}

func TestGDriveFile_AddRefusesWhatCannotRender(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "eng")
	for _, f := range []GDriveFile{
		{FileID: "*", OwnerFolder: "eng", TargetFolder: "eng"},
		{FileID: "a/b", OwnerFolder: "eng", TargetFolder: "eng"},
		{FileID: "abc", OwnerFolder: "", TargetFolder: "eng"},
		{FileID: "abc", OwnerFolder: "eng", TargetFolder: ""},
	} {
		if err := s.AddGDriveFile(f); err == nil {
			t.Errorf("AddGDriveFile(%+v) accepted", f)
		}
	}
	if all, _ := s.ListGDriveFiles(""); len(all) != 0 {
		t.Fatalf("a refused bind left a file: %+v", all)
	}
	if routes := s.AllRoutes(); len(routes) != 0 {
		t.Fatalf("a refused bind left routes: %+v", routes)
	}
}
