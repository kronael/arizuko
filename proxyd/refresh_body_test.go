package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type trackedRefreshBody struct {
	io.Reader
	closed bool
}

func (b *trackedRefreshBody) Close() error {
	b.closed = true
	return nil
}

type failedRefreshTransport struct{ body *trackedRefreshBody }

func (p failedRefreshTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 401, Body: p.body, Header: make(http.Header)}, nil
}

func TestFailedRefreshClosesBody(t *testing.T) {
	_, ks := es256KeySet(t)
	s := &server{cfg: config{authdURL: "http://authd"}, ks: ks}
	body := &trackedRefreshBody{Reader: strings.NewReader("invalid session")}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: failedRefreshTransport{body}}
	t.Cleanup(func() { http.DefaultClient = old })
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "refresh_token", Value: "revoked"})
	if got := s.tryRefreshViaAuthd(httptest.NewRecorder(), r); got != nil {
		t.Fatal("failed refresh authenticated")
	}
	if !body.closed {
		t.Fatal("401 refresh response body remains open")
	}
}
