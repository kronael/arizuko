package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestDeleteStoreChecksCredentialOwnerBeforeDeleting(t *testing.T) {
	var deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		if r.Method == http.MethodDelete {
			deletes = append(deletes, r.URL.Path)
			return
		}
		switch r.URL.Path {
		case "/accounts":
			_, _ = w.Write([]byte(`[{"account_id":"id","account_name":"arizuko_eval"}]`))
		case "/accounts/id/users":
			_, _ = w.Write([]byte(`[{"user_name":"authd"}]`))
		case "/accounts/id/users/authd/access-keys":
			_, _ = w.Write([]byte(`[{"access_key_id":"key","is_active":true}]`))
		default:
			if r.Header.Get("X-Amz-Target") == "DynamoDB_20120810.ListTables" {
				_, _ = w.Write([]byte(`{"TableNames":[]}`))
			}
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "DYNAMODB_URL="+srv.URL+"\nAWS_REGION=us-east-1\n")
	writeFile(t, filepath.Join(dir, "env", "authd.env"), "AWS_ACCESS_KEY_ID=key\nAWS_SECRET_ACCESS_KEY=secret\n")
	m := &manager{base: srv.URL, client: srv.Client()}
	if err := deleteStore(dir, "arizuko_eval", m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deletes, []string{"/accounts/id"}) {
		t.Fatalf("deleted %v", deletes)
	}
	deletes = nil
	writeFile(t, filepath.Join(dir, "env", "authd.env"), "AWS_ACCESS_KEY_ID=foreign\nAWS_SECRET_ACCESS_KEY=secret\n")
	if err := deleteStore(dir, "arizuko_eval", m); err == nil || len(deletes) != 0 {
		t.Fatalf("foreign key: deleted %v, error %v", deletes, err)
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
