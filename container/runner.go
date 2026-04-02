package container

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/diary"
	"github.com/onvos/arizuko/grants"
	"github.com/onvos/arizuko/groupfolder"
	"github.com/onvos/arizuko/ipc"
	"github.com/onvos/arizuko/mountsec"
	"github.com/onvos/arizuko/router"
)

const (
	outputStartMarker = "---ARIZUKO_OUTPUT_START---"
	outputEndMarker   = "---ARIZUKO_OUTPUT_END---"
	maxOutputSize     = 10 * 1024 * 1024 // 10MB
	containerHome     = "/home/node"
)

var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9-]`)

func SanitizeFolder(folder string) string {
	s := strings.ReplaceAll(folder, "/", "-")
	s = safeNameRe.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
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
	Config      core.GroupConfig `json:"-"`
	SlinkToken  string           `json:"-"`
	Annotations []string         `json:"-"`
	OnOutput    OnOutputFn       `json:"-"`
	GatedFns    ipc.GatedFns     `json:"-"`
	StoreFns    ipc.StoreFns     `json:"-"`
}

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

func Run(cfg *core.Config, folders *groupfolder.Resolver, in Input) Output {
	root := !strings.Contains(in.Folder, "/")

	groupDir := in.GroupPath
	if groupDir == "" {
		groupDir, _ = folders.GroupPath(in.Folder)
	}
	os.MkdirAll(groupDir, 0o755)
	chown(groupDir, 1000, 1000)
	writeGatewayCaps(groupDir, cfg)

	mounts := BuildMounts(cfg, in, groupDir, root, folders)

	appDir := cfg.HostAppDir
	latest := migrationVersion(
		filepath.Join(appDir, "ant", "skills", "self", "MIGRATION_VERSION"))
	agent := migrationVersion(
		filepath.Join(groupDir, ".claude", "skills", "self", "MIGRATION_VERSION"))
	if agent < latest {
		in.Annotations = append(in.Annotations, fmt.Sprintf(
			"[pending migration] Skills version %d < %d. "+
				"Run /migrate (main group) to sync all groups.",
			agent, latest))
	}

	if in.Topic != "" {
		in.Annotations = append(in.Annotations,
			"Topic session: "+in.Topic)
	}

	if ep := ReadRecentEpisodes(groupDir); ep != "" {
		in.Annotations = append(in.Annotations, ep)
	}

	if d := diary.Read(groupDir, 14); d != "" {
		in.Annotations = append(in.Annotations, d)
	}

	if uc := router.UserContextXml(in.Sender, groupDir); uc != "" {
		in.Annotations = append(in.Annotations, uc)
	}

	in.Soul = readOptional(filepath.Join(groupDir, "SOUL.md"))
	in.SystemMd = readOptional(filepath.Join(groupDir, "SYSTEM.md"))

	if len(in.Annotations) > 0 {
		in.Prompt = strings.Join(in.Annotations, "\n") +
			"\n\n" + in.Prompt
	}

	var sidecarNames []string
	if len(in.Config.Sidecars) > 0 {
		ipcDir, _ := folders.IpcPath(in.Folder)
		sidecarNames = StartSidecars(
			cfg, in.Folder, in.Config.Sidecars, ipcDir)
	}

	containerName := in.Name
	if containerName == "" {
		safe := safeNameRe.ReplaceAllString(in.Folder, "-")
		containerName = fmt.Sprintf(
			"arizuko-%s-%s-%d", cfg.Name, safe, time.Now().UnixMilli())
	}

	args := buildArgs(cfg, mounts, containerName)

	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)

	slog.Info("spawning container",
		"group", in.Folder, "container", containerName,
		"mounts", len(mounts), "root", root, "session", in.SessionID != "")
	slog.Debug("container args",
		"group", in.Folder,
		"args", strings.Join(args, " "))

	start := time.Now()

	cmd := exec.Command(Bin, args...)
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

	var stopMCP func()
	if ipcDir, err := folders.IpcPath(in.Folder); err == nil {
		sockPath := filepath.Join(ipcDir, "gated.sock")
		if stop, err := ipc.ServeMCP(sockPath, in.GatedFns, in.StoreFns, in.Folder, in.Grants); err != nil {
			slog.Warn("failed to start MCP server", "group", in.Folder, "err", err)
		} else {
			stopMCP = stop
		}
	}

	if err := cmd.Start(); err != nil {
		if stopMCP != nil {
			stopMCP()
		}
		return Output{Error: "start: " + err.Error()}
	}

	in.Secrets = readSecrets()
	in.AsstName = cfg.Name
	payload, _ := json.Marshal(in)
	in.Secrets = nil
	if _, err := stdin.Write(payload); err != nil {
		slog.Error("stdin write failed", "group", in.Folder, "err", err)
	}
	stdin.Close()

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

	cfgTimeout := cfg.Timeout
	if in.Config.Timeout > 0 {
		cfgTimeout = in.Config.Timeout
	}
	grace := cfg.IdleTimeout + 30*time.Second
	if cfgTimeout < grace {
		cfgTimeout = grace
	}

	var timedOut atomic.Bool
	timer := time.AfterFunc(cfgTimeout, func() {
		timedOut.Store(true)
		slog.Info("container timeout, stopping",
			"group", in.Folder, "container", containerName)
		stop := exec.Command(
			Bin, StopContainerArgs(containerName)...)
		if err := stop.Run(); err != nil {
			slog.Warn("graceful stop failed, killing",
				"group", in.Folder, "container", containerName)
			cmd.Process.Kill()
		}
	})

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

			if parseBuf.Len() < maxOutputSize {
				parseBuf.WriteString(chunk)
			}
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

				hadStreaming = true
				if parsed.SessionID != "" {
					newSessionID = parsed.SessionID
				}
				timer.Reset(cfg.IdleTimeout)

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

	if stopMCP != nil {
		stopMCP()
	}
	if len(sidecarNames) > 0 {
		StopSidecars(sidecarNames)
	}

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
	writeLog(logFile, in, containerName, elapsed, code,
		to, hadStreaming, fullBuf.String(), stderrStr, mounts)

	slog.Info("container exited",
		"group", in.Folder, "code", code,
		"duration", elapsed,
		"timedOut", to, "hadOutput", hadStreaming)

	if to {
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

func BuildMounts(
	cfg *core.Config, in Input,
	groupDir string, root bool,
	folders *groupfolder.Resolver,
) []VolumeMount {
	var m []VolumeMount

	m = append(m, VolumeMount{
		Host:      hp(cfg, groupDir),
		Container: containerHome,
	})
	media := filepath.Join(groupDir, "media")
	os.MkdirAll(media, 0o755)

	m = append(m, VolumeMount{
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
		m = append(m, VolumeMount{
			Host:      hp(cfg, share),
			Container: "/workspace/share",
			RO:        !shareRw,
		})
	}

	claudeDir := filepath.Join(groupDir, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	chown(claudeDir, 1000, 1000)
	seedSettings(claudeDir, cfg, in, root)

	ipcDir, err := folders.IpcPath(in.Folder)
	if err == nil {
		for _, sub := range []string{"input", "sidecars"} {
			os.MkdirAll(filepath.Join(ipcDir, sub), 0o755)
		}
		chown(ipcDir, 1000, 1000)
		m = append(m, VolumeMount{
			Host:      hp(cfg, ipcDir),
			Container: "/workspace/ipc",
		})
	}

	if os.Getenv("ARIZUKO_DEV") == "1" {
		runnerSrc := filepath.Join(cfg.HostAppDir, "ant", "src")
		if _, err := os.Stat(runnerSrc); err == nil {
			m = append(m, VolumeMount{
				Host:      hp(cfg, runnerSrc),
				Container: "/app/src",
			})
		}
	}

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
			add, in.Folder, root, mountsec.Allowlist{},
		) {
			m = append(m, VolumeMount{
				Host:      v.HostPath,
				Container: v.ContainerPath,
				RO:        v.Readonly,
			})
		}
	}

	if fi, err := os.Stat(cfg.WebDir); err == nil && fi.IsDir() {
		tier := strings.Count(in.Folder, "/")
		if tier > 2 {
			// tier 3+: no web mount
		} else if root {
			chown(cfg.WebDir, 1000, 1000)
			m = append(m, VolumeMount{
				Host:      hp(cfg, cfg.WebDir),
				Container: "/workspace/web",
			})
		} else {
			worldDir := filepath.Join(cfg.WebDir, world)
			os.MkdirAll(worldDir, 0o755)
			chown(worldDir, 1000, 1000)
			m = append(m, VolumeMount{
				Host:      hp(cfg, worldDir),
				Container: "/workspace/web",
			})
		}
	}

	if root {
		m = append(m, VolumeMount{
			Host:      hp(cfg, cfg.GroupsDir),
			Container: "/workspace/data/groups",
		})
	}

	return m
}

func buildArgs(
	cfg *core.Config, mounts []VolumeMount, name string,
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
			"-e", "HOME="+containerHome)
	}

	for _, m := range mounts {
		if m.RO {
			args = append(args,
				ReadonlyMountArgs(m.Host, m.Container)...)
		} else {
			args = append(args,
				"-v", m.Host+":"+m.Container)
		}
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
	}
	env["ARIZUKO_DELEGATE_DEPTH"] = fmt.Sprintf("%d", in.Depth)
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

	if len(in.Config.Sidecars) > 0 {
		managed, _ := settings["_managedSidecars"].([]any)
		var allowed []any
		if a, ok := settings["allowedTools"].([]any); ok {
			allowed = a
		}

		for name, spec := range in.Config.Sidecars {
			servers[name] = map[string]any{
				"command": "socat",
				"args": []string{
					"UNIX-CONNECT:/workspace/ipc/sidecars/" +
						name + ".sock",
					"STDIO",
				},
			}
			managed = append(managed, name)

			for _, tool := range spec.Tools {
				if tool == "*" {
					allowed = append(allowed,
						"mcp__"+name+"__*")
				} else {
					allowed = append(allowed,
						"mcp__"+name+"__"+tool)
				}
			}
		}

		settings["_managedSidecars"] = managed
		if len(allowed) > 0 {
			settings["allowedTools"] = allowed
		}
	}

	data, _ := json.MarshalIndent(settings, "", "  ")
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		slog.Warn("failed to write settings tmp", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, fp); err != nil {
		slog.Warn("failed to rename settings", "from", tmp, "to", fp, "err", err)
	}
	slog.Debug("settings seeded", "path", fp, "sidecars", len(in.Config.Sidecars))
}

// SeedGroupDir initializes the agent session directory for a newly created group.
// Called once at group creation time by group add, onbod, and register_group MCP tool.
func SeedGroupDir(cfg *core.Config, folder string) error {
	claudeDir := filepath.Join(cfg.GroupsDir, folder, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	chown(claudeDir, 1000, 1000)
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

	mdSrc := filepath.Join(cfg.HostAppDir, "ant", "CLAUDE.md")
	mdDst := filepath.Join(claudeDir, "CLAUDE.md")
	if _, err := os.Stat(mdDst); os.IsNotExist(err) {
		if data, err := os.ReadFile(mdSrc); err == nil {
			os.WriteFile(mdDst, data, 0o644)
		}
	}

	// Seed .claude.json if missing — SDK silently returns 0 messages without it.
	jsonDst := filepath.Join(claudeDir, ".claude.json")
	if _, err := os.Stat(jsonDst); os.IsNotExist(err) {
		userID := fmt.Sprintf("%x", sha256.Sum256([]byte("arizuko:"+folder)))
		data, _ := json.MarshalIndent(map[string]any{
			"firstStartTime":            time.Now().Format(time.RFC3339),
			"userID":                    userID,
			"thinkingMigrationComplete": true,
			"sonnet45MigrationComplete": true,
		}, "", "  ")
		os.WriteFile(jsonDst, append(data, '\n'), 0o644)
		chown(jsonDst, 1000, 1000)
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
		if e.IsDir() {
			cpDir(sp, dp)
		} else if data, err := os.ReadFile(sp); err != nil {
			slog.Warn("cpDir: read failed", "path", sp, "err", err)
		} else if err := os.WriteFile(dp, data, 0o644); err != nil {
			slog.Warn("cpDir: write failed", "path", dp, "err", err)
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
	if timedOut {
		fmt.Fprintf(&b, "Had Streaming Output: %v\n", hadOutput)
	}

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
		fmt.Fprintf(&b, "\n=== Stdout ===\n%s\n", stdout)
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
