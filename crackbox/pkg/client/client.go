// Package client is the HTTP admin client for crackbox proxy daemon.
// Used by consumers (e.g. arizuko gated) to register agent IPs against a
// running daemon. Idempotent: Set replaces, Remove is no-op on missing.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	base string
	http *http.Client
}

func New(adminURL string) *Client {
	return &Client{
		base: adminURL,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

type registerReq struct {
	IP        string   `json:"ip"`
	ID        string   `json:"id"`
	Allowlist []string `json:"allowlist"`
}

type unregisterReq struct {
	IP string `json:"ip"`
}

func (c *Client) Register(ip, id string, allowlist []string) error {
	body, _ := json.Marshal(registerReq{IP: ip, ID: id, Allowlist: allowlist})
	return c.post("/v1/register", body)
}

func (c *Client) Unregister(ip string) error {
	body, _ := json.Marshal(unregisterReq{IP: ip})
	return c.post("/v1/unregister", body)
}

type StateEntry struct {
	IP        string   `json:"ip"`
	ID        string   `json:"id"`
	Allowlist []string `json:"allowlist"`
}

func (c *Client) State() ([]StateEntry, error) {
	resp, err := c.http.Get(c.base + "/v1/state")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("state: status %d", resp.StatusCode)
	}
	var out []StateEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) post(path string, body []byte) error {
	resp, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	return nil
}
