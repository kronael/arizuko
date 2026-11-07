package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func (b *bot) passthrough(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		expectedHash, suppliedHash := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(supplied))
		if token == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(expectedHash[:], suppliedHash[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		api, path, ok := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/google/"), "/")
		b.cred.mu.Lock()
		var base string
		var client *http.Client
		switch api {
		case "drive":
			if b.cred.driveAPI.state == "on" {
				base, client = b.cred.apiBase, b.cred.http
			}
		case "sheets":
			if b.cred.sheetsAPI.state == "on" {
				base, client = b.cred.sheetsAPIBase, b.cred.sheetsHTTP
			}
		}
		b.cred.mu.Unlock()
		if !ok || client == nil {
			http.NotFound(w, r)
			return
		}
		decodedPath, err := url.PathUnescape("/" + path)
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		target, err := url.Parse(base)
		if err != nil {
			http.Error(w, "invalid API URL", http.StatusBadGateway)
			return
		}
		proxy := &httputil.ReverseProxy{Transport: client.Transport, Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = decodedPath
			pr.Out.URL.RawPath = "/" + path
			pr.Out.Header.Del("Authorization")
		}}
		proxy.ServeHTTP(w, r)
	})
}
