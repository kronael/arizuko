// egress serves one sandbox's outbound HTTP/HTTPS through a fixed
// allowlist. Point the sandbox at it with HTTP_PROXY/HTTPS_PROXY.
//
//	egress --listen :3128 --allow api.anthropic.com,github.com --client 10.99.4.7
//
// The rules are fixed for the life of the process. There is no admin
// API and no registry: to change the rules, start a new proxy (spec 6/24).
// A rule that can never match — a URL, a host:port, an IP, a stray
// wildcard, a duplicate, a host in both lists — refuses to start rather
// than reading as protection (spec 6/25 1b).
package main

import (
	"context"
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

	"github.com/kronael/arizuko/egress"
)

func main() {
	fs := flag.NewFlagSet("egress", flag.ExitOnError)
	listen := fs.String("listen", envOr("EGRESS_LISTEN", ":3128"), "proxy listen address")
	allow := fs.String("allow", os.Getenv("EGRESS_ALLOW"), "comma-separated allowlist of host names")
	deny := fs.String("deny", os.Getenv("EGRESS_DENY"), "comma-separated denylist; deny beats allow, carving exceptions out of it")
	allowAll := fs.Bool("allow-all", os.Getenv("EGRESS_ALLOW_ALL") != "", "allow every host name (--deny and the address checks still apply)")
	client := fs.String("client", os.Getenv("EGRESS_CLIENT"), "the only peer IP allowed to use this proxy; empty accepts any peer")
	denyPrivate := fs.Bool("deny-private", os.Getenv("EGRESS_DENY_PRIVATE") != "", "also refuse destinations resolving into a private range")
	allowLoopback := fs.Bool("allow-loopback", os.Getenv("EGRESS_ALLOW_LOOPBACK") != "", "permit destinations on this host (local development)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: egress [--listen :3128] [--allow host,host] [--deny host,host] [--allow-all] [--client ip] [--deny-private] [--allow-loopback]\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	allowlist := splitCSV(*allow)
	denylist := splitCSV(*deny)
	if len(allowlist) == 0 && !*allowAll {
		// An empty allowlist denies everything. That is a valid rule but
		// almost always a misconfiguration, so say so rather than serve a
		// proxy that refuses every request without explanation.
		slog.Warn("empty allowlist: every destination will be denied")
	}

	// Bind before reporting healthy. The caller starts the sandbox only once
	// GET /health answers 200, so the sandbox cannot make a request that
	// arrives before this proxy is serving with its allowlist loaded.
	// New validates both lists: a rule that can never match refuses to
	// start, loudly, instead of reading as protection (spec 6/25 1b).
	p, err := egress.New(allowlist, denylist, *client)
	if err != nil {
		slog.Error("invalid rule list", "err", err)
		os.Exit(1)
	}
	p.SetAllowAll(*allowAll)
	p.SetDenyPrivate(*denyPrivate)
	p.SetAllowLoopback(*allowLoopback)
	// Register BEFORE the listener accepts: between "serving" and
	// signal.Notify a SIGTERM takes Go's default disposition and kills the
	// process outright, so the graceful Shutdown below never runs. dashd,
	// proxyd and webd already register first.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen", "addr", *listen, "err", err)
		os.Exit(1)
	}
	srv := p.Server(*listen)
	p.SetReady(true)
	slog.Info("egress up", "listen", lis.Addr().String(), "client", *client,
		"allow", len(allowlist), "deny", len(denylist), "allow_all", *allowAll)

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		slog.Error("serve", "err", err)
		os.Exit(1)
	case <-stop:
	}

	slog.Info("egress shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
