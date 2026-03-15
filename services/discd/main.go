package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	bot, err := newBot(cfg)
	if err != nil {
		slog.Error("discord auth failed", "err", err)
		os.Exit(1)
	}

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	token, err := rc.register(cfg.Name, cfg.ListenURL,
		[]string{"discord:"}, map[string]bool{
			"send_text": true, "send_file": true, "typing": true,
		})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.token = token
	slog.Info("registered with router", "url", cfg.RouterURL)

	if err := bot.start(ctx, rc); err != nil {
		slog.Error("discord connect failed", "err", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("http server starting", "addr", cfg.ListenAddr)
	srv := &http.Server{Handler: newServer(cfg, bot).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.deregister()
	bot.stop()
	srv.Close()
}

type config struct {
	Name, DiscordToken, RouterURL, ChannelSecret string
	ListenAddr, ListenURL, AssistantName          string
}

func loadConfig() config {
	return config{
		Name:          envOr("CHANNEL_NAME", "discord"),
		DiscordToken:  mustEnv("DISCORD_BOT_TOKEN"),
		RouterURL:     mustEnv("ROUTER_URL"),
		ChannelSecret: envOr("CHANNEL_SECRET", ""),
		ListenAddr:    envOr("LISTEN_ADDR", ":9002"),
		ListenURL:     envOr("LISTEN_URL", "http://discord:9002"),
		AssistantName: envOr("ASSISTANT_NAME", ""),
	}
}

func envOr(k, v string) string {
	if e := os.Getenv(k); e != "" {
		return e
	}
	return v
}

func mustEnv(k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	slog.Error("required env var missing", "key", k)
	os.Exit(1)
	return ""
}
