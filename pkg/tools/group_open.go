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
	return "Open or close a silent group for ordinary members. action=open: the bot answers members; action=close: the " +
		"bot is silent for members again; action=status: show the current state. Owner/staff are served either way. " +
		"In a group chat, targets the current group automatically; from DM, optionally pass 'group' (name, agent, or JID)."
}

func (t *GroupOpenTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"open", "close", "status"}},
			"group":  map[string]any{"type": "string", "description": "Optional group/rule name, open_agent, or JID when called from DM."},
		},
		"required": []string{"action"},
	}
}

type silentRuleMatch struct {
	name string
	key  string
	rule config.DispatchRule
}

func (t *GroupOpenTool) silentRules() []silentRuleMatch {
	if t.cfg.Agents.Dispatch == nil {
		return nil
	}
	var out []silentRuleMatch
	for _, r := range t.cfg.Agents.Dispatch.Rules {
		chat := strings.ToLower(strings.TrimSpace(r.When.Chat))
		channel := strings.ToLower(strings.TrimSpace(r.When.Channel))
		if strings.TrimSpace(r.Agent) != routing.SilentAgent || strings.TrimSpace(r.OpenAgent) == "" ||
			!strings.HasPrefix(chat, "group:") || channel == "" {
			continue
		}
		out = append(out, silentRuleMatch{
			name: strings.TrimSpace(r.Name),
			key:  routing.OpenKey(channel, chat),
			rule: r,
		})
	}
	return out
}

// resolveRule finds the target silent rule from the current chat or an explicit group query (for DM).
func (t *GroupOpenTool) resolveRule(channel, chatID, groupQuery string) (name, key, errMsg string) {
	q := strings.ToLower(strings.TrimSpace(groupQuery))
	if q == "" {
		if name, key = t.ruleFor(channel, chatID); key != "" {
			return name, key, ""
		}
		// In a group chat without a silent rule, refuse rather than touching another group.
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(chatID)), "@g.us") {
			return "", "", "Grup ini tidak punya mode diam"
		}
		all := t.silentRules()
		if len(all) == 1 {
			return all[0].name, all[0].key, ""
		}
		if len(all) > 1 {
			var names []string
			for _, m := range all {
				names = append(names, m.name)
			}
			return "", "", "Sebutkan grup yang dimaksud: " + strings.Join(names, ", ")
		}
		return "", "", "Grup ini tidak punya mode diam"
	}

	all := t.silentRules()
	var exact, partial []silentRuleMatch
	for _, m := range all {
		rName := strings.ToLower(m.name)
		rOpen := strings.ToLower(strings.TrimSpace(m.rule.OpenAgent))
		rChat := strings.ToLower(strings.TrimSpace(m.rule.When.Chat))
		rBare := strings.TrimPrefix(rChat, "group:")
		if q == rName || q == rOpen || q == rChat || q == rBare {
			exact = append(exact, m)
		} else if strings.Contains(rName, q) || strings.Contains(rOpen, q) || strings.Contains(rBare, q) {
			partial = append(partial, m)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	if len(matches) == 1 {
		return matches[0].name, matches[0].key, ""
	}
	if len(matches) > 1 {
		var names []string
		for _, m := range matches {
			names = append(names, m.name)
		}
		return "", "", "Grup terlalu umum, pilih salah satu: " + strings.Join(names, ", ")
	}
	return "", "", "Grup dengan mode diam tidak ditemukan: " + strings.TrimSpace(groupQuery)
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
	groupQuery, _ := args["group"].(string)
	name, key, errMsg := t.resolveRule(ToolChannel(ctx), ToolChatID(ctx), groupQuery)
	if errMsg != "" {
		return ErrorResult(errMsg)
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
