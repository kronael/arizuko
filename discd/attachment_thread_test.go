package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestAttachmentHTTPMaterializesItsDeclaredThread(t *testing.T) {
	for _, endpoint := range []string{"/send-file", "/send-voice"} {
		for _, existing := range []string{"", "existing-thread"} {
			t.Run(endpoint+existing, func(t *testing.T) {
				rec := &recorded{}
				srv := newThreadDiscordServer(t, rec, http.StatusOK, `{"id":"root-1"}`)
				defer srv.Close()
				defer mockAllDiscordEndpoints(srv.URL)()
				b := &bot{session: newTestSession(t)}
				mux := chanlib.NewAdapterMux("discord", []string{"discord:"}, b,
					func() bool { return true }, func() int64 { return 0 }, nil)
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				for key, value := range map[string]string{
					"chat_jid": "discord:g1/ch-1", "filename": "report.txt",
					"thread_root": "root-1", "thread_id": existing,
				} {
					if err := form.WriteField(key, value); err != nil {
						t.Fatal(err)
					}
				}
				file, err := form.CreateFormFile("file", "report.txt")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("the artifact")); err != nil {
					t.Fatal(err)
				}
				if err := form.Close(); err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, endpoint, &body)
				req.Header.Set("Content-Type", form.FormDataContentType())
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("upload status=%d body=%s", w.Code, w.Body.String())
				}
				destination := existing
				wantCalls := 1
				if existing == "" {
					destination, wantCalls = "root-1", 2
					assertPath(t, rec, "POST", "/channels/ch-1/messages/root-1/threads")
				}
				assertPath(t, rec, "POST", "/channels/"+destination+"/messages")
				if got := len(rec.snapshot()); got != wantCalls {
					t.Fatalf("unexpected placeholder or nested thread: calls=%d want=%d", got, wantCalls)
				}
			})
		}
	}
}

func TestAttachmentThreadFailureNeverPostsAtParent(t *testing.T) {
	for _, voice := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "voice"}[voice], func(t *testing.T) {
			rec := &recorded{}
			srv := newThreadDiscordServer(t, rec, http.StatusForbidden, `{"message":"Missing Permissions","code":50013}`)
			defer srv.Close()
			defer mockAllDiscordEndpoints(srv.URL)()
			b := &bot{session: newTestSession(t)}
			path := filepath.Join(t.TempDir(), "result.txt")
			if err := os.WriteFile(path, []byte("result"), 0600); err != nil {
				t.Fatal(err)
			}
			var id string
			var err error
			if voice {
				id, err = b.SendVoice("discord:g1/ch-1", path, "result", "", "root-1")
			} else {
				id, err = b.SendFile("discord:g1/ch-1", path, "result.txt", "result", "", "", "root-1")
			}
			if err == nil || id != "" || len(rec.snapshot()) != 1 {
				t.Fatalf("thread failure must refuse attachment: id=%q err=%v calls=%+v", id, err, rec.snapshot())
			}
			assertPath(t, rec, "POST", "/channels/ch-1/messages/root-1/threads")
		})
	}
}

func TestSendOpenerThreadKeepsAValidReplyReferenceRequest(t *testing.T) {
	rec := &recorded{}
	srv := newThreadDiscordServer(t, rec, http.StatusOK, `{"id":"root-1"}`)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()
	b := &bot{session: newTestSession(t)}
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:g1/ch-1", Content: "the answer",
		ReplyTo: "human-parent", ThreadRoot: "root-1",
	}); err != nil {
		t.Fatal(err)
	}
	for _, request := range rec.snapshot() {
		if request.Path == "/channels/root-1/messages" &&
			!strings.Contains(request.Body, `"message_reference":{"message_id":"human-parent","fail_if_not_exists":false}`) {
			t.Fatalf("reply reference can reject the thread post: %s", request.Body)
		}
	}
	assertPath(t, rec, "POST", "/channels/root-1/messages")
}
