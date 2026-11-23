package dynstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
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

// A read names a page size on every request, filtered or not. A server reads
// what one request lets it before it cuts the answer to 1 MB, and ExtendDB
// reads the rest of the partition: the boot walk of a 232,330-message chat on
// sloth made it read 228,650 rows for the first 3,486 it returned (BUGS L920).
func TestEveryRequestNamesAPage(t *testing.T) {
	const entity, items, page = "rows", 1100, 1000
	cfg := dyntest.Config(t)
	upstream, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []int
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".Query") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			var in struct{ Limit int }
			if err := json.Unmarshal(body, &in); err != nil {
				t.Error(err)
			}
			mu.Lock()
			asked = append(asked, in.Limit)
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
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
	for first := 0; first < items; first += objstore.MaxBatchActions {
		writes := make([]objstore.Write, objstore.MaxBatchActions)
		for i := range writes {
			writes[i] = objstore.Write{Op: objstore.Put, Entity: entity,
				Key:   objstore.Key{Partition: "chat", Sort: fmt.Sprintf("m%04d", first+i)},
				Attrs: map[string]string{"verb": "message"}}
		}
		if _, err := s.GuardedBatch(ctx, nil, writes); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		query objstore.Query
		want  int
		pages []int
	}{
		{"whole partition", objstore.Query{}, items, []int{page, page}},
		{"filter matches nothing", objstore.Query{Limit: 1,
			Where: []objstore.Cmp{{Attr: "verb", Op: objstore.Eq, Value: "status"}}}, 0, []int{page, page}},
		{"limit past one page", objstore.Query{Limit: 1050}, 1050, []int{page, 50}},
		{"limit inside one page", objstore.Query{Limit: 7}, 7, []int{7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked = nil
			tc.query.Entity, tc.query.Partition = entity, "chat"
			got, err := s.Query(ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("the read returned %d items, want %d", len(got), tc.want)
			}
			if !slices.Equal(asked, tc.pages) {
				t.Fatalf("the requests asked for %v items, want %v", asked, tc.pages)
			}
		})
	}
}
