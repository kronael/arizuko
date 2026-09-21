package dynstore

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore"
)

// A store server that accepts the connection and then answers nothing must fail
// the call, not hold it. The SDK's own client bounds the dial and the TLS
// handshake and nothing after them, and a daemon reaches the store mostly from
// a context that never cancels, so without requestTimeout this call never
// returns (BUGS L341).
func TestSilentServerFailsInsteadOfHolding(t *testing.T) {
	addr := silentServer(t)

	prev := requestTimeout
	requestTimeout = 200 * time.Millisecond
	t.Cleanup(func() { requestTimeout = prev })

	store, err := Dial(Config{Endpoint: "http://" + addr, KeyID: DevKeyID, Secret: DevSecret})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := store.Query(context.Background(), objstore.Query{Entity: "any", Partition: "p"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent server answered a query")
		}
		if waited := time.Since(start); waited > 10*time.Second {
			t.Errorf("the call took %s; the bound is %s per attempt", waited, requestTimeout)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the call never returned: the store client bounds no attempt")
	}
}

// silentServer accepts every connection and holds it open without a reply. It
// keeps each one, because closing would answer the caller with an error the
// client returns at once.
func silentServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return listener.Addr().String()
}
