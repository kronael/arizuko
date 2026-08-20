package run

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPTargetCost covers the /v1/cost read wiring: sums tokens on 200,
// treats 404 as "no cost recorded" (0, no error — also what a routd predating
// the endpoint effectively returns), and surfaces other failures.
func TestHTTPTargetCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cost" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("turn_id") {
		case "t1":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"turn_id":"t1","folder":"eval","input_tokens":100,"output_tokens":25,"cost_cents":3}`))
		case "boom":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tgt := &HTTPTarget{API: srv.URL, Token: "tok"}
	if n, err := tgt.Cost("t1"); err != nil || n != 125 {
		t.Fatalf("Cost(t1) = %d, %v; want 125, nil", n, err)
	}
	if n, err := tgt.Cost("absent"); err != nil || n != 0 {
		t.Fatalf("Cost(absent) = %d, %v; want 0, nil (404 is not an error)", n, err)
	}
	if _, err := tgt.Cost("boom"); err == nil {
		t.Fatal("Cost(boom): want error on 500")
	}
	if n, err := tgt.Cost(""); err != nil || n != 0 {
		t.Fatalf("Cost(\"\") = %d, %v; want 0, nil", n, err)
	}
}

// TestHTTPTargetSession covers the GET /v1/topic_sessions read the baseline
// reset depends on: the root topic is queried as topic="", the bearer rides
// along, and a non-2xx surfaces (a silent "" on 403 would skip the reset and
// let every case inherit the root session).
func TestHTTPTargetSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/topic_sessions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("folder") + "|" + r.URL.Query().Get("topic") {
		case "eval|":
			w.Write([]byte(`{"session_id":"56952d01"}`))
		case "eval|t1":
			w.Write([]byte(`{"session_id":""}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	tgt := &HTTPTarget{API: srv.URL, Token: "tok"}
	if sid, err := tgt.Session("eval", ""); err != nil || sid != "56952d01" {
		t.Fatalf("Session(eval, \"\") = %q, %v; want 56952d01, nil", sid, err)
	}
	if sid, err := tgt.Session("eval", "t1"); err != nil || sid != "" {
		t.Fatalf("Session(eval, t1) = %q, %v; want \"\", nil", sid, err)
	}
	if _, err := tgt.Session("other", ""); err == nil {
		t.Fatal("Session(other): want error on 403, not a silent empty session")
	}
}
