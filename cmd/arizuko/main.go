package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/onvos/arizuko/compose"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group|status> ...")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "create":
		cmdCreate(os.Args[2:])
	case "group":
		cmdGroup(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func cmdRun(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko run <instance>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := instanceDir(name)

	yml, err := compose.Generate(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
	outPath := filepath.Join(dataDir, "docker-compose.yml")
	if err := os.WriteFile(outPath, []byte(yml), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: write compose: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command("docker", "compose", "-f", outPath, "up", "--remove-orphans")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: docker compose: %v\n", err)
		os.Exit(1)
	}
}

func instanceDir(name string) string {
	return fmt.Sprintf("/srv/data/arizuko_%s", name)
}

func cmdCreate(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko create <name>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := instanceDir(name)

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
		secret := make([]byte, 16)
		rand.Read(secret)
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nCONTAINER_IMAGE=arizuko-agent:latest\nAPI_PORT=8080\nCHANNEL_SECRET=%s\n", name, hex.EncodeToString(secret))
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
		JID:     "main",
		Name:    name,
		Folder:  "main",
		AddedAt: time.Now(),
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

	dataDir := instanceDir(instance)
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
			fmt.Printf("%s\t%s\t%s\n", g.JID, g.Name, g.Folder)
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

		groupDir := filepath.Join(dataDir, "groups", folder, "logs")
		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed: mkdir group dir: %v\n", err)
			os.Exit(1)
		}

		err := s.PutGroup(jid, core.Group{
			JID:     jid,
			Name:    name,
			Folder:  folder,
			AddedAt: time.Now(),
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

func cmdStatus(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko status <instance>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := instanceDir(name)
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
