package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

type tokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type redditClient struct {
	chanlib.NoFileSender
	cfg       config
	http      *http.Client
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	cursors   map[string]string
	skipFirst map[string]bool
	files     *fileCache
}

func newRedditClient(cfg config) (*redditClient, error) {
	rc := &redditClient{
		cfg:       cfg,
		http:      &http.Client{Timeout: 15 * time.Second},
		cursors:   map[string]string{},
		skipFirst: map[string]bool{},
		files:     newFileCache(1000),
	}
	if err := rc.refreshToken(); err != nil {
		return nil, err
	}
	return rc, nil
}

func (rc *redditClient) loadCursors() {
	b, err := os.ReadFile(filepath.Join(rc.cfg.DataDir, "cursors.json"))
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &rc.cursors); err != nil {
		slog.Warn("cursors.json parse failed, starting fresh", "err", err)
	}
}

func (rc *redditClient) saveCursors() {
	os.MkdirAll(rc.cfg.DataDir, 0o755)
	b, _ := json.Marshal(rc.cursors)
	os.WriteFile(filepath.Join(rc.cfg.DataDir, "cursors.json"), b, 0o600)
}

func (rc *redditClient) refreshToken() error {
	data := url.Values{
		"grant_type": {"password"},
		"username":   {rc.cfg.Username},
		"password":   {rc.cfg.Password},
	}
	req, err := http.NewRequest("POST", "https://www.reddit.com/api/v1/access_token",
		strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(rc.cfg.ClientID, rc.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", rc.cfg.UserAgent)

	resp, err := rc.http.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("token request: status %d: %s", resp.StatusCode, string(b))
	}
	var tr tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("token decode: %w", err)
	}
	if tr.AccessToken == "" || tr.ExpiresIn <= 0 {
		return fmt.Errorf("token response missing access_token or expires_in")
	}
	rc.mu.Lock()
	rc.token = tr.AccessToken
	rc.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn-60) * time.Second)
	rc.mu.Unlock()
	slog.Info("reddit authenticated", "user", rc.cfg.Username)
	return nil
}

func (rc *redditClient) ensureToken() error {
	rc.mu.Lock()
	expired := time.Now().After(rc.expiresAt)
	rc.mu.Unlock()
	if expired {
		return rc.refreshToken()
	}
	return nil
}

func (rc *redditClient) do(method, path string, params map[string]string, form url.Values) (*http.Response, error) {
	if err := rc.ensureToken(); err != nil {
		return nil, err
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, "https://oauth.reddit.com"+path, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if len(params) > 0 {
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	rc.mu.Lock()
	req.Header.Set("Authorization", "Bearer "+rc.token)
	rc.mu.Unlock()
	req.Header.Set("User-Agent", rc.cfg.UserAgent)
	return rc.doWithRetry(req)
}

// maxRetryAfter caps Retry-After so a misbehaving upstream can't stall the
// poll goroutine for hours.
const maxRetryAfter = 5 * time.Minute

func (rc *redditClient) doWithRetry(req *http.Request) (*http.Response, error) {
	refreshedOn401 := false
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := chanlib.DoWithRetry(rc.http, req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			wait := 5 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				var secs float64
				if _, perr := fmt.Sscanf(ra, "%f", &secs); perr == nil && secs > 0 {
					wait = time.Duration(secs) * time.Second
				}
			}
			if wait > maxRetryAfter {
				wait = maxRetryAfter
			}
			time.Sleep(wait)
			continue
		}
		// 401: token may have been revoked early. Refresh once and retry.
		if resp.StatusCode == 401 && !refreshedOn401 {
			resp.Body.Close()
			refreshedOn401 = true
			if err := rc.refreshToken(); err != nil {
				return nil, fmt.Errorf("refresh after 401: %w", err)
			}
			rc.mu.Lock()
			req.Header.Set("Authorization", "Bearer "+rc.token)
			rc.mu.Unlock()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("rate limited after 3 retries")
}

type mediaMetaItem struct {
	Status string `json:"status"`
	Mime   string `json:"m"`
	S      struct {
		U string `json:"u"`
		X int    `json:"x"`
		Y int    `json:"y"`
	} `json:"s"`
}

type galleryItem struct {
	MediaID string `json:"media_id"`
}

type thing struct {
	Kind string `json:"kind"`
	Data struct {
		Name      string  `json:"name"`
		Author    string  `json:"author"`
		Body      string  `json:"body"`
		Selftext  string  `json:"selftext"`
		Title     string  `json:"title"`
		CreatedAt float64 `json:"created_utc"`
		ID        string  `json:"id"`
		ParentID  string  `json:"parent_id"`
		LinkID    string  `json:"link_id"`
		Subreddit string  `json:"subreddit"`
		URL       string  `json:"url"`
		PostHint  string  `json:"post_hint"`
		IsGallery bool    `json:"is_gallery"`
		Media     *struct {
			RedditVideo *struct {
				FallbackURL string `json:"fallback_url"`
			} `json:"reddit_video"`
		} `json:"media"`
		GalleryData *struct {
			Items []galleryItem `json:"items"`
		} `json:"gallery_data"`
		MediaMetadata map[string]mediaMetaItem `json:"media_metadata"`
	} `json:"data"`
}

type listing struct {
	Data struct {
		Before   string  `json:"before"`
		After    string  `json:"after"`
		Children []thing `json:"children"`
	} `json:"data"`
}

func (rc *redditClient) poll(ctx context.Context, router *chanlib.RouterClient) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rc.pollOnce(router)
		}
	}
}

func (rc *redditClient) pollOnce(router *chanlib.RouterClient) {
	rc.pollSource("inbox", "/message/inbox.json", router)
	for _, sr := range rc.cfg.Subreddits {
		rc.pollSource("sr:"+sr, "/r/"+sr+"/new.json", router)
	}
}

func (rc *redditClient) pollSource(key, path string, router *chanlib.RouterClient) {
	prevCursor := rc.cursors[key]
	params := map[string]string{"limit": "25"}
	if prevCursor != "" {
		params["before"] = prevCursor
	}

	resp, err := rc.do("GET", path, params, nil)
	if err != nil {
		slog.Error("reddit get failed", "path", path, "err", err)
		return
	}
	defer resp.Body.Close()
	var l listing
	if json.NewDecoder(resp.Body).Decode(&l) != nil {
		return
	}

	// Skip first poll for new sources (no persisted cursor) to avoid replaying history.
	// Still advance the cursor to the latest so subsequent polls start from here.
	if prevCursor == "" && !rc.skipFirst[key] {
		rc.skipFirst[key] = true
		if len(l.Data.Children) > 0 {
			rc.cursors[key] = l.Data.Children[0].Data.Name
			rc.saveCursors()
		}
		return
	}
	// Deliver oldest-first (reverse the newest-first listing) and advance the
	// cursor only after each successful delivery so a crash mid-batch doesn't
	// skip undelivered items.
	children := l.Data.Children
	for i := len(children) - 1; i >= 0; i-- {
		t := children[i]
		rc.handleThing(t, key, router)
		rc.cursors[key] = t.Data.Name
		rc.saveCursors()
	}
}

func (rc *redditClient) handleThing(t thing, key string, router *chanlib.RouterClient) {
	d := t.Data
	sender := "reddit:" + d.Author
	jid := sender
	if strings.HasPrefix(key, "sr:") {
		jid = "reddit:r_" + d.Subreddit
	}
	content := d.Body
	if content == "" {
		content = d.Title
		if d.Selftext != "" {
			content += "\n\n" + d.Selftext
		}
	}
	if content == "" {
		return
	}
	verb, topic := "message", ""
	switch t.Kind {
	case "t1":
		if d.ParentID != "" {
			verb = "reply"
			topic = d.ParentID
			if strings.HasPrefix(d.ParentID, "t3_") {
				topic = d.LinkID
			}
		}
	case "t3":
		verb = "post"
	}
	atts := rc.extractAttachments(t)
	for _, a := range atts {
		content += fmt.Sprintf(" [Attachment: %s]", a.Filename)
	}
	if err := router.SendMessage(chanlib.InboundMsg{
		ID:          d.Name,
		ChatJID:     jid,
		Sender:      sender,
		SenderName:  d.Author,
		Content:     content,
		Timestamp:   int64(d.CreatedAt),
		Topic:       topic,
		Verb:        verb,
		Attachments: atts,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

// FetchHistory pulls a subreddit listing (newest-first). JID shape
// `reddit:r_<sr>` maps to `/r/<sr>/new.json`; user DM JIDs are
// unsupported because /message/inbox isn't filterable by counterparty.
// Reddit listing endpoints cap depth at ~1000 items.
func (rc *redditClient) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	jid := req.ChatJID
	if !strings.HasPrefix(jid, "reddit:r_") {
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
	sr := strings.TrimPrefix(jid, "reddit:r_")
	if sr == "" {
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	params := map[string]string{"limit": fmt.Sprintf("%d", limit)}
	resp, err := rc.do("GET", "/r/"+sr+"/new.json", params, nil)
	if err != nil {
		return chanlib.HistoryResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return chanlib.HistoryResponse{}, fmt.Errorf("reddit listing: status %d: %s", resp.StatusCode, string(b))
	}
	var l listing
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return chanlib.HistoryResponse{}, err
	}
	before := req.Before
	msgs := make([]chanlib.InboundMsg, 0, len(l.Data.Children))
	// Reverse to oldest-first to match poll delivery order.
	for i := len(l.Data.Children) - 1; i >= 0; i-- {
		t := l.Data.Children[i]
		d := t.Data
		ts := int64(d.CreatedAt)
		if !before.IsZero() && ts >= before.Unix() {
			continue
		}
		content := d.Body
		if content == "" {
			content = d.Title
			if d.Selftext != "" {
				content += "\n\n" + d.Selftext
			}
		}
		if content == "" {
			continue
		}
		verb, topic := "message", ""
		switch t.Kind {
		case "t1":
			if d.ParentID != "" {
				verb = "reply"
				topic = d.ParentID
				if strings.HasPrefix(d.ParentID, "t3_") {
					topic = d.LinkID
				}
			}
		case "t3":
			verb = "post"
		}
		atts := rc.extractAttachments(t)
		for _, a := range atts {
			content += fmt.Sprintf(" [Attachment: %s]", a.Filename)
		}
		msgs = append(msgs, chanlib.InboundMsg{
			ID:          d.Name,
			ChatJID:     jid,
			Sender:      "reddit:" + d.Author,
			SenderName:  d.Author,
			Content:     content,
			Timestamp:   ts,
			Topic:       topic,
			Verb:        verb,
			Attachments: atts,
		})
	}
	return chanlib.HistoryResponse{Source: "platform-capped", Cap: "1000", Messages: msgs}, nil
}

func (rc *redditClient) Send(req chanlib.SendRequest) (string, error) {
	var data url.Values
	var path string
	if req.ReplyTo != "" {
		path = "/api/comment"
		data = url.Values{"thing_id": {req.ReplyTo}, "text": {req.Content}}
	} else {
		path = "/api/submit"
		data = url.Values{
			"kind":  {"self"},
			"sr":    {"u_" + rc.cfg.Username},
			"title": {"arizuko"},
			"text":  {req.Content},
		}
	}
	resp, err := rc.do("POST", path, nil, data)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return "", nil
}

func (rc *redditClient) Typing(string, bool) {}

func (rc *redditClient) extractAttachments(t thing) []chanlib.InboundAttachment {
	d := t.Data
	switch {
	case d.Media != nil && d.Media.RedditVideo != nil && d.Media.RedditVideo.FallbackURL != "":
		return []chanlib.InboundAttachment{rc.makeAttachment(d.Media.RedditVideo.FallbackURL, "video/mp4", "video.mp4")}
	case d.IsGallery && d.GalleryData != nil && d.MediaMetadata != nil:
		var atts []chanlib.InboundAttachment
		for _, item := range d.GalleryData.Items {
			meta, ok := d.MediaMetadata[item.MediaID]
			if !ok || meta.Status != "valid" || meta.S.U == "" {
				continue
			}
			imgURL := strings.ReplaceAll(meta.S.U, "&amp;", "&")
			atts = append(atts, rc.makeAttachment(imgURL, meta.Mime, item.MediaID+extFromRedditMime(meta.Mime)))
		}
		return atts
	case d.URL != "" && isRedditImageURL(d.URL, d.PostHint):
		return []chanlib.InboundAttachment{rc.makeAttachment(d.URL, mimeFromExt(d.URL), filenameFromURL(d.URL))}
	}
	return nil
}

func (rc *redditClient) makeAttachment(rawURL, mime, fname string) chanlib.InboundAttachment {
	id := rc.files.Put(rawURL)
	proxyURL := rc.cfg.ListenURL + "/files/" + id
	return chanlib.InboundAttachment{
		Mime:     mime,
		Filename: fname,
		URL:      proxyURL,
	}
}

func isRedditImageURL(u, hint string) bool {
	if hint == "image" {
		return true
	}
	if strings.Contains(u, "i.redd.it") {
		return true
	}
	lower := strings.ToLower(u)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(lower, ext) || strings.Contains(lower, ext+"?") {
			return true
		}
	}
	return false
}

func mimeFromExt(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.Contains(lower, ".png"):
		return "image/png"
	case strings.Contains(lower, ".gif"):
		return "image/gif"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	}
	return "image/jpeg"
}

func filenameFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "image.jpg"
	}
	if base := filepath.Base(parsed.Path); base != "" && base != "." && base != "/" {
		return base
	}
	return "image.jpg"
}

func extFromRedditMime(m string) string {
	switch m {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".jpg"
}

type fileCache struct {
	mu      sync.Mutex
	ids     map[string]string
	order   []string
	maxSize int
}

func newFileCache(max int) *fileCache {
	return &fileCache{ids: map[string]string{}, maxSize: max}
}

func (fc *fileCache) Put(rawURL string) string {
	h := sha256.Sum256([]byte(rawURL))
	id := hex.EncodeToString(h[:8])
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.ids[id]; !ok {
		fc.ids[id] = rawURL
		fc.order = append(fc.order, id)
		for len(fc.ids) > fc.maxSize {
			oldest := fc.order[0]
			fc.order = fc.order[1:]
			delete(fc.ids, oldest)
		}
	}
	return id
}

func (fc *fileCache) Get(id string) (string, bool) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	u, ok := fc.ids[id]
	return u, ok
}
