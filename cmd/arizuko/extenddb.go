package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/kronael/arizuko/objstore/dynstore"
)

const adminUser = "admin"

type manager struct {
	base     string
	client   doer
	password string
}

type doer interface {
	Do(*http.Request) (*http.Response, error)
}

type key struct {
	ID     string `json:"access_key_id"`
	Active bool   `json:"is_active"`
	Secret string `json:"secret_access_key"`
}

func newManager(view map[string]string) (*manager, error) {
	password := os.Getenv("EXTENDDB_PASSWORD")
	if password == "" {
		return nil, fmt.Errorf("EXTENDDB_PASSWORD is the password of the %s user of the store server", adminUser)
	}
	ca, err := dynstore.DecodeCA(view[dynstore.EnvCA])
	if err != nil {
		return nil, err
	}
	client, err := dynstore.HTTPClient(ca)
	if err != nil {
		return nil, err
	}
	return &manager{base: strings.TrimRight(view[dynstore.EnvURL], "/") + "/management", client: client, password: password}, nil
}

func (m *manager) do(method, path string, in, out any, tolerate int) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, m.base+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(adminUser, m.password)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == tolerate {
			return nil
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m *manager) accounts() (map[string]string, error) {
	var rows []struct {
		ID   string `json:"account_id"`
		Name string `json:"account_name"`
	}
	if err := m.do("GET", "/accounts", nil, &rows, 0); err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, r := range rows {
		byName[r.Name] = r.ID
	}
	return byName, nil
}

func (m *manager) account(name string) (string, error) {
	byName, err := m.accounts()
	if err != nil {
		return "", err
	}
	if id, ok := byName[name]; ok {
		return id, nil
	}
	var made struct {
		ID string `json:"account_id"`
	}
	err = m.do("POST", "/accounts", map[string]string{"account_name": name}, &made, 0)
	return made.ID, err
}

func (m *manager) deleteAccount(name string) error {
	accounts, err := m.accounts()
	if err != nil {
		return err
	}
	id, ok := accounts[name]
	if !ok {
		return nil
	}
	return m.do("DELETE", "/accounts/"+url.PathEscape(id), nil, nil, 0)
}

func (m *manager) users(acct string) ([]string, error) {
	var rows []struct {
		Name string `json:"user_name"`
	}
	if err := m.do("GET", "/accounts/"+url.PathEscape(acct)+"/users", nil, &rows, 0); err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names, nil
}

func (m *manager) user(acct, name string) error {
	return m.do("POST", "/accounts/"+url.PathEscape(acct)+"/users", map[string]string{"user_name": name}, nil, http.StatusConflict)
}

func (m *manager) putPolicy(acct, user, doc string) error {
	return m.do("PUT", "/accounts/"+url.PathEscape(acct)+"/users/"+url.PathEscape(user)+"/policy/tables", json.RawMessage(doc), nil, 0)
}

func (m *manager) keys(acct, user string) ([]key, error) {
	var rows []key
	err := m.do("GET", "/accounts/"+url.PathEscape(acct)+"/users/"+url.PathEscape(user)+"/access-keys", nil, &rows, 0)
	return rows, err
}

func (m *manager) mint(acct, user string) (string, string, error) {
	var k key
	err := m.do("POST", "/accounts/"+url.PathEscape(acct)+"/users/"+url.PathEscape(user)+"/access-keys", nil, &k, 0)
	return k.ID, k.Secret, err
}
