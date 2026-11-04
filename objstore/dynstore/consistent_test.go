package dynstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

// Every Query reads the base table strongly consistent: an authorization read
// and the counter re-read in objstore.Increment must see the committed write
// (BUGS L483). The driver declares no secondary index, so the flag is never
// refused.
func TestQuerySendsConsistentRead(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{"Items":[],"Count":0}`))
	}))
	t.Cleanup(srv.Close)

	store, err := Dial(Config{Endpoint: srv.URL, KeyID: DevKeyID, Secret: DevSecret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Query(context.Background(), objstore.Query{Entity: "any", Partition: "p"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"ConsistentRead":true`) {
		t.Errorf("query body lacks ConsistentRead:true: %s", body)
	}
	if strings.Contains(body, "IndexName") {
		t.Errorf("query names a secondary index, which refuses ConsistentRead: %s", body)
	}
}
