package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/kronael/arizuko/store"
)

func TestRouteBackendValidation(t *testing.T) {
	mux, _, _ := testResourceMux(t, nil)
	for i, tc := range []struct {
		backend  string
		redirect string
		valid    bool
	}{
		{"http://%", "", false},
		{"/relative", "", false},
		{"ftp://example.com", "", false},
		{"http://:8080", "", false},
		{"http://example.com", "", true},
		{"https://example.com", "", true},
		{"http://%", "/target", true},
	} {
		path := fmt.Sprintf("/backend-%d/", i)
		out := doJSON(t, mux, "POST", "/v1/proxyd_routes", map[string]any{"path": path, "backend": tc.backend, "redirect_to": tc.redirect, "auth": "public"})
		if (out.Code == http.StatusCreated) != tc.valid {
			t.Errorf("backend %q redirect %q: status=%d body=%s", tc.backend, tc.redirect, out.Code, out.Body)
		}
		if tc.redirect == "" {
			proxy := buildRouteProxy(store.ProxydRoute{Path: path, Backend: tc.backend})
			if (proxy != nil) != tc.valid {
				t.Errorf("backend %q: proxy=%v", tc.backend, proxy)
			}
		}
	}
}
