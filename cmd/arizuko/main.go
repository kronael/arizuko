package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/onvos/arizuko/core"
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

	groups := s.AllGroups()
	slog.Info("started",
		"name", cfg.Name,
		"groups", len(groups),
		"image", cfg.Image,
	)

	// TODO: gateway.New(cfg, s).Run(ctx)
	fmt.Println("gateway not yet implemented")
}

func cmdCreate(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko create <name>")
		os.Exit(1)
	}
	name := args[0]
	// TODO: seed data dir
	fmt.Printf("create %s: not yet implemented\n", name)
}

func cmdGroup(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko group <list|add|rm> ...")
		os.Exit(1)
	}
	// TODO: group management
	fmt.Printf("group %s: not yet implemented\n", args[0])
}
