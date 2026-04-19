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
	req.Header.Set("User-Agent", chanlib.UserAgent)
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
	req.Header.Set("User-Agent", chanlib.UserAgent)
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
		return
	}
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", jid, "message_id", uriToKey(n.URI), "content_len", len(content), "verb", verb)
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
	if err := bc.xrpc("POST", "com.atproto.repo.createRecord", nil, body, nil); err != nil {
		return "", err
	}
	slog.Debug("send", "chat_jid", req.ChatJID, "source", "bluesky")
	return "", nil
}

func (bc *bskyClient) Typing(string, bool) {}

func (bc *bskyClient) Post(req chanlib.PostRequest) (string, error) {
	if len(req.MediaPaths) > 0 {
		return "", fmt.Errorf("bluesky post: media upload not implemented")
	}
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      req.Content,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	body := map[string]any{
		"repo":       bc.getSession().DID,
		"collection": "app.bsky.feed.post",
		"record":     record,
	}
	var result struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}
	if err := bc.xrpc("POST", "com.atproto.repo.createRecord", nil, body, &result); err != nil {
		return "", fmt.Errorf("bluesky post: %w", err)
	}
	return result.URI, nil
}

func (bc *bskyClient) React(req chanlib.ReactRequest) error {
	cid, err := bc.getPostCID(req.TargetID)
	if err != nil {
		return fmt.Errorf("bluesky like: get cid: %w", err)
	}
	record := map[string]any{
		"$type":     "app.bsky.feed.like",
		"subject":   map[string]string{"uri": req.TargetID, "cid": cid},
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	body := map[string]any{
		"repo":       bc.getSession().DID,
		"collection": "app.bsky.feed.like",
		"record":     record,
	}
	if err := bc.xrpc("POST", "com.atproto.repo.createRecord", nil, body, nil); err != nil {
		return fmt.Errorf("bluesky like: %w", err)
	}
	return nil
}

func (bc *bskyClient) DeletePost(req chanlib.DeleteRequest) error {
	parts := strings.Split(req.TargetID, "/")
	if len(parts) < 5 {
		return fmt.Errorf("bluesky delete: target_id must be an at:// URI")
	}
	body := map[string]any{
		"repo":       parts[2],
		"collection": "app.bsky.feed.post",
		"rkey":       parts[len(parts)-1],
	}
	if err := bc.xrpc("POST", "com.atproto.repo.deleteRecord", nil, body, nil); err != nil {
		return fmt.Errorf("bluesky delete: %w", err)
	}
	return nil
}

type feedViewPost struct {
	Post struct {
		URI    string `json:"uri"`
		Author struct {
			DID         string `json:"did"`
			Handle      string `json:"handle"`
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Record    json.RawMessage `json:"record"`
		IndexedAt string          `json:"indexedAt"`
	} `json:"post"`
	Reply *struct {
		Parent struct {
			URI string `json:"uri"`
		} `json:"parent"`
	} `json:"reply,omitempty"`
}

// FetchHistory returns recent posts authored by the DID encoded in ChatJID
// via app.bsky.feed.getAuthorFeed. Bluesky uses opaque cursors, so Before
// is applied client-side by filtering indexedAt < Before and paginating
// while the page's oldest item is still after Before (up to 5 pages).
// Limit is clamped to [1, 100] per Bluesky's API cap.
func (bc *bskyClient) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	did := strings.TrimPrefix(req.ChatJID, "bluesky:")
	if did == "" {
		return chanlib.HistoryResponse{}, fmt.Errorf("invalid chat_jid")
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var out []chanlib.InboundMsg
	cursor := ""
	for page := 0; page < 5 && len(out) < limit; page++ {
		params := map[string]string{"actor": did, "limit": fmt.Sprintf("%d", limit)}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Feed   []feedViewPost `json:"feed"`
			Cursor string         `json:"cursor"`
		}
		if err := bc.xrpc("GET", "app.bsky.feed.getAuthorFeed", params, nil, &result); err != nil {
			return chanlib.HistoryResponse{}, err
		}
		if len(result.Feed) == 0 {
			break
		}
		for _, fv := range result.Feed {
			ts, _ := time.Parse(time.RFC3339, fv.Post.IndexedAt)
			if !req.Before.IsZero() && !ts.Before(req.Before) {
				continue
			}
			var rec struct {
				Text  string `json:"text"`
				Reply *struct {
					Parent struct {
						URI string `json:"uri"`
					} `json:"parent"`
				} `json:"reply,omitempty"`
			}
			json.Unmarshal(fv.Post.Record, &rec)
			name := fv.Post.Author.DisplayName
			if name == "" {
				name = fv.Post.Author.Handle
			}
			replyTo := ""
			if rec.Reply != nil {
				replyTo = uriToKey(rec.Reply.Parent.URI)
			}
			out = append(out, chanlib.InboundMsg{
				ID:         uriToKey(fv.Post.URI),
				ChatJID:    req.ChatJID,
				Sender:     "bluesky:" + fv.Post.Author.DID,
				SenderName: name,
				Content:    rec.Text,
				Timestamp:  ts.Unix(),
				ReplyTo:    replyTo,
			})
			if len(out) >= limit {
				break
			}
		}
		if result.Cursor == "" || result.Cursor == cursor {
			break
		}
		cursor = result.Cursor
	}
	return chanlib.HistoryResponse{Source: "platform", Messages: out}, nil
}

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
		req.Header.Set("User-Agent", chanlib.UserAgent)
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
		resp, err := chanlib.DoWithRetry(bc.http, req)
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
