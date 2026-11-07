package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kronael/arizuko/anteval/pkg/report"
	"github.com/kronael/arizuko/anteval/pkg/spec"
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
	fmt.Fprintln(os.Stderr, "usage: anteval run --env FILE --sink URL [flags] | anteval validate [--cases dir] | anteval dash <report.json>")
	os.Exit(2)
}

// cmdValidate loads + validates the case catalog without a target — a CI guard
// so a malformed case fails the build, not a live run.
func cmdValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	casesDir := fs.String("cases", "cases", "directory of *.toml case files")
	_ = fs.Parse(args) // ExitOnError: a bad flag exits inside Parse
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
	if err := doRun(args, realCLI{}); err != nil && !errors.Is(err, flag.ErrHelp) {
		fatal(err)
	}
}

func doRun(args []string, cli lifecycleCLI) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	casesDir := fs.String("cases", "cases", "directory of case files")
	envFile := fs.String("env", "", "instance configuration file")
	name := fs.String("target", "", "fresh instance name")
	product := fs.String("product", "", "product template")
	sinkBind := fs.String("sink-addr", "", "callback sink bind address")
	sink := fs.String("sink", "", "guest-reachable callback URL")
	nonce := fs.String("nonce", "", "run nonce")
	smoke := fs.Bool("smoke", false, "run only smoke cases")
	dim := fs.String("dimension", "", "run one dimension")
	only := fs.String("case", "", "run one case")
	md := fs.String("md", "", "markdown report path")
	jsonPath := fs.String("json", "", "JSON report path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *envFile == "" || *sink == "" {
		return fmt.Errorf("--env and --sink are required; --target names a fresh instance")
	}
	cases, err := spec.Load(*casesDir)
	if err != nil {
		return err
	}
	cases = spec.Filter(cases, *smoke, *dim, *only)
	if len(cases) == 0 {
		return fmt.Errorf("no cases match selectors")
	}
	if *nonce == "" {
		*nonce = "e" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return doLifecycle(ctx, cli, lifecycleOptions{
		envFile: *envFile, name: *name, product: *product, nonce: *nonce,
		sinkBind: *sinkBind, sinkURL: *sink, md: *md, json: *jsonPath,
		cases: cases,
	})
}

func cmdDash(args []string) {
	fs := flag.NewFlagSet("dash", flag.ExitOnError)
	_ = fs.Parse(args) // ExitOnError: a bad flag exits inside Parse
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
