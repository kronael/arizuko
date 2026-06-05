package routd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// ttsConfig is routd's slice of the TTS_* env (mirror of core.Config's TTS
// fields). CacheDir is where synthesized Opus is memoized (gated uses
// ProjectRoot/tts; routd's cmd points it at DATA_DIR/tts).
type ttsConfig struct {
	Enabled  bool
	URL      string
	Voice    string
	Model    string
	Timeout  time.Duration
	CacheDir string
}

// TTSConfig builds the send_voice synthesis config for the cmd layer (the
// struct is unexported; the cmd reads the TTS_* env and passes it to SetTTS).
func TTSConfig(enabled bool, url, voice, model string, timeout time.Duration, cacheDir string) ttsConfig {
	return ttsConfig{Enabled: enabled, URL: url, Voice: voice, Model: model, Timeout: timeout, CacheDir: cacheDir}
}

// httpClient is routd's shared HTTP client for the TTS + Whisper + media
// download calls (gateway-private in gated; reimplemented here for the port).
var httpClient = &http.Client{Timeout: 30 * time.Second}

// sendVoice synthesizes text → Opus via the TTS service and hands the cached
// audio path to the Deliverer for the owning adapter to upload. Faithful port
// of gateway.sendVoice: validate before TTS, refuse when TTS is off / URL is
// empty / no voice resolves, memoize on (text, voice, model). Returns the
// platform id. The ipc tool layer persists the bot row (recordOutbound), so
// this must NOT persist — deliver-only, like mcpDeliver.
func (s *Server) sendVoice(jid, text, voice, folder, threadID string) (string, error) {
	if s.deliver == nil {
		return "", nil
	}
	if err := validateVoiceText(text); err != nil {
		return "", err
	}
	if !s.tts.Enabled {
		return "", chanlib.Unsupported("send_voice", "tts", "TTS_ENABLED=false on this instance")
	}
	if s.tts.URL == "" {
		return "", fmt.Errorf("TTS_BASE_URL is empty")
	}
	resolved := s.resolveVoice(voice, folder)
	if resolved == "" {
		return "", fmt.Errorf("no voice configured (set TTS_VOICE or pass voice arg)")
	}
	audioPath, err := s.tts.cacheOrSynthesize(text, resolved, s.tts.Model)
	if err != nil {
		return "", fmt.Errorf("synthesize: %w", err)
	}
	return s.deliver.SendVoice(jid, audioPath, "", threadID)
}

func (s *Server) resolveVoice(arg, folder string) string {
	if arg != "" {
		return arg
	}
	if folder != "" && s.groupsDir != "" {
		if v := readSoulVoice(filepath.Join(s.groupsDir, folder, "PERSONA.md")); v != "" {
			return v
		}
	}
	return s.tts.Voice
}

var soulFrontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)
var soulVoiceRE = regexp.MustCompile(`(?m)^voice:\s*([^\s#]+)\s*(?:#.*)?$`)

func readSoulVoice(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fm := soulFrontmatterRE.FindSubmatch(data)
	if fm == nil {
		return ""
	}
	m := soulVoiceRE.FindSubmatch(fm[1])
	if m == nil {
		return ""
	}
	return strings.Trim(string(m[1]), `"'`)
}

func (c ttsConfig) cacheOrSynthesize(text, voice, model string) (string, error) {
	dir := c.CacheDir
	if dir == "" {
		dir = "tts"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("tts cache dir: %w", err)
	}
	h := sha256.Sum256([]byte(text + "\x00" + voice + "\x00" + model))
	path := filepath.Join(dir, hex.EncodeToString(h[:])+".ogg")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	audio, err := c.synthesize(text, voice, model)
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, audio, 0o644); err != nil {
		return "", fmt.Errorf("write tts cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("rename tts cache: %w", err)
	}
	return path, nil
}

// synthesize POSTs to the OpenAI-compatible /v1/audio/speech endpoint and
// returns the audio bytes. Format is fixed to opus (Opus in an Ogg container)
// because every supported channel encodes its voice primitive that way
// (Telegram NewVoice, WhatsApp ptt, Discord audio attachment). Faithful port
// of gateway.synthesize.
func (c ttsConfig) synthesize(text, voice, model string) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{
		"model":           model,
		"voice":           voice,
		"input":           text,
		"response_format": "opus",
	})
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	url := strings.TrimRight(c.URL, "/") + "/v1/audio/speech"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
	}
	return io.ReadAll(resp.Body)
}

// ErrTooLong: Kokoro times out past 5000 chars.
var ErrTooLong = errors.New("text too long for voice synthesis (max 5000 chars)")

func validateVoiceText(text string) error {
	t := strings.TrimSpace(text)
	if t == "" {
		return fmt.Errorf("text is empty")
	}
	if len(t) > 5000 {
		return ErrTooLong
	}
	return nil
}
