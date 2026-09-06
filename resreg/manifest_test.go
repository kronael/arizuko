package resreg

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
)

// The manifest helpers decide what a `.yaml` document means before any
// transaction opens: which owner DB it targets, which folders it names, and
// whether every one of those folders exists. None of them had a test.
// SplitDocuments, SubsystemOf, KnownFolders, ValidateFolderRefs,
// ManifestScopes and mergeScopes were all 0%.

// SplitDocuments

// The split is a yaml.v3 decoder loop, not a string split on "---", so a
// literal "---" inside a scalar cannot be mistaken for a boundary. That is the
// whole reason the function exists rather than strings.Split.
func TestSplitDocumentsUsesTheYAMLBoundaryNotTheCharacters(t *testing.T) {
	docs, err := SplitDocuments([]byte("a: 1\n"))
	if err != nil {
		t.Fatalf("single document: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("a single-document file split into %d", len(docs))
	}

	docs, err = SplitDocuments([]byte("a: 1\n---\nb: 2\n---\nc: 3\n"))
	if err != nil {
		t.Fatalf("three documents: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("got %d documents, want 3", len(docs))
	}
	for i, want := range []string{"a: 1", "b: 2", "c: 3"} {
		if !strings.Contains(string(docs[i]), want) {
			t.Errorf("document %d = %q, want it to carry %q", i, docs[i], want)
		}
	}

	// A "---" inside a value. String-splitting would cut this document in two
	// and the second half would fail to parse as a manifest.
	docs, err = SplitDocuments([]byte("note: |\n  before\n  ---\n  after\n"))
	if err != nil {
		t.Fatalf("literal --- in a scalar: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("a literal --- inside a block scalar split the document into %d", len(docs))
	}
	if !strings.Contains(string(docs[0]), "after") {
		t.Errorf("the tail of the scalar was lost: %q", docs[0])
	}

	if docs, err := SplitDocuments(nil); err != nil || len(docs) != 0 {
		t.Errorf("empty input gave (%d docs, %v), want (0, nil)", len(docs), err)
	}

	if _, err := SplitDocuments([]byte("a: [1, 2\n")); err == nil {
		t.Error("malformed YAML was split without an error")
	}
}

// SubsystemOf

// twoSubsystems registers one resource in each of two owner DBs so the mixing
// rule has something to catch.
func twoSubsystems(t *testing.T) {
	t.Helper()
	reset()
	Register(Resource{
		Name: "testrows", Table: "testrows", DB: testSubsystem,
		RowType: reflect.TypeFor[TestRow](), PKFields: []string{"Kind", "Name"},
		Scope: ScopeSpec{Field: "Kind"},
	})
	Register(Resource{
		Name: "otherrows", Table: "otherrows", DB: "other",
		RowType: reflect.TypeFor[TestRow](), PKFields: []string{"Kind", "Name"},
		Scope: ScopeSpec{Field: "Kind"},
	})
}

// Apply resolves each document to ONE owner DB. A document mixing two would
// be written half into each, or all into the wrong one.
func TestSubsystemOfRefusesAnEmptyOrMixedDocument(t *testing.T) {
	twoSubsystems(t)

	got, err := SubsystemOf(map[string]any{"testrows": []TestRow{}})
	if err != nil {
		t.Fatalf("a single-subsystem document was refused: %v", err)
	}
	if got != testSubsystem {
		t.Errorf("subsystem = %q, want %q", got, testSubsystem)
	}

	if _, err := SubsystemOf(map[string]any{}); err == nil {
		t.Error("an empty document inferred a subsystem out of nothing")
	}

	_, err = SubsystemOf(map[string]any{"testrows": []TestRow{}, "otherrows": []TestRow{}})
	if err == nil {
		t.Fatal("a document mixing two owner DBs was accepted")
	}
	// The operator has to know WHICH two to split.
	if !strings.Contains(err.Error(), testSubsystem) || !strings.Contains(err.Error(), "other") {
		t.Errorf("the error must name both subsystems, got %q", err)
	}

	_, err = SubsystemOf(map[string]any{"nosuchresource": []TestRow{}})
	if err == nil {
		t.Fatal("an unknown resource name inferred a subsystem")
	}
	if !strings.Contains(err.Error(), "nosuchresource") {
		t.Errorf("the error must name the unknown resource, got %q", err)
	}
}

// ManifestScopes / mergeScopes

// The rollback feeds ManifestScopes back as PruneScopes. A scope it misses is
// a scope the rollback leaves behind.
func TestManifestScopesIsTheSortedUnionOfEveryMentionedRow(t *testing.T) {
	twoSubsystems(t)

	// The rows are the TYPED slice ParseYAML produces, not raw maps:
	// manifestScopes reads the scope column by struct field index.
	got := ManifestScopes(testSubsystem, map[string]any{
		"testrows": []TestRow{
			{Kind: "b", Name: "x", Value: "1"},
			{Kind: "a", Name: "y", Value: "2"},
			{Kind: "b", Name: "z", Value: "3"},
		},
		// Another subsystem's rows must not contribute.
		"otherrows": []TestRow{{Kind: "zzz", Name: "n", Value: "v"}},
	})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ManifestScopes = %v, want the sorted deduplicated [a b]", got)
	}

	if got := ManifestScopes(testSubsystem, map[string]any{}); len(got) != 0 {
		t.Errorf("an empty manifest gave %v", got)
	}
	if got := ManifestScopes("nosuch", map[string]any{"testrows": []TestRow{}}); len(got) != 0 {
		t.Errorf("an unknown subsystem gave %v", got)
	}
}

func TestMergeScopesIsASortedUnion(t *testing.T) {
	cases := []struct {
		a, b, want []string
	}{
		{[]string{"b", "a"}, nil, []string{"b", "a"}}, // b empty returns a untouched
		{[]string{"b", "a"}, []string{}, []string{"b", "a"}},
		{[]string{"b"}, []string{"a"}, []string{"a", "b"}},
		{[]string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{nil, []string{"b", "a"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		got := mergeScopes(c.a, c.b)
		if len(got) != len(c.want) {
			t.Errorf("mergeScopes(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("mergeScopes(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
				break
			}
		}
	}
}

// KnownFolders / ValidateFolderRefs

// GroupRow stands in for the `groups` resource: the rows that DEFINE which
// folders exist.
type GroupRow struct {
	Folder string `db:"folder" yaml:"folder"`
	JID    string `db:"jid"    yaml:"jid"`
}

const folderSchema = `
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT);
CREATE TABLE groups (folder TEXT PRIMARY KEY, jid TEXT NOT NULL);
CREATE TABLE testrows (
  kind  TEXT NOT NULL,
  name  TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (kind, name)
);
`

func folderEngine(t *testing.T) *sql.DB {
	t.Helper()
	reset()
	db := dbtest.Open(t)
	if _, err := db.Exec(folderSchema); err != nil {
		t.Fatal(err)
	}
	Register(Resource{
		Name: GroupsResource, Table: "groups", DB: testSubsystem,
		RowType: reflect.TypeFor[GroupRow](), PKFields: []string{"Folder"},
		Scope: ScopeSpec{Field: "Folder"},
	})
	Register(Resource{
		Name: "testrows", Table: "testrows", DB: testSubsystem,
		RowType: reflect.TypeFor[TestRow](), PKFields: []string{"Kind", "Name"},
		Scope: ScopeSpec{Field: "Kind"},
	})
	return db
}

// A restore presents every document at once, so a folder-scoped row may sit in
// a different document from the groups row that declares it. KnownFolders is
// the union of what is live and what the restore declares.
func TestKnownFoldersUnionsTheLiveDBAndEveryManifest(t *testing.T) {
	db := folderEngine(t)
	if _, err := db.Exec(`INSERT INTO groups (folder, jid) VALUES ('live-one','x:1')`); err != nil {
		t.Fatal(err)
	}

	got, err := KnownFolders(db,
		map[string]any{GroupsResource: []GroupRow{{Folder: "doc-a", JID: "x:2"}}},
		map[string]any{GroupsResource: []GroupRow{{Folder: "doc-b", JID: "x:3"}}},
	)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	for _, want := range []string{"live-one", "doc-a", "doc-b"} {
		if !got[want] {
			t.Errorf("%q missing from the known set %v", want, got)
		}
	}
	if got["never-declared"] {
		t.Error("KnownFolders invented a folder")
	}

	// Live-only, no manifests at all.
	got, err = KnownFolders(db)
	if err != nil {
		t.Fatal(err)
	}
	if !got["live-one"] || len(got) != 1 {
		t.Errorf("live-only = %v, want just live-one", got)
	}
}

func TestKnownFoldersErrorsWhenGroupsIsNotRegistered(t *testing.T) {
	reset()
	db := dbtest.Open(t)
	if _, err := KnownFolders(db); err == nil {
		t.Fatal("KnownFolders succeeded with no groups resource; every folder would read as unknown")
	}
}

// The missing-group rule. Two of the real references are FK'd and would fail
// SQLite's own check mid-write; the ones with no FK produce a silently
// orphaned row instead, which is the gap this closes — so the check has to
// happen before the transaction, not inside it.
func TestValidateFolderRefsRefusesARowNamingAnUnknownFolder(t *testing.T) {
	folderEngine(t)
	known := map[string]bool{"acme": true}

	err := ValidateFolderRefs(testSubsystem, map[string]any{
		"testrows": []TestRow{
			{Kind: "acme", Name: "ok", Value: "1"},
			{Kind: "ghost", Name: "bad", Value: "2"},
		},
	}, known)
	if err == nil {
		t.Fatal("a row naming a folder that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "testrows") {
		t.Errorf("the error must name the resource and the folder, got %q", err)
	}

	if err := ValidateFolderRefs(testSubsystem, map[string]any{
		"testrows": []TestRow{{Kind: "acme", Name: "ok", Value: "1"}},
	}, known); err != nil {
		t.Errorf("a row naming a known folder was refused: %v", err)
	}

	// The empty scope is exempt on purpose: network_rules carries folder="" for
	// instance-global rules, which names no group by design.
	if err := ValidateFolderRefs(testSubsystem, map[string]any{
		"testrows": []TestRow{{Kind: "", Name: "global", Value: "1"}},
	}, known); err != nil {
		t.Errorf("the instance-global empty scope was refused: %v", err)
	}

	// A resource the document does not mention is not checked.
	if err := ValidateFolderRefs(testSubsystem, map[string]any{}, known); err != nil {
		t.Errorf("an empty manifest was refused: %v", err)
	}
}
