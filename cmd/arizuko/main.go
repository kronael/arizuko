package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/onvos/arizuko/api"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/channels"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/gateway"
	"github.com/onvos/arizuko/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group> ...")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun()
	case "create":
		cmdCreate(os.Args[2:])
	case "group":
		cmdGroup(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func cmdRun() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	s, err := store.Open(cfg.StoreDir)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	gw := gateway.New(cfg, s)

	hooks := core.ChannelHooks{
		OnMessage: func(msg core.Message) {
			if err := s.PutMessage(msg); err != nil {
				slog.Error("store message", "err", err)
			}
		},
		OnChat: func(jid, name string, group bool) {
			ch := "telegram"
			if len(jid) > 0 {
				for _, p := range []string{"telegram:", "discord:", "whatsapp:", "email:", "web:"} {
					if len(jid) > len(p) && jid[:len(p)] == p {
						ch = p[:len(p)-1]
						break
					}
				}
			}
			s.PutChat(jid, name, ch, group)
		},
	}

	if cfg.TelegramToken != "" {
		gw.AddChannel(channels.NewTelegram(
			cfg.TelegramToken, cfg.Name, cfg.TriggerRE, hooks))
	}
	if cfg.DiscordToken != "" {
		gw.AddChannel(channels.NewDiscord(
			cfg.DiscordToken, cfg.Name, cfg.TriggerRE, hooks))
	}

	if cfg.APIPort > 0 {
		reg := chanreg.New(cfg.ChannelSecret)
		apiSrv := api.New(reg, s)
		apiSrv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
			gw.RemoveChannel(name) // remove old if re-registering
			gw.AddChannel(ch)
			ch.DrainOutbox()
		})
		apiSrv.OnDeregister(func(name string) {
			gw.RemoveChannel(name)
		})

		addr := net.JoinHostPort("", strconv.Itoa(cfg.APIPort))
		srv := &http.Server{Addr: addr, Handler: apiSrv.Handler()}
		go func() {
			slog.Info("api server starting", "addr", addr)
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				slog.Error("api server error", "err", err)
			}
		}()

		reg.StartHealthLoop(context.Background())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := gw.Run(ctx); err != nil {
		slog.Error("gateway error", "err", err)
		os.Exit(1)
	}
}

func cmdCreate(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko create <name>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := fmt.Sprintf("/srv/data/arizuko_%s", name)

	for _, sub := range []string{"store", "groups/main/.claude", "groups/main/logs", "data", "web"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed: mkdir %s: %v\n", sub, err)
			os.Exit(1)
		}
	}

	claudeMd := filepath.Join(dataDir, "groups/main/.claude/CLAUDE.md")
	if _, err := os.Stat(claudeMd); os.IsNotExist(err) {
		content := `# Agent Instructions

You operate inside a group chat where participants often talk to each other
and may not be addressing you. Only respond when you are clearly being spoken
to — by name, direct mention, or as the obvious recipient of a question or
request. If the conversation is between users and does not involve you, stay
silent.
`
		if err := os.WriteFile(claudeMd, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Failed: write CLAUDE.md: %v\n", err)
			os.Exit(1)
		}
	}

	envFile := filepath.Join(dataDir, ".env")
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nCONTAINER_IMAGE=arizuko-agent:latest\n", name)
		if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Failed: write .env: %v\n", err)
			os.Exit(1)
		}
	}

	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: open db: %v\n", err)
		os.Exit(1)
	}

	err = s.PutGroup("main", core.Group{
		JID:      "main",
		Name:     name,
		Folder:   "main",
		NeedTrig: false,
		AddedAt:  time.Now(),
	})
	s.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: add default group: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("created instance %s at %s\n", name, dataDir)
}

func cmdGroup(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko group <instance> <list|add|rm> ...")
		os.Exit(1)
	}
	instance := args[0]
	action := args[1]

	dataDir := fmt.Sprintf("/srv/data/arizuko_%s", instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: open db: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	switch action {
	case "list":
		groups := s.AllGroups()
		for _, g := range groups {
			trig := "no-trigger"
			if g.NeedTrig {
				trig = "trigger"
			}
			fmt.Printf("%s\t%s\t%s\t%s\n", g.JID, g.Name, g.Folder, trig)
		}

	case "add":
		if len(args) < 4 {
			fmt.Println("usage: arizuko group <instance> add <jid> <name> [folder]")
			os.Exit(1)
		}
		jid := args[2]
		name := args[3]
		folder := "main"
		if len(args) > 4 {
			folder = args[4]
		}
		needTrig := folder != "main"

		groupDir := filepath.Join(dataDir, "groups", folder, "logs")
		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed: mkdir group dir: %v\n", err)
			os.Exit(1)
		}

		err := s.PutGroup(jid, core.Group{
			JID:      jid,
			Name:     name,
			Folder:   folder,
			NeedTrig: needTrig,
			AddedAt:  time.Now(),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed: add group: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("added group %s (%s) -> %s\n", name, jid, folder)

	case "rm":
		if len(args) < 3 {
			fmt.Println("usage: arizuko group <instance> rm <jid>")
			os.Exit(1)
		}
		jid := args[2]
		if err := s.DeleteGroup(jid); err != nil {
			fmt.Fprintf(os.Stderr, "Failed: remove group: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("removed group %s\n", jid)

	default:
		fmt.Fprintf(os.Stderr, "unknown group action: %s\n", action)
		os.Exit(1)
	}
}
