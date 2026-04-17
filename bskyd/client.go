package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

type session struct {
	DID        string `json:"did"`
	AccessJwt  string `json:"accessJwt"`
	RefreshJwt string `json:"refreshJwt"`
}

type bskyClient struct {
	chanlib.NoFileSender
	cfg     config
	mu      sync.RWMutex // guards session
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
	if s := bc.loadSession(); s != nil && bc.refreshSession(s.RefreshJwt) == nil {
		return nil
	}
	return bc.createSession()
}

func (bc *bskyClient) storeSession(s session) {
	bc.mu.Lock()
	bc.session = s
	bc.mu.Unlock()
	bc.saveSession()
}

func (bc *bskyClient) getSession() session {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.session
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
	s := bc.getSession()
	b, _ := json.Marshal(s)
	os.WriteFile(bc.sessionPath(), b, 0o600)
}

// httpStatusError lets callers inspect the status code of an HTTP failure
// without string-matching the error message.
type httpStatusError struct {
	Code int
	Body string
	Op   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.Op, e.Code, e.Body)
}

func isHTTPStatus(err error, code int) bool {
	var h *httpStatusError
	if errors.As(err, &h) {
		return h.Code == code
	}
	return false
}

func (bc *bskyClient) createSession() error {
	body, _ := json.Marshal(map[string]string{
		"identifier": bc.cfg.Identifier, "password": bc.cfg.Password,
	})
	req, _ := http.NewRequest("POST",
		bc.cfg.Service+"/xrpc/com.atproto.server.createSession", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := bc.http.Do(req)
	if err != nil {
		return fmt.Errorf("createSession: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{Code: resp.StatusCode, Body: string(b), Op: "createSession"}
	}
	var s session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return err
	}
	bc.storeSession(s)
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{Code: resp.StatusCode, Body: string(b), Op: "refreshSession"}
	}
	var s session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return err
	}
	bc.storeSession(s)
	return nil
}

func (bc *bskyClient) poll(ctx context.Context, rc *chanlib.RouterClient) {
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
		Embed *embedRecord `json:"embed,omitempty"`
	} `json:"record"`
}

type embedRecord struct {
	Type   string       `json:"$type"`
	Images []embedImage `json:"images,omitempty"`
}

type embedImage struct {
	Alt   string  `json:"alt"`
	Image blobRef `json:"image"`
}

type blobRef struct {
	Type     string      `json:"$type"`
	Ref      blobRefLink `json:"ref"`
	MimeType string      `json:"mimeType"`
	Size     int64       `json:"size"`
}

type blobRefLink struct {
	Link string `json:"$link"`
}

func (bc *bskyClient) fetchNotifications(rc *chanlib.RouterClient) error {
	var result struct {
		Notifications []notification `json:"notifications"`
	}
	params := map[string]string{"reasons": "mention,reply", "limit": "25"}
	if err := bc.xrpc("GET", "app.bsky.notification.listNotifications", params, nil, &result); err != nil {
		return err
	}
	// API returns newest-first. Walk oldest→newest, handling each, then
	// updateSeen with the processed item's IndexedAt. This ensures older
	// unread notifications beyond the 25-item window stay unread (not
	// silently dropped by a bulk seenAt=now).
	ns := result.Notifications
	for i := len(ns) - 1; i >= 0; i-- {
		n := ns[i]
		if n.IsRead {
			continue
		}
		bc.handleNotification(n, rc)
		// Advance seen pointer to the just-processed notification's timestamp.
		// Use IndexedAt as-is so the server's clock is authoritative.
		if n.IndexedAt != "" {
			_ = bc.xrpc("POST", "app.bsky.notification.updateSeen", nil,
				map[string]string{"seenAt": n.IndexedAt}, nil)
		}
	}
	return nil
}

func (bc *bskyClient) handleNotification(n notification, rc *chanlib.RouterClient) {
	jid := "bluesky:" + n.Author.DID
	name := n.Author.DisplayName
	if name == "" {
		name = n.Author.Handle
	}
	topic := ""
	if n.Record.Reply != nil {
		topic = n.Record.Reply.Parent.URI
	}
	verb := "message"
	if n.Reason == "reply" {
		verb = "reply"
	}
	atts := bc.extractAttachments(n)
	content := n.Record.Text
	for _, a := range atts {
		content += fmt.Sprintf(" [Image: %s]", a.Filename)
	}
	ts, _ := time.Parse(time.RFC3339, n.IndexedAt)
	if err := rc.SendMessage(chanlib.InboundMsg{
		ID:          uriToKey(n.URI),
		ChatJID:     jid,
		Sender:      jid,
		SenderName:  name,
		Content:     content,
		Timestamp:   ts.Unix(),
		Topic:       topic,
		Verb:        verb,
		Attachments: atts,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (bc *bskyClient) extractAttachments(n notification) []chanlib.InboundAttachment {
	if n.Record.Embed == nil || n.Record.Embed.Type != "app.bsky.embed.images" {
		return nil
	}
	var atts []chanlib.InboundAttachment
	for i, img := range n.Record.Embed.Images {
		cid := img.Image.Ref.Link
		if cid == "" {
			continue
		}
		url := ""
		if bc.cfg.ListenURL != "" {
			url = bc.cfg.ListenURL + "/files/" + n.Author.DID + "/" + cid
		}
		atts = append(atts, chanlib.InboundAttachment{
			Mime:     img.Image.MimeType,
			Filename: fmt.Sprintf("image_%d%s", i, blobExt(img.Image.MimeType)),
			URL:      url,
			Size:     img.Image.Size,
		})
	}
	return atts
}

func blobExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".bin"
}

func (bc *bskyClient) Send(req chanlib.SendRequest) (string, error) {
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      req.Content,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	if req.ReplyTo != "" {
		cid, err := bc.getPostCID(req.ReplyTo)
		if err != nil {
			return "", fmt.Errorf("get parent cid: %w", err)
		}
		ref := map[string]string{"uri": req.ReplyTo, "cid": cid}
		record["reply"] = map[string]any{"root": ref, "parent": ref}
	}
	body := map[string]any{
		"repo":       bc.getSession().DID,
		"collection": "app.bsky.feed.post",
		"record":     record,
	}
	return "", bc.xrpc("POST", "com.atproto.repo.createRecord", nil, body, nil)
}

func (bc *bskyClient) Typing(string, bool) {}

func (bc *bskyClient) getPostCID(uri string) (string, error) {
	parts := strings.Split(uri, "/")
	if len(parts) < 5 {
		return "", fmt.Errorf("invalid uri: %s", uri)
	}
	var result struct {
		CID string `json:"cid"`
	}
	params := map[string]string{"repo": parts[2], "collection": "app.bsky.feed.post", "rkey": parts[len(parts)-1]}
	if err := bc.xrpc("GET", "com.atproto.repo.getRecord", params, nil, &result); err != nil {
		return "", err
	}
	return result.CID, nil
}

func (bc *bskyClient) xrpc(method, nsid string, params map[string]string, body, out any) error {
	do := func() error {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, bc.cfg.Service+"/xrpc/"+nsid, r)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		s := bc.getSession()
		req.Header.Set("Authorization", "Bearer "+s.AccessJwt)
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
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return &httpStatusError{Code: resp.StatusCode, Body: string(b), Op: "xrpc " + nsid}
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	err := do()
	if err != nil && isHTTPStatus(err, 401) {
		// Token rejected: try refresh; if refresh itself fails, propagate both
		// the original 401 and the refresh error instead of silently retrying
		// with a stale token.
		refreshErr := bc.refreshSession(bc.getSession().RefreshJwt)
		if refreshErr != nil {
			if cerr := bc.createSession(); cerr != nil {
				return fmt.Errorf("xrpc %s: 401 and re-auth failed: refresh=%v create=%w",
					nsid, refreshErr, cerr)
			}
		}
		return do()
	}
	return err
}

func uriToKey(uri string) string {
	parts := strings.Split(uri, "/")
	return parts[len(parts)-1]
}
