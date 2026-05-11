package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

// TestTTSCacheRoundtrip drives sendVoice end-to-end with a stub TTS
// server and a stub channel; second call returns the cached file
// (server hits stay at 1).
func TestTTSCacheRoundtrip(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Voice string `json:"voice"`
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Voice != "af_bella" || body.Input != "hello" {
			http.Error(w, "bad payload", 400)
			return
		}
		hits++
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write([]byte("OggS-fake-bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, _ := store.OpenMem()
	defer st.Close()
	cfg := &core.Config{
		Name:        "test",
		ProjectRoot: dir,
		GroupsDir:   filepath.Join(dir, "groups"),
		IpcDir:      filepath.Join(dir, "ipc"),
		TTSEnabled:  true,
		TTSURL:      srv.URL,
		TTSVoice:    "af_bella",
		TTSModel:    "kokoro",
		TTSTimeout:  5 * time.Second,
	}
	gw := New(cfg, st)

	// Round 1: cache miss → 1 hit, file written.
	path1, err := gw.ttsCacheOrSynthesize("hello", "af_bella", "kokoro")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	data, err := os.ReadFile(path1)
	if err != nil || string(data) != "OggS-fake-bytes" {
		t.Fatalf("cache file = %q (err %v)", string(data), err)
	}

	// Round 2: cache hit → still 1 hit total.
	path2, err := gw.ttsCacheOrSynthesize("hello", "af_bella", "kokoro")
	if err != nil {
		t.Fatalf("synthesize2: %v", err)
	}
	if path1 != path2 {
		t.Errorf("cache returned different paths: %q vs %q", path1, path2)
	}
	if hits != 1 {
		t.Errorf("server hit count = %d, want 1 (second call must hit cache)", hits)
	}
}

// TestResolveVoice walks precedence: arg > PERSONA.md frontmatter > default.
func TestResolveVoice(t *testing.T) {
	dir := t.TempDir()
	groups := filepath.Join(dir, "groups", "alice")
	os.MkdirAll(groups, 0o755)
	persona := `---
name: Alice
voice: nova
---
She speaks softly.
`
	os.WriteFile(filepath.Join(groups, "PERSONA.md"), []byte(persona), 0o644)

	st, _ := store.OpenMem()
	defer st.Close()
	cfg := &core.Config{
		ProjectRoot: dir,
		GroupsDir:   filepath.Join(dir, "groups"),
		TTSVoice:    "default-voice",
	}
	gw := New(cfg, st)

	cases := []struct {
		name, arg, folder, want string
	}{
		{"arg wins", "shimmer", "alice", "shimmer"},
		{"frontmatter wins over default", "", "alice", "nova"},
		{"default", "", "", "default-voice"},
		{"missing folder falls through", "", "ghost", "default-voice"},
	}
	for _, tc := range cases {
		got := gw.resolveVoice(tc.arg, tc.folder)
		if got != tc.want {
			t.Errorf("%s: resolveVoice(%q, %q) = %q, want %q",
				tc.name, tc.arg, tc.folder, got, tc.want)
		}
	}
}

// TestValidateVoiceText asserts the empty / oversize guards.
func TestValidateVoiceText(t *testing.T) {
	if err := validateVoiceText(""); err == nil {
		t.Error("empty text should reject")
	}
	if err := validateVoiceText("   \t\n"); err == nil {
		t.Error("whitespace-only text should reject")
	}
	if err := validateVoiceText("hi"); err != nil {
		t.Errorf("normal text rejected: %v", err)
	}
	long := strings.Repeat("a", 5001)
	if err := validateVoiceText(long); err == nil {
		t.Error("5001 chars should reject")
	}
}
