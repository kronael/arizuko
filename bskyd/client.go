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
	"sync/atomic"
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
	chanlib.NoVoiceSender
	chanlib.NoSocial
	cfg     config
	mu      sync.RWMutex // guards session
	session session
	http    *http.Client
	// authed reflects whether the most recent auth/refresh succeeded.
	// Set true in createSession/refreshSession; cleared when both refresh
	// and create fail in the xrpc 401 handler.
	authed        atomic.Bool
	lastInboundAt atomic.Int64
}

func (bc *bskyClient) isConnected() bool    { return bc.authed.Load() }
func (bc *bskyClient) LastInboundAt() int64 { return bc.lastInboundAt.Load() }

func newBskyClient(cfg config) (*bskyClient, error) {
	bc := &bskyClient{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
	bc.lastInboundAt.Store(time.Now().Unix())
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
	bc.authed.Store(true)
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
	bc.authed.Store(true)
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

// bskyUserJID renders a canonical Bluesky user JID. DIDs contain `:`
// (`did:plc:<rest>`), so we percent-encode them so `<rest>` survives
// `path.Match` glob semantics where `*` doesn't cross `/`.
func bskyUserJID(did string) string {
	return "bluesky:user/" + strings.ReplaceAll(did, ":", "%3A")
}

// bskyDIDFromJID reverses bskyUserJID for outbound calls. Accepts both
// legacy `bluesky:<did>` and typed `bluesky:user/<encoded>`.
func bskyDIDFromJID(jid string) string {
	if strings.HasPrefix(jid, "bluesky:user/") {
		enc := strings.TrimPrefix(jid, "bluesky:user/")
		return strings.ReplaceAll(enc, "%3A", ":")
	}
	return strings.TrimPrefix(jid, "bluesky:")
}

func (bc *bskyClient) handleNotification(n notification, rc *chanlib.RouterClient) {
	jid := bskyUserJID(n.Author.DID)
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
		// All current bskyd inbound is feed-side (replies, mentions,
		// likes on public posts). DM API isn't wired yet; when it is,
		// classify those handlers IsGroup=false.
		IsGroup: true,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	bc.lastInboundAt.Store(time.Now().Unix())
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

// createRecord posts a record to com.atproto.repo.createRecord and returns
// the URI assigned by the PDS. Centralises the repo/collection/record
// envelope shared by Send/Post/Like/Quote/Repost/SendFile.
func (bc *bskyClient) createRecord(collection string, record map[string]any) (string, error) {
	body := map[string]any{
		"repo":       bc.getSession().DID,
		"collection": collection,
		"record":     record,
	}
	var result struct {
		URI string `json:"uri"`
	}
	if err := bc.xrpc("POST", "com.atproto.repo.createRecord", nil, body, &result); err != nil {
		return "", err
	}
	return result.URI, nil
}

// strongRef builds a {uri, cid} reference by fetching the target's CID.
func (bc *bskyClient) strongRef(uri string) (map[string]string, error) {
	cid, err := bc.getPostCID(uri)
	if err != nil {
		return nil, err
	}
	return map[string]string{"uri": uri, "cid": cid}, nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func (bc *bskyClient) Send(req chanlib.SendRequest) (string, error) {
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      req.Content,
		"createdAt": nowRFC3339(),
	}
	if req.ReplyTo != "" {
		ref, err := bc.strongRef(req.ReplyTo)
		if err != nil {
			return "", fmt.Errorf("get parent cid: %w", err)
		}
		record["reply"] = map[string]any{"root": ref, "parent": ref}
	}
	if _, err := bc.createRecord("app.bsky.feed.post", record); err != nil {
		return "", err
	}
	return "", nil
}

func (bc *bskyClient) Typing(string, bool) {}

// SendFile dispatches by extension. Bluesky's PDS only accepts image
// blobs in app.bsky.feed.post embeds (no native video/audio/document);
// non-image extensions return a structured Unsupported teaching the
// agent to send a link in the post text instead.
func (bc *bskyClient) SendFile(_, path, name, caption string) error {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(path))
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		embed, err := bc.uploadImageEmbed(path, name)
		if err != nil {
			return fmt.Errorf("bluesky send_file: %w", err)
		}
		_, err = bc.createRecord("app.bsky.feed.post", map[string]any{
			"$type":     "app.bsky.feed.post",
			"text":      caption,
			"createdAt": nowRFC3339(),
			"embed":     embed,
		})
		return err
	default:
		return chanlib.Unsupported("send_file", "bluesky",
			"Bluesky's PDS embed surface only accepts image blobs (.jpg/.png/.webp/.gif). For video/audio/documents host the file elsewhere and `send(content=<url>)` — Bluesky auto-renders a link card.")
	}
}

func (bc *bskyClient) Post(req chanlib.PostRequest) (string, error) {
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      req.Content,
		"createdAt": nowRFC3339(),
	}
	if len(req.MediaPaths) > 0 {
		embed, err := bc.uploadImageEmbed(req.MediaPaths[0], "")
		if err != nil {
			return "", fmt.Errorf("bluesky post: %w", err)
		}
		record["embed"] = embed
	}
	uri, err := bc.createRecord("app.bsky.feed.post", record)
	if err != nil {
		return "", fmt.Errorf("bluesky post: %w", err)
	}
	return uri, nil
}

func (bc *bskyClient) Like(req chanlib.LikeRequest) error {
	ref, err := bc.strongRef(req.TargetID)
	if err != nil {
		return fmt.Errorf("bluesky like: %w", err)
	}
	_, err = bc.createRecord("app.bsky.feed.like", map[string]any{
		"$type":     "app.bsky.feed.like",
		"subject":   ref,
		"createdAt": nowRFC3339(),
	})
	return err
}

func (bc *bskyClient) Delete(req chanlib.DeleteRequest) error {
	repo, rkey, err := splitATURI(req.TargetID)
	if err != nil {
		return fmt.Errorf("bluesky delete: %w", err)
	}
	body := map[string]any{
		"repo":       repo,
		"collection": "app.bsky.feed.post",
		"rkey":       rkey,
	}
	return bc.xrpc("POST", "com.atproto.repo.deleteRecord", nil, body, nil)
}

func (bc *bskyClient) Forward(chanlib.ForwardRequest) (string, error) {
	return "", chanlib.Unsupported("forward", "bluesky",
		"Bluesky has no forward primitive. Use `repost(source_msg_id=...)` to amplify, or `quote(comment=...)` to share with commentary.")
}

func (bc *bskyClient) Quote(req chanlib.QuoteRequest) (string, error) {
	ref, err := bc.strongRef(req.SourceMsgID)
	if err != nil {
		return "", fmt.Errorf("bluesky quote: %w", err)
	}
	return bc.createRecord("app.bsky.feed.post", map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      req.Comment,
		"createdAt": nowRFC3339(),
		"embed": map[string]any{
			"$type":  "app.bsky.embed.record",
			"record": ref,
		},
	})
}

func (bc *bskyClient) Repost(req chanlib.RepostRequest) (string, error) {
	ref, err := bc.strongRef(req.SourceMsgID)
	if err != nil {
		return "", fmt.Errorf("bluesky repost: %w", err)
	}
	return bc.createRecord("app.bsky.feed.repost", map[string]any{
		"$type":     "app.bsky.feed.repost",
		"subject":   ref,
		"createdAt": nowRFC3339(),
	})
}

func (bc *bskyClient) Dislike(chanlib.DislikeRequest) error {
	return chanlib.Unsupported("dislike", "bluesky",
		"Bluesky has no native downvote. Use `reply` with textual disagreement instead.")
}

// Edit: putRecord succeeds at the PDS but Bluesky's appview ignores updates
// to app.bsky.feed.post records, so the edit never reaches the feed. Stay
// a hint until the appview prohibition is lifted.
func (bc *bskyClient) Edit(chanlib.EditRequest) error {
	return chanlib.Unsupported("edit", "bluesky",
		"Bluesky's appview rejects post edits. Use `delete(target_id=...)` then `post(content=...)` to retract-and-resend.")
}

// uploadImageEmbed reads a local file, uploads it as a blob, and returns
// an app.bsky.embed.images embed referencing the uploaded blob.
func (bc *bskyClient) uploadImageEmbed(path, alt string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	blob, err := bc.uploadBlob(data, mimeFromExt(path))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"$type":  "app.bsky.embed.images",
		"images": []map[string]any{{"alt": alt, "image": blob}},
	}, nil
}

func (bc *bskyClient) uploadBlob(data []byte, mime string) (map[string]any, error) {
	req, err := http.NewRequest("POST", bc.cfg.Service+"/xrpc/com.atproto.repo.uploadBlob", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mime)
	req.Header.Set("Authorization", "Bearer "+bc.getSession().AccessJwt)
	req.Header.Set("User-Agent", chanlib.UserAgent)
	resp, err := chanlib.DoWithRetry(bc.http, req)
	if err != nil {
		return nil, fmt.Errorf("uploadBlob: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpStatusError{Code: resp.StatusCode, Body: string(b), Op: "uploadBlob"}
	}
	var result struct {
		Blob map[string]any `json:"blob"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Blob, nil
}

func mimeFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

// splitATURI parses an at://<repo>/<collection>/<rkey> URI into its repo
// and rkey components.
func splitATURI(uri string) (repo, rkey string, err error) {
	parts := strings.Split(uri, "/")
	if len(parts) < 5 {
		return "", "", fmt.Errorf("invalid at:// uri: %s", uri)
	}
	return parts[2], parts[len(parts)-1], nil
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
	did := bskyDIDFromJID(req.ChatJID)
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
				Sender:     bskyUserJID(fv.Post.Author.DID),
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
	repo, rkey, err := splitATURI(uri)
	if err != nil {
		return "", err
	}
	var result struct {
		CID string `json:"cid"`
	}
	params := map[string]string{"repo": repo, "collection": "app.bsky.feed.post", "rkey": rkey}
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
				bc.authed.Store(false)
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
