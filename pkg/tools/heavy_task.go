package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
)

const heavyTaskMaxOutput = 12000

// HeavyTaskTool runs the host's heavy-task script for a sandboxed agent: the job (pi + browser, yt-dlp, ...) runs on
// the InstaBox machine only (HEAVY_TASK_REMOTE_ONLY=1, never on this small VM), the model comes from a per-agent
// allowlist, and the job's files are copied into the agent workspace under out/heavy-<time>/.
type HeavyTaskTool struct {
	command, workspace string
	models             []string
	timeout            time.Duration
}

func NewHeavyTaskTool(cfg *config.AgentHeavyTaskConfig, workspace string) *HeavyTaskTool {
	t := &HeavyTaskTool{command: cfg.Command, workspace: workspace, models: cfg.Models,
		timeout: time.Duration(cfg.TimeoutMinutes) * time.Minute}
	if t.command == "" {
		t.command = "heavy-task"
	}
	if t.timeout <= 0 {
		t.timeout = 20 * time.Minute
	}
	return t
}

func (t *HeavyTaskTool) Name() string { return "heavy_task" }

func (t *HeavyTaskTool) Description() string {
	return "Run one heavy job on a separate cloud machine with its own AI agent and shell: real browser (agent-browser), " +
		"downloading and transcribing video/audio (yt-dlp, Groq Whisper), scraping, long multi-step research. " +
		"Write a self-contained instruction (goal, inputs, expected output, files to save). Usually 15-60 seconds, up to " +
		"several minutes; one job at a time. Files the job saves come back under out/heavy-<time>/ in your workspace. " +
		"The default model is the first in the list; pick a stronger one only for analysis, comparison of many sources, " +
		"long research or important reports."
}

func (t *HeavyTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"instruction": map[string]any{"type": "string", "description": "Self-contained instruction for the job."},
			"model":       map[string]any{"type": "string", "enum": t.models, "description": "Model for the job; default " + t.models[0] + "."},
		},
		"required": []string{"instruction"},
	}
}

func (t *HeavyTaskTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	instr, _ := args["instruction"].(string)
	if instr = strings.TrimSpace(instr); instr == "" {
		return ErrorResult("heavy_task: instruction is required")
	}
	model, _ := args["model"].(string)
	if model == "" {
		model = t.models[0]
	}
	if !slices.Contains(t.models, model) {
		return ErrorResult("heavy_task: model not allowed for this agent; use one of " + strings.Join(t.models, ", "))
	}
	rel := filepath.Join("out", "heavy-"+time.Now().Format("20060102-150405"))

	// the script has its own deadline (minutes, plus polling slack); this one only stops a hung script
	ctx, cancel := context.WithTimeout(ctx, t.timeout+5*time.Minute)
	defer cancel()
	minutes := fmt.Sprint(int(t.timeout / time.Minute))
	cmd := exec.CommandContext(ctx, t.command, "--model", model, "--timeout", minutes, "--", instr)
	cmd.Dir = t.workspace
	cmd.Env = append(os.Environ(), "HEAVY_TASK_REMOTE_ONLY=1", "HEAVY_TASK_COPY_TO="+filepath.Join(t.workspace, rel))
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	text := tailHeavyTask(strings.TrimSpace(string(out)))
	if _, statErr := os.Stat(filepath.Join(t.workspace, rel)); statErr == nil {
		text += "\nfiles: " + filepath.ToSlash(rel) + "/"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrorResult("heavy_task: timed out\n" + text)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ErrorResult(fmt.Sprintf("heavy_task: exit code %d\n%s", ee.ExitCode(), text))
		}
		return ErrorResult("heavy_task: " + err.Error())
	}
	return NewToolResult(text)
}

// tailHeavyTask keeps the end of the output, where the job's answer is.
func tailHeavyTask(s string) string {
	if len(s) <= heavyTaskMaxOutput {
		return s
	}
	cut := len(s) - heavyTaskMaxOutput
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "[output truncated]\n" + s[cut:]
}
