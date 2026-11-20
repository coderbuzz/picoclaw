package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/groupopen"
	"github.com/sipeed/picoclaw/pkg/routing"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

func TestGroupOpenTool(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Dispatch = &config.DispatchConfig{Rules: []config.DispatchRule{
		{Name: "g1", Agent: "-", OpenAgent: "alumni", When: config.DispatchSelector{Channel: "whatsapp", Chat: "group:1203630000000000@g.us"}},
		{Name: "plain", Agent: "-", When: config.DispatchSelector{Channel: "whatsapp", Chat: "group:other@g.us"}},
	}}
	tool := NewGroupOpenTool(cfg)
	run := func(chat, action string) *ToolResult {
		ctx := toolshared.WithToolContext(context.Background(), "whatsapp", chat)
		return tool.Execute(ctx, map[string]any{"action": action})
	}
	key := routing.OpenKey("whatsapp", "group:1203630000000000@g.us")
	store := groupopen.For(cfg.WorkspacePath())

	if r := run("1203630000000000@g.us", "open"); r.IsError || !strings.Contains(r.ForLLM, "menjawab") || !store.IsOpen(key) {
		t.Fatalf("open = %+v", r)
	}
	if r := run("1203630000000000@g.us", "status"); !strings.Contains(r.ForLLM, "terbuka") {
		t.Fatalf("status = %+v", r)
	}
	if r := run("1203630000000000@g.us", "close"); r.IsError || !strings.Contains(r.ForLLM, "diam") || store.IsOpen(key) {
		t.Fatalf("close = %+v", r)
	}
	for _, chat := range []string{"other@g.us", "unknown@g.us", ""} {
		if r := run(chat, "open"); !r.IsError || !strings.Contains(r.ForLLM, "tidak punya mode diam") {
			t.Fatalf("chat %q must be refused: %+v", chat, r)
		}
	}
	if r := run("1203630000000000@g.us", "bogus"); !r.IsError {
		t.Fatal("bad action must error")
	}
}
