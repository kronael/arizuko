package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type session struct {
	DID        string `json:"did"`
	AccessJwt  string `json:"accessJwt"`
	RefreshJwt string `json:"refreshJwt"`
}

type bskyClient struct {
	cfg     config
	session session
	http    *http.Client
}

func newBskyClient(cfg config) (*bskyClient, error) {
	bc := &bskyClient{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
	if err := bc.auth(); err != nil {
		return nil, err
	}
	return bc, nil
}

func (bc *bskyClient) auth() error {
	if s := bc.loadSession(); s != nil {
		if err := bc.refreshSession(s.RefreshJwt); err == nil {
			return nil
		}
	}
	return bc.createSession()
}

func (bc *bskyClient) sessionPath() string {
	return filepath.Join(bc.cfg.DataDir, "bluesky-session.json")
}

func (bc *bskyClient) loadSession() *session {
	b, err := os.ReadFile(bc.sessionPath())
	if err != nil {
		return nil
	}
	var s session
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

func (bc *bskyClient) saveSession() {
	os.MkdirAll(bc.cfg.DataDir, 0o755)
	b, _ := json.Marshal(bc.session)
	os.WriteFile(bc.sessionPath(), b, 0o600)
}

func (bc *bskyClient) createSession() error {
	body := map[string]string{"identifier": bc.cfg.Identifier, "password": bc.cfg.Password}
	var s session
	if err := bc.xrpcWithAuth("POST", "com.atproto.server.createSession", nil, body, &s, ""); err != nil {
		return fmt.Errorf("createSession: %w", err)
	}
	bc.session = s
	bc.saveSession()
	slog.Info("bluesky authenticated", "did", s.DID)
	return nil
}

func (bc *bskyClient) refreshSession(refreshJwt string) error {
	req, _ := http.NewRequest("POST",
		bc.cfg.Service+"/xrpc/com.atproto.server.refreshSession", nil)
	req.Header.Set("Authorization", "Bearer "+refreshJwt)
	resp, err := bc.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("refresh: status %d", resp.StatusCode)
	}
	var s session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return err
	}
	bc.session = s
	bc.saveSession()
	return nil
}

func (bc *bskyClient) poll(ctx context.Context, rc *routerClient) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		if err := bc.fetchNotifications(rc); err != nil {
			slog.Error("notification poll error", "err", err)
		}
	}
}

type notification struct {
	URI       string `json:"uri"`
	Reason    string `json:"reason"`
	IsRead    bool   `json:"isRead"`
	IndexedAt string `json:"indexedAt"`
	Author    struct {
		DID         string `json:"did"`
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Record struct {
		Text  string `json:"text"`
		Type  string `json:"$type"`
		Reply *struct {
			Parent struct {
				URI string `json:"uri"`
			} `json:"parent"`
		} `json:"reply,omitempty"`
	} `json:"record"`
}

func (bc *bskyClient) fetchNotifications(rc *routerClient) error {
	var result struct {
		Notifications []notification `json:"notifications"`
	}
	params := map[string]string{"reasons": "mention,reply", "limit": "25"}
	if err := bc.xrpcWithAuth("GET", "app.bsky.notification.listNotifications", params, nil, &result, bc.session.AccessJwt); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var processed int
	for _, n := range result.Notifications {
		if n.IsRead {
			continue
		}
		bc.handleNotification(n, rc)
		processed++
	}

	if processed > 0 {
		bc.xrpcWithAuth("POST", "app.bsky.notification.updateSeen", nil,
			map[string]string{"seenAt": now}, nil, bc.session.AccessJwt)
	}
	return nil
}

func (bc *bskyClient) handleNotification(n notification, rc *routerClient) {
	jid := "bluesky:" + n.Author.DID
	name := n.Author.DisplayName
	if name == "" {
		name = n.Author.Handle
	}
	_ = rc.SendChat(jid, name, false)

	topic := ""
	if n.Record.Reply != nil {
		topic = n.Record.Reply.Parent.URI
	}

	verb := "message"
	switch n.Reason {
	case "reply":
		verb = "reply"
	case "mention":
		verb = "message"
	}

	ts, _ := time.Parse(time.RFC3339, n.IndexedAt)
	err := rc.SendMessage(inboundMsg{
		ID:         uriToKey(n.URI),
		ChatJID:    jid,
		Sender:     "bluesky:" + n.Author.DID,
		SenderName: name,
		Content:    n.Record.Text,
		Timestamp:  ts.Unix(),
		IsGroup:    false,
		Topic:      topic,
		Verb:       verb,
	})
	if err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (bc *bskyClient) createPost(ctx context.Context, text, replyParentURI string) error {
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      text,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}

	if replyParentURI != "" {
		cid, err := bc.getPostCID(replyParentURI)
		if err != nil {
			return fmt.Errorf("get parent cid: %w", err)
		}
		ref := map[string]string{"uri": replyParentURI, "cid": cid}
		record["reply"] = map[string]any{"root": ref, "parent": ref}
	}

	body := map[string]any{
		"repo":       bc.session.DID,
		"collection": "app.bsky.feed.post",
		"record":     record,
	}
	return bc.xrpcAuth("POST", "com.atproto.repo.createRecord", nil, body, nil)
}

func (bc *bskyClient) getPostCID(uri string) (string, error) {
	// at://did/collection/rkey
	parts := strings.Split(uri, "/")
	if len(parts) < 5 {
		return "", fmt.Errorf("invalid uri: %s", uri)
	}
	repo := parts[2]
	rkey := parts[len(parts)-1]
	params := map[string]string{
		"repo": repo, "collection": "app.bsky.feed.post", "rkey": rkey,
	}
	var result struct {
		CID string `json:"cid"`
	}
	if err := bc.xrpcAuth("GET", "com.atproto.repo.getRecord", params, nil, &result); err != nil {
		return "", err
	}
	return result.CID, nil
}

func (bc *bskyClient) xrpcAuth(method, nsid string, params map[string]string, body any, out any) error {
	err := bc.xrpcWithAuth(method, nsid, params, body, out, bc.session.AccessJwt)
	if err != nil && strings.Contains(err.Error(), "401") {
		if rerr := bc.refreshSession(bc.session.RefreshJwt); rerr != nil {
			bc.createSession()
		}
		return bc.xrpcWithAuth(method, nsid, params, body, out, bc.session.AccessJwt)
	}
	return err
}

func (bc *bskyClient) xrpcWithAuth(method, nsid string, params map[string]string, body any, out any, jwt string) error {
	url := bc.cfg.Service + "/xrpc/" + nsid

	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	q := req.URL.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := bc.http.Do(req)
	if err != nil {
		return fmt.Errorf("xrpc %s: %w", nsid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("xrpc %s: status %d: %s", nsid, resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func uriToKey(uri string) string {
	parts := strings.Split(uri, "/")
	return parts[len(parts)-1]
}
