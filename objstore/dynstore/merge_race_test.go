package dynstore_test

import (
	"context"
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

func TestMergeInitPresenceRaces(t *testing.T) {
	for _, mode := range []string{"create", "create_second", "delete", "external"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			cfg := dyntest.Config(t)
			cfg.Prefix = dynstore.TestPrefix()
			base, err := dynstore.Dial(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := base.CreateEntity(ctx, "rows"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := base.DropEntity(context.Background(), "rows"); err != nil {
					t.Error(err)
				}
			})
			first := objstore.Key{Partition: "p", Sort: "first"}
			second := objstore.Key{Partition: "p", Sort: "second"}
			companion := objstore.Key{Partition: "p", Sort: "companion"}
			if mode == "delete" {
				seed := objstore.Write{Op: objstore.Put, Entity: "rows", Key: first,
					Attrs: map[string]string{"old": "yes"}}
				if ok, err := base.GuardedBatch(ctx, nil, []objstore.Write{seed}); err != nil || !ok {
					t.Fatalf("seed: %v %v", ok, err)
				}
			}
			target, err := url.Parse(cfg.Endpoint)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Amz-Target") == "DynamoDB_20120810.TransactWriteItems" && attempts.Add(1) == 1 {
					op := objstore.Put
					if mode == "delete" {
						op = objstore.Delete
					}
					key := first
					if mode == "create_second" {
						key = second
					}
					race := objstore.Write{Op: op, Entity: "rows", Key: key,
						Attrs: map[string]string{"racer": "yes"}}
					if ok, err := base.GuardedBatch(ctx, nil, []objstore.Write{race}); err != nil || !ok {
						t.Errorf("race: %v %v", ok, err)
					}
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			cfg.Endpoint = server.URL
			s, err := dynstore.Dial(cfg)
			if err != nil {
				t.Fatal(err)
			}
			checks := []objstore.Check{}
			if mode == "external" {
				checks = append(checks, objstore.Check{
					Entity: "rows", Key: objstore.Key{Partition: "p", Sort: "missing"},
					Cond: objstore.Cond{Item: objstore.ItemPresent},
				})
			}
			ok, err := s.GuardedBatch(ctx, checks, []objstore.Write{
				{Op: objstore.Merge, Entity: "rows", Key: first, Init: map[string]string{"born": "first"}},
				{Op: objstore.Merge, Entity: "rows", Key: second, Init: map[string]string{"born": "second"}},
				{Op: objstore.Put, Entity: "rows", Key: companion, Attrs: map[string]string{"done": "yes"}},
			})
			wantAttempts := int32(2)
			if mode == "external" {
				wantAttempts = 1
			}
			if err != nil || attempts.Load() != wantAttempts || ok != (mode != "external") {
				t.Fatalf("committed=%v attempts=%d error=%v", ok, attempts.Load(), err)
			}
			rows, err := base.Query(ctx, objstore.Query{Entity: "rows", Partition: "p"})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]map[string]string{}
			for _, row := range rows {
				got[row.Key.Sort] = row.Attrs
			}
			if mode == "external" {
				if len(got) != 1 || got["first"]["racer"] != "yes" {
					t.Fatalf("refusal changed companions: %v", got)
				}
			} else {
				if len(got) != 3 || got["companion"]["done"] != "yes" {
					t.Fatalf("wrong merged rows: %v", got)
				}
				if mode == "create_second" {
					if got["second"]["racer"] != "yes" || got["second"]["born"] != "" {
						t.Fatalf("second merge changed raced item: %v", got)
					}
				} else if got["second"]["born"] != "second" ||
					(mode == "create" && got["first"]["born"] != "") ||
					(mode == "delete" && got["first"]["born"] != "first") {
					t.Fatalf("wrong merged rows: %v", got)
				}
			}
		})
	}
}

func TestMergeInitUnclassifiableCancellationIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		if r.Header.Get("X-Amz-Target") == "DynamoDB_20120810.Query" {
			if _, err := w.Write([]byte(`{"Items":[],"Count":0}`)); err != nil {
				t.Error(err)
			}
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		body := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException",` +
			`"CancellationReasons":[{"Code":"ConditionalCheckFailed"},{}]}`
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	s, err := dynstore.Dial(dynstore.Config{
		Endpoint: server.URL, KeyID: dynstore.DevKeyID, Secret: dynstore.DevSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{
		{Op: objstore.Merge, Entity: "rows", Key: objstore.Key{Partition: "p", Sort: "a"},
			Init: map[string]string{"born": "a"}},
		{Op: objstore.Put, Entity: "rows", Key: objstore.Key{Partition: "p", Sort: "b"}},
	})
	if err == nil || ok {
		t.Fatalf("committed=%v error=%v; want error", ok, err)
	}
}
