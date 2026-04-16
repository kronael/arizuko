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
	b, _ := json.Marshal(rc.cursors)
	os.MkdirAll(rc.cfg.DataDir, 0o755)
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
	var tr tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("token decode: %w", err)
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
	tok := rc.token
	rc.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", rc.cfg.UserAgent)
	return rc.doWithRetry(req)
}

func (rc *redditClient) doWithRetry(req *http.Request) (*http.Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := rc.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			wait := 5 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				var secs float64
				fmt.Sscanf(ra, "%f", &secs)
				wait = time.Duration(secs) * time.Second
			}
			time.Sleep(wait)
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
	b, _ := io.ReadAll(resp.Body)

	var l listing
	if json.Unmarshal(b, &l) != nil {
		return
	}

	if len(l.Data.Children) > 0 {
		rc.cursors[key] = l.Data.Children[0].Data.Name
		rc.saveCursors()
	}

	// Skip first poll for new sources (no persisted cursor) to avoid replaying history.
	if prevCursor == "" && !rc.skipFirst[key] {
		rc.skipFirst[key] = true
		return
	}
	for _, t := range l.Data.Children {
		rc.handleThing(t, key, router)
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

	// t1=comment, t3=post, t4=DM
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
	if d.Media != nil && d.Media.RedditVideo != nil && d.Media.RedditVideo.FallbackURL != "" {
		return []chanlib.InboundAttachment{rc.makeAttachment(d.Media.RedditVideo.FallbackURL, "video/mp4", "video.mp4")}
	}
	if d.IsGallery && d.GalleryData != nil && d.MediaMetadata != nil {
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
	}
	if d.URL != "" && isRedditImageURL(d.URL, d.PostHint) {
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
	default:
		return "image/jpeg"
	}
}

func filenameFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "image.jpg"
	}
	base := filepath.Base(parsed.Path)
	if base == "" || base == "." || base == "/" {
		return "image.jpg"
	}
	return base
}

func extFromRedditMime(m string) string {
	switch m {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
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
