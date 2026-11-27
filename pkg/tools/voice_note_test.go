package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

func TestVoiceNoteTool(t *testing.T) {
	tool := NewVoiceNoteTool(&config.AgentVoiceNoteConfig{
		Command:      "edge-tts",
		Voice:        "id-ID-GadisNeural",
		MaxChars:     1000,
		CooldownSecs: 10,
	})
	store := media.NewFileMediaStore()
	tool.SetMediaStore(store)

	tool.runCmd = func(ctx context.Context, name string, args ...string) error {
		outPath := args[len(args)-1]
		return os.WriteFile(outPath, []byte("fake-audio"), 0o600)
	}

	ctx := toolshared.WithToolContext(context.Background(), "whatsapp", "6285745674994-1483054751@g.us")

	// Empty text fails.
	if res := tool.Execute(ctx, map[string]any{"text": "   "}); !res.IsError {
		t.Fatalf("expected error for empty text: %+v", res)
	}

	// Over MaxChars fails.
	tooLong := strings.Repeat("a", 1001)
	if res := tool.Execute(ctx, map[string]any{"text": tooLong}); !res.IsError || !strings.Contains(res.ForLLM, "1001") {
		t.Fatalf("expected max chars error: %+v", res)
	}

	// Invalid voice rejected.
	if res := tool.Execute(ctx, map[string]any{"text": "halo", "voice": "bad;rm -rf /"}); !res.IsError {
		t.Fatalf("expected invalid voice error: %+v", res)
	}

	// Valid execution succeeds with ResponseHandled and 1 media ref.
	res := tool.Execute(ctx, map[string]any{"text": "Halo semua, tes suara Sirkel.", "voice": "id-ID-ArdiNeural"})
	if res.IsError || !res.ResponseHandled || len(res.Media) != 1 {
		t.Fatalf("expected success with media, got: %+v", res)
	}

	// Immediate second call hits cooldown.
	res2 := tool.Execute(ctx, map[string]any{"text": "Tes kedua"})
	if !res2.IsError || !strings.Contains(res2.ForLLM, "tunggu") {
		t.Fatalf("expected cooldown error, got: %+v", res2)
	}
}
