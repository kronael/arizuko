package dynstore_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

// A filtered read with a Limit that matches nothing reads the partition in
// pages, not one item per request: krons' status lookups (BUGS L825) sent
// 2,563 requests per pass, one per message.
func TestFilteredLimitReadIsNotPagedPerItem(t *testing.T) {
	const entity, items = "rows", 60
	cfg := dyntest.Config(t)
	upstream, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cfg.Endpoint, cfg.Prefix = srv.URL, dynstore.TestPrefix()
	s, err := dynstore.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.DropEntity(ctx, entity) }()
	for i := range items {
		w := objstore.Write{Op: objstore.Put, Entity: entity,
			Key: objstore.Key{Partition: "chat", Sort: fmt.Sprintf("m%03d", i)}, Attrs: map[string]string{"verb": "message"}}
		if _, err := s.GuardedBatch(ctx, nil, []objstore.Write{w}); err != nil {
			t.Fatal(err)
		}
	}
	requests.Store(0)
	got, err := s.Query(ctx, objstore.Query{Entity: entity, Partition: "chat",
		Where:      []objstore.Cmp{{Attr: "verb", Op: objstore.Eq, Value: "status"}},
		Descending: true, Limit: 1})
	if err != nil || len(got) != 0 {
		t.Fatalf("query: %v %v", got, err)
	}
	if n := requests.Load(); n > 2 {
		t.Fatalf("a filtered read over %d items took %d requests, want one page", items, n)
	}
}
