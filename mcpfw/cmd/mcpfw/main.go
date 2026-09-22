// mcpfw serve --upstream <url> --rules <file> [--listen :7070] [--filter-list]
//
// A transparent MCP tool-call filter: it parses JSON-RPC, decides tools/call
// against a flat ruleset, and forwards what it allows. It runs with no arizuko
// process and no data directory (spec 6/12-mcp-firewall).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kronael/arizuko/mcpfw/internal/transport"
	"github.com/kronael/arizuko/mcpfw/pkg/firewall"
	"github.com/kronael/arizuko/mcpfw/pkg/rule"
)

func main() {
	fs := flag.NewFlagSet("mcpfw", flag.ExitOnError)
	upstream := fs.String("upstream", "", "MCP server URL to gate (required)")
	rules := fs.String("rules", "", "ruleset JSON file (required)")
	listen := fs.String("listen", ":7070", "listen address")
	filterList := fs.Bool("filter-list", false, "hide denied tools from tools/list")
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	_ = fs.Parse(args)
	if *upstream == "" || *rules == "" {
		fmt.Fprintln(os.Stderr, "mcpfw: --upstream and --rules are required")
		os.Exit(2)
	}

	f, err := os.Open(*rules)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcpfw: %v\n", err)
		os.Exit(1)
	}
	set, err := rule.Load(f)
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	fw := &firewall.Firewall{
		Rules:      set,
		Upstream:   transport.New(*upstream, nil),
		FilterList: *filterList,
		Log: func(tool string, allowed bool) {
			slog.Info("mcpfw decision", "tool", tool, "allowed", allowed)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		out, err := fw.Handle(json.RawMessage(body))
		if err != nil {
			slog.Error("mcpfw handle", "err", err)
			http.Error(w, "gate error", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	// Register the signal handler BEFORE the listener accepts: a SIGTERM in
	// between takes Go's default disposition and kills the process, so the
	// Shutdown below never runs (BUGS L127).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	srv := &http.Server{Addr: *listen, Handler: mux}
	go func() {
		slog.Info("mcpfw listening", "addr", *listen, "upstream", *upstream, "rules", len(set.Rules))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("mcpfw serve", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	slog.Info("mcpfw stopping")
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
}
