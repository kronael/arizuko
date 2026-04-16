package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/onvos/arizuko/compose"
	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group|status|pair|generate> ...")
		fmt.Println("  group <instance> list | add | rm | grant | ungrant | grants")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "generate":
		cmdGenerate(os.Args[2:])
	case "create":
		cmdCreate(os.Args[2:])
	case "group":
		cmdGroup(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "pair":
		cmdPair(os.Args[2:])
	default:
		die("unknown command: %s", os.Args[1])
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func cmdRun(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko run <instance>")
		os.Exit(1)
	}
	outPath := generateCompose(instanceDir(args[0]))
	cmd := exec.Command("docker", "compose", "-f", outPath, "up", "--remove-orphans")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		die("Failed: docker compose: %v", err)
	}
}

func cmdGenerate(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko generate <instance>")
		os.Exit(1)
	}
	generateCompose(instanceDir(args[0]))
}

func generateCompose(dataDir string) string {
	yml, err := compose.Generate(dataDir)
	if err != nil {
		die("Failed: %v", err)
	}
	outPath := filepath.Join(dataDir, "docker-compose.yml")
	if err := os.WriteFile(outPath, []byte(yml), 0o644); err != nil {
		die("Failed: write compose: %v", err)
	}
	return outPath
}

func instanceDir(name string) string {
	if base := os.Getenv("ARIZUKO_DATA_DIR"); base != "" {
		return filepath.Join(base, "arizuko_"+name)
	}
	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = "/srv"
	}
	return filepath.Join(prefix, "data", "arizuko_"+name)
}

func cmdCreate(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko create <name>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := instanceDir(name)

	if err := os.MkdirAll(filepath.Join(dataDir, "services"), 0o755); err != nil {
		die("Failed: mkdir services: %v", err)
	}

	envFile := filepath.Join(dataDir, ".env")
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		secret := make([]byte, 16)
		rand.Read(secret)
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nCONTAINER_IMAGE=arizuko-ant:latest\nAPI_PORT=8080\nCHANNEL_SECRET=%s\n",
			name, hex.EncodeToString(secret))
		if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
			die("Failed: write .env: %v", err)
		}
	}

	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	err = s.PutGroup(core.Group{Name: name, Folder: "main", AddedAt: time.Now()})
	s.Close()
	if err != nil {
		die("Failed: add default group: %v", err)
	}

	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	if err := container.SetupGroup(cfg, "main", ""); err != nil {
		slog.Warn("failed to setup group dir", "folder", "main", "err", err)
	}
	// Git repo is init'd lazily by gateway.ensureGroupGitRepo on first
	// agent run — correct ownership because gated runs as uid 1000.
	fmt.Printf("created instance %s at %s\n", name, dataDir)
}

func cmdGroup(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko group <instance> <list|add|rm|grant|ungrant|grants> ...")
		os.Exit(1)
	}
	instance := args[0]
	action := args[1]

	dataDir := instanceDir(instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "list":
		groups := s.AllGroups()
		for _, g := range groups {
			fmt.Printf("%s\t%s\n", g.Folder, g.Name)
		}

	case "add":
		if len(args) < 4 {
			fmt.Println("usage: arizuko group <instance> add <jid> <name> [folder]")
			os.Exit(1)
		}
		jid := args[2]
		name := args[3]
		folder := name
		if len(args) > 4 {
			folder = args[4]
		}

		cfg, err := core.LoadConfigFrom(dataDir)
		if err != nil {
			die("Failed: load config: %v", err)
		}
		if err := container.SetupGroup(cfg, folder, ""); err != nil {
			die("Failed: setup group dir: %v", err)
		}

		if err := s.PutGroup(core.Group{Name: name, Folder: folder, AddedAt: time.Now()}); err != nil {
			die("Failed: add group: %v", err)
		}
		match := "room=" + core.JidRoom(jid)
		s.AddRoute(core.Route{Seq: 0, Match: match, Target: folder})
		fmt.Printf("added group %s (%s) -> %s\n", name, jid, folder)

	case "rm":
		if len(args) < 3 {
			fmt.Println("usage: arizuko group <instance> rm <folder>")
			os.Exit(1)
		}
		folder := args[2]
		if err := s.DeleteGroup(folder); err != nil {
			die("Failed: remove group: %v", err)
		}
		fmt.Printf("removed group %s\n", folder)

	case "grant":
		if len(args) < 4 {
			fmt.Println("usage: arizuko group <instance> grant <sub> <pattern>")
			os.Exit(1)
		}
		if err := runGrant(s, args[2], args[3], os.Stdout); err != nil {
			die("Failed: grant: %v", err)
		}

	case "ungrant":
		if len(args) < 4 {
			fmt.Println("usage: arizuko group <instance> ungrant <sub> <pattern>")
			os.Exit(1)
		}
		if err := runUngrant(s, args[2], args[3], os.Stdout); err != nil {
			die("Failed: ungrant: %v", err)
		}

	case "grants":
		sub := ""
		if len(args) >= 3 {
			sub = args[2]
		}
		if err := runGrants(s, sub, os.Stdout); err != nil {
			die("Failed: grants: %v", err)
		}

	default:
		die("unknown group action: %s", action)
	}
}

func runGrant(s *store.Store, sub, pat string, w io.Writer) error {
	if sub == "" || pat == "" {
		return fmt.Errorf("sub and pattern must be non-empty")
	}
	created, err := s.Grant(sub, pat)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(w, "granted %s -> %s\n", sub, pat)
	} else {
		fmt.Fprintf(w, "already granted: %s -> %s\n", sub, pat)
	}
	return nil
}

func runUngrant(s *store.Store, sub, pat string, w io.Writer) error {
	if sub == "" || pat == "" {
		return fmt.Errorf("sub and pattern must be non-empty")
	}
	n, err := s.Ungrant(sub, pat)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintf(w, "no grant to remove: %s -> %s\n", sub, pat)
	} else {
		fmt.Fprintf(w, "ungranted %s -> %s (%d row)\n", sub, pat, n)
	}
	return nil
}

func runGrants(s *store.Store, sub string, w io.Writer) error {
	grants, err := s.Grants(sub)
	if err != nil {
		return err
	}
	if len(grants) == 0 {
		fmt.Fprintln(w, "no grants")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SUB\tPATTERN\tGRANTED_AT")
	for _, g := range grants {
		ts := g.GrantedAt
		if ts == "" {
			ts = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", g.Sub, g.Pattern, ts)
	}
	return tw.Flush()
}

func cmdPair(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko pair <instance> <service> [args...]")
		os.Exit(1)
	}
	name := args[0]
	service := args[1]
	dataDir := instanceDir(name)
	composePath := requireCompose(dataDir, name)

	cmdArgs := append([]string{"compose", "-f", composePath, "run", "--rm", service}, args[2:]...)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		die("Failed: %v", err)
	}
}

func requireCompose(dataDir, name string) string {
	p := filepath.Join(dataDir, "docker-compose.yml")
	if _, err := os.Stat(p); err != nil {
		die("no compose file at %s — run 'arizuko generate %s' first", p, name)
	}
	return p
}

func cmdStatus(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko status <instance>")
		os.Exit(1)
	}
	name := args[0]
	dataDir := instanceDir(name)
	composePath := requireCompose(dataDir, name)

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
