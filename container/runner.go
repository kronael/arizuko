package container

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	maxOutputSize     = 10 * 1024 * 1024
)

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
}

type Input struct {
	Prompt    string            `json:"prompt"`
	SessionID string            `json:"sessionId,omitempty"`
	ChatJID   string            `json:"chatJid"`
	Folder    string            `json:"groupFolder"`
	GroupPath string            `json:"-"`
	Name      string            `json:"-"`
	Config    core.GroupConfig  `json:"-"`
	OnOutput  func(string, string) `json:"-"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	MsgCount  int               `json:"messageCount,omitempty"`
	Depth     int               `json:"delegateDepth,omitempty"`
	IsTask    bool              `json:"isScheduledTask,omitempty"`
	AsstName  string            `json:"assistantName,omitempty"`
}

type Output struct {
	Text         string `json:"result"`
	NewSessionID string `json:"newSessionId,omitempty"`
	Error        string `json:"error,omitempty"`
	HadOutput    bool   `json:"-"`
}

type mount struct {
	host      string
	container string
	ro        bool
}

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

	mounts := buildMounts(cfg, in, groupDir, root, folders)
	containerName := in.Name

	args := buildArgs(cfg, mounts, containerName)

	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)

	slog.Info("spawning container",
		"group", in.Folder, "container", containerName,
		"mounts", len(mounts))

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

	// Write input JSON to stdin
	in.Secrets = readSecrets()
	in.AsstName = cfg.Name
	payload, _ := json.Marshal(in)
	in.Secrets = nil
	stdin.Write(payload)
	stdin.Close()

	// Drain stderr
	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 256*1024)
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

	// Timeout management
	configTimeout := cfg.Timeout
	if in.Config.Timeout > 0 {
		configTimeout = in.Config.Timeout
	}
	grace := cfg.IdleTimeout + 30*time.Second
	if configTimeout < grace {
		configTimeout = grace
	}

	var timedOut bool
	timer := time.AfterFunc(configTimeout, func() {
		timedOut = true
		slog.Error("container timeout",
			"group", in.Folder, "container", containerName)
		exec.Command(runtime.Bin,
			runtime.StopContainerArgs(containerName)...).Run()
	})

	// Parse streaming output
	var (
		parseBuf     strings.Builder
		hadStreaming  bool
		newSessionID string
	)

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 256*1024), maxOutputSize)
	for sc.Scan() {
		line := sc.Text()
		parseBuf.WriteString(line)
		parseBuf.WriteByte('\n')

		buf := parseBuf.String()
		for {
			si := strings.Index(buf, outputStartMarker)
			if si == -1 {
				break
			}
			ei := strings.Index(buf[si:], outputEndMarker)
			if ei == -1 {
				break
			}
			ei += si

			js := strings.TrimSpace(
				buf[si+len(outputStartMarker) : ei])
			buf = buf[ei+len(outputEndMarker):]
			parseBuf.Reset()
			parseBuf.WriteString(buf)

			var out struct {
				Status    string `json:"status"`
				Result    string `json:"result"`
				SessionID string `json:"newSessionId"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal([]byte(js), &out); err != nil {
				slog.Warn("failed to parse output",
					"group", in.Folder, "err", err)
				continue
			}

			if out.SessionID != "" {
				newSessionID = out.SessionID
			}
			hadStreaming = true
			timer.Reset(configTimeout)

			if in.OnOutput != nil {
				in.OnOutput(out.Result, out.Status)
			}
		}
	}

	err = cmd.Wait()
	timer.Stop()

	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}

	slog.Info("container exited",
		"group", in.Folder, "code", code,
		"timedOut", timedOut, "hadOutput", hadStreaming)

	if timedOut {
		if hadStreaming {
			return Output{
				NewSessionID: newSessionID,
				HadOutput:    true,
			}
		}
		return Output{
			Error: fmt.Sprintf(
				"container timed out after %s", configTimeout),
		}
	}

	if code != 0 {
		stderrMu.Lock()
		tail := stderrBuf.String()
		stderrMu.Unlock()
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return Output{
			NewSessionID: newSessionID,
			HadOutput:    hadStreaming,
			Error: fmt.Sprintf(
				"container exited %d: %s", code, tail),
		}
	}

	if hadStreaming {
		return Output{
			NewSessionID: newSessionID,
			HadOutput:    true,
		}
	}

	// Legacy: parse from accumulated stdout
	buf := parseBuf.String()
	si := strings.LastIndex(buf, outputStartMarker)
	ei := strings.LastIndex(buf, outputEndMarker)
	if si != -1 && ei > si {
		js := strings.TrimSpace(
			buf[si+len(outputStartMarker) : ei])
		var out Output
		if json.Unmarshal([]byte(js), &out) == nil {
			return out
		}
	}

	return Output{Error: "no parseable output from container"}
}

func buildMounts(
	cfg *RunnerConfig, in Input,
	groupDir string, root bool,
	folders *groupfolder.Resolver,
) []mount {
	var m []mount

	// Group folder
	m = append(m, mount{
		host:      hp(cfg, groupDir),
		container: "/workspace/group",
	})

	// Media
	media := filepath.Join(groupDir, "media")
	os.MkdirAll(media, 0o755)
	m = append(m, mount{
		host:      hp(cfg, media),
		container: "/workspace/media",
	})

	// App source
	m = append(m, mount{
		host:      cfg.HostAppDir,
		container: "/workspace/self",
		ro:        true,
	})

	// Shared dir
	world := strings.SplitN(in.Folder, "/", 2)[0]
	share := filepath.Join(cfg.GroupsDir, world, "share")
	os.MkdirAll(share, 0o755)
	m = append(m, mount{
		host:      hp(cfg, share),
		container: "/workspace/share",
		ro:        !root,
	})

	// Claude sessions
	sess := filepath.Join(
		cfg.DataDir, "sessions", in.Folder, ".claude")
	os.MkdirAll(sess, 0o755)
	chown(sess, 1000, 1000)
	seedSettings(sess, cfg, in, root)
	seedSkills(cfg, sess)
	m = append(m, mount{
		host:      hp(cfg, sess),
		container: "/home/node/.claude",
	})

	// IPC dir
	ipcDir, _ := folders.IpcPath(in.Folder)
	for _, sub := range []string{
		"messages", "tasks", "input", "requests", "replies",
	} {
		os.MkdirAll(filepath.Join(ipcDir, sub), 0o755)
	}
	chown(ipcDir, 1000, 1000)
	m = append(m, mount{
		host:      hp(cfg, ipcDir),
		container: "/workspace/ipc",
	})

	// Agent runner source
	m = append(m, mount{
		host:      cfg.HostAppDir + "/container/agent-runner/src",
		container: "/app/src",
	})

	// Additional mounts
	if len(in.Config.Mounts) > 0 {
		var add []mountsec.AdditionalMount
		for _, cm := range in.Config.Mounts {
			add = append(add, mountsec.AdditionalMount{
				HostPath:      cm.Host,
				ContainerPath: cm.Container,
			})
		}
		for _, v := range mountsec.ValidateAdditionalMounts(
			add, in.Folder, root, cfg.Allowlist,
		) {
			m = append(m, mount{
				host:      v.HostPath,
				container: v.ContainerPath,
				ro:        v.Readonly,
			})
		}
	}

	// Web dir
	if fi, err := os.Stat(cfg.WebDir); err == nil && fi.IsDir() {
		chown(cfg.WebDir, 1000, 1000)
		m = append(m, mount{
			host:      hp(cfg, cfg.WebDir),
			container: "/workspace/web",
		})
	}

	// Root: sessions dir for migrate
	if root {
		sd := filepath.Join(cfg.DataDir, "sessions")
		os.MkdirAll(sd, 0o755)
		m = append(m, mount{
			host:      hp(cfg, sd),
			container: "/workspace/data/sessions",
		})
	}

	return m
}

func buildArgs(cfg *RunnerConfig, mounts []mount, name string) []string {
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
		if m.ro {
			args = append(args,
				runtime.ReadonlyMountArgs(m.host, m.container)...)
		} else {
			args = append(args,
				"-v", m.host+":"+m.container)
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
	// DataDir parent is the project root
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

func seedSettings(
	sessDir string, cfg *RunnerConfig,
	in Input, root bool,
) {
	fp := filepath.Join(sessDir, "settings.json")
	var settings map[string]any
	if data, err := os.ReadFile(fp); err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}

	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	env["CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD"] = "1"
	env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "0"
	env["WEB_HOST"] = cfg.WebHost
	env["NANOCLAW_ASSISTANT_NAME"] = cfg.Name
	env["NANOCLAW_IS_ROOT"] = ""
	if root {
		env["NANOCLAW_IS_ROOT"] = "1"
	}
	env["NANOCLAW_DELEGATE_DEPTH"] = fmt.Sprintf("%d", in.Depth)
	settings["env"] = env

	data, _ := json.MarshalIndent(settings, "", "  ")
	os.WriteFile(fp, append(data, '\n'), 0o644)
}

func seedSkills(cfg *RunnerConfig, sessDir string) {
	src := filepath.Join(cfg.HostAppDir, "container", "skills")
	dst := filepath.Join(sessDir, "skills")
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
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
	mdDst := filepath.Join(sessDir, "CLAUDE.md")
	if _, err := os.Stat(mdDst); os.IsNotExist(err) {
		if data, err := os.ReadFile(mdSrc); err == nil {
			os.WriteFile(mdDst, data, 0o644)
		}
	}
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
	filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err == nil {
			os.Chown(p, uid, gid)
		}
		return nil
	})
}

func WriteTasksSnapshot(
	groupPath string, tasks []core.Task,
) {
	p := filepath.Join(groupPath, ".state", "tasks.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(tasks)
	os.WriteFile(p, b, 0o644)
}

func WriteGroupsSnapshot(
	groupPath string, groups []core.Group,
) {
	p := filepath.Join(groupPath, ".state", "groups.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(groups)
	os.WriteFile(p, b, 0o644)
}
