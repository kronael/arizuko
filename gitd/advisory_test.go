package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestWebhook_SenderlessSecurityAdvisory(t *testing.T) {
	e := newTestEnv(t, secretMap{"app/example": "secret"})
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	body := []byte(`{"action":"published","installation":{"id":123}}`)
	w := e.deliver(t, "/github/app/example", "secret", "security_advisory", "advisory", body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dropped":"unsubscribed"`) {
		t.Errorf("valid advisory: status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "level=DEBUG") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("valid advisory logs: %s", logs.String())
	}
	w = e.deliver(t, "/github/app/example", "wrong", "security_advisory", "bad-signature", body)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: status=%d body=%s", w.Code, w.Body.String())
	}
	w = e.deliver(t, "/github/app/example", "secret", "security_advisory", "bad-source", []byte(`{"action":"published"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad source: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := e.routd.messages(); len(got) != 0 {
		t.Errorf("routed messages: %+v", got)
	}
}
