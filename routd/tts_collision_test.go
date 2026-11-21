package routd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTSCacheConcurrentSynthesis(t *testing.T) {
	const n = 32
	audio := bytes.Repeat([]byte("audio"), 200000)
	var requests atomic.Int32
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == n {
			close(ready)
		}
		<-ready
		_, _ = w.Write(audio)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := TTSConfig(true, srv.URL, "voice", "model", 30*time.Second, dir)
	hash := sha256.Sum256([]byte("hello\x00voice\x00model"))
	wantPath := filepath.Join(dir, hex.EncodeToString(hash[:])+".ogg")
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			path, err := cfg.cacheOrSynthesize("hello", "voice", "model")
			if err != nil {
				t.Error(err)
				return
			}
			if path != wantPath {
				t.Errorf("path=%q want %q", path, wantPath)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, audio) {
				t.Errorf("cached length %d: %v", len(got), err)
			}
		})
	}
	wg.Wait()
}
