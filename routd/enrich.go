package routd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// mediaConfig is routd's slice of the media/transcription env. Audio AND video
// transcription both fire under VoiceEnabled && WhisperURL != "". VideoEnabled
// mirrors the env field but the enrich path doesn't read it.
type mediaConfig struct {
	Enabled      bool
	MaxBytes     int64
	WhisperURL   string
	WhisperModel string
	VoiceEnabled bool
	VideoEnabled bool
	// Bearer yields routd's service:routd ES256 token, presented on the
	// adapter /files download (the adapter gates it via chanlib.Auth). nil →
	// local dev (no AUTHD_URL); the download goes out unauthenticated.
	Bearer func(context.Context) (string, error)
}

// MediaConfig builds the inbound enrichment config for the cmd layer (the
// struct is unexported; the cmd reads the env and passes it to LoopConfig).
func MediaConfig(enabled bool, maxBytes int64, whisperURL, whisperModel string,
	voiceEnabled, videoEnabled bool, bearer func(context.Context) (string, error)) mediaConfig {
	return mediaConfig{
		Enabled: enabled, MaxBytes: maxBytes, WhisperURL: whisperURL, WhisperModel: whisperModel,
		VoiceEnabled: voiceEnabled, VideoEnabled: videoEnabled, Bearer: bearer,
	}
}

// enrichAttachments downloads a message's inbound attachments into the group's
// dated media dir, transcribes voice/video via Whisper, and rewrites msg.Content
// with <attachment .../> blocks (persisting the rewrite via EnrichMessage so
// later turns' observed context sees it too). No-op when media is disabled or the
// message has no attachments. Failures log WARN and skip the offending
// attachment; the turn proceeds.
func (l *Loop) enrichAttachments(ctx context.Context, msg *core.Message, folder string) {
	if !l.media.Enabled || msg.Attachments == "" {
		return
	}
	var atts []chanlib.InboundAttachment
	if err := json.Unmarshal([]byte(msg.Attachments), &atts); err != nil || len(atts) == 0 {
		return
	}

	groupPath, err := l.folders.GroupPath(folder)
	if err != nil {
		slog.Warn("enrich: group path", "folder", folder, "err", err)
		return
	}
	day := time.Now().Format("20060102")
	mediaDir := groupfolder.GroupMediaDir(groupPath, day)
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		slog.Warn("enrich: mkdir", "dir", mediaDir, "err", err)
		return
	}

	langs := readWhisperLanguages(groupPath)
	extra := ""
	for i, att := range atts {
		ext := extFromMime(att.Mime, att.Filename)
		fname := sanitizeFilename(att.Filename)
		if fname == "" {
			fname = fmt.Sprintf("%s-%d%s", msg.ID, i, ext)
		}
		dest := filepath.Join(mediaDir, fname)
		if _, err := os.Stat(dest); err == nil {
			fname = fmt.Sprintf("%s-%d%s", msg.ID, i, ext)
			dest = filepath.Join(mediaDir, fname)
		}

		if att.URL == "" {
			if att.Data == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(att.Data)
			if err != nil {
				slog.Warn("enrich: base64 decode", "err", err)
				continue
			}
			if err := os.WriteFile(dest, raw, 0o644); err != nil {
				slog.Warn("enrich: write base64", "dest", dest, "err", err)
				continue
			}
		} else {
			if err := downloadFile(ctx, att.URL, dest, l.media.Bearer, l.media.MaxBytes); err != nil {
				slog.Warn("enrich: download failed", "url", att.URL, "err", err)
				continue
			}
		}

		displayName := att.Filename
		if displayName == "" {
			displayName = fname
		}
		transcript := ""
		if l.media.VoiceEnabled && l.media.WhisperURL != "" {
			if strings.HasPrefix(att.Mime, "audio/") {
				transcript = whisperTranscribe(ctx, l.media.WhisperURL, l.media.WhisperModel, dest, att.Mime, langs)
			} else if strings.HasPrefix(att.Mime, "video/") {
				if audioPath := extractVideoAudio(dest); audioPath != "" {
					transcript = whisperTranscribe(ctx, l.media.WhisperURL, l.media.WhisperModel, audioPath, "audio/mpeg", langs)
					os.Remove(audioPath)
				}
			}
		}
		containerPath := core.ContainerHome + "/media/" + day + "/" + fname
		if transcript != "" {
			extra += fmt.Sprintf("\n<attachment path=%q mime=%q filename=%q transcript=%q/>",
				containerPath, att.Mime, displayName, transcript)
		} else {
			extra += fmt.Sprintf("\n<attachment path=%q mime=%q filename=%q/>",
				containerPath, att.Mime, displayName)
		}
	}

	if extra == "" {
		return
	}

	msg.Content += extra
	msg.Attachments = ""
	if err := l.db.EnrichMessage(msg.ID, msg.Content); err != nil {
		slog.Warn("enrich: store update failed", "id", msg.ID, "err", err)
	}
}

func downloadFile(ctx context.Context, url, dest string, bearer func(context.Context) (string, error), maxBytes int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if bearer != nil {
		tok, terr := bearer(ctx)
		if terr != nil {
			return fmt.Errorf("service token: %w", terr)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	body := io.Reader(resp.Body)
	if maxBytes > 0 {
		// +1 so a body exactly at the limit reads short and an oversized body
		// trips n > maxBytes instead of truncating.
		body = io.LimitReader(resp.Body, maxBytes+1)
	}
	n, cpErr := io.Copy(f, body)
	if closeErr := f.Close(); cpErr == nil {
		cpErr = closeErr
	}
	if cpErr == nil && maxBytes > 0 && n > maxBytes {
		cpErr = fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	if cpErr != nil {
		os.Remove(dest)
	}
	return cpErr
}

func whisperTranscribe(ctx context.Context, baseURL, model, path, mime string, langs []string) string {
	if len(langs) == 0 {
		langs = []string{""}
	}
	var results []string
	for _, lang := range langs {
		if t := transcribeOnce(ctx, baseURL, model, path, lang, mime); t != "" {
			results = append(results, t)
		}
	}
	return strings.Join(results, "\n")
}

func transcribeOnce(ctx context.Context, baseURL, model, path, lang, mime string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	url := baseURL + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, f)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", mime)
	q := req.URL.Query()
	q.Set("model", model)
	if lang != "" {
		q.Set("language", lang)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var out struct {
		Text string `json:"text"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return strings.TrimSpace(out.Text)
}

func readWhisperLanguages(groupPath string) []string {
	data, err := os.ReadFile(filepath.Join(groupPath, ".whisper-language"))
	if err != nil {
		return nil
	}
	var langs []string
	for _, line := range strings.Split(string(data), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			langs = append(langs, l)
		}
	}
	return langs
}

func extractVideoAudio(videoPath string) string {
	audioPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + "-audio.mp3"
	cmd := exec.Command("ffmpeg", "-y", "-i", videoPath, "-vn", "-acodec", "libmp3lame", "-q:a", "4", audioPath)
	if err := cmd.Run(); err != nil {
		return ""
	}
	return audioPath
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	if name == "." || name == "/" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == '\x00' {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) > 200 {
		ext := filepath.Ext(s)
		s = s[:200-len(ext)] + ext
	}
	return s
}

// preferredExts pins canonical extensions so agents see file types Claude's
// Read tool recognizes. Without this, mime.ExtensionsByType("image/jpeg")
// picks .jfif or .jpe depending on the host's /etc/mime.types, and Claude
// can't natively load those.
var preferredExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"audio/ogg":  ".ogg",
	"audio/mpeg": ".mp3",
	"audio/mp4":  ".m4a",
	"video/mp4":  ".mp4",
}

func extFromMime(mimeType, filename string) string {
	if filename != "" {
		if ext := filepath.Ext(filename); ext != "" {
			return strings.ToLower(ext)
		}
	}
	if ext, ok := preferredExts[mimeType]; ok {
		return ext
	}
	exts, _ := mime.ExtensionsByType(mimeType)
	if len(exts) > 0 {
		return exts[0]
	}
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "." + strings.TrimPrefix(mimeType, "image/")
	case strings.HasPrefix(mimeType, "video/"):
		return "." + strings.TrimPrefix(mimeType, "video/")
	case strings.HasPrefix(mimeType, "audio/"):
		return ".mp3"
	}
	return ".bin"
}
