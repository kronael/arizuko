package dynstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

// A guarded identity lookup must see rows committed before its marker read.
func TestQueryRequestsStrongConsistency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ConsistentRead bool
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid query", http.StatusBadRequest)
			return
		}
		if !request.ConsistentRead {
			t.Error("query permits stale rows after a committed write")
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		// The client owns any disconnect after the complete response.
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	defer server.Close()
	store, err := Dial(Config{Endpoint: server.URL, KeyID: DevKeyID, Secret: DevSecret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Query(context.Background(), objstore.Query{Entity: "messages", Partition: "chat"}); err != nil {
		t.Fatal(err)
	}
}
