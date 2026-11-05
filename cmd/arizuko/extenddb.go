package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/objstore/dynstore"
)

const adminUser = "admin"

type manager struct {
	base     string
	client   *http.Client
	password string
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
	client := &http.Client{Timeout: 30 * time.Second}
	if len(ca) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("%s holds no certificate", dynstore.EnvCA)
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}
	}
	return &manager{base: strings.TrimRight(view[dynstore.EnvURL], "/") + "/management", client: client, password: password}, nil
}

func (m *manager) do(method, path string, in, out any, also ...int) error {
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
		if slices.Contains(also, resp.StatusCode) {
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
	if err := m.do("GET", "/accounts", nil, &rows); err != nil {
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
	err = m.do("POST", "/accounts", map[string]string{"account_name": name}, &made)
	return made.ID, err
}

func (m *manager) users(acct string) ([]string, error) {
	var rows []struct {
		Name string `json:"user_name"`
	}
	if err := m.do("GET", "/accounts/"+url.PathEscape(acct)+"/users", nil, &rows); err != nil {
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
	return m.do("PUT", "/accounts/"+url.PathEscape(acct)+"/users/"+url.PathEscape(user)+"/policy/tables", json.RawMessage(doc), nil)
}

func (m *manager) keys(acct, user string) ([]key, error) {
	var rows []key
	err := m.do("GET", "/accounts/"+url.PathEscape(acct)+"/users/"+url.PathEscape(user)+"/access-keys", nil, &rows)
	return rows, err
}

func (m *manager) mint(acct, user string) (string, string, error) {
	var k key
	err := m.do("POST", "/accounts/"+url.PathEscape(acct)+"/users/"+url.PathEscape(user)+"/access-keys", nil, &k)
	return k.ID, k.Secret, err
}
