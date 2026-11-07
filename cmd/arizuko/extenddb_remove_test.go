package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore"
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

func TestDeleteTablesSelectsPrefixes(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		switch r.Header.Get("X-Amz-Target") {
		case "DynamoDB_20120810.ListTables":
			_, _ = w.Write([]byte(`{"TableNames":["routd.one","authd.two","runed.three"]}`))
		case "DynamoDB_20120810.DeleteTable":
			var req struct{ TableName string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			deleted = append(deleted, req.TableName)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c, err := dynstore.Client(dynstore.Config{Endpoint: srv.URL, Region: "us-east-1", KeyID: "key", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteTables(context.Background(), c, []string{"routd", "authd"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deleted, []string{"routd.one", "authd.two"}) {
		t.Fatalf("deleted %v", deleted)
	}
}
