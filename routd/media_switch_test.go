package routd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

func TestEnrichMediaTranscriptionSwitches(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nfor arg do out=$arg; done\nprintf audio > \"$out\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"text":"transcribed"}`)) }))
	defer whisper.Close()
	for _, voice := range []bool{false, true} {
		for _, video := range []bool{false, true} {
			for _, mime := range []string{"audio/ogg", "video/mp4"} {
				t.Run(fmt.Sprintf("voice=%t/video=%t/%s", voice, video, mime), func(t *testing.T) {
					db, loop, _ := enrichLoop(t, mediaConfig{Enabled: true, WhisperURL: whisper.URL, VoiceEnabled: voice, VideoEnabled: video})
					raw, _ := json.Marshal([]chanlib.InboundAttachment{{Data: "YXVkaW8=", Mime: mime, Filename: "media"}})
					msg := &core.Message{ID: "m", ChatJID: "tg:1", Timestamp: time.Now().UTC(), Attachments: string(raw)}
					if err := db.PutMessage(*msg); err != nil {
						t.Fatal(err)
					}
					if err := loop.enrichAttachments(context.Background(), msg, "demo", false); err != nil {
						t.Fatal(err)
					}
					want := voice
					if strings.HasPrefix(mime, "video/") {
						want = video
					}
					if got := strings.Contains(msg.Content, `transcript="transcribed"`); got != want {
						t.Errorf("transcribed=%t want %t", got, want)
					}
				})
			}
		}
	}
}
