package main

import (
	"net/http"
	"testing"
)

func TestRouteUpdateRedirectDestination(t *testing.T) {
	mux, _, st := testResourceMux(t, nil)
	w := doJSON(t, mux, "POST", "/v1/proxyd_routes", map[string]any{
		"path": "/mine/", "redirect_to": "/old/", "auth": "public",
	})
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, destination := range []string{"/new/", ""} {
		args := map[string]any{"redirect_to": destination}
		if destination == "" {
			args["backend"] = "http://backend:8080"
		}
		w = doJSON(t, mux, "PATCH", "/v1/proxyd_routes/mine/", args)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		routes, err := st.AllProxydRoutes()
		if err != nil || len(routes) != 1 || routes[0].RedirectTo != destination {
			t.Fatalf("routes = %v, error = %v, want redirect %q", routes, err, destination)
		}
	}
}
