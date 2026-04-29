// crackbox CLI: forward-proxy daemon + per-source allowlists.
// Subcommands are sugar over the same daemon code path. There is no
// separate "single-shot" proxy implementation — `crackbox run` orchestrates
// `crackbox proxy serve` plus a few docker calls.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/onvos/arizuko/crackbox/pkg/admin"
	"github.com/onvos/arizuko/crackbox/pkg/client"
	"github.com/onvos/arizuko/crackbox/pkg/proxy"
	"github.com/onvos/arizuko/crackbox/pkg/run"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "proxy":
		cmdProxy(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "state":
		cmdState(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `crackbox — forward-proxy daemon with per-source allowlist

usage:
  crackbox proxy serve [--listen :3128 --admin :3129]
  crackbox run --allow <list> [--id <name>] [--image <img>] -- <cmd>...
  crackbox state [--admin <url>]
`)
}

func cmdProxy(args []string) {
	if len(args) < 1 || args[0] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: crackbox proxy serve [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("proxy serve", flag.ExitOnError)
	listen := fs.String("listen", envOr("CRACKBOX_PROXY_ADDR", ":3128"), "proxy listen addr")
	adminAddr := fs.String("admin", envOr("CRACKBOX_ADMIN_ADDR", ":3129"), "admin api listen addr")
	fs.Parse(args[1:])

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	reg := admin.NewRegistry()
	api := admin.NewAPI(reg)
	px := proxy.New(reg)

	proxySrv := px.Server(*listen)
	apiSrv := &http.Server{
		Addr:              *adminAddr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("crackbox up", "proxy", *listen, "admin", *adminAddr)

	go func() {
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("proxy serve", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("crackbox shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxySrv.Shutdown(ctx)
	apiSrv.Shutdown(ctx)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	allow := fs.String("allow", "", "comma-separated allowlist (required)")
	id := fs.String("id", "", "id label for logs (default: random)")
	image := fs.String("image", "alpine:3.20", "container image to run user command in")
	proxyImg := fs.String("proxy-image", envOr("CRACKBOX_IMAGE", "crackbox:latest"), "crackbox proxy image")
	subnet := fs.String("subnet", envOr("CRACKBOX_SUBNET", "10.99.0.0/16"), "Docker network subnet")
	quiet := fs.Bool("quiet", false, "suppress crackbox-prefixed status lines")
	fs.Parse(args)

	if *allow == "" {
		fmt.Fprintln(os.Stderr, "--allow is required")
		os.Exit(2)
	}
	cmdAndArgs := fs.Args()
	if len(cmdAndArgs) == 0 {
		fmt.Fprintln(os.Stderr, "command required after flags (use -- to separate)")
		os.Exit(2)
	}

	allowList := splitCSV(*allow)
	code, err := run.Run(run.Args{
		Image:     *image,
		Cmd:       cmdAndArgs,
		Allow:     allowList,
		ID:        *id,
		ProxyImg:  *proxyImg,
		NetSubnet: *subnet,
		Quiet:     *quiet,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "crackbox run:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func cmdState(args []string) {
	fs := flag.NewFlagSet("state", flag.ExitOnError)
	adminURL := fs.String("admin", envOr("CRACKBOX_ADMIN", "http://localhost:3129"), "admin api base url")
	fs.Parse(args)

	cli := client.New(*adminURL)
	state, err := cli.State()
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(state)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
