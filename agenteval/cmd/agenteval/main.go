// Command agenteval runs the agent-capability eval (spec 5/37) against a live
// arizuko instance and reports pass/fail per capability. See README.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/kronael/arizuko/agenteval/pkg/report"
	"github.com/kronael/arizuko/agenteval/pkg/run"
	"github.com/kronael/arizuko/agenteval/pkg/spec"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "validate":
		cmdValidate(os.Args[2:])
	case "dash":
		cmdDash(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agenteval run <target-base> [flags] | agenteval validate [--cases dir] | agenteval dash <report.json>")
	os.Exit(2)
}

// cmdValidate loads + validates the case catalog without a target — a CI guard
// so a malformed case fails the build, not a live run.
func cmdValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	casesDir := fs.String("cases", "cases", "directory of *.toml case files")
	fs.Parse(args)
	cases, err := spec.Load(*casesDir)
	if err != nil {
		fatal(err)
	}
	smoke := 0
	for _, c := range cases {
		if c.Smoke {
			smoke++
		}
	}
	fmt.Printf("%d cases ok (%d smoke)\n", len(cases), smoke)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	casesDir := fs.String("cases", "cases", "directory of *.toml case files")
	api := fs.String("api", "", "routd REST base serving /v1 (default = <target>)")
	mcp := fs.String("mcp", "", "inspect-compatible MCP-over-HTTP base (enables mcp/parity checks)")
	token := fs.String("token", os.Getenv("AGENTEVAL_TOKEN"), "bearer token for the eval root folder")
	chat := fs.String("chat", "", "eval agent chat JID to inject tasks into (e.g. web:eval)")
	sinkAddr := fs.String("sink-addr", "", "local bind for the callback sink (e.g. :9099; default 127.0.0.1:0)")
	sink := fs.String("sink", "", "externally reachable sink base URL agents call back to (default local bind)")
	nonce := fs.String("nonce", "", "run nonce (default timestamp)")
	smoke := fs.Bool("smoke", false, "run only the smoke basis")
	dim := fs.String("dimension", "", "run only one dimension")
	only := fs.String("case", "", "run only one case id")
	mdOut := fs.String("md", "", "also write the markdown report to this path")
	jsonOut := fs.String("json", "", "also write the JSON report to this path")
	fs.Parse(args)
	if fs.NArg() < 1 {
		usage()
	}
	base := fs.Arg(0)
	if *api == "" {
		*api = base
	}
	if *nonce == "" {
		*nonce = "e" + strconv.FormatInt(time.Now().Unix(), 36)
	}

	cases, err := spec.Load(*casesDir)
	if err != nil {
		fatal(err)
	}
	cases = spec.Filter(cases, *smoke, *dim, *only)
	if len(cases) == 0 {
		fatal(fmt.Errorf("no cases match selectors"))
	}

	tgt := &run.HTTPTarget{API: *api, MCPURL: *mcp, Token: *token}
	results := run.Drive(run.Config{
		Target: tgt, Cases: cases, Nonce: *nonce, TargetBase: base, Chat: *chat,
		SinkBind: *sinkAddr, SinkURL: *sink,
	})

	md := report.Markdown(results)
	fmt.Print(md)
	if *mdOut != "" {
		os.WriteFile(*mdOut, []byte(md), 0o644)
	}
	if *jsonOut != "" {
		if b, err := report.JSON(results); err == nil {
			os.WriteFile(*jsonOut, b, 0o644)
		}
	}
	if !report.AllPassed(results) {
		os.Exit(1)
	}
}

func cmdDash(args []string) {
	fs := flag.NewFlagSet("dash", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		usage()
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal(err)
	}
	var doc struct {
		Results []report.Result `json:"results"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		fatal(err)
	}
	fmt.Print(report.Markdown(doc.Results))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
