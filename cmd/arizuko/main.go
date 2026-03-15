package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/onvos/arizuko/api"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/compose"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/gateway"
	"github.com/onvos/arizuko/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group|compose|status> ...")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun()
	case "create":
		cmdCreate(os.Args[2:])
	case "group":
		cmdGroup(os.Args[2:])
	case "compose":
		cmdCompose(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
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

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	var children []*exec.Cmd

	// start timed (scheduler)
	dbPath := filepath.Join(cfg.StoreDir, "messages.db")
	timed := exec.CommandContext(ctx, "timed")
	timed.Env = append(os.Environ(),
		"DATABASE="+dbPath,
		"TIMEZONE="+cfg.Timezone,
	)
	timed.Stdout = os.Stdout
	timed.Stderr = os.Stderr
	if err := timed.Start(); err != nil {
		slog.Error("timed start failed", "err", err)
	} else {
		slog.Info("scheduler started", "pid", timed.Process.Pid)
		children = append(children, timed)
	}

	// start teled (telegram) if token is set
	if cfg.TelegramToken != "" {
		teled := exec.CommandContext(ctx, "teled")
		teled.Env = append(os.Environ(),
			"TELEGRAM_BOT_TOKEN="+cfg.TelegramToken,
			"ROUTER_URL="+fmt.Sprintf("http://localhost:%d", cfg.APIPort),
			"CHANNEL_SECRET="+cfg.ChannelSecret,
			"ASSISTANT_NAME="+cfg.Name,
			"LISTEN_ADDR=:9001",
			"LISTEN_URL=http://localhost:9001",
		)
		teled.Stdout = os.Stdout
		teled.Stderr = os.Stderr
		if err := teled.Start(); err != nil {
			slog.Error("teled start failed", "err", err)
		} else {
			slog.Info("telegram adapter started", "pid", teled.Process.Pid)
			children = append(children, teled)
		}
	}

	// wait for children in background
	for _, c := range children {
		go func(cmd *exec.Cmd) {
			if err := cmd.Wait(); err != nil {
				slog.Warn("child exited", "err", err)
			}
		}(c)
	}

	gw := gateway.New(cfg, s)

	reg := chanreg.New(cfg.ChannelSecret)
	apiSrv := api.New(reg, s)
	apiSrv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
		gw.RemoveChannel(name)
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

	for _, sub := range []string{"store", "groups/main/.claude", "groups/main/logs", "data", "web", "services"} {
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
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nCONTAINER_IMAGE=arizuko-agent:latest\nAPI_PORT=8080\nCHANNEL_SECRET=\n", name)
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

func cmdCompose(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko compose <instance> [--dry-run]")
		os.Exit(1)
	}
	name := args[0]
	dataDir := fmt.Sprintf("/srv/data/arizuko_%s", name)
	dryRun := len(args) > 1 && args[1] == "--dry-run"

	yml, err := compose.Generate(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}

	if dryRun {
		fmt.Print(yml)
		return
	}

	outPath := filepath.Join(dataDir, "docker-compose.yml")
	if err := os.WriteFile(outPath, []byte(yml), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: write compose: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", outPath)

	cmd := exec.Command("docker", "compose", "-f", outPath, "up", "-d", "--remove-orphans")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: docker compose up: %v\n", err)
		os.Exit(1)
	}
}

func cmdStatus(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko status <instance>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := fmt.Sprintf("/srv/data/arizuko_%s", name)
	composePath := filepath.Join(dataDir, "docker-compose.yml")

	if _, err := os.Stat(composePath); err != nil {
		fmt.Fprintf(os.Stderr, "no compose file at %s — run 'arizuko compose %s' first\n", composePath, name)
		os.Exit(1)
	}

	cmd := exec.Command("docker", "compose", "-f", composePath, "ps", "--format",
		"table {{.Name}}\t{{.Status}}\t{{.Ports}}")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil || cfg.APIPort == 0 {
		return
	}
	url := fmt.Sprintf("http://localhost:%d/v1/channels", cfg.APIPort)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("\nrouter API unreachable at %s\n", url)
		return
	}
	defer resp.Body.Close()
	var channels struct {
		Channels []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"channels"`
	}
	if json.NewDecoder(resp.Body).Decode(&channels) == nil && len(channels.Channels) > 0 {
		fmt.Printf("\nregistered channels:\n")
		for _, ch := range channels.Channels {
			fmt.Printf("  %s → %s\n", ch.Name, ch.URL)
		}
	} else {
		fmt.Printf("\nno channels registered\n")
	}
}
