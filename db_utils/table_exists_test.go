package db_utils

// TableExists is the one catalog probe in the tree, so it is tested on both
// engines and against the fixture's own per-engine catalog query — an
// independent oracle, written in the other catalog's terms, rather than the
// production statement checked against itself.

import "testing"

func TestTableExistsAgreesWithTheEnginesOwnCatalog(t *testing.T) {
	forEachEngine(t, func(t *testing.T, e *engineFixture) {
		db := e.open(t, t.Name())

		got, err := TableExists(db, "widgets")
		if err != nil {
			t.Fatalf("TableExists on an empty database: %v", err)
		}
		if got {
			t.Fatal("TableExists says widgets is there before anything created it")
		}
		if e.tableExists(t, db, "widgets") {
			t.Fatal("the fixture disagrees: widgets is there before anything created it")
		}

		if _, err := db.Exec("CREATE TABLE widgets (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatalf("create widgets: %v", err)
		}
		got, err = TableExists(db, "widgets")
		if err != nil {
			t.Fatalf("TableExists: %v", err)
		}
		if !got {
			t.Error("TableExists misses a table that is there, so a legacy carry would skip its rows")
		}
		if !e.tableExists(t, db, "widgets") {
			t.Error("the fixture disagrees: widgets is not there after CREATE TABLE")
		}

		if _, err := db.Exec("DROP TABLE widgets"); err != nil {
			t.Fatalf("drop widgets: %v", err)
		}
		got, err = TableExists(db, "widgets")
		if err != nil {
			t.Fatalf("TableExists after DROP: %v", err)
		}
		if got {
			t.Error("TableExists still sees a dropped table, so a carry would read rows that are gone")
		}
	})
}

// The name is matched exactly. openOwned proves a path names the owner database
// it claims to by finding one table, and a probe that matched loosely would
// accept a neighbour's database whose table differs only in case.
func TestTableExistsMatchesTheNameExactly(t *testing.T) {
	forEachEngine(t, func(t *testing.T, e *engineFixture) {
		db := e.open(t, t.Name())
		if _, err := db.Exec("CREATE TABLE invites (ref TEXT PRIMARY KEY)"); err != nil {
			t.Fatalf("create invites: %v", err)
		}
		for _, name := range []string{"INVITES", "invite", "invitess", "invites_legacy"} {
			got, err := TableExists(db, name)
			if err != nil {
				t.Fatalf("TableExists(%q): %v", name, err)
			}
			if got {
				t.Errorf("TableExists(%q) is true and only `invites` exists", name)
			}
		}
	})
}

// A view carries the name of a table and none of its guarantees. openOwned's
// answer decides whether a daemon boots against the database it was pointed at.
func TestTableExistsRejectsAViewOfThatName(t *testing.T) {
	forEachEngine(t, func(t *testing.T, e *engineFixture) {
		db := e.open(t, t.Name())
		if _, err := db.Exec("CREATE TABLE acl_rows (principal TEXT)"); err != nil {
			t.Fatalf("create acl_rows: %v", err)
		}
		if _, err := db.Exec("CREATE VIEW acl AS SELECT principal FROM acl_rows"); err != nil {
			t.Fatalf("create the acl view: %v", err)
		}
		got, err := TableExists(db, "acl")
		if err != nil {
			t.Fatalf("TableExists: %v", err)
		}
		if got {
			t.Error("a view answers as a table, so openOwned would accept a database that only looks like routd's")
		}
	})
}
