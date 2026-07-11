package main

import (
	"flag"
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
