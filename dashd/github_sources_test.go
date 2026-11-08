package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestGitHubSourcesList(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `[
		{"source":"repo/owner/project","owner_folder":"team","secret":"list-secret"},
		{"source":"app/reviewer","owner_folder":"team"},
		{"source":"org/<script>alert(1)","owner_folder":"team"},
		{"source":"app/hidden-app","owner_folder":"other"}]`
	d := &dash{routd: routdDB(t), routdURL: u.srv.URL,
		svc: func(context.Context) (string, error) { return "dashd-token", nil }}
	seedGrant(t, d.routd, core.ACLRow{
		Principal: "member@x", Action: "interact", Scope: "team", Effect: "allow",
	})
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("GET", "/dash/github_sources/", "", "member@x"))
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}
	if u.calls != 1 || u.method != "GET" || u.path != "/v1/github_sources" {
		t.Fatalf("upstream: calls=%d %s %s", u.calls, u.method, u.path)
	}
	if u.authz != "Bearer dashd-token" || u.userSub != "" {
		t.Fatalf("upstream identity: bearer=%q user=%q", u.authz, u.userSub)
	}
	body := w.Body.String()
	for _, want := range []string{
		"<td>repo</td>", "<td>app</td>", "<td>org</td>",
		"owner/project", "reviewer", "&lt;script&gt;", "Group folder", "add source",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %q", want)
		}
	}
	for _, forbidden := range []string{"hidden-app", "list-secret", "<script>alert(1)"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("list exposed %q", forbidden)
		}
	}
}

func TestGitHubSourcesWrite(t *testing.T) {
	for _, tc := range []struct {
		name, path, method, upstreamPath, response string
		status                                     int
		form                                       url.Values
	}{
		{
			name: "add", path: "/dash/github_sources/", method: "POST",
			upstreamPath: "/v1/github_sources", status: http.StatusCreated,
			response: `{"source":"app/reviewer","url":"https://example.com/github/app/reviewer","secret":"new-secret"}`,
			form:     url.Values{"kind": {"app"}, "name": {"reviewer"}, "owner_folder": {"team/review"}},
		},
		{
			name: "delete", path: "/dash/github_sources/delete", method: "DELETE",
			upstreamPath: "/v1/github_sources/repo/owner/project", status: http.StatusOK,
			response: `{"deleted":true,"secret":"deleted-secret"}`,
			form:     url.Values{"source": {"repo/owner/project"}, "owner_folder": {"team/review"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newRoutdUpstream(t)
			u.status, u.respond = tc.status, tc.response
			d := &dash{routd: routdDB(t), routdURL: u.srv.URL,
				svc: func(context.Context) (string, error) { return "dashd-token", nil }}
			scope := "team/review"
			if tc.name == "add" {
				scope = "**"
			}
			seedAdmin(t, d.routd, "admin@x", scope)
			w := httptest.NewRecorder()
			newMux(d).ServeHTTP(w, adminReq("POST", tc.path, tc.form.Encode(), "admin@x"))
			if tc.name == "add" {
				// The secret exists only in this response; the page shows it once.
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "new-secret") ||
					!strings.Contains(w.Body.String(), "https://example.com/github/app/reviewer") {
					t.Fatalf("add = %d body=%s", w.Code, w.Body.String())
				}
			} else {
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/dash/github_sources/" {
					t.Fatalf("write = %d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
				}
				if strings.Contains(w.Body.String()+w.Header().Get("Location"), "secret") {
					t.Fatal("delete exposed a secret")
				}
			}
			if u.calls != 1 || u.method != tc.method || u.path != tc.upstreamPath {
				t.Fatalf("upstream: calls=%d %s %s", u.calls, u.method, u.path)
			}
			if u.authz != "Bearer dashd-token" || u.userSub != "" {
				t.Fatalf("upstream identity: bearer=%q user=%q", u.authz, u.userSub)
			}
			if tc.name == "add" {
				var body map[string]string
				if err := json.Unmarshal([]byte(u.body), &body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 3 || body["kind"] != "app" || body["name"] != "reviewer" ||
					body["owner_folder"] != "team/review" {
					t.Fatalf("registration = %v", body)
				}
			} else if u.query != "owner_folder=team%2Freview" || u.body != "" {
				t.Fatalf("deletion query=%q body=%q", u.query, u.body)
			}
		})
	}
}

func TestGitHubSourcesAppRequiresOperator(t *testing.T) {
	u := newRoutdUpstream(t)
	u.status, u.respond = http.StatusCreated, `{"source":"app/bot","secret":"secret"}`
	d := &dash{routd: routdDB(t), routdURL: u.srv.URL}
	seedAdmin(t, d.routd, "admin@x", "team")
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/github_sources/", "kind=app&name=bot&owner_folder=team", "admin@x"))
	if w.Code != http.StatusForbidden || u.calls != 0 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, u.calls, w.Body.String())
	}
}

func TestGitHubSourcesScopedAdminDenied(t *testing.T) {
	for _, kind := range []string{"repo", "org"} {
		t.Run(kind, func(t *testing.T) {
			u := newRoutdUpstream(t)
			u.status, u.respond = http.StatusCreated, `{"source":"source","secret":"secret"}`
			d := &dash{routd: routdDB(t), routdURL: u.srv.URL}
			seedAdmin(t, d.routd, "admin@x", "team")
			w := httptest.NewRecorder()
			name := "owner"
			if kind == "repo" {
				name = "owner/project"
			}
			form := url.Values{"kind": {kind}, "name": {name}, "owner_folder": {"team"}}
			newMux(d).ServeHTTP(w, adminReq("POST", "/dash/github_sources/", form.Encode(), "admin@x"))
			if w.Code != http.StatusForbidden || u.calls != 0 {
				t.Fatalf("status=%d calls=%d", w.Code, u.calls)
			}
		})
	}
}

func TestGitHubSourcesOperatorAddsEachKind(t *testing.T) {
	for _, kind := range []string{"repo", "org", "app"} {
		t.Run(kind, func(t *testing.T) {
			u := newRoutdUpstream(t)
			u.status, u.respond = http.StatusCreated, `{"source":"source","secret":"secret"}`
			d := &dash{routd: routdDB(t), routdURL: u.srv.URL}
			seedAdmin(t, d.routd, "admin@x", "**")
			name := "owner"
			if kind == "repo" {
				name = "owner/repo"
			}
			form := url.Values{"kind": {kind}, "name": {name}, "owner_folder": {"team"}}
			w := httptest.NewRecorder()
			newMux(d).ServeHTTP(w, adminReq("POST", "/dash/github_sources/", form.Encode(), "admin@x"))
			if w.Code != http.StatusOK || u.calls != 1 || !strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, u.calls, w.Body.String())
			}
		})
	}
}

func TestGitHubSourcesWriteGuards(t *testing.T) {
	d := &dash{routd: routdDB(t)}
	seedAdmin(t, d.routd, "admin@x", "team")
	u := newRoutdUpstream(t)
	d.routdURL = u.srv.URL
	mux := newMux(d)
	for _, path := range []string{"/dash/github_sources/", "/dash/github_sources/delete"} {
		for _, tc := range []struct {
			name, sub, folder, origin string
			status                    int
		}{
			{"anonymous", "", "team", "", http.StatusUnauthorized},
			{"member", "member@x", "team", "", http.StatusForbidden},
			{"other folder", "admin@x", "other", "", http.StatusForbidden},
			{"cross origin", "admin@x", "team", "https://foreign.example", http.StatusForbidden},
		} {
			t.Run(path+tc.name, func(t *testing.T) {
				form := url.Values{"kind": {"app"}, "name": {"reviewer"},
					"source": {"app/reviewer"}, "owner_folder": {tc.folder}}
				r := adminReq("POST", path, form.Encode(), tc.sub)
				r.Header.Set("Origin", tc.origin)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatalf("write = %d, want %d", w.Code, tc.status)
				}
				if u.calls != 0 {
					t.Fatal("refused request reached routd")
				}
			})
		}
	}
}

func TestGitHubSourcesListRequiresUser(t *testing.T) {
	u := newRoutdUpstream(t)
	d := &dash{routdURL: u.srv.URL}
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, httptest.NewRequest("GET", "/dash/github_sources/", nil))
	if w.Code != http.StatusUnauthorized || u.calls != 0 {
		t.Fatalf("anonymous list = %d, upstream calls=%d", w.Code, u.calls)
	}
}

func TestGitHubSourcesUpstreamFailure(t *testing.T) {
	u := newRoutdUpstream(t)
	u.status, u.respond = http.StatusForbidden, `{"secret":"error-secret"}`
	d := &dash{routd: routdDB(t), routdURL: u.srv.URL}
	mux := newMux(d)
	for _, path := range []string{"/dash/github_sources/", "/dash/github_sources/delete"} {
		methods := []string{"POST"}
		if path == "/dash/github_sources/" {
			methods = append(methods, "GET")
		}
		for _, method := range methods {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, asOperator(adminReq(method, path,
				"kind=app&name=reviewer&owner_folder=team&source=app%2Freviewer", "")))
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "error-secret") {
				t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
			}
		}
	}
}
