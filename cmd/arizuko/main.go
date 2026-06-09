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

	"github.com/BurntSushi/toml"
	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

type productManifest struct {
	Skills []string `toml:"skills"`
	Env    []struct {
		Key      string `toml:"key"`
		Required bool   `toml:"required"`
		Hint     string `toml:"hint"`
	} `toml:"env"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group|gate|invite|identity|chat|status|pair|generate|token|apply|plan|get|export> ...")
		fmt.Println("  group    <instance> list | add | rm | grant | ungrant | grants")
		fmt.Println("  gate     <instance> list | add | rm | enable | disable")
		fmt.Println("  invite   <instance> create <target_glob> [--max-uses|-n N] [--expires|-e DURATION]")
		fmt.Println("  invite   <instance> list [--issued-by|-b SUB]")
		fmt.Println("  invite   <instance> revoke <token>")
		fmt.Println("  identity <instance> list | link <sub> [--name|-n NAME] [--id|-i ID] | unlink <sub>")
		fmt.Println("  network  <instance> allow <folder> <target> | deny <folder> <target> | list [<folder>]")
		fmt.Println("  route    <instance> list | add <match> <target> [--seq|-s N] | rm <id>")
		fmt.Println("  token    <instance> issue chat <folder> [<suffix>]")
		fmt.Println("  token    <instance> issue webhook <folder> <label> [<suffix>]")
		fmt.Println("  token    <instance> list <folder>")
		fmt.Println("  token    <instance> revoke <jid>")
		fmt.Println("  chat     <instance>  — interactive Claude Code session bound to root MCP socket")
		fmt.Println("  send     <instance> <folder> \"<msg>\" [--wait|-w | --stream|-S] [--token|-t <raw>] [--topic|-T <t>]")
		fmt.Println("  secret   <instance> set <folder> KEY --value|-v V | list <folder> | delete <folder> KEY")
		fmt.Println("  user-secret <instance> set <user_sub> KEY --value|-v V | list <user_sub> | delete <user_sub> KEY")
		fmt.Println("  budget   <instance> set <folder|user> <name|sub> --daily|-d N | show <folder|user> <name|sub>")
		fmt.Println("  apply    <instance> <manifest.yaml> [--force|-f]")
		fmt.Println("  plan     <instance> <manifest.yaml>  — non-mutating diff vs live config")
		fmt.Println("  get      <instance> <resource>       — emit one resource as a YAML fragment")
		fmt.Println("  export   <instance> [output.yaml]")
		fmt.Println("  migrate-split <instance> [--dry-run]  — populate routd.db + runed.db from messages.db (CUTOVER_SPLIT)")
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
		"network":     cmdNetwork,
		"route":       cmdRoute,
		"secret":      cmdSecret,
		"user-secret": cmdUserSecret,
		"budget":      cmdBudget,
		"token":       cmdToken,
		"apply":       cmdApply,  // spec 5/36
		"plan":        cmdPlan,   // spec 5/36
		"get":         cmdGet,    // spec 5/36
		"export":      cmdExport, // spec 5/36
		"migrate-split": cmdMigrateSplit,
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

// auditCLI logs a mutating CLI operation. Secrets are redacted in-place.
func auditCLI(s *store.Store, cmd string, args []string) {
	redacted := make([]string, len(args))
	copy(redacted, args)
	if (cmd == "secret set" || cmd == "user-secret set") && len(redacted) >= 2 {
		for i, a := range redacted {
			if a == "--value" && i+1 < len(redacted) {
				redacted[i+1] = "[redacted]"
				break
			}
		}
	}
	_ = s.LogCLIAudit(os.Getenv("USER"), cmd, strings.Join(redacted, " "))
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

// parseCreateFlags parses `create` args via flexParse so --product (-p) works in
// any position relative to the <name> positional. It requires EXACTLY one
// positional so a misplaced flag or a stray second arg errors rather than being
// silently dropped.
func parseCreateFlags(args []string) (name, product string, err error) {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.StringVar(&product, "product", "", "product template (creator|personal|pm|reality|slack-team|socials|strategy|support|trip)")
	fs.StringVar(&product, "p", "", "product template (creator|personal|pm|reality|slack-team|socials|strategy|support|trip)")
	if err = flexParse(fs, args); err != nil {
		return "", "", err
	}
	if fs.NArg() != 1 {
		return "", "", fmt.Errorf("expected <name>")
	}
	return fs.Arg(0), product, nil
}

func cmdCreate(args []string) {
	rawName, product, err := parseCreateFlags(args)
	if err != nil {
		fmt.Println("usage: arizuko create <name> [--product|-p <name>]")
		os.Exit(1)
	}
	name, err := core.SanitizeInstance(rawName)
	if err != nil {
		die("Failed: %v", err)
	}
	dataDir := mustInstanceDir(name)

	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}

	var productDir string
	var manifest *productManifest
	if product != "" {
		productDir = filepath.Join(cfg.HostAppDir, "ant", "examples", product)
		manifestPath := filepath.Join(productDir, "PRODUCT.md")
		if _, err := os.Stat(manifestPath); err != nil {
			entries, _ := os.ReadDir(filepath.Join(cfg.HostAppDir, "ant", "examples"))
			var known []string
			for _, e := range entries {
				if e.IsDir() {
					known = append(known, e.Name())
				}
			}
			die("Failed: unknown product %q — known: %s", product, strings.Join(known, ", "))
		}
		var m productManifest
		if _, err := toml.DecodeFile(manifestPath, &m); err != nil {
			die("Failed: parse PRODUCT.md: %v", err)
		}
		manifest = &m
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "services"), 0o755); err != nil {
		die("Failed: mkdir services: %v", err)
	}

	envFile := filepath.Join(dataDir, ".env")
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			die("Failed: crypto/rand: %v", err)
		}
		// SECRETS_KEY is required by gated (secrets encrypted at rest); generate
		// one so a fresh instance starts. Rotate by comma-prepending a new key.
		secretsKey := make([]byte, 32)
		if _, err := rand.Read(secretsKey); err != nil {
			die("Failed: crypto/rand: %v", err)
		}
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nCONTAINER_IMAGE=%s\nAPI_PORT=%d\nCHANNEL_SECRET=%s\nSECRETS_KEY=%s\n",
			name, core.DefaultImage, core.DefaultAPIPort, hex.EncodeToString(secret), hex.EncodeToString(secretsKey))
		// 0600: .env holds CHANNEL_SECRET plus operator-populated OAuth
		// secrets and tokens — not world-readable.
		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			die("Failed: write .env: %v", err)
		}
	}

	storeDir := filepath.Join(dataDir, "store")
	s, err := store.Open(storeDir)
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()
	// Daemons run as uid 1000 (compose `user: '1000:1000'`) but `sudo arizuko
	// create` makes the tree root-owned, so uid 1000 can't open the SQLite DBs
	// under store/ (SQLITE_CANTOPEN). chown store/ — and only store/ — to 1000
	// so every daemon's DB is writable; the data-dir root and .env stay
	// root-owned (matches the working krons layout).
	chownStore(storeDir)
	if err := s.PutGroup(core.Group{Folder: "main", AddedAt: time.Now(), Product: product}); err != nil {
		die("Failed: add default group: %v", err)
	}
	if err := container.SetupGroup(cfg, "main", productDir); err != nil {
		slog.Warn("failed to setup group dir", "folder", "main", "err", err)
	}
	if err := s.SeedDefaultTasks("main", "main"); err != nil {
		slog.Warn("failed to seed default tasks", "folder", "main", "err", err)
	}
	fmt.Printf("created instance %s at %s\n", name, dataDir)
	if manifest != nil {
		fmt.Println()
		if len(manifest.Env) > 0 {
			fmt.Printf("env — set in %s/.env:\n", dataDir)
			for _, e := range manifest.Env {
				req := "optional"
				if e.Required {
					req = "required"
				}
				fmt.Printf("  %-28s (%s) %s\n", e.Key, req, e.Hint)
			}
			fmt.Println()
		}
		if len(manifest.Skills) > 0 {
			fmt.Printf("skills: %s\n", strings.Join(manifest.Skills, ", "))
		}
		fmt.Printf("\nnext: populate %s/groups/main/facts/ then: arizuko run %s\n", dataDir, name)
	}
}

// containerUID/GID is the uid every compose service runs as (`user:
// '1000:1000'`). Hardcoded to match compose; not a config knob.
const containerUID, containerGID = 1000, 1000

// chownStore best-effort chowns store/ (and its contents) to the container uid
// so uid-1000 daemons can open their SQLite DBs in a root-owned tree. Non-root
// callers can't chown to another uid (EPERM); warn and continue rather than
// fail create — on a user-writable /srv/data the operator-owned dir is already
// fine.
func chownStore(storeDir string) {
	err := filepath.Walk(storeDir, func(p string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chown(p, containerUID, containerGID)
	})
	if err != nil {
		slog.Warn("could not chown store dir to container uid; run as root or fix manually",
			"dir", storeDir, "uid", containerUID, "err", err,
			"fix", fmt.Sprintf("sudo chown -R %d:%d %s", containerUID, containerGID, storeDir))
	}
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
			fmt.Println(g.Folder)
		}

	case "add":
		// Parse positionals + optional --product flag from the action-args
		// (args[2:]). Mirror cmdCreate so a child group can be seeded from
		// the same product template (PERSONA.md + CLAUDE.md + facts/).
		fs := flag.NewFlagSet("group add", flag.ContinueOnError)
		productFlag := fs.String("product", "", "product template applied to this group (same set as `arizuko create --product`)")
		if err := flexParse(fs, args[2:]); err != nil || fs.NArg() != 2 {
			die("usage: arizuko group <instance> add <jid> <folder> [--product <name>]")
		}
		jid, folder := fs.Arg(0), fs.Arg(1)
		if !groupfolder.IsValidFolder(folder) {
			die("Failed: invalid folder %q", folder)
		}

		cfg, err := core.LoadConfigFrom(dataDir)
		if err != nil {
			die("Failed: load config: %v", err)
		}
		productDir := ""
		if *productFlag != "" {
			productDir = filepath.Join(cfg.HostAppDir, "ant", "examples", *productFlag)
			if _, err := os.Stat(filepath.Join(productDir, "PRODUCT.md")); err != nil {
				die("Failed: unknown product %q (looked for %s/PRODUCT.md)", *productFlag, productDir)
			}
		}
		if err := container.SetupGroup(cfg, folder, productDir); err != nil {
			die("Failed: setup group dir: %v", err)
		}
		// groups/routes/scheduled_tasks live in routd.db post-split. Use the
		// routd.db-preferring handle + audit-free writers (PutGroupRow/PutRouteRow):
		// routd.db has no audit_log table, so the audited PutGroup/AddRoute would
		// roll back — same discipline as dashd's group-create + `arizuko grant`.
		gs := mustOpenACL(dataDir)
		defer gs.Close()
		if err := gs.SeedDefaultTasks(folder, folder); err != nil {
			slog.Warn("failed to seed default tasks", "folder", folder, "err", err)
		}
		if err := gs.PutGroupRow(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
			die("Failed: add group: %v", err)
		}
		auditCLI(s, "group add", []string{jid, folder})
		// Discord guild channels (not DMs) default to mention-only:
		// a higher-priority verb=mention trigger row + a catch-all
		// observe row. Non-mention messages accumulate as context
		// without firing the agent.
		if strings.HasPrefix(jid, "discord:") && !strings.HasPrefix(jid, "discord:dm/") {
			if _, err := gs.PutRouteRow(core.Route{
				Seq:    -1,
				Match:  "room=" + core.JidRoom(jid) + " verb=mention",
				Target: folder,
			}); err != nil {
				die("Failed: add route: %v", err)
			}
			if _, err := gs.PutRouteRow(core.Route{
				Seq:    0,
				Match:  "room=" + core.JidRoom(jid),
				Target: folder + "#observe",
			}); err != nil {
				die("Failed: add route: %v", err)
			}
		} else if _, err := gs.PutRouteRow(core.Route{
			Seq: 0, Match: "room=" + core.JidRoom(jid), Target: folder,
		}); err != nil {
			die("Failed: add route: %v", err)
		}
		fmt.Printf("added group %s -> %s\n", jid, folder)

	case "rm":
		need(args, 3, "arizuko group <instance> rm <folder>")
		folder := args[2]
		// groups live in routd.db post-split — use the routd.db-preferring handle
		// + audit-free DeleteGroupRow (routd.db has no audit_log), matching `add`.
		gs := mustOpenACL(dataDir)
		defer gs.Close()
		if err := gs.DeleteGroupRow(folder); err != nil {
			die("Failed: remove group: %v", err)
		}
		auditCLI(s, "group rm", []string{folder})
		fmt.Printf("removed group %s\n", folder)

	case "grant":
		need(args, 4, "arizuko group <instance> grant <sub> <pattern>")
		acl := mustOpenACL(dataDir)
		defer acl.Close()
		if err := runGrant(acl, args[2], args[3], os.Stdout); err != nil {
			die("Failed: grant: %v", err)
		}

	case "ungrant":
		need(args, 4, "arizuko group <instance> ungrant <sub> <pattern>")
		acl := mustOpenACL(dataDir)
		defer acl.Close()
		if err := runUngrant(acl, args[2], args[3], os.Stdout); err != nil {
			die("Failed: ungrant: %v", err)
		}

	case "grants":
		sub := ""
		if len(args) >= 3 {
			sub = args[2]
		}
		acl := mustOpenACL(dataDir)
		defer acl.Close()
		if err := runGrants(acl, sub, os.Stdout); err != nil {
			die("Failed: grants: %v", err)
		}

	default:
		die("unknown group action: %s", action)
	}
}

var errEmptyGrant = fmt.Errorf("sub and pattern must be non-empty")

// mustOpenACL opens the *Store holding acl + acl_membership for the grant write
// path. DUAL-PATH (mirrors mustOpenOnbod): in the split routd OWNS them in
// routd.db (spec 5/5), so when that file exists the CLI writes there directly —
// same FS-access discipline as it wrote messages.db before, no token plumbing.
// Monolith (no routd.db) → messages.db via store.Open, so `arizuko grant` works
// on both topologies.
func mustOpenACL(dataDir string) *store.Store {
	storeDir := filepath.Join(dataDir, "store")
	if _, err := os.Stat(filepath.Join(storeDir, "routd.db")); err == nil {
		s, oerr := store.OpenRoutd(storeDir)
		if oerr != nil {
			die("Failed: open routd.db: %v", oerr)
		}
		return s
	}
	s, err := store.Open(storeDir)
	if err != nil {
		die("Failed: open db: %v", err)
	}
	return s
}

// mustOpenOnbod opens the *Store holding onbod's OWNED tables (invites +
// onboarding_gates) for the invite/gate write path. DUAL-PATH: in the split
// onbod OWNS them in onbod.db (spec 5/5), so when that file exists the CLI
// writes there directly — same FS-access discipline as it wrote messages.db
// before, no token plumbing. Monolith (no onbod.db) → messages.db via
// store.Open. Mirrors mustOpenACL but stays dual so the CLI works pre- and
// post-cutover.
func mustOpenOnbod(dataDir string) *store.Store {
	storeDir := filepath.Join(dataDir, "store")
	if _, err := os.Stat(filepath.Join(storeDir, "onbod.db")); err == nil {
		s, oerr := store.OpenOnbod(storeDir)
		if oerr != nil {
			die("Failed: open onbod.db: %v", oerr)
		}
		return s
	}
	s, err := store.Open(storeDir)
	if err != nil {
		die("Failed: open db: %v", err)
	}
	return s
}

// runGrant writes acl rows audit-free (routd.db has no audit_log table — same
// discipline as routd's own grant endpoint).
func runGrant(s *store.Store, sub, pat string, w io.Writer) error {
	if sub == "" || pat == "" {
		return errEmptyGrant
	}
	if pat == "**" {
		// Operator grant: add to role:operator instead of a per-folder row.
		if err := s.PutMembership(sub, "role:operator", "arizuko grant"); err != nil {
			return err
		}
		fmt.Fprintf(w, "granted %s -> role:operator\n", sub)
		return nil
	}
	if err := s.PutACLRow(core.ACLRow{
		Principal: sub, Action: "admin", Scope: pat,
		Effect: "allow", GrantedBy: "arizuko grant",
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "granted %s admin -> %s\n", sub, pat)
	return nil
}

func runUngrant(s *store.Store, sub, pat string, w io.Writer) error {
	if sub == "" || pat == "" {
		return errEmptyGrant
	}
	if pat == "**" {
		if err := s.RemoveMembershipBare(sub, "role:operator"); err != nil {
			return err
		}
		fmt.Fprintf(w, "ungranted %s -> role:operator\n", sub)
		return nil
	}
	if err := s.RemoveACLRowBare(core.ACLRow{
		Principal: sub, Action: "admin", Scope: pat, Effect: "allow",
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "ungranted %s admin -> %s\n", sub, pat)
	return nil
}

func runGrants(s *store.Store, sub string, w io.Writer) error {
	rows := s.ListACL(sub)
	if len(rows) == 0 && len(s.Ancestors(sub)) == 0 {
		fmt.Fprintln(w, "no grants")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRINCIPAL\tACTION\tSCOPE\tEFFECT\tGRANTED_AT")
	for _, r := range rows {
		ts := r.GrantedAt
		if ts == "" {
			ts = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Principal, r.Action, r.Scope, r.Effect, ts)
	}
	// Also show role memberships transitively reachable from sub.
	if anc := s.Ancestors(sub); len(anc) > 0 {
		fmt.Fprintf(tw, "\nMEMBER OF\n")
		for _, p := range anc {
			fmt.Fprintf(tw, "  %s\n", p)
		}
	}
	return tw.Flush()
}

func cmdGate(args []string) {
	need(args, 2, "arizuko gate <instance> <list|add|rm|enable|disable> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s := mustOpenOnbod(dataDir)
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

// parseInviteCreate parses `invite create` args via flexParse so --max-uses (-n)
// and --expires (-e) work in any position relative to the <target_glob>
// positional. It requires EXACTLY one positional and a max-uses >= 1; a misplaced
// flag or missing/extra positional errors rather than being silently dropped.
func parseInviteCreate(args []string) (glob string, maxUses int, expiresAt *time.Time, err error) {
	fs := flag.NewFlagSet("invite create", flag.ContinueOnError)
	fs.IntVar(&maxUses, "max-uses", 1, "max uses")
	fs.IntVar(&maxUses, "n", 1, "max uses")
	var expDur time.Duration
	fs.DurationVar(&expDur, "expires", 0, "expires after this duration")
	fs.DurationVar(&expDur, "e", 0, "expires after this duration")
	if err = flexParse(fs, args); err != nil {
		return "", 0, nil, err
	}
	if fs.NArg() != 1 {
		return "", 0, nil, fmt.Errorf("expected <target_glob>")
	}
	if maxUses < 1 {
		return "", 0, nil, fmt.Errorf("invalid --max-uses %d", maxUses)
	}
	if expDur > 0 {
		t := time.Now().Add(expDur)
		expiresAt = &t
	}
	return fs.Arg(0), maxUses, expiresAt, nil
}

func cmdInvite(args []string) {
	need(args, 2, "arizuko invite <instance> <create|list|revoke> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s := mustOpenOnbod(dataDir)
	defer s.Close()

	switch action {
	case "create":
		glob, maxUses, expiresAt, err := parseInviteCreate(args[2:])
		if err != nil {
			die("usage: arizuko invite <instance> create <target_glob> [--max-uses|-n N] [--expires|-e DURATION]: %v", err)
		}
		inv, err := s.CreateInvite(glob, "cli", maxUses, expiresAt)
		if err != nil {
			die("Failed: %v", err)
		}
		auditCLI(s, "invite create", []string{glob})
		fmt.Printf("token: %s\n", inv.Token)
		fmt.Printf("target_glob: %s\n", inv.TargetGlob)
		fmt.Printf("max_uses: %d\n", inv.MaxUses)
		if inv.ExpiresAt != nil {
			fmt.Printf("expires_at: %s\n", inv.ExpiresAt.Format(time.RFC3339))
		}

	case "list":
		fs := flag.NewFlagSet("invite list", flag.ContinueOnError)
		issuedBy := fs.String("issued-by", "", "filter by issuer sub")
		fs.StringVar(issuedBy, "b", "", "filter by issuer sub")
		if err := flexParse(fs, args[2:]); err != nil || fs.NArg() != 0 {
			die("usage: arizuko invite <instance> list [--issued-by|-b SUB]")
		}
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
		auditCLI(s, "invite revoke", []string{args[2]})
		fmt.Printf("invite revoked: %s\n", args[2])

	default:
		die("unknown invite action: %s", action)
	}
}

// parseIdentityLink parses `identity link` args via flexParse so --name (-n) and
// --id (-i) work in any position relative to the <sub> positional. It requires
// EXACTLY one positional; a misplaced flag or missing/extra positional errors
// rather than being silently dropped.
func parseIdentityLink(args []string) (sub, id, name string, err error) {
	fs := flag.NewFlagSet("identity link", flag.ContinueOnError)
	fs.StringVar(&name, "name", "", "identity display name (used when creating)")
	fs.StringVar(&name, "n", "", "identity display name (used when creating)")
	fs.StringVar(&id, "id", "", "existing identity id (skip creation)")
	fs.StringVar(&id, "i", "", "existing identity id (skip creation)")
	if err = flexParse(fs, args); err != nil {
		return "", "", "", err
	}
	if fs.NArg() != 1 {
		return "", "", "", fmt.Errorf("expected <sub>")
	}
	return fs.Arg(0), id, name, nil
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
		sub, idArg, name, err := parseIdentityLink(args[2:])
		if err != nil {
			die("usage: arizuko identity <instance> link <sub> [--name|-n NAME] [--id|-i ID]: %v", err)
		}
		if err := runIdentityLink(s, sub, idArg, name, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
		auditCLI(s, "identity link", []string{sub})
	case "unlink":
		need(args, 3, "arizuko identity <instance> unlink <sub>")
		if err := runIdentityUnlink(s, args[2], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
		auditCLI(s, "identity unlink", []string{args[2]})
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
