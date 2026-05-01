package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/onvos/arizuko/compose"
	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group|gate|invite|identity|chat|status|pair|generate> ...")
		fmt.Println("  group    <instance> list | add | rm | grant | ungrant | grants")
		fmt.Println("  gate     <instance> list | add | rm | enable | disable")
		fmt.Println("  invite   <instance> create <target_glob> [--max-uses N] [--expires DURATION]")
		fmt.Println("  invite   <instance> list [--issued-by SUB]")
		fmt.Println("  invite   <instance> revoke <token>")
		fmt.Println("  identity <instance> list | link <sub> [--name NAME] [--id ID] | unlink <sub>")
		fmt.Println("  network  <instance> allow <folder> <target> | deny <folder> <target> | list [<folder>]")
		fmt.Println("  chat     <instance>  — interactive Claude Code session bound to root MCP socket")
		fmt.Println("  send     <instance> <folder> \"<msg>\" [--wait | --stream] [--steer <turn_id>]")
		os.Exit(1)
	}

	cmds := map[string]func([]string){
		"run":      cmdRun,
		"generate": cmdGenerate,
		"create":   cmdCreate,
		"group":    cmdGroup,
		"gate":     cmdGate,
		"invite":   cmdInvite,
		"identity": cmdIdentity,
		"chat":     cmdChat,
		"send":     cmdSend,
		"status":   cmdStatus,
		"pair":     cmdPair,
		"network":  cmdNetwork,
	}
	fn, ok := cmds[os.Args[1]]
	if !ok {
		die("unknown command: %s", os.Args[1])
	}
	fn(os.Args[2:])
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func need(args []string, n int, usage string) {
	if len(args) < n {
		fmt.Println("usage: " + usage)
		os.Exit(1)
	}
}

func cmdRun(args []string) {
	need(args, 1, "arizuko run <instance>")
	outPath := generateCompose(mustInstanceDir(args[0]))
	cmd := exec.Command("docker", "compose", "-f", outPath, "up", "--remove-orphans")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		die("Failed: docker compose: %v", err)
	}
}

func cmdGenerate(args []string) {
	need(args, 1, "arizuko generate <instance>")
	generateCompose(mustInstanceDir(args[0]))
}

func generateCompose(dataDir string) string {
	yml, err := compose.Generate(dataDir)
	if err != nil {
		die("Failed: %v", err)
	}
	outPath := filepath.Join(dataDir, "docker-compose.yml")
	// Atomic write: tempfile in the same dir + rename. Prevents a partial
	// YAML on mid-write crash (which would break `docker compose up`).
	tmp, err := os.CreateTemp(dataDir, ".docker-compose.yml.*")
	if err != nil {
		die("Failed: create tempfile: %v", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(yml); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		die("Failed: write tempfile: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		die("Failed: close tempfile: %v", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		die("Failed: chmod tempfile: %v", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		die("Failed: rename compose: %v", err)
	}
	return outPath
}

func mustInstanceDir(name string) string {
	dir, err := instanceDir(name)
	if err != nil {
		die("Failed: %v", err)
	}
	return dir
}

func instanceDir(name string) (string, error) {
	clean, err := core.SanitizeInstance(name)
	if err != nil {
		return "", err
	}
	if base := os.Getenv("ARIZUKO_DATA_DIR"); base != "" {
		return filepath.Join(base, "arizuko_"+clean), nil
	}
	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = "/srv"
	}
	return filepath.Join(prefix, "data", "arizuko_"+clean), nil
}

func cmdCreate(args []string) {
	need(args, 1, "arizuko create <name>")
	name, err := core.SanitizeInstance(args[0])
	if err != nil {
		die("Failed: %v", err)
	}
	dataDir := mustInstanceDir(name)

	if err := os.MkdirAll(filepath.Join(dataDir, "services"), 0o755); err != nil {
		die("Failed: mkdir services: %v", err)
	}

	envFile := filepath.Join(dataDir, ".env")
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			die("Failed: crypto/rand: %v", err)
		}
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nCONTAINER_IMAGE=arizuko-ant:latest\nAPI_PORT=8080\nCHANNEL_SECRET=%s\n",
			name, hex.EncodeToString(secret))
		// 0600: .env holds CHANNEL_SECRET plus operator-populated OAuth
		// secrets and tokens — not world-readable.
		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			die("Failed: write .env: %v", err)
		}
	}

	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	if err := s.PutGroup(core.Group{Name: name, Folder: "main", AddedAt: time.Now()}); err != nil {
		s.Close()
		die("Failed: add default group: %v", err)
	}

	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		s.Close()
		die("Failed: load config: %v", err)
	}
	if err := container.SetupGroup(cfg, "main", ""); err != nil {
		slog.Warn("failed to setup group dir", "folder", "main", "err", err)
	}
	if err := s.SeedDefaultTasks("main", "main"); err != nil {
		slog.Warn("failed to seed default tasks", "folder", "main", "err", err)
	}
	s.Close()
	fmt.Printf("created instance %s at %s\n", name, dataDir)
}

func cmdGroup(args []string) {
	need(args, 2, "arizuko group <instance> <list|add|rm|grant|ungrant|grants> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "list":
		for _, g := range s.AllGroups() {
			fmt.Printf("%s\t%s\n", g.Folder, g.Name)
		}

	case "add":
		need(args, 4, "arizuko group <instance> add <jid> <name> [folder]")
		jid, name := args[2], args[3]
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
		if err := s.SeedDefaultTasks(folder, folder); err != nil {
			slog.Warn("failed to seed default tasks", "folder", folder, "err", err)
		}

		if err := s.PutGroup(core.Group{Name: name, Folder: folder, AddedAt: time.Now()}); err != nil {
			die("Failed: add group: %v", err)
		}
		if _, err := s.AddRoute(core.Route{Seq: 0, Match: "room=" + core.JidRoom(jid), Target: folder}); err != nil {
			die("Failed: add route: %v", err)
		}
		fmt.Printf("added group %s (%s) -> %s\n", name, jid, folder)

	case "rm":
		need(args, 3, "arizuko group <instance> rm <folder>")
		folder := args[2]
		if err := s.DeleteGroup(folder); err != nil {
			die("Failed: remove group: %v", err)
		}
		fmt.Printf("removed group %s\n", folder)

	case "grant":
		need(args, 4, "arizuko group <instance> grant <sub> <pattern>")
		if err := runGrant(s, args[2], args[3], os.Stdout); err != nil {
			die("Failed: grant: %v", err)
		}

	case "ungrant":
		need(args, 4, "arizuko group <instance> ungrant <sub> <pattern>")
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

var errEmptyGrant = fmt.Errorf("sub and pattern must be non-empty")

func runGrant(s *store.Store, sub, pat string, w io.Writer) error {
	if sub == "" || pat == "" {
		return errEmptyGrant
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
		return errEmptyGrant
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

func cmdGate(args []string) {
	need(args, 2, "arizuko gate <instance> <list|add|rm|enable|disable> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "list":
		gates, err := s.ListGates()
		if err != nil {
			die("Failed: list gates: %v", err)
		}
		if len(gates) == 0 {
			fmt.Println("no gates")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "GATE\tLIMIT/DAY\tENABLED")
		for _, g := range gates {
			en := "yes"
			if !g.Enabled {
				en = "no"
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\n", g.Gate, g.LimitPerDay, en)
		}
		tw.Flush()

	case "add":
		need(args, 4, "arizuko gate <instance> add <spec> <N>/day")
		spec := args[2]
		limitStr := strings.TrimSuffix(args[3], "/day")
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit <= 0 {
			die("Failed: invalid limit %q", args[3])
		}
		if err := s.PutGate(spec, limit); err != nil {
			die("Failed: add gate: %v", err)
		}
		fmt.Printf("gate added: %s %d/day\n", spec, limit)

	case "rm":
		need(args, 3, "arizuko gate <instance> rm <spec>")
		if err := s.DeleteGate(args[2]); err != nil {
			die("Failed: rm gate: %v", err)
		}
		fmt.Printf("gate removed: %s\n", args[2])

	case "enable":
		need(args, 3, "arizuko gate <instance> enable <spec>")
		if err := s.EnableGate(args[2], true); err != nil {
			die("Failed: enable gate: %v", err)
		}
		fmt.Printf("gate enabled: %s\n", args[2])

	case "disable":
		need(args, 3, "arizuko gate <instance> disable <spec>")
		if err := s.EnableGate(args[2], false); err != nil {
			die("Failed: disable gate: %v", err)
		}
		fmt.Printf("gate disabled: %s\n", args[2])

	default:
		die("unknown gate action: %s", action)
	}
}

func cmdInvite(args []string) {
	need(args, 2, "arizuko invite <instance> <create|list|revoke> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "create":
		fs := flag.NewFlagSet("invite create", flag.ExitOnError)
		maxUses := fs.Int("max-uses", 1, "max uses")
		expDur := fs.Duration("expires", 0, "expires after this duration")
		fs.Parse(args[2:])
		if fs.NArg() < 1 {
			die("usage: arizuko invite <instance> create <target_glob> [--max-uses N] [--expires DURATION]")
		}
		if *maxUses < 1 {
			die("invalid --max-uses %d", *maxUses)
		}
		var expiresAt *time.Time
		if *expDur > 0 {
			t := time.Now().Add(*expDur)
			expiresAt = &t
		}
		inv, err := s.CreateInvite(fs.Arg(0), "cli", *maxUses, expiresAt)
		if err != nil {
			die("Failed: %v", err)
		}
		fmt.Printf("token: %s\n", inv.Token)
		fmt.Printf("target_glob: %s\n", inv.TargetGlob)
		fmt.Printf("max_uses: %d\n", inv.MaxUses)
		if inv.ExpiresAt != nil {
			fmt.Printf("expires_at: %s\n", inv.ExpiresAt.Format(time.RFC3339))
		}

	case "list":
		fs := flag.NewFlagSet("invite list", flag.ExitOnError)
		issuedBy := fs.String("issued-by", "", "filter by issuer sub")
		fs.Parse(args[2:])
		invs, err := s.ListInvites(*issuedBy)
		if err != nil {
			die("Failed: %v", err)
		}
		if len(invs) == 0 {
			fmt.Println("no invites")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TOKEN\tTARGET_GLOB\tISSUED_BY\tISSUED_AT\tEXPIRES_AT\tUSED")
		for _, inv := range invs {
			exp := "-"
			if inv.ExpiresAt != nil {
				exp = inv.ExpiresAt.Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d/%d\n",
				inv.Token, inv.TargetGlob, inv.IssuedBySub,
				inv.IssuedAt.Format(time.RFC3339), exp,
				inv.UsedCount, inv.MaxUses)
		}
		tw.Flush()

	case "revoke":
		need(args, 3, "arizuko invite <instance> revoke <token>")
		if err := s.RevokeInvite(args[2]); err != nil {
			die("Failed: %v", err)
		}
		fmt.Printf("invite revoked: %s\n", args[2])

	default:
		die("unknown invite action: %s", action)
	}
}

func cmdIdentity(args []string) {
	need(args, 2, "arizuko identity <instance> <list|link|unlink> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "list":
		if err := runIdentityList(s, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "link":
		fs := flag.NewFlagSet("identity link", flag.ExitOnError)
		name := fs.String("name", "", "identity display name (used when creating)")
		idArg := fs.String("id", "", "existing identity id (skip creation)")
		fs.Parse(args[2:])
		if fs.NArg() < 1 {
			die("usage: arizuko identity <instance> link <sub> [--name NAME] [--id ID]")
		}
		if err := runIdentityLink(s, fs.Arg(0), *idArg, *name, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "unlink":
		need(args, 3, "arizuko identity <instance> unlink <sub>")
		if err := runIdentityUnlink(s, args[2], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown identity action: %s", action)
	}
}

func runIdentityList(s *store.Store, w io.Writer) error {
	idents, err := s.ListIdentities()
	if err != nil {
		return err
	}
	if len(idents) == 0 {
		fmt.Fprintln(w, "no identities")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tCREATED_AT\tSUBS")
	for _, idn := range idents {
		subs, _ := s.SubsForIdentity(idn.ID)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			idn.ID, idn.Name, idn.CreatedAt.Format(time.RFC3339),
			strings.Join(subs, ","))
	}
	return tw.Flush()
}

// runIdentityLink binds sub to an identity. If id is empty, a new identity
// is created with name (defaulting to sub) and sub becomes its first claim.
// If id is provided, sub is added to that existing identity.
func runIdentityLink(s *store.Store, sub, id, name string, w io.Writer) error {
	if sub == "" {
		return fmt.Errorf("sub required")
	}
	if id == "" {
		dn := name
		if dn == "" {
			dn = sub
		}
		idn, err := s.CreateIdentity(dn)
		if err != nil {
			return err
		}
		if err := s.LinkSub(idn.ID, sub); err != nil {
			return err
		}
		fmt.Fprintf(w, "created identity %s (%s) and linked %s\n", idn.ID, idn.Name, sub)
		return nil
	}
	if _, ok := s.GetIdentity(id); !ok {
		return fmt.Errorf("identity %s not found", id)
	}
	if err := s.LinkSub(id, sub); err != nil {
		return err
	}
	fmt.Fprintf(w, "linked %s -> %s\n", sub, id)
	return nil
}

func runIdentityUnlink(s *store.Store, sub string, w io.Writer) error {
	if sub == "" {
		return fmt.Errorf("sub required")
	}
	ok, err := s.UnlinkSub(sub)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(w, "no claim to remove for %s\n", sub)
		return nil
	}
	fmt.Fprintf(w, "unlinked %s\n", sub)
	return nil
}

// cmdChat launches `claude` (Claude Code CLI) wired to the instance's root
// IPC socket via socat. Local-operator only — socket access is the auth.
func cmdChat(args []string) {
	need(args, 1, "arizuko chat <instance>")
	dataDir := mustInstanceDir(args[0])

	sock := filepath.Join(dataDir, "ipc", "main", "gated.sock")
	if _, err := os.Stat(sock); err != nil {
		die("no IPC socket at %s — is the instance running?", sock)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		die("'claude' not in PATH — install Claude Code CLI first")
	}
	if _, err := exec.LookPath("socat"); err != nil {
		die("'socat' not in PATH — install socat first")
	}

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"arizuko": map[string]any{
				"command": "socat",
				"args":    []string{"STDIO", "UNIX-CONNECT:" + sock},
			},
		},
	}
	tmp, err := os.CreateTemp("", "arizuko-chat-*.json")
	if err != nil {
		die("Failed: temp file: %v", err)
	}
	defer os.Remove(tmp.Name())
	if err := json.NewEncoder(tmp).Encode(cfg); err != nil {
		die("Failed: write config: %v", err)
	}
	tmp.Close()

	cmd := exec.Command("claude", "--mcp-config", tmp.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		die("Failed: claude: %v", err)
	}
}

func cmdPair(args []string) {
	need(args, 2, "arizuko pair <instance> <service> [args...]")
	name, service := args[0], args[1]
	composePath := requireCompose(mustInstanceDir(name), name)

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
	need(args, 1, "arizuko status <instance>")
	name := args[0]
	dataDir := mustInstanceDir(name)
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
	// Explicit timeout — default http.Client has none and a hung router
	// would block `arizuko status` indefinitely.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
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
