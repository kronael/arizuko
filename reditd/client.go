package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type tokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type redditClient struct {
	cfg       config
	http      *http.Client
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	cursors   map[string]string
	skipFirst map[string]bool
}

func newRedditClient(cfg config) (*redditClient, error) {
	rc := &redditClient{
		cfg:       cfg,
		http:      &http.Client{Timeout: 15 * time.Second},
		cursors:   map[string]string{},
		skipFirst: map[string]bool{},
	}
	if err := rc.refreshToken(); err != nil {
		return nil, err
	}
	return rc, nil
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

func (rc *redditClient) get(path string, params map[string]string) (*http.Response, error) {
	if err := rc.ensureToken(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", "https://oauth.reddit.com"+path, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	rc.mu.Lock()
	tok := rc.token
	rc.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", rc.cfg.UserAgent)
	return rc.doWithRetry(req)
}

func (rc *redditClient) post(path string, data url.Values) (*http.Response, error) {
	if err := rc.ensureToken(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "https://oauth.reddit.com"+path,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	} `json:"data"`
}

type listing struct {
	Data struct {
		Before   string  `json:"before"`
		After    string  `json:"after"`
		Children []thing `json:"children"`
	} `json:"data"`
}

func (rc *redditClient) poll(ctx context.Context, router *routerClient) {
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

func (rc *redditClient) pollOnce(router *routerClient) {
	rc.pollSource("inbox", "/message/inbox.json", router)
	for _, sr := range rc.cfg.Subreddits {
		rc.pollSource("sr:"+sr, "/r/"+sr+"/new.json", router)
	}
}

func (rc *redditClient) pollSource(key, path string, router *routerClient) {
	params := map[string]string{"limit": "25"}
	if before := rc.cursors[key]; before != "" {
		params["before"] = before
	}

	resp, err := rc.get(path, params)
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
	}

	if !rc.skipFirst[key] {
		rc.skipFirst[key] = true
		return
	}

	for _, t := range l.Data.Children {
		rc.handleThing(t, key, router)
	}
}

func (rc *redditClient) handleThing(t thing, key string, router *routerClient) {
	d := t.Data
	sender := "reddit:" + d.Author

	isSubreddit := strings.HasPrefix(key, "sr:")
	jid := sender
	if isSubreddit {
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

	chatName := d.Author
	if isSubreddit && d.Subreddit != "" {
		chatName = "r/" + d.Subreddit
	}
	_ = router.SendChat(jid, chatName, isSubreddit)

	// Derive verb from thing kind and context.
	// t1 = comment, t3 = link/post, t4 = message
	verb := "message"
	topic := ""
	switch t.Kind {
	case "t1": // comment
		if d.ParentID != "" && strings.HasPrefix(d.ParentID, "t3_") {
			// comment on a post
			verb = "reply"
			topic = d.LinkID
		} else if d.ParentID != "" {
			// reply to a comment
			verb = "reply"
			topic = d.ParentID
		}
	case "t3": // link/post (subreddit feed)
		verb = "post"
	case "t4": // private message
		verb = "message"
	}

	err := router.SendMessage(inboundMsg{
		ID:         d.Name,
		ChatJID:    jid,
		Sender:     sender,
		SenderName: d.Author,
		Content:    content,
		Timestamp:  int64(d.CreatedAt),
		IsGroup:    isSubreddit,
		Topic:      topic,
		Verb:       verb,
	})
	if err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (rc *redditClient) comment(thingID, text string) error {
	data := url.Values{
		"thing_id": {thingID},
		"text":     {text},
	}
	resp, err := rc.post("/api/comment", data)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (rc *redditClient) submit(text string) error {
	data := url.Values{
		"kind":  {"self"},
		"sr":    {"u_" + rc.cfg.Username},
		"title": {"arizuko"},
		"text":  {text},
	}
	resp, err := rc.post("/api/submit", data)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
