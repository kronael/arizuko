package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndFilter(t *testing.T) {
	dir := t.TempDir()
	body := `
[[case]]
id = "a1"
dimension = "self"
smoke = true
prompt = "do {nonce}"
[case.check]
kind = "callback"

[[case]]
id = "w1"
dimension = "web"
prompt = "publish"
[case.check]
kind = "http_status"
url = "{target}/x"
want = 200
`
	if err := os.WriteFile(filepath.Join(dir, "c.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 cases, got %d", len(cases))
	}
	if len(Filter(cases, true, "", "")) != 1 {
		t.Fatal("smoke filter")
	}
	if len(Filter(cases, false, "web", "")) != 1 {
		t.Fatal("dimension filter")
	}
	if len(Filter(cases, false, "", "a1")) != 1 {
		t.Fatal("id filter")
	}
}

func TestValidate(t *testing.T) {
	if Validate(Case{ID: "x", Prompt: "p", Check: Check{Kind: "nope"}}) == nil {
		t.Fatal("want error on unknown kind")
	}
	if Validate(Case{ID: "x", Prompt: "p", Check: Check{Kind: "http_status"}}) == nil {
		t.Fatal("want error on http_status missing url/want")
	}
	if Validate(Case{ID: "x", Prompt: "p", Check: Check{Kind: "callback"}}) != nil {
		t.Fatal("valid case rejected")
	}
}

func TestHarnessField(t *testing.T) {
	dir := t.TempDir()
	body := `
[[case]]
id = "h1"
prompt = "p"
harness = '{"backend":"codex","model":"m"}'
[case.check]
kind = "callback"
`
	if err := os.WriteFile(filepath.Join(dir, "c.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cases[0].Harness != `{"backend":"codex","model":"m"}` || cases[0].HarnessBackend() != "codex" {
		t.Fatalf("case = %+v", cases[0])
	}
	if (Case{}).HarnessBackend() != "" {
		t.Fatal("a case without a harness names a backend")
	}
	for _, harness := range []string{`[]`, `1`, `"x"`, `null`, `{"bad`} {
		err := Validate(Case{ID: "h2", Prompt: "p", Harness: harness, Check: Check{Kind: "callback"}})
		if err == nil || !strings.Contains(err.Error(), "case h2") {
			t.Errorf("harness %s: err = %v, want a refusal naming the case", harness, err)
		}
	}
}

// The runner's expander refuses a brace it cannot fill, and the case then fails
// before it is injected: `curl -w '%{http_code}'` did that to egress-diagnose.
// The load refuses such a text; the placeholders the runner fills pass.
func TestValidateRefusesABraceTheRunnerCannotExpand(t *testing.T) {
	ok := Case{ID: "p1", Prompt: "curl {sink}/cb/{nonce} {target} {cb.url}", Check: Check{Kind: "rest_reply", Chat: "{chat}", Text: "{nonce}-x"}}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"curl -w '%{http_code}' x", "json {\"a\":1}", "open { only", "close } only"} {
		err := Validate(Case{ID: "p2", Prompt: prompt, Check: Check{Kind: "callback"}})
		if err == nil || !strings.Contains(err.Error(), "case p2") {
			t.Errorf("prompt %q: err = %v, want a refusal naming the case", prompt, err)
		}
	}
	if err := Validate(Case{ID: "p3", Prompt: "p", Check: Check{Kind: "rest_reply", Text: "{reply}"}}); err == nil {
		t.Error("accepted an unknown placeholder in a check")
	}
}
