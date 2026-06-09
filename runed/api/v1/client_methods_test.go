package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStopFolder_OK(t *testing.T) {
	var gotBody StopRunRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs/stop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(StopRunResponse{Killed: true, RunID: "r9"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok", 5*time.Second)
	out, err := c.StopFolder(context.Background(), "atlas/main")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.Folder != "atlas/main" {
		t.Fatalf("folder = %q; want atlas/main", gotBody.Folder)
	}
	if !out.Killed || out.RunID != "r9" {
		t.Fatalf("out = %+v; want {Killed:true RunID:r9}", out)
	}
}

func TestStopFolder_NonOK_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs/stop", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(Err{Error: "forbidden", Message: "nope"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok", 5*time.Second)
	_, err := c.StopFolder(context.Background(), "atlas/main")
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err = %T; want *APIError", err)
	}
	if ae.Status != http.StatusForbidden || ae.Code != "forbidden" || ae.Msg != "nope" {
		t.Fatalf("APIError = %+v", ae)
	}
	if ae.Error() != "runed 403 forbidden: nope" {
		t.Fatalf("Error() = %q", ae.Error())
	}
}

func TestRecentSessions_OK(t *testing.T) {
	var gotFolder, gotN string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/recent", func(w http.ResponseWriter, r *http.Request) {
		gotFolder = r.URL.Query().Get("folder")
		gotN = r.URL.Query().Get("n")
		_ = json.NewEncoder(w).Encode(RecentSessionsResponse{
			Sessions: []RecentSessionRecord{{SessionID: "s1", GroupFolder: "atlas/main"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok", 5*time.Second)
	out, err := c.RecentSessions(context.Background(), "atlas/main", 3)
	if err != nil {
		t.Fatal(err)
	}
	if gotFolder != "atlas/main" || gotN != "3" {
		t.Fatalf("query folder=%q n=%q; want atlas/main, 3", gotFolder, gotN)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].SessionID != "s1" {
		t.Fatalf("sessions = %+v", out.Sessions)
	}
}

func TestRecentSessions_OmitsZeroN(t *testing.T) {
	var hasN bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/recent", func(w http.ResponseWriter, r *http.Request) {
		_, hasN = r.URL.Query()["n"]
		_ = json.NewEncoder(w).Encode(RecentSessionsResponse{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok", 5*time.Second)
	if _, err := c.RecentSessions(context.Background(), "atlas/main", 0); err != nil {
		t.Fatal(err)
	}
	if hasN {
		t.Fatal("n=0 should be omitted from the query")
	}
}
