package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// The component-contract probe (spec 6/16).
//
// Primarily an OPERATOR DEPLOYMENT CHECK. Go tests read the source, so they
// check the code; this checks the deployed instance, which no test can — a
// daemon passes every test and still ships with openapi.json behind auth, or a
// gate unwired. Nothing else in arizuko catches that.
//
// Secondarily a conformance check for a component whose source you do not have.
// Real but rare: per 6/7 "capabilities federate, infrastructure conforms", a
// stranger adding a capability uses 5/13 and never touches this contract.
//
// What it CANNOT do, stated so nobody reads a pass as more than it is: it
// detects paths the doc advertises and the mux does not serve, never the
// reverse. Deriving the mounted set by probing is the mistake BUGS F33 records —
// proxyd answers every path from a "/" catch-all, so "it responded" never proves
// "this resource is mounted here".

type probeResult struct {
	name string
	ok   bool
	why  string
}

func cmdProbe(args []string) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	var token, foreign string
	fs.StringVar(&token, "token", "", "bearer")
	fs.StringVar(&token, "t", "", "bearer")
	fs.StringVar(&foreign, "foreign-folder", "", "a folder the bearer must not reach")
	if err := flexParse(fs, args); err != nil || fs.NArg() != 1 {
		usage(err, "arizuko probe <base-url> [--token|-t <bearer>] [--foreign-folder <folder>]\n"+
			"  checks a live component against the spec 6/16 component contract")
	}
	base := strings.TrimSuffix(fs.Arg(0), "/")

	client := &http.Client{Timeout: 10 * time.Second}
	results := runProbe(client, base, token, foreign)

	failed := 0
	for _, r := range results {
		mark := "ok  "
		if !r.ok {
			mark = "FAIL"
			failed++
		}
		fmt.Printf("%s  %-34s %s\n", mark, r.name, r.why)
	}
	fmt.Printf("\n%d/%d checks passed\n", len(results)-failed, len(results))
	if failed > 0 {
		os.Exit(1)
	}
}

// publicPaths are the two endpoints the contract requires to answer WITHOUT
// credentials. Everything else under /v1 answering 200 unauthenticated is a
// missing gate, not a feature.
var publicPaths = map[string]bool{"/openapi.json": true, "/health": true, "/metrics": true}

func runProbe(client *http.Client, base, token, foreign string) []probeResult {
	var out []probeResult

	doc, status, err := fetchOpenAPI(client, base)
	if err != nil {
		return append(out, probeResult{"openapi.json is public", false, err.Error()})
	}
	if status != http.StatusOK {
		return append(out, probeResult{"openapi.json is public", false,
			fmt.Sprintf("got %d — the contract requires it mounted before auth", status)})
	}
	out = append(out, probeResult{"openapi.json is public", true, "200 without credentials"})

	if doc.OpenAPI == "" || !strings.HasPrefix(doc.OpenAPI, "3.") {
		out = append(out, probeResult{"openapi 3.x document", false,
			fmt.Sprintf("openapi field is %q", doc.OpenAPI)})
	} else {
		out = append(out, probeResult{"openapi 3.x document", true, doc.OpenAPI})
	}

	// An empty doc is CONFORMING, not a failure. dashd mounts only /dash/ pages
	// and owns no /v1 resource, so it truthfully advertises nothing — and the
	// first live run of this probe failed it, which is how the check got fixed.
	// A component owes an honest doc, not a non-empty one.
	if len(doc.Paths) == 0 {
		out = append(out, probeResult{"resources advertised", true,
			"none — legal for a UI-only component; nothing further to check"})
		return out
	}
	out = append(out, probeResult{"resources advertised", true,
		fmt.Sprintf("%d path(s)", len(doc.Paths))})

	// Concrete paths only. A templated path needs a real id to probe, and a
	// wrong id's 404 is indistinguishable from an unmounted route.
	var concrete []string
	for p := range doc.Paths {
		if !strings.Contains(p, "{") {
			concrete = append(concrete, p)
		}
	}
	sort.Strings(concrete)

	var missing, ungated []string
	for _, p := range concrete {
		code := probeStatus(client, base+p, "")
		switch {
		case code == http.StatusNotFound:
			missing = append(missing, p)
		case code == http.StatusOK && !publicPaths[p]:
			ungated = append(ungated, p)
		}
	}

	if len(missing) > 0 {
		out = append(out, probeResult{"every advertised path is served", false,
			"404 on " + strings.Join(missing, ", ")})
	} else {
		out = append(out, probeResult{"every advertised path is served", true,
			fmt.Sprintf("%d concrete path(s), none 404", len(concrete))})
	}

	if len(ungated) > 0 {
		out = append(out, probeResult{"non-public paths require auth", false,
			"200 without credentials on " + strings.Join(ungated, ", ")})
	} else {
		out = append(out, probeResult{"non-public paths require auth", true,
			"no unauthenticated 200 outside openapi/health/metrics"})
	}

	if token != "" && foreign != "" {
		out = append(out, probeForeignFolder(client, base, token, foreign, concrete))
	}
	return out
}

// probeForeignFolder is the containment half of the contract: a handler that
// resolves a folder param must bind it to the caller's folder. With a token
// scoped elsewhere, naming `foreign` must not succeed.
func probeForeignFolder(client *http.Client, base, token, foreign string, paths []string) probeResult {
	for _, p := range paths {
		if publicPaths[p] {
			continue
		}
		code := probeStatus(client, base+p+"?folder="+foreign, token)
		if code == http.StatusOK {
			return probeResult{"foreign folder is refused", false,
				fmt.Sprintf("200 for folder=%s on %s", foreign, p)}
		}
	}
	return probeResult{"foreign folder is refused", true,
		fmt.Sprintf("no 200 for folder=%s on %d path(s)", foreign, len(paths))}
}

func probeStatus(client *http.Client, url, token string) int {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // drained so the connection pools; only the status is read
	return resp.StatusCode
}

type openAPIDoc struct {
	OpenAPI string                     `json:"openapi"`
	Paths   map[string]json.RawMessage `json:"paths"`
}

func fetchOpenAPI(client *http.Client, base string) (openAPIDoc, int, error) {
	var doc openAPIDoc
	resp, err := client.Get(base + "/openapi.json")
	if err != nil {
		return doc, 0, fmt.Errorf("unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return doc, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return doc, resp.StatusCode, nil
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return doc, resp.StatusCode, fmt.Errorf("not JSON: %w", err)
	}
	return doc, resp.StatusCode, nil
}
