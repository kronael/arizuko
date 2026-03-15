package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type routerClient struct {
	url, secret, token string
	client             *http.Client
}

func newRouterClient(url, secret string) *routerClient {
	return &routerClient{url: url, secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
}

type inboundMsg struct {
	ID         string `json:"id"`
	ChatJID    string `json:"chat_jid"`
	Sender     string `json:"sender"`
	SenderName string `json:"sender_name"`
	Content    string `json:"content"`
	Timestamp  int64  `json:"timestamp"`
	IsGroup    bool   `json:"is_group"`
}

func (r *routerClient) register(name, url string, prefixes []string, caps map[string]bool) (string, error) {
	var resp struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
		Error string `json:"error"`
	}
	err := r.post("/v1/channels/register", map[string]any{
		"name": name, "url": url, "jid_prefixes": prefixes, "capabilities": caps,
	}, r.secret, &resp)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("register: %s", resp.Error)
	}
	return resp.Token, nil
}

func (r *routerClient) deregister() error {
	var resp struct{ OK bool }
	return r.post("/v1/channels/deregister", nil, r.token, &resp)
}

func (r *routerClient) sendMessage(msg inboundMsg) error {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := r.post("/v1/messages", msg, r.token, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("deliver: %s", resp.Error)
	}
	return nil
}

func (r *routerClient) sendChat(jid, name string, isGroup bool) error {
	var resp struct{ OK bool }
	return r.post("/v1/chats", map[string]any{
		"chat_jid": jid, "name": name, "is_group": isGroup,
	}, r.token, &resp)
}

func (r *routerClient) post(path string, body any, auth string, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", r.url+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("router %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("router %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
