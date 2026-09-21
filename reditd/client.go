package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
)

type tokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type redditClient struct {
	chanlib.NoVoiceSender
	chanlib.NoSocial
	cfg       config
	http      *http.Client
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	cursors   map[string]string
	skipFirst map[string]bool
	files     *chanlib.URLCache
	// lastPollOK is the wall clock of the most recent successful poll.
	// isConnected treats the adapter as healthy when a poll succeeded in the
	// last pollStaleAfter; newly-started clients are healthy after auth.
	lastPollOK    atomic.Int64
	lastInboundAt atomic.Int64
}

func (rc *redditClient) LastInboundAt() int64 { return rc.lastInboundAt.Load() }

// pollStaleAfter is the tolerance before /health flips to 503.
const pollStaleAfter = 15 * time.Minute

func (rc *redditClient) isConnected() bool {
	last := rc.lastPollOK.Load()
	if last == 0 {
		return false
	}
	return time.Since(time.Unix(last, 0)) < pollStaleAfter
}

func (rc *redditClient) markPollOK() {
	rc.lastPollOK.Store(time.Now().Unix())
}

func newRedditClient(cfg config, chanStore *chanstore.Store) (*redditClient, error) {
	rc := &redditClient{
		cfg:       cfg,
		http:      &http.Client{Timeout: 15 * time.Second},
		cursors:   map[string]string{},
		skipFirst: map[string]bool{},
		files:     chanlib.NewURLCache(1000, chanStore),
	}
	rc.lastInboundAt.Store(time.Now().Unix())
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
	rc.markPollOK()
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
	policy := chanlib.RetryTransient
	if redditNonIdempotent[path] {
		policy = chanlib.RetryRateLimitOnly
	}
	return rc.doWithRetry(req, policy)
}

// redditNonIdempotent names the endpoints that CREATE something and take no
// idempotency key of their own. They retry a 429 — a documented refusal, with
// Retry-After — but never a 5xx or a dropped connection, which may have posted
// the thing already (BUGS L108, the class D5 named). Every other endpoint is a
// read or a mutation addressed by target id, so a full retry is free.
var redditNonIdempotent = map[string]bool{
	"/api/comment": true,
	"/api/submit":  true,
}

// doWithRetry adds the ONE thing chanlib's policy cannot do: refresh an expired
// token once and re-send. 429 and 5xx belong to chanlib.DoWithRetry and are not
// retried again here — a second loop around it multiplied one logical call into
// nine requests at the rate limiter (BUGS L124), and its own 5-minute
// Retry-After clamp contradicted chanlib's 30-second one.
func (rc *redditClient) doWithRetry(req *http.Request, policy chanlib.RetryPolicy) (*http.Response, error) {
	resp, err := chanlib.DoWithPolicy(rc.http, req, policy)
	if err != nil {
		return nil, err
	}
	// 401: the token may have been revoked early. Refresh once and re-send.
	if resp.StatusCode == 401 {
		resp.Body.Close()
		if err := rc.refreshToken(); err != nil {
			return nil, fmt.Errorf("refresh after 401: %w", err)
		}
		rc.mu.Lock()
		req.Header.Set("Authorization", "Bearer "+rc.token)
		rc.mu.Unlock()
		resp, err = chanlib.DoWithPolicy(rc.http, req, policy)
		if err != nil {
			return nil, err
		}
	}
	// A 429 that outlived the policy's budget is a documented refusal — nothing
	// was created — so it becomes a typed transient error the dedup sweep may
	// retry, rather than a fourth-through-ninth request now (slakd's shape).
	if resp.StatusCode == 429 {
		resp.Body.Close()
		return nil, fmt.Errorf("reddit: rate limited: %w", chanlib.ErrNotDelivered)
	}
	return resp, nil
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
	interval := rc.cfg.PollInterval
	if interval < time.Second {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
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
	rc.markPollOK()

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
	for _, t := range slices.Backward(children) {

		if err := rc.handleThing(t, key, router); err != nil {
			// Stop advancing on the first delivery failure so the next poll
			// re-fetches from the last delivered item instead of skipping it.
			return
		}
		rc.cursors[key] = t.Data.Name
		rc.saveCursors()
	}
}

// thingToMsg converts a reddit thing to an InboundMsg. ok=false if no content.
func (rc *redditClient) thingToMsg(t thing, jid string) (chanlib.InboundMsg, bool) {
	d := t.Data
	content := d.Body
	if content == "" {
		content = d.Title
		if d.Selftext != "" {
			content += "\n\n" + d.Selftext
		}
	}
	if content == "" {
		return chanlib.InboundMsg{}, false
	}
	verb, topic, replyTo := "message", "", ""
	switch t.Kind {
	case "t1":
		if d.ParentID != "" {
			verb = "reply"
			replyTo = d.ParentID
			topic = d.ParentID
			if strings.HasPrefix(d.ParentID, "t3_") {
				topic = d.LinkID
			}
		}
	case "t3":
		verb = "post"
	}
	// The poll and the history verb both reach here without a context.
	atts := rc.extractAttachments(context.Background(), t)
	for _, a := range atts {
		content += fmt.Sprintf(" [Attachment: %s]", a.Filename)
	}
	chatName := ""
	if d.Subreddit != "" {
		chatName = "r/" + d.Subreddit
	}
	return chanlib.InboundMsg{
		ID:          d.Name,
		ChatJID:     jid,
		Sender:      "reddit:user/" + d.Author,
		SenderName:  d.Author,
		Content:     content,
		Timestamp:   int64(d.CreatedAt),
		Topic:       topic,
		Verb:        verb,
		ReplyTo:     replyTo,
		Attachments: atts,
		// t4 is a private inbox message (DM); t1/t3 (comment/submission)
		// always live inside a subreddit and are multi-actor.
		IsGroup:  t.Kind != "t4",
		ChatName: chatName,
	}, true
}

func (rc *redditClient) handleThing(t thing, key string, router *chanlib.RouterClient) error {
	// chat_jid is the conversation context: subreddit for poller-sourced
	// items, the author's user JID for inbox/DM-sourced items.
	jid := "reddit:user/" + t.Data.Author
	if strings.HasPrefix(key, "sr:") {
		jid = "reddit:subreddit/" + t.Data.Subreddit
	}
	msg, ok := rc.thingToMsg(t, jid)
	if !ok {
		return nil
	}
	if err := router.SendMessage(msg); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return err
	}
	rc.lastInboundAt.Store(time.Now().Unix())
	return nil
}

// FetchHistory pulls a subreddit listing (newest-first). JID shape
// `reddit:subreddit/<sr>` (typed) or `reddit:r_<sr>` (legacy) both map to
// `/r/<sr>/new.json`; user DM JIDs are unsupported because
// /message/inbox isn't filterable by counterparty. Reddit listing
// endpoints cap depth at ~1000 items.
func (rc *redditClient) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	jid := req.ChatJID
	var sr string
	switch {
	case strings.HasPrefix(jid, "reddit:subreddit/"):
		sr = strings.TrimPrefix(jid, "reddit:subreddit/")
	case strings.HasPrefix(jid, "reddit:r_"):
		sr = strings.TrimPrefix(jid, "reddit:r_")
	default:
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
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
	for _, t := range slices.Backward(l.Data.Children) {

		ts := int64(t.Data.CreatedAt)
		if !before.IsZero() && ts >= before.Unix() {
			continue
		}
		msg, ok := rc.thingToMsg(t, jid)
		if !ok {
			continue
		}
		msgs = append(msgs, msg)
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
		data = selfPostData(rc.cfg.Username, req.Content)
	}
	resp, err := rc.do("POST", path, nil, data)
	if err != nil {
		return "", err
	}
	if err := checkStatus(resp, "reddit send"); err != nil {
		return "", err
	}
	return "", nil
}

func (rc *redditClient) Typing(string, bool) {}

// checkStatus closes resp.Body and returns an error when the post-retry
// status is outside 2xx. do() already retries 429/401; this guards the
// remaining failures so an outbound verb doesn't report silent success.
func checkStatus(resp *http.Response, op string) error {
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		// A 429 that outlived do()'s retries is still a documented refusal —
		// nothing was created — so the dedup claim is released for the
		// sweep's retry instead of held until dead-letter.
		return fmt.Errorf("%s: status 429: %w", op, chanlib.ErrNotDelivered)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s: status %d: %s", op, resp.StatusCode, string(b))
	}
	return nil
}

func (rc *redditClient) Post(req chanlib.PostRequest) (string, error) {
	if len(req.MediaPaths) > 0 {
		return "", fmt.Errorf("reddit post: media upload not implemented")
	}
	resp, err := rc.do("POST", "/api/submit", nil, selfPostData(rc.cfg.Username, req.Content))
	if err != nil {
		return "", fmt.Errorf("reddit post: %w", err)
	}
	if err := checkStatus(resp, "reddit post"); err != nil {
		return "", err
	}
	return "", nil
}

func selfPostData(username, content string) url.Values {
	return url.Values{
		"kind":  {"self"},
		"sr":    {"u_" + username},
		"title": {"arizuko"},
		"text":  {content},
	}
}

func (rc *redditClient) Like(req chanlib.LikeRequest) error {
	return rc.vote(req.TargetID, 1, "like")
}

func (rc *redditClient) SendFile(_, _, _, _, _, _ string) (string, error) {
	return "", chanlib.Unsupported("send_file", "reddit",
		"Reddit image upload is a 3-step flow (websocket lease → S3 PUT → submit kind:image) and is not wired up. Inline a URL in `post(content=...)` for now.")
}

func (rc *redditClient) Forward(chanlib.ForwardRequest) (string, error) {
	return "", chanlib.Unsupported("forward", "reddit",
		"Reddit has no forward primitive. Use `post` to cross-post the content with attribution.")
}

func (rc *redditClient) Quote(chanlib.QuoteRequest) (string, error) {
	return "", chanlib.Unsupported("quote", "reddit",
		"Reddit has no quote primitive. Use `post(content=\"<commentary>\\n\\n>quoted text\")` with markdown blockquote.")
}

func (rc *redditClient) Repost(chanlib.RepostRequest) (string, error) {
	return "", chanlib.Unsupported("repost", "reddit",
		"Reddit's crosspost API is unstable. Use `post(content=\"...\", url=<original-permalink>)` to share with attribution.")
}

func (rc *redditClient) Dislike(req chanlib.DislikeRequest) error {
	return rc.vote(req.TargetID, -1, "dislike")
}

// vote casts /api/vote dir=±1 on a thing (t1_ comment or t3_ post).
func (rc *redditClient) vote(targetID string, dir int, tool string) error {
	if targetID == "" {
		return fmt.Errorf("reddit %s: empty target_id", tool)
	}
	data := url.Values{
		"id":  {targetID},
		"dir": {fmt.Sprintf("%d", dir)},
	}
	resp, err := rc.do("POST", "/api/vote", nil, data)
	if err != nil {
		return fmt.Errorf("reddit %s: %w", tool, err)
	}
	return checkStatus(resp, "reddit "+tool)
}

// Edit updates a self-post (t3_) or comment (t1_) via /api/editusertext.
// Link submissions return a Reddit API error; surfaced as-is.
func (rc *redditClient) Edit(req chanlib.EditRequest) error {
	if req.TargetID == "" {
		return fmt.Errorf("reddit edit: empty target_id")
	}
	data := url.Values{"thing_id": {req.TargetID}, "text": {req.Content}}
	resp, err := rc.do("POST", "/api/editusertext", nil, data)
	if err != nil {
		return fmt.Errorf("reddit edit: %w", err)
	}
	return checkStatus(resp, "reddit edit")
}

func (rc *redditClient) Delete(req chanlib.DeleteRequest) error {
	data := url.Values{"id": {req.TargetID}}
	resp, err := rc.do("POST", "/api/del", nil, data)
	if err != nil {
		return fmt.Errorf("reddit delete: %w", err)
	}
	return checkStatus(resp, "reddit delete")
}

func (rc *redditClient) extractAttachments(ctx context.Context, t thing) []chanlib.InboundAttachment {
	d := t.Data
	switch {
	case d.Media != nil && d.Media.RedditVideo != nil && d.Media.RedditVideo.FallbackURL != "":
		return []chanlib.InboundAttachment{rc.makeAttachment(ctx, d.Media.RedditVideo.FallbackURL, "video/mp4", "video.mp4")}
	case d.IsGallery && d.GalleryData != nil && d.MediaMetadata != nil:
		var atts []chanlib.InboundAttachment
		for _, item := range d.GalleryData.Items {
			meta, ok := d.MediaMetadata[item.MediaID]
			if !ok || meta.Status != "valid" || meta.S.U == "" {
				continue
			}
			imgURL := strings.ReplaceAll(meta.S.U, "&amp;", "&")
			atts = append(atts, rc.makeAttachment(ctx, imgURL, meta.Mime, item.MediaID+extFromRedditMime(meta.Mime)))
		}
		return atts
	case d.URL != "" && isRedditImageURL(d.URL, d.PostHint):
		return []chanlib.InboundAttachment{rc.makeAttachment(ctx, d.URL, imageMimeFromURL(d.URL), filenameFromURL(d.URL))}
	}
	return nil
}

func (rc *redditClient) makeAttachment(ctx context.Context, rawURL, mime, fname string) chanlib.InboundAttachment {
	id := rc.files.Put(ctx, rawURL)
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

func imageMimeFromURL(u string) string {
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

// DashSections is reditd's per-adapter delta on the shared adapter dashboard
// (spec 6/32). Two things about reditd are invisible on the shared page and
// matter to an operator:
//
//   - WHICH sources it polls. reditd is the one adapter that reads a feed the
//     bot is not addressed in — the configured subreddits — which BUGS/spec
//     6/33 records as the single boundary exception. An operator who cannot see
//     that list cannot audit it.
//   - HOW FAR each source has read. A cursor stuck at the same fullname across
//     restarts is how a silently-broken poll looks; the shared "last inbound"
//     tile cannot show it, because one live source keeps the whole adapter
//     looking fresh.
func (rc *redditClient) DashSections() []chanlib.DashSection {
	rc.mu.Lock()
	keys := make([]string, 0, len(rc.cursors))
	for k := range rc.cursors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][2]string, 0, len(keys)+1)
	for _, k := range keys {
		rows = append(rows, [2]string{k, rc.cursors[k]})
	}
	rc.mu.Unlock()

	// A configured source that has never set a cursor is absent from the map,
	// so list it explicitly as unread rather than letting it vanish — a
	// subreddit that never polled is exactly what an operator is looking for.
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	for _, sr := range rc.cfg.Subreddits {
		if !seen["sr:"+sr] {
			rows = append(rows, [2]string{"sr:" + sr, "(never read)"})
		}
	}
	return []chanlib.DashSection{{Title: "Poll sources and cursors", Rows: rows}}
}
