package container

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/groupfolder"
	"github.com/onvos/arizuko/mountsec"
	"github.com/onvos/arizuko/runtime"
)

const (
	outputStartMarker = "---NANOCLAW_OUTPUT_START---"
	outputEndMarker   = "---NANOCLAW_OUTPUT_END---"
	maxOutputSize     = 10 * 1024 * 1024 // 10MB
)

var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// Input is written to container stdin as JSON.
type Input struct {
	Prompt    string            `json:"prompt"`
	SessionID string            `json:"sessionId,omitempty"`
	ChatJID   string            `json:"chatJid"`
	Folder    string            `json:"groupFolder"`
	IsTask    bool              `json:"isScheduledTask,omitempty"`
	AsstName  string            `json:"assistantName,omitempty"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	MsgCount  int               `json:"messageCount,omitempty"`
	Depth     int               `json:"delegateDepth,omitempty"`
	Channel   string            `json:"channelName,omitempty"`

	// Non-serialized fields used by the runner.
	GroupPath   string           `json:"-"`
	Name        string           `json:"-"`
	Config      core.GroupConfig `json:"-"`
	SlinkToken  string           `json:"-"`
	Annotations []string         `json:"-"`
	OnOutput    OnOutputFn       `json:"-"`
}

// Output is parsed from container stdout between marker pairs.
type Output struct {
	Status       string `json:"status"` // success|error
	Result       string `json:"result"`
	NewSessionID string `json:"newSessionId,omitempty"`
	Error        string `json:"error,omitempty"`
	HadOutput    bool   `json:"-"`
}

type OnOutputFn func(result, status string)

type VolumeMount struct {
	Host      string
	Container string
	RO        bool
}

// RunnerConfig holds all config needed by the runner.
type RunnerConfig struct {
	Image           string
	Timeout         time.Duration
	IdleTimeout     time.Duration
	Timezone        string
	HostAppDir      string
	HostProjectRoot string
	HostGroupsDir   string
	DataDir         string
	GroupsDir       string
	WebDir          string
	WebHost         string
	Name            string
	Allowlist       mountsec.Allowlist
	MediaEnabled    bool
	MediaMaxBytes   int64
	VoiceEnabled    bool
	VideoEnabled    bool
	WhisperModel    string
	WhisperURL      string
}

// Run spawns a docker container, writes input JSON to stdin, streams
// output from stdout, parses OUTPUT_START/END markers, and returns
// the final result.
func Run(cfg *RunnerConfig, in Input) Output {
	root := !strings.Contains(in.Folder, "/")
	folders := &groupfolder.Resolver{
		GroupsDir: cfg.GroupsDir,
		DataDir:   cfg.DataDir,
	}

	groupDir := in.GroupPath
	if groupDir == "" {
		groupDir, _ = folders.GroupPath(in.Folder)
	}
	os.MkdirAll(groupDir, 0o755)
	chown(groupDir, 1000, 1000)
	writeGatewayCaps(groupDir, cfg)

	mounts := BuildMounts(cfg, in, groupDir, root, folders)

	// Check migration version, annotate if behind
	appDir := cfg.HostAppDir
	latest := migrationVersion(
		filepath.Join(appDir, "container", "skills", "self", "MIGRATION_VERSION"))
	sessDir := filepath.Join(cfg.DataDir, "sessions", in.Folder, ".claude")
	agent := migrationVersion(
		filepath.Join(sessDir, "skills", "self", "MIGRATION_VERSION"))
	if agent < latest {
		in.Annotations = append(in.Annotations, fmt.Sprintf(
			"[pending migration] Skills version %d < %d. "+
				"Run /migrate (main group) to sync all groups.",
			agent, latest))
	}

	// Prepend annotations to prompt
	if len(in.Annotations) > 0 {
		in.Prompt = strings.Join(in.Annotations, "\n") +
			"\n\n" + in.Prompt
	}

	containerName := in.Name
	if containerName == "" {
		safe := safeNameRe.ReplaceAllString(in.Folder, "-")
		containerName = fmt.Sprintf(
			"arizuko-%s-%d", safe, time.Now().UnixMilli())
	}

	args := buildArgs(cfg, mounts, containerName)

	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)

	slog.Info("spawning container",
		"group", in.Folder, "container", containerName,
		"mounts", len(mounts), "root", root)
	slog.Debug("container args",
		"group", in.Folder,
		"args", strings.Join(args, " "))

	start := time.Now()

	cmd := exec.Command(runtime.Bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Output{Error: "stdin pipe: " + err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Output{Error: "stdout pipe: " + err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Output{Error: "stderr pipe: " + err.Error()}
	}

	if err := cmd.Start(); err != nil {
		return Output{Error: "start: " + err.Error()}
	}

	// Write input JSON with secrets to stdin, then close
	in.Secrets = readSecrets()
	in.AsstName = cfg.Name
	payload, _ := json.Marshal(in)
	in.Secrets = nil
	stdin.Write(payload)
	stdin.Close()

	// Drain stderr in background
	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), maxOutputSize)
		for sc.Scan() {
			line := sc.Text()
			slog.Debug("container stderr",
				"group", in.Folder, "line", line)
			stderrMu.Lock()
			if stderrBuf.Len() < maxOutputSize {
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
			}
			stderrMu.Unlock()
		}
	}()

	// Timeout: max(configTimeout, idleTimeout + 30s)
	cfgTimeout := cfg.Timeout
	if in.Config.Timeout > 0 {
		cfgTimeout = in.Config.Timeout
	}
	grace := cfg.IdleTimeout + 30*time.Second
	if cfgTimeout < grace {
		cfgTimeout = grace
	}

	var timedOut bool
	timer := time.AfterFunc(cfgTimeout, func() {
		timedOut = true
		slog.Error("container timeout, stopping gracefully",
			"group", in.Folder, "container", containerName)
		stop := exec.Command(
			runtime.Bin, runtime.StopContainerArgs(containerName)...)
		if err := stop.Run(); err != nil {
			slog.Warn("graceful stop failed, killing",
				"group", in.Folder, "container", containerName)
			cmd.Process.Kill()
		}
	})

	// Parse streaming output from stdout
	var (
		parseBuf     strings.Builder
		fullBuf      strings.Builder
		hadStreaming  bool
		newSessionID string
	)

	reader := bufio.NewReader(stdout)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])

			// Accumulate for logging (capped)
			if fullBuf.Len() < maxOutputSize {
				rem := maxOutputSize - fullBuf.Len()
				if len(chunk) > rem {
					fullBuf.WriteString(chunk[:rem])
					slog.Warn("container stdout truncated",
						"group", in.Folder, "size", fullBuf.Len())
				} else {
					fullBuf.WriteString(chunk)
				}
			}

			// Stream-parse for output markers
			parseBuf.WriteString(chunk)
			for {
				s := parseBuf.String()
				si := strings.Index(s, outputStartMarker)
				if si == -1 {
					break
				}
				ei := strings.Index(s[si:], outputEndMarker)
				if ei == -1 {
					break // incomplete pair, wait for more
				}
				ei += si

				js := strings.TrimSpace(
					s[si+len(outputStartMarker) : ei])
				rest := s[ei+len(outputEndMarker):]
				parseBuf.Reset()
				parseBuf.WriteString(rest)

				var parsed struct {
					Status    string `json:"status"`
					Result    string `json:"result"`
					SessionID string `json:"newSessionId"`
					Error     string `json:"error"`
				}
				if err := json.Unmarshal(
					[]byte(js), &parsed,
				); err != nil {
					slog.Warn("failed to parse streamed output",
						"group", in.Folder, "err", err)
					continue
				}

				if parsed.SessionID != "" {
					newSessionID = parsed.SessionID
				}
				hadStreaming = true

				// Reset timeout on activity
				timer.Reset(cfgTimeout)

				if in.OnOutput != nil {
					in.OnOutput(parsed.Result, parsed.Status)
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	exitErr := cmd.Wait()
	timer.Stop()
	elapsed := time.Since(start)

	code := 0
	if exitErr != nil {
		if ee, ok := exitErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}

	// Write log file
	stderrMu.Lock()
	stderrStr := stderrBuf.String()
	stderrMu.Unlock()

	ts := time.Now().Format("2006-01-02T15-04-05")
	logFile := filepath.Join(logsDir, "container-"+ts+".log")
	writeLog(logFile, in, containerName, elapsed, code,
		timedOut, hadStreaming, fullBuf.String(), stderrStr, mounts)

	slog.Info("container exited",
		"group", in.Folder, "code", code,
		"duration", elapsed,
		"timedOut", timedOut, "hadOutput", hadStreaming)

	// Handle timeout
	if timedOut {
		if hadStreaming {
			slog.Info(
				"container timed out after output (idle cleanup)",
				"group", in.Folder, "container", containerName,
				"duration", elapsed)
			return Output{
				Status:       "success",
				NewSessionID: newSessionID,
				HadOutput:    true,
			}
		}
		slog.Error("container timed out with no output",
			"group", in.Folder, "container", containerName,
			"duration", elapsed)
		return Output{
			Status: "error",
			Error: fmt.Sprintf(
				"Container timed out after %s", cfgTimeout),
		}
	}

	// Handle non-zero exit
	if code != 0 {
		slog.Error("container exited with error",
			"group", in.Folder, "code", code,
			"duration", elapsed, "logFile", logFile)
		tail := stderrStr
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return Output{
			Status:       "error",
			NewSessionID: newSessionID,
			HadOutput:    hadStreaming,
			Error: fmt.Sprintf(
				"Container exited with code %d: %s", code, tail),
		}
	}

	// Streaming mode: already delivered via OnOutput
	if hadStreaming {
		slog.Info("container completed (streaming mode)",
			"group", in.Folder, "duration", elapsed,
			"newSessionId", newSessionID)
		return Output{
			Status:       "success",
			NewSessionID: newSessionID,
			HadOutput:    true,
		}
	}

	// Legacy mode: parse last marker pair from accumulated stdout
	allStdout := fullBuf.String()
	si := strings.LastIndex(allStdout, outputStartMarker)
	ei := strings.LastIndex(allStdout, outputEndMarker)
	if si != -1 && ei > si {
		js := strings.TrimSpace(
			allStdout[si+len(outputStartMarker) : ei])
		var out Output
		if json.Unmarshal([]byte(js), &out) == nil {
			slog.Info("container completed",
				"group", in.Folder, "duration", elapsed,
				"status", out.Status,
				"hasResult", out.Result != "")
			return out
		}
	}

	// Fallback: last non-empty line
	lines := strings.Split(strings.TrimSpace(allStdout), "\n")
	if len(lines) > 0 {
		var out Output
		if json.Unmarshal([]byte(lines[len(lines)-1]), &out) == nil {
			return out
		}
	}

	return Output{
		Status: "error",
		Error:  "no parseable output from container",
	}
}

// BuildMounts assembles the volume mounts for a container run.
func BuildMounts(
	cfg *RunnerConfig, in Input,
	groupDir string, root bool,
	folders *groupfolder.Resolver,
) []VolumeMount {
	var m []VolumeMount

	// Group working directory
	m = append(m, VolumeMount{
		Host:      hp(cfg, groupDir),
		Container: "/workspace/group",
	})

	// Media dir
	media := filepath.Join(groupDir, "media")
	os.MkdirAll(media, 0o755)
	m = append(m, VolumeMount{
		Host:      hp(cfg, media),
		Container: "/workspace/media",
	})

	// App source read-only
	m = append(m, VolumeMount{
		Host:      cfg.HostAppDir,
		Container: "/workspace/self",
		RO:        true,
	})

	// Shared dir per world (first segment of folder)
	world := strings.SplitN(in.Folder, "/", 2)[0]
	share := filepath.Join(cfg.GroupsDir, world, "share")
	os.MkdirAll(share, 0o755)
	m = append(m, VolumeMount{
		Host:      hp(cfg, share),
		Container: "/workspace/share",
		RO:        !root,
	})

	// Claude sessions dir (.claude)
	sessDir := filepath.Join(
		cfg.DataDir, "sessions", in.Folder, ".claude")
	os.MkdirAll(sessDir, 0o755)
	chown(sessDir, 1000, 1000)
	seedSettings(sessDir, cfg, in, root)
	seedSkills(cfg, sessDir)
	m = append(m, VolumeMount{
		Host:      hp(cfg, sessDir),
		Container: "/home/node/.claude",
	})

	// IPC dir
	ipcDir, err := folders.IpcPath(in.Folder)
	if err == nil {
		for _, sub := range []string{
			"messages", "tasks", "input", "requests", "replies", "sidecars",
		} {
			os.MkdirAll(filepath.Join(ipcDir, sub), 0o755)
		}
		chown(ipcDir, 1000, 1000)
		m = append(m, VolumeMount{
			Host:      hp(cfg, ipcDir),
			Container: "/workspace/ipc",
		})
	}

	// Agent runner source — seed once, mount read-write
	runnerSrc := filepath.Join(
		cfg.HostAppDir, "container", "agent-runner", "src")
	groupRunnerDir := filepath.Join(
		cfg.DataDir, "sessions", in.Folder, "agent-runner-src")
	if _, err := os.Stat(groupRunnerDir); os.IsNotExist(err) {
		if _, err := os.Stat(runnerSrc); err == nil {
			cpDir(runnerSrc, groupRunnerDir)
			chown(groupRunnerDir, 1000, 1000)
		}
	}
	m = append(m, VolumeMount{
		Host:      cfg.HostAppDir + "/container/agent-runner/src",
		Container: "/app/src",
	})

	// Additional mounts from group config
	if len(in.Config.Mounts) > 0 {
		var add []mountsec.AdditionalMount
		for _, cm := range in.Config.Mounts {
			ro := cm.RO
			add = append(add, mountsec.AdditionalMount{
				HostPath:      cm.Host,
				ContainerPath: cm.Container,
				Readonly:      &ro,
			})
		}
		for _, v := range mountsec.ValidateAdditionalMounts(
			add, in.Folder, root, cfg.Allowlist,
		) {
			m = append(m, VolumeMount{
				Host:      v.HostPath,
				Container: v.ContainerPath,
				RO:        v.Readonly,
			})
		}
	}

	// Web dir
	if fi, err := os.Stat(cfg.WebDir); err == nil && fi.IsDir() {
		chown(cfg.WebDir, 1000, 1000)
		m = append(m, VolumeMount{
			Host:      hp(cfg, cfg.WebDir),
			Container: "/workspace/web",
		})
	}

	// Root group gets sessions/ rw for migrate skill
	if root {
		sd := filepath.Join(cfg.DataDir, "sessions")
		os.MkdirAll(sd, 0o755)
		m = append(m, VolumeMount{
			Host:      hp(cfg, sd),
			Container: "/workspace/data/sessions",
		})
	}

	return m
}

func buildArgs(
	cfg *RunnerConfig, mounts []VolumeMount, name string,
) []string {
	args := []string{
		"run", "-i", "--rm",
		"--name", name,
		"--shm-size=1g",
		"-e", "TZ=" + cfg.Timezone,
	}

	uid := os.Getuid()
	gid := os.Getgid()
	if uid > 0 && uid != 1000 {
		args = append(args,
			"--user", fmt.Sprintf("%d:%d", uid, gid),
			"-e", "HOME=/home/node")
	}

	for _, m := range mounts {
		if m.RO {
			args = append(args,
				runtime.ReadonlyMountArgs(m.Host, m.Container)...)
		} else {
			args = append(args,
				"-v", m.Host+":"+m.Container)
		}
	}

	args = append(args, cfg.Image)
	return args
}

// hp translates a local path to a host-side path for
// docker-in-docker scenarios.
func hp(cfg *RunnerConfig, local string) string {
	if cfg.HostProjectRoot == "" {
		return local
	}
	projRoot := filepath.Dir(cfg.DataDir)
	if !strings.HasPrefix(local, projRoot) {
		return local
	}
	rel, _ := filepath.Rel(projRoot, local)
	return filepath.Join(cfg.HostProjectRoot, rel)
}

func readSecrets() map[string]string {
	s := make(map[string]string)
	for _, k := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY",
	} {
		if v := os.Getenv(k); v != "" {
			s[k] = v
		}
	}
	return s
}

// seedSettings creates or updates settings.json, injecting env vars
// that change per spawn.
func seedSettings(
	claudeDir string, cfg *RunnerConfig,
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

	// Always update per-spawn values
	env["WEB_HOST"] = cfg.WebHost
	env["NANOCLAW_ASSISTANT_NAME"] = cfg.Name
	env["NANOCLAW_IS_ROOT"] = ""
	if root {
		env["NANOCLAW_IS_ROOT"] = "1"
	}
	env["NANOCLAW_DELEGATE_DEPTH"] = fmt.Sprintf("%d", in.Depth)
	if in.Channel != "" {
		settings["outputStyle"] = in.Channel
	}
	if in.SlinkToken != "" {
		env["SLINK_TOKEN"] = in.SlinkToken
	}
	settings["env"] = env

	data, _ := json.MarshalIndent(settings, "", "  ")
	os.WriteFile(fp, append(data, '\n'), 0o644)
}

// seedSkills copies skill directories from container/skills/ into
// the group's .claude/skills/. Only seeds if not already present.
func seedSkills(cfg *RunnerConfig, claudeDir string) {
	src := filepath.Join(cfg.HostAppDir, "container", "skills")
	dst := filepath.Join(claudeDir, "skills")

	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}

	nameRe := regexp.MustCompile(`^[a-z0-9-]+$`)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !nameRe.MatchString(e.Name()) {
			slog.Warn("skipping skill with invalid name",
				"name", e.Name())
			continue
		}
		d := filepath.Join(dst, e.Name())
		if _, err := os.Stat(d); err == nil {
			continue
		}
		cpDir(filepath.Join(src, e.Name()), d)
	}
	chown(dst, 1000, 1000)

	mdSrc := filepath.Join(cfg.HostAppDir, "container", "CLAUDE.md")
	mdDst := filepath.Join(claudeDir, "CLAUDE.md")
	if _, err := os.Stat(mdDst); os.IsNotExist(err) {
		if data, err := os.ReadFile(mdSrc); err == nil {
			os.WriteFile(mdDst, data, 0o644)
		}
	}
}

func writeGatewayCaps(groupDir string, cfg *RunnerConfig) {
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

func migrationVersion(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var v int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &v)
	return v
}

func cpDir(src, dst string) {
	os.MkdirAll(dst, 0o755)
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		if e.IsDir() {
			cpDir(sp, dp)
		} else if data, err := os.ReadFile(sp); err == nil {
			os.WriteFile(dp, data, 0o644)
		}
	}
}

func chown(dir string, uid, gid int) {
	filepath.WalkDir(dir,
		func(p string, _ os.DirEntry, err error) error {
			if err == nil {
				os.Chown(p, uid, gid)
			}
			return nil
		})
}

func writeLog(
	path string, in Input,
	cname string, dur time.Duration,
	code int, timedOut, hadOutput bool,
	stdout, stderr string,
	mounts []VolumeMount,
) {
	var b strings.Builder
	if timedOut {
		fmt.Fprintf(&b, "=== Container Run Log (TIMEOUT) ===\n")
	} else {
		fmt.Fprintf(&b, "=== Container Run Log ===\n")
	}
	fmt.Fprintf(&b, "Timestamp: %s\n",
		time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "Group: %s\n", in.Folder)
	fmt.Fprintf(&b, "Container: %s\n", cname)
	fmt.Fprintf(&b, "Duration: %s\n", dur)
	fmt.Fprintf(&b, "Exit Code: %d\n", code)
	if timedOut {
		fmt.Fprintf(&b, "Had Streaming Output: %v\n", hadOutput)
	}

	isErr := code != 0 || timedOut
	lvl := os.Getenv("LOG_LEVEL")
	verbose := lvl == "debug" || lvl == "trace"

	if verbose || isErr {
		fmt.Fprintf(&b, "\n=== Input ===\n")
		ij, _ := json.MarshalIndent(in, "", "  ")
		b.Write(ij)
		fmt.Fprintf(&b, "\n\n=== Mounts ===\n")
		for _, m := range mounts {
			ro := ""
			if m.RO {
				ro = " (ro)"
			}
			fmt.Fprintf(&b, "%s -> %s%s\n",
				m.Host, m.Container, ro)
		}
		fmt.Fprintf(&b, "\n=== Stderr ===\n%s\n", stderr)
		fmt.Fprintf(&b, "\n=== Stdout ===\n%s\n", stdout)
	} else {
		fmt.Fprintf(&b, "\n=== Input Summary ===\n")
		fmt.Fprintf(&b, "Prompt length: %d chars\n",
			len(in.Prompt))
		sid := in.SessionID
		if sid == "" {
			sid = "new"
		}
		fmt.Fprintf(&b, "Session ID: %s\n", sid)
		fmt.Fprintf(&b, "\n=== Mounts ===\n")
		for _, m := range mounts {
			ro := ""
			if m.RO {
				ro = " (ro)"
			}
			fmt.Fprintf(&b, "%s%s\n", m.Container, ro)
		}
	}

	os.WriteFile(path, []byte(b.String()), 0o644)
	slog.Debug("container log written",
		"logFile", path, "verbose", verbose)
}

// WriteTasksSnapshot writes a tasks JSON file into the IPC dir
// for the container to read.
func WriteTasksSnapshot(
	folders *groupfolder.Resolver,
	folder string, isRoot bool,
	tasks []core.Task,
) {
	ipcDir, err := folders.IpcPath(folder)
	if err != nil {
		slog.Warn("cannot write tasks snapshot",
			"folder", folder, "err", err)
		return
	}
	os.MkdirAll(ipcDir, 0o755)

	filtered := tasks
	if !isRoot {
		var f []core.Task
		for _, t := range tasks {
			if t.Group == folder {
				f = append(f, t)
			}
		}
		filtered = f
	}

	data, _ := json.MarshalIndent(filtered, "", "  ")
	p := filepath.Join(ipcDir, "current_tasks.json")
	os.WriteFile(p, data, 0o644)
}

// WriteGroupsSnapshot writes available groups JSON into the IPC dir
// for the container to read.
func WriteGroupsSnapshot(
	folders *groupfolder.Resolver,
	folder string, isRoot bool,
	groups []core.Group,
) {
	ipcDir, err := folders.IpcPath(folder)
	if err != nil {
		slog.Warn("cannot write groups snapshot",
			"folder", folder, "err", err)
		return
	}
	os.MkdirAll(ipcDir, 0o755)

	visible := groups
	if !isRoot {
		visible = nil
	}

	snap := struct {
		Groups   []core.Group `json:"groups"`
		LastSync string       `json:"lastSync"`
	}{
		Groups:   visible,
		LastSync: time.Now().Format(time.RFC3339),
	}

	data, _ := json.MarshalIndent(snap, "", "  ")
	p := filepath.Join(ipcDir, "available_groups.json")
	os.WriteFile(p, data, 0o644)
}
