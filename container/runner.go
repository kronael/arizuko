package container

import (
	"bufio"
	"context"
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

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/diary"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/mountsec"
	"github.com/kronael/arizuko/router"
)

const (
	maxOutputSize               = 10 * 1024 * 1024 // 10MB
	containerHome               = core.ContainerHome
	containerGracePeriod        = 30 * time.Second
	containerSoftDeadlineOffset = 2 * time.Minute
)

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

func worldOf(folder string, root bool) string {
	if root {
		return ""
	}
	if i := strings.IndexByte(folder, '/'); i >= 0 {
		return folder[:i]
	}
	return folder
}

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

	GroupPath string           `json:"-"`
	Name      string           `json:"-"`
	Config    core.GroupConfig `json:"-"`
	Model     string           `json:"-"` // per-group model override; empty = instance default
	// QueryTimeoutMs is the agent's in-container query timeout, derived from
	// runed's RunTTL and set just below it so the agent aborts + delivers a
	// graceful summary BEFORE runed's hard container kill. 0 = unset (agent
	// uses its built-in default).
	QueryTimeoutMs int64        `json:"-"`
	Annotations    []string     `json:"-"`
	GatedFns       ipc.GatedFns `json:"-"`
	StoreFns       ipc.StoreFns `json:"-"`
	// ExternalMCP: the MCP socket is owned by the caller (routd hosts it
	// in-process); skip the in-container ServeMCP, just mount the ipc dir.
	ExternalMCP bool `json:"-"`

	Egress EgressConfig `json:"-"`

	// PaneLookup returns true when the given platform channel ID
	// corresponds to an open Slack assistant pane. Used by
	// pickOutputStyle to map slack:<ws>/channel/<id> → slack-pane.
	// Other platforms always pass nil or a func returning false.
	// Spec 5/O.
	PaneLookup func(channelID string) bool `json:"-"`
}

type Output struct {
	Status       string `json:"status"` // success|error
	Result       string `json:"result"`
	NewSessionID string `json:"newSessionId,omitempty"`
	Error        string `json:"error,omitempty"`
	HadOutput    bool   `json:"-"`
	// ExitCode is the container's process exit code (0 = clean). MessageCount
	// is the number of [ant] agent lines observed on stderr — a per-spawn
	// activity count runed echoes into session_log (spec 5/P § envelope 6).
	ExitCode     int `json:"-"`
	MessageCount int `json:"-"`
}

// Runner runs a containerized agent invocation. Tests inject fakes.
type Runner interface {
	Run(cfg *core.Config, folders *groupfolder.Resolver, in Input) Output
}

// DockerRunner is the production Runner backed by the docker CLI.
type DockerRunner struct{}

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

	// Tier 0/1 are operator-run bots; append "*" so they pass crackbox
	// unconstrained while still benefiting from logging/secret injection.
	if tierOf(in.Folder, root) <= 1 && in.Egress.AllowlistFn != nil {
		base := in.Egress.AllowlistFn
		in.Egress.AllowlistFn = func(id string) ([]string, error) {
			list, err := base(id)
			if err != nil {
				return nil, err
			}
			return append(list, "*"), nil
		}
	}

	egressNet, egressIP, eerr := registerEgress(in.Egress, in.Folder)
	if eerr != nil {
		return Output{Status: "error", Error: "egress register: " + eerr.Error()}
	}
	defer unregisterEgress(in.Egress, egressIP)

	args := buildArgs(cfg, mounts, containerName, in.Egress, egressNet, egressIP, in.Model, in.QueryTimeoutMs)

	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)

	slog.Info("spawning container",
		"group", in.Folder, "container", containerName,
		"mounts", len(mounts), "root", root, "session", in.SessionID != "")
	slog.Debug("container args",
		"group", in.Folder,
		"args", strings.Join(args, " "))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryAgent,
		Action:   "container.spawn",
		Actor:    "system",
		Surface:  audit.SurfaceGateway,
		Resource: "containers/" + containerName,
		Folder:   in.Folder,
		TurnID:   in.SessionID,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"image": cfg.Image,
			"root":  root,
		},
	})

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
	if ipcDir != "" && !in.ExternalMCP {
		sockPath := groupfolder.IpcSocket(ipcDir)
		// Expected peer uid for SO_PEERCRED check and socket chown.
		// Defaults to 1000 (ant image's `node` user). In dev, the host
		// uid may be propagated via --user <uid>:<gid>; match it.
		expectedUID := 1000
		if uid := os.Getuid(); uid > 0 && uid != 1000 {
			expectedUID = uid
		}
		if stop, err := ipc.ServeMCP(sockPath, in.GatedFns, in.StoreFns, in.Folder, in.Grants, expectedUID, os.Getenv("ARIZUKO_LOCAL_SUB")); err != nil {
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

	// Container env carries operator anchors only (ANTHROPIC_API_KEY /
	// CLAUDE_CODE_OAUTH_TOKEN — required for the Claude Code SDK to call
	// the LLM). Folder- and user-scoped secrets are broker-resolved at
	// tool-call time inside ipc.injectSecretsAdapter; spec 7/Y.
	in.Secrets = readSecrets()
	in.AsstName = cfg.Name
	payload, _ := json.Marshal(in)
	in.Secrets = nil
	if _, err := stdin.Write(payload); err != nil {
		slog.Error("stdin write failed", "group", in.Folder, "err", err)
	}
	stdin.Close()

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
				// docker kill, not cmd.Process.Kill() — the latter only kills the CLI client.
				if kerr := exec.Command(Bin, "kill", containerName).Run(); kerr != nil {
					slog.Warn("docker kill failed, forcing removal",
						"group", in.Folder, "container", containerName, "err", kerr)
					exec.Command(Bin, "rm", "-f", containerName).Run()
				}
			}
		})
	}

	// Idle timer + resetIdle are created before the stderr goroutine that
	// reads resetIdle — the var is written exactly once, prior to any
	// concurrent read, so no synchronization on the func value is needed.
	idleTimer := time.AfterFunc(cfg.IdleTimeout, func() {
		stopContainer("idle timeout")
	})
	resetIdle := func() { idleTimer.Reset(cfg.IdleTimeout) }

	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	// Declared early so the stderr goroutine can reach it.
	var idleResets atomic.Int32
	const maxIdleResets = 240 // 60s * 240 = 4h max via idle resets
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
	grace := cfg.IdleTimeout + containerGracePeriod
	if cfgTimeout < grace {
		cfgTimeout = grace
	}

	deadline := time.AfterFunc(cfgTimeout, func() {
		stopContainer("hard deadline")
	})

	var softDeadline *time.Timer
	if cfgTimeout > containerSoftDeadlineOffset {
		softDeadline = time.AfterFunc(cfgTimeout-containerSoftDeadlineOffset, func() {
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

	exitErr := cmd.Wait()
	idleTimer.Stop()
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
	exitOutcome := audit.OutcomeOK
	if code != 0 || to {
		exitOutcome = audit.OutcomeError
	}
	exitErrMsg := ""
	if to {
		exitErrMsg = "timeout"
	} else if code != 0 {
		exitErrMsg = fmt.Sprintf("exit_code=%d", code)
	}
	audit.Emit(context.Background(), audit.Event{
		Category:   audit.CategoryAgent,
		Action:     "container.exit",
		Actor:      "system",
		Surface:    audit.SurfaceGateway,
		Resource:   "containers/" + containerName,
		Folder:     in.Folder,
		TurnID:     in.SessionID,
		Outcome:    exitOutcome,
		ErrorMsg:   exitErrMsg,
		DurationMS: elapsed.Milliseconds(),
		ParamsSummary: map[string]any{
			"exit_code": code,
			"timed_out": to,
		},
	})

	msgCount := int(idleResets.Load()) // [ant] lines observed this spawn

	if to {
		slog.Error("container timed out",
			"group", in.Folder, "container", containerName,
			"duration", elapsed)
		return Output{
			Status: "error",
			Error: fmt.Sprintf(
				"Container timed out after %s", cfgTimeout),
			ExitCode:     code,
			MessageCount: msgCount,
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
			ExitCode:     code,
			MessageCount: msgCount,
		}
	}

	return Output{Status: "success", ExitCode: code, MessageCount: msgCount}
}

func prepareInput(cfg *core.Config, in Input, groupDir string) Input {
	latest := MigrationVersion(
		filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills", "self", "MIGRATION_VERSION"))
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

	// Auto-migrate the old SOUL.md name to PERSONA.md on read.
	personaPath := filepath.Join(groupDir, "PERSONA.md")
	soulPath := filepath.Join(groupDir, "SOUL.md")
	if _, err := os.Stat(personaPath); err != nil {
		if _, err := os.Stat(soulPath); err == nil {
			_ = os.Rename(soulPath, personaPath)
		}
	}
	in.Soul = readOptional(personaPath)
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
		Container: "/opt/arizuko",
		RO:        true,
	})

	// Every container gets the world's shared dir at /var/lib/share (writable):
	// a per-world scratch/handoff space all its groups see. Root groups
	// (world == "") share the instance-wide groups/share. A `share_mount`
	// grant with readonly=true downgrades it to RO for that folder.
	world := worldOf(in.Folder, root)
	share := filepath.Join(cfg.GroupsDir, world, "share")
	os.MkdirAll(share, 0o755)
	m = append(m, volumeMount{
		Host:      hp(cfg, share),
		Container: "/var/lib/share",
		RO:        grants.CheckAction(in.Grants, "share_mount", map[string]string{"readonly": "true"}),
	})

	claudeDir := filepath.Join(groupDir, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	seedSettings(claudeDir, cfg, in, root)

	ipcDir, err := folders.IpcPath(in.Folder)
	if err == nil {
		os.MkdirAll(groupfolder.IpcInputDir(ipcDir), 0o755)
		m = append(m, volumeMount{
			Host:      hp(cfg, ipcDir),
			Container: "/run/ipc",
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

	// Layered codex mount: per-group writable dir first, then RO file
	// overmounts for shared creds. Order matters for docker volume layering.
	// No os.Stat — HOST paths are resolved by the docker daemon at spawn time.
	if cfg.HostCodexDir != "" {
		groupCodex := filepath.Join(groupDir, ".codex")
		os.MkdirAll(groupCodex, 0o755)
		m = append(m, volumeMount{Host: hp(cfg, groupCodex), Container: containerHome + "/.codex"})
		m = append(m, volumeMount{
			Host: filepath.Join(cfg.HostCodexDir, "auth.json"), Container: containerHome + "/.codex/auth.json", RO: true,
		})
		m = append(m, volumeMount{
			Host: filepath.Join(cfg.HostCodexDir, "config.toml"), Container: containerHome + "/.codex/config.toml", RO: true,
		})
	}

	if len(in.Config.Mounts) > 0 {
		add := make([]mountsec.AdditionalMount, len(in.Config.Mounts))
		for i, cm := range in.Config.Mounts {
			ro := cm.RO
			add[i] = mountsec.AdditionalMount{HostPath: cm.Host, ContainerPath: cm.Container, Readonly: &ro}
		}
		for _, v := range mountsec.ValidateAdditionalMounts(add, in.Folder, root, mountsec.Allowlist{}) {
			m = append(m, volumeMount{Host: v.HostPath, Container: v.ContainerPath, RO: v.Readonly})
		}
	}

	// specs/5/V-web-vhosts.md: tier 0-2 get RO access to the whole public
	// web tree at /var/lib/www, plus per-group bind-mount slots for the
	// writable web surfaces (~/public_html and ~/private_html). Tier 3+
	// get no web surface.
	pubHost := filepath.Join(cfg.WebDir, "pub")
	if fi, err := os.Stat(pubHost); err == nil && fi.IsDir() && tierOf(in.Folder, root) <= 2 {
		m = append(m, volumeMount{
			Host:      hp(cfg, pubHost),
			Container: "/var/lib/www",
			RO:        true,
		})
	}

	if tierOf(in.Folder, root) <= 2 {
		// ~/public_html: served at /pub/<folder>/ (no auth).
		pubGroupHost := filepath.Join(cfg.WebDir, "pub", in.Folder)
		os.MkdirAll(pubGroupHost, 0o755)
		m = append(m, volumeMount{
			Host:      hp(cfg, pubGroupHost),
			Container: filepath.Join(containerHome, "public_html"),
		})

		// ~/private_html: served at /priv/<folder>/ (OAuth/JWT).
		privGroupHost := filepath.Join(cfg.WebDir, "priv", in.Folder)
		os.MkdirAll(privGroupHost, 0o755)
		m = append(m, volumeMount{
			Host:      hp(cfg, privGroupHost),
			Container: filepath.Join(containerHome, "private_html"),
		})
	}

	if root {
		m = append(m, volumeMount{
			Host:      hp(cfg, cfg.GroupsDir),
			Container: "/var/lib/groups",
		})
	}

	return m
}

func buildArgs(
	cfg *core.Config, mounts []volumeMount, name string, egress EgressConfig, network, ip string,
	model string, queryTimeoutMs int64,
) []string {
	args := []string{
		"run", "-i", "--rm",
		"--name", name,
		"--shm-size=1g",
		"-e", "TZ=" + cfg.Timezone,
	}
	// ant reads these from process.env (index.ts model:, claude.ts timeout const at
	// module load) — settings.json's env block reaches the SDK subprocess, not ant
	// itself, so the model/timeout MUST arrive as real container env or they silently
	// fall to ant's opus default + 15min hardcode. Cost: sloth's fable override and
	// every RUNED_RUN_TIMEOUT bump were dead until 2026-06-09.
	if model != "" {
		args = append(args, "-e", "ARIZUKO_MODEL="+model)
	}
	if queryTimeoutMs > 0 {
		args = append(args, "-e", "ARIZUKO_QUERY_TIMEOUT_MS="+strconv.FormatInt(queryTimeoutMs, 10))
	}
	if network != "" {
		proxy := egress.ProxyURL
		if proxy == "" {
			proxy = "http://crackbox:3128"
		}
		args = append(args, "--network", network)
		if ip != "" {
			args = append(args, "--ip", ip)
		}
		args = append(args,
			"-e", "HTTP_PROXY="+proxy,
			"-e", "HTTPS_PROXY="+proxy,
			"-e", "NO_PROXY=localhost,127.0.0.1,gated,routd,crackbox",
			"-e", "NODE_OPTIONS=--require=/app/proxy-shim.js")
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

// seedOutputStyles refreshes the per-surface output-style files into the group's
// .claude/output-styles on EVERY spawn. cpDirOverwrite (not cpDirFresh): these are
// platform-managed content, not operator-editable — the agent never edits
// discord-channel.md. cpDirFresh skipped existing files, so a style tweak (e.g. the
// repeated discord length/header fixes) only ever reached freshly-created or
// hand-synced groups and silently regressed everywhere else. Overwrite makes every
// spawn carry the current source-tree style; a group's own CUSTOM style file (one
// not present in src) is left untouched (cpDirImpl only copies src entries).
func seedOutputStyles(cfg *core.Config, claudeDir string) {
	src := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "output-styles")
	dst := filepath.Join(claudeDir, "output-styles")
	cpDirOverwrite(src, dst)
}

// seedMigrateSkill force-refreshes the platform's self-update bootstrap skill on
// every spawn. migrate is the ONLY thing that syncs skills, so a stale copy can
// never update itself — it deadlocks every future migration. Cost 11 days of
// frozen skills on the live split: groups kept the pre-split migrate skill that
// reads the removed /workspace mount ("/migrate blocked: /workspace not mounted")
// while seedSkills' cpDirFresh refused to overwrite it. Unlike stock skills
// (operator-editable, 3-way merged), migrate is platform-owned — always
// overwritten, like output-styles. MIGRATION_VERSION lives under self/, untouched.
func seedMigrateSkill(cfg *core.Config, claudeDir string) {
	src := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills", "migrate")
	dst := filepath.Join(claudeDir, "skills", "migrate")
	cpDirOverwrite(src, dst)
}

func seedSettings(
	claudeDir string, cfg *core.Config,
	in Input, root bool,
) {
	seedOutputStyles(cfg, claudeDir)
	seedMigrateSkill(cfg, claudeDir)
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
	// WEB_PREFIX tells the agent its publishing surface: "pub" for root
	// (served at /pub/<folder>/), the world subdomain for tier 1-2, "" for
	// tier 3+ (no web mount). Spec: specs/5/V-web-vhosts.md.
	tier := tierOf(in.Folder, root)
	switch {
	case root:
		env["ARIZUKO_IS_ROOT"] = "1"
		env["WEB_PREFIX"] = "pub"
	case tier == 1:
		env["WEB_PREFIX"] = in.Folder // vhost subdomain prefix
	case tier == 2:
		env["WEB_PREFIX"] = worldOf(in.Folder, root) // same vhost as parent world
	default:
		env["WEB_PREFIX"] = "" // tier 3+: no mount, no surface
	}
	env["ARIZUKO_DELEGATE_DEPTH"] = strconv.Itoa(in.Depth)
	env["ARIZUKO_GROUP_FOLDER"] = in.Folder
	env["ARIZUKO_GROUP_NAME"] = groupfolder.NameOf(in.Folder)
	env["ARIZUKO_GROUP_PARENT"] = groupfolder.ParentOf(in.Folder)
	env["ARIZUKO_WORLD"] = worldOf(in.Folder, root)
	env["ARIZUKO_TIER"] = strconv.Itoa(tier)
	if in.Model != "" {
		env["ARIZUKO_MODEL"] = in.Model
	} else {
		delete(env, "ARIZUKO_MODEL")
	}
	if in.QueryTimeoutMs > 0 {
		env["ARIZUKO_QUERY_TIMEOUT_MS"] = strconv.FormatInt(in.QueryTimeoutMs, 10)
	}
	if in.Channel != "" {
		if name := pickOutputStyle(in.Channel, in.ChatJID, in.Topic, in.PaneLookup); name != "" {
			settings["outputStyle"] = name
		}
	}
	settings["env"] = env

	// Claude Code's permission + sandbox layer is asserted by the platform,
	// not the agent. arizuko already isolates via crackbox egress + Docker +
	// the gated MCP socket, so the agent runs with all tools allowed and the
	// SDK's own sandbox off. Write both authoritatively every spawn: a stray
	// agent-added permissions block (web egress lives in crackbox, never here)
	// must not accumulate, and a future SDK default can't silently re-enable
	// the sandbox.
	settings["permissions"] = map[string]any{"defaultMode": "bypassPermissions"}
	settings["sandbox"] = map[string]any{"enabled": false}

	servers, _ := settings["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["arizuko"] = map[string]any{
		"command": "socat",
		"args":    []string{"STDIO", "UNIX-CONNECT:/run/ipc/gated.sock"},
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

// pickOutputStyle resolves the agent's outputStyle setting for one turn.
// Derives <platform>-<surface> from the JID/topic/pane signal; falls
// back to <platform> when no surface split applies. Spec 5/O.
//
// Image contract: the agent image ships every per-surface file listed
// in the spec table. Claude Code's readOutputStyle reads the named
// file from <home>/.claude/output-styles/<name>.md at agent startup;
// per-group operator overrides at the same path take precedence
// automatically. No host-side existence check — name selection and
// file loading are separate concerns.
func pickOutputStyle(
	channel, chatJID, topic string,
	paneLookup func(channelID string) bool,
) string {
	if channel == "" {
		return ""
	}
	if surface := deriveSurface(channel, chatJID, topic, paneLookup); surface != "" {
		return channel + "-" + surface
	}
	return channel
}

// deriveSurface maps (channel, chatJID, topic, pane) to a surface
// suffix. Returns "" when the platform has no per-surface split.
// Mirrors the table in specs/5/Y-output-styles-per-surface.md.
func deriveSurface(
	channel, chatJID, topic string,
	paneLookup func(channelID string) bool,
) string {
	switch channel {
	case "slack":
		p, ok := chanlib.ParseSlackJID(chatJID)
		if !ok {
			return ""
		}
		switch p.Kind {
		case "dm":
			return "dm"
		case "group":
			return "channel"
		case "channel":
			if paneLookup != nil && paneLookup(p.ID) {
				return "pane"
			}
			if topic != "" {
				return "thread"
			}
			return "channel"
		}
	case "telegram":
		rest := strings.TrimPrefix(chatJID, "telegram:")
		if rest == chatJID {
			return ""
		}
		kind, _, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		switch kind {
		case "user":
			return "dm"
		case "group":
			return "group"
		}
	case "discord":
		rest := strings.TrimPrefix(chatJID, "discord:")
		if rest == chatJID {
			return ""
		}
		kind, _, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		if kind == "dm" {
			return "dm"
		}
		return "channel"
	}
	return ""
}

func SetupGroup(cfg *core.Config, folder, prototype string) error {
	r := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	groupDir, err := r.GroupPath(folder)
	if err != nil {
		return fmt.Errorf("invalid group folder: %w", err)
	}
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
	// Per-group web slots — bind-mounted into ~/public_html and ~/private_html
	// at agent spawn time. Pre-create here so the dirs exist before the first
	// docker run and inherit the container's uid via chownR in seedGroupDir.
	// Spec 5/V.
	if cfg.WebDir != "" {
		os.MkdirAll(filepath.Join(cfg.WebDir, "pub", folder), 0o755)
		os.MkdirAll(filepath.Join(cfg.WebDir, "priv", folder), 0o755)
	}
	return seedGroupDir(cfg, folder)
}

func seedGroupDir(cfg *core.Config, folder string) error {
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	claudeDir := filepath.Join(groupDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	seedSkills(cfg, claudeDir, folder)
	// Host uid may differ from container node=1000; chown so ant can write.
	chownR(groupDir, containerUID, containerUID)
	return nil
}

const containerUID = 1000

func chownR(root string, uid, gid int) {
	filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		os.Lchown(p, uid, gid)
		return nil
	})
}

func seedSkills(cfg *core.Config, claudeDir, folder string) {
	src := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills")
	dst := filepath.Join(claudeDir, "skills")
	base := filepath.Join(claudeDir, ".merge-base", "skills")

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
		if _, err := os.Stat(filepath.Join(d, ".disabled")); err == nil {
			// Disabled by operator: don't seed; remove SKILL.md so
			// Claude Code stops indexing it, and drop the stale
			// merge-base so re-enable starts from a fresh sync.
			os.Remove(filepath.Join(d, "SKILL.md"))
			os.RemoveAll(filepath.Join(base, e.Name()))
			continue
		}
		s := filepath.Join(src, e.Name())
		// ours: preserve operator edits — only write missing files.
		cpDirFresh(s, d)
		// merge-base: full mirror of upstream — wipe then copy.
		baseDir := filepath.Join(base, e.Name())
		os.RemoveAll(baseDir)
		cpDirOverwrite(s, baseDir)
	}

	mdSrc := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "CLAUDE.md")
	mdDst := filepath.Join(claudeDir, "CLAUDE.md")
	mdBase := filepath.Join(claudeDir, ".merge-base", "CLAUDE.md")
	if data, err := os.ReadFile(mdSrc); err == nil {
		// ours: only write if missing — preserve operator edits.
		if _, err := os.Stat(mdDst); os.IsNotExist(err) {
			os.WriteFile(mdDst, data, 0o644)
		}
		// merge-base: always refresh to mirror upstream.
		os.MkdirAll(filepath.Dir(mdBase), 0o755)
		os.WriteFile(mdBase, data, 0o644)
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

// CopySession duplicates a Claude Code session jsonl from src uuid
// to dst uuid under a group's `.claude/projects/-home-node/` dir.
// Slug is fixed (`-home-node`) because every container mounts groupDir
// at $HOME=/home/node — Claude Code slugifies that path identically
// across folders. Pure file op: rename-after-write for atomicity.
// Returns nil with WARN log when src is missing (caller gets a fresh
// session, no parent context). Used by spec 6/F fork path.
func CopySession(groupDir, srcUUID, dstUUID string) error {
	projDir := filepath.Join(groupDir, ".claude", "projects", "-home-node")
	src := filepath.Join(projDir, srcUUID+".jsonl")
	dst := filepath.Join(projDir, dstUUID+".jsonl")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("CopySession: parent session file missing",
				"src", src, "groupDir", groupDir)
			return nil
		}
		return fmt.Errorf("read src: %w", err)
	}
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func cpDirOverwrite(src, dst string) { cpDirImpl(src, dst, false) }

// cpDirFresh copies src→dst, skipping files that already exist in dst.
// Used to seed operator-owned `ours` without clobbering live edits.
func cpDirFresh(src, dst string) { cpDirImpl(src, dst, true) }

func cpDirImpl(src, dst string, skipExisting bool) {
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
		// Lstat: copying symlink targets would leak arbitrary host files.
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
			cpDirImpl(sp, dp, skipExisting)
			continue
		}
		if skipExisting {
			if _, err := os.Stat(dp); err == nil {
				continue
			}
		}
		if data, err := os.ReadFile(sp); err != nil {
			slog.Warn("cpDir: read failed", "path", sp, "err", err)
		} else {
			// Unlink an existing dst before writing: a prior root-owned copy
			// (from a past sudo op) can't be truncate-opened by the uid-1000
			// daemon, but the uid-1000-owned dir lets us unlink + recreate it.
			// Without this, cpDirOverwrite silently fails on root-owned files
			// and the styles stay frozen — the staleness it was meant to cure.
			os.Remove(dp)
			if err := os.WriteFile(dp, data, 0o644); err != nil {
				slog.Warn("cpDir: write failed", "path", dp, "err", err)
			}
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
