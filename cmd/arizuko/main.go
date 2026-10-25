package main

import (
	"context"
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

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: arizuko <run|create|group|gate|invite|chat|talk|status|pair|generate|packages|token|apply|plan|get|export> ...")
		fmt.Println("  group    <instance> list | add | rm [-f] <folder> | grant | ungrant | grants | thread-replies <folder> <on|off|default>")
		fmt.Println("  gate     <instance> list | add | rm | enable | disable")
		fmt.Println("  invite   <instance> create <target_glob> [--max-uses|-n N] [--expires|-e DURATION]")
		fmt.Println("  invite   <instance> list [--issued-by|-b SUB]")
		fmt.Println("  invite   <instance> revoke <token>")
		fmt.Println("  network  <instance> allow <folder> <target> | deny <folder> <target> | list [<folder>]")
		fmt.Println("  images   <instance> [--prune|-p]  — list rootfs/kernel trees and mark the in-use ones; --prune removes the rest")
		fmt.Println("  route    <instance> list | add <match> <target> [--seq|-s N] | rm <id>")
		fmt.Println("  packages <instance> list | add <name> | remove <name>")
		fmt.Println("  packages <instance> install <git-url-or-dir> | upgrade <name> | sync")
		fmt.Println("  products <instance> list | apply <folder>  — the bundled catalog, or blend the group's ~/arizuko.toml mix")
		fmt.Println("  generate <instance>  — writes docker-compose.yml; also relinks any installed fragment that's still an identical catalog copy")
		fmt.Println("  token    <instance> issue chat <folder> [<suffix>]")
		fmt.Println("  token    <instance> issue webhook <folder> <label> [<suffix>]")
		fmt.Println("  token    <instance> issue bearer <folder> --scope|-s s1,s2 [--ttl|-t 1h] [--sub SUB]")
		fmt.Println("  token    <instance> list <folder>")
		fmt.Println("  token    <instance> revoke <jid>")
		fmt.Println("  chat     <instance>  — interactive Claude Code session bound to root MCP socket")
		fmt.Println("  send     <instance> <folder> \"<msg>\" [--wait|-w | --stream|-S] [--token|-t <raw>] [--topic|-T <t>]")
		fmt.Println("  talk     [@name] [-g <folder>] [-t <thread>] [-p [prompt]]  — REPL against a live instance; no DB, config in ~/.config/arizuko/talk.toml")
		fmt.Println("  secret   <instance> set <folder> KEY --value|-v V | list <folder> | delete <folder> KEY")
		fmt.Println("  user-secret <instance> set <user_sub> KEY --value|-v V | list <user_sub> | delete <user_sub> KEY")
		fmt.Println("  budget   <instance> set <folder|user> <name|sub> --daily|-d N | show <folder|user> <name|sub>")
		fmt.Println("  apply    <instance> <manifest.yaml> [--force|-f]")
		fmt.Println("  plan     <instance> <manifest.yaml>  — non-mutating diff vs live config")
		fmt.Println("  get      <instance> <resource>       — emit one resource as a YAML fragment")
		fmt.Println("  export   <instance> [output.yaml]")
		fmt.Println("  archive  export <instance> [file] [--quiesced]  — full backup: config+secrets+messages+group trees")
		fmt.Println("  archive  apply  <instance> <archive.tar> [--force]")
		fmt.Println("  probe    <base-url> [--token|-t T] [--foreign-folder|-f F]  — check a live component against the 6/16 contract")
		os.Exit(1)
	}

	cmds := map[string]func([]string){
		"run":          cmdRun,
		"generate":     cmdGenerate,
		"proxy-config": cmdProxyConfig,
		"create":       cmdCreate,
		"github":       cmdGithub,
		"gdrive":       cmdGdrive,
		"group":        cmdGroup,
		"gate":         cmdGate,
		"invite":       cmdInvite,
		"chat":         cmdChat,
		"send":         cmdSend,
		"talk":         cmdTalk, // spec 6/26 gap 1
		"status":       cmdStatus,
		"pair":         cmdPair,
		"network":      cmdNetwork,
		"route":        cmdRoute,
		"images":       cmdImages,
		"packages":     cmdPackages, // spec 5/27-compose-native-packaging
		"products":     cmdProducts, // spec 5/28-packages composition
		"secret":       cmdSecret,
		"user-secret":  cmdUserSecret,
		"budget":       cmdBudget,
		"token":        cmdToken,
		"apply":        cmdApply,   // spec 5/8
		"plan":         cmdPlan,    // spec 5/8
		"get":          cmdGet,     // spec 5/8
		"export":       cmdExport,  // spec 5/8
		"archive":      cmdArchive, // spec 5/8
		"probe":        cmdProbe,   // spec 6/16 component contract
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
	dataDir := mustInstanceDir(args[0])
	generateCompose(dataDir)
}

func generateCompose(dataDir string) string {
	// Installed fragments that are still identical copies of the bundled
	// catalog become symlinks here, so a shipped fix (adapter state mounts, a
	// retired env var) reaches this instance on every generate/deploy — no
	// operator step, no flag to remember (BUGS R1).
	relinkCatalog(dataDir)
	yml, routes, err := compose.Generate(dataDir)
	if err != nil {
		die("Failed: %v", err)
	}
	// The release's route table is rows, not env. Written before the compose
	// file so a `docker compose up` never starts proxyd against a table this
	// run failed to reconcile.
	syncProxydRoutes(dataDir, routes)
	// The other half a package installs that no group creation will ever reach
	// again: its skills, into the groups that already exist (BUGS L47).
	syncPackageSkills(dataDir)
	outPath := filepath.Join(dataDir, "docker-compose.yml")
	// Atomic write: tempfile in the same dir + rename. Prevents a partial
	// YAML on mid-write crash (which would break `docker compose up`).
	tmp, err := os.CreateTemp(dataDir, ".docker-compose.yml.*")
	if err != nil {
		die("Failed: create tempfile: %v", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(yml); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		die("Failed: write tempfile: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		die("Failed: close tempfile: %v", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		die("Failed: chmod tempfile: %v", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
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

// instanceDir resolves one instance's data directory AND puts that instance's
// environment into this process, because the store door branches on
// DYNAMODB_URL: a subcommand that resolved the directory without the
// environment opened the file on an instance whose daemons read the server.
func instanceDir(name string) (string, error) {
	clean, err := core.SanitizeInstance(name)
	if err != nil {
		return "", err
	}
	dir := filepath.Join("/srv", "data", "arizuko_"+clean)
	if base := os.Getenv("ARIZUKO_DATA_DIR"); base != "" {
		dir = filepath.Join(base, "arizuko_"+clean)
	} else if prefix := os.Getenv("PREFIX"); prefix != "" {
		dir = filepath.Join(prefix, "data", "arizuko_"+clean)
	}
	core.LoadInstanceEnv(dir)
	return dir, nil
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
	var manifest *container.ProductManifest
	if product != "" {
		productDir = filepath.Join(cfg.HostAppDir, "ant", "examples", product)
		manifestPath := filepath.Join(productDir, container.ManifestFile)
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
		m, err := container.ReadProductManifest(productDir)
		if err != nil {
			die("Failed: parse %s: %v", container.ManifestFile, err)
		}
		manifest = &m
		declareProduct(filepath.Join(cfg.GroupsDir, store.DefaultFolder), productDir,
			filepath.ToSlash(filepath.Join("ant", "examples", product)))
	}

	// services/ holds installed compose fragments. surrogate/ holds the
	// operator's own OAuth provider TOMLs (auth/surrogate reads it, and it is a
	// named dashd mount) — an absent bind source is created by docker as ROOT,
	// unreadable to the uid-1000 daemons, so seed it here instead.
	for _, sub := range []string{"services", "surrogate"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o755); err != nil {
			die("Failed: mkdir %s: %v", sub, err)
		}
	}

	envFile := filepath.Join(dataDir, ".env")
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		// AUTH_SECRET keys the OAuth CSRF state (authd, dashd) and switches proxyd's
		// auth on. It signs no cookie: authd's refresh cookie is an opaque token
		// stored as a sha256 digest, and access tokens are ES256. Generate one so a
		// fresh instance's login flow works out of the box; the ES256 service tokens
		// are provisioned per-daemon by compose generation.
		authSecret := make([]byte, 32)
		if _, err := rand.Read(authSecret); err != nil {
			die("Failed: crypto/rand: %v", err)
		}
		// SECRETS_KEY is required for folder secrets encrypted at rest; generate one
		// so a fresh instance starts. Rotate by comma-prepending a new key.
		secretsKey := make([]byte, 32)
		if _, err := rand.Read(secretsKey); err != nil {
			die("Failed: crypto/rand: %v", err)
		}
		// The product choice is DECLARED here and read back by routd, which writes
		// the default group's row at its first boot. `create` runs before the
		// instance has a store server at all, so the row cannot be written here.
		//
		// There is no egress switch to write any more. The turn runs in a KVM
		// guest whose tap accepts the proxy and drops everything else, and runed
		// refuses to start without the proxy binary (spec 6/41 §"Egress by name,
		// sealed below the kernel"), so isolation is a property of the substrate
		// rather than a variable an operator can leave empty. That closes the
		// half of BUGS `L24` that asked whether the default should be on.
		content := fmt.Sprintf("ASSISTANT_NAME=%s\nAPI_PORT=%d\nAUTH_SECRET=%s\nSECRETS_KEY=%s\nARIZUKO_DEFAULT_PRODUCT=%s\n",
			name, core.DefaultAPIPort, hex.EncodeToString(authSecret), hex.EncodeToString(secretsKey), product)
		// 0600: .env holds AUTH_SECRET plus operator-populated OAuth
		// secrets and tokens — not world-readable.
		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			die("Failed: write .env: %v", err)
		}
	}

	// `create` makes the instance's FILES and nothing else: the directory, the
	// `.env`, the compose fragments and the default group's own tree. Every ROW
	// is its owner daemon's, written at that daemon's first boot — routd seeds
	// the ACL floor, the egress floor and this group there (store/seed.go). A
	// new instance has no store server yet, so there is nothing here to write to.
	//
	// store/ is still this command's: the store server keeps its PostgreSQL data
	// and its certificate under it (template/services/extenddb.yml), and docker
	// creates a missing bind source as ROOT while every service runs as uid 1000
	// — so a directory that is not here before the first `up` comes back
	// unwritable.
	storeDir := filepath.Join(dataDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		die("Failed: make the store dir at %s: %v", storeDir, err)
	}
	chownStore(storeDir)
	if err := container.SetupGroup(cfg, store.DefaultFolder, productDir); err != nil {
		slog.Warn("failed to setup group dir", "folder", store.DefaultFolder, "err", err)
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
	need(args, 2, "arizuko group <instance> <list|add|rm|grant|ungrant|grants|thread-replies> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	if action == "rename" {
		cmdGroupRename(instance, dataDir, args[2:])
		return
	}
	// groups/routes/scheduled_tasks/acl live in routd.db post-split, and so does
	// audit_log (routd migration 0016) — one routd.db handle serves every action
	// here, reads included. `list` used to read the frozen messages.db and print
	// the pre-split group set, disagreeing with the writes beside it (BUGS.md Q1).
	// AsCLI attributes grant/ungrant to the invoking operator: the CLI has no
	// X-User-* header to learn a sub from.
	s := mustOpenACL(dataDir).AsCLI(os.Getenv("USER"))
	defer func() { _ = s.Close() }()

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
			if _, err := os.Stat(filepath.Join(productDir, container.ManifestFile)); err != nil {
				die("Failed: unknown product %q (looked for %s/%s)", *productFlag, productDir, container.ManifestFile)
			}
			declareProduct(filepath.Join(cfg.GroupsDir, folder), productDir,
				filepath.ToSlash(filepath.Join("ant", "examples", *productFlag)))
		}
		if err := container.SetupGroup(cfg, folder, productDir); err != nil {
			die("Failed: setup group dir: %v", err)
		}
		if err := s.SeedDefaultTasks(folder, folder); err != nil {
			slog.Warn("failed to seed default tasks", "folder", folder, "err", err)
		}
		if err := s.PutGroupRow(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
			die("Failed: add group: %v", err)
		}
		auditCLI(s, "group add", []string{jid, folder})
		// Discord guild channels (not DMs) default to mention-only:
		// a higher-priority verb=mention trigger row + a catch-all
		// observe row. Non-mention messages accumulate as context
		// without firing the agent.
		if strings.HasPrefix(jid, "discord:") && !strings.HasPrefix(jid, "discord:dm/") {
			if _, err := s.PutRouteRow(core.Route{
				Seq:    -1,
				Match:  "room=" + core.JidRoom(jid) + " verb=mention",
				Target: folder,
			}); err != nil {
				die("Failed: add route: %v", err)
			}
			if _, err := s.PutRouteRow(core.Route{
				Seq:    0,
				Match:  "room=" + core.JidRoom(jid),
				Target: folder + "#observe",
			}); err != nil {
				die("Failed: add route: %v", err)
			}
		} else if _, err := s.PutRouteRow(core.Route{
			Seq: 0, Match: "room=" + core.JidRoom(jid), Target: folder,
		}); err != nil {
			die("Failed: add route: %v", err)
		}
		fmt.Printf("added group %s -> %s\n", jid, folder)

	case "rm":
		folder, force, err := parseGroupRm(args[2:])
		if err != nil {
			die("usage: %s", groupRmUsage)
		}
		cfg, err := core.LoadConfigFrom(dataDir)
		if err != nil {
			die("Failed: load config: %v", err)
		}
		var client *runedv1.Client
		if !force {
			if client, err = runedClient(dataDir); err != nil {
				die("Failed: %v", err)
			}
		}
		if err := runGroupRm(context.Background(), s, cfg, client, folder, force, os.Stdout); err != nil {
			die("Failed: %v", err)
		}

	case "grant", "ungrant":
		row, err := parseGrantArgs(action, args[2:])
		if err != nil {
			die("Failed: %s: %v", action, err)
		}
		run := runGrant
		if action == "ungrant" {
			run = runUngrant
		}
		if err := run(s, row, os.Stdout); err != nil {
			die("Failed: %s: %v", action, err)
		}

	case "grants":
		sub := ""
		if len(args) >= 3 {
			sub = args[2]
		}
		if err := runGrants(s, sub, os.Stdout); err != nil {
			die("Failed: grants: %v", err)
		}

	case "thread-replies":
		need(args, 4, "arizuko group <instance> thread-replies <folder> <on|off|default>")
		folder, val := args[2], args[3]
		var on *bool
		switch val {
		case "on":
			v := true
			on = &v
		case "off":
			v := false
			on = &v
		case "default":
			on = nil
		default:
			die("Failed: expected on|off|default, got %q", val)
		}
		if err := s.SetThreadReplies(folder, on); err != nil {
			die("Failed: set thread-replies: %v", err)
		}
		label := val
		if on == nil {
			label = "default (group chats thread, DMs do not)"
		}
		fmt.Printf("thread-replies for %s set to %s\n", folder, label)

	default:
		die("unknown group action: %s", action)
	}
}

var errEmptyGrant = fmt.Errorf("sub and pattern must be non-empty")

// mustOpenACL opens the *Store holding acl + acl_membership for the grant write
// path. routd OWNS them (spec 5/16-mcp-rest-unification); the CLI reaches them through store's own
// door, which answers the endpoint a served instance keeps them on and
// otherwise the file under the data dir it mounts — same access discipline, no
// token plumbing.
func mustOpenACL(dataDir string) *store.Store {
	s, err := mustOpenOwner(store.OwnerRoutd, dataDir)
	if err != nil {
		die("Failed: open routd's store: %v", err)
	}
	return s
}

// mustOpenOnbod opens the *Store holding onbod's OWNED tables (invites +
// onboarding_gates) for the invite/gate write path. onbod OWNS them (spec 5/16-mcp-rest-unification)
// and the CLI writes them the same way.
func mustOpenOnbod(dataDir string) *store.Store {
	s, err := mustOpenOwner(store.OwnerOnbod, dataDir)
	if err != nil {
		die("Failed: open onbod's store: %v", err)
	}
	return s
}

// mustOpenOwner is the one place this binary names an owner's target: the file
// under <dataDir>/store, which store.OpenOwner ignores on an instance served
// from an endpoint.
func mustOpenOwner(owner, dataDir string) (*store.Store, error) {
	return store.OpenOwner(owner)
}

// grantUsage is the one usage string both verbs print, because they take the
// same row and a divergence would document a flag only one of them has.
const grantUsage = "arizuko group <instance> <grant|ungrant> <sub> <pattern> [action] " +
	"[--effect allow|deny] [--params <matcher>]"

// parseGrantArgs reads the operator's words into the acl row the verb addresses.
//
// IT IS THE ROW, not five loose strings: a grant and its revocation name ONE
// item — store.aclKey joins principal, action, scope, params, predicate and
// effect — so a revoke that carries fewer fields than the grant addresses a
// different item and silently deletes nothing. That is L138: `ungrant` pinned
// action=admin, effect=allow and params="", which is exactly one of the six
// shapes an operator can now write.
//
// `predicate` has no flag. routd's face leaves it out for the same reason
// (acl_resource.go aclArgsRow): a claim condition is a different vocabulary
// from a rule over call arguments, and a revoke pins it empty to match.
func parseGrantArgs(verb string, args []string) (core.ACLRow, error) {
	fs := flag.NewFlagSet("group "+verb, flag.ContinueOnError)
	effect := fs.String("effect", "", "allow or deny; default allow")
	params := fs.String("params", "", "argument matcher the row is narrowed to, as add_acl writes it")
	if err := flexParse(fs, args); err != nil {
		return core.ACLRow{}, err
	}
	if fs.NArg() < 2 || fs.NArg() > 3 {
		return core.ACLRow{}, fmt.Errorf("usage: %s", grantUsage)
	}
	return core.ACLRow{
		Principal: fs.Arg(0), Scope: fs.Arg(1), Action: fs.Arg(2),
		Effect: *effect, Params: *params,
	}, nil
}

// checkGrantRow refuses a row no evaluator would read the way the operator
// typed it. The effect check is here and not only in store.aclPut because
// RemoveACLRow does NOT validate: a delete of a coordinate nobody wrote
// succeeds, so a typo'd effect on `ungrant` would report success and leave the
// rule standing.
func checkGrantRow(row core.ACLRow) error {
	if row.Principal == "" || row.Scope == "" {
		return errEmptyGrant
	}
	if err := store.ValidateACLAction(row.Action); err != nil {
		return err
	}
	if !store.EffectAllowed(row.Effect) {
		return fmt.Errorf("unknown effect %q — %q or %q; every other spelling reads as a "+
			"grant to the evaluator", row.Effect, store.EffectAllow, store.EffectDeny)
	}
	return nil
}

// operatorRow reports whether the row is the operator membership rather than a
// per-folder grant. BOTH verbs ask, and they must agree: `ungrant sub **` used
// to drop the operator role whatever action it carried, so revoking one `**`
// rule took the whole role with it.
func operatorRow(row core.ACLRow) bool {
	return row.Scope == "**" && row.Action == "admin" &&
		row.Effect == store.EffectAllow && row.Params == ""
}

// runGrant writes acl rows through the AUDITED AddACLRow / AddMembership:
// routd.db HAS had audit_log since routd migration 0016, so the grant and its
// audit row commit together, matching what the same grant already records
// through dashd and MCP. Granting authority is the mutation an audit trail
// exists for.
func runGrant(s *store.Store, row core.ACLRow, w io.Writer) error {
	row = store.GrantDefaults(row)
	if err := checkGrantRow(row); err != nil {
		return err
	}
	// The third write surface reads the same alias warning add_acl and dashd's
	// form do. A row written against a linked alias looks applied and never
	// fires, and the denial afterwards reads as policy (BUGS L152).
	warn := func(m string) { _, _ = fmt.Fprintln(w, "warning: "+m) }
	if operatorRow(row) {
		// Operator grant: add to role:operator instead of a per-folder row.
		if err := s.AddMembership(row.Principal, "role:operator", "arizuko grant"); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "granted %s -> role:operator\n", row.Principal)
		grantPrincipalWarn(row.Principal, warn)
		return nil
	}
	row.GrantedBy = "arizuko grant"
	if err := s.AddACLRow(row); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "granted "+describeGrant(row))
	grantPrincipalWarn(row.Principal, warn)
	return nil
}

func runUngrant(s *store.Store, row core.ACLRow, w io.Writer) error {
	row = store.GrantDefaults(row)
	if err := checkGrantRow(row); err != nil {
		return err
	}
	if operatorRow(row) {
		if err := s.RemoveMembership(row.Principal, "role:operator"); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "ungranted %s -> role:operator\n", row.Principal)
		return nil
	}
	if err := s.RemoveACLRow(row); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "ungranted "+describeGrant(row))
	return nil
}

func runGrants(s *store.Store, sub string, w io.Writer) error {
	// "no grants" must mean no grants, not "could not read them" (BUGS A4).
	rows, err := s.ListACL(sub)
	if err != nil {
		return err
	}
	if len(rows) == 0 && len(s.Ancestors(sub)) == 0 {
		_, _ = fmt.Fprintln(w, "no grants")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// PARAMS is in the table because it is in the KEY. A listing that hides it
	// shows two narrowings of one tool as one indistinguishable line, and an
	// operator cannot type the `ungrant` that removes either (L138).
	_, _ = fmt.Fprintln(tw, "PRINCIPAL\tACTION\tSCOPE\tEFFECT\tPARAMS\tGRANTED_AT")
	for _, r := range rows {
		ts := r.GrantedAt
		if ts == "" {
			ts = "-"
		}
		params := r.Params
		if params == "" {
			params = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Principal, r.Action, r.Scope, r.Effect, params, ts)
	}
	// Also show role memberships transitively reachable from sub.
	if anc := s.Ancestors(sub); len(anc) > 0 {
		_, _ = fmt.Fprintf(tw, "\nMEMBER OF\n")
		for _, p := range anc {
			_, _ = fmt.Fprintf(tw, "  %s\n", p)
		}
	}
	return tw.Flush()
}

func cmdGate(args []string) {
	need(args, 2, "arizuko gate <instance> <list|add|rm|enable|disable> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s := mustOpenOnbod(dataDir)
	defer func() { _ = s.Close() }()

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
		_, _ = fmt.Fprintln(tw, "GATE\tLIMIT/DAY\tENABLED")
		for _, g := range gates {
			en := "yes"
			if !g.Enabled {
				en = "no"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n", g.Gate, g.LimitPerDay, en)
		}
		_ = tw.Flush()

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
	defer func() { _ = s.Close() }()

	switch action {
	case "create":
		glob, maxUses, expiresAt, err := parseInviteCreate(args[2:])
		if err != nil {
			die("usage: arizuko invite <instance> create <target_glob> [--max-uses|-n N] [--expires|-e DURATION]: %v", err)
		}
		inv, rawToken, err := s.CreateInvite(glob, "cli", maxUses, expiresAt)
		if err != nil {
			die("Failed: %v", err)
		}
		auditCLI(s, "invite create", []string{glob})
		fmt.Printf("token: %s\n", rawToken)
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
		_, _ = fmt.Fprintln(tw, "REF\tTARGET_GLOB\tISSUED_BY\tISSUED_AT\tEXPIRES_AT\tUSED")
		for _, inv := range invs {
			exp := "-"
			if inv.ExpiresAt != nil {
				exp = inv.ExpiresAt.Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d/%d\n",
				inv.Ref, inv.TargetGlob, inv.IssuedBySub,
				inv.IssuedAt.Format(time.RFC3339), exp,
				inv.UsedCount, inv.MaxUses)
		}
		_ = tw.Flush()

	case "revoke":
		need(args, 3, "arizuko invite <instance> revoke <ref>")
		if err := s.RevokeInviteByRef(args[2]); err != nil {
			die("Failed: %v", err)
		}
		auditCLI(s, "invite revoke", []string{args[2]})
		fmt.Printf("invite revoked: %s\n", args[2])

	default:
		die("unknown invite action: %s", action)
	}
}

// cmdChat launches `claude` (Claude Code CLI) wired to the instance's root
// IPC socket via socat. Local-operator only — socket access is the auth.
func cmdChat(args []string) {
	need(args, 1, "arizuko chat <instance> [--folder <folder>]")
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	folder := fs.String("folder", "main", "group folder for the IPC socket")
	if err := flexParse(fs, args); err != nil || fs.NArg() != 1 || !groupfolder.IsValidFolder(*folder) {
		die("usage: arizuko chat <instance> [--folder <folder>]")
	}
	dataDir := mustInstanceDir(fs.Arg(0))

	sock := filepath.Join(dataDir, "ipc", *folder, "gated.sock")
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
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := json.NewEncoder(tmp).Encode(cfg); err != nil {
		die("Failed: write config: %v", err)
	}
	// claude reads this file next, so a close that lost the tail hands it a
	// truncated config rather than none.
	if err := tmp.Close(); err != nil {
		die("Failed: close config: %v", err)
	}

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
	// `docker compose ps` prints its own diagnosis on stderr and a non-zero exit
	// adds nothing; the channel probe below is the rest of the answer either way.
	_ = cmd.Run()

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
	defer func() { _ = resp.Body.Close() }()
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
