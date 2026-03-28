package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/onvos/arizuko/chanlib"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()
	if cfg.ChannelSecret == "" {
		slog.Warn("CHANNEL_SECRET not set; HTTP endpoints unauthenticated")
	}
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	bot, err := newBot(cfg)
	if err != nil {
		slog.Error("telegram auth failed", "err", err)
		os.Exit(1)
	}

	rc := chanlib.NewRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	token, err := rc.Register(cfg.Name, cfg.ListenURL,
		[]string{"telegram:"}, map[string]bool{
			"send_text": true, "send_file": true, "typing": true,
		})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.Token = token
	slog.Info("registered with router", "url", cfg.RouterURL)

	go bot.poll(ctx, rc)

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
	rc.Deregister()
	bot.stop()
	srv.Close()
}

type config struct {
	Name, TelegramToken, RouterURL, ChannelSecret string
	ListenAddr, ListenURL, AssistantName           string
	StateFile                                      string
}

func loadConfig() config {
	dataDir := chanlib.EnvOr("DATA_DIR", "/srv/app/home")
	name := chanlib.EnvOr("CHANNEL_NAME", "telegram")
	return config{
		Name:          name,
		TelegramToken: chanlib.MustEnv("TELEGRAM_BOT_TOKEN"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9001"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://telegram:9001"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
		StateFile:     dataDir + "/teled-offset-" + name,
	}
}
