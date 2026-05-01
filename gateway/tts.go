package gateway

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

	"github.com/onvos/arizuko/chanlib"
)

// sendVoice synthesizes text via the configured TTS backend, caches the
// audio under <data>/tts/<sha256(text+voice+model)>.ogg, and dispatches
// to the chat's channel via SendVoice.
//
// Voice resolution precedence (highest first):
//
//  1. explicit `voice` arg from the agent
//  2. `voice:` field in the group's SOUL.md YAML frontmatter
//  3. instance-default Config.TTSVoice (env TTS_VOICE)
//
// Returns ErrUnsupported when the channel cannot deliver voice; the IPC
// layer maps that to a 501 the agent can fall back from (`send` instead).
func (g *Gateway) sendVoice(jid, text, voice, folder string) (string, error) {
	if !g.canSendToJID(jid) {
		return "", nil
	}
	if !g.cfg.TTSEnabled {
		return "", chanlib.Unsupported("send_voice", "tts", "TTS_ENABLED=false on this instance")
	}
	if g.cfg.TTSURL == "" {
		return "", fmt.Errorf("TTS_BASE_URL is empty")
	}
	ch := g.findChannelForJID(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	resolved := g.resolveVoice(voice, folder)
	if resolved == "" {
		return "", fmt.Errorf("no voice configured (set TTS_VOICE or pass voice arg)")
	}
	audioPath, err := g.ttsCacheOrSynthesize(text, resolved, g.cfg.TTSModel)
	if err != nil {
		return "", fmt.Errorf("synthesize: %w", err)
	}
	return ch.SendVoice(jid, audioPath, "")
}

// resolveVoice walks the precedence chain. Empty return → caller errors.
func (g *Gateway) resolveVoice(arg, folder string) string {
	if arg != "" {
		return arg
	}
	if folder != "" {
		if v := readSoulVoice(filepath.Join(g.cfg.GroupsDir, folder, "SOUL.md")); v != "" {
			return v
		}
	}
	return g.cfg.TTSVoice
}

// soulFrontmatterRE captures the leading YAML frontmatter block (--- ... ---).
var soulFrontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)

// soulVoiceRE captures `voice:` lines inside the frontmatter.
var soulVoiceRE = regexp.MustCompile(`(?m)^voice:\s*([^\s#]+)\s*(?:#.*)?$`)

// readSoulVoice returns the `voice:` field of SOUL.md's frontmatter, or
// "" if SOUL.md is missing, has no frontmatter, or no voice key.
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

// ttsCacheOrSynthesize returns the local path of the synthesized audio,
// hitting the on-disk cache by sha256(text+voice+model). Cache lives at
// <project_root>/tts/.
func (g *Gateway) ttsCacheOrSynthesize(text, voice, model string) (string, error) {
	dir := filepath.Join(g.cfg.ProjectRoot, "tts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("tts cache dir: %w", err)
	}
	h := sha256.Sum256([]byte(text + "\x00" + voice + "\x00" + model))
	path := filepath.Join(dir, hex.EncodeToString(h[:])+".ogg")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	audio, err := g.synthesize(text, voice, model)
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

// synthesize POSTs to the OpenAI-compatible /v1/audio/speech endpoint
// and returns the audio bytes. Format is fixed to ogg (response_format)
// because every supported channel encodes its voice primitive as
// ogg/opus (Telegram NewVoice, WhatsApp ptt, Discord audio attachment).
func (g *Gateway) synthesize(text, voice, model string) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{
		"model":           model,
		"voice":           voice,
		"input":           text,
		"response_format": "ogg",
	})
	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.TTSTimeout)
	defer cancel()
	url := strings.TrimRight(g.cfg.TTSURL, "/") + "/v1/audio/speech"
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

// ErrTooLong is returned by sendVoice when the input exceeds the 5000-char
// safety bound (Kokoro starts to time out / chunk awkwardly past that).
var ErrTooLong = errors.New("text too long for voice synthesis (max 5000 chars)")

// validateVoiceText rejects empty / oversize input early so we never
// wedge a long-running TTS round-trip on a doomed payload.
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

// Compile-time guard: keep chanlib.ErrUnsupported import live for the
// channel-side ErrUnsupported propagation in send_voice.
var _ = chanlib.ErrUnsupported
