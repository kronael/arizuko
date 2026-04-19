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
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

// LinkedIn v2 API reference: https://learn.microsoft.com/en-us/linkedin/marketing/integrations/community-management/shares/share-api

type linkClient struct {
	chanlib.NoFileSender
	chanlib.NoSocial
	cfg       config
	http      *http.Client
	mu        sync.Mutex
	token     string
	refresh   string
	expiresAt time.Time
	meURN     string // urn:li:person:xxx
	meName    string
	seen      map[string]bool // inbound dedup
	stateFile string
	interval  time.Duration
}

type state struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Seen         []string  `json:"seen"`
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func newLinkClient(cfg config) (*linkClient, error) {
	interval, err := time.ParseDuration(cfg.PollInterval)
	if err != nil || interval <= 0 {
		interval = 5 * time.Minute
	}
	lc := &linkClient{
		cfg:       cfg,
		http:      &http.Client{Timeout: 30 * time.Second},
		token:     cfg.AccessToken,
		refresh:   cfg.RefreshToken,
		seen:      map[string]bool{},
		stateFile: filepath.Join(cfg.DataDir, "linkd-state-"+cfg.Name+".json"),
		interval:  interval,
	}
	lc.loadState()
	if lc.token == "" {
		return nil, fmt.Errorf("no LINKEDIN_ACCESS_TOKEN and no persisted state")
	}
	// Fetch /v2/me to confirm auth + cache own URN.
	if err := lc.fetchMe(); err != nil {
		// One retry after refresh if refresh token present.
		if lc.refresh != "" {
			if rerr := lc.refreshAccessToken(); rerr != nil {
				return nil, fmt.Errorf("initial auth: %w; refresh: %v", err, rerr)
			}
			if err := lc.fetchMe(); err != nil {
				return nil, fmt.Errorf("fetch /v2/me after refresh: %w", err)
			}
		} else {
			return nil, fmt.Errorf("fetch /v2/me: %w", err)
		}
	}
	slog.Info("linkedin connected", "urn", lc.meURN, "name", lc.meName)
	return lc, nil
}

func (lc *linkClient) loadState() {
	b, err := os.ReadFile(lc.stateFile)
	if err != nil {
		return
	}
	var s state
	if json.Unmarshal(b, &s) != nil {
		return
	}
	if s.AccessToken != "" {
		lc.token = s.AccessToken
	}
	if s.RefreshToken != "" {
		lc.refresh = s.RefreshToken
	}
	lc.expiresAt = s.ExpiresAt
	for _, id := range s.Seen {
		lc.seen[id] = true
	}
}

func (lc *linkClient) saveState() {
	os.MkdirAll(lc.cfg.DataDir, 0o755)
	lc.mu.Lock()
	seen := make([]string, 0, len(lc.seen))
	for k := range lc.seen {
		seen = append(seen, k)
	}
	// cap seen list so file doesn't grow forever
	if len(seen) > 5000 {
		seen = seen[:5000]
	}
	s := state{
		AccessToken:  lc.token,
		RefreshToken: lc.refresh,
		ExpiresAt:    lc.expiresAt,
		Seen:         seen,
	}
	lc.mu.Unlock()
	b, _ := json.Marshal(s)
	os.WriteFile(lc.stateFile, b, 0o600)
}

func (lc *linkClient) refreshAccessToken() error {
	if lc.refresh == "" {
		return fmt.Errorf("no refresh token configured")
	}
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {lc.refresh},
		"client_id":     {lc.cfg.ClientID},
		"client_secret": {lc.cfg.ClientSecret},
	}
	req, err := http.NewRequest("POST", lc.cfg.OAuthBase+"/oauth/v2/accessToken",
		strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", chanlib.UserAgent)

	resp, err := lc.http.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("token decode: %w", err)
	}
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		return fmt.Errorf("token refresh: status %d: %s", resp.StatusCode, tr.ErrorDesc)
	}
	lc.mu.Lock()
	lc.token = tr.AccessToken
	if tr.RefreshToken != "" {
		lc.refresh = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		lc.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn-60) * time.Second)
	}
	lc.mu.Unlock()
	lc.saveState()
	slog.Info("linkedin token refreshed")
	return nil
}

// meResp: /v2/me — https://learn.microsoft.com/en-us/linkedin/shared/integrations/people/profile-api
type meResp struct {
	ID             string `json:"id"`
	LocalizedFirst string `json:"localizedFirstName"`
	LocalizedLast  string `json:"localizedLastName"`
}

func (lc *linkClient) fetchMe() error {
	resp, err := lc.do("GET", "/v2/me", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("/v2/me: status %d: %s", resp.StatusCode, string(b))
	}
	var m meResp
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return err
	}
	if m.ID == "" {
		return fmt.Errorf("/v2/me: empty id")
	}
	lc.mu.Lock()
	lc.meURN = "urn:li:person:" + m.ID
	lc.meName = strings.TrimSpace(m.LocalizedFirst + " " + m.LocalizedLast)
	lc.mu.Unlock()
	return nil
}

func (lc *linkClient) do(method, path string, params map[string]string, body io.Reader) (*http.Response, error) {
	full := lc.cfg.APIBase + path
	req, err := http.NewRequest(method, full, body)
	if err != nil {
		return nil, err
	}
	if len(params) > 0 {
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	lc.mu.Lock()
	tok := lc.token
	lc.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", chanlib.UserAgent)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := chanlib.DoWithRetry(lc.http, req)
	if err != nil {
		return nil, err
	}
	// 401 → refresh once and retry.
	if resp.StatusCode == 401 && lc.refresh != "" {
		resp.Body.Close()
		if rerr := lc.refreshAccessToken(); rerr != nil {
			return nil, fmt.Errorf("refresh on 401: %w", rerr)
		}
		req2, _ := http.NewRequest(method, full, body)
		if len(params) > 0 {
			q := req2.URL.Query()
			for k, v := range params {
				q.Set(k, v)
			}
			req2.URL.RawQuery = q.Encode()
		}
		lc.mu.Lock()
		req2.Header.Set("Authorization", "Bearer "+lc.token)
		lc.mu.Unlock()
		req2.Header.Set("User-Agent", chanlib.UserAgent)
		req2.Header.Set("X-Restli-Protocol-Version", "2.0.0")
		return chanlib.DoWithRetry(lc.http, req2)
	}
	return resp, nil
}

// Share / post structures. LinkedIn v2 `shares` endpoint.
// https://learn.microsoft.com/en-us/linkedin/marketing/integrations/community-management/shares/share-api
type shareItem struct {
	ID        string `json:"id"`        // urn:li:share:xxx
	Activity  string `json:"activity"`  // urn:li:activity:xxx
	Created   shareTS `json:"created"`
	Owner     string `json:"owner"`
	Text      struct {
		Text string `json:"text"`
	} `json:"text"`
}

type shareTS struct {
	Time int64 `json:"time"`
}

type sharesResp struct {
	Elements []shareItem `json:"elements"`
}

// commentItem: /v2/socialActions/{urn}/comments
// https://learn.microsoft.com/en-us/linkedin/marketing/integrations/community-management/shares/network-update-social-actions
type commentItem struct {
	ID       string  `json:"id"`     // numeric string within the post
	Actor    string  `json:"actor"`  // urn:li:person:xxx
	Created  shareTS `json:"created"`
	Message  struct {
		Text string `json:"text"`
	} `json:"message"`
	ParentComment string `json:"parentComment,omitempty"`
	ObjectType    string `json:"$type,omitempty"`
}

type commentsResp struct {
	Elements []commentItem `json:"elements"`
}

func (lc *linkClient) poll(ctx context.Context, router *chanlib.RouterClient) {
	// initial delay to let Register settle in tests and real runs
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lc.pollOnce(ctx, router)
			t.Reset(lc.interval)
		}
	}
}

func (lc *linkClient) pollOnce(ctx context.Context, router *chanlib.RouterClient) {
	_ = ctx
	shares, err := lc.fetchOwnShares()
	if err != nil {
		slog.Warn("linkedin: fetch shares failed", "err", err)
		return
	}
	for _, sh := range shares {
		urn := sh.Activity
		if urn == "" {
			urn = sh.ID
		}
		if urn == "" {
			continue
		}
		comments, err := lc.fetchComments(urn)
		if err != nil {
			slog.Warn("linkedin: fetch comments failed", "urn", urn, "err", err)
			continue
		}
		for _, c := range comments {
			lc.deliverComment(router, urn, c)
		}
	}
	lc.saveState()
}

func (lc *linkClient) fetchOwnShares() ([]shareItem, error) {
	params := map[string]string{
		"q":      "owners",
		"owners": lc.meURN,
		"count":  "20",
	}
	resp, err := lc.do("GET", "/v2/shares", params, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("/v2/shares: status %d: %s", resp.StatusCode, string(b))
	}
	var sr sharesResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return sr.Elements, nil
}

func (lc *linkClient) fetchComments(urn string) ([]commentItem, error) {
	// URL path encodes the activity URN per Rest.li convention.
	// https://learn.microsoft.com/en-us/linkedin/marketing/integrations/community-management/shares/network-update-social-actions
	path := "/v2/socialActions/" + url.PathEscape(urn) + "/comments"
	resp, err := lc.do("GET", path, map[string]string{"count": "20"}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("comments: status %d: %s", resp.StatusCode, string(b))
	}
	var cr commentsResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	return cr.Elements, nil
}

func (lc *linkClient) deliverComment(router *chanlib.RouterClient, postURN string, c commentItem) {
	// Comment dedup key: post URN + comment id.
	key := postURN + "|" + c.ID
	lc.mu.Lock()
	if lc.seen[key] {
		lc.mu.Unlock()
		return
	}
	lc.seen[key] = true
	meURN := lc.meURN
	lc.mu.Unlock()

	// Skip own comments.
	if c.Actor == meURN {
		return
	}
	// Skip empty text — LinkedIn allows media-only comments we don't handle.
	if strings.TrimSpace(c.Message.Text) == "" {
		return
	}

	jid := "linkedin:" + postURN
	sender := "linkedin:" + c.Actor
	verb := "comment"
	replyTo := ""
	if c.ParentComment != "" {
		verb = "reply"
		replyTo = c.ParentComment
	}
	ts := c.Created.Time / 1000
	if ts == 0 {
		ts = time.Now().Unix()
	}
	msg := chanlib.InboundMsg{
		ID:         postURN + "-" + c.ID,
		ChatJID:    jid,
		Sender:     sender,
		SenderName: c.Actor,
		Content:    c.Message.Text,
		Timestamp:  ts,
		Topic:      postURN,
		Verb:       verb,
		ReplyTo:    replyTo,
	}
	if err := router.SendMessage(msg); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	slog.Debug("inbound", "chat_jid", jid, "message_id", msg.ID, "verb", verb)
}

// Outbound. /v2/ugcPosts for new posts, /v2/socialActions/<urn>/comments for comment.
// https://learn.microsoft.com/en-us/linkedin/marketing/integrations/community-management/shares/ugc-post-api

type ugcPostBody struct {
	Author         string                 `json:"author"`
	LifecycleState string                 `json:"lifecycleState"`
	SpecificContent map[string]any        `json:"specificContent"`
	Visibility     map[string]string      `json:"visibility"`
}

type commentBody struct {
	Actor   string `json:"actor"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	ParentComment string `json:"parentComment,omitempty"`
}

func (lc *linkClient) Send(req chanlib.SendRequest) (string, error) {
	// ChatJID format: linkedin:<urn>. If ChatJID names a post URN and
	// ReplyTo is empty, we treat it as a new top-level comment on that
	// post. With no ChatJID context that looks like a post, fall back to
	// publishing a new ugcPost (only when AutoPublish=true).
	urn := strings.TrimPrefix(req.ChatJID, "linkedin:")

	if isPostURN(urn) {
		return lc.postComment(urn, req.Content, req.ReplyTo)
	}
	if !lc.cfg.AutoPublish {
		return "", fmt.Errorf("LINKEDIN_AUTO_PUBLISH=false; refusing to publish new post")
	}
	return lc.postShare(req.Content)
}

func (lc *linkClient) postShare(text string) (string, error) {
	lc.mu.Lock()
	author := lc.meURN
	lc.mu.Unlock()
	body := ugcPostBody{
		Author:         author,
		LifecycleState: "PUBLISHED",
		SpecificContent: map[string]any{
			"com.linkedin.ugc.ShareContent": map[string]any{
				"shareCommentary":    map[string]string{"text": text},
				"shareMediaCategory": "NONE",
			},
		},
		Visibility: map[string]string{
			"com.linkedin.ugc.MemberNetworkVisibility": "PUBLIC",
		},
	}
	b, _ := json.Marshal(body)
	resp, err := lc.do("POST", "/v2/ugcPosts", nil, strings.NewReader(string(b)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("ugcPosts: status %d: %s", resp.StatusCode, string(raw))
	}
	// LinkedIn returns the new URN in the x-restli-id header or a JSON body.
	if id := resp.Header.Get("X-RestLi-Id"); id != "" {
		return id, nil
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out)
	return out.ID, nil
}

func (lc *linkClient) postComment(postURN, text, parentComment string) (string, error) {
	lc.mu.Lock()
	actor := lc.meURN
	lc.mu.Unlock()
	var body commentBody
	body.Actor = actor
	body.Message.Text = text
	body.ParentComment = parentComment
	b, _ := json.Marshal(body)
	path := "/v2/socialActions/" + url.PathEscape(postURN) + "/comments"
	resp, err := lc.do("POST", path, nil, strings.NewReader(string(b)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("comment: status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out)
	return out.ID, nil
}

// FetchHistory returns comments on an own-post URN. JID shape
// `linkedin:<postURN>`; non-post JIDs (e.g. person URNs) aren't
// addressable via LinkedIn's community-management API and return
// "unsupported" with an empty response.
func (lc *linkClient) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	urn := strings.TrimPrefix(req.ChatJID, "linkedin:")
	if !isPostURN(urn) {
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	path := "/v2/socialActions/" + url.PathEscape(urn) + "/comments"
	params := map[string]string{"count": fmt.Sprintf("%d", limit)}
	resp, err := lc.do("GET", path, params, nil)
	if err != nil {
		return chanlib.HistoryResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return chanlib.HistoryResponse{}, fmt.Errorf("comments: status %d: %s", resp.StatusCode, string(b))
	}
	var cr commentsResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return chanlib.HistoryResponse{}, err
	}
	lc.mu.Lock()
	meURN := lc.meURN
	lc.mu.Unlock()
	before := req.Before
	msgs := make([]chanlib.InboundMsg, 0, len(cr.Elements))
	for _, c := range cr.Elements {
		if c.Actor == meURN {
			continue
		}
		if strings.TrimSpace(c.Message.Text) == "" {
			continue
		}
		ts := c.Created.Time / 1000
		if !before.IsZero() && ts != 0 && ts >= before.Unix() {
			continue
		}
		verb := "comment"
		replyTo := ""
		if c.ParentComment != "" {
			verb = "reply"
			replyTo = c.ParentComment
		}
		msgs = append(msgs, chanlib.InboundMsg{
			ID:         urn + "-" + c.ID,
			ChatJID:    "linkedin:" + urn,
			Sender:     "linkedin:" + c.Actor,
			SenderName: c.Actor,
			Content:    c.Message.Text,
			Timestamp:  ts,
			Topic:      urn,
			Verb:       verb,
			ReplyTo:    replyTo,
		})
	}
	return chanlib.HistoryResponse{Source: "platform", Messages: msgs}, nil
}

func (lc *linkClient) Typing(string, bool) {}

func isPostURN(urn string) bool {
	return strings.Contains(urn, ":activity:") ||
		strings.Contains(urn, ":share:") ||
		strings.Contains(urn, ":ugcPost:")
}
