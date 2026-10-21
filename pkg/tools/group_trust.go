package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	groupTrustTimeout   = 30 * time.Second
	groupTrustMaxOutput = 4000
)

var (
	groupTrustMember  = regexp.MustCompile(`^[0-9]{6,20}$`)
	groupTrustProfile = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	groupTrustStrip   = strings.NewReplacer("+", "", " ", "", "@lid", "", "@s.whatsapp.net", "")
)

// GroupTrustTool lets a group owner agent grant or revoke member trust by running the host's
// `group-agents trust <profile> ...` outside the agent's exec sandbox. No shell, fixed profile,
// member validated as digits only; cmd.Dir is the agent workspace so the script's authorize() passes.
type GroupTrustTool struct {
	profile, command, workspace string
}

func NewGroupTrustTool(cfg *config.AgentGroupTrustConfig, workspace string) *GroupTrustTool {
	cmd := cfg.Command
	if cmd == "" {
		cmd = "group-agents"
	}
	return &GroupTrustTool{profile: cfg.Profile, command: cmd, workspace: workspace}
}

func (t *GroupTrustTool) Name() string { return "group_trust" }

func (t *GroupTrustTool) Description() string {
	return "Grant, revoke or list trusted members of this WhatsApp group profile (owner only). " +
		"`member` is a phone number or the numeric ID (LID) taken from a mention or a quoted message. " +
		"The bot restarts about 20 seconds after a change."
}

func (t *GroupTrustTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"add", "remove", "list"}},
			"member": map[string]any{"type": "string", "description": "Phone number or numeric ID (digits; +, spaces, @lid and @s.whatsapp.net are ignored). Required for add and remove."},
			"name":   map[string]any{"type": "string", "description": "Optional display name for add."},
		},
		"required": []string{"action"},
	}
}

func (t *GroupTrustTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	member, _ := args["member"].(string)
	name, _ := args["name"].(string)
	if !groupTrustProfile.MatchString(t.profile) {
		return ErrorResult("group_trust: invalid profile in config")
	}
	argv := []string{"trust", t.profile, action}
	switch action {
	case "list":
	case "add", "remove":
		member = groupTrustStrip.Replace(strings.TrimSpace(member))
		if !groupTrustMember.MatchString(member) {
			return ErrorResult("group_trust: member must be a phone number or numeric ID (6-20 digits)")
		}
		argv = append(argv, member)
		// name is data for the script, never an option: reject leading "-" and control characters
		if name = strings.TrimSpace(name); action == "add" && name != "" {
			if strings.HasPrefix(name, "-") || strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
				return ErrorResult("group_trust: invalid name")
			}
			argv = append(argv, name)
		}
	default:
		return ErrorResult("group_trust: unknown action " + action)
	}

	ctx, cancel := context.WithTimeout(ctx, groupTrustTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.command, argv...)
	cmd.Dir = t.workspace
	cmd.WaitDelay = time.Second // grandchildren holding the pipe must not outlive the timeout
	out, err := cmd.CombinedOutput()
	text := truncateGroupTrust(strings.TrimSpace(string(out)))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrorResult(fmt.Sprintf("group_trust: timed out after %s\n%s", groupTrustTimeout, text))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ErrorResult(fmt.Sprintf("group_trust: exit code %d\n%s", ee.ExitCode(), text))
		}
		return ErrorResult("group_trust: " + err.Error())
	}
	return NewToolResult(text + "\nexit code 0")
}

func truncateGroupTrust(s string) string {
	if len(s) <= groupTrustMaxOutput {
		return s
	}
	cut := groupTrustMaxOutput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[output truncated]"
}
