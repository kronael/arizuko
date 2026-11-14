package routd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/retry"
	"github.com/kronael/arizuko/store"
)

// mediaConfig is routd's slice of the media/transcription env. Audio AND video
// transcription fire under VoiceEnabled && WhisperURL != ""; the enrich path
// does not read VideoEnabled.
type mediaConfig struct {
	Enabled      bool
	MaxBytes     int64
	WhisperURL   string
	WhisperModel string
	VoiceEnabled bool
	VideoEnabled bool
	// Bearer yields routd's service token for the adapter /files download;
	// nil → local dev, unauthenticated.
	Bearer func(context.Context) (string, error)
	// Adapters names the hosts that may receive Bearer: a download from any
	// other host carries no Authorization header.
	Adapters *chanreg.Registry
}

// MediaConfig builds the inbound enrichment config for the cmd layer.
func MediaConfig(enabled bool, maxBytes int64, whisperURL, whisperModel string,
	voiceEnabled, videoEnabled bool, bearer func(context.Context) (string, error),
	adapters *chanreg.Registry) mediaConfig {
	return mediaConfig{
		Enabled: enabled, MaxBytes: maxBytes, WhisperURL: whisperURL, WhisperModel: whisperModel,
		VoiceEnabled: voiceEnabled, VideoEnabled: videoEnabled, Bearer: bearer, Adapters: adapters,
	}
}

// bearerFor returns the service-token source for a download from rawURL: Bearer
// when rawURL names a registered adapter's own scheme and host, nil otherwise.
func (c mediaConfig) bearerFor(rawURL string) func(context.Context) (string, error) {
	if c.Adapters == nil {
		return nil
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	for _, entry := range c.Adapters.All() {
		adapter, err := url.Parse(entry.URL)
		if err == nil && adapter.Scheme == target.Scheme && adapter.Host == target.Host {
			return c.Bearer
		}
	}
	return nil
}

func (l *Loop) enrichAttachments(ctx context.Context, msg *core.Message, folder string, deferrable bool) error {
	if !l.media.Enabled || msg.Attachments == "" {
		return nil
	}
	snapshot, found, err := l.db.store().ReadAttachmentRetry(ctx, *msg)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("attachment message missing: %s", msg.ID)
	}
	caller := msg
	msg = &snapshot.Message
	if snapshot.State == "failed" {
		*caller = *msg
		return nil
	}
	now := time.Now()
	if deferrable && snapshot.State == "pending" && snapshot.NextAt.After(now) {
		return &attachmentDeferred{at: snapshot.NextAt}
	}
	var atts []chanlib.InboundAttachment
	malformed := json.Unmarshal([]byte(msg.Attachments), &atts) != nil || len(atts) == 0
	if malformed {
		atts = []chanlib.InboundAttachment{{}}
	}
	groupPath, err := l.folders.GroupPath(folder)
	if err != nil {
		return err
	}
	day := now.Format("20060102")
	mediaDir := groupfolder.GroupMediaDir(groupPath, day)
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return err
	}
	langs := readWhisperLanguages(groupPath)
	var extra strings.Builder
	var created []string
	var terminal *retry.Decision
	cleanup := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
	}
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
		var failure retry.Failure
		if malformed || att.URL == "" && att.Data == "" {
			failure.Kind = "permanent"
		} else if att.URL == "" {
			raw, err := base64.StdEncoding.DecodeString(att.Data)
			if err != nil {
				failure.Kind = "permanent"
			} else if err = os.WriteFile(dest, raw, 0o644); err != nil {
				failure.Kind = "permanent"
			} else {
				created = append(created, dest)
			}
		} else if err := downloadFile(ctx, att.URL, dest, l.media.bearerFor(att.URL), l.media.MaxBytes); err != nil {
			var fetch *attachmentFetchError
			if errors.As(err, &fetch) {
				failure = fetch.failure
			} else if errors.Is(err, errFetchPermanent) {
				failure.Kind = "permanent"
			} else if errors.Is(err, errFetchTimeout) {
				failure.Kind = "timeout"
			} else {
				failure = retry.HTTPFailure(0, "", err, now)
			}
		} else {
			created = append(created, dest)
		}
		if failure.Kind != "" {
			decision := retry.Next(snapshot.Failures, failure, now, time.Time{})
			if !deferrable {
				decision.Stop = true
				decision.NextAt = time.Time{}
			}
			if !decision.Stop {
				cleanup()
				committed, err := l.db.store().DeferAttachments(ctx, snapshot, decision, "")
				if err != nil {
					return err
				}
				if !committed {
					return fmt.Errorf("attachment changed during enrichment")
				}
				return &attachmentDeferred{at: decision.NextAt}
			}
			terminal = &decision
			extra.WriteString(unavailableAttachment(att, retry.Notice("Attachment unavailable", decision)))
			continue
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
					_ = os.Remove(audioPath)
				}
			}
		}
		containerPath := core.ContainerHome + "/media/" + day + "/" + fname
		if transcript != "" {
			fmt.Fprintf(&extra, "\n<attachment path=%q mime=%q filename=%q transcript=%q/>", containerPath, att.Mime, displayName, transcript)
		} else {
			fmt.Fprintf(&extra, "\n<attachment path=%q mime=%q filename=%q/>", containerPath, att.Mime, displayName)
		}
	}
	content := msg.Content + extra.String()
	var committed bool
	if terminal != nil {
		committed, err = l.db.store().DeferAttachments(ctx, snapshot, *terminal, content)
	} else {
		committed, err = l.db.store().CompleteAttachments(ctx, snapshot, content)
	}
	if err != nil || !committed {
		cleanup()
		if err != nil {
			return err
		}
		return fmt.Errorf("attachment changed during enrichment")
	}
	msg.Content = content
	if terminal == nil {
		msg.Attachments = ""
	}
	row, err := store.IndexedFrom(*msg)
	if err != nil {
		return err
	}
	l.db.search.Put(row)
	*caller = *msg
	return nil
}

type attachmentDeferred struct{ at time.Time }

func (e *attachmentDeferred) Error() string { return errFetchDeferred.Error() }
func (e *attachmentDeferred) Unwrap() error { return errFetchDeferred }

type attachmentFetchError struct {
	failure retry.Failure
	err     error
}

func (e *attachmentFetchError) Error() string { return e.err.Error() }
func (e *attachmentFetchError) Unwrap() error { return e.err }

// unavailableAttachment is the ONE renderer for a file that did not arrive:
// the agent is TOLD, otherwise the turn runs on a message that names a file it
// never received and nobody can see anything is missing (BUGS L163).
func unavailableAttachment(att chanlib.InboundAttachment, why string) string {
	name := att.Filename
	if name == "" {
		name = "(unnamed)"
	}
	return fmt.Sprintf("\n<attachment filename=%q mime=%q unavailable=%q/>",
		name, att.Mime, why)
}

// downloadClient fetches inbound attachments. It has no whole-request timeout:
// downloadFile bounds the response headers, then the body by its size.
var downloadClient = &http.Client{}

var (
	fetchHeaderTimeout = 45 * time.Second
	fetchBodyBase      = 30 * time.Second
)

const (
	// fetchBodyRate is the slowest body transfer, in bytes per second, a
	// download is given time for.
	fetchBodyRate = 256 << 10
)

var (
	errFetchPermanent = errors.New("the file is refused")
	errFetchTimeout   = errors.New("attachment download timed out")
	errFetchDeferred  = errors.New("an attachment download failed transiently")
)

// downloadFile writes url's body to dest. An error wrapping errFetchPermanent
// is one a retry cannot fix: a 4xx answer, a body over maxBytes, or a local
// write failure. Every other error is transient.
func downloadFile(ctx context.Context, url, dest string, bearer func(context.Context) (string, error), maxBytes int64) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	deadline := time.AfterFunc(fetchHeaderTimeout, func() { cancel(errFetchTimeout) })
	defer deadline.Stop()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errFetchPermanent, err)
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
	resp, err := downloadClient.Do(req)
	if err != nil {
		return fetchCause(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		failure := retry.HTTPFailure(resp.StatusCode, resp.Header.Get("Retry-After"), nil, time.Now())
		err := fmt.Errorf("HTTP %d", resp.StatusCode)
		if failure.Kind == "permanent" {
			err = fmt.Errorf("%w: %w", errFetchPermanent, err)
		}
		return &attachmentFetchError{failure: failure, err: err}
	}
	size := resp.ContentLength
	if size < 0 {
		size = maxBytes
	}
	deadline.Reset(fetchBodyBase + time.Duration(size/fetchBodyRate)*time.Second)
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("%w: %w", errFetchPermanent, err)
	}
	body := io.Reader(resp.Body)
	if maxBytes > 0 {
		// +1 so a body exactly at the limit reads short and an oversized body
		// trips n > maxBytes instead of truncating.
		body = io.LimitReader(resp.Body, maxBytes+1)
	}
	n, cpErr := io.Copy(f, body)
	if cpErr != nil {
		cpErr = fetchCause(ctx, cpErr)
	}
	if closeErr := f.Close(); cpErr == nil && closeErr != nil {
		cpErr = fmt.Errorf("%w: %w", errFetchPermanent, closeErr)
	}
	if cpErr == nil && maxBytes > 0 && n > maxBytes {
		cpErr = fmt.Errorf("%w: file exceeds %d bytes", errFetchPermanent, maxBytes)
	}
	if cpErr != nil {
		_ = os.Remove(dest)
	}
	return cpErr
}

// fetchCause names the deadline that cut a download, which the transport
// reports only as a cancelled context.
func fetchCause(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("%w: %w", cause, err)
	}
	return err
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
	defer func() { _ = f.Close() }()

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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return ""
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// The turn then carries the attachment with no transcript, which reads to
		// the agent as a voice note that said nothing.
		slog.Warn("whisper response will not parse", "url", url, "err", err)
		return ""
	}
	return strings.TrimSpace(out.Text)
}

func readWhisperLanguages(groupPath string) []string {
	data, err := os.ReadFile(filepath.Join(groupPath, ".whisper-language"))
	if err != nil {
		return nil
	}
	var langs []string
	for line := range strings.SplitSeq(string(data), "\n") {
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

// preferredExts pins canonical extensions the agent's Read tool recognizes:
// mime.ExtensionsByType("image/jpeg") picks .jfif or .jpe depending on the
// host's /etc/mime.types, and Claude cannot load those.
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
