package container

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/diary"
	"github.com/onvos/arizuko/grants"
	"github.com/onvos/arizuko/groupfolder"
	"github.com/onvos/arizuko/ipc"
	"github.com/onvos/arizuko/mountsec"
	"github.com/onvos/arizuko/router"
)

const (
	maxOutputSize = 10 * 1024 * 1024 // 10MB
	containerHome = "/home/node"
)

// execCommand is the hook used to spawn the docker CLI. Tests override it
// to avoid the real runtime while still exercising arg assembly.
var execCommand = exec.Command

var (
	safeNameRe  = regexp.MustCompile(`[^a-zA-Z0-9-]`)
	skillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
)

func SanitizeFolder(folder string) string {
	s := strings.ReplaceAll(folder, "/", "-")
	s = safeNameRe.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
}

// worldOf returns the top-level folder segment (tier-1 world).
// Empty for the root bot.
func worldOf(folder string, root bool) string {
	if root {
		return ""
	}
	if i := strings.IndexByte(folder, '/'); i >= 0 {
		return folder[:i]
	}
	return folder
}

// tierOf returns the bot's tier: 0 root, 1 world, 2 building, 3+ room.
func tierOf(folder string, root bool) int {
	if root {
		return 0
	}
	if folder == "" {
		return 0
	}
	return strings.Count(folder, "/") + 1
}

type Input struct {
	Prompt    string            `json:"prompt"`
	SessionID string            `json:"sessionId,omitempty"`
	ChatJID   string            `json:"chatJid"`
	Folder    string            `json:"groupFolder"`
	Topic     string            `json:"topic,omitempty"`
	AsstName  string            `json:"assistantName,omitempty"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	MsgCount  int               `json:"messageCount,omitempty"`
	Depth     int               `json:"delegateDepth,omitempty"`
	Channel   string            `json:"channelName,omitempty"`
	MessageID string            `json:"messageId,omitempty"`
	Grants    []string          `json:"grants,omitempty"`
	Sender    string            `json:"sender,omitempty"`
	Soul      string            `json:"soul,omitempty"`
	SystemMd  string            `json:"systemMd,omitempty"`

	GroupPath   string           `json:"-"`
	Name        string           `json:"-"`
	GroupName   string           `json:"-"`
	Parent      string           `json:"-"`
	Config      core.GroupConfig `json:"-"`
	SlinkToken  string           `json:"-"`
	Annotations []string         `json:"-"`
	GatedFns    ipc.GatedFns     `json:"-"`
	StoreFns    ipc.StoreFns     `json:"-"`

	// SecretsResolver resolves folder + user secrets at spawn time.
	// nil disables injection (only `base` env from gated process is used).
	SecretsResolver SecretsResolver `json:"-"`

	// Egress optionally enables crackbox/egred network isolation. Zero
	// value disables it; agent spawns on the default Docker bridge.
	Egress EgressConfig `json:"-"`
}

// SecretsResolver is the subset of *store.Store the container runner needs to
// resolve folder + user secrets at spawn. See specs/7/35-tenant-self-service.md.
type SecretsResolver interface {
	FolderSecretsResolved(folder string) (map[string]string, error)
	UserSecrets(userSub string) (map[string]string, error)
	GetChatIsGroup(jid string) bool
	UserSubByJID(jid string) (string, bool)
}

type Output struct {
	Status       string `json:"status"` // success|error
	Result       string `json:"result"`
	NewSessionID string `json:"newSessionId,omitempty"`
	Error        string `json:"error,omitempty"`
	HadOutput    bool   `json:"-"`
}

// Runner runs a containerized agent invocation. The default implementation
// is DockerRunner (package-level Run). Tests inject fakes.
type Runner interface {
	Run(cfg *core.Config, folders *groupfolder.Resolver, in Input) Output
}

// DockerRunner is the production Runner backed by the docker CLI.
type DockerRunner struct{}

// Run delegates to the package-level Run.
func (DockerRunner) Run(cfg *core.Config, folders *groupfolder.Resolver, in Input) Output {
	return Run(cfg, folders, in)
}

type volumeMount struct {
	Host      string
	Container string
	RO        bool
}

func Run(cfg *core.Config, folders *groupfolder.Resolver, in Input) Output {
	root := !strings.Contains(in.Folder, "/")

	groupDir := in.GroupPath
	if groupDir == "" {
		groupDir, _ = folders.GroupPath(in.Folder)
	}
	os.MkdirAll(groupDir, 0o755)
	writeGatewayCaps(groupDir, cfg)

	mounts := buildMounts(cfg, in, groupDir, root, folders)
	in = prepareInput(cfg, in, groupDir)

	ipcDir, _ := folders.IpcPath(in.Folder)

	containerName := in.Name
	if containerName == "" {
		safe := safeNameRe.ReplaceAllString(in.Folder, "-")
		containerName = fmt.Sprintf(
			"arizuko-%s-%s-%d", cfg.Name, safe, time.Now().UnixMilli())
	}

	args := buildArgs(cfg, mounts, containerName, in.Egress.Network)

	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)

	slog.Info("spawning container",
		"group", in.Folder, "container", containerName,
		"mounts", len(mounts), "root", root, "session", in.SessionID != "")
	slog.Debug("container args",
		"group", in.Folder,
		"args", strings.Join(args, " "))

	start := time.Now()

	cmd := execCommand(Bin, args...)
	cmd.Stdout = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Output{Error: "stdin pipe: " + err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Output{Error: "stderr pipe: " + err.Error()}
	}

	stopMCP := func() {}
	if ipcDir != "" {
		sockPath := groupfolder.IpcSocket(ipcDir)
		// Expected peer uid for SO_PEERCRED check and socket chown.
		// Defaults to 1000 (ant image's `node` user). In dev, the host
		// uid may be propagated via --user <uid>:<gid>; match it.
		expectedUID := 1000
		if uid := os.Getuid(); uid > 0 && uid != 1000 {
			expectedUID = uid
		}
		if stop, err := ipc.ServeMCP(sockPath, in.GatedFns, in.StoreFns, in.Folder, in.Grants, expectedUID); err != nil {
			slog.Warn("failed to start MCP server",
				"group", in.Folder, "container", containerName, "err", err)
		} else {
			stopMCP = stop
		}
	}

	if err := cmd.Start(); err != nil {
		stopMCP()
		return Output{Error: "start: " + err.Error()}
	}

	egressIP, eerr := registerEgress(in.Egress, in.Folder, containerName)
	if eerr != nil {
		slog.Warn("egress register failed",
			"group", in.Folder, "container", containerName, "err", eerr)
	}
	defer unregisterEgress(in.Egress, egressIP)

	in.Secrets = resolveSpawnEnv(in.SecretsResolver, readSecrets(), in.Folder, in.ChatJID)
	in.AsstName = cfg.Name
	payload, _ := json.Marshal(in)
	in.Secrets = nil
	if _, err := stdin.Write(payload); err != nil {
		slog.Error("stdin write failed", "group", in.Folder, "err", err)
	}
	stdin.Close()

	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	// resetIdle is wired to the idle timer below; declared early so the
	// stderr goroutine can reach it. Capped to bound runaway agents.
	var idleResets atomic.Int32
	const maxIdleResets = 240 // 60s * 240 = 4h max via idle resets
	resetIdle := func() {}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), maxOutputSize)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "[ant]") {
				slog.Info("container agent",
					"group", in.Folder, "line", line)
				if idleResets.Add(1) <= maxIdleResets {
					resetIdle()
				}
			} else {
				slog.Debug("container stderr",
					"group", in.Folder, "line", line)
			}
			stderrMu.Lock()
			if stderrBuf.Len() < maxOutputSize {
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
			}
			stderrMu.Unlock()
		}
	}()

	cfgTimeout := cfg.Timeout
	if in.Config.Timeout > 0 {
		cfgTimeout = in.Config.Timeout
	}
	grace := cfg.IdleTimeout + 30*time.Second
	if cfgTimeout < grace {
		cfgTimeout = grace
	}

	var timedOut atomic.Bool
	var stopOnce sync.Once
	stopContainer := func(reason string) {
		stopOnce.Do(func() {
			timedOut.Store(true)
			slog.Info("container stopping",
				"reason", reason, "group", in.Folder, "container", containerName)
			stop := exec.Command(
				Bin, StopContainerArgs(containerName)...)
			if err := stop.Run(); err != nil {
				slog.Warn("graceful stop failed, killing container",
					"group", in.Folder, "container", containerName, "err", err)
				// docker stop failed: kill the container itself via docker kill.
				// cmd.Process.Kill() only kills the local docker CLI client,
				// leaving the container running (orphan).
				if kerr := exec.Command(Bin, "kill", containerName).Run(); kerr != nil {
					slog.Warn("docker kill failed, forcing removal",
						"group", in.Folder, "container", containerName, "err", kerr)
					exec.Command(Bin, "rm", "-f", containerName).Run()
				}
			}
		})
	}
	deadline := time.AfterFunc(cfgTimeout, func() {
		stopContainer("hard deadline")
	})

	var softDeadline *time.Timer
	if cfgTimeout > 2*time.Minute {
		softDeadline = time.AfterFunc(cfgTimeout-2*time.Minute, func() {
			if timedOut.Load() {
				return
			}
			slog.Info("soft deadline firing, warning agent",
				"group", in.Folder, "container", containerName)
			if ipcDir != "" {
				inputDir := groupfolder.IpcInputDir(ipcDir)
				os.MkdirAll(inputDir, 0o755)
				name := fmt.Sprintf("%d-deadline.json", time.Now().UnixMilli())
				fp := filepath.Join(inputDir, name)
				tmp := fp + ".tmp"
				payload, _ := json.Marshal(map[string]string{
					"type": "message",
					"text": "\u26a0\ufe0f SYSTEM: You have ~2 minutes before this session is forcefully terminated. Wrap up NOW: summarize what you accomplished, what is still pending, and deliver your response to the user.",
				})
				if err := os.WriteFile(tmp, payload, 0o644); err == nil {
					if err := os.Rename(tmp, fp); err != nil {
						os.Remove(tmp)
					}
				}
			}
			exec.Command(Bin, "kill", "--signal=SIGUSR1", containerName).Run()
		})
	}

	timer := time.AfterFunc(cfg.IdleTimeout, func() {
		stopContainer("idle timeout")
	})
	resetIdle = func() { timer.Reset(cfg.IdleTimeout) }

	exitErr := cmd.Wait()
	timer.Stop()
	deadline.Stop()
	if softDeadline != nil {
		softDeadline.Stop()
	}

	stopMCP()

	elapsed := time.Since(start)

	code := 0
	if exitErr != nil {
		if ee, ok := exitErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}

	stderrMu.Lock()
	stderrStr := stderrBuf.String()
	stderrMu.Unlock()

	ts := time.Now().Format("2006-01-02T15-04-05")
	logFile := filepath.Join(logsDir, "container-"+ts+".log")
	to := timedOut.Load()
	writeLog(logFile, in, containerName, elapsed, code, to, stderrStr, mounts)

	slog.Info("container exited",
		"group", in.Folder, "container", containerName, "code", code,
		"duration", elapsed, "timedOut", to)

	if to {
		slog.Error("container timed out",
			"group", in.Folder, "container", containerName,
			"duration", elapsed)
		return Output{
			Status: "error",
			Error: fmt.Sprintf(
				"Container timed out after %s", cfgTimeout),
		}
	}

	if code != 0 {
		slog.Error("container exited with error",
			"group", in.Folder, "container", containerName, "code", code,
			"duration", elapsed, "logFile", logFile)
		tail := stderrStr
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return Output{
			Status: "error",
			Error: fmt.Sprintf(
				"Container exited with code %d: %s", code, tail),
		}
	}

	return Output{Status: "success"}
}

func prepareInput(cfg *core.Config, in Input, groupDir string) Input {
	latest := MigrationVersion(
		filepath.Join(cfg.HostAppDir, "ant", "skills", "self", "MIGRATION_VERSION"))
	agent := MigrationVersion(
		filepath.Join(groupDir, ".claude", "skills", "self", "MIGRATION_VERSION"))
	if agent < latest {
		in.Annotations = append(in.Annotations, fmt.Sprintf(
			"[pending migration] Skills version %d < %d. "+
				"Run /migrate (main group) to sync all groups.",
			agent, latest))
	}
	if in.Topic != "" {
		in.Annotations = append(in.Annotations, "Topic session: "+in.Topic)
	}
	if ep := ReadRecentEpisodes(groupDir); ep != "" {
		in.Annotations = append(in.Annotations, ep)
	}
	if d := diary.Read(groupDir, 14); d != "" {
		in.Annotations = append(in.Annotations, d)
	}
	if wk := readOptional(filepath.Join(groupDir, "work.md")); wk != "" {
		in.Annotations = append(in.Annotations,
			"<knowledge layer=\"work\">\n"+wk+"\n</knowledge>")
	}
	if uc := router.UserContextXml(in.Sender, groupDir); uc != "" {
		in.Annotations = append(in.Annotations, uc)
	}

	in.Soul = readOptional(filepath.Join(groupDir, "SOUL.md"))
	in.SystemMd = readOptional(filepath.Join(groupDir, "SYSTEM.md"))

	in.Annotations = append(in.Annotations,
		"[resolve] Invoke /resolve now \u2014 classify, recall, "+
			"match skills, then respond.")

	if len(in.Annotations) > 0 {
		in.Prompt = strings.Join(in.Annotations, "\n") + "\n\n" + in.Prompt
	}
	return in
}

func buildMounts(
	cfg *core.Config, in Input,
	groupDir string, root bool,
	folders *groupfolder.Resolver,
) []volumeMount {
	var m []volumeMount

	m = append(m, volumeMount{
		Host:      hp(cfg, groupDir),
		Container: containerHome,
	})
	media := filepath.Join(groupDir, "media")
	os.MkdirAll(media, 0o755)

	m = append(m, volumeMount{
		Host:      cfg.HostAppDir,
		Container: "/workspace/self",
		RO:        true,
	})

	world := strings.SplitN(in.Folder, "/", 2)[0]
	shareRw := grants.CheckAction(in.Grants, "share_mount", map[string]string{"readonly": "false"})
	shareRo := !shareRw && grants.CheckAction(in.Grants, "share_mount", map[string]string{"readonly": "true"})
	if shareRw || shareRo {
		share := filepath.Join(cfg.GroupsDir, world, "share")
		os.MkdirAll(share, 0o755)
		m = append(m, volumeMount{
			Host:      hp(cfg, share),
			Container: "/workspace/share",
			RO:        !shareRw,
		})
	}

	claudeDir := filepath.Join(groupDir, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	seedSettings(claudeDir, cfg, in, root)

	ipcDir, err := folders.IpcPath(in.Folder)
	if err == nil {
		os.MkdirAll(groupfolder.IpcInputDir(ipcDir), 0o755)
		m = append(m, volumeMount{
			Host:      hp(cfg, ipcDir),
			Container: "/workspace/ipc",
		})
	}

	if os.Getenv("ARIZUKO_DEV") == "1" {
		runnerSrc := filepath.Join(cfg.HostAppDir, "ant", "src")
		if _, err := os.Stat(runnerSrc); err == nil {
			m = append(m, volumeMount{
				Host:      hp(cfg, runnerSrc),
				Container: "/app/src",
			})
		}
	}

	if len(in.Config.Mounts) > 0 {
		add := make([]mountsec.AdditionalMount, len(in.Config.Mounts))
		for i, cm := range in.Config.Mounts {
			ro := cm.RO
			add[i] = mountsec.AdditionalMount{
				HostPath: cm.Host, ContainerPath: cm.Container, Readonly: &ro,
			}
		}
		for _, v := range mountsec.ValidateAdditionalMounts(add, in.Folder, root, mountsec.Allowlist{}) {
			m = append(m, volumeMount{Host: v.HostPath, Container: v.ContainerPath, RO: v.Readonly})
		}
	}

	if fi, err := os.Stat(cfg.WebDir); err == nil && fi.IsDir() && strings.Count(in.Folder, "/") <= 2 {
		webHost := cfg.WebDir
		if !root {
			webHost = filepath.Join(cfg.WebDir, world)
			os.MkdirAll(webHost, 0o755)
		}
		m = append(m, volumeMount{
			Host:      hp(cfg, webHost),
			Container: "/workspace/web",
		})
	}

	if root {
		m = append(m, volumeMount{
			Host:      hp(cfg, cfg.GroupsDir),
			Container: "/workspace/data/groups",
		})
	}

	return m
}

func buildArgs(
	cfg *core.Config, mounts []volumeMount, name, network string,
) []string {
	args := []string{
		"run", "-i", "--rm",
		"--name", name,
		"--shm-size=1g",
		"-e", "TZ=" + cfg.Timezone,
	}
	if network != "" {
		args = append(args, "--network", network)
	}

	uid := os.Getuid()
	gid := os.Getgid()
	if uid > 0 && uid != 1000 {
		args = append(args,
			"--user", fmt.Sprintf("%d:%d", uid, gid),
			"-e", "HOME="+containerHome)
	}

	for _, m := range mounts {
		spec := m.Host + ":" + m.Container
		if m.RO {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}

	args = append(args, cfg.Image)
	return args
}

func hp(cfg *core.Config, local string) string {
	if cfg.HostProjectRoot == "" {
		return local
	}
	if !strings.HasPrefix(local, cfg.ProjectRoot) {
		return local
	}
	rel, _ := filepath.Rel(cfg.ProjectRoot, local)
	return filepath.Join(cfg.HostProjectRoot, rel)
}

func readSecrets() map[string]string {
	var s map[string]string
	for _, k := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY",
	} {
		if v := os.Getenv(k); v != "" {
			if s == nil {
				s = make(map[string]string, 2)
			}
			s[k] = v
		}
	}
	return s
}

// mergeSecrets returns a ∪ b with b overlaying a. Either may be nil.
func mergeSecrets(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// resolveSpawnEnv composes the env injected into the agent container per
// specs/7/35-tenant-self-service.md §Resolution: base ∪ folder ∪ user
// (user only when chat is single-user). When resolver is nil or its
// secrets API is disabled (no AUTH_SECRET), returns base unchanged.
func resolveSpawnEnv(
	resolver SecretsResolver, base map[string]string, folder, chatJID string,
) map[string]string {
	if resolver == nil {
		return base
	}
	folderSecrets, err := resolver.FolderSecretsResolved(folder)
	if err != nil {
		// ErrSecretCipherNotConfigured (no AUTH_SECRET) and any DB error
		// fall through quietly — base env still flows.
		slog.Debug("folder secrets resolve skipped", "folder", folder, "err", err)
		return base
	}
	merged := mergeSecrets(base, folderSecrets)

	if !resolver.GetChatIsGroup(chatJID) {
		if userSub, ok := resolver.UserSubByJID(chatJID); ok {
			userSecrets, err := resolver.UserSecrets(userSub)
			if err == nil {
				merged = mergeSecrets(merged, userSecrets)
			} else {
				slog.Debug("user secrets resolve skipped", "user_sub", userSub, "err", err)
			}
		}
	}
	return merged
}

func seedSettings(
	claudeDir string, cfg *core.Config,
	in Input, root bool,
) {
	fp := filepath.Join(claudeDir, "settings.json")
	var settings map[string]any
	if data, err := os.ReadFile(fp); err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}

	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{
			"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1",
			"CLAUDE_CODE_DISABLE_AUTO_MEMORY":              "0",
		}
	}

	env["WEB_HOST"] = cfg.WebHost
	env["ARIZUKO_ASSISTANT_NAME"] = cfg.Name
	env["ARIZUKO_IS_ROOT"] = ""
	if root {
		env["ARIZUKO_IS_ROOT"] = "1"
		env["WEB_PREFIX"] = "pub"
	} else {
		env["WEB_PREFIX"] = "pub/" + in.Folder
	}
	env["ARIZUKO_DELEGATE_DEPTH"] = strconv.Itoa(in.Depth)
	env["ARIZUKO_GROUP_FOLDER"] = in.Folder
	env["ARIZUKO_GROUP_NAME"] = in.GroupName
	env["ARIZUKO_GROUP_PARENT"] = in.Parent
	env["ARIZUKO_WORLD"] = worldOf(in.Folder, root)
	env["ARIZUKO_TIER"] = strconv.Itoa(tierOf(in.Folder, root))
	if in.Channel != "" {
		settings["outputStyle"] = in.Channel
	}
	if in.SlinkToken != "" {
		env["SLINK_TOKEN"] = in.SlinkToken
	}
	settings["env"] = env

	servers, _ := settings["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["arizuko"] = map[string]any{
		"command": "socat",
		"args":    []string{"STDIO", "UNIX-CONNECT:/workspace/ipc/gated.sock"},
	}
	settings["mcpServers"] = servers

	data, _ := json.MarshalIndent(settings, "", "  ")
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		slog.Warn("failed to write settings tmp", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, fp); err != nil {
		slog.Warn("failed to rename settings", "from", tmp, "to", fp, "err", err)
	}
	slog.Debug("settings seeded", "path", fp)
}

func SetupGroup(cfg *core.Config, folder, prototype string) error {
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return fmt.Errorf("mkdir group: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(groupDir, "logs"), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	if prototype != "" {
		if err := chanlib.CopyDirNoSymlinks(prototype, groupDir); err != nil {
			slog.Warn("setup group: copy prototype", "folder", folder, "err", err)
		}
	}
	return seedGroupDir(cfg, folder)
}

func seedGroupDir(cfg *core.Config, folder string) error {
	claudeDir := filepath.Join(cfg.GroupsDir, folder, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	seedSkills(cfg, claudeDir, folder)
	return nil
}

func seedSkills(cfg *core.Config, claudeDir, folder string) {
	src := filepath.Join(cfg.HostAppDir, "ant", "skills")
	dst := filepath.Join(claudeDir, "skills")

	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !skillNameRe.MatchString(e.Name()) {
			slog.Warn("skipping skill with invalid name",
				"name", e.Name())
			continue
		}
		d := filepath.Join(dst, e.Name())
		// Re-seed on every call so upstream skill updates propagate.
		// Extra files added locally are preserved (cpDir only overwrites).
		cpDir(filepath.Join(src, e.Name()), d)
	}

	mdSrc := filepath.Join(cfg.HostAppDir, "ant", "CLAUDE.md")
	mdDst := filepath.Join(claudeDir, "CLAUDE.md")
	if data, err := os.ReadFile(mdSrc); err == nil {
		os.WriteFile(mdDst, data, 0o644)
	}

	jsonDst := filepath.Join(claudeDir, ".claude.json")
	if _, err := os.Stat(jsonDst); os.IsNotExist(err) {
		userID := fmt.Sprintf("%x", sha256.Sum256([]byte("arizuko:"+folder)))
		data, _ := json.MarshalIndent(map[string]any{
			"firstStartTime": time.Now().Format(time.RFC3339),
			"userID":         userID,
		}, "", "  ")
		os.WriteFile(jsonDst, append(data, '\n'), 0o644)
	}
}

func readOptional(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeGatewayCaps(groupDir string, cfg *core.Config) {
	var b strings.Builder
	fmt.Fprintf(&b, "[voice]\nenabled = %v\nmodel = %q\n\n",
		cfg.VoiceEnabled, cfg.WhisperModel)
	fmt.Fprintf(&b, "[video]\nenabled = %v\n\n", cfg.VideoEnabled)
	fmt.Fprintf(&b, "[media]\nenabled = %v\nmax_size_mb = %d\n\n",
		cfg.MediaEnabled, cfg.MediaMaxBytes/(1024*1024))
	if cfg.WebHost != "" {
		fmt.Fprintf(&b, "[web]\nenabled = true\nhost = %q\n", cfg.WebHost)
	} else {
		fmt.Fprintf(&b, "[web]\nenabled = false\n")
	}
	os.WriteFile(filepath.Join(groupDir, ".gateway-caps"),
		[]byte(b.String()), 0o644)
}

func MigrationVersion(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var v int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &v)
	return v
}

func cpDir(src, dst string) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		slog.Warn("cpDir: mkdir failed", "path", dst, "err", err)
		return
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		slog.Warn("cpDir: readdir failed", "path", src, "err", err)
		return
	}
	for _, e := range entries {
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		// Use Lstat so symlinks are detected here rather than followed;
		// copying a symlink's target would leak arbitrary host files
		// into the group-writable skills tree.
		fi, err := os.Lstat(sp)
		if err != nil {
			slog.Warn("cpDir: lstat failed", "path", sp, "err", err)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			slog.Warn("cpDir: skipping symlink", "path", sp)
			continue
		}
		if fi.IsDir() {
			cpDir(sp, dp)
		} else if data, err := os.ReadFile(sp); err != nil {
			slog.Warn("cpDir: read failed", "path", sp, "err", err)
		} else if err := os.WriteFile(dp, data, 0o644); err != nil {
			slog.Warn("cpDir: write failed", "path", dp, "err", err)
		}
	}
}

func writeLog(
	path string, in Input,
	cname string, dur time.Duration,
	code int, timedOut bool,
	stderr string,
	mounts []volumeMount,
) {
	isErr := code != 0 || timedOut
	lvl := os.Getenv("LOG_LEVEL")
	verbose := lvl == "debug" || lvl == "trace"

	suffix := ""
	if timedOut {
		suffix = " (TIMEOUT)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== Container Run Log%s ===\n", suffix)
	fmt.Fprintf(&b, "Timestamp: %s\nGroup: %s\nContainer: %s\nDuration: %s\nExit Code: %d\n",
		time.Now().Format(time.RFC3339), in.Folder, cname, dur, code)

	b.WriteString("\n=== Mounts ===\n")
	for _, m := range mounts {
		ro := ""
		if m.RO {
			ro = " (ro)"
		}
		if verbose || isErr {
			fmt.Fprintf(&b, "%s -> %s%s\n", m.Host, m.Container, ro)
		} else {
			fmt.Fprintf(&b, "%s%s\n", m.Container, ro)
		}
	}

	if verbose || isErr {
		fmt.Fprintf(&b, "\n=== Input ===\n")
		ij, _ := json.MarshalIndent(in, "", "  ")
		b.Write(ij)
		fmt.Fprintf(&b, "\n\n=== Stderr ===\n%s\n", stderr)
	} else {
		sid := in.SessionID
		if sid == "" {
			sid = "new"
		}
		fmt.Fprintf(&b, "\n=== Input Summary ===\nPrompt length: %d chars\nSession ID: %s\n",
			len(in.Prompt), sid)
	}

	os.WriteFile(path, []byte(b.String()), 0o644)
	slog.Debug("container log written", "logFile", path, "verbose", verbose)
}

func writeSnapshot(folders *groupfolder.Resolver, folder, filename string, v any) {
	ipcDir, err := folders.IpcPath(folder)
	if err != nil {
		slog.Warn("cannot write snapshot", "folder", folder, "file", filename, "err", err)
		return
	}
	os.MkdirAll(ipcDir, 0o755)
	data, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(filepath.Join(ipcDir, filename), data, 0o644)
}

func WriteTasksSnapshot(
	folders *groupfolder.Resolver,
	folder string, isRoot bool,
	tasks []core.Task,
) {
	if !isRoot {
		var f []core.Task
		for _, t := range tasks {
			if t.Owner == folder {
				f = append(f, t)
			}
		}
		tasks = f
	}
	writeSnapshot(folders, folder, "current_tasks.json", tasks)
}

func WriteGroupsSnapshot(
	folders *groupfolder.Resolver,
	folder string, isRoot bool,
	groups []core.Group,
) {
	if !isRoot {
		groups = nil
	}
	writeSnapshot(folders, folder, "available_groups.json", struct {
		Groups   []core.Group `json:"groups"`
		LastSync string       `json:"lastSync"`
	}{groups, time.Now().Format(time.RFC3339)})
}
