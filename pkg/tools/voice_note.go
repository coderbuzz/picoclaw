package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

const (
	defaultVoiceNoteCommand  = "edge-tts"
	defaultVoiceNoteVoice    = "id-ID-GadisNeural"
	defaultVoiceNoteMaxChars = 1000
	defaultVoiceNoteCooldown = 10 * time.Second
	voiceNoteTimeout         = 30 * time.Second
)

var validEdgeVoiceRe = regexp.MustCompile(`^[a-zA-Z]{2,3}-[a-zA-Z]{2,4}-[a-zA-Z0-9]+Neural$`)

// VoiceNoteTool synthesizes short text into an Ogg/Opus voice note via edge-tts + ffmpeg
// and attaches it to the outbound message via MediaStore.
type VoiceNoteTool struct {
	command    string
	voice      string
	maxChars   int
	cooldown   time.Duration
	mediaStore media.MediaStore
	runCmd     func(ctx context.Context, name string, args ...string) error

	mu       sync.Mutex
	lastSent map[string]time.Time
}

func NewVoiceNoteTool(cfg *config.AgentVoiceNoteConfig) *VoiceNoteTool {
	cmd := defaultVoiceNoteCommand
	voice := defaultVoiceNoteVoice
	maxChars := defaultVoiceNoteMaxChars
	cooldown := defaultVoiceNoteCooldown

	if cfg != nil {
		if s := strings.TrimSpace(cfg.Command); s != "" {
			cmd = s
		}
		if s := strings.TrimSpace(cfg.Voice); s != "" && validEdgeVoiceRe.MatchString(s) {
			voice = s
		}
		if cfg.MaxChars > 0 {
			maxChars = cfg.MaxChars
		}
		if cfg.CooldownSecs > 0 {
			cooldown = time.Duration(cfg.CooldownSecs) * time.Second
		} else if cfg.CooldownSecs < 0 {
			cooldown = 0
		}
	}

	return &VoiceNoteTool{
		command:  cmd,
		voice:    voice,
		maxChars: maxChars,
		cooldown: cooldown,
		lastSent: make(map[string]time.Time),
		runCmd: func(ctx context.Context, name string, args ...string) error {
			c := exec.CommandContext(ctx, name, args...)
			if out, err := c.CombinedOutput(); err != nil {
				msg := strings.TrimSpace(string(out))
				if msg != "" {
					return fmt.Errorf("%v: %s", err, msg)
				}
				return err
			}
			return nil
		},
	}
}

func (t *VoiceNoteTool) SetMediaStore(store media.MediaStore) {
	t.mediaStore = store
}

func (t *VoiceNoteTool) Name() string { return "voice_note" }

func (t *VoiceNoteTool) Description() string {
	return fmt.Sprintf(
		"Convert text (max %d characters) into a voice note audio message (Ogg/Opus) and send it to the current chat. "+
			"Supports Indonesian (id-ID-GadisNeural, id-ID-ArdiNeural), Javanese (jv-ID-SitiNeural, jv-ID-DimasNeural), "+
			"Sundanese (su-ID-TutiNeural), English (en-US-JennyNeural), Hindi (hi-IN-SwaraNeural), etc.",
		t.maxChars,
	)
}

func (t *VoiceNoteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("Text to speak (plain text, max %d characters).", t.maxChars),
			},
			"voice": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("Optional Edge TTS voice name (default: %s).", t.voice),
			},
		},
		"required": []string{"text"},
	}
}

func (t *VoiceNoteTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	text, _ := args["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrorResult("voice_note: text wajib diisi")
	}
	runeCount := utf8.RuneCountInString(text)
	if runeCount > t.maxChars {
		return ErrorResult(fmt.Sprintf("voice_note: teks terlalu panjang (%d karakter, maksimal %d karakter); ringkas dulu.", runeCount, t.maxChars))
	}

	voice := t.voice
	if rawVoice, _ := args["voice"].(string); strings.TrimSpace(rawVoice) != "" {
		v := strings.TrimSpace(rawVoice)
		if !validEdgeVoiceRe.MatchString(v) {
			return ErrorResult("voice_note: format nama voice tidak valid (contoh: id-ID-GadisNeural)")
		}
		voice = v
	}

	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)
	if channel == "" || chatID == "" {
		return ErrorResult("voice_note: channel/chat tujuan tidak tersedia")
	}
	if t.mediaStore == nil {
		return ErrorResult("voice_note: media store belum tersedia")
	}

	if errMsg := t.checkCooldown(channel + ":" + chatID); errMsg != "" {
		return ErrorResult(errMsg)
	}

	tmpDir, err := os.MkdirTemp("", "picoclaw-voicenote-*")
	if err != nil {
		return ErrorResult("voice_note: gagal membuat folder sementara: " + err.Error())
	}
	mp3Path := filepath.Join(tmpDir, "tts.mp3")
	oggName := fmt.Sprintf("voice-%d.ogg", time.Now().UnixNano())
	oggPath := filepath.Join(tmpDir, oggName)

	runCtx, cancel := context.WithTimeout(ctx, voiceNoteTimeout)
	defer cancel()

	if err := t.runCmd(runCtx, t.command, "--text", text, "--voice", voice, "--write-media", mp3Path); err != nil {
		_ = os.RemoveAll(tmpDir)
		return ErrorResult("voice_note: gagal membuat suara (edge-tts): " + err.Error())
	}
	if err := t.runCmd(runCtx, "ffmpeg", "-y", "-loglevel", "error", "-i", mp3Path, "-c:a", "libopus", "-b:a", "24k", "-ar", "16000", "-ac", "1", oggPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return ErrorResult("voice_note: gagal konversi audio (ffmpeg): " + err.Error())
	}
	_ = os.Remove(mp3Path)

	scope := fmt.Sprintf("tool:voice_note:%s:%s", channel, chatID)
	ref, err := t.mediaStore.Store(oggPath, media.MediaMeta{
		Filename:      oggName,
		ContentType:   "audio/ogg; codecs=opus",
		Source:        "tool:voice_note",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, scope)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return ErrorResult("voice_note: gagal menyimpan media: " + err.Error())
	}

	t.recordSent(channel + ":" + chatID)
	return MediaResult(fmt.Sprintf("Voice note dikirim (%d karakter, %s).", runeCount, voice), []string{ref}).WithResponseHandled()
}

func (t *VoiceNoteTool) checkCooldown(key string) string {
	if t.cooldown <= 0 {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.lastSent[key]; ok {
		if rem := t.cooldown - time.Since(prev); rem > 0 {
			return fmt.Sprintf("voice_note: tunggu %d detik lagi sebelum kirim voice note berikutnya", int(rem.Seconds())+1)
		}
	}
	return ""
}

func (t *VoiceNoteTool) recordSent(key string) {
	if t.cooldown <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastSent[key] = time.Now()
}
