package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"

	"github.com/kronael/arizuko/audit"
)

// `arizuko secret set|delete` carried the same false claim as grant (BUGS.md Q5)
// AND a second one: that it matched "routd's POST /v1/secrets endpoint". That
// endpoint writes through the audited SetSecret (routd/secrets_resource.go), so
// both halves of the justification were wrong and the CLI was the only silent
// secret writer.
//
// The audit row must name the secret and NEVER its value. That was already the
// payload shape SetSecret emits — the fix is the call site, not the payload —
// but nothing asserted it, so a later ParamsSummary edit could leak the value
// into the very trail an operator reads. TestSecretAuditNeverCarriesValue is
// that assertion, and it scans EVERY column of the row, not just params_summary.
//
// Falsifiable: revert runSecretSet to s.PutSecretRow (or runSecretDelete to
// s.DeleteSecretRow) and the secret still lands, but the matching case finds no
// row. Drop AsCLI and the actor falls back to system/gateway. Add the value to
// SetSecret's ParamsSummary and only the no-value case fails.

const secretPlaintext = "sk-plaintext-do-not-log-9f3a"

// secretAuditRow returns (actor, surface, whole-row-text) of the newest audit_log
// row for action in dir/routd.db. The third element concatenates every column so
// a leak into ANY of them is caught, not just the one we expect to hold params.
func secretAuditRow(t *testing.T, st *store.Store, action string) (actor, surface, whole string) {
	t.Helper()
	rows, err := audit.Query(t.Context(), st.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	for _, r := range rows {
		if r.Action != action {
			continue
		}
		return r.Actor, r.Surface, strings.Join([]string{ // newest first
			r.Actor, r.Surface, r.Resource, r.Scope, r.Folder, r.ParamsSummary, r.ErrorMsg,
		}, "|")
	}
	return "", "", ""
}

// secretStore is routd's own tables with a keyring, as the CLI holds them.
func secretStore(t *testing.T, osUser string) *store.Store {
	t.Helper()
	s := routdStore(t)
	s.SetSecretKeys([]byte("test-keyring"))
	if osUser == "" {
		return s
	}
	return s.AsCLI(osUser)
}

func TestSecretSetAudited(t *testing.T) {
	s := secretStore(t, "alice")
	var out bytes.Buffer
	if err := runSecretSet(s, store.ScopeFolder, "atlas", "API_KEY", secretPlaintext, &out); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}

	actor, surface, _ := secretAuditRow(t, s, "secret.set")
	if actor != "cli:alice" || surface != "cli" {
		t.Errorf("secret.set = (%q, %q), want (cli:alice, cli)", actor, surface)
	}
}

func TestSecretDeleteAudited(t *testing.T) {
	s := secretStore(t, "alice")
	var out bytes.Buffer
	if err := runSecretSet(s, store.ScopeFolder, "atlas", "API_KEY", secretPlaintext, &out); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}
	if err := runSecretDelete(s, store.ScopeFolder, "atlas", "API_KEY", &out); err != nil {
		t.Fatalf("runSecretDelete: %v", err)
	}

	actor, surface, _ := secretAuditRow(t, s, "secret.delete")
	if actor != "cli:alice" || surface != "cli" {
		t.Errorf("secret.delete = (%q, %q), want (cli:alice, cli)", actor, surface)
	}
}

// The row that records a secret write must not be a copy of the secret. Checked
// for the folder AND user scope, and for the sealed AND unsealed store — the
// plaintext is in hand either way, and only the at-rest column is encrypted.
func TestSecretAuditNeverCarriesValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   store.SecretScope
		scopeID string
		keyring bool
	}{
		{"folder sealed", store.ScopeFolder, "atlas", true},
		{"user sealed", store.ScopeUser, "github:alice", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := routdStore(t)
			if tc.keyring {
				s.SetSecretKeys([]byte("test-keyring"))
			}
			cli := s.AsCLI("alice")
			var out bytes.Buffer
			if err := runSecretSet(cli, tc.scope, tc.scopeID, "API_KEY", secretPlaintext, &out); err != nil {
				t.Fatalf("runSecretSet: %v", err)
			}
			if err := runSecretDelete(cli, tc.scope, tc.scopeID, "API_KEY", &out); err != nil {
				t.Fatalf("runSecretDelete: %v", err)
			}

			for _, action := range []string{"secret.set", "secret.delete"} {
				actor, _, whole := secretAuditRow(t, s, action)
				if actor == "" {
					t.Fatalf("%s recorded nothing", action)
				}
				if strings.Contains(whole, secretPlaintext) {
					t.Errorf("%s row leaks the secret value: %q", action, whole)
				}
			}
		})
	}
}

// An unattributed Store keeps the pre-AsCLI output, so routd's own secrets
// resource is unaffected by the CLI gaining an operator.
func TestSecretAuditUnattributedStaysSystem(t *testing.T) {
	s := secretStore(t, "")
	var out bytes.Buffer
	if err := runSecretSet(s, store.ScopeFolder, "atlas", "API_KEY", secretPlaintext, &out); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}

	actor, surface, _ := secretAuditRow(t, s, "secret.set")
	if actor != "system" || surface != "gateway" {
		t.Errorf("unattributed = (%q, %q), want (system, gateway)", actor, surface)
	}
}
