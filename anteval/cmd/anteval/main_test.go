package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlexParseRunForm covers the documented `run <target> --flags…` shape:
// std flag would stop at <target> and silently drop every flag after it —
// the failure mode of the first live run (all cases ran, token/chat empty).
func TestFlexParseRunForm(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	api := fs.String("api", "", "")
	token := fs.String("token", "", "")
	chat := fs.String("chat", "", "")
	smoke := fs.Bool("smoke", false, "")

	args := []string{
		"https://krons.fiu.wtf",
		"--api", "http://localhost:8081",
		"--token", "tok123",
		"--chat", "web:eval",
		"--smoke",
	}
	if err := flexParse(fs, args); err != nil {
		t.Fatal(err)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "https://krons.fiu.wtf" {
		t.Fatalf("positionals = %v", fs.Args())
	}
	if *api != "http://localhost:8081" || *token != "tok123" || *chat != "web:eval" || !*smoke {
		t.Errorf("flags not parsed: api=%q token=%q chat=%q smoke=%v", *api, *token, *chat, *smoke)
	}
}

func TestFlexParseUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := flexParse(fs, []string{"target", "--nope"}); err == nil {
		t.Fatal("expected error for undefined flag")
	}
}

// captureStdout runs f with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	w.Close()
	os.Stdout = orig
	return <-done
}

// cmdValidate is the CI guard: a malformed case must fail the build rather
// than a live run. Its success path was 0%, so nothing proved the guard reads
// a directory at all.
func TestCmdValidateCountsCasesAndSmoke(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.toml"), []byte(`
[[case]]
id = "a1"
dimension = "test"
smoke = true
prompt = "Ref {nonce}. Reply with the ref and nothing else."
[case.check]
kind = "rest_reply"
text = "{nonce}"

[[case]]
id = "a2"
dimension = "test"
prompt = "Ref {nonce}. Say bye."
[case.check]
kind = "rest_reply"
text = "bye"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { cmdValidate([]string{"--cases", dir}) })
	if !strings.Contains(out, "2 cases ok") {
		t.Errorf("output = %q, want the case count", out)
	}
	if !strings.Contains(out, "(1 smoke)") {
		t.Errorf("output = %q, want the smoke count", out)
	}
}

// Every product ships its own eval suite (spec 5/9-agent-capability-eval: "a product carries its own
// cases"), and `anteval validate` is what keeps them loadable. A suite that
// stops parsing is a product whose behaviour nothing checks any more —
// silently, because the harness simply finds no cases.
func TestEveryShippedProductSuiteValidates(t *testing.T) {
	products, err := os.ReadDir("../../../ant/examples")
	if err != nil {
		t.Fatalf("read the product corpus: %v", err)
	}
	checked := 0
	for _, p := range products {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join("../../../ant/examples", p.Name(), "eval")
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		checked++
		out := captureStdout(t, func() { cmdValidate([]string{"--cases", dir}) })
		if strings.HasPrefix(out, "0 cases ok") {
			t.Errorf("%s ships an eval/ directory that loads zero cases", p.Name())
		}
	}
	if checked < 5 {
		t.Fatalf("only %d product suites found; the path is wrong and this passes vacuously", checked)
	}
}

// cmdDash renders a finished report. Reading a report the harness itself
// wrote and printing nothing would look like a clean run.
func TestCmdDashRendersTheResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"results":[
		{"id":"a1","capability":"reply","pass":true},
		{"id":"a2","capability":"send","pass":false}
	]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { cmdDash([]string{path}) })
	if out == "" {
		t.Fatal("cmdDash printed nothing for a report with two results")
	}
	for _, want := range []string{"a1", "a2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}
