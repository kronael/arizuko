package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteAccountSelectsExactName(t *testing.T) {
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[{"account_id":"one","account_name":"arizuko_eval"},{"account_id":"two","account_name":"arizuko_eval2"}]`))
		case http.MethodDelete:
			deleted = r.URL.Path
		}
	}))
	defer srv.Close()
	m := &manager{base: srv.URL, client: srv.Client()}
	if err := m.deleteAccount("arizuko_eval"); err != nil {
		t.Fatal(err)
	}
	if deleted != "/accounts/one" {
		t.Fatalf("deleted %q", deleted)
	}
	deleted = ""
	if err := m.deleteAccount("arizuko_missing"); err != nil || deleted != "" {
		t.Fatalf("missing account: deleted %q, error %v", deleted, err)
	}
}
