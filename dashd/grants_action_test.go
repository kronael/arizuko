package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

func TestGrantActionSubmission(t *testing.T) {
	d, db := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	for _, action := range []string{"send", "mcp:send", "egress", "shell:env:CUSTOM_TOKEN"} {
		t.Run(action, func(t *testing.T) {
			form := url.Values{
				"principal": {"folder:team"}, "action": {action}, "scope": {"team"},
			}.Encode()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/grants", form, "alice@x"))
			if action == "send" {
				if w.Code != http.StatusBadRequest ||
					!strings.Contains(w.Body.String(), "send") ||
					!strings.Contains(w.Body.String(), "mcp:") {
					t.Errorf("invalid action must show a useful client error: %d %s", w.Code, w.Body)
				}
				if n := countGrants(t, db, "folder:team", action); n != 0 {
					t.Errorf("refused action persisted %d rows", n)
				}
				return
			}
			if w.Code != http.StatusSeeOther {
				t.Fatalf("valid action refused: %d %s", w.Code, w.Body)
			}
			if n := countGrants(t, db, "folder:team", action); n != 1 {
				t.Errorf("valid action persisted %d rows", n)
			}
			if action == "shell:env:CUSTOM_TOKEN" {
				keys := auth.ShellEnvKeys(store.NewOver(db),
					auth.Caller{Principal: "folder:team"}, "team", []string{"CUSTOM_TOKEN"})
				if len(keys) != 1 || keys[0] != "CUSTOM_TOKEN" {
					t.Errorf("shell resolver omitted the custom key: %v", keys)
				}
				return
			}
			if !auth.Authorize(store.NewOver(db), auth.Caller{Principal: "folder:team"},
				action, "team", nil) {
				t.Error("persisted action did not authorize")
			}
		})
	}
}

func TestGrantActionForm(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("GET", "/dash/groups/team/grants", "", "alice@x"))
	if w.Code != http.StatusOK {
		t.Fatalf("grants form: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, action := range []string{
		"admin", "mcp:send", "mcp:add_acl", "mcp:*", "egress", "web:publish", "gh:write",
	} {
		if !strings.Contains(body, `<option value="`+action+`"`) {
			t.Errorf("form omitted %s", action)
		}
	}
	if strings.Contains(body, `<option value="send"`) {
		t.Error("form offers a bare tool name")
	}
	if !strings.Contains(body, `<input type="text" name="action"`) ||
		!strings.Contains(body, `list="actions"`) {
		t.Error("action must allow a concrete custom shell:env key")
	}
}
