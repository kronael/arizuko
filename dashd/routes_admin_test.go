package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/kronael/arizuko/core"
)

// seedFolderAdmin grants sub admin on exactly one folder (and creates it), so a
// caller can be an admin of B while holding nothing on A.
func seedFolderAdmin(t *testing.T, d *dash, sub, folder string) {
	t.Helper()
	seedFolder(t, d.routd, folder)
	seedAdmin(t, d.routd, sub, folder)
}

// handleRouteUpdate must gate on the EXISTING row's target folder, not only on
// the submitted one. Gating on the submitted target alone let an admin of
// folder B repoint any route id — including one targeting folder A they cannot
// see — into B, stealing the rule (BUGS "[SEC] routes: handleRouteUpdate
// authorizes new target, not existing row", 2026-06-18).
func TestRouteUpdate_deniesAdminOfOtherFolder(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolderAdmin(t, d, "mallory@x", "mallory")

	id := seedRoute(t, routd, core.Route{Seq: 1, Match: "m=1", Target: "team"})

	form := url.Values{"seq": {"1"}, "match": {"m=1"}, "target": {"mallory"}}.Encode()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("PATCH", "/dash/routes/"+strconv.FormatInt(id, 10), form, "mallory@x"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("PATCH of another folder's route = %d, want 403; body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool {
		return r.ID == id && r.Target == "team"
	}); n != 1 {
		t.Error("route was repointed despite the denial")
	}
}

// Moving a route to a folder the caller does not admin is refused even when the
// caller does admin the folder it currently targets — the gate covers both ends.
func TestRouteUpdate_deniesMoveToUnownedFolder(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolderAdmin(t, d, "mallory@x", "mallory")
	seedFolderAdmin(t, d, "bob@x", "bob")

	id := seedRoute(t, routd, core.Route{Seq: 2, Match: "m=2", Target: "mallory"})

	form := url.Values{"seq": {"2"}, "match": {"m=2"}, "target": {"bob"}}.Encode()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("PATCH", "/dash/routes/"+strconv.FormatInt(id, 10), form, "mallory@x"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("PATCH donating a route into an unowned folder = %d, want 403; body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool {
		return r.ID == id && r.Target == "mallory"
	}); n != 1 {
		t.Error("route was donated despite the denial")
	}
}

// The gate denies by scope, not blanket: an admin of the route's own folder
// still updates it in place.
func TestRouteUpdate_allowsOwnFolder(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolderAdmin(t, d, "mallory@x", "mallory")

	id := seedRoute(t, routd, core.Route{Seq: 3, Match: "m=3", Target: "mallory"})

	form := url.Values{"seq": {"7"}, "match": {"m=updated"}, "target": {"mallory"}}.Encode()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("PATCH", "/dash/routes/"+strconv.FormatInt(id, 10), form, "mallory@x"))
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent && w.Code != http.StatusSeeOther {
		t.Fatalf("PATCH of own folder's route = %d, want 2xx/303; body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool {
		return r.ID == id && r.Match == "m=updated"
	}); n != 1 {
		t.Error("own-folder update did not land")
	}
}

// handleRouteDelete shares the class: it resolves the folder from the stored
// row, so another folder's admin cannot delete a rule by guessing its id.
func TestRouteDelete_deniesAdminOfOtherFolder(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolderAdmin(t, d, "mallory@x", "mallory")

	id := seedRoute(t, routd, core.Route{Seq: 4, Match: "m=4", Target: "team"})

	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/routes/"+strconv.FormatInt(id, 10)+"/delete", "", "mallory@x"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("delete of another folder's route = %d, want 403; body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool { return r.ID == id }); n != 1 {
		t.Error("route was deleted despite the denial")
	}
}
