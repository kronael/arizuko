package main

import (
	"net/http/httptest"
	"testing"

	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// TestTimedOpenAPI_AdvertisesOnlyWhatItMounts is the F32 guard, and it is the
// F21/F27 shape applied to the daemon those guards structurally cannot see:
// both of those live in package routd and probe the mux routd builds. timed is
// `package main`, so nothing can import it — the assertion is single-sourced in
// resreg/resregtest and called from here with timed's real mux and real list.
//
// Two halves, because the first alone would be VACUOUS while
// timedOpenAPIResources is empty — zero advertised paths means zero assertions.
// The second half is the anchor: it renders the resource F32 wrongly named and
// pins that timed serves none of it, and it t.Fatals if that renders nothing.
func TestTimedOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	mux := newTimedMux(&dashServer{})

	// Half 1 — the class guard. Everything timed advertises must resolve on the
	// mux timed builds. Fires the moment a resource is added to the list without
	// a matching mount.
	resregtest.AssertServesWhatItAdvertises(t, "timed", timedOpenAPIResources, mux)

	// Half 2 — the anchor, and the F32 regression itself. scheduled_tasks is
	// routd's; timed only CALLS it. Emitting it here is what advertised four
	// phantom operations.
	resregtest.AssertServesNoneOf(t, "timed", []string{"scheduled_tasks"}, mux)
}

// TestTimedMux_ServesItsThreeRoutes pins what timed DOES serve, so half 1 above
// cannot be satisfied by a mux that serves nothing at all.
func TestTimedMux_ServesItsThreeRoutes(t *testing.T) {
	mux := newTimedMux(&dashServer{})
	for _, want := range [][2]string{
		{"GET", "/health"},
		{"GET", "/openapi.json"},
		{"GET", "/dash/timed/"},
	} {
		if _, pattern := mux.Handler(httptest.NewRequest(want[0], want[1], nil)); pattern == "" {
			t.Errorf("timed's mux does not serve %s %s", want[0], want[1])
		}
	}
}
