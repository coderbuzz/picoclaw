package tools

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/groupopen"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// GroupOpenTool opens or closes the silent dispatch rule of the current group so ordinary members can (or can no
// longer) talk to the bot. The chat comes from the turn, never from arguments.
type GroupOpenTool struct {
	cfg *config.Config
}

func NewGroupOpenTool(cfg *config.Config) *GroupOpenTool { return &GroupOpenTool{cfg: cfg} }

func (t *GroupOpenTool) Name() string { return "group_open" }

func (t *GroupOpenTool) Description() string {
	return "Open or close this group for ordinary members. action=open: the bot answers members; action=close: the " +
		"bot is silent for members again; action=status: show the current state. Owner/staff are served either way."
}

func (t *GroupOpenTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"open", "close", "status"}},
		},
		"required": []string{"action"},
	}
}

// ruleFor finds the silent rule with open_agent that covers this channel and chat id (any chat type prefix).
func (t *GroupOpenTool) ruleFor(channel, chatID string) (name, key string) {
	if t.cfg.Agents.Dispatch == nil || chatID == "" {
		return "", ""
	}
	suffix := ":" + strings.ToLower(strings.TrimSpace(chatID))
	channel = strings.ToLower(strings.TrimSpace(channel))
	for _, r := range t.cfg.Agents.Dispatch.Rules {
		chat := strings.ToLower(strings.TrimSpace(r.When.Chat))
		if strings.TrimSpace(r.Agent) != routing.SilentAgent || strings.TrimSpace(r.OpenAgent) == "" ||
			!strings.HasPrefix(chat, "group:") || !strings.HasSuffix(chat, suffix) ||
			strings.ToLower(strings.TrimSpace(r.When.Channel)) != channel {
			continue
		}
		return r.Name, routing.OpenKey(channel, chat)
	}
	return "", ""
}

func (t *GroupOpenTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	if action != "open" && action != "close" && action != "status" {
		return ErrorResult("group_open: action must be open, close or status")
	}
	name, key := t.ruleFor(ToolChannel(ctx), ToolChatID(ctx))
	if key == "" {
		return ErrorResult("Grup ini tidak punya mode diam")
	}
	store := groupopen.For(t.cfg.WorkspacePath())
	if action != "status" {
		if err := store.Set(key, action == "open"); err != nil {
			return ErrorResult("group_open: cannot save state: " + err.Error())
		}
		logger.InfoCF("tool", "group_open", map[string]any{"action": action, "rule": name})
	}
	if store.IsOpen(key) {
		if action == "open" {
			return NewToolResult("Bot kini menjawab anggota")
		}
		return NewToolResult("Status: bot menjawab anggota (terbuka)")
	}
	if action == "close" {
		return NewToolResult("Bot kini diam untuk anggota")
	}
	return NewToolResult("Status: bot diam untuk anggota (tertutup)")
}
