package spec

import (
	"os"
	"path/filepath"
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
