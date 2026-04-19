package main

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

var allowedRedditHosts = map[string]bool{
	"i.redd.it":                true,
	"v.redd.it":                true,
	"preview.redd.it":          true,
	"external-preview.redd.it": true,
	"i.imgur.com":              true,
	"imgur.com":                true,
}

type server struct {
	cfg       config
	rc        chanlib.BotHandler
	files     *chanlib.URLCache
	safeFetch func(string) bool
}

func newServer(cfg config, rc chanlib.BotHandler, files *chanlib.URLCache) *server {
	return &server{cfg: cfg, rc: rc, files: files, safeFetch: isSafeFetchURL}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"reddit:"}, s.rc)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/files/")
	if id == "" {
		chanlib.WriteErr(w, 400, "file_id required")
		return
	}
	rawURL, ok := s.files.Get(id)
	if !ok {
		chanlib.WriteErr(w, 404, "file not found")
		return
	}
	if s.safeFetch != nil && !s.safeFetch(rawURL) {
		chanlib.WriteErr(w, 400, "disallowed url")
		return
	}
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		chanlib.WriteErr(w, 502, "download failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		chanlib.WriteErr(w, 502, "download failed")
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	max := s.cfg.MaxFileBytes
	if max <= 0 {
		max = 20 * 1024 * 1024
	}
	io.Copy(w, io.LimitReader(resp.Body, max))
}

func isSafeFetchURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if !allowedRedditHosts[strings.ToLower(host)] {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return false
		}
	}
	return true
}
