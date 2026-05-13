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
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/crackbox/pkg/admin"
	"github.com/kronael/arizuko/crackbox/pkg/client"
	"github.com/kronael/arizuko/crackbox/pkg/config"
	"github.com/kronael/arizuko/crackbox/pkg/proxy"
	"github.com/kronael/arizuko/crackbox/pkg/run"
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
  crackbox proxy serve [--config <path>] [--listen :3128] [--admin :3129] [--transparent :3127]
  crackbox run --allow <list> [--id <name>] [--image <img>] -- <cmd>...
  crackbox state [--admin <url>]
`)
}

// resolveAddr applies precedence: flag > env > config-file value.
// flagSet says whether the user passed the flag (so empty-string flag
// can override a non-empty env/config).
func resolveAddr(flagVal string, flagSet bool, envName, fileVal string) string {
	if flagSet {
		return flagVal
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return fileVal
}

func cmdProxy(args []string) {
	if len(args) < 1 || args[0] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: crackbox proxy serve [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("proxy serve", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to TOML config (overrides default search)")
	listen := fs.String("listen", "", "proxy listen addr (overrides config + env)")
	adminAddr := fs.String("admin", "", "admin api listen addr")
	transparent := fs.String("transparent", "", "transparent-mode listen addr (\"\" = disable)")
	fs.Parse(args[1:])

	flagSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flagSet[f.Name] = true })

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if cfg.Source != "" {
		slog.Info("config loaded", "path", cfg.Source)
	}

	proxyAddr := resolveAddr(*listen, flagSet["listen"], "CRACKBOX_PROXY_ADDR", cfg.Proxy.Listen)
	adminListen := resolveAddr(*adminAddr, flagSet["admin"], "CRACKBOX_ADMIN_ADDR", cfg.Proxy.AdminListen)
	transparentAddr := resolveAddr(*transparent, flagSet["transparent"], "CRACKBOX_TRANSPARENT_ADDR", cfg.Proxy.TransparentListen)

	statePath := envOr("CRACKBOX_STATE_PATH", cfg.State.Path)
	var reg *admin.Registry
	if statePath != "" {
		r, err := admin.NewPersistentRegistry(statePath)
		if err != nil {
			slog.Error("registry load", "path", statePath, "err", err)
			os.Exit(1)
		}
		reg = r
		slog.Info("registry persistent", "path", statePath)
	} else {
		reg = admin.NewRegistry()
	}

	secret := envOr("CRACKBOX_ADMIN_SECRET", cfg.Admin.Secret)
	if secret == "" {
		slog.Warn("admin secret unset: admin API mutations are unauthenticated")
	}
	api := admin.NewAPIWithProxy(reg, proxyAddr).WithSecret(secret)
	px := proxy.New(reg)

	proxySrv := px.Server(proxyAddr)
	apiSrv := &http.Server{
		Addr:              adminListen,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	transparentLog := transparentAddr
	if transparentLog == "" {
		transparentLog = "disabled"
	}
	slog.Info("crackbox up", "proxy", proxyAddr, "admin", adminListen, "transparent", transparentLog)

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

	var transparentLis net.Listener
	if transparentAddr != "" {
		l, err := net.Listen("tcp", transparentAddr)
		if err != nil {
			slog.Error("transparent listen", "addr", transparentAddr, "err", err)
			os.Exit(1)
		}
		transparentLis = l
		go func() {
			if err := px.ServeTransparent(l); err != nil {
				slog.Error("transparent serve", "err", err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("crackbox shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxySrv.Shutdown(ctx)
	apiSrv.Shutdown(ctx)
	if transparentLis != nil {
		transparentLis.Close()
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	allow := fs.String("allow", "", "comma-separated allowlist (required)")
	id := fs.String("id", "", "id label for logs (default: random)")
	image := fs.String("image", "alpine:3.20", "container image to run user command in")
	proxyImg := fs.String("proxy-image", envOr("CRACKBOX_IMAGE", "crackbox:latest"), "crackbox proxy image")
	subnet := fs.String("subnet", envOr("CRACKBOX_SUBNET", "10.88.0.0/16"), "Docker network subnet")
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

	cli := client.New(*adminURL, "")
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
