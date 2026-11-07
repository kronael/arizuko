package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"pub/site/index.html": "index", "pub/file.txt": "public", "pub/.env": "secret", "pub/nested/.env.local": "secret", "pub/nested/data.db": "secret", "pub/nested/.git/config": "secret", "priv/a/index.html": "private", "priv/b/secret.txt": "other", "root.txt": "root"} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"pub/link": "../priv/b/secret.txt", "pub/env-link": ".env", "pub/env-local-link": "nested/.env.local", "pub/db-link": "nested/data.db", "pub/outside": "/etc/passwd", "priv/a/other": "../b/secret.txt", "pub/git-link": "nested/.git/config"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "pub", "badindex"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../priv/b/secret.txt", filepath.Join(dir, "pub", "badindex", "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "pub", "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	handler := staticFiles(dir)
	for _, tc := range []struct {
		url, groups    string
		status         int
		body, location string
	}{
		{"/pub/site/", "", 200, "index", ""}, {"/pub/site?q=1", "", 301, "", "/pub/site/?q=1"},
		{"/site/", "", 200, "index", ""}, {"/site?q=1", "", 301, "", "/site/?q=1"}, {"/pub/badindex/", "", 404, "", ""}, {"/pub/empty?q=1", "", 301, "", "/pub/empty/?q=1"}, {"/pub/empty/", "", 404, "", ""}, {"/file.txt", "", 200, "public", ""}, {"/root.txt", "", 404, "", ""}, {"/missing", "", 404, "", ""},
		{"/pub/.env", "", 404, "", ""}, {"/pub/%2eenv", "", 404, "", ""}, {"/pub/nested/.env.local", "", 404, "", ""}, {"/pub/nested/%2eenv.local", "", 404, "", ""}, {"/pub/nested/data.db", "", 404, "", ""}, {"/pub/nested/data%2edb", "", 404, "", ""}, {"/pub/nested/.git/config", "", 404, "", ""}, {"/pub/nested/%2egit/config", "", 404, "", ""},
		{"/pub/env-link", "", 404, "", ""}, {"/pub/env-local-link", "", 404, "", ""}, {"/pub/db-link", "", 404, "", ""}, {"/pub/outside", "", 404, "", ""}, {"/pub/git-link", "", 404, "", ""}, {"/pub/link", "", 404, "", ""}, {"/pub/%2e%2e/root.txt", "", 404, "", ""},
		{"/priv/a/", `["a"]`, 200, "private", ""}, {"/priv/a/other", `["a","b"]`, 404, "", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			req.Header.Set("X-User-Groups", tc.groups)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status || (tc.body != "" && w.Body.String() != tc.body) || (tc.location != "" && w.Header().Get("Location") != tc.location) {
				t.Fatalf("got %d %q %q", w.Code, w.Body.String(), w.Header().Get("Location"))
			}
		})
	}
}

func TestStaticFilesThroughRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pub", "site"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pub", "site", "index.html"), []byte("public"), 0644); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config{authSecret: "testsecret"}, files: staticFiles(dir), rr: newRoutesResource(nil, nil)}
	for _, tc := range []struct {
		url    string
		status int
		body   string
	}{{"/pub/site/", 200, "public"}, {"/priv/site/", http.StatusSeeOther, ""}} {
		w := httptest.NewRecorder()
		s.route(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if w.Code != tc.status || (tc.body != "" && w.Body.String() != tc.body) {
			t.Fatalf("%s: got %d %q", tc.url, w.Code, w.Body.String())
		}
	}
}
